package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

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
			if now.Unix()-o.At > int64(finishWindow(o.Task.Kind).Seconds()) {
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
