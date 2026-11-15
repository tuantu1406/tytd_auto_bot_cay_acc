package tytd_go

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

/*
Auto Tiên Vực Thương Đồ (Sky Trade caravan) — the /auto_thiendo_tienvuc
feature, one loop per login, gated by the account's opt-in DB switch.

The game economy (all of it verified against the client at
../remoteSkyTrade): the player flies a cargo ship between 15 ports. Each
port lists goods at a live UNIT price; carried cargo books an AGGREGATE
cost. Trading is allowed whenever the ship is idle (targetPort == 0):
sell any cargo at the current port, buy up to min(stock, cash) at the
current port, then set sail. Travel takes RUNBUSINESS_MOVE_TIME seconds
(the client's own model: one constant per event, regardless of route),
and the arrival is claimed with 215009 — until then the map is locked.

So every port has exactly three options, and the loop picks the best:
  - SELL all cargo here, if its live value here beats what we paid;
  - BUY here and SAIL for a port craving THIS port's specialty (the
    specialGoodId the event config gives each port): the profit calc
    only starts once such a port is reported, and then the wallet fills
    with every good profitable at that destination, richest ratio first,
    if total (sellPrice − buyPrice) × qty is strictly positive;
  - otherwise WAIT: prices are re-read every minute, and the đạo hữu
    gets a heads-up every three minutes that nothing is profitable yet.
    The ship never sails a loss-making leg.

Every decision is recomputed from a fresh ENTER_MAP snapshot, so a
market refresh (server push) mid-plan simply changes the next plan —
the server's answers, never local optimism, move our books.

The event day is 10:00–22:00; at the close the whole rare/special board
reshuffles for the next 10:00 open. With the separate /auto_tangtoc_tienvuc
opt-in, from 21:58 normal trading is OVER: the leg still in flight gets
ONE burn of acceleration items to dock before the gate shuts, then the
ship makes its one and only last run — carrying its cargo or the port's
specialty to a random port, betting tomorrow's board makes it pay — and
the caravan sleeps until the next open (stLastRunPlan). No chaining of
fresh legs and speed-ups past the close: it burned the whole item bag
dock-to-dock while the gamble never fired.
*/

const (
	stModeBuy  int32 = 3 // the client's pack: type = (qty>>32)<<2 + 3 for buy...
	stModeSell int32 = 0 // ...and + 0 for sell (one endpoint for both)

	stWirePortBase = 10000 // wire port 10001..10015 ↔ displayed cảng 1..15

	skyTradeAlertGap   = 3 * time.Minute // "nothing profitable" reminder cadence
	skyTradePoll       = time.Minute     // the buy/sell/run-boat decision cycle
	skyTradeBusyWait   = 30 * time.Second
	skyTradeNoActWait  = 10 * time.Minute
	skyTradeReqTimeout = 15 * time.Second

	stMoveTimeFallback  = 14400 // client defaults until the event config says
	stSpeedTimeFallback = 300   // otherwise (RUNBUSINESS_* parsed on 21003)
)

// stDecisionWait is the decision cycle, injectable per player (tests drive
// it fast; 0 keeps the one-minute default).
func (p *Player) stDecisionWait() time.Duration {
	if p.stPoll > 0 {
		return p.stPoll
	}
	return skyTradePoll
}


// stGoodsLabel spells a good the way the game names it ("Cửu U Ngọc"),
// falling back to the bare id only for goods outside the language table.
func stGoodsLabel(id int32) string {
	if n := gimap.SkyGoodsName(id); n != "" {
		return n
	}
	return "hàng " + strconv.Itoa(int(id))
}

// stFmtDur spells a duration the way the đạo hữu reads it.
func stFmtDur(d time.Duration) string {
	if d < time.Minute {
		return "dưới 1 phút"
	}
	d = d.Round(time.Minute)
	h := int64(d / time.Hour)
	m := int64(d % time.Hour / time.Minute)
	switch {
	case h == 0:
		return fmt.Sprintf("%d phút", m)
	case m == 0:
		return fmt.Sprintf("%d giờ", h)
	default:
		return fmt.Sprintf("%d giờ %d phút", h, m)
	}
}

// ─── decision (pure) ─────────────────────────────────────────────────────────

type stAction int

const (
	stWait     stAction = iota // no leg is profitable right now
	stSellAt                   // sell all cargo at the current port
	stMoveSell                 // carry the cargo to destPort, it pays best there
	stBuyMove                  // buy here, then sail to destPort
	stLastRun                  // pre-close gamble: load for the night, sail to a RANDOM port
)

// stBuyItem is one line of the shopping list for a buy-and-sail leg.
type stBuyItem struct {
	goodsID int32
	qty     int64
}

// stPlan is one turn's verdict. profit is the expected net gain of the leg.
// stBuyMove rides on buys — the cargo load the planner computed for the
// destination port (one or more goods; the wallet is shared).
type stPlan struct {
	action   stAction
	buys     []stBuyItem
	destPort int32
	profit   int64
}

// stMarket finds one port's live listing (nil when the snapshot lacks it).
func stMarket(markets []*proto.SkyTradePortGoodsInfo, port int32) *proto.SkyTradePortGoodsInfo {
	for _, m := range markets {
		if m.GetPortId() == port {
			return m
		}
	}
	return nil
}

// stListing reads a good's unit price and stock from a port listing. The
// third result says the good is listed at all — an unlisted port cannot
// sell it back to us.
func stListing(market *proto.SkyTradePortGoodsInfo, goodsID int32) (unit, stock int64, listed bool) {
	if market == nil {
		return 0, 0, false
	}
	for _, g := range market.GetGoods() {
		if g.GetGoodsId() == goodsID {
			return g.GetPrice(), g.GetNum(), true
		}
	}
	return 0, 0, false
}

// stRareIsCargo says the port reported one of our held goods as its scarce
// good. rareGoodsId==0 (nobody reported yet) never matches: an unreported
// port is not a legal destination.
func stRareIsCargo(market *proto.SkyTradePortGoodsInfo, goodsIDs []int32) bool {
	rare := market.GetRareGoodsId()
	if rare == 0 {
		return false
	}
	for _, id := range goodsIDs {
		if id == rare {
			return true
		}
	}
	return false
}

// skyTradeDecide is the whole brain of the caravan, pure over one snapshot:
// current port, cash, cargo (num + AGGREGATE cost in price), every port's
// live unit prices and reported scarce good, and special — the current
// port's OWN specialty (its specialGoodId; 0 = not learned yet).
// Hard rules:
//   - strictly positive profit is the only reason to act;
//   - the ship only goes to (or sells at) a port whose reported rareGoodsId
//     is one of the goods in the hold — sailing for a plain price gap is
//     not enough, we trade the SCARCE good where it is scarce;
//   - with the produce table loaded, an empty hold WAITS for a port whose
//     reported rare is exactly THIS port's specialty; once one is seen the
//     wallet fills with every good profitable there, richest ratio first
//     (other goods ride along — the specialty opens the door).
//     Before the table lands, the rare-match rule alone applies.
//
// Ties prefer staying put.
func skyTradeDecide(currentPort int32, funds int64, cargo []*proto.SkyTradeGoods, markets []*proto.SkyTradePortGoodsInfo, special int32) stPlan {
	type held struct {
		goodsID int32
		num     int64
		cost    int64 // aggregate, as the server books it
	}
	var load []held
	var ids []int32
	for _, g := range cargo {
		if g.GetNum() > 0 && g.GetGoodsId() != 0 {
			load = append(load, held{g.GetGoodsId(), g.GetNum(), g.GetPrice()})
			ids = append(ids, g.GetGoodsId())
		}
	}

	if len(load) > 0 {
		// Selling here first — we are already docked, but ONLY while this
		// port still reports one of our goods as scarce.
		here := stMarket(markets, currentPort)
		hereRevenue, hereCost, sellableHere := int64(0), int64(0), true
		for _, h := range load {
			unit, _, listed := stListing(here, h.goodsID)
			if !listed {
				sellableHere = false
				break
			}
			hereRevenue += unit * h.num
			hereCost += h.cost
		}
		if sellableHere && stRareIsCargo(here, ids) && hereRevenue > hereCost {
			return stPlan{action: stSellAt, profit: hereRevenue - hereCost}
		}

		var best stPlan
		for _, m := range markets {
			if m.GetPortId() == currentPort || !stRareIsCargo(m, ids) {
				continue
			}
			revenue, cost, sellable := int64(0), int64(0), true
			for _, h := range load {
				unit, _, listed := stListing(m, h.goodsID)
				if !listed {
					sellable = false
					break
				}
				revenue += unit * h.num
				cost += h.cost
			}
			if !sellable {
				continue
			}
			if p := revenue - cost; p > best.profit {
				best = stPlan{action: stMoveSell, destPort: m.GetPortId(), profit: p}
			}
		}
		if best.profit > 0 {
			return best
		}
		return stPlan{action: stWait}
	}

	// Empty hold: the move is buy-here-sail-there. For each port that
	// REPORTS a scarce good we can buy profitably here, load the WHOLE
	// wallet the best way: goods ranked by profit-per-coin (margin ÷ unit
	// cost), filled to their stock caps — every đồng works on its most
	// profitable line first. The hold may mix goods; the destination only
	// has to crave one of them (the server-side rare rule).
	cur := stMarket(markets, currentPort)
	if cur == nil || funds <= 0 {
		return stPlan{action: stWait}
	}
	var best stPlan
	for _, m := range markets {
		if m.GetPortId() == currentPort || m.GetRareGoodsId() == 0 {
			continue
		}
		// The specialty anchor: with the produce table loaded, no profit
		// calc runs unless this port craves EXACTLY our port's specialty.
		if special != 0 && m.GetRareGoodsId() != special {
			continue
		}
		type cand struct {
			id    int32
			buy   int64
			sell  int64
			stock int64
			ratio float64
		}
		var cands []cand
		hasRare := false
		for _, g := range cur.GetGoods() {
			unit, stock, _ := stListing(cur, g.GetGoodsId())
			if unit <= 0 || stock <= 0 {
				continue
			}
			sellUnit, _, listed := stListing(m, g.GetGoodsId())
			if !listed || sellUnit <= unit {
				continue
			}
			if g.GetGoodsId() == m.GetRareGoodsId() {
				hasRare = true
			}
			cands = append(cands, cand{id: g.GetGoodsId(), buy: unit, sell: sellUnit, stock: stock,
				ratio: float64(sellUnit-unit) / float64(unit)})
		}
		if !hasRare || len(cands) == 0 {
			continue // the far port must crave something we can profit on
		}
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].ratio != cands[j].ratio {
				return cands[i].ratio > cands[j].ratio
			}
			return cands[i].id < cands[j].id // stable tie-break
		})
		left := funds
		var load []stBuyItem
		var profit int64
		for _, c := range cands {
			qty := int64(c.stock)
			if afford := left / int64(c.buy); afford < qty {
				qty = afford
			}
			if qty <= 0 {
				continue
			}
			if qty > math.MaxInt32 { // the wire packs 32-bit chunks; stay inside
				qty = math.MaxInt32
			}
			load = append(load, stBuyItem{c.id, qty})
			left -= qty * int64(c.buy)
			profit += qty * int64(c.sell-c.buy)
		}
		if profit > best.profit {
			best = stPlan{action: stBuyMove, buys: load, destPort: m.GetPortId(), profit: profit}
		}
	}
	if best.profit > 0 {
		return best
	}
	return stPlan{action: stWait}
}

// stPlanDesc phrases one verdict for the log.
func stPlanDesc(plan stPlan) string {
	switch plan.action {
	case stSellAt:
		return fmt.Sprintf("bán hết hàng tại cảng (lời %d)", plan.profit)
	case stMoveSell:
		return fmt.Sprintf("chở hàng sang cảng %d bán (lời %d)", plan.destPort-stWirePortBase, plan.profit)
	case stBuyMove:
		if len(plan.buys) == 1 {
			return fmt.Sprintf("mua %d × %s rồi chạy sang cảng %d (lời %d)",
				plan.buys[0].qty, stGoodsLabel(plan.buys[0].goodsID), plan.destPort-stWirePortBase, plan.profit)
		}
		return fmt.Sprintf("mua %d món (%s làm chủ lực) rồi chạy sang cảng %d (lời %d)",
			len(plan.buys), stGoodsLabel(plan.buys[0].goodsID), plan.destPort-stWirePortBase, plan.profit)
	case stLastRun:
		if len(plan.buys) == 0 {
			return fmt.Sprintf("chuyến cuối: chở hàng sang cảng %d (ngẫu nhiên)", plan.destPort-stWirePortBase)
		}
		if len(plan.buys) == 1 {
			return fmt.Sprintf("chuyến cuối: mua đặc sản %s rồi chạy sang cảng %d (ngẫu nhiên)",
				stGoodsLabel(plan.buys[0].goodsID), plan.destPort-stWirePortBase)
		}
		return fmt.Sprintf("chuyến cuối: mua %d món hàng rẻ nhất rồi chạy sang cảng %d (ngẫu nhiên)",
			len(plan.buys), plan.destPort-stWirePortBase)
	default:
		return "chờ cảng báo hàng hiếm khớp món hàng với giá có lời"
	}
}

// skyTradeArrivalAt is the client's own arrival model: departure ms +
// event-wide travel seconds − acceleration already bought. Local only —
// 215009's answer is the proof of arrival, the clock is just the alarm.
func skyTradeArrivalAt(startTimeMs int64, speeds int32, moveNeed, speedUp int64) int64 {
	return startTimeMs/1000 + moveNeed - int64(speeds)*speedUp
}

// ─── wire helpers over the stRoundTrip seats ────────────────────────────────

// skyTradeDealReq packs one 215005 request. The client's 64-bit quantity is
// split across the two int32 fields — num carries the low 32 bits, type the
// high 32 bits shifted left two then plus the buy(3)/sell(0) mode. Ordinary
// quantities keep the high half (and thus most of type) zero.
func skyTradeDealReq(actID, port, goodsID int32, qty int64, mode int32) *proto.SkyTradeDealReq {
	return &proto.SkyTradeDealReq{
		ActivityId: pb.Int32(actID),
		Port:       pb.Int32(port),
		GoodsId:    pb.Int32(goodsID),
		Num:        pb.Int32(int32(qty & 0xFFFFFFFF)),
		Type:       pb.Int32(int32(uint64(qty)>>32<<2 + uint64(mode))),
	}
}

func (p *Player) stDeal(actID, port, goodsID int32, qty int64, mode int32) (*proto.SkyTradeDealResp, error) {
	body, err := pb.Marshal(skyTradeDealReq(actID, port, goodsID, qty, mode))
	if err != nil {
		return nil, err
	}
	data, err := p.stRoundTrip(SKY_TRADE_BUY_CMD, SERVER_SKY_TRADE_BUY, body, skyTradeReqTimeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.SkyTradeDealResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) stSail(actID, destPort int32) bool {
	body, err := pb.Marshal(&proto.SkyTradeGotoPortReq{ActivityId: pb.Int32(actID), Port: pb.Int32(destPort)})
	if err != nil {
		return false
	}
	data, err := p.stRoundTrip(SKY_TRADE_MOVE_CMD, SERVER_SKY_TRADE_MOVE, body, skyTradeReqTimeout)
	if err != nil {
		log.Printf("[%s] [SkyTrade] auto: move failed: %v", p.Username, err)
		return false
	}
	var rsp proto.SkyTradeGotoPortResp
	if err := pb.Unmarshal(data, &rsp); err != nil || rsp.GetRet() != 0 {
		return false // refused code already logged by stHandleFrame
	}
	log.Printf("[%s] [SkyTrade] auto: thuyền rời cảng → cảng %d", p.Username, destPort-stWirePortBase)
	return true
}

func (p *Player) stClaimArrival(actID int32) {
	body, err := skyTradeActivityBody(actID)
	if err != nil {
		return
	}
	data, err := p.stRoundTrip(SKY_TRADE_MOVE_FINISH_CMD, SERVER_SKY_TRADE_MOVE_FINISH, body, skyTradeReqTimeout)
	if err != nil {
		log.Printf("[%s] [SkyTrade] auto: arrival claim failed: %v", p.Username, err)
		return
	}
	var rsp proto.SkyTradeGetRewardResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return
	}
	// stHandleFrame already logged a refused code; only the happy path
	// earns a line here (which port we now call home until the next sail).
	if rsp.GetRet() == 0 {
		log.Printf("[%s] [SkyTrade] auto: cập cảng %d (+%d điểm)", p.Username, rsp.GetEvent().GetTargetPort(), rsp.GetEvent().GetAddFame())
		p.sendNotify(fmt.Sprintf("⛵ Thương đồ Tiên Vực: thuyền đã cập cảng %d (+%d điểm uy tín)",
			rsp.GetEvent().GetTargetPort()-stWirePortBase, rsp.GetEvent().GetAddFame()))
	}
}

// ─── end-of-day acceleration sprint ─────────────────────────────────────────

// stSprintArmed is the end-of-day clock. The event day runs 10:00–22:00
// (VN wall time, ictZone — never the server's own TZ, which may be UTC);
// from 21:58 normal trading stops: the loop grants one speed-up to finish
// the leg in flight, then only the random last run may fire
// (stLastRunPlan), and the caravan idles until the close. Tests override
// the whole clock via stSprintTest.
func (p *Player) stSprintArmed(now time.Time) bool {
	if p.stSprintTest != nil {
		return p.stSprintTest(now)
	}
	vn := now.In(ictZone)
	return vn.Hour() == 21 && vn.Minute() >= 58
}

// stLastRunPlan is the pre-close gamble, consulted once per event day when
// the sprint window is armed and the normal planner has nothing profitable.
// At 22:00 the event closes and at tomorrow's 10:00 reopen every port's
// specialty and every reported rare change — so an idle, loaded berth is
// a coin flip anyway, while an idle EMPTY berth at the same port is worth
// nothing. The ship therefore sails to a RANDOM port (never the current
// one): with cargo aboard it simply carries it through the night, with an
// empty hold and cash it buys THIS port's SPECIAL good — its own produce,
// always at home price here — no profit calc needed (tomorrow's prices are
// unknowable anyway). Only while the specialty is still unknown does it
// fall back to the cheapest listings. The sprint's speed-ups then burn
// whatever items it takes to dock before the close.
// Returns stWait when there is nothing to bet: no cargo, no cash, or the
// specialty (or any fallback line) is not buyable here.
func stLastRunPlan(currentPort int32, funds int64, cargo []*proto.SkyTradeGoods, cur *proto.SkyTradePortGoodsInfo, special int32, rnd func(int) int) stPlan {
	if currentPort <= stWirePortBase || currentPort > stWirePortBase+15 {
		return stPlan{action: stWait}
	}
	// Uniform over the other 14 wire ports: pick 1..14, step over current.
	port := int32(rnd(14) + 1)
	if vis := currentPort - stWirePortBase; port >= vis {
		port++
	}
	dest := port + stWirePortBase

	for _, g := range cargo {
		if g.GetNum() > 0 {
			return stPlan{action: stLastRun, destPort: dest} // carry it through the night
		}
	}
	if funds <= 0 || cur == nil {
		return stPlan{action: stWait}
	}
	// The bet's load: the port's specialty, the one good it is always
	// stocked in. One line, all the cash, no margin math — this is a
	// position on tomorrow's reshuffled board, not today's arb.
	if special != 0 {
		unit, stock, listed := stListing(cur, special)
		if !listed || unit <= 0 || stock <= 0 {
			return stPlan{action: stWait}
		}
		qty := stock
		if q := funds / unit; q < qty {
			qty = q
		}
		if qty > math.MaxInt32 {
			qty = math.MaxInt32
		}
		if qty <= 0 {
			return stPlan{action: stWait}
		}
		return stPlan{action: stLastRun, buys: []stBuyItem{{special, qty}}, destPort: dest}
	}
	// Specialty still unknown (table never landed): cheapest listings first.
	type line struct {
		id, unit, stock int64
	}
	var lines []line
	for _, g := range cur.GetGoods() {
		unit, stock, _ := stListing(cur, g.GetGoodsId())
		if unit <= 0 || stock <= 0 {
			continue
		}
		lines = append(lines, line{int64(g.GetGoodsId()), unit, stock})
	}
	if len(lines) == 0 {
		return stPlan{action: stWait}
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].unit != lines[j].unit {
			return lines[i].unit < lines[j].unit // cheapest (surplus) first
		}
		return lines[i].id < lines[j].id
	})
	cash := funds
	var buys []stBuyItem
	for _, l := range lines {
		if cash < l.unit {
			break // sorted ascending: no later line is affordable either
		}
		qty := l.stock
		if q := cash / l.unit; q < qty {
			qty = q
		}
		if qty > math.MaxInt32 {
			qty = math.MaxInt32
		}
		buys = append(buys, stBuyItem{int32(l.id), qty})
		cash -= qty * l.unit
	}
	if len(buys) == 0 {
		return stPlan{action: stWait}
	}
	return stPlan{action: stLastRun, buys: buys, destPort: dest}
}

// stSpeedUp burns just enough acceleration items to dock right now — the
// client's own count, ceil(remaining ÷ seconds-per-item); the server clamps
// against the bag. True means the answer arrived and the loop should
// re-snapshot: either the ship docked (the answer carries the arrival
// event — no 215009 claim needed, the client treats it the same way) or it
// carries the fresh, shorter ETA. Refused codes are logged by
// stHandleFrame; false leaves the loop on its normal pacing.
func (p *Player) stSpeedUp(actID int32, eta time.Duration, speedUp int64, destPort int32) bool {
	if speedUp <= 0 {
		return false
	}
	sec := int64(eta / time.Second)
	if sec < 1 {
		sec = 1
	}
	num := int32((sec + speedUp - 1) / speedUp)
	body, err := pb.Marshal(&proto.SkyTradeAddSpeedReq{ActivityId: pb.Int32(actID), Num: pb.Int32(num)})
	if err != nil {
		return false
	}
	data, err := p.stRoundTrip(SKY_TRADE_SPEED_UP_CMD, SERVER_SKY_TRADE_SPEED_UP, body, skyTradeReqTimeout)
	if err != nil {
		log.Printf("[%s] [SkyTrade] auto: speed-up failed: %v", p.Username, err)
		return false
	}
	var rsp proto.SkyTradeAddSpeedResp
	if err := pb.Unmarshal(data, &rsp); err != nil || rsp.GetRet() != 0 {
		return false
	}
	if ev := rsp.GetEvent(); ev.GetEventType() != 0 {
		log.Printf("[%s] [SkyTrade] auto: tăng tốc cập cảng %d (+%d điểm)",
			p.Username, ev.GetTargetPort()-stWirePortBase, ev.GetAddFame())
		p.sendNotify(fmt.Sprintf("⛵ Thương đồ Tiên Vực: tăng tốc cập cảng %d (+%d điểm uy tín)",
			ev.GetTargetPort()-stWirePortBase, ev.GetAddFame()))
		return true
	}
	log.Printf("[%s] [SkyTrade] auto: đã dùng %d giấy tăng tốc cho chuyến về cảng %d",
		p.Username, num, destPort-stWirePortBase)
	p.sendNotify(fmt.Sprintf("⛵ Thương đồ Tiên Vực: dùng %d giấy tăng tốc để về cảng %d trước khi sự kiện đóng",
		num, destPort-stWirePortBase))
	return true
}

// ─── loop ────────────────────────────────────────────────────────────────────

// skyTradeAutoLoop drives one account's caravan. The opt-in gate is the
// Player field (seeded from the session's own row at login, patched live by
// /auto_thiendo_tienvuc and the settings page) — never a DB query: several
// rows can share one game_uid when different people configure the same game
// account, and only the session knows whose toggle governs it
// (owner 2026-10-27). False stops the ship between legs, not mid-deal.
func (p *Player) skyTradeAutoLoop() {
	p.skyTradeAutoLoopAfter(skyTradeSettle + 5*time.Second)
}

// skyTradeAutoLoopAfter is the loop with the settle delay injectable (tests
// drive the ship immediately; production lets the login pushes settle).
func (p *Player) skyTradeAutoLoopAfter(settle time.Duration) {
	if !p.stSleepCtx(settle) {
		return
	}
	var nextAlert time.Time
	// One state line in the log per CHANGE of situation (tắt / chưa có sự
	// kiện / đóng cửa sổ / cảng+quyết định / đang chạy) — enough to tell
	// "not enabled" from "waiting for prices" from "sailing", without a
	// line every poll. The old 5-minute status heartbeat is GONE (owner
	// 2026-10-27) — the loop only speaks for real events now, and those
	// ride the account's Notify switch like everywhere else (no
	// forceNotify — owner same day).
	lastState := ""
	record := func(s string) {
		if s == lastState {
			return
		}
		lastState = s
		log.Printf("[%s] [SkyTrade] auto: %s", p.Username, s)
	}
	for {
		if !p.AutoSkyTrade {
			record("tắt — bật bằng /auto_thiendo_tienvuc true")
			nextAlert = time.Time{}
			if !p.stSleepCtx(time.Minute) {
				return
			}
			continue
		}
		actID, ok := p.stActiveActivity()
		if !ok {
			nextAlert = time.Time{}
			// The event is announced in the activity list before it opens,
			// so instead of blind 10-minute polls the loop sets an alarm for
			// the exact begin time (the 10:00 daily open) plus a short settle
			// for the server's own window flip. Long waits are still capped
			// at 10 minutes so a schedule revision is noticed.
			if begin, found := p.stNextOpening(time.Now()); found {
				record(fmt.Sprintf("Tiên Vực Thương Đồ mở lúc %s — bot sẽ tự chạy khi tới giờ",
					time.UnixMilli(begin).In(ictZone).Format("15:04")))
				d := time.Until(time.UnixMilli(begin)) + p.stOpenSettleWait()
				if d > skyTradeNoActWait {
					d = skyTradeNoActWait
				}
				if d < time.Second {
					d = time.Second
				}
				if !p.stSleepCtx(d) {
					return
				}
				continue
			}
			record("chưa thấy Tiên Vực Thương Đồ đang mở trong danh sách sự kiện")
			// Backfill: the row can be absent because the SERVER never
			// pushed its 21036 add to this session (the main list cannot be
			// pulled). The id is remembered from any past appearance, so one
			// 21003 asks the server directly — its mainConfig answer
			// recreates the row (activityStore.mergeDetail).
			if id, known := p.ActList.lastIDForType(skyTradeActivityType); known {
				p.activityPull(id)
			}
			if !p.stSleepCtx(skyTradeNoActWait) {
				return
			}
			continue
		}

		snap, err := p.stSnapshot(actID, skyTradeReqTimeout)
		if err != nil {
			log.Printf("[%s] [SkyTrade] auto: snapshot failed: %v", p.Username, err)
			if !p.stSleepCtx(skyTradeBusyWait) {
				return
			}
			continue
		}
		if snap.GetRet() != 0 {
			// Outside the daily trading window (the server's call) — the
			// refuse code is logged by stHandleFrame; announce the state.
			record(fmt.Sprintf("server chưa mở cửa sổ mua bán (code %d)", snap.GetRet()))
			if !p.stSleepCtx(skyTradeNoActWait) {
				return
			}
			continue
		}
		pd := snap.GetPlayerData()
		ship := pd.GetAirshipInfo()
		if ship == nil {
			if !p.stSleepCtx(skyTradeBusyWait) {
				return
			}
			continue
		}

		// An arrival report earns its free claim — but it must NEVER gate
		// the map (owner 2026-10-27 "luôn claim báo cáo cập cảng để có
		// thể chạy tiếp"). A failed/slow claim that left eventType stuck
		// parked the ship at dock — trading logic never ran, no sleep, no
		// notify ("báo cáo port mà không thấy chạy", <live-user> 2026-10-27:
		// event=1 for hours while port 7 craved exactly the held good).
		// So: claim, then trade on THIS snapshot. The ship data here is
		// already post-position (docked at the arrival port); if the
		// server truly refuses MOVE before the claim is booked, stSail is
		// refused → busy-backoff → retry next wake. No deadlock, no hot
		// loop.
		if snap.GetEvent().GetEventType() != 0 {
			p.stClaimArrival(actID)
		}

		// One ICT calendar date per loop: the sprint window, the speed-up
		// allowance and the last-run flags all key off it.
		dayVN := time.Now().In(ictZone).Format("2006-01-02")
		// eod = the closing minutes (opt-in): normal trading is OVER.
		// From 21:58 the only allowed moves are ONE speed-up to finish the
		// leg already in flight, then the once-per-day random last run —
		// never a fresh normal leg (that chain burned speed-up after
		// speed-up: dock → profitable plan → sail → speed-up again).
		eod := p.stSprintArmed(time.Now()) &&
			p.AutoSkyTradeSpeed

		if ship.GetTargetPort() != 0 {
			record(fmt.Sprintf("thuyền đang chạy → cảng %d", ship.GetTargetPort()-stWirePortBase))
			need, up := p.stTravelTimes()
			eta := time.Until(time.Unix(skyTradeArrivalAt(ship.GetStartTime(), ship.GetSpeeds(), need, up), 0))
			// End-of-day sprint (opt-in): the window's ONE speed-up — enough
			// items to dock this leg right now. Once it's burned the day's
			// quota is spent (stSprintBurnDay): a docked ship must not start
			// another normal leg just to speed that one up too.
			// The wait below keeps its normal
			// pace when the server refuses (bag empty / limit), so a denied
			// speed-up never hot-loops.
			if eta > 0 && eod && dayVN != p.stSprintBurnDay &&
				p.stSpeedUp(actID, eta, up, ship.GetTargetPort()) {
				p.stSprintBurnDay = dayVN
				continue
			}
			d := eta + 15*time.Second
			if poll := p.stDecisionWait(); d > poll {
				d = poll // the snapshot's event field catches early arrivals
			}
			if d < time.Second {
				d = time.Second
			}
			if !p.stSleepCtx(d) {
				return
			}
			continue
		}

		// End-of-day mode: no fresh normal legs, no repeat gamble — the ship
		// leaves ONCE for a random port (carrying its cargo or the port's
		// specialty) and then idles until the close. A profitable plan the
		// normal brain would chase here is exactly what used to chain
		// speed-ups past the window; tomorrow's board reshuffles anyway.
		if eod {
			after := p.stDecisionWait()
			if p.stLastRunDay != dayVN {
				lp := stLastRunPlan(ship.GetSourcePort(), pd.GetAvailableFunds(), pd.GetGoods(),
					stMarket(snap.GetPortGoods(), ship.GetSourcePort()),
					p.stSpecialGood(ship.GetSourcePort()), rand.Intn)
				if lp.action == stLastRun {
					p.stLastRunDay = dayVN
					load := "chở hàng đang có"
					if len(lp.buys) == 1 {
						load = fmt.Sprintf("mua đặc sản %s của cảng", stGoodsLabel(lp.buys[0].goodsID))
					} else if len(lp.buys) > 1 {
						load = fmt.Sprintf("mua %d món hàng rẻ nhất cảng", len(lp.buys))
					}
					p.sendNotify(fmt.Sprintf("🌙 Thương đồ Tiên Vực: chuyến cuối trước giờ đóng cửa — %s rồi dong buồm tới cảng %d (chọn ngẫu nhiên). 10:00 mai hàng hiếm và giá đổi hết, biết đâu trúng lớn; bot dừng tự chạy tới khi sự kiện mở lại.",
						load, lp.destPort-stWirePortBase))
					if p.stBuyAndGo(actID, ship.GetSourcePort(), lp) {
						after = 0 // at sea now: re-look immediately, then idle to close
					} else {
						after = skyTradeBusyWait
					}
					record(fmt.Sprintf("chuyến cuối → cảng %d (chờ đóng cửa)", lp.destPort-stWirePortBase))
				} else {
					// Nothing to bet on — retire at the dock and say so once.
					p.stLastRunDay = dayVN
					p.sendNotify("🌙 Thương đồ Tiên Vực: trước giờ đóng cửa không còn hàng để cược chuyến cuối — bot neo cảng, dừng tự chạy tới khi sự kiện mở lại lúc 10:00 mai.")
					record(fmt.Sprintf("đã chốt ở cảng %d, chờ đóng cửa", ship.GetSourcePort()-stWirePortBase))
				}
			} else {
				record(fmt.Sprintf("đã chốt chuyến cuối ở cảng %d, chờ đóng cửa", ship.GetSourcePort()-stWirePortBase))
			}
			if !p.stSleepCtx(after) {
				return
			}
			continue
		}

		plan := skyTradeDecide(ship.GetSourcePort(), pd.GetAvailableFunds(), pd.GetGoods(), snap.GetPortGoods(), p.stSpecialGood(ship.GetSourcePort()))
		record(fmt.Sprintf("cảng %d, %d xu, %d món hàng → %s",
			ship.GetSourcePort()-stWirePortBase, pd.GetAvailableFunds(), len(pd.GetGoods()), stPlanDesc(plan)))
		// The one-minute poll paces PRICE WATCHING (stWait); a turn that
		// actually moved something re-looks immediately — after selling,
		// the buy + sail happens in the same breath, not next minute. A
		// refused turn backs off so the server never faces a request loop.
		after := p.stDecisionWait()
		switch plan.action {
		case stSellAt:
			if p.stSellAll(actID, ship.GetSourcePort(), pd.GetGoods()) {
				after = 0 // hold just became cash: re-plan NOW (buy + sail)
			} else {
				after = skyTradeBusyWait
			}
		case stBuyMove:
			if p.stBuyAndGo(actID, ship.GetSourcePort(), plan) {
				p.sendNotify(fmt.Sprintf("⛵ Thương đồ Tiên Vực: xuất phát cảng %d → cảng %d, lợi dự kiến %d",
					ship.GetSourcePort()-stWirePortBase, plan.destPort-stWirePortBase, plan.profit))
			} else {
				after = skyTradeBusyWait
			}
		case stMoveSell:
			if p.stSail(actID, plan.destPort) {
				p.sendNotify(fmt.Sprintf("⛵ Thương đồ Tiên Vực: chở hàng sang cảng %d bán, lợi dự kiến %d",
					plan.destPort-stWirePortBase, plan.profit))
			} else {
				after = skyTradeBusyWait
			}
		default: // stWait — the alert cadence is the whole point here.
			// (The closing-minutes gamble lives in the end-of-day block
			// above; normal trading never reaches 21:58 still cruising.)
			if time.Now().After(nextAlert) {
				p.sendNotify(fmt.Sprintf("⛵ Thương đồ Tiên Vực: đang chờ ở cảng %d — chưa có cảng nào báo hàng hiếm khớp món hàng với giá lời; bot sẽ tự chạy thuyền khi thấy chuyến sinh lời.",
					ship.GetSourcePort()-stWirePortBase))
				nextAlert = time.Now().Add(skyTradeAlertGap)
			}
		}
		if !p.stSleepCtx(after) {
			return
		}
	}
}

// stSellAll empties the hold at the current port using the SNAPSHOT's
// quantities — each good is sold exactly once; any refusal (price moved,
// server had other plans) aborts and the next snapshot replans. False means
// the turn did not complete, so the loop backs off before re-looking.
func (p *Player) stSellAll(actID, port int32, cargo []*proto.SkyTradeGoods) bool {
	for _, g := range cargo {
		if g.GetNum() <= 0 {
			continue
		}
		rsp, err := p.stDeal(actID, port, g.GetGoodsId(), g.GetNum(), stModeSell)
		if err != nil {
			log.Printf("[%s] [SkyTrade] auto: sell failed: %v", p.Username, err)
			return false
		}
		if rsp.GetRet() != 0 {
			return false // stHandleFrame logged the code; replan on next snapshot
		}
		log.Printf("[%s] [SkyTrade] auto: đã bán %d × món %d tại cảng %d (còn %d xu)",
			p.Username, g.GetNum(), g.GetGoodsId(), port-stWirePortBase, rsp.GetAvailableFunds())
		p.sendNotify(fmt.Sprintf("⛵ Thương đồ Tiên Vực: đã bán %d × %s ở cảng %d (còn %d xu)",
			g.GetNum(), stGoodsLabel(g.GetGoodsId()), port-stWirePortBase, rsp.GetAvailableFunds()))
	}
	return true
}

// stBuyAndGo works the shopping list top-down (best profit-per-coin first)
// and sails once the wallet is spent. A refusal most likely means the
// cargo-hold value cap — halve and retry that line, like the UI does when
// the confirm button clamps the quantity; the remaining cash then flows to
// the next lines on the list.
func (p *Player) stBuyAndGo(actID, port int32, plan stPlan) bool {
	for _, item := range plan.buys {
		qty := item.qty
		done := false
		for attempts := 0; attempts < 4 && qty > 0; attempts++ {
			rsp, err := p.stDeal(actID, port, item.goodsID, qty, stModeBuy)
			if err != nil {
				log.Printf("[%s] [SkyTrade] auto: buy failed: %v", p.Username, err)
				return false
			}
			if rsp.GetRet() != 0 {
				log.Printf("[%s] [SkyTrade] auto: mua %d × món %d bị từ chối (code %d), thử ít hơn",
					p.Username, qty, item.goodsID, rsp.GetRet())
				qty /= 2
				continue
			}
			log.Printf("[%s] [SkyTrade] auto: đã mua %d × món %d tại cảng %d (còn %d xu)",
				p.Username, qty, item.goodsID, port-stWirePortBase, rsp.GetAvailableFunds())
			p.sendNotify(fmt.Sprintf("⛵ Thương đồ Tiên Vực: đã mua %d × %s ở cảng %d (còn %d xu)",
				qty, stGoodsLabel(item.goodsID), port-stWirePortBase, rsp.GetAvailableFunds()))
			done = true
			break
		}
		if !done {
			return false // this line never fit — replan from a fresh snapshot
		}
	}
	log.Printf("[%s] [SkyTrade] auto: chất hàng xong, nhổ neo", p.Username)
	return p.stSail(actID, plan.destPort)
}

// stActiveActivity is the live type-150 instance id (active window only —
// show/reward phases must not send trade traffic).
func (p *Player) stActiveActivity() (int32, bool) {
	now := time.Now()
	for _, e := range p.ActList.byType(skyTradeActivityType) {
		if e.status(now) == "active" {
			return e.ActivityID, true
		}
	}
	return 0, false
}

// stNextOpening returns the soonest future BeginTime among the announced
// type-150 instances — the daily 10:00 open appears in the activity list
// before it starts (show/none status), so the loop can sleep precisely to
// it instead of polling blind. False when no upcoming window is known yet.
func (p *Player) stNextOpening(now time.Time) (int64, bool) {
	var best int64
	ms := now.UnixMilli()
	for _, e := range p.ActList.byType(skyTradeActivityType) {
		if e.BeginTime <= ms {
			continue
		}
		if best == 0 || e.BeginTime < best {
			best = e.BeginTime
		}
	}
	return best, best != 0
}

// stOpenSettleWait pads the open-time alarm so the server's own trading
// window has flipped when the first ENTER lands (tests shrink it).
func (p *Player) stOpenSettleWait() time.Duration {
	if p.stOpenSettle > 0 {
		return p.stOpenSettle
	}
	return 15 * time.Second
}

func (p *Player) stSleepCtx(d time.Duration) bool {
	select {
	case <-p.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// ─── event-delivered travel timing ───────────────────────────────────────────

// stMergeConfig folds RUNBUSINESS_MOVE_TIME and RUNBUSINESS_ACCELATE_ITEM_
// EFFECT (seconds) out of the hourly 21003 detail answer for the type-150
// instance — the same answer ActList already folds for the window fields.
// Until the first answer arrives, the client's own fallbacks stand in; the
// arrival is always proven by 215009's answer, so a wrong estimate only
// shifts when we ASK, never what we know.
func (p *Player) stMergeConfig(data []byte) {
	var rsp proto.RspGetActivityDetail
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return
	}
	dc := rsp.GetActivity().GetDetailConfig()
	cc := dc.GetCommonConfig()
	if cc.GetMainConfig().GetType() != skyTradeActivityType {
		return
	}
	var need, up int64
	for _, pc := range cc.GetParamConfig() {
		v, err := strconv.ParseInt(pc.GetValue(), 10, 64)
		if err != nil || v <= 0 {
			continue
		}
		switch pc.GetParam() {
		case "RUNBUSINESS_MOVE_TIME":
			need = v
		case "RUNBUSINESS_ACCELATE_ITEM_EFFECT":
			up = v
		}
	}
	// The static tables ride in uniqueConfig: portConfig names each port's
	// SPECIAL good (its own produce, specialGoodId). An empty-hold run
	// anchors on it: the profit calc only starts once a port reports our
	// current port's specialty as its scarce good.
	var special map[int32]int32
	if uc := dc.GetUniqueConfig(); len(uc) > 0 {
		var sync proto.SkyTradeConfigSync
		if err := pb.Unmarshal(uc, &sync); err == nil {
			for _, pc := range sync.GetPortConfig() {
				if pc.GetPortId() != 0 && pc.GetSpecialGoodId() != 0 {
					if special == nil {
						special = map[int32]int32{}
					}
					special[pc.GetPortId()] = pc.GetSpecialGoodId()
				}
			}
		} else {
			log.Printf("[%s] [SkyTrade] uniqueConfig decode: %v", p.Username, err)
		}
	}
	if need == 0 && up == 0 && special == nil {
		return
	}
	p.StMu.Lock()
	if need != 0 {
		p.stMoveNeedSec = need
	}
	if up != 0 {
		p.stSpeedUpSec = up
	}
	if special != nil {
		p.stSpecials = special
	}
	p.StMu.Unlock()
}

// stSpecialGood is the current port's specialty (0 = unknown until the
// detail config has landed).
func (p *Player) stSpecialGood(port int32) int32 {
	p.StMu.Lock()
	defer p.StMu.Unlock()
	return p.stSpecials[port]
}

// stTravelTimes returns the seconds-per-leg and seconds-per-accelerator in
// force, with the client's fallback constants before the config lands.
func (p *Player) stTravelTimes() (moveNeed, speedUp int64) {
	p.StMu.Lock()
	moveNeed, speedUp = p.stMoveNeedSec, p.stSpeedUpSec
	p.StMu.Unlock()
	if moveNeed == 0 {
		moveNeed = stMoveTimeFallback
	}
	if speedUp == 0 {
		speedUp = stSpeedTimeFallback
	}
	return moveNeed, speedUp
}
