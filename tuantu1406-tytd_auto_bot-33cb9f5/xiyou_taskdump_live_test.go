//go:build live

package tytd_go

// TÂY DU PHỤC MA — main-task dump.
//
// The account sits on boss row 10507 forever (31170/31171) and its
// XiYouPlayerDataMsg.mainConditonId = 100014 — the generic activity
// CONDITION system tracks the current MAIN TASK. Theory: the server gates
// UPDATE_PROGRESS on the current main-task condition being fulfilled
// (real kills / boss win), which is exactly the owner's "ải 5 phải gửi
// request lên server đánh, trả win mới được qua ải".
//
// This probe dumps, for the live XiYou activity:
//   - conditionConfig rows around mainConditonId (param/type/desc/rewards);
//   - the player's conditionDataList values (progress vs target);
//   - energy item 105280 holdings.
// Read-only.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouTaskDumpLive -v -timeout 240s .

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

func TestXiYouTaskDumpLive(t *testing.T) {
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
	var detailReplies [][]byte
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
			if ty == SERVER_ACTIVITY_DETAIL {
				mu.Lock()
				detailReplies = append(detailReplies, append([]byte{}, d...))
				mu.Unlock()
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
	t.Logf("actID=%d", actID)

	mu.Lock()
	before := len(detailReplies)
	mu.Unlock()
	body, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID})
	p.write(pack.Pack(ACTIVITY_GET_DATA_CMD, body, uid))
	lim := time.Now().Add(15 * time.Second)
	var raw []byte
	for time.Now().Before(lim) && raw == nil {
		mu.Lock()
		if len(detailReplies) > before {
			raw = append([]byte{}, detailReplies[before]...)
		}
		mu.Unlock()
		if raw == nil {
			time.Sleep(300 * time.Millisecond)
		}
	}
	if raw == nil {
		t.Fatal("no 21003 answer")
	}
	var rsp proto.RspGetActivityDetail
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	t.Logf("ret=%d", rsp.GetRet())
	ac := rsp.GetActivity()
	if ac == nil {
		t.Fatal("no activity payload")
	}
	if dc := ac.GetDetailConfig(); dc != nil && dc.GetCommonConfig() != nil {
		cc := dc.GetCommonConfig()
		for _, c := range cc.GetConditionConfig() {
			t.Logf("COND cfg id=%d type=%d param=%v desc=%q rewards=%q",
				c.GetConditionId(), c.GetType(), c.GetParam(), c.GetDesc(), c.GetRewards())
		}
	} else {
		t.Logf("NO conditionConfig in detail — raw=%x", raw[:min(len(raw), 300)])
	}
	for _, cd := range ac.GetConditionDataList() {
		t.Logf("COND data id=%d value=%q got=%v", cd.GetConditionId(), cd.GetValue(), cd.GetIsGetReward())
	}
	// energy + realm/power via playerData? XiYou playerData rides SYNC, not
	// here — just log the raw tail so nothing is lost.
	t.Logf("commonData fields: mallBuyCount=%d", len(ac.GetMallBuyCountList()))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
