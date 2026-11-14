package sites

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/storage"
)

func Migrations() []storage.Migration {
	return []storage.Migration{{Module: "sites", Version: 1, Name: "nginx sites", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(`CREATE TABLE IF NOT EXISTS sites(id TEXT PRIMARY KEY, node_id TEXT NOT NULL, value TEXT NOT NULL, created_at INTEGER NOT NULL)`)
		return err
	}}}
}

var ErrNotFound = errors.New("站点不存在")

type Store struct {
	DB *sql.DB
	mu sync.Mutex
}

func (s *Store) List(ctx context.Context) ([]Site, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT value FROM sites ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Site{}
	for rows.Next() {
		var raw string
		var v Site
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (Site, error) {
	all, err := s.List(ctx)
	if err != nil {
		return Site{}, err
	}
	for _, x := range all {
		if x.ID == id {
			return x, nil
		}
	}
	return Site{}, ErrNotFound
}

// Save creates (empty id) or replaces a site after Validate; an enabled site
// may not share a domain with another enabled site on the same node.
func (s *Store) Save(ctx context.Context, site Site) (Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := site.Validate(); err != nil {
		return Site{}, err
	}
	all, err := s.List(ctx)
	if err != nil {
		return Site{}, err
	}
	if site.ID != "" && !slices.ContainsFunc(all, func(x Site) bool { return x.ID == site.ID }) {
		return Site{}, ErrNotFound
	}
	for _, other := range all {
		if other.ID == site.ID {
			site.Created = other.Created
			continue
		}
		if other.NodeID != site.NodeID || !other.Enabled || !site.Enabled {
			continue
		}
		for _, d := range site.Domains {
			if slices.Contains(other.Domains, d) {
				return Site{}, fmt.Errorf("域名 %s 已在站点「%s」里", d, other.Name)
			}
		}
	}
	if site.ID == "" {
		var raw [6]byte
		rand.Read(raw[:])
		site.ID, site.Created = hex.EncodeToString(raw[:]), time.Now().Unix()
	}
	raw, _ := json.Marshal(site)
	_, err = s.DB.ExecContext(ctx, "INSERT INTO sites VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET node_id=excluded.node_id, value=excluded.value", site.ID, site.NodeID, string(raw), site.Created)
	return site, err
}

func (s *Store) Delete(ctx context.Context, id string) (Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	site, err := s.Get(ctx, id)
	if err != nil {
		return Site{}, err
	}
	_, err = s.DB.ExecContext(ctx, "DELETE FROM sites WHERE id=?", id)
	return site, err
}
