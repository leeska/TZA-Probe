# 2026-09-18 本地验收

当前状态：独立重建开发首版，非生产发布。原仓库基线见 `source-snapshot.json`，重建分支（已合并删除）。

## 已验证

- 20 个 Go 测试通过 `go test -race ./...`，无 race 报告。
- `go vet ./...` 与 `git diff --check` 通过。
- 所有前端模块以 ESM 模式解析通过，并实际加载完整模块依赖图；无需前端构建即可嵌入。
- DOM 集成测试通过真实 HTTP 服务验证：登录、创建/编辑节点、保存计费与独立告警选项、一次性 Agent 配置、目标目录填表、指定节点绑定、告警页面、退出登录、公开卡片/搜索/IPv6 切换。
- 本机 macOS arm64 真实 Agent 完成 CPU / 内存 / 磁盘 / 网卡 / 运行时间上报，周期账本累积。
- 本地 TCP 任务 3 次连接全部成功，结果通过租约上传并可查询。
- 重启 Server 和 Agent 后恢复上报，节点、计费和历史仍保存。
- Linux amd64 / arm64 的 Server 与 Agent 均在 `CGO_ENABLED=0` 下交叉编译成功。
- Agent 依赖图不含 SQLite、storage、httpapi 或 web。
- 鉴权用例覆盖私有节点不可见、Agent 不能访问管理接口、错误和轮换令牌、跨源写入与安全会话 Cookie。
- 存储用例覆盖显式空绑定、节点越权上传、重复租约、任务编辑失效、计数器重置、月末/闰年计费边界、数据留存、旧库拒绝。
- 通知用例覆盖续费/流量/离线独立开关、负载持续时间、触发去重、解除事件、真实本地 Webhook 发送与禁止重定向。
- 路由解析测试覆盖 ASN 证据、未知结果、IPv4 / IPv6 地址脱敏与输出内存上限。

## 本轮新增验收与修复

- Docker Desktop 已启动；镜像构建成功，Server 在 Linux arm64 容器以 UID/GID 65532 运行。容器重建后，挂载卷中的节点、任务和令牌保留。后台 DOM 集成流程在容器服务上通过。
- Docker 基础镜像已锁定本次实际构建的多架构镜像摘要；一次浮动标签重新解析因 registry 网络超时失败，固定摘要后重建成功。
- 官方 NextTrace v1.7.3 Linux arm64 下载资产 SHA-256 与 GitHub release 提供的 digest 一致：`4fbf436e2d4737e4a491e71ce3cd140a7a268d43ec94fb9ac9497aec7eda080e`。
- 在隔离 Linux 容器中以专用测试 Agent 执行 IPv4/IPv6 loopback 回程，两者都完成 Server 下发、NextTrace 执行、路径脱敏和结果回传。目标没有运营商 ASN，标签正确显示“未知”。
- 对上海电信/联通/移动 IPv4 及电信 IPv6 进行有限次真实探测，发现旧别名 NXDOMAIN 和本地代理 Fake-IP DNS 干扰。修正目标后 IPv4 有到达结果但无有效运营商 ASN；容器外部 IPv6 未成功。这不构成真实三网线路识别成功的证据。
- 从源站当前 TSV 更新为 186 个明确公网 IP 目标，校验地址族、端口和唯一性；不再将失效域名或本地合成 DNS 地址放入目录。保留原始域名仅作为来源记录。IP 格式有效不代表目标可达。
- Agent 检测 `198.18.0.0/15` 合成目标并拒绝生成虚假成功结果；保留明确的 DNS、权限、工具与超时原因代码，不上传原始错误文本。
- NextTrace 调用显式设置 `-M` 关闭完整路径分享地图。ASN 查询仍依赖工具的数据源。
- 该轮的外部二进制方案后续被内置 NTrace-core 取代，见文末“2026-09-21 内置 NTrace-core”。
- 主机启动时间和网卡集合组成计数器来源标识；来源变化自动重建基线，避免把新网卡的累计量误记到当前周期。新增回归测试通过。
- 历史图按实际时间定位，缺失时间段不连线；图表回归测试通过。详情弹窗和时间范围切换忽略已过期请求，避免旧响应覆盖新页面。
- 详情权限检查改为按节点主键查询，避免逐个节点请求导致全量节点/账本查询。

## 仍需外部环境验收

- 桌面/手机布局尚未完成最终浏览器视觉验收；DOM 测试不验证 CSS 排版。
- 当前开发机使用代理/Fake-IP 网络，无法据此验收真实运营商 ASN 路径。仍需在具备直接网络出口的 Linux 节点验证三网 IPv4/IPv6、ASN 数据源可用性及商用线路的实际表现。
- systemd 样例与大规模节点长期资源消耗尚未实机验证。容器内 Agent 指标仅用于验证 Linux 采集链路，不作为宿主机监控的部署方案。
- 当前通知出口为通用 Webhook；没有直接内置 Telegram / 邮件适配器。
- 尚未推送分支、发布 release 或更新生产服务。

## 复现 DOM 集成检查

仅针对独立 loopback 数据目录，脚本会创建并删除自己创建的测试节点和任务。不要指向生产数据。

```sh
npm install --prefix /tmp/tza-ui-deps --no-audit --no-fund jsdom@26.1.0 esbuild@0.25.10
go build -o bin/tza-server ./cmd/server
./bin/tza-server --listen 127.0.0.1:25874 --data /tmp/tza-ui-data
# 另一个终端：
TZA_UI_DEPS=/tmp/tza-ui-deps \
TZA_TEST_URL=http://127.0.0.1:25874 \
TZA_TEST_ADMIN_FILE=/tmp/tza-ui-data/admin-token \
node scripts/ui-smoke.mjs
```

这些 Node 依赖只用于 DOM 验收，生产构建与运行不依赖它们。

## 当前留存预览

- 公开页：http://127.0.0.1:25874/
- 后台：http://127.0.0.1:25874/admin
- 独立数据：`/tmp/tza-v2-rebuild-preview`
- 管理密钥文件：`/tmp/tza-v2-rebuild-preview/admin-token`（不写入仓库或日志）
- 本地预览节点是这台开发机的真实指标；未配置外部网络探测。
- Server 与 Agent 当前由本次开发会话启动，非开机服务；临时目录可能被系统清理。

## 2026-09-18 组合网络任务验收

- 22 个 Go 测试通过，含 race 检查；go vet 通过。
- 新增组合任务回归：两种检测 × 两种 IP 协议独立租约、四项结果不覆盖、TCP 单独到期、有效租约不重复下发、清空节点后不再执行。
- 新增模型回归：海外“其他”运营商可用；IP 地址族不匹配、重复检测类型、过短回程周期被拒绝。
- DOM + 实际 HTTP Server 联调通过：地区搜索、筛选结果批量选择、搜索不丢已选、双栈双检测保存、编辑回填、自定义海外双栈目标保存。
- 所有浏览器 JS 的 ESM 语法检查和图表测试通过。
- 本次表单检查为 DOM 模拟，不等同于真实浏览器的视觉验收。

## 2026-09-18 液态玻璃与 Telegram 验收

- `web/glass.css` 为公共页、后台、弹窗、卡片和控件提供统一的半透明、背景模糊、边缘高光与低饱和渐变层；支持无透明度和减少动态效果偏好。
- Telegram Sender 使用固定 `api.telegram.org`、5 秒超时、禁止重定向，Token 不进入错误信息。
- Telegram 配置格式、成功/恢复文本、HTTP 错误和 Token 脱敏测试通过。
- UI DOM + 实际本地 Server 联调通过；视觉测试仍需在真实浏览器中检查。

## 2026-09-19 真实浏览器视觉验收

- 新增 `scripts/visual-check.mjs`：用 Playwright + Chromium 对公开页（桌面 1440 / 手机 390）、在线节点详情弹窗、后台登录、节点、网络探测、告警页和手机后台截图，并把页面脚本错误视为失败。使用方式同 DOM 检查，额外需要 `playwright` 依赖；`TZA_CHROME` 可复用本机已有的 Chromium。
- 在 3 个节点（1 个本机真实 Agent 在线、2 个未接入）的独立数据目录上完成截图，无页面脚本错误。液态玻璃层、概览四格、卡片指标条、速率块、三网回程格、到期与价格行、详情图表在桌面和手机宽度下均正常排版。
- 修复截图发现的问题：网络探测页头部按钮组误用表格行内 `.row-actions` 类导致“创建任务”失去主按钮样式；节点表状态列在未接入时留下悬空的 `·` 分隔符；手机宽度下 hero 的“最近同步”卡改为整行横排。
- 修复后 DOM 集成检查、图表测试和 `go test -race ./...` 全部通过。
- 未覆盖：暗色模式、`prefers-reduced-transparency`、有真实三网探测结果时的路线格与路径展示（开发机无直接出口），以及手机宽度下后台表格需横向滚动才能看到操作列。

## 2026-09-19 亮暗主题切换

- 原先深色样式只跟随 `prefers-color-scheme`。现改为由 `<html data-theme>` 驱动：`web/theme-boot.js` 在样式表前同步执行，按 `localStorage` 中的 `tza-theme`（`light` / `dark`，缺省跟随系统）设置属性，避免首屏闪烁；`web/theme.js` 提供“跟随系统 → 浅色 → 深色”三态按钮，并在跟随系统时响应系统切换。按钮位于公开页顶栏、后台侧栏底部和登录页。
- 引导脚本必须是外部文件：Server 的 CSP 为 `script-src 'self'`，内联脚本会被拦截（真实浏览器验收时已实际触发并据此修正）。
- `glass.css` 的 12 个深色媒体查询块全部改写为 `[data-theme="dark"]` 规则；无 `backdrop-filter` 和 `prefers-reduced-transparency` 的不透明回退补充了深色取值，避免深色页面上出现浅色面板。
- `scripts/visual-check.mjs` 新增深色截图（公开页、登录、后台、详情）并断言：预设深色生效、按钮两次点击后切到浅色、刷新后保持。12 张截图均无页面脚本错误；DOM 集成检查、图表测试与 `go test -race ./...` 通过。

## 2026-09-19 手机宽度后台

- 节点、任务、事件三张后台表和任务执行记录表加 `class="stack"`，每个单元格带 `data-label`。≤760px 时表头隐藏，每行堆叠为“标签 : 值”卡片：标签占首列并跨行，文本、`small`、徽标、操作链接自动落入第二列逐行排列，不再需要横向滚动即可看到操作列。桌面宽度下样式不变。
- 手机宽度下后台侧栏由约 360px 的纵向块压缩为约 150px：品牌一行、三个导航按钮等宽横排、底部链接与主题按钮横排，隐藏 WORKSPACE 标题和版本小字。
- `scripts/visual-check.mjs` 新增手机宽度任务页截图，并断言所有 `.table-scroll` 在 390px 宽度下 `scrollWidth` 不超过 `clientWidth`。13 张截图通过；DOM 集成检查与图表测试通过。
- 修正过程记录：单元格最初用 flex 布局，`td small`（`display:block`）变成横排的 flex 项把主机名和副标题挤在一行；改为 grid 后解决，并用 `justify-self: start` 防止徽标被拉伸到整行。

## 2026-09-19 减少透明度回退

- `scripts/visual-check.mjs` 通过 CDP `Emulation.setEmulatedMedia` 模拟 `prefers-reduced-transparency: reduce`，浅色与深色主题各截一张公开页，并断言 `matchMedia` 命中且节点卡片的 `backdrop-filter` 计算值为 `none`。
- 两种主题下头部、概览格、工具条、节点卡片均退化为不透明面板，文字对比度不依赖背景模糊；卡片内部的次级半透明块位于不透明面板之上，不影响可读性。15 张截图通过。
- 本轮临时环境的 loopback TCP 任务已有结果，卡片显示 `TCP 延迟 0 ms · 连接失败率 0.0%`，验证了公开卡片的 TCP 汇总渲染路径。

## 2026-09-19 真实告警事件排版

- 在临时环境配置指向不可达端口的 Webhook，为演示节点开启流量（额度 100 MB、50%）、负载（持续 30 秒）和续费（7 天）提醒；引擎依次产生 3 条触发事件，关闭负载提醒后产生 1 条解除事件，每条都带 `webhook connection failed` 投递错误并按退避重试。
- 桌面告警页：类型、触发 / 解除徽标、内容、`待发送 / 重试 · N 次尝试 · 错误文本` 三行通知列均正常；手机宽度堆叠后同样可读。`scripts/visual-check.mjs` 新增手机告警页截图，16 张通过。
- 顺带确认：管理接口 `PUT /admin/nodes/{id}` 拒绝未知字段，回传的只读字段（`online`、`metrics`、`traffic_used` 等）须先剔除；负载提醒持续时间下限 30 秒。

## 2026-09-20 后台弹窗视觉验收

- `scripts/visual-check.mjs` 新增 8 张弹窗截图：节点编辑、密钥重置确认、新建任务、编辑任务、执行记录、批量绑定、通知渠道配置、手机宽度新建任务。脚本只打开弹窗截图后按 Esc 关闭，不提交任何表单，也不会真的重置密钥。24 张截图通过，DOM 集成检查通过。
- 修复：手机宽度下任务编辑器的目标目录退化为单列，220px 滚动区只露出不到两个目标；现改为搜索整行 + 地区/运营商并排、目录两列、滚动区 320px，一屏可见 7 个目标。规则须放在 `glass.css` 的手机媒体块——它在 `styles.css` 之后加载，同优先级的基础规则会覆盖 `styles.css` 里的媒体查询。
- 修复：执行记录弹窗的“刷新状态”和批量绑定弹窗的“选中筛选结果 / 清空选择”是无类名的裸按钮，渲染为紧挨着的普通文字；统一使用任务编辑器已有的 `.catalog-toolbar` 容器。


## 2026-09-20 任务编辑器排版整理

- “检测内容”原本是两行松散的复选框，看不出“TCP 延迟 / 三网回程”与“IPv4 / IPv6”是两组不同含义的选项。现拆成带小标题的两组（检测类型、IP 协议），每个选项是等高的可点击方块，选中态高亮；执行节点列表沿用同一套方块样式，四节视觉一致。
- 目录卡片（`.catalog-option`）是一个 `<label>`，继承了全局 `label { flex-direction: column }`，勾选框一直浮在文字上方居中。补上 `flex-direction: row` 后改为勾选框在左、地区 / 运营商在右，卡片更矮，同样高度的滚动区多显示一行。
- 表单末尾的任务名称与“启用任务”原本没有小节标题、复选框悬在输入框下方，现归入“4. 任务设置”并排显示；最后一节不再画分隔线。
- 手机宽度：选项组与任务设置改单列，节点列表单列（避免节点名折行），滚动区 220px。
- 登录表单补充隐藏的 `autocomplete="username"` 字段（固定值 `admin`），让密码管理器能把管理密钥识别为完整的登录项；页面上没有任何按钮会提交登录表单——后台所有操作按钮都在 `<form>` 之外或标了 `type="button"`。
- 亮 / 暗、桌面 / 手机各截一份确认；`visual-check.mjs` 24 张截图、DOM 集成、图表检查与 Go 测试全部通过。

## 2026-09-20 Agent 一键安装

- 新增 `internal/agentdist`：Server 公开提供 `/install.sh`（静态 POSIX sh，不含任何密钥）与 `/dl/tza-agent-linux-{amd64,arm64}[.sha256]`（支持 Range 续传，写超时放宽到 10 分钟）。二进制优先取数据目录 `agents/`，其次取构建时嵌入的副本；两处都没有时两个路由返回 404，启动日志有提示。后台“接入”弹窗改为直接给出 `curl -fsSL <server>/install.sh | sh -s -- --endpoint <server> --token <密钥>`，agent.json 折叠到“手动安装”里。
- 端到端：本机起 `0.0.0.0:25975` 的 Server，在 Docker 容器里用 systemctl 桩验证。Alpine（wget 路径）暴露了 `/etc/systemd/system` 目录不存在时脚本失败，已补 `install -d`；Debian bookworm（curl 路径）从 `curl | sh` 到后台节点显示在线（`2.0.0-dev arm64 linux`）全程通过。同一容器内再验证：不带 `--token` 重跑为升级并保留 `.previous`；`--interfaces ' eth0 , eth1'` 正确去空格写成 JSON 数组；非 64 位十六进制密钥被拒绝。
- 单元测试：目录副本优先于嵌入副本、校验和、Range 请求、非法架构名与路径穿越返回 404、脚本与仓库副本逐字节一致且不含 64 位十六进制串。测试用 `noEmbed` 开关屏蔽本机可能存在的嵌入二进制，CI 上无嵌入副本时同样通过。
- CI 发布任务改为先交叉编译两种架构的 Agent 放入 `internal/agentdist/bin` 再构建 Server，发布包里的 Server 自带一键安装能力。
- 视觉：接入弹窗桌面 / 深色 / 手机各一张，命令框与“复制命令”按钮并排，手机上纵向堆叠；`--token` 不再被 `break-all` 从中间截断。DOM 集成检查新增断言：弹窗里的命令必须精确等于 `curl -fsSL <base>/install.sh | sh -s -- --endpoint <base> --token <密钥>`。
- 补充：命令不再带 `sudo`。脚本非 root 时若有 sudo，会用 `sudo env TZA_TOKEN=… sh -c "curl … | sh -s -- …"` 重新拉取执行（管道执行时正文已被读走，无法原地提权）；参数从解析结果重建，`--interfaces`/`--nexttrace`（含单引号路径）原样传递。Debian 容器验证 root 直接执行、普通用户自动 sudo、普通用户无 sudo 明确报错、普通用户 sudo 升级四条路径。第一版在这里有两个 bug——`$@` 已被 `shift` 消费、`sed` 替换串少一层转义——都是容器里真跑才发现的。

## 2026-09-20 提醒阈值的表单死锁

- 现象：某些节点打开“编辑”后，提醒规则的阈值全是 0，点保存被浏览器原生校验拦住（“值必须大于或等于 1”），这个节点再也保存不了。
- 原因：服务端只在规则**启用**时要求有效阈值，未启用时允许 0；而表单对 `traffic_percent` / `cpu_percent` / `memory_percent` / `hold_seconds` 无条件加了 `min="1"` / `min="30"`。通过 API 创建的节点（alerts 为 Go 零值）落到这个缝隙里，UI 新建的节点因为带默认值不受影响——已用 Playwright 分别复现两条路径确认。
- 修复：未启用的规则回填默认阈值而非 0；规则未勾选时禁用其阈值输入（与任务编辑器 `interval` 的既有做法一致），既跳过原生校验也表明该值当前不生效；提交时从元素而非 FormData 读数，取消勾选不会清掉已填的阈值。禁用输入整体变淡。
- 验证：全 0 节点可保存 → 勾上流量/负载后阈值可编辑且服务端接受 → 再取消勾选，手填的 77 仍然保留。DOM 集成检查新增断言（阈值不得低于自身 min、禁用与勾选状态联动），已确认去掉修复后该断言失败。
- 补充：节点编辑“基本信息”里的“在公开页面展示”是网格中唯一的裸复选框，被网格拉成 59px 高后内容居中，比同排输入框中心高 12px，文字也是灰的。改为与任务编辑器同一套方块样式，并 `align-self: end` + 36px 高 + 10px 圆角与输入框严格对齐（量测：两者同为 356–392）。选中时高亮为强调色，规则写成 `.form-grid > label.checkbox`，不影响 `.rule-row` 内的复选框。亮/暗/手机各截一张确认。

## 2026-09-20 卡片延迟按运营商分格

- 卡片原来只有一行聚合的“TCP 延迟 55 ms / 连接失败率”，看不出是哪家运营商、哪个地区。改成与三网回程同构的格子：电信 / 联通 / 移动 / 其他四格，复用 `.route-label` 样式，`.latency-grid` 为四列。
- 地区来源：节点新增 `latency_region`（后台“卡片延迟地区”），留空沿用 `representative`，两者都空则不限地区并在标题右侧标“全部地区”——这样旧节点不会因为没设地区而丢失原有的延迟展示。
- 分桶规则：`电信 / 联通 / 移动` 按任务运营商精确匹配，其余（海外自定义目标、没有运营商的旧任务）归入“其他”。每格取该地区同运营商全部目标的平均延迟；有观测但无成功结果显示“超时”，无对应任务显示“未检测”，悬停提示给出最近检测时间与成功次数。
- 原“连接失败率”单独一行被格子取代：失败在格子里表现为“超时”，精确的成功/发送次数进了悬停提示，详情弹窗的按目标失败率图表不变。
- 亚毫秒延迟原来会显示成 `0 ms`，改为小于 10 ms 时保留一位小数。
- 验证：在演示任务上补出上海的电信/联通/移动/其他四个目标，等 Agent 真实探测后四格都出数（0.1–0.3 ms）；把该节点的延迟地区改成“广州”（无任务）后四格变“未检测”，而三网回程仍按代表地区“上海”显示——确认两个地区设置相互独立。亮/暗/手机各截一张。DOM 集成检查新增断言：延迟地区可保存并回填、卡片延迟格恰为四个且含四个运营商名。

## 2026-09-20 每组目标单独选择 IP 协议

- 原来 IPv4 / IPv6 是全任务一个开关，93 组目标只能同进同退。数据模型本来就是每个 `Target` 自带 `family`，限制只在编辑器里，因此没有改动服务端和存储。
- 改法：`selected` 由 `Map(key → Target[])` 变成 `Map(key → {all, families})`，`all` 是该组可用地址、`families` 是本任务要测的协议。已选目标的小标签从单个按钮变成「名称 + IPv4/IPv6 开关 + 移除」，目录里没有对应地址的协议按钮置灰，每组至少保留一种（否则提示并拒绝关闭）。上方的 IP 协议改为批量开关：勾选变化时套用到全部已选，之后仍可逐组微调；新加入的组按它取默认值，若该组没有对应地址则退回它自己有的协议，避免加进来却什么都不测。
- 提交校验相应放宽：不再要求“已勾选的全局协议必须有对应地址”，改为「至少一组目标」+「至少一个地址」。
- 验证（Playwright 真实浏览器）：批量选中四川三组默认双栈 → 电信关掉 IPv6、移动关掉 IPv4 → 再关电信的 IPv4 被拒绝并提示 → 保存后服务端存的正是 `电信:[ipv4] 联通:[ipv4,ipv6] 移动:[ipv6]` → 重新打开编辑三组开关原样回填。亮/暗/手机各截一张。
- DOM 集成检查同步更新：新增每组开关的断言（默认跟随全局、单组关闭不影响其他组、存盘并重开后保持），目标数量断言由 6/8 改为 5/7。

## 2026-09-21 内置 NTrace-core（吸纳 NextTrace）

- 回程不再依赖运维安装的外部二进制：`github.com/nxtrace/NTrace-core v1.7.4` 作为 Go 依赖链接进 `tza-agent`，`internal/probe/embedded.go` 在进程内调用 `trace.TracerouteWithContext`，固定 TCP、24 跳、单次测量、1500 毫秒超时、`DisableMPLS`，并额外加 45 秒预算；`nexttrace` 配置项保留为外部二进制兜底。
- 移植方式与约束：ASN 数据源通过 `geo_provider` 选择（默认与上游一致 `NextTrace-API`，可 `IPAPI.com`、`disable-geoip`）；`trace.Hops` 按 TTL 归并、地址照旧脱敏为 /16 或前两组十六进制，ASN 走 `model.NormalizeASN`；到达判定同时接受上游 `StopReasonDestination` 和“跳地址等于目标地址”，后者沿用原 `--raw` 解析器的判据。
- 许可证影响：NTrace-core 是 GPL-3.0，本仓库其余部分是 MIT。`tza-agent` 因链接该库按 GPL-3.0 分发，新增 `NOTICE` 并在 `LICENSE` 末尾标注；`tza-server` 不链接该库。
- 工具链：`go.mod` 提升到 `go 1.27.1`，Dockerfile 基础镜像换为 `golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414`（digest 取自 registry 的 `1.27-alpine` 索引）。
- 验证：`CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 与 `arm64` 交叉编译通过（pcap/cgo 只在 darwin 分支需要，Linux 走原始套接字）；`go test -race ./...`、`go vet ./...`、`gofmt -l .` 通过；新增 `embedded_test.go` 覆盖跳数映射、地址脱敏、ASN 归一、到达判定与失败原因（EPERM→permission、deadline→timeout、空结果→tool_failed）。
- **已撤回的结论**：开发机开着代理的 Fake-IP，本机对 `1.1.1.1:443` 的进程内追踪只走了本机代理栈，拿到的 `1.1.*.*`、`AS13335`、0.358 ms 是代理转发结果，不是公网回程路径。该结果不作为三网线路识别的证据，本文档不再引用。
- 有效验证仅限不依赖公网路径的部分：`embedded_test.go` 的跳数映射、地址脱敏、ASN 归一、到达判定与失败原因用例；`probe.Runner{Embedded: …}` 驱动 Server 下发→Agent 执行→结果上传的完整链路（结果内容只用于确认管线可用，不用于判定线路）。
- 未覆盖：本机没有 Docker daemon，容器镜像未重新构建；本机存在 Fake-IP，**任何在本机发起的回程探测都不构成有效验收**。内置追踪器的真实行为（跳数、ASN、GIA/GT 判定、45 秒预算）必须在具备直连出口的真实节点上复验。另：本机沙箱网络下上游追踪器不会自行提前停止，因此 45 秒预算与“跳地址等于目标地址”的到达判定是本轮的实际兜底，同样需要在真实节点确认。

### 同日复核：撤掉代理后的真实直连数据

开发机代理与 Fake-IP 已关闭后复测（`DNS 返回 104.16.x`、无代理进程、系统代理关闭、默认路由走 `en0`），本轮拿到的是真数据：

- 新增 `TestEmbeddedLiveTrace`（`TZA_LIVE_TRACE_TARGET` 显式开启，需要 root）。用法：
  `sudo TZA_LIVE_TRACE_TARGET=223.5.5.5 TZA_LIVE_TRACE_GEO=IPAPI.com go test ./internal/probe/ -run LiveTrace -v`。
- 直接证据：进程内追踪到达 `223.5.5.5:443`，跳地址 `192.168.*.*`（本机内网）与 `183.221.*.*`，ASN 实测为 `AS9808`，RTT 22–39 ms，`Received=1`；把该跳喂给标签逻辑得到 `CMI`，与 AS9808 的运营商一致。目标选 `1.1.1.1`、`8.8.8.8` 时该线路直接 filtered（`nc` 验证），结果是 `timeout` + `未知`，符合“不编造线路”。
- **探测方式必须换成 ICMP**：旧实现沿用了外部二进制的 `-T`（TCP），在这条线路上中间跳全部为空；ICMP 能拿到真实中间跳，UDP 一个都不返回。NextTrace CLI 不带 `-T`/`-U` 时默认也是 ICMP，所以内置实现默认改为 ICMP，并新增 `trace_method` 配置（`icmp`/`tcp`/`udp`）。同时把并发从 2 提到 6，否则 24 跳在 45 秒预算内跑不完。
- 数据源表现：`IPAPI.com` 能返回 `AS37963`/`AS9808`（偶发 2 秒超时）；上游默认 `NextTrace-API` 在本机没有返回任何 ASN（它需要 PoW 通道）；`disable-geoip` 时空 ASN 由本地前缀表兜底，不影响判断流程。三条路径都不编造数据。
- 仍然受限：本机出口是移动线路（第一跳就 22–39 ms，跳间波动大），TTL-exceeded 被大量过滤，多数中间跳仍是 `*`，因此**这张网络不能用来判定三网回程标签是否正确**。要确认 `163`/`CN2GIA`/`CN2GT`/`9929` 等标签，仍需在用户那台四川电信之类的生产节点上跑同一条命令。

### 同日修复：Linux 上从 TTL 0 探测（由部署方报出）

部署方在 Linux 上复跑 `TestEmbeddedTracesLoopback` 失败：
`send error (ttl=0, attempt=0): setsockopt: invalid argument` / `Reason: timeout`。
根因是两个 bug，都已修复：

- `trace.Config` 没有设置 `BeginHop`，库默认 0，于是第一个探测包带着 TTL 0 发出。TTL 0 不是有效跳数，Linux 直接以 `setsockopt: invalid argument` 拒绝并让整次追踪失败；macOS 容忍了这个值，所以本地没暴露。现固定 `BeginHop: 1`（与 NextTrace CLI 的 `-f 1` 默认一致）。
- 把 `res.Hops` 的**切片下标当成了 TTL**。库内部按 `ttl-1` 存（`updateHop` 用 `k := ttl - 1`，打印器传的也是下标），因此第一个跳会被丢弃、其余跳号整体错位一格，`StopReason.Hop`（存的是 TTL）也按下标去查。现统一 `TTL = index + 1`，`destinationHop` 用 `ttl - 1` 取下标；上一版为绕过服务端“TTL 必须 ≥ 1”而加的“跳过 ttl<1”是在掩盖症状，已删除。
- 回归：`TestEmbeddedTracesLoopback` 在 macOS 由 12 秒失败边缘变为 **0.3 秒通过**；新增 `TestMapTraceResultTreatsTargetHopAsArrival` 钉住“目标跳即到达”与跳号语义，`TestMapTraceResultKeepsEvidenceAndMasksAddresses` 改为按真实布局（下标 0 = TTL 1）构造。
- **仍未在 Linux 上验证**：本次尝试用 `debian:bookworm-slim` + 挂载的 Go 工具链离线编译来复跑，但开发机的 Clash（网络扩展 HakoMacExtension、DNS `198.18.0.2`、Fake-IP）在会话中途重新启用，容器内下载依赖被中间人证书拦截，`docker pull` 也超时，因此没有取得 Linux 结论。Linux 复验由部署方在目标机上执行。

## 2026-09-21 卡片延迟格改成"每格一次探测"

- 需求澄清：格子的语义是**每次探测的结果**，不是"整条进度条表示延迟高低"。原来的实现是单值量级条（按聚合延迟点亮前 N 格再统一上色），语义不对。
- 数据侧新增 `GET /api/v1/latency-samples?limit=N`（默认 20，上限 60），用窗口函数按 `节点/运营商/协议/地区` 分组各取最近 N 次已完成的 TCP 结果，返回逐次的 `at/latency/sent/received`；只返回调用者本来就可见的节点（公开节点或管理员），不引入新的可见性。回程任务不进入该接口。
- 前端：每行 20 格，最右是最新一次、左侧更早，样本不足时左边留空；每格按那次结果落到 20 档色带（延迟以 200 ms 为满量程，丢包以 20% 为满量程），失败的那次用空心红格 `.fail`，与"无数据"的灰格 `.idle` 区分；每格 `title` 是那次的时间与数值。
- 顺带修掉一个会让整条色带失效的坑：页面 CSP 是 `style-src 'self'`，行内 `style` 属性会被浏览器忽略（属性留在 DOM 里但 `el.style` 为空、计算值仍是样式表的基础值）。色带因此改成 `styles.css` 里的 `.quality-bar.q1…q20`，由类名着色，`scripts/ui-smoke.mjs` 增加断言禁止再退回行内样式。
- 验证：新增 `verify8.mjs`，用接口返回的原始结果独立算出 60 个格子应有的颜色，再与真实 Chrome 里 DOM 的类名逐一比对（延迟/丢包各 3 个运营商 × 20 格），并断言每格带自己的时间、样本不足时左侧留空、失败格为 `.fail`；`go test -race ./...`、`go vet ./...`、`git diff --check`、`ui-smoke.mjs` 均通过。存储层新增 `TestRecentLatencySamplesKeepsPerProbeResults`（分组上限、失败探测保留、回程任务不入表、时间升序）。
