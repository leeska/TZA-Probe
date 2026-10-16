# HTTP API v1

全部 JSON。管理自动化用 `Authorization: Bearer <admin-token>`；Agent 用自己的节点令牌。浏览器通过 `POST /api/v1/login` 提交后台账号 `{ "username": "...", "password": "..." }` 获取 Cookie，管理密钥登录已移除。生产环境必须由 TLS 代理保护。

| 方法与路径（均以 /api/v1 开头） | 用途 |
| --- | --- |
| GET /nodes | 公开节点；登录管理员可见全部 |
| GET /nodes/live | 与 /nodes 同一批节点，只带每秒会变的部分：`id`、`online`、`last_seen`、`metrics`、`traffic_used`、`avg_bps`、`covered_hours`、`window_hours`、`throttled`。公开页每秒取它，配置、探测计划与解锁结论随 10 秒一轮的 /nodes 更新 |
| GET /nodes/{id}/history?hours=24 | 1–720 小时，最多约 240 桶的主机曲线 ；`slim=1` 只带 cpu / memory_used / memory_total / rate_in / rate_out |
| GET /observations | 所有可见节点的最新观测，按节点 id 分组，不带跳表（首页卡片用） |
| GET /nodes/{id}/observations | 每个检测项最新网络结果，带 stale 状态 |
| GET /latency-samples?limit=20 | 卡片延迟格用：每个「节点 / 运营商 / 协议 / 地区」最近 N 次逐次 TCP 结果（范围 1–60），只含调用者可见的节点 |
| POST /login、POST /logout、GET /session | 管理会话；`GET /session` 另回服务端版本 `version`（内置 Agent 安装包同版本，后台据此标出该升级的 Agent） |
| GET、POST /admin/nodes | 列表、新建节点（返回一次性 token） |
| PUT、PATCH、DELETE /admin/nodes/{id} | 编辑（PUT 整份替换；PATCH 只改提交的字段、嵌套对象按字段合并，返回合并后的节点）、删除节点 |
| GET、POST /admin/alert-rules | 提醒规则列表、新建一条（返回 `{"id":…}`）；同类型同范围覆盖同一台节点时 400 并说明冲突在哪 |
| PUT、PATCH、DELETE /admin/alert-rules/{id} | 整份替换、只改提交的字段（返回改后的规则）、删除一条提醒规则；PATCH 与 MCP 的 `update_alert_rule` 同一套合并规则 |
| POST /admin/nodes/{id}/token | 撤销旧令牌，返回新令牌 |
| GET、POST /admin/tasks | 列表、创建探测任务（返回 `{"id":…}`） |
| PUT、PATCH、DELETE /admin/tasks/{id} | 编辑（PUT 整份替换；PATCH 同上合并语义）、删除任务 |
| PUT /admin/tasks/{id}/enabled | `{"enabled":true|false}`：恢复 / 暂停 |
| POST /admin/tasks/{id}/run | 立即安排执行；任务暂停或未绑定节点时 409 |
| PUT /admin/tasks/bindings | 两种写法，一个事务：`{"task_ids":[…],"node_ids":[…]}` 把所选任务换成同一份节点列表；`{"task_ids":[…],"add":[…],"remove":[…]}` 只增删这几个节点，各任务其余绑定不变（后台批量绑定用这种）。两种混用、或 add 与 remove 都为空时 400 |
| GET /admin/catalog | 任务目标目录：内置离线目录 `{source, snapshot_date, verification, targets:[{id, family, region, carrier, host, ip, port}]}` 加上已安装的目标集插件——这些目标另带 `plugin` / `plugin_name`，`id` 写成 `plugin:<插件 id>:<目标 id>`，`plugins:[{id, name, count}]` 列出并入了哪些插件。删除插件只影响这里，已建好的任务照常运行 |
| GET /admin/events?before=&limit=&node_id=&kind=&active= | 告警与发送结果，按 id 倒序分页：`limit` 默认 100、上限 500，`before` 传上一页最后一条的 id 续读，`node_id` / `kind` 筛选；`active=1` 只列正在告警的项目，每项取最近一次事件（MCP `recent_events` 的 `active: true` 同义） |
| GET、PUT、PATCH /admin/notification | 通知出口：查询渠道状态（不回凭据）、设置 Telegram / Webhook / 关闭；PUT 整份提交，PATCH 只改传了的项 |
| POST /admin/notification/test | 向已配置的渠道发送测试通知 |
| GET /admin/backup | 下载整个数据库的一致快照（SQLite 文件，`VACUUM INTO` 生成，不用停服）；文件名 `tza-backup-YYYYMMDD-HHMMSS.db` |
| GET、PUT /admin/account | 后台账号：GET 回 `{"username":…,"configured":true}`；PUT 提交 `{"current_password":…,"username":…,"password":…}` 修改用户名与密码（用户名 1–32 个非空白字符，密码 8–128 个字符；当前密码错误 400），成功后除本次以外的浏览器会话全部失效。忘记密码在服务器执行 `tza-server reset-password` |
| GET、POST、DELETE /admin/mcp-key | MCP API Key：GET 只回 `configured` 与已启用时的 `created_at`；POST 生成或重新生成，回一次性 `api_key`；DELETE 撤销。数据库只保存摘要，旧 Key 在重新生成或撤销后立即失效 |
| GET、PUT /admin/panel | 面板显示项（PUT 只改传了的项）：站点名称 `site_name`（标签页标题与品牌区的名字，最多 32 字，空串恢复默认的 TZA Probe）、只读的 logo 地址 `site_logo`（未上传为空）、默认明暗 `default_theme`、皮肤 `default_skin`、延迟色带时间窗（`latency_window_minutes`，1–10080 分钟：色带固定画最近 10 次探测，这一项是它们的年龄上限，早于它的不画；界面给 1 小时 / 6 小时 / 24 小时 / 3 天 / 7 天 档位，7 天即探测结果保留上限） |
| PUT、DELETE /admin/panel/logo | 站点 logo：PUT 的请求体就是图片本身（不走 JSON），类型按内容判断，只收 PNG / JPEG / GIF / WebP / ICO / SVG，最大 512 KB，回 `{"site_logo":"/site-logo?v=…"}`；SVG 含脚本、事件属性、外链引用或 DOCTYPE 时 400，超大 413。DELETE 移除，回到内置字标。文件存在数据目录的 `branding/`，不在数据库备份里 |
| GET、POST /admin/themes、DELETE /admin/themes/{id} | 主题包清单（内置 + 已安装，市场装的带 `"source":"market"`）、上传 zip、删除，见 [THEMES.md](THEMES.md#接口) |
| PUT /admin/themes/{id}/settings | 改主题设置（主题在 theme.json 里声明的菜单，附加包开关也在其中），请求体只写要改的项，回 `{options}`，见 [THEMES.md](THEMES.md#主题设置) |
| GET /admin/plugins | 已安装的插件：`[{id, kind, name, hint, version, author, source, permissions, settings, targets}]`，`source` 为 `market` 表示从市场装的；通知插件带设置表单 `settings`（key、label、type、required、secret、help、placeholder、default、options），目标集带目标个数 `targets` |
| POST /admin/plugins/inspect | 请求体是插件 zip（`Content-Type: application/zip`）：只校验、不安装，回同样的结构（面板据此列出权限请管理员确认）；不合规 400 |
| POST /admin/plugins | 安装上传的插件 zip，回装好的插件；同 id 替换（之前是市场装的就改为本地）。格式见 [PLUGINS.md](PLUGINS.md) |
| DELETE /admin/plugins/{id} | 删除插件；正被用作通知渠道时 409（先在配置出口换渠道），没有这个插件 404 |
| /admin/market/* | TZA-CC 主题市场：目录、详情、安装 / 更新、检查更新、实例绑定、预览图代理，见下文「主题市场」 |
| POST /mcp | Model Context Protocol 工具（读取与写入，不在 /api/v1 前缀下，见下节） |
| POST /admin/nodes/batch | 批量修改：`{"ids":[…],"patch":{…}}`，同一份部分字段（结构同 PATCH /admin/nodes/{id}，嵌套对象按字段合并）合并到每台节点，一个事务，全成或全不改；常用 `group`、`region`、`display.route_region`、`public`。不能改 `name` / `order`；回 `{"updated":N}` |
| POST /admin/groups/rename | `{"from":…,"to":…}`：整组改名，作用于 `from` 的提醒规则一起改；`to` 是已有分组即合并（合并后同类规则冲突则 400、不改），为空即解散回未分组。回 `{"moved":N}` |
| GET、POST /admin/agents/upgrade | 一键升级 Agent。POST `{"ids":[…]}` 把节点排进升级队列，回每台的结果 `results`：`queued`（下一次上报时下发）、`current`（已是面板版本）、`manual`（Agent 早于一键升级，需在机器上不带 `--token` 执行一次安装命令）、`unavailable`（服务端没有该架构的安装包）、`never`（从未接入）。GET 回 `{version, archs, items:{<id>:{state, error, requested_at, sent_at}}}`，`state` 另有 `sent`（已下发，等它以新版本上报）与 `failed`（下发 5 分钟后版本仍未变）。队列只在内存里，Server 重启后重新点即可 |
| POST /agent/report | `{protocol:1,metrics:{...}}`；平常回 204。排了升级、且 Agent 报了 `self_update` 能力时回 200 `{"upgrade":{"version","path","sha256"}}`：Agent 从同一台 Server 的 `path` 下载、校验摘要、替换自身后以状态 3 退出，由 systemd / OpenRC 重启 |
| GET /agent/jobs | 获取有限租约任务：按种类分别领取，最多 8 个 TCP + 1 个回程 + 1 个解锁（Agent 须报 `parallel_jobs` 能力，否则整批最多两个） |
| POST /agent/results | 上传与租约绑定的结果 |

不在 /api/v1 下的两个品牌相关地址：`GET /site-logo` 公开提供已上传的 logo（没有时 404；带 `nosniff` 和自己的 `default-src 'none'; sandbox` CSP，直接打开 SVG 也跑不了脚本）；`GET /theme-default.js` 是首屏脚本，除默认外观外另写 `window.__tzaBrand={"name":…,"logo":…}`，`theme-boot.js` 据此在首屏前换掉标签页标题与 favicon。

准确字段由 `internal/model` 中的 JSON 标签定义。未登录请求拿到的节点不含提醒规则（`alerts` 为空）。解锁结论只随 `/nodes` 的 `unlock` 字段下发。

## Model Context Protocol（`POST /mcp`）

首次接入请先阅读 [MCP 接入指南](MCP.md)，其中包含 Key 生成、客户端配置、工具清单及可运行的 curl 示例。

请求头携带 `Authorization: Bearer <MCP API Key>`，Key 在“面板设置”生成，初始未配置时端点拒绝访问。管理密钥、面板会话 Cookie 与节点密钥不通行，MCP Key 也不能登录后台或访问管理 HTTP API，跨源写入按原有规则拒绝。写工具与面板共用同一套持久化与校验，因此 AI 客户端改出来的状态和面板改出来的完全一致。只接受 POST 与 `application/json`，GET / DELETE 返回 405；响应默认为 `application/json`，客户端只接受 `text/event-stream` 时用一条 SSE 消息回复同一条 JSON-RPC 消息。没有会话状态，每个请求独立处理。

同一个地址支持两代协议：

- `2026-07-28`（无握手）：每个请求在 `_meta` 里声明版本，并在头部镜像 `MCP-Protocol-Version`、`Mcp-Method`（`tools/call` 还有 `Mcp-Name`，非 ASCII 用 `=?base64?…?=`），三者必须与请求体一致，缺失或不符返回 400 与 `-32020 HeaderMismatch`；版本不认识返回 400 与 `-32022 UnsupportedProtocolVersionError`（`data.supported` 列出支持版本）；方法不认识返回 404 与 `-32601`。结果带 `resultType: "complete"` 与 `_meta["io.modelcontextprotocol/serverInfo"]`，`server/discover` 与 `tools/list` 带 `ttlMs` / `cacheScope`。
- `2025-11-25`、`2025-06-18`（`initialize` 握手）：按客户端声明的版本回答，之后用 `MCP-Protocol-Version` 头声明，结果不带 `resultType` / 缓存字段。完全没有版本头的请求按 `2025-11-25` 处理（`2025-03-26` 及更早的客户端不发这个头）。

工具分读写两部分，`tools/list` 的 `annotations` 用 `readOnlyHint` / `destructiveHint` 标注。读取：`list_nodes`、`list_tasks`、`list_alert_rules`、`node_history`、`node_network_history`、`node_observations`、`recent_events`（可选 `before` / `limit` / `node_id` / `kind`，与 HTTP 同一套分页）、`latency_samples`、`list_catalog`（可选 `region` / `carrier` / `family` 过滤，含已装目标集插件的目标）、`task_runs`、`list_plugins`、`get_settings`。写入：`create_node`、`update_node`、`delete_node`、`rotate_node_token`、`correct_node_traffic`、`create_task`、`update_task`、`delete_task`、`control_task`（`action` 取 `pause` / `resume` / `run` / `bind`，`bind` 带 `node_ids`；内部分别走启停、立即执行、绑定三个操作）、`create_alert_rule` / `update_alert_rule`（`rule_id` + 要改的字段，合并语义）/ `delete_alert_rule`、`update_settings`、`test_notification`；市场：`search_market`（只读）与 `install_market_package`（见 [MCP.md](MCP.md)，市场关闭时不可用）。参数规则与 HTTP 接口一致：`hours` 省略或为 0 用默认 24（主机历史上限 720、网络历史上限 168），`limit` 默认 20、上限 60，超出按上限截断并在结果里回显实际窗口；未知参数、缺少 `node_id` / `task_id`（`rule_id` 缺失按工具错误返回）、未知工具、非法枚举，以及存储校验拒绝的输入（如绑定不存在的节点）都返回 `-32602`；节点或任务不存在、存储读写失败按工具结果返回 `isError: true` 与说明文字。`update_node` / `update_task` / `update_alert_rule` 是合并语义：只提交要改的字段，没提到的字段保持原值，嵌套对象（`billing` 等）按字段合并，列表整份替换；`update_alert_rule` 改了 `scope` 时，新范围用不上的列表没重新提交就清掉（`exclude_node_ids` 在 `all` 与 `group` 之间切换时保留，被排除的节点不会因为换了范围就开始告警）。
`create_node` 与 `rotate_node_token` 返回 `id`、`token` 和 `install_command`——与面板「接入」弹窗同一条命令，`--endpoint` 由请求的 Host 与 `X-Forwarded-Proto` 拼出，端点不是 HTTPS 时附 `warning` 提示。`update_settings` / `get_settings` 只回渠道状态与掩码字段，已存的 Bot Token / Webhook 从不回显（两者另外回面板显示项：延迟色带时间窗 `latency_window_minutes`、访客默认明暗 `default_theme` 与皮肤 `default_skin`、站点名称 `site_name` 与 logo 地址 `site_logo`；logo 不能经 MCP 上传）。`update_settings` 只改传了的项：不传 `channel` 沿用当前渠道，Telegram 的 `bot_token` / `chat_id` 留空沿用已存的，只传显示项时不动通知配置；关闭通知须显式传 `channel: "none"`，一项都不传返回 `-32602`；`test_notification` 复用面板的发送路径，未配置渠道或发送失败按工具错误返回原因。

## 主题市场（TZA-CC）

Server 默认连官方 TZA-CC（`https://tza.cc`），`--cc-url` / `TZA_CC_URL` 可指向别的 CC，设为 `off` 关闭市场（见 [THEMES.md](THEMES.md#市场tza-cc)）。浏览器只请求下面这些本站路由，目录、预览图、安装包都由 Server 代为获取。市场关闭时 `GET /admin/market/status` 回 `{"configured":false,…}`，其余路由 503 `{"error":"TZA-CC 市场已关闭：…"}`。全部需要管理员登录，写操作同样拒绝跨源请求。

| 方法与路径（/api/v1/admin/market…） | 请求 | 响应 |
| --- | --- | --- |
| GET /status | — | `{"configured","url","probe","trusted_keys","bound","instance_id","account","bound_at","other_url","pending":{"user_code","verify_url","expires_at","interval"}}`；`other_url` 表示本机存的是另一个 CC 的绑定（不会发给当前 CC）。从不回 token |
| GET /catalog?type=skin\|addon\|plugin&for=&q=&page= | `q` ≤ 64 字，`page` 1–1000 | `{"items":[{"id","type","name","summary","author":{"name","handle"},"latest","price","downloads","updated_at","swatch","cover","url","kind","permissions","installed":{"version","source":"market\|local"}}],"page","more"}`；`kind`（notify / targets）与 `permissions`（最新兼容版本能做的事）只有插件有；`price` 以分计，0 为免费；`owned` 为 true 表示免费或绑定的 TZA-CC 账号拥有这个付费包，付费包未拥有时安装会被 CC 拒绝（平台关闭商业功能时目录里不会出现付费包）；`installed` 是本机同 id 的包，没装就没有；本机还不支持的类型或插件 kind、id 不合规的条目被滤掉，不在 CC 同源上的 `cover` / `url` 被清空 |
| GET /packages/{id} | — | 上面的字段加 `description`、`screenshots`（CC 同源 URL）、`versions:[{"version","changelog","probe","size","published_at","compatible"}]`（新的在前；`compatible` 要 CC 的判断与本机按 `probe` 范围的判断都成立） |
| POST /install | `{"id":"matcha","version":"1.2.0"}`，`version` 省略即当前 Probe 能用的最新版 | `{"id","type","kind","name","version","source":"market","permissions"}`：按包里是 theme.json 还是 plugin.json 装进主题或插件目录（插件另回 `kind` 与 `permissions`）。同 id 的已装包（市场或本地）被替换 |
| GET /updates | — | `{"updates":[{"id","version","latest"}]}`：市场装的包（主题、附加包与插件）里 CC 认为有兼容新版的；没有市场装的包时不请求 CC |
| POST /connect | 可选 `{"name":"实例名"}`（≤ 40 字，默认 TZA Probe） | `{"user_code","verify_url","expires_at","interval"}`：开始设备码绑定，设备码本身留在 Server |
| POST /connect/poll | — | `{"status":"pending\|bound\|expired\|none","account","interval"}`；比 `interval` 更快的轮询直接回 pending、不请求 CC |
| DELETE /connect | — | 取消进行中的绑定并解绑：通知 CC 撤销 token、删除本机 `cc-instance`。204；CC 没确认时仍删本机凭据，回 200 `{"warning":"…"}` |
| GET /media?url=<CC 上的图片地址> | 只代理 CC 同源（协议 + 主机 + 端口）的地址 | 图片本身：只放行 png / jpeg / webp / gif / avif（不收 SVG），≤ 5 MB，`Cache-Control: private, max-age=3600`；别的地址 400 |

错误：参数不合规 400；市场里没有这个包 / 版本 404；CC 回 409（如版本需要更新的 Probe、账号实例数已满）原样 409；CC 不可达、CC 其他错误、安装包未通过校验（大小、SHA-256、签名、包内 id / 版本不符）502；市场已关闭或没有信任公钥 503。错误正文都是 `{"error":"<中文说明>"}`，CC 返回的说明以「TZA-CC：」开头。安装最长等 3 分钟（下载 ≤ 20 MB）。

## 通知渠道

Telegram 消息格式与各类事件字段见 [通知模板说明](NOTIFICATIONS.md)。事件正文保存触发或解除当时的数据快照，重试投递不会用最新指标改写；Webhook 的 JSON 字段结构保持不变，但 `event.message` 是可读文本、格式不承诺稳定，接收端应按 `kind`、`active` 等字段判断。

`GET /admin/notification` 返回当前渠道状态，不返回凭据。`PUT /admin/notification` 支持 Telegram、Webhook、通知插件或关闭通知（整份提交，空值即清空；Bot Token 留空保留已存的）；通知插件写 `{"channel":"plugin","plugin":"ntfy","plugin_settings":{"server":"…","topic":"…","token":"…"}}`，按插件声明的设置校验（必填、格式、`url` 类设置的域名），密钥类设置留空沿用已存的值（同一个插件时）；GET 对插件渠道另回 `plugin`、`plugin_name`、非密钥设置 `plugin_settings` 和 `plugin_secrets`（`{"token":true}` 只说明已保存，从不回值），插件被删或不再有效时回 `plugin_error`。`PATCH /admin/notification` 只改传了的项，与 MCP `update_settings` 的通知部分同一套规则（不传 `channel` 沿用当前渠道，留空的凭据沿用已存的，关闭须显式传 `channel: "none"`，一项都不传返回 400），返回改后的渠道状态；`POST /admin/notification/test` 发送测试通知。Telegram Token 只写入服务端，界面不回显，告警、恢复和重试共用同一事件队列。面板显示项是另一个资源：`GET /admin/panel` 返回生效值（`latency_window_minutes` 未设置时 60 分钟），`PUT /admin/panel` 只改传了的项。两者分开，避免「通知那套空值即清空」的语义顺手把 Webhook 或 Token 抹掉。

HTTP 401：未授权；403：跨源写入；404：不存在或不可见；409：冲突，`error` 写明是哪一种（如报告过频、租约失效、重复结果、计费周期已变化、任务暂停或未绑定节点时立即执行）；400：无效输入；500：内部错误（详细内容仅记录服务日志）。错误正文统一为 `{"error":"<中文说明>"}`。成功变更通常返回 204；创建节点/轮换令牌返回 200 JSON。

管理员创建的探测目标允许内网地址，便于自托管网络监控；仅管理员可配置目标和通知出口。该权限不提供给公开页面或 Agent。

## 节点结构

节点配置按关注点分组，运行状态与生效的提醒规则平铺在节点视图上：

```json
{"id":"…","name":"东京","group":"JP","region":"日本","vendor":"netcup","order":0,"public":true,
 "display":{"route_region":"上海","latency_region":"","latency_carriers":["电信"],"route_carriers":["电信","联通"]},
 "billing":{"reset_at":"2026-09-01T00:00:00Z","traffic_limit":2000000000000,"traffic_mode":"max",
            "free":false,"price":9.9,"currency":"USD","price_period":"月","expires_at":""},
 "line":{"mbps":1000,"throttle_mbps":100,"throttle_hours":24,"throttle_on":"bandwidth"},
 "alerts":[{"id":"…","kind":"offline","hold_seconds":60,"scope":"all"},{"id":"…","kind":"cpu","threshold":90,"hold_seconds":300,"repeat_seconds":1800,"scope":"group"}]}
```

`alerts` 只读：是服务端按规则范围算出的这台节点实际生效的规则（每种至多一条），新建、编辑节点时不提交它。

- `display`：公开卡片显示哪些探测结果。`route_region` 是回程显示的地区，`latency_region` 留空沿用它；两者须与所绑任务的目标地区逐字一致。保存时去掉首尾空白，新填或改动的地区必须出现在内置目录或某个任务的目标地区里，否则 400 并指出是哪个地区（节点原有的值不受影响，照常可编辑）。`latency_carriers` / `route_carriers` 只在这台机器真有的探测项里再筛一道，留空显示全部；回程只有电信、联通、移动三网。
- `region`（节点所在地区）只做展示，不参与匹配，不做这项检查。
- `decoration`：这台节点卡片上的主题装饰，可省略（按节点名、地区、默认项自动选）。写成「包 id/装饰项 id」，取当前外观或依附它的附加包在 `theme.json` 里声明的 `decorations`（见 [THEMES.md](THEMES.md#按节点挑装饰)）；当前外观不提供这一项时按自动处理。
- `vendor`：商家（如 netcup / Hytron），可省略。公开卡片上显示为名牌，公开接口照常返回，前台搜索也按它匹配；最多 60 字节，首尾不能有空格。
- `billing`：`reset_at` 是周期锚点；`free` 见下文。
- `line`：线路带宽 `mbps` 与商家降速规则。
- 节点视图另带运行状态：`online`、`last_seen`、`metrics`、`cycle_start` / `cycle_end`、`traffic_used`、`avg_bps`、`covered_hours`、`window_hours`、`throttled`、`probe_targets`、`unlock`。
- 创建时间与用量校正是服务端自记的状态，存在单独的列里，不在节点配置与任何响应中。

## 提醒规则

规则单独存放（`/admin/alert-rules`），一条规则一种类型，按范围作用于节点：

```json
{"id":"…","kind":"cpu","threshold":90,"hold_seconds":300,"repeat_seconds":1800,
 "scope":"group","groups":["HK",""],"exclude_node_ids":["…"]}
```

- 提醒规则的参数：`offline` 用 `hold_seconds`（判定离线后再等 0–86400 秒；从没上报过的新节点自创建起先给 15 分钟装 Agent，过了才算离线）；`renewal` 用 `threshold`（到期前 0–365 天）；`traffic` 用 `threshold`（额度百分比 1–100）；`throttle` 无参数（按线路降速规则，节点须填 `line.throttle_mbps`）；`cpu` / `memory` 用 `threshold`（1–100%）与 `hold_seconds`（持续 30–86400 秒）。`repeat_seconds` 每条规则各自设置：0 为触发 / 恢复各一次，非零 300–604800 秒。不属于该种规则的参数会被拒绝。
- 规则范围 `scope`：`all` 全部节点（含以后新加的）；`group` 作用于 `groups` 列出的分组（空串是未分组的节点）；`nodes` 作用于 `node_ids` 列出的节点。`all` / `group` 可用 `exclude_node_ids` 排除个别节点。同一台节点同一种类型只生效一条，优先级 `nodes` > `group` > `all`；所以同类型、同范围的两条规则不能覆盖同一台节点（两条 `all`、共用一个分组、共用一台节点），保存时 400。节点不存在时 400。分组不做存在性检查（分组可以以后再建），所列分组下没有节点时照样保存，但 MCP 的 `create_alert_rule` / `update_alert_rule` 结果带 `warning`，面板规则列表也会标出——分组改名后规则不会跟着改，要手动改。删除节点会把它从所有规则的列表里去掉，只指定了它的规则一并删除。
- 达量降速、续费、流量规则分别依赖节点的 `line.throttle_mbps`、`billing.expires_at`、`billing.traffic_limit`；没填的节点即使在范围内也不会触发。

## 任务结构

任务是 `type` 区分的联合类型，定义放在与类型同名的对象里，另一个不能同时出现：

```json
{"name":"三网","type":"probe","enabled":true,"node_ids":["…"],
 "probe":{"checks":["tcp","route"],"targets":[{"host":"…","port":443,"family":"ipv4","region":"上海","carrier":"电信"}],
          "interval":60,"route_interval":3600}}
{"name":"解锁检测","type":"unlock","enabled":true,"node_ids":["…"],
 "unlock":{"interval":3600,"family":""}}
```

- `probe.interval` 是 TCP 秒数（30–86400，选了 tcp 时必填），`probe.route_interval` 是回程秒数（900–86400，选了 route 时必填）。
- 目标必须明确指定 ipv4 或 ipv6；双栈域名填写为两个目标，IP 字面量必须匹配地址族。运营商支持电信、联通、移动、其他。最多 200 个地址；重复检测项被拒绝。
- 目标可带 `checks`，只对这个目标收窄任务的检测项，须是 `probe.checks` 的子集；省略即全部。例如同一任务里三网目标测延迟 + 回程、海外服务只测延迟：`{"host":"www.google.com","port":443,"family":"ipv4","region":"Google","carrier":"其他","checks":["tcp"]}`。
- `unlock.family` 留空即自动：IPv4，纯 IPv6 的机器改派 IPv6。间隔最短 3600 秒。
- `node_ids` 是明确节点列表，空列表不执行。

Server 把任务展开为单项探测（checks × targets，目标自带 `checks` 时只取其中那几项；解锁任务一项）。Agent 领到的 `job.task` 与观测里的 `task` 是这一项探测：`id`（所属任务）、`name`、`kind`（tcp / route / unlock）、`host`、`port`、`family`、`region`、`carrier`、`interval`。每项独立计时与保留最近结果；排队时优先最早到期项。

编辑任务时，只有「探测什么」变了（检测项、目标、解锁出口协议）才清掉该任务的旧结果与在途租约；只改名称、间隔、启停或绑定节点会保留结果，与专用的启停、绑定接口一致。

## 周期校正、告警与历史

- `GET /api/v1/nodes/{id}/network-history?hours=24`：按 TCP 目标和 IP 协议返回历史序列，范围 1–168 小时、每个目标约 240 个时间桶。沿用节点详情的公开/管理员权限；失败连接不伪造零延迟，连接失败率按发送/成功次数加权。编辑或删除任务会清除其已有任务结果：面板与调度立刻按新状态显示，历史行由后台分批删除（每批持写锁约 0.1 秒，进程重启后接着删），删完之前已删任务的结果不再出现在这里。删除节点同理。
- `POST /api/v1/admin/nodes/{id}/traffic`：管理员提交 `{"used":5000000000,"cycle":<cycle_start>}`，将当前周期已用总量校正为指定字节数。周期已变化返回 409。校正保留原始计数，后续增量继续累计，下周期失效；更改重置时间或计费方向会清除校正。
- 节点配置的重置时间与到期时间都是 RFC3339（精确到秒）：`billing.reset_at` 是周期锚点，只取它的「日 + 时分秒」逐月顺延，短月取月末；`billing.expires_at` 留空表示不设。
- 后台表单的重置/到期时间填 `YYYY-MM-DD HH:MM:SS`（UTC），提交前转成 RFC3339；格式不符或日期不存在（如 2 月 31 日）在本地就报错。- 节点的 `billing.free`（布尔）标记免费机器：公开卡片写「免费」，保存时 `price` 与 `price_period` 清空。
- 告警事件带 `last_error`：最近一次通知投递错误；成功投递后清空。不保存 Telegram Token、请求 URL 或响应正文。

## 网络任务管理

- 任务的管理状态各有一个接口：`PUT /admin/tasks/{id}/enabled` 启停，`POST /admin/tasks/{id}/run` 立即执行，`PUT /admin/tasks/bindings` 绑定。绑定替换所选任务的全部绑定，空列表停止调度；无效节点或任务使本次整体失败，不会只改一部分。
- 启停与绑定保留历史结果，暂停不取消已领取的探测；立即执行要求启用且已绑定节点，由 Agent 拉取执行，遵守原有并发限制。请求使用一个时间标记，不积累无限任务队列。
- `GET /api/v1/admin/tasks/health` 返回 `{任务 id: {ok, failed, pending, last_finished}}`：各任务每个绑定节点、每个目标最近一次执行的汇总（未完成且超过完成时限的算失败），从没执行过的任务不出现；任务列表的「最近执行」一列用它。
- `GET /api/v1/admin/tasks/{id}/runs` 返回当前绑定节点每个已执行目标的最近领取时间、完成时间、结果和失败原因；全部尚未执行的节点显示未执行，未返回原始路径地址。
- 后台支持选择多个任务后统一绑定节点，并支持搜索和全选筛选结果。一次请求、一个事务保存。
- 提醒规则在「告警与通知 → 提醒规则」里逐条新建、编辑、删除，编辑时选范围并实时显示覆盖几台、其中几台被更具体的同类规则盖过；下方「各节点生效情况」逐台列出每种提醒的生效参数与来源（全部 / 分组 / 单独）。节点编辑表单只显示当前生效规则的摘要与入口。
- 复制任务打开预填创建表单，默认暂停，保存前可修改目标和绑定。改了探测内容的编辑会清除该任务旧结果；只改名称、间隔、启停或绑定不会清除。
