// 解锁检测面板的共用渲染。公开卡片的徽标 / 分类浮层与节点详情的「解锁检测」块都从这里出，
// 两处的分数、分类统计与服务列表因此永远同源。数据来自 /api/v1/nodes 的 unlock 字段
// （或 /api/v1/nodes/{id}/unlocks），目录（名称 / 分类 / 是否只报地区）来自
// /api/v1/unlock-catalog——目录是固定的 24 项，前后端共用同一份 id 与名称。
import { api, esc } from "./api.js";
import { date } from "./format.js";

// 状态词表与后端 model 的常量一一对应（yes / no / originals_only / banned / failed）。
export const unlockStateName = {
  yes: "已解锁",
  no: "未解锁",
  originals_only: "仅自制剧",
  banned: "IP 被封禁",
  failed: "检测失败",
};

// 计入分数的状态：已解锁与「仅自制剧」都算（与后端 UnlockResult.Unlocked 同一口径）。
export const unlockCounts = (status) =>
  status === "yes" || status === "originals_only";

// 绿色小锁：只有「已解锁」画它，颜色走 CSS 的 currentColor，深色主题也跟得上。
function lockIcon() {
  return `<svg class="unlock-lock" viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M4.6 7.4V5.2a3.4 3.4 0 0 1 6.8 0v2.2" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/><rect x="3" y="7.2" width="10" height="6.6" rx="2.2" fill="currentColor"/></svg>`;
}

let catalogPromise = null;
// 目录全站固定，取一次就缓存。失败时给一个空目录而不是抛错：徽标与分数照样显示，
// 只是没有分类与服务名——不把整张卡片拖垮。
export function unlockCatalog() {
  if (!catalogPromise)
    catalogPromise = api("/unlock-catalog").catch(() => ({
      categories: [],
      services: [],
    }));
  return catalogPromise;
}

// 一行服务右侧的状态。region_only 的项（Bing Region / Apple Region / Steam Currency …）
// 给的是地区或币种文字，不是解锁结论：只显示那段文字，既不写「未解锁」，也不画锁。
export function unlockStateCell(result, service) {
  if (service?.region_only) {
    const text = result.region || result.detail || "";
    return `<span class="unlock-state region" title="${esc(text)}">${esc(text)}</span>`;
  }
  const name = unlockStateName[result.status] || result.status || "";
  const icon = result.status === "yes" ? lockIcon() : "";
  return `<span class="unlock-state ${esc(result.status || "")}">${icon}<span>${esc(name)}</span></span>`;
}

// 每个分类的「已解锁 / 目录项数」。分母固定取目录里的项数（24 项分到四个分类），
// 所以节点只报了一部分时总分仍是 /24——与妙妙屋同一口径。
export function unlockCategoryStats(summary, catalog) {
  const have = new Map((summary?.services || []).map((r) => [r.service, r]));
  return (catalog?.categories || [])
    .map((category) => {
      const list = (catalog?.services || []).filter(
        (s) => s.category === category,
      );
      if (!list.length) return null;
      const unlocked = list.filter((s) =>
        unlockCounts(have.get(s.id)?.status),
      ).length;
      return { category, unlocked, total: list.length };
    })
    .filter(Boolean);
}

// 完整面板：标题行「解锁检测」+ 右侧分数，下面一行分类统计，再按分类列出服务。
// tested 为真时在末尾补一行检测时间（详情块要它，卡片浮层不要）。
export function unlockPanel(summary, catalog, { tested = false } = {}) {
  const score = summary?.score ?? 0;
  const total = summary?.total ?? (catalog?.services || []).length;
  const have = new Map((summary?.services || []).map((r) => [r.service, r]));
  const stats = unlockCategoryStats(summary, catalog);
  // 只列节点真报过的服务：没报的项不摆一行「未检测」占位（用户明确反感占位元素）。
  const groups = stats
    .map(({ category }) => {
      const rows = (catalog?.services || [])
        .filter((s) => s.category === category && have.has(s.id))
        .map(
          (s) =>
            `<li><span class="unlock-service">${esc(s.name)}</span>${unlockStateCell(have.get(s.id), s)}</li>`,
        )
        .join("");
      return rows
        ? `<div class="unlock-group" data-category="${esc(category)}"><h4>${esc(category)}</h4><ul>${rows}</ul></div>`
        : "";
    })
    .join("");
  const catLine = stats
    .map((s) => `${esc(s.category)} ${s.unlocked}/${s.total}`)
    .join(" · ");
  return `<div class="unlock-panel"><div class="unlock-head"><span class="unlock-title">解锁检测</span><strong class="unlock-score">${score}/${total}</strong></div>${catLine ? `<div class="unlock-cats">${catLine}</div>` : ""}${groups}${tested && summary?.tested_at ? `<div class="unlock-tested">检测于 ${esc(date(summary.tested_at))}</div>` : ""}</div>`;
}

// 公开卡片上的徽标：锁图标 + 分数。没有解锁数据的节点返回空串——不渲染任何占位元素。
export function unlockBadge(n) {
  const summary = n?.unlock;
  if (!summary || !summary.total) return "";
  const score = `${summary.score}/${summary.total}`;
  return `<button type="button" class="unlock-badge" data-unlock="${esc(n.id)}" aria-expanded="false" aria-label="${esc(n.name)} 解锁检测 ${score}" title="解锁检测 ${score}">${lockIcon()}<span class="unlock-badge-score">${score}</span></button>`;
}
