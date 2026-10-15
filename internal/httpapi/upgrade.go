package httpapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/agentdist"
	"github.com/leeska/TZA-Probe/internal/model"
)

// 一键升级 Agent：后台把节点排进队列，Agent 下一次上报（约 5 秒一次）时在回复里拿到新版本的
// 下载路径与 sha256，自己下载、校验、替换并重启（见 internal/agent/update.go）。
//
// 队列只放在内存里：它是「下一次上报时顺手告诉它」的一次性指令，Server 重启丢了重新点一下
// 即可，不值得为它加一张表。离线的节点照样排队，回来时第一次上报就会升级；排了 24 小时还没
// 回来的丢掉。只有带 self_update 能力（新安装脚本装的、带 --self-update 启动）的 Agent 会被
// 下发；更早的 Agent 要在机器上手工执行一次安装命令，之后就都能一键升级了。

// Upgrade states shown in the panel.
const (
	upgradeQueued      = "queued"      // 等下一次上报
	upgradeSent        = "sent"        // 已下发，等它以新版本重新上报
	upgradeFailed      = "failed"      // 下发后迟迟没换上新版本，或服务端没有它的架构
	upgradeManual      = "manual"      // Agent 太旧，不会自升级
	upgradeCurrent     = "current"     // 已是面板版本
	upgradeUnavailable = "unavailable" // 服务端没有这个架构的安装包
	upgradeNever       = "never"       // 从未接入
)

const (
	upgradeQueueTTL = 24 * time.Hour
	// 下载十几 MB、校验、重启，正常一两分钟内就会以新版本上报。
	upgradeSentTTL = 5 * time.Minute
)

type upgradeItem struct {
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`
	RequestedAt int64  `json:"requested_at"`
	SentAt      int64  `json:"sent_at,omitempty"`
}

type upgradeQueue struct {
	mu    sync.Mutex
	items map[string]*upgradeItem
}

// expire drops stale queue entries and turns a delivery that never took into
// a failure. Callers hold mu.
func (q *upgradeQueue) expire(now time.Time) {
	for id, it := range q.items {
		switch {
		case it.State == upgradeQueued && now.Unix()-it.RequestedAt > int64(upgradeQueueTTL/time.Second):
			delete(q.items, id)
		case it.State == upgradeSent && now.Unix()-it.SentAt > int64(upgradeSentTTL/time.Second):
			it.State, it.Error = upgradeFailed, "已下发升级，但 Agent 一直没以新版本上报；请查看那台机器的 Agent 日志（journalctl -u tza-agent）"
		}
	}
}

// upgradeReply is what the report handler sends back: an upgrade instruction
// when this node is queued and its Agent can take one, nil otherwise. A report
// carrying the panel's version clears the node's entry: the upgrade is done.
func (a *API) upgradeReply(id string, m model.Metrics, now time.Time) *model.AgentUpgrade {
	q := &a.upgrades
	q.mu.Lock()
	defer q.mu.Unlock()
	it := q.items[id]
	if it == nil {
		return nil
	}
	if m.Version == model.Version {
		delete(q.items, id)
		return nil
	}
	if it.State != upgradeQueued {
		return nil
	}
	if !m.AllowsSelfUpdate() {
		it.State = upgradeManual
		return nil
	}
	var sum string
	var err error
	if a.Agents != nil {
		sum, err = a.Agents.Sum(m.Arch)
	}
	if a.Agents == nil || err != nil {
		it.State, it.Error = upgradeUnavailable, "服务端没有 "+m.Arch+" 架构的 Agent 安装包"
		return nil
	}
	it.State, it.SentAt = upgradeSent, now.Unix()
	return &model.AgentUpgrade{Version: model.Version, Path: "/dl/" + agentdist.Name(m.Arch), SHA256: sum}
}

func (a *API) upgradeRoutes(route func(string, http.HandlerFunc)) {
	// 当前队列：面板据此在 Agent 版本旁标「排队中 / 升级中 / 未完成」。
	route("GET /api/v1/admin/agents/upgrade", func(w http.ResponseWriter, r *http.Request) {
		q := &a.upgrades
		q.mu.Lock()
		q.expire(time.Now())
		items := map[string]upgradeItem{}
		for id, it := range q.items {
			items[id] = *it
		}
		q.mu.Unlock()
		var archs []string
		if a.Agents != nil {
			archs = a.Agents.Available()
		}
		write(w, 200, map[string]any{"version": model.Version, "archs": archs, "items": items})
	})
	// 把勾选的节点排进升级队列；每台给出结果（排队 / 已是新版 / 需手工升级一次 …）。
	route("POST /api/v1/admin/agents/upgrade", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			IDs []string `json:"ids"`
		}
		if !decode(w, r, &v) {
			return
		}
		if len(v.IDs) == 0 || len(v.IDs) > 500 {
			write(w, 400, map[string]string{"error": "节点数量须在 1—500 之间"})
			return
		}
		nodes, err := a.Store.Nodes(r.Context(), time.Now())
		if err != nil {
			fail(w, err)
			return
		}
		byID := map[string]model.NodeView{}
		for _, n := range nodes {
			byID[n.ID] = n
		}
		now := time.Now()
		results := map[string]string{}
		q := &a.upgrades
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.items == nil {
			q.items = map[string]*upgradeItem{}
		}
		for _, id := range v.IDs {
			n, ok := byID[id]
			if !ok {
				write(w, 404, map[string]string{"error": "节点不存在：" + id})
				return
			}
			m := n.Metrics
			state := upgradeQueued
			switch {
			case m == nil:
				state = upgradeNever
			case m.Version == model.Version:
				state = upgradeCurrent
			case !m.AllowsSelfUpdate():
				state = upgradeManual
			case a.Agents == nil:
				state = upgradeUnavailable
			default:
				if _, err := a.Agents.Sum(m.Arch); err != nil {
					state = upgradeUnavailable
				}
			}
			results[id] = state
			if state == upgradeQueued {
				q.items[id] = &upgradeItem{State: upgradeQueued, RequestedAt: now.Unix()}
			} else {
				delete(q.items, id)
			}
		}
		write(w, 200, map[string]any{"version": model.Version, "results": results})
	})
}
