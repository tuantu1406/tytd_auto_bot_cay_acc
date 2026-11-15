//go:build live

package tytd_go

// OPCODE-TRANSPORT probe for Tiên Cảnh Vị Ương (WeYound, activity type 186).
// The decoded protobuf/WeYound asset's CodeEnum lists resp codes 218677..218699
// (218682 Worship_Resp etc) — but the RebornTrial live probe proved the pack
// transport strips 200000 (217200 → answer "4330"). Which scheme the SERVER
// actually answers on decides the SERVER_WE_YOUND_* hex table. This probe
// fires ONLY read-only / free ops (ENTER_ACTIVITY, ENTER, TRAP_SEEK, WORSHIP,
// BONUS, GET_BONUS_RESULT, UNION_GIFT_LIST — nothing buys, spends or donates)
// and dumps every frame that arrives after each request with its transport
// hex + first payload bytes, so the table is derived from the wire, not
// guessed.
//
//	TYTD_LIVE=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestTienCanhOpcodeLive -v -timeout 300s .

import (
	"encoding/hex"
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

func TestTienCanhOpcodeLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1")
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
	write := make(chan []byte, 256)
	go func() {
		for f := range write {
			_ = conn.WriteMessage(2, f)
		}
	}()
	all := make(chan [2]string, 16384)
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
			select {
			case all <- [2]string{ty, hex.EncodeToString(d)}:
			default:
			}
		}
	}()
	kwStop := make(chan struct{})
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-kwStop:
				return
			case <-tk.C:
				write <- pack.Pack(PING_CMD, []byte{}, uid)
			}
		}
	}()
	defer close(kwStop)
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	write <- pack.Pack(AUTH_CMD, authMSG, uid)

	// ---- find the live type-186 activity
	var actID int32
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) && actID == 0 {
		select {
		case a := <-all:
			if a[0] == SERVER_ACTIVITY_LIST {
				raw, _ := hex.DecodeString(a[1])
				var push proto.PushActivityList
				if pb.Unmarshal(raw, &push) == nil {
					now := time.Now()
					for _, c := range push.GetMainConfig() {
						if c.GetType() == 186 && entryFromConfig(c).status(now) == "active" {
							actID = c.GetActivityId()
						}
					}
				}
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if actID == 0 {
		t.Fatal("no active type-186 activity")
	}
	t.Logf("live 186 activityId = %d", actID)

	drain := func(label string) {
		end := time.Now().Add(2500 * time.Millisecond)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				// log anything plausibly a 186-series answer (0x48xx band)
				if len(a[0]) == 4 && a[0][:2] == "48" || len(a[0]) == 4 && a[0][:2] == "49" {
					t.Logf("[%s] frame %s payload=%s", label, a[0], a[1])
				}
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	send := func(cmd string, body []byte) {
		write <- pack.Pack(cmd, body, uid)
	}
	// {1: activityId} via a tiny hand encoder (varint field 1)
	mk := func() []byte {
		v := uint64(actID)
		out := []byte{0x08}
		for v >= 0x80 {
			out = append(out, byte(v)|0x80)
			v >>= 7
		}
		return append(out, byte(v))
	}
	body := mk()

	t.Log("=== ENTER_ACTIVITY 000355e8 (218600)")
	send("000355e8", body)
	drain("EA")
	t.Log("=== ENTER 000355e9 (218601) {1:act,2:false}")
	send("000355e9", body)
	drain("ENTER")
	t.Log("=== TRAP_SEEK 000355ee (218606)")
	send("000355ee", body)
	drain("TRAPSEEK")
	t.Log("=== WORSHIP 000355f4 (218612)")
	send("000355f4", body)
	drain("WORSHIP")
	t.Log("=== BONUS 000355fc (218620)")
	send("000355fc", body)
	drain("BONUS")
	t.Log("=== GET_BONUS_RESULT 000355fd (218621)")
	send("000355fd", body)
	drain("BONUSRES")
	t.Log("=== UNION_GIFT_LIST 0003560c (218636)")
	send("0003560c", body)
	drain("GIFTLIST")
	t.Log("done — compare each frame hex with req−200000 (CodeEnum tells the truth if WORSHIP answered 48bf=18687, req−200000 rules if 48fc−…)")
}
