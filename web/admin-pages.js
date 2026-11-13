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

export const adminPages = [
  { id: "overview", label: "总览", page: overviewPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="6" height="8" rx="1.5"/><rect x="11" y="3" width="6" height="5" rx="1.5"/><rect x="11" y="10" width="6" height="7" rx="1.5"/><rect x="3" y="13" width="6" height="4" rx="1.5"/></svg>' },
  { id: "nodes", label: "节点管理", page: nodesPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="3" width="6" height="6" rx="1.5"/><rect x="11" y="3" width="6" height="6" rx="1.5"/><rect x="3" y="11" width="6" height="6" rx="1.5"/><rect x="11" y="11" width="6" height="6" rx="1.5"/></svg>' },
  { id: "tasks", label: "网络探测", page: tasksPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><circle cx="10" cy="10" r="3"/><circle cx="10" cy="10" r="0.8" fill="currentColor"/></svg>' },
  { id: "remote", label: "远程管理", page: remotePage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="2.5" y="3.5" width="15" height="13" rx="2"/><polyline points="6 8 8.5 10 6 12"/><line x1="10.5" y1="12" x2="14" y2="12"/></svg>' },
  { id: "alerts", label: "告警与通知", page: renderAlerts, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10" cy="10" r="7"/><polyline points="10 6 10 10 13 12"/></svg>' },
  { id: "audit", label: "审计日志", page: auditPage, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 2.5h7l3.5 3.5v11.5h-10.5z"/><polyline points="12 2.5 12 6 15.5 6"/><line x1="7.5" y1="10" x2="12.5" y2="10"/><line x1="7.5" y1="13" x2="12.5" y2="13"/></svg>' },
  { id: "themes", label: "主题", page: renderThemes, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 3.2c-3.9 0-7 2.5-7 5.6 0 1.9 1.6 3 3.4 3h1.1c.9 0 1.5.9 1 1.7-.6 1 .1 2.3 1.4 2.3 3.9 0 7-2.6 7-5.8S13.9 3.2 10 3.2Z"/><circle cx="7.4" cy="8.2" r="1"/><circle cx="11" cy="7.2" r="1"/></svg>' },
  { id: "plugins", label: "插件", page: renderPlugins, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 3.5h4v2.2a1.6 1.6 0 1 0 3.2 0V5.5H16.5V9h-1.3a1.6 1.6 0 1 0 0 3.2h1.3v4.3H12v-1.3a1.6 1.6 0 1 0-3.2 0v1.3H3.5V12h1.3a1.6 1.6 0 1 0 0-3.2H3.5V5.5H8z"/></svg>' },
  { id: "settings", label: "面板设置", page: renderSettings, icon: '<svg class="nav-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="4" y1="6" x2="16" y2="6"/><line x1="4" y1="14" x2="16" y2="14"/><circle cx="8" cy="6" r="2" fill="currentColor"/><circle cx="12" cy="14" r="2" fill="currentColor"/></svg>' },
];

export const adminPage = (id) => adminPages.find((p) => p.id === id) || adminPages[0];

export const navHTML = () =>
  adminPages.map((p) => `<button data-tab="${p.id}">${p.icon}<span>${p.label}</span></button>`).join("");
