# 三网延迟与回程线路

TZA Probe 在同一份本地目录上提供两套互相独立的选择：

- 三网延迟：保存后生成 TZA 托管的 TCP Ping 任务，继续复用 Komari 原有 Ping 调度、Metric Store 和丢包统计。
- 三网回程：按较长周期向 Agent 下发受限 traceroute，并保存最近一次结构化线路结果。

两套选择默认都为空。延迟选择为空时不会生成托管 Ping 任务；回程选择为空或总开关关闭时不会下发 traceroute。手动创建的普通 Ping 任务没有 `managed_by=tza-carrier-ping` 标记，不会被选择器修改或删除。

## 三网延迟配置

- `carrier_ping_selections`：延迟监控组合，格式与回程组合相同，默认 `[]`。
- `carrier_ping_interval_seconds`：延迟检测周期，默认 `60` 秒，范围 `10` 到 `3600` 秒。

管理员通过 `/admin/monitoring` 创建延迟或回程任务，并在每个任务中绑定一台或多台明确的机器。延迟任务继续使用原生 PingTask API；内置目标使用 `*.ip.zstaticcdn.com` 域名，不把快照 IP 写入探测目标。回程任务通过 `admin:setCarrierRouteTasks` 保存，任务拥有独立周期、启用状态、备用域名和目标节点列表。实现位于 `internal/probe`，两类监控互不共享选择集合。

## 三网回程配置

TZA Probe 的三网回程检测由 Core 和 Agent 协作完成：

1. Core 按每个回程任务自己的周期（默认 60 分钟，最小 15 分钟）从本地内置目录使用 IPv4/IPv6 CDN 域名下发任务；空节点列表不会扩散到所有机器。
2. 支持 v2 的 Agent 使用参数化 `traceroute` 执行 TCP 探测；TCP traceroute 不可用时降级到 UDP traceroute。探测有目标数、并发数、最大跳数和超时上限。
3. Agent 只回传结构化结果，Core 在内存中保存每个节点的最近结果，并通过 `public:getCarrierRouteStats` 提供给主题。隐藏节点仍遵循公共 RPC 的权限检查。

Core 配置项可以通过管理员设置接口修改：

- `carrier_route_tasks`：显式回程任务数组，默认 `[]`。每项包含 `id`、`name`、`clients`、`enabled`、`region`、`carrier`、`family`、`host`、`backup_host`、`port` 和 `interval_seconds`。
- `carrier_route_selections`：旧版组合配置，仅用于首次启动迁移；Core 会把它转换为显式任务并快照当时的节点 UUID，之后不再自动扩散。

目标目录只从 `internal/probe/targets_embedded.json` 读取。旧版本遗留的 `carrier_route_targets_url` 配置不会再触发网络请求；更新目录需要随 Core 源码审核、测试并重新构建。

默认没有任何监控组合，也不会创建调度任务或向 Agent 下发探测。管理员需要在支持 `admin:getCarrierRouteOptions` / `admin:setCarrierRouteSelections` 的设置界面中逐项添加地区、运营商和 IP 协议；IPv4 与 IPv6 可以独立选择。

Glassmorphism 从 Core 返回的 `enabled`、`interval_seconds` 和 `selections` 派生展示状态，不再维护重复的探测开关或周期。Agent 不支持 v2 或缺少 `traceroute` 时，主题会显示“不支持/暂无结果”，不会伪造线路标签。

线路标签采用 TcpQuality 的核心识别规则（10099、CN2GIA、9929、4837、163、CMIN2、CMI 等），仅使用 Agent 内置的已知 IP 前缀进行确定性分类，不依赖外部 ASN/Whois 服务。
