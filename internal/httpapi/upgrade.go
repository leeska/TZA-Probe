package httpapi

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/agentdist"
	"github.com/leeska/TZA-Probe/internal/agentsig"
	"github.com/leeska/TZA-Probe/internal/model"
)

// 一键升级 Agent：后台把节点排进队列，Agent 下一次上报（约 5 秒一次）时在回复里拿到新版本的
// 下载路径与 sha256，自己下载、校验、替换并重启（见 internal/agent/update.go）。
//
// 队列只放在内存里：它是「下一次上报时顺手告诉它」的一次性指令，Server 重启丢了重新点一下
// 即可，不值得为它加一张表。离线的节点照样排队，回来时第一次上报就会升级；排了 24 小时还没
// 回来的丢掉。只有带 self_update 能力（新安装脚本装的、带 --self-update 启动且钉了升级公钥）
// 的 Agent 会被下发；更早的 Agent 要在机器上手工执行一次安装命令，之后就都能一键升级了。
//
// 下发的只能是运维者签过名的安装包（见 internal/agentsig）：Server 手里没有签名私钥，
// 没有 .sig 的架构标「未签名」、不下发，Agent 那边也会拒收没签名或比自己旧的版本。

// Upgrade states shown in the panel.
const (
	upgradeQueued      = "queued"      // 等下一次上报
	upgradeSent        = "sent"        // 已下发，等它以新版本重新上报
	upgradeFailed      = "failed"      // 下发后迟迟没换上新版本，或服务端没有它的架构
	upgradeManual      = "manual"      // Agent 太旧，不会自升级
	upgradeCurrent     = "current"     // 已是面板版本
	upgradeUnavailable = "unavailable" // 服务端没有这个架构的安装包
	upgradeUnsigned    = "unsigned"    // 有安装包但没有可用的签名，不能一键升级
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
	Version     string `json:"version,omitempty"` // 下发的版本；以它上报即算升级完成
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

// offer finds the signed release an Agent reporting m could upgrade to. state
// is upgradeQueued when there is one, else the reason there is none.
func (a *API) offer(m model.Metrics) (sig agentsig.Signature, state, reason string) {
	if a.Agents == nil {
		return sig, upgradeUnavailable, "服务端没有 Agent 安装包"
	}
	sig, err := a.Agents.Signature(m.Arch)
	switch {
	case errors.Is(err, agentdist.ErrUnsigned):
		return sig, upgradeUnsigned, "服务端的 " + m.Arch + " 安装包没有可用签名（" + err.Error() + "）；用 tza-server agent-sign 签好再放进 agents 目录"
	case err != nil:
		return sig, upgradeUnavailable, "服务端没有 " + m.Arch + " 架构的 Agent 安装包"
	case !agentsig.Newer(sig.Version, m.Version):
		return sig, upgradeCurrent, ""
	}
	return sig, upgradeQueued, ""
}

// upgradeReply is what the report handler sends back: an upgrade instruction
// when this node is queued and its Agent can take one, nil otherwise. A report
// carrying the offered version clears the node's entry: the upgrade is done.
func (a *API) upgradeReply(id string, m model.Metrics, now time.Time) *model.AgentUpgrade {
	q := &a.upgrades
	q.mu.Lock()
	defer q.mu.Unlock()
	it := q.items[id]
	if it == nil {
		return nil
	}
	if m.Version == model.Version || (it.Version != "" && m.Version == it.Version) {
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
	sig, state, reason := a.offer(m)
	if state == upgradeCurrent {
		delete(q.items, id)
		return nil
	}
	if state != upgradeQueued {
		it.State, it.Error = state, reason
		return nil
	}
	it.State, it.SentAt, it.Version = upgradeSent, now.Unix(), sig.Version
	return &model.AgentUpgrade{Version: sig.Version, Path: "/dl/" + agentdist.Name(m.Arch), SHA256: sig.SHA256, Arch: sig.Arch, KeyID: sig.KeyID, Sig: sig.Sig}
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
		archs, signed := []string{}, []string{}
		if a.Agents != nil {
			archs = a.Agents.Available()
			for _, arch := range archs {
				if _, err := a.Agents.Signature(arch); err == nil {
					signed = append(signed, arch)
				}
			}
		}
		write(w, 200, map[string]any{"version": model.Version, "archs": archs, "signed": signed, "items": items})
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
			default:
				_, state, _ = a.offer(*m)
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
