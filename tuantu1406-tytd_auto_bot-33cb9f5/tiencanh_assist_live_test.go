//go:build live

package tytd_go

// ASSIST (hỗ trợ) live probe for Tiên Cảnh Vị Ương — owner 2026-11-13
// "khi đánh quái không chết, hệ thống sẽ tự gọi hỗ trợ. thêm poll check có ai
// cần hỗ trợ thì hỗ trợ".
//
// Reads the assist board (ASSIST_LOG 218608) and reports EXACTLY what the
// server lists: trapped union members (need a free ASSIST 218607 rescue) and
// monsters whose attacker left alive (the system auto-called for help — those
// are helped by ATTACKING them, which wySweepMonsters already does; the client
// JS never sends ASSIST's monsterId, always null). Then runs the real
// wyAssistCycle and reads the verdict. We cannot force a mate to be trapped,
// so an empty trap list ⇒ clean no-op still proves the poll transport + gate.
//
//	TYTD_LIVE=1 go test -tags "sqlcipher live" -count=1 -run TestTienCanhAssistLive -v -timeout 240s .

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

func TestTienCanhAssistLive(t *testing.T) {
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
	db, _ := gorm.Open(sqlite.Open("file:tcassist?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256)}

	var sendMu sync.Mutex
	sentTypes := map[string]int{}
	var bannedSeen []string
	banned := func(ty string) bool {
		n, _ := strconv.ParseInt(ty, 16, 32)
		return n == 0x55ed || n == 0x55f8 || (n >= 0x5606 && n <= 0x560b)
	}
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
	sent := func(ty string) int {
		sendMu.Lock()
		defer sendMu.Unlock()
		return sentTypes[ty]
	}

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
				p.wyMergeConfig(realData)
				p.stDeliver(msgType, realData)
			case SERVER_WE_YOUND_ASSIST, SERVER_WE_YOUND_ASSIST_LOG:
				p.wyHandleFrame(msgType, realData)
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
		for _, e := range p.ActList.byType(wyActivityType) {
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
		t.Fatal("no active type-186 activity")
	}
	t.Logf("live 186 activityId=%d uid=%d", actID, uid)

	// 1. read the board directly — what does the server actually list?
	lg, err := p.wyAssistLog(actID)
	if err != nil {
		t.Fatalf("ASSIST_LOG 218608 err=%v", err)
	}
	t.Logf("ASSIST_LOG ret=%d assistNum=%d trapped=%d monstersNeedingHelp=%d",
		lg.GetRet(), lg.GetAssistNum(), len(lg.GetTrapInfo()), len(lg.GetMonsterInfo()))
	for _, m := range lg.GetTrapInfo() {
		ap := m.GetAppearance()
		t.Logf("  trapped: pid=%d name=%q floor=%d trapEnd=%d seek=%v",
			ap.GetPlayerId(), ap.GetNickName(), m.GetFloor(), m.GetTrapEndTime(), m.GetTrapSeek())
	}
	for _, m := range lg.GetMonsterInfo() {
		t.Logf("  help-me monster: id=%d blood=%s/%s explorePid=%d defeat=%v",
			m.GetId(), m.GetRemainBloods(), m.GetMaxBloods(), m.GetExplorePlayerId(), m.GetDefeat())
	}

	// 2. run the REAL production assist cycle. Trap-rescue only fires when a
	// mate is genuinely trapped; otherwise it's the honest no-op.
	before := sent("55ef") // ASSIST low16
	p.wyAssistCycle(actID)
	after := sent("55ef")
	t.Logf("wyAssistCycle: ASSIST sent %d→%d (%d trapped listed)", before, after, len(lg.GetTrapInfo()))
	if len(lg.GetTrapInfo()) > 0 && after == before {
		t.Errorf("trapped mates listed but no ASSIST sent")
	}

	sendMu.Lock()
	keys := make([]string, 0, len(sentTypes))
	for k, v := range sentTypes {
		keys = append(keys, k+"×"+strconv.Itoa(v))
	}
	bs := append([]string(nil), bannedSeen...)
	sendMu.Unlock()
	t.Logf("outbound: %v", keys)
	if len(bs) > 0 {
		t.Errorf("ITEM BAN VIOLATION: %v", bs)
	}
}
