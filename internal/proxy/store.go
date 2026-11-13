package proxy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leeska/TZA-Probe/internal/storage"
)

const ddl = `CREATE TABLE IF NOT EXISTS proxy_inbounds(id TEXT PRIMARY KEY, node_id TEXT NOT NULL, value TEXT NOT NULL, created_at INTEGER NOT NULL)`

func Migrations() []storage.Migration {
	return []storage.Migration{{Module: "proxy", Version: 1, Name: "inbounds", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(ddl)
		return err
	}}}
}

var ErrNotFound = errors.New("入站不存在")

type Store struct{ DB *sql.DB }

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
	list, err := s.List(ctx)
	if err != nil {
		return Inbound{}, err
	}
	for _, other := range list {
		if other.ID != in.ID && other.NodeID == in.NodeID && other.Port == in.Port && other.Enabled && in.Enabled {
			return Inbound{}, fmt.Errorf("端口 %d 已被「%s」使用", in.Port, other.Name)
		}
	}
	if in.ID == "" {
		var raw [8]byte
		rand.Read(raw[:])
		in.ID, in.Created = hex.EncodeToString(raw[:]), time.Now().Unix()
	}
	if in.UserIDs == nil {
		in.UserIDs = []string{}
	}
	raw, _ := json.Marshal(in)
	_, err = s.DB.ExecContext(ctx, "INSERT INTO proxy_inbounds VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET node_id=excluded.node_id, value=excluded.value", in.ID, in.NodeID, string(raw), in.Created)
	return in, err
}

func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.DB.ExecContext(ctx, "DELETE FROM proxy_inbounds WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
