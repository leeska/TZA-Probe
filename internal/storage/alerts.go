package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/leeska/TZA-Probe/internal/model"
)

// AlertRules lists every rule in the order they were created.
func (s *Store) AlertRules(ctx context.Context) ([]model.AlertRule, error) {
	return alertRules(ctx, s.read)
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func alertRules(ctx context.Context, db querier) ([]model.AlertRule, error) {
	rows, err := db.QueryContext(ctx, "SELECT config FROM alert_rules ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AlertRule{}
	for rows.Next() {
		var config string
		if err = rows.Scan(&config); err != nil {
			return nil, err
		}
		var r model.AlertRule
		if err = json.Unmarshal([]byte(config), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveAlertRule creates or replaces one rule and returns its id. It refuses a
// node that does not exist and a rule that would compete with another of the
// same kind and scope on some node (see AlertRule.Overlaps), all inside the
// write lock so two saves cannot both pass the check.
func (s *Store) SaveAlertRule(ctx context.Context, r model.AlertRule, create bool) (string, error) {
	if err := r.Validate(); err != nil {
		return "", model.Invalid(err.Error())
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	rules, err := alertRules(ctx, tx)
	if err != nil {
		return "", err
	}
	if create {
		if len(rules) >= model.MaxAlertRules {
			return "", model.Invalid("提醒规则过多")
		}
		r.ID = model.Secret()[:16]
	} else if !slices.ContainsFunc(rules, func(o model.AlertRule) bool { return o.ID == r.ID }) {
		return "", ErrNotFound
	}
	for _, id := range append(slices.Clone(r.NodeIDs), r.Exclude...) {
		var one int
		if err = tx.QueryRowContext(ctx, "SELECT 1 FROM nodes WHERE id=?", id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return "", model.Invalid("节点不存在：" + id)
		} else if err != nil {
			return "", err
		}
	}
	if err = r.Overlaps(rules); err != nil {
		return "", model.Invalid(err.Error())
	}
	if create {
		_, err = tx.ExecContext(ctx, "INSERT INTO alert_rules(id,config) VALUES(?,?)", r.ID, encode(r))
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE alert_rules SET config=? WHERE id=?", encode(r), r.ID)
	}
	if err != nil {
		return "", err
	}
	return r.ID, tx.Commit()
}

func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	r, err := s.db.ExecContext(ctx, "DELETE FROM alert_rules WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// forgetNodeInRules drops a deleted node from every rule's lists. A rule that
// named only that node covers nothing any more and goes with it.
func forgetNodeInRules(ctx context.Context, tx *sql.Tx, id string) error {
	rules, err := alertRules(ctx, tx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if !slices.Contains(r.NodeIDs, id) && !slices.Contains(r.Exclude, id) {
			continue
		}
		r.NodeIDs = slices.DeleteFunc(r.NodeIDs, func(v string) bool { return v == id })
		r.Exclude = slices.DeleteFunc(r.Exclude, func(v string) bool { return v == id })
		if r.Scope == model.ScopeNodes && len(r.NodeIDs) == 0 {
			_, err = tx.ExecContext(ctx, "DELETE FROM alert_rules WHERE id=?", r.ID)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE alert_rules SET config=? WHERE id=?", encode(r), r.ID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
