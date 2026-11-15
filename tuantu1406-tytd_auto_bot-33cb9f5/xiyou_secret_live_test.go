//go:build live

package tytd_go

// Ảo Cảnh (Secret) engine live test — real autoXiyou (activity discovery) +
// real autoXiyouSecret against the live server. Observational: fold the
// secret state, watch one window, report Start/Select/Kill/Sweep outcomes
// from the logs. Short window per owner's pacing preference.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 TYTD_LIVE_USER=... TYTD_LIVE_PASS=... \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiyouSecretLive -v -timeout 600s .
//	TYTD_XIYOU_WATCH=120  → window seconds (default 90)

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

func TestXiyouSecretLive(t *testing.T) {
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
			switch ty {
			case SERVER_XIYOU_DATA, SERVER_XIYOU_SYNC, SERVER_XIYOU_ATTACK,
				SERVER_XIYOU_RAND_EQUIP, SERVER_XIYOU_RESOLVE, SERVER_XIYOU_UPD_PROGRESS,
				SERVER_XIYOU_UPDATE_SEED, SERVER_XIYOU_SECRET_START, SERVER_XIYOU_SECRET_SELECT,
				SERVER_XIYOU_SECRET_KILL, SERVER_XIYOU_SECRET_SWEEP:
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

	// drain notifies so forceNotify never blocks
	go func() {
		for n := range p.NotiChan {
			t.Logf("NOTIFY: %s", n.MSG)
		}
	}()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && len(p.ActList.byType(XIYOU_ACTIVITY_TYPE)) == 0 {
		time.Sleep(300 * time.Millisecond)
	}
	if len(p.ActList.byType(XIYOU_ACTIVITY_TYPE)) == 0 {
		t.Fatal("activity type 311 not in the live list — nothing to auto")
	}
	p.Xiyou = true
	go p.autoXiyou()
	go p.autoXiyouSecret()

	// wait for discovery + first fold
	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)
		if p.xiyou.actID.Load() != 0 && p.xiyou.secID.Load() != 0 {
			break
		}
	}
	if p.xiyou.actID.Load() == 0 {
		t.Fatal("loop never discovered the activity id")
	}
	t.Logf("actID=%d secretID=%d sweep=%d/3 power=%s",
		p.xiyou.actID.Load(), p.xiyou.secID.Load(), p.xiyou.sweepTimes.Load(),
		p.xiyou.str(&p.xiyou.power))
	if p.xiyou.secID.Load() == 0 {
		t.Logf("secretID=0 — acc chưa mở Ảo Cảnh hoặc server không trả field 19; engine sẽ pollquietly")
	}

	watch := 90 * time.Second
	if v := os.Getenv("TYTD_XIYOU_WATCH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			watch = time.Duration(n) * time.Second
		}
	}
	startSec := p.xiyou.secID.Load()
	startSweep := p.xiyou.sweepTimes.Load()
	time.Sleep(watch)
	t.Logf("after %v: secret %d→%d sweep %d→%d park=%d",
		watch, startSec, p.xiyou.secID.Load(), startSweep, p.xiyou.sweepTimes.Load(),
		p.xiyou.secParkID.Load())
	// Soft assertions: something must have MOVED if the acc is eligible.
	if startSec > 0 && p.xiyou.secID.Load() > startSec {
		t.Logf("✓ frontier advanced over the wire (Kill accepted!)")
	} else if p.xiyou.sweepTimes.Load() > startSweep {
		t.Logf("✓ free sweep landed over the wire")
	} else if p.xiyou.secParkID.Load() == 0 {
		t.Logf("— no frontier/sweep movement this window (check [XiYou] ảo cảnh log lines above)")
	}
}
