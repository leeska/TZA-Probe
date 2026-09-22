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
// 表单里的 UTC 时刻统一写成「YYYY-MM-DD HH:MM:SS」，与接口的 RFC3339 互转。
function pad(n) {
  return String(n).padStart(2, "0");
}
export function stamp(value) {
  if (!value) return "";
  const at = new Date(typeof value === "number" ? value * 1000 : value);
  if (Number.isNaN(at.getTime())) return "";
  return `${at.getUTCFullYear()}-${pad(at.getUTCMonth() + 1)}-${pad(at.getUTCDate())} ${pad(at.getUTCHours())}:${pad(at.getUTCMinutes())}:${pad(at.getUTCSeconds())}`;
}
export function stampISO(text) {
  const m = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})(?::(\d{2}))?Z?$/.exec(
    String(text ?? "").trim(),
  );
  if (!m) throw new Error("时间格式应为 YYYY-MM-DD HH:MM:SS（UTC）");
  const [, y, mo, d, h, mi, s = "00"] = m;
  const at = new Date(`${y}-${mo}-${d}T${h}:${mi}:${s}Z`);
  // 2 月 31 日这类溢出会被 Date 顺延到下个月，必须挡掉而不是悄悄改日期。
  if (
    Number.isNaN(at.getTime()) ||
    at.getUTCMonth() + 1 !== Number(mo) ||
    at.getUTCDate() !== Number(d) ||
    at.getUTCHours() !== Number(h) ||
    at.getUTCMinutes() !== Number(mi)
  ) {
    throw new Error("时间不存在，请检查日期与时刻");
  }
  return `${y}-${mo}-${d}T${h}:${mi}:${s}Z`;
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
  tool_missing: "回程工具不可用",
  tool_failed: "探测工具执行失败",
  timeout: "探测超时",
  connect_failed: "TCP 连接失败",
};
