import { api, send, esc, guard, modal, toast } from "./api.js";
import { field, confirmAction } from "./admin-state.js";

const phaseNames = { baseline: "干净基线观测", followup: "干净独立复测", experiment: "本机批准的实验" };
const reasonNames = { controller_gap: "中控错过时段，已跳过", executor_ineligible: "节点状态、配置或执行条件不满足，已跳过", binding_changed: "授权身份变化，已跳过", daily_budget_exhausted: "UTC 日总预算已用尽或预算状态未知，已跳过" };
const statusNames = { pending: "等待当前窗口派发", leased: "等待节点回执", accepted: "已受理", completed: "命令已确认", rejected: "本机拒绝", cancelled: "已取消", expired: "窗口已过期" };

function slotRow(slot = { time: "08:00", phase: "followup" }) {
  return `<div class="executor-slot-row"><label>节点当地时间<input type="time" name="slot_time" value="${esc(slot.time)}" required></label><label>用途与阶段<select name="slot_phase">${Object.entries(phaseNames).map(([value, label]) => `<option value="${value}" ${value === slot.phase ? "selected" : ""}>${label}</option>`).join("")}</select></label><button type="button" data-remove-slot>移除时段</button></div>`;
}

function editSchedule(view, current) {
  const schedule = current.schedule;
  const services = view.config?.services.filter(service => service.enabled) || [];
  const config = view.hello?.status?.config;
  if (!services.length || !config) { toast("先验证并应用本机配置"); return; }
  const selected = (purpose, service) => (schedule?.[`${purpose}_services`] || []).includes(service);
  const checks = purpose => services.filter(service => purpose === "observe" || service.experiment).map(service => `<label class="checkbox"><input type="checkbox" name="${purpose}" value="${esc(service.service_id)}" ${selected(purpose, service.service_id) ? "checked" : ""}>${esc(service.service_id)}</label>`).join("") || "<p>本机未批准实验服务。</p>";
  const slots = schedule?.slots || [{ time: "08:00", phase: "followup" }];
  const dialog = modal("编辑节点托管日程", `<form id="executor-schedule-form"><p>每个时段只轮换执行一个服务。每日预算由整个节点共享，增加服务会降低每个服务的覆盖频率。</p><label class="checkbox"><input type="checkbox" name="enabled" ${schedule?.enabled ? "checked" : ""}>明确启用此节点的中控排期</label>${field("节点 IANA 时区", "timezone", schedule?.timezone || "", "text", 'placeholder="例如 Asia/Singapore" required')}<fieldset class="executor-service-set"><legend>观测服务</legend><div class="option-tiles">${checks("observe")}</div></fieldset><fieldset class="executor-service-set"><legend>实验服务</legend><div class="option-tiles">${checks("experiment")}</div></fieldset><div data-schedule-slots>${slots.map(slotRow).join("")}</div><button type="button" data-add-slot>添加时段</button>${field("单次超时（毫秒）", "timeout_ms", schedule?.timeout_ms || Math.min(view.config.limits.timeout_ms, 30000), "number", `min="1000" max="${view.config.limits.timeout_ms}" required`)}<p class="hint">将固定配置修订 ${config.revision}。每日节点总预算 ${view.config.daily_budget.runs} 次、${view.config.daily_budget.network_bytes} 字节；每个时段网络预留 ${view.config.limits.network_bytes} 字节。实际准入按 UTC 日计费，单次任务也共享额度。</p><p class="hint">时段窗口为一分钟；断线、暂停、旧配置或中控错过时段会跳过。夏令时缺失时刻跳过，重复时刻仅取第一次。保存不会补跑当前或过去时段。</p><div class="form-footer"><button class="primary" type="submit">核对并保存日程</button></div></form>`);
  const wireRemove = () => dialog.querySelectorAll("[data-remove-slot]").forEach(button => { button.onclick = () => button.closest(".executor-slot-row").remove(); });
  wireRemove();
  dialog.querySelector("[data-add-slot]").onclick = () => {
    if (dialog.querySelectorAll(".executor-slot-row").length >= 32) { toast("最多 32 个时段"); return; }
    dialog.querySelector("[data-schedule-slots]").insertAdjacentHTML("beforeend", slotRow());
    wireRemove();
  };
  dialog.querySelector("form").onsubmit = guard(async event => {
    event.preventDefault();
    const data = new FormData(event.target);
    const times = data.getAll("slot_time");
    const phases = data.getAll("slot_phase");
    const payload = {
      expected_revision: schedule?.revision || 0, enabled: data.get("enabled") === "on",
      timezone: data.get("timezone").trim(), config,
      observe_services: data.getAll("observe"), experiment_services: data.getAll("experiment"),
      slots: times.map((time, index) => ({ time, phase: phases[index] })), timeout_ms: Number(data.get("timeout_ms")),
    };
    confirmAction("保存节点托管日程", `${payload.enabled ? "启用" : "保存为停用"}；时区 ${payload.timezone}，每日 ${payload.slots.length} 个节点总时段，${payload.observe_services.length} 项观测服务、${payload.experiment_services.length} 项实验服务。已派发任务继续查询原任务，未派发的旧排期将取消。`, async () => {
      await send(`/admin/nodes/${encodeURIComponent(view.node_id)}/executors/ipsentinel/schedule`, "PUT", payload);
      toast("日程已保存，等待未来时段");
      await showSchedule(view.node_id);
    }, { danger: false, label: "保存日程" });
  });
}

export async function showSchedule(nodeID) {
  const path = `/admin/nodes/${encodeURIComponent(nodeID)}/executors/ipsentinel/schedule`;
  const [current, views] = await Promise.all([api(path), api("/admin/executors")]);
  const view = views.find(item => item.node_id === nodeID);
  const schedule = current.schedule;
  const fresh = view?.received_at && Date.now() - view.received_at * 1000 < 45000;
  const canEdit = fresh && view.binding.mode === "managed" && view.hello?.status.control.mode === "managed" && view.config;
  const dialog = modal("节点托管日程", `<p>${schedule ? `${schedule.enabled ? "中控排期已启用" : "日程已停用"} · ${esc(schedule.timezone)} · 修订 ${schedule.revision}` : "尚未创建日程。默认不会自动开始周期任务。"}</p><div class="heading-actions"><button data-schedule-refresh>刷新日程</button><button data-schedule-edit ${canEdit ? "" : "disabled"}>${schedule ? "核对并编辑日程" : "创建日程"}</button>${schedule?.enabled ? '<button class="danger" data-schedule-disable>停用未来排期</button>' : ""}</div>${canEdit ? "" : "<p>需要托管授权、新鲜状态和已应用配置才能编辑；停用操作仍可用。</p>"}${schedule ? `<p>节点总时段 ${schedule.slots.length} 个 / 当地日；每日总预算 ${schedule.daily_budget.runs} 次、${schedule.daily_budget.network_bytes} 字节，不随服务数增加。</p><p>固定配置修订 ${schedule.config.revision}。时段：${schedule.slots.map(slot => `${esc(slot.time)} ${phaseNames[slot.phase]}`).join("、")}</p><h3>服务覆盖预估</h3><p class="hint">按未来一年时区变化估算最长计划间隔；仅在所有时段均能执行时成立。离线、暂停、UTC 日预算耗尽会延长覆盖。实验不计入恢复确认。</p>${current.coverage.map(entry => `<p><strong>${esc(entry.service_id)} / ${entry.purpose === "observe" ? "观测" : "实验"}</strong>：${entry.max_gap_hours == null ? "没有可用时段" : `最长计划间隔 ${esc(Number(entry.max_gap_hours.toFixed(2)))} 小时`} · <span class="pill">${entry.sufficient_48h ? "计划满足 48h" : "48h 覆盖不足"}</span></p>`).join("")}` : ""}<h3>最近时段</h3>${current.recent.length ? current.recent.map(entry => `<article class="panel"><p>${esc(entry.local_date)} ${esc(entry.local_time)} · ${esc(entry.timezone)}</p><p>${esc(entry.service_id)} / ${esc(phaseNames[entry.phase] || entry.phase)} · ${esc(entry.status === "skipped" ? reasonNames[entry.reason] || "已跳过" : statusNames[entry.action_status] || "已入队")}</p>${entry.run_state ? `<p>执行状态：${esc(entry.run_state)}</p>` : ""}<p class="hint">逻辑任务：${esc(entry.job_id)}</p></article>`).join("") : "<p>没有已处理时段；错过的历史日程不会补跑。</p>"}`);
  dialog.querySelector("[data-schedule-refresh]").onclick = guard(() => showSchedule(nodeID));
  dialog.querySelector("[data-schedule-edit]").onclick = () => editSchedule(view, current);
  const disable = dialog.querySelector("[data-schedule-disable]");
  if (disable) disable.onclick = () => confirmAction("停用未来排期", "取消尚未派发的排期任务，保留时段历史。已派发任务继续查询原任务；需要停止本机活动任务时，请另行暂停执行器。", async () => {
    await send(path, "DELETE", { expected_revision: schedule.revision });
    toast("未来排期已停用");
    await showSchedule(nodeID);
  });
}
