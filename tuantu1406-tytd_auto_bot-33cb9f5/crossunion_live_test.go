//go:build live

package tytd_go

// One-shot read-only probe: find which ACTIVE activity carries the
// conditionConfig rows with conditionId >= 20000 (the cross-server union
// recharge milestones claimed via 21045) and print its condition data.
//
//	TYTD_LIVE=1 TYTD_RBT_CROSS=1 go test -tags "sqlcipher live" -count=1 -run TestCrossUnionCondLive -v -timeout 180s .

import (
	"context"
	"encoding/hex"
	"os"
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

func TestCrossUnionCondLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_CROSS") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_CROSS=1")
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
	db, _ := gorm.Open(sqlite.Open("file:rbtcross?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid)}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256)}
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	all := make(chan [2]string, 8192)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			if ty == SERVER_ACTIVITY_LIST || ty == SERVER_ACTIVITY_LIST_UPDATE {
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			}
			select {
			case all <- [2]string{ty, hex.EncodeToString(d)}:
			default:
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-done }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(p.ActList.snapshot()) == 0 {
		time.Sleep(300 * time.Millisecond)
	}
	now := time.Now()
	var actIDs []int32
	for _, e := range p.ActList.snapshot() {
		if e.status(now) == "active" {
			actIDs = append(actIDs, e.ActivityID)
		}
	}
	for _, e := range p.ActList.snapshot() {
		t.Logf("ACTLIST id=%d type=%d status=%s", e.ActivityID, e.Type, e.status(now))
	}
	t.Logf("active activities: %v", actIDs)
	for _, id := range actIDs {
		req, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: id})
		p.writeChan <- pack.Pack(ACTIVITY_GET_DATA_CMD, req, uid)
		time.Sleep(150 * time.Millisecond)
	}
	seen := map[int32]bool{}
	ends := time.Now().Add(45 * time.Second)
	for time.Now().Before(ends) && len(seen) < len(actIDs) {
		select {
		case a := <-all:
			if a[0] != SERVER_ACTIVITY_DETAIL {
				continue
			}
			raw, _ := hex.DecodeString(a[1])
			var rsp proto.RspGetActivityDetail
			if pb.Unmarshal(raw, &rsp) != nil {
				continue
			}
			id := rsp.GetActivity().GetActivityId()
			if seen[id] {
				continue
			}
			seen[id] = true
			hits := 0
			ids := []string{}
			for _, cd := range rsp.GetActivity().GetDetailConfig().GetCommonConfig().GetConditionConfig() {
				ids = append(ids, strconv.Itoa(int(cd.GetConditionId())))
			}
			if len(ids) > 0 {
				t.Logf("ACT %d type=%d condIDs: %v", id, rsp.GetActivity().GetDetailConfig().GetCommonConfig().GetMainConfig().GetType(), ids)
			}
			for _, cd := range rsp.GetActivity().GetDetailConfig().GetCommonConfig().GetConditionConfig() {
				if cd.GetConditionId() >= 20000 {
					hits++
					t.Logf("ACT %d COND cfg condId=%d type=%d param=%v rewards=%q desc=%q",
						id, cd.GetConditionId(), cd.GetType(), cd.GetParam(), cd.GetRewards(), cd.GetDesc())
				}
			}
			for _, cd := range rsp.GetActivity().GetConditionDataList() {
				if cd.GetConditionId() >= 20000 || hits > 0 {
					t.Logf("ACT %d COND data condId=%d value=%q got=%v", id, cd.GetConditionId(), cd.GetValue(), cd.GetIsGetReward())
				}
			}
			if hits == 0 {
				t.Logf("ACT %d: no 2xxxx conditions (%d answers seen)", id, len(seen))
			}
		case <-time.After(300 * time.Millisecond):
		}
	}
	t.Logf("done: %d/%d activities answered", len(seen), len(actIDs))

	// Claim 21004 for a READY milestone and dump EVERY answer type seen for
	// 4s — the cond sweep needs the real answer opcode.
	// (cond ids from the 172 answer were logged earlier; pick 10052/10001)
	// Does a silent 21004 still apply? claim 10052 then re-pull 21003.
	{
		desk, _ := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(9062433), ConditionId: pb.Int32(0)})
		p.writeChan <- pack.Pack("0000521f", desk, uid) // 21047 open-to-desk
		t.Log("sent 21047 open desk")
		time.Sleep(1 * time.Second)
		body, _ := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(9062433), ConditionId: pb.Int32(10052)})
		p.writeChan <- pack.Pack(ACTIVITY_COND_REWARD_CMD, body, uid)
		t.Log("sent 21004 cond 10052 (act set); waiting 3s; re-pulling detail…")
		time.Sleep(3 * time.Second)
		req, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: 9062433})
		p.writeChan <- pack.Pack(ACTIVITY_GET_DATA_CMD, req, uid)
		stop := time.Now().Add(6 * time.Second)
		for time.Now().Before(stop) {
			select {
			case a := <-all:
				if a[0] != SERVER_ACTIVITY_DETAIL {
					continue
				}
				raw, _ := hex.DecodeString(a[1])
				var rsp proto.RspGetActivityDetail
				if pb.Unmarshal(raw, &rsp) != nil || rsp.GetActivity().GetActivityId() != 9062433 {
					continue
				}
				for _, cd := range rsp.GetActivity().GetConditionDataList() {
					if cd.GetConditionId() == 10052 || cd.GetConditionId() == 10001 {
						t.Logf("AFTER claim: cond %d value=%q got=%v", cd.GetConditionId(), cd.GetValue(), cd.GetIsGetReward())
					}
				}
				break
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	// Type-84 rows for 9062433: config vs server data vs claim attempts.
	{
		req, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: 9062433})
		p.writeChan <- pack.Pack(ACTIVITY_GET_DATA_CMD, req, uid)
		stop := time.Now().Add(6 * time.Second)
		gotData := false
		for time.Now().Before(stop) && !gotData {
			select {
			case a := <-all:
				if a[0] != SERVER_ACTIVITY_DETAIL {
					continue
				}
				raw, _ := hex.DecodeString(a[1])
				var rsp proto.RspGetActivityDetail
				if pb.Unmarshal(raw, &rsp) != nil || rsp.GetActivity().GetActivityId() != 9062433 {
					continue
				}
				vals := map[int32][2]string{}
				for _, cd := range rsp.GetActivity().GetConditionDataList() {
					vals[cd.GetConditionId()] = [2]string{cd.GetValue(), fmtBool(cd.GetIsGetReward())}
				}
				for _, cd := range rsp.GetActivity().GetDetailConfig().GetCommonConfig().GetConditionConfig() {
					if cd.GetType() == 84 {
						v := vals[cd.GetConditionId()]
						t.Logf("T84 cond %d param=%s rewards=%s value=%q got=%v", cd.GetConditionId(), cd.GetParam(), cd.GetRewards(), v[0], v[1])
					}
				}
				// 20xxx rows data?
				for id, v := range vals {
					if id >= 20000 {
						t.Logf("DATA2xxxx %d value=%q got=%v", id, v[0], v[1])
					}
				}
				gotData = true
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
}

func fmtBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func truncHex(h string) string {
	if len(h) > 80 {
		return h[:80] + "…"
	}
	return h
}
