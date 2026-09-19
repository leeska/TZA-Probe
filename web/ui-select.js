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
  shell.append(trigger, pop);

  const sync = () => {
    const opt = select.selectedOptions[0];
    trigger.querySelector(".select-value").textContent = opt ? opt.textContent : "";
  };
  const open = () => {
    closeAll(shell);
    pop.innerHTML = [...select.options]
      .map(
        (o, i) =>
          `<button type="button" class="select-opt${o.selected ? " selected" : ""}" data-i="${i}">${esc(o.textContent)}${CHECK}</button>`,
      )
      .join("");
    pop.scrollTop = 0;
    shell.classList.add("open");
  };
  const close = () => shell.classList.remove("open");

  trigger.addEventListener("click", (e) => {
    e.stopPropagation();
    shell.classList.contains("open") ? close() : open();
  });
  pop.addEventListener("click", (e) => {
    const b = e.target.closest(".select-opt");
    if (!b) return;
    select.selectedIndex = Number(b.dataset.i);
    select.dispatchEvent(new Event("change", { bubbles: true }));
    sync();
    close();
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
