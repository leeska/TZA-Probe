#!/usr/bin/env bash
# TZA-Probe 的检查与构建流水线（不依赖 GitHub Actions，任意 Linux 机器可跑）：
#   check  gofmt / go vet / go test（有 gcc 时加 -race）/ web/*.js 语法 /
#          presentation-test.mjs / 一次性 Server 上的 ui-smoke.mjs
#   build  构建 linux amd64 / arm64 的 Server（内含两个架构的 Agent）+ 两个发行包与 sha256
#   all    check 后 build（默认）
#
#   bash scripts/pipeline.sh [check|build|all]
#
# 环境变量：
#   SHA=<短 sha>        写进版本号（默认 git rev-parse --short HEAD）
#   VERSION=<字符串>    覆盖版本号（默认：HEAD 正好在 tag 上就用 tag，否则 1.0.0-dev+<sha>）
#   OUT=<目录>          产物目录（默认 <仓库>/dist）
#   UI_SMOKE=0          跳过 ui-smoke（只跑纯 Go 与纯前端部分）
#   TZA_NODE / TZA_NPM  指定 Node / npm（默认取 PATH 里的）
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

STAGE=${1:-all}
case "$STAGE" in check|build|all) ;; *) echo "用法: bash scripts/pipeline.sh [check|build|all]" >&2; exit 2; esac

export PATH=$PATH:/usr/local/go/bin
GO=${TZA_GO:-go}
command -v "$GO" >/dev/null || { echo "!! 找不到 go（试 TZA_GO=/path/to/go）" >&2; exit 1; }
NODE=${TZA_NODE:-$(command -v node || true)}
NPM=${TZA_NPM:-$(command -v npm || true)}
SHA=${SHA:-$(git rev-parse --short HEAD)}
OUT=${OUT:-$ROOT/dist}
export GOFLAGS=${GOFLAGS:--mod=mod}

TMP=$(mktemp -d /tmp/tza-pipeline.XXXXXX)
SRV_PID=""
BUILT_AGENTS=0
cleanup() {
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null
  # 编进 agentdist 的 Agent 二进制不能留在这个检出里：它们是被忽略的未跟踪文件，
  # git checkout --force 不会清掉，之后在同一检出里直接 `go build ./cmd/server`
  # 就会把这份旧 Agent 内嵌进去。构建时用过就删掉，恢复干净检出。
  if [ "$BUILT_AGENTS" = 1 ]; then rm -f internal/agentdist/bin/tza-agent-linux-*; fi
  rm -rf "$TMP"
  return 0
}
trap cleanup EXIT

FAILED=0
say()  { printf '\n== %s\n' "$*"; }
note() { printf '   %s\n' "$*"; }
now()  { date +%s; }
# 没有 sha256sum 的 macOS 用 shasum -a 256，输出格式相同。
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
dur()  { echo "$(( $(now) - $1 ))s"; }

# ---------------------------------------------------------------- check

check_go() {
  local t0 out rc
  say "gofmt（期望无输出）"
  out=$(gofmt -l . 2>&1)
  [ -z "$out" ] && note "clean" || { printf '%s\n' "$out" | sed 's/^/   !! 未格式化: /'; FAILED=1; }

  say "go vet ./..."
  t0=$(now); "$GO" vet ./... 2>&1 | sed 's/^/   /'; rc=${PIPESTATUS[0]}; note "vet $(dur $t0) rc=$rc"; [ "$rc" = 0 ] || FAILED=1

  say "go test ./..."
  t0=$(now)
  "$GO" test ./... > "$TMP/test.log" 2>&1; rc=$?
  note "ok 包数: $(grep -c '^ok' "$TMP/test.log")"
  grep -E '^(FAIL|---)' "$TMP/test.log" | head -20 | sed 's/^/   /'
  note "go test $(dur $t0) rc=$rc"; [ "$rc" = 0 ] || FAILED=1

  if command -v gcc >/dev/null; then
    say "go test -race ./...（有 gcc，按 CI 口径跑）"
    t0=$(now)
    "$GO" test -race ./... > "$TMP/race.log" 2>&1; rc=$?
    grep -E '^(FAIL|WARNING: DATA RACE)' "$TMP/race.log" | head -10 | sed 's/^/   /'
    note "go test -race $(dur $t0) rc=$rc"; [ "$rc" = 0 ] || FAILED=1
  else
    note "本机没有 gcc，跳过 go test -race（-race 要 cgo）"
  fi
}

check_web() {
  local t0 rc f
  say "web/*.js 语法（node --input-type=module --check）"
  if [ -z "$NODE" ]; then note "!! 没有 node，跳过"; FAILED=1; return; fi
  rc=0
  for f in web/*.js; do
    "$NODE" --input-type=module --check < "$f" || { note "!! 语法错误: $f"; rc=1; }
  done
  note "node $("$NODE" -v)  文件数 $(ls web/*.js | wc -l)  rc=$rc"; [ "$rc" = 0 ] || FAILED=1

  say "scripts/presentation-test.mjs"
  t0=$(now); "$NODE" scripts/presentation-test.mjs; rc=$?
  note "presentation-test $(dur $t0) rc=$rc"; [ "$rc" = 0 ] || FAILED=1
}

check_ui_smoke() {
  [ "${UI_SMOKE:-1}" = 1 ] || { note "UI_SMOKE=0，跳过 ui-smoke"; return; }
  local t0 rc port=${TZA_UI_PORT:-25874} deps=${TZA_UI_DEPS:-/tmp/tza-ui-deps} i code
  say "ui-smoke.mjs（一次性 Server + jsdom）"
  "$GO" build -o "$TMP/tza-server" ./cmd/server || { note "!! 构建一次性 Server 失败"; FAILED=1; return; }
  mkdir -p "$TMP/data"
  "$TMP/tza-server" --listen "127.0.0.1:$port" --data "$TMP/data" > "$TMP/server.log" 2>&1 &
  SRV_PID=$!
  code=000
  for i in $(seq 1 30); do
    code=$(curl -s -o /dev/null -m 2 -w '%{http_code}' "http://127.0.0.1:$port/healthz" || true)
    [ "$code" = 200 ] && break
    sleep 1
  done
  note "healthz=$code pid=$SRV_PID data=$TMP/data"
  if [ "$code" != 200 ]; then tail -5 "$TMP/server.log" | sed 's/^/   /'; FAILED=1; return; fi
  if [ ! -d "$deps/node_modules/jsdom" ]; then
    note "装 jsdom@26.1.0 + esbuild@0.25.10 到 $deps"
    "$NPM" install --prefix "$deps" --no-audit --no-fund jsdom@26.1.0 esbuild@0.25.10 > "$TMP/npm.log" 2>&1 \
      || { tail -5 "$TMP/npm.log" | sed 's/^/   /'; FAILED=1; return; }
  fi
  t0=$(now)
  TZA_UI_DEPS="$deps" TZA_TEST_URL="http://127.0.0.1:$port" TZA_TEST_ADMIN_FILE="$TMP/data/admin-token" \
    "$NODE" scripts/ui-smoke.mjs 2>&1 | tail -20 | sed 's/^/   /'
  rc=${PIPESTATUS[0]}
  note "ui-smoke $(dur $t0) rc=$rc"; [ "$rc" = 0 ] || FAILED=1
  kill "$SRV_PID" 2>/dev/null; SRV_PID=""
}

run_check() {
  [ -n "$NODE" ] || { echo "!! 找不到 node（试 TZA_NODE=/path/to/node）" >&2; exit 1; }
  check_go
  check_web
  check_ui_smoke
}

# ---------------------------------------------------------------- build

version() {
  if [ -n "${VERSION:-}" ]; then echo "$VERSION"; return; fi
  local tag
  tag=$(git describe --exact-match --tags HEAD 2>/dev/null || true)
  [ -n "$tag" ] && { echo "$tag"; return; }
  echo "1.0.0-dev+$SHA"
}

run_build() {
  local V LDFLAGS arch t0 t1 stage
  V=$(version)
  LDFLAGS="-s -w -X github.com/leeska/TZA-Probe/internal/model.Version=$V"
  say "构建（版本 $V，短 sha $SHA）"
  rm -rf "$OUT" "$TMP/stage"; mkdir -p "$OUT" "$TMP/stage"

  # Server 内嵌两个架构的 Agent（面板「接入」的一行命令要用），所以先编 Agent 再编 Server。
  mkdir -p internal/agentdist/bin
  BUILT_AGENTS=1
  t0=$(now)
  for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch "$GO" build -trimpath -ldflags "$LDFLAGS" \
      -o "internal/agentdist/bin/tza-agent-linux-$arch" ./cmd/agent || { FAILED=1; return; }
  done
  t1=$(now)
  note "agent amd64+arm64: $(dur $t0)  $(ls -l internal/agentdist/bin | awk 'NR>1{printf "%s %s  ", $9, $5}')"

  for arch in amd64 arm64; do
    stage="$TMP/stage/$arch"; mkdir -p "$stage"
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch "$GO" build -trimpath -ldflags "$LDFLAGS" \
      -o "$stage/tza-server" ./cmd/server || { FAILED=1; return; }
    cp "internal/agentdist/bin/tza-agent-linux-$arch" "$stage/tza-agent"
    cp README.md LICENSE NOTICE "$stage/"
    cp -r deploy docs "$stage/"
    ( cd "$stage" && tar -czf "$OUT/tza-probe-linux-$arch.tar.gz" . ) || { FAILED=1; return; }
    ( cd "$OUT" && sha256 "tza-probe-linux-$arch.tar.gz" > "tza-probe-linux-$arch.tar.gz.sha256" ) || { FAILED=1; return; }
    # 只有 Linux 主机能直接运行这个二进制（macOS 上交叉编出的 Linux 程序跑不起来）。
    if [ "$arch" = "$("$GO" env GOARCH)" ] && [ "$(uname -s)" = Linux ]; then
      note "server --version: $("$stage/tza-server" --version 2>&1 | tail -1)"
    fi
  done
  note "server amd64+arm64 与打包: $(dur $t1)"

  ( cd "$OUT" && sha256 tza-probe-linux-*.tar.gz > SHA256SUMS )
  echo "$V" > "$OUT/VERSION"
  say "产物 $OUT"
  ( cd "$OUT" && ls -l | awk 'NR>1{printf "   %-38s %8.1f MB\n", $9, $5/1048576}' )
  sed 's/^/   /' "$OUT/SHA256SUMS"
}

# ---------------------------------------------------------------- main

if [ "$STAGE" = check ] || [ "$STAGE" = all ]; then run_check; fi
if [ "$STAGE" = build ] || [ "$STAGE" = all ]; then run_build; fi

if [ "$FAILED" = 0 ]; then
  printf '\n== 流水线通过（stage=%s sha=%s）\n' "$STAGE" "$SHA"
else
  printf '\n== 流水线失败（stage=%s sha=%s）\n' "$STAGE" "$SHA"
  exit 1
fi
