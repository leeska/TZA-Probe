package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// A v2 node config only had billing_day (day of month). Opening that database
// must turn it into an anchor instant without moving the day, keep unrelated
// keys, and leave the cycle boundaries where they were.
func TestBillingDayMigratesToAnchor(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	restore := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = restore }()

	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(schema)
	exec("BEGIN; ALTER TABLE events ADD COLUMN last_error TEXT NOT NULL DEFAULT ''; CREATE INDEX events_rule_time ON events(node_id,kind,at); PRAGMA user_version=2; COMMIT;")
	configs := map[string]string{
		"day1":  `{"id":"day1","name":"a","billing_day":1,"traffic_mode":"sum","currency":"USD"}`,
		"day15": `{"id":"day15","name":"b","billing_day":15,"traffic_mode":"sum"}`,
		"day30": `{"id":"day30","name":"c","billing_day":30,"traffic_mode":"sum"}`,
		"day31": `{"id":"day31","name":"d","billing_day":31,"traffic_mode":"sum"}`,
		"none":  `{"id":"none","name":"e","traffic_mode":"sum"}`,
	}
	for id, config := range configs {
		exec("INSERT INTO nodes(id,config,token_hash) VALUES('" + id + "','" + config + "','" + id + "-hash')")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	views, err := s.Nodes(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"day1":  "2026-09-01T00:00:00Z",
		"day15": "2026-09-15T00:00:00Z",
		"day30": "2026-08-30T00:00:00Z", // 9 月 30 日还没到，锚点退回 8 月
		"day31": "2026-08-31T00:00:00Z", // 锚点必须落在真有 31 日的月份，不能被钳成 30 日
		"none":  "2026-09-01T00:00:00Z", // 缺字段退回每月 1 日
	}
	if len(views) != len(want) {
		t.Fatalf("节点数 = %d, want %d", len(views), len(want))
	}
	for _, v := range views {
		if v.Billing.ResetAt != want[v.ID] {
			t.Errorf("%s: billing_at = %q, want %q", v.ID, v.Billing.ResetAt, want[v.ID])
		}
	}
	// 周期边界与旧规则一致：31 日的锚点在 9 月被钳到 9 月 30 日。
	for _, v := range views {
		if v.ID != "day31" {
			continue
		}
		start := time.Unix(v.CycleStart, 0).UTC().Format(time.RFC3339)
		end := time.Unix(v.CycleEnd, 0).UTC().Format(time.RFC3339)
		if start != "2026-08-31T00:00:00Z" || end != "2026-09-30T00:00:00Z" {
			t.Fatalf("day31 周期 = %s -> %s", start, end)
		}
	}

	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 14 {
		t.Fatalf("user_version = %d (%v)", version, err)
	}
	var raw string
	if err = s.db.QueryRow("SELECT config FROM nodes WHERE id='day1'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	if err = json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	if _, stale := fields["billing_day"]; stale {
		t.Errorf("billing_day 应已移除: %s", raw)
	}
	// v12 之后计费字段在 billing 分组里。
	billing, _ := fields["billing"].(map[string]any)
	if billing["currency"] != "USD" || fields["name"] != "a" {
		t.Errorf("其他字段被改动: %s", raw)
	}
	// 再开一次不应重复迁移（config 里已有 billing_at）。
	s.Close()
	if again, err := Open(path); err != nil {
		t.Fatal(err)
	} else {
		defer again.Close()
		views, err = again.Nodes(context.Background(), now)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range views {
			if v.Billing.ResetAt != want[v.ID] {
				t.Errorf("二次打开后 %s: billing_at = %q", v.ID, v.Billing.ResetAt)
			}
		}
	}
}
