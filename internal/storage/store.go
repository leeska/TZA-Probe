// Package storage is the SQLite adapter. It never imports HTTP, Agent, or UI code.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	_ "modernc.org/sqlite"
)

var ErrNotFound = model.ErrNotFound
var ErrConflict = model.ErrConflict

type Store struct {
	db     *sql.DB
	read   *sql.DB
	writes sync.Mutex
}

// readConns is how many connections serve read-only queries. One writer
// connection is enough for the write paths (they are serialized by writes), but
// the panel fans out: the home page asks for the node list, the last probes of
// every node and the per-target observations at once, and with a single
// connection those 18 requests queue head to tail. 线上就是这样：延迟与回程要
// 等好几秒才出来，而每次请求本身只要几十毫秒。
const readConns = 4

// openReadOnly opens the same file a second time for reads. WAL lets readers and
// the writer run side by side, and query_only makes a stray write fail loudly
// instead of quietly going through a connection that never sets foreign_keys.
// The PRAGMAs ride in the DSN because a plain Exec only reaches the connections
// that already exist, and a pool creates more later.
func openReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(readConns)
	db.SetMaxIdleConns(readConns)
	return db, nil
}

// query / queryRow are the read paths: they go to the read pool so a slow panel
// request cannot hold up the writer or a second panel request.
func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.read.QueryContext(ctx, query, args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.read.QueryRowContext(ctx, query, args...)
}

// ProbeWindow bounds how far back the panel looks for each target's newest
// result. Probes run every 30-60s (TCP) and every hour (route), and a result
// older than a few minutes already renders as stale, so a day holds far more
// history than the panel can show.
//
// 这个界不是装饰：jobs 保留七天，窗口之外的旧值本来也不该显示。这里只影响
// 物化表（latest）里哪些行算数，不再决定读路径要扫多少行：分组键在写入时就
// 落在 jobs.probe_key 上，latest 由触发器维护（每节点几十行），所以窗口大小
// 与首页耗时无关了。
const ProbeWindow = 24 * time.Hour

// probeKeyExpr 是 model.Task.ProbeKey 的 SQL 版本，只在 jobs.probe_key 为空时兜底
// （正常写入路径由 Go 侧算好；测试与历史行可能没带）。prefix 传 "NEW." 或 ""：
// 触发器里必须写 NEW.task_id，迁移的 UPDATE 里是裸列。两边格式必须一致：
// <任务 id>|<kind>|<family>|<host>|<port>|<region>|<carrier>。
// 每个字段都 COALESCE 到空串：老数据里缺 key 的行 json_extract 返回 NULL，直接
// 拼接会让整键变 NULL（写入 NOT NULL 列会失败）；用空串则所有缺字段的行仍归一组，
// 与旧查询「NULL 与 NULL 同组」的行为一致。
func probeKeyExpr(prefix string) string {
	field := func(key string) string {
		return "COALESCE(json_extract(" + prefix + "task,'$." + key + "'),'')"
	}
	return "COALESCE(" + prefix + `task_id,'')||'|'||` + field("kind") + "||'|'||" + field("family") + "||'|'||" +
		field("host") + "||'|'||" + field("port") + "||'|'||" + field("region") + "||'|'||" + field("carrier")
}

// latestKeySQL 取一行的分组键：优先用写入时算好的 jobs.probe_key。
func latestKeySQL() string {
	return `CASE WHEN NEW.probe_key<>'' THEN NEW.probe_key ELSE ` + probeKeyExpr("NEW.") + ` END`
}

// latestSchema 是「每个分组最新一次结果」的物化表与它的触发器。jobs 只追加，
// 读路径原先每次都要在窗口内重新 json_extract 上万行（线上 65ms/节点、首页
// 扇出 465ms），这张表把同一份结果留在几十行里（实测 0.03ms/节点）。
//   - INSERT：比 (issued,id) 更新才覆盖，与旧 SQL 的 ORDER BY issued DESC,id DESC 一致；
//   - UPDATE：只在被更新的行正是该组的最新那条时刷新（乱序完成的老任务不覆盖新的）；
//   - DELETE：Clean 的保留期删除与 SaveTask 的「改任务即清历史」都靠它收尾。
//
// json_valid 守卫保留：任务 JSON 坏掉的行不进物化表，这一组继续显示更早那条，
// 与旧查询用 json_valid 过滤掉坏行的行为一致。
var latestSchema = `
CREATE TABLE latest(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, probe_key TEXT NOT NULL, job_id TEXT NOT NULL, task TEXT NOT NULL, issued INTEGER NOT NULL, finished INTEGER, result TEXT, PRIMARY KEY(node_id,probe_key));
CREATE TRIGGER jobs_latest_insert AFTER INSERT ON jobs WHEN json_valid(NEW.task) BEGIN
  INSERT INTO latest(node_id,probe_key,job_id,task,issued,finished,result)
  VALUES(NEW.node_id,` + latestKeySQL() + `,NEW.id,NEW.task,NEW.issued,NEW.finished,NEW.result)
  ON CONFLICT(node_id,probe_key) DO UPDATE SET job_id=excluded.job_id,task=excluded.task,issued=excluded.issued,finished=excluded.finished,result=excluded.result
  WHERE excluded.issued>latest.issued OR (excluded.issued=latest.issued AND excluded.job_id>latest.job_id);
END;
CREATE TRIGGER jobs_latest_update AFTER UPDATE OF task,issued,finished,result ON jobs WHEN json_valid(NEW.task) BEGIN
  UPDATE latest SET task=NEW.task,issued=NEW.issued,finished=NEW.finished,result=NEW.result
  WHERE job_id=NEW.id AND node_id=NEW.node_id;
END;
CREATE TRIGGER jobs_latest_delete AFTER DELETE ON jobs BEGIN
  DELETE FROM latest WHERE job_id=OLD.id;
END;`

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > 6 {
		db.Close()
		return nil, errors.New("database version is newer than this binary")
	}
	if version == 0 {
		// Refuse accidental use of a legacy database, even when it has user_version=0.
		var tables int
		if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			db.Close()
			return nil, err
		}
		if tables != 0 {
			db.Close()
			return nil, errors.New("database is not an empty TZA v2 database")
		}
		_, err = db.Exec(schema)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	if version < 2 {
		if _, err = db.Exec("BEGIN; ALTER TABLE events ADD COLUMN last_error TEXT NOT NULL DEFAULT ''; CREATE INDEX events_rule_time ON events(node_id,kind,at); PRAGMA user_version=2; COMMIT;"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version < 3 {
		// 重置日从「每月第几天」改成带秒的锚点时刻，旧的 config 就地转换。
		if err = migrateBillingAnchor(db, nowFunc()); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version < 4 {
		// 「每个目标最新一次」从「读时扫窗口」改成物化表 + 触发器。
		if err = migrateLatest(db, nowFunc()); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version < 5 {
		// 小时流量桶，供「窗口内平均带宽」（达量降速）使用。不回填：天桶切不出小时，
		// 而窗口本来就从这台机器开始上报的那一刻算起（CoveredHours 会如实写出来）。
		if _, err = db.Exec("BEGIN; CREATE TABLE IF NOT EXISTS traffic_hour(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, hour INTEGER NOT NULL, incoming INTEGER NOT NULL, outgoing INTEGER NOT NULL, PRIMARY KEY(node_id,hour)); CREATE INDEX IF NOT EXISTS traffic_hour_age ON traffic_hour(hour); PRAGMA user_version=5; COMMIT;"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if version < 6 {
		// 解锁检测结论（每台节点 × 每个服务一行）。不回填：结论是 Agent 报上来的，
		// 没有历史数据可推。
		if _, err = db.Exec("BEGIN; CREATE TABLE IF NOT EXISTS unlocks(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, service TEXT NOT NULL, status TEXT NOT NULL, region TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', tested_at INTEGER NOT NULL, PRIMARY KEY(node_id,service)); CREATE INDEX IF NOT EXISTS unlocks_age ON unlocks(tested_at); PRAGMA user_version=6; COMMIT;"); err != nil {
			db.Close()
			return nil, err
		}
	}
	read, err := openReadOnly(path)
	if err != nil {
		db.Close()
		return nil, err
	}
	s.read = read
	return s, nil
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

const schema = `BEGIN;
CREATE TABLE nodes(id TEXT PRIMARY KEY, config TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, last_seen INTEGER NOT NULL DEFAULT 0, metrics TEXT);
CREATE TABLE samples(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, at INTEGER NOT NULL, payload TEXT NOT NULL, PRIMARY KEY(node_id,at));
CREATE INDEX samples_age ON samples(at);
CREATE TABLE traffic(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, day INTEGER NOT NULL, incoming INTEGER NOT NULL, outgoing INTEGER NOT NULL, PRIMARY KEY(node_id,day));
CREATE TABLE traffic_hour(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, hour INTEGER NOT NULL, incoming INTEGER NOT NULL, outgoing INTEGER NOT NULL, PRIMARY KEY(node_id,hour));
CREATE INDEX traffic_hour_age ON traffic_hour(hour);
CREATE TABLE tasks(id TEXT PRIMARY KEY, config TEXT NOT NULL);
CREATE TABLE bindings(task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, due INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(task_id,node_id));
CREATE TABLE jobs(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, task TEXT NOT NULL, issued INTEGER NOT NULL, finished INTEGER, result TEXT);
CREATE INDEX jobs_node ON jobs(node_id,issued);
CREATE INDEX jobs_age ON jobs(issued);
CREATE TABLE alert_state(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, kind TEXT NOT NULL, since INTEGER NOT NULL, active INTEGER NOT NULL, PRIMARY KEY(node_id,kind));
CREATE TABLE events(id INTEGER PRIMARY KEY AUTOINCREMENT, node_id TEXT NOT NULL, node_name TEXT NOT NULL, kind TEXT NOT NULL, message TEXT NOT NULL, active INTEGER NOT NULL, at INTEGER NOT NULL, delivered INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0);
CREATE TABLE unlocks(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, service TEXT NOT NULL, status TEXT NOT NULL, region TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', tested_at INTEGER NOT NULL, PRIMARY KEY(node_id,service));
CREATE INDEX unlocks_age ON unlocks(tested_at);
CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
PRAGMA user_version=6;
COMMIT;`

func (s *Store) Close() error {
	err := s.db.Close()
	if s.read != nil {
		if rerr := s.read.Close(); err == nil {
			err = rerr
		}
	}
	return err
}

func encode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
func (s *Store) Nodes(ctx context.Context, now time.Time) ([]model.NodeView, error) {
	plan, err := s.ProbeTargets(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, "SELECT config,last_seen,metrics FROM nodes")
	if err != nil {
		return nil, err
	}
	views := []model.NodeView{}
	for rows.Next() {
		var v model.NodeView
		var config string
		var metrics sql.NullString
		if err = rows.Scan(&config, &v.LastSeen, &metrics); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(config), &v.Node); err != nil {
			rows.Close()
			return nil, err
		}
		if metrics.Valid {
			if err = json.Unmarshal([]byte(metrics.String), &v.Metrics); err != nil {
				rows.Close()
				return nil, err
			}
			if v.Metrics != nil {
				v.IPFamilies = v.Metrics.IPFamilies
			}
		}
		v.Online = v.LastSeen > 0 && now.Unix()-v.LastSeen < int64(model.OfflineAfter.Seconds())
		v.ProbeTargets = plan[v.ID]
		start, end := model.Cycle(now, model.BillingAnchor(v.BillingAt, now))
		v.CycleStart = start.Unix()
		v.CycleEnd = end.Unix()
		views = append(views, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range views {
		v := &views[i]
		var in, out int64
		if err = s.queryRow(ctx, "SELECT coalesce(sum(incoming),0),coalesce(sum(outgoing),0) FROM traffic WHERE node_id=? AND day>=? AND day<?", v.ID, v.CycleStart, v.CycleEnd).Scan(&in, &out); err != nil {
			return nil, err
		}
		// 日桶只能整天算：锚点不在 00:00 时周期首日那一天的桶整体早于 cycle_start，被上面
		// 这条查询整段丢掉，用小时桶把锚点之后的那一段补回来（见 cycleStartSlice）。
		edgeIn, edgeOut, err := s.cycleStartSlice(ctx, v.ID, v.CycleStart, v.CycleEnd)
		if err != nil {
			return nil, err
		}
		v.TrafficUsed = model.Traffic(in+edgeIn, out+edgeOut, v.TrafficMode)
		if v.AdjustmentCycle == v.CycleStart {
			v.TrafficUsed = max(0, v.TrafficUsed+v.TrafficAdjustment)
		}
	}
	if err := s.attachWindow(ctx, views, now); err != nil {
		return nil, err
	}
	// 解锁结论与延迟/降速一样，是「这台机器现在是什么样」的一部分：一次按节点分组的
	// 查询把整页都算完（首页每秒一轮，逐台查会把 1 核机的时间叠回去）。
	unlocks, err := s.unlockSummaries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range views {
		views[i].Unlock = unlocks[views[i].ID]
	}
	// 降速状态在这里统一判定（不放在 attachWindow 里）：按周期额度降速的线路即使一个
	// 小时桶都还没有，只要本周期用量已经顶到额度就是已降速。
	for i := range views {
		views[i].Throttled = views[i].ThrottleActive()
	}
	return views, nil
}

// cycleStartSlice 返回周期首日里真正属于本周期的那一段流量。
//
// traffic 表按 UTC 整天存桶，Nodes 的 day>=cycle_start 只能整天算，于是计费锚点不在
// 00:00 的节点（腾讯轻量 09-23 12:36、BWH DC9 09-15 15:17 都是）在周期一开始就出问题：
// 首日那一天的桶整体早于 cycle_start，被整段丢掉——面板数字比商家自己的计费口径少了
// 一整段（实测腾讯 40.6 MB / 3.4%），而且数字其实在锚点日 00:00 就归零，与卡片上写的
// 锚点时刻对不上。这里用同一份网卡增量落的整点桶（traffic_hour）补回 [cycle_start, 当天 24:00)。
//
// 锚点不在整点时，含锚点的那一个小时横跨边界：宁可少算它（月周期约 0.03%），也不能
// 多算——按周期额度降速的线路会因此提前被判定为用满。小时桶只留 30 天，切不到就退回
// 原样（不补），任何情况下都不会多算。
func (s *Store) cycleStartSlice(ctx context.Context, id string, start, end int64) (int64, int64, error) {
	if start%86400 == 0 {
		return 0, 0, nil // 锚点在整日边界：日桶已经精确覆盖，别动
	}
	from := start
	if from%3600 != 0 {
		from = from/3600*3600 + 3600
	}
	to := min(end, start/86400*86400+86400)
	if from >= to {
		return 0, 0, nil
	}
	var in, out int64
	if err := s.queryRow(ctx, "SELECT coalesce(sum(incoming),0),coalesce(sum(outgoing),0) FROM traffic_hour WHERE node_id=? AND hour>=? AND hour<?", id, from, to).Scan(&in, &out); err != nil {
		return 0, 0, err
	}
	return in, out, nil
}

// attachWindow 给每台节点算「降速窗口内的平均带宽」。
//
// 数据来自 traffic_hour（Report 每次上报把网卡计数增量累加到整点桶），所以窗口任意、
// 且代价与窗口无关：一次按节点分组的查询就把所有节点都算完（首页每秒一轮 /nodes，
// 逐台查会把 1 核机的时间又叠回去）。窗口起点向下取整到整点：分子覆盖的桶区间与
// 分母 (now-起点) 完全对应；而 now 与窗口起点的分钟相位相同，开头多算的那点不足
// 一小时正好补上当前这个没走完的桶。
//
// 覆盖不足窗口（新接入、Agent 停过）时按真实覆盖秒数取平均，并把 CoveredHours 报给
// 面板，由它写「统计中」——不假装有一整个窗口的数据。
func (s *Store) attachWindow(ctx context.Context, views []model.NodeView, now time.Time) error {
	if len(views) == 0 {
		return nil
	}
	byWindow := map[int][]string{}
	for i := range views {
		hours := views[i].Node.WindowHours()
		views[i].WindowHours = hours
		byWindow[hours] = append(byWindow[hours], views[i].ID)
	}
	for hours, ids := range byWindow {
		start := (now.Unix() - int64(hours)*3600) / 3600 * 3600
		span := now.Unix() - start
		rows, err := s.query(ctx, "SELECT node_id,coalesce(sum(incoming),0),coalesce(sum(outgoing),0),min(hour) FROM traffic_hour WHERE hour>=? GROUP BY node_id", start)
		if err != nil {
			return err
		}
		type window struct{ in, out, first int64 }
		buckets := map[string]window{}
		for rows.Next() {
			var id string
			var w window
			if err = rows.Scan(&id, &w.in, &w.out, &w.first); err != nil {
				rows.Close()
				return err
			}
			buckets[id] = w
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		wanted := map[string]bool{}
		for _, id := range ids {
			wanted[id] = true
		}
		for i := range views {
			v := &views[i]
			b, ok := buckets[v.ID]
			if !wanted[v.ID] || !ok || span <= 0 {
				continue
			}
			covered := min(span, now.Unix()-b.first)
			if covered <= 0 {
				continue
			}
			v.AvgBits = float64(model.Traffic(b.in, b.out, v.TrafficMode)) * 8 / float64(covered)
			v.CoveredHours = int((covered + 3599) / 3600)
			if covered >= span {
				v.CoveredHours = hours
			}
		}
	}
	return nil
}

// SaveNode creates or updates a node and returns the stored id plus, on
// creation, the one-time Agent token.
func (s *Store) SaveNode(ctx context.Context, n model.Node, create bool) (string, string, error) {
	if err := n.Validate(); err != nil {
		return "", "", err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	if create {
		n.TrafficAdjustment = 0
		n.AdjustmentCycle = 0
		n.ID = model.Secret()[:16]
		n.CreatedAt = time.Now().Unix()
		token := model.Secret()
		_, err := s.db.ExecContext(ctx, "INSERT INTO nodes(id,config,token_hash) VALUES(?,?,?)", n.ID, encode(n), model.Hash(token))
		return n.ID, token, err
	}
	var old string
	if err := s.queryRow(ctx, "SELECT config FROM nodes WHERE id=?", n.ID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	} else if err != nil {
		return "", "", err
	}
	var prior model.Node
	if err := json.Unmarshal([]byte(old), &prior); err != nil {
		return "", "", err
	}
	n.CreatedAt = prior.CreatedAt
	n.TrafficAdjustment = prior.TrafficAdjustment
	n.AdjustmentCycle = prior.AdjustmentCycle
	if n.BillingAt != prior.BillingAt || n.TrafficMode != prior.TrafficMode {
		n.TrafficAdjustment = 0
		n.AdjustmentCycle = 0
	}
	_, err := s.db.ExecContext(ctx, "UPDATE nodes SET config=? WHERE id=?", encode(n), n.ID)
	return n.ID, "", err
}
func (s *Store) DeleteNode(ctx context.Context, id string) error { return s.delete(ctx, "nodes", id) }

// DeleteTask removes a task and, for an unlock task, the verdicts it produced:
// they are derived state, and leaving them behind would show a machine's unlock
// panel for a check nothing runs any more.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	t, err := s.taskConfig(ctx, id)
	if err != nil {
		return err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if t.Kind == model.UnlockKind {
		if err = clearUnlocks(ctx, tx, t.NodeIDs); err != nil {
			return err
		}
	}
	r, err := tx.ExecContext(ctx, "DELETE FROM tasks WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// taskConfig reads a stored task, used by the writers that must know what a
// task was before they change it.
func (s *Store) taskConfig(ctx context.Context, id string) (model.Task, error) {
	var config string
	err := s.queryRow(ctx, "SELECT config FROM tasks WHERE id=?", id).Scan(&config)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, ErrNotFound
	}
	if err != nil {
		return model.Task{}, err
	}
	var t model.Task
	if err = json.Unmarshal([]byte(config), &t); err != nil {
		return model.Task{}, err
	}
	return t, nil
}

// clearUnlocks drops the stored verdicts of the named nodes.
func clearUnlocks(ctx context.Context, tx *sql.Tx, nodeIDs []string) error {
	for _, id := range nodeIDs {
		if _, err := tx.ExecContext(ctx, "DELETE FROM unlocks WHERE node_id=?", id); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) delete(ctx context.Context, table, id string) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	r, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) Rotate(ctx context.Context, id string) (string, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	token := model.Secret()
	r, err := s.db.ExecContext(ctx, "UPDATE nodes SET token_hash=? WHERE id=?", model.Hash(token), id)
	if err != nil {
		return "", err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return "", ErrNotFound
	}
	return token, nil
}
func (s *Store) Authenticate(ctx context.Context, token string) (string, error) {
	if len(token) != 64 {
		return "", ErrNotFound
	}
	var id string
	err := s.queryRow(ctx, "SELECT id FROM nodes WHERE token_hash=?", model.Hash(token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
func (s *Store) Report(ctx context.Context, id string, m model.Metrics, now time.Time) error {
	if err := m.Validate(); err != nil {
		return err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous sql.NullString
	var seen int64
	if err = tx.QueryRowContext(ctx, "SELECT metrics,last_seen FROM nodes WHERE id=?", id).Scan(&previous, &seen); err != nil {
		return err
	}
	if now.Unix()-seen < 3 {
		return ErrConflict
	}
	var in, out int64
	if previous.Valid {
		var p model.Metrics
		if err = json.Unmarshal([]byte(previous.String), &p); err != nil {
			return err
		}
		if m.CounterID == p.CounterID && m.Uptime >= p.Uptime {
			if m.NetIn >= p.NetIn {
				in = int64(m.NetIn - p.NetIn)
			}
			if m.NetOut >= p.NetOut {
				out = int64(m.NetOut - p.NetOut)
			}
		}
	}
	payload := encode(m)
	if _, err = tx.ExecContext(ctx, "UPDATE nodes SET metrics=?,last_seen=? WHERE id=?", payload, now.Unix(), id); err != nil {
		return err
	}
	// One persisted sample per minute; current state is refreshed on each report.
	at := now.Unix() / 60 * 60
	if _, err = tx.ExecContext(ctx, "INSERT INTO samples VALUES(?,?,?) ON CONFLICT(node_id,at) DO UPDATE SET payload=excluded.payload", id, at, payload); err != nil {
		return err
	}
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).Unix()
	if _, err = tx.ExecContext(ctx, "INSERT INTO traffic VALUES(?,?,?,?) ON CONFLICT(node_id,day) DO UPDATE SET incoming=incoming+excluded.incoming,outgoing=outgoing+excluded.outgoing", id, day, in, out); err != nil {
		return err
	}
	// 同一份增量再落一个整点桶：天桶服务「本周期用量」，小时桶服务「窗口内平均带宽」
	// （达量降速）。天桶切不出 24 小时以外的窗口，也不该为了一个展示指标回头去扫
	// samples（每节点每天 1440 行、还要 json_extract 出累计计数）。
	hour := now.Unix() / 3600 * 3600
	if _, err = tx.ExecContext(ctx, "INSERT INTO traffic_hour VALUES(?,?,?,?) ON CONFLICT(node_id,hour) DO UPDATE SET incoming=incoming+excluded.incoming,outgoing=outgoing+excluded.outgoing", id, hour, in, out); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) History(ctx context.Context, id string, hours int, now time.Time) ([]model.Sample, error) {
	// Aggregate into <= 240 buckets. Each bucket returns its last observed sample.
	hours = min(max(hours, 1), 720)
	step := max(60, hours*3600/240)
	rows, err := s.query(ctx, `SELECT at,payload FROM samples WHERE node_id=? AND at IN (SELECT max(at) FROM samples WHERE node_id=? AND at>=? GROUP BY at/?) ORDER BY at`, id, id, now.Add(-time.Duration(hours)*time.Hour).Unix(), step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Sample{}
	skipped := 0
	for rows.Next() {
		var x model.Sample
		var data string
		if err = rows.Scan(&x.At, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &x.Metrics); err != nil {
			// 坏掉的样本行（旧版本格式或半写入）只跳过自己：一台机器的历史不该
			// 因为一行读不出来就整体打不开。
			skipped++
			continue
		}
		out = append(out, x)
	}
	if skipped > 0 {
		slog.Warn("skipped unreadable samples", "node", id, "samples", skipped)
	}
	return out, rows.Err()
}

// ProbeTargets maps node id → the distinct probe entries bound to it (kind / carrier / family /
// region, from every enabled task's leaves). See model.NodeView.ProbeTargets for why the panel
// needs this instead of inferring the cells from stored jobs.
func (s *Store) ProbeTargets(ctx context.Context) (map[string][]model.ProbeTarget, error) {
	tasks, err := s.Tasks(ctx)
	if err != nil {
		return nil, err
	}
	type key struct{ kind, carrier, family, region string }
	plan := map[string][]model.ProbeTarget{}
	seen := map[string]map[key]bool{}
	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		for _, leaf := range t.Leaves() {
			k := key{leaf.Kind, leaf.Carrier, leaf.Family, leaf.Region}
			for _, node := range leaf.NodeIDs {
				if seen[node] == nil {
					seen[node] = map[key]bool{}
				}
				if seen[node][k] {
					continue
				}
				seen[node][k] = true
				plan[node] = append(plan[node], model.ProbeTarget{
					Kind: k.kind, Carrier: k.carrier, Family: k.family, Region: k.region,
				})
			}
		}
	}
	return plan, nil
}

func (s *Store) Tasks(ctx context.Context) ([]model.Task, error) {
	rows, err := s.query(ctx, "SELECT config FROM tasks ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Task{}
	for rows.Next() {
		var data string
		var t model.Task
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveTask creates or updates a probe task and returns the stored id.
func (s *Store) SaveTask(ctx context.Context, t model.Task, create bool) (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if create {
		t.ID = model.Secret()[:16]
		_, err = tx.ExecContext(ctx, "INSERT INTO tasks VALUES(?,?)", t.ID, encode(t))
	} else {
		// 改任务即清历史：解锁结论是从结果派生的，旧结论必须跟着走，否则面板上会
		// 留着一份没有任务在跑的解锁面板。旧配置要先读出来，改 kind 也要清。
		previous, perr := s.taskConfig(ctx, t.ID)
		if perr != nil {
			return "", perr
		}
		var r sql.Result
		r, err = tx.ExecContext(ctx, "UPDATE tasks SET config=? WHERE id=?", encode(t), t.ID)
		if err == nil {
			n, _ := r.RowsAffected()
			if n == 0 {
				return "", ErrNotFound
			}
		}
		if err == nil && (previous.Kind == model.UnlockKind || t.Kind == model.UnlockKind) {
			if err = clearUnlocks(ctx, tx, append(append([]string{}, previous.NodeIDs...), t.NodeIDs...)); err != nil {
				return "", err
			}
		}
	}
	if err != nil {
		return "", err
	}
	// Editing invalidates in-flight leases and old route classifications.
	if _, err = tx.ExecContext(ctx, "DELETE FROM jobs WHERE task_id=?", t.ID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM bindings WHERE task_id=?", t.ID); err != nil {
		return "", err
	}
	for _, id := range t.NodeIDs {
		var exists int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE id=?", id).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return "", fmt.Errorf("%w: bound node does not exist", model.ErrInvalid)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO bindings(task_id,node_id) VALUES(?,?)", t.ID, id); err != nil {
			return "", err
		}
	}
	return t.ID, tx.Commit()
}
func (s *Store) Lease(ctx context.Context, node string, now time.Time) ([]model.Job, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Address-family capability comes from the Agent's own report. Unknown
	// capability (older Agent or first report not arrived yet) keeps the old
	// behaviour so upgrades do not silently disable every probe.
	var metricsJSON sql.NullString
	var capabilities model.Metrics
	if err = tx.QueryRowContext(ctx, "SELECT metrics FROM nodes WHERE id=?", node).Scan(&metricsJSON); err != nil {
		return nil, err
	}
	if metricsJSON.Valid {
		if err = json.Unmarshal([]byte(metricsJSON.String), &capabilities); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT t.config,b.due FROM bindings b JOIN tasks t ON t.id=b.task_id WHERE b.node_id=? ORDER BY t.id", node)
	if err != nil {
		return nil, err
	}
	candidates := []model.Task{}
	requested := map[string]int64{}
	for rows.Next() {
		var data string
		var t model.Task
		var requestAt int64
		if err = rows.Scan(&data, &requestAt); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &t); err != nil {
			rows.Close()
			return nil, err
		}
		requested[t.ID] = requestAt
		if !t.Enabled {
			continue
		}
		for _, leaf := range t.Leaves() {
			if leaf.Kind == model.UnlockKind {
				// 解锁检测只有报了这个能力的 Agent 才领得到：老 Agent 会把它整轮
				// 报成 failed，面板上就成了一片没来由的红。未知能力在这里是「不能」。
				if capabilities.AllowsUnlock() {
					candidates = append(candidates, leaf)
				}
				continue
			}
			if capabilities.AllowsFamily(leaf.Family) {
				candidates = append(candidates, leaf)
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// At most one route and one TCP lease at once per node, including other agents using its token.
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE node_id=? AND finished IS NULL AND issued>?", node, now.Add(-90*time.Second).Unix()).Scan(&active); err != nil {
		return nil, err
	}
	if active > 0 {
		return []model.Job{}, nil
	}
	// Read scheduling history once. Retention exceeds the maximum task interval.
	last := map[string]int64{}
	history, err := tx.QueryContext(ctx, "SELECT task,max(issued) FROM jobs WHERE node_id=? GROUP BY task", node)
	if err != nil {
		return nil, err
	}
	for history.Next() {
		var data string
		var at int64
		var leaf model.Task
		if err = history.Scan(&data, &at); err != nil {
			history.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &leaf); err != nil {
			history.Close()
			return nil, err
		}
		if at > last[leaf.ProbeKey()] {
			last[leaf.ProbeKey()] = at
		}
	}
	err = history.Err()
	history.Close()
	if err != nil {
		return nil, err
	}
	// Oldest due first prevents starvation when many targets share a node.
	due := func(t model.Task) int64 {
		if requested[t.ID] > last[t.ProbeKey()] {
			return requested[t.ID]
		}
		if at, ok := last[t.ProbeKey()]; ok {
			return at + int64(t.Interval)
		}
		return 0
	}
	sort.SliceStable(candidates, func(i, j int) bool { return due(candidates[i]) < due(candidates[j]) })
	out := []model.Job{}
	kinds := map[string]bool{}
	for _, t := range candidates {
		if kinds[t.Kind] || due(t) > now.Unix() {
			continue
		}
		kinds[t.Kind] = true
		j := model.Job{ID: model.Secret()[:24], Task: t}
		if _, err = tx.ExecContext(ctx, "INSERT INTO jobs(id,task_id,node_id,task,issued,probe_key) VALUES(?,?,?,?,?,?)", j.ID, t.ID, node, encode(t), now.Unix(), t.ProbeKey()); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, tx.Commit()
}
func (s *Store) Finish(ctx context.Context, node string, r model.Result, now time.Time) error {
	if err := r.Validate(); err != nil {
		return err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 上报窗口按任务类型分岔：一个延迟 / 回程探测几秒就回来，超过 90 秒的结果多半是
	// 这台机器上另一个进程在冒充；而解锁检测一轮本来就是几分钟（20 多项 HTTPS），
	// 用同一个窗口会把每一轮都判成过期。
	var task model.Task
	var issued int64
	var taskJSON sql.NullString
	if err = tx.QueryRowContext(ctx, "SELECT issued,task FROM jobs WHERE id=? AND node_id=? AND finished IS NULL", r.JobID, node).Scan(&issued, &taskJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if taskJSON.Valid {
		if uerr := json.Unmarshal([]byte(taskJSON.String), &task); uerr != nil {
			// 任务 JSON 坏掉的行按未知处理：结论丢掉，别让上报失败。
			slog.Warn("unreadable job task", "job", r.JobID)
		}
	}
	window := now.Add(-90 * time.Second).Unix()
	if task.Kind == model.UnlockKind {
		window = now.Add(-unlockFinishWindow).Unix()
	}
	if issued < window {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, "UPDATE jobs SET finished=?,result=? WHERE id=? AND node_id=? AND finished IS NULL", now.Unix(), encode(r), r.JobID, node)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrConflict
	}
	// 解锁结论在同一事务里落到 unlocks：面板读到的永远是整轮结果，不会看到半轮。
	if len(r.Unlocks) > 0 && task.Kind == model.UnlockKind {
		for _, u := range r.Unlocks {
			if _, err = tx.ExecContext(ctx, "INSERT INTO unlocks(node_id,service,status,region,detail,tested_at) VALUES(?,?,?,?,?,?) ON CONFLICT(node_id,service) DO UPDATE SET status=excluded.status,region=excluded.region,detail=excluded.detail,tested_at=excluded.tested_at", node, u.Service, u.Status, u.Region, u.Detail, now.Unix()); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Unlocks reads one node's stored verdicts as the panel summary, nil when the
// machine has never run the check.
func (s *Store) Unlocks(ctx context.Context, node string) (*model.UnlockSummary, error) {
	rows, err := s.query(ctx, "SELECT service,status,region,detail,tested_at FROM unlocks WHERE node_id=?", node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []model.UnlockResult{}
	testedAt := int64(0)
	for rows.Next() {
		var u model.UnlockResult
		var at int64
		if err = rows.Scan(&u.Service, &u.Status, &u.Region, &u.Detail, &at); err != nil {
			return nil, err
		}
		results = append(results, u)
		testedAt = max(testedAt, at)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// 未知服务（比本 Server 新的 Agent）只跳过自己：其余结论照旧显示。目录里删掉的
	// 项同理，不再出现在面板上。
	known := results[:0]
	for _, u := range results {
		if model.KnownUnlockService(u.Service) {
			known = append(known, u)
		}
	}
	return model.SummarizeUnlocks(orderUnlocks(known), testedAt), nil
}

// unlockSummaries reads every node's verdicts in one grouped query for /nodes.
func (s *Store) unlockSummaries(ctx context.Context) (map[string]*model.UnlockSummary, error) {
	rows, err := s.query(ctx, "SELECT node_id,service,status,region,detail,tested_at FROM unlocks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[string][]model.UnlockResult{}
	tested := map[string]int64{}
	for rows.Next() {
		var id string
		var u model.UnlockResult
		var at int64
		if err = rows.Scan(&id, &u.Service, &u.Status, &u.Region, &u.Detail, &at); err != nil {
			return nil, err
		}
		if !model.KnownUnlockService(u.Service) {
			continue
		}
		grouped[id] = append(grouped[id], u)
		tested[id] = max(tested[id], at)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]*model.UnlockSummary, len(grouped))
	for id, services := range grouped {
		out[id] = model.SummarizeUnlocks(orderUnlocks(services), tested[id])
	}
	return out, nil
}

// orderUnlocks sorts verdicts into catalogue order so the panel lists services
// the same way on every node.
func orderUnlocks(services []model.UnlockResult) []model.UnlockResult {
	rank := map[string]int{}
	for i, id := range model.UnlockServiceIDs() {
		rank[id] = i
	}
	sort.SliceStable(services, func(i, j int) bool { return rank[services[i].Service] < rank[services[j].Service] })
	return services
}
func (s *Store) Observations(ctx context.Context, node string, now time.Time) ([]model.Observation, error) {
	// 读的是物化表 latest：每台节点、每个分组（任务 × 种类 × 协议族 × 目标/端口 ×
	// 地区 × 运营商）只留最新一行，由 jobs 上的触发器维护。旧实现在窗口内对
	// 上万行逐行 json_extract 再排序（线上 65ms/节点、首页扇出 465ms），这里
	// 只剩几十行（实测 0.03ms/节点）。
	// 窗口仍然生效：窗口外的最后一条不显示，与从前一致。
	// json_valid 兜底：任务 JSON 坏掉的行写不进 latest，这一组继续显示更早那条
	// （旧查询同样用 json_valid 过滤），下面的 Unmarshal 再挡一层。
	rows, err := s.query(ctx, `SELECT task,issued,finished,result FROM latest WHERE node_id=? AND issued>=? ORDER BY probe_key`, node, now.Add(-ProbeWindow).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Observation{}
	skipped := 0
	for rows.Next() {
		var o model.Observation
		var task string
		var finished sql.NullInt64
		var result sql.NullString
		if err = rows.Scan(&task, &o.At, &finished, &result); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(task), &o.Task); err != nil {
			// 任务 JSON 读不出来就没法把这条结果摆到面板上：跳过它，别让整个节点 500。
			skipped++
			continue
		}
		if result.Valid {
			if err = json.Unmarshal([]byte(result.String), &o.Result); err != nil {
				// 任务还在、结果坏了：这一条按“未知”显示，比整页打不开有用。
				skipped++
				o.Result = model.Result{Status: "unknown"}
			} else {
				o.At = finished.Int64
				// 线路标签是跳表的解释，不是实测数据本身：读的时候按当前规则重算，
				// 判定规则修正后历史记录跟着更新，不必等下一次探测。
				if o.Task.Kind == "route" {
					o.Result.Route = model.RouteLabel(o.Result.Hops)
					if o.Result.Status != "ok" {
						o.Result.Route = "未知"
					}
				}
			}
		} else {
			o.Result.Status = "pending"
			if now.Unix()-o.At > 90 {
				o.Result.Status = "timeout"
			}
		}
		o.Stale = now.Unix()-o.At > int64(o.Task.Interval*2+90)
		out = append(out, o)
	}
	if skipped > 0 {
		slog.Warn("skipped unreadable observations", "node", node, "rows", skipped)
	}
	return out, rows.Err()
}

// 保留期：Clean 按这几个时长删旧行。JobsRetention 是公开的，因为面板的延迟色带时间窗
// 上限就是它——窗口再大也取不到更早的探测（model.MaxLatencyWindowMinutes 与它一致，
// storage 的测试会把两者钉在一起）。
const (
	// unlockFinishWindow is how late an unlock round may report. One round is
	// minutes, not seconds (see probe.unlockBudget).
	unlockFinishWindow = 15 * time.Minute
	samplesRetention   = 30 * 24 * time.Hour
	trafficRetention   = 400 * 24 * time.Hour
	// 小时桶服务窗口内平均带宽（窗口上限 168 小时），也用来补周期首日那一段
	// （锚点不在 00:00 时，日桶整天算会把首日整段丢掉，见 cycleStartSlice）——
	// 月周期最长 31 天，留 45 天保证周期第一天的小时桶还在。
	trafficHourRetention = 45 * 24 * time.Hour
	JobsRetention        = 7 * 24 * time.Hour
	eventsRetention      = 90 * 24 * time.Hour
)

func (s *Store) Clean(ctx context.Context, now time.Time) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []struct {
		query string
		age   time.Duration
	}{{"DELETE FROM samples WHERE at<?", samplesRetention}, {"DELETE FROM traffic WHERE day<?", trafficRetention}, {"DELETE FROM traffic_hour WHERE hour<?", trafficHourRetention}, {"DELETE FROM jobs WHERE issued<?", JobsRetention}, {"DELETE FROM events WHERE at<?", eventsRetention}} {
		if _, err = tx.ExecContext(ctx, q.query, now.Add(-q.age).Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.queryRow(ctx, "SELECT value FROM settings WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO settings VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

// Transition persists debounce and edge state atomically with its durable notification event.
func (s *Store) Transition(ctx context.Context, n model.Node, kind, message string, condition bool, hold int, now time.Time) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var since int64
	var active bool
	err = tx.QueryRowContext(ctx, "SELECT since,active FROM alert_state WHERE node_id=? AND kind=?", n.ID, kind).Scan(&since, &active)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	previous := active
	if condition {
		if since == 0 {
			since = now.Unix()
		}
		if now.Unix()-since >= int64(hold) {
			active = true
		}
	} else {
		since = 0
		active = false
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO alert_state VALUES(?,?,?,?) ON CONFLICT(node_id,kind) DO UPDATE SET since=excluded.since,active=excluded.active", n.ID, kind, since, active); err != nil {
		return err
	}
	repeat := false
	if active && previous && n.Alerts.RepeatSeconds > 0 {
		var last int64
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(at),0) FROM events WHERE node_id=? AND kind=?", n.ID, kind).Scan(&last); err != nil {
			return err
		}
		repeat = now.Unix()-last >= int64(n.Alerts.RepeatSeconds)
	}
	if active != previous || repeat {
		if !active {
			message = "条件已解除或提醒已关闭"
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO events(node_id,node_name,kind,message,active,at) VALUES(?,?,?,?,?,?)", n.ID, n.Name, kind, message, active, now.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Events(ctx context.Context, pending bool, now time.Time) ([]model.Event, error) {
	where := ""
	order := "DESC"
	if pending {
		where = "WHERE delivered=0 AND next_attempt<=? AND attempts<10"
		order = "ASC"
	}
	args := []any{}
	if pending {
		args = append(args, now.Unix())
	}
	rows, err := s.query(ctx, "SELECT id,node_id,node_name,kind,message,active,at,delivered,attempts,last_error FROM events "+where+" ORDER BY id "+order+" LIMIT 100", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var e model.Event
		if err = rows.Scan(&e.ID, &e.NodeID, &e.NodeName, &e.Kind, &e.Message, &e.Active, &e.At, &e.Delivered, &e.Attempts, &e.LastError); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) Delivery(ctx context.Context, e model.Event, ok bool, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE events SET delivered=?,last_error=?,attempts=attempts+1,next_attempt=? WHERE id=?", ok, e.LastError, now.Add(time.Duration(min(3600, 30*(1<<min(e.Attempts, 7))))*time.Second).Unix(), e.ID)
	return err
}

// IsPublic is a keyed lookup; detail requests must not reload all nodes and billing ledgers.
func (s *Store) IsPublic(ctx context.Context, id string) (bool, error) {
	var public bool
	err := s.queryRow(ctx, "SELECT json_extract(config,'$.public') FROM nodes WHERE id=?", id).Scan(&public)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return public, err
}
