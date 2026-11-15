package tytd_go

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The auto caravan (Tiên Vực Thương Đồ): planner, wire packing, event
// config folding, and the loop end-to-end against injected answers.

// stGood builds a MARKET row (unit price + stock); cargo rows on the ship
// book an AGGREGATE cost in price, hence the second constructor.
func stGood(id int32, unit, stock int64) *proto.SkyTradeGoods {
	return &proto.SkyTradeGoods{GoodsId: pb.Int32(id), Price: pb.Int64(unit), Num: pb.Int64(stock)}
}

func stCargo(id int32, num, aggregateCost int64) *proto.SkyTradeGoods {
	return &proto.SkyTradeGoods{GoodsId: pb.Int32(id), Num: pb.Int64(num), Price: pb.Int64(aggregateCost)}
}

func stPort(port int32, goods ...*proto.SkyTradeGoods) *proto.SkyTradePortGoodsInfo {
	return &proto.SkyTradePortGoodsInfo{PortId: pb.Int32(port), Goods: goods}
}

// stPortR is a port whose scarce good has been REPORTED (rareGoodsId set) —
// the only kind of port the caravan may sail to.
func stPortR(port, rare int32, goods ...*proto.SkyTradeGoods) *proto.SkyTradePortGoodsInfo {
	return &proto.SkyTradePortGoodsInfo{PortId: pb.Int32(port), RareGoodsId: pb.Int32(rare), Goods: goods}
}

// stSea completes the market picture: all 15 ports reporting their scarce
// good (fillers pick id 999 — no cargo can ever match it), with the given
// ports replacing their filler wholesale. A realistic full-map fixture —
// NOT a planner precondition: one craving port is enough to act.
func stSea(ports ...*proto.SkyTradePortGoodsInfo) []*proto.SkyTradePortGoodsInfo {
	repl := map[int32]*proto.SkyTradePortGoodsInfo{}
	for _, p := range ports {
		repl[p.GetPortId()] = p
	}
	out := make([]*proto.SkyTradePortGoodsInfo, 0, 15)
	for n := int32(1); n <= 15; n++ {
		port := stWirePortBase + n
		if p, ok := repl[port]; ok {
			out = append(out, p)
			continue
		}
		out = append(out, stPortR(port, 999))
	}
	return out
}

func TestSkyTradeDecide(t *testing.T) {
	// 10001 reports a good nobody can carry (999): the map is complete but
	// this port craves nothing we deal in.
	m1Low := stPortR(10001, 999, stGood(1, 100, 50))
	m2High := stPortR(10002, 1, stGood(1, 180, 100)) // cảng 2 báo hàng 1 hiếm
	m2Mid := stPortR(10002, 1, stGood(1, 150, 100))

	cases := []struct {
		name    string
		port    int32
		funds   int64
		cargo   []*proto.SkyTradeGoods
		markets []*proto.SkyTradePortGoodsInfo
		special int32 // this port's specialty (0 = table not learned)
		want    stPlan
	}{
		{
			name:    "empty hold buys the best margin toward a scarce port",
			port:    10001,
			funds:   1000,
			markets: stSea(m1Low, m2High),
			want:    stPlan{action: stBuyMove, buys: []stBuyItem{{1, 10}}, destPort: 10002, profit: 800},
		},
		{
			name:  "wallet fills the most profitable lines, mixing goods for one scarce destination",
			port:  10001,
			funds: 5000,
			markets: stSea(
				stPortR(10001, 999, stGood(1, 100, 10), stGood(2, 200, 10)),
				stPortR(10002, 1, stGood(1, 150, 99), stGood(2, 260, 99)), // rare 1; both goods profitable
			),
			want: stPlan{action: stBuyMove, buys: []stBuyItem{{1, 10}, {2, 10}}, destPort: 10002, profit: 1100},
		},
		{
			name:  "scarce good itself unaffordable-but-profitable rides along even when cash only fits it",
			port:  10001,
			funds: 2000,
			markets: stSea(
				stPortR(10001, 999, stGood(2, 200, 10)),
				stPortR(10002, 2, stGood(2, 260, 99)), // only the rare good, profitable
			),
			want: stPlan{action: stBuyMove, buys: []stBuyItem{{2, 10}}, destPort: 10002, profit: 600},
		},
		{
			name:  "destination must crave a good we can carry — profitable goods but rare is a dead listing",
			port:  10001,
			funds: 5000,
			markets: stSea(
				stPortR(10001, 999, stGood(2, 200, 10)),
				stPortR(10002, 3, stGood(2, 260, 99)), // craves good 3, unlisted anywhere
			),
			want: stPlan{action: stWait},
		},
		{
			name:  "cash runs out along the list — lower-ratio lines get the change, never the debt",
			port:  10001,
			funds: 1500,
			markets: stSea(
				stPortR(10001, 999, stGood(1, 100, 50), stGood(2, 200, 50)),
				stPortR(10002, 1, stGood(1, 150, 99), stGood(2, 260, 99)),
			),
			// g1 ratio .5 fills 15 units (1500 xu) → g2 gets nothing.
			want: stPlan{action: stBuyMove, buys: []stBuyItem{{1, 15}}, destPort: 10002, profit: 750},
		},
		{
			name:    "specialty table anchors the run on a port craving THIS port's specialty, even against a richer craving port",
			port:    10001,
			funds:   1000,
			special: 1, // good 1 is port 1's own specialty
			markets: stSea(
				stPortR(10001, 999, stGood(1, 100, 50), stGood(2, 100, 50)),
				stPortR(10002, 2, stGood(1, 200, 99), stGood(2, 900, 99)), // craves good 2 — richer, WRONG door
				stPortR(10003, 1, stGood(1, 180, 99)),                     // craves our specialty
			),
			// good 2 rides along toward 10003 only if listed profitable
			// there — 10003 does not list it, so the load is specialty alone.
			want: stPlan{action: stBuyMove, buys: []stBuyItem{{1, 10}}, destPort: 10003, profit: 800},
		},
		{
			name:    "specialty has no craving port yet — wait, no other-goods shortcut",
			port:    10001,
			funds:   1000,
			special: 1,
			markets: stSea(
				stPortR(10001, 999, stGood(1, 100, 50), stGood(2, 100, 50)),
				stPortR(10002, 2, stGood(1, 900, 99), stGood(2, 900, 99)), // craves 2 only
			),
			want: stPlan{action: stWait},
		},
		{
			name:    "no port reports our good scarce — high prices alone are NOT enough",
			port:    10001,
			funds:   1000,
			markets: stSea(m1Low, stPortR(10002, 999, stGood(1, 500, 100))),
			want:    stPlan{action: stWait},
		},
		{
			name:    "partial map is enough — one scarce port craving our cheapest good wins now",
			port:    10001,
			funds:   1000,
			markets: []*proto.SkyTradePortGoodsInfo{m1Low, m2High}, // 2 of 15 reported
			want:    stPlan{action: stBuyMove, buys: []stBuyItem{{1, 10}}, destPort: 10002, profit: 800},
		},
		{
			name:    "profitable cargo sells where it stands (scarce here)",
			port:    10002,
			cargo:   []*proto.SkyTradeGoods{stCargo(1, 10, 1000)},
			markets: stSea(m1Low, m2High),
			want:    stPlan{action: stSellAt, profit: 800},
		},
		{
			name:    "profit here but nobody reported it scarce — do not sell",
			port:    10002,
			cargo:   []*proto.SkyTradeGoods{stCargo(1, 10, 1000)},
			markets: stSea(m1Low, stPortR(10002, 999, stGood(1, 180, 100))),
			want:    stPlan{action: stWait},
		},
		{
			name:    "cargo sells better across the sea (scarce there)",
			port:    10001,
			cargo:   []*proto.SkyTradeGoods{stCargo(1, 10, 2000)},
			markets: stSea(m1Low, stPortR(10002, 1, stGood(1, 250, 9))),
			want:    stPlan{action: stMoveSell, destPort: 10002, profit: 500},
		},
		{
			name:    "loss everywhere parks the ship",
			port:    10001,
			cargo:   []*proto.SkyTradeGoods{stCargo(1, 10, 2000)},
			markets: stSea(m1Low, m2Mid),
			want:    stPlan{action: stWait},
		},
		{
			name:    "current port cannot sell it — move anyway (scarce there)",
			port:    10001,
			cargo:   []*proto.SkyTradeGoods{stCargo(2, 5, 100)},
			markets: stSea(m1Low, stPortR(10002, 2, stGood(2, 50, 9))),
			want:    stPlan{action: stMoveSell, destPort: 10002, profit: 150},
		},
		{
			name:    "scarce port must match OUR good, not any good",
			port:    10001,
			cargo:   []*proto.SkyTradeGoods{stCargo(1, 10, 1000)},
			markets: stSea(m1Low, stPortR(10002, 2, stGood(1, 180, 100), stGood(2, 10, 1))),
			want:    stPlan{action: stWait},
		},
		{
			name:    "stock caps the buy even with deep pockets",
			port:    10001,
			funds:   1_000_000_000,
			markets: stSea(stPortR(10001, 999, stGood(1, 100, 7)), m2High),
			want:    stPlan{action: stBuyMove, buys: []stBuyItem{{1, 7}}, destPort: 10002, profit: 560},
		},
		{
			name:    "no money, no cargo, no plan",
			port:    10001,
			funds:   0,
			markets: stSea(m1Low, m2High),
			want:    stPlan{action: stWait},
		},
		{
			name:    "zero-stock goods are unbuyable",
			port:    10001,
			funds:   10_000,
			markets: stSea(stPortR(10001, 999, stGood(1, 100, 0)), m2High),
			want:    stPlan{action: stWait},
		},
	}
	for _, c := range cases {
		got := skyTradeDecide(c.port, c.funds, c.cargo, c.markets, c.special)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestSkyTradeDealReqPacking(t *testing.T) {
	// Ordinary quantities: low half only, type carries just the mode.
	r := skyTradeDealReq(12345, 10001, 7, 1000, stModeBuy)
	if r.GetNum() != 1000 || r.GetType() != 3 {
		t.Errorf("buy 1000: num=%d type=%d, want 1000/3", r.GetNum(), r.GetType())
	}
	r = skyTradeDealReq(12345, 10001, 7, 1000, stModeSell)
	if r.GetNum() != 1000 || r.GetType() != 0 {
		t.Errorf("sell 1000: num=%d type=%d, want 1000/0", r.GetNum(), r.GetType())
	}
	// The client's 64-bit path: high half rides in type<<2.
	r = skyTradeDealReq(12345, 10001, 7, (int64(1)<<32)+5, stModeBuy)
	if r.GetNum() != 5 || r.GetType() != 7 { // (1<<2)+3
		t.Errorf("buy 2^32+5: num=%d type=%d, want 5/7", r.GetNum(), r.GetType())
	}
}

func TestSkyTradeArrivalAt(t *testing.T) {
	start := int64(1_700_000_000_000) // ms
	got := skyTradeArrivalAt(start, 3, 14400, 300)
	if want := int64(1_700_000_000) + 14400 - 900; got != want {
		t.Fatalf("arrivalAt=%d want %d", got, want)
	}
}

func TestStMergeConfigTravelTimes(t *testing.T) {
	p := &Player{Username: "u"}
	detail := &proto.RspGetActivityDetail{Activity: &proto.ActivityCommonData{
		ActivityId: pb.Int32(1500),
		DetailConfig: &proto.ActivityDetailConfig{CommonConfig: &proto.ActivityCommonConfig{
			MainConfig:  &proto.ActivityMainConfig{ActivityId: pb.Int32(1500), Type: pb.Int32(150)},
			ParamConfig: []*proto.ActivityParamConfig{{Param: pb.String("RUNBUSINESS_MOVE_TIME"), Value: pb.String("3600")}},
		}},
	}}
	data, err := pb.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	p.stMergeConfig(data)
	if need, up := p.stTravelTimes(); need != 3600 || up != 300 {
		t.Fatalf("after MOVE_TIME=3600: need=%d up=%d, want 3600/300 (fallback)", need, up)
	}

	// A non-SkyTrade answer must not touch the timings.
	other := &proto.RspGetActivityDetail{Activity: &proto.ActivityCommonData{
		ActivityId: pb.Int32(77),
		DetailConfig: &proto.ActivityDetailConfig{CommonConfig: &proto.ActivityCommonConfig{
			MainConfig:  &proto.ActivityMainConfig{ActivityId: pb.Int32(77), Type: pb.Int32(18)},
			ParamConfig: []*proto.ActivityParamConfig{{Param: pb.String("RUNBUSINESS_MOVE_TIME"), Value: pb.String("99")}},
		}},
	}}
	data, err = pb.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	p.stMergeConfig(data)
	if need, _ := p.stTravelTimes(); need != 3600 {
		t.Fatalf("type-18 answer leaked MOVE_TIME: %d", need)
	}
	// Garbage never panics and changes nothing.
	p.stMergeConfig([]byte{0xFF, 0xFF})
}

// ─── loop plumbing ───────────────────────────────────────────────────────────

func stMemDB(t *testing.T, autoOn bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:thiendo_%s_%v?mode=memory&cache=shared", t.Name(), autoOn)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.User{ID: "1", Username: "u", GameUID: 42, AutoSkyTrade: autoOn}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func stLoopPlayer(t *testing.T, autoOn bool) *Player {
	t.Helper()
	now := time.Now()
	p := &Player{
		Username:    "u",
		UID:         42,
		DB:          stMemDB(t, autoOn),
		ChatID:      7,
		Notify:      true,
		NotiChan:    make(chan NotifyMGS, 16),
		writeChan:   make(chan []byte, 64),
		stPoll:      100 * time.Millisecond, // production default: 1 min poll
		// Opt-in gates are Player fields (owner 2026-10-27) — the DB row
		// no longer steers the loop.
		AutoSkyTrade: autoOn,
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	t.Cleanup(cancel)
	setActivities(&p.ActList, activityEntry{
		ActivityID: 1500, Type: skyTradeActivityType,
		BeginTime: now.Add(-time.Hour).UnixMilli(), EndTime: now.Add(time.Hour).UnixMilli(),
	})
	return p
}

func stExpectCmd(t *testing.T, p *Player, want string, within time.Duration) []byte {
	t.Helper()
	select {
	case f := <-p.writeChan:
		if ft := frameFuncType(t, f); ft != want {
			t.Fatalf("sent %s, want %s", ft, want)
		}
		return f
	case <-time.After(within):
		t.Fatalf("no %s frame within %v", want, within)
		return nil
	}
}

func stAnswer(t *testing.T, p *Player, msgType string, m pb.Message) {
	t.Helper()
	data, err := pb.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p.stHandleFrame(msgType, data)
}

// The full turn: snapshot → buy the best margin → sail → notification →
// next snapshot answers with the daily window closed, after which the loop
// must fall quiet.
func TestSkyTradeAutoLoopTrades(t *testing.T) {
	p := stLoopPlayer(t, true)
	m1 := stPortR(10001, 999, stGood(1, 100, 50)) // reported, craves nothing we hold
	m2 := stPortR(10002, 1, stGood(1, 180, 100))  // scarce good 1 here → legal target

	go p.skyTradeAutoLoopAfter(0)

	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 3*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AirshipInfo:    &proto.SkyTradeAirshipInfo{SourcePort: pb.Int32(10001)},
			AvailableFunds: pb.Int64(1000),
		},
		PortGoods: stSea(m1, m2),
	})

	buyFrame := stExpectCmd(t, p, SKY_TRADE_BUY_CMD, 5*time.Second)
	var deal proto.SkyTradeDealReq
	if err := pb.Unmarshal(buyFrame[18:], &deal); err != nil {
		t.Fatal(err)
	}
	if deal.GetPort() != 10001 || deal.GetGoodsId() != 1 || deal.GetNum() != 10 || deal.GetType() != 3 {
		t.Fatalf("deal %v/%v × %v type %v, want buy 10 × goods 1 at 10001",
			deal.GetPort(), deal.GetGoodsId(), deal.GetNum(), deal.GetType())
	}
	stAnswer(t, p, SERVER_SKY_TRADE_BUY, &proto.SkyTradeDealResp{Ret: pb.Int32(0)})

	moveFrame := stExpectCmd(t, p, SKY_TRADE_MOVE_CMD, 5*time.Second)
	var mv proto.SkyTradeGotoPortReq
	if err := pb.Unmarshal(moveFrame[18:], &mv); err != nil {
		t.Fatal(err)
	}
	if mv.GetPort() != 10002 {
		t.Fatalf("sailing to %d, want 10002", mv.GetPort())
	}
	stAnswer(t, p, SERVER_SKY_TRADE_MOVE, &proto.SkyTradeGotoPortResp{
		Ret: pb.Int32(0),
		AirshipInfo: &proto.SkyTradeAirshipInfo{
			SourcePort: pb.Int32(10001), TargetPort: pb.Int32(10002),
			StartTime: pb.Int64(time.Now().UnixMilli()),
		},
	})

	// Every action notifies: the accepted buy AND the departure.
	var gotBuy, gotSail bool
	for !gotBuy || !gotSail {
		select {
		case n := <-p.NotiChan:
			switch {
			case strings.Contains(n.MSG, "đã mua 10 × hàng 1 ở cảng 1"):
				gotBuy = true
			case strings.Contains(n.MSG, "xuất phát cảng 1 → cảng 2") && strings.Contains(n.MSG, "800"):
				gotSail = true
			default:
				t.Fatalf("unexpected notice: %q", n.MSG)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("missing notices (buy=%v sail=%v)", gotBuy, gotSail)
		}
	}

	// One second later the loop re-snapshots; this answer says the trading
	// window closed → quiet back-off, no further requests.
	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 5*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, &proto.SkyTradeEnterResp{Ret: pb.Int32(3)})
	select {
	case f := <-p.writeChan:
		t.Fatalf("quiet back-off continued sending: %x", f)
	case <-time.After(500 * time.Millisecond):
	}
}

// Idle with every price below cost: NO deal/sail frames may ever go out,
// and the đạo hữu is alerted at once (the 3-minute gate is the loop's own).
func TestSkyTradeAutoLoopWaitsAndAlerts(t *testing.T) {
	p := stLoopPlayer(t, true)
	go p.skyTradeAutoLoopAfter(0)

	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 3*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	losing := &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AirshipInfo:    &proto.SkyTradeAirshipInfo{SourcePort: pb.Int32(10001)},
			AvailableFunds: pb.Int64(0),
			Goods:          []*proto.SkyTradeGoods{stCargo(1, 10, 2000)}, // cost 2000
		},
		PortGoods: stSea(
			stPortR(10001, 999, stGood(1, 100, 50)),
			// 2500 sell would be profitable, but nobody reported it
			// scarce — the map is complete, and the ship still refuses
			// the run and waits.
			stPortR(10002, 999, stGood(1, 250, 50)),
		),
	}
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, losing)

	select {
	case n := <-p.NotiChan:
		if !strings.Contains(n.MSG, "chưa có cảng nào") || !strings.Contains(n.MSG, "cảng 1") {
			t.Fatalf("wait alert: %q", n.MSG)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no wait alert")
	}

	// Keep answering the polls so idle turns COMPLETE. The 5-minute status
	// heartbeat was removed (owner 2026-10-27) — after the single wait alert
	// (nextAlert gates the next one at 3 min) the loop must stay SILENT and
	// must NEVER deal or sail — no scarce port matches the cargo.
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case f := <-p.writeChan:
			switch ft := frameFuncType(t, f); ft {
			case SKY_TRADE_ENTER_CMD:
			case SKY_TRADE_ENTER_MAP_CMD:
				stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, losing)
			default:
				t.Fatalf("waiting ship sent %s", ft)
			}
		case n := <-p.NotiChan:
			if strings.Contains(n.MSG, "chưa có cảng nào") {
				continue // the alert may surface again in this window
			}
			t.Fatalf("unexpected notice: %q", n.MSG)
		case <-deadline:
			break loop // quiet idle = the whole point
		}
	}
}

// The one-minute poll paces PRICE WATCHING only. With prices that say
// "sell here", the very same breath must continue into re-snapshot → buy →
// sail even when the poll interval is an hour.
func TestSkyTradeAutoLoopSellFlowsIntoBuyWithoutWait(t *testing.T) {
	p := stLoopPlayer(t, true)
	p.stPoll = time.Hour // if the chain ever waits a poll, this test times out
	go p.skyTradeAutoLoopAfter(0)

	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 3*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AirshipInfo:    &proto.SkyTradeAirshipInfo{SourcePort: pb.Int32(10001)},
			AvailableFunds: pb.Int64(500),
			Goods:          []*proto.SkyTradeGoods{stCargo(1, 10, 1000)}, // 1800 here beats 1000
		},
		PortGoods: stSea(
			stPortR(10001, 1, stGood(1, 180, 50)),   // scarce here → sell-in-place allowed
			stPortR(10002, 999, stGood(1, 200, 50)), // pricier but reports a different scarce good: not a target
		),
	})

	sell := stExpectCmd(t, p, SKY_TRADE_BUY_CMD, 5*time.Second)
	var sreq proto.SkyTradeDealReq
	if err := pb.Unmarshal(sell[18:], &sreq); err != nil {
		t.Fatal(err)
	}
	if sreq.GetType() != 0 || sreq.GetNum() != 10 || sreq.GetPort() != 10001 {
		t.Fatalf("sell req type=%d num=%d port=%d, want 0/10/10001", sreq.GetType(), sreq.GetNum(), sreq.GetPort())
	}
	stAnswer(t, p, SERVER_SKY_TRADE_BUY, &proto.SkyTradeDealResp{Ret: pb.Int32(0), AvailableFunds: pb.Int64(2300)})

	// Sold → immediate re-snapshot (no hour wait), which now says buy+go.
	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 3*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AirshipInfo:    &proto.SkyTradeAirshipInfo{SourcePort: pb.Int32(10001)},
			AvailableFunds: pb.Int64(2300),
		},
		PortGoods: stSea(
			stPortR(10001, 999, stGood(1, 100, 50)),
			stPortR(10002, 1, stGood(1, 180, 100)), // scarce good 1 → buy+move target
		),
	})

	buy := stExpectCmd(t, p, SKY_TRADE_BUY_CMD, 3*time.Second)
	var breq proto.SkyTradeDealReq
	if err := pb.Unmarshal(buy[18:], &breq); err != nil {
		t.Fatal(err)
	}
	if breq.GetType() != 3 || breq.GetNum() != 23 { // min(stock 50, 2300/100)
		t.Fatalf("buy req type=%d num=%d, want 3/23", breq.GetType(), breq.GetNum())
	}
	stAnswer(t, p, SERVER_SKY_TRADE_BUY, &proto.SkyTradeDealResp{Ret: pb.Int32(0)})
	sail := stExpectCmd(t, p, SKY_TRADE_MOVE_CMD, 3*time.Second)
	var mv proto.SkyTradeGotoPortReq
	if err := pb.Unmarshal(sail[18:], &mv); err != nil {
		t.Fatal(err)
	}
	if mv.GetPort() != 10002 {
		t.Fatalf("sail port %d, want 10002", mv.GetPort())
	}
	stAnswer(t, p, SERVER_SKY_TRADE_MOVE, &proto.SkyTradeGotoPortResp{
		Ret: pb.Int32(0),
		AirshipInfo: &proto.SkyTradeAirshipInfo{
			SourcePort: pb.Int32(10001), TargetPort: pb.Int32(10002),
			StartTime: pb.Int64(time.Now().UnixMilli()),
		},
	})

	// The whole chain notified: sell, buy, departure.
	var gotSell, gotBuy, gotSail bool
	deadline := time.After(5 * time.Second)
	for (!gotSell || !gotBuy || !gotSail) && deadline != nil {
		select {
		case n := <-p.NotiChan:
			switch {
			case strings.Contains(n.MSG, "đã bán 10 × hàng 1 ở cảng 1"):
				gotSell = true
			case strings.Contains(n.MSG, "đã mua 23 × hàng 1 ở cảng 1"):
				gotBuy = true
			case strings.Contains(n.MSG, "xuất phát cảng 1 → cảng 2"):
				gotSail = true
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("missing notices: sell=%v buy=%v sail=%v", gotSell, gotBuy, gotSail)
		}
	}

	// Sailing now → quiet until the poll/arrival.
	select {
	case f := <-p.writeChan:
		t.Fatalf("traffic while sailing: %x", f)
	case <-time.After(500 * time.Millisecond):
	}
}

// The DB switch is the whole gate: off means zero sky-trade traffic.
func TestSkyTradeAutoLoopDisabled(t *testing.T) {
	p := stLoopPlayer(t, false)
	go p.skyTradeAutoLoopAfter(0)
	select {
	case f := <-p.writeChan:
		t.Fatalf("disabled account sent traffic: %x", f)
	case <-time.After(500 * time.Millisecond):
	}
}

// ─── end-of-day sprint ───────────────────────────────────────────────────────

func stSetSpeed(t *testing.T, p *Player, on bool) {
	t.Helper()
	p.AutoSkyTradeSpeed = on // Player-object gate (owner 2026-10-27)
}

// The sprint clock arms at 21:58 (the event closes at 22:00) and the test
// override wins over the wall clock.
// Notifications name goods the way the game does; only ids outside the
// language table fall back to "hàng <id>".
func TestStGoodsLabel(t *testing.T) {
	if got := stGoodsLabel(1007); got != "Cửu U Ngọc" {
		t.Fatalf("1007 label: %q", got)
	}
	if got := stGoodsLabel(1); got != "hàng 1" {
		t.Fatalf("unknown id label: %q", got)
	}
}

// The 21003 detail's uniqueConfig carries the event's port table — every
// port's specialty, the anchor an empty-hold run waits on.
func TestStMergeConfigSpecials(t *testing.T) {
	p := &Player{Username: "u"}
	uc, err := pb.Marshal(&proto.SkyTradeConfigSync{
		ActivityId: pb.Int32(1500),
		PortConfig: []*proto.SkyTradePortConfig{
			{PortId: pb.Int32(10001), SpecialGoodId: pb.Int32(7)},
			{PortId: pb.Int32(10002), SpecialGoodId: pb.Int32(3)},
			{PortId: pb.Int32(10003)}, // unconfigured port stays unknown
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail := &proto.RspGetActivityDetail{Activity: &proto.ActivityCommonData{
		ActivityId: pb.Int32(1500),
		DetailConfig: &proto.ActivityDetailConfig{
			UniqueConfig: uc,
			CommonConfig: &proto.ActivityCommonConfig{
				MainConfig: &proto.ActivityMainConfig{ActivityId: pb.Int32(1500), Type: pb.Int32(150)},
			},
		},
	}}
	data, err := pb.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	p.stMergeConfig(data)
	if got := p.stSpecialGood(10001); got != 7 {
		t.Fatalf("port 1 specialty: %d, want 7", got)
	}
	if got := p.stSpecialGood(10003); got != 0 {
		t.Fatalf("unknown port must stay 0, got %d", got)
	}
}

func TestStLastRunPlan(t *testing.T) {
	rnd := func(n int) int { return 0 } // deterministic: lowest roll
	// Empty hold + cash: buy the port's SPECIALTY — one line, all the
	// wallet, no margin math (it's a position on tomorrow's board).
	got := stLastRunPlan(10001, 1000, nil, stPortR(10001, 999,
		stGood(1, 100, 50), stGood(2, 50, 5), stGood(3, 10, 2)), 1, rnd)
	want := stPlan{action: stLastRun, destPort: 10002, buys: []stBuyItem{{1, 10}}} // min(stock 50, 1000/100)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("specialty load: %+v, want %+v", got, want)
	}
	// Specialty out of stock or unaffordable → no bet (no substitution).
	if p := stLastRunPlan(10001, 1000, nil, stPortR(10001, 999, stGood(1, 100, 0)), 1, rnd); p.action != stWait {
		t.Fatalf("specialty unstocked: %+v", p)
	}
	if p := stLastRunPlan(10001, 50, nil, stPortR(10001, 999, stGood(1, 100, 5)), 1, rnd); p.action != stWait {
		t.Fatalf("specialty unaffordable: %+v", p)
	}
	// Specialty unknown (table never landed): fall back to the cheapest
	// listings, wallet first on the lowest unit price.
	got = stLastRunPlan(10001, 1000, nil, stPortR(10001, 999,
		stGood(1, 100, 50), stGood(2, 50, 5), stGood(3, 10, 2)), 0, rnd)
	want = stPlan{action: stLastRun, destPort: 10002, buys: []stBuyItem{{3, 2}, {2, 5}, {1, 7}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback load: %+v, want %+v", got, want)
	}
	// Cargo aboard: carry it through the night — never sell at a plain
	// port just to re-buy, and no buys needed.
	got = stLastRunPlan(10001, 0, []*proto.SkyTradeGoods{stCargo(1, 10, 1000)}, nil, 1, rnd)
	if !reflect.DeepEqual(got, stPlan{action: stLastRun, destPort: 10002}) {
		t.Fatalf("with cargo: %+v", got)
	}
	// Nothing to bet: empty hold, no cash / no listings.
	if p := stLastRunPlan(10001, 0, nil, stPortR(10001, 999, stGood(1, 100, 5)), 0, rnd); p.action != stWait {
		t.Fatalf("no cash: %+v", p)
	}
	if p := stLastRunPlan(10001, 1000, nil, stPortR(10001, 999, stGood(1, 100, 0)), 0, rnd); p.action != stWait {
		t.Fatalf("nothing listed: %+v", p)
	}
	// The random berth is uniform over the OTHER 14 ports — never our own.
	for roll := 0; roll < 14; roll++ {
		r := func(int) int { return roll }
		for _, home := range []int32{10001, 10008, 10015} {
			p := stLastRunPlan(home, 0, []*proto.SkyTradeGoods{stCargo(1, 1, 1)}, nil, 0, r)
			if p.action != stLastRun || p.destPort == home || p.destPort < 10001 || p.destPort > 10015 {
				t.Fatalf("roll %d from %d: %+v", roll, home, p)
			}
		}
	}
	// Junk port id → no bet, no panic.
	if p := stLastRunPlan(7, 1000, nil, stPortR(7, 999, stGood(1, 1, 1)), 0, rnd); p.action != stWait {
		t.Fatalf("bogus port: %+v", p)
	}
}

// Sprint clock armed + switch on + nothing profitable: the ship does not
// idle into the close — it sails the random last run (notify first).
func TestSkyTradeAutoLoopLastRunSails(t *testing.T) {
	p := stLoopPlayer(t, true)
	stSetSpeed(t, p, true)
	p.stSprintTest = func(time.Time) bool { return true }
	go p.skyTradeAutoLoopAfter(0)

	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 5*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AvailableFunds: pb.Int64(600),
			AirshipInfo:    &proto.SkyTradeAirshipInfo{SourcePort: pb.Int32(10001)},
			// rare 999 listed nowhere → the planner waits → the gamble runs.
		},
		PortGoods: []*proto.SkyTradePortGoodsInfo{
			stPortR(10001, 999, stGood(2, 120, 5)), // no profitable port anywhere
		},
	})
	// The cheapest-good load goes out first: one buy of 5 × good 2
	// (funds 600 ÷ 120).
	body := stExpectCmd(t, p, SKY_TRADE_BUY_CMD, 5*time.Second)
	var deal proto.SkyTradeDealReq
	if err := pb.Unmarshal(body[18:], &deal); err != nil {
		t.Fatal(err)
	}
	if deal.GetType() != 3 || deal.GetNum() != 5 || deal.GetGoodsId() != 2 {
		t.Fatalf("last-run buy: %+v", &deal)
	}
	stAnswer(t, p, SERVER_SKY_TRADE_BUY, &proto.SkyTradeDealResp{Ret: pb.Int32(0), AvailableFunds: pb.Int64(0)})
	// Then the sail — to a random port (never our own).
	sailBody := stExpectCmd(t, p, SKY_TRADE_MOVE_CMD, 5*time.Second)
	var mv proto.SkyTradeGotoPortReq
	if err := pb.Unmarshal(sailBody[18:], &mv); err != nil {
		t.Fatal(err)
	}
	if mv.GetPort() == 10001 || mv.GetPort() < 10001 || mv.GetPort() > 10015 {
		t.Fatalf("last-run sail target %d", mv.GetPort())
	}
	stAnswer(t, p, SERVER_SKY_TRADE_MOVE, &proto.SkyTradeGotoPortResp{Ret: pb.Int32(0)})
	// The user was told before the anchor came up.
	seenNotice := false
	for {
		select {
		case n := <-p.NotiChan:
			if strings.Contains(n.MSG, "chuyến cuối") {
				seenNotice = true
			}
		default:
			goto done
		}
	}
done:
	if !seenNotice {
		t.Fatal("no last-run notice")
	}
	// And only once per day: the next waiting turn must not gamble again.
	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 5*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AvailableFunds: pb.Int64(600),
			AirshipInfo:    &proto.SkyTradeAirshipInfo{SourcePort: pb.Int32(10003)},
		},
		PortGoods: []*proto.SkyTradePortGoodsInfo{stPortR(10003, 999, stGood(2, 120, 5))},
	})
	select {
	case f := <-p.writeChan:
		if ft := frameFuncType(t, f); ft == SKY_TRADE_BUY_CMD || ft == SKY_TRADE_MOVE_CMD {
			t.Fatalf("second last run on the same day: %s", ft)
		}
	case <-time.After(400 * time.Millisecond):
	}
}

// The live bug: after the window's speed-up docked the ship, the normal
// brain found a profitable leg and sailed it — then the sprint dutifully
// speed-up that one too, dock to dock, burning the whole bag while the
// random last run never fired. Once the day is closed out (speed-up spent
// AND last run done), trade traffic must be zero until the next open.
func TestSkyTradeAutoLoopEndOfDayStopsTrading(t *testing.T) {
	p := stLoopPlayer(t, true)
	stSetSpeed(t, p, true)
	p.stSprintTest = func(time.Time) bool { return true }
	today := time.Now().In(ictZone).Format("2006-01-02")
	p.stSprintBurnDay = today // the window's one speed-up already spent
	p.stLastRunDay = today    // the gamble already sailed
	p.stSpecials = map[int32]int32{10001: 1}
	go p.skyTradeAutoLoopAfter(0)

	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 5*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	// A plan the pre-fix loop would chase: our specialty reported scarce
	// at 10002 with a fat margin.
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AvailableFunds: pb.Int64(10_000),
			AirshipInfo:    &proto.SkyTradeAirshipInfo{SourcePort: pb.Int32(10001)},
		},
		PortGoods: stSea(
			stPortR(10001, 999, stGood(1, 100, 50)),
			stPortR(10002, 1, stGood(1, 300, 99)),
		),
	})
	deadline := time.After(600 * time.Millisecond)
	for {
		select {
		case f := <-p.writeChan:
			ft := frameFuncType(t, f)
			if ft == SKY_TRADE_BUY_CMD || ft == SKY_TRADE_MOVE_CMD || ft == SKY_TRADE_SPEED_UP_CMD {
				t.Fatalf("trade traffic after the day was closed out: %s", ft)
			}
		case <-deadline:
			return
		}
	}
}

// One speed-up per event day, period: with the day's burn already spent,
// an in-flight leg must wait out at normal pace (this was the 21:59
// second-speed-up storm).
func TestSkyTradeAutoLoopSpeedUpOncePerDay(t *testing.T) {
	p := stLoopPlayer(t, true)
	stSetSpeed(t, p, true)
	p.stSprintTest = func(time.Time) bool { return true }
	p.stSprintBurnDay = time.Now().In(ictZone).Format("2006-01-02")
	go p.skyTradeAutoLoopAfter(0)

	sailing := &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AirshipInfo: &proto.SkyTradeAirshipInfo{
				SourcePort: pb.Int32(10001), TargetPort: pb.Int32(10002),
				StartTime: pb.Int64(time.Now().Add(-time.Minute).UnixMilli()),
			},
		},
	}
	for i := 0; i < 3; i++ {
		// stExpectCmd fatals on any non-ENTER frame — a SPEED_UP here is
		// exactly the bug.
		stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 5*time.Second)
		stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
		stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, sailing)
	}
}

func TestStSprintArmed(t *testing.T) {
	p := &Player{}
	at := func(h, m int) time.Time {
		return time.Date(2026, 9, 28, h, m, 0, 0, ictZone)
	}
	if p.stSprintArmed(at(21, 57)) {
		t.Fatal("armed before 21:58")
	}
	if !p.stSprintArmed(at(21, 58)) {
		t.Fatal("not armed at 21:58")
	}
	// The clock reads the VN wall time, not the server's: the same
	// instants expressed in UTC must agree.
	utcAt := func(h, m int) time.Time {
		return time.Date(2026, 9, 28, h, m, 0, 0, time.UTC)
	}
	if p.stSprintArmed(utcAt(14, 57)) { // 21:57 ICT
		t.Fatal("armed before 21:58 (UTC-stamped instant)")
	}
	if !p.stSprintArmed(utcAt(14, 58)) { // 21:58 ICT
		t.Fatal("not armed at 21:58 (UTC-stamped instant)")
	}
	if !p.stSprintArmed(at(21, 59)) {
		t.Fatal("not armed at 21:59")
	}
	if p.stSprintArmed(at(22, 0)) {
		t.Fatal("armed after close")
	}
	if p.stSprintArmed(at(9, 30)) {
		t.Fatal("armed mid-event day")
	}
	p.stSprintTest = func(time.Time) bool { return true }
	if !p.stSprintArmed(at(12, 0)) {
		t.Fatal("test override ignored")
	}
}

// Sprint armed with the switch on: the in-flight ship's leg gets its
// acceleration items burned until the answer docks it — and a speed-up that
// arrives needs NO 215009 claim (the client plays the same arrival). The
// loop then re-snapshots and keeps trading.
func TestSkyTradeAutoLoopSprintsToPort(t *testing.T) {
	p := stLoopPlayer(t, true)
	stSetSpeed(t, p, true)
	p.stSprintTest = func(time.Time) bool { return true }
	go p.skyTradeAutoLoopAfter(0)

	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 3*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AirshipInfo: &proto.SkyTradeAirshipInfo{
				SourcePort: pb.Int32(10001), TargetPort: pb.Int32(10002),
				StartTime: pb.Int64(time.Now().Add(-time.Minute).UnixMilli()),
			},
		},
	})

	sp := stExpectCmd(t, p, SKY_TRADE_SPEED_UP_CMD, 5*time.Second)
	var req proto.SkyTradeAddSpeedReq
	if err := pb.Unmarshal(sp[18:], &req); err != nil {
		t.Fatal(err)
	}
	if req.GetActivityId() != 1500 {
		t.Fatalf("speed-up activityId %d, want 1500", req.GetActivityId())
	}
	// Left ≈ 14340 s of the 14400 fallback at 300 s/item → 47–48 items.
	if req.GetNum() < 47 || req.GetNum() > 48 {
		t.Fatalf("speed-up num %d, want ~48 (ceil of remaining/300)", req.GetNum())
	}
	stAnswer(t, p, SERVER_SKY_TRADE_SPEED_UP, &proto.SkyTradeAddSpeedResp{
		Ret: pb.Int32(0),
		Event: &proto.SkyTradeArrivalEvent{
			SourcePort: pb.Int32(10001), TargetPort: pb.Int32(10002),
			EventType: pb.Int32(1), AddFame: pb.Int32(12),
		},
	})

	// Docked notice, and the next wake re-snapshots instead of claiming an
	// arrival — any MOVE_FINISH frame would fail the ENTER_CMD expectation.
	select {
	case n := <-p.NotiChan:
		if !strings.Contains(n.MSG, "tăng tốc cập cảng 2") || !strings.Contains(n.MSG, "12 điểm uy tín") {
			t.Fatalf("sprint arrival notice: %q", n.MSG)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no speed-up arrival notice")
	}
	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 5*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AirshipInfo: &proto.SkyTradeAirshipInfo{SourcePort: pb.Int32(10002)},
		},
		PortGoods: stSea(
			stPortR(10002, 999, stGood(1, 100, 50)), // empty hold, no scarce match → plain wait
		),
	})
	select {
	case n := <-p.NotiChan:
		// Sprint test arms the end-of-day clock too: with nothing left to
		// bet (empty hold, no cash) the loop now retires — either that
		// 🌙 notice or the plain wait alert is a correct docking voice.
		if !strings.Contains(n.MSG, "chưa có cảng nào") &&
			!strings.Contains(n.MSG, "chuyến cuối") {
			t.Fatalf("unexpected notice after docking: %q", n.MSG)
		}
	case <-time.After(5 * time.Second):
		// The wait alert carries a 3-minute gate that a previous turn may
		// have armed; a quiet-but-trading loop is still correct.
	}
}

// A pending arrival report must NEVER park the ship at dock (owner
// 2026-10-27 "luôn claim báo cáo cập cảng để có thể chạy tiếp"): the loop
// claims the reward AND trades on the SAME snapshot. <live-user> sat at 10004
// holding good 1013 for hours — port 7 craved exactly it (+6021) — behind
// a claim gate that `continue`d before any decision (and before beat()).
func TestSkyTradeAutoLoopClaimThenTrades(t *testing.T) {
	p := stLoopPlayer(t, true)
	m1 := stPortR(10001, 999, stGood(1, 100, 50))
	m2 := stPortR(10002, 1, stGood(1, 700, 100))

	go p.skyTradeAutoLoopAfter(0)

	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 3*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
	stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AirshipInfo: &proto.SkyTradeAirshipInfo{SourcePort: pb.Int32(10001)},
			Goods:       []*proto.SkyTradeGoods{stGood(1, 100, 1)}, // 1 unit, cost 100
		},
		PortGoods: stSea(m1, m2),
		Event: &proto.SkyTradeArrivalEvent{
			SourcePort: pb.Int32(10001), TargetPort: pb.Int32(10001),
			EventType: pb.Int32(1), AddFame: pb.Int32(9),
		},
	})

	// The reward claim goes out — and the very same turn must still plan:
	// the hold is worth 700 at port 2 (profit 600), so the MOVE follows.
	stExpectCmd(t, p, SKY_TRADE_MOVE_FINISH_CMD, 5*time.Second)
	stAnswer(t, p, SERVER_SKY_TRADE_MOVE_FINISH, &proto.SkyTradeGetRewardResp{
		Ret: pb.Int32(0),
		Event: &proto.SkyTradeArrivalEvent{TargetPort: pb.Int32(10001), AddFame: pb.Int32(9)},
	})
	moveFrame := stExpectCmd(t, p, SKY_TRADE_MOVE_CMD, 5*time.Second)
	var mv proto.SkyTradeGotoPortReq
	if err := pb.Unmarshal(moveFrame[18:], &mv); err != nil {
		t.Fatal(err)
	}
	if mv.GetPort() != 10002 {
		t.Fatalf("sailing to %d, want 10002", mv.GetPort())
	}
	stAnswer(t, p, SERVER_SKY_TRADE_MOVE, &proto.SkyTradeGotoPortResp{
		Ret: pb.Int32(0),
		AirshipInfo: &proto.SkyTradeAirshipInfo{
			SourcePort: pb.Int32(10001), TargetPort: pb.Int32(10002),
			StartTime: pb.Int64(time.Now().UnixMilli()),
		},
	})
}

// The daily 10:00 open: the announced window alone must wake the loop —
// zero sky-trade traffic before the begin time, a snapshot right after.
func TestSkyTradeAutoLoopWakesAtOpen(t *testing.T) {
	p := stLoopPlayer(t, true)
	p.stOpenSettle = 10 * time.Millisecond
	begin := time.Now().Add(300 * time.Millisecond)
	setActivities(&p.ActList, activityEntry{
		ActivityID: 1500, Type: skyTradeActivityType,
		BeginTime: begin.UnixMilli(), EndTime: begin.Add(time.Hour).UnixMilli(),
	})
	go p.skyTradeAutoLoopAfter(0)

	select {
	case f := <-p.writeChan:
		t.Fatalf("traffic before the announced open: %x", f)
	case <-time.After(200 * time.Millisecond):
	}
	stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 3*time.Second)
	stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
}

// Sprint clock armed but the switch OFF: the ship waits out its leg at the
// normal pace and no speed-up ever goes out.
func TestSkyTradeAutoLoopSprintRespectsSwitch(t *testing.T) {
	p := stLoopPlayer(t, true) // auto trade on, speed default off
	p.stSprintTest = func(time.Time) bool { return true }
	go p.skyTradeAutoLoopAfter(0)

	sailing := &proto.SkyTradeEnterResp{
		Ret: pb.Int32(0),
		PlayerData: &proto.SkyTradePlayerData{
			AirshipInfo: &proto.SkyTradeAirshipInfo{
				SourcePort: pb.Int32(10001), TargetPort: pb.Int32(10002),
				StartTime: pb.Int64(time.Now().Add(-time.Minute).UnixMilli()),
			},
		},
	}
	for i := 0; i < 4; i++ {
		stExpectCmd(t, p, SKY_TRADE_ENTER_CMD, 5*time.Second)
		stExpectCmd(t, p, SKY_TRADE_ENTER_MAP_CMD, 3*time.Second)
		stAnswer(t, p, SERVER_SKY_TRADE_ENTER_MAP, sailing)
	}
	// A speed-up frame would have tripped the ENTER_CMD match above; give
	// any late one a last look.
	select {
	case f := <-p.writeChan:
		if ft := frameFuncType(t, f); ft == SKY_TRADE_SPEED_UP_CMD {
			t.Fatal("speed-up sent while the switch is off")
		}
	case <-time.After(300 * time.Millisecond):
	}
}
