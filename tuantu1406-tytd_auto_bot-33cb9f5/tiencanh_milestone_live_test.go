//go:build live

package tytd_go

// MILESTONE (nạp bang) live probe for Tiên Cảnh Vị Ương — owner 2026-11-13
// "thêm auto nhận thưởng nạp bang". Runs the REAL wyCondSweep against the live
// activity and reads the verdicts:
//   1. How many ladder rows did 21003 fold for THIS activity, and how do the
//      nạp-bang (type 84 / condID≥20000) rows split from the type groups?
//   2. What does the server answer to the per-id 21045 and the blind per-type
//      21033 (ret / rewards string)?
//   3. No banned frame leaves the socket (USE_ITEM 0x55ed, AUTO_SWITCH 0x55f8,
//      PVP 0x5606..0x0b).
//
//	TYTD_LIVE=1 go test -tags "sqlcipher live" -count=1 -run TestTienCanhMilestoneLive -v -timeout 300s .

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

func TestTienCanhMilestoneLive(t *testing.T) {
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
	db, _ := gorm.Open(sqlite.Open("file:tcmile?mode=memory&cache=shared"), &gorm.Config{})
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
			case SERVER_ACTIVITY_TOKEN_REWARD, SERVER_ACTIVITY_CROSS_RECHARGE:
				// The two milestone answers ride the shared stChans seat —
				// wyRoundTrip(ACTIVITY_TOKEN_REWARD / _CROSS_RECHARGE) waits here.
				p.stDeliver(msgType, realData)
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

	body, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID})
	data, err := p.wyRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body)
	if err != nil {
		t.Fatal(err)
	}
	p.wyMergeConfig(data)

	// 1. how many ladder rows folded, and their split.
	p.WyMu.Lock()
	rows := append([]*proto.ActivityConditionConfig{}, p.wyCondCfgs...)
	p.WyMu.Unlock()
	cross, types := 0, map[int32]int{}
	for _, cfg := range rows {
		cid := cfg.GetConditionId()
		if cfg.GetType() == 84 || cid >= 20000 {
			cross++
		} else {
			types[cfg.GetType()]++
		}
	}
	t.Logf("folded conditionConfig rows=%d (nạp-bang cross=%d, type-groups=%d)", len(rows), cross, len(types))
	for typ, n := range types {
		t.Logf("  type %d: %d rungs", typ, n)
	}

	// 2. PER-ROW verdicts first (owner "chưa thấy nhận thưởng nạp bang" —
	// which door answers which code?), then the real production sweep.
	for _, cfg := range rows {
		cid := cfg.GetConditionId()
		act := cfg.GetActivityId()
		if act == 0 {
			act = actID
		}
		if cfg.GetType() == 84 || cid >= 20000 {
			rw, err := p.wyClaimCross(act, cid)
			who := cfg.GetDesc()
			if who == "" {
				who = cfg.GetTitle()
			}
			t.Logf("21045 act=%d cond=%d type=%d [%q] → err=%v rewards=%q", act, cid, cfg.GetType(), who, err, rw)
		}
	}
	seenT := map[int32]bool{}
	for _, cfg := range rows {
		if cfg.GetType() == 84 || cfg.GetConditionId() >= 20000 || seenT[cfg.GetType()] {
			continue
		}
		seenT[cfg.GetType()] = true
		rw, err := p.wyClaimType(actID, cfg.GetType())
		t.Logf("21033 act=%d type=%d → err=%v rewards=%q", actID, cfg.GetType(), err, rw)
	}

	// 3. the REAL production sweep (idempotent — re-firing settled rungs is a
	// no-op server-side). Outbound tap keys are low16 ("5235"/"5221").
	before := sent("5235") + sent("5221")
	p.wyCondSweep(actID)
	after := sent("5235") + sent("5221")
	t.Logf("milestone ops sent by sweep: 21045=%d 21033=%d (Δ=%d for %d rows) — server ret is the sole arbiter; ret≠0 = nothing ready",
		sent("5235"), sent("5221"), after-before, len(rows))

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
