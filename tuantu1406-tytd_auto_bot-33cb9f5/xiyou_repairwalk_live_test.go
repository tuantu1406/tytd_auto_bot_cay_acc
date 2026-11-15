//go:build live

package tytd_go

// TÂY DU PHỤC MA — repair + clean-walk test.
//
// Root-cause theory from the boss-gate series: every boss row reached by
// LEGIT step-by-step claims crossed fine even with empty levels (10107/10207/
// 10307/10407 all crossed, rewards paid). The ONLY row that ever stayed
// locked (31170/31171 forever) is 10507 — reached by the JUMP-CLAMP (the
// server answered the illegal claim 30407 with ret=0 and parked progress on
// the legit nextStage without running its normal enter-row bookkeeping).
//
// This test:
//	1. probes recovery paths from the corrupted 10507 row:
//	   a) bogus far claim (clamp again — maybe the clamp path re-inits),
//	   b) backward claim (10101),
//	   then re-reads server progress;
//	2. if progress moved back, walks CLEANLY (≥5s spacing, empty levels) to
//	   the first boss row and tries to cross — the definitive test of the
//	   owner's "ải cuối mỗi cấp không lừa được server" claim.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouRepairWalkLive -v -timeout 420s .

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

func TestXiYouRepairWalkLive(t *testing.T) {
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
	var dataReplies, upReplies [][]byte
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
	state := func() (int32, int64, int32) {
		mu.Lock()
		before := len(dataReplies)
		mu.Unlock()
		body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
		p.write(pack.Pack(XIYOU_DATA_INFO_CMD, body, uid))
		d := waitNew(&dataReplies, before, 8*time.Second)
		if d == nil {
			return -1, 0, 0
		}
		var r proto.XiYouPlayerDataInfoResp
		if pb.Unmarshal(d, &r) != nil || r.GetInfo() == nil {
			return -1, 0, 0
		}
		pw, _ := strconv.ParseInt(r.GetInfo().GetPower(), 10, 64)
		return r.GetInfo().GetProgress(), pw, r.GetInfo().GetMainConditonId()
	}
	assert := func(want int32) (int32, string, int32) {
		mu.Lock()
		before := len(upReplies)
		mu.Unlock()
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: want, CodeVersion: xiyouCodeVersion,
		})
		p.write(pack.Pack(XIYOU_UPDATE_PROGRESS_CMD, body, uid))
		d := waitNew(&upReplies, before, 8*time.Second)
		if d == nil {
			return -998, "timeout", 0
		}
		var r proto.XiYouUpdateProgressResp
		if pb.Unmarshal(d, &r) != nil {
			return -999, "unmarshal", 0
		}
		return r.GetRet(), r.GetReward(), r.GetProgress()
	}

	progress, power, cond := state()
	t.Logf("STATE progress=%d power=%d mainConditonId=%d", progress, power, cond)
	stages := xiyouLoadStages(t)

	if progress == 10507 {
		// ── 1. recovery probes ────────────────────────────────────────────
		time.Sleep(8 * time.Second)
		ret, rew, respProg := assert(30407) // bogus far — clamp path again
		t.Logf("recovery bogus-30407: ret=%d respProgress=%d reward=%q", ret, respProg, rew)
		p2, _, _ := state()
		t.Logf("server progress after bogus: %d", p2)
		if p2 == 10507 {
			time.Sleep(8 * time.Second)
			ret, rew, respProg = assert(10101) // backward
			t.Logf("recovery backward-10101: ret=%d respProgress=%d reward=%q", ret, respProg, rew)
			p2, _, _ = state()
			t.Logf("server progress after backward: %d", p2)
		}
		if p2 == 10507 {
			time.Sleep(8 * time.Second)
			ret, rew, respProg = assert(20101) // plain legit target, long gap
			t.Logf("recovery plain-20101 after 8s gap: ret=%d respProgress=%d reward=%q", ret, respProg, rew)
			p2, _, _ = state()
			t.Logf("server progress after plain: %d", p2)
		}
		progress = p2
	}

	// ── 2. clean walk from wherever we are to the next boss cross ────────
	for i := 0; i < 40; i++ {
		row, ok := stages[progress]
		if !ok || row.NextStage == 0 {
			t.Logf("chain tail at %d", progress)
			return
		}
		ret, rew, respProg := assert(row.NextStage)
		t.Logf("clean step %d→%d type=%d verifyP=%d: ret=%d respProg=%d reward=%q",
			progress, row.NextStage, row.Type, row.VerifyPower, ret, respProg, rew)
		if ret == 0 {
			progress = respProg
			if respProg == 0 {
				progress = row.NextStage
			}
			if row.Type == 2 {
				t.Logf(">>> BOSS %d CROSSED cleanly (empty levels, reward=%q) — server DOES trust boss crossings when arrived legitimately", row.NextStage, rew)
			}
			time.Sleep(5 * time.Second)
			continue
		}
		t.Logf("blocked at %d (type=%d) with ret=%d", progress, row.Type, ret)
		if row.Type == 2 {
			// give it a couple more spaced tries before declaring the wall
			crossed := false
			for t2 := 1; t2 <= 2; t2++ {
				time.Sleep(12 * time.Second)
				ret2, rew2, _ := assert(row.NextStage)
				t.Logf("spaced boss try %d: ret=%d reward=%q", t2, ret2, rew2)
				if ret2 == 0 {
					progress = row.NextStage
					crossed = true
					break
				}
			}
			if !crossed {
				t.Errorf("BOSS WALL CONFIRMED at %d (verifyP=%d power=%d) even walking cleanly", progress, row.VerifyPower, power)
				return
			}
		} else {
			return
		}
	}
	_, pw, c := state()
	t.Logf("END progress=%d power=%d cond=%d", progress, pw, c)
}
