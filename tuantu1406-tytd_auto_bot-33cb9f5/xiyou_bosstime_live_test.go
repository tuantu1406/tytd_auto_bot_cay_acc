//go:build live

package tytd_go

// TÂY DU PHỤC MA — boss fight with REAL fight duration.
//
// Owner (again, 2026-11-04): "ải cuối cần fight ở server rồi mới gửi request
// lên server để update trạng thái, mày không thấy trong code à" — the code
// IS: msg_reqRoleData + msg_reqSeed (231137) → the boss battle RUNS for
// 10-60+ seconds of real time (20fps sim, timeLimit=XIYOU_STAGE_TIME_LIMIT)
// → onBattleWin → msg_updateProgress(next, levels).
//
// Every earlier probe fired UPDATE_PROGRESS <1s after UPDATE_SEED. If the
// server records the seed issue time and rejects "boss win" claims that
// arrive faster than the fight could possibly take, that explains
// 31170 (no fight session) / 31171 (session too fresh) perfectly.
//
// This test replays the client wire order faithfully and waits like a real
// battle would: DATA_INFO → UPDATE_SEED → sleep → UPDATE_PROGRESS(20101).
// Waits are escalated (30s, 75s, 130s) across attempts; levels start empty
// (server can't demand more than nothing if it only watches time) and get
// frame-shaped on later attempts.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouBossTimeLive -v -timeout 900s .

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

func TestXiYouBossTimeLive(t *testing.T) {
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

	deadline := time.Now().Add(20 * time.Second)
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

	attempts := []struct {
		fight  time.Duration
		levels []int32
	}{
		{30 * time.Second, nil},
		{75 * time.Second, nil},
		{75 * time.Second, []int32{20, 120, 220, 320}},
		{130 * time.Second, []int32{20, 120, 220, 320, 420, 520, 620, 720, 820, 920, 1020, 1120, 1220, 1320}},
	}
	for i, a := range attempts {
		// faithful client order: reqRoleData, reqSeed, FIGHT (wait), progress
		db, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
		ask(XIYOU_DATA_INFO_CMD, db, &dataReplies)
		sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
		d := ask("000386e1", sb, &seedReplies)
		seed := int32(0)
		if d != nil {
			var r proto.XiYouUpdateSeedResp
			pb.Unmarshal(d, &r)
			seed = r.GetSeed()
		}
		t.Logf("attempt %d: roleData+seed=%d issued — fighting for %v (levels=%v)", i+1, seed, a.fight, a.levels)
		time.Sleep(a.fight)
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: 20101, CodeVersion: xiyouCodeVersion, Levels: a.levels,
		})
		d = ask(XIYOU_UPDATE_PROGRESS_CMD, body, &upReplies)
		ret, prog, rew := int32(-998), int32(0), ""
		if d != nil {
			var r proto.XiYouUpdateProgressResp
			if pb.Unmarshal(d, &r) == nil {
				ret, prog, rew = r.GetRet(), r.GetProgress(), r.GetReward()
			}
		}
		t.Logf("attempt %d after %v fight: ret=%d respProg=%d reward=%q", i+1, a.fight, ret, prog, rew)
		if ret == 0 {
			t.Logf(">>> CROSSED 10507 after %v fight (levels=%v) — server gates on fight TIME (seed→progress gap)", a.fight, a.levels)
			return
		}
		time.Sleep(20 * time.Second)
	}
	t.Errorf("real-duration fights still refused — levels must be sim-exact")
}
