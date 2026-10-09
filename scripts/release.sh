#!/usr/bin/env bash
# 发布 GitHub Release（替代原 .github/workflows/check.yml 里的 release job，2026-10-09 起自建）。
# 只写 tag 与 release，不碰 main；发行包由 scripts/pipeline.sh build 产出。
#
#   GITHUB_TOKEN=... bash scripts/release.sh <tag> <dist 目录> [目标 sha]
#   例：GITHUB_TOKEN=$(grep ^GITHUB_TOKEN /root/.hermes/.env | cut -d= -f2) bash scripts/release.sh v2.0.0 dist
#
# 做四件事：校验产物 sha256 → tag 指到目标提交（已存在则不动）→ 建 release（GitHub 自动生成说明）
# → 上传 tarball 与 .sha256（同名资产先删后传）→ 读回核对。
set -euo pipefail

TAG=${1:?用法: bash scripts/release.sh <tag> <dist 目录> [目标 sha]}
DIST=${2:?缺少 dist 目录}
REPO=${REPO:-leeska/TZA-Probe}
TOKEN=${GITHUB_TOKEN:?需要 GITHUB_TOKEN}
SHA=${3:-$(git rev-parse HEAD)}
API=https://api.github.com/repos/$REPO
UPLOADS=https://uploads.github.com/repos/$REPO

gh_json() { curl -sS -H "Accept: application/vnd.github+json" -H "Authorization: Bearer $TOKEN" "$@"; }

case "$TAG" in v*) ;; *) echo "!! tag 要以 v 开头（install.sh --version 按 vX.Y.Z 下载）：$TAG" >&2; exit 1;; esac
[ -f "$DIST/SHA256SUMS" ] || { echo "!! $DIST 里没有 SHA256SUMS，先跑 scripts/pipeline.sh build" >&2; exit 1; }

echo "== 校验产物"
( cd "$DIST" && sha256sum -c SHA256SUMS )
ASSETS=()
for f in "$DIST"/tza-probe-linux-*.tar.gz "$DIST"/tza-probe-linux-*.tar.gz.sha256; do ASSETS+=("$f"); done
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

echo "== 上传资产"
for f in "${ASSETS[@]}"; do
  n=${f##*/}
  old=$(gh_json "$API/releases/$rid/assets" | python3 -c "import json,sys
n=sys.argv[1]
print(next((a['id'] for a in json.load(sys.stdin) if a['name']==n), ''))" "$n")
  [ -n "$old" ] && { gh_json -X DELETE "$API/releases/assets/$old" >/dev/null; echo "   删掉同名旧资产 $n (id $old)"; }
  t0=$(date +%s%3N)
  gh_json -X POST "$UPLOADS/releases/$rid/assets?name=$n" \
    -H "Content-Type: application/octet-stream" --data-binary "@$f" \
    | python3 -c "import json,sys; d=json.load(sys.stdin); print('   %-40s %8.2f MB  state=%s' % (d['name'], d['size']/1048576, d['state']))"
  echo "      上传耗时 $(( $(date +%s%3N) - t0 ))ms"
done

echo "== 读回核对"
gh_json "$API/releases/tags/$TAG" | python3 -c 'import json,sys
d=json.load(sys.stdin)
print("   ", d["html_url"], "tag=", d["tag_name"], "assets=", len(d["assets"]))
for a in d["assets"]: print("      %-40s %8.2f MB" % (a["name"], a["size"]/1048576))'
