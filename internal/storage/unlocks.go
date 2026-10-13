package storage

import (
	"context"
	"database/sql"
	"sort"

	"github.com/leeska/TZA-Probe/internal/model"
)

// clearUnlocks drops the stored verdicts of the named nodes.
func clearUnlocks(ctx context.Context, tx *sql.Tx, nodeIDs []string) error {
	for _, id := range nodeIDs {
		if _, err := tx.ExecContext(ctx, "DELETE FROM unlocks WHERE node_id=?", id); err != nil {
			return err
		}
	}
	return nil
}

// Unlocks reads one node's stored verdicts as the panel summary, nil when the
// machine has never run the check.
func (s *Store) Unlocks(ctx context.Context, node string) (*model.UnlockSummary, error) {
	rows, err := s.query(ctx, "SELECT service,status,region,detail,tested_at FROM unlocks WHERE node_id=?", node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []model.UnlockResult{}
	testedAt := int64(0)
	for rows.Next() {
		var u model.UnlockResult
		var at int64
		if err = rows.Scan(&u.Service, &u.Status, &u.Region, &u.Detail, &at); err != nil {
			return nil, err
		}
		results = append(results, u)
		testedAt = max(testedAt, at)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// 未知服务（比本 Server 新的 Agent）只跳过自己：其余结论照旧显示。目录里删掉的
	// 项同理，不再出现在面板上。
	known := results[:0]
	for _, u := range results {
		if model.KnownUnlockService(u.Service) {
			known = append(known, u)
		}
	}
	return model.SummarizeUnlocks(orderUnlocks(known), testedAt), nil
}

// unlockSummaries reads every node's verdicts in one grouped query for /nodes.
func (s *Store) unlockSummaries(ctx context.Context) (map[string]*model.UnlockSummary, error) {
	rows, err := s.query(ctx, "SELECT node_id,service,status,region,detail,tested_at FROM unlocks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[string][]model.UnlockResult{}
	tested := map[string]int64{}
	for rows.Next() {
		var id string
		var u model.UnlockResult
		var at int64
		if err = rows.Scan(&id, &u.Service, &u.Status, &u.Region, &u.Detail, &at); err != nil {
			return nil, err
		}
		if !model.KnownUnlockService(u.Service) {
			continue
		}
		grouped[id] = append(grouped[id], u)
		tested[id] = max(tested[id], at)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]*model.UnlockSummary, len(grouped))
	for id, services := range grouped {
		out[id] = model.SummarizeUnlocks(orderUnlocks(services), tested[id])
	}
	return out, nil
}

// orderUnlocks sorts verdicts into catalogue order so the panel lists services
// the same way on every node.
func orderUnlocks(services []model.UnlockResult) []model.UnlockResult {
	rank := map[string]int{}
	for i, id := range model.UnlockServiceIDs() {
		rank[id] = i
	}
	sort.SliceStable(services, func(i, j int) bool { return rank[services[i].Service] < rank[services[j].Service] })
	return services
}
