// 后台「备份」：每天把主控的数据（数据库在线快照、账号、密钥、主题与插件）用口令加密后写到所选节点，
// 节点只留最近几份。恢复只需要备份文件和口令：tza-server backup-restore。
import { api, send, esc, toast, guard } from "./api.js";
import { bytes } from "./format.js";
import { content, render, state } from "./admin-state.js";

const STATUS = { running: ["进行中", ""], ok: ["成功", "good"], partial: ["部分成功", "warn"], failed: ["失败", "bad"] };
let poller;

const localHour = (utc) => {
  const d = new Date();
  d.setUTCHours(utc, 0, 0, 0);
  return d.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
};

function targetRow(t = {}) {
  const options = state.nodes.map((n) => `<option value="${esc(n.id)}" ${n.id === t.node_id ? "selected" : ""}>${esc(n.name)}</option>`).join("");
  return `<div class="cert-target-row backup-target-row"><select name="b_node" required><option value="">选择节点</option>${options}</select><input name="b_dir" value="${esc(t.dir || "")}" placeholder="目录，留空为 /var/backups/tza-probe"><button type="button" class="danger-text" data-remove-target>移除</button></div>`;
}

export async function backupPage(nodesReady) {
  clearTimeout(poller);
  const [, b] = await Promise.all([nodesReady, api("/admin/backups")]);
  const nodeName = (id) => state.nodes.find((n) => n.id === id)?.name || id;
  const hours = Array.from({ length: 24 }, (_, h) => `<option value="${h}" ${h === b.hour ? "selected" : ""}>${String(h).padStart(2, "0")}:00 UTC（本地 ${localHour(h)}）</option>`).join("");
  const runs = b.runs.map((r) => {
    const [label, tone] = STATUS[r.status] || [r.status, ""];
    const targets = r.targets.map((t) => `<div><strong>${esc(nodeName(t.node_id))}</strong> ${t.error ? `<span class="pill bad">失败</span> ${esc(t.error)}` : `<span class="pill good">已存</span> 共保留 ${t.kept} 份`}</div>`).join("");
    return `<tr><td data-label="时间">${new Date(r.started_at * 1000).toLocaleString("zh-CN")}<small>${r.trigger === "manual" ? "手动" : "每日计划"}${r.finished_at ? ` · 用时 ${Math.max(1, r.finished_at - r.started_at)} 秒` : ""}</small></td><td data-label="结果"><span class="pill ${tone}">${label}</span>${r.error ? `<small>${esc(r.error)}</small>` : ""}</td><td data-label="大小">${r.size ? bytes(r.size) : "—"}${r.name ? `<small><code>${esc(r.name)}</code></small>` : ""}</td><td data-label="存放">${targets || "—"}</td></tr>`;
  }).join("");
  content().innerHTML = `<section data-part="page-heading" class="page-heading"><div><div class="eyebrow">BACKUP</div><h1>备份</h1><p>每天把主控的数据用口令加密后写到所选节点，存放的节点读不到内容。数据库在线快照，不用停服。</p></div><div class="heading-actions"><button id="backup-now" class="primary" ${b.has_passphrase && b.targets.length && !b.running ? "" : "disabled"}>${b.running ? "备份中…" : "立即备份"}</button></div></section>
  <section data-part="panel" class="panel"><form id="backup-form"><section class="form-section"><h3>计划</h3><label class="checkbox"><input type="checkbox" name="enabled" ${b.enabled ? "checked" : ""}>每天自动备份</label><div class="form-grid"><label>时间<select name="hour">${hours}</select></label><label>每个节点保留份数<input name="keep" type="number" min="1" max="100" value="${b.keep}" required></label></div></section>
  <section class="form-section"><h3>备份口令</h3><p class="hint">${b.has_passphrase ? "已设置。填新的会替换它：之后的备份用新口令，<strong>之前的备份仍然只能用旧口令打开</strong>。" : "还没设置。至少 12 个字符。"}口令不会保存在任何地方（主控只存由它派生的密钥），<strong>丢了口令，备份就打不开</strong>，请另外记好。</p><div class="form-grid"><label>口令<input name="passphrase" type="password" autocomplete="new-password" minlength="12" ${b.has_passphrase ? 'placeholder="不改就留空"' : "required"}></label><label>再输一次<input name="passphrase2" type="password" autocomplete="new-password"></label></div></section>
  <section class="form-section"><h3>存放到节点</h3><p class="hint">节点要用 --manage 接入。备份文件 <code>tza-probe-日期-时间.tzabk</code>（0600）写进目录，目录里只保留最近几份。建议选和主控不在同一机房的节点。</p><div class="backup-targets">${b.targets.map(targetRow).join("")}</div><button type="button" id="add-backup-target">＋ 添加节点</button></section>
  <div class="form-footer"><button type="submit" class="primary">保存</button></div></form></section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>记录</h3></div><div class="table-scroll"><table class="stack"><thead><tr><th>时间</th><th>结果</th><th>大小</th><th>存放</th></tr></thead><tbody>${runs || '<tr><td colspan="4" class="empty-cell">还没有备份。</td></tr>'}</tbody></table></div></section>
  <section data-part="panel" class="panel"><div class="panel-head"><h3>怎样恢复</h3></div><p class="hint">把备份文件拷到要恢复的机器上（装好 tza-server），执行下面的命令输入口令，它会把数据解到一个新的空目录；然后停掉服务、把这个目录换成数据目录（或在服务里用 --data 指向它）、交给服务用户，再启动。</p><div class="command-block"><pre>sudo tza-server backup-restore --in tza-probe-XXXXXXXX-XXXXXX.tzabk --data /var/lib/tza-probe-restored
sudo chown -R tza-probe:tza-probe /var/lib/tza-probe-restored</pre></div></section>`;
  const form = content().querySelector("#backup-form");
  const box = form.querySelector(".backup-targets");
  const wireRemove = () => box.querySelectorAll("[data-remove-target]").forEach((x) => (x.onclick = () => x.closest(".backup-target-row").remove()));
  form.querySelector("#add-backup-target").onclick = () => { box.insertAdjacentHTML("beforeend", targetRow()); wireRemove(); };
  wireRemove();
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const el = (n) => form.elements.namedItem(n);
    if (el("passphrase").value !== el("passphrase2").value) return toast("两次输入的口令不一样");
    await send("/admin/backups", "PUT", {
      enabled: el("enabled").checked,
      hour: Number(el("hour").value),
      keep: Number(el("keep").value),
      passphrase: el("passphrase").value,
      targets: [...box.querySelectorAll(".backup-target-row")].map((row) => ({ node_id: row.querySelector('[name="b_node"]').value, dir: row.querySelector('[name="b_dir"]').value.trim() })),
    });
    toast("已保存");
    await render();
  });
  content().querySelector("#backup-now").onclick = guard(async () => {
    await send("/admin/backups/run", "POST");
    toast("已开始备份，数据库大时要几分钟");
    await render();
  });
  if (b.running) {
    const tick = () => {
      if (state.tab !== "backup") return;
      if (document.querySelector("dialog[open]") || document.activeElement?.closest?.("#backup-form")) poller = setTimeout(tick, 5000);
      else render();
    };
    poller = setTimeout(tick, 5000);
  }
}
