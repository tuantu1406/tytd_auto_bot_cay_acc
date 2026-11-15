//go:build live

package tytd_go

// LIVE PROBE 2 (owner 2026-11-07, after first blind ship): "acc test đã thấy
// gửi cam lộ, chưa thấy nhận cam lộ — ở giao diện cam lộ có nút gửi cam lộ và
// nút nhận nhanh, nhận nhanh là nhận tất cả cam lộ, cứ spam nút đó là được.
// thưởng cảnh giới trong bảng 'chi tiết cảnh giới' chưa thấy nhận".
//
// Three questions, answered with raw rets on sonbinhsb:
//
//	A) "nhận nhanh": is GET_BONUS_ITEM id=0 really claim-all (it answered
//	SILENT last probe), or must the bot fire one 217222 per ready notice id
//	from the 217220 board? Dump the board, fire id=0 AND every status==0 id,
//	re-dump: status flipping 0→1 proves which shape the server honours.
//	B) "thưởng cảnh giới" (type-307 "Cảnh giới đạt N" rows in the chi tiết
//	cảnh giới panel): per-type 21033 answered ret=210 and per-id 21004 was
//	silent — try the 21045 SHAPE per row (that handler demonstrably talks,
//	ret=204) and per-row 21033 on 307 ids specifically.
//	C) blind-scan the never-seen RBT gap opcodes 217210–217219
//	(0003507a..00035083) with a raw frame tap — if the level ladder has its
//	own claim opcode, it answers here.
//
//	 TYTD_LIVE=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialClaimLive2 -v -timeout 900s .

import (
	"context"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type tapRec struct {
	msgType string
	data    []byte
	at      time.Time
}

func TestRebornTrialClaimLive2(t *testing.T) {
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
	db, _ := gorm.Open(sqlite.Open("file:rbtclaim2?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid)}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 256), writeChan: make(chan []byte, 256)}

	// raw tap: EVERY frame lands here (bounded ring) — the opcode scan reads it.
	var tapMu sync.Mutex
	var tap []tapRec
	tapNote := func(mt string, d []byte) {
		tapMu.Lock()
		tap = append(tap, tapRec{mt, append([]byte(nil), d...), time.Now()})
		if len(tap) > 256 {
			tap = tap[len(tap)-256:]
		}
		tapMu.Unlock()
	}

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
			tapNote(msgType, realData)
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
				p.rbtHandleFrame(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-readerDone }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

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

	// short-timeout raw round trip (probes must not burn 15 s each)
	rt := func(cmd, want string, body []byte) ([]byte, error) {
		return p.stRoundTrip(cmd, want, body, 6*time.Second)
	}

	// fold config (condCfgs for the 307 ladder)
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if _, err := rt(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err != nil {
			t.Logf("21003 pull err=%v", err)
		}
	}

	dumpBoard := func(tag string) []*proto.RebornTrialBonusInfo {
		data, err := rt(RBT_BONUS_CMD, SERVER_RBT_BONUS, rbtActivityBody(actID))
		if err != nil {
			t.Logf("board[%s]: err %v", tag, err)
			return nil
		}
		var rsp proto.RebornTrialBonusResp
		if pb.Unmarshal(data, &rsp) != nil {
			t.Logf("board[%s]: undecodable %x", tag, data)
			return nil
		}
		t.Logf("board[%s]: ret=%d canReceive=%d exp=%d info=%d", tag, rsp.GetRet(), rsp.GetCanReceive(), rsp.GetExp(), len(rsp.GetInfo()))
		for _, in := range rsp.GetInfo() {
			t.Logf("  notice id=%d status=%d itemId=%d name=%q time=%d", in.GetId(), in.GetStatus(), in.GetItemId(), in.GetName(), in.GetTime())
		}
		return rsp.GetInfo()
	}

	// ── A) NHẬN NHANH: board → id=0 → per-id → re-board ──
	before := dumpBoard("before")
	if data, err := rt(RBT_BONUS_ITEM_CMD, SERVER_RBT_BONUS_ITEM,
		func() []byte {
			b, _ := pb.Marshal(&proto.RebornTrialGetBonusItemReq{ActivityId: actID, Id: 0})
			return b
		}()); err != nil {
		t.Logf("217222 id=0: SILENT/no answer (%v)", err)
	} else {
		var rsp proto.RebornTrialGetBonusItemResp
		_ = pb.Unmarshal(data, &rsp)
		t.Logf("217222 id=0: ret=%d reward=%q canReceive=%d", rsp.GetRet(), rsp.GetReward(), rsp.GetCanReceive())
	}
	ids := make([]int64, 0, len(before))
	for _, in := range before {
		if in.GetStatus() == 0 {
			ids = append(ids, in.GetId())
		}
	}
	if len(ids) > 12 {
		t.Logf("per-id probe sampling 12 of %d ready notices", len(ids))
		ids = ids[:12]
	}
	for _, id := range ids {
		b, _ := pb.Marshal(&proto.RebornTrialGetBonusItemReq{ActivityId: actID, Id: id})
		if data, err := rt(RBT_BONUS_ITEM_CMD, SERVER_RBT_BONUS_ITEM, b); err != nil {
			t.Logf("217222 id=%d: SILENT/no answer (%v)", id, err)
			continue
		} else {
			var rsp proto.RebornTrialGetBonusItemResp
			_ = pb.Unmarshal(data, &rsp)
			t.Logf("217222 id=%d: ret=%d reward=%q canReceive=%d", id, rsp.GetRet(), rsp.GetReward(), rsp.GetCanReceive())
		}
	}
	dumpBoard("after")
	// RE-CLAIM the same ids immediately: ret≠0 ⇒ claims are one-shot (no
	// double-pay risk in the engine loop); ret=0+reward again ⇒ the engine
	// MUST keep a local claimed-ids ring.
	for _, id := range ids[:min(4, len(ids))] {
		b, _ := pb.Marshal(&proto.RebornTrialGetBonusItemReq{ActivityId: actID, Id: id})
		if data, err := rt(RBT_BONUS_ITEM_CMD, SERVER_RBT_BONUS_ITEM, b); err != nil {
			t.Logf("217222 RE-claim id=%d: SILENT (%v)", id, err)
			continue
		} else {
			var rsp proto.RebornTrialGetBonusItemResp
			_ = pb.Unmarshal(data, &rsp)
			t.Logf("217222 RE-claim id=%d: ret=%d reward=%q", id, rsp.GetRet(), rsp.GetReward())
		}
	}

	// harvest raw too (owner "nhận thưởng cây")
	if data, err := rt(RBT_BONUS_RESULT_CMD, SERVER_RBT_BONUS_RESULT, rbtActivityBody(actID)); err != nil {
		t.Logf("217221 harvest: SILENT (%v)", err)
	} else {
		var rsp proto.RebornTrialGetBonusResultResp
		_ = pb.Unmarshal(data, &rsp)
		t.Logf("217221 harvest: ret=%d exp=%d reward=%q canReceive=%d", rsp.GetRet(), rsp.GetExp(), rsp.GetReward(), rsp.GetCanReceive())
	}

	// ── B) THƯỞNG CẢNH GIỚI: 307 rows via 21045-shape and per-row 21033 ──
	p.RbtMu.Lock()
	var ladder []int32
	got := map[int32]bool{}
	for _, c := range p.rbtCondCfgs {
		if c.GetType() == 307 {
			ladder = append(ladder, c.GetConditionId())
		}
	}
	for id, cd := range p.rbtCondData {
		got[id] = cd.GetIsGetReward()
	}
	p.RbtMu.Unlock()
	sort.Slice(ladder, func(i, j int) bool { return ladder[i] < ladder[j] })
	sample := ladder
	if len(sample) > 10 {
		sample = sample[:10] // smallest rungs — the reached ones
	}
	for _, cid := range sample {
		if got[cid] {
			t.Logf("307 rung %d: already claimed (skip)", cid)
			continue
		}
		b, _ := pb.Marshal(&proto.ReceiveCrossUnionRechargeRewardReq{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(cid)})
		if data, err := rt(ACTIVITY_CROSS_RECHARGE_CMD, SERVER_ACTIVITY_CROSS_RECHARGE, b); err != nil {
			t.Logf("21045 rung %d: SILENT (%v)", cid, err)
		} else {
			var rsp proto.RspGetActivityConditionReward
			_ = pb.Unmarshal(data, &rsp)
			t.Logf("21045 rung %d: ret=%d rewards=%q", cid, rsp.GetRet(), rsp.GetRewards())
		}
		b2, _ := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(cid)})
		if data, err := rt(ACTIVITY_TOKEN_REWARD_CMD, SERVER_ACTIVITY_TOKEN_REWARD, b2); err != nil {
			t.Logf("21033 rung %d: SILENT (%v)", cid, err)
		} else {
			var rsp proto.RspGetActivityConditionReward
			_ = pb.Unmarshal(data, &rsp)
			t.Logf("21033 rung %d: ret=%d rewards=%q", cid, rsp.GetRet(), rsp.GetRewards())
		}
	}

	// ── B2) cảnh giới rows addressed by PARAM (the level number) instead
	// of the row id — the panel might key its claim buttons on the param ──
	p.RbtMu.Lock()
	var params []int32
	for _, c := range p.rbtCondCfgs {
		if c.GetType() == 307 && len(c.GetParam()) > 0 {
			n, _ := strconv.Atoi(c.GetParam()[0])
			params = append(params, int32(n))
		}
	}
	p.RbtMu.Unlock()
	sort.Slice(params, func(i, j int) bool { return params[i] < params[j] })
	if len(params) > 8 {
		params = params[:8]
	}
	for _, prm := range params {
		b, _ := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(prm)})
		if data, err := rt(ACTIVITY_TOKEN_REWARD_CMD, SERVER_ACTIVITY_TOKEN_REWARD, b); err != nil {
			t.Logf("21033 param %d: SILENT (%v)", prm, err)
		} else {
			var rsp proto.RspGetActivityConditionReward
			_ = pb.Unmarshal(data, &rsp)
			t.Logf("21033 param %d: ret=%d rewards=%q", prm, rsp.GetRet(), rsp.GetRewards())
		}
	}
	// one rung via 21004 with the param too, for completeness (expected dead)
	if len(params) > 0 {
		b, _ := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(params[0])})
		if data, err := rt(ACTIVITY_COND_REWARD_CMD, SERVER_ACTIVITY_COND_REWARD, b); err != nil {
			t.Logf("21004 param %d: SILENT (%v) — expected", params[0], err)
		} else {
			var rsp proto.RspGetActivityConditionReward
			_ = pb.Unmarshal(data, &rsp)
			t.Logf("21004 param %d: ret=%d rewards=%q", params[0], rsp.GetRet(), rsp.GetRewards())
		}
	}

	// ── C) opcode scan 217210–217219 with raw tap ──
	for hexCmd := 0x0003507a; hexCmd <= 0x00035083; hexCmd++ {
		cmd := strconv.FormatInt(int64(hexCmd), 16)
		tapMu.Lock()
		tap = nil
		tapMu.Unlock()
		p.writeChan <- pack.Pack(cmd, rbtActivityBody(actID), p.UID)
		time.Sleep(2500 * time.Millisecond)
		tapMu.Lock()
		for _, r := range tap {
			// ignore global chatter that pushes anyway
			if r.msgType == "040c" || r.msgType == "03e9" {
				continue
			}
			t.Logf("SCAN %s → answered %s: %x", cmd, r.msgType, r.data)
		}
		tapMu.Unlock()
	}

	// re-fold: did anything flip to claimed?
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if _, err := rt(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err == nil {
			p.RbtMu.Lock()
			claimed := 0
			for _, cd := range p.rbtCondData {
				if cd.GetIsGetReward() {
					claimed++
				}
			}
			p.RbtMu.Unlock()
			t.Logf("re-fold claimed rows: %d (was 6/98 baseline-ish)", claimed)
		}
	}

	for {
		select {
		case n := <-p.NotiChan:
			t.Logf("NOTIFY: %s", n.MSG)
		default:
			return
		}
	}
}
