#!/usr/bin/env bash
# 发布 GitHub Release。
# 只写 tag 与 release，不碰 main；发行包由 scripts/pipeline.sh build 产出。
#
#   GITHUB_TOKEN=... bash scripts/release.sh <tag> <dist 目录> [目标 sha]
#   例：GITHUB_TOKEN=ghp_... bash scripts/release.sh v1.0.0 dist
#   没设 GITHUB_TOKEN 时用 gh 的登录凭据（gh auth token）。
#
# Linux 与 macOS 都能跑：只用 bash 3.2 语法（macOS 自带），sha256 校验在没有
# sha256sum 时改用 shasum -a 256，计时不用 GNU date 的 %N。
#
# 做四件事：校验产物 sha256 → tag 指到目标提交（已存在则不动）→ 建 release（GitHub 自动生成说明）
# → 上传 tarball 与 .sha256 → 读回核对。
#
# 替换已有附件时不先删后传：先把全部新附件以临时名（<名字>.uploading）传完并核对大小，
# 再逐个「删旧 + 改名」。上传阶段断网、失败，已发布的附件一个都不动；重跑会清掉残留的
# 临时附件再传。切换阶段每个附件只有删与改名两次调用之间的一瞬没有文件。
set -euo pipefail

TAG=${1:?用法: bash scripts/release.sh <tag> <dist 目录> [目标 sha]}
DIST=${2:?缺少 dist 目录}
REPO=${REPO:-leeska/TZA-Probe}
TOKEN=${GITHUB_TOKEN:-}
if [ -z "$TOKEN" ] && command -v gh >/dev/null; then TOKEN=$(gh auth token 2>/dev/null || true); fi
[ -n "$TOKEN" ] || { echo "!! 需要 GITHUB_TOKEN（或先 gh auth login）" >&2; exit 1; }
SHA=${3:-$(git rev-parse HEAD)}
# 两个地址可覆盖，只为了对着本地假服务测试这个脚本。
API=${GITHUB_API:-https://api.github.com}/repos/$REPO
UPLOADS=${GITHUB_UPLOADS:-https://uploads.github.com}/repos/$REPO

gh_json() { curl -sS -H "Accept: application/vnd.github+json" -H "Authorization: Bearer $TOKEN" "$@"; }
# 幂等请求（GET / DELETE / 改名）遇到连不上、超时等网络错误自动重试。
gh_idem() { gh_json --retry 4 --retry-delay 3 --retry-all-errors "$@"; }
# 按名字找附件 id，没有就输出空。
asset_id() {
  gh_idem "$API/releases/$rid/assets?per_page=100" | python3 -c "import json,sys
n=sys.argv[1]
print(next((a['id'] for a in json.load(sys.stdin) if a['name']==n), ''))" "$1"
}
# 毫秒时间戳：macOS 的 date 不支持 %N。
now_ms() { python3 -c 'import time; print(int(time.time() * 1000))'; }
sha256_check() {
  if command -v sha256sum >/dev/null; then sha256sum -c "$@"; else shasum -a 256 -c "$@"; fi
}

case "$TAG" in v*) ;; *) echo "!! tag 要以 v 开头（install.sh --version 按 vX.Y.Z 下载）：$TAG" >&2; exit 1;; esac
[ -f "$DIST/SHA256SUMS" ] || { echo "!! $DIST 里没有 SHA256SUMS，先跑 scripts/pipeline.sh build" >&2; exit 1; }

echo "== 校验产物"
( cd "$DIST" && sha256_check SHA256SUMS )
ASSETS=()
# 每个安装包后面紧跟它的 .sha256：切换时成对换，不让新包配旧校验和（install.sh 会校验失败）。
for f in "$DIST"/tza-probe-linux-*.tar.gz; do ASSETS+=("$f" "$f.sha256"); done
printf '   %s\n' "${ASSETS[@]##*/}"

echo "== tag $TAG -> ${SHA:0:7}"
code=$(gh_json -o /dev/null -w '%{http_code}' "$API/git/ref/tags/$TAG")
if [ "$code" = 200 ]; then
  echo "   tag 已存在，不动"
else
  gh_json -X POST "$API/git/refs" -d "{\"ref\":\"refs/tags/$TAG\",\"sha\":\"$SHA\"}" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print("   建 tag:", d.get("ref") or d)' 
fi

echo "== 建 release"
rid=$(gh_json "$API/releases/tags/$TAG" | python3 -c 'import json,sys
try:
    d=json.load(sys.stdin); print(d.get("id") or "")
except Exception: print("")')
if [ -n "$rid" ]; then
  echo "   release 已存在（id $rid），复用"
else
  rid=$(gh_json -X POST "$API/releases" \
    -d "{\"tag_name\":\"$TAG\",\"name\":\"$TAG\",\"target_commitish\":\"$SHA\",\"generate_release_notes\":true,\"draft\":false,\"prerelease\":false}" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("id") or "")')
  [ -n "$rid" ] || { echo "!! 建 release 失败" >&2; exit 1; }
  echo "   新 release id=$rid"
fi

echo "== 上传资产（临时名）"
for f in "${ASSETS[@]}"; do
  n=${f##*/}
  tmp="$n.uploading"
  size=$(( $(wc -c < "$f") ))
  ok=""
  for attempt in 1 2 3 4; do
    left=$(asset_id "$tmp")
    [ -z "$left" ] || gh_idem -X DELETE "$API/releases/assets/$left" >/dev/null
    t0=$(now_ms)
    resp=$(gh_json -X POST "$UPLOADS/releases/$rid/assets?name=$tmp" \
      -H "Content-Type: application/octet-stream" --data-binary "@$f" || true)
    if printf '%s' "$resp" | python3 -c "import json,sys
d=json.load(sys.stdin)
sys.exit(0 if d.get('name')==sys.argv[1] and d.get('state')=='uploaded' and d.get('size')==int(sys.argv[2]) else 1)" "$tmp" "$size" 2>/dev/null; then
      printf '   %-40s %8.2f MB  %sms\n' "$n" "$(python3 -c "print($size/1048576)")" "$(( $(now_ms) - t0 ))"
      ok=1; break
    fi
    echo "   $n 第 $attempt 次上传失败，重试" >&2
    sleep $(( attempt * 5 ))
  done
  [ -n "$ok" ] || { echo "!! $n 上传失败；已发布的附件没有改动，重跑本脚本即可" >&2; exit 1; }
done

echo "== 切换为正式名"
for f in "${ASSETS[@]}"; do
  n=${f##*/}
  tmp_id=$(asset_id "$n.uploading")
  [ -n "$tmp_id" ] || { echo "!! 找不到刚传的 $n.uploading" >&2; exit 1; }
  old=$(asset_id "$n")
  [ -z "$old" ] || gh_idem -X DELETE "$API/releases/assets/$old" >/dev/null
  gh_idem -X PATCH "$API/releases/assets/$tmp_id" -d "{\"name\":\"$n\"}" | python3 -c "import json,sys
d=json.load(sys.stdin); assert d.get('name')==sys.argv[1], d" "$n" \
    || { echo "!! $n 改名失败：新文件仍在 $n.uploading，重跑本脚本即可" >&2; exit 1; }
  echo "   $n${old:+（替换 id $old）}"
done

echo "== 读回核对"
gh_json "$API/releases/tags/$TAG" | python3 -c 'import json,sys
d=json.load(sys.stdin)
print("   ", d["html_url"], "tag=", d["tag_name"], "assets=", len(d["assets"]))
for a in d["assets"]: print("      %-40s %8.2f MB" % (a["name"], a["size"]/1048576))'
