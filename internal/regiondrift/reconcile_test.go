package regiondrift

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipguard"
	"github.com/leeska/TZA-Probe/internal/model"
)

type fakeNodes []string

func (f fakeNodes) Nodes(context.Context, time.Time) ([]model.NodeView, error) {
	out := []model.NodeView{}
	for _, id := range f {
		var n model.NodeView
		n.ID, n.Name = id, id
		out = append(out, n)
	}
	return out, nil
}

// 任务 + 节点设置 → 每台节点的检测设置：在任务里且设好目标地区、出口在线的节点得到设置；
// 没设目标地区或出口未连接的留在 pending 里说明原因；什么都没变时不重写（不打断进行中的检测）；
// 停用任务只关掉设置，删掉任务或把节点移出任务就删掉它的设置。
func TestReconcile(t *testing.T) {
	store, err := ipguard.Open(filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	broker := ipguard.NewBroker(store)
	m := &Module{Broker: broker, Nodes: fakeNodes{"a", "b", "c"}}
	broker.Report("a", ipguard.Status{Enabled: true, AllowedIPs: []string{"8.8.8.8"}, ReceivedAt: time.Now().Unix()})
	for _, id := range []string{"a", "b"} {
		if err := store.SaveNodeSettings(ctx, id, ipguard.NodeSettings{Family: "ipv4", Country: "US"}); err != nil {
			t.Fatal(err)
		}
	}
	task, err := store.SaveTask(ctx, ipguard.Task{Name: "美国节点", Enabled: true, Services: []string{"google"}, NodeIDs: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveTask(ctx, ipguard.Task{Name: "另一个", Services: []string{"meta"}, NodeIDs: []string{"a"}}); err == nil {
		t.Fatal("一台节点只能属于一个任务")
	}
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	policies, _ := store.Policies(ctx)
	a, ok := policies["a"]
	if !ok || !a.Enabled || a.ExpectedIP != "8.8.8.8" || a.Country != "US" || len(a.Services) != 1 || a.Services[0] != "google" {
		t.Fatalf("a 的检测设置：%+v", a)
	}
	if _, ok := policies["b"]; ok || len(policies) != 1 {
		t.Fatalf("出口未连接的 b 没有设置：%v", policies)
	}
	pending := m.Pending()
	if pending["b"] == "" || pending["c"] != "未设置目标地区：在节点管理里设置" || pending["a"] != "" {
		t.Fatalf("pending：%v", pending)
	}
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.Policies(ctx); again["a"].Revision != a.Revision {
		t.Fatalf("没变化不能重写：%d → %d", a.Revision, again["a"].Revision)
	}
	task.Enabled = false
	if _, err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.Policies(ctx); p["a"].Enabled || p["a"].ExpectedIP != "8.8.8.8" {
		t.Fatalf("停用任务只关掉设置：%+v", p["a"])
	}
	task.NodeIDs = []string{"b", "c"}
	if _, err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.Policies(ctx); len(p) != 0 {
		t.Fatalf("移出任务就删掉设置：%v", p)
	}
	if _, err := store.SaveTask(ctx, ipguard.Task{Name: "另一个", Enabled: true, Services: []string{"meta"}, NodeIDs: []string{"a"}}); err != nil {
		t.Fatalf("移出后可以加入别的任务：%v", err)
	}
	if err := store.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.Policies(ctx); len(p) != 1 || p["a"].Services[0] != "meta" {
		t.Fatalf("新任务接管 a：%v", p)
	}
}
