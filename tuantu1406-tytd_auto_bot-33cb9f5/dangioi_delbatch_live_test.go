//go:build live

package tytd_go

// BATCH-cuốc live probe (owner 2026-10-19: "mày test cuốc batch có thành công
// không, tao mới tận tay test cuốc 1 ô thôi"). The unit fakes echo back
// whatever ids they're handed, so they NEVER prove the SERVER honours a
// multi-landId DEL. elixirDelLots packs up to elixirBatch(6) lots into ONE
// 00039811 packet {landId non-packed…, oneKey:false}. This probe reproduces
// EXACTLY that one-packet shape on a real account and counts how many lots
// ACTUALLY flip to state=0 on the follow-up ENTER:
//
//	1. ENTER → pick up to `want` growing lots (state=2), record ids.
//	2. GET each (collect any pending round — the single-lot probe proved DEL
//	   of a canAward>0 round is a silent no-op), then DEL the WHOLE batch in
//	   ONE packet (oneKey=false), asserting the request really carried >1 id.
//	3. ENTER again → of the requested ids, how many are state=0 grass=0?
//	   all freed → server honours batch DEL (elixirDelLots is safe as shipped);
//	   only the FIRST freed → server silently drops the tail → the batch loop
//	   must become per-lot (one DEL each).
//
//	Gentle by default: caps the batch at 3 lots so we don't raze the account.
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiDelBatchLive -v -timeout 300s .

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

func TestDanGioiDelBatchLive(t *testing.T) {
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
	want := 3
	if v := os.Getenv("TYTD_LIVE_BATCH"); v != "" {
		n, _ := strconv.Atoi(v)
		want = n
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
	all := make(chan [2]string, 8192)
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

	call := func(cmd, ansType string, body []byte, tag string) []byte {
		end := time.Now().Add(3 * time.Second)
		for time.Now().Before(end) {
			select {
			case <-all:
			case <-time.After(150 * time.Millisecond):
			}
		}
		write <- pack.Pack(cmd, body, uid)
		end = time.Now().Add(8 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if a[0] == ansType {
					raw, _ := hex.DecodeString(a[1])
					t.Logf("%s %s len=%d", tag, ansType, len(raw))
					return raw
				}
			case <-time.After(250 * time.Millisecond):
			}
		}
		t.Logf("!! %s: no %s answer", tag, ansType)
		return nil
	}
	enter := func(tag string) *proto.ElixirEnterHomeResp {
		b, _ := pb.Marshal(&proto.ElixirEnterHomeReq{PlayerId: pb.Int64(int64(uid))})
		raw := call(S_ELIXIR_ENTER, SERVER_DANGIOI_INFO, b, tag)
		if raw == nil {
			return nil
		}
		var rsp proto.ElixirEnterHomeResp
		if err := pb.Unmarshal(raw, &rsp); err != nil {
			t.Fatalf("%s: 8aae: %v", tag, err)
		}
		return &rsp
	}
	find := func(rsp *proto.ElixirEnterHomeResp, id int32) *proto.ElixirLandInfo {
		for _, l := range rsp.GetLands() {
			if l.GetLandId() == id {
				return l
			}
		}
		return nil
	}

	before := enter("ENTER-before")
	if before == nil {
		t.Fatal("no ENTER")
	}
	var ids []int32
	for _, l := range before.GetLands() {
		if l.GetState() == 2 && l.GetGrassId() != 0 {
			ids = append(ids, l.GetLandId())
			if len(ids) >= want {
				break
			}
		}
	}
	if len(ids) < 2 {
		t.Fatalf("only %d growing lots available — cannot prove BATCH del (need >=2); re-run later or TYTD_LIVE_BATCH", len(ids))
	}
	t.Logf("batch victim ids: %v", ids)
	for _, id := range ids {
		if l := find(before, id); l != nil {
			t.Logf("  lot %d: grass=%d lv=%d canAward=%d hadRecv=%d", id, l.GetGrassId(), l.GetGrassLv(), l.GetCanAwardRound(), l.GetHadReceiveRound())
		}
	}

	// GET each (mirror elixirDelLots: collect pending rounds first).
	for _, id := range ids {
		gb, _ := pb.Marshal(&proto.ElixirGetGrassReq{LandId: []int32{id}})
		call(S_ELIXIR_GET_GRASS, SERVER_ELIXIR_GET, gb, fmt.Sprintf("GET-%d", id))
	}

	// ONE DEL packet for the whole batch, exactly elixirDelLots' shape.
	db, _ := pb.Marshal(&proto.ElixirDelPlantGrassReq{LandId: ids, OneKey: pb.Bool(false)})
	t.Logf("DEL batch wire payload: %x (want all ids non-packed 08.. then 10 00)", db)
	raw := call(S_ELIXIR_DEL, SERVER_ELIXIR_DEL, db, "DEL-batch")
	if raw == nil {
		t.Fatal("8ad1 never answered")
	}
	var rsp proto.ElixirDelPlantGrassResp
	pb.Unmarshal(raw, &rsp)
	echoed := make(map[int32]bool)
	for _, l := range rsp.GetLandInfo() {
		echoed[l.GetLandId()] = true
	}
	t.Logf("DEL-batch ret=%d reward=%q echo=%d/%d (echoed ids %v)", rsp.GetRet(), rsp.GetReward(), len(rsp.GetLandInfo()), len(ids), echoed)

	after := enter("ENTER-after")
	if after == nil {
		t.Fatal("no ENTER after")
	}
	freed := 0
	for _, id := range ids {
		l := find(after, id)
		if l == nil {
			t.Logf("  lot %d: GONE from field", id)
			freed++
			continue
		}
		state := l.GetState()
		if state == 0 && l.GetGrassId() == 0 {
			freed++
			t.Logf("  lot %d: FREED (state=0 grass=0) echoed=%v", id, echoed[id])
		} else {
			t.Logf("  lot %d: STILL PLANTED state=%d grass=%d canAward=%d echoed=%v", id, state, l.GetGrassId(), l.GetCanAwardRound(), echoed[id])
		}
	}
	fmt.Printf("VERDICT-DEL-BATCH: requested=%d echo=%d freed-after-enter=%d → %s\n",
		len(ids), len(rsp.GetLandInfo()), freed,
		map[bool]string{true: "server honours batch DEL (elixirDelLots one-packet is SAFE)",
			false: "server DROPS the batch tail → elixirDelLots must DEL per-lot"}[freed == len(ids)])
}
