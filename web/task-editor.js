import { esc, modal, guard, toast, send } from "./api.js";

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

export function taskSummary(t) {
  const targets = t.targets || [t];
  return {
    checks: (t.checks || [t.kind])
      .map((x) => (x === "route" ? "回程" : "延迟"))
      .join(" + "),
    targets: [...new Set(targets.map((x) => x.region + " " + x.carrier))].join(
      "、",
    ),
    families: [
      ...new Set(targets.map((x) => (x.family === "ipv4" ? "IPv4" : "IPv6"))),
    ].join(" / "),
    count: targets.length,
    intervals: (t.checks || [t.kind])
      .map(
        (k) =>
          `${k === "route" ? "回程" : "延迟"} ${k === "route" ? t.route_interval || t.interval : t.interval} 秒`,
      )
      .join(" / "),
  };
}

export function taskEditor(existing, catalog, nodes, onSaved) {
  const t = existing || {
    checks: ["tcp", "route"],
    interval: 60,
    route_interval: 3600,
    enabled: true,
    node_ids: [],
  };
  const checks = t.checks || [t.kind];
  const families = new Set(
    existing ? (t.targets || [t]).map((x) => x.family) : ["ipv4", "ipv6"],
  );
  const groups = new Map();
  for (const x of catalog.targets) {
    const key = x.region + " / " + x.carrier;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(target(x));
  }
  const selected = new Map();
  for (const x of existing ? t.targets || [t] : []) {
    const key = x.region + " / " + x.carrier;
    const matching = groups
      .get(key)
      ?.some(
        (y) => y.host === x.host && y.port === x.port && y.family === x.family,
      );
    const selectionKey = matching ? key : key + " · " + x.host;
    if (matching) selected.set(selectionKey, groups.get(key));
    else selected.set(selectionKey, [target(x)]);
  }
  const d = modal(
    existing?.id ? "编辑网络任务" : "创建网络任务",
    `
    <form id="task-form">
      <section class="form-section"><h3>1. 选择目标</h3>
        <div class="catalog-filters">
          <label>搜索目录<input id="catalog-search" placeholder="输入地区、运营商或地址" type="search"></label>
          <label>地区<select id="catalog-region"><option value="">全部地区</option>${options([...new Set(catalog.targets.map((x) => x.region))])}</select></label>
          <label>运营商<select id="catalog-carrier"><option value="">全部运营商</option>${options(carriers)}</select></label>
        </div>
        <div class="catalog-toolbar"><span id="catalog-count"></span><button type="button" id="select-visible">选中筛选结果</button><button type="button" id="clear-targets">清空已选</button></div>
        <div id="catalog-results" class="catalog-results"></div>
        <p class="hint">同一地区和运营商只需选一次，地址会按下方勾选的 IP 协议自动匹配。</p>
        <details><summary>添加自定义目标（含海外运营商）</summary>
          <div class="form-grid">
            <label>地区<input name="custom-region" placeholder="例如：新加坡"></label>
            <label>运营商<select name="custom-carrier">${options(["其他", "电信", "联通", "移动"])}</select></label>
            <label>IPv4 地址或域名<input name="custom-ipv4" placeholder="IPv4 或支持 A 记录的域名"></label>
            <label>IPv6 地址或域名<input name="custom-ipv6" placeholder="IPv6 或支持 AAAA 记录的域名"></label>
            <label>端口<input name="custom-port" type="number" min="1" max="65535" value="443"></label>
          </div><button type="button" id="add-custom">添加目标</button>
        </details>
        <div id="selected-targets" class="selected-targets"></div>
      </section>
      <section class="form-section"><h3>2. 检测内容</h3>
        <div class="option-groups">
          <div class="option-group"><span class="option-label">检测类型</span><div class="option-tiles">${checkbox("TCP 延迟", "tcp", checks.includes("tcp"))}${checkbox("三网 / 海外回程", "route", checks.includes("route"))}</div></div>
          <div class="option-group"><span class="option-label">IP 协议</span><div class="option-tiles">${checkbox("IPv4", "ipv4", families.has("ipv4"))}${checkbox("IPv6", "ipv6", families.has("ipv6"))}</div></div>
        </div>
        <details><summary>检测频率</summary><div class="form-grid">
          <label>延迟间隔（秒）<input name="interval" type="number" min="30" max="86400" value="${t.kind === "route" ? 60 : t.interval}"></label>
          <label>回程间隔（秒）<input name="route_interval" type="number" min="900" max="86400" value="${t.route_interval || (t.kind === "route" ? t.interval : 3600)}"></label>
        </div></details>
      </section>
      <section class="form-section"><h3>3. 执行节点</h3>
        <div class="catalog-toolbar"><button type="button" id="all-nodes">全选节点</button><button type="button" id="no-nodes">清空节点</button></div>
        <div class="node-checkboxes">${nodes.map((n) => checkbox(n.name, "node:" + n.id, t.node_ids?.includes(n.id))).join("") || "<p>请先创建节点</p>"}</div>
        <p class="hint">空选表示不执行；每台节点最多同时执行一个延迟和一个回程检测，多目标依次排队。</p>
      </section>
      <section class="form-section"><h3>4. 任务设置</h3>
        <div class="form-grid task-meta"><label>任务名称<input name="name" maxlength="100" value="${esc(t.name || "")}" placeholder="留空自动命名" autocomplete="off"></label><div class="option-tiles">${checkbox("启用任务", "enabled", t.enabled)}</div></div>
      </section>
      <p id="task-summary" class="notice" aria-live="polite"></p>
      <div class="form-footer"><button type="submit" class="primary">保存任务</button></div>
    </form>`,
  );
  const form = d.querySelector("form");
  const el = (name) => form.elements.namedItem(name);
  let visible = [];
  const activeTargets = () =>
    [...selected.values()].flat().filter((x) => el(x.family).checked);
  function update() {
    d.querySelector("#selected-targets").innerHTML = [...selected.keys()]
      .map(
        (key) =>
          `<button type="button" data-remove="${esc(key)}" aria-label="移除 ${esc(key)}">${esc(key)} ×</button>`,
      )
      .join("");
    d.querySelectorAll("[data-remove]").forEach(
      (b) =>
        (b.onclick = () => {
          selected.delete(b.dataset.remove);
          renderCatalog();
        }),
    );
    const count = activeTargets().length;
    const kinds = Number(el("tcp").checked) + Number(el("route").checked);
    const bound = nodes.filter((n) => el("node:" + n.id).checked).length;
    d.querySelector("#task-summary").textContent =
      `已选 ${selected.size} 组目标 · ${count} 个地址 · 每台节点 ${count * kinds} 个检测项 · ${bound} 台执行节点`;
    el("interval").disabled = !el("tcp").checked;
    el("route_interval").disabled = !el("route").checked;
  }
  function renderCatalog() {
    const query = d.querySelector("#catalog-search").value.trim().toLowerCase();
    const region = d.querySelector("#catalog-region").value;
    const carrier = d.querySelector("#catalog-carrier").value;
    visible = [...groups].filter(
      ([key, xs]) =>
        (!region || xs[0].region === region) &&
        (!carrier || xs[0].carrier === carrier) &&
        (key + " " + xs.map((x) => x.host).join(" "))
          .toLowerCase()
          .includes(query),
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
            selected.set(
              input.dataset.target,
              groups.get(input.dataset.target),
            );
          else selected.delete(input.dataset.target);
          update();
        }),
    );
    update();
  }
  for (const id of ["catalog-search", "catalog-region", "catalog-carrier"])
    d.querySelector("#" + id).addEventListener(
      id === "catalog-search" ? "input" : "change",
      renderCatalog,
    );
  d.querySelector("#select-visible").onclick = () => {
    for (const [key, xs] of visible) selected.set(key, xs);
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
      xs,
    );
    update();
  });
  form.addEventListener("change", update);
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const checks = ["tcp", "route"].filter((k) => el(k).checked);
    const targets = activeTargets().filter(
      (x, i, all) =>
        all.findIndex((y) => JSON.stringify(y) === JSON.stringify(x)) === i,
    );
    if (!checks.length) throw new Error("请至少选择一种检测内容");
    if (!el("ipv4").checked && !el("ipv6").checked)
      throw new Error("请至少选择一种 IP 协议");
    if (!targets.length) throw new Error("请选择与 IP 协议匹配的目标");
    for (const family of ["ipv4", "ipv6"])
      if (el(family).checked && !targets.some((x) => x.family === family))
        throw new Error(
          `已勾选 ${family.toUpperCase()}，请添加对应地址，或取消该协议`,
        );
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
        checks,
        targets,
        interval: Number(el("interval").value),
        route_interval: Number(el("route_interval").value),
        enabled: el("enabled").checked,
        node_ids: nodes
          .filter((n) => el("node:" + n.id).checked)
          .map((n) => n.id),
      },
    );
    d.close();
    await onSaved();
    toast("任务已保存");
  });
  renderCatalog();
}
