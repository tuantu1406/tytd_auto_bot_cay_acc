//go:build live

package tytd_go

// One-shot live probe for the pending event that blocks the caravan road
// (reported: luyện đan / SMELT screen). Dumps the event exactly as the
// server sees it, then asks the server which actions it accepts: each
// option, then SKIP. READ + single attempts only — no loops, no items.
//
//	TYTD_LIVE=1 TYTD_RBT_STUCK=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialStuckLive -v -timeout 120s .

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

func TestRebornTrialStuckLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_STUCK") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_STUCK=1")
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
	db, _ := gorm.Open(sqlite.Open("file:rbtstuck?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid), AutoRebornTrial: true}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel, Notify: true,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 64),
		AutoRebornTrial: true} // Player-object gate (owner 2026-10-27)
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch ty {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			case SERVER_RBT_ENTER_ACTIVITY, SERVER_RBT_ENTER, SERVER_RBT_WORSHIP, SERVER_RBT_EXPLORE,
				SERVER_RBT_SELECT_OPTION, SERVER_RBT_SKIP_EVENT, SERVER_RBT_BONUS, SERVER_RBT_BONUS_RESULT, SERVER_RBT_BONUS_ITEM, SERVER_RBT_SEND_BONUS_ITEM,
				SERVER_RBT_GUESS_DATA, SERVER_RBT_GUESS_REWARD, SERVER_RBT_AUTO_EXPLORE, SERVER_RBT_USE_MAGIC:
				p.rbtHandleFrame(ty, d)
			case SERVER_ACTIVITY_BUY_GOODS, SERVER_ACTIVITY_COND_REWARD, SERVER_ACTIVITY_CROSS_RECHARGE, SERVER_ACTIVITY_TOKEN_REWARD:
				p.rbtHandleFrame(ty, d)
			case "03eb":
				p.rbtMergeConfig(d)
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-done }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(30 * time.Second)
	var actID int32
	for time.Now().Before(deadline) && actID == 0 {
		actID, _, _ = p.rbtActiveActivity()
		time.Sleep(500 * time.Millisecond)
	}
	if actID == 0 {
		t.Fatal("no active 172")
	}
	en, err := p.rbtEnter(actID)
	if err != nil || en.GetRet() != 0 {
		t.Fatalf("ENTER ret %v err %v", en.GetRet(), err)
	}
	pd := en.GetPlayerData()
	t.Logf("STATE: level=%d exp=%d totalExp=%d hp=%d map=%d", pd.GetLevel(), pd.GetExp(), pd.GetTotalExp(), pd.GetHp(), pd.GetMap())
	for _, m := range pd.GetMagicItem() {
		t.Logf("MAGIC: type=%d lv=%d cd=%d", m.GetMagicType(), m.GetMagicLevel(), m.GetCd())
	}
	ev := pd.GetEvent()
	if ev == nil {
		t.Logf("no pending event right now (hp=%d) — exploring once to sample the next node", pd.GetHp())
		xp, xerr := p.rbtExplore(actID)
		t.Logf("EXPLORE ret=%d err=%v", xp.GetRet(), xerr)
		if xp.GetPlayerData() != nil {
			pd = xp.GetPlayerData()
			ev = pd.GetEvent()
		}
	}
	if ev == nil {
		t.Fatal("still no event after explore")
	}
	t.Logf("EVENT: type=%d stat=%d npcId=%d value=%d param1=%d param2=%d rewards=%q item=%v",
		ev.GetType(), ev.GetStat(), ev.GetNpcId(), ev.GetValue(), ev.GetParam1(), ev.GetParam2(),
		ev.GetRewards(), ev.GetItem())
	for _, o := range ev.GetOption() {
		t.Logf("  OPTION order=%d costExp=%d reduceValue=%d success=%d", o.GetOrder(), o.GetCostExp(), o.GetReduceValue(), o.GetSuccess())
	}
	for _, o := range ev.GetOption() {
		rsp, err := p.rbtSelectOption(actID, o.GetOrder())
		t.Logf("SELECT order=%d → ret=%d newEventStat=%v exp=%d", o.GetOrder(), rsp.GetRet(), rsp.GetEvent().GetStat(), rsp.GetExp())
		if err == nil && rsp.GetRet() == 0 && rsp.GetEvent() != nil && rsp.GetEvent().GetStat() != 0 {
			t.Log("resolved by options")
			return
		}
	}
	sk, err := p.rbtSkipEvent(actID)
	t.Logf("SKIP → ret=%d eventStat=%v err=%v", sk.GetRet(), sk.GetEvent().GetStat(), err)
}
