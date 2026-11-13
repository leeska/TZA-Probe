package regiondrift

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipguard"
	"github.com/leeska/TZA-Probe/internal/module"
)

var serviceNames = map[string]string{"google": "Google", "meta": "Meta"}

// Overview is one card: how many enabled nodes read their target region, and
// which ones a recent check found in another region. It follows the panel's
// reading: only a completed check for the node's current target counts, and a
// result older than a day is stale rather than normal.
func (m *Module) Overview(ctx context.Context) ([]module.Card, error) {
	if m.Broker == nil {
		return nil, nil
	}
	card := module.Card{ID: "regiondrift", Title: "地区偏移检测", Page: "ipguard"}
	if !m.Broker.WorkerConfigured {
		card.Value, card.Note = "未安装", "检测浏览器尚未安装"
		return []module.Card{card}, nil
	}
	policies, err := m.Broker.Store.Policies(ctx)
	if err != nil {
		return nil, err
	}
	jobs, err := m.Broker.Store.Jobs(ctx, "")
	if err != nil {
		return nil, err
	}
	nodes, err := m.Nodes.Nodes(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, n := range nodes {
		names[n.ID] = n.Name
	}
	now := time.Now().Unix()
	enabled, drifted, unknown := 0, 0, 0
	ids := make([]string, 0, len(policies))
	for id := range policies {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return names[ids[i]] < names[ids[j]] })
	for _, id := range ids {
		policy := policies[id]
		if !policy.Enabled || names[id] == "" {
			continue
		}
		enabled++
		for _, service := range policy.Services {
			job := latestCheck(jobs, id, service, policy)
			switch {
			case job == nil || job.State != "completed" || job.Result == nil || now-job.FinishedAt > 86400:
				unknown++
			case job.Result.Assessment == "other_region" && job.Result.Page.Value != policy.Country:
				drifted++
				if len(card.Items) < module.MaxCardItems {
					card.Items = append(card.Items, module.CardItem{Label: names[id], Note: fmt.Sprintf("%s：%s，目标 %s", serviceNames[service], job.Result.Page.Value, policy.Country), Tone: "bad"})
				}
			case job.Result.Assessment != "target_observed":
				unknown++
			}
		}
	}
	switch {
	case enabled == 0:
		card.Value, card.Note = "未开启", "还没有节点开启检测"
	case drifted > 0:
		card.Value, card.Tone = fmt.Sprintf("%d 项偏移", drifted), "bad"
		card.Note = fmt.Sprintf("%d 个节点开启检测", enabled)
	default:
		card.Value, card.Tone = "正常", "good"
		card.Note = fmt.Sprintf("%d 个节点开启检测", enabled)
		if unknown > 0 {
			card.Tone = ""
			card.Note += fmt.Sprintf("，%d 项暂无结论", unknown)
		}
	}
	if now-m.Broker.WorkerSeen() > 45 {
		card.Tone, card.Note = "warn", "检测浏览器未连接"
	}
	return []module.Card{card}, nil
}

// latestCheck is the newest detection (not a correction attempt) of service on
// node made for the node's current target.
func latestCheck(jobs []ipguard.Job, node, service string, policy ipguard.Policy) *ipguard.Job {
	for i := range jobs {
		j := &jobs[i]
		if j.NodeID == node && j.Service == service && j.Phase != "experiment" && j.Policy.Country == policy.Country && j.Policy.ExpectedIP == policy.ExpectedIP {
			return j
		}
	}
	return nil
}
