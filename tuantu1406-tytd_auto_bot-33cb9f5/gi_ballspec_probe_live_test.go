//go:build live

package tytd_go

// GOD ISLAND BALL-PANEL SPEC PROBE (owner 2026-10-19: "ngưỡng này vào bóng
// linh lực sẽ thấy") — the client's spiritual-ball panel displays the
// revive HP threshold, so it MUST ride a field our proto never modelled.
// The ball panel's answer is 1aaa (206826): our Go model stops at field 5
// while the decoded protocol lists up to 12 (fightValue, time,
// updatePowerCount, lastUpdatePowerTime, deadWeakEffect, peakFruitNum,
// fruitPoolMax); INFO_LOAD 1aa4 likewise hides deadWeakEffect=19 and
// params=28. This probe logs in (<live-user> by default), pulls 206826 +
// 206820 for every open 68/333 variant, and WALKS THE RAW WIRE of each
// answer top-level field by field (number, wire type, value / printable
// string / hex) so the threshold is read from the server's own bytes —
// never guessed. Read-only: no item use, no ball drink, no orders.
//
//	TYTD_LIVE=1 TYTD_GI_BALLDUMP=1 \
//	  TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestGIBallSpecProbeLive -v -timeout 300s .

import (
	"encoding/hex"
	"fmt"
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

// wireDump walks one protobuf payload at the TOP level only and prints
// every field as "f<num>(wt<type>)=value" — unknown-to-our-proto fields
// included, which is the whole point.
func wireDump(b []byte) string {
	var out []byte
	for len(b) > 0 {
		key, n := pbConsumeVarint(b)
		if n <= 0 {
			break
		}
		b = b[n:]
		num, wt := uint64(key)>>3, uint64(key)&7
		switch wt {
		case 0: // varint
			v, n := pbConsumeVarint(b)
			if n <= 0 {
				return string(out) + " <truncated>"
			}
			b = b[n:]
			out = append(out, fmt.Sprintf("f%d=%d ", num, v)...)
		case 1: // 64-bit
			if len(b) < 8 {
				return string(out) + " <truncated>"
			}
			var v uint64
			for i := 0; i < 8; i++ {
				v |= uint64(b[i]) << (8 * i)
			}
			b = b[8:]
			out = append(out, fmt.Sprintf("f%d=<64b %d> ", num, v)...)
		case 2: // length-delimited
			l, n := pbConsumeVarint(b)
			if n <= 0 || uint64(len(b)) < uint64(n)+l {
				return string(out) + " <truncated>"
			}
			b = b[n:]
			val := b[:l]
			b = b[l:]
			if s := wireText(val); s != "" {
				out = append(out, fmt.Sprintf("f%d=\"%s\" ", num, s)...)
			} else {
				sample := val
				if len(sample) > 48 {
					sample = sample[:48]
				}
				out = append(out, fmt.Sprintf("f%d{len %d hex %s} ", num, l, hex.EncodeToString(sample))...)
			}
		case 5: // 32-bit
			if len(b) < 4 {
				return string(out) + " <truncated>"
			}
			v := uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24
			b = b[4:]
			out = append(out, fmt.Sprintf("f%d=<32b %d> ", num, v)...)
		default:
			return string(out) + fmt.Sprintf(" <bad wt %d>", wt)
		}
	}
	return string(out)
}

func pbConsumeVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

// printable renders b as a string when it looks like text (UTF-8 runes and
// digits/punct only), else "" so the caller hex-dumps it.
func wireText(b []byte) string {
	if len(b) == 0 || len(b) > 256 {
		return ""
	}
	for _, c := range b {
		if c < 0x20 || c == 0x7f {
			return ""
		}
	}
	return string(b)
}

func TestGIBallSpecProbeLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_GI_BALLDUMP") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_GI_BALLDUMP=1")
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

	writeChan := make(chan []byte, 256)
	go func() {
		for f := range writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	type probeFrame struct {
		ty   string
		data []byte
	}
	var mu sync.Mutex
	var frames []probeFrame
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
			cp := append([]byte(nil), d...)
			mu.Lock()
			frames = append(frames, probeFrame{ty, cp})
			mu.Unlock()
		}
	}()
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for range tk.C {
			select {
			case writeChan <- pack.Pack(PING_CMD, []byte{}, uid):
			default:
			}
		}
	}()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)
	time.Sleep(5 * time.Second)

	// Discover open 68/333 variants from the login activity push.
	var active []int32
	seenID := map[int32]bool{}
	mu.Lock()
	for _, f := range frames {
		var push proto.PushActivityList
		if pb.Unmarshal(f.data, &push) != nil {
			continue
		}
		for _, mc := range push.GetMainConfig() {
			if (mc.GetType() == giActivityTypeGodIsland || mc.GetType() == giActivityTypePeakIsland) &&
				mc.GetActivityId() != 0 && !seenID[mc.GetActivityId()] {
				seenID[mc.GetActivityId()] = true
				active = append(active, mc.GetActivityId())
				t.Logf("ACTIVITY: id=%d type=%d", mc.GetActivityId(), mc.GetType())
			}
		}
	}
	mu.Unlock()
	if len(active) == 0 {
		fmt.Printf("VERDICT-BALLSPEC: SKIP — no 68/333 activity announced for %s right now\n", user)
		return
	}

	// Pull the ball panel + game info + base info once per variant, then
	// give the server 3s per round-trip.
	for _, aid := range active {
		b, _ := pb.Marshal(&proto.GodIslandGameSpiritualBallInfoReqMsg{ActivityId: aid})
		writeChan <- pack.Pack(GOD_ISLAND_BALL_INFO_CMD, b, uid)
		b, _ = pb.Marshal(&proto.GodIslandGameInfoReqMsg{ActivityId: aid})
		writeChan <- pack.Pack(GOD_ISLAND_INFO_LOAD_CMD, b, uid)
		b, _ = pb.Marshal(&proto.GodIslandBaseInfoReqMsg{ActivityId: aid})
		writeChan <- pack.Pack(GOD_ISLAND_BASE_INFO_CMD, b, uid)
		time.Sleep(3 * time.Second)
	}

	// Dump every 1aaa / 1aa4 answer raw, newest first per activity.
	printed := 0
	mu.Lock()
	for _, f := range frames {
		switch f.ty {
		case SERVER_GOD_ISLAND_BALL_INFO:
			fmt.Printf("=== 1aaa BALL-PANEL raw (%d bytes) ===\n  %s\n  hex: %s\n",
				len(f.data), wireDump(f.data), hex.EncodeToString(f.data))
			printed++
		case SERVER_GOD_ISLAND_INFO_LOAD:
			var r proto.GodIslandGameInfoRespMsg
			ret := int32(-1)
			if pb.Unmarshal(f.data, &r) == nil {
				ret = r.GetRet()
			}
			fmt.Printf("=== 1aa4 INFO_LOAD raw ret=%d (%d bytes) ===\n  %s\n",
				ret, len(f.data), wireDump(f.data))
			printed++
		case SERVER_GOD_ISLAND_BASE_INFO:
			fmt.Printf("=== 1aa8 BASE raw (%d bytes) ===\n  %s\n", len(f.data), wireDump(f.data))
			printed++
		}
	}
	mu.Unlock()
	if printed == 0 {
		fmt.Printf("VERDICT-BALLSPEC: requests sent for %v but NO answers arrived — residency gate? rerun while the account is on the island\n", active)
		return
	}
	fmt.Printf("VERDICT-BALLSPEC: %d frame(s) dumped — walk fields 6..12 of 1aaa and 19/28 of 1aa4 for the revive threshold the ball panel draws\n", printed)
}
