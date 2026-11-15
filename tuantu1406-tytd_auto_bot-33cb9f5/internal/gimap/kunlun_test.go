package gimap

import (
	"bytes"
	"image/png"
	"math"
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func kuMiniFixture(t *testing.T) []byte {
	t.Helper()
	rsp := &proto.KunlunWarMiniMapInfoRespMsg{
		CityInfo: []*proto.KunlunWarMiniCityInfo{
			{CityId: pb.Int32(2001), UnionId: pb.Int64(202)}, // gateway first in proto order
			{CityId: pb.Int32(1001), UnionId: pb.Int64(101), HaveFight: pb.Bool(true)},
			{CityId: pb.Int32(2002), UnionId: pb.Int64(202)},
		},
		CellInfo: []*proto.KunlunWarMiniCellInfo{
			{Index: pb.Int32(32*61 + 32), UnionId: pb.Int64(303)},
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

func TestKunlunLayout(t *testing.T) {
	mp, err := kunlunLayout()
	if err != nil {
		t.Fatal(err)
	}
	if mp.Col != 61 || mp.Row != 61 || len(mp.Tiles) != 61 {
		t.Fatalf("grid %dx%d rows=%d", mp.Col, mp.Row, len(mp.Tiles))
	}
	for _, r := range mp.Tiles {
		if len(r) != 61 {
			t.Fatalf("row width %d", len(r))
		}
		for _, g := range r {
			if g < 0 || g > 4 {
				t.Fatalf("gid %d outside {0..4}", g)
			}
		}
	}
	if len(mp.Cities) != 37 {
		t.Fatalf("cities %d", len(mp.Cities))
	}
	// Centre city 1001 sits exactly on the map middle tile (32,32).
	if c := mp.Cities[0]; c.ID != 1001 || c.TX != 32 || c.TY != 32 {
		t.Fatalf("first city %+v — client order must start at 1001|(32,32)", c)
	}
	// Cities stand on city ground (the 1001 core) or event ground (the
	// gateways/strongholds on gid-1 plateaus). One exception: the client
	// anchors home city 6007 on the void beyond the right edge — its sprite
	// hangs off the grid (mapOut row "6007|60|31|…|332|243").
	for _, c := range mp.Cities {
		tx, ty := int(c.TX), int(c.TY)
		g := mp.Tiles[ty][tx]
		if (g == 1 || g == 2) || (g == 0 && c.ID/1000 == 6) {
			continue
		}
		t.Fatalf("city %d on tile (%d,%d) gid %d", c.ID, tx, ty, g)
	}
}

func TestKuStatic(t *testing.T) {
	st, err := StreamStatic(StreamKunlun)
	if err != nil {
		t.Fatal(err)
	}
	if st.Which != StreamKunlun || st.Art != "ku" {
		t.Fatalf("which=%s art=%s", st.Which, st.Art)
	}
	if len(st.Cities) != 37 {
		t.Fatalf("cities %d", len(st.Cities))
	}
	wantTiles := 0
	mp, _ := kunlunLayout()
	for _, r := range mp.Tiles {
		for _, g := range r {
			if g != 0 {
				wantTiles++
			}
		}
	}
	if len(st.Tiles) != wantTiles {
		t.Fatalf("tiles %d, want %d (voids omitted)", len(st.Tiles), wantTiles)
	}
	for _, c := range st.Cities {
		if c.X < 0 || c.X > 1 || c.Y < 0 || c.Y > 1 {
			t.Fatalf("city %d out of unit box (%f,%f)", c.ID, c.X, c.Y)
		}
		if c.ID == 1001 && c.Name == "" {
			t.Fatal("city 1001 has no localized name")
		}
	}
}

// The web map ships no markers: a click anywhere inverse-projects to a cell
// (client's getPosIndex inverse) and orders the move. Every passable tile
// must round-trip exactly, the grid string must match the tile matrix, and
// city cells must resolve to their names.
func TestKuCellClickSolve(t *testing.T) {
	st, err := StreamStatic(StreamKunlun)
	if err != nil {
		t.Fatal(err)
	}
	mp, err := kunlunLayout()
	if err != nil {
		t.Fatal(err)
	}
	g := st.Ku
	if g == nil {
		t.Fatal("kunlun static must carry the click grid")
	}
	if g.Col != mp.Col || g.Row != mp.Row || len(g.Grid) != g.Col*g.Row {
		t.Fatalf("grid %dx%d len %d", g.Col, g.Row, len(g.Grid))
	}
	x0, y0 := g.OX, g.OY
	solve := func(nx, ny float64) (int, int) { // the exact math giKuCellAt runs
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
			if ok := kuCellPassable(mp, pos); ok != (gid == 1 || gid == 2 || gid == 4) {
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
	if passable < 1000 {
		t.Fatalf("only %d passable cells", passable)
	}
	if !KunlunCellPassable(32*61 + 32) {
		t.Fatal("centre city cell must be marchable")
	}
	if KunlunCellPassable(-1) || KunlunCellPassable(61*61) {
		t.Fatal("out-of-range pos must not be marchable")
	}
	for _, c := range mp.Cities {
		pos := int64(c.TY)*int64(mp.Col) + int64(c.TX)
		name, ok := KunlunCityNameAt(pos)
		if !ok || name == "" {
			t.Fatalf("city %d at %d has no web name (%q,%v)", c.ID, pos, name, ok)
		}
	}
	if _, ok := KunlunCityNameAt(0); ok {
		t.Fatal("(0,0) is not a city")
	}
	// Static cities carry their board cell so the clicked pop finds them.
	posOf := map[uint64]uint64{}
	for _, c := range st.Cities {
		posOf[c.P] = c.ID
	}
	if posOf[uint64(32*61+32)] != 1001 {
		t.Fatalf("cell 32,32 static city = %d", posOf[uint64(32*61+32)])
	}
}

func TestKuUnionSlotsOrder(t *testing.T) {
	blob := kuMiniFixture(t)
	rsp, err := DecodeKunlunMini(blob)
	if err != nil {
		t.Fatal(err)
	}
	mp, _ := kunlunLayout()
	slots := kuUnionSlots(mp, rsp)
	// Client city order, NOT proto order: union 101 owns 1001 (first in
	// mapOut order) → slot 0; 202 owns 2001 → slot 1; cell-only 303 → slot 2.
	if slots[101] != 0 || slots[202] != 1 || slots[303] != 2 {
		t.Fatalf("slots %v — want {101:0 202:1 303:2}", slots)
	}
}

func TestKunlunStreamFrame(t *testing.T) {
	blob := kuMiniFixture(t)
	sm, err := Stream(StreamKunlun, blob)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Which != StreamKunlun {
		t.Fatalf("which %q", sm.Which)
	}
	if len(sm.Cities) != 37 {
		t.Fatalf("cities %d", len(sm.Cities))
	}
	byID := map[uint64]StreamCity{}
	for _, c := range sm.Cities {
		byID[c.ID] = c
	}
	c1 := byID[1001]
	if c1.Owner != "Aethel" || c1.CI == nil || *c1.CI != 0 || !c1.Fight {
		t.Fatalf("1001 %+v", c1)
	}
	if c1.Color != kuColorHex(kuColorVec[0]) {
		t.Fatalf("1001 color %s want %s", c1.Color, kuColorHex(kuColorVec[0]))
	}
	c2 := byID[2001]
	if c2.Owner != "Bravo" || c2.CI == nil || *c2.CI != 1 {
		t.Fatalf("2001 %+v", c2)
	}
	// mapOut's own order: 1001 must be the FIRST city row the page draws.
	if sm.Cities[0].ID != 1001 {
		t.Fatalf("frame order starts %d", sm.Cities[0].ID)
	}
}

func TestRenderKunlun(t *testing.T) {
	img, err := RenderKunlun(kuMiniFixture(t), 1400)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(img, []byte("\x89PNG")) {
		t.Fatalf("not a PNG (%d bytes)", len(img))
	}
}

// The events card layers markers over the immutable ku background
// (/events/mapart?b=ku&p=bg.jpg since the 2026-10-06 SERVER-RAM policy
// removed the live.png server render) — they align only because kuStatic,
// streamKunlun and RenderKunlun share ONE box (kuWorldBox). Lock that
// invariant.
func TestKuBoxAlignment(t *testing.T) {
	st, err := StreamStatic(StreamKunlun)
	if err != nil {
		t.Fatal(err)
	}
	sm, err := Stream(StreamKunlun, kuMiniFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if sm.Art != "ku" || sm.Bg != "bg.jpg" {
		t.Fatalf("frame art/bg = %q/%q — page background would be missing", sm.Art, sm.Bg)
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
	if pt, ok := KunlunCityPoint(1001); !ok || pt != 32*61+32 {
		t.Fatalf("KunlunCityPoint(1001) = %d,%v want %d", pt, ok, 32*61+32)
	}
	if _, ok := KunlunCityPoint(999999); ok {
		t.Fatal("unknown city must not resolve")
	}
	// The background PNG the page fetches renders from an EMPTY payload too.
	img, err := RenderKunlun(nil, 1400)
	if err != nil {
		t.Fatal(err)
	}
	if len(img) < 8 || string(img[1:4]) != "PNG" {
		t.Fatal("base render is not a PNG")
	}
	// The WEB board PNG must carry the exact world-box aspect — the browser
	// stretches it over the same 0..1 space the markers use, so any legend
	// strip or margin would slide every city off its painted plate.
	board, err := RenderKunlunBoard(kuMiniFixture(t), 1200)
	if err != nil {
		t.Fatal(err)
	}
	hdr, err := png.DecodeConfig(bytes.NewReader(board))
	if err != nil {
		t.Fatal(err)
	}
	mp, err := kunlunLayout()
	if err != nil {
		t.Fatal(err)
	}
	_, _, bw, bh := kuWorldBox(kuPackMain, mp)
	want := float64(1200) * bh / bw
	if math.Abs(float64(hdr.Height)-want) > 2 {
		t.Fatalf("board PNG height %d, world-box aspect wants %f — markers would misalign", hdr.Height, want)
	}
}
