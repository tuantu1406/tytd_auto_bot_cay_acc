package tytd_go

// /bacminh_follow event-side transport on the NORTH board — the wire mirror
// of player_event_kunlunwar_test.go with the differences this feature is
// about: activityId is the type-332 window, posIndex math runs on the 119
// grid, the eat capture oracle is the user's own NORTH packet
// ({9062374, 105363, 1}), and the two boards must never seat one account at
// the same time (transport invariant — answers ride shared hexes).

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func bmOpenEventPlayer(t *testing.T) (*EventPlayer, chan []byte) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := &EventPlayer{Username: "evbm", UID: 4243, Running: true,
		ctx: ctx, cancel: cancel, writeChan: make(chan []byte, 32)}
	now := time.Now()
	full, err := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{{
		ActivityId:    pb.Int32(888),
		Type:          pb.Int32(activityTypeBacMinh),
		BeginShowTime: pb.Int64(now.Add(-2 * time.Hour).UnixMilli()),
		BeginTime:     pb.Int64(now.Add(-time.Hour).UnixMilli()),
		EndTime:       pb.Int64(now.Add(time.Hour).UnixMilli()),
		EndShowTime:   pb.Int64(now.Add(2 * time.Hour).UnixMilli()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	p.ActList.applyList("evbm", full, true)
	if !p.BacMinhWarEventOpen() {
		t.Fatal("332 window not seen")
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

// kuAssertCmdFrame checks a raw frame's 4-byte command hex (shared by the
// north minimap-pull assertions; Unpack's string form loses the high bytes).
func kuAssertCmdFrame(t *testing.T, f []byte, wantCmd, what string) {
	t.Helper()
	if ty := hex.EncodeToString(f[6:10]); ty != wantCmd {
		t.Fatalf("%s frame cmd %s — want %s", what, ty, wantCmd)
	}
}

func TestBmEventFollowEntersThenMoves(t *testing.T) {
	p, frames := bmOpenEventPlayer(t)
	cols := gimap.BacMinhCols()
	if cols != 119 {
		t.Fatalf("BacMinhCols = %d, want 119", cols)
	}
	const target = int32(60*119 + 60)
	done := make(chan error, 1)
	go func() { done <- p.BacMinhWarMoveTo(target) }()

	ty, body := kuNextFrame(t, frames)
	if ty != KUNLUN_ENTER_GAME_CMD[4:] {
		t.Fatalf("first frame %s, want EnterGame", ty)
	}
	var ereq proto.KunlunWarEnterGameReq
	if pb.Unmarshal(body, &ereq) != nil || ereq.GetActivityId() != 888 {
		t.Fatalf("EnterGame body %+v — activityId must be the 332 window", &ereq)
	}
	// warKindEP flipped to bm BEFORE the write (shared-hex routing).
	if p.warKindEP != warKindBacMinh {
		t.Fatalf("warKindEP=%q during in-flight north request", p.warKindEP)
	}
	resp, _ := pb.Marshal(&proto.KunlunWarEnterGameResp{
		Ret: pb.Int32(0), Home: pb.Int32(6013),
		MyPlayerInfo: &proto.KunlunWarPlayerInfo{Pos: pb.Int32(10*119 + 1)},
	})
	p.bmHandleEnterGame(resp)

	ty, body = kuNextFrame(t, frames)
	if ty != KUNLUN_MOVE_CMD[4:] {
		t.Fatalf("second frame %s, want Move", ty)
	}
	var mreq proto.KunlunWarMoveReq
	if pb.Unmarshal(body, &mreq) != nil || mreq.GetTargetPoint() != target || mreq.GetActivityId() != 888 {
		t.Fatalf("Move body %+v", &mreq)
	}
	if err := <-done; err != nil {
		t.Fatalf("order returned %v", err)
	}
	// Arrival then re-follow = silent no-op.
	arr, _ := pb.Marshal(&proto.KunlunWarMyGameInfoSyncMsg{Location: pb.Int32(target)})
	p.bmHandleMyGameInfo(arr)
	if err := p.BacMinhWarMoveTo(target); err != nil {
		t.Fatalf("re-follow: %v", err)
	}
	kuNoFrame(t, frames)
}

func TestBmEventDeadAccountRefused(t *testing.T) {
	p, frames := bmOpenEventPlayer(t)
	done := make(chan error, 1)
	go func() { done <- p.BacMinhWarMoveTo(40*119 + 20) }()
	kuNextFrame(t, frames) // EnterGame
	// Join STANDING ELSEWHERE (5,5-ish) so the first order really marches.
	resp, _ := pb.Marshal(&proto.KunlunWarEnterGameResp{Ret: pb.Int32(0), MyPlayerInfo: &proto.KunlunWarPlayerInfo{Pos: pb.Int32(5*119 + 5)}})
	p.bmHandleEnterGame(resp)
	kuNextFrame(t, frames) // Move
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	loc, _ := pb.Marshal(&proto.KunlunWarMyGameInfoSyncMsg{Location: pb.Int32(0)})
	p.bmHandleMyGameInfo(loc)
	err := p.BacMinhWarMoveTo(60*119 + 60)
	if err == nil || !strings.Contains(err.Error(), "chết") {
		t.Fatalf("dead account order = %v, want death refusal", err)
	}
	kuNoFrame(t, frames)
}

// Transport invariant: each board's ensureScene must refuse while the OTHER
// board seats the account — the server cannot attribute answers that ride
// the same SERVER_KUNLUN_* hexes, so neither can we.
func TestBmEventSeatExclusion(t *testing.T) {
	p, _ := bmOpenEventPlayer(t)
	// Kunlun window also open (same fleet can see both in the calendar).
	now := time.Now()
	full, _ := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{{
		ActivityId: pb.Int32(777), Type: pb.Int32(activityTypeKunlunWar),
		BeginShowTime: pb.Int64(now.Add(-2 * time.Hour).UnixMilli()),
		BeginTime:     pb.Int64(now.Add(-time.Hour).UnixMilli()),
		EndTime:       pb.Int64(now.Add(time.Hour).UnixMilli()),
		EndShowTime:   pb.Int64(now.Add(2 * time.Hour).UnixMilli()),
	}}})
	p.ActList.applyList("evbm", full, false) // INCREMENTAL merge keeps the 332 row
	if !p.KunlunWarEventOpen() || !p.BacMinhWarEventOpen() {
		t.Fatal("both windows must be open for this guard test")
	}
	p.bm = &bacminhEventState{SceneIn: true}
	if err := p.kuEnsureScene(777, time.Second); err == nil || !strings.Contains(err.Error(), "Bắc Minh") {
		t.Fatalf("kunlun join while seated north = %v, want cross-refusal", err)
	}
	p.bm = &bacminhEventState{}
	p.ku = &kuEventState{SceneIn: true}
	if err := p.bmEnsureScene(888, time.Second); err == nil || !strings.Contains(err.Error(), "Côn Lôn") {
		t.Fatalf("north join while seated kunlun = %v, want cross-refusal", err)
	}
}

func TestBmEventStatusLines(t *testing.T) {
	ep := &EventPlayer{}
	if got := ep.BmStatus(); got != "⏹️ chưa login" {
		t.Fatalf("offline: %q", got)
	}
	ep.Running = true
	ep.bm = &bacminhEventState{}
	if got := ep.BmStatus(); !strings.Contains(got, "chưa mở") {
		t.Fatalf("no activity: %q", got)
	}
	ep.bm.ActivityID = 6
	if got := ep.BmStatus(); !strings.Contains(got, "ngoài map") {
		t.Fatalf("outside scene: %q", got)
	}
	ep.bm.SceneIn = true
	// (59,60) on the 119 grid: pos = 60*119+59 = 7200-… compute explicitly.
	ep.bm.Point = int32(59 + 60*119)
	ep.bm.Energy, ep.bm.HomeCity = 3, 6013
	got := ep.BmStatus()
	want := fmt.Sprintf("ô %d (59,60)", ep.bm.Point)
	if !strings.Contains(got, want) || !strings.Contains(got, "NL 3") {
		t.Fatalf("in scene: %q want %q", got, want)
	}
	ep.bm.Point = 0
	if got := ep.BmStatus(); !strings.Contains(got, "chết") {
		t.Fatalf("dead: %q", got)
	}
}

// The north minimap pull must carry floor+group together on 332 too
// (EMPTY-board lesson is engine-wide), learning them via EnterActivity first.
func TestBmEventMiniMapPull(t *testing.T) {
	p, frames := bmOpenEventPlayer(t)
	enter, _ := pb.Marshal(&proto.KunlunWarEnterActivityResp{
		Ret: pb.Int32(0), Floor: pb.Int32(10002), Group: pb.Int32(1),
	})
	mini, _ := pb.Marshal(&proto.KunlunWarMiniMapInfoRespMsg{
		BaseData: []*proto.KunlunUnionBaseData{{UnionId: pb.Int64(1), Name: pb.String("N")}},
	})
	done := make(chan error, 1)
	go func() {
		b, err := p.RequestBacMinhMiniMap(2 * time.Second)
		if err == nil && !bytes.Equal(b, mini) {
			err = fmt.Errorf("payload not delivered through the seat")
		}
		done <- err
	}()
	kuAssertCmdFrame(t, <-frames, KUNLUN_ENTER_ACTIVITY_CMD, "EnterActivity")
	p.bmDeliverFrame(SERVER_KUNLUN_ENTER_ACTIVITY, enter)
	f := <-frames
	kuAssertCmdFrame(t, f, KUNLUN_MINI_MAP_INFO_CMD, "MiniMap")
	var req proto.KunlunWarMiniMapInfoReqMsg
	if err := pb.Unmarshal(f[18:], &req); err != nil {
		t.Fatalf("minimap req: %v", err)
	}
	if req.GetFloor() != 10002 || req.GetGroup() != 1 || req.GetActivityId() != 888 {
		t.Fatalf("north floor/group/activity pairing lost: %+v", &req)
	}
	p.bmDeliverFrame(SERVER_KUNLUN_MINI_MAP_INFO, mini)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Auto rebirth on the north board stays FREE (useItem=false) on the backoff —
// the item budget is the server's; the manual command is the only item path.
func TestBmAutoRebirthFreeProbe(t *testing.T) {
	p, frames := bmOpenEventPlayer(t)
	p.bm = &bacminhEventState{SceneIn: true, ReviveAt: time.Now().Add(-time.Hour).UnixMilli()}
	now := time.Now()
	p.bmTryAutoRebirth(now)
	ty, body := kuNextFrame(t, frames)
	if ty != KUNLUN_BACK_HOME_CMD[4:] {
		t.Fatalf("probe frame %s, want BackHome", ty)
	}
	var req proto.KunlunWarBackHomeReq
	if pb.Unmarshal(body, &req) != nil || req.GetActivityId() != 888 || req.GetUseItem() {
		t.Fatalf("probe body %+v — auto path must never spend items", &req)
	}
	p.bmTryAutoRebirth(now.Add(time.Second))
	kuNoFrame(t, frames)
}

// /bacminh_hoisinh [item] — user-authorized instant revive: the alive army
// refuses, the dead army gets the EXACT useItem flag (free default, item on
// argument), and the verdict stays command-owned.
func TestBmManualRebirthCommand(t *testing.T) {
	p, frames := bmOpenEventPlayer(t)
	p.bm = &bacminhEventState{SceneIn: true, Point: 321}
	if err := p.BacMinhWarRebirth(false); err == nil || !strings.Contains(err.Error(), "đang sống") {
		t.Fatalf("alive rebirth = %v, want refusal", err)
	}
	kuNoFrame(t, frames)
	p.bm.Point = 0
	if err := p.BacMinhWarRebirth(true); err != nil {
		t.Fatalf("item rebirth: %v", err)
	}
	ty, body := kuNextFrame(t, frames)
	if ty != KUNLUN_BACK_HOME_CMD[4:] {
		t.Fatalf("command frame %s", ty)
	}
	var req proto.KunlunWarBackHomeReq
	if pb.Unmarshal(body, &req) != nil || req.GetActivityId() != 888 || !req.GetUseItem() {
		t.Fatalf("command body %+v — want activityId=888 useItem=true", &req)
	}
	if !p.bm.manualRebirth {
		t.Fatal("manual flag not set — leader would never hear the verdict")
	}
	ok, _ := pb.Marshal(&proto.KunlunWarBackHomeResp{Ret: pb.Int32(0),
		BackHomeMsg: &proto.KunlunWarBackHomeMsg{ItemRebirthCount: pb.Int32(1)}})
	p.bmHandleBackHome(ok)
	if p.bm.manualRebirth || p.bm.itemUsed != 1 || p.bm.ReviveAt != 0 {
		t.Fatalf("answer bookkeeping %+v", p.bm)
	}
	kuNoFrame(t, frames)
}

// ⚡tăng tốc on 332 — press-thru authorized by the user: same request shape
// as Côn Lôn (activityId + speedUpCount echo), no confirm path to test here.
func TestBmAccelerateWires(t *testing.T) {
	p, frames := bmOpenEventPlayer(t)
	p.bm = &bacminhEventState{SceneIn: true, Point: 123, SpeedUp: 2}
	if err := p.BmAccelerate(); err != nil {
		t.Fatal(err)
	}
	ty, body := kuNextFrame(t, frames)
	if ty != KUNLUN_ACCELERATE_CMD[4:] {
		t.Fatalf("accelerate frame %s", ty)
	}
	var req proto.KunlunWarAcclerateMoveReqMsg
	if pb.Unmarshal(body, &req) != nil || req.GetActivityId() != 888 || req.GetSpeedUpCount() != 2 {
		t.Fatalf("accelerate body %+v", &req)
	}
	ok, _ := pb.Marshal(&proto.KunlunWarAcclerateMoveRespMsg{Ret: pb.Int32(0),
		SpeedUpCount: pb.Int32(3), CostInfo: pb.String("105369=1")})
	p.bmHandleAccelerate(ok)
	if p.bm.SpeedUp != 3 {
		t.Fatalf("counter not advanced: %d", p.bm.SpeedUp)
	}
}

// The user's 2026-10-07 NORTH packet capture is the eat opcode/body oracle:
// cmd 00035b16 with payload 08 e6 8f a9 04 | 10 93 b7 06 | 18 01 →
// {activityId:9062374, itemId:105363, num:1}. Marshal must reproduce those
// bytes EXACTLY (also pins bmFruitItemObserved as the fallback the press uses).
func TestBmUseItemMatchesCapture(t *testing.T) {
	raw, err := pb.Marshal(&proto.KunlunWarUseItemReqMsg{
		ActivityId: pb.Int32(9062374), ItemId: pb.Int32(105363), Num: pb.Int32(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x08, 0xe6, 0x8f, 0xa9, 0x04, 0x10, 0x93, 0xb7, 0x06, 0x18, 0x01}
	if !bytes.Equal(raw, want) {
		t.Fatalf("wire shape drifted: %x want %x", raw, want)
	}
	if bmFruitItemObserved != 105363 {
		t.Fatalf("fallback fruit id drifted: %d", bmFruitItemObserved)
	}
}

// BacMinhEatFruit presses with the account's own bag id when the bot
// harvested this run, else the captured fallback — never an invented id.
func TestBmEatFruitItemSelection(t *testing.T) {
	p, frames := bmOpenEventPlayer(t)
	if err := p.BacMinhEatFruit(1); err != nil {
		t.Fatal(err)
	}
	_, body := kuNextFrame(t, frames)
	var req proto.KunlunWarUseItemReqMsg
	if pb.Unmarshal(body, &req) != nil || req.GetItemId() != bmFruitItemObserved || req.GetNum() != 1 {
		t.Fatalf("fallback eat body %+v — want captured 105363 num=1", &req)
	}
	p.bm = &bacminhEventState{FruitItem: 111222}
	if err := p.BacMinhEatFruit(3); err != nil {
		t.Fatal(err)
	}
	_, body = kuNextFrame(t, frames)
	if pb.Unmarshal(body, &req) != nil || req.GetItemId() != 111222 || req.GetNum() != 3 {
		t.Fatalf("bag-id eat body %+v — want observed 111222 num=3", &req)
	}
}

// A 19911 harvest receipt is the ONLY source that may upgrade the bag id —
// the reward string parse is the shared kuRewardItemID oracle.
func TestBmSparPowerFoldsBagID(t *testing.T) {
	p := &EventPlayer{Username: "evbm", Running: true, writeChan: make(chan []byte, 4)}
	ok, _ := pb.Marshal(&proto.KunlunWarGetSparPowerRespMsg{
		Ret: pb.Int32(0), Reward: pb.String("105363=2"), Exp: pb.Int32(20),
	})
	p.bmHandleSparPower(ok)
	if p.bm == nil || p.bm.FruitItem != 105363 || p.bm.FruitBag != 2 {
		t.Fatalf("bag id+count not folded from receipt: %+v", p.bm)
	}
}

// Top-up-to-10 with the bag clamp — the north mirror of TestKuEatFruitToTen:
// <10 NL eats ceil(needs/5) max 2, "thiếu quả thì cắn số còn lại", known-empty
// bag sends NOTHING, ≥10 NL is a silent skip.
func TestBmEatFruitToTen(t *testing.T) {
	p, frames := bmOpenEventPlayer(t)
	p.bm = &bacminhEventState{Energy: 3}
	if n, err := p.BacMinhEatFruitToTen(); err != nil || n != 2 {
		t.Fatalf("energy 3 → num %v err %v, want 2", n, err)
	}
	_, body := kuNextFrame(t, frames)
	var req proto.KunlunWarUseItemReqMsg
	if pb.Unmarshal(body, &req) != nil || req.GetNum() != 2 || req.GetActivityId() != 888 {
		t.Fatalf("wire body %+v", &req)
	}
	p.bm = &bacminhEventState{Energy: 0, FruitItem: bmFruitItemObserved, FruitBag: 1}
	if n, err := p.BacMinhEatFruitToTen(); err != nil || n != 1 {
		t.Fatalf("bag 1 → num %v err %v, want clamp 1", n, err)
	}
	kuNextFrame(t, frames)
	p.bm = &bacminhEventState{Energy: 0, FruitItem: bmFruitItemObserved}
	if n, err := p.BacMinhEatFruitToTen(); n != 0 || err != nil {
		t.Fatalf("known-empty bag → %d/%v, want no packet", n, err)
	}
	p.bm = &bacminhEventState{Energy: 10}
	if n, err := p.BacMinhEatFruitToTen(); n != 0 || err != nil {
		t.Fatalf("energy 10 → %d/%v, want skip", n, err)
	}
	kuNoFrame(t, frames)
}

// The customer report of 2026-10-07 was EXACTLY this board ("cắn có 2 quả
// trong khi túi vẫn còn"): north heal ≠ 5 NL/fruit, so v2's capped single
// press stopped short. v3 learns the real heal from each verdict and keeps
// biting (bounded): server heals 2 then 1 NL/fruit ⇒ bites 2 → 3 → 3, bag
// 10→2, chase disarmed at ≥10 with NO further frame.
func TestBmEatChaseLearnsHeal(t *testing.T) {
	p, frames := bmOpenEventPlayer(t)
	p.bm = &bacminhEventState{Energy: 0, FruitItem: bmFruitItemObserved, FruitBag: 10}
	if n, err := p.BacMinhEatFruitToTen(); err != nil || n != 2 {
		t.Fatalf("first bite num %d err %v, want 2 (first-guess heal 5)", n, err)
	}
	_, body := kuNextFrame(t, frames)
	var req proto.KunlunWarUseItemReqMsg
	if pb.Unmarshal(body, &req) != nil || req.GetNum() != 2 || req.GetItemId() != bmFruitItemObserved {
		t.Fatalf("wire bite1 %+v", &req)
	}
	g1, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{Ret: pb.Int32(0), Energy: pb.Int32(4)})
	p.bmHandleUseItem(g1)
	if p.bm.FruitHeal != 2 {
		t.Fatalf("north heal not learned: %d", p.bm.FruitHeal)
	}
	_, body = kuNextFrame(t, frames) // chase: ceil(6/2)=3
	if pb.Unmarshal(body, &req) != nil || req.GetNum() != 3 {
		t.Fatalf("wire bite2 %+v, want num 3", &req)
	}
	g2, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{Ret: pb.Int32(0), Energy: pb.Int32(7)})
	p.bmHandleUseItem(g2)            // 4→7 with 3 fruits ⇒ heal 1
	_, body = kuNextFrame(t, frames) // chase: ceil(3/1)=3
	if pb.Unmarshal(body, &req) != nil || req.GetNum() != 3 {
		t.Fatalf("wire bite3 %+v, want num 3", &req)
	}
	g3, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{Ret: pb.Int32(0), Energy: pb.Int32(10)})
	p.bmHandleUseItem(g3)
	if p.bm.Energy != 10 || p.bm.FruitBag != 2 || p.bm.eatChase {
		t.Fatalf("chase must settle at 10: %+v", p.bm)
	}
	// Welfare (19912) fruit folds into the trusted north bag too — the second
	// half of the undercount that starved v2.
	w, _ := pb.Marshal(&proto.KunlunWarGetWelfareRespMsg{Ret: pb.Int32(0), Reward: pb.String("105363=4"), Exp: pb.Int32(1)})
	p.bmHandleWelfare(w)
	if p.bm.FruitBag != 6 {
		t.Fatalf("welfare fruit not folded: %+v", p.bm)
	}
	kuNoFrame(t, frames)
}

// North twin of TestKuEatChaseStaleReArms — the customer's dead 🍈 button on
// Đỉnh Bắc Minh (2026-11-04): lost verdict ⇒ stale chase ⇒ next press re-arms.
func TestBmEatChaseStaleReArms(t *testing.T) {
	p, frames := bmOpenEventPlayer(t)
	p.bm = &bacminhEventState{Energy: 3, eatChase: true, eatChaseAt: time.Now().Add(-time.Minute), eatSendNum: 2}
	if n, err := p.BacMinhEatFruitToTen(); err != nil || n != 2 {
		t.Fatalf("stale chase must re-arm: num %d err %v, want 2", n, err)
	}
	kuNextFrame(t, frames)
	if !p.bm.eatChase || p.bm.eatSendNum != 2 {
		t.Fatalf("re-armed chase state: %+v", p.bm)
	}
	p.bm = &bacminhEventState{Energy: 3, eatChase: true, eatChaseAt: time.Now()}
	if n, err := p.BacMinhEatFruitToTen(); n != 0 || err != nil {
		t.Fatalf("fresh chase must still dedup: %d/%v", n, err)
	}
	kuNoFrame(t, frames)
}
