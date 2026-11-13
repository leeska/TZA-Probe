// 使用者自己的用量页（/u/{token}）：只凭链接里的 token，不需要登录。显示状态、本期流量、
// 下次重置与到期时间，以及各客户端的订阅链接。
import { api, esc } from "./api.js";
import { bytes } from "./format.js";
import { brandHTML } from "./brand.js";
import { themeButton } from "./theme.js";
import { subscriptionHTML, wireCopy } from "./subscription.js";

const STATUS = { active: ["可用", "good"], disabled: ["已停用", ""], expired: ["已过期", "bad"], exceeded: ["本期流量已用完", "bad"] };

export async function mountUserPage(app, token) {
  app.innerHTML = `<header data-part="public-header" class="public-header">${brandHTML()}<nav>${themeButton()}</nav></header><main class="user-page"><p role="status">正在读取…</p></main>`;
  const main = app.querySelector("main");
  let u;
  try {
    u = await api(`/u/${encodeURIComponent(token)}`);
  } catch (error) {
    main.innerHTML = `<section class="panel user-card"><h1>链接无效</h1><p>这个链接不存在或已更换，请向管理员要新的链接。</p></section>`;
    return;
  }
  const [label, tone] = STATUS[u.status] || [u.status, ""];
  const limit = u.traffic_limit;
  const percent = limit ? Math.min(100, (u.traffic_used / limit) * 100) : 0;
  const day = (unix) => new Date(unix * 1000).toLocaleDateString("zh-CN");
  main.innerHTML = `<section class="panel user-card"><div class="user-head"><h1>${esc(u.name)}</h1><span class="pill ${tone}">${label}</span></div><div class="user-usage"><span>本期已用</span><strong>${bytes(u.traffic_used)}${limit ? `<small> / ${bytes(limit)}</small>` : "<small> · 不限流量</small>"}</strong>${limit ? `<progress max="100" value="${percent.toFixed(1)}" aria-label="已用 ${percent.toFixed(0)}%"></progress><small>剩余 ${bytes(Math.max(0, limit - u.traffic_used))}</small>` : ""}</div><dl class="user-facts"><div><dt>本期开始</dt><dd>${day(u.period_start)}</dd></div><div><dt>下次重置</dt><dd>${u.next_reset ? day(u.next_reset) : "不重置"}</dd></div><div><dt>到期</dt><dd>${u.expires_at ? esc(u.expires_at.slice(0, 10)) : "长期"}</dd></div></dl><p class="hint">上传 ${bytes(u.traffic_up || 0)} · 下载 ${bytes(u.traffic_down || 0)}，每分钟更新。</p></section><section class="panel user-card"><h2>订阅</h2><p class="hint">选和你的客户端对应的一条，复制后在客户端里「从 URL 导入订阅」，或直接点「导入」。这个页面和订阅链接都是你的凭证，请不要分享给别人。</p>${subscriptionHTML(token, u.name)}</section>`;
  wireCopy(main, (text) => {
    const status = main.querySelector("[data-copied]") || Object.assign(document.createElement("p"), { role: "status" });
    status.dataset.copied = "";
    status.className = "hint";
    status.textContent = text;
    main.append(status);
  });
}
