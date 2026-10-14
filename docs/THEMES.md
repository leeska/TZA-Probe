# 主题包

面板的外观风格（右上角那颗调色盘按钮）由两部分组成：

- **内置皮肤**：液态玻璃、复古印刷、现代极简、二次元，打包在程序里。
- **主题包**：后台「面板设置 → 主题」上传的 zip，存在数据目录的 `themes/` 下，不用重新编译。

装好的主题包和内置皮肤一样出现在外观菜单里，也能在「默认外观」里设成访客的首屏皮肤。

## 包里有什么

```
matcha.zip
└── matcha/              # 可以有一层顶层目录（zip -r 打出来的那种），会自动去掉
    ├── theme.json       # 必需：清单
    ├── theme.css        # 至少一个样式表
    └── img/paper.webp   # 可选：样式表引用的图片、字体
```

只收这些文件类型：`theme.json`、`.css`、`.webp` / `.png` / `.jpg` / `.gif` / `.avif` 图片、`.woff` / `.woff2` 字体。
**不收脚本、HTML、SVG**，也不收符号链接；任何一个文件不合规，整个包都会被拒收，磁盘上不留任何东西。

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
| `id` | 2–32 位小写字母、数字或连字符，以字母开头；不能与内置皮肤重名（glass / press / shadcn / anime）。**同一个 id 再次上传即覆盖升级。** |
| `name` | 菜单里显示的名字，最多 30 字。 |
| `hint` | 名字下面的一行说明，最多 60 字，可省略。 |
| `version` / `author` | 可省略，只用于后台展示。 |
| `chrome` | 浏览器地址栏 / 状态栏底色（`<meta name="theme-color">`），浅色与深色各一个 `#rrggbb`。 |
| `swatch` | 菜单里那块三格色样，三个 `#rrggbb`：底色、面、强调色。 |
| `styles` | 包内样式表的相对路径，1–8 个，按顺序加载。 |

## 怎么写样式表

主题包的样式表**叠在默认样式（液态玻璃）之上**，只在访客选中这套主题时加载，并排在内置样式表之后。
所以最简单的主题只改颜色变量就够了——版式、组件全部沿用默认。

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

### 底纸

默认皮肤在 `body::before` 上画了四团缓慢流动的彩色光斑。换底纸就覆盖它：

```css
html[data-skin="matcha"] body::before {
  inset: 0;
  animation: none;
  background: radial-gradient(ellipse at 15% 10%, rgb(150 190 110 / 0.28), transparent 50%), #eef0e2;
}
```

### 再往深改

可以针对页面里的类名写规则（`.node-card`、`.panel`、`.overview-tile` 等），想看一套完整改法可以参考仓库里的
`web/press.css`、`web/shadcn.css`、`web/anime.css`。类名不是稳定接口，面板升级时可能调整；只改变量的主题最不容易坏。

### 图片与字体

用相对路径引用包里的文件即可，例如 `url("img/paper.webp")`、`@font-face { src: url("fonts/x.woff2") }`。
页面的安全策略只允许加载本站资源，**引用外部地址（CDN 字体、图床图片）不会生效**，请把文件放进包里。

## 上传、升级与删除

1. 把目录打成 zip：`cd examples/themes && zip -r matcha.zip matcha`
2. 后台「面板设置 → 主题 → 上传主题包」，选这个 zip。
3. 右上角外观菜单里就能选它；想让访客默认就看到它，在「默认外观」里选它保存。

- **升级**：同一个 id 再传一次，整包替换（旧版本多出来的文件不会残留）。
- **删除**：在主题列表里点删除。正在用它的访客会回到默认皮肤；它若是前台默认，默认也回到液态玻璃。
- **备份**：主题包在数据目录的 `themes/` 下，**不在**「下载备份」的数据库快照里，迁移服务器时连同这个目录一起拷。

## 接口

- `GET /api/v1/admin/themes`：内置 + 已安装的清单。
- `POST /api/v1/admin/themes`：请求体就是 zip 本身（`Content-Type: application/zip`），返回装好的清单。
- `DELETE /api/v1/admin/themes/{id}`：删除主题包。
- MCP 的 `update_settings` 里，`default_skin` 可以填已安装主题包的 id。

## 示例

仓库里的 [`examples/themes/matcha`](../examples/themes/matcha) 是一个只改变量与底纸的完整示例，可以直接打包上传，也可以复制一份改成自己的。
