# 独立前台主题

从 Probe 1.5.0 起，主题可以完全拥有公开前台，而不是覆盖核心卡片。独立主题自行决定 HTML、CSS、导航、页面、组件、交互和数据刷新；不需要核心 DOM、CSS 变量或 `data-part`。可以使用原生 JS，也可以把 React、Vue、Svelte 等框架构建为静态资源后打包。

后台仍使用核心管理界面。CSS 主题、附加包和已有主题的安装方式不变；独立前台也出现在同一外观菜单里，能设为默认外观、上传更新或从市场安装。

## 最小主题

```text
my-theme/
  theme.json
  index.html
  main.js
  screen.css
  assets/…
```

```json
{
  "id": "my-theme",
  "name": "我的前台",
  "version": "1.0.0",
  "chrome": {"light": "#ffffff", "dark": "#101b29"},
  "swatch": ["#101b29", "#1d3146", "#5ee1b1"],
  "frontend": {"entry": "index.html", "api_version": 1}
}
```

`entry` 是包内 HTML 相对路径，必须存在；`api_version` 当前为 `1`。不带 `frontend` 的包继续按 CSS 主题校验。独立前台不声明 `base` / `layout`，这些是核心组件的布局选项；`styles` 可以省略，资源由主题自己引用。可以声明 `settings` 和 `decorations`，通过 SDK 获取后自行使用；核心不会向独立文档强塞装饰层或设置控件。

`index.html`：

```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1">
    <link rel="stylesheet" href="screen.css">
    <script src="/theme-sdk.js"></script>
    <script type="module" src="main.js"></script>
  </head>
  <body><main id="my-dashboard"></main></body>
</html>
```

`main.js`：

```js
const context = await TZA.ready;
const nodes = await TZA.api('/nodes');
const main = document.querySelector('#my-dashboard');
for (const node of nodes) {
  const row = document.createElement('p');
  row.textContent = `${node.name} · ${node.online ? '在线' : '离线'}`;
  main.append(row);
}
```

安装包包含构建结果和资源，不包含 `node_modules`。使用相对资源基址（例如 Vite 的 `base: './'`），将代码分块、字体、图片、JSON、SVG 或 WASM 一并放进包。ES modules、动态 import、内联脚本/样式和 CSS-in-JS 都在独立文档内运行。包内 JSON 可用相对 URL `fetch`；不要从 CDN 拉依赖。

打包与验证：

```sh
go run ./tools/themepack -out dist/themes examples/themes/observatory
```

将生成的 zip 上传后台「主题」，选中它或设置为默认外观。开发文档 `README.md` 不进入 zip。市场上传也使用同一个 zip；CC 会将独立前台的最低兼容范围设为 `>=1.5.0`，不接受包含旧 Probe 的范围。审核和安装页会展示独立 HTML / JavaScript 及只读公开数据说明。

## SDK v1

SDK 是 `/theme-sdk.js` 提供的经典脚本，暴露 `window.TZA`；可在任意框架入口之前加载。TypeScript 声明见 [frontend-api.d.ts](frontend-api.d.ts)。

| API | 行为 |
| --- | --- |
| `TZA.ready` | Promise，连接宿主后返回公开上下文。 |
| `TZA.context` | 最近一次上下文。 |
| `TZA.api(path)` | GET 公开 API，路径如 `/nodes`，不带 `/api/v1` 前缀；返回解析后的 JSON。失败时 Promise reject。 |
| `TZA.onContext(fn)` | 立即回调已有上下文，之后在路由或明暗变化时回调；返回取消订阅函数。 |
| `TZA.navigate(path)` | 更新主页面地址及浏览器历史，并推送新上下文，主题自行渲染路由。支持查询参数与 hash。 |
| `TZA.setSkin(id)` | 使用已安装的另一外观；跨渲染模式时重载，旧文档和计时器随之销毁。 |
| `TZA.setMode('light' / 'dark' / 'auto')` | 保存访客明暗偏好；系统变化时也更新上下文和文档的 `data-theme`。 |
| `TZA.openAdmin()` | 前往核心后台入口。 |
| `TZA.storage.get(key)` / `.set(key,value)` | 异步读写此主题独立命名空间中的 JSON 偏好；`null` 删除。键最多 64 位字母数字、`_ . -`，单值最多 32 KB。 |
| `TZA.reportError(error)` | 显示宿主的恢复入口。脚本异常、未处理的 Promise 错误会自动报告。 |

上下文：

```js
{
  api_version: 1,
  theme: { id, name, frontend, settings, options, decorations, /* 清单字段 */ },
  themes: [ /* 内置、已安装主题及附加包清单 */ ],
  brand: { name, logo },
  mode: 'light',             // 解析后的明暗
  preference: 'auto',        // 访客偏好
  route: '/node/123?view=cpu', // 主页面 pathname + search + hash
  adminURL: '/admin'
}
```

`theme.options` 包含设置的当前值或清单默认值。上下文与节点数据分开，主题自行选择刷新周期；可以轮询 `/nodes/live` 更新实时字段，较低频率获取 `/nodes` 的配置及解锁结果。公开数据的字段见 [API.md](API.md)。删除、更新包或管理员修改设置后，刷新页面读取最新清单。

可调用的公开路径：

- `/nodes`、`/nodes/live`
- `/nodes/{id}/history`、`/nodes/{id}/network-history`、`/nodes/{id}/observations`
- `/observations`、`/latency-samples`、`/unlock-catalog`

保留服务端原有查询参数，如 `hours=24`、`slim=1`。每个独立前台最多 8 个并发桥接请求，单个请求 15 秒超时。桥接不接受任意 URL、请求头、HTTP 方法或后台路径。

## 路由和生命周期

独立文档内的 `location` 属于主题静态资源。使用 `TZA.context.route` 作为业务路由；例如 React 使用 memory router 并与 `onContext` / `navigate` 同步，或直接使用 hash 路由。不要把资源路径误当作主页面路由。

主页面支持深链接刷新与浏览器前进后退。`/admin`、`/api`、`/themes`、`/dl`、`/mcp` 和健康接口由核心保留；主题不覆盖这些服务入口。正常路径中的所有内容由主题自行渲染。离开主题时使用 `pagehide` 停止自己创建的轮询或订阅。

入口加载失败、SDK 无法连接或脚本运行失败时，宿主提供「使用默认前台」和后台管理入口。即使主题损坏，访问 `/?tza-theme=shadcn` 也能恢复默认前台；`/admin?tza-theme=shadcn` 始终能管理主题。`?tza-theme=<已安装 id>` 也可用于预览。

## 执行边界

独立文档使用 `sandbox="allow-scripts"`，不授予 `allow-same-origin`。HTTP 响应也附带 CSP sandbox，直接打开 HTML 或 SVG 同样不继承站点权限。主题不能读取宿主 DOM、Cookie、后台会话或宿主 localStorage；偏好通过主题命名空间保存。主题脚本不在核心后台执行。

只有本包的静态资源允许直接 fetch，网络监控数据通过宿主的公开桥接读取。宿主省略 Cookie / Authorization，即使访客同时登录了后台，也只返回匿名公开范围，私有节点和私有历史不会泄露。主题不运行特权探测，不需要后台 Key。外部连接、嵌入页面、表单提交、弹窗和顶层导航不开放；需要跳转后台或切换外观时用 SDK。

隔离的是权限和运行环境，页面内容完全由开发者决定。主题不再依赖核心卡片结构、布局钩子或组件渲染函数。

## 示例与验收

[`examples/themes/observatory`](../examples/themes/observatory) 展示了全自定义侧栏、数据表、搜索排序、详情路由、独立 SVG 图表、动态模块分块、JSON 配置、明暗切换和偏好存储。

真实浏览器检查使用一次性本地 Server，覆盖已登录后台时的公开数据边界、DOM / storage 隔离、静态资源加载、深链接刷新/后退、后台不加载主题、主题切换和错误恢复：

```sh
# playwright 安装在单独的依赖目录，TZA_BROWSER_DEPS 指向其 package.json 所在目录。
TZA_BROWSER_DEPS=/tmp/tza-browser-deps \
TZA_TEST_URL=http://127.0.0.1:25995 \
TZA_TEST_ADMIN_FILE=/tmp/frontend-data/admin-token \
TZA_TEST_CREDENTIALS_FILE=/tmp/frontend-credentials \
bun scripts/frontend-browser-test.mjs
```
