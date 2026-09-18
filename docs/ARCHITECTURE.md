# 组件边界

所有运行组件统一版本发布，但保持依赖方向单向。解耦不要求运行消息队列或额外服务。

```text
web (HTTP JSON only) ───> httpapi ───> Repository port <── storage (SQLite)
                                             │
notify.Engine ───> Repository port             │
       └────────> Sender port <── Webhook      │
                                             │
Agent ───> Transport port <── HTTPTransport ──┘
  ├─────> Collector port <── SystemCollector (gopsutil)
  └─────> Prober port <── probe.Runner (TCP / NextTrace)

model: shared data contract, validation, billing calendar, route labels
cmd/server and cmd/agent: composition roots and lifecycle
```

- `model` 仅依赖标准库，不导入数据库、HTTP、UI。
- `storage` 实现持久化，没有服务端路由和探测逻辑。
- `httpapi` 只依赖持久化接口；从 `cmd/server` 注入 SQLite 实现。
- `notify` 的规则和发送器使用独立接口，未来增加 Telegram / 邮件适配器不会改动采集模块。
- `agent` 的采集、传输、探测接口可分别替换；CPU 采集不会被最长 60 秒的回程执行阻塞。
- `probe` 不知道数据库或 HTTP；任务执行只允许固定动作，拒绝超频任务和无效目标。
- `web` 只调用 `/api/v1`。`api.js` 处理通信，`admin.js` 负责后台，`app.js` 负责公开页，`detail.js` 负责共享详情，`charts.js` 与 `format.js` 负责展示。不依赖旧主题资产。

Server 的发布产物包含网页，Agent 单独编译，只链接其依赖图，不包含数据库和管理界面。两者通过版本化 JSON 协议协作。SQLite WAL 支持读写，单连接与显式事务约束写入顺序；任务租约、流量差值、告警转换具备事务边界。

任务租约绑定节点、任务快照和 90 秒有效期。未知节点、重复结果、过期结果无法写入。每节点在已有有效租约时不再下发下一批。编辑任务使旧任务结果与租约失效。Agent 上传失败最多重试 3 次；下次探测周期重新下发，状态不能伪装为持续正常。

公开端无 IP 上报字段；路径在 Agent 脱敏，HTTP 再过滤。公开查询只允许公开节点，管理密钥与 Agent 密钥不能互换。浏览器会话为 12 小时 HttpOnly/SameSite Cookie，服务重启失效。配置节点密钥只返回一次，数据库保存 SHA-256 摘要。管理密钥来自环境或权限 0600 的本地文件，不在日志中出现。

设计刻意不抽象通用工作流、插件容器或万能 RPC 层。新增功能先归属现有边界，避免重新产生历史包袱。


组合网络任务由 model 展开为单项探测，storage 按单项最近签发时间独立调度。
调度键包含父任务、检测类型、IP 协议、地址、端口、地区和运营商。
结果按相同维度取最新记录，双栈及延迟/回程不互相覆盖。
后台 task-editor 模块独立处理目录搜索、筛选、多选和自定义目标；Agent 无需了解目录或表单。
