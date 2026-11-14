package regiondrift

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/ipguard"
)

// reconcileEvery is how often the derived policies are brought in line with
// the tasks and node settings even when nothing was saved: a node whose exit
// was not connected at save time gets its policy once the exit reports.
const reconcileEvery = time.Minute

// state holds what the last reconcile could not do, per node: the reason the
// panel shows next to that node (未设置目标地区, 出口未连接 …).
type state struct {
	mu      sync.Mutex
	pending map[string]string
	run     sync.Mutex // one reconcile at a time
}

// Run keeps the policies reconciled until ctx ends.
func (m *Module) Run(ctx context.Context) {
	if m.Broker == nil {
		return
	}
	ticker := time.NewTicker(reconcileEvery)
	defer ticker.Stop()
	for {
		if err := m.reconcile(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("region drift reconcile", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Pending is the reason each node in a task has no current policy.
func (m *Module) Pending() map[string]string {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	out := make(map[string]string, len(m.state.pending))
	for k, v := range m.state.pending {
		out[k] = v
	}
	return out
}

// reconcile derives every node's policy from its task and node settings:
//   - a node in no task, or without node settings, has no policy (deleted)
//   - otherwise the policy carries the task's switches and services and the
//     node's country, exit family and position; the exit IP comes from the
//     node's live egress report
//
// A policy that already matches is left alone, so jobs are not cancelled by a
// reconcile that changes nothing.
func (m *Module) reconcile(ctx context.Context) error {
	m.state.run.Lock()
	defer m.state.run.Unlock()
	store := m.Broker.Store
	tasks, err := store.Tasks(ctx)
	if err != nil {
		return err
	}
	settings, err := store.NodeSettings(ctx)
	if err != nil {
		return err
	}
	policies, err := store.Policies(ctx)
	if err != nil {
		return err
	}
	nodes, err := m.Nodes.Nodes(ctx, time.Now())
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, n := range nodes {
		known[n.ID] = true
	}
	owner := map[string]ipguard.Task{}
	for _, t := range tasks {
		for _, id := range t.NodeIDs {
			owner[id] = t
		}
	}
	pending := map[string]string{}
	ids := map[string]bool{}
	for id := range policies {
		ids[id] = true
	}
	for id := range owner {
		ids[id] = true
	}
	for id := range ids {
		task, inTask := owner[id]
		node, configured := settings[id]
		policy, has := policies[id]
		if !inTask || !known[id] || !configured {
			if inTask && known[id] {
				pending[id] = "未设置目标地区：在节点管理里设置"
			}
			if has {
				if err := store.DeletePolicy(ctx, id); err != nil {
					return err
				}
			}
			continue
		}
		want := ipguard.Settings{Revision: policy.Revision, Enabled: task.Enabled, AutoRecover: task.AutoRecover, Family: node.Family, Country: node.Country, Services: task.Services, Position: node.Position}
		status := m.Broker.Status(id)
		if has && matches(policy, want, status) {
			continue
		}
		next, err := want.Policy(policy, status)
		if err != nil {
			pending[id] = err.Error()
			continue
		}
		if _, err := store.SavePolicy(ctx, id, next); err != nil {
			pending[id] = err.Error()
		}
	}
	m.state.mu.Lock()
	m.state.pending = pending
	m.state.mu.Unlock()
	return nil
}

// matches reports whether policy already is what want derives to. The exit IP
// counts only while the egress report is fresh: a stale report keeps the
// policy as it is rather than churning it.
func matches(policy ipguard.Policy, want ipguard.Settings, status ipguard.Status) bool {
	a, b := slices.Clone(policy.Services), slices.Clone(want.Services)
	sort.Strings(a)
	sort.Strings(b)
	if policy.Enabled != want.Enabled || policy.AutoRecover != want.AutoRecover || policy.Country != want.Country || !slices.Equal(a, b) || !samePosition(policy.Position, want.Position) {
		return false
	}
	if strings.Contains(policy.ExpectedIP, ":") != (want.Family == "ipv6") {
		return false
	}
	if !want.Enabled || !status.Enabled || time.Now().Unix()-status.ReceivedAt >= 45 {
		return true
	}
	return slices.Contains(status.AllowedIPs, policy.ExpectedIP)
}

func samePosition(a, b *ipguard.Position) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
