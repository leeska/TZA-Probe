#!/usr/bin/env python3
"""二次元立绘「多图层」拆分（看板那套改法的地基）。

把一张「她自己双手捧着白牌子」的整图拆成三层，页面里的顺序是
    人物层 → HTML 看板（板上排节点数据）→ 双手层（压在看板上沿）
于是看板上沿是 HTML 自己的边框，不会再有原图那块牌子的描边/阴影残线；手又真的是
从板上面搭下来的，手指压过板沿那一段与接触阴影都在手层里。

  (a) 人物层   web/anime/<id>.webp         牌子那块矩形整块清掉（含被它压住的双手）；
                                           矩形内用 CPA 图生图补画的身体填回去（那份补画是
                                           被看板完全盖住的保险，露不出来）
  (b) 双手层   web/anime/<id>-hands.webp   牌面以内、非白（= 手指/轮廓）的像素 → 真 alpha；
                                           板沿以上那一条按「手指列」向上续接，手指才不会在
                                           板沿处被横切；再补一层很淡的接触阴影
  (c) 闭眼帧   web/anime/<id>-blink.webp   原闭眼整图做同样的矩形清理（眨眼层与人物层同画布、
                                           同缩放，眼睛以外不许动）

几何（牌子上外沿 / 下外沿 / 左外沿 / 右外沿的行列）逐台实测，见 PLATE：这是整套版式唯一的
硬数据，人物层要清的矩形、看板上沿的高度、收起时板留多高，全都由它推出来。

用法（源图与闭眼帧先拷一份到 SRC，脚本可重复跑）：
    python3 scripts/anime-split-layers.py [--src /root/work/anime-src] [--gen /tmp/layers] [--only jp]
"""
import argparse, os, sys
import numpy as np
from PIL import Image, ImageDraw, ImageFilter

# 立绘 px：上外沿 top、下外沿 bot、左外沿 l、右外沿 r（全部实测，见会话记录）
PLATE = {
    "jp-nosla-gen2": dict(top=463, bot=671, l=8, r=578),
    "de-nosla-fra": dict(top=464, bot=648, l=8, r=522),
    "us-vmiss-tri": dict(top=447, bot=672, l=24, r=543),
    "us-bwh-dc9": dict(top=458, bot=656, l=7, r=539),
}
# CPA 图生图那份补画的文件名前缀（/tmp/layers/<tag>-genA-*.png）
TAG = {
    "jp-nosla-gen2": "jp",
    "de-nosla-fra": "de",
    "us-vmiss-tri": "vmiss",
    "us-bwh-dc9": "bwh",
}
CLEAR_PAD = 3          # 清矩形左右各多清几像素（吃掉描边的抗锯齿）
PLATE_EDGE = 9         # 牌子左右描边的宽度：牌面（白）从这里开始
WHITE_MIN = 246        # 牌面白的判定：三通道最小值 > 它
ALPHA_RAMP = 12        # 手层 alpha 的软边斜坡（照度差多少算全不透明）
SHADOW_ALPHA = 0.16    # 手指压在板面上的接触阴影强度
EDGE_PAD = 14          # 牌子下外沿以下要顺手抹掉的高度：牌子自己的下沿倒角 + 那条描边线


def repair_plate_edge(arr, bot, x0, x1):
    """抹掉牌子下外沿以下那条「牌子自己的下边」。

    清矩形清到 bot 为止（bot = 牌子的下外沿），可牌子还带着一圈下沿倒角 + 一条描边线，
    正好落在 bot+1 起的十来行里 —— 原图里它是牌子的一部分，展开态被看板盖住看不出来，
    但收起态看板只剩头部，它就会横贯人物、被当成残线（gpt-6.1-sol 验收就卡在这一条）。
    这一段在人物层里其实没有别的内容（下面是裙子/腿），所以按列做一次竖向线性插值
    （从矩形底那一行渐变到下面那一行），倒角与线一起抹平，裙子照旧连过去。
    人物层与闭眼帧都要做（同一张画布，不然眨眼那一帧会把线闪出来）。
    """
    y0, y1 = bot + 1, min(bot + EDGE_PAD, arr.shape[0] - 2)
    if y1 <= y0:
        return
    top_row = arr[y0 - 1, x0:x1].astype(np.float32)
    bot_row = arr[y1 + 1, x0:x1].astype(np.float32)
    for y in range(y0, y1 + 1):
        t = (y - (y0 - 1)) / ((y1 + 1) - (y0 - 1))
        arr[y, x0:x1] = (top_row * (1 - t) + bot_row * t).round().astype(arr.dtype)



def load_rgba(p):
    return Image.open(p).convert("RGBA")


def interior_top(a, rows, x0, x1, need=0.45):
    """牌面（一整行都接近纯白）从哪一行开始。"""
    for y in rows:
        row = a[y, x0:x1]
        if row.size and ((row[..., 3] > 200) & (row[..., :3].min(axis=1) > 248)).mean() > need:
            return y
    raise SystemExit("找不到牌面行")


def fill_holes(mask):
    """把手指内部的高光洞补上：从画布四边把「掩膜之外的连通区」灌满，剩下的空的就是洞。
    （numpy 里没有 scipy，用 PIL 的 MaxFilter 当膨胀算子，一次推 4px。）"""
    bg = ~mask
    reach = np.zeros_like(bg)
    reach[0] = bg[0]
    reach[-1] = bg[-1]
    reach[:, 0] = bg[:, 0]
    reach[:, -1] = bg[:, -1]
    r = Image.fromarray((reach * 255).astype(np.uint8), "L")
    for _ in range(600):
        before = int(reach.sum())
        r = r.filter(ImageFilter.MaxFilter(9))
        reach = (np.asarray(r) > 0) & bg
        r = Image.fromarray((reach * 255).astype(np.uint8), "L")
        if int(reach.sum()) == before:
            break
    holes = bg & ~reach
    return mask | holes, int(holes.sum())


def split(pid, src, gen, out_dir, report):
    plate = PLATE[pid]
    W = None
    art = load_rgba(f"{src}/{pid}-open.webp")
    blink = load_rgba(f"{src}/{pid}-blink.webp")
    W, H = art.size
    assert blink.size == (W, H), "闭眼帧要与立绘同尺寸"
    a = np.asarray(art).astype(np.int16)
    ys = np.arange(H)[:, None]
    top, bot, L, R = plate["top"], plate["bot"], plate["l"], plate["r"]
    x0, x1 = max(0, L - CLEAR_PAD), min(W, R + CLEAR_PAD + 1)

    # ---------- (a) 人物层：牌子矩形清成透明 ----------
    char = art.copy()
    ca = np.asarray(char).copy()
    ca[top:bot + 1, x0:x1, 3] = 0
    # 矩形内用图生图的补画填回去（与立绘同画布：按尺寸缩放后落在同一个原点）
    gen_path = None
    if gen:
        for k in range(4):
            p = f"{gen}/{TAG[pid]}-genA-{k}.png"
            if os.path.exists(p):
                gen_path = p
                break
    if gen_path:
        g = load_rgba(gen_path).resize((W, H), Image.LANCZOS)
        ga = np.asarray(g)
        ca[top:bot + 1, x0:x1] = ga[top:bot + 1, x0:x1]
    repair_plate_edge(ca, bot, x0, x1)
    Image.fromarray(ca.astype(np.uint8), "RGBA").save(f"{out_dir}/{pid}.webp", "WEBP", quality=92, method=6)

    # ---------- 闭眼帧：同一矩形清掉（补画不留，人物层在它下面） ----------
    ba = np.asarray(blink).copy()
    ba[top:bot + 1, x0:x1, 3] = 0
    repair_plate_edge(ba, bot, x0, x1)
    Image.fromarray(ba.astype(np.uint8), "RGBA").save(f"{out_dir}/{pid}-blink.webp", "WEBP", quality=92, method=6)

    # ---------- (b) 双手层 ----------
    # 牌面以内、非白 = 手指与轮廓；板沿那条描边（整行非白）不能算进来，所以只用牌面以下的
    # 行来取手，再按列向上续接到板沿——手指在板沿上下是同一批列。
    ix0, ix1 = L + PLATE_EDGE, R - PLATE_EDGE + 1
    i_top = interior_top(a, range(top, top + 40), ix0, ix1)
    lum = a[..., :3].min(axis=2)
    # 手窗：牌面顶上十来行里「有深色轮廓」的列 = 手指所在的两块。牌面自己的一点点侧向渐变
    # （实测最暗 240）够不到这个门限，不会被当成手。
    band = (a[i_top:i_top + 12, ix0:ix1, 3] > 128) & (lum[i_top:i_top + 12, ix0:ix1] < 200)
    cols = band.any(axis=0)
    wins, s = [], None
    for i, v in enumerate(cols):
        if v and s is None:
            s = i
        elif not v and s is not None:
            wins.append((max(0, s - 3), i + 3)); s = None
    if s is not None:
        wins.append((max(0, s - 3), len(cols) + 3))
    # 手指压到板沿下面多深：手窗里最后一行还有深色轮廓的地方（这类画法最多压三十来像素，
    # 再深就是牌子下沿那条描边了，不能算进手里）
    deep = [y for y in range(i_top, min(bot + 1, i_top + 40)) if any(((a[y, ix0 + w0:ix0 + w1, 3] > 128) & (lum[y, ix0 + w0:ix0 + w1] < 200)).any() for w0, w1 in wins)]
    f_bot = (max(deep) + 2) if deep else i_top
    mask = np.zeros((H, W), bool)
    for w0, w1 in wins:
        xa, xb = ix0 + w0, ix0 + w1
        mask[i_top:f_bot + 1, xa:xb] = ((a[i_top:f_bot + 1, xa:xb, 3] > 128) & (lum[i_top:f_bot + 1, xa:xb] < WHITE_MIN))
    # 向上续接：板沿到牌面之间那一带，只有手指列有内容
    for y in range(top, i_top):
        mask[y, ix0:ix1] = mask[i_top, ix0:ix1]
    # 手指内部的高光会被「非白」漏掉 → 填洞
    mask, n_holes = fill_holes(mask)
    # 手层 alpha：实心处 1，边缘按「离牌面白有多远」给软坡
    alpha = np.clip((250 - lum) / ALPHA_RAMP, 0, 1)
    alpha = np.where(a[..., 3] > 128, alpha, 0)
    alpha = np.where(mask, np.maximum(alpha, 0.45), 0.0)
    rgba = np.zeros((H, W, 4), np.uint8)
    rgba[..., :3] = a[..., :3].astype(np.uint8)
    rgba[..., 3] = (alpha * 255).astype(np.uint8)

    # 接触阴影：手指掩膜往下挪一点、糊一下，只留在手外面那几条（手上不加）
    m = Image.fromarray((mask * 255).astype(np.uint8), "L")
    shadow_src = m.filter(ImageFilter.MaxFilter(5))          # 往外补一圈
    shadow = Image.new("L", (W, H), 0)
    shadow.paste(shadow_src, (0, 4))                          # 整体下移 4px
    shadow = shadow.filter(ImageFilter.GaussianBlur(2.2))
    sh = np.asarray(shadow).astype(np.float32) / 255.0
    sh = sh * (1 - alpha)                                     # 手自己那几像素不压暗
    sh = sh * SHADOW_ALPHA
    sh[f_bot + 8:] = 0
    # 合成：阴影画在手下面（先阴影后手）
    out = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    sh_img = Image.new("RGBA", (W, H), (58, 48, 54, 0))
    sh_img.putalpha(Image.fromarray((sh * 255).astype(np.uint8), "L"))
    out.alpha_composite(sh_img)
    out.alpha_composite(Image.fromarray(rgba, "RGBA"))
    out.save(f"{out_dir}/{pid}-hands.webp", "WEBP", quality=92, method=6)

    solid = int((alpha > 0.5).sum())
    ys2, xs2 = np.where(alpha > 0.05)
    bb = None if len(xs2) == 0 else (int(xs2.min()), int(ys2.min()), int(xs2.max()), int(ys2.max()))
    report.append((pid, (W, H), "plate_top=%d(%.4f)" % (top, top / H), "interior_top=%d" % i_top,
                   "plate_bot=%d(%.4f)" % (bot, bot / H), "x=%d..%d" % (L, R),
                   "hands_px=%d" % solid, "holes=%d" % n_holes, "f_bot=%d" % f_bot, "hands_bbox=%s" % (bb,),
                   "gen=%s" % (os.path.basename(gen_path) if gen_path else "-")))
    return art


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default="/root/work/anime-src")
    ap.add_argument("--gen", default="/tmp/layers")
    ap.add_argument("--out", default="/root/work/tza-probe/web/anime")
    ap.add_argument("--only", default="")
    ap.add_argument("--repair-only", action="store_true",
                    help="只就地补抹「牌子下沿倒角 + 描边线」那条残线（人物层 / 闭眼帧），不重跑拆层")
    args = ap.parse_args()
    report = []
    if args.repair_only:
        for pid in PLATE:
            if args.only and args.only != pid and args.only != TAG[pid]:
                continue
            plate = PLATE[pid]
            for suf in ("", "-blink"):
                p = os.path.join(args.out, f"{pid}{suf}.webp")
                if not os.path.exists(p):
                    print("missing", p, file=sys.stderr)
                    continue
                im = Image.open(p).convert("RGBA")
                a = np.asarray(im).copy()
                x0, x1 = max(0, plate["l"] - CLEAR_PAD), min(im.size[0], plate["r"] + CLEAR_PAD + 1)
                repair_plate_edge(a, plate["bot"], x0, x1)
                Image.fromarray(a.astype(np.uint8), "RGBA").save(p, "WEBP", quality=92, method=6)
                print("repaired", os.path.basename(p))
        return
    for pid in PLATE:
        if args.only and args.only != pid and args.only != TAG[pid]:
            continue
        split(pid, args.src, args.gen, args.out, report)
    for r in report:
        print(" | ".join(str(x) for x in r))
    for pid in PLATE:
        for suf in ("", "-hands", "-blink"):
            p = f"{args.out}/{pid}{suf}.webp"
            if os.path.exists(p):
                print("%-34s %8d B" % (os.path.basename(p), os.path.getsize(p)))


if __name__ == "__main__":
    main()
