package tytd_go

// People-on-map ledger tests (port of 2026-10-19, opcodes live-proven by
// gi_people_probe_live_test.go): fold semantics (newest departure wins,
// empty-name rows never wipe known names, expiry pruning), per-variant
// isolation, cross-account merge with own-side marking, 1aa7 → page body
// flattening with march-ledger name resolution, seat delivery, the
// no-activityId attribution policy (sole variant / ambiguous drop), and the
// SSE march re-issue that keeps rows alive across failing minimap pulls.

import (
	"strings"
	"testing"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func giRouteInfo(pid int64, from, to int32, t0, ar int64, union int64, name string) *proto.GodIslandGameRouteInfo {
	return &proto.GodIslandGameRouteInfo{
		PlayerId: pb.Int64(pid), FromCityId: pb.Int32(from), ToCityId: pb.Int32(to),
		Time: pb.Int64(t0), Arrival: pb.Int64(ar),
		UnionId: pb.Int64(union), Name: pb.String(name),
	}
}

func TestGIFoldRouteLedger(t *testing.T) {
	p := &EventPlayer{}
	ev := p.giSlot(GIEventNormal)
	now := time.Now()
	ms := now.UnixMilli()

	p.GodIslandMu.Lock()
	// Expired row for pid9 (arrived 5 min ago) — planted, must be pruned by
	// the next fold.
	ev.Routes = map[int64]giRouteRec{9: {From: 1001, To: 1002, Time: ms - 400_000, Arrival: ms - 300_000}}
	p.giFoldRouteInfos(ev, []*proto.GodIslandGameRouteInfo{
		giRouteInfo(1, 1003, 2013, ms, ms+60_000, 7, "tocu"),
	}, now)
	// Older departure must NOT supersede; empty name must NOT wipe "tocu".
	p.giFoldRouteInfos(ev, []*proto.GodIslandGameRouteInfo{
		giRouteInfo(1, 1004, 2014, ms-10_000, ms+50_000, 8, ""),
	}, now)
	// Newer departure wins, name carried forward when blank.
	p.giFoldRouteInfos(ev, []*proto.GodIslandGameRouteInfo{
		giRouteInfo(1, 1005, 2015, ms+5_000, ms+90_000, 7, ""),
	}, now)
	// Garbage rows (no pid / no destination) never enter.
	p.giFoldRouteInfos(ev, []*proto.GodIslandGameRouteInfo{
		giRouteInfo(0, 1001, 1002, ms, ms+60_000, 7, "ghost"),
		giRouteInfo(2, 1001, 0, ms, ms+60_000, 7, "ghost"),
	}, now)
	r1 := ev.Routes[1]
	_, hasExpired := ev.Routes[9]
	_, hasGarbage := ev.Routes[2]
	p.GodIslandMu.Unlock()

	if hasExpired {
		t.Error("expired march row was not pruned")
	}
	if hasGarbage {
		t.Error("garbage row (no pid / no to-city) entered the ledger")
	}
	if r1.From != 1005 || r1.To != 2015 || r1.Time != ms+5_000 || r1.Union != 7 {
		t.Errorf("newest-departure-wins broken: %+v", r1)
	}
	if r1.Name != "tocu" {
		t.Errorf("name must survive an empty-name follow-up row, got %q", r1.Name)
	}
}

func TestGIRoutesMergedAcrossAccountsPerVariant(t *testing.T) {
	mk := func(name string, union int64, rows map[int64]giRouteRec, peak map[int64]giRouteRec) *EventPlayer {
		ep := &EventPlayer{Username: name, ChatID: 5, Running: true}
		ev := ep.giSlot(GIEventNormal)
		ev.ActivityID = 42
		ev.Union = union
		ep.GodIslandMu.Lock()
		ev.Routes = rows
		if peak != nil {
			pv := ep.giSlotLocked(GIEventPeak)
			pv.ActivityID = 43
			pv.Routes = peak
		}
		ep.GodIslandMu.Unlock()
		return ep
	}
	ms := time.Now().UnixMilli()
	// ep1 saw pid1's older departure; ep2 (own union 7) saw the newer one
	// plus an enemy march and one already landed.
	ep1 := mk("ev1", 4, map[int64]giRouteRec{
		1: {From: 1003, To: 2013, Time: ms - 20_000, Arrival: ms + 40_000, Union: 4, Name: "tocu"},
	}, map[int64]giRouteRec{
		// Same pid on the OTHER island — must never leak into bonglai.
		1: {From: 9001, To: 9002, Time: ms + 1000, Arrival: ms + 90_000, Union: 4, Name: "peakbot"},
	})
	ep2 := mk("ev2", 7, map[int64]giRouteRec{
		1:   {From: 1003, To: 2013, Time: ms, Arrival: ms + 60_000, Union: 7, Name: "tocu"},
		2:   {From: 1006, To: 2014, Time: ms, Arrival: ms + 90_000, Union: 9, Name: "enemy"},
		100: {From: 1001, To: 1002, Time: ms - 300_000, Arrival: ms - 100_000, Union: 7, Name: "landed"},
	}, nil)
	tt := &TeleBot{}
	tt.session.Store(int64(5), ep1)
	tt.session.Store(int64(6), ep2)

	routes := tt.giRoutesMerged(5, GIEventNormal, time.Now())
	if len(routes) != 2 {
		t.Fatalf("want 2 live rows (newest pid1 + enemy pid2, landed dropped), got %+v", routes)
	}
	// Sorted by arrival: pid1 (ms+60s) then pid2 (ms+90s).
	if routes[0].FC != 1003 || routes[0].T != ms || !routes[0].O {
		t.Errorf("newest-departure merge / own-side marking broken: %+v", routes[0])
	}
	if routes[0].N != "tocu" {
		t.Errorf("name lost in merge: %+v", routes[0])
	}
	if routes[1].TC != 2014 || routes[1].O {
		t.Errorf("enemy row mis-marked: %+v", routes[1])
	}
	// Peak board keeps its own ledger: pid1's peak row must not appear.
	peakRoutes := tt.giRoutesMerged(5, GIEventPeak, time.Now())
	if len(peakRoutes) != 1 || peakRoutes[0].FC != 9001 {
		t.Fatalf("variant isolation broken: %+v", peakRoutes)
	}
}

func TestGIViewAnswerNamesAndSides(t *testing.T) {
	rsp := &proto.GodIslandGameCityInfoRespMsg{
		Ret: 0,
		CityInfo: []*proto.GodIslandGameCityInfo{
			{
				CityId: 1001, HaveFight: pb.Bool(true), Hp: pb.Int32(500), CurHp: pb.Int32(320),
				BaseData: &proto.GodIslandUnionSimpleInfo{UnionId: pb.Int64(7), UnionName: pb.String("Nhà Mình")},
				HeadInfo: []*proto.GodIslandPlayerHeadData{
					{PlayerId: pb.Int64(1)}, // empty name — resolved via the ledger
					{PlayerId: pb.Int64(5), Name: pb.String("ran")},
				},
			},
			{CityId: 0}, // garbage row never ships
		},
	}
	ledger := map[int64]giRouteRec{1: {Name: "tocu", Union: 7}}
	own := map[int64]bool{7: true}
	b := giViewAnswer(rsp, ledger, nil, own)
	if len(b.Cities) != 1 || b.Heads != 2 {
		t.Fatalf("body shape: %+v", b)
	}
	c := b.Cities["1001"]
	if c.HP != 500 || c.Cur != 320 || !c.Fight || c.U != 7 || c.UN != "Nhà Mình" {
		t.Errorf("city row: %+v", c)
	}
	if len(c.Heads) != 2 {
		t.Fatalf("heads: %+v", c.Heads)
	}
	// March-ledger name resolution, never a fabricated label.
	if c.Heads[0].P != 1 || c.Heads[0].N != "tocu" || !c.Heads[0].O {
		t.Errorf("head1 (ledger-named, own city): %+v", c.Heads[0])
	}
	if c.Heads[1].N != "ran" || !c.Heads[1].O {
		t.Errorf("head2 (roster-named, own city): %+v", c.Heads[1])
	}
	// Enemy city, head unknown EVERYWHERE: no name, no own flag — the page
	// draws "người <id>" itself.
	b2 := giViewAnswer(&proto.GodIslandGameCityInfoRespMsg{
		CityInfo: []*proto.GodIslandGameCityInfo{{
			CityId:   1002,
			BaseData: &proto.GodIslandUnionSimpleInfo{UnionId: pb.Int64(9)},
			HeadInfo: []*proto.GodIslandPlayerHeadData{{PlayerId: pb.Int64(77)}},
		}},
	}, nil, nil, own)
	h := b2.Cities["1002"].Heads[0]
	if h.N != "" || h.O || h.P != 77 {
		t.Errorf("enemy head must stay nameless + unmarked: %+v", h)
	}
	// Bug 1 fix: a head absent from the LIVE ledger resolves via the
	// persistent name cache (and gets its side from the cached union).
	b3 := giViewAnswer(&proto.GodIslandGameCityInfoRespMsg{
		CityInfo: []*proto.GodIslandGameCityInfo{{
			CityId:   1002,
			BaseData: &proto.GodIslandUnionSimpleInfo{UnionId: pb.Int64(9)},
			HeadInfo: []*proto.GodIslandPlayerHeadData{{PlayerId: pb.Int64(77)}},
		}},
	}, nil, map[int64]giNameRec{77: {Name: "hidden", Union: 7}}, own)
	h3 := b3.Cities["1002"].Heads[0]
	if h3.N != "hidden" || !h3.O {
		t.Errorf("name-cache resolution broken: %+v", h3)
	}
}

// Bug 1 (2026-10-19): march rows expire but NAMES must not — a person who
// marched an hour ago still stands (and must still show a real name).
func TestGINameLedgerSurvivesPruning(t *testing.T) {
	p := &EventPlayer{Username: "ev1"}
	p.giSlot(GIEventNormal).ActivityID = 42 // sole variant → push attributable
	push, _ := pb.Marshal(&proto.GodIslandGameRouteInfoSyncMsg{
		RouteInfo: []*proto.GodIslandGameRouteInfo{
			giRouteInfo(1, 1003, 2013, time.Now().UnixMilli(), time.Now().UnixMilli()+60_000, 7, "tocu"),
		},
	})
	p.giHandleRouteSync(push)

	// Force the prune: a fold stamped 5 minutes ahead.
	ev := p.giSlot(GIEventNormal)
	p.GodIslandMu.Lock()
	p.giFoldRouteInfos(ev, nil, time.Now().Add(5*time.Minute))
	_, rowStill := ev.Routes[1]
	nm := p.giNames[1]
	p.GodIslandMu.Unlock()
	if rowStill {
		t.Fatal("march row should have been pruned")
	}
	if nm.Name != "tocu" || nm.Union != 7 {
		t.Fatalf("name must OUTLIVE the march row, got %+v", nm)
	}
}

// 1ac0 battle lines + 1abf win banners are the roster's richest name
// sources (lineInfo.playerInfo / battle sides / win info all carry names).
func TestGILineAndWinNameFolding(t *testing.T) {
	p := &EventPlayer{Username: "ev1"}
	line, _ := pb.Marshal(&proto.GodIslandGameLineInfoSyncMsg{
		LineInfo: []*proto.GodIslandGameLineInfo{
			{PlayerId: pb.Int64(11), PlayerInfo: &proto.GodIslandGameLinePlayerInfo{
				Name: pb.String("linh"), Union: pb.Int64(8),
			}},
		},
		BattleInfo: []*proto.GodIslandBattleMsg{
			{Attacker: &proto.GodIslandBattlePlayer{PlayerInfo: &proto.GodIslandGameLinePlayerInfo{
				Name: pb.String("du"), HeadInfo: &proto.GodIslandPlayerHeadData{PlayerId: pb.Int64(12)},
			}}},
		},
	})
	p.giHandleLineSync(line)
	win, _ := pb.Marshal(&proto.GodIslandGamePlayerWinSyncMsg{
		Info: []*proto.GodIslandGamePlayerWinInfo{
			{Name: pb.String("vương"), HeadInfo: &proto.GodIslandPlayerHeadData{PlayerId: pb.Int64(13)}},
		},
	})
	p.giHandlePlayerWin(win)
	p.GodIslandMu.Lock()
	a, b, c := p.giNames[11], p.giNames[12], p.giNames[13]
	p.GodIslandMu.Unlock()
	if a.Name != "linh" || a.Union != 8 {
		t.Errorf("lineInfo fold: %+v", a)
	}
	if b.Name != "du" {
		t.Errorf("battle attacker fold: %+v", b)
	}
	if c.Name != "vương" {
		t.Errorf("win banner fold: %+v", c)
	}
}

func TestGICityInfoFoldsAndFeedsSeat(t *testing.T) {
	p := &EventPlayer{Username: "ev1", Running: true}
	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	rsp := &proto.GodIslandGameCityInfoRespMsg{
		Ret: 0,
		CityInfo: []*proto.GodIslandGameCityInfo{{
			CityId: 1001, Hp: pb.Int32(500),
			HeadInfo: []*proto.GodIslandPlayerHeadData{{PlayerId: pb.Int64(1), Name: pb.String("x")}},
		}},
		RouteInfo: []*proto.GodIslandGameRouteInfo{giRouteInfo(1, 1003, 2013, time.Now().UnixMilli(), time.Now().UnixMilli()+60_000, 7, "tocu")},
	}
	raw, _ := pb.Marshal(rsp)

	// Seat armed → answer lands in the channel AND folds.
	ch := make(chan []byte, 1)
	p.GodIslandMu.Lock()
	p.giCityPending = ch
	p.GodIslandMu.Unlock()
	p.giHandleCityInfo(ev, raw)
	select {
	case got := <-ch:
		if string(got) != string(raw) {
			t.Fatal("seat got a different payload")
		}
	default:
		t.Fatal("seat never received the 1aa7")
	}
	p.GodIslandMu.Lock()
	nR, nRo := len(ev.Routes), len(ev.Roster)
	p.GodIslandMu.Unlock()
	if nR != 1 || nRo != 1 {
		t.Fatalf("1aa7 did not fold: routes=%d roster=%d", nR, nRo)
	}

	// ret != 0 → no fold, no seat delivery (waiter times out, page keeps view).
	bad, _ := pb.Marshal(&proto.GodIslandGameCityInfoRespMsg{Ret: 6833})
	p.GodIslandMu.Lock()
	p.giCityPending = ch
	p.GodIslandMu.Unlock()
	p.giHandleCityInfo(ev, bad)
	select {
	case <-ch:
		t.Fatal("refused answer must NOT reach the seat")
	default:
	}
	p.GodIslandMu.Lock()
	if len(ev.Routes) != 1 || len(ev.Roster) != 1 {
		t.Fatal("refused answer must NOT fold")
	}
	p.GodIslandMu.Unlock()
}

func TestGIMarchPushAttribution(t *testing.T) {
	row := giRouteInfo(1, 1003, 2013, time.Now().UnixMilli(), time.Now().UnixMilli()+60_000, 7, "tocu")
	push, _ := pb.Marshal(&proto.GodIslandGameRouteInfoSyncMsg{RouteInfo: []*proto.GodIslandGameRouteInfo{row}})

	// Sole announced variant → attributed there.
	p := &EventPlayer{Username: "solo"}
	p.giSlot(GIEventNormal).ActivityID = 42
	p.giHandleRouteSync(push)
	if ev := p.giSlot(GIEventNormal); len(ev.Routes) != 1 {
		t.Fatalf("sole-variant push not folded: %+v", ev.Routes)
	}

	// Both announced → the frame cannot be told apart → dropped (never
	// guessed onto a board).
	p2 := &EventPlayer{Username: "both"}
	p2.giSlot(GIEventNormal).ActivityID = 42
	p2.giSlot(GIEventPeak).ActivityID = 43
	p2.giHandleRouteSync(push)
	p2.GodIslandMu.Lock()
	n0, n1 := len(p2.GiEvents[GIEventNormal].Routes), len(p2.GiEvents[GIEventPeak].Routes)
	p2.GodIslandMu.Unlock()
	if n0 != 0 || n1 != 0 {
		t.Fatalf("ambiguous push must be dropped, got %d/%d rows", n0, n1)
	}
}

func TestGICityBattleFlags(t *testing.T) {
	p := &EventPlayer{Username: "ev1"}
	ev := p.giSlot(GIEventNormal)
	ev.ActivityID = 42
	push, _ := pb.Marshal(&proto.GodIslandGameCityBattleSyncMsg{
		CityStates: []*proto.GodIslandCityBattleState{
			{CityId: pb.Int32(1003), IsBattle: pb.Bool(true)},
			{CityId: pb.Int32(1004), IsBattle: pb.Bool(false)},
		},
	})
	p.giHandleCityBattle(push)
	p.GodIslandMu.Lock()
	_, on := ev.FightSeen[1003]
	_, off := ev.FightSeen[1004]
	p.GodIslandMu.Unlock()
	if !on || off {
		t.Fatalf("1ace flags: on=%v off=%v", on, off)
	}
	// The server clearing a fight deletes the entry.
	clear, _ := pb.Marshal(&proto.GodIslandGameCityBattleSyncMsg{
		CityStates: []*proto.GodIslandCityBattleState{{CityId: pb.Int32(1003), IsBattle: pb.Bool(false)}},
	})
	p.giHandleCityBattle(clear)
	p.GodIslandMu.Lock()
	_, still := ev.FightSeen[1003]
	p.GodIslandMu.Unlock()
	if still {
		t.Fatal("cleared fight flag must be deleted")
	}
}

// Owner bug 3 (2026-10-19, "lúc thấy lúc không"): marches must NEVER blink
// behind a failing 206819 minimap pull — giPushMarches re-issues the last
// good SSE frame with a FRESH ledger list (pure RAM, zero game traffic).
func TestGIMarchesSurvivePullFailures(t *testing.T) {
	const chat = int64(7)
	c := webSSEHub.subscribe(chat)
	defer webSSEHub.unsubscribe(c)
	ep := &EventPlayer{Username: "ev1", ChatID: chat, Running: true}
	ev := ep.giSlot(GIEventNormal)
	ev.ActivityID = 42
	ms := time.Now().UnixMilli()
	ep.GodIslandMu.Lock()
	ev.Routes = map[int64]giRouteRec{
		1: {From: 1003, To: 2013, Time: ms, Arrival: ms + 60_000, Union: 7, Name: "tocu"},
	}
	ev.Roster = map[int32]giCityRow{
		1001: {HP: 500, Cur: 320, Seen: time.Now()},
		1002: {HP: 0, Cur: 0}, // no figure → never a bar
	}
	ep.GodIslandMu.Unlock()
	tt := &TeleBot{}
	tt.session.Store(chat, ep)
	last := &gimap.StreamMap{Which: gimap.StreamBonglai}

	// No baseline frame yet → nothing to re-issue (normal err path wins).
	if tt.giPushMarches(chat, gimap.StreamBonglai, nil) {
		t.Fatal("re-issue must refuse before the first good frame")
	}
	if !tt.giPushMarches(chat, gimap.StreamBonglai, last) {
		t.Fatal("live march must ride the stale frame while the pull fails")
	}
	select {
	case m := <-c.ch:
		if m.event != "map" || !strings.Contains(m.data, `"fc":1003`) || !strings.Contains(m.data, `"n":"tocu"`) ||
			!strings.Contains(m.data, `"cities":{"1001":{"hp":500,"cur":320}}`) {
			t.Fatalf("re-issued frame wrong: %s / %s", m.event, m.data)
		}
	default:
		t.Fatal("no frame reached the SSE client")
	}
	// The OTHER variant has no rows → refuse, so the err note can ship.
	if tt.giPushMarches(chat, gimap.StreamThienma, last) {
		t.Fatal("idle variant must not push an empty march frame")
	}
	select {
	case m := <-c.ch:
		t.Fatalf("unexpected extra frame: %s", m.data)
	default:
	}
}

// Owner 2026-10-19 "thiếu show hp của city, mới thấy show hp của thành
// chính": per-city HP bars ride the SSE frame from the merged standing
// rows — hp-less rows skipped, variants kept apart.
func TestGICityHPMerged(t *testing.T) {
	mk := func(name string, norm, peak map[int32]giCityRow) *EventPlayer {
		ep := &EventPlayer{Username: name, ChatID: 5, Running: true}
		ep.giSlot(GIEventNormal).ActivityID = 42
		ep.giSlot(GIEventPeak).ActivityID = 43
		ep.GodIslandMu.Lock()
		ep.GiEvents[GIEventNormal].Roster = norm
		if peak != nil {
			ep.GiEvents[GIEventPeak].Roster = peak
		}
		ep.GodIslandMu.Unlock()
		return ep
	}
	oldT := time.Now().Add(-10 * time.Minute)
	ep1 := mk("ev1", map[int32]giCityRow{1001: {HP: 500, Cur: 320, Seen: oldT}, 1002: {HP: 0}}, nil)
	ep2 := mk("ev2", map[int32]giCityRow{1001: {HP: 500, Cur: 90, Dead: true, Seen: time.Now()}, 1003: {HP: 400, Cur: 400, Seen: time.Now()}},
		map[int32]giCityRow{9001: {HP: 700, Cur: 1, Seen: time.Now()}})
	tt := &TeleBot{}
	tt.session.Store(int64(5), ep1)
	tt.session.Store(int64(6), ep2)

	hp := tt.giCityHPMerged(5, GIEventNormal)
	if len(hp) != 2 {
		t.Fatalf("want 2 HP rows (hp=0 skipped), got %+v", hp)
	}
	// ep2 heard the fresher 1abe damage — the row SEEN LAST wins; dead carried.
	if r := hp["1001"]; r.HP != 500 || r.Cur != 90 || !r.Dead {
		t.Errorf("merge seen-last-wins broken: %+v", r)
	}
	if r := hp["1003"]; r.HP != 400 || r.Cur != 400 || r.Dead {
		t.Errorf("second account rows lost: %+v", r)
	}
	if _, leak := hp["9001"]; leak {
		t.Error("peak row leaked into bonglai")
	}
	// Variant isolation the other way, and the empty answer is nil (the
	// frame field must simply be absent, not an empty object).
	peak := tt.giCityHPMerged(5, GIEventPeak)
	if len(peak) != 1 || peak["9001"].HP != 700 {
		t.Errorf("peak merge: %+v", peak)
	}
	if tt.giCityHPMerged(int64(99), GIEventNormal) != nil {
		t.Error("unknown chat must answer nil")
	}
}
