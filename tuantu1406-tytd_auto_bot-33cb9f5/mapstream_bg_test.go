package tytd_go

// P2 pins for the /ditich mini app: bgRosterFrame is the ONLY place raw
// Ball GVG arena pixels become the normalized dots the page paints, so the
// ring transform, the −1 degradation sentinels, the dead flag and the
// deterministic order are pinned here (canvas pixels themselves are
// phone-only, as always).

import (
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func bgFrameTestPlayer() *EventPlayer {
	p := &EventPlayer{}
	st := p.bgState()
	st.PlaceID = 4003
	st.Rooms = map[int32]*proto.BallGVGPlaceConfigTemp{
		4003: {Id: pb.Int32(4003), Name: pb.String("Hang Sâu Cổ Đại"), Radius: pb.Int32(300)},
	}
	return p
}

func TestBGRosterFrameRing(t *testing.T) {
	p := bgFrameTestPlayer()
	st := p.bgState()
	now := time.Now()
	st.Roster = map[int64]*bgRosterUser{
		1: {Name: "giữa", CampID: 1, HP: "100", X: "1125.00000", Y: "900.00000"},
		2: {Name: "vành", CampID: 1, HP: "100", X: "1290.00000", Y: "900.00000"}, // centre + r·0.55 → nx=1 → clamp .95
		3: {Name: "chết", CampID: 2, HP: "100", ReviveTime: now.Add(20 * time.Second).UnixMilli(), X: "1125.00000", Y: "960.00000"},
		4: {Name: "mờ", CampID: 3, HP: "100", X: "", Y: ""},
	}
	f := p.bgRosterFrame(now)
	if f == nil || f.Place != 4003 || f.R != 300 {
		t.Fatalf("frame = %+v", f)
	}
	if f.Name != "4003 — Hang Sâu Cổ Đại" { // bgRoomName ships "id — name"
		t.Fatalf("room name = %q", f.Name)
	}
	byName := map[string]int{}
	for i, b := range f.Balls {
		byName[b.N] = i
	}
	mid := f.Balls[byName["giữa"]]
	if mid.X != 0.5 || mid.Y != 0.5 || mid.D {
		t.Fatalf("centre ball = %+v", mid)
	}
	ring := f.Balls[byName["vành"]]
	if ring.X < 0.95 || ring.X > 0.95 { // exactly the clamp ceiling
		t.Fatalf("ring ball x = %v", ring.X)
	}
	dead := f.Balls[byName["chết"]]
	if !dead.D || dead.Rv == 0 {
		t.Fatalf("dead ball = %+v", dead)
	}
	unk := f.Balls[byName["mờ"]]
	if unk.X != -1 || unk.Y != -1 {
		t.Fatalf("unparseable ball must keep sentinels: %+v", unk)
	}
	for _, b := range f.Balls {
		if b.X >= 0 && (b.X < 0.05 || b.X > 0.95) {
			t.Fatalf("%s x out of range: %v", b.N, b.X)
		}
		if b.Y >= 0 && (b.Y < 0.05 || b.Y > 0.95) {
			t.Fatalf("%s y out of range: %v", b.N, b.Y)
		}
	}
	// Deterministic: camp asc, then name — and stable across calls.
	for i := 1; i < len(f.Balls); i++ {
		a, b := f.Balls[i-1], f.Balls[i]
		if a.C > b.C || (a.C == b.C && a.N > b.N) {
			t.Fatalf("balls not camp/name sorted: %+v", f.Balls)
		}
	}
	f2 := p.bgRosterFrame(now)
	if len(f2.Balls) != len(f.Balls) {
		t.Fatal("roster frame unstable")
	}
	for i := range f.Balls {
		if f2.Balls[i].N != f.Balls[i].N {
			t.Fatalf("sort not deterministic at %d", i)
		}
	}
}

func TestBGRosterFrameDegradation(t *testing.T) {
	// Lobby-side (no room) or empty roster → no field at all.
	p := &EventPlayer{}
	if f := p.bgRosterFrame(time.Now()); f != nil {
		t.Fatalf("lobby frame = %+v want nil", f)
	}
	p = bgFrameTestPlayer()
	p.bgState().PlaceID = 0
	if f := p.bgRosterFrame(time.Now()); f != nil {
		t.Fatalf("place 0 frame = %+v want nil", f)
	}
	// Cold config (radius unknown): R omitted, EVERY ball sentinel →
	// page list-mode. This is the honest no-dots path.
	p = bgFrameTestPlayer()
	st := p.bgState()
	st.Rooms = nil
	st.Roster = map[int64]*bgRosterUser{
		1: {Name: "a", CampID: 1, HP: "10", X: "1125.00000", Y: "900.00000"},
	}
	f := p.bgRosterFrame(time.Now())
	if f == nil || f.R != 0 || len(f.Balls) != 1 || f.Balls[0].X != -1 {
		t.Fatalf("cold-config frame = %+v", f)
	}
}
