package ipsentinel

import (
	"context"
	"errors"
	"testing"
	"time"
)

type actionTransportFixture struct {
	action  ExecutorAction
	results []ActionResult
}

type managedSyncTransportFixture struct {
	*syncTransportFixture
	*actionTransportFixture
}

func (transport *actionTransportFixture) ExecutorAction(context.Context, Hello) (ExecutorAction, error) {
	return transport.action, nil
}

func (transport *actionTransportFixture) FinishExecutorAction(_ context.Context, result ActionResult) error {
	transport.results = append(transport.results, result)
	return nil
}

type droppedAdmission struct {
	Controller
	submitted int
}

func (client *droppedAdmission) RunSubmit(ctx context.Context, id string, job JobParams) (Admission, error) {
	client.submitted++
	admission, err := client.Controller.RunSubmit(ctx, id, job)
	if err != nil {
		return admission, err
	}
	return Admission{}, &TransportError{Err: errors.New("reply lost after durable admission")}
}

type rejectedAdmission struct {
	Controller
	code      string
	retryable bool
}

type invalidConfigController struct{ Controller }

func (client invalidConfigController) ConfigValidate(context.Context, string, Config) (Validation, error) {
	return Validation{Valid: false, Issues: []ValidationIssue{}}, nil
}

func TestManagedInvalidConfigIsNotAccepted(t *testing.T) {
	client, binding, hello, action := actionFixture(t)
	config := fixtureConfig(t)
	action.Kind, action.Config = MethodConfigValidate, &config
	transport := &actionTransportFixture{action: action}
	if err := ExecuteAction(context.Background(), invalidConfigController{client}, transport, binding, hello); err != nil {
		t.Fatal(err)
	}
	if len(transport.results) != 1 || transport.results[0].Accepted || transport.results[0].Validation == nil || transport.results[0].Validation.Valid {
		t.Fatal("invalid local validation accepted", transport.results)
	}
}

func TestManagedExpiredWindowRecoversAdmissionWithoutNewSubmission(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "accepted"}[admitted], func(t *testing.T) {
			client, _, hello, action := actionFixture(t)
			controller := &droppedAdmission{Controller: client}
			if admitted {
				if _, err := performAction(context.Background(), controller, action, hello, time.Now()); !IsTransportError(err) {
					t.Fatal(err)
				}
			}
			end, err := ParseContractTime(action.ExpiresAt)
			if err != nil {
				t.Fatal(err)
			}
			result, err := performAction(context.Background(), controller, action, hello, end.Add(time.Second))
			if admitted {
				if err != nil || !result.Accepted || result.Run == nil || result.Run.JobID != action.JobID || controller.submitted != 1 {
					t.Fatal("lost receipt not recovered from original job", result, err)
				}
			} else {
				var rejected *RPCError
				if !errors.As(err, &rejected) || rejected.Err.Code != ErrCodeExpired || result.Accepted || controller.submitted != 0 {
					t.Fatal("expired unknown job resubmitted or accepted", result, err)
				}
			}
		})
	}
}

func (client rejectedAdmission) RunSubmit(context.Context, string, JobParams) (Admission, error) {
	return Admission{}, &RPCError{Method: MethodRunSubmit, Err: Error{Code: client.code, Message: "fixture rejection", Retryable: client.retryable}}
}

func actionFixture(t *testing.T) (*Client, Binding, Hello, ExecutorAction) {
	t.Helper()
	_, client := newUDSFixture(t)
	config := fixtureConfig(t)
	hello, err := client.Hello(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Target: client.Target, ExecutorInstanceID: hello.Status.ExecutorInstanceID, DescriptorSHA256: hello.DescriptorSHA256, Epoch: hello.Status.Control.Epoch, Mode: ModeManaged}
	receipt, err := client.ConfigApply(context.Background(), "", Fence{ExecutorInstanceID: binding.ExecutorInstanceID, Epoch: binding.Epoch}, 0, config)
	if err != nil {
		t.Fatal(err)
	}
	hello, err = client.Hello(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	action := ExecutorAction{ID: NewUUID(), Binding: binding, Kind: MethodRunSubmit, LeaseID: NewUUID(), RequestID: NewUUID(), Attempt: 1, JobID: NewUUID(), ConfigRevision: receipt.Config.Revision, ConfigSHA256: receipt.Config.SHA256, ServiceID: config.Services[0].ServiceID, Purpose: PurposeObserve, Phase: PhaseBaseline, NotBefore: now.Add(-time.Second).Format(time.RFC3339), ExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339), TimeoutMS: 30000}
	return client, binding, hello, action
}

func TestManagedLostAdmissionOnlyQueriesOriginalJob(t *testing.T) {
	client, binding, hello, action := actionFixture(t)
	controller := &droppedAdmission{Controller: client}
	transport := &actionTransportFixture{action: action}
	if err := ExecuteAction(context.Background(), controller, transport, binding, hello); !IsTransportError(err) {
		t.Fatal("lost admission treated as business failure", err)
	}
	if len(transport.results) != 0 {
		t.Fatal("unknown transport result acknowledged")
	}
	transport.action.LeaseID = NewUUID()
	transport.action.Attempt++
	if err := ExecuteAction(context.Background(), controller, transport, binding, hello); err != nil {
		t.Fatal(err)
	}
	if controller.submitted != 1 || len(transport.results) != 1 || !transport.results[0].Accepted || transport.results[0].Run.JobID != action.JobID {
		t.Fatal("retransmission started another job", controller.submitted, transport.results)
	}
}

func TestManagedPermanentAndRetryableRejections(t *testing.T) {
	for _, test := range []struct {
		code      string
		retryable bool
	}{{ErrCodePolicyDenied, false}, {ErrCodeBusy, true}, {ErrCodeNotDue, true}} {
		t.Run(test.code, func(t *testing.T) {
			client, binding, hello, action := actionFixture(t)
			transport := &actionTransportFixture{action: action}
			if err := ExecuteAction(context.Background(), rejectedAdmission{Controller: client, code: test.code, retryable: test.retryable}, transport, binding, hello); err != nil {
				t.Fatal(err)
			}
			if len(transport.results) != 1 || transport.results[0].Accepted || transport.results[0].Retryable != test.retryable || transport.results[0].ErrorCode != test.code {
				t.Fatal(transport.results)
			}
		})
	}
}

func TestManagedStopAllowedWithDegradedFeaturesAndBindingMismatchRejected(t *testing.T) {
	client, binding, hello, action := actionFixture(t)
	action.Kind = MethodControlPause
	hello.Features = []string{"journal"}
	hello.Status.Health = "degraded"
	transport := &actionTransportFixture{action: action}
	if err := ExecuteAction(context.Background(), client, transport, binding, hello); err != nil {
		t.Fatal(err)
	}
	if len(transport.results) != 1 || !transport.results[0].Accepted || !transport.results[0].Control.Paused {
		t.Fatal("degraded runtime could not stop", transport.results)
	}
	transport.results = nil
	transport.action.Binding.Epoch++
	if err := ExecuteAction(context.Background(), client, transport, binding, hello); err == nil || len(transport.results) != 0 {
		t.Fatal("different binding executed")
	}
}

func TestSynchronizerDispatchesOnlyExplicitManagedLease(t *testing.T) {
	client, binding, hello, action := actionFixture(t)
	binding.Mode = ModeManaged
	action.Binding = binding
	transport := &managedSyncTransportFixture{
		syncTransportFixture:   &syncTransportFixture{receipt: SyncReceipt{Binding: binding}},
		actionTransportFixture: &actionTransportFixture{action: action},
	}
	syncer := Synchronizer{Reader: client, Transport: transport, Target: client.Target}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(transport.results) != 1 || !transport.results[0].Accepted || transport.results[0].Run == nil || transport.results[0].Run.JobID != action.JobID {
		t.Fatal("managed lease was not dispatched after durable sync", transport.results)
	}
	if transport.syncTransportFixture.receipt.ThroughSeq != 1 {
		t.Fatal("journal cursor was not durably advanced first", transport.syncTransportFixture.receipt)
	}

	mock, readonlyClient := newUDSFixture(t)
	mock.Control.Mode = ModeManagedReadonly
	readonlyHello, err := readonlyClient.Hello(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	readonlyBinding := Binding{Target: readonlyClient.Target, ExecutorInstanceID: readonlyHello.Status.ExecutorInstanceID, DescriptorSHA256: readonlyHello.DescriptorSHA256, Epoch: readonlyHello.Status.Control.Epoch, Mode: ModeManagedReadonly}
	readonlyTransport := &managedSyncTransportFixture{syncTransportFixture: &syncTransportFixture{receipt: SyncReceipt{Binding: readonlyBinding}}, actionTransportFixture: &actionTransportFixture{action: action}}
	if err := (Synchronizer{Reader: readonlyClient, Transport: readonlyTransport, Target: readonlyClient.Target}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(readonlyTransport.results) != 0 {
		t.Fatal("read-only sync dispatched managed command")
	}
	_ = hello
}
