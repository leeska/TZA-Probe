package ipsentinel

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Binding struct {
	Target             Target `json:"target"`
	ExecutorInstanceID string `json:"executor_instance_id"`
	DescriptorSHA256   string `json:"descriptor_sha256"`
	Epoch              int64  `json:"epoch"`
	Mode               string `json:"mode,omitempty"`
}

type SyncReport struct {
	Hello    Hello      `json:"hello"`
	AfterSeq int64      `json:"after_seq"`
	Page     RecordPage `json:"page"`
}

type SyncReceipt struct {
	Binding    Binding `json:"binding"`
	ThroughSeq int64   `json:"through_seq"`
}

type View struct {
	NodeID     string  `json:"node_id"`
	Binding    Binding `json:"binding"`
	ThroughSeq int64   `json:"through_seq"`
	ReceivedAt int64   `json:"received_at"`
	Hello      *Hello  `json:"hello"`
	Config     *Config `json:"config,omitempty"`
}

type SyncTransport interface {
	ExecutorCursor(context.Context) (SyncReceipt, error)
	SaveExecutorRecords(context.Context, SyncReport) (SyncReceipt, error)
}

type Reader interface {
	Hello(context.Context, string) (Hello, error)
	RecordsList(context.Context, string, int64, int64) (RecordPage, error)
	RecordsAck(context.Context, string, Fence, int64) (Ack, error)
}

type Synchronizer struct {
	Reader    Reader
	Transport SyncTransport
	Target    Target
	Now       func() time.Time
}

func (binding Binding) Validate() error {
	if binding.Mode != "" && binding.Mode != ModeManagedReadonly && binding.Mode != ModeManaged {
		return semanticErr("invalid authorized mode")
	}
	raw, err := json.Marshal(Fence{ExecutorInstanceID: binding.ExecutorInstanceID, Epoch: binding.Epoch})
	if err != nil {
		return err
	}
	if err := ValidateDefinition("fence", raw, nil); err != nil {
		return err
	}
	raw, err = json.Marshal(binding.Target)
	if err != nil {
		return err
	}
	if err := ValidateDefinition("target", raw, nil); err != nil {
		return err
	}
	raw, err = json.Marshal(binding.DescriptorSHA256)
	if err != nil {
		return err
	}
	return ValidateDefinition("hash", raw, nil)
}

func (report SyncReport) Validate(binding Binding, now time.Time) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(report.Hello)
	if err != nil {
		return err
	}
	if err := ValidateDefinition("hello", raw, nil); err != nil {
		return err
	}
	raw, err = json.Marshal(report.Page)
	if err != nil {
		return err
	}
	if err := ValidateDefinition("record_page", raw, nil); err != nil {
		return err
	}
	status := report.Hello.Status
	if status.ExecutorInstanceID != binding.ExecutorInstanceID || report.Page.ExecutorInstanceID != binding.ExecutorInstanceID || report.Hello.DescriptorSHA256 != binding.DescriptorSHA256 || status.Control.Epoch != binding.Epoch {
		return semanticErr("executor identity, descriptor or epoch changed; authorization required")
	}
	if status.Control.Mode != ModeManagedReadonly && status.Control.Mode != ModeManaged {
		return semanticErr("executor is not in an approved managed mode")
	}
	if status.Control.Mode == ModeManaged && binding.Mode != ModeManaged {
		return semanticErr("managed execution requires explicit authorization")
	}
	observed, err := ParseContractTime(status.ObservedAt)
	if err != nil {
		return err
	}
	if observed.Before(now.Add(-30*time.Second)) || observed.After(now.Add(30*time.Second)) {
		return semanticErr("executor clock_skew")
	}
	if report.AfterSeq < 0 || report.AfterSeq > MaxSafeInt {
		return semanticErr("invalid source cursor")
	}
	if len(report.Page.Records) == 0 {
		if report.Page.NextSeq != report.AfterSeq || report.Page.HasMore {
			return semanticErr("empty page cursor mismatch")
		}
	} else if report.Page.Records[0].Seq != report.AfterSeq+1 {
		return semanticErr("source sequence gap")
	}
	return nil
}

func (s Synchronizer) Sync(ctx context.Context) error {
	if s.Reader == nil || s.Transport == nil {
		return errors.New("executor sync is not configured")
	}
	cursor, err := s.Transport.ExecutorCursor(ctx)
	if err != nil {
		return err
	}
	if cursor.Binding.Target != s.Target {
		return semanticErr("local and server target bindings differ")
	}
	hello, err := s.Reader.Hello(ctx, "")
	if err != nil {
		return err
	}
	page, err := s.Reader.RecordsList(ctx, "", cursor.ThroughSeq, 50)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	report := SyncReport{Hello: hello, AfterSeq: cursor.ThroughSeq, Page: page}
	if err := report.Validate(cursor.Binding, now); err != nil {
		return err
	}
	receipt, err := s.Transport.SaveExecutorRecords(ctx, report)
	if err != nil {
		return err
	}
	if receipt.Binding != cursor.Binding || receipt.ThroughSeq != page.NextSeq || receipt.ThroughSeq > MaxSafeInt {
		return semanticErr("invalid durable receipt")
	}
	if receipt.ThroughSeq != 0 {
		fence := Fence{ExecutorInstanceID: cursor.Binding.ExecutorInstanceID, Epoch: cursor.Binding.Epoch}
		ack, err := s.Reader.RecordsAck(ctx, "", fence, receipt.ThroughSeq)
		if err != nil {
			return err
		}
		if ack.AckedSeq < receipt.ThroughSeq {
			return semanticErr("executor ACK cursor did not advance")
		}
	}
	if controller, ok := s.Reader.(Controller); ok {
		if transport, ok := s.Transport.(ActionTransport); ok && hello.Status.Control.Mode == ModeManaged {
			return ExecuteAction(ctx, controller, transport, cursor.Binding, hello)
		}
	}
	if controller, ok := s.Reader.(Controller); ok && cursor.Binding.Mode == ModeManaged && hello.Status.Control.Mode == ModeManaged {
		if actionTransport, ok := s.Transport.(ActionTransport); ok {
			return ExecuteAction(ctx, controller, actionTransport, cursor.Binding, hello)
		}
	}
	return nil
}
