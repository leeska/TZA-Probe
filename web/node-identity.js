import { esc } from "./api.js";
import { date, osLabel, statusLabel } from "./format.js";
import { DISTRO_LOGOS } from "./distro-logos.js";

// Match specific derivatives before their parent distribution.
const DISTROS = [
  [/almalinux|alma\s*linux/i, "almalinux"], [/rocky/i, "rockylinux"],
  [/kubuntu/i, "kubuntu"], [/lubuntu/i, "lubuntu"], [/xubuntu/i, "xubuntu"],
  [/ubuntu\s*mate/i, "ubuntumate"], [/ubuntu/i, "ubuntu"],
  [/debian/i, "debian"], [/alpine/i, "alpinelinux"], [/arch/i, "archlinux"],
  [/centos/i, "centos"], [/fedora/i, "fedora"], [/red\s*hat|rhel/i, "redhat"],
  [/open\s*suse|suse/i, "opensuse"], [/gentoo/i, "gentoo"], [/nix\s*os/i, "nixos"],
  [/kali/i, "kalilinux"], [/linux\s*mint|^mint\b/i, "linuxmint"],
  [/freebsd/i, "freebsd"], [/openbsd/i, "openbsd"], [/void/i, "voidlinux"],
  [/asahi/i, "asahilinux"], [/darwin|macos|mac os/i, "apple"], [/linux/i, "linux"],
];
const COMPUTER = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" focusable="false"><rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4"/></svg>';
export function distroBadge(n) {
  const label = osLabel(n, "系统未报告");
  const match = DISTROS.find(([pattern]) => pattern.test(label));
  const fallback = n.metrics?.os || n.agent_status?.os || "";
  const slug = match?.[1] || (fallback === "linux" ? "linux" : "");
  return `<span class="os-tag" role="img" aria-label="${esc(label)}" title="${esc(label)}" data-distro="${slug || "unknown"}">${DISTRO_LOGOS[slug] || COMPUTER}</span>`;
}

// Longer Chinese aliases win (e.g. 新西兰 before 荷兰); Latin aliases match whole words.
const REGIONS = [
  ["HK", "香港", "hong kong"], ["MO", "澳门", "macao", "macau"], ["TW", "台湾", "taiwan", "taipei"],
  ["JP", "日本", "东京", "大阪", "japan", "tokyo", "osaka"],
  ["KR", "韩国", "首尔", "korea", "seoul"], ["SG", "新加坡", "singapore"],
  ["MY", "马来西亚", "malaysia"], ["TH", "泰国", "thailand", "bangkok"],
  ["VN", "越南", "vietnam"], ["PH", "菲律宾", "philippines"],
  ["ID", "印度尼西亚", "印尼", "indonesia"], ["IN", "印度", "india"],
  ["AE", "阿联酋", "迪拜", "united arab emirates", "dubai", "uae"],
  ["IL", "以色列", "israel"], ["TR", "土耳其", "turkey", "türkiye"],
  ["AU", "澳大利亚", "澳洲", "悉尼", "australia", "sydney"], ["NZ", "新西兰", "new zealand"],
  ["US", "美国", "洛杉矶", "纽约", "圣何塞", "西雅图", "达拉斯", "united states", "usa", "los angeles", "new york", "san jose", "seattle", "dallas"],
  ["CA", "加拿大", "canada", "toronto", "vancouver"], ["MX", "墨西哥", "mexico"], ["BR", "巴西", "brazil"],
  ["GB", "英国", "伦敦", "united kingdom", "great britain", "uk", "london"], ["IE", "爱尔兰", "ireland"],
  ["FR", "法国", "巴黎", "france", "paris"], ["DE", "德国", "法兰克福", "germany", "frankfurt", "berlin"],
  ["NL", "荷兰", "netherlands", "amsterdam"], ["BE", "比利时", "belgium"],
  ["CH", "瑞士", "switzerland"], ["AT", "奥地利", "austria"], ["SE", "瑞典", "sweden"],
  ["NO", "挪威", "norway"], ["DK", "丹麦", "denmark"], ["FI", "芬兰", "finland"],
  ["IT", "意大利", "italy"], ["ES", "西班牙", "spain"], ["PT", "葡萄牙", "portugal"],
  ["PL", "波兰", "poland"], ["CZ", "捷克", "czechia", "czech republic"],
  ["RU", "俄罗斯", "russia", "moscow"], ["ZA", "南非", "south africa"],
  ["CN", "中国", "大陆", "北京", "上海", "天津", "重庆", "河北", "山西", "辽宁", "吉林", "黑龙江", "江苏", "浙江", "安徽", "福建", "江西", "山东", "河南", "湖北", "湖南", "广东", "广西", "海南", "四川", "贵州", "云南", "西藏", "陕西", "甘肃", "青海", "宁夏", "新疆", "内蒙古", "深圳", "广州", "成都", "杭州", "武汉", "南京", "西安", "china", "beijing", "shanghai", "shenzhen", "guangzhou"],
];
const ALIASES = REGIONS.flatMap(([code, ...names]) => names.map((name) => [code, name])).sort((a, b) => b[1].length - a[1].length);
export function regionFlag(region) {
  const text = String(region || "").trim();
  const existing = text.match(/[\u{1F1E6}-\u{1F1FF}]{2}/u)?.[0];
  let code;
  // A configured code (US / US-LAX / JP Tokyo) is stronger than a city alias.
  const prefix = text.match(/^([a-z]{2})(?:$|[\s/·_-])/i)?.[1].toUpperCase();
  if (REGIONS.some(([id]) => id === prefix)) code = prefix;
  if (!code) code = ALIASES.find(([, name]) => /[^\x00-\x7F]/.test(name)
    ? text.includes(name)
    : new RegExp(`(?:^|[^a-z])${name}(?:$|[^a-z])`, "i").test(text))?.[0];
  const flag = existing || (code ? String.fromCodePoint(...[...code].map((c) => c.charCodeAt(0) + 127397)) : "🌐");
  const label = text || "地区未设置";
  return `<span class="node-flag" role="img" aria-label="${esc(label)}" title="${esc(label)}">${flag}</span>`;
}

export const offlineTitle = (n) => n.last_seen ? `最近连接 ${date(n.last_seen)}` : "尚未接入";
export function offlineBadge(n) {
  return `<span class="node-state" role="status" title="${esc(offlineTitle(n))}"${n.online ? " hidden" : ""}>${n.online ? "离线" : esc(statusLabel(n))}</span>`;
}
