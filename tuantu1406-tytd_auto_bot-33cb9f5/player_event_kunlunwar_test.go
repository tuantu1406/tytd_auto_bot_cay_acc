package tytd_go

// /kunlun_follow event-side transport: the auto EnterGame seat, the Move
// that follows it (exact target + activityId on the wire), the no-op when
// already at the leader's block, and the dead-account refusal.

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func kuOpenEventPlayer(t *testing.T) (*EventPlayer, chan []byte) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := &EventPlayer{Username: "ev1", UID: 4242, Running: true,
		ctx: ctx, cancel: cancel, writeChan: make(chan []byte, 32)}
	now := time.Now()
	full, err := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{{
		ActivityId:    pb.Int32(777),
		Type:          pb.Int32(activityTypeKunlunWar),
		BeginShowTime: pb.Int64(now.Add(-2 * time.Hour).UnixMilli()),
		BeginTime:     pb.Int64(now.Add(-time.Hour).UnixMilli()),
		EndTime:       pb.Int64(now.Add(time.Hour).UnixMilli()),
		EndShowTime:   pb.Int64(now.Add(2 * time.Hour).UnixMilli()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	p.ActList.applyList("ev1", full, true)
	if !p.KunlunWarEventOpen() {
		t.Fatal("event window not seen")
	}
	frames := make(chan []byte, 16)
	go func() {
		for f := range p.writeChan {
			select {
			case frames <- f:
			default:
			}
		}
	}()
	return p, frames
}

func kuNextFrame(t *testing.T, frames chan []byte) (string, []byte) {
	t.Helper()
	select {
	case f := <-frames:
		isMSG, ty, body := pack.Unpack(f)
		if !isMSG {
			t.Fatalf("not a MSG frame: %x", f[:12])
		}
		return ty, body
	case <-time.After(3 * time.Second):
		t.Fatal("no frame sent")
		return "", nil
	}
}

func kuNoFrame(t *testing.T, frames chan []byte) {
	t.Helper()
	select {
	case f := <-frames:
		_, ty, _ := pack.Unpack(f)
		t.Fatalf("unexpected frame %s", ty)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestKuEventFollowEntersThenMoves(t *testing.T) {
	p, frames := kuOpenEventPlayer(t)
	const target = int32(32*61 + 32)
	done := make(chan error, 1)
	go func() { done <- p.KunlunWarMoveTo(target) }()

	ty, body := kuNextFrame(t, frames)
	if ty != KUNLUN_ENTER_GAME_CMD[4:] { // Unpack reports the low 16 bits ("5afe")
		t.Fatalf("first frame %s, want EnterGame", ty)
	}
	var ereq proto.KunlunWarEnterGameReq
	if pb.Unmarshal(body, &ereq) != nil || ereq.GetActivityId() != 777 {
		t.Fatalf("EnterGame body %+v", &ereq)
	}
	// Server answers: scene accepted, standing elsewhere (leader not here).
	resp, _ := pb.Marshal(&proto.KunlunWarEnterGameResp{
		Ret: pb.Int32(0), Home: pb.Int32(6001),
		MyPlayerInfo: &proto.KunlunWarPlayerInfo{Pos: pb.Int32(6*61 + 1)},
	})
	p.kuHandleEnterGame(resp)

	ty, body = kuNextFrame(t, frames)
	if ty != KUNLUN_MOVE_CMD[4:] {
		t.Fatalf("second frame %s, want Move", ty)
	}
	var mreq proto.KunlunWarMoveReq
	if pb.Unmarshal(body, &mreq) != nil || mreq.GetTargetPoint() != target || mreq.GetActivityId() != 777 {
		t.Fatalf("Move body %+v", &mreq)
	}
	if err := <-done; err != nil {
		t.Fatalf("order returned %v", err)
	}
	// Accepted march logs the arrival math without error.
	ok, _ := pb.Marshal(&proto.KunlunWarMoveResp{Ret: pb.Int32(0), RouteInfo: &proto.KunlunWarGameRouteInfo{
		ToPoint: pb.Int32(target), Time: pb.Int64(1000), Arrival: pb.Int64(9000),
	}})
	p.kuHandleMove(ok)
	bad, _ := pb.Marshal(&proto.KunlunWarMoveResp{Ret: pb.Int32(19944)})
	p.kuHandleMove(bad) // logged only

	// After the march arrives, a second follow to the SAME block is a
	// silent no-op (already there).
	arr, _ := pb.Marshal(&proto.KunlunWarMyGameInfoSyncMsg{Location: pb.Int32(target)})
	p.kuHandleMyGameInfo(arr)
	if err := p.KunlunWarMoveTo(target); err != nil {
		t.Fatalf("re-follow: %v", err)
	}
	kuNoFrame(t, frames)
}

func TestKuEventDeadAccountRefused(t *testing.T) {
	p, frames := kuOpenEventPlayer(t)
	// Join scene standing far from the (future) leader block.
	done := make(chan error, 1)
	go func() { done <- p.KunlunWarMoveTo(6*61 + 1) }()
	kuNextFrame(t, frames) // EnterGame
	resp, _ := pb.Marshal(&proto.KunlunWarEnterGameResp{Ret: pb.Int32(0), MyPlayerInfo: &proto.KunlunWarPlayerInfo{Pos: pb.Int32(40*61 + 20)}})
	p.kuHandleEnterGame(resp)
	kuNextFrame(t, frames) // Move to same block (first order → joined far)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Now die (219933 location 0) and re-order elsewhere.
	loc, _ := pb.Marshal(&proto.KunlunWarMyGameInfoSyncMsg{Location: pb.Int32(0)})
	p.kuHandleMyGameInfo(loc)
	err := p.KunlunWarMoveTo(32*61 + 32)
	if err == nil || !strings.Contains(err.Error(), "chết") {
		t.Fatalf("dead account order = %v, want death refusal", err)
	}
	kuNoFrame(t, frames)
}

func TestKuEventClosedWindow(t *testing.T) {
	p := &EventPlayer{Username: "ev0", Running: true,
		ctx: context.Background(), writeChan: make(chan []byte, 1)}
	if p.KunlunWarEventOpen() {
		t.Fatal("empty list must be closed")
	}
	if err := p.KunlunWarMoveTo(1234); err != errKuClosed {
		t.Fatalf("closed window = %v, want errKuClosed", err)
	}
}

func TestKuEventStatusLines(t *testing.T) {
	ep := &EventPlayer{}
	if got := ep.KunlunStatus(); got != "⏹️ chưa login" {
		t.Fatalf("offline: %q", got)
	}
	ep.Running = true
	ep.ku = &kuEventState{}
	if got := ep.KunlunStatus(); !strings.Contains(got, "chưa mở") {
		t.Fatalf("no activity: %q", got)
	}
	ep.ku.ActivityID = 5
	if got := ep.KunlunStatus(); !strings.Contains(got, "ngoài map") {
		t.Fatalf("outside scene: %q", got)
	}
	ep.ku.SceneIn = true
	ep.ku.Point = 395
	ep.ku.Energy, ep.ku.HomeCity = 2, 6007
	got := ep.KunlunStatus()
	// 395 = y*61+x → (29,6) — the same projection the follow message uses.
	if !strings.Contains(got, "ô 395 (29,6)") || !strings.Contains(got, "NL 2") {
		t.Fatalf("in scene: %q", got)
	}
	ep.ku.Point = 0 // client beenKill convention
	if got := ep.KunlunStatus(); !strings.Contains(got, "chết") {
		t.Fatalf("dead: %q", got)
	}
}

// The event transport can serve the whole mapstream tick: EnterActivity to
// learn floor+group, then 219919 carrying BOTH (the empty-board lesson),
// cached on the next pull. Answers arrive as push frames through kuDeliver,
// exactly like the /kunlun_follow EnterGame seat.
func TestKuEventMiniMapPull(t *testing.T) {
	p, frames := kuOpenEventPlayer(t)
	enter, _ := pb.Marshal(&proto.KunlunWarEnterActivityResp{
		Ret: pb.Int32(0), Floor: pb.Int32(10000), Group: pb.Int32(3),
	})
	mini, _ := pb.Marshal(&proto.KunlunWarMiniMapInfoRespMsg{
		BaseData: []*proto.KunlunUnionBaseData{{UnionId: pb.Int64(1), Name: pb.String("X")}},
	})
	done := make(chan error, 1)
	go func() {
		b, err := p.RequestKunlunMiniMap(2 * time.Second)
		if err == nil && !bytes.Equal(b, mini) {
			err = fmt.Errorf("payload not delivered through the seat")
		}
		done <- err
	}()
	f := <-frames
	if ty := hex.EncodeToString(f[6:10]); ty != KUNLUN_ENTER_ACTIVITY_CMD {
		t.Fatalf("first frame cmd %s — want EnterActivity %s", ty, KUNLUN_ENTER_ACTIVITY_CMD)
	}
	p.kuDeliverFrame(SERVER_KUNLUN_ENTER_ACTIVITY, enter)
	f = <-frames
	if ty := hex.EncodeToString(f[6:10]); ty != KUNLUN_MINI_MAP_INFO_CMD {
		t.Fatalf("second frame cmd %s — want MiniMap %s", ty, KUNLUN_MINI_MAP_INFO_CMD)
	}
	var req proto.KunlunWarMiniMapInfoReqMsg
	if err := pb.Unmarshal(f[18:], &req); err != nil {
		t.Fatalf("minimap req: %v", err)
	}
	if req.GetFloor() != 10000 || req.GetGroup() != 3 {
		t.Fatalf("floor=%d group=%d — the EMPTY-board lesson must not regress", req.GetFloor(), req.GetGroup())
	}
	p.kuDeliverFrame(SERVER_KUNLUN_MINI_MAP_INFO, mini)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Second pull: roster cached → no EnterActivity frame, straight minimap.
	go func() {
		_, err := p.RequestKunlunMiniMap(2 * time.Second)
		done <- err
	}()
	f = <-frames
	if ty := hex.EncodeToString(f[6:10]); ty != KUNLUN_MINI_MAP_INFO_CMD {
		t.Fatalf("cached pull first frame %s — want straight minimap", ty)
	}
	p.kuDeliverFrame(SERVER_KUNLUN_MINI_MAP_INFO, mini)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// ---- revive (BackHome 219915) ---------------------------------------------

// Auto rebirth must fire the FREE request only, on the backoff, and stop when
// the army returns. With KUNLUNWAR_FREE_REVIVE_CD never reaching the bot, an
// early refusal is by-design and must not clear the death stamp.
func TestKuAutoRebirthFreeProbe(t *testing.T) {
	p, frames := kuOpenEventPlayer(t)
	// Dead in-scene with an old death stamp → the next check fires.
	p.ku = &kuEventState{SceneIn: true, ReviveAt: time.Now().Add(-time.Hour).UnixMilli()}
	now := time.Now()
	p.kuTryAutoRebirth(now)
	ty, body := kuNextFrame(t, frames)
	if ty != KUNLUN_BACK_HOME_CMD[4:] {
		t.Fatalf("probe frame %s, want BackHome", ty)
	}
	var req proto.KunlunWarBackHomeReq
	if pb.Unmarshal(body, &req) != nil || req.GetActivityId() != 777 || req.GetUseItem() {
		t.Fatalf("probe body %+v — want activityId=777, useItem=false (item ban)", &req)
	}
	// Backoff: a second check a second later must not double-send.
	p.kuTryAutoRebirth(now.Add(time.Second))
	kuNoFrame(t, frames)
	if p.ku.rebirthTries != 1 {
		t.Fatalf("tries=%d, want 1", p.ku.rebirthTries)
	}
	// A CD refusal keeps the death state and re-arms on the backoff.
	bad, _ := pb.Marshal(&proto.KunlunWarBackHomeResp{Ret: pb.Int32(19915)})
	p.kuHandleBackHome(bad)
	if p.ku.ReviveAt == 0 || p.ku.rebirthTries != 1 {
		t.Fatalf("refusal wiped death bookkeeping: %+v", p.ku)
	}
	p.kuTryAutoRebirth(now.Add(kuRebirthBackoff))
	if ty, _ := kuNextFrame(t, frames); ty != KUNLUN_BACK_HOME_CMD[4:] {
		t.Fatalf("re-arm probe %s", ty)
	}
	// Success clears the cycle and folds the server's use-counters.
	ok, _ := pb.Marshal(&proto.KunlunWarBackHomeResp{Ret: pb.Int32(0),
		BackHomeMsg: &proto.KunlunWarBackHomeMsg{FreeRebirthCount: pb.Int32(1)}})
	p.kuHandleBackHome(ok)
	if p.ku.ReviveAt != 0 || p.ku.rebirthTries != 0 || p.ku.freeUsed != 1 {
		t.Fatalf("success bookkeeping %+v", p.ku)
	}
	back, _ := pb.Marshal(&proto.KunlunWarMyGameInfoSyncMsg{Location: pb.Int32(9 * 61)})
	p.kuHandleMyGameInfo(back)
	p.kuTryAutoRebirth(now.Add(2 * kuRebirthBackoff))
	kuNoFrame(t, frames) // alive → the loop is done
}

// /kunlun_hoisinh [item]: refuses while the army is alive, otherwise sends the
// exact useItem flag and marks the answer as command-owned (leader notified).
func TestKuManualRebirthCommand(t *testing.T) {
	p, frames := kuOpenEventPlayer(t)
	p.ku = &kuEventState{SceneIn: true, Point: 123} // standing on a tile
	if err := p.KunlunWarRebirth(true); err == nil || !strings.Contains(err.Error(), "đang sống") {
		t.Fatalf("alive rebirth = %v, want refusal", err)
	}
	kuNoFrame(t, frames)
	p.ku.Point = 0 // dead
	if err := p.KunlunWarRebirth(true); err != nil {
		t.Fatalf("item rebirth: %v", err)
	}
	ty, body := kuNextFrame(t, frames)
	if ty != KUNLUN_BACK_HOME_CMD[4:] {
		t.Fatalf("command frame %s", ty)
	}
	var req proto.KunlunWarBackHomeReq
	if pb.Unmarshal(body, &req) != nil || req.GetActivityId() != 777 || !req.GetUseItem() {
		t.Fatalf("command body %+v — want activityId=777 useItem=true", &req)
	}
	if !p.ku.manualRebirth {
		t.Fatal("manual flag not set — leader would never hear the verdict")
	}
	// The answer consumes the flag, folds counters, and the manual send
	// paused the auto probe (lastRebirthTry) so no free frame piggybacks.
	ok, _ := pb.Marshal(&proto.KunlunWarBackHomeResp{Ret: pb.Int32(0),
		BackHomeMsg: &proto.KunlunWarBackHomeMsg{ItemRebirthCount: pb.Int32(1)}})
	p.kuHandleBackHome(ok)
	if p.ku.manualRebirth || p.ku.itemUsed != 1 || p.ku.ReviveAt != 0 {
		t.Fatalf("answer bookkeeping %+v", p.ku)
	}
	kuNoFrame(t, frames)
}

// The accelerate opcodes are DERIVED, not captured: the client MsgInfo table
// lists AcclerateMove between HeartBeat (219917) and MiniMap (219919), so the
// request must be 219918 and its answer sits in the usual −200000 window
// (= 19918 = 4dce, the one gap in SERVER_KUNLUN_*). If a real capture ever
// disagrees, fix command.go and this test together.
func TestKuAccelerateOpcodeMath(t *testing.T) {
	cmd, err := strconv.ParseInt(KUNLUN_ACCELERATE_CMD, 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := strconv.ParseInt(SERVER_KUNLUN_ACCELERATE, 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != 219918 || cmd-resp != 200000 {
		t.Fatalf("opcode arithmetic off: cmd=%d resp=%d", cmd, resp)
	}
}

func TestKuAccelerateWires(t *testing.T) {
	p, frames := kuOpenEventPlayer(t)
	p.ku = &kuEventState{SceneIn: true, Point: 123, SpeedUp: 4}
	if err := p.KuAccelerate(); err != nil {
		t.Fatal(err)
	}
	ty, body := kuNextFrame(t, frames)
	if ty != KUNLUN_ACCELERATE_CMD[4:] {
		t.Fatalf("accelerate frame %s", ty)
	}
	var req proto.KunlunWarAcclerateMoveReqMsg
	if pb.Unmarshal(body, &req) != nil || req.GetActivityId() != 777 || req.GetSpeedUpCount() != 4 {
		t.Fatalf("accelerate body %+v — want activityId=777 speedUpCount=4", &req)
	}

	// Dead army (client beenKill convention Point=0) refuses up front.
	p.ku.Point = 0
	if err := p.KuAccelerate(); err == nil || !strings.Contains(err.Error(), "chết") {
		t.Fatalf("dead accelerate = %v, want death refusal", err)
	}
	kuNoFrame(t, frames)
}

// The 19918 answer advances the counter the NEXT request must echo — losing
// that echo is how Holy Land's twin desynced in early testing.
func TestKuAccelerateAnswerAdvancesCounter(t *testing.T) {
	p, frames := kuOpenEventPlayer(t)
	p.ku = &kuEventState{SceneIn: true, Point: 123, SpeedUp: 4}
	ok, _ := pb.Marshal(&proto.KunlunWarAcclerateMoveRespMsg{Ret: pb.Int32(0),
		SpeedUpCount: pb.Int32(5), CostInfo: pb.String("x1")})
	p.kuHandleAccelerate(ok)
	if p.ku.SpeedUp != 5 {
		t.Fatalf("counter not advanced: %d", p.ku.SpeedUp)
	}
	if err := p.KuAccelerate(); err != nil {
		t.Fatal(err)
	}
	_, body := kuNextFrame(t, frames)
	var req proto.KunlunWarAcclerateMoveReqMsg
	if pb.Unmarshal(body, &req) != nil || req.GetSpeedUpCount() != 5 {
		t.Fatalf("stale baseline echoed: %+v", &req)
	}
	// A refusal (army not marching) must NOT move the counter.
	bad, _ := pb.Marshal(&proto.KunlunWarAcclerateMoveRespMsg{Ret: pb.Int32(19944)})
	p.kuHandleAccelerate(bad)
	if p.ku.SpeedUp != 5 {
		t.Fatalf("refusal moved counter: %d", p.ku.SpeedUp)
	}
}

// The user's 2026-10-06 packet capture is the opcode/body oracle for "cắn
// quả": cmd 00035b16 (=219926) with payload
// 08 e4 8f a9 04 | 10 a0 b6 06 | 18 02  →  {activityId:9062372, itemId:105248, num:2}.
// Marshal must reproduce those bytes EXACTLY — any field renumbering in
// tytd.proto breaks here first.
func TestKuUseItemMatchesCapture(t *testing.T) {
	raw, err := pb.Marshal(&proto.KunlunWarUseItemReqMsg{
		ActivityId: pb.Int32(9062372), ItemId: pb.Int32(105248), Num: pb.Int32(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x08, 0xe4, 0x8f, 0xa9, 0x04, 0x10, 0xa0, 0xb6, 0x06, 0x18, 0x02}
	if !bytes.Equal(raw, want) {
		t.Fatalf("wire shape drifted: %x want %x", raw, want)
	}
}

func TestKuRewardItemID(t *testing.T) {
	if kuRewardItemID("105248=6") != 105248 {
		t.Fatal("single pair")
	}
	if kuRewardItemID("105248=6;2=1") != 105248 {
		t.Fatal("multi pair must take the first")
	}
	for _, bad := range []string{"", "abc", "=5", "0=5", "-1=2", "x"} {
		if kuRewardItemID(bad) != 0 {
			t.Fatalf("garbage %q parsed to non-zero", bad)
		}
	}
}

// A ret=0 eat answer carries the server's post-eat energy (client Mgr:
// updateEnergy). Refusals must leave the ledger untouched.
func TestKuHandleUseItemFolds(t *testing.T) {
	p := &EventPlayer{Username: "ev", Running: true}
	p.ku = &kuEventState{Energy: 15, SceneIn: true}
	ok, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{
		Ret: pb.Int32(0), Energy: pb.Int32(20), EnergyRecoverTime: pb.Int64(1234567),
	})
	p.kuHandleUseItem(ok)
	if p.ku.Energy != 20 {
		t.Fatalf("energy not folded: %d", p.ku.Energy)
	}
	bad, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{Ret: pb.Int32(19944), Energy: pb.Int32(99)})
	p.kuHandleUseItem(bad)
	if p.ku.Energy != 20 {
		t.Fatalf("refusal moved energy: %d", p.ku.Energy)
	}
}

// Top-up-to-10 button rule (user 2026-10-07): <10 NL → eat ceil(needs/5)
// (max 2) in ONE packet; ≥10 NL → NO packet at all (skip, not an error).
func TestKuEatFruitToTen(t *testing.T) {
	p, frames := kuOpenEventPlayer(t)
	p.ku = &kuEventState{Energy: 3} // ceil(7/5)=2 → tops to 13
	n, err := p.KunlunWarEatFruitToTen()
	if err != nil || n != 2 {
		t.Fatalf("energy 3 → num %d err %v, want 2", n, err)
	}
	_, body := kuNextFrame(t, frames)
	var req proto.KunlunWarUseItemReqMsg
	if pb.Unmarshal(body, &req) != nil || req.GetNum() != 2 {
		t.Fatalf("wire num %+v", &req)
	}
	p.ku = &kuEventState{Energy: 5} // exactly one bite
	if n, err = p.KunlunWarEatFruitToTen(); err != nil || n != 1 {
		t.Fatalf("energy 5 → num %d err %v, want 1", n, err)
	}
	kuNextFrame(t, frames)
	for _, full := range []int32{10, 12, 20} {
		p.ku = &kuEventState{Energy: full}
		if n, err = p.KunlunWarEatFruitToTen(); n != 0 || err != nil {
			t.Fatalf("energy %d → %d/%v, want silent skip", full, n, err)
		}
	}
	// Bag counted this run (FruitItem!=0): thiếu quả → cắn số còn lại (user
	// 2026-10-07); giỏ biết là hết → KHÔNG gửi packet nào.
	p.ku = &kuEventState{Energy: 0, FruitItem: kuFruitItemObserved, FruitBag: 1}
	if n, err = p.KunlunWarEatFruitToTen(); err != nil || n != 1 {
		t.Fatalf("bag 1 → num %d err %v, want clamp to 1", n, err)
	}
	kuNextFrame(t, frames)
	p.ku = &kuEventState{Energy: 3, FruitItem: kuFruitItemObserved} // bag known-empty
	if n, err = p.KunlunWarEatFruitToTen(); n != 0 || err != nil {
		t.Fatalf("empty bag → %d/%v, want no packet", n, err)
	}
	kuNoFrame(t, frames)
}

// Bag arithmetic (v3): the 19911 receipt ADDS EVERY matching "id=count" pair
// (the customer bug of 2026-10-07: a reward string can pay fruit twice), the
// 19912 welfare verdict folds fruit in too while the ledger is trusted, and
// only a ret=0 19926 answer REMOVES the in-flight num (refusals leave the
// estimate untouched and disarm the chase instead of leaking packets).
func TestKuFruitBagTracking(t *testing.T) {
	p := &EventPlayer{Username: "ev", Running: true, writeChan: make(chan []byte, 4)}
	ok, _ := pb.Marshal(&proto.KunlunWarGetSparPowerRespMsg{Ret: pb.Int32(0), Reward: pb.String("105248=6|2=1|105248=2"), Exp: pb.Int32(9)})
	p.kuHandleSparPower(ok)
	if p.ku.FruitItem != 105248 || p.ku.FruitBag != 8 {
		t.Fatalf("receipt fold must sum all pairs: %+v", p.ku)
	}
	p.ku.eatSendNum = 2
	bad, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{Ret: pb.Int32(101)})
	p.kuHandleUseItem(bad)
	if p.ku.FruitBag != 8 || p.ku.eatSendNum != 0 || p.ku.eatChase {
		t.Fatalf("refusal must not spend and must disarm chase: %+v", p.ku)
	}
	p.ku.eatSendNum = 2
	good, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{Ret: pb.Int32(0), Energy: pb.Int32(10)})
	p.kuHandleUseItem(good)
	if p.ku.FruitBag != 6 || p.ku.Energy != 10 {
		t.Fatalf("success must spend: %+v", p.ku)
	}
	// Welfare (19912) pays fruit too — trusted ledger folds it; untrusted one
	// stays untouched (an unknown history must never fake a small bag).
	w, _ := pb.Marshal(&proto.KunlunWarGetWelfareRespMsg{Ret: pb.Int32(0), Reward: pb.String("105248=3"), Exp: pb.Int32(1)})
	p.kuHandleWelfare(w)
	if p.ku.FruitBag != 9 {
		t.Fatalf("welfare fruit not folded: %+v", p.ku)
	}
	p2 := &EventPlayer{Username: "ev", Running: true}
	p2.kuHandleWelfare(w)
	if p2.ku != nil && p2.ku.FruitBag != 0 {
		t.Fatalf("untrusted ledger must not gain a bag: %+v", p2.ku)
	}
}

// The customer fix (2026-10-07 "cắn có 2 quả trong khi túi vẫn còn"): one press
// CHASES 10 NL using the heal LEARNED from each verdict — here the server
// heals 2 NL/fruit then 1, so the press must fire 2 → 3 → 3 bites (bag 10→2)
// instead of v2's single capped-at-2 packet. Each next packet only appears
// after the previous verdict, and the chase ends at ≥10 with NO further frame.
func TestKuEatChaseLearnsHeal(t *testing.T) {
	p, frames := kuOpenEventPlayer(t)
	p.ku = &kuEventState{Energy: 0, FruitItem: kuFruitItemObserved, FruitBag: 10}
	if n, err := p.KunlunWarEatFruitToTen(); err != nil || n != 2 {
		t.Fatalf("first bite num %d err %v, want 2 (first-guess heal 5)", n, err)
	}
	_, body := kuNextFrame(t, frames)
	var req proto.KunlunWarUseItemReqMsg
	if pb.Unmarshal(body, &req) != nil || req.GetNum() != 2 {
		t.Fatalf("wire bite1 %+v", &req)
	}
	// verdict: energy 0→4 with 2 fruits ⇒ real heal = 2 NL/fruit
	g1, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{Ret: pb.Int32(0), Energy: pb.Int32(4)})
	p.kuHandleUseItem(g1)
	if p.ku.FruitHeal != 2 {
		t.Fatalf("heal not learned: %d", p.ku.FruitHeal)
	}
	_, body = kuNextFrame(t, frames) // chase: ceil(6/2)=3
	if pb.Unmarshal(body, &req) != nil || req.GetNum() != 3 {
		t.Fatalf("wire bite2 %+v, want num 3", &req)
	}
	g2, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{Ret: pb.Int32(0), Energy: pb.Int32(7)})
	p.kuHandleUseItem(g2)            // 4→7 with 3 fruits ⇒ heal 1
	_, body = kuNextFrame(t, frames) // chase: ceil(3/1)=3
	if pb.Unmarshal(body, &req) != nil || req.GetNum() != 3 {
		t.Fatalf("wire bite3 %+v, want num 3", &req)
	}
	g3, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{Ret: pb.Int32(0), Energy: pb.Int32(10)})
	p.kuHandleUseItem(g3)
	if p.ku.Energy != 10 || p.ku.FruitBag != 2 || p.ku.eatChase {
		t.Fatalf("chase must settle at 10: %+v", p.ku)
	}
	kuNoFrame(t, frames) // target reached — no 4th packet
}

// Stale chase must NOT kill the button forever (customer report 2026-11-04
// "bắc minh và côn lôn nút cắn quả không work"): a chase armed >kuEatChaseStuck
// ago means its 4dd6 verdict was lost (mis-routed to the other board, or the
// server stayed silent for an out-of-scene eat). The next press re-arms and
// bites again; a FRESH chase still dedups as before.
func TestKuEatChaseStaleReArms(t *testing.T) {
	p, frames := kuOpenEventPlayer(t)
	p.ku = &kuEventState{Energy: 3, eatChase: true, eatChaseAt: time.Now().Add(-time.Minute), eatSendNum: 2}
	if n, err := p.KunlunWarEatFruitToTen(); err != nil || n != 2 {
		t.Fatalf("stale chase must re-arm: num %d err %v, want 2", n, err)
	}
	kuNextFrame(t, frames)
	if !p.ku.eatChase || p.ku.eatSendNum != 2 {
		t.Fatalf("re-armed chase state: %+v", p.ku)
	}
	p.ku = &kuEventState{Energy: 3, eatChase: true, eatChaseAt: time.Now()}
	if n, err := p.KunlunWarEatFruitToTen(); n != 0 || err != nil {
		t.Fatalf("fresh chase must still dedup: %d/%v", n, err)
	}
	kuNoFrame(t, frames)
}

// The 4dd6 verdict router (reader) delivers to the board with the bite IN
// FLIGHT, not by warKindEP — the fire-and-forget eat send never flips it.
func TestEatPendingEPRouting(t *testing.T) {
	p := &EventPlayer{Username: "ev", Running: true}
	p.ku = &kuEventState{}
	p.bm = &bacminhEventState{}
	if ku, bm := p.eatPendingEP(); ku || bm {
		t.Fatalf("idle must be (false,false): %v/%v", ku, bm)
	}
	p.bm.eatSendNum = 1
	if ku, bm := p.eatPendingEP(); ku || !bm {
		t.Fatalf("north bite must route bm: %v/%v", ku, bm)
	}
	p.ku.eatSendNum = 1
	if ku, bm := p.eatPendingEP(); !ku || !bm {
		t.Fatalf("both bites must tie (fall back to warKind): %v/%v", ku, bm)
	}
}
