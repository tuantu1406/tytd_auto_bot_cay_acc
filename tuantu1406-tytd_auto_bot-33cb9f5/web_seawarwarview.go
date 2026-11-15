package tytd_go

// Web layer for the Thương Minh Yêu Vực war map (/seawarwar): the SSE frame
// source and the ⚔️ attack order. Same BANG CHIẾN FIGHTING LAW as the Luân Hồi
// bracket (owner 2026-11-08, re-affirmed for SeaWar 2026-11-11 "vẽ map khiêu
// chiến chiến trường thương minh đi, bấm vào nhà chọn đánh là cả đám đánh,
// không cần check xem có lượt hay không"): the ONLY combatants are the
// leader's RUNNING event accounts, they attack ONLY on the player's map tap,
// and NO attempt/hour/item check gates the press — the server ret on 234938
// is the sole arbiter (the guild-war family stays ORDER-ONLY
// [[tytd-item-ban]]: the auto engine never sends 234931..234944).
//
// The board reads (234931 challenge list, 234936 enter, 234937 defenders)
// ride the leader's own session when resident — one union shares one board —
// otherwise the first resident event account with 349 open serves them.

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"
)

// swWarStale re-pulls 234931 when the cached board is older than this; the
// SSE cadence (~1 min tick, faster on explicit refresh) stays kind to the
// war socket.
const swWarStale = 20 * time.Second

// swWarBoardCache remembers the last challenge-list per leader chat so the
// SSE loop doesn't re-press 234931 on every heartbeat.
type swWarBoardCache struct {
	mu     sync.Mutex
	byChat map[int64]swWarBoard
}

type swWarBoard struct {
	at    time.Time
	actID int32
	list  *proto.SeaWarChallengeListResp
}

var swWarBoards = &swWarBoardCache{byChat: map[int64]swWarBoard{}}

// swWarSource is what the view needs from whichever session answers the
// board: both twins implement it.
type swWarSource interface {
	swActiveActivity() (int32, bool)
	swChallengeList(actID int32) (*proto.SeaWarChallengeListResp, error)
	swAttacksSnapshot() []swAttackRec
	swSelfName() string
	swHighPvpValue() int32
}

func (p *Player) swSelfName() string      { return p.Username }
func (p *EventPlayer) swSelfName() string { return p.Username }

// swHighPvpValue exposes the folded SEAWAR_HIGH_PVP_TIMES for the popup's
// lit/exhausted lệnh mirror (swLenhLit).
func (p *Player) swHighPvpValue() int32 {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	return p.swHighPvp
}
func (p *EventPlayer) swHighPvpValue() int32 {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	return p.swHighPvp
}

// swWarDonor picks the session that answers 234931: leader's own Player
// first, then member Players, then any running event account with 349 open
// (the union shares one board — any resident seat answers the same).
func (t *TeleBot) swWarDonor(chatID int64) swWarSource {
	if v, ok := t.session.Load(chatID); ok {
		if p, okP := v.(*Player); okP && p.Running {
			if _, active := p.swActiveActivity(); active {
				return p
			}
		}
	}
	if ws := t.swWarriors(chatID); len(ws) > 0 {
		return ws[0]
	}
	ms, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", chatID))
	if err != nil {
		return nil
	}
	for _, m := range ms {
		id, err := strconv.ParseInt(m.ID, 10, 64)
		if err != nil {
			continue
		}
		v, ok := t.session.Load(id)
		if !ok {
			continue
		}
		if p, okP := v.(*Player); okP && p.Running {
			if _, active := p.swActiveActivity(); active {
				return p
			}
		}
	}
	return nil
}

// seawarwarStreamPayload builds the which="seawarwar" frame.
func (t *TeleBot) seawarwarStreamPayload(chatID int64) (mapStreamResult, error) {
	src := t.swWarDonor(chatID)
	if src == nil {
		return mapStreamResult{idle: true}, nil
	}
	actID, active := src.swActiveActivity()
	if !active {
		return mapStreamResult{idle: true}, nil
	}
	swWarBoards.mu.Lock()
	b := swWarBoards.byChat[chatID]
	swWarBoards.mu.Unlock()
	if b.list == nil || b.actID != actID || time.Since(b.at) > swWarStale {
		if rsp, err := src.swChallengeList(actID); err == nil && rsp.GetRet() == 0 {
			b = swWarBoard{at: time.Now(), actID: actID, list: rsp}
			swWarBoards.mu.Lock()
			swWarBoards.byChat[chatID] = b
			swWarBoards.mu.Unlock()
		}
	}
	if b.list == nil || len(b.list.GetUnionMsg()) == 0 {
		return mapStreamResult{idle: true}, nil
	}
	var fights []gimap.LhFightIn
	for _, a := range src.swAttacksSnapshot() {
		fights = append(fights, gimap.LhFightIn{
			Name: a.Name, TargetID: a.TargetID, AtMilli: a.At.UnixMilli(),
		})
	}
	var selfUnion int64
	if v, ok := t.session.Load(chatID); ok {
		if p, okP := v.(*Player); okP {
			selfUnion = int64(p.ClanID)
		}
	}
	sm := gimap.SeaWarWarStream(b.list.GetUnionMsg(), b.list.GetCanBattleUnionIds(),
		selfUnion, time.Now().UnixMilli(), fights)
	sm.Sw.FreeNormal = b.list.GetFreeNormalTimes()
	return mapStreamResult{sm: sm}, nil
}

// swWarriors lists the leader's RUNNING event accounts with Thương Minh Yêu
// Vực open — the ONLY war combatants (bang chiến law; regular accounts never
// attack). Same discovery idiom as rbtWarriors.
func (t *TeleBot) swWarriors(chatID int64) []*EventPlayer {
	try := func(id int64) *EventPlayer {
		v, ok := t.session.Load(id)
		if !ok {
			return nil
		}
		ep, okE := v.(*EventPlayer)
		if !okE || !ep.Running {
			return nil
		}
		if _, active := ep.swActiveActivity(); !active {
			return nil
		}
		return ep
	}
	var out []*EventPlayer
	if ep := try(chatID); ep != nil {
		out = append(out, ep)
	}
	evs, err := model.GetEventUsersByLeader(t.ydb.GetDB(), strconv.FormatInt(chatID, 10))
	if err != nil {
		return out
	}
	for _, m := range evs {
		if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
			if ep := try(id); ep != nil {
				out = append(out, ep)
			}
		}
	}
	return out
}

// swOrderAttack is the ONE SeaWar war attack path: the player's house tap
// makes EVERY running event account enter the chosen union base and blind-
// fire one challenge per defender (EP.swWarOrderOne — no attempt check, no
// items, server ret sole arbiter). One honest line per account (async order
// fire law), errors inline.
func (t *TeleBot) swOrderAttack(chatID int64, unionID int64, baseName string) ([]string, error) {
	ws := t.swWarriors(chatID)
	if len(ws) == 0 {
		return nil, fmt.Errorf("không có acc sự kiện nào đang online trong Thương Minh Yêu Vực — acc thường không đánh chiến trường")
	}
	lines := make([]string, 0, len(ws))
	for _, ep := range ws {
		line, err := ep.swWarOrderOne(unionID)
		if err != nil {
			lines = append(lines, fmt.Sprintf("• %s: ❌ %s", ep.Username, err.Error()))
			continue
		}
		log.Printf("[%s] [TMYV] war order: đánh cứ điểm %d", ep.Username, unionID)
		lines = append(lines, fmt.Sprintf("• %s: %s", ep.Username, line))
	}
	return lines, nil
}

// swWarCityAnswer is the popup-detail side of /events/mapcity?which=seawarwar:
// who defends this base (234936 rooms + 234937 defenders) + recent hits.
func (t *TeleBot) swWarCityAnswer(src swWarSource, unionID int64) (map[string]any, error) {
	actID, active := src.swActiveActivity()
	if !active {
		return nil, fmt.Errorf("sự kiện chưa mở")
	}
	out := map[string]any{"union": unionID, "defenders": []map[string]any{}, "attacks": []map[string]any{}}
	var ent *proto.SeaWarEnterChallengeResp
	var err error
	switch s := src.(type) {
	case *Player:
		ent, err = s.swEnterChallenge(actID, unionID)
	case *EventPlayer:
		ent, err = s.swEnterChallenge(actID, unionID)
	}
	if err != nil {
		return nil, err
	}
	// A frowned ENTER is not a wall for the popup (blind-order law): show
	// what the board + live rows still reveal.
	defs := []map[string]any{}
	seen := map[int64]bool{}
	addDef := func(b *proto.SeaWarBeChallengedInfo) {
		pb := b.GetPlayerBaseDataMsg()
		if pb == nil || pb.GetPlayerId() == 0 || seen[pb.GetPlayerId()] {
			return
		}
		seen[pb.GetPlayerId()] = true
		defs = append(defs, map[string]any{
			"name": pb.GetNickName(), "room": b.GetRoomId(), "rank": b.GetRank(),
		})
	}
	// LỆNH table (owner 2026-11-11 "list ra có những lệnh nào còn lượt rồi
	// chơi theo lệnh đó"): challengeTimes COUNTS ATTACKS MADE on the row —
	// the client dims it once isChallenge fires (thường: times>0, cao cấp:
	// times ≥ SEAWAR_HIGH_PVP_TIMES). LIT = swLenhLit = còn đánh được. The
	// free normal command rides freeNormalTimes (CHALLENGE_LIST field 9);
	// the attack press always plays lit lệnh thường (vé cao cấp 105405 / vé
	// thường 105404 are never spent — [[tytd-item-ban]]).
	ready0, ready1 := 0, 0
	highPvp := src.swHighPvpValue()
	lenhRows := []map[string]any{}
	for _, d := range ent.GetDeclarationInfos() {
		pbd := d.GetPlayerBaseDataMsg()
		if pbd.GetPlayerId() == 0 {
			continue
		}
		lit := swLenhLit(d.GetType(), d.GetChallengeTimes(), highPvp)
		if lit {
			if d.GetType() == 1 {
				ready1++
			} else {
				ready0++
			}
		}
		if len(lenhRows) < 12 {
			lenhRows = append(lenhRows, map[string]any{"name": pbd.GetNickName(), "room": d.GetRoomId(), "lit": lit})
		}
	}
	out["lenh"] = lenhRows
	var freeNormal int32
	// live challenge rows first (whoever is fighting there right now)
	if list, err := src.swChallengeList(actID); err == nil && list.GetRet() == 0 {
		freeNormal = list.GetFreeNormalTimes()
		for _, in := range swWarInfosOf(list, unionID) {
			addDef(in.GetBeChallengedInfo())
		}
	}
	out["orders"] = []map[string]any{
		{"name": "Lệnh thường", "free": freeNormal, "targets": ready0,
			"can": freeNormal > 0 || ready0 > 0, "note": "dùng lượt free hằng ngày — bot chỉ đánh lệnh này"},
		{"name": "Lệnh cao cấp", "free": 0, "targets": ready1, "can": false,
			"note": "cần vé 105405 trong túi — bot không tự cắn vé"},
	}
	for _, rid := range swWarRoomIDs(ent, swWarMaxRooms) {
		var ri *proto.SeaWarRoomInfoResp
		switch s := src.(type) {
		case *Player:
			ri, err = s.swRoomInfo(actID, unionID, rid)
		case *EventPlayer:
			ri, err = s.swRoomInfo(actID, unionID, rid)
		}
		if err != nil || ri.GetRet() != 0 {
			continue
		}
		for _, d := range ri.GetBeChallengedInfos() {
			addDef(d)
		}
	}
	out["defenders"] = defs
	now := time.Now()
	atks := []map[string]any{}
	for _, a := range src.swAttacksSnapshot() {
		if a.TargetID != unionID {
			continue
		}
		who := a.Name
		if who == "" {
			who = a.Union
		}
		atks = append(atks, map[string]any{"name": who, "damage": a.Damage,
			"ago": int(now.Sub(a.At).Seconds())})
		if len(atks) >= 8 {
			break
		}
	}
	// newest first — the ring is append-ordered.
	for i, j := 0, len(atks)-1; i < j; i, j = i+1, j-1 {
		atks[i], atks[j] = atks[j], atks[i]
	}
	out["attacks"] = atks
	return out, nil
}

// webSeaWarWarOrder is the POST /events/order which="seawarwar" arm. The
// ONLY attack door for this war (ORDER-ONLY law) — fire on tap, no confirm,
// no attempt check ([[tytd-no-order-confirm]] + owner 2026-11-11).
func (t *TeleBot) webSeaWarWarOrder(w http.ResponseWriter, r *http.Request, chatID int64) {
	if r.FormValue("act") != "attack" {
		t.webRedirectMsg(w, r, "/seawarwar", "!!chọn hành động trước đã")
		return
	}
	unionID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("arg")), 10, 64)
	if err != nil || unionID <= 0 {
		t.webRedirectMsg(w, r, "/seawarwar", "!!mục tiêu không hợp lệ")
		return
	}
	// Name the target from the cached board (display only — the server
	// decides whether this union is beatable).
	baseName := "#" + strconv.FormatInt(unionID, 10)
	swWarBoards.mu.Lock()
	b := swWarBoards.byChat[chatID]
	swWarBoards.mu.Unlock()
	for _, m := range b.list.GetUnionMsg() {
		if bs := m.GetBase(); bs != nil && bs.GetUnionId() == unionID && bs.GetName() != "" {
			baseName = bs.GetName()
			break
		}
	}
	lines, err := t.swOrderAttack(chatID, unionID, baseName)
	if err != nil {
		t.webRedirectMsg(w, r, "/seawarwar", "!!"+err.Error())
		return
	}
	t.webRedirectMsg(w, r, "/seawarwar", "#ok#⚔️ "+baseName+"\n"+strings.Join(lines, "\n"))
}
