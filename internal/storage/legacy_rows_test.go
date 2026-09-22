package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// 这些行由早期版本写下时字段更少：任务 JSON 没有 family / port，结果 JSON 没有 sent。
// json_extract 在缺失键上返回 NULL，直接 Scan 进 Go 的 string / int 会报错，一条旧数据
// 就能让整个节点的网络历史返回 500（生产上的「读取节点详情失败：服务器处理失败」）。
func TestNetworkHistoryToleratesLegacyJobs(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now()
	insertJob(t, s, id, "job-legacy", `{"id":"leaf-legacy","name":"上海联通","kind":"tcp","host":"203.0.113.9","region":"上海","carrier":"联通","interval":60,"enabled":true,"node_ids":["`+id+`"]}`, `{"status":"ok","received":3,"latency":21.5,"hops":[]}`)
	// 任务 JSON 都读不出来的行只跳过自己，不该影响同一节点的其它目标。
	insertJob(t, s, id, "job-broken", `{"name":`, `{"status":"ok","sent":3,"received":3}`)
	// 结果 JSON 坏掉的行按“没有结果”聚合，同样不能让查询整体失败。
	insertJob(t, s, id, "job-broken-result", `{"id":"leaf-broken","name":"上海移动","kind":"tcp","family":"ipv4","host":"203.0.113.11","port":443,"region":"上海","carrier":"移动","interval":60,"enabled":true,"node_ids":["`+id+`"]}`, `{oops`)
	insertJob(t, s, id, "job-current", `{"id":"leaf-current","name":"上海电信","kind":"tcp","family":"ipv4","host":"203.0.113.10","port":443,"region":"上海","carrier":"电信","interval":60,"enabled":true,"node_ids":["`+id+`"]}`, `{"status":"ok","sent":3,"received":3,"latency":18.2,"hops":[]}`)

	series, err := s.NetworkHistory(ctx, id, 24, now)
	if err != nil {
		t.Fatalf("旧行不能让网络历史整体失败：%v", err)
	}
	if len(series) != 3 {
		t.Fatalf("want the two readable targets plus the torn-result one, got %d: %+v", len(series), series)
	}
	legacy := series[0]
	var byHost = map[string]model.NetworkSeries{}
	for _, s := range series {
		byHost[s.Host] = s
	}
	if legacy = byHost["203.0.113.9"]; legacy.Name != "上海联通" {
		t.Fatalf("unreadable job must not become a series: %+v", series)
	}
	// 旧行没有 family：目标是 IP 字面量，地址族由地址决定，补成 ipv4 才能和新数据接上。
	if legacy.Family != "ipv4" && legacy.Host == "203.0.113.9" {
		t.Fatalf("legacy family must be derived from the address, got %+v", legacy)
	}
	if len(legacy.Samples) != 1 || legacy.Samples[0].Latency == nil || *legacy.Samples[0].Latency != 21.5 {
		t.Fatalf("legacy sample lost its latency: %+v", legacy.Samples)
	}
	// sent 缺失时 sum 为 NULL：不能变成连接失败率，也不能让请求失败。
	if legacy.Samples[0].Failure != nil {
		t.Fatalf("missing sent must not fabricate a failure rate: %+v", legacy.Samples[0].Failure)
	}
	var current *model.NetworkSeries
	for i := range series {
		if series[i].Host == "203.0.113.10" {
			current = &series[i]
		}
	}
	if current == nil || current.Port != 443 || current.Family != "ipv4" {
		t.Fatalf("current row must keep its own fields: %+v", series)
	}
	for _, s := range series {
		if s.Host != "203.0.113.11" {
			continue
		}
		if len(s.Samples) != 1 || s.Samples[0].Latency != nil || s.Samples[0].Failure != nil {
			t.Fatalf("a torn result must aggregate as no result, not as零延迟: %+v", s.Samples)
		}
	}
}

// 坏掉的样本行（旧格式或半写入）只跳过自己：一台机器的历史不该因为一行读不出来就整体打不开。
func TestHistorySkipsUnreadableSamples(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now()
	good, err := json.Marshal(metrics())
	if err != nil {
		t.Fatal(err)
	}
	for i, payload := range []string{string(good), "[]", "{oops"} {
		if _, err = s.db.ExecContext(ctx, "INSERT OR REPLACE INTO samples(node_id,at,payload) VALUES(?,?,?)", id, now.Add(-time.Duration(i)*time.Minute).Unix(), payload); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.History(ctx, id, 24, now)
	if err != nil {
		t.Fatalf("坏样本不能让历史整体失败：%v", err)
	}
	if len(out) != 1 || out[0].Metrics.CPU != metrics().CPU {
		t.Fatalf("want exactly the readable sample, got %+v", out)
	}
}

// 结果 JSON 读不出来时按“未知”显示；任务 JSON 读不出来的行跳过。
func TestObservationsTolerateUnreadableRows(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := makeNode(t, s)
	now := time.Now()
	insertJob(t, s, id, "job-bad-result", `{"id":"leaf-1","name":"上海电信","kind":"tcp","family":"ipv4","host":"203.0.113.10","port":443,"region":"上海","carrier":"电信","interval":60,"enabled":true,"node_ids":["`+id+`"]}`, `{"status":"ok","latency":"not a number"}`)
	insertJob(t, s, id, "job-bad-task", `{"name":`, `{"status":"ok","sent":3,"received":3}`)

	out, err := s.Observations(ctx, id, now)
	if err != nil {
		t.Fatalf("坏行不能让观测列表整体失败：%v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want the readable observation only, got %+v", out)
	}
	if out[0].Result.Status != "unknown" || out[0].Task.Carrier != "电信" {
		t.Fatalf("a torn result must read as unknown, kept task intact: %+v", out[0])
	}
}

func insertJob(t *testing.T, s *Store, nodeID, jobID, taskJSON, resultJSON string) {
	t.Helper()
	now := time.Now()
	// bindings / jobs 都带外键，先把这条任务本身放进 tasks 表。
	if _, err := s.db.ExecContext(context.Background(),
		"INSERT OR IGNORE INTO tasks(id,config) VALUES(?,?)", "task-"+jobID, "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(context.Background(),
		"INSERT INTO jobs(id,task_id,node_id,task,issued,finished,result) VALUES(?,?,?,?,?,?,?)",
		jobID, "task-"+jobID, nodeID, taskJSON, now.Add(-2*time.Minute).Unix(), now.Add(-time.Minute).Unix(), resultJSON); err != nil {
		t.Fatal(err)
	}
}
