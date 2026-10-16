# 二次元（anime）

官方主题包：线路机角色卡，樱花粉。`"base": "plain"`（不叠液态玻璃），`"layout": {"align_rows": false}`（角色卡上半张是立绘，不做同排段落拉齐）。
面板只提供通用机制——卡片装饰槽、按节点挑装饰、附加包（见 [docs/THEMES.md](../../docs/THEMES.md)），角色怎么画全在本包的 `anime.css` 里。

## 角色是怎么画上去的

每台节点的角色是一个**装饰项**：本包 `theme.json` 的 `decorations` 里有五个内置角色，角色包（附加包）再补更多。
面板按「节点设置里指定的 → 节点名 → 所在地区 → 默认」挑一个，在卡壳与背景层上写 `data-deco="<包 id>/<角色 id>"`、
卡壳上写 `data-deco-pack="<包 id>"`；`anime.css` 用卡片的四个装饰槽画出来：

| 装饰槽 | 画的是 |
| --- | --- |
| `card-backdrop` | 整张立绘（头、肩、双手在牌子上沿以上，裙摆在下沿以下，一张连续的图） |
| `card-backdrop-layer` | 闭眼帧，按 `--cast-blink-dur` / `--cast-blink-delay` 眨眼 |
| `card-overlay` | 双手层，压在 HTML 看板上沿，修补被板沿吃掉的指尖 |
| `card-plate` | 商家名牌，挂在看板上沿正中、两只手之间 |

看板（`card-body`）盖在立绘的牌子上；几何（立绘高、板顶、收起态板条高）由牌子上沿比例 `--cast-top-frac` 在 CSS 里直接算出。
点卡片头的名字收起 / 展开，面板写 `data-rolled="true"`，本包把看板压成一条、露出立绘的身体。指针在卡上移动时面板写
`--tilt-nx` / `--tilt-ny`，本包用它们做裸眼 3D 倾斜与层间视差。

内置角色的图片、牌子比例、配色与眨眼节奏写在 `anime.css` 里（按 `data-deco="anime/<id>"` 一人一条规则），清单里只写匹配规则：

| 装饰项 id | 线路 | 商家 | 设定 |
| --- | --- | --- | --- |
| `jp-nosla-gen2` | Gen.2 | 家人云 | Gen.2 亚太精品线 × 家人云；三网回程 CTG / 10099→9929 / CMIN2，61/57/65ms。日本元素 = 樱花簪 + 千鸟格水手领 + 红白缎带；人物 = 银白拖到腰的长直发 + 猫耳 + 紫瞳半眯的傲娇脸（全系列唯一的兽耳、唯一的紫瞳），藏青水手服，胸口「家人」铭牌 |
| `de-nosla-fra` | 三网回程 CTG · 10099→9929 · CMIN2 | 家人云 | 法兰克福，欧洲线里回程最规整的一条（150–190ms）。德国元素 = 黑红金缎带 + Dirndl（白衬衫 / 深绿系带胸衣 / 白围裙）+ 麻花辫上的椒盐卷饼发夹；人物 = 蜂蜜金发盘成麻花冠 + 灰绿瞳的端庄微笑（唯一的盘发、唯一传统服饰），胸口「家人」铭牌 |
| `us-vmiss-tri` | CN2GIA · 10099→9929 · CMIN2 | 云上飞行 | 美西三网精品（CN2GIA / 9929 / CMIN2，160ms 上下）。卖家 VMISS 官网是蓝白科技风 + 云朵涂鸦 + 亮黄点缀，人物据此做：栗棕双马尾 + 电蓝挑染 + 圆亮蓝瞳 + 张口大笑挥手（唯一的双马尾、唯一连帽衫、唯一大幅笑容），白色 techwear 连帽衫 + 云朵发夹 + 黄星发夹 + 星条缎带，胸口铭牌写 VMISS |
| `us-bwh-dc9` | CN2GIA · 10099→9929 · CMIN2 | 搬瓦工 | 洛杉矶 DC9：CN2GIA + 9929 + CMIN2（159–167ms）。卖家搬瓦工（BandwagonHost）官网是金黄→琥珀渐变 #FFC107→#F57C00 配深棕/黑与银灰 HOST、黑机架 + 金色灯；人物 = 琥珀棕高马尾 + 银灰挑染 + 金瞳 + 咧嘴露虎牙（唯一的高马尾、唯一工装背带裤、唯一趴在牌上的姿势），深棕背带裤 + 大篷车轮发夹 + 星条缎带 + 金星别针，胸口铭牌写 Bandwagon |
| `generic` | — | — | 通用默认人物：深棕长直发 + 白衬衫 + 藏青百褶裙，中性日常、无任何国家 / 商家元素；整身立绘 + 手蒙版 + 闭眼帧同一套约定。 |

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
