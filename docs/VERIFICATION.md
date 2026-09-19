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
- 本��临时环境的 loopback TCP 任务已有结果，卡片显示 `TCP 延迟 0 ms · 连接失败率 0.0%`，验证了公开卡片的 TCP 汇总渲染路径。

