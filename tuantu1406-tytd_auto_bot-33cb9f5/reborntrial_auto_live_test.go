//go:build live

package tytd_go

// LIVE smoke for the auto Luân Hồi Bí Cảnh loop (never sends items).
// Runs the real rebornTrialAutoLoop against the test account for a few
// seconds and prints what the engine sees: EnterActivity fields parsed
// from live bytes (unionId/selfRank — schema proof), the map refuse code,
// and the loop's own state log. Outside the 19h–22h window the expected
// outcome is exactly: gate parsed OK, ENTER refused 15001, loop waits.
//
//	TYTD_LIVE=1 TYTD_RBT_AUTO=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialAutoLive -v -timeout 120s .

import (
	"context"
	"encoding/hex"
	"fmt"
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

func TestRebornTrialAutoLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_AUTO") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_AUTO=1")
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
	db, _ := gorm.Open(sqlite.Open("file:rbtauto?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid), AutoRebornTrial: true}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel, Notify: true,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 64),
		rbtPoll: 3 * time.Second, AutoRebornTrial: true} // Player-object gate (owner 2026-10-27)
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	all := make(chan [2]string, 8192)
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
				SERVER_RBT_GUESS_DATA, SERVER_RBT_GUESS_REWARD, SERVER_RBT_AUTO_EXPLORE:
				p.rbtHandleFrame(ty, d)
			case SERVER_ACTIVITY_BUY_GOODS, SERVER_ACTIVITY_COND_REWARD, SERVER_ACTIVITY_CROSS_RECHARGE, SERVER_ACTIVITY_TOKEN_REWARD:
				p.rbtHandleFrame(ty, d)
			case "03eb":
				p.rbtMergeConfig(d)
				p.stDeliver("03eb", d)
			}
			select {
			case all <- [2]string{ty, hex.EncodeToString(d)}:
			default:
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-done }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	rbtLive := func() bool {
		now := time.Now()
		for _, e := range p.ActList.byType(rbtActivityType) {
			if e.status(now) == "active" {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !rbtLive() {
		time.Sleep(500 * time.Millisecond)
	}
	if !rbtLive() {
		t.Skip("type 172 not active in the list")
	}
	// The union tree is open ALL day: exercise the real tree cycle first.
	if actID, _, ok := p.rbtActiveActivity(); ok {
		p.rbtTreeCycle(actID)
		time.Sleep(2 * time.Second)
	}

	// Direct one-shot tick: outside the daily window this degenerates to
	// EnterActivity + a refused ENTER — exactly the schema proof we want.
	if actID, beginMs, ok := p.rbtActiveActivity(); ok {
		_, round, _ := rbtPhase(beginMs, p.rbtCfg(), time.Now())
		t.Logf("direct tick (round %d)…", round)
		p.rbtPlayTick(actID, round)
		time.Sleep(2 * time.Second)
	}

	go p.rebornTrialAutoLoopAfter(2 * time.Second)

	// Observe ~75 s of loop behaviour (first iteration includes the shop
	// refresh + buys), then report.
	end := time.Now().Add(75 * time.Second)
	seen := map[string]int{}
	for time.Now().Before(end) {
		select {
		case a := <-all:
			n, _ := strconv.ParseInt(a[0], 16, 32)
			if n >= 0x4330 && n <= 0x4363 {
				seen[a[0]]++
				if a[0] == "4330" && len(a[1]) > 0 {
					var rsp proto.RebornTrialEnterActivityResp
					if raw, err := hex.DecodeString(a[1]); err == nil && pb.Unmarshal(raw, &rsp) == nil {
						t.Logf("EnterActivity parsed: ret=%d selfRank=%d round=%d group=%d unionId=%d position=%d",
							rsp.GetRet(), rsp.GetSelfRank(), rsp.GetRound(), rsp.GetGroup(), rsp.GetUnionId(), rsp.GetPosition())
					}
				}
			}
		case n := <-p.NotiChan:
			t.Logf("NOTIFY: %s", n.MSG)
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	fmt.Println("observed answer types:", seen)
}
