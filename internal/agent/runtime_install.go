package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"time"

	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type RuntimeInstallConfig struct {
	CCURL            string            `json:"cc_url"`
	CCKeysPath       string            `json:"cc_keys_path"`
	ProbeVersion     string            `json:"probe_version"`
	StagingDir       string            `json:"staging_dir"`
	InstallRoot      string            `json:"install_root"`
	TrustedInstaller string            `json:"trusted_installer"`
	StateDir         string            `json:"state_dir"`
	ConfigPath       string            `json:"config_path"`
	PolicyPath       string            `json:"policy_path"`
	Unit             string            `json:"unit"`
	Target           ipsentinel.Target `json:"target"`
}

type RuntimeTransport interface {
	RuntimeExchange(context.Context, runtimepkg.ChannelReport) (runtimepkg.ChannelReply, error)
}

type RuntimeWorker interface {
	Run(context.Context, runtimepkg.ChannelRequest) (runtimepkg.ChannelResult, error)
}

type RuntimePending struct {
	Enrollment runtimepkg.Enrollment    `json:"enrollment"`
	Result     runtimepkg.ChannelResult `json:"result"`
}

type RuntimeJournal interface {
	Lock() (func(), error)
	Load() (*RuntimePending, error)
	Save(RuntimePending) error
	Clear(string) error
}

type RuntimeInstaller struct {
	Transport  RuntimeTransport
	Worker     RuntimeWorker
	Enrollment runtimepkg.Enrollment
	Journal    RuntimeJournal
	period     time.Duration
}

func (installer RuntimeInstaller) Sync(ctx context.Context) error {
	if installer.Transport == nil || installer.Worker == nil || installer.Journal == nil || installer.Enrollment.Validate() != nil {
		return errors.New("runtime channel requires a provisioned worker, transport and durable journal")
	}
	unlock, err := installer.Journal.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	pending, err := installer.Journal.Load()
	if err != nil {
		return err
	}
	if pending != nil && pending.Result.State == "running" {
		pending.Result = runtimeUnknown(pending.Result.RequestID, pending.Result.PlanSHA256)
		if err := installer.Journal.Save(*pending); err != nil {
			return err
		}
	}
	report := runtimepkg.ChannelReport{Enrollment: installer.Enrollment}
	if pending != nil && pending.Enrollment == installer.Enrollment {
		report.Result = &pending.Result
	}
	reply, err := installer.Transport.RuntimeExchange(ctx, report)
	if err != nil {
		return err
	}
	if pending != nil {
		if report.Result != nil && reply.Request != nil && reply.Request.ID == pending.Result.RequestID {
			if pending.Result.State != "awaiting_approval" || !sameRuntimeReview(*reply.Request, pending.Result) {
				return errors.New("controller did not durably acknowledge the runtime result")
			}
		}
		if err := installer.Journal.Clear(pending.Result.RequestID); err != nil {
			return err
		}
	}
	request := reply.Request
	if request == nil {
		return nil
	}
	if request.Enrollment != installer.Enrollment || !ipsentinel.ValidUUID(request.ID) || request.ChannelInput.Validate() != nil {
		return errors.New("runtime request differs from the locally provisioned node")
	}
	if request.State == "running" {
		return installer.publish(ctx, runtimeUnknown(request.ID, request.PlanSHA256))
	}
	if request.State == "requested" {
		result, err := installer.Worker.Run(ctx, *request)
		if err != nil {
			result = runtimepkg.ChannelResult{RequestID: request.ID, State: "failed", ErrorCode: "plan_unavailable"}
		}
		if result.State == "awaiting_approval" {
			if err := runtimepkg.ValidateChannelPlan(*request, result); err != nil {
				return err
			}
		} else if result.State != "failed" || result.RequestID != request.ID || result.ErrorCode != "plan_unavailable" {
			return errors.New("runtime planner returned an invalid result")
		}
		return installer.publish(ctx, result)
	}
	if request.State != "approved" || request.ApprovedAt <= 0 || time.Now().Unix()-request.ApprovedAt > 900 || request.ApprovedAt > time.Now().Unix()+5 {
		return errors.New("runtime operation has no current approval")
	}
	review := runtimepkg.ChannelResult{RequestID: request.ID, State: "awaiting_approval", PlanSHA256: request.PlanSHA256, Review: request.Review, Operation: request.Operation}
	if err := runtimepkg.ValidateChannelPlan(*request, review); err != nil {
		return err
	}
	preflight := *request
	preflight.State, preflight.PlanSHA256, preflight.Review, preflight.Operation = "requested", "", nil, nil
	current, err := installer.Worker.Run(ctx, preflight)
	if err != nil || current.State != "awaiting_approval" || current.PlanSHA256 != request.PlanSHA256 || !sameRuntimeReview(*request, current) {
		return installer.publish(ctx, runtimepkg.ChannelResult{RequestID: request.ID, State: "failed", PlanSHA256: request.PlanSHA256, ErrorCode: "plan_changed"})
	}
	started := runtimepkg.ChannelResult{RequestID: request.ID, State: "running", PlanSHA256: request.PlanSHA256}
	reply, err = installer.Transport.RuntimeExchange(ctx, runtimepkg.ChannelReport{Enrollment: installer.Enrollment, Result: &started})
	if err != nil {
		return err
	}
	if reply.Request == nil || reply.Request.ID != request.ID || reply.Request.State != "running" || reply.Request.Enrollment != installer.Enrollment || !sameRuntimeReview(*reply.Request, review) {
		return errors.New("runtime approval was cancelled or changed before execution")
	}
	if err := installer.Journal.Save(RuntimePending{Enrollment: installer.Enrollment, Result: started}); err != nil {
		return err
	}
	result, err := installer.Worker.Run(ctx, *request)
	if err != nil || runtimepkg.ValidateChannelReceipt(*request, result) != nil {
		result = runtimeUnknown(request.ID, request.PlanSHA256)
	}
	return installer.publish(ctx, result)
}

func (installer RuntimeInstaller) publish(ctx context.Context, result runtimepkg.ChannelResult) error {
	if err := installer.Journal.Save(RuntimePending{Enrollment: installer.Enrollment, Result: result}); err != nil {
		return err
	}
	reply, err := installer.Transport.RuntimeExchange(ctx, runtimepkg.ChannelReport{Enrollment: installer.Enrollment, Result: &result})
	if err != nil {
		return err
	}
	if reply.Request != nil && reply.Request.ID == result.RequestID {
		if result.State != "awaiting_approval" || !sameRuntimeReview(*reply.Request, result) {
			return errors.New("controller did not acknowledge the runtime result")
		}
	}
	return installer.Journal.Clear(result.RequestID)
}

func runtimeUnknown(id, digest string) runtimepkg.ChannelResult {
	return runtimepkg.ChannelResult{RequestID: id, State: "unknown", PlanSHA256: digest, ErrorCode: "operation_interrupted"}
}

func sameRuntimeReview(request runtimepkg.ChannelRequest, result runtimepkg.ChannelResult) bool {
	first, firstErr := json.Marshal([]any{request.PlanSHA256, request.Review, request.Operation})
	second, secondErr := json.Marshal([]any{result.PlanSHA256, result.Review, result.Operation})
	return firstErr == nil && secondErr == nil && string(first) == string(second)
}

func (installer RuntimeInstaller) Run(ctx context.Context) {
	period := installer.period
	if period <= 0 {
		period = 10 * time.Second
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		call, cancel := context.WithTimeout(ctx, 35*time.Minute)
		err := installer.Sync(call)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("runtime installation channel unavailable; local operation journal retained")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type LocalRuntime struct {
	Config     RuntimeInstallConfig
	Enrollment runtimepkg.Enrollment
	Keys       map[string]ed25519.PublicKey
}

func ProvisionRuntime(config RuntimeInstallConfig, journal FileRuntimePending) (*LocalRuntime, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("the runtime installation channel requires Linux")
	}
	if err := validateRuntimePaths(config); err != nil {
		return nil, err
	}
	keys, err := loadRuntimeKeys(config.CCKeysPath)
	if err != nil {
		return nil, err
	}
	if _, err := runtimepkg.NewRepository(config.CCURL, keys); err != nil {
		return nil, err
	}
	identity, err := journal.Identity()
	if err != nil {
		return nil, err
	}
	scope, err := ipsentinel.CanonicalSHA256(struct {
		Config   RuntimeInstallConfig         `json:"config"`
		Keys     map[string]ed25519.PublicKey `json:"keys"`
		Identity string                       `json:"identity"`
	}{config, keys, identity})
	if err != nil {
		return nil, err
	}
	enrollment := runtimepkg.Enrollment{
		ScopeSHA256: scope, Target: config.Target,
		Host: runtimepkg.Host{Platform: "linux-" + runtime.GOARCH, ProbeVersion: config.ProbeVersion, AgentVersion: strings.TrimPrefix(model.Version, "v")},
	}
	if err := enrollment.Validate(); err != nil {
		return nil, err
	}
	return &LocalRuntime{Config: config, Enrollment: enrollment, Keys: keys}, nil
}

func (local *LocalRuntime) Run(ctx context.Context, request runtimepkg.ChannelRequest) (runtimepkg.ChannelResult, error) {
	if local == nil || request.Enrollment != local.Enrollment || request.ChannelInput.Validate() != nil {
		return runtimepkg.ChannelResult{}, errors.New("runtime request is outside the locally provisioned scope")
	}
	if local.Config.CCKeysPath != "" {
		keys, err := loadRuntimeKeys(local.Config.CCKeysPath)
		if err != nil || !sameRuntimeKeys(keys, local.Keys) {
			return runtimepkg.ChannelResult{}, errors.New("local runtime trust changed; restart the dedicated installer to invalidate pending approvals")
		}
	}
	if request.State != "requested" && request.State != "approved" {
		return runtimepkg.ChannelResult{}, errors.New("runtime request is neither a plan request nor an approved operation")
	}
	if request.State == "requested" && (request.Review != nil || request.Operation != nil || request.PlanSHA256 != "") {
		return runtimepkg.ChannelResult{}, errors.New("runtime plan request contains an existing approval")
	}
	if request.State == "approved" {
		review := runtimepkg.ChannelResult{RequestID: request.ID, State: "awaiting_approval", PlanSHA256: request.PlanSHA256, Review: request.Review, Operation: request.Operation}
		if err := runtimepkg.ValidateChannelPlan(request, review); err != nil {
			return runtimepkg.ChannelResult{}, err
		}
	}
	result := runtimepkg.ChannelResult{RequestID: request.ID}
	if request.Action == "stage" {
		repository, err := runtimepkg.NewRepository(local.Config.CCURL, local.Keys)
		if err != nil {
			return result, err
		}
		release, err := repository.Fetch(ctx, request.Version, local.Enrollment.Host, local.Enrollment.Target)
		if err != nil {
			return result, err
		}
		review := release.Review()
		if request.State == "requested" {
			result.State, result.PlanSHA256, result.Review = "awaiting_approval", review.PlanSHA256, &review
			return result, nil
		}
		if review.PlanSHA256 != request.PlanSHA256 {
			return result, errors.New("reviewed runtime release changed")
		}
		permission := review.ApprovalTemplate
		permission.Approved = true
		approval, _ := json.Marshal(permission)
		receipt, err := repository.Stage(ctx, runtimepkg.Store{Dir: local.Config.StagingDir}, release, approval)
		if err != nil {
			return result, err
		}
		result.State, result.PlanSHA256, result.Receipt = "staged", receipt.PlanSHA256, &receipt
		return result, nil
	}
	options := runtimepkg.OperationOptions{Action: request.Action, InstallRoot: local.Config.InstallRoot, ReferencePath: local.Config.TrustedInstaller}
	if request.Action == "select" {
		options.StateDir, options.ConfigPath, options.PolicyPath, options.Unit = local.Config.StateDir, local.Config.ConfigPath, local.Config.PolicyPath, local.Config.Unit
	}
	review, err := runtimepkg.PrepareOperation(ctx, runtimepkg.Store{Dir: local.Config.StagingDir}, request.StagePlanSHA256, local.Keys, local.Enrollment.Host, local.Enrollment.Target, options)
	if err != nil {
		return result, err
	}
	if request.State == "requested" {
		result.State, result.PlanSHA256, result.Operation = "awaiting_approval", review.PlanSHA256, &review
		return result, nil
	}
	if review.PlanSHA256 != request.PlanSHA256 {
		return result, errors.New("approved local runtime installation inputs changed")
	}
	permission := review.ApprovalTemplate
	permission.Approved = true
	approval, _ := json.Marshal(permission)
	receipt, err := runtimepkg.ApplyOperation(ctx, runtimepkg.Store{Dir: local.Config.StagingDir}, request.StagePlanSHA256, local.Keys, local.Enrollment.Host, local.Enrollment.Target, options, approval)
	if err != nil {
		return result, err
	}
	result.State, result.PlanSHA256, result.OperationReceipt = receipt.State, receipt.PlanSHA256, &receipt
	return result, nil
}

func sameRuntimeKeys(first, second map[string]ed25519.PublicKey) bool {
	firstRaw, _ := json.Marshal(first)
	secondRaw, _ := json.Marshal(second)
	return string(firstRaw) == string(secondRaw)
}

func parseRuntimeKeys(raw []byte) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, key, err := tzapkg.ParsePublicKey(line)
		if err != nil || keys[id] != nil {
			return nil, errors.New("invalid or duplicate pinned runtime public key")
		}
		keys[id] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("no pinned runtime public key")
	}
	return keys, nil
}

func (t *HTTPTransport) RuntimeExchange(ctx context.Context, report runtimepkg.ChannelReport) (runtimepkg.ChannelReply, error) {
	var reply runtimepkg.ChannelReply
	err := t.request(ctx, "POST", "/api/v1/agent/runtime", report, &reply)
	return reply, err
}
