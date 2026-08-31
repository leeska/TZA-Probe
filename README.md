# TZA Probe

TZA Probe 是一套自托管的主机状态、三网延迟与回程线路监控系统。Core 负责管理、调度和结果存储，配套 Agent 在受控边界内执行探测。

## 三网探针

- 内置 31 个地区的电信、联通、移动目标目录。
- 每个地区、运营商、IPv4、IPv6 都可独立选择。
- 延迟监控与回程线路使用两套独立配置，默认全部关闭。
- 回程探测默认每 60 分钟执行一次，最短允许 15 分钟。
- 目标目录随 Core 本地发布，运行时不依赖第三方目录服务。
- Agent 只接收经过限制的结构化任务，不执行任意命令。

管理入口为 `/admin/monitoring`（旧 `/admin/probes` 和 `/admin/ping` 自动跳转）。该页面由 Core 内置的 TZA Probe Web 提供，刷新后仍保持真实路由；展示主题不再携带后台副本。

升级已有 Komari 配置时，Core 首次启动会把旧的 `carrier_route_selections`
转换为显式回程任务，并快照当时的节点 UUID。之后新增节点不会自动加入，必须在监控中心的任务编辑器中明确绑定。

详细协议、调度和安全边界见 [docs/CarrierRoute.md](docs/CarrierRoute.md)。

## 组件

| 组件 | 仓库 | 职责 |
| --- | --- | --- |
| Core | [leeska/TZA-Probe](https://github.com/leeska/TZA-Probe) | 管理后台、节点状态、调度、结果与公开 API |
| Web | [leeska/komari-web](https://github.com/leeska/komari-web) | TZA Probe Core 内置前端与探针中心 |
| Agent | [leeska/TZA-Probe-Agent](https://github.com/leeska/TZA-Probe-Agent) | 主机指标、延迟和受限回程探测 |
| Glassmorphism | [leeska/komari-theme-Glassmorphism](https://github.com/leeska/komari-theme-Glassmorphism) | 可选公开展示主题 |

## 本地开发

Core 使用 Go 1.25。构建前需要把前端产物放入 `web/public/defaultTheme/`；GitHub Actions 会自动从 TZA Probe Web 仓库的 `main` 分支构建并嵌入。

```bash
go test ./internal/probe ./database/tasks ./web/rpc/jsonrpc ./internal/server
go build -o tza-probe .
./tza-probe server
```

默认监听 `0.0.0.0:25774`。首次运行后打开 `/install` 完成管理员和数据库配置。

Linux 可以直接使用仓库内安装器；新安装会使用 `/opt/tza-probe`、`tza-probe.service` 和 `data/tza-probe.db`：

```bash
curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/install.sh | bash
```

## 兼容性

TZA Probe 从 Komari 演变而来。为避免破坏已有数据库、Agent 协议和增量升级，当前保留原 Go module 路径、部分内部配置键和旧数据文件名。默认启动会优先使用新的 `tza-probe.db`，但在检测到已有 `komari.db` 时自动沿用旧库；这些仅是兼容层，不是产品依赖关系。

## License

项目继续遵循上游 MIT License。原项目版权和许可声明保留在 [LICENSE](LICENSE) 及源文件历史中。
