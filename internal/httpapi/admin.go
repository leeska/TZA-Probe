package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/account"
	"github.com/leeska/TZA-Probe/internal/brand"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
	"github.com/leeska/TZA-Probe/internal/service"
	"github.com/leeska/TZA-Probe/internal/theme"
	"github.com/leeska/TZA-Probe/pkg/tzapkg"
)

// themeView is one skin as the browser needs it: installed packages carry the
// URLs of their stylesheets (with the package revision, so a re-upload busts caches).
type themeView struct {
	theme.Manifest
	CSS []string `json:"css,omitempty"`
	// Options are the administrator's values for the theme's settings menu.
	Options map[string]any `json:"options,omitempty"`
}

func themeList(store *theme.Store, options map[string]map[string]any) []themeView {
	out := []themeView{}
	for _, m := range store.List() {
		v := themeView{Manifest: m}
		if len(m.Settings) > 0 {
			v.Options = options[m.ID]
		}
		for _, style := range m.Styles {
			v.CSS = append(v.CSS, "/themes/"+m.ID+"/"+style+"?v="+m.Rev)
		}
		out = append(out, v)
	}
	return out
}

// adminRoutes is the management API. Every route decodes its request and hands
// it to the service, which the MCP tools share; nothing here decides a rule.
func (a *API) adminRoutes(mux *http.ServeMux) {
	route := func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, a.admin(handler)) }

	// ---- themes：内置皮肤 + 后台上传的主题包（zip，见 docs/THEMES.md）
	route("GET /api/v1/admin/themes", func(w http.ResponseWriter, r *http.Request) {
		options, err := a.svc.SkinOptions(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, themeList(a.Themes, options))
	})
	// 主题设置：主题在 theme.json 里声明的菜单（附加包的开关也在这里），对所有访客生效。
	route("PUT /api/v1/admin/themes/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		var patch map[string]json.RawMessage
		if !decode(w, r, &patch) {
			return
		}
		id := r.PathValue("id")
		var settings []theme.Setting
		found := false
		for _, m := range a.Themes.List() {
			if m.ID == id && m.Type != theme.TypeAddon {
				settings, found = m.Settings, true
			}
		}
		if !found {
			write(w, 404, map[string]string{"error": "没有这个主题"})
			return
		}
		values, err := a.svc.SetSkinOptions(r.Context(), id, settings, patch)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]any{"options": values})
	})
	route("POST /api/v1/admin/themes", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, theme.MaxUpload))
		if err != nil {
			write(w, 413, map[string]string{"error": fmt.Sprintf("主题包最大 %d MB", theme.MaxUpload>>20)})
			return
		}
		m, err := a.Themes.Install(body)
		if err != nil {
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		write(w, 200, m)
	})
	route("DELETE /api/v1/admin/themes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Themes.Remove(r.PathValue("id")); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				write(w, 404, map[string]string{"error": "没有这个主题包"})
				return
			}
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(204)
	})

	// ---- plugins：声明式插件（通知渠道 / 探测目标集，见 docs/PLUGINS.md）
	route("GET /api/v1/admin/plugins", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, a.svc.PluginList())
	})
	// 上传前先看一眼：只校验、列出它能做的事，不落盘；面板据此请管理员确认。
	route("POST /api/v1/admin/plugins/inspect", func(w http.ResponseWriter, r *http.Request) {
		body, ok := zipBody(w, r)
		if !ok {
			return
		}
		p, err := tzapkg.OpenPlugin(body)
		if err != nil {
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		write(w, 200, service.ViewPlugin(p))
	})
	route("POST /api/v1/admin/plugins", func(w http.ResponseWriter, r *http.Request) {
		body, ok := zipBody(w, r)
		if !ok {
			return
		}
		p, err := a.Plugins.Install(body)
		if err != nil {
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		write(w, 200, service.ViewPlugin(p))
	})
	route("DELETE /api/v1/admin/plugins/{id}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, a.svc.RemovePlugin(r.Context(), r.PathValue("id")))
	})

	// ---- market：TZA-CC 的主题市场（目录、安装、更新、实例绑定、预览图代理），见 market.go
	a.marketRoutes(route)

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
	// 批量修改：同一份部分字段（分组、地区、公开…）合并到勾选的每台节点上，全成或全不成。
	route("POST /api/v1/admin/nodes/batch", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			IDs   []string        `json:"ids"`
			Patch json.RawMessage `json:"patch"`
		}
		if !decode(w, r, &v) {
			return
		}
		count, err := a.svc.BatchPatchNodes(r.Context(), v.IDs, v.Patch)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]int{"updated": count})
	})
	// 分组改名 / 合并 / 解散（to 为空即回到未分组），作用于这个分组的提醒规则一起改。
	route("POST /api/v1/admin/groups/rename", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if !decode(w, r, &v) {
			return
		}
		count, err := a.svc.RenameGroup(r.Context(), v.From, v.To)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]int{"moved": count})
	})
	a.upgradeRoutes(route)
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
		// 内置目录 + 已安装的目标集插件（插件目标带 plugin / plugin_name，id 加了 plugin: 前缀）。
		out, err := a.svc.Catalog()
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, out)
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
		write(w, 200, a.svc.PublicNotification(config))
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
		write(w, 200, a.svc.PublicNotification(config))
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
	// ---- 后台账号：登录用的用户名与密码。改动要带当前密码；成功后其他浏览器会话全部失效。
	route("GET /api/v1/admin/account", func(w http.ResponseWriter, r *http.Request) {
		if a.AccountDir == "" {
			write(w, 404, map[string]string{"error": "未启用后台账号"})
			return
		}
		acct, err := account.Load(a.AccountDir)
		if errors.Is(err, account.ErrNotFound) {
			write(w, 200, map[string]any{"username": "", "configured": false})
			return
		}
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]any{"username": acct.Username, "configured": true})
	})
	route("PUT /api/v1/admin/account", func(w http.ResponseWriter, r *http.Request) {
		if a.AccountDir == "" {
			write(w, 404, map[string]string{"error": "未启用后台账号"})
			return
		}
		var v struct {
			CurrentPassword string `json:"current_password"`
			Username        string `json:"username"`
			Password        string `json:"password"`
		}
		if !decode(w, r, &v) {
			return
		}
		username := strings.TrimSpace(v.Username)
		if err := account.Validate(username, v.Password); err != nil {
			write(w, 400, map[string]string{"error": err.Error()})
			return
		}
		acct, err := account.Load(a.AccountDir)
		switch {
		case errors.Is(err, account.ErrNotFound):
			// 还没有账号（例如数据目录是旧版本留下的）：已登录的管理员直接设一个。
		case err != nil:
			fail(w, err)
			return
		default:
			now := time.Now()
			if a.loginLimited(now, false) {
				write(w, 429, map[string]string{"error": "尝试过于频繁，请稍后重试"})
				return
			}
			if !acct.Check(acct.Username, v.CurrentPassword) {
				a.loginLimited(now, true)
				write(w, 400, map[string]string{"error": "当前密码不正确"})
				return
			}
		}
		if err := account.Save(a.AccountDir, username, v.Password); err != nil {
			fail(w, err)
			return
		}
		a.dropOtherSessions(r)
		write(w, 200, map[string]any{"username": username, "configured": true})
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
	// 站点 logo：请求体就是图片本身（不走 JSON）。类型由内容判断，不看 Content-Type。
	route("PUT /api/v1/admin/panel/logo", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, brand.MaxLogo))
		if err != nil {
			write(w, 413, map[string]string{"error": fmt.Sprintf("logo 最大 %d KB", brand.MaxLogo>>10)})
			return
		}
		if err := a.Logo.Save(body); err != nil {
			if errors.Is(err, brand.ErrInvalid) {
				write(w, 400, map[string]string{"error": err.Error()})
				return
			}
			fail(w, err)
			return
		}
		write(w, 200, map[string]string{"site_logo": a.Logo.URL()})
	})
	route("DELETE /api/v1/admin/panel/logo", func(w http.ResponseWriter, r *http.Request) {
		respond(w, a.Logo.Remove())
	})
}

// zipBody reads an uploaded package (the zip itself is the body).
func zipBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, theme.MaxUpload))
	if err != nil {
		write(w, 413, map[string]string{"error": fmt.Sprintf("压缩包最大 %d MB", theme.MaxUpload>>20)})
		return nil, false
	}
	return body, true
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

// dropOtherSessions signs out every browser except the one making r, after
// the password changed.
func (a *API) dropOtherSessions(r *http.Request) {
	keep := ""
	if c, err := r.Cookie("tza_session"); err == nil {
		keep = model.Hash(c.Value)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for key := range a.sessions {
		if key != keep {
			delete(a.sessions, key)
		}
	}
}
