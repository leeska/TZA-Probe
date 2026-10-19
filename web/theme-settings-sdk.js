// Developer settings document bridge v1. Never exposes cookies or arbitrary APIs.
(() => {
  const protocol = "tza-theme-settings", pending = new Map(), listeners = new Set();
  let sequence = 0, state, resolve;
  const ready = new Promise((r) => { resolve = r; });
  const send = (data) => parent.postMessage({ protocol, version: 1, ...data }, "*");
  const hello = setInterval(() => send({ event: "hello" }), 150);
  const deadline = setTimeout(() => clearInterval(hello), 12000);
  addEventListener("message", (event) => {
    const data = event.data;
    if (event.source !== parent || data?.protocol !== protocol || data.version !== 1) return;
    if (data.event === "context") {
      state = data.value; clearInterval(hello); clearTimeout(deadline);
      document.documentElement.dataset.theme = state.mode;
      resolve(state); for (const fn of listeners) fn(state);
    } else if (pending.has(data.id)) {
      const request = pending.get(data.id); pending.delete(data.id); clearTimeout(request.timer);
      data.error ? request.reject(new Error(data.error)) : request.resolve(data.value);
    }
  });
  const call = async (method, args) => {
    await ready;
    const id = String(++sequence);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(id); reject(new Error("设置请求超时")); }, method === "addAddon" ? 300000 : 30000);
      pending.set(id, { resolve, reject, timer }); send({ id, method, args });
    });
  };
  window.TZASettings = Object.freeze({
    ready, get context() { return state; },
    save: (patch) => call("save", { patch }),
    addAddon: () => call("addAddon"),
    uploadAddon: (file) => call("uploadAddon", { file }),
    bindNode: (node, decoration) => call("bindNode", { node, decoration }),
    onContext(fn) { listeners.add(fn); if (state) fn(state); return () => listeners.delete(fn); },
  });
  addEventListener("pagehide", () => { clearInterval(hello); clearTimeout(deadline); for (const r of pending.values()) { clearTimeout(r.timer); r.reject(new Error("设置已关闭")); } pending.clear(); listeners.clear(); });
  send({ event: "hello" });
})();
