package tytd_go

// Đỉnh Bắc Minh ("北冥之巅", activity TYPE 332) — the NORTH map on the SAME
// client bundle and transport as Côn Lôn (web module remoteKunlunWar), but a
// DIFFERENT event: own activity id, own 119×119 map (mapNewOut.json), own
// config params. Every scene opcode is the kunlun 219xxx family verbatim —
// the server routes by the activityId inside each body — so the transport is
// a mirror of player_kunlunwar.go on a separate ledger (p.bm/BmMu/bmChans).
//
// TRANSPORT INVARIANT: answers come back on the identical SERVER_KUNLUN_*
// hexes, so one socket can only ever sit in ONE war scene; the wsHandler
// routes kunlun-family answers by p.warKind (player.go). In practice Côn Lôn
// runs weekends and Bắc Minh midweek, but the seat guard below is enforced
// regardless.
//
// ITEM BAN: Bắc Minh has NO item exceptions — no fruit eat, no item revive.
// Free-only tree ops (219910 read / 219911 claim / 219912 welfare-collect)
// are mirrored by the event account; ⚡tăng tốc (219918) is press-through per
// the user ("cứ bấm thôi khỏi cần hỏi") — the SERVER decides item-vs-gems.

import (
	"errors"
	"fmt"
	"log"
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
	activityTypeBacMinh int32 = 332

	warKindKunlun  = "ku"
	warKindBacMinh = "bm"
)

var (
	errBmBusy    = fmt.Errorf("một lệnh bắc minh đang chờ")
	errBmTimeout = errors.New("server không trả lời Đỉnh Bắc Minh, thử lại sau")
	errBmGone    = errKuGone
	errBmClosed  = errors.New("sự kiện Đỉnh Bắc Minh chưa mở")
)

// bacminhState is the per-account Đỉnh Bắc Minh ledger (guarded by BmMu).
// Mirrors kunlunState field for field — see player_kunlunwar.go for the
// meaning of each; the board keys are posIndex = y*119+x on the north map.
type bacminhState struct {
	ActivityID int32
	BeginTime  int64 // ms, from the activity-list entry
	EndTime    int64

	Cfg        *proto.KunlunWarConfigSync // activity-detail uniqueConfig (may be nil)
	OccupyCost int32

	// Accel cost model (user-confirmed): press 219918 directly — the server
	// spends one NORTHPOLEWAR_ACCEL_ITEM if the bag has it, else the
	// KUNLUNWAR_ACCELERATE_COST gem entry indexed by speedUpCount. Folded
	// here purely so the HUD can label the button (🎁 vs 💎).
	AccelItemID int32   // NORTHPOLEWAR_ACCEL_ITEM (0 = absent ⇒ gem path)
	AccelGemID  int32   // e.g. 100000 (ngọc)
	AccelCosts  []int32 // gem counts per press, index = speedUpCount
	AccelLimit  int32   // KUNLUNWAR_ACCELATE_LIMIT (0 = unseen)

	Enter  *proto.KunlunWarEnterActivityResp
	Game   *proto.KunlunWarEnterGameResp
	Mini   *proto.KunlunWarMiniMapInfoRespMsg
	MiniAt time.Time

	CellOwner map[int32]*proto.KunlunWarMiniCellInfo
	CityOwner map[int32]*proto.KunlunWarMiniCityInfo
	Unions    map[int64]*proto.KunlunUnionBaseData

	Group  *proto.KunlunWarGroupInfoResp
	LastAt map[string]time.Time

	SelfPoint int32
	Routes    map[int64]kuRouteRec
	MyUnion   int64
	MiniRaw   []byte

	wantAt    time.Time
	sceneLoop bool
}

func newBacminhState() *bacminhState {
	return &bacminhState{
		CellOwner: map[int32]*proto.KunlunWarMiniCellInfo{},
		CityOwner: map[int32]*proto.KunlunWarMiniCityInfo{},
		Unions:    map[int64]*proto.KunlunUnionBaseData{},
		LastAt:    map[string]time.Time{},
		Routes:    map[int64]kuRouteRec{},
	}
}

func (p *Player) bmState() *bacminhState {
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	if p.bm == nil {
		p.bm = newBacminhState()
	}
	return p.bm
}

// ---- war-scene mutex ---------------------------------------------------------

// setWarKind records which war scene the socket just entered (guarded KuMu —
// the reader checks it there). Callers must NOT hold BmMu... actually the bm
// path does hold BmMu and takes KuMu here; the kunlun path only ever takes
// KuMu, so the lock order is always BmMu→KuMu and never the reverse.
func (p *Player) setWarKind(kind string) {
	p.KuMu.Lock()
	p.warKind = kind
	p.KuMu.Unlock()
}

// kuSeatedInScene reports whether the socket currently holds a live Côn Lôn
// scene (the mutex guard on the north entry points).
func (p *Player) kuSeatedInScene() bool {
	p.KuMu.Lock()
	seated := p.ku != nil && p.ku.Game != nil
	p.KuMu.Unlock()
	return seated
}

// bmSeatedInScene is the mirror guard used by the kunlun entry points.
func (p *Player) bmSeatedInScene() bool {
	p.BmMu.Lock()
	seated := p.bm != nil && p.bm.Game != nil
	p.BmMu.Unlock()
	return seated
}

// ---- activity window -------------------------------------------------------

// BacMinhActivityID is the live instance id of an OPEN Đỉnh Bắc Minh window
// (0 = closed). Same ActList source of truth as KunlunWarActivityID.
func (p *Player) BacMinhActivityID() int32 {
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeBacMinh) {
		if e.status(now) == "active" {
			return e.ActivityID
		}
	}
	return 0
}

func (p *Player) bacminhWindow() (id int32, begin, end int64) {
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeBacMinh) {
		if e.status(now) == "active" {
			return e.ActivityID, e.BeginTime, e.EndTime
		}
	}
	return 0, 0, 0
}

// bacminhWarOpen is the type-332 activity-list window.
func bacminhWarOpen(p *Player) bool {
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeBacMinh) {
		if e.status(now) == "active" {
			return true
		}
	}
	return false
}

// bmPhase classifies now against the phase windows — the client reuses the
// kunlun phase machine for the north map (live 21003: uniqueConfig IS a
// KunlunWarConfigSync with 5 PeakHoldTimeConfigs).
func (st *bacminhState) bmPhase(now time.Time) string {
	if st.Cfg == nil || len(st.Cfg.GetPeakHoldTimeConfigs()) == 0 {
		// No phase table (21003 unseen) — fall back to the raw activity
		// window so the status line is never empty while open.
		if st.ActivityID != 0 && st.BeginTime != 0 {
			ms := now.UnixMilli()
			if ms < st.BeginTime {
				return "not_open"
			}
			if ms < st.EndTime {
				return "battle"
			}
			return "reward"
		}
		return "unknown"
	}
	ph := append([]*proto.KunlunPeakHoldTimeConfig(nil), st.Cfg.GetPeakHoldTimeConfigs()...)
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
			return "prepare"
		}
		if ms < be {
			switch c.GetType() {
			case 1:
				return "signup"
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

// bmPhaseWindow is bmPhase plus the flip instant of the current window.
func (st *bacminhState) bmPhaseWindow(now time.Time) (string, int64) {
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

// bmPhaseInfo is the /bacminh mini app countdown strip payload.
func (p *Player) bmPhaseInfo(now time.Time) *gimap.KuPhase {
	st := p.bmState()
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	st.ActivityID, st.BeginTime, st.EndTime = p.bacminhWindow()
	ph, end := st.bmPhaseWindow(now)
	n := kuPhaseVN(ph)
	if n == "" {
		return nil
	}
	return &gimap.KuPhase{S: ph, N: n, End: end}
}

// bmRoutesSnapshot renders folded north marches for the mini app frame
// (kuRoutesFrom is ledger-generic and reused as-is).
func (p *Player) bmRoutesSnapshot(now time.Time) []gimap.KuRoute {
	st := p.bmState()
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	return kuRoutesFrom(st.Routes, st.MyUnion, int64(p.UID), st.MiniRaw, st.Unions, now)
}

// ---- scene keep (march feed) -----------------------------------------------

// bmSceneKeep marks the north march feed as wanted; while fresh the loop sits
// the socket INSIDE the scene (219902 + 219917/15s) so 219943 broadcasts flow
// for EVERY player, same trick as kunlun. Refuses while a Côn Lôn scene holds
// the socket (transport invariant).
func (p *Player) bmSceneKeep(now time.Time) {
	if p.BacMinhActivityID() == 0 {
		return
	}
	st := p.bmState()
	p.BmMu.Lock()
	st.wantAt = now
	running := st.sceneLoop
	st.sceneLoop = true
	p.BmMu.Unlock()
	if !running {
		go p.bmSceneLoop()
	}
}

func (p *Player) bmSceneLoop() {
	t := time.NewTicker(kuHeartbeatEvery)
	defer t.Stop()
	first := make(chan time.Time, 1)
	first <- time.Now()
	for {
		select {
		case <-p.ctx.Done():
			p.bmSceneEnd()
			return
		case <-t.C:
		case <-first:
		}
		if p.BacMinhActivityID() == 0 {
			p.bmSceneEnd()
			return
		}
		st := p.bmState()
		p.BmMu.Lock()
		idle := time.Since(st.wantAt)
		in := st.Game != nil
		p.BmMu.Unlock()
		if idle > time.Minute {
			p.bmSceneLeave()
			return
		}
		if !in {
			if _, err := p.RequestBacMinhEnterGame(5 * time.Second); err != nil {
				log.Printf("[%s] [BacMinh] vào scene giữ feed: %v", p.Username, err)
				continue
			}
			debugf("[%s] [BacMinh] vào scene — feed hành quân trực tiếp bật", p.Username)
		}
		actID := p.BacMinhActivityID()
		body, _ := pb.Marshal(&proto.KunlunWarHeartBeatReqMsg{ActivityId: &actID})
		select {
		case p.writeChan <- pack.Pack(KUNLUN_HEART_BEAT_CMD, body, p.UID):
		case <-p.ctx.Done():
			p.bmSceneEnd()
			return
		default: // pipe full — next tick retries
		}
	}
}

func (p *Player) bmSceneEnd() {
	st := p.bmState()
	p.BmMu.Lock()
	st.sceneLoop = false
	p.BmMu.Unlock()
}

func (p *Player) bmSceneLeave() {
	st := p.bmState()
	p.BmMu.Lock()
	st.sceneLoop = false
	in := st.Game != nil
	p.BmMu.Unlock()
	if !in {
		return
	}
	if err := p.RequestBacMinhLeave(5 * time.Second); err != nil {
		log.Printf("[%s] [BacMinh] rời scene: %v", p.Username, err)
		return
	}
	p.BmMu.Lock()
	st.Game = nil
	p.BmMu.Unlock()
	debugf("[%s] [BacMinh] rời scene — tắt feed hành quân trực tiếp", p.Username)
}

// ---- seats (same bcChans pattern, own ledger) -------------------------------

func (p *Player) bmSeatBusy(msgType string) bool {
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	_, busy := p.bmChans[msgType]
	return busy
}

func (p *Player) bmClaim(msgType string) (chan []byte, error) {
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	if p.bmChans == nil {
		p.bmChans = map[string]chan []byte{}
	}
	if _, busy := p.bmChans[msgType]; busy {
		return nil, errBmBusy
	}
	ch := make(chan []byte, 1)
	p.bmChans[msgType] = ch
	return ch, nil
}

func (p *Player) bmRelease(msgType string, ch chan []byte) {
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	if cur, ok := p.bmChans[msgType]; ok && cur == ch {
		delete(p.bmChans, msgType)
	}
}

// bmDeliver hands one north answer to a waiting seat AND folds push deltas
// into the north cache. Reader-goroutine safe: never blocks.
func (p *Player) bmDeliver(msgType string, data []byte) {
	switch msgType {
	case SERVER_KUNLUN_BLOCK_SYNC, SERVER_KUNLUN_CITY_SYNC, SERVER_KUNLUN_MINI_MAP_INFO,
		SERVER_KUNLUN_CITY_BATTLE_SYNC, SERVER_KUNLUN_ATTACK_SYNC, SERVER_KUNLUN_GROUP_RANK_SYNC,
		SERVER_KUNLUN_MY_GAME_SYNC, SERVER_KUNLUN_LEFT_OUT_SYNC, SERVER_KUNLUN_ROUTE_SYNC,
		SERVER_KUNLUN_LINE_SYNC:
		p.bmFold(msgType, data)
	}
	p.BmMu.Lock()
	ch := p.bmChans[msgType]
	p.BmMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// bmFold is kuFold on the north ledger — byte-identical semantics, separate
// state (the two events' boards must never mix; see the transport invariant).
func (p *Player) bmFold(msgType string, data []byte) {
	st := p.bmState()
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
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
		// rank is display-only; names stay authoritative (same as ku).
		return
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
			debugf("[%s] [BacMinh] 219943: %d march(s) folded, ledger=%d", p.Username, n, len(st.Routes))
		}
		kuPruneRoutes(st.Routes)
	case SERVER_KUNLUN_LINE_SYNC:
		var rsp proto.KunlunWarGameLineInfoSyncMsg
		if pb.Unmarshal(data, &rsp) != nil {
			return
		}
		if st.Routes == nil {
			st.Routes = map[int64]kuRouteRec{}
		}
		// NOTE: kuFoldLineInfos resolves city ids through KunlunCityPoint
		// (kunlun mapOut table). North city ids (7xxx decos, 8xxx strongholds)
		// mostly aren't in that table and get dropped — the home-march synth
		// is a kunlun-side convenience; north marches arrive via 219943 anyway.
		if n := kuFoldLineInfos(st.Routes, rsp.GetLineInfo()); n > 0 {
			debugf("[%s] [BacMinh] 219944: %d home-march(es) folded, ledger=%d", p.Username, n, len(st.Routes))
		}
		kuPruneRoutes(st.Routes)
	}
}

// bmMergeConfig folds the type-332 21003 detail (params + phase config).
// Gate is ==332; the kunlun gate stays 199 — two different events.
func (p *Player) bmMergeConfig(data []byte) {
	var rsp proto.RspGetActivityDetail
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	dc := rsp.GetActivity().GetDetailConfig()
	if dc.GetCommonConfig().GetMainConfig().GetType() != activityTypeBacMinh {
		return
	}
	st := p.bmState()
	p.BmMu.Lock()
	for _, pc := range dc.GetCommonConfig().GetParamConfig() {
		switch pc.GetParam() {
		case "KUNLUNWAR_OCCUPY_AREA_COST":
			if v, err := strconv.Atoi(pc.GetValue()); err == nil && v > 0 {
				st.OccupyCost = int32(v)
			}
		case "KUNLUNWAR_ACCELERATE_COST":
			// "id=count|id=count|…" — engine splits on '|'.
			var costs []int32
			var gemID int32
			for _, part := range strings.Split(pc.GetValue(), "|") {
				idS, cntS, ok := strings.Cut(part, "=")
				if !ok {
					continue
				}
				id, e1 := strconv.Atoi(idS)
				cnt, e2 := strconv.Atoi(cntS)
				if e1 != nil || e2 != nil || cnt <= 0 {
					continue
				}
				costs = append(costs, int32(cnt))
				gemID = int32(id)
			}
			if len(costs) > 0 {
				st.AccelGemID, st.AccelCosts = gemID, costs
			}
		case "NORTHPOLEWAR_ACCEL_ITEM":
			if v, err := strconv.Atoi(pc.GetValue()); err == nil && v > 0 {
				st.AccelItemID = int32(v)
			}
		case "KUNLUNWAR_ACCELATE_LIMIT":
			if v, err := strconv.Atoi(pc.GetValue()); err == nil && v > 0 {
				st.AccelLimit = int32(v)
			}
		}
	}
	p.BmMu.Unlock()
	var sync proto.KunlunWarConfigSync
	if pb.Unmarshal(dc.GetUniqueConfig(), &sync) != nil || len(sync.GetPeakHoldTimeConfigs()) == 0 {
		return
	}
	p.BmMu.Lock()
	st.Cfg = &sync
	p.BmMu.Unlock()
	log.Printf("[%s] [BacMinh] config: %d phase windows, %d cities", p.Username,
		len(sync.GetPeakHoldTimeConfigs()), len(sync.GetCityConfig()))
}

func (p *Player) bmRoundTrip(cmd, wantType string, body []byte, timeout time.Duration) ([]byte, error) {
	ch, err := p.bmClaim(wantType)
	if err != nil {
		return nil, err
	}
	defer p.bmRelease(wantType, ch)
	// Claim the north side of the shared-hex routing BEFORE the write (the
	// reader routes SERVER_KUNLUN_* answers by warKind — flipping only after
	// success would send this request's own answer to the kunlun seats).
	p.setWarKind(warKindBacMinh)
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
	case <-p.ctx.Done():
		return nil, errBmGone
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, errBmTimeout
	case <-p.ctx.Done():
		return nil, errBmGone
	}
}

// ---- wire requests ---------------------------------------------------------

// RequestBacMinhEnterActivity loads roster/rank and refreshes the ledger
// window; also claims the war seat for the north reader routing.
func (p *Player) RequestBacMinhEnterActivity(timeout time.Duration) (*proto.KunlunWarEnterActivityResp, error) {
	id, begin, end := p.bacminhWindow()
	if id == 0 {
		return nil, errBmClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarEnterActivityReq{ActivityId: &id})
	raw, err := p.bmRoundTrip(KUNLUN_ENTER_ACTIVITY_CMD, SERVER_KUNLUN_ENTER_ACTIVITY, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarEnterActivityResp
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		return nil, err
	}
	st := p.bmState()
	p.BmMu.Lock()
	st.ActivityID, st.BeginTime, st.EndTime = id, begin, end
	st.Enter = &rsp
	p.BmMu.Unlock()
	return &rsp, nil
}

// RequestBacMinhEnterGame takes the north scene seat. Refused while a Côn Lôn
// scene holds the socket: both events answer on the same SERVER_KUNLUN_*
// hexes, a second war scene would cross-deliver answers (transport invariant).
func (p *Player) RequestBacMinhEnterGame(timeout time.Duration) (*proto.KunlunWarEnterGameResp, error) {
	id := p.BacMinhActivityID()
	if id == 0 {
		return nil, errBmClosed
	}
	if p.kuSeatedInScene() {
		return nil, fmt.Errorf("acc đang ngồi trong trận Côn Lôn — rời Côn Lôn trước đã")
	}
	body, _ := pb.Marshal(&proto.KunlunWarEnterGameReq{ActivityId: &id})
	raw, err := p.bmRoundTrip(KUNLUN_ENTER_GAME_CMD, SERVER_KUNLUN_ENTER_GAME, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarEnterGameResp
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		return nil, err
	}
	st := p.bmState()
	p.BmMu.Lock()
	st.Game = &rsp
	st.SelfPoint = rsp.GetMyPlayerInfo().GetPos()
	if u := rsp.GetMyPlayerInfo().GetUnionId(); u != 0 {
		st.MyUnion = u
	}
	p.BmMu.Unlock()
	return &rsp, nil
}

// BacMinhLeaderPoint is the /bacminh_follow anchor (same semantics as
// KunlunLeaderPoint; north grid is 119-wide — decomp uses gimap.BacMinhCols).
func (p *Player) BacMinhLeaderPoint(to time.Duration) (int32, error) {
	if p.BacMinhActivityID() == 0 {
		return 0, errBmClosed
	}
	st := p.bmState()
	p.BmMu.Lock()
	pt, fresh := st.SelfPoint, st.Game != nil
	p.BmMu.Unlock()
	if pt == 0 || !fresh {
		if _, err := p.RequestBacMinhEnterGame(to); err != nil {
			return 0, err
		}
		p.BmMu.Lock()
		pt = st.SelfPoint
		p.BmMu.Unlock()
	}
	if pt <= 0 {
		return 0, fmt.Errorf("quân của bang chủ đang chết/chờ hồi sinh ở Đỉnh Bắc Minh — hồi sinh xong hãy follow")
	}
	return pt, nil
}

// RequestBacMinhMiniMap pulls the WHOLE north map ownership snapshot —
// live-proven 2026-10-07: 86 KB, cities=81 cells=8821 unions=16, grid 119².
// The group-with-floor rule applies exactly as on kunlun.
func (p *Player) RequestBacMinhMiniMap(timeout time.Duration) ([]byte, error) {
	id := p.BacMinhActivityID()
	if id == 0 {
		return nil, errBmClosed
	}
	st := p.bmState()
	p.BmMu.Lock()
	floor, group := int32(0), int32(0)
	if st.Enter != nil {
		floor, group = st.Enter.GetFloor(), st.Enter.GetGroup()
	}
	p.BmMu.Unlock()
	body, _ := pb.Marshal(&proto.KunlunWarMiniMapInfoReqMsg{ActivityId: &id, Floor: &floor, Group: &group})
	if group == 0 {
		if _, err := p.RequestBacMinhEnterActivity(timeout); err == nil {
			p.BmMu.Lock()
			if st.Enter != nil {
				floor, group = st.Enter.GetFloor(), st.Enter.GetGroup()
			}
			p.BmMu.Unlock()
			body, _ = pb.Marshal(&proto.KunlunWarMiniMapInfoReqMsg{ActivityId: &id, Floor: &floor, Group: &group})
		}
	}
	return p.bmRoundTrip(KUNLUN_MINI_MAP_INFO_CMD, SERVER_KUNLUN_MINI_MAP_INFO, body, timeout)
}

// RequestBacMinhGameBlockInfo is the 219903 view-window pull for the north
// page (who stands where, city HP) — seat + floor/group rules as kunlun.
func (p *Player) RequestBacMinhGameBlockInfo(blocks []int32, timeout time.Duration) (*proto.KunlunWarGameBlockInfoResp, error) {
	if len(blocks) == 0 {
		return nil, fmt.Errorf("không có ô nào để hỏi")
	}
	id := p.BacMinhActivityID()
	if id == 0 {
		return nil, errBmClosed
	}
	st := p.bmState()
	p.BmMu.Lock()
	seated := st.Game != nil
	floor, group := int32(0), int32(0)
	if st.Enter != nil {
		floor, group = st.Enter.GetFloor(), st.Enter.GetGroup()
	}
	p.BmMu.Unlock()
	if !seated {
		return nil, fmt.Errorf("acc chưa đứng trong trận Đỉnh Bắc Minh")
	}
	if group == 0 {
		if _, err := p.RequestBacMinhEnterActivity(timeout); err == nil {
			p.BmMu.Lock()
			if st.Enter != nil {
				floor, group = st.Enter.GetFloor(), st.Enter.GetGroup()
			}
			p.BmMu.Unlock()
		}
	}
	body, _ := pb.Marshal(&proto.KunlunWarGameBlockInfoReq{
		ActivityId: &id, BlockVec: blocks, FloorId: &floor, GroupId: &group,
	})
	raw, err := p.bmRoundTrip(KUNLUN_GAME_BLOCK_INFO_CMD, SERVER_KUNLUN_GAME_BLOCK_INFO, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarGameBlockInfoResp
	if pb.Unmarshal(raw, &rsp) != nil {
		return nil, fmt.Errorf("trả lời Đỉnh Bắc Minh block-info không đọc được")
	}
	if rsp.GetRet() != 0 {
		return nil, fmt.Errorf("Đỉnh Bắc Minh block info ret=%d", rsp.GetRet())
	}
	p.BmMu.Lock()
	if st.Routes == nil {
		st.Routes = map[int64]kuRouteRec{}
	}
	if n := kuFoldRouteInfos(st.Routes, rsp.GetRouteInfo()); n > 0 {
		debugf("[%s] [BacMinh] 219903: %d march(s) folded, ledger=%d", p.Username, n, len(st.Routes))
	}
	p.BmMu.Unlock()
	return &rsp, nil
}

// RequestBacMinhGroupInfo lists one group's unions for name/flag lookup.
func (p *Player) RequestBacMinhGroupInfo(group, floor int32, timeout time.Duration) (*proto.KunlunWarGroupInfoResp, error) {
	id := p.BacMinhActivityID()
	if id == 0 {
		return nil, errBmClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarGroupInfoReq{ActivityId: &id, GroupId: &group, FloorId: &floor})
	raw, err := p.bmRoundTrip(KUNLUN_GROUP_INFO_CMD, SERVER_KUNLUN_GROUP_INFO, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarGroupInfoResp
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		return nil, err
	}
	st := p.bmState()
	p.BmMu.Lock()
	st.Group = &rsp
	for _, u := range rsp.GetGroupInfo() {
		st.Unions[u.GetUnionId()] = &proto.KunlunUnionBaseData{
			UnionId: u.UnionId, Flag: u.Flag, Name: u.UnionName, ServerId: u.ServerId,
		}
	}
	p.BmMu.Unlock()
	return &rsp, nil
}

// RequestBacMinhLeave exits the north scene (219958) and frees the war seat.
func (p *Player) RequestBacMinhLeave(timeout time.Duration) error {
	id := p.BacMinhActivityID()
	if id == 0 {
		return errBmClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarLeaveReq{ActivityId: &id})
	if _, err := p.bmRoundTrip(KUNLUN_LEAVE_CMD, SERVER_KUNLUN_LEAVE, body, timeout); err != nil {
		return err
	}
	p.setWarKind(warKindKunlun)
	return nil
}

// BacMinhStatus is the /bacminh_info summary; read-only, lock-safe.
func (p *Player) BacMinhStatus(now time.Time) map[string]any {
	st := p.bmState()
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	out := map[string]any{"activityId": st.ActivityID, "phase": st.bmPhase(now)}
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
