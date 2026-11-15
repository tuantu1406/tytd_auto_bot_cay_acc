package gimap

// Tests for the client-faithful Holy Land compositor: manifest-backed sprite
// pack, the unionIdVec color order, layout anchors on the exact world box,
// and a full composite render with ownership applied.

import (
	"bytes"
	"image/png"
	"math"
	"os"
	"testing"

	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// hlSamplePayload builds a minimap response: the 12 main cities split between
// unions 77 and 5 (and unowned), mines owned by a late union that never
// appears among the mains (fallback color path).
func hlSamplePayload(t *testing.T) []byte {
	t.Helper()
	var resp proto.HolyLandGameMiniMapInfoRespMsg
	for id := uint64(1001); id <= 1012; id++ {
		c := &proto.HolyLandGameMiniCityInfo{CityId: pb.Int32(int32(id))}
		switch id % 3 {
		case 0:
			c.BaseData = &proto.HolyLandMiniUnionInfo{UnionId: pb.Int64(77), Name: pb.String("Thiên Long")}
		case 1:
			c.BaseData = &proto.HolyLandMiniUnionInfo{UnionId: pb.Int64(5), Name: pb.String("Bái Hổ")}
		}
		if id == 1004 {
			c.HaveFight = pb.Bool(true)
		}
		resp.CityInfo = append(resp.CityInfo, c)
	}
	for id := uint64(2001); id <= 2015; id++ {
		c := &proto.HolyLandGameMiniCityInfo{CityId: pb.Int32(int32(id))}
		if id%2 == 0 {
			c.BaseData = &proto.HolyLandMiniUnionInfo{UnionId: pb.Int64(4242), Name: pb.String("Tử Vi")}
		}
		resp.CityInfo = append(resp.CityInfo, c)
	}
	for _, id := range []uint64{3001, 3002, 3003, 3004, 3005, 3006, 4001} {
		resp.CityInfo = append(resp.CityInfo, &proto.HolyLandGameMiniCityInfo{CityId: pb.Int32(int32(id)),
			BaseData: &proto.HolyLandMiniUnionInfo{UnionId: pb.Int64(77), Name: pb.String("Thiên Long")}})
	}
	resp.Ret = pb.Int32(0)
	out, err := pb.Marshal(&resp)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHolyLandUnionColorOrder(t *testing.T) {
	overlay := map[string]cityState{
		"1001": {UnionID: 7}, "1002": {}, "1005": {UnionID: 7}, "1003": {UnionID: 9},
	}
	seq := hlUnionOrder(overlay)
	want := []uint64{7, 0, 9} // ascending id first-occurrence, 0 pushed by 1002
	if len(seq) != len(want) {
		t.Fatalf("seq=%v want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("seq=%v want %v", seq, want)
		}
	}
	if c := hlUnionColor(seq, 9); c != hlColorVec[2] {
		t.Errorf("union 9 color = %v, want vec[2] %v", c, hlColorVec[2])
	}
	fb := hlUnionColor(seq, 4242) // never among mains → pale fallback, not a vec entry
	if fb == (rgba{}) {
		t.Error("fallback color is zero")
	}
}

func TestHolyLandLayoutAnchors(t *testing.T) {
	payload := hlSamplePayload(t)
	lay, err := LayoutHolyLand(payload, 1400)
	if err != nil {
		t.Fatal(err)
	}
	if lay.Width != 1400 || lay.Height != int(math.Round(1400*hlWorldH/hlWorldW)) {
		t.Fatalf("canvas %dx%d", lay.Width, lay.Height)
	}
	if len(lay.Cities) != 34 {
		t.Fatalf("cities %d", len(lay.Cities))
	}
	// World box: min/max city centers (padded) must sit inside the canvas.
	s := hlScale(1400)
	for _, c := range lay.Cities {
		if c.X < 0 || c.X >= lay.Width || c.Y < 0 || c.Y >= lay.Height {
			t.Fatalf("city %d at %d,%d outside %dx%d", c.ID, c.X, c.Y, lay.Width, lay.Height)
		}
	}
	// 1001 sits at mapOut (1958.52+100)×(516.833+200) scaled.
	var c1001 *CityPoint
	for i := range lay.Cities {
		if lay.Cities[i].ID == 1001 {
			c1001 = &lay.Cities[i]
		}
	}
	if c1001 == nil {
		t.Fatal("1001 missing")
	}
	if dx := math.Abs(float64(c1001.X) - 2058.52*s); dx > 1 {
		t.Errorf("1001 x=%d want %.0f", c1001.X, 2058.52*s)
	}
	if c1001.Owner != "" || c1001.Fight { // 1001%3==2 → unowned sample city
		t.Errorf("1001 owner/fight = %q/%v", c1001.Owner, c1001.Fight)
	}
	for _, c := range lay.Cities {
		if c.ID == 1002 && c.Owner != "Thiên Long" { // 1002%3==0
			t.Errorf("1002 owner = %q", c.Owner)
		}
	}
	for _, c := range lay.Cities {
		if c.ID == 1004 && !c.Fight {
			t.Error("1004 lost its fight flag")
		}
	}
}

func TestHolyLandRenderComposite(t *testing.T) {
	payload := hlSamplePayload(t)
	pngBytes, err := RenderHolyLand(payload, 1400)
	if err != nil {
		t.Fatal(err)
	}
	if len(pngBytes) < 10_000 {
		t.Fatalf("PNG suspiciously small: %d bytes", len(pngBytes))
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != 1400 {
		t.Fatalf("width %d", b.Dx())
	}
	// The render must actually paint art: sample the bg grid area and a city
	// anchor — neither may be the flat canvas fill.
	s := hlScale(1400)
	px := func(x, y float64) (uint32, uint32, uint32, uint32) { return img.At(int(x*s), int(y*s)).RGBA() }
	r, g, bl, _ := px(750, 900) // inside tile 1
	if r == 8 && g == 18 && bl == 35 {
		t.Error("background tile not painted")
	}
	// 4001 sits at mapOut x=2800,y=1400 → fortress region colored.
	r, g, bl, _ = px(2900, 1600)
	if r == 8 && g == 18 && bl == 35 {
		t.Error("city/fortress area not painted")
	}
	if os.Getenv("HL_DUMP") != "" {
		_ = os.WriteFile("/tmp/hl_go_render.png", pngBytes, 0o644)
	}
}

func TestHolyLandSpritePackLoads(t *testing.T) {
	pk, err := loadHLPack()
	if err != nil {
		t.Fatal(err)
	}
	if len(pk.man.Roads) < 50 || len(pk.man.Builds) < 4 || len(pk.man.Forts) < 4 || pk.man.Plate == nil {
		t.Fatalf("incomplete manifest: roads=%d builds=%d forts=%d plate=%v", len(pk.man.Roads), len(pk.man.Builds), len(pk.man.Forts), pk.man.Plate != nil)
	}
	if n := len(pk.man.BGTile.Files); n != 10 {
		t.Fatalf("bg tiles %d", n)
	}
	for _, gid := range []string{"8", "57"} {
		if m, ok := pk.man.Roads[gid]; !ok {
			t.Fatalf("road %s missing", gid)
		} else if _, err := pk.img(m.File); err != nil {
			t.Fatal(err)
		}
	}
	for k := range pk.man.Builds {
		if _, err := pk.tint(pk.man.Builds[k].File, hlColorVec[3], 127); err != nil {
			t.Fatalf("tint build %s: %v", k, err)
		}
	}
	if _, err := pk.img(pk.man.Forts["1"].File); err != nil {
		t.Fatal(err)
	}
}

func TestHolyLandStreamRoutes(t *testing.T) {
	minimap := hlSamplePayload(t)
	now := int64(1700000000000)
	routes := mustMarshalHL(t, &proto.HolyLandGameCityInfoRespMsg{
		Ret: pb.Int32(0),
		RouteInfo: []*proto.HolyLandGameRouteInfo{
			{FromCityId: pb.Int32(1001), ToCityId: pb.Int32(2002), Time: pb.Int64(now), Arrival: pb.Int64(now + 60000), UnionId: pb.Int64(77), Name: pb.String("Meo")},
			{FromCityId: pb.Int32(1001), ToCityId: pb.Int32(9999)}, // stale endpoint must drop
			{FromCityId: pb.Int32(1001), ToCityId: pb.Int32(1001)}, // self route must drop
		},
	})
	sm, err := HolyLandStream(minimap, routes)
	if err != nil {
		t.Fatal(err)
	}
	if len(sm.Routes) != 1 {
		t.Fatalf("routes %+v", sm.Routes)
	}
	r := sm.Routes[0]
	if r.From != 1001 || r.To != 2002 || r.Name != "Meo" || r.Start != now {
		t.Fatalf("route %+v", r)
	}
	// union 77 owns mains in this sample → first owned entry of [0,77,5] is
	// index 1 → colorVec[1].
	if r.Color != "#6885fd" {
		t.Fatalf("route color %s", r.Color)
	}
	// Without the snapshot blob the frame must stay exactly as before.
	sm2, err := HolyLandStream(minimap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sm2.Routes) != 0 {
		t.Fatal("nil blob must yield no routes")
	}
}

func mustMarshalHL(t *testing.T, m pb.Message) []byte {
	t.Helper()
	out, err := pb.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The browser dispatches SSE map frames by frame["which"] — a frame leaving
// the decoder without it is silently dropped by the page (2026-10-03 blank
// map regression). Stream() and HolyLandStream() must always stamp it.
func TestHolyLandStreamCarriesWhich(t *testing.T) {
	m := hlSamplePayload(t)
	f, err := HolyLandStream(m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Which != StreamCuuu {
		t.Fatalf("HolyLandStream Which=%q want %q", f.Which, StreamCuuu)
	}
	f2, err := Stream(StreamCuuu, m)
	if err != nil {
		t.Fatal(err)
	}
	if f2.Which != StreamCuuu {
		t.Fatalf("Stream Which=%q want %q", f2.Which, StreamCuuu)
	}
}
