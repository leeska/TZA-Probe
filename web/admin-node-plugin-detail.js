import { esc, guard, modal } from "./api.js";
import { renderIPGuard } from "./admin-ip-guard.js";

// 地区偏移检测（IPSentinel）是 Server 内置功能：主控保存设置与任务，并通过独立隔离浏览器执行检测；
// Agent 只需本机出口许可。检测什么、在哪些节点上检测由「网络探测」里的地区偏移检测任务决定；
// 每台节点不同的部分（目标地区、出口地址族、精确定位）在「节点管理」里用这个弹窗设置。
// 关掉弹窗时回调 onClose，打开它的页面据此重读。
export function openRegionDialog(onClose, options = {}) {
  const d = modal(`地区偏移设置 · ${options.name || ""}`, `<section data-node-plugin-detail><p class="hint region-intro">这台节点的目标地区、出口地址族与精确定位。检测哪些服务、是否启用由「网络探测」里的地区偏移检测任务决定。</p><div data-plugin-workspace></div></section>`);
  d.classList.add("wide-dialog", "region-dialog");
  const workspace = d.querySelector("[data-plugin-workspace]");
  const closed = () => {
    d.removeEventListener("close", closed);
    d.classList.remove("wide-dialog", "region-dialog");
    workspace.disposeRegionGuard?.();
    onClose?.();
  };
  d.addEventListener("close", closed);
  const show = async () => {
    workspace.innerHTML = '<p role="status">正在读取出口与设置…</p>';
    try {
      await renderIPGuard(workspace, { node: options.node });
    } catch (error) {
      if (workspace.isConnected) {
        workspace.innerHTML = `<section class="panel"><p role="alert">加载失败：${esc(error.message)}</p><button>重新加载</button></section>`;
        workspace.querySelector("button").onclick = guard(show);
      }
    }
  };
  return show();
}
