# TZA Probe

一个主机与网络探针：**一个仓库、Server / Agent 两个程序、一个 SQLite 数据库**。后台与公开页随 Server 嵌入发布，没有 Node 构建依赖。

当前版本 `v1.0.0`，首个公开版本。

## 功能

- **主机监控**：CPU / 内存 / 根挂载点磁盘 / 网络速率 / 网卡累计 / 在线状态，30 天分钟级历史。
- **网络探测**：按节点显式绑定的 IPv4 / IPv6 TCP 延迟与三网回程；回程标签按实际经过的骨干命名（163 / CN2GIA / CN2GT / CTG / 4837 / 9929 / 10099 / CMIN2 / CMI）。
- **解锁检测**：节点用自己的出口检测 24 个流媒体 / AI / 交易所 / 常用服务，给出 `19/24` 式分数与明细。
- **计费与提醒**：计费周期、流量额度、价格与到期时间、达量降速；离线 / 续费 / 流量 / 降速 / 负载提醒，经 Telegram 或 Webhook 发送。
- **公开面板**：卡片与列表两种视图，浅色 / 深色主题与多套外观风格；搜索认中文、全拼、拼音首字母、英文与代号（香港 = `xianggang` / `xg` / `hk`）。
- **管理后台**：节点管理与拖动排序、密钥轮换、任务目录检索与批量绑定、用量校正、告警事件与投递状态。
- **MCP 接口** `/mcp`：AI 客户端用后台生成的 MCP API Key 读写整站，能力与管理面板一致。

## 一键部署

Linux 主机（amd64 / arm64），一条命令装成 systemd 服务：

```sh
curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh
```

脚本拉取 `main` 源码构建（需要 Go 1.27 与 git），安装 `/usr/local/bin/tza-server` 与 systemd 单元，建好 `/var/lib/tza-probe` 并放入 amd64 / arm64 两个 Agent。重复执行即升级：旧二进制保留为 `tza-server.previous`，服务自动重启并核对运行的是新版本。可选参数：`--version vX.Y.Z`（改用 GitHub Release，免 Go 与 git）、`--listen ADDR`、`--data DIR`、`--no-systemd`。

装完脚本会打印后台用户名、密码（密码只在首次安装时显示一次，登录后可在“面板设置 → 后台账号”修改；忘记密码执行 `sudo tza-server reset-password --data /var/lib/tza-probe`）。访问 `http://127.0.0.1:25774/admin` 用用户名密码登录。MCP API Key 在“面板设置”中生成。跨主机接入需在前面配置 HTTPS 反代，见 [部署说明](docs/DEPLOYMENT.md)。

Docker 部署（镜像本地构建，数据放命名卷）：

```sh
git clone --depth 1 https://github.com/leeska/TZA-Probe && cd TZA-Probe
docker compose -f deploy/docker-compose.yml up -d --build
```

后台初始用户名与密码只在首次启动的容器日志里出现一次（`docker compose -f deploy/docker-compose.yml logs tza-server`）。

被监控主机不需要装 Go：后台新增节点后，“接入”弹窗给出的 `curl ... | sh -s -- --endpoint ... --token ...` 命令会下载并启动 Agent。

## 本地运行

需要 Go 1.27，不需要 CGO、外部数据库或 Node。

```sh
go build -o bin/tza-server ./cmd/server
go build -o bin/tza-agent ./cmd/agent
./bin/tza-server --listen 127.0.0.1:25774 --data ./data
```

首次启动的日志会给出后台用户名 `admin` 与随机初始密码（只显示这一次；也可先执行 `./bin/tza-server init --data ./data` 在终端拿到）。访问 `http://127.0.0.1:25774/admin` 用它登录；忘记密码执行 `./bin/tza-server reset-password --data ./data`。也可设置 `TZA_ADMIN_TOKEN`（至少 32 个字符），密钥不写入日志。接入配置里的地址必须能从 Agent 主机访问，远程机器不能用 `127.0.0.1` 或 `localhost`。也可以下载 `agent.json` 手动运行：

```sh
chmod 600 agent.json
./bin/tza-agent --config agent.json
```

```json
{
  "endpoint": "https://probe.example.com",
  "token": "后台生成的64位节点密钥",
  "interfaces": ["eth0"],
  "mount": "/",
  "nexttrace": "",
  "geo_provider": "",
  "trace_method": "",
  "allow_http": false
}
```

- `interfaces` 为空时汇总所有非 loopback 网卡；有桥接、容器、隧道的机器应填公网网卡，避免重复计数。
- `mount` 指定统计的文件系统。
- `nexttrace` 留空用内置追踪核心，填绝对路径则改调外部 NextTrace（仅作运维兜底）。
- `trace_method` 是回程探测方式：`icmp`（默认）、`tcp` 或 `udp`。
- `geo_provider` 是跳 ASN 数据源，默认 `IPAPI.com`；`disable-geoip` 时只靠本地前缀表。上游默认的 `NextTrace-API` 在常驻进程里查不到结果，不作默认。

正式支持 Linux amd64 / arm64，macOS 可用于开发验证。

## 三网探测

后台建任务，从内置目录按地区、运营商、IP 协议检索并多选目标（也可填自定义目标，海外选“其他”运营商），再明确绑定节点；空绑定不执行。一个任务可同时勾选延迟与回程，每组目标可单独选 IPv4 / IPv6。

- **频率**：TCP 最短 30 秒；回程最短 15 分钟、默认 1 小时。单个节点每次最多领 8 个 TCP、1 个回程和 1 个解锁任务，各自并行执行，长任务不会卡住延迟探测。
- **TCP 延迟**是连接耗时与连接失败比例，**不是 ICMP 丢包率**。卡片每个运营商一格，下方 20 个小格各代表一次探测，失败画空心红格，无数据画灰格。色带时间范围在后台“面板设置 → 延迟色带”里调。
- **回程**由 Agent 内置的 NTrace-core（NextTrace 的追踪核心）在进程内执行，固定 30 跳、单次测量、45 秒预算，不生成分享地图。标签 ASN 优先、注册库核对过的运营商网段兜底，表外地址保持“未知”，不编造线路；判定规则见 `internal/model/route.go`。卡片只显示节点“卡片代表地区”的结果，详情展示全部；跳地址公开前脱敏。
- **IP 协议可用性**以内核真有该族路由为准：IPv4 只交给本机代理或隧道、没有 IPv4 默认路由的机器不会上报 IPv4，对应任务也不派发。
- **Fake-IP / 代理**：DNS 返回 `198.18.0.0/15` 时 Agent 拒绝把它当目标。开着代理的机器测出来的是代理路径，**不能用来验收回程**。
- **目标目录**是 186 个公网 IP 快照，不代表可达，部署后应在实际节点验证；维护时运行 `scripts/refresh-catalog.py`，Server / Agent 不自动拉取。

内置追踪核心 NTrace-core 为 GPL-3.0，Agent 二进制的分发条款见 [NOTICE](NOTICE)。

## 解锁检测

建一个类型为 `unlock` 的任务并绑定节点（无目标、无端口，最短 1 小时一轮）。Agent 用这台机器自己的出口逐项发起 HTTPS 检测，全部在 Go 标准库里完成，不起 shell、不需要额外安装。IP 协议留空时走 IPv4，纯 IPv6 机器由 Server 改派 IPv6。

- 结论五种：已解锁 / 仅自制剧 / IP 被封禁 / 未解锁 / 检测失败。分数 = 已解锁 + 仅自制剧，分母固定 24。**检测失败不是结论**，连不上、超时、页面异常都记失败，不会写成未解锁。
- Bing / Apple / Google Play / Steam / OneTrust 只给地区或币种，不参与解锁判断。
- 结果反映的是这台机器的实际出口：开着代理或分流时，答案是那条出口的。
- 改任务、删任务、删节点都会连带清掉旧结论。接口见 [HTTP 协议](docs/API.md)。

## 计费与提醒

- 周期按 UTC 时刻逐月重置、精确到秒，当月不足该日时取月末。额度按十进制字节，统计方式可选双向合计、取较大方向、仅下载、仅上传。
- 流量是 Agent 上报的计数器差值：首次上报建立基线；主机重启或网卡集合变化时重建基线，不重复计费。重启前未上报的部分无法恢复，因此它是运维估算，不能替代服务商账单。可手动校正本周期用量，下周期自动失效。
- 达量降速支持两种口径：近 N 小时平均带宽超过阈值，或周期额度用满后限速。
- 提醒规则逐条设置，每条作用于全部节点、某几个分组或指定节点；同一台节点同一种提醒只生效一条，指定节点 > 分组 > 全部节点，作用于全部节点的规则会自动套到新加的节点上（离线提醒对还没接入过的新节点先等 15 分钟，留出装 Agent 的时间）。
- 负载超过阈值并持续设定时间才触发；离线可额外等待；可选不少于 5 分钟的重复提醒。
- 同一时间使用一个通知渠道（Telegram 或 Webhook）。Webhook 接收 `{ "version": 1, "event": { ... } }`，2xx 为成功；失败指数退避、最多 10 次，接收端按 `event.id` 去重。投递是至少一次语义；未配置渠道时事件照常记录，配置后只补发 24 小时内的。
- Telegram 推送按状态、节点、实际读数、触发条件与时间分段，解除事件区分指标正常、节点离线和规则移除；详见 [通知模板说明](docs/NOTIFICATIONS.md)。
- 保留期固定：主机历史 30 天、网络结果 7 天、流量账本 400 天、告警事件 90 天。

## MCP 接口

Server 在 `POST /mcp` 提供 [Model Context Protocol](https://modelcontextprotocol.io) 端点，用“面板设置 → MCP API Key”生成的独立 Key（`Authorization: Bearer <API Key>`）鉴权。Key 只在生成时显示一次，可重新生成或撤销，旧 Key 立即失效；管理密钥和面板 Cookie 无法访问 MCP。工具覆盖节点、任务、提醒规则及通知和面板设置，读写与面板走同一套校验；已存的 Bot Token / Webhook 不回显。**写工具立即生效、删除不可撤销**，接入前请确认客户端会在执行前征求你的同意。

```json
{ "mcpServers": { "tza-probe": { "type": "http", "url": "https://probe.example.com/mcp", "headers": { "Authorization": "Bearer MCP_API_KEY" } } } }
```

共 25 个工具，包含查询和修改节点、任务、提醒规则、通知及面板设置。`update_*` 只修改提交的字段。完整的 Key 管理、客户端配置、工具清单、curl 自检和排错步骤见 [MCP 接入指南](docs/MCP.md)；协议字段见 [HTTP 协议](docs/API.md)。

## 升级顺序

以后升级时**先升 Server，再升节点 Agent。** Agent 在上报里声明能力（`capabilities`），Server 只把对方能执行的任务派给它；反过来新 Agent 报给旧 Server 会因为不认识的能力字段被拒（HTTP 400），节点显示离线。

## 部署与维护

- [部署说明](docs/DEPLOYMENT.md)：手工安装、反代、升级、备份与恢复。
- [MCP 接入指南](docs/MCP.md)：API Key、客户端配置、工具能力与连接排错。
- [主题包](docs/THEMES.md)：自己做一套外观风格（zip 上传，只含样式表与图片），格式、颜色变量与示例。
- [组件边界](docs/ARCHITECTURE.md)、[HTTP 协议](docs/API.md)、[项目范围](docs/SCOPE.md)。
- [验证方法](docs/VERIFICATION.md)：怎样跑检查，以及哪些行为只能在真实环境验证。

检查与构建用自带的流水线（不依赖 GitHub Actions），Linux 与 macOS 都能跑。需要 Go 1.27；`check` 的前端部分另需 Node ≥ 20.19 或 Bun（没有 Node 时自动用 Bun）：

```sh
bash scripts/pipeline.sh check   # gofmt / go vet / go test（有 gcc 时加 -race）/ web/*.js 语法 / 前端集成检查
bash scripts/pipeline.sh build   # dist/tza-probe-linux-{amd64,arm64}.tar.gz(+.sha256)
bash scripts/pipeline.sh all     # 默认：check 后 build
GITHUB_TOKEN=... bash scripts/release.sh vX.Y.Z dist   # 打 tag、建 Release、上传发行包
```

`UI_SMOKE=0` 跳过前端集成检查，`OUT=<目录>` 换产物目录，`TZA_NODE` / `TZA_NPM` 指定 Node 与 npm，`TZA_BUN` 指定 Bun。

本项目不做插件市场、远程终端、任意命令、文件管理、多数据库和自动更新。

## License

MIT。内置追踪核心 NTrace-core 为 GPL-3.0，见 [NOTICE](NOTICE)。目标目录的数据来源见 `internal/catalog/targets.json` 中的 `source`。
