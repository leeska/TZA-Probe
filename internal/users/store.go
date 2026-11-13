package users

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
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

type Store struct{ DB *sql.DB }

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
	rows, err := s.DB.QueryContext(ctx, "SELECT value FROM users ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	return s.scan(rows)
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
	u.Name, u.Note = strings.TrimSpace(u.Name), strings.TrimSpace(u.Note)
	if err := u.Validate(); err != nil {
		return User{}, err
	}
	u.ID, u.Token, u.CreatedAt, u.PeriodStart, u.TrafficUsed = newID(), NewToken(), now.Unix(), now.Unix(), 0
	return u, s.put(ctx, u)
}

// Update changes what the administrator edits; the token, usage and period
// are kept (they change through RotateToken, ResetUsage and accounting).
func (s *Store) Update(ctx context.Context, id string, in User) (User, error) {
	in.Name, in.Note = strings.TrimSpace(in.Name), strings.TrimSpace(in.Note)
	if err := in.Validate(); err != nil {
		return User{}, err
	}
	u, err := s.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	u.Name, u.Note, u.Enabled, u.TrafficLimit, u.ResetDay, u.ExpiresAt = in.Name, in.Note, in.Enabled, in.TrafficLimit, in.ResetDay, in.ExpiresAt
	return u, s.put(ctx, u)
}

func (s *Store) Delete(ctx context.Context, id string) error {
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
	u, err := s.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	u.Token = NewToken()
	return u, s.put(ctx, u)
}

// ResetUsage zeroes the usage and starts a new period now.
func (s *Store) ResetUsage(ctx context.Context, id string, now time.Time) (User, error) {
	u, err := s.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	u.TrafficUsed, u.PeriodStart = 0, now.Unix()
	return u, s.put(ctx, u)
}

// RollPeriods starts a new period for every user whose reset day has passed.
// Run periodically; returns how many were reset.
func (s *Store) RollPeriods(ctx context.Context, now time.Time) (int, error) {
	list, err := s.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range list {
		next := u.NextReset()
		if next == 0 || now.Unix() < next {
			continue
		}
		u.TrafficUsed, u.PeriodStart = 0, next
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
