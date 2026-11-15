//go:build live

package tytd_go

// READ-ONLY one-shot: dump the whole pushed activity list (type, id, window,
// open-now) so a new event ("Thương Minh Yêu Vực", owner 2026-11-09, server
// 710547) can be located. No commands beyond AUTH + PING.
//
//	TYTD_LIVE=1 TYTD_ACTDUMP=1 [TYTD_LIVE_SERVER=710547 TYTD_LIVE_USER=... TYTD_LIVE_PASS=...] \
//	  go test -tags "sqlcipher live" -count=1 -run TestActListDumpLive -v -timeout 180s .

import (
	"context"
	"os"
	"sort"
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

func TestActListDumpLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ACTDUMP") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ACTDUMP=1")
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
	db, _ := gorm.Open(sqlite.Open("file:actdump?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{}, &model.SystemConfig{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel, Notify: false,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 64)}
	go func() {
		for f := range p.writeChan {
			if err := conn.WriteMessage(2, f); err != nil {
				return
			}
		}
	}()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.write(pack.Pack(AUTH_CMD, authMSG, uid))
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			if ty == SERVER_ACTIVITY_LIST || ty == SERVER_ACTIVITY_LIST_UPDATE {
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			}
		}
	}()
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				p.write(pack.Pack(PING_CMD, []byte{}, uid))
			}
		}
	}()
	// give the list + updates time to land
	time.Sleep(12 * time.Second)
	all := p.ActList.snapshot()
	sort.Slice(all, func(i, j int) bool { return all[i].Type < all[j].Type })
	now := time.Now().UnixMilli()
	t.Logf("server=%d uid=%d total=%d at %d", server, uid, len(all), now)
	for _, e := range all {
		open := e.BeginTime <= now && now < e.EndTime
		t.Logf("ACT type=%-4d id=%d begin=%s end=%s open=%v", e.Type, e.ActivityID,
			time.UnixMilli(e.BeginTime).Format("01-02 15:04"),
			time.UnixMilli(e.EndTime).Format("01-02 15:04"), open)
	}
}
