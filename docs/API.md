# HTTP API v1

全部 JSON。管理自动化用 `Authorization: Bearer <admin-token>`；Agent 用自己的节点令牌。浏览器通过 `POST /api/v1/login` 提交 `{ "token": "..." }` 获取 Cookie。生产环境必须由 TLS 代理保护。

| 方法与路径（均以 /api/v1 开头） | 用途 |
| --- | --- |
| GET /nodes | 公开节点；登录管理员可见全部 |
| GET /nodes/{id}/history?hours=24 | 1–720 小时，最多约 240 桶的主机曲线 |
| GET /nodes/{id}/observations | 每个检测项最新网络结果，带 stale 状态 |
| GET /latency-samples?limit=20 | 卡片延迟格用：每个「节点 / 运营商 / 协议 / 地区」最近 N 次逐次 TCP 结果（范围 1–60），只含调用者可见的节点 |
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

## 周期校正、告警与历史补充

- `GET /api/v1/nodes/{id}/network-history?hours=24`：按 TCP 目标和 IP 协议返回历史序列，范围 1–168 小时、每个目标约 240 个时间桶。沿用节点详情的公开/管理员权限；失败连接不伪造零延迟，连接失败率按发送/成功次数加权。编辑或删除任务会清除其已有任务结果。
- `POST /api/v1/admin/nodes/{id}/traffic`：管理员提交 `{"used":5000000000,"cycle":<cycle_start>}`，将当前周期已用总量校正为指定字节数。周期已变化返回 409。校正保留原始计数，后续增量继续累计，下周期失效；更改重置时间或计费方向会清除校正。
- 节点配置的重置时间与到期时间都是 RFC3339（精确到秒）：`billing_at` 是周期锚点，只取它的「日 + 时分秒」逐月顺延，短月取月末；`expires_at` 留空表示不设。旧的 `billing_day`（每月第几天）不再接受。
- 后台表单的重置/到期时间填 `YYYY-MM-DD HH:MM:SS`（UTC），提交前转成 RFC3339；格式不符或日期不存在（如 2 月 31 日）在本地就报错。数字输入不再画步进箭头，滚轮经过数字、日期时间与下拉框时先失焦，滚动页面不会误改已填好的值。
- 节点告警新增 `offline_seconds`（判定离线后额外等待 0–86400 秒）与 `repeat_seconds`（0 为仅触发/恢复各一次；非零范围 300–604800 秒）。旧配置缺省为 0，不改变其通知频率。
- 告警事件新增 `last_error`：最近一次通知投递错误；成功投递后清空。不保存 Telegram Token、请求 URL 或响应正文。

数据库版本升级为 3，启动时自动完成两段迁移：v1 数据库补通知错误列和事件索引，v2 数据库把节点的 `billing_day`（每月第几天）改写成 `billing_at`（带秒的锚点时刻，取锚点日确实存在的最近一个月，避免被短月钳掉后逐月跑偏）。旧版二进制不能打开新版数据库，回滚时需恢复升级前备份。

## 网络任务管理

- 管理员接口 `POST /api/v1/admin/tasks/{id}/control` 接收 `{"action":"pause|resume|run|bind","node_ids":[]}`。仅 bind 使用 node_ids，并替换该任务全部绑定；空列表不会执行任务。无效节点使本次更新整体失败。
- pause/resume/bind 保留历史结果，暂停不取消已领取的探测；run 要求启用且已绑定节点，由 Agent 拉取执行，遵守原有并发限制。请求使用一个时间标记，不积累无限任务队列。
- `GET /api/v1/admin/tasks/{id}/runs` 返回当前绑定节点每个已执行目标的最近领取时间、完成时间、结果和失败原因；全部尚未执行的节点显示未执行，未返回原始路径地址。
- 后台支持选择多个任务后统一绑定节点，并支持搜索和全选筛选结果。跨任务依次保存，失败时提示已成功数量。
- 复制任务打开预填创建表单，默认暂停，保存前可修改目标和绑定。编辑探测定义仍会清除该任务旧结果；专用暂停、恢复、绑定操作不会清除。
