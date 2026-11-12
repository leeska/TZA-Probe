package ipguard

import (
	"testing"
	"time"
)

func TestTermsEvidenceIndependentOfSearchAndRecovery(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Result)
		want   string
	}{
		{"search challenge", func(result *Result) {}, "target_observed"},
		{"different country", func(result *Result) { result.GoogleTerms.Page.Value = "JP" }, "other_region"},
		{"exit mismatch", func(result *Result) { result.EgressAfter = "1.1.1.1" }, "unknown"},
		{"missing sandbox", func(result *Result) { result.Sandbox = false }, "unknown"},
		{"missing isolation", func(result *Result) { result.NetworkIsolated = false }, "unknown"},
		{"wrong origin", func(result *Result) { result.GoogleTerms.Page.FinalOrigin = "https://www.google.com" }, "unknown"},
		{"wrong signal", func(result *Result) { result.GoogleTerms.Page.Signal = "google_page_country" }, "unknown"},
		{"wrong code", func(result *Result) { result.GoogleTerms.Page.Value = "us" }, "unknown"},
		{"terms failed", func(result *Result) { result.GoogleTerms.Reason = "http_forbidden" }, "unknown"},
		{"location injected", func(result *Result) { result.GeolocationOverride = true }, "unknown"},
		{"transport failed", func(result *Result) { result.Reason = "origin_not_allowed" }, "unknown"},
		{"over budget", func(result *Result) { result.NetworkBytes = 2 << 20 }, "unknown"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			store := testStore(t)
			policy := testPolicy()
			policy.AutoRecover, policy.IntervalMinutes, policy.Services = true, 720, []string{"google"}
			if _, err := store.SavePolicy(t.Context(), "node", policy); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Enqueue(t.Context(), "node", "google", "baseline", ""); err != nil {
				t.Fatal(err)
			}
			job, err := store.Claim(t.Context())
			if err != nil || job == nil {
				t.Fatal(err)
			}
			result := Result{Execution: "failed", Reason: "challenge_detected", BrowserVersion: "test", Sandbox: true, NetworkIsolated: true, EgressBefore: policy.ExpectedIP, EgressAfter: policy.ExpectedIP, Page: Page{Outcome: "unknown"}, GoogleTerms: &RegionObservation{Page: Page{Outcome: "normal", FinalOrigin: "https://policies.google.com", Signal: "google_terms_country", Value: "US"}, Assessment: "forged"}}
			item.change(&result)
			if err := store.Complete(t.Context(), job.ID, job.Lease, result); err != nil {
				t.Fatal(err)
			}
			jobs, err := store.Jobs(t.Context(), "node")
			if err != nil || len(jobs) != 1 {
				t.Fatal(err)
			}
			if jobs[0].Result.Assessment != "unknown" || jobs[0].Result.GoogleTerms.Assessment != item.want {
				t.Fatalf("unexpected assessment: %+v", jobs[0].Result)
			}
			broker := NewBroker(store)
			broker.workerSeen.Store(time.Now().Unix())
			broker.Report("node", Status{Enabled: true})
			broker.ScheduleOnce(t.Context(), time.Now())
			jobs, _ = store.Jobs(t.Context(), "node")
			if len(jobs) != 1 {
				t.Fatal("terms evidence triggered recovery")
			}
		})
	}
}

func TestTermsAllowlistIsNarrowAndNotSearchOrigin(t *testing.T) {
	if !PermittedHost("google", "policies.google.com") || PermittedHost("meta", "policies.google.com") || PermittedHost("google", "policies.google.com.evil") || PermittedPageOrigin("google", "https://policies.google.com") {
		t.Fatal("terms origin confused with search or expanded to other services")
	}
}
