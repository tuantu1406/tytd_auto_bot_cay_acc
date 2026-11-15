package tytd_go

// Offline tests for the async ⚡ TĂNG TỐC path (owner 2026-10-21:
// "gửi async, chỉ việc bắn lên server game, response nhận sau, acc nào lỗi
// thì alert nhẹ… không lỗi thì thôi" after the "20 acc bấm tăng tốc, chỉ
// 3-4 acc chạy" report). Pins: one press = one frame; a repeat press inside
// giAccelDedupGap is swallowed (its answer is still in flight — the old
// frantic re-click echoed the stale baseline and drew silent refusals); a
// 1ac1 refusal now FOLDS the server's speedUpCount (self-heal of a drifted
// baseline — the old handler dropped ret≠0 with zero trace), clears the
// press stamp, and raises exactly one light leader alert only for a press a
// human actually made.

import (
	"strings"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func giAccelEP(t *testing.T) (*EventPlayer, *giEvent) {
	t.Helper()
	p := &EventPlayer{Running: true, Username: "acccuoi", ChatID: 9002}
	p.writeChan = make(chan []byte, 32)
	p.NotiChan = make(chan NotifyMGS, 8)
	wireCtxEP(t, p)
	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	ev.Name = "Bồng Lai"
	ev.Moving = true
	ev.SpeedUp = 5
	return p, ev
}

// readAccelFrame pops one accelerate frame and returns its request.
func readAccelFrame(t *testing.T, p *EventPlayer) *proto.GodIslandGameAcclerateMoveReqMsg {
	t.Helper()
	select {
	case f := <-p.writeChan:
		if got := frameFuncType(t, f); got != GOD_ISLAND_ACCELERATE_CMD {
			t.Fatalf("frame funcType = %s, want %s", got, GOD_ISLAND_ACCELERATE_CMD)
		}
		var req proto.GodIslandGameAcclerateMoveReqMsg
		if pb.Unmarshal(f[18:], &req) != nil {
			t.Fatal("bad accelerate request payload")
		}
		return &req
	default:
		t.Fatal("expected an accelerate frame, socket is silent")
		return nil
	}
}

func giAccelAnswer(t *testing.T, p *EventPlayer, ev *giEvent, ret int32, count *int32) {
	t.Helper()
	p.giHandleAccelerateResp(ev, mustMarshal(t, &proto.GodIslandGameAcclerateMoveRespMsg{
		Ret:          ret,
		SpeedUpCount: count,
	}))
}

func TestGodIslandAccelerateAsyncAlert(t *testing.T) {
	p, ev := giAccelEP(t)

	// 1. press → one frame, current baseline echoed.
	if err := p.GodIslandAccelerate(GIEventNormal); err != nil {
		t.Fatalf("press: %v", err)
	}
	if req := readAccelFrame(t, p); req.GetSpeedUpCount() != 5 || req.GetActivityId() != 42 {
		t.Fatalf("frame count=%d act=%d, want 5/42", req.GetSpeedUpCount(), req.GetActivityId())
	}

	// 2. frantic double press inside the gap → silent, no second frame
	//    (the old shape re-clicked the stale baseline into a refusal).
	if err := p.GodIslandAccelerate(GIEventNormal); err != nil {
		t.Fatalf("dedup press must be silent-nil, got %v", err)
	}
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("dedup failed: %d extra frame(s) on the wire", n)
	}

	// 3. REFUSAL with the server's echo → count folds ANYWAY (self-heal),
	//    the stamp clears, and the leader gets ONE light alert.
	giAccelAnswer(t, p, ev, 7, pb.Int32(9))
	if ev.SpeedUp != 9 {
		t.Fatalf("SpeedUp=%d after refused echo, want 9 (refusals must self-heal the baseline)", ev.SpeedUp)
	}
	select {
	case n := <-p.NotiChan:
		if !strings.Contains(n.MSG, "ret=7") || !strings.Contains(n.MSG, "Bồng Lai") || n.ChatID != 9002 {
			t.Fatalf("alert %q chat %d, want ret=7 + island name + leader chat", n.MSG, n.ChatID)
		}
	default:
		t.Fatal("human press refused but no leader alert")
	}
	if len(p.NotiChan) != 0 {
		t.Fatal("alert must be ONE line, not spam")
	}

	// 4. answer closed the press → an immediate re-press fires with the
	//    healed count.
	if err := p.GodIslandAccelerate(GIEventNormal); err != nil {
		t.Fatalf("post-answer press: %v", err)
	}
	if req := readAccelFrame(t, p); req.GetSpeedUpCount() != 9 {
		t.Fatalf("re-press count=%d, want the healed 9", req.GetSpeedUpCount())
	}

	// 5. a refusal with NO recent human press (engine noise) → log only,
	//    never an alert.
	ev.AccelPressAt = time.Now().Add(-giAccelAlertWindow - time.Second)
	giAccelAnswer(t, p, ev, 3, nil)
	select {
	case n := <-p.NotiChan:
		t.Fatalf("stray refusal alerted the leader: %q", n.MSG)
	default:
	}

	// 6. standing army → immediate, visible refusal (dispatch-time error,
	//    still returned synchronously so the caller can act on it).
	ev.Moving = false
	ev.AccelPressAt = time.Time{}
	if err := p.GodIslandAccelerate(GIEventNormal); err == nil ||
		!strings.Contains(err.Error(), "không hành quân") {
		t.Fatalf("standing-army press err=%v, want 'không hành quân'", err)
	}
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("standing army still burned %d frame(s)", n)
	}
}
