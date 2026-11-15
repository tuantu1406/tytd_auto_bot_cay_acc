//go:build live

package tytd_go

// TÂY DU PHỤC MA — faithful boss crossing on a REAL progressed account
// (owner: "ải cuối cần fight ở server rồi mới gửi request lên server để
// update trạng thái" + "mày test với acc <live-user>").
//
// <live-user>@710564 sits on a boss row with a full build (5 skills,
// 3 passives, 12 gear, power 6.4M). If the server simulates the boss fight
// itself (cow quick/ad proves it can), the verdict depends on the build —
// this account should WIN where the weak bot account lost (31170 forever).
//
// Flow mirrors the real client: reqRoleData(231116) + reqSeed(231137) →
// fight for real seconds → msg_updateProgress(next, levels).
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 TYTD_LIVE_USER=... TYTD_LIVE_PASS=... \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouBossCrossLive -v -timeout 600s .

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

func TestXiYouBossCrossLive(t *testing.T) {
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
	curProgress := func() int32 {
		body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
		d := ask(XIYOU_DATA_INFO_CMD, body, &dataReplies)
		if d == nil {
			return -1
		}
		var r proto.XiYouPlayerDataInfoResp
		if pb.Unmarshal(d, &r) != nil || r.GetInfo() == nil {
			return -1
		}
		return r.GetInfo().GetProgress()
	}

	cur := curProgress()
	st, ok := xiyouStageTable()[cur]
	t.Logf("progress=%d stage(type=%d,next=%d) found=%v power-build account %s", cur, st.typ, st.next, ok, user)
	if !ok || st.next == 0 {
		t.Fatalf("no stage row for %d — nothing to cross", cur)
	}

	attempts := []struct {
		fight  time.Duration
		levels []int32
	}{
		{35 * time.Second, nil},
		{35 * time.Second, []int32{20, 120, 220, 320, 420, 520, 620, 720, 820, 920, 1020, 1120, 1220, 1320}},
		{75 * time.Second, []int32{20, 120, 220, 320, 420, 520, 620, 720, 820, 920, 1020, 1120, 1220, 1320, 1420, 1520, 1620, 1720, 1820, 1920}},
	}
	for i, a := range attempts {
		// fresh roleData+seed each attempt, exactly like onReqBossFight
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
		t.Logf("attempt %d: seed=%d — fighting %v then assert %d→%d (levels=%v)", i+1, seed, a.fight, cur, st.next, a.levels)
		time.Sleep(a.fight)
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: st.next, CodeVersion: xiyouCodeVersion, Levels: a.levels,
		})
		d = ask(XIYOU_UPDATE_PROGRESS_CMD, body, &upReplies)
		ret, prog, rew := int32(-998), int32(0), ""
		if d != nil {
			var r proto.XiYouUpdateProgressResp
			if pb.Unmarshal(d, &r) == nil {
				ret, prog, rew = r.GetRet(), r.GetProgress(), r.GetReward()
			}
		}
		t.Logf("attempt %d: ret=%d respProg=%d reward=%q", i+1, ret, prog, rew)
		if ret == 0 {
			now := curProgress()
			t.Logf(">>> CROSSED %d→%d (server says progress now %d) — strong build wins the server-side sim", cur, st.next, now)
			return
		}
		time.Sleep(15 * time.Second)
	}
	now := curProgress()
	t.Errorf("strong account still refused at boss %d (progress now %d) — gate is NOT build-gated sim", cur, now)
}
