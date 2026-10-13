# 组件边界

所有运行组件统一版本发布，依赖方向单向。解耦不要求运行消息队列或额外服务。

```text
web (HTTP JSON) ───> httpapi ──┬──> service ───> Store port <── storage (SQLite)
                               │       ▲
                               │       └── mcp（MCP 工具：同一组管理操作）
                               └──> Repository port（公开可见性、Agent 协议）
notify.Engine ───> Repository port（节点、告警状态、事件）
       ├────────> Channels port <── service（通知渠道的存取）
       └────────> Sender port <── Telegram / Webhook
Agent ───> Transport port <── HTTPTransport ───> httpapi
  ├─────> Collector port <── SystemCollector (gopsutil)
  └─────> Prober port <── probe.Runner (TCP / 内置 NTrace-core)

model: shared data contract, validation, billing calendar, route labels
cmd/server and cmd/agent: composition roots and lifecycle
```

- `model` 仅依赖标准库，不导入数据库、HTTP、UI。按领域分文件：`node.go`、`alert.go`、`billing.go`、`task.go`、`result.go`、`metrics.go`（Agent 上报与能力）、`event.go`、`unlock.go`、`route.go`、`settings.go`、`errors.go`。
- `storage` 实现持久化，没有服务端路由和探测逻辑。`store.go` 只管打开与连接池，`schema.go` / `migrate.go` 是表结构与版本迁移，其余按调用方分文件：`nodes.go`、`billing.go`、`agent.go`（上报、租约、结果）、`tasks.go`、`task_controls.go`、`observations.go`、`history.go`、`network_history.go`、`tcp_buckets.go`（网络历史的 6 分钟预聚合）、`samples.go`、`unlocks.go`、`events.go`、`settings.go`、`purge.go`（保留期与分批删除）、`backup.go`。
- `service` 是后台与 MCP 共用的管理操作：节点、任务、设置的校验，部分更新的合并（PATCH / `update_*`），时间窗夹取，以及错误的种类与中文说明（`model.Invalid` / `NotFound` / `Conflict`）都只在这里定一次。适配层只负责解码请求、把错误翻成各自的形式（HTTP 状态码 / JSON-RPC 错误或工具结果）。
- `httpapi` 只依赖持久化接口；从 `cmd/server` 注入 SQLite 实现。管理路由在 `httpapi/admin.go`，全部调用 `service`；公开读取与 Agent 协议在 `httpapi/api.go`。
- `mcp` 调用同一个 `service`，读工具与写工具都走面板那套校验、脱敏与合并语义；它不知道路由与鉴权，由 `httpapi` 使用独立 MCP API Key 鉴权，因此 AI 客户端与面板看到同一份数据、同一套规则。需要接入命令时按请求的 Host 与 `X-Forwarded-Proto` 拼出与面板「接入」弹窗相同的那条命令。
- `notify` 只管告警规则、事件投递与发送器（Telegram / Webhook），不知道配置存在哪里：渠道经 `Channels` 接口拿到，存取、补全与校验由 `service` 负责（`service/notification.go`）。
- `agent` 的采集、传输、探测接口可分别替换；CPU 采集不会被最长 60 秒的回程执行阻塞。
- `probe` 不知道数据库或 HTTP；任务执行只允许固定动作，拒绝超频任务和无效目标。
- `web` 只调用 `/api/v1`。`api.js` 处理通信，`app.js` 负责公开页，只在进入 `/admin` 时才动态加载后台模块。后台的外壳（登录、侧栏、切页）是 `admin.js`，各页分别是 `admin-nodes.js`、`admin-tasks.js`、`admin-alerts.js`、`admin-settings.js`，共用状态与表单工具在 `admin-state.js`；页面之间经 `admin-state.js` 的 `go()` / `render()` 跳转与重绘，不互相引用外壳。`detail.js` 负责共享详情，`charts.js` 与 `format.js` 负责展示。`task-editor.js` 负责任务表单，`task-controls.js` 负责任务操作，`agent-setup.js` 负责接入弹窗（一键安装命令与 agent.json）。`internal/agentdist` 通过 `/install.sh` 与 `/dl/tza-agent-linux-<arch>` 分发静态安装脚本和 Agent 二进制，二进制优先取数据目录 `agents/`，其次取构建时嵌入的副本。它们通过 HTTP / 回调协作。

Server 的发布产物包含网页，Agent 单独编译，只链接其依赖图，不包含数据库和管理界面。两者通过版本化 JSON 协议协作。SQLite WAL 支持读写，单连接与显式事务约束写入顺序；任务租约、流量差值、告警转换具备事务边界。

任务租约绑定节点、任务快照和 90 秒有效期。未知节点、重复结果、过期结果无法写入。同一种类在已有有效租约时不再下发（解锁检测的有效期与其 15 分钟上报窗口一致）；报了 `parallel_jobs` 能力的 Agent 按种类分别判断，一次最多领 8 个 TCP、1 个回程、1 个解锁任务并各自并行执行，长任务不阻塞延迟探测；没报这项能力的 Agent 按整批判断，每次最多两个任务。编辑任务定义使旧任务结果与租约失效。专用暂停、恢复、节点绑定操作保留历史；手动执行只调整调度时间，仍由 Agent 拉取，不引入远程命令通道。Agent 上传失败最多重试 3 次；下次探测周期重新下发，状态不能伪装为持续正常。

公开端无 IP 上报字段；路径在 Agent 脱敏，HTTP 再过滤。公开查询只允许公开节点，管理密钥与 Agent 密钥不能互换。浏览器会话为 12 小时 HttpOnly/SameSite Cookie，服务重启失效。配置节点密钥只返回一次，数据库保存 SHA-256 摘要。管理密钥来自环境或权限 0600 的本地文件，不在日志中出现。后台账号（用户名 + PBKDF2-SHA256 密码摘要）存于数据目录 0600 的 `admin-account`，每次登录重读，终端 `tza-server reset-password` 改完即生效；后台仅支持用户名密码登录，超限时直接拒绝、不再计算摘要。MCP 使用后台生成的独立 API Key，数据库只保存 SHA-256 摘要，生成时仅回显一次，重新生成与撤销即时生效。

设计刻意不抽象通用工作流、插件容器或万能 RPC 层。新增功能先归属现有边界。

任务（`model.Task`，type = probe / unlock 的联合）由 model 展开为单项探测（`model.Probe`），storage 按单项最近签发时间独立调度；`Probe` 也是下发给 Agent 的租约内容与观测里的任务快照。
调度键包含父任务、检测类型、IP 协议、地址、端口、地区和运营商。

网络历史 24 小时以上的三档（24 小时 / 3 天 / 7 天，分组步长都是 6 分钟的倍数）读 `tcp_buckets`：TCP 结果写入时由 jobs 上的触发器累加进「节点 × 探针 × 6 分钟」的格子，查询只在格子上再聚合一次，结果与逐行聚合一致（7 天从约 1.3 秒降到约 50 毫秒）。格子随保留期、改任务、删节点各自删除，不靠逐行回减。1 小时 / 6 小时仍逐行聚合。
结果按相同维度取最新记录，双栈及延迟/回程不互相覆盖。
后台 task-editor 模块独立处理目录搜索、筛选、多选和自定义目标；Agent 无需了解目录或表单。


计费周期与校正规则分别归属 model、storage/billing.go；主机与网络历史由存储层聚合，web/charts.js 只负责绘图。通知失败原因存入事件记录，不将凭据或原始响应暴露给前端。
部署保持 Server / Agent 两个进程及 SQLite；数据保留时间固定在 storage.Clean；备份可在后台在线下载（`VACUUM INTO` 一致快照），也可按部署文档停服复制。所有写入共用一把写锁：保留期清理、编辑 / 删除任务与删除节点的大批历史行都分批删除（`storage/purge.go`，每批持锁约 0.1 秒，删除队列记在 `purges` 表里、重启后继续），不在一个事务里把 Agent 的上报挡在锁外。`/api/` 的 JSON 响应按 `Accept-Encoding` 用 gzip 压缩。不加入维护模式、插件容器或额外基础服务。
