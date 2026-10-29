package model

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"time"
)

// BlockSpec compares the same literal public IP and listening TCP port from
// explicitly selected mainland and overseas vantage points. Agents receive
// ordinary TCP jobs, so this does not change their wire protocol.
type BlockSpec struct {
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	Family          string   `json:"family"`
	Interval        int      `json:"interval"`
	MainlandNodeIDs []string `json:"mainland_node_ids"`
	OverseasNodeIDs []string `json:"overseas_node_ids"`
}

func (t Task) validateBlock() error {
	b := t.Block
	ip := net.ParseIP(b.Host)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return errors.New("IP 被墙检测的目标须为公网 IPv4 / IPv6 地址，不能填域名")
	}
	addr, _ := netip.ParseAddr(b.Host)
	for _, reserved := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(reserved).Contains(addr.Unmap()) {
			return errors.New("IP 被墙检测不能使用保留、文档或测试网段地址")
		}
	}
	if b.Interval < 60 || b.Interval > 3600 {
		return errors.New("IP 被墙检测间隔须为 60–3600 秒")
	}
	if err := t.Leaves()[0].Validate(); err != nil {
		return err
	}
	if len(b.MainlandNodeIDs) == 0 || len(b.OverseasNodeIDs) == 0 || len(b.MainlandNodeIDs)+len(b.OverseasNodeIDs) > 20 {
		return errors.New("请分别选择至少一个大陆和境外检测节点，合计最多 20 个")
	}
	seen := map[string]bool{}
	for _, id := range append(slices.Clone(b.MainlandNodeIDs), b.OverseasNodeIDs...) {
		if id == "" || seen[id] || !slices.Contains(t.NodeIDs, id) {
			return errors.New("检测节点不能重复、兼任大陆与境外角色，且必须在 node_ids 中绑定")
		}
		seen[id] = true
	}
	if len(seen) != len(t.NodeIDs) {
		return errors.New("IP 被墙检测的每个绑定节点都须指定大陆或境外角色，请通过编辑任务调整")
	}
	return nil
}

type BlockAttempt struct {
	Issued   int64  `json:"issued"`
	Finished int64  `json:"finished"`
	Result   Result `json:"result"`
}

type BlockEvidence struct {
	NodeID   string         `json:"node_id"`
	NodeName string         `json:"node_name"`
	Role     string         `json:"role"`
	State    string         `json:"state"`
	Attempts []BlockAttempt `json:"attempts"`
}

type BlockCheckView struct {
	TaskID   string          `json:"task_id"`
	Name     string          `json:"name"`
	Host     string          `json:"host"`
	Port     int             `json:"port"`
	Family   string          `json:"family"`
	Status   string          `json:"status"`
	Detail   string          `json:"detail"`
	Evidence []BlockEvidence `json:"evidence"`
}

// BlockEvidenceState never treats a missing report, DNS/tool failure or an
// expired lease as a failed connection. Two actual rounds, separated by at
// least 30 seconds, are needed before a failure contributes to a verdict.
func BlockEvidenceState(attempts []BlockAttempt, interval int, now time.Time) string {
	if len(attempts) == 0 {
		return "pending"
	}
	fresh := func(a BlockAttempt) bool {
		return a.Issued <= now.Unix() && now.Unix()-a.Issued <= int64(2*interval+30) && a.Finished > 0 && a.Finished <= now.Unix()
	}
	a := attempts[0]
	if a.Finished == 0 {
		if now.Unix()-a.Issued > 90 {
			return "unavailable"
		}
		return "pending"
	}
	if !fresh(a) {
		return "stale"
	}
	if a.Result.Status == "ok" && a.Result.Received > 0 {
		return "reachable"
	}
	failed := func(a BlockAttempt) bool {
		r := a.Result
		return fresh(a) && (r.Status == "timeout" || r.Status == "failed") && (r.Reason == "connect_failed" || r.Reason == "timeout") && r.Sent == 3 && r.Received == 0
	}
	if !failed(a) {
		return "unavailable"
	}
	if len(attempts) < 2 || !failed(attempts[1]) || a.Issued-attempts[1].Issued < 30 {
		return "unconfirmed"
	}
	return "failed"
}

func (v *BlockCheckView) Evaluate(enabled bool, interval int) {
	v.Status, v.Detail = "unknown", "证据不足：等待在线节点的有效检测；失败须连续两轮，境内外结果须在同一检测周期附近。"
	if !enabled {
		v.Status, v.Detail = "paused", "任务已暂停，历史结果不参与当前判定。"
		return
	}
	cn, cnOK, cnFail, abroad, abroadFail := 0, 0, 0, 0, 0
	var cnTimes, abroadOKTimes []int64
	for _, e := range v.Evidence {
		if e.Role == "mainland" {
			cn++
			if e.State == "reachable" {
				cnOK++
			}
			if e.State == "failed" {
				cnFail++
				cnTimes = append(cnTimes, e.Attempts[0].Issued)
			}
		} else {
			abroad++
			if e.State == "failed" {
				abroadFail++
			}
			if e.State == "reachable" {
				abroadOKTimes = append(abroadOKTimes, e.Attempts[0].Issued)
			}
		}
	}
	if cn == 0 || abroad == 0 {
		return
	}
	switch {
	case cnOK == cn:
		v.Status, v.Detail = "reachable", "所选大陆节点均可连接该 IP 的指定 TCP 端口。"
	case cnOK > 0 && cnFail > 0:
		v.Status, v.Detail = "partial", "部分大陆节点连续两轮连接失败，另有大陆节点可达；可能是地区、运营商或线路差异。"
	case cnFail == cn && len(abroadOKTimes) > 0:
		for _, at := range abroadOKTimes {
			aligned := true
			for _, cnAt := range cnTimes {
				if cnAt-at > int64(interval+30) || at-cnAt > int64(interval+30) {
					aligned = false
				}
			}
			if aligned {
				v.Status, v.Detail = "suspected", "疑似被墙：所选大陆节点连续两轮连接失败，境外同期可达。仍可能是防火墙、地区封禁或路由故障，结论仅适用于该 IP 与端口。"
				return
			}
		}
	case cnFail == cn && abroadFail == abroad:
		v.Status, v.Detail = "unreachable", "所选大陆与境外节点均连续两轮连接失败，请检查目标服务、端口与防火墙；无法据此判断被墙。"
	}
}
