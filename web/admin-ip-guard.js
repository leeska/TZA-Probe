// 地区偏移检测结果的判读（任务列表、检测结果弹窗共用）与开通命令。

const active = job => ["queued", "running"].includes(job.state);
const fresh = timestamp => timestamp && Date.now() / 1000 - timestamp < 45;
const sameEnvironment = (first, second) => first && second && ["expected_ip", "country", "locale", "timezone"].every(key => first[key] === second[key]) && ["latitude", "longitude", "country", "timezone"].every(key => first.position?.[key] === second.position?.[key]);
const reasons = { http_denied: "服务拒绝访问，未取得地区信息", http_forbidden: "服务拒绝此出口访问（403），未取得地区信息", challenge_detected: "遇到验证页面，暂时无法判断", origin_not_allowed: "页面跳转到未批准的地址", egress_mismatch: "出口 IP 与设置不一致", local_permission_required: "此节点尚未开通检测出口" };
export const serverInstall = "curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh -s -- --with-browser";
export const nodeEnable = () => `curl -fsSL ${location.origin}/install.sh | sh -s -- --endpoint ${location.origin} --ipguard`;

const countryLabel = code => {
  try { return `${new Intl.DisplayNames(["zh-CN"], { type: "region" }).of(code)}（${code}）`; }
  catch { return code; }
};

export function serviceReading(node, service, jobs) {
  if (!node.policy?.enabled) return { label: "未开启", tone: "", note: "加入已启用的地区偏移检测任务后自动检查" };
  const terms = service === "google_terms";
  const latest = jobs.find(job => job.node_id === node.id && job.service === (terms ? "google" : service) && (!terms || job.phase !== "experiment") && sameEnvironment(job.policy, node.policy));
  if (!latest) return { label: "待检测", tone: "", note: "等待首次检测" };
  if (active(latest)) return { label: latest.phase === "experiment" ? "正在校正" : latest.phase === "followup" ? "正在复测" : "检测中", tone: "", note: "完成后自动更新" };
  const time = latest.finished_at ? new Date(latest.finished_at * 1000).toLocaleString() : "";
  if (Date.now() / 1000 - latest.finished_at > 86400) return { label: "已过期", tone: "", note: `上次检测 ${time}` };
  if (terms) {
    const observation = latest.result?.google_terms;
    if (!observation) return { label: "待检测", tone: "", note: "下次 Google 检测时同时读取条款地区" };
    const country = observation.page?.signal === "google_terms_country" && /^[A-Z]{2}$/.test(observation.page.value) ? observation.page.value : "";
    const comparison = country ? `实际：${countryLabel(country)} · 目标：${countryLabel(node.policy.country)} · ${time}` : "";
    if (observation.assessment === "target_observed" && country === node.policy.country) return { label: "正常", tone: "good", note: comparison };
    if (observation.assessment === "other_region" && country && country !== node.policy.country) return { label: "地区偏移", tone: "bad", note: comparison };
    return { label: "未知", tone: "", note: `${reasons[observation.reason] || "未取得条款适用地区"} · ${time}` };
  }
  if (latest.phase === "experiment") return { label: latest.state === "completed" ? "待复测" : "未知", tone: "", note: latest.state === "completed" ? "已尝试校正，等待独立复测" : "校正未完成，不能确认恢复" };
  const result = latest.result;
  const country = result?.page?.signal === (service === "google" ? "google_page_country" : "ads_library_country") && /^[A-Z]{2}$/.test(result.page.value) ? result.page.value : "";
  const comparison = country ? `实际：${countryLabel(country)} · 目标：${countryLabel(node.policy.country)} · ${time}` : "";
  if (latest.state === "completed" && result?.assessment === "target_observed" && country === node.policy.country) return { label: "正常", tone: "good", note: comparison };
  if (latest.state === "completed" && result?.assessment === "other_region" && country && country !== node.policy.country) return { label: "地区偏移", tone: "bad", note: comparison };
  if (["http_denied", "http_forbidden"].includes(result?.reason)) return { label: "访问受限", tone: "", note: `${reasons[result.reason]} · ${time}` };
  return { label: "未知", tone: "", note: `${reasons[result?.reason] || (result?.page?.signal === "homepage_hkredirect" ? "旧检测没有实际地区证据，等待重新检测" : "未取得实际地区，暂时无法与目标比较")} ${time}` };
}
