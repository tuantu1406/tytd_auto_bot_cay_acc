//go:build live

package tytd_go

// READ-then-play live probe for remoteRebornTrial ("Thử Luyện", systemOpenId
// 172). Static protocol (bundle Protocol enum lines 211970-211983): requests
// 217200..217251, answers = request − 200000 (17200..17251, hex 0x4330..).
// The subpackage JS is not local, so field shapes must be captured live:
//   A. 21003 detail → 03eb: uniqueConfig = RebornTrialConfigSync (thresholds/energy)
//   B. ENTER_ACTIVITY(217200)/ENTER(217201)/GROUP_INFO(217202)/GET_RED_POINT(217236)
//      with the house body {1: activityId}
//   C. TYTD_RBT_PLAY=1 additionally fires AUTO_EXPLORE_REQ(217251) — the
//      client's own free auto-loop; USE_MAGIC/USE_ITEM/SKIP (item ops) are
//      NEVER sent (permanent item ban [[tytd-item-ban]]).
// Raw answers are saved to /tmp/cscal/rbt_<opcode>.bin for offline decode.
//
//	TYTD_LIVE=1 TYTD_RBT_PROBE=1 [TYTD_RBT_PLAY=1] go test -count=1 -tags "sqlcipher live" -run TestRebornTrialProbe -v -timeout 240s .

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
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

const rbtType int32 = 172

func rbtBody(id int32) []byte {
	b := protowire.AppendTag(nil, 1, protowire.VarintType)
	return protowire.AppendVarint(b, uint64(uint32(id)))
}

func TestRebornTrialProbe(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_PROBE") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_PROBE=1")
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
	db, _ := gorm.Open(sqlite.Open("file:rbtprobe?mode=memory&cache=shared"), &gorm.Config{})
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
	ans := make(chan [2]string, 4096)
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

	var actID int32
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && actID == 0 {
		time.Sleep(500 * time.Millisecond)
		now := time.Now().UnixMilli()
		for _, e := range p.ActList.byType(rbtType) {
			if e.BeginTime <= now && now < e.EndTime {
				actID = e.ActivityID
			}
		}
	}
	if actID == 0 {
		t.Skip("REBORN_TRIAL (type 172) not open for this account right now")
	}
	t.Logf("type 172 open: activityId=%d", actID)

	// drain collects answers while we fire the sequence.
	pending := map[string][][]byte{}
	drain := func(dur time.Duration) {
		end := time.Now().Add(dur)
		for time.Now().Before(end) {
			select {
			case a := <-ans:
				n, err := strconv.ParseInt(a[0], 16, 32)
				if err != nil {
					continue
				}
				if n == 0x3eb || (n >= 0x4330 && n <= 0x4363) { // detail + 17200..17251
					raw, _ := hex.DecodeString(a[1])
					pending[a[0]] = append(pending[a[0]], raw)
				}
			default:
				time.Sleep(50 * time.Millisecond)
			}
		}
	}

	send := func(dec int32, body []byte, tag string) {
		cmd := fmt.Sprintf("%08x", dec)
		t.Logf("SEND %s (%d / %s)", tag, dec, cmd)
		p.writeChan <- pack.Pack(cmd, body, uid)
		drain(3 * time.Second)
	}

	// A. config detail
	send(21003, rbtBody(actID), "ACTIVITY_GET_DATA")
	// B. read-ish sequence
	send(217200, rbtBody(actID), "ENTER_ACTIVITY")
	send(217201, rbtBody(actID), "ENTER")
	send(217202, rbtBody(actID), "GROUP_INFO")
	send(217236, rbtBody(actID), "GET_RED_POINT")
	send(217210, rbtBody(actID), "UNION_SCORE")
	send(217226, rbtBody(actID), "GUESS_DATA")
	body2 := func(f2 uint64) []byte {
		b := rbtBody(actID)
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		return protowire.AppendVarint(b, f2)
	}
	if os.Getenv("TYTD_RBT_PLAY") != "" {
		send(217251, rbtBody(actID), "AUTO_EXPLORE")
		send(217205, rbtBody(actID), "EXPLORE")
	}
	if os.Getenv("TYTD_RBT_ACT") != "" {
		send(217200, rbtBody(actID), "ENTER_ACTIVITY#2")
		send(217201, rbtBody(actID), "ENTER")
		send(217203, rbtBody(actID), "WORSHIP")
		send(217230, rbtBody(actID), "BATTLE_ENTER")
		// AUTO_EXPLORE with the client's DEFAULT settingItems (event->settings)
		auto := rbtBody(actID)
		for _, ev := range []struct {
			e  uint64
			sv []uint64
		}{
			{1, []uint64{1, 1, 1, 0}}, {2, []uint64{1, 1, 1, 0}}, {3, []uint64{1, 1, 1}},
			{4, []uint64{1, 1, 1}}, {5, []uint64{1, 1, 1}}, {6, []uint64{0, 2}}, {7, []uint64{0, 2}},
			{20, []uint64{0}}, {21, []uint64{0}}, {22, []uint64{0, 0}},
		} {
			item := protowire.AppendTag(nil, 1, protowire.VarintType)
			item = protowire.AppendVarint(item, ev.e)
			for _, s := range ev.sv {
				item = protowire.AppendTag(item, 2, protowire.VarintType)
				item = protowire.AppendVarint(item, s)
			}
			auto = protowire.AppendTag(auto, 2, protowire.BytesType)
			auto = protowire.AppendBytes(auto, item)
		}
		send(217251, auto, "AUTO_EXPLORE#defaults")
		send(217205, rbtBody(actID), "EXPLORE#after")
		send(217201, rbtBody(actID), "ENTER#3")
	}
	if os.Getenv("TYTD_RBT_ENTER") != "" {
		for x := 1; x <= 4; x++ { // f7=[1..4]: try each trial as {act, id}
			send(217201, body2(uint64(x)), fmt.Sprintf("ENTER#gate=%d", x))
			send(217202, body2(uint64(x)), fmt.Sprintf("GROUP_INFO#gate=%d", x))
		}
		send(217251, body2(1), "AUTO_EXPLORE#f2=1")
		send(217200, rbtBody(actID), "ENTER_ACTIVITY#after")
	}

	for op, list := range pending {
		for i, raw := range list {
			var sb []byte
			mrWalk(raw, 0, &sb)
			t.Logf("ANSWER %s #%d (%dB):\n%s", op, i, len(raw), string(sb))
			os.WriteFile(fmt.Sprintf("/tmp/cscal/rbt_%s_%d.bin", op, i), raw, 0o644)
		}
	}
	t.Logf("captured opcodes: %d", len(pending))
}
