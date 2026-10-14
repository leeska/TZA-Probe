# 角色包生成器

把「地区 → 角色设定」做成一个能在后台直接上传的角色包（`"type": "characters"` 的 zip，格式见 [docs/THEMES.md](../../docs/THEMES.md)）。
在 Claude Code 里打开本仓库时，也可以直接让它用 `character-pack` 这个 skill 帮你从设计角色一路做到打包。

## 准备

```sh
python3 -m pip install pillow numpy
export TZA_IMAGE_API_BASE=https://api.example.com/v1   # 兼容 OpenAI /chat/completions、能返回图片的接口
export TZA_IMAGE_API_KEY=sk-...                         # 只从环境变量读，不会写进任何文件
export TZA_IMAGE_MODEL=gemini-3-pro-image               # 可省略，默认就是它（Nano Banana Pro）
```

没有接口也能用：自己用任何工具画好原图，放进 `work/raw/<角色 id>.png`（闭眼帧放 `<角色 id>-blink.png`），直接跑 `build`。

## 设定文件

```json
{
  "id": "europe",
  "name": "欧洲角色",
  "version": "1.0.0",
  "author": "你的名字",
  "characters": [
    {
      "id": "germany",
      "name": "德国",
      "regions": ["德国", "法兰克福"],
      "design": "A calm young woman with a chin-length ash-teal bob, thin round glasses and teal eyes; an oversized cream knit cardigan over a white collared blouse, a black-red-gold striped ribbon tie, a charcoal plaid pleated skirt, dark knee socks and brown loafers. Modern Berlin office style."
    }
  ]
}
```

- `regions` 与节点「所在地区」逐字比对；也可以写 `nodes`（节点名）或 `"default": true`。
- `design` 用英文写外形：发型发色、眼睛、服装、配饰、气质。**不要写商家名、文字、logo、国旗**——商家名由卡片上的名牌显示。
- 想指定配色就加 `palette`（格式同 docs/THEMES.md）；不写则按服装和发色自动配。
- 每个角色出两次图（立绘 + 闭眼帧）；不要闭眼帧就写 `"blink": false`。

## 运行

```sh
python3 make_pack.py generate pack.json --work work            # 出图，原图缓存在 work/raw/
python3 make_pack.py build pack.json --work work --out europe.zip
# 或一步到位：python3 make_pack.py all pack.json --work work --out europe.zip
```

`build` 会：

1. 把白底去成透明（从图片四边向内填充，牌子被描边围住，不会被抠掉）；
2. 找出那块白色牌子，按它居中裁成 585×1530（与内置角色同一画布），并测出牌子上下外沿占图高的比例；
3. 拆出双手层——牌子白面上的手指，卡片的看板盖住牌子后，手指照样压在板沿上；
4. 把闭眼帧按同样方式处理，和立绘对不齐（牌子差 12px 以上）就先不用；
5. 按发色与服装取一个强调色，配出浅色 / 深色两套牌面颜色；
6. 打包成 zip，并出一张 `work/preview.png`：红框是测出的牌子，青色是双手层。

**每次都看一眼 preview.png**：红框没框住牌子、或手的位置不对，就重画那一张：

```sh
python3 make_pack.py generate pack.json --work work --only germany --force
```

## 上传

后台「面板设置 → 主题 → 上传主题包」，选打好的 zip。角色会按地区自动用到对应节点上；节点设置里也可以手动指定角色，
节点的「商家」会写在看板上沿的名牌上。

`reference.webp` 是生成时随提示词一起发给模型的构图参考（全身、双手捧白牌、牌子的位置与大小），不要删。
