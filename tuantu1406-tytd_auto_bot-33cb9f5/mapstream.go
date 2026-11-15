package tytd_go

// Live event-map streaming to the web page. While /events is open the page
// heartbeats a subscription per visible map card; this side runs one loop
// per (leader, map) that every few seconds pulls the same one-shot minimap
// the Telegram map commands use and pushes the decoded geometry as an SSE
// "map" frame for the browser to draw with the game's own art.
//
// Source order, shared by EVERY map (see streamCandidates): the leader's own
// account first, then a RANDOM ONLINE event account (the battle-map
// residents — Cửu U only answers for residents at all, see
// cuuuStreamPayload), then clone members. Thương Đồ has no event-account
// pool (event accounts never sail): leader → sticky member → random member.
// A rejected/timed-out candidate falls through within the tick, so one
// sleeping account never blacks out the card. When the page closes (or the
// tab hides), the subscription lapses, the loop retires, and the game sees
// zero extra traffic.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"
)

const (
	mapStreamTick       = 5 * time.Second
	mapSubTTL           = 20 * time.Second // page heartbeats every ~8 s
	mapStreamTimeout    = 10 * time.Second // shorter than the legacy /events/map pull
	mapStreamMaxFails   = 3                // consecutive pull failures before telling the page
	mapStreamIdleRepeat = 60 * time.Second // min gap between "event not open" frames
	mapStreamFailNote   = "không lấy được map"
)

// mapStreamWhich is the closed set of streamable maps: validated BEFORE any
// session or game state is touched, so an unknown ?which= costs nothing.
var mapStreamWhich = map[string]bool{
	gimap.StreamBonglai:  true,
	gimap.StreamThienma:  true,
	gimap.StreamCuuu:     true,
	gimap.StreamDitich:   true,
	gimap.StreamThuongdo: true,
	gimap.StreamKunlun:   true,
	gimap.StreamBacMinh:   true,
	gimap.StreamLuanHoi:   true,
	gimap.StreamSeaWarWar: true,
}

// mapSub is one (chatID, which) subscription: a heartbeat deadline plus the
// running flag that keeps exactly one streamer goroutine per pair.
type mapSub struct {
	deadline time.Time
	running  bool
}

type mapSubs struct {
	mu sync.Mutex
	by map[int64]map[string]*mapSub
}

var webMapSubs = &mapSubs{by: map[int64]map[string]*mapSub{}}

// extend marks fresh interest; it returns the sub plus true only when a
// streamer loop must be started (first heartbeat, or one after the loop
// retired — the 8 s page heartbeat re-tries automatically, so a heartbeat
// racing the loop exit self-heals). The sub is handed to the loop so its
// finish() can identify the entry it owns.
func (s *mapSubs) extend(chatID int64, which string) (*mapSub, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.by[chatID]
	if m == nil {
		m = map[string]*mapSub{}
		s.by[chatID] = m
	}
	sub := m[which]
	if sub == nil {
		sub = &mapSub{}
		m[which] = sub
	}
	sub.deadline = time.Now().Add(mapSubTTL)
	if sub.running {
		return sub, false
	}
	sub.running = true
	return sub, true
}

// release drops interest; the streamer notices on its next tick and retires.
func (s *mapSubs) release(chatID int64, which string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub := s.by[chatID][which]; sub != nil {
		sub.deadline = time.Time{}
	}
}

// alive reports whether the loop should stream another tick. It reads the
// loop's OWN sub (identity-safe even when a racing heartbeat replaced it).
func (s *mapSubs) alive(sub *mapSub) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(sub.deadline)
}

// finish retires the loop and deletes THE ENTRY IT OWNED — identity check,
// so a heartbeat that already installed a fresh sub is never evicted. Entry
// deletion (instead of leaving running=false forever) keeps finished
// subscriptions from accumulating per (viewer, map) (audit finding #5).
func (s *mapSubs) finish(chatID int64, which string, sub *mapSub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.by[chatID]; m != nil && m[which] == sub {
		delete(m, which)
	}
}

// webEventMapSub is GET /events/mapsub?which=…&on=1|0 — the page's subscribe
// heartbeat (on=1, every ~8 s while a card is visible) and goodbye (on=0 on
// pagehide). Plain 200 "ok"; unknown maps are refused before anything else
// runs, so bad input never reaches a game session.
func (t *TeleBot) webEventMapSub(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	which := r.URL.Query().Get("which")
	if !mapStreamWhich[which] {
		// Loud on purpose (bug report 2026-10-05 "no kunlun log"): an old
		// binary rejects a new page's which= with a 400 and used to say
		// nothing — the map then hangs at "đang chờ dữ liệu" with NO clue.
		// Seeing this line in a CURRENT build means a page/proxy mismatch.
		log.Printf("[web] mapsub %d: which=%q not supported by this binary — restart with the latest build", chatID, which)
		http.Error(w, "bản đồ không hợp lệ", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("on") == "0" {
		webMapSubs.release(chatID, which)
		fmt.Fprint(w, "ok")
		return
	}
	if sub, start := webMapSubs.extend(chatID, which); start {
		go t.mapStreamLoop(chatID, which, sub)
	}
	fmt.Fprint(w, "ok")
}

// SSE "map" frame bodies. One data frame embeds the gimap stream layout;
// idle/err frames are the page's only other possible states, both in game
// Vietnamese with no protocol detail.
type mapStreamFrame struct {
	TS int64 `json:"ts"`
	*gimap.StreamMap
}

type mapStreamIdleFrame struct {
	Which string `json:"which"`
	Idle  bool   `json:"idle"`
}

type mapStreamErrFrame struct {
	Which string `json:"which"`
	Err   string `json:"err"`
}

// webEventMapStatic serves GET /events/mapstatic?which=… — the card's
// static scene (terrain tiles, footprints, labels, world box) fetched once
// and kept in browser cache; live SSE frames only carry the changing owner/
// fight/palette fields after that. Errors stay plain Vietnamese like the
// rest of the web surface.
func (t *TeleBot) webEventMapStatic(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	which := r.URL.Query().Get("which")
	if !mapStreamWhich[which] {
		http.Error(w, "bản đồ không hợp lệ", http.StatusBadRequest)
		return
	}
	st, err := gimap.StreamStatic(which)
	if err != nil {
		log.Printf("[web] mapstatic %s: %v", which, err)
		http.Error(w, "không tải được nền bản đồ", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	if err := json.NewEncoder(w).Encode(st); err != nil {
		log.Printf("[web] mapstatic %s encode: %v", which, err)
	}
}

// mapStreamLoop pumps one (leader, map) subscription until the page stops
// refreshing the deadline. It never outlives its mapSub: exit goes through
// finish(), which frees the slot for a future heartbeat.
func (t *TeleBot) mapStreamLoop(chatID int64, which string, sub *mapSub) {
	log.Printf("[web] mapstream %d/%s: loop started", chatID, which)
	ticker := time.NewTicker(mapStreamTick)
	defer ticker.Stop()
	fails := 0
	var lastIdle time.Time
	var sticky *Player      // Thương Đồ: the member a fallback is streaming from
	var lastGI *gimap.StreamMap // GI: last good frame — marches ride it across pull failures
	noSSELogged, idleLogged, firstPush := false, false, false
	pump := func() bool {
		if !webMapSubs.alive(sub) {
			log.Printf("[web] mapstream %d/%s: loop retired (page stopped heartbeat)", chatID, which)
			webMapSubs.finish(chatID, which, sub)
			return false
		}
		if !webSSEHub.has(chatID) {
			if !noSSELogged {
				noSSELogged = true
				log.Printf("[web] mapstream %d/%s: no SSE connection for this chat — page is not holding /sse open", chatID, which)
			}
			return true // SSE reconnect blip: hold off rather than pull for nobody
		}
		res, err := t.mapStreamPayload(chatID, which, sticky)
		if err != nil {
			fails++
			if fails <= 2 {
				log.Printf("[web] mapstream %d/%s: pull fail #%d: %v", chatID, which, fails, err)
			}
			sticky = nil // this puller just failed: next pick moves on
			// GI marches are pure in-RAM ledger state (owner bug3
			// 2026-10-19 "lúc thấy lúc không"): a stalling 206819 minimap
			// pull must NEVER blink them off the page. Re-issue the last
			// good frame with a fresh march list and keep the failure in
			// the log; the normal err note only ships once marches run out.
			if (which == gimap.StreamBonglai || which == gimap.StreamThienma) &&
				t.giPushMarches(chatID, which, lastGI) {
				if fails == mapStreamMaxFails {
					log.Printf("[web] mapstream %d/%s: pull keeps failing (%v) — marches still served from the ledger", chatID, which, err)
				}
				return true
			}
			if fails == mapStreamMaxFails {
				// err may carry an account-side reason (also Vietnamese);
				// the page only ever sees the fixed note. The puller has
				// already applied any per-map back-off (Di Tích 2100).
				log.Printf("[web] mapstream %d/%s: %v", chatID, which, err)
				webSSEHub.push(chatID, "map", mapStreamErrFrame{Which: which, Err: mapStreamFailNote})
			}
			return true
		}
		fails = 0
		if res.idle {
			if !idleLogged {
				idleLogged = true
				log.Printf("[web] mapstream %d/%s: idle — no eligible account (event closed for every online account?)", chatID, which)
			}
			sticky = nil
			if time.Since(lastIdle) >= mapStreamIdleRepeat {
				webSSEHub.push(chatID, "map", mapStreamIdleFrame{Which: which, Idle: true})
				lastIdle = time.Now()
			}
			return true
		}
		if which == gimap.StreamThuongdo {
			sticky = res.pulled // keeps the same boat view until it breaks
			if sticky.ChatID == chatID {
				sticky = nil // leader pulls need no stickiness
			}
		}
		if !firstPush {
			firstPush = true
			log.Printf("[web] mapstream %d/%s: first frame pushed (%d cities, %d routes)", chatID, which, len(res.sm.Cities), len(res.sm.Routes))
		}
		if res.sm.Which == "" {
			res.sm.Which = which // dispatch key — never let an empty which reach the page
		}
		if which == gimap.StreamBonglai || which == gimap.StreamThienma {
			lastGI = res.sm // baseline frame for the march re-issue path above
		}
		webSSEHub.push(chatID, "map", mapStreamFrame{TS: time.Now().Unix(), StreamMap: res.sm})
		return true
	}
	// Fix 3: the FIRST pump runs the moment a subscriber opens the map —
	// waiting a whole mapStreamTick for frame 0 was part of the "one minute
	// to see anything" complaint. Cadence after that stays on the ticker.
	if !pump() {
		return
	}
	for range ticker.C {
		if !pump() {
			return
		}
	}
}

// mapStreamResult is one pull outcome: a decoded frame (plus the account it
// came from, so a Thương Đồ loop can stay sticky with that member), or
// "nothing to pull".
type mapStreamResult struct {
	sm     *gimap.StreamMap
	pulled *Player
	idle   bool
}

// streamCand is one account the live map may be pulled from THIS tick, with
// the type-neutral pull closure so regular sessions and event accounts can
// share one failthrough loop.
type streamCand struct {
	pull   func(time.Duration) ([]byte, error)
	viewer bool         // the leader's own session — the only honest "Tôi" boat
	pl     *Player      // regular session (Thương Đồ stickiness, Di Tích back-off)
	ep     *EventPlayer // event session (Di Tích roster surfacing)
	name   string       // display name for the pulled boat (regular account)
}

// playerStreamCand wraps a regular session's per-map pull.
func playerStreamCand(p *Player, which string, viewer bool) streamCand {
	name := p.Name
	if name == "" {
		name = p.Username
	}
	return streamCand{
		viewer: viewer,
		pl:     p,
		name:   name,
		pull: func(to time.Duration) ([]byte, error) {
			switch which {
			case gimap.StreamBonglai:
				return p.RequestGodIslandMiniMap(GIEventNormal, to)
			case gimap.StreamThienma:
				return p.RequestGodIslandMiniMap(GIEventPeak, to)
			case gimap.StreamDitich:
				return p.RequestBallGVGMap(to)
			case gimap.StreamThuongdo:
				return p.RequestSkyTradeMap(to)
			case gimap.StreamKunlun:
				return p.RequestKunlunMiniMap(to)
			case gimap.StreamBacMinh:
				return p.RequestBacMinhMiniMap(to)
			}
			return nil, fmt.Errorf("unknown map %q", which)
		},
	}
}

// eventGIStreamCand / eventBGStreamCand wrap an event account's island / Di
// Tích pull — the battle-map residents the page streams from while the
// leader is offline.
func eventGIStreamCand(ep *EventPlayer, giWhich int) streamCand {
	return streamCand{pull: func(to time.Duration) ([]byte, error) {
		return ep.RequestGodIslandMiniMap(giWhich, to)
	}}
}

func eventBGStreamCand(ep *EventPlayer) streamCand {
	return streamCand{ep: ep, pull: func(to time.Duration) ([]byte, error) {
		return ep.RequestBallGVGMap(to)
	}}
}

func eventKunlunStreamCand(ep *EventPlayer) streamCand {
	return streamCand{ep: ep, pull: func(to time.Duration) ([]byte, error) {
		return ep.RequestKunlunMiniMap(to)
	}}
}

func eventBacMinhStreamCand(ep *EventPlayer) streamCand {
	return streamCand{ep: ep, pull: func(to time.Duration) ([]byte, error) {
		return ep.RequestBacMinhMiniMap(to)
	}}
}

// streamCandidates builds the strict source order every map now shares (the
// one Cửu U pioneered): the leader's own account first, then RANDOM ONLINE
// event accounts, then clone members last.
//
// Battle maps (Bồng Lai, Thiên Ma, Di Tích) are identical for every account,
// so any eligible one answers — but the leader leads so the page is honest
// about their own bracket when they play, and event accounts come before
// members because they are the ones actually inside the battlefield. Thương
// Đồ is not: its snapshot marks the pulled account's own ship as "you", and
// event accounts never sail — so its pool stays leader-first, then the sticky
// member the loop streamed from last (swapping boats every tick read as
// teleporting), then a shuffled member.
func (t *TeleBot) streamCandidates(chatID int64, which string, preferred *Player) []streamCand {
	var out []streamCand
	if v, ok := t.session.Load(chatID); ok {
		if p, ok := v.(*Player); ok && p.Running && t.mapStreamerEligible(p, which) {
			out = append(out, playerStreamCand(p, which, true))
		}
	}
	if which == gimap.StreamBonglai || which == gimap.StreamThienma || which == gimap.StreamDitich {
		giWhich := GIEventNormal
		if which == gimap.StreamThienma {
			giWhich = GIEventPeak
		}
		var evs []streamCand
		t.session.Range(func(_, v any) bool {
			ep, ok := v.(*EventPlayer)
			if !ok || ep.ChatID != chatID || !ep.Running {
				return true
			}
			if which == gimap.StreamDitich {
				if eventBgEligible(ep) {
					evs = append(evs, eventBGStreamCand(ep))
				}
				return true
			}
			if eventGiEligible(ep, giWhich) {
				evs = append(evs, eventGIStreamCand(ep, giWhich))
			}
			return true
		})
		rand.Shuffle(len(evs), func(i, j int) { evs[i], evs[j] = evs[j], evs[i] })
		out = append(out, evs...)
	}
	if which == gimap.StreamKunlun {
		// Côn Lôn event accounts are the union's battle bots and the ONLY
		// march feed the leader's phone cannot block: seated ones already
		// hold scene seats (their sockets carry the 4de7 broadcasts), idle
		// ones get seated by mapStreamPayload's c.ep arm the moment they get
		// a pull turn. So EVERY event account leads, seated first; the leader
		// follows as plain fallback (219919 needs no residency) — ahead of
		// idle accounts the leader used to win every tick, no event account
		// was ever seated, and the mini app showed zero marches forever.
		var seated, idle []streamCand
		t.session.Range(func(_, v any) bool {
			ep, ok := v.(*EventPlayer)
			if !ok || ep.ChatID != chatID || !ep.Running || !ep.KunlunWarEventOpen() {
				return true
			}
			if st := ep.kuState(); st.SceneIn {
				seated = append(seated, eventKunlunStreamCand(ep))
			} else {
				idle = append(idle, eventKunlunStreamCand(ep))
			}
			return true
		})
		rand.Shuffle(len(seated), func(i, j int) { seated[i], seated[j] = seated[j], seated[i] })
		rand.Shuffle(len(idle), func(i, j int) { idle[i], idle[j] = idle[j], idle[i] })
		if ev := append(seated, idle...); len(ev) > 0 {
			out = append(ev, out...) // leader (already in out) trails everyone
		}
	}
	if which == gimap.StreamBacMinh {
		// Đỉnh Bắc Minh: same seated-first rule as Côn Lôn (219943 marches
		// only ride in-scene sockets). A fleet watching BOTH war boards keeps
		// one loop per which — an account seated for Côn Lôn still pulls the
		// north board fine (219919 needs no residency), only its march feed
		// would be kunlun-side, and the scene guards stop it from ever
		// double-seating.
		var seated, idle []streamCand
		t.session.Range(func(_, v any) bool {
			ep, ok := v.(*EventPlayer)
			if !ok || ep.ChatID != chatID || !ep.Running || !ep.BacMinhWarEventOpen() {
				return true
			}
			st := ep.bmStateEP()
			ep.BmMu.Lock()
			in := st.SceneIn
			ep.BmMu.Unlock()
			if in {
				seated = append(seated, eventBacMinhStreamCand(ep))
			} else {
				idle = append(idle, eventBacMinhStreamCand(ep))
			}
			return true
		})
		rand.Shuffle(len(seated), func(i, j int) { seated[i], seated[j] = seated[j], seated[i] })
		rand.Shuffle(len(idle), func(i, j int) { idle[i], idle[j] = idle[j], idle[i] })
		if ev := append(seated, idle...); len(ev) > 0 {
			out = append(ev, out...)
		}
	}
	if t.ydb == nil { // zero-value TeleBot (tests) has no member store
		return out
	}
	ms, err := model.GetMembersByLeader(t.ydb.GetDB(), strconv.FormatInt(chatID, 10))
	if err != nil {
		return out
	}
	var mem []streamCand
	for _, m := range ms {
		id, err := strconv.ParseInt(m.ID, 10, 64)
		if err != nil {
			continue
		}
		v, ok := t.session.Load(id)
		if !ok {
			continue
		}
		p, ok := v.(*Player)
		if !ok || !p.Running || !t.mapStreamerEligible(p, which) {
			continue
		}
		c := playerStreamCand(p, which, false)
		if preferred != nil && p == preferred {
			out = append(out, c) // sticky Thương Đồ member: same boat next
			continue
		}
		mem = append(mem, c)
	}
	rand.Shuffle(len(mem), func(i, j int) { mem[i], mem[j] = mem[j], mem[i] })
	return append(out, mem...)
}

// hlMiniPuller is one account the Cửu U minimap can be pulled from — the
// leader's Player or an EventPlayer, same one-shot contract.
type hlMiniPuller interface {
	RequestHolyLandMiniMap(timeout time.Duration) ([]byte, error)
}

// cuuuStreamPayload answers the Cửu U tick with a strict source order: the
// leader's own account first (the page is honest about their bracket when
// they play), then a RANDOM online event account — event accounts actually
// fight in Cửu U, and only battlefield residents get a minimap answer (a
// non-resident pull is rejected ret=9505) — then members as a last resort.
// A rejected/timed-out candidate falls through within the same tick, so one
// sleeping account never blacks out the card.
func (t *TeleBot) cuuuStreamPayload(chatID int64) (mapStreamResult, error) {
	cands := t.cuuuCandidates(chatID)
	if len(cands) == 0 {
		return mapStreamResult{idle: true}, nil // nobody online for this event
	}
	var lastErr error
	for _, p := range cands {
		payload, err := p.RequestHolyLandMiniMap(mapStreamTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		// Marching routes need residency too (209522 ret=9505 otherwise) —
		// ask only the account type that has a cached bracket and tolerate a
		// missing/failed snapshot: the ownership frame still renders.
		var routesBlob []byte
		if ep, ok := p.(*EventPlayer); ok {
			if blob, err := ep.RequestHolyLandCityRoutes(mapStreamTimeout); err == nil {
				routesBlob = blob
			}
		}
		sm, err := gimap.HolyLandStream(payload, routesBlob)
		if err != nil {
			lastErr = err // ret!=0 answers (e.g. 9505 not resident) try the next one
			continue
		}
		return mapStreamResult{sm: sm}, nil
	}
	if lastErr != nil {
		return mapStreamResult{}, fmt.Errorf("%d ứng viên đều lỗi, cuối cùng: %w", len(cands), lastErr)
	}
	return mapStreamResult{}, fmt.Errorf("%d ứng viên nhưng không lấy được map", len(cands))
}

// cuuuCandidates builds the ordered pull list: [main] then shuffled event
// accounts then shuffled members; every entry passed the per-map gate.
func (t *TeleBot) cuuuCandidates(chatID int64) []hlMiniPuller {
	var out []hlMiniPuller
	if v, ok := t.session.Load(chatID); ok {
		if p, ok := v.(*Player); ok && p.Running && t.mapStreamerEligible(p, gimap.StreamCuuu) {
			out = append(out, p)
		}
	}
	var evs []hlMiniPuller
	t.session.Range(func(_, v any) bool {
		if ep, ok := v.(*EventPlayer); ok && ep.ChatID == chatID && ep.Running && eventHlEligible(ep) {
			evs = append(evs, ep)
		}
		return true
	})
	rand.Shuffle(len(evs), func(i, j int) { evs[i], evs[j] = evs[j], evs[i] })
	out = append(out, evs...)
	if t.ydb != nil { // zero-value TeleBot (tests) has no member store
		if ms, err := model.GetMembersByLeader(t.ydb.GetDB(), strconv.FormatInt(chatID, 10)); err == nil {
			var mem []hlMiniPuller
			for _, m := range ms {
				id, err := strconv.ParseInt(m.ID, 10, 64)
				if err != nil {
					continue
				}
				if v, ok := t.session.Load(id); ok {
					if p, ok := v.(*Player); ok && p.Running && t.mapStreamerEligible(p, gimap.StreamCuuu) {
						mem = append(mem, p)
					}
				}
			}
			rand.Shuffle(len(mem), func(i, j int) { mem[i], mem[j] = mem[j], mem[i] })
			out = append(out, mem...)
		}
	}
	return out
}

// mapStreamPayload answers one tick for every map except Cửu U (which has
// its own route-snapshot path). The source order is strict and shared —
// leader first, then a random online event account, then members (see
// streamCandidates) — and a rejected/timed-out candidate falls through
// WITHIN the tick, so one sleeping or busy account never blacks out the
// card. Di Tích keeps its per-account back-off: a ret=2100 board-closed
// refusal marks THAT regular account, never an event account (they have no
// such flag and the board really is closed only for the asker).
func (t *TeleBot) mapStreamPayload(chatID int64, which string, preferred *Player) (mapStreamResult, error) {
	if which == gimap.StreamCuuu {
		return t.cuuuStreamPayload(chatID)
	}
	if which == gimap.StreamLuanHoi {
		return t.luanhoiStreamPayload(chatID)
	}
	if which == gimap.StreamSeaWarWar {
		return t.seawarwarStreamPayload(chatID)
	}
	cands := t.streamCandidates(chatID, which, preferred)
	if len(cands) == 0 {
		// Côn Lôn extra door: a fleet where ONLY event accounts are logged
		// in still answers 219919 — the event transport gained its own
		// minimap seats (2026-10-05 "not see kunlun log": the leader's own
		// session was an EventPlayer and no regular account existed to pull).
		if which == gimap.StreamBacMinh {
			if ep := t.eventBacMinhPuller(chatID); ep != nil {
				if id := ep.BmActivityID(); id != 0 {
					_ = ep.bmEnsureScene(id, 8*time.Second)
				}
				if raw, err := ep.RequestBacMinhMiniMap(mapStreamTimeout); err == nil {
					if sm, err := gimap.Stream(which, raw); err == nil {
						if sm.Ku == nil {
							sm.Ku = &gimap.KuFrame{}
						}
						sm.Ku.Routes = t.bmRoutesMerged(chatID, nil, ep, time.Now())
						t.bmMineOverlay(sm, chatID, raw)
						return mapStreamResult{sm: sm}, nil
					}
				}
			}
		}
		if which == gimap.StreamKunlun {
			if ep := t.eventKunlunPuller(chatID); ep != nil {
				// Sit this account IN the scene first: 219943 march broadcasts
				// (the mini app's every-player lines) only reach in-scene
				// sockets, and the follow path already leaves it there.
				if id := ep.KuKunlunActivityID(); id != 0 {
					_ = ep.kuEnsureScene(id, 8*time.Second)
				}
				if raw, err := ep.RequestKunlunMiniMap(mapStreamTimeout); err == nil {
					if sm, err := gimap.Stream(which, raw); err == nil {
						// The event account is IN the scene — its socket
						// carries the 219943 march broadcasts the mini app
						// animates (the minimap snapshot itself has no
						// route rows).
						if sm.Ku == nil {
							sm.Ku = &gimap.KuFrame{}
						}
						sm.Ku.Routes = t.kuRoutesMerged(chatID, nil, ep, time.Now())
						t.kuMineOverlay(sm, chatID, raw)
						return mapStreamResult{sm: sm}, nil
					}
				}
			}
		}
		return mapStreamResult{idle: true}, nil // event closed for everyone online
	}
	var lastErr error
	for _, c := range cands {
		payload, err := c.pull(mapStreamTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		var sm *gimap.StreamMap
		if which == gimap.StreamThuongdo && !c.viewer {
			sm, err = gimap.SkyTradeStreamAs(payload, c.name, false)
		} else {
			sm, err = gimap.Stream(which, payload)
		}
		if err != nil {
			var bgErr *gimap.BallGVGError
			if which == gimap.StreamDitich && c.pl != nil && errors.As(err, &bgErr) && bgErr.Ret == gimap.BallGVGErrBoardClosed {
				c.pl.bgMarkClosed(time.Now())
			}
			lastErr = err
			continue
		}
		if which == gimap.StreamDitich && c.pl != nil {
			c.pl.bgClearClosed() // accepted: trust the activity list again
		}
		if which == gimap.StreamDitich && c.ep != nil {
			// The puller is an event account that STANDS IN a room: surface
			// its per-player roster so the /ditich mini app can paint every
			// occupant inside that room's arena disc. Returns nil (no field)
			// when the account is lobby-side or the room has no occupants.
			sm.Tc = c.ep.bgRosterFrame(time.Now())
		}
		if which == gimap.StreamKunlun && c.pl != nil {
			if sm.Ku == nil {
				sm.Ku = &gimap.KuFrame{}
			}
			// Watching the feed == wanting every march: keeps this socket
			// in-scene (219917/15s) so 219943 streams ALL players, and
			// leaves again a minute after the last viewer closes the page.
			c.pl.kuSceneKeep(time.Now())
			sm.Ku.Routes = t.kuRoutesMerged(chatID, c.pl, nil, time.Now())
			sm.Ku.Phase = c.pl.kuPhaseInfo(time.Now())
			t.kuMineOverlay(sm, chatID, payload)
		}
		if which == gimap.StreamKunlun && c.ep != nil {
			if sm.Ku == nil {
				sm.Ku = &gimap.KuFrame{}
			}
			// Seat this account if the follow path hasn't: without a scene
			// seat its socket carries no 219943 marches. Same policy as the
			// event-only arm below — an event account staying seated is the
			// intended state (the leader's phone is never disturbed).
			if id := c.ep.KuKunlunActivityID(); id != 0 {
				_ = c.ep.kuEnsureScene(id, 8*time.Second)
			}
			sm.Ku.Routes = t.kuRoutesMerged(chatID, nil, c.ep, time.Now())
			t.kuMineOverlay(sm, chatID, payload)
		}
		if which == gimap.StreamBacMinh && c.pl != nil {
			if sm.Ku == nil {
				sm.Ku = &gimap.KuFrame{}
			}
			c.pl.bmSceneKeep(time.Now())
			sm.Ku.Routes = t.bmRoutesMerged(chatID, c.pl, nil, time.Now())
			sm.Ku.Phase = c.pl.bmPhaseInfo(time.Now())
			t.bmMineOverlay(sm, chatID, payload)
		}
		if which == gimap.StreamBacMinh && c.ep != nil {
			if sm.Ku == nil {
				sm.Ku = &gimap.KuFrame{}
			}
			if id := c.ep.BmActivityID(); id != 0 {
				_ = c.ep.bmEnsureScene(id, 8*time.Second)
			}
			sm.Ku.Routes = t.bmRoutesMerged(chatID, nil, c.ep, time.Now())
			t.bmMineOverlay(sm, chatID, payload)
		}
		if which == gimap.StreamBonglai || which == gimap.StreamThienma {
			// People on the islands (owner 2026-10-19): marches and real
			// per-city HP come from the merged RAM ledgers (the minimap
			// proto has no hp fields); standing people lists arrive through
			// the mapview twin the page asks on camera stop.
			t.giAttachPeople(sm, chatID, which)
		}
		return mapStreamResult{sm: sm, pulled: c.pl}, nil
	}
	return mapStreamResult{}, fmt.Errorf("%d ứng viên đều lỗi, cuối cùng: %w", len(cands), lastErr)
}

// giWhichOf maps the SSE which string to the GI variant slot.
func giWhichOf(which string) int {
	if which == gimap.StreamThienma {
		return GIEventPeak
	}
	return GIEventNormal
}

// giAttachPeople fills sm.Gi from the merged in-RAM ledgers for one island
// variant: march rows (giRoutesMerged) + last-known real per-city HP
// (giCityHPMerged — the 206819 minimap proto carries no hp fields, owner
// 2026-10-19 "thiếu show hp của city"). Returns whether anything was
// attached. Zero game-socket cost.
func (t *TeleBot) giAttachPeople(sm *gimap.StreamMap, chatID int64, which string) bool {
	gw := giWhichOf(which)
	gi := &gimap.GiFrame{}
	if rows := t.giRoutesMerged(chatID, gw, time.Now()); len(rows) > 0 {
		gi.Routes = rows
	}
	if hp := t.giCityHPMerged(chatID, gw); hp != nil {
		gi.Cities = hp
	}
	if gi.Routes == nil && gi.Cities == nil {
		return false
	}
	sm.Gi = gi
	return true
}

// giPushMarches re-issues the last good GI frame carrying a FRESH people
// payload while the minimap pull itself is failing: the rows come from the
// merged in-RAM ledgers (giAttachPeople — zero game-socket cost), so the
// page's marches and HP bars never blink behind a stalling 206819 (owner
// bug3 2026-10-19). The city fields ride the last successful frame — stale
// territory colour beats a blackout, and standing people lists refresh
// independently through the mapview twin on camera stop. False (→ the normal
// err path) when nothing is marching or the loop never landed a baseline.
func (t *TeleBot) giPushMarches(chatID int64, which string, last *gimap.StreamMap) bool {
	if last == nil {
		return false
	}
	if rows := t.giRoutesMerged(chatID, giWhichOf(which), time.Now()); len(rows) == 0 {
		return false // nobody marching — let the err note ship
	}
	sm := *last // shallow copy — push marshals synchronously under the hub lock
	t.giAttachPeople(&sm, chatID, which)
	webSSEHub.push(chatID, "map", mapStreamFrame{TS: time.Now().Unix(), StreamMap: &sm})
	return true
}

// kuMergeRouteLedgers gathers the Côn Lôn march ledger of EVERY account in
// this chat (leader + all running event accounts with the event open) into
// one pid→row map, newest departure winning. A single socket only ever hears
// the 219943 broadcasts that were flying while IT sat in the scene — so any
// one ledger is permanently missing lines, and rotating one puller per tick
// made a foreign march wait its account's turn (the reported ~1-minute
// "đường đã mở" latency). Merging every seated copy surfaces a march the
// instant ANY of our sockets received it.
func (t *TeleBot) kuMergeRouteLedgers(chatID int64) map[int64]kuRouteRec {
	out := map[int64]kuRouteRec{}
	merge := func(recs map[int64]kuRouteRec) {
		for pid, r := range recs {
			if cur, ok := out[pid]; !ok || r.Time >= cur.Time {
				out[pid] = r
			}
		}
	}
	if v, ok := t.session.Load(chatID); ok {
		if lp, ok := v.(*Player); ok {
			st := lp.kuState()
			lp.KuMu.Lock()
			merge(st.Routes)
			lp.KuMu.Unlock()
		}
	}
	t.session.Range(func(_, v any) bool {
		if ep, ok := v.(*EventPlayer); ok && ep.ChatID == chatID && ep.Running && ep.KunlunWarEventOpen() {
			st := ep.kuState()
			ep.KuMu.Lock()
			merge(st.Routes)
			ep.KuMu.Unlock()
		}
		return true
	})
	return out
}

// kuRoutesMerged builds frame routes from the MERGED ledgers, classified and
// coloured with the answering puller's own scene state (pl or ep, exactly
// one non-nil) — its union paints the K kinds, its 219919 answer holds the
// palette the slot numbers come from.
func (t *TeleBot) kuRoutesMerged(chatID int64, pl *Player, ep *EventPlayer, now time.Time) []gimap.KuRoute {
	recs := t.kuMergeRouteLedgers(chatID)
	switch {
	case pl != nil:
		st := pl.kuState()
		pl.KuMu.Lock()
		defer pl.KuMu.Unlock()
		return kuRoutesFrom(recs, st.MyUnion, int64(pl.UID), st.MiniRaw, st.Unions, now)
	case ep != nil:
		st := ep.kuState()
		ep.KuMu.Lock()
		defer ep.KuMu.Unlock()
		return kuRoutesFrom(recs, st.MyUnion, int64(ep.UID), st.MiniRaw, st.Unions, now)
	}
	return nil
}

// kuMineOverlay stamps the OWN side onto a kunlun frame: every cell a
// leader's event account stands on (KuFrame.Mine) and the palette slots of
// their unions (KuFrame.MySlots). The /kunlun mini app keys its radial menu
// off these — Khám phá / Càn quét only appear where an army really stands,
// because the sweep request has no target field and always acts on the
// army's current tile. raw is the same 219919 answer the frame was built
// from (slots are first-appearance order INSIDE one payload — pair them).
func (t *TeleBot) kuMineOverlay(sm *gimap.StreamMap, chatID int64, raw []byte) {
	if sm == nil {
		return
	}
	pts := map[int64]bool{}
	unions := map[int64]bool{}
	add := func(id int64) {
		v, ok := t.session.Load(id)
		if !ok {
			return
		}
		ep, ok := v.(*EventPlayer)
		if !ok {
			return
		}
		pt, un := ep.kuScenePos()
		if pt > 0 {
			pts[int64(pt)] = true
		}
		if un != 0 {
			unions[un] = true
		}
	}
	add(chatID)
	// The leader's MAIN account shares the chatID session slot but is a
	// *Player — its union's tiles belong to "team mình" for the team
	// colouring even though the leader never appears in Ku.Mine (explore/
	// sweep orders only run on event accounts actually standing there).
	if v, ok := t.session.Load(chatID); ok {
		if lp, ok := v.(*Player); ok {
			lst := lp.kuState()
			lp.KuMu.Lock()
			un := lst.MyUnion
			lp.KuMu.Unlock()
			if un != 0 {
				unions[un] = true
			}
		}
	}
	// Bare-TeleBot fixtures (stream e2e tests) carry no store: own points
	// from the session map alone still make the overlay correct.
	if t.ydb != nil {
		leaderID := strconv.FormatInt(chatID, 10)
		if users, err := model.GetEventUsersByLeader(t.ydb.GetDB(), leaderID); err == nil {
			for _, m := range users {
				if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
					add(id)
				}
			}
		}
	}
	if len(pts) == 0 && len(unions) == 0 {
		return
	}
	if sm.Ku == nil {
		sm.Ku = &gimap.KuFrame{}
	}
	for p := range pts {
		sm.Ku.Mine = append(sm.Ku.Mine, p)
	}
	sort.Slice(sm.Ku.Mine, func(i, j int) bool { return sm.Ku.Mine[i] < sm.Ku.Mine[j] })
	if len(unions) > 0 && len(raw) > 0 {
		if slots, err := gimap.KunlunSlotsOf(raw); err == nil {
			for u := range unions {
				if s, ok := slots[u]; ok {
					sm.Ku.MySlots = append(sm.Ku.MySlots, s)
				}
			}
			sort.Ints(sm.Ku.MySlots)
		}
	}
}

// eventKunlunPuller picks any RUNNING event account of this leader whose
// server announced Côn Lôn — leader chat first, then the leader's event
// users. Returns nil when the fleet has none (the caller then reports idle).
func (t *TeleBot) eventKunlunPuller(chatID int64) *EventPlayer {
	try := func(id int64) *EventPlayer {
		v, ok := t.session.Load(id)
		if !ok {
			return nil
		}
		ep, ok := v.(*EventPlayer)
		if !ok || !ep.Running || !ep.KunlunWarEventOpen() {
			return nil
		}
		return ep
	}
	if ep := try(chatID); ep != nil {
		return ep
	}
	leaderID := strconv.FormatInt(chatID, 10)
	ms, err := model.GetEventUsersByLeader(t.ydb.GetDB(), leaderID)
	if err != nil {
		return nil
	}
	for _, m := range ms {
		if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
			if ep := try(id); ep != nil {
				return ep
			}
		}
	}
	return nil
}

// playerEventOpen reports whether the server announced this event to a
// resident regular account at all. Deliberately weaker than
// mapStreamerEligible: no one-shot-channel condition — a pull in flight
// must not make the page's island section blink off between ticks.
func playerEventOpen(p *Player, which string) bool {
	switch which {
	case gimap.StreamBonglai:
		p.GiMu.Lock()
		defer p.GiMu.Unlock()
		return p.GiActivityID != 0
	case gimap.StreamThienma:
		p.GiMu.Lock()
		defer p.GiMu.Unlock()
		return p.GiPeakActivityID != 0
	case gimap.StreamCuuu:
		// Schedule-sync gate (209570): the event runs without any ActList
		// 346 entry — see holyLandEventOpen's comment.
		return holyLandEventOpen(p)
	case gimap.StreamDitich:
		// WINDOW check, not the announced id: BgActivityID is learned once
		// and only re-learned on instance change — after the event ends the
		// card used to stay up forever ("di tich not running but still
		// show"). Mirrors mapStreamerEligible and the cuuu/thuongdo cases.
		return ballGVGOpen(p)
	case gimap.StreamThuongdo:
		now := time.Now()
		for _, e := range p.ActList.byType(skyTradeActivityType) {
			if e.status(now) == "active" {
				return true
			}
		}
		return false
	case gimap.StreamKunlun:
		return kunlunWarOpen(p)
	case gimap.StreamBacMinh:
		return bacminhWarOpen(p)
	case gimap.StreamLuanHoi:
		_, _, active := p.rbtActiveActivity()
		return active
	case gimap.StreamSeaWarWar:
		_, active := p.swActiveActivity()
		return active
	}
	return false
}

// ballGVGOpen is the Di Tích event window straight from the activity list
// (same source of truth the cuuu/thuongdo gates use).
func ballGVGOpen(p *Player) bool {
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeBallGVG) {
		if e.status(now) == "active" {
			return true
		}
	}
	return false
}

// mapStreamerEligible is the per-account gate: the event must be live for
// this account and its one-shot channel must be free, so a stream pull
// never disturbs (nor gets disturbed by) the account's own map requests.
func (t *TeleBot) mapStreamerEligible(p *Player, which string) bool {
	switch which {
	case gimap.StreamBonglai, gimap.StreamThienma:
		p.GiMu.Lock()
		defer p.GiMu.Unlock()
		if p.GiPending != nil {
			return false
		}
		if which == gimap.StreamThienma {
			return p.GiPeakActivityID != 0
		}
		return p.GiActivityID != 0
	case gimap.StreamCuuu:
		p.HlMu.Lock()
		busy := p.HlPending != nil
		p.HlMu.Unlock()
		if busy {
			return false
		}
		return holyLandEventOpen(p)
	case gimap.StreamDitich:
		p.BgMu.Lock()
		id, busy := p.BgActivityID, p.BgPending != nil
		p.BgMu.Unlock()
		if busy || id == 0 || p.bgClosed(time.Now()) {
			return false
		}
		return ballGVGOpen(p)
	case gimap.StreamThuongdo:
		if p.stSeatBusy(SERVER_SKY_TRADE_ENTER_MAP) {
			return false
		}
		now := time.Now()
		for _, e := range p.ActList.byType(skyTradeActivityType) {
			if e.status(now) == "active" {
				return true
			}
		}
		return false
	case gimap.StreamKunlun:
		// Kunlun seats are per cmd type; only the minimap seat can collide
		// with a stream pull.
		if p.kuSeatBusy(SERVER_KUNLUN_MINI_MAP_INFO) {
			return false
		}
		return kunlunWarOpen(p)
	case gimap.StreamBacMinh:
		// Same per-type-seat rule on the north board (its own bmChans).
		if p.bmSeatBusy(SERVER_KUNLUN_MINI_MAP_INFO) {
			return false
		}
		return bacminhWarOpen(p)
	}
	return false
}

// ---- Đỉnh Bắc Minh stream helpers (engine twins of the ku* ones above) ----

// bmMergeRouteLedgers gathers the NORTH march ledger of every account in
// this chat (leader + running event accounts with 332 open), newest
// departure winning — same rationale as kuMergeRouteLedgers.
func (t *TeleBot) bmMergeRouteLedgers(chatID int64) map[int64]kuRouteRec {
	out := map[int64]kuRouteRec{}
	merge := func(recs map[int64]kuRouteRec) {
		for pid, r := range recs {
			if cur, ok := out[pid]; !ok || r.Time >= cur.Time {
				out[pid] = r
			}
		}
	}
	if v, ok := t.session.Load(chatID); ok {
		if lp, ok := v.(*Player); ok {
			st := lp.bmState()
			lp.BmMu.Lock()
			merge(st.Routes)
			lp.BmMu.Unlock()
		}
	}
	t.session.Range(func(_, v any) bool {
		if ep, ok := v.(*EventPlayer); ok && ep.ChatID == chatID && ep.Running && ep.BacMinhWarEventOpen() {
			st := ep.bmStateEP()
			ep.BmMu.Lock()
			merge(st.Routes)
			ep.BmMu.Unlock()
		}
		return true
	})
	return out
}

// bmRoutesMerged builds north frame routes from the merged ledgers, coloured
// with the answering puller's own scene state (pl or ep, one non-nil).
func (t *TeleBot) bmRoutesMerged(chatID int64, pl *Player, ep *EventPlayer, now time.Time) []gimap.KuRoute {
	recs := t.bmMergeRouteLedgers(chatID)
	switch {
	case pl != nil:
		st := pl.bmState()
		pl.BmMu.Lock()
		defer pl.BmMu.Unlock()
		return kuRoutesFrom(recs, st.MyUnion, int64(pl.UID), st.MiniRaw, st.Unions, now)
	case ep != nil:
		st := ep.bmStateEP()
		ep.BmMu.Lock()
		defer ep.BmMu.Unlock()
		return kuRoutesFrom(recs, st.MyUnion, int64(ep.UID), st.MiniRaw, st.Unions, now)
	}
	return nil
}

// bmMineOverlay stamps the own side onto a NORTH frame (see kuMineOverlay):
// event stand-points via bmScenePos, unions via the leader's bm state, slots
// via BacMinhSlotsOf on the SAME payload the frame was built from.
func (t *TeleBot) bmMineOverlay(sm *gimap.StreamMap, chatID int64, raw []byte) {
	if sm == nil {
		return
	}
	pts := map[int64]bool{}
	unions := map[int64]bool{}
	add := func(id int64) {
		v, ok := t.session.Load(id)
		if !ok {
			return
		}
		ep, ok := v.(*EventPlayer)
		if !ok {
			return
		}
		pt, un := ep.bmScenePos()
		if pt > 0 {
			pts[int64(pt)] = true
		}
		if un != 0 {
			unions[un] = true
		}
	}
	add(chatID)
	if v, ok := t.session.Load(chatID); ok {
		if lp, ok := v.(*Player); ok {
			lst := lp.bmState()
			lp.BmMu.Lock()
			un := lst.MyUnion
			lp.BmMu.Unlock()
			if un != 0 {
				unions[un] = true
			}
		}
	}
	if t.ydb != nil {
		leaderID := strconv.FormatInt(chatID, 10)
		if users, err := model.GetEventUsersByLeader(t.ydb.GetDB(), leaderID); err == nil {
			for _, m := range users {
				if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
					add(id)
				}
			}
		}
	}
	if len(pts) == 0 && len(unions) == 0 {
		return
	}
	if sm.Ku == nil {
		sm.Ku = &gimap.KuFrame{}
	}
	for p := range pts {
		sm.Ku.Mine = append(sm.Ku.Mine, p)
	}
	sort.Slice(sm.Ku.Mine, func(i, j int) bool { return sm.Ku.Mine[i] < sm.Ku.Mine[j] })
	if len(unions) > 0 && len(raw) > 0 {
		if slots, err := gimap.BacMinhSlotsOf(raw); err == nil {
			for u := range unions {
				if s, ok := slots[u]; ok {
					sm.Ku.MySlots = append(sm.Ku.MySlots, s)
				}
			}
			sort.Ints(sm.Ku.MySlots)
		}
	}
}

// eventBacMinhPuller picks any RUNNING event account of this leader whose
// server announced Đỉnh Bắc Minh (see eventKunlunPuller).
func (t *TeleBot) eventBacMinhPuller(chatID int64) *EventPlayer {
	try := func(id int64) *EventPlayer {
		v, ok := t.session.Load(id)
		if !ok {
			return nil
		}
		ep, ok := v.(*EventPlayer)
		if !ok || !ep.Running || !ep.BacMinhWarEventOpen() {
			return nil
		}
		return ep
	}
	if ep := try(chatID); ep != nil {
		return ep
	}
	if t.ydb == nil {
		return nil
	}
	leaderID := strconv.FormatInt(chatID, 10)
	ms, err := model.GetEventUsersByLeader(t.ydb.GetDB(), leaderID)
	if err != nil {
		return nil
	}
	for _, m := range ms {
		if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
			if ep := try(id); ep != nil {
				return ep
			}
		}
	}
	return nil
}
