package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/leeska/TZA-Probe/internal/ipsentinel"
	"github.com/leeska/TZA-Probe/internal/model"
)

// InstallIdentity hands a freshly installed node its IPSentinel handshake
// identity: a controller-wide UUID plus a per-node UUID, both assigned once
// and stored in the node config so later install-command reruns keep them.
// The node token is the only credential; it proves the caller owns the node.
func (s *Store) InstallIdentity(ctx context.Context, token string) (model.RuntimeTarget, error) {
	id, err := s.Authenticate(ctx, token)
	if err != nil {
		return model.RuntimeTarget{}, model.NotFound("节点 token 无效")
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.RuntimeTarget{}, err
	}
	defer tx.Rollback()
	var config string
	if err = tx.QueryRowContext(ctx, "SELECT config FROM nodes WHERE id=?", id).Scan(&config); err != nil {
		return model.RuntimeTarget{}, err
	}
	var node model.Node
	if err = json.Unmarshal([]byte(config), &node); err != nil {
		return model.RuntimeTarget{}, err
	}
	if ipsentinel.ValidUUID(node.RuntimeTarget.ControllerID) && ipsentinel.ValidUUID(node.RuntimeTarget.NodeID) {
		return node.RuntimeTarget, nil
	}
	controller, err := serverSettingTx(ctx, tx, "ipsentinel_controller_id")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.RuntimeTarget{}, err
	}
	if !ipsentinel.ValidUUID(controller) {
		controller = ipsentinel.NewUUID()
		if err = setServerSettingTx(ctx, tx, "ipsentinel_controller_id", controller); err != nil {
			return model.RuntimeTarget{}, err
		}
	}
	node.RuntimeTarget = model.RuntimeTarget{ControllerID: controller, NodeID: ipsentinel.NewUUID()}
	updated, err := json.Marshal(node)
	if err != nil {
		return model.RuntimeTarget{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE nodes SET config=? WHERE id=?", string(updated), id); err != nil {
		return model.RuntimeTarget{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.RuntimeTarget{}, err
	}
	return node.RuntimeTarget, nil
}

func serverSettingTx(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, "SELECT value FROM server_settings WHERE key=?", key).Scan(&value)
	return value, err
}

func setServerSettingTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO server_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

func requireRuntimeAllowedTx(ctx context.Context, tx *sql.Tx, node string) error {
	config, err := nodeConfigTx(ctx, tx, node)
	if err != nil {
		return err
	}
	if config.RuntimeInstallDisabled {
		return model.Conflict("该节点已关闭主控执行；可在插件 Agent 页重新允许")
	}
	return nil
}
