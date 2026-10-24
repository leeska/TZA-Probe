#!/bin/sh
# TZA Probe Server 一键安装（Linux amd64 / arm64）。
#
# 两种安装方式，二选一：
#
# 从 GitHub Release 安装（默认，推荐）：下载预编译发布包，免 Go 与 git，发布包内嵌两种架构的
# Agent（签过名的发布可以在后台一键升级 Agent）。
# 安装最新发布：
#   curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh
# 安装指定版本：
#   curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh -s -- --release v1.0.0
#
# 从源码构建（需要 git 与 Go）：拉取 main（或 --ref 指定的分支 / tag）在本机编译 Server 与两种
# 架构的 Agent。本机编出的 Agent 没有签名，节点只能重跑接入命令升级。
# 构建 main（指定分支或 tag 把 --source 换成 --ref REF）：
#   curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh -s -- --source
#
# 脚本只做五件事：取到 tza-server、装到 /usr/local/bin、建好数据目录、生成管理密钥与
# 后台账号、装 systemd 服务。
# 重复执行即升级：旧二进制保留为 tza-server.previous，数据目录不动，已有的密钥与账号保留；
# 服务已在运行时自动重启换上新版本，并等到新版本应答 /healthz 再结束。
# 后台初始密码只在首次安装时显示一次；忘记密码执行 sudo tza-server reset-password --data 数据目录。
set -eu

REPO="leeska/TZA-Probe"
RAW="https://raw.githubusercontent.com/${REPO}/main"
MODE="release"
VERSION=""
REF="main"
LISTEN="127.0.0.1:25774"
DATA="/var/lib/tza-probe"
BIN="/usr/local/bin/tza-server"
UNIT="/etc/systemd/system/tza-server.service"
SERVICE_USER="tza-probe"
SYSTEMD="auto"
PICKED=""

usage() {
  cat <<'EOF'
TZA Probe Server 一键安装（Linux amd64 / arm64）

  curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh -s -- [选项]

安装方式（二选一，默认 --release）
  --release [vX.Y.Z] 从 GitHub Release 下载预编译发布包，免 git 与 Go；不写版本装最新发布
  --source           从源码构建（需要 git 与 Go），默认 main 分支
  --ref REF          源码构建用的分支或 tag，默认 main（隐含 --source）
  --version vX.Y.Z   同 --release vX.Y.Z

其他选项
  --listen ADDR      监听地址，默认 127.0.0.1:25774（跨主机接入请配置 HTTPS 反代）
  --data DIR         数据目录，默认 /var/lib/tza-probe（保存后台账号、数据库与兼容 API 凭据）
  --no-systemd       只安装二进制与数据目录，不注册 systemd 服务
  -h, --help         显示本说明
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --release)
      MODE="release"; PICKED="${PICKED}r"; shift
      case "${1:-}" in v[0-9]*) VERSION="$1"; shift ;; esac ;;
    --version) MODE="release"; PICKED="${PICKED}r"; VERSION="${2:?--version 需要 vX.Y.Z}"; shift 2 ;;
    --source) MODE="source"; PICKED="${PICKED}s"; shift ;;
    --ref) MODE="source"; PICKED="${PICKED}s"; REF="${2:?--ref 需要分支或 tag}"; shift 2 ;;
    --listen) LISTEN="${2:?--listen 需要地址}"; shift 2 ;;
    --data) DATA="${2:?--data 需要目录}"; shift 2 ;;
    --no-systemd) SYSTEMD="no"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done
case "$PICKED" in *r*s*|*s*r*) PICKED="both" ;; esac
if [ "$PICKED" = "both" ]; then
  echo "从源码构建（--source/--ref）与从 Release 安装（--release/--version）只能选一种" >&2; exit 2
fi
case "$VERSION" in ""|v*) ;; *) echo "版本号要以 v 开头，例如 v1.0.0：$VERSION" >&2; exit 2 ;; esac

if [ "$(id -u)" != "0" ]; then
  # 通过 curl | sh 执行时脚本本体就在标准输入上，不能用 sudo 重新执行自己，
  # 只能请用户把整条命令加上 sudo 再跑一遍。
  echo "请以 root 执行，或在整条命令前加 sudo：" >&2
  echo "  curl -fsSL ${RAW}/deploy/install.sh | sudo sh -s -- ..." >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的架构：$(uname -m)（仅提供 amd64 / arm64）" >&2; exit 1 ;;
esac

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "缺少 $1，请先安装后重试" >&2; exit 1; }
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
UNIT_SRC=""

if [ "$MODE" = "release" ]; then
  need curl
  need sha256sum
  echo "安装方式：GitHub Release 预编译发布包"
  if [ -z "$VERSION" ]; then
    # /releases/latest 会跳到 /releases/tag/<最新 tag>，从最终地址里取版本号。
    LATEST="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest" || true)"
    VERSION="${LATEST##*/tag/}"
    case "$VERSION" in
      v[0-9]*) echo "最新发布：${VERSION}" ;;
      *) echo "查不到最新发布版本，请用 --release vX.Y.Z 指定，或改用 --source 从源码构建" >&2; exit 1 ;;
    esac
  fi
  PACK="tza-probe-linux-${ARCH}.tar.gz"
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${PACK}"
  echo "下载 ${URL}"
  if ! curl -fsSL -o "${WORK}/${PACK}" "$URL"; then
    echo "下载失败：确认该版本已发布（发布包名 ${PACK}）" >&2
    exit 1
  fi
  if curl -fsSL -o "${WORK}/${PACK}.sha256" "${URL}.sha256"; then
    (cd "$WORK" && sha256sum -c "${PACK}.sha256") || { echo "校验和不匹配，已中止安装" >&2; exit 1; }
  else
    echo "未取到校验和文件，跳过校验" >&2
  fi
  tar -xzf "${WORK}/${PACK}" -C "$WORK"
  [ -f "${WORK}/tza-server" ] || { echo "发布包里没有 tza-server" >&2; exit 1; }
  [ -f "${WORK}/deploy/tza-server.service" ] && UNIT_SRC="${WORK}/deploy/tza-server.service"
else
  need git
  need go
  echo "安装方式：从源码构建 ${REF}"
  echo "克隆 ${REPO} 并构建（需要能访问 github.com 与 Go 模块代理）"
  git clone --depth 1 --branch "$REF" "https://github.com/${REPO}.git" "${WORK}/src"
  if [ -f "${WORK}/src/scripts/version.sh" ]; then
    VERSION=$(sh "${WORK}/src/scripts/version.sh" "${WORK}/src")
  else
    # Older release tags predate the shared resolver. Preserve the checked-out tag.
    VERSION=$(git -C "${WORK}/src" describe --exact-match --tags --match 'v[0-9]*' HEAD 2>/dev/null || true)
    if ! printf '%s\n' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
      BASE_VERSION=$(sed -n 's/^var Version = "\([^"]*\)".*/\1/p' "${WORK}/src/internal/model/model.go")
      VERSION="${BASE_VERSION%%-*}-dev+$(git -C "${WORK}/src" rev-parse --short HEAD)"
    fi
  fi
  LDFLAGS="-s -w -X github.com/${REPO}/internal/model.Version=${VERSION}"
  (cd "${WORK}/src" && CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" \
    -o "${WORK}/tza-server" ./cmd/server)
  [ -f "${WORK}/src/deploy/tza-server.service" ] && UNIT_SRC="${WORK}/src/deploy/tza-server.service"
  # 源码构建默认不含 Agent 二进制，后台的一键安装命令会没用。这里顺手把两种架构都
  # 编好放进数据目录（Server 分发时优先用数据目录里这份，见 docs/DEPLOYMENT.md）。
  AGENTS="${WORK}/agents"
  mkdir -p "$AGENTS"
  for arch in amd64 arm64; do
    (cd "${WORK}/src" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" \
      -o "${AGENTS}/tza-agent-linux-${arch}" ./cmd/agent)
  done
  echo "已构建 Agent：tza-agent-linux-amd64、tza-agent-linux-arm64"
fi

if [ -f "$BIN" ]; then
  cp -p "$BIN" "${BIN}.previous"
  echo "已保留旧二进制：${BIN}.previous"
fi
install -m 0755 "${WORK}/tza-server" "$BIN"

if ! getent group "$SERVICE_USER" >/dev/null 2>&1; then
  groupadd --system "$SERVICE_USER"
fi
if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --gid "$SERVICE_USER" --home-dir "$DATA" --shell /usr/sbin/nologin "$SERVICE_USER"
fi
install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA"
# 数据目录里的 Agent 优先于 Server 内嵌的那份。源码构建放进去的打上标记，之后改用 Release
# 安装时据此清掉，不然发布包里签过名的 Agent 会一直被旧的本机构建挡住。
MARK="${DATA}/agents/.source-build"
if [ -n "${AGENTS:-}" ] && [ -d "${AGENTS:-}" ]; then
  install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" "${DATA}/agents"
  for arch in amd64 arm64; do
    install -m 0600 -o "$SERVICE_USER" -g "$SERVICE_USER" \
      "${AGENTS}/tza-agent-linux-${arch}" "${DATA}/agents/tza-agent-linux-${arch}"
  done
  echo "$VERSION" > "$MARK"
  chown "$SERVICE_USER:$SERVICE_USER" "$MARK"
elif [ -f "$MARK" ]; then
  rm -f "${DATA}/agents/tza-agent-linux-amd64" "${DATA}/agents/tza-agent-linux-arm64" "$MARK"
  echo "已移除之前源码构建放入 ${DATA}/agents 的 Agent，改用发布包内嵌的 Agent"
elif [ -f "${DATA}/agents/tza-agent-linux-amd64" ] || [ -f "${DATA}/agents/tza-agent-linux-arm64" ]; then
  echo "注意：${DATA}/agents 里已有 Agent，会优先于发布包内嵌的 Agent 分发；不需要请删掉后重启服务" >&2
fi

# 服务启动前生成管理密钥与后台账号：密码只在这里打印一次，不进 systemd 日志。
# init 以 root 运行时会把新文件交给数据目录的属主（服务账户）。
CREDS="$("$BIN" init --data "$DATA")"

started="no"
if [ "$SYSTEMD" != "no" ] && [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  if [ -z "$UNIT_SRC" ]; then
    UNIT_SRC="${WORK}/tza-server.service"
    fetch="no"
    if command -v curl >/dev/null 2>&1 && curl -fsSL -o "$UNIT_SRC" "${RAW}/deploy/tza-server.service"; then
      fetch="yes"
    fi
    if [ "$fetch" = "no" ]; then
      # 与 deploy/tza-server.service 保持一致，取不到远端单元文件时的兜底。
      cat > "$UNIT_SRC" <<'UNIT'
[Unit]
Description=TZA Probe Server
After=network.target

[Service]
User=tza-probe
Group=tza-probe
ExecStart=/usr/local/bin/tza-server --listen 127.0.0.1:25774 --data /var/lib/tza-probe
Restart=on-failure
RestartSec=5
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/tza-probe

[Install]
WantedBy=multi-user.target
UNIT
    fi
  fi
  sed -e "s#^ExecStart=.*#ExecStart=${BIN} --listen ${LISTEN} --data ${DATA}#" "$UNIT_SRC" > "$UNIT"
  # 数据目录不在默认位置时，systemd 的只读保护也要跟着放行。
  if [ "$DATA" != "/var/lib/tza-probe" ]; then
    sed -i "s#^ReadWritePaths=.*#ReadWritePaths=${DATA}#" "$UNIT"
  fi
  systemctl daemon-reload
  systemctl enable tza-server >/dev/null 2>&1
  # 升级时服务还在跑旧进程，enable --now 不会换掉它：一律 restart（没在跑时等同 start）。
  if systemctl is-active --quiet tza-server; then ACTION="重启"; else ACTION="启动"; fi
  # 重启失败也要把后面的登录信息打印出来（首次安装的密码只显示这一次），最后再以失败退出。
  if systemctl restart tza-server; then started="yes"; else started="failed"; fi
  # 等到 /healthz 报出的正是刚装的版本，才算新进程起来了（旧进程退出前也可能还在应答）。
  RUNNING=""
  if [ "$started" = "yes" ] && command -v curl >/dev/null 2>&1; then
    for _ in $(seq 1 30); do
      RUNNING="$(curl -fsS -m 2 "http://${LISTEN}/healthz" 2>/dev/null | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')"
      [ "$RUNNING" = "$VERSION" ] && break
      sleep 1
    done
  fi
fi

echo
if [ "$MODE" = "release" ]; then FROM="GitHub Release"; else FROM="源码构建 ${REF}"; fi
echo "已安装：${BIN}（版本 ${VERSION}，来自 ${FROM}）"
echo "数据目录：${DATA}"
if [ "$started" = "yes" ]; then
  if [ "$RUNNING" = "$VERSION" ]; then
    echo "服务已${ACTION}，正在运行版本 ${RUNNING}"
  elif ! command -v curl >/dev/null 2>&1; then
    echo "服务已${ACTION}（没有 curl，未核对运行版本）"
  elif [ -n "$RUNNING" ]; then
    echo "!! 服务已${ACTION}，但 30 秒内应答的仍是版本 ${RUNNING}，请查看服务日志" >&2
  else
    echo "!! 服务已${ACTION}，但 30 秒内没有应答 http://${LISTEN}/healthz，请查看服务日志" >&2
  fi
  echo "服务状态：systemctl status tza-server"
  echo "服务日志：journalctl -u tza-server -n 100 --no-pager"
elif [ "$started" = "failed" ]; then
  echo "!! systemctl restart tza-server 失败，服务没有起来。查看原因：" >&2
  echo "   journalctl -u tza-server -n 100 --no-pager" >&2
  echo "   旧二进制保留在 ${BIN}.previous，可 cp -p 回去后再 systemctl restart tza-server" >&2
else
  echo "未注册 systemd 服务，手动启动："
  echo "  sudo -u ${SERVICE_USER} ${BIN} --listen ${LISTEN} --data ${DATA}"
fi
echo
echo "================ 后台登录信息 ================"
echo "$CREDS"
echo "=============================================="
echo "后台仅支持用户名密码登录；MCP API Key 请登录后台，在“面板设置”中生成。"
echo "忘记后台密码：sudo ${BIN} reset-password --data ${DATA}"
if [ "${LISTEN%%:*}" = "127.0.0.1" ]; then
  echo "管理页面：http://${LISTEN}/admin （跨主机访问请在前面配置 HTTPS 反代）"
else
  echo "管理页面：http://${LISTEN}/admin"
fi
echo
echo "下一步：登录后台新增节点，“接入”弹窗会给出一条命令，在 Agent 主机执行即可装好 Agent。"
[ "$started" != "failed" ] || exit 1
