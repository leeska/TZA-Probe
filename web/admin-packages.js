import { guard, modal, esc } from "./api.js";
import { state } from "./admin-state.js";
// 主题页与插件页共用的小部件：类型名、插件小牌子、色样、翻页条，以及插件装之前的权限确认。
const KINDS = { notify: "通知渠道", targets: "探测目标集" };

export const kindName = (k) => KINDS[k] || "插件";
// 插件没有色样和封面：列表里画一枚写着类型的小牌子。
export const glyph = (kind) => `<span class="plugin-glyph" aria-hidden="true">${kind === "targets" ? "目标集" : "通知"}</span>`;
const permList = (perms) => (perms?.length ? `<ul class="plugin-perms">${perms.map((x) => `<li>${esc(x)}</li>`).join("")}</ul>` : "");
export const swatch = (colors) =>
  `<span class="skin-swatch" data-colors="${esc((colors || []).join(","))}" aria-hidden="true"><i></i><i></i><i></i></span>`;

// 翻页条：只有一页时不出现。attr 是按钮上带目标页码的 data 属性名；total 知道时写「第 n / 共 m 页」。
export function pager(page, hasNext, attr, total = 0) {
  if (page <= 1 && !hasNext) return "";
  return `<nav class="catalog-toolbar list-pager" aria-label="翻页"><button type="button" class="quiet" data-${attr}="${page - 1}" ${page > 1 ? "" : "disabled"}>上一页</button><span>第 ${page}${total ? ` / ${total}` : ""} 页</span><button type="button" class="quiet" data-${attr}="${page + 1}" ${hasNext ? "" : "disabled"}>下一页</button></nav>`;
}

// 插件装之前先列出它能做的事，管理员确认后才装。
export function confirmPlugin(p, action, label) {
  const changed = p.previousPermissions ? (p.permissions || []).filter((permission) => !p.previousPermissions.includes(permission)) : [];
  const d = modal(
    `安装插件 · ${p.name}`,
    `<p>${esc(kindName(p.kind))}${p.version ? ` · v${esc(p.version)}` : ""}${p.author ? ` · ${esc(p.author?.name || p.author)}` : ""}。这个插件能做的事：</p>${permList(p.permissions) || '<p class="muted">插件没有声明任何能力。</p>'}${changed.length ? `<div class="notice"><strong>本版本有变化的权限</strong>${permList(changed)}</div>` : ""}<p class="hint">插件只含声明式的配置与模板，由 Probe 解释执行，不含任何代码。${p.kind === "notify" ? "装好后要在「告警与通知 → 配置出口」选用它才会发通知。" : "装好后这些目标出现在新建任务的目标目录里，不会自动建任务。"}</p><div class="form-footer"><button class="primary" id="confirm-plugin">${esc(label)}</button></div>`,
  );
  d.querySelector("#confirm-plugin").onclick = guard(async (e) => {
    e.currentTarget.disabled = true;
    try {
      await action();
    } finally {
      e.target.disabled = false;
    }
  });
}

export function pluginToast(m) {
  // 目标集改了任务目录：后台缓存的目录作废，下次打开任务表单重新读。
  state.catalog = null;
  return m.kind === "targets"
    ? `目标集已安装：${m.name}（新建任务时在目标目录里能找到）`
    : `通知插件已安装：${m.name}（到「告警与通知 → 配置出口」选用它）`;
}

// 色样：CSP 的 style-src 'self' 会丢掉 HTML 里的 style 属性，经 CSSOM 写的不受限。
export function paint(root) {
  for (const box of root.querySelectorAll("[data-colors]")) {
    const colors = box.dataset.colors.split(",");
    box.querySelectorAll("i").forEach((cell, i) => {
      if (/^#[0-9a-fA-F]{6}$/.test(colors[i] || "")) cell.style.background = colors[i];
    });
  }
}
