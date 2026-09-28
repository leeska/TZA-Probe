package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leeska/TZA-Probe/internal/model"
)

func TestDistroLabel(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"debian", "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"13\"\nID=debian\n", "Debian 13"},
		{"ubuntu", "NAME=\"Ubuntu\"\nVERSION=\"24.04.1 LTS (Noble Numbat)\"\nVERSION_ID=\"24.04\"\nID=ubuntu\n", "Ubuntu 24.04"},
		{"alpine", "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.21.0\n", "Alpine 3.21.0"},
		{"rocky", "NAME=\"Rocky Linux\"\nID=\"rocky\"\nVERSION_ID=\"9.4\"\n", "Rocky 9.4"},
		{"no version id falls back to VERSION without the codename", "NAME=\"Slackware\"\nVERSION=\"15.0 (stable)\"\n", "Slackware 15.0"},
		{"rolling release has no version at all", "NAME=\"Arch Linux\"\nID=arch\n", "Arch"},
		{"id only", "ID=gentoo\n", "Gentoo"},
		{"name without version", "NAME=\"Debian GNU/Linux\"\n", "Debian"},
		{"unknown system names nothing", "PRETTY_NAME=\"\"\n", ""},
		{"not a release file", "not a release file\n", ""},
		{"comments and quoted values", "# comment\nNAME='Debian GNU/Linux'\nVERSION_ID='12'\n", "Debian 12"},
		{"a stray tab in the name is collapsed", "NAME=\"Debian	GNU/Linux\"\nVERSION_ID=\"13\"\n", "Debian 13"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := distroLabel(tc.content); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// Odd release files must never produce a label the metrics schema rejects:
// one bad host would otherwise fail every report it sends.
func TestDistroLabelStaysWithinMetricsLimit(t *testing.T) {
	long := "NAME=\"Very Long Distribution Name Which Goes On And On\"\nVERSION_ID=\"2026.09-rc1-with-an-enormous-suffix\"\n"
	label := distroLabel(long)
	if label == "" {
		t.Fatal("a long release file must still produce a label")
	}
	if len(label) > 60 {
		t.Fatalf("label longer than the metrics limit: %q", label)
	}
	metrics := model.Metrics{MemoryTotal: 1, Distro: label}
	if err := metrics.Validate(); err != nil {
		t.Fatalf("the panel would reject this label: %v", err)
	}
}

func TestDetectDistroFallsBackToGOOS(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "os-release")
	unknown := filepath.Join(dir, "unknown-release")
	known := filepath.Join(dir, "known-release")
	if err := os.WriteFile(unknown, []byte("not a release file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(known, []byte("NAME=\"Debian GNU/Linux\"\nVERSION_ID=\"13\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original := osReleasePath
	defer func() { osReleasePath = original }()
	for _, tc := range []struct{ path, want string }{
		{missing, runtime.GOOS},
		{unknown, runtime.GOOS},
		{known, "Debian 13"},
	} {
		osReleasePath = tc.path
		if got := detectDistro(); got != tc.want {
			t.Fatalf("%s: got %q want %q", filepath.Base(tc.path), got, tc.want)
		}
	}
	// The real host must always produce something for the panel to show.
	osReleasePath = original
	if got := Distro(); got == "" {
		t.Fatal("Distro() returned nothing on the real host")
	}
}
