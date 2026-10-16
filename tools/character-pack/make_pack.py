#!/usr/bin/env python3
"""TZA Probe 角色包生成器：把「地区 → 角色设定」变成一个能在后台直接上传的角色包（zip）。

两步，可以分开跑：

  generate  按设定调图像接口出图：每个角色一张立绘 + 一张闭眼帧（在立绘上只改眼睛）。
            原图缓存在 <work>/raw/，重跑只补缺的，不会重复花钱；--force 才重画。
  build     处理缓存里的原图：去白底 → 找牌子 → 裁成 585×1530 → 测牌子上下沿比例 →
            拆出双手层 → 对齐闭眼帧 → 按服装取配色（设定里写了就用设定的）→ 打包成 zip，
            另出一张 preview.png 供人检查（牌子框画在图上，框歪了说明那张要重画）。

  all       generate + build。

用法：
  python3 make_pack.py all pack.json --work work/ --out europe.zip
  python3 make_pack.py build pack.json --work work/        # 原图自己放进 work/raw/<id>.png 也行

图像接口（兼容 OpenAI 的 /chat/completions，能返回图片的模型，例如 Nano Banana Pro）：
  TZA_IMAGE_API_BASE   如 https://api.example.com/v1
  TZA_IMAGE_API_KEY    密钥（只从环境变量读，不写进任何文件、不打印）
  TZA_IMAGE_MODEL      默认 gemini-3-pro-image

设定文件 pack.json 的格式见 README.md；角色包格式见仓库 docs/THEMES.md。
依赖：Pillow、numpy（python3 -m pip install pillow numpy）。
"""
import argparse
import base64
import colorsys
import io
import json
import os
import re
import sys
import urllib.error
import urllib.request
import zipfile

try:
    import numpy as np
    from PIL import Image, ImageDraw
except ImportError:
    sys.exit("需要 Pillow 与 numpy：python3 -m pip install pillow numpy")

HERE = os.path.dirname(os.path.abspath(__file__))
REFERENCE = os.path.join(HERE, "reference.webp")
CANVAS_W, CANVAS_H = 585, 1530
ID = re.compile(r"^[a-z][a-z0-9-]{0,31}$")
PACK_ID = re.compile(r"^[a-z][a-z0-9-]{1,31}$")

LAYOUT = """Create a NEW original anime character illustration. Use the attached image ONLY as a strict layout and style template: copy its art style (soft clean anime cel shading, thin clean lineart, gentle colouring), its body proportions, its framing and its exact pose. Do NOT copy the template girl's face, hair or clothes.

Layout rules (must match the template exactly):
- Tall portrait, same canvas proportions as the template, plain pure white background, nothing else in the scene.
- Full body, standing straight, facing the viewer, head near the top edge and both shoes fully visible near the bottom edge, centered horizontally.
- She holds a blank, pure white rectangular board horizontally in front of her chest with both hands, in exactly the same position and size as the board in the template: the board's top edge sits at about 28% of the image height, its bottom edge at about 42%, and it spans the same width as in the template. Her fingers curl over the top edge of the board from behind, like in the template.
- The board is completely blank and has a thin light-grey outline. No text, letters, numbers, logos or flags anywhere in the image: not on the board, not on her clothes, not on accessories (a name tag, if any, must be blank).

The character:
"""

BLINK = """Edit this image: close both of her eyes gently, as in a natural relaxed blink (curved closed eyelids with eyelashes). Change NOTHING else: same pose, same framing, same canvas size, same colours, same board, same background, every other pixel identical. Output a single image only."""


# ---------------------------------------------------------------------------- spec

def load_spec(path):
    with open(path, encoding="utf-8") as f:
        spec = json.load(f)
    if not PACK_ID.match(spec.get("id", "")):
        sys.exit("pack.json 的 id 须为 2–32 位小写字母、数字或连字符，以字母开头")
    if not spec.get("name"):
        sys.exit("pack.json 要有 name")
    chars = spec.get("characters") or []
    if not chars:
        sys.exit("pack.json 里至少要有一个角色（characters）")
    seen = set()
    for c in chars:
        if not ID.match(c.get("id", "")) or c["id"] in seen:
            sys.exit(f"角色 id {c.get('id')!r} 不合规或重复（小写字母、数字、连字符，以字母开头）")
        seen.add(c["id"])
        if not c.get("name") or not c.get("design"):
            sys.exit(f"角色 {c['id']} 要有 name 与 design（英文外形描述）")
    return spec


# ---------------------------------------------------------------------------- image API

def api_config():
    base = os.environ.get("TZA_IMAGE_API_BASE", "").rstrip("/")
    key = os.environ.get("TZA_IMAGE_API_KEY", "")
    if not base or not key:
        sys.exit("先设置 TZA_IMAGE_API_BASE 与 TZA_IMAGE_API_KEY（兼容 OpenAI 的 /chat/completions 图像接口）")
    return base, key, os.environ.get("TZA_IMAGE_MODEL", "gemini-3-pro-image")


def data_url(path):
    ext = os.path.splitext(path)[1].lower().lstrip(".")
    mime = {"jpg": "jpeg"}.get(ext, ext)
    with open(path, "rb") as f:
        return f"data:image/{mime};base64," + base64.b64encode(f.read()).decode()


def ask_image(prompt, image_path):
    """One chat call with a text prompt and one image; returns the first image in the reply."""
    base, key, model = api_config()
    body = {
        "model": model,
        "messages": [{"role": "user", "content": [
            {"type": "text", "text": prompt},
            {"type": "image_url", "image_url": {"url": data_url(image_path)}},
        ]}],
    }
    req = urllib.request.Request(base + "/chat/completions", data=json.dumps(body).encode(),
                                 headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=600) as r:
            raw = r.read()
    except urllib.error.HTTPError as e:
        sys.exit(f"图像接口返回 {e.code}：{e.read()[:500].decode(errors='replace')}")
    found = re.search(rb"data:image/(?:png|jpeg|webp);base64,([A-Za-z0-9+/=]+)", raw)
    if not found:
        try:
            message = json.loads(raw)["choices"][0]["message"].get("content")
        except Exception:
            message = raw[:300]
        sys.exit(f"接口没有返回图片：{str(message)[:300]}")
    return Image.open(io.BytesIO(base64.b64decode(found.group(1)))).convert("RGB")


def generate(spec, work, only=None, force=False):
    raw_dir = os.path.join(work, "raw")
    os.makedirs(raw_dir, exist_ok=True)
    for c in spec["characters"]:
        if only and c["id"] not in only:
            continue
        art = os.path.join(raw_dir, c["id"] + ".png")
        blink = os.path.join(raw_dir, c["id"] + "-blink.png")
        if force or not os.path.exists(art):
            print(f"[{c['id']}] 生成立绘 …", flush=True)
            ask_image(LAYOUT + c["design"].strip() + "\n\nOutput a single image only.", REFERENCE).save(art)
            if os.path.exists(blink):
                os.remove(blink)  # 立绘重画了，旧的闭眼帧对不上
        else:
            print(f"[{c['id']}] 立绘已有，跳过（--force 重画）")
        if c.get("blink", True) and (force or not os.path.exists(blink)):
            print(f"[{c['id']}] 生成闭眼帧 …", flush=True)
            ask_image(BLINK, art).save(blink)


# ---------------------------------------------------------------------------- processing

def remove_background(im):
    """Flood-fill the white background from the image border to transparent.
    The board's inside stays opaque: its outline closes it off."""
    rgb = im.convert("RGB")
    a = np.asarray(rgb).astype(np.int16)
    h, w = a.shape[:2]
    # 近白处标 255，其它 0；从四条边上每个还没填过的近白像素出发，把连通的近白区域填成 128。
    # .copy()：fromarray 出来的图共享 numpy 的只读缓冲，floodfill 在它上面会悄悄什么都不做。
    mask = Image.fromarray(np.where(a.min(axis=2) >= 236, 255, 0).astype(np.uint8), "L").copy()
    border = [(x, y) for x in range(w) for y in (0, h - 1)] + [(x, y) for y in range(h) for x in (0, w - 1)]
    for xy in border:
        if mask.getpixel(xy) == 255:
            ImageDraw.floodfill(mask, xy, 128)
    seen = np.asarray(mask) == 128
    out = np.dstack([np.asarray(rgb), np.where(seen, 0, 255).astype(np.uint8)])
    # 留白与人物之间那一圈抗锯齿的浅色像素：按接近白色的程度给半透明，免得透明底上一圈白边。
    edge = (~seen) & np.pad(seen, 1)[1:-1, 2:] | (~seen) & np.pad(seen, 1)[1:-1, :-2] | (~seen) & np.pad(seen, 1)[2:, 1:-1] | (~seen) & np.pad(seen, 1)[:-2, 1:-1]
    lum = a.mean(axis=2)
    soft = edge & (lum > 200)
    out[..., 3][soft] = np.clip((255 - lum[soft]) * 255 / 55, 0, 255).astype(np.uint8)
    return Image.fromarray(out, "RGBA")


def find_board(im):
    """The board is the largest opaque, near-white rectangle in the 12–70% height band.
    Returns (left, top, right, bottom) of its outer edge in image pixels."""
    a = np.asarray(im)
    h, w = a.shape[:2]
    white = (a[..., 3] > 200) & (a[..., :3].min(axis=2) >= 240)
    rows = []
    for y in range(int(h * 0.12), int(h * 0.70)):
        row = white[y]
        # 这一行里最长的一段连续白
        best, start, run_start = (0, 0, 0), None, 0
        for x in range(w):
            if row[x]:
                if start is None:
                    start = x
            elif start is not None:
                if x - start > best[0]:
                    best = (x - start, start, x)
                start = None
        if start is not None and w - start > best[0]:
            best = (w - start, start, w)
        if best[0] >= w * 0.28:
            rows.append((y, best[1], best[2]))
    if not rows:
        raise ValueError("找不到牌子：图里没有一块够宽的白色牌子")
    # 最长的一段连续行 = 牌面
    blocks, cur = [], [rows[0]]
    for r in rows[1:]:
        if r[0] == cur[-1][0] + 1:
            cur.append(r)
        else:
            blocks.append(cur)
            cur = [r]
    blocks.append(cur)
    block = max(blocks, key=len)
    if len(block) < h * 0.05:
        raise ValueError("找到的白色区域太矮，不像牌子")
    lefts = sorted(r[1] for r in block)
    rights = sorted(r[2] for r in block)
    # 左右取中位数：手指压住的那几行不影响
    left, top, right, bottom = lefts[len(lefts) // 2], block[0][0], rights[len(rights) // 2], block[-1][0]
    # 从牌面往外一行行 / 一列列扩，把浅灰描边与描边外那圈白一起算进牌子：只要这一行（列）的中段
    # 大多是「不透明、偏亮、几乎不带颜色」的像素就继续。卡片的看板按这个外沿盖，扩不够描边会从板沿露出来。
    rgb = a[..., :3].astype(np.int16)
    boardish = (a[..., 3] > 200) & (rgb.mean(axis=2) > 150) & ((rgb.max(axis=2) - rgb.min(axis=2)) < 26)
    cx0, cx1 = left + (right - left) // 5, right - (right - left) // 5
    cy0, cy1 = top + (bottom - top) // 4, bottom - (bottom - top) // 4
    while top > 0 and boardish[top - 1, cx0:cx1].mean() > 0.7:
        top -= 1
    while bottom < h - 1 and boardish[bottom + 1, cx0:cx1].mean() > 0.7:
        bottom += 1
    while left > 0 and boardish[cy0:cy1, left - 1].mean() > 0.7:
        left -= 1
    while right < w - 1 and boardish[cy0:cy1, right + 1].mean() > 0.7:
        right += 1
    return (left, top, right, bottom)


def fit_canvas(im, board):
    """Crop around the board's centre at the 585:1530 aspect and scale to the canvas."""
    w, h = im.size
    left, top, right, bottom = board
    cw = round(h * CANVAS_W / CANVAS_H)
    if right - left + 12 > cw:  # 牌子比画布还宽：加高画布（上下补透明）来放下它
        cw = right - left + 12
        ch = round(cw * CANVAS_H / CANVAS_W)
        pad = (ch - h) // 2
        canvas = Image.new("RGBA", (w, ch), (0, 0, 0, 0))
        canvas.paste(im, (0, pad))
        im, h, top, bottom = canvas, ch, top + pad, bottom + pad
    cx = (left + right) // 2
    x0 = max(0, min(w - cw, cx - cw // 2))
    if x0 < 0 or cw > w:
        canvas = Image.new("RGBA", (cw, h), (0, 0, 0, 0))
        canvas.paste(im, ((cw - w) // 2, 0))
        im, x0, left, right = canvas, 0, left + (cw - w) // 2, right + (cw - w) // 2
    cropped = im.crop((x0, 0, x0 + cw, h)).resize((CANVAS_W, CANVAS_H), Image.LANCZOS)
    s = CANVAS_H / h
    return cropped, (round((left - x0) * s), round(top * s), round((right - x0) * s), round(bottom * s))


def inner_box(a, board):
    """From the board's outer edge, step inward past the outline to the first pure-white row /
    column (sampled along the board's middle, away from the fingers)."""
    left, top, right, bottom = board
    white = (a[..., 3] > 200) & (a[..., :3].min(axis=2) >= 240)
    cx0, cx1 = left + (right - left) // 3, right - (right - left) // 3
    cy0, cy1 = top + (bottom - top) // 3, bottom - (bottom - top) // 3
    t, b_, l, r = top, bottom, left, right
    while t < bottom and white[t, cx0:cx1].mean() < 0.9:
        t += 1
    while b_ > top and white[b_, cx0:cx1].mean() < 0.9:
        b_ -= 1
    while l < right and white[cy0:cy1, l].mean() < 0.9:
        l += 1
    while r > left and white[cy0:cy1, r].mean() < 0.9:
        r -= 1
    return l, t, r, b_


def hands_layer(im, board):
    """Fingers that the HTML board would hide. Inside the board's white face every opaque,
    non-white pixel is a finger — the face itself is blank. The outline band and the strip just
    above the board's top edge (the card board's 6px lip covers it) keep only the finger columns,
    so the lip still shows as a clean edge everywhere else instead of the outline or clothes."""
    a = np.asarray(im).astype(np.int16)
    h, w = a.shape[:2]
    rgb = a[..., :3]
    left, top, right, bottom = board
    il, it, ir, ib = inner_box(a, board)
    finger = (a[..., 3] > 200) & (rgb.min(axis=2) < 232)
    keep = np.zeros((h, w), bool)
    keep[it:ib, il:ir] = finger[it:ib, il:ir]
    # 有手指的列（白色牌面上四分之一里出现过手指像素），左右各放宽 3px；只有这些列往上续到板沿之上
    cols = keep[it:it + max(4, (ib - it) // 4)].any(axis=0)
    # 不到 8px 宽的一段不是手指（多半是牌子圆角处的描边），连同它在板内的像素一起丢掉
    x = 0
    while x < w:
        if cols[x]:
            end = x
            while end < w and cols[end]:
                end += 1
            if end - x < 8:
                cols[x:end] = False
                keep[it:ib, x:end] = False
            x = end
        else:
            x += 1
    cols = np.convolve(cols.astype(int), np.ones(7, int), "same") > 0
    band = slice(max(0, top - 10), it)
    keep[band] = finger[band] & cols[None, :]
    out = np.zeros((h, w, 4), np.uint8)
    out[..., :3] = rgb.astype(np.uint8)
    out[..., 3] = np.where(keep, 255, 0)
    return Image.fromarray(out, "RGBA")


def palette_from(im, board):
    """An accent from the outfit's most common saturated hue, and the card colours mixed from it."""
    a = np.asarray(im).astype(np.float32) / 255
    h = a.shape[0]
    _, top, _, bottom = board
    body = np.concatenate([a[: max(1, top - 4)], a[bottom + 4: int(h * 0.75)]])
    px = body[body[..., 3] > 0.9][:, :3]
    if len(px) == 0:
        accent = (1.0, 0.31, 0.6)
    else:
        hsv = np.array([colorsys.rgb_to_hsv(*p) for p in px[:: max(1, len(px) // 20000)]])
        # 二次元的发色、衣服常是低饱和的灰调色（灰青、灰粉），门槛放低到 0.12 才数得到头发
        mask = (hsv[:, 1] > 0.12) & (hsv[:, 2] > 0.25) & (hsv[:, 2] < 0.95)
        # 去掉肤色：偏粉到橙黄（色相跨过 0 那一段）、不太饱和、偏亮
        skin = ((hsv[:, 0] > 0.92) | (hsv[:, 0] < 0.11)) & (hsv[:, 1] < 0.45) & (hsv[:, 2] > 0.6)
        mask &= ~skin
        if mask.sum() < 50:
            accent = (1.0, 0.31, 0.6)
        else:
            sel = hsv[mask]
            bins = np.floor(sel[:, 0] * 24).astype(int) % 24
            top_bin = np.bincount(bins, minlength=24).argmax()
            hue = (top_bin + 0.5) / 24
            s = float(np.clip(np.median(sel[bins == top_bin, 1]), 0.45, 0.85))
            accent = colorsys.hsv_to_rgb(hue, s, 0.72)
    return palette_around(accent)


def mix(c, d, t):
    return tuple(c[i] * (1 - t) + d[i] * t for i in range(3))


def hexc(c):
    return "#" + "".join(f"{round(max(0, min(1, v)) * 255):02x}" for v in c)


def palette_around(accent):
    white, night = (1, 1, 1), (0.08, 0.075, 0.12)
    bright = mix(accent, white, 0.35)
    return {
        "light": {"board": hexc(mix(accent, white, 0.93)), "line": hexc(mix(accent, white, 0.6)),
                  "track": hexc(mix(accent, white, 0.82)), "accent": hexc(accent)},
        "dark": {"board": hexc(mix(accent, night, 0.86)), "cell": hexc(mix(accent, night, 0.8)),
                 "line": hexc(mix(accent, night, 0.6)), "track": hexc(mix(accent, night, 0.74)), "accent": hexc(bright)},
    }


def process_one(raw_path):
    im = remove_background(Image.open(raw_path))
    board = find_board(im)
    return fit_canvas(im, board)


def blink_timing(key):
    """同一个角色每次都一样、不同角色错开的眨眼节奏（几张卡同时眨眼很假）：按「包 id/角色 id」散列。"""
    h = 7
    for ch in key:
        h = (h * 31 + ord(ch)) & 0xFFFFFFFF
    return f"{5 + (h % 17) / 10:.1f}s", f"{0.2 + ((h >> 5) % 13) / 10:.1f}s"


def decoration(entry, pack_id):
    """一个角色 → 附加包里的一个装饰项。图片与数值的名字是二次元主题的约定（themes/anime/README.md）：
    面板把它们原样写成卡片上的 --deco-<名字>，二次元的样式表再接到自己的变量上。"""
    d = {"id": entry["id"], "name": entry["name"]}
    for k in ("nodes", "regions", "default"):
        if entry.get(k):
            d[k] = entry[k]
    d["images"] = {k: entry[k] for k in ("art", "hands", "blink") if entry.get(k)}
    dur, delay = blink_timing(f"{pack_id}/{entry['id']}")
    v = {"plate-top": str(entry["plate"]["top"]), "plate-bottom": str(entry["plate"]["bottom"]), "blink-dur": dur, "blink-delay": delay}
    for mode, suffix in (("light", "light"), ("dark", "dark")):
        pal = entry["palette"][mode]
        accent = pal["accent"].lstrip("#")
        v[f"board-{suffix}"] = pal["board"]
        v[f"cell-{suffix}"] = pal.get("cell") or ("rgb(255 255 255 / 0.62)" if mode == "light" else pal["board"])
        v[f"line-{suffix}"] = pal["line"]
        v[f"track-{suffix}"] = pal["track"]
        v[f"accent-{suffix}"] = pal["accent"]
        v[f"accent-rgb-{suffix}"] = ", ".join(str(int(accent[i:i + 2], 16)) for i in (0, 2, 4))
    d["vars"] = v
    return d


def build(spec, work, out_zip):
    raw_dir, layer_dir = os.path.join(work, "raw"), os.path.join(work, "layers")
    os.makedirs(layer_dir, exist_ok=True)
    chars, files, previews = [], {}, []
    for c in spec["characters"]:
        cid = c["id"]
        raw = next((os.path.join(raw_dir, cid + e) for e in (".png", ".jpg", ".webp") if os.path.exists(os.path.join(raw_dir, cid + e))), None)
        if not raw:
            sys.exit(f"[{cid}] 没有原图 {raw_dir}/{cid}.png：先跑 generate，或自己放一张进去")
        art, board = process_one(raw)
        left, top, right, bottom = board
        plate = {"top": round(top / CANVAS_H, 4), "bottom": round(bottom / CANVAS_H, 4)}
        if not (0.1 <= plate["top"] <= 0.7 and plate["bottom"] >= plate["top"] + 0.03 and plate["bottom"] <= 0.9):
            sys.exit(f"[{cid}] 牌子位置不对（上沿 {plate['top']}、下沿 {plate['bottom']}），这张要重画：python3 make_pack.py generate … --only {cid} --force")
        entry = {"id": cid, "name": c["name"], "art": f"{cid}.webp", "plate": plate}
        for k in ("regions", "nodes"):
            if c.get(k):
                entry[k] = c[k]
        if c.get("default"):
            entry["default"] = True
        files[f"{cid}.webp"] = art
        files[f"{cid}-hands.webp"] = hands_layer(art, board)
        entry["hands"] = f"{cid}-hands.webp"
        blink_raw = os.path.join(raw_dir, cid + "-blink.png")
        if os.path.exists(blink_raw):
            blink, bboard = process_one(blink_raw)
            drift = max(abs(bboard[i] - board[i]) for i in range(4))
            if drift > 12:
                print(f"[{cid}] 闭眼帧和立绘没对齐（牌子差 {drift}px），先不用它；重画：generate --only {cid} --force", file=sys.stderr)
            else:
                files[f"{cid}-blink.webp"] = blink
                entry["blink"] = f"{cid}-blink.webp"
        entry["palette"] = c.get("palette") or palette_from(art, board)
        chars.append(entry)
        # 预览：立绘 + 牌子框（红）+ 双手层（青），框歪了一眼能看出来
        p = Image.new("RGBA", art.size, (235, 238, 245, 255))
        p.alpha_composite(art)
        hands = files[f"{cid}-hands.webp"].copy()
        tint = Image.new("RGBA", art.size, (0, 200, 200, 0))
        tint.putalpha(hands.getchannel("A").point(lambda v: 90 if v else 0))
        p.alpha_composite(tint)
        ImageDraw.Draw(p).rectangle(board, outline=(230, 30, 60, 255), width=3)
        previews.append((cid, p))
        print(f"[{cid}] 牌子 {plate['top']:.3f}–{plate['bottom']:.3f} · 强调色 {entry['palette']['light']['accent']} · 闭眼帧 {'有' if 'blink' in entry else '无'}")

    # 附加包：依附官方二次元主题（id anime），每个角色是一个装饰项（docs/THEMES.md「附加包」）。
    manifest = {"id": spec["id"], "type": "addon", "for": "anime", "name": spec["name"]}
    for k in ("version", "author"):
        if spec.get(k):
            manifest[k] = spec[k]
    manifest["decorations"] = [decoration(c, spec["id"]) for c in chars]
    for name, image in files.items():
        image.save(os.path.join(layer_dir, name), "WEBP", quality=86, method=6)
    with open(os.path.join(layer_dir, "theme.json"), "w", encoding="utf-8") as f:
        json.dump(manifest, f, ensure_ascii=False, indent=2)
    with zipfile.ZipFile(out_zip, "w", zipfile.ZIP_DEFLATED) as z:
        z.write(os.path.join(layer_dir, "theme.json"), "theme.json")
        for name in files:
            z.write(os.path.join(layer_dir, name), name)
    sheet = Image.new("RGBA", (CANVAS_W * len(previews) // 2 + 8 * len(previews), CANVAS_H // 2 + 30), (255, 255, 255, 255))
    for i, (cid, p) in enumerate(previews):
        sheet.paste(p.resize((CANVAS_W // 2, CANVAS_H // 2)), (i * (CANVAS_W // 2 + 8), 30))
        ImageDraw.Draw(sheet).text((i * (CANVAS_W // 2 + 8) + 4, 8), cid, fill=(30, 30, 40, 255))
    sheet.convert("RGB").save(os.path.join(work, "preview.png"))
    size = os.path.getsize(out_zip)
    print(f"已打包 {out_zip}（{size / 1024:.0f} KB，{len(chars)} 个角色）；检查图 {os.path.join(work, 'preview.png')}")


def selftest():
    """Offline check of the processing steps on a synthetic picture: no API, no files."""
    w, h = 848, 1264
    im = Image.new("RGB", (w, h), (255, 255, 255))
    d = ImageDraw.Draw(im)
    d.rectangle((330, 60, 520, 1220), fill=(60, 140, 150))                    # 身体（灰青）
    d.rounded_rectangle((190, 390, 660, 560), radius=8, fill=(255, 255, 255), outline=(205, 210, 220), width=5)  # 牌子
    for x in (260, 560):                                                      # 两只手压在牌沿上
        d.ellipse((x, 370, x + 40, 420), fill=(248, 212, 200), outline=(120, 80, 70), width=2)
    cut = remove_background(im)
    alpha = np.asarray(cut)[..., 3]
    assert alpha[5, 5] == 0 and alpha[475, 425] == 255, "去底：背景该透明、牌面该保留"
    board = find_board(cut)
    assert abs(board[1] - 390) <= 4 and abs(board[3] - 560) <= 4 and abs(board[0] - 190) <= 4 and abs(board[2] - 660) <= 4, f"找牌子：{board}"
    art, b = fit_canvas(cut, board)
    assert art.size == (CANVAS_W, CANVAS_H)
    top = b[1] / CANVAS_H
    assert abs(top - 390 / h) < 0.01, f"牌子上沿比例：{top}"
    hands = np.asarray(hands_layer(art, b))[..., 3]
    ys, xs = np.nonzero(hands)
    assert len(xs) > 200, "双手层：手指没拆出来"
    assert ys.max() < b[1] + (b[3] - b[1]) // 2, "双手层：不该含牌子下半部分"
    assert (hands[:, : b[0] + 3] == 0).all() and (hands[:, b[2] - 3:] == 0).all(), "双手层：不该含牌子左右描边"
    pal = palette_from(art, b)
    hue = colorsys.rgb_to_hsv(*[int(pal["light"]["accent"][i:i + 2], 16) / 255 for i in (1, 3, 5)])[0]
    assert 0.42 < hue < 0.58, f"配色：强调色该取灰青，得到 {pal['light']['accent']}"
    deco = decoration({"id": "test", "name": "测试", "regions": ["德国"], "art": "test.webp", "hands": "test-hands.webp", "plate": {"top": 0.3, "bottom": 0.45}, "palette": pal}, "europe")
    assert deco["images"] == {"art": "test.webp", "hands": "test-hands.webp"} and deco["regions"] == ["德国"], f"装饰项：{deco}"
    assert deco["vars"]["plate-top"] == "0.3" and deco["vars"]["accent-rgb-light"].count(",") == 2, f"装饰项数值：{deco['vars']}"
    assert all(len(k) <= 24 and len(v) <= 64 for k, v in deco["vars"].items()), "装饰项数值超出清单的长度限制"
    print("selftest ok")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("command", choices=["generate", "build", "all", "selftest"])
    ap.add_argument("spec", nargs="?", help="设定文件 pack.json")
    ap.add_argument("--work", default="work", help="工作目录（缓存原图、中间图层与检查图）")
    ap.add_argument("--out", help="输出的角色包 zip（默认 <work>/<包 id>.zip）")
    ap.add_argument("--only", nargs="*", help="只处理这些角色 id")
    ap.add_argument("--force", action="store_true", help="generate 时重画已有的原图")
    args = ap.parse_args()
    if args.command == "selftest":
        selftest()
        return
    if not args.spec:
        ap.error("需要设定文件 pack.json")
    spec = load_spec(args.spec)
    os.makedirs(args.work, exist_ok=True)
    if args.command in ("generate", "all"):
        generate(spec, args.work, set(args.only or []) or None, args.force)
    if args.command in ("build", "all"):
        build(spec, args.work, args.out or os.path.join(args.work, spec["id"] + ".zip"))


if __name__ == "__main__":
    main()
