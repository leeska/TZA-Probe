# HTTP API v1

全部 JSON。管理自动化用 `Authorization: Bearer <admin-token>`；Agent 用自己的节点令牌。浏览器通过 `POST /api/v1/login` 提交 `{ "token": "..." }` 获取 Cookie。生产环境必须由 TLS 代理保护。

| 方法与路径（均以 /api/v1 开头） | 用途 |
| --- | --- |
| GET /nodes | 公开节点；登录管理员可见全部 |
| GET /nodes/{id}/history?hours=24 | 1–720 小时，最多约 240 桶的主机曲线 |
| GET /nodes/{id}/observations | 每个检测项最新网络结果，带 stale 状态 |
| POST /login、POST /logout、GET /session | 管理会话 |
| GET、POST /admin/nodes | 列表、新建节点（返回一次性 token） |
| PUT、DELETE /admin/nodes/{id} | 编辑、删除节点 |
| POST /admin/nodes/{id}/token | 撤销旧令牌，返回新令牌 |
| GET、POST /admin/tasks | 列表、创建探测任务 |
| PUT、DELETE /admin/tasks/{id} | 编辑、删除任务 |
| GET /admin/catalog | 内置离线目标目录 |
| GET /admin/events | 最近 100 条告警与发送结果 |
| GET、PUT /admin/settings | 查询是否配置通知、设置 webhook |
| POST /admin/settings/test | 显式测试已配置 Webhook |
| POST /agent/report | `{protocol:1,metrics:{...}}` |
| GET /agent/jobs | 获取最多两个有限租约任务 |
| POST /agent/results | 上传与租约绑定的结果 |

准确字段由 `internal/model` 中的 JSON 标签定义。未知 JSON 字段、超大请求、额外 JSON 值会被拒绝。非公开节点对未登录请求返回 404。任务为空绑定不执行，不表示所有节点。

## 通知渠道

`GET /admin/settings` 返回当前渠道状态，不返回凭据。`PUT /admin/settings` 支持 Telegram、Webhook 或关闭通知；`POST /admin/settings/test` 发送测试通知。Telegram Token 只写入服务端，界面不回显，告警、恢复和重试共用同一事件队列。

HTTP 401：未授权；403：跨源写入；404：不存在或不可见；409：报告过频、租约失效、重复结果；400：无效输入；500：内部错误（详细内容仅记录服务日志）。成功变更通常返回 204；创建节点/轮换令牌返回 200 JSON。

管理员创建的探测目标允许内网地址，便于自托管网络监控；仅管理员可配置目标和通知出口。该权限不提供给公开页面或 Agent。


## 组合网络任务

管理接口支持 `checks: ["tcp", "route"]` 和 `targets: [{host, port, family, region, carrier}]`。
`interval` 是 TCP 秒数（30–86400），`route_interval` 是回程秒数（900–86400）。
目标必须明确指定 ipv4 或 ipv6；双栈域名填写为两个目标，IP 字面量必须匹配地址族。
运营商支持电信、联通、移动、其他。最多 200 个地址；重复检测项被拒绝。
`node_ids` 是明确节点列表，空列表不执行。

Server 将 checks × targets 展开为单项 Job.Task；Agent 与公开 observations 仍消费独立的
kind / host / port / family / region / carrier 快照，不依赖后台表单。
每项独立计时与保留最近结果；排队时优先最早到期项，每节点最多一项 TCP 和一项回程同时执行。
开发版本已有的单项任务仍可执行，编辑保存后采用组合结构。
