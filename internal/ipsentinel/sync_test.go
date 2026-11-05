package ipsentinel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type syncTransportFixture struct {
	receipt      SyncReceipt
	report       SyncReport
	fail         bool
	alterReceipt bool
	stored       int
}

func (transport *syncTransportFixture) ExecutorCursor(context.Context) (SyncReceipt, error) {
	return transport.receipt, nil
}
func (transport *syncTransportFixture) SaveExecutorRecords(_ context.Context, report SyncReport) (SyncReceipt, error) {
	transport.report = report
	if transport.fail {
		return SyncReceipt{}, errors.New("connection lost before durable receipt")
	}
	transport.receipt.ThroughSeq = report.Page.NextSeq
	transport.stored += len(report.Page.Records)
	if transport.alterReceipt {
		transport.receipt.Binding.ExecutorInstanceID = NewUUID()
	}
	return transport.receipt, nil
}

type lostAckReader struct {
	Reader
	lost bool
}

func (reader *lostAckReader) RecordsAck(ctx context.Context, requestID string, fence Fence, through int64) (Ack, error) {
	ack, err := reader.Reader.RecordsAck(ctx, requestID, fence, through)
	if err == nil && !reader.lost {
		reader.lost = true
		return Ack{}, &TransportError{Err: errors.New("ACK reply lost after commit")}
	}
	return ack, err
}

func TestSyncRecoversFromLostAckWithoutReplayingRecords(t *testing.T) {
	mock, client := newUDSFixture(t)
	mock.Control.Mode = ModeManagedReadonly
	mock.TriggerAutoPause("operator")
	hello, err := client.Hello(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Target: client.Target, ExecutorInstanceID: hello.Status.ExecutorInstanceID, DescriptorSHA256: hello.DescriptorSHA256, Epoch: hello.Status.Control.Epoch}
	transport := &syncTransportFixture{receipt: SyncReceipt{Binding: binding}}
	reader := &lostAckReader{Reader: client}
	syncer := Synchronizer{Reader: reader, Transport: transport, Target: client.Target}
	if err := syncer.Sync(context.Background()); !IsTransportError(err) {
		t.Fatal(err)
	}
	if transport.receipt.ThroughSeq != 1 {
		t.Fatal("server cursor not committed")
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if transport.stored != 1 {
		t.Fatal("records replayed after durable ACK", transport.stored)
	}
}

func TestSyncRequiresDurableReceiptBeforeAck(t *testing.T) {
	mock, client := newUDSFixture(t)
	mock.Control.Mode = ModeManagedReadonly
	mock.RequireHelloFirst = true
	mock.TriggerAutoPause("operator")
	hello, err := client.Hello(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Target: client.Target, ExecutorInstanceID: hello.Status.ExecutorInstanceID, DescriptorSHA256: hello.DescriptorSHA256, Epoch: hello.Status.Control.Epoch}
	transport := &syncTransportFixture{receipt: SyncReceipt{Binding: binding}, fail: true}
	syncer := Synchronizer{Reader: client, Target: client.Target, Transport: transport}
	if err := syncer.Sync(context.Background()); err == nil {
		t.Fatal("lost receipt ignored")
	}
	status, err := client.StatusGet(context.Background(), "")
	if err != nil || status.Journal.AckedSeq != 0 {
		t.Fatal(status, err)
	}
	transport.fail = false
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = client.StatusGet(context.Background(), "")
	if err != nil || status.Journal.AckedSeq != 1 {
		t.Fatal(status, err)
	}
	client.Close()
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal("reconnect failed", err)
	}
}

func TestSyncRejectsClockAndIdentityChanges(t *testing.T) {
	var envelope struct {
		Result Hello `json:"result"`
	}
	if err := json.Unmarshal(fixture(t, "examples/hello.response.json"), &envelope); err != nil {
		t.Fatal(err)
	}
	hello := envelope.Result
	hello.Status.Control.Mode = ModeManagedReadonly
	now, err := ParseContractTime(hello.Status.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Target: Target{ControllerID: NewUUID(), NodeID: NewUUID()}, ExecutorInstanceID: hello.Status.ExecutorInstanceID, DescriptorSHA256: hello.DescriptorSHA256, Epoch: hello.Status.Control.Epoch}
	report := SyncReport{Hello: hello, Page: RecordPage{ExecutorInstanceID: binding.ExecutorInstanceID, Records: []Record{}}}
	if err := report.Validate(binding, now); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*SyncReport){
		func(report *SyncReport) { report.Hello.Status.ExecutorInstanceID = NewUUID() },
		func(report *SyncReport) { report.Hello.Status.Control.Epoch++ },
		func(report *SyncReport) { report.Hello.Status.ObservedAt = now.Add(time.Minute).Format(time.RFC3339) },
		func(report *SyncReport) { report.Page.NextSeq = 9 },
	} {
		changed := report
		mutate(&changed)
		if err := changed.Validate(binding, now); err == nil {
			t.Fatal("invalid sync report accepted")
		}
	}
}
