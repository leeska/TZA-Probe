# 地区偏移检测

检查节点的公网出口在 Google、Meta 眼里属于哪个地区（例如香港 VPS 是否被 Google 认成中国大陆「送中」），
与你设定的目标地区比较；可选在偏移时尝试一次校正并独立复测。这是 Server 的内置功能，默认不安装。

页面地区是 Google / Meta 对一个匿名会话给出的判断，不等于 IP 注册库、账户地区或信誉；
「正常」只表示没发现页面地区异常，「校正完成」也不等于已经恢复。

## 结构

```text
Server（排期、判定、存档）── 私有 Unix socket ── tza-browser（Chromium，无网络）
   │                                                    │ 每条连接都绑定任务租约
   └── 已鉴权的反向隧道 ── 节点 Agent ── 只允许 Google / Meta / 出口回显的 HTTPS ──► 目标站点
```

- **浏览器** 是 `tza-server browser-worker`，与 Server 同一个二进制、同一次发布。Chromium 是固定版本的
  Chrome for Testing headless-shell（linux64 / linux-arm64，下载后校验 SHA-256），装在 `/opt/tza-browser`。
- 浏览器服务 `tza-browser` 以独立用户运行，systemd `PrivateNetwork=yes`：只有回环网卡，没有任何直连出口。
  它对外的唯一通道是 Server 的私有 socket（`/run/tza-browser/control.sock`，只有 Server 与浏览器两个服务账户所在的
  `tza-browser-control` 组能连），Server 再经节点 Agent 主动建立的隧道转发。隧道断了任务就失败，没有回退路径。
- **Agent** 只在本机许可文件开通后才转发：在 Agent 端解析 DNS 并要求全部结果是公网地址、固定实际连接的地址、
  锁定地址族、只连 443，单次任务限 120 秒与 20 MiB，许可文件一改立即生效。
- **判定在 Server**：浏览器只上报读到的原始信号，Server 核对沙箱、网络隔离、前后出口 IP、页面来源与流量后
  才给出「正常 / 地区偏移」，不采信浏览器自己的结论。

## 安装（Server 主机）

```sh
curl -fsSL https://raw.githubusercontent.com/leeska/TZA-Probe/main/deploy/install.sh | sudo sh -s -- --with-browser
```

脚本在正常安装 / 升级 Server 之外：

1. 建用户 `tza-browser` 与组 `tza-browser-control`；
2. `tza-server browser-install` 下载并校验 Chromium，列出缺少的系统库与字体，用 apt 装上
   （其他发行版会列出缺什么、请你装好后重跑）；
3. Chromium 的沙箱需要非特权用户命名空间：Ubuntu 23.10 起由 AppArmor 限制，脚本写入只放行这一个可执行文件的
   `/etc/apparmor.d/tza-browser-chromium`；内核整体关闭 userns 时脚本停下并说明，不替你改内核参数；
4. 在与正式服务相同的隔离里运行 `browser-worker --self-test`：必须只有回环网卡、非 root、有字体、
   每个渲染进程都有 seccomp 过滤且在独立的 PID 命名空间里，通过后才启用；
5. 安装 `tza-browser.service`，给 Server 加上 `--browser-socket /run/tza-browser/control.sock`。

装过一次后，再执行安装脚本（不带 `--with-browser`）也会保留并一起升级浏览器。资源：约 300 MB 内存、
200 MB 磁盘；服务单元限 `MemoryMax=700M`，可用 drop-in 调整。Docker 部署暂不支持浏览器。

没有安装浏览器时，后台「节点管理 → 地区偏移」弹窗只显示安装说明。

## 开通节点出口

节点默认不允许浏览器经它访问外部网站。在节点上执行（后台该页会给出带本机地址的完整命令）：

```sh
curl -fsSL https://probe.example.com/install.sh | sh -s -- --endpoint https://probe.example.com --ipguard
```

只更新 Agent 并写入 `/etc/tza-probe/ipguard-egress.json`（0600），不改其他配置。不写 IP 时经
`api.ipify.org` / `api6.ipify.org` 查本机公网出口；NAT 主机（本机源地址与公网出口不同）自动写好 `source_ips`。
也可以直接指定：`--ipguard 203.0.113.7,2001:db8::7`。关闭：`--ipguard-off`。

## 检测与判定

后台选节点、目标地区与出口地址族（IPv4 / IPv6，只能从 Agent 报告的已授权出口里选），勾选 Google / Meta，保存后生效。
语言与时区按目标地区自动匹配。开启后每 12 小时检查一次，节点离线时跳过，恢复后补做。

- **Google 搜索地区**：读中立首页可见页脚里的地区名称（按页面语言、浏览器语言和英语的地区名称表认，
  只接受可见、非链接、完整匹配的文字）；首页没有时补读一次中立搜索页 `search?q=Google`。
  不设置 gl、uule 等参数，不用坐标，不绕过验证；多个地区冲突、遇到验证页或读不到时为未知。
- **Google 条款地区**：同一次检测先读 `https://policies.google.com/terms` 的国家版本，与搜索地区分开显示，不参与自动校正。
- **Meta**：读广告资料库默认国家，页面数据与可见的国家选择器必须一致。
- 实际地区等于目标为「正常」，不等为「地区偏移」；未知、拒绝访问、验证页、过期、失败、中断和取消都不当作正常。

每次检测都是全新的临时会话，检测前后各核对一次出口 IP（`api64.ipify.org`），与设置不一致即失败。

## 自动校正

开启「偏移时尝试自动校正」后，只有一次干净检测确认偏移，才尝试一次校正：用为该节点、服务和设置单独保留的浏览器配置，
可选注入你选的定位坐标（只授权给 Google / Meta 主站）并停留一段时间；随后再做一次独立的干净复测。
复测仍偏移不循环重试，下个周期照常检测。校正不改变出口 IP、服务商数据库或账户地区。

目标定位可以按出口 IP 近似定位（ipwho.is）、搜索地点（Photon）或手填经纬度（OpenStreetMap Nominatim 与 TimeAPI 核对地区和时区）；
这些查询只在管理员操作时由 Server 发出，地图瓦片来自 tile.openstreetmap.org。

## 故障排查

| 现象 | 检查 |
|---|---|
| 页面提示「先安装检测浏览器」 | Server 没带 `--browser-socket` 启动：用 `--with-browser` 重跑安装脚本 |
| 「检测浏览器未连接」 | `systemctl status tza-browser`、`journalctl -u tza-browser -n 50` |
| 自检 `sandbox: false` / `chromium did not start: No usable sandbox` | 用户命名空间被禁用：见上文第 3 步 |
| 自检 `fonts: false` | 装 `fontconfig fonts-dejavu-core`；没有字体时 Chromium 遇到网页字体会卡住 |
| 自检 `network_isolated: false` | 浏览器没在 `PrivateNetwork=yes` 下运行（只允许 down 且无地址的内核隧道设备） |
| 节点一直「出口未连接」 | 节点上执行 `--ipguard` 命令；确认 Agent 在线 |
| 结果 `egress_mismatch` | 节点出口变了：重新执行 `--ipguard`；出口上报后一分钟内自动换成新出口 |
| 结果「遇到验证页面」 | 该出口被 Google 限流，等待后再测；不触发校正 |

## 验证

- `internal/browserworker`：信号提取（逐条移植自原 Python 版的测试）、网络隔离判定、`/proc` 沙箱证据、启动参数、
  隧道白名单、结果投递与崩溃恢复、解压防护。
- `internal/ipguard`：排期、自动校正链、去重、证据判定。
- `tools/browser-e2e`：真实环境验收。它在真实的 `ipguard` 数据库上提供 worker socket（Agent 换成上游代理），
  把浏览器放进无网络的沙箱里跑一次 Google 与 Meta 检测，输出 Server 判定后的记录。
