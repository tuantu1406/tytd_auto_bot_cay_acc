package tytd_go

import (
	"log"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Gather Energy (tụ linh trận) automation. Daily rhythm from
// GATHER_ENERGY_PROTOCOL.md + the owner's rules, all times ICT (UTC+7), the
// server window ENERGY_AREA_OPEN_TIME 600|720 = 10:00–22:00:
//
//   - any hour: claim finished-participation rewards as they queue up, and
//     watch the free ad claims (ENERGY_AREA_AD_LIMIT = 3/day, server's
//     getTimes is authoritative, client cooldown 10 s — we space 15 s).
//   - 10:00–21:30  → join the highest-income open formation that ENDS before
//     21:30, so its payoff lands the same day.
//   - 21:30–21:58  → join the highest-income formation with MORE THAN 10 h
//     left; the 22:00 close does not stop a running formation, so those keep
//     paying overnight — but never one with MORE THAN 24 h left: a joined
//     slot must end within a day of joining (owner's rule).
//   - 21:59–22:00  → deadline: join the best formation available; the 10 h
//     floor is gone, the 24 h cap still stands.
//   - 22:00–10:00  → the game closes the board (no join/open possible) but
//     formations keep running: the loop stays up, still claims rewards and
//     ads, and simply waits for the next window.
//
// Join path mirrors the official client: FIRST_LIST_VIEW picks the target,
// INSIDE_VIEW_NEW re-verifies it (window rule, free slot 1–4, not already
// in it, 5-minute rejoin cooldown), then ATTEND_NEW with the formation id
// only — the server assigns the seat. Never FIGHT: no dislodging anyone.

const (
	gatherAdLimit         = 3
	gatherAdCooldown      = 15 * time.Second  // ENERGY_AREA_AD_COLD_TIME is 10 s
	gatherPollInterval    = time.Minute       // fine enough to catch the 21:59 deadline minute
	gatherFirstDelay      = 40 * time.Second  // behind free spins (35 s)
	gatherListCooldown    = time.Minute       // one board scan per minute while looking
	gatherClaimCooldown   = 8 * time.Second   // one GET_REWARD in flight
	gatherEnterRetryAfter = 2 * time.Minute   // re-ask ENTER_NEW if the answer never came
	gatherWantTimeout     = 2 * time.Minute   // abandon a target whose replies vanished
	gatherLateMinLeft     = 10 * time.Hour    // owner's late-window bar
	gatherMaxLeft         = 24 * time.Hour    // joined slots must end within a day (21:30–21:59)
	gatherRejoinCD        = 302 * time.Second // client's hard 300000 ms, +2 s slack

	gatherOpenMin     = 10 * 60    // 10:00
	gatherCloseMin    = 22 * 60    // 22:00
	gatherSameDayMin  = 21*60 + 30 // 21:30 — early phase wants formations ending before this
	gatherDeadlineMin = 21*60 + 59 // 21:59 — last minute, drop the time bar
)

type gatherPhase int

const (
	gatherClosed   gatherPhase = iota // board shut (22:00–10:00)
	gatherEarly                       // 10:00–21:29: end before 21:30
	gatherLate                        // 21:30–21:58: more than 10 h left, at most 24 h
	gatherDeadline                    // 21:59–21:59:59: anything (up to the 24 h cap)
)

func (ph gatherPhase) String() string {
	switch ph {
	case gatherEarly:
		return "đầu ngày"
	case gatherLate:
		return "cuối ngày 10–24h"
	case gatherDeadline:
		return "hạn chót"
	default:
		return "nghỉ"
	}
}

func gatherPhaseFor(mins int) gatherPhase {
	switch {
	case mins < gatherOpenMin || mins >= gatherCloseMin:
		return gatherClosed
	case mins < gatherSameDayMin:
		return gatherEarly
	case mins < gatherDeadlineMin:
		return gatherLate
	default:
		return gatherDeadline
	}
}

// gatherCutoffToday is epoch-ms of 21:30 ICT on now's own date.
func gatherCutoffToday(now time.Time) int64 {
	y, m, d := now.In(ictZone).Date()
	return time.Date(y, m, d, 21, 30, 0, 0, ictZone).UnixMilli()
}

// gatherEntry is one FIRST_LIST_VIEW row in the shape the picker wants.
type gatherEntry struct {
	id        int64
	income    int64
	endTimeMs int64
	listSize  int32
	isAttend  bool
	openerID  int64
}

// gatherPick applies the phase's time bar, the account's income floor
// (minIncome from /tu_linh_min, 0 = no bar) and the client's own eligibility
// (room left, not already in it, not my own formation) and returns the best
// formation id — income first, later endTime breaking ties, exactly the
// official ranking. 0 means nothing worth joining.
func gatherPick(entries []gatherEntry, myID int64, phase gatherPhase, cutoffMs, nowMs, minIncome int64) int64 {
	if phase == gatherClosed {
		return 0
	}
	var best *gatherEntry
	for i := range entries {
		e := &entries[i]
		if e.id == 0 || e.listSize >= 5 || e.isAttend || (e.openerID != 0 && e.openerID == myID) {
			continue
		}
		if e.income < minIncome {
			continue // slower than the owner's floor — not worth a slot
		}
		switch phase {
		case gatherEarly:
			if e.endTimeMs >= cutoffMs {
				continue
			}
		case gatherLate:
			if !gatherWithinTimeBars(e.endTimeMs, nowMs) ||
				e.endTimeMs-nowMs <= int64(gatherLateMinLeft/time.Millisecond) {
				continue
			}
		case gatherDeadline:
			if !gatherWithinTimeBars(e.endTimeMs, nowMs) {
				continue
			}
		}
		if best == nil || e.income > best.income ||
			(e.income == best.income && e.endTimeMs > best.endTimeMs) {
			best = e
		}
	}
	if best == nil {
		return 0
	}
	return best.id
}

// gatherWithinTimeBars is the owner's 21:30–21:59 cap: a joined slot must
// end within 24 h of joining — nothing that runs longer than a full day.
func gatherWithinTimeBars(endTimeMs, nowMs int64) bool {
	return endTimeMs-nowMs <= int64(gatherMaxLeft/time.Millisecond)
}

// gatherMeetsPhaseBar re-checks a formation against the current phase's rule
// inside the INSIDE_VIEW_NEW answer (the compact list can be stale).
func gatherMeetsPhaseBar(e gatherEntry, phase gatherPhase, cutoffMs, nowMs int64) bool {
	switch phase {
	case gatherEarly:
		return e.endTimeMs < cutoffMs
	case gatherLate:
		return gatherWithinTimeBars(e.endTimeMs, nowMs) &&
			e.endTimeMs-nowMs > int64(gatherLateMinLeft/time.Millisecond)
	case gatherDeadline:
		return gatherWithinTimeBars(e.endTimeMs, nowMs)
	default:
		return false
	}
}

// gatherMinIncome reads the account's formation income floor from the live
// DB (/tu_linh_min or the settings page), so changing it takes effect on the
// next scan without a re-login. Nil DB (tests, session w/o handle) or a
// missing row reads as 0 — no bar.
func (p *Player) gatherMinIncome() int64 {
	if p.DB == nil {
		return 0
	}
	return model.GatherMinIncome(p.DB, p.UID)
}

func (p *Player) gatherSend(cmd string, msg pb.Message) {
	// The single wire choke: with the switch off no Tụ Linh traffic leaves,
	// including replies to pushes that raced the owner's toggle-off.
	if !p.gatherEnabled() {
		return
	}
	body, err := pb.Marshal(msg)
	if err != nil {
		log.Printf("[%s] [TuLinh] dựng gói %s lỗi: %v", p.Username, cmd, err)
		return
	}
	p.write(pack.Pack(cmd, body, p.UID))
}

// gatherTick is one minute of the loop: resync when stale, claim a queued
// reward, take a free ad claim, and — only while the board is open and a
// seat is actually available to us — scan the board for a formation.
func (p *Player) gatherTick(now time.Time) {
	day := now.Format("2006-01-02")
	mins := now.Hour()*60 + now.Minute()
	phase := gatherPhaseFor(mins)

	p.gatherMu.Lock()
	if p.gatherWantID != 0 && now.Sub(p.gatherWantAt) > gatherWantTimeout {
		p.gatherWantID = 0 // its replies vanished; allow a fresh scan
	}
	stale := !p.gatherKnown || p.gatherDay != day ||
		now.Sub(p.gatherEnterAt) > gatherEnterRetryAfter
	claimID := int64(-1)
	if len(p.gatherPending) > 0 && now.Sub(p.gatherClaimAt) >= gatherClaimCooldown {
		claimID = p.gatherPending[0]
		p.gatherClaimAt = now
	}
	adOK := p.gatherKnown && p.gatherAdTimes < gatherAdLimit &&
		now.Sub(p.gatherAdLast) >= gatherAdCooldown
	if adOK {
		p.gatherAdLast = now
	}
	// One scan per minute while the server grants us another join seat
	// (attendNum < totalAttendNum — the runtime seat count is server-defined,
	// one or two concurrent formations). Duplicate joins on the SAME
	// formation are guarded by the list's isAttend flag and the inside-view
	// teamList scan, both server data, so no local "already joined" flag is
	// needed here.
	listOK := p.gatherKnown && phase != gatherClosed &&
		p.gatherWantID == 0 &&
		p.gatherAttend < p.gatherTotal &&
		now.Sub(p.gatherListAt) >= gatherListCooldown
	// …and the once-a-minute mirror of that check when the server says we
	// are out of join slots (keeps the board refresh ticking while full).
	// Owner 2026-10-10: the [TuLinh] chatter logs are gone for good — the
	// loop is trusted now; only "bad … resp" protocol-drift alarms remain.
	fullLog := p.gatherKnown && phase != gatherClosed &&
		p.gatherWantID == 0 &&
		p.gatherTotal > 0 && p.gatherAttend >= p.gatherTotal &&
		now.Sub(p.gatherListAt) >= gatherListCooldown
	if stale {
		p.gatherDay, p.gatherEnterAt = day, now
	}
	if listOK || fullLog {
		p.gatherListAt = now
	}
	p.gatherMu.Unlock()

	if stale {
		p.gatherSend(GATHER_ENTER_NEW_CMD, &proto.GatherEnergyEnterNewReq{})
	}
	if claimID >= 0 {
		p.gatherSend(GATHER_GET_REWARD_CMD, &proto.GatherEnergyRewardReq{Id: pb.Int64(claimID)})
	}
	if adOK {
		p.gatherSend(GATHER_GET_AD_AWARD_CMD, &proto.GatherEnergyGetADAwardReq{IsUseADTime: pb.Bool(false)})
	}
	if listOK {
		p.gatherSend(GATHER_FIRST_LIST_VIEW_CMD,
			&proto.GatherEnergyFirstListViewReq{Offset: pb.Int32(0), FilterType: pb.Int32(0)})
	}
}

// gatherEnabled is the gate checked before EVERY gather attempt: first the
// GLOBAL gate (/set_auto_tu_linh — admin, DB-backed), then the session's own
// switch p.AutoGather (owner 2026-10-27: event opt-in gates live ONLY on the
// Player object — a DB lookup keyed by game_uid would mix up rows when one
// game account is configured by several people). /auto_tu_linh and the
// settings page patch the field on the live session, so toggles take effect
// on the next tick without a re-login.
func (p *Player) gatherEnabled() bool {
	return model.IsAutoTuLinhGateOpen(p.DB) && p.AutoGather
}

func (p *Player) autoGatherEnergy() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(gatherFirstDelay):
	}
	for {
		if p.gatherEnabled() {
			p.gatherTick(time.Now().In(ictZone))
		}
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(gatherPollInterval):
		}
	}
}

// gatherHandleEnter installs the authoritative state: totals, used ad count,
// and every finished reward the server already has queued for us.
func (p *Player) gatherHandleEnter(data []byte) {
	var resp proto.GatherEnergyEnterNewResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [TuLinh] bad enter resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		p.gatherMu.Lock()
		p.gatherEnterAt = time.Time{} // retry on the next tick
		p.gatherMu.Unlock()
		return
	}
	p.gatherMu.Lock()
	p.gatherKnown = true
	p.gatherTotal = resp.GetTotalAttendNum()
	if ge := resp.GetGatherEnergy(); ge != nil {
		p.gatherAttend = ge.GetAttendNum()
		if ge.GetTimes != nil {
			p.gatherAdTimes = ge.GetGetTimes()
		}
	}
	for _, r := range resp.GetReward() {
		if id := r.GetId(); id > 0 && !gatherHasID(p.gatherPending, id) {
			p.gatherPending = append(p.gatherPending, id)
		}
	}
	p.gatherMu.Unlock()
}

func gatherHasID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// gatherHandleAd: ret==0 means the opening item landed; getTimes is the
// server's authoritative claim count and also arrives on rejections, so the
// budget is taken from it rather than from local counting.
func (p *Player) gatherHandleAd(data []byte) {
	var resp proto.GatherEnergyGetADAwardResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [TuLinh] bad ad resp: %v", p.Username, err)
		return
	}
	p.gatherMu.Lock()
	if resp.GetTimes != nil {
		p.gatherAdTimes = resp.GetGetTimes()
	}
	p.gatherMu.Unlock()
}

// gatherHandleList ranks the board and queues an INSIDE_VIEW_NEW re-check
// for the best candidate; joining itself only ever happens from the inside
// handler, so a stale list can never produce a blind ATTEND.
func (p *Player) gatherHandleList(data []byte, now time.Time) {
	var resp proto.GatherEnergyFirstListViewResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [TuLinh] bad list resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		return
	}
	entries := make([]gatherEntry, 0, len(resp.GetList()))
	for _, e := range resp.GetList() {
		base := e.GetEnergyBaseMsg()
		if base == nil {
			continue
		}
		entries = append(entries, gatherEntry{
			id: base.GetId(), income: base.GetIncome(), endTimeMs: base.GetEndTime(),
			listSize: e.GetListSize(), isAttend: e.GetIsAttend(),
			openerID: e.GetOpenerMsg().GetPlayerId(),
		})
	}
	ict := now.In(ictZone)
	phase := gatherPhaseFor(ict.Hour()*60 + ict.Minute())
	id := gatherPick(entries, int64(p.UID), phase, gatherCutoffToday(now), now.UnixMilli(), p.gatherMinIncome())
	if id == 0 {
		return
	}
	p.gatherMu.Lock()
	p.gatherWantID, p.gatherWantAt = id, now
	p.gatherMu.Unlock()
	p.gatherSend(GATHER_INSIDE_VIEW_CMD, &proto.GatherEnergyInsideViewNewReq{Id: pb.Int64(id)})
}

// gatherHandleInside is the last gate before ATTEND_NEW: the phase rule on
// fresh data, a free slot among 1–4, not already a member, and the 5-minute
// rejoin cooldown after a dislodgement.
func (p *Player) gatherHandleInside(data []byte, now time.Time) {
	var resp proto.GatherEnergyInsideViewNewResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [TuLinh] bad inside resp: %v", p.Username, err)
		return
	}
	ict := now.In(ictZone)
	giveUp := func() {
		p.gatherMu.Lock()
		p.gatherWantID = 0
		p.gatherMu.Unlock()
	}
	if resp.GetRet() != 0 {
		giveUp()
		return
	}
	detail := resp.GetDetailInfo()
	base := detail.GetEnergyBaseMsg()
	if base.GetId() == 0 {
		giveUp()
		return
	}
	phase := gatherPhaseFor(ict.Hour()*60 + ict.Minute())
	e := gatherEntry{id: base.GetId(), income: base.GetIncome(), endTimeMs: base.GetEndTime()}
	if !gatherMeetsPhaseBar(e, phase, gatherCutoffToday(now), now.UnixMilli()) {
		giveUp() // rules moved on between list and inside; just rescan
		return
	}
	// The last speed check before sitting: the board entry was ranked on the
	// compact list, the join itself uses this fresh income.
	if minInc := p.gatherMinIncome(); e.income < minInc {
		giveUp()
		return
	}
	occupied := map[int32]bool{}
	me := int64(p.UID)
	for _, t := range detail.GetTeamList() {
		occupied[t.GetIndex()] = true
		if t.GetPlayerInfo().GetPlayerId() == me {
			giveUp() // already in this one; the next scan simply won't re-pick it (isAttend)
			return
		}
	}
	free := false
	for idx := int32(1); idx <= 4; idx++ { // index 0 is the opener's own seat
		if !occupied[idx] {
			free = true
			break
		}
	}
	if !free {
		giveUp()
		return
	}
	// Join-side cooldown after being dislodged: client math is
	// dislodgedTime + 300000 ms <= now. A countdown-style value (the
	// executable is inconsistent) stays harmless — far below an epoch.
	if dt := resp.GetDislodgedTime(); dt > 0 && dt+int64(gatherRejoinCD/time.Millisecond) > now.UnixMilli() {
		giveUp()
		return
	}
	p.gatherMu.Lock()
	seatsLeft := p.gatherAttend < p.gatherTotal
	p.gatherMu.Unlock()
	if !seatsLeft { // seat budget can fill between scan and inside (2nd join, sync push)
		giveUp()
		return
	}
	p.gatherSend(GATHER_ATTEND_NEW_CMD, &proto.GatherEnergyAttendNewReq{Id: pb.Int64(base.GetId())})
}

func (p *Player) gatherHandleAttend(data []byte) {
	var resp proto.GatherEnergyAttendNewResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [TuLinh] bad attend resp: %v", p.Username, err)
		return
	}
	p.gatherMu.Lock()
	p.gatherWantID = 0
	if resp.GetRet() == 0 {
		p.gatherAttend++ // mirror the client; ENTER_NEW/sync re-authoritative it
	}
	p.gatherMu.Unlock()
}

// gatherHandleRewardShow: one of the (up to two, server-defined) formations
// we hold has ended (finished or dislodged) — its reward is queued by id and
// exactly one join seat frees up. Other held formations are untouched, and
// with a seat open again the next tick's scan goes looking for a replacement.
func (p *Player) gatherHandleRewardShow(data []byte) {
	var resp proto.GatherEnergyRewardShowResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [TuLinh] bad reward-show push: %v", p.Username, err)
		return
	}
	id := resp.GetReward().GetId()
	if id == 0 {
		return
	}
	p.gatherMu.Lock()
	if !gatherHasID(p.gatherPending, id) {
		p.gatherPending = append(p.gatherPending, id)
	}
	if p.gatherAttend > 0 {
		p.gatherAttend--
	}
	p.gatherMu.Unlock()
}

// gatherHandleClaim settles the claim in flight (one at a time). The reward
// itself lands in the bag regardless of how the server answers, so the id
// leaves the queue on both verdicts (owner 2026-10-10: no per-claim logs).
func (p *Player) gatherHandleClaim(data []byte) {
	var resp proto.GatherEnergyRewardResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [TuLinh] bad claim resp: %v", p.Username, err)
		return
	}
	p.gatherMu.Lock()
	if len(p.gatherPending) > 0 {
		p.gatherPending = p.gatherPending[1:]
	}
	p.gatherMu.Unlock()
}

func (p *Player) gatherHandleSync(data []byte) {
	var msg proto.SyncGatherEnergyMsg
	if err := pb.Unmarshal(data, &msg); err != nil {
		log.Printf("[%s] [TuLinh] bad sync push: %v", p.Username, err)
		return
	}
	ge := msg.GetGatherEnergy()
	if ge == nil {
		return
	}
	p.gatherMu.Lock()
	p.gatherKnown = true
	p.gatherAttend = ge.GetAttendNum()
	if ge.GetTimes != nil {
		p.gatherAdTimes = ge.GetGetTimes()
	}
	p.gatherMu.Unlock()
}
