#!/usr/bin/env python3
"""Bake Luân Hồi Bí Cảnh battle-map art into internal/gimap embeds.

Inputs (downloaded from the pinned CDN bundle
https://cdn-xddq-vn.hwrescdn.com/71004001/remoteAssets/remoteRebornTrial/<ver>/):
  bg0.jpg … bg3.jpg            750×1800 battle scene tiles (client: MAP_BG_* )
  mountainIcon.png             the union base building sprite (490×376)

Outputs (written next to this tool's --out dir, then embedded):
  luanhoi_bg.jpg    3000×1800 stitched scene background (jpg q78)
  luanhoi_base.png  mountainIcon with the FGUI atlas bleed REMOVED: only the
                    connected component seeded inside the keep is left (the
                    game packs other sprites' scraps around it).

Usage:
  python3 luanhoi_art.py --dir <download-dir> --out <gimap-dir> [--report]
"""
import argparse, sys
from collections import deque
from PIL import Image


def keep_component(im, seed):
    """Return (kept_image, stats) keeping only the alpha-connected component
    containing seed=(x,y) via 4-neighbour flood over alpha>8."""
    g = im.convert("RGBA")
    w, h = g.size
    px = g.load()
    alpha = [px[x, y][3] > 8 for y in range(h) for x in range(w)]

    def idx(x, y):
        return y * w + x

    seen = [False] * (w * h)
    sx, sy = seed
    if not alpha[idx(sx, sy)]:
        sys.exit(f"seed {seed} is transparent — pick a point inside the base")
    q = deque([(sx, sy)])
    seen[idx(sx, sy)] = True
    comp = 0
    minx, miny, maxx, maxy = sx, sy, sx, sy
    while q:
        x, y = q.popleft()
        comp += 1
        minx, miny = min(minx, x), min(miny, y)
        maxx, maxy = max(maxx, x), max(maxy, y)
        for nx, ny in ((x + 1, y), (x - 1, y), (x, y + 1), (x, y - 1)):
            if 0 <= nx < w and 0 <= ny < h and not seen[idx(nx, ny)] and alpha[idx(nx, ny)]:
                seen[idx(nx, ny)] = True
                q.append((nx, ny))
    out = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    src = list(g.getdata())
    dst = [(r, gg, b, a) if seen[i] else (0, 0, 0, 0) for i, (r, gg, b, a) in enumerate(src)]
    out.putdata(dst)
    bbox = (minx, miny, maxx + 1, maxy + 1)
    return out, (comp, w * h, bbox)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True, help="download dir with the raw assets")
    ap.add_argument("--out", required=True, help="internal/gimap target dir")
    ap.add_argument("--seed", default="245,300", help="flood seed x,y inside the base")
    args = ap.parse_args()

    # 1. background: four tiles left→right (client MAP_BG_COUNT=4 × MAP_BG_WIDTH=750)
    tiles = [Image.open(f"{args.dir}/bg{i}.jpg") for i in range(4)]
    W = sum(t.width for t in tiles)
    H = max(t.height for t in tiles)
    bg = Image.new("RGB", (W, H))
    x = 0
    for t in tiles:
        bg.paste(t, (x, 0))
        x += t.width
    bg.save(f"{args.out}/luanhoi_bg.jpg", quality=78, optimize=True)
    print(f"luanhoi_bg.jpg {bg.size}")

    # 2. base sprite: drop disconnected atlas bleed
    sx, sy = (int(v) for v in args.seed.split(","))
    base, (comp, total, bbox) = keep_component(Image.open(f"{args.dir}/mountainIcon.png"), (sx, sy))
    kept = comp / total
    base = base.crop(bbox)
    base.save(f"{args.out}/luanhoi_base.png", optimize=True)
    print(f"luanhoi_base.png size={base.size} kept={comp}/{total} ({kept:.1%}) bbox={bbox}")
    if kept < 0.4 or kept > 0.99:
        print("!! kept fraction looks off — inspect the flood seed", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
