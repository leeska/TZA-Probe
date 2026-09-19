import { esc, modal, guard, toast } from "./api.js";

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

export function showToken(token) {
  const config = {
    endpoint: location.origin,
    token,
    interfaces: [],
    mount: "/",
    nexttrace: "",
    allow_http: location.protocol === "http:",
  };
  const d = modal(
    "节点接入配置",
    `<p>密钥仅在此处显示。将配置保存到 Agent 主机，文件权限设为 600。</p><pre id="agent-config">${esc(JSON.stringify(config, null, 2))}</pre><p class="hint">填写需要统计的公网网卡；三网回程需安装 NextTrace 并填写其绝对路径。生产环境使用 HTTPS。</p><p class="hint">Linux systemd：将对应架构的 tza-agent 与安装脚本放在同一目录，先按需修改脚本中的网卡和 NextTrace 路径，再执行下方命令。脚本包含密钥，请私密保存，安装后删除。</p><pre>sudo sh install-agent.sh</pre><div class="form-footer"><button id="download-install">下载安装脚本</button><button id="copy-config">复制配置</button><button id="download-config" class="primary">下载 agent.json</button></div>`,
  );
  d.querySelector("#download-install").onclick = () =>
    download("install-agent.sh", installationScript(config), "text/plain");
  d.querySelector("#copy-config").onclick = guard(async () => {
    await navigator.clipboard.writeText(JSON.stringify(config, null, 2));
    toast("配置已复制");
  });
  d.querySelector("#download-config").onclick = () =>
    download("agent.json", JSON.stringify(config, null, 2), "application/json");
}

function download(filename, content, type) {
  const url = URL.createObjectURL(new Blob([content], { type }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
