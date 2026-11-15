//go:build live

package tytd_go

// DEL (nhổ cây) semantics probe (owner 2026-10-16): before automating the
// pull, ground-truth S_ELIXIR_DEL_PLANT_GRASS 235537→8ad1 on a test acc:
//
//	1. ENTER-before: pick ONE growing lot (state=2), record its fields AND the
//	   account's whole grassInfos bag (num = trees held per grass).
//	2. explicit DEL that landId (NEVER oneKey — privilege card, 35520).
//	3. read 8ad1: ret / landInfo / reward string / userInfo diff:
//	   - ret!=0            → refused, nothing to automate;
//	   - ret==0, lot frees → the mechanic works; then compare grassInfos
//	     before/after: bag UNCHANGED ⇒ free pull (nothing consumed/refunded);
//	     bag GREW          ⇒ server refunds the tree; bag SHRANK ⇒ it CHARGES
//	     something → report exactly what and keep the loop hands-off.
//	4. re-ENTER: does the lot show empty, and is the old round history
//	   (hadReceiveRound) preserved?
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710547 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiDelProbeLive -v -timeout 300s .

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

func TestDanGioiDelProbeLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710547
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
	bag := func(ui *proto.ElixirUserInfo) map[int32]int32 {
		m := map[int32]int32{}
		for _, g := range ui.GetGrassInfos() {
			m[g.GetGrassId()] = g.GetNum()
		}
		return m
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
	bagBefore := bag(before.GetUserInfo())
	var victim *proto.ElixirLandInfo
	var growing int
	for _, l := range before.GetLands() {
		if l.GetState() == 2 && l.GetGrassId() != 0 {
			growing++
			// prefer a lot with an uncollected ripe round (canAward>0) — that
			// is the multi-round shape the auto-loop actually cuốc's.
			if victim == nil || (victim.GetCanAwardRound() == 0 && l.GetCanAwardRound() > 0) {
				victim = l
			}
		}
	}
	if victim == nil {
		t.Fatal("no growing lot to pull")
	}
	t.Logf("victim land %d: grass=%d lv=%d start=%d canAward=%d hadRecv=%d (growing lots: %d)",
		victim.GetLandId(), victim.GetGrassId(), victim.GetGrassLv(), victim.GetRoundStartTime(),
		victim.GetCanAwardRound(), victim.GetHadReceiveRound(), growing)

	// HYPOTHESIS (owner 2026-10-18 <live-user>): DEL of a lot with an
	// UNCOLLECTED ripe round (canAward>0) answers ret=0 with an EMPTY land
	// echo = silent no-op (the client always GETs before 铲除). So: GET the
	// victim first (collect the ripe round), then DEL — does it echo + free?
	if victim.GetCanAwardRound() > 0 {
		gb, _ := pb.Marshal(&proto.ElixirGetGrassReq{LandId: []int32{victim.GetLandId()}})
		got := call(S_ELIXIR_GET_GRASS, SERVER_ELIXIR_GET, gb, "GET-victim")
		if got != nil {
			var gr proto.ElixirGetGrassResp
			if pb.Unmarshal(got, &gr) == nil {
				t.Logf("GET-victim ret=%d landEcho=%d", gr.GetRet(), len(gr.GetLand()))
			}
		}
		if mid := enter("ENTER-mid (after GET)"); mid != nil {
			if m := find(mid, victim.GetLandId()); m != nil {
				t.Logf("victim AFTER-GET: state=%d canAward=%d hadRecv=%d", m.GetState(), m.GetCanAwardRound(), m.GetHadReceiveRound())
			}
		}
	}
	db, _ := pb.Marshal(&proto.ElixirDelPlantGrassReq{LandId: []int32{victim.GetLandId()}, OneKey: pb.Bool(false)})
	t.Logf("DEL wire payload: %x (client verbatim = 08 %02x 10 00)", db, victim.GetLandId())
	raw := call(S_ELIXIR_DEL, SERVER_ELIXIR_DEL, db, "DEL")
	if raw == nil {
		t.Fatal("8ad1 never answered")
	}
	var rsp proto.ElixirDelPlantGrassResp
	pb.Unmarshal(raw, &rsp)
	t.Logf("DEL ret=%d reward=%q landInfo=%d", rsp.GetRet(), rsp.GetReward(), len(rsp.GetLandInfo()))
	if rsp.GetUserInfo() != nil {
		bagAfter := bag(rsp.GetUserInfo())
		diffs := 0
		for id, n := range bagBefore {
			if bagAfter[id] != n {
				t.Logf("bag %d: %d → %d", id, n, bagAfter[id])
				diffs++
			}
		}
		if diffs == 0 {
			t.Logf("bag in DEL resp: UNCHANGED vs before")
		}
	}

	after := enter("ENTER-after")
	if after == nil {
		t.Fatal("no ENTER after")
	}
	v2 := find(after, victim.GetLandId())
	if v2 == nil {
		t.Fatal("victim vanished")
	}
	bagNow := bag(after.GetUserInfo())
	changed := 0
	for id, n := range bagBefore {
		if bagNow[id] != n {
			t.Logf("bag-after-enter %d: %d → %d", id, n, bagNow[id])
			changed++
		}
	}
	t.Logf("victim land %d AFTER: grass=%d lv=%d state=%d start=%d canAward=%d hadRecv=%d",
		v2.GetLandId(), v2.GetGrassId(), v2.GetGrassLv(), v2.GetState(),
		v2.GetRoundStartTime(), v2.GetCanAwardRound(), v2.GetHadReceiveRound())
	fmt.Printf("VERDICT-DEL: ret=%d reward=%q lot-after state=%d grass=%d hadRecv %d→%d bag-diffs=%d\n",
		rsp.GetRet(), rsp.GetReward(), v2.GetState(), v2.GetGrassId(),
		victim.GetHadReceiveRound(), v2.GetHadReceiveRound(), changed)
}
