//go:build live

package tytd_go

// Read-only live state peek: ENTER and print level/exp/hp/pending event.
//	TYTD_LIVE=1 TYTD_RBT_STATE=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialStateLive -v -timeout 90s .

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

func TestRebornTrialStateLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_STATE") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_STATE=1")
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
	db, _ := gorm.Open(sqlite.Open("file:rbtstate?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid)}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 64)}
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
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-done }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)
	deadline := time.Now().Add(25 * time.Second)
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
		t.Fatalf("ENTER ret=%d err=%v", en.GetRet(), err)
	}
	pd := en.GetPlayerData()
	t.Logf("STATE level=%d exp=%d hp=%d map=%d", pd.GetLevel(), pd.GetExp(), pd.GetHp(), pd.GetMap())
	if ev := pd.GetEvent(); ev != nil && ev.GetStat() == 0 {
		t.Logf("PENDING EVENT type=%d value=%d options=%v", ev.GetType(), ev.GetValue(), func() []int64 {
			c := []int64{}
			for _, o := range ev.GetOption() {
				c = append(c, int64(o.GetOrder())*1000000+o.GetCostExp())
			}
			return c
		}())
	} else {
		t.Log("no pending event")
	}
}
