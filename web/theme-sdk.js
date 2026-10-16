// Public frontend SDK v1. Classic script: usable with any framework or none.
// Include before the theme bundle: <script src="/theme-sdk.js"></script>.
(function () {
  "use strict";
  const protocol = "tza-frontend";
  const pending = new Map(), listeners = new Set();
  let sequence = 0, state, resolveReady, rejectReady;
  const ready = new Promise((resolve, reject) => { resolveReady = resolve; rejectReady = reject; });
  // Avoid an unhandled rejection in a static frontend that does not consume ready.
  ready.catch(() => {});
  const send = (data) => parent.postMessage({ protocol, version: 1, ...data }, "*");
  const hello = setInterval(() => send({ event: "hello" }), 100);
  const deadline = setTimeout(() => { clearInterval(hello); rejectReady(new Error("请从 Probe 主页面加载此主题")); }, 12000);
  addEventListener("message", (event) => {
    const data = event.data;
    if (event.source !== parent || data?.protocol !== protocol || data.version !== 1) return;
    if (data.event === "context") {
      state = data.value;
      clearInterval(hello);
      clearTimeout(deadline);
      document.documentElement.dataset.theme = state.mode;
      resolveReady(state);
      for (const fn of listeners) fn(state);
    } else if (pending.has(data.id)) {
      const request = pending.get(data.id);
      pending.delete(data.id);
      clearTimeout(request.timer);
      data.error ? request.reject(new Error(data.error)) : request.resolve(data.value);
    }
  });
  const call = async (method, args) => {
    await ready;
    const id = String(++sequence);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(id); reject(new Error("前台请求超时")); }, 20000);
      pending.set(id, { resolve, reject, timer });
      send({ id, method, args });
    });
  };
  const reportError = (error) => send({ event: "error", message: String(error?.message || error).slice(0, 300) });
  window.TZA = Object.freeze({
    version: 1, ready,
    get context() { return state; },
    api: (path) => call("api", { path }),
    setSkin: (id) => call("setSkin", { id }),
    setMode: (mode) => call("setMode", { mode }),
    navigate: (path) => call("navigate", { path }),
    openAdmin: () => call("openAdmin"),
    storage: Object.freeze({ get: (key) => call("storage.get", { key }), set: (key, value) => call("storage.set", { key, value }) }),
    onContext(fn) { listeners.add(fn); if (state) fn(state); return () => listeners.delete(fn); },
    reportError,
  });
  addEventListener("error", (event) => { if (event.error || event.target?.tagName === "SCRIPT") reportError(event.error || "主题脚本加载失败"); }, true);
  addEventListener("unhandledrejection", (event) => reportError(event.reason));
  addEventListener("pagehide", () => {
    clearInterval(hello); clearTimeout(deadline);
    for (const request of pending.values()) { clearTimeout(request.timer); request.reject(new Error("主题已关闭")); }
    pending.clear(); listeners.clear();
  });
  if (parent !== window) send({ event: "hello" });
})();
