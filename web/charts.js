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

const px = (value) => Math.round(value * 100) / 100 + "px";

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
    // 有常驻读数框的多折线图：指针只驱动竖线、圆点和图下方的读数框，图上不出现浮层；
    // 单序列的主机图没有读数框，仍用跟着指针的浮层（位置逻辑挑曲线更空的一侧）。
    const legendCells = [...figure.querySelectorAll("[data-legend-value]")];
    const legendMode = legendCells.length > 0;
    const readoutTime = figure.querySelector("[data-readout-time]");
    const latestValue = (s) => {
      const points = Array.isArray(s.points) ? s.points : [];
      for (let i = points.length - 1; i >= 0; i -= 1) {
        if (points[i][1] != null && Number.isFinite(points[i][1])) return points[i][1];
      }
      return null;
    };
    // 指针离开后，图下方的读数框回到“最近一次采样”。
    const showLatest = () => {
      series.forEach((s, index) => {
        const cell = legendCells[index];
        if (cell) cell.textContent = formatValue(latestValue(s), s.unit);
      });
      if (readoutTime) {
        const at = series.reduce((value, s) => {
          const points = Array.isArray(s.points) ? s.points : [];
          const last = points.length ? points[points.length - 1][0] : 0;
          return last > value ? last : value;
        }, 0);
        if (at) readoutTime.textContent = stamp(at).full;
      }
    };
    const draw = (event) => {
      const rect = svg.getBoundingClientRect();
      const box = body.getBoundingClientRect();
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
      // 位置一律按 svg 的实际盒子换算：按 .chart-body 的百分比会被坐标轴和图例的
      // 高度带偏（数据点因此落在曲线之外）。竖线只覆盖绘图区，不越到横坐标外面。
      const x = rect.left - box.left + ratio * rect.width;
      const top = rect.top - box.top;
      guide.style.left = px(x);
      guide.style.top = px(top);
      guide.style.height = px(rect.height);
      const only = rows.length === 1 ? rows[0] : null;
      // 数值 → 绘图区里的 y：纵轴 0 在 110、量程在 8（viewBox 高 120）。
      const plotY = (value) =>
        top + ((110 - (Math.min(ceiling, Math.max(0, value)) / ceiling) * 102) / 120) * rect.height;
      if (only && only.value != null && Number.isFinite(only.value) && ceiling > 0) {
        dot.hidden = false;
        dot.className = "chart-dot " + (only.color || "");
        dot.style.left = px(x);
        dot.style.top = px(plotY(only.value));
      } else {
        dot.hidden = true;
      }
      overlay.hidden = false;
      // 有常驻读数框的图（多折线）：读数写进图下方的框里，图上只留竖线和圆点，曲线不被任何浮层遮住。
      if (legendMode) {
        rows.forEach((row, index) => {
          const cell = legendCells[index];
          if (cell) cell.textContent = formatValue(row.value, row.unit);
        });
        if (readoutTime) readoutTime.textContent = stamp(rows[0].at).full;
        tip.hidden = true;
        return;
      }
      tip.hidden = false;
      tip.innerHTML =
        `<b>${esc(stamp(rows[0].at).full)}</b>` +
        rows
          .map(
            (row) =>
              `<span title="${esc(row.full || row.label || "")}"><i class="${esc(row.color || "")}"></i>${esc(row.label || "")}<em>${esc(formatValue(row.value, row.unit))}</em></span>`,
          )
          .join("");
      // 读数框的位置：先试绘图区正中，那一带正好压着曲线就让到曲线更少的一侧——看这一列
      // 各条曲线的 y，整体偏上就贴下沿、偏下就贴上沿。横向仍跟着指针走并夹在图表里。
      const tipWidth = tip.offsetWidth;
      const tipHeight = tip.offsetHeight;
      tip.style.left = px(Math.min(Math.max(x - tipWidth / 2, 0), Math.max(0, box.width - tipWidth)));
      const plotTop = top;
      const plotBottom = top + rect.height;
      const clampTop = (value) => Math.min(Math.max(value, plotTop), Math.max(plotTop, plotBottom - tipHeight));
      const curve = rows
        .filter((row) => row.value != null && Number.isFinite(row.value) && ceiling > 0)
        .map((row) => plotY(row.value));
      let chosen = clampTop(plotTop + (rect.height - tipHeight) / 2);
      const covers = (candidate) =>
        curve.some((y) => y > candidate - 6 && y < candidate + tipHeight + 6);
      if (curve.length && covers(chosen)) {
        const mean = curve.reduce((sum, y) => sum + y, 0) / curve.length;
        chosen = clampTop(mean < plotTop + rect.height / 2 ? plotBottom - tipHeight : plotTop);
      }
      tip.style.top = px(chosen);
    };
    body.addEventListener("pointermove", draw);
    body.addEventListener("pointerleave", () => {
      overlay.hidden = true;
      if (legendMode) showLatest();
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
      <svg viewBox="0 0 680 120" preserveAspectRatio="none" role="img" aria-label="${esc(label)}历史曲线，纵轴 0 到 ${esc(scaleLabel)}">
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
