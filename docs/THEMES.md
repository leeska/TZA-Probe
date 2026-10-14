# 主题包与角色包

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

### 商家名牌

节点填了「商家」时，卡片和列表里会多一枚 `.vendor-plate`（默认是地区 · 分组后面的一枚强调色小牌），
主题可以按这个类名重画它。二次元主题的角色卡另有一层 `.anime-plate`，把商家挂在看板上沿当名牌。

### 再往深改

可以针对页面里的类名写规则（`.node-card`、`.panel`、`.overview-tile` 等），想看一套完整改法可以参考仓库里的
`web/press.css`、`web/shadcn.css`、`web/anime.css`。类名不是稳定接口，面板升级时可能调整；只改变量的主题最不容易坏。

### 图片与字体

用相对路径引用包里的文件即可，例如 `url("img/paper.webp")`、`@font-face { src: url("fonts/x.woff2") }`。
页面的安全策略只允许加载本站资源，**引用外部地址（CDN 字体、图床图片）不会生效**，请把文件放进包里。

## 角色包

角色包是 `"type": "characters"` 的主题包：它不是一套外观，只给**二次元**主题添角色。装好后不出现在外观菜单里，
二次元主题的卡片会按下面的顺序给每台节点挑角色：

1. 节点设置里手动指定的「角色」（角色包被删了就跳过）；
2. 角色包里 `nodes` 写了这台节点名的角色；
3. 内置的四个专属角色（按节点名）；
4. 角色包里 `regions` 包含这台节点「所在地区」的角色；
5. 角色包里标了 `"default": true` 的角色；
6. 内置的通用角色。

```json
{
  "id": "europe",
  "type": "characters",
  "name": "欧洲角色",
  "version": "1.0.0",
  "characters": [
    {
      "id": "germany",
      "name": "德国",
      "regions": ["德国", "Germany"],
      "art": "germany.webp",
      "hands": "germany-hands.webp",
      "blink": "germany-blink.webp",
      "plate": { "top": 0.307, "bottom": 0.446 },
      "palette": {
        "light": { "board": "#eef8f7", "line": "#a8d8d3", "track": "#d3eeeb", "accent": "#2a8f86" },
        "dark":  { "board": "#14262a", "line": "#2b4d52", "track": "#1d363a", "accent": "#6fd3c9" }
      }
    }
  ]
}
```

| 字段 | 说明 |
| --- | --- |
| `id` / `name` | 角色 id（小写字母、数字、连字符）与显示名。节点里指定时写「角色包 id/角色 id」，如 `europe/germany`。 |
| `regions` / `nodes` / `default` | 匹配规则，见上面的顺序。地区按节点「所在地区」逐字比对。 |
| `art` | 立绘：**透明底**的全身像，双手捧着一块牌子。建议画布 585×1530（与内置角色一致），人物水平居中。 |
| `hands` | 可选，双手层：只含手（透明底），与立绘同一画布，压在看板上沿，让手指盖住板沿。没有就省略，手会被看板盖住。 |
| `blink` | 可选，闭眼帧：与立绘同一画布，只有眼睛不同，卡片会隔几秒眨一次眼。 |
| `plate` | 牌子上外沿、下外沿各占立绘高度的比例（0.1–0.7、比上沿至少大 0.03）。卡片的看板按它盖在牌子上，算准了看板才严丝合缝。 |
| `palette` | 卡片配色，浅色、深色各一套：`board` 牌面、`line` 描边、`track` 进度条底槽、`accent` 强调色（名牌、进度条、按钮），`cell` 格子底色可省略。 |

**自己生成角色包**：仓库与发行包里的 [`tools/character-pack`](../tools/character-pack/README.md) 能从「地区 → 角色设定」一路做到 zip：
调图像接口出立绘与闭眼帧，自动去白底、找牌子、裁成标准画布、测 `plate`、拆双手层、按服装配色并打包。
在 Claude Code 里打开本仓库，直接说「给香港和德国做二次元角色」即可触发 `character-pack` skill，由它设计角色、把关每张图。

一个角色包最多 60 个角色；图片只收 webp / png / jpg / avif。角色包不带样式表。商家名不要画进立绘里——
卡片会把节点的「商家」写在看板上沿的名牌上，同一个地区角色可以给不同商家的机器共用。

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
