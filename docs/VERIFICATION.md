# 验证方法

这份文档写两件事：怎样验证一次修改，以及哪些行为只能在真实环境里验证。

## 每次修改

```sh
gofmt -l .                 # 必须无输出
go vet ./...
go test -race ./...        # 没有 gcc 的机器去掉 -race
```

`bash scripts/pipeline.sh check` 会一次跑完以上三项，外加 `web/*.js` 语法检查、`scripts/presentation-test.mjs` 和一次性 Server 上的前端集成检查（需要 Node ≥ 20.19 或 Bun；没有 Node 时自动用 Bun，jsdom 与 esbuild 也由它安装）。

前端检查同时运行 `scripts/market-permissions-test.mjs`，覆盖插件快捷更新确认、权限变化提示、取消不安装、历史版本独立权限，以及已绑定实例空清单同步。真实 CC 数据库上的下载表单提交、回执幂等和实例清单替换由 TZA-CC 的 `TestDownloadFormReceiptsAndInventory` 覆盖，需配置独立的 `TZA_CC_TEST_DATABASE_URL`。

## 前端集成检查（ui-smoke）

只针对独立 loopback 数据目录。脚本会创建并删除自己的测试节点和任务，**不要指向生产数据**。

```sh
npm install --prefix /tmp/tza-ui-deps --no-audit --no-fund jsdom@26.1.0 esbuild@0.25.10
go build -o bin/tza-server ./cmd/server
mkdir -p /tmp/tza-ui-data
./bin/tza-server init --data /tmp/tza-ui-data > /tmp/tza-ui-credentials
./bin/tza-server --listen 127.0.0.1:25874 --data /tmp/tza-ui-data --cc-url off
# 官方主题包（复古印刷 / 现代极简 / 二次元）打成 zip，ui-smoke 会把它们装到这台一次性 Server 上：
go run ./tools/themepack -out /tmp/tza-ui-themes themes/*
# 另一个终端：
TZA_UI_DEPS=/tmp/tza-ui-deps \
TZA_TEST_URL=http://127.0.0.1:25874 \
TZA_TEST_ADMIN_FILE=/tmp/tza-ui-data/admin-token \
TZA_TEST_CREDENTIALS_FILE=/tmp/tza-ui-credentials \
TZA_TEST_THEMES=/tmp/tza-ui-themes \
node scripts/ui-smoke.mjs
```

用 Bun 时把安装换成在依赖目录里 `bun add jsdom@26.1.0 esbuild@0.25.10`（目录里先放一个只含 `{}` 的 package.json），把 `node` 换成 `bun`。这些依赖只用于验收，生产构建与运行不依赖它们。

## 需要真实浏览器的检查

jsdom 不做布局，排版类问题只有真浏览器量得出来。用法见各脚本开头的注释。

| 脚本 | 检查内容 |
|---|---|
| `scripts/frontend-browser-test.mjs` | 独立前台渲染、公开数据隔离、模块分块、深链接、移动端、后台分离与故障恢复，见 [FRONTENDS.md](FRONTENDS.md) |
| `scripts/visual-check.mjs` | 公开页、详情弹窗、后台的桌面与手机截图 |
| `scripts/card-overflow-check.mjs` | 卡片与列表里的文字不越过卡片右边框 |
| `scripts/route-nowrap-check.mjs` | 回程格居中且一行读完（三套皮肤） |
| `scripts/layout-webkit.mjs` | WebKit 下整页高度不随刷新抖动 |
| `scripts/unlock-acceptance.sh` | 解锁检测按 Agent 路径完整走一遍：上报、落库、计分 |

## 只能在真实环境验证的

- **三网回程标签**：必须在有直连出口的 Linux 节点上测。开着代理或 Fake-IP 的机器测到的是代理路径，开发机的结果不能作为证据。
- **ASN 数据源可用性**：`IPAPI.com` 等第三方数据源的可用率随时间变化，需要在节点上观察。
- **解锁检测结论**：取决于节点出口，与其他检测脚本对照时须用同一协议族。
- **长期资源消耗**：大规模节点下 Server 的 SQLite 体积与 1 核机上的查询耗时。

## Agent 诊断回归

`internal/agent/heartbeat_test.go` 验证采集失败/超时时心跳独立发送、无伪造指标、错误分类不泄露原始路径、上报恢复，以及旧 Server 的 404/405 降级。storage 回归确认心跳不改变采样与计费基线、报告恢复、首次接入和节点清理；HTTP 回归验证节点令牌隔离与状态校验。`ui-smoke.mjs` 通过真实 Server 发送诊断与恢复上报，确认卡片即时显示异常并清除旧诊断；布局需另用真实浏览器验证桌面与手机宽度。

## 告警评估暂停回归

`internal/notify/evaluation_test.go` 覆盖 CPU / 内存在离线、采集失败、采集超时、上报失败时保留当前告警、不产生伪恢复或新重复提醒，以及 Server 重启、有效高/低读数恢复、持续计时重置和规则删除。storage 检查暂停状态只写一次，HTTP 检查当前状态字段与不可变历史隔离。流水线中的 `scripts/alert-evaluation-test.mjs` 使用真实页面模块验证「待确认」提示、历史与恢复状态和转义；仍需在一次性 Server 上用内置浏览器检查实际布局。

## 审阅修复回归

源码版本解析由 `cmd/server` 的版本测试覆盖精确 tag、开发分支、无 Git 的源码目录及安装旧 tag 的兼容路径。`scripts/theme-addons-test.mjs` 验证自定义设置主题仍可浏览、安装、更新和删除附加包；与 `scripts/market-permissions-test.mjs` 使用相同的临时前端依赖。

Agent 的状态序号测试覆盖故障心跳在恢复报告之后到达、报告尚未应答时的心跳，以及服务端重启和 Agent 新会话。CC 的 `TestConcurrentFirstRegistration` 需配置独立测试数据库，跨两个连接池并发注册，验证仅一位首次管理员及关闭注册时的行为。
