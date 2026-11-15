//go:build live

package tytd_go

// Read-only watcher: polls ComposeBall ENTER and dumps the FULL sync state
// (per-ball id/config/position/velocity/scale + per-config drop ledger) on
// every poll. When the real client plays on this account, server-approved
// merges appear here verbatim — that is the merge format ground truth.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveComposeBallWatch -v -timeout 900s .

import (
	"context"
	"os"
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

func TestLiveComposeBallWatch(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1")
	}
	var (
		liveUser   = liveUserEnv(t)
		livePass   = livePassEnv(t)
		liveServer = 710564
	)
	passportToken, err := request.LoginGPassport(liveUser, livePass)
	if err != nil {
		t.Fatal(err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatal(err)
	}
	gameURL, err := request.GetServerURL(userID, liveServer)
	if err != nil {
		t.Fatal(err)
	}
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
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
	db, _ := gorm.Open(sqlite.Open("file:cb_watch_live?mode=memory&cache=shared"), &gorm.Config{})
	if err := db.AutoMigrate(&model.User{}, &model.SystemConfig{}); err != nil {
		t.Fatal(err)
	}
	p := &Player{
		Username: liveUser, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan:  make(chan NotifyMGS, 64),
		writeChan: make(chan []byte, 64),
	}
	go func() {
		for frame := range p.writeChan {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	rdone := make(chan struct{})
	go func() {
		defer close(rdone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, t, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch t {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, t == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.cbMergeConfig(d)
			case SERVER_COMPOSE_BALL_ENTER, SERVER_COMPOSE_BALL_COMPOSE,
				SERVER_COMPOSE_BALL_USE_ITEM, SERVER_COMPOSE_BALL_HP_ITEM,
				SERVER_COMPOSE_BALL_SETTLE:
				p.cbDeliver(t, d)
			}
		}
	}()
	defer func() { conn.Close(); <-rdone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(20 * time.Second)
	actID := int32(0)
	for actID == 0 && time.Now().Before(deadline) {
		actID = p.ComposeBallActivityID()
		if actID == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if actID == 0 {
		t.Skip("window closed")
	}
	dump := func(who string) {
		e, err := p.RequestComposeBallEnterGame(15 * time.Second)
		if err != nil {
			t.Logf("%s: ENTER transport %v", who, err)
			return
		}
		sd := e.GetSyncData()
		if sd == nil {
			t.Logf("%s: ENTER ret=%d, no sync", who, e.GetRet())
			return
		}
		t.Logf("%s: ret=%d gameOver=%v id=%d cfg=%d hp=%d cost=%d score=%d max=%d balls=%d", who,
			e.GetRet(), sd.GetGameOver(), sd.GetId(), sd.GetConfigId(),
			sd.GetSyncData().GetHp(), sd.GetSyncData().GetCostHp(),
			sd.GetSyncData().GetScore(), sd.GetSyncData().GetMaxScore(), len(sd.GetBalls()))
		for _, c := range sd.GetCount() {
			t.Logf("%s:   ledger cfg=%d count=%d", who, c.GetConfigId(), c.GetCount())
		}
		for _, b := range sd.GetBalls() {
			t.Logf("%s:   BALL id=%d cfg=%d pos=%q vel=%q scale=%q", who, b.GetId(), b.GetConfigId(), b.GetPosition(), b.GetLinearVelocity(), b.GetScale())
		}
	}
	dump("t0")
	end := time.Now().Add(8 * time.Minute)
	for i := 1; time.Now().Before(end); i++ {
		time.Sleep(30 * time.Second)
		dump("poll")
	}
}
