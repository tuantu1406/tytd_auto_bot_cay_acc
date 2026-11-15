//go:build live

package tytd_go

// LIVE PROBE (owner 2026-11-07): "event bí cảnh luân hồi đang thiếu tự động:
// nhận thưởng nạp bang, gửi cam lộ, nhận cam lộ, nhận thưởng cây, nhận thưởng
// tăng cấp … test với acc sonbinhsb". Drives the production blind sweeps
// (rbtTreeCycle, rbtCondSweep) on a live sonbinhsb session, then PROBES the
// claim paths for the one feature with no code yet — the per-level reward of
// the caravan character panel (thưởng tăng cấp): per-id 21004 (classic
// condition claim, memory says "silently ignored" — worth a fresh look) and
// UP_LEVEL 217204 (dead client API, never sent; the answer hex 4334 is a
// guess, the probe reports whether it ever appears). NOTHING spends items:
// 217208 USE_ITEM is never sent; 217223 grant stays config-armed as usual.
//
//	TYTD_LIVE=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialLevelClaimLive -v -timeout 300s .

import (
	"context"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRebornTrialLevelClaimLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710564
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}

	pt, err := request.LoginGPassport(user, pass)
	if err != nil {
		t.Fatal(err)
	}
	sd, uidF, err := request.LoginFuntap(pt)
	if err != nil {
		t.Fatal(err)
	}
	gu, err := request.GetServerURL(uidF, server)
	if err != nil {
		t.Fatal(err)
	}
	token, wsURL, uid, err := request.GetWSKey(gu, sd)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, _ := gorm.Open(sqlite.Open("file:rbtlvl?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid), AutoRebornTrial: true}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 256), writeChan: make(chan []byte, 256),
		AutoRebornTrial: true}

	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			isMSG, msgType, realData := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.ActList.mergeDetail(p.Username, realData)
				p.rbtMergeConfig(realData)
				p.stDeliver(msgType, realData)
			case SERVER_ACTIVITY_BUY_GOODS, SERVER_ACTIVITY_COND_REWARD,
				SERVER_ACTIVITY_CROSS_RECHARGE, SERVER_ACTIVITY_TOKEN_REWARD:
				p.stDeliver(msgType, realData)
			default:
				// Every SERVER_RBT_* (+ any unknown answer the probe fires
				// at, e.g. the 4334 UP_LEVEL guess) goes through the
				// production frame handler and the seat deliverer.
				p.rbtHandleFrame(msgType, realData)
				p.stDeliver(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-readerDone }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// ── wait for the live 172 window (owner says the event is open) ──
	deadline := time.Now().Add(30 * time.Second)
	var actID int32
	for time.Now().Before(deadline) {
		now := time.Now()
		for _, e := range p.ActList.byType(rbtActivityType) {
			if e.status(now) == "active" {
				actID = e.ActivityID
			}
		}
		if actID != 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if actID == 0 {
		t.Fatal("no active type-172 activity on server", server)
	}
	t.Logf("live 172 activityId=%d", actID)

	// ── fold config via 21003 (production merge) ──
	body, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID})
	if _, err := p.rbtRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err != nil {
		t.Logf("21003 pull err=%v — continuing with whatever folded", err)
	}
	p.RbtMu.Lock()
	types := map[int32]int{}
	var sampleCross []int32
	for _, c := range p.rbtCondCfgs {
		types[c.GetType()]++
		if c.GetType() == 84 || c.GetConditionId() >= 20000 {
			sampleCross = append(sampleCross, c.GetConditionId())
		}
	}
	unclaimed := make([]int32, 0, len(p.rbtCondData))
	for id, cd := range p.rbtCondData {
		if !cd.GetIsGetReward() {
			unclaimed = append(unclaimed, id)
		}
	}
	t.Logf("condCfgs=%d types=%v crossRows=%v condData=%d unclaimed=%d liquidIDs=%v energyItem=%d",
		len(p.rbtCondCfgs), types, sampleCross, len(p.rbtCondData), len(unclaimed), p.rbtLiquidIDs, p.rbtEnergyItem)
	p.RbtMu.Unlock()
	baselineUnclaimed := len(unclaimed)

	// ── condCfgs anatomy: which group is "thưởng tăng cấp"? ──
	seen := map[int32]int{}
	for _, c := range p.rbtCondCfgs {
		typ := c.GetType()
		seen[typ]++
		if seen[typ] > 3 {
			continue
		}
		t.Logf("cfg type=%d cond=%d title=%q desc=%q param=%v rewards=%q",
			typ, c.GetConditionId(), c.GetTitle(), c.GetDesc(), c.GetParam(), c.GetRewards())
	}

	// ── raw per-type 21033 (one per group) — log EVERY ret ──
	for typ := range seen {
		b, _ := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(typ)})
		data, err := p.rbtRoundTrip(ACTIVITY_TOKEN_REWARD_CMD, SERVER_ACTIVITY_TOKEN_REWARD, b)
		if err != nil {
			t.Logf("21033 type %d: err %v", typ, err)
			continue
		}
		var rsp proto.RspGetActivityConditionReward
		_ = pb.Unmarshal(data, &rsp)
		t.Logf("21033 type %d: ret=%d rewards=%q", typ, rsp.GetRet(), rsp.GetRewards())
	}

	// ── per-ROW 21033 (conditionId = row id) on an unclaimed sample — the
	// alternative shape for a per-level ladder claim ──
	{
		p.RbtMu.Lock()
		var sample []int32
		for id, cd := range p.rbtCondData {
			if !cd.GetIsGetReward() {
				sample = append(sample, id)
			}
		}
		p.RbtMu.Unlock()
		sort.Slice(sample, func(i, j int) bool { return sample[i] < sample[j] })
		if len(sample) > 10 {
			sample = sample[:10]
		}
		for _, cid := range sample {
			b, _ := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(cid)})
			data, err := p.rbtRoundTrip(ACTIVITY_TOKEN_REWARD_CMD, SERVER_ACTIVITY_TOKEN_REWARD, b)
			if err != nil {
				t.Logf("21033 row %d: err %v", cid, err)
				continue
			}
			var rsp proto.RspGetActivityConditionReward
			_ = pb.Unmarshal(data, &rsp)
			t.Logf("21033 row %d: ret=%d rewards=%q", cid, rsp.GetRet(), rsp.GetRewards())
		}
	}

	// ── baseline ENTER: level/exp/hp for the level-reward diff ──
	enter, err := p.rbtEnter(actID)
	if err != nil || enter.GetRet() != 0 {
		t.Fatalf("ENTER err=%v ret=%d", err, enter.GetRet())
	}
	pd := enter.GetPlayerData()
	t.Logf("BEFORE: level=%d exp=%d hp=%d", pd.GetLevel(), pd.GetExp(), pd.GetHp())

	// ── the five production blind passes ──
	p.rbtTreeCycle(actID) // grant (217223) + harvest (217221) + water (217222) blind
	p.rbtCondSweep(actID) // nạp bang per-id 21045 + per-type blind 21033

	// ── raw rets for the tree ops the blind cycle just fired (production
	// swallows non-0 quietly; the probe needs the actual numbers) ──
	if data, err := p.rbtRoundTrip(RBT_BONUS_RESULT_CMD, SERVER_RBT_BONUS_RESULT, rbtActivityBody(actID)); err != nil {
		t.Logf("217221 harvest: err %v", err)
	} else {
		var rsp proto.RebornTrialGetBonusResultResp
		_ = pb.Unmarshal(data, &rsp)
		t.Logf("217221 harvest: ret=%d exp=%d reward=%q", rsp.GetRet(), rsp.GetExp(), rsp.GetReward())
	}
	waterBody, _ := pb.Marshal(&proto.RebornTrialGetBonusItemReq{ActivityId: actID, Id: 0})
	if data, err := p.rbtRoundTrip(RBT_BONUS_ITEM_CMD, SERVER_RBT_BONUS_ITEM, waterBody); err != nil {
		t.Logf("217222 water: err %v", err)
	} else {
		var rsp proto.RebornTrialGetBonusItemResp
		_ = pb.Unmarshal(data, &rsp)
		t.Logf("217222 water: ret=%d reward=%q", rsp.GetRet(), rsp.GetReward())
	}
	for _, id := range []int32{105170, 105171, 105172} {
		b, _ := pb.Marshal(&proto.RebornTrialSendBonusItemReq{ActivityId: pb.Int32(actID), ItemId: pb.Int32(id), Num: pb.Int32(1)})
		if data, err := p.rbtRoundTrip(RBT_SEND_BONUS_ITEM_CMD, SERVER_RBT_SEND_BONUS_ITEM, b); err != nil {
			t.Logf("217223 grant %d: err %v", id, err)
		} else {
			var rsp proto.RebornTrialSendBonusItemResp
			_ = pb.Unmarshal(data, &rsp)
			t.Logf("217223 grant %d: ret=%d", id, rsp.GetRet())
		}
	}
	{
		p.RbtMu.Lock()
		var crossSample []int32
		for _, c := range p.rbtCondCfgs {
			if c.GetType() == 84 || c.GetConditionId() >= 20000 {
				crossSample = append(crossSample, c.GetConditionId())
			}
		}
		p.RbtMu.Unlock()
		sort.Slice(crossSample, func(i, j int) bool { return crossSample[i] < crossSample[j] })
		if len(crossSample) > 4 {
			crossSample = crossSample[:4]
		}
		for _, cid := range crossSample {
			b, _ := pb.Marshal(&proto.ReceiveCrossUnionRechargeRewardReq{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(cid)})
			if data, err := p.rbtRoundTrip(ACTIVITY_CROSS_RECHARGE_CMD, SERVER_ACTIVITY_CROSS_RECHARGE, b); err != nil {
				t.Logf("21045 cross %d: err %v", cid, err)
			} else {
				var rsp proto.RspGetActivityConditionReward
				_ = pb.Unmarshal(data, &rsp)
				t.Logf("21045 cross %d: ret=%d rewards=%q", cid, rsp.GetRet(), rsp.GetRewards())
			}
		}
	}

	// ── PROBE A: per-id 21004 on UNCLAIMED condition rows (silent server
	// = 15 s timeout each — sample at most 12, smallest ids first) ──
	sort.Slice(unclaimed, func(i, j int) bool { return unclaimed[i] < unclaimed[j] })
	if len(unclaimed) > 8 {
		t.Logf("21004 probe sampling 8 of %d unclaimed", len(unclaimed))
		unclaimed = unclaimed[:8]
	}
	for _, cid := range unclaimed {
		b, _ := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(cid)})
		data, err := p.rbtRoundTrip(ACTIVITY_COND_REWARD_CMD, SERVER_ACTIVITY_COND_REWARD, b)
		if err != nil {
			t.Logf("21004 cond %d: err %v", cid, err)
			continue
		}
		var rsp proto.RspGetActivityConditionReward
		_ = pb.Unmarshal(data, &rsp)
		t.Logf("21004 cond %d: ret=%d rewards=%q", cid, rsp.GetRet(), rsp.GetRewards())
	}

	// ── PROBE B: UP_LEVEL 217204 (never sent in production; answer hex 4334
	// is derived req−2000000 — the probe shows whether it exists at all) ──
	before := pd.GetLevel()
	data, err := p.rbtRoundTrip("00035074", "4334", rbtActivityBody(actID))
	if err != nil {
		t.Logf("UP_LEVEL 217204: no answer (%v) — client-API-dead confirmed", err)
	} else {
		t.Logf("UP_LEVEL 217204 answered rawlen=%d: %x", len(data), data)
		var dr proto.RebornTrialDataResp
		if pb.Unmarshal(data, &dr) == nil {
			t.Logf("UP_LEVEL decodes as DataResp: ret=%d", dr.GetRet())
		}
		// Field-level walk so an unknown shape still reports its ret field.
		for b := data; len(b) > 0; {
			num, wt, n := protowire.ConsumeField(b)
			if n <= 0 {
				break
			}
			if num == 1 && wt == protowire.VarintType {
				v, _ := protowire.ConsumeVarint(b[1:])
				t.Logf("UP_LEVEL f1(varint)=%d", v)
			}
			b = b[n:]
		}
	}
	after, err := p.rbtEnter(actID)
	if err == nil && after.GetPlayerData() != nil {
		t.Logf("AFTER: level=%d exp=%d hp=%d (before level=%d)", after.GetPlayerData().GetLevel(),
			after.GetPlayerData().GetExp(), after.GetPlayerData().GetHp(), before)
	}

	// ── re-fold: how many rows flipped to claimed this pass? ──
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if _, err := p.rbtRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err == nil {
			p.RbtMu.Lock()
			now := 0
			for _, cd := range p.rbtCondData {
				if !cd.GetIsGetReward() {
					now++
				}
			}
			p.RbtMu.Unlock()
			t.Logf("re-fold unclaimed: %d (was %d)", now, baselineUnclaimed)
		}
	}

	// ── notifies the pass produced ──
	for {
		select {
		case n := <-p.NotiChan:
			t.Logf("NOTIFY: %s", n.MSG)
		default:
			return
		}
	}
}
