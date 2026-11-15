package tytd_go

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Unit tests for the Holy Land (Cửu U) integration. Wire helpers
// (mustMarshal, frameFuncType) live in player_event_godisland_test.go.

// The server's incoming transport id is decimal(full id) % 100000 rendered as
// %04x; pin every HolyLand pair declared in command.go so a fat-fingered hex
// constant fails here instead of silently mis-routing live frames. Push-only
// types (209550+) have no outgoing counterpart, so their full ids are literal.
func TestHLCmdTransportMap(t *testing.T) {
	// Outgoing request ids come from command.go (8-hex constants).
	hexPairs := []struct{ cmd, transport string }{
		{HOLY_LAND_BASE_INFO_CMD, SERVER_HOLY_LAND_BASE_INFO},
		{HOLY_LAND_HEART_BEAT_CMD, SERVER_HOLY_LAND_HEART_BEAT},
		{HOLY_LAND_GAME_INFO_CMD, SERVER_HOLY_LAND_GAME_INFO},
		{HOLY_LAND_SCHEDULE_CMD, SERVER_HOLY_LAND_SCHEDULE},
		{HOLY_LAND_MOVE_CMD, SERVER_HOLY_LAND_MOVE},
		{HOLY_LAND_MINIMAP_CMD, SERVER_HOLY_LAND_MINIMAP},
		{HOLY_LAND_ACCELERATE_CMD, SERVER_HOLY_LAND_ACCELERATE},
		{HOLY_LAND_BACK_HOME_CMD, SERVER_HOLY_LAND_BACK_HOME},
		{HOLY_LAND_ATTACK_CMD, SERVER_HOLY_LAND_ATTACK},
		{HOLY_LAND_AUTO_ATK_CMD, SERVER_HOLY_LAND_AUTO_ATK},
		{HOLY_LAND_WORSHIP_CMD, SERVER_HOLY_LAND_WORSHIP},
		{HOLY_LAND_RANK_REWARD_CMD, SERVER_HOLY_LAND_RANK_REWARD},
		{HOLY_LAND_CLAIM_REWARD_CMD, SERVER_HOLY_LAND_CLAIM_REWARD},
	}
	// Push-only types (no outgoing counterpart) pinned by their full decimal id.
	decPairs := []struct {
		fullId    int64
		transport string
	}{
		{209550, SERVER_HOLY_LAND_CITY_CHANGE},
		{209553, SERVER_HOLY_LAND_MYGAMEINFO},
		{209554, SERVER_HOLY_LAND_OCCUPY_CITY},
		{209555, SERVER_HOLY_LAND_LEFT_OUT},
		{209557, SERVER_HOLY_LAND_CITY_BATTLE},
		{209558, SERVER_HOLY_LAND_BEEN_KILL},
		{209559, SERVER_HOLY_LAND_ROUTE_INFO},
		{209560, SERVER_HOLY_LAND_AUTO_STOP},
		{209575, SERVER_HOLY_LAND_SCHEDULE_PUSH},
	}
	check := func(full int64, src, want string) {
		if got := fmt.Sprintf("%04x", full%100000); got != want {
			t.Errorf("%s -> transport %s, want %s", src, got, want)
		}
	}
	for _, pr := range hexPairs {
		full, err := strconv.ParseInt(pr.cmd, 16, 64)
		if err != nil {
			t.Fatalf("bad cmd %q: %v", pr.cmd, err)
		}
		check(full, pr.cmd, pr.transport)
	}
	for _, pr := range decPairs {
		check(pr.fullId, strconv.FormatInt(pr.fullId, 10), pr.transport)
	}
}

// proto3 drops explicit zero values unless the field is `optional`; the
// HolyLand requests rely on that presence trick (useItem=false must be ON the
// wire or the server can't tell free rebirth from item rebirth). Pin it.
func TestHLPresenceOnWire(t *testing.T) {
	reqs := []struct {
		name string
		msg  pb.Message
	}{
		{"back-home free", &proto.HolyLandGameBackHomeReq{UseItem: pb.Bool(false)}},
		{"auto-attack off", &proto.HolyLandGameAutoAttackReqMsg{TargetId: pb.Int32(0)}},
		{"accelerate base 0", &proto.HolyLandGameAcclerateMoveReqMsg{SpeedUpCount: pb.Int32(0)}},
	}
	for _, r := range reqs {
		data, err := pb.Marshal(r.msg)
		if err != nil {
			t.Fatal(err)
		}
		// All three carry exactly one optional field #1 (varint wire type).
		if !bytes.Equal(data, []byte{0x08, 0x00}) {
			t.Errorf("%s: wire bytes %x, want 0800", r.name, data)
		}
	}

	data := mustMarshal(t, &proto.HolyLandGameAtkReqMsg{TargetId: pb.Int32(1001), IsNpc: pb.Bool(false)})
	var atk proto.HolyLandGameAtkReqMsg
	if err := pb.Unmarshal(data, &atk); err != nil {
		t.Fatal(err)
	}
	if atk.TargetId == nil || *atk.TargetId != 1001 {
		t.Fatalf("atk targetId presence lost: %+v", &atk)
	}
	if atk.IsNpc == nil || *atk.IsNpc != false {
		t.Fatalf("atk isNpc=false presence lost: %+v", &atk)
	}
	if atk.EnemyId != nil {
		t.Fatalf("enemyId must stay absent when unset: %+v", &atk)
	}

	// RebirthData presence is the death signal — nil vs empty-message must
	// survive the round-trip distinctly.
	data = mustMarshal(t, &proto.HolyLandGameInfoRespMsg{
		Ret: pb.Int32(0),
		RebirthData: &proto.HolyLandBeenKillSyncMsg{
			RebirthTime: pb.Int64(time.Now().Add(30 * time.Second).Unix()),
		},
	})
	var gi proto.HolyLandGameInfoRespMsg
	if err := pb.Unmarshal(data, &gi); err != nil {
		t.Fatal(err)
	}
	if gi.RebirthData == nil || gi.RebirthData.RebirthTime == nil {
		t.Fatalf("rebirth presence lost: %+v", &gi)
	}
}

func TestHLRebirthLeft(t *testing.T) {
	futureSec := time.Now().Add(300 * time.Second).Unix()
	futureMs := futureSec * 1000
	if _, waiting := hlRebirthLeft(futureSec, time.Now()); !waiting {
		t.Error("seconds deadline not recognized")
	}
	if _, waiting := hlRebirthLeft(futureMs, time.Now()); !waiting {
		t.Error("milliseconds deadline not recognized")
	}
	if _, waiting := hlRebirthLeft(time.Now().Add(-10*time.Second).Unix(), time.Now()); waiting {
		t.Error("elapsed deadline still waiting")
	}
	if _, waiting := hlRebirthLeft(0, time.Now()); waiting {
		t.Error("zero deadline must not count as waiting")
	}
}

// Pure auto-rebirth gate table: resident, dead, cooldown (s AND ms form),
// the bounded probe budget, and the once-a-minute backoff stamp. The free
// counter NEVER gates (customer bug 2026-10-24 — the server's optional field
// must not be able to silently block every revive; kunlun law: bounded
// probes, server arbitrates), and Dead with an UNKNOWN cooldown probes too
// (Dead only comes from server-declared kills).
func TestHLShouldAutoRebirth(t *testing.T) {
	now := time.Now()
	elapsedSec := now.Add(-10 * time.Second).Unix()
	elapsedMs := elapsedSec * 1000
	futureSec := now.Add(300 * time.Second).Unix()

	base := func() *holyLandState {
		return &holyLandState{Dead: true, RebirthTime: elapsedSec}
	}

	cases := []struct {
		name  string
		setup func(*holyLandState)
		want  bool
	}{
		{"free rebirth ready", func(*holyLandState) {}, true},
		{"ms cooldown elapsed", func(s *holyLandState) { s.RebirthTime = elapsedMs }, true},
		{"left out", func(s *holyLandState) { s.LeftOut = true }, false},
		{"alive", func(s *holyLandState) { s.Dead = false }, false},
		{"free counter 0 does not gate", func(s *holyLandState) { s.FreeRebirth = 0 }, true},
		{"free counter folded does not gate", func(s *holyLandState) { s.FreeRebirth = 3 }, true},
		{"cooldown pending", func(s *holyLandState) { s.RebirthTime = futureSec }, false},
		{"unknown cooldown probes", func(s *holyLandState) { s.RebirthTime = 0 }, true},
		{"tries exhausted", func(s *holyLandState) { s.rebirthTries = hlMaxRebirthTries }, false},
		{"backoff active", func(s *holyLandState) { s.lastRebirthTry = now.Add(-30 * time.Second) }, false},
		{"backoff expired", func(s *holyLandState) { s.lastRebirthTry = now.Add(-2 * time.Minute) }, true},
	}
	for _, c := range cases {
		st := base()
		c.setup(st)
		if got := hlShouldAutoRebirth(st, now); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// holyLandOrder refuses every order when stopped / closed / eliminated / dead,
// and refusals must never touch the socket.
func TestHLGuards(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*EventPlayer)
	}{
		{"stopped", func(*EventPlayer) {}}, // Running=false
		{"not open", func(p *EventPlayer) { p.Running = true }},
		{"left out", func(p *EventPlayer) {
			p.Running = true
			p.hlOpen = true
			p.hlState().LeftOut = true
		}},
		{"dead waiting", func(p *EventPlayer) {
			p.Running = true
			p.hlOpen = true
			st := p.hlState()
			st.Dead = true
			// Cooldown still pending refuses the free route; item=0 refuses
			// the item route; the rest refuse on dead. (A dead account with
			// the cooldown elapsed MUST be able to revive — that case lives
			// in TestHLRebirthWire/TestHLRebirthServerJudge now.)
			st.RebirthTime = time.Now().Add(300 * time.Second).Unix()
		}},
	}
	for _, c := range cases {
		p := &EventPlayer{}
		c.setup(p)
		p.writeChan = make(chan []byte, 8)
		wireCtxEP(t, p)
		fns := map[string]func() error{
			"move":        func() error { return p.HolyLandMoveTo(1001) },
			"attack":      func() error { return p.HolyLandAttackTo(1001, false) },
			"auto":        func() error { return p.HolyLandAutoAttack(1001) },
			"accel":       p.HolyLandAccelerate,
			"rebirthFree": func() error { return p.HolyLandRebirth(false) },
			"rebirthItem": func() error { return p.HolyLandRebirth(true) },
		}
		for name, fn := range fns {
			if err := fn(); err == nil {
				t.Errorf("%s/%s: expected refusal", c.name, name)
			}
		}
		if n := len(p.writeChan); n != 0 {
			t.Errorf("%s: %d frames leaked on refusal", c.name, n)
		}
	}
}

// Open, alive, resident: every order reaches the socket as the right frame
// with its optional fields actually serialized.
func TestHLOrdersWire(t *testing.T) {
	open := func(p *EventPlayer) {
		p.Running = true
		p.hlOpen = true
	}
	orders := []struct {
		name string
		cmd  string
		call func(*EventPlayer) error
	}{
		{"move", HOLY_LAND_MOVE_CMD, func(p *EventPlayer) error { return p.HolyLandMoveTo(1001) }},
		{"attack", HOLY_LAND_ATTACK_CMD, func(p *EventPlayer) error { return p.HolyLandAttackTo(1001, false) }},
		{"attacknpc", HOLY_LAND_ATTACK_CMD, func(p *EventPlayer) error { return p.HolyLandAttackTo(1002, true) }},
		{"auto", HOLY_LAND_AUTO_ATK_CMD, func(p *EventPlayer) error { return p.HolyLandAutoAttack(2001) }},
	}
	for _, o := range orders {
		p := &EventPlayer{}
		open(p)
		p.writeChan = make(chan []byte, 4)
		wireCtxEP(t, p)
		if err := o.call(p); err != nil {
			t.Fatalf("%s: %v", o.name, err)
		}
		frame := <-p.writeChan
		if got := frameFuncType(t, frame); got != o.cmd {
			t.Fatalf("%s: cmd %s want %s", o.name, got, o.cmd)
		}
	}

	// Accelerate needs a march and echoes the cached speed-up counter.
	p := &EventPlayer{}
	open(p)
	p.writeChan = make(chan []byte, 4)
	wireCtxEP(t, p)
	if err := p.HolyLandAccelerate(); err == nil || len(p.writeChan) != 0 {
		t.Fatalf("accelerate must require marching, frame count %d", len(p.writeChan))
	}
	st := p.hlState()
	st.Moving = true
	st.SpeedUp = 3
	if err := p.HolyLandAccelerate(); err != nil {
		t.Fatal(err)
	}
	frame := <-p.writeChan
	if frameFuncType(t, frame) != HOLY_LAND_ACCELERATE_CMD {
		t.Fatalf("accel cmd %s", frameFuncType(t, frame))
	}
	var acc proto.HolyLandGameAcclerateMoveReqMsg
	if err := pb.Unmarshal(frame[18:], &acc); err != nil {
		t.Fatal(err)
	}
	if acc.SpeedUpCount == nil || *acc.SpeedUpCount != 3 {
		t.Fatalf("accelerate baseline lost: %+v", &acc)
	}

	// Auto-attack OFF bypasses the dead guard (parking the unit is always
	// safe) and must serialize targetId=0.
	p = &EventPlayer{}
	open(p)
	p.hlState().Dead = true
	p.writeChan = make(chan []byte, 4)
	wireCtxEP(t, p)
	if err := p.HolyLandAutoAttack(0); err != nil {
		t.Fatalf("auto-off refused while dead: %v", err)
	}
	frame = <-p.writeChan
	if frameFuncType(t, frame) != HOLY_LAND_AUTO_ATK_CMD {
		t.Fatalf("auto-off cmd %s", frameFuncType(t, frame))
	}
	if payload := frame[18:]; !bytes.Equal(payload, []byte{0x08, 0x00}) {
		t.Fatalf("auto-off wire %x, want 0800", payload)
	}
}

// Manual rebirth routes: free waits only for the cooldown (the free COUNT
// never refuses — customer bug 2026-10-24; the server arbitrates); item
// needs a server-reported item; the request carries useItem explicitly
// either way.
func TestHLRebirthWire(t *testing.T) {
	seed := func(item, free int32, rebirthAt int64) *EventPlayer {
		p := &EventPlayer{}
		p.Running = true
		p.hlOpen = true
		st := p.hlState()
		st.Dead = true
		st.FreeRebirth = free
		st.ItemRebirth = item
		st.RebirthTime = rebirthAt
		p.writeChan = make(chan []byte, 4)
		wireCtxEP(t, p)
		return p
	}

	elapsed := time.Now().Add(-10 * time.Second).Unix()

	// cooldown pending → free route refused, no frame
	p := seed(1, 1, time.Now().Add(300*time.Second).Unix())
	if err := p.HolyLandRebirth(false); err == nil || len(p.writeChan) != 0 {
		t.Fatal("free rebirth must wait for cooldown")
	}

	// free count never folded (0) → STILL fires: the server is the budget
	// judge; a silent local refusal was the reported bug.
	p = seed(1, 0, elapsed)
	if err := p.HolyLandRebirth(false); err != nil {
		t.Fatalf("free rebirth must not gate on the free counter: %v", err)
	}
	frame := <-p.writeChan
	if frameFuncType(t, frame) != HOLY_LAND_BACK_HOME_CMD {
		t.Fatalf("back-home cmd %s", frameFuncType(t, frame))
	}

	// free ready → useItem=false on the wire
	p = seed(1, 1, elapsed)
	if err := p.HolyLandRebirth(false); err != nil {
		t.Fatal(err)
	}
	frame = <-p.writeChan
	if frameFuncType(t, frame) != HOLY_LAND_BACK_HOME_CMD {
		t.Fatalf("back-home cmd %s", frameFuncType(t, frame))
	}
	if payload := frame[18:]; !bytes.Equal(payload, []byte{0x08, 0x00}) {
		t.Fatalf("free rebirth wire %x, want 0800", payload)
	}

	// item route → useItem=true
	p = seed(2, 0, 0)
	if err := p.HolyLandRebirth(true); err != nil {
		t.Fatal(err)
	}
	frame = <-p.writeChan
	var req proto.HolyLandGameBackHomeReq
	if err := pb.Unmarshal(frame[18:], &req); err != nil {
		t.Fatal(err)
	}
	if req.UseItem == nil || *req.UseItem != true {
		t.Fatalf("item rebirth must send useItem=true: %+v", &req)
	}

	// item route without an item → refused
	p = seed(0, 1, elapsed)
	if err := p.HolyLandRebirth(true); err == nil || len(p.writeChan) != 0 {
		t.Fatal("item rebirth must require an item")
	}
}

// Server-arbitrated rebirth (kunlun law, customer bug 2026-10-24): the auto
// probe runs even when the free counter was never folded; a REFUSAL of an
// auto probe is routine log-only, while a command's refusal notifies the
// leader; success re-arms the probe budget; the budget bounds the loop.
func TestHLRebirthServerJudge(t *testing.T) {
	p := &EventPlayer{}
	p.Running = true
	p.hlOpen = true
	p.Notify = true
	p.NotiChan = make(chan NotifyMGS, 16)
	p.writeChan = make(chan []byte, 16)
	wireCtxEP(t, p)
	st := p.hlState()
	st.Dead = true // counters stay 0 — as if 209521 never carried them

	// auto probe fires with an unseen counter
	p.hlTryAutoRebirth(time.Now())
	if len(p.writeChan) != 1 {
		t.Fatalf("auto probe must fire with counter 0, got %d frames", len(p.writeChan))
	}
	if st.rebirthTries != 1 {
		t.Fatalf("probe not counted: %d", st.rebirthTries)
	}

	// auto refusal → log only, no leader spam, still dead
	p.hlHandleBackHome(mustMarshal(t, &proto.HolyLandGameBackHomeRespMsg{Ret: pb.Int32(5)}))
	if len(p.NotiChan) != 0 {
		t.Fatalf("auto probe refusal notified the leader: %+v", <-p.NotiChan)
	}
	if !p.hl.Dead {
		t.Fatal("refusal must not revive")
	}

	// budget bounds the loop
	st.rebirthTries = hlMaxRebirthTries
	st.lastRebirthTry = time.Time{}
	p.hlTryAutoRebirth(time.Now())
	if len(p.writeChan) != 1 {
		t.Fatalf("tries must be bounded at %d, got %d frames", hlMaxRebirthTries, len(p.writeChan))
	}

	// manual free: fires despite counter 0, refusal NOTIFIES (command context)
	if err := p.HolyLandRebirth(false); err != nil {
		t.Fatalf("manual free rebirth gated on an unfolded counter: %v", err)
	}
	if len(p.writeChan) != 2 {
		t.Fatalf("manual frame missing (%d frames)", len(p.writeChan))
	}
	p.hlHandleBackHome(mustMarshal(t, &proto.HolyLandGameBackHomeRespMsg{Ret: pb.Int32(7)}))
	if len(p.NotiChan) != 1 {
		t.Fatalf("manual refusal must notify, got %d", len(p.NotiChan))
	}

	// manual success: revives + re-arms budget + notifies
	if err := p.HolyLandRebirth(false); err != nil {
		t.Fatal(err)
	}
	p.hlHandleBackHome(mustMarshal(t, &proto.HolyLandGameBackHomeRespMsg{Ret: pb.Int32(0)}))
	if p.hl.Dead || p.hl.rebirthTries != 0 || p.hl.RebirthTime != 0 {
		t.Fatalf("success did not fully reconcile: %+v", p.hl)
	}
	if len(p.NotiChan) != 2 {
		t.Fatalf("success notify missing (%d)", len(p.NotiChan))
	}
}

// Game info declaring RebirthData WITHOUT a rebirthTime must still stamp a
// now-deadline (the old code left RebirthTime 0 and the auto gate refused to
// fire forever — one of the two silent blockers of the 2026-10-24 report).
func TestHLGameInfoStampsUnknownCooldown(t *testing.T) {
	p := &EventPlayer{}
	p.writeChan = make(chan []byte, 4)
	wireCtxEP(t, p)
	p.hlHandleGameInfo(mustMarshal(t, &proto.HolyLandGameInfoRespMsg{
		Ret:         pb.Int32(0),
		RebirthData: &proto.HolyLandBeenKillSyncMsg{TargetName: pb.String("acme")},
	}))
	if !p.hl.Dead || p.hl.RebirthTime == 0 {
		t.Fatalf("death without cooldown field not stamped: %+v", p.hl)
	}
	if !hlShouldAutoRebirth(p.hl, time.Now()) {
		t.Fatal("stamped unknown-cooldown death must be probe-eligible")
	}
}

// Response/push handlers keep the cached state honest.
func TestHLHandlers(t *testing.T) {
	p := &EventPlayer{}
	p.writeChan = make(chan []byte, 16)
	wireCtxEP(t, p)

	// schedule: open + a running battle window = the display opens; the
	// day anchors are cached from the same answer.
	now := time.Now()
	p.hlHandleSchedule(mustMarshal(t, &proto.HolyLandBattleApplyDataSyncMsg{
		Ret: pb.Int32(0), IsOpen: pb.Bool(true), HasQuality: pb.Bool(true), RoundId: pb.Int32(2),
		ScheduleTimestamp: []int64{hlLiveAnchor(now)},
	}))
	if !p.HolyLandEventOpen() || p.hl.RoundId != 2 || !p.hl.HasQuality || len(p.hl.ScheduleApply) != 1 {
		t.Fatal("schedule open not cached")
	}
	// isOpen=true with an ENDED window: the season-level lie the user
	// reported 2026-10-06 — the gate must stay shut.
	p.hlHandleSchedule(mustMarshal(t, &proto.HolyLandBattleApplyDataSyncMsg{
		Ret: pb.Int32(0), IsOpen: pb.Bool(true), RoundId: pb.Int32(3),
		ScheduleTimestamp: []int64{hlEndedAnchor(now)},
	}))
	if p.HolyLandEventOpen() {
		t.Fatal("isOpen=true outside every battle window must not show")
	}
	// ret!=0 closes the event even mid-round — and drops the stale anchors.
	p.hlHandleSchedule(mustMarshal(t, &proto.HolyLandBattleApplyDataSyncMsg{Ret: pb.Int32(9)}))
	if p.HolyLandEventOpen() || p.hl.ScheduleApply != nil || p.hl.ScheduleTs != nil {
		t.Fatal("ret!=0 must close the event and drop day anchors")
	}

	// activity list: incremental adds only, full lists may withdraw
	p.hlHandleActivityList(mustMarshal(t, &proto.PushActivityList{
		MainConfig: []*proto.ActivityMainConfig{{Type: pb.Int32(346)}},
	}), false)
	if !p.hlOpen {
		t.Fatal("incremental list must flag Holy Land")
	}
	p.hlHandleActivityList(mustMarshal(t, &proto.PushActivityList{
		MainConfig: []*proto.ActivityMainConfig{{Type: pb.Int32(99)}},
	}), false)
	if !p.hlOpen {
		t.Fatal("incremental list must not withdraw")
	}
	p.hlHandleActivityList(mustMarshal(t, &proto.PushActivityList{}), true)
	if p.hlOpen {
		t.Fatal("full list without type 346 must withdraw")
	}
	p.hlOpen = true

	// game info: RebirthData presence is the death signal, route is movement
	p.hlHandleGameInfo(mustMarshal(t, &proto.HolyLandGameInfoRespMsg{
		Ret: pb.Int32(0), FreeRebirthCount: pb.Int32(1), ItemRebirthCount: pb.Int32(2),
		MyRouteInfo: &proto.HolyLandGameRouteInfo{ToCityId: pb.Int32(1005)},
	}))
	if !p.hl.Moving || p.hl.RouteTo != 1005 || p.hl.Dead {
		t.Fatalf("route/dead wrong: %+v", p.hl)
	}
	p.hlHandleGameInfo(mustMarshal(t, &proto.HolyLandGameInfoRespMsg{
		Ret: pb.Int32(0), FreeRebirthCount: pb.Int32(1),
		RebirthData: &proto.HolyLandBeenKillSyncMsg{
			RebirthTime: pb.Int64(time.Now().Add(60 * time.Second).UnixMilli()),
			TargetName:  pb.String("acme"),
		},
	}))
	if p.hl.Moving || p.hl.RouteTo != 0 || !p.hl.Dead || p.hl.KillerName != "acme" {
		t.Fatalf("rebirth data not applied: %+v", p.hl)
	}
	// ret!=0 is the NON-RESIDENT answer — routine for every registered
	// account that has not entered the map. It must not touch the open
	// flag (schedule owns that); the 3 s flip-flop it caused hid Cửu U
	// while it was genuinely open (live log 2026-10-05 07:26). Re-open
	// via schedule first (the step above closed it on purpose).
	p.hlHandleSchedule(mustMarshal(t, &proto.HolyLandBattleApplyDataSyncMsg{
		Ret: pb.Int32(0), IsOpen: pb.Bool(true), RoundId: pb.Int32(4),
	}))
	p.hlHandleGameInfo(mustMarshal(t, &proto.HolyLandGameInfoRespMsg{Ret: pb.Int32(7)}))
	if !p.hl.IsOpen {
		t.Fatal("game info ret!=0 must NOT close the schedule-owned IsOpen")
	}
	if !p.hlOpen {
		t.Fatal("game info ret!=0 must not withdraw the activity flag")
	}
	if !p.hl.Dead {
		t.Fatal("ret!=0 must not clear dead optimistically")
	}

	// mygameinfo sync: presence-checked; any non-zero location revives
	p.hlHandleMyGameInfoSync(mustMarshal(t, &proto.HolyLandGameMyGameInfoSyncMsg{
		Location: pb.Int32(1002),
	}))
	if p.hl.Dead || p.hl.Location != 1002 {
		t.Fatalf("sync revive failed: %+v", p.hl)
	}

	// been-kill push marks death + clears the march. Even WITHOUT a cooldown
	// field it stamps a now-deadline and the inline auto-probe fires (server
	// arbitrates; bounded by hlMaxRebirthTries — kunlun law).
	p.hl.Dead = false
	p.hl.Moving = true
	p.hl.RouteTo = 1005
	p.hl.FreeRebirth = 0 // counter irrelevant to the gate (customer bug fix)
	p.hlHandleBeenKillSync(mustMarshal(t, &proto.HolyLandBeenKillSyncMsg{
		CityId: pb.Int32(1003), KillByNPC: pb.Bool(true), RebirthTime: pb.Int64(0),
	}))
	if !p.hl.Dead || p.hl.Moving {
		t.Fatalf("been-kill state: %+v", p.hl)
	}
	// explicit rebirthTime=0 = "no wait" → the auto probe fires at once.
	frame := <-p.writeChan
	if frameFuncType(t, frame) != HOLY_LAND_BACK_HOME_CMD {
		t.Fatalf("auto rebirth cmd %s", frameFuncType(t, frame))
	}
	// A push WITHOUT the field at all must stamp a now-deadline when none is
	// known (the old code left 0 only via game-info; BeenKill must too, and
	// must NOT clobber a still-pending cooldown carried from before).
	p.hl.Dead = false
	p.hl.RebirthTime = time.Now().Add(600 * time.Second).Unix()
	p.hlHandleBeenKillSync(mustMarshal(t, &proto.HolyLandBeenKillSyncMsg{
		CityId: pb.Int32(1003), KillByNPC: pb.Bool(true),
	}))
	if p.hl.RebirthTime <= time.Now().Add(590*time.Second).Unix() {
		t.Fatalf("pending cooldown must survive a field-less kill push: %d", p.hl.RebirthTime)
	}
	p.hl.RebirthTime = 0
	p.hlHandleBeenKillSync(mustMarshal(t, &proto.HolyLandBeenKillSyncMsg{
		CityId: pb.Int32(1003), KillByNPC: pb.Bool(true),
	}))
	if p.hl.RebirthTime == 0 {
		t.Fatal("field-less kill push with no known cooldown must stamp a now-deadline")
	}
	// A third death re-arms the budget; elapsed ms cooldown probes too
	// (backoff reset by hand — production would just wait the minute).
	p.hl.lastRebirthTry = time.Time{}
	p.hlHandleBeenKillSync(mustMarshal(t, &proto.HolyLandBeenKillSyncMsg{
		CityId:      pb.Int32(1003),
		KillByNPC:   pb.Bool(true),
		RebirthTime: pb.Int64(time.Now().Add(-5 * time.Second).UnixMilli()),
	}))
	frame = <-p.writeChan
	if frameFuncType(t, frame) != HOLY_LAND_BACK_HOME_CMD {
		t.Fatalf("auto rebirth cmd %s", frameFuncType(t, frame))
	}

	// rank reward: claim once per availability window, re-arm on hasReward
	p.hlHandleRankRewardState(mustMarshal(t, &proto.HolyLandPlayerUnionRankRewardRespMsg{
		Ret: pb.Int32(0), UnionRank: pb.Int32(3), HasReward: pb.Bool(true),
	}))
	frame = <-p.writeChan
	if frameFuncType(t, frame) != HOLY_LAND_CLAIM_REWARD_CMD {
		t.Fatalf("claim cmd %s", frameFuncType(t, frame))
	}
	p.hlHandleRankRewardState(mustMarshal(t, &proto.HolyLandPlayerUnionRankRewardRespMsg{
		Ret: pb.Int32(0), UnionRank: pb.Int32(3), HasReward: pb.Bool(true),
	}))
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("double claim: %d frames", n)
	}
	p.hlHandleRankRewardState(mustMarshal(t, &proto.HolyLandPlayerUnionRankRewardRespMsg{
		Ret: pb.Int32(0), HasReward: pb.Bool(false),
	}))
	p.hlHandleRankRewardState(mustMarshal(t, &proto.HolyLandPlayerUnionRankRewardRespMsg{
		Ret: pb.Int32(0), UnionRank: pb.Int32(3), HasReward: pb.Bool(true),
	}))
	if frame = <-p.writeChan; frameFuncType(t, frame) != HOLY_LAND_CLAIM_REWARD_CMD {
		t.Fatalf("claim not re-armed: %s", frameFuncType(t, frame))
	}

	// left-out only applies to our union; auto-stop clears the 托管 target
	p.hl.Union = 77
	p.hl.AutoAtk = 1003
	p.hlHandleLeftOut(mustMarshal(t, &proto.HolyLandLeftOutSyncInfo{UnionId: pb.Int64(88)}))
	if p.hl.LeftOut {
		t.Fatal("foreign union left-out applied to us")
	}
	p.hlHandleLeftOut(mustMarshal(t, &proto.HolyLandLeftOutSyncInfo{UnionId: pb.Int64(77)}))
	if !p.hl.LeftOut {
		t.Fatal("own left-out not applied")
	}
	p.hlHandleAutoStop(mustMarshal(t, &proto.HolyLandGameAutoBattleStopSyncMsg{Ret: pb.Int32(0)}))
	if p.hl.AutoAtk != 0 {
		t.Fatalf("auto-stop must clear target: %d", p.hl.AutoAtk)
	}

	// accelerate echo caches only a present counter with ret==0
	p.hlHandleAccelerate(mustMarshal(t, &proto.HolyLandGameAcclerateMoveRespMsg{Ret: pb.Int32(0), SpeedUpCount: pb.Int32(9)}))
	if p.hl.SpeedUp != 9 {
		t.Fatalf("accelerate echo not cached: %d", p.hl.SpeedUp)
	}
	p.hlHandleAccelerate(mustMarshal(t, &proto.HolyLandGameAcclerateMoveRespMsg{Ret: pb.Int32(5), SpeedUpCount: pb.Int32(11)}))
	if p.hl.SpeedUp != 9 {
		t.Fatalf("refused accelerate must not change counter: %d", p.hl.SpeedUp)
	}

	// back-home success revives + refreshes counts
	p.hlHandleBackHome(mustMarshal(t, &proto.HolyLandGameBackHomeRespMsg{
		Ret: pb.Int32(0), FreeRebirthCount: pb.Int32(0), ItemRebirthCount: pb.Int32(1),
	}))
	if p.hl.Dead || p.hl.FreeRebirth != 0 || p.hl.ItemRebirth != 1 {
		t.Fatalf("back-home reconcile: %+v", p.hl)
	}
}

// Player-level one-shot minimap: request frame, response delivery, single
// outstanding request, and status-line rendering.
func TestHLPlayerRequestMiniMap(t *testing.T) {
	p := &Player{UID: 4242}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	p.writeChan = make(chan []byte, 2)

	want := mustMarshal(t, &proto.HolyLandGameMiniMapInfoRespMsg{
		Ret: pb.Int32(0),
		CityInfo: []*proto.HolyLandGameMiniCityInfo{{
			CityId:   pb.Int32(1001),
			BaseData: &proto.HolyLandMiniUnionInfo{UnionId: pb.Int64(77), Name: pb.String("bang")},
		}},
	})
	go func() {
		frame := <-p.writeChan
		if got := frameFuncType(t, frame); got != HOLY_LAND_MINIMAP_CMD {
			t.Errorf("minimap cmd %s", got)
		}
		p.hlDeliverMiniMap(want)
	}()

	got, err := p.RequestHolyLandMiniMap(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("payload mismatch")
	}
	if p.HlPending != nil {
		t.Fatal("pending slot not cleared")
	}

	// busy slot refuses a second concurrent request without touching the wire
	p.HlPending = make(chan []byte, 1)
	if _, err := p.RequestHolyLandMiniMap(time.Second); err == nil {
		t.Fatal("expected busy error")
	}
	p.HlPending = nil
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("busy request queued %d frames", n)
	}
}

func TestHLStatus(t *testing.T) {
	p := &EventPlayer{}
	if s := p.HolyLandStatus(); s != "⏹️ chưa login" {
		t.Fatalf("stopped status %q", s)
	}
	p.Running = true
	if s := p.HolyLandStatus(); s != "⏳ Cửu U chưa mở" {
		t.Fatalf("closed status %q", s)
	}
	p.hlOpen = true
	p.hlState().Moving = true
	p.hlState().RouteTo = 1005
	if s := p.HolyLandStatus(); s != "🏯 đang hành quân tới thành 1005" {
		t.Fatalf("moving status %q", s)
	}
	p.hlState().AutoAtk = 2001
	if s := p.HolyLandStatus(); s != "🏯 đang hành quân tới thành 1005 · treo máy ở thành 2001" {
		t.Fatalf("moving+auto status %q", s)
	}
	p.hlState().LeftOut = true
	if s := p.HolyLandStatus(); s != "🚪 đã bị loại khỏi Cửu U" {
		t.Fatalf("leftout status %q", s)
	}
}
