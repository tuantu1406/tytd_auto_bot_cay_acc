//go:build live

package tytd_go

// Read-only live probe: pin the BATTLE_* opcodes of Luân Hồi Bí Cảnh (172).
// 217230 (BATTLE_ENTER, the bracket list) was confirmed earlier; HEART /
// UNIONPLAYER / CHALLENGE numbering was never seen on the wire. We send each
// candidate with a deliberately-invalid target unionId — every answer type
// observed identifies an opcode; refusals cost nothing (no items, battle
// challenge against a nonsense union can only be rejected).
//
//	TYTD_LIVE=1 TYTD_RBT_BATTLE=1 go test -tags "sqlcipher live" -count=1 -run TestRbtBattleOpcodesLive -v -timeout 240s .

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
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRbtBattleOpcodesLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_BATTLE") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_BATTLE=1")
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
	db, _ := gorm.Open(sqlite.Open("file:rbtbattle?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid)}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256)}
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
			if ty == SERVER_ACTIVITY_LIST || ty == SERVER_ACTIVITY_LIST_UPDATE {
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
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

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(p.ActList.snapshot()) == 0 {
		time.Sleep(300 * time.Millisecond)
	}
	now := time.Now()
	var actID int32
	for _, e := range p.ActList.snapshot() {
		if e.Type == rbtActivityType && e.status(now) == "active" {
			actID = e.ActivityID
		}
	}
	if actID == 0 {
		t.Fatal("no active 172")
	}
	t.Logf("172 actID=%d", actID)

	drain := func(d time.Duration) {
		end := time.Now().Add(d)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				raw, _ := hex.DecodeString(a[1])
				t.Logf("ANSWER %s len=%d", a[0], len(raw))
				walkWire(t, raw, "  ", 0)
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	send := func(dec int, body []byte, tag string) {
		cmd := "00000000"
		if dec > 0 {
			cmd = fmt.Sprintf("%08x", dec)
		}
		t.Logf("SEND %s (%s)", tag, cmd)
		p.writeChan <- pack.Pack(cmd, body, uid)
		drain(2500 * time.Millisecond)
	}

	send(217200, rbtBody(actID), "ENTER_ACTIVITY")
	send(217201, rbtBody(actID), "ENTER")
	send(217230, rbtBody(actID), "BATTLE_ENTER?217230")
	send(217231, rbtBody(actID), "CAND 217231")
	send(217232, rbtBody(actID), "CAND 217232")
	up1 := rbtBody(actID)
	up1 = protowire.AppendTag(up1, 2, protowire.VarintType)
	up1 = protowire.AppendVarint(up1, 7105626388)
	send(217231, up1, "UNIONPLAYER?217231")
	up2 := rbtBody(actID)
	up2 = protowire.AppendTag(up2, 2, protowire.VarintType)
	up2 = protowire.AppendVarint(up2, 7105620235)
	send(217231, up2, "UNIONPLAYER?217231 #2")
	// CHALLENGE candidate: {activityId, unionId=12345 (nonsense → refuse)}.
	chal := rbtBody(actID)
	chal = protowire.AppendTag(chal, 2, protowire.VarintType)
	chal = protowire.AppendVarint(chal, 12345)
	send(217233, chal, "CAND 217233 challenge")
	send(217234, rbtBody(actID), "CAND 217234")
}
