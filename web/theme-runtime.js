// Host for public frontend API v1. Only this small broker lives in the parent;
// theme markup, scripts, CSS, state and routing belong to the isolated document.
import { setSkin, SKINS, skinOption } from "./skins.js";
import { preference, setPreference } from "./theme.js";

const PROTOCOL = "tza-frontend";
const PUBLIC = /^\/(nodes(?:\/live|\/[a-zA-Z0-9_-]{1,64}\/(?:history|network-history|observations))?|observations|latency-samples|unlock-catalog)$/;
const RESERVED = /^\/(?:admin|api|themes|dl|mcp|healthz)(?:\/|$)/;

export function publicURL(value) {
  if (typeof value !== "string" || !value.startsWith("/") || value.length > 2048) throw new Error("需要公开 API 路径");
  const url = new URL(value, location.origin);
  if (url.origin !== location.origin || !PUBLIC.test(url.pathname) || url.hash) throw new Error("主题只能读取公开 API");
  return "/api/v1" + url.pathname + url.search;
}

export function startFrontend(skin) {
  const host = document.querySelector("#app");
  const css = document.createElement("link");
  css.rel = "stylesheet";
  css.href = "/theme-runtime.css";
  document.head.append(css);
  document.documentElement.dataset.frontend = skin.id;
  const frame = document.createElement("iframe");
  frame.id = "theme-frontend";
  frame.title = skin.name;
  frame.setAttribute("sandbox", "allow-scripts");
  frame.setAttribute("referrerpolicy", "same-origin");
  frame.src = `/themes/${skin.id}/${skin.frontend.entry}?v=${encodeURIComponent(skin.rev || "")}`;
  let alive = true, connected = false, inFlight = 0;
  const controllers = new Set();
  const context = () => ({
    api_version: 1,
    theme: { ...skin, options: Object.fromEntries((skin.settings || []).map((st) => [st.id, skinOption(skin.id, st.id)])) },
    themes: window.__tzaThemes || SKINS,
    brand: window.__tzaBrand || {},
    mode: document.documentElement.dataset.theme,
    preference: preference(),
    route: location.pathname + location.search + location.hash,
    adminURL: "/admin",
  });
  const post = (message) => { if (alive) frame.contentWindow?.postMessage({ protocol: PROTOCOL, version: 1, ...message }, "*"); };
  const broadcast = () => post({ event: "context", value: context() });
  const clean = () => {
    alive = false;
    clearTimeout(timer);
    removeEventListener("message", receive);
    removeEventListener("popstate", broadcast);
    removeEventListener("hashchange", broadcast);
    removeEventListener("pagehide", clean);
    observer.disconnect();
    for (const controller of controllers) controller.abort();
  };
  const fail = (error) => {
    clean();
    delete document.documentElement.dataset.frontend;
    const section = document.createElement("main");
    section.className = "empty";
    const title = document.createElement("h1");
    title.textContent = "主题前台暂时无法加载";
    const message = document.createElement("p");
    message.textContent = String(error?.message || error).slice(0, 300);
    const recover = document.createElement("a");
    recover.className = "button";
    recover.href = "/?tza-theme=shadcn";
    recover.textContent = "使用默认前台";
    const admin = document.createElement("a");
    admin.href = "/admin?tza-theme=shadcn";
    admin.textContent = "管理主题";
    section.append(title, message, recover, " ", admin);
    host.replaceChildren(section);
  };
  async function execute(method, args = {}) {
    switch (method) {
      case "context": return context();
      case "api": {
        const url = publicURL(args.path);
        const controller = new AbortController();
        controllers.add(controller);
        const deadline = setTimeout(() => controller.abort(), 15000);
        try {
          // Deliberately omit the parent's session and all authorization headers.
          const response = await fetch(url, { credentials: "omit", redirect: "error", signal: controller.signal, headers: { Accept: "application/json" } });
          if (!response.ok) throw new Error(`公开 API 请求失败 (${response.status})`);
          return await response.json();
        } finally { clearTimeout(deadline); controllers.delete(controller); }
      }
      case "setSkin": {
        if (!SKINS.some((t) => t.id === args.id)) throw new Error("主题不存在");
        setSkin(args.id);
        return null;
      }
      case "setMode": {
        if (!["light", "dark", "auto"].includes(args.mode)) throw new Error("明暗取值无效");
        setPreference(args.mode);
        broadcast();
        return null;
      }
      case "navigate": {
        if (typeof args.path !== "string" || !args.path.startsWith("/") || args.path.length > 2048) throw new Error("路由无效");
        const url = new URL(args.path, location.origin);
        if (url.origin !== location.origin || RESERVED.test(url.pathname)) throw new Error("不能导航到系统路径");
        url.searchParams.delete("tza-theme");
        history.pushState(null, "", url.pathname + url.search + url.hash);
        broadcast();
        return null;
      }
      case "openAdmin": location.assign("/admin"); return null;
      case "storage.get":
      case "storage.set": {
        if (typeof args.key !== "string" || !/^[a-zA-Z0-9_.-]{1,64}$/.test(args.key)) throw new Error("存储键无效");
        const key = `tza-frontend:${skin.id}:${args.key}`;
        if (method === "storage.get") return JSON.parse(localStorage.getItem(key) || "null");
        if (args.value == null) localStorage.removeItem(key);
        else {
          const value = JSON.stringify(args.value);
          if (value.length > 32768) throw new Error("单项主题偏好最多 32 KB");
          localStorage.setItem(key, value);
        }
        return null;
      }
      default: throw new Error("不支持的前台方法");
    }
  }
  async function receive(event) {
    const data = event.data;
    if (!alive || event.source !== frame.contentWindow || event.origin !== "null" || data?.protocol !== PROTOCOL || data.version !== 1) return;
    if (data.event === "hello") {
      connected = true;
      clearTimeout(timer);
      broadcast();
      return;
    }
    if (data.event === "error") { fail(data.message || "主题运行失败"); return; }
    if (typeof data.id !== "string" || data.id.length > 64) return;
    if (inFlight >= 8) { post({ id: data.id, error: "主题请求过多，请稍后重试" }); return; }
    inFlight++;
    try { post({ id: data.id, value: await execute(data.method, data.args) }); }
    catch (error) { post({ id: data.id, error: error.message }); }
    finally { inFlight--; }
  }
  const observer = new MutationObserver(() => { if (connected) broadcast(); });
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  const timer = setTimeout(() => fail("主题未连接前台 API，请检查入口或使用默认前台"), 12000);
  frame.addEventListener("error", () => fail("主题入口加载失败"));
  addEventListener("message", receive);
  addEventListener("popstate", broadcast);
  addEventListener("hashchange", broadcast);
  addEventListener("pagehide", clean);
  host.replaceChildren(frame);
}
