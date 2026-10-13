package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

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
		return model.Conflict("上报过于频繁")
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
	// 增量属于上一次上报到这一次之间：按时长比例摊进这段时间经过的每个桶。平时两次上报只隔
	// 几秒，就是当前这一个桶；Agent 停了几天、主机没重启时，以前整段增量都记进重连的那一小时，
	// 窗口均值瞬间暴涨（误报达量降速），跨了计费周期还会把上一周期的流量记进新周期。
	from := now.Unix()
	if seen > 0 {
		from = seen
	}
	for _, p := range spread(from, now.Unix(), 86400, in, out) {
		if _, err = tx.ExecContext(ctx, "INSERT INTO traffic VALUES(?,?,?,?) ON CONFLICT(node_id,day) DO UPDATE SET incoming=incoming+excluded.incoming,outgoing=outgoing+excluded.outgoing", id, p.at, p.in, p.out); err != nil {
			return err
		}
	}
	// 同一份增量再落整点桶：天桶服务「本周期用量」，小时桶服务「窗口内平均带宽」
	// （达量降速）。天桶切不出 24 小时以外的窗口，也不该为了一个展示指标回头去扫
	// samples（每节点每天 1440 行、还要 json_extract 出累计计数）。
	for _, p := range spread(from, now.Unix(), 3600, in, out) {
		if _, err = tx.ExecContext(ctx, "INSERT INTO traffic_hour VALUES(?,?,?,?) ON CONFLICT(node_id,hour) DO UPDATE SET incoming=incoming+excluded.incoming,outgoing=outgoing+excluded.outgoing", id, p.at, p.in, p.out); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type piece struct{ at, in, out int64 }

// spread splits a counter delta observed over [from, to] into size-aligned
// buckets, in proportion to the seconds of the interval each bucket covers. The
// pieces always add up to exactly in / out (the last one takes the rounding),
// and a zero-length interval puts everything into the bucket holding to.
func spread(from, to, size, in, out int64) []piece {
	from = max(from, to-int64(maxSpread.Seconds()))
	if to <= from || (in == 0 && out == 0) {
		return []piece{{to / size * size, in, out}}
	}
	total := float64(to - from)
	var pieces []piece
	var givenIn, givenOut, covered int64
	for b := from / size * size; b < to; b += size {
		covered += min(b+size, to) - max(b, from)
		share := float64(covered) / total
		cumIn, cumOut := int64(float64(in)*share), int64(float64(out)*share)
		if b+size >= to {
			cumIn, cumOut = in, out
		}
		pieces = append(pieces, piece{b, cumIn - givenIn, cumOut - givenOut})
		givenIn, givenOut = cumIn, cumOut
	}
	return pieces
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
	candidates := []model.Probe{}
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
				if !capabilities.AllowsUnlock() {
					continue
				}
				// 协议族留空时 Agent 固定走 IPv4（双栈机两条出口的结论不一样）；
				// 纯 IPv6 机器没有 IPv4 出口，那样每一项都会 failed，这里改派 IPv6。
				if leaf.Family == "" && !capabilities.AllowsFamily("ipv4") && capabilities.AllowsFamily("ipv6") {
					leaf.Family = "ipv6"
				}
				family := leaf.Family
				if family == "" {
					family = "ipv4"
				}
				if capabilities.AllowsFamily(family) {
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
	// A kind with a job still in flight gets nothing new, including for other
	// agents using this node's token. An Agent that runs its jobs side by side is
	// judged per kind, so a minutes-long unlock round no longer holds up the
	// latency probes; an older Agent waits for its whole lease, so any open job
	// blocks it. An unlock job stays in flight for as long as it may still report.
	parallel := capabilities.AllowsParallelJobs()
	busy := map[string]bool{}
	inflight, err := tx.QueryContext(ctx, "SELECT DISTINCT json_extract(task,'$.kind') FROM jobs WHERE node_id=? AND finished IS NULL AND (issued>? OR (json_extract(task,'$.kind')=? AND issued>?))",
		node, now.Add(-90*time.Second).Unix(), model.UnlockKind, now.Add(-unlockFinishWindow).Unix())
	if err != nil {
		return nil, err
	}
	for inflight.Next() {
		var kind sql.NullString
		if err = inflight.Scan(&kind); err != nil {
			inflight.Close()
			return nil, err
		}
		busy[kind.String] = true
	}
	err = inflight.Err()
	inflight.Close()
	if err != nil {
		return nil, err
	}
	if len(busy) > 0 && !parallel {
		return []model.Job{}, nil
	}
	// Read scheduling history once. Retention exceeds the maximum task interval.
	// The materialized latest table already holds the newest issue per probe
	// key; scanning a week of jobs here ran on every poll under the write lock.
	last := map[string]int64{}
	history, err := tx.QueryContext(ctx, "SELECT probe_key,issued FROM latest WHERE node_id=?", node)
	if err != nil {
		return nil, err
	}
	for history.Next() {
		var key string
		var at int64
		if err = history.Scan(&key, &at); err != nil {
			history.Close()
			return nil, err
		}
		last[key] = at
	}
	err = history.Err()
	history.Close()
	if err != nil {
		return nil, err
	}
	// 补测队列：Finish 在「这次回程没测出骨干证据、上一次是好的」时排一条两分钟后的
	// 补测。这里把它读出来当提前到点的 due 用（读完即领，领走就删，见下面的循环）。
	retry := map[string]int64{}
	pending, err := tx.QueryContext(ctx, "SELECT probe_key,at FROM retries WHERE node_id=?", node)
	if err != nil {
		return nil, err
	}
	for pending.Next() {
		var key string
		var at int64
		if err = pending.Scan(&key, &at); err != nil {
			pending.Close()
			return nil, err
		}
		retry[key] = at
	}
	err = pending.Err()
	pending.Close()
	if err != nil {
		return nil, err
	}
	// Oldest due first prevents starvation when many targets share a node.
	due := func(t model.Probe) int64 {
		if requested[t.ID] > last[t.ProbeKey()] {
			return requested[t.ID]
		}
		d := int64(0)
		if at, ok := last[t.ProbeKey()]; ok {
			d = at + int64(t.Interval)
		}
		if at, ok := retry[t.ProbeKey()]; ok && at < d {
			return at
		}
		return d
	}
	sort.SliceStable(candidates, func(i, j int) bool { return due(candidates[i]) < due(candidates[j]) })
	out := []model.Job{}
	issued := map[string]int{}
	for _, t := range candidates {
		if busy[t.Kind] || issued[t.Kind] >= leaseLimit(t.Kind, parallel) || due(t) > now.Unix() {
			continue
		}
		if !parallel && len(out) >= legacyLeaseJobs {
			break
		}
		issued[t.Kind]++
		j := model.Job{ID: jobID(now), Task: t}
		if _, err = tx.ExecContext(ctx, "INSERT INTO jobs(id,task_id,node_id,task,issued,probe_key) VALUES(?,?,?,?,?,?)", j.ID, t.ID, node, encode(t), now.Unix(), t.ProbeKey()); err != nil {
			return nil, err
		}
		if _, ok := retry[t.ProbeKey()]; ok {
			// 领走就等于用掉了：留着它这条探针会因为「补测时间已过」被无限重领。
			if _, err = tx.ExecContext(ctx, "DELETE FROM retries WHERE node_id=? AND probe_key=?", node, t.ProbeKey()); err != nil {
				return nil, err
			}
		}
		out = append(out, j)
	}
	return out, tx.Commit()
}

// jobID is a new job's primary key: eight hex digits of the issue time, then 64
// random bits — 24 characters like before, so nothing that stores or validates
// one changes. The time prefix is for the B-tree: with a fully random id every
// insert and every retention delete landed on a random page of the primary-key
// index, about a millisecond a row once the table no longer fits the cache.
// Time-ordered ids keep both at the right-hand edge and the oldest pages.
func jobID(now time.Time) string {
	return fmt.Sprintf("%08x", uint32(now.Unix())) + model.Secret()[:16]
}

// leaseLimit is how many jobs of one kind a single lease may carry.
func leaseLimit(kind string, parallel bool) int {
	if parallel && kind == "tcp" {
		return tcpLeaseJobs
	}
	return 1
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
	var task model.Probe
	var issued int64
	var taskJSON sql.NullString
	if err = tx.QueryRowContext(ctx, "SELECT issued,task FROM jobs WHERE id=? AND node_id=? AND finished IS NULL", r.JobID, node).Scan(&issued, &taskJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Conflict("任务已失效或结果已上报")
		}
		return err
	}
	if taskJSON.Valid {
		if uerr := json.Unmarshal([]byte(taskJSON.String), &task); uerr != nil {
			// 任务 JSON 坏掉的行按未知处理：结论丢掉，别让上报失败。
			slog.Warn("unreadable job task", "job", r.JobID)
		}
	}
	if issued < now.Add(-finishWindow(task.Kind)).Unix() {
		return model.Conflict("任务已超时，结果不再接收")
	}
	result, err := tx.ExecContext(ctx, "UPDATE jobs SET finished=?,result=? WHERE id=? AND node_id=? AND finished IS NULL", now.Unix(), encode(r), r.JobID, node)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return model.Conflict("结果已上报")
	}
	// 回程「未知」多半是一次抖动：一条没走到目标的 trace 就会把那条探针写成未知，而正常
	// 周期要等一整个 interval（本机 1 小时）才重测，面板上于是挂着整整一轮的未知。所以只要
	// 上一次是好的、这一次没有骨干证据，就排一次两分钟后的补测（Lease 会提前领走它）。
	// 补测再没证据时不再续命——两轮都空就按正常周期走，否则一条长期测不出的目标会每两分钟
	// 烧一次 trace。
	if task.Kind == model.RouteKind && r.Route == model.RouteUnknown {
		var prev string
		perr := tx.QueryRowContext(ctx, "SELECT result FROM jobs WHERE node_id=? AND probe_key=? AND id<>? AND result IS NOT NULL ORDER BY issued DESC LIMIT 1", node, task.ProbeKey(), r.JobID).Scan(&prev)
		if perr != nil && !errors.Is(perr, sql.ErrNoRows) {
			return perr
		}
		var before model.Result
		if prev != "" && json.Unmarshal([]byte(prev), &before) == nil && before.Route != "" && before.Route != model.RouteUnknown {
			if _, err = tx.ExecContext(ctx, "INSERT INTO retries(node_id,probe_key,at) VALUES(?,?,?) ON CONFLICT(node_id,probe_key) DO UPDATE SET at=excluded.at", node, task.ProbeKey(), now.Add(routeRetryDelay).Unix()); err != nil {
				return err
			}
		}
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

// 保留期：Clean 按这几个时长删旧行。JobsRetention 是公开的，因为面板的延迟色带时间窗
// 上限就是它——窗口再大也取不到更早的探测（model.MaxLatencyWindowMinutes 与它一致，
// storage 的测试会把两者钉在一起）。
// finishWindow is how long after it was issued a job may still report (see
// Finish). Anything still open past it will never be accepted, so the panel and
// the run history call it a timeout; before it, the job is simply running.
func finishWindow(kind string) time.Duration {
	if kind == model.UnlockKind {
		return unlockFinishWindow
	}
	return 90 * time.Second
}

// maxSpread bounds how far back a counter delta is spread: past the hourly
// retention the buckets are deleted anyway, and the bound keeps one report from
// writing thousands of rows. A longer gap is spread over its last maxSpread.
const maxSpread = trafficHourRetention

// tcpLeaseJobs is how many latency probes one lease hands an Agent that runs
// jobs side by side. With one per lease and a 10s poll a node managed six
// probes a minute, so a task with a dozen targets at 30s quietly ran every
// two minutes instead. Each probe is three handshakes; eight at once stays far
// from disturbing the measurements.
const tcpLeaseJobs = 8

// legacyLeaseJobs caps a lease for an Agent without CapabilityParallelJobs: it
// runs only the first two jobs it receives and drops the rest.
const legacyLeaseJobs = 2
