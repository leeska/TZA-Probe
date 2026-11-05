# 所选节点的 runtime 审核与暂存

`cmd/executor-runtime` 实现 M4 在节点侧的第一步：验证 CC 签名、生成节点专属计划、取得精确批准、暂存并复核制品。它是独立的本机运维工具，不进入普通 Agent 的心跳或旧 Job/Result 协议。

当前命令只有 `plan`、`stage` 和 `verify`。回执固定为 `state: "staged"`、`activated: false`。这表示声明及依赖字节已经核验，不表示已经安装 Python/Chromium、启动服务、通过资源预检、完成 managed adopt，或获得托管任务执行权。它不解压、不执行制品，也不修改 Agent、IPSentinel 配置、数据库、timer 或 systemd。

## 输入与信任

运维者可先通过已有的 CC 官方制品分发接口取得文件后从本机暂存，也可用显式 `--cc-url` 让本工具通过 CC 的精确版本与摘要接口下载。后者只信任本机固定公钥，不保存下载票据，不接受非同源 URL或重定向，也不发送 CC 会话、Cookie 或实例 token。无论选择哪种方式，CC 发布签名和全部制品摘要都要本机复验。

- `archive.zip`：仅包含 `executor.json` 的声明包，最多 20 MiB。
- `signature.json`：CC 现有 `tzapkg.Signature` 格式，最多 4 KiB；未知字段和重复字段拒绝。
- `cc-keys`：本机事先固定的 CC Ed25519 公钥，一行一个 `key-id:base64`；不从包或网络自动获取信任密钥。重复 key ID 拒绝。
- `binding.json`：本机已配置的 `controller_id`、`node_id` UUID，与 IPSentinel 交接身份一致。这里不是 TZA 的旧节点 ID，不会自动生成或替换已有绑定。
- `artifacts/`：该架构需要的原始制品，文件名固定为 `<sha256>.bin`。只接受普通目录内的有界普通文件，不跟随制品目录或制品文件符号链接。

工具以实际编译运行平台选择 `linux-amd64` 或 `linux-arm64`，没有远程下发或命令行覆盖架构的开关。`--probe-version` 和 `--agent-version` 是本机运维者核对后填写的版本声明，必须满足签名清单；工具不会据此声称已探测线上版本。开发版后缀不作为正式发布版本接受。

计划包括完整包摘要、descriptor 的 RFC 8785 摘要、签名 key ID、节点身份、实际平台、版本声明、兼容矩阵、全部权限及该架构的 runtime/依赖大小与摘要。整个计划的 JCS SHA-256 是批准对象。更换节点、版本、权限、签名密钥或兼容声明，均不能沿用旧批准。

## 本机流程

在选择的 Linux 测试节点运行，使用独立的私有目录。公钥、绑定和制品必须事先核对。以下占位路径和版本需要按本机现状填写，不是生产安装指令。

```bash
go build -o /private/qa/executor-runtime ./cmd/executor-runtime

/private/qa/executor-runtime plan \
  --archive /private/incoming/archive.zip \
  --signature /private/incoming/signature.json \
  --cc-keys /private/config/cc-keys \
  --binding /private/config/binding.json \
  --probe-version 1.9.2 \
  --agent-version 1.9.2 \
  > /private/qa/review.json
```

`plan` 不写暂存目录、不读取依赖文件，不检查运行能力。输出 `plan`、`plan_sha256` 和默认 `approved: false` 的 `approval_template`。管理员审核所选节点、来源、全部权限、版本和字节预算后，将模板单独保存为批准文件，并明确把 `approved` 改为 `true`：

```json
{
  "format_version": 1,
  "plan_sha256": "<review.json 中的真实 64 位摘要>",
  "approved": true
}
```

这份文件只批准暂存；不是中控托管授权、OS 权限授予或激活许可。

也可以让工具直接从配置的 CC 下载声明与依赖；该模式仅使用本地公钥和已有公开下载票据，不带 CC 管理员会话、Cookie 或实例 token。CC 声明下载票据限定为该仓库发布的精确 package/version 路径，拒绝重定向及外站 URL；依赖按签名清单的摘要路径下载。未批准前不下载依赖字节：

```bash
/private/qa/executor-runtime plan \
  --cc-url https://tza.cc \
  --release-version <已审核的精确版本> \
  --cc-keys /private/config/cc-keys \
  --binding /private/config/binding.json \
  --probe-version 1.9.2 \
  --agent-version 1.9.2 \
  > /private/qa/review.json

/private/qa/executor-runtime stage \
  --cc-url https://tza.cc \
  --release-version <同一个精确版本> \
  --cc-keys /private/config/cc-keys \
  --binding /private/config/binding.json \
  --probe-version 1.9.2 \
  --agent-version 1.9.2 \
  --approval /private/qa/approval.json \
  --store /private/runtime-staging
```

HTTP 仅为 literal loopback 测试地址开放；任意非 loopback CC 必须使用 HTTPS。请求禁用自动重定向和压缩、限制 metadata/headers/响应体/超时，并且不附加授权头。CC 托管仍须通过现有审核和发布；本机 plan 不是替代 CC 发布审批。

```bash
/private/qa/executor-runtime stage \
  --archive /private/incoming/archive.zip \
  --signature /private/incoming/signature.json \
  --cc-keys /private/config/cc-keys \
  --binding /private/config/binding.json \
  --probe-version 1.9.2 \
  --agent-version 1.9.2 \
  --approval /private/qa/approval.json \
  --artifacts /private/incoming/artifacts \
  --store /private/runtime-staging
```

`stage` 在检查批准后才创建暂存目录。目录必须为私有普通目录（无组/其他用户权限），新目录为 `0700`。每个对象流式重算精确大小及 SHA-256，在独立临时目录全部通过后原子公布到 `staged/<plan_sha256>/`。制品和元数据为 `0400`，没有执行位。相同计划用独立目录锁串行处理；失败不公布完成回执，正常返回时同步文件和目录。

重复 `stage` 会重新检查现存声明、签名、批准、回执和全部制品，不用“目录存在”代替成功。独立复核使用当前本机信任密钥，移除旧密钥后不能继续验证该版本：

```bash
/private/qa/executor-runtime verify \
  --plan-sha256 <真实计划摘要> \
  --store /private/runtime-staging \
  --cc-keys /private/config/cc-keys \
  --binding /private/config/binding.json \
  --probe-version 1.9.2 \
  --agent-version 1.9.2
```

每次状态查询都从已存归档重新验签、重建计划、核对绑定并重算制品摘要。修改回执为 `active`、换节点或缺失文件均报错。不会静默修复被改写的已公布目录。

进程被强制终止或机器掉电时，可能保留 `locks/<plan_sha256>/` 或 `pending/<随机目录>/`；旧的完成目录不会因此被替换。先确认没有同一计划的活动进程，再由本机运维者恢复锁和临时目录；工具不按超时擅自夺锁。暂存目录需要本机磁盘配额和运维清理，不会删除 IPSentinel 历史或 Profile。

## 激活前仍需交付

截至 2026-11-05，IPSentinel 源码版本为 `0.4.0.dev2`，现有 CLI 入口为 `ipsentinel`。CC 声明要求的正式 `ipsentinel-control` 制品入口、平台依赖归档布局及固定安装/升级/回滚动作仍须由运行时发行方交付并共同验收。不得把开发版改名成正式 `0.4.0`，或把不可执行 fixture 当作可安装运行时。

后续安装阶段需要真实官方制品、运行时本机固定安装入口和逐版本数据库兼容规则；再接入中控授权记录、Agent 类型化安装通道和安装状态页面。升级还要停止新派发和本机准入、drain、保存未 ACK journal、做一致性备份、递增 epoch，并在数据库不兼容时拒绝直接启动旧版本。当前暂存回执不能跨过这些步骤。

测试使用临时 Ed25519 密钥和不可执行文本制品，覆盖节点/版本/权限改变、错误签名、大小与摘要不符、撤销信任密钥、回执篡改、重复暂存及并发写入。这些测试不访问外站、不启动 Chromium，不证明 Linux cgroup、非 root peer、OOM 或生产接入已经验收。

```bash
go test -race ./internal/executorruntime ./cmd/executor-runtime
```

2026-11-05 验证：Probe 全量 `go test ./...` 和 `go test -race ./...` 均通过（639 项、26 个包），`go vet ./...` 通过。CC 临时 PostgreSQL 17.11 集成测试会启动 Probe runtime 客户端，走已审核签名 ticket、精确版本 archive 下载、摘要路径依赖下载、受批准暂存及本机回执复核；测试服务、密钥和依赖均为本地 fixture。最新暂存及 CC 客户端代码也编译成 Linux amd64 测试程序，在既有 MeowBox 测试机的独立临时目录以 `nobody` 身份通过，使用本地 fixture endpoint，不访问生产 CC；测试前后原 `tza-agent.service` 均为 active。没有真实官方 runtime 制品，没有进行 runtime 激活或浏览器验收。
