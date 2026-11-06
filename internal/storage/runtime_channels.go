package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

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
	if input.Validate() != nil {
		return runtimepkg.ChannelRequest{}, model.Invalid("请指定 stage/install/select、精确版本及对应暂存请求")
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
		if request.Enrollment != report.Enrollment {
			return reply, model.Conflict("安装回执的节点配置范围已改变")
		}
		if err := applyRuntimeResult(&request, *report.Result, now); err != nil {
			return reply, err
		}
		if err := saveRuntimeRequest(ctx, tx, node, request); err != nil {
			return reply, err
		}
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
		request.Review, request.Operation, request.PlanSHA256 = result.Review, result.Operation, result.PlanSHA256
	case "running":
		if result.PlanSHA256 != request.PlanSHA256 || result.Receipt != nil || result.OperationReceipt != nil || result.Review != nil || result.Operation != nil || result.ErrorCode != "" {
			return model.Invalid("安装执行确认无效")
		}
		if request.State == "running" {
			return nil
		}
		if request.State != "approved" || now-request.ApprovedAt > 900 {
			return model.Conflict("安装批准已过期或已撤销")
		}
	case "staged", "installed", "selected":
		if runtimepkg.ValidateChannelReceipt(*request, result) != nil {
			return model.Invalid("安装回执与批准不符，或试图越过激活边界")
		}
		if request.State == result.State {
			if encode(request.Receipt) == encode(result.Receipt) && encode(request.OperationReceipt) == encode(result.OperationReceipt) {
				return nil
			}
			return model.Conflict("已保存的安装回执不能被改写")
		}
		if request.State != "running" {
			return model.Conflict("安装请求尚未执行，或已经结束")
		}
		request.Receipt, request.OperationReceipt = result.Receipt, result.OperationReceipt
	case "failed", "unknown":
		if result.Review != nil || result.Operation != nil || result.Receipt != nil || result.OperationReceipt != nil ||
			result.PlanSHA256 != request.PlanSHA256 ||
			(result.ErrorCode != "plan_unavailable" && result.ErrorCode != "plan_changed" && result.ErrorCode != "operation_interrupted") {
			return model.Invalid("安装失败回执无效")
		}
		if request.State == result.State && request.ErrorCode == result.ErrorCode {
			return nil
		}
		if result.State == "unknown" && request.State != "running" || result.State == "failed" && request.State != "requested" && request.State != "approved" {
			return model.Conflict("安装请求状态已改变")
		}
		request.ErrorCode = result.ErrorCode
	default:
		return model.Invalid("安装回执状态无效")
	}
	request.State, request.UpdatedAt = result.State, now
	return nil
}
