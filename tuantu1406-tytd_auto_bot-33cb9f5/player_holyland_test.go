package tytd_go

import (
	"encoding/hex"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Battle-window anchors for tests. The window check only ADDS offsets to the
// server's absolute day anchors, so a test anchor need not be a midnight.
func hlLiveAnchor(now time.Time) int64  { return now.UnixMilli() - hlFightBeginMs - 5*60*1000 }
func hlEndedAnchor(now time.Time) int64 { return now.UnixMilli() - hlFightBeginMs - 90*60*1000 }

// The Cửu U gate is TWO facts: the schedule (209570 → 2562) says open AND
// now lies inside one of the scheduleTimestamp battle windows. The live
// probe (2026-10-03) proved ActList 346 is not required for the event to
// run; the user report (2026-10-06) proved isOpen alone is not sufficient —
// it holds across the season while rounds run one hour a day.
func TestHolyLandScheduleGate(t *testing.T) {
	now := time.Now()
	p := &Player{}
	if holyLandEventOpen(p) {
		t.Fatal("zero-state player must not be open")
	}

	// isOpen=true WITHOUT any day anchor: nothing honest → closed (the
	// client's own default schedule [0,0,0,0,0] renders NotOpen).
	noSched, _ := pb.Marshal(&proto.HolyLandBattleApplyDataSyncMsg{
		Ret: pb.Int32(0), IsOpen: pb.Bool(true), RoundId: pb.Int32(4),
	})
	p.hlHandleScheduleMsg(noSched)
	if holyLandEventOpen(p) {
		t.Fatal("open with no day anchors must not show")
	}

	open, _ := pb.Marshal(&proto.HolyLandBattleApplyDataSyncMsg{
		Ret: pb.Int32(0), IsOpen: pb.Bool(true), RoundId: pb.Int32(4),
		ScheduleTimestamp: []int64{hlEndedAnchor(now), hlLiveAnchor(now)},
	})
	p.hlHandleScheduleMsg(open)
	if !holyLandEventOpen(p) {
		t.Fatal("isOpen=true inside a battle window must open the gate")
	}
	if p.HlRoundId != 4 {
		t.Fatalf("roundId not cached: %d", p.HlRoundId)
	}

	ended, _ := pb.Marshal(&proto.HolyLandBattleApplyDataSyncMsg{
		Ret: pb.Int32(0), IsOpen: pb.Bool(true),
		ScheduleTimestamp: []int64{hlEndedAnchor(now)},
	})
	p.hlHandleScheduleMsg(ended)
	if holyLandEventOpen(p) {
		t.Fatal("isOpen=true past every window must close the display (the 2026-10-06 bug)")
	}

	closed, _ := pb.Marshal(&proto.HolyLandBattleApplyDataSyncMsg{
		Ret: pb.Int32(0), IsOpen: pb.Bool(false),
	})
	p.hlHandleScheduleMsg(closed)
	if holyLandEventOpen(p) {
		t.Fatal("isOpen=false must close the gate (round ended)")
	}

	refused, _ := pb.Marshal(&proto.HolyLandBattleApplyDataSyncMsg{
		Ret: pb.Int32(1), IsOpen: pb.Bool(true),
	})
	p.hlHandleScheduleMsg(refused)
	if p.HlOpen || p.HlScheduleApply != nil {
		t.Fatal("ret!=0 must not trust isOpen (no schedule for us) and must drop anchors")
	}

	// Garbage answers must not corrupt the cache.
	p.hlHandleScheduleMsg(open) // open again
	p.hlHandleScheduleMsg([]byte{0xff, 0xff})
	if !holyLandEventOpen(p) {
		t.Fatal("unparsable payload must be ignored, not treated as closed")
	}

	// The ActList 346 season row alone can NEVER prove a running round —
	// announcement without day anchors stays hidden.
	bare := &Player{}
	setActivities(&bare.ActList, activityEntry{
		ActivityID: 777, Type: hlActivityTypeHolyLand,
		BeginTime: now.Add(-time.Hour).UnixMilli(), EndTime: now.Add(time.Hour).UnixMilli(),
	})
	if holyLandEventOpen(bare) {
		t.Fatal("actlist-346 window without schedule timestamps must not show")
	}
}

// A 2567 push (login + round boundaries) must trigger an immediate 209570
// re-request on the wire, so the gate flips within seconds.
func TestHolyLandSchedulePushNeverReasks(t *testing.T) {
	// Regression: the server pushes 2567 as an echo of EVERY 209570 answer.
	// Re-asking there hot-looped the schedule protocol on every regular
	// account with Cửu U open — pushes must be absorbed (no wire frames).
	ch := make(chan []byte, 4)
	p := &Player{writeChan: ch, UID: 7}
	for _, ret := range []int32{0, 1} {
		push, _ := pb.Marshal(&proto.HolyLandBattleTimeStampsDataSyncMsg{Ret: pb.Int32(ret)})
		p.hlHandleSchedulePushMsg(push)
	}
	if len(ch) != 0 {
		frames := hex.EncodeToString((<-ch)[6:10])
		t.Fatalf("push caused %d wire frame(s), first cmd=%s — re-ask must be gone", len(ch)+1, frames)
	}
}

// The push's day anchors are usable BEFORE the first 2562 answer (2567
// arrives at login) but the 2562 answer always wins the precedence, and a
// schedule-closed answer outranks everything (2026-10-05 season-row bug).
func TestHolyLandGateScheduleOutranksSeasonList(t *testing.T) {
	now := time.Now()
	p := &Player{}
	setActivities(&p.ActList, activityEntry{
		ActivityID: 777, Type: hlActivityTypeHolyLand,
		BeginTime: now.Add(-time.Hour).UnixMilli(), EndTime: now.Add(time.Hour).UnixMilli(),
	})
	push, _ := pb.Marshal(&proto.HolyLandBattleTimeStampsDataSyncMsg{
		Ret: pb.Int32(0), ScheduleTimestamp: []int64{hlLiveAnchor(now)},
	})
	p.hlHandleSchedulePushMsg(push)
	if !holyLandEventOpen(p) {
		t.Fatal("announcement + pushed anchors inside a window must open before the first 2562 answer")
	}

	closed, _ := pb.Marshal(&proto.HolyLandBattleApplyDataSyncMsg{
		Ret: pb.Int32(0), IsOpen: pb.Bool(false),
	})
	p.hlHandleScheduleMsg(closed)
	if holyLandEventOpen(p) {
		t.Fatal("schedule-closed must hide the event even with 346 active + pushed anchors")
	}

	open, _ := pb.Marshal(&proto.HolyLandBattleApplyDataSyncMsg{
		Ret: pb.Int32(0), IsOpen: pb.Bool(true), RoundId: pb.Int32(9),
		ScheduleTimestamp: []int64{hlLiveAnchor(now)},
	})
	p.hlHandleScheduleMsg(open)
	if !holyLandEventOpen(p) {
		t.Fatal("schedule-open + live window must show")
	}
}

func TestEventHolyLandGateScheduleOutranksSeasonList(t *testing.T) {
	now := time.Now()
	ep := &EventPlayer{}
	ep.hl = &holyLandState{}
	ep.hlOpen = true // hlHandleActivityList saw the 346 row
	if eventHlOpen(ep) || ep.HolyLandEventOpen() {
		t.Fatal("announcement alone (no day anchors) must not show")
	}
	// 2567 at login supplies the anchors before the first 2562 answer:
	ep.hl.ScheduleTs = []int64{hlLiveAnchor(now)}
	if !eventHlOpen(ep) || !ep.HolyLandEventOpen() {
		t.Fatal("announcement + live pushed window must open the gate")
	}
	ep.hl.OpenKnown = true // 209570 answered; IsOpen=false
	if eventHlOpen(ep) || ep.HolyLandEventOpen() {
		t.Fatal("schedule-closed must hide between rounds (the 2026-10-05 bug)")
	}
	ep.hl.IsOpen = true
	ep.hl.ScheduleApply = []int64{hlEndedAnchor(now)}
	if eventHlOpen(ep) || ep.HolyLandEventOpen() {
		t.Fatal("season isOpen with only ended windows must hide (the 2026-10-06 bug)")
	}
	ep.hl.ScheduleApply = []int64{hlLiveAnchor(now)}
	if !eventHlOpen(ep) || !ep.HolyLandEventOpen() {
		t.Fatal("season isOpen inside a live window must show")
	}
}
