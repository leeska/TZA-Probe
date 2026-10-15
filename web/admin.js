import { themeButton } from "./theme.js";
import { skinButton } from "./skins.js";
import { api, send, guard } from "./api.js";
import { renderAlerts } from "./admin-alerts.js";
import { renderThemes } from "./admin-themes.js";
import { renderPlugins } from "./admin-plugins.js";
import { nodesPage } from "./admin-nodes.js";
import { renderSettings } from "./admin-settings.js";
import { state, content, loadNodes, setRenderer } from "./admin-state.js";
import { tasksPage } from "./admin-tasks.js";
import { brandHTML } from "./brand.js";
export async function mountAdmin(root) {
  setRenderer(render);
  state.container = root;
  try {
    state.serverVersion = (await api("/session")).version || "";
    shell();
    await render();
  } catch (e) {
    if (e.status === 401) login();
    else throw e;
  }
}
function login() {
  state.container.innerHTML = `<div class="login-layout"><section class="login-story">${brandHTML()}<div><div class="eyebrow">SMALL STACK. CLEAR CONTROL.</div><h1>少一点复杂，<br>多一点掌控。</h1><p>主机、网络、用量与提醒。<br>一套属于自己的基础设施观测工具。</p></div><div class="login-story-foot"><small>TZA PROBE / CONTROL PLANE</small>${skinButton()}${themeButton()}</div></section><main class="login-panel"><form id="login"><div class="eyebrow">WELCOME BACK</div><h2>进入管理控制台</h2><p>使用后台用户名与密码登录。</p><label>用户名<input type="text" name="username" required autocomplete="username" autocapitalize="off" spellcheck="false" autofocus></label><label>密码<input type="password" name="password" required autocomplete="current-password"></label><p class="hint">忘记密码时在服务器执行 tza-server reset-password 重置。</p><button class="primary" type="submit">登录控制台 →</button><a href="/">← 返回公开面板</a></form></main></div>`;
  const form = state.container.querySelector("form");
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const button = e.target.querySelector("button[type=submit]");
    button.disabled = true;
    const data = new FormData(e.target);
    try {
      await send(
        "/login",
        "POST",
        { username: data.get("username"), password: data.get("password") },
      );
      state.serverVersion = (await api("/session")).version || "";
      shell();
      await render();
    } finally {
      button.disabled = false;
    }
  });
}
function shell() {
  state.container.innerHTML = `<div class="admin-layout"><aside class="sidebar">${brandHTML()}<div class="sidebar-caption">WORKSPACE</div><nav><button data-tab="nodes"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="6" height="6" rx="1.5"/><rect x="11" y="3" width="6" height="6" rx="1.5"/><rect x="3" y="11" width="6" height="6" rx="1.5"/><rect x="11" y="11" width="6" height="6" rx="1.5"/></svg><span>节点管理</span></button><button data-tab="tasks"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><circle cx="10" cy="10" r="3"/><circle cx="10" cy="10" r="0.8" fill="currentColor"/></svg><span>网络探测</span></button><button data-tab="alerts"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><polyline points="10 6 10 10 13 12"/></svg><span>告警与通知</span></button><button data-tab="themes"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 3.2c-3.9 0-7 2.5-7 5.6 0 1.9 1.6 3 3.4 3h1.1c.9 0 1.5.9 1 1.7-.6 1 .1 2.3 1.4 2.3 3.9 0 7-2.6 7-5.8S13.9 3.2 10 3.2Z"/><circle cx="7.4" cy="8.2" r="1"/><circle cx="11" cy="7.2" r="1"/></svg><span>主题</span></button><button data-tab="plugins"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 3.5h4v2.2a1.6 1.6 0 1 0 3.2 0V5.5H16.5V9h-1.3a1.6 1.6 0 1 0 0 3.2h1.3v4.3H12v-1.3a1.6 1.6 0 1 0-3.2 0v1.3H3.5V12h1.3a1.6 1.6 0 1 0 0-3.2H3.5V5.5H8z"/></svg><span>插件</span></button><button data-tab="settings"><svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="4" y1="6" x2="16" y2="6"/><line x1="4" y1="14" x2="16" y2="14"/><circle cx="8" cy="6" r="2" fill="currentColor"/><circle cx="12" cy="14" r="2" fill="currentColor"/></svg><span>面板设置</span></button></nav><div class="sidebar-bottom"><a href="/">公开面板 ↗</a>${skinButton("sidebar-skin")}${themeButton("sidebar-theme")}<button id="logout">退出登录</button><small>独立运行 · TZA PROBE</small></div></aside><main class="admin-main"><header class="admin-top"><span>控制台 <span class="muted">/</span> <b id="breadcrumb"></b></span><span class="pill" id="fleet-status" aria-live="polite"></span></header><div id="admin-content"></div></main></div>`;
  state.container.querySelectorAll("[data-tab]").forEach(
    (b) =>
      (b.onclick = guard(async () => {
        state.tab = b.dataset.tab;
        await render();
      })),
  );
  state.container.querySelector("#logout").onclick = guard(async () => {
    await send("/logout", "POST");
    login();
  });
}
async function render() {
  // 切页时面包屑与侧栏高亮立刻换，内容要等接口回来：中间这半秒把旧内容调暗并标成忙，
  // 不然看起来像点到了「网络探测」却还停在「节点管理」。
  const area = content();
  // 换页 / 重绘会把行连同「更多」一起换掉；挪到 body 下还开着的菜单一并清掉，不留孤儿。
  document.querySelectorAll("body > .row-menu").forEach((m) => m.remove());
  area?.classList.add("is-loading");
  area?.setAttribute("aria-busy", "true");
  try {
    await renderTab();
  } finally {
    area?.classList.remove("is-loading");
    area?.removeAttribute("aria-busy");
  }
}
async function renderTab() {
  state.container
    .querySelectorAll("[data-tab]")
    .forEach((b) => {
      const on = b.dataset.tab === state.tab;
      b.classList.toggle("active", on);
      // 高亮只是颜色，读屏读不出「现在在哪一页」。
      if (on) b.setAttribute("aria-current", "page");
      else b.removeAttribute("aria-current");
    });
  state.container.querySelector("#breadcrumb").textContent = {
    nodes: "节点管理",
    tasks: "网络探测",
    alerts: "告警与通知",
    themes: "主题",
    plugins: "插件",
    settings: "面板设置",
  }[state.tab];
  await { nodes: nodesPage, tasks: tasksPage, alerts: renderAlerts, themes: renderThemes, plugins: renderPlugins, settings: renderSettings }[state.tab](loadNodes());
}
