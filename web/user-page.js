// 使用者自己的用量页（/u/{token}）：只凭链接里的 token，不需要登录。显示状态、本期流量、
// 下次重置与到期时间。订阅链接在代理功能上线后加在这里。
import { api, esc } from "./api.js";
import { bytes } from "./format.js";
import { brandHTML } from "./brand.js";
import { themeButton } from "./theme.js";

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
  main.innerHTML = `<section class="panel user-card"><div class="user-head"><h1>${esc(u.name)}</h1><span class="pill ${tone}">${label}</span></div><div class="user-usage"><span>本期已用</span><strong>${bytes(u.traffic_used)}${limit ? `<small> / ${bytes(limit)}</small>` : "<small> · 不限流量</small>"}</strong>${limit ? `<progress max="100" value="${percent.toFixed(1)}" aria-label="已用 ${percent.toFixed(0)}%"></progress><small>剩余 ${bytes(Math.max(0, limit - u.traffic_used))}</small>` : ""}</div><dl class="user-facts"><div><dt>本期开始</dt><dd>${day(u.period_start)}</dd></div><div><dt>下次重置</dt><dd>${u.next_reset ? day(u.next_reset) : "不重置"}</dd></div><div><dt>到期</dt><dd>${u.expires_at ? esc(u.expires_at.slice(0, 10)) : "长期"}</dd></div></dl><p class="hint">订阅链接会在代理功能上线后显示在这里。这个页面的链接就是你的凭证，请不要分享给别人。</p></section>`;
}
