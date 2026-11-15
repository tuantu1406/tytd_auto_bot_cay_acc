package tytd_go

// View-window state tests for the /kunlun mini app (the 4-fix round):
// kuViewAnswer must flatten the 219903 response into the shapes the page
// reads — standing rows with the OWN-side mark, per-tile union capture
// progress (the "cell HP" bar), and real city HP; and the march latency fix
// needs kuMergeRouteLedgers to fold EVERY seated socket's ledger with the
// newest departure winning per player.

import (
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func TestKuViewAnswerFlatten(t *testing.T) {
	rsp := &proto.KunlunWarGameBlockInfoResp{
		BlockInfo: []*proto.KunlunWarBlockInfo{
			{Pos: pb.Int32(1953), // (32,32) on the 61-wide board
				DisplayInfo: []*proto.KunlunWarPlayerDisplayInfo{
					{NickName: pb.String("Minh"), UnionId: pb.Int64(101), Power: pb.String("12,3")},
					{NickName: pb.String("Thù"), UnionId: pb.Int64(202), Power: pb.String("99")},
				},
				UnionBlockData: []*proto.KunlunWarUnionBlockData{
					{UnionId: pb.Int64(101), OccupyProgress: pb.Int32(70)},
					{UnionId: pb.Int64(202), OccupyProgress: pb.Int32(30)},
				}},
			{Pos: pb.Int32(1234), DisplayInfo: []*proto.KunlunWarPlayerDisplayInfo{
				{NickName: pb.String("Khuất"), UnionId: pb.Int64(303)},
			}},
			{Pos: pb.Int32(1235)}, // empty tile: no rows, no prog
		},
		CityInfo: []*proto.KunlunWarGameCityInfo{
			{CityId: pb.Int32(1001), Hp: pb.Int32(500000), CurHp: pb.Int32(420000),
				HaveFight: pb.Bool(true), UnionId: pb.Int64(101)},
		},
	}
	own := map[int64]bool{101: true}
	b := kuViewAnswer(rsp, own)

	if len(b.Cells["1953"]) != 2 || len(b.Cells["1234"]) != 1 {
		t.Fatalf("standing rows lost: %+v", b.Cells)
	}
	if _, ok := b.Cells["1235"]; ok {
		t.Fatalf("empty tile emitted rows: %v", b.Cells["1235"])
	}
	if !b.Cells["1953"][0].O || b.Cells["1953"][1].O {
		t.Fatalf("own-side mark wrong: %+v", b.Cells["1953"])
	}
	if b.Cells["1953"][0].N != "Minh" || b.Cells["1953"][0].P != "12,3" {
		t.Fatalf("display row lost fields: %+v", b.Cells["1953"][0])
	}
	pg := b.Prog["1953"]
	if len(pg) != 2 || pg[0].Prog != 70 || pg[1].Prog != 30 {
		t.Fatalf("occupy progress flatten wrong: %+v", b.Prog)
	}
	if _, ok := b.Prog["1234"]; ok {
		t.Fatalf("tile without unionBlockData must not appear in prog")
	}
	ci := b.Cities["1001"]
	if ci.HP != 500000 || ci.Cur != 420000 || !ci.Fight || ci.U != 101 {
		t.Fatalf("city hp rows lost: %+v", ci)
	}
	if b.N != 3 {
		t.Fatalf("blocks=%d want 3", b.N)
	}
}

// The latency fix: one socket only ever heard the 219943 broadcasts flying
// while IT sat in-scene, so per-tick single-puller rotation made foreign
// marches wait up to n×5s. The merge must union all ledgers (leader + every
// event account with the event open) and let the NEWEST departure win per
// player — a stale copy in another ledger must not resurrect an old line.
func TestKuMergeRouteLedgersNewestWins(t *testing.T) {
	now := time.Now()
	ms := now.UnixMilli()
	act, _ := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{{
		ActivityId: pb.Int32(555), Type: pb.Int32(activityTypeKunlunWar),
		BeginShowTime: pb.Int64(now.Add(-time.Hour).UnixMilli()),
		BeginTime:     pb.Int64(now.Add(-time.Hour).UnixMilli()),
		EndTime:       pb.Int64(now.Add(time.Hour).UnixMilli()),
		EndShowTime:   pb.Int64(now.Add(2 * time.Hour).UnixMilli()),
	}}})
	mkEp := func(name string, routes map[int64]kuRouteRec) *EventPlayer {
		ep := &EventPlayer{Username: name, ChatID: 42, UID: uint64(len(name)), Running: true}
		ep.ActList.applyList(name, act, true)
		ep.ku = &kuEventState{Routes: routes}
		return ep
	}
	epA := mkEp("aaa", map[int64]kuRouteRec{
		7: {From: 100, To: 200, Time: ms - 40000, Arrival: ms + 10000, UnionID: 101}, // stale copy of pid7
		8: {From: 101, To: 201, Time: ms - 5000, Arrival: ms + 25000, UnionID: 202},  // only in A
	})
	epB := mkEp("bbb", map[int64]kuRouteRec{
		7: {From: 105, To: 205, Time: ms - 10000, Arrival: ms + 40000, UnionID: 101}, // freshest pid7
	})
	// A CLOSED-event account must not contribute (it never hears marches):
	// no activity list at all.
	epC := mkEp("ccc", map[int64]kuRouteRec{9: {From: 1, To: 2, Time: ms, Arrival: ms + 1000}})
	epC.ActList = activityStore{}
	lp := &Player{Username: "leader", UID: 7, ChatID: 42, Running: true}
	lp.ku = newKunlunState()
	lp.ku.Routes = map[int64]kuRouteRec{
		6: {From: 99, To: 199, Time: ms - 1000, Arrival: ms + 59000, UnionID: 303},
		7: {From: 100, To: 200, Time: ms - 30000, Arrival: ms + 20000, UnionID: 101}, // mid-stale pid7
	}
	tt := &TeleBot{}
	tt.session.Store(int64(42), lp)
	tt.session.Store(int64(1001), epA)
	tt.session.Store(int64(1002), epB)
	tt.session.Store(int64(1003), epC)

	out := tt.kuMergeRouteLedgers(42)
	if len(out) != 3 { // pid 6,7,8 present; closed-event pid9 absent
		t.Fatalf("merged %d rows want 3: %+v", len(out), out)
	}
	if r := out[7]; r.Time != ms-10000 || r.To != 205 {
		t.Fatalf("stale pid7 won over epB's fresher row: %+v", r)
	}
	if r := out[8]; r.UnionID != 202 {
		t.Fatalf("event-A-only march lost: %+v", r)
	}
	if _, ok := out[9]; ok {
		t.Fatalf("closed-event account leaked a march: %+v", out[9])
	}
}
