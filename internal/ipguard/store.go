package ipguard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Database struct {
	db    *sql.DB
	mutex sync.Mutex
	own   bool // Open made db, so Close closes it
}

// ddl creates the region drift tables. In the server they live in the main
// database (Migrations); Open creates them in a file of their own for tests
// and tools.
const ddl = `CREATE TABLE IF NOT EXISTS ipguard_policies(node_id TEXT PRIMARY KEY, revision INTEGER NOT NULL, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ipguard_jobs(id TEXT PRIMARY KEY,node_id TEXT NOT NULL,service TEXT NOT NULL,phase TEXT NOT NULL,policy TEXT NOT NULL,state TEXT NOT NULL,created_at INTEGER NOT NULL,started_at INTEGER NOT NULL DEFAULT 0,finished_at INTEGER NOT NULL DEFAULT 0,lease TEXT NOT NULL DEFAULT '',result TEXT NOT NULL DEFAULT '',dedupe TEXT NOT NULL UNIQUE);
CREATE INDEX IF NOT EXISTS ipguard_jobs_node_time ON ipguard_jobs(node_id,created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS ipguard_jobs_one_active ON ipguard_jobs(node_id) WHERE state IN ('queued','running');`

// New uses db, a database the region drift migrations have already run on
// (the server's main database). Close leaves db open.
func New(db *sql.DB) *Database { return &Database{db: db} }

// Open opens a standalone database file holding only the region drift tables.
func Open(path string) (*Database, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	file.Close()
	if err := os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA journal_mode=WAL;\n" + ddl); err != nil {
		db.Close()
		return nil, err
	}
	return &Database{db: db, own: true}, nil
}

func (store *Database) Close() error {
	if !store.own {
		return nil
	}
	return store.db.Close()
}

func (store *Database) Policies(ctx context.Context) (map[string]Policy, error) {
	rows, err := store.db.QueryContext(ctx, "SELECT node_id,value FROM ipguard_policies")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]Policy{}
	for rows.Next() {
		var node, raw string
		var policy Policy
		if err := rows.Scan(&node, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return nil, err
		}
		result[node] = policy
	}
	return result, rows.Err()
}

func (store *Database) SavePolicy(ctx context.Context, node string, policy Policy) (Policy, error) {
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Policy{}, err
	}
	defer tx.Rollback()
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM ipguard_policies WHERE node_id=?", node).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Policy{}, err
	}
	if revision != policy.Revision {
		return Policy{}, errors.New("配置已变化，请刷新")
	}
	policy.Revision++
	raw, _ := json.Marshal(policy)
	if _, err = tx.ExecContext(ctx, "INSERT INTO ipguard_policies VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET revision=excluded.revision,value=excluded.value", node, policy.Revision, string(raw)); err != nil {
		return Policy{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE ipguard_jobs SET state='cancelled',finished_at=? WHERE node_id=? AND state IN ('queued','running')", time.Now().Unix(), node); err != nil {
		return Policy{}, err
	}
	return policy, tx.Commit()
}

func (store *Database) Enqueue(ctx context.Context, node, service, phase, dedupe string) (Job, error) {
	if phase != "baseline" && phase != "followup" && phase != "experiment" {
		return Job{}, errors.New("任务阶段无效")
	}
	selected, ok := Lookup(service)
	if !ok || phase == "experiment" && selected.Experiment == "" {
		return Job{}, errors.New("服务不支持此操作")
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var raw string
	var policy Policy
	if err = tx.QueryRowContext(ctx, "SELECT value FROM ipguard_policies WHERE node_id=?", node).Scan(&raw); err != nil {
		return Job{}, errors.New("先设置出口节点")
	}
	if err = json.Unmarshal([]byte(raw), &policy); err != nil {
		return Job{}, err
	}
	if !policy.Enabled || !contains(policy.Services, service) {
		return Job{}, errors.New("节点或服务未启用")
	}
	if phase != "baseline" {
		parentID := strings.TrimPrefix(dedupe, phase+":")
		parent, err := scanJob(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM ipguard_jobs WHERE id=? AND node_id=? AND service=?", parentID, node, service))
		valid := err == nil && policy.AutoRecover && parent.Policy.Revision == policy.Revision && parent.State == "completed" && parent.CurrentSignal() && time.Now().Unix()-parent.FinishedAt < 1800
		if !valid || phase == "experiment" && (parent.Phase != "baseline" || parent.Result.Assessment != "other_region") || phase == "followup" && (parent.Phase != "experiment" || parent.Result.Assessment != "experiment_only") {
			return Job{}, errors.New("校正或复测缺少当前授权的前置地区检测")
		}
	}
	job := Job{ID: ID(), NodeID: node, Service: service, Phase: phase, Policy: policy, State: "queued", CreatedAt: time.Now().Unix()}
	if dedupe == "" {
		dedupe = job.ID
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO ipguard_jobs(id,node_id,service,phase,policy,state,created_at,dedupe) VALUES(?,?,?,?,?,'queued',?,?)", job.ID, node, service, phase, raw, job.CreatedAt, dedupe)
	if err != nil {
		return Job{}, errors.New("节点已有任务，或此时段已处理")
	}
	return job, tx.Commit()
}

func contains(values []string, value string) bool {
	for _, entry := range values {
		if entry == value {
			return true
		}
	}
	return false
}

const columns = "id,node_id,service,phase,policy,state,created_at,started_at,finished_at,lease,result"

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var job Job
	var policy, result string
	err := row.Scan(&job.ID, &job.NodeID, &job.Service, &job.Phase, &policy, &job.State, &job.CreatedAt, &job.StartedAt, &job.FinishedAt, &job.Lease, &result)
	if err != nil {
		return job, err
	}
	if err = json.Unmarshal([]byte(policy), &job.Policy); err != nil {
		return job, err
	}
	if result != "" {
		err = json.Unmarshal([]byte(result), &job.Result)
	}
	return job, err
}

func (store *Database) Jobs(ctx context.Context, node string) ([]Job, error) {
	query := "SELECT " + columns + " FROM ipguard_jobs WHERE node_id=? ORDER BY created_at DESC,rowid DESC LIMIT 100"
	arguments := []any{node}
	if node == "" {
		query = "SELECT " + columns + " FROM ipguard_jobs WHERE id IN (SELECT id FROM (SELECT id,row_number() OVER (PARTITION BY node_id,service ORDER BY created_at DESC,rowid DESC) AS position FROM ipguard_jobs) WHERE position<=3) ORDER BY created_at DESC,rowid DESC"
		arguments = nil
	}
	rows, err := store.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		job.Lease = ""
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (store *Database) Claim(ctx context.Context) (*Job, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if _, err := store.db.ExecContext(ctx, "UPDATE ipguard_jobs SET state='expired',finished_at=? WHERE state='running' AND started_at+json_extract(policy,'$.timeout_seconds')+10<?", time.Now().Unix(), time.Now().Unix()); err != nil {
		return nil, err
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE ipguard_jobs SET state='expired',finished_at=? WHERE state='queued' AND created_at<?", time.Now().Unix(), time.Now().Add(-5*time.Minute).Unix()); err != nil {
		return nil, err
	}
	var running int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM ipguard_jobs WHERE state='running'").Scan(&running); err != nil {
		return nil, err
	}
	if running > 0 {
		return nil, nil
	}
	job, err := scanJob(store.db.QueryRowContext(ctx, "SELECT "+columns+" FROM ipguard_jobs WHERE state='queued' ORDER BY created_at,rowid LIMIT 1"))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.Lease = ID()
	job.StartedAt = time.Now().Unix()
	job.State = "running"
	_, err = store.db.ExecContext(ctx, "UPDATE ipguard_jobs SET state='running',lease=?,started_at=? WHERE id=? AND state='queued'", job.Lease, job.StartedAt, job.ID)
	return &job, err
}

func (store *Database) Running(ctx context.Context, id, lease string) (*Job, error) {
	job, err := scanJob(store.db.QueryRowContext(ctx, "SELECT "+columns+" FROM ipguard_jobs WHERE id=? AND lease=? AND state='running'", id, lease))
	if err != nil {
		return nil, err
	}
	if time.Now().Unix() > job.StartedAt+int64(job.Policy.TimeoutSeconds)+10 {
		return nil, errors.New("任务租约已到期")
	}
	return &job, nil
}

func (store *Database) Complete(ctx context.Context, id, lease string, result Result) error {
	job, err := store.Running(ctx, id, lease)
	if err != nil {
		return err
	}
	if result.Execution != "completed" && result.Execution != "failed" {
		return errors.New("任务结果无效")
	}
	if len(result.DeniedOrigins) > 32 || len(result.Reason) > 256 || len(result.BrowserVersion) > 100 || len(result.Page.Value) > 100 || len(result.Page.FinalOrigin) > 256 || len(result.Page.Signal) > 100 || result.NetworkBytes < 0 {
		return errors.New("任务证据超限")
	}
	for _, origin := range result.DeniedOrigins {
		if len(origin) > 256 {
			return errors.New("任务证据超限")
		}
	}
	if terms := result.GoogleTerms; terms != nil {
		if len(terms.Reason) > 256 || len(terms.Page.Value) > 100 || len(terms.Page.FinalOrigin) > 256 || len(terms.Page.Signal) > 100 {
			return errors.New("任务证据超限")
		}
		terms.Assessment = "unknown"
	}
	result.Assessment = "unknown"
	if job.Phase != "experiment" && result.GeolocationOverride {
		result.Execution = "failed"
		result.Reason = "observation_geolocation_override"
	}
	if result.Execution == "completed" && result.Reason == "" && result.BrowserVersion != "" && result.NetworkBytes <= job.Policy.NetworkBytes && result.Sandbox && result.NetworkIsolated && result.EgressBefore == job.Policy.ExpectedIP && result.EgressAfter == job.Policy.ExpectedIP && result.Page.Outcome == "normal" && PermittedPageOrigin(job.Service, result.Page.FinalOrigin) {
		if job.Phase == "experiment" && result.Page.Signal == CountrySignal(job.Service) {
			result.Assessment = "experiment_only"
		} else if job.Phase != "experiment" {
			country := result.Page.Country(job.Service)
			if country != "" && country == job.Policy.Country {
				result.Assessment = "target_observed"
			} else if country != "" {
				result.Assessment = "other_region"
			}
		}
	}
	if terms := result.GoogleTerms; terms != nil && job.Service == "google" && job.Phase != "experiment" && !result.GeolocationOverride && result.BrowserVersion != "" && result.NetworkBytes <= job.Policy.NetworkBytes && result.Sandbox && result.NetworkIsolated && result.EgressBefore == job.Policy.ExpectedIP && result.EgressAfter == job.Policy.ExpectedIP && contains([]string{"", "challenge_detected", "challenge_or_unavailable", "http_denied", "http_forbidden"}, result.Reason) && terms.Reason == "" && terms.Page.Outcome == "normal" && terms.Page.FinalOrigin == "https://policies.google.com" && terms.Page.Signal == "google_terms_country" {
		country := terms.Page.Value
		if len(country) == 2 && country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z' {
			terms.Assessment = "other_region"
			if country == job.Policy.Country {
				terms.Assessment = "target_observed"
			}
		}
	}
	raw, _ := json.Marshal(result)
	updated, err := store.db.ExecContext(ctx, "UPDATE ipguard_jobs SET state=?,finished_at=?,result=? WHERE id=? AND lease=? AND state='running'", result.Execution, time.Now().Unix(), string(raw), id, lease)
	if err != nil {
		return err
	}
	count, _ := updated.RowsAffected()
	if count != 1 {
		return errors.New("任务状态已变化")
	}
	return nil
}

func (store *Database) Cancel(ctx context.Context, id string) error {
	_, err := store.db.ExecContext(ctx, "UPDATE ipguard_jobs SET state='cancelled',finished_at=? WHERE id=? AND state IN ('queued','running')", time.Now().Unix(), id)
	return err
}
func (store *Database) Recover(ctx context.Context) error {
	_, err := store.db.ExecContext(ctx, "UPDATE ipguard_jobs SET state='interrupted',finished_at=? WHERE state='running'", time.Now().Unix())
	return err
}
