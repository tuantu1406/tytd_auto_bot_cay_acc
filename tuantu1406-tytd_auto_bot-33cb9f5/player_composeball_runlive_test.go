//go:build live

package tytd_go

// Live one-shot run test (user, 2026-10-04: "run test it account test until
// 0 hp"): drives the REAL cbRunOnce engine on the throwaway account until
// HP is exhausted (or game over / day-lock). No SETTLE unless the engine
// itself chooses it; NO items (cbRunOnce has no item code path at all).
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveComposeBallUntilNoHP -v -timeout 1800s .

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

func TestLiveComposeBallUntilNoHP(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1")
	}
	liveRunUser, liveRunPass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		liveRunUser = v
		liveRunPass = os.Getenv("TYTD_LIVE_PASS")
	}
	liveRunServer := 710564
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("bad TYTD_LIVE_SERVER %q", v)
		}
		liveRunServer = n
	}
	t.Logf("live server: %d", liveRunServer)
	passportToken, err := request.LoginGPassport(liveRunUser, liveRunPass)
	if err != nil {
		t.Fatal(err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatal(err)
	}
	gameURL, err := request.GetServerURL(userID, liveRunServer)
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
	db, _ := gorm.Open(sqlite.Open("file:cb_run_live?mode=memory&cache=shared"), &gorm.Config{})
	if err := db.AutoMigrate(&model.User{}, &model.SystemConfig{}); err != nil {
		t.Fatal(err)
	}
	p := &Player{
		Username: liveRunUser, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		Notify:    true,
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

	// Give the config table a moment (activity detail poll) — the planner
	// needs widths; ENTER proceeds regardless and cbRunOnce re-polls.
	p.cbRequestConfig(actID)
	time.Sleep(1500 * time.Millisecond)

	// Empirical probe: the engine day-locks after any SETTLE/13017. The
	// test run settled at score 0 before (planner bug, fixed) — clear the
	// LOCAL lock to test whether the SERVER really refuses compose after a
	// same-day settle (memory says yes on 710547; this proves it on 710564
	// — if every compose answers 13017, the run stops via LockDay again).
	p.CbMu.Lock()
	p.cb.LockDay = ""
	p.CbMu.Unlock()

	// The engine run, synchronously (goroutine would race the final ENTER).
	p.cbRunOnce()

	p.CbMu.Lock()
	reLocked := p.cb.LockDay != ""
	uploads := p.cb.NextID
	p.CbMu.Unlock()
	if reLocked {
		t.Logf("engine holds a day-lock at exit (normal after SETTLE; only meaningful if NOTIFY mentions refusal)")
	}
	t.Logf("engine drop-counter after run: %d", uploads)

	for { // drain notifications the run emitted
		select {
		case n := <-p.NotiChan:
			t.Logf("NOTIFY: %s", n.MSG)
		default:
			goto drained
		}
	}
drained:
	// Final authoritative state straight from the server.
	e, err := p.RequestComposeBallEnterGame(15 * time.Second)
	if err != nil {
		t.Fatalf("final ENTER: %v", err)
	}
	sd := e.GetSyncData()
	if sd == nil {
		t.Fatalf("final ENTER ret=%d no sync", e.GetRet())
	}
	u := sd.GetSyncData()
	t.Logf("FINAL: ret=%d gameOver=%v hp=%d costHp=%d score=%d max=%d balls=%d",
		e.GetRet(), sd.GetGameOver(), u.GetHp(), u.GetCostHp(), u.GetScore(), u.GetMaxScore(), len(sd.GetBalls()))
	for _, c := range sd.GetCount() {
		t.Logf("FINAL ledger cfg=%d count=%d", c.GetConfigId(), c.GetCount())
	}
	// Engine policy 2026-10-04 (final): hp=0 → stop immediately (no wait, no
	// restart, board kept). The run ends on hp=0, GameOver, a failure ret
	// (notify+stop), or the day lock. Verify it reached one of those.
	p.CbMu.Lock()
	seenHP, overFlag := p.cb.HP, p.cb.GameOver
	p.CbMu.Unlock()
	t.Logf("engine exit state: hp=%d gameOver=%v serverGameOver=%v locked=%v", seenHP, overFlag, sd.GetGameOver(), reLocked)
	if overFlag || sd.GetGameOver() {
		return // ended by game over (settle is the correct close)
	}
	if reLocked {
		return // ended by day lock
	}
	if seenHP < 1 || u.GetHp() < 1 {
		t.Logf("ended at hp=0 — correct stop per policy (uploads=%d)", uploads)
		return
	}
	// Otherwise the run must have consumed real uploads (safety cap or a
	// failure-ret stop — both left the board intact).
	if uploads <= 2 {
		t.Errorf("engine exited early without hp/lock/gameover: uploads=%d hp=%d", uploads, seenHP)
	}
}
