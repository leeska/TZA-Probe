// Package service is the management surface both adapters share: the HTTP
// panel API and the MCP tools call the same operations here, so validation,
// partial-update merging, clamping and the shape of every error are decided
// once. Adapters only decode requests and translate errors.
//
// Errors carry their kind (model.ErrInvalid / ErrNotFound / ErrConflict, see
// model.Invalid / NotFound / Conflict) and a message the caller can show as is.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/leeska/TZA-Probe/internal/catalog"
	"github.com/leeska/TZA-Probe/internal/model"
	"github.com/leeska/TZA-Probe/internal/notify"
)

// Store is the persistence port the management operations need.
type Store interface {
	Nodes(context.Context, time.Time) ([]model.NodeView, error)
	SaveNode(context.Context, model.Node, bool) (string, string, error)
	SaveNodeOrder(context.Context, []string) error
	DeleteNode(context.Context, string) error
	Rotate(context.Context, string) (string, error)
	CorrectTraffic(context.Context, string, int64, int64, time.Time) error
	SetNodeAlerts(context.Context, []string, model.Alerts) error

	Tasks(context.Context) ([]model.Task, error)
	SaveTask(context.Context, model.Task, bool) (string, error)
	DeleteTask(context.Context, string) error
	SetTaskEnabled(context.Context, string, bool) error
	RunTask(context.Context, string, time.Time) error
	BindTasks(context.Context, []string, []string) error
	ChangeBindings(context.Context, []string, []string, []string) error
	Backup(context.Context) (string, func(), error)
	TaskRuns(context.Context, string, time.Time) ([]model.TaskRun, error)
	TaskHealth(context.Context, time.Time) (map[string]model.TaskHealth, error)

	History(context.Context, string, int, time.Time) ([]model.Sample, error)
	NetworkHistory(context.Context, string, int, time.Time) ([]model.NetworkSeries, error)
	Observations(context.Context, string, time.Time) ([]model.Observation, error)
	RecentLatencySamples(context.Context, []string, int, time.Time) ([]model.LatencySample, error)
	Events(context.Context, model.EventQuery) ([]model.Event, error)

	Setting(context.Context, string) (string, error)
	SetSetting(context.Context, string, string) error
}

// Service runs the management operations. Notifier may be nil; then only
// TestNotification fails (ErrNoSender).
type Service struct {
	Store    Store
	Notifier notify.Sender
}

// ErrNoSender means this Server was started without a notification sender.
var ErrNoSender = errors.New("通知发送组件不可用")

// SendError is a test notification the channel refused; the message is the
// channel's own reason.
type SendError struct{ Err error }

func (e SendError) Error() string { return "测试通知发送失败：" + e.Err.Error() }

func (e SendError) Unwrap() error { return e.Err }

// ---------------------------------------------------------------- nodes

func (s *Service) Nodes(ctx context.Context) ([]model.NodeView, error) {
	return s.Store.Nodes(ctx, time.Now())
}

// Node resolves one node; a missing id is a NotFound naming it.
func (s *Service) Node(ctx context.Context, id string) (model.NodeView, error) {
	if id == "" {
		return model.NodeView{}, model.Invalid("缺少节点 id")
	}
	nodes, err := s.Store.Nodes(ctx, time.Now())
	if err != nil {
		return model.NodeView{}, err
	}
	for _, n := range nodes {
		if n.ID == id {
			return n, nil
		}
	}
	return model.NodeView{}, model.NotFound("节点不存在：" + id)
}

// CreateNode stores a new node and returns its id and one-time Agent key.
func (s *Service) CreateNode(ctx context.Context, n model.Node) (string, string, error) {
	n.ID = ""
	n = n.Normalized()
	if err := n.Validate(); err != nil {
		return "", "", model.Invalid(err.Error())
	}
	if err := s.checkRegions(ctx, n.Display, model.NodeDisplay{}); err != nil {
		return "", "", err
	}
	return s.Store.SaveNode(ctx, n, true)
}

// ReplaceNode stores the whole submitted configuration (the panel form).
func (s *Service) ReplaceNode(ctx context.Context, id string, n model.Node) error {
	n.ID = id
	n = n.Normalized()
	if err := n.Validate(); err != nil {
		return model.Invalid(err.Error())
	}
	current, err := s.Node(ctx, id)
	if err != nil {
		return err
	}
	if err = s.checkRegions(ctx, n.Display, current.Display); err != nil {
		return err
	}
	_, _, err = s.Store.SaveNode(ctx, n, false)
	return err
}

// checkRegions rejects a card region nothing can ever match. The card shows a
// result only when the node's region equals a task target's region letter for
// letter, so 「上海市」 or a typo silently leaves the card empty. A region is
// known when the built-in catalogue or some task's targets use it. A value the
// node already had is let through, so an older node stays editable.
func (s *Service) checkRegions(ctx context.Context, next, previous model.NodeDisplay) error {
	changed := []struct{ label, value, before string }{
		{"卡片回程地区", next.RouteRegion, previous.RouteRegion},
		{"卡片延迟地区", next.LatencyRegion, previous.LatencyRegion},
	}
	var known map[string]bool
	for _, c := range changed {
		if c.value == "" || c.value == c.before {
			continue
		}
		if known == nil {
			var err error
			if known, err = s.KnownRegions(ctx); err != nil {
				return err
			}
		}
		if !known[c.value] {
			return model.Invalid(fmt.Sprintf("%s「%s」不在内置目录或任何任务的目标地区里，卡片不会有结果；请从候选里选，或先建好这个地区的任务", c.label, c.value))
		}
	}
	return nil
}

// KnownRegions is every region a card can match: the built-in catalogue's
// target regions plus the regions of every task's targets.
func (s *Service) KnownRegions(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	for _, region := range catalogRegions() {
		out[region] = true
	}
	tasks, err := s.Store.Tasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.Probe == nil {
			continue
		}
		for _, target := range t.Probe.Targets {
			if target.Region != "" {
				out[target.Region] = true
			}
		}
	}
	return out, nil
}

// catalogRegions reads the regions of the embedded catalogue once.
var catalogRegions = sync.OnceValue(func() []string {
	var snapshot struct {
		Targets []struct {
			Region string `json:"region"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(catalog.JSON, &snapshot); err != nil {
		return nil
	}
	out := []string{}
	for _, t := range snapshot.Targets {
		out = append(out, t.Region)
	}
	return out
})

// PatchNode overlays only the submitted fields onto the stored node; nested
// objects merge field by field, so a partial edit cannot blank anything.
func (s *Service) PatchNode(ctx context.Context, id string, patch json.RawMessage) (model.Node, error) {
	current, err := s.Node(ctx, id)
	if err != nil {
		return model.Node{}, err
	}
	merged, err := Merge(current.Node, patch)
	if err != nil {
		return model.Node{}, model.Invalid("参数无法合并到当前节点：" + err.Error())
	}
	return merged, s.ReplaceNode(ctx, current.ID, merged)
}

func (s *Service) DeleteNode(ctx context.Context, id string) error {
	return s.Store.DeleteNode(ctx, id)
}

// RotateToken revokes the node's key and returns a new one.
func (s *Service) RotateToken(ctx context.Context, id string) (string, error) {
	return s.Store.Rotate(ctx, id)
}

// CorrectTraffic sets this cycle's used volume; cycle must be the node's
// current cycle_start, otherwise the cycle moved on and the call conflicts.
func (s *Service) CorrectTraffic(ctx context.Context, id string, used, cycle int64) error {
	if used < 0 {
		return model.Invalid("已用流量不能为负")
	}
	return s.Store.CorrectTraffic(ctx, id, used, cycle, time.Now())
}

// ReorderNodes takes the whole visible id list in its new order.
func (s *Service) ReorderNodes(ctx context.Context, ids []string) error {
	if len(ids) == 0 || len(ids) > 500 {
		return model.Invalid("节点数量须在 1—500 之间")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return model.Invalid("节点 id 不能为空或重复")
		}
		seen[id] = true
	}
	return s.Store.SaveNodeOrder(ctx, ids)
}

// SetAlerts writes one rule set to every listed node in one transaction.
func (s *Service) SetAlerts(ctx context.Context, ids []string, alerts model.Alerts) error {
	if len(ids) > 500 {
		return model.Invalid("一次最多 500 个节点")
	}
	return s.Store.SetNodeAlerts(ctx, ids, alerts)
}

// ---------------------------------------------------------------- tasks

func (s *Service) Tasks(ctx context.Context) ([]model.Task, error) {
	return s.Store.Tasks(ctx)
}

// Task resolves one task; a missing id is a NotFound naming it.
func (s *Service) Task(ctx context.Context, id string) (model.Task, error) {
	if id == "" {
		return model.Task{}, model.Invalid("缺少任务 id")
	}
	tasks, err := s.Store.Tasks(ctx)
	if err != nil {
		return model.Task{}, err
	}
	for _, t := range tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return model.Task{}, model.NotFound("任务不存在：" + id)
}

func (s *Service) CreateTask(ctx context.Context, t model.Task) (string, error) {
	t.ID = ""
	if err := t.Validate(); err != nil {
		return "", model.Invalid(err.Error())
	}
	return s.Store.SaveTask(ctx, t, true)
}

// ReplaceTask stores the whole submitted definition. Changing what is probed
// clears the task's earlier results (see storage.SaveTask).
func (s *Service) ReplaceTask(ctx context.Context, id string, t model.Task) error {
	t.ID = id
	if err := t.Validate(); err != nil {
		return model.Invalid(err.Error())
	}
	_, err := s.Store.SaveTask(ctx, t, false)
	return err
}

// PatchTask overlays only the submitted fields onto the stored task.
func (s *Service) PatchTask(ctx context.Context, id string, patch json.RawMessage) (model.Task, error) {
	current, err := s.Task(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	merged, err := Merge(current, patch)
	if err != nil {
		return model.Task{}, model.Invalid("参数无法合并到当前任务：" + err.Error())
	}
	return merged, s.ReplaceTask(ctx, current.ID, merged)
}

func (s *Service) DeleteTask(ctx context.Context, id string) error {
	return s.Store.DeleteTask(ctx, id)
}

func (s *Service) SetTaskEnabled(ctx context.Context, id string, enabled bool) error {
	return s.Store.SetTaskEnabled(ctx, id, enabled)
}

func (s *Service) RunTask(ctx context.Context, id string) error {
	return s.Store.RunTask(ctx, id, time.Now())
}

// BindTasks gives every listed task the same node list, all or nothing.
func (s *Service) BindTasks(ctx context.Context, taskIDs, nodeIDs []string) error {
	if len(taskIDs) > 500 {
		return model.Invalid("一次最多 500 个任务")
	}
	return s.Store.BindTasks(ctx, taskIDs, nodeIDs)
}

// Backup snapshots the database for download; the caller removes the file
// with the returned cleanup once it has been sent.
func (s *Service) Backup(ctx context.Context) (string, func(), error) {
	return s.Store.Backup(ctx)
}

// ChangeBindings adds and removes nodes on every listed task, keeping each
// task's other bindings, all or nothing.
func (s *Service) ChangeBindings(ctx context.Context, taskIDs, add, remove []string) error {
	if len(taskIDs) > 500 {
		return model.Invalid("一次最多 500 个任务")
	}
	if len(add) == 0 && len(remove) == 0 {
		return model.Invalid("没有要添加或移除的节点")
	}
	return s.Store.ChangeBindings(ctx, taskIDs, add, remove)
}

func (s *Service) TaskRuns(ctx context.Context, id string) ([]model.TaskRun, error) {
	return s.Store.TaskRuns(ctx, id, time.Now())
}

// TaskHealth is TaskRuns summed per task, for the task list.
func (s *Service) TaskHealth(ctx context.Context) (map[string]model.TaskHealth, error) {
	return s.Store.TaskHealth(ctx, time.Now())
}

// ---------------------------------------------------------------- reads

// Window clamps a look-back the way every read does: 0 means the default,
// anything else is clamped to 1..highest.
func Window(value, fallback, highest int) int {
	if value == 0 {
		return fallback
	}
	return min(max(value, 1), highest)
}

const (
	DefaultHistoryHours    = 24
	MaxHistoryHours        = 720
	MaxNetworkHistoryHours = 168
	DefaultLatencySamples  = 20
	MaxLatencySamples      = 60
)

// History returns the host samples and the window actually read.
func (s *Service) History(ctx context.Context, id string, hours int) ([]model.Sample, int, error) {
	hours = Window(hours, DefaultHistoryHours, MaxHistoryHours)
	samples, err := s.Store.History(ctx, id, hours, time.Now())
	return samples, hours, err
}

// NetworkHistory returns the per-target series and the window actually read.
func (s *Service) NetworkHistory(ctx context.Context, id string, hours int) ([]model.NetworkSeries, int, error) {
	hours = Window(hours, DefaultHistoryHours, MaxNetworkHistoryHours)
	series, err := s.Store.NetworkHistory(ctx, id, hours, time.Now())
	return series, hours, err
}

// Observations is the newest result per probe, redacted for display: bindings
// drop out, hop addresses keep only their prefix, stale routes read 未知.
func (s *Service) Observations(ctx context.Context, id string) ([]model.Observation, error) {
	list, err := s.Store.Observations(ctx, id, time.Now())
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i] = list[i].Redact()
	}
	return list, nil
}

// LatencySamples reads the newest TCP results per group within the panel's
// latency window and returns the per-group count and window it used.
func (s *Service) LatencySamples(ctx context.Context, ids []string, limit int) ([]model.LatencySample, int, int, error) {
	limit = Window(limit, DefaultLatencySamples, MaxLatencySamples)
	panel, err := s.Panel(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	since := time.Now().Add(-time.Duration(panel.LatencyWindowMinutes) * time.Minute)
	samples, err := s.Store.RecentLatencySamples(ctx, ids, limit, since)
	return samples, limit, panel.LatencyWindowMinutes, err
}

func (s *Service) Events(ctx context.Context, q model.EventQuery) ([]model.Event, error) {
	return s.Store.Events(ctx, q.Normalize())
}

// ---------------------------------------------------------------- settings

// Panel reads the visitor-facing display settings, normalized: an unset or
// hand-edited value falls back to the built-in default.
func (s *Service) Panel(ctx context.Context) (model.PanelSettings, error) {
	window, err := s.Store.Setting(ctx, model.SettingLatencyWindowMinutes)
	if err != nil {
		return model.PanelSettings{}, err
	}
	theme, err := s.Store.Setting(ctx, model.SettingDefaultTheme)
	if err != nil {
		return model.PanelSettings{}, err
	}
	skin, err := s.Store.Setting(ctx, model.SettingDefaultSkin)
	if err != nil {
		return model.PanelSettings{}, err
	}
	return model.PanelSettings{
		LatencyWindowMinutes: model.LatencyWindowMinutes(window),
		DefaultTheme:         model.PanelTheme(theme),
		DefaultSkin:          model.PanelSkin(skin),
	}, nil
}

// PatchPanel changes only the fields the patch carries.
func (s *Service) PatchPanel(ctx context.Context, patch model.PanelPatch) (model.PanelSettings, error) {
	if patch.Empty() {
		return model.PanelSettings{}, model.Invalid("至少给一项面板显示设置")
	}
	if err := patch.Validate(); err != nil {
		return model.PanelSettings{}, model.Invalid(err.Error())
	}
	if err := s.writePanel(ctx, patch); err != nil {
		return model.PanelSettings{}, err
	}
	return s.Panel(ctx)
}

func (s *Service) writePanel(ctx context.Context, patch model.PanelPatch) error {
	if patch.LatencyWindowMinutes != nil {
		if err := s.Store.SetSetting(ctx, model.SettingLatencyWindowMinutes, strconv.Itoa(*patch.LatencyWindowMinutes)); err != nil {
			return err
		}
	}
	if patch.DefaultTheme != nil {
		if err := s.Store.SetSetting(ctx, model.SettingDefaultTheme, *patch.DefaultTheme); err != nil {
			return err
		}
	}
	if patch.DefaultSkin != nil {
		if err := s.Store.SetSetting(ctx, model.SettingDefaultSkin, *patch.DefaultSkin); err != nil {
			return err
		}
	}
	return nil
}

// UpdateSettings is the combined partial update the MCP tool offers: a nil
// notification leaves the channel alone. Everything is validated before
// anything is written, so a bad panel value cannot leave the channel changed.
func (s *Service) UpdateSettings(ctx context.Context, notification *notify.Config, panel model.PanelPatch) (notify.Config, model.PanelSettings, error) {
	if notification == nil && panel.Empty() {
		return notify.Config{}, model.PanelSettings{}, model.Invalid("至少给一项设置；关闭通知请传 channel=none")
	}
	if err := panel.Validate(); err != nil {
		return notify.Config{}, model.PanelSettings{}, model.Invalid(err.Error())
	}
	config, err := s.Notification(ctx)
	if err != nil {
		return notify.Config{}, model.PanelSettings{}, err
	}
	if notification != nil {
		if config, err = s.resolveNotificationPatch(ctx, *notification); err != nil {
			return notify.Config{}, model.PanelSettings{}, err
		}
		if err = s.saveNotification(ctx, config); err != nil {
			return notify.Config{}, model.PanelSettings{}, err
		}
	}
	if err = s.writePanel(ctx, panel); err != nil {
		return notify.Config{}, model.PanelSettings{}, err
	}
	view, err := s.Panel(ctx)
	return config, view, err
}

// ---------------------------------------------------------------- merge

// Merge overlays the fields a caller actually sent onto the stored value: what
// was submitted changes, a field left out keeps its current value, and a
// nested object (alerts, …) merges field by field instead of being replaced.
// Identity keys are never taken from the patch. The result is decoded strictly,
// so a misspelled field fails loudly.
func Merge[T any](current T, submitted json.RawMessage) (T, error) {
	var out T
	data, err := json.Marshal(current)
	if err != nil {
		return out, err
	}
	base := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &base); err != nil {
		return out, err
	}
	patch := map[string]json.RawMessage{}
	if len(submitted) > 0 {
		if err := json.Unmarshal(submitted, &patch); err != nil {
			return out, err
		}
	}
	for _, key := range []string{"id", "node_id", "task_id"} {
		delete(patch, key)
	}
	merged, err := mergeObjects(base, patch)
	if err != nil {
		return out, err
	}
	data, err = json.Marshal(merged)
	if err != nil {
		return out, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return out, decoder.Decode(&out)
}

func mergeObjects(base, patch map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	for key, value := range patch {
		if existing, ok := base[key]; ok {
			var left, right map[string]json.RawMessage
			if json.Unmarshal(existing, &left) == nil && json.Unmarshal(value, &right) == nil && left != nil && right != nil {
				merged, err := mergeObjects(left, right)
				if err != nil {
					return nil, err
				}
				data, err := json.Marshal(merged)
				if err != nil {
					return nil, err
				}
				base[key] = data
				continue
			}
		}
		base[key] = value
	}
	return base, nil
}
