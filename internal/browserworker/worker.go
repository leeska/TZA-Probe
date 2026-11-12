// Package browserworker is the isolated browser that reads Google and Meta page
// regions for the region drift check (internal/ipguard). It runs as its own
// service user in a network namespace with only loopback, and reaches the web
// exclusively through the Server's private worker socket, which relays every
// connection through the node's Agent. It replaces the Python/Playwright worker
// of the former ipsentinel repository with the same protocol and readings.
package browserworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipguard"
)

var identity = regexp.MustCompile(`^[a-f0-9]{48}$`)

// Worker is the claim loop.
type Worker struct {
	Runner
	client *Client
	// Poll is the idle wait between empty claims.
	Poll time.Duration
}

func New(socket, state, chromium string) *Worker {
	return &Worker{Runner: Runner{Socket: socket, State: state, Chromium: chromium}, client: NewClient(socket), Poll: 2 * time.Second}
}

// Run claims and executes jobs until ctx ends. A second worker on the same
// state directory is refused (flock), as one Server runs one job at a time.
func (w *Worker) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Join(w.State, "jobs"), 0700); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return errors.New("browser worker must not run as root")
	}
	if !Isolated(w.ifaces()) {
		return errors.New("browser worker requires a private network namespace with only loopback (systemd PrivateNetwork=yes)")
	}
	lock, err := os.OpenFile(filepath.Join(w.State, "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another browser worker is using this state directory")
	}
	if _, err := os.Stat(w.Chromium); err != nil {
		return fmt.Errorf("chromium not found at %s: %w", w.Chromium, err)
	}
	if !FontsInstalled() {
		return errors.New("no fonts installed (fontconfig): Chromium stalls on pages with web fonts; install fontconfig and fonts-dejavu-core")
	}
	slog.Info("browser worker started", "socket", w.Socket, "state", w.State, "chromium", w.Chromium)
	for ctx.Err() == nil {
		if err := w.cycle(ctx); err != nil {
			slog.Warn("browser worker", "error", err)
			sleep(ctx, 3*time.Second)
		}
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (w *Worker) cycle(ctx context.Context) error {
	if err := w.deliver(ctx); err != nil {
		return err
	}
	var job *ipguard.Job
	if err := w.client.Call(ctx, "POST", "/claim", nil, "", &job); err != nil {
		return err
	}
	if job == nil {
		sleep(ctx, w.Poll)
		return nil
	}
	if err := w.run(ctx, *job); err != nil {
		return err
	}
	return w.deliver(ctx)
}

// run executes one job, checking every second that the Server still wants it.
// The result is written to disk first, so a crash or a lost reply only delays it.
func (w *Worker) run(parent context.Context, job ipguard.Job) error {
	if !identity.MatchString(job.ID) || !identity.MatchString(job.Lease) {
		return errors.New("invalid job identity")
	}
	work := filepath.Join(w.State, "jobs", job.ID)
	if err := os.Mkdir(work, 0700); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(work, "job.json"), job); err != nil {
		return err
	}
	timeout := time.Duration(job.Policy.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	slog.Info("browser job started", "job", job.ID, "service", job.Service, "phase", job.Phase)
	results := make(chan ipguard.Result, 1)
	go func() { results <- w.Execute(ctx, job) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	cancelled := false
	var result ipguard.Result
wait:
	for {
		select {
		case result = <-results:
			break wait
		case <-ctx.Done():
			// Deadline or shutdown: Execute unwinds through its cancelled CDP calls.
			result = <-results
			if parent.Err() == nil {
				result = ipguard.Result{Execution: "failed", Reason: "timeout", Page: ipguard.Page{Outcome: "unknown"}, DeniedOrigins: result.DeniedOrigins, Assessment: "unknown", NetworkIsolated: result.NetworkIsolated, Sandbox: result.Sandbox, BrowserVersion: result.BrowserVersion}
			}
			break wait
		case <-ticker.C:
			var protocol *ProtocolError
			err := w.client.Call(parent, "GET", "/jobs/"+job.ID+"/active", nil, job.Lease, nil)
			if errors.As(err, &protocol) && protocol.Status == 409 {
				cancelled = true
				cancel()
				<-results
				break wait
			}
		}
	}
	w.cleanTemporary()
	if cancelled || parent.Err() != nil {
		slog.Info("browser job stopped", "job", job.ID, "cancelled", cancelled)
		return os.RemoveAll(work)
	}
	slog.Info("browser job finished", "job", job.ID, "execution", result.Execution, "reason", result.Reason)
	return writeJSON(filepath.Join(work, "result.json"), result)
}

func (w *Worker) cleanTemporary() {
	matches, _ := filepath.Glob(filepath.Join(w.State, "observe-*"))
	for _, path := range matches {
		os.RemoveAll(path)
	}
}

// deliver posts every stored result; a 409 means the job is no longer valid
// on the Server (expired, cancelled) and the result is dropped.
func (w *Worker) deliver(ctx context.Context) error {
	outputs, _ := filepath.Glob(filepath.Join(w.State, "jobs", "*", "result.json"))
	for _, output := range outputs {
		dir := filepath.Dir(output)
		var job ipguard.Job
		raw, err := os.ReadFile(filepath.Join(dir, "job.json"))
		if err == nil {
			err = json.Unmarshal(raw, &job)
		}
		result, readErr := os.ReadFile(output)
		if err != nil || readErr != nil || !identity.MatchString(job.ID) {
			os.RemoveAll(dir)
			continue
		}
		err = w.client.Call(ctx, "POST", "/jobs/"+job.ID+"/result", bytes.NewReader(result), job.Lease, nil)
		var protocol *ProtocolError
		if err != nil && !(errors.As(err, &protocol) && protocol.Status == 409) {
			return err
		}
		os.RemoveAll(dir)
	}
	// Jobs that never produced a result (crash mid-job) are stale once their lease is gone.
	dirs, _ := filepath.Glob(filepath.Join(w.State, "jobs", "*"))
	for _, dir := range dirs {
		if _, err := os.Stat(filepath.Join(dir, "result.json")); err == nil {
			continue
		}
		var job ipguard.Job
		raw, err := os.ReadFile(filepath.Join(dir, "job.json"))
		if err == nil && json.Unmarshal(raw, &job) == nil && identity.MatchString(job.ID) {
			if err := writeJSON(filepath.Join(dir, "result.json"), ipguard.Result{Execution: "failed", Reason: "browser_failed", Page: ipguard.Page{Outcome: "unknown"}, DeniedOrigins: []string{}, Assessment: "unknown"}); err == nil {
				continue
			}
		}
		os.RemoveAll(dir)
	}
	return nil
}

// writeJSON replaces path atomically and durably (temp file, fsync, rename, fsync dir).
func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(0600); err == nil {
		if _, err = file.Write(raw); err == nil {
			err = file.Sync()
		}
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// SelfTest is what the installer runs inside the worker's own sandbox: the
// namespace has only loopback, the user is not root, Chromium starts with its
// sandbox and every renderer is confined. No page outside data: is loaded.
type SelfTestReport struct {
	NetworkIsolated bool   `json:"network_isolated"`
	NotRoot         bool   `json:"not_root"`
	Fonts           bool   `json:"fonts"`
	BrowserVersion  string `json:"browser_version"`
	Sandbox         bool   `json:"sandbox"`
	SandboxDetail   string `json:"sandbox_detail"`
	Error           string `json:"error,omitempty"`
}

func (r *SelfTestReport) OK() bool {
	return r.NetworkIsolated && r.NotRoot && r.Fonts && r.Sandbox && r.Error == ""
}

func SelfTest(ctx context.Context, state, chromium string) SelfTestReport {
	runner := Runner{State: state, Chromium: chromium}
	report := SelfTestReport{NetworkIsolated: Isolated(runner.ifaces()), NotRoot: os.Geteuid() != 0, Fonts: FontsInstalled()}
	profile, err := profileDir(state, "selftest-")
	if err != nil {
		report.Error = err.Error()
		return report
	}
	defer os.RemoveAll(profile)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	browser, err := Start(ctx, Launch{Bin: chromium, Profile: profile})
	if err != nil {
		report.Error = err.Error()
		return report
	}
	defer browser.Close()
	report.BrowserVersion = browser.Version
	page, err := browser.Page(nil)
	if err == nil {
		_, err = Navigate(page, "data:text/html,<p>self-test</p>", 15*time.Second)
	}
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.Sandbox, report.SandboxDetail = SandboxEvidence(runner.procRoot(), browser.PID())
	return report
}
