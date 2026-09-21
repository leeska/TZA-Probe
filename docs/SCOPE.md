# 项目范围与需求对照

目标是简单、可控、无旧版兼容包袱的探针。保持一个仓库、Server / Agent 两个程序、一个 SQLite 数据库；前后台重建，组件通过明确接口协作。

下表为代码和界面入口核对，不代表真实环境验收完成。

| 原始需求 | 当前入口 | 实现位置 |
|---|---|---|
| 主机状态、资源、网络速率 | 公开卡片与节点详情 | agent/collector.go、web/app.js、web/detail.js |
| 内置目标容易查找 | 网络任务表单的搜索、地区/运营商/协议筛选与多选 | web/task-editor.js、internal/catalog |
| 单任务组合延迟与回程、IPv4 与 IPv6 | 表单可同时选择；Agent 按单项调度 | model.Task.Leaves、storage.Lease |
| 包含海外“其他”运营商 | 自定义目标运营商选项 | web/task-editor.js、model.Task.Validate |
| 卡片只显示指定地区，详情看其他地区；延迟与回程的地区可分别指定 | 节点编辑的卡片代表地区 / 卡片延迟地区、卡片、详情页 | web/admin.js、web/app.js、web/detail.js |
| IPv4 / IPv6 分开展示 | 公开页与详情页协议切换 | web/app.js、web/detail.js |
| 周期流量、额度、价格、到期时间 | 节点编辑、公开卡片 | model.Cycle、storage/billing.go、web/admin.js |
| 可选续费、流量、负载、离线告警 | 节点提醒规则 | internal/notify、web/admin.js |
| Telegram 机器人通知 | 告警与通知 → 配置出口 | notify/transport.go |
| 前后台重构、克制的玻璃效果 | 公开页、后台 | web/styles.css、web/glass.css |
| 各组件解耦 | 依赖接口和版本化 HTTP 协议 | docs/ARCHITECTURE.md |
| 不迁移旧数据 | 新建独立数据库，拒绝旧版数据库 | storage.Open |

## 简单使用所需的配套能力

已补充历史图、Agent 接入配置和安装脚本、当前周期用量校正、通知状态和失败原因，以及任务暂停/恢复、复制、批量绑定、立即安排执行和最近执行记录。

关键约定：
- 空节点绑定不执行，前端不会执行探测命令。
- TCP 连接失败率不等同于 ICMP 丢包率；负载告警当前是 CPU 或内存使用率。
- 未检测、过期或失败的回程不编造线路；标签只按实际跳数与 ASN 证据命名（163 / CN2GIA / CN2GT / 4837 / 9929 / 10099 / CMIN2 / CMI / CTG），不写“精品回程”这类营销词。
- 通知出口一次选择 Telegram 或 Webhook，不引入复杂多渠道路由。
- 固定保留主机历史 30 天、网络结果 7 天、账本 400 天、事件 90 天；备份采用停服复制。

## 不扩展的范围

当前不做维护模式、后台备份系统、远程终端、插件市场、多用户权限系统、复杂报表、旧协议适配或自动更新。新需求只有确实服务于探针本身时才纳入，避免把简单探针重新做成运维平台。

部署、更新和恢复以 DEPLOYMENT.md 为准。VERIFICATION.md 是历史记录，不能代替后续修改的实际验收。
