package gimap

// Đỉnh Bắc Minh layout/geometry/render tests. The north board shares the
// KunlunWar engine (compositor, palette, slot rule) but NOT the geometry:
// 119×119 grid, anchor [12733,107,63], 81 cities — several of which stand on
// gid-3 mountain or gid-4 bridge tiles (client initNorthCity plates over
// obstacle art), unlike Côn Lôn where every city sat on playable ground.

import (
	"bytes"
	"encoding/json"
	"image/png"
	"math"
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func bmMiniFixture(t *testing.T) []byte {
	t.Helper()
	rsp := &proto.KunlunWarMiniMapInfoRespMsg{
		CityInfo: []*proto.KunlunWarMiniCityInfo{
			{CityId: pb.Int32(3001), UnionId: pb.Int64(202)},
			{CityId: pb.Int32(1001), UnionId: pb.Int64(101), HaveFight: pb.Bool(true)},
			{CityId: pb.Int32(3002), UnionId: pb.Int64(202)},
		},
		CellInfo: []*proto.KunlunWarMiniCellInfo{
			{Index: pb.Int32(59*119 + 10), UnionId: pb.Int64(303)}, // gid-1 event cell
		},
		BaseData: []*proto.KunlunUnionBaseData{
			{UnionId: pb.Int64(101), Name: pb.String("Aethel")},
			{UnionId: pb.Int64(202), Name: pb.String("Bravo")},
			{UnionId: pb.Int64(303), Name: pb.String("Charlie")},
		},
	}
	b, err := pb.Marshal(rsp)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBacMinhLayout(t *testing.T) {
	mp, err := bacminhLayout()
	if err != nil {
		t.Fatal(err)
	}
	if mp.Col != 119 || mp.Row != 119 || len(mp.Tiles) != 119 {
		t.Fatalf("grid %dx%d rows=%d", mp.Col, mp.Row, len(mp.Tiles))
	}
	for _, r := range mp.Tiles {
		if len(r) != 119 {
			t.Fatalf("row width %d", len(r))
		}
		for _, g := range r {
			if g < 0 || g > 4 {
				t.Fatalf("gid %d outside {0..4}", g)
			}
		}
	}
	if len(mp.Cities) != 81 {
		t.Fatalf("cities %d, want 81 (live 219919 cityInfo count)", len(mp.Cities))
	}
	// Client mapNewOut order starts with the 7xxx home plate.
	if c := mp.Cities[0]; c.ID != 7012 {
		t.Fatalf("first city %+v — client order must start 7012", c)
	}
	// Anchor affine re-fit from the north base[] points (bake script asserted
	// max error 0 over all 14161 anchors; the two slope terms are the engine
	// constants, only the x-origin moved with the bigger grid).
	if len(mp.Anchor) != 3 || mp.Anchor[0] != 12733 || mp.Anchor[1] != 107 || mp.Anchor[2] != 63 {
		t.Fatalf("anchor %v, want [12733 107 63]", mp.Anchor)
	}
	// The centre castle exists and stands on its city tile.
	for _, c := range mp.Cities {
		if c.ID == 1001 {
			if int(c.TX) != 60 || int(c.TY) != 60 || mp.Tiles[60][60] != 2 {
				t.Fatalf("1001 at (%v,%v) gid %d", c.TX, c.TY, mp.Tiles[60][60])
			}
			return
		}
	}
	t.Fatal("no city 1001 on the north board")
}

// All 81 city posIndexes must resolve through the exported lookups (they are
// the web march targets), and passability must follow exactly the engine
// gid rule — the north simply has cities on gid 3/4, which is the client's
// own choice, not a data error.
func TestBacMinhCityPoints(t *testing.T) {
	mp, err := bacminhLayout()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range mp.Cities {
		pos := int64(c.TY)*int64(mp.Col) + int64(c.TX)
		if pt, ok := BacMinhCityPoint(uint64(c.ID)); !ok || int64(pt) != pos {
			t.Fatalf("BacMinhCityPoint(%d) = %d,%v want %d", c.ID, pt, ok, pos)
		}
		name, ok := BacMinhCityNameAt(pos)
		if !ok || name == "" {
			t.Fatalf("city %d at %d has no name (%q,%v)", c.ID, pos, name, ok)
		}
	}
	if _, ok := BacMinhCityPoint(999999); ok {
		t.Fatal("unknown city must not resolve")
	}
}

// Click inverse-projection must round-trip every passable cell of the
// 119² board (same math the /bacminh page runs as giKuCellAt).
func TestBmCellClickSolve(t *testing.T) {
	st, err := StreamStatic(StreamBacMinh)
	if err != nil {
		t.Fatal(err)
	}
	mp, err := bacminhLayout()
	if err != nil {
		t.Fatal(err)
	}
	g := st.Ku
	if g == nil {
		t.Fatal("north static must carry the click grid")
	}
	if g.Col != mp.Col || g.Row != mp.Row || len(g.Grid) != g.Col*g.Row {
		t.Fatalf("grid %dx%d len %d", g.Col, g.Row, len(g.Grid))
	}
	x0, y0 := g.OX, g.OY
	solve := func(nx, ny float64) (int, int) {
		u := (x0 + nx*st.WorldW - float64(g.Col-1)*g.HW) / g.HW
		v := (y0 + ny*st.WorldH) / g.HH
		return int(math.Round((u + v) / 2)), int(math.Round((v - u) / 2))
	}
	passable := 0
	for ty := 0; ty < mp.Row; ty++ {
		for tx := 0; tx < mp.Col; tx++ {
			gid := mp.Tiles[ty][tx]
			if int(g.Grid[ty*g.Col+tx]-'0') != gid {
				t.Fatalf("grid digit (%d,%d) %c want %d", tx, ty, g.Grid[ty*g.Col+tx], gid)
			}
			pos := int64(ty)*int64(mp.Col) + int64(tx)
			if ok := BacMinhCellPassable(pos); ok != (gid == 1 || gid == 2 || gid == 4) {
				t.Fatalf("passable(%d,%d) gid %d = %v", tx, ty, gid, ok)
			}
			if gid == 0 || gid == 3 {
				continue
			}
			passable++
			px, py := kuTileCenter(float64(tx), float64(ty), mp.Col)
			sx, sy := solve((px-x0)/st.WorldW, (py-y0)/st.WorldH)
			if sx != tx || sy != ty {
				t.Fatalf("click on (%d,%d) solved to (%d,%d)", tx, ty, sx, sy)
			}
		}
	}
	// Live north cellInfo carries 8821 rows = exactly the gid-1 count; the
	// city/bridge tiles ride on top (716 + 309).
	if passable < 9000 {
		t.Fatalf("only %d passable cells", passable)
	}
	if BacMinhCols() != 119 {
		t.Fatalf("BacMinhCols = %d", BacMinhCols())
	}
	if !BacMinhCellPassable(59*119+10) || BacMinhCellPassable(-1) || BacMinhCellPassable(119*119) {
		t.Fatal("range/passable sanity")
	}
	posOf := map[uint64]uint64{}
	for _, c := range st.Cities {
		posOf[c.P] = c.ID
	}
	if posOf[uint64(60*119+60)] != 1001 {
		t.Fatalf("cell 60,60 static city = %d", posOf[uint64(60*119+60)])
	}
}

func TestBmUnionSlotsOrder(t *testing.T) {
	blob := bmMiniFixture(t)
	rsp, err := DecodeBacMinhMini(blob)
	if err != nil {
		t.Fatal(err)
	}
	mp, _ := bacminhLayout()
	slots := kuUnionSlots(mp, rsp)
	// Expected = first owning city in the north client's OWN city order
	// (7xxx plateaus first, then 3xxx…), then cell-only unions.
	want := map[int64]int{}
	next := 0
	owner := map[int]int64{}
	for _, c := range rsp.GetCityInfo() {
		owner[int(c.GetCityId())] = c.GetUnionId()
	}
	for _, c := range mp.Cities {
		u := owner[c.ID]
		if u == 0 {
			continue
		}
		if _, seen := want[u]; !seen {
			want[u] = next
			next++
		}
	}
	want[303] = next // cell-only union lands last
	for u, s := range want {
		if slots[u] != s {
			t.Fatalf("slot(%d) = %d, want %d (slots %v)", u, slots[u], s, slots)
		}
	}
	if got := BacMinhUnionSlot(blob, 303); got != want[303] {
		t.Fatalf("BacMinhUnionSlot %d want %d", got, want[303])
	}
}

func TestBacMinhStreamFrame(t *testing.T) {
	blob := bmMiniFixture(t)
	sm, err := Stream(StreamBacMinh, blob)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Which != StreamBacMinh || sm.Art != "bm" || sm.Bg != "bg.jpg" {
		t.Fatalf("which=%s art=%s bg=%s", sm.Which, sm.Art, sm.Bg)
	}
	if len(sm.Cities) != 81 {
		t.Fatalf("cities %d", len(sm.Cities))
	}
	slots, err := BacMinhSlotsOf(blob)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uint64]StreamCity{}
	for _, c := range sm.Cities {
		byID[c.ID] = c
	}
	c1 := byID[1001]
	if c1.Owner != "Aethel" || c1.CI == nil || !c1.Fight {
		t.Fatalf("1001 %+v", c1)
	}
	if c1.Color != kuColorHex(kuColorVec[slots[101]%len(kuColorVec)]) {
		t.Fatalf("1001 color %s — palette rule drifted", c1.Color)
	}
	if sm.Ku == nil || len(sm.Ku.Cells) != 1 || sm.Ku.Cells[0].P != 59*119+10 {
		t.Fatalf("frame cells %+v", sm.Ku)
	}
}

func TestBmBoxAlignment(t *testing.T) {
	st, err := StreamStatic(StreamBacMinh)
	if err != nil {
		t.Fatal(err)
	}
	if st.Which != StreamBacMinh || st.Art != "bm" {
		t.Fatalf("static which=%s art=%s", st.Which, st.Art)
	}
	sm, err := Stream(StreamBacMinh, bmMiniFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	sx := map[uint64][2]float64{}
	for _, c := range st.Cities {
		sx[c.ID] = [2]float64{c.X, c.Y}
	}
	for _, c := range sm.Cities {
		p := sx[c.ID]
		if dx, dy := c.X-p[0], c.Y-p[1]; dx*dx+dy*dy > 1e-12 {
			t.Fatalf("city %d misaligned: static (%f,%f) vs frame (%f,%f)", c.ID, p[0], p[1], c.X, c.Y)
		}
	}
	board, err := RenderBacMinhBoard(bmMiniFixture(t), 1200)
	if err != nil {
		t.Fatal(err)
	}
	hdr, err := png.DecodeConfig(bytes.NewReader(board))
	if err != nil {
		t.Fatal(err)
	}
	mp, _ := bacminhLayout()
	_, _, bw, bh := kuWorldBox(bmPackMain, mp)
	want := float64(1200) * bh / bw
	if math.Abs(float64(hdr.Height)-want) > 2 {
		t.Fatalf("board PNG height %d, world-box aspect wants %f — markers would misalign", hdr.Height, want)
	}
}

func TestRenderBacMinh(t *testing.T) {
	img, err := RenderBacMinh(bmMiniFixture(t), 1400)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(img, []byte("\x89PNG")) {
		t.Fatalf("not a PNG (%d bytes)", len(img))
	}
	// The web base plate renders from an EMPTY payload too (the /bacminh page
	// fetches it before any live frame).
	img, err = RenderBacMinh(nil, 1400)
	if err != nil {
		t.Fatal(err)
	}
	if len(img) < 8 || string(img[1:4]) != "PNG" {
		t.Fatal("base render is not a PNG")
	}
	// SERVER-RAM policy: after a render the pack must hold ZERO pixels.
	bmPackMain.pxMu.Lock()
	loaded := bmPackMain.pixelsLoaded
	bmPackMain.pxMu.Unlock()
	if loaded {
		t.Fatal("north sheet still pinned after RenderBacMinh")
	}
}

func TestBacMinhSceneJSON(t *testing.T) {
	b := BacMinhSceneJSON()
	if len(b) == 0 {
		t.Fatal("empty scene")
	}
	var sc kuScene
	if err := json.Unmarshal(b, &sc); err != nil {
		t.Fatalf("scene not valid JSON: %v", err)
	}
	if sc.Col != 119 || sc.Row != 119 || len(sc.Grid) != 119*119 {
		t.Fatalf("grid %dx%d len=%d", sc.Col, sc.Row, len(sc.Grid))
	}
	if sc.Box[2] <= 0 || sc.Box[3] <= 0 {
		t.Fatalf("bad box %v", sc.Box)
	}
	if sc.HW != kuHalfW || sc.HH != kuHalfH || sc.Fade != kuFade {
		t.Fatalf("geometry constants drifted: %+v", sc)
	}
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
	if len(sc.Layers.Ground) < 5000 || len(sc.Layers.Deco) < 100 {
		t.Fatalf("layers too thin: ground %d deco %d", len(sc.Layers.Ground), len(sc.Layers.Deco))
	}
	for i := 1; i < len(sc.Layers.Deco); i++ {
		a, b2 := sc.Layers.Deco[i-1], sc.Layers.Deco[i]
		ca, cb := a.Y+a.H/2, b2.Y+b2.H/2
		if ca > cb || (ca == cb && a.X > b2.X) {
			t.Fatalf("deco unsorted at %d", i)
		}
	}
	if len(sc.Cities) != 81 {
		t.Fatalf("cities %d", len(sc.Cities))
	}
	for _, c := range sc.Cities {
		if c.Pos < 0 || int(c.Pos) >= len(sc.Grid) {
			t.Fatalf("city %d pos %d", c.ID, c.Pos)
		}
		if g := sc.Grid[c.Pos]; g != '1' && g != '2' && g != '3' && g != '4' {
			t.Fatalf("city %d on void gid %c", c.ID, g)
		}
		if c.Rank != c.ID%1000 {
			t.Fatalf("city %d rank %d", c.ID, c.Rank)
		}
	}
	if sc.Sheet[0] != 2048 || sc.Sheet[1] <= 0 {
		t.Fatalf("sheet size %v", sc.Sheet)
	}
	if len(BmSheetPNG()) == 0 || len(BmBgJPEG()) == 0 {
		t.Fatal("north sheet/bg embeds empty")
	}
	if len(sc.Assets.Lines) != 5 {
		t.Fatalf("lines %d", len(sc.Assets.Lines))
	}
	for _, n := range sc.Assets.Lines {
		if data, ok := BmArtFile(n); !ok || len(data) == 0 {
			t.Fatalf("line art %q missing", n)
		}
	}
	if data, ok := BmArtFile(sc.Assets.Smallmap); !ok || len(data) == 0 {
		t.Fatalf("smallmap %q missing", sc.Assets.Smallmap)
	}
	if sc.Assets.Smallmap != "bmzd_smallmap.png" {
		t.Fatalf("smallmap %q", sc.Assets.Smallmap)
	}
	if string(BacMinhSceneJSON()) != string(b) {
		t.Fatal("scene JSON not memoised/stable")
	}
	for name, r := range sc.Rects {
		if r[0] < 0 || r[1] < 0 || r[0]+r[2] > sc.Sheet[0] || r[1]+r[3] > sc.Sheet[1] {
			t.Fatalf("rect %q %v outside sheet %v", name, r, sc.Sheet)
		}
	}
}

func TestBmArtNamesExposeLines(t *testing.T) {
	names := map[string]bool{}
	for _, n := range BmArtNames() {
		names[n] = true
	}
	for _, n := range KuLineNames() { // the engine ships ONE line-art family
		if !names[n] {
			t.Fatalf("line %q not in BmArtNames %v", n, names)
		}
	}
	if !names["bmzd_smallmap.png"] {
		t.Fatal("north smallmap not embedded under its client name")
	}
}

// TestNorthCityCenteredPlacement pins the 2026-11-03 fix for "thành vẽ lệch
// với chân đế" (user, referencing Thiên Ma Tiên Vực): north city plates are
// placed CENTRED on their cell — the scene bakes Y = anchor + hh + h/2 (the
// browser's plate rule top = y−h then straddles the cell centre) — and every
// scene city carries boff = hh + h/2 so the name hangs under the plate
// bottom. Kunlun must stay raised (bottom edge on the anchor, no boff).
func TestNorthCityCenteredPlacement(t *testing.T) {
	mp, err := bacminhLayout()
	if err != nil {
		t.Fatal(err)
	}
	var sc kuScene
	if err := json.Unmarshal(BacMinhSceneJSON(), &sc); err != nil {
		t.Fatal(err)
	}
	x0, y0, _, _ := kuWorldBox(bmPackMain, mp)
	offX := kuOffX(mp) + x0
	findPlate := func(list []kuSceneSprite, img string, px, off float64) kuSceneSprite {
		for _, sp := range list {
			if sp.Flags&2 != 0 && sp.Img == img && sp.X-(px-off) < 0.01 && sp.X-(px-off) > -0.01 {
				return sp
			}
		}
		t.Fatalf("no city plate sprite %q at x=%v", img, px-off)
		return kuSceneSprite{}
	}
	for _, c := range mp.Cities {
		px, py := kuAnchor(mp, c.TX, c.TY)
		sp := findPlate(sc.Layers.Deco, c.Img, px, offX)
		want := py - y0 + kuHalfH + float64(c.H)/2
		if d := sp.Y - want; d > 0.01 || d < -0.01 {
			t.Fatalf("city %d plate Y %v, want cell-centred %v (d %v)", c.ID, sp.Y, want, d)
		}
	}
	for _, c := range sc.Cities {
		if c.Boff == 0 {
			t.Fatalf("north city %d missing boff", c.ID)
		}
	}
	// Côn Lôn regression: raised plates, anchor bottom, no boff.
	var ks kuScene
	if err := json.Unmarshal(KunlunSceneJSON(), &ks); err != nil {
		t.Fatal(err)
	}
	kmp, err := kunlunLayout()
	if err != nil {
		t.Fatal(err)
	}
	kx0, ky0, _, _ := kuWorldBox(kuPackMain, kmp)
	koffX := kuOffX(kmp) + kx0
	for _, c := range kmp.Cities {
		px, py := kuAnchor(kmp, c.TX, c.TY)
		sp := findPlate(ks.Layers.Deco, c.Img, px, koffX)
		if d := sp.Y - (py - ky0); d > 0.01 || d < -0.01 {
			t.Fatalf("kunlun city %d plate Y %v, want raised on anchor %v", c.ID, sp.Y, py-ky0)
		}
	}
	for _, c := range ks.Cities {
		if c.Boff != 0 {
			t.Fatalf("kunlun city %d carries boff %v", c.ID, c.Boff)
		}
	}
}
