import { esc, guard } from "./api.js";
import { content, state } from "./admin-state.js";
import { renderIPGuard } from "./admin-ip-guard.js";

// 地区偏移检测（IPSentinel）是 Server 内置功能：主控保存设置与任务，并通过独立隔离浏览器执行检测；
// Agent 只需本机出口许可。不需要另装插件。
export async function renderRegionPage(nodesReady) {
  await nodesReady;
  state.tab = "ipguard";
  const root = content();
  root.innerHTML = '<section data-node-plugin-detail><div class="page-heading"><div><h1>地区偏移检测 <small class="muted">IPSentinel</small></h1><p>查看 Google、Meta 地区状态，按目标定位尝试校正并独立复测。</p></div></div><div data-plugin-workspace></div></section>';
  const workspace = root.querySelector("[data-plugin-workspace]");
  const show = async () => {
    workspace.innerHTML = '<p role="status">正在读取出口与任务…</p>';
    try {
      await renderIPGuard(workspace);
    } catch (error) {
      if (workspace.isConnected) {
        workspace.innerHTML = `<section class="panel"><p role="alert">加载失败：${esc(error.message)}</p><button>重新加载</button></section>`;
        workspace.querySelector("button").onclick = guard(show);
      }
    }
  };
  await show();
}
