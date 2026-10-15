# MCP 接入指南

TZA Probe 在 `POST /mcp` 提供 MCP 接口，AI 客户端可查询节点、探测结果和告警，也可管理节点、任务、提醒规则及通知和面板设置，从 TZA-CC 市场搜索、安装主题包与插件，并把通知插件设为告警渠道。服务端与后台共用业务校验，写入会立即反映到面板。

## 1. 生成 API Key

1. 使用用户名密码登录 `/admin`。
2. 打开 **面板设置 → MCP API Key**，点击 **生成 API Key**。
3. 复制并保存完整 Key，格式为 `tza_mcp_…`。Key 只显示一次，关闭后无法再次查看。
4. 记下同一区块中的连接地址，例如 `https://probe.example.com/mcp`。

安装、升级脚本不会生成或打印 MCP Key。首次生成前，MCP 请求返回 HTTP 401。数据库只保存 Key 的 SHA-256 摘要，服务重启后 Key 仍有效。

当前每个面板只有一个有效 MCP Key。点击 **重新生成** 会立即使旧 Key 失效，所有使用它的客户端都需要更新；点击 **撤销** 会停用 MCP 访问，之后可再生成新 Key。

## 2. 配置客户端

客户端需要支持远程 HTTP MCP、自定义请求头和本服务支持的协议版本。填写：

| 配置项 | 值 |
| --- | --- |
| 名称 | `tza-probe`，也可自定义 |
| 服务地址 | `https://probe.example.com/mcp` |
| 传输 | HTTP / Streamable HTTP |
| 请求头名 | `Authorization` |
| 请求头值 | `Bearer tza_mcp_…`，替换为完整 Key |

若客户端接受 `mcpServers`、`type`、`url`、`headers` 这种 JSON 配置结构，可使用下面的示例。不同客户端的字段、文件位置和添加入口可能不同，请按客户端的配置格式填写上述地址与请求头。

```json
{
  "mcpServers": {
    "tza-probe": {
      "type": "http",
      "url": "https://probe.example.com/mcp",
      "headers": {
        "Authorization": "Bearer REPLACE_WITH_MCP_API_KEY"
      }
    }
  }
}
```

将域名换成自己的面板地址，将 `REPLACE_WITH_MCP_API_KEY` 换成完整 Key，保留前面的 `Bearer `。远程连接使用 HTTPS；反代配置见 [部署说明](DEPLOYMENT.md)。

地址是 `/mcp`，不带 `/api/v1` 前缀，也没有单独的 `/sse` 地址。用浏览器直接打开地址会发送 GET 并得到 405，这不代表服务不可用。若客户端只能填写 URL、不能携带自定义认证头，无法直接连接当前端点；本服务没有 OAuth 登录流程。

连接成功后应能列出 **28 个工具**。可以先让客户端执行只读查询，例如：“列出所有节点，告诉我哪些离线”或“查看当前正在告警的项目”。

## 3. 权限与凭据

MCP Key 具有下表中全部工具的权限，包含写入与删除；当前没有只读 Key、按节点授权或多个独立 Key。工具的 `readOnlyHint` / `destructiveHint` 是客户端提示，不是权限限制。写入不会在服务端等待人工审批，请在客户端配置写操作确认。

| 凭据 | 用途 |
| --- | --- |
| 后台用户名密码、浏览器 Cookie | 登录管理后台；Cookie 不能访问 MCP |
| MCP API Key | 仅访问 `/mcp`，不能登录后台或访问管理 HTTP API |
| 节点密钥 | Agent 上报、领取和完成任务，不能访问 MCP |
| 旧 `admin-token` / `TZA_ADMIN_TOKEN` | 兼容管理 HTTP API，不用于后台登录或 MCP |

从旧版升级后，如果客户端原来使用管理密钥或面板 Cookie，请在后台生成 MCP Key，再更新客户端的 `Authorization` 请求头。忘记后台密码可在服务器执行 `tza-server reset-password --data 数据目录`；忘记 MCP Key 则在后台重新生成。

## 4. 工具清单

具体参数、必填项和枚举由 `tools/list` 返回的 `inputSchema` 给出。节点、任务和提醒规则的数据结构见 [HTTP API](API.md)。

| 工具 | 用途 |
| --- | --- |
| `list_nodes` | 查询全部节点、运行状态、计费与生效提醒规则 |
| `list_tasks` | 查询网络探测和解锁任务、启停状态与绑定节点 |
| `node_history` | 查询指定节点的主机历史，`node_id` 必填，可传 `hours` |
| `node_network_history` | 查询指定节点的网络历史，`node_id` 必填，可传 `hours` |
| `node_observations` | 查询指定节点的探测结论与脱敏回程跳表 |
| `recent_events` | 查询事件，可按节点、类型、时间分页；`active: true` 只看当前告警 |
| `latency_samples` | 查询各节点、运营商、协议、地区的逐次 TCP 延迟样本 |
| `list_alert_rules` | 查询提醒规则及其范围、阈值和间隔 |
| `list_catalog` | 查询目标目录（内置 + 已装目标集插件），可筛选地区、运营商与协议 |
| `task_runs` | 查询指定任务的领取、完成时间和失败原因 |
| `get_settings` | 查询通知渠道状态与公开面板显示项，不回显通知凭据 |
| `create_node` | 新建节点，返回 ID、一次性节点密钥及 Agent 接入命令 |
| `update_node` | 按字段修改节点，`node_id` 必填 |
| `delete_node` | 删除节点、令牌及绑定，`node_id` 必填 |
| `rotate_node_token` | 轮换节点密钥，旧密钥立即失效，返回新密钥与接入命令 |
| `correct_node_traffic` | 校正本周期已用流量，提交 `node_id`、字节数 `used` 和周期起点 `cycle` |
| `create_alert_rule` | 新建一条提醒规则 |
| `update_alert_rule` | 按字段修改提醒规则，`rule_id` 必填 |
| `delete_alert_rule` | 删除提醒规则，`rule_id` 必填 |
| `create_task` | 新建 `probe` 网络探测或 `unlock` 解锁任务 |
| `update_task` | 按字段修改任务，`task_id` 必填 |
| `delete_task` | 删除任务及结果，`task_id` 必填 |
| `control_task` | 暂停、恢复、立即执行或替换节点绑定 |
| `update_settings` | 按字段修改通知渠道（含 `channel: "plugin"` + `plugin` + `plugin_settings`）或公开面板显示项 |
| `test_notification` | 向已配置的通知渠道发送测试消息 |
| `list_plugins` | 只读：已安装的插件，`kind`（notify / targets）、来源、`permissions`，通知插件带设置表单 `settings`（填 `update_settings` 的 `plugin_settings` 用） |
| `search_market` | 只读：搜索 TZA-CC 市场（`type` = `skin` / `characters` / `plugin`、`q`、`page`；插件带 `kind` 与 `permissions`），每项标出本机已装的版本与来源；传 `id` 看详情与版本列表（含 `compatible`），传 `updates: true` 只列有兼容新版的市场包 |
| `install_market_package` | 从市场安装或更新主题包 / 角色包 / 插件（插件请先把 `search_market` 返回的 `permissions` 给用户确认），`id` 必填，`version` 省略即当前 Probe 能用的最新版；验签不过整包拒收。会替换同 id 的已装包 |

常用操作规则：

- `update_*` 只修改提交的字段，嵌套对象按字段合并，列表整份替换。先查询对象 ID 和当前配置，再提交修改。
- `control_task` 的 `action` 可取 `pause`、`resume`、`run`、`bind`；`bind` 同时提交 `node_ids`。空列表表示不绑定任何节点，停止调度，不代表全部节点。`run` 只是安排任务，实际执行要等 Agent 领取。
- 提醒规则 `scope` 可取 `all`、`group`、`nodes`；同类型优先级为指定节点 > 分组 > 全部节点。同类型、同范围覆盖同一节点的重复规则会被拒绝。
- `update_settings` 不传通知项时不会改通知配置；关闭通知须显式传 `channel: "none"`。通知 Token / Webhook 不在查询结果中回显。
- 通知插件：先 `list_plugins` 看 `settings`，再 `update_settings` 传 `{"channel":"plugin","plugin":"ntfy","plugin_settings":{"topic":"…"}}`。设置不符合插件要求（必填、格式、域名）返回 `-32602`。之后只改某一项时只传那一项（`plugin_settings` 里没传或留空的沿用已存值，同一个插件时）。密钥类设置从不回显，`get_settings` 只在 `plugin_secrets` 里说明哪些已保存；插件被删或失效时回 `plugin_error`。`list_catalog` 的结果包含已装目标集插件的目标（带 `plugin`）。
- 回程标签按实际观测命名，跳地址已脱敏；未知标签或超时不能当成已确认线路。
- 两个市场工具默认连官方 TZA-CC；Server 用 `--cc-url off` 关掉市场时返回 `isError: true` 与「市场已关闭」；它们会访问外部服务，`annotations.openWorldHint` 为 true。类型、版本号等参数不合法返回 `-32602`；包不存在、CC 不可达、安装包未通过校验按工具错误返回原因。没有绑定、解绑工具：账号绑定要在浏览器里登录 TZA-CC，请在后台「市场」页操作。

MCP 不提供后台账号修改、MCP Key 管理、数据库备份、TZA-CC 账号绑定、远程终端或任意命令执行工具。账号、Key 和备份在后台操作；`resources/list` 与 `prompts/list` 返回空列表。

## 5. 用 curl 检查连接

下面使用服务端支持的 `2025-11-25` 协议。将域名换成自己的面板地址，在 Bash / Zsh 中执行。Key 用隐藏输入读取，不放进 URL。

```sh
MCP_URL='https://probe.example.com/mcp'
printf 'MCP API Key: '
read -r -s MCP_API_KEY
printf '\n'

# 初始化：应返回 protocolVersion、serverInfo 和 tools 能力。
curl -sS -i "$MCP_URL" \
  -H "Authorization: Bearer $MCP_API_KEY" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl-check","version":"1.0"}}}'

# 查询工具清单：应返回 28 个工具。
curl -sS "$MCP_URL" \
  -H "Authorization: Bearer $MCP_API_KEY" \
  -H 'Content-Type: application/json' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  --data '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# 只读查询节点。
curl -sS "$MCP_URL" \
  -H "Authorization: Bearer $MCP_API_KEY" \
  -H 'Content-Type: application/json' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  --data '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_nodes","arguments":{}}}'

unset MCP_API_KEY
```

端点无会话状态，不需要保存 `Mcp-Session-Id`。工具结果在 `result.content` 中，成功结果还提供 `structuredContent`；工具执行失败可能返回 HTTP 200 和 `result.isError: true`，不能只看 HTTP 状态判断成功。

服务端也支持 `2025-06-18`，以及 `2026-07-28` 的逐请求元数据模式。后者需要 `_meta` 与版本、方法、工具名请求头匹配；自行实现客户端时按 [协议说明](API.md) 发送，不要只替换上述示例的版本头。

## 6. 常见错误

| 现象 | 检查与处理 |
| --- | --- |
| HTTP 401 | Key 尚未生成、已撤销或被重新生成；是否用了旧管理密钥；请求头是否为 `Authorization: Bearer 完整Key`；反代是否保留认证头 |
| HTTP 403 | 请求携带的 `Origin` 与服务端看到的 Host 不一致；检查客户端来源与反代的 Host 转发 |
| HTTP 404 | 确认地址为 `/mcp`；查看 JSON-RPC 是否报告未知方法 |
| HTTP 405 | 客户端发送了 GET / DELETE；端点只接受 POST，不提供长期 GET SSE 流 |
| HTTP 415 | 设置 `Content-Type: application/json` |
| JSON-RPC `-32020` | `2026-07-28` 模式下，请求头与请求体元数据缺失或不匹配 |
| JSON-RPC `-32022` | 协议版本不受支持；查看错误中的 `supported` 列表 |
| JSON-RPC `-32602` | 未知工具、未知参数、必填字段缺失或参数不合法；按 `tools/list` 的 schema 检查 |
| `result.isError: true` | 读取 `content` 中的原因，例如对象不存在、规则冲突或通知发送失败 |
| 工具列表里没有账号、Key 或备份工具 | 当前没有这些 MCP 工具，请到后台操作 |

恢复连接后先执行 `tools/list` 或 `list_nodes` 确认认证和协议正常，再尝试写操作。
