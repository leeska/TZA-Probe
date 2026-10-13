package storage

// probeKeyExpr 是 model.Task.ProbeKey 的 SQL 版本，只在 jobs.probe_key 为空时兜底
// （正常写入路径由 Go 侧算好；测试与历史行可能没带）。prefix 传 "NEW." 或 ""：
// 触发器里必须写 NEW.task_id，迁移的 UPDATE 里是裸列。两边格式必须一致：
// <任务 id>|<kind>|<family>|<host>|<port>|<region>|<carrier>。
// 每个字段都 COALESCE 到空串：老数据里缺 key 的行 json_extract 返回 NULL，直接
// 拼接会让整键变 NULL（写入 NOT NULL 列会失败）；用空串则所有缺字段的行仍归一组，
// 与旧查询「NULL 与 NULL 同组」的行为一致。
func probeKeyExpr(prefix string) string {
	field := func(key string) string {
		return "COALESCE(json_extract(" + prefix + "task,'$." + key + "'),'')"
	}
	return "COALESCE(" + prefix + `task_id,'')||'|'||` + field("kind") + "||'|'||" + field("family") + "||'|'||" +
		field("host") + "||'|'||" + field("port") + "||'|'||" + field("region") + "||'|'||" + field("carrier")
}

// latestKeySQL 取一行的分组键：优先用写入时算好的 jobs.probe_key。
func latestKeySQL() string {
	return `CASE WHEN NEW.probe_key<>'' THEN NEW.probe_key ELSE ` + probeKeyExpr("NEW.") + ` END`
}

// latestSchema 是「每个分组最新一次结果」的物化表与它的触发器。jobs 只追加，
// 读路径原先每次都要在窗口内重新 json_extract 上万行（线上 65ms/节点、首页
// 扇出 465ms），这张表把同一份结果留在几十行里（实测 0.03ms/节点）。
//   - INSERT：比 (issued,id) 更新才覆盖，与旧 SQL 的 ORDER BY issued DESC,id DESC 一致；
//   - UPDATE：只在被更新的行正是该组的最新那条时刷新（乱序完成的老任务不覆盖新的）；
//   - DELETE：Clean 的保留期删除与 SaveTask 的「改任务即清历史」都靠它收尾。
//
// json_valid 守卫保留：任务 JSON 坏掉的行不进物化表，这一组继续显示更早那条，
// 与旧查询用 json_valid 过滤掉坏行的行为一致。
var latestSchema = `
CREATE TABLE latest(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, probe_key TEXT NOT NULL, job_id TEXT NOT NULL, task TEXT NOT NULL, issued INTEGER NOT NULL, finished INTEGER, result TEXT, PRIMARY KEY(node_id,probe_key));
CREATE TRIGGER jobs_latest_insert AFTER INSERT ON jobs WHEN json_valid(NEW.task) BEGIN
  INSERT INTO latest(node_id,probe_key,job_id,task,issued,finished,result)
  VALUES(NEW.node_id,` + latestKeySQL() + `,NEW.id,NEW.task,NEW.issued,NEW.finished,NEW.result)
  ON CONFLICT(node_id,probe_key) DO UPDATE SET job_id=excluded.job_id,task=excluded.task,issued=excluded.issued,finished=excluded.finished,result=excluded.result
  WHERE excluded.issued>latest.issued OR (excluded.issued=latest.issued AND excluded.job_id>latest.job_id);
END;
CREATE TRIGGER jobs_latest_update AFTER UPDATE OF task,issued,finished,result ON jobs WHEN json_valid(NEW.task) BEGIN
  UPDATE latest SET task=NEW.task,issued=NEW.issued,finished=NEW.finished,result=NEW.result
  WHERE job_id=NEW.id AND node_id=NEW.node_id;
END;
CREATE TRIGGER jobs_latest_delete AFTER DELETE ON jobs BEGIN
  DELETE FROM latest WHERE job_id=OLD.id;
END;`

// schema is the version 1 baseline. A fresh database runs it and then every
// migration in Open, exactly like an upgraded one, so the migrations are the
// single description of each later table and column and user_version always
// says what is really there.
const schema = `BEGIN;
CREATE TABLE nodes(id TEXT PRIMARY KEY, config TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, last_seen INTEGER NOT NULL DEFAULT 0, metrics TEXT);
CREATE TABLE samples(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, at INTEGER NOT NULL, payload TEXT NOT NULL, PRIMARY KEY(node_id,at));
CREATE INDEX samples_age ON samples(at);
CREATE TABLE traffic(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, day INTEGER NOT NULL, incoming INTEGER NOT NULL, outgoing INTEGER NOT NULL, PRIMARY KEY(node_id,day));
CREATE TABLE tasks(id TEXT PRIMARY KEY, config TEXT NOT NULL);
CREATE TABLE bindings(task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, due INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(task_id,node_id));
CREATE TABLE jobs(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, task TEXT NOT NULL, issued INTEGER NOT NULL, finished INTEGER, result TEXT);
CREATE INDEX jobs_node ON jobs(node_id,issued);
CREATE INDEX jobs_age ON jobs(issued);
CREATE TABLE alert_state(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, kind TEXT NOT NULL, since INTEGER NOT NULL, active INTEGER NOT NULL, PRIMARY KEY(node_id,kind));
CREATE TABLE events(id INTEGER PRIMARY KEY AUTOINCREMENT, node_id TEXT NOT NULL, node_name TEXT NOT NULL, kind TEXT NOT NULL, message TEXT NOT NULL, active INTEGER NOT NULL, at INTEGER NOT NULL, delivered INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0);
CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
PRAGMA user_version=1;
COMMIT;`
