// Deliberately independent from the core chart renderer, loaded as a split chunk.
export function draw(samples) {
  const values = samples.map((s) => Math.max(0, Math.min(100, s.metrics?.cpu || 0)));
  const points = values.map((v, i) => `${(i / Math.max(1, values.length - 1) * 900).toFixed(2)},${(180 - v * 1.8).toFixed(2)}`).join(" ");
  return values.length ? `<svg class="history" viewBox="0 0 900 180" role="img" aria-label="CPU 历史曲线"><path d="M0 0H900M0 90H900M0 180H900"/><polyline points="${points}"/></svg>` : '<p class="muted">暂无历史数据</p>';
}
