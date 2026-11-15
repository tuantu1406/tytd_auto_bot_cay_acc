package gimap

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Stream layouts: normalized coordinates, game-art icon paths, owner colors,
// and road links for the browser renderer.

func TestStreamGodIsland(t *testing.T) {
	payload, err := pb.Marshal(&proto.GodIslandGameMiniMapInfoRespMsg{
		Ret: 0,
		CityInfo: []*proto.GodIslandGameMiniCityInfo{
			{
				CityId: pb.Int32(1001),
				BaseData: &proto.GodIslandUnionOwnerInfo{
					UnionId:   pb.Int64(12345),
					UnionName: pb.String("Rồng Lửa"),
				},
				HaveFight: pb.Bool(true),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMapConfig(); err != nil {
		t.Skipf("CDN unreachable: %v", err)
	}
	sm, err := Stream(StreamBonglai, payload)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Art != ArtGodIsland || sm.Bg != "images/ImmortalIslandBg.jpg" {
		t.Fatalf("art/bg wrong: %q %q", sm.Art, sm.Bg)
	}
	if sm.Aspect <= 0 {
		t.Fatalf("aspect wrong: %v", sm.Aspect)
	}
	if len(sm.Cities) == 0 {
		t.Fatal("no cities")
	}
	var owned *StreamCity
	for i, c := range sm.Cities {
		if c.X < 0 || c.X > 1 || c.Y < 0 || c.Y > 1 {
			t.Fatalf("city %d outside unit square: %v %v", c.ID, c.X, c.Y)
		}
		if c.W <= 0 || c.H <= 0 {
			t.Fatalf("city %d lost client sprite size: w=%v h=%v", c.ID, c.W, c.H)
		}
		if c.X-c.W/2 < -1e-9 || c.X+c.W/2 > 1+1e-9 || c.Y-c.H/2 < -1e-9 || c.Y+c.H/2 > 1+1e-9 {
			t.Fatalf("city %d sprite does not fit the box: %+v", c.ID, c)
		}
		if !validCityIcon(c.Icon, "images/city/") {
			t.Fatalf("bad gi icon for %d: %q", c.ID, c.Icon)
		}
		if i > 0 && sm.Cities[i-1].ID >= c.ID {
			t.Fatalf("cities not sorted at %d", i)
		}
		if c.Owner == "Rồng Lửa" {
			owned = &sm.Cities[i]
		}
	}
	if owned == nil {
		t.Fatal("owned city missing from stream")
	}
	if !owned.Fight || !strings.HasPrefix(owned.Color, "#") || len(owned.Color) != 7 {
		t.Fatalf("owner state wrong: %+v", *owned)
	}

	peak, err := Stream(StreamThienma, payload)
	if err != nil {
		t.Fatal(err)
	}
	if peak.Bg != "images/ImmortalIslandBg_peak.jpg" {
		t.Fatalf("peak bg wrong: %q", peak.Bg)
	}
	// Measured world boxes: the 98-city peak rectangle is a different shape
	// than the 50-city normal one, so a hardcoded aspect could not produce
	// both.
	if math.Abs(peak.Aspect-sm.Aspect) < 0.02 {
		t.Fatalf("peak and normal share one aspect (%v vs %v): boxes not measured", peak.Aspect, sm.Aspect)
	}
	for _, c := range peak.Cities {
		if !validCityIcon(c.Icon, "images/city/peak/") {
			t.Fatalf("bad peak icon for %d: %q", c.ID, c.Icon)
		}
		if c.W <= 0 || c.H <= 0 {
			t.Fatalf("peak city %d lost client sprite size: w=%v h=%v", c.ID, c.W, c.H)
		}
		if c.X-c.W/2 < -1e-9 || c.X+c.W/2 > 1+1e-9 || c.Y-c.H/2 < -1e-9 || c.Y+c.H/2 > 1+1e-9 {
			t.Fatalf("peak city %d sprite does not fit the box: %+v", c.ID, c)
		}
	}
}

func TestStreamHolyLand(t *testing.T) {
	payload, err := pb.Marshal(&proto.HolyLandGameMiniMapInfoRespMsg{
		CityInfo: []*proto.HolyLandGameMiniCityInfo{
			{
				CityId:    pb.Int32(1001),
				BaseData:  &proto.HolyLandMiniUnionInfo{UnionId: pb.Int64(99), Name: pb.String("Phượng Hoàng")},
				HaveFight: pb.Bool(true),
			},
			{CityId: pb.Int32(4001)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sm, err := Stream(StreamCuuu, payload)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Art != ArtHolyLand || sm.Bg != "images/bgItem/sdzf_bg1.jpg" {
		t.Fatalf("art/bg wrong: %q %q", sm.Art, sm.Bg)
	}
	byID := map[uint64]StreamCity{}
	for _, c := range sm.Cities {
		byID[c.ID] = c
		if c.X < 0 || c.X > 1 || c.Y < 0 || c.Y > 1 {
			t.Fatalf("hl city %d outside unit square: %v %v", c.ID, c.X, c.Y)
		}
		if c.W <= 0 || c.H <= 0 {
			t.Fatalf("hl city %d lost client sprite size: w=%v h=%v", c.ID, c.W, c.H)
		}
		if c.X-c.W/2 < -1e-9 || c.X+c.W/2 > 1+1e-9 || c.Y-c.H/2 < -1e-9 || c.Y+c.H/2 > 1+1e-9 {
			t.Fatalf("hl city %d sprite does not fit the box: %+v", c.ID, c)
		}
		tier := c.ID / 1000
		if want := fmt.Sprintf("images/city/%d.png", tier); c.Icon != want {
			t.Fatalf("hl icon wrong for %d: %q want %q", c.ID, c.Icon, want)
		}
	}
	if len(sm.Cities) != 34 {
		t.Fatalf("hl static city count: %d", len(sm.Cities))
	}
	if len(sm.Links) != 50 {
		t.Fatalf("hl road count: %d", len(sm.Links))
	}
	for _, l := range sm.Links {
		if _, ok := byID[l[0]]; !ok {
			t.Fatalf("link references unknown city %d", l[0])
		}
		if _, ok := byID[l[1]]; !ok {
			t.Fatalf("link references unknown city %d", l[1])
		}
	}
	if c := byID[1001]; c.Owner != "Phượng Hoàng" || !c.Fight || c.Color == "" {
		t.Fatalf("hl owner state lost: %+v", c)
	}
	if c := byID[4001]; c.Owner != "" || c.Color != "" || c.Fight {
		t.Fatalf("unowned hl city not clean: %+v", c)
	}
}

func TestStreamBallGVG(t *testing.T) {
	payload := ballGVGTestResponse(0,
		ballGVGTestPlace(7, 2, ballGVGTestMember(2, 4), ballGVGTestMember(1, 1)),
		ballGVGTestPlace(2, 0),
	)
	sm, err := Stream(StreamDitich, payload)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Art != "" || sm.Bg != "" {
		t.Fatalf("ditich must be schematic, got art %q bg %q", sm.Art, sm.Bg)
	}
	if len(sm.Cities) != 2 || sm.Cities[0].ID != 2 || sm.Cities[1].ID != 7 {
		t.Fatalf("ditich cities wrong: %+v", sm.Cities)
	}
	empty, held := sm.Cities[0], sm.Cities[1]
	if empty.Owner != "" || empty.Color != "" || empty.Info != "trống" {
		t.Fatalf("unheld slot wrong: %+v", empty)
	}
	if held.Owner != "Phe 2" || held.Color != campHex(2) {
		t.Fatalf("held slot wrong: %+v", held)
	}
	if !strings.Contains(held.Info, "P1:1") || !strings.Contains(held.Info, "P2:4") {
		t.Fatalf("member counts lost: %q", held.Info)
	}
	for _, c := range sm.Cities {
		if c.X < 0 || c.X > 1 || c.Y < 0 || c.Y > 1 {
			t.Fatalf("slot outside unit square: %v %v", c.X, c.Y)
		}
		if c.W != 0 || c.H != 0 {
			t.Fatalf("ditich has no per-slot sprite size, got w=%v h=%v", c.W, c.H)
		}
	}
}

func TestStreamUnknownWhich(t *testing.T) {
	if _, err := Stream("skytrade", nil); err == nil {
		t.Fatal("unknown which must error")
	}
}

func validCityIcon(icon, dir string) bool {
	if !strings.HasPrefix(icon, dir) || !strings.HasSuffix(icon, ".png") {
		return false
	}
	n := strings.TrimSuffix(strings.TrimPrefix(icon, dir), ".png")
	return strings.HasPrefix(n, "1") || strings.HasPrefix(n, "2") ||
		strings.HasPrefix(n, "3") || strings.HasPrefix(n, "4") || n == "5"
}

// The client's getUnionColor replication: slot = index of the union's first
// city in the client's own city order (later occurrences reuse it).
func TestUnionColorSlots(t *testing.T) {
	cities := []city{{ID: 1001}, {ID: 1002}, {ID: 1003}, {ID: 1004}}
	overlay := map[string]cityState{
		"1002": {Owner: "B", UnionID: 70},
		"1001": {Owner: "A", UnionID: 42},
		"1003": {Owner: "A", UnionID: 42}, // same union, later city → keeps slot 0
	}
	slots := unionColorSlots(cities, overlay)
	if slots[42] != 0 || slots[70] != 1 {
		t.Fatalf("slots=%v, want union42→0, union70→1", slots)
	}
	if _, ok := slots[99]; ok {
		t.Fatal("unowned union got a slot")
	}
}

// Holy Land static scene is fully offline (embedded layout): world box,
// labeled cities inside the unit square, and the road links.
func TestStreamStaticHolyLand(t *testing.T) {
	st, err := StreamStatic(StreamCuuu)
	if err != nil {
		t.Fatalf("StreamStatic(cuuu): %v", err)
	}
	if st.Which != StreamCuuu || st.Art != ArtHolyLand || st.Bg == "" {
		t.Fatalf("static identity: %+v", st)
	}
	if !(st.Aspect > 0 && st.WorldW > 0 && st.WorldH > 0) {
		t.Fatalf("bad world: %+v", st)
	}
	if len(st.Cities) == 0 || len(st.Links) == 0 {
		t.Fatalf("empty static cities/links: %d/%d", len(st.Cities), len(st.Links))
	}
	named := 0
	for _, c := range st.Cities {
		if c.X < 0 || c.X > 1 || c.Y < 0 || c.Y > 1 {
			t.Fatalf("city %d outside unit box: %v", c.ID, c)
		}
		if c.Name != "" {
			named++
		}
	}
	if named == 0 {
		t.Fatal("no Holy Land city carries a client label")
	}
}

// God Island static scene (CDN-gated like the frame tests): every terrain
// tile inside the box, territory attribution present, names resolved.
func TestStreamStaticGodIsland(t *testing.T) {
	if _, err := LoadMapConfig(); err != nil {
		t.Skipf("CDN unreachable: %v", err)
	}
	st, err := StreamStatic(StreamBonglai)
	if err != nil {
		t.Fatalf("StreamStatic(bonglai): %v", err)
	}
	if len(st.Tiles) == 0 || st.TileW <= 0 || st.TileH <= 0 {
		t.Fatalf("missing terrain: %d tiles", len(st.Tiles))
	}
	claimed, named := 0, 0
	for _, tl := range st.Tiles {
		if tl.X < 0 || tl.X > 1 || tl.Y < 0 || tl.Y > 1 {
			t.Fatalf("tile outside unit box: %+v", tl)
		}
		if tl.C != 0 {
			claimed++
		}
	}
	for _, c := range st.Cities {
		if c.Name != "" {
			named++
		}
	}
	if claimed == 0 {
		t.Fatal("no tile belongs to a city — territory art impossible")
	}
	if named == 0 {
		t.Fatal("no God Island city carries a client label")
	}
}
