// 后台的地区选择器：中文、全拼、拼音首字母和英文代号都能检索——与公开面板的目录
// 搜索是同一套匹配规则（web/search.js），所以「xg / hongkong / 香港 / hk」都能命中。
//
// 以前是 <input list="region-options"> + <datalist>：浏览器只按字面包含匹配，
// 打「xg」「hongkong」什么都搜不出来，只能一行行滚动建议表。
//
// 控件本身仍是**一个真正的 <input name="…">**：表单取值、回填、目录外自由填写
//（「节点所在地区」允许写任意城市）与原来完全一样，这里只是给它配了一份可检索的
// 建议弹层。外壳沿用 .select-shell / .select-pop / .select-opt，四套皮肤各有一份
// 样式，不必再给 .combo 补第二套外观。
import { esc } from "./api.js";
import { searchMatch } from "./search.js";

const MAX_OPTIONS = 300;

// label / name / value / options 与旧的 regionSelect 一致，多一个 placeholder：
// 空值在「代表地区 / 延迟地区」上是有含义的（沿用代表地区），所以文案写在占位符里。
export function regionCombobox(
  label,
  name,
  value,
  options,
  { placeholder = "选择或输入地区" } = {},
) {
  const pool = [];
  for (const option of options || []) {
    const region = String(option ?? "").trim();
    if (region && !pool.includes(region)) pool.push(region);
  }
  // 节点自己的值再并一次：目录外、已废弃的地区不能因为换控件而被清掉。
  if (value && !pool.includes(value)) pool.push(value);
  const items = pool
    .slice(0, MAX_OPTIONS)
    .map(
      (region) =>
        `<button type="button" class="select-opt combo-opt${region === value ? " selected" : ""}" data-region-option="${esc(region)}" role="option" aria-selected="${region === value}">${esc(region)}</button>`,
    )
    .join("");
  return `<label>${label}<span class="select-shell combo" data-combo><input class="select-trigger combo-input" name="${name}" type="text" value="${esc(value)}" placeholder="${esc(placeholder)}" autocomplete="off" spellcheck="false" role="combobox" aria-autocomplete="list" aria-expanded="false" maxlength="60"><button type="button" class="combo-clear" aria-label="清除地区" title="清除"${value ? "" : " hidden"}>×</button><div class="select-pop" role="listbox" aria-label="地区建议">${items}<p class="combo-empty" hidden>没有匹配的地区，可直接输入</p></div></span></label>`;
}

// 表单建好之后调一次：把弹层里的按钮、键盘与「点外面关掉」接上。
export function wireComboboxes(root) {
  root.querySelectorAll("[data-combo]").forEach(wire);
}

function wire(box) {
  const input = box.querySelector(".combo-input");
  const pop = box.querySelector(".select-pop");
  const clear = box.querySelector(".combo-clear");
  const note = pop.querySelector(".combo-empty");
  const opts = [...pop.querySelectorAll("[data-region-option]")];
  let active = -1;

  const visible = () => opts.filter((option) => !option.hidden);
  const mark = (region) => {
    for (const option of opts) {
      const on = option.dataset.regionOption === region;
      option.classList.toggle("selected", on);
      option.setAttribute("aria-selected", String(on));
    }
  };
  const close = () => {
    box.classList.remove("open");
    input.setAttribute("aria-expanded", "false");
    active = -1;
    opts.forEach((option) => option.classList.remove("active"));
  };
  const setActive = (index) => {
    const list = visible();
    opts.forEach((option) => option.classList.remove("active"));
    active = list.length ? (index + list.length) % list.length : -1;
    const option = list[active];
    if (!option) return;
    option.classList.add("active");
    // 滚动到当前项：offsetTop 系数值在 jsdom 里都是 0，测试环境自然变成空操作。
    const top = option.offsetTop,
      bottom = top + option.offsetHeight;
    if (top < pop.scrollTop) pop.scrollTop = top;
    else if (bottom > pop.scrollTop + pop.clientHeight) pop.scrollTop = bottom - pop.clientHeight;
  };
  const filter = (query) => {
    let hits = 0;
    for (const option of opts) {
      const match = !query || searchMatch(query, option.dataset.regionOption);
      option.hidden = !match;
      if (match) hits += 1;
    }
    note.hidden = hits > 0;
    pop.scrollTop = 0;
    setActive(-1);
  };
  const open = (fresh) => {
    filter(fresh ? input.value.trim() : "");
    box.classList.add("open");
    input.setAttribute("aria-expanded", "true");
  };
  const pick = (region) => {
    input.value = region;
    clear.hidden = false;
    mark(region);
    close();
    input.dispatchEvent(new Event("change", { bubbles: true }));
  };

  input.addEventListener("focus", () => open(false));
  input.addEventListener("input", () => {
    clear.hidden = !input.value;
    open(true);
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!box.classList.contains("open")) open(false);
      setActive(active + (e.key === "ArrowDown" ? 1 : -1));
      return;
    }
    if (e.key === "Enter") {
      const list = visible();
      if (box.classList.contains("open") && list[active]) {
        e.preventDefault();
        pick(list[active].dataset.regionOption);
      }
      return;
    }
    if (e.key === "Escape") close();
  });
  // 弹层里的 mousedown 不能让输入框先失焦：失焦会先关掉这一层，随后的 click 就点空了。
  pop.addEventListener("mousedown", (e) => e.preventDefault());
  pop.addEventListener("click", (e) => {
    const button = e.target.closest("[data-region-option]");
    if (button) pick(button.dataset.regionOption);
  });
  clear.addEventListener("click", () => {
    input.value = "";
    clear.hidden = true;
    mark("");
    input.focus();
    open(false);
  });
  input.addEventListener("blur", () => {
    setTimeout(() => {
      if (!box.contains(document.activeElement)) close();
    }, 120);
  });
}
