const activeStates = new Set(["requested", "awaiting_approval", "approved", "running"]);

export function nodePluginEligibility(channel, now = Date.now() / 1000) {
  if (!channel) return "需要管理员先允许此 Agent 安装组件";
  if (!Number.isFinite(channel.seen_at) || now - channel.seen_at >= 45 || channel.seen_at > now + 5) return "Agent 安装状态已过期，请刷新";
  if (!channel.enrollment?.scope_sha256) return "Agent 安装许可不完整，请管理员核对";
  if (channel.requests?.some(request => activeStates.has(request.state))) return "已有操作处理中，请先查看并处理";
  return "";
}
