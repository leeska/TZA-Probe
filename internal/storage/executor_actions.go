package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
)

const executorLeaseDuration = 30 * time.Second

func executorBindingTx(ctx context.Context, tx *sql.Tx, node string) (ipsentinel.Binding, *ipsentinel.Hello, bool, error) {
	var binding ipsentinel.Binding
	var raw string
	var snapshot sql.NullString
	var paused bool
	err := tx.QueryRowContext(ctx, "SELECT binding,hello,dispatch_paused FROM executor_bindings WHERE node_id=?", node).Scan(&raw, &snapshot, &paused)
	if errors.Is(err, sql.ErrNoRows) {
		return binding, nil, paused, model.NotFound("尚未授权执行器")
	}
	if err != nil {
		return binding, nil, paused, err
	}
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		return binding, nil, paused, err
	}
	var hello *ipsentinel.Hello
	if snapshot.Valid {
		if err := json.Unmarshal([]byte(snapshot.String), &hello); err != nil {
			return binding, nil, paused, err
		}
	}
	return binding, hello, paused, nil
}

func validateManagedHello(binding ipsentinel.Binding, hello *ipsentinel.Hello, now time.Time) error {
	if binding.Mode != ipsentinel.ModeManaged || hello == nil {
		return model.Conflict("需要明确授权托管模式并等待有效状态同步")
	}
	page := ipsentinel.RecordPage{ExecutorInstanceID: binding.ExecutorInstanceID, Records: []ipsentinel.Record{}}
	if err := (ipsentinel.SyncReport{Hello: *hello, Page: page}).Validate(binding, now); err != nil || hello.Status.Control.Mode != ipsentinel.ModeManaged {
		return model.Conflict("执行器身份、模式或状态已变化，请核对授权")
	}
	return nil
}

func requireFreshExecutorSnapshot(ctx context.Context, tx *sql.Tx, node string, now time.Time) error {
	var received int64
	if err := tx.QueryRowContext(ctx, "SELECT received_at FROM executor_bindings WHERE node_id=?", node).Scan(&received); err != nil {
		return err
	}
	if received == 0 || now.Sub(time.Unix(received, 0)) > 45*time.Second || time.Unix(received, 0).After(now.Add(5*time.Second)) {
		return model.Conflict("最近执行器状态已过期，刷新后再试")
	}
	return nil
}

func (s *Store) CreateExecutorAction(ctx context.Context, node string, action ipsentinel.ExecutorAction) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := createExecutorActionTx(ctx, tx, node, action, nowFunc().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func createExecutorActionTx(ctx context.Context, tx *sql.Tx, node string, action ipsentinel.ExecutorAction, now time.Time) error {
	binding, hello, paused, err := executorBindingTx(ctx, tx, node)
	if err != nil {
		return err
	}
	if err := requireFreshExecutorSnapshot(ctx, tx, node, now); err != nil {
		return err
	}
	if err := validateManagedHello(binding, hello, now); err != nil {
		return err
	}
	action.Binding = binding
	action.LeaseID, action.RequestID, action.Attempt = "", "", 0
	action.JobID = strings.ToLower(action.JobID)
	if err := action.Validate(); err != nil {
		return model.Invalid("命令不符合执行器契约：" + err.Error())
	}
	switch action.Kind {
	case ipsentinel.MethodConfigValidate, ipsentinel.MethodConfigApply:
		if !ipsentinel.HasFeature(*hello, "config") {
			return model.Conflict("本机未开放配置能力")
		}
		profiles := map[string]ipsentinel.Profile{}
		for _, profile := range hello.Profiles {
			profiles[profile.ProfileID] = profile
		}
		if err := ipsentinel.ValidateConfigSemantic(*action.Config, profiles); err != nil {
			return model.Invalid("配置包含未批准的 Profile、域或用途")
		}
		if action.Kind == ipsentinel.MethodConfigApply {
			revision := int64(0)
			if hello.Status.Config != nil {
				revision = hello.Status.Config.Revision
			}
			if revision != action.ExpectedRevision {
				return model.Conflict("配置修订已变化，请重新核对")
			}
			digest, err := ipsentinel.ConfigSHA256(*action.Config)
			if err != nil {
				return err
			}
			var validated int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM executor_actions WHERE node_id=? AND binding=? AND kind='config.validate' AND status='completed'
 AND json_extract(result,'$.validation.valid')=1 AND json_extract(result,'$.validation.config_sha256')=? LIMIT 1`, node, encode(binding), digest).Scan(&validated); errors.Is(err, sql.ErrNoRows) {
				return model.Conflict("请先取得本机配置验证通过的回执")
			} else if err != nil {
				return err
			}
		}
	case ipsentinel.MethodRunSubmit:
		start, _ := ipsentinel.ParseContractTime(action.NotBefore)
		end, _ := ipsentinel.ParseContractTime(action.ExpiresAt)
		if !end.After(now) || start.After(now.Add(15*time.Minute)) {
			return model.Invalid("任务时间窗口已过期或过远")
		}
		if paused || hello.Status.Control.Paused || hello.Status.Control.Draining || hello.Status.Health != "ready" || !ipsentinel.HasFeature(*hello, "run") || !hello.LocalPolicy.HardLimitsAvailable {
			return model.Conflict("当前执行器不可执行新任务")
		}
		if err := validateExecutorJob(ctx, tx, node, binding, *hello, action); err != nil {
			return err
		}
	case ipsentinel.MethodRunCancel:
		var originalID, status string
		var attempt int64
		if err := tx.QueryRowContext(ctx, `SELECT id,status,attempt FROM executor_actions WHERE node_id=? AND binding=? AND kind='run.submit' AND lower(json_extract(payload,'$.job_id'))=?`, node, encode(binding), action.JobID).Scan(&originalID, &status, &attempt); errors.Is(err, sql.ErrNoRows) {
			return model.NotFound("没有这个逻辑任务")
		} else if err != nil {
			return err
		}
		if status == "pending" && attempt == 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE executor_actions SET status='cancelled',updated_at=? WHERE id=?", now.Unix(), originalID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO executor_actions(node_id,id,kind,payload,status,created_at,updated_at,binding,request_id) VALUES(?,?,?,?,'completed',?,?,?,?)`, node, action.ID, action.Kind, encode(action), now.Unix(), now.Unix(), encode(binding), action.ID); err != nil {
				return err
			}
			return nil
		}
	case ipsentinel.MethodControlResume:
		if action.ExpectedPauseGeneration != hello.Status.Control.PauseGeneration {
			return model.Conflict("暂停代次已变化")
		}
	}
	var previous string
	if action.Kind == ipsentinel.MethodRunSubmit {
		err := tx.QueryRowContext(ctx, `SELECT payload FROM executor_actions WHERE node_id=? AND binding=? AND kind='run.submit' AND lower(json_extract(payload,'$.job_id'))=?`, node, encode(binding), action.JobID).Scan(&previous)
		if err == nil {
			var before ipsentinel.ExecutorAction
			if err := json.Unmarshal([]byte(previous), &before); err != nil {
				return err
			}
			beforeDigest, _ := ipsentinel.JobDigest(before.Job())
			afterDigest, _ := ipsentinel.JobDigest(action.Job())
			if beforeDigest == afterDigest {
				return nil
			}
			return model.Conflict("逻辑任务 ID 已对应其他不可变执行内容")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO executor_actions(node_id,id,kind,payload,status,created_at,updated_at,binding,request_id) VALUES(?,?,?,?,'pending',?,?,?,?)`, node, action.ID, action.Kind, encode(action), now.Unix(), now.Unix(), encode(binding), action.ID); err != nil {
		var coded interface{ Code() int }
		if errors.As(err, &coded) && coded.Code()&255 == 19 {
			return model.Conflict("命令 ID 已存在")
		}
		return err
	}
	if action.Kind == ipsentinel.MethodControlPause {
		if _, err := tx.ExecContext(ctx, "UPDATE executor_bindings SET dispatch_paused=1 WHERE node_id=?", node); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE executor_actions SET status='cancelled',updated_at=? WHERE node_id=? AND kind='run.submit' AND status='pending' AND attempt=0", now.Unix(), node); err != nil {
			return err
		}
	}
	return nil
}

func validateExecutorJob(ctx context.Context, tx *sql.Tx, node string, binding ipsentinel.Binding, hello ipsentinel.Hello, action ipsentinel.ExecutorAction) error {
	if hello.Status.Config == nil || *hello.Status.Config != action.Job().Config {
		return model.Conflict("任务配置引用与当前修订不一致")
	}
	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT config FROM executor_configs WHERE node_id=? AND binding=? AND revision=? AND digest=?", node, encode(binding), action.ConfigRevision, action.ConfigSHA256).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return model.Conflict("缺少本机已确认的配置快照，请先验证并应用配置")
	} else if err != nil {
		return err
	}
	var config ipsentinel.Config
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return err
	}
	if action.TimeoutMS > config.Limits.TimeoutMS {
		return model.Invalid("任务超时超过配置限额")
	}
	for _, service := range config.Services {
		if !service.Enabled || service.ServiceID != action.ServiceID {
			continue
		}
		profileID := service.ObservationProfileID
		if action.Purpose == ipsentinel.PurposeExperiment {
			if service.Experiment == nil {
				return model.Invalid("该服务未批准实验用途")
			}
			profileID = service.Experiment.ProfileID
		}
		for _, profile := range hello.Profiles {
			if profile.ProfileID == profileID && profile.Purpose == action.Purpose && profile.AdapterID == service.AdapterID {
				return nil
			}
		}
	}
	return model.Invalid("任务服务或用途不在本机批准配置中")
}

func (s *Store) LeaseExecutorAction(ctx context.Context, node string, hello ipsentinel.Hello) (ipsentinel.ExecutorAction, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ipsentinel.ExecutorAction{}, err
	}
	defer tx.Rollback()
	binding, persisted, paused, err := executorBindingTx(ctx, tx, node)
	if err != nil {
		return ipsentinel.ExecutorAction{}, err
	}
	now := nowFunc().UTC()
	if err := requireFreshExecutorSnapshot(ctx, tx, node, now); err != nil {
		return ipsentinel.ExecutorAction{}, err
	}
	if err := validateManagedHello(binding, persisted, now); err != nil {
		return ipsentinel.ExecutorAction{}, err
	}
	if err := validateManagedHello(binding, &hello, now); err != nil {
		return ipsentinel.ExecutorAction{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload,status,attempt,request_id FROM executor_actions
 WHERE node_id=? AND binding=? AND status IN ('pending','leased') AND lease_until<=?
 ORDER BY CASE kind WHEN 'control.pause' THEN 0 WHEN 'run.cancel' THEN 1 WHEN 'control.resume' THEN 2 ELSE 3 END,created_at,rowid`, node, encode(binding), now.Unix())
	if err != nil {
		return ipsentinel.ExecutorAction{}, err
	}
	type candidate struct {
		action            ipsentinel.ExecutorAction
		status, requestID string
		attempt           int64
	}
	candidates := []candidate{}
	for rows.Next() {
		var raw string
		var entry candidate
		if err := rows.Scan(&raw, &entry.status, &entry.attempt, &entry.requestID); err != nil {
			rows.Close()
			return ipsentinel.ExecutorAction{}, err
		}
		if err := json.Unmarshal([]byte(raw), &entry.action); err != nil {
			rows.Close()
			return ipsentinel.ExecutorAction{}, err
		}
		candidates = append(candidates, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ipsentinel.ExecutorAction{}, err
	}
	rows.Close()
	for _, entry := range candidates {
		action := entry.action
		if action.Kind == ipsentinel.MethodRunSubmit {
			end, _ := ipsentinel.ParseContractTime(action.ExpiresAt)
			if entry.attempt == 0 && !end.After(now) {
				if _, err := tx.ExecContext(ctx, "UPDATE executor_actions SET status='expired',updated_at=? WHERE id=?", now.Unix(), action.ID); err != nil {
					return ipsentinel.ExecutorAction{}, err
				}
				continue
			}
			if entry.attempt == 0 && (hello.Status.Config == nil || *hello.Status.Config != action.Job().Config) {
				if _, err := tx.ExecContext(ctx, "UPDATE executor_actions SET status='expired',updated_at=? WHERE id=?", now.Unix(), action.ID); err != nil {
					return ipsentinel.ExecutorAction{}, err
				}
				continue
			}
			start, _ := ipsentinel.ParseContractTime(action.NotBefore)
			if entry.attempt == 0 && (now.Before(start) || paused || hello.Status.Control.Paused || hello.Status.Control.Draining || hello.Status.Health != "ready" || !ipsentinel.HasFeature(hello, "run") || !hello.LocalPolicy.HardLimitsAvailable) {
				continue
			}
		}
		if action.Kind == ipsentinel.MethodRunCancel {
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM executor_actions WHERE node_id=? AND binding=? AND kind='run.submit' AND status='leased' AND lease_until>? AND json_extract(payload,'$.job_id')=? AND run IS NULL`, node, encode(binding), now.Unix(), action.JobID).Scan(&active); err != nil {
				return ipsentinel.ExecutorAction{}, err
			}
			if active > 0 {
				continue
			}
		}
		if slices.Contains([]string{ipsentinel.MethodConfigApply, ipsentinel.MethodConfigValidate}, action.Kind) && !ipsentinel.HasFeature(hello, "config") {
			continue
		}
		if action.Kind == ipsentinel.MethodControlResume && entry.attempt == 0 && action.ExpectedPauseGeneration != hello.Status.Control.PauseGeneration {
			if _, err := tx.ExecContext(ctx, "UPDATE executor_actions SET status='rejected',updated_at=? WHERE id=?", now.Unix(), action.ID); err != nil {
				return ipsentinel.ExecutorAction{}, err
			}
			continue
		}
		action.LeaseID, action.RequestID, action.Attempt = ipsentinel.NewUUID(), entry.requestID, entry.attempt+1
		if _, err := tx.ExecContext(ctx, `UPDATE executor_actions SET status='leased',lease_id=?,lease_until=?,attempt=attempt+1,updated_at=? WHERE id=?`, action.LeaseID, now.Add(executorLeaseDuration).Unix(), now.Unix(), action.ID); err != nil {
			return ipsentinel.ExecutorAction{}, err
		}
		return action, tx.Commit()
	}
	return ipsentinel.ExecutorAction{}, tx.Commit()
}

func validateActionResult(action ipsentinel.ExecutorAction, result ipsentinel.ActionResult) error {
	validate := func(definition string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return ipsentinel.ValidateDefinition(definition, raw, nil)
	}
	if result.Accepted && (result.Retryable || result.ErrorCode != "") {
		return model.Invalid("成功回执包含错误状态")
	}
	if !result.Accepted {
		if action.Kind == ipsentinel.MethodConfigValidate && result.Validation != nil && action.Config != nil {
			digest, err := ipsentinel.ConfigSHA256(*action.Config)
			if err != nil {
				return err
			}
			if result.Validation.ConfigSHA256 != digest {
				return model.Invalid("配置验证回执不匹配")
			}
			return validate("validation", result.Validation)
		}
		if result.ErrorCode == "" {
			return model.Invalid("缺少拒绝原因")
		}
		if result.Retryable && !slices.Contains([]string{ipsentinel.ErrCodeBusy, ipsentinel.ErrCodeNotDue, ipsentinel.ErrCodeUnhealthy, ipsentinel.ErrCodeStoragePressure}, result.ErrorCode) {
			return model.Invalid("该拒绝不允许自动重试")
		}
		return validate("error", ipsentinel.Error{Code: result.ErrorCode, Message: result.Message, Retryable: result.Retryable})
	}
	switch action.Kind {
	case ipsentinel.MethodConfigValidate:
		if result.Validation == nil || !result.Validation.Valid {
			return model.Invalid("缺少本机配置验证")
		}
		digest, _ := ipsentinel.ConfigSHA256(*action.Config)
		if result.Validation.ConfigSHA256 != digest {
			return model.Invalid("配置验证摘要不匹配")
		}
		return validate("validation", result.Validation)
	case ipsentinel.MethodConfigApply:
		if result.Config == nil {
			return model.Invalid("缺少配置应用回执")
		}
		digest, _ := ipsentinel.ConfigSHA256(*action.Config)
		expected := action.ExpectedRevision
		if result.Config.Changed {
			expected++
		}
		if result.Config.Config.Revision != expected || result.Config.Config.SHA256 != digest {
			return model.Invalid("配置应用回执不匹配")
		}
		return validate("config_ref", result.Config.Config)
	case ipsentinel.MethodRunSubmit, ipsentinel.MethodRunCancel:
		if action.Kind == ipsentinel.MethodRunCancel && result.NotStarted {
			if !result.Accepted || result.Run != nil {
				return model.Invalid("未启动任务取消回执无效")
			}
			return nil
		}
		if result.Run == nil || !strings.EqualFold(result.Run.JobID, action.JobID) {
			return model.Invalid("缺少匹配的逻辑任务回执")
		}
		if err := validate("run", result.Run); err != nil {
			return err
		}
		if result.Run.Result != nil && action.Kind == ipsentinel.MethodRunSubmit {
			native := result.Run.Result
			if native.Config != action.Job().Config || native.ServiceID != action.ServiceID || native.Purpose != action.Purpose || native.Phase != action.Phase || native.ExecutorInstanceID != action.Binding.ExecutorInstanceID || native.Epoch != action.Binding.Epoch {
				return model.Invalid("任务结果与原始准入内容不一致")
			}
		}
	case ipsentinel.MethodControlPause, ipsentinel.MethodControlResume:
		if result.Control == nil || result.Control.Epoch != action.Binding.Epoch || result.Control.Mode != ipsentinel.ModeManaged || result.Control.Paused != (action.Kind == ipsentinel.MethodControlPause) {
			return model.Invalid("控制回执不匹配")
		}
		return validate("control", result.Control)
	}
	return nil
}

func (s *Store) FinishExecutorAction(ctx context.Context, node string, result ipsentinel.ActionResult) error {
	if !ipsentinel.ValidUUID(result.ID) || !ipsentinel.ValidUUID(result.LeaseID) || len(result.Message) > 1000 {
		return model.Invalid("执行器回执无效")
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw, status, lease string
	var until int64
	var previous sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT a.payload,a.status,a.lease_id,a.lease_until,a.result FROM executor_actions a JOIN executor_bindings b ON b.node_id=a.node_id AND b.binding=a.binding WHERE a.node_id=? AND a.id=?`, node, result.ID).Scan(&raw, &status, &lease, &until, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Conflict("命令授权已失效")
	}
	if err != nil {
		return err
	}
	if previous.Valid && previous.String == encode(result) {
		return tx.Commit()
	}
	if status != "leased" || lease != result.LeaseID || until <= nowFunc().Unix() {
		return model.Conflict("命令租约已失效")
	}
	var action ipsentinel.ExecutorAction
	if err := json.Unmarshal([]byte(raw), &action); err != nil {
		return err
	}
	if err := validateActionResult(action, result); err != nil {
		return model.Invalid("执行器回执不符合命令契约")
	}
	now := nowFunc()
	status = "rejected"
	nextRequest, nextLeaseUntil := action.ID, int64(0)
	if result.Accepted {
		status = "completed"
		if action.Kind == ipsentinel.MethodRunSubmit {
			status = "accepted"
		}
	}
	if result.Retryable {
		status = "pending"
		nextRequest = ipsentinel.NewUUID()
		nextLeaseUntil = now.Add(15 * time.Second).Unix()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE executor_actions SET status=?,result=?,request_id=?,lease_until=?,updated_at=? WHERE id=?`, status, encode(result), nextRequest, nextLeaseUntil, now.Unix(), action.ID); err != nil {
		return err
	}
	if result.Accepted && action.Kind == ipsentinel.MethodConfigApply {
		digest, _ := ipsentinel.ConfigSHA256(*action.Config)
		stored, err := tx.ExecContext(ctx, `INSERT INTO executor_configs(node_id,instance_id,revision,digest,config,binding) VALUES(?,?,?,?,?,?)
 ON CONFLICT(node_id,instance_id,revision) DO UPDATE SET binding=excluded.binding WHERE executor_configs.digest=excluded.digest`, node, action.Binding.ExecutorInstanceID, result.Config.Config.Revision, digest, encode(action.Config), encode(action.Binding))
		if err != nil {
			return err
		}
		count, err := stored.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return model.Conflict("同一配置修订出现不同内容")
		}
	}
	if result.Accepted && action.Kind == ipsentinel.MethodControlResume {
		var newer int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM executor_actions WHERE node_id=? AND kind='control.pause' AND rowid>(SELECT rowid FROM executor_actions WHERE id=?)`, node, action.ID).Scan(&newer); err != nil {
			return err
		}
		if newer == 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE executor_bindings SET dispatch_paused=0 WHERE node_id=?", node); err != nil {
				return err
			}
		}
	}
	if result.Run != nil {
		if err := saveExecutorRun(ctx, tx, node, encode(action.Binding), *result.Run); err != nil {
			return err
		}
	}
	if action.Kind == ipsentinel.MethodRunCancel && result.Accepted && result.NotStarted {
		if _, err := tx.ExecContext(ctx, `UPDATE executor_actions SET status='cancelled',lease_id='',updated_at=? WHERE node_id=? AND binding=? AND kind='run.submit' AND lower(json_extract(payload,'$.job_id'))=? AND status IN ('pending','leased')`, now.Unix(), node, encode(action.Binding), action.JobID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func saveExecutorRun(ctx context.Context, tx *sql.Tx, node, binding string, run ipsentinel.Run) error {
	var previous sql.NullString
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT run,payload FROM executor_actions WHERE node_id=? AND binding=? AND kind='run.submit' AND lower(json_extract(payload,'$.job_id'))=?`, node, binding, strings.ToLower(run.JobID)).Scan(&previous, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var action ipsentinel.ExecutorAction
	if err := json.Unmarshal([]byte(raw), &action); err != nil {
		return err
	}
	if run.Result != nil {
		result := run.Result
		if result.ExecutorInstanceID != action.Binding.ExecutorInstanceID || result.Epoch != action.Binding.Epoch || result.Config != action.Job().Config || result.ServiceID != action.ServiceID || result.Purpose != action.Purpose || result.Phase != action.Phase {
			return model.Conflict("原生任务终态与准入内容不符")
		}
	}
	if previous.Valid {
		var before ipsentinel.Run
		if err := json.Unmarshal([]byte(previous.String), &before); err != nil {
			return err
		}
		if before.RunID != run.RunID {
			return model.Conflict("同一逻辑任务出现不同 run ID")
		}
		if before.Result != nil {
			if run.Result != nil {
				beforeDigest, _ := ipsentinel.CanonicalSHA256(before)
				afterDigest, _ := ipsentinel.CanonicalSHA256(run)
				if beforeDigest != afterDigest {
					return model.Conflict("任务终态内容发生变化")
				}
			}
			return nil
		}
		if before.State == ipsentinel.StateRunning && run.State == ipsentinel.StateAccepted {
			return nil
		}
		if before.State == ipsentinel.StateCancelling && run.State != ipsentinel.StateCancelled && run.Result == nil {
			return nil
		}
		if (before.State == ipsentinel.StateRunning || before.State == ipsentinel.StateCancelling) && run.State == ipsentinel.StateAccepted {
			return nil
		}
		if before.State == ipsentinel.StateRunning && run.State == ipsentinel.StateCancelling {
			if _, err := tx.ExecContext(ctx, `UPDATE executor_actions SET run=? WHERE node_id=? AND binding=? AND kind='run.submit' AND lower(json_extract(payload,'$.job_id'))=?`, encode(run), node, binding, strings.ToLower(run.JobID)); err != nil {
				return err
			}
			return nil
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE executor_actions SET run=? WHERE node_id=? AND binding=? AND kind='run.submit' AND lower(json_extract(payload,'$.job_id'))=?`, encode(run), node, binding, strings.ToLower(run.JobID))
	return err
}

func projectExecutorRun(ctx context.Context, tx *sql.Tx, node, binding string, record ipsentinel.Record) error {
	var run ipsentinel.Run
	switch record.Kind {
	case ipsentinel.RecordRunAccepted, ipsentinel.RecordRunStarted:
		if err := json.Unmarshal(record.Data, &run); err != nil {
			return err
		}
	case ipsentinel.RecordRunTerminal:
		var result ipsentinel.RunResult
		if err := json.Unmarshal(record.Data, &result); err != nil {
			return err
		}
		if result.Provenance != "native" {
			return nil
		}
		run = ipsentinel.Run{JobID: result.JobID, RunID: result.RunID, State: result.Execution, AcceptedAt: result.AcceptedAt, Result: &result}
	default:
		return nil
	}
	return saveExecutorRun(ctx, tx, node, binding, run)
}

func (s *Store) ExecutorActions(ctx context.Context, node string) ([]ipsentinel.ActionView, error) {
	rows, err := s.query(ctx, "SELECT payload,status,result,run,attempt FROM executor_actions WHERE node_id=? ORDER BY rowid DESC LIMIT 50", node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	actions := []ipsentinel.ActionView{}
	for rows.Next() {
		var raw string
		var result, run sql.NullString
		var view ipsentinel.ActionView
		if err := rows.Scan(&raw, &view.Status, &result, &run, &view.Attempt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &view.ExecutorAction); err != nil {
			return nil, err
		}
		if result.Valid {
			if err := json.Unmarshal([]byte(result.String), &view.Result); err != nil {
				return nil, err
			}
		}
		if run.Valid {
			if err := json.Unmarshal([]byte(run.String), &view.Run); err != nil {
				return nil, err
			}
		}
		actions = append(actions, view)
	}
	return actions, rows.Err()
}
