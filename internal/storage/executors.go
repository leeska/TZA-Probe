package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
)

const executorSchema = `
CREATE TABLE IF NOT EXISTS executor_bindings(
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 binding TEXT NOT NULL, instance_id TEXT NOT NULL UNIQUE,
 cursor INTEGER NOT NULL DEFAULT 0, received_at INTEGER NOT NULL DEFAULT 0, hello TEXT);
CREATE TABLE IF NOT EXISTS executor_records(
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 instance_id TEXT NOT NULL, seq INTEGER NOT NULL, record_id TEXT NOT NULL,
 digest TEXT NOT NULL, payload TEXT NOT NULL,
 PRIMARY KEY(node_id,instance_id,seq), UNIQUE(node_id,instance_id,record_id));
CREATE TABLE IF NOT EXISTS executor_projections(
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 instance_id TEXT NOT NULL, projection_key TEXT NOT NULL, state TEXT NOT NULL,
 PRIMARY KEY(node_id,instance_id,projection_key));
`

const executorActionSchema = `
CREATE TABLE IF NOT EXISTS executor_actions(
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 id TEXT PRIMARY KEY, kind TEXT NOT NULL, payload TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending', created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL, result TEXT);
CREATE INDEX IF NOT EXISTS executor_action_pending ON executor_actions(node_id,status,created_at);
CREATE TABLE IF NOT EXISTS executor_configs(
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 instance_id TEXT NOT NULL, revision INTEGER NOT NULL, digest TEXT NOT NULL, config TEXT NOT NULL,
 PRIMARY KEY(node_id,instance_id,revision));
`

const executorLeaseSchema = `
UPDATE executor_actions SET status='cancelled',result=NULL WHERE status IN ('pending','leased');
UPDATE executor_actions SET result=json_object('id',id,'lease_id','','accepted',json(CASE WHEN status='completed' THEN 'true' ELSE 'false' END),'retryable',json('false'),'message',result) WHERE binding='' AND result IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS executor_job_identity ON executor_actions(node_id,binding,lower(json_extract(payload,'$.job_id'))) WHERE kind='run.submit' AND binding<>'';
`

func migrateExecutorLeases(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, column := range []struct{ table, name, definition string }{
		{"executor_actions", "binding", "TEXT NOT NULL DEFAULT ''"},
		{"executor_actions", "lease_id", "TEXT NOT NULL DEFAULT ''"},
		{"executor_actions", "lease_until", "INTEGER NOT NULL DEFAULT 0"},
		{"executor_actions", "attempt", "INTEGER NOT NULL DEFAULT 0"},
		{"executor_actions", "request_id", "TEXT NOT NULL DEFAULT ''"},
		{"executor_actions", "run", "TEXT"},
		{"executor_bindings", "dispatch_paused", "INTEGER NOT NULL DEFAULT 0"},
		{"executor_configs", "binding", "TEXT NOT NULL DEFAULT ''"},
	} {
		var exists int
		if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info(?) WHERE name=?", column.table, column.name).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			if _, err := tx.Exec("ALTER TABLE " + column.table + " ADD COLUMN " + column.name + " " + column.definition); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(executorLeaseSchema + "PRAGMA user_version=20;"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BindExecutor(ctx context.Context, node string, binding ipsentinel.Binding) error {
	if err := binding.Validate(); err != nil {
		return model.Invalid("执行器绑定参数无效")
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM nodes WHERE id=?", node).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return model.NotFound("节点不存在")
	} else if err != nil {
		return err
	}
	var owner string
	if err := tx.QueryRowContext(ctx, "SELECT node_id FROM executor_bindings WHERE instance_id=? AND node_id<>?", binding.ExecutorInstanceID, node).Scan(&owner); err == nil {
		return model.Conflict("这个执行器实例已绑定其他节点")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	maintenance, err := releaseRuntimeMaintenanceTx(ctx, tx, node, binding)
	if err != nil {
		return err
	}
	var previous string
	err = tx.QueryRowContext(ctx, "SELECT binding FROM executor_bindings WHERE node_id=?", node).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previous != "" {
		var before ipsentinel.Binding
		if err := json.Unmarshal([]byte(previous), &before); err != nil {
			return err
		}
		if before == binding {
			if maintenance {
				if _, err := tx.ExecContext(ctx, "UPDATE executor_bindings SET dispatch_paused=1 WHERE node_id=?", node); err != nil {
					return err
				}
			}
			return tx.Commit()
		}
	}
	if err := disableExecutorScheduleTx(ctx, tx, node, nowFunc(), nil); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE executor_actions SET status='cancelled',lease_id=''
 WHERE node_id=? AND status IN ('pending','leased')`, node); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO executor_bindings(node_id,binding,instance_id,dispatch_paused) VALUES(?,?,?,?)
 ON CONFLICT(node_id) DO UPDATE SET binding=excluded.binding,instance_id=excluded.instance_id,cursor=0,received_at=0,hello=NULL,dispatch_paused=excluded.dispatch_paused`, node, encode(binding), binding.ExecutorInstanceID, maintenance); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ExecutorCursor(ctx context.Context, node string) (ipsentinel.SyncReceipt, error) {
	var receipt ipsentinel.SyncReceipt
	var binding string
	err := s.queryRow(ctx, "SELECT binding,cursor FROM executor_bindings WHERE node_id=?", node).Scan(&binding, &receipt.ThroughSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return receipt, model.NotFound("尚未授权执行器绑定")
	}
	if err != nil {
		return receipt, err
	}
	err = json.Unmarshal([]byte(binding), &receipt.Binding)
	return receipt, err
}

func (s *Store) UnbindExecutor(ctx context.Context, node string) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := disableExecutorScheduleTx(ctx, tx, node, nowFunc(), nil); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE executor_actions SET status='cancelled',lease_id='' WHERE node_id=? AND status IN ('pending','leased')`, node); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM executor_bindings WHERE node_id=?", node); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveExecutorRecords(ctx context.Context, node string, report ipsentinel.SyncReport, now time.Time) (ipsentinel.SyncReceipt, error) {
	var receipt ipsentinel.SyncReceipt
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	var rawBinding string
	if err := tx.QueryRowContext(ctx, "SELECT binding,cursor FROM executor_bindings WHERE node_id=?", node).Scan(&rawBinding, &receipt.ThroughSeq); errors.Is(err, sql.ErrNoRows) {
		return receipt, model.NotFound("尚未授权执行器绑定")
	} else if err != nil {
		return receipt, err
	}
	if err := json.Unmarshal([]byte(rawBinding), &receipt.Binding); err != nil {
		return receipt, err
	}
	if err := report.Validate(receipt.Binding, now); err != nil {
		return receipt, model.Invalid("执行器记录或绑定校验失败")
	}
	if report.AfterSeq > receipt.ThroughSeq {
		return receipt, model.Conflict("记录游标不连续")
	}
	for _, record := range report.Page.Records {
		digest, err := ipsentinel.CanonicalSHA256(record)
		if err != nil {
			return receipt, err
		}
		var existingDigest string
		err = tx.QueryRowContext(ctx, "SELECT digest FROM executor_records WHERE node_id=? AND instance_id=? AND seq=?", node, record.ExecutorInstanceID, record.Seq).Scan(&existingDigest)
		if err == nil {
			if existingDigest != digest {
				return receipt, model.Conflict("相同序号的记录内容发生变化")
			}
			if record.Seq > receipt.ThroughSeq {
				if record.Seq != receipt.ThroughSeq+1 {
					return receipt, model.Conflict("记录存在数据缺口")
				}
				receipt.ThroughSeq = record.Seq
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return receipt, err
		}
		var previousSequence int64
		err = tx.QueryRowContext(ctx, "SELECT seq FROM executor_records WHERE node_id=? AND instance_id=? AND record_id=?", node, record.ExecutorInstanceID, record.RecordID).Scan(&previousSequence)
		if err == nil {
			return receipt, model.Conflict("记录 ID 已对应其他序号")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return receipt, err
		}
		if record.Seq != receipt.ThroughSeq+1 {
			return receipt, model.Conflict("记录存在数据缺口")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO executor_records(node_id,instance_id,seq,record_id,digest,payload) VALUES(?,?,?,?,?,?)", node, record.ExecutorInstanceID, record.Seq, record.RecordID, digest, encode(record)); err != nil {
			return receipt, err
		}
		if err := executorEvent(ctx, tx, node, record, now); err != nil {
			return receipt, err
		}
		if err := projectExecutorRun(ctx, tx, node, rawBinding, record); err != nil {
			return receipt, err
		}
		receipt.ThroughSeq = record.Seq
	}
	var previousHello sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT hello FROM executor_bindings WHERE node_id=?", node).Scan(&previousHello); err != nil {
		return receipt, err
	}
	replaceSnapshot := true
	if previousHello.Valid {
		var previous ipsentinel.Hello
		if err := json.Unmarshal([]byte(previousHello.String), &previous); err != nil {
			return receipt, err
		}
		before, err := ipsentinel.ParseContractTime(previous.Status.ObservedAt)
		if err != nil {
			return receipt, err
		}
		after, err := ipsentinel.ParseContractTime(report.Hello.Status.ObservedAt)
		if err != nil {
			return receipt, err
		}
		replaceSnapshot = !after.Before(before)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE executor_bindings SET cursor=? WHERE node_id=?", receipt.ThroughSeq, node); err != nil {
		return receipt, err
	}
	if replaceSnapshot {
		if _, err := tx.ExecContext(ctx, "UPDATE executor_bindings SET hello=?,received_at=? WHERE node_id=?", encode(report.Hello), now.Unix(), node); err != nil {
			return receipt, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ipsentinel.SyncReceipt{}, err
	}
	return receipt, nil
}

func executorEvent(ctx context.Context, tx *sql.Tx, node string, record ipsentinel.Record, now time.Time) error {
	var key, state, message string
	switch record.Kind {
	case ipsentinel.RecordControlChanged:
		var control ipsentinel.Control
		if err := json.Unmarshal(record.Data, &control); err != nil {
			return err
		}
		key, state = "control", "active"
		if control.Paused {
			state = "paused"
		}
		message = "IPSentinel 本机暂停状态变化：" + state + "。这不表示服务地区表现已恢复。"
	case ipsentinel.RecordAssessmentChanged, ipsentinel.RecordAssessmentSnapshot:
		var assessment ipsentinel.Assessment
		if err := json.Unmarshal(record.Data, &assessment); err != nil {
			return err
		}
		key, state = assessment.ServiceID, assessment.State
		message = "IPSentinel 服务 " + assessment.ServiceID + " 的判读阶段变化：" + state + "。请查看观测时间和有效期；不代表 IP 数据库修复。"
		if record.Kind == ipsentinel.RecordAssessmentSnapshot {
			message = ""
		}
		if assessment.State != ipsentinel.AssessmentUnknown && assessment.State != ipsentinel.AssessmentNotApplicable {
			if assessment.ValidUntil == nil {
				message = ""
			} else if until, err := ipsentinel.ParseContractTime(*assessment.ValidUntil); err != nil || !until.After(now) {
				message = ""
			}
		}
	default:
		return nil
	}
	var previous string
	err := tx.QueryRowContext(ctx, "SELECT state FROM executor_projections WHERE node_id=? AND instance_id=? AND projection_key=?", node, record.ExecutorInstanceID, key).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO executor_projections VALUES(?,?,?,?) ON CONFLICT(node_id,instance_id,projection_key) DO UPDATE SET state=excluded.state`, node, record.ExecutorInstanceID, key, state); err != nil {
		return err
	}
	if previous == state || (previous == "" && (state == "unknown" || state == "not_applicable" || state == "active")) || message == "" {
		return nil
	}
	recorded, err := ipsentinel.ParseContractTime(record.RecordedAt)
	if err != nil || recorded.Before(now.Add(-model.EventDeliveryWindow)) || recorded.After(now.Add(30*time.Second)) {
		return nil
	}
	var config string
	if err := tx.QueryRowContext(ctx, "SELECT config FROM nodes WHERE id=?", node).Scan(&config); err != nil {
		return err
	}
	var metadata model.Node
	if err := json.Unmarshal([]byte(config), &metadata); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO events(node_id,node_name,kind,message,active,at) VALUES(?,?,?,?,?,?)", node, metadata.Name, "executor", message, true, now.Unix())
	return err
}

func (s *Store) Executors(ctx context.Context) ([]ipsentinel.View, error) {
	rows, err := s.query(ctx, `SELECT b.node_id,b.binding,b.cursor,b.received_at,b.hello,c.config FROM executor_bindings b
 LEFT JOIN executor_configs c ON c.node_id=b.node_id AND c.binding=b.binding AND c.revision=json_extract(b.hello,'$.status.config.revision') AND c.digest=json_extract(b.hello,'$.status.config.sha256') ORDER BY b.node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := []ipsentinel.View{}
	for rows.Next() {
		var view ipsentinel.View
		var binding string
		var hello, config sql.NullString
		if err := rows.Scan(&view.NodeID, &binding, &view.ThroughSeq, &view.ReceivedAt, &hello, &config); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(binding), &view.Binding); err != nil {
			return nil, err
		}
		if hello.Valid {
			if err := json.Unmarshal([]byte(hello.String), &view.Hello); err != nil {
				return nil, err
			}
		}
		if config.Valid {
			if err := json.Unmarshal([]byte(config.String), &view.Config); err != nil {
				return nil, err
			}
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

func (s *Store) ExecutorRecords(ctx context.Context, node string, after int64) ([]ipsentinel.Record, error) {
	rows, err := s.query(ctx, `SELECT r.payload FROM executor_records r JOIN executor_bindings b ON b.node_id=r.node_id AND b.instance_id=r.instance_id WHERE r.node_id=? AND r.seq>? ORDER BY r.seq LIMIT 50`, node, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []ipsentinel.Record{}
	for rows.Next() {
		var raw string
		var record ipsentinel.Record
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
