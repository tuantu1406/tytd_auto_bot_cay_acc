package tytd_go

import (
	"context"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Samples come from ../remoteBlessFrog/STATIC_ANALYSIS.md wire captures, so
// these pin internal/proto's God Island schema to the real protocol.

// minimap sample: field2 (len 5) = 08 89 27 18 00 decodes as
// cityId=5001, no baseData (unowned), haveFight=false
func TestGIMiniMapSample(t *testing.T) {
	var resp proto.GodIslandGameMiniMapInfoRespMsg
	if err := pb.Unmarshal([]byte{0x12, 0x05, 0x08, 0x89, 0x27, 0x18, 0x00}, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.GetCityInfo()) != 1 {
		t.Fatalf("cities: %d", len(resp.GetCityInfo()))
	}
	c := resp.GetCityInfo()[0]
	if c.GetCityId() != 5001 || c.GetBaseData() != nil || c.GetHaveFight() {
		t.Fatalf("got %+v", c)
	}
}

// base info sample: 08 AB 8A A9 04 -> activityId=9061675, 10 91 4E -> floor=10001
func TestGIBaseInfoSample(t *testing.T) {
	var resp proto.GodIslandBaseInfoRespMsg
	// ret=1 (08 01), activityId field 2, floor field 5: 28 91 4E
	if err := pb.Unmarshal([]byte{0x08, 0x01, 0x10, 0xAB, 0x8A, 0xA9, 0x04, 0x28, 0x91, 0x4E}, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.GetRet() != 1 || resp.GetActivityId() != 9061675 || resp.GetFloor() != 10001 {
		t.Fatalf("got %+v", &resp)
	}
}

// activity push sample: one mainConfig entry { activityId=9061675, type=68 }
func TestGIActivityPush(t *testing.T) {
	msg, err := pb.Marshal(&proto.PushActivityList{
		MainConfig: []*proto.ActivityMainConfig{{
			ActivityId: pb.Int32(9061675),
			Type:       pb.Int32(giActivityTypeGodIsland),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var push proto.PushActivityList
	if err := pb.Unmarshal(msg, &push); err != nil {
		t.Fatal(err)
	}
	cfg := push.GetMainConfig()[0]
	if cfg.GetActivityId() != 9061675 || cfg.GetType() != giActivityTypeGodIsland {
		t.Fatalf("got %+v", cfg)
	}
}

// GodIslandMoveTo is command-driven (no auto-play loop): it must refuse
// stopped/unknown-activity/bad-city orders without touching the socket, and
// queue exactly move-then-auto-attack for a valid order.
func TestGIMoveTo(t *testing.T) {
	p := &EventPlayer{}
	if err := p.GodIslandMoveTo(GIEventNormal, 2005); err == nil {
		t.Fatal("expected error for stopped account")
	}

	p.Running = true
	p.writeChan = make(chan []byte, 4)
	wireCtxEP(t, p)
	if err := p.GodIslandMoveTo(GIEventNormal, 2005); err == nil {
		t.Fatal("expected error without activity id")
	}

	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	// Marching HOME is legal (owner 2026-10-19 "hành quân cho phép hành quân
	// về nhà") — only bad ids are refused.
	for _, bad := range []int32{0, -5} {
		if err := p.GodIslandMoveTo(GIEventNormal, bad); err == nil {
			t.Fatalf("expected error for city %d", bad)
		}
	}
	if len(p.writeChan) != 0 {
		t.Fatal("refused orders must not reach the socket")
	}
	for _, home := range []int32{giHomeCityID, 6400} {
		ev.Home = home
		if err := p.GodIslandMoveTo(GIEventNormal, home); err != nil {
			t.Fatalf("march home (%d) must be allowed: %v", home, err)
		}
		if f := <-p.writeChan; frameFuncType(t, f) != GOD_ISLAND_MOVE_CMD {
			t.Fatalf("home march must queue the move frame, got %x", f)
		}
	}
	ev.Home = 0

	if err := p.GodIslandMoveTo(GIEventNormal, 2005); err != nil {
		t.Fatal(err)
	}
	if len(p.writeChan) != 1 {
		t.Fatalf("queued %d frames, want move only", len(p.writeChan))
	}
	first := <-p.writeChan
	if len(first) < 10 || hex.EncodeToString(first[6:10]) != GOD_ISLAND_MOVE_CMD {
		t.Fatalf("frame must be the move request, got %x", first)
	}
}

// /bonglai_canqua → one 206837 frame carrying {activityId, useCount};
// the 50-fruit default itself is the handler's job, this checks the refusal
// paths and the wire payload.
func TestGIUseFruit(t *testing.T) {
	p := &EventPlayer{}
	if err := p.GodIslandUseFruit(GIEventNormal, 50); err == nil {
		t.Fatal("expected error for stopped account")
	}

	p.Running = true
	p.writeChan = make(chan []byte, 4)
	wireCtxEP(t, p)
	if err := p.GodIslandUseFruit(GIEventNormal, 50); err == nil {
		t.Fatal("expected error without activity id")
	}

	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	if err := p.GodIslandUseFruit(GIEventNormal, 0); err == nil {
		t.Fatal("expected error for zero count")
	}
	if len(p.writeChan) != 0 {
		t.Fatal("refused orders must not reach the socket")
	}

	if err := p.GodIslandUseFruit(GIEventNormal, 50); err != nil {
		t.Fatal(err)
	}
	if len(p.writeChan) != 1 {
		t.Fatalf("queued %d frames, want 1", len(p.writeChan))
	}
	frame := <-p.writeChan
	if frameFuncType(t, frame) != GOD_ISLAND_USE_FRUIT_CMD {
		t.Fatalf("frame must be the use-fruit request, got %x", frame)
	}
	var req proto.GodIslandUseFruitReq
	if err := pb.Unmarshal(frame[18:], &req); err != nil {
		t.Fatalf("bad use-fruit payload: %v", err)
	}
	if req.GetActivityId() != 42 || req.GetUseCount() != 50 {
		t.Fatalf("use fruit %d/%d, want 42/50", req.GetActivityId(), req.GetUseCount())
	}
}

// /bonglai_tangtoc → one 206849 frame with speedUpCount = the CURRENT cached
// count (the server does the increment itself); the cache is only ever what
// the INFO_LOAD poll reported, orders never bump it.
func TestGIAccelerate(t *testing.T) {
	p := &EventPlayer{}
	if err := p.GodIslandAccelerate(GIEventNormal); err == nil {
		t.Fatal("expected error for stopped account")
	}

	p.Running = true
	p.writeChan = make(chan []byte, 8)
	wireCtxEP(t, p)
	if err := p.GodIslandAccelerate(GIEventNormal); err == nil {
		t.Fatal("expected error without activity id")
	}

	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	// server requires myRouteInfo (created by MOVE): refuse while stationed
	if err := p.GodIslandAccelerate(GIEventNormal); err == nil {
		t.Fatal("expected error while not marching")
	}

	route := &proto.GodIslandGameRouteInfo{ToCityId: pb.Int32(2005)}
	// server truth: marching, 3 speed-ups already used
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{
		Ret:          0,
		SpeedUpCount: pb.Int32(3),
		MyRouteInfo:  route,
	}))
	if ev.SpeedUp != 3 || !ev.Moving {
		t.Fatalf("cache=%d moving=%v, want 3/true", ev.SpeedUp, ev.Moving)
	}
	// ret != 0 or missing count must not clobber the cache
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{Ret: 9, SpeedUpCount: pb.Int32(0), MyRouteInfo: route}))
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{Ret: 0, MyRouteInfo: route}))
	if ev.SpeedUp != 3 {
		t.Fatalf("cache=%d after bad infos, want 3", ev.SpeedUp)
	}

	// orders send the CURRENT cached count untouched (3, 3): the server
	// increments, we never bump.
	for _, want := range []int32{3, 3} {
		if err := p.GodIslandAccelerate(GIEventNormal); err != nil {
			t.Fatal(err)
		}
		frame := <-p.writeChan
		if frameFuncType(t, frame) != GOD_ISLAND_ACCELERATE_CMD {
			t.Fatalf("frame must be the accelerate request, got %x", frame)
		}
		var req proto.GodIslandGameAcclerateMoveReqMsg
		if err := pb.Unmarshal(frame[18:], &req); err != nil {
			t.Fatalf("bad accelerate payload: %v", err)
		}
		if req.GetActivityId() != 42 || req.GetSpeedUpCount() != want {
			t.Fatalf("accelerate %d/%d, want 42/%d", req.GetActivityId(), req.GetSpeedUpCount(), want)
		}
		// answer accepted → the press closes (server confirms the same
		// count, cache untouched) and the dedup guard lets the next press
		// through. A re-press BEFORE this answer is swallowed — see
		// TestGodIslandAccelerateAsyncAlert.
		p.giHandleAccelerateResp(ev, mustMarshal(t, &proto.GodIslandGameAcclerateMoveRespMsg{
			Ret: 0, SpeedUpCount: pb.Int32(want),
		}))
	}
	if ev.SpeedUp != 3 {
		t.Fatalf("cache=%d, want 3 (orders must not bump it)", ev.SpeedUp)
	}

	// the 1ac1 response's speedUpCount (= N+1) replaces the cache
	p.giHandleAccelerateResp(ev, mustMarshal(t, &proto.GodIslandGameAcclerateMoveRespMsg{
		Ret: 0, CostInfo: pb.String("item:1024:1"), SpeedUpCount: pb.Int32(4),
	}))
	// a REFUSED call (ret != 0) now folds the echo too — self-heal of a
	// drifted baseline (the old handler dropped ret≠0 with zero trace, so
	// one drift became an invisible permanent refusal).
	p.giHandleAccelerateResp(ev, mustMarshal(t, &proto.GodIslandGameAcclerateMoveRespMsg{
		Ret: 9, SpeedUpCount: pb.Int32(2),
	}))
	if ev.SpeedUp != 2 {
		t.Fatalf("cache=%d after refused echo, want 2", ev.SpeedUp)
	}
	if err := p.GodIslandAccelerate(GIEventNormal); err != nil {
		t.Fatal(err)
	}
	frame := <-p.writeChan
	var req proto.GodIslandGameAcclerateMoveReqMsg
	if err := pb.Unmarshal(frame[18:], &req); err != nil {
		t.Fatal(err)
	}
	if req.GetSpeedUpCount() != 2 {
		t.Fatalf("accelerate after self-heal=%d, want 2", req.GetSpeedUpCount())
	}

	// route gone (arrived/home) → refuse again
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{Ret: 0}))
	if err := p.GodIslandAccelerate(GIEventNormal); err == nil {
		t.Fatal("expected error after route cleared")
	}
}

// /bonglai_hoimau → one 206827 frame with usePower = max-cur from the
// cached ball power; optimistic full-ball, reconciled by the 6827 answer.
func TestGIHealPower(t *testing.T) {
	p := &EventPlayer{}
	if err := p.GodIslandHealPower(GIEventNormal); err == nil {
		t.Fatal("expected error for stopped account")
	}

	p.Running = true
	p.writeChan = make(chan []byte, 4)
	wireCtxEP(t, p)
	if err := p.GodIslandHealPower(GIEventNormal); err == nil {
		t.Fatal("expected error without activity id")
	}

	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	if err := p.GodIslandHealPower(GIEventNormal); err == nil {
		t.Fatal("expected error with unknown ball power")
	}
	if len(p.writeChan) != 0 {
		t.Fatal("refused orders must not reach the socket")
	}

	// bad ret must not seed the cache
	p.giHandleBallInfo(ev, mustMarshal(t, &proto.GodIslandGameSpiritualBallInfoRespMsg{
		Ret: 9, BallCurPower: pb.String("10"), BallMaxPower: pb.String("20"),
	}))
	if err := p.GodIslandHealPower(GIEventNormal); err == nil {
		t.Fatal("ret!=0 ball info seeded the cache")
	}

	healWant := func(want int64) {
		t.Helper()
		if err := p.GodIslandHealPower(GIEventNormal); err != nil {
			t.Fatal(err)
		}
		frame := <-p.writeChan
		if frameFuncType(t, frame) != GOD_ISLAND_USE_BALL_CMD {
			t.Fatalf("frame must be the use-ball request, got %x", frame)
		}
		var req proto.GodIslandGameUseSpiritualBallReqMsg
		if err := pb.Unmarshal(frame[18:], &req); err != nil {
			t.Fatalf("bad use-ball payload: %v", err)
		}
		if req.GetActivityId() != 42 || req.GetUsePower() != want {
			t.Fatalf("heal %d/%d, want 42/%d", req.GetActivityId(), req.GetUsePower(), want)
		}
	}
	errWant := func(needle string) {
		t.Helper()
		err := p.GodIslandHealPower(GIEventNormal)
		if err == nil || !strings.Contains(err.Error(), needle) {
			t.Fatalf("expected error containing %q, got %v", needle, err)
		}
		if len(p.writeChan) != 0 {
			t.Fatal("refused orders must not reach the socket")
		}
	}

	// Dead unit (server revive countdown still running): refuse up-front
	// with the countdown — the server rejects use-ball while dead (live:
	// ret=6852), so the order must never reach the socket.
	ev.ReviveTime = time.Now().Add(time.Minute).UnixMilli()
	errWant("đang chết")
	ev.ReviveTime = 0

	// Marching unit: the client only drinks while stationary (or dead), so
	// refuse before burning ball.
	ev.Moving = true
	errWant("đang hành quân")
	ev.Moving = false

	// Live-verified semantics: BallCur is the reservoir drawn 1:1, BallMax a
	// per-draw ceiling the reservoir can exceed. HP unknown: draw the whole
	// reservoir (70, under the 50000 ceiling).
	p.giHandleBallInfo(ev, mustMarshal(t, &proto.GodIslandGameSpiritualBallInfoRespMsg{
		Ret: 0, BallCurPower: pb.String("70"), BallMaxPower: pb.String("50000"),
	}))
	healWant(70)
	// No optimistic bump any more: without server word the reservoir is
	// still 70, so a repeat draws the same amount.
	healWant(70)

	// the 6827 answer reconciles the reservoir to 40
	p.giHandleUseBallResp(ev, mustMarshal(t, &proto.GodIslandGameUseSpiritualBallRespMsg{
		Ret: 0, BallCurPower: pb.String("40"),
	}))
	healWant(40)

	// use-fruit response refreshes the reservoir
	p.giHandleUseFruitResp(ev, mustMarshal(t, &proto.GodIslandUseFruitResp{
		Ret: 0, BallCurPower: pb.String("145"), BallMaxPower: pb.String("50000"),
	}))
	healWant(145)

	// Regression (field report, real numbers from server 710564): dead unit
	// 0/7968 with a 259425 reservoir and a 50000 draw ceiling — the old
	// ballMax-ballCur formula read -209425 and reported "đã cạn" (before my
	// earlier change, "máu đã đầy"). Correct draw: HP need 7968.
	p.giHandleBallInfo(ev, mustMarshal(t, &proto.GodIslandGameSpiritualBallInfoRespMsg{
		Ret: 0, CurPower: pb.String("0"), MaxPower: pb.String("7968"),
		BallCurPower: pb.String("259425"), BallMaxPower: pb.String("50000"),
	}))
	healWant(7968)

	// reservoir can be the tighter constraint: 20/100 blood, ball holds 10
	p.giHandleBallInfo(ev, mustMarshal(t, &proto.GodIslandGameSpiritualBallInfoRespMsg{
		Ret: 0, CurPower: pb.String("20"), MaxPower: pb.String("100"),
		BallCurPower: pb.String("10"), BallMaxPower: pb.String("50000"),
	}))
	healWant(10)

	// INFO_LOAD feeds the HP cache too: 50/100 with a rich ball draws 50
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{
		Ret: 0, CurPower: pb.String("50"), MaxPower: pb.String("100"),
	}))
	p.giHandleBallInfo(ev, mustMarshal(t, &proto.GodIslandGameSpiritualBallInfoRespMsg{
		Ret: 0, BallCurPower: pb.String("5000"), BallMaxPower: pb.String("10000"),
	}))
	healWant(50)

	// genuinely full blood: refuse even with a full reservoir
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{
		Ret: 0, CurPower: pb.String("100"), MaxPower: pb.String("100"),
	}))
	errWant("máu đã đầy")

	// dry reservoir with missing blood: distinct from the full-HP refusal.
	p.giHandleBallInfo(ev, mustMarshal(t, &proto.GodIslandGameSpiritualBallInfoRespMsg{
		Ret: 0, CurPower: pb.String("0"), MaxPower: pb.String("100"),
		BallCurPower: pb.String("0"), BallMaxPower: pb.String("50000"),
	}))
	errWant("linh lực cầu đã cạn")

	// per-draw ceiling bites when need and reservoir both exceed it:
	// 0/10000 blood, 100000 reservoir, ceiling 500 → draw 500
	p.giHandleBallInfo(ev, mustMarshal(t, &proto.GodIslandGameSpiritualBallInfoRespMsg{
		Ret: 0, CurPower: pb.String("0"), MaxPower: pb.String("10000"),
		BallCurPower: pb.String("100000"), BallMaxPower: pb.String("500"),
	}))
	healWant(500)
}

// /my_event_members status line: dead is reviveTime in the future (ms or s),
// leftout means kicked out, otherwise running with the cached city. Bad infos
// (ret != 0, missing fields) must not clobber the cache.
func TestGIStatus(t *testing.T) {
	p := &EventPlayer{}
	if got := p.GodIslandStatus(GIEventNormal); got != "⏹️ chưa login" {
		t.Fatalf("stopped: %q", got)
	}

	p.Running = true
	if got := p.GodIslandStatus(GIEventNormal); got != "⏳ chưa vào đảo" {
		t.Fatalf("no activity: %q", got)
	}
	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	if got := p.GodIslandStatus(GIEventNormal); got != "🏝️ đang chạy" {
		t.Fatalf("bare running: %q", got)
	}
	// The peak twin of the same account is still closed until its own push
	// arrives — its status line must stay at "chưa vào đảo".
	if got := p.GodIslandStatus(GIEventPeak); got != "⏳ chưa vào đảo" {
		t.Fatalf("closed peak: %q", got)
	}

	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{
		Ret: 0, MyRouteInfo: &proto.GodIslandGameRouteInfo{ToCityId: pb.Int32(3001)},
	}))
	if got := p.GodIslandStatus(GIEventNormal); got != "🏝️ đang hành quân tới thành 3001" {
		t.Fatalf("marching: %q", got)
	}

	now := time.Now()
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{
		Ret:        0,
		Location:   pb.Int32(2005),
		ReviveTime: pb.Int64(now.Add(2 * time.Minute).UnixMilli()),
	}))
	// countdown itself is racy by a second, only check its shape
	if got := p.GodIslandStatus(GIEventNormal); !strings.HasPrefix(got, "☠️") || !strings.Contains(got, "hồi sinh sau ") {
		t.Fatalf("dead(ms): %q", got)
	}

	// ret != 0 must not touch anything
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{
		Ret: 9, Location: pb.Int32(3000), Leftout: pb.Bool(true),
	}))
	if ev.Location != 2005 || ev.LeftOut {
		t.Fatalf("ret!=0 clobbered cache: %d/%v", ev.Location, ev.LeftOut)
	}

	// revive deadline passed → running again, city still known
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{
		Ret:        0,
		ReviveTime: pb.Int64(now.Add(-time.Minute).UnixMilli()),
	}))
	if got := p.GodIslandStatus(GIEventNormal); got != "🏝️ đang đóng ở thành 2005" {
		t.Fatalf("revived: %q", got)
	}

	// seconds-format reviveTime is normalized to ms
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{
		Ret:        0,
		ReviveTime: pb.Int64(now.Add(time.Hour).Unix()),
	}))
	if !strings.HasPrefix(p.GodIslandStatus(GIEventNormal), "☠️") {
		t.Fatalf("dead(s): %q", p.GodIslandStatus(GIEventNormal))
	}
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{Ret: 0, ReviveTime: pb.Int64(0)}))

	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{Ret: 0, Leftout: pb.Bool(true)}))
	if got := p.GodIslandStatus(GIEventNormal); got != "🚪 đã bị loại khỏi đảo" {
		t.Fatalf("leftout: %q", got)
	}

	// empty ret==0 info changes nothing
	p.giHandleGameInfo(ev, mustMarshal(t, &proto.GodIslandGameInfoRespMsg{Ret: 0}))
	if ev.Location != 2005 || !ev.LeftOut {
		t.Fatalf("empty info clobbered cache: %d/%v", ev.Location, ev.LeftOut)
	}
}

// mustMarshal encodes a proto message for feeding the event ws handlers.
func mustMarshal(t *testing.T, m pb.Message) []byte {
	t.Helper()
	data, err := pb.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// frameFuncType returns the 8-hex-char funcType of a pack.Pack frame.
func frameFuncType(t *testing.T, frame []byte) string {
	t.Helper()
	if len(frame) < 10 {
		t.Fatalf("frame too short: %x", frame)
	}
	return hex.EncodeToString(frame[6:10])
}

// wireCtx binds a live context to a bare test player so p.write enqueues:
// since the OOM round (#40) p.write silently drops frames when p.ctx is nil
// (production truth — a player without a ctx is not connected). Wire tests
// emulate a CONNECTED account, so they must carry one.
func wireCtx(t *testing.T, p *Player) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	t.Cleanup(cancel)
}

// wireCtxEP is wireCtx for event accounts (EventPlayer has its own ctx).
func wireCtxEP(t *testing.T, p *EventPlayer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	t.Cleanup(cancel)
}

// /bonglai_attack_thuve → move + auto-attack onlyNpc=true;
// /bonglai_attack → move + auto-attack onlyNpc=false (engages union players).
func TestGIAttackTo(t *testing.T) {
	p := &EventPlayer{}
	if err := p.GodIslandAttackTo(GIEventNormal, 2005, true); err == nil {
		t.Fatal("expected error for stopped account")
	}

	p.Running = true
	p.writeChan = make(chan []byte, 4)
	wireCtxEP(t, p)
	if err := p.GodIslandAttackTo(GIEventNormal, 2005, false); err == nil {
		t.Fatal("expected error without activity id")
	}

	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	for _, bad := range []int32{0, -5, giHomeCityID} {
		if err := p.GodIslandAttackTo(GIEventNormal, bad, true); err == nil {
			t.Fatalf("expected error for city %d", bad)
		}
	}
	// The island's own base city (per INFO_LOAD) is unattackable too — this
	// is the ONLY path that still refuses home (move allows it since
	// 2026-10-19).
	ev.Home = 6400
	if err := p.GodIslandAttackTo(GIEventNormal, 6400, false); err == nil {
		t.Fatal("expected error for the event's own home city")
	}
	if err := p.GodIslandAttackTo(GIEventNormal, giHomeCityID, false); err != nil {
		t.Fatalf("6001 must be targetable once the server says home is 6400: %v", err)
	}
	<-p.writeChan
	<-p.writeChan
	ev.Home = 0
	if len(p.writeChan) != 0 {
		t.Fatal("refused orders must not reach the socket")
	}

	for _, onlyNpc := range []bool{true, false} {
		if err := p.GodIslandAttackTo(GIEventNormal, 2005, onlyNpc); err != nil {
			t.Fatal(err)
		}
		if len(p.writeChan) != 2 {
			t.Fatalf("queued %d frames, want move + auto-attack", len(p.writeChan))
		}
		moveFrame, atkFrame := <-p.writeChan, <-p.writeChan
		if frameFuncType(t, moveFrame) != GOD_ISLAND_MOVE_CMD {
			t.Fatalf("first frame must be the move request, got %x", moveFrame)
		}
		if frameFuncType(t, atkFrame) != GOD_ISLAND_AUTO_ATK_CMD {
			t.Fatalf("second frame must be the auto-attack request, got %x", atkFrame)
		}
		var atk proto.GodIslandGameAutoAttackReqMsg
		if err := pb.Unmarshal(atkFrame[18:], &atk); err != nil {
			t.Fatalf("bad auto-attack payload: %v", err)
		}
		if atk.GetActivityId() != 42 || atk.GetTargetId() != 2005 {
			t.Fatalf("attack %d/%d, want 42/2005", atk.GetActivityId(), atk.GetTargetId())
		}
		if atk.GetOnlyNpc() != onlyNpc {
			t.Fatalf("onlyNpc=%v, want %v", atk.GetOnlyNpc(), onlyNpc)
		}
	}
}

// Plant garden upkeep: config discovery, per-notice liquid collection,
// one-shot fruit claim, and donation of each configured liquid item. Payload
// shapes are live-verified (TestLiveGodIslandPlant).
func TestGIPlantGarden(t *testing.T) {
	p := &EventPlayer{Running: true}
	p.writeChan = make(chan []byte, 16)
	wireCtxEP(t, p)
	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42

	wantFrame := func(cmd string) *[]byte {
		t.Helper()
		select {
		case frame := <-p.writeChan:
			if got := frameFuncType(t, frame); got != cmd {
				t.Fatalf("frame %s, want %s", got, cmd)
			}
			return &frame
		default:
			t.Fatalf("no frame on writeChan, want %s", cmd)
			return nil
		}
	}
	noFrame := func() {
		t.Helper()
		if len(p.writeChan) != 0 {
			t.Fatalf("%d unexpected frames", len(p.writeChan))
		}
	}

	// --- cycle 1: liquidIds unknown → plant info + one config fetch ---
	p.giPlantCycle(ev)
	frame := wantFrame(GOD_ISLAND_PLANT_INFO_CMD)
	var preq proto.GodIslandGamePlantInfoReqMsg
	if err := pb.Unmarshal((*frame)[18:], &preq); err != nil || preq.GetActivityId() != 42 {
		t.Fatalf("plant info req %+v err %v", &preq, err)
	}
	frame = wantFrame(ACTIVITY_GET_DATA_CMD)
	var dreq proto.ReqGetActivityDetail
	if err := pb.Unmarshal((*frame)[18:], &dreq); err != nil || dreq.GetActivityId() != 42 {
		t.Fatalf("activity detail req %+v err %v", &dreq, err)
	}
	noFrame()

	// config answer with the live-observed energy list
	p.giHandleActivityDetail(ev, mustMarshal(t, &proto.RspGetActivityDetail{
		Ret: 0,
		Activity: &proto.ActivityCommonData{
			ActivityId: pb.Int32(42),
			DetailConfig: &proto.ActivityDetailConfig{
				CommonConfig: &proto.ActivityCommonConfig{
					ParamConfig: []*proto.ActivityParamConfig{
						{Param: pb.String("GODISLAND_BATTLE_TIME"), Value: pb.String("300")},
						{Param: pb.String("GODISLAND_ITEM_ENERGY"), Value: pb.String("105041|5;105042|10;105043|20")},
					},
				},
			},
		},
	}))
	p.GodIslandMu.Lock()
	ids := append([]int32(nil), ev.LiquidIDs...)
	p.GodIslandMu.Unlock()
	if len(ids) != 3 || ids[0] != 105041 || ids[2] != 105043 {
		t.Fatalf("liquid ids %v", ids)
	}

	// --- plant info: only status==0 notices with a real id get watered ---
	p.giHandlePlantInfo(ev, mustMarshal(t, &proto.GodIslandGamePlantInfoRespMsg{
		Ret: 0, Lv: pb.Int32(16), CanReceive: pb.Int32(0), WaterCount: pb.Int32(6),
		NoticeInfo: []*proto.GodIslandGamePlantNoticeInfo{
			{Id: pb.Int64(7), LiquidId: pb.Int32(105041), Status: pb.Int32(0)},
			{Id: pb.Int64(8), LiquidId: pb.Int32(105042), Status: pb.Int32(1)},
			{Id: pb.Int64(0), LiquidId: pb.Int32(105041), Status: pb.Int32(0)},
			{Id: pb.Int64(9), LiquidId: pb.Int32(105043), Status: pb.Int32(0)},
		},
	}))
	for _, want := range []int64{7, 9} {
		frame = wantFrame(GOD_ISLAND_PLANT_WATER_CMD)
		var wreq proto.GodIslandGamePlantWaterReqMsg
		if err := pb.Unmarshal((*frame)[18:], &wreq); err != nil {
			t.Fatal(err)
		}
		if wreq.GetActivityId() != 42 || wreq.GetId() != want {
			t.Fatalf("water %d/%d, want 42/%d", wreq.GetActivityId(), wreq.GetId(), want)
		}
	}
	noFrame()

	// --- water response ripens the garden: claim exactly once ---
	p.giHandlePlantWater(ev, mustMarshal(t, &proto.GodIslandGamePlantWaterRespMsg{
		Ret: 0, Reward: pb.String("100010=1"), CanReceive: pb.Int32(1), Lv: pb.Int32(16),
	}))
	wantFrame(GOD_ISLAND_RECEIVE_FRUITS_CMD)
	p.giHandlePlantWater(ev, mustMarshal(t, &proto.GodIslandGamePlantWaterRespMsg{
		Ret: 0, CanReceive: pb.Int32(1),
	}))
	noFrame() // still ripe, must not re-claim
	p.giHandleReceiveFruits(ev, mustMarshal(t, &proto.GodIslandGamePlantReceiveFruitsRespMsg{
		Ret: 0, Reward: pb.String("105039=201"), Lv: pb.Int32(16), CanReceive: pb.Int32(0),
	}))
	noFrame()

	// refused responses must not trigger anything
	p.giHandlePlantWater(ev, mustMarshal(t, &proto.GodIslandGamePlantWaterRespMsg{Ret: 9}))
	p.giHandlePlantInfo(ev, mustMarshal(t, &proto.GodIslandGamePlantInfoRespMsg{Ret: 9}))
	noFrame()

	// --- cycle 2: ids cached → plant info + one donation of each item ---
	p.giPlantCycle(ev)
	wantFrame(GOD_ISLAND_PLANT_INFO_CMD)
	for _, lid := range []int32{105041, 105042, 105043} {
		frame = wantFrame(GOD_ISLAND_SEND_LIQUID_CMD)
		var sreq proto.GodIslandGameSendLiquidReqMsg
		if err := pb.Unmarshal((*frame)[18:], &sreq); err != nil {
			t.Fatal(err)
		}
		if sreq.GetActivityId() != 42 || sreq.GetLiquidId() != lid || sreq.GetSendNum() != 1 {
			t.Fatalf("send liquid %+v, want 42/%d/1", &sreq, lid)
		}
	}
	noFrame()
}

// GODISLAND_ITEM_ENERGY parses exactly like the client (106937): pairs split
// on ';' then '|', malformed entries dropped.
func TestGIParseLiquidIDs(t *testing.T) {
	got := giParseLiquidIDs("105041|5;105042|10;105043|20")
	if len(got) != 3 || got[0] != 105041 || got[1] != 105042 || got[2] != 105043 {
		t.Fatalf("got %v", got)
	}
	// "9|" splits to ["9",""] — 2 parts, so the client (106937) accepts the id
	// too; ids must be positive, the energy value is not used by the bot.
	if got := giParseLiquidIDs("abc;1|2|3;0|5;-7|2"); len(got) != 0 {
		t.Fatalf("garbage accepted: %v", got)
	}
	if got := giParseLiquidIDs("9|"); len(got) != 1 || got[0] != 9 {
		t.Fatalf(`"9|" rejected: %v`, got)
	}
	if got := giParseLiquidIDs(""); len(got) != 0 {
		t.Fatalf("empty accepted: %v", got)
	}
}

// Bồng Lai and Thiên Ma run as two independent events on ONE connection: the
// activity push must capture both variants, responses (which carry no
// activityId) must land on the event that asked for them via the per-type
// FIFO, ambiguous unrequested frames must be dropped, and self-describing
// pushes must attribute by their own activityId.
func TestGIDualEvents(t *testing.T) {
	p := &EventPlayer{Running: true}
	p.writeChan = make(chan []byte, 16)
	wireCtxEP(t, p)

	// --- activity push captures BOTH variants (68 and 333) at once ---
	p.giHandleActivityList(mustMarshal(t, &proto.PushActivityList{
		MainConfig: []*proto.ActivityMainConfig{
			{ActivityId: pb.Int32(111), Type: pb.Int32(giActivityTypeGodIsland)},
			{ActivityId: pb.Int32(999), Type: pb.Int32(7)}, // unrelated activity
			{ActivityId: pb.Int32(222), Type: pb.Int32(giActivityTypePeakIsland)},
		},
	}))
	evN := p.GiEvents[GIEventNormal]
	evP := p.GiEvents[GIEventPeak]
	if evN == nil || evN.ActivityID != 111 {
		t.Fatalf("normal slot %+v, want activity 111", evN)
	}
	if evP == nil || evP.ActivityID != 222 {
		t.Fatalf("peak slot %+v, want activity 222", evP)
	}
	if !p.GodIslandEventOpen(GIEventNormal) || !p.GodIslandEventOpen(GIEventPeak) {
		t.Fatal("both events must report open")
	}

	// --- FIFO: two INFO_LOAD requests (normal then peak) — wire order and
	// queue order match, so the two answers must land 1:1 in order ---
	p.giSend(evN, GOD_ISLAND_INFO_LOAD_CMD, SERVER_GOD_ISLAND_INFO_LOAD,
		&proto.GodIslandGameInfoReqMsg{ActivityId: 111})
	p.giSend(evP, GOD_ISLAND_INFO_LOAD_CMD, SERVER_GOD_ISLAND_INFO_LOAD,
		&proto.GodIslandGameInfoReqMsg{ActivityId: 222})
	fN, fP := <-p.writeChan, <-p.writeChan
	if frameFuncType(t, fN) != GOD_ISLAND_INFO_LOAD_CMD || frameFuncType(t, fP) != GOD_ISLAND_INFO_LOAD_CMD {
		t.Fatalf("expected two INFO_LOAD frames, got %x %x", fN, fP)
	}
	var rN, rP proto.GodIslandGameInfoReqMsg
	if err := pb.Unmarshal(fN[18:], &rN); err != nil || rN.GetActivityId() != 111 {
		t.Fatalf("first request %+v err %v, want activity 111", &rN, err)
	}
	if err := pb.Unmarshal(fP[18:], &rP); err != nil || rP.GetActivityId() != 222 {
		t.Fatalf("second request %+v err %v, want activity 222", &rP, err)
	}
	p.giRoute(SERVER_GOD_ISLAND_INFO_LOAD, mustMarshal(t,
		&proto.GodIslandGameInfoRespMsg{Ret: 0, Location: pb.Int32(2005)}),
		(*EventPlayer).giHandleGameInfo)
	p.giRoute(SERVER_GOD_ISLAND_INFO_LOAD, mustMarshal(t,
		&proto.GodIslandGameInfoRespMsg{Ret: 0, Location: pb.Int32(3005)}),
		(*EventPlayer).giHandleGameInfo)
	if evN.Location != 2005 || evP.Location != 3005 {
		t.Fatalf("FIFO misroute: normal=%d peak=%d, want 2005/3005", evN.Location, evP.Location)
	}

	// --- both open, empty queue: an unrequested INFO_LOAD is ambiguous and
	// must be dropped rather than guessed ---
	p.giRoute(SERVER_GOD_ISLAND_INFO_LOAD, mustMarshal(t,
		&proto.GodIslandGameInfoRespMsg{Ret: 0, Location: pb.Int32(9999)}),
		(*EventPlayer).giHandleGameInfo)
	if evN.Location != 2005 || evP.Location != 3005 {
		t.Fatalf("ambiguous frame applied: normal=%d peak=%d", evN.Location, evP.Location)
	}

	// exactly one open event → the fallback may attribute it
	p2 := &EventPlayer{Running: true}
	p2.giHandleActivityList(mustMarshal(t, &proto.PushActivityList{
		MainConfig: []*proto.ActivityMainConfig{
			{ActivityId: pb.Int32(111), Type: pb.Int32(giActivityTypeGodIsland)},
		},
	}))
	p2.giRoute(SERVER_GOD_ISLAND_INFO_LOAD, mustMarshal(t,
		&proto.GodIslandGameInfoRespMsg{Ret: 0, Location: pb.Int32(4242)}),
		(*EventPlayer).giHandleGameInfo)
	if ev := p2.GiEvents[GIEventNormal]; ev == nil || ev.Location != 4242 {
		t.Fatalf("single-open fallback did not attribute: %+v", ev)
	}

	// --- pushes self-attribute by their required activityId ---
	p.giHandleMyGameInfoSync(mustMarshal(t, &proto.GodIslandGameMyGameInfoSyncMsg{
		ActivityId: 222, Location: pb.Int32(3010),
	}))
	if evP.Location != 3010 || evN.Location != 2005 {
		t.Fatalf("push misroute: peak=%d normal=%d, want 3010/2005", evP.Location, evN.Location)
	}
	// foreign activityId touches neither event
	p.giHandleMyGameInfoSync(mustMarshal(t, &proto.GodIslandGameMyGameInfoSyncMsg{
		ActivityId: 333, Location: pb.Int32(1),
	}))
	if evP.Location != 3010 || evN.Location != 2005 {
		t.Fatalf("foreign push applied: peak=%d normal=%d", evP.Location, evN.Location)
	}

	// --- LEFT_OUT is account-wide by union, so only the frame's own
	// activityId decides WHICH island we left ---
	evN.Union, evP.Union = 777, 777
	p.giHandleLeftOut(mustMarshal(t, &proto.GodIslandLeftOutSyncInfo{ActivityId: 222, UnionId: 777}))
	if !evP.LeftOut || evN.LeftOut {
		t.Fatalf("left-out misroute: peak=%v normal=%v, want true/false", evP.LeftOut, evN.LeftOut)
	}
}

// Server event pushes: cache updates between polls + leader notifications,
// all in game language (no protocol details must reach the notification text).
func TestGISyncPushes(t *testing.T) {
	p := &EventPlayer{Running: true, Notify: true}
	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	ev.Union = 777
	p.NotiChan = make(chan NotifyMGS, 8)
	p.writeChan = make(chan []byte, 8)
	wireCtxEP(t, p)

	wantNotify := func(needle string) {
		t.Helper()
		select {
		case n := <-p.NotiChan:
			if !strings.Contains(n.MSG, needle) {
				t.Fatalf("notify %q, want substring %q", n.MSG, needle)
			}
		default:
			t.Fatalf("no notification, want substring %q", needle)
		}
	}
	noNotify := func() {
		t.Helper()
		if len(p.NotiChan) != 0 {
			t.Fatalf("%d unexpected notifications", len(p.NotiChan))
		}
	}

	// The notification must name the island so a leader running both
	// variants knows which one it refers to — game language only.
	if label := giEventLabel(GIEventPeak); label != "Thiên Ma" {
		t.Fatalf("peak label %q", label)
	}

	// MYGAMEINFO_SYNC updates location + HP without waiting for a poll.
	ev.Moving = true
	p.giHandleMyGameInfoSync(mustMarshal(t, &proto.GodIslandGameMyGameInfoSyncMsg{
		ActivityId: 42, Location: pb.Int32(2005),
		CurPower: pb.String("100"), MaxPower: pb.String("200"),
	}))
	p.GodIslandMu.Lock()
	loc, cur, max := ev.Location, ev.PowerCur, ev.PowerMax
	moving := ev.Moving
	p.GodIslandMu.Unlock()
	if loc != 2005 || cur != 100 || max != 200 {
		t.Fatalf("sync caches loc=%d hp=%d/%d", loc, cur, max)
	}
	if !moving {
		t.Fatal("mygameinfo sync must not touch march state (no route info in it)")
	}

	// foreign activity id: ignored entirely
	p.giHandleMyGameInfoSync(mustMarshal(t, &proto.GodIslandGameMyGameInfoSyncMsg{
		ActivityId: 99, Location: pb.Int32(1),
	}))
	p.GodIslandMu.Lock()
	loc = ev.Location
	p.GodIslandMu.Unlock()
	if loc != 2005 {
		t.Fatalf("foreign sync changed location to %d", loc)
	}

	// BEEN_KILL_SYNC: dead state + exact revive countdown + one notification.
	revive := time.Now().Add(2 * time.Minute).UnixMilli()
	p.giHandleBeenKillSync(mustMarshal(t, &proto.GodIslandBeenKillSyncMsg{
		ActivityId: 42, CityId: pb.Int32(2005), KillByNPc: pb.Bool(true),
		ReviveTime: pb.Int64(revive),
	}))
	p.GodIslandMu.Lock()
	loc, moving = ev.Location, ev.Moving
	rt := ev.ReviveTime
	p.GodIslandMu.Unlock()
	if loc != 0 || moving || rt != revive {
		t.Fatalf("been-kill state loc=%d moving=%v revive=%d", loc, moving, rt)
	}
	select {
	case n := <-p.NotiChan:
		for _, needle := range []string{"☠️", "Bồng Lai", "hồi sinh sau"} {
			if !strings.Contains(n.MSG, needle) {
				t.Fatalf("notify %q, want substring %q", n.MSG, needle)
			}
		}
	default:
		t.Fatal("no death notification")
	}
	noNotify()

	// AUTO_BATTLE_STOP_SYNC: one notification.
	p.giHandleAutoBattleStop(mustMarshal(t, &proto.GodIslandAutoBattleStopSyncMsg{ActivityId: 42}))
	wantNotify("tự động tấn công đã dừng")

	// LEFT_OUT_SYNC: other unions are silent; ours clears state and notifies.
	p.giHandleLeftOut(mustMarshal(t, &proto.GodIslandLeftOutSyncInfo{ActivityId: 42, UnionId: 12345}))
	p.GodIslandMu.Lock()
	leftOut := ev.LeftOut
	p.GodIslandMu.Unlock()
	if leftOut {
		t.Fatal("foreign union's left-out marked us as left out")
	}
	noNotify()
	p.giHandleLeftOut(mustMarshal(t, &proto.GodIslandLeftOutSyncInfo{ActivityId: 42, UnionId: 777}))
	p.GodIslandMu.Lock()
	leftOut = ev.LeftOut
	p.GodIslandMu.Unlock()
	if !leftOut {
		t.Fatal("own left-out not recorded")
	}
	wantNotify("rời khỏi đảo")
	noNotify()
}

// TestGIShopSweep pins the God Island shop auto-buy (owner 2026-10-19):
// one 21003 ask while the table is unknown, then per sweep exactly ONE unit
// of every gate-passing row (free / ad-watch / tiên ngọc) up to its daily
// cap — the money row is NEVER sent. Answers (1005 carries no mallId)
// attribute through the MallPending FIFO in send order.
func TestGIShopSweep(t *testing.T) {
	p := &EventPlayer{Running: true}
	p.writeChan = make(chan []byte, 32)
	wireCtxEP(t, p)
	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42

	wantFrame := func(cmd string) *[]byte {
		t.Helper()
		select {
		case frame := <-p.writeChan:
			if got := frameFuncType(t, frame); got != cmd {
				t.Fatalf("frame %s, want %s", got, cmd)
			}
			return &frame
		default:
			t.Fatalf("no frame on writeChan, want %s", cmd)
			return nil
		}
	}
	noFrame := func() {
		t.Helper()
		if len(p.writeChan) != 0 {
			t.Fatalf("%d unexpected frames", len(p.writeChan))
		}
	}

	// --- shop table unknown → exactly one 21003 ask, then throttle ---
	p.giShopSweep(ev)
	frame := wantFrame(ACTIVITY_GET_DATA_CMD)
	var dreq proto.ReqGetActivityDetail
	if err := pb.Unmarshal((*frame)[18:], &dreq); err != nil || dreq.GetActivityId() != 42 {
		t.Fatalf("detail req %+v err %v", &dreq, err)
	}
	noFrame()
	p.giShopSweep(ev) // inside the throttle window
	noFrame()

	// --- fold a live-shaped table: free + ad + jade + ONE money row ---
	p.giHandleActivityDetail(ev, mustMarshal(t, &proto.RspGetActivityDetail{
		Ret: 0,
		Activity: &proto.ActivityCommonData{
			ActivityId:       pb.Int32(42),
			MallBuyCountList: []*proto.ActivityMallBuyCountData{{MallId: pb.Int32(1), Count: pb.Int64(1)}},
			DetailConfig: &proto.ActivityDetailConfig{
				CommonConfig: &proto.ActivityCommonConfig{
					MallConfig: []*proto.ActivityMallConfig{
						{MallTempMsg: &proto.ActivityMallTempMsg{Id: pb.Int32(1), Name: pb.String("Phúc Lợi Miễn Phí"), Price: pb.String("0"), BuyLimit: pb.Int32(1)}},
						{MallTempMsg: &proto.ActivityMallTempMsg{Id: pb.Int32(2), Name: pb.String("Gói Video Cổ Vũ"), Price: pb.String("0"), Param: pb.String("3|1800|0|0"), BuyLimit: pb.Int32(2)}},
						{MallTempMsg: &proto.ActivityMallTempMsg{Id: pb.Int32(3), Name: pb.String("Gói Tiên Ngọc"), Price: pb.String("100000=TienNgoc"), BuyLimit: pb.Int32(2)}},
						{MallTempMsg: &proto.ActivityMallTempMsg{Id: pb.Int32(4), Name: pb.String("Gói Nạp 128k"), Price: pb.String("12800"), Param: pb.String("1|"), BuyLimit: pb.Int32(5)}},
					},
				},
			},
		},
	}))
	p.GodIslandMu.Lock()
	if len(ev.MallRows) != 4 || ev.MallBought[1] != 1 {
		p.GodIslandMu.Unlock()
		t.Fatalf("fold: %d rows, bought %v", len(ev.MallRows), ev.MallBought)
	}
	ev.ShopLast = time.Time{} // release the throttle
	p.GodIslandMu.Unlock()

	// --- sweep: row 1 already bought server-side, row 4 is MONEY → only 2+3 ---
	p.giShopSweep(ev)
	seen := map[int32]int{}
	for i := 0; i < 2; i++ {
		frame := wantFrame(ACTIVITY_BUY_GOODS_CMD)
		var breq proto.ReqBuyActivityGoods
		if err := pb.Unmarshal((*frame)[18:], &breq); err != nil {
			t.Fatal(err)
		}
		if breq.GetActivityId() != 42 || breq.GetCount() != 1 || breq.GetIsUseADTime() {
			t.Fatalf("buy req %+v, want 42/*/1 no-AD", &breq)
		}
		seen[breq.GetMallId()]++
	}
	noFrame()
	if len(seen) != 2 || seen[2] != 1 || seen[3] != 1 {
		t.Fatalf("bought %v — want exactly {2:1, 3:1} (money row 4 forbidden)", seen)
	}
	p.GodIslandMu.Lock()
	pending := append([]int32(nil), ev.MallPending...)
	p.GodIslandMu.Unlock()
	if len(pending) != 2 || pending[0] != 2 || pending[1] != 3 {
		t.Fatalf("pending queue %v, want [2 3]", pending)
	}

	// --- answers attribute in send order; refusal does not count ---
	p.Notify = true
	p.NotiChan = make(chan NotifyMGS, 8)
	if !p.giDeliverBuyGoods(mustMarshal(t, &proto.RspBuyActivityGoods{Ret: 0, Rewards: pb.String("105039=5")})) {
		t.Fatal("buy answer dropped — queue said [2 3]")
	}
	p.GodIslandMu.Lock()
	adds2 := ev.MallAdds[2]
	p.GodIslandMu.Unlock()
	if adds2 != 1 {
		t.Fatalf("MallAdds[2]=%d after success, want 1", adds2)
	}
	select {
	case n := <-p.NotiChan:
		if !strings.Contains(n.MSG, "gói xem quảng cáo") || !strings.Contains(n.MSG, "Gói Video Cổ Vũ") {
			t.Fatalf("notify %q", n.MSG)
		}
	default:
		t.Fatal("success was not notified")
	}
	if !p.giDeliverBuyGoods(mustMarshal(t, &proto.RspBuyActivityGoods{Ret: 9})) {
		t.Fatal("refusal dropped instead of consumed")
	}
	p.GodIslandMu.Lock()
	adds3 := ev.MallAdds[3]
	p.GodIslandMu.Unlock()
	if adds3 != 0 {
		t.Fatalf("MallAdds[3]=%d after refusal, want 0", adds3)
	}
	if p.giDeliverBuyGoods(mustMarshal(t, &proto.RspBuyActivityGoods{Ret: 0})) {
		t.Fatal("empty queue must hand the frame to Luân Hồi (false)")
	}

	// --- caps saturated → nothing on the wire ---
	p.GodIslandMu.Lock()
	ev.MallAdds[2], ev.MallAdds[3] = 2, 2
	ev.ShopLast = time.Time{}
	p.GodIslandMu.Unlock()
	p.giShopSweep(ev)
	noFrame()

	// --- ICT day flip resets the LOCAL adds; the server count still caps row 1 ---
	p.GodIslandMu.Lock()
	ev.MallDay = "2000-01-01"
	ev.ShopLast = time.Time{}
	p.GodIslandMu.Unlock()
	p.giShopSweep(ev)
	seen = map[int32]int{}
	for i := 0; i < 2; i++ {
		frame := wantFrame(ACTIVITY_BUY_GOODS_CMD)
		var breq proto.ReqBuyActivityGoods
		if err := pb.Unmarshal((*frame)[18:], &breq); err != nil {
			t.Fatal(err)
		}
		seen[breq.GetMallId()]++
	}
	noFrame()
	if len(seen) != 2 || seen[2] != 1 || seen[3] != 1 {
		t.Fatalf("after day flip bought %v — want {2:1, 3:1}, row 1 capped by server count", seen)
	}
}

// [[tytd-item-ban]]: giShopSweep is the ONLY place in player_event.go that
// may reference the 21005 send constant — no other God Island code path can
// put a purchase on the wire, so a recharge row has no sender at all.
func TestGIShopBanSourceScan(t *testing.T) {
	src, err := os.ReadFile("player_event.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range strings.Split(string(src), "\nfunc ") {
		if !strings.Contains(chunk, "ACTIVITY_BUY_GOODS_CMD") {
			continue
		}
		header := chunk
		if i := strings.Index(header, "\n"); i >= 0 {
			header = header[:i] // just the func signature line
		}
		// the split consumed the "func " prefix — match the signature tail
		if !strings.Contains(header, "giShopSweep(") {
			t.Errorf("ACTIVITY_BUY_GOODS_CMD used outside giShopSweep: %s", header)
		}
	}
}
