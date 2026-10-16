# 部署、更新、备份

## 0. 一键安装（推荐）

Linux 主机（amd64 / arm64），一条命令装成 systemd 服务。按来源分两种方式：

| 方式 | 命令 | 依赖 | Agent |
| --- | --- | --- | --- |
| 从 Release 一键安装（默认） | `curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh \| sudo sh`<br>指定版本加 `-s -- --release vX.Y.Z` | curl、sha256sum | 发布包内嵌；发布签过名时可在后台一键升级 |
| 从源码构建 | `curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh \| sudo sh -s -- --source`<br>指定分支 / tag 用 `--ref REF` | git、Go 1.27 | 本机编译放进 `<数据目录>/agents/`，未签名 |

- Release 方式不写版本时装最新发布（`/releases/latest`），下载后按 `.sha256` 校验；`--version vX.Y.Z` 与 `--release vX.Y.Z` 等价。
- 安装 `/usr/local/bin/tza-server`，旧二进制保留为 `tza-server.previous`，重复执行即升级。
- 创建 `tza-probe` 系统账户与 `/var/lib/tza-probe`（0700），数据目录不会被覆盖。
- 安装 `deploy/tza-server.service` 并 `systemctl enable --now`，等待 `/healthz` 通过后打印管理地址与密钥位置。
- 源码构建时顺带编译 amd64 / arm64 两个 Agent 放进 `<数据目录>/agents/`，后台的一键安装命令开箱可用（Server 分发 Agent 时优先用数据目录里这份，升级 Agent 不必重编 Server）。在服务器上编出来的 Agent 没有签名，不能一键升级；要用一键升级见下文「一键升级」。
- 两种方式可以互相切换，重跑对应命令即可。源码构建放进 `<数据目录>/agents/` 的 Agent 带 `.source-build` 标记，改用 Release 安装时会被清掉，换回发布包内嵌的 Agent；没有标记的（手工放的）保留并给出提示。
- 其他参数：`--listen ADDR`（默认 `127.0.0.1:25774`）、`--data DIR`（默认 `/var/lib/tza-probe`）、`--no-systemd`（只装二进制与数据目录）。无 systemd 的环境下脚本会跳过服务注册并打印手动启动命令。

Docker 部署用 `deploy/docker-compose.yml`（数据放命名卷）：

```sh
git clone --depth 1 https://github.com/leeska/TZA-Probe && cd TZA-Probe
docker compose -f deploy/docker-compose.yml up -d --build
```

以下手工步骤用于定制安装、更新与备份。

## 先选运行方式

本地预览按 README 的“本地运行”启动。长期运行使用 Linux systemd；Docker 只用于 Server，Agent 直接运行在被监控主机。Server 和 Agent 可以在同一台或不同机器上，网页已嵌入 Server，不需要额外部署前端。

以下 Linux 命令从仓库根目录执行。需要 Go 1.27；目标机器使用编译好的二进制时不需要 Go、Node 或外部数据库。

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

要让 Server 提供一键安装，先把两种架构的 Agent 放到 `internal/agentdist/bin/` 再编译 Server（见该目录的 README）；`scripts/pipeline.sh build` 的发行包和 Docker 镜像已包含。版本号通过 `-ldflags "-X github.com/leeska/TZA-Probe/internal/model.Version=v1.0.0"` 注入，不注入时为 `1.0.0-dev`。发布 GitHub Release 用 `scripts/release.sh`（见 README）。

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

安装脚本结束时打印后台用户名、密码（密码只在首次安装时显示一次）。访问 `https://probe.example.com/admin` 用用户名密码登录。MCP API Key 在“面板设置”中生成、重新生成或撤销，客户端配置和连接排错见 [MCP 接入指南](MCP.md)。用户名与密码在“面板设置 → 后台账号”修改。新增节点后，后台提供配置和安装脚本。

Agent 的 endpoint 必须是它能访问的 Server 地址。远程 Agent 不能使用 Server 的 `127.0.0.1` 或 `localhost` 地址。从正确的 HTTPS 地址访问后台再生成接入配置，通常不需要手动改 endpoint。

## 3. 安装 Agent（二选一）

### 一键安装（推荐）

后台“接入”弹窗给出一条命令，在 Agent 主机以 root 执行即可（非 root 且有 sudo 时脚本会自动用 sudo 重新执行）：

```sh
curl -fsSL https://probe.example.com/install.sh | sh -s -- --endpoint https://probe.example.com --token <节点密钥>
```

脚本从 **这台 Server** 的 `/dl/tza-agent-linux-<arch>` 下载与主机架构匹配的 Agent（x86_64 / arm64），校验 SHA-256，写入 `/etc/tza-probe/agent.json`（0600）和 systemd 单元并启动。可选参数：`--interfaces eth0,eth1` 指定统计网卡，`--trace-method icmp|tcp|udp` 指定内置回程的探测方式（默认 `icmp`），`--geo-provider NAME` 指定内置回程的 ASN 数据源（默认 `IPAPI.com`），`--nexttrace /path` 改用外部 NextTrace 二进制（默认用内置实现），`--uninstall` 卸载。不带 `--token` 再执行一次即为升级 Agent，会保留现有配置并备份旧二进制为 `tza-agent.previous`。脚本本身不含密钥，密钥只作为命令参数出现；不希望它进入 shell 历史时可改用 `TZA_TOKEN=<密钥>` 环境变量。

Server 提供的 Agent 来自两处，前者优先：数据目录下的 `agents/tza-agent-linux-<arch>`（运维手动放置，升级 Agent 不必重编 Server），以及构建时嵌入在 Server 内的副本（发行包已包含；自行编译见 `internal/agentdist/bin/README.md`）。两处都没有时 Server 启动日志会提示，安装脚本则报“该 Server 未提供此架构的 Agent”。

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

## TZA-CC 市场（可选）

后台「主题」「插件」两页各带一块市场，从 TZA-CC 安装主题包、附加包与插件（见 [THEMES.md](THEMES.md#市场tza-cc)），TZA-CC 账号在「面板设置 → 账号与安全」连接。复古印刷、现代极简、二次元三套外观也是从市场装的主题包；不连市场的部署可以用发行包 `themes/` 里的 zip（或仓库 `go run ./tools/themepack themes/*` 打出的 zip）在「主题」页上传。默认连官方 TZA-CC（`https://tza.cc`），但 Server 只在管理员打开市场、安装 / 更新、绑定时才访问它，平时不出站。不想要市场就加 `--cc-url off`，其余功能不受影响。

| 参数 | 环境变量 | 说明 |
| --- | --- | --- |
| `--cc-url URL` | `TZA_CC_URL` | TZA-CC 地址，如 `https://cc.example.com`（可带路径前缀）。参数优先于环境变量；都不设时用官方地址 `https://tza.cc`；`off`（不分大小写）关闭市场 |
| `--cc-key ID:BASE64`（可重复） | `TZA_CC_KEYS`（逗号分隔） | 额外信任的 TZA-CC 签名公钥（测试或私有部署的 CC 用，格式为 `<key id>:<base64>`），与内置的官方公钥合并（官方 CC 不用配）。同一个 key id 出现两次且内容不同时拒绝启动 |

```sh
tza-server --data /var/lib/tza-probe --cc-url https://cc.example.com --cc-key 'tzacc-2026-1a2b3c4d:1PcvnQ8z…'
```

systemd 部署可在 `tza-server.service` 的 `[Service]` 里加 `Environment=TZA_CC_URL=…` 与 `Environment=TZA_CC_KEYS=…`。

- 只配地址不配公钥时可以浏览市场，但安装一律拒绝（签名无法验证）。启动日志会提示。
- 地址是明文 `http://` 时日志会警告：绑定后的实例 token 会明文传输，只适合本机或可信内网的测试 CC。
- Server 只主动访问 CC（NAT 后面也能用），需要能出站到 CC 地址；安装包下载地址可能在 CC 的对象存储上，同样要能访问。HTTP 代理按 `HTTPS_PROXY` / `HTTP_PROXY` 环境变量生效。
- 绑定凭据存在数据目录的 `cc-instance`（0600），市场来源记录在 `themes/.market/`；整目录备份时一并带走。换 `--cc-url` 不会把旧凭据发给新地址。

## 日常操作

- 状态：`sudo systemctl status tza-server` / `sudo systemctl status tza-agent`。
- 日志：`sudo journalctl -u tza-server -n 100 --no-pager` / `sudo journalctl -u tza-agent -n 100 --no-pager`。
- 配置修改后：`sudo systemctl restart tza-agent`；服务不会自动重载配置。
- 后台节点列表显示 Agent 版本和最后上报时间；网络任务的“执行记录”显示最近执行及失败原因。
- 忘记后台密码：`sudo tza-server reset-password --data /var/lib/tza-probe` 生成新密码并打印（加 `--username 名字` 同时改用户名），不用停服，下次登录即生效；已登录的会话在服务重启后失效。账号保存在数据目录的 `admin-account`（密码只存 PBKDF2 摘要）。
- 管理密钥丢失时停服，重新生成 `admin-token` 并维持服务账户所有权和 0600 权限后启动。Server 重启会使浏览器会话失效。

## 升级与回滚

一键安装的 Server 重新执行安装脚本即可升级：脚本替换二进制后自动 `systemctl restart tza-server`，并等到 `/healthz` 报出新版本号才结束；重启失败或 30 秒内仍是旧版本时会提示查看日志，旧二进制保留在 `tza-server.previous`。手工安装时先构建新二进制，保留旧二进制和数据备份，再替换；不要向正在运行的文件直接覆盖：

```sh
sudo systemctl stop tza-server
sudo cp -p /usr/local/bin/tza-server /usr/local/bin/tza-server.previous
# 在此执行下节的停服数据备份，再继续。
sudo install -m 0755 bin/tza-server /usr/local/bin/tza-server
sudo systemctl start tza-server
```

先升 Server，再升 Agent。Agent 升级使用相同的 stop → 保留旧二进制 → install → start 顺序（或不带 `--token` 重新执行安装命令），不需要重置密钥。

**一键升级**只下发运维者签过名的 Agent。签名私钥只放在你自己的电脑上，Server 手里只有签好的安装包、`.sig` 与公钥；节点安装时钉住公钥，之后只接受这把密钥签过、且比自己新的版本。所以 Server 或后台账号即使被人接管，也推不了自己的程序，也不能把节点退回旧版本——最多让一键升级停摆。

1. 在自己电脑上生成一次密钥（私钥另行备份，丢了就要换钥）：

   ```sh
   tza-server agent-keygen --id ops-2026 --out ~/tza-agent-signing/agent.key
   ```

   同目录会写出公钥 `upgrade-key.pub`。

2. 每次发版，用带版本号的 ldflags（`-X github.com/leeska/TZA-Probe/internal/model.Version=X.Y.Z`）编好 Agent 后签名；版本须是 `X.Y.Z`，不带 `-dev` 之类后缀：

   ```sh
   tza-server agent-sign --key ~/tza-agent-signing/agent.key --version X.Y.Z tza-agent-linux-amd64 tza-agent-linux-arm64
   ```

   `bash scripts/pipeline.sh build` 设了 `TZA_AGENT_KEY=~/tza-agent-signing/agent.key` 时会自动签名，并把签名与公钥一起嵌进 Server。

3. 把两个二进制、各自的 `.sig` 和 `upgrade-key.pub` 放进 Server 数据目录的 `agents/`（属主 `tza-probe`）。`/dl/upgrade-key.pub` 随即可取。

4. 每台节点执行一次安装命令（首次接入用「接入」给出的命令；已接入的不带 `--token` 再执行一次），安装脚本把公钥钉到 `/etc/tza-probe/upgrade-key.pub`。之后在后台「节点管理」勾选节点点「升级 Agent」（或标题栏的「升级 Agent（N）」、那一行的「升级」、状态格里的「需更新 ⇡」）即可。

Agent 在下一次上报时拿到新版本的下载路径、SHA-256 与签名，先验签（密钥、架构、版本须比自己新），再下载到 `/var/lib/tza-agent`、核对摘要并试跑 `--version`，然后以状态 3 退出；systemd 单元的 `ExecStartPre=-+/usr/local/bin/tza-agent --config /etc/tza-probe/agent.json --apply-update` 在重启前按钉住的公钥再验一遍，才把它换到 `/usr/local/bin/tza-agent`（单元开了 `ProtectSystem=strict`，Agent 自己写不了 /usr 和 /etc），旧版本留在 `tza-agent.previous`。OpenRC 没有沙箱，Agent 验签后就地替换，`supervise-daemon` 负责拉起。

后台的提示：「安装包未签名」表示 `agents/` 里缺 `.sig`、`.sig` 签的不是当前这个二进制，或缺公钥；「需手工升级一次」表示节点上的 Agent 较早、或还没钉公钥，按弹窗里的命令在机器上执行一次即可。没配签名时节点照常安装和运行，只是升级要在机器上重跑安装命令。换钥：生成新钥、用新钥签名、替换 `agents/upgrade-key.pub`，再在每台节点重跑一次安装命令。

新版本如果改了数据库结构，Server 启动时自动升级数据库。从数据库版本 14 升上来时，Server 会在后台把已有一周的探测结果汇总进网络历史格子（10 台节点约十几秒，1 核机更久，日志 `network history buckets backfilled` 表示完成）；期间面板照常使用，只是 24 小时以上的网络历史仍按旧方式计算。升级后的数据库旧程序打不开，因此回滚必须同时还原旧二进制和升级前的数据备份。

## 备份与恢复

最简单的是后台“面板设置 → 数据备份”直接下载数据库快照（`GET /api/v1/admin/backup`，不用停服）；恢复时停服，把它放进数据目录、命名为 `tza-probe.db`（替换原文件），删掉同目录的 `tza-probe.db-wal` 与 `tza-probe.db-shm` 后启动。快照含通知凭据与 MCP Key 摘要，按私密文件保管；它只含数据库，管理密钥 `admin-token` 与后台账号 `admin-account` 需另外保存。

要连同管理密钥一起备份，就停服复制完整数据目录。以下为 Server systemd 部署的备份命令：

```sh
sudo systemctl stop tza-server
backup_dir="/var/backups/tza-probe/$(date +%Y%m%d-%H%M%S)"
sudo install -d -m 0700 "$backup_dir"
sudo cp -a /var/lib/tza-probe "$backup_dir/data"
sudo systemctl start tza-server
```

确认复制成功后再认为备份完成。备份包含管理密钥、通知凭据及 SQLite WAL/SHM 文件，保持私有权限。不能只复制运行中的 `tza-probe.db`。

恢复时停止 Server，将当前目录移到另一个路径留作回退；把备份的 `data` 完整复制回 `/var/lib/tza-probe`，恢复 `tza-probe:tza-probe` 所有权后启动对应版本的 Server。Agent 配置位于各主机的 `/etc/tza-probe/agent.json`，单独保存。

## Docker Server（可选）

```sh
docker build -t tza-probe:local .
docker volume create tza-data
docker run -d --name tza-server --restart unless-stopped \
  -p 127.0.0.1:25774:25774 -v tza-data:/data tza-probe:local
```

容器运行账户 UID 为 65532；使用宿主目录绑定时需授予对应权限。更新镜像仍需保留原数据卷，备份时停止容器后复制完整数据。Agent 在宿主机运行，避免把容器指标当成主机指标。
