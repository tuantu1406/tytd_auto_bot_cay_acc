//go:build live

package tytd_go

// LIVE PROBE 4 (owner 2026-11-07): "chưa thấy nhận cam lộ … nút nhận nhanh,
// nhận nhanh là nhận tất cả cam lộ, cứ spam nút đó là được". Client-JS mine
// found the button: onClickReceiveAll → reqWaterMsg({id:0}) — the SAME
// 217222 we tried — but the client goes through protobufjs, which encodes
// explicitly-set defaults: its wire body carries tag 2 = 0 (bytes 10 00)
// while our Go proto3 marshal of Id:0 DROPS the field. Hypothesis: the
// server claims all when id is PRESENT and zero, and ignores the request
// entirely when id is absent (that's why probe-2's id=0 was silent).
//
// This probe fires rbtBonusItemAllBody (the exact shipping bytes) once and
// re-reads the 217220 board: mass status 0→1 flips (or canReceive→0)
// prove claim-all; a lingering status==0 set then justifies the per-id
// belt in rbtTreeCycle.
//
//	 TYTD_LIVE=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialWaterAllLive -v -timeout 600s .

import (
	"context"
	"os"
	"strconv"
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

func TestRebornTrialWaterAllLive(t *testing.T) {
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
	db, _ := gorm.Open(sqlite.Open("file:rbtwaterall?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid)}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 256), writeChan: make(chan []byte, 256)}

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
	dump := func(tag string) (int, int) {
		data, err := rt(RBT_BONUS_CMD, SERVER_RBT_BONUS, rbtActivityBody(actID))
		if err != nil {
			t.Logf("board[%s]: err %v", tag, err)
			return -1, -1
		}
		var rsp proto.RebornTrialBonusResp
		if pb.Unmarshal(data, &rsp) != nil {
			t.Logf("board[%s]: undecodable", tag)
			return -1, -1
		}
		ready, total := 0, 0
		for _, in := range rsp.GetInfo() {
			total++
			if in.GetStatus() == 0 {
				ready++
			}
		}
		t.Logf("board[%s]: ret=%d canReceive=%d exp=%d total=%d ready=%d", tag, rsp.GetRet(), rsp.GetCanReceive(), rsp.GetExp(), total, ready)
		return ready, total
	}

	ready0, _ := dump("before")

	// the shipping claim-all: rbtBonusItemAllBody's exact bytes
	wi, err := p.rbtClaimBonusAll(actID)
	if err != nil {
		t.Fatalf("claim-all (forced id=0): %v — hypothesis WRONG, keep per-id loop", err)
	}
	t.Logf("claim-all forced id=0: ret=%d reward=%q canReceive=%d", wi.GetRet(), wi.GetReward(), wi.GetCanReceive())

	ready1, _ := dump("after")
	if ready0 >= 0 && ready1 >= 0 && ready1 < ready0 {
		t.Logf("VERDICT: claim-all WORKS (%d → %d ready)", ready0, ready1)
	} else if ready0 == ready1 && ready0 > 0 {
		t.Logf("VERDICT: claim-all answered but ready set UNCHANGED — per-id belt required")
	}

	// second press on an (almost) empty board: the spam-idiom the owner
	// described — must answer, not go silent
	if wi2, err := p.rbtClaimBonusAll(actID); err != nil {
		t.Logf("claim-all press 2: %v", err)
	} else {
		t.Logf("claim-all press 2: ret=%d reward=%q", wi2.GetRet(), wi2.GetReward())
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
