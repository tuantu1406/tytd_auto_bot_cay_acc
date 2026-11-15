package tytd_go

/*
WeYound MARCH (2026-10-18 rewrite) — the tick's movement brain, ported
method-for-method from the weyound bundle (startAutoExplore → doMoveAuto →
doBlockEventAuto / doEventCase → req*):

  * the board is the CDN base grid ∪ server view (player_weyound_map.go);
    the wire view alone is BLANK-only, walking it finds no fog (live-proved
    — this is why the first engine never moved),
  * getMovePath + shift/pop rule: walk up to the tile BEFORE the target
    (MOVE pos = waypoint); if the last path node is not Blank/Base the
    client pops it — the player never stands on Fog/Event; the op then
    fires at the ORIGINAL target remotely (EXPLORE/DEAL_EVENT pos = target),
  * event order follows the client's auto tick: Buff → Select → Monster →
    Light → Talk → Trap reveal, one re-walk of unDealCell per resolution,
  * Monster: never re-attack a monster your own attackInfo already
    touched (attackNum>0 → client l=null = no auto attack). The cell's
    event.objId is the monster msg id (live-confirmed 2026-11-13 — ev.id is
    a different space), so the rule finally binds.
  * MONSTER SWEEP (owner law 2026-11-13 "mở phải ô có quái thì không đánh =>
    đánh luôn"): pending-scan parity alone starves the shared board — live
    probe saw NINE live monsters on the floor while unDealCell was empty
    (union mates' digs never enter OUR pending list). wySweepMonsters runs at
    every march loop head and swings at EVERY live monster cell (walk +
    DEAL_EVENT{param1=1}) — free (no stamina; accepted at hp=0), exactly ONE
    swing per monster with the result ignored (server refuses every hit
    after the first with 18628 — owner "chỉ được đánh đúng 1 lần… không cần
    quan tâm kết quả"), then the march opens other cells; kills claimed
    immediately.
  * Select: param1 = Number(monsterParam) − 1 from the eventConfig row —
    the client's plain Number(), NO pipe split; unparseable → retire,
    never guess 0 (that is the CHEAPEST tier — opposite of the owner's
    tinhnang rule),
  * Light: DEAL_EVENT{pos=event tile, params null, selectView=[fog tile
    with the most fog neighbours — getFogPosSurroundedByFog]},
  * trapped (trapEndTime in the future, ms): DEAL_EVENT at the PLAYER pos
    once stamina ≥ Number(eventCfg[11007].param.split("|")[1]) — below
    that the client only shows the item popup; we WAIT (item ban),
  * fog dig blocked while hp < mapConfig.cost unless a FreeExplore buff
    (buffConfig type 1) is active — the server is still the final oracle,
  * nextIsUnlock sight rule (NextFloor in the grid, or bonus floor with
    the floor box claimed) → ENTER{enterNext:true} instead of more digs.
*/

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const wyTrapEventID = 11007 // RemoteWeYoundConfig.TRAP_EVENT_ID

// ─── tick state ──────────────────────────────────────────────────────────────

// wyState is the display board + player mirror for one tick pass. Server
// answers mutate it incrementally (refreshTileData semantics: a view cell
// replaces type/event/reachable/root on the existing tile).
type wyState struct {
	pd         *proto.WeYoundPlayerMsg
	px, py     int32
	grid       *wyGrid
	viewIdx    map[string]*proto.WeYoundCellMsg
	monsters   map[int64]*proto.WeYoundMonsterMsg
	mapID      int32
	floor      int32
	memberSize int32
}

func wyViewToCells(cs []*proto.WeYoundCellMsg) []*wyViewCell {
	out := make([]*wyViewCell, 0, len(cs))
	for _, c := range cs {
		if c.GetPos() == nil {
			continue
		}
		vc := &wyViewCell{x: c.GetPos().GetX(), y: c.GetPos().GetY(),
			typ: c.GetType(), reachable: c.GetReachable()}
		if e := c.GetEvent(); e != nil {
			vc.event = &wyEventLite{id: e.GetId(), objId: e.GetObjId(), configId: e.GetConfigId(), evType: e.GetEventType()}
		}
		if r := c.GetRoot(); r != nil {
			vc.root = &wyPosLite{r.GetX(), r.GetY()}
		}
		out = append(out, vc)
	}
	return out
}

// ingest applies a server view answer (reqExplore/reqDealEvent/
// doOpenArea/ENTER): merge into viewIdx, then patch the display grid.
func (st *wyState) ingest(cs []*proto.WeYoundCellMsg) {
	if st.viewIdx == nil {
		st.viewIdx = map[string]*proto.WeYoundCellMsg{}
	}
	for _, c := range cs {
		if c.GetPos() == nil {
			continue
		}
		k := cellKey(c.GetPos().GetX(), c.GetPos().GetY())
		st.viewIdx[k] = c
		if st.grid != nil {
			if cell := st.grid.at(c.GetPos().GetX(), c.GetPos().GetY()); cell != nil {
				cell.typ = c.GetType()
				cell.reachable = c.GetReachable()
				cell.event = nil
				if e := c.GetEvent(); e != nil {
					cell.event = &wyEventLite{id: e.GetId(), objId: e.GetObjId(), configId: e.GetConfigId(), evType: e.GetEventType()}
				}
				cell.root = nil
				if r := c.GetRoot(); r != nil {
					cell.root = &wyPosLite{r.GetX(), r.GetY()}
				}
			}
		}
	}
}

func (st *wyState) ingestMonsters(ms []*proto.WeYoundMonsterMsg) {
	if st.monsters == nil {
		st.monsters = map[int64]*proto.WeYoundMonsterMsg{}
	}
	for _, m := range ms {
		if m.GetId() != 0 {
			st.monsters[m.GetId()] = m
		}
	}
}

// syncPlayer swaps in the freshest player msg and re-reads the position.
func (st *wyState) syncPlayer(pd *proto.WeYoundPlayerMsg) {
	if pd == nil {
		return
	}
	st.pd = pd
	if pp := pd.GetPos(); pp != nil {
		st.px, st.py = pp.GetX(), pp.GetY()
	}
}

// ─── config-backed board build ───────────────────────────────────────────────

// wySyncBoard rebuilds the display grid from the CDN base json for the
// current floor's mapId (mapConfig-folded; mapCfgMap.get(floor), loop-map
// fallback beyond maxCfgFloor) + the accumulated server view. False = no
// usable layout (drift / unknown mapId) — the tick must NOT march.
func (p *Player) wySyncBoard(st *wyState) bool {
	p.WyMu.Lock()
	row, _ := p.wyMapCfgRowLocked(st.floor)
	p.WyMu.Unlock()
	if row == nil {
		log.Printf("[%s] [TCVU] thiếu cấu hình bản đồ tầng %d (mapConfig chưa fold?) — chưa cuốc được", p.Username, st.floor)
		return false
	}
	base := wyMapBase(int(row.GetMapId()), st.memberSize)
	if base == nil {
		log.Printf("[%s] [TCVU] tầng %d dùng mapId %d ngoài danh mục nhúng — không đoán layout, bỏ qua tầng",
			p.Username, st.floor, row.GetMapId())
		return false
	}
	st.mapID = row.GetMapId()
	view := make([]*wyViewCell, 0, len(st.viewIdx))
	for _, c := range st.viewIdx {
		view = append(view, wyViewToCells([]*proto.WeYoundCellMsg{c})[0])
	}
	st.grid = wyBuildGrid(base, view)
	return st.grid != nil
}

// wyMapCfgRowLocked mirrors model.getMapCfg: exact floor, else the loop
// table over the last 10 configured floors.
func (p *Player) wyMapCfgRowLocked(floor int32) (*proto.WeYoundMapConfigMsg, int32) {
	if len(p.wyMapCfg) == 0 {
		return nil, 0
	}
	maxFloor := int32(0)
	for f := range p.wyMapCfg {
		if f > maxFloor {
			maxFloor = f
		}
	}
	if row := p.wyMapCfg[floor]; row != nil {
		return row, maxFloor
	}
	if floor >= maxFloor-9 {
		if row := p.wyMapCfg[maxFloor-10+floor%10]; row != nil {
			return row, maxFloor
		}
	}
	return nil, maxFloor
}

// wyGridDiagOnce logs the composed grid once per (map,floor) so a live pass
// shows the fog histogram without spamming every tick.
func (p *Player) wyGridDiagOnce(st *wyState) bool {
	key := fmt.Sprintf("%d:f%d", st.mapID, st.floor)
	if p.wyDiagLogged == key {
		return false
	}
	p.wyDiagLogged = key
	return true
}

// ─── movement (client doMoveAuto shift/pop rule) ─────────────────────────────

// wyWalkTo walks the player toward (tx,ty) exactly like doMoveAuto: the
// MOVE pos is the waypoint BEFORE the target (never the target itself
// unless it is Blank/Base); the caller then fires the op at (tx,ty).
// False = no path (caller should treat the target as a dead end) or a
// transport death (errStGone semantics surface as false as well — the
// loop's next ENTER re-oracles everything).
func (p *Player) wyWalkTo(actID int32, st *wyState, tx, ty int32) bool {
	if st.grid == nil || st.grid.at(tx, ty) == nil {
		return false
	}
	path := st.grid.movePath(st.px, st.py, tx, ty)
	if len(path) < 2 {
		return false
	}
	path = path[1:] // client shift() — start tile is where we already are
	last := path[len(path)-1]
	// client pop(): the player may only end the walk on Blank/Base — the
	// target tile itself (Fog/Event/…) never becomes the MOVE destination.
	if last[0] == tx && last[1] == ty {
		if t := st.grid.resolveType(last[0], last[1]); t != wyTileBlank && t != wyTileBase {
			path = path[:len(path)-1]
		}
	}
	if len(path) == 0 {
		return true // already adjacent — op fires remotely from here
	}
	wp := path[len(path)-1]
	if wp[0] == st.px && wp[1] == st.py {
		return true
	}
	ret, err := p.wyMove(actID, wp[0], wp[1])
	if err != nil {
		return false
	}
	if ret != 0 {
		log.Printf("[%s] [TCVU] move (%d,%d) ret %d — bước hụt, thử lại nhịp sau", p.Username, wp[0], wp[1], ret)
		return false
	}
	st.px, st.py = wp[0], wp[1]
	return true
}

// ─── pending events (client doEventCase, auto branch) ────────────────────────

// wyResolvePending walks unDealCell in the client's auto order and resolves
// every event it can, syncing the board from each answer.
func (p *Player) wyResolvePending(actID int32, st *wyState) {
	actKey := fmt.Sprintf("%d:f%d", actID, st.floor)
	order := []int32{wyEvBuff, wyEvSelect, wyEvMonster, wyEvLight, wyEvTalk, wyEvTrap}
	resolved := 0
	for resolved < wyEventBudget {
		found := false
		for _, want := range order {
			cell, ev := p.wyPickPending(st, want, actKey)
			if cell == nil {
				continue
			}
			found = true
			resolved++
			cx, cy := cell.GetPos().GetX(), cell.GetPos().GetY()
			if !p.wyResolveEventLite(actID, st, ev, cx, cy, actKey) {
				p.wyMarkStuck(actKey, cx, cy)
			}
			break // re-scan from the top with the refreshed board
		}
		if !found {
			return
		}
	}
}

// wyPickPending returns the first non-retired unDealCell whose resolved
// event on the grid has type want (Box retires silently — rewards already
// rode the EXPLORE answer; monster already attacked by us retires too).
func (p *Player) wyPickPending(st *wyState, want int32, actKey string) (*proto.WeYoundCellMsg, *wyEventLite) {
	for _, c := range st.pd.GetUnDealCell() {
		if c.GetPos() == nil || c.GetEvent() == nil {
			continue
		}
		cx, cy := c.GetPos().GetX(), c.GetPos().GetY()
		ev := st.grid.resolveEvent(cx, cy)
		if ev == nil {
			ev = &wyEventLite{id: c.GetEvent().GetId(), objId: c.GetEvent().GetObjId(),
				configId: c.GetEvent().GetConfigId(), evType: c.GetEvent().GetEventType()}
		}
		if ev.evType != want {
			continue
		}
		if p.wyIsStuck(actKey, cx, cy) {
			continue
		}
		if want == wyEvMonster {
			// client doEventCase: own attackNum>0 → l=null → no auto attack.
			// The scene monsters are keyed by the MONSTER msg id, which is NOT
			// the cell's ev.id (probe 2026-11-13: ev.id 9717 vs monster 104189)
			// — resolve through objId/configId, never the raw ev.id.
			if wyMyAttack(wyMonsterForEvent(st, ev), p.UID) {
				p.wyMarkStuck(actKey, cx, cy)
				continue
			}
		}
		return c, ev
	}
	return nil, nil
}

// wyResolveEventLite performs one event op with the client's exact body.
// false = the op was REFUSED → the caller retires the cell.
func (p *Player) wyResolveEventLite(actID int32, st *wyState, ev *wyEventLite, cx, cy int32, actKey string) bool {
	switch ev.evType {
	case wyEvMonster:
		if !p.wyWalkTo(actID, st, cx, cy) {
			return false
		}
		rsp, err := p.wyDealEventLite(actID, cx, cy, pb.Int32(1), nil, nil)
		if err != nil || rsp.GetRet() != 0 {
			return false
		}
		// Ledger parity with wySweepMonsters: one swing per monster cell,
		// whichever path landed it.
		p.wyMarkAttacked(actKey, fmt.Sprintf("m%d:e%d", ev.objId, ev.id))
		st.ingest(rsp.GetView())
		st.ingestMonsters(rsp.GetMonster())
		st.syncPlayer(rsp.GetPlayer())
		for _, m := range rsp.GetMonster() {
			if m.GetId() == 0 {
				continue
			}
			if parseBlood(m.GetRemainBloods()) <= 0 || m.GetDefeat() {
				if r, err := p.wyGetMonsterReward(actID, m.GetId()); err == nil {
					log.Printf("[%s] [TCVU] quái %d ret %d", p.Username, m.GetId(), r.GetRet())
				}
			}
		}
		return true
	case wyEvSelect:
		if !p.wyWalkTo(actID, st, cx, cy) {
			return false
		}
		p.WyMu.Lock()
		cfg := p.wyEventCfg[ev.configId]
		p.WyMu.Unlock()
		if cfg == nil {
			return false // no row — refuse, never guess 0 (cheapest tier)
		}
		// client: Number(monsterParam) − 1 — plain Number(), NOT a pipe split.
		mp, err := strconv.ParseFloat(strings.TrimSpace(cfg.GetMonsterParam()), 64)
		if err != nil || mp < 1 {
			return false
		}
		rsp, err := p.wyDealEventLite(actID, cx, cy, pb.Int32(int32(mp)-1), nil, nil)
		if err != nil || rsp.GetRet() != 0 {
			return false
		}
		st.ingest(rsp.GetView())
		st.syncPlayer(rsp.GetPlayer())
		return true
	case wyEvBuff, wyEvTalk:
		if !p.wyWalkTo(actID, st, cx, cy) {
			return false
		}
		rsp, err := p.wyDealEventLite(actID, cx, cy, nil, nil, nil)
		if err != nil || rsp.GetRet() != 0 {
			return false
		}
		st.ingest(rsp.GetView())
		st.syncPlayer(rsp.GetPlayer())
		return true
	case wyEvLight:
		// Auto pick (autoSelectLightArea): the fog tile most surrounded by
		// fog, then DEAL_EVENT{pos=event tile, null params, selectView:[it]}.
		lx, ly, ok := st.grid.fogPosSurrounded(st.px, st.py)
		if !ok {
			return false
		}
		if !p.wyWalkTo(actID, st, cx, cy) {
			return false
		}
		sel := []*proto.WeYoundPosMsg{{X: pb.Int32(lx), Y: pb.Int32(ly)}}
		rsp, err := p.wyDealEventLite(actID, cx, cy, nil, nil, sel)
		if err != nil || rsp.GetRet() != 0 {
			return false
		}
		st.ingest(rsp.GetView())
		st.syncPlayer(rsp.GetPlayer())
		return true
	case wyEvTrap:
		// Free reveal — TRAP_SEEK, no items.
		ret, err := p.wyTrapSeek(actID)
		return err == nil && ret == 0
	default:
		return true // Box etc. — nothing to do
	}
}

// ─── monster sweep (owner law 2026-11-13 "đánh luôn") ────────────────────────

const wyAttackBudget = 10 // monster hits per sweep pass — sanity cap

// wyMonsterForEvent ties one grid event cell back to its scene monster
// record. The cell's ev.id is the EVENT instance id — a different space from
// the monster msg Id (probe 2026-11-13: cell ev.id 9717 vs monster id
// 104189, same board). objId is the primary key (unconfirmed live — kept
// first so a positive hit wins), the configId↔eventConfigId match is the
// fallback. nil = unknown; callers must treat unknown as "still attackable"
// (đánh luôn — never let a mapping gap skip a monster).
func wyMonsterForEvent(st *wyState, ev *wyEventLite) *proto.WeYoundMonsterMsg {
	if ev == nil {
		return nil
	}
	if ev.objId != 0 {
		if m := st.monsters[ev.objId]; m != nil {
			return m
		}
	}
	for _, m := range st.monsters {
		if ev.configId != 0 && (m.GetEventConfigId() == ev.configId || m.GetConfigId() == ev.configId) {
			return m
		}
	}
	return nil
}

// wyMyAttack — client doEventCase rule: our own attackInfo entry with
// attackNum>0 means we already swung at this monster this round.
func wyMyAttack(m *proto.WeYoundMonsterMsg, uid uint64) bool {
	for _, ai := range m.GetAttackInfo() {
		if ai.GetPlayerId() == int64(uid) && ai.GetAttackNum() > 0 {
			return true
		}
	}
	return false
}

// wySweepMonsters — EVERY live monster cell on the shared floor takes one
// hit, regardless of our own unDealCell. Live-proved 2026-11-13: the pending
// scan alone starves the board (union mates' monsters never enter OUR
// unDealCell; unDealCell was empty with a live monster on the floor), while
// DEAL_EVENT{pos,param1=1} is accepted at hp=0, for another member's monster,
// outside the pending list (ret=0, 11097 bloods → defeat, reward path opens).
// Attacks cost no stamina. ONE swing per monster, result ignored — owner
// law 2026-11-13 "chỉ được đánh quái đúng 1 lần, nên engine chỉ cần đánh
// xong đi mở ô khác, không cần quan tâm kết quả" (the server answers every
// hit after the first with 18628). The session ledger commits the swing
// whatever the ret; our attackInfo entry is the second lock when the
// cell↔monster mapping is known. Dead cells are skipped — the free claim
// lives in wyTick / the success path below. False = transport death.
func (p *Player) wySweepMonsters(actID int32, st *wyState) bool {
	actKey := fmt.Sprintf("%d:f%d", actID, st.floor)
	type tgt struct {
		x, y int32
		ev   *wyEventLite
	}
	var targets []tgt
	seen := map[*wyEventLite]bool{} // multi-cell events share the root's pointer
	for k := range st.grid.cells {
		x, y := k%st.grid.w, k/st.grid.w
		ev := st.grid.resolveEvent(x, y)
		if ev == nil || ev.evType != wyEvMonster || seen[ev] {
			continue
		}
		seen[ev] = true
		targets = append(targets, tgt{x, y, ev})
	}
	hit := 0
	for _, tg := range targets {
		if hit >= wyAttackBudget {
			break
		}
		m := wyMonsterForEvent(st, tg.ev)
		if m != nil && (m.GetDefeat() || parseBlood(m.GetRemainBloods()) <= 0) {
			continue // corpse — wyTick's free claim pass collects the reward
		}
		lkey := fmt.Sprintf("m%d:e%d", tg.ev.objId, tg.ev.id)
		if p.wyWasAttacked(actKey, lkey) {
			continue
		}
		if wyMyAttack(m, p.UID) { // client rule: one swing per monster per round
			p.wyMarkAttacked(actKey, lkey)
			continue
		}
		if !p.wyWalkTo(actID, st, tg.x, tg.y) {
			continue // no route yet (fog walls) — retry next tick, never retire
		}
		// ONE swing per monster, period (owner 2026-11-13 "chỉ được đánh
		// đúng 1 lần, đánh xong đi mở ô khác, không cần quan tâm kết quả"):
		// the ledger commits whether the server answers ret 0 or 18628 — no
		// cell is ever re-swung this session.
		p.wyMarkAttacked(actKey, lkey)
		rsp, err := p.wyDealEventLite(actID, tg.x, tg.y, pb.Int32(1), nil, nil)
		if err != nil {
			return false
		}
		hit++
		if rsp.GetRet() != 0 {
			log.Printf("[%s] [TCVU] đánh quái (%d,%d) ret %d (một phát là xong)",
				p.Username, tg.x, tg.y, rsp.GetRet())
			continue
		}
		st.ingest(rsp.GetView())
		st.ingestMonsters(rsp.GetMonster())
		st.syncPlayer(rsp.GetPlayer())
		for _, mm := range rsp.GetMonster() {
			if mm.GetId() == 0 {
				continue
			}
			if parseBlood(mm.GetRemainBloods()) <= 0 || mm.GetDefeat() {
				if r, err := p.wyGetMonsterReward(actID, mm.GetId()); err == nil && r.GetRet() == 0 {
					log.Printf("[%s] [TCVU] hạ quái %d ở (%d,%d) — thưởng: %s",
						p.Username, mm.GetId(), tg.x, tg.y, strings.TrimSpace(r.GetRewards()))
				}
			}
		}
	}
	return true
}

// ─── trapped ─────────────────────────────────────────────────────────────────

// wyBreakTrap is the doBreakTrap port: DEAL_EVENT at the PLAYER position
// with null params, but only once stamina ≥ the config threshold — below
// it the client only shows the item popup, and we never buy items
// [[tytd-item-ban]]: waiting is the move.
func (p *Player) wyBreakTrap(actID int32, st *wyState, need int32) bool {
	ret, err := p.wyDealEventLite(actID, st.px, st.py, nil, nil, nil)
	_ = need
	if err != nil || ret.GetRet() != 0 {
		return false
	}
	if st.pd != nil {
		st.pd.TrapEndTime = pb.Int64(0)
	}
	st.syncPlayer(ret.GetPlayer())
	return true
}

// wyTrapNeed — Number(eventCfg[TRAP_EVENT_ID].param.split("|")[1]).
func (p *Player) wyTrapNeed() (int32, bool) {
	p.WyMu.Lock()
	cfg := p.wyEventCfg[wyTrapEventID]
	p.WyMu.Unlock()
	if cfg == nil {
		return 0, false
	}
	parts := strings.Split(cfg.GetParam(), "|")
	if len(parts) < 2 {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, false
	}
	return int32(f), true
}

// ─── the fog march (client startAutoExplore / doBlockEventAuto) ──────────────

func (p *Player) wyFogMarch(actID int32, st *wyState) bool {
	actKey := fmt.Sprintf("%d:f%d", actID, st.floor)
	deadFog := map[string]bool{} // this tick only — re-oracled next poll
	explores, advances := 0, 0
	for explores < wyStepBudget {
		if st.pd == nil {
			return true
		}
		// Trapped first — nothing else moves (client isTrap guards).
		if st.pd.GetTrapEndTime() > 0 && st.pd.GetTrapEndTime() > time.Now().UnixMilli() {
			need, ok := p.wyTrapNeed()
			if !ok {
				log.Printf("[%s] [TCVU] đang kẹt bẫy nhưng thiếu cấu hình bẫy %d — chờ", p.Username, wyTrapEventID)
				return true
			}
			if st.pd.GetHp() < need {
				log.Printf("[%s] [TCVU] kẹt bẫy, thể lực %d < %d — chờ hồi (không cắn vật phẩm)", p.Username, st.pd.GetHp(), need)
				return true
			}
			if !p.wyBreakTrap(actID, st, need) {
				return true
			}
			continue
		}

		// Owner law 2026-11-13 "đánh luôn": every live monster on the board
		// takes a swing BEFORE further digs or the floor advance — free, no
		// stamina, server-arbitrated (see wySweepMonsters).
		if !p.wySweepMonsters(actID, st) {
			return false
		}

		// nextIsUnlock sight rule (client formula).
		isBonus := st.mapID == 0
		canNext := st.grid.hasNextFloorTile() || (isBonus && floorClaimed(st.pd, st.floor))
		if canNext {
			if advances >= 3 {
				log.Printf("[%s] [TCVU] lên tầng liên tiếp %d lần bị chặn — để nhịp sau", p.Username, advances)
				return true
			}
			advances++
			if !p.wyAdvanceFloor(actID, st) {
				return true
			}
			continue
		}

		fx, fy, ok := st.grid.nearestCanExplore(st.px, st.py, func(x, y int32) bool {
			return deadFog[cellKey(x, y)] || p.wyIsStuck(actKey, x, y)
		})
		if !ok {
			return true // nothing diggable — the server says the floor is done
		}
		// checkHpEnough: a FOG dig costs mapConfig.cost stamina unless a
		// FreeExplore buff (buffConfig type 1) covers it. The server stays
		// the oracle — this only avoids a certain refuse.
		p.WyMu.Lock()
		row, _ := p.wyMapCfgRowLocked(st.floor)
		freeExplore := p.wyFreeExploreLocked(st.pd)
		p.WyMu.Unlock()
		cost := int32(0)
		if row != nil {
			cost = row.GetCost()
		}
		if st.pd.GetHp() < cost && freeExplore <= 0 {
			log.Printf("[%s] [TCVU] thể lực %d < giá cuốc %d — dừng, chờ hồi (ENERGY_COLDTIME)", p.Username, st.pd.GetHp(), cost)
			return true
		}

		if !p.wyWalkTo(actID, st, fx, fy) {
			deadFog[cellKey(fx, fy)] = true // client Text_184: no route — next tile
			continue
		}
		ex, err := p.wyExplore(actID, fx, fy)
		explores++
		if err != nil {
			return false
		}
		switch {
		case ex.GetRet() == wyRetCloudPass:
			if !p.wyAdvanceFloor(actID, st) {
				return true
			}
			continue
		case ex.GetRet() != 0:
			// Stamina exhausted / refused — end the march, the next poll
			// (or ENERGY_COLDTIME recovery) resumes. NEVER burn the
			// stamina item [[tytd-item-ban]].
			log.Printf("[%s] [TCVU] explore (%d,%d) ret %d — dừng cuốc, chờ nhịp sau", p.Username, fx, fy, ex.GetRet())
			p.wyMarkStuck(actKey, fx, fy)
			return true
		}
		st.ingest(ex.GetView())
		st.ingestMonsters(extractSceneMonster(ex))
		st.syncPlayer(ex.GetPlayer())
		if r := strings.TrimSpace(ex.GetRewards()); r != "" {
			log.Printf("[%s] [TCVU] (%d,%d): %s", p.Username, fx, fy, r)
		}
		// Fresh events from this dig may block further fog (client order).
		p.wyResolvePending(actID, st)
	}
	return true
}

func extractSceneMonster(ex *proto.WeYoundExploreResp) []*proto.WeYoundMonsterMsg {
	if ex.GetMonster() == nil {
		return nil
	}
	return []*proto.WeYoundMonsterMsg{ex.GetMonster()}
}

// wyAdvanceFloor is reqNextFloor→doNextFloor→reqEnterMap(enterNext=true):
// the cloud-pass panel is a human gate; the bot just sends ENTER{next}.
func (p *Player) wyAdvanceFloor(actID int32, st *wyState) bool {
	en, err := p.wyEnter(actID, true)
	if err != nil {
		return false
	}
	if en.GetRet() != 0 || en.GetScene() == nil {
		log.Printf("[%s] [TCVU] lên tầng bị từ chối ret %d", p.Username, en.GetRet())
		return false
	}
	sc := en.GetScene()
	st.memberSize = en.GetMemberSize()
	st.floor = sc.GetFloor()
	st.ingest(sc.GetView())
	st.ingestMonsters(sc.GetMonster())
	if pd := sc.GetPlayer(); pd != nil {
		st.syncPlayer(pd)
	}
	if !p.wySyncBoard(st) {
		return false
	}
	log.Printf("[%s] [TCVU] lên tầng %d (thể lực %d)", p.Username, st.floor, st.pd.GetHp())
	return true
}

// ─── small re-implementations (old board helpers retired with the march) ─────

func parseBlood(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func floorClaimed(pd *proto.WeYoundPlayerMsg, floor int32) bool {
	for _, f := range pd.GetRewardFloors() {
		if f == floor {
			return true
		}
	}
	return false
}

func cellKey(x, y int32) string { return fmt.Sprintf("%d,%d", x, y) }

func (p *Player) wyMarkStuck(actKey string, x, y int32) {
	if p.wyStuck == nil {
		p.wyStuck = map[string]bool{}
	}
	p.wyStuck[actKey+"|"+cellKey(x, y)] = true
}

func (p *Player) wyIsStuck(actKey string, x, y int32) bool {
	return p.wyStuck != nil && p.wyStuck[actKey+"|"+cellKey(x, y)]
}

// wyMarkAttacked / wyWasAttacked — the sweep's swing ledger (one DEAL_EVENT
// attack per monster event cell per session/round, client-rule parity).
func (p *Player) wyMarkAttacked(actKey, lkey string) {
	if p.wyAttked == nil {
		p.wyAttked = map[string]bool{}
	}
	p.wyAttked[actKey+"|"+lkey] = true
}

func (p *Player) wyWasAttacked(actKey, lkey string) bool {
	return p.wyAttked != nil && p.wyAttked[actKey+"|"+lkey]
}

// wyFreeExploreLocked counts FreeExplore buff stacks (buffConfig type 1).
func (p *Player) wyFreeExploreLocked(pd *proto.WeYoundPlayerMsg) int32 {
	if pd == nil || len(p.wyBuffType) == 0 {
		return 0
	}
	var n int32
	for _, b := range pd.GetBuff() {
		if p.wyBuffType[b.GetBuffId()] == 1 {
			n += b.GetCount()
		}
	}
	return n
}
