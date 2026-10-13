package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// migrate brings the database at db up to the current version: a fresh file
// gets the baseline schema, then every step after its user_version runs in
// order. Each step commits its own user_version, so a crash between steps
// resumes where it stopped.
func migrate(db *sql.DB) (err error) {
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 14 {
		return errors.New("database version is newer than this binary")
	}
	if version == 0 {
		// Refuse accidental use of a legacy database, even when it has user_version=0.
		var tables int
		if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			return errors.New("database is not an empty TZA Probe database")
		}
		_, err = db.Exec(schema)
		if err != nil {
			return err
		}
		version = 1
	}
	if version < 2 {
		if _, err = db.Exec("BEGIN; ALTER TABLE events ADD COLUMN last_error TEXT NOT NULL DEFAULT ''; CREATE INDEX events_rule_time ON events(node_id,kind,at); PRAGMA user_version=2; COMMIT;"); err != nil {
			return err
		}
	}
	if version < 3 {
		// 重置日从「每月第几天」改成带秒的锚点时刻，旧的 config 就地转换。
		if err = migrateBillingAnchor(db, nowFunc()); err != nil {
			return err
		}
	}
	if version < 4 {
		// 「每个目标最新一次」从「读时扫窗口」改成物化表 + 触发器。
		if err = migrateLatest(db, nowFunc()); err != nil {
			return err
		}
	}
	if version < 5 {
		// 小时流量桶，供「窗口内平均带宽」（达量降速）使用。不回填：天桶切不出小时，
		// 而窗口本来就从这台机器开始上报的那一刻算起（CoveredHours 会如实写出来）。
		if _, err = db.Exec("BEGIN; CREATE TABLE IF NOT EXISTS traffic_hour(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, hour INTEGER NOT NULL, incoming INTEGER NOT NULL, outgoing INTEGER NOT NULL, PRIMARY KEY(node_id,hour)); CREATE INDEX IF NOT EXISTS traffic_hour_age ON traffic_hour(hour); PRAGMA user_version=5; COMMIT;"); err != nil {
			return err
		}
	}
	if version < 6 {
		// 解锁检测结论（每台节点 × 每个服务一行）。不回填：结论是 Agent 报上来的，
		// 没有历史数据可推。
		if _, err = db.Exec("BEGIN; CREATE TABLE IF NOT EXISTS unlocks(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, service TEXT NOT NULL, status TEXT NOT NULL, region TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', tested_at INTEGER NOT NULL, PRIMARY KEY(node_id,service)); CREATE INDEX IF NOT EXISTS unlocks_age ON unlocks(tested_at); PRAGMA user_version=6; COMMIT;"); err != nil {
			return err
		}
	}
	if version < 7 {
		// 回程补测队列（每台节点 × 每条探针一行，见 Store.Finish）。不回填：它是
		// 「刚出了一次未知」的短命标记，迁移时的旧结果没有补测可言。
		if _, err = db.Exec("BEGIN; CREATE TABLE IF NOT EXISTS retries(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, probe_key TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY(node_id,probe_key)); CREATE INDEX IF NOT EXISTS retries_at ON retries(at); PRAGMA user_version=7; COMMIT;"); err != nil {
			return err
		}
	}
	if version < 8 {
		// 两个删除路径上的索引。没有它们时：jobs_latest_delete 触发器每删一行 job 就把 latest
		// 整表扫一遍（Clean 每小时删几万行，全程持写锁）；SaveTask / DeleteTask 的
		// DELETE FROM jobs WHERE task_id=?（以及外键级联）把七天的 jobs 整表扫一遍。写锁
		// 被占住时所有 Agent 的上报与领任务都在排队。
		if _, err = db.Exec("BEGIN; CREATE INDEX IF NOT EXISTS latest_job ON latest(job_id); CREATE INDEX IF NOT EXISTS jobs_task ON jobs(task_id); PRAGMA user_version=8; COMMIT;"); err != nil {
			return err
		}
	}
	if version < 9 {
		// 后台分批删除的队列（见 purge.go）：改任务、删任务、删节点时要删的大批历史行。
		if _, err = db.Exec("BEGIN; CREATE TABLE IF NOT EXISTS purges(id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, target TEXT NOT NULL, cutoff INTEGER NOT NULL); PRAGMA user_version=9; COMMIT;"); err != nil {
			return err
		}
	}
	if version < 10 {
		// 「免费」以前写在价格周期里（price_period 含「免费」），现在是单独的 free 字段：
		// 就地改成 free=true，并清掉价格与周期，之后周期里不再接受「免费」。
		if _, err = db.Exec(`BEGIN; UPDATE nodes SET config=json_set(config,'$.free',json('true'),'$.price',0,'$.price_period','') WHERE instr(coalesce(json_extract(config,'$.price_period'),''),'免费')>0; PRAGMA user_version=10; COMMIT;`); err != nil {
			return err
		}
	}
	if version < 11 {
		// 任务改成 type + probe / unlock 的联合结构：旧的组合任务、单项任务与解锁任务就地转换。
		if err = migrateTaskShape(db); err != nil {
			return err
		}
	}
	if version < 12 {
		// 节点配置按关注点分组（display / billing / line），服务端自记的创建时间与用量校正
		// 从配置里搬到单独的列。
		if err = migrateNodeShape(db); err != nil {
			return err
		}
	}
	if version < 13 {
		// 提醒规则从「开关 + 阈值」平铺对象改成规则列表；负载规则拆成 CPU 与内存两条。
		if err = migrateAlertRules(db); err != nil {
			return err
		}
	}
	if version < 14 {
		// 提醒规则从每台节点各存一份改成单独的规则表，按范围（全部 / 分组 / 指定节点）作用。
		if err = migrateAlertScopes(db); err != nil {
			return err
		}
	}
	return nil
}

// legacyTask is every shape a stored task config had before version 11: the
// combined form (checks × targets), the single-probe form (kind + one target)
// and the unlock form (kind unlock + interval / family).
type legacyTask struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Enabled       bool           `json:"enabled"`
	NodeIDs       []string       `json:"node_ids"`
	Checks        []string       `json:"checks"`
	Targets       []model.Target `json:"targets"`
	RouteInterval int            `json:"route_interval"`
	Kind          string         `json:"kind"`
	Host          string         `json:"host"`
	Port          int            `json:"port"`
	Family        string         `json:"family"`
	Region        string         `json:"region"`
	Carrier       string         `json:"carrier"`
	Interval      int            `json:"interval"`
}

// upgrade converts a legacy config into the tagged union. The probe keys stay
// exactly as they were (same task id, kind, family, host, port, region and
// carrier per probe), so results, history and schedules carry over.
func (l legacyTask) upgrade() model.Task {
	t := model.Task{ID: l.ID, Name: l.Name, Enabled: l.Enabled, NodeIDs: l.NodeIDs}
	if t.NodeIDs == nil {
		t.NodeIDs = []string{}
	}
	switch {
	case l.Kind == model.UnlockKind:
		t.Type = model.TaskUnlock
		t.Unlock = &model.UnlockSpec{Interval: l.Interval, Family: l.Family}
	case len(l.Checks) > 0 || len(l.Targets) > 0:
		t.Type = model.TaskProbe
		t.Probe = &model.ProbeSpec{Checks: l.Checks, Targets: l.Targets, Interval: l.Interval, RouteInterval: l.RouteInterval}
	default:
		// 单项任务：一个检测项、一个目标；它的间隔按检测类型落到对应字段。
		t.Type = model.TaskProbe
		spec := &model.ProbeSpec{Checks: []string{l.Kind}, Targets: []model.Target{{Host: l.Host, Port: l.Port, Family: l.Family, Region: l.Region, Carrier: l.Carrier}}}
		if l.Kind == model.RouteKind {
			spec.RouteInterval = l.Interval
		} else {
			spec.Interval = l.Interval
		}
		t.Probe = spec
	}
	return t
}

// migrateTaskShape rewrites every task config into the version 11 shape. A row
// already in that shape (it has a type) is left alone.
func migrateTaskShape(db *sql.DB) error {
	rows, err := db.Query("SELECT id, config FROM tasks")
	if err != nil {
		return err
	}
	type change struct{ id, config string }
	var changes []change
	for rows.Next() {
		var id, config string
		if err = rows.Scan(&id, &config); err != nil {
			rows.Close()
			return err
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err = json.Unmarshal([]byte(config), &probe); err != nil {
			rows.Close()
			return err
		}
		if probe.Type != "" {
			continue
		}
		var legacy legacyTask
		if err = json.Unmarshal([]byte(config), &legacy); err != nil {
			rows.Close()
			return err
		}
		changes = append(changes, change{id, encode(legacy.upgrade())})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range changes {
		if _, err = tx.Exec("UPDATE tasks SET config=? WHERE id=?", c.config, c.id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("PRAGMA user_version=11"); err != nil {
		return err
	}
	return tx.Commit()
}

// legacyNode is a node config before version 12: one flat object that also
// carried the server-kept creation time and cycle correction.
type legacyNode struct {
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	Group             string       `json:"group"`
	Region            string       `json:"region"`
	Order             int          `json:"order"`
	Public            bool         `json:"public"`
	Representative    string       `json:"representative"`
	LatencyRegion     string       `json:"latency_region"`
	LatencyCarriers   []string     `json:"latency_carriers"`
	TrafficLimit      int64        `json:"traffic_limit"`
	TrafficMode       string       `json:"traffic_mode"`
	BillingAt         string       `json:"billing_at"`
	Free              bool         `json:"free"`
	Price             float64      `json:"price"`
	Currency          string       `json:"currency"`
	PricePeriod       string       `json:"price_period"`
	ExpiresAt         string       `json:"expires_at"`
	LineMbps          int          `json:"line_mbps"`
	ThrottleMbps      int          `json:"throttle_mbps"`
	ThrottleHours     int          `json:"throttle_hours"`
	ThrottleOn        string       `json:"throttle_on"`
	Alerts            legacyAlerts `json:"alerts"`
	CreatedAt         int64        `json:"created_at"`
	TrafficAdjustment int64        `json:"traffic_adjustment"`
	AdjustmentCycle   int64        `json:"adjustment_cycle"`
}

// nodeV12 is a node config as versions 12 and 13 stored it: the alert rules
// still lived on the node (version 14 moved them to their own table).
type nodeV12 struct {
	model.Node
	Alerts model.Alerts `json:"alerts"`
}

func (l legacyNode) upgrade() nodeV12 {
	return nodeV12{Node: model.Node{
		ID: l.ID, Name: l.Name, Group: l.Group, Region: l.Region, Order: l.Order, Public: l.Public,
		Display: model.NodeDisplay{RouteRegion: l.Representative, LatencyRegion: l.LatencyRegion, LatencyCarriers: l.LatencyCarriers},
		Billing: model.Billing{ResetAt: l.BillingAt, TrafficLimit: l.TrafficLimit, TrafficMode: l.TrafficMode, Free: l.Free,
			Price: l.Price, Currency: l.Currency, PricePeriod: l.PricePeriod, ExpiresAt: l.ExpiresAt},
		Line: model.Line{Mbps: l.LineMbps, ThrottleMbps: l.ThrottleMbps, ThrottleHours: l.ThrottleHours, ThrottleOn: l.ThrottleOn},
	}, Alerts: l.Alerts.upgrade()}
}

// legacyAlerts is a node's alert configuration before version 13: one flat
// object of on/off switches and thresholds, one repeat interval for all of
// them, and a single load rule covering CPU or memory.
type legacyAlerts struct {
	OfflineSeconds int     `json:"offline_seconds"`
	RepeatSeconds  int     `json:"repeat_seconds"`
	Offline        bool    `json:"offline"`
	Renewal        bool    `json:"renewal"`
	RenewalDays    int     `json:"renewal_days"`
	Traffic        bool    `json:"traffic"`
	TrafficPercent float64 `json:"traffic_percent"`
	Throttle       bool    `json:"throttle"`
	Load           bool    `json:"load"`
	CPUPercent     float64 `json:"cpu_percent"`
	MemoryPercent  float64 `json:"memory_percent"`
	HoldSeconds    int     `json:"hold_seconds"`
}

// upgrade keeps every rule that was on, with its thresholds; the shared repeat
// interval goes onto each of them. A rule that was off had only remembered
// thresholds, and the list form has no off rules to keep them on.
func (l legacyAlerts) upgrade() model.Alerts {
	out := model.Alerts{}
	add := func(on bool, r model.AlertRule) {
		if on {
			r.RepeatSeconds = l.RepeatSeconds
			out = append(out, r)
		}
	}
	add(l.Offline, model.AlertRule{Kind: model.AlertOffline, HoldSeconds: l.OfflineSeconds})
	add(l.Renewal, model.AlertRule{Kind: model.AlertRenewal, Threshold: float64(l.RenewalDays)})
	add(l.Traffic, model.AlertRule{Kind: model.AlertTraffic, Threshold: l.TrafficPercent})
	add(l.Throttle, model.AlertRule{Kind: model.AlertThrottle})
	add(l.Load, model.AlertRule{Kind: model.AlertCPU, Threshold: l.CPUPercent, HoldSeconds: l.HoldSeconds})
	add(l.Load, model.AlertRule{Kind: model.AlertMemory, Threshold: l.MemoryPercent, HoldSeconds: l.HoldSeconds})
	return out
}

// migrateAlertRules rewrites every node's alerts into the version 13 rule
// list. A config whose alerts is already a list is left alone. The debounce
// state of the retired load rule goes too: CPU and memory are tracked anew.
func migrateAlertRules(db *sql.DB) error {
	rows, err := db.Query("SELECT id, config FROM nodes")
	if err != nil {
		return err
	}
	type change struct{ id, config string }
	var changes []change
	for rows.Next() {
		var id, config string
		if err = rows.Scan(&id, &config); err != nil {
			rows.Close()
			return err
		}
		fields := map[string]json.RawMessage{}
		if err = json.Unmarshal([]byte(config), &fields); err != nil {
			rows.Close()
			return err
		}
		raw := fields["alerts"]
		if len(raw) > 0 && raw[0] == '[' {
			continue
		}
		var legacy legacyAlerts
		if len(raw) > 0 && string(raw) != "null" {
			if err = json.Unmarshal(raw, &legacy); err != nil {
				rows.Close()
				return err
			}
		}
		fields["alerts"] = json.RawMessage(encode(legacy.upgrade()))
		changes = append(changes, change{id, encode(fields)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range changes {
		if _, err = tx.Exec("UPDATE nodes SET config=? WHERE id=?", c.config, c.id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("DELETE FROM alert_state WHERE kind='load'"); err != nil {
		return err
	}
	if _, err = tx.Exec("PRAGMA user_version=13"); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateAlertScopes moves every node's own rule list into the alert_rules
// table. Nodes that carried the very same rule (kind and every parameter) share
// one node-scoped rule listing them, so nothing changes about who is alerted
// and a fleet with one common setting sees one rule per kind. Rules never widen
// to 全部节点 here: a node added later must not start alerting by itself.
func migrateAlertScopes(db *sql.DB) error {
	rows, err := db.Query("SELECT id, config FROM nodes ORDER BY rowid")
	if err != nil {
		return err
	}
	var rules []model.AlertRule
	index := map[string]int{}
	for rows.Next() {
		var id, config string
		if err = rows.Scan(&id, &config); err != nil {
			rows.Close()
			return err
		}
		var node struct {
			Alerts []model.AlertRule `json:"alerts"`
		}
		if err = json.Unmarshal([]byte(config), &node); err != nil {
			rows.Close()
			return err
		}
		for _, r := range node.Alerts {
			key := encode(model.AlertRule{Kind: r.Kind, Threshold: r.Threshold, HoldSeconds: r.HoldSeconds, RepeatSeconds: r.RepeatSeconds})
			i, seen := index[key]
			if !seen {
				i = len(rules)
				index[key] = i
				rules = append(rules, model.AlertRule{ID: model.Secret()[:16], Kind: r.Kind, Threshold: r.Threshold,
					HoldSeconds: r.HoldSeconds, RepeatSeconds: r.RepeatSeconds, Scope: model.ScopeNodes})
			}
			rules[i].NodeIDs = append(rules[i].NodeIDs, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("CREATE TABLE IF NOT EXISTS alert_rules(id TEXT PRIMARY KEY, config TEXT NOT NULL)"); err != nil {
		return err
	}
	for _, r := range rules {
		if _, err = tx.Exec("INSERT INTO alert_rules(id,config) VALUES(?,?)", r.ID, encode(r)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE nodes SET config=json_remove(config,'$.alerts')"); err != nil {
		return err
	}
	if _, err = tx.Exec("PRAGMA user_version=14"); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateNodeShape adds the server-state columns, moves those values out of
// every config and rewrites the config into the grouped version 12 shape.
func migrateNodeShape(db *sql.DB) error {
	rows, err := db.Query("SELECT id, config FROM nodes")
	if err != nil {
		return err
	}
	type change struct {
		id, config                    string
		created, adjustment, adjusted int64
	}
	var changes []change
	for rows.Next() {
		var id, config string
		if err = rows.Scan(&id, &config); err != nil {
			rows.Close()
			return err
		}
		var shape struct {
			Billing json.RawMessage `json:"billing"`
		}
		if err = json.Unmarshal([]byte(config), &shape); err != nil {
			rows.Close()
			return err
		}
		if shape.Billing != nil {
			continue // 已经是分组后的形状
		}
		var legacy legacyNode
		if err = json.Unmarshal([]byte(config), &legacy); err != nil {
			rows.Close()
			return err
		}
		changes = append(changes, change{id, encode(legacy.upgrade()), legacy.CreatedAt, legacy.TrafficAdjustment, legacy.AdjustmentCycle})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing := map[string]bool{}
	columns, err := tx.Query("SELECT name FROM pragma_table_info('nodes')")
	if err != nil {
		return err
	}
	for columns.Next() {
		var name string
		if err = columns.Scan(&name); err != nil {
			columns.Close()
			return err
		}
		existing[name] = true
	}
	columns.Close()
	for _, column := range []string{"created_at", "traffic_adjustment", "adjustment_cycle"} {
		if existing[column] {
			continue
		}
		if _, err = tx.Exec("ALTER TABLE nodes ADD COLUMN " + column + " INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	for _, c := range changes {
		if _, err = tx.Exec("UPDATE nodes SET config=?,created_at=?,traffic_adjustment=?,adjustment_cycle=? WHERE id=?", c.config, c.created, c.adjustment, c.adjusted, c.id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("PRAGMA user_version=12"); err != nil {
		return err
	}
	return tx.Commit()
}

// nowFunc is swapped in tests so the anchor migration can be asserted exactly.
var nowFunc = time.Now

// migrateBillingAnchor rewrites every node config that still carries billing_day
// (a day of month) into billing_at (a full instant).
func migrateBillingAnchor(db *sql.DB, now time.Time) error {
	rows, err := db.Query("SELECT id, config FROM nodes")
	if err != nil {
		return err
	}
	type change struct{ id, config string }
	var changes []change
	for rows.Next() {
		var id, config string
		if err = rows.Scan(&id, &config); err != nil {
			rows.Close()
			return err
		}
		var fields map[string]any
		if err = json.Unmarshal([]byte(config), &fields); err != nil {
			rows.Close()
			return err
		}
		if _, migrated := fields["billing_at"]; migrated {
			continue
		}
		day := 1
		if raw, ok := fields["billing_day"].(float64); ok && raw >= 1 && raw <= 31 {
			day = int(raw)
		}
		fields["billing_at"] = anchorFromDay(day, now).Format(time.RFC3339)
		delete(fields, "billing_day")
		encoded, err := json.Marshal(fields)
		if err != nil {
			rows.Close()
			return err
		}
		changes = append(changes, change{id, string(encoded)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range changes {
		if _, err = tx.Exec("UPDATE nodes SET config=? WHERE id=?", c.config, c.id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("PRAGMA user_version=3"); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateLatest 建物化表 latest 并回填窗口内已有的结果。
//
// 顺序与代价：加列是 schema-only（常量默认值，不重写表）；回填只覆盖 ProbeWindow
// 以内的行——窗口外的行面板本来也不显示，换算量后线上扫描从 47 万行降到窗口内的
// 几万行，1 核机的启动迁移才不会变成几十秒的停机。历史行不回填 jobs.probe_key
// （整表 UPDATE 是最贵的一步，且没必要）：触发器在 probe_key 为空时用 SQL 侧的
// 同构表达式兜底，两条路径算出的键一致，同一分组不会被拆开。
func migrateLatest(db *sql.DB, now time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	start := time.Now()
	window := now.Add(-ProbeWindow).Unix()
	steps := []string{
		"ALTER TABLE jobs ADD COLUMN probe_key TEXT NOT NULL DEFAULT ''",
		latestSchema,
		`INSERT INTO latest(node_id,probe_key,job_id,task,issued,finished,result)
		 SELECT node_id,CASE WHEN probe_key<>'' THEN probe_key ELSE ` + probeKeyExpr("") + ` END,id,task,issued,finished,result
		   FROM jobs WHERE json_valid(task) AND issued>=` + strconv.FormatInt(window, 10) + ` AND id IN (
		   SELECT id FROM (SELECT id,row_number() OVER(PARTITION BY node_id,CASE WHEN probe_key<>'' THEN probe_key ELSE ` + probeKeyExpr("") + ` END ORDER BY issued DESC,id DESC) n
		     FROM jobs WHERE json_valid(task) AND issued>=` + strconv.FormatInt(window, 10) + `) WHERE n=1)`,
		"PRAGMA user_version=4",
	}
	for _, step := range steps {
		if _, err = tx.Exec(step); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// 迁移只在版本升级时跑一次，把行数与耗时留在日志里：1 核机换二进制重启时，
	// 「面板还要多久回来」就看这一行。
	var rows int
	if err = db.QueryRow("SELECT count(*) FROM latest").Scan(&rows); err != nil {
		return err
	}
	slog.Info("materialized latest observations", "rows", rows, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

// anchorFromDay picks the anchor for a legacy day of month: the newest date not
// after now whose month really has that day. time.Date rolls a too-long day into
// the next month, which is how a mismatch is detected — clamping here would move
// the configured day for every later cycle.
func anchorFromDay(day int, now time.Time) time.Time {
	now = now.UTC()
	day = min(max(day, 1), 31)
	for i := 0; i < 48; i++ {
		candidate := time.Date(now.Year(), time.Month(int(now.Month())-i), day, 0, 0, 0, 0, time.UTC)
		if candidate.Day() == day && !candidate.After(now) {
			return candidate
		}
	}
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}
