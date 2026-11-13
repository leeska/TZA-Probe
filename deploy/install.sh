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
# 后台账号、装 systemd 服务。加 --with-browser 时再装地区偏移检测用的隔离浏览器
# （固定版本的 Chromium、独立用户与无网络的 tza-browser 服务，见 docs/IP-GUARD-INTEGRATION.md）。
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
BROWSER="no"
BROWSER_UNIT="/etc/systemd/system/tza-browser.service"
BROWSER_DIR="/opt/tza-browser"
BROWSER_STATE="/var/lib/tza-browser"
BROWSER_RUN="/run/tza-browser"
BROWSER_USER="tza-browser"
BROWSER_GROUP="tza-browser-control"
NGINX="auto"
NGINX_LOG="/etc/nginx/conf.d/tza-probe-log.conf"

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
  --with-browser     同时安装地区偏移检测的隔离浏览器（约 300 MB 内存、200 MB 磁盘；需要 systemd）。
                     装过一次后，再次执行本脚本会保留并一起升级它
  --no-nginx         本机装有 nginx 时也不写入 /etc/nginx/conf.d/tza-probe-log.conf
                     （默认写入：定义 $tza_loggable，站点用它跳过 Agent 上报的访问日志）
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
    --with-browser) BROWSER="yes"; shift ;;
    --no-nginx) NGINX="no"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1" >&2; usage >&2; exit 2 ;;
  esac
done
case "$PICKED" in *r*s*|*s*r*) PICKED="both" ;; esac
if [ "$PICKED" = "both" ]; then
  echo "从源码构建（--source/--ref）与从 Release 安装（--release/--version）只能选一种" >&2; exit 2
fi
case "$VERSION" in ""|v*) ;; *) echo "版本号要以 v 开头，例如 v1.0.0：$VERSION" >&2; exit 2 ;; esac
# 装过浏览器的主机：升级时不带 --with-browser 也保留它。
[ -f "$BROWSER_UNIT" ] && BROWSER="yes"
if [ "$BROWSER" = "yes" ] && [ "$SYSTEMD" = "no" ]; then
  echo "--with-browser 需要 systemd（浏览器靠 systemd 的 PrivateNetwork 隔离网络），不能与 --no-systemd 同用" >&2; exit 2
fi

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
BROWSER_UNIT_SRC=""

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
  [ -f "${WORK}/deploy/tza-browser.service" ] && BROWSER_UNIT_SRC="${WORK}/deploy/tza-browser.service"
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
  [ -f "${WORK}/src/deploy/tza-browser.service" ] && BROWSER_UNIT_SRC="${WORK}/src/deploy/tza-browser.service"
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

# 升级前先用新版本核对数据目录：库比新版本还新、或早于 v2.0.0（不再升级）时在这里停下，
# 旧二进制与正在运行的服务原样保留。v2.0.0 起的库能原地升级，迁移在新版本启动时执行。
# 旧版本的 tza-server 没有这条子命令，跳过。
if [ -d "$DATA" ] && grep -q 'check-data' "${WORK}/tza-server" 2>/dev/null; then
  if ! "${WORK}/tza-server" check-data --data "$DATA"; then
    echo "!! 版本 ${VERSION} 不能使用现有数据目录 ${DATA}，已中止，现有安装与服务未做任何改动。" >&2
    echo "   库比这个版本还新：请安装更新的版本。库早于 v2.0.0：换一个数据目录全新部署，" >&2
    echo "   例如 --data /var/lib/tza-probe-v2（旧目录保留，可随时回到旧版本），或继续安装旧版本 --release v1.9.1。" >&2
    exit 1
  fi
fi

if [ -f "$BIN" ] && [ -f "${DATA}/tza-probe.db" ]; then
  echo "提示：新版本启动时可能迁移数据库，迁移后旧版本打不开；需要回滚的话，请先备份 ${DATA}。"
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

# ---------------------------------------------------------------- 隔离浏览器（--with-browser）

# ldd 报缺的共享库 → Debian / Ubuntu 包名；带 t64 的是 Debian 13 / Ubuntu 24.04 起的新名字，
# 两个都试，装得上哪个用哪个。"fonts" 不是库：没有字体时 Chromium 一遇到网页字体就卡住。
browser_packages() {
  for lib in "$@"; do
    case "$lib" in
      libX11.so.*) echo libx11-6 ;;
      libXcomposite.so.*) echo libxcomposite1 ;;
      libXdamage.so.*) echo libxdamage1 ;;
      libXext.so.*) echo libxext6 ;;
      libXfixes.so.*) echo libxfixes3 ;;
      libXrandr.so.*) echo libxrandr2 ;;
      libxcb.so.*) echo libxcb1 ;;
      libxkbcommon.so.*) echo libxkbcommon0 ;;
      libasound.so.*) echo "libasound2t64|libasound2" ;;
      libatk-1.0.so.*) echo "libatk1.0-0t64|libatk1.0-0" ;;
      libatk-bridge-2.0.so.*) echo "libatk-bridge2.0-0t64|libatk-bridge2.0-0" ;;
      libatspi.so.*) echo "libatspi2.0-0t64|libatspi2.0-0" ;;
      libglib-2.0.so.*|libgio-2.0.so.*|libgobject-2.0.so.*) echo "libglib2.0-0t64|libglib2.0-0" ;;
      libcups.so.*) echo "libcups2t64|libcups2" ;;
      libdbus-1.so.*) echo libdbus-1-3 ;;
      libexpat.so.*) echo libexpat1 ;;
      libgbm.so.*) echo libgbm1 ;;
      libnspr4.so) echo libnspr4 ;;
      libnss3.so|libnssutil3.so|libsmime3.so) echo libnss3 ;;
      libdrm.so.*) echo libdrm2 ;;
      libudev.so.*) echo libudev1 ;;
      fonts) echo fontconfig; echo fonts-dejavu-core ;;
      *) echo "?$lib" ;;
    esac
  done | sort -u
}

browser_libraries() {
  missing="$("$BIN" browser-install --dir "$BROWSER_DIR" "$@")" && return 0
  status=$?
  [ "$status" = 3 ] || { echo "安装 Chromium 失败（见上方信息）" >&2; exit 1; }
  if ! command -v apt-get >/dev/null 2>&1; then
    echo "Chromium 还缺这些系统库，请用发行版的包管理器装上后重新执行本脚本：" >&2
    echo "$missing" | sed 's/^/  /' >&2
    exit 1
  fi
  picked=""
  unknown=""
  for want in $(browser_packages $missing); do
    case "$want" in
      \?*) unknown="${unknown} ${want#?}"; continue ;;
    esac
    choice=""
    for candidate in $(echo "$want" | tr '|' ' '); do
      if apt-cache show "$candidate" >/dev/null 2>&1; then choice="$candidate"; break; fi
    done
    [ -n "$choice" ] && picked="${picked} ${choice}"
  done
  if [ -n "$unknown" ]; then
    echo "Chromium 还缺这些系统库，脚本认不出对应的包，请手动装上后重新执行：${unknown}" >&2
    exit 1
  fi
  echo "安装 Chromium 依赖：${picked}"
  # shellcheck disable=SC2086
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends $picked >/dev/null
  if ! still="$("$BIN" browser-install --dir "$BROWSER_DIR" --check-libs)"; then
    echo "装完依赖后 Chromium 仍缺：" >&2
    echo "$still" | sed 's/^/  /' >&2
    exit 1
  fi
}

# Chromium 的沙箱要用非特权用户命名空间。Ubuntu 23.10 起 AppArmor 默认拦下它：给这一个
# Chromium 可执行文件写一份只放行 userns 的配置（Google Chrome 的官方包也是这么做的）。
# 内核整体关掉了 userns 时不替你改内核参数，只说明怎么改。
browser_userns() {
  if [ "$(sysctl -n kernel.unprivileged_userns_clone 2>/dev/null || echo 1)" = 0 ]; then
    echo "内核关闭了非特权用户命名空间（kernel.unprivileged_userns_clone=0），Chromium 的沙箱无法工作。" >&2
    echo "确认可以开启后执行：sysctl -w kernel.unprivileged_userns_clone=1，并写入 /etc/sysctl.d/ 永久生效，再重新执行本脚本。" >&2
    exit 1
  fi
  if [ "$(sysctl -n user.max_user_namespaces 2>/dev/null || echo 1)" = 0 ]; then
    echo "内核把 user.max_user_namespaces 设成了 0，Chromium 的沙箱无法工作；调大后重新执行本脚本。" >&2
    exit 1
  fi
  if [ "$(sysctl -n kernel.apparmor_restrict_unprivileged_userns 2>/dev/null || echo 0)" = 1 ]; then
    command -v apparmor_parser >/dev/null 2>&1 || { echo "AppArmor 限制了用户命名空间，但找不到 apparmor_parser" >&2; exit 1; }
    cat > /etc/apparmor.d/tza-browser-chromium <<'APPARMOR'
# TZA Probe：地区偏移检测的隔离浏览器。只放行 Chromium 沙箱需要的用户命名空间。
abi <abi/4.0>,
include <tunables/global>

profile tza-browser-chromium /opt/tza-browser/chromium-*/chrome-headless-shell-*/chrome-headless-shell flags=(unconfined) {
  userns,
  include if exists <local/tza-browser-chromium>
}
APPARMOR
    apparmor_parser -r /etc/apparmor.d/tza-browser-chromium
    echo "已为 Chromium 写入 AppArmor 配置：/etc/apparmor.d/tza-browser-chromium"
  fi
}

browser_unit() {
  if [ -n "$BROWSER_UNIT_SRC" ]; then
    cp "$BROWSER_UNIT_SRC" "$BROWSER_UNIT"
    return
  fi
  if command -v curl >/dev/null 2>&1 && curl -fsSL -o "$BROWSER_UNIT" "${RAW}/deploy/tza-browser.service"; then
    return
  fi
  # 与 deploy/tza-browser.service 保持一致（cmd/server 的测试会核对），取不到时的兜底。
  cat > "$BROWSER_UNIT" <<'UNIT'
[Unit]
Description=TZA Probe browser worker (region drift check)
Documentation=https://github.com/leeska/TZA-Probe/blob/main/docs/IP-GUARD-INTEGRATION.md
After=tza-server.service

[Service]
# Chromium reads Google / Meta pages for the region drift check. It has no network
# of its own: PrivateNetwork leaves only loopback, and every connection goes through
# the Server's private socket and from there through the node's Agent.
User=tza-browser
Group=tza-browser
SupplementaryGroups=tza-browser-control
ExecStart=/usr/local/bin/tza-server browser-worker --socket /run/tza-browser/control.sock --state /var/lib/tza-browser
StateDirectory=tza-browser
StateDirectoryMode=0700
Restart=on-failure
RestartSec=5
PrivateNetwork=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ReadWritePaths=/run/tza-browser
NoNewPrivileges=yes
CapabilityBoundingSet=
LockPersonality=yes
RestrictRealtime=yes
UMask=0077
# Chromium's own sandbox needs unprivileged user namespaces: do not add
# RestrictNamespaces= or PrivateUsers= here.
# Resources: one job at a time; raise MemoryMax with a drop-in on large pages.
MemoryMax=700M
MemorySwapMax=0
CPUQuota=85%
TasksMax=384
KillMode=control-group

[Install]
WantedBy=multi-user.target
UNIT
}

setup_browser() {
  # 旧版本的 tza-server 没有 browser-install / browser-worker：它会把不认识的子命令当成
  # 普通启动，以 root 起一个 Server 卡住脚本。先确认这个二进制带着浏览器。
  if ! grep -q 'browser-install' "$BIN" 2>/dev/null; then
    echo "!! 版本 ${VERSION} 的 tza-server 不含隔离浏览器，跳过 --with-browser（Server 照常安装）。" >&2
    echo "   请安装包含浏览器的发布版本，或用 --source 从源码构建。" >&2
    BROWSER="failed"
    return
  fi
  echo "安装地区偏移检测的隔离浏览器"
  getent group "$BROWSER_USER" >/dev/null 2>&1 || groupadd --system "$BROWSER_USER"
  id -u "$BROWSER_USER" >/dev/null 2>&1 || useradd --system --gid "$BROWSER_USER" --home-dir "$BROWSER_STATE" --shell /usr/sbin/nologin "$BROWSER_USER"
  # 只有 Server 与浏览器两个服务账户在这个组里：组决定谁能连私有 socket。
  getent group "$BROWSER_GROUP" >/dev/null 2>&1 || groupadd --system "$BROWSER_GROUP"
  usermod -aG "$BROWSER_GROUP" "$SERVICE_USER"
  usermod -aG "$BROWSER_GROUP" "$BROWSER_USER"
  browser_libraries
  browser_userns
  echo "d ${BROWSER_RUN} 0750 ${SERVICE_USER} ${BROWSER_GROUP} -" > /etc/tmpfiles.d/tza-browser.conf
  systemd-tmpfiles --create /etc/tmpfiles.d/tza-browser.conf
  install -d -m 0700 -o "$BROWSER_USER" -g "$BROWSER_USER" "$BROWSER_STATE"
  # 先在与正式服务相同的隔离里自检：只有回环网卡、非 root、沙箱生效、有字体。
  echo "浏览器自检（与正式服务同样的隔离）"
  if ! systemd-run --quiet --wait --pipe --collect --uid="$BROWSER_USER" --gid="$BROWSER_USER" \
      -p PrivateNetwork=yes -p PrivateTmp=yes -p NoNewPrivileges=yes -p ProtectHome=yes \
      "$BIN" browser-worker --self-test --state "$BROWSER_STATE"; then
    echo "!! 浏览器自检未通过，未启用地区偏移检测（Server 照常安装）。上面的 JSON 指出了原因。" >&2
    BROWSER="failed"
    return
  fi
  browser_unit
  # Server 侧：打开私有 socket，并允许它在只读的 /run 里写这个目录。
  sed -i -e "s#^ExecStart=\(.*\)\$#ExecStart=\1 --browser-socket ${BROWSER_RUN}/control.sock#" "$UNIT"
  sed -i -e "s#^ReadWritePaths=\(.*\)\$#ReadWritePaths=\1 ${BROWSER_RUN}#" "$UNIT"
  grep -q '^SupplementaryGroups=' "$UNIT" || sed -i "/^Group=/a SupplementaryGroups=${BROWSER_GROUP}" "$UNIT"
}

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
  [ "$BROWSER" = "yes" ] && setup_browser
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
  if [ "$BROWSER" = "yes" ] && [ "$started" = "yes" ]; then
    systemctl enable tza-browser >/dev/null 2>&1
    if systemctl restart tza-browser; then BROWSER="started"; else BROWSER="failed"; fi
  fi
elif [ "$BROWSER" = "yes" ]; then
  echo "--with-browser 需要 systemd，本机没有可用的 systemd，未安装浏览器" >&2
  BROWSER="failed"
fi

# ---------------------------------------------------------------- nginx 访问日志过滤
# 本机装有 nginx 时写入 conf.d/tza-probe-log.conf，只定义 $tza_loggable，不改任何站点：
# 站点要在 server 块里写 access_log ... if=$tza_loggable 才生效（见 deploy/nginx-site.conf）。
# nginx -t 不通过就撤回这个文件，nginx 原样不动（本机配置原本就有错时也会走到这里）。
nginx_log() {
  NGINX="skipped"
  command -v nginx >/dev/null 2>&1 && [ -d /etc/nginx/conf.d ] || return 0
  cat > "${WORK}/tza-probe-log.conf" <<'NGINX'
# TZA Probe：Agent 周期上报的访问日志过滤（http 层，由 deploy/install.sh 装到 /etc/nginx/conf.d/）。
# 每个节点每隔几秒就上报一次，全部记下来一天上百 MB。/api/v1/agent/* 返回 2xx / 3xx，
# 或 499（Agent 主动断开长轮询）时 $tza_loggable 为 0；其他请求和 Agent 的 4xx / 5xx 照常记录。
# 站点的 server 块里这样用：access_log /var/log/nginx/access.log combined if=$tza_loggable;
map "$status $request_uri" $tza_loggable {
    "~^([23][0-9][0-9]|499) /api/v1/agent/" 0;
    default 1;
}
NGINX
  if [ -f "$NGINX_LOG" ] && cmp -s "${WORK}/tza-probe-log.conf" "$NGINX_LOG"; then
    NGINX="current"
    return 0
  fi
  OLD_NGINX_LOG=""
  if [ -f "$NGINX_LOG" ]; then OLD_NGINX_LOG="${WORK}/tza-probe-log.conf.old"; cp -p "$NGINX_LOG" "$OLD_NGINX_LOG"; fi
  install -m 0644 "${WORK}/tza-probe-log.conf" "$NGINX_LOG"
  if ! nginx -t >/dev/null 2>&1; then
    if [ -n "$OLD_NGINX_LOG" ]; then cp -p "$OLD_NGINX_LOG" "$NGINX_LOG"; else rm -f "$NGINX_LOG"; fi
    NGINX="failed"
    return 0
  fi
  NGINX="installed"
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet nginx; then
    systemctl reload nginx || NGINX="reload-failed"
  fi
}
[ "$NGINX" = "no" ] || nginx_log

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
if [ "$BROWSER" = "started" ]; then
  echo "地区偏移检测浏览器已启动：systemctl status tza-browser（日志 journalctl -u tza-browser）"
  echo "节点开通检测出口：在节点上重新执行接入命令并加 --ipguard（后台「地区偏移检测」页给出完整命令）"
elif [ "$BROWSER" = "failed" ]; then
  echo "!! 地区偏移检测浏览器没有启用，见上方原因；修好后重新执行本脚本并加 --with-browser" >&2
fi
case "$NGINX" in
  installed|current|reload-failed)
    [ "$NGINX" = "installed" ] && echo "已写入 ${NGINX_LOG}（Agent 上报的访问日志过滤）"
    [ "$NGINX" = "reload-failed" ] && echo "!! 已写入 ${NGINX_LOG}，但 systemctl reload nginx 失败，请手动重载" >&2
    if ! grep -rqs 'if=\$tza_loggable' /etc/nginx/sites-enabled /etc/nginx/conf.d; then
      echo "   站点的 server 块里加上 access_log /var/log/nginx/access.log combined if=\$tza_loggable; 才会生效"
    fi ;;
  failed)
    echo "!! 写入 ${NGINX_LOG} 后 nginx -t 未通过，已撤回，nginx 未改动；用 nginx -t 查看原因" >&2 ;;
esac
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
