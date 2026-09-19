# 部署、更新、备份

## Linux 单机部署

构建 `tza-server` 和 `tza-agent`，复制到 `/usr/local/bin/`。Server 所在机器：

```sh
sudo useradd --system --home /var/lib/tza-probe --shell /usr/sbin/nologin tza-probe
sudo install -d -m 0700 -o tza-probe -g tza-probe /var/lib/tza-probe
sudo install -m 0644 deploy/tza-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tza-server
```

服务默认只监听 `127.0.0.1:25774`，在前面放 Caddy / nginx TLS 反代。反代须保留 Host 和 Origin，不缓存 `/api/`。Caddy 示例：

```caddyfile
probe.example.com {
    reverse_proxy 127.0.0.1:25774
}
```

在服务器本地读取 `/var/lib/tza-probe/admin-token` 登录后台，不把密钥放到 URL。管理密钥丢失时停止服务，重新生成该文件并确保权限 0600 后启动；全部浏览器会话随重启失效。修改环境密钥也需要重启。

Agent 主机：将后台生成的配置保存为 `/etc/tza-probe/agent.json`，仅服务账户可读，配置实际网卡与磁盘挂载点。安装并验证 NextTrace 后，填写绝对路径。Linux 三网回程工具需要相应原始网络权限；示例服务以 root 启动并收窄 capability 集合，且禁止任意远程命令。仅基础监控和 TCP 时可自行换成普通账户，不授予 `CAP_NET_RAW`。

```sh
sudo install -d -m 0700 /etc/tza-probe
sudo install -m 0600 agent.json /etc/tza-probe/agent.json
sudo install -m 0644 deploy/tza-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tza-agent
```

真实生产环境暂未验收的部分：具体发行版的 systemd 限制与 NextTrace 权限组合、真实跨运营商 IPv6 路径、ASN 服务可达性。上线前在一台代表机器试运行，检查日志与三网任务结果，再批量部署。

## Docker Server

```sh
docker build -t tza-probe:local .
docker volume create tza-data
docker run -d --name tza-server --restart unless-stopped \
  -p 127.0.0.1:25774:25774 -v tza-data:/data tza-probe:local
```

容器只运行 Server，不把 Agent 放入容器伪装为主机指标。保留 TLS 反代。运行账户 UID 65532，绑定宿主目录时需预先授予该账户目录权限。镜像已通过 Linux arm64 非 root 运行和后台流程验证；基础镜像锁定已验收摘要，只在本地构建，不自动发布。

## 更新 / 回滚

1. 保留当前二进制与配置，停止 Server。
2. 备份完整私有数据目录（包含 `admin-token` 以及可能存在的 SQLite WAL/SHM 文件）。
3. 替换 Server 二进制，启动检查 `/healthz`、后台登录、节点上报和任务结果。
4. 更新 Agent 二进制并重启；协议版本不匹配时不会自动降级旧协议。
5. 需要回滚时，停止新进程，同时还原旧二进制和相应备份数据目录。不要把高版本数据库强行交给旧二进制。

## 一致性备份

最简单可靠的方式是**停止 Server 后**复制整个数据目录，再启动服务。不能只复制运行中的 `tza-v2.db`；WAL 中可能有未合并事务。如果以后需要不停机备份，可单独实现 SQLite Backup API 适配器，不能用文件复制替代。

备份包含管理密钥和 Webhook 地址，应维持私有权限。恢复演练应在单独目录和端口进行，避免旧版或生产库被意外打开。

## 后台生成 Agent 安装脚本

添加节点或更换节点密钥后，接入窗口可下载配置及 `install-agent.sh`。将自行构建的、与目标 Linux 架构相符的 `tza-agent` 二进制和脚本放在同一目录，按需修改脚本内网卡、挂载点和 NextTrace 绝对路径，然后运行 `sudo sh install-agent.sh`。脚本安装本地二进制、私有配置与 systemd 服务，不下载程序、不从服务端接收任意命令。

脚本包含节点密钥，安装后删除副本。生产环境接入地址使用 HTTPS；从 HTTP 面板生成的配置会显式开启 HTTP，部署到公网前应改为 HTTPS 并关闭 `allow_http`。NextTrace 仍需单独安装。后台节点列表显示 Agent 版本、架构和最后上报时间。
