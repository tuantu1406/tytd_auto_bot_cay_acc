package tytd_go

// Web layer for the Luân Hồi Bí Cảnh bracket map (/luanhoi): the SSE frame
// source and the two map orders. BANG CHIẾN FIGHTING LAW (owner 2026-11-08
// "đánh bang chiến chỉ đánh ở event thôi, theo lệnh của player trên mini
// map"): the ⚔️ attack order is executed by the leader's EVENT accounts —
// every running one, free attempts only — and NOTHING else; the regular
// account never attacks and there is no auto-fight. The 🎫 cắn vé order
// stays the single sanctioned USE_ITEM command, on the main account's bag.
// The bracket reads ride the leader's own session (the union shares one
// board — owner's event accs sit in the same bang).

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"
)

// luanHoiStale re-pulls 217230 when the cached bracket is older than this;
// fresher answers come free from the same seat on every refresh.
const luanHoiStale = 25 * time.Second

// luanhoiDonor picks the session that answers 217230: the leader's own
// regular session first (the bracket is theirs); if the main account is not
// logged in as a Player, any member session whose 172 is live serves the
// same per-union board (same idiom as the kunlun event-session fallback).
func (t *TeleBot) luanhoiDonor(chatID int64) *Player {
	if v, ok := t.session.Load(chatID); ok {
		if p, okP := v.(*Player); okP && p.Running {
			if _, _, active := p.rbtActiveActivity(); active {
				return p
			}
		}
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
			if _, _, active := p.rbtActiveActivity(); active {
				return p
			}
		}
	}
	return nil
}

// luanhoiStreamPayload builds the which="luanhoi" frame. The main account's
// own session is the only source (the bracket is per-union).
func (t *TeleBot) luanhoiStreamPayload(chatID int64) (mapStreamResult, error) {
	p := t.luanhoiDonor(chatID)
	if p == nil {
		return mapStreamResult{idle: true}, nil
	}
	actID, _, active := p.rbtActiveActivity()
	if !active {
		return mapStreamResult{idle: true}, nil
	}
	nodes, snapAct, at := p.rbtBattleSnapshot()
	if len(nodes) == 0 || snapAct != actID || time.Since(at) > luanHoiStale {
		// Best effort: a seat-busy or timeout here just serves the last
		// snapshot (or idle when there is none).
		if rsp, err := p.rbtBattleList(actID); err == nil && rsp.GetRet() == 0 {
			nodes, snapAct, at = p.rbtBattleSnapshot()
		}
	}
	if len(nodes) == 0 {
		return mapStreamResult{idle: true}, nil
	}
	p.RbtMu.Lock()
	round, num, free, recov := p.rbtRoundNo, p.rbtBattleNum, p.rbtFreeAttempts, p.rbtBattleRecov
	p.RbtMu.Unlock()
	var fights []gimap.LhFightIn
	for _, a := range p.rbtAttacksSnapshot() {
		fights = append(fights, gimap.LhFightIn{
			Name: a.Name, TargetID: a.TargetID, Damage: a.Damage,
			AtMilli: a.At.UnixMilli(),
		})
	}
	sm := gimap.LuanHoiStream(nodes, int64(p.ClanID), round, num, free, recov,
		time.Now().UnixMilli(), fights)
	return mapStreamResult{sm: sm, pulled: p}, nil
}

// luanHoiCityAnswer is the popup-detail side of /events/mapcity?which=luanhoi:
// who guards this base (217231) + the recent hits the ring remembers.
func (t *TeleBot) luanHoiCityAnswer(p *Player, unionID int64) (map[string]any, error) {
	actID, _, active := p.rbtActiveActivity()
	if !active {
		return nil, fmt.Errorf("sự kiện chưa mở")
	}
	out := map[string]any{"union": unionID, "defenders": []map[string]any{}, "attacks": []map[string]any{}}
	list, err := p.rbtUnionPlayers(actID, unionID)
	if err != nil {
		return nil, err
	}
	defs := []map[string]any{}
	for _, pl := range list {
		defs = append(defs, map[string]any{"name": pl.GetNickName()})
	}
	out["defenders"] = defs
	now := time.Now()
	atks := []map[string]any{}
	recs := p.rbtAttacksSnapshot()
	for i := len(recs) - 1; i >= 0; i-- { // newest hits first
		a := recs[i]
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
	out["attacks"] = atks
	return out, nil
}

// rbtWarriors lists the leader's RUNNING event accounts with LHBC open —
// the ONLY bang chiến combatants (owner 2026-11-08 "đánh bang chiến chỉ
// đánh ở event thôi, theo lệnh của player trên mini map"). Same discovery
// idiom as eventKunlunPuller: leader-chat key first, then event users.
func (t *TeleBot) rbtWarriors(chatID int64) []*EventPlayer {
	try := func(id int64) *EventPlayer {
		v, ok := t.session.Load(id)
		if !ok {
			return nil
		}
		ep, okE := v.(*EventPlayer)
		if !okE || !ep.Running {
			return nil
		}
		if _, _, active := ep.rbtActiveActivity(); !active {
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

// rbtOrderAttack is the ONE bang chiến attack path: the player's ⚔️ order
// from the mini map (or /luanhoi_attack) makes EVERY running event account
// challenge the chosen base with FREE attempts (EP.rbtWarOrderOne gates
// attempts and never bites tickets [[tytd-item-ban]]). One honest line per
// account, errors inline; the server stays the sole arbiter of the war
// window (outside it 217233 simply refuses and the line says so).
func (t *TeleBot) rbtOrderAttack(chatID int64, unionID int64, baseName string) ([]string, error) {
	ws := t.rbtWarriors(chatID)
	if len(ws) == 0 {
		return nil, fmt.Errorf("không có acc sự kiện nào đang online trong Luân Hồi Bí Cảnh — acc thường không đánh bang chiến")
	}
	lines := make([]string, 0, len(ws))
	for _, ep := range ws {
		rsp, err := ep.rbtWarOrderOne(unionID)
		if err != nil {
			lines = append(lines, fmt.Sprintf("• %s: ❌ %s", ep.Username, err.Error()))
			continue
		}
		log.Printf("[%s] [LHBC] war order: đánh base %d", ep.Username, unionID)
		lines = append(lines, fmt.Sprintf("• %s: %s", ep.Username, rbtResultLine(baseName, rsp)))
	}
	return lines, nil
}

// webLuanHoiOrder is the POST /events/order which="luanhoi" arm.
func (t *TeleBot) webLuanHoiOrder(w http.ResponseWriter, r *http.Request, chatID int64) {
	act := r.FormValue("act")
	switch act {
	case "attack":
		unionID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("arg")), 10, 64)
		if err != nil || unionID <= 0 {
			t.webRedirectMsg(w, r, "/luanhoi", "!!mục tiêu không hợp lệ")
			return
		}
		// The union shares one board, so the leader-side donor names the
		// base; the hit itself lands on the event accounts.
		baseName := "#" + strconv.FormatInt(unionID, 10)
		if d := t.luanhoiDonor(chatID); d != nil {
			if snap, _, _ := d.rbtBattleSnapshot(); snap != nil {
				for _, m := range snap {
					if b := m.GetBase(); b != nil && b.GetUnionId() == unionID && b.GetName() != "" {
						baseName = b.GetName()
						break
					}
				}
			}
		}
		lines, err := t.rbtOrderAttack(chatID, unionID, baseName)
		if err != nil {
			t.webRedirectMsg(w, r, "/luanhoi", "!!"+err.Error())
			return
		}
		t.webRedirectMsg(w, r, "/luanhoi", "#ok#⚔️ "+baseName+"\n"+strings.Join(lines, "\n"))
	case "ve":
		v, ok := t.session.Load(chatID)
		p, okP := v.(*Player)
		if !ok || !okP || !p.Running {
			t.webRedirectMsg(w, r, "/luanhoi", "!!login (acc chính) đang offline")
			return
		}
		actID, _, active := p.rbtActiveActivity()
		if !active {
			t.webRedirectMsg(w, r, "/luanhoi", "!!Luân Hồi Bí Cảnh chưa mở — chưa cần vé")
			return
		}
		num := 1
		if s := strings.TrimSpace(r.FormValue("arg")); s != "" {
			n, err := strconv.Atoi(s)
			// Hard cap 5/press (ticket-ban exception stays deliberate).
			if err != nil || n < 1 || n > 5 {
				t.webRedirectMsg(w, r, "/luanhoi", "!!số vé phải là 1..5")
				return
			}
			num = n
		}
		rsp, err := p.rbtUseTicket(actID, num)
		if err != nil {
			t.webRedirectMsg(w, r, "/luanhoi", "!!gửi lệnh cắn vé thất bại ("+err.Error()+")")
			return
		}
		if rsp.GetRet() != 0 {
			t.webRedirectMsg(w, r, "/luanhoi", fmt.Sprintf("!!server từ chối cắn vé (ret %d — hết vé trong túi?)", rsp.GetRet()))
			return
		}
		t.webRedirectMsg(w, r, "/luanhoi", fmt.Sprintf("#ok#🎫 Đã cắn %d vé — còn %d lượt đánh", num, rsp.GetBattleNum()))
	default:
		t.webRedirectMsg(w, r, "/luanhoi", "!!chọn hành động trước đã")
	}
}

// luanHoiNodes converts a bracket snapshot for the Telegram-side renderers,
// folding the fight ring onto each base (red ring + newest attacker name).
func luanHoiNodes(p *Player, selfUnion int64) []gimap.LuanHoiNode {
	msgs, _, _ := p.rbtBattleSnapshot()
	type hit struct {
		name  string
		count int
	}
	recs := p.rbtAttacksSnapshot() // oldest first
	hits := map[int64]*hit{}
	for i := len(recs) - 1; i >= 0; i-- { // walk newest first: name = latest attacker
		a := recs[i]
		h := hits[a.TargetID]
		if h == nil {
			h = &hit{name: a.Name}
			if h.name == "" {
				h.name = a.Union
			}
			hits[a.TargetID] = h
		}
		h.count++
	}
	out := make([]gimap.LuanHoiNode, 0, len(msgs))
	for _, m := range msgs {
		b := m.GetBase()
		if b == nil || b.GetUnionId() == 0 {
			continue
		}
		pos := int(m.GetPosition())
		if pos == 0 {
			pos = int(m.GetRank())
		}
		if pos < 1 {
			pos = 1
		}
		n := gimap.LuanHoiNode{
			ID: b.GetUnionId(), Name: b.GetName(), Score: b.GetScore(),
			Rank: int(m.GetRank()), Pos: pos, LeftHP: m.GetReduceBlood(),
			Defeat: m.GetHasDefeated(), IsSelf: selfUnion != 0 && b.GetUnionId() == selfUnion,
		}
		if h := hits[b.GetUnionId()]; h != nil {
			n.AtkName, n.AtkCount = h.name, h.count
		}
		out = append(out, n)
	}
	return out
}
