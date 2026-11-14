package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leeska/TZA-ControlCenter/internal/channel"
	"github.com/leeska/TZA-ControlCenter/internal/httpjson"
	"github.com/leeska/TZA-ControlCenter/internal/model"
	"github.com/leeska/TZA-ControlCenter/internal/module"
	"github.com/leeska/TZA-ControlCenter/internal/storage"
)

func Migrations() []storage.Migration {
	return []storage.Migration{{Module: "backup", Version: 1, Name: "backup settings and runs", Up: func(c *storage.MigrationContext) error {
		for _, ddl := range []string{
			`CREATE TABLE IF NOT EXISTS backup_state(key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS backup_runs(id TEXT PRIMARY KEY, value TEXT NOT NULL, started_at INTEGER NOT NULL)`,
		} {
			if _, err := c.Tx.Exec(ddl); err != nil {
				return err
			}
		}
		return nil
	}}}
}

// DefaultDir is where a node keeps backups unless told otherwise.
const DefaultDir = "/var/backups/tza-probe"

// Target is a node directory that receives every backup.
type Target struct {
	NodeID string `json:"node_id"`
	Dir    string `json:"dir"`
}

// Config is the backup setup. Key holds the passphrase-derived key; it is
// never sent to the panel.
type Config struct {
	Enabled bool     `json:"enabled"`
	Hour    int      `json:"hour"` // UTC hour of the daily backup
	Keep    int      `json:"keep"`
	Targets []Target `json:"targets"`
	Key     *Key     `json:"key,omitempty"`
}

// TargetResult is one target's outcome in a run.
type TargetResult struct {
	NodeID string `json:"node_id"`
	Dir    string `json:"dir"`
	Error  string `json:"error,omitempty"`
	Kept   int    `json:"kept"`
}

// Run statuses.
const (
	RunRunning = "running"
	RunOK      = "ok"
	RunPartial = "partial" // some targets failed
	RunFailed  = "failed"
)

// Run is one backup.
type Run struct {
	ID       string         `json:"id"`
	Trigger  string         `json:"trigger"` // schedule | manual
	Started  int64          `json:"started_at"`
	Finished int64          `json:"finished_at,omitempty"`
	Status   string         `json:"status"`
	Name     string         `json:"name,omitempty"`
	Size     int64          `json:"size,omitempty"`
	DBSize   int64          `json:"db_size,omitempty"`
	SHA256   string         `json:"sha256,omitempty"`
	Error    string         `json:"error,omitempty"`
	Targets  []TargetResult `json:"targets"`
}

// Opener opens a management channel stream (channel.Hub).
type Opener interface {
	Open(ctx context.Context, node, kind string, params any) (net.Conn, error)
	Status(node string) channel.Status
}

// NodeLister is the part of the core store the module reads.
type NodeLister interface {
	Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error)
}

// Module serves 备份. DB and DataDir are nil / empty until the database is
// open.
type Module struct {
	DB      *sql.DB
	DataDir string
	Hub     Opener
	Nodes   NodeLister
	Now     func() time.Time // tests
	run     sync.Mutex
	mu      sync.Mutex
	running string
	// KeyParams overrides the scrypt cost (tests).
	KeyParams *Params
	// Events records alerts for the notification channels (nil: none).
	Events interface {
		RecordEvent(context.Context, model.Event) error
	}
}

func (*Module) ID() string                      { return "backup" }
func (*Module) Migrations() []storage.Migration { return Migrations() }

func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Module) state(ctx context.Context, key string) (string, error) {
	var v string
	err := m.DB.QueryRowContext(ctx, "SELECT value FROM backup_state WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (m *Module) setState(ctx context.Context, key, value string) error {
	_, err := m.DB.ExecContext(ctx, "INSERT INTO backup_state VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

// Config reads the setup (Keep 7, Hour 3 UTC when never saved).
func (m *Module) Config(ctx context.Context) (Config, error) {
	c := Config{Hour: 3, Keep: 7, Targets: []Target{}}
	raw, err := m.state(ctx, "config")
	if err != nil || raw == "" {
		return c, err
	}
	err = json.Unmarshal([]byte(raw), &c)
	if c.Targets == nil {
		c.Targets = []Target{}
	}
	return c, err
}

func (m *Module) saveConfig(ctx context.Context, c Config) error {
	raw, _ := json.Marshal(c)
	return m.setState(ctx, "config", string(raw))
}

func (m *Module) saveRun(ctx context.Context, r Run) error {
	raw, _ := json.Marshal(r)
	if _, err := m.DB.ExecContext(ctx, "INSERT INTO backup_runs VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", r.ID, string(raw), r.Started); err != nil {
		return err
	}
	// Keep the last 90 runs.
	_, err := m.DB.ExecContext(ctx, "DELETE FROM backup_runs WHERE id NOT IN (SELECT id FROM backup_runs ORDER BY started_at DESC LIMIT 90)")
	return err
}

// Runs lists the latest runs first.
func (m *Module) Runs(ctx context.Context, limit int) ([]Run, error) {
	rows, err := m.DB.QueryContext(ctx, "SELECT value FROM backup_runs ORDER BY started_at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var raw string
		var r Run
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &r) == nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

func (m *Module) capable(node string) bool {
	st := m.Hub.Status(node)
	return st.Connected && slices.Contains(st.Capabilities, channel.CapabilityBackup)
}

// Backup makes one backup now and sends it to every target. It returns the
// recorded run (also on failure, with Error set).
func (m *Module) Backup(ctx context.Context, trigger string) (Run, error) {
	if !m.run.TryLock() {
		return Run{}, errors.New("已经有一次备份在进行")
	}
	defer m.run.Unlock()
	now := m.now()
	var id [6]byte
	rand.Read(id[:])
	r := Run{ID: hex.EncodeToString(id[:]), Trigger: trigger, Started: now.Unix(), Status: RunRunning, Targets: []TargetResult{}, Name: "tza-probe-" + now.UTC().Format("20060102-150405") + Extension}
	m.mu.Lock()
	m.running = r.ID
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.running = ""
		m.mu.Unlock()
	}()
	fail := func(err error) (Run, error) {
		r.Status, r.Error, r.Finished = RunFailed, err.Error(), m.now().Unix()
		m.saveRun(context.WithoutCancel(ctx), r)
		m.alert(context.WithoutCancel(ctx))
		slog.Warn("backup failed", "error", err)
		return r, err
	}
	cfg, err := m.Config(ctx)
	if err != nil {
		return fail(err)
	}
	if cfg.Key == nil {
		return fail(errors.New("还没设备份口令"))
	}
	if len(cfg.Targets) == 0 {
		return fail(errors.New("还没选存放备份的节点"))
	}
	for _, t := range cfg.Targets {
		r.Targets = append(r.Targets, TargetResult{NodeID: t.NodeID, Dir: t.Dir})
	}
	m.saveRun(ctx, r)

	// Encrypt into a work file in the data directory (it may be large; the
	// size and checksum go into each target's request line first).
	work := filepath.Join(m.DataDir, ".backup-"+r.ID+".tmp")
	defer os.Remove(work)
	f, err := os.OpenFile(work, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail(err)
	}
	sum := sha256.New()
	enc, err := NewWriter(io.MultiWriter(f, sum), *cfg.Key)
	if err == nil {
		r.DBSize, err = Snapshot(ctx, m.DB, m.DataDir, enc)
	}
	if err == nil {
		err = enc.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	info, err := os.Stat(work)
	if err != nil {
		return fail(err)
	}
	r.Size, r.SHA256 = info.Size(), hex.EncodeToString(sum.Sum(nil))

	ok := 0
	for i, t := range cfg.Targets {
		kept, err := m.send(ctx, t, work, r, cfg.Keep)
		if err != nil {
			r.Targets[i].Error = err.Error()
			continue
		}
		r.Targets[i].Kept = kept
		ok++
	}
	r.Finished = m.now().Unix()
	switch ok {
	case len(cfg.Targets):
		r.Status = RunOK
	case 0:
		r.Status, r.Error = RunFailed, "没有一个节点存下了备份"
	default:
		r.Status = RunPartial
	}
	m.saveRun(context.WithoutCancel(ctx), r)
	m.alert(context.WithoutCancel(ctx))
	slog.Info("backup", "status", r.Status, "size", r.Size, "targets", ok)
	if r.Status == RunFailed {
		return r, errors.New(r.Error)
	}
	return r, nil
}

func (m *Module) send(ctx context.Context, t Target, work string, r Run, keep int) (int, error) {
	if !m.capable(t.NodeID) {
		return 0, errors.New("节点没有连接管理通道或没开备份能力：在节点上重跑接入命令并加 --manage")
	}
	f, err := os.Open(work)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	conn, err := m.Hub.Open(ctx, t.NodeID, channel.KindBackupWrite, channel.BackupWriteParams{Dir: t.Dir, Name: r.Name, Size: r.Size, SHA256: r.SHA256, Keep: keep})
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Hour))
	if _, err := io.Copy(conn, f); err != nil {
		return 0, fmt.Errorf("传输失败：%w", err)
	}
	var res channel.BackupWriteResult
	if err := channel.ReadLine(conn, &res); err != nil {
		return 0, err
	}
	if res.Error != "" {
		return 0, errors.New(res.Error)
	}
	return len(res.Kept), nil
}

// Run takes the daily backup once a day after the configured UTC hour.
func (m *Module) Run(ctx context.Context) {
	if m.DB == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		m.scheduled(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Module) scheduled(ctx context.Context) {
	m.alert(ctx)
	cfg, err := m.Config(ctx)
	if err != nil || !cfg.Enabled || cfg.Key == nil || len(cfg.Targets) == 0 {
		return
	}
	now := m.now().UTC()
	day := now.Format("2006-01-02")
	if last, _ := m.state(ctx, "last_scheduled"); last == day || now.Hour() < cfg.Hour {
		return
	}
	// Mark the day first: a failing backup waits for tomorrow (or 立即备份)
	// instead of running every five minutes.
	m.setState(ctx, "last_scheduled", day)
	m.Backup(ctx, "schedule")
}

// Problem is what is wrong with the backups now ("" when nothing, or when the
// daily backup is off): the latest run failed or only partly stored, or no
// backup succeeded for two days.
func (m *Module) Problem(ctx context.Context) (string, error) {
	cfg, err := m.Config(ctx)
	if err != nil || !cfg.Enabled || cfg.Key == nil || len(cfg.Targets) == 0 {
		return "", err
	}
	runs, err := m.Runs(ctx, 30)
	if err != nil || len(runs) == 0 {
		return "", err
	}
	latest := runs[0]
	switch latest.Status {
	case RunFailed:
		return "最近一次备份失败：" + latest.Error, nil
	case RunPartial:
		for _, t := range latest.Targets {
			if t.Error != "" {
				return "最近一次备份有节点没存下：" + t.Error, nil
			}
		}
	}
	for _, r := range runs {
		if r.Status == RunOK || r.Status == RunPartial {
			if age := m.now().Sub(time.Unix(r.Finished, 0)); age > 48*time.Hour {
				return fmt.Sprintf("已经 %d 天没有成功的备份", int(age.Hours()/24)), nil
			}
			return "", nil
		}
	}
	return "", nil
}

// alert sends a firing event when the backups turn bad and a recovery when
// they are good again (the state lives in backup_state "alerted").
func (m *Module) alert(ctx context.Context) {
	if m.Events == nil {
		return
	}
	problem, err := m.Problem(ctx)
	if err != nil {
		return
	}
	was, _ := m.state(ctx, "alerted")
	if (problem != "") == (was == "1") {
		return
	}
	message := problem
	if problem == "" {
		message = "备份已恢复正常"
	}
	if err := m.Events.RecordEvent(ctx, model.Event{NodeID: "backup", NodeName: "主控备份", Kind: model.EventBackup, Message: message, Active: problem != ""}); err != nil {
		slog.Warn("backup: alert not recorded", "error", err)
		return
	}
	flag := "0"
	if problem != "" {
		flag = "1"
	}
	m.setState(ctx, "alerted", flag)
}

// View is the setup as the panel sees it.
type View struct {
	Enabled       bool     `json:"enabled"`
	Hour          int      `json:"hour"`
	Keep          int      `json:"keep"`
	Targets       []Target `json:"targets"`
	HasPassphrase bool     `json:"has_passphrase"`
	Running       bool     `json:"running"`
	Runs          []Run    `json:"runs"`
}

// Input is what the panel saves; an empty Passphrase keeps the current key.
type Input struct {
	Enabled    bool     `json:"enabled"`
	Hour       int      `json:"hour"`
	Keep       int      `json:"keep"`
	Targets    []Target `json:"targets"`
	Passphrase string   `json:"passphrase"`
}

// MinPassphrase is the shortest passphrase accepted.
const MinPassphrase = 12

func (m *Module) Routes(r module.Routes) {
	if m.DB == nil {
		return
	}
	r.Admin("GET /api/v1/admin/backups", func(w http.ResponseWriter, req *http.Request) {
		cfg, err := m.Config(req.Context())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		runs, err := m.Runs(req.Context(), 30)
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		m.mu.Lock()
		running := m.running != ""
		m.mu.Unlock()
		httpjson.Write(w, 200, View{Enabled: cfg.Enabled, Hour: cfg.Hour, Keep: cfg.Keep, Targets: cfg.Targets, HasPassphrase: cfg.Key != nil, Running: running, Runs: runs})
	})
	r.Admin("PUT /api/v1/admin/backups", func(w http.ResponseWriter, req *http.Request) {
		var in Input
		if !httpjson.Decode(w, req, &in) {
			return
		}
		cfg, err := m.Config(req.Context())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		if in.Hour < 0 || in.Hour > 23 || in.Keep < 1 || in.Keep > channel.MaxBackupKeep {
			httpjson.Error(w, 400, "时间为 0–23 点，保留份数为 1–100")
			return
		}
		nodes, err := m.Nodes.Nodes(req.Context(), time.Now())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		targets := []Target{}
		seen := map[string]bool{}
		for _, t := range in.Targets {
			t.Dir = strings.TrimSpace(t.Dir)
			if t.Dir == "" {
				t.Dir = DefaultDir
			}
			if !slices.ContainsFunc(nodes, func(n model.NodeView) bool { return n.ID == t.NodeID }) {
				httpjson.Error(w, 400, "节点不存在")
				return
			}
			if !strings.HasPrefix(t.Dir, "/") || strings.Contains(t.Dir, "..") || len(t.Dir) > 255 || seen[t.NodeID+"\x00"+t.Dir] {
				httpjson.Error(w, 400, "目录须是绝对路径，同一节点同一目录只能填一次")
				return
			}
			seen[t.NodeID+"\x00"+t.Dir] = true
			targets = append(targets, t)
		}
		if in.Passphrase != "" {
			if utf8.RuneCountInString(in.Passphrase) < MinPassphrase {
				httpjson.Error(w, 400, fmt.Sprintf("备份口令至少 %d 个字符", MinPassphrase))
				return
			}
			params := DefaultParams
			if m.KeyParams != nil {
				params = *m.KeyParams
			}
			key, err := DeriveKey(in.Passphrase, params)
			if err != nil {
				httpjson.Fail(w, err)
				return
			}
			cfg.Key = &key
		}
		if in.Enabled && (cfg.Key == nil || len(targets) == 0) {
			httpjson.Error(w, 400, "开启每日备份前要先设备份口令、选至少一个节点")
			return
		}
		cfg.Enabled, cfg.Hour, cfg.Keep, cfg.Targets = in.Enabled, in.Hour, in.Keep, targets
		if err := m.saveConfig(req.Context(), cfg); err != nil {
			httpjson.Fail(w, err)
			return
		}
		httpjson.Write(w, 200, map[string]bool{"ok": true})
	})
	// 立即备份：在后台跑，页面轮询 GET 的 running 与记录。
	r.Admin("POST /api/v1/admin/backups/run", func(w http.ResponseWriter, req *http.Request) {
		cfg, err := m.Config(req.Context())
		if err != nil {
			httpjson.Fail(w, err)
			return
		}
		if cfg.Key == nil || len(cfg.Targets) == 0 {
			httpjson.Error(w, 400, "先设备份口令、选存放备份的节点")
			return
		}
		m.mu.Lock()
		busy := m.running != ""
		m.mu.Unlock()
		if busy {
			httpjson.Error(w, 409, "已经有一次备份在进行")
			return
		}
		go m.Backup(context.Background(), "manual")
		httpjson.Write(w, 202, map[string]bool{"started": true})
	})
}

// Overview is one card: when the last good backup was made.
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	if m.DB == nil {
		return nil, nil
	}
	cfg, err := m.Config(ctx)
	if err != nil {
		return nil, err
	}
	card := module.Card{ID: "backup", Title: "备份", Page: "backup"}
	runs, err := m.Runs(ctx, 30)
	if err != nil {
		return nil, err
	}
	var last *Run
	for i := range runs {
		if runs[i].Status == RunOK || runs[i].Status == RunPartial {
			last = &runs[i]
			break
		}
	}
	switch {
	case cfg.Key == nil || len(cfg.Targets) == 0:
		card.Value, card.Tone, card.Note = "未设置", "warn", "数据只有主控这一份：设口令、选节点后开启每日备份"
	case last == nil:
		card.Value, card.Tone, card.Note = "还没有", "warn", "还没有成功的备份"
	default:
		age := m.now().Sub(time.Unix(last.Finished, 0))
		card.Value = time.Unix(last.Finished, 0).Local().Format("01-02 15:04")
		card.Tone, card.Note = "good", "最近一次成功，"+model.HumanBytes(last.Size)
		if age > 48*time.Hour {
			card.Tone, card.Note = "bad", fmt.Sprintf("已经 %d 天没有成功备份", int(age.Hours()/24))
		} else if last.Status == RunPartial {
			card.Tone, card.Note = "warn", "最近一次只有部分节点存下"
		}
	}
	if len(runs) > 0 && runs[0].Status == RunFailed {
		card.Items = append(card.Items, module.CardItem{Label: "最近一次失败", Note: runs[0].Error, Tone: "bad"})
		if card.Tone == "good" {
			card.Tone = "warn"
		}
	}
	if !cfg.Enabled && cfg.Key != nil && len(cfg.Targets) > 0 {
		card.Items = append(card.Items, module.CardItem{Label: "每日备份已关闭", Note: "只在手动点「立即备份」时备份", Tone: "warn"})
	}
	return []module.Card{card}, nil
}
