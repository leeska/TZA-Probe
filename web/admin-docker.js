// 后台「Docker」：开启了管理通道的节点上的容器、镜像和 Compose 项目。节点上的 Agent 调本机
// Docker Engine API（只放行列表、启停、删除这些操作），Compose 用 docker compose 命令。
// 日志、拉镜像、Compose 操作和容器终端都是 WebSocket 流，用和远程终端相同的窗口显示。
import { api, send, esc, toast, modal, guard } from "./api.js";
import { bytes } from "./format.js";
import { content, confirmAction, moreMenu, wireMoreMenus, render, state } from "./admin-state.js";
import { openStream } from "./admin-remote.js";

const STATE = { running: ["运行中", "good"], exited: ["已停止", ""], created: ["已创建", ""], paused: ["已暂停", ""], restarting: ["重启中", "warn"], dead: ["异常", "bad"], removing: ["删除中", ""] };
const stream = (params) => `/api/v1/admin/docker/stream?${new URLSearchParams(params)}`;
const short = (id) => (id || "").replace(/^sha256:/, "").slice(0, 12);
const ago = (unix) => (unix ? new Date(unix * 1000).toLocaleString("zh-CN") : "");

function ports(c) {
  const list = (c.Ports || []).filter((p) => p.PublicPort).map((p) => `${p.IP && p.IP !== "0.0.0.0" && p.IP !== "::" ? p.IP + ":" : ""}${p.PublicPort}→${p.PrivatePort}/${p.Type}`);
  return [...new Set(list)].join(" ") || "—";
}

export async function dockerPage(nodesReady) {
  const [, nodes] = await Promise.all([nodesReady, api("/admin/docker")]);
  const capable = nodes.filter((n) => n.capable && n.connected);
  if (!capable.some((n) => n.id === state.dockerNode)) state.dockerNode = capable[0]?.id || "";
  const node = state.dockerNode;
  const options = nodes.map((n) => `<option value="${esc(n.id)}" ${n.id === node ? "selected" : ""} ${n.capable && n.connected ? "" : "disabled"}>${esc(n.name)}${n.capable && n.connected ? "" : n.connected ? "（未开启 Docker 管理）" : "（管理通道未连接）"}</option>`).join("");
  const heading = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">DOCKER</div><h1>Docker</h1><p>节点要用 --manage 接入。启停、删除、拉镜像、Compose 操作和容器终端都写入审计日志。</p></div><div class="heading-actions"><label class="inline-select">节点<select id="docker-node">${options || "<option>没有节点</option>"}</select></label><button id="docker-refresh">↻ 刷新</button></div></section>`;
  if (!node) {
    content().innerHTML = `${heading}<section data-part="panel" class="panel"><p class="empty-cell">还没有开启 Docker 管理的节点：在装了 Docker 的节点上重跑接入命令并加 --manage。</p></section>`;
    wireHeading();
    return;
  }
  content().innerHTML = `${heading}<section data-part="panel" class="panel"><p role="status" class="empty-cell">正在读取…</p></section>`;
  wireHeading();
  let ov;
  try {
    ov = await api(`/admin/docker/nodes/${encodeURIComponent(node)}`);
  } catch (error) {
    content().querySelector("[data-part=panel]").innerHTML = `<p class="empty-cell">${esc(error.message)}</p>`;
    return;
  }
  if (state.dockerNode !== node) return;
  const containers = (ov.containers || []).sort((a, b) => (a.Names?.[0] || "").localeCompare(b.Names?.[0] || ""));
  const images = (ov.images || []).sort((a, b) => b.Created - a.Created);
  const projects = ov.compose?.projects || [];
  const version = ov.version ? `Docker ${esc(ov.version.Version)}${ov.compose?.compose ? ` · Compose ${esc(ov.compose.compose)}` : " · 没有 compose 插件"}` : "";
  const containerRows = containers.map((c) => {
    const name = (c.Names?.[0] || c.Id).replace(/^\//, "");
    const [label, tone] = STATE[c.State] || [c.State, ""];
    const project = c.Labels?.["com.docker.compose.project"];
    const running = c.State === "running";
    return `<tr><td data-label="容器">${esc(name)}<small>${esc(short(c.Id))}${project ? ` · ${esc(project)}` : ""}</small></td><td data-label="镜像">${esc(c.Image)}</td><td data-label="状态"><span class="pill ${tone}">${label}</span><small>${esc(c.Status)}</small></td><td data-label="端口">${esc(ports(c))}</td><td data-label="操作"><div class="row-actions"><button data-logs="${esc(c.Id)}" data-name="${esc(name)}">日志</button>${running ? `<button data-exec="${esc(c.Id)}" data-name="${esc(name)}">终端</button>` : `<button data-act="start" data-id="${esc(c.Id)}" data-name="${esc(name)}">启动</button>`}${moreMenu(`${running ? `<button data-act="stop" data-id="${esc(c.Id)}" data-name="${esc(name)}">停止</button>` : ""}<button data-act="restart" data-id="${esc(c.Id)}" data-name="${esc(name)}">重启</button><button class="menu-danger" data-remove="${esc(c.Id)}" data-name="${esc(name)}">删除</button>`)}</div></td></tr>`;
  }).join("");
  const imageRows = images.map((i) => {
    const tags = (i.RepoTags || []).filter((t) => t !== "<none>:<none>");
    return `<tr><td data-label="镜像">${tags.length ? tags.map(esc).join("<br>") : '<span class="muted">未标记</span>'}<small>${esc(short(i.Id))}</small></td><td data-label="大小">${bytes(i.Size)}</td><td data-label="创建">${esc(ago(i.Created))}</td><td data-label="操作"><div class="row-actions"><button class="danger-text" data-rmi="${esc(i.Id)}" data-name="${esc(tags[0] || short(i.Id))}">删除</button></div></td></tr>`;
  }).join("");
  const projectRows = projects.map((p) => `<tr><td data-label="项目">${esc(p.name)}<small>${p.managed ? "面板创建" : "节点上已有"}</small></td><td data-label="目录"><code>${esc(p.dir || "—")}</code></td><td data-label="容器">${p.running} / ${p.containers} 运行</td><td data-label="操作"><div class="row-actions"><button data-edit-project="${esc(p.name)}">编辑</button><button data-compose="up" data-project="${esc(p.name)}">部署</button>${moreMenu(`<button data-compose="pull" data-project="${esc(p.name)}">拉取镜像</button><button data-compose="restart" data-project="${esc(p.name)}">重启</button><button class="menu-danger" data-compose="down" data-project="${esc(p.name)}">停止并移除容器</button><button class="menu-danger" data-delete-project="${esc(p.name)}" data-managed="${p.managed ? 1 : ""}">删除项目</button>`)}</div></td></tr>`).join("");
  content().querySelector("[data-part=panel]").outerHTML = `${ov.error ? `<section class="panel docker-error"><p class="panel-body">${esc(ov.error)}</p></section>` : ""}
  <section data-part="panel" class="panel"><div class="panel-head"><h3>容器 <span class="muted">${containers.length}</span></h3><small>${version}</small></div><div class="table-scroll"><table class="stack"><thead><tr><th>容器</th><th>镜像</th><th>状态</th><th>端口</th><th>操作</th></tr></thead><tbody>${containerRows || '<tr><td colspan="5" class="empty-cell">没有容器。</td></tr>'}</tbody></table></div></section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>Compose 项目 <span class="muted">${projects.length}</span></h3><button id="new-project" class="primary">＋ 新建项目</button></div><div class="table-scroll"><table class="stack"><thead><tr><th>项目</th><th>目录</th><th>容器</th><th>操作</th></tr></thead><tbody>${projectRows || '<tr><td colspan="4" class="empty-cell">没有 Compose 项目。面板新建的项目放在节点的 /opt/tza-compose/&lt;项目名&gt;/。</td></tr>'}</tbody></table></div></section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>镜像 <span class="muted">${images.length}</span></h3><button id="pull-image">拉取镜像</button></div><div class="table-scroll"><table class="stack"><thead><tr><th>镜像</th><th>大小</th><th>创建</th><th>操作</th></tr></thead><tbody>${imageRows || '<tr><td colspan="4" class="empty-cell">没有镜像。</td></tr>'}</tbody></table></div></section>`;
  wireMoreMenus(content());
  const nodeName = nodes.find((n) => n.id === node)?.name || node;
  const base = `/admin/docker/nodes/${encodeURIComponent(node)}`;
  const q = (sel, fn) => content().querySelectorAll(sel).forEach((b) => (b.onclick = () => fn(b)));
  q("[data-logs]", (b) => openStream(stream({ node, kind: "logs", container: b.dataset.logs, tail: 500, follow: 1 }), `日志 · ${b.dataset.name}`));
  q("[data-exec]", (b) => openStream(stream({ node, kind: "exec", container: b.dataset.exec }), `容器终端 · ${b.dataset.name}`, { interactive: true }));
  q("[data-act]", (b) => guard(async () => {
    const labels = { start: "启动", stop: "停止", restart: "重启" };
    toast(`正在${labels[b.dataset.act]}「${b.dataset.name}」…`);
    await send(`${base}/containers/${b.dataset.id}/${b.dataset.act}`, "POST");
    toast(`已${labels[b.dataset.act]}「${b.dataset.name}」`);
    await render();
  })());
  q("[data-remove]", (b) => confirmAction(`删除容器 · ${b.dataset.name}`, `强制删除「${b.dataset.name}」（运行中的会先停止）。容器里没有挂载出去的数据会一起丢失，此操作无法撤销。`, async () => { await send(`${base}/containers/${b.dataset.remove}`, "DELETE"); await render(); }, { label: "删除" }));
  q("[data-rmi]", (b) => confirmAction(`删除镜像 · ${b.dataset.name}`, `删除镜像「${b.dataset.name}」。还有容器在用时 Docker 会拒绝。`, async () => { await send(`${base}/images/${b.dataset.rmi}`, "DELETE"); await render(); }, { label: "删除" }));
  q("[data-edit-project]", (b) => guard(() => projectEditor(node, nodeName, b.dataset.editProject))());
  q("[data-compose]", (b) => {
    const action = b.dataset.compose, project = b.dataset.project;
    const titles = { up: "部署", down: "停止并移除容器", pull: "拉取镜像", restart: "重启" };
    const run = () => openStream(stream({ node, kind: "compose", project, action }), `${titles[action]} · ${project}`, { onExit: () => render() });
    if (action === "down") confirmAction(`停止并移除 · ${project}`, `对「${project}」执行 docker compose down：停止并删除这个项目的容器和网络（数据卷保留）。`, async () => run(), { label: "执行" });
    else run();
  });
  q("[data-delete-project]", (b) => deleteProject(node, b.dataset.deleteProject, !!b.dataset.managed));
  content().querySelector("#new-project").onclick = () => projectEditor(node, nodeName, "");
  content().querySelector("#pull-image").onclick = () => pullForm(node);
}

function wireHeading() {
  const select = content().querySelector("#docker-node");
  if (select) select.onchange = () => { state.dockerNode = select.value; render(); };
  const refresh = content().querySelector("#docker-refresh");
  if (refresh) refresh.onclick = () => render();
}

function deleteProject(node, project, managed) {
  const d = modal(`删除项目 · ${project}`, `<form id="delete-project"><p>${managed
    ? `先对「${esc(project)}」执行 docker compose down（停止并删除容器和网络），再删除节点上的项目目录 <code>/opt/tza-compose/${esc(project)}</code>，compose 文件一起删掉。此操作无法撤销。`
    : `「${esc(project)}」不是在面板里建的：只执行 docker compose down 停止并删除容器和网络，项目文件留在节点上。`}</p><label class="checkbox"><input type="checkbox" name="volumes">同时删除这个项目的数据卷（数据库等数据会丢失）</label><div class="form-footer"><button type="submit" class="primary danger">删除</button></div></form>`);
  const form = d.querySelector("#delete-project");
  form.onsubmit = (e) => {
    e.preventDefault();
    const volumes = form.elements.namedItem("volumes").checked;
    d.close();
    openStream(stream({ node, kind: "compose", project, action: "delete", ...(volumes ? { volumes: 1 } : {}) }), `删除 · ${project}`, { onExit: () => render() });
  };
}

function pullForm(node) {
  const d = modal("拉取镜像", `<form id="pull-form"><label>镜像<input name="image" required maxlength="255" placeholder="如 nginx、nginx:1.27 或 ghcr.io/owner/app:tag" autocapitalize="off" spellcheck="false"></label><p class="hint">不写标签就是 latest。私有仓库需要先在节点上 docker login。</p><div class="form-footer"><button type="submit" class="primary">拉取</button></div></form>`);
  const form = d.querySelector("#pull-form");
  form.onsubmit = (e) => {
    e.preventDefault();
    const image = form.elements.namedItem("image").value.trim();
    d.close();
    openStream(stream({ node, kind: "pull", image }), `拉取 · ${image}`, { onExit: () => render() });
  };
}

const TEMPLATE = `services:
  app:
    image: nginx:latest
    restart: unless-stopped
    ports:
      - "8080:80"
`;

async function projectEditor(node, nodeName, project) {
  let file = { content: TEMPLATE, path: "" };
  if (project) file = await api(`/admin/docker/nodes/${encodeURIComponent(node)}/compose/${encodeURIComponent(project)}`);
  const d = modal(project ? `编辑 · ${project}` : `新建 Compose 项目 · ${nodeName}`, `<form id="compose-form">${project ? `<p class="hint">文件：<code>${esc(file.path)}</code>。保存前节点会用 docker compose config 检查，旧文件留在同目录的 .tza-bak。</p>` : `<label>项目名<input name="project" required maxlength="63" pattern="[a-z0-9][a-z0-9_-]*" placeholder="小写字母、数字、- 和 _" autocapitalize="off" spellcheck="false"></label><p class="hint">保存到节点的 /opt/tza-compose/&lt;项目名&gt;/compose.yaml，保存前用 docker compose config 检查。</p>`}<label>compose.yaml<textarea name="content" rows="18" spellcheck="false" class="code-input" required>${esc(file.content)}</textarea></label><p class="hint compose-error" hidden></p><div class="form-footer"><label class="checkbox"><input type="checkbox" name="deploy" checked>保存后立即部署（up -d）</label><button type="submit" class="primary">保存</button></div></form>`);
  const form = d.querySelector("#compose-form");
  const errorBox = form.querySelector(".compose-error");
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const name = project || form.elements.namedItem("project").value.trim();
    errorBox.hidden = true;
    try {
      await send(`/admin/docker/nodes/${encodeURIComponent(node)}/compose/${encodeURIComponent(name)}`, "PUT", { content: form.elements.namedItem("content").value });
    } catch (error) {
      errorBox.hidden = false;
      errorBox.innerHTML = `${esc(error.message)}${error.data?.output ? `<pre class="audit-detail">${esc(error.data.output)}</pre>` : ""}`;
      return;
    }
    const deploy = form.elements.namedItem("deploy").checked;
    d.close();
    toast("已保存");
    if (deploy) openStream(stream({ node, kind: "compose", project: name, action: "up" }), `部署 · ${name}`, { onExit: () => render() });
    else await render();
  });
}
