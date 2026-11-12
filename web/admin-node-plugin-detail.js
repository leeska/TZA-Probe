import { api, esc, guard, send, toast } from "./api.js";
import { content, state, confirmAction, go } from "./admin-state.js";
import { renderIPGuard } from "./admin-ip-guard.js";

export async function renderRegionPage(nodesReady) {
  const [, plugins] = await Promise.all([nodesReady, api("/admin/plugins")]);
  const installed = plugins.find(plugin => plugin.id === "ipsentinel-runtime" && plugin.kind === "agent");
  if (!installed) {
    content().innerHTML = '<section class="page-heading"><h1>地区偏移检测</h1></section><section class="panel"><h2>尚未安装 IPSentinel</h2><p>安装插件后，在这里直接管理 Google、Meta 地区检测。</p><button class="primary" data-region-install>前往插件市场</button></section>';
    content().querySelector("[data-region-install]").onclick = guard(async () => {
      state.pluginsView = "market";
      await go("plugins");
    });
    return;
  }
  await openNodePluginDetail({ ...installed, type: "executor", installed, versions: [] }, { standalone: true });
}

export async function openNodePluginDetail(pkg, { trusted = true, back, standalone = false } = {}) {
  if (pkg.id !== "ipsentinel-runtime" || pkg.type !== "executor") throw new Error("不支持此插件");
  document.querySelector("#dialog")?.close();
  state.tab = standalone ? "ipguard" : "plugins";
  const root = content();
  root.innerHTML = `<section data-node-plugin-detail><div class="page-heading"><div>${standalone ? "" : '<button class="quiet" data-plugin-back>← 返回插件</button>'}<h1>地区偏移检测 <small class="muted">IPSentinel</small></h1><p>查看 Google、Meta 地区状态，按目标定位尝试校正并独立复测。</p></div></div><div data-plugin-install></div><div data-plugin-workspace></div></section>`;
  const page = root.querySelector("[data-node-plugin-detail]");
  const backButton = page.querySelector("[data-plugin-back]");
  if (backButton) backButton.onclick = guard(async () => {
    if (back) await back();
    else await (await import("./admin-plugins.js")).renderPlugins();
  });
  const show = async () => {
    const area = page.querySelector("[data-plugin-install]");
    const workspace = page.querySelector("[data-plugin-workspace]");
    const version = (pkg.versions || []).find(release => release.compatible);
    const update = version && pkg.installed && version.version !== pkg.installed.version;
    area.innerHTML = pkg.installed
      ? `${update ? `<button data-install-manager ${trusted ? "" : "disabled"}>更新至 v${esc(version.version)}</button>` : ""}`
      : `<section class="panel"><h2>安装地区偏移检测</h2><p>主控保存设置与任务，并通过独立隔离浏览器执行检测；Agent 只需本机出口许可，不再安装 Chromium。</p><button class="primary" data-install-manager ${trusted && version ? "" : "disabled"}>安装管理部分${version ? ` v${esc(version.version)}` : ""}</button>${!trusted ? '<p class="warn-text">缺少可信签名公钥，仅可浏览。</p>' : !version ? '<p>暂无兼容版本。</p>' : ""}</section>`;
    const install = area.querySelector("[data-install-manager]");
    if (install && version) install.onclick = () => confirmAction(update ? "更新地区偏移检测" : "安装地区偏移检测", `安装 v${version.version} 的签名管理部分，不启动检测、不安装 Agent 本机浏览器。隔离浏览器服务需由主控运维单独部署。`, async () => {
      install.disabled = true;
      try {
        const installed = await send("/admin/market/install", "POST", { id: pkg.id, version: version.version });
        pkg.installed = { version: installed.version, source: installed.source };
        toast("管理部分已安装");
        if (page.isConnected) await show();
      } finally { install.disabled = false; }
    }, { label: update ? "更新管理部分" : "安装管理部分", danger: false });
    if (!pkg.installed) { workspace.innerHTML = ""; return; }
    workspace.innerHTML = '<p role="status">正在读取出口与任务…</p>';
    try { await renderIPGuard(workspace); }
    catch (error) {
      if (workspace.isConnected) {
        workspace.innerHTML = `<section class="panel"><p role="alert">加载失败：${esc(error.message)}</p><button>重新加载</button></section>`;
        workspace.querySelector("button").onclick = guard(show);
      }
    }
  };
  await show();
}
