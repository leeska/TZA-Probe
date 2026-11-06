import { api, send, guard, esc, modal } from "./api.js";

const activeStates = new Set(["requested", "awaiting_approval", "approved", "running"]);

export function nodePluginEligibility(channel, now = Date.now() / 1000) {
  if (!channel) return "未授权节点安装；需先在本机配置安装通道";
  if (!Number.isFinite(channel.seen_at) || now - channel.seen_at >= 45 || channel.seen_at > now + 5) return "安装通道已离线或过期，请刷新";
  if (!channel.enrollment?.scope_sha256) return "节点安装身份不完整";
  if (channel.requests?.some(request => activeStates.has(request.state))) return "已有安装请求，请先查看并处理";
  return "";
}

export async function choosePluginNodes(pkg, version, onManage) {
  const [nodes, channels] = await Promise.all([api("/admin/nodes"), api("/admin/runtime")]);
  const byNode = new Map(channels.map(channel => [channel.node_id, channel]));
  const dialog = modal(`安装到节点 · ${pkg.name}`, `<p>IPSentinel v${esc(version)} · 先生成计划，再批准下载。未授权的节点不会下载依赖或启动进程。</p><p class="hint">当前入口需要节点已配置本机安装通道；不自动授予系统权限。平台、Agent 版本、下载总量和权限由节点验签后列出。</p><form data-node-plugin-form><div class="node-plugin-targets">${nodes.map((node, index) => {
    const channel = byNode.get(node.id);
    const reason = nodePluginEligibility(channel);
    const host = channel?.enrollment?.host;
    return `<label class="node-plugin-target"><input type="checkbox" name="node" value="${index}" ${reason ? "disabled" : ""}><span><b>${esc(node.name || node.id)}</b><small>${esc(host ? `${host.platform} · Agent ${host.agent_version}` : "未配置安装通道")}</small><small data-node-plugin-result="${index}" role="status">${esc(reason || "可生成计划；不是已安装或已启用")}</small></span></label>`;
  }).join("") || '<p>还没有节点，请先接入 Agent。</p>'}</div><p class="hint">生成计划不会下载 Python 或 Chromium。在本插件「设置 → 节点安装与更新」核对实际大小、权限和精确摘要后再批准下载；安装、启动和托管授权仍需分别确认。</p><p data-node-plugin-summary role="status"></p><div class="form-footer node-plugin-actions"><button type="button" class="quiet" data-node-plugin-manage>查看插件设置</button><button type="submit" class="primary" disabled>为所选节点生成计划</button></div></form>`);
  const form = dialog.querySelector("[data-node-plugin-form]");
  const submit = form.querySelector("[type=submit]");
  const choices = [...form.querySelectorAll('input[name="node"]')];
  const completed = new Set();
  let busy = false;
  const refresh = () => {
    for (const choice of choices) {
      const index = Number(choice.value);
      const reason = nodePluginEligibility(byNode.get(nodes[index].id));
      choice.disabled = busy || completed.has(index) || Boolean(reason);
      if (reason && !completed.has(index)) {
        choice.checked = false;
        form.querySelector(`[data-node-plugin-result="${index}"]`).textContent = reason;
      }
    }
    submit.disabled = busy || !choices.some(choice => choice.checked && !choice.disabled);
  };
  form.onchange = refresh;
  const timer = setInterval(() => {
    if (!dialog.open || !form.isConnected) {
      clearInterval(timer);
      return;
    }
    refresh();
  }, 1000);
  dialog.addEventListener("close", () => clearInterval(timer), { once: true });
  dialog.querySelector("[data-node-plugin-manage]").onclick = guard(async () => {
    dialog.close();
    if (onManage) await onManage();
    else await (await import("./admin-node-plugin-detail.js")).openNodePluginDetail(pkg, { settings: true });
  });
  form.onsubmit = guard(async event => {
    event.preventDefault();
    if (busy) return;
    refresh();
    const selected = choices.filter(choice => choice.checked && !choice.disabled);
    if (!selected.length) return;
    busy = true;
    refresh();
    let failed = 0;
    try {
      for (const choice of selected) {
        if (!dialog.open || !form.isConnected) break;
        const index = Number(choice.value);
        const node = nodes[index];
        const output = form.querySelector(`[data-node-plugin-result="${index}"]`);
        try {
          const channel = byNode.get(node.id);
          const reason = nodePluginEligibility(channel);
          if (reason) throw new Error(reason);
          const request = await send("/admin/market/node-plans", "POST", {
            id: pkg.id, version, node_id: node.id, scope_sha256: channel.enrollment.scope_sha256,
          });
          completed.add(index);
          choice.checked = false;
          output.textContent = `已申请计划 · ${request.id}；等待节点生成并核对批准`;
        } catch (error) {
          failed++;
          choice.checked = false;
          output.textContent = `未确认成功：${error.message}。请先查看节点请求，避免重复操作。`;
        }
      }
      form.querySelector("[data-node-plugin-summary]").textContent = `已申请 ${completed.size} 个节点计划${failed ? `；本次 ${failed} 个未确认成功` : ""}。尚未安装或启用。`;
    } finally {
      busy = false;
      refresh();
    }
  });
}
