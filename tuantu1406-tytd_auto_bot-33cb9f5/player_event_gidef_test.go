package tytd_go

// Offline tests for the def-nha engine (player_event_gidef.go). Harness is
// the GI wire pattern: bare EventPlayer + writeChan + wireCtxEP, then call
// p.giDefHome(which) directly (godIslandStay just calls it per tick). The
// registry is package-level, so every test uses its own ChatID arena.

import (
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// giDefResetArena wipes the package-level registry row for one chat: the
// arena lives across tests (and across -count reruns), so every test starts
// from a clean slate or leftover wants/lastAct from a previous run would
// block pickTurn and hang the raw <-writeChan reads.
func giDefResetArena(chatID int64) {
	giDefReg.mu.Lock()
	delete(giDefReg.byChat, giDefKey{chatID, GIEventNormal})
	giDefReg.mu.Unlock()
}

// giDefTestEP builds one connected account parked in an arena: dead unless
// the caller flips PowerCur, home 6400, per-draw ball ceiling 200.
func giDefTestEP(t *testing.T, chatID int64, username string) (*EventPlayer, *giEvent) {
	t.Helper()
	giDefResetArena(chatID) // construction order is before any registration — safe to wipe
	p := &EventPlayer{Running: true, Username: username, ServerID: 7, ChatID: chatID}
	p.writeChan = make(chan []byte, 32)
	wireCtxEP(t, p)
	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	ev.Name = "Bồng Lai"
	ev.Home = 6400
	// alive ⇔ location!=0 (live-proven: corpses read location=0), so a
	// freshly-built account stands AT HOME.
	ev.Location = 6400
	ev.PowerKnown = true
	ev.PowerCur = 1000
	ev.PowerMax = 1000 // owner-fixed revive bar = 30% = 300
	ev.BallKnown = true
	ev.BallCur = 500
	ev.BallMax = 200
	return p, ev
}

// giDefArm marks the home as fighting right now (1ace fold shape).
func giDefArm(ev *giEvent) {
	ev.FightSeen = map[int32]int64{6400: time.Now().UnixMilli()}
}

// giDefKill parks the unit as a corpse at home, cooldown passed. DEAD ⇔
// location==0 (live-proven), not curPower==0.
func giDefKill(ev *giEvent) {
	ev.PowerCur = 0
	ev.Location = 0
	ev.Moving = false
	ev.DeadCity = 6400
	ev.ReviveTime = 0
}

// giDefReadFrame pops one frame and asserts it is one of our engine calls,
// unmarshalling its payload.
func giDefReadFrame(t *testing.T, p *EventPlayer, wantCmd string) []byte {
	t.Helper()
	select {
	case f := <-p.writeChan:
		if got := frameFuncType(t, f); got != wantCmd {
			t.Fatalf("frame funcType = %s, want %s", got, wantCmd)
		}
		return f[18:]
	default:
		t.Fatalf("expected a %s frame, socket is silent", wantCmd)
		return nil
	}
}

func giDefAssertSilent(t *testing.T, p *EventPlayer, why string) {
	t.Helper()
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("%s: expected silence, got %d frame(s)", why, n)
	}
}

// Sip ladder: the picked corpse drinks min(sip, reservoir, per-draw) and
// never a full draw; the peer corpse stays silent inside the act gap.
func TestGIDefSipOncePerTurn(t *testing.T) {
	a, evA := giDefTestEP(t, 9101, "aaa")
	b, evB := giDefTestEP(t, 9101, "bbb")
	giDefArm(evA)
	giDefArm(evB)
	giDefKill(evA)
	giDefKill(evB)

	a.giDefHome(GIEventNormal) // aaa oldest want (first registration) → sip
	giDefReadFrame(t, a, GOD_ISLAND_USE_BALL_CMD)
	giDefAssertSilent(t, b, "peer inside act gap must wait")

	// aaa already acted this window — its own re-tick is throttled too.
	a.giDefHome(GIEventNormal)
	giDefAssertSilent(t, a, "own action gap must throttle")

	// Open the chat-wide window and confirm ROTATION: bbb, just now
	// registered, must be picked even though aaa still waits (never wake
	// the same account twice in a row).
	dc := giDefChatFor(9101, GIEventNormal)
	dc.mu.Lock()
	dc.lastAct = time.Time{}
	dc.mu.Unlock()
	a.giDefHome(GIEventNormal)
	giDefAssertSilent(t, a, "just-woken account must yield while others wait")
	b.giDefHome(GIEventNormal)
	f := <-b.writeChan
	if got := frameFuncType(t, f); got != GOD_ISLAND_USE_BALL_CMD {
		t.Fatalf("bbb frame = %s, want sip", got)
	}
	var req proto.GodIslandGameUseSpiritualBallReqMsg
	if err := pb.Unmarshal(f[18:], &req); err != nil {
		t.Fatal(err)
	}
	// top-up to the owner-fixed 30% bar: 300−0, reservoir 500, per-draw
	// ceiling 200 → 200 (reservoir/ceiling clamp, never more than the bar).
	if req.GetUsePower() != 200 || req.GetActivityId() != 42 {
		t.Fatalf("sip = %d (act %d), want 200/42", req.GetUsePower(), req.GetActivityId())
	}
}

// Sip is capped by the reservoir and the per-draw ceiling.
func TestGIDefSipCaps(t *testing.T) {
	p, ev := giDefTestEP(t, 9102, "cap")
	giDefArm(ev)
	giDefKill(ev)
	ev.BallCur = 30
	ev.BallMax = 10 // per-draw ceiling below the reservoir
	p.giDefHome(GIEventNormal)
	f := <-p.writeChan
	var req proto.GodIslandGameUseSpiritualBallReqMsg
	if err := pb.Unmarshal(f[18:], &req); err != nil {
		t.Fatal(err)
	}
	if req.GetUsePower() != 10 {
		t.Fatalf("sip = %d, want per-draw cap 10", req.GetUsePower())
	}
}

// Dry ball → exactly ONE fruit bite per gap (206837, useCount 1), no sip.
func TestGIDefFruitWhenBallDry(t *testing.T) {
	p, ev := giDefTestEP(t, 9103, "dry")
	giDefArm(ev)
	giDefKill(ev)
	ev.BallCur = 0
	p.giDefHome(GIEventNormal)
	f := <-p.writeChan
	if got := frameFuncType(t, f); got != GOD_ISLAND_USE_FRUIT_CMD {
		t.Fatalf("frame = %s, want fruit", got)
	}
	var req proto.GodIslandUseFruitReq
	if err := pb.Unmarshal(f[18:], &req); err != nil {
		t.Fatal(err)
	}
	if req.GetUseCount() != 1 || req.GetActivityId() != 42 {
		t.Fatalf("fruit req = %d/%d, want 1/42", req.GetActivityId(), req.GetUseCount())
	}
	// Same window: fruit throttle (and act gap) must mute the re-tick.
	p.giDefHome(GIEventNormal)
	giDefAssertSilent(t, p, "fruit gap must throttle")
	// Ball refilled by the 1ab5 → next round sips instead of biting. Open
	// BOTH gates: the chat-wide window and this account's own act gap.
	dc := giDefChatFor(9103, GIEventNormal)
	dc.mu.Lock()
	dc.lastAct = time.Time{}
	dc.mu.Unlock()
	ev.DefLastAct = time.Time{}
	ev.BallCur = 400
	p.giDefHome(GIEventNormal)
	if got := frameFuncType(t, <-p.writeChan); got != GOD_ISLAND_USE_BALL_CMD {
		t.Fatal("expected sip after refill")
	}
}

// A live ally standing at home freezes every corpse ("có người sống trong
// base thì đợi").
func TestGIDefHolderFreezesCorpses(t *testing.T) {
	h, evH := giDefTestEP(t, 9104, "holder")
	d, evD := giDefTestEP(t, 9104, "dead")
	giDefArm(evH)
	giDefArm(evD)
	evH.PowerCur = 300
	evH.Location = 6400
	giDefKill(evD)

	h.giDefHome(GIEventNormal) // stamps holder
	giDefAssertSilent(t, h, "alive holder has nothing to do at home")
	d.giDefHome(GIEventNormal)
	giDefAssertSilent(t, d, "corpses wait while a holder stands at home")
}

// Peace at home (no fresh 1ace flag) → total silence even for corpses.
func TestGIDefSilentWhenPeace(t *testing.T) {
	p, ev := giDefTestEP(t, 9105, "peace")
	giDefKill(ev) // FightSeen empty
	p.giDefHome(GIEventNormal)
	giDefAssertSilent(t, p, "no fight flag → no actions")
}

// Revive cooldown still running → the ball would be refused (ret=6852):
// the engine must not drink yet.
func TestGIDefSilentDuringCooldown(t *testing.T) {
	p, ev := giDefTestEP(t, 9106, "cd")
	giDefArm(ev)
	giDefKill(ev)
	ev.ReviveTime = time.Now().Add(60 * time.Second).UnixMilli()
	p.giDefHome(GIEventNormal)
	giDefAssertSilent(t, p, "countdown not passed → no sip")
}

// The bug the live probe exposed: a corpse that already holds SOME HP
// (location==0, cur < the 30% bar) is still DEAD and must keep getting
// sipped up to the bar — and a STANDING low-HP unit (location!=0) must
// never be heal-queued. No march-home frame is ever legal (revive lands
// at base by itself — owner: "sẽ tự về base ngay").
func TestGIDefHungCorpseStillHeals(t *testing.T) {
	p, ev := giDefTestEP(t, 9107, "walker")
	giDefArm(ev)
	giDefKill(ev)
	ev.PowerCur = 10 // half-healed corpse: dead (live: dead at 2000/11206)
	p.giDefHome(GIEventNormal)
	f := <-p.writeChan
	if got := frameFuncType(t, f); got != GOD_ISLAND_USE_BALL_CMD {
		t.Fatalf("frame = %s, want sip for a below-bar corpse", got)
	}
	var req proto.GodIslandGameUseSpiritualBallReqMsg
	if err := pb.Unmarshal(f[18:], &req); err != nil {
		t.Fatal(err)
	}
	// top-up: 300−10=290, reservoir 500, per-draw 200 → 200.
	if req.GetUsePower() != 200 {
		t.Fatalf("sip = %d, want 200 (top-up clamped by per-draw)", req.GetUsePower())
	}

	// At/above the bar the drink STOPS ("chỉ hồi vừa đủ"): 300 is enough.
	giDefResetArena(9107)
	p2, ev2 := giDefTestEP(t, 9107, "walker")
	giDefArm(ev2)
	giDefKill(ev2)
	ev2.PowerCur = 300
	p2.giDefHome(GIEventNormal)
	giDefAssertSilent(t, p2, "corpse AT the bar must not be topped up")

	// A LOW-HP but STANDING unit (location!=0) is a live defender, not a
	// heal candidate — and it must not be marched anywhere.
	giDefResetArena(9107)
	p3, ev3 := giDefTestEP(t, 9107, "walker")
	giDefArm(ev3)
	ev3.PowerCur = 10
	ev3.Location = 6400
	p3.giDefHome(GIEventNormal)
	giDefAssertSilent(t, p3, "standing low-HP unit must be left alone")
}

// Home unknown (first INFO_LOADs) → never defend an assumed city.
func TestGIDefNoHomeNoAction(t *testing.T) {
	p, ev := giDefTestEP(t, 9108, "blind")
	giDefKill(ev)
	ev.FightSeen = map[int32]int64{6400: time.Now().UnixMilli()}
	ev.Home = 0
	p.giDefHome(GIEventNormal)
	giDefAssertSilent(t, p, "Home==0 must not emit anything")
}

// A corpse that died at ANOTHER city (or unknown, not last-seen at home)
// is not a def candidate — clearing it also keeps the queue honest.
func TestGIDefOnlyRevivesHomeDeaths(t *testing.T) {
	p, ev := giDefTestEP(t, 9109, "away")
	giDefArm(ev)
	giDefKill(ev)
	ev.DeadCity = 3001 // died elsewhere
	p.giDefHome(GIEventNormal)
	giDefAssertSilent(t, p, "death away from home is not def work")

	dc := giDefChatFor(9109, GIEventNormal)
	dc.mu.Lock()
	n := len(dc.wants)
	dc.mu.Unlock()
	if n != 0 {
		t.Fatalf("non-def corpse registered %d want(s)", n)
	}
}
