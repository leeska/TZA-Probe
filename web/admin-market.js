import { api, send, toast, guard, modal, esc } from "./api.js";
import { bytes } from "./format.js";
import { ANIME } from "./skins.js";
import { state, content, confirmAction } from "./admin-state.js";
// TZA-CC 市场的共用部分：「主题」页（主题包）和「插件」页各带一块市场；角色包是二次元主题的附加内容，
// 它的市场挂在二次元的设置菜单里（characterMarket），不是主题市场的一个分类，TZA-CC 账号在
// 「面板设置 → 账号与安全」。浏览器只请求本服务器的 /admin/market/*，目录、预览图与安装包都由服务器
// 代为获取（预览图走 /admin/market/media 代理），CSP 不用放开，TZA-CC 也看不到访客。
// 安装前服务器核对大小、SHA-256、平台签名与包内 id / 版本；插件装之前先把它能做的事列出来请管理员确认。
const SCOPES = {
  themes: [["skin", "主题"]],
  characters: [["characters", "角色包"]],
  plugins: [["plugin", "插件"]],
};
const KINDS = { notify: "通知渠道", targets: "探测目标集" };
const views = {
  themes: { type: "skin", q: "", page: 1, items: [], more: false, error: "" },
  characters: { type: "characters", q: "", page: 1, items: [], more: false, error: "" },
  plugins: { type: "plugin", q: "", page: 1, items: [], more: false, error: "" },
};
let status = { configured: false };
// 有兼容新版的市场包（/admin/market/updates）：卡片与已安装列表的「有更新」都以它为准。
let updateIds = new Set();
let pollTimer = 0;
// 装 / 删 / 连接之后重画当前页：由正在显示的页面（主题、插件、设置）注册。
let rerender = async () => {};

export const media = (url) => "/api/v1/admin/market/media?url=" + encodeURIComponent(url);
export const typeName = (t) => ({ characters: "角色包", plugin: "插件" })[t] || "主题";
export const kindName = (k) => KINDS[k] || "插件";
// 插件没有色样和封面：卡片与列表里画一枚写着类型的小牌子。
export const glyph = (kind) => `<span class="plugin-glyph" aria-hidden="true">${kind === "targets" ? "目标集" : "通知"}</span>`;
const permList = (perms) => (perms?.length ? `<ul class="plugin-perms">${perms.map((x) => `<li>${esc(x)}</li>`).join("")}</ul>` : "");
const day = (value) => {
  const at = new Date(value);
  return value && !Number.isNaN(at.getTime()) ? at.toLocaleDateString("zh-CN") : "";
};
// 价格以分计；付费包（price > 0）要绑定的 TZA-CC 账号拥有它才能装（owned）。
const yuan = (cents) => "¥" + (cents / 100).toFixed(2);
const locked = (it) => it.price > 0 && !it.owned;
export const swatch = (colors) =>
  `<span class="skin-swatch" data-colors="${esc((colors || []).join(","))}" aria-hidden="true"><i></i><i></i><i></i></span>`;
// 角色包只给二次元主题添角色：二次元主题包没装时装了也不生效，市场里先拦住。
const animeInstalled = () => (window.__tzaThemes || []).some((t) => t.id === ANIME && t.type !== "characters");

export const marketStatus = () => status;
export const hasUpdate = (id) => updateIds.has(id);

// 进页时读一次市场状态；installed 里有市场装的包时顺带查更新，scope 给了就把那一页的目录也取回来。
export async function loadMarket(rerenderPage, installed = [], scope = "") {
  const scopes = (Array.isArray(scope) ? scope : [scope]).filter(Boolean);
  rerender = rerenderPage;
  stopPoll();
  status = await api("/admin/market/status").catch(() => ({ configured: false }));
  if (!status.configured) {
    updateIds = new Set();
    return [];
  }
  const [updates] = await Promise.all([
    installed.some((t) => t.source === "market") ? api("/admin/market/updates").then((v) => v.updates).catch(() => []) : [],
    ...scopes.map((s) => fetchCatalog(s, false)),
  ]);
  updateIds = new Set(updates.map((u) => u.id));
  return updates;
}

// 二次元设置菜单里的角色包市场：没开市场就不出现，样子与主题市场同一套（搜索、卡片、详情里安装）。
export function characterMarket() {
  if (!status.configured) return "";
  return `<div class="market-panel is-nested" data-scope="characters"><div class="opt-row"><span class="theme-copy"><b>从市场添加角色包</b><small>TZA-CC 上的二次元角色包，装好后出现在上面的列表里</small></span></div><div class="market-body">${catalogHTML("characters")}</div></div>`;
}

// 页头右侧的账号状态。
export function marketPill() {
  if (!status.configured) return '<span class="pill">市场已关闭</span>';
  return status.bound ? '<span class="pill good">已连接 TZA-CC</span>' : '<span class="pill">未连接 TZA-CC 账号</span>';
}

// 一页的市场：类型切换（主题页是主题 / 角色包，插件页只有插件）、搜索、卡片与「加载更多」。
export function catalogPanel(scope) {
  if (!status.configured) {
    return `<section class="panel webhook-panel market-off"><div><h3>市场已关闭</h3><p>Server 启动时用 <code>--cc-url off</code>（或环境变量 <code>TZA_CC_URL=off</code>）关掉了市场。去掉它即连官方 TZA-CC；测试或私有部署的 CC 用 <code>--cc-url https://…</code> 指定，签名公钥用 <code>--cc-key</code> / <code>TZA_CC_KEYS</code> 添加。</p><small>不连 TZA-CC 也可以在上面上传 zip 手动安装。</small></div></section>`;
  }
  const keys = status.trusted_keys ? "" : '<p class="warn-text market-keys">没有可信的签名公钥：可以浏览，但安装会被拒绝。启动时用 --cc-key 或 TZA_CC_KEYS 添加。</p>';
  return `<section class="panel market-panel" id="market-catalog" data-scope="${scope}"><div class="panel-head"><div><h3>市场</h3><span class="muted">从 TZA-CC 安装${scope === "plugins" ? "插件" : "主题（二次元的角色包在它的「设置」里）"}；免费包不连接账号也能装，付费包要先在「面板设置 → 账号与安全」连接 TZA-CC。</span></div></div>${keys}<div class="market-body">${catalogHTML(scope)}</div></section>`;
}

async function fetchCatalog(scope, append) {
  const view = views[scope];
  const params = new URLSearchParams({ type: view.type, page: String(view.page) });
  if (view.q) params.set("q", view.q);
  try {
    const page = await api("/admin/market/catalog?" + params);
    view.items = append ? [...view.items, ...page.items] : page.items;
    view.more = page.more;
    view.error = "";
  } catch (e) {
    if (!append) view.items = [];
    view.more = false;
    view.error = e.message;
  }
}

function catalogHTML(scope) {
  const view = views[scope];
  const types = SCOPES[scope];
  const tabs = types.length > 1 ? `<div class="segmented" role="group" aria-label="包类型">${types.map(([id, label]) => `<button type="button" data-market-type="${id}" class="${view.type === id ? "active" : ""}" aria-pressed="${view.type === id}">${label}</button>`).join("")}</div>` : "";
  const cards = view.items.map(card).join("");
  const empty = view.error
    ? `<p class="market-empty warn-text">${esc(view.error)}</p>`
    : `<p class="market-empty">${view.q ? `没有找到「${esc(view.q)}」相关的${typeName(view.type)}。` : `市场里还没有${typeName(view.type)}。`}</p>`;
  const note = view.type === "characters" && !animeInstalled() ? '<p class="hint market-note">角色包只给二次元主题添角色：先在「主题」里装好二次元，再装角色包。</p>' : "";
  return `<div class="market-toolbar">${tabs}<form class="market-search" role="search"><input class="market-q" type="search" maxlength="64" placeholder="搜索名称、作者" value="${esc(view.q)}" aria-label="搜索市场"><button type="submit" class="primary">搜索</button></form></div>${note}${cards ? `<div class="market-grid">${cards}</div>` : empty}<div class="catalog-toolbar event-more"><button class="quiet market-more" ${view.more ? "" : "hidden"}>加载更多</button></div>`;
}

function card(it) {
  // 没有封面或封面取不到（.broken）时露出三格色样，角色包没有色样就写类型。
  const fallback = it.type === "plugin" ? glyph(it.kind) : it.swatch?.length ? swatch(it.swatch) : `<span class="muted">${typeName(it.type)}</span>`;
  const cover = `<span class="market-cover${it.cover ? "" : " broken"}">${it.cover ? `<img data-media src="${esc(media(it.cover))}" alt="" loading="lazy">` : ""}${fallback}</span>`;
  const badge = it.installed?.source === "market" && updateIds.has(it.id)
    ? '<span class="pill warn">有更新</span>'
    : it.installed
      ? `<span class="pill good">${it.installed.source === "market" ? "已安装" : "已有本地版"}</span>`
      : it.price > 0
        ? `<span class="pill${it.owned ? " good" : ""}">${it.owned ? "已拥有" : yuan(it.price)}</span>`
        : "";
  const meta = [it.type === "plugin" && kindName(it.kind), it.latest && "v" + it.latest, it.author?.name, it.downloads ? `${it.downloads} 次下载` : ""].filter(Boolean);
  return `<button type="button" class="market-card${it.type === "plugin" ? " is-plugin" : ""}" data-package="${esc(it.id)}">${cover}<span class="market-card-body"><span class="market-card-title"><b>${esc(it.name)}</b>${badge}</span><small>${esc(it.summary || "")}</small><span class="market-card-meta">${meta.map((m) => `<span>${esc(m)}</span>`).join("")}</span></span></button>`;
}

// 已安装列表里一行的「市场 / 本地」标记与「有更新 / 详情」按钮，主题页与插件页共用。
export function sourceBits(t, updates) {
  const u = updates.find((x) => x.id === t.id);
  const market = t.source === "market";
  return {
    pills: `${u ? '<span class="pill warn">有更新</span>' : ""}<span class="pill${market ? " good" : ""}">${market ? "市场" : "本地"}</span>`,
    actions: [
      u ? `<button type="button" class="primary" data-market-update="${esc(t.id)}" data-version="${esc(u.latest)}">更新到 v${esc(u.latest)}</button>` : "",
      market && status.configured ? `<button type="button" class="quiet" data-package="${esc(t.id)}">详情</button>` : "",
    ].join(""),
  };
}

// 插件装之前先列出它能做的事，管理员确认后才装（市场安装与本地上传都走这里）。
export function confirmPlugin(p, action, label) {
  const d = modal(
    `安装插件 · ${p.name}`,
    `<p>${esc(kindName(p.kind))}${p.version ? ` · v${esc(p.version)}` : ""}${p.author ? ` · ${esc(p.author?.name || p.author)}` : ""}。这个插件能做的事：</p>${permList(p.permissions) || '<p class="muted">插件没有声明任何能力。</p>'}<p class="hint">插件只含声明式的配置与模板，由 Probe 解释执行，不含任何代码。${p.kind === "notify" ? "装好后要在「告警与通知 → 配置出口」选用它才会发通知。" : "装好后这些目标出现在新建任务的目标目录里，不会自动建任务。"}</p><div class="form-footer"><button class="primary" id="confirm-plugin">${esc(label)}</button></div>`,
  );
  d.querySelector("#confirm-plugin").onclick = guard(async (e) => {
    e.currentTarget.disabled = true;
    try {
      await action();
    } finally {
      e.target.disabled = false;
    }
  });
}

export function pluginToast(m) {
  // 目标集改了任务目录：后台缓存的目录作废，下次打开任务表单重新读。
  state.catalog = null;
  return m.kind === "targets"
    ? `目标集已安装：${m.name}（新建任务时在目标目录里能找到）`
    : `通知插件已安装：${m.name}（到「告警与通知 → 配置出口」选用它）`;
}

export function paint(root) {
  for (const box of root.querySelectorAll("[data-colors]")) {
    const colors = box.dataset.colors.split(",");
    // CSP 的 style-src 'self' 会丢掉 HTML 里的 style 属性，经 CSSOM 写的不受限。
    box.querySelectorAll("i").forEach((cell, i) => {
      if (/^#[0-9a-fA-F]{6}$/.test(colors[i] || "")) cell.style.background = colors[i];
    });
  }
  // 预览图取不到（CC 下线、图片被删）时退回占位，不留一个破图标。
  for (const img of root.querySelectorAll("img[data-media]")) {
    img.addEventListener("error", () => img.parentElement?.classList.add("broken"), { once: true });
  }
}

// 市场块与已安装列表里的「详情」「更新到」按钮。
export function wireMarket(root = content()) {
  paint(root);
  root.querySelectorAll("[data-scope]").forEach(wireCatalog);
  root.querySelectorAll(".theme-panel [data-package]").forEach((b) => (b.onclick = guard(() => detail(b.dataset.package))));
  root.querySelectorAll("[data-market-update]").forEach((b) =>
    (b.onclick = guard(async () => {
      b.disabled = true;
      try {
        await install(b.dataset.marketUpdate, b.dataset.version);
      } finally {
        b.disabled = false;
      }
    })),
  );
}

function wireCatalog(box) {
  if (!box) return;
  const scope = box.dataset.scope;
  const view = views[scope];
  const body = box.querySelector(".market-body");
  paint(body);
  const reload = async (append = false) => {
    box.classList.add("is-loading");
    try {
      await fetchCatalog(scope, append);
      body.innerHTML = catalogHTML(scope);
    } finally {
      box.classList.remove("is-loading");
    }
    wireCatalog(box);
  };
  body.querySelectorAll("[data-market-type]").forEach((b) =>
    (b.onclick = guard(async () => {
      view.type = b.dataset.marketType;
      view.page = 1;
      await reload();
    })),
  );
  body.querySelector(".market-search").onsubmit = guard(async (e) => {
    e.preventDefault();
    view.q = body.querySelector(".market-q").value.trim();
    view.page = 1;
    await reload();
  });
  body.querySelector(".market-more").onclick = guard(async () => {
    view.page += 1;
    await reload(true);
  });
  body.querySelectorAll(".market-card").forEach((b) => (b.onclick = guard(() => detail(b.dataset.package))));
}

// 详情弹窗：说明、截图（经代理）、版本列表与更新说明；主按钮装当前 Probe 能用的最新版。
async function detail(id) {
  const p = await api("/admin/market/packages/" + encodeURIComponent(id));
  const best = p.versions.find((v) => v.compatible);
  const have = p.installed;
  // 角色包要先有二次元主题；已经装着的角色包照常能更新（二次元被删后它们只是暂不生效）。
  const needsAnime = p.type === "characters" && !animeInstalled() && !have;
  const shots = [p.cover, ...(p.screenshots || [])].filter(Boolean);
  const versions = p.versions.map((v) => {
    const current = have?.source === "market" && have.version === v.version;
    const action = current
      ? '<span class="pill good">已安装</span>'
      : v.compatible && !locked(p) && !needsAnime
        ? `<button type="button" class="quiet" data-install-version="${esc(v.version)}">安装此版本</button>`
        : v.compatible
          ? ""
        : `<span class="pill warn" title="需要 Probe ${esc(v.probe)}">不兼容</span>`;
    const meta = [day(v.published_at), v.size ? bytes(v.size) : "", v.probe ? "需要 Probe " + v.probe : ""].filter(Boolean).join(" · ");
    return `<div class="market-version"><div><b>v${esc(v.version)}</b><small>${esc(meta)}</small>${v.changelog ? `<p>${esc(v.changelog)}</p>` : ""}</div>${action}</div>`;
  }).join("");
  let label = "没有兼容当前 Probe 的版本";
  if (best) {
    if (!have) label = `安装 v${best.version}`;
    else if (have.source !== "market") label = `用市场版 v${best.version} 替换本地包`;
    else if (have.version === best.version) label = `已安装 v${best.version}`;
    else label = `更新到 v${best.version}`;
  }
  if (best && locked(p)) label = `付费包 ${yuan(p.price)}，账号还没有拥有`;
  if (best && needsAnime) label = "先安装二次元主题";
  const disabled = !best || locked(p) || needsAnime || (have?.source === "market" && have.version === best.version);
  // 付费包：没绑定账号的先连接；绑定了但没拥有的去 TZA-CC 用兑换码兑换，回来刷新即可安装。
  const paid = p.price > 0
    ? `<p class="${locked(p) ? "warn-text" : "hint"}">${locked(p) ? (status.bound ? `这是付费包（${yuan(p.price)}），你连接的 TZA-CC 账号还没有拥有它。在 TZA-CC 用兑换码兑换到这个账号后，回来重新打开即可安装。` : `这是付费包（${yuan(p.price)}）。先在「面板设置 → 账号与安全」连接 TZA-CC 账号，并在 TZA-CC 用兑换码兑换到这个账号。`) : `付费包 · 你的账号已拥有（${yuan(p.price)}）。`}</p>`
    : "";
  const anime = needsAnime ? '<p class="warn-text">角色包只给二次元主题添角色：先在「主题」页的市场里装好二次元。</p>' : "";
  const meta = [p.id, typeName(p.type), p.author?.name, p.downloads ? `${p.downloads} 次下载` : "", day(p.updated_at) && "更新于 " + day(p.updated_at)].filter(Boolean).join(" · ");
  const plugin = p.type === "plugin";
  const d = modal(
    p.name,
    `<div class="market-detail">${plugin ? `<p>${glyph(p.kind)}</p>` : ""}${shots.length ? `<div class="market-shots">${shots.map((s) => `<span class="market-shot"><img data-media src="${esc(media(s))}" alt="" loading="lazy"></span>`).join("")}</div>` : p.swatch?.length ? `<p>${swatch(p.swatch)}</p>` : ""}<p class="market-meta">${esc(meta)}${p.url ? ` · <a href="${esc(p.url)}" target="_blank" rel="noopener noreferrer">在 TZA-CC 查看</a>` : ""}</p>${paid}${anime}${p.description ? `<p class="market-desc">${esc(p.description)}</p>` : p.summary ? `<p class="market-desc">${esc(p.summary)}</p>` : ""}${have ? `<p class="hint">本机已有 ${have.version ? "v" + esc(have.version) : ""}（${have.source === "market" ? "市场" : "本地上传"}）。</p>` : ""}${plugin ? `<h3>这个插件能做的事</h3>${permList(p.permissions) || '<p class="muted">市场没有列出。</p>'}` : ""}<h3>版本</h3><div class="market-versions">${versions || '<p class="muted">市场没有列出版本。</p>'}</div><div class="form-footer"><button class="primary" id="market-install" ${disabled ? "disabled" : ""}>${esc(label)}</button></div></div>`,
  );
  paint(d);
  const run = async (version, button) => {
    button.disabled = true;
    const text = button.textContent;
    button.textContent = "下载与校验中…";
    try {
      await install(p.id, version);
    } finally {
      button.disabled = false;
      button.textContent = text;
    }
  };
  d.querySelector("#market-install").onclick = guard(async (e) => {
    const button = e.currentTarget;
    if (plugin) {
      confirmPlugin({ ...p, version: best.version }, () => install(p.id, best.version), have ? (have.source === "market" ? `更新到 v${best.version}` : "替换本地插件并安装") : "确认安装");
      return;
    }
    if (have && have.source !== "market") {
      confirmAction(`替换本地包 · ${p.name}`, `本机的「${p.name}」是本地上传的，安装市场版会替换它。`, () => install(p.id, best.version), { label: "替换", danger: false });
      return;
    }
    await run(best.version, button);
  });
  d.querySelectorAll("[data-install-version]").forEach((b) =>
    (b.onclick = guard((e) => (plugin ? confirmPlugin({ ...p, version: b.dataset.installVersion }, () => install(p.id, b.dataset.installVersion), "确认安装") : run(b.dataset.installVersion, e.currentTarget)))),
  );
}

async function install(id, version) {
  const m = await send("/admin/market/install", "POST", { id, version });
  document.querySelector("#dialog")?.close();
  await rerender();
  if (m.type === "plugin") return toast(pluginToast(m));
  toast(m.type === "characters" ? `角色包已安装：${m.name} v${m.version}（二次元主题里按节点名 / 地区自动用上）` : `主题已安装：${m.name} v${m.version}（右上角外观菜单里可以选它）`);
}

// ---- TZA-CC 账号（面板设置 → 账号与安全）----
// 设备码流程：点「连接」拿到一串验证码，到 TZA-CC 登录后输入它；本页按服务器给的间隔轮询，
// 确认后凭据存在数据目录的 cc-instance。免费包不连接也能装，连接后下载会计入账号、能看到每台实例装了什么。
export function accountPanel() {
  if (!status.configured) {
    return `<section class="panel webhook-panel market-connect"><div><h3>TZA-CC 账号</h3><p>市场已关闭（Server 用 <code>--cc-url off</code> 启动），不需要连接账号。</p></div></section>`;
  }
  const keys = status.trusted_keys ? "" : '<p class="warn-text">没有可信的签名公钥：可以浏览，但安装会被拒绝。启动时用 --cc-key 或 TZA_CC_KEYS 添加。</p>';
  const host = `<small>市场地址：${esc(status.url)} · Probe ${esc(status.probe || "")}</small>`;
  if (status.pending) {
    const p = status.pending;
    return `<section class="panel webhook-panel market-connect"><div><h3>连接 TZA-CC</h3><p>在浏览器打开 <a href="${esc(p.verify_url)}" target="_blank" rel="noopener noreferrer">${esc(p.verify_url)}</a>，登录后输入验证码：</p><p><code class="market-code" id="market-code">${esc(p.user_code)}</code></p><p class="hint" id="market-poll">等待确认… 验证码 ${esc(new Date(p.expires_at).toLocaleTimeString("zh-CN", { hour12: false }))} 前有效。</p>${keys}${host}</div><div class="webhook-actions"><button id="market-copy-code">复制验证码</button><button id="market-cancel">取消</button></div></section>`;
  }
  if (status.bound) {
    return `<section class="panel webhook-panel market-connect"><div><h3>TZA-CC 账号</h3><p>已连接：<b>${esc(status.account || "（未命名账号）")}</b> · 实例 ${esc(status.instance_id)}${status.bound_at ? ` · ${esc(day(status.bound_at))} 绑定` : ""}</p>${keys}${host}</div><div class="webhook-actions"><button id="market-disconnect" class="danger">断开连接</button></div></section>`;
  }
  const other = status.other_url ? `<p class="warn-text">本机保存的是另一个 TZA-CC（${esc(status.other_url)}）的绑定，对当前地址不生效。</p>` : "";
  return `<section class="panel webhook-panel market-connect"><div><h3>TZA-CC 账号</h3><p>「主题」「插件」页的市场里，免费包不连接也能装。连接后下载计入你的账号，付费包也要连接后才能装，可在 TZA-CC 用户中心看到每台实例装了什么。</p>${other}${keys}${host}</div><div class="webhook-actions">${status.other_url ? '<button id="market-disconnect">删除旧绑定</button>' : ""}<button id="market-connect" class="primary">连接 TZA-CC</button></div></section>`;
}

export function wireAccount(root = content()) {
  root.querySelector("#market-connect")?.addEventListener("click", connectForm);
  root.querySelector("#market-cancel")?.addEventListener("click", guard(async () => {
    stopPoll();
    await send("/admin/market/connect", "DELETE");
    await rerender();
  }));
  root.querySelector("#market-copy-code")?.addEventListener("click", guard(async () => {
    const code = status.pending?.user_code || "";
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(code);
      toast("验证码已复制");
    } else toast("请手动输入验证码：" + code);
  }));
  root.querySelector("#market-disconnect")?.addEventListener("click", () =>
    confirmAction(
      "断开 TZA-CC",
      status.bound ? `断开后这台实例从账号「${status.account || ""}」移除，已装的包不受影响，之后仍可再连接。` : "删除本机保存的旧绑定凭据。",
      async () => {
        const out = await send("/admin/market/connect", "DELETE");
        await rerender();
        toast(out?.warning || "已断开 TZA-CC");
      },
      { label: "断开连接" },
    ),
  );
  if (status.pending) schedulePoll(status.pending.interval);
}

function connectForm() {
  const d = modal("连接 TZA-CC", `<form><label>实例名<input name="name" maxlength="40" placeholder="TZA Probe" autocomplete="off"></label><p class="hint">用来在 TZA-CC 用户中心区分你的几台实例，可留空。除实例名与 Probe 版本外不发送这台服务器的任何信息。</p><div class="form-footer"><button class="primary" type="submit">获取验证码</button></div></form>`);
  d.querySelector("form").onsubmit = guard(async (e) => {
    e.preventDefault();
    const name = String(new FormData(e.target).get("name") || "").trim();
    await send("/admin/market/connect", "POST", name ? { name } : {});
    d.close();
    await rerender();
  });
}

function stopPoll() {
  clearTimeout(pollTimer);
  pollTimer = 0;
}

// 轮询只在验证码还显示着时进行：换页或面板被重画掉就停。
function schedulePoll(seconds) {
  stopPoll();
  pollTimer = setTimeout(async () => {
    const line = content()?.querySelector("#market-poll");
    if (!line) return;
    try {
      const r = await send("/admin/market/connect/poll", "POST");
      if (r.status === "pending") return schedulePoll(r.interval || seconds);
      await rerender();
      if (r.status === "bound") toast(`已连接 TZA-CC：${r.account || ""}`);
      else if (r.status === "expired") toast("验证码已过期，请重新连接");
    } catch (e) {
      line.textContent = `暂时没有确认（${e.message}），稍后自动重试…`;
      schedulePoll(seconds);
    }
  }, Math.max(2, seconds || 5) * 1000);
}
