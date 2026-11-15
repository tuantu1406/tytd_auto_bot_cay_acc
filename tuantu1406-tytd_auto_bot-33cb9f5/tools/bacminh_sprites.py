#!/usr/bin/env python3
"""Pack the Đỉnh Bắc Minh sprite sheet for the gimap renderer.

Crops every imgName used by a baked map json out of the FGUI atlas set
(parse_fgui.py output over vn/RemoteKunlunWar.bin) + the loose PNGs in
images/map/, then shelf-packs them into one RGBA sheet with a
name -> [x,y,w,h] manifest (same schema as kunlun_sprites.json).

NORTH CASTLES ARE SPINE, NOT FGUI (2026-11-03): the client renders every
north castle as an animated spine skeleton (spine/northCity/<name>) chosen by
floor(cityId/1000) + id%1000 thresholds — the map json imgName is ignored.
The kunlun-aliased FGUI castle art this tool used to pack was therefore wrong
("sai hình cái nhà"). NORTH_DIR now holds the offline-rendered spine stills
(spine_render.py: idle first-frame where it fixes floating FX layers, setup
pose where idle breaks the dragon/IK), one <name>.png per client spine name:
  city1 cityUp2 citydown2 city3 city4 cityUp5 citydown5 cityUp6 citydown6
The map json cites these names directly (bacminh_bake.py rewrites them from
the city id, sizes included) — no alias table remains. Destroyed 6xxx cities
are HIDDEN by the client, so no Broken art is packed.

This is an OFFLINE bake: the server never decodes the sheet except on an
explicit Telegram photo render (server RAM policy).

Usage:
  bacminh_sprites.py MAP.json FGUI.json ATLAS_DIR LOOSE_DIR NORTH_DIR OUT.png OUT.json
"""
import json, os, sys
from PIL import Image

NORTH = {"city1", "cityUp2", "citydown2", "city3", "city4",
         "cityUp5", "citydown5", "cityUp6", "citydown6"}
SHEET_W, GAP = 2048, 1


def main():
    mapj, fg, atlas_dir, loose_dir, north_dir, out_png, out_json = sys.argv[1:8]
    m = json.load(open(mapj))
    need = {c["img"] for c in m["cities"]}
    for lay in ("Water", "Floor", "Block", "Object", "Cloud"):
        need |= {e[2] for e in m[lay]}

    fgui = json.load(open(fg))
    items = {i["name"]: i for i in fgui["items"] if i.get("type") == "image"}

    def crop(name):
        if name in NORTH:
            p = os.path.join(north_dir, name + ".png")
            if os.path.exists(p):
                return Image.open(p).convert("RGBA")
            raise SystemExit(f"no north spine still for {name} in {north_dir}")
        src = name
        it = items.get(src)
        if it is not None:
            sp = it["sprite"]
            apath = os.path.join(atlas_dir, sp["atlas"])
            if not os.path.exists(apath):  # on-disk names are prefixed with the bundle name
                pkg = fgui.get("name")
                cand = os.path.join(atlas_dir, f"{pkg}_{sp['atlas']}") if pkg else None
                if not (cand and os.path.exists(cand)):
                    suff = "_" + sp["atlas"]
                    hits = [fn for fn in os.listdir(atlas_dir) if fn.endswith(suff)]
                    if not hits:
                        raise SystemExit(f"no atlas page for {sp['atlas']} in {atlas_dir}")
                    cand = os.path.join(atlas_dir, hits[0])
                apath = cand
            page = Image.open(apath).convert("RGBA")
            x, y, w, h = sp["rect"]
            im = page.crop((x, y, x + w, y + h))
            if sp["rotated"]:
                im = im.transpose(Image.ROTATE_90)
            if sp.get("offset") or sp.get("orig"):
                print(f"WARN {name}: offset/orig not null, tight crop used",
                      file=sys.stderr)
            return im
        for cand in (src, name):
            p = os.path.join(loose_dir, cand + ".png")
            if os.path.exists(p):
                return Image.open(p).convert("RGBA")
        raise SystemExit(f"no art for {name} (alias src {src})")

    sprites = {n: crop(n) for n in sorted(need)}

    # shelf pack, tallest first
    order = sorted(sprites.items(), key=lambda kv: (-kv[1].height, kv[0]))
    x = y = rowh = 0
    pos = {}
    for n, im in order:
        if x and x + im.width > SHEET_W:
            x, y = 0, y + rowh + GAP
            rowh = 0
        pos[n] = (x, y)
        x += im.width + GAP
        rowh = max(rowh, im.height)
    H = y + rowh
    sheet = Image.new("RGBA", (SHEET_W, H), (0, 0, 0, 0))
    man = {}
    for n, im in sprites.items():
        px, py = pos[n]
        sheet.paste(im, (px, py))
        man[n] = [px, py, im.width, im.height]
    sheet.save(out_png)
    with open(out_json, "w") as fh:
        json.dump(man, fh, separators=(",", ":"))
    print(f"{out_png}: {SHEET_W}x{H}, {len(man)} sprites")


if __name__ == "__main__":
    main()
