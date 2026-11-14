package backup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leeska/TZA-ControlCenter/internal/agent"
	"github.com/leeska/TZA-ControlCenter/internal/backup"
	"github.com/leeska/TZA-ControlCenter/internal/channel"
	"github.com/leeska/TZA-ControlCenter/internal/httpapi"
	"github.com/leeska/TZA-ControlCenter/internal/model"
	"github.com/leeska/TZA-ControlCenter/internal/module"
	"github.com/leeska/TZA-ControlCenter/internal/remote"
	"github.com/leeska/TZA-ControlCenter/internal/storage"
)

type recorder struct {
	mu     sync.Mutex
	events []model.Event
}

func (r *recorder) RecordEvent(_ context.Context, e model.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) list() []model.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.Event{}, r.events...)
}

// 备份：在线快照数据库与数据目录，用口令加密后经管理通道写到节点，只留最近几份；没开备份
// 能力的节点记为失败（部分成功）；拿备份文件和口令能恢复出一个可用的数据目录。
func TestBackupToNodeAndRestore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dataDir := t.TempDir()
	remoteMod, backupMod := &remote.Module{}, &backup.Module{}
	mods := []module.Module{remoteMod, backupMod}
	store, err := storage.OpenWith(filepath.Join(dataDir, backup.DatabaseName), module.Migrations(mods))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, token, err := store.SaveNode(ctx, model.Node{Name: "vault", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	offline, _, _ := store.SaveNode(ctx, model.Node{Name: "offline", Billing: model.Billing{ResetAt: "2026-11-01T00:00:00Z", TrafficMode: "sum"}}, true)
	os.WriteFile(filepath.Join(dataDir, "admin-account"), []byte("account"), 0600)
	os.MkdirAll(filepath.Join(dataDir, "themes", "t1"), 0700)
	os.WriteFile(filepath.Join(dataDir, "themes", "t1", "theme.json"), []byte("{}"), 0600)
	os.MkdirAll(filepath.Join(dataDir, "kernels"), 0700)
	os.WriteFile(filepath.Join(dataDir, "kernels", "xray"), []byte("big"), 0600)
	os.WriteFile(filepath.Join(dataDir, "admin-sessions"), []byte("{}"), 0600)

	hub := &channel.Hub{}
	var clock sync.Mutex
	now := time.Date(2026, 11, 14, 2, 0, 0, 0, time.UTC)
	remoteMod.Hub, remoteMod.Nodes = hub, store
	backupMod.DB, backupMod.DataDir, backupMod.Hub, backupMod.Nodes = store.DB(), dataDir, hub, store
	backupMod.KeyParams = &backup.Params{LogN: 10, R: 8, P: 1}
	events := &recorder{}
	backupMod.Events = events
	backupMod.Now = func() time.Time { clock.Lock(); defer clock.Unlock(); now = now.Add(time.Minute); return now }
	admin := model.Secret()
	server := httptest.NewServer((&httpapi.API{Store: store, AdminToken: admin, Assets: http.NotFoundHandler(), Modules: mods}).Handler())
	defer server.Close()
	call := func(method, path string, body any, out any) int {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, server.URL+"/api/v1"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil {
			json.NewDecoder(res.Body).Decode(out)
		}
		return res.StatusCode
	}
	transport, err := agent.NewTransport(server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	go (&agent.Channel{Transport: transport, Handlers: agent.BackupHandlers(), Capabilities: []string{channel.CapabilityBackup}}).Run(ctx)
	for deadline := time.Now().Add(10 * time.Second); !hub.Status(node).Connected; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("channel never connected")
		}
	}

	var view backup.View
	call("GET", "/admin/backups", nil, &view)
	if view.HasPassphrase || view.Keep != 7 || view.Hour != 3 {
		t.Fatalf("默认：%+v", view)
	}
	cards, _ := backupMod.Overview(ctx)
	if cards[0].Tone != "warn" || !strings.Contains(cards[0].Note, "只有主控这一份") {
		t.Fatalf("没设置时总览要提醒：%+v", cards)
	}
	vault := filepath.Join(t.TempDir(), "backups")
	if code := call("PUT", "/admin/backups", map[string]any{"enabled": true, "hour": 3, "keep": 2, "targets": []map[string]string{{"node_id": node, "dir": vault}}}, nil); code != 400 {
		t.Fatal("没设口令不能开启", code)
	}
	if code := call("PUT", "/admin/backups", map[string]any{"enabled": true, "hour": 3, "keep": 2, "passphrase": "short", "targets": []map[string]string{{"node_id": node, "dir": vault}}}, nil); code != 400 {
		t.Fatal("口令太短要拒绝", code)
	}
	if code := call("PUT", "/admin/backups", map[string]any{"enabled": true, "hour": 3, "keep": 2, "passphrase": "a long backup passphrase", "targets": []map[string]string{{"node_id": node, "dir": vault}, {"node_id": offline}}}, nil); code != 200 {
		t.Fatal(code)
	}
	call("GET", "/admin/backups", nil, &view)
	if !view.HasPassphrase || view.Targets[1].Dir != backup.DefaultDir {
		t.Fatalf("%+v", view)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), `"key"`) {
		t.Fatal("密钥不能出现在接口里")
	}

	for i := 0; i < 3; i++ {
		run, err := backupMod.Backup(ctx, "manual")
		if err != nil || run.Status != backup.RunPartial || run.Targets[0].Error != "" || !strings.Contains(run.Targets[1].Error, "--manage") {
			t.Fatalf("第 %d 次：%v %+v", i, err, run)
		}
	}
	// 告警：有节点没存下 → 一条触发通知，连续几次不重复。
	if ev := events.list(); len(ev) != 1 || !ev[0].Active || ev[0].Kind != model.EventBackup || !strings.Contains(ev[0].Message, "没存下") {
		t.Fatalf("备份告警：%+v", ev)
	}
	files, _ := filepath.Glob(filepath.Join(vault, "*.tzabk"))
	if len(files) != 2 {
		t.Fatalf("只保留 2 份：%v", files)
	}
	if info, _ := os.Stat(files[1]); info.Mode().Perm() != 0600 {
		t.Fatal("备份文件要 0600")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dataDir, ".backup-*")); len(leftovers) != 0 {
		t.Fatal("临时文件要清掉", leftovers)
	}
	content, _ := os.ReadFile(files[1])
	if bytes.Contains(content, []byte("SQLite format")) || bytes.Contains(content, []byte("vault")) {
		t.Fatal("备份必须是加密的")
	}

	// 恢复：口令对才能解开，解出的是能打开的数据目录，缓存和会话不在里面。
	f, _ := os.Open(files[1])
	if _, err := backup.NewReader(f, "wrong passphrase!!"); err == nil {
		t.Fatal("口令错要拒绝")
	}
	f.Close()
	f, _ = os.Open(files[1])
	r, err := backup.NewReader(f, "a long backup passphrase")
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := backup.Extract(r, restored); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, want := range []string{"admin-account", "themes/t1/theme.json", backup.DatabaseName} {
		if _, err := os.Stat(filepath.Join(restored, want)); err != nil {
			t.Errorf("缺 %s", want)
		}
	}
	for _, not := range []string{"kernels", "admin-sessions"} {
		if _, err := os.Stat(filepath.Join(restored, not)); err == nil {
			t.Errorf("%s 不该在备份里", not)
		}
	}
	again, err := storage.OpenWith(filepath.Join(restored, backup.DatabaseName), module.Migrations(mods))
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := again.Nodes(ctx, time.Now())
	again.Close()
	if len(nodes) != 2 {
		t.Fatalf("恢复出的库：%d 个节点", len(nodes))
	}
	if err := backup.Extract(bytes.NewReader(nil), restored); err == nil {
		t.Fatal("不能恢复到非空目录")
	}

	cards, _ = backupMod.Overview(ctx)
	if cards[0].Tone != "warn" || !strings.Contains(cards[0].Note, "部分节点") {
		t.Fatalf("部分成功：%+v", cards)
	}
	// 去掉连不上的节点后再备份一次：恢复，发一条解除通知。
	call("PUT", "/admin/backups", map[string]any{"enabled": true, "hour": 3, "keep": 2, "targets": []map[string]string{{"node_id": node, "dir": vault}}}, nil)
	if run, err := backupMod.Backup(ctx, "manual"); err != nil || run.Status != backup.RunOK {
		t.Fatal(run, err)
	}
	if ev := events.list(); len(ev) != 2 || ev[1].Active {
		t.Fatalf("备份恢复：%+v", ev)
	}
	call("GET", "/admin/backups", nil, &view)
	if len(view.Runs) != 4 || view.Runs[0].Size == 0 || view.Runs[0].SHA256 == "" {
		t.Fatalf("记录：%+v", view.Runs)
	}
}
