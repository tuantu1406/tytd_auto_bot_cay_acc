package tytd_go

// Generalized stream-source ordering (2026-10-04): EVERY map now shares the
// Cửu U rule — leader's own account first, then a RANDOM ONLINE event
// account, then members — with per-candidate failthrough inside one tick.
// Thương Đồ has no event pool (event accounts never sail) and keeps the
// sticky member. These tests cover the new candidate builder and the event
// seats that make it possible.

import (
	"context"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/gimap"

	"google.golang.org/protobuf/encoding/protowire"
)

func newGIEventAccount(uid, chatID int64) *EventPlayer {
	ctx, cancel := context.WithCancel(context.Background())
	p := &EventPlayer{UID: uint64(uid), ChatID: chatID, Running: true}
	p.ctx = ctx
	p.cancel = cancel
	p.writeChan = make(chan []byte, 2)
	p.GiEvents[GIEventNormal] = &giEvent{ActivityID: 901}
	p.GiEvents[GIEventPeak] = &giEvent{ActivityID: 902}
	return p
}

func newBGEventAccount(uid, chatID int64) *EventPlayer {
	p := newGIEventAccount(uid, chatID)
	p.GiEvents[GIEventNormal], p.GiEvents[GIEventPeak] = nil, nil
	p.bg = &ballGVGState{ActivityID: 903}
	setActivities(&p.ActList, activityEntry{
		ActivityID: 903, Type: activityTypeBallGVG,
		BeginTime: time.Now().Add(-time.Hour).UnixMilli(), EndTime: time.Now().Add(time.Hour).UnixMilli(),
	})
	return p
}

func newGIPlayer(chatID int64, running bool) *Player {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Player{UID: uint64(chatID), ChatID: chatID, Running: running, GiActivityID: 901, GiPeakActivityID: 902}
	p.ctx = ctx
	p.cancel = cancel
	p.writeChan = make(chan []byte, 2)
	return p
}

// streamCandidates must lead with the leader's session, then event accounts,
// skipping foreign/offline/busy/kicked ones.
func TestStreamCandidatesMainFirstThenEvents(t *testing.T) {
	bot := &TeleBot{}
	chat := int64(7)
	main := newGIPlayer(chat, true)
	bot.session.Store(chat, main)
	good := newGIEventAccount(8000000001, chat)
	busy := newGIEventAccount(8000000002, chat)
	busy.giMiniPending = make(chan []byte, 1)
	kicked := newGIEventAccount(8000000003, chat)
	kicked.GiEvents[GIEventNormal].LeftOut = true
	off := newGIEventAccount(8000000004, chat)
	off.Running = false
	foreign := newGIEventAccount(8000000005, 99)
	for _, ep := range []*EventPlayer{good, busy, kicked, off, foreign} {
		bot.session.Store(int64(ep.UID), ep)
	}

	cands := bot.streamCandidates(chat, gimap.StreamBonglai, nil)
	if len(cands) != 2 {
		t.Fatalf("candidates %d want 2 (main + good event)", len(cands))
	}
	if cands[0].pl != main || !cands[0].viewer {
		t.Fatal("leader's own account must lead the list")
	}
	if cands[1].pl != nil || cands[1].viewer {
		t.Fatal("event candidate must carry no *Player and never be the viewer")
	}
}

// Thiên Ma streams through the PEAK slot (ActivityID + LeftOut per variant).
func TestStreamCandidatesThienmaUsesPeakSlot(t *testing.T) {
	bot := &TeleBot{}
	chat := int64(7)
	ep := newGIEventAccount(8000000001, chat)
	ep.GiEvents[GIEventPeak].LeftOut = true // only the peak variant is out
	bot.session.Store(int64(ep.UID), ep)

	if got := bot.streamCandidates(chat, gimap.StreamBonglai, nil); len(got) != 1 {
		t.Fatalf("bonglai must still stream while only peak is kicked: %+v", got)
	}
	if got := bot.streamCandidates(chat, gimap.StreamThienma, nil); len(got) != 0 {
		t.Fatalf("kicked-from-peak account must not stream Thiên Ma: %+v", got)
	}
}

// Thương Đồ keeps its regular-only pool: event accounts never appear.
func TestStreamCandidatesThuongdoHasNoEventPool(t *testing.T) {
	bot := &TeleBot{}
	chat := int64(7)
	bot.session.Store(chat, newGIPlayer(chat, false)) // main offline
	ep := newGIEventAccount(8000000001, chat)
	bot.session.Store(int64(ep.UID), ep)
	if got := bot.streamCandidates(chat, gimap.StreamThuongdo, nil); len(got) != 0 {
		t.Fatalf("event accounts must never stream Thương Đồ: %+v", got)
	}
}

// A failing main pull falls through to the event account WITHIN the tick:
// main fails instantly (dead ctx, full writeChan), the event seat answers
// with a valid (ret=0, no places) Di Tích summary.
func TestMapStreamPayloadFallsThroughToEvent(t *testing.T) {
	bot := &TeleBot{}
	chat := int64(7)
	main := newGIPlayer(chat, true)
	main.BgActivityID = 903
	setActivities(&main.ActList, activityEntry{
		ActivityID: 903, Type: activityTypeBallGVG,
		BeginTime: time.Now().Add(-time.Hour).UnixMilli(), EndTime: time.Now().Add(time.Hour).UnixMilli(),
	})
	main.writeChan <- []byte{}
	main.writeChan <- []byte{} // writer goroutine dead: sends never complete
	main.cancel()              // the ctx branch fires immediately
	bot.session.Store(chat, main)

	ep := newBGEventAccount(8000000001, chat)
	go func() {
		for frame := range ep.writeChan {
			if frameFuncType(t, frame) == BALL_GVG_GET_PLACE_CMD {
				ep.bgDeliverMap(bgPlacesBlob(0, 1))
			}
		}
	}()
	bot.session.Store(int64(ep.UID), ep)

	res, err := bot.mapStreamPayload(chat, gimap.StreamDitich, nil)
	if err != nil {
		t.Fatalf("event account must answer after main fails: %v", err)
	}
	if res.sm == nil || res.sm.Which != gimap.StreamDitich || res.pulled != nil {
		t.Fatalf("bad result from event pull: %+v", res)
	}
}

// All candidates failing must surface ONE aggregate error (the loop logs it
// and pushes the fixed user-facing note).
func TestMapStreamPayloadAllFail(t *testing.T) {
	bot := &TeleBot{}
	chat := int64(7)
	dead := newGIEventAccount(8000000001, chat)
	dead.writeChan <- []byte{}
	dead.writeChan <- []byte{}
	dead.cancel()
	bot.session.Store(int64(dead.UID), dead)

	if _, err := bot.mapStreamPayload(chat, gimap.StreamBonglai, nil); err == nil ||
		!strings.Contains(err.Error(), "1 ứng viên đều lỗi") {
		t.Fatalf("expected aggregate failure, got %v", err)
	}
}

// Event GI seat: the wire carries 206834 with the variant's activityId, the
// delivered answer lands on the seat, and the seat frees afterwards.
func TestEventGISeatRoundTrip(t *testing.T) {
	ep := newGIEventAccount(8000000001, 7)
	want := []byte("minimap-payload")
	go func() {
		frame := <-ep.writeChan
		if got := frameFuncType(t, frame); got != GOD_ISLAND_MINIMAP_CMD {
			t.Errorf("wire cmd %s want %s", got, GOD_ISLAND_MINIMAP_CMD)
		}
		ep.giDeliverMiniMap(want)
	}()
	got, err := ep.RequestGodIslandMiniMap(GIEventPeak, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("payload %q want %q", got, want)
	}
	if ep.giMiniPending != nil {
		t.Fatal("seat must be released")
	}
}

// Event BG seat: same contract on 21111, sharing the reader case with the
// stay loop's own place requests.
func TestEventBGSeatRoundTrip(t *testing.T) {
	ep := newBGEventAccount(8000000001, 7)
	want := bgPlacesBlob(0, 1)
	go func() {
		frame := <-ep.writeChan
		if got := frameFuncType(t, frame); got != BALL_GVG_GET_PLACE_CMD {
			t.Errorf("wire cmd %s want %s", got, BALL_GVG_GET_PLACE_CMD)
		}
		ep.bgDeliverMap(want)
	}()
	got, err := ep.RequestBallGVGMap(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("payload %v want %v", got, want)
	}
	if !eventBgEligible(ep) {
		t.Fatal("open window must be eligible once the seat frees")
	}
}

// Half-dead connections (writer gone, writeChan full) must fail FAST on both
// new event seats — the streamer loop may never block on the wire send.
func TestEventMapSeatsFailFastOnDeadConnection(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*EventPlayer) error
	}{
		{"gi", func(p *EventPlayer) error { _, e := p.RequestGodIslandMiniMap(GIEventNormal, 5*time.Second); return e }},
		{"bg", func(p *EventPlayer) error { _, e := p.RequestBallGVGMap(5*time.Second); return e }},
	} {
		ep := newBGEventAccount(8000000001, 7)
		ep.GiEvents[GIEventNormal] = &giEvent{ActivityID: 901}
		ep.writeChan <- []byte{}
		ep.writeChan <- []byte{} // 2-slot buffer full: sends block forever…
		ep.cancel()             // …unless the ctx branch fires
		done := make(chan error, 1)
		go func() { done <- tc.call(ep) }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "rớt kết nối") {
				t.Fatalf("%s: fast-fail error expected, got %v", tc.name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: seat blocked on a dead connection", tc.name)
		}
	}
}

// Deliver without a seat must never block or panic (reader hot path).
func TestEventMapDeliverNoSeat(t *testing.T) {
	ep := newGIEventAccount(8000000001, 7)
	done := make(chan struct{})
	go func() {
		ep.giDeliverMiniMap([]byte("x"))
		ep.bgDeliverMap([]byte("y"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deliver without a seat blocked")
	}
}

func bgPlacesBlob(ret int32, places ...int32) []byte {
	msg := protowire.AppendTag(nil, 1, protowire.VarintType)
	msg = protowire.AppendVarint(msg, uint64(ret))
	for _, pid := range places {
		inner := protowire.AppendTag(nil, 1, protowire.VarintType)
		inner = protowire.AppendVarint(inner, uint64(pid))
		msg = protowire.AppendTag(msg, 2, protowire.BytesType)
		msg = protowire.AppendBytes(msg, inner)
	}
	return msg
}
