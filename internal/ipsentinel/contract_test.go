package ipsentinel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fixtureRoot = "testdata/contracts/ipsentinel-executor-v1"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureConfig(t *testing.T) Config {
	t.Helper()
	var config Config
	if err := DecodeStrict(fixture(t, "examples/config.json"), &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func TestSharedContractVectors(t *testing.T) {
	var vectors struct {
		Valid   []struct{ File, Definition string }
		Invalid []struct {
			Name, Base, Definition string
			Replace                []any
			Value                  any
			RejectedBy             string `json:"rejected_by"`
		}
		CanonicalConfigSHA256 string `json:"canonical_config_sha256"`
	}
	if err := json.Unmarshal(fixture(t, "vectors.json"), &vectors); err != nil {
		t.Fatal(err)
	}
	profiles := map[string]Profile{}
	for _, profile := range DefaultMockProfiles() {
		profiles[profile.ProfileID] = profile
	}
	for _, vector := range vectors.Valid {
		t.Run(vector.File, func(t *testing.T) {
			if err := ValidateDefinition(vector.Definition, fixture(t, vector.File), profiles); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, vector := range vectors.Invalid {
		t.Run(vector.Name, func(t *testing.T) {
			value, err := ParseStrictJSON(fixture(t, vector.Base))
			if err != nil {
				t.Fatal(err)
			}
			target := value
			for _, part := range vector.Replace[:len(vector.Replace)-1] {
				switch key := part.(type) {
				case string:
					target = target.(map[string]any)[key]
				case float64:
					target = target.([]any)[int(key)]
				}
			}
			switch key := vector.Replace[len(vector.Replace)-1].(type) {
			case string:
				target.(map[string]any)[key] = vector.Value
			case float64:
				target.([]any)[int(key)] = vector.Value
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateDefinition(vector.Definition, raw, profiles)
			want := ErrSchema
			if vector.RejectedBy == "semantic" {
				want = ErrSemantic
			}
			if !errors.Is(err, want) {
				t.Fatalf("want %v, got %v", want, err)
			}
		})
	}
	digest, err := ConfigSHA256(fixtureConfig(t))
	if err != nil || digest != vectors.CanonicalConfigSHA256 {
		t.Fatalf("digest=%s err=%v", digest, err)
	}
}

func TestPinnedContractChecksums(t *testing.T) {
	checked := 0
	for _, line := range strings.Split(string(fixture(t, "SHA256SUMS")), "\n") {
		if line == "" {
			continue
		}
		digest, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatal("invalid checksum entry")
		}
		name, copied := strings.CutPrefix(name, "contracts/ipsentinel-executor-v1/")
		if !copied {
			continue
		}
		sum := sha256.Sum256(fixture(t, name))
		if hex.EncodeToString(sum[:]) != digest {
			t.Fatalf("checksum mismatch: %s", name)
		}
		checked++
	}
	if checked != 41 {
		t.Fatalf("checked %d contract artifacts", checked)
	}
}

func TestStrictBoundaries(t *testing.T) {
	for _, raw := range []string{
		`{"x":1,"x":2}`, `{"x":NaN}`, `{} {}`, `{"x":"\ud800"}`, `{"x":"\udfff"}`,
		strings.Repeat("[", 17) + "0" + strings.Repeat("]", 17),
	} {
		if _, err := ParseStrictJSON([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	raw := fixture(t, "examples/hello.response.json")
	for _, replacement := range []struct{ old, new string }{
		{`"ok": true`, `"ok": null`},
		{`"hard_limits_available": true`, `"hard_limits_available": null`},
		{`"database_version": 2`, `"database_version": 9007199254740992`},
		{`"features": [`, `"features": null, "unused": [`},
	} {
		mutated := bytes.Replace(raw, []byte(replacement.old), []byte(replacement.new), 1)
		if _, err := ValidateResponse(mutated, nil); err == nil {
			t.Errorf("accepted %s", replacement.new)
		}
	}
}

func newUDSFixture(t *testing.T) (*MockExecutor, *Client) {
	t.Helper()
	dir, err := os.MkdirTemp("", "tza-uds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	target := Target{ControllerID: "22222222-2222-4222-8222-222222222222", NodeID: "33333333-3333-4333-8333-333333333333"}
	mock, err := NewMockExecutor(filepath.Join(dir, "control.sock"), target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mock.Close() })
	client := NewClient(mock.SocketPath, target)
	t.Cleanup(client.Close)
	return mock, client
}

func TestUDSLifecycleAndReplay(t *testing.T) {
	mock, client := newUDSFixture(t)
	ctx := context.Background()
	hello, err := client.Hello(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	fence := Fence{ExecutorInstanceID: hello.Status.ExecutorInstanceID, Epoch: hello.Status.Control.Epoch}
	requestID := NewUUID()
	receipt, err := client.ConfigApply(ctx, requestID, fence, 0, fixtureConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := client.ConfigApply(ctx, requestID, fence, 0, fixtureConfig(t))
	if err != nil || replayed != receipt {
		t.Fatal(replayed, err)
	}
	now := time.Now().UTC()
	job := JobParams{Fence: fence, JobID: NewUUID(), Config: receipt.Config, ServiceID: "google", Purpose: PurposeObserve, Phase: PhaseBaseline,
		NotBefore: now.Add(-time.Minute).Format(time.RFC3339), ExpiresAt: now.Add(time.Minute).Format(time.RFC3339), TimeoutMS: 120000}
	admission, err := client.RunSubmit(ctx, "", job)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := client.RunSubmit(ctx, "", job)
	if err != nil || duplicate.Run.RunID != admission.Run.RunID || !duplicate.Duplicate {
		t.Fatal(duplicate, err)
	}
	changed := job
	changed.Phase = PhaseFollowup
	_, err = client.RunSubmit(ctx, "", changed)
	var rpc *RPCError
	if !errors.As(err, &rpc) || rpc.Err.Code != ErrCodeIdempotencyConflict {
		t.Fatal(err)
	}
	cancelled, err := client.RunCancel(ctx, "", fence, job.JobID)
	if err != nil || cancelled.State != StateCancelled {
		t.Fatal(cancelled, err)
	}
	page, err := client.RecordsList(ctx, "", 0, 50)
	if err != nil || len(page.Records) != 3 {
		t.Fatal(page, err)
	}
	ack, err := client.RecordsAck(ctx, "", fence, page.NextSeq)
	if err != nil || ack.AckedSeq != page.NextSeq {
		t.Fatal(ack, err)
	}
	pause, err := client.ControlPause(ctx, "", fence)
	if err != nil || !pause.Paused {
		t.Fatal(pause, err)
	}
	mock.TriggerAutoPause("rate_limited")
	_, err = client.ControlResume(ctx, "", fence, pause.PauseGeneration)
	if !errors.As(err, &rpc) || rpc.Err.Code != ErrCodePauseConflict {
		t.Fatal(err)
	}
	status, err := client.StatusGet(ctx, "")
	if err != nil || status.Budget.UsedRuns != 1 || !status.Control.Paused {
		t.Fatal(status, err)
	}
}

func TestCanonicalJSONVectors(t *testing.T) {
	for _, input := range []string{"1e+30", "1e-7", "0.000001", "-0", "333333333.33333329"} {
		parsed, err := ParseStrictJSON([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		result, err := CanonicalJSON(parsed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := strconv.ParseFloat(string(result), 64); err != nil {
			t.Fatal(err)
		}
	}
}
