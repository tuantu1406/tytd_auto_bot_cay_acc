package tytd_go

// mapStreamPayload("kunlun") end-to-end with a scripted game connection:
// the leader session answers 219919 and the frame must decode to 37 cities.

import (
	"context"
	"os"
	"testing"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func TestKunlunStreamPayloadE2E(t *testing.T) {
	bin, err := os.ReadFile("/tmp/kunlun_live_mini.bin")
	if err != nil {
		t.Skip("no live mini bin")
	}
	// sanity: ret==0 & has cities
	var rsp proto.KunlunWarMiniMapInfoRespMsg
	if pb.Unmarshal(bin, &rsp) != nil || len(rsp.GetCityInfo()) != 37 {
		t.Skip("live bin not usable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Player{Username: "leader", UID: 7, Running: true, ctx: ctx, cancel: cancel,
		writeChan: make(chan []byte, 8)}
	now := time.Now()
	full, _ := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{{
		ActivityId: pb.Int32(555), Type: pb.Int32(activityTypeKunlunWar),
		BeginShowTime: pb.Int64(now.Add(-time.Hour).UnixMilli()),
		BeginTime:     pb.Int64(now.Add(-time.Hour).UnixMilli()),
		EndTime:       pb.Int64(now.Add(time.Hour).UnixMilli()),
		EndShowTime:   pb.Int64(now.Add(2 * time.Hour).UnixMilli()),
	}}})
	p.ActList.applyList("leader", full, true)
	go func() {
		for f := range p.writeChan {
			isMSG, ty, _ := pack.Unpack(f)
			if !isMSG {
				continue
			}
			switch ty {
			case KUNLUN_ENTER_ACTIVITY_CMD[4:]:
				ent, _ := pb.Marshal(&proto.KunlunWarEnterActivityResp{Ret: pb.Int32(0), Floor: pb.Int32(10000), Group: pb.Int32(3)})
				p.kuDeliver(SERVER_KUNLUN_ENTER_ACTIVITY, ent)
			case KUNLUN_MINI_MAP_INFO_CMD[4:]:
				p.kuDeliver(SERVER_KUNLUN_MINI_MAP_INFO, bin)
			}
		}
	}()
	t2 := &TeleBot{}
	t2.session.Store(int64(7), p)
	res, err := t2.mapStreamPayload(int64(7), "kunlun", nil)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if res.idle {
		t.Fatal("idle — gate rejected an eligible account")
	}
	if res.sm == nil || len(res.sm.Cities) != 37 {
		t.Fatalf("frame cities=%v", func() int {
			if res.sm == nil {
				return -1
			}
			return len(res.sm.Cities)
		}())
	}
}

// kuMineOverlay stamps the viewing leader's own event accounts onto a
// kunlun frame: the tile an in-scene account stands on must appear in Ku.Mine
// (the radial menu's explore/sweep gate) and its union must map to the same
// palette slot KuFrame.Cells uses (Ku.MySlots).
func TestKunlunMineOverlay(t *testing.T) {
	bin, err := os.ReadFile("/tmp/kunlun_live_mini.bin")
	if err != nil {
		t.Skip("no live mini bin")
	}
	var rsp proto.KunlunWarMiniMapInfoRespMsg
	if pb.Unmarshal(bin, &rsp) != nil || len(rsp.GetCellInfo()) == 0 {
		t.Skip("live bin has no occupied cells")
	}
	var uid int64
	for _, cl := range rsp.GetCellInfo() {
		if cl.GetUnionId() != 0 {
			uid = cl.GetUnionId()
			break
		}
	}
	if uid == 0 {
		t.Skip("no union-occupied cell in bin")
	}
	sm, err := gimap.Stream("kunlun", bin)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	ep := &EventPlayer{Username: "ev1", Running: true}
	ep.ku = &kuEventState{SceneIn: true, Point: 123, MyUnion: uid}
	tt := &TeleBot{}
	tt.session.Store(int64(5), ep)
	tt.kuMineOverlay(sm, int64(5), bin)
	if sm.Ku == nil {
		t.Fatal("Ku frame vanished")
	}
	if len(sm.Ku.Mine) != 1 || sm.Ku.Mine[0] != 123 {
		t.Fatalf("Mine=%v want [123]", sm.Ku.Mine)
	}
	want, err := gimap.KunlunSlotsOf(bin)
	if err != nil {
		t.Fatalf("KunlunSlotsOf: %v", err)
	}
	slot, ok := want[uid]
	if !ok {
		t.Fatalf("bin union %d has no slot", uid)
	}
	found := false
	for _, s := range sm.Ku.MySlots {
		if s == slot {
			found = true
		}
	}
	if !found {
		t.Fatalf("MySlots=%v missing slot %d", sm.Ku.MySlots, slot)
	}
}

// The "no marches on screen" regression, round two: kunlun used to have NO
// event-account candidates at all (leader-only pull, phone holds the scene
// seat → zero routes). The first fix still put the leader AHEAD of unseated
// event accounts, and the leader pull always succeeded — so idle accounts
// never got their turn, never got seated by the c.ep arm, and marches stayed
// empty forever. Correct order: seated events, then idle events (they become
// seated feeders), leader last as pure fallback.
func TestKunlunStreamEventSeatedFirst(t *testing.T) {
	tt := &TeleBot{} // ydb nil → member tail skipped, exactly the test shape
	now := time.Now()
	full, err := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{{
		ActivityId:    pb.Int32(666),
		Type:          pb.Int32(activityTypeKunlunWar),
		BeginShowTime: pb.Int64(now.Add(-time.Hour).UnixMilli()),
		BeginTime:     pb.Int64(now.Add(-time.Hour).UnixMilli()),
		EndTime:       pb.Int64(now.Add(time.Hour).UnixMilli()),
		EndShowTime:   pb.Int64(now.Add(2 * time.Hour).UnixMilli()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	lp := &Player{Username: "lead", UID: 7, Running: true}
	lp.ActList.applyList("lead", full, true)
	if !kunlunWarOpen(lp) {
		t.Fatal("leader kunlun window not seen")
	}
	tt.session.Store(int64(9), lp)
	seated, _ := kuOpenEventPlayer(t)
	seated.ChatID = 9
	seated.ku = &kuEventState{SceneIn: true}
	tt.session.Store(int64(4242), seated)
	idle, _ := kuOpenEventPlayer(t)
	idle.ChatID = 9
	idle.ku = &kuEventState{}
	tt.session.Store(int64(4343), idle)
	cands := tt.streamCandidates(int64(9), gimap.StreamKunlun, nil)
	if len(cands) != 3 {
		t.Fatalf("cands=%d, want 3 (seated, leader, idle)", len(cands))
	}
	if cands[0].ep != seated {
		t.Fatal("seated battle bot must pull first — it holds the march feed")
	}
	if cands[1].ep != idle {
		t.Fatal("unseated event account must precede the leader — otherwise it never gets seated and marches never flow")
	}
	if cands[2].pl != lp {
		t.Fatal("leader is the fallback only — it trails every event account")
	}
}

// The full user-visible path for marches with NO leader client in the scene:
// a seated event account alone answers 219919, and its route ledger must ride
// out on KuFrame.Routes with the stand-point overlay still applied.
func TestKunlunStreamEventPuller(t *testing.T) {
	bin, err := os.ReadFile("/tmp/kunlun_live_mini.bin")
	if err != nil {
		t.Skip("no live mini bin")
	}
	var rsp proto.KunlunWarMiniMapInfoRespMsg
	if pb.Unmarshal(bin, &rsp) != nil || len(rsp.GetCellInfo()) == 0 {
		t.Skip("live bin has no occupied cells")
	}
	var uid int64
	for _, cl := range rsp.GetCellInfo() {
		if cl.GetUnionId() != 0 {
			uid = cl.GetUnionId()
			break
		}
	}
	if uid == 0 {
		t.Skip("no union-occupied cell in bin")
	}
	ep, frames := kuOpenEventPlayer(t)
	ep.ChatID = 9
	go func() {
		for f := range frames {
			isMSG, ty, _ := pack.Unpack(f)
			if !isMSG {
				continue
			}
			switch ty {
			case KUNLUN_ENTER_ACTIVITY_CMD[4:]:
				ent, _ := pb.Marshal(&proto.KunlunWarEnterActivityResp{Ret: pb.Int32(0), Floor: pb.Int32(10000), Group: pb.Int32(3)})
				ep.kuDeliverFrame(SERVER_KUNLUN_ENTER_ACTIVITY, ent)
			case KUNLUN_MINI_MAP_INFO_CMD[4:]:
				ep.kuDeliverFrame(SERVER_KUNLUN_MINI_MAP_INFO, bin)
			}
		}
	}()
	nowMs := time.Now().UnixMilli()
	ep.ku = &kuEventState{
		SceneIn:   true,
		Point:     123,
		MyUnion:   uid,
		Routes:    map[int64]kuRouteRec{777: {From: 10, To: 20, Time: nowMs, Arrival: nowMs + 30000, UnionID: uid}},
	}
	tt := &TeleBot{}
	tt.session.Store(int64(9), ep)
	res, err := tt.mapStreamPayload(int64(9), "kunlun", nil)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if res.idle || res.sm == nil {
		t.Fatal("idle — seated event account rejected as puller")
	}
	if len(res.sm.Cities) == 0 {
		t.Fatal("frame has no cities")
	}
	if res.sm.Ku == nil || len(res.sm.Ku.Routes) != 1 {
		t.Fatalf("routes=%v, want the one live march", func() []gimap.KuRoute {
			if res.sm.Ku == nil {
				return nil
			}
			return res.sm.Ku.Routes
		}())
	}
	r := res.sm.Ku.Routes[0]
	if r.FP != 10 || r.TP != 20 || r.K != 3 {
		t.Fatalf("route=%+v, want fp=10 tp=20 k=3 (own union)", r)
	}
	if r.S < 0 {
		t.Fatalf("route slot %d, want a palette slot from the same payload", r.S)
	}
	// The overlay must still fire from the same session load (no ydb).
	if len(res.sm.Ku.Mine) != 1 || res.sm.Ku.Mine[0] != 123 {
		t.Fatalf("Mine=%v want [123]", res.sm.Ku.Mine)
	}
	slots, err := gimap.KunlunSlotsOf(bin)
	if err != nil {
		t.Fatalf("KunlunSlotsOf: %v", err)
	}
	want := false
	if s, ok := slots[uid]; ok {
		for _, g := range res.sm.Ku.MySlots {
			if g == s {
				want = true
			}
		}
	}
	if !want {
		t.Fatalf("MySlots=%v missing union %d's slot", res.sm.Ku.MySlots, uid)
	}
}

// The /kunlun page marks "other players fighting HERE" from the per-tile
// haveFight of 219919 (block-level, not just cities): owned quiet → slot tint
// without F; owned+fought → same slot AND F; EMPTY+fought → shipped with
// s=−1 so the browser adds no tint (slot 0 would paint the first union's
// colour onto a tile nobody owns); empty quiet → dropped entirely. No /tmp
// dependency — the payload is synthetic.
func TestKunlunStreamCombatCells(t *testing.T) {
	payload, err := pb.Marshal(&proto.KunlunWarMiniMapInfoRespMsg{
		Ret: pb.Int32(0),
		CellInfo: []*proto.KunlunWarMiniCellInfo{
			{Index: pb.Int32(1285), UnionId: pb.Int64(9001)},                        // owned, quiet
			{Index: pb.Int32(1286), UnionId: pb.Int64(9002), HaveFight: pb.Bool(true)}, // owned, fought over
			{Index: pb.Int32(1287), HaveFight: pb.Bool(true)},                      // contested, nobody holds it
			{Index: pb.Int32(1288)},                                                // empty, quiet
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sm, err := gimap.Stream("kunlun", payload)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if sm.Ku == nil {
		t.Fatal("Ku frame missing")
	}
	if len(sm.Ku.Cells) != 3 {
		t.Fatalf("cells=%d, want 3 (quiet empty tile dropped)", len(sm.Ku.Cells))
	}
	byP := map[int64]gimap.KuCell{}
	for _, c := range sm.Ku.Cells {
		byP[c.P] = c
	}
	if _, ok := byP[1288]; ok {
		t.Fatal("quiet empty tile shipped")
	}
	if c := byP[1285]; c.S != 0 || c.F {
		t.Fatalf("1285=%+v, want slot 0 quiet (first union in row order)", c)
	}
	if c := byP[1286]; c.S != 1 || !c.F {
		t.Fatalf("1286=%+v, want slot 1 + fight flag", c)
	}
	if c := byP[1287]; c.S != -1 || !c.F {
		t.Fatalf("1287=%+v, want s=-1 + fight flag (no tint)", c)
	}
}
