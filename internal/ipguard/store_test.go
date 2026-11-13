package ipguard

import (
	"context"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{Enabled: true, ExpectedIP: "8.8.8.8", Country: "US", Locale: "en-US", Timezone: "UTC", Services: []string{"google", "meta"}, TimeoutSeconds: 30, NetworkBytes: 1 << 20, DwellSeconds: 2}
}

func testStore(t *testing.T) *Database {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestPolicyAndDestinations(t *testing.T) {
	if err := testPolicy().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "100.64.1.1", "169.254.1.1", "192.0.2.2", "224.0.0.1", "240.0.0.1", "::1", "::ffff:8.8.8.8", "64:ff9b::808:808", "2001:db8::1", "2002:808:808::1", "3fff::1", "fc00::1", "fe80::1"} {
		if PublicAddress(netip.MustParseAddr(address)) {
			t.Errorf("accepted reserved %s", address)
		}
	}
	for _, address := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !PublicAddress(netip.MustParseAddr(address)) {
			t.Errorf("rejected public %s", address)
		}
	}
	for _, host := range []string{"www.google.com.evil.test", "127.0.0.1", "www.google.com:443", "accounts.google.com", "www.google.com.", "WWW.GOOGLE.COM"} {
		if PermittedHost("google", host) {
			t.Errorf("accepted %s", host)
		}
	}
	if PermittedHost("unknown", "api64.ipify.org") {
		t.Fatal("unknown service accepted")
	}
	if !PermittedHost("meta", "scontent-sin6-1.xx.fbcdn.net") || PermittedHost("meta", "scontent-x.evil.xx.fbcdn.net") {
		t.Fatal("meta shards")
	}
	for _, origin := range []string{"https://api64.ipify.org", "https://www.google.com/a", "https://www.google.com:443", "https://www.google.com?x=1", "https://a@www.google.com", "https://www.gstatic.com"} {
		if PermittedPageOrigin("google", origin) {
			t.Errorf("accepted page %s", origin)
		}
	}
	if !PermittedPageOrigin("google", "https://www.google.com.hk") {
		t.Fatal("redirect evidence rejected")
	}
}

func TestManualChecksDoNotHaveDailyQuota(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "guard.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	if _, err := store.SavePolicy(ctx, "node", policy); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE ipguard_policies SET value=json_set(value,'$.daily_runs',1,'$.daily_network_bytes',1) WHERE node_id='node'"); err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(ctx, "node", "google", "baseline", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, err = store.Enqueue(ctx, "node", "meta", "baseline", "")
	if err != nil {
		t.Fatal(err)
	}
	store.Cancel(ctx, job.ID)
	for attempt := 0; attempt < 40; attempt++ {
		job, err := store.Enqueue(ctx, "node", "google", "baseline", "")
		if err != nil {
			t.Fatal("historical checks blocked manual check", attempt, err)
		}
		if _, err := store.Enqueue(ctx, "node", "meta", "baseline", ""); err == nil {
			t.Fatal("concurrent task admitted")
		}
		if err := store.Cancel(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaimConcurrentCancellationAndExpiration(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	policy, err := store.SavePolicy(ctx, "first", testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SavePolicy(ctx, "second", testPolicy()); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"first", "second"} {
		if _, err := store.Enqueue(ctx, node, "google", "baseline", ""); err != nil {
			t.Fatal(err)
		}
	}
	claimed := make(chan *Job, 8)
	var workers sync.WaitGroup
	for attempt := 0; attempt < 8; attempt++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			job, err := store.Claim(ctx)
			if err != nil {
				t.Error(err)
			}
			if job != nil {
				claimed <- job
			}
		}()
	}
	workers.Wait()
	close(claimed)
	if len(claimed) != 1 {
		t.Fatalf("claimed %d", len(claimed))
	}
	job := <-claimed
	if _, err := store.Running(ctx, job.ID, "wrong"); err == nil {
		t.Fatal("wrong lease accepted")
	}
	if _, err := store.db.Exec("UPDATE ipguard_jobs SET started_at=? WHERE id=?", time.Now().Add(-time.Hour).Unix(), job.ID); err != nil {
		t.Fatal(err)
	}
	next, err := store.Claim(ctx)
	if err != nil || next == nil || next.ID == job.ID {
		t.Fatalf("expired job stalls queue: %v", err)
	}
	if err := store.Cancel(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, next.ID, next.Lease, Result{Execution: "completed"}); err == nil {
		t.Fatal("cancelled job accepted result")
	}
	policy.Enabled = false
	if _, err := store.SavePolicy(ctx, "first", policy); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SavePolicy(ctx, "first", policy); err == nil {
		t.Fatal("stale revision accepted")
	}
}

func TestEvidenceCannotInferIPCountry(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	if _, err := store.SavePolicy(ctx, "node", testPolicy()); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://api64.ipify.org", "https://www.google.com"} {
		if _, err := store.Enqueue(ctx, "node", "google", "baseline", ""); err != nil {
			t.Fatal(err)
		}
		job, err := store.Claim(ctx)
		if err != nil || job == nil {
			t.Fatal(err)
		}
		result := Result{Execution: "completed", BrowserVersion: "chromium-test", Sandbox: true, NetworkIsolated: true, EgressBefore: job.Policy.ExpectedIP, EgressAfter: job.Policy.ExpectedIP, Page: Page{Outcome: "normal", FinalOrigin: origin, Signal: "homepage_hkredirect", Value: "NO_HKREDIRECT"}}
		if err := store.Complete(ctx, job.ID, job.Lease, result); err != nil {
			t.Fatal(err)
		}
		jobs, err := store.Jobs(ctx, "node")
		if err != nil {
			t.Fatal(err)
		}
		if jobs[0].Result.Assessment != "unknown" || jobs[0].Lease != "" {
			t.Fatalf("invalid evidence: %+v", jobs[0])
		}
	}
}

func TestCountriesAreComparedToSelectedTarget(t *testing.T) {
	for _, service := range []string{"google", "meta"} {
		for _, target := range []string{"US", "JP", "DE", "CN", "HK"} {
			t.Run(service+"/"+target, func(t *testing.T) {
				store := testStore(t)
				policy := testPolicy()
				policy.Country = target
				if _, err := store.SavePolicy(t.Context(), "node", policy); err != nil {
					t.Fatal(err)
				}
				for _, observed := range []string{"US", "JP", "DE", "CN", "HK", "TH", "", "NO_HKREDIRECT", "all"} {
					if _, err := store.Enqueue(t.Context(), "node", service, "baseline", ""); err != nil {
						t.Fatal(err)
					}
					job, err := store.Claim(t.Context())
					if err != nil || job == nil {
						t.Fatal(err)
					}
					origin := "https://www.google.com"
					if service == "meta" {
						origin = "https://www.facebook.com"
					}
					result := Result{Execution: "completed", BrowserVersion: "test", Sandbox: true, NetworkIsolated: true, EgressBefore: policy.ExpectedIP, EgressAfter: policy.ExpectedIP, Page: Page{Outcome: "normal", FinalOrigin: origin, Signal: CountrySignal(service), Value: observed}}
					if err := store.Complete(t.Context(), job.ID, job.Lease, result); err != nil {
						t.Fatal(err)
					}
					jobs, _ := store.Jobs(t.Context(), "node")
					expected := "unknown"
					if len(observed) == 2 {
						expected = "other_region"
						if observed == target {
							expected = "target_observed"
						}
					}
					if jobs[0].Result.Assessment != expected {
						t.Fatalf("target=%s observed=%s: %s", target, observed, jobs[0].Result.Assessment)
					}
				}
			})
		}
	}
}
