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

	"github.com/leeska/TZA-Probe/internal/storage"
	"github.com/leeska/TZA-Probe/internal/users"
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
