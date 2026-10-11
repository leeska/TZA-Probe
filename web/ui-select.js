// 自定义下拉：把原生 <select> 包装成玻璃风格触发器 + 弹层，
// 真实 select 保留在表单里（FormData / change 事件兼容），仅隐藏视觉。
import { esc } from "./api.js";

const SELECTOR = "select:not([multiple]):not([data-enhanced])";
const CHEVRON = `<svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true"><path d="M3 4.5l3 3 3-3" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>`;
const CHECK = `<svg class="ck" width="13" height="13" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M5.5 12.5l4.4 4.4L18.5 8" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/></svg>`;

function closeAll(except) {
  document.querySelectorAll(".select-shell.open").forEach((s) => {
    if (s !== except) s.classList.remove("open");
  });
}

/** 弹层能画到哪：视口与所有可滚动祖先的**交集**。后台的编辑弹窗是 overflow:auto 的
 *  滚动容器，裁切发生在它身上而不是视口（实测「上传 / 下载」那格下方只剩 138px，
 *  弹层要 174px，只按视口算就会开在被裁掉的位置）。 */
function clipBounds(el) {
  let top = 0;
  let bottom = window.innerHeight;
  for (let p = el.parentElement; p; p = p.parentElement) {
    if (!p.clientHeight) continue;
    if (!/(auto|scroll|hidden)/.test(getComputedStyle(p).overflowY)) continue;
    const r = p.getBoundingClientRect();
    top = Math.max(top, r.top);
    bottom = Math.min(bottom, r.bottom);
  }
  return { top, bottom };
}

function enhance(select) {
  if (select.dataset.enhanced) return;
  select.dataset.enhanced = "1";
  select.tabIndex = -1;
  const shell = document.createElement("span");
  shell.className = "select-shell";
  select.before(shell);
  shell.append(select);
  const trigger = document.createElement("button");
  trigger.type = "button";
  trigger.className = "select-trigger";
  trigger.setAttribute("aria-haspopup", "listbox");
  trigger.innerHTML = `<span class="select-value"></span>${CHEVRON}`;
  const pop = document.createElement("div");
  pop.className = "select-pop";
  pop.setAttribute("role", "listbox");
  shell.append(trigger, pop);

  const sync = () => {
    const opt = select.selectedOptions[0];
    trigger.querySelector(".select-value").textContent = opt ? opt.textContent : "";
  };
  // 选项是 <div role="option">，不是 <button>：按钮基样式里有 `.webhook-actions button`
  // （0,1,1）这种按容器命中的规则，它压过 glass.css 的 .select-opt（0,1,0），会把选项改成
  // 白底 inline-flex 按钮**横排成一行**，弹层只剩一行高、后面的选项溢出被面板裁掉
  // （后台「面板设置 → 默认外观」实测三个选项 x=1008/1271/1534）。换成 div 就不吃那套基样式。
  const pick = (b) => {
    select.selectedIndex = Number(b.dataset.i);
    select.dispatchEvent(new Event("change", { bubbles: true }));
    sync();
    close();
  };
  const open = () => {
    closeAll(shell);
    pop.innerHTML = [...select.options]
      .map(
        (o, i) =>
          `<div class="select-opt${o.selected ? " selected" : ""}" role="option" aria-selected="${o.selected}" data-i="${i}" tabindex="0">${esc(o.textContent)}${CHECK}</div>`,
      )
      .join("");
    pop.scrollTop = 0;
    shell.classList.add("open");
    trigger.setAttribute("aria-expanded", "true");
    // 视口（或弹窗）下沿放不下就往上开；两边都不够高时把自己压到可用高度，让弹层内部
    // 滚动承担——手机上面板里的控件常落在下半屏，往下开会掉到折线以下（实测 390px 宽时
    // 弹层底到 930px，视口只有 900px）。
    const bounds = clipBounds(shell);
    const rect = trigger.getBoundingClientRect();
    const below = bounds.bottom - rect.bottom;
    const above = rect.top - bounds.top;
    pop.style.maxHeight = "";
    const natural = pop.getBoundingClientRect().height;
    const flip = natural + 12 > below && above > below;
    pop.classList.toggle("pop-up", flip);
    const avail = (flip ? above : below) - 12;
    if (natural > avail) pop.style.maxHeight = `${Math.max(120, Math.round(avail))}px`;
  };
  const close = () => {
    shell.classList.remove("open");
    trigger.setAttribute("aria-expanded", "false");
  };

  trigger.setAttribute("aria-expanded", "false");
  trigger.addEventListener("click", (e) => {
    e.stopPropagation();
    shell.classList.contains("open") ? close() : open();
  });
  pop.addEventListener("click", (e) => {
    const b = e.target.closest(".select-opt");
    if (b) pick(b);
  });
  // 选项不再是 button，回车/空格没有默认激活行为，键盘这一路要自己接（与原来的 Tab + Enter 等价）。
  pop.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    const b = e.target.closest(".select-opt");
    if (!b) return;
    e.preventDefault();
    pick(b);
  });
  sync();
  // 选项列表被外部重建时同步触发器文字
  new MutationObserver(sync).observe(select, { childList: true });
}

function sweep(root) {
  root.querySelectorAll(SELECTOR).forEach(enhance);
}

new MutationObserver((muts) => {
  for (const m of muts) {
    for (const n of m.addedNodes) {
      if (n.nodeType !== 1) continue;
      if (n.matches(SELECTOR)) enhance(n);
      else sweep(n);
    }
  }
}).observe(document.body, { childList: true, subtree: true });

document.addEventListener("click", (e) => {
  document.querySelectorAll(".select-shell.open").forEach((s) => {
    if (!s.contains(e.target)) s.classList.remove("open");
  });
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") closeAll();
});

sweep(document);
