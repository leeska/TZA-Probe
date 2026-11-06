import { esc, guard } from "./api.js";
import { content, state, loadNodes } from "./admin-state.js";
import { renderExecutors } from "./admin-executors.js";
import { choosePluginNodes } from "./admin-node-plugins.js";
import { renderIPGuard } from "./admin-ip-guard.js";

export async function openNodePluginDetail(pkg, { settings = false, trusted = true, back } = {}) {
  if (pkg.id !== "ipsentinel-runtime" || pkg.type !== "executor") throw new Error("不支持此节点插件");
  document.querySelector("#dialog")?.close();
  state.tab = "plugins";
  const root = content();
  root.innerHTML = `<section data-node-plugin-detail><div class="page-heading"><div><button class="quiet" data-plugin-back>← 返回插件</button><h1>IP 地区防护 <small class="muted">IPSentinel</small></h1><p>TZA-CC 官方节点插件 · 防送中检测与拉回尝试</p></div></div><div class="segmented settings-tabs" role="tablist" aria-label="插件详情"><button type="button" role="tab" data-plugin-section="overview">防送中与拉回</button><button type="button" role="tab" data-plugin-section="settings">安装与高级设置</button></div><div data-plugin-body></div></section>`;
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
        if (settingsRoot.isConnected) showVersions();
      } catch (error) {
        if (settingsRoot.isConnected) {
          settingsRoot.innerHTML = `<p role="alert">${esc(error.message)}</p><button data-plugin-retry>重新加载设置</button>`;
          settingsRoot.querySelector("button").onclick = guard(() => show("settings"));
        }
      }
      return;
    }
    body.innerHTML = `<div class="ip-guard-intro"><h2>检测送中，尝试拉回</h2><p>检测现状 → 按批准方案尝试拉回 → 干净复测。各服务独立判断，不保证改变服务商的地区库。</p></div><div data-ip-guard></div>`;
    const workspace = body.querySelector("[data-ip-guard]");
    try {
      await renderIPGuard(workspace, async () => {
        await show("settings");
        page.querySelector("[data-guard-versions]")?.setAttribute("open", "");
      });
    } catch (error) {
      if (workspace.isConnected) {
        workspace.innerHTML = `<section class="panel"><p role="alert">节点状态加载失败：${esc(error.message)}</p><button data-guard-retry>重新加载</button></section>`;
        workspace.querySelector("button").onclick = guard(() => show("overview"));
      }
    }
  };
  const showVersions = () => {
    const versions = (pkg.versions || []).filter(version => version.compatible);
    const section = page.querySelector("[data-plugin-body]");
    section.insertAdjacentHTML("afterbegin", `<details class="panel ip-guard-versions" data-guard-versions><summary>安装到节点${versions[0] ? ` · v${esc(versions[0].version)}` : ""}</summary><p>默认关闭；批准安装计划后才下载依赖。首次安装需节点已配置本机安装通道，安装与托管授权分别确认。</p><div class="market-versions">${versions.map(version => `<div class="market-version"><b>v${esc(version.version)}</b><button class="primary" data-node-plugin-version="${esc(version.version)}" ${trusted ? "" : "disabled"}>选择节点</button></div>`).join("") || "<p>暂无可获取的兼容版本；已有节点仍可管理。</p>"}</div>${trusted ? "" : '<p class="warn-text">缺少可信签名公钥，仅可浏览。</p>'}<details><summary>权限与版本说明</summary><ul>${(pkg.permissions || []).map(permission => `<li>${esc(permission)}</li>`).join("")}</ul><p>${esc(pkg.description || pkg.summary || "具体权限、依赖大小和兼容性以节点验证后的安装计划为准。")}</p></details></details>`);
    const body = section;
    body.querySelectorAll("[data-node-plugin-version]").forEach(button => {
      button.onclick = guard(async () => {
        button.disabled = true;
        try {
          await choosePluginNodes(pkg, button.dataset.nodePluginVersion, async () => {
            await show("settings");
            page.querySelector("[data-guard-installation]")?.setAttribute("open", "");
          });
        } finally { button.disabled = false; }
      });
    });
  };
  page.querySelectorAll("[data-plugin-section]").forEach(button => {
    button.onclick = guard(() => show(button.dataset.pluginSection));
  });
  await show(settings ? "settings" : "overview");
}
