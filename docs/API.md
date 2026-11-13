# HTTP API v1

全部 JSON。管理自动化用 `Authorization: Bearer <admin-token>`；Agent 用自己的节点令牌。浏览器通过 `POST /api/v1/login` 提交后台账号 `{ "username": "...", "password": "..." }` 获取 Cookie，管理密钥登录已移除。生产环境必须由 TLS 代理保护。

密码校验前限制实际尝试速度：匿名请求共享 30 次突发额度，每 2 秒补充一次；持有效设备 Cookie 的请求使用独立的 10 次额度，每 6 秒补充一次。额度耗尽后在密码校验前直接返回 429，并通过 `Retry-After` 告知下一次额度恢复的等待秒数；正确密码同样遵守额度，成功和失败的校验都消耗额度。额度自动补充，不永久锁定账号；改密后旧设备 Cookie 失效。浏览器会话绑定登录时验证的密码版本；终端重置密码使所有旧会话失效，网页改密仅保留本次会话。

前台（访客）接口：`/nodes`、`/nodes/live`、`/nodes/{id}/history`、`/nodes/{id}/network-history`、`/nodes/{id}/observations`、`/observations`、`/latency-samples`、`/unlock-catalog`。后台开了「整个前台需要登录」时，未登录的请求一律 401 `{"error":"此面板需要登录后查看","login":true}`；关掉的区块里，`/nodes/{id}/network-history` 与 `/latency-samples` 属于 `network`、`/unlock-catalog` 属于 `unlock`，访客请求 404，观测接口滤掉对应类型。管理员不受这些开关影响。开关在 `/theme-default.js` 里以 `window.__tzaPublic={hidden, login}` 交给前台。

| 方法与路径（均以 /api/v1 开头） | 用途 |
| --- | --- |
| GET /nodes | 公开节点；登录管理员可见全部。后台关掉的前台区块，访客拿到的节点里对应字段为空：`network` 清空 `probe_targets` 与 `display`，`unlock` 清空 `unlock`，`billing` 只保留 `billing` 的 `reset_at`、`traffic_limit`、`traffic_mode`，`remaining_value` 为 null |
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
| GET /admin/block-checks | IP 被墙检测结论与节点最近两轮证据，需管理员鉴权；状态为 reachable / suspected / partial / unreachable / unknown / paused |
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
| GET、PUT /admin/panel | 面板显示项（PUT 只改传了的项）：站点名称 `site_name`（标签页标题与品牌区的名字，最多 32 字，空串恢复默认的 TZA Probe）、只读的 logo 地址 `site_logo`（未上传为空）、默认明暗 `default_theme`、皮肤 `default_skin`、延迟色带时间窗（`latency_window_minutes`，1–10080 分钟：色带固定画最近 10 次探测，这一项是它们的年龄上限，早于它的不画；界面给 1 小时 / 6 小时 / 24 小时 / 3 天 / 7 天 档位，7 天即探测结果保留上限）、计费汇总单位 `price_unit`（公开面板顶部「剩余价值汇总 / 每月成本」统一折算成的货币，取值与节点货币选项同表，默认 CNY）、前台关掉的区块 `public_hidden`（`network` 延迟与回程、`unlock` 解锁检测、`billing` 价格与到期；关掉后访客接口不再返回这部分数据，见下方公开接口说明）、整个前台是否需要登录 `public_login` |
| PUT、DELETE /admin/panel/logo | 站点 logo：PUT 的请求体就是图片本身（不走 JSON），类型按内容判断，只收 PNG / JPEG / GIF / WebP / ICO / SVG，最大 512 KB，回 `{"site_logo":"/site-logo?v=…"}`；SVG 含脚本、事件属性、外链引用或 DOCTYPE 时 400，超大 413。DELETE 移除，回到内置字标。文件存在数据目录的 `branding/`，不在数据库备份里 |
| GET、POST /admin/themes、DELETE /admin/themes/{id} | 主题包清单（内置 + 已安装）、上传 zip、删除，见 [THEMES.md](THEMES.md#接口) |
| PUT /admin/themes/{id}/settings | 改主题设置（主题在 theme.json 里声明的菜单，附加包开关也在其中），请求体只写要改的项，回 `{options}`，见 [THEMES.md](THEMES.md#主题设置) |
| GET /admin/plugins | 已安装的插件：`[{id, kind, name, hint, version, author, source, permissions, settings, targets}]`；通知插件带设置表单 `settings`（key、label、type、required、secret、help、placeholder、default、options），目标集带目标个数 `targets` |
| POST /admin/plugins/inspect | 请求体是插件 zip（`Content-Type: application/zip`）：只校验、不安装，回同样的结构（面板据此列出权限请管理员确认）；不合规 400 |
| POST /admin/plugins | 安装上传的插件 zip，回装好的插件；同 id 替换。格式见 [PLUGINS.md](PLUGINS.md) |
| DELETE /admin/plugins/{id} | 删除插件；正被用作通知渠道时 409（先在配置出口换渠道），没有这个插件 404 |
| POST /mcp | Model Context Protocol 工具（读取与写入，不在 /api/v1 前缀下，见下节） |
| POST /admin/nodes/batch | 批量修改：`{"ids":[…],"patch":{…}}`，同一份部分字段（结构同 PATCH /admin/nodes/{id}，嵌套对象按字段合并）合并到每台节点，一个事务，全成或全不改；常用 `group`、`region`、`display.route_region`、`public`。不能改 `name` / `order`；回 `{"updated":N}` |
| POST /admin/groups/rename | `{"from":…,"to":…}`：整组改名，作用于 `from` 的提醒规则一起改；`to` 是已有分组即合并（合并后同类规则冲突则 400、不改），为空即解散回未分组。回 `{"moved":N}` |
| GET、POST /admin/agents/upgrade | 一键升级 Agent。POST `{"ids":[…]}` 把节点排进升级队列，回每台的结果 `results`：`queued`（下一次上报时下发）、`current`（已是签名版本或更新）、`manual`（Agent 较早或节点上没钉升级公钥，需在机器上不带 `--token` 执行一次安装命令）、`unsigned`（该架构的安装包没有可用签名）、`unavailable`（服务端没有该架构的安装包）、`never`（从未接入）。GET 回 `{version, archs, signed, items:{<id>:{state, error, version, requested_at, sent_at}}}`，`signed` 是签名可用的架构，`state` 另有 `sent`（已下发，等它以新版本上报）与 `failed`（下发 5 分钟后版本仍未变）。队列只在内存里，Server 重启后重新点即可 |
| POST /agent/report | `{protocol:1,metrics:{...}}`；平常回 204。排了升级、且 Agent 报了 `self_update` 能力时回 200 `{"upgrade":{"version","path","sha256","arch","key_id","sig"}}`：`sig` 是运维者对 `(arch, version, sha256)` 的 Ed25519 签名（`internal/agentsig`），Agent 只在签名来自钉住的公钥、架构相符、版本比自己新时才从同一台 Server 的 `path` 下载、校验摘要、替换自身并以状态 3 退出，由 systemd / OpenRC 重启 |
| POST /agent/heartbeat | `{protocol:1,status:{state,reason?,version,os,arch,distro?,cpu_cores?,capabilities?}}`；节点令牌鉴权，通常回 204，有签名升级时与 report 相同回 200。每节点最短间隔 3 秒，只更新连接和诊断，不写指标、历史或流量。见下文 |
| GET /agent/jobs | 获取有限租约任务：按种类分别领取，最多 8 个 TCP + 1 个回程 + 1 个解锁（Agent 须报 `parallel_jobs` 能力，否则整批最多两个） |
| POST /agent/results | 上传与租约绑定的结果 |
| GET /admin/remote | 远程管理：每台节点管理通道的状态 `[{id, name, connected, since, capabilities}]`；只有本机开启了远程管理（`--manage`）的 Agent 会连上 |
| POST /admin/remote/exec | `{"node_id":…,"command":…,"timeout_seconds":60}`：经管理通道在节点上用 `/bin/sh -c` 执行一条命令（命令最多 16 KB，超时 1–3600 秒），等它结束后回 `{output, exit_code, error, truncated, duration_ms}`，输出最多 1 MB；通道未连接回 502。命令原文与退出码写入审计日志 |
| GET /admin/remote/terminal?node=&cols=&rows= | WebSocket（须与面板同源）：节点上的交互终端（登录 shell + PTY）。浏览器发 `{"i":"按键"}` 与 `{"resize":{"cols","rows"}}`，收二进制帧的终端输出，结束时收文本帧 `{"exit":码}`。会话的开始与结束（时长、键入字节数，不记录内容）写入审计日志 |
| GET /admin/nodes/{id}/locate | 按这台节点 Agent 最近一次连接的公网源地址（经本机反代时取 `X-Forwarded-For` 的最后一项，只认公网地址，只存内存）查定位（ipwho.is，中文名，缓存一天）：`{ip, country_code, country, province, city, region, latitude, longitude, timezone, supported}`。`region` 是「节点所在地区」的取值（大陆取城市，港澳台用简称，其他用国名），`supported` 表示能否作为地区偏移的目标地区。还没收到公网连接时 409 |
| GET /admin/ipguard；POST /admin/ipguard/tasks；PUT、DELETE /admin/ipguard/tasks/{id}；POST /admin/ipguard/tasks/{id}/run；PUT、DELETE /admin/ipguard/node-settings/{id} | 地区偏移检测。GET 回任务 `tasks`、节点设置 `node_settings`、未生效的节点及原因 `pending`，以及各节点的出口状态、推导出的检测设置与最近检测。任务 `{name, enabled, auto_recover, services, node_ids}`，一台节点只能属于一个任务；`run` 给任务里已就绪的节点各排一次检测，回 `{queued, skipped}`。节点设置 `{country, family, position}`（目标地区、出口地址族、可选精确定位），PUT 回 `{settings, pending}`。每台节点的检测设置由任务和节点设置推导，不能直接编辑 |
| GET、POST /admin/users；PUT、DELETE /admin/users/{id}；POST /admin/users/{id}/token；POST /admin/users/{id}/reset | 使用者（分给朋友的账号）：`{name, note, enabled, traffic_limit, reset_day, expires_at}`。`traffic_limit` 为字节、0 不限；`reset_day` 为每月 1–28 日、0 不重置；`expires_at` 为 RFC 3339、空为长期。返回值另带 `id`、`token`（订阅与用量页凭证）、`traffic_used`、`period_start`、`next_reset` 和 `status`（`active` / `disabled` / `expired` / `exceeded`）。`token` 换一个新的，旧链接立即失效；`reset` 清零本期用量并从现在开始新周期 |
| GET /u/{token} | 使用者凭 token 查看自己的用量（不需要登录，不受「整个前台需要登录」影响）：`{name, status, traffic_limit, traffic_used, traffic_up, traffic_down, reset_day, period_start, next_reset, expires_at}`，不含 id 与备注。token 不存在时 404 |
| GET /admin/proxy；POST /admin/proxy/inbounds；PUT、DELETE /admin/proxy/inbounds/{id}；GET /admin/proxy/inbounds/{id}/links；POST /admin/proxy/sync | 代理。GET 回入站 `inbounds`、各节点同步状态 `nodes`（`capable`、`connected`、`arch`、`error`、`kernels.{xray,sing-box}.{version, active, synced, error, stats_error}`）和固定的内核版本 `kernels`。入站 `{name, node_id, kernel, protocol, port, enabled, address, user_ids, reality: {dest, server_name}}`：`kernel` 为 `xray` 或 `sing-box`，`protocol` 为 `vless-reality` 或 `ss2022`（2022-blake3-aes-128-gcm），`user_ids` 为空表示全部使用者；Reality 的密钥对与 short id、SS 的服务端密钥由主控生成，编辑时保留。同一节点上启用的入站不能撞端口。`links` 回每位可用使用者的分享链接（需要先填 `address`）。`sync` 立即同步（同时读入 xray 的流量计数）：最多等 20 秒，做完回 200 `{nodes, syncing:false}`；没做完（如首次给节点传内核）回 202 `{nodes, syncing:true}`，同步在后台继续，轮询 GET /admin/proxy 的 `syncing` 直到 false，`sync_error` 是最近一轮的错误。端口 62789 留给 xray 的流量统计接口 |
| POST /admin/proxy/forwards；PUT、DELETE /admin/proxy/forwards/{id} | 转发规则（GET /admin/proxy 的 `forwards` 列出全部）。`{name, enabled, user_id, node_id, port, network, target, target_port, exit}`：`node_id`/`port` 是入口，`network` 为 `tcp`、`udp` 或 `tcp,udp`（默认），`target`/`target_port` 是目标；`exit` 为空是直接转发，`{node_id, address, port}` 是经隧道中转（`address` 是入口节点连出口节点用的域名或 IP，`port` 是出口节点上为这条规则开的隧道端口，密钥 `key` 由主控生成并保留）。`user_id` 为空只统计；有值时流量计入这位使用者，他不可用时规则停掉。`traffic_up`、`traffic_down` 是规则创建以来的累计流量，由主控维护，提交时忽略。入口和隧道端口不能与同节点上启用的入站或其他转发冲突，也不能用 62789 |
| GET /admin/nodes/{id}/address | 节点 Agent 最近一次连接用的公网 IP `{ip}`（填写连接地址用）；还没收到公网连接时 409 |
| GET /admin/docker；GET /admin/docker/nodes/{node} | Docker。前者列出节点 `{id, name, connected, capable}`（`capable` 是 Agent 报了 `docker` 能力）。后者是一台节点的概况：`version`、`containers`、`images` 是 Docker Engine 自己的 JSON（`/version`、`/containers/json?all=1`、`/images/json`），`compose` 是 `{projects: [{name, dir, files, managed, containers, running}], compose}`（`compose` 是 compose 插件版本，没有插件时为空），`error` 是读取时遇到的错误。节点没连管理通道或没有 Docker 能力时 409 |
| POST /admin/docker/nodes/{node}/containers/{id}/{start\|stop\|restart}；DELETE /admin/docker/nodes/{node}/containers/{id}；DELETE /admin/docker/nodes/{node}/images/{id}[?force=1] | 容器启停、强制删除容器、按 ID 删镜像。Docker 的错误原样带回状态码和 `error`；已经是目标状态（304）算成功。都写审计日志 |
| GET、PUT /admin/docker/nodes/{node}/compose/{project} | 读或保存项目的 compose 文件 `{path, content}` / `{content}`（最多 256 KB）。项目名 `[a-z0-9][a-z0-9_-]*`；不存在的项目新建在节点的 `/opt/tza-compose/<项目名>/compose.yaml`。保存前节点运行 `docker compose config --quiet`，不通过回 400 `{error, output}` |
| GET /admin/docker/stream（WebSocket） | `node` 加 `kind=logs`（`container`、`tail`、`follow=1`、`timestamps=1`）、`pull`（`image`）、`compose`（`project`、`action` 为 up / down / pull / restart / delete；delete 先 down，面板建的项目再删目录，`volumes=1` 同时删数据卷）或 `exec`（`container`、`cols`、`rows`，交互）。浏览器发 `{"i": 键入}`、`{"resize": {cols, rows}}`；收二进制帧的输出和最后一条文本帧 `{exit, error}`。要求同源 Origin，开启和结束都写审计日志 |
| GET /admin/certs；POST /admin/certs；PUT、DELETE /admin/certs/{id} | 证书。GET 回 `certs`（`{id, domains, dns_id, targets, serial, issuer, not_before, not_after, status, error, next_attempt, issuing}`，不带证书和私钥）、`dns`（Cloudflare 账号，令牌只回 `token_hint`）和 `staging`。提交 `{domains, dns_id, targets: [{node_id, dir, reload}]}`：域名第一个是证书名，`*.` 开头是通配符，最多 20 个；`dir` 留空为 `/etc/tza-certs/<域名>`；`status` 为 pending、valid 或 error。改了域名会重签。删除不动节点上的文件 |
| POST /admin/certs/{id}/renew；POST /admin/certs/{id}/deploy；GET /admin/certs/{id}/download?part=fullchain\|key | 立即续期（清掉失败退避）、重新下发到所有目标、下载证书链或私钥（PEM，私钥下载写审计日志） |
| POST /admin/certs/dns；PUT、DELETE /admin/certs/dns/{id}；PUT /admin/certs/settings | Cloudflare DNS 账号 `{name, token}`（保存前向 Cloudflare 校验令牌；编辑时 `token` 留空不改；还有证书在用时删除回 409）。`settings` 为 `{staging}`：用 Let's Encrypt 测试环境 |
| GET /sub/{token} | 使用者的订阅，不需要登录（不受「整个前台需要登录」影响）。`?format=clash`（mihomo YAML）、`singbox`（sing-box 1.12+ JSON）、`base64`（分享链接），省略时按 User-Agent 判断；`?mode=global` 全部代理，默认国内直连。响应头 `Subscription-Userinfo: upload=…; download=…; total=…[; expire=…]`、`Profile-Update-Interval: 12`、`Profile-Title`。token 不存在 404，使用者停用或过期 403（纯文本说明）；流量用完的照常返回，客户端据此显示用量 |
| GET /admin/overview | 后台总览：`{cards, generated_at}`。每张卡 `{id, title, page, value, note, tone, items:[{label, note, tone}]}`，`tone` 为空、`good`、`warn` 或 `bad`，`page` 是点开去的后台页面。先是核心的 `nodes`、`billing`（两周内到期与流量过 90%）、`tasks`（含被墙检测异常）、`alerts`，再是各模块的（`regiondrift`、`remote`） |
| GET /admin/audit?before=&limit=&node= | 审计日志，按 id 倒序分页（`limit` 默认 100、上限 500）：后台的每次写操作（路由与状态码，不含请求体）、登录与登录失败、远程命令与终端。字段 `{id, at, actor, source, action, node_id, status, detail}`，保留 365 天 |
| GET /admin/totp、POST /admin/totp/setup、POST /admin/totp/enable、POST /admin/totp/disable | 两步验证：GET 回 `{"enabled":…}`；setup 生成新密钥回 `{secret, uri}`（10 分钟内有效）；enable `{"code":…}` 用它算出的验证码确认后生效；disable `{"code":…}` 用当前验证码关闭。开启后 `POST /login` 还要 `code`：密码正确但缺验证码或验证码错误时 401 `{"error":…,"totp":true}`，同一个验证码只能用一次 |
| GET /agent/channel | 管理通道（协议 v2）：Agent 用节点令牌建立的 WebSocket，请求头 `X-Channel-Capabilities` 列出它提供的能力（如 `shell`），其上用 yamux 多路复用，由 Server 按需开流，见 [ARCHITECTURE.md](ARCHITECTURE.md#管理通道) |

不在 /api/v1 下的两个品牌相关地址：`GET /site-logo` 公开提供已上传的 logo（没有时 404；带 `nosniff` 和自己的 `default-src 'none'; sandbox` CSP，直接打开 SVG 也跑不了脚本）；`GET /theme-default.js` 是首屏脚本，除默认外观外另写 `window.__tzaBrand={"name":…,"logo":…}`，`theme-boot.js` 据此在首屏前换掉标签页标题与 favicon。

准确字段由 `internal/model` 中的 JSON 标签定义。未登录请求拿到的节点不含提醒规则（`alerts` 为空）。解锁结论只随 `/nodes` 的 `unlock` 字段下发。

## Model Context Protocol（`POST /mcp`）

首次接入请先阅读 [MCP 接入指南](MCP.md)，其中包含 Key 生成、客户端配置、工具清单及可运行的 curl 示例。

请求头携带 `Authorization: Bearer <MCP API Key>`，Key 在“面板设置”生成，初始未配置时端点拒绝访问。管理密钥、面板会话 Cookie 与节点密钥不通行，MCP Key 也不能登录后台或访问管理 HTTP API，跨源写入按原有规则拒绝。写工具与面板共用同一套持久化与校验，因此 AI 客户端改出来的状态和面板改出来的完全一致。只接受 POST 与 `application/json`，GET / DELETE 返回 405；响应默认为 `application/json`，客户端只接受 `text/event-stream` 时用一条 SSE 消息回复同一条 JSON-RPC 消息。没有会话状态，每个请求独立处理。

同一个地址支持两代协议：

- `2026-07-28`（无握手）：每个请求在 `_meta` 里声明版本，并在头部镜像 `MCP-Protocol-Version`、`Mcp-Method`（`tools/call` 还有 `Mcp-Name`，非 ASCII 用 `=?base64?…?=`），三者必须与请求体一致，缺失或不符返回 400 与 `-32020 HeaderMismatch`；版本不认识返回 400 与 `-32022 UnsupportedProtocolVersionError`（`data.supported` 列出支持版本）；方法不认识返回 404 与 `-32601`。结果带 `resultType: "complete"` 与 `_meta["io.modelcontextprotocol/serverInfo"]`，`server/discover` 与 `tools/list` 带 `ttlMs` / `cacheScope`。
- `2025-11-25`、`2025-06-18`（`initialize` 握手）：按客户端声明的版本回答，之后用 `MCP-Protocol-Version` 头声明，结果不带 `resultType` / 缓存字段。完全没有版本头的请求按 `2025-11-25` 处理（`2025-03-26` 及更早的客户端不发这个头）。

工具分读写两部分，`tools/list` 的 `annotations` 用 `readOnlyHint` / `destructiveHint` 标注。读取：`block_checks`（IP 被墙检测结论与节点最近两轮证据）、`list_nodes`、`list_tasks`、`list_alert_rules`、`node_history`、`node_network_history`、`node_observations`、`recent_events`（可选 `before` / `limit` / `node_id` / `kind`，与 HTTP 同一套分页）、`latency_samples`、`list_catalog`（可选 `region` / `carrier` / `family` 过滤，含已装目标集插件的目标）、`task_runs`、`list_plugins`、`get_settings`。写入：`create_node`、`update_node`、`delete_node`、`rotate_node_token`、`correct_node_traffic`、`create_task`、`update_task`、`delete_task`、`control_task`（`action` 取 `pause` / `resume` / `run` / `bind`，`bind` 带 `node_ids`；内部分别走启停、立即执行、绑定三个操作）、`create_alert_rule` / `update_alert_rule`（`rule_id` + 要改的字段，合并语义）/ `delete_alert_rule`、`update_settings`、`test_notification`。参数规则与 HTTP 接口一致：`hours` 省略或为 0 用默认 24（主机历史上限 720、网络历史上限 168），`limit` 默认 20、上限 60，超出按上限截断并在结果里回显实际窗口；未知参数、缺少 `node_id` / `task_id`（`rule_id` 缺失按工具错误返回）、未知工具、非法枚举，以及存储校验拒绝的输入（如绑定不存在的节点）都返回 `-32602`；节点或任务不存在、存储读写失败按工具结果返回 `isError: true` 与说明文字。`update_node` / `update_task` / `update_alert_rule` 是合并语义：只提交要改的字段，没提到的字段保持原值，嵌套对象（`billing` 等）按字段合并，列表整份替换；`update_alert_rule` 改了 `scope` 时，新范围用不上的列表没重新提交就清掉（`exclude_node_ids` 在 `all` 与 `group` 之间切换时保留，被排除的节点不会因为换了范围就开始告警）。
`create_node` 与 `rotate_node_token` 返回 `id`、`token` 和 `install_command`——与面板「接入」弹窗同一条命令，`--endpoint` 由请求的 Host 与 `X-Forwarded-Proto` 拼出，端点不是 HTTPS 时附 `warning` 提示。`update_settings` / `get_settings` 只回渠道状态与掩码字段，已存的 Bot Token / Webhook 从不回显（两者另外回面板显示项：延迟色带时间窗 `latency_window_minutes`、访客默认明暗 `default_theme` 与皮肤 `default_skin`、站点名称 `site_name` 与 logo 地址 `site_logo`、计费汇总单位 `price_unit`；logo 不能经 MCP 上传）。`update_settings` 只改传了的项：不传 `channel` 沿用当前渠道，Telegram 的 `bot_token` / `chat_id` 留空沿用已存的，只传显示项时不动通知配置；关闭通知须显式传 `channel: "none"`，一项都不传返回 `-32602`；`test_notification` 复用面板的发送路径，未配置渠道或发送失败按工具错误返回原因。


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
- `billing`：`reset_at` 是周期锚点；`free` 见下文。新建（POST）时 `reset_at` 留空取本月 1 日 00:00 UTC，`traffic_mode` 留空取 `sum`；编辑（PUT）是整份替换，仍要写全。
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

## 独立主题前台 API

主题包可声明 `frontend: {"entry":"index.html","api_version":1}`，通过隔离文档中的 `window.TZA` 使用现有 `/api/v1/` 公开读接口。
桥接明确省略宿主后台会话；公开节点、历史、探测结果与解锁目录都遵循匿名可见范围。
完整协议、类型与示例见 [FRONTENDS.md](FRONTENDS.md) 和 [frontend-api.d.ts](frontend-api.d.ts)。

## Agent 连接与指标诊断

`/nodes`、`/nodes/live` 与后台节点返回 `last_seen`（最近收到有效指标或心跳）、`metrics_at`（最近有效指标，0 表示从未采集成功）、`metrics_stale`、`agent_status`。心跳超过指标时间时显示诊断；有效指标上报会原子清除旧诊断，`agent_status:null` 明确通知前端清除旧状态。仍在线但指标超过 45 秒，或诊断为采集/上报失败时，`metrics_stale:true`、`metrics:null`，不能当作零使用率。离线判定仍为 45 秒无连接。历史与流量只来自有效指标。

`state` 为 `starting`、`collecting`、`reporting`、`ok`、`collect_failed`、`collect_timeout`、`report_failed`；`reason` 是固定分类 `cpu/memory/disk/network/interfaces/uptime/boot_time/invalid/unknown/timeout/transport` 或空。Agent 不通过诊断传输原始错误、挂载路径、网卡名或凭据；具体错误保留在本机日志。元数据继续用于识别版本及签名升级，不作为指标样本。

## 当前告警的评估暂停

`GET /admin/events?active=1` 在已触发的 CPU / 内存告警因指标不可用而暂停评估时返回 `evaluation_paused:true`。此时 `active:true` 表示该告警尚未获得解除依据，消息和采样时间仍是上次触发/重复事件的快照。恢复有效读数后，该字段消失：正常读数解除，高读数继续原告警。未达到持续要求的计时在数据缺失时重置。关闭或移除规则仍会解除告警。

普通事件历史和通知投递快照不含这个实时字段，内容不随节点当前状态变化。暂停标记继续使用原有字段；Agent 状态排序使用 schema 17 的独立会话水位表。

## Agent 状态排序

新 Agent 在每次进程启动时生成随机 `agent_session`（64 位小写十六进制），在指标与诊断中共用递增的 `agent_sequence`（正整数）。同一故障的重复心跳保留序号；成功报告分配更大的序号，旧故障心跳晚到也不会覆盖恢复结果。服务端在同一事务中保存顺序水位。未知会话首次请求返回 HTTP 409 和 `{"agent_session_challenge":"<64位十六进制>"}`，不更新节点状态；Agent 将该值放入同一指标/诊断的 `agent_session_proof` 后重试。挑战绑定节点、请求会话和当前活动会话；活动会话变化后，旧挑战失效，必须重新确认。HTTP Agent 自动处理挑战，每次调用最多发送 3 次请求；普通限频 409 不重试。确认凭证不写入指标、历史或公开诊断。新会话确认并取代旧会话后，旧会话的在途请求被忽略。退役会话保留 7 天，当前会话不因离线清理。序号不依赖 Agent 或 Server 的墙上时钟。

省略会话排序字段的旧 Agent 仍可接入，沿用接收顺序。升级时先部署 Server（自动迁移至 schema 17），再部署 Agent；新 Agent 的字段不应发送给旧 Server。回退旧 Server 前须恢复升级前的数据库备份。


## 剩余价值

节点完整视图与 `/nodes/live` 的 `remaining_value` 为同币种的时间比例估算：`价格 × max(0, 到期时刻 − 当前时刻) ÷ 价格周期时长`，精确到秒，界面显示两位小数。固定折算：周 7 天、月 30 天、季 90 天、半年 180 天、年 365 天、两年 730 天、三年 1095 天；与流量重置周期独立，也不按流量余量打折。多期预付可高于单期价格。免费节点为 0；付费节点缺少正价格、有效到期时间或上述周期时为 `null`，不推测未知值。沿用 `billing.currency`，不换汇或混合汇总币种。公开面板顶部的「剩余价值汇总 / 每月成本」两块磁贴在前端把各节点的估算与每月折算价（`价格 × 30 ÷ 周期天数`）按面板设置 `price_unit` 统一：跨币种按 `web/format.js` 里的内置固定参考汇率折算，不取行情、不随市场更新；币种不在汇率表、未配置剩余价值或未填价格 / 周期的节点不计入，磁贴的悬停说明给出计入台数。


## IP 被墙检测任务

通过现有任务 CRUD 创建 `type=block`，例如：

```json
{
  "name": "目标 IP 可达性",
  "type": "block",
  "enabled": true,
  "node_ids": ["mainland-node", "overseas-node"],
  "block": {
    "host": "1.1.1.1",
    "port": 443,
    "family": "ipv4",
    "interval": 300,
    "mainland_node_ids": ["mainland-node"],
    "overseas_node_ids": ["overseas-node"]
  }
}
```

`host` 须为公网 IP 字面量，协议须匹配，端口 1–65535，间隔 60–3600 秒；两组节点至少各一个，不能重复，合计最多 20 个，`node_ids` 必须等于两组的并集。不能带 `probe` / `unlock`。节点角色由管理员依据实际直连出口选择，不从节点名称或地区标签自动推断。修改角色请用任务 PUT / PATCH 同时更新 `node_ids` 与 `block`，批量绑定不支持更改此类任务的角色。

Server 向这些节点派发普通 TCP 作业，兼容现有 Agent，每轮连接 3 次；`block_checks` MCP 工具与 `GET /api/v1/admin/block-checks` 提供相同结果。每项含 `task_id/name/host/port/family/status/detail/evidence`；`evidence` 含 `node_id/node_name/role/state/attempts`，`role` 为 mainland / overseas，`attempts` 最多两条（新的在前，含 issued / finished / result）。离线或已删除节点不使用历史证据；上报异常、协议不可用、未返回结果也不算连接失败。

大陆节点全部连续两轮失败（每轮实际尝试 3 次且无成功，两轮至少间隔 30 秒），并有境外节点在相邻检测周期可达时返回 `suspected`。全部大陆节点可达为 `reachable`；部分大陆节点可达且另有节点连续失败为 `partial`；大陆与境外全部连续两轮失败为 `unreachable`；证据不足为 `unknown`，暂停为 `paused`。证据有效期为 2 倍检测间隔加 30 秒；疑似判定还要求境内外最新检测发起时间相差不超过 1 倍间隔加 30 秒。修改检测配置立即隔离旧作业证据。目标防火墙、地区封禁、服务异常与路由故障均可能影响结果，此判定不能证明封锁原因，不会自动发送告警。
