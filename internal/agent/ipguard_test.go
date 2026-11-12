package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipguard"
)

func TestEgressPerCheckLimitAndLocalRevocation(t *testing.T) {
	directory := t.TempDir()
	policyPath := filepath.Join(directory, "policy.json")
	policy := EgressPolicy{Enabled: true, AllowedIPs: []string{"8.8.8.8"}, Services: []string{"google"}}
	writePolicy := func() {
		t.Helper()
		raw, _ := json.Marshal(policy)
		if err := os.WriteFile(policyPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writePolicy()
	job := ipguard.Job{ID: "job", Service: "google", StartedAt: time.Now().Unix(), Policy: ipguard.Policy{ExpectedIP: "8.8.8.8", NetworkBytes: 1 << 20, TimeoutSeconds: 120}}
	egress := &Egress{PolicyFile: policyPath}
	if !egress.consume(job, 1<<20) {
		t.Fatal("single check transfer refused")
	}
	if egress.consume(job, 1) {
		t.Fatal("single check exceeded limit")
	}
	for attempt := 0; attempt < 300; attempt++ {
		next := job
		next.ID = fmt.Sprint("job-", attempt)
		if !egress.consume(next, 1<<20) {
			t.Fatal("daily or cumulative cap retained", attempt)
		}
	}
	expired := job
	expired.ID = "expired"
	expired.StartedAt = time.Now().Add(-time.Hour).Unix()
	if egress.consume(expired, 0) {
		t.Fatal("expired job admitted")
	}
	egress.jobUsage["expired"] = egressUsage{Deadline: expired.StartedAt}
	job.ID = "current"
	if !egress.consume(job, 0) {
		t.Fatal("current job refused")
	}
	if _, exists := egress.jobUsage["expired"]; exists {
		t.Fatal("expired usage retained")
	}
	if egress.consume(job, -1) {
		t.Fatal("negative bytes accepted")
	}
	nextDeadline := job
	nextDeadline.StartedAt++
	if egress.consume(nextDeadline, 0) {
		t.Fatal("same job deadline extended")
	}
	var workers sync.WaitGroup
	var accepted int
	var acceptedMutex sync.Mutex
	for attempt := 0; attempt < 20; attempt++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if egress.consume(job, 1<<18) {
				acceptedMutex.Lock()
				accepted++
				acceptedMutex.Unlock()
			}
		}()
	}
	workers.Wait()
	if accepted != 4 {
		t.Fatal("parallel streams bypassed single check limit", accepted)
	}
	policy.Enabled = false
	writePolicy()
	if egress.consume(job, 0) {
		t.Fatal("revocation ignored")
	}
	policy.Enabled = true
	policy.AllowedIPs = []string{"1.1.1.1"}
	writePolicy()
	if egress.consume(job, 0) {
		t.Fatal("IP revocation ignored")
	}
	policy.AllowedIPs = []string{"8.8.8.8"}
	writePolicy()
	if err := os.Chmod(policyPath, 0644); err != nil {
		t.Fatal(err)
	}
	if egress.consume(job, 0) {
		t.Fatal("public policy accepted")
	}
}

func TestEgressNATBindsLocalSourceAndKeepsPublicExitAuthorization(t *testing.T) {
	policy := EgressPolicy{Enabled: true, AllowedIPs: []string{"8.8.8.8", "2606:4700:4700::1111"}, Services: []string{"google"}, SourceIPs: map[string]string{"8.8.8.8": "10.0.0.9"}}
	source, err := policy.sourceAddress("8.8.8.8")
	if err != nil || source.String() != "10.0.0.9" {
		t.Fatal("NAT source not selected", source, err)
	}
	source, err = policy.sourceAddress("2606:4700:4700::1111")
	if err != nil || source.String() != "2606:4700:4700::1111" {
		t.Fatal("direct public exit changed", source, err)
	}
	if _, err := policy.sourceAddress("10.0.0.9"); err == nil {
		t.Fatal("private source accepted as public exit")
	}
	for _, invalid := range []string{"", "127.0.0.1", "0.0.0.0", "169.254.1.1", "::1", "::ffff:10.0.0.9", "2606:4700:4700::1111", "224.0.0.1"} {
		policy.SourceIPs["8.8.8.8"] = invalid
		if _, err := policy.sourceAddress("8.8.8.8"); err == nil {
			t.Fatal("invalid NAT binding accepted", invalid)
		}
	}
	policy.SourceIPs = map[string]string{"1.1.1.1": "10.0.0.9"}
	directory := t.TempDir()
	path := filepath.Join(directory, "policy.json")
	raw, _ := json.Marshal(policy)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	egress := Egress{PolicyFile: path}
	if _, err := egress.policy(); err == nil {
		t.Fatal("mapping for unapproved exit accepted")
	}
	policy.SourceIPs = map[string]string{"8.8.8.8": "10.0.0.9"}
	raw, _ = json.Marshal(policy)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	job := ipguard.Job{ID: "nat", Service: "google", StartedAt: time.Now().Unix(), Policy: ipguard.Policy{ExpectedIP: "8.8.8.8", NetworkBytes: 1 << 20, TimeoutSeconds: 120}}
	if !egress.consume(job, 1) {
		t.Fatal("approved NAT public exit refused")
	}
	job.Policy.ExpectedIP = "10.0.0.9"
	if egress.consume(job, 1) {
		t.Fatal("source address bypassed public exit permission")
	}
}
