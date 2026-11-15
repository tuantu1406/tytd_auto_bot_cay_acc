//go:build live

package tytd_go

// Levels-shape matrix on the STRONG progressed account (<live-user>):
// previous matrix ran on the weak bot account whose server-side sim verdict
// was probably LOSS — so shapes could not be judged. This one targets the
// account's real boss row and also fires the cow quick battle (server
// simulates, no client levels) as a proof the server sim runs for this acc.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 TYTD_LIVE_USER=... TYTD_LIVE_PASS=... TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouBossMatrixStrongLive -v -timeout 600s .

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
)

func TestXiYouBossMatrixStrongLive(t *testing.T) {
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
		Username: user, Name: user, UID: uid, Notify: false,
		NotiChan: make(chan NotifyMGS, 256), writeChan: make(chan []byte, 256), ctx: ctx,
	}
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.write(pack.Pack(AUTH_CMD, authMSG, uid))
	var mu sync.Mutex
	var dataReplies, upReplies, seedReplies [][]byte
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
			mu.Lock()
			switch ty {
			case SERVER_XIYOU_DATA:
				dataReplies = append(dataReplies, append([]byte{}, d...))
			case SERVER_XIYOU_UPD_PROGRESS:
				upReplies = append(upReplies, append([]byte{}, d...))
			case "79a1":
				seedReplies = append(seedReplies, append([]byte{}, d...))
			}
			mu.Unlock()
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
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) && len(p.ActList.byType(XIYOU_ACTIVITY_TYPE)) == 0 {
		time.Sleep(300 * time.Millisecond)
	}
	var actID int32
	nowMs := time.Now().UnixMilli()
	for _, e := range p.ActList.byType(XIYOU_ACTIVITY_TYPE) {
		if e.BeginTime <= nowMs && nowMs < e.EndTime {
			actID = e.ActivityID
		}
	}
	if actID == 0 {
		t.Fatal("type 311 not open")
	}
	waitNew := func(repls *[][]byte, before int, timeout time.Duration) []byte {
		lim := time.Now().Add(timeout)
		for time.Now().Before(lim) {
			mu.Lock()
			var d []byte
			if len(*repls) > before {
				d = append([]byte{}, (*repls)[before]...)
			}
			mu.Unlock()
			if d != nil {
				return d
			}
			time.Sleep(200 * time.Millisecond)
		}
		return nil
	}
	ask := func(cmd string, body []byte, repls *[][]byte) []byte {
		mu.Lock()
		before := len(*repls)
		mu.Unlock()
		p.write(pack.Pack(cmd, body, uid))
		return waitNew(repls, before, 8*time.Second)
	}

	// ── current row
	db, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
	d := ask(XIYOU_DATA_INFO_CMD, db, &dataReplies)
	var cur, next int32
	if d != nil {
		var r proto.XiYouPlayerDataInfoResp
		if pb.Unmarshal(d, &r) == nil && r.GetInfo() != nil {
			cur = r.GetInfo().GetProgress()
			if st, ok := xiyouStageTable()[cur]; ok {
				next = st.next
			}
		}
	}
	if next == 0 {
		t.Fatalf("no next for progress=%d", cur)
	}
	t.Logf("strong acc %s on row %d → target %d", user, cur, next)

	attempts := []struct {
		name   string
		levels []int32
	}{
		{"framelike14", []int32{20, 120, 220, 320, 420, 520, 620, 720, 820, 920, 1020, 1120, 1220, 1320}},
		{"slowramp50", []int32{50, 100, 150, 200, 250, 300, 350, 400, 450, 500}},
		{"bigramp", []int32{100, 800, 1600, 2400, 3200, 4000, 4800, 5600}},
		{"smallinc", []int32{7, 15, 26, 40, 57, 77, 100}},
		{"single-big", []int32{5000}},
	}
	for i, a := range attempts {
		sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
		if sd := ask("000386e1", sb, &seedReplies); sd != nil {
			var r proto.XiYouUpdateSeedResp
			pb.Unmarshal(sd, &r)
			t.Logf("attempt %d [%s] seed=%d", i+1, a.name, r.GetSeed())
		}
		time.Sleep(8 * time.Second)
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: next, CodeVersion: xiyouCodeVersion, Levels: a.levels,
		})
		d = ask(XIYOU_UPDATE_PROGRESS_CMD, body, &upReplies)
		ret, prog, rew := int32(-998), int32(0), ""
		if d != nil {
			var r proto.XiYouUpdateProgressResp
			if pb.Unmarshal(d, &r) == nil {
				ret, prog, rew = r.GetRet(), r.GetProgress(), r.GetReward()
			}
		}
		t.Logf("attempt %d [%s] levels=%v: ret=%d respProg=%d reward=%q", i+1, a.name, a.levels, ret, prog, rew)
		if ret == 0 {
			t.Logf(">>> CROSSED %d→%d with levels=%v — server gate is WEAK on strong accounts, NO sim needed", cur, next, a.levels)
			return
		}
		time.Sleep(12 * time.Second)
	}
	t.Errorf("strong account rejects all shapes too — crossing needs sim-exact levels")
}
