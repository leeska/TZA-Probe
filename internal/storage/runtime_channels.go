package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/leeska/TZA-Probe/internal/agentsig"
	runtimepkg "github.com/leeska/TZA-Probe/internal/executorruntime"
	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
)

const runtimeChannelSchema = `
CREATE TABLE IF NOT EXISTS runtime_channels(
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 enrollment TEXT NOT NULL,seen_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS runtime_requests(
 id TEXT PRIMARY KEY,node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 state TEXT NOT NULL,payload TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS runtime_requests_node ON runtime_requests(node_id,created_at);
CREATE UNIQUE INDEX IF NOT EXISTS runtime_requests_active ON runtime_requests(node_id)
 WHERE state IN ('requested','awaiting_approval','approved','running');
`

const runtimeMaintenanceSchema = `
CREATE TABLE IF NOT EXISTS runtime_maintenance(
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 request_id TEXT NOT NULL, created_at INTEGER NOT NULL);
`

func runtimeMaintenanceTx(ctx context.Context, tx *sql.Tx, node string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_maintenance WHERE node_id=?", node).Scan(&count)
	return count != 0, err
}

func releaseRuntimeMaintenanceTx(ctx context.Context, tx *sql.Tx, node string, binding ipsentinel.Binding) (bool, error) {
	held, err := runtimeMaintenanceTx(ctx, tx, node)
	if err != nil || !held {
		return held, err
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT r.payload FROM runtime_requests r JOIN runtime_maintenance m ON m.node_id=r.node_id
 WHERE r.node_id=? AND r.state='started_paused' AND r.rowid>(SELECT rowid FROM runtime_requests WHERE id=m.request_id)
 ORDER BY r.rowid DESC LIMIT 1`, node).Scan(&raw)
	if err != nil {
		return true, model.Conflict("升级维护尚未取得启动验收回执，不能重新授权或恢复派发")
	}
	var request runtimepkg.ChannelRequest
	if json.Unmarshal([]byte(raw), &request) != nil || request.LifecycleReceipt == nil {
		return true, model.Conflict("启动验收回执无效")
	}
	var changed int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_requests WHERE node_id=? AND (
 state IN ('requested','awaiting_approval','approved','running') OR
 (rowid>(SELECT rowid FROM runtime_requests WHERE id=?) AND json_extract(payload,'$.action') IN ('select','drain','activate')
 AND state IN ('selected','drained','started_paused','unknown')))`, node, request.ID).Scan(&changed); err != nil {
		return true, err
	}
	if changed != 0 {
		return true, model.Conflict("启动验收后存在新的运行时操作，须先完成并重新检查当前状态")
	}
	enrollment, seen, err := runtimeChannelTx(ctx, tx, node)
	if err != nil {
		return true, err
	}
	hello := request.LifecycleReceipt.Hello
	expected := ipsentinel.Binding{
		Target: request.Enrollment.Target, ExecutorInstanceID: hello.Status.ExecutorInstanceID,
		Epoch: hello.Status.Control.Epoch, DescriptorSHA256: hello.DescriptorSHA256, Mode: ipsentinel.ModeManaged,
	}
	if binding != expected || request.Enrollment != enrollment || time.Now().Unix()-seen > 45 ||
		time.Now().Unix()-request.UpdatedAt > 900 {
		return true, model.Conflict("请按当前启动验收回执重新授权实例、epoch、descriptor 和目标；过期回执须重新验收")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM runtime_maintenance WHERE node_id=?", node); err != nil {
		return true, err
	}
	return true, nil
}

func runtimeChannelTx(ctx context.Context, tx *sql.Tx, node string) (runtimepkg.Enrollment, int64, error) {
	var raw string
	var seen int64
	var enrollment runtimepkg.Enrollment
	err := tx.QueryRowContext(ctx, "SELECT enrollment,seen_at FROM runtime_channels WHERE node_id=?", node).Scan(&raw, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return enrollment, 0, model.NotFound("节点尚未启用 runtime 安装通道")
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &enrollment)
	}
	return enrollment, seen, err
}

func runtimeRequestTx(ctx context.Context, tx *sql.Tx, node, id string) (runtimepkg.ChannelRequest, error) {
	var raw string
	var request runtimepkg.ChannelRequest
	err := tx.QueryRowContext(ctx, "SELECT payload FROM runtime_requests WHERE node_id=? AND id=?", node, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return request, model.NotFound("安装请求不存在")
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &request)
	}
	return request, err
}

func saveRuntimeRequest(ctx context.Context, tx *sql.Tx, node string, request runtimepkg.ChannelRequest) error {
	_, err := tx.ExecContext(ctx, `UPDATE runtime_requests SET state=?,payload=? WHERE node_id=? AND id=?`, request.State, encode(request), node, request.ID)
	return err
}

func nodeConfigTx(ctx context.Context, tx *sql.Tx, node string) (model.Node, error) {
	var config string
	if err := tx.QueryRowContext(ctx, "SELECT config FROM nodes WHERE id=?", node).Scan(&config); err != nil {
		return model.Node{}, err
	}
	var n model.Node
	return n, json.Unmarshal([]byte(config), &n)
}

func (s *Store) RuntimeChannels(ctx context.Context) ([]runtimepkg.ChannelView, error) {
	rows, err := s.query(ctx, "SELECT node_id,enrollment,seen_at FROM runtime_channels ORDER BY node_id")
	if err != nil {
		return nil, err
	}
	views := []runtimepkg.ChannelView{}
	for rows.Next() {
		var view runtimepkg.ChannelView
		var raw string
		if err := rows.Scan(&view.NodeID, &raw, &view.SeenAt); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &view.Enrollment); err != nil {
			rows.Close()
			return nil, err
		}
		views = append(views, view)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for index := range views {
		view := &views[index]
		if err := s.queryRow(ctx, "SELECT EXISTS(SELECT 1 FROM runtime_maintenance WHERE node_id=?)", view.NodeID).Scan(&view.Maintenance); err != nil {
			return nil, err
		}
		view.Requests = []runtimepkg.ChannelRequest{}
		rows, err := s.query(ctx, "SELECT payload FROM runtime_requests WHERE node_id=? ORDER BY created_at DESC,rowid DESC LIMIT 50", view.NodeID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw string
			var request runtimepkg.ChannelRequest
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, err
			}
			if err := json.Unmarshal([]byte(raw), &request); err != nil {
				rows.Close()
				return nil, err
			}
			view.Requests = append(view.Requests, request)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return views, nil
}

func (s *Store) CreateRuntimeRequest(ctx context.Context, node string, input runtimepkg.ChannelInput) (runtimepkg.ChannelRequest, error) {
	return s.createRuntimeRequest(ctx, node, input, "")
}

func (s *Store) CreateMarketRuntimeRequest(ctx context.Context, node, version, scope string) (runtimepkg.ChannelRequest, error) {
	if len(scope) != 64 {
		return runtimepkg.ChannelRequest{}, model.Invalid("请重新选择节点安装身份")
	}
	return s.createRuntimeRequest(ctx, node, runtimepkg.ChannelInput{Action: "stage", Version: version}, scope)
}

func (s *Store) createRuntimeRequest(ctx context.Context, node string, input runtimepkg.ChannelInput, scope string) (runtimepkg.ChannelRequest, error) {
	if input.Validate() != nil {
		return runtimepkg.ChannelRequest{}, model.Invalid("请指定有效组件操作、精确版本及对应暂存请求")
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return runtimepkg.ChannelRequest{}, err
	}
	defer tx.Rollback()
	enrollment, seen, err := runtimeChannelTx(ctx, tx, node)
	if err != nil {
		return runtimepkg.ChannelRequest{}, err
	}
	if err := requireRuntimeAllowedTx(ctx, tx, node); err != nil {
		return runtimepkg.ChannelRequest{}, err
	}
	if scope != "" && enrollment.ScopeSHA256 != scope {
		return runtimepkg.ChannelRequest{}, model.Conflict("节点安装配置已变化，请刷新后重新选择")
	}
	now := time.Now().Unix()
	if now-seen > 45 {
		return runtimepkg.ChannelRequest{}, model.Conflict("节点安装通道状态过期，等待 Agent 上报")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_requests WHERE node_id=? AND state IN ('requested','awaiting_approval','approved','running')`, node).Scan(&active); err != nil {
		return runtimepkg.ChannelRequest{}, err
	}
	if active != 0 {
		return runtimepkg.ChannelRequest{}, model.Conflict("节点已有未完成的安装请求")
	}
	var uncertain int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_requests WHERE node_id=? AND state='unknown'", node).Scan(&uncertain); err != nil {
		return runtimepkg.ChannelRequest{}, err
	}
	if uncertain != 0 {
		return runtimepkg.ChannelRequest{}, model.Conflict("Agent 正在核对上次操作结果，完成前不能重复安装")
	}
	request := runtimepkg.ChannelRequest{ID: ipsentinel.NewUUID(), ChannelInput: input, Enrollment: enrollment, State: "requested", CreatedAt: now, UpdatedAt: now}
	if input.Action != "stage" {
		source, err := runtimeRequestTx(ctx, tx, node, input.StageRequestID)
		if err != nil {
			return request, err
		}
		if source.Action != "stage" || source.State != "staged" || source.Receipt == nil || source.Version != input.Version || source.Enrollment != enrollment {
			return request, model.Conflict("选择当前节点与配置范围下已完成的同版本暂存请求")
		}
		request.StagePlanSHA256 = source.PlanSHA256
		if input.Action == "enable" {
			if agentsig.Newer("1.10.7", enrollment.Host.AgentVersion) {
				return request, model.Conflict("请先升级 Agent，再启用插件")
			}
			var bound int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM executor_bindings WHERE node_id=?", node).Scan(&bound); err != nil {
				return request, err
			}
			held, err := runtimeMaintenanceTx(ctx, tx, node)
			if err != nil {
				return request, err
			}
			if bound != 0 || held {
				return request, model.Conflict("已有插件接入或维护记录，不能重复首次启用")
			}
		}
	}
	if input.Action == "activate" {
		held, err := runtimeMaintenanceTx(ctx, tx, node)
		if err != nil {
			return request, err
		}
		if !held {
			return request, model.Conflict("启动验收前须先建立升级维护暂停并完成排空")
		}
	}
	if input.Action == "drain" {
		binding, hello, _, err := executorBindingTx(ctx, tx, node)
		if err != nil {
			return request, err
		}
		if err := validateManagedHello(binding, hello, time.Now()); err != nil {
			return request, err
		}
		if binding.Target != enrollment.Target {
			return request, model.Conflict("安装身份与托管执行器目标不一致")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_maintenance(node_id,request_id,created_at) VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET request_id=excluded.request_id,created_at=excluded.created_at`, node, request.ID, now); err != nil {
			return request, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE executor_bindings SET dispatch_paused=1 WHERE node_id=?", node); err != nil {
			return request, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE executor_actions SET status='cancelled',updated_at=? WHERE node_id=? AND status='pending' AND attempt=0 AND kind IN ('run.submit','control.resume','config.apply')`, now, node); err != nil {
			return request, err
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO runtime_requests(id,node_id,state,payload,created_at) VALUES(?,?,?,?,?)", request.ID, node, request.State, encode(request), now); err != nil {
		return request, err
	}
	return request, tx.Commit()
}

func (s *Store) ApproveRuntimeRequest(ctx context.Context, node, id string, approval runtimepkg.ChannelApproval) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireRuntimeAllowedTx(ctx, tx, node); err != nil {
		return err
	}
	request, err := runtimeRequestTx(ctx, tx, node, id)
	if err != nil {
		return err
	}
	enrollment, seen, err := runtimeChannelTx(ctx, tx, node)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	if !approval.Approved || approval.PlanSHA256 == "" || approval.PlanSHA256 != request.PlanSHA256 || request.State != "awaiting_approval" ||
		request.Enrollment != enrollment || now-seen > 45 || now-request.UpdatedAt > 900 {
		return model.Conflict("批准必须匹配当前节点的有效计划；过期或变更后需重新生成")
	}
	request.State, request.ApprovedAt, request.UpdatedAt = "approved", now, now
	if err := saveRuntimeRequest(ctx, tx, node, request); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CancelRuntimeRequest(ctx context.Context, node, id string) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	request, err := runtimeRequestTx(ctx, tx, node, id)
	if err != nil {
		return err
	}
	if !runtimepkg.ActiveChannelState(request.State) || request.State == "running" {
		return model.Conflict("执行中的安装操作不能远程取消；先确认本机结果")
	}
	request.State, request.UpdatedAt = "cancelled", time.Now().Unix()
	if err := saveRuntimeRequest(ctx, tx, node, request); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RuntimeExchange(ctx context.Context, node string, report runtimepkg.ChannelReport) (runtimepkg.ChannelReply, error) {
	reply := runtimepkg.ChannelReply{}
	if report.Enrollment.Validate() != nil {
		return reply, model.Invalid("节点安装通道身份无效")
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return reply, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	previous, _, priorErr := runtimeChannelTx(ctx, tx, node)
	if priorErr != nil && !errors.Is(priorErr, model.ErrNotFound) {
		return reply, priorErr
	}
	if priorErr == nil && previous != report.Enrollment {
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT payload FROM runtime_requests WHERE node_id=? AND state IN ('requested','awaiting_approval','approved','running')`, node).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return reply, err
		}
		if err == nil {
			var request runtimepkg.ChannelRequest
			if err := json.Unmarshal([]byte(raw), &request); err != nil {
				return reply, err
			}
			request.ErrorCode, request.UpdatedAt = "node_configuration_changed", now
			if request.State == "running" {
				request.State = "unknown"
			} else {
				request.State = "cancelled"
			}
			if err := saveRuntimeRequest(ctx, tx, node, request); err != nil {
				return reply, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_channels(node_id,enrollment,seen_at) VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET enrollment=excluded.enrollment,seen_at=excluded.seen_at`, node, encode(report.Enrollment), now); err != nil {
		return reply, err
	}
	if report.Result != nil && (priorErr != nil || previous == report.Enrollment) {
		request, err := runtimeRequestTx(ctx, tx, node, report.Result.RequestID)
		if err != nil {
			return reply, err
		}
		recoverable := request.State == "unknown" || request.State == "staged" || request.State == "installed" ||
			request.State == "failed" && request.ErrorCode == "operation_not_applied"
		if request.Enrollment != report.Enrollment && !(report.Reconcile && recoverable && request.Enrollment.SameScope(report.Enrollment)) {
			return reply, model.Conflict("安装回执的节点配置范围已改变")
		}
		if err := applyRuntimeResult(&request, *report.Result, now); err != nil {
			return reply, err
		}
		if request.State == "enabled" {
			if err := bindEnabledPluginTx(ctx, tx, node, request); err != nil {
				return reply, err
			}
		}
		if err := saveRuntimeRequest(ctx, tx, node, request); err != nil {
			return reply, err
		}
	}
	config, err := nodeConfigTx(ctx, tx, node)
	if err != nil {
		return reply, err
	}
	if config.RuntimeInstallDisabled {
		return reply, tx.Commit()
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM runtime_requests WHERE node_id=? AND state IN ('requested','approved','running')`, node).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return reply, err
	}
	if err == nil {
		var request runtimepkg.ChannelRequest
		if err := json.Unmarshal([]byte(raw), &request); err != nil {
			return reply, err
		}
		if request.State == "approved" && now-request.ApprovedAt > 900 {
			request.State, request.UpdatedAt = "expired", now
			if err := saveRuntimeRequest(ctx, tx, node, request); err != nil {
				return reply, err
			}
		} else {
			reply.Request = &request
		}
	}
	if reply.Request == nil && report.Reconcile {
		err := tx.QueryRowContext(ctx, `SELECT payload FROM runtime_requests WHERE node_id=? AND state='unknown'
 AND json_extract(payload,'$.enrollment.scope_sha256')=? AND json_extract(payload,'$.action') IN ('stage','install','enable')
 ORDER BY created_at,rowid LIMIT 1`, node, report.Enrollment.ScopeSHA256).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return reply, err
		}
		if err == nil {
			var check runtimepkg.ChannelRequest
			if err := json.Unmarshal([]byte(raw), &check); err != nil {
				return reply, err
			}
			reply.Check = &check
		}
	}
	return reply, tx.Commit()
}

func applyRuntimeResult(request *runtimepkg.ChannelRequest, result runtimepkg.ChannelResult, now int64) error {
	if result.RequestID != request.ID {
		return model.Invalid("安装请求身份无效")
	}
	switch result.State {
	case "awaiting_approval":
		if runtimepkg.ValidateChannelPlan(*request, result) != nil {
			return model.Invalid("节点返回的计划与操作或节点身份不符")
		}
		if request.State != "requested" {
			if request.PlanSHA256 == result.PlanSHA256 {
				return nil
			}
			return model.Conflict("安装计划已改变，不能覆盖原批准")
		}
		request.Review, request.Operation, request.Lifecycle, request.PlanSHA256 = result.Review, result.Operation, result.Lifecycle, result.PlanSHA256
	case "running":
		if result.PlanSHA256 != request.PlanSHA256 || result.Receipt != nil || result.OperationReceipt != nil || result.LifecycleReceipt != nil || result.Review != nil || result.Operation != nil || result.Lifecycle != nil || result.ErrorCode != "" {
			return model.Invalid("安装执行确认无效")
		}
		if request.State == "running" {
			return nil
		}
		if request.State != "approved" || now-request.ApprovedAt > 900 {
			return model.Conflict("安装批准已过期或已撤销")
		}
	case "staged", "installed", "selected", "drained", "started_paused", "enabled":
		if runtimepkg.ValidateChannelReceipt(*request, result) != nil {
			return model.Invalid("安装回执与批准不符，或试图越过激活边界")
		}
		if request.State == result.State {
			if encode(request.Receipt) == encode(result.Receipt) && encode(request.OperationReceipt) == encode(result.OperationReceipt) && encode(request.LifecycleReceipt) == encode(result.LifecycleReceipt) {
				return nil
			}
			return model.Conflict("已保存的安装回执不能被改写")
		}
		if request.State != "running" && !(request.State == "unknown" && (request.Action == "stage" || request.Action == "install" || request.Action == "enable")) {
			return model.Conflict("安装请求尚未执行，或已经结束")
		}
		request.Receipt, request.OperationReceipt = result.Receipt, result.OperationReceipt
		request.LifecycleReceipt = result.LifecycleReceipt
	case "failed", "unknown":
		if result.Review != nil || result.Operation != nil || result.Lifecycle != nil || result.LifecycleReceipt != nil || result.Receipt != nil || result.OperationReceipt != nil ||
			result.PlanSHA256 != request.PlanSHA256 ||
			(result.ErrorCode != "plan_unavailable" && result.ErrorCode != "plan_changed" && result.ErrorCode != "operation_interrupted" && result.ErrorCode != "operation_not_applied") {
			return model.Invalid("安装失败回执无效")
		}
		if request.State == result.State && request.ErrorCode == result.ErrorCode {
			return nil
		}
		reconciled := result.State == "failed" && result.ErrorCode == "operation_not_applied" && request.State == "unknown" && (request.Action == "stage" || request.Action == "install")
		if result.ErrorCode == "operation_not_applied" && !reconciled {
			return model.Conflict("仅允许 Agent 核对未完成的下载或安装")
		}
		if !reconciled && (result.State == "unknown" && request.State != "running" || result.State == "failed" && request.State != "requested" && request.State != "approved") {
			return model.Conflict("安装请求状态已改变")
		}
		request.ErrorCode = result.ErrorCode
	default:
		return model.Invalid("安装回执状态无效")
	}
	if result.State != "unknown" {
		request.ErrorCode = result.ErrorCode
	}
	request.State, request.UpdatedAt = result.State, now
	return nil
}

func bindEnabledPluginTx(ctx context.Context, tx *sql.Tx, node string, request runtimepkg.ChannelRequest) error {
	hello := request.OperationReceipt.Hello
	binding := ipsentinel.Binding{
		Target: request.Enrollment.Target, ExecutorInstanceID: hello.Status.ExecutorInstanceID,
		Epoch: hello.Status.Control.Epoch, DescriptorSHA256: hello.DescriptorSHA256, Mode: ipsentinel.ModeManaged,
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	var existing string
	err := tx.QueryRowContext(ctx, "SELECT binding FROM executor_bindings WHERE node_id=?", node).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if existing != encode(binding) {
			return model.Conflict("启用期间插件接入身份已变化，保留当前授权")
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO executor_bindings(node_id,binding,instance_id) VALUES(?,?,?)", node, encode(binding), binding.ExecutorInstanceID); err != nil {
		return err
	}
	config := runtimepkg.EnableConfig(*request.Enable)
	_, err = tx.ExecContext(ctx, "INSERT INTO executor_configs(node_id,instance_id,revision,digest,config,binding) VALUES(?,?,?,?,?,?)",
		node, binding.ExecutorInstanceID, hello.Status.Config.Revision, hello.Status.Config.SHA256, encode(config), encode(binding))
	return err
}
