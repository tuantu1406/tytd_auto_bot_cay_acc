package gimap

import (
	"strconv"
	"strings"
	"testing"

	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func TestSkyTradeStatic(t *testing.T) {
	st, err := SkyTradeStatic()
	if err != nil {
		t.Fatal(err)
	}
	if st.Which != StreamThuongdo || len(st.Cities) != 29 || len(st.Links) != 29 {
		t.Fatalf("static: which=%s cities=%d links=%d", st.Which, len(st.Cities), len(st.Links))
	}
	if st.Art != ArtSkyTrade || st.Bg != skyTradeBg {
		t.Fatalf("static art/bg: %q/%q, want the game backdrop bundle", st.Art, st.Bg)
	}
	byID := map[uint64]StaticCity{}
	for _, c := range st.Cities {
		byID[c.ID] = c
		if c.X < 0 || c.X > 1 || c.Y < 0 || c.Y > 1 {
			t.Fatalf("city %d outside normalized box: %f,%f", c.ID, c.X, c.Y)
		}
	}
	for n := 1; n <= 15; n++ {
		c, ok := byID[uint64(SkyWirePortBase+n)]
		if !ok {
			t.Fatalf("port %d missing", n)
		}
		// The game names its ports and paints their palaces; Lab keeps the
		// number as a badge.
		if c.Lab != strconv.Itoa(n) || c.Name != skyPortRealName[n] {
			t.Fatalf("port %d labels: %q / %q", n, c.Lab, c.Name)
		}
		if c.Icon != skyPortIcon(n) || c.W != skyPortW || c.H != skyPortH {
			t.Fatalf("port %d art: %q %f/%f", n, c.Icon, c.W, c.H)
		}
	}
	for n := 20; n <= 33; n++ {
		if _, ok := byID[uint64(skyWaypointBase+n)]; !ok {
			t.Fatalf("waypoint %d missing", n)
		}
	}
	for _, l := range st.Links {
		if _, ok := byID[l[0]]; !ok {
			t.Fatalf("link %v references unknown city", l)
		}
		if _, ok := byID[l[1]]; !ok {
			t.Fatalf("link %v references unknown city", l)
		}
	}
}

func skyPayload(t *testing.T, r *proto.SkyTradeEnterResp) []byte {
	t.Helper()
	b, err := pb.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSkyTradeStream(t *testing.T) {
	sm0 := &proto.SkyTradeEnterResp{}
	sm0.Ret = pb.Int32(0)
	// Own ship sailing port 5 → port 7 with a level-3 hull.
	sm0.PlayerData = &proto.SkyTradePlayerData{
		AirshipInfo: &proto.SkyTradeAirshipInfo{
			SourcePort: pb.Int32(SkyWirePortBase + 5),
			TargetPort: pb.Int32(SkyWirePortBase + 7),
			Level:      pb.Int32(3),
			StartTime:  pb.Int64(1000),
		},
	}
	// One union ship en route port 1 → port 2.
	sm0.UnionPlayerData = []*proto.SkyTradeUnionPlayerData{{
		Player: &proto.SkyTradePlayerBase{
			NickName:  pb.String("Đào Vũ"),
			UnionData: &proto.SkyTradeUnionBase{UnionId: pb.Int64(9), Name: pb.String("Thiên Môn")},
		},
		AirshipInfo: &proto.SkyTradeAirshipInfo{
			SourcePort: pb.Int32(SkyWirePortBase + 1),
			TargetPort: pb.Int32(SkyWirePortBase + 2),
		},
	}}
	sm0.PortGoods = []*proto.SkyTradePortGoodsInfo{{
		PortId:      pb.Int32(SkyWirePortBase + 12),
		RareGoodsId: pb.Int32(1009),
	}, {
		// An id outside the goods table falls back to "#id".
		PortId:      pb.Int32(SkyWirePortBase + 3),
		RareGoodsId: pb.Int32(77),
	}}
	sm, err := SkyTradeStream(skyPayload(t, sm0))
	if err != nil {
		t.Fatal(err)
	}
	if len(sm.Links) != 29 || sm.Which != StreamThuongdo {
		t.Fatalf("frame skeleton: links=%d which=%s", len(sm.Links), sm.Which)
	}
	if sm.Art != ArtSkyTrade || sm.Bg != skyTradeBg {
		t.Fatalf("stream art/bg: %q/%q", sm.Art, sm.Bg)
	}
	byID := map[uint64]StreamCity{}
	for _, c := range sm.Cities {
		byID[c.ID] = c
	}
	// 15 ports + 14 waypoints + 2 moving ships.
	if len(sm.Cities) != 31 {
		t.Fatalf("cities=%d, want 31", len(sm.Cities))
	}
	port12 := byID[uint64(SkyWirePortBase+12)]
	if !strings.Contains(port12.Info, "hàng hiếm: Tiên Tàm Tơ Trù") {
		t.Fatalf("rare goods name missing: %q", port12.Info)
	}
	port3 := byID[uint64(SkyWirePortBase+3)]
	if !strings.Contains(port3.Info, "hàng hiếm #77") {
		t.Fatalf("unknown goods id must fall back to #id: %q", port3.Info)
	}
	// Own ship is first in the moving list: highlighted, hull-named, and
	// wearing its level-3 sprite. Its chip reads "Tôi" like the game's.
	own := byID[skyShipBase]
	if own.Color != "#f2d98c" || own.Icon != "images/boat/3.png" || own.Name != "Tôi" {
		t.Fatalf("own ship marker: %+v", own)
	}
	if own.W != skyShipW || own.H != skyShipH {
		t.Fatalf("own ship size: %f/%f", own.W, own.H)
	}
	if !strings.Contains(own.Info, "Lương Nghi Huyền Chu") ||
		!strings.Contains(own.Info, "Thanh Liên Đài → Thiên Cơ Các") {
		t.Fatalf("own ship info: %q", own.Info)
	}
	// Union ship en route port 1 → port 2.
	ship := byID[skyShipBase+1]
	if ship.Lab != "🚢" {
		t.Fatalf("moving ship marker: %+v", ship)
	}
	// A ship without a reported level still wears the client's base hull.
	if ship.Icon != "images/boat/1.png" {
		t.Fatalf("ship icon: %q, want the level-1 boat sprite", ship.Icon)
	}
	if !strings.Contains(ship.Info, "Thiên Môn") || !strings.Contains(ship.Info, "Bắc Hàn Giới → Thạch Lâm") {
		t.Fatalf("ship info: %q", ship.Info)
	}
	if ship.Name != "Đào Vũ" {
		t.Fatalf("union ship chip: %q", ship.Name)
	}
	// Ports wear the client's palace art on the live frames too.
	if port12.Icon != skyPortIcon(12) {
		t.Fatalf("port icon: %q", port12.Icon)
	}
	// Midpoint of the client's port 1 and port 2 nodes.
	m, err := loadSkyMap()
	if err != nil {
		t.Fatal(err)
	}
	var n1, n2 skyNode
	for _, nd := range m.Nodes {
		if nd.N == 1 {
			n1 = nd
		}
		if nd.N == 2 {
			n2 = nd
		}
	}
	if want := (n1.X + n2.X) / 2 / m.Width; !(ship.X > want-1e-9 && ship.X < want+1e-9) {
		t.Fatalf("ship x %f, want midpoint %f", ship.X, want)
	}
	// targetPort 0 is the client's docking rule: the ship sits on its port,
	// starred for the player and highlighted gold.
	smDock := &proto.SkyTradeEnterResp{Ret: pb.Int32(0), PlayerData: &proto.SkyTradePlayerData{
		AirshipInfo: &proto.SkyTradeAirshipInfo{SourcePort: pb.Int32(SkyWirePortBase + 5)},
	}}
	smDocked, err := SkyTradeStream(skyPayload(t, smDock))
	if err != nil {
		t.Fatal(err)
	}
	port5 := (func() StreamCity {
		for _, c := range smDocked.Cities {
			if c.ID == uint64(SkyWirePortBase+5) {
				return c
			}
		}
		t.Fatal("port 5 missing from docked frame")
		return StreamCity{}
	})()
	if port5.Color != "#f2d98c" || !strings.Contains(port5.Owner, "bạn") {
		t.Fatalf("own dock not highlighted: %+v", port5)
	}
	if !strings.Contains(port5.Info, "★ Tôi") {
		t.Fatalf("own dock missing from info: %q", port5.Info)
	}
	// Waypoints carry no label: the page distinguishes them from Ball GVG
	// by which, so the server leaves lab empty (json omitempty) here.
	if wp := byID[uint64(skyWaypointBase+20)]; wp.Lab != "" || wp.Name != "" {
		t.Fatalf("waypoint must stay a bare dot: %+v", wp)
	}
}

// A snapshot pulled from another member's session must never call that
// account's boat "Tôi" or ring it gold: the page viewer is not the puller.
func TestSkyTradeStreamAsMember(t *testing.T) {
	r := &proto.SkyTradeEnterResp{Ret: pb.Int32(0), PlayerData: &proto.SkyTradePlayerData{
		AirshipInfo: &proto.SkyTradeAirshipInfo{
			SourcePort: pb.Int32(SkyWirePortBase + 1),
			TargetPort: pb.Int32(SkyWirePortBase + 2),
		},
	}}
	sm, err := SkyTradeStreamAs(skyPayload(t, r), "Đào Vũ", false)
	if err != nil {
		t.Fatal(err)
	}
	var boat StreamCity
	for _, c := range sm.Cities {
		if c.ID == skyShipBase {
			boat = c
		}
	}
	if boat.Name != "Đào Vũ" || boat.Color != "" {
		t.Fatalf("member-pulled boat: %+v", boat)
	}
}

// Hull art is clamped into the five boat sprites the bundle ships.
func TestSkyBoatIcon(t *testing.T) {
	for level, want := range map[int32]string{
		0: "images/boat/1.png", 1: "images/boat/1.png",
		3: "images/boat/3.png", 5: "images/boat/5.png",
		9: "images/boat/5.png", -2: "images/boat/1.png",
	} {
		if got := skyBoatIcon(level); got != want {
			t.Fatalf("skyBoatIcon(%d)=%q want %q", level, got, want)
		}
	}
}

func TestSkyTradeStreamErrors(t *testing.T) {
	bad := &proto.SkyTradeEnterResp{Ret: pb.Int32(9)}
	if _, err := SkyTradeStream(skyPayload(t, bad)); err == nil {
		t.Fatal("ret!=0 must be an error")
	}
	if _, err := SkyTradeStream([]byte{0xff, 0xff}); err == nil {
		t.Fatal("corrupt payload must be an error")
	}
	if _, err := Stream(StreamThuongdo, skyPayload(t, &proto.SkyTradeEnterResp{Ret: pb.Int32(0)})); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := StreamStatic(StreamThuongdo); err != nil {
		t.Fatalf("static dispatch: %v", err)
	}
}
