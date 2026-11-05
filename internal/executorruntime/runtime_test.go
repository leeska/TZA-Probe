package executorruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type fixture struct {
	manifest  tzapkg.ExecutorManifest
	archive   []byte
	signature []byte
	keys      map[string]ed25519.PublicKey
	private   ed25519.PrivateKey
	host      Host
	target    ipsentinel.Target
	source    string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile("../../pkg/tzapkg/testdata/runtime/executor.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := tzapkg.ParseExecutor(raw)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := fixture{
		manifest: manifest, keys: map[string]ed25519.PublicKey{"test-only": public}, private: private,
		host: Host{"linux-amd64", "1.9.2", "1.9.2"},
		target: ipsentinel.Target{
			ControllerID: "22222222-2222-4222-8222-222222222222",
			NodeID:       "33333333-3333-4333-8333-333333333333",
		},
		source: t.TempDir(),
	}
	data.archive, data.signature = data.sign(t, manifest, manifest.ID, manifest.Version)
	for _, name := range []string{"runtime", "python", "chromium", "libraries"} {
		body := []byte("fixture " + name + ", no executable\n")
		digest := sha256.Sum256(body)
		if err := os.WriteFile(filepath.Join(data.source, hex.EncodeToString(digest[:])+".bin"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return data
}

func (data fixture) sign(t *testing.T, manifest tzapkg.ExecutorManifest, id, version string) ([]byte, []byte) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("executor.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buffer.Bytes()
	signature, err := json.Marshal(tzapkg.Sign(data.private, "test-only", id, version, archive))
	if err != nil {
		t.Fatal(err)
	}
	return archive, signature
}

func (data fixture) inspect(t *testing.T) *Release {
	t.Helper()
	release, err := Inspect(data.archive, data.signature, data.keys, data.host, data.target)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func approved(t *testing.T, release *Release) []byte {
	t.Helper()
	approval := release.Review().ApprovalTemplate
	approval.Approved = true
	raw, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newStore(t *testing.T) Store {
	t.Helper()
	return Store{Dir: filepath.Join(t.TempDir(), "runtime-store")}
}

func TestPlanBindsSignedReleaseToSelectedNode(t *testing.T) {
	data := newFixture(t)
	release := data.inspect(t)
	review := release.Review()
	if review.ApprovalTemplate.Approved || review.PlanSHA256 != review.ApprovalTemplate.PlanSHA256 || len(review.Plan.Artifacts) != 4 || len(review.Plan.Permissions) != 4 || review.Plan.Host != data.host || review.Plan.Target != data.target {
		t.Fatal(review)
	}
	var descriptor any
	if err := json.Unmarshal(data.manifest.Descriptor, &descriptor); err != nil {
		t.Fatal(err)
	}
	expectedDigest, err := ipsentinel.CanonicalSHA256(descriptor)
	if err != nil || expectedDigest != review.Plan.DescriptorSHA256 {
		t.Fatal(expectedDigest, review.Plan.DescriptorSHA256, err)
	}
	var total int64
	for _, artifact := range review.Plan.Artifacts {
		total += artifact.SizeBytes
	}
	if review.Plan.TotalBytes != total {
		t.Fatal(review.Plan.TotalBytes, total)
	}
	review.Plan.Permissions[0] = "altered"
	review.Plan.Artifacts[0].SHA256 = "altered"
	if release.Review().Plan.Permissions[0] == "altered" || release.Review().Plan.Artifacts[0].SHA256 == "altered" {
		t.Fatal("review mutated the verified release")
	}
}

func TestPlanRejectsUntrustedIncompatibleOrMismatchedReleases(t *testing.T) {
	for _, name := range []string{
		"untrusted", "malformed-key", "signature", "archive", "signed-package", "signed-version",
		"old-probe", "old-agent", "development-agent", "darwin", "other-architecture",
		"bad-controller", "bad-node", "unknown-signature-field", "duplicate-signature-field",
	} {
		t.Run(name, func(t *testing.T) {
			data := newFixture(t)
			switch name {
			case "untrusted":
				data.keys = nil
			case "malformed-key":
				data.keys["test-only"] = []byte("invalid")
			case "signature":
				var signature tzapkg.Signature
				json.Unmarshal(data.signature, &signature)
				signature.Sig = strings.Repeat("A", 88)
				data.signature, _ = json.Marshal(signature)
			case "archive":
				data.archive = append(data.archive, 'x')
			case "signed-package":
				data.archive, data.signature = data.sign(t, data.manifest, "other-package", data.manifest.Version)
			case "signed-version":
				data.archive, data.signature = data.sign(t, data.manifest, data.manifest.ID, "0.4.1")
			case "old-probe":
				data.host.ProbeVersion = "1.5.9"
			case "old-agent":
				data.host.AgentVersion = "1.5.9"
			case "development-agent":
				data.host.AgentVersion = "1.9.2-dev"
			case "darwin":
				data.host.Platform = "darwin-arm64"
			case "other-architecture":
				data.host.Platform = "linux-arm64"
			case "bad-controller":
				data.target.ControllerID = ""
			case "bad-node":
				data.target.NodeID = "../other-node"
			case "unknown-signature-field":
				data.signature = append(data.signature[:len(data.signature)-1], []byte(`,"url":"https://example.test"}`)...)
			case "duplicate-signature-field":
				data.signature = append(data.signature[:len(data.signature)-1], []byte(`,"package":"ipsentinel-runtime"}`)...)
			}
			if _, err := Inspect(data.archive, data.signature, data.keys, data.host, data.target); err == nil {
				t.Fatal("invalid release was accepted")
			}
		})
	}
}

func TestApprovalCannotFollowChangesToNodePermissionsOrVersion(t *testing.T) {
	for _, name := range []string{"node", "controller", "host-version", "release-version", "permissions", "compatibility"} {
		t.Run(name, func(t *testing.T) {
			data := newFixture(t)
			original := data.inspect(t)
			switch name {
			case "node":
				data.target.NodeID = "44444444-4444-4444-8444-444444444444"
			case "controller":
				data.target.ControllerID = "44444444-4444-4444-8444-444444444444"
			case "host-version":
				data.host.AgentVersion = "1.9.3"
			case "release-version":
				data.manifest.Version = "0.4.1"
				data.manifest.Descriptor = bytes.ReplaceAll(data.manifest.Descriptor, []byte("0.4.0"), []byte("0.4.1"))
			case "permissions":
				var descriptor ipsentinel.RuntimeDescriptor
				json.Unmarshal(data.manifest.Descriptor, &descriptor)
				descriptor.Permissions = descriptor.Permissions[:3]
				restricted := data.manifest
				restricted.Descriptor, _ = json.Marshal(descriptor)
				archive, signature := data.sign(t, restricted, restricted.ID, restricted.Version)
				var err error
				original, err = Inspect(archive, signature, data.keys, data.host, data.target)
				if err != nil {
					t.Fatal(err)
				}
			case "compatibility":
				data.manifest.Compatibility.ProbeMin = "1.9.0"
			}
			data.archive, data.signature = data.sign(t, data.manifest, data.manifest.ID, data.manifest.Version)
			changed := data.inspect(t)
			store := newStore(t)
			if _, err := store.Stage(context.Background(), changed, approved(t, original), data.source); err == nil {
				t.Fatal("old approval accepted for changed plan")
			}
			if _, err := os.Stat(store.Dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unapproved plan created staging files", err)
			}
		})
	}
}

func TestStageIsAtomicIdempotentAndExplicitlyInactive(t *testing.T) {
	data := newFixture(t)
	release := data.inspect(t)
	store := newStore(t)
	approval := approved(t, release)
	first, err := store.Stage(context.Background(), release, approval, data.source)
	if err != nil {
		t.Fatal(err)
	}
	if first.Activated || first.State != "staged" || first.PlanSHA256 != release.digest {
		t.Fatal(first)
	}
	for _, artifact := range first.Plan.Artifacts {
		info, err := os.Stat(filepath.Join(store.Dir, "staged", first.PlanSHA256, "artifacts", artifact.SHA256+".bin"))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 || info.Size() != artifact.SizeBytes {
			t.Fatal(info, err)
		}
	}
	second, err := store.Stage(context.Background(), release, approval, data.source)
	if err != nil || second.StagedAt != first.StagedAt {
		t.Fatal(second, err)
	}
	verified, err := store.Verify(context.Background(), first.PlanSHA256, data.keys, data.host, data.target)
	if err != nil || verified.PlanSHA256 != first.PlanSHA256 || verified.Activated {
		t.Fatal(verified, err)
	}
	for _, name := range []string{"pending", "locks"} {
		entries, err := os.ReadDir(filepath.Join(store.Dir, name))
		if err != nil || len(entries) != 0 {
			t.Fatal(entries, err)
		}
	}
	if _, err := store.Verify(context.Background(), first.PlanSHA256, nil, data.host, data.target); err == nil {
		t.Fatal("staged release still trusted after key removal")
	}
	otherNode := data.target
	otherNode.NodeID = "44444444-4444-4444-8444-444444444444"
	if _, err := store.Verify(context.Background(), first.PlanSHA256, data.keys, data.host, otherNode); err == nil {
		t.Fatal("staged receipt accepted on another node")
	}
}

func TestStageRejectsUnapprovedInvalidAndCancelledInputsWithoutPublishing(t *testing.T) {
	for _, name := range []string{"no-approval", "declined", "duplicate-approval-field", "unknown-approval-field", "short", "large", "wrong-hash", "missing", "symlink", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			data := newFixture(t)
			release := data.inspect(t)
			store := newStore(t)
			approval := approved(t, release)
			artifact := release.plan.Artifacts[0]
			source := filepath.Join(data.source, artifact.SHA256+".bin")
			ctx := context.Background()
			switch name {
			case "no-approval":
				approval = nil
			case "declined":
				approval, _ = json.Marshal(release.Review().ApprovalTemplate)
			case "duplicate-approval-field":
				approval = append(approval[:len(approval)-1], []byte(`,"approved":true}`)...)
			case "unknown-approval-field":
				approval = append(approval[:len(approval)-1], []byte(`,"postinstall":"run"}`)...)
			case "short":
				os.WriteFile(source, []byte("short"), 0o600)
			case "large":
				os.WriteFile(source, bytes.Repeat([]byte("x"), int(artifact.SizeBytes)+1), 0o600)
			case "wrong-hash":
				os.WriteFile(source, bytes.Repeat([]byte("x"), int(artifact.SizeBytes)), 0o600)
			case "missing":
				os.Remove(source)
			case "symlink":
				os.Remove(source)
				os.Symlink(filepath.Join(t.TempDir(), "outside"), source)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if _, err := store.Stage(ctx, release, approval, data.source); err == nil {
				t.Fatal("invalid stage succeeded")
			}
			for _, directory := range []string{"staged", "pending", "locks"} {
				entries, err := os.ReadDir(filepath.Join(store.Dir, directory))
				if !errors.Is(err, os.ErrNotExist) && (err != nil || len(entries) != 0) {
					t.Fatal(directory, entries, err)
				}
			}
		})
	}
}

func overwrite(t *testing.T, name string, raw []byte) {
	t.Helper()
	if err := os.Chmod(name, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0o400); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAndRetryRehashStoredFilesAndRejectForgedReceipts(t *testing.T) {
	for _, name := range []string{"artifact", "archive", "signature", "approval", "receipt-node", "receipt-active", "artifact-symlink"} {
		t.Run(name, func(t *testing.T) {
			data := newFixture(t)
			release := data.inspect(t)
			store := newStore(t)
			approval := approved(t, release)
			receipt, err := store.Stage(context.Background(), release, approval, data.source)
			if err != nil {
				t.Fatal(err)
			}
			bundle := filepath.Join(store.Dir, "staged", receipt.PlanSHA256)
			switch name {
			case "artifact":
				artifact := receipt.Plan.Artifacts[0]
				overwrite(t, filepath.Join(bundle, "artifacts", artifact.SHA256+".bin"), bytes.Repeat([]byte("x"), int(artifact.SizeBytes)))
			case "archive":
				overwrite(t, filepath.Join(bundle, "archive.zip"), append(data.archive, 'x'))
			case "signature":
				overwrite(t, filepath.Join(bundle, "signature.json"), []byte("{}"))
			case "approval":
				overwrite(t, filepath.Join(bundle, "approval.json"), []byte("{}"))
			case "receipt-node", "receipt-active":
				if name == "receipt-node" {
					receipt.Plan.Target.NodeID = "44444444-4444-4444-8444-444444444444"
				} else {
					receipt.State, receipt.Activated = "active", true
				}
				raw, _ := json.Marshal(receipt)
				overwrite(t, filepath.Join(bundle, "receipt.json"), raw)
			case "artifact-symlink":
				artifact := receipt.Plan.Artifacts[0]
				file := filepath.Join(bundle, "artifacts", artifact.SHA256+".bin")
				os.Remove(file)
				os.Symlink(filepath.Join(data.source, artifact.SHA256+".bin"), file)
			}
			if _, err := store.Verify(context.Background(), release.digest, data.keys, data.host, data.target); err == nil {
				t.Fatal("tampered staged bundle verified")
			}
			if _, err := store.Stage(context.Background(), release, approval, data.source); err == nil {
				t.Fatal("retry accepted tampered staged bundle")
			}
		})
	}
}

func TestStagingRejectsUnsafeDirectoriesAndSerializesWriters(t *testing.T) {
	t.Run("world-readable-store", func(t *testing.T) {
		data := newFixture(t)
		store := newStore(t)
		os.Mkdir(store.Dir, 0o755)
		release := data.inspect(t)
		if _, err := store.Stage(context.Background(), release, approved(t, release), data.source); err == nil {
			t.Fatal("permissive staging directory accepted")
		}
	})
	t.Run("symlink-store", func(t *testing.T) {
		data := newFixture(t)
		store := newStore(t)
		os.Symlink(t.TempDir(), store.Dir)
		release := data.inspect(t)
		if _, err := store.Stage(context.Background(), release, approved(t, release), data.source); err == nil {
			t.Fatal("symlink staging directory accepted")
		}
	})
	t.Run("symlink-subdirectory", func(t *testing.T) {
		data := newFixture(t)
		store := newStore(t)
		os.Mkdir(store.Dir, 0o700)
		outside := t.TempDir()
		os.Symlink(outside, filepath.Join(store.Dir, "staged"))
		release := data.inspect(t)
		if _, err := store.Stage(context.Background(), release, approved(t, release), data.source); err == nil {
			t.Fatal("symlink staging subdirectory accepted")
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Fatal(entries, err)
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		data := newFixture(t)
		release := data.inspect(t)
		approval := approved(t, release)
		store := newStore(t)
		var workers sync.WaitGroup
		results := make(chan error, 6)
		for range 6 {
			workers.Go(func() {
				_, err := store.Stage(context.Background(), release, approval, data.source)
				results <- err
			})
		}
		workers.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !strings.Contains(err.Error(), "locked") {
				t.Fatal(err)
			}
		}
		if successes == 0 {
			t.Fatal("no staging operation completed")
		}
		if _, err := store.Verify(context.Background(), release.digest, data.keys, data.host, data.target); err != nil {
			t.Fatal(err)
		}
	})
}
