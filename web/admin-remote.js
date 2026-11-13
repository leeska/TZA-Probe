import { api, esc, modal, guard, send, toast } from "./api.js";
import { date } from "./format.js";
import { content, state } from "./admin-state.js";

// 远程管理：经管理通道在节点上执行命令、打开终端。只有用 --manage 安装（或重新执行安装
// 命令加 --manage）的 Agent 才会连上通道；本机 manage.json 的 disabled_capabilities 可以单独关掉 shell。
export async function remotePage(nodesReady) {
  const [, list] = await Promise.all([nodesReady, api("/admin/remote")]);
  const connected = list.filter((n) => n.connected);
  const rows = list.map((n) => {
    const shell = n.connected && n.capabilities.includes("shell");
    const status = n.connected ? `<span class="pill good">已连接</span><small>${esc(date(n.since))} 起 · ${esc(n.capabilities.join("、") || "无能力")}</small>` : '<span class="pill">未连接</span><small>Agent 未开启管理能力或离线</small>';
    return `<tr><td data-label="节点">${esc(n.name)}</td><td data-label="管理通道">${status}</td><td data-label="操作"><div class="row-actions"><button data-terminal="${esc(n.id)}" ${shell ? "" : "disabled"}>终端</button><button data-exec="${esc(n.id)}" ${shell ? "" : "disabled"}>执行命令</button><button data-audit-node="${esc(n.id)}">操作记录</button></div></td></tr>`;
  }).join("");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">REMOTE</div><h1>远程管理<span>${connected.length}</span></h1><p>经管理通道在节点上执行命令、打开终端。每次操作都记入审计日志。</p></div></section>
  <section data-part="panel" class="panel"><div class="table-scroll"><table class="stack"><thead><tr><th>节点</th><th>管理通道</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="3" class="empty-cell">还没有节点。</td></tr>'}</tbody></table></div>
  <p class="hint remote-hint">节点要开启管理能力：在节点上重新执行接入命令并加 <code>--manage</code>（会换成不带沙箱的服务单元，Agent 以 root 运行）。在节点的 <code>/etc/tza-probe/manage.json</code> 里写 <code>"disabled_capabilities": ["shell"]</code> 并重启 Agent，可以在本机关掉命令与终端。</p></section>`;
  const name = (id) => list.find((n) => n.id === id)?.name || id;
  content().querySelectorAll("[data-exec]").forEach((b) => (b.onclick = () => execDialog(b.dataset.exec, name(b.dataset.exec))));
  content().querySelectorAll("[data-terminal]").forEach((b) => (b.onclick = guard(() => openTerminal(b.dataset.terminal, name(b.dataset.terminal)))));
  content().querySelectorAll("[data-audit-node]").forEach((b) => (b.onclick = guard(async () => {
    state.auditNode = b.dataset.auditNode;
    state.tab = "audit";
    await auditPage(Promise.resolve());
  })));
}

function execDialog(node, name) {
  const d = modal(`在 ${name} 上执行命令`, `<form id="exec-form"><label>命令<textarea name="command" rows="4" required maxlength="16384" spellcheck="false" placeholder="例如：df -h; systemctl status nginx --no-pager"></textarea></label>
    <label>超时（秒）<input name="timeout" type="number" min="1" max="3600" value="60"></label>
    <p class="hint">以 Agent 的身份（root）经 /bin/sh -c 执行；输出最多保留 1 MB。命令原文会写入审计日志，不要在命令里写密码。</p>
    <pre class="exec-output" hidden></pre>
    <div class="form-footer"><button type="submit" class="primary">执行</button></div></form>`);
  d.classList.add("wide-dialog");
  d.addEventListener("close", () => d.classList.remove("wide-dialog"), { once: true });
  const form = d.querySelector("form");
  const out = d.querySelector(".exec-output");
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const data = new FormData(form);
    out.hidden = false;
    out.textContent = "执行中…";
    const r = await send("/admin/remote/exec", "POST", { node_id: node, command: data.get("command"), timeout_seconds: Number(data.get("timeout")) });
    const tail = [`退出码 ${r.exit_code}`, `${(r.duration_ms / 1000).toFixed(1)} 秒`, r.truncated ? "输出已截断（1 MB）" : "", r.error || ""].filter(Boolean).join(" · ");
    out.textContent = (r.output || "（无输出）") + "\n— " + tail;
  });
}

// xterm.js 按需加载（约 290 KB，只有打开终端时才下载）。
let xtermReady;
function loadXterm() {
  xtermReady ||= new Promise((resolve, reject) => {
    const css = document.createElement("link");
    css.rel = "stylesheet";
    css.href = "/vendor-xterm.css";
    document.head.append(css);
    const load = (src) => new Promise((ok, fail) => {
      const s = document.createElement("script");
      s.src = src;
      s.onload = ok;
      s.onerror = () => fail(new Error("终端组件加载失败"));
      document.head.append(s);
    });
    load("/vendor-xterm.js").then(() => load("/vendor-xterm-fit.js")).then(() => resolve(window), reject);
  });
  return xtermReady;
}

// 面板的 CSP 不允许内联 <style>，xterm 却把配色写进自己插入的 <style>：把它的内容
// 转成可构造样式表（不受 style-src 限制）挂上，并跟着它的变化更新。
function adoptXtermStyles(root) {
  const sheets = new Map();
  const sync = () => {
    for (const el of root.querySelectorAll("style")) {
      let sheet = sheets.get(el);
      if (!sheet) {
        sheet = new CSSStyleSheet();
        sheets.set(el, sheet);
        document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
      }
      sheet.replaceSync(el.textContent);
    }
  };
  const observer = new MutationObserver(sync);
  observer.observe(root, { subtree: true, childList: true, characterData: true });
  sync();
  return () => {
    observer.disconnect();
    document.adoptedStyleSheets = document.adoptedStyleSheets.filter((s) => ![...sheets.values()].includes(s));
  };
}

function openTerminal(node, name) {
  return openStream(`/api/v1/admin/remote/terminal?node=${encodeURIComponent(node)}`, `终端 · ${name}`, { interactive: true });
}

// openStream 打开一个经管理通道转接的 WebSocket 流并用 xterm 显示：终端（interactive，键入和
// 窗口大小发给节点）或只出不进的输出（日志、拉镜像、Compose 操作）。path 不含 cols/rows。
export async function openStream(path, title, { interactive = false, onExit } = {}) {
  const w = await loadXterm();
  const d = modal(title, `<div class="terminal-box"><div class="terminal-host"></div></div><p class="hint terminal-status">连接中…</p>`);
  d.classList.add("terminal-dialog");
  const host = d.querySelector(".terminal-host");
  const status = d.querySelector(".terminal-status");
  const term = new w.Terminal({ cursorBlink: interactive, disableStdin: !interactive, convertEol: !interactive, fontSize: 13, fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace", scrollback: 10000, theme: { background: "#0b0d10" } });
  const fit = new w.FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(host);
  const unstyle = adoptXtermStyles(host);
  fit.fit();
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(`${proto}//${location.host}${path}${path.includes("?") ? "&" : "?"}cols=${term.cols}&rows=${term.rows}`);
  ws.binaryType = "arraybuffer";
  let ended = false;
  ws.onopen = () => {
    status.textContent = interactive ? "已连接。关闭窗口即断开；本次会话写入审计日志。" : "进行中…关闭窗口即停止；本次操作写入审计日志。";
    if (interactive) term.focus();
  };
  ws.onmessage = (e) => {
    if (typeof e.data === "string") {
      const m = JSON.parse(e.data);
      ended = true;
      status.textContent = interactive ? `会话已结束（退出码 ${m.exit}）${m.error ? " · " + m.error : ""}` : m.exit === 0 ? "完成" : `失败（退出码 ${m.exit}）${m.error ? " · " + m.error : ""}`;
      onExit?.(m);
      return;
    }
    term.write(new Uint8Array(e.data));
  };
  ws.onclose = () => { if (!ended) status.textContent = "连接已断开"; };
  ws.onerror = () => toast("连接失败：节点的管理通道可能未连接");
  const sendJSON = (v) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(v));
  if (interactive) term.onData((data) => sendJSON({ i: data }));
  const resize = () => { fit.fit(); if (interactive) sendJSON({ resize: { cols: term.cols, rows: term.rows } }); };
  const observer = new ResizeObserver(resize);
  observer.observe(host);
  // 弹窗是同一个 <dialog>：从另一个弹窗（如 compose 编辑）直接打开时，那次关闭排队的 close
  // 事件会晚一步到达，此时弹窗仍开着，要忽略。
  const onClose = () => {
    if (d.open) return;
    d.removeEventListener("close", onClose);
    observer.disconnect();
    ws.close();
    term.dispose();
    unstyle();
    d.classList.remove("terminal-dialog");
  };
  d.addEventListener("close", onClose);
}

// 审计日志：后台的每次写操作、登录，以及命令与终端会话。
export async function auditPage(nodesReady) {
  await nodesReady;
  const node = state.auditNode || "";
  const entries = await api(`/admin/audit?limit=200${node ? "&node=" + encodeURIComponent(node) : ""}`);
  const nodeName = (id) => state.nodes.find((n) => n.id === id)?.name || id;
  const rows = entries.map((e) => `<tr><td data-label="时间">${esc(date(e.at))}</td><td data-label="操作者">${esc(e.actor)}<small>${esc(e.source)}</small></td><td data-label="操作"><code>${esc(actionName(e.action))}</code>${e.node_id ? `<small>${esc(nodeName(e.node_id))}</small>` : ""}</td><td data-label="结果">${esc(String(e.status || "—"))}</td><td data-label="详情">${e.detail ? `<pre class="audit-detail">${esc(e.detail)}</pre>` : "—"}</td></tr>`).join("");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">AUDIT</div><h1>审计日志</h1><p>后台的每次写操作、登录，以及远程命令与终端会话。保留 365 天。</p></div>${node ? `<div class="heading-actions"><button id="audit-all">查看全部</button></div>` : ""}</section>
  <section data-part="panel" class="panel">${node ? `<div class="panel-head"><h3>节点：${esc(nodeName(node))}</h3></div>` : ""}<div class="table-scroll"><table class="stack"><thead><tr><th>时间</th><th>操作者</th><th>操作</th><th>结果</th><th>详情</th></tr></thead><tbody>${rows || '<tr><td colspan="5" class="empty-cell">还没有记录。</td></tr>'}</tbody></table></div></section>`;
  const all = content().querySelector("#audit-all");
  if (all) all.onclick = guard(async () => { state.auditNode = ""; await auditPage(Promise.resolve()); });
}

const ACTIONS = { login: "登录", "login.failed": "登录失败", "shell.exec": "执行命令", "shell.terminal": "打开终端", "shell.terminal.closed": "终端结束", "docker.exec": "进入容器终端", "docker.exec.closed": "容器终端结束", "docker.logs": "查看容器日志", "docker.logs.closed": "日志查看结束", "docker.image.pull": "拉取镜像", "docker.image.pull.closed": "拉取镜像结束", "docker.image.remove": "删除镜像", "docker.container.start": "启动容器", "docker.container.stop": "停止容器", "docker.container.restart": "重启容器", "docker.container.remove": "删除容器", "docker.compose.write": "保存 compose 文件", "docker.compose.up": "Compose 部署", "docker.compose.down": "Compose 停止并移除", "docker.compose.pull": "Compose 拉取镜像", "docker.compose.restart": "Compose 重启", "docker.compose.delete": "删除 Compose 项目", "certs.download": "下载证书文件" };
const actionName = (a) => ACTIONS[a] || a;
