package tytd_go

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// newYardPlayer is a Player wired like a live one enough for the yard paths:
// a cancellable ctx (yardDrawTick refuses without it, cancelling also
// defuses its cooldown timers at test end) and a roomy writeChan.
func newYardPlayer(t *testing.T) *Player {
	t.Helper()
	p := &Player{Username: "u", UID: 42}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.writeChan = make(chan []byte, 16)
	return p
}

// Yard snapshot → collect/make decisions. Everything is driven through
// yardHandleEnter like the reader goroutine does, and asserted on the frames
// that reach writeChan: a GAIN_REWARD (0x34b13) per ripe instance, a
// BUILD_MAKE (0x34b11) only for idle wish pools with a known recipe.

func yardInstance(uid int64, bid int32, d *proto.YardBuildDetailMsg) *proto.YardBuildingMsg {
	return &proto.YardBuildingMsg{
		YardCellMsg:        &proto.YardCellMsg{UniqueId: pb.Int64(uid), BuildId: pb.Int32(bid)},
		YardBuildDetailMsg: d,
	}
}

func snapBytes(t *testing.T, areas ...*proto.YardAreaInfoMsg) []byte {
	t.Helper()
	msg, err := pb.Marshal(&proto.YardEnterResp{Ret: 0, AreaInfo: areas})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return msg
}

func TestYardEnterDecisions(t *testing.T) {
	p := newYardPlayer(t)
	now := time.Now().UnixMilli()

	// Area 1 (default) with one representative of every state.
	area := &proto.YardAreaInfoMsg{AreaId: pb.Int32(1), BuildingMsg: []*proto.YardBuildingMsg{
		// 1. idle wish pool, recipe never learned → nothing may be sent.
		yardInstance(11, yardWishPoolBuildID, &proto.YardBuildDetailMsg{Status: pb.Int32(0)}),
		// 2. farmland still making, start >1 min ago, no endTime → collect.
		yardInstance(12, 1001, &proto.YardBuildDetailMsg{Status: pb.Int32(1), StartTime: pb.Int64(now - 10*60_000)}),
		// 3. wish pool making a real batch, started >1 min ago → learn + collect.
		yardInstance(13, yardWishPoolBuildID, &proto.YardBuildDetailMsg{
			Status: pb.Int32(1), StartTime: pb.Int64(now - 2*60_000),
			ProductId: pb.Int32(400016), TotalNum: pb.Int64(158),
		}),
		// 4. stove making but only 10 s in and endTime in the future → not ripe.
		yardInstance(14, 1002, &proto.YardBuildDetailMsg{
			Status: pb.Int32(1), StartTime: pb.Int64(now - 10_000), EndTime: pb.Int64(now + 5*60_000),
		}),
		// 5. finished run (endTime passed, status still 1) → collect.
		yardInstance(15, 1001, &proto.YardBuildDetailMsg{
			Status: pb.Int32(1), StartTime: pb.Int64(now - 30*60_000), EndTime: pb.Int64(now - 60_000),
		}),
		// 6. tree auto-producing → collect, and never a make later either.
		yardInstance(16, 1003, &proto.YardBuildDetailMsg{Status: pb.Int32(1), StartTime: pb.Int64(now - 10*60_000)}),
		// 7. wish pool upgrading → neither collect nor make.
		yardInstance(17, yardWishPoolBuildID, &proto.YardBuildDetailMsg{
			Status: pb.Int32(2), LevelUpEndTime: pb.Int64(now + 600_000),
		}),
		// 8. wish pool making <1 min → collect not yet worth trying.
		yardInstance(18, yardWishPoolBuildID, &proto.YardBuildDetailMsg{
			Status: pb.Int32(1), StartTime: pb.Int64(now - 20_000),
			ProductId: pb.Int32(400016), TotalNum: pb.Int64(3),
		}),
	}}
	p.yardHandleEnter(snapBytes(t, area))

	// Exactly four GAIN_REWARD frames: instances 12, 13, 15 and 16.
	var gains []int64
	var makes int
	for n := len(p.writeChan); n > 0; n-- {
		frame := <-p.writeChan
		switch frameFuncType(t, frame) {
		case YARD_GAIN_REWARD_CMD:
			var req proto.YardBuildGainRewardReq
			if err := pb.Unmarshal(frame[18:], &req); err != nil {
				t.Fatalf("bad gain payload: %v", err)
			}
			gains = append(gains, req.GetUniqueId())
		case YARD_BUILD_MAKE_CMD:
			makes++
		default:
			t.Fatalf("unexpected frame type %x", frame)
		}
	}
	if makes != 0 {
		t.Fatalf("made %d batches, want 0 (recipe not learned yet, nothing idle+known)", makes)
	}
	want := map[int64]bool{12: true, 13: true, 15: true, 16: true}
	if len(gains) != len(want) {
		t.Fatalf("collected %v, want %v", gains, []int64{12, 13, 15, 16})
	}
	for _, uid := range gains {
		if !want[uid] {
			t.Fatalf("unexpected collect of instance %d", uid)
		}
	}

	// The recipe from instance 13 is now remembered…
	p.YardMu.Lock()
	rec := p.YardRecipes[13]
	p.YardMu.Unlock()
	if rec == nil || rec.productID != 400016 || rec.num != 158 {
		t.Fatalf("recipe = %+v, want {400016 158}", rec)
	}
	// …and the young instance 18 learned too but wasn't collected.
	p.YardMu.Lock()
	rec18 := p.YardRecipes[18]
	p.YardMu.Unlock()
	if rec18 == nil || rec18.num != 3 {
		t.Fatalf("instance 18 recipe = %+v, want num 3", rec18)
	}

	// Second snapshot: the wish pools go idle. 13 reports level 3, so the
	// static table wins over its cached 400016×158 recipe (client default:
	// highest crop unlocked at the CURRENT level, max batch for that level —
	// level 3 → crop 3 = 400003, batch 72). 11 has no level and no recipe →
	// stays silent; 12 is farmland idle → never made.
	p.yardHandleEnter(snapBytes(t, &proto.YardAreaInfoMsg{AreaId: pb.Int32(1), BuildingMsg: []*proto.YardBuildingMsg{
		yardInstance(11, yardWishPoolBuildID, &proto.YardBuildDetailMsg{Status: pb.Int32(0)}),
		yardInstance(12, 1001, &proto.YardBuildDetailMsg{Status: pb.Int32(0)}),
		yardInstance(13, yardWishPoolBuildID, &proto.YardBuildDetailMsg{Status: pb.Int32(0), Level: pb.Int32(3)}),
	}}))
	if len(p.writeChan) != 1 {
		t.Fatalf("queued %d frames, want exactly the 13 restart", len(p.writeChan))
	}
	frame := <-p.writeChan
	if ft := frameFuncType(t, frame); ft != YARD_BUILD_MAKE_CMD {
		t.Fatalf("frame %s, want make request", ft)
	}
	var req proto.YardBuildMakeReq
	if err := pb.Unmarshal(frame[18:], &req); err != nil {
		t.Fatalf("bad make payload: %v", err)
	}
	if req.GetUniqueId() != 13 || req.GetBuildId() != yardWishPoolBuildID ||
		req.GetProductId() != 400003 || req.GetNum() != 72 || req.GetIsCancel() {
		t.Fatalf("make = %+v, want {13 1004 400003 72 false}", &req)
	}
}

// The static selection must reproduce the client exactly. The wire capture
// (level 17 → productId 400016, num 158) is the anchor case: threshold 16
// covers crop 16 but not crop 17, and batch is 158.
func TestYardStaticSelection(t *testing.T) {
	cases := []struct {
		lv           int32
		product, num int32
		ok           bool
	}{
		{1, 400001, 48, true},
		{16, 400016, 156, true}, // level 16 already unlocks crop 16
		{17, 400016, 158, true}, // wire capture case: crop 17 NOT yet unlocked
		{18, 400017, 160, true},
		{50, 400028, 237, true},
		{0, 0, 0, false},  // partial snapshot without level
		{51, 0, 0, false}, // beyond the shipped table
	}
	for _, c := range cases {
		id, num, ok := yardHighestWishProduct(c.lv)
		if id != c.product || num != c.num || ok != c.ok {
			t.Fatalf("level %d → (%d,%d,%v), want (%d,%d,%v)", c.lv, id, num, ok, c.product, c.num, c.ok)
		}
	}
	// getUnlockCropNeedLv parity: crop 16 first unlocks at level 16 (whose
	// threshold is also 16; level 17 repeats it).
	if got := yardRequiredWishLevel(16); got != 16 {
		t.Fatalf("required level for crop 16 = %d, want 16", got)
	}
	if got := yardRequiredWishLevel(28); got != 47 {
		t.Fatalf("required level for crop 28 = %d, want 47", got)
	}
}

// A recipe the server left on an IDLE detail (status 0 but productId set)
// is picked up and used in the same pass — no waiting for a producing
// snapshot.
func TestYardIdleCarriesRecipe(t *testing.T) {
	p := newYardPlayer(t)
	p.yardHandleEnter(snapBytes(t, &proto.YardAreaInfoMsg{AreaId: pb.Int32(1), BuildingMsg: []*proto.YardBuildingMsg{
		yardInstance(7, yardWishPoolBuildID, &proto.YardBuildDetailMsg{
			Status: pb.Int32(0), ProductId: pb.Int32(400016), TotalNum: pb.Int64(158),
		}),
	}}))
	if len(p.writeChan) != 1 {
		t.Fatalf("queued %d frames, want the restart", len(p.writeChan))
	}
	frame := <-p.writeChan
	if frameFuncType(t, frame) != YARD_BUILD_MAKE_CMD {
		t.Fatalf("frame must be the make request, got %x", frame)
	}
}

// ret != 0 means the yard isn't in a usable state — zero frames out.
func TestYardEnterRetNonZero(t *testing.T) {
	p := newYardPlayer(t)
	msg, _ := pb.Marshal(&proto.YardEnterResp{Ret: 7})
	p.yardHandleEnter(msg)
	if len(p.writeChan) != 0 {
		t.Fatalf("ret!=0 snapshot queued %d frames", len(p.writeChan))
	}
	p.yardHandleEnter([]byte{0xff, 0xff, 0xff, 0xff, 0xff}) // garbage must not panic
	if len(p.writeChan) != 0 {
		t.Fatal("garbage snapshot queued frames")
	}
}

// The draw commands must map like every other yard command: outgoing full id,
// incoming id%100000 as %04x.
func TestYardDrawCmdMap(t *testing.T) {
	for _, c := range []struct {
		cmd     string
		full    int64
		srvType string
	}{
		{YARD_DRAW_CMD, 215822, SERVER_YARD_DRAW},
	} {
		n, err := strconv.ParseInt(c.cmd, 16, 64)
		if err != nil || n != c.full {
			t.Fatalf("%s parses to %d/%v, want %d", c.cmd, n, err, c.full)
		}
		if got := fmt.Sprintf("%04x", c.full%100000); got != c.srvType {
			t.Fatalf("%d transport = %s, want %s", c.full, got, c.srvType)
		}
	}
	if n, _ := strconv.ParseInt("00034b2a", 16, 64); n != 215850 {
		t.Fatalf("data sync cmd is 00034b2a / 215850")
	}
	if got := fmt.Sprintf("%04x", 215850%100000); got != SERVER_YARD_DATA_SYNC {
		t.Fatalf("215850 transport = %s, want %s", got, SERVER_YARD_DATA_SYNC)
	}
}

// The exact wire bodies: proto2-required isTen must be present even when
// false; the default pool 0 goes out like the client sends it; the ad draw
// only differs by the ad-slot flag.
func TestYardDrawReqWire(t *testing.T) {
	free, err := pb.Marshal(&proto.YardDrawReq{IsTen: pb.Bool(false), PoolId: pb.Int32(0)})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", free); got != "08002000" {
		t.Fatalf("free draw wire = %s, want 08002000", got)
	}
	ad, err := pb.Marshal(&proto.YardDrawReq{IsTen: pb.Bool(false), IsADType: pb.Bool(true), PoolId: pb.Int32(0)})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", ad); got != "080018012000" {
		t.Fatalf("ad draw wire = %s, want 080018012000", got)
	}
}

func TestYardDrawWant(t *testing.T) {
	now := time.Now()
	data := func(free, ad int32) *proto.YardDrawDataMsg {
		return &proto.YardDrawDataMsg{FreeDrawTimes: pb.Int32(free), AdCount: pb.Int32(ad)}
	}
	st := &yardDrawState{day: now.In(ictZone).Format("2006-01-02"), data: data(0, 0)}
	if got := yardDrawWant(st, now); got != yardDrawFree {
		t.Fatalf("fresh day = %d, want free", got)
	}
	st.freeSent = 1
	if got := yardDrawWant(st, now); got != yardDrawAd {
		t.Fatalf("free spent locally (server stale at 0) = %d, want ad — local cap protects paid tickets", got)
	}
	st.adSent = 1
	if got := yardDrawWant(st, now); got != yardDrawAd {
		t.Fatalf("one ad left = %d, want ad", got)
	}
	st.adSent = 2
	if got := yardDrawWant(st, now); got != yardDrawNone {
		t.Fatalf("day budget spent = %d, want none", got)
	}
	// Cooldown and backoff block everything.
	st.lastSend = now.Add(-time.Second)
	if got := yardDrawWant(st, now); got != yardDrawNone {
		t.Fatalf("inside cooldown = %d, want none", got)
	}
	st.lastSend = time.Time{}
	st.nextTry = now.Add(time.Minute)
	if got := yardDrawWant(st, now); got != yardDrawNone {
		t.Fatalf("in backoff = %d, want none", got)
	}
	// A new ICT day refills the local budget but not the server counters:
	// with freeDrawTimes already at the cap, only the ad side may fire.
	st = &yardDrawState{day: "2000-01-01", freeSent: 1, adSent: 2, data: data(0, 1)}
	if got := yardDrawWant(st, now); got != yardDrawFree {
		t.Fatalf("rollover refills local free = %d, want free", got)
	}
	if st.freeSent != 0 || st.adSent != 0 {
		t.Fatalf("rollover did not reset local budget: %+v", st)
	}
	st.data = data(1, 1)
	if got := yardDrawWant(st, now); got != yardDrawAd {
		t.Fatalf("server free spent = %d, want ad", got)
	}
	// No server counters yet → nothing.
	if got := yardDrawWant(&yardDrawState{}, now); got != yardDrawNone {
		t.Fatalf("no data = %d, want none", got)
	}
}

// End to end over the fake socket: an ENTER carrying fresh counters fires
// the free draw; a refusal sits out; fresh counters chain the ad draws and
// the day's budget caps at 1 free + 2 ad.
func TestYardDrawFlow(t *testing.T) {
	p := newYardPlayer(t)
	snap := func(free, ad int32) []byte {
		msg, err := pb.Marshal(&proto.YardEnterResp{Ret: 0, DrawData: &proto.YardDrawDataMsg{
			FreeDrawTimes: pb.Int32(free), AdCount: pb.Int32(ad),
		}})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	// Past the cooldown so chained state advances deterministically.
	uncooldown := func() {
		p.YardMu.Lock()
		p.yardDraw.lastSend = time.Now().Add(-time.Hour)
		p.YardMu.Unlock()
	}
	recvDraw := func(wantAD bool) {
		t.Helper()
		if len(p.writeChan) != 1 {
			t.Fatalf("queued %d frames, want one draw", len(p.writeChan))
		}
		frame := <-p.writeChan
		if ft := frameFuncType(t, frame); ft != YARD_DRAW_CMD {
			t.Fatalf("frame %s, want %s", ft, YARD_DRAW_CMD)
		}
		var req proto.YardDrawReq
		if err := pb.Unmarshal(frame[18:], &req); err != nil {
			t.Fatalf("bad draw payload: %v", err)
		}
		if req.GetIsTen() || req.GetIsUseADTime() || req.GetPoolId() != 0 || req.GetTimes() != 0 {
			t.Fatalf("draw req = %+v", &req)
		}
		if req.GetIsADType() != wantAD {
			t.Fatalf("isADType = %v, want %v", req.GetIsADType(), wantAD)
		}
	}

	p.yardHandleEnter(snap(0, 0)) // free + ad budget open
	recvDraw(false)               // free draw first
	// …and the immediate follow-up is cooldown-blocked, so nothing else lands.
	p.yardDrawTick()
	if len(p.writeChan) != 0 {
		t.Fatalf("cooldown leaked a frame")
	}

	// Server says free spent (ret==0 not needed here): counters via a draw
	// resp also feed the state; refuse one first to prove the sit-out.
	refused, _ := pb.Marshal(&proto.YardDrawResp{Ret: 3})
	p.yardHandleDraw(refused)
	uncooldown()
	p.yardDrawTick()
	if len(p.writeChan) != 0 {
		t.Fatalf("backoff leaked a frame")
	}
	p.YardMu.Lock()
	p.yardDraw.nextTry = time.Time{}
	p.YardMu.Unlock()

	// Fresh counters from a draw resp: free spent, adCount 0 → ad draw.
	withDrawData, _ := pb.Marshal(&proto.YardDrawResp{Ret: 0, DrawData: &proto.YardDrawDataMsg{
		FreeDrawTimes: pb.Int32(1), AdCount: pb.Int32(0),
	}})
	p.yardHandleDraw(withDrawData)
	uncooldown()
	p.yardDrawTick()
	recvDraw(true)
	uncooldown()
	p.yardDrawTick()
	recvDraw(true) // second and last ad slot
	uncooldown()
	p.yardDrawTick()
	if len(p.writeChan) != 0 {
		t.Fatalf("day budget leaked a third draw")
	}

	// Day rollover push with reset counters refills everything.
	push, _ := pb.Marshal(&proto.YardRefreshDataSync{DrawData: &proto.YardDrawDataMsg{
		FreeDrawTimes: pb.Int32(0), AdCount: pb.Int32(0),
	}})
	p.YardMu.Lock()
	p.yardDraw.day = "2000-01-01"
	p.YardMu.Unlock()
	p.yardHandleDataSync(push)
	recvDraw(false)
}

// ret!=0 ENTER (yard closed) must not touch the draw state at all.
func TestYardDrawIgnoresClosedYard(t *testing.T) {
	p := newYardPlayer(t)
	msg, _ := pb.Marshal(&proto.YardEnterResp{Ret: 9, DrawData: &proto.YardDrawDataMsg{}})
	p.yardHandleEnter(msg)
	if len(p.writeChan) != 0 {
		t.Fatalf("closed yard queued %d frames", len(p.writeChan))
	}
	p.YardMu.Lock()
	defer p.YardMu.Unlock()
	if p.yardDraw.data != nil {
		t.Fatalf("closed yard refreshed draw counters")
	}
}
