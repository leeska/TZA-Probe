// Package storage is the SQLite adapter. It never imports HTTP, Agent, or UI code.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	_ "modernc.org/sqlite"
)

var ErrNotFound = model.ErrNotFound
var ErrConflict = model.ErrConflict

type Store struct {
	db     *sql.DB
	read   *sql.DB
	writes sync.Mutex
}

// readConns is how many connections serve read-only queries. One writer
// connection is enough for the write paths (they are serialized by writes), but
// the panel fans out: the home page asks for the node list, the last probes of
// every node and the per-target observations at once, and with a single
// connection those 18 requests queue head to tail. 线上就是这样：延迟与回程要
// 等好几秒才出来，而每次请求本身只要几十毫秒。
const readConns = 4

// openReadOnly opens the same file a second time for reads. WAL lets readers and
// the writer run side by side, and query_only makes a stray write fail loudly
// instead of quietly going through a connection that never sets foreign_keys.
// The PRAGMAs ride in the DSN because a plain Exec only reaches the connections
// that already exist, and a pool creates more later.
func openReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(readConns)
	db.SetMaxIdleConns(readConns)
	return db, nil
}

// query / queryRow are the read paths: they go to the read pool so a slow panel
// request cannot hold up the writer or a second panel request.
func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.read.QueryContext(ctx, query, args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.read.QueryRowContext(ctx, query, args...)
}

// ProbeWindow bounds how far back the panel looks for each target's newest
// result. Probes run every 30-60s (TCP) and every hour (route), and a result
// older than a few minutes already renders as stale, so a day holds far more
// history than the panel can show.
//
// 这个界不是装饰：jobs 保留七天，而「每个目标最新一次」与「每个分组最近 20 次」
// 两条查询原先都要走完这台节点（或全部可见节点）七天的任务表，每次轮询都要把
// 上万行的 task JSON 重新 json_extract 一遍——节点一多，首页的延迟与回程就要等
// 好几秒才出来（线上实测 latency-samples 3.5s、单台节点 observations 0.5s）。
// jobs_node(node_id,issued) 索引能把 issued 的范围先切出来，代价随窗口线性下降。
const ProbeWindow = 24 * time.Hour

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > 3 {
		db.Close()
		return nil, errors.New("database version is newer than this binary")
	}
	if version == 0 {
		// Refuse accidental use of a legacy database, even when it has user_version=0.
		var tables int
		if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			db.Close()
			return nil, err
		}
		if tables != 0 {
			db.Close()
			return nil, errors.New("database is not an empty TZA v2 database")
		}
		_, err = db.Exec(schema)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	if version < 2 {
		if _, err = db.Exec("BEGIN; ALTER TABLE events ADD COLUMN last_error TEXT NOT NULL DEFAULT ''; CREATE INDEX events_rule_time ON events(node_id,kind,at); PRAGMA user_version=2; COMMIT;"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version < 3 {
		// 重置日从「每月第几天」改成带秒的锚点时刻，旧的 config 就地转换。
		if err = migrateBillingAnchor(db, nowFunc()); err != nil {
			db.Close()
			return nil, err
		}
	}
	read, err := openReadOnly(path)
	if err != nil {
		db.Close()
		return nil, err
	}
	s.read = read
	return s, nil
}

// nowFunc is swapped in tests so the anchor migration can be asserted exactly.
var nowFunc = time.Now

// migrateBillingAnchor rewrites every node config that still carries billing_day
// (a day of month) into billing_at (a full instant).
func migrateBillingAnchor(db *sql.DB, now time.Time) error {
	rows, err := db.Query("SELECT id, config FROM nodes")
	if err != nil {
		return err
	}
	type change struct{ id, config string }
	var changes []change
	for rows.Next() {
		var id, config string
		if err = rows.Scan(&id, &config); err != nil {
			rows.Close()
			return err
		}
		var fields map[string]any
		if err = json.Unmarshal([]byte(config), &fields); err != nil {
			rows.Close()
			return err
		}
		if _, migrated := fields["billing_at"]; migrated {
			continue
		}
		day := 1
		if raw, ok := fields["billing_day"].(float64); ok && raw >= 1 && raw <= 31 {
			day = int(raw)
		}
		fields["billing_at"] = anchorFromDay(day, now).Format(time.RFC3339)
		delete(fields, "billing_day")
		encoded, err := json.Marshal(fields)
		if err != nil {
			rows.Close()
			return err
		}
		changes = append(changes, change{id, string(encoded)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range changes {
		if _, err = tx.Exec("UPDATE nodes SET config=? WHERE id=?", c.config, c.id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("PRAGMA user_version=3"); err != nil {
		return err
	}
	return tx.Commit()
}

// anchorFromDay picks the anchor for a legacy day of month: the newest date not
// after now whose month really has that day. time.Date rolls a too-long day into
// the next month, which is how a mismatch is detected — clamping here would move
// the configured day for every later cycle.
func anchorFromDay(day int, now time.Time) time.Time {
	now = now.UTC()
	day = min(max(day, 1), 31)
	for i := 0; i < 48; i++ {
		candidate := time.Date(now.Year(), time.Month(int(now.Month())-i), day, 0, 0, 0, 0, time.UTC)
		if candidate.Day() == day && !candidate.After(now) {
			return candidate
		}
	}
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

const schema = `BEGIN;
CREATE TABLE nodes(id TEXT PRIMARY KEY, config TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, last_seen INTEGER NOT NULL DEFAULT 0, metrics TEXT);
CREATE TABLE samples(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, at INTEGER NOT NULL, payload TEXT NOT NULL, PRIMARY KEY(node_id,at));
CREATE INDEX samples_age ON samples(at);
CREATE TABLE traffic(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, day INTEGER NOT NULL, incoming INTEGER NOT NULL, outgoing INTEGER NOT NULL, PRIMARY KEY(node_id,day));
CREATE TABLE tasks(id TEXT PRIMARY KEY, config TEXT NOT NULL);
CREATE TABLE bindings(task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, due INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(task_id,node_id));
CREATE TABLE jobs(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, task TEXT NOT NULL, issued INTEGER NOT NULL, finished INTEGER, result TEXT);
CREATE INDEX jobs_node ON jobs(node_id,issued);
CREATE INDEX jobs_age ON jobs(issued);
CREATE TABLE alert_state(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, kind TEXT NOT NULL, since INTEGER NOT NULL, active INTEGER NOT NULL, PRIMARY KEY(node_id,kind));
CREATE TABLE events(id INTEGER PRIMARY KEY AUTOINCREMENT, node_id TEXT NOT NULL, node_name TEXT NOT NULL, kind TEXT NOT NULL, message TEXT NOT NULL, active INTEGER NOT NULL, at INTEGER NOT NULL, delivered INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0);
CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
PRAGMA user_version=1;
COMMIT;`

func (s *Store) Close() error {
	err := s.db.Close()
	if s.read != nil {
		if rerr := s.read.Close(); err == nil {
			err = rerr
		}
	}
	return err
}

func encode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
func (s *Store) Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error) {
	rows, err := s.query(ctx, "SELECT config,last_seen,metrics FROM nodes")
	if err != nil {
		return nil, err
	}
	views := []model.NodeView{}
	for rows.Next() {
		var v model.NodeView
		var config string
		var metrics sql.NullString
		if err = rows.Scan(&config, &v.LastSeen, &metrics); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(config), &v.Node); err != nil {
			rows.Close()
			return nil, err
		}
		if metrics.Valid {
			if err = json.Unmarshal([]byte(metrics.String), &v.Metrics); err != nil {
				rows.Close()
				return nil, err
			}
			if v.Metrics != nil {
				v.IPFamilies = v.Metrics.IPFamilies
			}
		}
		v.Online = v.LastSeen > 0 && now.Unix()-v.LastSeen < int64(model.OfflineAfter.Seconds())
		start, end := model.Cycle(now, model.BillingAnchor(v.BillingAt, now))
		v.CycleStart = start.Unix()
		v.CycleEnd = end.Unix()
		views = append(views, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range views {
		v := &views[i]
		var in, out int64
		if err = s.queryRow(ctx, "SELECT coalesce(sum(incoming),0),coalesce(sum(outgoing),0) FROM traffic WHERE node_id=? AND day>=? AND day<?", v.ID, v.CycleStart, v.CycleEnd).Scan(&in, &out); err != nil {
			return nil, err
		}
		v.TrafficUsed = model.Traffic(in, out, v.TrafficMode)
		if v.AdjustmentCycle == v.CycleStart {
			v.TrafficUsed = max(0, v.TrafficUsed+v.TrafficAdjustment)
		}
	}
	return views, nil
}

// SaveNode creates or updates a node and returns the stored id plus, on
// creation, the one-time Agent token.
func (s *Store) SaveNode(ctx context.Context, n model.Node, create bool) (string, string, error) {
	if err := n.Validate(); err != nil {
		return "", "", err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	if create {
		n.TrafficAdjustment = 0
		n.AdjustmentCycle = 0
		n.ID = model.Secret()[:16]
		n.CreatedAt = time.Now().Unix()
		token := model.Secret()
		_, err := s.db.ExecContext(ctx, "INSERT INTO nodes(id,config,token_hash) VALUES(?,?,?)", n.ID, encode(n), model.Hash(token))
		return n.ID, token, err
	}
	var old string
	if err := s.queryRow(ctx, "SELECT config FROM nodes WHERE id=?", n.ID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	} else if err != nil {
		return "", "", err
	}
	var prior model.Node
	if err := json.Unmarshal([]byte(old), &prior); err != nil {
		return "", "", err
	}
	n.CreatedAt = prior.CreatedAt
	n.TrafficAdjustment = prior.TrafficAdjustment
	n.AdjustmentCycle = prior.AdjustmentCycle
	if n.BillingAt != prior.BillingAt || n.TrafficMode != prior.TrafficMode {
		n.TrafficAdjustment = 0
		n.AdjustmentCycle = 0
	}
	_, err := s.db.ExecContext(ctx, "UPDATE nodes SET config=? WHERE id=?", encode(n), n.ID)
	return n.ID, "", err
}
func (s *Store) DeleteNode(ctx context.Context, id string) error { return s.delete(ctx, "nodes", id) }
func (s *Store) DeleteTask(ctx context.Context, id string) error { return s.delete(ctx, "tasks", id) }
func (s *Store) delete(ctx context.Context, table, id string) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	r, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) Rotate(ctx context.Context, id string) (string, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	token := model.Secret()
	r, err := s.db.ExecContext(ctx, "UPDATE nodes SET token_hash=? WHERE id=?", model.Hash(token), id)
	if err != nil {
		return "", err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return "", ErrNotFound
	}
	return token, nil
}
func (s *Store) Authenticate(ctx context.Context, token string) (string, error) {
	if len(token) != 64 {
		return "", ErrNotFound
	}
	var id string
	err := s.queryRow(ctx, "SELECT id FROM nodes WHERE token_hash=?", model.Hash(token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
func (s *Store) Report(ctx context.Context, id string, m model.Metrics, now time.Time) error {
	if err := m.Validate(); err != nil {
		return err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous sql.NullString
	var seen int64
	if err = tx.QueryRowContext(ctx, "SELECT metrics,last_seen FROM nodes WHERE id=?", id).Scan(&previous, &seen); err != nil {
		return err
	}
	if now.Unix()-seen < 3 {
		return ErrConflict
	}
	var in, out int64
	if previous.Valid {
		var p model.Metrics
		if err = json.Unmarshal([]byte(previous.String), &p); err != nil {
			return err
		}
		if m.CounterID == p.CounterID && m.Uptime >= p.Uptime {
			if m.NetIn >= p.NetIn {
				in = int64(m.NetIn - p.NetIn)
			}
			if m.NetOut >= p.NetOut {
				out = int64(m.NetOut - p.NetOut)
			}
		}
	}
	payload := encode(m)
	if _, err = tx.ExecContext(ctx, "UPDATE nodes SET metrics=?,last_seen=? WHERE id=?", payload, now.Unix(), id); err != nil {
		return err
	}
	// One persisted sample per minute; current state is refreshed on each report.
	at := now.Unix() / 60 * 60
	if _, err = tx.ExecContext(ctx, "INSERT INTO samples VALUES(?,?,?) ON CONFLICT(node_id,at) DO UPDATE SET payload=excluded.payload", id, at, payload); err != nil {
		return err
	}
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).Unix()
	if _, err = tx.ExecContext(ctx, "INSERT INTO traffic VALUES(?,?,?,?) ON CONFLICT(node_id,day) DO UPDATE SET incoming=incoming+excluded.incoming,outgoing=outgoing+excluded.outgoing", id, day, in, out); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) History(ctx context.Context, id string, hours int, now time.Time) ([]model.Sample, error) {
	// Aggregate into <= 240 buckets. Each bucket returns its last observed sample.
	hours = min(max(hours, 1), 720)
	step := max(60, hours*3600/240)
	rows, err := s.query(ctx, `SELECT at,payload FROM samples WHERE node_id=? AND at IN (SELECT max(at) FROM samples WHERE node_id=? AND at>=? GROUP BY at/?) ORDER BY at`, id, id, now.Add(-time.Duration(hours)*time.Hour).Unix(), step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Sample{}
	skipped := 0
	for rows.Next() {
		var x model.Sample
		var data string
		if err = rows.Scan(&x.At, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &x.Metrics); err != nil {
			// 坏掉的样本行（旧版本格式或半写入）只跳过自己：一台机器的历史不该
			// 因为一行读不出来就整体打不开。
			skipped++
			continue
		}
		out = append(out, x)
	}
	if skipped > 0 {
		slog.Warn("skipped unreadable samples", "node", id, "samples", skipped)
	}
	return out, rows.Err()
}
func (s *Store) Tasks(ctx context.Context) ([]model.Task, error) {
	rows, err := s.query(ctx, "SELECT config FROM tasks ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Task{}
	for rows.Next() {
		var data string
		var t model.Task
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveTask creates or updates a probe task and returns the stored id.
func (s *Store) SaveTask(ctx context.Context, t model.Task, create bool) (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if create {
		t.ID = model.Secret()[:16]
		_, err = tx.ExecContext(ctx, "INSERT INTO tasks VALUES(?,?)", t.ID, encode(t))
	} else {
		var r sql.Result
		r, err = tx.ExecContext(ctx, "UPDATE tasks SET config=? WHERE id=?", encode(t), t.ID)
		if err == nil {
			n, _ := r.RowsAffected()
			if n == 0 {
				return "", ErrNotFound
			}
		}
	}
	if err != nil {
		return "", err
	}
	// Editing invalidates in-flight leases and old route classifications.
	if _, err = tx.ExecContext(ctx, "DELETE FROM jobs WHERE task_id=?", t.ID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM bindings WHERE task_id=?", t.ID); err != nil {
		return "", err
	}
	for _, id := range t.NodeIDs {
		var exists int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE id=?", id).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return "", fmt.Errorf("%w: bound node does not exist", model.ErrInvalid)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO bindings(task_id,node_id) VALUES(?,?)", t.ID, id); err != nil {
			return "", err
		}
	}
	return t.ID, tx.Commit()
}
func (s *Store) Lease(ctx context.Context, node string, now time.Time) ([]model.Job, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Address-family capability comes from the Agent's own report. Unknown
	// capability (older Agent or first report not arrived yet) keeps the old
	// behaviour so upgrades do not silently disable every probe.
	var metricsJSON sql.NullString
	var capabilities model.Metrics
	if err = tx.QueryRowContext(ctx, "SELECT metrics FROM nodes WHERE id=?", node).Scan(&metricsJSON); err != nil {
		return nil, err
	}
	if metricsJSON.Valid {
		if err = json.Unmarshal([]byte(metricsJSON.String), &capabilities); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT t.config,b.due FROM bindings b JOIN tasks t ON t.id=b.task_id WHERE b.node_id=? ORDER BY t.id", node)
	if err != nil {
		return nil, err
	}
	candidates := []model.Task{}
	requested := map[string]int64{}
	for rows.Next() {
		var data string
		var t model.Task
		var requestAt int64
		if err = rows.Scan(&data, &requestAt); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &t); err != nil {
			rows.Close()
			return nil, err
		}
		requested[t.ID] = requestAt
		if !t.Enabled {
			continue
		}
		for _, leaf := range t.Leaves() {
			if capabilities.AllowsFamily(leaf.Family) {
				candidates = append(candidates, leaf)
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// At most one route and one TCP lease at once per node, including other agents using its token.
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE node_id=? AND finished IS NULL AND issued>?", node, now.Add(-90*time.Second).Unix()).Scan(&active); err != nil {
		return nil, err
	}
	if active > 0 {
		return []model.Job{}, nil
	}
	// Read scheduling history once. Retention exceeds the maximum task interval.
	last := map[string]int64{}
	history, err := tx.QueryContext(ctx, "SELECT task,max(issued) FROM jobs WHERE node_id=? GROUP BY task", node)
	if err != nil {
		return nil, err
	}
	for history.Next() {
		var data string
		var at int64
		var leaf model.Task
		if err = history.Scan(&data, &at); err != nil {
			history.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &leaf); err != nil {
			history.Close()
			return nil, err
		}
		if at > last[leaf.ProbeKey()] {
			last[leaf.ProbeKey()] = at
		}
	}
	err = history.Err()
	history.Close()
	if err != nil {
		return nil, err
	}
	// Oldest due first prevents starvation when many targets share a node.
	due := func(t model.Task) int64 {
		if requested[t.ID] > last[t.ProbeKey()] {
			return requested[t.ID]
		}
		if at, ok := last[t.ProbeKey()]; ok {
			return at + int64(t.Interval)
		}
		return 0
	}
	sort.SliceStable(candidates, func(i, j int) bool { return due(candidates[i]) < due(candidates[j]) })
	out := []model.Job{}
	kinds := map[string]bool{}
	for _, t := range candidates {
		if kinds[t.Kind] || due(t) > now.Unix() {
			continue
		}
		kinds[t.Kind] = true
		j := model.Job{ID: model.Secret()[:24], Task: t}
		if _, err = tx.ExecContext(ctx, "INSERT INTO jobs(id,task_id,node_id,task,issued) VALUES(?,?,?,?,?)", j.ID, t.ID, node, encode(t), now.Unix()); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, tx.Commit()
}
func (s *Store) Finish(ctx context.Context, node string, r model.Result, now time.Time) error {
	if err := r.Validate(); err != nil {
		return err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	result, err := s.db.ExecContext(ctx, "UPDATE jobs SET finished=?,result=? WHERE id=? AND node_id=? AND finished IS NULL AND issued>=?", now.Unix(), encode(r), r.JobID, node, now.Add(-90*time.Second).Unix())
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrConflict
	}
	return nil
}
func (s *Store) Observations(ctx context.Context, node string, now time.Time) ([]model.Observation, error) {
	// json_valid 挡住坏掉的任务 JSON：json1 遇到非法 JSON 会让整条语句报 "malformed JSON"，
	// 一条旧数据不该让整台节点的观测列表打不开。
	// issued>=ProbeWindow 之外的行不参与分组：最新一次观测在探测间隔（30-60s / 1h）之后
	// 早已被判为过期，读它们只会让每台节点的请求都去扫一遍七天的任务表。
	rows, err := s.query(ctx, `SELECT task,issued,finished,result FROM jobs WHERE node_id=? AND id IN (SELECT id FROM (SELECT id,row_number() OVER(PARTITION BY task_id,json_extract(task,'$.kind'),json_extract(task,'$.family'),json_extract(task,'$.host'),json_extract(task,'$.port'),json_extract(task,'$.region'),json_extract(task,'$.carrier') ORDER BY issued DESC,id DESC) n FROM jobs WHERE node_id=? AND json_valid(task) AND issued>=?) WHERE n=1) ORDER BY task_id`, node, node, now.Add(-ProbeWindow).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Observation{}
	skipped := 0
	for rows.Next() {
		var o model.Observation
		var task string
		var finished sql.NullInt64
		var result sql.NullString
		if err = rows.Scan(&task, &o.At, &finished, &result); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(task), &o.Task); err != nil {
			// 任务 JSON 读不出来就没法把这条结果摆到面板上：跳过它，别让整个节点 500。
			skipped++
			continue
		}
		if result.Valid {
			if err = json.Unmarshal([]byte(result.String), &o.Result); err != nil {
				// 任务还在、结果坏了：这一条按“未知”显示，比整页打不开有用。
				skipped++
				o.Result = model.Result{Status: "unknown"}
			} else {
				o.At = finished.Int64
				// 线路标签是跳表的解释，不是实测数据本身：读的时候按当前规则重算，
				// 判定规则修正后历史记录跟着更新，不必等下一次探测。
				if o.Task.Kind == "route" {
					o.Result.Route = model.RouteLabel(o.Result.Hops)
					if o.Result.Status != "ok" {
						o.Result.Route = "未知"
					}
				}
			}
		} else {
			o.Result.Status = "pending"
			if now.Unix()-o.At > 90 {
				o.Result.Status = "timeout"
			}
		}
		o.Stale = now.Unix()-o.At > int64(o.Task.Interval*2+90)
		out = append(out, o)
	}
	if skipped > 0 {
		slog.Warn("skipped unreadable observations", "node", node, "rows", skipped)
	}
	return out, rows.Err()
}
func (s *Store) Clean(ctx context.Context, now time.Time) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []struct {
		query string
		age   time.Duration
	}{{"DELETE FROM samples WHERE at<?", 30 * 24 * time.Hour}, {"DELETE FROM traffic WHERE day<?", 400 * 24 * time.Hour}, {"DELETE FROM jobs WHERE issued<?", 7 * 24 * time.Hour}, {"DELETE FROM events WHERE at<?", 90 * 24 * time.Hour}} {
		if _, err = tx.ExecContext(ctx, q.query, now.Add(-q.age).Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.queryRow(ctx, "SELECT value FROM settings WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO settings VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

// Transition persists debounce and edge state atomically with its durable notification event.
func (s *Store) Transition(ctx context.Context, n model.Node, kind, message string, condition bool, hold int, now time.Time) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var since int64
	var active bool
	err = tx.QueryRowContext(ctx, "SELECT since,active FROM alert_state WHERE node_id=? AND kind=?", n.ID, kind).Scan(&since, &active)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	previous := active
	if condition {
		if since == 0 {
			since = now.Unix()
		}
		if now.Unix()-since >= int64(hold) {
			active = true
		}
	} else {
		since = 0
		active = false
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO alert_state VALUES(?,?,?,?) ON CONFLICT(node_id,kind) DO UPDATE SET since=excluded.since,active=excluded.active", n.ID, kind, since, active); err != nil {
		return err
	}
	repeat := false
	if active && previous && n.Alerts.RepeatSeconds > 0 {
		var last int64
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(at),0) FROM events WHERE node_id=? AND kind=?", n.ID, kind).Scan(&last); err != nil {
			return err
		}
		repeat = now.Unix()-last >= int64(n.Alerts.RepeatSeconds)
	}
	if active != previous || repeat {
		if !active {
			message = "条件已解除或提醒已关闭"
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO events(node_id,node_name,kind,message,active,at) VALUES(?,?,?,?,?,?)", n.ID, n.Name, kind, message, active, now.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Events(ctx context.Context, pending bool, now time.Time) ([]model.Event, error) {
	where := ""
	order := "DESC"
	if pending {
		where = "WHERE delivered=0 AND next_attempt<=? AND attempts<10"
		order = "ASC"
	}
	args := []any{}
	if pending {
		args = append(args, now.Unix())
	}
	rows, err := s.query(ctx, "SELECT id,node_id,node_name,kind,message,active,at,delivered,attempts,last_error FROM events "+where+" ORDER BY id "+order+" LIMIT 100", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var e model.Event
		if err = rows.Scan(&e.ID, &e.NodeID, &e.NodeName, &e.Kind, &e.Message, &e.Active, &e.At, &e.Delivered, &e.Attempts, &e.LastError); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) Delivery(ctx context.Context, e model.Event, ok bool, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE events SET delivered=?,last_error=?,attempts=attempts+1,next_attempt=? WHERE id=?", ok, e.LastError, now.Add(time.Duration(min(3600, 30*(1<<min(e.Attempts, 7))))*time.Second).Unix(), e.ID)
	return err
}

// IsPublic is a keyed lookup; detail requests must not reload all nodes and billing ledgers.
func (s *Store) IsPublic(ctx context.Context, id string) (bool, error) {
	var public bool
	err := s.queryRow(ctx, "SELECT json_extract(config,'$.public') FROM nodes WHERE id=?", id).Scan(&public)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return public, err
}
