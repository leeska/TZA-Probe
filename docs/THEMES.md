# 主题包与附加包

面板的外观风格（右上角那颗调色盘按钮）由两部分组成：

- **内置皮肤**：只有现代极简（默认），打包在程序里。
- **主题包**：后台「主题」页从 TZA-CC 市场安装（见[市场](#市场tza-cc)），或上传 zip；存在数据目录的 `themes/` 下，不用重新编译。
- **附加包**：依附某一套主题的扩展包（`"type": "addon"`），给它添更多按节点挑选的装饰项，见[附加包](#附加包)。
  官方的液态玻璃、复古印刷、二次元都是主题包，源码在仓库的 [`themes/`](../themes)（各自的用法见包目录里的 README），`go run ./tools/themepack themes/*` 打成 zip（发行包里也带一份 `themes/*.zip`，不连市场时上传它们）。

主题包有两种模式：**CSS 主题**沿用核心组件，**独立前台**用自己的 HTML / JavaScript 完全定义公开页的结构、布局、交互和路由。
独立前台从 Probe 1.5.0 起支持，详见 [独立前台开发](FRONTENDS.md)；示例是 [`observatory`](../examples/themes/observatory)。后台管理始终由核心提供。

装好的主题包和内置皮肤一样出现在外观菜单里，也能在「面板设置 → 外观与品牌 → 默认外观」里设成访客的首屏皮肤。没配过默认外观的部署，访客首屏是现代极简。

> 液态玻璃从内置改为主题包；升级后未安装 `glass.zip` 的站点会回到现代极简。需要继续使用时，从市场安装或上传发行包中的 `glass.zip`。

> 现代极简在 v1.2 是市场上的主题包（id `shadcn`），从 v1.3 起重新编进程序。升级后数据目录里那份旧包会被忽略（不再列出、同 id 不能再装），市场里也不再显示它；之前选了它的访客和默认设置照常生效。

## 包里有什么

```
matcha.zip
└── matcha/              # 可以有一层顶层目录（zip -r 打出来的那种），会自动去掉
    ├── theme.json       # 必需：清单
    ├── theme.css        # 至少一个样式表
    └── img/paper.webp   # 可选：样式表引用的图片、字体
```

只收这些文件类型：`theme.json`、`.css`、`.webp` / `.png` / `.jpg` / `.gif` / `.avif` 图片、`.woff` / `.woff2` 字体。
上述是 CSS 主题和附加包的文件范围，它们不收脚本、HTML、SVG 或额外 JSON。
声明 `frontend` 或 `settings_ui` 的隔离界面还可携带 `.html`、`.js` / `.mjs`、`.json`、`.svg`、`.wasm`、`.ico`、`.webmanifest`、`.ttf` / `.otf`，
运行在单独的隔离文档里。所有模式都不收符号链接；任何文件不合规，整包拒收。

限制：压缩包 ≤ 20 MB，解压后 ≤ 48 MB，单个文件 ≤ 16 MB，最多 300 个文件，文件名只用字母、数字、`. _ - /`。

## theme.json

```json
{
  "id": "matcha",
  "name": "抹茶",
  "hint": "示例主题包 · 平涂 · 抹茶绿",
  "version": "1.0.0",
  "author": "TZA Probe",
  "chrome": { "light": "#eef0e2", "dark": "#141811" },
  "swatch": ["#eef0e2", "#fbfaf3", "#5f8f3a"],
  "styles": ["theme.css"]
}
```

| 字段 | 说明 |
| --- | --- |
| `id` | 2–32 位小写字母、数字或连字符，以字母开头；不能与内置皮肤重名（shadcn）；press / anime 是官方主题包的 id。**同一个 id 再次上传即覆盖升级。** |
| `name` | 菜单里显示的名字，最多 30 字。 |
| `hint` | 名字下面的一行说明，最多 60 字，可省略。 |
| `version` / `author` | 可省略，只用于后台展示。 |
| `chrome` | 浏览器地址栏 / 状态栏底色（`<meta name="theme-color">`），浅色与深色各一个 `#rrggbb`。 |
| `swatch` | 菜单里那块三格色样，三个 `#rrggbb`：底色、面、强调色。 |
| `styles` | CSS 主题的样式表，1–8 个，按顺序加载。独立前台可省略，由它自己的 HTML / JS 引用资源。 |
| `frontend` | 独立前台入口，如 `{"entry":"index.html","api_version":1}`，见 [开发指南](FRONTENDS.md)。 |
| `settings_ui` | 可省略：开发者自定义设置弹窗入口，如 `{"entry":"settings/index.html","api_version":1}`。 |
| `settings` | 可省略：这套主题的设置菜单，见下面的[主题设置](#主题设置)。 |
| `base` | 兼容字段：接受 `"glass"` / `"plain"`，材质 CSS 由主题自己携带，见[基础层](#基础层)。 |
| `layout` | 可省略：卡片的布局变体（板面段落、密度、默认视图），见[布局变体](#布局变体)。 |
| `decorations` | 可省略：按节点挑选的装饰项，最多 60 项，见[按节点挑装饰](#按节点挑装饰)。 |

## 开发者自定义设置弹窗

从 Probe 1.6.0 起，CSS 主题和独立前台都可以声明 `settings_ui`。弹窗内容、样式和交互由开发者提供；没有声明时，核心在弹窗中按 `settings` 生成控件。附加包不能声明设置界面。

```json
"settings_ui": { "entry": "settings/index.html", "api_version": 1 }
```

入口必须是包内存在的 `.html` 文件。可以携带 HTML、JS、CSS 等资源；可执行文件始终受隔离文档 CSP 约束，无法读取后台 DOM、会话和存储。不要把管理员接口地址交给主题脚本。
在入口中先引入 `<script src="/theme-settings-sdk.js"></script>`，再加载自己的脚本。使用全局 `TZASettings`：

```js
TZASettings.onContext(({ theme, addons, nodes, mode }) => {
  // theme 包含清单和当前 options；addons 仅当前主题的附加包。
  // nodes 仅包含 id / name / region / decoration。
  // 每次保存成功后重新推送 context。
});
await TZASettings.ready;
await TZASettings.save({ motion: "off" });
await TZASettings.addAddon(); // 在按钮点击中调用，由核心选择文件并导入
await TZASettings.uploadAddon(fileInput.files[0]); // File/Blob，依附当前主题的 zip
await TZASettings.bindNode(nodeID, "my-pack/character-id");
await TZASettings.bindNode(nodeID, ""); // 恢复自动匹配
```

只允许修改本主题已声明的设置，导入 `for` 等于本主题 id 的附加包，以及绑定本主题或其附加包内存在的角色。节点绑定只修改 `decoration`，保留节点的其他配置。请求由父页面代办，弹窗关闭后销毁桥接监听与 iframe。
主题包市场会将声明 `settings_ui` 的版本限制到 Probe >=1.6.0，避免向旧版发放缺少设置桥接的包。可参考 [`themes/anime/settings/`](../themes/anime/settings)。

## 主题设置

后台「主题」页里，声明了 `settings` 的主题旁边有一颗「设置」，点开是这套主题自己的设置弹窗。**菜单里有哪些项由主题开发者决定**，
但必须按下面的规范声明：面板只认这几种类型，照着画控件、校验取值、存到服务端，对所有访客生效；CSS 主题使用这些控件；独立前台也能通过 SDK 读取设置，自行决定公开页上的呈现。

```json
"settings": [
  { "id": "density", "type": "choice", "label": "卡片密度", "hint": "紧凑时每行多放一张",
    "options": [{ "value": "cozy", "label": "舒适" }, { "value": "compact", "label": "紧凑" }], "default": "cozy" },
  { "id": "paper", "type": "toggle", "label": "纸纹底", "default": "on" }
]
```

| 字段 | 说明 |
| --- | --- |
| `id` | 1–24 位小写字母、数字或连字符，以字母开头，同一主题里不重复。也是样式表里 `data-opt-<id>` 的名字。 |
| `type` | `toggle`（开关，值 `on` / `off`）、`choice`（单选，2–8 个 `options`）或 `addons`（装饰来源开关，最多一项，见下）。 |
| `label` / `hint` | 菜单里的名字（≤ 20 字）与一行说明（≤ 60 字，可省略）。 |
| `options` | 只给 `choice`：每项 `value`（规则同 `id`，互不重复）与 `label`（≤ 20 字）。 |
| `default` | 没调过时的值。`toggle` 省略即 `off`，`choice` 省略即第一个选项。 |

最多 6 项；附加包不带 `settings`。不合规的 `settings` 会让整个包被拒收，错误信息指出是哪一项。

**样式表怎么用**：这套主题正在用时，面板把每项 `toggle` / `choice` 的当前值写在 `<html>` 上（首屏前就写好，不闪）：

```css
html[data-skin="matcha"][data-opt-paper="off"] body::before { background: #eef0e2; }
html[data-skin="matcha"][data-opt-density="compact"] .node-grid { gap: 12px; }
```

换到别的主题时这些属性会被清掉，所以选择器带上自己的 `data-skin` 即可，不会串到别的主题。

**附加包开关**（`"type": "addons"`）：菜单里列出这套主题自带的装饰项（主题声明了 `decorations` 时）和每个依附它的附加包，
各带一个开关；关掉的来源不参与挑装饰（连节点里手动指定的也跳过），但主题自带的 `default` 项总是最后的兜底。依附它的附加包的
更新、删除和「从市场添加」也都在这个菜单里——附加包不是市场的一个分类，而是挂在它依附的主题下面。没有这一项的主题，依附它的附加包照样生效，只是不能单独关。

接口：设置值随 `GET /api/v1/admin/themes` 与首屏的 `/theme-default.js` 发出（每个主题的 `options`）；
`PUT /api/v1/admin/themes/{id}/settings`，请求体只写要改的项，如 `{"motion": "off"}`、`{"addons": ["europe"]}`（关掉的来源 id 列表，主题自己的 id 代表它自带的装饰项）。
没声明过的项、不合规的值、没有设置菜单的主题都回 400。

## 怎么写样式表

主题包的样式表**叠在面板的基础样式之上**（默认是液态玻璃，见[基础层](#基础层)），只在访客选中这套主题时加载，并排在内置样式表之后。
所以最简单的主题只改颜色变量就够了——版式、组件全部沿用默认。

**稳定接口 = 设计令牌（CSS 变量）+ `data-part` 钩子。** 下面表格里列出的变量和 `[data-part="…"]` 属性跨版本保持不变，
确需改动时会写进发行说明；**类名（`.node-card`、`.metrics-grid` 等）不是稳定接口**，面板升级时可能调整。
写主题时优先改令牌，令牌不够再用 `data-part` 选择器，最后才动类名。

选择器请带上 `[data-skin="<你的 id>"]`：优先级稳稳压过默认样式里的 `:root` 和 `[data-theme="dark"]`，
不依赖加载顺序。深色用 `[data-theme="dark"]` 再写一份。

```css
:root[data-skin="matcha"] {
  --ink: #23301f;
  --accent: #5f8f3a;
  --accent-rgb: 95, 143, 58;
  --glass-bg: #fbfaf3;
  --blur: none;
}

:root[data-skin="matcha"][data-theme="dark"] {
  --ink: #eef2e6;
  --accent: #9cc76f;
  --accent-rgb: 156, 199, 111;
  --glass-bg: #1d2219;
}
```

### 颜色变量

| 变量 | 用在哪 |
| --- | --- |
| `--ink` / `--muted` / `--subtle` | 正文、次要文字、更淡的说明与占位 |
| `--line` | 分隔线、表格线 |
| `--accent` / `--accent-rgb` | 强调色：链接、选中态、主按钮；`-rgb` 是同一色的 `r, g, b`，给半透明用 |
| `--green` `--orange` `--red` `--purple` `--cyan` `--indigo`（及 `-rgb`） | 状态色：在线 / 警告 / 告警等，一般不必改 |
| `--glass-bg` / `--glass-bg-subtle` / `--glass-bg-elevated` | 面（卡片、面板）/ 浅一层的格子 / 弹层 |
| `--glass-border` / `--glass-border-subtle` | 面的描边 |
| `--glass-shadow` / `--glass-shadow-lg` / `--glass-inner-rim` | 投影；不想要内描边就写 `0 0 #0000`（不能写 `none`，它是阴影列表里的一项） |
| `--blur` | 毛玻璃，`none` 即关掉 |
| `--progress-track` | 进度条的底槽 |

### 字体、字号、圆角、间距

都定义在默认样式的 `:root` 上，组件规则一律读这些令牌，所以改一处整站跟着变。

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `--font-sans` | 系统无衬线（SF / PingFang / Segoe UI …） | 正文字体 |
| `--font-mono` | `ui-monospace, SFMono-Regular, Menlo …` | 等宽：IP 地址、命令、代码 |
| `--font-display` | 不定义（= 沿用正文） | 标题（h1–h3）、节点名、总览格里的大数字 |
| `--text-3xs` … `--text-3xl` | 9 / 10 / 11 / 12 / 13 / 14 / 16 / 18 / 22 / 28 px（`3xs` `2xs` `xs` `sm` `base` `md` `lg` `xl` `2xl` `3xl`） | 字号阶梯 |
| `--radius-scale` | `1` | 所有圆角的倍数：`0` 全直角，`0.5` 收一半，`1.5` 更圆；胶囊与圆点不受影响 |
| `--radius-xs` … `--radius-3xl` | 6 / 8 / 12 / 16 / 20 / 24 / 28 px（`xs` `sm` `md` `lg` `xl` `2xl` `3xl`），都已乘 `--radius-scale` | 圆角阶梯 |
| `--radius-pill` | `9999px` | 胶囊（标签、进度条、开关） |
| `--space-0-5` … `--space-10` | 2 / 4 / 6 / 8 / 10 / 12 / 14 / 16 / 18 / 20 / 22 / 24 / 28 / 32 / 40 px（4px 一档、带半档：`0-5` `1` `1-5` `2` … `8` `10`） | 间距阶梯：padding、margin、gap |

`--radius-scale` 与圆角阶梯要写在 `:root[data-skin="<id>"]` 上（阶梯在根上算好再继承下去，写在子元素上不生效）。
字号与间距阶梯通常不必整体改，它们主要让组件规则互相对齐；想做「紧凑」版式时改对应组件的语义令牌更稳。

```css
:root[data-skin="matcha"] {
  --font-display: "Matcha Serif", var(--font-sans);
  --radius-scale: 0.5;
}
```

### 组件语义令牌

卡片、面板、总览格各有一组令牌，**默认不定义**：组件规则写成 `var(--card-bg, <默认值>)`，主题定义了就用主题的，
没定义就是上面颜色变量画出的玻璃面。它们可以写在 `:root[data-skin]` 上，也可以写在更具体的选择器上（比如只给离线卡片换底色）。

| 变量 | 默认值 | 用在哪 |
| --- | --- | --- |
| `--card-bg` / `--card-border` | `--glass-bg` / `--glass-border` | 节点卡片的底与描边色 |
| `--card-shadow` / `--card-shadow-hover` | `--glass-shadow` / `--glass-shadow-lg`（各加 `--glass-inner-rim`） | 卡片投影，悬停时 |
| `--card-radius` | `--radius-2xl` | 卡片圆角 |
| `--card-pad` | `22px 22px 20px` | 卡片内边距（卡片视图） |
| `--panel-bg` / `--panel-border` / `--panel-shadow` / `--panel-radius` | `--glass-bg` / `--glass-border` / `--glass-shadow` / `--radius-xl` | 后台的面板 |
| `--tile-bg` / `--tile-border` / `--tile-shadow` / `--tile-shadow-hover` / `--tile-radius` / `--tile-pad` | `--glass-bg` / `--glass-border` / `--glass-shadow` / `--glass-shadow-lg` / `--radius-xl` / `18px 22px` | 公开页顶部的四个总览格 |

```css
html[data-skin="matcha"] [data-part="node-card"].is-offline { --card-bg: #f3f1ea; }
```

### 图表颜色

| 变量 | 默认 | 用在哪 |
| --- | --- | --- |
| `--series-0` … `--series-7` | `#ff625f` `#36c8bf` `#9b7cff` `#5a9cff` `#ffad3d` `#eb63b5` `#36c98b` `#ff873d` | 详情页多折线（各探测目标）与图例的八个系列色，超过八条循环使用 |
| `--chart-blue` / `--chart-orange` | `#007aff` / `#ff9500` | 主机历史折线下方的渐变（蓝 = 入站与默认，橙 = 出站）；折线本身用 `--accent` / `--orange` |

### data-part 钩子

页面里的关键组件都带一个 `data-part` 属性，名字跨版本稳定。要改令牌管不到的地方（版式、隐藏某一段、加装饰）就用它当选择器：

| `data-part` | 是什么 |
| --- | --- |
| `public-header` / `hero-meta` / `overview` / `overview-tile` / `toolbar` / `node-grid` | 公开页：顶栏、最近同步、总览区与其中的格子、搜索筛选条、节点列表容器（列表视图时还带 `.node-list`） |
| `node-card` | 一台节点的卡片（`.is-online` / `.is-offline` 表示状态）。卡壳上还带 `data-region` / `data-group` / `data-vendor`（节点的所在地区、分组、商家，没填就没有这个属性），可以按它们选装饰图；挑中了装饰项时还有 `data-deco` / `data-deco-pack`（见[按节点挑装饰](#按节点挑装饰)） |
| `card-body` / `card-top` / `node-sub` | 卡片内容、头部（状态点 + 名字 + 标签）、地区 · 分组那一行 |
| `metrics-grid` / `metric` | CPU / 内存 / 磁盘 / 周期用量四格，及其中每一格 |
| `card-stats` | 速率、累计流量、到期与价格那条信息带 |
| `vendor-plate` | 商家名牌 |
| `card-backdrop` / `card-backdrop-layer` / `card-overlay` / `card-plate` | 卡片装饰槽：背景层、背景层里的第二层、前景层、商家名牌，默认不显示，见[卡片装饰槽](#卡片装饰槽) |
| `detail-stats` / `chart` | 详情弹窗的统计格、每张历史图表 |
| `sidebar` / `admin-top` / `page-heading` / `panel` | 后台：侧栏、顶栏、页标题、面板 |

```css
/* 卡片里不显示 CPU / 内存那四格 */
html[data-skin="matcha"] [data-part="node-card"] [data-part="metrics-grid"] { display: none; }
```

### 底纸

默认皮肤在 `body::before` 上画了四团缓慢流动的彩色光斑。换底纸就覆盖它：

```css
html[data-skin="matcha"] body::before {
  inset: 0;
  animation: none;
  background: radial-gradient(ellipse at 15% 10%, rgb(150 190 110 / 0.28), transparent 50%), #eef0e2;
}
```

### 商家名牌

节点填了「商家」时，卡片和列表里会多一枚 `[data-part="vendor-plate"]`（默认是地区 · 分组后面的一枚强调色小牌），
主题可以按它重画。卡片上另有一个装饰槽 `[data-part="card-plate"]`，内容也是商家名，可以画成独立的一块名牌（二次元把它挂在看板上沿，明信片示例画成邮戳），见[卡片装饰槽](#卡片装饰槽)。

### 基础层

核心只加载公共组件结构和内置现代极简；液态玻璃的材质样式在 `themes/glass/glass.css`，随 `glass.zip` 安装、选择后加载。
`base` 的旧取值 `plain` / `glass` 仍可解析，但不再自动叠加内置玻璃 CSS。旧主题若依赖玻璃材质，需要把该材质 CSS 放进自己的包并在 `styles` 中列出；普通主题使用 `plain`。

### 布局变体

`layout` 声明卡片视图的版式，面板读清单后在渲染时生效，CSS 主题无需脚本；独立前台不受这些布局变体约束：

```json
"layout": {
  "card_sections": ["node-sub", "metrics-grid", "card-stats"],
  "density": "compact",
  "default_view": "card"
}
```

| 字段 | 说明 |
| --- | --- |
| `card_sections` | 卡片头部（名字、标签、「详细」）下面的板面段落，按写的顺序排，没写的段落不显示。可用：`node-sub`（地区 · 分组与地址族开关）、`metrics-grid`（CPU / 内存 / 磁盘 / 周期用量）、`card-stats`（速率、累计、到期与价格）、`network`（延迟色带与三网回程）。每项最多一次；写成 `[]` 就只剩头部。不写即默认的四段全显示。只影响卡片视图，列表视图不变。 |
| `density` | `"cozy"`（默认）或 `"compact"`：紧凑时卡片内边距、卡片间距、四格指标的间距各收一档。面板写成 `<html data-density="compact">`，样式表也可以按它再调。 |
| `default_view` | `"card"` 或 `"list"`：访客没在右上角选过展示方式时的默认视图。访客选过就以访客的为准。 |
| `align_rows` | 默认 `true`：同一排卡片的段落补齐到同一高度。卡片不是一摞段落的主题（比如上半张是整幅装饰图）写 `false`。 |

不合规的 `layout`（不认识的段落名、重复、取值不对）会让整个包被拒收，错误信息指出是哪一项。

### 卡片装饰槽

每张卡片（卡片视图）都有四个装饰元素，**默认不显示**，主题用 CSS 放开并画上东西——背景图、角标、名牌都行：

| `data-part` | 位置 | 内容 |
| --- | --- | --- |
| `card-backdrop` | 卡片第一个子元素，在看板（`card-body`）下面 | 空，画背景用 |
| `card-backdrop-layer` | `card-backdrop` 里面的一层 | 空，给第二张图（比如眨眼时换的那一帧）用 |
| `card-overlay` | 卡片里看板之后，叠在看板上面 | 空，画前景（角标、折角、压在看板上的手） |
| `card-plate` | 最后，只在节点填了商家时有 | 商家名文本 |

它们都在卡片的 `overflow: hidden` 之内；带 `data-bleed` 的层允许铺到卡片边框（越过内边距）。叠放顺序靠 `position` / `z-index` 自己定，
卡片本身没有定位，要用绝对定位时先给 `[data-part="node-card"]` 写 `position: relative`。按节点挑图用卡壳上的 `data-region` / `data-group` / `data-vendor`：

```css
html[data-skin="postcard"] [data-part="card-backdrop"] { display: block; position: absolute; inset: 0; background: url("img/stripes.webp"); }
html[data-skin="postcard"] [data-part="node-card"][data-region="日本"] [data-part="card-backdrop"] { background-image: url("img/japan.webp"); }
```

要按节点换整套图（每台机一张不同的画），用下面的[按节点挑装饰](#按节点挑装饰)：面板挑好之后把图片写成变量，样式表接到槽位上。

另外两样卡片状态也交给主题来画：点卡片头的名字收起 / 展开，面板在卡壳上写 `data-rolled="true"`（状态按节点记在访客本地，默认样式不改变卡片）；
指针在卡上移动时，面板在卡壳上写 `--tilt-nx` / `--tilt-ny`（指针相对卡心的位置，-1…1）并加上 `.is-tilt`，可以拿来做倾斜或层间视差。

### 按节点挑装饰

主题包（以及依附它的附加包）可以在清单里声明 `decorations`：一组装饰项，每项写匹配规则、几张图片和若干数值。面板给每张卡挑一项：

1. 节点设置里手动指定的「主题装饰」（写成 `包 id/项 id`）；
2. `nodes` 里有这台节点名的项（不分大小写）；
3. `regions` 里有这台节点「所在地区」的项（逐字比对）；
4. 标了 `"default": true` 的项；
5. 主题自带的 `default` 项（即使在设置里关掉了主题自带的项）。

每一步都是附加包排在主题自带的项前面（装附加包就是为了替换），在[附加包开关](#主题设置)里关掉的来源整个跳过。
挑中后面板在卡壳上写 `data-deco="<包 id>/<项 id>"` 与 `data-deco-pack="<包 id>"`（背景层上也有一份 `data-deco`），
并把这一项的每张图片、每个数值写成卡壳上的 CSS 变量 `--deco-<名字>`（图片是 `url(...)`）。**面板不认识这些名字的含义**，怎么用全由主题的样式表决定。

```json
"decorations": [
  { "id": "tokyo", "name": "东京", "regions": ["日本"], "images": { "art": "img/tokyo.webp" }, "vars": { "tint": "#c8372d", "shift": "12px" } },
  { "id": "plain", "name": "通用", "default": true, "images": { "art": "img/plain.webp" } }
]
```

```css
html[data-skin="postcard"] [data-part="node-card"][data-deco] [data-part="card-backdrop"] {
  display: block;
  background: var(--deco-art) top center / cover, var(--deco-tint, transparent);
}
```

| 字段 | 说明 |
| --- | --- |
| `id` / `name` | 1–32 位小写字母、数字或连字符（包内不重复）与显示名（≤ 30 字，节点设置的下拉里就是它）。 |
| `nodes` / `regions` / `default` | 匹配规则，见上面的顺序。`nodes` 最多 50 个、`regions` 最多 30 个。 |
| `images` | 名字 → 包内图片路径（webp / png / jpg / avif），最多 8 张。名字规则同 settings 的 `id`。 |
| `vars` | 名字 → 值，最多 32 个。值只能是长度、数字、颜色或时间这类简单值（字母、数字、`# . , % ( ) / + -` 和空格，≤ 64 字符），不能带引号、分号或花括号。名字不能和 `images` 里的重复。 |

主题自己的装饰项也可以不带图片，只写匹配规则，图放在样式表里按 `[data-deco="<包 id>/<项 id>"]` 一项一条规则挂（二次元的内置角色就是这样）。

### 再往深改

令牌与 `data-part` 都不够时，也可以针对页面里的类名写规则，想看一套完整改法可以参考仓库里的
`themes/press/press.css`、`themes/anime/anime.css` 与内置的 `web/shadcn.css`。类名不是稳定接口，面板升级时可能调整；只改令牌的主题最不容易坏。

### 图片与字体

用相对路径引用包里的文件即可，例如 `url("img/paper.webp")`、`@font-face { src: url("fonts/x.woff2") }`。
页面的安全策略只允许加载本站资源，**引用外部地址（CDN 字体、图床图片）不会生效**，请把文件放进包里。

## 附加包

附加包是 `"type": "addon"` 的包：它不是一套外观，只给 `for` 写的那套主题添装饰项。装好后不出现在外观菜单里，
列在它依附的主题下面（主题有附加包开关时就在那个设置菜单里开关）；那套主题没装或没在用时不生效，主题被删后附加包保留，装回即恢复。

```json
{
  "id": "europe",
  "type": "addon",
  "for": "postcard",
  "name": "欧洲邮票",
  "version": "1.0.0",
  "decorations": [
    { "id": "germany", "name": "德国", "regions": ["德国", "Germany"], "images": { "art": "germany.webp" }, "vars": { "tint": "#2a8f86" } }
  ]
}
```

| 字段 | 说明 |
| --- | --- |
| `for` | 必填：依附的主题 id（不能是自己）。 |
| `decorations` | 必填，1–60 项，格式见[按节点挑装饰](#按节点挑装饰)。图片名字与数值名字要按依附的那套主题约定的来写（看那套主题的说明）。 |
| `name` / `hint` / `version` / `author` | 同主题包。 |

附加包不带 `styles`、`settings_ui`、`settings`、`base`、`layout`、`chrome`、`swatch`：外观完全由它依附的主题决定。
官方二次元的角色包就是依附 `anime` 的附加包，图片与数值的约定见 [themes/anime/README.md](../themes/anime/README.md)。

## 上传、升级与删除

1. 把目录打成 zip：`cd examples/themes && zip -r matcha.zip matcha`
2. 后台「主题 → 上传主题包 / 附加包」，选这个 zip。
3. 右上角外观菜单里就能选它；想让访客默认就看到它，在「面板设置 → 外观与品牌 → 默认外观」里选它保存。

- **升级**：同一个 id 再传一次，整包替换（旧版本多出来的文件不会残留）。
- **删除**：在「主题」页的已安装列表里点删除。正在用它的访客会回到默认皮肤；它若是前台默认，默认也回到现代极简。删掉一套主题时依附它的附加包保留，但要装回那套主题才生效。
- **备份**：主题包在数据目录的 `themes/` 下（市场来源记录在 `themes/.market/`），**不在**「下载备份」的数据库快照里，迁移服务器时连同这个目录一起拷。

## 市场（TZA-CC）

TZA-CC 是 TZA Probe 的云端市场。Server 默认使用官方 TZA-CC（`https://tza.cc`，内置其签名公钥）。后台「主题」页下方的市场
按主题浏览、搜索、看详情（附加包在它依附的主题的设置菜单里浏览）（说明、截图、版本与更新说明），一键安装或更新；插件在「插件」页的市场里。TZA-CC 账号在「面板设置 → 账号与安全」连接。插件（通知渠道、探测目标集）的格式与安装确认见 [PLUGINS.md](PLUGINS.md)，验签与来源、更新规则与主题包相同。
`--cc-url`（或 `TZA_CC_URL`）可改连测试或私有部署的 CC，设为 `off` 关闭市场，见 [部署说明](DEPLOYMENT.md#tza-cc-市场可选)。

- **不连 CC 也一样能用**：市场只是多一个来源，「主题」页上传 zip 的路径不变；CC 停服或断网，已经装好的包照常使用。
- **浏览器不直连 CC**：目录、预览图和安装包都由 Server 代为获取（预览图走 `/api/v1/admin/market/media` 代理，只取 CC 同源地址上的 png / jpeg / webp / gif / avif，≤ 5 MB），面板的 CSP 不用放开，CC 也看不到访客。
- **CC 收不到监控数据**：Server 只在你打开市场、安装 / 更新、绑定时请求 CC，带上的只有 Probe 版本、绑定后的实例 token、从市场装的包的 `id@version`（检查更新时）和绑定时填的实例名。
- **验签后才安装**：市场下发的每个版本都带 CC 的 Ed25519 签名（覆盖包 id、版本与 zip 的 SHA-256）。Server 下载后依次核对：大小与 CC 登记的一致且 ≤ 20 MB、SHA-256、签名来自信任的公钥（`--cc-key` / `TZA_CC_KEYS`）、包内 `theme.json` 的 `id` 与市场包 id 一致、`version` 与签名版本一致；再走与本地上传完全相同的包校验。任何一步不过，整包拒收，磁盘上不留东西，已装的旧版本不受影响。没有配置任何信任公钥时可以浏览，但安装一律拒绝。
- **版本兼容**：市场版本号固定为 `MAJOR.MINOR.PATCH`。每个版本可以声明需要的 Probe 版本（如 `>=1.1.0`），不兼容的版本在详情里标「不兼容」、不给安装；「安装」默认装当前 Probe 能用的最新版。
- **来源与更新**：市场装的包在列表里标「市场」，手动上传的标「本地」。市场装的包记得自己的版本与 SHA-256（存在 `themes/.market/<id>.json`，不对外提供），打开市场页时向 CC 查一次有没有兼容的新版，有就标「有更新」，**由你点更新，不会自动更新**。本地上传同一个 id 会替换市场装的包，之后按本地包对待。
- **付费包**：TZA-CC 开启商业功能后，开发者可以给包定价。付费包在卡片上标价格，只有绑定的 TZA-CC 账号拥有它（兑换码兑换或管理员授予）时才能安装，未拥有时详情页说明怎么获取；免费包不受影响。
- **绑定账号（可选）**：免费包不绑定也能装。「连接 TZA-CC」走设备码流程（同 `gh auth login`）：后台显示一串验证码，到 CC 登录后输入它即可；凭据存在数据目录的 `cc-instance`（0600，同 `admin-account`），只发给绑定时那个 CC，CC 只保存 token 的摘要。CC 对 token 回 401（被撤销或在用户中心解绑）时 Server 自动删掉本机凭据。「断开连接」通知 CC 解绑并删除本机凭据。

## 接口

- `GET /api/v1/admin/themes`：内置（现代极简、液态玻璃）+ 已安装的清单，声明了设置的主题带 `options`。
- `PUT /api/v1/admin/themes/{id}/settings`：改主题设置，见[主题设置](#主题设置)。
- `POST /api/v1/admin/themes`：请求体就是 zip 本身（`Content-Type: application/zip`），返回装好的清单。
- `DELETE /api/v1/admin/themes/{id}`：删除主题包。
- MCP 的 `update_settings` 里，`default_skin` 可以填已安装主题包的 id。
- 已安装清单里市场装的包带 `"source": "market"`（本地上传的没有这个字段）。
- 市场：`/api/v1/admin/market/*`，见 [HTTP API](API.md#主题市场tza-cc)；MCP 的 `search_market` / `install_market_package`。

## 示例

仓库里有两个完整示例，可以直接打包上传，也可以复制一份改成自己的：

- [`examples/themes/matcha`](../examples/themes/matcha)：叠在液态玻璃上，只用稳定接口（颜色、圆角、字体、卡片令牌、图表色与一条 `data-part` 规则）加底纸。
- [`examples/themes/postcard`](../examples/themes/postcard)：`"base": "plain"`，用布局变体（只留三段、紧凑密度）和三个卡片装饰槽（信封斜纹、邮戳名牌、折角），按 `data-region` 换配色，不带图片。
