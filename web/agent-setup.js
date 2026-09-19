// Generates an offline installation script. No downloads or remote shell execution.
export function installationScript(config) {
  const json = JSON.stringify(config, null, 2);
  return `#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo "请使用 sudo sh install-agent.sh"; exit 1; }
command -v systemctl >/dev/null || { echo "需要使用 systemd 的 Linux"; exit 1; }
[ -f ./tza-agent ] || { echo "请将对应架构的 tza-agent 放在当前目录"; exit 1; }
install -m 0755 ./tza-agent /usr/local/bin/tza-agent
install -d -m 0700 /etc/tza-probe
umask 077
cat > /etc/tza-probe/agent.json <<'TZA_AGENT_CONFIG'
${json}
TZA_AGENT_CONFIG
chmod 600 /etc/tza-probe/agent.json
cat > /etc/systemd/system/tza-agent.service <<'TZA_AGENT_SERVICE'
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
chmod 644 /etc/systemd/system/tza-agent.service
systemctl daemon-reload
systemctl enable tza-agent
systemctl restart tza-agent
systemctl --no-pager status tza-agent
`;
}
