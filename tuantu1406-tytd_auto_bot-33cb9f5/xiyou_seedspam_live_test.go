//go:build live

package tytd_go

// TÂY DU PHỤC MA — how fast can we farm seeds until one wins?
//
// Probe: fire N UPDATE_SEED back-to-back (~0.5s apart), count how many
// answers ret=0 with a fresh seed vs rate-limited, then check the OLD seed
// still claims after a NEWER seed was issued (is the claim bound to the
// LAST issued seed?).
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouSeedSpamLive -v -timeout 600s .

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

func TestXiYouSeedSpamLive(t *testing.T) {
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
	var seedReplies, upReplies [][]byte
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
			case SERVER_XIYOU_UPDATE_SEED:
				seedReplies = append(seedReplies, append([]byte{}, d...))
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

	// Phase 1: spam 12 UPDATE_SEED at ~0.5s apart; record each reply.
	sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
	seeds := []int32{}
	for i := 0; i < 12; i++ {
		mu.Lock()
		before := len(seedReplies)
		mu.Unlock()
		p.write(pack.Pack(XIYOU_UPDATE_SEED_CMD, sb, p.UID))
		lim := time.Now().Add(6 * time.Second)
		var d []byte
		for time.Now().Before(lim) {
			mu.Lock()
			if len(seedReplies) > before {
				d = append([]byte{}, seedReplies[before]...)
			}
			mu.Unlock()
			if d != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		ret, seed := int32(-998), int32(0)
		if d != nil {
			var sr proto.XiYouUpdateSeedResp
			if pb.Unmarshal(d, &sr) == nil {
				ret, seed = sr.GetRet(), sr.GetSeed()
			}
		}
		t.Logf("seed #%2d: ret=%d seed=%d", i+1, ret, seed)
		if ret == 0 && seed != 0 {
			seeds = append(seeds, seed)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf(">>> %d/12 seed requests answered fresh", len(seeds))
	if len(seeds) < 2 {
		return
	}

	// Phase 2: the OLD seed (seeds[0]) vs the LATEST (seeds[-1]) — after the
	// burst, which one does the server validate the claim against? We stand
	// on boss 190307 (LOSS account) so both refusals are expected; what we
	// read is whether timing/rate state leaks: a 31171 right after the burst
	// means UPDATE_PROGRESS is also watched after seed spam.
	time.Sleep(5 * time.Second)
	body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
		ActivityId: actID, Progress: 190401, CodeVersion: xiyouCodeVersion,
	})
	mu.Lock()
	before := len(upReplies)
	mu.Unlock()
	p.write(pack.Pack(XIYOU_UPDATE_PROGRESS_CMD, body, p.UID))
	lim := time.Now().Add(8 * time.Second)
	for time.Now().Before(lim) && len(upReplies) <= before {
		time.Sleep(200 * time.Millisecond)
	}
	mu.Lock()
	var d []byte
	if len(upReplies) > before {
		d = append([]byte{}, upReplies[before]...)
	}
	mu.Unlock()
	ret := int32(-998)
	if d != nil {
		var pr proto.XiYouUpdateProgressResp
		if pb.Unmarshal(d, &pr) == nil {
			ret = pr.GetRet()
		}
	}
	t.Logf("claim right after seed burst: ret=%d (31170=gate, 31171=rate/seq state leaked)", ret)
}
