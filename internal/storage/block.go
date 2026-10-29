package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/leeska/TZA-Probe/internal/model"
)

// BlockChecks uses the existing TCP job history. Holding the writer lock keeps
// a concurrent task edit/role change from mixing old evidence with new roles.
func (s *Store) BlockChecks(ctx context.Context, now time.Time) ([]model.BlockCheckView, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	tasks, err := s.Tasks(ctx)
	if err != nil {
		return nil, err
	}
	hasBlock := false
	for _, task := range tasks {
		hasBlock = hasBlock || task.Type == model.TaskBlock
	}
	if !hasBlock {
		return []model.BlockCheckView{}, nil
	}
	nodes, err := s.Nodes(ctx, now)
	if err != nil {
		return nil, err
	}
	byID := map[string]model.NodeView{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	out := []model.BlockCheckView{}
	for _, t := range tasks {
		if t.Type != model.TaskBlock || t.Block == nil {
			continue
		}
		b := t.Block
		v := model.BlockCheckView{TaskID: t.ID, Name: t.Name, Host: b.Host, Port: b.Port, Family: b.Family, Evidence: []model.BlockEvidence{}}
		for _, group := range []struct {
			role string
			ids  []string
		}{{"mainland", b.MainlandNodeIDs}, {"overseas", b.OverseasNodeIDs}} {
			for _, id := range group.ids {
				e := model.BlockEvidence{NodeID: id, NodeName: id, Role: group.role, State: "offline", Attempts: []model.BlockAttempt{}}
				n, exists := byID[id]
				if exists {
					e.NodeName = n.Name
				}
				if exists && n.Online {
					// The bound join excludes rows belonging to a removed binding.
					rows, err := s.query(ctx, `SELECT j.issued,j.finished,j.result FROM jobs j JOIN bindings b ON b.task_id=j.task_id AND b.node_id=j.node_id WHERE j.node_id=? AND j.task_id=? AND j.probe_key=? AND j.issued>=? ORDER BY j.issued DESC,j.id DESC LIMIT 2`, id, t.ID, t.Leaves()[0].ProbeKey(), now.Unix()-int64(b.Interval*2+30))
					if err != nil {
						return nil, err
					}
					for rows.Next() {
						var a model.BlockAttempt
						var finished sql.NullInt64
						var raw sql.NullString
						if err = rows.Scan(&a.Issued, &finished, &raw); err != nil {
							rows.Close()
							return nil, err
						}
						a.Finished = finished.Int64
						if raw.Valid {
							if json.Unmarshal([]byte(raw.String), &a.Result) != nil || a.Result.Validate() != nil {
								a.Result = model.Result{}
							}
						}
						e.Attempts = append(e.Attempts, a)
					}
					err = rows.Err()
					rows.Close()
					if err != nil {
						return nil, err
					}
					e.State = model.BlockEvidenceState(e.Attempts, b.Interval, now)
					if n.Metrics == nil || n.MetricsStale || !n.Metrics.AllowsFamily(b.Family) {
						e.State = "unavailable"
					}
				}
				v.Evidence = append(v.Evidence, e)
			}
		}
		v.Evaluate(t.Enabled, b.Interval)
		out = append(out, v)
	}
	return out, nil
}
