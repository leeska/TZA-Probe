# 节点执行器：IPSentinel 只读、托管控制与中控排期

产品界面以「IP 地区防护」呈现，默认页突出防送中检测、拉回尝试与干净复测；安装与技术维护收进高级页。官方插件归属与源码整合边界见 [IP-GUARD-INTEGRATION.md](IP-GUARD-INTEGRATION.md)。现有包 ID 和协议身份不变。

节点执行器在市场中作为「节点插件」展示；「插件 → 市场 → 节点插件 → IPSentinel」可选择节点，并在详情的「设置」中管理安装审批、接入授权、配置、排期和历史。后台不再提供独立的「节点执行器」菜单。已有接入或安装记录也会在「插件 → 已安装」提供详情与设置入口，市场不可用时仍能管理本地记录。生成计划不下载依赖，不代表安装或启用。首次自动配置、按需唤醒和停用卸载的交付边界见 [NODE-PLUGINS.md](NODE-PLUGINS.md)。

当前实现包括 M1 只读同步、M2 单任务托管控制面与 M3 中控日程：内建 TCP、Route、Unlock 注册分发，以及独立 IPSentinel 的状态、历史记录、配置验证/应用、单任务准入、取消、暂停与代次恢复。旧 Agent 的 `Job/Result` 线协议、能力白名单和原有排期保持兼容。IPSentinel 数据使用独立 API 和表，不写入延迟、回程或旧解锁结果。

## 职责与契约

- Agent 在独立循环内访问本机 UDS，单 RPC 5 秒超时、同步轮次 20 秒上限，不阻塞心跳、指标或普通探测。
- 中控持久保存安装授权、状态快照、连续记录和通知事件。事务提交后才返回确认游标。
- Agent 根据中控确认游标调用 `records.ack`；HTTP 失败或确认丢失时不 ACK，下一轮从中控游标重读。
- 只读模式保留 IPSentinel 本机排期；managed 模式完成本机交接后，仅中控负责周期排期。IPSentinel 负责浏览器、证据资格和服务判读。TZA 不重复实现恢复归约器。
- 本机模式交接由管理员执行。TZA 不远程 adopt、不下发 Shell、不选择 Socket 路径，也不替本机放宽资源或 Profile 权限。
- M3 的排期控制面已实现。M4 清单审核、签名制品分发、节点暂存、独立本机安装/版本选择、Agent 安装通道与后台计划审核已实现，详见 [RUNTIME-STAGING.md](RUNTIME-STAGING.md)。IPSentinel 已交付 `0.4.0` Debian 13 amd64 真实候选；正式 CC 发布与生产安装验收仍需完成。托管控制面的本机测试不等于真实 Linux worker、浏览器或生产验收。
- 暂存、安装、选择版本、服务启动/启用、本机 adopt 和任务创建分别处理。install/select 只调用管理员独立部署且摘要固定的参考安装器，分别需要节点及操作专属批准；不执行发行包内脚本、不远程 adopt。升级或回退需要停止派发、drain、停止服务和一致性备份；不兼容数据库拒绝直接回退，不覆盖未 ACK journal。

基线为 IPSentinel Executor Contract **1.0.0** / `ipsentinel.executor/1`。权威源为 IPSentinel 仓库 `docs/integration/` 和 `contracts/ipsentinel-executor-v1/`。固定副本位于 `internal/ipsentinel/testdata/contracts/ipsentinel-executor-v1/`，Go 校验器直接嵌入这份 Schema（摘要 `4fc05e95eefa5abd295a7c6d7f1fc7c4e844f9144ca7dd192d5ed5f28c9b9075`）。共享的 Schema、示例、向量共 41 个契约文件都与 `SHA256SUMS` 对照；其中列出的 IPSentinel 文档和 Python 脚本未复制进 TZA。更新协议须先协调权威版本和校验向量。

`assessment-vectors.json` 只保存为参考制品并核对摘要；恢复归约逻辑由 IPSentinel 维护。不能把 Go 侧示例校验或 Mock 测试称作浏览器执行、Linux cgroup 或生产验收通过。

### M4 官方 runtime 分发边界

TZA-CC 新增独立的 `executor` 包类型，既不改变 `plugin.json` 含义，也不复用它安装可执行内容。当前只接受 ID `ipsentinel-runtime`、CC manifest format 1、IPSentinel Executor Contract 1.0.0 和 Linux amd64/arm64；仅有效平台管理员可上传。`executor.json` 严格解码并由权威契约 Schema 验证，禁止 Shell、postinstall、systemd 动作、下载 URL 和未知 JSON 字段。兼容矩阵固定 CC/Probe/Agent 最低版本、Python 版本；每平台须固定 runtime 二进制、Python、Chromium 和系统依赖版本、SHA-256 与大小，且依赖摘要和 Chromium revision 必须与 descriptor 一致。声明本身仍由现有 CC Ed25519 release signature 签名，故权限、descriptor、依赖和兼容矩阵都受该签名覆盖。

CC 平台管理员上传原始依赖与 runtime 文件时必须提供清单里的 SHA-256 路径和精确 `Content-Length`。HTTP 端限管理员 session、CSRF、same-origin、每人每小时 30 件以及每件 1 GiB；服务流式写入临时 blob 后逐字节复核 SHA-256/大小，不接受 URL 或脚本。PostgreSQL 中按摘要与大小登记不可变 blob；发布审核会重新读取所有依赖、计算摘要并验证官方管理员持有免费 `ipsentinel-runtime` 包，任一依赖缺失或改写即阻止发布。下载端核对已发布归档的签名记录与归档摘要，并验证每个对象的摘要/大小；清单通过 CC Ed25519 签名，节点侧在安装前仍须独立用固定信任密钥验签。仅对已发布、未隐藏版本中清单列出的摘要提供字节；撤回后停止访问。市场目录只展示 `ipsentinel-runtime`，Probe 的普通主题/插件安装器会拒绝运行时包。

当前制品 API 是官方运行文件分发，不是节点安装协议。独立本机工具 `cmd/executor-runtime` 已增加所选节点的签名审核、精确批准、制品暂存、安装和版本选择，详见 [runtime 本机流程](RUNTIME-STAGING.md)。暂存状态为 `staged`，安装为 `installed`，选择为 `selected`；这些回执均保持 `activated: false`，不代表已经启动服务或可执行任务。

本机安装不授予中控 executor 权限，也不把浏览器放进普通 Agent。版本选择要求已停止服务并排空任务，备份后才切换；兼容旧版本可使用同一选择流程回退，不自动恢复旧数据库或在失败后重启旧版。运行时启动负责 fingerprint/epoch 更新；改变 epoch 后仍需重新授权。Probe 市场仅在「节点插件」分类展示此包，主控主题/插件安装器仍拒绝它。升级 drain 编排与暂停启动验收已有实现；首次自动配置、自动交接及后台制品上传 UI 仍未完成。不要用通用 `curl` 或普通 ZIP 安装入口绕过节点批准。

## 接入顺序

仅在已明确选择的 Linux VPS 操作。安装 IPSentinel 本身及首次授权应由管理员在本机完成；中控不会从市场自动安装 Python 或 Chromium。

1. 使用支持 M1 的 IPSentinel 运行时，在其本机 CLI 初始化身份并执行 `control adopt-readonly`，填写该安装专属的控制器和节点 UUID。TZA 原有节点 ID 仍保持原格式，不需要改成 UUID。
2. 启动 `control serve`，显式允许 TZA Agent 的运行 UID。默认 Socket 为 `/run/ipsentinel/control.sock`，父目录与 Socket 权限由执行器设置。
3. **服务启动并完成 UID 授权后**再运行 `control info`，取得实际 `target`、`executor_instance_id`、`descriptor_sha256` 和 `control.epoch`。首次登记 UID 可能改变 epoch，不能使用服务启动前读取的旧代次。
4. 在 TZA 后台“插件 → IPSentinel → 设置”中选择节点，输入上述身份并授权只读同步。
5. 在该节点的本机 `agent.json` 增加 `ipsentinel`，然后按现有服务管理方式重启 Agent：

```json
{
  "ipsentinel": {
    "socket_path": "/run/ipsentinel/control.sock",
    "target": {
      "controller_id": "22222222-2222-4222-8222-222222222222",
      "node_id": "33333333-3333-4333-8333-333333333333"
    }
  }
}
```

这里仅展示应合并到原文件的新增字段；保留原 endpoint、token、interfaces 等配置。两个 UUID 是合成示例，必须换成本机 `control info` 的值。Socket 路径只来自本机配置，不接受中控下发。

上述接入默认为 `managed_readonly`，只读授权不会因为 Agent 升级或本机改变模式而自动获得执行权限。托管接入须先按 IPSentinel 的 `docs/CONTROL-M2.md`，由本机管理员备份安装、停止旧 timer、审查本机策略并执行 `control adopt`，然后在后台重新核对实际实例、descriptor、epoch，明确选择“托管配置与单任务执行”。不要远程调用 adopt，也不要同时运行两套调度或通知。

托管能力以实时 `hello.features`、mode、health、配置和本机隔离预检为准；旧 Agent 能力白名单没有加入一个无条件的可执行标志。控制服务降级时仍可暂停/取消；新准入必须通过本机检查。

实例、descriptor 或 epoch 改变时，上传会被拒绝，需要管理员重新核对授权。中控“撤销同步授权”停止数据接收但不停止本机排期、不删除私有 Profile 或已有记录。重新授权相同实例时可重放记录恢复游标；数据缺口明确失败，不静默跳过。

## 中控 API

所有接口位于 `/api/v1`：

| 接口 | 身份 | 用途 |
| --- | --- | --- |
| `GET /agent/executors/ipsentinel` | 该节点 Agent token | 读取批准的身份和持久游标 |
| `POST /agent/executors/ipsentinel/records` | 该节点 Agent token | 上传 `{hello, after_seq, page}`，提交后返回 `{binding, through_seq}` |
| `GET /admin/executors` | 管理员 | 状态及当前授权列表 |
| `PUT /admin/nodes/{id}/executors/ipsentinel` | 管理员 | 授权 `{target, executor_instance_id, descriptor_sha256, epoch}` |
| `DELETE /admin/nodes/{id}/executors/ipsentinel` | 管理员 | 撤销接收授权，保留历史 |
| `GET /admin/nodes/{id}/executors/ipsentinel/records?after_seq=0` | 管理员 | 当前实例历史，每页最多 50 条 |
| `POST /admin/nodes/{id}/executors/ipsentinel/actions` | 管理员 | 创建类型化单任务命令，返回 202 表示已入队 |
| `GET /admin/nodes/{id}/executors/ipsentinel/actions` | 管理员 | 最近 50 条命令及独立 run 状态 |
| `POST /agent/executors/ipsentinel/actions` | 该节点 Agent token | 使用实时 hello 获取一个租约 |
| `POST /agent/executors/ipsentinel/action-results` | 该节点 Agent token | 使用命令 ID 与租约令牌提交类型化回执 |
| `GET /admin/nodes/{id}/executors/ipsentinel/schedule` | 管理员 | 节点日程、各服务覆盖预估与最近 50 个时段 |
| `PUT /admin/nodes/{id}/executors/ipsentinel/schedule` | 管理员 | 使用 `expected_revision` 创建或替换日程，固定当前配置与绑定 |
| `DELETE /admin/nodes/{id}/executors/ipsentinel/schedule` | 管理员 | 使用 `{expected_revision}` 停用未来排期，保留日程与时段历史 |

这些数据不进入公开面板 API，也不透传给主题桥接。管理员写接口沿用同源检查；Agent 不能用自己的 key 授权绑定。

SQLite schema **23** 包含同步表、`executor_actions`、不可变配置快照、`executor_schedules`、`executor_slots`、runtime 安装通道和持久维护暂停。19→20 会取消旧实现中没有身份及租约令牌的待处理命令，管理员需重新核对并创建；旧记录保留。20→21 只增加排期表，不自动创建或启用日程；21→22 增加节点专属安装请求；22→23 仅增加 `runtime_maintenance`，不自动暂停现有节点、授权或安装。按节点、实例、seq 去重，同时约束 record_id；同序号不同内容、同 ID 不同序号、断号、身份不符均拒绝并回滚。当前不自动清理记录、命令、时段、安装请求与配置表；大规模部署前还需补充中央归档与磁盘配额策略，当前阶段应单节点验收。

## 单任务控制流程

1. 明确批准 `mode: "managed"` 的绑定。省略 mode 或选择 `managed_readonly` 都不能派发。
2. 在后台“验证托管配置”填写本机批准的契约 JSON；`config.validate` 不产生修订。管理员在“托管命令”刷新并查看本机通过回执。
3. 核对服务、用途、单次资源/超时、每日预算，再单独确认 `config.apply`。以当前 revision 做 CAS，保存执行器返回的真实 revision/hash；相同配置可返回 `changed:false`，不会虚构下一修订。
4. 只从本机已应用配置选择服务和用途。任务固定配置 revision/hash、job UUID、phase、not_before、expires_at 和 timeout；时间窗口最多 15 分钟，后台单次任务使用五分钟窗口。实验不计入恢复确认。
5. 后台命令需要不超过 45 秒的新鲜 Agent 快照。命令租约为 30 秒，持久保存独立的 lease ID、RPC request ID 和 attempt。传输失败不写失败终态；租约到期后保留原 request ID 与 job 内容重投。旧 lease ID 不能覆盖新租约回执。
6. 每次发送 `run.submit` 前先查 `run.get`；已受理或终态任务只查询原 job，不重新浏览。只有明确的 busy/not_due 等可重试拒绝才更换 RPC request ID，保留 job ID 和原窗口，并退避 15 秒。永久拒绝结束命令。
7. 命令 `accepted` 只表示执行器已持久受理。页面分别显示 command 状态与 run 状态；running、cancelling 和原生终态通过 journal 投影，并与原记录、通知 outbox 和持久游标在同一事务内保存。旧 accepted 快照不能覆盖终态。

后台暂停先锁住中央新派发，再优先要求本机暂停并清理 worker。未派发任务可以本地取消；已派发任务由 `run.cancel` 和 journal 确认。恢复固定使用管理员看到的 pause generation，不自动采用新代次。若恢复命令后又请求暂停，旧恢复回执不能解开中央门禁。

重新绑定或撤销授权会使所有未完成命令失效，保留其历史回执；迟到回执被拒绝。尚未领取的任务可在执行器受理前由中控直接取消。撤销不是远程停止：已经被本机受理或正在传输的 RPC 可能继续完成；需要先暂停并确认本机清理完成。撤销不会回退到本机自动排期，也不会删除历史或私有 Profile。

窗口到期后，从未派发的任务直接过期；已经派发但回执未知的任务仍可取得查询租约，即使此时执行器已暂停或配置已变化。Agent 先查询原 job；找到则恢复原回执，找不到且原窗口已过则拒绝，不会再次提交或虚报成功。`config.validate` 返回 `valid:false` 时保存未通过回执，不标成命令成功，也不能据此应用配置。

## 中控周期排期

完成本机 managed adopt、授权同步并应用配置后，在“托管日程”中明确选择启用。每个节点只有一份日程，保存须携带 `expected_revision`（首次为 0），并固定当时的绑定、epoch 和配置 revision/hash。服务与用途必须来自本机已应用配置；只读授权不能创建日程。节点时区须明确填写 IANA 名称，不采用中控或浏览器的默认时区。

日程使用 `observe_services`、`experiment_services`、`slots: [{time:"08:00",phase:"followup"}]` 和 `timeout_ms`。阶段为 baseline/followup/experiment，每个当地时段只执行一个服务；观测与实验分别按当地民用日期及该用途的时段序号轮换，和收到多少回执无关。最多 32 个时段、每种用途最多 32 个服务；同一时刻不能配置两个时段。单次超时、节点总次数及总网络预留不得超过已批准配置。配置预算不会按服务数翻倍；单次任务与周期任务也共享执行器按 UTC 日执行的实际预算。

Server 每 5 秒运行独立排期循环。仅跨过当前时段、前次检查距今不超过 15 秒、节点 UTC 日预算有可验证余量、绑定与配置匹配且执行器状态新鲜/可执行时才入队。预算日期不吻合、预算快照缺失/未知、次数已用尽或单次网络预留超过剩余额度都跳过当前时段。时段窗口固定为一分钟；中控间断、Agent 快照过期、暂停、能力不足或配置变化时记为跳过。已经错过的历史时段不扫描、不补跑；Agent 在一分钟窗口之后回来时，从未派发的任务直接过期。曾派发的任务只恢复原 job 的受理状态。时钟回拨时保留持久时间高水位，不因回拨重建任务。

夏令时缺失时刻直接跳过；重复时刻只采用第一次出现，即使第一次因停机错过，也不会改在第二次补跑。时段去重键为节点、绑定、当地日期及 HH:MM；稳定 job UUID 从同一内容推导。时段记录和命令在同一 SQLite 事务内提交；并发 ticker、重启或同日编辑不会重新执行同一时段。

页面按未来一年及足够的轮换周期估算各服务的最长计划间隔，超过 48 小时明确提示覆盖不足，计算包含时区变化和缺失时刻。此预估假设所有计划时段都能执行；离线、暂停、UTC 日额度耗尽和失败会进一步延长实际覆盖，不能由“计划满足 48h”推断证据已有效。

修改或停用日程会取消尚未派发的旧排期任务，保留全部时段历史。已派发但回执未知的任务继续查询原任务；需要停止活动 worker 时另行暂停。实例、descriptor、epoch 或授权模式变化，以及撤销绑定，都会停用旧日程；重新核对绑定与配置后必须显式保存启用，不能自动沿用旧授权。

## 证据和通知

- 节点在线只代表 TZA Agent 有联系；`hello.status.health` 为执行器最近快照；执行能力、准入与服务证据分别判断。
- 同步超过 45 秒没有刷新，或者 assessment 到达 `valid_until`，后台显示未知/过期。页面定时重算有效期，刷新按钮重新拉取状态。
- `completed` 不等于恢复。Google 的 confirmed 显示“首页跳转消失，连续干净复测通过”，不显示 IP 国家修复。
- `legacy_import` 原样存档，不作为 TZA 自动确认恢复的依据。
- `assessment.changed`、暂停状态变化写入 TZA 现有通知 outbox；重复记录不重复通知，快照、未变化状态和初始 unknown 不通知。未知或阶段变化使用“执行器状态变化”，不伪装成告警解除。超过通知窗口的旧记录不重放通知。

## 验证

```bash
go test ./internal/ipsentinel ./internal/storage ./internal/agent ./internal/httpapi ./internal/notify
go test -race ./internal/ipsentinel ./internal/storage ./internal/agent ./internal/httpapi ./internal/notify
TZA_IPSENTINEL_ROOT=/absolute/path/to/ipsentinel go test ./internal/httpapi -run TestPythonReadonlyUDSIntegration -count=1
TZA_IPSENTINEL_ROOT=/absolute/path/to/ipsentinel go test ./internal/httpapi -run TestPythonManagedAdmissionUDSIntegration -count=1
TZA_IPSENTINEL_ROOT=/absolute/path/to/ipsentinel go test ./internal/httpapi -run TestPythonScheduledAdmissionUDSIntegration -count=1
```

Python 测试需要 IPSentinel `.venv/bin/python` 及当前运行时模块。只读测试在临时目录启动真实 Python UDS，验证身份、状态、游标、重连和 ACK。托管测试使用真实 Python ControlProtocol、策略校验、SQLite、UDS 和 Go/HTTP 通道，但注入仅允许准入/取消的 worker 测试替身与状态适配；覆盖配置修订、任务去重、取消终态、暂停代次和持久 ACK。排期联调等待下一个真实 UTC 分钟时段，验证中央排期到 Python 准入的 job/run 稳定性；通常耗时不超过 70 秒。它们不启动 Chromium、不访问站点、不证明 Linux cgroup/非 root peer 隔离。

可设置 `TZA_EXECUTOR_BROWSER_QA=/absolute/private/tmp` 保持托管测试服务，测试账户仅写入该目录的 `browser-fixture.json`（0600），每秒同步；写入 `done` 文件后测试关闭服务，最长十分钟。仅用于临时浏览器 QA，不用于生产，也不要保存或分享测试账户。

尚待后续验收：TZA Agent + 真实 Linux 非 root peer/cgroup + Chromium 的端到端运行、OOM/重启/断网和资源预算实机测试，以及生产只读节点接入、M3 本机运维交接、真实官方 runtime 发行制品、M4 所选节点安装/升级/回滚。当前 CC/Probes 分发测试使用不可执行的文本 fixture，不能替代生产制品或 Linux 实机验收；IPSentinel 自身已有的 Linux 测试证据不能替代 TZA 这一端的实机验收。
