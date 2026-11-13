// 订阅链接：同一个 token，按格式给出地址和一键导入链接（后台与使用者页共用）。
// 服务端也会按客户端的 User-Agent 自动选格式，所以「通用」地址适合大多数客户端。
import { esc } from "./api.js";

export const subscriptionURL = (token, format = "") => `${location.origin}/api/v1/sub/${encodeURIComponent(token)}${format ? `?format=${format}` : ""}`;

export function subscriptionItems(token, name) {
  const title = `TZA · ${name}`;
  const clash = subscriptionURL(token, "clash");
  const singbox = subscriptionURL(token, "singbox");
  return [
    { label: "自动识别", note: "客户端是 Clash、sing-box 还是其他，服务端自己判断", url: subscriptionURL(token) },
    { label: "Clash / mihomo", note: "Clash Verge、FlClash、Stash 等", url: clash, open: `clash://install-config?url=${encodeURIComponent(clash)}&name=${encodeURIComponent(title)}` },
    { label: "sing-box", note: "SFA、SFI、Hiddify 等；xray 内核的 Reality 节点连不上 sing-box 客户端，不在其中", url: singbox, open: `sing-box://import-remote-profile?url=${encodeURIComponent(singbox)}#${encodeURIComponent(title)}` },
    { label: "通用链接（base64）", note: "v2rayN、v2rayNG、Shadowrocket 等", url: subscriptionURL(token, "base64") },
  ];
}

export function subscriptionHTML(token, name) {
  return `<ul class="sub-links">${subscriptionItems(token, name).map((s) => `<li><div><strong>${esc(s.label)}</strong><small>${esc(s.note)}</small></div><div class="command-block"><pre>${esc(s.url)}</pre><button type="button" class="primary" data-copy="${esc(s.url)}">复制</button>${s.open ? `<a class="button" href="${esc(s.open)}">导入</a>` : ""}</div></li>`).join("")}</ul>`;
}

export function wireCopy(root, toast) {
  root.querySelectorAll("[data-copy]").forEach((b) => (b.onclick = () => navigator.clipboard?.writeText(b.dataset.copy).then(() => toast?.("已复制"), () => toast?.("复制失败，请手动选择"))));
}
