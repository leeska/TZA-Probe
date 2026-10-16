# 项目范围

目标是简单、可控的探针：一个仓库、Server / Agent 两个程序、一个 SQLite 数据库，组件通过明确接口协作。

## 功能与代码位置

| 功能 | 界面入口 | 实现位置 |
|---|---|---|
| 主机状态、资源、网络速率 | 公开卡片与节点详情 | agent/collector.go、web/app.js、web/detail.js |
| 内置目标容易查找 | 网络任务表单的搜索、地区/运营商/协议筛选与多选 | web/task-editor.js、internal/catalog |
| 单任务组合延迟与回程、IPv4 与 IPv6 | 表单可同时选择；Agent 按单项调度 | model.Task.Leaves、storage.Lease |
| 海外目标（“其他”运营商） | 自定义目标运营商选项 | web/task-editor.js、model.Task.Validate |
| 卡片只显示指定地区，详情看全部；延迟与回程的地区可分别指定 | 节点编辑的卡片回程地区（display.route_region）/ 卡片延迟地区 | web/admin-nodes.js、web/app.js、web/detail.js |
| IPv4 / IPv6 分开展示 | 公开页与详情页协议切换 | web/app.js、web/detail.js |
| 周期流量、额度、价格、到期时间 | 节点编辑、公开卡片 | model.Cycle、storage/billing.go、web/admin-nodes.js |
| 续费、流量、降速、负载、离线提醒 | 告警与通知 → 提醒规则 | internal/notify、web/admin-alerts.js |
| Telegram / Webhook / 通知插件 | 告警与通知 → 配置出口 | notify/transport.go、notify/plugin.go |
| 公开页与后台，克制的玻璃效果与多套外观 | 公开页、后台 | web/styles.css、web/glass.css |
| 数据库在线备份下载 | 面板设置 → 数据 → 数据备份 | storage/backup.go |
| AI 客户端读写探针数据（覆盖管理面板的操作） | `POST /mcp` 的 MCP 工具 | internal/mcp、internal/httpapi |
| 主题包 / 附加包：本地上传与 TZA-CC 市场安装（验签、来源与更新提示）；内置现代极简（默认）与液态玻璃，官方的复古印刷 / 二次元源码在 themes/；主题设置菜单（theme.json 的 settings，二次元的附加包开关在其中） | 主题（已安装 + 市场） | pkg/tzapkg、internal/theme、internal/market、themes/、tools/themepack、web/admin-themes.js、web/admin-market.js |
| 声明式插件：通知渠道、探测目标集（市场安装或上传，装前确认权限） | 插件（已安装 + 市场）；配置出口；任务目录 | pkg/tzapkg（plugin.go、tpl.go）、internal/plugin、notify/plugin.go、service/plugins.go |
| 组件解耦 | 依赖接口和版本化 HTTP 协议 | docs/ARCHITECTURE.md |

配套能力：历史图、Agent 接入配置和一键安装脚本、当前周期用量校正、通知状态和失败原因，以及任务暂停/恢复、复制、批量绑定、立即安排执行和最近执行记录。

## 关键约定

- 空节点绑定不执行，前端不会执行探测命令。
- TCP 连接失败率不等同于 ICMP 丢包率；负载提醒是 CPU 或内存使用率。
- 回程追踪核心内置在 Agent（NTrace-core，GPL-3.0，见 NOTICE），节点不需要安装外部二进制；跳的 ASN/地区默认取 `IPAPI.com`，可用 `--geo-provider` 显式指定或 `disable-geoip` 关闭。
- 未检测、过期或失败的回程不编造线路；标签按实际 ASN 与跳地址前缀命名（163 / CN2GIA / CN2GT / 4837 / 9929 / 10099 / CMIN2 / CMI / CTG），ASN 优先、前缀兜底，表外前缀保持未知，不写“精品回程”这类营销词。同一运营商的多条线路收敛成一个标签（电信按 CTG > CN2 > 163，移动按 CMIN2 > CMI）；联通轨迹里出现过 9929 时不再另记 4837（那是这条国际线的落地侧），没有 9929 时 4837 仍是联通线路名。ASN 数据源没解析出来的跳，Agent 会按注册库核对过的运营商自营网段（真实地址匹配，再脱敏）兜底，避免数据源失灵时线路名退回未知。
- 通知出口一次选择一个渠道（Telegram、Webhook 或一个通知插件），不引入复杂多渠道路由。
- 固定保留主机历史 30 天、网络结果 7 天、账本 400 天、事件 90 天。

## 不做的事

不做维护模式、远程终端、多用户权限系统、复杂报表或自动更新。新需求只有确实服务于探针本身时才纳入，避免把简单探针做成运维平台。

市场的边界：TZA-CC 市场可选，不连也不缺功能；包验签后安装，有新版只提示、不自动更新。插件只有声明式的两类——通知渠道（一个 HTTP 请求模板，只能发往声明的域名）和探测目标集（只往任务目录里加候选目标）——格式本身限定了它们能做的事，见 [PLUGINS.md](PLUGINS.md)。不做可执行代码的插件、前端注入脚本、Agent 端插件，也不提供远程命令通道；边界见 [ARCHITECTURE.md](ARCHITECTURE.md)。

部署、升级和恢复以 [DEPLOYMENT.md](DEPLOYMENT.md) 为准；[VERIFICATION.md](VERIFICATION.md) 说明验证方法与只能在真实环境验证的部分。
