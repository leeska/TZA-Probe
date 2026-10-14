import { esc, modal, guard, toast, send } from "./api.js";
import { searchMatch } from "./search.js";
import { regionOptions } from "./regions.js";
import { regionCombobox, wireComboboxes } from "./region-picker.js";

const carriers = ["电信", "联通", "移动", "其他"];
const checkbox = (label, name, checked) =>
  `<label class="checkbox"><input type="checkbox" name="${esc(name)}" ${checked ? "checked" : ""}>${esc(label)}</label>`;
const options = (values) =>
  values.map((x) => `<option>${esc(x)}</option>`).join("");
const target = ({ host, port, family, region, carrier }) => ({
  host,
  port,
  family,
  region,
  carrier,
});
// 自定义目标的地区会与节点卡片的回程 / 延迟地区逐字比对，手输容易写出对不上的名字，
// 所以给候选；目录里没有的其他地区仍然可以自己填。
const regionSuggestions = (catalog, existing) =>
  regionOptions(
    catalog.targets.map((x) => x.region),
    (existing?.probe?.targets || []).map((x) => x.region),
  );

// 常用海外目标：一键勾选，不用每次手填自定义目标。地区写服务名，图表里每条线就读作
// 「Google · 其他」。只能 TCP 探测，公共 DNS 测 53 端口；域名只列确定有 AAAA 记录的才给 IPv6，
// 免得勾上 IPv6 就整组失败。
const presetGroups = [
  ["公共 DNS", [
    ["Cloudflare DNS", 53, "1.1.1.1", "2606:4700:4700::1111"],
    ["Google DNS", 53, "8.8.8.8", "2001:4860:4860::8888"],
    ["Quad9 DNS", 53, "9.9.9.9", "2620:fe::fe"],
    ["OpenDNS", 53, "208.67.222.222", "2620:119:35::35"],
  ]],
  ["Telegram", [
    ["Telegram DC1", 443, "149.154.175.53", "2001:b28:f23d:f001::a"],
    ["Telegram DC2", 443, "149.154.167.51", "2001:67c:4e8:f002::a"],
    ["Telegram DC3", 443, "149.154.175.100", "2001:b28:f23d:f003::a"],
    ["Telegram DC4", 443, "149.154.167.91", "2001:67c:4e8:f004::a"],
    ["Telegram DC5", 443, "91.108.56.130", "2001:b28:f23f:f005::a"],
  ]],
  ["主流网站", [
    ["Google", 443, "www.google.com", "www.google.com"],
    ["YouTube", 443, "www.youtube.com", "www.youtube.com"],
    ["GitHub", 443, "github.com"],
    ["Cloudflare", 443, "www.cloudflare.com", "www.cloudflare.com"],
    ["Netflix", 443, "www.netflix.com", "www.netflix.com"],
    ["ChatGPT", 443, "chatgpt.com"],
    ["Discord", 443, "discord.com"],
    ["Steam", 443, "store.steampowered.com"],
  ]],
].map(([title, items]) => [
  title,
  items.map(([region, port, v4, v6]) => [
    region + " / 其他",
    [
      { host: v4, port, family: "ipv4", region, carrier: "其他" },
      ...(v6 ? [{ host: v6, port, family: "ipv6", region, carrier: "其他" }] : []),
    ],
  ]),
]);
const presets = new Map(presetGroups.flatMap(([, items]) => items));

// 间隔按最自然的单位写：3600 秒写「1 小时」，90 秒照写「90 秒」——只在整除时进位。
function every(seconds) {
  if (seconds % 3600 === 0) return `每 ${seconds / 3600} 小时`;
  if (seconds % 60 === 0 && seconds >= 120) return `每 ${seconds / 60} 分钟`;
  return `每 ${seconds} 秒`;
}

export function taskSummary(t) {
  // 解锁检测没有目标与端口：它的对象是内置目录里的 24 项服务，列表里要一眼看出它是这一类任务，
  // 所以「检测内容」写「解锁检测」、目标写「内置目录」，不去算不存在的地址数。
  if (t.type === "unlock") {
    return {
      unlock: true,
      checks: "解锁检测",
      targets: "流媒体 / AI / 交易所 / 其他",
      targetsFull: "内置目录（流媒体 / AI / 交易所 / 其他）",
      regions: 0,
      families: "",
      count: 0,
      intervals: every(t.unlock.interval),
    };
  }
  const p = t.probe;
  const targets = p.targets;
  // 地区 / 运营商按地区归并：同一地区的运营商合成一行，不再为每个运营商重复一遍地区名。
  const rank = (carrier) => {
    const at = ["电信", "联通", "移动", "其他"].indexOf(carrier);
    return at < 0 ? 9 : at;
  };
  const byRegion = new Map();
  for (const x of targets) {
    const region = x.region || "其他";
    if (!byRegion.has(region)) byRegion.set(region, new Set());
    byRegion.get(region).add(x.carrier || "其他");
  }
  const groups = [...byRegion].map(
    ([region, carriers]) =>
      `${region} ${[...carriers].sort((a, b) => rank(a) - rank(b)).join("/")}`,
  );
  return {
    checks: p.checks
      .map((x) => (x === "route" ? "回程" : "延迟"))
      .join(" + "),
    // 列表里只放前三个地区，超出的折成省略号，数量交给单元格的次级行（targetsFull 是完整名单，悬停显示）。
    targets: groups.length > 3 ? `${groups.slice(0, 3).join("、")} …` : groups.join("、"),
    targetsFull: groups.join("、"),
    regions: byRegion.size,
    families: [
      ...new Set(targets.map((x) => (x.family === "ipv4" ? "IPv4" : "IPv6"))),
    ].join(" / "),
    count: targets.length,
    intervals: p.checks
      .map((k) => `${k === "route" ? "回程" : "延迟"}${every(k === "route" ? p.route_interval : p.interval)}`)
      .join(" / "),
  };
}

export function taskEditor(existing, catalog, nodes, onSaved) {
  // 任务是 type + 定义的联合：probe（网络探测：延迟 / 回程，带目标）或 unlock（解锁检测：
  // 无目标，按内置目录逐项检测）。表单两种都摆着，切换时只换显示，保存时只发当前那一种。
  const t = existing || { type: "probe", enabled: true, node_ids: [] };
  const probe = t.probe || { checks: ["tcp", "route"], targets: [], interval: 60, route_interval: 3600 };
  const unlock = t.unlock || { interval: 3600, family: "" };
  const checks = probe.checks;
  // 编辑已有解锁任务时直接进解锁表单；新建默认是网络探测。
  let kind = t.type === "unlock" ? "unlock" : "probe";
  const families = new Set(
    probe.targets.length ? probe.targets.map((x) => x.family) : ["ipv4", "ipv6"],
  );
  const groups = new Map();
  for (const x of catalog.targets) {
    const key = x.region + " / " + x.carrier;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(target(x));
  }
  // 每组目标记录自己的 IP 协议：all 是该组可用地址，families 是本任务实际要测的协议。
  const selected = new Map();
  for (const x of probe.targets) {
    const key = x.region + " / " + x.carrier;
    const pool = groups.get(key) || presets.get(key);
    const matching = pool?.some(
      (y) => y.host === x.host && y.port === x.port && y.family === x.family,
    );
    const selectionKey = matching ? key : key + " · " + x.host;
    const entry = selected.get(selectionKey) || { all: [], families: new Set() };
    if (matching) entry.all = pool;
    else if (!entry.all.some((y) => y.host === x.host && y.family === x.family))
      entry.all.push(target(x));
    entry.families.add(x.family);
    selected.set(selectionKey, entry);
  }
  const availableFamilies = (all) => ["ipv4", "ipv6"].filter((f) => all.some((x) => x.family === f));
  // 新加入的组按上方“IP 协议”勾选；该组没有对应地址时退回它自己有的协议，避免加进来却什么都不测。
  const defaultFamilies = (all) => {
    const available = availableFamilies(all);
    const wanted = available.filter((f) => el(f)?.checked);
    return new Set(wanted.length ? wanted : available);
  };
  const d = modal(
    existing?.id
      ? kind === "unlock"
        ? "编辑解锁检测"
        : "编辑网络任务"
      : kind === "unlock"
        ? "创建解锁检测"
        : "创建网络任务",
    `
    <form id="task-form">
      <section class="form-section"><h3>任务类型与名称</h3>
        <div class="option-tiles" id="task-kind">
          <label class="checkbox"><input type="radio" name="kind" value="probe" ${kind === "probe" ? "checked" : ""}>网络探测（延迟 / 回程）</label>
          <label class="checkbox"><input type="radio" name="kind" value="unlock" ${kind === "unlock" ? "checked" : ""}>解锁检测</label>
        </div>
        <p class="hint">解锁检测由节点自己的出口逐项检测流媒体 / AI / 交易所，没有目标地址与端口，间隔最短 1 小时。</p>
        <div class="form-grid task-meta"><label>任务名称<input name="name" maxlength="100" value="${esc(t.name || "")}" placeholder="留空自动命名" autocomplete="off"></label><div class="option-tiles">${checkbox("启用任务", "enabled", t.enabled)}</div></div>
      </section>
      <div id="probe-sections"${kind === "unlock" ? " hidden" : ""}>
      <section class="form-section"><h3>选择目标</h3>
        <p id="selected-label" class="option-label"></p>
        <div id="selected-targets" class="selected-targets"></div>
        <div class="preset-groups">${presetGroups
          .map(
            ([title, items]) =>
              `<div class="preset-group"><span class="option-label">${esc(title)}</span><div class="option-tiles">${items
                .map(
                  ([key, xs]) =>
                    `<label class="checkbox" title="${esc(xs.map((x) => x.host).filter((h, i, all) => all.indexOf(h) === i).join(" / "))}"><input type="checkbox" data-preset="${esc(key)}" ${selected.has(key) ? "checked" : ""}>${esc(xs[0].region)}<small>:${xs[0].port}</small></label>`,
                )
                .join("")}</div></div>`,
          )
          .join("")}</div>
        <p class="option-label">国内三网目录</p>
        <div class="catalog-filters">
          <label>搜索目录<input id="catalog-search" placeholder="地区、运营商、地址（支持 hb、ct、hk 这类拼音与缩写）" type="search"></label>
          <label>地区<select id="catalog-region"><option value="">全部地区</option>${options([...new Set(catalog.targets.map((x) => x.region))])}</select></label>
          <label>运营商<select id="catalog-carrier"><option value="">全部运营商</option>${options(carriers)}</select></label>
        </div>
        <div class="catalog-toolbar"><span id="catalog-count"></span><button type="button" id="select-visible">选中筛选结果</button><button type="button" id="clear-targets">清空已选</button></div>
        <div id="catalog-results" class="catalog-results"></div>
        <p class="hint">同一地区和运营商只需选一次。每组默认按下方勾选的 IP 协议检测，可在“已选目标”上单独改成只测 IPv4、只测 IPv6 或两者都测。</p>
        <details><summary>添加自定义目标（含海外运营商）</summary>
          <div class="form-grid">
            ${regionCombobox("地区", "custom-region", "", regionSuggestions(catalog, existing))}
            <label>运营商<select name="custom-carrier">${options(["其他", "电信", "联通", "移动"])}</select></label>
            <label>IPv4 地址或域名<input name="custom-ipv4" placeholder="IPv4 或支持 A 记录的域名"></label>
            <label>IPv6 地址或域名<input name="custom-ipv6" placeholder="IPv6 或支持 AAAA 记录的域名"></label>
            <label>端口<input name="custom-port" type="number" min="1" max="65535" value="443"></label>
          </div><button type="button" id="add-custom">添加目标</button>
        </details>
      </section>
      <section class="form-section"><h3>检测内容</h3>
        <div class="option-groups">
          <div class="option-group"><span class="option-label">检测类型</span><div class="option-tiles">${checkbox("TCP 延迟", "tcp", checks.includes("tcp"))}${checkbox("三网 / 海外回程", "route", checks.includes("route"))}</div></div>
          <div class="option-group"><span class="option-label">IP 协议（批量设置，可在已选目标上单独调整）</span><div class="option-tiles">${checkbox("IPv4", "ipv4", families.has("ipv4"))}${checkbox("IPv6", "ipv6", families.has("ipv6"))}</div></div>
        </div>
        <details><summary>检测频率</summary><div class="form-grid">
          <label>延迟间隔（秒）<input name="interval" type="number" min="30" max="86400" value="${probe.interval || 60}"></label>
          <label>回程间隔（秒）<input name="route_interval" type="number" min="900" max="86400" value="${probe.route_interval || 3600}"></label>
        </div></details>
      </section>
      </div>
      <section class="form-section" id="unlock-section"${kind === "unlock" ? "" : " hidden"}><h3>解锁检测</h3>
        <div class="form-grid">
          <label>检测间隔（秒）<input name="unlock_interval" type="number" min="3600" max="86400" value="${unlock.interval}"></label>
          <label>IP 协议<select name="unlock_family"><option value="">自动（由节点选择）</option><option value="ipv4"${unlock.family === "ipv4" ? " selected" : ""}>IPv4</option><option value="ipv6"${unlock.family === "ipv6" ? " selected" : ""}>IPv6</option></select></label>
        </div>
        <p class="hint">留空即自动：纯 IPv6 的节点也能跑，不会因为默认 IPv4 而整轮失败。间隔最短 1 小时（每轮是从这台机器自己的出口发几十次 HTTPS 检测）。</p>
      </section>
      <section class="form-section"><h3>执行节点</h3>
        <div class="catalog-toolbar"><button type="button" id="all-nodes">全选节点</button><button type="button" id="no-nodes">清空节点</button></div>
        <div class="node-checkboxes">${nodes.map((n) => checkbox(n.name, "node:" + n.id, t.node_ids?.includes(n.id))).join("") || "<p>请先创建节点</p>"}</div>
        <p class="hint">空选表示不执行；每台节点最多同时执行一个延迟和一个回程检测，多目标依次排队。</p>
      </section>
      <p id="task-summary" class="notice" aria-live="polite"></p>
      <div class="form-footer"><button type="submit" class="primary">保存任务</button></div>
    </form>`,
  );
  const form = d.querySelector("form");
  const el = (name) => form.elements.namedItem(name);
  wireComboboxes(form);
  let visible = [];
  const activeTargets = () =>
    [...selected.values()].flatMap((e) =>
      e.all.filter((x) => e.families.has(x.family)),
    );
  function update() {
    // 已选目标排在目录上面：编辑已有任务时一打开就看到它测的是什么，不用先滚过整份目录。
    d.querySelector("#selected-label").textContent = selected.size
      ? `已选目标 ${selected.size} 组`
      : "还没有选目标：从下方目录勾选，或添加自定义目标";
    d.querySelector("#selected-targets").innerHTML = [...selected]
      .map(([key, e]) => {
        const available = availableFamilies(e.all);
        const toggles = ["ipv4", "ipv6"]
          .map((f) => {
            const has = available.includes(f);
            const on = e.families.has(f);
            return `<button type="button" class="chip-family ${on ? "on" : ""}" data-family-toggle="${esc(key)}" data-family="${f}" ${has ? "" : "disabled"} title="${has ? (on ? "点击停止检测" : "点击加入检测") : "该目标没有对应地址"}" aria-pressed="${on}">${f === "ipv4" ? "IPv4" : "IPv6"}</button>`;
          })
          .join("");
        return `<span class="target-chip"><strong>${esc(key)}</strong><span class="chip-families">${toggles}</span><button type="button" class="chip-remove" data-remove="${esc(key)}" aria-label="移除 ${esc(key)}">×</button></span>`;
      })
      .join("");
    d.querySelectorAll("[data-remove]").forEach(
      (b) =>
        (b.onclick = () => {
          selected.delete(b.dataset.remove);
          renderCatalog();
        }),
    );
    d.querySelectorAll("[data-family-toggle]").forEach(
      (b) =>
        (b.onclick = () => {
          const e = selected.get(b.dataset.familyToggle);
          const family = b.dataset.family;
          if (!e.families.has(family)) e.families.add(family);
          else if (e.families.size > 1) e.families.delete(family);
          else {
            toast("每组目标至少保留一种 IP 协议");
            return;
          }
          update();
        }),
    );
    d.querySelectorAll("[data-preset]").forEach(
      (input) => (input.checked = selected.has(input.dataset.preset)),
    );
    const count = activeTargets().length;
    const kinds = Number(el("tcp").checked) + Number(el("route").checked);
    const bound = nodes.filter((n) => el("node:" + n.id).checked).length;
    if (kind === "unlock") {
      // 解锁检测没有目标与检测项，汇总只写间隔与执行节点数——不写没有意义的「0 个地址」。
      const every = Number(el("unlock_interval").value) || 3600;
      d.querySelector("#task-summary").textContent = `解锁检测 · 每 ${every} 秒 · ${bound} 台执行节点`;
    } else {
      d.querySelector("#task-summary").textContent =
        `已选 ${selected.size} 组目标 · ${count} 个地址 · 每台节点 ${count * kinds} 个检测项 · ${bound} 台执行节点`;
    }
    el("interval").disabled = !el("tcp").checked;
    el("route_interval").disabled = !el("route").checked;
  }
  function renderCatalog() {
    const query = d.querySelector("#catalog-search").value;
    const region = d.querySelector("#catalog-region").value;
    const carrier = d.querySelector("#catalog-carrier").value;
    visible = [...groups].filter(
      ([key, xs]) =>
        (!region || xs[0].region === region) &&
        (!carrier || xs[0].carrier === carrier) &&
        searchMatch(query, key, ...xs.map((x) => x.host)),
    );
    d.querySelector("#catalog-count").textContent = `${visible.length} 组目标`;
    d.querySelector("#catalog-results").innerHTML =
      visible
        .map(
          ([key, xs]) =>
            `<label class="catalog-option"><input type="checkbox" data-target="${esc(key)}" ${selected.has(key) ? "checked" : ""}><span><strong>${esc(key)}</strong><small>${xs.map((x) => (x.family === "ipv4" ? "IPv4" : "IPv6")).join(" · ")}</small></span></label>`,
        )
        .join("") ||
      '<p class="hint">没有匹配目标，可清除筛选或添加自定义目标。</p>';
    d.querySelectorAll("[data-target]").forEach(
      (input) =>
        (input.onchange = () => {
          if (input.checked)
            selected.set(input.dataset.target, {
              all: groups.get(input.dataset.target),
              families: defaultFamilies(groups.get(input.dataset.target)),
            });
          else selected.delete(input.dataset.target);
          update();
        }),
    );
    update();
  }
  d.querySelectorAll("[data-preset]").forEach(
    (input) =>
      (input.onchange = () => {
        const key = input.dataset.preset;
        if (input.checked)
          selected.set(key, { all: presets.get(key), families: defaultFamilies(presets.get(key)) });
        else selected.delete(key);
        update();
      }),
  );
  for (const id of ["catalog-search", "catalog-region", "catalog-carrier"])
    d.querySelector("#" + id).addEventListener(
      id === "catalog-search" ? "input" : "change",
      renderCatalog,
    );
  d.querySelector("#select-visible").onclick = () => {
    for (const [key, xs] of visible)
      if (!selected.has(key))
        selected.set(key, { all: xs, families: defaultFamilies(xs) });
    renderCatalog();
  };
  d.querySelector("#clear-targets").onclick = () => {
    selected.clear();
    renderCatalog();
  };
  for (const [id, checked] of [
    ["all-nodes", true],
    ["no-nodes", false],
  ])
    d.querySelector("#" + id).onclick = () => {
      for (const n of nodes) el("node:" + n.id).checked = checked;
      update();
    };
  d.querySelector("#add-custom").onclick = guard(() => {
    const region = el("custom-region").value.trim(),
      carrier = el("custom-carrier").value,
      port = Number(el("custom-port").value);
    const xs = ["ipv4", "ipv6"]
      .map((family) => ({
        family,
        host: el("custom-" + family).value.trim(),
        region,
        carrier,
        port,
      }))
      .filter((x) => x.host);
    if (
      !region ||
      !xs.length ||
      !Number.isInteger(port) ||
      port < 1 ||
      port > 65535
    )
      throw new Error("请填写地区、有效端口和至少一个地址");
    selected.set(
      region + " / " + carrier + " · " + xs.map((x) => x.host).join(" / "),
      { all: xs, families: new Set(xs.map((x) => x.family)) },
    );
    update();
  });
  // 上方的 IP 协议是批量开关：勾选变化时套用到全部已选目标，之后仍可逐组微调。
  for (const family of ["ipv4", "ipv6"])
    el(family).addEventListener("change", () => {
      for (const e of selected.values()) e.families = defaultFamilies(e.all);
      update();
    });
  // 任务类型：网络探测与解锁检测各有一套区块，切换时只显示对应那套（区块自身 hidden）。
  d.querySelectorAll('input[name="kind"]').forEach((r) =>
    r.addEventListener("change", () => {
      kind = el("kind").value === "unlock" ? "unlock" : "probe";
      d.querySelector("#probe-sections").hidden = kind === "unlock";
      d.querySelector("#unlock-section").hidden = kind !== "unlock";
      update();
    }),
  );
  form.addEventListener("change", update);
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const nodeIds = nodes
      .filter((n) => el("node:" + n.id).checked)
      .map((n) => n.id);
    // 解锁检测只发 unlock 一侧：联合类型里同时带 probe 会被服务端拒绝。
    if (kind === "unlock") {
      const interval = Number(el("unlock_interval").value);
      if (!Number.isInteger(interval) || interval < 3600 || interval > 86400)
        throw new Error("解锁检测的间隔最短 3600 秒、最长 86400 秒");
      await send(
        "/admin/tasks" + (existing?.id ? "/" + existing.id : ""),
        existing?.id ? "PUT" : "POST",
        {
          name: el("name").value.trim() || "解锁检测",
          type: "unlock",
          unlock: { interval, family: el("unlock_family").value },
          enabled: el("enabled").checked,
          node_ids: nodeIds,
        },
      );
      d.close();
      await onSaved();
      toast("任务已保存");
      return;
    }
    const checks = ["tcp", "route"].filter((k) => el(k).checked);
    const targets = activeTargets().filter(
      (x, i, all) =>
        all.findIndex((y) => JSON.stringify(y) === JSON.stringify(x)) === i,
    );
    if (!checks.length) throw new Error("请至少选择一种检测内容");
    if (!selected.size) throw new Error("请至少选择一组目标");
    if (!targets.length) throw new Error("请为已选目标至少保留一种 IP 协议");
    const name =
      el("name").value.trim() ||
      (
        [...selected.keys()].slice(0, 2).join("、") +
        (selected.size > 2 ? " 等" : "")
      ).slice(0, 100);
    await send(
      "/admin/tasks" + (existing?.id ? "/" + existing.id : ""),
      existing?.id ? "PUT" : "POST",
      {
        name,
        type: "probe",
        probe: {
          checks,
          targets,
          interval: Number(el("interval").value),
          route_interval: Number(el("route_interval").value),
        },
        enabled: el("enabled").checked,
        node_ids: nodeIds,
      },
    );
    d.close();
    await onSaved();
    toast("任务已保存");
  });
  renderCatalog();
}
