export function bytes(n = 0) {
  if (!Number.isFinite(n)) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (Math.abs(n) >= 1000 && i < 4) {
    n /= 1000;
    i++;
  }
  return `${n.toFixed(i && n < 100 ? 1 : 0)} ${units[i]}`;
}
export function date(value) {
  if (!value) return "—";
  return new Date(
    typeof value === "number" ? value * 1000 : value,
  ).toLocaleString("zh-CN", { hour12: false });
}
export function days(value) {
  if (!value) return null;
  return Math.ceil((new Date(value) - Date.now()) / 86400000);
}
export function percent(used, total) {
  return total > 0 ? Math.min(100, (used / total) * 100) : 0;
}
export const statusName = {
  ok: "正常",
  pending: "检测中",
  timeout: "超时",
  unavailable: "工具未配置",
  failed: "检测失败",
};
export const kindName = {
  offline: "离线提醒",
  renewal: "续费提醒",
  traffic: "流量警告",
  load: "负载警告",
  test: "通知测试",
};

export const reasonName = {
  synthetic_dns: "DNS 返回代理虚拟地址，请使用真实目标 IP",
  dns: "目标地址解析失败",
  permission: "探测权限不足",
  tool_missing: "未配置 NextTrace",
  tool_failed: "探测工具执行失败",
  timeout: "探测超时",
  connect_failed: "TCP 连接失败",
};
