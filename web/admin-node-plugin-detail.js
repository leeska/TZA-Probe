import { esc, guard } from "./api.js";
import { content, state, loadNodes } from "./admin-state.js";
import { renderExecutors } from "./admin-executors.js";
import { choosePluginNodes } from "./admin-node-plugins.js";

export async function openNodePluginDetail(pkg, { settings = false, trusted = true, back } = {}) {
  if (pkg.id !== "ipsentinel-runtime" || pkg.type !== "executor") throw new Error("不支持此节点插件");
  document.querySelector("#dialog")?.close();
  state.tab = "plugins";
  const root = content();
  root.innerHTML = `<section data-node-plugin-detail><div class="page-heading"><div><button class="quiet" data-plugin-back>← 返回插件</button><h1>${esc(pkg.name || "IPSentinel")}</h1><p>节点插件 · 安装到所选节点</p></div></div><div class="segmented settings-tabs" role="tablist" aria-label="插件详情"><button type="button" role="tab" data-plugin-section="overview">详情与版本</button><button type="button" role="tab" data-plugin-section="settings">设置</button></div><div data-plugin-body></div></section>`;
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
    const body = page.querySelector("[data-plugin-body]");
    if (section === "settings") {
      body.innerHTML = '<div data-executor-settings><p role="status">正在读取插件设置…</p></div>';
      const settingsRoot = body.querySelector("[data-executor-settings]");
      try {
        await renderExecutors(loadNodes(), settingsRoot);
      } catch (error) {
        if (settingsRoot.isConnected) {
          settingsRoot.innerHTML = `<p role="alert">${esc(error.message)}</p><button data-plugin-retry>重新加载设置</button>`;
          settingsRoot.querySelector("button").onclick = guard(() => show("settings"));
        }
      }
      return;
    }
    const versions = (pkg.versions || []).filter(version => version.compatible);
    body.innerHTML = `<section class="panel market-detail"><p class="market-desc">${esc(pkg.description || pkg.summary || "IPSentinel 节点观测与托管执行")}</p><p>默认不下载运行依赖、不启动插件进程。选择节点后按计划批准下载，配置、排期、安装与运行状态都在本插件的「设置」中管理。</p><h2>声明权限</h2><ul class="plugin-perms">${(pkg.permissions || []).map(permission => `<li>${esc(permission)}</li>`).join("")}</ul><p class="hint">依赖和平台以节点验签后的计划为准。首次安装目前仍需在节点配置本机安装通道。</p><div class="market-versions">${versions.map(version => `<div class="market-version"><div><b>v${esc(version.version)}</b><small>${esc(version.probe ? "需要 Probe " + version.probe : "")}</small>${version.changelog ? `<p>${esc(version.changelog)}</p>` : ""}</div><button class="primary" data-node-plugin-version="${esc(version.version)}" ${trusted ? "" : "disabled"}>选择节点</button></div>`).join("") || "<p>当前没有可获取的兼容版本；已有节点仍可在「设置」中管理。</p>"}</div>${trusted ? "" : '<p class="warn-text">缺少可信签名公钥，仅可浏览。</p>'}</section>`;
    body.querySelectorAll("[data-node-plugin-version]").forEach(button => {
      button.onclick = guard(async () => {
        button.disabled = true;
        try {
          await choosePluginNodes(pkg, button.dataset.nodePluginVersion, () => show("settings"));
        } finally { button.disabled = false; }
      });
    });
  };
  page.querySelectorAll("[data-plugin-section]").forEach(button => {
    button.onclick = guard(() => show(button.dataset.pluginSection));
  });
  await show(settings ? "settings" : "overview");
}
