package agentdist

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDirectoryOverrideChecksumAndRanges(t *testing.T) {
	dir := t.TempDir()
	body := []byte("fake agent binary for amd64")
	if err := os.WriteFile(filepath.Join(dir, Name("amd64")), body, 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Dist{Dir: dir, noEmbed: true}
	if got := d.Available(); len(got) != 1 || got[0] != "amd64" {
		t.Fatalf("available = %v, want [amd64]", got)
	}
	sum, err := d.Sum("amd64")
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(body)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("sum = %s", sum)
	}
	if _, err := d.Sum("arm64"); err != ErrMissing {
		t.Fatalf("arm64 should be missing, got %v", err)
	}
	if _, err := d.Sum("../etc"); err != ErrMissing {
		t.Fatalf("invalid arch must not touch the filesystem, got %v", err)
	}
	r := httptest.NewRequest("GET", "/dl/"+Name("amd64"), nil)
	r.Header.Set("Range", "bytes=5-9")
	w := httptest.NewRecorder()
	if err := d.ServeBinary(w, r, "amd64"); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusPartialContent || w.Body.String() != "agent" {
		t.Fatalf("range download: %d %q", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, Name("amd64")) {
		t.Fatalf("content-disposition = %q", cd)
	}
}

func TestParseDownload(t *testing.T) {
	cases := map[string][3]any{
		"tza-agent-linux-amd64":        {"amd64", false, true},
		"tza-agent-linux-arm64.sha256": {"arm64", true, true},
		"tza-agent-linux-mips":         {"", false, false},
		"tza-server-linux-amd64":       {"", false, false},
		"..":                           {"", false, false},
	}
	for name, want := range cases {
		arch, sum, ok := ParseDownload(name)
		if arch != want[0] || sum != want[1] || ok != want[2] {
			t.Errorf("%s: got (%s, %v, %v), want %v", name, arch, sum, ok, want)
		}
	}
}

func TestScriptIsPOSIXAndSecretFree(t *testing.T) {
	script := string(Script())
	if !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Fatal("script must start with a POSIX sh shebang")
	}
	for _, needle := range []string{"set -eu", "/dl/$NAME.sha256", "chmod 0600 \"$CONFIG\"", "systemctl restart tza-agent", "--uninstall", "<<'TZA_AGENT_SERVICE'", "--geo-provider", "--trace-method", "\"geo_provider\": \"%s\"", "\"trace_method\": \"%s\"",
		// 初始化系统两条路都要在：只写 systemd 时，Alpine/OpenRC 节点跑面板“接入”的一行命令
		// 会直接 fail（实测 kr/sg 两台因此长期停在旧版本），只能手工替换二进制。
		"INIT=openrc", "rc-service tza-agent start", "rc-update add tza-agent default", "<<'TZA_AGENT_INIT'"} {
		if !strings.Contains(script, needle) {
			t.Errorf("script missing %q", needle)
		}
	}
	// The script is served without authentication, so it must never carry
	// anything node- or server-specific.
	if regexp.MustCompile(`[0-9a-f]{64}`).MatchString(script) {
		t.Fatal("script must not contain a key")
	}
	// Bytes served must equal the embedded file so the /install.sh route can be
	// diffed against the repository copy.
	repo, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if string(repo) != script {
		t.Fatal("served script differs from repository copy")
	}
}

func TestEmbeddedCopiesWhenPresent(t *testing.T) {
	d := &Dist{}
	for _, arch := range d.Available() {
		sum, err := d.Sum(arch)
		if err != nil || len(sum) != 64 {
			t.Fatalf("%s: sum %q err %v", arch, sum, err)
		}
		again, _ := d.Sum(arch)
		if again != sum {
			t.Fatalf("%s: cached sum differs", arch)
		}
		w := httptest.NewRecorder()
		if err := d.ServeBinary(w, httptest.NewRequest("GET", "/dl/"+Name(arch), nil), arch); err != nil || w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("%s: serve %d %v", arch, w.Code, err)
		}
	}
}

// Deployments can preserve mtimes when installing or rolling back binaries.
func TestChecksumCacheDetectsPreservedTimestampReplacement(t *testing.T) {
	for _, mode := range []string{"in-place-size-change", "atomic-same-size-replacement", "in-place-same-size-change"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, Name("amd64"))
			stamp := time.Unix(1700000000, 0)
			old, next := []byte("old binary"), []byte("new binary")
			if mode == "in-place-size-change" {
				next = append(next, []byte(" with more bytes")...)
			}
			write := func(path string, data []byte) {
				t.Helper()
				if err := os.WriteFile(path, data, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			write(path, old)
			d := &Dist{Dir: dir, noEmbed: true}
			if _, err := d.Sum("amd64"); err != nil {
				t.Fatal(err)
			}
			if mode == "atomic-same-size-replacement" {
				replacement := filepath.Join(dir, "next")
				write(replacement, next)
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			} else {
				write(path, next)
			}
			want := sha256.Sum256(next)
			got, err := d.Sum("amd64")
			if err != nil || got != hex.EncodeToString(want[:]) {
				t.Fatalf("checksum after replacement = %s, %v; want %x", got, err, want)
			}
			w := httptest.NewRecorder()
			if err := d.ServeBinary(w, httptest.NewRequest("GET", "/dl/"+Name("amd64"), nil), "amd64"); err != nil || w.Body.String() != string(next) {
				t.Fatalf("served bytes do not match replacement: %q, %v", w.Body.String(), err)
			}
		})
	}
}
