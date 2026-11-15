package gimap

// Scene-bundle tests: everything the /kunlun mini app needs to reproduce
// the Go compositor pass must be IN the JSON (rects for every sprite, a grid
// cell for every tile, city plates standing on gid-2 tiles) and every asset
// it names must actually be embedded and served.

import (
	"encoding/json"
	"testing"
)

func TestKunlunSceneJSON(t *testing.T) {
	b := KunlunSceneJSON()
	if len(b) == 0 {
		t.Fatal("empty scene")
	}
	var sc kuScene
	if err := json.Unmarshal(b, &sc); err != nil {
		t.Fatalf("scene not valid JSON: %v", err)
	}
	if sc.Col != 61 || sc.Row != 61 || len(sc.Grid) != 61*61 {
		t.Fatalf("grid %dx%d len=%d", sc.Col, sc.Row, len(sc.Grid))
	}
	if sc.Box[2] <= 0 || sc.Box[3] <= 0 {
		t.Fatalf("bad box %v", sc.Box)
	}
	if sc.HW != kuHalfW || sc.HH != kuHalfH || sc.Fade != kuFade {
		t.Fatalf("geometry constants drifted: %+v", sc)
	}
	if len(sc.Palette) == 0 || len(sc.Palette) > 12 {
		t.Fatalf("palette %d", len(sc.Palette))
	}
	// Every sprite on every layer needs a real rect, sizes, and a legal flag
	// word; coordinates must sit inside the world box (± sprite overhang).
	check := func(list []kuSceneSprite, layer string) {
		for i, sp := range list {
			r, ok := sc.Rects[sp.Img]
			if !ok || r[2] <= 0 || r[3] <= 0 {
				t.Fatalf("%s[%d] %q has no usable rect", layer, i, sp.Img)
			}
			if sp.W <= 0 || sp.H <= 0 {
				t.Fatalf("%s[%d] %q bad size %vx%v", layer, i, sp.Img, sp.W, sp.H)
			}
			if sp.Flags&^7 != 0 {
				t.Fatalf("%s[%d] unknown flags %d", layer, i, sp.Flags)
			}
			if sp.X < -sp.W || sp.X > sc.Box[2]+sp.W || sp.Y < -sp.H || sp.Y > sc.Box[3]+sp.H {
				t.Fatalf("%s[%d] %q at %v,%v outside box %v", layer, i, sp.Img, sp.X, sp.Y, sc.Box)
			}
		}
	}
	check(sc.Layers.Ground, "ground")
	check(sc.Layers.Deco, "deco")
	if len(sc.Layers.Ground) < 1000 || len(sc.Layers.Deco) < 100 {
		t.Fatalf("layers too thin: ground %d deco %d", len(sc.Layers.Ground), len(sc.Layers.Deco))
	}
	// Deco must arrive pre-sorted by depth = centre-y (Y + H/2 — the client's
	// zIndex = data.y + data.height/2), ties by X, so the browser blits in
	// server order.
	for i := 1; i < len(sc.Layers.Deco); i++ {
		a, b := sc.Layers.Deco[i-1], sc.Layers.Deco[i]
		ca, cb := a.Y+a.H/2, b.Y+b.H/2
		if ca > cb || (ca == cb && a.X > b.X) {
			t.Fatalf("deco unsorted at %d", i)
		}
	}
	// Cities: 37 plates, each standing on its own gid-2 tile.
	if len(sc.Cities) != 37 {
		t.Fatalf("cities %d", len(sc.Cities))
	}
	for _, c := range sc.Cities {
		if c.Pos < 0 || int(c.Pos) >= len(sc.Grid) {
			t.Fatalf("city %d pos %d", c.ID, c.Pos)
		}
		// Cities sit on passable ground: gid 2 (city tile) or, for cities the
		// client spawns on event cells, gid 1. 6007 is the void anchor city —
		// it stands on gid 0 by design (see tytd-kunlunwar-port notes).
		g := sc.Grid[c.Pos]
		if c.ID == 6007 {
			if g != '0' {
				t.Fatalf("anchor city 6007 on gid %c (want 0)", g)
			}
		} else if g != '2' && g != '1' {
			t.Fatalf("city %d on gid %c (want 1 or 2)", c.ID, g)
		}
		if c.Rank != c.ID%1000 {
			t.Fatalf("city %d rank %d", c.ID, c.Rank)
		}
	}
	// Assets: named files must resolve through the same embeds mapart serves.
	if len(sc.Sheet) != 2 || sc.Sheet[0] <= 0 || sc.Sheet[1] <= 0 {
		t.Fatalf("sheet size %v", sc.Sheet)
	}
	if len(KuSheetPNG()) == 0 || len(KuBgJPEG()) == 0 {
		t.Fatal("sheet/bg embeds empty")
	}
	if len(sc.Assets.Lines) != 5 {
		t.Fatalf("lines %d", len(sc.Assets.Lines))
	}
	for _, n := range sc.Assets.Lines {
		if data, ok := KuArtFile(n); !ok || len(data) == 0 {
			t.Fatalf("line art %q missing", n)
		}
	}
	if data, ok := KuArtFile(sc.Assets.Smallmap); !ok || len(data) == 0 {
		t.Fatalf("smallmap %q missing", sc.Assets.Smallmap)
	}
	// Memoised: byte-stable across calls (the browser caches this for a day).
	if string(KunlunSceneJSON()) != string(b) {
		t.Fatal("scene JSON not memoised/stable")
	}
	// Rects must stay inside the sheet bitmap.
	for name, r := range sc.Rects {
		if r[0] < 0 || r[1] < 0 || r[0]+r[2] > sc.Sheet[0] || r[1]+r[3] > sc.Sheet[1] {
			t.Fatalf("rect %q %v outside sheet %v", name, r, sc.Sheet)
		}
	}
}

func TestKunlunFrameKuCells(t *testing.T) {
	sm, err := Stream(StreamKunlun, kuMiniFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if sm.Ku == nil {
		t.Fatal("frame missing Ku")
	}
	if len(sm.Ku.Cells) != 1 {
		t.Fatalf("cells %+v", sm.Ku.Cells)
	}
	cell := sm.Ku.Cells[0]
	if cell.P != 32*61+32 {
		t.Fatalf("cell pos %d", cell.P)
	}
	// Charlie (303) only holds a cell: first-appearance order (client city
	// order, then cell rows) puts it AFTER the two city unions, and the slot
	// must match the exported lookup the mini app uses for route colors.
	if cell.S != 2 || cell.S != KuUnionSlot(kuMiniFixture(t), 303) {
		t.Fatalf("cell slot %d, want 2", cell.S)
	}
}

func TestKuArtNamesExposeLines(t *testing.T) {
	names := map[string]bool{}
	for _, n := range KuArtNames() {
		names[n] = true
	}
	for _, n := range KuLineNames() {
		if !names[n] {
			t.Fatalf("line %q not in KuArtNames %v", n, names)
		}
	}
}
