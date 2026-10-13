import { esc, modal, guard, toast } from "./api.js";

// Builds the one-line installation command. The script itself is static and
// served by this Server at /install.sh; the endpoint and node key travel as
// arguments so nothing secret is ever cached or logged as a URL.
export function installCommand(endpoint, token) {
  return `curl -fsSL ${endpoint}/install.sh | sh -s -- --endpoint ${endpoint} --token ${token}`;
}

// name 写进标题：同时给几台机器接入时，要分得清这条命令是哪一台的。
export function showToken(token, name = "") {
  const endpoint = location.origin;
  const config = {
    endpoint,
    token,
    interfaces: [],
    mount: "/",
    nexttrace: "",
    geo_provider: "",
    trace_method: "",
    allow_http: location.protocol === "http:",
  };
  const command = installCommand(endpoint, token);
  const d = modal(
    name ? `节点接入 · ${name}` : "节点接入",
    `<p>密钥仅在此处显示。在 Agent 主机（Linux + systemd，x86_64 / arm64）以 root（或有 sudo 权限的用户）执行下面这一条命令，会自动下载对应架构的 Agent、写入配置并启动服务：</p>
    ${endpointWarning()}
    <div class="command-block"><pre id="install-command">${esc(command)}</pre><button type="button" id="copy-command" class="primary">复制命令</button></div>
    <p class="hint">命令包含节点密钥，只在需要接入的主机上执行。以后不带 <code>--token</code> 再执行一次即为升级 Agent。</p>
    <details class="more"><summary>可选参数</summary><p class="hint">默认统计全部非 loopback 网卡，三网回程使用内置的 NTrace-core（与 NextTrace 同源，无需另行安装）。需要时在命令末尾追加：<code>--interfaces eth0</code>（只统计指定网卡）、<code>--nexttrace /usr/local/bin/nexttrace</code>（改用外部二进制）、<code>--geo-provider disable-geoip</code>（换 / 关闭 ASN 数据源，默认 <code>IPAPI.com</code>）、<code>--trace-method tcp</code>（探测方式，默认 ICMP）。</p></details>
    <details><summary>手动安装 / 查看 agent.json</summary>
      <p class="hint">不方便让主机联网下载时，可自行编译 Agent 并使用以下配置，安装步骤见 docs/DEPLOYMENT.md。</p>
      <pre id="agent-config">${esc(JSON.stringify(config, null, 2))}</pre>
      <div class="catalog-toolbar"><button type="button" id="copy-config">复制配置</button><button type="button" id="download-config">下载 agent.json</button></div>
    </details>`,
  );
  d.querySelector("#copy-command").onclick = guard(async () => {
    await navigator.clipboard.writeText(command);
    toast("安装命令已复制");
  });
  d.querySelector("#copy-config").onclick = guard(async () => {
    await navigator.clipboard.writeText(JSON.stringify(config, null, 2));
    toast("配置已复制");
  });
  d.querySelector("#download-config").onclick = () =>
    download("agent.json", JSON.stringify(config, null, 2), "application/json");
}

// 命令里的 --endpoint 就是当前打开后台用的地址。用 localhost / 127.0.0.1 打开时，别的机器按这个
// 地址根本连不回面板；用明文 HTTP 时密钥会明文经过网络。以前这句提醒埋在一长段说明的末尾，
// 现在单独放在命令上方。
function endpointWarning() {
  const host = location.hostname;
  if (host === "localhost" || host === "::1" || host === "[::1]" || host.startsWith("127."))
    return `<div class="callout danger"><strong>其他机器连不上这个地址</strong><span>命令里的面板地址是 ${esc(location.origin)}，只有本机能访问。请用 Agent 主机能访问的域名或 IP 打开后台，再重新生成命令。</span></div>`;
  if (location.protocol === "http:")
    return `<div class="callout warn"><strong>当前是明文 HTTP</strong><span>节点密钥会明文经过网络，命令只适用于可信私网。生产环境请从 HTTPS 地址打开后台再生成。</span></div>`;
  return "";
}

function download(filename, content, type) {
  const url = URL.createObjectURL(new Blob([content], { type }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
