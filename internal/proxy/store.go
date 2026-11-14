package proxy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/storage"
	"github.com/leeska/TZA-ControlCenter/internal/users"
)

const ddl = `CREATE TABLE IF NOT EXISTS proxy_inbounds(id TEXT PRIMARY KEY, node_id TEXT NOT NULL, value TEXT NOT NULL, created_at INTEGER NOT NULL)`
const forwardsDDL = `CREATE TABLE IF NOT EXISTS proxy_forwards(id TEXT PRIMARY KEY, value TEXT NOT NULL, created_at INTEGER NOT NULL)`

func Migrations() []storage.Migration {
	return []storage.Migration{{Module: "proxy", Version: 1, Name: "inbounds", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(ddl)
		return err
	}}, {Module: "proxy", Version: 2, Name: "forwards", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(forwardsDDL)
		return err
	}}, {Module: "proxy", Version: 3, Name: "rule templates, external subscriptions", Up: func(c *storage.MigrationContext) error {
		for _, ddl := range []string{
			`CREATE TABLE IF NOT EXISTS proxy_templates(id TEXT PRIMARY KEY, value TEXT NOT NULL, created_at INTEGER NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS proxy_sources(id TEXT PRIMARY KEY, value TEXT NOT NULL, created_at INTEGER NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS proxy_state(key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		} {
			if _, err := c.Tx.Exec(ddl); err != nil {
				return err
			}
		}
		return nil
	}}}
}

var (
	ErrNotFound        = errors.New("入站不存在")
	ErrForwardNotFound = errors.New("转发规则不存在")
)

// Store keeps inbounds and forwards. Writes hold mu: both share the nodes'
// ports, and traffic accounting rewrites forwards while an administrator may
// be editing one.
type Store struct {
	DB *sql.DB
	mu sync.Mutex
}

func newID() string {
	var raw [8]byte
	rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

func (s *Store) List(ctx context.Context) ([]Inbound, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT value FROM proxy_inbounds ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Inbound{}
	for rows.Next() {
		var raw string
		var in Inbound
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) get(ctx context.Context, id string) (Inbound, error) {
	list, err := s.List(ctx)
	if err != nil {
		return Inbound{}, err
	}
	for _, in := range list {
		if in.ID == id {
			return in, nil
		}
	}
	return Inbound{}, ErrNotFound
}

// Save creates (empty id) or replaces an inbound after Prepare. An enabled
// inbound may not share a port with another enabled one on the same node.
func (s *Store) Save(ctx context.Context, in Inbound) (Inbound, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var previous *Inbound
	if in.ID != "" {
		old, err := s.get(ctx, in.ID)
		if err != nil {
			return Inbound{}, err
		}
		previous = &old
		in.Created = old.Created
	}
	if err := in.Prepare(previous); err != nil {
		return Inbound{}, err
	}
	if err := s.checkPorts(ctx, ports([]Inbound{in}, nil)); err != nil {
		return Inbound{}, err
	}
	if in.ID == "" {
		in.ID, in.Created = newID(), time.Now().Unix()
	}
	if in.UserIDs == nil {
		in.UserIDs = []string{}
	}
	raw, _ := json.Marshal(in)
	_, err := s.DB.ExecContext(ctx, "INSERT INTO proxy_inbounds VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET node_id=excluded.node_id, value=excluded.value", in.ID, in.NodeID, string(raw), in.Created)
	return in, err
}

func (s *Store) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.DB.ExecContext(ctx, "DELETE FROM proxy_inbounds WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// checkPorts refuses want when an enabled inbound or forward already holds
// one of its ports. A new item's owner id is empty and so never itself.
func (s *Store) checkPorts(ctx context.Context, want []portUse) error {
	inbounds, err := s.List(ctx)
	if err != nil {
		return err
	}
	forwards, err := s.Forwards(ctx)
	if err != nil {
		return err
	}
	return conflict(ports(inbounds, forwards), want)
}

func (s *Store) Forwards(ctx context.Context) ([]Forward, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT value FROM proxy_forwards ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Forward{}
	for rows.Next() {
		var raw string
		var f Forward
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) forward(ctx context.Context, id string) (Forward, error) {
	list, err := s.Forwards(ctx)
	if err != nil {
		return Forward{}, err
	}
	for _, f := range list {
		if f.ID == id {
			return f, nil
		}
	}
	return Forward{}, ErrForwardNotFound
}

func (s *Store) putForward(ctx context.Context, f Forward) error {
	raw, _ := json.Marshal(f)
	_, err := s.DB.ExecContext(ctx, "INSERT INTO proxy_forwards VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", f.ID, string(raw), f.Created)
	return err
}

// SaveForward creates (empty id) or replaces a forward after Prepare; its
// ports may not be taken on the entry or exit node.
func (s *Store) SaveForward(ctx context.Context, f Forward) (Forward, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var previous *Forward
	if f.ID != "" {
		old, err := s.forward(ctx, f.ID)
		if err != nil {
			return Forward{}, err
		}
		previous = &old
	}
	if err := f.Prepare(previous); err != nil {
		return Forward{}, err
	}
	if err := s.checkPorts(ctx, ports(nil, []Forward{f})); err != nil {
		return Forward{}, err
	}
	if f.ID == "" {
		f.ID, f.Created = newID(), time.Now().Unix()
	}
	return f, s.putForward(ctx, f)
}

func (s *Store) DeleteForward(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.DB.ExecContext(ctx, "DELETE FROM proxy_forwards WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrForwardNotFound
	}
	return nil
}

// AddForwardTraffic adds counted bytes to forwards' running totals; ids of
// forwards deleted meanwhile are ignored.
func (s *Store) AddForwardTraffic(ctx context.Context, usage map[string]users.Usage) error {
	if len(usage) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, add := range usage {
		f, err := s.forward(ctx, id)
		if errors.Is(err, ErrForwardNotFound) {
			continue
		} else if err != nil {
			return err
		}
		f.TrafficUp += max(0, add.Up)
		f.TrafficDown += max(0, add.Down)
		if err := s.putForward(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// rows reads the JSON values of a table, oldest first.
func rows[T any](ctx context.Context, db *sql.DB, table string) ([]T, error) {
	r, err := db.QueryContext(ctx, "SELECT value FROM "+table+" ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []T{}
	for r.Next() {
		var raw string
		var v T
		if err := r.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, r.Err()
}

func (s *Store) putRow(ctx context.Context, table, id string, created int64, v any) error {
	raw, _ := json.Marshal(v)
	_, err := s.DB.ExecContext(ctx, "INSERT INTO "+table+" VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", id, string(raw), created)
	return err
}

func (s *Store) deleteRow(ctx context.Context, table, id string, missing error) error {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return missing
	}
	return nil
}

var (
	ErrTemplateNotFound = errors.New("规则模板不存在")
	ErrSourceNotFound   = errors.New("外部订阅不存在")
)

// Templates lists the built-in templates, then the custom ones.
func (s *Store) Templates(ctx context.Context) ([]Template, error) {
	custom, err := rows[Template](ctx, s.DB, "proxy_templates")
	if err != nil {
		return nil, err
	}
	return append(append([]Template{}, BuiltInTemplates...), custom...), nil
}

// Template finds a template by id (built-in or custom).
func (s *Store) Template(ctx context.Context, id string) (Template, error) {
	all, err := s.Templates(ctx)
	if err != nil {
		return Template{}, err
	}
	for _, t := range all {
		if t.ID == id {
			return t, nil
		}
	}
	return Template{}, ErrTemplateNotFound
}

func (s *Store) SaveTemplate(ctx context.Context, t Template) (Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := t.Validate(); err != nil {
		return Template{}, err
	}
	t.BuiltIn = false
	if t.ID == "" {
		t.ID, t.Created = newID(), time.Now().Unix()
	} else {
		old, err := s.Template(ctx, t.ID)
		if err != nil {
			return Template{}, err
		}
		if old.BuiltIn {
			return Template{}, errors.New("内置模板不能修改，可以照着它新建一个")
		}
		t.Created = old.Created
	}
	return t, s.putRow(ctx, "proxy_templates", t.ID, t.Created, t)
}

func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if def, _ := s.State(ctx, "default_template"); def == id {
		s.SetState(ctx, "default_template", "")
	}
	return s.deleteRow(ctx, "proxy_templates", id, ErrTemplateNotFound)
}

func (s *Store) Sources(ctx context.Context) ([]Source, error) {
	return rows[Source](ctx, s.DB, "proxy_sources")
}

func (s *Store) Source(ctx context.Context, id string) (Source, error) {
	all, err := s.Sources(ctx)
	if err != nil {
		return Source{}, err
	}
	for _, src := range all {
		if src.ID == id {
			return src, nil
		}
	}
	return Source{}, ErrSourceNotFound
}

// SaveSource creates or edits what the administrator sets; the fetched
// nodes are kept unless the URL changed.
func (s *Store) SaveSource(ctx context.Context, src Source) (Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := src.Validate(); err != nil {
		return Source{}, err
	}
	if src.ID == "" {
		src.ID, src.Created = newID(), time.Now().Unix()
		src.Nodes, src.Skipped, src.FetchedAt, src.Error = nil, 0, 0, ""
	} else {
		old, err := s.Source(ctx, src.ID)
		if err != nil {
			return Source{}, err
		}
		src.Created = old.Created
		if old.URL == src.URL {
			src.Nodes, src.Skipped, src.FetchedAt, src.Error = old.Nodes, old.Skipped, old.FetchedAt, old.Error
		}
	}
	return src, s.putRow(ctx, "proxy_sources", src.ID, src.Created, src)
}

// UpdateSource applies fn to the stored source under the lock.
func (s *Store) UpdateSource(ctx context.Context, id string, fn func(*Source)) (Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := s.Source(ctx, id)
	if err != nil {
		return Source{}, err
	}
	fn(&src)
	return src, s.putRow(ctx, "proxy_sources", src.ID, src.Created, src)
}

func (s *Store) DeleteSource(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteRow(ctx, "proxy_sources", id, ErrSourceNotFound)
}

// State reads a proxy setting ("" when unset).
func (s *Store) State(ctx context.Context, key string) (string, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, "SELECT value FROM proxy_state WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetState(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO proxy_state VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}
