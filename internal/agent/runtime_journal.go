package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
)

const runtimeJournalLimit = 128 << 10

var runtimeServiceUnit = regexp.MustCompile(`^ipsentinel-[A-Za-z0-9@_.-]+\.service$`)

type FileRuntimePending struct {
	Directory string
}

func (journal FileRuntimePending) open() (*os.Root, error) {
	if !filepath.IsAbs(journal.Directory) || filepath.Clean(journal.Directory) != journal.Directory {
		return nil, errors.New("runtime journal requires an absolute canonical private directory")
	}
	if err := os.MkdirAll(journal.Directory, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(journal.Directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("runtime journal must be private and cannot be a symlink")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() {
		return nil, errors.New("runtime journal must be owned by the installer user")
	}
	return os.OpenRoot(journal.Directory)
}

func (journal FileRuntimePending) Lock() (func(), error) {
	root, err := journal.open()
	if err != nil {
		return nil, err
	}
	file, err := root.OpenFile("channel.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	root.Close()
	if err != nil {
		return nil, err
	}
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		file.Close()
		return nil, errors.New("runtime channel lock is not a private regular file")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another runtime installer is active on this node")
	}
	return func() {
		syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
	}, nil
}

func readRuntimeJournal(root *os.Root, name string) ([]byte, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !ok || int(owner.Uid) != os.Geteuid() || info.Size() > runtimeJournalLimit {
		return nil, errors.New("runtime journal entry must be a bounded private regular file owned by the installer")
	}
	raw, err := io.ReadAll(io.LimitReader(file, runtimeJournalLimit+1))
	if err != nil || len(raw) > runtimeJournalLimit {
		return nil, errors.New("runtime journal entry exceeds its limit")
	}
	return raw, nil
}

func syncRuntimeJournal(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func writeRuntimeJournal(root *os.Root, name string, raw []byte) error {
	if len(raw) > runtimeJournalLimit {
		return errors.New("runtime journal payload exceeds its limit")
	}
	temporary := ".pending-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := root.Rename(temporary, name); err != nil {
		return err
	}
	return syncRuntimeJournal(root)
}

func (journal FileRuntimePending) Identity() (string, error) {
	unlock, err := journal.Lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	root, err := journal.open()
	if err != nil {
		return "", err
	}
	defer root.Close()
	raw, err := readRuntimeJournal(root, "identity")
	if errors.Is(err, os.ErrNotExist) {
		identity := ipsentinel.NewUUID()
		return identity, writeRuntimeJournal(root, "identity", []byte(identity))
	}
	if err != nil || !ipsentinel.ValidUUID(string(raw)) {
		return "", errors.New("runtime journal identity is invalid; explicit local recovery is required")
	}
	return string(raw), nil
}

func (journal FileRuntimePending) Load() (*RuntimePending, error) {
	root, err := journal.open()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	raw, err := readRuntimeJournal(root, "pending.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending RuntimePending
	if ipsentinel.DecodeStrict(raw, &pending) != nil || !ipsentinel.ValidUUID(pending.Result.RequestID) || pending.Enrollment.Validate() != nil {
		return nil, errors.New("runtime journal is damaged; explicit local recovery is required")
	}
	return &pending, nil
}

func (journal FileRuntimePending) Save(pending RuntimePending) error {
	if !ipsentinel.ValidUUID(pending.Result.RequestID) || pending.Enrollment.Validate() != nil {
		return errors.New("invalid runtime journal identity")
	}
	switch pending.Result.State {
	case "awaiting_approval", "running", "staged", "installed", "selected", "drained", "started_paused", "unknown", "failed":
	default:
		return errors.New("invalid runtime journal state")
	}
	root, err := journal.open()
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return writeRuntimeJournal(root, "pending.json", raw)
}

func (journal FileRuntimePending) Clear(id string) error {
	if !ipsentinel.ValidUUID(id) {
		return errors.New("invalid runtime journal identity")
	}
	pending, err := journal.Load()
	if err != nil || pending == nil {
		return err
	}
	if pending.Result.RequestID != id {
		return errors.New("runtime journal identity changed")
	}
	root, err := journal.open()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir("receipts", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := root.Lstat("receipts")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("runtime receipt archive is not private")
	}
	archive, err := root.OpenRoot("receipts")
	if err != nil {
		return err
	}
	defer archive.Close()
	raw, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	if err := writeRuntimeJournal(archive, id+"-"+pending.Result.State+".json", raw); err != nil {
		return err
	}
	if err := root.Remove("pending.json"); err != nil {
		return err
	}
	return syncRuntimeJournal(root)
}

func validateRuntimePaths(config RuntimeInstallConfig) error {
	if os.Geteuid() != 0 {
		return errors.New("runtime installation requires a dedicated privileged Agent")
	}
	for _, name := range []string{config.CCKeysPath, config.StagingDir, config.InstallRoot, config.TrustedInstaller, config.StateDir, config.ConfigPath, config.PolicyPath} {
		if !filepath.IsAbs(name) || filepath.Clean(name) != name || name == "/" || strings.ContainsRune(name, 0) {
			return errors.New("runtime channel requires explicit canonical local paths")
		}
	}
	if !runtimeServiceUnit.MatchString(config.Unit) {
		return errors.New("runtime channel requires a fixed IPSentinel service")
	}
	for _, name := range []string{config.CCKeysPath, config.TrustedInstaller, config.StagingDir, config.InstallRoot} {
		for current := name; ; current = filepath.Dir(current) {
			info, err := os.Lstat(current)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err == nil {
				owner, ok := info.Sys().(*syscall.Stat_t)
				if !ok || owner.Uid != 0 || info.Mode().Perm()&0o022 != 0 || info.Mode()&os.ModeSymlink != 0 {
					return errors.New("runtime trust and installation paths must have root-owned ancestors without other-user write access")
				}
				if current != name && !info.IsDir() {
					return errors.New("runtime path ancestor is not a directory")
				}
			}
			if current == "/" {
				break
			}
		}
	}
	return nil
}

func loadRuntimeKeys(name string) (map[string]ed25519.PublicKey, error) {
	file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errors.New("runtime public key file must be a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, errors.New("runtime public key file exceeds its limit")
	}
	return parseRuntimeKeys(raw)
}
