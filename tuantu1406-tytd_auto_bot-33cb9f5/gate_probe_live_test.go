//go:build live

package tytd_go

import (
	"encoding/hex"
	"os"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	pb "google.golang.org/protobuf/proto"

	"github.com/gorilla/websocket"
)

// TestLiveGateProbe answers the two deployment questions with one login:
// (1) bug1 — with Cửu U absent in the real game, does the server still keep a
// type-346 ActList row "active", and does 209570 ANSWER (ret/isOpen) or stay
// silent? Silent means our OpenKnown never flips and the list-fallback keeps
// the card shown — a real code bug, not a stale binary.
// (2) bug2 — is a type-199 row "active" on this server at all (kunlun
// mapstream would honestly be idle / 400 the sub otherwise)?
//
// TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveGateProbe -v -timeout 180s .
func TestLiveGateProbe(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveUser = liveUserEnv(t)
		livePass = livePassEnv(t)
	)
	liveServer := 710564
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			liveServer = n
		}
	}
	passportToken, err := request.LoginGPassport(liveUser, livePass)
	if err != nil {
		t.Fatalf("LoginGPassport: %v", err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatalf("LoginFuntap: %v", err)
	}
	gameURL, err := request.GetServerURL(userID, liveServer)
	if err != nil {
		t.Fatalf("GetServerURL: %v", err)
	}
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
	if err != nil {
		t.Fatalf("GetWSKey: %v", err)
	}
	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()
	wrCh := make(chan []byte, 16)
	go func() {
		for f := range wrCh {
			if conn.WriteMessage(2, f) != nil {
				return
			}
		}
	}()
	defer close(wrCh)
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	wrCh <- pack.Pack(AUTH_CMD, authMSG, uid)

	got := make(chan [2]string, 64) // msgType -> raw hex
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		conn.SetReadLimit(8 << 20)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if len(data) < 18 {
				continue
			}
			mt := hex.EncodeToString(data[8:10])
			payload := append([]byte(nil), data[18:]...)
			t.Logf("frame mt=%s len=%d", mt, len(data))
			got <- [2]string{mt, hex.EncodeToString(payload)}
		}
	}()

	var list []byte
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case g := <-got:
			if g[0] == SERVER_HOLY_LAND_SCHEDULE_PUSH {
				b, _ := hex.DecodeString(g[1])
				var push proto.HolyLandBattleTimeStampsDataSyncMsg
				if pb.Unmarshal(b, &push) == nil {
					for i, ts := range push.GetScheduleTimestamp() {
						t.Logf("HL-2567 ts[%d]=%s", i, time.UnixMilli(ts).In(ictZone).Format("01-02 15:04:05"))
					}
				}
			}
			if g[0] == SERVER_ACTIVITY_LIST {
				b, _ := hex.DecodeString(g[1])
				list = b
				deadline = time.Now().Add(3 * time.Second) // keep draining, but near the end
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if list == nil {
		t.Fatal("no activity list push observed")
	}
	var push proto.PushActivityList
	if err := pb.Unmarshal(list, &push); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	now := time.Now()
	hl346, ku199 := 0, 0
	for _, c := range push.GetMainConfig() {
		if typ := c.GetType(); typ == 346 || typ == 199 {
			e := entryFromConfig(c)
			t.Logf("%s row id=%d status=%s begin=%s end=%s", map[int32]string{346: "HL-346", 199: "KU-199"}[typ],
				c.GetActivityId(), e.status(now),
				time.UnixMilli(c.GetBeginTime()).In(ictZone).Format("01-02 15:04"),
				time.UnixMilli(c.GetEndTime()).In(ictZone).Format("01-02 15:04"))
			if typ == 346 {
				hl346++
			} else {
				ku199++
			}
		}
	}
	if hl346 == 0 {
		t.Log("VERDICT bug1: NO type-346 row at all (matches 2026-10-03 truth)")
	}
	if ku199 == 0 {
		t.Log("VERDICT bug2: no type-199 row → kunlun stream would be idle")
	}

	// Ask the schedule: answer or silence?
	wrCh <- pack.Pack(HOLY_LAND_SCHEDULE_CMD, []byte{}, uid)
	answered := false
	deadline = time.Now().Add(10 * time.Second)
	for !answered && time.Now().Before(deadline) {
		select {
		case g := <-got:
			// ONLY 2562 carries the schedule answer. Reading 2567 (the
			// timestamp push) with this message yields zero-defaults —
			// that mis-read is what produced a bogus "isOpen=false"
			// verdict on 2026-10-05 while the event was open.
			if g[0] == SERVER_HOLY_LAND_SCHEDULE {
				b, _ := hex.DecodeString(g[1])
				var resp proto.HolyLandBattleApplyDataSyncMsg
				if pb.Unmarshal(b, &resp) == nil {
					t.Logf("209570→2562: ret=%d isOpen=%v roundId=%d", resp.GetRet(), resp.GetIsOpen(), resp.GetRoundId())
					answered = true
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !answered {
		t.Log("VERDICT bug1: SERVER NEVER ANSWERED 209570 → OpenKnown can never flip; fallback must go")
	}
}
