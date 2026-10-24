package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"log/slog"
	"math"
	"time"
)

// 大批量删除（改任务清历史、删任务、删节点、保留期清理）不能在一个事务里做完：所有写入
// 共用一把写锁，10 台 × 8 目标一周的数据，改一次任务要删 186 万行、持锁 95 秒，这期间
// 所有 Agent 的上报与领任务都在排队，15 秒就超时。所以这些删除拆成两段：
//   - 事务里只做立刻要生效的部分（配置、绑定、in-flight 任务、物化表 latest 里的行），
//     面板与调度马上就是新状态；
//   - 历史行记进 purges 表，由后台一批一批删（每批持锁约 0.1 秒，见 purgeChunkTarget），批与批之间放开写锁。
//
// purges 是持久的：进程在删到一半时重启，Open 之后会接着删。

// A chunk is sized so it holds the write lock for about purgeChunkTarget:
// measured per-row cost ranges from ~50µs (pages cached, rows adjacent) to over
// a millisecond (a cold cache after a restart, rows scattered over the indexes),
// so a fixed size is either far too slow or holds Agents up for seconds. The
// size starts small, doubles while chunks finish quickly and halves when one
// runs long.
const (
	purgeChunkTarget = 100 * time.Millisecond
	purgeChunkStart  = 200
	purgeChunkMin    = 20
	purgeChunkMax    = 5000
)

// Purge kinds, see runPurge.
const (
	purgeTaskJobs = "task"
	purgeNodeRows = "node"
)

// startPurger runs pending purges now and whenever a writer queues one.
func (s *Store) startPurger() {
	ctx, cancel := context.WithCancel(context.Background())
	s.purgeWake = make(chan struct{}, 1)
	s.purgeStop = cancel
	s.purgeDone = make(chan struct{})
	s.wakePurger()
	go func() {
		defer close(s.purgeDone)
		// 升级上来的库先把网络历史格子补齐（每批持锁几十毫秒），再处理删除队列。
		if err := s.backfillTCPBuckets(ctx); err != nil && ctx.Err() == nil {
			slog.Error("network history backfill failed; older ranges keep the slow path until the next start", "error", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.purgeWake:
			}
			if err := s.runPurges(ctx); err != nil && ctx.Err() == nil {
				slog.Error("background purge failed", "error", err)
				// 失败的那条留在 purges 里，下次唤醒（或下一次 Clean）再试。
			}
		}
	}()
}

func (s *Store) stopPurger() {
	if s.purgeStop == nil {
		return
	}
	s.purgeStop()
	<-s.purgeDone
}

// wakePurger asks the background purger to look at the queue. It never blocks.
func (s *Store) wakePurger() {
	if s.purgeWake == nil {
		return
	}
	select {
	case s.purgeWake <- struct{}{}:
	default:
	}
}

// queuePurge records a purge inside the caller's transaction, so it exists
// exactly when the change that needs it is committed.
func queuePurge(ctx context.Context, tx *sql.Tx, kind, target string, cutoff int64) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO purges(kind,target,cutoff) VALUES(?,?,?)", kind, target, cutoff)
	return err
}

// jobsCutoff is the highest job rowid committed so far: a purge of a task's
// history removes rows up to it and never the jobs leased after the edit.
func jobsCutoff(ctx context.Context, tx *sql.Tx) (int64, error) {
	var cutoff sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT max(rowid) FROM jobs").Scan(&cutoff)
	return cutoff.Int64, err
}

// runPurges works through the queue oldest first.
func (s *Store) runPurges(ctx context.Context) error {
	for {
		var id, cutoff int64
		var kind, target string
		err := s.queryRow(ctx, "SELECT id,kind,target,cutoff FROM purges ORDER BY id LIMIT 1").Scan(&id, &kind, &target, &cutoff)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		start := time.Now()
		removed, err := s.runPurge(ctx, kind, target, cutoff)
		if err != nil {
			return err
		}
		if err = s.locked(ctx, "DELETE FROM purges WHERE id=?", id); err != nil {
			return err
		}
		slog.Info("purged history", "kind", kind, "rows", removed, "took", time.Since(start).Round(time.Millisecond))
	}
}

func (s *Store) runPurge(ctx context.Context, kind, target string, cutoff int64) (int64, error) {
	switch kind {
	case purgeTaskJobs:
		// 格子先删：含旧结果的格子（job_min 不超过截止）整格去掉，见 tcp_buckets.go。
		buckets, err := s.deleteChunks(ctx, "tcp_buckets", "task_id=? AND job_min<=?", target, cutoff)
		if err != nil {
			return buckets, err
		}
		jobs, err := s.deleteChunks(ctx, "jobs", "task_id=? AND rowid<=?", target, cutoff)
		return buckets + jobs, err
	case purgeNodeRows:
		jobs, err := s.deleteChunks(ctx, "jobs", "node_id=?", target)
		if err != nil {
			return jobs, err
		}
		buckets, err := s.deleteChunks(ctx, "tcp_buckets", "node_id=?", target)
		if err != nil {
			return jobs + buckets, err
		}
		samples, err := s.deleteChunks(ctx, "samples", "node_id=?", target)
		return jobs + buckets + samples, err
	default:
		slog.Warn("unknown purge kind dropped", "kind", kind)
		return 0, nil
	}
}

// deleteChunks deletes the rows of table matching where, a chunk at a time,
// each chunk under the write lock in its own statement (see purgeChunkTarget).
func (s *Store) deleteChunks(ctx context.Context, table, where string, args ...any) (int64, error) {
	query := "DELETE FROM " + table + " WHERE rowid IN (SELECT rowid FROM " + table + " WHERE " + where + " LIMIT ?)"
	var total int64
	size := purgeChunkStart
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		s.writes.Lock()
		began := time.Now()
		r, err := s.db.ExecContext(ctx, query, append(args, size)...)
		took := time.Since(began)
		s.writes.Unlock()
		if err != nil {
			return total, err
		}
		n, _ := r.RowsAffected()
		total += n
		if n < int64(size) {
			return total, nil
		}
		switch {
		case took < purgeChunkTarget/2:
			size = min(size*2, purgeChunkMax)
		case took > purgeChunkTarget*2:
			size = max(size/2, purgeChunkMin)
		}
	}
}

// locked runs one statement under the write lock.
func (s *Store) locked(ctx context.Context, query string, args ...any) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// withoutCascade runs fn in a transaction on the writer connection with foreign
// key enforcement off, so deleting a task or a node row does not cascade into
// its whole history inside the lock. fn must delete every small child table
// itself and queue a purge for the large ones; until the purge finishes those
// rows are orphans that no read path reaches (every one starts from the nodes,
// tasks, bindings or latest tables). The caller holds the write lock.
func (s *Store) withoutCascade(ctx context.Context, fn func(*sql.Tx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// The pragma is a no-op inside a transaction, so it is set on the connection.
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
			// Never hand a connection without cascades back to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// noCutoff purges every row of the target.
const noCutoff = math.MaxInt64

const (
	// unlockFinishWindow is how late an unlock round may report. One round is
	// minutes, not seconds (see probe.unlockBudget).
	unlockFinishWindow = 15 * time.Minute
	samplesRetention   = 30 * 24 * time.Hour
	trafficRetention   = 400 * 24 * time.Hour
	// 小时桶服务窗口内平均带宽（窗口上限 168 小时），也用来补周期首日那一段
	// （锚点不在 00:00 时，日桶整天算会把首日整段丢掉，见 cycleStartSlice）——
	// 月周期最长 31 天，留 45 天保证周期第一天的小时桶还在。
	trafficHourRetention = 45 * 24 * time.Hour
	JobsRetention        = 7 * 24 * time.Hour
	eventsRetention      = 90 * 24 * time.Hour
	// routeRetryDelay 是一次回程补测的等待时长：抖动通常就是十几秒到一两分钟的事，
	// 等两分钟既避开正在过境的那一段，又不至于让面板把「未知」挂成常态。
	routeRetryDelay = 2 * time.Minute
	// retryRetention 只兜底：补测在 Lease 领走时就删了，留下来的都是节点离线时排下的
	// 死行（也可能是已删任务的），一天之后一律清掉。
	retryRetention = 24 * time.Hour
)

// Clean applies the retention periods. Each table is deleted a chunk of rows
// at a time with the write lock released in between: an hour of expired jobs
// is a quarter of a million rows on ten nodes, 11s under one lock, long enough
// for every Agent's report to time out.
func (s *Store) Clean(ctx context.Context, now time.Time) error {
	for _, q := range []struct {
		table, column string
		age           time.Duration
	}{{"samples", "at", samplesRetention}, {"traffic", "day", trafficRetention}, {"traffic_hour", "hour", trafficHourRetention}, {"jobs", "issued", JobsRetention}, {"tcp_buckets", "bucket", JobsRetention + tcpBucketSeconds*time.Second}, {"events", "at", eventsRetention}, {"retries", "at", retryRetention}} {
		if _, err := s.deleteChunks(ctx, q.table, q.column+"<?", now.Add(-q.age).Unix()); err != nil {
			return err
		}
	}
	// Retired sessions outlive the Agent's 15-second HTTP request budget by
	// seven days; keep the current watermark even for a long-offline node.
	if _, err := s.deleteChunks(ctx, "node_agent_sessions", "active=0 AND seen_at<?", now.Add(-7*24*time.Hour).Unix()); err != nil {
		return err
	}
	// 后台删除的兜底：上次唤醒时失败的那条也在这里再试一次。
	s.wakePurger()
	return nil
}
