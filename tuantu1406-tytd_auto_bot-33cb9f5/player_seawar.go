package tytd_go

/*
Thương Minh Yêu Vực (client name CelestialSeaWar, activity type 349, logic
hosted in the remoteGoldPeach bundle). Engine ported 2026-11-09 from the
client's own auto-controller (csw_sim/AutoControl.min.js + modules/) and the
xor-1 protobuf/SeaWar TextAsset (csw_sim/SeaWar.proto) — [[tytd-seawar-port]].

Owner scope (2026-11-09): "không cần vẽ map, chỉ cần auto chạy free, thêm nút
bấm dùng item, ở chức năng rèn đồ thì ưu tiên thuộc tính sát thương cuối" —
no map renderer, free actions only, the energy item (SEAWAR_ENERGY_ITEM,
default 105402) is spent ONLY when the owner presses the button
(/thuongminh_suc, [[tytd-item-ban]] exception, BUTTON-ONLY: the auto path
never sends 234922), and FORGE_GEM picks the ball whose effect is a
final-damage skill (GameSkillDB type 1017/1018 — the 445 ids baked below
from csw_sim/final_damage_skill_ids.json; the client's own
getSoulBallAttrSortNum ranks exactly these at 2 = best).

Free actions automated: ENTER (+WORSHIP when canWorship), ENTER_GAME,
EXPLORE, HANDLE_EVENT (monster fight, weakest-player PVP + union mark call
on a failed kill, boxes, water
spring, forge), NEXT_REGION with useItem=false, UNION_WELFARE(+claim) and
UNION_RECEIVE_LIQUID, HEART_BEAT keep-alive. Never automated: the guild-war
family 234931..234944 (ORDER-ONLY law), TRIGGER_GIFT (paid mall — skipped
with action=2), and any item spend. Blind-poll law: every claim fires
unconditionally, the server ret is the sole arbiter.

The gameplay core (swPlayTick + event resolution) is shared verbatim between
the Player twin (this file, opt-in p.AutoSeaWar) and the EventPlayer twin
(player_event_seawar.go, "login là chạy") through the swAPI interface — each
twin supplies its own transport (stRoundTrip vs the swChans seat map).
*/

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// errSwClosed = the activity is not open for this account right now.
var errSwClosed = errors.New("seawar: activity not open")

const (
	swActivityType = 349

	swPoll          = time.Minute      // in-window decision cycle
	swStatusGap     = 5 * time.Minute  // heartbeat while actually playing
	swIdleWait      = 10 * time.Minute // sleep cap while the activity is absent
	swReqTimeout    = 15 * time.Second
	swShopFailWait  = 10 * time.Minute // refused shop row rests before retry
	swMinTick       = 5 * time.Second  // wake-bell floor: never tick faster than this
	swExploreBudget = 80               // explores per tick — sanity cap, never a goal
	swEventBudget   = 30               // event resolves per tick
	swDonateBudget  = 200              // phát-linh-lộ presses per welfare round

	// Donate-burst law v2 (owner 2026-11-11 "1 phút phát được có 1 linh lộ,
	// phát cần phát hết luôn"): the server refuses the SECOND immediate
	// phát (donate cooldown) — the old stop-at-first-refusal loop therefore
	// drained the bag at exactly 1 item/engine-tick. A non-101 refusal now
	// means "wait swDonateRefuseWait, press again", tolerated up to
	// swDonateRefuseTries CONSECUTIVELY (≈96s — covers a full 1-minute
	// cooldown so a burst crosses it and keeps donating); 101 = bag dry,
	// stop at once. A round with ZERO successes parks the path for
	// swDonatePark so a capped/no-union account never stalls the tick.
	swDonateRefuseTries = 12
	swDonateRefuseWait  = 8 * time.Second
	swDonatePark        = 15 * time.Minute

	// Daily battle rhythm (owner 2026-11-11 — REPLACES the 21:55 dump
	// gate): the day OPENS at 10:00 ICT — before that the bot does no
	// strength ops at all. Morning/afternoon: climb to floor 3 and DUMP
	// all strength there; the account parks at floor 3 (no floor-4 jump)
	// until swDumpHour:swDumpMin = 16:30 ICT, when the evening window
	// opens: jump the moment a condition is met (mid-chain "đủ điều kiện
	// là lên ngay") and dump freely up the ladder.
	swRunStartHour = 10
	swFarmEndHour  = 10 // morning window closes 10:30 — hard tl ban after it
	swFarmEndMin   = 30
	swDumpHour     = 16
	swDumpMin      = 30
)

// SeaWarEventType (client enum): what curEventData.eventType means.
const (
	swEvMonster   = 1 // CHALLENGE_MONSTER — free fight, action=3
	swEvPlayer    = 2 // CHALLENGE_PLAYER — PVP vs listed opponent, action=3 + id
	swEvNormalBox = 3 // OPEN_NORMAL_BOX — {} (eventId only)
	swEvHelpBox   = 4 // OPEN_HELP_BOX — {}
	swEvForge     = 5 // FORGE_GEM — {id: ballUid}
	swEvGift      = 6 // TRIGGER_GIFT — PAID mall popup → skip (action=2)
	swEvSpring    = 7 // PROBE_WATER_SPRING — {}
)

// ECelestialSeaWarEventAction.
const (
	swActionMark        = 1 // raise the union mark (call teammates)
	swActionSkip        = 2
	swActionChallenge   = 3
	swActionRequestMark = 4 // pre-flight for MARK (client sendRequestMark)
)

// swFinalEffects — GameSkillDB ids whose type is FinalAddDamage(1017) or
// FinalReduceDamage(1018): the client's forge sort ranks these first
// (attrSortNum 2). Owner law: "ưu tiên thuộc tính sát thương cuối".
// Baked 2026-11-09 from csw_sim/GameSkillDB.json (445 ids, both classes).
var swFinalEffects = map[int32]bool{}

func init() {
	for _, id := range []int32{
		20017, 20018, 20041, 20042, 20065, 20066, 20089, 20090, 100017, 100018,
		101017, 101018, 102017, 102018, 103017, 103018, 104017, 104018, 105017, 105018,
		120007, 120009, 120013, 120016, 120019, 120022, 120025, 120028, 120029, 120031,
		120035, 120037, 120039, 120041, 120043, 120046, 120048, 120049, 120050, 120053,
		120055, 120058, 120059, 120062, 120063, 120065, 120067, 120069, 120071, 120073,
		120075, 150005, 150015, 150016, 150017, 150018, 150019, 150020, 150021, 150022,
		150023, 150024, 150025, 150026, 150027, 150028, 150029, 150030, 151001, 151002,
		160005, 160013, 160017, 160022, 170001, 170010, 180009, 180010, 180011, 180012,
		180013, 180015, 190001, 190002, 190003, 190004, 190005, 190006, 190007, 190008,
		190009, 190010, 190011, 190012, 190013, 190014, 190015, 190016, 190017, 200002,
		230001, 230002, 230003, 230004, 230005, 230006, 230007, 230008, 248007, 248008,
		248009, 248010, 248029, 248035, 248055, 248056, 248062, 248063, 248065, 248080,
		248089, 248102, 248106, 248128, 248141, 248146, 248156, 248197, 248200, 248201,
		248207, 248218, 248228, 260008, 260013, 260020, 260022, 260025, 260026, 260027,
		260028, 260036, 260038, 260041, 260042, 260043, 260044, 260045, 260046, 260048,
		260054, 260055, 260056, 260057, 260058, 260061, 260062, 260064, 260066, 260068,
		260069, 260070, 260071, 260072, 260076, 260077, 260078, 260079, 260080, 260081,
		260082, 260084, 260085, 260088, 260089, 260092, 260093, 260095, 260096, 260097,
		260099, 260101, 260105, 260106, 260107, 260108, 260109, 260110, 260111, 260112,
		260113, 260115, 260116, 260117, 260118, 260119, 260122, 260125, 260128, 260131,
		260132, 260133, 260134, 260136, 260137, 260138, 260139, 261002, 261011, 261012,
		261013, 262002, 262011, 291016, 291017, 292016, 292017, 293016, 293017, 351088,
		351089, 351090, 351091, 351092, 351093, 351115, 351116, 351138, 351139, 361016,
		361017, 361018, 361023, 361024, 361025, 361026, 361027, 361028, 361029, 361030,
		361031, 361032, 361033, 361034, 361035, 361036, 361037, 361038, 362316, 362317,
		381017, 381018, 381032, 381033, 400007, 400008, 400009, 400010, 440007, 440008,
		442012, 451001, 451002, 505003, 505004, 510014, 510015, 510016, 510019, 510028,
		510029, 510030, 510036, 510037, 510041, 510045, 510046, 510050, 510051, 510055,
		510056, 510059, 510063, 510066, 510067, 530001, 530002, 530003, 530004, 531001,
		531002, 540001, 550016, 550017, 580003, 580004, 590008, 590009, 590010, 600003,
		600004, 600007, 600008, 621002, 621003, 621005, 621007, 621008, 621010, 630317,
		630318, 630417, 630418, 670006, 670007, 670009, 670010, 670012, 670013, 682534,
		682535, 684001, 684003, 684005, 684007, 722005, 722006, 722007, 722008, 722009,
		722010, 735005, 735007, 735009, 735011, 746026, 746027, 770001, 770002, 770007,
		770008, 770009, 770010, 780001, 780002, 780005, 780006, 780009, 780010, 780013,
		780014, 790001, 790002, 790003, 790004, 790005, 790006, 790007, 790008, 790009,
		790010, 790011, 790012, 790013, 790101, 790102, 790103, 790104, 790105, 790106,
		790107, 790108, 790109, 790110, 790111, 790112, 790113, 801701, 801702, 801703,
		801704, 801705, 801706, 801801, 801802, 801803, 801804, 801805, 801806, 841039,
		841040, 841041, 841042, 841043, 841044, 841045, 841046, 841047, 841048, 850045,
		860001, 860002, 860003, 860004, 891003, 900017, 900018, 931701, 931801, 940001,
		940002, 940003, 940004, 940005, 940006, 940007, 940008, 940009, 940010, 940011,
		940012, 940013, 940014, 940015, 940101, 940102, 940103, 940104, 940105, 940106,
		940107, 940108, 940109, 940110, 940111, 940112, 940113, 940114, 940115, 950003,
		950004, 970017, 970018, 995001, 995002,
	} {
		swFinalEffects[id] = true
	}
}

// ─── shared core ─────────────────────────────────────────────────────────────

// swCfgView is one locked snapshot of the folded 21003 config.
type swCfgView struct {
	ballEffect   map[int32]int32  // soulBall configId → GameSkillDB effect id
	ballType     map[int32]int32  // soulBall configId → ball type (client tie-break)
	regionEnergy map[int32]int32  // region id → strength per explore
	regionCond   map[int32]string // region id → consumeCondition "evId=num|itemId=num" (OR — see swRegionConditions)
	// eventCfgIds = every id in SeaWarEventConfigMsgs — the classifier that
	// splits a consumeCondition pair into event-part vs phi-thạch-part
	// (live probe 2026-11-10: "10009=2|105403=2").
	eventCfgIds map[int32]bool
	helpBoxNeed int32 // OPEN_HELP_BOX param: helpers needed to fill a box
}

// ECelestialSeaWarAssistType (client GameLog): SeaWarAssistData.type.
const (
	swAssistFight = 0 // teammate mid-fight (assistInfos)
	swAssistBox   = 1 // teammate's help box short of helpers (helpBoxInfos)
)

// swAPI is what the shared tick needs from either twin.
type swAPI interface {
	swName() string // log prefix (username)
	swLog(format string, args ...any)
	swEnter(actID int32) (*proto.SeaWarEnterResp, error)
	swEnterGame(actID int32) (*proto.SeaWarEnterGameResp, error)
	swExplore(actID int32) (*proto.SeaWarExploreResp, error)
	swHandle(actID, evID, action int32, id int64) (*proto.SeaWarHandleEventResp, error)
	swWorship(actID int32)
	swWelfare(actID int32) (*proto.SeaWarUnionWelfareResp, error)
	swWelfareReward(actID int32)
	swReceiveLiquid(actID int32, id int64)
	swCondSweep(actID int32)                         // nạp-bang milestone rungs (21045), 5-min cadence
	swSendLiquid(actID, itemID int32) (int32, error) // 234917 one donate press
	swLiquidItemIDs() []int32                        // the 3 linh lộ tiers (SEAWAR_ITEM_ENERGY / baked)
	// Donate-burst park: after a tick that got NOTHING through (cap/no
	// union), the path sleeps instead of stalling every 1-min tick.
	swDonatePaused(now time.Time) bool
	swDonatePause(until time.Time)
	swHeartBeat(actID int32)
	swNextRegion(actID, next int32, useItem bool) (*proto.SeaWarNextRegionResp, error)
	swCfg() swCfgView
	// swMarkWorship claims the once-per-ICT-day worship slot (true = first).
	swMarkWorship(day string) bool
	// swMarkExhaust claims the once-per-day-per-region exhaustion notice.
	swMarkExhaust(key string) bool
	// swExhausted delivers the "hết thể lực — bấm nút" notice (owner-only
	// button decision, never auto — [[tytd-item-ban]]).
	swExhausted(region int32)
	// swSelf is the session's game playerId — used to split the assist
	// board into "teammates needing help" vs "my own finished entries".
	swSelf() int64
	// Assist family (all FREE — no strength, no items; server arbitrates
	// the daily assistTimes cap through ret).
	swAssistList(actID int32) (*proto.SeaWarAssistListResp, error)
	swAssist(actID, typ int32, targetPlayerID, id int64) (*proto.SeaWarAssistRsp, error)
	swAssistReward(actID, typ int32, id int64) (*proto.SeaWarAssistRewardRsp, error)
	// Shop (21005): rows folded from 21003 + per-day bought counters.
	swMall() (rows []*proto.ActivityMallTempMsg, bought map[int32]int64)
	swPullMall() bool // one 21003 pull + fold; true = rows now available
	swBuyMall(actID, mallID int32) (*proto.RspBuyActivityGoods, error)
	swMarkMallBought(mallID int32)
	// Failure suppression: a refused row (ladder lock ret=307 live, jade
	// balance, ad cooldown) rests 10 minutes before the sweep retries it.
	swShopReady(mallID int32) bool
	swMarkShopFail(mallID int32)
	// swShopNotify announces a successful shop purchase (Player:
	// sendNotify; EP: forceNotify — [[tytd-delegation]] alert laws).
	swShopNotify(text string)
}

// swCmpFightValue orders fightValue strings (decimal, may exceed int64
// display digits): longer numeric string = stronger; strip leading zeros.
func swCmpFightValue(a, b string) int {
	as, bs := strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(as) != len(bs) {
		return len(as) - len(bs)
	}
	return strings.Compare(as, bs)
}

// swPickForgeBall is the owner's forge rule (2026-11-09, refined same day):
// final-damage ("sát thương cuối") effects first — score 2, everything else
// 0 — and WITHIN the stc group the HIGHER attribute value wins (GameSkillDB
// param, swFinalEffectValue; the very number the forge UI shows). Remaining
// ties break on lower ball type (client AUTO_SELECT_MIN_LV) then lower
// configId. cfg.ballEffect empty (config not folded yet) → everything scores
// 0 and the client's plain min-LV order applies — a safe fallback, never a
// stall.
func swPickForgeBall(balls []*proto.SeaWarBuffBallMsg, cfg swCfgView) *proto.SeaWarBuffBallMsg {
	if len(balls) == 0 {
		return nil
	}
	type cand struct {
		ball  *proto.SeaWarBuffBallMsg
		score int
		val   int32
		typ   int32
	}
	cands := make([]cand, 0, len(balls))
	for _, b := range balls {
		if b == nil {
			continue
		}
		eff := cfg.ballEffect[b.GetConfigId()]
		score, val := 0, int32(0)
		if swFinalEffects[eff] {
			score = 2
			val = swFinalEffectValue[eff] // owner: "viên stc có thuộc tính cao"
		}
		cands = append(cands, cand{b, score, val, cfg.ballType[b.GetConfigId()]})
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if cands[i].val != cands[j].val {
			return cands[i].val > cands[j].val
		}
		if cands[i].typ != cands[j].typ {
			return cands[i].typ < cands[j].typ
		}
		return cands[i].ball.GetConfigId() < cands[j].ball.GetConfigId()
	})
	return cands[0].ball
}

// swRegionConditions parses a floor's consumeCondition. Server truth
// (live probe 2026-11-10, <live-user>@710547): every floor row reads
// "monsterEventId=2|105403=1|2" — 105403 = THƯƠNG MINH PHI THẠCH, and the
// two sides are an OR: "khi lên tầng cần thoả mãn 1 trong 2 điều kiện là
// đánh đủ số quái hoặc tiêu hao đá để lên tầng" (owner 2026-11-10). The old
// all()-over-pairs check demanded the stone pair inside eventCompleteVec
// too — a stone count NEVER appears there — so free advance was dead false
// on every floor: the "đủ điều kiện vẫn kẹt tầng" bug.
// eventOK = every event-side pair met; itemNeed = phi-thạch count for the
// paid jump (0 = none/none-needed). Unknown region → (false, 0) = no advance.
func swRegionConditions(cfg swCfgView, pd *proto.SeaWarPlayerDataMsg, region int32) (eventOK bool, itemNeed int32) {
	if pd == nil {
		return false, 0
	}
	cond, ok := cfg.regionCond[region]
	if !ok {
		return false, 0 // region table not folded (or bogus region) → don't advance
	}
	if s := strings.TrimSpace(cond); s == "" || s == "0" {
		return true, 0 // free advance (floor 5 top reads "0" — server still arbitrates)
	}
	done := map[int32]int32{}
	for _, c := range pd.GetEventCompleteVec() {
		done[c.GetEventId()] = c.GetNum()
	}
	missing := false
	for _, part := range strings.Split(cond, "|") {
		kv := strings.Split(strings.TrimSpace(part), "=")
		if len(kv) != 2 {
			continue
		}
		var key, want int32
		if _, err := fmt.Sscanf(kv[0], "%d", &key); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(kv[1], "%d", &want); err != nil {
			continue
		}
		if key == 0 {
			continue // "0" sentinel pair
		}
		// classify: known event id → monster/event side; otherwise the
		// phi-thạch side. Without the folded event table fall back to the
		// observed digit rule (event ids ≤5 digits, item ids 1054xx ≥6).
		isEvent := key < 100000
		if cfg.eventCfgIds != nil {
			isEvent = cfg.eventCfgIds[key]
		}
		if isEvent {
			if done[key] < want {
				missing = true // keep parsing — the stone side must still be
				// reported: it is the PAID door the floor-3↑ jump needs.
			}
		} else {
			itemNeed += want
		}
	}
	return !missing, itemNeed
}

// swFloorPos ranks a region inside the folded region table (floor ids are
// config values — 10001..10005 this season — never hardcode): 0 = first
// floor, 2 = third floor (the "nằm im" floor), -1 = unknown / table not
// folded. Shared by the stone law and the floor-4 dump gate.
func swFloorPos(cfg swCfgView, region int32) int {
	if region <= 0 {
		return -1
	}
	var ids []int32
	for id := range cfg.regionEnergy {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for pos, id := range ids {
		if id == region {
			return pos
		}
	}
	return -1
}

// swRegionStoneOK — the stone law (owner 2026-11-10): "từ tầng 1 lên 2 lên 3
// không được dùng đá thương minh phi thạch để lên tầng, phải đánh đủ quái.
// 3 lên 4 thì được dùng đá". Leaving floor 1 (pos 0) or floor 2 (pos 1) =
// monsters only; floor 3 (pos 2) and beyond may burn phi-thạch via
// useItem=true.
func swRegionStoneOK(cfg swCfgView, region int32) bool {
	return swFloorPos(cfg, region) >= 2
}

// swPhase — the daily battle rhythm slot (owner 2026-11-11):
// sleep = before 10:00 ICT (no strength ops), farm = the 10:00–10:29
// morning window (climb to floor 3, dump everything there), hold =
// 10:30–16:29 ABSOLUTE strength ban ("tuyệt đối không tiêu thể lực"),
// dump = evening free window (≥16:30): jump on condition instantly, dump
// up the ladder.
type swPhase int

const (
	swPhaseSleep swPhase = iota
	swPhaseFarm
	swPhaseHold
	swPhaseDump
)

// swPhaseOf maps the ICT clock to today's rhythm. Tests override swNowFunc
// to pin the hour.
func swPhaseOf(now time.Time) swPhase {
	t := now.In(ictZone)
	switch {
	case t.Hour() < swRunStartHour:
		return swPhaseSleep
	case t.Hour() < swFarmEndHour || (t.Hour() == swFarmEndHour && t.Minute() < swFarmEndMin):
		return swPhaseFarm
	case t.Hour() > swDumpHour || (t.Hour() == swDumpHour && t.Minute() >= swDumpMin):
		return swPhaseDump
	default:
		return swPhaseHold
	}
}

// swNowFunc is the engine clock — tests override it to pin the 21:55 gate.
var swNowFunc = time.Now

// swSleepFunc is the engine's pacing hook (owner 2026-11-10: "mỗi lần tiêu
// thể lực cách nhau 1 giây, không cần vội"). Overridable so the deterministic
// unit suite doesn't pay real seconds; production sleeps for real.
var swSleepFunc = time.Sleep

// swTargetKilled reports whether the challenged opponent is finished in the
// post-challenge eventInfo — absent from the fresh list, or hp parses ≤ 0
// (client CelestialSeaWarEventPVP: !t.eventData ends the fight, setPlayerHp
// reads the returned hp). An unparseable hp means NOT killed: alive entries
// keep their hp string, absence is the kill signal.
func swTargetKilled(eventInfo []byte, playerID int64) bool {
	var cp proto.SeaWarEvent_ChallengePlayer
	if pb.Unmarshal(eventInfo, &cp) != nil {
		return true // unparseable fresh info — treat the board as moved on
	}
	for _, c := range cp.GetPlayerData() {
		if c != nil && c.GetPlayerId() == playerID {
			v, err := strconv.ParseFloat(c.GetHp(), 10)
			return err == nil && v <= 0
		}
	}
	return true
}

// swResolveEvent resolves one pending event, mirroring the client's
// CelestialSeaWarEvent* click handlers. Returns the next pending event (nil
// = board clear) and ok=false when the chain must stop this tick.
func swResolveEvent(api swAPI, actID int32, ev *proto.SeaWarEventDataMsg, cfg swCfgView) (*proto.SeaWarEventDataMsg, bool) {
	send := func(action int32, id int64, what string) (*proto.SeaWarEventDataMsg, bool) {
		rsp, err := api.swHandle(actID, ev.GetEventId(), action, id)
		if err != nil {
			return nil, false
		}
		if rsp.GetRet() != 0 {
			api.swLog("[TMYV] %s bị từ chối (code %d)", what, rsp.GetRet())
			// Clear the stuck event so explore can resume; if even the
			// skip is refused, stop the chain — next tick retries.
			if sr, err := api.swHandle(actID, ev.GetEventId(), swActionSkip, 0); err != nil || sr == nil || sr.GetRet() != 0 {
				return nil, false
			}
			return nil, true
		}
		return rsp.GetEventData(), true
	}
	switch ev.GetEventType() {
	case swEvMonster:
		return send(swActionChallenge, 0, "đánh quái")
	case swEvPlayer:
		// Owner law (2026-11-11): "cứ gặp người là pick người yếu nhất
		// đánh, nếu đánh thua thì gọi đồng đội hỗ trợ". Pick mirrors the
		// client's AUTO_SELECT_MIN_POWER (chooseMinPowerOpponentIndex):
		// weakest by fightValue among ALL entries — no isHadAtk filter.
		// The only PASS mirror left is the client's lone-hit opponent
		// (isHadFight: 1 entry, already hit → skip). A failed kill (the
		// target survives in the returned event with hp>0) calls the
		// union through the client's btn_mark flow: REQUEST_MARK then
		// MARK (MARK only after REQUEST_MARK ret==0 — reqHandleEvent's
		// callback fires on success only). The mark never rides send():
		// a refused mark must NOT skip the event, only log (blind-fire
		// law; server ret is the sole arbiter).
		var cp proto.SeaWarEvent_ChallengePlayer
		var list []*proto.SeaWarEvent_ChallengePlayerData
		if pb.Unmarshal(ev.GetEventInfo(), &cp) == nil {
			for _, c := range cp.GetPlayerData() {
				if c != nil && c.GetPlayerId() != 0 {
					list = append(list, c)
				}
			}
		}
		var best *proto.SeaWarEvent_ChallengePlayerData
		for _, c := range list {
			if best == nil || swCmpFightValue(c.GetFightValue(), best.GetFightValue()) < 0 {
				best = c
			}
		}
		if best == nil || (len(list) == 1 && best.GetIsHadAtk()) {
			return send(swActionSkip, 0, "PVP (chỉ còn đối thủ đã đánh)")
		}
		next, ok := send(swActionChallenge, best.GetPlayerId(), fmt.Sprintf("đánh người %s", best.GetNickName()))
		if ok && next != nil && next.GetEventId() != 0 && best.GetAssistId() == 0 &&
			!swTargetKilled(next.GetEventInfo(), best.GetPlayerId()) {
			api.swLog("[TMYV] %s chưa hạ được — gọi đồng đội", best.GetNickName())
			if rsp, err := api.swHandle(actID, ev.GetEventId(), swActionRequestMark, 0); err == nil && rsp != nil && rsp.GetRet() == 0 {
				if rsp2, err2 := api.swHandle(actID, ev.GetEventId(), swActionMark, 0); err2 == nil && rsp2 != nil && rsp2.GetRet() != 0 {
					api.swLog("[TMYV] đánh dấu cầu viện %s bị từ chối (code %d)", best.GetNickName(), rsp2.GetRet())
				}
			} else if err == nil && rsp != nil {
				api.swLog("[TMYV] cầu viện (request) %s bị từ chối (code %d)", best.GetNickName(), rsp.GetRet())
			}
		}
		return next, ok
	case swEvNormalBox:
		return send(0, 0, "mở rương")
	case swEvHelpBox:
		return send(0, 0, "mở rương trợ giúp")
	case swEvSpring:
		return send(0, 0, "thám tuyền")
	case swEvForge:
		var fg proto.SeaWarEvent_ForgeGem
		if pb.Unmarshal(ev.GetEventInfo(), &fg) != nil || len(fg.GetBallList()) == 0 {
			return send(swActionSkip, 0, "luyện bảo thạch (sự kiện rỗng)")
		}
		pick := swPickForgeBall(fg.GetBallList(), cfg)
		if pick == nil {
			return send(swActionSkip, 0, "luyện bảo thạch (không chọn được)")
		}
		tag := "bảo thạch thường"
		if swFinalEffects[cfg.ballEffect[pick.GetConfigId()]] {
			tag = "bảo thạch SÁT THƯƠNG CUỐI"
		}
		return send(0, pick.GetUid(), "luyện "+tag)
	default: // TRIGGER_GIFT (6) & unknowns: paid mall popups — always skipped
		return send(swActionSkip, 0, fmt.Sprintf("bỏ sự kiện type %d", ev.GetEventType()))
	}
}

// swPlayTick drives one in-window wake: enter → worship → board drain
// (explore + resolve events) → region advance → welfare claims → beat.
func swPlayTick(api swAPI, actID int32) {
	enter, err := api.swEnter(actID)
	if err != nil {
		return
	}
	if enter.GetRet() != 0 {
		api.swLog("[TMYV] vào hoạt động bị từ chối (code %d)", enter.GetRet())
		return
	}
	if enter.GetCanWorship() {
		if day := time.Now().In(ictZone).Format("2006-01-02"); api.swMarkWorship(day) {
			api.swWorship(actID)
		}
	}
	eg, err := api.swEnterGame(actID)
	if err != nil {
		return
	}
	if eg.GetRet() != 0 {
		api.swLog("[TMYV] vào bảng bị từ chối (code %d)", eg.GetRet())
		return
	}
	cfg := api.swCfg()
	pd := eg.GetPlayerData()
	// Drain whatever event the board is holding from a previous session.
	if ev := eg.GetCurEventData(); ev != nil && ev.GetEventId() != 0 {
		for i := 0; i < swEventBudget; i++ {
			next, ok := swResolveEvent(api, actID, ev, cfg)
			if !ok {
				break
			}
			if next == nil || next.GetEventId() == 0 {
				break
			}
			ev = next
		}
	}
	// Explore chain (drain): blind steps until the server refuses or
	// strength runs out; every eventData that lands is resolved inline.
	// budgetHit=false records that the chain ended on a REAL stop (refusal
	// or empty strength), not on the sanity budget — the exhaustion notice
	// below trusts only that signal. drain returns true on a transport
	// error that must abort the whole tick.
	//
	// Daily battle rhythm (owner 2026-11-11): 10:00 ICT the day opens —
	// climb to floor 3 and DUMP every scrap of strength there, then park;
	// from 16:30 ICT the evening window opens and dumps freely up the
	// ladder. Before 10:00: no strength ops at all.
	//
	// BLIND JUMP-BUTTON LAW (owner 2026-11-10, replaces every local
	// condition read after "có acc thì đánh dư boss rồi vẫn không lên —
	// sync trạng thái không đúng"): the engine never asks swRegionConditions
	// whether a jump is due — it SPAMS the jump buttons and the server
	// arbitrates. After EVERY strength spend (every fight): floor 1/2 → one
	// free press; floor 3/4 → free press, 1s sleep, stone press
	// (swRegionStoneOK still guards the monsters-only floors 1→2→3). The
	// ONLY floor-state truth is the freshest playerData on the wire — every
	// resp carrying one updates pd, a ret==0 jump without one triggers an
	// ENTER_GAME refresh ("update trạng thái tầng hiện tại kịp thời").
	// 1s between strength spends ("cách nhau 1 giây, không cần vội").
	phase := swPhaseOf(swNowFunc())
	nextKnown := func(r int32) bool { // don't blind-jump past the top floor once folded
		return len(cfg.regionEnergy) == 0 || cfg.regionEnergy[r+1] > 0 || cfg.regionCond[r+1] != ""
	}
	// "trước 4h30 chiều chỉ được nằm max ở tầng 3" (owner 2026-11-11): the
	// farm/hold window NEVER leaves floor 3 — evaluated FRESH per press so a
	// mid-chain climb reaching floor 3 parks instantly; per-tick state
	// machine, not a one-shot cron.
	parkedNow := func() bool {
		return (phase == swPhaseFarm || phase == swPhaseHold) && swFloorPos(cfg, pd.GetRegionId()) >= 2
	}
	refreshPD := func() {
		if eg, err := api.swEnterGame(actID); err == nil && eg != nil && eg.GetRet() == 0 && eg.GetPlayerData() != nil {
			pd = eg.GetPlayerData()
		}
	}
	spamJump := func() {
		cur := pd.GetRegionId()
		if parkedNow() || !nextKnown(cur) {
			return
		}
		if nr, err := api.swNextRegion(actID, cur+1, false); err == nil && nr != nil && nr.GetRet() == 0 {
			if nr.GetPlayerData() != nil {
				pd = nr.GetPlayerData()
			} else {
				refreshPD() // keep the floor truth fresh
			}
			return
		} else {
			api.swLog("[TMYV] nhảy tầng %d→%d miễn phí bị từ chối (ret=%v) — bấm nút đá", cur, cur+1, retOrNil(nr, err))
		}
		// Stone door: ONLY from floor 3 up (floors 1→2→3 are monsters-only,
		// owner 2026-11-11). Blind-fire — a stoneless account just gets ret≠0.
		if swRegionStoneOK(cfg, cur) {
			swSleepFunc(time.Second)
			if nr, err := api.swNextRegion(actID, cur+1, true); err == nil && nr != nil && nr.GetRet() == 0 {
				api.swLog("[TMYV] lên tầng %d→%d bằng thương minh phi thạch", cur, cur+1)
				if nr.GetPlayerData() != nil {
					pd = nr.GetPlayerData()
				} else {
					refreshPD()
				}
			} else {
				api.swLog("[TMYV] nhảy tầng %d→%d bằng phi thạch bị từ chối (ret=%v)", cur, cur+1, retOrNil(nr, err))
			}
		}
	}
	budgetHit, fought := true, false
	drain := func() bool {
		for i := 0; i < swExploreBudget; i++ {
			r := pd.GetRegionId()
			need := cfg.regionEnergy[r]
			if need <= 0 {
				need = 1 // unknown cost → blind-poll law: let the server arbitrate
			}
			if pd.GetStrength() < need {
				budgetHit = false
				break
			}
			ex, err := api.swExplore(actID)
			if err != nil {
				return true
			}
			if ex.GetRet() != 0 {
				budgetHit = false
				break // exhausted / phase closed / cooldown — server decides
			}
			fought = true
			if ex.GetPlayerData() != nil {
				pd = ex.GetPlayerData()
			}
			ev := ex.GetEventData()
			for j := 0; j < swEventBudget && ev != nil && ev.GetEventId() != 0; j++ {
				next, ok := swResolveEvent(api, actID, ev, cfg)
				if !ok || next == nil || next.GetEventId() == 0 {
					break
				}
				ev = next
			}
			// "mỗi lần tiêu thể lực cách nhau 1 giây, không cần vội" +
			// spam the jump buttons after EVERY fight (blind, no condition
			// check — the server is the sole arbiter).
			swSleepFunc(time.Second)
			spamJump()
		}
		return false
	}
	// "từ 10h30 đến 16h30 tuyệt đối không tiêu thể lực" (owner 2026-11-11):
	// drain — the only strength spender — runs exclusively in the morning
	// farm window and the evening dump window.
	if phase == swPhaseFarm || phase == swPhaseDump {
		if drain() {
			return
		}
	}
	if pd != nil && phase != swPhaseSleep {
		// One blind press-pair when NOTHING was fought this tick (hold-window
		// free climbs below floor 3, drained/stuck floors, condition already
		// met from an earlier tick). After a fight the buttons were already
		// spammed inside the chain — no double press. parkedNow keeps floor-3 law.
		if !fought {
			spamJump()
		}
		// Exhaustion notice (once per day+region): the energy item is the
		// owner's BUTTON decision, never auto — nudge so they see it.
		// Live-gated: only a strength==0 that survived a refused explore
		// counts (the loop above breaks on refusal OR exhaustion; if the
		// budget broke instead, lastRet below tells us the server never
		// refused and the notice would be a lie — suppress it).
		if pd.GetStrength() <= 0 && !budgetHit {
			cur := pd.GetRegionId() // FRESH floor — pd is the only truth
			key := fmt.Sprintf("%s/%d", time.Now().In(ictZone).Format("2006-01-02"), cur)
			if api.swMarkExhaust(key) {
				api.swExhausted(cur)
			}
		}
	}
	// Union welfare: board, then blind-claim everything ready; ret is the
	// sole arbiter (blind-poll law). Linh lộ = "Nhận nhanh" claim-all, ONE
	// press per tick (owner 2026-11-11: "nhận linh lộ thì có nút nhận nhanh,
	// định kì click vào đó là được"): the client's quick-claim button is
	// reqUnionReceiveLiquid{id:0} — the SAME CommonPlantView pattern as
	// Luân Hồi's "Nhận nhanh" [[tytd-reborntrial-172]] rbtBonusItemAllBody
	// trap, id=0 MUST be present on the wire (proto3 would drop it; hence
	// optional int64 + pb.Int64). The old per-notice status==0 loop never
	// consumed anything (live: 341× 95a4, notices stayed status=0).
	if w, err := api.swWelfare(actID); err == nil && w != nil && w.GetRet() == 0 {
		if w.GetCanReceive() > 0 {
			api.swWelfareReward(actID)
		}
		api.swReceiveLiquid(actID, 0)
		// PHÁT linh lộ (owner 2026-11-11: "mặc định phát hết linh lộ đang
		// có, không phải giữ") — donate EVERY union-liquid item in the bag
		// to the union, one client-shaped press at a time until the server
		// says stop. [[tytd-item-ban]] AUTO exception (13).
		swDonateLiquid(api, actID)
	}
	// NẠP BANG milestone (owner 2026-11-11: "thêm nhận thưởng nạp bang,
	// thưởng nạp bang có linh lộ"): the union-recharge ladder of THIS event
	// — type-84 / 2xxxx condition rows settle per-id via 21045 (shape
	// captured live in Luân Hồi [[tytd-reborntrial-172]], same 0415 door);
	// its rewards include linh lộ, so it joins the welfare round. Blind,
	// server ret sole arbiter (idempotent — received rows never re-settle);
	// 5-min cadence inside the twin.
	api.swCondSweep(actID)
	// Assist teammates + harvest finished assist rewards (owner
	// 2026-11-09: "khi thấy có đồng đội cần hỗ trợ thì vào hỗ trợ luôn").
	swAssistSweep(api, actID)
	// Shop: auto-buy free/ad-watch/tiên-ngọc rows only (owner
	// 2026-11-09; same [[tytd-item-ban]] exception shape as LHBC 21005).
	swShopSweep(api, actID)
	api.swHeartBeat(actID)
}

// swAssistSweep reads the assist board and acts like the client's GameLog
// panel: a teammate FIGHT entry whose monster/player still has hp>0 gets an
// assist (234910 type=0 — the server runs the help battle and answers a
// battleRecordMsg); a teammate BOX entry short of helpers
// (helpUsers < OPEN_HELP_BOX param) gets one help tap (type=1); every
// FINISHED entry (fight hp<=0 / box filled) that has not been received yet
// — teammate's or my own — claims 234912. Own unfinished entries are never
// "assisted" (the client hides those buttons in the HELP tab); the server
// arbitrates the daily assistTimes cap through ret (blind-poll law).
func swAssistSweep(api swAPI, actID int32) {
	list, err := api.swAssistList(actID)
	if err != nil || list == nil || list.GetRet() != 0 {
		return
	}
	self := api.swSelf()
	need := api.swCfg().helpBoxNeed
	for _, e := range list.GetData() {
		if e == nil {
			continue
		}
		switch e.GetType() {
		case swAssistFight:
			ai := e.GetAssistInfos()
			if ai == nil || ai.GetAssistId() == 0 {
				continue
			}
			// Client: isFinishFightHelp = t.hp && Number(t.hp)<=0 — an
			// absent hp string means NOT finished, never "0 ≤ 0".
			finished := ai.GetHp() != ""
			if finished {
				if v, perr := strconv.ParseFloat(ai.GetHp(), 10); perr != nil || v > 0 {
					finished = false
				}
			}
			switch {
			case ai.GetPlayerId() != self && !finished:
				if rsp, err := api.swAssist(actID, swAssistFight, ai.GetPlayerId(), ai.GetAssistId()); err == nil && rsp != nil && rsp.GetRet() != 0 {
					api.swLog("[TMYV] hỗ trợ chiến đấu %s bị từ chối (code %d)", ai.GetNickName(), rsp.GetRet())
				}
			case finished && !ai.GetIsReceive():
				if rsp, err := api.swAssistReward(actID, swAssistFight, ai.GetAssistId()); err == nil && rsp != nil && rsp.GetRet() != 0 {
					api.swLog("[TMYV] nhận thưởng hỗ trợ chiến đấu bị từ chối (code %d)", rsp.GetRet())
				}
			}
		case swAssistBox:
			hb := e.GetHelpBoxInfos()
			if hb == nil || hb.GetAssistId() == 0 {
				continue
			}
			helped := int32(len(hb.GetHelpUsers()))
			switch {
			case hb.GetPlayerId() != self && need > 0 && helped < need:
				if rsp, err := api.swAssist(actID, swAssistBox, hb.GetPlayerId(), hb.GetAssistId()); err == nil && rsp != nil && rsp.GetRet() != 0 {
					api.swLog("[TMYV] hỗ trợ rương %s bị từ chối (code %d)", hb.GetPlayerName(), rsp.GetRet())
				}
			case need > 0 && helped >= need && !hb.GetIsReceive():
				if rsp, err := api.swAssistReward(actID, swAssistBox, hb.GetAssistId()); err == nil && rsp != nil && rsp.GetRet() != 0 {
					api.swLog("[TMYV] nhận thưởng hỗ trợ rương bị từ chối (code %d)", rsp.GetRet())
				}
			}
		}
	}
}

// swShopSweep mirrors rbtShopSweep: every row that rbtMallAuto deems
// bot-buyable (price "0"/"X=0" free or ad-watch, or the "100000=" tiên ngọc
// pack) is bought up to its buyLimit/day; every other row (numeric money
// price) is structurally never sent. Refusals end the row until the next
// cycle — the server is the sole arbiter (cooldowns, jade balance).
func swShopSweep(api swAPI, actID int32) {
	rows, bought := api.swMall()
	if len(rows) == 0 {
		if !api.swPullMall() {
			return
		}
		rows, bought = api.swMall()
	}
	if len(rows) == 0 {
		return
	}
	if bought == nil {
		bought = map[int32]int64{}
	}
	// Ladder order: the "Ưu Đãi Phúc Lợi" rungs unlock sequentially (live:
	// next rung refuses 307 until its predecessor lands), so sweep lowest
	// Id first.
	sort.Slice(rows, func(i, j int) bool { return rows[i].GetId() < rows[j].GetId() })
	for _, mt := range rows {
		if mt == nil {
			continue
		}
		limit, ad, jade := rbtMallAuto(mt)
		if limit == 0 {
			continue
		}
		if !api.swShopReady(mt.GetId()) {
			continue
		}
		used := bought[mt.GetId()]
		for used < limit {
			rsp, err := api.swBuyMall(actID, mt.GetId())
			if err != nil || rsp == nil || rsp.GetRet() != 0 {
				api.swLog("[TMYV] shop: không mua được %s (%d/%d): err=%v ret=%v — nghỉ 10 phút",
					mt.GetName(), used+1, limit, err, rsp.GetRet())
				api.swMarkShopFail(mt.GetId())
				break
			}
			api.swMarkMallBought(mt.GetId())
			used++
			kind := "quà miễn phí"
			if ad {
				kind = "gói xem quảng cáo"
			} else if jade {
				kind = "gói tiên ngọc"
			}
			api.swShopNotify(fmt.Sprintf("🌊 Thương Minh Yêu Vực: đã mua %s (%s) %d/%d — quà: %s",
				kind, mt.GetName(), used, limit, rsp.GetRewards()))
		}
	}
}

// ─── Player twin ─────────────────────────────────────────────────────────────

func (p *Player) swLog(format string, args ...any) {
	log.Printf("["+p.Username+"] "+format, args...)
}

// swActiveActivity is the live type-349 instance; "reward" (End..EndShow)
// accepted too so late claims still land — marching dies naturally on
// ENTER refusal.
func (p *Player) swActiveActivity() (int32, bool) {
	now := time.Now()
	for _, e := range p.ActList.byType(swActivityType) {
		if st := e.status(now); st == "active" || st == "reward" {
			return e.ActivityID, true
		}
	}
	return 0, false
}

// swMergeConfig folds the type-349 slice of a 21003 answer: the energy item
// id (button), soulBall effect/type tables (forge priority) and the region
// energy/unlock conditions.
func (p *Player) swMergeConfig(data []byte) {
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
		// "Phát linh lộ" donate items (client getActivityConfigString
		// "SEAWAR_ITEM_ENERGY" → reqUnionSendLiquid{itemId}). CONFIG FOLD
		// ONLY — spending bag items is item-banned EXCEPT the authorized
		// AUTO donate [[tytd-item-ban]] (13). Parsed rbt-style
		// ("id|value;…", cf. RENBORNTRIAL_ITEM_ENERGY) so a multi-type
		// config folds ALL types; live <live-user> value is the plain single
		// "105531", while RECORD proves THREE tiers exist
		// (105531/105532/105533) — the rest come from the baked default.
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
			// getHelpBoxNeedHelpNum: param of the OPEN_HELP_BOX event row.
			// Same pass folds the event-id classifier for consumeCondition
			// (swRegionConditions: event side vs phi-thạch side).
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
	if cc := cc.GetConditionConfig(); len(cc) > 0 {
		p.swCondCfgs = cc
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

// atoiTrim parses a decimal config value tolerating spaces.
func atoiTrim(s string) (int32, error) {
	var n int32
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}

// swHandleFrame routes every 349 answer to the waiting seat and logs
// refusals; the USE_ITEM verdict (button-only) alerts the owner.
func (p *Player) swHandleFrame(msgType string, data []byte) {
	switch msgType {
	case SERVER_SW_USE_ITEM:
		var rsp proto.SeaWarUseItemResp
		if pb.Unmarshal(data, &rsp) == nil && rsp.GetRet() != 0 {
			p.sendNotify(fmt.Sprintf("🌊 Dùng vật phẩm thể lực Thương Minh Yêu Vực thất bại (code %d)", rsp.GetRet()))
		}
	case SERVER_SW_ASSIST_SYNC, SERVER_SW_ASSIST_SYNC_RAW:
		// 234913 push: teammate opened an assist/box call. The client only
		// uses it to refresh an OPEN board — we use it as a wake bell and
		// still poll 234909 for the truth (board is poll-only [[tytd-seawar-port]]).
		// Live: the server delivers this one as RAW low16 "95a1", not the
		// −200000 "8861" the answer frames use — both accepted.
		p.swLog("[TMYV] có tin đồng đội cần hỗ trợ — quét bảng ngay")
		p.swPingWake()
	case SERVER_SW_CHALLENGE_INFO_SYNC:
		// 234932 push: someone challenged a union — fold onto the war map's
		// fight ring (LHBC 4362 idiom; the map's red rings read this).
		var rsp proto.SeaWarChallengeInfoSync
		if pb.Unmarshal(data, &rsp) == nil {
			p.swNoteFight(&rsp)
		}
		return // push, no seat waiting on it
	}
	p.stDeliver(msgType, data)
}

// swUseItemOrder is the owner's authorized button path
// ([[tytd-item-ban]] exception, BUTTON ONLY): fires 234922 {activityId,num}
// once; the 886a verdict alerts on refusal (async order fire — success is
// silent). The auto engine NEVER calls this.
func (p *Player) swUseItemOrder(num int32) error {
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
		return errStGone
	case <-time.After(5 * time.Second):
		return errStGone
	}
	p.swLog("[TMYV] lệnh dùng %d vật phẩm thể lực (id %d)", num, p.swEnergyItemID())
	return nil
}

func (p *Player) swEnergyItemID() int32 {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if p.swEnergyItem > 0 {
		return p.swEnergyItem
	}
	return 105402 // client-shipped SEAWAR_ENERGY_ITEM default
}

// ─── Player swAPI implementation ─────────────────────────────────────────────

func (p *Player) swName() string { return p.Username }

// retOrNil reports a refused-op verdict for diagnostics (the typed resp is
// nil on transport errors).
func retOrNil[T interface {
	comparable
	GetRet() int32
}](resp T, err error) any {
	if err != nil {
		return err
	}
	var zero T
	if resp == zero {
		return "nil"
	}
	return resp.GetRet()
}

// swDonateLiquid empties the bag's linh lộ into the union (owner
// 2026-11-11 "mặc định phát hết linh lộ đang có, không phải giữ" — the
// [[tytd-item-ban]] AUTO exception (13)). v3 (owner same day "có 3 loại
// linh lộ, mày check cả 3 loại chưa"): linh lộ comes in THREE tiers —
// 105531/105532/105533 (RECORD leaderboard "105531=12|105533=16|
// 105532=12", same 3-liquid shape as Luân Hồi's union-tree water
// [[tytd-reborntrial-172]]) — while SEAWAR_ITEM_ENERGY only names ONE.
// The OLD single-item loop stopped the whole pass at 105531's bag-dry
// ret=101 and never touched the other two. Now every configured (else
// baked) id gets its own burst in turn; 101 means only THAT type is dry
// → move to the next.
// v2 law kept (owner "1 phút phát được có 1 linh lộ, phát cần phát hết
// luôn"): a refusal other than 101 is the server's donate cooldown —
// wait swDonateRefuseWait and press again, up to swDonateRefuseTries
// CONSECUTIVE (≈96s, crosses a full 1-minute cooldown). A pass that got
// ZERO successes parks for swDonatePark so a capped/no-union account
// never stalls the tick with backoff sleeps. swDonateBudget bounds the
// whole pass. Success is silent; refusals never log per press.
func swDonateLiquid(api swAPI, actID int32) {
	items := api.swLiquidItemIDs()
	if len(items) == 0 {
		return // no donate item configured — nothing to phát
	}
	if api.swDonatePaused(swNowFunc()) {
		return // parked after an all-refusal round — try again later
	}
	presses, successes := 0, 0
	for _, item := range items {
		refused := 0
		for presses < swDonateBudget {
			presses++
			ret, err := api.swSendLiquid(actID, item)
			if err == nil && ret == 0 {
				successes++
				refused = 0
				swSleepFunc(time.Second) // client-shaped pacing between clicks
				continue
			}
			if err == nil && ret == 101 {
				break // THIS type's bag dry — the other types still get their turn
			}
			refused++
			if refused >= swDonateRefuseTries || (successes == 0 && refused >= 3) {
				// Nothing (or nothing NEW) gets through at all — park
				// instead of burning backoff sleeps every tick.
				api.swDonatePause(swNowFunc().Add(swDonatePark))
				return
			}
			swSleepFunc(swDonateRefuseWait)
		}
		if presses >= swDonateBudget {
			api.swLog("[TMYV] phát linh lộ chạm ngưỡng %d lượt trong một vòng — dừng an toàn, tick sau phát tiếp", swDonateBudget)
			return
		}
	}
}

// swTap is a test-only wire tap (nil in production, one nil-check): every
// successful round-trip is offered to it as (answerType, requestBody,
// responseBody). Live tests use it to prove engine decisions on the wire
// without parsing the whole socket themselves.
var swTap func(ty string, req, resp []byte)

func (p *Player) swRT(cmd, wantType string, body []byte) ([]byte, error) {
	data, err := p.stRoundTrip(cmd, wantType, body, swReqTimeout)
	if swTap != nil && err == nil {
		swTap(wantType, body, data)
	}
	return data, err
}

func swCommonBody(actID int32) []byte {
	b, err := pb.Marshal(&proto.SeaWarCommonReq{ActivityId: actID})
	if err != nil { // structurally impossible for a scalar
		return nil
	}
	return b
}

func (p *Player) swEnter(actID int32) (*proto.SeaWarEnterResp, error) {
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

func (p *Player) swEnterGame(actID int32) (*proto.SeaWarEnterGameResp, error) {
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

func (p *Player) swExplore(actID int32) (*proto.SeaWarExploreResp, error) {
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

func (p *Player) swHandle(actID, evID, action int32, id int64) (*proto.SeaWarHandleEventResp, error) {
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

func (p *Player) swWorship(actID int32) {
	data, err := p.swRT(SW_WORSHIP_CMD, SERVER_SW_WORSHIP, swCommonBody(actID))
	if err != nil {
		return
	}
	var rsp proto.SeaWarWorshipResp
	if pb.Unmarshal(data, &rsp) == nil && rsp.GetRet() != 0 {
		p.swLog("[TMYV] bái bị từ chối (code %d)", rsp.GetRet())
	}
}

func (p *Player) swWelfare(actID int32) (*proto.SeaWarUnionWelfareResp, error) {
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

func (p *Player) swWelfareReward(actID int32) {
	_, _ = p.swRT(SW_UNION_WELFARE_REWARD_CMD, SERVER_SW_UNION_WELFARE_REWARD, swCommonBody(actID))
}

func (p *Player) swReceiveLiquid(actID int32, id int64) {
	body, err := pb.Marshal(&proto.SeaWarUnionLiquidRewardReq{ActivityId: actID, Id: pb.Int64(id)})
	if err != nil {
		return
	}
	_, _ = p.swRT(SW_UNION_RECEIVE_LIQUID_CMD, SERVER_SW_UNION_RECEIVE_LIQUID, body)
}

// swCondSweep claims THIS event's union-recharge (nạp bang) milestone rungs
// — the type-84 / 2xxxx condition rows of the 21003 fold — exactly like
// Luân Hồi's rbtCondSweep cross path [[tytd-reborntrial-172]]: per-id 21045
// {activityId, conditionId}, answer 0415 RspGetActivityConditionReward.
// The rewards include linh lộ (owner 2026-11-11). Blind law: the 21003
// stored values are known stale zeros, the server ret is the sole arbiter
// and the op is idempotent, so rows are pressed without any local check.
// 5-min cadence (swNowFunc) so a 1-min poll never spams the door.
func (p *Player) swCondSweep(actID int32) {
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
		// Blind pull: never skip because the fold hasn't happened yet.
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
			continue // token/other ladders are 21033's business — not ours
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
			continue // ret≠0 = nothing ready — quiet (blind-poll law)
		}
		name := cfg.GetDesc()
		if name == "" {
			name = cfg.GetTitle()
		}
		if len(cfg.GetParam()) > 0 {
			name = strings.ReplaceAll(name, "{0}", cfg.GetParam()[0])
		}
		p.sendNotify(fmt.Sprintf("🌊 Thương Minh Yêu Vực: đã nhận quà mốc nạp bang %s — %s", name, rewards))
	}
}

// swClaimCond receives one nạp-bang rung via 21045 (same door as Luân Hồi's
// rbtClaimCross — ReceiveCrossUnionRechargeRewardReq, answer 0415).
func (p *Player) swClaimCond(actID, condID int32) (string, error) {
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

// swSendLiquid fires ONE client-shaped donate press 234917 {activityId,
// itemId, num:1} — authorized AUTO spend [[tytd-item-ban]] (13); ret is
// the verdict (101 = bag empty).
func (p *Player) swSendLiquid(actID, itemID int32) (int32, error) {
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

func (p *Player) swLiquidItemIDs() []int32 {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if len(p.swLiquidItems) > 0 {
		out := make([]int32, len(p.swLiquidItems))
		copy(out, p.swLiquidItems)
		return out
	}
	// All THREE linh lộ tiers — RECORD leaderboard proved the union
	// accepts 105531, 105532 and 105533 ("105531=12|105533=16|105532=12",
	// live 2026-11-11). SEAWAR_ITEM_ENERGY only names the first.
	return []int32{105531, 105532, 105533}
}

func (p *Player) swDonatePaused(now time.Time) bool {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	return now.Before(p.swDonatePauseUntil)
}

func (p *Player) swDonatePause(until time.Time) {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	p.swDonatePauseUntil = until
}

func (p *Player) swHeartBeat(actID int32) {
	body, err := pb.Marshal(&proto.SeaWarHeartBeatReq{ActivityId: actID, ChallengeState: pb.Bool(false)})
	if err != nil {
		return
	}
	_, _ = p.swRT(SW_HEART_BEAT_CMD, SERVER_SW_HEART_BEAT, body)
}

// ─── chiến trường khiêu chiến (234931..234938) — ORDER-ONLY ─────────────────
// The guild-war family never rides the auto engine ([[tytd-item-ban]]
// ORDER-ONLY law, same as LHBC bang chiến): these packets are sent ONLY from
// the sanctioned web war-map order (webSeaWarWarOrder → EP swWarOrderOne)
// and from the read-only map pulls (challenge list / defenders). Attempt
// counts are never a local gate (owner 2026-11-11 "không cần check xem có
// lượt hay không" — the server ret is the sole arbiter).

// swChallengeList pulls the union-base board (234931): one node per rival
// union + the canBattleUnionIds set + freeNormalTimes (display only — the
// order path never gates on it).
func (p *Player) swChallengeList(actID int32) (*proto.SeaWarChallengeListResp, error) {
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

// swEnterChallenge opens one union base (234936) — rooms arrive in
// declarationInfos (roomId per challenger row).
func (p *Player) swEnterChallenge(actID int32, unionID int64) (*proto.SeaWarEnterChallengeResp, error) {
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

// swRoomInfo pulls one room's defenders (234937).
func (p *Player) swRoomInfo(actID int32, unionID int64, roomID int32) (*proto.SeaWarRoomInfoResp, error) {
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

// swChallenge fires ONE attack (234938). Blind-fire law: no local attempt
// check — a no-attempt account simply gets ret≠0 and nothing is spent
// (attacking itself costs no items; the fruit/ticket bans stay intact).
func (p *Player) swChallenge(actID int32, unionID int64, roomID int32, playerID int64) (*proto.SeaWarChallengeResp, error) {
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

// swDeclaration 宣战 234944 — UNION-level: one member declaring opens the
// base for the whole bang (owner 2026-11-11), so NO bot path ever presses
// it (kept for probes/manual only; 95c0 hard-banned in the live writers).
// type 0 = lệnh thường (isFree=false burns vé 105404), type 1 = lệnh cao
// cấp (burns vé 105405) — both item-banned [[tytd-item-ban]].
func (p *Player) swDeclaration(actID int32, unionID int64, typ int32, isFree bool) (*proto.SeaWarDeclarationResp, error) {
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

// swAttackRec is one challenge push (8874 SeaWarChallengeInfoSync) folded
// onto the war map's fight ring — same shape as rbtAttackRec.
type swAttackRec struct {
	Name     string
	Union    string
	TargetID int64
	Damage   string
	At       time.Time
}

// swNoteFight appends a challenge push to the ring (guarded by SwMu).
const swFightLogCap = 60
const swAttackWindow = 30 * time.Minute

func (p *Player) swNoteFight(rsp *proto.SeaWarChallengeInfoSync) {
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

func (p *Player) swAttacksSnapshot() []swAttackRec {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	return append([]swAttackRec{}, p.swAttacks...)
}

func (p *Player) swNextRegion(actID, next int32, useItem bool) (*proto.SeaWarNextRegionResp, error) {
	// useItem PRESENT on the wire either way (proto2-required on the server —
	// omitting it breaks their parse; rbtBonusItemAllBody precedent).
	// useItem=true burns thương minh phi thạch — gated by swRegionStoneOK
	// at the ONLY call site (floors 3↑, owner 2026-11-10).
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

func (p *Player) swCfg() swCfgView {
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

func (p *Player) swMarkWorship(day string) bool {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if p.swWorshipDay == day {
		return false
	}
	p.swWorshipDay = day
	return true
}

func (p *Player) swMarkExhaust(key string) bool {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if p.swExhaustKey == key {
		return false
	}
	p.swExhaustKey = key
	return true
}

func (p *Player) swExhausted(region int32) {
	p.sendNotify(fmt.Sprintf("🌊 Thương Minh Yêu Vực: hết thể lực ở tầng %d — bấm /thuongminh_suc để dùng vật phẩm (bot không tự dùng).", region))
}

func (p *Player) swSelf() int64 { return int64(p.UID) }

func (p *Player) swAssistList(actID int32) (*proto.SeaWarAssistListResp, error) {
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

func (p *Player) swAssist(actID, typ int32, targetPlayerID, id int64) (*proto.SeaWarAssistRsp, error) {
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

func (p *Player) swAssistReward(actID, typ int32, id int64) (*proto.SeaWarAssistRewardRsp, error) {
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

func (p *Player) swMall() ([]*proto.ActivityMallTempMsg, map[int32]int64) {
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

// swPullMall asks for one 21003 answer (the reader folds rows + bought
// counts through swMergeConfig before the seat gets the bytes).
func (p *Player) swPullMall() bool {
	body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: p.swActiveActivityMust()})
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

// swActiveActivityMust is the loop-context convenience: sweeps only ever
// run inside a tick, where the activity is open.
func (p *Player) swActiveActivityMust() int32 {
	actID, _ := p.swActiveActivity()
	return actID
}

func (p *Player) swBuyMall(actID, mallID int32) (*proto.RspBuyActivityGoods, error) {
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

func (p *Player) swMarkMallBought(mallID int32) {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if p.swMallAdds == nil {
		p.swMallAdds = map[int32]int64{}
	}
	p.swMallAdds[mallID]++
}

func (p *Player) swShopNotify(text string) { p.sendNotify(text) }

// swShopReady / swMarkShopFail — refused rows rest swShopFailWait so a
// ladder-locked rung (307 live) isn't re-fired every tick; the map resets on
// the ICT day rollover like the bought-counts.
func (p *Player) swShopReady(mallID int32) bool {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	today := time.Now().In(ictZone).Format("2006-01-02")
	if p.swShopDay != today {
		p.swShopDay, p.swShopFails = today, nil
	}
	return p.swShopFails[mallID].Before(time.Now())
}

func (p *Player) swMarkShopFail(mallID int32) {
	p.SwMu.Lock()
	defer p.SwMu.Unlock()
	if p.swShopFails == nil {
		p.swShopFails = map[int32]time.Time{}
	}
	p.swShopFails[mallID] = time.Now().Add(swShopFailWait)
}

// ─── wake bell ───────────────────────────────────────────────────────────────
// The server pushes 234913 SeaWarAssistSync when a teammate opens an assist
// (fight/box) call. The real client only refreshes an OPEN board from it; we
// ring it into the loop's sleep so the next poll lands within seconds. The
// board truth stays poll-only (234909) — the push is a hint, never trusted.

func (p *Player) swPingWake() {
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

// swSleepPoll waits d, or returns early on a push wake — clamped so ticks
// never start closer than swMinTick apart (a push storm cannot hot-loop).
func (p *Player) swSleepPoll(d time.Duration, tickStart time.Time) bool {
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

// ─── auto loop ───────────────────────────────────────────────────────────────

func (p *Player) seaWarAutoLoop() {
	p.seaWarAutoLoopAfter(8 * time.Second)
}

func (p *Player) seaWarAutoLoopAfter(settle time.Duration) {
	if !p.rbtSleepCtx(settle) {
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
		p.sendNotify(fmt.Sprintf("🌊 Thương Minh Yêu Vực: %s — trạng thái này %s",
			statusNow, stFmtDur(time.Since(stateSince))))
		nextStatus = time.Now().Add(swStatusGap)
	}
	for {
		if !p.AutoSeaWar {
			record("tắt — bật bằng /auto_thuongminh true")
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
		statusNow = fmt.Sprintf("đang yêu vục tầng (act %d)", actID)
		record(statusNow)
		tickStart := time.Now()
		swPlayTick(p, actID)
		beat()
		if !p.swSleepPoll(swPoll, tickStart) {
			return
		}
	}
}
