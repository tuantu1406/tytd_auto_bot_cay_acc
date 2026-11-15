//go:build live

package tytd_go

// GOD ISLAND SHOP-DUMP PROBE (owner 2026-10-20: "tao thấy chưa tự mua
// gói") — the auto-buy loop reads its table from the 21003 answer
// (03eb RspGetActivityDetail: commonConfig.mallConfig + mallBuyCountList).
// This probe logs in (<live-user> by default), asks 21003 for every open
// 68/333 variant and prints EVERY mall row EXACTLY as the server sends it
// (id, price, buyLimit, param) together with the verdict of the engine's
// own gate rbtMallAuto — so "why did the bot never buy" is answered from
// real bytes: empty table (server refuses / mall rides another message),
// ret≠0, or rows that all classify as recharge (limit 0 — correctly
// never bought under [[tytd-item-ban]]). Read-only: 21003 is a free
// config read; NO 21005 is ever sent.
//
//	TYTD_LIVE=1 TYTD_GI_SHOPDUMP=1 \
//	  TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestGIShopDumpProbeLive -v -timeout 300s .

import (
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

func TestGIShopDumpProbeLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_GI_SHOPDUMP") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_GI_SHOPDUMP=1")
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
		fmt.Printf("VERDICT-SHOPDUMP: SKIP — no 68/333 activity announced for %s right now\n", user)
		return
	}

	// Ask the activity detail once per variant (free config read) and give
	// the server 3s per round-trip.
	for _, aid := range active {
		b, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: aid})
		writeChan <- pack.Pack(ACTIVITY_GET_DATA_CMD, b, uid)
		time.Sleep(3 * time.Second)
	}

	// Walk every 03eb answer and replay the engine's gate on each row.
	printed := 0
	mu.Lock()
	for _, f := range frames {
		if f.ty != SERVER_ACTIVITY_DETAIL {
			continue
		}
		var resp proto.RspGetActivityDetail
		if pb.Unmarshal(f.data, &resp) != nil {
			fmt.Printf("=== 03eb UNPARSEABLE (%d bytes) ===\n", len(f.data))
			printed++
			continue
		}
		act := resp.GetActivity()
		fmt.Printf("=== 03eb act=%d ret=%d ===\n", act.GetActivityId(), resp.GetRet())
		if resp.GetRet() != 0 {
			fmt.Printf("  REFUSED — engine logs this since 2026-10-20; shop table unreachable\n")
			printed++
			continue
		}
		for _, bc := range resp.GetActivity().GetMallBuyCountList() {
			fmt.Printf("  bought: mallId=%d count=%d\n", bc.GetMallId(), bc.GetCount())
		}
		rows := act.GetDetailConfig().GetCommonConfig().GetMallConfig()
		if len(rows) == 0 {
			fmt.Printf("  NO mallConfig rows — the 21003 answer carries no shop (giShopSweep would ask forever)\n")
		}
		for _, mc := range rows {
			mt := mc.GetMallTempMsg()
			if mt == nil {
				continue
			}
			limit, ad, jade := rbtMallAuto(mt)
			verdict := "KHÔNG MUA (nạp tiền)"
			if limit > 0 {
				verdict = fmt.Sprintf("MUA (limit %d", limit)
				if ad {
					verdict += ", ad"
				}
				if jade {
					verdict += ", tiên ngọc"
				}
				verdict += ")"
			}
			fmt.Printf("  row id=%d price=%q buyLimit=%d param=%q → %s\n",
				mt.GetId(), mt.GetPrice(), mt.GetBuyLimit(), mt.GetParam(), verdict)
		}
		printed++
	}
	mu.Unlock()
	if printed == 0 {
		fmt.Printf("VERDICT-SHOPDUMP: asked 21003 for %v but NO 03eb arrived — residency gate? rerun while an account is resident\n", active)
		return
	}
	fmt.Printf("VERDICT-SHOPDUMP: %d answer(s) dumped — rows with MUA verdict must be bought by giShopSweep within one 10-min sweep; KHÔNG MUA everywhere = nothing is buyable (correct under item-ban)\n", printed)
}
