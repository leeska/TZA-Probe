package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/leeska/TZA-Probe/internal/catalog"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
)

// adminRoutes is the management API. Every route decodes its request and hands
// it to the service, which the MCP tools share; nothing here decides a rule.
func (a *API) adminRoutes(mux *http.ServeMux) {
	route := func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, a.admin(handler)) }

	// ---- nodes
	route("GET /api/v1/admin/nodes", a.nodes)
	route("POST /api/v1/admin/nodes", func(w http.ResponseWriter, r *http.Request) {
		var n model.Node
		if !decode(w, r, &n) {
			return
		}
		id, token, err := a.svc.CreateNode(r.Context(), n)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]string{"id": id, "token": token})
	})
	route("PUT /api/v1/admin/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		var n model.Node
		if !decode(w, r, &n) {
			return
		}
		respond(w, a.svc.ReplaceNode(r.Context(), r.PathValue("id"), n))
	})
	// PATCH 只改提交了的字段，嵌套对象按字段合并（与 MCP 的 update_node 同一套规则）。
	route("PATCH /api/v1/admin/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		patch, ok := rawBody(w, r)
		if !ok {
			return
		}
		n, err := a.svc.PatchNode(r.Context(), r.PathValue("id"), patch)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, n)
	})
	route("DELETE /api/v1/admin/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, a.svc.DeleteNode(r.Context(), r.PathValue("id")))
	})
	// 拖动排序：请求体是当前可见的整份节点 id 列表，服务端按 0,1,2… 重写 order 一个字段。
	route("POST /api/v1/admin/nodes/order", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			IDs []string `json:"ids"`
		}
		if !decode(w, r, &v) {
			return
		}
		respond(w, a.svc.ReorderNodes(r.Context(), v.IDs))
	})
	route("POST /api/v1/admin/nodes/{id}/traffic", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Used  int64 `json:"used"`
			Cycle int64 `json:"cycle"`
		}
		if !decode(w, r, &v) {
			return
		}
		respond(w, a.svc.CorrectTraffic(r.Context(), r.PathValue("id"), v.Used, v.Cycle))
	})
	route("POST /api/v1/admin/nodes/{id}/token", func(w http.ResponseWriter, r *http.Request) {
		token, err := a.svc.RotateToken(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]string{"token": token})
	})

	// ---- alert rules：每条规则单独增删改，范围是全部节点 / 分组 / 指定节点。
	route("GET /api/v1/admin/alert-rules", func(w http.ResponseWriter, r *http.Request) {
		rules, err := a.svc.AlertRules(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, rules)
	})
	route("POST /api/v1/admin/alert-rules", func(w http.ResponseWriter, r *http.Request) {
		var rule model.AlertRule
		if !decode(w, r, &rule) {
			return
		}
		id, err := a.svc.CreateAlertRule(r.Context(), rule)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]string{"id": id})
	})
	route("PUT /api/v1/admin/alert-rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		var rule model.AlertRule
		if !decode(w, r, &rule) {
			return
		}
		respond(w, a.svc.ReplaceAlertRule(r.Context(), r.PathValue("id"), rule))
	})
	// PATCH 只改提交了的字段（与 MCP 的 update_alert_rule 同一套规则）：改 scope 时新范围
	// 用不上的列表没重新提交就清掉，排除列表在全部节点与分组之间切换时保留。
	route("PATCH /api/v1/admin/alert-rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		patch, ok := rawBody(w, r)
		if !ok {
			return
		}
		rule, err := a.svc.PatchAlertRule(r.Context(), r.PathValue("id"), patch)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, rule)
	})
	route("DELETE /api/v1/admin/alert-rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, a.svc.DeleteAlertRule(r.Context(), r.PathValue("id")))
	})

	// ---- tasks
	route("GET /api/v1/admin/tasks", func(w http.ResponseWriter, r *http.Request) {
		tasks, err := a.svc.Tasks(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, tasks)
	})
	route("POST /api/v1/admin/tasks", func(w http.ResponseWriter, r *http.Request) {
		var t model.Task
		if !decode(w, r, &t) {
			return
		}
		id, err := a.svc.CreateTask(r.Context(), t)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]string{"id": id})
	})
	route("PUT /api/v1/admin/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		var t model.Task
		if !decode(w, r, &t) {
			return
		}
		respond(w, a.svc.ReplaceTask(r.Context(), r.PathValue("id"), t))
	})
	route("PATCH /api/v1/admin/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		patch, ok := rawBody(w, r)
		if !ok {
			return
		}
		t, err := a.svc.PatchTask(r.Context(), r.PathValue("id"), patch)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, t)
	})
	route("DELETE /api/v1/admin/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, a.svc.DeleteTask(r.Context(), r.PathValue("id")))
	})
	// 任务的管理状态各自一个出口：启停、立即执行、绑定节点。三者都不改探测定义，所以都不清历史结果；
	// 绑定按批提交（单个任务就是一批一个），一个事务里要么全改要么都不改。
	route("PUT /api/v1/admin/tasks/{id}/enabled", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Enabled *bool `json:"enabled"`
		}
		if !decode(w, r, &v) {
			return
		}
		if v.Enabled == nil {
			write(w, 400, map[string]string{"error": "缺少 enabled"})
			return
		}
		respond(w, a.svc.SetTaskEnabled(r.Context(), r.PathValue("id"), *v.Enabled))
	})
	route("POST /api/v1/admin/tasks/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		respond(w, a.svc.RunTask(r.Context(), r.PathValue("id")))
	})
	// 两种写法：node_ids 把所选任务的绑定都换成这一份；add / remove 只增删这几个节点，
	// 各任务原有的其他绑定不动（多选任务时各自的节点列表往往不一样）。两种不能混用。
	route("PUT /api/v1/admin/tasks/bindings", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			TaskIDs []string  `json:"task_ids"`
			NodeIDs *[]string `json:"node_ids"`
			Add     []string  `json:"add"`
			Remove  []string  `json:"remove"`
		}
		if !decode(w, r, &v) {
			return
		}
		switch {
		case v.NodeIDs != nil && (v.Add != nil || v.Remove != nil):
			write(w, 400, map[string]string{"error": "node_ids 与 add / remove 只能用一种"})
		case v.NodeIDs != nil:
			respond(w, a.svc.BindTasks(r.Context(), v.TaskIDs, *v.NodeIDs))
		default:
			respond(w, a.svc.ChangeBindings(r.Context(), v.TaskIDs, v.Add, v.Remove))
		}
	})
	// 任务列表的「最近执行」一列：每个任务各目标最近一次执行的成功 / 失败 / 进行中计数。
	route("GET /api/v1/admin/tasks/health", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.svc.TaskHealth(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, out)
	})
	route("GET /api/v1/admin/tasks/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.svc.TaskRuns(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, out)
	})
	route("GET /api/v1/admin/catalog", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(catalog.JSON)
	})

	// ---- events
	// 事件保存 90 天，一页放不下：before 是上一页最后一条的 id，node_id / kind 用来筛选；
	// active=1 只列正在告警的（每条取最近一次事件）。
	route("GET /api/v1/admin/events", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		events, err := a.svc.Events(r.Context(), model.EventQuery{Before: before, Limit: limit, NodeID: q.Get("node_id"), Kind: q.Get("kind"), Active: q.Get("active") == "1"})
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, events)
	})

	// ---- settings
	// 通知出口与面板显示项是两组设置，各自一个资源：通知那套是「整份提交、空值即清空」，
	// 面板显示项是「只改传了的项」，混在一个出口里会让只想改显示项的人顺手把 Bot Token 清掉。
	route("GET /api/v1/admin/notification", func(w http.ResponseWriter, r *http.Request) {
		config, err := a.svc.Notification(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, config.Public())
	})
	route("PUT /api/v1/admin/notification", func(w http.ResponseWriter, r *http.Request) {
		var v notify.Config
		if !decode(w, r, &v) {
			return
		}
		_, err := a.svc.ReplaceNotification(r.Context(), v)
		respond(w, err)
	})
	// PATCH 只改传了的项（与 MCP 的 update_settings 同一套规则）：不传 channel 沿用当前渠道，
	// 留空的凭据沿用已存的；关闭通知要显式传 channel=none。
	route("PATCH /api/v1/admin/notification", func(w http.ResponseWriter, r *http.Request) {
		var v notify.Config
		if !decode(w, r, &v) {
			return
		}
		config, err := a.svc.PatchNotification(r.Context(), v)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, config.Public())
	})
	route("POST /api/v1/admin/notification/test", func(w http.ResponseWriter, r *http.Request) {
		_, err := a.svc.TestNotification(r.Context())
		respond(w, err)
	})
	// 数据库备份：VACUUM INTO 出一份一致的快照再整份下载，不用停服（快照是读事务，
	// 面板与 Agent 上报照常）。快照写在数据目录旁边的临时目录里，发完即删。
	route("GET /api/v1/admin/backup", func(w http.ResponseWriter, r *http.Request) {
		path, cleanup, err := a.svc.Backup(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		defer cleanup()
		f, err := os.Open(path)
		if err != nil {
			fail(w, err)
			return
		}
		defer f.Close()
		name := "tza-backup-" + time.Now().UTC().Format("20060102-150405") + ".db"
		w.Header().Set("Content-Type", "application/vnd.sqlite3")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Cache-Control", "no-store")
		if info, err := f.Stat(); err == nil {
			w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		}
		io.Copy(w, f)
	})
	route("GET /api/v1/admin/panel", func(w http.ResponseWriter, r *http.Request) {
		panel, err := a.svc.Panel(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, panel)
	})
	route("PUT /api/v1/admin/panel", func(w http.ResponseWriter, r *http.Request) {
		var v model.PanelPatch
		if !decode(w, r, &v) {
			return
		}
		_, err := a.svc.PatchPanel(r.Context(), v)
		respond(w, err)
	})
}

// rawBody reads a JSON object for a PATCH: the service merges it field by
// field, so it stays raw here (the strict decode happens after the merge).
func rawBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	if !jsonRequest(w, r) {
		return nil, false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		write(w, 400, map[string]string{"error": "请求体过大或读取失败"})
		return nil, false
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(data, &object); err != nil || object == nil {
		write(w, 400, map[string]string{"error": "请求体须为一个 JSON 对象"})
		return nil, false
	}
	return data, true
}
