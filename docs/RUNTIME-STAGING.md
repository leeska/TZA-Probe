# 所选节点的 runtime 审核、暂存与本机安装

> 当前插件安装通道已合入 Agent，见 [普通插件管理](PLUGIN-MANAGEMENT.md)。
> 下述早期独立服务的验收记录保留作为历史证据，不表示当前还需补装助手。

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

### 升级排空与启动交接

升级仍逐步批准：`stage` → `install` → `drain` → `select` → `activate` → 重新授权 → 单独恢复。回退也是对保留的兼容版本重新生成这些计划，不恢复旧数据库或 ACK 游标。首次 managed adopt、普通用户和服务定义的部署仍是本机运维步骤，`activate` 只接受已有托管实例的排空检查点。

创建 `drain` 请求会立即在中控持久建立维护暂停，阻止新任务、配置应用和恢复命令，取消未派发任务；本机暂停和停止服务仍须管理员批准精确计划。普通状态同步、journal 落库与 ACK、原任务查询和取消继续工作。撤销、过期、配置变更或 `unknown` 不解除维护暂停。已经租出的配置/恢复命令不能被假定撤回；本机执行前和排空期间会再次核对身份、配置及暂停状态，检测变化后拒绝继续。

`drain` 使用原实例/epoch 的 `control.pause`，会触发 IPSentinel 原有取消语义；它不是让活动浏览任务无限等待自然结束。最多等待两分钟，只有本机 accepted/running/cancelling 全部消失且 journal `acked_seq == latest_seq` 后才停止专用服务，确认 inactive 并持久保存 root 私有的 `drained.json`。未 ACK 结果或未知状态不允许切换。独立的参考 `select` 仍负责 control-server/node 锁、数据库 schema 兼容性和一致性备份。

`activate` 精确绑定已选发行摘要、配置/策略摘要、systemd unit 与 drop-in 定义摘要、非 root UDS UID/GID、旧实例/epoch/descriptor/config 以及排空检查点和 journal 水位。启动前重新验证已安装文件清单、宿主库和版本；执行期间持有与参考安装器相同的 `.install.lock`。不执行制品脚本，不启用服务自启动，不执行 adopt。

启动后同时检查 systemd active 与新鲜 hello：版本精确匹配，实例与配置不变，数据库版本和 journal 水位不倒退，原 ACK 后无缺口，运行能力与 hard limits 可用，本机仍暂停且没有活动任务。runtime/descriptor 改变必须递增 epoch。暂停状态的 hello 允许 `degraded`，因此成功回执明确为 `started_paused`，不能显示成恢复或可派发；只有独立恢复后，本机 `health=ready` 才允许新准入。启动失败或检查超时会尝试停止该专用服务，并保留维护暂停；停止本身失败或进程中断仍为 `unknown`，必须在节点核对，不自动重试、选回旧版或覆盖数据库。

管理员从启动回执核对目标、实例、epoch 和 descriptor，明确重新授权后才解除维护锁；派发仍保持暂停。普通 Agent 重新同步有效 hello，再按当前暂停 generation 单独恢复。重绑会停用旧日程；管理员需要重新审查并启用日程。旧 epoch、过期/其他配置范围的启动回执不能解除维护锁。

专用安装配置可增加：

```json
{
  "peer": {
    "socket_path": "/run/ipsentinel/control.sock",
    "uid": 1001,
    "gid": 1001
  }
}
```

UID/GID 必须是本机实际授权的普通 Agent 身份，并有 Socket 路径访问权限；不能直接照抄示例数值。安装程序固定在 root 所有且其他用户不可写的 `/usr/local/libexec/tza/tza-runtime-installer`。root 安装器以这个身份启动该受限可执行文件的 `--runtime-peer` 子进程，清除附加组，只允许 hello 与 fenced pause。子进程不读取中控凭据、安装配置或签名密钥。为此专用安装 unit 需要 `CAP_SETUID` / `CAP_SETGID`；普通 Agent 不增加 root 能力。不配置 peer 时原有 stage/install/select 仍可用，drain/activate 会拒绝生成计划。

保留 `PrivateDevices`、`RestrictAddressFamilies`、`RestrictSUIDSGID` 和 `RestrictRealtime`。Free2 的实际 systemd 沙箱中，仅配置 capability bounding set 时 `CAP_SETUID` 会从有效集合消失；为专用 root 安装器增加 `AmbientCapabilities=CAP_SETUID CAP_SETGID CAP_KILL`，使启动受限 peer 前可以降权，并能在超时或退出时终止自己创建的降权子进程。`CAP_KILL` 不用于通用进程控制；它是 Linux capability，并不由内核限制为只能向自己的子进程发信号。切换到非 root UID 后上述能力必须清空，peer 入口会拒绝有效/许可/ambient capability 非零或仍持有附加组的进程。普通 Agent 和浏览器 worker 不增加这些能力。安装二进制和父目录需 root 所有、普通 peer 可遍历执行（目录/二进制 0755），不能将父目录设为 0700。

进程中断后仍复用持久执行标记与回执：没有已提交的终态回执就报告 `unknown`，不会再启动/停止一次服务。管理员应核对服务、`current`、`selection.json`、本机 journal 和数据库，再决定新的显式请求；不要删除执行标记来让旧请求重新执行。

#### 2026-11-06 升级编排验证

前一阶段本地 Go 全量、竞态测试和 `go vet ./...` 通过，完整 `pipeline.sh check` 通过，含 runtime UI 回归；该阶段流水线 SHA `2fdc830` 是运行时 HEAD，不代表后续改动已包含在该提交。测试覆盖持久维护暂停、撤销后仍暂停、配置/身份变化、活动任务或未 ACK journal 阻止停止、启动失败、旧 epoch、实例更换、journal 回退、缺少隔离能力、启动后意外恢复及检查点篡改。HTTP/存储通道验证完整计划、幂等回执、明确新 epoch 授权以及绑定后仍暂停；新的运行时操作不能复用更早的启动验收来解除维护。Agent 补充启动终态回执丢失只重传、重启执行标记转 unknown 而不再次启动服务的测试。`stage/install/select` 旧通道回归保留。

Free2 在与最终专用安装服务一致的 NNP、capability bounding/ambient、只读系统、私有 tmp/设备、受限地址族、SUID/实时限制与有限写路径中通过目标测试。真实编译 Agent 子进程以 UID/GID 65534 且无附加组连接本机测试 UDS；内核 SO_PEERCRED 确认 UID，root peer 被拒绝，入口检查有效/许可/ambient capability 为零。临时 IPSentinel 名称的 sleep 服务实际完成定义核对、启动/active、停止/inactive；`systemd-analyze verify` 通过。状态机的发行、hello 与执行能力仍是受控 fixture，不是 Free2 上真实 Chromium 或候选升级验收；该机与当前候选宿主库不匹配，真实候选改在兼容的 Debian 13 Free 上验证。

本地真实后台使用上述测试创建的一致 SQLite 快照核对维护提示、通道过期禁用、启动回执预填新 epoch/descriptor、保存重新授权后清除维护提示但等待新的 Agent 同步。390px 页面无横向溢出，授权弹窗截图已核对。为检查成功提交，本机 QA 数据库只刷新通道/回执时间，不改变已通过测试的计划与回执；没有调用生产 API。证据在 `/tmp/tza-lifecycle-20261106/`，Free2 的 `evidence/free2/` 包含最终测试与 unit 校验日志；远端临时 unit、专用 helper 和测试目录清理后，原 Agent 状态保持 inactive。

#### 2026-11-06 Debian 13 真实升级与回退验收

`TestRuntimeUpgradeRollbackWithRealChromiumLinux` 在兼容的 Debian 13 Free 测试 VPS 上完整通过，用时 **471.95 秒**，测试进程正常退出 `0/SUCCESS`。它使用一次性真实 HTTP 主控、实际普通 Agent、真实 IPSentinel systemd 服务、非 root UDS peer 和实际 Chromium；不是生产 CC 发布或生产节点部署。测试只在显式设置 `TZA_RUNTIME_UPGRADE_LINUX_INPUT` 时运行，并要求独立的 QA unit、安装根和状态目录。

验收中的三次外网 Cloudflare trace 导航均为 HTTP 200、`outcome=normal`，前后出口 IP 匹配：

| 阶段 | Runtime | epoch | Chromium run_id |
| --- | --- | --- | --- |
| 基线 | 0.4.0 | 4 | `5c143fae-c5cf-4cde-acb4-1e858fbf5044` |
| 升级后 | 0.4.1 | 5 | `dd656b7b-f481-43ba-993f-d0e16dd42e00` |
| 回退后 | 0.4.0 | 6 | `dc1917f6-2eba-493a-ade2-48966032aad2` |

三次结果均为 `hard_limits_verified=true`、`cleanup=complete`、`oom_kill_count=0`。网络计量仍是 `host_interface`，`network_isolation_verified=false`；这不能声称通过了独立网络命名空间隔离验收。普通 Agent 的实际 UID/GID 均为 65534、无附加组，有效/许可/ambient capability 均为零。专用安装服务定义的 `systemd-analyze verify` 通过。

升级和回退都完整经过排空、选版、启动为暂停态、新 epoch 显式重授权、普通 Agent 重新同步和单独恢复。旧 epoch 重绑被拒绝，不能复用旧启动回执解除维护。配置指纹不变，预算连续累计为 3 次运行、9 次导航，journal 的最新序号和 ACK 均为 25，三条浏览器终态记录都保留；停服后保存的原生 SQLite 副本通过 `integrity_check`。测试阶段固定执行 7 次批准的 helper 操作，每次故意丢失已提交的终态 HTTP 回执，重传均没有重复执行 helper。

首轮虽完成上述功能断言，但因缺少 `CAP_KILL` 无法终止自身创建的降权 Agent，未正常退出，不计作完整通过；本轮补齐专用安装服务的该能力，并让测试在退出前明确等待 Agent 清理。排查时还发现一次误用 macOS Agent 构建导致的 hello 超时，因此增加 Agent/helper 可执行预检、QA harness 的 Linux amd64 ELF 预检，以及 hello 失败时保存最后观测和错误。最终通过的是显式 `GOOS=linux GOARCH=amd64` 构建后的完整重跑。

两个候选均由 **测试密钥 `tza-upgrade-qa-only`** 签名。`0.4.1` 仅为升级测试 fixture，不是正式发布版；验收的 Probe/Agent 构建注入 `1.9.2` 仅用于测试兼容边界，不代表该正式版本已经发布，不能照搬为生产版本声明。候选归档 SHA-256：

- `0.4.0`：`3b2baac8e43dcf120972f9880f6af7f959e7c694f1db5c88f4f0fc8696978795`
- `0.4.1`：`01e4c29d105871a9506559a85717ee8fc6b72820becc22ee0c277aec8719b100`

最新本地 Go 回归为 **663 项通过、43 项按平台或显式开关跳过，26 包**；`go test -race ./...`、`go vet ./...` 和完整 `pipeline.sh check` 均通过，含 runtime UI 与一次性主控 DOM 回归。可选 character-pack selftest 因本机缺少 Pillow/numpy 跳过，不计作通过。流水线 SHA `2b9fd10` 仅为当时 HEAD，本轮验收修正尚未提交。

完整证据保留在本机 `/tmp/tza-upgrade-live-20261106/final/`，含 `verified-summary.json`、三次运行与操作回执、原生数据库副本、进程身份、unit 校验、构建摘要、前后服务状态和清理结果。远端证据归档下载后已核对 SHA-256。远端 QA unit、harness、helper、Socket、cgroup、进程、安装根和状态目录均已清理；原有 `tza-agent.service` 与 `ipsentinel-observe@free-v4.timer` 前后状态均为 active，未对它们执行停启操作。

本轮不修改 IPSentinel 权威协议，不部署 bero/netcup，不发布 fixture 或测试签名。正式上线前仍需固定 CC 对 Probe 的可复现版本依赖、核对正式版本与签名制品，并单独完成资产、备份和回滚检查。

### Agent 安装通道配置

`runtime_install` 由普通 Agent 的独立后台任务处理，与指标、心跳和探测并行，停机时一起取消并等待退出。
标准 systemd 安装无需填写此字段，Agent 会自动配置；已有专用安装服务会在升级时停用，配置与日志原地保留。
`--runtime-installer` 仅保留为旧运维入口，不再由安装脚本创建第二个服务。

Agent 原本由安装脚本以 root 启动（之前不是无 root 用户，只是能力受限）。内置插件通过 systemd drop-in
在 NET_RAW 之外添加 CHOWN/DAC_OVERRIDE/FOWNER/SETUID/SETGID/KILL，并开放固定安装、暂存和状态目录。
保留 `ProtectSystem=strict`、`ProtectHome=true` 和 `NoNewPrivileges=true`；不把插件权限加入 ambient 集合。
降权 peer 直接使用 `/usr/local/bin/tza-agent --runtime-peer`，不再复制第二份程序。
本机关闭插件后移除该 drop-in；浏览器仍必须以非 root 和受限 cgroup 运行，签名与精确计划批准继续有效。

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

API 为 `POST /api/v1/agent/runtime`、`GET /api/v1/admin/runtime` 和 `/api/v1/admin/nodes/{id}/runtime/requests[/{request}]`。后台“插件 → IPSentinel → 设置 → 节点安装与更新”展示分阶段状态与完整批准计划。管理接口沿用管理员会话、CSRF/same-origin 保护；Agent token 不能创建或批准自己的请求。SQLite schema 23 保存节点配置范围、安装请求和维护暂停。安装/选择回执不等于服务健康、adopt 或托管执行授权；启动验收也不等于可派发或恢复。

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
