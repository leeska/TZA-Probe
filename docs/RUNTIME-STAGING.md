# 所选节点的 runtime 审核、暂存与本机安装

`cmd/executor-runtime` 实现 M4 在节点侧的第一步：验证 CC 签名、生成节点专属计划、取得精确批准、暂存并复核制品。它是独立的本机运维工具，不进入普通 Agent 的心跳或旧 Job/Result 协议。

`plan`、`stage` 和 `verify` 的回执固定为 `state: "staged"`、`activated: false`。这表示声明及依赖字节已经核验，不表示已经安装 Python/Chromium、启动服务、通过资源预检、完成 managed adopt，或获得托管任务执行权。暂存阶段不解压、不执行制品，也不修改 Agent、IPSentinel 配置、数据库、timer 或 systemd。独立的 `install-plan/install` 和 `select-plan/select` 负责本机安装与版本选择，分别要求新的精确批准。

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

## 本机安装与版本选择

### Agent 安装通道

`runtime_install` 仅由 `tza-agent --runtime-installer` 显式启用；普通 Agent 即使读取到该配置，也不启动安装通道。安装进程不采集指标、不运行 TCP/Route/Unlock、不处理 Agent 自更新，也不访问 IPSentinel UDS。使用独立 `deploy/tza-runtime-installer.service`，沿用该节点的 Agent token；普通 `tza-agent.service` 的沙箱和行为保持原样。

专用服务以 root 运行，保留 `ProtectSystem=strict`、`ProtectHome=true`、`PrivateTmp=true` 和 `NoNewPrivileges=true`，仅开放声明的暂存、安装及 IPSentinel 状态目录写入，能力限制为 CHOWN/DAC_OVERRIDE/FOWNER。管理员须事先准备配置、公钥、固定 helper、root 所有的安装根和专用普通用户拥有的状态目录，并用 `systemd-analyze verify` 核对单元。自定义安装路径需要相应调整这份专用单元的 `ReadWritePaths`，不能通过放宽普通 Agent 的沙箱完成安装。

本机配置示例中的路径均为运维者提前审核的配置，不接受后台指定任意安装路径、脚本或 unit：

```json
{
  "runtime_install": {
    "cc_url": "https://tza.cc",
    "cc_keys_path": "/etc/tza-probe/runtime-cc-keys",
    "probe_version": "1.9.2",
    "staging_dir": "/var/lib/tza-agent-runtime/staging",
    "install_root": "/opt/ipsentinel-runtime",
    "trusted_installer": "/usr/local/libexec/tza/ipsentinel-install-runtime.py",
    "state_dir": "/var/lib/ipsentinel/state/managed",
    "config_path": "/etc/ipsentinel/runtime.toml",
    "policy_path": "/etc/ipsentinel/runtime-policy.toml",
    "unit": "ipsentinel-runtime.service",
    "target": {
      "controller_id": "<本机交接 UUID>",
      "node_id": "<本机节点 UUID>"
    }
  }
}
```

配置应保存在 root `0600` 的 `/etc/tza-probe/runtime-installer.json`，同时包含普通 Agent 同格式的 `endpoint`、`token` 和上面的 `runtime_install`。`probe_version` 必须由运维者核对实际主控版本后填写；Agent 版本直接来自当前二进制，不允许覆盖或去掉开发版后缀冒充发布版。示例版本不是对线上版本的自动探测结论。

Agent 使用本机 CC 公钥和固定平台/版本生成暂存计划，后台才可创建 `stage/install/select` 请求。节点配置、信任密钥和持久本机通道身份共同形成范围摘要；更换信任文件后需重启安装服务，旧批准失效。请求及待确认回执持久保存；中控重新验证计划摘要、目标、权限、制品摘要、安装器与配置绑定后才显示批准。`install/select` 必须关联同一节点配置范围下同版本的已完成暂存；版本选择仍要求已排空并停止的服务及本机 TOML 配置。

批准 15 分钟过期；执行前再次生成计划并取得中控 running 确认，随后在本机 fsync 执行标记，再调用安装器。完成回执先持久保存，HTTP 确认丢失只重发回执；同节点安装进程由文件锁串行处理。若进程在执行标记与完成回执之间终止，重启只上报“结果未知”，不再次执行或自动恢复旧数据库；管理员先核对本机 `current`、`selection.json` 和 journal，再生成新计划。已确认的计划及回执保留在 `agent-pending/receipts/`。损坏或权限异常的 journal 会拒绝工作，不能删除日志来假装操作未发生。

API 为 `POST /api/v1/agent/runtime`、`GET /api/v1/admin/runtime` 和 `/api/v1/admin/nodes/{id}/runtime/requests[/{request}]`。后台“节点执行器 → Runtime 安装通道”展示分阶段状态与完整批准计划。管理接口沿用管理员会话、CSRF/same-origin 保护；Agent token 不能创建或批准自己的请求。SQLite schema 22 保存节点配置范围和安装请求。通道回执不等于服务健康、adopt 或托管执行授权；这些仍是后续独立步骤。

2026-11-06 已使用真实 IPSentinel 候选在测试 CC 上验证 Agent 的计划生成、批准后下载及暂存复核，不经生产 CC。管理员认证、跨站写入拒绝、完整计划校验、重复回执与配置范围变化均有集成测试；浏览器在本地临时管理员账户中检查空状态、计划展开、批准操作和 390px 无横向溢出，展开完整 JSON 时弹窗 client/scroll 均为 356px。`scripts/runtime-ui-test.mjs` 覆盖批准目标索引、转义、状态过期、未知历史和关联暂存，并纳入流水线。

Free Debian 13 的隔离 systemd 单元使用与专用安装服务相同的只读系统、有限写路径、能力、私有 tmp/设备和无新权限约束，完成真实 `install`、`select` 的 HTTP→Agent→固定 helper 联调。两步终态 HTTP 回执分别故意丢失，重试只上传已落盘回执；实际 helper 共执行两次。安装后无 current；独立选择批准后生成 current 和一致性备份，服务保持 inactive、`activated=false`。未在该测试中启动浏览器、adopt 或修改现有 Agent；浏览器执行证据仍见 2026-11-05 记录。

Free2（`23.246.165.231`，Debian 13 amd64/systemd 257）以 `nobody` 运行通道、执行前撤销、掉线回执、重启标记、私有 journal 和文件锁测试，全部通过。随后只读调用固定参考安装器的宿主校验，当前候选因 `/usr/lib/x86_64-linux-gnu/libXcomposite.so.1.0.0` 摘要不匹配而被拒绝；没有替换系统库、安装候选或跳过校验。Free2 没有运行中的 TZA Agent，测试后保持该状态。

两台机器的临时程序、安装根、状态和 unit 均已清理。Free 原 Agent 与观测 timer 保持 active；证据位于本机 `/tmp/tza-runtime-channel-20261106.rQ9L2M/evidence/free/` 和 `evidence/free2/`，浏览器截图位于同一 QA 根目录。该结果不代表正式 CC 发布或 bero/netcup 部署。

本轮 Probe 全量测试、竞态测试（含真实 Python 协议联调）与 `go vet ./...` 通过；完整 `pipeline.sh check` 通过，包含新增 runtime UI 回归。角色包生成器的可选自检因本机缺少 Pillow/numpy 跳过，与安装通道无关。CC 在临时 PostgreSQL 17.11 上启用真实候选后，检查脚本及竞态测试通过。流水线记录的 `sha=c646fd8` 是执行时 HEAD，不代表本轮工作区改动已提交。临时数据库、浏览器与本地 Server 均已停止。

IPSentinel 已交付 `0.4.0` 真实发行候选、固定 `bin/ipsentinel-control` 入口及参考安装器。首版仅支持 Debian 13 amd64，要求精确宿主库及软件包版本匹配；`linux-amd64` 不表示支持所有 Linux 发行版。候选经测试 CC 签名不等于正式发布。

TZA 不执行制品内的安装脚本。管理员需独立部署 IPSentinel 的 `scripts/install_runtime.py` 到 `/usr/local/libexec/tza/ipsentinel-install-runtime.py`，固定 SHA-256 为 `8df4220b5a6ada13e9c4f238849a4dc691fc3e1170b2645784d5c0bfdcd96640`。helper、系统 Python 及其祖先目录必须 root 所有、无其他用户写权限；helper 路径不允许符号链接。`--trusted-installer` 可指定另一条满足相同条件且字节完全一致的本机路径，不能批准任意脚本。

下面命令在所选 Linux 节点本机以 root 执行，占位摘要和路径须替换为实际审核值：

```bash
/private/qa/executor-runtime install-plan \
  --store /private/runtime-staging \
  --stage-plan-sha256 <已暂存的真实计划摘要> \
  --cc-keys /private/config/cc-keys --binding /private/config/binding.json \
  --probe-version 1.9.2 --agent-version 1.9.2 \
  --install-root /opt/ipsentinel-runtime > /private/qa/install-review.json
```

审核输出中的 `approval_template` 后，另存为批准文件并明确改为 `approved: true`。安装批准必须包含 `action: "install"`、`format_version: 1` 和本次 `plan_sha256`。保持所有参数相同，将子命令换为 `install` 并增加 `--approval /private/qa/install-approval.json`。安装成功仅表示 `state: "installed", selected: false, activated: false`，不会创建 `current` 或启动服务。

版本选择需要已存在的独立状态目录、本机审核的 TOML 配置/策略，以及已停止的 IPSentinel systemd 服务：

```bash
/private/qa/executor-runtime select-plan \
  --store /private/runtime-staging \
  --stage-plan-sha256 <已暂存的真实计划摘要> \
  --cc-keys /private/config/cc-keys --binding /private/config/binding.json \
  --probe-version 1.9.2 --agent-version 1.9.2 \
  --install-root /opt/ipsentinel-runtime \
  --state-dir /var/lib/ipsentinel/state/managed \
  --runtime-config /etc/ipsentinel/runtime.toml \
  --runtime-policy /etc/ipsentinel/runtime-policy.toml \
  --unit ipsentinel-runtime.service > /private/qa/select-review.json
```

策略的 `browser_path` 必须为该安装根下的 `current/browsers`，状态目录必须与配置一致。审核后使用独立的 `action: "select"` 批准，将子命令换为 `select`，其余参数保持相同。选择回执为 `state: "selected", selected: true, activated: false`，附带一致性备份路径。

每次操作都会重新验证暂存及本机信任密钥。批准还绑定 helper 摘要、安装根、当前链接和 `selection.json` 摘要；选择额外绑定 state/config/policy/unit 及配置和策略字节。改变任一输入需重新计划和批准。helper 在安装锁内重新检查链接、选择记录及配置摘要，随后执行固定的 install/select 函数。

选择前必须停止派发、drain 并停止服务。参考安装器检查活动任务和数据库兼容范围，保存 SQLite 一致性快照及配置/策略备份后再切换。回滚同样是选择已安装的兼容旧摘要，不覆盖数据库、不丢弃未 ACK journal。留下 `selection.json: prepared` 的中断需要管理员按 IPSentinel 文档显式本机 recover；TZA 不自动抢锁或恢复旧数据库。

服务启动/启用、本机 adopt 和创建任务仍为独立步骤。普通服务无法读取 root `0600` 配置/策略，应提供服务用户拥有的 `0600` 副本，实际服务使用的字节必须与批准一致。当前策略校验拒绝任何组/其他用户权限；实机 systemd credentials 为 `0440`，不能直接作为策略文件传入。只有通过实际 hello/status、非 root UDS peer 和 Chromium/资源预检后，才可另行授权中控绑定与派发。

## 验证边界

基础测试使用临时 Ed25519 密钥和不可执行文本制品，覆盖节点/版本/权限改变、错误签名、大小与摘要不符、撤销信任密钥、回执篡改、重复暂存及并发写入。安装操作测试需设置 `TZA_IPSENTINEL_INSTALLER` 才会核对固定的上游 helper，并验证操作批准相互隔离、输入变更失效和异常激活拒绝：

```bash
TZA_IPSENTINEL_INSTALLER=/absolute/ipsentinel/scripts/install_runtime.py \
  go test -race ./internal/executorruntime ./cmd/executor-runtime
```

CC 的 `TestRealRuntimeCandidateReviewAndNodeStaging` 通过 `TZA_RUNTIME_CANDIDATE_DIR` 选择真实候选，在临时 PostgreSQL 与测试 CC 上复核五个实际制品、审核签名、精确版本下载和节点暂存。签名密钥仅用于测试，不能加入生产信任列表。

`TestInstalledLinuxRuntimeThroughTZAAgent` 由 `TZA_EXECUTOR_LIVE_SOCKET` 和 `TZA_EXECUTOR_LIVE_INPUT` 显式启用，连接隔离安装的 managed 服务；通过临时 TZA SQLite 控制器和真实 HTTP→Agent→UDS 运行单次 Chromium observe，并验证终态、资源/清理、ACK 丢失重试、同 job 重投和预算去重。输入必须包含独立 `target` 和本机批准的单服务 `config`，该测试会实际访问配置选定的官方观测站点。它不代表生产 CC 发布或生产节点验收。

### 2026-11-05 实机记录

使用 IPSentinel HEAD `00f4700` 的 `0.4.0` 候选，声明 ZIP SHA-256 为 `3b2baac8e43dcf120972f9880f6af7f959e7c694f1db5c88f4f0fc8696978795`。在 Free Debian 13 amd64 测试机使用独立安装根、状态和临时 unit，TZA 工具完成以下流程：

- 安装回执为 `installed / selected=false / activated=false`，此时没有 `current` 链接。
- 独立选择批准后为 `selected / activated=false`，生成备份；之后另行本机 adopt 和启动服务。
- 执行器以 `ipsentinel` UID 988 运行，Agent HTTP/UDS 测试以 `nobody` UID 65534 运行，仅额外授予测试 Socket 的组访问，不修改既有 Agent 或用户组。
- 真实 Cloudflare observe 完成，run ID 为 `ffbcdf4c-89ab-4a2a-a65e-56d01280def2`；worker 用时 18.395 秒，cgroup 峰值 `460185600` 字节，`hard_limits_verified=true`、`oom_kill_count=0`、`cleanup=complete`。
- 丢失中控持久回执时没有提前 ACK，重试后 ACK 到连续 seq 9；同 logical job 再次入队仍返回原 run，每日运行预算只记 1 次，原生终态投影到 TZA 命令。

网络统计为 `host_interface`，`network_isolation_verified=false`；本次未故意触发 OOM，也未验收跨版本升级、自动回滚或生产安装。停止临时服务后确认其 cgroup 已移除，再移除临时 unit、安装根和状态；原 `tza-agent.service` 与独立观测 timer 均保持 active。操作计划、批准、收据及测试日志已下载保存到本机 `/tmp/tza-install-live-20261105.Nl1fbu/evidence/`。bero 的 CC 和 netcup 的 Probe 未变更。

最终本机复核启用 `TZA_IPSENTINEL_ROOT` 和 `TZA_IPSENTINEL_INSTALLER`，Probe 全量和竞态测试均通过（657 项、26 个包），`go vet ./...` 通过；其中 Python 托管/排期测试仍使用 admission-only worker，与上面的真实 Linux 浏览器验收分开。CC 在临时 PostgreSQL 17.11 上启用真实候选测试，`scripts/check.sh` 和 `go test -race ./...` 均通过（竞态测试 73 项、8 个包）。临时 PostgreSQL 已停止。
