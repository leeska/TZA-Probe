import { api, send, esc, toast, modal, guard } from "./api.js";
import { regionCombobox, wireComboboxes } from "./region-picker.js";
import { regionOptions } from "./regions.js";
import { state, content, render, loadNodes, confirmAction } from "./admin-state.js";
// 节点页的批量操作：勾选若干台 → 底部浮出工具栏（设分组、设地区、公开 / 隐藏、升级 Agent），
// 外加「分组管理」「地区管理」两个弹窗按分组 / 地区整体调整成员、改名、解散，以及一键升级 Agent。
// 批量修改走 POST /admin/nodes/batch：同一份部分字段合并到每台节点，全成或全不成。

// 勾选状态跨重绘保留（保存后整页重画，勾着的还勾着，可以接着做下一步）。
export const picked = new Set();
// 一键升级的队列状态（GET /admin/agents/upgrade），按节点 id。
let upgrades = {};

export async function loadUpgrades() {
  try {
    upgrades = (await api("/admin/agents/upgrade")).items || {};
  } catch {
    upgrades = {};
  }
}

export const isStale = (n) => !!(n.metrics?.version && state.serverVersion && n.metrics.version !== state.serverVersion);
// 钉了升级公钥、带 --self-update 的 Agent 报 self_update，能一键升级；更早的（或节点上没有公钥的）
// 要在机器上手工执行一次安装命令。
const selfUpdating = (n) => !!n.metrics?.capabilities?.includes("self_update");
const upgradeCommand = () => `curl -fsSL ${location.origin}/install.sh | sh -s -- --endpoint ${location.origin}`;

const UPGRADE_BADGES = {
  queued: ["排队升级", "", "下一次上报（约 5 秒）时下发；离线的机器上线后再升级"],
  sent: ["升级中…", "", "已下发，Agent 正在下载、校验并重启"],
  failed: ["升级未完成", "warn", ""],
  manual: ["需手工升级一次", "warn", "这台的 Agent 不接受一键升级（版本较早，或节点上没有升级公钥），请在机器上执行一次升级命令"],
  unavailable: ["无安装包", "warn", ""],
  unsigned: ["安装包未签名", "warn", "服务端的 Agent 安装包没有可用签名，不能一键升级；见部署说明「一键升级」"],
};
// 状态格里「Agent 版本 · 架构」那一行：版本落后标「需更新」，排了升级的标进度。
export function agentLine(n) {
  if (!n.metrics) return "Agent 未接入";
  let out = "Agent " + [n.metrics.version, n.metrics.arch].filter(Boolean).map(esc).join(" · ");
  const it = upgrades[n.id];
  const badge = it && UPGRADE_BADGES[it.state];
  if (badge) out += ` <span class="pill upgrade-pill ${badge[1]}" title="${esc(it.error || badge[2])}">${badge[0]}</span>`;
  else if (isStale(n))
    out += ` <button type="button" class="agent-stale" data-upgrade-one="${esc(n.id)}" title="面板版本 ${esc(state.serverVersion)}。${selfUpdating(n) ? "点这里单独升级这一台；勾选多台后点「升级 Agent」批量升级。" : "这台的 Agent 早于一键升级：点这里看升级命令，在机器上执行一次之后就能一键升级。"}">需更新 ⇡</button>`;
  return out;
}

// 操作列里的「升级」：版本落后、且没有排着或正在进行的升级时才出现。
export function upgradeButton(n) {
  const busy = ["queued", "sent"].includes(upgrades[n.id]?.state);
  return isStale(n) && !busy ? `<button type="button" data-upgrade-one="${esc(n.id)}" title="把这台的 Agent 升级到 ${esc(state.serverVersion)}">升级</button>` : "";
}

// 单台升级：状态格的「需更新」和操作列的「升级」都走这里。委托到 document，
// 轮询原地刷新 Agent 那一行后新按钮不用重新绑定。
document.addEventListener("click", (e) => {
  const b = e.target.closest?.("[data-upgrade-one]");
  if (b) guard(() => confirmUpgrade([b.dataset.upgradeOne]))();
});

// ---------------------------------------------------------------- 勾选与工具栏

export function pickCell(n) {
  return `<td class="pick-cell" data-label="选择"><input type="checkbox" data-pick="${n.id}" ${picked.has(n.id) ? "checked" : ""} aria-label="选择 ${esc(n.name)}"></td>`;
}
export const pickHead = '<th class="select-col"><label class="checkbox select-all"><input id="pick-all" type="checkbox" aria-label="选择全部节点"><span>全选</span></label></th>';

export function batchBar() {
  return `<div id="batch-bar" class="batch-bar" role="toolbar" aria-label="批量操作" hidden><span class="batch-count">已选 <b>0</b> 台</span><button type="button" data-batch="group">设分组</button><button type="button" data-batch="region">设地区</button><button type="button" data-batch="public">公开</button><button type="button" data-batch="private">隐藏</button><button type="button" data-batch="upgrade">升级 Agent</button><button type="button" data-batch="clear" class="batch-clear" aria-label="取消选择" title="取消选择">×</button></div>`;
}

// 节点页每次重画后调用：接上勾选框、全选、按分组 / 地区点选、工具栏与升级按钮。
export function wireFleet() {
  const root = content();
  // 已删除的节点不留在勾选里。
  for (const id of [...picked]) if (!state.nodes.some((n) => n.id === id)) picked.delete(id);
  const boxes = () => [...root.querySelectorAll("[data-pick]")];
  let last = -1;
  boxes().forEach((box, i) => {
    box.addEventListener("click", (e) => {
      // Shift 点选：从上一次点的那一行到这一行，全部设成与这一格相同的状态。
      if (e.shiftKey && last >= 0) {
        const list = boxes();
        const [from, to] = [Math.min(last, i), Math.max(last, i)];
        for (const b of list.slice(from, to + 1)) b.checked = box.checked;
      }
      last = i;
      syncPicked();
    });
  });
  const all = root.querySelector("#pick-all");
  if (all)
    all.onchange = () => {
      for (const b of boxes()) b.checked = all.checked;
      syncPicked();
    };
  // 点「分组 / 地区」格里的名字：选中同组（同地区）的全部节点，再点一次取消。
  root.querySelectorAll("[data-pick-by]").forEach(
    (b) =>
      (b.onclick = () => {
        const { pickBy: key, value } = b.dataset;
        const ids = state.nodes.filter((n) => (n[key] || "") === value).map((n) => n.id);
        const on = !ids.every((id) => picked.has(id));
        for (const box of boxes()) if (ids.includes(box.dataset.pick)) box.checked = on;
        syncPicked();
      }),
  );
  const bar = root.querySelector("#batch-bar");
  bar.querySelectorAll("[data-batch]").forEach((b) => (b.onclick = guard(() => batchAction(b.dataset.batch))));
  root.querySelector("#manage-groups").onclick = guard(() => manager(GROUP));
  root.querySelector("#manage-regions").onclick = guard(() => manager(REGION));
  const upgradeAll = root.querySelector("#upgrade-all");
  if (upgradeAll) upgradeAll.onclick = guard(() => confirmUpgrade(state.nodes.filter(isStale).map((n) => n.id)));
  syncPicked();
  schedulePoll();
}

function syncPicked() {
  const root = content();
  picked.clear();
  for (const b of root.querySelectorAll("[data-pick]")) {
    if (b.checked) picked.add(b.dataset.pick);
    b.closest("tr")?.classList.toggle("row-picked", b.checked);
  }
  const all = root.querySelector("#pick-all");
  if (all) {
    all.checked = picked.size > 0 && picked.size === state.nodes.length;
    all.indeterminate = picked.size > 0 && picked.size < state.nodes.length;
  }
  const bar = root.querySelector("#batch-bar");
  bar.hidden = picked.size === 0;
  bar.querySelector("b").textContent = picked.size;
  document.body.classList.toggle("has-batch-bar", picked.size > 0);
}

async function batchAction(action) {
  const ids = [...picked];
  if (action === "clear") {
    for (const b of content().querySelectorAll("[data-pick]")) b.checked = false;
    syncPicked();
  } else if (action === "group") groupDialog(ids);
  else if (action === "region") regionDialog(ids);
  else if (action === "public" || action === "private") {
    await batchPatch(ids, { public: action === "public" });
    toast(`已将 ${ids.length} 台设为${action === "public" ? "公开" : "隐藏"}`);
  } else if (action === "upgrade") confirmUpgrade(ids);
}

async function batchPatch(ids, patch) {
  await send("/admin/nodes/batch", "POST", { ids, patch });
  await render();
}

const nameList = (ids, limit = 6) => {
  const names = ids.map((id) => state.nodes.find((n) => n.id === id)?.name || id);
  return names.slice(0, limit).join("、") + (names.length > limit ? ` 等 ${names.length} 台` : "");
};

// ---------------------------------------------------------------- 设分组 / 设地区（对勾选的节点）

// 已有的分组 / 地区及其台数，按拼音排；未分组（空值）排最后。
function buckets(key) {
  const count = new Map();
  for (const n of state.nodes) count.set(n[key] || "", (count.get(n[key] || "") || 0) + 1);
  return [...count].sort(([a], [b]) => !a - !b || a.localeCompare(b, "zh-Hans-CN"));
}

function groupDialog(ids) {
  const chips = buckets("group")
    .filter(([g]) => g)
    .map(([g, c]) => `<button type="button" class="chip" data-chip="${esc(g)}">${esc(g)}<small>${c}</small></button>`)
    .join("");
  const d = modal(
    `设置分组 · ${ids.length} 台`,
    `<form><p class="hint">${esc(nameList(ids))}</p>${chips ? `<div class="chip-row">${chips}</div>` : ""}<label>分组名<input name="group" maxlength="100" placeholder="留空即未分组（面板上显示为「默认」）" autocomplete="off"></label><p class="hint">作用于这个分组的提醒规则会自动套到移进来的节点上。</p><div class="form-footer"><button class="primary" type="submit">移到这个分组</button></div></form>`,
  );
  const input = d.querySelector("[name=group]");
  d.querySelectorAll("[data-chip]").forEach((b) => (b.onclick = () => ((input.value = b.dataset.chip), input.focus())));
  input.focus();
  d.querySelector("form").onsubmit = guard(async (e) => {
    e.preventDefault();
    const group = input.value.trim();
    await batchPatch(ids, { group });
    d.close();
    toast(`已将 ${ids.length} 台移到${group ? `「${group}」` : "未分组"}`);
  });
}

// 卡片地区只给能对上的候选（内置目录与任务用到的地区，服务端只认这些）；「节点所在地区」
// 另外带上海外常用地区，它只用于显示。
function regionDialog(ids) {
  const chosen = state.nodes.filter((n) => ids.includes(n.id));
  const nodeOptions = regionOptions(state.regions, chosen.map((n) => n.region));
  const cardOptions = state.regions;
  const d = modal(
    `设置地区 · ${ids.length} 台`,
    `<form><p class="hint">${esc(nameList(ids))}。留空的项不修改。</p><div class="form-grid">${regionCombobox("节点所在地区", "region", "", nodeOptions, { placeholder: "不修改" })}${regionCombobox("卡片回程地区", "route_region", "", cardOptions, { placeholder: "不修改" })}${regionCombobox("卡片延迟地区", "latency_region", "", cardOptions, { placeholder: "不修改" })}</div><label class="checkbox"><input type="checkbox" name="clear_card">清空卡片地区（回到按任务地区匹配）</label><p class="hint">卡片地区要与探测任务目标的地区逐字相同，公开卡片才有延迟和回程。</p><div class="form-footer"><button class="primary" type="submit">应用到 ${ids.length} 台</button></div></form>`,
  );
  const form = d.querySelector("form");
  wireComboboxes(form);
  const clear = form.elements.namedItem("clear_card");
  clear.onchange = () => {
    for (const name of ["route_region", "latency_region"]) form.elements.namedItem(name).disabled = clear.checked;
  };
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const value = (name) => form.elements.namedItem(name).value.trim();
    const patch = {};
    if (value("region")) patch.region = value("region");
    const display = {};
    if (clear.checked) Object.assign(display, { route_region: "", latency_region: "" });
    else {
      if (value("route_region")) display.route_region = value("route_region");
      if (value("latency_region")) display.latency_region = value("latency_region");
    }
    if (Object.keys(display).length) patch.display = display;
    if (!Object.keys(patch).length) throw new Error("没有要修改的项");
    await batchPatch(ids, patch);
    d.close();
    toast(`已更新 ${ids.length} 台的地区`);
  });
}

// ---------------------------------------------------------------- 分组管理 / 地区管理

const GROUP = {
  key: "group",
  title: "分组管理",
  noun: "分组",
  empty: "未分组",
  hint: "点左边的分组查看成员；点「加入」「移出」当场生效。改名会连同作用于这个分组的提醒规则一起改，改成已有的名字即合并。",
  nameField: (value) => `<label>分组名<input name="name" value="${esc(value)}" maxlength="100" required autocomplete="off"></label>`,
  rename: (from, to) => send("/admin/groups/rename", "POST", { from, to }),
  dissolve: "解散",
};
const REGION = {
  key: "region",
  title: "地区管理",
  noun: "地区",
  empty: "未设置地区",
  hint: "这里管的是「节点所在地区」（卡片上显示的位置），点「加入」「移出」当场生效。卡片按哪个地区挑探测结果，在勾选节点后的「设地区」里改。",
  nameField: (value) => regionCombobox("地区", "name", value, regionOptions(state.regions, state.nodes.map((n) => n.region))),
  rename: (from, to) =>
    send("/admin/nodes/batch", "POST", { ids: state.nodes.filter((n) => (n.region || "") === from).map((n) => n.id), patch: { region: to } }),
  dissolve: "清除",
};

// 分组 / 地区管理：一个两栏弹窗，不再层层套弹窗。左栏是全部分组（含「未分组」）和新建，右栏是选中那一组：
// 顶部就地改名 / 解散，下面平铺全部节点（按面板顺序，位置不随加入移出跳动）：已在这一组的高亮、点「移出」，
// 其余的标着现在所在的分组、点「加入」；搜索框筛选，筛选时可以一次加入筛选出来的。每次点击当场生效（一次
// 批量请求），没有最后的「保存」；节点只能在一个分组里，所以「加入」就是从原分组移过来。
function manager(kind, selected) {
  const d = modal(kind.title, `<p class="hint">${kind.hint}</p><div class="bucket-board"></div>`);
  d.classList.add("wide-dialog");
  d.addEventListener("close", () => d.classList.remove("wide-dialog"), { once: true });
  const board = d.querySelector(".bucket-board");
  const view = { current: selected ?? buckets(kind.key)[0]?.[0] ?? "", fresh: null, mode: "", q: "" };
  const of = (n) => n[kind.key] || "";
  const label = (v) => (v ? esc(v) : `<span class="muted">${kind.empty}</span>`);
  const searchText = (n) => `${n.name} ${n.group || ""} ${n.region || ""}`.toLowerCase();

  const paint = () => {
    const list = buckets(kind.key);
    if (view.fresh && list.some(([v]) => v === view.fresh)) view.fresh = null;
    if (view.current !== view.fresh && !list.some(([v]) => v === view.current)) view.current = list[0]?.[0] ?? "";
    // 新建还没落地的那一组排在「未分组」前面（buckets 已把未分组放在最后）。
    const named = list.filter(([v]) => v);
    const rows = [...named, ...(view.fresh ? [[view.fresh, 0]] : []), ...list.filter(([v]) => !v)]
      .map(([v, c]) => `<button type="button" class="bucket${v === view.current ? " on" : ""}" data-bucket="${esc(v)}" aria-pressed="${v === view.current}"><span>${label(v)}</span><small>${c}</small></button>`)
      .join("");
    const create = `<form class="bucket-new">${kind === REGION ? regionCombobox("", "name", "", regionOptions(state.regions, state.nodes.map((n) => n.region)), { placeholder: "新建地区" }) : `<input name="name" maxlength="100" placeholder="新建分组" autocomplete="off" aria-label="新分组名">`}<button type="submit" class="quiet" aria-label="新建">＋</button></form>`;

    const cur = view.current;
    const inside = state.nodes.filter((n) => of(n) === cur);
    let head;
    if (view.mode === "rename" && cur) {
      head = `<form class="bucket-rename">${kind.nameField(cur)}<div class="bucket-head-actions"><button type="button" class="quiet" data-cancel>取消</button><button type="submit" class="primary">保存</button></div><p class="hint">${kind === GROUP ? "改成已有的分组名即合并；作用于这个分组的提醒规则跟着改名。" : "改成已有的地区即合并。"}</p></form>`;
    } else if (view.mode === "dissolve" && cur) {
      head = `<div class="bucket-confirm"><p>${kind.dissolve}「${esc(cur)}」？里面的 ${inside.length} 台节点将变为${kind.empty}。</p><div class="bucket-head-actions"><button type="button" class="quiet" data-cancel>取消</button><button type="button" class="danger" data-confirm-dissolve>${kind.dissolve}</button></div></div>`;
    } else {
      head = `<div class="bucket-head"><h3>${label(cur)}<small>${inside.length} 台</small></h3>${cur ? `<div class="bucket-head-actions"><button type="button" class="quiet" data-mode="rename">改名</button><button type="button" class="danger" data-mode="dissolve" ${inside.length ? "" : "disabled"}>${kind.dissolve}</button></div>` : ""}</div>`;
    }
    const rowsAll = state.nodes
      .map((n) => {
        const here = of(n) === cur;
        const action = here
          ? cur
            ? `<button type="button" class="bucket-act" data-out="${n.id}" title="移到${kind.empty}">移出</button>`
            : ""
          : `<button type="button" class="bucket-act add" data-in="${n.id}">加入</button>`;
        return `<li class="bucket-node${here ? " in" : ""}" data-search="${esc(searchText(n))}"><span class="bucket-node-name">${esc(n.name)}</span><small>${here ? "✓ 在这里" : label(of(n))}</small>${action}</li>`;
      })
      .join("");
    const fresh = view.fresh === cur && !inside.length ? `<p class="bucket-empty">新${kind.noun}，加入第一台节点后才会保存。</p>` : "";
    board.innerHTML = `<aside class="bucket-side"><div class="bucket-list" role="group" aria-label="${kind.noun}">${rows || `<p class="bucket-empty">还没有${kind.noun}</p>`}</div>${create}</aside><section class="bucket-main">${head}<div class="bucket-section"><div class="bucket-label"><span>全部节点 · ${state.nodes.length} 台</span><span class="bucket-tools"><input type="search" class="bucket-search" placeholder="搜索名称 / 分组 / 地区" value="${esc(view.q)}" autocomplete="off" aria-label="筛选节点"><button type="button" class="bucket-act add" data-all-in hidden></button></span></div>${fresh}<ul class="bucket-nodes">${rowsAll || '<li class="bucket-empty">还没有节点</li>'}</ul></div></section>`;
    wireComboboxes(board);
    applySearch();
    board.querySelector(".bucket-rename input")?.focus();
  };

  const matches = (n) => !view.q.trim() || searchText(n).includes(view.q.trim().toLowerCase());
  // 「加入筛选出的 N 台」：筛选出来、还不在这一组的节点。
  const toAdd = () => state.nodes.filter((n) => matches(n) && of(n) !== view.current);
  const applySearch = () => {
    const q = view.q.trim().toLowerCase();
    for (const row of board.querySelectorAll(".bucket-node")) row.hidden = !!q && !row.dataset.search.includes(q);
    const all = board.querySelector("[data-all-in]");
    if (all) {
      // 只在筛选时出现：不筛选就点会把全部节点一下子搬进来，没有确认。
      const n = toAdd().length;
      all.textContent = `加入筛选出的 ${n} 台`;
      all.hidden = !q || !n;
    }
  };

  // 一次批量改动：期间整块变灰挡住连点，完成后重读节点、原地重画。
  const apply = async (ids, value, message) => {
    if (!ids.length) return;
    board.classList.add("busy");
    try {
      await send("/admin/nodes/batch", "POST", { ids, patch: { [kind.key]: value } });
      await render();
      toast(message);
    } finally {
      board.classList.remove("busy");
      paint();
    }
  };
  const name = (id) => state.nodes.find((n) => n.id === id)?.name || id;
  const target = () => view.current;
  const where = (v) => (v ? `「${v}」` : kind.empty);

  board.onclick = guard(async (e) => {
    const t = e.target.closest("button");
    if (!t || board.classList.contains("busy")) return;
    if (t.dataset.bucket !== undefined) {
      Object.assign(view, { current: t.dataset.bucket, mode: "" });
      return paint();
    }
    if (t.dataset.mode) return (view.mode = t.dataset.mode), paint();
    if (t.hasAttribute("data-cancel")) return (view.mode = ""), paint();
    if (t.dataset.in) return apply([t.dataset.in], target(), `${name(t.dataset.in)} 已移到${where(target())}`);
    if (t.dataset.out) return apply([t.dataset.out], "", `${name(t.dataset.out)} 已移到${kind.empty}`);
    if (t.hasAttribute("data-all-in")) {
      const ids = toAdd().map((n) => n.id);
      return apply(ids, target(), `${ids.length} 台已移到${where(target())}`);
    }
    if (t.hasAttribute("data-confirm-dissolve")) {
      const from = view.current;
      board.classList.add("busy");
      try {
        await kind.rename(from, "");
        await render();
        toast(`已${kind.dissolve}「${from}」`);
        Object.assign(view, { current: "", mode: "" });
      } finally {
        board.classList.remove("busy");
        paint();
      }
    }
  });
  board.oninput = (e) => {
    if (!e.target.classList.contains("bucket-search")) return;
    view.q = e.target.value;
    applySearch();
  };
  board.onsubmit = guard(async (e) => {
    e.preventDefault();
    const form = e.target;
    const value = form.elements.namedItem("name").value.trim();
    if (!value) throw new Error(`${kind.noun}名不能为空`);
    if (form.classList.contains("bucket-new")) {
      // 分组 / 地区只存在于节点上：新建的先挂在列表里，等加入第一台节点才算数。
      Object.assign(view, buckets(kind.key).some(([v]) => v === value) ? { current: value } : { current: value, fresh: value }, { mode: "" });
      return paint();
    }
    const from = view.current;
    if (value === from) return (view.mode = ""), paint();
    board.classList.add("busy");
    try {
      await kind.rename(from, value);
      await render();
      toast(`「${from}」已改名为「${value}」`);
      Object.assign(view, { current: value, mode: "" });
    } finally {
      board.classList.remove("busy");
      paint();
    }
  });
  paint();
}

// ---------------------------------------------------------------- 一键升级 Agent

function confirmUpgrade(ids) {
  if (!ids.length) return toast("没有需要升级的节点");
  confirmAction(
    `升级 Agent · ${ids.length} 台`,
    `${nameList(ids)} 将升级到面板版本 ${state.serverVersion}：Agent 下载新版本、校验后自动重启，期间探测中断几秒。已是最新版本的会跳过。`,
    () => upgradeAgents(ids),
    { label: "升级", danger: false },
  );
}

async function upgradeAgents(ids) {
  const { results } = await send("/admin/agents/upgrade", "POST", { ids });
  const by = {};
  for (const [id, s] of Object.entries(results)) (by[s] ||= []).push(id);
  await loadUpgrades();
  refreshAgentLines();
  schedulePoll();
  const parts = [];
  if (by.queued) parts.push(`${by.queued.length} 台已排队升级`);
  if (by.current) parts.push(`${by.current.length} 台已是最新`);
  if (by.never) parts.push(`${by.never.length} 台从未接入`);
  if (by.unavailable) parts.push(`${by.unavailable.length} 台的架构没有安装包`);
  if (by.unsigned) parts.push(`${by.unsigned.length} 台的安装包没有签名，不能一键升级`);
  if (by.manual?.length) manualUpgrade(by.manual, parts);
  else toast(parts.join("，") || "没有可升级的节点");
}

// 早于一键升级的 Agent：给一条不带密钥的升级命令（保留现有配置，只换二进制并重写服务单元），
// 执行一次之后这些机器就都能一键升级了。
function manualUpgrade(ids, parts) {
  const command = upgradeCommand();
  const d = modal(
    `需要手工升级一次 · ${ids.length} 台`,
    `${parts.length ? `<p>${esc(parts.join("，"))}。</p>` : ""}<p>${esc(nameList(ids, 20))} 的 Agent 不接受一键升级（版本较早，或节点上还没有升级公钥）。在这些机器上以 root（或有 sudo 权限的用户）执行一次下面的命令：保留现有配置与密钥，更新程序和服务单元，并记下本服务器的升级公钥。之后它们就能在这里一键升级了。</p><div class="command-block"><pre>${esc(command)}</pre><button type="button" id="copy-upgrade" class="primary">复制命令</button></div><div class="form-footer"><button type="button" class="primary" id="upgrade-done">知道了</button></div>`,
  );
  d.querySelector("#copy-upgrade").onclick = guard(async () => {
    await navigator.clipboard.writeText(command);
    toast("升级命令已复制");
  });
  d.querySelector("#upgrade-done").onclick = () => d.close();
}

function refreshAgentLines() {
  for (const el of content()?.querySelectorAll("[data-agent]") || []) {
    const n = state.nodes.find((x) => x.id === el.dataset.agent);
    if (!n) continue;
    el.innerHTML = agentLine(n);
    // 操作列的「升级」跟着进度显隐：排上了就收起，升级失败 / 仍落后再出现。
    const actions = el.closest("tr")?.querySelector(".row-actions");
    if (!actions) continue;
    actions.querySelector("[data-upgrade-one]")?.remove();
    const btn = upgradeButton(n);
    if (btn) actions.querySelector("[data-delete]")?.insertAdjacentHTML("beforebegin", btn);
  }
  const all = content()?.querySelector("#upgrade-all");
  if (all) {
    const stale = state.nodes.filter(isStale).length;
    all.hidden = !stale;
    all.querySelector("b").textContent = stale;
  }
}

// 有排队或进行中的升级时每 5 秒重读一次，原地刷新 Agent 那一行，不整页重画（不打断勾选和拖动）。
let pollTimer = 0;
function schedulePoll() {
  clearTimeout(pollTimer);
  if (!Object.values(upgrades).some((it) => it.state === "queued" || it.state === "sent")) return;
  pollTimer = setTimeout(async () => {
    if (state.tab !== "nodes" || !content()?.querySelector("[data-agent]")) return;
    try {
      await Promise.all([loadNodes(), loadUpgrades()]);
    } catch {
      return schedulePoll();
    }
    refreshAgentLines();
    const finished = !Object.values(upgrades).some((it) => it.state === "queued" || it.state === "sent");
    if (finished) toast("Agent 升级已全部完成或结束，详见各节点的版本");
    schedulePoll();
  }, 5000);
}
