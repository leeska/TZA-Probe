// 后台总览：核心与各模块的卡片（GET /admin/overview），每张卡一个结论、一句说明和几条待处理项，
// 点卡片进对应页面。某一块读不到只影响那一张卡。
import { api, esc } from "./api.js";
import { content, go } from "./admin-state.js";

const toneClass = { good: "good", warn: "warn", bad: "bad" };

function cardHTML(card) {
  const items = (card.items || [])
    .map((i) => `<li class="${toneClass[i.tone] || ""}"><span>${esc(i.label)}</span><small>${esc(i.note || "")}</small></li>`)
    .join("");
  const tag = card.page ? "button" : "div";
  const attrs = card.page ? ` type="button" data-go="${esc(card.page)}" aria-label="${esc(card.title)}：${esc(card.value)}，打开${esc(card.title)}"` : "";
  return `<${tag} class="overview-card ${toneClass[card.tone] || ""}" data-card="${esc(card.id)}"${attrs}><span class="overview-card-title">${esc(card.title)}</span><strong>${esc(card.value)}</strong><small>${esc(card.note || "")}</small>${items ? `<ul>${items}</ul>` : ""}</${tag}>`;
}

export async function overviewPage(nodesReady) {
  const [, data] = await Promise.all([nodesReady, api("/admin/overview")]);
  const root = content();
  const time = new Date(data.generated_at * 1000).toLocaleTimeString("zh-CN", { hour12: false });
  root.innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">OVERVIEW</div><h1>总览</h1><p>各模块需要关注的事项，点卡片进入对应页面。更新于 ${esc(time)}。</p></div><div class="heading-actions"><button id="refresh-overview" class="quiet">↻ 刷新</button></div></section><div class="overview-cards">${data.cards.map(cardHTML).join("")}</div>`;
  root.querySelector("#refresh-overview").onclick = () => go("overview");
  root.querySelectorAll("[data-go]").forEach((b) => (b.onclick = () => go(b.dataset.go)));
}
