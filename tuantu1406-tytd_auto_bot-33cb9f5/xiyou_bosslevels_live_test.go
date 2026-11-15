//go:build live

package tytd_go

// TÂY DU PHỤC MA — levels-matrix lottery on the chapter-boss wall.
//
// Verified 2026-11-04 (TestXiYouBossFightLive): the account sits on 10507
// (ch1→ch2 gate), UPDATE_PROGRESS answers 31170 on the first spaced try and
// 31171 after; UPDATE_SEED hands out a fresh seed (335872) with ret=0; kill
// task 100014 stays 0 (ATTACK on a boss row counts nothing); claiming other
// conds never happened. Client-side checkServerLevels() is dead code, so the
// SERVER replays/validates the boss fight against its issued seed via the
// levels checkpoints (frames 20,120,220… of RandomSeed consumption).
//
// This is a brute-force lottery: one UPDATE_SEED followed immediately by
// UPDATE_PROGRESS(20101) with a different candidate levels shape per attempt,
// ≥45s spacing to stay clear of any retry cooldown. If one lands ret=0 we
// learn the server's levels check is weak; if none do, the crossing needs a
// real combat sim (engine must park + notify at chapter bosses).
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouBossLevelsLive -v -timeout 900s .

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

func TestXiYouBossLevelsLive(t *testing.T) {
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
		name   string
		seed   bool
		levels []int32
	}{
		{"control-nil", false, nil},
		{"seed+spawnlike", true, []int32{3, 40, 130, 260}},
		{"seed+framelike", true, []int32{20, 120, 220, 320}},
		{"seed+single1", true, []int32{1}},
		{"seed+flat2x5", true, []int32{2, 2, 2, 2, 2}},
		{"seed+longramp", true, []int32{1, 50, 150, 300, 500, 750, 1050}},
		{"seed+zeros", true, []int32{0, 0, 0}},
		{"seed+maxint", true, []int32{2147483646}},
		{"seed+flat10x3", true, []int32{10, 10, 10}},
	}
	for i, a := range attempts {
		if a.seed {
			sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
			d := ask("000386e1", sb, &seedReplies)
			if d != nil {
				var r proto.XiYouUpdateSeedResp
				pb.Unmarshal(d, &r)
				t.Logf("attempt %d [%s] seed=%d", i+1, a.name, r.GetSeed())
			} else {
				t.Logf("attempt %d [%s] seed: NO RESP", i+1, a.name)
			}
		}
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: 20101, CodeVersion: xiyouCodeVersion, Levels: a.levels,
		})
		d := ask(XIYOU_UPDATE_PROGRESS_CMD, body, &upReplies)
		ret, prog, rew := int32(-998), int32(0), ""
		if d != nil {
			var r proto.XiYouUpdateProgressResp
			if pb.Unmarshal(d, &r) == nil {
				ret, prog, rew = r.GetRet(), r.GetProgress(), r.GetReward()
			}
		}
		t.Logf("attempt %d [%s] levels=%v: ret=%d respProg=%d reward=%q", i+1, a.name, a.levels, ret, prog, rew)
		if ret == 0 {
			t.Logf(">>> CROSSED with levels=%v — server levels-check is WEAK: %s", a.levels, a.name)
			return
		}
		time.Sleep(45 * time.Second)
	}
	t.Errorf("no levels shape accepted — chapter-boss crossing needs real sim-generated levels")
}
