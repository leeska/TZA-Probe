import { THEMES } from "./theme.js";
import { SKINS } from "./skins.js";
import { api, send, toast, guard } from "./api.js";
import { content } from "./admin-state.js";
// 面板设置：公开面板的全站显示项（延迟色带时间窗、访客首屏外观）。以前挂在「告警与通知」页里，
// 但它们跟告警无关，单独一页。两项都走 /admin/panel 保存（见下方注释）。
export async function renderSettings() {
  const settings = await api("/admin/panel");
  // 时间范围给几个常用档位（分钟是接口单位，档位只是界面）：
  // 探测结果保留 7 天，所以更长的一律取到保留上限，不另设选项。
  const windows = [
    [60, "1 小时"],
    [360, "6 小时"],
    [1440, "24 小时"],
    [4320, "3 天"],
    [10080, "7 天（上限）"],
  ];
  const windowNow = Number(settings.latency_window_minutes ?? 60);
  const windowOptions = (windows.some(([m]) => m === windowNow) ? windows : [...windows, [windowNow, `${windowNow} 分钟`]])
    .sort((a, b) => a[0] - b[0])
    .map(([m, label]) => `<option value="${m}"${m === windowNow ? " selected" : ""}>${label}</option>`)
    .join("");
  // 后台存的值直接亮在下拉框里；认不出来（未配置 / 手改过库）就显示内置默认。
  const themeNow = THEMES.some((t) => t.id === settings.default_theme) ? settings.default_theme : "auto";
  const skinNow = SKINS.some((s) => s.id === settings.default_skin) ? settings.default_skin : "glass";
  content().innerHTML = `<section class="page-heading"><div><div class="eyebrow">PUBLIC PANEL</div><h1>面板设置</h1><p>公开面板的全站显示选项，以及数据备份。</p></div></section><section class="panel webhook-panel"><div><h3>延迟色带</h3><p>公开卡片的延迟色带每格一次探测，固定画最近 10 次；早于这个范围的探测不再显示，那几格留空。</p><small>探测正常时色带总是满的，这一项只在探测间隔很长或节点停过一阵时看得出区别。探测结果保留 7 天。</small></div><div class="webhook-actions"><label class="window-field">只显示最近<select id="latency-window">${windowOptions}</select></label><button id="save-window" class="primary">保存</button></div></section><section class="panel webhook-panel"><div><h3>默认外观</h3><p>访客第一次打开公开面板时使用的明暗与风格。</p><small>访客在面板右上角切换过之后，以访客自己的选择为准。</small></div><div class="webhook-actions"><label class="window-field">明暗<select id="default-theme">${THEMES.map((t) => `<option value="${t.id}"${t.id === themeNow ? " selected" : ""}>${t.name}</option>`).join("")}</select></label><label class="window-field">风格<select id="default-skin">${SKINS.map((s) => `<option value="${s.id}"${s.id === skinNow ? " selected" : ""}>${s.name}</option>`).join("")}</select></label><button id="save-look" class="primary">保存</button></div></section><section class="panel webhook-panel"><div><h3>数据备份</h3><p>下载整个数据库的一份快照：节点、任务、历史与设置都在里面。不用停服，面板和 Agent 上报照常。</p><small>升级到会改数据库结构的版本之前先下载一份。恢复时先停服，把它放进数据目录、命名为 tza-probe.db（替换原文件），并删掉同目录的 tza-probe.db-wal 与 tza-probe.db-shm，再启动。</small></div><div class="webhook-actions"><a id="download-backup" class="button primary" href="/api/v1/admin/backup" download>下载备份</a></div></section>`;
  // 面板显示项走 /admin/panel：通知那个出口的语义是「空值即清空」，
  // 用同一个 PUT 保存显示项会顺手把 webhook 或 Bot Token 清掉。
  content().querySelector("#save-window").onclick = guard(async () => {
    const minutes = Number(content().querySelector("#latency-window").value);
    await send("/admin/panel", "PUT", { latency_window_minutes: minutes });
    await renderSettings();
    const label = (content().querySelector("#latency-window").selectedOptions[0]?.textContent || `${minutes} 分钟`).replace(/（.*）/, "");
    toast(`延迟色带只显示最近 ${label}内的探测`);
  });
  // 前台默认外观：访客还没自己选过时的第一屏。保存后即写进 /theme-default.js，
  // 下一次打开（包括别的访客）就是新值；当前这份重渲染一遍让人当场看到。
  content().querySelector("#save-look").onclick = guard(async () => {
    const theme = content().querySelector("#default-theme").value;
    const skin = content().querySelector("#default-skin").value;
    await send("/admin/panel", "PUT", { default_theme: theme, default_skin: skin });
    await renderSettings();
    toast(`前台默认外观：${THEMES.find((t) => t.id === theme).name} · ${SKINS.find((s) => s.id === skin).name}`);
  });
}
