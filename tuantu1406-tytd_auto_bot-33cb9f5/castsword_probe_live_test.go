//go:build live

package tytd_go

// Live auto-play test for Luyện Khí Đại Hội (remoteCastSword, type 119) on
// the authorized test account. Login → auth → wait for the activity push →
// CastSwordAutoPlay (FREE moves only — the board's item tools are never
// touched) → prints the final score. The wire schema this exercises was
// recovered from this account on 2026-10-08 (see tytd.proto comments).
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestCastSwordLivePlay -v -timeout 400s .

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

func TestCastSwordLivePlay(t *testing.T) {
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
	db, _ := gorm.Open(sqlite.Open("file:castsword_live?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{}, &model.SystemConfig{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel, Notify: true,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 64)}
	go func() {
		for f := range p.writeChan {
			if err := conn.WriteMessage(2, f); err != nil {
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel() // production wsHandler treats a read error as session death
				return
			}
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch ty {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			case SERVER_CASTSWORD_ENTER, SERVER_CASTSWORD_MOVE,
				SERVER_CASTSWORD_SETTLE, SERVER_CASTSWORD_GETREWARD:
				p.csDeliver(ty, d)
			}
		}
	}()
	defer func() { conn.Close(); <-done }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// Wait for the activity list, then verify ENTER parses into the real
	// board (this is the schema proof: 16 cells + score + strength all
	// non-nil-shaped fields after Unmarshal).
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := p.csActiveID(); err == nil {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if _, err := p.csActiveID(); err != nil {
		t.Skip("Luyện Khí Đại Hội (type 119) not open for this account right now")
	}
	rsp, err := p.RequestCastSwordEnter()
	if err != nil {
		t.Fatalf("ENTER: %v", err)
	}
	e := rsp.GetEntity()
	if len(e.GetCells()) != 16 {
		t.Fatalf("schema drift: cells=%d (want 16)", len(e.GetCells()))
	}
	t.Logf("ENTER ok: score=%d high=%d strength=%d tools=%d",
		e.GetScore(), e.GetHighScore(), e.GetStrength(), len(e.GetTools()))

	moves, score, why, err := p.CastSwordAutoPlay(nil)
	t.Logf("AUTOPLAY: %d nước, điểm %d, lý do %q, err=%v", moves, score, why, err)
	if err != nil {
		t.Fatalf("autoplay: %v", err)
	}
	if moves < 1 {
		t.Fatalf("autoplay played %d moves — board or gate broken", moves)
	}
}
