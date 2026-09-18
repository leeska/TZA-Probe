export async function api(path, options = {}) {
  const response = await fetch("/api/v1" + path, {
    credentials: "same-origin",
    ...options,
    headers: { "Content-Type": "application/json", ...options.headers },
  });
  if (!response.ok) {
    let data;
    try {
      data = await response.json();
    } catch {}
    const error = new Error(data?.error || `请求失败 (${response.status})`);
    error.status = response.status;
    throw error;
  }
  return response.status === 204 ? null : response.json();
}
export const send = (path, method, data) =>
  api(path, {
    method,
    ...(data === undefined ? {} : { body: JSON.stringify(data) }),
  });
export const esc = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
export function toast(message) {
  const el = document.querySelector("#toast");
  el.textContent = message;
  el.classList.add("visible");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => el.classList.remove("visible"), 5000);
}
export function modal(title, body) {
  const d = document.querySelector("#dialog");
  delete d.dataset.nodeId;
  d.innerHTML = `<div class="modal-head"><h2 id="dialog-title">${esc(title)}</h2><button type="button" class="icon close" aria-label="关闭">×</button></div>${body}`;
  d.querySelector(".close").onclick = () => d.close();
  d.onclick = (e) => {
    const rect = d.getBoundingClientRect();
    const isInDialog =
      rect.top <= e.clientY &&
      e.clientY <= rect.top + rect.height &&
      rect.left <= e.clientX &&
      e.clientX <= rect.left + rect.width;
    if (!isInDialog) d.close();
  };
  if (!d.open) d.showModal();
  return d;
}
export function guard(fn) {
  return async (...args) => {
    try {
      await fn(...args);
    } catch (e) {
      toast(e.message);
    }
  };
}
