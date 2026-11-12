CREATE TABLE nodes(id TEXT PRIMARY KEY, config TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, last_seen INTEGER NOT NULL DEFAULT 0, metrics TEXT, created_at INTEGER NOT NULL DEFAULT 0, traffic_adjustment INTEGER NOT NULL DEFAULT 0, adjustment_cycle INTEGER NOT NULL DEFAULT 0);
CREATE TABLE samples(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, at INTEGER NOT NULL, payload TEXT NOT NULL, PRIMARY KEY(node_id,at));
CREATE INDEX samples_age ON samples(at);
CREATE TABLE traffic(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, day INTEGER NOT NULL, incoming INTEGER NOT NULL, outgoing INTEGER NOT NULL, PRIMARY KEY(node_id,day));
CREATE TABLE tasks(id TEXT PRIMARY KEY, config TEXT NOT NULL);
CREATE TABLE bindings(task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, due INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(task_id,node_id));
CREATE TABLE jobs(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, task TEXT NOT NULL, issued INTEGER NOT NULL, finished INTEGER, result TEXT, probe_key TEXT NOT NULL DEFAULT '');
CREATE INDEX jobs_node ON jobs(node_id,issued);
CREATE INDEX jobs_age ON jobs(issued);
CREATE INDEX jobs_task ON jobs(task_id);
CREATE TABLE alert_state(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, kind TEXT NOT NULL, since INTEGER NOT NULL, active INTEGER NOT NULL, PRIMARY KEY(node_id,kind));
CREATE TABLE events(id INTEGER PRIMARY KEY AUTOINCREMENT, node_id TEXT NOT NULL, node_name TEXT NOT NULL, kind TEXT NOT NULL, message TEXT NOT NULL, active INTEGER NOT NULL, at INTEGER NOT NULL, delivered INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '');
CREATE INDEX events_rule_time ON events(node_id,kind,at);
CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE latest(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, probe_key TEXT NOT NULL, job_id TEXT NOT NULL, task TEXT NOT NULL, issued INTEGER NOT NULL, finished INTEGER, result TEXT, PRIMARY KEY(node_id,probe_key));
CREATE INDEX latest_job ON latest(job_id);
CREATE TRIGGER jobs_latest_insert AFTER INSERT ON jobs WHEN json_valid(NEW.task) BEGIN
  INSERT INTO latest(node_id,probe_key,job_id,task,issued,finished,result)
  VALUES(NEW.node_id,CASE WHEN NEW.probe_key<>'' THEN NEW.probe_key ELSE COALESCE(NEW.task_id,'')||'|'||COALESCE(json_extract(NEW.task,'$.kind'),'')||'|'||COALESCE(json_extract(NEW.task,'$.family'),'')||'|'||COALESCE(json_extract(NEW.task,'$.host'),'')||'|'||COALESCE(json_extract(NEW.task,'$.port'),'')||'|'||COALESCE(json_extract(NEW.task,'$.region'),'')||'|'||COALESCE(json_extract(NEW.task,'$.carrier'),'') END,NEW.id,NEW.task,NEW.issued,NEW.finished,NEW.result)
  ON CONFLICT(node_id,probe_key) DO UPDATE SET job_id=excluded.job_id,task=excluded.task,issued=excluded.issued,finished=excluded.finished,result=excluded.result
  WHERE excluded.issued>latest.issued OR (excluded.issued=latest.issued AND excluded.job_id>latest.job_id);
END;
CREATE TRIGGER jobs_latest_update AFTER UPDATE OF task,issued,finished,result ON jobs WHEN json_valid(NEW.task) BEGIN
  UPDATE latest SET task=NEW.task,issued=NEW.issued,finished=NEW.finished,result=NEW.result
  WHERE job_id=NEW.id AND node_id=NEW.node_id;
END;
CREATE TRIGGER jobs_latest_delete AFTER DELETE ON jobs BEGIN
  DELETE FROM latest WHERE job_id=OLD.id;
END;
CREATE TABLE traffic_hour(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, hour INTEGER NOT NULL, incoming INTEGER NOT NULL, outgoing INTEGER NOT NULL, PRIMARY KEY(node_id,hour));
CREATE INDEX traffic_hour_age ON traffic_hour(hour);
CREATE TABLE unlocks(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, service TEXT NOT NULL, status TEXT NOT NULL, region TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', tested_at INTEGER NOT NULL, PRIMARY KEY(node_id,service));
CREATE INDEX unlocks_age ON unlocks(tested_at);
CREATE TABLE retries(node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, probe_key TEXT NOT NULL, at INTEGER NOT NULL, PRIMARY KEY(node_id,probe_key));
CREATE INDEX retries_at ON retries(at);
CREATE TABLE purges(id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, target TEXT NOT NULL, cutoff INTEGER NOT NULL);
CREATE TABLE alert_rules(id TEXT PRIMARY KEY, config TEXT NOT NULL);
CREATE TABLE tcp_buckets(node_id TEXT NOT NULL, bucket INTEGER NOT NULL, probe_key TEXT NOT NULL, task_id TEXT NOT NULL, job_min INTEGER NOT NULL, finished INTEGER NOT NULL, lat_sum REAL NOT NULL, lat_n INTEGER NOT NULL, sent INTEGER NOT NULL, received INTEGER NOT NULL, task TEXT NOT NULL, PRIMARY KEY(node_id,bucket,probe_key));
CREATE INDEX tcp_buckets_age ON tcp_buckets(bucket);
CREATE INDEX tcp_buckets_task ON tcp_buckets(task_id);
CREATE TRIGGER jobs_tcp_bucket_insert AFTER INSERT ON jobs WHEN NEW.finished IS NOT NULL BEGIN
  INSERT INTO tcp_buckets(node_id,bucket,probe_key,task_id,job_min,finished,lat_sum,lat_n,sent,received,task)
  SELECT NEW.node_id,NEW.finished-NEW.finished%360,k,NEW.task_id,NEW.rowid,NEW.finished,
         COALESCE(lat,0),lat IS NOT NULL,COALESCE(json_extract(r,'$.sent'),0),COALESCE(json_extract(r,'$.received'),0),NEW.task
  FROM (SELECT k,r,CASE WHEN json_extract(r,'$.status')='ok' THEN json_extract(r,'$.latency') END AS lat
        FROM (SELECT CASE WHEN NEW.probe_key<>'' THEN NEW.probe_key WHEN json_valid(NEW.task) THEN COALESCE(NEW.task_id,'')||'|'||COALESCE(json_extract(NEW.task,'$.kind'),'')||'|'||COALESCE(json_extract(NEW.task,'$.family'),'')||'|'||COALESCE(json_extract(NEW.task,'$.host'),'')||'|'||COALESCE(json_extract(NEW.task,'$.port'),'')||'|'||COALESCE(json_extract(NEW.task,'$.region'),'')||'|'||COALESCE(json_extract(NEW.task,'$.carrier'),'') END AS k,CASE WHEN json_valid(NEW.result) THEN NEW.result END AS r))
  WHERE substr(k,length(NEW.task_id)+2,4)='tcp|' ON CONFLICT(node_id,bucket,probe_key) DO UPDATE SET
  task=CASE WHEN excluded.finished>=tcp_buckets.finished THEN excluded.task ELSE tcp_buckets.task END,
  finished=max(tcp_buckets.finished,excluded.finished),
  job_min=min(tcp_buckets.job_min,excluded.job_min),
  lat_sum=tcp_buckets.lat_sum+excluded.lat_sum,
  lat_n=tcp_buckets.lat_n+excluded.lat_n,
  sent=tcp_buckets.sent+excluded.sent,
  received=tcp_buckets.received+excluded.received;
END;
CREATE TRIGGER jobs_tcp_bucket_finish AFTER UPDATE OF finished ON jobs WHEN OLD.finished IS NULL AND NEW.finished IS NOT NULL BEGIN
  INSERT INTO tcp_buckets(node_id,bucket,probe_key,task_id,job_min,finished,lat_sum,lat_n,sent,received,task)
  SELECT NEW.node_id,NEW.finished-NEW.finished%360,k,NEW.task_id,NEW.rowid,NEW.finished,
         COALESCE(lat,0),lat IS NOT NULL,COALESCE(json_extract(r,'$.sent'),0),COALESCE(json_extract(r,'$.received'),0),NEW.task
  FROM (SELECT k,r,CASE WHEN json_extract(r,'$.status')='ok' THEN json_extract(r,'$.latency') END AS lat
        FROM (SELECT CASE WHEN NEW.probe_key<>'' THEN NEW.probe_key WHEN json_valid(NEW.task) THEN COALESCE(NEW.task_id,'')||'|'||COALESCE(json_extract(NEW.task,'$.kind'),'')||'|'||COALESCE(json_extract(NEW.task,'$.family'),'')||'|'||COALESCE(json_extract(NEW.task,'$.host'),'')||'|'||COALESCE(json_extract(NEW.task,'$.port'),'')||'|'||COALESCE(json_extract(NEW.task,'$.region'),'')||'|'||COALESCE(json_extract(NEW.task,'$.carrier'),'') END AS k,CASE WHEN json_valid(NEW.result) THEN NEW.result END AS r))
  WHERE substr(k,length(NEW.task_id)+2,4)='tcp|' ON CONFLICT(node_id,bucket,probe_key) DO UPDATE SET
  task=CASE WHEN excluded.finished>=tcp_buckets.finished THEN excluded.task ELSE tcp_buckets.task END,
  finished=max(tcp_buckets.finished,excluded.finished),
  job_min=min(tcp_buckets.job_min,excluded.job_min),
  lat_sum=tcp_buckets.lat_sum+excluded.lat_sum,
  lat_n=tcp_buckets.lat_n+excluded.lat_n,
  sent=tcp_buckets.sent+excluded.sent,
  received=tcp_buckets.received+excluded.received;
END;
CREATE TABLE node_health(node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE, seen_at INTEGER NOT NULL, status TEXT NOT NULL);
CREATE TABLE node_agent_sessions(
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 session TEXT NOT NULL, sequence INTEGER NOT NULL,
 active INTEGER NOT NULL, seen_at INTEGER NOT NULL,
 PRIMARY KEY(node_id,session));
CREATE UNIQUE INDEX node_agent_session_active ON node_agent_sessions(node_id) WHERE active=1;
CREATE TABLE server_settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
