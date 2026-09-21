# 部署、更新、备份

## 先选运行方式

本地预览按 README 的“快速运行”启动。长期运行使用 Linux systemd；Docker 只用于 Server，Agent 直接运行在被监控主机。Server 和 Agent 可以在同一台或不同机器上，网页已嵌入 Server，不需要额外部署前端。

以下 Linux 命令从仓库根目录执行。需要 Go 1.25+；目标机器使用编译好的二进制时不需要 Go、Node 或外部数据库。

## 1. 构建

在目标 Linux 主机编译：

```sh
go build -trimpath -o bin/tza-server ./cmd/server
go build -trimpath -o bin/tza-agent ./cmd/agent
```

如果在 macOS 等机器上为 Linux 编译，显式选择目标架构（x86_64 为 amd64，aarch64 为 arm64）：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/linux-amd64/tza-server ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/linux-amd64/tza-agent ./cmd/agent
```

ARM 主机将两条命令的 `GOARCH` 和输出目录改为 arm64。将产物复制到目标主机；以下安装步骤假设二进制位于目标主机的 `bin/`。

要让 Server 提供一键安装，先把两种架构的 Agent 放到 `internal/agentdist/bin/` 再编译 Server（见该目录的 README）；CI 的发布包和 Docker 镜像已包含。版本号通过 `-ldflags "-X github.com/leeska/TZA-Probe/internal/model.Version=v2.0.0"` 注入，不注入时为 `2.0.0-dev`。推送 `v*` 标签会自动生成 GitHub Release 并附上两种架构的 tar.gz 与校验和。

## 2. 安装 Server

首次安装：

```sh
sudo install -m 0755 bin/tza-server /usr/local/bin/tza-server
id tza-probe >/dev/null 2>&1 || sudo useradd --system --user-group --home /var/lib/tza-probe --shell /usr/sbin/nologin tza-probe
sudo install -d -m 0700 -o tza-probe -g tza-probe /var/lib/tza-probe
sudo install -m 0644 deploy/tza-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tza-server
```

服务监听 `127.0.0.1:25774`。跨主机接入时，在前面配置 Caddy / nginx 的 HTTPS 反代，保留 Host 和 Origin，不缓存 `/api/`。Caddy 示例：

```caddyfile
probe.example.com {
    reverse_proxy 127.0.0.1:25774
}
```

在服务器本地读取 `/var/lib/tza-probe/admin-token`，访问 `https://probe.example.com/admin` 登录。管理密钥不放入 URL。新增节点后，后台提供配置和安装脚本。

Agent 的 endpoint 必须是它能访问的 Server 地址。远程 Agent 不能使用 Server 的 `127.0.0.1` 或 `localhost` 地址。从正确的 HTTPS 地址访问后台再生成接入配置，通常不需要手动改 endpoint。

## 3. 安装 Agent（二选一）

### 一键安装（推荐）

后台“接入”弹窗给出一条命令，在 Agent 主机以 root 执行即可（非 root 且有 sudo 时脚本会自动用 sudo 重新执行）：

```sh
curl -fsSL https://probe.example.com/install.sh | sh -s -- --endpoint https://probe.example.com --token <节点密钥>
```

脚本从 **这台 Server** 的 `/dl/tza-agent-linux-<arch>` 下载与主机架构匹配的 Agent（x86_64 / arm64），校验 SHA-256，写入 `/etc/tza-probe/agent.json`（0600）和 systemd 单元并启动。可选参数：`--interfaces eth0,eth1` 指定统计网卡，`--trace-method icmp|tcp|udp` 指定内置回程的探测方式（默认 `icmp`），`--geo-provider NAME` 指定内置回程的 ASN 数据源（默认 `NextTrace-API`），`--nexttrace /path` 改用外部 NextTrace 二进制（默认用内置实现），`--uninstall` 卸载。不带 `--token` 再执行一次即为升级 Agent，会保留现有配置并备份旧二进制为 `tza-agent.previous`。脚本本身不含密钥，密钥只作为命令参数出现；不希望它进入 shell 历史时可改用 `TZA_TOKEN=<密钥>` 环境变量。

Server 提供的 Agent 来自两处，前者优先：数据目录下的 `agents/tza-agent-linux-<arch>`（运维手动放置，升级 Agent 不必重编 Server），以及构建时嵌入在 Server 内的副本（CI 发布包已包含；自行编译见 `internal/agentdist/bin/README.md`）。两处都没有时 Server 启动日志会提示，安装脚本则报“该 Server 未提供此架构的 Agent”。

### 手动安装

将后台下载的 `agent.json` 放在当前目录，修改必要配置后执行：

```sh
sudo install -m 0755 bin/tza-agent /usr/local/bin/tza-agent
sudo install -d -m 0700 /etc/tza-probe
sudo install -m 0600 agent.json /etc/tza-probe/agent.json
sudo install -m 0644 deploy/tza-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tza-agent
```

`interfaces` 为空时统计非 loopback 网卡；有容器、桥接或隧道时建议明确指定公网网卡，避免重复计数。三网回程由内置 NTrace-core 在 Agent 进程内完成，不需要额外安装；仅在需要改用外部二进制时才填写 `nexttrace` 绝对路径。服务示例以 root 启动，并收窄 capability 集合；仅基础监控和 TCP 可改成普通账户且不授予 `CAP_NET_RAW`。

回程探测从 Agent 主机发出，因此该主机必须直连出口：如果主机开着代理（尤其是 Fake-IP），跳数与 ASN 会变成代理转发路径，线路标签随之失真。验收回程请选择未挂代理的节点，或临时关闭本机代理。

生产接入使用 HTTPS。`allow_http` 只适用于可信私网；从 HTTP 面板生成的配置会开启它，改用公网 HTTPS 时同时设为 false。

## 日常操作

- 状态：`sudo systemctl status tza-server` / `sudo systemctl status tza-agent`。
- 日志：`sudo journalctl -u tza-server -n 100 --no-pager` / `sudo journalctl -u tza-agent -n 100 --no-pager`。
- 配置修改后：`sudo systemctl restart tza-agent`；服务不会自动重载配置。
- 后台节点列表显示 Agent 版本和最后上报时间；网络任务的“执行记录”显示最近执行及失败原因。
- 管理密钥丢失时停服，重新生成 `admin-token` 并维持服务账户所有权和 0600 权限后启动。Server 重启会使浏览器会话失效。

## 更新与回滚

先构建新二进制，保留旧二进制和数据备份，再替换；不要向正在运行的文件直接覆盖：

```sh
sudo systemctl stop tza-server
sudo cp -p /usr/local/bin/tza-server /usr/local/bin/tza-server.previous
# 在此执行下节的停服数据备份，再继续。
sudo install -m 0755 bin/tza-server /usr/local/bin/tza-server
sudo systemctl start tza-server
```

Agent 更新使用相同的 stop → 保留旧二进制 → install → start 顺序，不需要重置密钥。协议版本不匹配时不会自动降级。

数据库当前版本为 2，Server 启动会自动升级本项目的 v1 数据库。回滚必须同时还原旧二进制和升级前数据备份，不能让旧程序读取新版数据库。旧版数据不导入本项目。

## 简单备份与恢复

采用停服复制完整数据目录，不增加后台备份系统。以下为 Server systemd 部署的备份命令：

```sh
sudo systemctl stop tza-server
backup_dir="/var/backups/tza-probe/$(date +%Y%m%d-%H%M%S)"
sudo install -d -m 0700 "$backup_dir"
sudo cp -a /var/lib/tza-probe "$backup_dir/data"
sudo systemctl start tza-server
```

确认复制成功后再认为备份完成。备份包含管理密钥、通知凭据及 SQLite WAL/SHM 文件，保持私有权限。不能只复制运行中的 `tza-v2.db`。

恢复时停止 Server，将当前目录移到另一个路径留作回退；把备份的 `data` 完整复制回 `/var/lib/tza-probe`，恢复 `tza-probe:tza-probe` 所有权后启动对应版本的 Server。Agent 配置位于各主机的 `/etc/tza-probe/agent.json`，单独保存。

## Docker Server（可选）

```sh
docker build -t tza-probe:local .
docker volume create tza-data
docker run -d --name tza-server --restart unless-stopped \
  -p 127.0.0.1:25774:25774 -v tza-data:/data tza-probe:local
```

容器运行账户 UID 为 65532；使用宿主目录绑定时需授予对应权限。更新镜像仍需保留原数据卷，备份时停止容器后复制完整数据。Agent 在宿主机运行，避免把容器指标当成主机指标。

以上是操作说明。本轮未执行真实节点、Telegram 或生产部署验收。
