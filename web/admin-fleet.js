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
// 新安装脚本装的 Agent 带 self_update，能一键升级；更早的要在机器上手工执行一次安装命令。
const selfUpdating = (n) => !!n.metrics?.capabilities?.includes("self_update");
const upgradeCommand = () => `curl -fsSL ${location.origin}/install.sh | sh -s -- --endpoint ${location.origin}`;

const UPGRADE_BADGES = {
  queued: ["排队升级", "", "下一次上报（约 5 秒）时下发；离线的机器上线后再升级"],
  sent: ["升级中…", "", "已下发，Agent 正在下载、校验并重启"],
  failed: ["升级未完成", "warn", ""],
  manual: ["需手工升级一次", "warn", "这台的 Agent 早于一键升级，请在机器上执行一次升级命令"],
  unavailable: ["无安装包", "warn", ""],
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
  hint: "改名会连同作用于这个分组的提醒规则一起改；改成已有的分组名即合并。解散后节点回到未分组。",
  nameField: (value) => `<label>分组名<input name="name" value="${esc(value)}" maxlength="100" required autocomplete="off"></label>`,
  rename: (from, to) => send("/admin/groups/rename", "POST", { from, to }),
  dissolve: "解散",
};
const REGION = {
  key: "region",
  title: "地区管理",
  noun: "地区",
  empty: "未设置地区",
  hint: "这里管的是「节点所在地区」（卡片上显示的位置）。卡片按哪个地区挑探测结果，在勾选节点后的「设地区」里改。",
  nameField: (value) => regionCombobox("地区", "name", value, regionOptions(state.regions, state.nodes.map((n) => n.region))),
  rename: (from, to) =>
    send("/admin/nodes/batch", "POST", { ids: state.nodes.filter((n) => (n.region || "") === from).map((n) => n.id), patch: { region: to } }),
  dissolve: "清除",
};

function manager(kind) {
  const rows = buckets(kind.key)
    .map(
      ([value, count]) =>
        `<tr><td>${value ? esc(value) : `<span class="muted">${kind.empty}</span>`}</td><td>${count} 台</td><td><div class="row-actions">${
          value
            ? `<button type="button" data-members="${esc(value)}">调整成员</button><button type="button" data-rename="${esc(value)}">改名</button><button type="button" class="danger" data-dissolve="${esc(value)}">${kind.dissolve}</button>`
            : `<button type="button" data-select="">勾选这些节点</button>`
        }</div></td></tr>`,
    )
    .join("");
  const d = modal(
    kind.title,
    `<p class="hint">${kind.hint}</p><div class="table-scroll"><table class="manager-table"><thead><tr><th>${kind.noun}</th><th>节点</th><th>操作</th></tr></thead><tbody>${rows || `<tr><td colspan="3" class="empty-cell">还没有节点</td></tr>`}</tbody></table></div><div class="form-footer"><button type="button" class="primary" id="new-bucket">＋ 新建${kind.noun}</button></div>`,
  );
  d.querySelector("#new-bucket").onclick = guard(() => members(kind, null));
  d.querySelectorAll("[data-members]").forEach((b) => (b.onclick = guard(() => members(kind, b.dataset.members))));
  d.querySelectorAll("[data-rename]").forEach((b) => (b.onclick = guard(() => renameDialog(kind, b.dataset.rename))));
  d.querySelectorAll("[data-select]").forEach(
    (b) =>
      (b.onclick = () => {
        d.close();
        for (const box of content().querySelectorAll("[data-pick]")) {
          const n = state.nodes.find((x) => x.id === box.dataset.pick);
          box.checked = (n?.[kind.key] || "") === b.dataset.select;
        }
        syncPicked();
      }),
  );
  d.querySelectorAll("[data-dissolve]").forEach(
    (b) =>
      (b.onclick = () => {
        const value = b.dataset.dissolve;
        confirmAction(
          `${kind.dissolve}${kind.noun} · ${value}`,
          `「${value}」里的 ${state.nodes.filter((n) => n[kind.key] === value).length} 台节点将变为${kind.empty}。`,
          async () => {
            await kind.rename(value, "");
            await render();
            toast(`已${kind.dissolve}「${value}」`);
            manager(kind);
          },
          { label: kind.dissolve },
        );
      }),
  );
}

function renameDialog(kind, from) {
  const d = modal(
    `${kind.noun}改名 · ${from}`,
    `<form>${kind.nameField(from)}<p class="hint">${kind === GROUP ? "改成已有的分组名，两个分组会合并。" : "改成已有的地区，两个地区会合并。"}</p><div class="form-footer"><button class="primary" type="submit">保存</button></div></form>`,
  );
  const form = d.querySelector("form");
  wireComboboxes(form);
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const to = form.elements.namedItem("name").value.trim();
    if (!to) throw new Error(`${kind.noun}名不能为空`);
    if (to === from) return manager(kind);
    await kind.rename(from, to);
    await render();
    toast(`已改名为「${to}」`);
    manager(kind);
  });
}

// 成员勾选：勾上的移进这个分组 / 地区，原来在里面、被取消勾选的移出（变为未分组 / 未设置）。
// value 为 null 是新建：先填名字。
function members(kind, value) {
  const fresh = value === null;
  const list = state.nodes
    .map((n) => {
      const current = n[kind.key] || "";
      return `<label class="checkbox member-row" data-search="${esc(`${n.name} ${current} ${n.region || ""}`.toLowerCase())}"><input type="checkbox" name="member" value="${n.id}" ${!fresh && current === value ? "checked" : ""}><span>${esc(n.name)}</span><small>${current ? esc(current) : kind.empty}</small></label>`;
    })
    .join("");
  const d = modal(
    fresh ? `新建${kind.noun}` : `调整成员 · ${value}`,
    `<form>${fresh ? kind.nameField("") : ""}<div class="member-tools"><input type="search" name="filter" placeholder="按名称、${kind.noun}筛选" autocomplete="off"><button type="button" data-all="1">全选可见</button><button type="button" data-all="0">全不选</button></div><div class="member-list">${list}</div><p class="hint">勾上的节点移到这个${kind.noun}${fresh ? "" : `；原来在「${esc(value)}」里、取消勾选的变为${kind.empty}`}。</p><div class="form-footer"><button class="primary" type="submit">保存</button></div></form>`,
  );
  const form = d.querySelector("form");
  wireComboboxes(form);
  const filter = form.elements.namedItem("filter");
  filter.oninput = () => {
    const q = filter.value.trim().toLowerCase();
    for (const row of form.querySelectorAll(".member-row")) row.hidden = !!q && !row.dataset.search.includes(q);
  };
  form.querySelectorAll("[data-all]").forEach(
    (b) =>
      (b.onclick = () => {
        for (const row of form.querySelectorAll(".member-row:not([hidden]) input")) row.checked = b.dataset.all === "1";
      }),
  );
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const target = fresh ? form.elements.namedItem("name").value.trim() : value;
    if (!target) throw new Error(`${kind.noun}名不能为空`);
    const checked = new Set([...form.querySelectorAll("input[name=member]:checked")].map((b) => b.value));
    const moveIn = state.nodes.filter((n) => checked.has(n.id) && (n[kind.key] || "") !== target).map((n) => n.id);
    const moveOut = fresh ? [] : state.nodes.filter((n) => !checked.has(n.id) && (n[kind.key] || "") === target).map((n) => n.id);
    if (!moveIn.length && !moveOut.length) return manager(kind);
    if (moveIn.length) await send("/admin/nodes/batch", "POST", { ids: moveIn, patch: { [kind.key]: target } });
    if (moveOut.length) await send("/admin/nodes/batch", "POST", { ids: moveOut, patch: { [kind.key]: "" } });
    await render();
    toast(`「${target}」：移入 ${moveIn.length} 台${moveOut.length ? `，移出 ${moveOut.length} 台` : ""}`);
    manager(kind);
  });
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
  if (by.manual?.length) manualUpgrade(by.manual, parts);
  else toast(parts.join("，") || "没有可升级的节点");
}

// 早于一键升级的 Agent：给一条不带密钥的升级命令（保留现有配置，只换二进制并重写服务单元），
// 执行一次之后这些机器就都能一键升级了。
function manualUpgrade(ids, parts) {
  const command = upgradeCommand();
  const d = modal(
    `需要手工升级一次 · ${ids.length} 台`,
    `${parts.length ? `<p>${esc(parts.join("，"))}。</p>` : ""}<p>${esc(nameList(ids, 20))} 的 Agent 早于一键升级，不会自己换版本。在这些机器上以 root（或有 sudo 权限的用户）执行一次下面的命令：保留现有配置与密钥，只更新程序和服务单元。之后它们就能在这里一键升级了。</p><div class="command-block"><pre>${esc(command)}</pre><button type="button" id="copy-upgrade" class="primary">复制命令</button></div><div class="form-footer"><button type="button" class="primary" id="upgrade-done">知道了</button></div>`,
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
