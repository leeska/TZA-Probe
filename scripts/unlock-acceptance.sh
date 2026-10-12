#!/usr/bin/env bash
# 解锁检测的真数据验收：起一份一次性 Server，完全按 Agent 的路径走一遍
#   建公开节点 → 上报（带 unlock 能力）→ 建 unlock 任务 → 租约 → 上报 24 项结论
# 然后读公开接口确认结论真落库、分数真按 24 项分母算；再读 /api/v1/nodes 确认 unlock 字段，
# 并确认一台没跑过检测的机器**不带** unlock 字段（前端据此不渲染占位）。
# 用法：bash scripts/unlock-acceptance.sh   （需要 /usr/local/go/bin 的 go）
set -euo pipefail
WORK=/tmp/tza-unlock-accept
PORT=25911
BASE=http://127.0.0.1:$PORT
export PATH=$PATH:/usr/local/go/bin

pid=$(ss -ltnp 2>/dev/null | grep ":$PORT " | grep -o 'pid=[0-9]*' | cut -d= -f2 | head -1 || true)
[ -n "${pid:-}" ] && kill "$pid" && sleep 1
rm -rf "$WORK"; mkdir -p "$WORK/data"
cd "$(dirname "$0")/.."
go build -o "$WORK/tza-server" ./cmd/server
TZA_ADMIN_TOKEN=$(python3 -c 'import secrets;print(secrets.token_hex(24))')
export TZA_ADMIN_TOKEN
"$WORK/tza-server" --listen 127.0.0.1:$PORT --data "$WORK/data" > "$WORK/server.log" 2>&1 &
SERVER=$!
trap 'kill $SERVER 2>/dev/null || true' EXIT
for _ in $(seq 1 60); do curl -sf "$BASE/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/healthz")" = 200 ] || { echo "server 未就绪"; cat "$WORK/server.log"; exit 1; }

BASE=$BASE python3 - <<'PY'
import json, os, time, urllib.error, urllib.request

BASE = os.environ["BASE"]
TOKEN = os.environ["TZA_ADMIN_TOKEN"]

def call(method, path, body=None, token=TOKEN):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if data:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()

catalog = call("GET", "/api/v1/unlock-catalog", token="")[1]
assert len(catalog["services"]) == 24, catalog["services"]
print("unlock-catalog: %d 项 · 分类 %s" % (len(catalog["services"]), " / ".join(catalog["categories"])))

# 一台跑过检测的机器，和一台没跑过的（用来验证「没有数据就不带字段」）。
status, node = call("POST", "/api/v1/admin/nodes", {
    "name": "unlock-accept", "region": "日本", "group": "JP", "public": True,
    "billing_at": "2026-09-01T00:00:00Z", "traffic_mode": "sum"})
assert status == 200, node
node_id, node_key = node["id"], node["token"]
plain_id = call("POST", "/api/v1/admin/nodes", {
    "name": "unlock-accept-plain", "region": "日本", "public": True,
    "billing_at": "2026-09-01T00:00:00Z", "traffic_mode": "sum"})[1]["id"]

# 指标必须带上 capabilities=["unlock"]，否则服务端不会把解锁任务派给这台机器。
status, err = call("POST", "/api/v1/agent/report", {"protocol": 1, "metrics": {
    "counter_id": "unlock-accept", "cpu": 3, "memory_used": 1, "memory_total": 2,
    "disk_used": 1, "disk_total": 2, "net_in": 10, "net_out": 5, "rate_in": 0, "rate_out": 0,
    "uptime": 3600, "os": "linux", "distro": "Debian 13", "arch": "amd64", "cpu_cores": 1,
    "version": "accept", "ip_families": ["ipv4"], "capabilities": ["unlock"]}}, token=node_key)
assert status == 204, err

status, err = call("POST", "/api/v1/admin/tasks", {
    "name": "解锁检测验收", "type": "unlock", "unlock": {"interval": 3600, "family": ""},
    "enabled": True, "node_ids": [node_id]})
assert status == 200, err

jobs = call("GET", "/api/v1/agent/jobs", token=node_key)[1]
job = next((j for j in jobs if j["task"]["kind"] == "unlock"), None)
assert job, "没有领到解锁作业: %s" % [j["task"]["kind"] for j in jobs]

# 24 项结论：流媒体 7/9 · AI 3/3 · 交易所 3/3 · 其他 6/9 = 19/24。
# region_only 的 5 项（bing/apple/google_play/steam/onetrust）在面板上只显示地区 / 币种文字。
verdicts = [
    ("netflix", "originals_only", "JP"), ("disneyplus", "yes", "JP"), ("youtube_premium", "yes", ""),
    ("prime_video", "no", ""), ("tvb_anywhere", "no", ""), ("iqiyi", "yes", ""),
    ("dazn", "yes", ""), ("netflix_cdn", "yes", ""), ("youtube_cdn", "yes", ""),
    ("openai", "yes", ""), ("gemini", "yes", ""), ("claude", "yes", ""),
    ("bybit", "yes", ""), ("binance", "yes", ""), ("okx", "yes", ""),
    ("bing", "yes", "Japan"), ("apple", "yes", "JP"), ("google_play", "yes", "Japan"),
    ("google_search", "yes", ""), ("wikipedia", "yes", ""), ("reddit", "banned", ""),
    ("steam", "yes", "JPY"), ("onetrust", "banned", "US"), ("sdggge", "failed", ""),
]
status, err = call("POST", "/api/v1/agent/results", {
    "job_id": job["id"], "status": "ok", "sent": 0, "received": 0, "route": "未知", "hops": [],
    "unlocks": [{"service": s, "status": st, "region": r} for s, st, r in verdicts]}, token=node_key)
assert status == 204, err

# 解锁结论只随 /nodes 的 unlock 字段下发；匿名列表是 1 秒快照，等它过期再读。
time.sleep(1.1)
nodes = call("GET", "/api/v1/nodes", token="")[1]
unlocks = next(n for n in nodes if n["id"] == node_id)["unlock"]
assert unlocks["total"] == 24 and len(unlocks["services"]) == 24, unlocks
assert unlocks["score"] == 19, "分数应为 19，实际 %s" % unlocks["score"]
cats = {c: [0, 0] for c in catalog["categories"]}
names = {s["id"]: s for s in catalog["services"]}
for u in unlocks["services"]:
    svc = names[u["service"]]
    cats[svc["category"]][1] += 1
    if u["status"] in ("yes", "originals_only"):
        cats[svc["category"]][0] += 1
print("unlocks: %d/%d · %s · tested_at=%s" % (
    unlocks["score"], unlocks["total"],
    " · ".join("%s %d/%d" % (c, cats[c][0], cats[c][1]) for c in catalog["categories"]),
    unlocks["tested_at"]))

view = next(n for n in nodes if n["id"] == node_id)
assert view["unlock"]["score"] == 19 and view["unlock"]["total"] == 24, view.get("unlock")
plain = next(n for n in nodes if n["id"] == plain_id)
assert "unlock" not in plain, "没跑过检测的机器不该带 unlock 字段：%r" % plain.get("unlock")
assert any(t.get("kind") == "unlock" for t in view.get("probe_targets", [])), view.get("probe_targets")
print("/api/v1/nodes: 有数据的节点带 unlock=%d/%d，没跑过的节点不带该字段" % (
    view["unlock"]["score"], view["unlock"]["total"]))
print("OK: 真数据验收通过")
PY