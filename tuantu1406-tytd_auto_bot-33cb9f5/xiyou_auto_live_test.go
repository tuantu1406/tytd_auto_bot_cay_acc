//go:build live

package tytd_go

// TÂY DU PHỤC MA engine live test — runs the REAL autoXiyou sender and the
// REAL xiyouDeliver reader against the live server on the owner-designated
// account (<live-user>@710564). No seats: exactly the production wiring
// (one send loop + one reader pump). Verifies end-to-end over ~50s:
//
//	1. the loop discovers the type-311 activity from ActList (no blind scan);
//	2. ATTACK/RAND land (loop keeps moving: a ret=210 would zero the id and
//	   we would see re-discovery churn — we assert the id STAYS non-zero);
//	3. UPDATE_PROGRESS actually moves xiyou.progress (the whole point of the
//	   ải-chính-tuyến spam).
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiyouAutoLive -v -timeout 240s .

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
)

func TestXiyouAutoLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_XIYOU") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_XIYOU=1")
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
	p := &Player{
		Username:  user,
		Name:      user,
		UID:       uid,
		Notify:    false,
		NotiChan:  make(chan NotifyMGS, 256),
		writeChan: make(chan []byte, 256),
		ctx:       ctx,
	}
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.write(pack.Pack(AUTH_CMD, authMSG, uid))

	// Production pump: xiyou answers → xiyouDeliver; activity pushes → ActList.
	connDead := make(chan struct{})
	go func() {
		defer close(connDead)
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
			case SERVER_XIYOU_DATA, SERVER_XIYOU_SYNC, SERVER_XIYOU_ATTACK,
				SERVER_XIYOU_RAND_EQUIP, SERVER_XIYOU_RESOLVE, SERVER_XIYOU_UPD_PROGRESS,
				SERVER_XIYOU_UPDATE_SEED:
				go p.xiyouDeliver(ty, d)
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
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

	// wait for the activity list, then arm the toggle and run the real loop
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && len(p.ActList.byType(XIYOU_ACTIVITY_TYPE)) == 0 {
		time.Sleep(300 * time.Millisecond)
	}
	if len(p.ActList.byType(XIYOU_ACTIVITY_TYPE)) == 0 {
		t.Fatal("activity type 311 not in the live list — nothing to auto")
	}
	p.Xiyou = true
	go p.autoXiyou()

	startProgress := p.xiyou.progress.Load()
	var actID int32
	for i := 0; i < 50; i++ {
		time.Sleep(1 * time.Second)
		if id := p.xiyou.actID.Load(); id != 0 {
			actID = id
			break
		}
	}
	if actID == 0 {
		t.Fatal("loop never discovered the activity id")
	}
	t.Logf("discovered actID=%d progress=%d", actID, startProgress)

	// ENDLESS (owner 2026-11-10: "phải chạy mãi không cần login lại, đánh
	// thắng phải claim"): one login, connection held open forever — the
	// production autoXiyou loop keeps attacking and WINS→CLAIMS on its own
	// (it sleeps the full fight duration then UPDATE_PROGRESSs; a short-lived
	// harness that cancels mid-fight would kill the claim — that was the
	// 40s-harness bug). Heartbeat log every 30s; exits only on socket death
	// or process kill. Run with -timeout 0.
	if os.Getenv("TYTD_XIYOU_ENDLESS") != "" {
		t.Logf("ENDLESS: giữ kết nối chạy mãi — thắng là engine tự claim")
		hb := time.NewTicker(30 * time.Second)
		defer hb.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-connDead:
				t.Fatal("kết nối ws chết giữa chừng — thoát để launcher login lại")
			case <-hb.C:
				t.Logf("endless: actID=%d progress=%d power=%s exp=%s realm=%d",
					p.xiyou.actID.Load(), p.xiyou.progress.Load(),
					p.xiyou.str(&p.xiyou.power), p.xiyou.str(&p.xiyou.exp), p.xiyou.realm.Load())
			}
		}
	}

	// ~40 more seconds (override with TYTD_XIYOU_WATCH): at 5s/tick with
	// advance-every-4 we expect at least one stage-up in a healthy run.
	watch := 40 * time.Second
	if v := os.Getenv("TYTD_XIYOU_WATCH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			watch = time.Duration(n) * time.Second
		}
	}
	time.Sleep(watch)
	endAct := p.xiyou.actID.Load()
	endProgress := p.xiyou.progress.Load()
	t.Logf("after %v: actID=%d progress %d→%d power=%s exp=%s realm=%d",
		watch, endAct, startProgress, endProgress, p.xiyou.str(&p.xiyou.power), p.xiyou.str(&p.xiyou.exp), p.xiyou.realm.Load())
	if endAct == 0 {
		t.Fatal("actID was cleared — activity went wrong mid-run (ret=210?)")
	}
	if endProgress == startProgress && !xiyouIsBossRow(xiyouStageTable(), startProgress) {
		t.Error("progress did not move and we're not parked on a boss row — UPDATE_PROGRESS refused or ticks too slow")
	}
}
