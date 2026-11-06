import { esc, guard, send, toast } from "./api.js";
import { content, state, confirmAction } from "./admin-state.js";
import { renderPluginAgents } from "./admin-plugin-agents.js";
import { renderIPGuard } from "./admin-ip-guard.js";

export async function openNodePluginDetail(pkg, { settings = false, trusted = true, back } = {}) {
  if (pkg.id !== "ipsentinel-runtime" || pkg.type !== "executor") throw new Error("不支持此插件");
  document.querySelector("#dialog")?.close();
  state.tab = "plugins";
  const root = content();
  root.innerHTML = `<section data-node-plugin-detail><div class="page-heading"><div><button class="quiet" data-plugin-back>← 返回插件</button><h1>IP 地区防护 <small class="muted">IPSentinel</small></h1><p>TZA-CC 官方插件 · 防送中检测与拉回尝试</p></div></div><div data-plugin-install></div><div class="segmented settings-tabs" role="tablist" aria-label="插件详情"><button type="button" role="tab" data-plugin-section="overview">功能</button><button type="button" role="tab" data-plugin-section="settings">Agent</button></div><div data-plugin-body></div></section>`;
  const page = root.querySelector("[data-node-plugin-detail]");
  page.querySelector("[data-plugin-back]").onclick = guard(async () => {
    if (back) await back();
    else await (await import("./admin-plugins.js")).renderPlugins();
  });
  const show = async section => {
    page.querySelectorAll("[data-plugin-section]").forEach(button => {
      const selected = button.dataset.pluginSection === section;
      button.classList.toggle("active", selected);
      button.setAttribute("aria-selected", String(selected));
    });
    const installArea = page.querySelector("[data-plugin-install]");
    const body = page.querySelector("[data-plugin-body]");
    const version = (pkg.versions || []).find(release => release.compatible);
    const update = version && pkg.installed && version.version !== pkg.installed.version;
    installArea.innerHTML = pkg.installed
      ? `<p class="hint">主控管理部分 v${esc(pkg.installed.version)} · Agent 组件独立安装与启用</p>${update ? `<button data-install-manager ${trusted ? "" : "disabled"}>更新管理部分至 v${esc(version.version)}</button>` : ""}`
      : `<section class="panel"><h2>先安装插件</h2><p>主控安装汇总管理部分，不运行浏览器。之后在 Agent 页添加实际组件，未启用时不启动检测任务。</p><button class="primary" data-install-manager ${trusted && version ? "" : "disabled"}>安装插件${version ? ` v${esc(version.version)}` : ""}</button>${trusted ? version ? "" : "<p>暂无兼容版本。</p>" : '<p class="warn-text">缺少可信签名公钥，仅可浏览。</p>'}</section>`;
    const install = installArea.querySelector("[data-install-manager]");
    if (install && version) install.onclick = () => confirmAction(update ? "更新 IP 地区防护" : "安装 IP 地区防护", `在主控安装 v${version.version} 的签名管理部分；不下载或更新 Agent 运行依赖，不启用任务。`, async () => {
      install.disabled = true;
      try {
        const installed = await send("/admin/market/install", "POST", { id: pkg.id, version: version.version });
        pkg.installed = { version: installed.version, source: installed.source };
        toast("管理部分已安装，可管理 Agent 组件");
        if (page.isConnected) await show("settings");
      } finally { install.disabled = false; }
    }, { label: update ? "更新管理部分" : "安装插件", danger: false });
    if (!pkg.installed) {
      body.innerHTML = '<section class="panel"><h2>防送中与拉回</h2><p>检测服务的地区异常，尝试拉回，再用干净会话独立复测。不保证改变服务商的地区判定。</p></section>';
      return;
    }
    body.innerHTML = '<div data-plugin-workspace><p role="status">正在读取…</p></div>';
    const workspace = body.querySelector("[data-plugin-workspace]");
    try {
      if (section === "settings") await renderPluginAgents(pkg, workspace);
      else await renderIPGuard(workspace, () => show("settings"));
    } catch (error) {
      if (workspace.isConnected) {
        workspace.innerHTML = `<section class="panel"><p role="alert">加载失败：${esc(error.message)}</p><button>重新加载</button></section>`;
        workspace.querySelector("button").onclick = guard(() => show(section));
      }
    }
  };
  page.querySelectorAll("[data-plugin-section]").forEach(button => {
    button.onclick = guard(() => show(button.dataset.pluginSection));
  });
  await show(settings ? "settings" : "overview");
}
