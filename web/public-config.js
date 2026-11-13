// 前台区块开关与「需要登录」：由 /theme-default.js 写在 window.__tzaPublic（见 httpapi/public.go）。
// 访客拿到的数据服务端已经过滤；管理员登录后打开前台拿到的是全量数据，这里按同一规则去掉，
// 让管理员看到的版面与访客一致。
const config = (typeof window !== "undefined" && window.__tzaPublic) || {};

export const sectionHidden = (id) => Array.isArray(config.hidden) && config.hidden.includes(id);
export const loginRequired = () => config.login === true;

// 与服务端 publicPolicy.strip 一一对应。
export function applyHidden(node) {
  if (sectionHidden("network")) {
    node.probe_targets = [];
    node.display = { route_region: "", latency_region: "" };
  }
  if (sectionHidden("unlock")) node.unlock = null;
  if (sectionHidden("billing")) {
    const { reset_at, traffic_limit, traffic_mode } = node.billing || {};
    node.billing = { reset_at, traffic_limit, traffic_mode, price: 0, currency: "", price_period: "", expires_at: "" };
    node.remaining_value = null;
  }
  return node;
}

// 可关的区块，与 model.PublicSections 同序同 id（后台「面板设置 → 外观与显示」用）。
export const PUBLIC_SECTIONS = [
  { id: "network", label: "延迟与回程", note: "卡片上的延迟色带、三网回程，以及详情里的网络历史" },
  { id: "unlock", label: "解锁检测", note: "流媒体与 AI 服务的解锁结果" },
  { id: "billing", label: "价格与到期", note: "价格、到期时间、剩余价值，以及顶部的成本汇总" },
];
