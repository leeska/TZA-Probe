// Package storage is the SQLite adapter. It never imports HTTP, Agent, or UI code.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	writes sync.Mutex
}

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
	if version > 1 {
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
	return s, nil
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

func (s *Store) Close() error { return s.db.Close() }
func encode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
func (s *Store) Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT config,last_seen,metrics FROM nodes")
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
		}
		v.Online = v.LastSeen > 0 && now.Unix()-v.LastSeen < int64(model.OfflineAfter.Seconds())
		start, end := model.Cycle(now, v.BillingDay)
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
		if err = s.db.QueryRowContext(ctx, "SELECT coalesce(sum(incoming),0),coalesce(sum(outgoing),0) FROM traffic WHERE node_id=? AND day>=? AND day<?", v.ID, v.CycleStart, v.CycleEnd).Scan(&in, &out); err != nil {
			return nil, err
		}
		v.TrafficUsed = model.Traffic(in, out, v.TrafficMode)
	}
	return views, nil
}
func (s *Store) SaveNode(ctx context.Context, n model.Node, create bool) (string, error) {
	if err := n.Validate(); err != nil {
		return "", err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	if create {
		n.ID = model.Secret()[:16]
		n.CreatedAt = time.Now().Unix()
		token := model.Secret()
		_, err := s.db.ExecContext(ctx, "INSERT INTO nodes(id,config,token_hash) VALUES(?,?,?)", n.ID, encode(n), model.Hash(token))
		return token, err
	}
	var old string
	if err := s.db.QueryRowContext(ctx, "SELECT config FROM nodes WHERE id=?", n.ID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	var prior model.Node
	if err := json.Unmarshal([]byte(old), &prior); err != nil {
		return "", err
	}
	n.CreatedAt = prior.CreatedAt
	_, err := s.db.ExecContext(ctx, "UPDATE nodes SET config=? WHERE id=?", encode(n), n.ID)
	return "", err
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
	err := s.db.QueryRowContext(ctx, "SELECT id FROM nodes WHERE token_hash=?", model.Hash(token)).Scan(&id)
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
	rows, err := s.db.QueryContext(ctx, `SELECT at,payload FROM samples WHERE node_id=? AND at IN (SELECT max(at) FROM samples WHERE node_id=? AND at>=? GROUP BY at/?) ORDER BY at`, id, id, now.Add(-time.Duration(hours)*time.Hour).Unix(), step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Sample{}
	for rows.Next() {
		var x model.Sample
		var data string
		if err = rows.Scan(&x.At, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &x.Metrics); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) Tasks(ctx context.Context) ([]model.Task, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT config FROM tasks ORDER BY id")
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
func (s *Store) SaveTask(ctx context.Context, t model.Task, create bool) error {
	if err := t.Validate(); err != nil {
		return err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
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
				return ErrNotFound
			}
		}
	}
	if err != nil {
		return err
	}
	// Editing invalidates in-flight leases and old route classifications.
	if _, err = tx.ExecContext(ctx, "DELETE FROM jobs WHERE task_id=?", t.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM bindings WHERE task_id=?", t.ID); err != nil {
		return err
	}
	for _, id := range t.NodeIDs {
		var exists int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE id=?", id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("%w: bound node does not exist", model.ErrInvalid)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO bindings(task_id,node_id) VALUES(?,?)", t.ID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Lease(ctx context.Context, node string, now time.Time) ([]model.Job, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT t.config FROM bindings b JOIN tasks t ON t.id=b.task_id WHERE b.node_id=? ORDER BY t.id", node)
	if err != nil {
		return nil, err
	}
	candidates := []model.Task{}
	for rows.Next() {
		var data string
		var t model.Task
		if err = rows.Scan(&data); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &t); err != nil {
			rows.Close()
			return nil, err
		}
		if t.Enabled {
			candidates = append(candidates, t.Leaves()...)
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
	rows, err := s.db.QueryContext(ctx, `SELECT task,issued,finished,result FROM jobs WHERE node_id=? AND id IN (SELECT id FROM (SELECT id,row_number() OVER(PARTITION BY task_id,json_extract(task,'$.kind'),json_extract(task,'$.family'),json_extract(task,'$.host'),json_extract(task,'$.port'),json_extract(task,'$.region'),json_extract(task,'$.carrier') ORDER BY issued DESC,id DESC) n FROM jobs WHERE node_id=?) WHERE n=1) ORDER BY task_id`, node, node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Observation{}
	for rows.Next() {
		var o model.Observation
		var task string
		var finished sql.NullInt64
		var result sql.NullString
		if err = rows.Scan(&task, &o.At, &finished, &result); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(task), &o.Task); err != nil {
			return nil, err
		}
		if result.Valid {
			if err = json.Unmarshal([]byte(result.String), &o.Result); err != nil {
				return nil, err
			}
			o.At = finished.Int64
		} else {
			o.Result.Status = "pending"
			if now.Unix()-o.At > 90 {
				o.Result.Status = "timeout"
			}
		}
		o.Stale = now.Unix()-o.At > int64(o.Task.Interval*2+90)
		out = append(out, o)
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
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", key).Scan(&value)
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
	if active != previous {
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
	rows, err := s.db.QueryContext(ctx, "SELECT id,node_id,node_name,kind,message,active,at,delivered,attempts FROM events "+where+" ORDER BY id "+order+" LIMIT 100", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var e model.Event
		if err = rows.Scan(&e.ID, &e.NodeID, &e.NodeName, &e.Kind, &e.Message, &e.Active, &e.At, &e.Delivered, &e.Attempts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) Delivery(ctx context.Context, e model.Event, ok bool, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE events SET delivered=?,attempts=attempts+1,next_attempt=? WHERE id=?", ok, now.Add(time.Duration(min(3600, 30*(1<<min(e.Attempts, 7))))*time.Second).Unix(), e.ID)
	return err
}

// IsPublic is a keyed lookup; detail requests must not reload all nodes and billing ledgers.
func (s *Store) IsPublic(ctx context.Context, id string) (bool, error) {
	var public bool
	err := s.db.QueryRowContext(ctx, "SELECT json_extract(config,'$.public') FROM nodes WHERE id=?", id).Scan(&public)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return public, err
}
