import { esc } from "./api.js";
import { fold, searchKeys } from "./search.js";

let pickerID = 0;

export function searchPicker(label, name, value, choices, { required = true, allowCustom = true, placeholder = "选择或输入以搜索" } = {}) {
  const id = `search-picker-${++pickerID}`;
  const pool = new Map(choices.map(choice => [choice.value, choice]));
  if (value && !pool.has(value)) pool.set(value, { value, label: value });
  return `<div class="search-field"><label for="${id}">${esc(label)}</label><span class="select-shell combo search-picker" data-search-picker data-custom="${allowCustom}">
    <input id="${id}" name="${esc(name)}" class="select-trigger combo-input" type="text" value="${esc(value)}" placeholder="${esc(placeholder)}" autocomplete="off" spellcheck="false" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="${id}-options" ${required ? "required" : ""}>
    <button type="button" class="search-picker-toggle" aria-label="展开${esc(label)}选项" aria-controls="${id}-options" aria-expanded="false" tabindex="-1">⌄</button>
    <span class="select-pop" hidden><span id="${id}-options" role="listbox" aria-label="${esc(label)}选项">${[...pool.values()].map((choice, index) => `<span id="${id}-option-${index}" class="select-opt combo-opt" role="option" aria-selected="${choice.value === value}" data-choice-value="${esc(choice.value)}" data-choice-search="${esc([choice.label, choice.search || ""].join(" "))}">${esc(choice.label)}${choice.label === choice.value ? "" : `<small>${esc(choice.value)}</small>`}</span>`).join("")}</span><span class="combo-empty" role="status" hidden>${allowCustom ? "没有匹配项，可保留手动输入；保存时会检查格式。" : "没有匹配项，请从下拉列表选择有效选项。"}</span></span>
    </span></div>`;
}

function wirePicker(box) {
  if (box.dataset.wired) return;
  box.dataset.wired = "true";
  const input = box.querySelector("input");
  const toggle = box.querySelector(".search-picker-toggle");
  const pop = box.querySelector(".select-pop");
  const note = box.querySelector(".combo-empty");
  const options = [...box.querySelectorAll("[data-choice-value]")];
  const keys = options.map(option => searchKeys(option.dataset.choiceValue, option.dataset.choiceSearch));
  let active = -1;
  const visible = () => options.filter(option => !option.hidden);
  const validate = () => input.setCustomValidity(box.dataset.custom !== "true" && input.value && !options.some(option => option.dataset.choiceValue === input.value) ? "请从下拉列表选择有效选项" : "");
  const mark = () => options.forEach(option => {
    const selected = option.dataset.choiceValue === input.value;
    option.classList.toggle("selected", selected);
    option.setAttribute("aria-selected", String(selected));
  });
  const close = () => {
    box.classList.remove("open");
    pop.hidden = true;
    input.setAttribute("aria-expanded", "false");
    toggle.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-activedescendant");
    options.forEach(option => option.classList.remove("active"));
    active = -1;
  };
  const position = () => {
    let top = 8;
    let bottom = window.innerHeight - 8;
    for (let parent = box.parentElement; parent; parent = parent.parentElement) {
      if (!parent.clientHeight || !/(auto|scroll|hidden)/.test(getComputedStyle(parent).overflowY)) continue;
      const rect = parent.getBoundingClientRect();
      top = Math.max(top, rect.top + 8);
      bottom = Math.min(bottom, rect.bottom - 8);
    }
    const footer = box.closest("dialog")?.querySelector(".form-footer")?.getBoundingClientRect();
    const header = box.closest("dialog")?.querySelector(".modal-head")?.getBoundingClientRect();
    if (header?.height) top = Math.max(top, header.bottom + 8);
    if (footer?.height) bottom = Math.min(bottom, footer.top - 8);
    const rect = input.getBoundingClientRect();
    const above = rect.top - top - 6;
    const below = bottom - rect.bottom - 6;
    const up = below < 240 && above > below;
    pop.classList.toggle("pop-up", up);
    pop.style.maxHeight = `${Math.max(0, Math.min(240, up ? above : below))}px`;
  };
  const open = (query = "") => {
    if (input.matches(":disabled")) return;
    const tokens = query.split(/[\s/、,，]+/).map(fold).filter(Boolean);
    options.forEach((option, index) => {
      option.hidden = !tokens.every(token => keys[index].some(key => key.includes(token)));
      option.classList.remove("active");
    });
    note.hidden = visible().length > 0;
    active = -1;
    input.removeAttribute("aria-activedescendant");
    mark();
    box.classList.add("open");
    pop.hidden = false;
    input.setAttribute("aria-expanded", "true");
    toggle.setAttribute("aria-expanded", "true");
    pop.scrollTop = 0;
    position();
  };
  const pick = option => {
    input.value = option.dataset.choiceValue;
    validate();
    mark();
    input.focus({ preventScroll: true });
    close();
    input.dispatchEvent(new Event("input", { bubbles: true }));
    close();
    input.dispatchEvent(new Event("change", { bubbles: true }));
  };
  input.addEventListener("focus", () => open());
  input.addEventListener("click", () => { if (pop.hidden) open(); });
  input.addEventListener("input", () => { validate(); open(input.value.trim()); });
  input.addEventListener("change", validate);
  input.addEventListener("keydown", event => {
    if (event.isComposing) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (pop.hidden) open();
      const list = visible();
      if (!list.length) return;
      active = (active + (event.key === "ArrowDown" ? 1 : active < 0 ? 0 : -1) + list.length) % list.length;
      options.forEach(option => option.classList.remove("active"));
      const option = list[active];
      option.classList.add("active");
      input.setAttribute("aria-activedescendant", option.id);
      if (option.offsetTop < pop.scrollTop) pop.scrollTop = option.offsetTop;
      else if (option.offsetTop + option.offsetHeight > pop.scrollTop + pop.clientHeight) pop.scrollTop = option.offsetTop + option.offsetHeight - pop.clientHeight;
    } else if (event.key === "Enter" && !pop.hidden) {
      event.preventDefault();
      if (visible()[active]) pick(visible()[active]);
      else close();
    } else if (event.key === "Escape" && !pop.hidden) {
      event.preventDefault();
      event.stopPropagation();
      close();
    } else if (event.key === "Tab") close();
  });
  pop.addEventListener("mousedown", event => event.preventDefault());
  pop.addEventListener("click", event => {
    const option = event.target.closest("[data-choice-value]");
    if (option && !option.hidden) pick(option);
  });
  toggle.addEventListener("mousedown", event => event.preventDefault());
  toggle.addEventListener("click", () => {
    if (input.matches(":disabled")) return;
    const wasOpen = !pop.hidden;
    input.focus({ preventScroll: true });
    if (wasOpen) close();
    else open();
  });
  input.addEventListener("blur", () => { close(); validate(); });
  const dialog = box.closest("dialog");
  dialog?.addEventListener("scroll", close, { passive: true });
  dialog?.addEventListener("close", close);
  validate();
}

export function wireSearchPickers(root) {
  root.querySelectorAll("[data-search-picker]").forEach(wirePicker);
}
