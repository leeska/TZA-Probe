# 组件边界

所有运行组件统一版本发布，依赖方向单向。解耦不要求运行消息队列或额外服务。

```text
web (HTTP JSON) ───> httpapi ──┬──> service ───> Store port <── storage (SQLite)
                               │       ▲
                               │       └── mcp（MCP 工具：同一组管理操作）
                               ├──> Repository port（公开可见性、Agent 协议）
                               └──> theme.Store / plugin.Store ──> pkg/tzapkg（包格式、插件模板）
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
- `pkg/tzapkg` 是包格式：清单解析与文件校验。它是唯一可导出的包，只依赖标准库。签名代码 `sign.go` 是原 TZA-CC 市场审核签名用的，Probe 不调用；市场已停用，这部分暂时保留，以后若恢复市场再用。`theme` 与 `plugin` 用它落地安装。
- `plugin` 管已安装的声明式插件（`<data>/plugins/<id>/plugin.json`，不对外提供）：安装、列出、读取、删除，只认 `pkg/tzapkg` 校验通过的 `plugin.json`。插件是数据与模板，没有代码：通知插件由 `notify.Transport` 解释成一个 HTTP 请求（只能发往插件声明的域名，5 秒超时、不跟随重定向、回应最多读 64 KB），目标集只由 `service.Catalog` 并入任务目录、由 `KnownRegions` 认它们的地区。哪些事情插件不能碰（节点密钥、MCP Key、其他通知凭据、数据库、Agent）由这个结构保证：模板只能读事件、本插件的设置和派生值。「正被用作通知渠道的插件不能删」这条规则在 `service.RemovePlugin`。依赖方向：`service` / `notify` 只经接口用插件，`plugin` 只依赖 `pkg/tzapkg`。
- `theme` 管已安装的包：只内置现代极简（默认，`Builtins`），液态玻璃 / 复古印刷 / 二次元是仓库 `themes/` 下的官方主题包，`tools/themepack` 打成 zip 随发行包发布；附加包（`"type": "addon"`）依附某一套主题，给它添装饰项。面板只提供通用机制：卡片装饰槽、按节点挑装饰（`decorations.js`，结果写成 `data-deco` 与 `--deco-*` 变量）、布局变体与基础层，具体画法全在各主题包的样式表里。包里的点文件不经 `/themes/` 对外提供。主题在 theme.json 里声明的设置菜单（`settings`，见 docs/THEMES.md「主题设置」）由 `pkg/tzapkg` 校验，管理员的取值存在 settings 表的 `skin_options`，随清单发给访客。
- `web` 只调用 `/api/v1`。`api.js` 处理通信，`app.js` 负责公开页，只在进入 `/admin` 时才动态加载后台模块。后台的外壳（登录、侧栏、切页）是 `admin.js`，页面清单在 `admin-pages.js`，各页分别是 `admin-nodes.js`、`admin-tasks.js`、`admin-alerts.js`（含按插件设置画出的通知渠道表单）、`admin-themes.js`（已安装的主题与挂在各自主题下的附加包）、`admin-plugins.js`（已安装的插件）、`admin-settings.js`（子页：外观与品牌 / 账号与安全 / 数据），主题与插件页共用的小部件在 `admin-packages.js`，共用状态与表单工具在 `admin-state.js`；页面之间经 `admin-state.js` 的 `go()` / `render()` 跳转与重绘，不互相引用外壳。`detail.js` 负责共享详情，`charts.js` 与 `format.js` 负责展示。`task-editor.js` 负责任务表单，`task-controls.js` 负责任务操作，`agent-setup.js` 负责接入弹窗（一键安装命令与 agent.json）。`internal/agentdist` 通过 `/install.sh` 与 `/dl/tza-agent-linux-<arch>` 分发静态安装脚本和 Agent 二进制，二进制优先取数据目录 `agents/`，其次取构建时嵌入的副本；同处的 `.sig` 与 `upgrade-key.pub` 是一键升级用的运维者签名与公钥（`internal/agentsig`，私钥不在 Server 上）。它们通过 HTTP / 回调协作。

Server 的发布产物包含网页，Agent 单独编译，只链接其依赖图，不包含数据库和管理界面。两者通过版本化 JSON 协议协作。SQLite WAL 支持读写，单连接与显式事务约束写入顺序；任务租约、流量差值、告警转换具备事务边界。

任务租约绑定节点、任务快照和 90 秒有效期。未知节点、重复结果、过期结果无法写入。同一种类在已有有效租约时不再下发（解锁检测的有效期与其 15 分钟上报窗口一致）；报了 `parallel_jobs` 能力的 Agent 按种类分别判断，一次最多领 8 个 TCP、1 个回程、1 个解锁任务并各自并行执行，长任务不阻塞延迟探测；没报这项能力的 Agent 按整批判断，每次最多两个任务。编辑任务定义使旧任务结果与租约失效。专用暂停、恢复、节点绑定操作保留历史；手动执行只调整调度时间，仍由 Agent 拉取，不引入远程命令通道。Agent 上传失败最多重试 3 次；下次探测周期重新下发，状态不能伪装为持续正常。

公开端无 IP 上报字段；路径在 Agent 脱敏，HTTP 再过滤。公开查询只允许公开节点，管理密钥与 Agent 密钥不能互换。浏览器会话为 12 小时 HttpOnly/SameSite Cookie，服务重启失效。配置节点密钥只返回一次，数据库保存 SHA-256 摘要。管理密钥来自环境或权限 0600 的本地文件，不在日志中出现。后台账号（用户名 + PBKDF2-SHA256 密码摘要）存于数据目录 0600 的 `admin-account`，登录及浏览器会话鉴权时重读，终端 `tza-server reset-password` 改完即生效并撤销旧浏览器会话；后台仅支持用户名密码登录，超限时直接拒绝、不再计算摘要。实际密码校验按桶限流：没有设备凭据的请求共享 30 次突发额度、每 2 秒补充一次；登录成功过的浏览器拿到 HMAC 签名的 `tza_device` Cookie（绑定用户名与密码摘要，180 天，改密码即失效），之后使用独立的 10 次突发额度、每 6 秒补充一次；额度耗尽时正确密码也需等待补充，通过 `Retry-After` 告知等待秒数。不按来源 IP 分桶，因为 Server 通常在反代或 CDN 之后。MCP 使用后台生成的独立 API Key，数据库只保存 SHA-256 摘要，生成时仅回显一次，重新生成与撤销即时生效。

设计刻意不抽象通用工作流、插件容器或万能 RPC 层；插件只有上面两类声明式格式，不执行第三方代码。新增功能是编进二进制的**模块**（见下节），不是运行时加载的插件。

## 模块

项目正从探针扩成自用的服务器管理中心，功能按模块组织，仍是一个 Server 二进制加一个 Agent 二进制。

- **Server 端**：`internal/module` 定义接口。一个模块实现 `Module`（`ID()`），再按需实现：
  - `Migrator`：在主库里建自己的表，见下节
  - `Router`：通过 `module.Routes` 挂路由，有 `Public`、`Admin`、`Agent` 三种；鉴权和同源检查用的是 httpapi 自己那一套
  - `Overviewer`：给后台总览提供卡片（`module.Card`：标题、结论、语气、最多 6 条待处理项、点开去哪一页）。总览页 `GET /admin/overview` 先放核心的四张（节点、到期与流量、网络探测、告警），再按模块顺序放各模块的；某个模块读数出错只把它那张卡标成「读取失败」，不影响整页
  
  `cmd/server` 的 `shippedModules()` 列出本二进制带的全部模块，顺序就是迁移的执行顺序。现有四个模块：`regiondrift`（地区偏移检测）、`remote`（远程管理：管理通道、命令、终端）和 `users`（使用者：分给朋友的账号，流量额度按月重置、到期时间、订阅 token 与只读用量页 `/u/{token}`）、`proxy`（代理：节点上的 VLESS Reality / Shadowsocks 2022 入站与转发，见下面「代理」一节）、`docker`（节点上的容器、镜像与 Compose 项目，见「Docker」一节）、`certs`（Let's Encrypt 证书的签发、续期与下发，见「证书」一节）和 `backup`（加密备份到节点，见「备份」一节）。模块用 `internal/httpjson` 读写 JSON、把错误翻成状态码，和 httpapi 用的是同一套规则。第一个独立出来的模块是 `internal/regiondrift`（地区偏移检测），检测逻辑本身仍在 `internal/ipguard` 和 `internal/browserworker`。
- **任务类型**：`model.RegisterTaskKind` 登记每种 `Task.Type`。登记的内容包括：
  - 定义从哪个字段读、怎么校验、怎么展开成探测
  - `FixedBindings`：节点带角色，只能编辑任务来改绑定，不能批量绑定或解绑
  - `PrivateTargets`：探测目标不进公开的目标规划
  - `PerNodeResults`：节点不再执行任务时，要清掉这台节点的派生结论
  
  storage 和 MCP 只查这张表，不再按类型名写分支。内置的三种类型 probe、unlock、block 也通过它登记。
- **Agent 能力**：Agent 在上报和心跳里带 `capabilities`。Server 只检查格式：小写标识符、不重复、最多 32 个，不认识的名字照样保留，所以更新的 Agent 不会因为多报了能力被拒。某种能力具体怎么用，由用到它的模块自己判断，例如 `AllowsUnlock`。
- **前台区块开关**：`model.PublicSections` 列出可关的区块（`network` 延迟与回程、`unlock` 解锁检测、`billing` 价格与到期），另有「整个前台需要登录」。访客接口都经 `httpapi/public.go` 的 `a.public(区块, handler)`：需要登录时一律 401（`{"login":true}`），区块关掉时属于它的接口 404，节点列表里对应字段清空（`publicPolicy.strip`），观测按类型过滤。开关缓存 2 秒，后台保存时立即失效。前台从 `/theme-default.js` 的 `window.__tzaPublic` 读到开关（`web/public-config.js`），管理员登录后看前台拿到的是全量数据，前台按同一规则去掉，版面与访客一致。
- **任务入口**：「网络探测」的「创建任务」先选类型：延迟与回程、解锁检测、IP 被墙检测、地区偏移检测。前三种在核心的任务表里；地区偏移检测任务属于 regiondrift 模块（`ipguard_tasks`），和解锁检测一样一次选多台节点，只管检测哪些服务、是否启用、是否自动校正。每台节点不同的部分（目标地区、出口地址族、精确定位）是节点设置（`ipguard_node_settings`），在「节点管理 → 编辑节点 → 所在地区与定位」里设。调度器实际执行的每台节点的检测设置（`ipguard_policies`）不再直接编辑，由 `regiondrift.reconcile` 从两者推导：保存任务或节点设置后立即推导一次，之后每分钟一次（出口刚连上的节点据此补上）。没变化时不重写，以免打断进行中的检测；没设置目标地区或出口未连接的节点记在 `pending` 里给后台显示。一台节点只能属于一个地区偏移检测任务。迁移 regiondrift/2 把已有的每台节点设置拆成节点设置和任务（开关与服务相同的节点合进同一个任务）。
- **后台页面**：`web/admin-pages.js` 是页面清单，每一项是 id、名称、图标和页面函数。侧栏、面包屑和切页都由这张清单生成，新模块加一项就有自己的页面。

### 管理通道

协议 1 的 HTTP 轮询（上报、心跳、领任务）保持不变。远程管理另走一条**管理通道**（`internal/channel`）：每个开启了远程管理的 Agent 保持一条到 `GET /api/v1/agent/channel` 的 WebSocket（节点令牌鉴权，必须不带 Origin），上面跑 yamux 多路复用。

- **谁开流**：由 Server 开流，每个操作（一条命令、一个终端，以后还有日志、配置下发）一条流。流的第一行是 JSON `Request{kind, params}`，Agent 回一行 `Response{error}`，之后是这种操作自己的消息。shell 用的是换行分隔的 JSON `Message{o, i, resize, exit, error}`。
- **连接**：Server 端的 `channel.Hub` 记着每台节点最新的一条连接。同一节点再连进来，就替换掉旧连接。yamux 每 20 秒保活一次，nginx 默认 60 秒的空闲超时不会断开它。Agent 断线后会退避重连，间隔从 2 秒起，最长 1 分钟。
- **开关**：Agent 只在 `/etc/tza-probe/manage.json` 开启时连通道，这个文件由安装脚本的 `--manage` 写入。同一文件里的 `disabled_capabilities` 能在本机关掉单项能力：关掉的能力不注册处理器，Server 要求也打不开。开启远程管理的节点换用不带沙箱的服务单元，Agent 以 root 运行，这是远程管理本来的要求。
- **鉴权与审计**：终端的浏览器 WebSocket 是 GET 请求，API 不会替它检查同源，所以 `internal/remote` 自己检查 Origin。每条命令（原文和退出码）、每个终端会话（开始、结束、时长、键入字节数，不记录内容）都写入审计日志。

### 代理

`internal/proxy` 管入站，`internal/agent/proxy.go` 在节点上执行。

- **内核来源**：xray 与 sing-box 的版本和各架构压缩包的 SHA-256 固定在 `proxy.Kernels` 里。主控第一次需要时从官方 GitHub release 下载，校验不符直接丢弃，取出内核文件缓存在数据目录的 `kernels/`。节点从不自己下载。
- **下发**：管理通道上的四种请求，能力名 `proxy`：`proxy.status`（已装的内核、生效的配置哈希、服务是否在运行）、`proxy.install`（请求行后跟内核文件原始字节，节点校验 SHA-256 后装到 `/usr/local/lib/tza-probe/kernels/<内核>/<版本>/`）、`proxy.apply`（请求行后跟配置原始字节；节点先用内核自检 `xray run -test` / `sing-box check`，通过才写入 `/etc/tza-proxy/<内核>.json`（root:tza-proxy 0640，目录 0750；不放在 0700 的 `/etc/tza-probe` 下，否则服务用户读不到）并重启服务）、`proxy.remove`（停服务、删配置，保留内核）。
- **服务**：每个内核一个 systemd 单元 `tza-proxy-<内核>`，以系统用户 `tza-proxy`（第一次应用时创建）运行，只给 `CAP_NET_BIND_SERVICE`；配置里有使用者的凭据，文件为 root:tza-proxy 0640。不用 DynamicUser / LoadCredential，因为它们要 systemd 247+，Ubuntu 20.04 还是 245。
- **同步**：`proxy.Module.Sync` 为每个节点按启用的入站渲染配置（排序后的 JSON，哈希判断是否需要下发），只给当前可用的使用者（停用、过期、流量用完的去掉）。内核版本不符先装内核，配置哈希不符或服务没在运行就重新应用，节点不再需要的内核就移除。保存入站或使用者后立即同步一次，之后每分钟一次。
- **凭据**：每位使用者有自己的 VLESS UUID 和 Shadowsocks 2022 密钥（`users.User.ProxyUUID` / `ProxyKey`），「更换链接」时和 token 一起换掉。
- **流量**：xray 配置打开 stats API（只监听 `127.0.0.1:62789`，入站不能用这个端口），使用者以 id 作为 email。每轮同步、在任何会重启内核的操作之前，主控发 `proxy.stats`，节点执行 `xray api statsquery -pattern user>>> -reset` 读出并清零按人的上下行字节数；主控用 `users.Store.AddUsage` 累加到本期用量（`traffic_up`、`traffic_down`、`traffic_used`）。有人因此用完额度时立即再同步一轮，把他从配置里去掉。官方 sing-box 没有编进 v2ray API，所以 sing-box 入站不统计、不计入额度。内核重启前没读到的计数会丢，最多一分钟的量。
- **转发**：`proxy.Forward`（表 `proxy_forwards`，迁移 proxy/2），全部跑在节点的 xray 里。直接转发是入口节点上的 dokodemo-door 入站（标签 `fw-<id>`）连目标；隧道再加一个 Shadowsocks 2022 出站（`fw-<id>-out`，每条规则一个密钥）和路由规则，连到出口节点上这条规则专用的 SS2022 入站（`tun-<id>`），出口节点用 freedom 连目标。`desired` 把每条生效的规则（启用，且归属的使用者存在并可用）分到入口和出口节点的 `Plan` 里，节点只有转发也会装 xray。入站与转发（入口端口、隧道出口端口）在每台节点上共用端口冲突检查（`Store.checkPorts`）。xray 配置打开 `policy.system` 的入站计数，Agent 读全部计数（不再只读 `user>>>`），按规则的入口入站累计到规则自己的 `traffic_up/down`，归属使用者的同时计入他的用量；出口一侧不计，避免重复。xray 的 splice 零拷贝会绕过计数器（直接转发的下行一直是 0），所以 Agent 写的 xray 服务单元带 `Environment=XRAY_BUF_SPLICE=disable`。
- **订阅**：`GET /api/v1/sub/{token}`（`proxy.Module.subscription`，凭 token 不需要登录）。格式取 `?format=clash|singbox|base64`，没有时按 User-Agent 判断（`proxy.FormatFor`），都认不出就给 base64 分享链接。Clash/mihomo 与 sing-box 配置用内置规则：私有地址和中国大陆直连，其余走「节点选择」（含「自动选择」测速组）；`?mode=global` 全部代理。只列出这位使用者能用、填了连接地址的启用入站。xray 26.9.8 起 Reality 服务端要求客户端带 X25519MLKEM768 密钥，sing-box 的 uTLS 指纹不带（SagerNet/sing-box#4520，放宽 minClientVer 也不行），所以 sing-box 订阅里去掉 xray 内核的 Reality 入站。响应头 `Subscription-Userinfo` 带用量、额度和到期，停用或过期的使用者返回 403。

### Docker

`internal/docker` 在主控上，`internal/agent/docker.go` 在节点上，能力名 `docker`（管理通道上，节点要用 `--manage` 接入；管理版的 Agent 单元没有沙箱，以 root 运行）。

- **Engine API**：Agent 直接连本机 `/var/run/docker.sock`（不解析 docker 命令行输出）。主控发 `docker.api`，Agent 只执行白名单里的调用：`/version`、`/info`、容器与镜像列表、单个容器的详情、容器 start / stop / restart / 删除、按 ID 删镜像；路径里的容器名和镜像 ID 用正则校验，查询参数也有白名单。建容器、exec、build 这类调用都不放行。
- **流**：日志（`docker.logs`，没有 TTY 的容器要拆 Engine 的 8 字节分帧）、拉镜像（`docker.pull`，把进度 JSON 压成一行一步）、Compose 操作（`docker.compose.run`：up -d --remove-orphans、down、pull、restart）和容器终端（`docker.exec`：PTY 上跑 `docker exec -it <容器> sh -c "bash 或 sh"`，和主机终端共用 `runTerminal`）。它们都说 shell 能力的 `channel.Message`，主控用远程管理模块的 `Bridge` 转成浏览器的 WebSocket（`GET /api/v1/admin/docker/stream`），同源检查与审计日志（动作名和 `.closed`）同终端一样。
- **Compose 项目**：Agent 从容器标签（`com.docker.compose.project`、`working_dir`、`config_files`）和 `/opt/tza-compose/<项目名>/compose.yaml` 两处找项目，按项目名解析路径，主控从不传路径。保存时先写同目录的临时文件，`docker compose config --quiet` 通过才替换，旧文件留作 `.tza-bak`；新名字在 `/opt/tza-compose/` 下建目录（0700），文件 0600。删除（`delete`）是 `down --remove-orphans`（可加 `--volumes`），成功后只删 Root 下名为项目名的那一级目录，节点上原有的项目不动文件。
- **同一个 `<dialog>`**：前端所有弹窗共用一个元素，从编辑窗直接打开输出窗时，前一个弹窗的 close 事件会晚到，`openStream` 收到时弹窗仍开着就忽略。

### 证书

`internal/certs` 在主控上（迁移 certs/1：表 `certs`、`certs_dns`、`certs_state`），`internal/agent/certs.go` 在节点上，能力名 `certs`。

- **签发**：`certs.ACME` 用 `golang.org/x/crypto/acme` 向 Let's Encrypt（`certs_state.staging` 为 1 时用测试环境）申请，只用 DNS-01。账号密钥（ECDSA P-256）第一次用时生成，按 CA 目录分别存在 `certs_state`。每个授权在 Cloudflare 加一条 `_acme-challenge.<域名>` TXT（`certs.Cloudflare`：按名字逐级往上找 zone），等这个 zone 的每台权威 DNS 都答出记录（最多 3 分钟，不问公共 DNS，免得被缓存的「不存在」拖住）再提交验证，结束后删记录。证书私钥 ECDSA P-256，按 CA 返回的完整链存 PEM。
- **续期**：`Module.Run` 每小时和每次 Kick 跑一轮 `Process`。有效期剩不到三分之一（最多提前 30 天，`Cert.RenewAt`）、改了域名或点了「立即续期」时重签；失败保留旧证书，状态记 error，按 1、3、6、12、24 小时退避（Let's Encrypt 对失败验证有次数限制）。
- **下发**：每个目标是 节点 + 目录 + 重载命令。目标记着已下发的序列号，和当前证书不同就经管理通道发 `certs.deploy`：Agent 校验目录（规范的绝对路径，不能是 /etc、/usr 这类系统目录本身，不能在 /proc、/sys、/dev 下），先原子写 `privkey.pem`（0600）再写 `fullchain.pem`（0644），然后在该目录下用 `/bin/sh -c` 跑重载命令（1 分钟超时）。重载失败时文件已写入，记错误但不反复重试。
- **API 里不出私钥**：列表不带 PEM；私钥只能从 `download?part=key` 下载，写审计日志。Cloudflare 令牌只回末四位，保存时先调 `/user/tokens/verify` 校验。

### 备份

`internal/backup` 在主控上（迁移 backup/1：表 `backup_state`、`backup_runs`），`internal/agent/backup.go` 在节点上，能力名 `backup`。

- **快照**：`backup.Snapshot` 用 `VACUUM INTO` 在线拍一份一致的数据库副本（不停服），再把数据目录里其余文件打进 tar.gz。不备份的：正在用的库文件本身（-wal、-shm）、`kernels/`（代理内核，主控需要时重新下载）、`admin-sessions`、`*.migrated` 和模块自己的 `.backup-*` 临时文件。
- **加密**：口令经 scrypt（N=2^15、r=8、p=1，盐 16 字节）派生 AES-256 密钥。主控只存密钥和盐，不存口令。文件格式是 `TZABK1` 头（参数、盐、8 字节 nonce 前缀）加上若干块：每块 4 字节长度（最高位标记最后一块），后面是最多 64 KiB 明文的 AES-GCM 密文，nonce 为前缀加块序号，「是否最后一块」也写进附加数据，所以截断或改动过的文件都解不开。解密时先解第一块，口令错立刻报错。
- **写到节点**：加密结果先写到数据目录的临时文件，算出大小和 SHA-256，再经管理通道发 `backup.write`（请求行后跟原始字节）。Agent 校验目录（和证书相同的规则）、长度和校验和，原子落盘（0600），按文件名 `tza-probe-YYYYMMDD-HHMMSS.tzabk` 只保留最新 Keep 份，目录里的其他文件不动。
- **计划**：`Module.Run` 每 5 分钟看一次，每天过了设定的 UTC 小时就跑一次；当天先记下再跑，失败等第二天或手动「立即备份」。保留最近 90 条运行记录。
- **恢复**：`tza-server backup-restore --in 文件 --data 新目录`。口令取自 `TZA_BACKUP_PASSPHRASE`，或在终端不回显地输入。只解到不存在或空的目录，解完读到最后一块再确认完整。

### 审计日志与两步验证

- **审计日志**：存在 core 的第 2 号迁移建的 `audit_log` 表里。httpapi 的 `admin` 中间件把后台每次写操作记一条：路由模式和状态码，不记请求体，因为请求体可能带着密钥。登录成功和失败也各记一条。模块用 `module.Routes.Audit` 写更详细的记录，这条会顶掉通用的那条。记录保留 365 天。
- **两步验证**：`internal/totp` 实现 RFC 6238（HMAC-SHA1、30 秒、6 位，允许前后各一个时间步的误差）。密钥单独存在数据目录的 `admin-totp` 文件里，权限 0600，和 `admin-account` 互不影响。已用过的时间步也记在这个文件里，同一个验证码不能用第二次。手机丢了可以执行 `tza-server reset-password --disable-totp` 关闭。

### 数据库迁移

- **迁移的组成**：每个模块有自己从 1 开始编号的迁移（`storage.Migration`）。执行过的迁移记在 `schema_migrations(module, version)` 表里，每条在单独的事务中执行，失败就整条回滚，下次启动重试。需要搬文件的迁移，用 `AfterCommit` 等事务提交以后再动文件。
- **起点**：
  - core 的第 1 号迁移就是 v2.0.0 的基线（`schema.sql`，原 schema 30）。
  - v2.0.0 留下的库（`user_version = 30`）启动时直接记为「core 已执行到 1」，再继续执行之后的迁移。
  - 由迁移管理的库，`user_version` 记为 31。v2.0.0 会把它当成更新的版本而拒绝打开，所以回滚必须同时还原升级前的备份。
- **拒绝打开的情况**：
  - 库里某个模块的迁移，比本二进制知道的还新
  - 早于 v2.0.0 的库
  
  只出现在库里、本二进制没有的模块不算理由，那些表只是用不上。`storage.CheckDataDir` 用同一套规则只读地检查，安装脚本在替换二进制之前就用它把关。
- **已有的模块迁移**：`regiondrift` 的第 1 号迁移在主库建 `ipguard_policies` 和 `ipguard_jobs` 两张表，并导入 v2.0.0 单独存放的 `ipguard.db`。导入完成后，旧文件改名为 `ipguard.db.migrated` 保留。

任务（`model.Task`，`Type` 取已登记的任务类型：probe / unlock / block）由各自的类型展开为单项探测（`model.Probe`），storage 按单项最近签发时间独立调度；`Probe` 也是下发给 Agent 的租约内容与观测里的任务快照。
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

SQLite schema 16 增加 `node_health` 存储连接与诊断；`nodes.last_seen` 内部仍只代表指标时间，流量差值摊分、采样与历史基线均不受心跳影响。对外 `last_seen` 合并最近联系，另给 `metrics_at`。采集失败的当前指标不对外伪装成新值；已保存的历史与累计用量保留。CPU/内存提醒在指标不可用时暂停评估，已触发的告警保留并标记待确认，未完成的持续计时重置；有效正常读数或关闭规则才解除。离线提醒继续依照连接判定。

资源提醒使用现有 `alert_state` 保存三种评估结果：有效异常、有效正常、数据不可用。`PauseResourceAlert` 只清除连续观测起点 `since`，保留 `active`，不写事件；没有状态或已经暂停的记录不产生数据库写入。当前告警查询通过 `active=1 AND since=0` 判断暂停，不修改历史事件和通知重试快照。恢复观测后从新的有效读数开始累计持续时长。
