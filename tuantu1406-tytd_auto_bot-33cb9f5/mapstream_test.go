package tytd_go

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/gimap"
)

// Map streaming: streamer pick purity, subscription lifecycle, and the
// per-account eligibility gate that keeps member pulls undisturbed.

func TestMapSubsLifecycle(t *testing.T) {
	s := &mapSubs{by: map[int64]map[string]*mapSub{}}
	const chat = int64(4242)
	entry := func(ck int64, which string) *mapSub {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.by[ck][which]
	}

	sub, start := s.extend(chat, "cuuu")
	if !start || sub == nil {
		t.Fatal("first extend must ask to start a loop")
	}
	if _, start := s.extend(chat, "cuuu"); start {
		t.Fatal("heartbeat while running must not start a second loop")
	}
	if !s.alive(sub) {
		t.Fatal("fresh sub must be alive")
	}
	if entry(chat, "ditich") != nil || entry(999, "cuuu") != nil {
		t.Fatal("unknown subs registered entries")
	}

	s.release(chat, "cuuu")
	if s.alive(sub) {
		t.Fatal("released sub still alive")
	}
	// The loop notices and retires BY DELETING its own entry (audit #5:
	// finished subs used to pile up per (viewer, map) forever):
	s.finish(chat, "cuuu", sub)
	if entry(chat, "cuuu") != nil {
		t.Fatal("finish did not delete the retired entry")
	}
	sub2, start := s.extend(chat, "cuuu")
	if !start {
		t.Fatal("extend after retire must start a fresh loop")
	}
	// The old loop exiting LATE must not evict the sub it never owned:
	s.finish(chat, "cuuu", sub)
	if entry(chat, "cuuu") != sub2 {
		t.Fatal("stale finish evicted the fresh subscription")
	}

	// TTL expiry path without waiting 20 s:
	s.mu.Lock()
	sub2.deadline = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if s.alive(sub2) {
		t.Fatal("expired deadline reported alive")
	}
}

func TestMapStreamerEligible(t *testing.T) {
	now := time.Now()

	gi := &Player{}
	if t0 := (&TeleBot{}); t0.mapStreamerEligible(gi, gimap.StreamBonglai) {
		t.Fatal("gi eligible without activity id")
	}
	gi.GiActivityID = 500
	if !(&TeleBot{}).mapStreamerEligible(gi, gimap.StreamBonglai) {
		t.Fatal("gi with activity id must be eligible")
	}
	if (&TeleBot{}).mapStreamerEligible(gi, gimap.StreamThienma) {
		t.Fatal("peak must not ride the normal activity id")
	}
	gi.GiPending = make(chan []byte, 1)
	if (&TeleBot{}).mapStreamerEligible(gi, gimap.StreamBonglai) {
		t.Fatal("gi with a pending request must be skipped")
	}
	gi.GiPending = nil
	gi.GiPeakActivityID = 501
	if !(&TeleBot{}).mapStreamerEligible(gi, gimap.StreamThienma) {
		t.Fatal("peak with its own activity id must be eligible")
	}

	hl := &Player{}
	if (&TeleBot{}).mapStreamerEligible(hl, gimap.StreamCuuu) {
		t.Fatal("cuuu not eligible with no running round")
	}
	// The ActList 346 SEASON row alone must not put a fleet on the map —
	// streaming for an event the card gate hides wastes pulls (user
	// 2026-10-06: the row was up while nothing ran; ActList windows never
	// prove a running round, scheduleTimestamp does).
	setActivities(&hl.ActList, activityEntry{
		ActivityID: 777, Type: hlActivityTypeHolyLand,
		BeginTime: now.Add(-time.Hour).UnixMilli(), EndTime: now.Add(time.Hour).UnixMilli(),
	})
	if (&TeleBot{}).mapStreamerEligible(hl, gimap.StreamCuuu) {
		t.Fatal("season row alone must not stream")
	}
	hl.HlOpen = true
	hl.HlOpenKnown = true
	hl.HlScheduleApply = []int64{hlLiveAnchor(now)}
	if !(&TeleBot{}).mapStreamerEligible(hl, gimap.StreamCuuu) {
		t.Fatal("schedule-open + live battle window must be eligible")
	}
	hl.HlScheduleApply = []int64{hlEndedAnchor(now)}
	if (&TeleBot{}).mapStreamerEligible(hl, gimap.StreamCuuu) {
		t.Fatal("only ended windows must not stream (round over)")
	}
	hl.HlOpen = false
	hl.HlScheduleApply = []int64{hlLiveAnchor(now)}
	if (&TeleBot{}).mapStreamerEligible(hl, gimap.StreamCuuu) {
		t.Fatal("schedule-closed must not stream even inside a live window")
	}
	hl.HlOpen = true
	hl.HlPending = make(chan []byte, 1)
	if (&TeleBot{}).mapStreamerEligible(hl, gimap.StreamCuuu) {
		t.Fatal("pending holy-land request must block streaming")
	}

	bg := &Player{}
	if (&TeleBot{}).mapStreamerEligible(bg, gimap.StreamDitich) {
		t.Fatal("ditich eligible without activity id")
	}
	bg.BgActivityID = 900
	if (&TeleBot{}).mapStreamerEligible(bg, gimap.StreamDitich) {
		t.Fatal("ditich must not stream outside its event window (announce alone is not enough)")
	}
	if playerEventOpen(bg, gimap.StreamDitich) {
		t.Fatal("ditich card must not show outside its event window (announce alone is not enough)")
	}
	setActivities(&bg.ActList, activityEntry{
		ActivityID: 900, Type: activityTypeBallGVG,
		BeginTime: now.Add(time.Hour).UnixMilli(), EndTime: now.Add(3 * time.Hour).UnixMilli(),
	})
	if (&TeleBot{}).mapStreamerEligible(bg, gimap.StreamDitich) {
		t.Fatal("ditich must not stream before its window opens")
	}
	if playerEventOpen(bg, gimap.StreamDitich) {
		t.Fatal("ditich card must not show before its window opens")
	}
	setActivities(&bg.ActList, activityEntry{
		ActivityID: 900, Type: activityTypeBallGVG,
		BeginTime: now.Add(-time.Hour).UnixMilli(), EndTime: now.Add(time.Hour).UnixMilli(),
	})
	if !(&TeleBot{}).mapStreamerEligible(bg, gimap.StreamDitich) {
		t.Fatal("ditich with id and open window must be eligible")
	}
	if !playerEventOpen(bg, gimap.StreamDitich) {
		t.Fatal("ditich card must light up with an active window")
	}
	bg.BgPending = make(chan []byte, 1)
	if (&TeleBot{}).mapStreamerEligible(bg, gimap.StreamDitich) {
		t.Fatal("pending BallGVG request must block streaming")
	}
	if !playerEventOpen(bg, gimap.StreamDitich) {
		t.Fatal("card gate must ignore a busy one-shot channel (no blinking)")
	}

	st := &Player{}
	if (&TeleBot{}).mapStreamerEligible(st, gimap.StreamThuongdo) {
		t.Fatal("thuongdo eligible with no activity window")
	}
	setActivities(&st.ActList, activityEntry{
		ActivityID: 1500, Type: skyTradeActivityType,
		BeginTime: now.Add(-time.Hour).UnixMilli(), EndTime: now.Add(time.Hour).UnixMilli(),
	})
	if !(&TeleBot{}).mapStreamerEligible(st, gimap.StreamThuongdo) {
		t.Fatal("active sky trade window must be eligible")
	}
	if !playerEventOpen(st, gimap.StreamThuongdo) {
		t.Fatal("fleet card must light up with an active sky trade window")
	}
	st.stChans = map[string]chan []byte{SERVER_SKY_TRADE_ENTER_MAP: make(chan []byte, 1)}
	if (&TeleBot{}).mapStreamerEligible(st, gimap.StreamThuongdo) {
		t.Fatal("pending sky trade request must block streaming")
	}
	if !playerEventOpen(st, gimap.StreamThuongdo) {
		t.Fatal("open-card check must ignore a busy one-shot channel")
	}
	st.stChans = nil
	setActivities(&st.ActList, activityEntry{
		ActivityID: 1501, Type: skyTradeActivityType,
		BeginTime: now.Add(time.Hour).UnixMilli(), EndTime: now.Add(2 * time.Hour).UnixMilli(),
	})
	if (&TeleBot{}).mapStreamerEligible(st, gimap.StreamThuongdo) || playerEventOpen(st, gimap.StreamThuongdo) {
		t.Fatal("future sky trade window must not count")
	}

	if (&TeleBot{}).mapStreamerEligible(&Player{}, "skytrade") {
		t.Fatal("unknown which must be ineligible")
	}
}

// A ret=2100 (board closed) refusal muzzles further pulls for a cooldown,
// even while the activity list still advertises a window; the flag clears.
func TestDitichBoardClosedCooldown(t *testing.T) {
	now := time.Now()
	p := &Player{BgActivityID: 900}
	setActivities(&p.ActList, activityEntry{
		ActivityID: 900, Type: activityTypeBallGVG,
		BeginTime: now.Add(-time.Hour).UnixMilli(), EndTime: now.Add(time.Hour).UnixMilli(),
	})
	if !(&TeleBot{}).mapStreamerEligible(p, gimap.StreamDitich) {
		t.Fatal("open window must be eligible before any refusal")
	}
	p.bgMarkClosed(now)
	if (&TeleBot{}).mapStreamerEligible(p, gimap.StreamDitich) {
		t.Fatal("a fresh 2100 must stop the pulls")
	}
	p.bgClearClosed()
	if !(&TeleBot{}).mapStreamerEligible(p, gimap.StreamDitich) {
		t.Fatal("clearing the flag must restore streaming")
	}
}

func setActivities(a *activityStore, entries ...activityEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = map[int32]activityEntry{}
	for _, e := range entries {
		a.entries[e.ActivityID] = e
	}
}

// subEntry peeks at the raw registry entry for a (chat, which) pair —
// same-package test visibility into what the handlers registered.
func subEntry(chatID int64, which string) *mapSub {
	webMapSubs.mu.Lock()
	defer webMapSubs.mu.Unlock()
	return webMapSubs.by[chatID][which]
}

func dropSubs(chatID int64, whichs ...string) {
	webMapSubs.mu.Lock()
	defer webMapSubs.mu.Unlock()
	m := webMapSubs.by[chatID]
	for _, w := range whichs {
		delete(m, w)
	}
}

func TestWebEventMapSubValidation(t *testing.T) {
	const chat = int64(31337)
	defer dropSubs(chat, "bonglai", "nope")

	// Unknown which: refused with 400 and NOTHING registered — the
	// zero-outbound invariant starts at input validation.
	r := httptest.NewRequest(http.MethodGet, "/events/mapsub?which=nope&on=1", nil)
	w := httptest.NewRecorder()
	(&TeleBot{}).webEventMapSub(w, r, chat, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown which: %d", w.Code)
	}
	if subEntry(chat, "nope") != nil {
		t.Fatal("unknown which registered a subscription")
	}

	r = httptest.NewRequest(http.MethodGet, "/events/mapsub?which=bonglai&on=1", nil)
	w = httptest.NewRecorder()
	(&TeleBot{}).webEventMapSub(w, r, chat, nil)
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("subscribe: %d %q", w.Code, w.Body.String())
	}
	sub := subEntry(chat, "bonglai")
	if sub == nil || !webMapSubs.alive(sub) {
		t.Fatal("subscribe did not register")
	}

	r = httptest.NewRequest(http.MethodGet, "/events/mapsub?which=bonglai&on=0", nil)
	w = httptest.NewRecorder()
	(&TeleBot{}).webEventMapSub(w, r, chat, nil)
	if w.Code != http.StatusOK || webMapSubs.alive(sub) {
		t.Fatalf("unsubscribe: %d alive=%v", w.Code, webMapSubs.alive(sub))
	}

	// The retired loop's finish() must delete its own entry (audit #5) and
	// must NOT evict the fresh sub a racing heartbeat installed (identity).
	webMapSubs.finish(chat, "bonglai", sub)
	if subEntry(chat, "bonglai") != nil {
		t.Fatal("finish did not delete its own entry")
	}
	fresh, start := webMapSubs.extend(chat, "bonglai")
	if fresh == nil || !start {
		t.Fatal("heartbeat after finish did not start a fresh loop")
	}
	webMapSubs.finish(chat, "bonglai", sub) // stale loop exiting late
	if subEntry(chat, "bonglai") != fresh {
		t.Fatal("stale finish evicted the fresh subscription")
	}
	webMapSubs.finish(chat, "bonglai", fresh)
}

// /events/mapstatic: input validated before any work; the offline maps
// (Di Tích ring, embedded Cửu U layout) must answer with a real scene;
// God Island answers when the CDN is reachable (the page's own silent
// fallback covers the 502 otherwise).
func TestWebEventMapStatic(t *testing.T) {
	serve := func(q string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/events/mapstatic?"+q, nil)
		w := httptest.NewRecorder()
		(&TeleBot{}).webEventMapStatic(w, r, 1, nil)
		return w
	}
	if w := serve("which=nope"); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown map: %d", w.Code)
	}
	if w := serve(""); w.Code != http.StatusBadRequest {
		t.Fatalf("missing which: %d", w.Code)
	}
	if w := serve("which=ditich"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"worldW"`) {
		t.Fatalf("ditich static: %d %s", w.Code, w.Body.String())
	}
	if w := serve("which=cuuu"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"cities"`) {
		t.Fatalf("cuuu static: %d", w.Code)
	}
	if w := serve("which=thuongdo"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "Thanh Minh Cung") || !strings.Contains(w.Body.String(), `"links"`) {
		t.Fatalf("thuongdo static: %d %s", w.Code, w.Body.String())
	}
	if w := serve("which=bonglai"); w.Code == http.StatusOK {
		if !strings.Contains(w.Body.String(), `"tiles"`) {
			t.Fatal("gi static without terrain tiles")
		}
	} else if w.Code != http.StatusBadGateway {
		t.Fatalf("bonglai static: %d", w.Code) // 502 = CDN unreachable, page falls back
	}
}
