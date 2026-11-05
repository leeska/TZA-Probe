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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

type repositoryFixture struct {
	manifest  tzapkg.ExecutorManifest
	archive   []byte
	signature tzapkg.Signature
	keys      map[string]ed25519.PublicKey
	host      Host
	target    ipsentinel.Target
	artifacts map[string][]byte
}

func newRepositoryFixture(t *testing.T) repositoryFixture {
	t.Helper()
	raw, err := os.ReadFile("../../pkg/tzapkg/testdata/runtime/executor.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := tzapkg.ParseExecutor(raw)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
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
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := map[string][]byte{}
	for _, name := range []string{"runtime", "python", "chromium", "libraries"} {
		body := []byte("fixture " + name + ", no executable\n")
		digest := sha256.Sum256(body)
		artifacts[hex.EncodeToString(digest[:])] = body
	}
	return repositoryFixture{
		manifest: manifest, archive: archive.Bytes(),
		signature: tzapkg.Sign(private, "test-only", manifest.ID, manifest.Version, archive.Bytes()),
		keys:      map[string]ed25519.PublicKey{"test-only": public},
		host:      Host{"linux-amd64", "1.9.2", "1.9.2"},
		target: ipsentinel.Target{
			ControllerID: "22222222-2222-4222-8222-222222222222",
			NodeID:       "33333333-3333-4333-8333-333333333333",
		},
		artifacts: artifacts,
	}
}

func (fixture repositoryFixture) server(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Errorf("runtime request carried credentials")
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/packages/ipsentinel-runtime/download":
			body, err := io.ReadAll(io.LimitReader(request.Body, 4097))
			if err != nil || len(body) > 4096 {
				t.Errorf("invalid request body: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			var payload map[string]string
			if json.Unmarshal(body, &payload) != nil || payload["version"] != fixture.manifest.Version || payload["probe"] != fixture.host.ProbeVersion {
				t.Errorf("unexpected exact release request: %s", body)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if mode == "redirect-ticket" {
				http.Redirect(writer, request, "https://outside.example/ticket", http.StatusFound)
				return
			}
			address := server.URL + "/dl/ipsentinel-runtime/" + fixture.manifest.Version + "?exp=" + fmt.Sprint(time.Now().Add(5*time.Minute).Unix()) + "&sig=ticket"
			ticket := map[string]any{"url": address, "size": len(fixture.archive), "signature": fixture.signature, "receipt": "private-download-receipt-not-retained"}
			switch mode {
			case "foreign-ticket":
				ticket["url"] = strings.Replace(address, "127.0.0.1", "127.0.0.1.evil.example", 1)
			case "duplicate-query":
				ticket["url"] = address + "&sig=duplicate"
			case "expired-ticket":
				ticket["url"] = strings.Replace(address, "exp="+strings.Split(strings.Split(address, "exp=")[1], "&")[0], "exp=1", 1)
			case "wrong-size":
				ticket["size"] = len(fixture.archive) + 1
			case "unknown-ticket-field":
				writer.Header().Set("Content-Type", "application/json")
				writer.Write([]byte(`{"url":"invalid","size":1,"signature":{},"postinstall":"run"}`))
				return
			case "bad-signature":
				signature := fixture.signature
				signature.Sig = "invalid"
				ticket["signature"] = signature
			}
			writer.Header().Set("Content-Type", "application/json")
			json.NewEncoder(writer).Encode(ticket)
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/dl/"):
			if mode == "redirect-download" {
				http.Redirect(writer, request, "https://outside.example/archive", http.StatusFound)
				return
			}
			archive := fixture.archive
			if mode == "wrong-archive" {
				archive = append(bytes.Clone(archive), 'x')
			}
			writer.Header().Set("Content-Type", "application/zip")
			writer.Write(archive)
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/api/v1/packages/ipsentinel-runtime/versions/0.4.0/artifacts/"):
			digest := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
			body, ok := fixture.artifacts[digest]
			if !ok {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			body = bytes.Clone(body)
			if mode == "artifact-redirect" {
				http.Redirect(writer, request, "https://outside.example/artifact", http.StatusFound)
				return
			}
			if mode == "tampered-artifact" {
				body[0] ^= 1
			}
			if mode != "missing-digest" {
				responseDigest := digest
				if mode == "wrong-digest-header" {
					responseDigest = strings.Repeat("0", 64)
				}
				writer.Header().Set("X-Content-SHA256", responseDigest)
			}
			if mode == "encoded-artifact" {
				writer.Header().Set("Content-Encoding", "gzip")
			}
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.Write(body)
		default:
			t.Errorf("unexpected CC runtime request %s %s", request.Method, request.URL)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	return server
}

func TestCCRepositoryFetchesPinnedReleaseAndStreamsApprovedArtifacts(t *testing.T) {
	fixture := newRepositoryFixture(t)
	server := fixture.server(t, "ok")
	defer server.Close()
	repository, err := newRepository(server.URL, fixture.keys, nil)
	if err != nil {
		t.Fatal(err)
	}
	release, err := repository.Fetch(context.Background(), "0.4.0", fixture.host, fixture.target)
	if err != nil {
		t.Fatal(err)
	}
	if release.plan.Target != fixture.target || release.plan.Host != fixture.host || release.plan.ArchiveSHA256 != fixture.signature.SHA256 {
		t.Fatal(release.Review())
	}
	store := Store{Dir: filepath.Join(t.TempDir(), "runtime-store")}
	approval := release.Review().ApprovalTemplate
	approval.Approved = true
	approvalRaw, _ := json.Marshal(approval)
	receipt, err := repository.Stage(context.Background(), store, release, approvalRaw)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := store.Verify(context.Background(), release.digest, fixture.keys, fixture.host, fixture.target)
	if err != nil || receipt.State != "staged" || receipt.Activated || verified.PlanSHA256 != release.digest {
		t.Fatal(receipt, verified, err)
	}
}

func TestCCRepositoryRejectsUnsafeAddressTicketRedirectAndSignature(t *testing.T) {
	fixture := newRepositoryFixture(t)
	for _, address := range []string{"", "ftp://127.0.0.1", "http://example.com", "http://localhost", "https://u:p@example.com", "https://example.com/?token=secret", "https://example.com/#fragment", "https:opaque", "https://example.com\\@evil.example"} {
		t.Run("address/"+address, func(t *testing.T) {
			if _, err := NewRepository(address, fixture.keys); err == nil {
				t.Fatal("unsafe repository address accepted")
			}
		})
	}
	for _, mode := range []string{"foreign-ticket", "duplicate-query", "expired-ticket", "wrong-size", "unknown-ticket-field", "bad-signature", "redirect-ticket", "redirect-download", "wrong-archive"} {
		t.Run(mode, func(t *testing.T) {
			server := fixture.server(t, mode)
			defer server.Close()
			repository, err := newRepository(server.URL, fixture.keys, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.Fetch(context.Background(), "0.4.0", fixture.host, fixture.target); err == nil {
				t.Fatal("invalid or redirected CC response accepted")
			}
		})
	}
	t.Run("untrusted-key", func(t *testing.T) {
		server := fixture.server(t, "ok")
		defer server.Close()
		repository, _ := newRepository(server.URL, nil, nil)
		if repository != nil {
			t.Fatal("repository accepted no locally pinned key")
		}
	})
	t.Run("fetch-cancelled", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			<-request.Context().Done()
		}))
		defer server.Close()
		repository, _ := newRepository(server.URL, fixture.keys, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := repository.Fetch(ctx, "0.4.0", fixture.host, fixture.target); err == nil {
			t.Fatal("cancelled release fetch succeeded")
		}
	})
}

func TestCCArtifactCorruptionAndRevokedKeyNeverPublishOrVerify(t *testing.T) {
	for _, mode := range []string{"tampered-artifact", "artifact-redirect", "missing-digest", "wrong-digest-header", "encoded-artifact"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			server := fixture.server(t, mode)
			defer server.Close()
			repository, _ := newRepository(server.URL, fixture.keys, nil)
			release, err := repository.Fetch(context.Background(), "0.4.0", fixture.host, fixture.target)
			if err != nil {
				t.Fatal(err)
			}
			approval := release.Review().ApprovalTemplate
			approval.Approved = true
			approvalRaw, _ := json.Marshal(approval)
			store := Store{Dir: filepath.Join(t.TempDir(), "runtime-store")}
			if _, err := repository.Stage(context.Background(), store, release, approvalRaw); err == nil {
				t.Fatal("redirected or tampered artifact staged")
			}
			if _, err := os.Stat(filepath.Join(store.Dir, "staged", release.digest)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed stream published a staged receipt", err)
			}
		})
	}
	t.Run("revoked-key", func(t *testing.T) {
		fixture := newRepositoryFixture(t)
		server := fixture.server(t, "ok")
		defer server.Close()
		repository, _ := newRepository(server.URL, fixture.keys, nil)
		release, err := repository.Fetch(context.Background(), "0.4.0", fixture.host, fixture.target)
		if err != nil {
			t.Fatal(err)
		}
		store := Store{Dir: filepath.Join(t.TempDir(), "runtime-store")}
		approval := release.Review().ApprovalTemplate
		approval.Approved = true
		approvalRaw, _ := json.Marshal(approval)
		if _, err := repository.Stage(context.Background(), store, release, approvalRaw); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Verify(context.Background(), release.digest, nil, fixture.host, fixture.target); err == nil {
			t.Fatal("runtime verified after revoking the signing key")
		}
	})
}

func TestCCRepositoryStageSerializesConcurrentArtifactDownloads(t *testing.T) {
	fixture := newRepositoryFixture(t)
	server := fixture.server(t, "ok")
	defer server.Close()
	repository, _ := newRepository(server.URL, fixture.keys, nil)
	release, err := repository.Fetch(context.Background(), "0.4.0", fixture.host, fixture.target)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Dir: filepath.Join(t.TempDir(), "runtime-store")}
	approval := release.Review().ApprovalTemplate
	approval.Approved = true
	approvalRaw, _ := json.Marshal(approval)
	var workers sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		workers.Go(func() {
			_, err := repository.Stage(context.Background(), store, release, approvalRaw)
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
		t.Fatal("no CC staging request completed")
	}
}

func TestCCRepositoryAgainstPublishedFixture(t *testing.T) {
	address := os.Getenv("TZA_RUNTIME_REPOSITORY_URL")
	if address == "" {
		t.Skip("CC runtime fixture integration is not configured")
	}
	keysRaw, err := os.ReadFile(os.Getenv("TZA_RUNTIME_REPOSITORY_KEYS"))
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{}
	for _, line := range strings.Split(string(keysRaw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		id, key, err := tzapkg.ParsePublicKey(line)
		if err != nil {
			t.Fatal(err)
		}
		keys[id] = key
	}
	controller, node := os.Getenv("TZA_RUNTIME_TARGET_CONTROLLER"), os.Getenv("TZA_RUNTIME_TARGET_NODE")
	target := ipsentinel.Target{ControllerID: controller, NodeID: node}
	host := Host{
		Platform:     os.Getenv("TZA_RUNTIME_HOST_PLATFORM"),
		ProbeVersion: os.Getenv("TZA_RUNTIME_PROBE_VERSION"),
		AgentVersion: os.Getenv("TZA_RUNTIME_AGENT_VERSION"),
	}
	repository, err := NewRepository(address, keys)
	if err != nil {
		t.Fatal(err)
	}
	release, err := repository.Fetch(context.Background(), os.Getenv("TZA_RUNTIME_VERSION"), host, target)
	if err != nil {
		t.Fatal(err)
	}
	approval := release.Review().ApprovalTemplate
	approval.Approved = true
	approvalRaw, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Dir: os.Getenv("TZA_RUNTIME_STAGING_DIR")}
	receipt, err := repository.Stage(context.Background(), store, release, approvalRaw)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := store.Verify(context.Background(), release.digest, keys, host, target)
	if err != nil || receipt.State != "staged" || receipt.Activated || verified.PlanSHA256 != release.digest {
		t.Fatal(receipt, verified, err)
	}
}
