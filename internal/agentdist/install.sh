#!/bin/sh
# TZA Probe Agent 一键安装 / 升级脚本。由 Server 的 /install.sh 提供，后台“接入”弹窗会给出完整命令：
#
#   curl -fsSL https://probe.example.com/install.sh | sh -s -- --endpoint https://probe.example.com --token <节点密钥>
#
# 以 root 直接执行；非 root 且系统有 sudo 时会自动改用 sudo 重新执行。
# 可选参数：
#   --interfaces eth0,eth1   只统计这些网卡的流量（默认统计全部非 loopback 网卡）
#   --nexttrace /usr/bin/nexttrace   改用外部 NextTrace 二进制（默认用内置的 NTrace-core，无需安装）
#   --geo-provider NAME      内置回程的 ASN/地区数据源，默认 IPAPI.com；也可 disable-geoip、NextTrace-API 等
#                            （NextTrace-API 的 v3 通道靠 NextTrace CLI 自建 WebSocket，本进程内查询必然失败）
#   --trace-method NAME      内置回程的探测方式：icmp（默认，与 NextTrace CLI 一致）、tcp 或 udp
#   --allow-http             允许明文 HTTP 的 endpoint（仅限可信私网；endpoint 为 http:// 时自动开启）
#   --no-plugin              不安装插件组件安装器；主控将无法在此节点安装/更新 IPSentinel 等组件
#   --uninstall              停止服务并删除程序、配置与服务单元（systemd / OpenRC 均可）
#
# 不带 --token 再次执行时保留现有 /etc/tza-probe/agent.json，只更新二进制并重启，即升级。
# Server 配了升级公钥时，脚本把它钉在 /etc/tza-probe/upgrade-key.pub：此后 Agent 只接受
# 用对应私钥签过名、且比自己新的版本，后台「节点管理」就能一键升级，不必再登录机器。
# 私钥不在 Server 上，Server 被人接管也推不了自己的程序；换公钥要在节点上重跑一次本脚本。
# 密钥也可通过环境变量 TZA_TOKEN 传入，避免出现在命令行。
set -eu

ENDPOINT="${TZA_ENDPOINT:-}"
TOKEN="${TZA_TOKEN:-}"
INTERFACES=""
NEXTTRACE=""
GEO_PROVIDER=""
TRACE_METHOD=""
ALLOW_HTTP=""
NO_PLUGIN=""
UNINSTALL=""
CONFIG=/etc/tza-probe/agent.json
KEYFILE=/etc/tza-probe/upgrade-key.pub
BIN=/usr/local/bin/tza-agent
UNIT=/etc/systemd/system/tza-agent.service
INITD=/etc/init.d/tza-agent
RT_UNIT=/etc/systemd/system/tza-runtime-installer.service
RT_HELPER=/usr/local/libexec/tza/tza-runtime-installer
RT_KEYS=/etc/tza-probe/runtime-cc-keys
RT_PY=/usr/local/libexec/tza/ipsentinel-install-runtime.py
RT_CONFIG=/etc/tza-probe/runtime-installer.json
RT_STATE=/var/lib/tza-agent-runtime

while [ $# -gt 0 ]; do
  case "$1" in
    --endpoint) ENDPOINT="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --interfaces) INTERFACES="$2"; shift 2 ;;
    --nexttrace) NEXTTRACE="$2"; shift 2 ;;
    --geo-provider) GEO_PROVIDER="$2"; shift 2 ;;
    --trace-method) TRACE_METHOD="$2"; shift 2 ;;
    --allow-http) ALLOW_HTTP=true; shift ;;
    --no-plugin) NO_PLUGIN=true; shift ;;
    --uninstall) UNINSTALL=true; shift ;;
    -h|--help) sed -n '2,16p' "$0" 2>/dev/null || true; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

fail() { echo "错误: $*" >&2; exit 1; }

# endpoint 会原样拼进 sudo 重新执行的命令和 agent.json，只收 URL 里正常会出现的字符，
# 引号、空白和 shell 元字符一律拒绝。
case "$ENDPOINT" in
  *[!A-Za-z0-9.:/_~%\[\]-]*) fail "endpoint 含有不允许的字符：$ENDPOINT" ;;
esac

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
  [ -n "$GEO_PROVIDER" ] && quoted="$quoted --geo-provider $(q "$GEO_PROVIDER")"
  [ -n "$TRACE_METHOD" ] && quoted="$quoted --trace-method $(q "$TRACE_METHOD")"
  [ -n "$ALLOW_HTTP" ] && quoted="$quoted --allow-http"
  [ -n "$NO_PLUGIN" ] && quoted="$quoted --no-plugin"
  [ -n "$UNINSTALL" ] && quoted="$quoted --uninstall"
  if command -v curl >/dev/null 2>&1; then
    get="curl -fsSL '$ENDPOINT/install.sh'"
  else
    get="wget -qO- '$ENDPOINT/install.sh'"
  fi
  echo "非 root，改用 sudo 重新执行 ..." >&2
  exec sudo env TZA_TOKEN="$TOKEN" TZA_ENDPOINT="$ENDPOINT" sh -c "$get | sh -s --$quoted"
fi
# 初始化系统：systemd 优先，其次 OpenRC（Alpine 等）。两条路都要能用，否则后台“接入”
# 给出的一行命令在非 systemd 主机上直接失败，那些节点只能靠手工替换二进制升级。
INIT=""
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  INIT=systemd
elif command -v rc-service >/dev/null 2>&1 && command -v rc-update >/dev/null 2>&1; then
  INIT=openrc
else
  fail "需要 systemd 或 OpenRC（rc-service / rc-update）的 Linux"
fi

service_stop() { if [ "$INIT" = systemd ]; then systemctl stop tza-agent 2>/dev/null || true; else rc-service tza-agent stop >/dev/null 2>&1 || true; fi; }
service_active() { if [ "$INIT" = systemd ]; then systemctl is-active --quiet tza-agent; else rc-service tza-agent status >/dev/null 2>&1; fi; }
service_logs() { if [ "$INIT" = systemd ]; then journalctl -u tza-agent -n 20 --no-pager; else tail -n 20 /var/log/tza-agent.log; fi; }

if [ -n "$UNINSTALL" ]; then
  if [ "$INIT" = systemd ]; then
    systemctl disable --now tza-agent 2>/dev/null || true
  else
    rc-service tza-agent stop >/dev/null 2>&1 || true
    rc-update del tza-agent default >/dev/null 2>&1 || true
  fi
  rm -f "$UNIT" "$INITD" "$BIN" "$BIN.previous"
  if [ "$INIT" = systemd ]; then
    systemctl disable --now tza-runtime-installer 2>/dev/null || true
    rm -f "$RT_UNIT" "$RT_HELPER" "$RT_PY" "$RT_KEYS" "$RT_CONFIG"
  fi
  rm -rf /etc/tza-probe /var/lib/tza-agent
  [ "$INIT" = systemd ] && systemctl daemon-reload
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
  # 升级：endpoint 从现有配置读取。旧版安装脚本写的是单行 JSON，这里不能只认
  # 缩进过的多行格式，否则不带参数的升级会在这些节点上报“缺少 --endpoint”。
  [ -n "$ENDPOINT" ] || ENDPOINT=$(sed -n 's/.*"endpoint"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$CONFIG" | head -n 1)
  [ -n "$TOKEN" ] || TOKEN=$(sed -n 's/.*"token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$CONFIG" | head -n 1)
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

json_str() { printf '%s' "$1" | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p"; }

post() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --retry 2 -H 'Content-Type: application/json' -d "$2" -o "$3" "$1"
  else
    wget -q --post-data="$2" -O "$3" "$1"
  fi
}

disable_plugin() {
  systemctl disable --now tza-runtime-installer 2>/dev/null || true
  rm -f "$RT_UNIT" "$RT_HELPER" "$RT_PY" "$RT_KEYS" "$RT_CONFIG"
  systemctl daemon-reload 2>/dev/null || true
  echo "已按 --no-plugin 关闭插件组件：本机安装器已移除，主控不能在此节点安装组件"
}

provision_plugin() {
  fetch "$ENDPOINT/dl/runtime-cc-keys" "$TMP/rt-keys" || { echo "主控未提供运行组件签名公钥，插件组件未启用" >&2; return 1; }
  RT_HAS_KEY=""
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ""|"#"*) continue ;; esac
    printf '%s\n' "$line" | grep -Eq '^[a-z0-9][a-z0-9.-]{0,31}:[A-Za-z0-9+/]{43}=$' || { echo "主控提供的运行组件公钥格式不正确" >&2; return 1; }
    RT_HAS_KEY=true
  done < "$TMP/rt-keys"
  [ -n "$RT_HAS_KEY" ] || { echo "主控尚未配置运行组件签名公钥，插件组件未启用" >&2; return 1; }
  fetch "$ENDPOINT/dl/ipsentinel-install-runtime.py" "$TMP/rt.py" || { echo "无法下载参考安装器，插件组件未启用" >&2; return 1; }
  fetch "$ENDPOINT/dl/ipsentinel-install-runtime.py.sha256" "$TMP/rt.py.sum" || { echo "无法获取参考安装器校验和，插件组件未启用" >&2; return 1; }
  PY_EXPECTED=$(cut -d' ' -f1 "$TMP/rt.py.sum")
  if command -v sha256sum >/dev/null 2>&1; then
    PY_ACTUAL=$(sha256sum "$TMP/rt.py" | cut -d' ' -f1)
  else
    PY_ACTUAL=$(shasum -a 256 "$TMP/rt.py" | cut -d' ' -f1)
  fi
  [ "$PY_ACTUAL" = "$PY_EXPECTED" ] || { echo "参考安装器摘要不符，插件组件未启用" >&2; return 1; }
  post "$ENDPOINT/api/v1/install/identity" "{\"token\":\"$TOKEN\"}" "$TMP/identity" || { echo "主控不接受本节点身份请求（可能是主控版本过旧），插件组件未启用" >&2; return 1; }
  CONTROLLER=$(json_str "$(cat "$TMP/identity")" controller_id)
  NODE_UUID=$(json_str "$(cat "$TMP/identity")" node_id)
  CC_URL=$(json_str "$(cat "$TMP/identity")" cc_url)
  PROBE_VERSION=$(json_str "$(cat "$TMP/identity")" probe_version)
  case "$CONTROLLER$NODE_UUID" in *[!0-9a-zA-Z-]*) echo "节点握手身份格式不正确" >&2; return 1 ;; esac
  [ ${#CONTROLLER} -ge 32 ] && [ ${#NODE_UUID} -ge 32 ] || { echo "节点握手身份不完整" >&2; return 1; }
  case "$CC_URL" in https://*|http://127.0.0.1*|http://localhost*) ;; *) echo "主控提供的 CC 地址不可用，插件组件未启用" >&2; return 1 ;; esac
  case "$CC_URL" in *[!A-Za-z0-9.:/_~%\[\]-]*) echo "主控提供的 CC 地址格式不正确" >&2; return 1 ;; esac
  case "$PROBE_VERSION" in *[!0-9A-Za-z.+-]*) echo "主控提供的版本号格式不正确" >&2; return 1 ;; esac
  [ -n "$PROBE_VERSION" ] || { echo "主控未提供版本号，插件组件未启用" >&2; return 1; }
  install -m 0600 "$TMP/rt-keys" "$RT_KEYS"
  install -d -m 0755 /usr/local/libexec/tza /opt/ipsentinel-runtime /var/lib/ipsentinel/state/managed /etc/ipsentinel
  install -m 0755 "$TMP/rt.py" "$RT_PY"
  install -d -m 0755 "$(dirname "$RT_HELPER")"
  systemctl stop tza-runtime-installer 2>/dev/null || true
  install -m 0755 "$BIN" "$RT_HELPER"
  umask 077
  printf '{\n  "endpoint": "%s",\n  "token": "%s",\n  "allow_http": %s,\n  "runtime_install": {\n    "cc_url": "%s",\n    "cc_keys_path": "%s",\n    "probe_version": "%s",\n    "staging_dir": "%s/staging",\n    "install_root": "/opt/ipsentinel-runtime",\n    "trusted_installer": "%s",\n    "state_dir": "/var/lib/ipsentinel/state/managed",\n    "config_path": "/etc/ipsentinel/runtime.toml",\n    "policy_path": "/etc/ipsentinel/runtime-policy.toml",\n    "unit": "ipsentinel-runtime.service",\n    "target": { "controller_id": "%s", "node_id": "%s" }\n  }\n}\n' \
    "$ENDPOINT" "$TOKEN" "${ALLOW_HTTP:-false}" "$CC_URL" "$RT_KEYS" "$PROBE_VERSION" "$RT_STATE" "$RT_PY" "$CONTROLLER" "$NODE_UUID" > "$RT_CONFIG"
  chmod 0600 "$RT_CONFIG"
  cat > "$RT_UNIT" <<'TZA_RUNTIME_SERVICE'
[Unit]
Description=TZA node-approved runtime installer
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/libexec/tza/tza-runtime-installer --runtime-installer --config /etc/tza-probe/runtime-installer.json
StateDirectory=tza-agent-runtime
StateDirectoryMode=0700
WorkingDirectory=/var/lib/tza-agent-runtime
UMask=0077
Restart=on-failure
RestartSec=10
TimeoutStopSec=20
KillMode=control-group
NoNewPrivileges=true
CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_SETUID CAP_SETGID CAP_KILL
AmbientCapabilities=CAP_SETUID CAP_SETGID CAP_KILL
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ReadWritePaths=/var/lib/tza-agent-runtime /opt/ipsentinel-runtime /var/lib/ipsentinel/state/managed
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
RestrictSUIDSGID=true
RestrictRealtime=true

[Install]
WantedBy=multi-user.target
TZA_RUNTIME_SERVICE
  chmod 0644 "$RT_UNIT"
  systemctl daemon-reload
  systemctl enable --now tza-runtime-installer >/dev/null 2>&1
  systemctl restart tza-runtime-installer
  sleep 1
  systemctl is-active --quiet tza-runtime-installer || { journalctl -u tza-runtime-installer -n 10 --no-pager >&2; echo "插件安装器未能启动，插件组件未启用" >&2; return 1; }
  echo "插件组件安装器已启用；主控可在插件 Agent 页安装组件（安装、启动分别批准）"
}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
NAME="tza-agent-linux-$ARCH"
echo "下载 $ENDPOINT/dl/$NAME ..."
fetch "$ENDPOINT/dl/$NAME" "$TMP/tza-agent" || fail "下载失败：请确认 Server 地址可达，且该 Server 提供了 $ARCH 架构的 Agent"
fetch "$ENDPOINT/dl/$NAME.sha256" "$TMP/sum" || fail "无法获取校验和"
# 校验和与程序来自同一台 Server：它能发现下载截断或中途损坏，但证明不了 Server
# 本身可信。对 Server 的信任来自 HTTPS 与你自己核对过的地址。
EXPECTED=$(cut -d' ' -f1 "$TMP/sum")
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$TMP/tza-agent" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "$TMP/tza-agent" | cut -d' ' -f1)
else
  # 下面以 root 安装下载来的二进制，宁可拒绝安装也不能跳过校验。
  fail "系统没有 sha256sum 或 shasum，无法校验完整性；请安装任一工具后重试"
fi
[ "$ACTUAL" = "$EXPECTED" ] || fail "校验和不匹配，下载不完整，请重试"
chmod 0755 "$TMP/tza-agent"
"$TMP/tza-agent" --version >/dev/null 2>&1 || fail "下载的程序无法运行"
# 升级公钥：与程序一样来自这台 Server（执行本脚本就是信任它的时刻）。没有配置时照常安装，
# 只是不能一键升级；已经钉过的公钥保留。
PINNED=""
if fetch "$ENDPOINT/dl/upgrade-key.pub" "$TMP/key" 2>/dev/null; then
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ""|"#"*) continue ;;
    esac
    printf '%s\n' "$line" | grep -Eq '^[a-z0-9][a-z0-9.-]{0,31}:[A-Za-z0-9+/]{43}=$' || fail "Server 提供的升级公钥格式不正确"
    PINNED=true
  done < "$TMP/key"
fi

# 先停旧服务再替换，避免向运行中的文件写入；一键升级下载了一半的旧版本一并清掉
service_stop
rm -f /var/lib/tza-agent/tza-agent.new /var/lib/tza-agent/tza-agent.new.sha256 /var/lib/tza-agent/tza-agent.new.sig /var/lib/tza-agent/tza-agent.new.part
[ -f "$BIN" ] && cp -p "$BIN" "$BIN.previous"
install -m 0755 "$TMP/tza-agent" "$BIN"
install -d -m 0700 /etc/tza-probe
if [ -n "$PINNED" ]; then
  install -m 0600 "$TMP/key" "$KEYFILE"
  echo "已记下 Server 的升级公钥：$KEYFILE"
elif [ ! -f "$KEYFILE" ]; then
  echo "提示: 这台 Server 没有配置升级公钥，Agent 不接受一键升级；以后升级请重跑本命令" >&2
fi

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
  case "$GEO_PROVIDER" in *[\"\\]*) fail "数据源名称不能包含引号或反斜杠" ;; esac
  case "$TRACE_METHOD" in ""|icmp|tcp|udp) ;; *) fail "探测方式只能是 icmp、tcp 或 udp" ;; esac
  install -d -m 0700 /etc/tza-probe
  umask 077
  # 用 printf 逐字段写出，密钥与路径不经过 shell 二次展开
  printf '{\n  "endpoint": "%s",\n  "token": "%s",\n  "interfaces": [%s],\n  "mount": "/",\n  "nexttrace": "%s",\n  "geo_provider": "%s",\n  "trace_method": "%s",\n  "allow_http": %s\n}\n' \
    "$ENDPOINT" "$TOKEN" "$IFACES" "$NEXTTRACE" "$GEO_PROVIDER" "$TRACE_METHOD" "${ALLOW_HTTP:-false}" > "$CONFIG"
  chmod 0600 "$CONFIG"
fi

if [ "$INIT" = systemd ]; then
  install -d -m 0755 "$(dirname "$UNIT")"
  cat > "$UNIT" <<'TZA_AGENT_SERVICE'
[Unit]
Description=TZA Probe Agent
After=network-online.target
Wants=network-online.target

[Service]
# 一键升级：Agent 把新版本下载到 StateDirectory（沙箱里唯一可写的地方）后以状态 3 退出，
# 下次启动前由带 + 前缀（不受沙箱限制）的 ExecStartPre 按 /etc/tza-probe/upgrade-key.pub
# 再验一遍签名后换上；没有待装版本时什么也不做。
ExecStartPre=-+/usr/local/bin/tza-agent --config /etc/tza-probe/agent.json --apply-update
ExecStart=/usr/local/bin/tza-agent --config /etc/tza-probe/agent.json --self-update
StateDirectory=tza-agent
StateDirectoryMode=0700
Restart=on-failure
RestartSec=5
SuccessExitStatus=3
RestartForceExitStatus=3
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
else
  # OpenRC 用 supervise-daemon 托管，等价于 systemd 的 Restart=on-failure；
  # 二进制统一放 /usr/local/bin，旧布局（/opt/tza-probe/tza-agent）留档后让位。
  cat > "$INITD" <<'TZA_AGENT_INIT'
#!/sbin/openrc-run
name="TZA Probe Agent"
command="/usr/local/bin/tza-agent"
command_args="--config /etc/tza-probe/agent.json --self-update"
supervisor="supervise-daemon"
respawn_delay=5
respawn_max=0
respawn_period=60
pidfile="/run/tza-agent.pid"
output_log="/var/log/tza-agent.log"
error_log="/var/log/tza-agent.log"
depend() { need net; after firewall; }
# 一键升级：没有沙箱时 Agent 自己就能换上新版本；这里补上「下载后、换之前被停掉」的情形。
start_pre() { /usr/local/bin/tza-agent --config /etc/tza-probe/agent.json --apply-update || true; }
TZA_AGENT_INIT
  chmod 0755 "$INITD"
  rc-update add tza-agent default >/dev/null 2>&1 || true
  if [ -f /opt/tza-probe/tza-agent ]; then
    mv /opt/tza-probe/tza-agent "/opt/tza-probe/tza-agent.previous-$(date +%Y%m%d%H%M)" 2>/dev/null || true
  fi
  rc-service tza-agent restart >/dev/null 2>&1 || rc-service tza-agent start >/dev/null 2>&1
fi
sleep 2
if service_active; then
  if [ -n "$UPGRADE" ]; then echo "Agent 已升级并重启"; else echo "Agent 已安装并启动，稍后后台节点会显示在线"; fi
  if [ -z "$NEXTTRACE" ] && [ -z "$UPGRADE" ]; then
    echo "提示: 三网回程使用内置 NTrace-core（NextTrace 同源），无需额外安装；如需改用外部二进制，在 $CONFIG 填写 nexttrace 路径并重启服务"
  fi
else
  echo "Agent 未能启动，最近日志：" >&2
  service_logs >&2 || true
  exit 1
fi

# 插件组件（IPSentinel 等）：默认随安装一起启用；需要 root 写入安装根与专用服务，
# 因此只在 systemd 节点配置。OpenRC 节点与显式 --no-plugin 的节点跳过。
if [ "$INIT" != systemd ]; then
  echo "提示: 当前系统不是 systemd，插件组件暂不支持；普通探测功能不受影响"
elif [ -n "$NO_PLUGIN" ]; then
  disable_plugin
elif [ -z "$TOKEN" ]; then
  echo "提示: 未提供节点密钥，跳过插件组件配置"
else
  if ! provision_plugin; then
    echo "提示: 修复上述问题后重新运行本命令即可启用插件组件" >&2
  fi
fi
