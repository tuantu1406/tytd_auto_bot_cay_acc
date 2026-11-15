//go:build live

package tytd_go

// READ-ONLY live probe for remoteMarbleRebate (systemOpenId MARBLE_REBATE =
// 343, UI reuses the LimitTimeGroupBuy package). Static findings so far:
//   - S_LIMIT_TIME_GROUP_BUY_SYNC_PROGRESS_DATA_REQ   = 212003 (0x33c23)
//   - S_LIMIT_TIME_GROUP_BUY_GET_CONDITION_REWARD_REQ = 212004 (0x33c24) — CLAIM,
//     never sent by this probe
//   - playerData flows inside the GENERIC activity common-data sync (same
//     framework as Pharmacy/PupilRogue); type-343 dispatch just stores it and
//     emits MARBLE_REBATE_PLAYER_DATA_SYNC.
// This probe only: finds the type-343 instance in the pushed ActList, sends
// 212003 with a candidate {1:activityId} body, and dumps the answer tree with
// a raw protobuf walker. No claims, no writes beyond the read-ish sync.
//
//	TYTD_LIVE=1 TYTD_MR_PROBE=1 go test -count=1 -tags "sqlcipher live" -run TestMarbleRebateProbe -v -timeout 180s .

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	mrReqCmd  = "00033c23" // 212003 sync progress
	mrRepType = "00002ee3" // 12003 expected answer opcode
)

// mrWalk prints a protobuf wire tree up to depth d.
func mrWalk(b []byte, d int, sb *[]byte) {
	for len(b) > 0 {
		num, wt, n := protowire.ConsumeTag(b)
		if n < 0 {
			*sb = append(*sb, []byte("…truncated")...)
			return
		}
		b = b[n:]
		indent := ""
		for i := 0; i < d; i++ {
			indent += "  "
		}
		switch wt {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return
			}
			b = b[n:]
			*sb = append(*sb, fmt.Sprintf("%sf%d=%d\n", indent, num, v)...)
		case protowire.Fixed32Type:
			v, n := protowire.ConsumeFixed32(b)
			if n < 0 {
				return
			}
			b = b[n:]
			*sb = append(*sb, fmt.Sprintf("%sf%d=32:%d\n", indent, num, v)...)
		case protowire.Fixed64Type:
			v, n := protowire.ConsumeFixed64(b)
			if n < 0 {
				return
			}
			b = b[n:]
			*sb = append(*sb, fmt.Sprintf("%sf%d=64:%d\n", indent, num, v)...)
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return
			}
			b = b[n:]
			// nested message? try recursion, else hex/string
			if d < 4 && len(v) > 0 {
				var probe []byte
				mrWalk(v, d+1, &probe)
				if len(probe) > 0 && string(probe[2*len(probe)/3:]) != "…truncated" {
					*sb = append(*sb, fmt.Sprintf("%sf%d {\n", indent, num)...)
					*sb = append(*sb, probe...)
					*sb = append(*sb, indent+"}\n"...)
					continue
				}
			}
			*sb = append(*sb, fmt.Sprintf("%sf%d bytes=%q\n", indent, num, string(v))...)
		default:
			*sb = append(*sb, []byte("…bad wiretype")...)
			return
		}
	}
}

func TestMarbleRebateProbe(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_MR_PROBE") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_MR_PROBE=1")
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
	db, _ := gorm.Open(sqlite.Open("file:mrprobe?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{}, &model.SystemConfig{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel, Notify: true,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 64)}
	go func() {
		for f := range p.writeChan {
			if err := conn.WriteMessage(2, f); err != nil {
				return
			}
		}
	}()
	ans := make(chan [2]string, 64) // opcode, payload hex of any small answer
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
			switch ty {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			}
			select {
			case ans <- [2]string{ty, hex.EncodeToString(d)}:
			default:
			}
		}
	}()
	defer func() { conn.Close(); <-done }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// Drain the activity list, then hunt for type 343.
	full := make(chan [2]string, 4096) // log every answer for offline decode
	go func() {
		for a := range ans {
			select {
			case full <- a:
			default:
			}
		}
	}()
	var actID int32
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && actID == 0 {
		time.Sleep(500 * time.Millisecond)
		now := time.Now().UnixMilli()
		for _, e := range p.ActList.byType(343) {
			if e.BeginTime <= now && now < e.EndTime {
				actID = e.ActivityID
			}
		}
	}
	if actID == 0 {
		t.Skip("MARBLE_REBATE (type 343) not open for this account right now")
	}
	t.Logf("type 343 open: activityId=%d", actID)

	body := protowire.AppendTag(nil, 1, protowire.VarintType)
	body = protowire.AppendVarint(body, uint64(uint32(actID)))
	// 1) 21003 ACTIVITY_GET_DATA: full config incl. conditionDataList
	// (thresholds + rewards) and playerData. Client sends version "0" = full.
	dq := protowire.AppendTag(nil, 1, protowire.VarintType)
	dq = protowire.AppendVarint(dq, uint64(uint32(actID)))
	dq = protowire.AppendTag(dq, 2, protowire.BytesType)
	dq = protowire.AppendBytes(dq, []byte("0"))
	p.writeChan <- pack.Pack(ACTIVITY_GET_DATA_CMD, dq, uid)
	time.Sleep(5 * time.Second)
	// 2) 212003 dedicated sync
	p.writeChan <- pack.Pack(mrReqCmd, body, uid)

	if os.Getenv("TYTD_MR_CLAIM") != "" {
		body2 := func(f2 uint64) []byte {
			cb := protowire.AppendTag(nil, 1, protowire.VarintType)
			cb = protowire.AppendVarint(cb, uint64(uint32(actID)))
			if f2 != 0 {
				cb = protowire.AppendTag(cb, 2, protowire.VarintType)
				cb = protowire.AppendVarint(cb, f2)
			}
			return cb
		}
		// differential probes to separate "wrong schema" (silent) from
		// "state refused" (ret): ghost condition, then swapped fields.
		for c := 10001; c <= 10009; c++ {
			p.writeChan <- pack.Pack("00033c24", body2(uint64(c)), uid)
			time.Sleep(700 * time.Millisecond)
		}
		time.Sleep(3 * time.Second)
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case a := <-ans:
			if strings.TrimLeft(a[0], "0") == "2ee3" && a[1] != "" {
				raw, _ := hex.DecodeString(a[1])
				var sb []byte
				mrWalk(raw, 0, &sb)
				t.Logf("ANSWER %s (%d bytes):\n%s", a[0], len(raw), string(sb))
				return
			} else {
				select {
				case full <- a:
				default:
				}
			}
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	// dump everything we saw
	seen := map[string][]string{}
	for {
		select {
		case a := <-full:
			seen[a[0]] = append(seen[a[0]], a[1])
		default:
			goto out
		}
	}
out:
	for op, pl := range seen {
		for _, h := range pl {
			raw, _ := hex.DecodeString(h)
			var sb []byte
			mrWalk(raw, 0, &sb)
			t.Logf("FRAME %s (%dB)\n%s", op, len(raw), string(sb))
			if op == "03eb" && len(raw) > 1000 {
				os.WriteFile("/tmp/cscal/detail_03eb.bin", raw, 0o644)
			}
		}
	}
	t.Logf("total distinct opcodes: %d", len(seen))
}
