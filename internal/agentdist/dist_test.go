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
	for _, needle := range []string{"set -eu", "/dl/$NAME.sha256", "chmod 0600 \"$CONFIG\"", "systemctl restart tza-agent", "--uninstall", "<<'TZA_AGENT_SERVICE'"} {
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
