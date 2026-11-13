package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/storage"
)

const ddl = `CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, token TEXT NOT NULL UNIQUE, value TEXT NOT NULL, created_at INTEGER NOT NULL)`

// Migrations creates the users table in the main database.
func Migrations() []storage.Migration {
	return []storage.Migration{{Module: "users", Version: 1, Name: "users with quota, expiry and token", Up: func(c *storage.MigrationContext) error {
		_, err := c.Tx.Exec(ddl)
		return err
	}}}
}

var ErrNotFound = errors.New("使用者不存在")

// Store keeps users as JSON rows. Every read-modify-write holds mu, so the
// accounting loop adding usage and an administrator's edit never overwrite
// each other (the Server is the only writer).
type Store struct {
	DB *sql.DB
	mu sync.Mutex
}

func (s *Store) scan(rows *sql.Rows) ([]User, error) {
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var raw string
		var u User
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) List(ctx context.Context) ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(ctx)
}

func (s *Store) list(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT value FROM users ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	list, err := s.scan(rows)
	if err != nil {
		return nil, err
	}
	// Users made before proxy support get their credentials on first read.
	for i := range list {
		if list[i].ProxyUUID == "" || list[i].ProxyKey == "" {
			list[i].newCredentials()
			if err := s.put(ctx, list[i]); err != nil {
				return nil, err
			}
		}
	}
	return list, nil
}

func (s *Store) Get(ctx context.Context, id string) (User, error) {
	return s.one(ctx, "SELECT value FROM users WHERE id=?", id)
}

// ByToken finds the user a subscription token belongs to.
func (s *Store) ByToken(ctx context.Context, token string) (User, error) {
	if len(token) != 32 {
		return User{}, ErrNotFound
	}
	return s.one(ctx, "SELECT value FROM users WHERE token=?", token)
}

func (s *Store) one(ctx context.Context, query, arg string) (User, error) {
	rows, err := s.DB.QueryContext(ctx, query, arg)
	if err != nil {
		return User{}, err
	}
	list, err := s.scan(rows)
	if err != nil {
		return User{}, err
	}
	if len(list) == 0 {
		return User{}, ErrNotFound
	}
	return list[0], nil
}

func (s *Store) put(ctx context.Context, u User) error {
	raw, _ := json.Marshal(u)
	_, err := s.DB.ExecContext(ctx, "INSERT INTO users VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET token=excluded.token, value=excluded.value", u.ID, u.Token, string(raw), u.CreatedAt)
	return err
}

// Create stores a new user with a fresh id and token; usage starts now.
func (s *Store) Create(ctx context.Context, u User, now time.Time) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u.Name, u.Note = strings.TrimSpace(u.Name), strings.TrimSpace(u.Note)
	if err := u.Validate(); err != nil {
		return User{}, err
	}
	u.ID, u.Token, u.CreatedAt, u.PeriodStart = newID(), NewToken(), now.Unix(), now.Unix()
	u.TrafficUsed, u.TrafficUp, u.TrafficDown = 0, 0, 0
	u.newCredentials()
	return u, s.put(ctx, u)
}

// Update changes what the administrator edits; the token, usage and period
// are kept (they change through RotateToken, ResetUsage and accounting).
func (s *Store) Update(ctx context.Context, id string, in User) (User, error) {
	in.Name, in.Note = strings.TrimSpace(in.Name), strings.TrimSpace(in.Note)
	if err := in.Validate(); err != nil {
		return User{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	u.Name, u.Note, u.Enabled, u.TrafficLimit, u.ResetDay, u.ExpiresAt = in.Name, in.Note, in.Enabled, in.TrafficLimit, in.ResetDay, in.ExpiresAt
	if u.ProxyUUID == "" || u.ProxyKey == "" {
		u.newCredentials()
	}
	return u, s.put(ctx, u)
}

func (s *Store) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.DB.ExecContext(ctx, "DELETE FROM users WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateToken gives the user a new token; the old subscription link and usage
// page stop working at once.
func (s *Store) RotateToken(ctx context.Context, id string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	u.Token = NewToken()
	u.newCredentials()
	return u, s.put(ctx, u)
}

// ResetUsage zeroes the usage and starts a new period now.
func (s *Store) ResetUsage(ctx context.Context, id string, now time.Time) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	u.TrafficUsed, u.TrafficUp, u.TrafficDown, u.PeriodStart = 0, 0, 0, now.Unix()
	return u, s.put(ctx, u)
}

// RollPeriods starts a new period for every user whose reset day has passed.
// Run periodically; returns how many were reset.
func (s *Store) RollPeriods(ctx context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.list(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range list {
		next := u.NextReset()
		if next == 0 || now.Unix() < next {
			continue
		}
		u.TrafficUsed, u.TrafficUp, u.TrafficDown, u.PeriodStart = 0, 0, 0, next
		// A server that was off for months catches up to the current period.
		for {
			if after := u.NextReset(); after != 0 && now.Unix() >= after {
				u.PeriodStart = after
				continue
			}
			break
		}
		if err := s.put(ctx, u); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Usage is traffic counted for one user since the last report.
type Usage struct{ Up, Down int64 }

// AddUsage adds counted traffic to each user's current period and returns
// the ids that ran out of quota with it (their proxy access must go now).
// Ids of users deleted meanwhile are ignored.
func (s *Store) AddUsage(ctx context.Context, usage map[string]Usage, now time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exhausted := []string{}
	for id, add := range usage {
		if add.Up <= 0 && add.Down <= 0 {
			continue
		}
		u, err := s.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return exhausted, err
		}
		before := u.Status(now)
		u.TrafficUp += max(0, add.Up)
		u.TrafficDown += max(0, add.Down)
		u.TrafficUsed += max(0, add.Up) + max(0, add.Down)
		if err := s.put(ctx, u); err != nil {
			return exhausted, err
		}
		if before == StatusActive && u.Status(now) == StatusExceeded {
			exhausted = append(exhausted, id)
		}
	}
	return exhausted, nil
}
