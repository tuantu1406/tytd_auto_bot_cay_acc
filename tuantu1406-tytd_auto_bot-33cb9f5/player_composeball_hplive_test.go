//go:build live

package tytd_go

// Live probe (user correction 2026-10-04: "one drop always 1 hp cost"):
// measures what the SERVER actually charges per accepted compose and whether
// it enforces hp >= costHp. ENTER → read (hp, costHp) → ONE plain honest
// drop → read again. Three outcomes:
//   hp −1, accepted        → user right: drops cost 1 hp, costHp ≠ price
//   hp −costHp, accepted   → costHp is the price (escalating)
//   refused at hp<costHp   → server gates on costHp regardless
// NEVER settles, never uses items.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveComposeBallHpCost -v -timeout 600s .

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

func TestLiveComposeBallHpCost(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1")
	}
	var (
		liveHpUser   = liveUserEnv(t)
		liveHpPass   = livePassEnv(t)
		liveHpServer = 710564
	)
	passportToken, err := request.LoginGPassport(liveHpUser, liveHpPass)
	if err != nil {
		t.Fatal(err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatal(err)
	}
	gameURL, err := request.GetServerURL(userID, liveHpServer)
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
	db, _ := gorm.Open(sqlite.Open("file:cb_hp_live?mode=memory&cache=shared"), &gorm.Config{})
	if err := db.AutoMigrate(&model.User{}, &model.SystemConfig{}); err != nil {
		t.Fatal(err)
	}
	p := &Player{
		Username: liveHpUser, UID: uid, ChatID: int64(uid), Running: true,
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
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch ty {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.cbMergeConfig(d)
			case SERVER_COMPOSE_BALL_ENTER, SERVER_COMPOSE_BALL_COMPOSE,
				SERVER_COMPOSE_BALL_USE_ITEM, SERVER_COMPOSE_BALL_HP_ITEM,
				SERVER_COMPOSE_BALL_SETTLE:
				p.cbDeliver(ty, d)
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
	p.cbRequestConfig(actID)
	time.Sleep(1500 * time.Millisecond)

	snap := func(tag string) (hp, cost, score int32) {
		e, err := p.RequestComposeBallEnterGame(15 * time.Second)
		if err != nil {
			t.Fatalf("%s ENTER: %v", tag, err)
		}
		sd := e.GetSyncData()
		if sd == nil {
			t.Fatalf("%s ENTER ret=%d no sync", tag, e.GetRet())
		}
		u := sd.GetSyncData()
		t.Logf("%s: hp=%d costHp=%d score=%d balls=%d gameOver=%v", tag,
			u.GetHp(), u.GetCostHp(), u.GetScore(), len(sd.GetBalls()), sd.GetGameOver())
		p.cbApplySync(sd)
		return u.GetHp(), u.GetCostHp(), u.GetScore()
	}
	hp0, cost0, _ := snap("before")

	// ONE honest drop (planner, so the format is the proven one).
	p.CbMu.Lock()
	balls, composes, ok := p.cbPlanLocked()
	p.CbMu.Unlock()
	if !ok {
		t.Fatal("planner refused on a board with room")
	}
	t.Logf("uploading 1 drop id=%d cfg=%s composes=%d", balls[len(balls)-1].GetId(), balls[len(balls)-1].GetPosition(), len(composes))
	cresp, err := p.RequestComposeBallCompose(balls, composes, 15*time.Second)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if ret := cresp.GetRet(); ret != 0 {
		t.Fatalf("VERDICT: drop at hp=%d (costHp=%d) REFUSED ret=%d — server gates compose on hp>=costHp", hp0, cost0, ret)
	}
	hp1, cost1, _ := snap("after")
	t.Logf("VERDICT: hp %d→%d (Δ=%d, costHp was %d, now %d) → drop cost %d hp",
		hp0, hp1, hp0-hp1, cost0, cost1, hp0-hp1)
}
