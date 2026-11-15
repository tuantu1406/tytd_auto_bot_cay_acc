package tytd_go

// Ball GVG (Thượng Cổ Di Tích) — camp-vs-camp room fighting on event
// accounts. The state machine mirrors the Holy Land integration but is
// different in one structural way: EVERY Ball GVG message echoes the live
// activityId (learned from the activity list, system type 14), so responses
// attribute directly to the account's single Ball GVG context — no slots and
// no FIFO queue (the FIFO even HURTS here: 21003 is shared with God Island,
// and the activityId inside the answer routes it exactly — see
// bgActivityDetail).
//
// Room entry, rally mark and lobby exit fire only from leader commands
// (/ditich_*). Everything else the official client does on its own, we do on
// its behalf: while alive inside a capturable room the account auto-walks a
// shared ring route with random steps (21106 position reports) and auto-strikes
// the nearest alive enemy of another camp whenever the server-derived cooldown
// is open (21107 — BALL_GVG_FLOW.md §7: the server never attacks by itself; a
// strike IS one request, so automation lives here, on the client side). The
// residency loop keeps 21103 as entry + reconnect spine, fetches the room
// config once per activityId and refreshes the ownership map. There is NO
// revive request in this protocol — userData.reviveTime is an absolute
// deadline the account simply waits out.
//
// Protocol reference: ../remoteBallGVG/BALL_GVG_PROTOCOL.md +
// BALL_GVG_FLOW.md; message types live in internal/proto/tytd.proto
// (BallGVG* block).

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const (
	bgTickSeconds     = 5  // residency loop cadence
	bgAutoSeconds     = 2  // auto walk+strike cadence
	bgHeavyTicks      = 12 // ~60 s: ENTER_ACT resync + 21111 map refresh
	bgEnterRetryGap   = 10 * time.Second
	bgConfigRetryGap  = 2 * time.Minute
	bgAttackSendFloor = time.Second // local anti-spam floor between attacks
	bgMaxRosterLines  = 30          // /roster output cap
	bgRoutePoints     = 8           // waypoints on the shared walk route
)

// Room the UI shows at the centre of the arena view — a position report per
// room entry parks the avatar there before the walk begins.
const (
	bgCentreX  = "1125.00000"
	bgCentreY  = "900.00000"
	bgCentre   = 1125.0
	bgCentreYf = 900.0
)

// bgRosterUser is one visible occupant of our current room.
type bgRosterUser struct {
	PlayerID   int64
	Name       string
	CampID     int32
	HP         string
	ReviveTime int64 // absolute ms; dead while > now (from join snapshots)
	X, Y       string
}

func (u *bgRosterUser) alive(now time.Time) bool {
	if u.ReviveTime > 0 {
		if _, waiting := bgReviveLeft(u.ReviveTime, now); waiting {
			return false
		}
	}
	return !bgZeroHP(u.HP)
}

// ballGVGState is the entire Ball GVG state of one event account. Every
// field is guarded by EventPlayer.BgMu.
type ballGVGState struct {
	// Discovery (activity list, type 14). ActivityID==0 = not announced.
	ActivityID int32
	// Joined flips on the first ENTER_ACT ret==0 for this ActivityID — it
	// means the account IS registered in the event (done in-game by the
	// union leader; we never apply on our own).
	Joined bool
	// notifiedJoin is the edge so a refused entry is reported once per
	// activityId, not on every 10 s retry.
	notifiedJoin bool

	// Our combatant, replaced wholesale from userData (21103/21107/21121).
	CampID       int32
	PlaceID      int32 // current room (0 = activity lobby)
	TransferTime int64
	AttackTime   int64 // last attack ms; + AttackSpeed = next ok
	ReviveTime   int64 // absolute ms; dead while > now
	HP           string
	TotalHP      string
	FightValue   string
	AttackSpeed  int32 // ms between attacks

	// Camp metadata (21103.camp / 21120 push, REPLACE).
	MajorPlaceID int32

	// Scoreboard campId→score (21103.score / 21117 push, REPLACE).
	Scores map[int32]int64

	// Roster of the CURRENT room: playerId→entry. Rebuilt on room entry,
	// patched by 21123/21124/21116/21118.
	Roster map[int64]*bgRosterUser

	// Room config from 21003 uniqueConfig (names/types) + ownership from
	// 21111/21122 (placeId→owner campId).
	ConfigFetched bool
	lastConfigTry time.Time
	Rooms         map[int32]*proto.BallGVGPlaceConfigTemp
	Owners        map[int32]int32

	// Auto-walk: our last reported position + index on the shared ring
	// route. Every account walks the SAME waypoints; only the starting
	// index and step choices differ, so the fleet patrols one road.
	myX, myY   float64
	routeIdx   int  // current waypoint on the shared ring
	routeStart bool // true once the entry report established a position

	// lastMovePlace dedupes the per-entry position report; lastAttackSend
	// is the local anti-spam floor; lastEnterTry paces entry retries.
	lastMovePlace  int32
	lastAttackSend time.Time
	lastEnterTry   time.Time
}

// bgState returns the (lazily created) state slot; the pointer is stable,
// its FIELDS still require BgMu — same convention as hlState.
func (p *EventPlayer) bgState() *ballGVGState {
	p.BgMu.Lock()
	defer p.BgMu.Unlock()
	if p.bg == nil {
		p.bg = &ballGVGState{}
	}
	return p.bg
}

// bgSend marshals msg and puts the frame on the wire under bgSendMu so call
// order equals wire order (mirrors hlSend). 21110 LEAVE_ACT has no encoder
// server-side — never marshal a proto for it; we do not use it at all.
func (p *EventPlayer) bgSend(cmd string, msg pb.Message) {
	payload, err := pb.Marshal(msg)
	if err != nil {
		log.Printf("[%s] [BallGVG] marshal %s: %v", p.Username, cmd, err)
		return
	}
	p.bgSendMu.Lock()
	p.write(pack.Pack(cmd, payload, p.UID))
	p.bgSendMu.Unlock()
}

// ---------------------------------------------------------------------------
// Pure helpers (table-testable)
// ---------------------------------------------------------------------------

// bgZeroHP reports whether a decimal-string HP value is zero (dead). Values
// are big decimal strings; a parse failure is NOT treated as dead.
func bgZeroHP(hp string) bool {
	f, err := strconv.ParseFloat(strings.TrimSpace(hp), 64)
	return err == nil && f == 0
}

// bgReviveLeft wraps hlRebirthLeft: reviveTime is documented as absolute ms,
// but we normalize s-vs-ms the same way (1e12 cutoff) rather than trust it.
func bgReviveLeft(reviveTime int64, now time.Time) (time.Duration, bool) {
	return hlRebirthLeft(reviveTime, now)
}

// bgShouldTryEnter paces entry attempts: only while announced, not yet
// joined, and the retry gap passed.
func bgShouldTryEnter(st *ballGVGState, now time.Time) bool {
	return st.ActivityID != 0 && !st.Joined && now.Sub(st.lastEnterTry) >= bgEnterRetryGap
}

// bgAttackReady reports whether the attack cooldown is open: the server
// grants the next attack at userData.attackTime + property.attackSpeed
// (BALL_GVG_FLOW.md §7). Extracted for table tests.
func bgAttackReady(st *ballGVGState, now time.Time) bool {
	return now.UnixMilli() >= st.AttackTime+int64(st.AttackSpeed)
}

// bgAttackRefusal is the pure target gate for ordering an attack on u
// (nil = target absent). Returns "" when allowed. Room membership and the
// send floor are checked by the caller; here: self-fire, friendly camp, dead
// target. Extracted for table tests.
func bgAttackRefusal(st *ballGVGState, selfID int64, u *bgRosterUser, now time.Time) string {
	if u == nil {
		return "không có trong phòng"
	}
	if u.PlayerID == selfID {
		return "không đánh chính mình"
	}
	if u.CampID == st.CampID {
		return "cùng phe"
	}
	if !u.alive(now) {
		return "đang chết"
	}
	return ""
}

// bgRoutePoint returns the idx-th waypoint of the ring every account walks
// in a room: bgRoutePoints evenly spaced points on an ellipse around the UI
// centre. Pure and shared by all accounts — "random" comes from each
// account's starting index and step choices, not from a different route.
func bgRoutePoint(idx int, cx, cy, rx, ry float64) (float64, float64) {
	a := 2 * math.Pi * float64(((idx%bgRoutePoints)+bgRoutePoints)%bgRoutePoints) / bgRoutePoints
	return cx + rx*math.Cos(a), cy + ry*math.Sin(a)
}

// bgAutoAttackAllowed is the pure gate for one automated strike: alive, in a
// ROOM THAT IS A CAPTURABLE BATTLEFIELD (type 4 — BALL_GVG_FLOW.md §7 says
// auto combat only exists there, and we require the config cache so home
// rooms never fire), cooldown open per userData.attackTime+attackSpeed, and
// past the local send floor.
func bgAutoAttackAllowed(st *ballGVGState, now time.Time) bool {
	if !st.Joined || st.PlaceID <= 0 || !bgAliveLocked(st, now) {
		return false
	}
	if r := st.Rooms[st.PlaceID]; r == nil || r.GetType() != 4 {
		return false
	}
	return bgAttackReady(st, now) && now.Sub(st.lastAttackSend) >= bgAttackSendFloor
}

// bgPickAutoTarget is the stock rule (BALL_GVG_FLOW.md §7, minus the manual
// lock which we never hold): nearest alive enemy camp member by last known
// position; targets with no position seen yet form a deterministic
// (lowest-id) fallback so combat never stalls on a position-less room.
func bgPickAutoTarget(st *ballGVGState, selfID int64, myX, myY float64, now time.Time) *bgRosterUser {
	var best, fallback *bgRosterUser
	bestD := math.Inf(1)
	for _, e := range st.Roster {
		if bgAttackRefusal(st, selfID, e, now) != "" {
			continue
		}
		tx, errX := strconv.ParseFloat(strings.TrimSpace(e.X), 64)
		ty, errY := strconv.ParseFloat(strings.TrimSpace(e.Y), 64)
		if errX != nil || errY != nil {
			if fallback == nil || e.PlayerID < fallback.PlayerID {
				fallback = e
			}
			continue
		}
		d := (tx-myX)*(tx-myX) + (ty-myY)*(ty-myY)
		if d < bestD || (d == bestD && best != nil && e.PlayerID < best.PlayerID) {
			best, bestD = e, d
		}
	}
	if best != nil {
		return best
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Background residency loop
// ---------------------------------------------------------------------------

// bgStay keeps the account registered-resident in the event. Until an
// activityId is announced there is nothing to do (activity list pushes feed
// it). While announced but not joined, 21103 is retried (paced); once
// joined, 21103 repeats every heavy tick as the resync/reconnect spine and
// 21111 refreshes the ownership map. 21003 (room names, camp assignment)
// runs once per activityId with a slow retry. Walking and striking run in
// bgAuto; room entry / mark / exit stay leader commands.
func (p *EventPlayer) bgStay() {
	tick := 0
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(bgTickSeconds * time.Second):
		}

		st := p.bgState()
		now := time.Now()
		p.BgMu.Lock()
		actID := st.ActivityID
		joined, config := st.Joined, st.ConfigFetched
		enter := bgShouldTryEnter(st, now)
		if enter {
			st.lastEnterTry = now
		}
		cfgTry := actID != 0 && !config && now.Sub(st.lastConfigTry) >= bgConfigRetryGap
		if cfgTry {
			st.lastConfigTry = now
		}
		resync := joined && actID != 0 && tick%bgHeavyTicks == 0
		p.BgMu.Unlock()

		if actID == 0 {
			continue
		}
		if cfgTry {
			p.bgSend(ACTIVITY_GET_DATA_CMD, &proto.ReqGetActivityDetail{ActivityId: actID})
		}
		if enter || resync {
			p.bgSend(BALL_GVG_ENTER_ACT_CMD, &proto.BallGVGEnterActivityReqMsg{ActivityId: actID})
		}
		if resync {
			p.bgSend(BALL_GVG_GET_PLACE_CMD, &proto.BallGVGGetPlaceInfoReqMsg{ActivityId: actID})
		}
		tick++
	}
}

// bgReportPosition parks the avatar at the UI centre once per room entry
// (21106 is a position report, not a move order) and seeds the auto-walk
// with a fresh position + random route offset. Sent only while joined, in a
// room and alive.
func (p *EventPlayer) bgReportPosition() {
	st := p.bgState()
	p.BgMu.Lock()
	ok := st.Joined && st.PlaceID > 0 && st.lastMovePlace != st.PlaceID
	if !bgAliveLocked(st, time.Now()) {
		ok = false
	}
	if ok {
		st.lastMovePlace = st.PlaceID
		st.myX, st.myY = bgCentre, bgCentreYf
		st.routeIdx = rand.Intn(bgRoutePoints)
		st.routeStart = true
	}
	place := st.PlaceID
	actID := st.ActivityID
	p.BgMu.Unlock()
	if !ok {
		return
	}
	log.Printf("[%s] [BallGVG] position report for room %d", p.Username, place)
	p.bgSend(BALL_GVG_MOVE_CMD, &proto.BallGVGMoveReqMsg{
		ActivityId: actID,
		Position: &proto.BallGVGPositionTemp{
			X: bgCentreX, Y: bgCentreY, DirX: "0.00000", DirY: "1.00000",
			Time: time.Now().UnixMilli(),
		},
	})
}

// bgRosterFrame surfaces the CURRENT room's roster for the /ditich mini
// app: every player this resident account can see, positions normalized
// through the client's own elliptic walk ring (see gimap.TcFrame). When the
// room radius is unknown or a ball's coords fail to parse, that ball carries
// the −1 sentinel and the page shows the roster as a list instead of dots.
// Read-only: holds BgMu only while copying.
func (p *EventPlayer) bgRosterFrame(now time.Time) *gimap.TcFrame {
	st := p.bgState()
	p.BgMu.Lock()
	defer p.BgMu.Unlock()
	place := st.PlaceID
	if place <= 0 || len(st.Roster) == 0 {
		return nil
	}
	f := &gimap.TcFrame{Place: place, Name: bgRoomName(st, place)}
	r := 0.0
	if cfg := st.Rooms[place]; cfg != nil && cfg.GetRadius() > 0 {
		r = float64(cfg.GetRadius())
		f.R = r
	}
	for _, u := range st.Roster {
		b := gimap.TcBall{N: u.Name, C: u.CampID, HP: u.HP, X: -1, Y: -1}
		if !u.alive(now) {
			b.D = true
			b.Rv = u.ReviveTime
		}
		if r > 0 {
			x, errX := strconv.ParseFloat(strings.TrimSpace(u.X), 64)
			y, errY := strconv.ParseFloat(strings.TrimSpace(u.Y), 64)
			if errX == nil && errY == nil {
				b.X = bgNormClamp(0.5 + (x-bgCentre)/(2*r*0.55))
				b.Y = bgNormClamp(0.5 + (y-bgCentreYf)/(2*r*0.40))
			}
		}
		f.Balls = append(f.Balls, b)
	}
	// Deterministic order (Roster is a map) + a payload sanity cap: a full
	// 13-room war could otherwise bloat every 5 s SSE frame.
	sort.Slice(f.Balls, func(i, j int) bool {
		if f.Balls[i].C != f.Balls[j].C {
			return f.Balls[i].C < f.Balls[j].C
		}
		return f.Balls[i].N < f.Balls[j].N
	})
	if len(f.Balls) > 80 {
		f.Balls = f.Balls[:80]
	}
	return f
}

func bgNormClamp(v float64) float64 {
	if v < 0.05 {
		return 0.05
	}
	if v > 0.95 {
		return 0.95
	}
	return v
}

// bgAliveLocked reports whether WE are alive; caller holds BgMu.
func bgAliveLocked(st *ballGVGState, now time.Time) bool {
	if st.ReviveTime > 0 {
		if _, waiting := bgReviveLeft(st.ReviveTime, now); waiting {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Auto walk + auto strike (the stock client does this itself; a strike IS
// one 21107 request, so the automation legitimately lives on our side)
// ---------------------------------------------------------------------------

// bgAuto ticks the auto behaviour: every bgAutoSeconds it advances the
// avatar along the shared ring route (when alive in a room) and, in a
// capturable room, sends one attack at the nearest alive enemy when the
// server-derived cooldown allows. Refusals are log-only — the loop paces
// itself through AttackTime and the send floor, so a rejection cannot flood.
func (p *EventPlayer) bgAuto() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(bgAutoSeconds * time.Second):
		}
		p.bgAutoStep()
	}
}

// bgAutoStep is one auto tick: compute everything under BgMu, send outside.
func (p *EventPlayer) bgAutoStep() {
	st := p.bgState()
	now := time.Now()
	p.BgMu.Lock()
	actID, place := st.ActivityID, st.PlaceID
	move := false
	var mx, my, mdx, mdy string
	if st.Joined && place > 0 && st.routeStart && bgAliveLocked(st, now) {
		r := 300.0 // sane default walk ring when the config cache is cold
		if cfg := st.Rooms[place]; cfg != nil && cfg.GetRadius() > 0 {
			r = float64(cfg.GetRadius())
		}
		step := 1 + rand.Intn(2)
		if rand.Intn(3) == 0 {
			step = -step // sometimes double back — same ring, random feel
		}
		st.routeIdx = ((st.routeIdx+step)%bgRoutePoints + bgRoutePoints) % bgRoutePoints
		tx, ty := bgRoutePoint(st.routeIdx, bgCentre, bgCentreYf, r*0.55, r*0.40)
		dx, dy := tx-st.myX, ty-st.myY
		if n := math.Hypot(dx, dy); n > 1e-9 {
			dx, dy = dx/n, dy/n
		}
		st.myX, st.myY = tx, ty
		mx, my = fmt.Sprintf("%.5f", tx), fmt.Sprintf("%.5f", ty)
		mdx, mdy = fmt.Sprintf("%.5f", dx), fmt.Sprintf("%.5f", dy)
		move = true
	}
	attack := bgAutoAttackAllowed(st, now)
	var targetID int64
	if attack {
		if u := bgPickAutoTarget(st, int64(p.UID), st.myX, st.myY, now); u != nil {
			targetID = u.PlayerID
			st.lastAttackSend = now
		} else {
			attack = false
		}
	}
	p.BgMu.Unlock()

	if move {
		p.bgSend(BALL_GVG_MOVE_CMD, &proto.BallGVGMoveReqMsg{
			ActivityId: actID,
			Position:   &proto.BallGVGPositionTemp{X: mx, Y: my, DirX: mdx, DirY: mdy, Time: now.UnixMilli()},
		})
	}
	if attack {
		p.bgSend(BALL_GVG_ATTACK_CMD, &proto.BallGVGAttackReqMsg{ActivityId: actID, AttackPlayerId: targetID})
		log.Printf("[%s] [BallGVG] auto-strike %d in room %d", p.Username, targetID, place)
	}
}

// ---------------------------------------------------------------------------
// Shared-state writers (caller holds BgMu)
// ---------------------------------------------------------------------------

// bgApplyUserDataLocked replaces our combatant snapshot — the server sends
// complete userData on 21103/21107(ret)/21121; partial edits would race the
// authoritative copy.
func bgApplyUserDataLocked(st *ballGVGState, ud *proto.BallGVGUserDataTemp) {
	if ud == nil {
		return
	}
	if ud.CampId != nil {
		st.CampID = ud.GetCampId()
	}
	if ud.PlaceId != nil {
		st.PlaceID = ud.GetPlaceId()
	}
	if ud.TransferTime != nil {
		st.TransferTime = ud.GetTransferTime()
	}
	if ud.AttackTime != nil {
		st.AttackTime = ud.GetAttackTime()
	}
	if ud.ReviveTime != nil {
		st.ReviveTime = ud.GetReviveTime()
	}
	if pr := ud.GetProperty(); pr != nil {
		st.HP = pr.GetHp()
		st.TotalHP = pr.GetTotalHp()
		st.FightValue = pr.GetFightValue()
		st.AttackSpeed = pr.GetAttackSpeed()
	}
}

func bgUserName(u *proto.BallGVGOtherUserTemp) string {
	return u.GetPlayerData().GetPlayerBaseDataMsg().GetNickName()
}

// bgRosterUserLocked converts a wire temp into a roster entry.
func bgRosterUserLocked(u *proto.BallGVGOtherUserTemp) *bgRosterUser {
	e := &bgRosterUser{
		CampID:     u.GetCampId(),
		Name:       bgUserName(u),
		ReviveTime: u.GetReviveTime(),
	}
	if u.PlayerData != nil && u.PlayerData.PlayerBaseDataMsg != nil {
		e.PlayerID = u.PlayerData.PlayerBaseDataMsg.GetPlayerId()
	}
	if pr := u.GetProperty(); pr != nil {
		e.HP = pr.GetHp()
	}
	if pos := u.GetPosition(); pos != nil {
		e.X, e.Y = pos.GetX(), pos.GetY()
	}
	return e
}

// bgReplaceRosterLocked rebuilds the current-room roster from a placeData.
func bgReplaceRosterLocked(st *ballGVGState, pd *proto.BallGVGPlaceDataTemp) {
	if st.Roster == nil {
		st.Roster = map[int64]*bgRosterUser{}
	}
	if pd == nil {
		return
	}
	if pd.PlaceId != nil {
		st.PlaceID = pd.GetPlaceId()
	}
	roster := map[int64]*bgRosterUser{}
	for _, u := range pd.GetUser() {
		e := bgRosterUserLocked(u)
		if e.PlayerID != 0 {
			roster[e.PlayerID] = e
		}
	}
	st.Roster = roster
}

func bgScoresFromSync(temps []*proto.BallGVGCampScoreSyncTemp) map[int32]int64 {
	m := map[int32]int64{}
	for _, s := range temps {
		m[s.GetCampId()] = s.GetScore()
	}
	return m
}

// bgRoomName renders "12 — Huyền Vũ Quan" style labels, falling back to the
// bare id when the config cache is empty.
func bgRoomName(st *ballGVGState, placeID int32) string {
	if r := st.Rooms[placeID]; r != nil && r.GetName() != "" {
		return fmt.Sprintf("%d — %s", placeID, r.GetName())
	}
	return strconv.Itoa(int(placeID))
}

// ---------------------------------------------------------------------------
// Discovery & config
// ---------------------------------------------------------------------------

// bgHandleActivityList learns the live activityId of Ball GVG (type 14)
// from PushActivityList/SyncActivityList. Full lists may withdraw the
// announcement (event over → clear everything).
func (p *EventPlayer) bgHandleActivityList(data []byte, full bool) {
	var push proto.PushActivityList
	if err := pb.Unmarshal(data, &push); err != nil {
		return // giHandleActivityList logs the malformed frame already
	}
	var id int32
	for _, cfg := range push.GetMainConfig() {
		if cfg.GetType() == activityTypeBallGVG {
			id = cfg.GetActivityId()
			break
		}
	}
	st := p.bgState()
	p.BgMu.Lock()
	if id != 0 && id != st.ActivityID {
		st.ActivityID = id
		st.Joined = false
		st.notifiedJoin = false
		st.ConfigFetched = false
		st.lastConfigTry = time.Time{}
		st.Owners = nil
		log.Printf("[%s] [BallGVG] activity %d", p.Username, id)
	} else if id == 0 && full && st.ActivityID != 0 {
		log.Printf("[%s] [BallGVG] activity %d withdrawn", p.Username, st.ActivityID)
		st.ActivityID = 0
		st.Joined = false
		st.PlaceID = 0
		st.Roster = nil
		st.Owners = nil
	}
	p.BgMu.Unlock()
}

// bgActivityDetail routes the shared 21003 answer (03eb): Ball GVG's copy
// names its activityId, so it goes straight to the Ball GVG slot; anything
// else runs the God Island FIFO attribution via fallback.
func (p *EventPlayer) bgActivityDetail(data []byte, fallback func()) {
	var resp proto.RspGetActivityDetail
	if err := pb.Unmarshal(data, &resp); err == nil {
		act := resp.GetActivity().GetActivityId()
		p.BgMu.Lock()
		mine := act != 0 && p.bg != nil && p.bg.ActivityID == act
		p.BgMu.Unlock()
		if mine {
			p.bgHandleActivityDetail(&resp)
			return
		}
	}
	fallback()
}

// bgHandleActivityDetail caches the room table (uniqueConfig → place names,
// types) and our camp assignment (playerData).
func (p *EventPlayer) bgHandleActivityDetail(resp *proto.RspGetActivityDetail) {
	if resp.GetRet() != 0 {
		log.Printf("[%s] [BallGVG] activity detail refused: ret=%d", p.Username, resp.GetRet())
		return
	}
	act := resp.GetActivity()
	if act == nil {
		return
	}
	rooms := map[int32]*proto.BallGVGPlaceConfigTemp{}
	if raw := act.GetDetailConfig().GetUniqueConfig(); len(raw) > 0 {
		var cfg proto.BallGVGConfigSyncMsg
		if err := pb.Unmarshal(raw, &cfg); err != nil {
			log.Printf("[%s] [BallGVG] bad uniqueConfig: %v", p.Username, err)
		} else {
			for _, r := range cfg.GetPlaceConfig() {
				if r.GetId() != 0 {
					rooms[r.GetId()] = r
				}
			}
		}
	} else {
		return // nothing usable — let bgStay retry with the slow gap
	}
	st := p.bgState()
	p.BgMu.Lock()
	st.Rooms = rooms
	st.ConfigFetched = true
	if raw := act.GetPlayerData(); len(raw) > 0 {
		var pd proto.BallGVGActivityDataTemp
		if err := pb.Unmarshal(raw, &pd); err == nil && pd.GetCampId() != 0 && st.CampID == 0 {
			st.CampID = pd.GetCampId()
		}
	}
	p.BgMu.Unlock()
	log.Printf("[%s] [BallGVG] %d rooms cached", p.Username, len(rooms))
}

// ---------------------------------------------------------------------------
// Response / push handlers (eventWsHandler reader goroutine)
// ---------------------------------------------------------------------------

// bgHandleEnterAct is the spine: ret==0 means joined (registered in the
// event) and carries the full snapshot; ret!=0 means the server will not
// take us in (not registered / wrong phase) — reported once per activityId.
func (p *EventPlayer) bgHandleEnterAct(data []byte) {
	var resp proto.BallGVGEnterActivityRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [BallGVG] bad enter resp: %v", p.Username, err)
		return
	}
	actID := resp.GetUserData().GetActivityId()
	st := p.bgState()
	p.BgMu.Lock()
	if actID != 0 && st.ActivityID != 0 && actID != st.ActivityID {
		p.BgMu.Unlock()
		return // a stale answer for a previous activity instance
	}
	if resp.GetRet() != 0 {
		ret := resp.GetRet()
		notify := !st.notifiedJoin && st.ActivityID != 0
		st.notifiedJoin = true
		st.Joined = false
		p.BgMu.Unlock()
		log.Printf("[%s] [BallGVG] enter refused: ret=%d", p.Username, ret)
		if notify {
			p.sendNotify(fmt.Sprintf("⚠️ [Thượng Cổ] acc chưa được xếp vào sự kiện, cần đăng ký trong bang (lỗi %d)", ret))
		}
		return
	}
	first := !st.Joined
	st.Joined = true
	st.notifiedJoin = false
	bgApplyUserDataLocked(st, resp.UserData)
	if resp.Camp != nil {
		st.MajorPlaceID = resp.Camp.GetMajorPlaceId()
	}
	st.Scores = bgScoresFromSync(resp.Score)
	bgReplaceRosterLocked(st, resp.PlaceData)
	if resp.PlaceData == nil {
		st.PlaceID = 0
		st.Roster = map[int64]*bgRosterUser{}
	}
	p.BgMu.Unlock()
	if first {
		log.Printf("[%s] [BallGVG] joined: camp %d place %d", p.Username, st.CampID, st.PlaceID)
	}
	p.bgReportPosition()
}

// bgHandleEnterRoom confirms a room switch (21104) with its full roster.
func (p *EventPlayer) bgHandleEnterRoom(data []byte) {
	var resp proto.BallGVGEnterPlaceRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [BallGVG] bad enter-room resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [BallGVG] enter-room refused: ret=%d", p.Username, resp.GetRet())
		p.sendNotify(fmt.Sprintf("⚠️ [Thượng Cổ] vào cứ điểm thất bại (lỗi %d)", resp.GetRet()))
		return
	}
	st := p.bgState()
	p.BgMu.Lock()
	bgReplaceRosterLocked(st, resp.PlaceData)
	if resp.PlaceId != nil {
		st.PlaceID = resp.GetPlaceId()
	}
	if resp.TransferTime != nil {
		st.TransferTime = resp.GetTransferTime()
	}
	st.lastMovePlace = 0 // new room → report the centre position again
	place := st.PlaceID
	p.BgMu.Unlock()
	log.Printf("[%s] [BallGVG] entered room %d", p.Username, place)
	p.bgReportPosition()
}

// bgHandleLeaveRoom confirms 21105: back in the activity lobby, roster gone.
func (p *EventPlayer) bgHandleLeaveRoom(data []byte) {
	var resp proto.BallGVGLeavePlaceRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [BallGVG] bad leave-room resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [BallGVG] leave-room refused: ret=%d", p.Username, resp.GetRet())
		p.sendNotify(fmt.Sprintf("⚠️ [Thượng Cổ] rời cứ điểm thất bại (lỗi %d)", resp.GetRet()))
		return
	}
	st := p.bgState()
	p.BgMu.Lock()
	st.PlaceID = 0
	st.Roster = map[int64]*bgRosterUser{}
	p.BgMu.Unlock()
}

// bgHandleMoveRet: position reports are cosmetic; a refusal is log noise.
func (p *EventPlayer) bgHandleMoveRet(data []byte) {
	var resp proto.BallGVGMoveRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [BallGVG] bad move resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [BallGVG] position report refused: ret=%d", p.Username, resp.GetRet())
	}
}

// bgHandleAttackRet consumes our own 21107 answer: the server already
// computed the exchange — sync both HPs and surface refusals/kills.
func (p *EventPlayer) bgHandleAttackRet(data []byte) {
	var resp proto.BallGVGAttackRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [BallGVG] bad attack resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		// Auto strikes retry on the next open cooldown; a per-refusal
		// notify would flood the leader chat during a battle.
		log.Printf("[%s] [BallGVG] attack refused: ret=%d", p.Username, resp.GetRet())
		return
	}
	var killName string
	st := p.bgState()
	p.BgMu.Lock()
	bgApplyUserDataLocked(st, resp.UserData)
	if atk := resp.GetAttack(); atk != nil {
		if atk.AttackHp != nil {
			st.HP = atk.GetAttackHp() // our HP after the exchange
		}
		if victim := st.Roster[atk.GetAttackedPlayerId()]; victim != nil {
			if atk.AttackedHp != nil {
				victim.HP = atk.GetAttackedHp()
			}
			if bgZeroHP(victim.HP) {
				killName = victim.Name
				if killName == "" {
					killName = "?"
				}
			}
		}
	}
	score := resp.GetAttackScore()
	p.BgMu.Unlock()
	log.Printf("[%s] [BallGVG] attack ok, score+=%d", p.Username, score)
	if killName != "" {
		p.sendNotify(fmt.Sprintf("⚔️ [Thượng Cổ] hạ được %s", killName))
	}
}

// bgHandleMarkRet reports the rally-mark result (server checks the camp
// leader role; refusal means "not your call").
func (p *EventPlayer) bgHandleMarkRet(data []byte) {
	var resp proto.BallGVGMarkPlaceRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [BallGVG] bad mark resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [BallGVG] mark refused: ret=%d", p.Username, resp.GetRet())
		p.sendNotify(fmt.Sprintf("⚠️ [Thượng Cổ] đánh dấu thất bại (chỉ trưởng phe mới đánh dấu được, lỗi %d)", resp.GetRet()))
		return
	}
	log.Printf("[%s] [BallGVG] mark accepted", p.Username)
}

// bgHandlePlaceInfo refreshes the ownership map used by the map overlay and
// the seize-flip detector.
func (p *EventPlayer) bgHandlePlaceInfo(data []byte) {
	var resp proto.BallGVGGetPlaceInfoRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [BallGVG] bad place info: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		return
	}
	owners := map[int32]int32{}
	for _, pl := range resp.GetPlace() {
		owners[pl.GetPlaceId()] = pl.GetSeizeCampId()
	}
	st := p.bgState()
	p.BgMu.Lock()
	st.Owners = owners
	p.BgMu.Unlock()
}

// bgHandleFightSync patches roster HPs from others' exchanges; an ally
// dropping to zero HP is worth one notify (the 21124 push finalizes it).
func (p *EventPlayer) bgHandleFightSync(data []byte) {
	var sync proto.BallGVGAttackedSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [BallGVG] bad fight sync: %v", p.Username, err)
		return
	}
	var down []string
	st := p.bgState()
	p.BgMu.Lock()
	for _, atk := range sync.GetAttack() {
		if v := st.Roster[atk.GetAttackedPlayerId()]; v != nil && atk.AttackedHp != nil {
			wasAlive := !bgZeroHP(v.HP)
			v.HP = atk.GetAttackedHp()
			if wasAlive && bgZeroHP(v.HP) && v.CampID == st.CampID {
				name := v.Name
				if name == "" {
					name = "?"
				}
				down = append(down, name)
			}
		}
		if a := st.Roster[atk.GetAttackPlayerId()]; a != nil && atk.AttackHp != nil {
			a.HP = atk.GetAttackHp()
		}
	}
	p.BgMu.Unlock()
	for _, name := range down {
		p.sendNotify(fmt.Sprintf("☠️ [Thượng Cổ] %s vừa bị hạ", name))
	}
}

// bgHandleScoreSync REPLACES the camp scoreboard.
func (p *EventPlayer) bgHandleScoreSync(data []byte) {
	var sync proto.BallGVGCampScoreSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [BallGVG] bad score sync: %v", p.Username, err)
		return
	}
	st := p.bgState()
	p.BgMu.Lock()
	st.Scores = bgScoresFromSync(sync.GetCampScore())
	p.BgMu.Unlock()
}

// bgHandlePositionSync refreshes known roster positions.
func (p *EventPlayer) bgHandlePositionSync(data []byte) {
	var sync proto.BallGVGPlaceSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [BallGVG] bad position sync: %v", p.Username, err)
		return
	}
	st := p.bgState()
	p.BgMu.Lock()
	for _, u := range sync.GetUser() {
		if e := st.Roster[u.GetPlayerId()]; e != nil && u.Position != nil {
			e.X, e.Y = u.Position.GetX(), u.Position.GetY()
		}
	}
	p.BgMu.Unlock()
}

// bgHandleCampSync REPLACES camp metadata (rally mark).
func (p *EventPlayer) bgHandleCampSync(data []byte) {
	var sync proto.BallGVGCampSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [BallGVG] bad camp sync: %v", p.Username, err)
		return
	}
	st := p.bgState()
	p.BgMu.Lock()
	if sync.Camp != nil {
		st.CampID = sync.Camp.GetCampId()
		st.MajorPlaceID = sync.Camp.GetMajorPlaceId()
	}
	p.BgMu.Unlock()
}

// bgHandleBeatenSync is the authoritative "we were attacked" frame: userData
// must REPLACE ours; zero HP after the hit means death (no revive request
// exists — the account waits out userData.reviveTime).
func (p *EventPlayer) bgHandleBeatenSync(data []byte) {
	var sync proto.BallGVGAttackedUserSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [BallGVG] bad beaten sync: %v", p.Username, err)
		return
	}
	killedBy := sync.GetAttackUserData().GetPlayerData().GetPlayerBaseDataMsg().GetNickName()
	if killedBy == "" {
		killedBy = "đối thủ"
	}
	ourHPAfter := sync.GetAttack().GetAttackedHp()
	attackerHP := sync.GetAttack().GetAttackHp()
	st := p.bgState()
	p.BgMu.Lock()
	bgApplyUserDataLocked(st, sync.UserData)
	if ourHPAfter != "" {
		st.HP = ourHPAfter
	}
	if a := st.Roster[sync.GetAttack().GetAttackPlayerId()]; a != nil && attackerHP != "" {
		a.HP = attackerHP
	}
	dead := bgZeroHP(st.HP) && bgZeroHP(ourHPAfter)
	revive := st.ReviveTime
	p.BgMu.Unlock()
	if dead {
		msg := fmt.Sprintf("☠️ [Thượng Cổ] bị %s đánh gục", killedBy)
		if left, waiting := bgReviveLeft(revive, time.Now()); waiting {
			msg += fmt.Sprintf(", hồi sinh sau %dm%02ds", int(left.Minutes()), int(left.Seconds())%60)
		}
		log.Printf("[%s] [BallGVG] beaten down by %s, reviveTime=%d", p.Username, killedBy, revive)
		p.sendNotify(msg)
	}
}

// bgHandleSeizeSync tracks owner flips per place: our captures cheer, lost
// places alarm; third-party churn is log-only. Owners start empty, so the
// first sighting of a flip notifies at most once per place.
func (p *EventPlayer) bgHandleSeizeSync(data []byte) {
	var sync proto.BallGVGPlaceSeizeSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [BallGVG] bad seize sync: %v", p.Username, err)
		return
	}
	st := p.bgState()
	p.BgMu.Lock()
	placeID := sync.GetPlaceId()
	owner := sync.GetCampId()
	prev, known := st.Owners[placeID]
	if st.Owners == nil {
		st.Owners = map[int32]int32{}
	}
	st.Owners[placeID] = owner
	ours := st.CampID
	label := bgRoomName(st, placeID)
	p.BgMu.Unlock()
	if !known || owner == prev {
		return
	}
	switch {
	case owner == ours:
		log.Printf("[%s] [BallGVG] camp %d seized place %d", p.Username, ours, placeID)
		p.sendNotify(fmt.Sprintf("🏯 [Thượng Cổ] phe chiếm được cứ điểm %s", label))
	case prev == ours:
		log.Printf("[%s] [BallGVG] lost place %d to camp %d", p.Username, placeID, owner)
		p.sendNotify(fmt.Sprintf("🚨 [Thượng Cổ] mất cứ điểm %s vào phe %d", label, owner))
	}
}

// bgHandleJoinSync adds (or replaces) a room occupant.
func (p *EventPlayer) bgHandleJoinSync(data []byte) {
	var sync proto.BallGVGEnterPlaceSync
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [BallGVG] bad join sync: %v", p.Username, err)
		return
	}
	if sync.User == nil {
		return
	}
	e := bgRosterUserLocked(sync.User)
	if e.PlayerID == 0 {
		return
	}
	st := p.bgState()
	p.BgMu.Lock()
	if st.Roster == nil {
		st.Roster = map[int64]*bgRosterUser{}
	}
	st.Roster[e.PlayerID] = e
	p.BgMu.Unlock()
}

// bgHandleDropSync removes or death-marks a room occupant: type 1 = removed
// (dead body leaves), type 2 = died in place (stays, dead), anything else =
// walked out.
func (p *EventPlayer) bgHandleDropSync(data []byte) {
	var sync proto.BallGVGLeavePlaceSync
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [BallGVG] bad drop sync: %v", p.Username, err)
		return
	}
	st := p.bgState()
	p.BgMu.Lock()
	e := st.Roster[sync.GetPlayerId()]
	if e != nil {
		if sync.GetType() == 2 {
			e.HP = "0"
		} else {
			delete(st.Roster, sync.GetPlayerId())
		}
	}
	p.BgMu.Unlock()
}

// bgHandleEndData reports the round settlement.
func (p *EventPlayer) bgHandleEndData(data []byte) {
	var sync proto.BallGVGEndDataSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [BallGVG] bad end data: %v", p.Username, err)
		return
	}
	ed := sync.GetEndData()
	if ed == nil {
		return
	}
	var top []string
	for _, c := range ed.GetCampScore() {
		if c.GetRank() == 1 {
			top = append(top, fmt.Sprintf("phe %d: %d điểm", c.GetCampId(), c.GetScore()))
		}
	}
	bs := ed.GetBattleScore()
	msg := fmt.Sprintf("🏁 [Thượng Cổ] tổng kết hiệp: diệt %d · chiếm %d · %d điểm (hạng %d)",
		bs.GetKill(), bs.GetSeize(), ed.GetScore(), ed.GetRank())
	if len(top) > 0 {
		msg += " · nhất: " + strings.Join(top, ", ")
	}
	log.Printf("[%s] [BallGVG] round settled", p.Username)
	p.sendNotify(msg)
}

// ---------------------------------------------------------------------------
// Leader-command guards + public API (user-facing errors in game language).
// ---------------------------------------------------------------------------

// BallGVGEventOpen reports whether the event runs for this account RIGHT
// NOW: the instance must be announced AND the activity window be active.
// The announced id alone used to be enough, which kept Di Tích visible
// after the event ended (bug 2026-10: "di tich not running but still
// show"). Orders are NOT gated here — bgOrder keeps its own announced +
// Joined checks so queueing behaviour is unchanged.
func (p *EventPlayer) BallGVGEventOpen() bool {
	p.BgMu.Lock()
	announced := p.bg != nil && p.bg.ActivityID != 0
	p.BgMu.Unlock()
	if !announced {
		return false
	}
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeBallGVG) {
		if e.status(now) == "active" {
			return true
		}
	}
	return false
}

// bgOrder validates a general order: logged in, event announced, account
// registered in the event, not waiting on a revive.
func (p *EventPlayer) bgOrder() (*ballGVGState, error) {
	if !p.Running {
		return nil, fmt.Errorf("chưa login")
	}
	st := p.bgState()
	p.BgMu.Lock()
	defer p.BgMu.Unlock()
	if st.ActivityID == 0 {
		return nil, fmt.Errorf("sự kiện Thượng Cổ Di Tích chưa mở")
	}
	if !st.Joined {
		return nil, fmt.Errorf("acc chưa được xếp vào Thượng Cổ Di Tích (bang cần đăng ký trong game)")
	}
	if _, waiting := bgReviveLeft(st.ReviveTime, time.Now()); waiting {
		left, _ := bgReviveLeft(st.ReviveTime, time.Now())
		return nil, fmt.Errorf("quân đang chờ hồi sinh sau %dm%02ds",
			int(left.Minutes()), int(left.Seconds())%60)
	}
	return st, nil
}

// BallGVGEnterRoom joins battlefield room placeID (/ditich_enter).
func (p *EventPlayer) BallGVGEnterRoom(placeID int32) error {
	if placeID <= 0 {
		return fmt.Errorf("cứ điểm không hợp lệ")
	}
	st, err := p.bgOrder()
	if err != nil {
		return err
	}
	p.bgSend(BALL_GVG_ENTER_ROOM_CMD, &proto.BallGVGEnterPlaceReqMsg{
		ActivityId: st.ActivityID, PlaceId: placeID,
	})
	log.Println(p.Username, "[BallGVG] nhận lệnh vào cứ điểm", placeID)
	return nil
}

// BallGVGHome returns to our camp's home room (/ditich_ve) — home is the
// fixed placeId equal to our campId.
func (p *EventPlayer) BallGVGHome() error {
	st, err := p.bgOrder()
	if err != nil {
		return err
	}
	p.BgMu.Lock()
	camp, actID := st.CampID, st.ActivityID
	p.BgMu.Unlock()
	if camp <= 0 {
		return fmt.Errorf("chưa biết phe của quân, thử lại sau")
	}
	p.bgSend(BALL_GVG_ENTER_ROOM_CMD, &proto.BallGVGEnterPlaceReqMsg{
		ActivityId: actID, PlaceId: camp,
	})
	log.Println(p.Username, "[BallGVG] nhận lệnh về cứ điểm nhà, camp", camp)
	return nil
}

// BallGVGLeaveRoom steps back to the activity lobby (/ditich_leave). Also
// allowed while dead — only the join/open guards apply.
func (p *EventPlayer) BallGVGLeaveRoom() error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	st := p.bgState()
	p.BgMu.Lock()
	actID := st.ActivityID
	joined := st.Joined
	p.BgMu.Unlock()
	if actID == 0 {
		return fmt.Errorf("sự kiện Thượng Cổ Di Tích chưa mở")
	}
	if !joined {
		return fmt.Errorf("acc chưa được xếp vào Thượng Cổ Di Tích")
	}
	p.bgSend(BALL_GVG_LEAVE_ROOM_CMD, &proto.BallGVGLeavePlaceReqMsg{ActivityId: actID})
	log.Println(p.Username, "[BallGVG] nhận lệnh rời cứ điểm")
	return nil
}

// BallGVGMark sets the camp rally mark on placeID (/ditich_mark). Only the
// camp's major (leader) passes the server check; refusals go to the leader.
func (p *EventPlayer) BallGVGMark(placeID int32) error {
	if placeID <= 0 {
		return fmt.Errorf("cứ điểm không hợp lệ")
	}
	st, err := p.bgOrder()
	if err != nil {
		return err
	}
	p.bgSend(BALL_GVG_MARK_CMD, &proto.BallGVGMarkPlaceReqMsg{
		ActivityId: st.ActivityID, PlaceId: placeID,
	})
	log.Println(p.Username, "[BallGVG] nhận lệnh đánh dấu cứ điểm", placeID)
	return nil
}

// BallGVGRooms renders the cached room table for the leader (/ditich_rooms).
func (p *EventPlayer) BallGVGRooms() string {
	st := p.bgState()
	p.BgMu.Lock()
	if len(st.Rooms) == 0 {
		p.BgMu.Unlock()
		return "chưa tải được danh sách cứ điểm, thử lại sau"
	}
	ids := make([]int, 0, len(st.Rooms))
	for id := range st.Rooms {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	lines := []string{"🏯 Cứ điểm Thượng Cổ Di Tích:"}
	for _, id := range ids {
		r := st.Rooms[int32(id)]
		kind := "cố định"
		if r.GetType() >= 4 {
			kind = "tranh chấp"
		}
		owner := ""
		if c := st.Owners[int32(id)]; c != 0 {
			owner = fmt.Sprintf(" · phe %d đang giữ", c)
		}
		mark := ""
		if st.MajorPlaceID == r.GetId() {
			mark = " · 🚩 điểm tập kết"
		}
		lines = append(lines, fmt.Sprintf("  • %d — %s (%s)%s%s", r.GetId(), r.GetName(), kind, owner, mark))
	}
	p.BgMu.Unlock()
	return strings.Join(lines, "\n")
}

// BallGVGRoster renders the current room's occupants (/ditich_roster).
func (p *EventPlayer) BallGVGRoster() string {
	st := p.bgState()
	p.BgMu.Lock()
	defer p.BgMu.Unlock()
	if st.PlaceID == 0 || len(st.Roster) == 0 {
		return "quân đang không ở cứ điểm nào (/ditich_enter <id> trước)"
	}
	now := time.Now()
	lines := []string{fmt.Sprintf("👥 Phòng %s — %d người:", bgRoomName(st, st.PlaceID), len(st.Roster))}
	ids := make([]int64, 0, len(st.Roster))
	for id := range st.Roster {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := st.Roster[ids[i]], st.Roster[ids[j]]
		if a.CampID != b.CampID {
			return a.CampID < b.CampID
		}
		return a.Name < b.Name
	})
	for i, id := range ids {
		if i >= bgMaxRosterLines {
			lines = append(lines, fmt.Sprintf("  … còn %d người nữa", len(ids)-i))
			break
		}
		e := st.Roster[id]
		state := "sống"
		if !e.alive(now) {
			state = "chết"
		}
		tag := ""
		if e.CampID == st.CampID {
			tag = " 🟢"
		}
		name := e.Name
		if name == "" {
			name = "?"
		}
		lines = append(lines, fmt.Sprintf("  • %s — phe %d · %s%s", name, e.CampID, state, tag))
	}
	return strings.Join(lines, "\n")
}

// BallGVGStatus renders the one-line Vietnamese status shown in
// /my_event_members and the web card.
func (p *EventPlayer) BallGVGStatus() string {
	if !p.Running {
		return "⏹️ chưa login"
	}
	st := p.bgState()
	p.BgMu.Lock()
	defer p.BgMu.Unlock()
	if st.ActivityID == 0 {
		return "⏳ Thượng Cổ chưa mở"
	}
	if !st.Joined {
		return "⏳ Thượng Cổ: chưa được xếp vào sự kiện"
	}
	if left, waiting := bgReviveLeft(st.ReviveTime, time.Now()); waiting {
		return fmt.Sprintf("☠️ Thượng Cổ: chờ hồi sinh sau %dm%02ds",
			int(left.Minutes()), int(left.Seconds())%60)
	}
	score := ""
	if s, ok := st.Scores[st.CampID]; ok {
		score = fmt.Sprintf(" · điểm phe %d", s)
	}
	if st.PlaceID == 0 {
		return fmt.Sprintf("🏯 Thượng Cổ: phe %d · ngoài sảnh%s", st.CampID, score)
	}
	return fmt.Sprintf("🏯 Thượng Cổ: phe %d · cứ điểm %s%s", st.CampID, bgRoomName(st, st.PlaceID), score)
}
