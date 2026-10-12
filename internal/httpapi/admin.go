package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

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
	// 提醒规则按批设置（告警页的规则总览）：同一份规则写给所选节点，一个事务。
	route("PUT /api/v1/admin/nodes/alerts", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			NodeIDs []string     `json:"node_ids"`
			Alerts  model.Alerts `json:"alerts"`
		}
		if !decode(w, r, &v) {
			return
		}
		respond(w, a.svc.SetAlerts(r.Context(), v.NodeIDs, v.Alerts))
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
	route("PUT /api/v1/admin/tasks/bindings", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			TaskIDs []string `json:"task_ids"`
			NodeIDs []string `json:"node_ids"`
		}
		if !decode(w, r, &v) {
			return
		}
		respond(w, a.svc.BindTasks(r.Context(), v.TaskIDs, v.NodeIDs))
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
	// 事件保存 90 天，一页放不下：before 是上一页最后一条的 id，node_id / kind 用来筛选。
	route("GET /api/v1/admin/events", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		events, err := a.svc.Events(r.Context(), model.EventQuery{Before: before, Limit: limit, NodeID: q.Get("node_id"), Kind: q.Get("kind")})
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
	route("POST /api/v1/admin/notification/test", func(w http.ResponseWriter, r *http.Request) {
		_, err := a.svc.TestNotification(r.Context())
		respond(w, err)
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
