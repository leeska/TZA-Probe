#!/bin/sh
# TZA Probe Agent 一键安装 / 升级脚本。由 Server 的 /install.sh 提供，后台“接入”弹窗会给出完整命令：
#
#   curl -fsSL https://probe.example.com/install.sh | sh -s -- --endpoint https://probe.example.com --token <节点密钥>
#
# 以 root 直接执行；非 root 且系统有 sudo 时会自动改用 sudo 重新执行。
# 可选参数：
#   --interfaces eth0,eth1   只统计这些网卡的流量（默认统计全部非 loopback 网卡）
#   --nexttrace /usr/bin/nexttrace   三网回程使用的 NextTrace 路径（默认自动查找 PATH）
#   --allow-http             允许明文 HTTP 的 endpoint（仅限可信私网；endpoint 为 http:// 时自动开启）
#   --uninstall              停止服务并删除程序、配置和 systemd 单元
#
# 不带 --token 再次执行时保留现有 /etc/tza-probe/agent.json，只更新二进制并重启，即升级。
# 密钥也可通过环境变量 TZA_TOKEN 传入，避免出现在命令行。
set -eu

ENDPOINT="${TZA_ENDPOINT:-}"
TOKEN="${TZA_TOKEN:-}"
INTERFACES=""
NEXTTRACE=""
ALLOW_HTTP=""
UNINSTALL=""
CONFIG=/etc/tza-probe/agent.json
BIN=/usr/local/bin/tza-agent
UNIT=/etc/systemd/system/tza-agent.service

while [ $# -gt 0 ]; do
  case "$1" in
    --endpoint) ENDPOINT="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --interfaces) INTERFACES="$2"; shift 2 ;;
    --nexttrace) NEXTTRACE="$2"; shift 2 ;;
    --allow-http) ALLOW_HTTP=true; shift ;;
    --uninstall) UNINSTALL=true; shift ;;
    -h|--help) sed -n '2,14p' "$0" 2>/dev/null || true; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

fail() { echo "错误: $*" >&2; exit 1; }

if [ "$(id -u)" -ne 0 ]; then
  # 通过管道执行时脚本正文已被 sh 读走，无法直接提权重跑；用 sudo 重新拉取一次。
  command -v sudo >/dev/null 2>&1 || fail "需要 root 权限：请切换到 root 后再执行"
  [ -n "$ENDPOINT" ] || fail "需要 root 权限：请以 root 执行，或加上 --endpoint 以便通过 sudo 重新执行"
  command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || fail "需要 curl 或 wget"
  # 位置参数已被上面的解析消费���按解析结果重建；单引号转义保证原样传给 sudo 下的 sh
  q() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }
  quoted=""
  [ -n "$INTERFACES" ] && quoted="$quoted --interfaces $(q "$INTERFACES")"
  [ -n "$NEXTTRACE" ] && quoted="$quoted --nexttrace $(q "$NEXTTRACE")"
  [ -n "$ALLOW_HTTP" ] && quoted="$quoted --allow-http"
  [ -n "$UNINSTALL" ] && quoted="$quoted --uninstall"
  if command -v curl >/dev/null 2>&1; then
    get="curl -fsSL '$ENDPOINT/install.sh'"
  else
    get="wget -qO- '$ENDPOINT/install.sh'"
  fi
  echo "非 root，改用 sudo 重新执行 ..." >&2
  exec sudo env TZA_TOKEN="$TOKEN" TZA_ENDPOINT="$ENDPOINT" sh -c "$get | sh -s --$quoted"
fi
command -v systemctl >/dev/null 2>&1 || fail "需要使用 systemd 的 Linux"

if [ -n "$UNINSTALL" ]; then
  systemctl disable --now tza-agent 2>/dev/null || true
  rm -f "$UNIT" "$BIN"
  rm -rf /etc/tza-probe
  systemctl daemon-reload
  echo "已卸载 TZA Probe Agent"
  exit 0
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) fail "不支持的架构 $(uname -m)，请手动编译 Agent" ;;
esac

UPGRADE=""
if [ -n "$TOKEN" ]; then
  case "$TOKEN" in
    *[!0-9a-fA-F]*) fail "密钥格式不正确，应为后台生成的 64 位十六进制字符串" ;;
  esac
  [ ${#TOKEN} -eq 64 ] || fail "密钥长度不正确，应为 64 位"
else
  [ -f "$CONFIG" ] || fail "缺少 --token；首次安装请使用后台“接入”给出的完整命令"
  UPGRADE=true
  # 升级：endpoint 从现有配置读取
  [ -n "$ENDPOINT" ] || ENDPOINT=$(sed -n 's/^[[:space:]]*"endpoint":[[:space:]]*"\([^"]*\)".*/\1/p' "$CONFIG" | head -n 1)
fi
[ -n "$ENDPOINT" ] || fail "缺少 --endpoint（Agent 主机能访问的 Server 地址）"
ENDPOINT=${ENDPOINT%/}
case "$ENDPOINT" in
  https://*) ;;
  http://*) ALLOW_HTTP=true; echo "警告: endpoint 使用明文 HTTP，仅适用于可信私网" >&2 ;;
  *) fail "endpoint 必须以 http:// 或 https:// 开头" ;;
esac

# 下载工具：curl 或 wget 任一
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
else
  fail "需要 curl 或 wget"
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
NAME="tza-agent-linux-$ARCH"
echo "下载 $ENDPOINT/dl/$NAME ..."
fetch "$ENDPOINT/dl/$NAME" "$TMP/tza-agent" || fail "下载失败：请确认 Server 地址可达，且该 Server 提供了 $ARCH 架构的 Agent"
fetch "$ENDPOINT/dl/$NAME.sha256" "$TMP/sum" || fail "无法获取校验和"
EXPECTED=$(cut -d' ' -f1 "$TMP/sum")
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$TMP/tza-agent" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "$TMP/tza-agent" | cut -d' ' -f1)
else
  ACTUAL="$EXPECTED"
  echo "警告: 系统没有 sha256sum，跳过完整性校验" >&2
fi
[ "$ACTUAL" = "$EXPECTED" ] || fail "校验和不匹配，下载可能被截断或篡改"
chmod 0755 "$TMP/tza-agent"
"$TMP/tza-agent" --version >/dev/null 2>&1 || fail "下载的程序无法运行"

# 先停旧服务再替换，避免向运行中的文件写入
systemctl stop tza-agent 2>/dev/null || true
[ -f "$BIN" ] && cp -p "$BIN" "$BIN.previous"
install -m 0755 "$TMP/tza-agent" "$BIN"

if [ -z "$UPGRADE" ]; then
  [ -n "$NEXTTRACE" ] || NEXTTRACE=$(command -v nexttrace 2>/dev/null || true)
  IFACES=""
  OLDIFS=$IFS; IFS=','
  for i in $INTERFACES; do
    i=$(printf '%s' "$i" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
    [ -n "$i" ] || continue
    case "$i" in *[\"\\]*) fail "网卡名不能包含引号或反斜杠" ;; esac
    IFACES="${IFACES:+$IFACES, }\"$i\""
  done
  IFS=$OLDIFS
  case "$NEXTTRACE" in *[\"\\]*) fail "NextTrace 路径不能包含引号或反斜杠" ;; esac
  install -d -m 0700 /etc/tza-probe
  umask 077
  # 用 printf 逐字段写出，密钥与路径不经过 shell 二次展开
  printf '{\n  "endpoint": "%s",\n  "token": "%s",\n  "interfaces": [%s],\n  "mount": "/",\n  "nexttrace": "%s",\n  "allow_http": %s\n}\n' \
    "$ENDPOINT" "$TOKEN" "$IFACES" "$NEXTTRACE" "${ALLOW_HTTP:-false}" > "$CONFIG"
  chmod 0600 "$CONFIG"
fi

install -d -m 0755 "$(dirname "$UNIT")"
cat > "$UNIT" <<'TZA_AGENT_SERVICE'
[Unit]
Description=TZA Probe Agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/tza-agent --config /etc/tza-probe/agent.json
Restart=on-failure
RestartSec=5
UMask=0077
NoNewPrivileges=true
CapabilityBoundingSet=CAP_NET_RAW
AmbientCapabilities=CAP_NET_RAW
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
TZA_AGENT_SERVICE
chmod 0644 "$UNIT"
systemctl daemon-reload
systemctl enable tza-agent >/dev/null 2>&1
systemctl restart tza-agent
sleep 2
if systemctl is-active --quiet tza-agent; then
  if [ -n "$UPGRADE" ]; then echo "Agent 已升级并重启"; else echo "Agent 已安装并启动，稍后后台节点会显示在线"; fi
  if [ -z "$NEXTTRACE" ] && [ -z "$UPGRADE" ]; then
    echo "提示: 未找到 NextTrace，三网回程任务将显示“工具未配置”；安装后在 $CONFIG 填写路径并重启服务"
  fi
else
  echo "Agent 未能启动，最近日志：" >&2
  journalctl -u tza-agent -n 20 --no-pager >&2 || true
  exit 1
fi
