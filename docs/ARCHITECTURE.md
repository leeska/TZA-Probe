# 组件边界

所有运行组件统一版本发布，依赖方向单向。解耦不要求运行消息队列或额外服务。

```text
web (HTTP JSON) ───> httpapi ──┬──> service ───> Store port <── storage (SQLite)
                               │       ▲
                               │       └── mcp（MCP 工具：同一组管理操作）
                               ├──> Repository port（公开可见性、Agent 协议）
                               └──> market ──┬──> theme.Store ──┐
                                      │       └──> plugin.Store ─┴──> pkg/tzapkg（包格式、签名、插件模板）
                                      │        （mcp 的市场工具也走 market）
                                      └──> TZA-CC 实例 API（只出不进，HTTPS）
service ──> Plugins port <── plugin.Store（通知插件的设置校验、目标集并入目录、删除规则）
notify.Transport ──> Plugins port（通知插件渠道：渲染请求模板并发送）
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
- `notify` 只管告警规则、事件投递与发送器（Telegram / Webhook / 通知插件），不知道配置存在哪里：渠道经 `Channels` 接口拿到，存取、补全与校验由 `service` 负责（`service/notification.go`）。
- `agent` 的采集、传输、探测接口可分别替换；CPU 采集不会被最长 60 秒的回程执行阻塞。
- `probe` 不知道数据库或 HTTP；任务执行只允许固定动作，拒绝超频任务和无效目标。
- `pkg/tzapkg` 是包格式：清单解析、文件校验与 CC 签名（`sign.go`）。它是唯一可导出的包，TZA-CC 审核上传与 Probe 安装用同一份代码；只依赖标准库。`theme` 用它落地安装，`market` 用它验签。
- `market` 是 TZA-CC 的客户端：拉目录与详情、代理预览图、下载并验签（大小、SHA-256、Ed25519 签名、包内 id / 版本）后交给 `theme.Store.InstallMarket`，以及设备码绑定（凭据在数据目录 0600 的 `cc-instance`，只发给绑定时那个 CC）。它只在管理员操作时主动请求 CC，CC 从不回连，也收不到任何监控数据；不知道路由与鉴权，`httpapi`（`/api/v1/admin/market/*`）和 `mcp`（`search_market` / `install_market_package`）各自适配。浏览器不直连 CC，面板 CSP 保持 `'self'`。默认连官方 CC；`--cc-url off` 时客户端为 nil，其余功能不受影响。
- `plugin` 管已安装的声明式插件（`<data>/plugins/<id>/plugin.json`，市场来源记在 `plugins/.market/`，都不对外提供）：安装、列出、读取、删除，只认 `pkg/tzapkg` 校验通过的 `plugin.json`。插件是数据与模板，没有代码：通知插件由 `notify.Transport` 解释成一个 HTTP 请求（只能发往插件声明的域名，5 秒超时、不跟随重定向、回应最多读 64 KB），目标集只由 `service.Catalog` 并入任务目录、由 `KnownRegions` 认它们的地区。哪些事情插件不能碰（节点密钥、MCP Key、其他通知凭据、数据库、Agent）由这个结构保证：模板只能读事件、本插件的设置和派生值。「正被用作通知渠道的插件不能删」这条规则在 `service.RemovePlugin`。依赖方向：`service` / `notify` 只经接口用插件，`plugin` 只依赖 `pkg/tzapkg`。
- `theme` 管已安装的包：内置现代极简（默认）与液态玻璃（`Builtins`），复古印刷 / 二次元是仓库 `themes/` 下的官方主题包，`tools/themepack` 打成 zip 发到 TZA-CC；附加包（`"type": "addon"`）依附某一套主题，给它添装饰项。面板只提供通用机制：卡片装饰槽、按节点挑装饰（`decorations.js`，结果写成 `data-deco` 与 `--deco-*` 变量）、布局变体与基础层，具体画法全在各主题包的样式表里。本地上传与市场安装走同一套校验；市场来源（版本、SHA-256、签名密钥）记在 `themes/.market/<id>.json`，这个目录与包里的点文件都不经 `/themes/` 对外提供；本地再上传同一 id 即清掉来源。主题在 theme.json 里声明的设置菜单（`settings`，见 docs/THEMES.md「主题设置」）由 `pkg/tzapkg` 校验，管理员的取值存在 settings 表的 `skin_options`，随清单发给访客。
- `web` 只调用 `/api/v1`。`api.js` 处理通信，`app.js` 负责公开页，只在进入 `/admin` 时才动态加载后台模块。后台的外壳（登录、侧栏、切页）是 `admin.js`，各页分别是 `admin-nodes.js`、`admin-tasks.js`、`admin-alerts.js`（含按插件设置画出的通知渠道表单）、`admin-themes.js`（已安装的主题与挂在各自主题下的附加包）、`admin-plugins.js`（已安装的插件）、`admin-settings.js`（子页：外观与品牌 / 账号与安全 / 数据），市场块、更新提示与 TZA-CC 账号连接是它们共用的 `admin-market.js`，共用状态与表单工具在 `admin-state.js`；页面之间经 `admin-state.js` 的 `go()` / `render()` 跳转与重绘，不互相引用外壳。`detail.js` 负责共享详情，`charts.js` 与 `format.js` 负责展示。`task-editor.js` 负责任务表单，`task-controls.js` 负责任务操作，`agent-setup.js` 负责接入弹窗（一键安装命令与 agent.json）。`internal/agentdist` 通过 `/install.sh` 与 `/dl/tza-agent-linux-<arch>` 分发静态安装脚本和 Agent 二进制，二进制优先取数据目录 `agents/`，其次取构建时嵌入的副本；同处的 `.sig` 与 `upgrade-key.pub` 是一键升级用的运维者签名与公钥（`internal/agentsig`，私钥不在 Server 上）。它们通过 HTTP / 回调协作。

Server 的发布产物包含网页，Agent 单独编译，只链接其依赖图，不包含数据库和管理界面。两者通过版本化 JSON 协议协作。SQLite WAL 支持读写，单连接与显式事务约束写入顺序；任务租约、流量差值、告警转换具备事务边界。

任务租约绑定节点、任务快照和 90 秒有效期。未知节点、重复结果、过期结果无法写入。同一种类在已有有效租约时不再下发（解锁检测的有效期与其 15 分钟上报窗口一致）；报了 `parallel_jobs` 能力的 Agent 按种类分别判断，一次最多领 8 个 TCP、1 个回程、1 个解锁任务并各自并行执行，长任务不阻塞延迟探测；没报这项能力的 Agent 按整批判断，每次最多两个任务。编辑任务定义使旧任务结果与租约失效。专用暂停、恢复、节点绑定操作保留历史；手动执行只调整调度时间，仍由 Agent 拉取，不引入远程命令通道。Agent 上传失败最多重试 3 次；下次探测周期重新下发，状态不能伪装为持续正常。

公开端无 IP 上报字段；路径在 Agent 脱敏，HTTP 再过滤。公开查询只允许公开节点，管理密钥与 Agent 密钥不能互换。浏览器会话为 12 小时 HttpOnly/SameSite Cookie，服务重启失效。配置节点密钥只返回一次，数据库保存 SHA-256 摘要。管理密钥来自环境或权限 0600 的本地文件，不在日志中出现。后台账号（用户名 + PBKDF2-SHA256 密码摘要）存于数据目录 0600 的 `admin-account`，每次登录重读，终端 `tza-server reset-password` 改完即生效；后台仅支持用户名密码登录，超限时直接拒绝、不再计算摘要。登录失败按桶限流：没有设备凭据的请求共用每分钟 30 次；登录成功过的浏览器拿到 HMAC 签名的 `tza_device` Cookie（绑定用户名与密码摘要，180 天，改密码即失效），之后用自己每分钟 10 次的桶，匿名请求刷满共享桶也锁不住管理员。不按来源 IP 分桶，因为 Server 通常在反代或 CDN 之后。MCP 使用后台生成的独立 API Key，数据库只保存 SHA-256 摘要，生成时仅回显一次，重新生成与撤销即时生效。

设计刻意不抽象通用工作流、插件容器或万能 RPC 层；插件只有上面两类声明式格式，不执行第三方代码。新增功能先归属现有边界。

任务（`model.Task`，type = probe / unlock 的联合）由 model 展开为单项探测（`model.Probe`），storage 按单项最近签发时间独立调度；`Probe` 也是下发给 Agent 的租约内容与观测里的任务快照。
调度键包含父任务、检测类型、IP 协议、地址、端口、地区和运营商。

网络历史 24 小时以上的三档（24 小时 / 3 天 / 7 天，分组步长都是 6 分钟的倍数）读 `tcp_buckets`：TCP 结果写入时由 jobs 上的触发器累加进「节点 × 探针 × 6 分钟」的格子，查询只在格子上再聚合一次，结果与逐行聚合一致（7 天从约 1.3 秒降到约 50 毫秒）。格子随保留期、改任务、删节点各自删除，不靠逐行回减。1 小时 / 6 小时仍逐行聚合。
结果按相同维度取最新记录，双栈及延迟/回程不互相覆盖。
后台 task-editor 模块独立处理目录搜索、筛选、多选和自定义目标；Agent 无需了解目录或表单。


计费周期与校正规则分别归属 model、storage/billing.go；主机与网络历史由存储层聚合，web/charts.js 只负责绘图。通知失败原因存入事件记录，不将凭据或原始响应暴露给前端。
部署保持 Server / Agent 两个进程及 SQLite；数据保留时间固定在 storage.Clean；备份可在后台在线下载（`VACUUM INTO` 一致快照），也可按部署文档停服复制。所有写入共用一把写锁：保留期清理、编辑 / 删除任务与删除节点的大批历史行都分批删除（`storage/purge.go`，每批持锁约 0.1 秒，删除队列记在 `purges` 表里、重启后继续），不在一个事务里把 Agent 的上报挡在锁外。`/api/` 的 JSON 响应按 `Accept-Encoding` 用 gzip 压缩。不加入维护模式、可执行代码的插件容器或额外基础服务。

### 独立前台主题

`web/frontend.js` 在公开页按清单选择核心渲染器或 `theme-runtime.js`，后台始终加载核心。
`frontend.entry` 指向主题自己的 HTML；主题运行于无 `allow-same-origin` 的 iframe，HTML / SVG 的 HTTP 响应也携带 CSP sandbox，直接打开也不能继承站点权限。
`theme-sdk.js` 通过版本化 postMessage 协议读取公开 API、路由与外观偏好。宿主按具体 frame/source 校验来源，公开数据请求省略 Cookie / Authorization，拒绝后台、Agent 和 MCP API。
主题能独立使用任意 DOM、CSS、打包后的框架和代码拆分；核心只负责选择、安装、版本检查、运行边界及数据契约。

## 心跳与指标

Agent 的心跳在独立协程运行，采集阻塞、采集失败或指标上报失败不会阻塞连接诊断。成功指标在最近 10 秒内已证明在线时不再额外发送健康心跳。采集期限为 10 秒，默认心跳检查间隔 5 秒。旧 Server 对心跳返回 404/405 后，该 Agent 进程关闭心跳请求，继续原有 report/jobs 协议。

SQLite schema 16 增加 `node_health` 存储连接与诊断；`nodes.last_seen` 内部仍只代表指标时间，流量差值摊分、采样与历史基线均不受心跳影响。对外 `last_seen` 合并最近联系，另给 `metrics_at`。采集失败的当前指标不对外伪装成新值；已保存的历史与累计用量保留。CPU/内存提醒在指标不可用时停止评估，解除说明明确不能确认使用率恢复，离线提醒继续依照连接判定。
