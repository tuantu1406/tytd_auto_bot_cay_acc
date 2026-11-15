package tytd_go

import (
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"
	"tytd_go/internal/db"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Gather Energy (tụ linh trận): the time-of-day join rules, the ad/reward
// budget, and the list → inside → attend chain.

func TestGatherPhaseFor(t *testing.T) {
	for _, tc := range []struct {
		mins int
		want gatherPhase
	}{
		{0, gatherClosed}, {599, gatherClosed},
		{600, gatherEarly}, {1289, gatherEarly},
		{1290, gatherLate}, {1318, gatherLate},
		{1319, gatherDeadline},
		{1320, gatherClosed}, {1439, gatherClosed},
	} {
		if got := gatherPhaseFor(tc.mins); got != tc.want {
			t.Fatalf("phase(%d)=%v want %v", tc.mins, got, tc.want)
		}
	}
}

func TestGatherPickRules(t *testing.T) {
	ictNow := time.Date(2026, 9, 24, 12, 0, 0, 0, ictZone)
	cutoff := gatherCutoffToday(ictNow)
	nowMs := ictNow.UnixMilli()
	const myID = int64(42)

	mk := func(id, income, endDelta int64, size int32, attend bool, opener int64) gatherEntry {
		return gatherEntry{id: id, income: income, endTimeMs: nowMs + endDelta*1000,
			listSize: size, isAttend: attend, openerID: opener}
	}
	okEnd := (cutoff-nowMs)/1000 - 60 // seconds from now that lands comfortably before 21:30

	// Early phase: only sub-21:30 formations, not mine, not full, not joined.
	entries := []gatherEntry{
		mk(1, 100, okEnd, 4, false, 7),               // eligible
		mk(2, 900, (cutoff-nowMs)/1000, 4, false, 7), // ends exactly 21:30 — strictly before is required
		mk(3, 800, okEnd, 4, false, myID),            // my own
		mk(4, 700, okEnd, 5, false, 7),               // full
		mk(5, 600, okEnd, 4, true, 7),                // already joined
		mk(6, 50, okEnd, 0, false, 7),                // eligible, weaker
	}
	if got := gatherPick(entries, myID, gatherEarly, cutoff, nowMs, 0); got != 1 {
		t.Fatalf("early pick=%d, want 1", got)
	}

	// Income tie → later endTime (both still before 21:30).
	entries = []gatherEntry{mk(1, 100, okEnd-3600, 4, false, 7), mk(2, 100, okEnd, 4, false, 7)}
	if got := gatherPick(entries, myID, gatherEarly, cutoff, nowMs, 0); got != 2 {
		t.Fatalf("tie pick=%d, want 2 (later end)", got)
	}

	// Late phase (21:30–21:59): strictly more than 10 h left, but never more
	// than 24 h — a joined slot must end within a day. mk() builds endTimeMs
	// from nowMs, so re-anchor it at 21:40 for these entries.
	late := time.Date(2026, 9, 24, 21, 40, 0, 0, ictZone)
	lnow := late.UnixMilli()
	nowMs = lnow
	entries = []gatherEntry{
		mk(1, 500, 10*3600, 4, false, 7),   // exactly 10 h → not eligible
		mk(2, 100, 10*3600+1, 4, false, 7), // 10 h + 1 s → eligible
		mk(3, 400, 3600, 4, false, 7),      // one hour left → not eligible
		mk(4, 900, 24*3600+1, 4, false, 7), // 24 h + 1 s → the cap refuses it
	}
	if got := gatherPick(entries, myID, gatherLate, cutoff, lnow, 0); got != 2 {
		t.Fatalf("late pick=%d, want 2 (10 h floor + 24 h cap)", got)
	}

	// Exactly 24 h left is inside the cap ("duration <= 24h").
	entries = []gatherEntry{mk(1, 100, 24*3600, 4, false, 7)}
	if got := gatherPick(entries, myID, gatherLate, cutoff, lnow, 0); got != 1 {
		t.Fatalf("late pick=%d, want 1 (24 h exactly joins)", got)
	}

	// Deadline minute: the 10 h floor is gone, best income wins — but the
	// 24 h cap still stands.
	entries = []gatherEntry{mk(1, 500, 600, 4, false, 7), mk(2, 300, 10*3600+1, 4, false, 7)}
	if got := gatherPick(entries, myID, gatherDeadline, cutoff, lnow, 0); got != 1 {
		t.Fatalf("deadline pick=%d, want 1", got)
	}
	entries = []gatherEntry{mk(1, 500, 25*3600, 4, false, 7), mk(2, 300, 600, 4, false, 7)}
	if got := gatherPick(entries, myID, gatherDeadline, cutoff, lnow, 0); got != 2 {
		t.Fatalf("deadline pick=%d, want 2 (over-24h candidate skipped)", got)
	}

	// Closed board: never picks.
	if got := gatherPick(entries, myID, gatherClosed, cutoff, lnow, 0); got != 0 {
		t.Fatalf("closed pick=%d, want 0", got)
	}

	// Income floor (/tu_linh_min): below-floor entries are skipped even when
	// eligible; income == floor joins ("từ mức này"); a floor above every
	// entry means nothing to join. Deadline phase keeps the time bar out of
	// the way so only the floor decides.
	entries = []gatherEntry{mk(1, 100, 600, 4, false, 7), mk(2, 90, 600, 4, false, 7)}
	if got := gatherPick(entries, myID, gatherDeadline, cutoff, lnow, 95); got != 1 {
		t.Fatalf("floor95 pick=%d, want 1", got)
	}
	if got := gatherPick(entries, myID, gatherDeadline, cutoff, lnow, 100); got != 1 {
		t.Fatalf("floor100 pick=%d, want 1 (equal to floor joins)", got)
	}
	if got := gatherPick(entries, myID, gatherDeadline, cutoff, lnow, 120); got != 0 {
		t.Fatalf("floor120 pick=%d, want 0 (all below floor)", got)
	}
}

func gatherSeeded(t *testing.T, now time.Time) *Player {
	t.Helper()
	p := &Player{Username: "u", UID: 42, writeChan: make(chan []byte, 16), AutoGather: true}
	wireCtx(t, p)
	p.gatherMu.Lock()
	p.gatherKnown, p.gatherDay = true, now.Format("2006-01-02")
	p.gatherEnterAt, p.gatherTotal = now, 1
	p.gatherAdTimes = gatherAdLimit // tests opt into ad sends explicitly
	p.gatherMu.Unlock()
	return p
}

func TestGatherTickWires(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, ictZone)
	p := gatherSeeded(t, now)
	p.gatherMu.Lock()
	p.gatherAdTimes = 0
	p.gatherPending = []int64{77}
	p.gatherMu.Unlock()

	p.gatherTick(now)

	// Order inside one tick: claim, ad, list.
	frames := [][]byte{<-p.writeChan, <-p.writeChan, <-p.writeChan}
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("tick sent %d extra frames", n)
	}
	if ft := frameFuncType(t, frames[0]); ft != GATHER_GET_REWARD_CMD {
		t.Fatalf("frame0 %s, want claim", ft)
	}
	if got := hex.EncodeToString(frames[0][18:]); got != "084d" { // id 77
		t.Fatalf("claim payload %s, want 084d", got)
	}
	if ft := frameFuncType(t, frames[1]); ft != GATHER_GET_AD_AWARD_CMD {
		t.Fatalf("frame1 %s, want ad award", ft)
	}
	if got := hex.EncodeToString(frames[1][18:]); got != "0800" { // isUseADTime=false, explicit
		t.Fatalf("ad payload %s, want 0800", got)
	}
	if ft := frameFuncType(t, frames[2]); ft != GATHER_FIRST_LIST_VIEW_CMD {
		t.Fatalf("frame2 %s, want first list", ft)
	}
	if got := hex.EncodeToString(frames[2][18:]); got != "08001000" { // offset=0 filterType=0 explicit
		t.Fatalf("list payload %s, want 08001000", got)
	}

	// Same minute again: every cooldown still holds — zero frames.
	p.gatherTick(now.Add(time.Second))
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("repeat tick re-sent %d frames", n)
	}
}

func TestGatherTickGates(t *testing.T) {
	closed := time.Date(2026, 9, 24, 23, 0, 0, 0, ictZone)
	p := gatherSeeded(t, closed)
	// Overnight: no list scan…
	p.gatherTick(closed)
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("closed board sent %d frames", n)
	}
	// …but queued rewards still get claimed (handled in the next test).

	// Ad budget exhausted: no ad frame even in the window.
	day := time.Date(2026, 9, 24, 12, 0, 0, 0, ictZone)
	p = gatherSeeded(t, day) // gatherAdTimes already at the cap
	p.gatherTick(day)
	for len(p.writeChan) > 0 {
		if ft := frameFuncType(t, <-p.writeChan); ft == GATHER_GET_AD_AWARD_CMD {
			t.Fatal("ad claim sent past the 3/day cap")
		}
	}

	// Stale state (new ICT day): ENTER_NEW re-sync goes out, empty body.
	p = gatherSeeded(t, day)
	p.gatherMu.Lock()
	p.gatherDay = "2000-01-01"
	p.gatherMu.Unlock()
	p.gatherTick(day)
	f := <-p.writeChan
	if ft := frameFuncType(t, f); ft != GATHER_ENTER_NEW_CMD {
		t.Fatalf("frame %s, want enter resync", ft)
	}
	if len(f) != 18 {
		t.Fatalf("enter body not empty: %s", hex.EncodeToString(f[18:]))
	}
}

func TestGatherRewardQueueOvernight(t *testing.T) {
	now := time.Date(2026, 9, 24, 23, 0, 0, 0, ictZone)
	p := gatherSeeded(t, now)

	push, _ := pb.Marshal(&proto.GatherEnergyRewardShowResp{
		Reward: &proto.GatherEnergyRewardMsg{Id: pb.Int64(55)},
	})
	p.gatherMu.Lock()
	p.gatherAttend = 1
	p.gatherMu.Unlock()
	p.gatherHandleRewardShow(push)

	p.gatherMu.Lock()
	pend, attend := append([]int64(nil), p.gatherPending...), p.gatherAttend
	p.gatherMu.Unlock()
	if len(pend) != 1 || pend[0] != 55 {
		t.Fatalf("pending after push: %v", pend)
	}
	if attend != 0 {
		t.Fatalf("seat not released: attend=%d", attend)
	}

	// Closed board overnight — the claim still goes out.
	p.gatherTick(now)
	f := <-p.writeChan
	if ft := frameFuncType(t, f); ft != GATHER_GET_REWARD_CMD {
		t.Fatalf("frame %s, want claim", ft)
	}

	claimOK, _ := pb.Marshal(&proto.GatherEnergyRewardResp{Ret: pb.Int32(0), Id: pb.Int64(55)})
	p.gatherHandleClaim(claimOK)
	p.gatherMu.Lock()
	n := len(p.gatherPending)
	p.gatherMu.Unlock()
	if n != 0 {
		t.Fatalf("claim did not pop the queue: %d left", n)
	}
	p.gatherTick(now.Add(time.Second))
	if len(p.writeChan) != 0 {
		t.Fatal("claim re-sent after settling")
	}
}

func gatherListFrame(t *testing.T, id, income, endMs int64) []byte {
	t.Helper()
	b, err := pb.Marshal(&proto.GatherEnergyFirstListViewResp{
		List: []*proto.GatherEnergyFirstListMsg{{
			EnergyBaseMsg: &proto.GatherEnergyListBaseMsg{
				Id: pb.Int64(id), Income: pb.Int64(income), EndTime: pb.Int64(endMs),
			},
			OpenerMsg: &proto.GatherEnergyOpenerMsg{PlayerId: pb.Int64(7)},
			ListSize:  pb.Int32(2),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGatherListQueuesInsideCheck(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, ictZone)
	p := gatherSeeded(t, now)
	p.gatherHandleList(gatherListFrame(t, 900, 500, gatherCutoffToday(now)-60000), now)
	f := <-p.writeChan
	if ft := frameFuncType(t, f); ft != GATHER_INSIDE_VIEW_CMD {
		t.Fatalf("frame %s, want inside view", ft)
	}
	if got := hex.EncodeToString(f[18:]); got != "088407" { // id 900
		t.Fatalf("inside payload %s, want 088407", got)
	}
	if p.gatherWantID != 900 {
		t.Fatalf("want=%d, want 900", p.gatherWantID)
	}

	// Rejected list: no follow-up at all.
	bad, _ := pb.Marshal(&proto.GatherEnergyFirstListViewResp{Ret: pb.Int32(9)})
	p.gatherMu.Lock()
	p.gatherWantID = 0
	p.gatherMu.Unlock()
	p.gatherHandleList(bad, now)
	if len(p.writeChan) != 0 {
		t.Fatal("rejected list produced a frame")
	}
}

func TestGatherInsideGate(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, ictZone)
	cutoff := gatherCutoffToday(now)

	inside := func(endMs int64, team []*proto.GatherEnergyTeamMsgNew, dislodgeMs int64) []byte {
		b, err := pb.Marshal(&proto.GatherEnergyInsideViewNewResp{
			DetailInfo: &proto.GatherEnergyDetailListMsg{
				EnergyBaseMsg: &proto.GatherEnergyListBaseMsg{
					Id: pb.Int64(900), Income: pb.Int64(500), EndTime: pb.Int64(endMs),
				},
				TeamList: team,
			},
			DislodgedTime: pb.Int64(dislodgeMs),
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	slot := func(idx int32, who int64) *proto.GatherEnergyTeamMsgNew {
		return &proto.GatherEnergyTeamMsgNew{
			Index: pb.Int32(idx), PlayerInfo: &proto.GatherEnergyInsidePlayerMsg{PlayerId: pb.Int64(who)},
		}
	}
	newWant := func() *Player {
		p := gatherSeeded(t, now)
		p.gatherMu.Lock()
		p.gatherWantID, p.gatherWantAt = 900, now
		p.gatherMu.Unlock()
		return p
	}
	pollFrames := func(p *Player) []string {
		var out []string
		for len(p.writeChan) > 0 {
			out = append(out, frameFuncType(t, <-p.writeChan))
		}
		return out
	}

	// Formation no longer matches the early bar (ends after 21:30): silent
	// drop, want cleared, no attend.
	p := newWant()
	p.gatherHandleInside(inside(cutoff+3600_000, nil, 0), now)
	if got := pollFrames(p); len(got) != 0 {
		t.Fatalf("stale candidate attended anyway: %v", got)
	}
	if p.gatherWantID != 0 {
		t.Fatal("want not cleared after stale check")
	}

	// Valid: free slot among 1–4 (index 0 is the opener's own seat).
	p = newWant()
	p.gatherHandleInside(inside(cutoff-60_000, []*proto.GatherEnergyTeamMsgNew{
		slot(0, 7), slot(1, 8), slot(2, 9),
	}, 0), now)
	if got := pollFrames(p); len(got) != 1 || got[0] != GATHER_ATTEND_NEW_CMD {
		t.Fatalf("valid formation did not attend: %v", got)
	}

	// Full formation: attend blocked.
	p = newWant()
	p.gatherHandleInside(inside(cutoff-60_000, []*proto.GatherEnergyTeamMsgNew{
		slot(0, 7), slot(1, 8), slot(2, 9), slot(3, 10), slot(4, 11),
	}, 0), now)
	if got := pollFrames(p); len(got) != 0 {
		t.Fatalf("full formation attended: %v", got)
	}

	// Already a member: no attend — the server's teamList is the guard, and
	// the next scan won't re-pick it because the list row says isAttend.
	p = newWant()
	p.gatherHandleInside(inside(cutoff-60_000, []*proto.GatherEnergyTeamMsgNew{
		slot(0, 7), slot(1, 8), slot(2, 42),
	}, 0), now)
	if got := pollFrames(p); len(got) != 0 {
		t.Fatalf("double membership attended: %v", got)
	}
	if p.gatherWantID != 0 {
		t.Fatal("want not cleared after membership check")
	}

	// Dislodged 1 minute ago: the 5-minute rejoin cooldown holds.
	p = newWant()
	p.gatherHandleInside(inside(cutoff-60_000, nil, now.UnixMilli()-60_000), now)
	if got := pollFrames(p); len(got) != 0 {
		t.Fatalf("attended inside rejoin cooldown: %v", got)
	}

	// Seat budget full (joined one of one): inside must not attend either.
	p = newWant()
	p.gatherMu.Lock()
	p.gatherAttend, p.gatherTotal = 1, 1
	p.gatherMu.Unlock()
	p.gatherHandleInside(inside(cutoff-60_000, nil, 0), now)
	if got := pollFrames(p); len(got) != 0 {
		t.Fatalf("attended with attendNum==totalAttendNum: %v", got)
	}
}

func TestGatherAttendBooksState(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, ictZone)
	p := gatherSeeded(t, now)
	p.gatherMu.Lock()
	p.gatherWantID, p.gatherWantAt = 900, now
	p.gatherMu.Unlock()

	ok, _ := pb.Marshal(&proto.GatherEnergyAttendNewResp{Ret: pb.Int32(0)})
	p.gatherHandleAttend(ok)
	p.gatherMu.Lock()
	want, attend := p.gatherWantID, p.gatherAttend
	p.gatherMu.Unlock()
	if want != 0 || attend != 1 {
		t.Fatalf("after success want=%d attend=%d", want, attend)
	}

	// A push saying that run ended frees one seat again.
	push, _ := pb.Marshal(&proto.GatherEnergyRewardShowResp{
		Reward: &proto.GatherEnergyRewardMsg{Id: pb.Int64(5)},
	})
	p.gatherHandleRewardShow(push)
	p.gatherMu.Lock()
	attend = p.gatherAttend
	p.gatherMu.Unlock()
	if attend != 0 {
		t.Fatalf("after finish attend=%d, want 0", attend)
	}
}

// The server decides how many formations one player may hold at once
// (ENTER_NEW.totalAttendNum, usually 1, 2 with the extra slots) — a freed or
// second seat must restart the board scan, and holding one formation must
// never suppress it.
func TestGatherSecondSeatKeepsScanning(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, ictZone)
	p := gatherSeeded(t, now)
	p.gatherMu.Lock()
	p.gatherTotal, p.gatherAttend, p.gatherAdTimes = 2, 1, gatherAdLimit
	p.gatherMu.Unlock()

	// One of two seats held → still scan the board for the second join.
	p.gatherTick(now)
	seen := false
	for len(p.writeChan) > 0 {
		if ft := frameFuncType(t, <-p.writeChan); ft == GATHER_FIRST_LIST_VIEW_CMD {
			seen = true
		}
	}
	if !seen {
		t.Fatal("board not scanned while a second join seat is free")
	}

	// Both seats full → scan stops again.
	p.gatherMu.Lock()
	p.gatherAttend, p.gatherListAt, p.gatherEnterAt = 2, now.Add(-time.Hour), now
	p.gatherMu.Unlock()
	p.gatherTick(now.Add(time.Minute))
	for len(p.writeChan) > 0 {
		if ft := frameFuncType(t, <-p.writeChan); ft == GATHER_FIRST_LIST_VIEW_CMD {
			t.Fatal("board scanned with both seats used")
		}
	}

	// One formation ends → the freed seat goes looking for a replacement.
	push, _ := pb.Marshal(&proto.GatherEnergyRewardShowResp{
		Reward: &proto.GatherEnergyRewardMsg{Id: pb.Int64(7)},
	})
	p.gatherHandleRewardShow(push)
	p.gatherMu.Lock()
	p.gatherClaimAt = now.Add(-time.Hour) // let the queued claim go, we only watch the list here
	p.gatherMu.Unlock()
	p.gatherTick(now.Add(2 * time.Minute))
	seen = false
	for len(p.writeChan) > 0 {
		if ft := frameFuncType(t, <-p.writeChan); ft == GATHER_FIRST_LIST_VIEW_CMD {
			seen = true
		}
	}
	if !seen {
		t.Fatal("freed second seat did not restart the scan")
	}
}

func TestGatherEnterInstallsAuthority(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, ictZone)
	p := &Player{Username: "u", UID: 42, writeChan: make(chan []byte, 16), AutoGather: true}
	wireCtx(t, p)
	enter, _ := pb.Marshal(&proto.GatherEnergyEnterNewResp{
		TotalAttendNum: pb.Int32(1),
		GatherEnergy: &proto.GatherEnergyMsg{
			AttendNum: pb.Int32(1),
			GetTimes:  pb.Int32(3),
		},
		Reward: []*proto.GatherEnergyRewardMsg{{Id: pb.Int64(31)}},
	})
	p.gatherHandleEnter(enter)

	p.gatherMu.Lock()
	known, total, attend, ad, pend := p.gatherKnown, p.gatherTotal, p.gatherAttend, p.gatherAdTimes, len(p.gatherPending)
	p.gatherMu.Unlock()
	if !known || total != 1 || attend != 1 || ad != 3 || pend != 1 {
		t.Fatalf("enter not installed: known=%v total=%d attend=%d ad=%d pend=%d", known, total, attend, ad, pend)
	}

	// Server says all join seats are used → tick must not scan the board.
	p.gatherTick(now)
	for len(p.writeChan) > 0 {
		if ft := frameFuncType(t, <-p.writeChan); ft == GATHER_FIRST_LIST_VIEW_CMD {
			t.Fatal("board scanned while attendNum==totalAttendNum")
		}
	}
}

// The permanent Tụ Linh switch lives on the PLAYER OBJECT (owner 2026-10-27
// "tất cả các event chỉ được check trong object player"): /auto_tu_linh and
// the settings page patch the live session in place, so the toggle bites on
// the next minute without a re-login — and a shared game_uid (one game
// account configured by several people) can never make one session inherit
// another row's switch. The global /set_auto_tu_linh gate stays DB-backed.
func TestGatherEnabledSwitch(t *testing.T) {
	store, err := db.NewDB(filepath.Join(t.TempDir(), "data.dat"), getDK())
	if err != nil {
		t.Fatal(err)
	}
	g := store.GetDB()

	mkPlayer := func(uid uint64, on bool) *Player {
		pp := &Player{UID: uid, DB: g, writeChan: make(chan []byte, 16), AutoGather: on}
		wireCtx(t, pp) // "off must stay silent" only means something on a CONNECTED account
		return pp
	}

	if (&Player{}).gatherEnabled() {
		t.Fatal("zero-value Player must read as disabled (opt-in)")
	}
	// A DB row can no longer flip the switch — even one claiming ON under
	// the same game_uid (the multi-owner scenario the owner called out).
	if err := model.CreateUser(g, &model.User{ID: "9000000501", GameUID: 501, AutoGather: true}); err != nil {
		t.Fatal(err)
	}
	if mkPlayer(501, false).gatherEnabled() {
		t.Fatal("DB row must not enable a session whose field is off")
	}
	if !(&Player{AutoGather: true}).gatherEnabled() {
		t.Fatal("field on with nil DB must read as enabled (no per-account lookup left to hiccup)")
	}

	// Global gate (/set_auto_tu_linh), checked before every gather: closed
	// stops every account regardless of its own switch.
	model.SetConfig(g, model.ConfigKeyAutoTuLinh, "false")
	if mkPlayer(506, true).gatherEnabled() {
		t.Fatal("closed gate must disable gathering even for opted-in accounts")
	}
	model.SetConfig(g, model.ConfigKeyAutoTuLinh, "true")
	if !mkPlayer(507, true).gatherEnabled() {
		t.Fatal("reopened gate must let opted-in accounts gather again")
	}

	// Wire behavior: off → gatherSend stays silent; on → the frame goes out.
	off := mkPlayer(504, false)
	off.gatherSend(GATHER_FIRST_LIST_VIEW_CMD,
		&proto.GatherEnergyFirstListViewReq{Offset: pb.Int32(0), FilterType: pb.Int32(0)})
	select {
	case f := <-off.writeChan:
		t.Fatalf("switch off wrote a frame: %x", f)
	case <-time.After(50 * time.Millisecond):
	}

	on := mkPlayer(505, true)
	on.gatherSend(GATHER_FIRST_LIST_VIEW_CMD,
		&proto.GatherEnergyFirstListViewReq{Offset: pb.Int32(0), FilterType: pb.Int32(0)})
	select {
	case f := <-on.writeChan:
		if ft := frameFuncType(t, f); ft != GATHER_FIRST_LIST_VIEW_CMD {
			t.Fatalf("switch on sent %s, want board scan", ft)
		}
	case <-time.After(time.Second):
		t.Fatal("switch on sent nothing")
	}
}

// The /tu_linh_min income floor is read from the live DB at BOTH join gates:
// the board scan skips below-floor formations, and the inside view refuses
// to sit when the fresh income fell under the floor after the list ranked it.
func TestGatherIncomeFloor(t *testing.T) {
	store, err := db.NewDB(filepath.Join(t.TempDir(), "data.dat"), getDK())
	if err != nil {
		t.Fatal(err)
	}
	g := store.GetDB()
	if err := model.CreateUser(g, &model.User{
		ID: "9100000001", GameUID: 601, AutoGather: true, MinGatherIncome: 600,
	}); err != nil {
		t.Fatal(err)
	}
	p := &Player{Username: "u", UID: 601, DB: g, writeChan: make(chan []byte, 16), AutoGather: true}
	wireCtx(t, p)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, ictZone)
	cutoff := gatherCutoffToday(now)

	// List side: income 500 is below the 600 floor → no inside check queued.
	p.gatherHandleList(gatherListFrame(t, 900, 500, cutoff-60000), now)
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("below-floor formation queued %d frames", n)
	}

	// At the floor: the inside check goes out as before.
	p.gatherHandleList(gatherListFrame(t, 900, 600, cutoff-60000), now)
	if ft := frameFuncType(t, <-p.writeChan); ft != GATHER_INSIDE_VIEW_CMD {
		t.Fatalf("frame %s, want inside view", ft)
	}

	inside := func(income, endMs int64) []byte {
		b, err := pb.Marshal(&proto.GatherEnergyInsideViewNewResp{
			DetailInfo: &proto.GatherEnergyDetailListMsg{
				EnergyBaseMsg: &proto.GatherEnergyListBaseMsg{
					Id: pb.Int64(900), Income: pb.Int64(income), EndTime: pb.Int64(endMs),
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	want900 := func() {
		p.gatherMu.Lock()
		p.gatherWantID, p.gatherWantAt, p.gatherTotal = 900, now, 1
		p.gatherMu.Unlock()
	}
	drain := func() []string {
		var out []string
		for len(p.writeChan) > 0 {
			out = append(out, frameFuncType(t, <-p.writeChan))
		}
		return out
	}

	// Inside side: fresh income 500 < floor → no sit, want cleared.
	want900()
	p.gatherHandleInside(inside(500, cutoff-60000), now)
	if got := drain(); len(got) != 0 {
		t.Fatalf("below-floor formation attended: %v", got)
	}
	p.gatherMu.Lock()
	want := p.gatherWantID
	p.gatherMu.Unlock()
	if want != 0 {
		t.Fatal("want not cleared after below-floor check")
	}

	// At/above the floor: the sit proceeds.
	want900()
	p.gatherHandleInside(inside(600, cutoff-60000), now)
	if got := drain(); len(got) != 1 || got[0] != GATHER_ATTEND_NEW_CMD {
		t.Fatalf("at-floor formation did not attend: %v", got)
	}
}
