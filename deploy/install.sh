#!/bin/sh
# TZA Probe Server 一键安装（Linux amd64 / arm64）。
#
# 从源码构建 main 分支（需要 git 与 Go）：
#   curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh
# 从 GitHub Release 安装（免 Go 与 git，发布包同时带两种架构的 Agent）：
#   curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh -s -- --version v2.0.0
#
# 脚本只做四件事：取到 tza-server、装到 /usr/local/bin、建好数据目录、装 systemd 服务。
# 重复执行即升级：旧二进制保留为 tza-server.previous，数据目录不动。
set -eu

REPO="leeska/TZA-Probe"
RAW="https://raw.githubusercontent.com/${REPO}/main"
VERSION=""
LISTEN="127.0.0.1:25774"
DATA="/var/lib/tza-probe"
BIN="/usr/local/bin/tza-server"
UNIT="/etc/systemd/system/tza-server.service"
SERVICE_USER="tza-probe"
SYSTEMD="auto"

usage() {
  cat <<'EOF'
TZA Probe Server 一键安装（Linux amd64 / arm64）

  curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh -s -- [选项]

选项
  --version vX.Y.Z   从 GitHub Release 安装，免 git 与 Go
  --listen ADDR      监听地址，默认 127.0.0.1:25774（跨主机接入请配置 HTTPS 反代）
  --data DIR         数据目录，默认 /var/lib/tza-probe（内含 admin-token 与数据库）
  --no-systemd       只安装二进制与数据目录，不注册 systemd 服务
  -h, --help         显示本说明
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:?--version 需要 vX.Y.Z}"; shift 2 ;;
    --listen) LISTEN="${2:?--listen 需要地址}"; shift 2 ;;
    --data) DATA="${2:?--data 需要目录}"; shift 2 ;;
    --no-systemd) SYSTEMD="no"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done

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

if [ -n "$VERSION" ]; then
  need curl
  need sha256sum
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
  echo "克隆 ${REPO} 并构建（需要能访问 github.com 与 Go 模块代理）"
  git clone --depth 1 "https://github.com/${REPO}.git" "${WORK}/src"
  VERSION="2.0.0-dev+$(git -C "${WORK}/src" rev-parse --short HEAD)"
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
if [ -n "${AGENTS:-}" ] && [ -d "${AGENTS:-}" ]; then
  install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" "${DATA}/agents"
  for arch in amd64 arm64; do
    install -m 0600 -o "$SERVICE_USER" -g "$SERVICE_USER" \
      "${AGENTS}/tza-agent-linux-${arch}" "${DATA}/agents/tza-agent-linux-${arch}"
  done
fi

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
  systemctl enable --now tza-server
  started="yes"
  for _ in $(seq 1 20); do
    if command -v curl >/dev/null 2>&1 && curl -fsS "http://${LISTEN}/healthz" >/dev/null 2>&1; then break; fi
    sleep 1
  done
fi

echo
echo "已安装：${BIN}（版本 ${VERSION}）"
echo "数据目录：${DATA}"
if [ "$started" = "yes" ]; then
  echo "服务状态：systemctl status tza-server"
  echo "服务日志：journalctl -u tza-server -n 100 --no-pager"
else
  echo "未注册 systemd 服务，手动启动："
  echo "  sudo -u ${SERVICE_USER} ${BIN} --listen ${LISTEN} --data ${DATA}"
fi
echo "登录密钥：sudo cat ${DATA}/admin-token"
if [ "${LISTEN%%:*}" = "127.0.0.1" ]; then
  echo "管理页面：http://${LISTEN}/admin （跨主机访问请在前面配置 HTTPS 反代）"
else
  echo "管理页面：http://${LISTEN}/admin"
fi
echo
echo "下一步：登录后台新增节点，“接入”弹窗会给出一条命令，在 Agent 主机执行即可装好 Agent。"
