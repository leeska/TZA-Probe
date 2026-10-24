package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// oldObservationsSQL 是改动前的读路径：窗口内扫 jobs，按
// (task_id,kind,family,host,port,region,carrier) 分组的窗口函数取最新一条。
// 留着当基准：物化表只是把同一条分组规则搬到写入时，任何选择差异都该在这里暴露。
const oldObservationsSQL = `SELECT task,issued,finished,result FROM jobs WHERE node_id=? AND id IN (SELECT id FROM (SELECT id,row_number() OVER(PARTITION BY task_id,json_extract(task,'$.kind'),json_extract(task,'$.family'),json_extract(task,'$.host'),json_extract(task,'$.port'),json_extract(task,'$.region'),json_extract(task,'$.carrier') ORDER BY issued DESC,id DESC) n FROM jobs WHERE node_id=? AND json_valid(task) AND issued>=?) WHERE n=1) ORDER BY task_id`

// latestSelection 读物化表里该节点窗口内的 (分组键, issued)，排序后返回。
func latestSelection(t *testing.T, s *Store, node string, now time.Time) []string {
	t.Helper()
	rows, err := s.db.Query("SELECT probe_key,issued FROM latest WHERE node_id=? AND issued>=? ORDER BY probe_key", node, now.Add(-ProbeWindow).Unix())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var key string
		var issued int64
		if err = rows.Scan(&key, &issued); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s@%d", key, issued))
	}
	sort.Strings(out)
	return out
}

// referenceSelection 用改动前的 SQL 算出同一组签名。
func referenceSelection(t *testing.T, s *Store, node string, now time.Time) []string {
	t.Helper()
	rows, err := s.db.Query(oldObservationsSQL, node, node, now.Add(-ProbeWindow).Unix())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var taskJSON string
		var issued, finished *int64
		var result *string
		if err = rows.Scan(&taskJSON, &issued, &finished, &result); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s@%d", decodeTask(t, taskJSON).ProbeKey(), *issued))
	}
	sort.Strings(out)
	return out
}

func decodeTask(t *testing.T, raw string) model.Probe {
	t.Helper()
	var task model.Probe
	if err := json.Unmarshal([]byte(raw), &task); err != nil {
		t.Fatalf("task %q: %v", raw, err)
	}
	return task
}

func ptr(v int64) *int64 { return &v }

// assertSameSelection 断言物化表与旧查询在该节点上选出完全相同的行。
func assertSameSelection(t *testing.T, s *Store, node string, now time.Time) {
	t.Helper()
	want := referenceSelection(t, s, node, now)
	got := latestSelection(t, s, node, now)
	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Fatalf("物化表与旧查询选行不一致\n旧查询: %v\n物化表: %v", want, got)
	}
}

// jobArgs 用来直接写一行 jobs（绕过租约写入），用于构造乱序完成、坏 JSON 这类
// 只有历史数据或异常路径才会出现的行。
type jobArgs struct {
	id       string
	task     model.Probe
	taskJSON string
	issued   int64
	finished *int64
	result   *model.Result
}

func insertJobRow(t *testing.T, s *Store, node string, a jobArgs) {
	t.Helper()
	raw := a.taskJSON
	if raw == "" {
		raw = encode(a.task)
	}
	// jobs.task_id 有外键：直接写行时先把任务行补上（生产路径由 SaveTask 负责）。任务行存的是
	// 管理任务的形状，由这条探测按单项任务升级出来。
	if _, err := s.db.Exec("INSERT INTO tasks(id,config) VALUES(?,?) ON CONFLICT(id) DO NOTHING", a.task.ID, taskRow(a.task)); err != nil {
		t.Fatal(err)
	}
	var finished, result any
	if a.finished != nil {
		finished = *a.finished
	}
	if a.result != nil {
		result = encode(*a.result)
	}
	if _, err := s.db.Exec("INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result) VALUES(?,?,?,?,?,?,?)", a.id, a.task.ID, node, raw, a.issued, finished, result); err != nil {
		t.Fatal(err)
	}
}

func millis(v float64) *float64 { return &v }

// taskRow is the management task a single probe belongs to, as a tasks.config.
func taskRow(p model.Probe) string {
	return encode(legacyTask{ID: p.ID, Name: p.Name, Kind: p.Kind, Host: p.Host, Port: p.Port, Family: p.Family, Region: p.Region, Carrier: p.Carrier, Interval: p.Interval}.upgrade())
}

// TestObservationsSelectionMatchesWindowFunction 是本改动的核心等价性测试：
// 物化表必须与旧查询挑出同一批行——新的覆盖旧的、乱序完成不覆盖、窗口外不显示、
// 坏 JSON 不顶替、同任务不同目标/协议族各自成组。
func TestObservationsSelectionMatchesWindowFunction(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	node, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Second).Add(-time.Minute)

	tcp := func(id, host, family string) model.Probe {
		return model.Probe{Name: "n", ID: id, Kind: "tcp", Family: family, Host: host, Port: 443, Region: "上海", Carrier: "电信", Interval: 60}
	}
	// A：同组两条都有结果，取新的那条。
	insertJobRow(t, s, node, jobArgs{id: "a1", task: tcp("task-a", "a.example.com", "ipv4"), issued: now.Add(-5 * time.Minute).Unix(), finished: ptr(now.Add(-5 * time.Minute).Unix()), result: &model.Result{JobID: "a1", Status: "ok", Sent: 4, Received: 4, Latency: millis(10)}})
	insertJobRow(t, s, node, jobArgs{id: "a2", task: tcp("task-a", "a.example.com", "ipv4"), issued: now.Add(-1 * time.Minute).Unix(), finished: ptr(now.Add(-1 * time.Minute).Unix()), result: &model.Result{JobID: "a2", Status: "ok", Sent: 4, Received: 4, Latency: millis(20)}})
	// B：新的那条还没完成，旧的那条晚到结果——仍取新的（按 issued 排序）。
	insertJobRow(t, s, node, jobArgs{id: "b1", task: tcp("task-b", "b.example.com", "ipv4"), issued: now.Add(-3 * time.Minute).Unix()})
	insertJobRow(t, s, node, jobArgs{id: "b0", task: tcp("task-b", "b.example.com", "ipv4"), issued: now.Add(-9 * time.Minute).Unix(), finished: ptr(now.Add(-30 * time.Second).Unix()), result: &model.Result{JobID: "b0", Status: "failed", Reason: "connect_failed"}})
	// C：只有窗口外的行，不显示。
	insertJobRow(t, s, node, jobArgs{id: "c1", task: tcp("task-c", "c.example.com", "ipv4"), issued: now.Add(-30 * time.Hour).Unix()})
	// D：最新一条的任务 JSON 坏了，回落到更早那条（旧查询用 json_valid 过滤，物化表用触发器守卫）。
	insertJobRow(t, s, node, jobArgs{id: "d1", task: tcp("task-d", "d.example.com", "ipv4"), issued: now.Add(-2 * time.Minute).Unix(), finished: ptr(now.Add(-2 * time.Minute).Unix()), result: &model.Result{JobID: "d1", Status: "ok", Sent: 1, Received: 1, Latency: millis(30)}})
	insertJobRow(t, s, node, jobArgs{id: "d2", taskJSON: "{oops", issued: now.Add(-10 * time.Second).Unix()})
	// E：同任务同目标不同协议族 → 两个组。
	insertJobRow(t, s, node, jobArgs{id: "e1", task: tcp("task-e", "e.example.com", "ipv4"), issued: now.Add(-2 * time.Minute).Unix()})
	insertJobRow(t, s, node, jobArgs{id: "e2", task: tcp("task-e", "e.example.com", "ipv6"), issued: now.Add(-2 * time.Minute).Unix()})

	assertSameSelection(t, s, node, now)

	obs, err := s.Observations(ctx, node, now)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]model.Observation{}
	for _, o := range obs {
		byKey[o.Task.ProbeKey()] = o
	}
	if len(obs) != 5 {
		t.Fatalf("分组数 = %d，想要 5（%v）", len(obs), byKey)
	}
	if got := byKey[tcp("task-a", "a.example.com", "ipv4").ProbeKey()].Result.Latency; got == nil || *got != 20 {
		t.Errorf("A 组应取新结果 20ms，得到 %v", got)
	}
	if got := byKey[tcp("task-b", "b.example.com", "ipv4").ProbeKey()]; got.Result.Status != "pending" && got.Result.Status != "timeout" {
		t.Errorf("B 组应取未完成的新那条，状态 = %q", got.Result.Status)
	}
	if got := byKey[tcp("task-d", "d.example.com", "ipv4").ProbeKey()].Result.Latency; got == nil || *got != 30 {
		t.Errorf("D 组坏 JSON 不该顶替，得到 %v", got)
	}
	if _, ok := byKey[tcp("task-c", "c.example.com", "ipv4").ProbeKey()]; ok {
		t.Error("C 组在窗口外，不该出现")
	}
}

// TestLatestFollowsWritesAndDeletes 覆盖生产写入路径（租约 + 完成）与两种删除：
// 改任务会删掉该任务的全部历史，保留期清理删掉过期行，两者都要让物化表跟着走。
func TestLatestFollowsWritesAndDeletes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	node, token := makeNode(t, s)
	now := time.Now().Truncate(time.Second)

	task := model.Task{Name: "n", ID: "task-live", Enabled: true, Type: model.TaskProbe, Probe: &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{{Host: "live.example.com", Port: 443, Family: "ipv4", Region: "上海", Carrier: "电信"}}, Interval: 60}}
	id, err := s.SaveTask(ctx, task, true)
	if err != nil {
		t.Fatal(err)
	}
	task.ID = id // SaveTask 自己生成任务 id
	// 走真实写入路径：绑定（面板下发任务）、租约、完成。
	if _, err := s.db.Exec("INSERT INTO bindings(task_id,node_id,due) VALUES(?,?,0)", task.ID, node); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Lease(ctx, node, now)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("租约 = %d 个任务 (%v)", len(jobs), err)
	}
	if err = s.Finish(ctx, node, model.Result{JobID: jobs[0].ID, Status: "ok", Sent: 1, Received: 1, Latency: millis(12)}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	assertSameSelection(t, s, node, now.Add(time.Second))
	obs, err := s.Observations(ctx, node, now.Add(time.Second))
	if err != nil || len(obs) != 1 {
		t.Fatalf("观测 = %d 条 (%v)", len(obs), err)
	}
	if obs[0].Result.Status != "ok" || obs[0].Task.Host != "live.example.com" {
		t.Fatalf("观测内容不对：%+v", obs[0])
	}

	_ = token
	// 改任务 → SaveTask 删掉该任务的历史，物化表里该组也要消失。
	edited := task
	edited.Probe = &model.ProbeSpec{Checks: []string{"tcp"}, Targets: []model.Target{{Host: "moved.example.com", Port: 443, Family: "ipv4", Region: "上海", Carrier: "电信"}}, Interval: 60}
	if _, err = s.SaveTask(ctx, edited, false); err != nil {
		t.Fatal(err)
	}
	if got := latestSelection(t, s, node, now.Add(time.Second)); len(got) != 0 {
		t.Fatalf("改任务后物化表仍有 %v", got)
	}
	// 历史行由后台分批删（见 purge.go）：删完之后旧查询也该选不出它。
	drainPurges(t, s)
	assertSameSelection(t, s, node, now.Add(time.Second))

	// 保留期清理：删掉的旧行让位，最新的那条仍在。
	long := model.Probe{Name: "n", ID: "task-old", Kind: "tcp", Family: "ipv4", Host: "old.example.com", Port: 80, Region: "上海", Carrier: "联通", Interval: 60}
	insertJobRow(t, s, node, jobArgs{id: "old1", task: long, issued: now.Add(-9 * 24 * time.Hour).Unix()})
	insertJobRow(t, s, node, jobArgs{id: "new1", task: long, issued: now.Add(-2 * time.Minute).Unix()})
	if err = s.Clean(ctx, now); err != nil {
		t.Fatal(err)
	}
	sel := latestSelection(t, s, node, now.Add(time.Second))
	if len(sel) != 1 || sel[0] != fmt.Sprintf("%s@%d", long.ProbeKey(), now.Add(-2*time.Minute).Unix()) {
		t.Fatalf("清理后物化表 = %v", sel)
	}
	assertSameSelection(t, s, node, now.Add(time.Second))
}

// TestLatestMigrationBackfillsExistingJobs 造一个「上一版形状」的库（无 latest、
// 无触发器、jobs 无 probe_key），重新打开后要求：版本升到 4、物化表回填完整、
// 与旧查询的选择一致。
func TestLatestMigrationBackfillsExistingJobs(t *testing.T) {
	path := t.TempDir() + "/upgrade.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	node, _ := makeNode(t, s)
	now := time.Now().Truncate(time.Second)
	if _, err = s.db.Exec(`DROP TABLE latest; DROP TRIGGER jobs_latest_insert; DROP TRIGGER jobs_latest_update; DROP TRIGGER jobs_latest_delete; DROP TRIGGER jobs_tcp_bucket_insert; DROP TRIGGER jobs_tcp_bucket_finish; DROP TABLE tcp_buckets; DROP TABLE tcp_buckets_state; ALTER TABLE jobs DROP COLUMN probe_key; PRAGMA user_version=3;`); err != nil {
		t.Fatal(err)
	}
	for i, host := range []string{"one.example.com", "two.example.com"} {
		task := model.Probe{Name: "n", ID: fmt.Sprintf("task-%d", i), Kind: "tcp", Family: "ipv4", Host: host, Port: 443, Region: "上海", Carrier: "电信", Interval: 60}
		for n := 0; n < 3; n++ {
			issued := now.Add(time.Duration(-3*n) * time.Minute).Unix()
			insertJobRow(t, s, node, jobArgs{id: fmt.Sprintf("j%d%d", i, n), task: task, issued: issued, finished: ptr(issued), result: &model.Result{JobID: "x", Status: "ok", Sent: 1, Received: 1, Latency: millis(float64(n))}})
		}
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 17 {
		t.Fatalf("user_version = %d (%v)", version, err)
	}
	assertSameSelection(t, s, node, now)
	if sel := latestSelection(t, s, node, now); len(sel) != 2 {
		t.Fatalf("回填后物化表 = %v，想要 2 组", sel)
	}
	obs, err := s.Observations(ctx, node, now)
	if err != nil || len(obs) != 2 {
		t.Fatalf("观测 = %d 条 (%v)", len(obs), err)
	}
}
