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

  const gradId = `chart-grad-${color}`;
  const gradColor = color === "orange" ? "#ff9500" : "#007aff";

  const renderedSegments = segments
    .filter((pts) => pts.length > 0)
    .map((points) => {
      const firstX = points[0].split(",")[0];
      const lastX = points[points.length - 1].split(",")[0];
      const areaPoints = `${firstX},112 ${points.join(" ")} ${lastX},112`;
      return `
        <polygon class="chart-area ${color}" points="${areaPoints}" fill="url(#${gradId})" />
        <polyline class="chart-line ${color}" points="${points.join(" ")}" />
      `;
    })
    .join("");

  return `
    <figure class="chart">
      <figcaption>
        <strong>${esc(label)}</strong>
        <span>0–100% · ${samples.length} 个历史采样 · 自动聚合</span>
      </figcaption>
      <svg viewBox="0 0 680 120" role="img" aria-label="${esc(label)}历史曲线">
        <defs>
          <linearGradient id="${gradId}" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="${gradColor}" stop-opacity="0.32" />
            <stop offset="60%" stop-color="${gradColor}" stop-opacity="0.08" />
            <stop offset="100%" stop-color="${gradColor}" stop-opacity="0.0" />
          </linearGradient>
        </defs>
        <path class="gridline" d="M0 8H680 M0 59H680 M0 110H680" />
        ${renderedSegments}
      </svg>
      <div class="chart-axis">
        <span>${new Date(first * 1000).toLocaleString("zh-CN", { hour12: false })}</span>
        <span>${new Date(samples.at(-1).at * 1000).toLocaleString("zh-CN", { hour12: false })}</span>
      </div>
    </figure>
  `;
}
