package tytd_go

/*
Luân Hồi Bí Cảnh (RebornTrial, 172) for EVENT accounts — a faithful clone of
the Player engine (player_reborntrial.go) onto EventPlayer. Event users have
no per-account opt-in switch: as soon as an event account logs in, the loop
starts (Run() -> rebornTrialEventLoop), same "login is all it takes" policy
as godIslandStay / holyLandStay / bgAuto. All rules carry over verbatim:
exp-only decisions, bet nodes always wagered, treasures are CD-bound free
ops, no items EVER, guild war never automated, shop auto-buy limited to
free/ad/tiên-ngọc rows, milestone claims blind per-type 21033 + per-id 21045
for the union-recharge rungs [[tytd-reborntrial-172]].

Transport differences vs Player: answers ride a dedicated seat map
(rbtChans under RbtMu) fed by eventWsHandler via rbtHandleFrameEP, and the
hourly detail merge hook sits directly in the reader (rbtMergeConfig is
shared code, EP receiver below).
*/

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

var errRbtGone = errors.New("rbt: connection gone")
var errRbtTimeout = errors.New("rbt: answer timeout")
var errRbtBusy = errors.New("rbt: request already in flight")

// rbtRT is the EventPlayer seat transport (bmRoundTrip idiom, generic over
// answer type — one seat per answer hex, the loop is the only caller).
func (p *EventPlayer) rbtRT(cmd, wantType string, body []byte) ([]byte, error) {
	p.RbtMu.Lock()
	if p.rbtChans == nil {
		p.rbtChans = map[string]chan []byte{}
	}
	if p.rbtChans[wantType] != nil {
		p.RbtMu.Unlock()
		return nil, errRbtBusy
	}
	ch := make(chan []byte, 1)
	p.rbtChans[wantType] = ch
	p.RbtMu.Unlock()
	defer func() {
		p.RbtMu.Lock()
		if p.rbtChans[wantType] == ch {
			delete(p.rbtChans, wantType)
		}
		p.RbtMu.Unlock()
	}()
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
	case <-p.ctx.Done():
		return nil, errRbtGone
	case <-time.After(5 * time.Second):
		return nil, errRbtGone
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(rbtReqTimeout):
		return nil, errRbtTimeout
	case <-p.ctx.Done():
		return nil, errRbtGone
	}
}

// rbtHandleFrameEP is the event reader's entry point for every RebornTrial
// and generic-activity answer this session owns: refuseds get logged, the
// waiting seat gets the bytes.
func (p *EventPlayer) rbtHandleFrameEP(msgType string, data []byte) {
	switch msgType {
	case SERVER_RBT_ENTER:
		var rsp proto.RebornTrialDataResp
		if pb.Unmarshal(data, &rsp) == nil && rsp.GetRet() != 0 {
			log.Printf("[%s] [LHBC] vào bản đồ bị từ chối (code %d)", p.Username, rsp.GetRet())
		}
	case SERVER_RBT_ENTER_ACTIVITY:
		var rsp proto.RebornTrialEnterActivityResp
		if pb.Unmarshal(data, &rsp) == nil && rsp.GetRet() != 0 {
			log.Printf("[%s] [LHBC] vào hoạt động bị từ chối (code %d)", p.Username, rsp.GetRet())
		}
	}
	p.RbtMu.Lock()
	ch := p.rbtChans[msgType]
	p.RbtMu.Unlock()
	if ch != nil {
		select {
		case ch <- data:
		default:
		}
	}
}

// rebornTrialEventLoop is the event-account twin of rebornTrialAutoLoopAfter
// minus the DB opt-in gate — an event account exists to play events, so
// login alone turns it on.
func (p *EventPlayer) rebornTrialEventLoop() {
	if !p.rbtSleepCtx(8 * time.Second) {
		return
	}
	lastState := ""
	var stateSince time.Time
	record := func(s string) {
		if s == lastState {
			return
		}
		lastState = s
		stateSince = time.Now()
		log.Printf("[%s] [LHBC] auto: %s", p.Username, s)
	}
	var nextStatus time.Time
	statusNow := ""
	var nextTree time.Time
	beat := func() {
		if nextStatus.IsZero() {
			nextStatus = time.Now().Add(p.rbtBeatGap())
			return
		}
		if time.Now().Before(nextStatus) {
			return
		}
		p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: %s — trạng thái này %s",
			statusNow, stFmtDur(time.Since(stateSince))))
		nextStatus = time.Now().Add(p.rbtBeatGap())
	}
	for {
		if p.OffRbt {
			record("tắt — leader đã gạt tắt ở tab Sự kiện")
			nextStatus, statusNow = time.Time{}, ""
			if !p.rbtSleepCtx(time.Minute) {
				return
			}
			continue
		}
		actID, beginMs, ok := p.rbtActiveActivity()
		if !ok {
			record("chưa thấy Luân Hồi Bí Cảnh trong danh sách sự kiện")
			nextStatus, statusNow = time.Time{}, ""
			if !p.rbtSleepCtx(rbtIdleWait) {
				return
			}
			continue
		}
		if time.Now().After(nextTree) {
			p.rbtTreeCycle(actID)
			p.rbtShopSweep(actID)
			p.rbtCondSweep(actID)
			p.rbtRankSweep(actID)
			gap := rbtTreeGap
			if p.rbtTreeGap > 0 {
				gap = p.rbtTreeGap
			}
			nextTree = time.Now().Add(gap)
		}
		state, round, nextBound := rbtPhase(beginMs, p.rbtCfg(), time.Now())
		switch state {
		case rbtNone, rbtDone:
			record("hoạt động chưa vào vòng chiến — chờ tới kỳ")
			nextStatus, statusNow = time.Time{}, ""
			if !p.rbtSleepCtx(rbtWakeIn(nextBound)) {
				return
			}
			continue
		case rbtPrep:
			record(fmt.Sprintf("đang chờ vòng chiến (còn %s)", stFmtDur(time.Until(time.UnixMilli(nextBound)))))
			nextStatus, statusNow = time.Time{}, ""
			if !p.rbtSleepCtx(rbtWakeIn(nextBound)) {
				return
			}
			continue
		case rbtBattle:
		}
		statusNow = fmt.Sprintf("đang chạy xe thư thương vòng %d", round)
		record(statusNow)
		if !p.rbtPlayTick(actID, round) {
			return
		}
		// Bang chiến: NO auto-fight here — the ONLY combatants are the
		// event accounts, and they attack ONLY on the player's mini-map
		// order (owner 2026-11-08): webLuanHoiOrder / rbtAttackHandler
		// fire rbtWarOrderOne on this very session.
		beat()
		d := p.rbtPollWait()
		if nextBound > 0 {
			if untilPreClose := time.Until(time.UnixMilli(nextBound).Add(-rbtPreCloseGap)); untilPreClose > 0 && untilPreClose < d {
				d = untilPreClose
			}
		}
		if !p.rbtSleepCtx(d) {
			return
		}
	}
}

func (p *EventPlayer) rbtEnterActivity(actID int32) (*proto.RebornTrialEnterActivityResp, error) {
	data, err := p.rbtRT(RBT_ENTER_ACTIVITY_CMD, SERVER_RBT_ENTER_ACTIVITY, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialEnterActivityResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) rbtEnter(actID int32) (*proto.RebornTrialDataResp, error) {
	data, err := p.rbtRT(RBT_ENTER_CMD, SERVER_RBT_ENTER, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialDataResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) rbtExplore(actID int32) (*proto.RebornTrialDataResp, error) {
	data, err := p.rbtRT(RBT_EXPLORE_CMD, SERVER_RBT_EXPLORE, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialDataResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) rbtSelectOption(actID, order int32) (*proto.RebornTrialSelectOptionResp, error) {
	body, err := pb.Marshal(&proto.RebornTrialSelectOptionReq{ActivityId: actID, SelectIndex: order})
	if err != nil {
		return nil, err
	}
	data, err := p.rbtRT(RBT_SELECT_OPTION_CMD, SERVER_RBT_SELECT_OPTION, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialSelectOptionResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) rbtSkipEvent(actID int32) (*proto.RebornTrialSkipEventResp, error) {
	data, err := p.rbtRT(RBT_SKIP_EVENT_CMD, SERVER_RBT_SKIP_EVENT, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialSkipEventResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// rbtBuyGoods sends 21005 (one unit, isUseADTime=false exactly like the
// owner-captured packet — the server enforces limits/cooldowns itself).

func (p *EventPlayer) rbtBuyGoods(actID, mallID int32) (*proto.RspBuyActivityGoods, error) {
	body, err := pb.Marshal(&proto.ReqBuyActivityGoods{ActivityId: actID, MallId: mallID, Count: 1})
	if err != nil {
		return nil, err
	}
	data, err := p.rbtRT(ACTIVITY_BUY_GOODS_CMD, SERVER_ACTIVITY_BUY_GOODS, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.RspBuyActivityGoods
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// rbtMallAuto decides whether a shop row is bot-buyable and its daily cap.
// Owner authorisation 2026-10-09: (a) free/ad-watch rows (price "0"/"X=0" —
// includes "Gói Video Cổ Vũ" param "3|1800|…" and "Phúc Lợi Miễn Phí") and
// (b) the TIÊN NGỌC pack (price "100000=N") up to its full buyLimit/day.
// Every other row (numeric recharge price, param "1|") is money spend —
// NEVER sent.

func (p *EventPlayer) rbtShopSweep(actID int32) {
	// The shop table only exists after one 21003 answer was merged; nothing
	// else pulls detail for a 172 activity, so ask for it once per sweep.
	p.RbtMu.Lock()
	empty := len(p.rbtMallRows) == 0
	p.RbtMu.Unlock()
	if empty {
		body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID})
		if err == nil {
			if _, err := p.rbtRT(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err != nil {
				log.Printf("[%s] [LHBC] shop: không lấy được cấu hình cửa hàng (%v) — thử lại chu kỳ sau", p.Username, err)
				return // shop closed / offline — try next cycle
			}
			p.RbtMu.Lock()
			n := len(p.rbtMallRows)
			p.RbtMu.Unlock()
			log.Printf("[%s] [LHBC] shop: đọc được %d gói từ máy chủ", p.Username, n)
		}
	}
	today := time.Now().In(ictZone).Format("2006-01-02")
	p.RbtMu.Lock()
	rows := append([]*proto.ActivityMallTempMsg{}, p.rbtMallRows...)
	bought := p.rbtMallBought
	if p.rbtMallDay != today || p.rbtMallAdds == nil {
		p.rbtMallDay = today
		p.rbtMallAdds = map[int32]int64{}
	}
	adds := p.rbtMallAdds
	p.RbtMu.Unlock()
	if bought == nil {
		bought = map[int32]int64{}
	}
	for _, mt := range rows {
		limit, ad, jade := rbtMallAuto(mt)
		if limit == 0 {
			continue
		}
		used := bought[mt.GetId()] + adds[mt.GetId()]
		for used < limit {
			rsp, err := p.rbtBuyGoods(actID, mt.GetId())
			if err != nil || rsp.GetRet() != 0 {
				log.Printf("[%s] [LHBC] shop: không mua được %s (%d/%d): err=%v ret=%d — để lại chu kỳ sau",
					p.Username, mt.GetName(), used+1, limit, err, rsp.GetRet())
				break // cooldown / not enough jade / shop closed — retry next cycle
			}
			p.RbtMu.Lock()
			p.rbtMallAdds[mt.GetId()]++
			p.RbtMu.Unlock()
			used++
			kind := "quà miễn phí"
			if ad {
				kind = "gói xem quảng cáo"
			} else if jade {
				kind = "gói tiên ngọc"
			}
			p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: đã mua %s (%s) %d/%d — quà: %s",
				kind, mt.GetName(), used, limit, rsp.GetRewards()))
		}
	}
}

// rbtClaimType settles every ready milestone of one task TYPE via 21033
// (the RebornTrial panel's own claim path — the server batches the whole
// type group and answers {ret}; ret≠0 just means "nothing ready").

func (p *EventPlayer) rbtClaimType(actID, typ int32) (string, error) {
	body, err := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(typ)})
	if err != nil {
		return "", err
	}
	data, err := p.rbtRT(ACTIVITY_TOKEN_REWARD_CMD, SERVER_ACTIVITY_TOKEN_REWARD, body)
	if err != nil {
		return "", err
	}
	var rsp proto.RspGetActivityConditionReward
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return "", err
	}
	if rsp.GetRet() != 0 {
		return "", fmt.Errorf("ret %d", rsp.GetRet())
	}
	return rsp.GetRewards(), nil
}

// rbtClaimCross receives one cross-server union-recharge milestone (2xxxx
// band) via 21045 — {activityId, conditionId}, the shape captured live.

func (p *EventPlayer) rbtClaimCross(actID, condID int32) (string, error) {
	body, err := pb.Marshal(&proto.ReceiveCrossUnionRechargeRewardReq{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(condID)})
	if err != nil {
		return "", err
	}
	data, err := p.rbtRT(ACTIVITY_CROSS_RECHARGE_CMD, SERVER_ACTIVITY_CROSS_RECHARGE, body)
	if err != nil {
		return "", err
	}
	var rsp proto.RspGetActivityConditionReward
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return "", err
	}
	if rsp.GetRet() != 0 {
		return "", fmt.Errorf("ret %d", rsp.GetRet())
	}
	return rsp.GetRewards(), nil
}

// rbtCondSweep batch-claims milestones the panel groups by TYPE: one 21033
// per distinct type settles EVERY genuinely-ready row of that group (the
// server, not us, decides what's ready — 21003's condition values are known
// to be stale zeros for some types, live 2026-10-09: it claimed the "số
// người nạp trong bang" rung whose stored value read "0"). ret≠0 just means
// the group had nothing → quiet. 2xxxx cross-union rows go per-id through
// 21045. The sweep is idempotent (received rows never re-settle), so no
// local bookkeeping is needed beyond not spamming the same type twice in
// one pass.
// rbtCondSweep batch-claims milestones in two paths:
//   - UNION-RECHARGE rows (task type 84, live owner report 2026-10-09: the
//     "số người nạp trong Bang" rungs of THIS event — each event carries
//     its own ladder rows under its own activityId; the 21003 condition
//     value for them is a stale 0, the real count lives in 21511) → per-id
//     21045 {activityId, conditionId}, exactly like the client's
//     UnionActivitiesItem.
//   - every other row → one BLIND 21033 per distinct type (server settles
//     only genuinely-ready rungs, ret≠0 when the group has nothing; the
//     operation is idempotent).
// ret≠0 ⇒ quiet, no spam beyond one attempt per sweep pass.

func (p *EventPlayer) rbtCondSweep(actID int32) {
	p.RbtMu.Lock()
	cfgs := append([]*proto.ActivityConditionConfig{}, p.rbtCondCfgs...)
	p.RbtMu.Unlock()
	if len(cfgs) == 0 {
		// Blind pull (owner 2026-11-07): never skip the sweep because the
		// fold hasn't happened — 21003's answer merges rbtCondCfgs through
		// the reader, so one ask per cycle arms every later pass too.
		if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
			if _, err := p.rbtRT(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err != nil {
				return
			}
		}
		p.RbtMu.Lock()
		cfgs = append([]*proto.ActivityConditionConfig{}, p.rbtCondCfgs...)
		p.RbtMu.Unlock()
		if len(cfgs) == 0 {
			return
		}
	}
	seenType := map[int32]bool{}
	seenCross := map[int32]bool{}
	for _, cfg := range cfgs {
		cid := cfg.GetConditionId()
		act := cfg.GetActivityId()
		if act == 0 {
			act = actID
		}
		if cfg.GetType() == 84 || cid >= 20000 {
			if seenCross[cid] {
				continue
			}
			seenCross[cid] = true
			rewards, err := p.rbtClaimCross(act, cid)
			if err != nil || rewards == "" {
				continue
			}
			name := cfg.GetDesc()
			if name == "" {
				name = cfg.GetTitle()
			}
			if len(cfg.GetParam()) > 0 {
				name = strings.ReplaceAll(name, "{0}", cfg.GetParam()[0])
			}
			p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: nhận quà mốc nạp bang %s — %s", name, rewards))
			continue
		}
		if seenType[cfg.GetType()] {
			continue
		}
		seenType[cfg.GetType()] = true
		rewards, err := p.rbtClaimType(act, cfg.GetType())
		if err != nil {
			continue // ret≠0 / timeout: nothing ready in this group — quiet
		}
		if rewards == "" {
			continue
		}
		p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: đã nhận quà mốc nhóm %d — %s", cfg.GetType(), rewards))
	}
	p.rbtLadderSweep(actID)
}

// rbtLadderSweep claims the "chi tiết cảnh giới" ladder — the type-307
// "Cảnh giới đạt {0}" rows — exactly like the client's panel button: ONE
// batched 21022 GET_CONDITION_REWARD_BY_ARR (client JS mine 2026-11-07:
// CommonAchievementView with getAllRewardStatus()=true →
// ActivityMgr.reqGetActivityConditionRewardByArr({activityDataList});
// Protocol.S_ACTIVITY_GET_CONDITION_REWARD_BY_ARR=21022, answer 03fe).
// The server claims rungs in ascending order and stops at the first
// not-yet-reached one (ret=203), so one blind frame per cycle settles
// every ready rung; 21003's isGetReward diff names what landed.
func (p *EventPlayer) rbtLadderSweep(actID int32) {
	p.RbtMu.Lock()
	var refs []*proto.AcitivityConditionDataArr
	claimedBefore := map[int32]bool{}
	for _, c := range p.rbtCondCfgs {
		if c.GetType() == 307 && len(refs) < 100 {
			refs = append(refs, &proto.AcitivityConditionDataArr{
				ActivityId: pb.Int32(actID), ConditionId: pb.Int32(c.GetConditionId())})
		}
	}
	for id, cd := range p.rbtCondData {
		claimedBefore[id] = cd.GetIsGetReward()
	}
	p.RbtMu.Unlock()
	if len(refs) == 0 {
		return
	}
	body, err := pb.Marshal(&proto.ReqGetActivityConditionRewardByArr{ActivityDataList: refs})
	if err != nil {
		return
	}
	data, err := p.rbtRT(ACTIVITY_COND_REWARD_ARR_CMD, SERVER_ACTIVITY_COND_REWARD_ARR, body)
	if err != nil {
		return // silent ladder — nothing this cycle can settle it
	}
	var rsp proto.RspGetActivityConditionReward
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() == 0 && strings.TrimSpace(rsp.GetRewards()) != "" {
		p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: đã nhận quà cảnh giới: %s", strings.TrimSpace(rsp.GetRewards())))
		return
	}
	if b, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if _, err := p.rbtRT(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, b); err != nil {
			return
		}
	}
	p.RbtMu.Lock()
	var newly []string
	for _, c := range p.rbtCondCfgs {
		cid := c.GetConditionId()
		if c.GetType() != 307 || claimedBefore[cid] {
			continue
		}
		if cd := p.rbtCondData[cid]; cd != nil && cd.GetIsGetReward() {
			name := c.GetDesc()
			if name == "" {
				name = c.GetTitle()
			}
			if len(c.GetParam()) > 0 {
				name = strings.ReplaceAll(name, "{0}", c.GetParam()[0])
			}
			newly = append(newly, name)
		}
	}
	p.RbtMu.Unlock()
	if len(newly) > 0 {
		p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: đã nhận %d quà cảnh giới (%s)", len(newly), strings.Join(newly, ", ")))
	}
}

// rbtUseMagic sends 217207 — the CD-bound treasure ops (repair/illusion/
// suppress). No items or exp are involved; the server answers with the
// changed goods item, the refreshed magic item (new cd) and, for type 3,
// the resolved event.

func (p *EventPlayer) rbtUseMagic(actID, magicType, goodsID int32) (*proto.RebornTrialUseMagicResp, error) {
	body, err := pb.Marshal(&proto.RebornTrialUseMagicReq{ActivityId: actID, MagicType: magicType, GoodsId: goodsID})
	if err != nil {
		return nil, err
	}
	data, err := p.rbtRT(RBT_USE_MAGIC_CMD, SERVER_RBT_USE_MAGIC, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialUseMagicResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) rbtLimits() ([]int32, []int32) {
	p.RbtMu.Lock()
	repair, fake := p.rbtRepairLimit, p.rbtFakeLimit
	p.RbtMu.Unlock()
	def := []int32{1, 2, 3} // client defaults of the two activity params
	if len(repair) == 0 {
		repair = def
	}
	if len(fake) == 0 {
		fake = def
	}
	return repair, fake
}

// rbtTreasureSweep spends every CD-bound treasure the client would offer:
// repair (magicType 1) on goods whose status passes the repair list and
// were never repaired (magicTimes==0), illusion (magicType 2) on goods
// whose quality passes the fake list and are un-masked — both only on
// goods the ItemConfig marks as non-treasure (isMagic==0, table must be
// known or nothing is spent, mirroring the client which grays the buttons
// without config). Cooldowns from the answers gate the loop for free.

func (p *EventPlayer) rbtTreasureSweep(actID int32, pd *proto.RebornTrialPlayerData) {
	p.RbtMu.Lock()
	itemMagic := p.rbtItemMagic
	p.RbtMu.Unlock()
	if len(itemMagic) == 0 {
		return // no ItemConfig table yet — eligibility unknown, spend nothing
	}
	repair, fake := p.rbtLimits()
	inList := func(l []int32, v int32) bool {
		for _, x := range l {
			if x == v {
				return true
			}
		}
		return false
	}
	cd := map[int32]int32{}
	for _, m := range pd.GetMagicItem() {
		cd[m.GetMagicType()] = m.GetCd()
	}
	for round := 0; round < 8; round++ {
		used := false
		for _, mtype := range []int32{1, 2} {
			if cd[mtype] > 0 {
				continue
			}
			var target *proto.RebornTrialGoodsItem
			for _, g := range pd.GetGoodsItem() {
				isM, known := itemMagic[g.GetGoodsId()]
				if !known || isM > 0 {
					continue
				}
				if mtype == 1 {
					if g.GetMagicTimes() == 0 && inList(repair, g.GetStatus()) {
						target = g
						break
					}
				} else if g.GetMask() == 0 && inList(fake, g.GetQuality()) {
					target = g
					break
				}
			}
			if target == nil {
				continue
			}
			rsp, err := p.rbtUseMagic(actID, mtype, target.GetId())
			if err != nil {
				return
			}
			if rsp.GetRet() != 0 {
				cd[mtype] = 1 // refused — stop this type for this tick
				continue
			}
			if gi := rsp.GetGoodsItem(); gi != nil {
				for i, g := range pd.GoodsItem {
					if g.GetId() == gi.GetId() {
						pd.GoodsItem[i] = gi
						break
					}
				}
			}
			if mi := rsp.GetMagicItem(); mi != nil {
				cd[mi.GetMagicType()] = mi.GetCd()
			} else {
				cd[mtype] = 1
			}
			log.Printf("[%s] [LHBC] pháp bảo type %d dùng trên hàng #%d ok (cd %d)", p.Username, mtype, target.GetId(), cd[mtype])
			used = true
		}
		if !used {
			break
		}
	}
}

// rbtEventMagic3 tries the suppress-event treasure (magicType 3, goodsId 0)
// on the pending event — the client shows that button when the magic exists
// with cd 0 and the event's param2 is not blocking. Returns the resolved
// playerData event (possibly nil) and whether the attempt succeeded.

func (p *EventPlayer) rbtEventMagic3(actID int32, pd *proto.RebornTrialPlayerData) bool {
	ev := pd.GetEvent()
	if ev == nil || ev.GetStat() != 0 || ev.GetParam2() > 0 {
		return false
	}
	for _, m := range pd.GetMagicItem() {
		if m.GetMagicType() == 3 && m.GetCd() <= 0 {
			rsp, err := p.rbtUseMagic(actID, 3, 0)
			if err != nil || rsp.GetRet() != 0 {
				return false
			}
			if e2 := rsp.GetEvent(); e2 != nil && e2.GetStat() == 0 {
				// ret0-but-pending no-op (same trap as SELECT): this magic
				// does not clear this event — stand down, options decide.
				m.Cd = pb.Int32(m.GetCd() + 1)
				return false
			}
			pd.Event = rsp.GetEvent()
			if mi := rsp.GetMagicItem(); mi != nil {
				for _, mm := range pd.MagicItem {
					if mm.GetMagicType() == 3 {
						mm.Cd = pb.Int32(mi.GetCd())
						break
					}
				}
			}
			return true
		}
	}
	return false
}

func (p *EventPlayer) rbtWorship(actID int32) (*proto.RebornTrialWorshipResp, error) {
	data, err := p.rbtRT(RBT_WORSHIP_CMD, SERVER_RBT_WORSHIP, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialWorshipResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) rbtBonusInfo(actID int32) (*proto.RebornTrialBonusResp, error) {
	data, err := p.rbtRT(RBT_BONUS_CMD, SERVER_RBT_BONUS, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialBonusResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) rbtBonusResult(actID int32) (*proto.RebornTrialGetBonusResultResp, error) {
	data, err := p.rbtRT(RBT_BONUS_RESULT_CMD, SERVER_RBT_BONUS_RESULT, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialGetBonusResultResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// rbtClaimBonusAll presses the client's "Nhận nhanh" button: GET_BONUS_ITEM
// id=0 (FORCED on the wire via rbtBonusItemAllBody — a proto3 marshal of
// Id:0 drops the field and the server answers SILENT, live-proved
// 2026-11-07) collects every ready WATER notice on the union tree. Pure
// receive — the bot never SENDs liquid (217223 donates items
// [[tytd-item-ban]]).

func (p *EventPlayer) rbtClaimBonusAll(actID int32) (*proto.RebornTrialGetBonusItemResp, error) {
	data, err := p.rbtRT(RBT_BONUS_ITEM_CMD, SERVER_RBT_BONUS_ITEM, rbtBonusItemAllBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialGetBonusItemResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) rbtGuessData(actID int32) (*proto.RebornTrialGuessDataResp, error) {
	data, err := p.rbtRT(RBT_GUESS_DATA_CMD, SERVER_RBT_GUESS_DATA, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialGuessDataResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) rbtGuessReward(actID int32) (*proto.RebornTrialGuessRewardResp, error) {
	data, err := p.rbtRT(RBT_GUESS_REWARD_CMD, SERVER_RBT_GUESS_REWARD, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialGuessRewardResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// rbtAutoExplore fires the client's own free server-side batch with its
// DEFAULT settings table (the exact bytes the UI sends, captured live).
// Refused (window/level gate) → caller falls back to the manual march.

func (p *EventPlayer) rbtAutoExplore(actID int32) (*proto.RebornTrialAutoExploreRsp, error) {
	req := &proto.RebornTrialAutoExploreReq{ActivityId: actID}
	for _, d := range []struct {
		ev int32
		s  []int32
	}{
		{1, []int32{1, 1, 1, 0}}, {2, []int32{1, 1, 1, 0}}, {3, []int32{1, 1, 1}},
		{4, []int32{1, 1, 1}}, {5, []int32{1, 1, 1}}, {6, []int32{0, 2}}, {7, []int32{0, 2}},
		{20, []int32{0}}, {21, []int32{0}}, {22, []int32{0, 0}},
	} {
		req.SettingItems = append(req.SettingItems, &proto.RebornTrialAutoSettingItem{
			Event:        d.ev,
			EventSetting: d.s,
		})
	}
	body, err := pb.Marshal(req)
	if err != nil {
		return nil, err
	}
	data, err := p.rbtRT(RBT_AUTO_EXPLORE_CMD, SERVER_RBT_AUTO_EXPLORE, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialAutoExploreRsp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// rbtHandleFrame feeds auto-loop seats and logs every refused code — the
// single place a RebornTrial server answer is judged (same role as
// stHandleFrame for the caravan).

func (p *EventPlayer) rbtMergeConfig(data []byte) {
	var rsp proto.RspGetActivityDetail
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return
	}
	dc := rsp.GetActivity().GetDetailConfig()
	cc := dc.GetCommonConfig()
	if cc.GetMainConfig().GetType() != rbtActivityType {
		return
	}
	open, close_ := 0, 0
	for _, pc := range cc.GetParamConfig() {
		if pc.GetParam() == "RENBORNTRIAL_CHALLENGE_DURATION" {
			parts := strings.Split(pc.GetValue(), "|")
			if len(parts) == 2 {
				if a, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && a >= 0 && a <= 24 {
					open = a * 3600
				}
				if b, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && b >= 0 && b <= 24 {
					close_ = b * 3600
				}
			}
		}
	}
	var challengeItem int32
	var energyItem, staminaLimit int32
	var liquidIDs []int32
	repairLimit, fakeLimit := []int32{}, []int32{}
	parseList := func(v string) []int32 {
		out := []int32{}
		for _, part := range strings.Split(v, "|") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				out = append(out, int32(n))
			}
		}
		return out
	}
	for _, pc := range cc.GetParamConfig() {
		switch pc.GetParam() {
		case "RENBORNTRIAL_ENERGY_ITEM_ID":
			// Energy fruit id — arms rbtEatEnergy (client default 100159).
			if n, err := strconv.Atoi(strings.TrimSpace(pc.GetValue())); err == nil && n > 0 {
				energyItem = int32(n)
			}
		case "RENBORNTRIAL_INITIAL_ENERGY":
			// Stamina cap the fruit tops up to (client default 20).
			if n, err := strconv.Atoi(strings.TrimSpace(pc.GetValue())); err == nil && n > 0 {
				staminaLimit = int32(n)
			}
		case "RENBORNTRIAL_ITEM_ENERGY":
			// The union-tree liquid ("nước"): "id|value;id|value;…" —
			// exactly the 3 water types the client's grant view lists
			// (liquidProData → icon1..3). We donate every one we hold.
			for _, part := range strings.Split(pc.GetValue(), ";") {
				kv := strings.Split(strings.TrimSpace(part), "|")
				if n, err := strconv.Atoi(strings.TrimSpace(kv[0])); err == nil && n > 0 {
					liquidIDs = append(liquidIDs, int32(n))
				}
			}
		case "RENBORNTRIAL_GOODS_REPAIR_LIMIT":
			repairLimit = parseList(pc.GetValue())
		case "RENBORNTRIAL_GOODS_FAKE_LIMIT":
			fakeLimit = parseList(pc.GetValue())
		case "RENBORNTRIAL_CHALLENGE_ITEM":
			if n, err := strconv.Atoi(strings.TrimSpace(pc.GetValue())); err == nil && n > 0 {
				challengeItem = int32(n)
			}
		}
	}
	itemMagic := map[int32]int32{}
	var sync proto.RebornTrialConfigSync
	if len(dc.GetUniqueConfig()) > 0 && pb.Unmarshal(dc.GetUniqueConfig(), &sync) == nil {
		for _, ic := range sync.GetItemConfig() {
			itemMagic[ic.GetItemId()] = ic.GetIsMagic()
		}
	}
	var mallRows []*proto.ActivityMallTempMsg
	for _, mc := range cc.GetMallConfig() {
		if mt := mc.GetMallTempMsg(); mt != nil {
			mallRows = append(mallRows, mt)
		}
	}
	bought := map[int32]int64{}
	for _, bc := range rsp.GetActivity().GetMallBuyCountList() {
		bought[bc.GetMallId()] = bc.GetCount()
	}
	condCfgs := cc.GetConditionConfig()
	condData := map[int32]*proto.ActivityConditionData{}
	for _, cd := range rsp.GetActivity().GetConditionDataList() {
		condData[cd.GetConditionId()] = cd
	}
	var rankTypes []int32
	seenRT := map[int32]bool{}
	for _, rc := range cc.GetRankConfig() {
		if t := rc.GetType(); t > 0 && !seenRT[t] {
			seenRT[t] = true
			rankTypes = append(rankTypes, t)
		}
	}
	p.RbtMu.Lock()
	if len(mallRows) > 0 {
		p.rbtMallRows = mallRows
	}
	p.rbtMallBought = bought
	if len(condCfgs) > 0 {
		p.rbtCondCfgs = condCfgs
	}
	p.rbtCondData = condData
	// The merge is the source of truth for what's already received — the
	// local suppression cache rebuilds from it each hour (so daily-refresh
	// milestones become claimable again on their own schedule).
	got := map[int32]bool{}
	for id, cd := range condData {
		if cd.GetIsGetReward() {
			got[id] = true
		}
	}
	p.rbtCondGot = got
	if open > 0 && close_ > open {
		p.rbtDailyOpenSec, p.rbtDailyCloseSec = open, close_
	}
	if len(repairLimit) > 0 {
		p.rbtRepairLimit = repairLimit
	}
	if len(fakeLimit) > 0 {
		p.rbtFakeLimit = fakeLimit
	}
	if len(itemMagic) > 0 {
		p.rbtItemMagic = itemMagic
	}
	if len(liquidIDs) > 0 {
		p.rbtLiquidIDs = liquidIDs
	}
	if energyItem > 0 {
		p.rbtEnergyItem = energyItem
	}
	if staminaLimit > 0 {
		p.rbtStaminaLimit = staminaLimit
	}
	if challengeItem > 0 {
		p.rbtChallengeItem = challengeItem
	}
	if len(rankTypes) > 0 {
		p.rbtRankTypes = rankTypes
	}
	p.RbtMu.Unlock()
	// The phase calendar deliberately does NOT come from uniqueConfig's
	// timeConfig: live (2026-10-08) proved its beginTime/durationTime are
	// not plain hours the way the round-1 answer needs (round computed from
	// the folded table disagreed with the server's own EnterActivity
	// round=1). The client ships its own default stage table for exactly
	// this reason — rbtDefaultTimeCfg mirrors it and matched the live
	// server's round/phase reporting.
}

func (p *EventPlayer) rbtWindow() (int, int) {
	p.RbtMu.Lock()
	o, c := p.rbtDailyOpenSec, p.rbtDailyCloseSec
	p.RbtMu.Unlock()
	if o == 0 || c == 0 {
		return 19 * 3600, 22 * 3600 // live-verified client default
	}
	return o, c
}

func (p *EventPlayer) rbtCfg() [][3]int {
	p.RbtMu.Lock()
	defer p.RbtMu.Unlock()
	return p.rbtTimeCfg
}

// rbtActiveActivity is the live type-172 instance (whole-season window;
// phases and the daily gate are layered on top by the loop).

// "active" AND "reward" both count: after the season's EndTime the server
// flips the row to the reward-collect window (End..EndShow) but the shop,
// milestones, tree and cảnh-giới rungs are still claimable — the blind
// claim arms must stay alive there (owner 2026-11-08 "acc event không tự
// enter event" — the loop froze the moment the row left "active").
// Marching stays calendar-gated: rbtPhase returns rbtDone past begin+94h,
// so the battle branch never fires in the reward window.
func (p *EventPlayer) rbtActiveActivity() (int32, int64, bool) {
	now := time.Now()
	for _, e := range p.ActList.byType(rbtActivityType) {
		if st := e.status(now); st == "active" || st == "reward" {
			return e.ActivityID, e.BeginTime, true
		}
	}
	return 0, 0, false
}

// rbtNotePD caches the live battle counters (challenge attempts left +
// recover anchor) out of any playerData answer — map HUD support (parity
// with the Player engine).
func (p *EventPlayer) rbtNotePD(pd *proto.RebornTrialPlayerData) {
	if pd == nil {
		return
	}
	p.RbtMu.Lock()
	if pd.BattleNum != nil {
		p.rbtBattleNum = pd.GetBattleNum()
	}
	if pd.BattleRecoverTime != nil {
		p.rbtBattleRecov = pd.GetBattleRecoverTime()
	}
	p.RbtMu.Unlock()
}

func (p *EventPlayer) rbtSleepCtx(d time.Duration) bool {
	select {
	case <-p.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (p *EventPlayer) rbtPollWait() time.Duration {
	if p.rbtPoll > 0 {
		return p.rbtPoll
	}
	return rbtPoll
}

func (p *EventPlayer) rbtBeatGap() time.Duration {
	if p.rbtStatusGap > 0 {
		return p.rbtStatusGap
	}
	return rbtStatusGap
}

// ─── gameplay tick ───────────────────────────────────────────────────────────

// rbtPlayTick drives one in-window wake. Returns false only for connection
// loss; refusals are logged and leave the loop on its normal pacing.

func (p *EventPlayer) rbtPlayTick(actID int32, round int) bool {
	ea, err := p.rbtEnterActivity(actID)
	if err != nil || ea.GetRet() != 0 {
		return true
	}
	if ea.GetUnionId() == 0 {
		key := fmt.Sprintf("%d:%d", actID, round)
		if p.rbtQualNotified != key {
			p.rbtQualNotified = key
			p.sendNotify("🌀 Luân Hồi Bí Cảnh: tài khoản chưa có bang (hoặc chưa vào được vòng chiến) — bot chờ, không làm gì tốn kém. Vào bang rồi bật lại là bot tự chơi.")
		}
		return true
	}
	enter, err := p.rbtEnter(actID)
	if err != nil {
		return true
	}
	if enter.GetRet() != 0 {
		// 15001 phase-closed etc. The loop re-checks the clock each wake.
		log.Printf("[%s] [LHBC] ENTER ret %d (round %d) — chờ nhịp sau", p.Username, enter.GetRet(), round)
		return true
	}
	pd := enter.GetPlayerData()
	if pd == nil {
		return true
	}
	if ea.Round != nil {
		p.RbtMu.Lock()
		p.rbtRoundNo = ea.GetRound()
		p.RbtMu.Unlock()
	}
	p.rbtNotePD(pd)
	// The client's own free server-side batch first — one call, no items
	// (UseMagic setting is 0 = never). If the gate refuses (token score /
	// level), fall back silently to the manual march below.
	// AUTO_EXPLORE consumes a limited daily process count
	// (RENBORNTRIAL_AUTO_PROCESS_LIMIT — live ret 17204 once spent), so it
	// is fired exactly ONCE per activity:round; the manual march below
	// carries every later tick for free.
	autoKey := fmt.Sprintf("%d:%d", actID, round)
	p.RbtMu.Lock()
	usedAuto := p.rbtAutoUsed == autoKey
	p.RbtMu.Unlock()
	if !usedAuto {
		p.RbtMu.Lock()
		p.rbtAutoUsed = autoKey
		p.RbtMu.Unlock()
		if ar, err := p.rbtAutoExplore(actID); err == nil && ar.GetRet() != 0 {
			log.Printf("[%s] [LHBC] AUTO ret %d — tay chạy", p.Username, ar.GetRet())
		} else if err == nil && ar.GetRet() == 0 && ar.GetPlayerData() != nil {
			pd = ar.GetPlayerData()
			p.rbtNotePD(pd)
			if pd.GetLevel() > 0 {
				log.Printf("[%s] [LHBC] auto-explore: dừng loại %d, còn %d thể lực", p.Username, ar.GetStopType(), pd.GetHp())
			}
		}
	}
	p.rbtTreasureSweep(actID, pd)
	level := pd.GetLevel()
	exploreBudget, eventBudget := rbtStepBudget, rbtEventBudget
	for {
		ev := pd.GetEvent()
		if ev != nil && ev.GetStat() == 0 && eventBudget > 0 {
			eventBudget--
			log.Printf("[%s] [LHBC] gặp event type %d stat 0 (exp %d, hp %d, budget %d)", p.Username, ev.GetType(), pd.GetExp(), pd.GetHp(), eventBudget)
			if p.rbtEventMagic3(actID, pd) {
				continue
			}
			resolved := false
			opts := append([]*proto.RebornTrialEventOption{}, ev.GetOption()...)
			// selectIndex semantics (client-proven): EventOption.order is
			// 0-based (client renders e.order+1) and equals the array
			// position; other views send either and the server accepts both.
			// Owner strategy table (tinhnang.jpg) decides which option is
			// tried FIRST: bet/alchemy wager the HIGHEST exp affordable
			// ("chỉ chọn cái đắt tiền"), the rest follow the per-type
			// cycling preference — everything else stays the proven
			// affordability/refusal fan. Bets now also gate on balance (you
			// cannot wager exp you do not have). Exp is not an item
			// [[tytd-item-ban]]; betting stays owner-authorised.
			if p.rbtEvSeq == nil {
				p.rbtEvSeq = map[int32]int{}
			}
			// Bets keep the old no-gate rule: a bet node must NEVER SKIP —
			// the fan always fires, refusals retire options. Only the WALK
			// changed (expensive-first). Alchemy/explore costs stay gated.
			isBet := ev.GetType() == rbtEvBet
			rbtStrategySort(opts, rbtPreferOrder(p.rbtEvSeq, ev.GetType()), rbtEvExpensive[ev.GetType()])
			rejected := map[int32]bool{}
			for _, o := range opts {
				if rejected[o.GetOrder()] || (!isBet && o.GetCostExp() > pd.GetExp()) {
					continue
				}
				rsp, err := p.rbtSelectOption(actID, o.GetOrder())
				if err != nil {
					// Live 2026-10-09: some SMELT tiers get NO answer at all
					// (silence, not a refuse code) — the road only opens on
					// a higher tier that actually responds. Retire this
					// option and escalate; do not abandon the tick.
					log.Printf("[%s] [LHBC] SELECT order %d im lặng/tốc độ (%v) — lên option kế", p.Username, o.GetOrder(), err)
					rejected[o.GetOrder()] = true
					continue
				}
				if rsp.GetRet() == 0 {
					// PROVEN LIVE 2026-10-09 (luyện đan / SMELT): the server
					// can answer ret=0 while the event stays stat=0 and the
					// ledger fields are OMITTED (that option silently
					// no-ops — e.g. the cheapest alchemy tier the account
					// cannot actually feed). Such an answer resolves
					// NOTHING: retire the option, touch no fields, try the
					// next (higher-cost tiers really settle).
					if ev2 := rsp.GetEvent(); ev2 != nil && ev2.GetStat() == 0 {
						rejected[o.GetOrder()] = true
						log.Printf("[%s] [LHBC] option %d ret0-but-pending (type %d) — thử cửa sau", p.Username, o.GetOrder(), ev2.GetType())
						continue
					}
					if rsp.Exp != nil {
						pd.Exp = pb.Int64(rsp.GetExp())
					}
					if rsp.Hp != nil {
						pd.Hp = pb.Int32(rsp.GetHp())
					}
					if rsp.GetLevel() != 0 {
						pd.Level = pb.Int32(rsp.GetLevel())
					}
					if rsp.GetTotalExp() != 0 {
						pd.TotalExp = pb.Int64(rsp.GetTotalExp())
					}
					pd.Event = rsp.GetEvent()     // resolved event (stat≠0) or none
					p.rbtTreasureSweep(actID, pd) // rewards may have brought new goods
					if rsp.GetLevel() > level {
						p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: xe thư thương đạt cấp %d 🎉", rsp.GetLevel()))
						level = rsp.GetLevel()
					}
					resolved = true
					break
				}
				// Not affordable / refused: retire this option, try the next.
				rejected[o.GetOrder()] = true
			}
			if !resolved {
				sk, err := p.rbtSkipEvent(actID)
				if err != nil {
					return true
				}
				if sk.GetRet() != 0 {
					// Structural refusal (some nodes refuse to be skipped —
					// e.g. luyện đan while the server wants a real option).
					// Report exactly which event and refuse code.
					log.Printf("[%s] [LHBC] SKIP bị từ chối ret %d (event type %d, stat %d, value %d, param1 %d, param2 %d) — chờ nhịp sau",
						p.Username, sk.GetRet(), ev.GetType(), ev.GetStat(), ev.GetValue(), ev.GetParam1(), ev.GetParam2())
					return true
				}
				pd.Event = nil
			}
			continue
		}
		if exploreBudget <= 0 {
			break
		}
		exploreBudget--
		xp, err := p.rbtExplore(actID)
		if err != nil {
			return true
		}
		if xp.GetRet() != 0 {
			// Refuse speaks: out of energy for THIS floor's per-explore cost
			// (LevelConfig.cost grows with level) or the round just closed.
			// Owner 2026-10-09 "tự động cắn quả tăng thể lực": top stamina
			// with the energy fruit and retry — armed ONLY once the real
			// config folded rbtEnergyItem; 15001 (round closed) never eats
			// because fruit cannot fix it [[tytd-item-ban]].
			if xp.GetRet() != rbtRetPhaseClosed {
				old := pd.GetHp()
				if nhp, ate := p.rbtEatEnergy(actID, old); ate > 0 {
					pd.Hp = pb.Int32(nhp)
					log.Printf("[%s] [LHBC] cắn %d quả thể lực: %d → %d — chạy tiếp (ret %d)", p.Username, ate, old, nhp, xp.GetRet())
					continue
				}
			}
			log.Printf("[%s] [LHBC] march dừng: ret %d, thể lực còn %d, tầng %d — chờ hồi server", p.Username, xp.GetRet(), pd.GetHp(), pd.GetLevel())
			break
		}
		pd = xp.GetPlayerData()
		if pd == nil {
			break
		}
	}
	// Free once-per-round worship.
	if p.rbtWorshipped == nil {
		p.rbtWorshipped = map[string]bool{}
	}
	wkey := fmt.Sprintf("%d:%d", actID, round)
	if !p.rbtWorshipped[wkey] {
		w, err := p.rbtWorship(actID)
		if err == nil && w.GetRet() == 0 {
			p.rbtWorshipped[wkey] = true
			p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: đã bái %s vòng %d (+thưởng)", strings.TrimSpace(w.GetReward()), round))
		}
	}
	// 19h–22h GUILD WAR is deliberately NOT automated (user call
	// 2026-10-09): attacking rival unions stays the owner's manual play.
	// Guess rewards already achieved (claim only — the bot never bets).
	if gd, err := p.rbtGuessData(actID); err == nil && gd.GetRet() == 0 && !gd.GetIsReceive() && len(gd.GetRewardRankAchieve()) > 0 {
		if gr, err := p.rbtGuessReward(actID); err == nil && gr.GetRet() == 0 {
			p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: đã nhận thưởng đoán kết quả: %s", strings.TrimSpace(gr.GetRewards())))
		}
	}
	return true
}

// ─── union tree ──────────────────────────────────────────────────────────────

// rbtTreeCycle tends the union tree exactly like the bot's other events
// (Côn Lôn/Bồng Lai watering idiom): read the board (217220), harvest the
// fruit once ripe (217221, canReceive==1), and press the client's "Nhận
// nhanh" water-collect (217222 id=0 FORCED on the wire via
// rbtBonusItemAllBody — the watering BUTTON is a pure receive: it collects
// the water notices other members' donations made ready; a proto3-marshal
// id=0 lacks field 2 and is ignored by the server).
// The tree is NOT gated by the 19h–22h battle window, so the loop calls it
// on its own cadence in every phase. Donating liquid (217223) IS sent — all
// three water types, everything held — but ONLY for ids folded from
// RENBORNTRIAL_ITEM_ENERGY (owner 2026-10-10, exception to
// [[tytd-item-ban]]; no config folded = opcode never sent).

// rbtRankSweep claims the season RANK settlement reward (21006, answer
// 03ee) — twin of the Player arm (owner 2026-11-08 "thêm auto nhận
// reward, thêm vào cả player lẫn player_event"). One 21006 per folded
// rankConfig type (live 172: 1 = cá nhân, 2 = bang); config-armed —
// NOTHING folded ⇒ opcode NEVER sent. BLIND within the folded set:
// ret=0 + EMPTY rewards = no-op (never notify), ret=0 + rewards =
// actually granted → notify. Pure receive, never a spend [[tytd-item-ban]].
func (p *EventPlayer) rbtRankSweep(actID int32) {
	p.RbtMu.Lock()
	types := append([]int32(nil), p.rbtRankTypes...)
	p.RbtMu.Unlock()
	for _, rt := range types {
		body, err := pb.Marshal(&proto.ReqGetRankReward{ActivityId: actID, RankType: rt})
		if err != nil {
			return
		}
		data, err := p.rbtRT(ACTIVITY_RANK_REWARD_CMD, SERVER_ACTIVITY_RANK_REWARD, body)
		if err != nil {
			return
		}
		var rsp proto.RspGetRankReward
		if pb.Unmarshal(data, &rsp) != nil || rsp.GetRet() != 0 {
			continue
		}
		if rew := strings.TrimSpace(rsp.GetRewards()); rew != "" {
			log.Printf("[%s] [LHBC] nhận thưởng hạng loại %d: %s", p.Username, rt, rew)
			p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: đã nhận thưởng hạng %d — %s", rt, rew))
		}
	}
}

func (p *EventPlayer) rbtTreeCycle(actID int32) {
	// BLIND poll-claim (owner 2026-11-07: "cần poll mà nhận, không cần đợi
	// check, 5 phút nhận 1 lần"): every claim op fires each cycle — the
	// server's ret≠0 is the only arbiter of "nothing ready". The old
	// 217220 pre-gate (canReceive/ready-count) could starve a claim when the
	// board read lagged the donation that made it ready.
	// Donate first: our own fresh notices can then be harvested/claimed in
	// the same cycle (owner 2026-10-10 "phát sạch cả 3 loại").
	if n := p.rbtLiquidGrant(actID); n > 0 {
		p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: đã phát %d chai nước cây bang (đủ cả 3 loại)", n))
	}
	if fr, err := p.rbtBonusResult(actID); err == nil && fr.GetRet() == 0 && (fr.GetExp() > 0 || strings.TrimSpace(fr.GetReward()) != "") {
		log.Printf("[%s] [LHBC] cây bang: thu quả (+%d exp)", p.Username, fr.GetExp())
		p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: cây bang — thu quả: %s", strings.TrimSpace(fr.GetReward())))
	}
	if wi, err := p.rbtClaimBonusAll(actID); err == nil && wi.GetRet() == 0 && strings.TrimSpace(wi.GetReward()) != "" {
		log.Printf("[%s] [LHBC] cây bang: nhận suất nước", p.Username)
		p.sendNotify(fmt.Sprintf("🌀 Luân Hồi Bí Cảnh: đã nhận nước cây bang: %s", strings.TrimSpace(wi.GetReward())))
	}
}

// ─── guild war (auto) ────────────────────────────────────────────────────────

// rbtChallenge attacks one rival base (217233). Costs one free attempt,
// NEVER an item — the ticket (USE_ITEM) stays the command-only
// "/luanhoi_ve" exception [[tytd-item-ban]]. The server's rsp.free is the
// remaining-attempt truth (same discipline as the Player twin).
func (p *EventPlayer) rbtChallenge(actID int32, unionID int64) (*proto.RebornTrialChallengeResp, error) {
	body := rbtActivityBody(actID)
	body = protowire.AppendTag(body, 2, protowire.VarintType)
	body = protowire.AppendVarint(body, uint64(unionID))
	data, err := p.rbtRT(RBT_BATTLE_CHALLENGE_CMD, SERVER_RBT_BATTLE_CHALLENGE, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialChallengeResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	if rsp.GetRet() == 0 {
		p.RbtMu.Lock()
		if rsp.Free != nil {
			p.rbtFreeAttempts = rsp.GetFree()
			p.rbtBattleNum = rsp.GetFree()
		}
		p.RbtMu.Unlock()
	}
	return &rsp, nil
}

// rbtWarOrderOne executes ONE player bang-chiến order on this event
// account. Bang chiến fighting law (owner 2026-11-08 "đánh bang chiến chỉ
// đánh ở event thôi, theo lệnh của player trên mini map"): event accounts
// are the ONLY combatants and they attack ONLY when the player taps ⚔️ on
// the mini map (webLuanHoiOrder) or orders /luanhoi_attack — there is no
// timer-driven auto-fight anywhere. FREE attempts only: the ENTER (pure
// read) refreshes the meter and a 0 reading refuses locally the way the
// client's reqBattle gate does — the server happily no-ops 217233 at 0
// attempts with ret=0 all-zero (live-proven 2026-10-11), and the bot NEVER
// bites tickets ([[tytd-item-ban]]; USE_ITEM stays command-only
// "/luanhoi_ve"). ret≠0 refusals surface as errors for the caller.
func (p *EventPlayer) rbtWarOrderOne(unionID int64) (*proto.RebornTrialChallengeResp, error) {
	actID, _, active := p.rbtActiveActivity()
	if !active {
		return nil, fmt.Errorf("chưa thấy Luân Hồi Bí Cảnh mở trên acc này")
	}
	if int64(p.ClanID) == unionID {
		return nil, fmt.Errorf("đó là bang của acc — không tự đánh mình")
	}
	en, err := p.rbtEnter(actID)
	if err != nil {
		return nil, fmt.Errorf("không kiểm tra được lượt đánh (%v)", err)
	}
	p.rbtNotePD(en.GetPlayerData())
	p.RbtMu.Lock()
	num := p.rbtBattleNum
	p.RbtMu.Unlock()
	if num < 1 {
		return nil, fmt.Errorf("hết lượt đánh — bot không tự cắn vé (chủ lệnh /luanhoi_ve trên acc chính)")
	}
	cr, err := p.rbtChallenge(actID, unionID)
	if err != nil {
		return nil, err
	}
	if cr.GetRet() != 0 {
		return nil, fmt.Errorf("server từ chối tấn công (ret %d — ngoài giờ chiến / mục tiêu sai)", cr.GetRet())
	}
	return cr, nil
}

// ─── loop ────────────────────────────────────────────────────────────────────

// rebornTrialAutoLoop is the RebornTrial counterpart of skyTradeAutoLoop:
// the opt-in gate is the Player field p.AutoRebornTrial (owner 2026-10-27:
// event gates live ONLY on the Player object), so /auto_luanhoi false stops
// the caravan between ticks, phases and the daily window are computed
// locally but the server's refuse codes always overrule our calendar.
