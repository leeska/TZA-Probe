import { esc, guard, modal } from "./api.js";
import { renderIPGuard } from "./admin-ip-guard.js";

// 地区偏移检测（IPSentinel）是 Server 内置功能：主控保存设置与任务，并通过独立隔离浏览器执行检测；
// Agent 只需本机出口许可。它属于「网络探测」：从任务列表的「创建任务 → 地区偏移检测」或某一行的
// 「编辑」打开这个弹窗，按节点设置（每台节点有自己的出口、目标地区与定位）。关掉弹窗时回调
// onClose，任务列表据此重读那几行。
export function openRegionDialog(onClose) {
  const d = modal("地区偏移检测", '<section data-node-plugin-detail><p class="hint region-intro">按节点设置：选节点，开启检测并选目标地区；查看 Google、Meta 识别的地区，偏移时可尝试校正并独立复测。</p><div data-plugin-workspace></div></section>');
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
  return show();
}
