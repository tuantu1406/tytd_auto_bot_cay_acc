package tytd_go

/*
Thương Minh Yêu Vực (CelestialSeaWar, type 349) for EVENT accounts — a
faithful clone of the Player engine (player_seawar.go) onto EventPlayer via
the shared swPlayTick core. Event users have no opt-in switch: the loop
starts the moment the account is online (Run() → seaWarEventLoop), the same
"login is all it takes" policy as LHBC / godIslandStay / bgAuto. All rules
carry over verbatim: free actions only, energy item NEVER auto-spent
(button-only [[tytd-item-ban]]), forge prefers final-damage balls, the
guild-war family 234931..234944 is never automated, blind-poll claims —
server ret is the sole arbiter [[tytd-seawar-port]].

Transport differences vs Player: answers ride a dedicated seat map
(swChans under SwMu) fed by the event reader via swHandleFrameEP, mirroring
rbtRT/rbtHandleFrameEP exactly. EP alerts use forceNotify — event sessions
run with Notify=false, which would silently kill sendNotify.
*/

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

var errSwGone = errors.New("seawar: connection gone")
var errSwTimeout = errors.New("seawar: answer timeout")
var errSwBusy = errors.New("seawar: request already in flight")

// swRT is the EventPlayer seat transport (rbtRT idiom — one seat per answer
// hex; the loop goroutine is the only caller).
func (p *EventPlayer) swRT(cmd, wantType string, body []byte) ([]byte, error) {
	p.SwMu.Lock()
	if p.swChans == nil {
		p.swChans = map[string]chan []byte{}
	}
	if p.swChans[wantType] != nil {
		p.SwMu.Unlock()
		return nil, errSwBusy
	}
	ch := make(chan []byte, 1)
	p.swChans[wantType] = ch
	p.SwMu.Unlock()
	defer func() {
		p.SwMu.Lock()
		if p.swChans[wantType] == ch {
			delete(p.swChans, wantType)
		}
		p.SwMu.Unlock()
	}()
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
	case <-p.ctx.Done():
		return nil, errSwGone
	case <-time.After(5 * time.Second):
		return nil, errSwGone
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(swReqTimeout):
		return nil, errSwTimeout
	case <-p.ctx.Done():
		return nil, errSwGone
	}
}

// swHandleFrameEP is the event reader's entry point for every 349 answer
// this session owns: refusals get logged, the waiting seat gets the bytes.
func (p *EventPlayer) swHandleFrameEP(msgType string, data []byte) {
	switch msgType {
	case SERVER_SW_ENTER:
		var rsp proto.SeaWarEnterResp
		if pb.Unmarshal(data, &rsp) == nil && rsp.GetRet() != 0 {
			p.swLog("[TMYV] vào hoạt động bị từ chối (code %d)", rsp.GetRet())
		}
	case SERVER_SW_ENTER_GAME:
		var rsp proto.SeaWarEnterGameResp
		if pb.Unmarshal(data, &rsp) == nil && rsp.GetRet() != 0 {
			p.swLog("[TMYV] vào bảng bị từ chối (code %d)", rsp.GetRet())
		}
	case SERVER_SW_USE_ITEM:
		// Button verdict — 1-line alert, success stays silent (async order
		// fire law). forceNotify: EP sessions have Notify=false. Muted while
		// an eat-all loop runs: its stop-verdict is the expected "bag dry"
		// signal, one alert per clone would be spam (web card, 2026-11-09).
		var rsp proto.SeaWarUseItemResp
		if pb.Unmarshal(data, &rsp) == nil && rsp.GetRet() != 0 {
			if p.swEatMute.Load() {
				p.swLog("[TMYV] cắn quả: server từ chối (code %d) — coi như hết quả", rsp.GetRet())
			} else {
				p.forceNotify(fmt.Sprintf("🌊 [%s] dùng vật phẩm thể lực Thương Minh Yêu Vực thất bại (code %d)",
					p.Username, rsp.GetRet()))
			}
		}
	case SERVER_SW_ASSIST_SYNC, SERVER_SW_ASSIST_SYNC_RAW:
		// Teammate assist call pushed (234913) — ring the loop's wake bell;
		// the board itself stays poll-only (234909). Live: arrives as raw
		// low16 "95a1".
		p.swLog("[TMYV] có tin đồng đội cần hỗ trợ — quét bảng ngay")
		p.swPingWake()
		return
	case SERVER_SW_CHALLENGE_INFO_SYNC:
		// 234932 challenge push → war-map fight ring (Player twin parity).
		var rsp proto.SeaWarChallengeInfoSync
		if pb.Unmarshal(data, &rsp) == nil {
			p.swNoteFight(&rsp)
		}
		return
	}
	p.SwMu.Lock()
	ch := p.swChans[msgType]
	p.SwMu.Unlock()
	if ch != nil {
		select {
		case ch <- data:
		default:
		}
	}
}

func (p *EventPlayer) swLog(format string, args ...any) {
	log.Printf("["+p.Username+"] "+format, args...)
}

func (p *EventPlayer) swName() string { return p.Username }

// swActiveActivity — same active||reward acceptance as the Player twin.
func (p *EventPlayer) swActiveActivity() (int32, bool) {
	now := time.Now()
	for _, e := range p.ActList.byType(swActivityType) {
		if st := e.status(now); st == "active" || st == "reward" {
			return e.ActivityID, true
		}
	}
	return 0, false
}

// swMergeConfig folds the type-349 slice of a 21003 answer (see the Player
// twin for semantics).
func (p *EventPlayer) swMergeConfig(data []byte) {
	var rsp proto.RspGetActivityDetail
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return
	}
	dc := rsp.GetActivity().GetDetailConfig()
	cc := dc.GetCommonConfig()
	if cc.GetMainConfig().GetType() != swActivityType {
		return
	}
	var energyItem int32
	var liquidItems []int32
	var highPvp int32
	for _, pc := range cc.GetParamConfig() {
		if pc.GetParam() == "SEAWAR_ENERGY_ITEM" {
			if n, err := atoiTrim(pc.GetValue()); err == nil && n > 0 {
				energyItem = n
			}
		}
		// War-lệnh capacity (client refreshCurChallengeTimes): remaining =
		// (type==0 ? 1 : SEAWAR_HIGH_PVP_TIMES) - challengeTimes.
		if pc.GetParam() == "SEAWAR_HIGH_PVP_TIMES" {
			if n, err := atoiTrim(pc.GetValue()); err == nil && n > 0 {
				highPvp = n
			}
		}
		// 3 linh lộ tiers — rbt-style fold, see Player twin.
		if pc.GetParam() == "SEAWAR_ITEM_ENERGY" {
			for _, part := range strings.Split(pc.GetValue(), ";") {
				kv := strings.Split(strings.TrimSpace(part), "|")
				if n, err := atoiTrim(kv[0]); err == nil && n > 0 {
					liquidItems = append(liquidItems, n)
				}
			}
		}
	}
	var ballEffect, ballType, regionEnergy map[int32]int32
	var regionCond map[int32]string
	var eventCfgIds map[int32]bool
	var helpBoxNeed int32
	if len(dc.GetUniqueConfig()) > 0 {
		var sync proto.SeaWarConfigSync
		if pb.Unmarshal(dc.GetUniqueConfig(), &sync) == nil {
			for _, b := range sync.GetSeaWarBuffConfigMsgs() {
				if ballEffect == nil {
					ballEffect, ballType = map[int32]int32{}, map[int32]int32{}
				}
				ballEffect[b.GetId()] = b.GetEffect()
				ballType[b.GetId()] = b.GetType()
			}
			for _, r := range sync.GetSeaWarRegionConfigMsgs() {
				if regionEnergy == nil {
					regionEnergy, regionCond = map[int32]int32{}, map[int32]string{}
				}
				regionEnergy[r.GetId()] = r.GetEnergy()
				regionCond[r.GetId()] = r.GetConsumeCondition()
			}
			// event-id classifier for consumeCondition (swRegionConditions:
			// event side vs phi-thạch side) + OPEN_HELP_BOX param.
			for _, ev := range sync.GetSeaWarEventConfigMsgs() {
				if eventCfgIds == nil {
					eventCfgIds = map[int32]bool{}
				}
				eventCfgIds[ev.GetId()] = true
				if ev.GetEventType() == swEvHelpBox {
					if n, err := atoiTrim(ev.GetParam()); err == nil && n > 0 {
						helpBoxNeed = n
					}
				}
			}
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
	p.SwMu.Lock()
	if energyItem > 0 {
		p.swEnergyItem = energyItem
	}
	if len(liquidItems) > 0 {
		p.swLiquidItems = liquidItems
	}
	if ccfg := cc.GetConditionConfig(); len(ccfg) > 0 {
		p.swCondCfgs = ccfg
	}
	if ballEffect != nil {
		p.swBallEffect, p.swBallType = ballEffect, ballType
	}
	if regionEnergy != nil {
		p.swRegionEnergy, p.swRegionCond = regionEnergy, regionCond
	}
	if eventCfgIds != nil {
		p.swEventCfgIds = eventCfgIds
	}
	if helpBoxNeed > 0 {
		p.swHelpBoxNeed = helpBoxNeed
	}
	if highPvp > 0 {
		p.swHighPvp = highPvp
	}
	if len(mallRows) > 0 {
		p.swMallRows = mallRows
	}
	p.swMallBought = bought
	p.SwMu.Unlock()
}

// swEatAll — the web "🍏 Cắn quả — tất cả acc" card button (tab Sự kiện,
// owner 2026-11-09): drain the account's whole energy-fruit bag through the
// button-only 234922 path ([[tytd-item-ban]] exception — button only, the
// auto engine never calls this). One request per round at the validated cap
// (num=20, same ceiling /thuongminh_suc enforces), riding the swRT seat for
// the real verdict — blind-fire law: the SERVER decides when the bag is dry
// (any ret≠0 ends the loop), the bot never pre-counts a bag it does not
// track. Cap 10 rounds (200 quả) backstops a pathological ret==0 loop.
// Refusals stay LOG-only while the loop runs (swEatMute): the expected
// "hết quả" stop must not spam one alert per clone; the async order fire law
// keeps success silent anyway. Presses while an eat is in flight no-op.
func (p *EventPlayer) swEatAll() {
	if !p.Running {
		return
	}
	if !p.swEatBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer p.swEatBusy.Store(false)
		actID, ok := p.swActiveActivity()
		if !ok {
			p.swLog("[TMYV] cắn quả: hoạt động đã đóng — bỏ qua")
			return
		}
		p.swEatMute.Store(true)
		defer p.swEatMute.Store(false)
		const swEatRounds, swEatPerRound = 10, int32(20)
		used := int32(0)
		for round := 0; round < swEatRounds; round++ {
			body, err := pb.Marshal(&proto.SeaWarUseItemReq{ActivityId: actID, Num: swEatPerRound})
			if err != nil {
				return
			}
			data, err := p.swRT(SW_USE_ITEM_CMD, SERVER_SW_USE_ITEM, body)
			if err != nil {
				p.swLog("[TMYV] cắn quả: %v — dừng", err)
				return
			}
			var rsp proto.SeaWarUseItemResp
			if pb.Unmarshal(data, &rsp) != nil || rsp.GetRet() != 0 {
				return // server says hết (or refused) — sole arbiter
			}
			used += swEatPerRound
		}
		p.swLog("[TMYV] cắn quả: chạm trần %d vòng×%d (≥%d quả) — dừng", swEatRounds, swEatPerRound, used)
	}()
}

// swUseItemOrder — the owner's button, fired per event account
// (eventOrderBroadcast). Fire-and-forget like the other order paths: the
// 886a verdict alerts on refusal via swHandleFrameEP, success is silent.
func (p *EventPlayer) swUseItemOrder(num int32) error {
	if !p.Running {
		return errors.New("chưa login")
	}
	actID, ok := p.swActiveActivity()
	if !ok {
		return errSwClosed
	}
	if num <= 0 {
		num = 1
	}
	body, err := pb.Marshal(&proto.SeaWarUseItemReq{ActivityId: actID, Num: num})
	if err != nil {
		return err
	}
	select {
	case p.writeChan <- pack.Pack(SW_USE_ITEM_CMD, body, p.UID):
	case <-p.ctx.Done():
		return errSwGone
	case <-time.After(5 * time.Second):
		return errSwGone
	}
	p.swLog("[TMYV] lệnh dùng %d vật phẩm thể lực", num)
	return nil
}

// ─── EventPlayer swAPI implementation ────────────────────────────────────────

func (p *EventPlayer) swEnter(actID int32) (*proto.SeaWarEnterResp, error) {
	data, err := p.swRT(SW_ENTER_CMD, SERVER_SW_ENTER, swCommonBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarEnterResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swEnterGame(actID int32) (*proto.SeaWarEnterGameResp, error) {
	data, err := p.swRT(SW_ENTER_GAME_CMD, SERVER_SW_ENTER_GAME, swCommonBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarEnterGameResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swExplore(actID int32) (*proto.SeaWarExploreResp, error) {
	data, err := p.swRT(SW_EXPLORE_CMD, SERVER_SW_EXPLORE, swCommonBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarExploreResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swHandle(actID, evID, action int32, id int64) (*proto.SeaWarHandleEventResp, error) {
	body, err := pb.Marshal(&proto.SeaWarHandleEventReq{ActivityId: actID, EventId: evID, Action: pb.Int32(action), Id: pb.Int64(id)})
	if err != nil {
		return nil, err
	}
	data, err := p.swRT(SW_HANDLE_EVENT_CMD, SERVER_SW_HANDLE_EVENT, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarHandleEventResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swWorship(actID int32) {
	data, err := p.swRT(SW_WORSHIP_CMD, SERVER_SW_WORSHIP, swCommonBody(actID))
	if err != nil {
		return
	}
	var rsp proto.SeaWarWorshipResp
	if pb.Unmarshal(data, &rsp) == nil && rsp.GetRet() != 0 {
		p.swLog("[TMYV] bái bị từ chối (code %d)", rsp.GetRet())
	}
}

func (p *EventPlayer) swWelfare(actID int32) (*proto.SeaWarUnionWelfareResp, error) {
	data, err := p.swRT(SW_UNION_WELFARE_CMD, SERVER_SW_UNION_WELFARE, swCommonBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarUnionWelfareResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swWelfareReward(actID int32) {
	_, _ = p.swRT(SW_UNION_WELFARE_REWARD_CMD, SERVER_SW_UNION_WELFARE_REWARD, swCommonBody(actID))
}

func (p *EventPlayer) swReceiveLiquid(actID int32, id int64) {
	body, err := pb.Marshal(&proto.SeaWarUnionLiquidRewardReq{ActivityId: actID, Id: pb.Int64(id)})
	if err != nil {
		return
	}
	_, _ = p.swRT(SW_UNION_RECEIVE_LIQUID_CMD, SERVER_SW_UNION_RECEIVE_LIQUID, body)
}

// swCondSweep — EP twin of the Player nạp-bang milestone sweep (see the
// Player twin for law/semantics): type-84 / 2xxxx rows → per-id 21045,
// blind, 5-min cadence, rewards (linh lộ) notified via forceNotify —
// Notify=false on event accounts would otherwise kill sendNotify
// [[tytd-async-order-fire]].
func (p *EventPlayer) swCondSweep(actID int32) {
	now := swNowFunc()
	p.SwMu.Lock()
	if now.Before(p.swCondNext) {
		p.SwMu.Unlock()
		return
	}
	p.swCondNext = now.Add(5 * time.Minute)
	cfgs := append([]*proto.ActivityConditionConfig{}, p.swCondCfgs...)
	p.SwMu.Unlock()
	if len(cfgs) == 0 {
		if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
			if data, err := p.swRT(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err == nil {
				p.swMergeConfig(data)
			}
		}
		p.SwMu.Lock()
		cfgs = append([]*proto.ActivityConditionConfig{}, p.swCondCfgs...)
		p.SwMu.Unlock()
	}
	seen := map[int32]bool{}
	for _, cfg := range cfgs {
		cid := cfg.GetConditionId()
		if cfg.GetType() != 84 && cid < 20000 {
			continue
		}
		if seen[cid] {
			continue
		}
		seen[cid] = true
		a := cfg.GetActivityId()
		if a == 0 {
			a = actID
		}
		rewards, err := p.swClaimCond(a, cid)
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
		p.forceNotify(fmt.Sprintf("🌊 Thương Minh Yêu Vực: đã nhận quà mốc nạp bang %s — %s", name, rewards))
	}
}

func (p *EventPlayer) swClaimCond(actID, condID int32) (string, error) {
	body, err := pb.Marshal(&proto.ReceiveCrossUnionRechargeRewardReq{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(condID)})
	if err != nil {
		return "", err
	}
	data, err := p.swRT(ACTIVITY_CROSS_RECHARGE_CMD, SERVER_ACTIVITY_CROSS_RECHARGE, body)
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

// swSendLiquid — EP twin of the donate press 234917 (authorized AUTO spend
// [[tytd-item-ban]] (13); see Player twin for the law).
func (p *EventPlayer) swSendLiquid(actID, itemID int32) (int32, error) {
	body, err := pb.Marshal(&proto.SeaWarUnionLiquidSendReq{ActivityId: actID, ItemId: pb.Int32(itemID), Num: pb.Int32(1)})
	if err != nil {
		return -1, err
	}
	data, err := p.swRT(SW_UNION_SEND_LIQUID_CMD, SERVER_SW_UNION_SEND_LIQUID, body)
	if err != nil {
		return -1, err
	}
	var rsp proto.SeaWarUnionLiquidSendResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return -1, err
	}
	return rsp.GetRet(), nil
}

func (p *EventPlayer) swLiquidItemIDs() []int32 {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if len(p.swLiquidItems) > 0 {
		out := make([]int32, len(p.swLiquidItems))
		copy(out, p.swLiquidItems)
		return out
	}
	return []int32{105531, 105532, 105533} // all three linh lộ tiers (see Player twin)
}

func (p *EventPlayer) swDonatePaused(now time.Time) bool {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	return now.Before(p.swDonatePauseUntil)
}

func (p *EventPlayer) swDonatePause(until time.Time) {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	p.swDonatePauseUntil = until
}

// ─── chiến trường khiêu chiến (234931..234938) — ORDER-ONLY ─────────────────
// EP twins of the war packets (Player twin carries the law comments). The
// auto engine never sends CHALLENGE_BATTLE — only swWarOrderOne below does,
// and only when the player taps ⚔️ on the war map (webSeaWarWarOrder).

func (p *EventPlayer) swChallengeList(actID int32) (*proto.SeaWarChallengeListResp, error) {
	data, err := p.swRT(SW_CHALLENGE_LIST_CMD, SERVER_SW_CHALLENGE_LIST, swCommonBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarChallengeListResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swEnterChallenge(actID int32, unionID int64) (*proto.SeaWarEnterChallengeResp, error) {
	body, err := pb.Marshal(&proto.SeaWarEnterChallengeReq{ActivityId: actID, UnionId: unionID})
	if err != nil {
		return nil, err
	}
	data, err := p.swRT(SW_ENTER_CHALLENGE_CMD, SERVER_SW_ENTER_CHALLENGE, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarEnterChallengeResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swRoomInfo(actID int32, unionID int64, roomID int32) (*proto.SeaWarRoomInfoResp, error) {
	body, err := pb.Marshal(&proto.SeaWarRoomInfoReq{ActivityId: actID, UnionId: unionID, RoomId: roomID})
	if err != nil {
		return nil, err
	}
	data, err := p.swRT(SW_CHALLENGE_INFO_CMD, SERVER_SW_CHALLENGE_INFO, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarRoomInfoResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swChallenge(actID int32, unionID int64, roomID int32, playerID int64) (*proto.SeaWarChallengeResp, error) {
	body, err := pb.Marshal(&proto.SeaWarChallengeReq{ActivityId: actID, UnionId: unionID, RoomId: roomID, PlayerId: playerID})
	if err != nil {
		return nil, err
	}
	data, err := p.swRT(SW_CHALLENGE_BATTLE_CMD, SERVER_SW_CHALLENGE_BATTLE, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarChallengeResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// swDeclaration — EP twin: 宣战 234944, UNION-level (one member declares
// for the whole bang, owner 2026-11-11) — the war order path NEVER presses
// it; vé 105404/105405 stay item-banned [[tytd-item-ban]].
func (p *EventPlayer) swDeclaration(actID int32, unionID int64, typ int32, isFree bool) (*proto.SeaWarDeclarationResp, error) {
	body, err := pb.Marshal(&proto.SeaWarDeclarationReq{
		ActivityId: pb.Int32(actID), UnionId: pb.Int64(unionID),
		Type: pb.Int32(typ), IsFree: pb.Bool(isFree),
	})
	if err != nil {
		return nil, err
	}
	data, err := p.swRT(SW_DECLARATION_CMD, SERVER_SW_DECLARATION, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarDeclarationResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// swNoteFight / swAttacksSnapshot — EP twin of the war fight ring (SwMu).
func (p *EventPlayer) swNoteFight(rsp *proto.SeaWarChallengeInfoSync) {
	tu := rsp.GetTargetUnionId()
	if tu == 0 {
		return
	}
	p.SwMu.Lock()
	p.swAttacks = append(p.swAttacks, swAttackRec{
		Name: rsp.GetName(), Union: rsp.GetUnionName(),
		TargetID: tu, Damage: rsp.GetDamage(), At: time.Now(),
	})
	if cut := time.Now().Add(-swAttackWindow); len(p.swAttacks) > 0 {
		keep := p.swAttacks[:0]
		for _, a := range p.swAttacks {
			if a.At.After(cut) {
				keep = append(keep, a)
			}
		}
		p.swAttacks = keep
	}
	if len(p.swAttacks) > swFightLogCap {
		p.swAttacks = p.swAttacks[len(p.swAttacks)-swFightLogCap:]
	}
	p.SwMu.Unlock()
}

func (p *EventPlayer) swAttacksSnapshot() []swAttackRec {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	return append([]swAttackRec{}, p.swAttacks...)
}

// swWarTarget is one planned blind attack: room + defender to challenge.
type swWarTarget struct {
	RoomID   int32
	PlayerID int64
	Name     string
}

// swWarMaxRooms/swWarMaxPress bound ONE order on one account: a base is
// swept room by room, each defender pressed once — enough to throw the whole
// fleet at the chosen house without camping the war socket.
const (
	swWarMaxRooms = 4
	swWarMaxPress = 8
)

// swLenhLit mirrors the client's isChallenge gate (CelestialSeaWarMgrField-
// Mgr.js): isChallenge(type,times) = type0 ? times>0 : times>=HIGH_PVP_TIMES
// — TRUE means the lệnh is EXHAUSTED (client dims it, sorts it last). chal-
// lengeTimes counts attacks already made on the lệnh (client does
// curChallengeInfo.challengeTimes++ after each won Challenge), so a row is
// attackable — lit — exactly when !isChallenge: lệnh thường (type 0) needs
// times==0, lệnh cao cấp (type 1) needs times < highPvp. highPvp<=0 (config
// not folded) → blind: cao cấp rows stay attackable, server ret arbitrates.
func swLenhLit(typ, times, highPvp int32) bool {
	if typ == 1 {
		return highPvp <= 0 || times < highPvp
	}
	return times <= 0
}

// swWarPlan turns the ENTER answer (+ per-room defender pulls + the board's
// live challenge rows) into the attack queue. BLIND-ORDER LAW (owner
// 2026-11-11 "bấm vào nhà chọn đánh là cả đám đánh, không cần check xem có
// lượt hay không"): freeNormalTimes is never read as an account gate — the
// server's ret on 234938 is the sole arbiter of whether a press sticks.
// Rooms come from declarationInfos AND the board's beChallengedInfos rows
// for this union (deduped, first-seen order, cap swWarMaxRooms); a room that
// yielded no fresh defender falls back to the declarationInfos rows that
// name a player in it.
func swWarPlan(ent *proto.SeaWarEnterChallengeResp, rooms map[int32][]*proto.SeaWarBeChallengedInfo,
	infos []*proto.SeaWarChallengeInfoSync, unionID int64, maxRooms, maxPress int, highPvp int32) []swWarTarget {
	if ent == nil || maxRooms <= 0 || maxPress <= 0 {
		return nil
	}
	// LỆNH DIM LAW v2 (owner 2026-11-11 "danh sách đánh rồi vẫn hiện nhưng
	// tối màu → response có trường đánh rồi" + "tao thấy mày vẫn đánh theo
	// lệnh thứ 2"): declarationInfos.challengeTimes COUNTS ATTACKS MADE —
	// the client grays a row once isChallenge fires (type0 at times>0,
	// type1 at times>=SEAWAR_HIGH_PVP_TIMES) and sorts it last. The first
	// dim-law port read the counter backwards (kept times>0 rows) so the
	// bot skipped lệnh #1 and fought the exhausted #2. PRIMARY plan = LIT
	// lệnh rows only (swLenhLit, first-seen room order, room/press caps).
	// Only when NO lit lệnh exists at all do the blind discovery rows (live
	// challenge rows, then 234937 roomInfo defenders) plan — server ret
	// stays the arbiter there. An exhausted row never plans.
	var out []swWarTarget
	pressed := map[int64]bool{}
	exhaustedPid := map[int64]bool{} // dim rows: their defender is off-limits everywhere
	roomSeen := map[int32]bool{}
	roomOK := func(rid int32) bool {
		if roomSeen[rid] {
			return true
		}
		if len(roomSeen) >= maxRooms {
			return false
		}
		roomSeen[rid] = true
		return true
	}
	for _, d := range ent.GetDeclarationInfos() {
		pid := d.GetPlayerBaseDataMsg().GetPlayerId()
		if pid == 0 {
			continue
		}
		if !swLenhLit(d.GetType(), d.GetChallengeTimes(), highPvp) {
			exhaustedPid[pid] = true // tối màu — đã đánh rồi
			continue
		}
		if len(out) >= maxPress || !roomOK(d.GetRoomId()) {
			continue
		}
		if pressed[pid] {
			continue
		}
		pressed[pid] = true
		out = append(out, swWarTarget{RoomID: d.GetRoomId(), PlayerID: pid, Name: d.GetPlayerBaseDataMsg().GetNickName()})
	}
	if len(out) > 0 {
		return out
	}
	// Blind fallback (no lit lệnh board): rows the client paints from the
	// board/room pulls. A defender whose declaration row is exhausted
	// (tối màu) stays off-limits here too.
	liveByRoom := map[int32][]*proto.SeaWarBeChallengedInfo{}
	for _, in := range infos {
		if in.GetTargetUnionId() != unionID {
			continue
		}
		if b := in.GetBeChallengedInfo(); b != nil && b.GetPlayerBaseDataMsg().GetPlayerId() != 0 {
			liveByRoom[b.GetRoomId()] = append(liveByRoom[b.GetRoomId()], b)
		}
	}
	var order []int32
	seen := map[int32]bool{}
	addRoom := func(rid int32) {
		if seen[rid] || len(order) >= maxRooms {
			return
		}
		seen[rid] = true
		order = append(order, rid)
	}
	for rid := range liveByRoom {
		addRoom(rid)
	}
	for rid := range rooms {
		addRoom(rid)
	}
	press := func(rid int32, b *proto.SeaWarBeChallengedInfo) {
		if len(out) >= maxPress {
			return
		}
		pid := b.GetPlayerBaseDataMsg().GetPlayerId()
		if pid == 0 || pressed[pid] || exhaustedPid[pid] {
			return
		}
		pressed[pid] = true
		out = append(out, swWarTarget{RoomID: rid, PlayerID: pid, Name: b.GetPlayerBaseDataMsg().GetNickName()})
	}
	for _, rid := range order {
		for _, b := range liveByRoom[rid] {
			press(rid, b)
		}
		for _, b := range rooms[rid] {
			press(rid, b)
		}
	}
	return out
}

// swWarInfosOf the union's live challenge rows ride the CHALLENGE_LIST
// answer; a fresh pull is cheap and keeps the order self-contained.
func swWarInfosOf(list *proto.SeaWarChallengeListResp, unionID int64) []*proto.SeaWarChallengeInfoSync {
	if list == nil {
		return nil
	}
	var out []*proto.SeaWarChallengeInfoSync
	for _, in := range list.GetInfos() {
		if in.GetTargetUnionId() == unionID {
			out = append(out, in)
		}
	}
	return out
}

// swWarOrderOne executes ONE war-map order on this event account: enter the
// chosen union base and blind-fire one CHALLENGE_BATTLE per defender. Bang
// chiến fighting law (same as LHBC 2026-11-08): event accounts are the ONLY
// combatants and they attack ONLY on the player's map tap — no timer ever
// calls this. No attempt check anywhere (owner law), no items spent
// (attacking is free; [[tytd-item-ban]] intact — fruit/ticket paths untouched).
func (p *EventPlayer) swWarOrderOne(unionID int64) (string, error) {
	actID, ok := p.swActiveActivity()
	if !ok {
		return "", fmt.Errorf("chưa thấy Thương Minh Yêu Vực mở trên acc này")
	}
	if p.ClanID != 0 && int64(p.ClanID) == unionID {
		return "", fmt.Errorf("đó là bang của acc — không tự đánh mình")
	}
	// BLIND-ORDER LAW (owner 2026-11-11): "cứ bấm là đánh" — no ACCOUNT-level
	// attempt/item gate anywhere; the server ret is the sole arbiter.
	// LỆNH LAW (owner 2026-11-11, corrected twice): DECLARATION is UNION-
	// level — one member declaring opens the base for the whole bang, the
	// bot never presses it. Per-row truth lives in the response: declaration-
	// Infos.challengeTimes COUNTS ATTACKS ALREADY MADE on the lệnh (client
	// ++'s it after each fight) — the row dims (tối màu) once isChallenge
	// fires: lệnh thường at times>0, cao cấp at times>=SEAWAR_HIGH_PVP_TIMES.
	// Pressing a dim row was the "chỉ đánh lệnh đầu rồi báo hết lượt" bug;
	// reading the counter backwards was "vẫn đánh theo lệnh thứ 2". So: RE-
	// ENTER the base (fresh rows, client reqEnterRefresh idiom) before EVERY
	// press and only press LIT rows (swLenhLit). ENTER ret is never a gate;
	// unseen roomIds answer instantly (ret=34930), so sweeping 1..max is safe.
	var infos []*proto.SeaWarChallengeInfoSync
	if list, err := p.swChallengeList(actID); err == nil && list.GetRet() == 0 {
		infos = swWarInfosOf(list, unionID)
	}
	rooms := map[int32][]*proto.SeaWarBeChallengedInfo{}
	if first, err := p.swEnterChallenge(actID, unionID); err == nil {
		roomIDs := swWarRoomIDs(first, swWarMaxRooms)
		if len(roomIDs) == 0 {
			for r := int32(1); r <= int32(swWarMaxRooms); r++ {
				roomIDs = append(roomIDs, r)
			}
		}
		for _, rid := range roomIDs {
			if ri, err := p.swRoomInfo(actID, unionID, rid); err == nil && ri.GetRet() == 0 {
				rooms[rid] = ri.GetBeChallengedInfos()
			}
		}
	}
	p.SwMu.Lock()
	highPvp := p.swHighPvp
	p.SwMu.Unlock()
	pressed := map[int64]bool{}
	hits, lastRet, lastDamage := 0, int32(-1), ""
	for round := 0; round < swWarMaxPress; round++ {
		// Fresh lệnh board per press — challengeTimes moves after every fight.
		ent, err := p.swEnterChallenge(actID, unionID)
		if err != nil {
			if hits == 0 {
				return "", fmt.Errorf("mất kết nối khi vào cứ điểm (%v)", err)
			}
			break
		}
		targets := swWarPlan(ent, rooms, infos, unionID, swWarMaxRooms, swWarMaxPress, highPvp)
		var tg *swWarTarget
		for i := range targets {
			if !pressed[targets[i].PlayerID] {
				tg = &targets[i]
				break
			}
		}
		if tg == nil {
			if round == 0 {
				return "", fmt.Errorf("cứ điểm trống — không có lệnh nào sáng (chưa tuyên chiến, hoặc acc đã đánh hết lệnh) — enter ret %d", ent.GetRet())
			}
			break
		}
		pressed[tg.PlayerID] = true
		rsp, err := p.swChallenge(actID, unionID, tg.RoomID, tg.PlayerID)
		if err != nil {
			if hits == 0 {
				return "", fmt.Errorf("mất kết nối giữa loạt đánh (%v)", err)
			}
			break
		}
		if rsp.GetRet() != 0 {
			lastRet = rsp.GetRet()
			continue // blind law: server says no for this press — try the next
		}
		hits++
		if d := rsp.GetDamage(); d != "" {
			lastDamage = d
		}
		swSleepFunc(time.Second) // one battle at a time, owner "không cần vội"
	}
	if hits == 0 {
		// Neutral wording law: report the server's ret as-is — nothing local
		// checked and nothing blames the attempts counter.
		if len(pressed) == 0 {
			return "", fmt.Errorf("server không cho đánh (không gọi được cú nào)")
		}
		return "", fmt.Errorf("server từ chối cả loạt (%d lệnh thử, ret cuối %d)", len(pressed), lastRet)
	}
	line := fmt.Sprintf("⚔️ đánh trúng %d/%d", hits, len(pressed))
	if lastDamage != "" {
		line += " · dame cuối " + lastDamage
	}
	p.swLog("[TMYV] lệnh đánh cứ điểm %d: %s", unionID, line)
	return line, nil
}

// swWarRoomIDs lists the ENTER answer's rooms, deduped, first-seen order.
func swWarRoomIDs(ent *proto.SeaWarEnterChallengeResp, max int) []int32 {
	var out []int32
	seen := map[int32]bool{}
	for _, d := range ent.GetDeclarationInfos() {
		rid := d.GetRoomId()
		if seen[rid] {
			continue
		}
		seen[rid] = true
		out = append(out, rid)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func (p *EventPlayer) swHeartBeat(actID int32) {
	body, err := pb.Marshal(&proto.SeaWarHeartBeatReq{ActivityId: actID, ChallengeState: pb.Bool(false)})
	if err != nil {
		return
	}
	_, _ = p.swRT(SW_HEART_BEAT_CMD, SERVER_SW_HEART_BEAT, body)
}

func (p *EventPlayer) swNextRegion(actID, next int32, useItem bool) (*proto.SeaWarNextRegionResp, error) {
	// useItem=true = paid phi-thạch jump — gated by swRegionStoneOK at the
	// only call site (floors 3↑, owner 2026-11-10).
	body, err := pb.Marshal(&proto.SeaWarNextRegionReq{ActivityId: actID, NextRegionId: next, UseItem: pb.Bool(useItem)})
	if err != nil {
		return nil, err
	}
	data, err := p.swRT(SW_NEXT_REGION_CMD, SERVER_SW_NEXT_REGION, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarNextRegionResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swCfg() swCfgView {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	return swCfgView{
		ballEffect:   p.swBallEffect,
		ballType:     p.swBallType,
		regionEnergy: p.swRegionEnergy,
		regionCond:   p.swRegionCond,
		eventCfgIds:  p.swEventCfgIds,
		helpBoxNeed:  p.swHelpBoxNeed,
	}
}

func (p *EventPlayer) swMarkWorship(day string) bool {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if p.swWorshipDay == day {
		return false
	}
	p.swWorshipDay = day
	return true
}

func (p *EventPlayer) swMarkExhaust(key string) bool {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if p.swExhaustKey == key {
		return false
	}
	p.swExhaustKey = key
	return true
}

// swExhausted notifies the OWNER (forceNotify reaches the bound chat even
// with Notify=false) that this account needs the energy item — the button
// decision is theirs; event accounts never spend [[tytd-item-ban]].
func (p *EventPlayer) swExhausted(region int32) {
	p.forceNotify(fmt.Sprintf("🌊 [%s] Thương Minh Yêu Vực: hết thể lực ở tầng %d — cần bấm /thuongminh_suc để dùng vật phẩm.",
		p.Username, region))
}

func (p *EventPlayer) swSelf() int64 { return int64(p.UID) }

func (p *EventPlayer) swAssistList(actID int32) (*proto.SeaWarAssistListResp, error) {
	data, err := p.swRT(SW_ASSIST_LIST_CMD, SERVER_SW_ASSIST_LIST, swCommonBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarAssistListResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swAssist(actID, typ int32, targetPlayerID, id int64) (*proto.SeaWarAssistRsp, error) {
	body, err := pb.Marshal(&proto.SeaWarAssistReq{ActivityId: actID, Type: pb.Int32(typ), TargetPlayerId: pb.Int64(targetPlayerID), Id: pb.Int64(id)})
	if err != nil {
		return nil, err
	}
	data, err := p.swRT(SW_ASSIST_CMD, SERVER_SW_ASSIST, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarAssistRsp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swAssistReward(actID, typ int32, id int64) (*proto.SeaWarAssistRewardRsp, error) {
	body, err := pb.Marshal(&proto.SeaWarAssistRewardReq{ActivityId: actID, Type: pb.Int32(typ), Id: pb.Int64(id)})
	if err != nil {
		return nil, err
	}
	data, err := p.swRT(SW_ASSIST_REWARD_CMD, SERVER_SW_ASSIST_REWARD, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.SeaWarAssistRewardRsp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swMall() ([]*proto.ActivityMallTempMsg, map[int32]int64) {
	today := time.Now().In(ictZone).Format("2006-01-02")
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if p.swMallDay != today || p.swMallAdds == nil {
		p.swMallDay = today
		p.swMallAdds = map[int32]int64{}
	}
	merged := map[int32]int64{}
	for id, n := range p.swMallBought {
		merged[id] = n
	}
	for id, n := range p.swMallAdds {
		merged[id] += n
	}
	return append([]*proto.ActivityMallTempMsg{}, p.swMallRows...), merged
}

func (p *EventPlayer) swPullMall() bool {
	actID, ok := p.swActiveActivity()
	if !ok {
		return false
	}
	body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID})
	if err != nil {
		return false
	}
	if _, err := p.swRT(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err != nil {
		p.swLog("[TMYV] shop: không lấy được cấu hình cửa hàng (%v) — thử lại chu kỳ sau", err)
		return false
	}
	p.SwMu.Lock()
	n := len(p.swMallRows)
	p.SwMu.Unlock()
	if n > 0 {
		p.swLog("[TMYV] shop: đọc được %d gói từ máy chủ", n)
	}
	return n > 0
}

func (p *EventPlayer) swBuyMall(actID, mallID int32) (*proto.RspBuyActivityGoods, error) {
	body, err := pb.Marshal(&proto.ReqBuyActivityGoods{ActivityId: actID, MallId: mallID, Count: 1})
	if err != nil {
		return nil, err
	}
	data, err := p.swRT(ACTIVITY_BUY_GOODS_CMD, SERVER_ACTIVITY_BUY_GOODS, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.RspBuyActivityGoods
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *EventPlayer) swMarkMallBought(mallID int32) {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if p.swMallAdds == nil {
		p.swMallAdds = map[int32]int64{}
	}
	p.swMallAdds[mallID]++
}

func (p *EventPlayer) swShopReady(mallID int32) bool {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	today := time.Now().In(ictZone).Format("2006-01-02")
	if p.swShopDay != today {
		p.swShopDay, p.swShopFails = today, nil
	}
	return p.swShopFails[mallID].Before(time.Now())
}

func (p *EventPlayer) swMarkShopFail(mallID int32) {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if p.swShopFails == nil {
		p.swShopFails = map[int32]time.Time{}
	}
	p.swShopFails[mallID] = time.Now().Add(swShopFailWait)
}

// ─── wake bell (see Player twin) ─────────────────────────────────────────────

func (p *EventPlayer) swPingWake() {
	p.SwMu.Lock()
	if p.swWake == nil {
		p.swWake = make(chan struct{}, 1)
	}
	ch := p.swWake
	p.SwMu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (p *EventPlayer) swSleepPoll(d time.Duration, tickStart time.Time) bool {
	p.SwMu.Lock()
	if p.swWake == nil {
		p.swWake = make(chan struct{}, 1)
	}
	ch := p.swWake
	p.SwMu.Unlock()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.ctx.Done():
		return false
	case <-t.C:
		return true
	case <-ch:
		if r := tickStart.Add(swMinTick).Sub(time.Now()); r > 0 {
			select {
			case <-p.ctx.Done():
				return false
			case <-time.After(r):
			}
		}
		return true
	}
}

// swShopNotify — purchases are wins worth telling the owner about;
// forceNotify: EP sessions run with Notify=false.
func (p *EventPlayer) swShopNotify(text string) {
	p.forceNotify(fmt.Sprintf("[%s] %s", p.Username, text))
}

// ─── event-account loop ("login là chạy") ───────────────────────────────────

func (p *EventPlayer) seaWarEventLoop() {
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
		p.swLog("[TMYV] auto: %s", s)
	}
	var nextStatus time.Time
	statusNow := ""
	beat := func() {
		if nextStatus.IsZero() {
			nextStatus = time.Now().Add(swStatusGap)
			return
		}
		if time.Now().Before(nextStatus) {
			return
		}
		p.forceNotify(fmt.Sprintf("🌊 [%s] Thương Minh Yêu Vực: %s — trạng thái này %s",
			p.Username, statusNow, stFmtDur(time.Since(stateSince))))
		nextStatus = time.Now().Add(swStatusGap)
	}
	for {
		if p.OffSeaWar {
			record("tắt — leader đã gạt tắt ở tab Sự kiện")
			nextStatus, statusNow = time.Time{}, ""
			if !p.rbtSleepCtx(time.Minute) {
				return
			}
			continue
		}
		actID, ok := p.swActiveActivity()
		if !ok {
			record("chưa thấy Thương Minh Yêu Vực trong danh sách sự kiện")
			nextStatus, statusNow = time.Time{}, ""
			if !p.rbtSleepCtx(swIdleWait) {
				return
			}
			continue
		}
		statusNow = fmt.Sprintf("đang yêu vục (act %d)", actID)
		record(statusNow)
		tickStart := time.Now()
		swPlayTick(p, actID)
		beat()
		if !p.swSleepPoll(swPoll, tickStart) {
			return
		}
	}
}
