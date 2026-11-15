#!/usr/bin/env python3
"""Bake a client mapOut.json into the gimap layout schema.

Persisted 2026-10-07 for Đỉnh Bắc Minh — the kunlun bake of the same schema was
never scripted, so this tool doubles as its re-derivation: run it on
mapOut.json with --validate against internal/gimap/kunlun_map.json to prove the
semantics, then on mapNewOut.json to produce bacminh_map.json.

Output schema (byte-compatible field order with kunlun_map.json):
  col,row,tileW,tileH,tiles,cities,Water,Floor,Block,Object,Cloud,anchor
  - tiles:  mapVec rows as ints (gid 0 void,1 event,2 city,3 obstacle,4 bridge)
  - cities: {id,tx,ty,img,flip,w,h} in client file order
  - layers: [tx,ty,img,flip,w,h]
  - anchor: [A0,B,C] from least-squares over base[] — px=A0+B*(tx-ty), py=C*(tx+ty)

Raw row format (City + every layer):  id|tx|ty|imgName|flipX|flipY|type|w|h
base row format:                      tx|ty|px|py   (px/py = tile TOP vertex)

Usage:
  bacminh_bake.py MAP.json OUT.json [--validate EXISTING.json]
"""
import json, sys

ROUND = 5


def f(x):
    return round(float(x), ROUND)


NORTH_SPINE = {  # client RemoteKunlunWarCityItem.initNorthCity (2026-11-03):
                 # art = spine/northCity/<name>, chosen by floor(id/1000) + m
    1: lambda m: "city1",
    2: lambda m: "citydown2" if m > 2 else "cityUp2",
    3: lambda m: "city3",
    4: lambda m: "city4",
    5: lambda m: "citydown5" if m > 8 else "cityUp5",
    6: lambda m: "citydown6" if m > 8 else "cityUp6",
    # 7xxx rows are bridge decorations (img kept); 8xxx outposts reuse the
    # 6xxx pair the old alias map gave them (city10<-cityUp6, city13<-citydown6).
    8: lambda m: "citydown6" if m > 8 else "cityUp6",
}


def north_cities(cities, sizes):
    """Rewrite castle rows to the client's spine choice. w,h become the
    rendered still's pixel size — the client draws the spine at NATIVE scale
    (scaleNode w/h never rescales it), so the still's content size IS the
    true map-space footprint. County (3xxx) rows flip INVERTED: the client
    sets scaleX = flipX ? 1 : -1 there (everywhere else flipX ? -1 : 1)."""
    for c in cities:
        t, m = c["id"] // 1000, c["id"] % 1000
        pick = NORTH_SPINE.get(t)
        if pick is None:
            continue
        name = pick(m)
        c["img"] = name
        c["w"], c["h"] = sizes[name]
        if t == 3:
            c["flip"] = 1 - c["flip"]


def bake(src, north_sizes=None):
    d = json.load(open(src))
    col, row = d["col"], d["row"]
    tiles = [[int(c) for c in r.split(",")] for r in d["mapVec"]]
    assert len(tiles) == row and all(len(r) == col for r in tiles), "mapVec shape"

    def parse_layer(name):
        out = []
        for raw in d.get(name, []):
            p = raw.split("|")
            img = p[3]
            if img == "undefined":
                continue
            out.append((f(p[1]), f(p[2]), img, int(p[4]),
                        int(round(float(p[7]))), int(round(float(p[8])))))
        return out

    cities = []
    for raw in d["City"]:
        p = raw.split("|")
        assert p[3] != "undefined", "undefined city img " + raw
        cities.append({"id": int(p[0]), "tx": f(p[1]), "ty": f(p[2]),
                       "img": p[3], "flip": int(p[4]),
                       "w": int(round(float(p[7]))), "h": int(round(float(p[8])))})
        assert int(p[5]) == 0, "flipY nonzero " + raw
    if north_sizes:
        north_cities(cities, north_sizes)

    # anchor from base[] (client's own tile-top-vertex table)
    pts = []
    for raw in d["base"]:
        a, b, px, py = raw.split("|")
        pts.append((float(a), float(b), float(px), float(py)))
    assert len(pts) == col * row, "base len"
    seen = {(t[0], t[1]) for t in pts}
    assert len(seen) == col * row, "base (tx,ty) not unique — field order wrong?"
    # exact solve on any 3 independent points, verify ALL points
    def fit(field):  # returns (constant, coef of tx, coef of ty)
        # px = A + B*tx + C*ty; pick (0,0),(1,0),(0,1)
        m = {(t[0], t[1]): t for t in pts}
        p00, p10, p01 = m[(0.0, 0.0)], m[(1.0, 0.0)], m[(0.0, 1.0)]
        A = p00[field]
        B = p10[field] - A
        C = p01[field] - A
        return A, B, C
    ax, bx, cx = fit(2)   # px = ax + bx*tx + cx*ty
    ay, by, cy = fit(3)   # py = ay + by*tx + cy*ty
    assert abs(ay) < 1e-6, "py has a constant term"
    assert abs(bx + cx) < 1e-6, "px not a function of (tx-ty)"
    assert abs(by - cy) < 1e-6, "py not a function of (tx+ty)"
    maxerr = 0.0
    for tx, ty, px, py in pts:
        maxerr = max(maxerr, abs(ax + bx * (tx - ty) - px), abs(ay + cy * (tx + ty) - py))
    assert maxerr < 1e-6, f"anchor fit maxerr={maxerr}"
    anchor = [int(round(ax)), int(round(bx)), int(round(cy))]

    out = {"col": col, "row": row, "tileW": d["tileWidth"], "tileH": d["tileHeight"],
           "tiles": tiles, "cities": cities}
    for lay in ("Water", "Floor", "Block", "Object", "Cloud"):
        out[lay] = [list(x) for x in parse_layer(lay)]
    out["anchor"] = anchor
    return out


def cmp_val(path, a, b, diffs):
    if isinstance(a, dict) and isinstance(b, dict):
        for k in a.keys() | b.keys():
            if k not in a or k not in b:
                diffs.append(f"{path}.{k}: missing side")
            else:
                cmp_val(f"{path}.{k}", a[k], b[k], diffs)
    elif isinstance(a, list) and isinstance(b, list):
        if len(a) != len(b):
            diffs.append(f"{path}: len {len(a)} vs {len(b)}")
        else:
            for i, (x, y) in enumerate(zip(a, b)):
                cmp_val(f"{path}[{i}]", x, y, diffs)
    elif isinstance(a, (int, float)) and isinstance(b, (int, float)):
        if abs(a - b) > 1e-9:
            diffs.append(f"{path}: {a} vs {b}")
    elif a != b:
        diffs.append(f"{path}: {a!r} vs {b!r}")


def main():
    args = sys.argv[1:]
    validate = None
    if "--validate" in args:
        i = args.index("--validate")
        validate = args[i + 1]
        del args[i:i + 2]
    north_sizes = None
    if "--north-art" in args:
        i = args.index("--north-art")
        north_sizes = json.load(open(args[i + 1]))
        del args[i:i + 2]
    src, dst = args
    baked = bake(src, north_sizes)
    if validate:
        diffs = []
        cmp_val("$", json.load(open(validate)), baked, diffs)
        if diffs:
            print(f"VALIDATION FAILED ({len(diffs)} diffs):")
            for x in diffs[:40]:
                print(" ", x)
            sys.exit(1)
        print("VALIDATION OK — semantic match with existing output")
    with open(dst, "w") as fh:
        json.dump(baked, fh, separators=(",", ":"))
    print(f"wrote {dst}: col={baked['col']} row={baked['row']} "
          f"cities={len(baked['cities'])} anchor={baked['anchor']} "
          + " ".join(f"{k}={len(baked[k])}" for k in ("Water", "Floor", "Block", "Object", "Cloud")))


if __name__ == "__main__":
    main()
