package tytd_go

// Cửu U stream-source selection: leader's own account first, then a random
// ONLINE EVENT account (only battlefield residents get a 209526 answer),
// with per-candidate fallback inside a single tick.

import (
	"context"
	"testing"
	"time"
	"tytd_go/internal/gimap"

	pb "google.golang.org/protobuf/proto"

	"tytd_go/internal/proto"
)

// hlGoodPayload is a valid 209526 answer; hlRejectPayload mimics the
// ret=9505 "not resident" rejection.
func hlGoodPayload(t *testing.T) []byte {
	t.Helper()
	return mustMarshal(t, &proto.HolyLandGameMiniMapInfoRespMsg{
		Ret: pb.Int32(0),
		CityInfo: []*proto.HolyLandGameMiniCityInfo{{
			CityId:   pb.Int32(1001),
			BaseData: &proto.HolyLandMiniUnionInfo{UnionId: pb.Int64(77), Name: pb.String("bang")},
		}},
	})
}

func hlRejectPayload(t *testing.T) []byte {
	t.Helper()
	return mustMarshal(t, &proto.HolyLandGameMiniMapInfoRespMsg{Ret: pb.Int32(9505)})
}

// pullStub answers one RequestHolyLandMiniMap per goroutine: read the frame
// the account puts on the wire, deliver resp on its seat.
func pullStubPlayer(p *Player, resp []byte) {
	go func() {
		<-p.writeChan
		p.hlDeliverMiniMap(resp)
	}()
}

// pullStubEventAnswer answers every frame the event account sends: minimap
// with minimapResp, the 209522 route snapshot with routesResp (and checks
// the bracket was filled in).
func pullStubEventAnswer(t *testing.T, p *EventPlayer, minimapResp, routesResp []byte) {
	t.Helper()
	go func() {
		for frame := range p.writeChan {
			switch frameFuncType(t, frame) {
			case HOLY_LAND_MINIMAP_CMD:
				p.hlDeliverMiniMap(minimapResp)
			case HOLY_LAND_CITY_INFO_CMD:
				if routesResp == nil {
					continue
				}
				var req proto.HolyLandGameCityInfoReqMsg
				if pb.Unmarshal(frame[18:], &req) != nil || req.GetGameType() == 0 || req.GetGroupId() == 0 || len(req.GetCityVec()) != 34 {
					t.Errorf("209522 malformed: gt=%d group=%d cities=%d", req.GetGameType(), req.GetGroupId(), len(req.GetCityVec()))
				}
				p.hlDeliverRoutes(routesResp)
			}
		}
	}()
}

// hlRoutesBlob builds a 209522 answer with one march 1001→2002 by union 77.
func hlRoutesBlob(t *testing.T) []byte {
	t.Helper()
	now := time.Now().UnixMilli()
	return mustMarshal(t, &proto.HolyLandGameCityInfoRespMsg{
		Ret: pb.Int32(0),
		RouteInfo: []*proto.HolyLandGameRouteInfo{{
			PlayerId: pb.Int64(1), FromCityId: pb.Int32(1001), ToCityId: pb.Int32(2002),
			Time: pb.Int64(now), Arrival: pb.Int64(now + 30000), UnionId: pb.Int64(77), Name: pb.String("MeoMu"),
		}},
	})
}

func newMainPlayer(chatID int64, open, running bool) *Player {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Player{UID: uint64(chatID), ChatID: chatID, Running: running, HlOpen: open}
	if open {
		// The display/stream gate needs a RUNNING round, not just the flag.
		p.HlOpenKnown = true
		p.HlScheduleApply = []int64{hlLiveAnchor(time.Now())}
	}
	p.ctx = ctx
	p.cancel = cancel
	p.writeChan = make(chan []byte, 2)
	return p
}

func newEventAccount(uid int64, chatID int64) *EventPlayer {
	ctx, cancel := context.WithCancel(context.Background())
	p := &EventPlayer{UID: uint64(uid), ChatID: chatID, Running: true, hlOpen: true,
		hl: &holyLandState{
			IsOpen: true, OpenKnown: true,
			ScheduleApply: []int64{hlLiveAnchor(time.Now())},
		}}
	p.ctx = ctx
	p.cancel = cancel
	p.writeChan = make(chan []byte, 2)
	return p
}

func TestCuuuCandidatesMainFirstThenEvents(t *testing.T) {
	bot := &TeleBot{}
	chat := int64(7)
	main := newMainPlayer(chat, true, true)
	bot.session.Store(chat, main)
	ev1 := newEventAccount(8000000001, chat)
	ev2 := newEventAccount(8000000002, chat)
	bot.session.Store(int64(8000000001), ev1)
	bot.session.Store(int64(8000000002), ev2)
	// Another leader's event account must never show up.
	bot.session.Store(int64(8000000003), newEventAccount(8000000003, 99))

	cands := bot.cuuuCandidates(chat)
	if len(cands) != 3 {
		t.Fatalf("candidates %d want 3", len(cands))
	}
	if cands[0] != hlMiniPuller(main) {
		t.Fatal("leader's own account must lead the pick")
	}
	for _, c := range cands[1:] {
		ep, ok := c.(*EventPlayer)
		if !ok || ep.ChatID != chat {
			t.Fatalf("non-event/foreign candidate in tail: %T", c)
		}
	}
}

func TestCuuuCandidatesMainOfflineRandomsEvents(t *testing.T) {
	bot := &TeleBot{}
	chat := int64(7)
	bot.session.Store(chat, newMainPlayer(chat, true, false)) // offline
	ev := newEventAccount(8000000001, chat)
	bot.session.Store(int64(8000000001), ev)

	cands := bot.cuuuCandidates(chat)
	if len(cands) != 1 || cands[0] != hlMiniPuller(ev) {
		t.Fatalf("offline main must leave the event account: %v", cands)
	}
}

func TestCuuuCandidatesSkipsBusyEvent(t *testing.T) {
	bot := &TeleBot{}
	chat := int64(7)
	ev := newEventAccount(8000000001, chat)
	ev.hlMapPending = make(chan []byte, 1) // a pull already in flight
	bot.session.Store(int64(8000000001), ev)
	if cands := bot.cuuuCandidates(chat); len(cands) != 0 {
		t.Fatalf("busy event account must be skipped: %v", cands)
	}
}

func TestCuuuStreamPayloadFallsThroughReject(t *testing.T) {
	bot := &TeleBot{}
	chat := int64(7)
	main := newMainPlayer(chat, true, true)
	ev := newEventAccount(8000000001, chat)
	bot.session.Store(chat, main)
	bot.session.Store(int64(8000000001), ev)

	pullStubPlayer(main, hlRejectPayload(t)) // main online but NOT resident
	bracket := ev.hlState()
	ev.HolyMu.Lock()
	bracket.Group, bracket.GameType = 200004, 1
	ev.HolyMu.Unlock()
	pullStubEventAnswer(t, ev, hlGoodPayload(t), hlRoutesBlob(t))

	res, err := bot.cuuuStreamPayload(chat)
	if err != nil {
		t.Fatalf("fallthrough failed: %v", err)
	}
	if res.idle || res.sm == nil {
		t.Fatalf("want a frame, got %+v", res)
	}
	var found bool
	for _, c := range res.sm.Cities {
		if c.ID == 1001 && c.Owner == "bang" {
			found = true
			if c.Color == "" {
				t.Error("event frame lost its union color")
			}
		}
	}
	if !found {
		t.Fatal("frame does not carry the event account's answer")
	}
	if len(res.sm.Routes) != 1 {
		t.Fatalf("routes %d want 1 (marching snapshot lost)", len(res.sm.Routes))
	}
	rt := res.sm.Routes[0]
	if rt.From != 1001 || rt.To != 2002 || rt.Name != "MeoMu" || rt.Color != "#f5553b" || rt.Arrive <= rt.Start {
		t.Fatalf("route %+v", rt)
	}
}

func TestCuuuStreamPayloadNoRoutesWithoutBracket(t *testing.T) {
	bot := &TeleBot{}
	chat := int64(7)
	ev := newEventAccount(8000000001, chat) // no bracket cached
	bot.session.Store(int64(8000000001), ev)
	go func() {
		frame := <-ev.writeChan
		if frameFuncType(t, frame) != HOLY_LAND_MINIMAP_CMD {
			t.Errorf("unexpected second frame before bracket known")
		}
		ev.hlDeliverMiniMap(hlGoodPayload(t))
	}()
	res, err := bot.cuuuStreamPayload(chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.sm.Routes) != 0 {
		t.Fatal("routes without bracket must be empty, not fabricated")
	}
}

func TestCuuuStreamPayloadIdleWhenNobody(t *testing.T) {
	bot := &TeleBot{}
	res, err := bot.cuuuStreamPayload(7)
	if err != nil {
		t.Fatal(err)
	}
	if !res.idle {
		t.Fatalf("no online account must mean idle: %+v", res)
	}
}

func TestEventHLRequestMiniMapSeat(t *testing.T) {
	p := newEventAccount(8000000009, 7)
	want := hlGoodPayload(t)
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
	if string(got) != string(want) {
		t.Fatal("payload mismatch")
	}
	p.HolyMu.Lock()
	pending := p.hlMapPending
	p.HolyMu.Unlock()
	if pending != nil {
		t.Fatal("seat not cleared")
	}

	// busy seat refuses without touching the wire
	p.HolyMu.Lock()
	p.hlMapPending = make(chan []byte, 1)
	p.HolyMu.Unlock()
	if _, err := p.RequestHolyLandMiniMap(time.Second); err == nil {
		t.Fatal("expected busy error")
	}
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("busy request queued %d frames", n)
	}
}

func TestEventHLOpenGates(t *testing.T) {
	p := &EventPlayer{}
	if eventHlOpen(p) || eventHlEligible(p) {
		t.Fatal("closed account must not stream")
	}
	p.hlOpen = true
	if eventHlEligible(p) {
		t.Fatal("announcement alone (no day anchors) must not stream")
	}
	p.hl = &holyLandState{ScheduleTs: []int64{hlLiveAnchor(time.Now())}}
	if !eventHlEligible(p) {
		t.Fatal("announced event inside a pushed battle window opens the gate")
	}
	p.HolyMu.Lock()
	p.hlMapPending = make(chan []byte, 1)
	p.HolyMu.Unlock()
	if eventHlEligible(p) {
		t.Fatal("busy seat must not be eligible")
	}
}

// sanity: the stream frame for an event account uses the exact client colors.
func TestCuuuStreamFrameColorsFollowClientRule(t *testing.T) {
	payload := hlGoodPayload(t)
	sm, err := gimap.Stream(gimap.StreamCuuu, payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range sm.Cities {
		if c.ID == 1001 {
			// 1001 is the first main scanned, its union 77 enters unionIdVec
			// at index 0 (then 0 for the unowned rest) → colorVec[0].
			if c.Color != "#f5553b" {
				t.Fatalf("city 1001 color %s want #f5553b (client vec index 0)", c.Color)
			}
		}
	}
}

func TestPlayerHLRoutesAndLineSeats(t *testing.T) {
	p := newMainPlayer(7, true, true)
	want := hlRoutesBlob(t)
	go func() {
		frame := <-p.writeChan
		if got := frameFuncType(t, frame); got != HOLY_LAND_CITY_INFO_CMD {
			t.Errorf("routes cmd %s", got)
		}
		p.hlDeliverRoutes(want)
	}()
	got, err := p.RequestHolyLandCityRoutes(1, 200004, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("routes payload mismatch")
	}

	line := mustMarshal(t, &proto.HolyLandGameTargetCityLineInfoRespMsg{Ret: pb.Int32(0)})
	go func() {
		frame := <-p.writeChan
		if got := frameFuncType(t, frame); got != HOLY_LAND_TARGET_LINE_CMD {
			t.Errorf("line cmd %s", got)
		}
		p.hlDeliverLine(line)
	}()
	if _, err := p.RequestHolyLandCityLine(1001, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	// Seats are independent: a busy routes seat does not block the line.
	p.HlMu.Lock()
	p.HlRoutePending = make(chan []byte, 1)
	p.HlMu.Unlock()
	if _, err := p.RequestHolyLandCityRoutes(1, 2, time.Second); err == nil {
		t.Error("busy routes seat must refuse")
	}
	if len(p.writeChan) != 0 {
		t.Error("refused request must not touch the wire")
	}
}

func TestWebEventMapCityAnswerSkipsRejections(t *testing.T) {
	bad := mustMarshal(t, &proto.HolyLandGameTargetCityLineInfoRespMsg{Ret: pb.Int32(9505)})
	good := mustMarshal(t, &proto.HolyLandGameTargetCityLineInfoRespMsg{
		Ret: pb.Int32(0),
		AttackInfo: []*proto.HolyLandGameLinePlayerInfo{
			{Name: pb.String("KuTai"), UnionName: pb.String("Bạch Hổ")},
		},
		DefendInfo: []*proto.HolyLandGameLinePlayerInfo{
			{Name: pb.String("Thủ vệ thành"), Npc: pb.Bool(true)},
		},
	})
	res, err := webEventMapCityAnswer([]hlLinePuller{&fakeLinePuller{resp: bad}, &fakeLinePuller{resp: good}}, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Attackers) != 1 || res.Attackers[0].Name != "KuTai" || !res.Defenders[0].NPC {
		t.Fatalf("rows %+v", res)
	}
	if _, err := webEventMapCityAnswer(nil, 1001); err == nil {
		t.Fatal("no pullers must error, not fabricate a panel")
	}
}

type fakeLinePuller struct {
	resp []byte
	err  error
}

func (f *fakeLinePuller) RequestHolyLandCityLine(int32, time.Duration) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// A half-dead session (writer goroutine gone, buffered writeChan full) must
// fail fast — a blocking send there once froze the entire web map streamer
// loop, leaving the page at "đang chờ dữ liệu…".
func TestHLSeatSendFailsFastOnDeadConnection(t *testing.T) {
	p := newMainPlayer(7, true, true)
	p.writeChan <- []byte("x")
	p.writeChan <- []byte("x") // buffer full: any blocking send would hang
	p.cancel()                 // connection is dead

	done := make(chan error, 1)
	go func() {
		_, err := p.RequestHolyLandMiniMap(5 * time.Second)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dead session must error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestHolyLandMiniMap hung on a dead session (blocking write)")
	}
}

func TestEventHLRouteSendFailsFastOnDeadConnection(t *testing.T) {
	p := newEventAccount(8000000001, 7)
	st := p.hlState()
	p.HolyMu.Lock()
	st.Group, st.GameType = 200004, 1
	p.HolyMu.Unlock()
	p.writeChan <- []byte("x")
	p.writeChan <- []byte("x")
	p.cancel()

	done := make(chan error, 1)
	go func() {
		_, err := p.RequestHolyLandCityRoutes(5 * time.Second)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dead event session must error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("routes request hung on a dead event session")
	}
}
