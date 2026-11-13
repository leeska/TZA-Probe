package httpapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/module"
)

// overview is the admin home: core cards (nodes, billing, tasks, alerts)
// followed by each module's own. A card whose data cannot be read says so;
// the page itself never fails on one of them.
func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	cards := []module.Card{}
	add := func(id, title string, build func(context.Context) (module.Card, error)) {
		card, err := build(ctx)
		if err != nil {
			card = module.Card{Value: "读取失败", Note: err.Error(), Tone: "warn"}
		}
		if card.ID == "" {
			card.ID = id
		}
		if card.Title == "" {
			card.Title = title
		}
		cards = append(cards, card)
	}
	nodes, nodesErr := a.Store.Nodes(ctx, now)
	add("nodes", "节点", func(context.Context) (module.Card, error) {
		if nodesErr != nil {
			return module.Card{}, nodesErr
		}
		return nodesCard(nodes), nil
	})
	add("billing", "到期与流量", func(context.Context) (module.Card, error) {
		if nodesErr != nil {
			return module.Card{}, nodesErr
		}
		return billingCard(nodes, now), nil
	})
	add("tasks", "网络探测", a.tasksCard)
	add("alerts", "告警", a.alertsCard)
	cards = append(cards, module.Overview(ctx, a.Modules)...)
	write(w, 200, map[string]any{"cards": cards, "generated_at": now.Unix()})
}

func nodesCard(nodes []model.NodeView) module.Card {
	card := module.Card{ID: "nodes", Title: "节点", Page: "nodes"}
	online, pending := 0, 0
	for _, n := range nodes {
		switch {
		case n.Online:
			online++
		case n.LastSeen == 0:
			pending++
		default:
			if len(card.Items) < module.MaxCardItems {
				card.Items = append(card.Items, module.CardItem{Label: n.Name, Note: "离线，最后上报 " + time.Unix(n.LastSeen, 0).Format("01-02 15:04"), Tone: "bad"})
			}
		}
	}
	offline := len(nodes) - online - pending
	card.Value = fmt.Sprintf("%d / %d 在线", online, len(nodes))
	switch {
	case len(nodes) == 0:
		card.Value, card.Note = "0", "还没有节点"
	case offline > 0:
		card.Tone, card.Note = "bad", fmt.Sprintf("%d 台离线", offline)
	default:
		card.Tone, card.Note = "good", "全部在线"
	}
	if pending > 0 {
		card.Note += fmt.Sprintf("，%d 台等待首次上报", pending)
	}
	return card
}

// billingCard lists machines that expire within two weeks (or already did)
// and lines past 90% of their traffic allowance, soonest first.
func billingCard(nodes []model.NodeView, now time.Time) module.Card {
	card := module.Card{ID: "billing", Title: "到期与流量", Page: "nodes"}
	type entry struct {
		item module.CardItem
		at   int64
	}
	var list []entry
	for _, n := range nodes {
		if n.Billing.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, n.Billing.ExpiresAt); err == nil {
				days := int(math.Ceil(t.Sub(now).Hours() / 24))
				switch {
				case t.Before(now):
					list = append(list, entry{module.CardItem{Label: n.Name, Note: "已于 " + t.Local().Format("2006-01-02") + " 到期", Tone: "bad"}, t.Unix()})
				case days <= 14:
					list = append(list, entry{module.CardItem{Label: n.Name, Note: fmt.Sprintf("%d 天后到期（%s）", days, t.Local().Format("01-02")), Tone: "warn"}, t.Unix()})
				}
			}
		}
		if limit := n.Billing.TrafficLimit; limit > 0 && n.TrafficUsed*10 >= limit*9 {
			tone := "warn"
			if n.TrafficUsed >= limit {
				tone = "bad"
			}
			list = append(list, entry{module.CardItem{Label: n.Name, Note: fmt.Sprintf("本期流量已用 %.0f%%", float64(n.TrafficUsed)*100/float64(limit)), Tone: tone}, n.CycleEnd})
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].at < list[j].at })
	bad := false
	for i, e := range list {
		bad = bad || e.item.Tone == "bad"
		if i < module.MaxCardItems {
			card.Items = append(card.Items, e.item)
		}
	}
	switch {
	case len(list) == 0:
		card.Value, card.Tone, card.Note = "无需处理", "good", "两周内没有到期，流量都在 90% 以下"
	case bad:
		card.Value, card.Tone = fmt.Sprintf("%d 项", len(list)), "bad"
	default:
		card.Value, card.Tone = fmt.Sprintf("%d 项", len(list)), "warn"
	}
	return card
}

func (a *API) tasksCard(ctx context.Context) (module.Card, error) {
	card := module.Card{ID: "tasks", Title: "网络探测", Page: "tasks"}
	tasks, err := a.svc.Tasks(ctx)
	if err != nil {
		return card, err
	}
	health, err := a.svc.TaskHealth(ctx)
	if err != nil {
		return card, err
	}
	enabled, failing := 0, 0
	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		enabled++
		if h := health[t.ID]; h.Failed > 0 {
			failing++
			if len(card.Items) < module.MaxCardItems {
				card.Items = append(card.Items, module.CardItem{Label: t.Name, Note: fmt.Sprintf("%d 项失败，%d 项成功", h.Failed, h.OK), Tone: "warn"})
			}
		}
	}
	blocks, err := a.svc.BlockChecks(ctx)
	if err != nil {
		return card, err
	}
	suspected := 0
	for _, b := range blocks {
		if b.Status == "suspected" || b.Status == "partial" {
			suspected++
			if len(card.Items) < module.MaxCardItems {
				tone, word := "warn", "部分大陆节点不可达"
				if b.Status == "suspected" {
					tone, word = "bad", "疑似被墙"
				}
				card.Items = append(card.Items, module.CardItem{Label: b.Name, Note: fmt.Sprintf("%s:%d %s", b.Host, b.Port, word), Tone: tone})
			}
		}
	}
	card.Value = fmt.Sprintf("%d 个任务", enabled)
	switch {
	case len(tasks) == 0:
		card.Value, card.Note = "0", "还没有任务"
	case suspected > 0:
		card.Tone, card.Note = "bad", fmt.Sprintf("%d 项被墙检测异常", suspected)
	case failing > 0:
		card.Tone, card.Note = "warn", fmt.Sprintf("%d 个任务有失败", failing)
	default:
		card.Tone, card.Note = "good", "最近执行均成功"
	}
	if paused := len(tasks) - enabled; paused > 0 {
		card.Note += fmt.Sprintf("，%d 个已暂停", paused)
	}
	return card, nil
}

func (a *API) alertsCard(ctx context.Context) (module.Card, error) {
	card := module.Card{ID: "alerts", Title: "告警", Page: "alerts"}
	events, err := a.svc.Events(ctx, model.EventQuery{Active: true, Limit: 50})
	if err != nil {
		return card, err
	}
	for i, e := range events {
		if i < module.MaxCardItems {
			card.Items = append(card.Items, module.CardItem{Label: e.NodeName, Note: e.Message, Tone: "bad"})
		}
	}
	if len(events) == 0 {
		card.Value, card.Tone, card.Note = "无", "good", "当前没有触发中的告警"
	} else {
		card.Value, card.Tone, card.Note = fmt.Sprintf("%d 条", len(events)), "bad", "正在触发"
	}
	return card, nil
}
