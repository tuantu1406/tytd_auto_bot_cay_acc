//go:build live

package tytd_go

// FULL-ENGINE live test for Thương Minh Yêu Vực (CelestialSeaWar 349) —
// owner 2026-11-09 "làm auto hết như bí cảnh luân hồi, dùng acc sonbinhsb
// server 710547" + "chỉ auto chạy free, thêm nút bấm dùng item, rèn đồ ưu
// tiên sát thương cuối".
//
// Drives the REAL engine on a live session (tiencanh_engine_live_test
// harness): real Player + armed Player-field gate (AutoSeaWar=true,
// Player-object law), one 21003 pull folds the forge/region config through
// the production swMergeConfig, then three production swPlayTick passes run
// every frame through swHandleFrame seats. HARD bans asserted on the wire:
// USE_ITEM (low16 95aa) and the guild-war family 234931..234944
// (0x95b3..0x95c0) must NEVER leave the socket — item use is button-only
// [[tytd-item-ban]], war is ORDER-ONLY.
//
//	TYTD_LIVE=1 TYTD_SEAWAR=1 go test -tags "sqlcipher live" -count=1 -run TestSeaWarEngineLive -v -timeout 420s .

import (
	"context"
	"os"
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

func TestSeaWarEngineLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_SEAWAR") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_SEAWAR=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710547
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
	db, _ := gorm.Open(sqlite.Open("file:swlive?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256),
		AutoSeaWar: true} // Player-object gate (owner 2026-10-27)

	// ── ban bookkeeping: every outbound frame low16 is recorded + checked ──
	var sendMu sync.Mutex
	sentTypes := map[string]int{}
	banned := func(ty string) bool {
		if ty == "95aa" { // 234922 USE_ITEM — button-only
			return true
		}
		if len(ty) == 4 {
			if n, err := strconv.ParseInt(ty, 16, 32); err == nil && n >= 0x95b3 && n <= 0x95c0 {
				return true // 234931..234944 guild war (ORDER-ONLY)
			}
		}
		return false
	}
	recvTypes := map[string]int{}
	var recvMu sync.Mutex
	var bannedSeen []string
	go func() {
		for f := range p.writeChan {
			_, ty, _ := pack.Unpack(f)
			sendMu.Lock()
			sentTypes[ty]++
			if banned(ty) {
				bannedSeen = append(bannedSeen, ty)
			}
			sendMu.Unlock()
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
			recvMu.Lock()
			recvTypes[msgType]++
			recvMu.Unlock()
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.ActList.mergeDetail(p.Username, realData)
				p.swMergeConfig(realData)
				p.stDeliver(msgType, realData)
			case SERVER_ACTIVITY_BUY_GOODS:
				p.stDeliver(msgType, realData)
			case SERVER_SW_ENTER, SERVER_SW_ENTER_GAME, SERVER_SW_EXPLORE,
				SERVER_SW_HANDLE_EVENT, SERVER_SW_UNION_WELFARE, SERVER_SW_UNION_WELFARE_REWARD,
				SERVER_SW_UNION_RECEIVE_LIQUID, SERVER_SW_HEART_BEAT, SERVER_SW_NEXT_REGION,
				SERVER_SW_USE_ITEM, SERVER_SW_WORSHIP,
				SERVER_SW_ASSIST_LIST, SERVER_SW_ASSIST, SERVER_SW_ASSIST_REWARD,
				SERVER_SW_ASSIST_SYNC, SERVER_SW_ASSIST_SYNC_RAW:
				p.swHandleFrame(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-readerDone }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// ── wait for the live 349 window ──
	deadline := time.Now().Add(30 * time.Second)
	var actID int32
	for time.Now().Before(deadline) {
		now := time.Now()
		for _, e := range p.ActList.byType(swActivityType) {
			if st := e.status(now); st == "active" || st == "reward" {
				actID = e.ActivityID
			}
		}
		if actID != 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if actID == 0 {
		t.Fatal("no active type-349 activity (owner said it IS open on 710547)")
	}
	t.Logf("live 349 activityId=%d", actID)

	// Fold config FIRST (energy item / soulBall / region) like the hourly pull.
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if data, err := p.stRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body, 15*time.Second); err == nil {
			p.swMergeConfig(data)
		} else {
			t.Logf("21003 detail pull failed (%v) — engine continues blind-safe", err)
		}
	}
	p.SwMu.Lock()
	t.Logf("config folded: energyItem=%d balls=%d regions=%d",
		p.swEnergyItem, len(p.swBallEffect), len(p.swRegionEnergy))
	for _, mt := range p.swMallRows {
		t.Logf("mall row %d %q price=%q param=%q limit=%d type=%d bought=%d",
			mt.GetId(), mt.GetName(), mt.GetPrice(), mt.GetParam(),
			mt.GetBuyLimit(), mt.GetBuyLimitType(), p.swMallBought[mt.GetId()])
	}
	p.SwMu.Unlock()

	// ── three production ticks ──
	for i := 1; i <= 3; i++ {
		swPlayTick(p, actID)
		p.SwMu.Lock()
		worshipDay, exhKey := p.swWorshipDay, p.swExhaustKey
		p.SwMu.Unlock()
		t.Logf("tick %d done (worshipDay=%q exhaustKey=%q)", i, worshipDay, exhKey)
		time.Sleep(2 * time.Second)
	}

	// ── final board readout: ENTER must be accepted while open ──
	enter, err := p.swEnter(actID)
	if err != nil {
		t.Fatalf("post-tick ENTER err=%v", err)
	}
	t.Logf("post-tick ENTER ret=%d canWorship=%v region=%d", enter.GetRet(), enter.GetCanWorship(), enter.GetRegionId())
	if enter.GetRet() != 0 {
		t.Errorf("ENTER refused while activity open (ret %d) — engine would idle-loop", enter.GetRet())
	}

	sendMu.Lock()
	defer sendMu.Unlock()
	t.Logf("sent frame types: %v", sentTypes)
	recvMu.Lock()
	t.Logf("recv frame types: %v", recvTypes)
	recvMu.Unlock()
	if len(bannedSeen) > 0 {
		t.Fatalf("BAN VIOLATION — forbidden frames left the socket: %v", bannedSeen)
	}
}
