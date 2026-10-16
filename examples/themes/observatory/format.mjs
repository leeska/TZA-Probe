export const escape = (v) => String(v ?? "").replace(/[&<>"']/g, (c) => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"})[c]);
export function bytes(v = 0) { const units = ["B", "KB", "MB", "GB", "TB"]; let n = +v, i = 0; while (n >= 1000 && i < 4) { n /= 1000; i++; } return `${n.toFixed(i ? 1 : 0)} ${units[i]}`; }
