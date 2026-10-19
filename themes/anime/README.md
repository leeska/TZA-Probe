# 二次元（anime）

官方主题包：线路机角色卡，樱花粉。`"base": "plain"`（不叠液态玻璃），`"layout": {"align_rows": false}`（角色卡上半张是立绘，不做同排段落拉齐）。
面板只提供通用机制——卡片装饰槽、按节点挑装饰、附加包（见 [docs/THEMES.md](../../docs/THEMES.md)），角色怎么画全在本包的 `anime.css` 里。

## 随主题捆绑的基础角色

`anime.zip` 同时包含主题、设置弹窗和基础角色资源，安装主题后即可使用，无需另装角色包。
基础包仅含日本、德国、美国各一个角色，外加一个无地区的通用兜底角色；不再绑定具体节点名或商家。
尚未设置地区或没有对应地区角色的节点使用通用角色。地区使用节点的「所在地区」字段。
现有角色 id 保持不变，方便保留已保存的绑定。

后台「主题 → 二次元 → 设置」打开本包 `settings/index.html` 自定义的弹窗，在其中添加角色包、开关动效与角色包、选择每个节点的角色。角色绑定保存到节点 `decoration` 字段；选择自动则清除绑定，恢复按地区匹配。
开发者可通过 [`settings_ui` 与 TZASettings SDK](../../docs/THEMES.md#开发者自定义设置弹窗) 自定义整个弹窗。

角色仍由通用装饰机制渲染：`card-backdrop` 放立绘、`card-backdrop-layer` 放闭眼帧、`card-overlay` 放双手、`card-plate` 放节点的商家名牌。
图片、几何、配色与眨眼节奏在本包的 `anime.css` 中；附加角色从清单读取。

## 角色包（附加包）

角色包是 `"type": "addon", "for": "anime"` 的附加包，在二次元的「设置 → 角色包」里开关（`addons` 设置项；值里的 `anime` 代表内置角色），
也只在二次元正在用时生效。自己做角色包用仓库的 [`tools/character-pack`](../../tools/character-pack/README.md)。

角色包里每个装饰项按下面的名字写图片与数值，面板把它们写成卡壳上的 `--deco-<名字>`，`anime.css` 再接到自己的变量上：

| 名字 | 类型 | 说明 |
| --- | --- | --- |
| `art` | images | 立绘：**透明底**的全身像，双手捧着一块牌子，画布 585×1530，人物水平居中 |
| `hands` | images | 可选，双手层：只含手（透明底），与立绘同一画布 |
| `blink` | images | 可选，闭眼帧：与立绘同一画布，只有眼睛不同 |
| `plate-top` / `plate-bottom` | vars | 牌子上外沿、下外沿各占立绘高度的比例（0.1–0.7，下沿比上沿至少大 0.03） |
| `blink-dur` / `blink-delay` | vars | 眨眼周期与初始延迟，如 `5.4s` / `0.4s` |
| `board-light` / `cell-light` / `line-light` / `track-light` / `accent-light` | vars | 浅色下的牌面、格子底、描边、进度条底槽、强调色 |
| `accent-rgb-light` | vars | 浅色强调色的 `r, g, b`，给半透明用 |
| `board-dark` … `accent-rgb-dark` | vars | 深色下的同一套 |

```json
{
  "id": "europe",
  "type": "addon",
  "for": "anime",
  "name": "欧洲角色",
  "version": "1.0.0",
  "decorations": [
    {
      "id": "germany",
      "name": "德国",
      "regions": ["德国", "Germany"],
      "images": { "art": "germany.webp", "hands": "germany-hands.webp", "blink": "germany-blink.webp" },
      "vars": {
        "plate-top": "0.307", "plate-bottom": "0.446", "blink-dur": "6.2s", "blink-delay": "0.5s",
        "board-light": "#eef8f7", "cell-light": "rgb(255 255 255 / 0.62)", "line-light": "#a8d8d3",
        "track-light": "#d3eeeb", "accent-light": "#2a8f86", "accent-rgb-light": "42, 143, 134",
        "board-dark": "#14262a", "cell-dark": "#14262a", "line-dark": "#2b4d52",
        "track-dark": "#1d363a", "accent-dark": "#6fd3c9", "accent-rgb-dark": "111, 211, 201"
      }
    }
  ]
}
```

商家名不要画进立绘里：卡片会把节点的「商家」写在看板上沿的名牌上，同一个地区角色可以给不同商家的机器共用。
