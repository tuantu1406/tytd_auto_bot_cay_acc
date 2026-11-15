package tytd_go

import (
	"context"
	"encoding/hex"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Dream-level chop cap, task-driven chopping, the ad-task request, and the
// S_DREAM_DATA_SYNC login push — the union chop-task pipeline.

func TestMaxChopByDreamLv(t *testing.T) {
	cases := []struct{ lv, want int32 }{
		{0, 1}, {1, 1}, {8, 1}, // not-yet-synced and level 1-8
		{9, 2}, {11, 2},
		{12, 3}, {16, 3},
		{17, 4}, {18, 4},
		{19, 5}, {21, 5},
		{22, 6}, {32, 6},
		{33, 7}, {38, 7},
		{39, 8}, {44, 8},
		{45, 9}, {50, 9},
		{51, 10}, {99, 10},
	}
	for _, c := range cases {
		if got := maxChopByDreamLv(c.lv); got != c.want {
			t.Errorf("maxChopByDreamLv(%d)=%d want %d", c.lv, got, c.want)
		}
	}
}

func TestUnionChopPending(t *testing.T) {
	p := &Player{Username: "u", UID: 42}
	if p.unionChopPending() {
		t.Fatal("must not chop before the first 501 snapshot")
	}

	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120001), Value: pb.String("50"), State: pb.Int32(0)},  // at target, just unclaimed
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120002), Value: pb.String("100"), State: pb.Int32(1)}, // claimed
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120006), Value: pb.String("0"), State: pb.Int32(0)},   // ad task: never drives chop
	), true)
	if p.unionChopPending() {
		t.Fatal("ad task must not drive chopping")
	}

	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120005), Value: pb.String("349"), State: pb.Int32(0)},
	), false)
	if !p.unionChopPending() {
		t.Fatal("349/350 must drive chopping")
	}
	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120005), Value: pb.String("350"), State: pb.Int32(0)},
	), false)
	if p.unionChopPending() {
		t.Fatal("350/350 (done, awaiting claim) must stop driving chopping")
	}
	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120005), Value: pb.String("349"), State: pb.Int32(1)},
	), false)
	if p.unionChopPending() {
		t.Fatal("claimed short task must not drive chopping")
	}
}

// watchAdTask sends exactly the captured WatchAdReq body
// (08 00 | 10 C6 A9 07 | 18 00) once, only while the task sits incomplete,
// and throttles retries to one poll window.
func TestWatchAdTaskWire(t *testing.T) {
	p := &Player{Username: "u", UID: 42, writeChan: make(chan []byte, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx // p.write drops frames without a live ctx (#40) — wire tests need one
	t.Cleanup(cancel)
	p.taskKick = make(chan struct{}, 1)

	// Not synced yet: nothing.
	p.watchAdTask()
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("pre-sync watch sent %d frames", n)
	}

	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120006), Value: pb.String("0"), State: pb.Int32(0)},
	), true)
	p.watchAdTask()
	frame := <-p.writeChan
	if ft := frameFuncType(t, frame); ft != S_WATCH_AD_TASK {
		t.Fatalf("frame %s, want watch-ad", ft)
	}
	if got := hex.EncodeToString(frame[18:]); got != "080010c6a9071800" {
		t.Fatalf("watch-ad payload %s, want 080010c6a9071800", got)
	}

	// Immediate re-send: throttled.
	p.watchAdTask()
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("watch-ad re-sent without waiting a poll window")
	}

	// Completed (value 1) or claimed: never re-sent even after the throttle.
	p.lastWatchAd = time.Time{}
	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120006), Value: pb.String("1"), State: pb.Int32(0)},
	), false)
	p.watchAdTask()
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("watched ad task re-sent")
	}
	p.lastWatchAd = time.Time{}
	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120006), Value: pb.String("0"), State: pb.Int32(1)},
	), false)
	p.watchAdTask()
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("claimed ad task re-sent")
	}
}

// The exact 502 that completes a task must also kick the claim loop — the
// browser-side analogue of "rewards claimed the moment they unlock".
func TestTaskKickOnCompletion(t *testing.T) {
	p := &Player{Username: "u", UID: 42}
	p.taskKick = make(chan struct{}, 1)

	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120001), Value: pb.String("49"), State: pb.Int32(0)},
	), true)
	select {
	case <-p.taskKick:
		t.Fatal("49/50 is not claimable, kick fired")
	default:
	}

	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120001), Value: pb.String("50"), State: pb.Int32(0)},
	), false)
	select {
	case <-p.taskKick:
	default:
		t.Fatal("completion 502 did not kick the claim loop")
	}
}

// S_DREAM_DATA_SYNC (transport 00cf): field 1 caches the dream level that
// caps chops per request, while the SAME bytes' field-2 equipment list still
// gets auto-cancelled over 4eea (the old DoChuaHuyResponse behaviour).
func TestHandleDreamSync(t *testing.T) {
	p := &Player{Username: "u", UID: 42, writeChan: make(chan []byte, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx // p.write drops frames without a live ctx (#40)
	t.Cleanup(cancel)
	if p.maxChopTimes() != 1 {
		t.Fatal("unsynced account must chop 1 per request")
	}

	// One payload, both readings: field 1 varint 11 == dreamLv 11; the
	// DoChuaHuyResponse-shaped field-2 entry carries equipment id 77.
	combined, err := pb.Marshal(&proto.DoChuaHuyResponse{
		Error: 11,
		L1:    []*proto.L1Khongitems{{L2: &proto.L2Khongitems{Id: 77}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.handleDreamSync(combined)

	frame := <-p.writeChan
	if ft := frameFuncType(t, frame); ft != S_HUY_DO_CHAT_CAY {
		t.Fatalf("frame %s, want huy-do", ft)
	}
	if got := hex.EncodeToString(frame[18:]); got != "080112014d" { // oke=1, id=[77] (proto3-packed)
		t.Fatalf("huy-do payload %s, want 080112014d", got)
	}
	if got := p.maxChopTimes(); got != 2 {
		t.Fatalf("after dreamLv=11 maxChopTimes=%d, want 2", got)
	}

	// A later sync (level-up) replaces the cap.
	bump, _ := pb.Marshal(&proto.DreamDataMsg{DreamLv: pb.Int32(12)})
	p.handleDreamSync(bump)
	if got := p.maxChopTimes(); got != 3 {
		t.Fatalf("after dreamLv=12 maxChopTimes=%d, want 3", got)
	}

}

// autoPeach must ask for the dream cap, not a fixed 10.
func TestAutoPeachUsesDreamCap(t *testing.T) {
	p := &Player{Username: "u", UID: 42, Peach: true, writeChan: make(chan []byte, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	t.Cleanup(cancel)
	bump, _ := pb.Marshal(&proto.DreamDataMsg{DreamLv: pb.Int32(12)})
	p.handleDreamSync(bump) // lv 12 → cap 3

	go p.autoPeach()
	frame := <-p.writeChan
	if ft := frameFuncType(t, frame); ft != S_CHAT_CAY {
		t.Fatalf("frame %s, want chat-cay", ft)
	}
	if got := hex.EncodeToString(frame[18:]); got != "08011803" { // auto=true, times=3
		t.Fatalf("chop payload %s, want 08011803 (cap 3)", got)
	}
}

// A pending union task keeps chopping with the toggle off; chopStop (set by
// a peach-out rejection) halts it.
func TestTaskDrivenChopRespectsChopStop(t *testing.T) {
	p := &Player{Username: "u", UID: 42, writeChan: make(chan []byte, 16)}
	p.taskKick = make(chan struct{}, 1)
	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120005), Value: pb.String("0"), State: pb.Int32(0)},
	), true)

	if !p.taskDrivenChopOK() {
		t.Fatal("pending 350-chop task should drive chopping with Peach off")
	}
	p.setChopStop(true)
	if p.taskDrivenChopOK() {
		t.Fatal("chopStop must pause even task-driven chopping")
	}
	p.setChopStop(false)

	// With nothing pending the loop idles: a full 10 s wait, then continue.
	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120005), Value: pb.String("350"), State: pb.Int32(1)},
	), false)
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	t.Cleanup(cancel)
	go p.autoPeach()
	select {
	case f := <-p.writeChan:
		t.Fatalf("idling account chopped anyway: %x", f)
	case <-time.After(500 * time.Millisecond):
	}
}

// Chop stall recovery: 517/532 no longer stops the loop outright — the ids
// of the last unproven huỷ are re-sent and the server's authoritative
// undealt list is requested, exactly the cleanup the login 00cf performs.
func TestChopStallRecovery(t *testing.T) {
	p := &Player{Username: "u", UID: 42, Peach: true, writeChan: make(chan []byte, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx // p.write drops frames without a live ctx (#40)
	t.Cleanup(cancel)
	p.setPeachPending([]int32{77})

	stall, err := pb.Marshal(&proto.ChatCayResponse{Error: 517})
	if err != nil {
		t.Fatal(err)
	}
	p.handleChatCay(stall)

	f1 := <-p.writeChan
	if ft := frameFuncType(t, f1); ft != S_HUY_DO_CHAT_CAY {
		t.Fatalf("frame %s, want huy-do retry", ft)
	}
	if got := hex.EncodeToString(f1[18:]); got != "080112014d" { // oke=1, id=[77]
		t.Fatalf("retry payload %s, want 080112014d", got)
	}
	f2 := <-p.writeChan
	if ft := frameFuncType(t, f2); ft != S_GET_UNDEAL_EQUIP {
		t.Fatalf("frame %s, want get-undeal-equip", ft)
	}
	if l := len(f2[18:]); l != 0 {
		t.Fatalf("get-undeal payload not empty: %x", f2[18:])
	}
	if p.errPeach != 1 {
		t.Fatalf("errPeach=%d, want 1", p.errPeach)
	}
	if !p.Peach {
		t.Fatal("a single stall must not stop chopping")
	}
}

// Five consecutive stalls that found nothing to clear (empty pending, so the
// server list is the only lead and never arrives here) still retire with the
// classic funtap-overload back-off notice.
func TestChopStallGivesUpAfterFiveEmpty(t *testing.T) {
	p := &Player{Username: "u", UID: 42, Peach: true, writeChan: make(chan []byte, 32)}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx // p.write drops frames without a live ctx (#40)
	t.Cleanup(cancel)
	stall, err := pb.Marshal(&proto.ChatCayResponse{Error: 532})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		p.handleChatCay(stall)
	}
	if n := len(p.writeChan); n != 5 {
		t.Fatalf("5 stalls sent %d frames, want 5 get-undeal", n)
	}
	for i := 0; i < 5; i++ {
		if ft := frameFuncType(t, <-p.writeChan); ft != S_GET_UNDEAL_EQUIP {
			t.Fatalf("frame %d not get-undeal", i)
		}
	}
	if !p.Peach || p.chopStop {
		t.Fatal("5th stall must not retire yet (back-off starts past 5)")
	}

	p.handleChatCay(stall) // 6th with nothing found → back off
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("give-up sent %d frames, want none", n)
	}
	if p.Peach {
		t.Fatal("give-up must clear Peach")
	}
	if !p.chopStop {
		t.Fatal("give-up must set chopStop")
	}
	if p.errPeach != 0 {
		t.Fatalf("give-up should reset errPeach, got %d", p.errPeach)
	}
}

// An accepted chop is itself the proof the board cleared: it breaks the
// stall streak, re-arms chopping and re-bases pending on the new drops.
func TestChopStallClearsOnAcceptedChop(t *testing.T) {
	p := &Player{Username: "u", UID: 42, writeChan: make(chan []byte, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx // p.write drops frames without a live ctx (#40)
	t.Cleanup(cancel)
	p.dreamMu.Lock()
	p.errPeach = 3
	p.dreamMu.Unlock()
	p.setChopStop(true)
	p.setPeachPending([]int32{77})

	// Items deliberately avoid "100026" (khiêu chiến side effect + 1 s sleep).
	ok, err := pb.Marshal(&proto.ChatCayResponse{L1: []*proto.L1{
		{Items: []string{"900001"}, L2: &proto.L2{L3: &proto.L3{Id: 88}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p.handleChatCay(ok)

	f := <-p.writeChan
	if ft := frameFuncType(t, f); ft != S_HUY_DO_CHAT_CAY {
		t.Fatalf("frame %s, want huy-do for new drop", ft)
	}
	if got := hex.EncodeToString(f[18:]); got != "0801120158" { // oke=1, id=[88]
		t.Fatalf("new-drop huy payload %s, want 0801120158", got)
	}
	if p.errPeach != 0 {
		t.Fatalf("accepted chop left errPeach=%d", p.errPeach)
	}
	if p.chopStop {
		t.Fatal("accepted chop must re-arm chopping")
	}
	if ids := p.peachPendingIDs(); len(ids) != 1 || ids[0] != 88 {
		t.Fatalf("pending after accepted chop %v, want [88]", ids)
	}
}

// The server's answer to the stall probe carries the wedged items: huỷ them,
// re-base pending, and break the streak — recovery is making progress. An
// empty answer changes nothing, so genuine overload still retires.
func TestUnDealEquipRepairsStall(t *testing.T) {
	p := &Player{Username: "u", UID: 42, writeChan: make(chan []byte, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx // p.write drops frames without a live ctx (#40)
	t.Cleanup(cancel)
	p.dreamMu.Lock()
	p.errPeach = 4
	p.dreamMu.Unlock()

	list, err := pb.Marshal(&proto.DoChuaHuyResponse{
		L1: []*proto.L1Khongitems{{L2: &proto.L2Khongitems{Id: 77}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.handleUnDealEquip(list)

	f := <-p.writeChan
	if ft := frameFuncType(t, f); ft != S_HUY_DO_CHAT_CAY {
		t.Fatalf("frame %s, want huy-do repair", ft)
	}
	if got := hex.EncodeToString(f[18:]); got != "080112014d" {
		t.Fatalf("repair payload %s, want 080112014d", got)
	}
	if p.errPeach != 0 {
		t.Fatalf("repair left errPeach=%d, want 0", p.errPeach)
	}
	if ids := p.peachPendingIDs(); len(ids) != 1 || ids[0] != 77 {
		t.Fatalf("pending after repair %v, want [77]", ids)
	}

	p.dreamMu.Lock()
	p.errPeach = 5
	p.dreamMu.Unlock()
	empty, err := pb.Marshal(&proto.DoChuaHuyResponse{})
	if err != nil {
		t.Fatal(err)
	}
	p.handleUnDealEquip(empty)
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("empty undeal list wrote %d frames", n)
	}
	if p.errPeach != 5 {
		t.Fatalf("empty undeal list reset errPeach to %d, want 5", p.errPeach)
	}
}

// Error 101 is a real out-of-peach, not a stall: it stops even mid-back-off.
func TestChopPeachOutStillStops(t *testing.T) {
	p := &Player{Username: "u", UID: 42, Peach: true, writeChan: make(chan []byte, 16)}
	p.dreamMu.Lock()
	p.errPeach = 5
	p.dreamMu.Unlock()

	out, err := pb.Marshal(&proto.ChatCayResponse{Error: 101})
	if err != nil {
		t.Fatal(err)
	}
	p.handleChatCay(out)

	if n := len(p.writeChan); n != 0 {
		t.Fatalf("101 wrote %d frames", n)
	}
	if p.Peach || !p.chopStop {
		t.Fatalf("101: Peach=%v chopStop=%v, want stopped", p.Peach, p.chopStop)
	}
}

// Sanity: pack frame offset used across these tests.
func TestFrameOffset(t *testing.T) {
	f := pack.Pack(S_CHAT_CAY, []byte{0x01}, 42)
	if len(f) != 19 || f[18] != 0x01 {
		t.Fatalf("frame layout changed: %x", f)
	}
}
