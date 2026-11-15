package tytd_go

/*
Auto Luân Hồi Bí Cảnh (RebornTrial, systemOpenId 172) — the
/auto_luanhoi feature, one loop per login, gated by the account's opt-in DB
switch, modeled on the Tiên Vực Thương Đồ caravan loop (player_thiendo.go).

The game (protocol decoded live 2026-10-08 from the client's
protobuf/RebornTrial asset — see ../remoteRebornTrial/RebornTrial.decoded.proto
and the memory note [[tytd-reborntrial-172]]): a caravan roguelike run in
season rounds. Time structure, offsets from the activity's begin (ms):

	SignIn  0h → 24h        Prepare 24h → 34h
	Battle  34h → 46h (round 1)   58h → 70h (round 2)   82h → 94h (round 3)

Map play exists INSIDE A BATTLE ROUND ONLY — but all day once the round is
open (live 2026-10-09: ENTER/AUTO succeed at 11:05 ICT, hours before the
19|22 param window). The earlier 19|22-as-map-gate reading was wrong; the
15001 refusals seen overnight were the PREPARE phase, not the hour. The
loop plays every poll inside a round and lets per-command refuse codes
(15001 phase, 17221 worship-slot) speak for themselves. The "19|22" param
still merges (RENBORNTRIAL_CHALLENGE_DURATION) as metadata.
Qualification is server-side too: ENTER_ACTIVITY carries unionId (f6) and
selfRank (f2); a lone account (unionId 0) or rank > JOIN_LIMIT never gets in
(the loop reports it once per round and stays quiet).

In-window gameplay per tick:
  1. ENTER (217201) → playerData: level, exp, energy hp, pending event,
     free challenges left (battleNum). ret 15001 → window actually closed:
     sleep to the next one, no retries.
  2. A pending event (event.stat == 0) blocks the road: resolve it with
     SELECT_OPTION (217206, selectIndex = option.order — order is 0-based,
     equal to the option array position, client-proven). Non-bet events
     try the FIRST option whose costExp the account's exp can pay; the
     server's refusal codes retire options, everything unaffordable →
     SKIP_EVENT (217209, free abandon). Which option is tried FIRST follows
     the owner strategy table from tinhnang.jpg (rbtStrategySort: bet &
     alchemy wager the highest affordable exp, the rest cycle a per-type
     preference). The answers, not local math, move the books (same
     philosophy as the caravan's snapshot-first rule).
  3. EXPLORE (217205) step by step while the answer is ret 0; the refusal
     (energy spent / window closed) ends the march until the next poll.
  4. WORSHIP (217203) once per round — free reward from the round's top
     guild. The guild war (bracket list 217230, attack 217233 — opcode
     numbering live-probed 2026-10-09) is ORDER-ONLY per owner 2026-11-08
     ("đánh bang chiến chỉ đánh ở event thôi, theo lệnh của player trên
     mini map" — retracting the same-day AUTO call): the regular account
     NEVER fights; on the owner's ⚔️ map order (or /luanhoi_attack) every
     running EVENT account challenges the chosen base with FREE attempts
     (t.rbtOrderAttack → EP rbtWarOrderOne), and "/luanhoi_ve" on the main
     account stays the only ticket bite [[tytd-item-ban]].
  5. GUESS_REWARD when achieved ranks await, nothing else. The UNION
     TREE lives OUTSIDE the window gate: rbtTreeCycle runs on its own
     10-minute cadence in every phase (read board 217220, harvest fruit
     217221, collect ready water 217222 id=0 — all pure receives).

HARD RULES [[tytd-item-ban]]: USE_ITEM (217208) is sent ONLY to eat the
energy fruit (rbtEatEnergy — owner "tự động cắn quả tăng thể lực đi"
2026-10-09): ONLY the config-folded RENBORNTRIAL_ENERGY_ITEM_ID, ONLY after
a march refusal below the stamina cap, ≤5/tick, and NEVER before a real
21003 detail folded the id (unset = ban intact). The challenge ticket
(100160) stays command-only "/luanhoi_ve". SEND_BONUS_ITEM (217223) is sent
ONLY to donate tree water (rbtLiquidGrant — owner "cần phát sạch cả 3 loại"
2026-10-10), ONLY the ids folded from RENBORNTRIAL_ITEM_ENERGY (unset =
never). The loop NEVER sends
UP_LEVEL (217204, dead client API) or the guess-bet SELECTION (217227 —
betting on picks is a judgment call, not free play). USE_MAGIC (217207)
IS used (user "có gì dùng hết đi" 2026-10-09): cooldown-bound treasure ops
repair/illusion/suppress — no items, no exp; AUTO_EXPLORE already lets the
server use treasures per the client's own default (both switches on).
AUTO_EXPLORE (217251) exists as a free server-side batch with the client's
own default settings table; it is only tried once per tick and any refusal
falls back to the manual march above.

Every decision recomputes from a fresh ENTER, so a server push (round
flip, window close) changes the next tick, not our ledger.
*/

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const (
	rbtActivityType = 172

	rbtPoll        = time.Minute     // in-window decision cycle
	rbtStatusGap   = 5 * time.Minute // heartbeat while actually playing
	rbtBusyWait    = 30 * time.Second
	rbtIdleWait    = 10 * time.Minute // sleep cap outside gameplay windows
	rbtReqTimeout  = 15 * time.Second
	rbtStepBudget  = 60              // explores per tick — sanity cap, never a goal
	rbtEventBudget = 30              // event resolves per tick
	rbtDayChallMax = 5               // server default 3; cap covers the param=5 table
	rbtTreeGap     = 5 * time.Minute // union-tree/milestone cadence (window-independent; owner 2026-11-07 "5 phút nhận 1 lần")

	rbtEvBet = 7 // RebornTrialEventType BET — bet nodes need an actual wager (exp) to pass

	rbtPreCloseGap = 4 * time.Minute // owner 2026-10-09: one guaranteed run at round end −4 min (21:56 ICT when the round closes 22:00)
)

// ─── owner strategy table (tinhnang.jpg, owner-provided 2026-10-09) ─────────
// Client enum: Buy=1 Sell=2 Suppress=3 Relieve(BREAK)=4 Rescue=5
// Alchemy(SMELT)=6 Bet=7 Give=8; the server adds BuyFake=20 SellFake=21
// UseMagic=22. The image's "Sự cố" column names an NPC personality /
// sub-scene (bệnh nặng, hào phóng, bẩn xin …) whose ids live in the game's
// binary DB table (RebornTrial.pb.asset.json 404s — no decoded table
// exists), so sub-scene rows cannot be keyed. Where a row contradicts
// another (the three Buy-flavoured rows), the FIRST-listed variant is used
// and noted below. The table only decides which option is tried FIRST —
// the normal affordability/refusal fan still runs behind it, so a wrong
// preference can never stall a march.
const (
	rbtEvBuy      = 1
	rbtEvSell     = 2
	rbtEvSuppress = 3
	rbtEvRelieve  = 4 // độ kiếp — BREAK, handled by its own auto path
	rbtEvRescue   = 5
	rbtEvAlchemy  = 6 // SMELT — luyện đan

	// Fake trades (server-only enums): Lừa đảo / Bán lỗi.
	rbtEvBuyFake  = 20
	rbtEvSellFake = 21
)

// rbtStrategySeq maps an event type to the 1-BASED option numbers to cycle
// across successive encounters ("2 2 3" = pick 2, next time 2, next time 3).
var rbtStrategySeq = map[int32][]int32{
	rbtEvBuy:      {1},       // Trao đổi (NPC bán cho bạn): thử phương án 1 — từ chối, giữ sức lực
	rbtEvSell:     {1, 2, 3}, // Bạn bán cho NPC — dòng "giai đoạn đầu" (biến thể "trói Tiên" 1133/1233 cần sự cố chưa giải mã được)
	rbtEvSuppress: {1, 1, 1}, // Trấn áp quái vật / phá cấm kỵ: luôn 1 — CỐ TÌNH giữ vật phẩm ma thuật cấp thấp đầy kho
	rbtEvRescue:   {2, 2, 3}, // Chữa bệnh và cứu sống — dòng "Bệnh nặng" (các sự cố khác ghi 123/112 — không phân biệt được)
	// Owner 2026-10-09 "Lừa đảo / Bán lỗi: auto chọn lừa đảo": the cheat door
	// is the one with NO honest price — client-proven, only option order 1
	// renders a level-scaled cost (Text_219), so order 0 = free-but-risky
	// cheat, matching the tip "thất bại vẫn tốt hơn mất tiền". If a live
	// event shows the labels flipped, swap to {2} — the fan below still
	// falls back to the other door either way.
	rbtEvBuyFake:  {1},
	rbtEvSellFake: {1},
}

// rbtEvExpensive = "đánh bạc bằng đá và thuật giả kim — chỉ chọn cái đắt
// tiền, chứ không phải cái đúng": bet & alchemy try the HIGHEST affordable
// cost first. For alchemy this matches the live-proven truth (cheapest SMELT
// tiers ret0-no-op; higher tiers settle). Exp is not an item
// [[tytd-item-ban]] and betting stays owner-authorised (bet nodes always
// wagered, user OK).
var rbtEvExpensive = map[int32]bool{rbtEvBet: true, rbtEvAlchemy: true}

// rbtPreferOrder returns the 1-based preferred option for this event type and
// advances the per-type cycling cursor (nil map = stateless: always seq[0]).
func rbtPreferOrder(seqs map[int32]int, evType int32) int32 {
	seq, ok := rbtStrategySeq[evType]
	if !ok {
		return 0
	}
	cur := seqs[evType]
	if seqs != nil {
		seqs[evType] = (cur + 1) % len(seq)
	}
	return seq[cur]
}

// rbtStrategySort orders candidate options per the owner table: expensive-
// first for bet/alchemy; otherwise preferred-option-first then order-
// ascending (EventOption.order is 0-based; the table is 1-based).
func rbtStrategySort(opts []*proto.RebornTrialEventOption, prefer int32, expensive bool) {
	if expensive {
		sort.Slice(opts, func(i, j int) bool { return opts[i].GetCostExp() > opts[j].GetCostExp() })
		return
	}
	sort.SliceStable(opts, func(i, j int) bool {
		a := opts[i].GetOrder()+1 == prefer
		b := opts[j].GetOrder()+1 == prefer
		if a != b {
			return a
		}
		return opts[i].GetOrder() < opts[j].GetOrder()
	})
}

// rbtWakeIn is the idle sleep before a known phase boundary (ICT — the
// schedule is UTC+7, owner rule): poll conservatively while the boundary is
// far, but the LAST wait lands exactly on the boundary so the round open
// (10:00 ICT) is never missed by up to a whole idle interval.
func rbtWakeIn(nextMs int64) time.Duration {
	if nextMs <= 0 {
		return rbtIdleWait
	}
	d := time.Until(time.UnixMilli(nextMs)) + 3*time.Second
	if d > rbtIdleWait {
		return rbtIdleWait
	}
	if d < 0 {
		return time.Second
	}
	return d
}

// rbtDefaultTimeCfg mirrors the live uniqueConfig phase table the client
// reads (type, begin-hours, duration-hours): SignIn/Prepare/three Battle
// rounds. Used until rbtMergeConfig folds the real one.
var rbtDefaultTimeCfg = [][3]int{{1, 0, 24}, {2, 24, 10}, {3, 34, 12}, {3, 58, 12}, {3, 82, 12}}

// rbtWindowTest overrides the daily-window clock in tests (nil = real clock).
// Kept as a package var so the loop and tests share one injection point.
var rbtWindowTest func(now time.Time) bool

// rbtState is the coarse phase the account currently finds itself in.
type rbtState int

const (
	rbtNone   rbtState = iota // activity window closed
	rbtPrep                   // SignIn/Prepare: no map play server-side
	rbtBattle                 // inside a Battle round (window check separate)
	rbtDone                   // activity over (rewards/settlement)
)

// rbtPhase computes the current state, battle round number (1..3) and the
// start of the NEXT state boundary (ms) from the activity begin and the
// phase table — pure, mirrors the client's getRebornTrialTimeState.
func rbtPhase(beginMs int64, cfg [][3]int, now time.Time) (rbtState, int, int64) {
	if len(cfg) == 0 {
		cfg = rbtDefaultTimeCfg
	}
	h := float64(now.UnixMilli()-beginMs) / 3600000.0
	round := 0
	var nextMs int64
	for _, e := range cfg {
		begin, end := float64(e[1]), float64(e[1]+e[2])
		if h < begin {
			nextMs = beginMs + int64(begin*3600000)
			if round > 0 {
				return rbtPrep, round, nextMs // battle-over gap (rest)
			}
			if h < 0 {
				return rbtNone, 0, nextMs
			}
			return rbtPrep, 0, nextMs
		}
		if h < end {
			if e[0] == 3 {
				round++
				return rbtBattle, round, beginMs + int64(end*3600000)
			}
			return rbtPrep, 0, beginMs + int64(end*3600000)
		}
		if e[0] == 3 {
			round++
		}
	}
	return rbtDone, 0, 0
}

// rbtDailyWindow reports whether now (ICT, the schedule's wall time
// [[tytd-times-utc7]]) is inside the daily battle window openSec..closeSec
// (seconds from local midnight), and when the window next opens.
func rbtDailyWindow(now time.Time, openSec, closeSec int) (bool, time.Time) {
	if rbtWindowTest != nil {
		// Tests drive the clock wholesale; the next-open hint is not used.
		return rbtWindowTest(now), now.Add(rbtIdleWait)
	}
	vn := now.In(ictZone)
	mid := time.Date(vn.Year(), vn.Month(), vn.Day(), 0, 0, 0, 0, ictZone)
	secs := int(vn.Sub(mid).Seconds())
	open := mid.Add(time.Duration(openSec) * time.Second)
	if secs >= openSec && secs <= closeSec {
		return true, open.AddDate(0, 0, 1) // hint = tomorrow's open (for closing)
	}
	if !open.After(vn) {
		open = open.AddDate(0, 0, 1) // today's window already passed
	}
	return false, open
}

// rbtActivityBody packs the {activityId} request shared by a dozen opcodes.
func rbtActivityBody(actID int32) []byte {
	b, err := pb.Marshal(&proto.RebornTrialActivityReq{ActivityId: actID})
	if err != nil { // structurally impossible for a scalar
		return nil
	}
	return b
}

// rbtRoundTrip reuses the sky-trade seat plumbing (stClaim/stDeliver are
// generic over msgType): claim the answer seat, send, wait.
func (p *Player) rbtRoundTrip(cmd, wantType string, body []byte) ([]byte, error) {
	return p.stRoundTrip(cmd, wantType, body, rbtReqTimeout)
}

func (p *Player) rbtEnterActivity(actID int32) (*proto.RebornTrialEnterActivityResp, error) {
	data, err := p.rbtRoundTrip(RBT_ENTER_ACTIVITY_CMD, SERVER_RBT_ENTER_ACTIVITY, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialEnterActivityResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) rbtEnter(actID int32) (*proto.RebornTrialDataResp, error) {
	data, err := p.rbtRoundTrip(RBT_ENTER_CMD, SERVER_RBT_ENTER, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialDataResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) rbtExplore(actID int32) (*proto.RebornTrialDataResp, error) {
	data, err := p.rbtRoundTrip(RBT_EXPLORE_CMD, SERVER_RBT_EXPLORE, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialDataResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) rbtSelectOption(actID, order int32) (*proto.RebornTrialSelectOptionResp, error) {
	body, err := pb.Marshal(&proto.RebornTrialSelectOptionReq{ActivityId: actID, SelectIndex: order})
	if err != nil {
		return nil, err
	}
	data, err := p.rbtRoundTrip(RBT_SELECT_OPTION_CMD, SERVER_RBT_SELECT_OPTION, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialSelectOptionResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) rbtSkipEvent(actID int32) (*proto.RebornTrialSkipEventResp, error) {
	data, err := p.rbtRoundTrip(RBT_SKIP_EVENT_CMD, SERVER_RBT_SKIP_EVENT, rbtActivityBody(actID))
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
func (p *Player) rbtBuyGoods(actID, mallID int32) (*proto.RspBuyActivityGoods, error) {
	body, err := pb.Marshal(&proto.ReqBuyActivityGoods{ActivityId: actID, MallId: mallID, Count: 1})
	if err != nil {
		return nil, err
	}
	data, err := p.rbtRoundTrip(ACTIVITY_BUY_GOODS_CMD, SERVER_ACTIVITY_BUY_GOODS, body)
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
func rbtMallAuto(mt *proto.ActivityMallTempMsg) (limit int64, ad, jade bool) {
	pr := mt.GetPrice()
	free := pr == "0" || strings.HasSuffix(pr, "=0")
	xianyu := strings.HasPrefix(pr, "100000=")
	if !free && !xianyu {
		return 0, false, false
	}
	limit = int64(mt.GetBuyLimit())
	if limit < 1 {
		limit = 1
	}
	return limit, strings.HasPrefix(mt.GetParam(), "3|"), !free
}

// rbtShopSweep buys every auto-buyable shop row up to its daily cap.
// Server-side refusals (ad cooldown 1800s, insufficient tiên ngọc) simply
// end this row until the next cycle; mallBuyCountList refreshes hourly via
// 21003 and the day boundary resets the local adders.
func (p *Player) rbtShopSweep(actID int32) {
	// The shop table only exists after one 21003 answer was merged; nothing
	// else pulls detail for a 172 activity, so ask for it once per sweep.
	p.RbtMu.Lock()
	empty := len(p.rbtMallRows) == 0
	p.RbtMu.Unlock()
	if empty {
		body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID})
		if err == nil {
			if _, err := p.rbtRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err != nil {
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
func (p *Player) rbtClaimType(actID, typ int32) (string, error) {
	body, err := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(typ)})
	if err != nil {
		return "", err
	}
	data, err := p.rbtRoundTrip(ACTIVITY_TOKEN_REWARD_CMD, SERVER_ACTIVITY_TOKEN_REWARD, body)
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
func (p *Player) rbtClaimCross(actID, condID int32) (string, error) {
	body, err := pb.Marshal(&proto.ReceiveCrossUnionRechargeRewardReq{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(condID)})
	if err != nil {
		return "", err
	}
	data, err := p.rbtRoundTrip(ACTIVITY_CROSS_RECHARGE_CMD, SERVER_ACTIVITY_CROSS_RECHARGE, body)
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
//
// ret≠0 ⇒ quiet, no spam beyond one attempt per sweep pass.
func (p *Player) rbtCondSweep(actID int32) {
	p.RbtMu.Lock()
	cfgs := append([]*proto.ActivityConditionConfig{}, p.rbtCondCfgs...)
	p.RbtMu.Unlock()
	if len(cfgs) == 0 {
		// Blind pull (owner 2026-11-07): never skip the sweep because the
		// fold hasn't happened — 21003's answer merges rbtCondCfgs through
		// the reader, so one ask per cycle arms every later pass too.
		if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
			if _, err := p.rbtRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err != nil {
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
// The 21033/21045/21004 doors were ALL wrong for this ladder
// (210/202/silent — live probes 1–3). The server claims rungs in ascending
// order and stops at the first not-yet-reached one (ret=203, live: one
// batch flipped exactly the 7 reached rungs + seven 03ef syncs), so one
// blind frame per cycle settles every ready rung. rewards is only filled
// on an all-success batch; otherwise 21003's isGetReward diff names what
// actually landed.
func (p *Player) rbtLadderSweep(actID int32) {
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
	data, err := p.rbtRoundTrip(ACTIVITY_COND_REWARD_ARR_CMD, SERVER_ACTIVITY_COND_REWARD_ARR, body)
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
	// ret=203 (stopped at an unreached rung) still claimed its leading
	// ready rungs — read the truth back through 21003 and diff.
	if b, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if _, err := p.rbtRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, b); err != nil {
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
func (p *Player) rbtUseMagic(actID, magicType, goodsID int32) (*proto.RebornTrialUseMagicResp, error) {
	body, err := pb.Marshal(&proto.RebornTrialUseMagicReq{ActivityId: actID, MagicType: magicType, GoodsId: goodsID})
	if err != nil {
		return nil, err
	}
	data, err := p.rbtRoundTrip(RBT_USE_MAGIC_CMD, SERVER_RBT_USE_MAGIC, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialUseMagicResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) rbtLimits() ([]int32, []int32) {
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
func (p *Player) rbtTreasureSweep(actID int32, pd *proto.RebornTrialPlayerData) {
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
func (p *Player) rbtEventMagic3(actID int32, pd *proto.RebornTrialPlayerData) bool {
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

func (p *Player) rbtWorship(actID int32) (*proto.RebornTrialWorshipResp, error) {
	data, err := p.rbtRoundTrip(RBT_WORSHIP_CMD, SERVER_RBT_WORSHIP, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialWorshipResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) rbtBonusInfo(actID int32) (*proto.RebornTrialBonusResp, error) {
	data, err := p.rbtRoundTrip(RBT_BONUS_CMD, SERVER_RBT_BONUS, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialBonusResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) rbtBonusResult(actID int32) (*proto.RebornTrialGetBonusResultResp, error) {
	data, err := p.rbtRoundTrip(RBT_BONUS_RESULT_CMD, SERVER_RBT_BONUS_RESULT, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialGetBonusResultResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// rbtBonusItemAllBody hand-encodes {activityId, id=0} for 217222. The id
// field MUST be present on the wire: the client (protobufjs) encodes
// explicitly-set defaults, so its "Nhận nhanh" carries tag 2 = 0 (bytes
// 10 00) and the server honours it as claim-all — while a Go proto3
// marshal of Id:0 DROPS the field and the server answers SILENT.
// Live-proved 2026-11-07 on sonbinhsb@710564 (TestRebornTrialWaterAllLive):
// the two raw bytes claimed 510 ready notices in one call
// (reward "100009=278|100003=12500|100004=119|100038=635", board 510→0),
// while probe-2's proto3-marshal id=0 never answered.
func rbtBonusItemAllBody(actID int32) []byte {
	b, _ := pb.Marshal(&proto.RebornTrialGetBonusItemReq{ActivityId: actID})
	return append(b, 0x10, 0x00)
}

// rbtClaimBonusAll presses the client's "Nhận nhanh" button: GET_BONUS_ITEM
// id=0 (FORCED on the wire, see rbtBonusItemAllBody) collects every ready
// WATER notice on the union tree. Pure receive — the bot never SENDs
// liquid (217223 donates items [[tytd-item-ban]]).
func (p *Player) rbtClaimBonusAll(actID int32) (*proto.RebornTrialGetBonusItemResp, error) {
	data, err := p.rbtRoundTrip(RBT_BONUS_ITEM_CMD, SERVER_RBT_BONUS_ITEM, rbtBonusItemAllBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialGetBonusItemResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) rbtGuessData(actID int32) (*proto.RebornTrialGuessDataResp, error) {
	data, err := p.rbtRoundTrip(RBT_GUESS_DATA_CMD, SERVER_RBT_GUESS_DATA, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialGuessDataResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) rbtGuessReward(actID int32) (*proto.RebornTrialGuessRewardResp, error) {
	data, err := p.rbtRoundTrip(RBT_GUESS_REWARD_CMD, SERVER_RBT_GUESS_REWARD, rbtActivityBody(actID))
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
func (p *Player) rbtAutoExplore(actID int32) (*proto.RebornTrialAutoExploreRsp, error) {
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
	data, err := p.rbtRoundTrip(RBT_AUTO_EXPLORE_CMD, SERVER_RBT_AUTO_EXPLORE, body)
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
func (p *Player) rbtHandleFrame(msgType string, data []byte) {
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
	case SERVER_RBT_BATTLE_NOTIC:
		// Fight push (217234): who hit which base for how much — the /luanhoi
		// map paints these as red rings + popup lines ("thấy người đánh").
		var rsp proto.RebornTrialChallengeInfoSync
		if pb.Unmarshal(data, &rsp) == nil {
			p.rbtNoteFight(&rsp)
		}
	}
	p.stDeliver(msgType, data)
}

// rbtAttackRec is one fight notice (reader → map, guarded by RbtMu).
type rbtAttackRec struct {
	Name     string
	Union    string
	TargetID int64
	Damage   int64
	At       time.Time
}

// rbtFightLogCap bounds the ring — 30 min of war at frantic pace, ~5 KB.
const rbtFightLogCap = 60

// rbtNoteFight appends a push to the ring and prunes anything older than
// the map's attack window.
func (p *Player) rbtNoteFight(rsp *proto.RebornTrialChallengeInfoSync) {
	tu := rsp.GetTargetUnion()
	if tu == nil || tu.GetUnionId() == 0 {
		return
	}
	p.RbtMu.Lock()
	p.rbtAttacks = append(p.rbtAttacks, rbtAttackRec{
		Name: rsp.GetName(), Union: rsp.GetUnionName(),
		TargetID: tu.GetUnionId(), Damage: rsp.GetDamage(),
		At: time.Now(),
	})
	if cut := time.Now().Add(-rbtAttackWindow); len(p.rbtAttacks) > 0 {
		keep := p.rbtAttacks[:0]
		for _, a := range p.rbtAttacks {
			if a.At.After(cut) {
				keep = append(keep, a)
			}
		}
		p.rbtAttacks = keep
	}
	if len(p.rbtAttacks) > rbtFightLogCap {
		p.rbtAttacks = p.rbtAttacks[len(p.rbtAttacks)-rbtFightLogCap:]
	}
	p.RbtMu.Unlock()
}

// rbtAttackWindow: how long an attack stays visible on the map.
const rbtAttackWindow = 30 * time.Minute

// rbtAttacksSnapshot copies the ring for the map payload.
func (p *Player) rbtAttacksSnapshot() []rbtAttackRec {
	p.RbtMu.Lock()
	defer p.RbtMu.Unlock()
	return append([]rbtAttackRec{}, p.rbtAttacks...)
}

// rbtMergeConfig folds the daily battle window (RENBORNTRIAL_CHALLENGE_
// DURATION "19|22") and the phase table (uniqueConfig timeConfig) out of
// the hourly 21003 detail answer — same hook slot as stMergeConfig.
func (p *Player) rbtMergeConfig(data []byte) {
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
			// The "/luanhoi_ve" ticket (challenge-count item). Client ships
			// default 100160; the activity config may override it.
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

func (p *Player) rbtWindow() (int, int) {
	p.RbtMu.Lock()
	o, c := p.rbtDailyOpenSec, p.rbtDailyCloseSec
	p.RbtMu.Unlock()
	if o == 0 || c == 0 {
		return 19 * 3600, 22 * 3600 // live-verified client default
	}
	return o, c
}

func (p *Player) rbtCfg() [][3]int {
	p.RbtMu.Lock()
	defer p.RbtMu.Unlock()
	return p.rbtTimeCfg
}

// rbtActiveActivity is the live type-172 instance (whole-season window;
// phases and the daily gate are layered on top by the loop).
// Accepts "reward" too — see the EventPlayer twin: the post-season
// reward-collect window (End..EndShow) still has claimable shop/tree/
// milestone/rung rewards; marching stays calendar-gated by rbtPhase.
func (p *Player) rbtActiveActivity() (int32, int64, bool) {
	now := time.Now()
	for _, e := range p.ActList.byType(rbtActivityType) {
		if st := e.status(now); st == "active" || st == "reward" {
			return e.ActivityID, e.BeginTime, true
		}
	}
	return 0, 0, false
}

// rbtNotePD caches the live battle counters (challenge attempts left +
// recover anchor) out of any answer that carries playerData, so the /luanhoi
// map HUD shows them without extra wire.
func (p *Player) rbtNotePD(pd *proto.RebornTrialPlayerData) {
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

func (p *Player) rbtSleepCtx(d time.Duration) bool {
	select {
	case <-p.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (p *Player) rbtPollWait() time.Duration {
	if p.rbtPoll > 0 {
		return p.rbtPoll
	}
	return rbtPoll
}

func (p *Player) rbtBeatGap() time.Duration {
	if p.rbtStatusGap > 0 {
		return p.rbtStatusGap
	}
	return rbtStatusGap
}

// ─── gameplay tick ───────────────────────────────────────────────────────────

// rbtPlayTick drives one in-window wake. Returns false only for connection
// loss; refusals are logged and leave the loop on its normal pacing.
func (p *Player) rbtPlayTick(actID int32, round int) bool {
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

// rbtRankSweep claims the season RANK settlement reward (21006 GET_RANK_
// REWARD, answer 03ee — "thưởng hạng" of the season-end panel, owner
// 2026-11-08 "thêm auto nhận reward"). One 21006 per folded rankConfig
// type (live 172: 1 = cá nhân, 2 = bang) — config-armed like the liquid
// grant: NOTHING folded ⇒ opcode NEVER sent (the shop sweep's first-call
// 21003 pull folds it before this runs in the same cycle). BLIND within
// the folded set: the server ret is the sole arbiter — ret=0 with EMPTY
// rewards is a no-op (±0 lesson of 217233, never notify it), ret=0 WITH
// rewards means items were actually granted (live: rankType1 →
// "100152=2|100162=4|…", rankType2 → "100025=15|…|100004=60"). Pure
// receive — never a spend [[tytd-item-ban]].
func (p *Player) rbtRankSweep(actID int32) {
	p.RbtMu.Lock()
	types := append([]int32(nil), p.rbtRankTypes...)
	p.RbtMu.Unlock()
	for _, rt := range types {
		body, err := pb.Marshal(&proto.ReqGetRankReward{ActivityId: actID, RankType: rt})
		if err != nil {
			return
		}
		data, err := p.rbtRoundTrip(ACTIVITY_RANK_REWARD_CMD, SERVER_ACTIVITY_RANK_REWARD, body)
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

// rbtTreeCycle tends the union tree exactly like the bot's other events
// (Côn Lôn/Bồng Lai watering idiom): donate liquid (217223 — all three
// water types, everything held, ONLY ids folded from
// RENBORNTRIAL_ITEM_ENERGY — owner 2026-10-10 exception to
// [[tytd-item-ban]]; no config folded = opcode never sent), harvest the
// fruit (217221), and press the client's "Nhận nhanh" water-collect
// (217222 id=0 FORCED on the wire — rbtBonusItemAllBody; a proto3-marshal
// id=0 has no field 2 and the server ignores it, live 2026-11-07: one
// press swept 510 notices, ret=0, and answers ret=0 reward="" when empty).
// All three fire BLIND each cycle — the server's ret is the only arbiter
// (2026-11-07 live: 17212 = tree not ripe, 101 = bag empty; production
// never trusted these pre-checks).
// The tree is NOT gated by the 19h–22h battle window, so the loop calls it
// on its own cadence in every phase.
func (p *Player) rbtTreeCycle(actID int32) {
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

// ─── loop ────────────────────────────────────────────────────────────────────

// rebornTrialAutoLoop is the RebornTrial counterpart of skyTradeAutoLoop:
// the DB gate is re-read every wake (so /auto_luanhoi false stops the caravan
// between ticks), phases and the daily window are computed locally but the
// server's refuse codes always overrule our calendar.
func (p *Player) rebornTrialAutoLoop() {
	p.rebornTrialAutoLoopAfter(8 * time.Second)
}

func (p *Player) rebornTrialAutoLoopAfter(settle time.Duration) {
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
		log.Printf("[%s] [LHBC] auto: %s", p.Username, s)
	}
	var nextStatus time.Time
	statusNow := ""
	var nextTree time.Time // zero → first wake tends the tree immediately
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
		if !p.AutoRebornTrial {
			record("tắt — bật bằng /auto_luanhoi true")
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
		// Union tree runs all day, every phase — on its own cadence.
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
		// Live 2026-10-09 11:05 ICT (round 1 opened 10:00): ENTER and
		// AUTO_EXPLORE answer ret=0 in the MIDDLE of the day — the map is
		// playable ALL DAY inside a Battle round. The 19|22 param therefore
		// cannot gate exploration (the 15001s seen at 01:45 ICT were simply
		// the Prepare phase). We deliberately do NOT gate the loop on the
		// window: per-command refuse codes (15001/17221) already tell us
		// what the server accepts right now, and rbtHandleFrame logs them.
		statusNow = fmt.Sprintf("đang chạy xe thư thương vòng %d", round)
		record(statusNow)
		if !p.rbtPlayTick(actID, round) {
			return
		}
		// Bang chiến: the regular account NEVER fights (owner 2026-11-08
		// "đánh bang chiến chỉ đánh ở event thôi, theo lệnh của player trên
		// mini map") — only event accounts attack, and only on the
		// player's ⚔️ order from the map (rbtWarOrderOne on EP sessions).
		beat()
		// Owner rule 2026-10-09: rounds run 10:00–22:00 ICT — guarantee a
		// fresh tick at round end −4 min (21:56) even if the account is
		// idle/exhausted, so the last stamina is spent before close.
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
