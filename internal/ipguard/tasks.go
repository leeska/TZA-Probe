package ipguard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Region drift is configured in two halves, like the other network checks:
//
//   - Task: what to check and on which nodes (name, on/off, services, automatic
//     correction, node list). One task covers many nodes; a node belongs to at
//     most one task.
//   - NodeSettings: what differs per node (target country, exit address family,
//     precise position). Set in 节点管理.
//
// The per-node Policy the scheduler runs is derived from the two (see the
// regiondrift module's reconcile); it is never edited directly any more.
type Task struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	AutoRecover bool     `json:"auto_recover"`
	Services    []string `json:"services"`
	NodeIDs     []string `json:"node_ids"`
	CreatedAt   int64    `json:"created_at"`
}

type NodeSettings struct {
	Family   string    `json:"family"`
	Country  string    `json:"country"`
	Position *Position `json:"position,omitempty"`
}

// tasksDDL is regiondrift migration 2's tables.
const tasksDDL = `CREATE TABLE IF NOT EXISTS ipguard_tasks(id TEXT PRIMARY KEY, value TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS ipguard_node_settings(node_id TEXT PRIMARY KEY, value TEXT NOT NULL);`

// MaxTaskName bounds a task name, in characters.
const MaxTaskName = 40

// Validate checks a task as submitted (the id and creation time are the
// store's).
func (t Task) Validate() error {
	name := strings.TrimSpace(t.Name)
	if name == "" || utf8.RuneCountInString(name) > MaxTaskName {
		return fmt.Errorf("任务名称需要 1–%d 个字", MaxTaskName)
	}
	if len(t.Services) == 0 {
		return errors.New("至少选择一个服务")
	}
	seen := map[string]bool{}
	for _, id := range t.Services {
		if _, ok := Lookup(id); !ok || seen[id] {
			return errors.New("服务无效或重复")
		}
		seen[id] = true
	}
	nodes := map[string]bool{}
	for _, id := range t.NodeIDs {
		if id == "" || nodes[id] {
			return errors.New("节点为空或重复")
		}
		nodes[id] = true
	}
	return nil
}

// Validate checks node settings. Country must be a supported environment.
func (s NodeSettings) Validate() error {
	if _, ok := Environments[s.Country]; !ok {
		return errors.New("目标地区暂不支持")
	}
	if s.Family != "ipv4" && s.Family != "ipv6" {
		return errors.New("请选择 IPv4 或 IPv6")
	}
	if s.Position != nil {
		return s.Position.Validate(s.Country)
	}
	return nil
}

func (store *Database) Tasks(ctx context.Context) ([]Task, error) {
	rows, err := store.db.QueryContext(ctx, "SELECT value FROM ipguard_tasks ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var raw string
		var t Task
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ErrNoTask is an update or delete of a task that does not exist.
var ErrNoTask = errors.New("任务不存在")

// SaveTask creates (empty ID) or replaces a task. A node already in another
// task is refused: one node, one region drift task.
func (store *Database) SaveTask(ctx context.Context, t Task) (Task, error) {
	t.Name = strings.TrimSpace(t.Name)
	if err := t.Validate(); err != nil {
		return Task{}, err
	}
	if t.NodeIDs == nil {
		t.NodeIDs = []string{}
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	existing, err := tasksIn(ctx, tx)
	if err != nil {
		return Task{}, err
	}
	found := t.ID == ""
	for _, other := range existing {
		if other.ID == t.ID {
			found = true
			t.CreatedAt = other.CreatedAt
			continue
		}
		for _, id := range t.NodeIDs {
			if slices.Contains(other.NodeIDs, id) {
				return Task{}, fmt.Errorf("节点已在任务「%s」里，一台节点只能属于一个地区偏移检测任务", other.Name)
			}
		}
	}
	if !found {
		return Task{}, ErrNoTask
	}
	if t.ID == "" {
		t.ID = ID()[:16]
		t.CreatedAt = time.Now().Unix()
	}
	raw, _ := json.Marshal(t)
	if _, err := tx.ExecContext(ctx, "INSERT INTO ipguard_tasks VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", t.ID, string(raw), t.CreatedAt); err != nil {
		return Task{}, err
	}
	return t, tx.Commit()
}

func (store *Database) DeleteTask(ctx context.Context, id string) error {
	result, err := store.db.ExecContext(ctx, "DELETE FROM ipguard_tasks WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNoTask
	}
	return nil
}

func tasksIn(ctx context.Context, tx *sql.Tx) ([]Task, error) {
	rows, err := tx.QueryContext(ctx, "SELECT value FROM ipguard_tasks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var raw string
		var t Task
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (store *Database) NodeSettings(ctx context.Context) (map[string]NodeSettings, error) {
	rows, err := store.db.QueryContext(ctx, "SELECT node_id, value FROM ipguard_node_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]NodeSettings{}
	for rows.Next() {
		var node, raw string
		var s NodeSettings
		if err := rows.Scan(&node, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, err
		}
		out[node] = s
	}
	return out, rows.Err()
}

func (store *Database) SaveNodeSettings(ctx context.Context, node string, s NodeSettings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	raw, _ := json.Marshal(s)
	_, err := store.db.ExecContext(ctx, "INSERT INTO ipguard_node_settings VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET value=excluded.value", node, string(raw))
	return err
}

func (store *Database) DeleteNodeSettings(ctx context.Context, node string) error {
	_, err := store.db.ExecContext(ctx, "DELETE FROM ipguard_node_settings WHERE node_id=?", node)
	return err
}

// importPolicies is regiondrift migration 2's data step: every existing policy
// becomes node settings (country, family of its exit, position), and nodes
// with the same switches and services are grouped into one task each, so the
// derived policies come out exactly as they were.
func importPolicies(tx *sql.Tx) error {
	rows, err := tx.Query("SELECT node_id, value FROM ipguard_policies ORDER BY node_id")
	if err != nil {
		return err
	}
	type group struct {
		task  Task
		order int
	}
	groups := map[string]*group{}
	settings := map[string]NodeSettings{}
	for rows.Next() {
		var node, raw string
		var p Policy
		if err := rows.Scan(&node, &raw); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			rows.Close()
			return err
		}
		family := "ipv4"
		if strings.Contains(p.ExpectedIP, ":") {
			family = "ipv6"
		}
		settings[node] = NodeSettings{Family: family, Country: p.Country, Position: p.Position}
		services := slices.Clone(p.Services)
		sort.Strings(services)
		key := fmt.Sprintf("%t/%t/%s", p.Enabled, p.AutoRecover, strings.Join(services, ","))
		g, ok := groups[key]
		if !ok {
			g = &group{task: Task{Enabled: p.Enabled, AutoRecover: p.AutoRecover, Services: p.Services}, order: len(groups)}
			groups[key] = g
		}
		g.task.NodeIDs = append(g.task.NodeIDs, node)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for node, s := range settings {
		raw, _ := json.Marshal(s)
		if _, err := tx.Exec("INSERT INTO ipguard_node_settings VALUES(?,?)", node, string(raw)); err != nil {
			return err
		}
	}
	list := make([]*group, 0, len(groups))
	for _, g := range groups {
		list = append(list, g)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].order < list[j].order })
	now := time.Now().Unix()
	for i, g := range list {
		t := g.task
		t.ID = ID()[:16]
		t.Name = "地区偏移检测"
		if len(list) > 1 {
			t.Name = fmt.Sprintf("地区偏移检测 %d", i+1)
		}
		if !t.Enabled {
			t.Name += "（已停用）"
		}
		t.CreatedAt = now + int64(i)
		raw, _ := json.Marshal(t)
		if _, err := tx.Exec("INSERT INTO ipguard_tasks VALUES(?,?,?)", t.ID, string(raw), t.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}
