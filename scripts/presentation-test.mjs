import assert from "node:assert/strict";
import { chart } from "../web/charts.js";
const samples = [0, 60, 3600, 3660].map((at) => ({
  at,
  metrics: { cpu: 25, memory_used: 50, memory_total: 100 },
}));
const html = chart(samples, "cpu", "CPU", "teal", 1);
assert.equal(
  (html.match(/<polyline /g) || []).length,
  2,
  "offline gap must not be bridged",
);
assert(
  html.includes("11.1,84.5"),
  "timestamps must determine horizontal spacing",
);
assert(html.includes("680.0,84.5"));
assert(
  !chart([], "cpu", "<script>").includes("<script>"),
  "labels must be escaped",
);
console.log(
  "PASS: time-proportional chart, offline gaps, empty history and escaped labels.",
);
