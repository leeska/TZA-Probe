# 插件

插件是**声明式**的：一个只含 `plugin.json` 的 zip，里面是数据和模板，由 Probe 解释执行，不含任何代码。它能做的事被格式本身限定。安装前，后台会把插件能做什么逐条列出，由你确认。

目前有两类：

| kind | 作用 |
| --- | --- |
| `notify` | 通知渠道：把告警发到飞书、钉钉、企业微信、Bark、ntfy 这类服务。用一个 HTTP 请求模板描述发送方式 |
| `targets` | 探测目标集：给任务表单的目标目录添加一批地区 / 运营商目标，不会自动建任务 |

插件可以从 TZA-CC 市场安装（经过审核和签名），也可以在后台「插件 → 已安装 → 上传插件」手动上传。两种方式都先列出插件能做的事（发往哪些域名、收到什么内容，或添加哪些目标），确认后才安装。格式校验与主题包共用 `pkg/tzapkg`，TZA-CC 审核时用的是同一份代码。

## 包

```
feishu.zip
└── feishu/            # 可以有一层顶层目录
    └── plugin.json    # 唯一的文件
```

```json
{
  "id": "feishu",
  "type": "plugin",
  "kind": "notify",
  "name": "飞书机器人",
  "hint": "群机器人 Webhook，支持签名校验",
  "version": "1.0.0",
  "author": "TZA",
  "notify": { … }
}
```

`id`、`name`、`hint`、`version`、`author` 的规则与主题包相同（见 [THEMES.md](THEMES.md)）。`id` 不能用 `telegram` / `webhook` / `none` 和内置皮肤名。**清单里出现未知字段会被拒收**，拼错的字段不会被悄悄忽略。

## 通知渠道（kind: notify）

```json
"notify": {
  "hosts": ["open.feishu.cn", "open.larksuite.com"],
  "settings": [
    { "key": "webhook", "label": "Webhook 地址", "type": "url", "required": true, "secret": true },
    { "key": "secret", "label": "签名校验密钥", "type": "secret", "help": "机器人开启了签名校验时填写" }
  ],
  "computed": [
    { "name": "ts", "value": "{{now.unix}}" },
    { "name": "sign", "hmac_sha256": { "key": "{{computed.ts}}\n{{settings.secret}}", "message": "" }, "when": "settings.secret" },
    { "name": "signed_at", "value": "{{computed.ts}}", "when": "settings.secret" }
  ],
  "request": {
    "method": "POST",
    "url": "{{settings.webhook}}",
    "body": {
      "msg_type": "text",
      "content": { "text": "{{event.text}}" },
      "timestamp?": "{{computed.signed_at}}",
      "sign?": "{{computed.sign}}"
    }
  },
  "success": { "status": [200, 299], "json": { "path": "code", "equals": 0 } }
}
```

### hosts：能发往哪里

- 列出域名（最多 20 个，`*.example.com` 表示子域）时，请求只能发往这些域名，并且必须是 HTTPS。`url` 类型的设置在保存时就核对域名，模板渲染出的最终地址发送前还会再核对一次。
- `["*"]` 表示发往管理员自己填写的地址（自建的 ntfy、Gotify 等），可以是 HTTP。这时至少要有一项 `type` 为 `url` 的设置。

### settings：后台表单

| 字段 | 说明 |
| --- | --- |
| `key` | 小写字母开头的字母、数字、下划线，模板里用 `{{settings.<key>}}` 引用 |
| `label` / `help` / `placeholder` | 表单文字 |
| `type` | `text`、`secret`（密码框）、`url`、`select`（配 `options: [{value, label}]`） |
| `required` | 必填 |
| `secret` | 保存后不回显，留空表示沿用已存的值。`type: secret` 自动带这一项 |
| `default` | 留空时用的值（密钥类不能有默认值） |
| `pattern` | 整串匹配的正则（RE2） |

最多 12 项。

### 模板

字符串里的 `{{ 变量 | 过滤器 | … }}` 会被替换，除此之外没有任何逻辑：不能循环，不能调用函数，只能读下面这些变量。

| 变量 | 内容 |
| --- | --- |
| `event.title` | 一行标题，如 `TZA Probe · 告警触发 · CPU 使用率 · 东京` |
| `event.text` | 完整的多行正文（状态、类型、节点、读数、时间、事件编号），与 Telegram 消息内容一致（测试消息不带「通道」一行；超过约 8000 字时截断读数部分） |
| `event.message` | 只有读数与条件那一段 |
| `event.status` | `告警触发` / `告警解除` / `连接测试` |
| `event.kind` / `event.kind_name` | 类型代号（`offline`、`cpu`…）与中文名 |
| `event.node` / `event.node_id` | 节点名与 ID |
| `event.time` / `event.timestamp` | `2026-10-15 13:00:00 UTC` 与 Unix 秒 |
| `event.id` | 事件编号，接收端去重用；测试消息没有编号，为空 |
| `event.active` | `true`（触发、测试）或 `false`（解除） |
| `event.urgent` | 触发时为 `1`，解除和测试时为空，配合 `when` / `unless` 使用 |
| `event.priority` | `4` 触发、`3` 解除、`2` 测试（ntfy 的 1–5 级） |
| `settings.*` | 管理员填写的设置 |
| `computed.*` | 下面 `computed` 里算出的值 |
| `now.unix` / `now.unix_ms` / `now.iso` | 发送时刻 |

过滤器：`urlencode`（查询参数）、`urlpath`（路径段）、`base64`、`upper`、`lower`、`trim`、`single_line`、`truncate:N`（按字符截断，末尾加 `…`），以及只能放在最后的 `number` / `bool`：整个 JSON 字符串只有一个变量并以它结尾时，输出数字或布尔值而不是字符串，例如 `"priority": "{{event.priority|number}}"`。

### computed：派生值

按顺序计算，后面的可以引用前面的：

- `value`：一个模板。
- `hmac_sha256: { key, message }`：HMAC-SHA256，两个字段都是模板。
- `sha256`：对模板结果求 SHA-256。
- `encode`：摘要的编码，`base64`（默认）、`base64url` 或 `hex`。
- `when` / `unless`：条件，取 `settings.x`、`event.x` 或 `computed.x`，非空 / 为空时这一项才生效。不生效时值为空字符串。同名的后一项生效时覆盖前一项，可以用来写分支。

### request：请求

| 字段 | 说明 |
| --- | --- |
| `method` | `POST`（默认）、`GET`、`PUT` |
| `url` | 模板 |
| `headers` | 名字到模板的对象。不能设置 Host、Content-Length、Cookie 等由传输层管理的头 |
| `body_type` | `json`（默认）、`form`（`application/x-www-form-urlencoded`）、`text` 或 `none` |
| `body` | `json`：任意 JSON，字符串叶子都是模板，数字和布尔原样发送；`form`：字符串到模板的对象；`text`：一个模板字符串 |

`headers` 和 `body` 对象里以 `?` 结尾的键是**可选成员**：渲染为空时整项省略（比如只在填了签名密钥时才带 `sign`）。变量替换发生在 JSON 字符串内部，再整体序列化，节点名里有引号也不会破坏请求体。

请求体最大 64 KB，超时 5 秒，不跟随重定向。

### success：判断成功

- `status`：`[最小, 最大]`，默认 `[200, 299]`。
- `json`：回应体按 `path`（点分）取值后必须等于 `equals`；`equals` 是数组时等于其中任意一个即可。不少服务出错时也回 HTTP 200，只在 `errcode`、`code` 里说明，所以要靠这一项判断。

失败按 Probe 原有规则指数退避重试，最多 10 次。失败原因记录在事件里，回应体不会原样展示。

### 在 Probe 里使用

装好的通知插件会出现在「告警与通知 → 配置出口」的渠道列表里，与 Telegram、Webhook 并列。同一时间仍只使用一个渠道。密钥类设置保存后不回显，再次保存时留空即沿用。正被用作渠道的插件不能删除，需要先切换渠道。MCP 的 `update_settings` 用 `channel: "plugin"`、`plugin: "<id>"`、`plugin_settings: {…}` 选择它，`list_plugins` 列出已装插件和它们的设置项。

插件升级后不再接受已存的设置、或 plugin.json 因故失效时，投递失败的原因会写明是哪个插件、该做什么，配置出口也会提示，通知不会被悄悄关掉。

## 探测目标集（kind: targets）

```json
{
  "id": "jp-targets", "type": "plugin", "kind": "targets", "name": "日本常用目标", "version": "1.0.0",
  "targets": [
    { "id": "tyo-iij-v4", "family": "ipv4", "region": "东京", "carrier": "其他", "host": "203.0.113.10", "port": 443 }
  ]
}
```

| 字段 | 说明 |
| --- | --- |
| `id` | 包内唯一，小写字母、数字、连字符 |
| `family` | `ipv4` / `ipv6`；`host` 是 IP 字面量时必须与它一致 |
| `region` | 地区名，最多 30 字 |
| `carrier` | `电信`、`联通`、`移动`、`其他` |
| `host` | IP 地址或域名 |
| `port` | 1–65535 |

最多 500 个。装好后，这些目标出现在任务表单的目录检索里，标出来自哪个插件，用法与内置目录相同。删除插件只影响目录，已经建好的任务照常运行。目标集给的地址只是候选，**不代表可达**，部署后应在实际节点上验证。
