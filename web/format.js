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
// 磁盘与内存按 1024 换算（KiB / MiB / GiB）：商家按 GiB 标称、`df -h` / `free -h` 也按 GiB 报，
// 用十进制写出来会大 7.4%——100 GiB 的盘写成 105 GB、9.7 GiB 写成 10.4 GB，与规格和 df 都对不上。
// 流量仍走 bytes() 的十进制：商家的流量口径本来就是十进制（1 TB = 1e12），两套单位是两种口径，
// 不是不一致——卡片上「7.8 GiB」是内存、「9.6 GB」是流量，单位本身就把口径写清楚了。
export function gib(n = 0) {
  if (!Number.isFinite(n)) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  while (Math.abs(n) >= 1024 && i < 4) {
    n /= 1024;
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
// 节点状态的统一叫法：从没上报过的是「等待接入」，接入过又断开的才是「离线」。
// 公开卡片、详情弹窗与后台表格共用这一处，同一台机器不会这里写等待接入、那里写离线。
export function statusLabel(n) {
  if (n.online) return "在线";
  return n.last_seen ? "离线" : "等待接入";
}
// 系统标签的取数：Agent 读 /etc/os-release 报的发行版（distro，如「Debian 13」），
// 老 Agent 只有 runtime.GOOS（linux）——发行版本身已经说明它是 Linux，所以有 distro
// 就不再写 linux。卡片 / 列表 / 详情页共用这一处，三个渲染点不会各退各的。
export function osLabel(n, fallback = statusLabel(n)) {
  return n.metrics?.distro || n.metrics?.os || fallback;
}
// 带宽：接口给的窗口平均速率是 bit/s，按商家规格的口径写成 Mbit/s（十进制），
// 不足 1 Mbit/s 退到 kbit/s，别写成 0.0 Mbit/s。
export function mbps(bits) {
  if (!Number.isFinite(bits)) return "—";
  if (bits <= 0) return "0 bit/s";
  if (bits >= 1e6) return `${(bits / 1e6).toFixed(bits < 1e7 ? 2 : 1)} Mbit/s`;
  if (bits >= 1e3) return `${(bits / 1e3).toFixed(0)} kbit/s`;
  return `${bits.toFixed(0)} bit/s`;
}
// 线路带宽（后台填的 Mbit/s）：整千写 Gbit/s、其余原样 Mbit/s——商家页面写「1 Gbps / 1000 Mbit/s」
// 两种写法都有，整千的用 Gbit/s 更好读，非整千的（1536 / 200）别硬凑成小数 Gbit/s。
// 0 或没填返回空串，调用方据此整块不渲染（面板里不放「未标注」这类占位）。
export function bandwidth(lineMbps) {
  const value = Number(lineMbps);
  if (!Number.isFinite(value) || value <= 0) return "";
  if (value >= 1000) {
    const gbit = value / 1000;
    return `${Number.isInteger(gbit) ? gbit : gbit.toFixed(1)} Gbit/s`;
  }
  return `${value} Mbit/s`;
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
  throttle: "降速警告",
  load: "负载警告", // 旧事件：负载规则已拆成 CPU 与内存两条
  cpu: "CPU 警告",
  memory: "内存警告",
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
