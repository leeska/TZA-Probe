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

const rates = [0,60,120].map((at,i)=>({at,metrics:{rate_in:[1000,null,2000][i]}}));
const rateHTML = chart(rates,"rate_in","下载");
assert(rateHTML.includes("/s"));
assert(!rateHTML.includes("NaN"));
assert.equal((rateHTML.match(/<polyline /g)||[]).length,2,"unknown samples must break lines");
const failures = [0,60].map(at=>({at,metrics:{latency:null}}));
assert(!chart(failures,"latency","延迟").includes("<polyline"),"failed connections must not become zero latency");
const {installationScript} = await import("../web/agent-setup.js");
const install = installationScript({endpoint:"https://example.com",token:"secret"});
assert(install.includes("chmod 600 /etc/tza-probe/agent.json"));
assert(install.includes("systemctl restart tza-agent"));
assert(install.includes("<<'TZA_AGENT_CONFIG'"),"configuration heredoc must not expand shell substitutions");
console.log("PASS: rate units, unknown latency gaps and private offline installation script.");
