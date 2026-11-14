// 后台的页面清单：侧栏、面包屑和页面分发都从这里来。新功能模块在这里加一项即可，
// 不必再改 admin.js。顺序即侧栏顺序；id 存在 state.tab 里，也用作 data-tab。
import { renderAlerts } from "./admin-alerts.js";
import { nodesPage } from "./admin-nodes.js";
import { renderPlugins } from "./admin-plugins.js";
import { renderSettings } from "./admin-settings.js";
import { tasksPage } from "./admin-tasks.js";
import { renderThemes } from "./admin-themes.js";
import { remotePage, auditPage } from "./admin-remote.js";
import { overviewPage } from "./admin-overview.js";
import { usersPage } from "./admin-users.js";
import { proxyPage } from "./admin-proxy.js";
import { forwardPage } from "./admin-forward.js";
import { dockerPage } from "./admin-docker.js";
import { certsPage } from "./admin-certs.js";
import { backupPage } from "./admin-backup.js";
import { sitesPage } from "./admin-sites.js";

export const adminPages = [
  { id: "overview", group: "概览", label: "总览", page: overviewPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="6" height="8" rx="1.5"/><rect x="11" y="3" width="6" height="5" rx="1.5"/><rect x="11" y="10" width="6" height="7" rx="1.5"/><rect x="3" y="13" width="6" height="4" rx="1.5"/></svg>' },
  { id: "nodes", group: "节点与网络", label: "节点管理", page: nodesPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="6" height="6" rx="1.5"/><rect x="11" y="3" width="6" height="6" rx="1.5"/><rect x="3" y="11" width="6" height="6" rx="1.5"/><rect x="11" y="11" width="6" height="6" rx="1.5"/></svg>' },
  { id: "tasks", group: "节点与网络", label: "网络探测", page: tasksPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><circle cx="10" cy="10" r="3"/><circle cx="10" cy="10" r="0.8" fill="currentColor"/></svg>' },
  { id: "alerts", group: "节点与网络", label: "告警与通知", page: renderAlerts, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><polyline points="10 6 10 10 13 12"/></svg>' },
  { id: "remote", group: "节点与网络", label: "远程管理", page: remotePage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="2.5" y="3.5" width="15" height="13" rx="2"/><polyline points="6 8 8.5 10 6 12"/><line x1="10.5" y1="12" x2="14" y2="12"/></svg>' },
  { id: "users", group: "代理与使用者", label: "使用者", page: usersPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="8" cy="7" r="3"/><path d="M2.5 17c.6-3 2.8-4.5 5.5-4.5s4.9 1.5 5.5 4.5"/><path d="M13.5 4.2a3 3 0 0 1 0 5.6"/><path d="M15.5 12.8c1.2.6 1.9 2 2.1 4.2"/></svg>' },
  { id: "proxy", group: "代理与使用者", label: "代理", page: proxyPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="4.5" cy="10" r="2"/><circle cx="15.5" cy="5" r="2"/><circle cx="15.5" cy="15" r="2"/><path d="M6.4 9.1 13.6 5.9M6.4 10.9l7.2 3.2"/></svg>' },
  { id: "forward", group: "代理与使用者", label: "转发", page: forwardPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="4" cy="10" r="1.8"/><circle cx="16" cy="10" r="1.8"/><path d="M5.8 10h8.4"/><polyline points="11.5 7 14.2 10 11.5 13"/></svg>' },
  { id: "sites", group: "服务", label: "网站", page: sitesPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><path d="M3 10h14"/><path d="M10 3c2 2.2 3 4.5 3 7s-1 4.8-3 7c-2-2.2-3-4.5-3-7s1-4.8 3-7Z"/></svg>' },
  { id: "certs", group: "服务", label: "证书", page: certsPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="4" y="9" width="12" height="8.5" rx="1.8"/><path d="M6.8 9V6.5a3.2 3.2 0 0 1 6.4 0V9"/><circle cx="10" cy="13" r="1.2" fill="currentColor"/></svg>' },
  { id: "docker", group: "服务", label: "Docker", page: dockerPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="2.5" y="9" width="15" height="7.5" rx="1.5"/><rect x="4.5" y="5.5" width="3" height="3.5"/><rect x="8.5" y="5.5" width="3" height="3.5"/><rect x="8.5" y="2" width="3" height="3.5"/><rect x="12.5" y="5.5" width="3" height="3.5"/></svg>' },
  { id: "backup", group: "服务", label: "备份", page: backupPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><ellipse cx="10" cy="5" rx="6" ry="2.5"/><path d="M4 5v10c0 1.4 2.7 2.5 6 2.5s6-1.1 6-2.5V5"/><path d="M4 10c0 1.4 2.7 2.5 6 2.5s6-1.1 6-2.5"/></svg>' },
  { id: "audit", group: "系统", label: "审计日志", page: auditPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 2.5h7l3.5 3.5v11.5h-10.5z"/><polyline points="12 2.5 12 6 15.5 6"/><line x1="7.5" y1="10" x2="12.5" y2="10"/><line x1="7.5" y1="13" x2="12.5" y2="13"/></svg>' },
  { id: "themes", group: "系统", label: "主题", page: renderThemes, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 3.2c-3.9 0-7 2.5-7 5.6 0 1.9 1.6 3 3.4 3h1.1c.9 0 1.5.9 1 1.7-.6 1 .1 2.3 1.4 2.3 3.9 0 7-2.6 7-5.8S13.9 3.2 10 3.2Z"/><circle cx="7.4" cy="8.2" r="1"/><circle cx="11" cy="7.2" r="1"/></svg>' },
  { id: "plugins", group: "系统", label: "插件", page: renderPlugins, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 3.5h4v2.2a1.6 1.6 0 1 0 3.2 0V5.5H16.5V9h-1.3a1.6 1.6 0 1 0 0 3.2h1.3v4.3H12v-1.3a1.6 1.6 0 1 0-3.2 0v1.3H3.5V12h1.3a1.6 1.6 0 1 0 0-3.2H3.5V5.5H8z"/></svg>' },
  { id: "settings", group: "系统", label: "面板设置", page: renderSettings, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="4" y1="6" x2="16" y2="6"/><line x1="4" y1="14" x2="16" y2="14"/><circle cx="8" cy="6" r="2" fill="currentColor"/><circle cx="12" cy="14" r="2" fill="currentColor"/></svg>' },
];

export const adminPage = (id) => adminPages.find((p) => p.id === id) || adminPages[0];

// 侧栏按组排：每组一个小标题。入口多了以后平铺一列在矮屏上会被截掉，分组后好找，也能滚动。
export const navHTML = () => {
  let group = "";
  return adminPages.map((p) => {
    const caption = p.group !== group ? `<div class="nav-caption">${p.group}</div>` : "";
    group = p.group;
    return `${caption}<button data-tab="${p.id}">${p.icon}<span>${p.label}</span></button>`;
  }).join("");
};
