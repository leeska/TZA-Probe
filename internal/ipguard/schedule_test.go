package ipguard

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func finishScheduled(t *testing.T, store *Database, value string) Job {
	t.Helper()
	job, err := store.Claim(t.Context())
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	result := Result{Execution: "completed", BrowserVersion: "test", Sandbox: true, NetworkIsolated: true, EgressBefore: job.Policy.ExpectedIP, EgressAfter: job.Policy.ExpectedIP, Page: Page{Outcome: "normal", FinalOrigin: "https://www.google.com", Signal: "google_page_country", Value: value}}
	if err := store.Complete(t.Context(), job.ID, job.Lease, result); err != nil {
		t.Fatal(err)
	}
	return *job
}

func TestLegacyRedirectResultsNeverAuthorizeCountryRecovery(t *testing.T) {
	store := testStore(t)
	policy := testPolicy()
	policy.AutoRecover, policy.IntervalMinutes, policy.Services = true, 720, []string{"google"}
	policy, err := store.SavePolicy(t.Context(), "node", policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(t.Context(), "node", "google", "baseline", "check:legacy-slot"); err != nil {
		t.Fatal(err)
	}
	job := finishScheduled(t, store, "")
	legacy := Result{Execution: "completed", Assessment: "anomaly_observed", Page: Page{Signal: "homepage_hkredirect", Value: "CN_HKREDIRECT"}}
	raw, _ := json.Marshal(legacy)
	if _, err := store.db.Exec("UPDATE jobs SET result=? WHERE id=?", string(raw), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(t.Context(), "node", "google", "experiment", "experiment:"+job.ID); err == nil {
		t.Fatal("legacy redirect authorized a correction")
	}
	broker := NewBroker(store)
	broker.workerSeen.Store(time.Now().Unix())
	broker.Report("node", Status{Enabled: true})
	broker.ScheduleOnce(t.Context(), time.Now())
	jobs, _ := store.Jobs(t.Context(), "node")
	if len(jobs) != 2 || jobs[0].Phase != "baseline" || jobs[0].State != "queued" {
		t.Fatal("country measurement not rescheduled", jobs)
	}
	if jobs[1].Result.Assessment != "anomaly_observed" || jobs[1].Result.Page.Signal != "homepage_hkredirect" {
		t.Fatal("historical result rewritten")
	}
	if jobs[0].Policy.Revision != policy.Revision {
		t.Fatal("policy changed")
	}
}

func TestScheduledRecoveryIsBoundedAndDurable(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	policy := testPolicy()
	policy.Services = []string{"google"}
	policy.AutoRecover = true
	policy.IntervalMinutes = 720
	if _, err := store.SavePolicy(ctx, "node", policy); err != nil {
		t.Fatal(err)
	}
	broker := NewBroker(store)
	broker.workerSeen.Store(time.Now().Unix())
	broker.Report("node", Status{Enabled: true})
	broker.ScheduleOnce(ctx, time.Now())
	broker.ScheduleOnce(ctx, time.Now())
	if job := finishScheduled(t, store, "JP"); job.Phase != "baseline" {
		t.Fatal(job.Phase)
	}
	broker.ScheduleOnce(ctx, time.Now())
	if job := finishScheduled(t, store, "US"); job.Phase != "experiment" {
		t.Fatal(job.Phase)
	}
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	broker.ScheduleOnce(ctx, time.Now())
	if job := finishScheduled(t, store, "DE"); job.Phase != "followup" {
		t.Fatal(job.Phase)
	}
	for attempt := 0; attempt < 5; attempt++ {
		broker.ScheduleOnce(ctx, time.Now())
	}
	jobs, _ := store.Jobs(ctx, "node")
	if len(jobs) != 3 || jobs[0].Result.Assessment != "other_region" {
		t.Fatalf("recovery loop: %+v", jobs)
	}
}

func TestRecoveryRequiresCurrentConsentAndEvidence(t *testing.T) {
	for _, auto := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "unknown"}[auto], func(t *testing.T) {
			store := testStore(t)
			policy := testPolicy()
			policy.AutoRecover, policy.IntervalMinutes, policy.Services = auto, 720, []string{"google"}
			policy, _ = store.SavePolicy(t.Context(), "node", policy)
			if _, err := store.Enqueue(t.Context(), "node", "google", "experiment", ""); err == nil {
				t.Fatal("manual recovery accepted")
			}
			broker := NewBroker(store)
			broker.workerSeen.Store(time.Now().Unix())
			broker.Report("node", Status{Enabled: true})
			broker.ScheduleOnce(t.Context(), time.Now())
			value := "JP"
			if auto {
				value = ""
			}
			finishScheduled(t, store, value)
			broker.ScheduleOnce(t.Context(), time.Now())
			jobs, _ := store.Jobs(t.Context(), "node")
			if len(jobs) != 1 {
				t.Fatal("unapproved recovery", len(jobs))
			}
			if _, err := store.Enqueue(t.Context(), "node", "google", "baseline", ""); err != nil {
				t.Fatal(err)
			}
			policy.Enabled = false
			if _, err := store.SavePolicy(t.Context(), "node", policy); err != nil {
				t.Fatal(err)
			}
			if job, err := store.Claim(t.Context()); err != nil || job != nil {
				t.Fatal("disable did not cancel", err)
			}
		})
	}
}
