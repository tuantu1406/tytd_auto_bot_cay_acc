//go:build live

package tytd_go

// ENTER-BURST + RUN live probe (owner 2026-10-25: "/start_thanlong — tao
// nghi là server chưa trả info event thì mày đã chạy, và mày chạy liên tục
// làm lỗi, nên delay 1s"; plus "nhận 0 hp, vào ra vào lại là được" and
// "thỉnh thoảng bật thần long sẽ làm server disconnect").
//
// Stage 1 — REPRO the stale-info bug: right after the type-130 row shows up
// (no config wait, no settle delay) fire THREE raw ENTERs 1s apart and dump
// for each: ret, whether syncData/userDataSync arrived at all, whether the
// hp FIELD is present, hp/costHp, balls, gameOver, ms-since-auth. The owner's
// cure was manual leave+re-enter — this measures whether a plain re-ENTER
// carries the Linh Khí the first one didn't.
//
// Stage 2 — PROVE the fix end to end: run the REAL production cbRunOnce
// (its new gates: wait-for-config, 1s beat, ENTER re-tries). A 5s PING
// keeper rides the writer and the reader records its death cause, so a
// server disconnect mid-run is detected and named (owner bug 2).
//
// NO items on any path (cbRunOnce has none). NEVER settles unless the
// engine itself hits GameOver. Throwaway account <live-user>@710564.
//
//	TYTD_LIVE=1 TYTD_CB_ENTER=1 go test -count=1 -tags "sqlcipher live" -run TestLiveComposeBallEnterBurst -v -timeout 1200s .

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
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

func TestLiveComposeBallEnterBurst(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_CB_ENTER") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_CB_ENTER=1")
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
	db, _ := gorm.Open(sqlite.Open("file:cb_enter_burst?mode=memory&cache=shared"), &gorm.Config{})
	if err := db.AutoMigrate(&model.User{}, &model.SystemConfig{}); err != nil {
		t.Fatal(err)
	}
	p := &Player{
		Username: user, Name: user, UID: uid, ChatID: int64(uid), Running: true,
		Notify: true, DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan:  make(chan NotifyMGS, 256),
		writeChan: make(chan []byte, 64),
	}
	go func() {
		for frame := range p.writeChan {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	var readerErr atomic.Value // string: why the reader died
	rdone := make(chan struct{})
	go func() {
		defer close(rdone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				readerErr.Store(fmt.Sprintf("read: %v", err))
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
	var notesMu sync.Mutex
	var notes []string
	go func() {
		for n := range p.NotiChan {
			notesMu.Lock()
			notes = append(notes, n.MSG)
			notesMu.Unlock()
		}
	}()

	t0 := time.Now()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// Wait only for the ACTIVITY ROW (that is precisely the "server chưa trả
	// info event" window the owner suspects): fire ENTERs the moment the
	// type-130 id appears — no config wait, no extra settle time.
	deadline := time.Now().Add(25 * time.Second)
	actID := int32(0)
	for actID == 0 && time.Now().Before(deadline) {
		actID = p.ComposeBallActivityID()
		if actID == 0 {
			time.Sleep(400 * time.Millisecond)
		}
	}
	if actID == 0 {
		t.Skip("đại hội luyện khí chưa mở (actList không có type 130 active)")
	}
	t.Logf("actList 130 active sau %v — BURST 3 ENTERs tức thì (không wait config)", time.Since(t0))

	// ===== Stage 1: raw ENTER burst, dumps of the Linh Khí fold =====
	for i := 1; i <= 3; i++ {
		e, err := p.RequestComposeBallEnterGame(15 * time.Second)
		if err != nil {
			t.Fatalf("ENTER #%d: %v (reader: %v)", i, err, readerErr.Load())
		}
		s := e.GetSyncData()
		var u *proto.ComposeBallUserDataSync
		hpNil, balls, over := true, 0, false
		if s != nil {
			u = s.GetSyncData()
			balls, over = len(s.GetBalls()), s.GetGameOver()
			hpNil = u == nil || u.Hp == nil
		}
		t.Logf("ENTER#%d t=%v ret=%d sync=%v userSync=%v hpField=%v hp=%d costHp=%d score=%d balls=%d over=%v",
			i, time.Since(t0).Round(time.Millisecond), e.GetRet(),
			s != nil, u != nil, !hpNil, u.GetHp(), u.GetCostHp(), u.GetScore(), balls, over)
		p.cbApplySync(s)
		time.Sleep(time.Second)
	}

	// ===== Stage 2: production run with the 5s ping keeper =====
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				select {
				case p.writeChan <- pack.Pack(PING_CMD, []byte{}, uid):
				case <-ctx.Done():
				}
			}
		}
	}()
	t.Logf("=== cbRunOnce production (config-wait + 1s beat + ENTER re-tries) ===")
	done := make(chan struct{})
	go func() { p.cbRunOnce(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Minute):
		t.Fatal("cbRunOnce kẹt quá 15 phút")
	}
	p.CbMu.Lock()
	hp, score, have, over := p.cb.HP, p.cb.Score, p.cb.Have, p.cb.GameOver
	p.CbMu.Unlock()
	notesMu.Lock()
	for _, s := range notes {
		t.Logf("NOTIFY: %s", s)
	}
	notesMu.Unlock()
	if v := readerErr.Load(); v != nil {
		fmt.Printf("VERDICT-ENTERBURST: KẾT NỐI CHẾT giữa chừng: %v (hp=%d score=%d have=%v over=%v)\n", v, hp, score, have, over)
		t.Errorf("server disconnect trong lúc bật thần long — reproduce bug 2")
		return
	}
	fmt.Printf("VERDICT-ENTERBURST: OK — run xong sạch, kết nối sống, hp=%d score=%d have=%v over=%v\n", hp, score, have, over)
}
