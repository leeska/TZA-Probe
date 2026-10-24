package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// acceptAgentState runs inside the report/heartbeat transaction. Retired
// process sessions cannot become current again through delayed requests.
// Equal generations may refresh liveness for an ongoing failure.
func acceptAgentState(ctx context.Context, tx *sql.Tx, id string, state model.AgentState, now time.Time) (bool, error) {
	if state.Session == "" {
		return true, nil
	}
	var sequence int64
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT sequence,active FROM node_agent_sessions WHERE node_id=? AND session=?`, id, state.Session).Scan(&sequence, &active)
	if err == nil {
		if !active || state.Sequence < sequence {
			return false, nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE node_agent_sessions SET sequence=?,seen_at=? WHERE node_id=? AND session=?`, state.Sequence, now.Unix(), id, state.Session)
		return err == nil, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	// No unknown session may take ownership in a single request. A process
	// must echo a challenge for the current owner; a delayed request from a
	// process that has already exited cannot complete this round trip.
	var current, key string
	if err = tx.QueryRowContext(ctx, `SELECT token_hash,coalesce((SELECT session FROM node_agent_sessions WHERE node_id=nodes.id AND active=1),'') FROM nodes WHERE id=?`, id).Scan(&key, &current); err != nil {
		return false, err
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("agent-session-v1\n" + id + "\n" + state.Session + "\n" + current))
	challenge := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(state.Proof), []byte(challenge)) {
		return false, &model.AgentSessionChallenge{Challenge: challenge}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE node_agent_sessions SET active=0 WHERE node_id=? AND active=1`, id); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO node_agent_sessions(node_id,session,sequence,active,seen_at) VALUES(?,?,?,1,?)`, id, state.Session, state.Sequence, now.Unix())
	return err == nil, err
}
