package storage

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"time"
)

// tcp_buckets 是 TCP 探测结果按 6 分钟一格的预聚合：每台节点 × 每条探针 × 每格一行，
// 存延迟之和与个数、发包与收包数。网络历史 24 小时 / 3 天 / 7 天的分组步长（hours×15 秒）
// 都是 6 分钟的整数倍，于是这三档直接在格子上再聚合一次，结果与逐行聚合相同：以前 7 天
// 要把一台节点十几万行探测结果逐行 json_extract（线上 1 秒多，1 核机更慢），现在读的是
// 几千行格子。1 小时 / 6 小时的步长不是 6 分钟的倍数，本来也只读几千行，仍走逐行查询。
//
// 写入靠 jobs 上的触发器（结果写入时加进对应格子），所以无论哪条路径写结果都不会漏。
// 删除不走触发器（Clean 一次删几十万行 jobs，逐行回减太贵），而是各删各的：
//   - 保留期：Clean 按 bucket 删；
//   - 改任务 / 删任务：purge 按 task_id 删 job_min 不超过截止 rowid 的格子（含旧定义结果的
//     格子整格删，改任务那 6 分钟里新定义的少量结果随之丢掉，换来不必逐行回减）；
//   - 删节点：purge 按 node_id 删。
//
// 升级上来的库已有一周结果，迁移时只建表与触发器、记下起点，旧结果由后台按小时分批回填
// （见 backfillTCPBuckets）；回填完成前，起点之前的时间段仍走逐行查询。
const tcpBucketSeconds = 360

// tcpBucketKeySQL 是触发器里一行的分组键，与 jobKeySQL 同一套规则（NEW. 前缀）。
func tcpBucketKeySQL() string {
	return `CASE WHEN NEW.probe_key<>'' THEN NEW.probe_key WHEN json_valid(NEW.task) THEN ` + probeKeyExpr("NEW.") + ` END`
}

// tcpBucketUpsert 把一行或一批聚合好的结果加进格子。插入的列顺序固定，见 tcpBucketsSchema。
const tcpBucketUpsert = ` ON CONFLICT(node_id,bucket,probe_key) DO UPDATE SET
  task=CASE WHEN excluded.finished>=tcp_buckets.finished THEN excluded.task ELSE tcp_buckets.task END,
  finished=max(tcp_buckets.finished,excluded.finished),
  job_min=min(tcp_buckets.job_min,excluded.job_min),
  lat_sum=tcp_buckets.lat_sum+excluded.lat_sum,
  lat_n=tcp_buckets.lat_n+excluded.lat_n,
  sent=tcp_buckets.sent+excluded.sent,
  received=tcp_buckets.received+excluded.received`

func tcpBucketTrigger(name, event, when string) string {
	return `CREATE TRIGGER IF NOT EXISTS ` + name + ` AFTER ` + event + ` ON jobs WHEN ` + when + ` BEGIN
  INSERT INTO tcp_buckets(node_id,bucket,probe_key,task_id,job_min,finished,lat_sum,lat_n,sent,received,task)
  SELECT NEW.node_id,NEW.finished-NEW.finished%` + strconv.Itoa(tcpBucketSeconds) + `,k,NEW.task_id,NEW.rowid,NEW.finished,
         COALESCE(lat,0),lat IS NOT NULL,COALESCE(json_extract(r,'$.sent'),0),COALESCE(json_extract(r,'$.received'),0),NEW.task
  FROM (SELECT k,r,CASE WHEN json_extract(r,'$.status')='ok' THEN json_extract(r,'$.latency') END AS lat
        FROM (SELECT ` + tcpBucketKeySQL() + ` AS k,CASE WHEN json_valid(NEW.result) THEN NEW.result END AS r))
  WHERE substr(k,length(NEW.task_id)+2,4)='tcp|'` + tcpBucketUpsert + `;
END;`
}

// tcpBucketsSchema 是迁移 15 建的表、索引与触发器。tcp_buckets_state 一行：live_from 之后
// 写入的结果由触发器计入；[next, live_from) 这段旧结果还没回填，next>=live_from 即完成。
func tcpBucketsSchema() string {
	return `CREATE TABLE IF NOT EXISTS tcp_buckets(node_id TEXT NOT NULL, bucket INTEGER NOT NULL, probe_key TEXT NOT NULL, task_id TEXT NOT NULL, job_min INTEGER NOT NULL, finished INTEGER NOT NULL, lat_sum REAL NOT NULL, lat_n INTEGER NOT NULL, sent INTEGER NOT NULL, received INTEGER NOT NULL, task TEXT NOT NULL, PRIMARY KEY(node_id,bucket,probe_key));
CREATE INDEX IF NOT EXISTS tcp_buckets_age ON tcp_buckets(bucket);
CREATE INDEX IF NOT EXISTS tcp_buckets_task ON tcp_buckets(task_id);
CREATE TABLE IF NOT EXISTS tcp_buckets_state(id INTEGER PRIMARY KEY CHECK(id=1), next INTEGER NOT NULL, live_from INTEGER NOT NULL);
` + tcpBucketTrigger("jobs_tcp_bucket_insert", "INSERT", "NEW.finished IS NOT NULL") + `
` + tcpBucketTrigger("jobs_tcp_bucket_finish", "UPDATE OF finished", "OLD.finished IS NULL AND NEW.finished IS NOT NULL")
}

// migrateTCPBuckets creates the buckets and schedules the backfill of the
// results already stored. An empty jobs table (a fresh database) has nothing
// to backfill and is ready at once.
func migrateTCPBuckets(db *sql.DB, now time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(tcpBucketsSchema()); err != nil {
		return err
	}
	liveFrom := now.Unix()
	next := now.Add(-JobsRetention).Unix()
	next -= next % tcpBucketSeconds
	var finished int
	if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM jobs WHERE finished IS NOT NULL)").Scan(&finished); err != nil {
		return err
	}
	if finished == 0 {
		next = liveFrom
	}
	if _, err = tx.Exec("INSERT INTO tcp_buckets_state(id,next,live_from) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET next=excluded.next,live_from=excluded.live_from", next, liveFrom); err != nil {
		return err
	}
	if _, err = tx.Exec("PRAGMA user_version=15"); err != nil {
		return err
	}
	return tx.Commit()
}

// tcpBackfillWindow is how much finish time one backfill statement covers: on
// ten nodes an hour of results is about ten thousand rows, a few tens of
// milliseconds under the write lock.
const tcpBackfillWindow = time.Hour

// backfillTCPBuckets adds the results finished before the buckets existed, a
// window at a time with the write lock released in between, oldest first. It
// resumes where it stopped after a restart (the state row records how far it
// got, in the same transaction as each window).
func (s *Store) backfillTCPBuckets(ctx context.Context) error {
	var next, liveFrom int64
	if err := s.queryRow(ctx, "SELECT next,live_from FROM tcp_buckets_state WHERE id=1").Scan(&next, &liveFrom); err != nil {
		return err
	}
	if next >= liveFrom {
		s.tcpBucketsFrom.Store(0)
		return nil
	}
	s.tcpBucketsFrom.Store(liveFrom)
	start, rows := time.Now(), int64(0)
	window := int64(tcpBackfillWindow / time.Second)
	insert := `INSERT INTO tcp_buckets(node_id,bucket,probe_key,task_id,job_min,finished,lat_sum,lat_n,sent,received,task)
	  SELECT node_id,finished-finished%` + strconv.Itoa(tcpBucketSeconds) + `,k,task_id,min(id),max(finished),COALESCE(sum(lat),0),count(lat),COALESCE(sum(json_extract(r,'$.sent')),0),COALESCE(sum(json_extract(r,'$.received')),0),task
	  FROM (SELECT node_id,task_id,task,finished,id,k,r,CASE WHEN json_extract(r,'$.status')='ok' THEN json_extract(r,'$.latency') END AS lat
	        FROM (SELECT node_id,task_id,task,finished,rowid AS id,` + jobKeySQL + ` AS k,CASE WHEN json_valid(result) THEN result END AS r
	              FROM jobs WHERE issued>=? AND issued<? AND finished>=? AND finished<?)
	        WHERE substr(k,length(task_id)+2,4)='tcp|'
	          AND NOT EXISTS (SELECT 1 FROM purges WHERE kind='` + purgeTaskJobs + `' AND target=task_id AND cutoff>=id)
	          AND NOT EXISTS (SELECT 1 FROM purges WHERE kind='` + purgeNodeRows + `' AND target=node_id))
	  GROUP BY node_id,k,finished/` + strconv.Itoa(tcpBucketSeconds) + `
	  ` + tcpBucketUpsert
	for next < liveFrom {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(next+window, liveFrom)
		n, err := s.backfillWindow(ctx, insert, next, end)
		if err != nil {
			return err
		}
		rows += n
		next = end
	}
	s.tcpBucketsFrom.Store(0)
	slog.Info("network history buckets backfilled", "buckets", rows, "took", time.Since(start).Round(time.Millisecond))
	return nil
}

func (s *Store) backfillWindow(ctx context.Context, insert string, from, to int64) (int64, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// TCP 结果只在下发后 90 秒内收（见 Finish），issued 的下界放宽两分钟就不会漏行，
	// 又能走 jobs_age 索引。
	r, err := tx.ExecContext(ctx, insert, from-tcpFinishSlack, to, from, to)
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	if _, err = tx.ExecContext(ctx, "UPDATE tcp_buckets_state SET next=? WHERE id=1", to); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
