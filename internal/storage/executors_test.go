package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
)

func executorFixture(t *testing.T) (ipsentinel.Binding, ipsentinel.SyncReport, time.Time) {
	t.Helper()
	root := "../ipsentinel/testdata/contracts/ipsentinel-executor-v1/examples"
	load := func(name string, destination any) {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(envelope.Result, destination); err != nil {
			t.Fatal(err)
		}
	}
	var hello ipsentinel.Hello
	var page ipsentinel.RecordPage
	load("hello.response.json", &hello)
	load("records.list.response.json", &page)
	hello.Status.Control.Mode = ipsentinel.ModeManagedReadonly
	now, err := ipsentinel.ParseContractTime(hello.Status.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	binding := ipsentinel.Binding{Target: ipsentinel.Target{ControllerID: ipsentinel.NewUUID(), NodeID: ipsentinel.NewUUID()}, ExecutorInstanceID: hello.Status.ExecutorInstanceID, DescriptorSHA256: hello.DescriptorSHA256, Epoch: hello.Status.Control.Epoch}
	return binding, ipsentinel.SyncReport{Hello: hello, Page: page}, now
}

func TestExecutorDurableCursorAndRollback(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	ctx := context.Background()
	binding, report, now := executorFixture(t)
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_executor_commit BEFORE UPDATE ON executor_bindings BEGIN SELECT RAISE(ABORT,'injected commit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveExecutorRecords(ctx, node, report, now); err == nil {
		t.Fatal("injected transaction failure ignored")
	}
	cursor, err := store.ExecutorCursor(ctx, node)
	if err != nil || cursor.ThroughSeq != 0 {
		t.Fatal(cursor, err)
	}
	records, err := store.ExecutorRecords(ctx, node, 0)
	if err != nil || len(records) != 0 {
		t.Fatal("transaction did not roll back", records, err)
	}
	if _, err := store.db.Exec("DROP TRIGGER reject_executor_commit"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		receipt, err := store.SaveExecutorRecords(ctx, node, report, now)
		if err != nil || receipt.ThroughSeq != 1 {
			t.Fatal(receipt, err)
		}
	}
	changed := report
	changed.Page.Records = append([]ipsentinel.Record{}, report.Page.Records...)
	changed.Page.Records[0].RecordedAt = now.Add(-time.Second).Format(time.RFC3339)
	if _, err := store.SaveExecutorRecords(ctx, node, changed, now); !errors.Is(err, model.ErrConflict) {
		t.Fatal("conflicting payload accepted", err)
	}
	gap := report
	gap.AfterSeq = 1
	gap.Page.Records = append([]ipsentinel.Record{}, report.Page.Records...)
	gap.Page.Records[0].Seq = 3
	gap.Page.NextSeq = 3
	if _, err := store.SaveExecutorRecords(ctx, node, gap, now); err == nil {
		t.Fatal("gap accepted")
	}
	records, err = store.ExecutorRecords(ctx, node, 0)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
}

func TestExecutorBindingIsolationAndConcurrentReplay(t *testing.T) {
	store := openTest(t)
	first, _ := makeNode(t, store)
	second, _ := makeNode(t, store)
	ctx := context.Background()
	binding, report, now := executorFixture(t)
	if err := store.BindExecutor(ctx, first, binding); err != nil {
		t.Fatal(err)
	}
	if err := store.BindExecutor(ctx, second, binding); !errors.Is(err, model.ErrConflict) {
		t.Fatal("shared instance binding accepted", err)
	}
	if _, err := store.SaveExecutorRecords(ctx, second, report, now); !errors.Is(err, model.ErrNotFound) {
		t.Fatal("unbound node upload accepted", err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := store.SaveExecutorRecords(ctx, first, report, now); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	records, err := store.ExecutorRecords(ctx, first, 0)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	binding.ExecutorInstanceID = ipsentinel.NewUUID()
	if err := store.BindExecutor(ctx, first, binding); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveExecutorRecords(ctx, first, report, now); err == nil {
		t.Fatal("old instance upload accepted")
	}
	cursor, err := store.ExecutorCursor(ctx, first)
	if err != nil || cursor.ThroughSeq != 0 {
		t.Fatal(cursor, err)
	}
}

func TestExecutorResumeAfterServerRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	node, _ := makeNode(t, store)
	binding, report, now := executorFixture(t)
	ctx := context.Background()
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveExecutorRecords(ctx, node, report, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receipt, err := store.ExecutorCursor(ctx, node)
	if err != nil || receipt.ThroughSeq != 1 || receipt.Binding != binding {
		t.Fatal(receipt, err)
	}
	if _, err := store.SaveExecutorRecords(ctx, node, report, now); err != nil {
		t.Fatal(err)
	}
	records, err := store.ExecutorRecords(ctx, node, 0)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
}

func TestExecutorOutboxIsAtomicAndReplaySafe(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	ctx := context.Background()
	binding, report, now := executorFixture(t)
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	assessment := report.Hello.Status.Assessments[0]
	data, err := json.Marshal(assessment)
	if err != nil {
		t.Fatal(err)
	}
	report.Page.Records = []ipsentinel.Record{{ExecutorInstanceID: binding.ExecutorInstanceID, Seq: 1, RecordID: ipsentinel.NewUUID(), RecordedAt: now.Format(time.RFC3339), Kind: ipsentinel.RecordAssessmentChanged, Data: data}}
	for range 2 {
		if _, err := store.SaveExecutorRecords(ctx, node, report, now); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.PendingEvents(ctx, now)
	if err != nil || len(events) != 1 || events[0].Kind != "executor" || !events[0].Active {
		t.Fatal(events, err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_executor_commit BEFORE UPDATE ON executor_bindings BEGIN SELECT RAISE(ABORT,'injected commit failure'); END`); err != nil {
		t.Fatal(err)
	}
	assessment.State = ipsentinel.AssessmentUnknown
	assessment.ValidUntil = nil
	data, err = json.Marshal(assessment)
	if err != nil {
		t.Fatal(err)
	}
	report.AfterSeq = 1
	report.Page.NextSeq = 2
	report.Page.Records = []ipsentinel.Record{{ExecutorInstanceID: binding.ExecutorInstanceID, Seq: 2, RecordID: ipsentinel.NewUUID(), RecordedAt: now.Format(time.RFC3339), Kind: ipsentinel.RecordAssessmentChanged, Data: data}}
	if _, err := store.SaveExecutorRecords(ctx, node, report, now); err == nil {
		t.Fatal("commit failure ignored")
	}
	events, err = store.PendingEvents(ctx, now)
	if err != nil || len(events) != 1 {
		t.Fatal("outbox escaped rollback", events, err)
	}
}

func TestExecutorRevocationAndSnapshotOrdering(t *testing.T) {
	store := openTest(t)
	node, _ := makeNode(t, store)
	ctx := context.Background()
	binding, report, now := executorFixture(t)
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveExecutorRecords(ctx, node, report, now); err != nil {
		t.Fatal(err)
	}
	older := report
	older.Hello.Status.ObservedAt = now.Add(-time.Second).Format(time.RFC3339)
	older.Hello.Status.Health = "unavailable"
	if _, err := store.SaveExecutorRecords(ctx, node, older, now); err != nil {
		t.Fatal(err)
	}
	views, err := store.Executors(ctx)
	if err != nil || views[0].Hello.Status.Health != "ready" {
		t.Fatal("late snapshot replaced newer state", views, err)
	}
	if err := store.UnbindExecutor(ctx, node); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveExecutorRecords(ctx, node, report, now); !errors.Is(err, model.ErrNotFound) {
		t.Fatal("revoked upload accepted", err)
	}
	if err := store.BindExecutor(ctx, node, binding); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.SaveExecutorRecords(ctx, node, report, now)
	if err != nil || receipt.ThroughSeq != 1 {
		t.Fatal("reauthorized replay lost cursor", receipt, err)
	}
}
