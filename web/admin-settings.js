import { THEMES } from "./theme.js";
import { SKINS, registerThemes, paintSwatches } from "./skins.js";
import { api, send, toast, guard, modal, esc } from "./api.js";
import { content, field, confirmAction } from "./admin-state.js";
const MCP_DOC_URL = "https://github.com/leeska/TZA-Probe/blob/main/docs/MCP.md";
const THEME_DOC_URL = "https://github.com/leeska/TZA-Probe/blob/main/docs/THEMES.md";
const mcpDocs = () => `<p class="hint mcp-doc">接入文档：<a href="${MCP_DOC_URL}" target="_blank" rel="noopener noreferrer">${MCP_DOC_URL}</a></p>`;
// 面板设置：公开面板的全站显示项（延迟色带时间窗、访客首屏外观）。以前挂在「告警与通知」页里，
// 但它们跟告警无关，单独一页。两项都走 /admin/panel 保存（见下方注释）。
export async function renderSettings(nodesReady) {
  // 本页不用节点列表；切页时外壳传来的节点请求一起等，顶栏在线计数随之刷新。页内保存后重画不传。
  const [, settings, acct, mcp, themes] = await Promise.all([nodesReady, api("/admin/panel"), api("/admin/account").catch(() => null), api("/admin/mcp-key"), api("/admin/themes").catch(() => null)]);
  // 主题清单以服务端为准：装 / 删主题包之后这里重画，皮肤菜单与默认皮肤下拉一起换成新清单。
  if (themes) registerThemes(themes);
  // 时间范围给几个常用档位（分钟是接口单位，档位只是界面）：
  // 探测结果保留 7 天，所以更长的一律取到保留上限，不另设选项。
  const windows = [
    [60, "1 小时"],
    [360, "6 小时"],
    [1440, "24 小时"],
    [4320, "3 天"],
    [10080, "7 天（上限）"],
  ];
  const windowNow = Number(settings.latency_window_minutes ?? 60);
  const windowOptions = (windows.some(([m]) => m === windowNow) ? windows : [...windows, [windowNow, `${windowNow} 分钟`]])
    .sort((a, b) => a[0] - b[0])
    .map(([m, label]) => `<option value="${m}"${m === windowNow ? " selected" : ""}>${label}</option>`)
    .join("");
  // 后台存的值直接亮在下拉框里；认不出来（未配置 / 手改过库）就显示内置默认。
  const themeNow = THEMES.some((t) => t.id === settings.default_theme) ? settings.default_theme : "auto";
  const skinNow = SKINS.some((s) => s.id === settings.default_skin) ? settings.default_skin : "glass";
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">PUBLIC PANEL</div><h1>面板设置</h1><p>公开面板的显示选项、后台账号、MCP API Key 与数据备份。</p></div></section><section class="panel webhook-panel"><div><h3>延迟色带</h3><p>公开卡片的延迟色带每格一次探测，固定画最近 10 次；早于这个范围的探测不再显示，那几格留空。</p><small>探测正常时色带总是满的，这一项只在探测间隔很长或节点停过一阵时看得出区别。探测结果保留 7 天。</small></div><div class="webhook-actions"><label class="window-field">只显示最近<select id="latency-window">${windowOptions}</select></label><button id="save-window" class="primary">保存</button></div></section><section class="panel webhook-panel"><div><h3>默认外观</h3><p>访客第一次打开公开面板时使用的明暗与风格。</p><small>访客在面板右上角切换过之后，以访客自己的选择为准。</small></div><div class="webhook-actions"><label class="window-field">明暗<select id="default-theme">${THEMES.map((t) => `<option value="${t.id}"${t.id === themeNow ? " selected" : ""}>${t.name}</option>`).join("")}</select></label><label class="window-field">风格<select id="default-skin">${SKINS.map((s) => `<option value="${esc(s.id)}"${s.id === skinNow ? " selected" : ""}>${esc(s.name)}</option>`).join("")}</select></label><button id="save-look" class="primary">保存</button></div></section>${themePanel()}${acct ? `<section class="panel webhook-panel"><div><h3>后台账号</h3><p>${acct.configured ? `当前用户名：<b>${esc(acct.username)}</b>` : "尚未设置账号，请在服务器执行 tza-server reset-password 创建账号。"}</p><small>修改后其他已登录的浏览器需要重新登录。忘记密码时在服务器执行 tza-server reset-password --data 数据目录，会生成新密码。</small></div><div class="webhook-actions"><button id="edit-account" class="primary">${acct.configured ? "修改账号" : "设置账号"}</button></div></section>` : ""}<section class="panel webhook-panel"><div><h3>MCP API Key</h3><p>${mcp.configured ? `已启用 · 生成于 ${esc(new Date(mcp.created_at).toLocaleString())}` : "尚未生成，MCP 接口未启用。"}</p><small>用于 AI 客户端连接并管理节点、任务和设置。仅用于 MCP；重新生成或撤销后旧 Key 立即失效。</small><p>连接地址：<code>${esc(location.origin + "/mcp")}</code></p><p class="hint">请求头：Authorization: Bearer &lt;API Key&gt;</p>${mcpDocs()}</div><div class="webhook-actions"><button id="generate-mcp-key" class="primary">${mcp.configured ? "重新生成" : "生成 API Key"}</button>${mcp.configured ? '<button id="revoke-mcp-key">撤销</button>' : ""}</div></section><section class="panel webhook-panel"><div><h3>数据备份</h3><p>下载整个数据库的一份快照：节点、任务、历史与设置都在里面。不用停服，面板和 Agent 上报照常。</p><small>升级到会改数据库结构的版本之前先下载一份。恢复时先停服，把它放进数据目录、命名为 tza-probe.db（替换原文件），并删掉同目录的 tza-probe.db-wal 与 tza-probe.db-shm，再启动。</small></div><div class="webhook-actions"><a id="download-backup" class="button primary" href="/api/v1/admin/backup" download>下载备份</a></div></section>`;
  // 面板显示项走 /admin/panel：通知那个出口的语义是「空值即清空」，
  // 用同一个 PUT 保存显示项会顺手把 webhook 或 Bot Token 清掉。
  content().querySelector("#save-window").onclick = guard(async () => {
    const minutes = Number(content().querySelector("#latency-window").value);
    await send("/admin/panel", "PUT", { latency_window_minutes: minutes });
    await renderSettings();
    const label = (content().querySelector("#latency-window").selectedOptions[0]?.textContent || `${minutes} 分钟`).replace(/（.*）/, "");
    toast(`延迟色带只显示最近 ${label}内的探测`);
  });
  // 前台默认外观：访客还没自己选过时的第一屏。保存后即写进 /theme-default.js，
  // 下一次打开（包括别的访客）就是新值；当前这份重渲染一遍让人当场看到。
  content().querySelector("#save-look").onclick = guard(async () => {
    const theme = content().querySelector("#default-theme").value;
    const skin = content().querySelector("#default-skin").value;
    await send("/admin/panel", "PUT", { default_theme: theme, default_skin: skin });
    await renderSettings();
    toast(`前台默认外观：${THEMES.find((t) => t.id === theme).name} · ${SKINS.find((s) => s.id === skin).name}`);
  });
  paintSwatches(content());
  wireThemes();
  content().querySelector("#generate-mcp-key").onclick = guard(async () => {
    if (mcp.configured) {
      confirmMCP("重新生成 MCP API Key", "旧 Key 将立即失效，连接的客户端需要更新配置。", generateMCPKey);
    } else {
      await generateMCPKey();
    }
  });
  content().querySelector("#revoke-mcp-key")?.addEventListener("click", () => {
    confirmMCP("撤销 MCP API Key", "撤销后客户端无法再连接 MCP，直到生成新的 Key。", async () => {
      await send("/admin/mcp-key", "DELETE");
      document.querySelector("#dialog").close();
      await renderSettings();
      toast("MCP API Key 已撤销");
    });
  });
  content().querySelector("#edit-account")?.addEventListener("click", () => accountForm(acct));
}
// 主题：内置四套 + 后台上传的主题包。主题包是一个 zip（theme.json + 样式表 + 图片 / 字体，
// 不含脚本），装好后访客就能在右上角的外观菜单里选它，也能设成前台默认。格式见 docs/THEMES.md。
// 列表里除了外观（内置四套 + 主题包），还有角色包：它们不是外观，只给二次元主题添角色，
// 所以不在外观菜单里，这里用一排立绘缩略图代替色样，写清有几个角色、各自匹配哪些地区。
function themePanel() {
  const list = window.__tzaThemes || SKINS;
  const rows = list.map((t) => {
    const meta = t.builtin ? "" : [t.id, t.version && "v" + t.version, t.author].filter(Boolean).join(" · ");
    const pack = t.type === "characters";
    const lead = pack
      ? `<span class="pack-thumbs" aria-hidden="true">${(t.characters || []).slice(0, 4).map((c) => `<img src="/themes/${esc(t.id)}/${esc(c.art)}?v=${esc(t.rev || "")}" alt="" loading="lazy">`).join("")}</span>`
      : `<span class="skin-swatch" data-swatch="${esc(t.id)}" aria-hidden="true"><i></i><i></i><i></i></span>`;
    const hint = pack
      ? `${(t.characters || []).length} 个角色：${(t.characters || []).map((c) => c.name + ((c.regions || []).length ? `（${c.regions.join(" / ")}）` : c.default ? "（默认）" : "")).join("、")}`
      : t.hint || "";
    return `<div class="theme-row">${lead}<span class="theme-copy"><b>${esc(t.name)}</b><small>${esc(hint)}${meta ? `<br>${esc(meta)}` : ""}</small></span><span class="pill">${t.builtin ? "内置" : pack ? "角色包" : "主题包"}</span>${t.builtin ? "" : `<button type="button" class="danger" data-remove-theme="${esc(t.id)}">删除</button>`}</div>`;
  }).join("");
  return `<section class="panel theme-panel"><div class="panel-head"><div><h3>主题</h3><span class="muted">主题包换外观，角色包给二次元主题添角色；都只含样式表、图片和字体，不能带脚本。</span></div><div class="panel-actions"><label class="button primary theme-upload">上传主题包<input id="theme-file" type="file" accept=".zip,application/zip" hidden></label></div></div><div class="theme-list">${rows}</div><p class="hint">主题包格式与可用的颜色变量见 <a href="${THEME_DOC_URL}" target="_blank" rel="noopener noreferrer">docs/THEMES.md</a>。同一个 id 再次上传即覆盖升级；主题包存在数据目录的 themes/ 下，不在数据库备份里。</p></section>`;
}

function wireThemes() {
  const input = content().querySelector("#theme-file");
  input.onchange = guard(async () => {
    const file = input.files[0];
    input.value = "";
    if (!file) return;
    const m = await api("/admin/themes", { method: "POST", body: file, headers: { "Content-Type": "application/zip" } });
    await renderSettings();
    toast(m.type === "characters" ? `角色包已安装：${m.name}（二次元主题里按节点名 / 地区自动用上，也可以在节点设置里指定）` : `主题包已安装：${m.name}（右上角外观菜单里可以选它）`);
  });
  content().querySelectorAll("[data-remove-theme]").forEach((b) =>
    (b.onclick = () => {
      const t = (window.__tzaThemes || SKINS).find((s) => s.id === b.dataset.removeTheme);
      confirmAction(
        `删除主题包 · ${t?.name || b.dataset.removeTheme}`,
        t?.type === "characters"
          ? `删除后用到这些角色的节点回到自动匹配（多半是通用角色）。`
          : `删除后正在用它的访客会回到默认皮肤；它若是前台默认皮肤，默认也回到液态玻璃。`,
        async () => {
          await send("/admin/themes/" + encodeURIComponent(b.dataset.removeTheme), "DELETE");
          await renderSettings();
        },
        { label: "删除主题包" },
      );
    }),
  );
}

// 改用户名与密码：已有账号时要先输入当前密码。新密码填两遍，防止手误把自己锁在外面。
function accountForm(acct) {
  const current = acct.configured ? field("当前密码", "current_password", "", "password", 'required autocomplete="current-password"') : "";
  const d = modal(acct.configured ? "修改后台账号" : "设置后台账号", `<form>${current}${field("用户名", "username", acct.username || "admin", "text", 'required maxlength="32" autocomplete="username" autocapitalize="off" spellcheck="false"')}${field("新密码", "password", "", "password", 'required minlength="8" maxlength="128" autocomplete="new-password"')}${field("再输入一次新密码", "confirm", "", "password", 'required minlength="8" maxlength="128" autocomplete="new-password"')}<p class="hint">密码 8–128 个字符。保存后其他浏览器的登录会失效，本页保持登录。</p><div class="form-footer"><button class="primary" type="submit">保存</button></div></form>`);
  d.querySelector("form").onsubmit = guard(async (e) => {
    e.preventDefault();
    const data = new FormData(e.target);
    if (data.get("password") !== data.get("confirm")) {
      toast("两次输入的新密码不一致");
      return;
    }
    const saved = await send("/admin/account", "PUT", {
      current_password: data.get("current_password") || "",
      username: String(data.get("username")).trim(),
      password: data.get("password"),
    });
    d.close();
    await renderSettings();
    toast(`后台账号已更新：${saved.username}`);
  });
}

function confirmMCP(title, message, action) {
  const d = modal(title, `<p>${esc(message)}</p><div class="form-footer"><button id="confirm-mcp" class="primary">确认</button></div>`);
  d.querySelector("#confirm-mcp").onclick = guard(async (e) => {
    e.target.disabled = true;
    try { await action(); } finally { e.target.disabled = false; }
  });
}
async function generateMCPKey() {
  const saved = await send("/admin/mcp-key", "POST");
  const d = modal("保存 MCP API Key", `<p>Key 只显示这一次，请立即复制保存。关闭后无法再次查看。</p><label>API Key<input id="mcp-api-key" type="text" readonly autocomplete="off" spellcheck="false" value="${esc(saved.api_key)}"></label><p class="hint">连接地址：${esc(location.origin + "/mcp")}<br>请求头：Authorization: Bearer &lt;API Key&gt;</p>${mcpDocs()}<div class="form-footer"><button id="copy-mcp-key" class="primary">复制 API Key</button></div>`);
  const input = d.querySelector("#mcp-api-key");
  input.onclick = () => input.select();
  d.querySelector("#copy-mcp-key").onclick = guard(async () => {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(input.value);
      toast("MCP API Key 已复制");
    } else {
      input.select();
      toast("请复制选中的 API Key");
    }
  });
  d.addEventListener("close", () => { input.value = ""; input.removeAttribute("value"); }, { once: true });
  await renderSettings();
}
