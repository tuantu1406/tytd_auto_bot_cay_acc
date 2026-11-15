package tytd_go

// KunlunWar ("崑崙崢覇", activity TYPE 199) — union city-war on a static
// 61×61 iso map. This pass wires TRANSPORT + AUTHORITATIVE MAP CACHE only
// (HolyLand precedent): enter/heartbeat/minimap/group reads, push-sync
// folding into the board cache, and the mapstream payload. No auto-move /
// auto-attack logic lives here yet.
//
// Protocol: requests 219901..219959 → transport 4dbd..4df7 (%100000 rule);
// push-only syncs 4ddb..4df4 carry board deltas. Schemas in
// internal/proto/tytd.proto (recovered 2026-10-05 from the CDN protobuf/
// KunlunWar asset, /workspace/remoteKunlunWar). All block keys are
// posIndex = y*61+x; march timing is SERVER ms (arrival−time), never client
// extrapolation (see memory [[tytd-times-utc7]] for schedule conversions).

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const activityTypeKunlunWar int32 = 199

var (
	errKuBusy    = fmt.Errorf("một lệnh kunlun đang chờ")
	errKuTimeout = errors.New("server không trả lời Côn Lôn, thử lại sau")
	errKuGone    = errors.New("rớt kết nối server rồi đạo hữu")
	errKuClosed  = errors.New("sự kiện Côn Lôn chưa mở")
)

// kunlunState is the per-account KunlunWar ledger (guarded by KuMu).
type kunlunState struct {
	ActivityID int32
	BeginTime  int64 // ms, from the activity-list entry
	EndTime    int64

	Cfg *proto.KunlunWarConfigSync // activity-detail uniqueConfig (may be nil)
	// OccupyCost is the KUNLUNWAR_OCCUPY_AREA_COST param (client's
	// blockMaxAttackTimes): a tile is captured at occupyProgress ≥ 10×cost.
	// 0 = config not seen yet — UI shows raw progress only.
	OccupyCost int32

	Enter  *proto.KunlunWarEnterActivityResp // self rank/floor/group
	Game   *proto.KunlunWarEnterGameResp     // personal scene snapshot
	Mini   *proto.KunlunWarMiniMapInfoRespMsg
	MiniAt time.Time

	// Board cache folded from Mini + pushes. Keys: cityId / posIndex.
	CellOwner map[int32]*proto.KunlunWarMiniCellInfo
	CityOwner map[int32]*proto.KunlunWarMiniCityInfo
	Unions    map[int64]*proto.KunlunUnionBaseData // id → name/flag (any source)

	Group  *proto.KunlunWarGroupInfoResp
	LastAt map[string]time.Time // last round-trip per cmd (log only)

	// SelfPoint is where the leader's own army stands (posIndex; 0 =
	// beenKill). Fed by EnterGame's myPlayerInfo and the 219933 sync —
	// the anchor /kunlun_follow marches event accounts to.
	SelfPoint int32

	// Routes folds the client's live-march push (219943 KunlunGameRouteInfo
	// SyncMsg) keyed by playerId; the /kunlun mini app animates these as the
	// game's LineItem sprites. Entries past arrival+grace prune on next fold.
	Routes map[int64]kuRouteRec
	// MyUnion is self's union id (EnterGame myPlayerInfo) — the line-kind
	// classifier: own marches draw imgLine2, own union imgLine3.
	MyUnion int64
	// MiniRaw is the last 219919 answer bytes — the palette-slot source
	// for route colors without reparsing the board on every tick.
	MiniRaw []byte

	// Scene-keep for the /kunlun mini app: wantAt is the last tick anyone
	// asked for kunlun frames; while it stays fresh the sceneLoop keeps
	// this socket INSIDE the scene (219902 + 219917/15s) — the 219943
	// march broadcasts only flow to sockets the server holds in-scene, so
	// residency is what makes EVERY player's march show up, not just ours.
	wantAt    time.Time
	sceneLoop bool
}

// kuRouteRec is one live march (server ms timestamps, pass-through).
type kuRouteRec struct {
	From, To int32
	Time     int64 // depart, server ms
	Arrival  int64 // arrival, server ms
	UnionID  int64
	BackHome bool // playerInfo.backHomeMsg set → heading home sprite
}

func newKunlunState() *kunlunState {
	return &kunlunState{
		CellOwner: map[int32]*proto.KunlunWarMiniCellInfo{},
		CityOwner: map[int32]*proto.KunlunWarMiniCityInfo{},
		Unions:    map[int64]*proto.KunlunUnionBaseData{},
		LastAt:    map[string]time.Time{},
		Routes:    map[int64]kuRouteRec{},
	}
}

func (p *Player) kuState() *kunlunState {
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	if p.ku == nil {
		p.ku = newKunlunState()
	}
	return p.ku
}

// ---- activity window -------------------------------------------------------

// KunlunWarActivityID is the live instance id of an OPEN KunlunWar window
// (0 = closed). ActList is self-synchronised (login push / 21036 / 21003).
func (p *Player) KunlunWarActivityID() int32 {
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeKunlunWar) {
		if e.status(now) == "active" {
			return e.ActivityID
		}
	}
	return 0
}

func (p *Player) kuWindow() (id int32, begin, end int64) {
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeKunlunWar) {
		if e.status(now) == "active" {
			return e.ActivityID, e.BeginTime, e.EndTime
		}
	}
	return 0, 0, 0
}

// kuPhase classifies now against peakHoldTimeConfigs exactly like the
// client's getKunlunWarTimeState: SignUp(1)/Prepare(2)/Battle(3) windows
// begin beginTime HOURS after the activity start; gaps between configured
// windows are Prepare; SETTLE after the last window + KUNLUNWAR_SETTLE_TIME
// (300s), then Reward until the activity ends.
func (st *kunlunState) kuPhase(now time.Time) string {
	if st.Cfg == nil || len(st.Cfg.GetPeakHoldTimeConfigs()) == 0 {
		return "unknown"
	}
	ph := st.Cfg.GetPeakHoldTimeConfigs()
	sort.Slice(ph, func(i, j int) bool { return ph[i].GetBeginTime() < ph[j].GetBeginTime() })
	ms := now.UnixMilli()
	if st.BeginTime == 0 || ms < st.BeginTime {
		return "not_open"
	}
	var lastEnd int64
	for _, c := range ph {
		bs := st.BeginTime + int64(c.GetBeginTime())*3600_000
		be := bs + int64(c.GetDurationTime())*3600_000
		if be > lastEnd {
			lastEnd = be
		}
		if ms < bs {
			return "prepare" // gap before the next phase window
		}
		if ms < be {
			switch c.GetType() {
			case 1:
				return "signup"
			case 2:
				return "prepare"
			case 3:
				return "battle"
			}
			return "prepare"
		}
	}
	if ms > lastEnd+300_000 {
		return "reward"
	}
	return "settle"
}

// kuPhaseWindow is kuPhase plus the instant the current window flips
// (unix ms) — the /kunlun mini app's countdown strip. Falls back to the
// activity end for reward/settle.
func (st *kunlunState) kuPhaseWindow(now time.Time) (string, int64) {
	if st.Cfg == nil || len(st.Cfg.GetPeakHoldTimeConfigs()) == 0 {
		return "unknown", 0
	}
	ph := append([]*proto.KunlunPeakHoldTimeConfig(nil), st.Cfg.GetPeakHoldTimeConfigs()...)
	sort.Slice(ph, func(i, j int) bool { return ph[i].GetBeginTime() < ph[j].GetBeginTime() })
	ms := now.UnixMilli()
	var lastEnd int64
	for _, c := range ph {
		bs := st.BeginTime + int64(c.GetBeginTime())*3600_000
		be := bs + int64(c.GetDurationTime())*3600_000
		if be > lastEnd {
			lastEnd = be
		}
		if ms < bs {
			return "prepare", bs
		}
		if ms < be {
			switch c.GetType() {
			case 1:
				return "signup", be
			case 3:
				return "battle", be
			}
			return "prepare", be
		}
	}
	if ms > lastEnd+300_000 {
		return "reward", st.EndTime
	}
	return "settle", lastEnd + 300_000
}

// kuPhaseInfo is the mini app's countdown strip payload (nil until the
// config fold gives us real windows).
func (p *Player) kuPhaseInfo(now time.Time) *gimap.KuPhase {
	st := p.kuState()
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	ph, end := st.kuPhaseWindow(now)
	n := kuPhaseVN(ph)
	if n == "" {
		return nil
	}
	return &gimap.KuPhase{S: ph, N: n, End: end}
}

// kuPhaseVN renders the machine phase as the label the game itself shows.
func kuPhaseVN(phase string) string {
	switch phase {
	case "signup":
		return "Đăng ký"
	case "prepare":
		return "Chuẩn bị"
	case "battle":
		return "Giao chiến"
	case "settle":
		return "Tổng kết"
	case "reward":
		return "Nhận thưởng"
	}
	return ""
}

// kuRoutesSnapshot renders the folded marches for the /kunlun mini app
// frame. K follows the client's getLineImgName: 4 = heading home, 2 = this
// account itself, 3 = own union, 1 = enemy (friendly-union styling is v2).
func (p *Player) kuRoutesSnapshot(now time.Time) []gimap.KuRoute {
	st := p.kuState()
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	return kuRoutesFrom(st.Routes, st.MyUnion, int64(p.UID), st.MiniRaw, st.Unions, now)
}

// kuSceneKeep marks the mini app's march feed as wanted (called from the
// kunlun mapstream tick, i.e. only while someone watches) and lazily starts
// the client's own keep-in-scene rhythm. 219943 pushes only reach sockets
// the server holds INSIDE the scene — so while the page is open, the leader
// account sits in-scene and every player's march streams. The loop leaves
// the scene again once no frame has been wanted for a minute.
func (p *Player) kuSceneKeep(now time.Time) {
	if p.KunlunWarActivityID() == 0 {
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	st.wantAt = now
	running := st.sceneLoop
	st.sceneLoop = true
	p.KuMu.Unlock()
	if !running {
		go p.kuSceneLoop()
	}
}

func (p *Player) kuSceneLoop() {
	t := time.NewTicker(kuHeartbeatEvery)
	defer t.Stop()
	// First pass fires immediately: the seat was already *wanted* (kuSceneKeep
	// only starts this loop from a live viewer), and making that viewer wait a
	// whole heartbeat before the first EnterGame was part of the "vào map phải
	// đợi" complaint. Cadence afterwards stays on the 15s ticker.
	first := make(chan time.Time, 1)
	first <- time.Now()
	for {
		select {
		case <-p.ctx.Done():
			p.kuSceneEnd()
			return
		case <-t.C:
		case <-first:
		}
		if p.KunlunWarActivityID() == 0 {
			p.kuSceneEnd() // window closed: the scene dies with it anyway
			return
		}
		st := p.kuState()
		p.KuMu.Lock()
		idle := time.Since(st.wantAt)
		in := st.Game != nil
		p.KuMu.Unlock()
		if idle > time.Minute {
			p.kuSceneLeave()
			return
		}
		if !in {
			if _, err := p.RequestKunlunEnterGame(5 * time.Second); err != nil {
				log.Printf("[%s] [Kunlun] vào scene giữ feed: %v", p.Username, err)
				continue
			}
			debugf("[%s] [Kunlun] vào scene — feed hành quân trực tiếp bật", p.Username)
		}
		actID := p.KunlunWarActivityID()
		body, _ := pb.Marshal(&proto.KunlunWarHeartBeatReqMsg{ActivityId: &actID})
		select {
		case p.writeChan <- pack.Pack(KUNLUN_HEART_BEAT_CMD, body, p.UID):
		case <-p.ctx.Done():
			p.kuSceneEnd()
			return
		default: // pipe full — next tick retries
		}
	}
}

// kuSceneEnd forgets the loop flag without touching the server (window gone
// or socket dead — nobody to tell).
func (p *Player) kuSceneEnd() {
	st := p.kuState()
	p.KuMu.Lock()
	st.sceneLoop = false
	p.KuMu.Unlock()
}

// kuSceneLeave politely exits the scene after the last subscriber left.
func (p *Player) kuSceneLeave() {
	st := p.kuState()
	p.KuMu.Lock()
	st.sceneLoop = false
	in := st.Game != nil
	p.KuMu.Unlock()
	if !in {
		return
	}
	if err := p.RequestKunlunLeave(5 * time.Second); err != nil {
		log.Printf("[%s] [Kunlun] rời scene: %v", p.Username, err)
		return
	}
	p.KuMu.Lock()
	st.Game = nil
	p.KuMu.Unlock()
	debugf("[%s] [Kunlun] rời scene — tắt feed hành quân trực tiếp", p.Username)
}

// kuPruneRoutes drops expired marches (5 s render grace). Every writer must
// call it: 219943 pruned but the 219944 BackHome rows bypassed it, so a
// LINE-only push season grew the ledger unbounded (audit finding #3).
// Caller holds KuMu.
func kuPruneRoutes(recs map[int64]kuRouteRec) {
	cut := time.Now().UnixMilli() - 5000
	for pid, r := range recs {
		if r.Arrival < cut {
			delete(recs, pid)
		}
	}
}

// kuRoutesFrom is the shared ledger→frame fold (regular and event accounts
// keep the same kuRouteRec map — see kuBuildRouteLedger). Caller holds KuMu.
func kuRoutesFrom(recs map[int64]kuRouteRec, myUnion, selfPID int64, miniRaw []byte,
	unions map[int64]*proto.KunlunUnionBaseData, now time.Time) []gimap.KuRoute {
	cut := now.UnixMilli() - 5000
	out := make([]gimap.KuRoute, 0, len(recs))
	for rpid, r := range recs {
		if r.Arrival < cut {
			continue
		}
		k := int8(1)
		switch {
		case r.BackHome:
			k = 4
		case rpid == selfPID:
			k = 2
		case myUnion != 0 && r.UnionID == myUnion:
			k = 3
		}
		name := ""
		if u, ok := unions[r.UnionID]; ok {
			name = u.GetName()
		}
		out = append(out, gimap.KuRoute{
			FP: r.From, TP: r.To, T: r.Time, AR: r.Arrival, K: k,
			S: gimap.KuUnionSlot(miniRaw, r.UnionID), N: name,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AR < out[j].AR })
	return out
}

// kuFoldRouteInfos applies raw KunlunWarGameRouteInfo rows to a ledger map
// (nil map gets created lazily by the caller). Both the 219943 push and the
// 219903 view-window answer carry this exact element type, so the two feeds
// share one fold. Returns the number of rows applied.
func kuFoldRouteInfos(recs map[int64]kuRouteRec, ris []*proto.KunlunWarGameRouteInfo) int {
	n := 0
	for _, ri := range ris {
		pid := ri.GetPlayerId()
		if pid == 0 {
			continue
		}
		pi := ri.GetPlayerInfo()
		recs[pid] = kuRouteRec{
			From: ri.GetFromPoint(), To: ri.GetToPoint(),
			Time: ri.GetTime(), Arrival: ri.GetArrival(),
			UnionID: pi.GetUnionId(), BackHome: pi.GetBackHomeMsg() != nil,
		}
		n++
	}
	return n
}

// kuFoldLineInfos applies 219944 (KunlunWarGameLineInfoSyncMsg) queue rows to
// a march ledger, mirroring the client's syncLineInfo: inLineType==0 means an
// army just finished its fight and is marching HOME — the client synthesises a
// 15s BackHome route from outCityId→inCityId because 219943 never announces
// those. City-join/city-leave rows (lineType 2) and block-arrival rows
// (lineType 1) only move garrison rosters our ledger doesn't model: the
// standing player surfaces on the next 219903 view pull instead. City ids are
// resolved through KunlunCityPoint (getCityDisplayContent); unknown ids drop.
// Latest-wins by departure time so a fresher 219943 row survives. Returns the
// number of rows applied.
func kuFoldLineInfos(recs map[int64]kuRouteRec, lis []*proto.KunlunWarGameLineInfo) int {
	n := 0
	now := time.Now().UnixMilli()
	for _, l := range lis {
		pi := l.GetPlayerInfo()
		pid := l.GetPlayerId()
		if pid == 0 || l.GetInLineType() != 0 || pi == nil {
			continue
		}
		from, ok := int32(l.GetOutCityId()), true
		if l.GetOutLineType() == 2 { // leaving a CITY: outCityId is a cityId
			from, ok = gimap.KunlunCityPoint(uint64(l.GetOutCityId()))
		}
		to, okTo := gimap.KunlunCityPoint(uint64(l.GetInCityId()))
		if !ok || !okTo || l.GetOutCityId() == l.GetInCityId() {
			continue
		}
		if cur := recs[pid]; cur.Time > now {
			continue // a real march row (219943) is already newer
		}
		recs[pid] = kuRouteRec{
			From: from, To: to,
			Time: now, Arrival: now + 15000, // the client's own 15s home synth
			UnionID: pi.GetUnionId(), BackHome: true,
		}
		n++
	}
	return n
}

// kuBuildRouteRecs folds one 219943 answer into a ledger map. Returns the
// number of rows applied.
func kuBuildRouteRecs(recs map[int64]kuRouteRec, data []byte) int {
	var rsp proto.KunlunGameRouteInfoSyncMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return 0
	}
	return kuFoldRouteInfos(recs, rsp.GetRouteInfo())
}

// kunlunWarOpen is the type-199 activity-list window (same source of truth
// as the mapstream gates on the other battle maps).
func kunlunWarOpen(p *Player) bool {
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeKunlunWar) {
		if e.status(now) == "active" {
			return true
		}
	}
	return false
}

// kuSeatBusy reports whether one response seat is already taken (streamer
// eligibility check — never claims).
func (p *Player) kuSeatBusy(msgType string) bool {
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	_, busy := p.kuChans[msgType]
	return busy
}

// ---- seats (composeBall bcChans/cbChans pattern) ----------------------------

func (p *Player) kuClaim(msgType string) (chan []byte, error) {
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	if p.kuChans == nil {
		p.kuChans = map[string]chan []byte{}
	}
	if _, busy := p.kuChans[msgType]; busy {
		return nil, errKuBusy
	}
	ch := make(chan []byte, 1)
	p.kuChans[msgType] = ch
	return ch, nil
}

func (p *Player) kuRelease(msgType string, ch chan []byte) {
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	if cur, ok := p.kuChans[msgType]; ok && cur == ch {
		delete(p.kuChans, msgType)
	}
}

// kuDeliver hands one answer to a waiting seat AND folds push-only board
// deltas into the cache. Reader-goroutine safe: never blocks.
func (p *Player) kuDeliver(msgType string, data []byte) {
	switch msgType {
	case SERVER_KUNLUN_BLOCK_SYNC, SERVER_KUNLUN_CITY_SYNC, SERVER_KUNLUN_MINI_MAP_INFO,
		SERVER_KUNLUN_CITY_BATTLE_SYNC, SERVER_KUNLUN_ATTACK_SYNC, SERVER_KUNLUN_GROUP_RANK_SYNC,
		SERVER_KUNLUN_MY_GAME_SYNC, SERVER_KUNLUN_LEFT_OUT_SYNC, SERVER_KUNLUN_ROUTE_SYNC,
		SERVER_KUNLUN_LINE_SYNC:
		p.kuFold(msgType, data)
	}
	p.KuMu.Lock()
	ch := p.kuChans[msgType]
	p.KuMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// kuFold merges one push/mini answer into the board cache.
func (p *Player) kuFold(msgType string, data []byte) {
	st := p.kuState()
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	switch msgType {
	case SERVER_KUNLUN_MINI_MAP_INFO:
		var rsp proto.KunlunWarMiniMapInfoRespMsg
		if pb.Unmarshal(data, &rsp) != nil {
			return
		}
		st.Mini = &rsp
		st.MiniRaw = data
		st.MiniAt = time.Now()
		for _, c := range rsp.GetCityInfo() {
			st.CityOwner[c.GetCityId()] = c
		}
		for _, c := range rsp.GetCellInfo() {
			st.CellOwner[c.GetIndex()] = c
		}
		for _, u := range rsp.GetBaseData() {
			st.Unions[u.GetUnionId()] = u
		}
	case SERVER_KUNLUN_CITY_SYNC:
		var rsp proto.KunlunWarCitySync
		if pb.Unmarshal(data, &rsp) != nil {
			return
		}
		for _, c := range rsp.GetCityInfo() {
			owner := st.CityOwner[c.GetCityId()]
			if owner == nil {
				owner = &proto.KunlunWarMiniCityInfo{CityId: c.CityId}
				st.CityOwner[c.GetCityId()] = owner
			}
			owner.UnionId = c.UnionId
			owner.HaveFight = c.HaveFight
			if mi := c.GetDisplayInfo(); mi != nil {
				_ = mi // display roster lives on the full city info, not the mini row
			}
		}
	case SERVER_KUNLUN_BLOCK_SYNC:
		var rsp proto.KunlunWarBlockSync
		if pb.Unmarshal(data, &rsp) != nil {
			return
		}
		for _, b := range rsp.GetBlockInfo() {
			cell := st.CellOwner[b.GetPos()]
			if cell == nil {
				cell = &proto.KunlunWarMiniCellInfo{Index: b.Pos}
				st.CellOwner[b.GetPos()] = cell
			}
			if len(b.GetUnionBlockData()) > 0 {
				cell.UnionId = pb.Int64(b.GetUnionBlockData()[0].GetUnionId())
			} else {
				cell.UnionId = nil
			}
		}
	case SERVER_KUNLUN_CITY_BATTLE_SYNC:
		var rsp proto.KunlunWarGameCityBattleSyncMsg
		if pb.Unmarshal(data, &rsp) != nil {
			return
		}
		for _, cs := range rsp.GetCityStates() {
			owner := st.CityOwner[cs.GetCityId()]
			if owner == nil {
				owner = &proto.KunlunWarMiniCityInfo{CityId: cs.CityId}
				st.CityOwner[cs.GetCityId()] = owner
			}
			owner.HaveFight = cs.IsBattle
		}
	case SERVER_KUNLUN_ATTACK_SYNC:
		var rsp proto.KunlunWarAttackCitySyncMsg
		if pb.Unmarshal(data, &rsp) != nil {
			return
		}
		for _, a := range rsp.GetAttackCityInfo() {
			owner := st.CityOwner[a.GetCityId()]
			if owner == nil {
				owner = &proto.KunlunWarMiniCityInfo{CityId: a.CityId}
				st.CityOwner[a.GetCityId()] = owner
			}
			if len(a.GetPlayerIds()) > 0 {
				owner.UnionId = pb.Int64(a.GetPlayerIds()[0].GetUnionId())
			}
		}
	case SERVER_KUNLUN_GROUP_RANK_SYNC:
		var rsp proto.KunlunWarGroupRankSync
		if pb.Unmarshal(data, &rsp) != nil {
			return
		}
		if u, ok := st.Unions[rsp.GetUnionId()]; ok {
			_ = u // rank is display-only; names stay authoritative
		}
	case SERVER_KUNLUN_MY_GAME_SYNC:
		var rsp proto.KunlunWarMyGameInfoSyncMsg
		if pb.Unmarshal(data, &rsp) != nil {
			return
		}
		st.SelfPoint = rsp.GetLocation()
	case SERVER_KUNLUN_LEFT_OUT_SYNC:
		var rsp proto.KunlunWarLeftOutSyncInfo
		if pb.Unmarshal(data, &rsp) != nil {
			return
		}
		for _, idx := range rsp.GetBlockIndex() {
			delete(st.CellOwner, idx)
		}
	case SERVER_KUNLUN_ROUTE_SYNC:
		if st.Routes == nil {
			st.Routes = map[int64]kuRouteRec{}
		}
		if n := kuBuildRouteRecs(st.Routes, data); n > 0 {
			debugf("[%s] [Kunlun] 219943: %d march(s) folded, ledger=%d", p.Username, n, len(st.Routes))
		}
		// Arrived marches stop being drawn; the push stream never sends an
		// explicit delete, so expire here with a small grace.
		kuPruneRoutes(st.Routes)
	case SERVER_KUNLUN_LINE_SYNC:
		// 219944: queue fights also carry the synthesized march-HOME rows
		// 219943 never sends (see kuFoldLineInfos) — fold them so returning
		// armies appear on the /kunlun map without waiting for a view pull.
		var rsp proto.KunlunWarGameLineInfoSyncMsg
		if pb.Unmarshal(data, &rsp) != nil {
			return
		}
		if st.Routes == nil {
			st.Routes = map[int64]kuRouteRec{}
		}
		if n := kuFoldLineInfos(st.Routes, rsp.GetLineInfo()); n > 0 {
			debugf("[%s] [Kunlun] 219944: %d home-march(es) folded, ledger=%d", p.Username, n, len(st.Routes))
		}
		kuPruneRoutes(st.Routes)
	}
}

// kuMergeConfig folds the KunlunWarConfigSync out of a 21003 detail answer
// (mirror of cbMergeConfig). Only type-199 rows carry it.
func (p *Player) kuMergeConfig(data []byte) {
	var rsp proto.RspGetActivityDetail
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	dc := rsp.GetActivity().GetDetailConfig()
	if dc.GetCommonConfig().GetMainConfig().GetType() != activityTypeKunlunWar {
		return
	}
	// KUNLUNWAR_OCCUPY_AREA_COST rides commonConfig.paramConfig (the client's
	// blockMaxAttackTimes); a tile is fully captured at occupyProgress ≥
	// 10×cost (getBlockMaxHp). Fold it even when uniqueConfig is absent so
	// the /kunlun popup bar still has its threshold.
	st := p.kuState()
	p.KuMu.Lock()
	for _, pc := range dc.GetCommonConfig().GetParamConfig() {
		if pc.GetParam() == "KUNLUNWAR_OCCUPY_AREA_COST" {
			if v, err := strconv.Atoi(pc.GetValue()); err == nil && v > 0 {
				st.OccupyCost = int32(v)
			}
		}
	}
	p.KuMu.Unlock()
	var sync proto.KunlunWarConfigSync
	if pb.Unmarshal(dc.GetUniqueConfig(), &sync) != nil || len(sync.GetPeakHoldTimeConfigs()) == 0 {
		return
	}
	p.KuMu.Lock()
	st.Cfg = &sync
	p.KuMu.Unlock()
	log.Printf("[%s] [Kunlun] config: %d phase windows, %d cities", p.Username,
		len(sync.GetPeakHoldTimeConfigs()), len(sync.GetCityConfig()))
}

func (p *Player) kuRoundTrip(cmd, wantType string, body []byte, timeout time.Duration) ([]byte, error) {
	ch, err := p.kuClaim(wantType)
	if err != nil {
		return nil, err
	}
	defer p.kuRelease(wantType, ch)
	// The war answer rides a SERVER_KUNLUN_* hex SHARED with Đỉnh Bắc Minh —
	// the reader routes by warKind, which must therefore claim the kunlun
	// side BEFORE the request goes out (a post-success flip would let the
	// in-flight answer sail to the north seats and this one time out).
	p.setWarKind(warKindKunlun)
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
	case <-p.ctx.Done():
		return nil, errKuGone
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, errKuTimeout
	case <-p.ctx.Done():
		return nil, errKuGone
	}
}

// ---- wire requests ---------------------------------------------------------

// RequestKunlunEnterActivity loads the roster/rank view and refreshes the
// ledger window fields (cheap, safe to call every map tick).
func (p *Player) RequestKunlunEnterActivity(timeout time.Duration) (*proto.KunlunWarEnterActivityResp, error) {
	id, begin, end := p.kuWindow()
	if id == 0 {
		return nil, errKuClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarEnterActivityReq{ActivityId: &id})
	raw, err := p.kuRoundTrip(KUNLUN_ENTER_ACTIVITY_CMD, SERVER_KUNLUN_ENTER_ACTIVITY, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarEnterActivityResp
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		return nil, err
	}
	st := p.kuState()
	p.KuMu.Lock()
	st.ActivityID, st.BeginTime, st.EndTime = id, begin, end
	st.Enter = &rsp
	p.KuMu.Unlock()
	return &rsp, nil
}

func (p *Player) RequestKunlunEnterGame(timeout time.Duration) (*proto.KunlunWarEnterGameResp, error) {
	id := p.KunlunWarActivityID()
	if id == 0 {
		return nil, errKuClosed
	}
	// War-scene transport invariant: Đỉnh Bắc Minh answers on these SAME
	// SERVER_KUNLUN_* hexes — refuse while the north scene holds the socket.
	if p.bmSeatedInScene() {
		return nil, fmt.Errorf("acc đang ngồi trong trận Đỉnh Bắc Minh — rời Bắc Minh trước đã")
	}
	body, _ := pb.Marshal(&proto.KunlunWarEnterGameReq{ActivityId: &id})
	raw, err := p.kuRoundTrip(KUNLUN_ENTER_GAME_CMD, SERVER_KUNLUN_ENTER_GAME, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarEnterGameResp
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		return nil, err
	}
	st := p.kuState()
	p.KuMu.Lock()
	st.Game = &rsp
	st.SelfPoint = rsp.GetMyPlayerInfo().GetPos()
	if u := rsp.GetMyPlayerInfo().GetUnionId(); u != 0 {
		st.MyUnion = u
	}
	p.KuMu.Unlock()
	return &rsp, nil
}

// KunlunLeaderPoint answers the block where the leader's own account stands
// — the /kunlun_follow anchor. EnterGame is (re)asked whenever the position
// is unknown; a leader whose army is dead (pos 0) has no valid anchor.
func (p *Player) KunlunLeaderPoint(to time.Duration) (int32, error) {
	if p.KunlunWarActivityID() == 0 {
		return 0, errKuClosed
	}
	st := p.kuState()
	p.KuMu.Lock()
	pt, fresh := st.SelfPoint, st.Game != nil
	p.KuMu.Unlock()
	if pt == 0 || !fresh {
		if _, err := p.RequestKunlunEnterGame(to); err != nil {
			return 0, err
		}
		p.KuMu.Lock()
		pt = st.SelfPoint
		p.KuMu.Unlock()
	}
	if pt <= 0 {
		return 0, fmt.Errorf("quân của bang chủ đang chết/chờ hồi sinh ở Côn Lôn — hồi sinh xong hãy follow")
	}
	return pt, nil
}

// RequestKunlunMiniMap pulls the WHOLE-map ownership snapshot of our floor
// (group=0 → server default bracket). This is the mapstream tick.
func (p *Player) RequestKunlunMiniMap(timeout time.Duration) ([]byte, error) {
	id := p.KunlunWarActivityID()
	if id == 0 {
		return nil, errKuClosed
	}
	st := p.kuState()
	p.KuMu.Lock()
	floor, group := int32(0), int32(0)
	if st.Enter != nil {
		floor, group = st.Enter.GetFloor(), st.Enter.GetGroup()
	}
	p.KuMu.Unlock()
	// group MUST ride with floor: an answer for floor=10000 without group
	// is a silently EMPTY board (live 710564, 2026-10-05 — 12 unions held
	// 250/140/110 city points but got cities=0 cells=0).
	body, _ := pb.Marshal(&proto.KunlunWarMiniMapInfoReqMsg{ActivityId: &id, Floor: &floor, Group: &group})
	if group == 0 {
		// Roster never loaded → ask EnterActivity once (cheap) and retry.
		if _, err := p.RequestKunlunEnterActivity(timeout); err == nil {
			p.KuMu.Lock()
			if st.Enter != nil {
				floor, group = st.Enter.GetFloor(), st.Enter.GetGroup()
			}
			p.KuMu.Unlock()
			body, _ = pb.Marshal(&proto.KunlunWarMiniMapInfoReqMsg{ActivityId: &id, Floor: &floor, Group: &group})
		}
	}
	return p.kuRoundTrip(KUNLUN_MINI_MAP_INFO_CMD, SERVER_KUNLUN_MINI_MAP_INFO, body, timeout)
}

// RequestKunlunGameBlockInfo pulls the view-window detail (219903): who
// STANDS on each requested block (displayInfo with names), the cities' real
// HP, and every march inside the window. The route rows fold into the same
// ledger the 219943 push maintains, so they reach the /kunlun page through
// the existing SSE tick. Only an account ALREADY in the scene is asked — a
// page view must never drag the leader's phone into the battlefield.
func (p *Player) RequestKunlunGameBlockInfo(blocks []int32, timeout time.Duration) (*proto.KunlunWarGameBlockInfoResp, error) {
	if len(blocks) == 0 {
		return nil, fmt.Errorf("không có ô nào để hỏi")
	}
	id := p.KunlunWarActivityID()
	if id == 0 {
		return nil, errKuClosed
	}
	st := p.kuState()
	p.KuMu.Lock()
	seated := st.Game != nil
	floor, group := int32(0), int32(0)
	if st.Enter != nil {
		floor, group = st.Enter.GetFloor(), st.Enter.GetGroup()
	}
	p.KuMu.Unlock()
	if !seated {
		return nil, fmt.Errorf("acc chưa đứng trong trận Côn Lôn")
	}
	if group == 0 {
		// Same floor+group requirement as 219919: an answer without the
		// group is a silently EMPTY window.
		if _, err := p.RequestKunlunEnterActivity(timeout); err == nil {
			p.KuMu.Lock()
			if st.Enter != nil {
				floor, group = st.Enter.GetFloor(), st.Enter.GetGroup()
			}
			p.KuMu.Unlock()
		}
	}
	body, _ := pb.Marshal(&proto.KunlunWarGameBlockInfoReq{
		ActivityId: &id, BlockVec: blocks, FloorId: &floor, GroupId: &group,
	})
	raw, err := p.kuRoundTrip(KUNLUN_GAME_BLOCK_INFO_CMD, SERVER_KUNLUN_GAME_BLOCK_INFO, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarGameBlockInfoResp
	if pb.Unmarshal(raw, &rsp) != nil {
		return nil, fmt.Errorf("trả lời Côn Lôn block-info không đọc được")
	}
	if rsp.GetRet() != 0 {
		return nil, fmt.Errorf("Côn Lôn block info ret=%d", rsp.GetRet())
	}
	p.KuMu.Lock()
	if st.Routes == nil {
		st.Routes = map[int64]kuRouteRec{}
	}
	if n := kuFoldRouteInfos(st.Routes, rsp.GetRouteInfo()); n > 0 {
		debugf("[%s] [Kunlun] 219903: %d march(s) folded, ledger=%d", p.Username, n, len(st.Routes))
	}
	p.KuMu.Unlock()
	return &rsp, nil
}

// RequestKunlunSparInfo asks the fruit tree (219910): canReceive 1 = at
// least one fruit is ripe for THIS account. Player-side mirror of
// EventPlayer.RequestKunlunSparInfo (the live probe runs on a session).
func (p *Player) RequestKunlunSparInfo(timeout time.Duration) (int32, error) {
	id := p.KunlunWarActivityID()
	if id == 0 {
		return 0, errKuClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarSparInfoReqMsg{ActivityId: &id})
	raw, err := p.kuRoundTrip(KUNLUN_SPAR_INFO_CMD, SERVER_KUNLUN_SPAR_INFO, body, timeout)
	if err != nil {
		return 0, err
	}
	var rsp proto.KunlunWarSparInfoRespMsg
	if pb.Unmarshal(raw, &rsp) != nil {
		return 0, fmt.Errorf("trả lời Côn Lôn spar-info không đọc được")
	}
	return rsp.GetCanReceive(), nil
}

// RequestKunlunSparPower claims the ripe fruit (219911) once and returns the
// verdict — the server decides the reward; there is no amount field. Claim
// is a state change, so this only exists for the live probe; user commands
// go through the explicit 🍈 button → EventPlayer.KunlunWarClaimFruit.
func (p *Player) RequestKunlunSparPower(timeout time.Duration) (*proto.KunlunWarGetSparPowerRespMsg, error) {
	id := p.KunlunWarActivityID()
	if id == 0 {
		return nil, errKuClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarGetSparPowerReqMsg{ActivityId: &id})
	raw, err := p.kuRoundTrip(KUNLUN_SPAR_POWER_CMD, SERVER_KUNLUN_SPAR_POWER, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarGetSparPowerRespMsg
	if pb.Unmarshal(raw, &rsp) != nil {
		return nil, fmt.Errorf("trả lời Côn Lôn hái quả không đọc được")
	}
	return &rsp, nil
}

// RequestKunlunUseItem is the round-trip 219926 "cắn quả" for the live probe.
// itemId must be an id SEEN in real server data (the account's own 19911
// reward, or the 105248 the user's own packet capture on 2026-10-06 showed) —
// never a guess. This is one of the two authorized kunlun item ops.
func (p *Player) RequestKunlunUseItem(itemId, num int32, timeout time.Duration) (*proto.KunlunWarUseItemRespMsg, error) {
	id := p.KunlunWarActivityID()
	if id == 0 {
		return nil, errKuClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarUseItemReqMsg{ActivityId: &id, ItemId: &itemId, Num: &num})
	raw, err := p.kuRoundTrip(KUNLUN_USE_ITEM_CMD, SERVER_KUNLUN_USE_ITEM, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarUseItemRespMsg
	if pb.Unmarshal(raw, &rsp) != nil {
		return nil, fmt.Errorf("trả lời Côn Lôn dùng vật phẩm không đọc được")
	}
	return &rsp, nil
}

// RequestKunlunGroupInfo lists one group's unions (names/flags/scores).
func (p *Player) RequestKunlunGroupInfo(group, floor int32, timeout time.Duration) (*proto.KunlunWarGroupInfoResp, error) {
	id := p.KunlunWarActivityID()
	if id == 0 {
		return nil, errKuClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarGroupInfoReq{ActivityId: &id, GroupId: &group, FloorId: &floor})
	raw, err := p.kuRoundTrip(KUNLUN_GROUP_INFO_CMD, SERVER_KUNLUN_GROUP_INFO, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarGroupInfoResp
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		return nil, err
	}
	st := p.kuState()
	p.KuMu.Lock()
	st.Group = &rsp
	for _, u := range rsp.GetGroupInfo() {
		st.Unions[u.GetUnionId()] = &proto.KunlunUnionBaseData{
			UnionId: u.UnionId, Flag: u.Flag, Name: u.UnionName, ServerId: u.ServerId,
		}
	}
	p.KuMu.Unlock()
	return &rsp, nil
}

// RequestKunlunLeave exits the GAME scene (219958) — call after a probe run;
// the activity roster view stays available without it.
func (p *Player) RequestKunlunLeave(timeout time.Duration) error {
	id := p.KunlunWarActivityID()
	if id == 0 {
		return errKuClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarLeaveReq{ActivityId: &id})
	_, err := p.kuRoundTrip(KUNLUN_LEAVE_CMD, SERVER_KUNLUN_LEAVE, body, timeout)
	return err
}

// KunlunStatus is the /kunlun_info + web summary; read-only, lock-safe.
func (p *Player) KunlunStatus(now time.Time) map[string]any {
	st := p.kuState()
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	out := map[string]any{"activityId": st.ActivityID, "phase": st.kuPhase(now)}
	if st.Enter != nil {
		out["rank"] = st.Enter.GetSelfRank()
		out["floor"] = st.Enter.GetFloor()
		out["group"] = st.Enter.GetGroup()
		out["unionId"] = st.Enter.GetUnionId()
		out["round"] = st.Enter.GetRound()
	}
	if st.Game != nil {
		out["home"] = st.Game.GetHome()
		out["energy"] = st.Game.GetEnergy()
		out["cities"] = len(st.Game.GetUnionInfo()) // union rows, not city count
		out["selfPos"] = st.SelfPoint
	}
	if st.Mini != nil {
		held := 0
		for _, c := range st.Mini.GetCityInfo() {
			if st.Enter != nil && c.GetUnionId() == st.Enter.GetUnionId() {
				held++
			}
		}
		out["citiesHeld"] = held
		out["cells"] = len(st.CellOwner)
		out["miniAt"] = st.MiniAt.In(ictZone).Format("15:04:05")
	}
	return out
}
