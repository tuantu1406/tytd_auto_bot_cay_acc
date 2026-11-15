package tytd_go

// GOD ISLAND — people on the map (owner 2026-10-19: "hiển thị người trong
// thành, hiển thị người đang chạy giống như côn lôn"). The two kunlun
// counterparts are live-proven (probe gi_people_probe_live_test.go,
// activity 9062967): the standing roster rides the 206823 → 1aa7 view pull
// (cityInfo[].headInfo) and the march feed is the unsolicited 1aa6 push —
// plus two documented extras folded here: 1abe pushes full cityInfo rows
// whenever a city's roster changes, and 1ace flaps per-city fight flags
// (~19x/4min resident). This file owns the ledgers and the fold; the SSE
// frame attach lives in mapstream.go (giRoutesMerged), the people endpoint
// in web_giview.go. Names are NEVER fabricated: headInfo.name is empty in
// the standing roster, so a standing head borrows the name its playerId
// carried on a march row — otherwise it stays blank.

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Sentinels for the mapview twin's honest failure reporting (the page only
// sees the Vietnamese 502 text; these go to the debug log).
var (
	errGiNoAnswer = errors.New("island view: no answer")
)

func errGiRet(ret int32) error { return fmt.Errorf("island view: 1aa7 ret=%d", ret) }

// giCityVec is the client's FIXED home-block cityVec for the 206823 pull
// (STATIC_ANALYSIS.md:880, verified live 2026-10-19): asking the 40
// minimap cities at once returns ret=6833 — this exact 16-city union-
// colour-ordered vec is the only shape the server answers ret=0 with.
var giCityVec = []int32{1001, 1006, 1002, 1010, 1011, 1012, 1005, 1004, 1003, 1007, 1009, 1008, 1013, 1014, 1015, 1016}

// giRouteGrace keeps a march row a little past its server arrival time so
// the browser (which interpolates on its own clock) still sees the ribbon
// land; older rows are pruned at fold time.
const giRouteGrace = 3 * time.Minute

// giRouteRec is one player's newest march — the GI twin of kuRouteRec.
// Time/Arrival are server milliseconds (GodIslandGameRouteInfo time/arrival).
type giRouteRec struct {
	From, To int32
	Time     int64
	Arrival  int64
	Union    int64
	Name     string
}

// giHeadRec is one standing player (GodIslandPlayerHeadData, fields pinned
// from the live 1aa7 capture). Name is usually EMPTY — see file header.
type giHeadRec struct {
	PID  int64
	Ref  int64
	Name string
	Icon int32
}

// giCityRow is the last full standing row of one city (GodIslandGameCityInfo).
type giCityRow struct {
	Fight bool
	Union int64
	UName string
	HP    int32
	Cur   int32
	Dead  bool
	Heads []giHeadRec
	Seen  time.Time
}

// giNameRec is the persistent playerId→name cache (bug fix 2026-10-19
// "danh sách người trong thành phải resolve ra tên"): the standing roster
// carries no names, so a name seen ANYWHERE on the wire — 1aa6 march rows,
// 1ac0 battle lines (GodIslandGameLinePlayerInfo.name), 1abf win banners —
// is remembered FOREVER, outliving the march row it arrived on (Routes is
// pruned past arrival; without this, people who marched an hour ago came
// back as bare ids). Guarded by GodIslandMu, per account, shared across
// both islands (same pid = same person).
type giNameRec struct {
	Name  string
	Union int64
}

// giFoldName records one (pid, name[, union]) sighting; an empty name never
// overwrites a known one, a zero union never erases a known one. Caller
// holds GodIslandMu.
func (p *EventPlayer) giFoldName(pid int64, name string, union int64) {
	if pid == 0 || name == "" {
		return
	}
	if p.giNames == nil {
		p.giNames = map[int64]giNameRec{}
	}
	old := p.giNames[pid]
	if union == 0 {
		union = old.Union
	}
	p.giNames[pid] = giNameRec{Name: name, Union: union}
}

// giMergeNames gathers every name this chat's accounts have ever seen.
// NOT gated by variant state — names belong to people, not to a board.
func (t *TeleBot) giMergeNames(chatID int64) map[int64]giNameRec {
	out := map[int64]giNameRec{}
	t.session.Range(func(_, v any) bool {
		ep, ok := v.(*EventPlayer)
		if !ok || ep.ChatID != chatID || !ep.Running {
			return true
		}
		ep.GodIslandMu.Lock()
		for pid, r := range ep.giNames {
			if cur, ok := out[pid]; !ok || (cur.Name == "" && r.Name != "") {
				out[pid] = r
			}
		}
		ep.GodIslandMu.Unlock()
		return true
	})
	return out
}

// giFoldRouteInfos merges march rows into the slot ledger (caller holds
// GodIslandMu). Newest departure wins per playerId; a row arriving WITHOUT a
// name never wipes a name an earlier row carried (1aa6 rows do carry one —
// it is the roster's only name source). Expired rows are pruned here, the
// single place both the push and the view answer funnel through, mirroring
// kuFoldRouteInfos. Names ALSO land in the persistent giNames cache so the
// standing roster still resolves them after the row is pruned.
func (p *EventPlayer) giFoldRouteInfos(ev *giEvent, infos []*proto.GodIslandGameRouteInfo, now time.Time) {
	if ev.Routes == nil {
		ev.Routes = map[int64]giRouteRec{}
	}
	dead := now.Add(-giRouteGrace).UnixMilli()
	for pid, r := range ev.Routes {
		if r.Arrival < dead {
			delete(ev.Routes, pid)
		}
	}
	for _, r := range infos {
		pid := r.GetPlayerId()
		if pid == 0 || r.GetToCityId() == 0 {
			continue
		}
		old, ok := ev.Routes[pid]
		if ok && r.GetTime() < old.Time {
			continue // older departure cannot supersede a newer one
		}
		rec := giRouteRec{
			From: r.GetFromCityId(), To: r.GetToCityId(),
			Time: r.GetTime(), Arrival: r.GetArrival(),
			Union: r.GetUnionId(), Name: r.GetName(),
		}
		if rec.Name == "" {
			rec.Name = old.Name
		}
		ev.Routes[pid] = rec
		p.giFoldName(pid, rec.Name, rec.Union)
	}
}

// giFoldCityInfos replaces the cached standing rows of the pushed cities
// (caller holds GodIslandMu). Server rows are FULL (headInfo is the whole
// roster of that city), so last-write-wins is the honest merge.
func (p *EventPlayer) giFoldCityInfos(ev *giEvent, cities []*proto.GodIslandGameCityInfo, now time.Time) {
	if ev.Roster == nil {
		ev.Roster = map[int32]giCityRow{}
	}
	for _, c := range cities {
		if c.GetCityId() == 0 {
			continue
		}
		row := giCityRow{
			Fight: c.GetHaveFight(),
			Union: c.GetBaseData().GetUnionId(),
			UName: c.GetBaseData().GetUnionName(),
			HP:    c.GetHp(), Cur: c.GetCurHp(), Dead: c.GetDestroyed(),
			Seen: now,
		}
		for _, h := range c.GetHeadInfo() {
			if h.GetPlayerId() == 0 {
				continue
			}
			row.Heads = append(row.Heads, giHeadRec{
				PID: h.GetPlayerId(), Ref: h.GetRefId(),
				Name: h.GetName(), Icon: h.GetHeadIcon(),
			})
		}
		ev.Roster[c.GetCityId()] = row
	}
}

// giHandleRouteSync consumes the 1aa6 march push. It carries no activityId,
// so it attributes through the giClaim policy: the sole announced variant,
// dropped when both islands are open and the row cannot be told apart.
func (p *EventPlayer) giHandleRouteSync(data []byte) {
	var push proto.GodIslandGameRouteInfoSyncMsg
	if err := pb.Unmarshal(data, &push); err != nil {
		debugf("[%s] [GodIsland] bad 1aa6 march push: %v", p.Username, err)
		return
	}
	ev := p.giClaim(SERVER_GOD_ISLAND_ROUTE_SYNC)
	if ev == nil {
		debugf("[%s] [GodIsland] 1aa6 push not attributable (both islands open?) — dropped", p.Username)
		return
	}
	now := time.Now()
	p.GodIslandMu.Lock()
	p.giFoldRouteInfos(ev, push.GetRouteInfo(), now)
	p.GodIslandMu.Unlock()
}

// giHandleCityChange consumes the 1abe roster push (same no-activityId
// attribution as 1aa6).
func (p *EventPlayer) giHandleCityChange(data []byte) {
	var push proto.GodIslandGameCityChangeSyncMsg
	if err := pb.Unmarshal(data, &push); err != nil {
		debugf("[%s] [GodIsland] bad 1abe city push: %v", p.Username, err)
		return
	}
	ev := p.giClaim(SERVER_GOD_ISLAND_CITY_CHANGE)
	if ev == nil {
		return
	}
	now := time.Now()
	p.GodIslandMu.Lock()
	p.giFoldCityInfos(ev, push.GetCityInfo(), now)
	p.GodIslandMu.Unlock()
}

// giHandleCityBattle consumes the 1ace fight-flag push. isBattle=false
// deletes the entry, so the map never shows a fight the server cleared.
func (p *EventPlayer) giHandleCityBattle(data []byte) {
	var push proto.GodIslandGameCityBattleSyncMsg
	if err := pb.Unmarshal(data, &push); err != nil {
		debugf("[%s] [GodIsland] bad 1ace battle push: %v", p.Username, err)
		return
	}
	ev := p.giClaim(SERVER_GOD_ISLAND_CITY_BATTLE)
	if ev == nil {
		return
	}
	nowMs := time.Now().UnixMilli()
	p.GodIslandMu.Lock()
	if ev.FightSeen == nil {
		ev.FightSeen = map[int32]int64{}
	}
	for _, st := range push.GetCityStates() {
		if st.GetCityId() == 0 {
			continue
		}
		if st.GetIsBattle() {
			ev.FightSeen[st.GetCityId()] = nowMs
		} else {
			delete(ev.FightSeen, st.GetCityId())
		}
	}
	p.GodIslandMu.Unlock()
}

// giHandleLineSync consumes the 1ac0 battle-line push — folded here ONLY
// for names (lineInfo.playerInfo and both battle sides carry real names +
// unions). No ev is needed: names are not event state, so unlike 1aa6 this
// folds even when both islands are open.
func (p *EventPlayer) giHandleLineSync(data []byte) {
	var push proto.GodIslandGameLineInfoSyncMsg
	if err := pb.Unmarshal(data, &push); err != nil {
		debugf("[%s] [GodIsland] bad 1ac0 line push: %v", p.Username, err)
		return
	}
	p.GodIslandMu.Lock()
	for _, li := range push.GetLineInfo() {
		pi := li.GetPlayerInfo()
		pid := li.GetPlayerId()
		if pid == 0 {
			pid = pi.GetPlayerId()
		}
		if pid == 0 {
			pid = pi.GetHeadInfo().GetPlayerId()
		}
		p.giFoldName(pid, pi.GetName(), pi.GetUnion())
	}
	for _, b := range push.GetBattleInfo() {
		for _, bp := range []*proto.GodIslandBattlePlayer{b.GetAttacker(), b.GetBeAttacker()} {
			pi := bp.GetPlayerInfo()
			if pi == nil {
				continue
			}
			pid := pi.GetPlayerId()
			if pid == 0 {
				pid = pi.GetHeadInfo().GetPlayerId()
			}
			p.giFoldName(pid, pi.GetName(), pi.GetUnion())
		}
	}
	p.GodIslandMu.Unlock()
}

// giHandlePlayerWin consumes the 1abf victory banner — another named
// sighting, no attribution needed.
func (p *EventPlayer) giHandlePlayerWin(data []byte) {
	var push proto.GodIslandGamePlayerWinSyncMsg
	if err := pb.Unmarshal(data, &push); err != nil {
		debugf("[%s] [GodIsland] bad 1abf win push: %v", p.Username, err)
		return
	}
	p.GodIslandMu.Lock()
	for _, w := range push.GetInfo() {
		p.giFoldName(w.GetHeadInfo().GetPlayerId(), w.GetName(), 0)
	}
	p.GodIslandMu.Unlock()
}

// giHandleCityInfo consumes a 1aa7 view answer (via giRoute, so ev is the
// claimed requester). It ALWAYS folds (push-adjacent traffic is never
// wasted) and then feeds the mapview seat if one waits.
func (p *EventPlayer) giHandleCityInfo(ev *giEvent, data []byte) {
	var rsp proto.GodIslandGameCityInfoRespMsg
	if err := pb.Unmarshal(data, &rsp); err != nil {
		debugf("[%s] [GodIsland] bad 1aa7 city answer: %v", p.Username, err)
		return
	}
	if rsp.GetRet() != 0 {
		debugf("[%s] [GodIsland] 1aa7 ret=%d", p.Username, rsp.GetRet())
		return // seat waiter times out; the page keeps the old view
	}
	now := time.Now()
	p.GodIslandMu.Lock()
	p.giFoldRouteInfos(ev, rsp.GetRouteInfo(), now)
	p.giFoldCityInfos(ev, rsp.GetCityInfo(), now)
	ch := p.giCityPending
	p.GodIslandMu.Unlock()
	if ch != nil {
		select {
		case ch <- data:
		default:
		}
	}
}

// giVariantOpen reports whether one island variant is pullable on this
// account (announced, not kicked out).
func (p *EventPlayer) giVariantOpen(which int) bool {
	p.GodIslandMu.Lock()
	defer p.GodIslandMu.Unlock()
	if which < 0 || which >= giEventCount {
		return false
	}
	ev := p.GiEvents[which]
	return ev != nil && ev.ActivityID != 0 && !ev.LeftOut
}

// giEachVariant runs fn over this chat's event accounts whose variant
// `which` is open. which is per-VARIANT on purpose: the same account stands
// on both islands with the same playerId, so merging Bồng Lai ledgers with
// Thiên Ma rows would teleport marches between boards.
func (t *TeleBot) giEachVariant(chatID int64, which int, fn func(ep *EventPlayer, ev *giEvent)) {
	t.session.Range(func(_, v any) bool {
		ep, ok := v.(*EventPlayer)
		if !ok || ep.ChatID != chatID || !ep.Running {
			return true
		}
		ep.GodIslandMu.Lock()
		if which >= 0 && which < giEventCount {
			if ev := ep.GiEvents[which]; ev != nil && ev.ActivityID != 0 && !ev.LeftOut {
				fn(ep, ev)
			}
		}
		ep.GodIslandMu.Unlock()
		return true
	})
}

// giMergeRouteLedgers folds every account's march ledger for one variant
// into one pid→row map, newest departure winning — the kuMergeRouteLedgers
// rationale holds verbatim: any single socket only heard the 1aa6 pushes
// that flew while IT sat resident, so the merge surfaces a march the instant
// ANY of our sockets received it. Caller must not hold GodIslandMu.
func (t *TeleBot) giMergeRouteLedgers(chatID int64, which int) map[int64]giRouteRec {
	out := map[int64]giRouteRec{}
	t.giEachVariant(chatID, which, func(_ *EventPlayer, ev *giEvent) {
		for pid, r := range ev.Routes {
			if cur, ok := out[pid]; !ok || r.Time >= cur.Time {
				out[pid] = r
			}
		}
	})
	return out
}

// giOwnUnions lists the unions of the viewer's side on one variant: every
// event account's own union in that slot (the leader plays through event
// accounts here, so there is no regular-session union to add).
func (t *TeleBot) giOwnUnions(chatID int64, which int) map[int64]bool {
	out := map[int64]bool{}
	t.giEachVariant(chatID, which, func(_ *EventPlayer, ev *giEvent) {
		if ev.Union != 0 {
			out[ev.Union] = true
		}
	})
	return out
}

// giRoutesMerged builds the SSE frame rows from the merged ledgers: rows
// whose server arrival has passed are dropped (the ribbon already landed),
// and rows are marked own-side against the fleet's unions.
func (t *TeleBot) giRoutesMerged(chatID int64, which int, now time.Time) []gimap.GiRoute {
	recs := t.giMergeRouteLedgers(chatID, which)
	if len(recs) == 0 {
		return nil
	}
	own := t.giOwnUnions(chatID, which)
	nowMs := now.UnixMilli()
	var out []gimap.GiRoute
	for _, r := range recs {
		if r.Arrival <= nowMs || r.From == 0 || r.To == 0 {
			continue
		}
		out = append(out, gimap.GiRoute{
			FC: r.From, TC: r.To, T: r.Time, AR: r.Arrival,
			N: r.Name, O: own[r.Union],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AR < out[j].AR })
	return out
}

// giCityHPMerged answers the page's per-city HP bars (owner 2026-10-19
// "thiếu show hp của city, mới thấy show hp của thành chính"): the minimap
// proto carries NO hp fields, so bars ride the merged standing rows — every
// 1aa7 home-block answer and every 1abe city-change push ever folded. Rows
// with no HP figure (0) are skipped so the page never draws an empty bar;
// across accounts the row SEEN LAST wins (Seen — fold order is random, so
// recency must be explicit or two copies of a city trade values per tick).
func (t *TeleBot) giCityHPMerged(chatID int64, which int) map[string]gimap.GiCityHP {
	out := map[string]gimap.GiCityHP{}
	seen := map[string]time.Time{}
	t.giEachVariant(chatID, which, func(_ *EventPlayer, ev *giEvent) {
		for id, r := range ev.Roster {
			if r.HP <= 0 {
				continue
			}
			key := strconv.FormatInt(int64(id), 10)
			if old, ok := seen[key]; ok && !r.Seen.After(old) {
				continue
			}
			seen[key] = r.Seen
			out[key] = gimap.GiCityHP{HP: r.HP, Cur: r.Cur, Dead: r.Dead}
		}
	})
	if len(out) == 0 {
		return nil
	}
	return out
}
