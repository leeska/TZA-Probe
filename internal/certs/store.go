package certs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leeska/TZA-Probe/internal/storage"
)

func Migrations() []storage.Migration {
	return []storage.Migration{{Module: "certs", Version: 1, Name: "certificates, DNS accounts and ACME state", Up: func(c *storage.MigrationContext) error {
		for _, ddl := range []string{
			`CREATE TABLE IF NOT EXISTS certs(id TEXT PRIMARY KEY, value TEXT NOT NULL, created_at INTEGER NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS certs_dns(id TEXT PRIMARY KEY, value TEXT NOT NULL, created_at INTEGER NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS certs_state(key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		} {
			if _, err := c.Tx.Exec(ddl); err != nil {
				return err
			}
		}
		return nil
	}}}
}

var (
	ErrNotFound    = errors.New("证书不存在")
	ErrDNSNotFound = errors.New("DNS 账号不存在")
)

// DNSAccount is a Cloudflare API token the Server uses for DNS-01.
type DNSAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"` // "cloudflare"
	Token    string `json:"token,omitempty"`
	Created  int64  `json:"created_at"`
}

// Target is one place a certificate is deployed to.
type Target struct {
	NodeID string `json:"node_id"`
	Dir    string `json:"dir"`
	Reload string `json:"reload"`
	// Deployed is the serial last written there; Error / Output are the last
	// attempt's.
	Deployed   string `json:"deployed,omitempty"`
	DeployedAt int64  `json:"deployed_at,omitempty"`
	Error      string `json:"error,omitempty"`
	Output     string `json:"output,omitempty"`
}

// Cert statuses.
const (
	StatusPending = "pending" // waiting for its first issuance
	StatusValid   = "valid"
	StatusError   = "error" // the last issuance failed; a valid older cert may remain
)

// Cert is a certificate the Server keeps issued and deployed.
type Cert struct {
	ID      string   `json:"id"`
	Domains []string `json:"domains"`
	DNSID   string   `json:"dns_id"`
	Targets []Target `json:"targets"`
	// The issued certificate. KeyPEM never leaves through the API.
	CertPEM   string `json:"cert_pem,omitempty"`
	KeyPEM    string `json:"key_pem,omitempty"`
	Serial    string `json:"serial,omitempty"`
	Issuer    string `json:"issuer,omitempty"`
	NotBefore int64  `json:"not_before,omitempty"`
	NotAfter  int64  `json:"not_after,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	// LastAttempt / NextAttempt pace issuance (failures back off).
	LastAttempt int64 `json:"last_attempt,omitempty"`
	NextAttempt int64 `json:"next_attempt,omitempty"`
	Failures    int   `json:"failures,omitempty"`
	// Force asks for a new certificate on the next round (续期).
	Force   bool  `json:"force,omitempty"`
	Created int64 `json:"created_at"`
}

// RenewBefore caps how long before expiry a certificate is renewed.
const RenewBefore = 30 * 24 * time.Hour

// RenewAt is when c is renewed: a third of its lifetime before it expires
// (Let's Encrypt's advice, which keeps working as lifetimes shrink), but no
// earlier than RenewBefore.
func (c Cert) RenewAt() time.Time {
	life := time.Duration(c.NotAfter-c.NotBefore) * time.Second
	return time.Unix(c.NotAfter, 0).Add(-min(RenewBefore, life/3))
}

// Due reports whether c needs a new certificate at now.
func (c Cert) Due(now time.Time) bool {
	if c.NextAttempt > now.Unix() {
		return false
	}
	return c.Force || c.CertPEM == "" || !now.Before(c.RenewAt())
}

func newID() string {
	var raw [8]byte
	rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

// NormalizeDomains lower-cases, trims and checks the names (a leading "*."
// for a wildcard), dropping duplicates; the first is the certificate's name.
func NormalizeDomains(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, d := range in {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d == "" || seen[d] {
			continue
		}
		host := strings.TrimPrefix(d, "*.")
		if strings.Contains(host, "*") || !strings.Contains(host, ".") || !validName(host) {
			return nil, fmt.Errorf("「%s」不是有效的域名", d)
		}
		seen[d] = true
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, errors.New("请至少填写一个域名")
	}
	if len(out) > 20 {
		return nil, errors.New("一张证书最多 20 个域名")
	}
	return out, nil
}

func validName(h string) bool {
	if len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// DefaultDir is where a target writes when no directory is given.
func DefaultDir(domain string) string {
	return "/etc/tza-certs/" + strings.ReplaceAll(domain, "*", "_")
}

// Store keeps certificates, DNS accounts and the ACME state. mu orders the
// read-modify-writes of the background loop and the API.
type Store struct {
	DB *sql.DB
	mu sync.Mutex
}

func list[T any](ctx context.Context, db *sql.DB, table string) ([]T, error) {
	rows, err := db.QueryContext(ctx, "SELECT value FROM "+table+" ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var raw string
		var v T
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

func (s *Store) Certs(ctx context.Context) ([]Cert, error) { return list[Cert](ctx, s.DB, "certs") }

func (s *Store) DNSAccounts(ctx context.Context) ([]DNSAccount, error) {
	return list[DNSAccount](ctx, s.DB, "certs_dns")
}

func (s *Store) cert(ctx context.Context, id string) (Cert, error) {
	all, err := s.Certs(ctx)
	if err != nil {
		return Cert{}, err
	}
	for _, c := range all {
		if c.ID == id {
			return c, nil
		}
	}
	return Cert{}, ErrNotFound
}

func (s *Store) Cert(ctx context.Context, id string) (Cert, error) { return s.cert(ctx, id) }

func (s *Store) DNSAccount(ctx context.Context, id string) (DNSAccount, error) {
	all, err := s.DNSAccounts(ctx)
	if err != nil {
		return DNSAccount{}, err
	}
	for _, a := range all {
		if a.ID == id {
			return a, nil
		}
	}
	return DNSAccount{}, ErrDNSNotFound
}

func (s *Store) put(ctx context.Context, table, id string, created int64, v any) error {
	raw, _ := json.Marshal(v)
	_, err := s.DB.ExecContext(ctx, "INSERT INTO "+table+" VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", id, string(raw), created)
	return err
}

func (s *Store) remove(ctx context.Context, table, id string, missing error) error {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return missing
	}
	return nil
}

// SaveDNS creates or renames an account; an empty token on edit keeps the
// stored one.
func (s *Store) SaveDNS(ctx context.Context, a DNSAccount) (DNSAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.Name, a.Token = strings.TrimSpace(a.Name), strings.TrimSpace(a.Token)
	if a.Name == "" || utf8.RuneCountInString(a.Name) > 40 {
		return DNSAccount{}, errors.New("名称需要 1–40 个字")
	}
	a.Provider = "cloudflare"
	if a.ID != "" {
		old, err := s.DNSAccount(ctx, a.ID)
		if err != nil {
			return DNSAccount{}, err
		}
		a.Created = old.Created
		if a.Token == "" {
			a.Token = old.Token
		}
	} else {
		a.ID, a.Created = newID(), time.Now().Unix()
	}
	if a.Token == "" || len(a.Token) > 200 || strings.ContainsAny(a.Token, " \n\t") {
		return DNSAccount{}, errors.New("请填写 Cloudflare API 令牌")
	}
	return a, s.put(ctx, "certs_dns", a.ID, a.Created, a)
}

// DeleteDNS refuses while a certificate uses the account.
func (s *Store) DeleteDNS(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.Certs(ctx)
	if err != nil {
		return err
	}
	for _, c := range all {
		if c.DNSID == id {
			return fmt.Errorf("证书「%s」还在用这个账号", c.Domains[0])
		}
	}
	return s.remove(ctx, "certs_dns", id, ErrDNSNotFound)
}

// CertInput is what the administrator edits.
type CertInput struct {
	Domains []string `json:"domains"`
	DNSID   string   `json:"dns_id"`
	Targets []Target `json:"targets"`
}

// SaveCert creates a certificate (to be issued) or edits one. Changing the
// domains asks for a new certificate; deploy state of unchanged targets is
// kept.
func (s *Store) SaveCert(ctx context.Context, id string, in CertInput) (Cert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	domains, err := NormalizeDomains(in.Domains)
	if err != nil {
		return Cert{}, err
	}
	if _, err := s.DNSAccount(ctx, in.DNSID); err != nil {
		return Cert{}, errors.New("请选择 DNS 账号")
	}
	targets := []Target{}
	seen := map[string]bool{}
	for _, t := range in.Targets {
		t.Dir, t.Reload = strings.TrimSpace(t.Dir), strings.TrimSpace(t.Reload)
		if t.Dir == "" {
			t.Dir = DefaultDir(domains[0])
		}
		if t.NodeID == "" || seen[t.NodeID+"\x00"+t.Dir] {
			return Cert{}, errors.New("下发目标的节点为空或重复")
		}
		if !strings.HasPrefix(t.Dir, "/") || strings.Contains(t.Dir, "..") || len(t.Dir) > 255 {
			return Cert{}, errors.New("目录须是绝对路径")
		}
		if len(t.Reload) > 1024 || strings.ContainsAny(t.Reload, "\n\r") {
			return Cert{}, errors.New("重载命令最长 1024 字符、只能一行")
		}
		seen[t.NodeID+"\x00"+t.Dir] = true
		targets = append(targets, Target{NodeID: t.NodeID, Dir: t.Dir, Reload: t.Reload})
	}
	c := Cert{ID: id, Status: StatusPending, Created: time.Now().Unix()}
	if id != "" {
		if c, err = s.cert(ctx, id); err != nil {
			return Cert{}, err
		}
		if strings.Join(c.Domains, ",") != strings.Join(domains, ",") {
			c.Force, c.NextAttempt, c.Failures = true, 0, 0
		}
		// Keep the deploy state of a target that did not change.
		for i, t := range targets {
			for _, old := range c.Targets {
				if old.NodeID == t.NodeID && old.Dir == t.Dir && old.Reload == t.Reload {
					targets[i] = old
				}
			}
		}
	} else {
		c.ID = newID()
	}
	c.Domains, c.DNSID, c.Targets = domains, in.DNSID, targets
	return c, s.put(ctx, "certs", c.ID, c.Created, c)
}

func (s *Store) DeleteCert(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remove(ctx, "certs", id, ErrNotFound)
}

// Update applies fn to the stored certificate under the lock.
func (s *Store) Update(ctx context.Context, id string, fn func(*Cert)) (Cert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.cert(ctx, id)
	if err != nil {
		return Cert{}, err
	}
	fn(&c)
	return c, s.put(ctx, "certs", c.ID, c.Created, c)
}

// State reads a value of certs_state ("" when unset).
func (s *Store) State(ctx context.Context, key string) (string, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, "SELECT value FROM certs_state WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetState(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO certs_state VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}
