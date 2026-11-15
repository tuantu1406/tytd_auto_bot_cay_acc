package tytd_go

import (
	"fmt"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Holy Land (Cửu U Tranh Bá) support for regular players, driven by the
// /cuuu_map command and the web map endpoint: answer one-shot minimap
// requests against the requester's own live session. The event state machine
// for event accounts lives in player_event_holyland.go (EventPlayer).
//
// Unlike God Island there is no activityId and no bracket selector to track
// — the minimap request (209526) is empty (or gameType/group when we want a
// specific bracket) and the server answers for our own bracket, so the Player
// side is just the request/response plumbing.
//
// Open gate: the schedule sync 209570 → 2562 (isOpen/roundId). A live probe
// (player_event_holyland_live_test.go, 2026-10-03) proved the event can run
// weeks of rounds WITHOUT a type-346 entry ever appearing in ActList — the
// old ActList-only gate made the web card and /cuuu_map claim "not open"
// while the game showed Cửu U running. autoCuuuSchedule keeps HlOpen fresh.
// The other half arrived 2026-10-06: isOpen stays true across the season, so
// the gate additionally requires time.Now() inside a scheduleTimestamp battle
// window (the same scheduleTimestamp + GameConfigDB offsets the client's
// getHolyLandState uses) — see hlRunningNow.
//
// Protocol reference: ../remoteHolyLand/HOLY_LAND_PROTOCOL.md.

// hlDeliverMiniMap hands a SERVER_HOLY_LAND_MINIMAP payload to the pending
// map request, if one is outstanding. It never blocks the reader goroutine:
// with no listener (or a stale full channel) the payload is dropped.
func (p *Player) hlDeliverMiniMap(data []byte) {
	p.HlMu.Lock()
	ch := p.HlPending
	p.HlMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// RequestHolyLandMiniMap asks the server for our bracket's minimap (209526)
// and returns the raw HolyLandGameMiniMapInfoRespMsg payload, or an error
// after timeout / disconnect. Only one request may be outstanding per
// player (single answer type).
func (p *Player) RequestHolyLandMiniMap(timeout time.Duration) ([]byte, error) {
	p.HlMu.Lock()
	if p.HlPending != nil {
		p.HlMu.Unlock()
		return nil, fmt.Errorf("có yêu cầu map đang chờ, thử lại sau nhé")
	}
	ch := make(chan []byte, 1)
	p.HlPending = ch
	p.HlMu.Unlock()
	defer func() {
		p.HlMu.Lock()
		if p.HlPending == ch {
			p.HlPending = nil
		}
		p.HlMu.Unlock()
	}()

	// gameType/group omitted: the server picks our bracket.
	mapMSG, _ := pb.Marshal(&proto.HolyLandGameMiniMapInfoReqMsg{})
	select {
	case p.writeChan <- pack.Pack(HOLY_LAND_MINIMAP_CMD, mapMSG, p.UID):
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}

	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("server không trả lời map, thử lại sau")
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
}

const (
	cuuuScheduleCycle = 10 * time.Minute // 2562 refresh; boundaries are daily 00:00
	cuuuScheduleWarm  = 10 * time.Second // let the login pushes settle first
)

// hlHandleScheduleMsg caches the 209570 (→2562) answer. ret != 0 means the
// server has no schedule for us — treat as closed, like the EventPlayer
// handler does.
func (p *Player) hlHandleScheduleMsg(data []byte) {
	var resp proto.HolyLandBattleApplyDataSyncMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		return
	}
	p.HlMu.Lock()
	if resp.GetRet() == 0 {
		p.HlOpen = resp.GetIsOpen()
		p.HlRoundId = resp.GetRoundId()
		p.HlScheduleApply = resp.GetScheduleTimestamp()
	} else {
		// No schedule for us: the open flag and the day anchors age out
		// together (stale anchors + a later "open" would resurrect the
		// card at the season clock, not at the real round).
		p.HlOpen = false
		p.HlScheduleApply = nil
		p.HlScheduleTs = nil
	}
	p.HlOpenKnown = true
	p.HlMu.Unlock()
}

// hlHandleSchedulePushMsg consumes the 209575 timestamp refresh. LIVE
// TRUTH 2026-10-03: the server sends a 2567 push after EVERY 209570 it
// answers — an earlier version re-asked 209570 here, which fed itself into
// a hot poll loop (ask → push → ask …) on every regular account with the
// event open. Re-asking stays forbidden; autoCuuuSchedule owns the cadence.
// The one honest thing to take from the push is the day-anchor array itself,
// and only while the 2562 answer has not supplied its own yet (client
// precedence: battleApplyData ?? battleTimeStampsData).
func (p *Player) hlHandleSchedulePushMsg(data []byte) {
	var push proto.HolyLandBattleTimeStampsDataSyncMsg
	if err := pb.Unmarshal(data, &push); err != nil {
		return
	}
	p.HlMu.Lock()
	if push.GetRet() == 0 && len(p.HlScheduleApply) == 0 && len(push.GetScheduleTimestamp()) > 0 {
		p.HlScheduleTs = push.GetScheduleTimestamp()
	}
	p.HlMu.Unlock()
}

// holyLandEventOpen is the single source of truth for the Cửu U card /
// /cuuu_map gate: a round must be RUNNING NOW — the cached schedule open
// flag (ActList 346 announcement only until the first 2562 answer) AND
// time.Now() inside one of the server-anchored battle windows. The open
// flag alone is season-level and the 346 row is the season window; either
// shown the card all season long, which is the bug the user reported
// 2026-10-06. See hlRunningNow.
func holyLandEventOpen(p *Player) bool {
	p.HlMu.Lock()
	on, known := p.HlOpen, p.HlOpenKnown
	apply, ts := p.HlScheduleApply, p.HlScheduleTs
	p.HlMu.Unlock()
	open := on
	if !known && !open {
		now := time.Now()
		for _, e := range p.ActList.byType(hlActivityTypeHolyLand) {
			if e.status(now) == "active" {
				open = true
				break
			}
		}
	}
	return hlRunningNow(open, apply, ts, time.Now().UnixMilli())
}

// holyLandEventUpcoming returns the next battle start (epoch ms) when Cửu U
// is scheduled but not fighting yet — the events-tab countdown gate. Same
// open-flag resolution as holyLandEventOpen; 0 = nothing to show.
func holyLandEventUpcoming(p *Player) int64 {
	p.HlMu.Lock()
	on, known := p.HlOpen, p.HlOpenKnown
	apply, ts := p.HlScheduleApply, p.HlScheduleTs
	p.HlMu.Unlock()
	open := on
	if !known && !open {
		now := time.Now()
		for _, e := range p.ActList.byType(hlActivityTypeHolyLand) {
			if e.status(now) == "active" {
				open = true
				break
			}
		}
	}
	if !open {
		return 0
	}
	nowMs := time.Now().UnixMilli()
	if hlRunningNow(open, apply, ts, nowMs) {
		return 0
	}
	return hlNextBeginMs(apply, ts, nowMs)
}

// autoCuuuSchedule keeps Player.HlOpen fresh for the web card and /cuuu_map:
// ask 209570 shortly after login and every 10 min; 2567 pushes wake it early.
func (p *Player) autoCuuuSchedule() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(cuuuScheduleWarm):
	}
	for {
		p.write(pack.Pack(HOLY_LAND_SCHEDULE_CMD, []byte{}, p.UID))
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(cuuuScheduleCycle):
		}
	}
}

// ---------------------------------------------------------------------------
// Marching routes + city queues — the two pulls behind "I can see others
// moving and click a city to see who is fighting" (209522 → 2532 route
// snapshot; 209524 → 2534 per-city attacker/defender queue).

// hlSeatRoundTrip is the shared one-shot pattern: a free channel parked in
// *seat, request frame, wait for the reader to deliver.
func hlSeatRoundTrip(p *Player, seat *chan []byte, cmd string, body []byte, timeout time.Duration) ([]byte, error) {
	p.HlMu.Lock()
	if *seat != nil {
		p.HlMu.Unlock()
		return nil, fmt.Errorf("có yêu cầu map đang chờ, thử lại sau nhé")
	}
	ch := make(chan []byte, 1)
	*seat = ch
	p.HlMu.Unlock()
	defer func() {
		p.HlMu.Lock()
		if *seat == ch {
			*seat = nil
		}
		p.HlMu.Unlock()
	}()

	// ctx-aware send: a half-dead connection (writer goroutine exited,
	// writeChan full) would otherwise block here forever and freeze the
	// whole web map streamer loop.
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("server không trả lời, thử lại sau")
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
}

// hlAllCityIDs is the cityVec the client sends (it sends only the on-screen
// cities; we always want the whole bracket map).
func hlAllCityIDs() []int32 {
	var v []int32
	for id := 1001; id <= 1012; id++ {
		v = append(v, int32(id))
	}
	for id := 2001; id <= 2015; id++ {
		v = append(v, int32(id))
	}
	for id := 3001; id <= 3006; id++ {
		v = append(v, int32(id))
	}
	return append(v, 4001)
}

// RequestHolyLandCityRoutes asks 209522 for every city plus the marching-route
// snapshot. The server only answers battlefield residents (ret=9505
// otherwise) — the web streamer handles that by moving to the next account.
func (p *Player) RequestHolyLandCityRoutes(gameType, group int32, timeout time.Duration) ([]byte, error) {
	body, _ := pb.Marshal(&proto.HolyLandGameCityInfoReqMsg{
		CityVec: hlAllCityIDs(), GameType: &gameType, GroupId: &group,
	})
	return hlSeatRoundTrip(p, &p.HlRoutePending, HOLY_LAND_CITY_INFO_CMD, body, timeout)
}

// RequestHolyLandCityLine asks 209524 for one city's attacker/defender queue
// (the city-click popup).
func (p *Player) RequestHolyLandCityLine(cityID int32, timeout time.Duration) ([]byte, error) {
	body, _ := pb.Marshal(&proto.HolyLandGameTargetCityLineInfoReqMsg{CityId: &cityID})
	return hlSeatRoundTrip(p, &p.HlLinePending, HOLY_LAND_TARGET_LINE_CMD, body, timeout)
}

// hlDeliverRoutes / hlDeliverLine route reader frames into their seats.
func (p *Player) hlDeliverRoutes(data []byte) {
	p.HlMu.Lock()
	ch := p.HlRoutePending
	p.HlMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

func (p *Player) hlDeliverLine(data []byte) {
	p.HlMu.Lock()
	ch := p.HlLinePending
	p.HlMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}
