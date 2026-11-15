//go:build live

package tytd_go

// SINGLE probe for the last unverified assumption of the auto-submit pass
// (owner directive 2026-10-17 "bot phải tự nộp đơn hàng"): the wire mapping
// ElixirPushOrderReq{type,index} — we ship type=1 (destiny board),
// index=0-based slot order. This probe logs every destiny order + the bag,
// then pushes ONLY orders the bag can already pay (so the only possible
// spend is the grass the order itself demands — the server's own deduction,
// exactly what the owner ordered) and reads each 8abb answer: ret, rewards,
// grass deltas. A ret=0 with rewards on a bag-paid slot ⇒ mapping confirmed.
// ret!=0 everywhere with a clearly-paid bag ⇒ type/index guess is wrong and
// elixirSubmitOrders must not ship hot.
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiPushOrderLive -v -timeout 300s .

import (
	"encoding/hex"
	"fmt"
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

func TestDanGioiPushOrderLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR=1")
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

	// Let the session settle: the proven probes wait for the 501 task
	// snapshot before the first gameplay pull.
	d501 := time.Now().Add(25 * time.Second)
	for time.Now().Before(d501) {
		select {
		case a := <-all:
			if a[0] == SERVER_TASK_DATA_SEND {
				goto entered
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
entered:
	// pull ENTER 8aae (orders + bag live inside it), retry twice
	var rsp *proto.ElixirEnterHomeResp
	for attempt := 0; attempt < 3 && rsp == nil; attempt++ {
		enterREQ, _ := pb.Marshal(&proto.ElixirEnterHomeReq{PlayerId: pb.Int64(int64(uid))})
		write <- pack.Pack(S_ELIXIR_ENTER, enterREQ, uid)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && rsp == nil {
			select {
			case a := <-all:
				if a[0] == SERVER_DANGIOI_INFO {
					raw, _ := hex.DecodeString(a[1])
					var r proto.ElixirEnterHomeResp
					if pb.Unmarshal(raw, &r) == nil && r.GetRet() == 0 {
						rsp = &r
					}
				}
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	if rsp == nil {
		t.Fatal("no 8aae")
	}
	bag := map[int32]int32{}
	for _, g := range rsp.GetUserInfo().GetGrassInfos() {
		bag[g.GetGrassId()] = g.GetNum()
	}
	orders := rsp.GetOrderInfo().GetDestinyOrderInfos()
	t.Logf("destiny orders: %d · todayDestiny=%d", len(orders), rsp.GetOrderInfo().GetTodayDestinyCompleteTimes())
	type plan struct {
		idx  int32
		want map[int32]int32
		pay  bool
	}
	var plans []plan
	for i, d := range orders {
		want := map[int32]int32{}
		for _, g := range d.GetGrassInfos() {
			want[g.GetGrassId()] += g.GetNum()
		}
		pay := len(want) > 0
		var parts []string
		for id, n := range want {
			have := bag[id]
			if have < n {
				pay = false
			}
			parts = append(parts, fmt.Sprintf("%d need %d have %d", id, n, have))
		}
		plans = append(plans, plan{int32(i), want, pay})
		t.Logf("  order[%d] destinyId=%d: %v (payable=%v)", i, d.GetDestinyId(), parts, pay)
	}
	push := func(typ, idx int32) {
		body, _ := pb.Marshal(&proto.ElixirPushOrderReq{Type: pb.Int32(typ), Index: pb.Int32(idx)})
		write <- pack.Pack(S_ELIXIR_PUSH_ORDER, body, uid)
		end := time.Now().Add(5 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if a[0] == SERVER_ELIXIR_PUSH_ORDER {
					raw, _ := hex.DecodeString(a[1])
					var rr proto.ElixirPushOrderResp
					if pb.Unmarshal(raw, &rr) == nil {
						var gs []string
						for _, g := range rr.GetGrassInfos() {
							gs = append(gs, fmt.Sprintf("%d:%d", g.GetGrassId(), g.GetNum()))
						}
						t.Logf("PUSH type=%d index=%d → ret=%d rewards=%q bag=%v", typ, idx, rr.GetRet(), rr.GetRewards(), gs)
					}
					return
				}
			case <-time.After(150 * time.Millisecond):
			}
		}
		t.Logf("PUSH type=%d index=%d → NO 8abb ANSWER (opcode wrong?)", typ, idx)
	}
	fired := 0
	for _, pl := range plans {
		if !pl.pay {
			continue
		}
		push(1, pl.idx)
		fired++
	}
	if fired == 0 && len(orders) > 0 {
		// nothing payable — probe the mapping with the first slot anyway
		// (server must refuse a short order; refusal still proves opcode+shape)
		push(1, 0)
	}
}
