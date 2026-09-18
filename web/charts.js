import { esc } from "./api.js";
export function chart(samples, key, label, color = "teal", hours = 24) {
  if (samples.length < 2)
    return `<div class="chart-empty">${esc(label)} · 历史样本不足，采样后自动显示</div>`;
  const first = samples[0].at;
  const span = Math.max(1, samples.at(-1).at - first);
  const step = Math.max(60, (hours * 3600) / 240);
  const segments = [[]];
  let previous;
  for (const sample of samples) {
    const value =
      key === "memory"
        ? (sample.metrics.memory_used / sample.metrics.memory_total) * 100
        : sample.metrics[key];
    if (previous !== undefined && sample.at - previous > step * 2)
      segments.push([]);
    segments
      .at(-1)
      .push(
        `${(((sample.at - first) / span) * 680).toFixed(1)},${(110 - Math.min(100, Math.max(0, value)) * 1.02).toFixed(1)}`,
      );
    previous = sample.at;
  }
  return `<figure class="chart"><figcaption>${esc(label)} <span>0–100% · ${samples.length} 个样本 · 缺失时段留空</span></figcaption><svg viewBox="0 0 680 120" role="img" aria-label="${esc(label)}历史曲线"><path class="gridline" d="M0 8H680 M0 59H680 M0 110H680"/>${segments.map((points) => `<polyline class="${color}" points="${points.join(" ")}"/>`).join("")}</svg><div class="chart-axis"><span>${new Date(first * 1000).toLocaleString()}</span><span>${new Date(samples.at(-1).at * 1000).toLocaleString()}</span></div></figure>`;
}
