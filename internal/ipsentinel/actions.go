package ipsentinel

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

type ExecutorAction struct {
	ID                      string  `json:"id"`
	Kind                    string  `json:"kind"`
	Binding                 Binding `json:"binding"`
	LeaseID                 string  `json:"lease_id,omitempty"`
	RequestID               string  `json:"request_id,omitempty"`
	Attempt                 int64   `json:"attempt,omitempty"`
	ConfigRevision          int64   `json:"config_revision,omitempty"`
	ConfigSHA256            string  `json:"config_sha256,omitempty"`
	Config                  *Config `json:"config,omitempty"`
	ExpectedRevision        int64   `json:"expected_revision,omitempty"`
	JobID                   string  `json:"job_id,omitempty"`
	ServiceID               string  `json:"service_id,omitempty"`
	Purpose                 string  `json:"purpose,omitempty"`
	Phase                   string  `json:"phase,omitempty"`
	NotBefore               string  `json:"not_before,omitempty"`
	ExpiresAt               string  `json:"expires_at,omitempty"`
	TimeoutMS               int64   `json:"timeout_ms,omitempty"`
	ExpectedPauseGeneration int64   `json:"expected_pause_generation,omitempty"`
}

type ActionResult struct {
	ID         string         `json:"id"`
	LeaseID    string         `json:"lease_id"`
	Accepted   bool           `json:"accepted"`
	NotStarted bool           `json:"not_started,omitempty"`
	Retryable  bool           `json:"retryable"`
	Message    string         `json:"message"`
	ErrorCode  string         `json:"error_code,omitempty"`
	Config     *ConfigReceipt `json:"config,omitempty"`
	Validation *Validation    `json:"validation,omitempty"`
	Run        *Run           `json:"run,omitempty"`
	Control    *Control       `json:"control,omitempty"`
}

type ActionView struct {
	ExecutorAction
	Status string        `json:"status"`
	Result *ActionResult `json:"result,omitempty"`
	Run    *Run          `json:"run,omitempty"`
}

type ActionTransport interface {
	ExecutorAction(context.Context, Hello) (ExecutorAction, error)
	FinishExecutorAction(context.Context, ActionResult) error
}

type Controller interface {
	ConfigValidate(context.Context, string, Config) (Validation, error)
	ConfigApply(context.Context, string, Fence, int64, Config) (ConfigReceipt, error)
	RunSubmit(context.Context, string, JobParams) (Admission, error)
	RunGet(context.Context, string, string) (Run, error)
	RunCancel(context.Context, string, Fence, string) (Run, error)
	ControlPause(context.Context, string, Fence) (Control, error)
	ControlResume(context.Context, string, Fence, int64) (Control, error)
}

func HasFeature(hello Hello, feature string) bool { return slices.Contains(hello.Features, feature) }

func (action ExecutorAction) Job() JobParams {
	return JobParams{
		Fence: Fence{ExecutorInstanceID: action.Binding.ExecutorInstanceID, Epoch: action.Binding.Epoch},
		JobID: action.JobID, Config: ConfigRef{Revision: action.ConfigRevision, SHA256: action.ConfigSHA256},
		ServiceID: action.ServiceID, Purpose: action.Purpose, Phase: action.Phase,
		NotBefore: action.NotBefore, ExpiresAt: action.ExpiresAt, TimeoutMS: action.TimeoutMS,
	}
}

func (action ExecutorAction) Validate() error {
	if !ValidUUID(action.ID) || action.Binding.Mode != ModeManaged {
		return semanticErr("managed action identity required")
	}
	if err := action.Binding.Validate(); err != nil {
		return err
	}
	fence := Fence{ExecutorInstanceID: action.Binding.ExecutorInstanceID, Epoch: action.Binding.Epoch}
	var params any
	switch action.Kind {
	case MethodConfigValidate:
		if action.Config == nil {
			return semanticErr("missing config")
		}
		params = map[string]any{"config": action.Config}
	case MethodConfigApply:
		if action.Config == nil {
			return semanticErr("missing config")
		}
		params = map[string]any{"fence": fence, "config": action.Config, "expected_revision": action.ExpectedRevision}
	case MethodRunSubmit:
		params = action.Job()
	case MethodRunCancel:
		params = map[string]any{"fence": fence, "job_id": action.JobID, "reason": "operator"}
	case MethodControlPause:
		params = map[string]any{"fence": fence, "reason": "operator"}
	case MethodControlResume:
		params = map[string]any{"fence": fence, "expected_pause_generation": action.ExpectedPauseGeneration}
	default:
		return semanticErr("unsupported action")
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(Request{Protocol: Protocol, RequestID: action.ID, Method: action.Kind, Target: action.Binding.Target, Params: rawParams})
	if err != nil {
		return err
	}
	_, err = ValidateRequest(raw, nil)
	return err
}

func ExecuteAction(ctx context.Context, client Controller, transport ActionTransport, binding Binding, hello Hello) error {
	if binding.Mode != ModeManaged || hello.Status.Control.Mode != ModeManaged {
		return nil
	}
	action, err := transport.ExecutorAction(ctx, hello)
	if err != nil || action.ID == "" {
		return err
	}
	if action.Binding != binding || !ValidUUID(action.LeaseID) || !ValidUUID(action.RequestID) {
		return semanticErr("action authorization or lease mismatch")
	}
	if err := action.Validate(); err != nil {
		return err
	}
	result, err := performAction(ctx, client, action, hello, time.Now())
	if err != nil {
		var rejected *RPCError
		if !errors.As(err, &rejected) {
			return err
		}
		result.ErrorCode = rejected.Err.Code
		result.Message = rejected.Err.Code
		result.Retryable = rejected.Err.Retryable && slices.Contains([]string{
			ErrCodeBusy, ErrCodeNotDue, ErrCodeUnhealthy, ErrCodeStoragePressure,
		}, rejected.Err.Code)
	}
	result.ID, result.LeaseID = action.ID, action.LeaseID
	return transport.FinishExecutorAction(ctx, result)
}

func performAction(ctx context.Context, client Controller, action ExecutorAction, hello Hello, now time.Time) (ActionResult, error) {
	var result ActionResult
	fence := action.Job().Fence
	switch action.Kind {
	case MethodConfigValidate, MethodConfigApply:
		if !HasFeature(hello, "config") {
			return result, &RPCError{Err: Error{Code: ErrCodePolicyDenied, Message: "config capability unavailable"}}
		}
		if action.Kind == MethodConfigValidate {
			validation, err := client.ConfigValidate(ctx, "", *action.Config)
			result.Validation, result.Accepted = &validation, err == nil && validation.Valid
			return result, err
		}
		validation, err := client.ConfigValidate(ctx, "", *action.Config)
		if err != nil {
			return result, err
		}
		if !validation.Valid {
			return result, &RPCError{Method: MethodConfigApply, Err: Error{Code: ErrCodeInvalidConfig, Message: "config rejected by executor"}}
		}
		receipt, err := client.ConfigApply(ctx, action.RequestID, fence, action.ExpectedRevision, *action.Config)
		if err != nil {
			return result, err
		}
		digest, err := ConfigSHA256(*action.Config)
		if err != nil {
			return result, err
		}
		if receipt.Config.SHA256 != digest || receipt.Config.Revision < action.ExpectedRevision || receipt.Config.Revision > action.ExpectedRevision+1 {
			return result, semanticErr("config receipt mismatch")
		}
		result.Config, result.Accepted = &receipt, true
	case MethodRunSubmit:
		run, err := client.RunGet(ctx, "", action.JobID)
		if err == nil {
			result.Run, result.Accepted = &run, true
			break
		}
		var rejected *RPCError
		if !errors.As(err, &rejected) || rejected.Err.Code != ErrCodeUnknownJob {
			return result, err
		}
		if !HasFeature(hello, "run") || hello.Status.Health != "ready" || hello.Status.Control.Paused || hello.Status.Control.Draining || hello.Status.Config == nil || *hello.Status.Config != action.Job().Config {
			return result, &RPCError{Err: Error{Code: ErrCodePolicyDenied, Message: "executor no longer eligible for admission"}}
		}
		end, err := ParseContractTime(action.ExpiresAt)
		if err != nil {
			return result, err
		}
		if !now.Before(end) {
			return result, &RPCError{Method: MethodRunSubmit, Err: Error{Code: ErrCodeExpired, Message: "original admission window expired"}}
		}
		admission, err := client.RunSubmit(ctx, action.RequestID, action.Job())
		if err != nil {
			return result, err
		}
		result.Run, result.Accepted = &admission.Run, true
	case MethodRunCancel:
		run, err := client.RunGet(ctx, "", action.JobID)
		if err != nil {
			var rpcErr *RPCError
			if errors.As(err, &rpcErr) && rpcErr.Err.Code == ErrCodeUnknownJob {
				result.Accepted, result.NotStarted = true, true
				break
			}
			return result, err
		}
		_ = run
		run, err = client.RunCancel(ctx, action.RequestID, fence, action.JobID)
		if err != nil {
			return result, err
		}
		result.Run, result.Accepted = &run, true
	case MethodControlPause:
		control, err := client.ControlPause(ctx, action.RequestID, fence)
		if err != nil {
			return result, err
		}
		result.Control, result.Accepted = &control, control.Paused
	case MethodControlResume:
		control, err := client.ControlResume(ctx, action.RequestID, fence, action.ExpectedPauseGeneration)
		if err != nil {
			return result, err
		}
		result.Control, result.Accepted = &control, !control.Paused
	}
	return result, nil
}
