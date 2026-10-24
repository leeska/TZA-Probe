package account

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureSaveAndCheck(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty dir: %v", err)
	}
	user, pass, created, err := Ensure(dir)
	if err != nil || !created || user != DefaultUsername || len(pass) != 16 {
		t.Fatalf("first Ensure: %q %q %v %v", user, pass, created, err)
	}
	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("account file: %v %v", info, err)
	}
	// A second Ensure keeps the account and never reveals a password.
	if user2, pass2, created2, err := Ensure(dir); err != nil || created2 || pass2 != "" || user2 != user {
		t.Fatalf("second Ensure: %q %q %v %v", user2, pass2, created2, err)
	}
	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Check(user, pass) || a.Check(user, pass+"x") || a.Check("root", pass) {
		t.Fatal("Check accepted a wrong pair or rejected the right one")
	}
	if err := Save(dir, " ops ", "correct horse"); err != nil {
		t.Fatal(err)
	}
	if a, _ = Load(dir); a.Username != "ops" || !a.Check("ops", "correct horse") || a.Check(user, pass) {
		t.Fatal("Save did not replace the account")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "."+FileName+"-*")); len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct{ user, pass string }{
		{"", "password1"},
		{"a b", "password1"},
		{"admin", "short"},
		{"012345678901234567890123456789012", "password1"},
	} {
		if Validate(c.user, c.pass) == nil {
			t.Errorf("accepted %q / %q", c.user, c.pass)
		}
	}
	if err := Validate("管理员", "12345678"); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptFileIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("corrupt file: %v", err)
	}
	if _, _, _, err := Ensure(dir); err == nil {
		t.Fatal("Ensure overwrote a corrupt account silently")
	}
}

func TestWriteInitialCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := WriteInitialCredentials(dir, DefaultUsername, "a-generated-password"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, InitialFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), DefaultUsername) || !strings.Contains(string(data), "a-generated-password") {
		t.Fatalf("credentials file is missing the credentials: %s", data)
	}
	info, err := os.Stat(filepath.Join(dir, InitialFileName))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credentials file mode: %v %v", info, err)
	}
}
