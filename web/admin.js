import { themeButton } from "./theme.js";
import { skinButton } from "./skins.js";
import { api, send, guard } from "./api.js";
import { state, content, loadNodes, setRenderer } from "./admin-state.js";
import { brandHTML } from "./brand.js";
import { adminPage, navHTML } from "./admin-pages.js";
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
  state.container.innerHTML = `<div class="login-layout"><section class="login-story">${brandHTML()}<div><div class="eyebrow">SMALL STACK. CLEAR CONTROL.</div><h1>少一点复杂，<br>多一点掌控。</h1><p>主机、网络、用量与提醒。<br>一套属于自己的基础设施观测工具。</p></div><div class="login-story-foot"><small>TZA PROBE / CONTROL PLANE</small>${skinButton()}${themeButton()}</div></section><main class="login-panel"><form id="login"><div class="eyebrow">WELCOME BACK</div><h2>进入管理控制台</h2><p>使用后台用户名与密码登录。</p><label>用户名<input type="text" name="username" required autocomplete="username" autocapitalize="off" spellcheck="false" autofocus></label><label>密码<input type="password" name="password" required autocomplete="current-password"></label><label class="totp-field" hidden>两步验证码<input type="text" name="code" inputmode="numeric" pattern="[0-9 ]{6,7}" maxlength="7" autocomplete="one-time-code" placeholder="身份验证器里的 6 位数"></label><p class="hint">忘记密码时在服务器执行 tza-server reset-password 重置。</p><button class="primary" type="submit">登录控制台 →</button><a href="/">← 返回公开面板</a></form></main></div>`;
  const form = state.container.querySelector("form");
  form.onsubmit = guard(async (e) => {
    e.preventDefault();
    const button = e.target.querySelector("button[type=submit]");
    button.disabled = true;
    const data = new FormData(e.target);
    const codeField = e.target.querySelector(".totp-field");
    try {
      try {
        await send(
          "/login",
          "POST",
          { username: data.get("username"), password: data.get("password"), code: data.get("code") || "" },
        );
      } catch (err) {
        // 密码正确但开了两步验证：亮出验证码输入框。
        if (err.data?.totp && codeField.hidden) {
          codeField.hidden = false;
          codeField.querySelector("input").required = true;
          codeField.querySelector("input").focus();
        }
        throw err;
      }
      state.serverVersion = (await api("/session")).version || "";
      shell();
      await render();
    } finally {
      button.disabled = false;
    }
  });
}
function shell() {
  state.container.innerHTML = `<div class="admin-layout"><aside data-part="sidebar" class="sidebar">${brandHTML()}<div class="sidebar-caption">WORKSPACE</div><nav>${navHTML()}</nav><div class="sidebar-bottom"><a href="/">公开面板 ↗</a>${skinButton("sidebar-skin")}${themeButton("sidebar-theme")}<button id="logout">退出登录</button><small>独立运行 · TZA PROBE</small></div></aside><main class="admin-main"><header data-part="admin-top" class="admin-top"><span>控制台 <span class="muted">/</span> <b id="breadcrumb"></b></span><span class="pill" id="fleet-status" aria-live="polite"></span></header><div id="admin-content"></div></main></div>`;
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
  // 节点页的批量工具栏随页面一起换掉；它给 toast 与内容区让出的位置也一并收回。
  document.body.classList.remove("has-batch-bar");
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
  const page = adminPage(state.tab);
  state.container.querySelector("#breadcrumb").textContent = page.label;
  await page.page(loadNodes());
}
