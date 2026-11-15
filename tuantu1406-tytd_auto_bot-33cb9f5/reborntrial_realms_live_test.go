//go:build live

package tytd_go

// LIVE PROBE 3 (owner 2026-11-07): "thưởng cảnh giới trong bảng 'chi tiết
// cảnh giới' chưa thấy nhận". Client-JS mine answered the shape: the panel
// is CommonAchievementView with getAllRewardStatus()=true, so BOTH its
// buttons ride ActivityMgr.reqGetActRewardByArr →
// Protocol.S_ACTIVITY_GET_CONDITION_REWARD_BY_ARR = 21022 (0000521e), body
// ReqGetActivityConditionRewardByArr{activityDataList:[{activityId,
// conditionId}]} — one batched call for every CanGet row of the ladder
// (type 307 = ACHIEVE_LV_REWARD_TYPE, LV_ACHI_EXTRA_TYPE "4"). 21033/21045/
// 21004 were all wrong doors for this panel. Answer rides resp=req−20000
// ⇒ 1022 = "03fe" (confirmed here, not assumed).
//
// Probe on sonbinhsb: fold config, collect every unclaimed type-307 rung,
// fire ONE 21022 batch, re-fold: isGetReward flipping false→true with
// ret=0+rewards proves the engine path. Same batch shape is then wired
// into rbtCondSweep for the ladder.
//
// FIRST RUN (2026-11-07, sonbinhsb@710564): 41 unclaimed rungs → ONE batch
// answered ret=203 rewards="" on 03fe (type CONFIRMED, not assumed), yet
// the re-fold showed 7 rungs flipped to claimed and seven 03ef condition
// syncs landed ⇒ the server claims rungs in order and 203 = stopped at the
// first not-yet-reached rung. The per-row pass below reads ret per rung.
//
//	 TYTD_LIVE=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialRealmsClaimLive -v -timeout 600s .

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

func TestRebornTrialRealmsClaimLive(t *testing.T) {
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
	db, _ := gorm.Open(sqlite.Open("file:rbtrealms?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid)}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 256), writeChan: make(chan []byte, 256)}

	// raw tap so a surprise answer type still gets recorded
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
				SERVER_ACTIVITY_CROSS_RECHARGE, SERVER_ACTIVITY_TOKEN_REWARD,
				SERVER_ACTIVITY_COND_REWARD_ARR:
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

	rt := func(cmd, want string, body []byte) ([]byte, error) {
		return p.stRoundTrip(cmd, want, body, 6*time.Second)
	}
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if _, err := rt(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err != nil {
			t.Logf("21003 pull err=%v", err)
		}
	}

	// collect unclaimed type-307 rungs (the "chi tiết cảnh giới" ladder)
	p.RbtMu.Lock()
	var rungs []int32
	for _, c := range p.rbtCondCfgs {
		if c.GetType() == 307 {
			rungs = append(rungs, c.GetConditionId())
		}
	}
	claimedBefore := map[int32]bool{}
	for id, cd := range p.rbtCondData {
		claimedBefore[id] = cd.GetIsGetReward()
	}
	p.RbtMu.Unlock()
	sort.Slice(rungs, func(i, j int) bool { return rungs[i] < rungs[j] })
	var open []int32
	for _, r := range rungs {
		if !claimedBefore[r] {
			open = append(open, r)
		}
	}
	t.Logf("307 ladder: %d rungs, %d unclaimed", len(rungs), len(open))
	if len(open) == 0 {
		t.Log("ladder fully claimed — probe still fires the batch to read ret (expect non-zero)")
	}

	// ── ONE batched 21022, exactly like the client's panel button ──
	refs := make([]*proto.AcitivityConditionDataArr, 0, len(open))
	for _, r := range open {
		refs = append(refs, &proto.AcitivityConditionDataArr{
			ActivityId: pb.Int32(actID), ConditionId: pb.Int32(r)})
	}
	tapMu.Lock()
	tap = nil
	tapMu.Unlock()
	b, err := pb.Marshal(&proto.ReqGetActivityConditionRewardByArr{ActivityDataList: refs})
	if err != nil {
		t.Fatal(err)
	}
	data, err := rt(ACTIVITY_COND_REWARD_ARR_CMD, SERVER_ACTIVITY_COND_REWARD_ARR, b)
	if err != nil {
		t.Logf("21022 batch(%d): SILENT (%v) — check tap for surprise answer type", len(refs), err)
	} else {
		var rsp proto.RspGetActivityConditionReward
		_ = pb.Unmarshal(data, &rsp)
		t.Logf("21022 batch(%d): ret=%d rewards=%q", len(refs), rsp.GetRet(), rsp.GetRewards())
	}
	tapMu.Lock()
	for _, r := range tap {
		if r.msgType == "040c" || r.msgType == "03e9" {
			continue
		}
		t.Logf("tap after 21022: %s → %x", r.msgType, r.data)
	}
	tapMu.Unlock()

	// empty batch control: proves the server actually reads the list
	if b0, err := pb.Marshal(&proto.ReqGetActivityConditionRewardByArr{}); err == nil {
		if data, err := rt(ACTIVITY_COND_REWARD_ARR_CMD, SERVER_ACTIVITY_COND_REWARD_ARR, b0); err != nil {
			t.Logf("21022 empty: SILENT (%v)", err)
		} else {
			var rsp proto.RspGetActivityConditionReward
			_ = pb.Unmarshal(data, &rsp)
			t.Logf("21022 empty: ret=%d rewards=%q", rsp.GetRet(), rsp.GetRewards())
		}
	}

	// per-row batches: the batch stopped at the first not-ready rung
	// (ret=203 after 03ef flips ×7). One row per call shows the ret
	// per rung and whether rewards strings come back on ret=0 rows.
	p.RbtMu.Lock()
	still := map[int32]bool{}
	for id, cd := range p.rbtCondData {
		still[id] = cd.GetIsGetReward()
	}
	p.RbtMu.Unlock()
	fired := 0
	for _, r := range open {
		if still[r] || fired >= 14 {
			continue
		}
		fired++
		b1, _ := pb.Marshal(&proto.ReqGetActivityConditionRewardByArr{
			ActivityDataList: []*proto.AcitivityConditionDataArr{
				{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(r)}}})
		if data, err := rt(ACTIVITY_COND_REWARD_ARR_CMD, SERVER_ACTIVITY_COND_REWARD_ARR, b1); err != nil {
			t.Logf("21022 row %d: SILENT (%v)", r, err)
			continue
		} else {
			var rsp proto.RspGetActivityConditionReward
			_ = pb.Unmarshal(data, &rsp)
			t.Logf("21022 row %d: ret=%d rewards=%q", r, rsp.GetRet(), rsp.GetRewards())
		}
	}

	// re-fold: did the ladder flip to claimed?
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if _, err := rt(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err == nil {
			p.RbtMu.Lock()
			nowClaimed := 0
			for _, r := range rungs {
				if cd := p.rbtCondData[r]; cd != nil && cd.GetIsGetReward() {
					nowClaimed++
				}
			}
			p.RbtMu.Unlock()
			t.Logf("307 claimed now %d/%d (was %d/%d)", nowClaimed, len(rungs), len(rungs)-len(open), len(rungs))
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
