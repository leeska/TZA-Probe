import { esc } from "./api.js";
import { bytes } from "./format.js";

// 折线图的三件事：鼠标停在哪一列就显示那一列的时间和数值、横坐标把日期与时间分行
// 细分、多折线按 class 上色。页面 CSP 是 style-src 'self'，标记里的行内 style 会被
// 忽略（颜色因此必须来自 CSS），但 CSSOM 写入（el.style.left）可用，提示框位置走它。
export const palette = ["#ff625f", "#36c8bf", "#9b7cff", "#5a9cff", "#ffad3d", "#eb63b5", "#36c98b", "#ff873d"];
export const colorClass = (index) => `lc${index % palette.length}`;

const pad = (value) => String(value).padStart(2, "0");
export function stamp(at) {
  const d = new Date(at * 1000);
  return {
    date: `${d.getMonth() + 1}/${d.getDate()}`,
    time: `${pad(d.getHours())}:${pad(d.getMinutes())}`,
    full: `${d.getFullYear()}/${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`,
  };
}

// 横坐标刻度：在首尾之间均匀取 count 个时刻，日期与时间分行显示。
export function axisLabels(first, last, count = 5) {
  const span = Math.max(0, last - first);
  return Array.from({ length: count }, (_, i) =>
    stamp(Math.round(first + (span * i) / (count - 1))),
  );
}
export function axisRow(first, last, count = 5) {
  return `<div class="chart-axis">${axisLabels(first, last, count)
    .map((label) => `<span><b>${label.date}</b><i>${label.time}</i></span>`)
    .join("")}</div>`;
}

export function formatValue(value, unit) {
  if (value == null || !Number.isFinite(value)) return "—";
  if (unit === "%") return value.toFixed(0) + "%";
  if (unit === "B/s") return bytes(value) + "/s";
  return (value >= 100 ? value.toFixed(0) : value.toFixed(1)) + " ms";
}

// 悬停所需的数据挂在 figure 上：series 是 [{label, unit, color, points:[[at,value]]}]。
function payload(series, first, last, ceiling) {
  return `data-series="${esc(JSON.stringify(series))}" data-range="${first},${last}" data-ceiling="${Number.isFinite(ceiling) ? ceiling : 0}"`;
}

function nearest(points, at) {
  if (!points.length) return null;
  let best = points[0];
  for (const point of points) {
    if (Math.abs(point[0] - at) < Math.abs(best[0] - at)) best = point;
  }
  return best;
}

// 悬停提示：鼠标停在哪一列就报那一列每个序列的时间和数值。
export function wireCharts(root) {
  if (!root || !root.querySelectorAll) return;
  for (const figure of root.querySelectorAll("figure.chart[data-series]")) {
    if (figure.dataset.wired === "1") continue;
    figure.dataset.wired = "1";
    let series, range, ceiling;
    try {
      series = JSON.parse(figure.dataset.series);
      range = (figure.dataset.range || "").split(",").map(Number);
      ceiling = Number(figure.dataset.ceiling);
    } catch {
      continue;
    }
    const svg = figure.querySelector("svg");
    const body = figure.querySelector(".chart-body");
    if (!svg || !body || range.length !== 2 || !Number.isFinite(range[0]) || !Number.isFinite(range[1]))
      continue;
    if (!Array.isArray(series) || !series.length) continue;
    const overlay = document.createElement("div");
    overlay.className = "chart-hover";
    overlay.hidden = true;
    overlay.innerHTML =
      '<span class="chart-guide"></span><span class="chart-dot"></span><span class="chart-tip"></span>';
    body.append(overlay);
    const guide = overlay.querySelector(".chart-guide");
    const dot = overlay.querySelector(".chart-dot");
    const tip = overlay.querySelector(".chart-tip");
    const draw = (event) => {
      const rect = svg.getBoundingClientRect();
      if (!rect.width) return;
      const ratio = Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width));
      const at = range[0] + (range[1] - range[0]) * ratio;
      const rows = [];
      for (const s of series) {
        const point = nearest(Array.isArray(s.points) ? s.points : [], at);
        if (point) rows.push({ ...s, at: point[0], value: point[1] });
      }
      if (!rows.length) {
        overlay.hidden = true;
        return;
      }
      const percent = (ratio * 100).toFixed(2) + "%";
      guide.style.left = percent;
      tip.style.left = percent;
      tip.classList.toggle("flip", ratio > 0.62);
      const only = rows.length === 1 ? rows[0] : null;
      if (only && only.value != null && Number.isFinite(only.value) && ceiling > 0) {
        dot.hidden = false;
        dot.style.left = percent;
        dot.style.top =
          ((110 - (Math.min(ceiling, Math.max(0, only.value)) / ceiling) * 102) / 120) * 100 + "%";
        dot.className = "chart-dot " + (only.color || "");
      } else {
        dot.hidden = true;
      }
      tip.innerHTML =
        `<b>${esc(stamp(rows[0].at).full)}</b>` +
        rows
          .map(
            (row) =>
              `<span><i class="${esc(row.color || "")}"></i>${esc(row.label || "")}<em>${esc(formatValue(row.value, row.unit))}</em></span>`,
          )
          .join("");
      overlay.hidden = false;
    };
    body.addEventListener("pointermove", draw);
    body.addEventListener("pointerleave", () => {
      overlay.hidden = true;
    });
  }
}

export function chart(samples, key, label, color = "teal", hours = 24, options = {}) {
  if (samples.length < 2)
    return `<div class="chart-empty">${esc(label)} · 历史样本不足，采样后自动显示</div>`;

  const valueOf = (sample) => key === "memory"
    ? (sample.metrics.memory_used / sample.metrics.memory_total) * 100
    : sample.metrics[key];
  const percent = ["cpu", "memory", "failure"].includes(key);
  const unit = percent ? "%" : key.startsWith("rate_") ? "B/s" : "ms";
  const values = samples.map(valueOf).filter((v) => v != null && Number.isFinite(v));
  if (!values.length) return `<div class="chart-empty">${esc(label)} · 暂无有效样本</div>`;
  const ceiling = percent ? 100 : Math.max(1, ...values);
  // 纵坐标刻度：顶部是量程上限（百分比固定 100%，速率取峰值，延迟取历史峰值），
  // 中间是半量程，底部是 0。绘图区在 120 高的 viewBox 里，网格线固定在 8 / 59 / 110。
  const tick = (v) => formatValue(v, unit);
  const scaleLabel = tick(ceiling);
  const yAxis = `<div class="chart-y" aria-hidden="true"><span>${esc(tick(ceiling))}</span><span>${esc(tick(ceiling / 2))}</span><span>${esc(tick(0))}</span></div>`;
  const first = samples[0].at;
  const last = samples.at(-1).at;
  const span = Math.max(1, last - first);
  const step = Math.max(60, (hours * 3600) / 240);
  const segments = [[]];
  const points = [];
  let previous;

  for (const sample of samples) {
    const value = valueOf(sample);
    points.push([sample.at, value == null || !Number.isFinite(value) ? null : value]);
    if (value == null || !Number.isFinite(value)) { segments.push([]); previous = sample.at; continue; }
    if (previous !== undefined && sample.at - previous > step * 2)
      segments.push([]);
    segments
      .at(-1)
      .push(
        `${(((sample.at - first) / span) * 680).toFixed(1)},${(110 - Math.min(ceiling, Math.max(0, value)) / ceiling * 102).toFixed(1)}`,
      );
    previous = sample.at;
  }

  const gradId = `chart-grad-${options.id || key}-${color}`;
  const gradColor = color === "orange" ? "#ff9500" : "#007aff";

  const renderedSegments = segments
    .filter((pts) => pts.length > 0)
    .map((line) => {
      const firstX = line[0].split(",")[0];
      const lastX = line[line.length - 1].split(",")[0];
      const areaPoints = `${firstX},112 ${line.join(" ")} ${lastX},112`;
      return `
        <polygon class="chart-area ${color}" points="${areaPoints}" fill="url(#${gradId})" />
        <polyline class="chart-line ${color}" points="${line.join(" ")}" />
      `;
    })
    .join("");

  return `
    <figure class="chart" ${payload([{ label: options.seriesLabel || label, unit, color, points }], first, last, ceiling)}>
      <figcaption>
        <strong>${esc(label)}</strong>
        <span>0–${esc(scaleLabel)} · ${samples.length} 个历史采样 · 自动聚合</span>
      </figcaption>
      <div class="chart-plot">${yAxis}<div class="chart-body">
      <svg viewBox="0 0 680 120" role="img" aria-label="${esc(label)}历史曲线，纵轴 0 到 ${esc(scaleLabel)}">
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
      ${axisRow(first, last)}
      </div></div>
    </figure>
  `;
}
