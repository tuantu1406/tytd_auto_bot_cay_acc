//go:build live

package tytd_go

// Live Đan Giới (Linh Điền / Elixir) probe (owner 2026-10-16). Three jobs,
// ALL free by the owner-confirmed model (plants self-grow; water is a regen
// pool that is NEVER worth hoarding — "tưới lúc nào cũng được"):
//
//	a. save the raw 8aae frame to testdata/dangioi_8aae.hex (golden corpus for
//	   TestDanGioiGoldenEnterHome),
//	b. lock land semantics: ENTER → WATER oneKey → ENTER again, diff every
//	   land field — which fields prove "was watered"/growth progress,
//	c. PLANT oneKey with isPlantElves=true on whatever qualifies and re-pull —
//	   proves the Dược Linh tick rides the request (server caps 30/day).
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 go test -tags "sqlcipher live" -count=1 -run TestDanGioiProbeLive -v -timeout 240s .

import (
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
)

func TestDanGioiProbeLive(t *testing.T) {
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
	all := make(chan [2]string, 4096)
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

	// call waits the answer type, discarding the auth firehose until it sees
	// it — so it never confuses an ENTER push from the login burst.
	call := func(cmd, ansType string, body []byte, tag string) []byte {
		// Consume the login firehose first (3s), then send.
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
			t.Fatalf("%s: 8aae does not decode as ElixirEnterHomeResp: %v", tag, err)
		}
		return &rsp
	}
	landLine := func(l *proto.ElixirLandInfo) string {
		return fmt.Sprintf("st=%d g=%d lv=%d t=%d award=%d recv=%d elves=%d canElves=%d rob=%v",
			l.GetState(), l.GetGrassId(), l.GetGrassLv(), l.GetRoundStartTime(),
			l.GetCanAwardRound(), l.GetHadReceiveRound(), l.GetElvesNum(), l.GetCanAwardElvesNum(), l.GetCanRob())
	}
	dump := func(rsp *proto.ElixirEnterHomeResp, tag string) map[int32]string {
		m := map[int32]string{}
		t.Logf("%s: ret=%d water=%d bucket=%d elvesOpenOff=%v lands=%d",
			tag, rsp.GetRet(), rsp.GetUserInfo().GetWater(), rsp.GetUserInfo().GetWaterBucket(),
			rsp.GetUserInfo().GetElvesOpenOff(), len(rsp.GetLands()))
		ids := []int32{}
		for _, l := range rsp.GetLands() {
			m[l.GetLandId()] = landLine(l)
			ids = append(ids, l.GetLandId())
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, i := range ids[:min(10, len(ids))] {
			t.Logf("  lot %2d: %s", i, m[i])
		}
		return m
	}

	r1 := enter("ENTER#1")
	if r1 == nil {
		t.Fatal("no 8aae at all")
	}
	// golden corpus (first capture wins)
	if _, err := os.Stat("testdata/dangioi_8aae.hex"); err != nil {
		if err := os.MkdirAll("testdata", 0o755); err == nil {
			b, _ := pb.Marshal(r1)
			_ = os.WriteFile("testdata/dangioi_8aae.hex", []byte(hex.EncodeToString(b)), 0o644)
			t.Log("saved testdata/dangioi_8aae.hex")
		}
	}
	m1 := dump(r1, "BEFORE")

	gb, _ := pb.Marshal(&proto.ElixirGetGrassReq{OneKey: pb.Bool(true)})
	if gRaw := call(S_ELIXIR_GET_GRASS, SERVER_ELIXIR_GET, gb, "GET oneKey"); gRaw != nil {
		var gr proto.ElixirGetGrassResp
		if err := pb.Unmarshal(gRaw, &gr); err != nil {
			t.Fatalf("8ab2 does not decode as ElixirGetGrassResp: %v", err)
		}
		t.Logf("GET ret=%d land=%d grassInfo=%d landRewards=%d",
			gr.GetRet(), len(gr.GetLand()), len(gr.GetGrassInfo()), len(gr.GetLandRewards()))
	}

	wb, _ := pb.Marshal(&proto.ElixirWaterGrassReq{OneKey: pb.Bool(true)})
	wRaw := call(S_ELIXIR_WATER_GRASS, SERVER_ELIXIR_WATER, wb, "WATER oneKey")
	if wRaw != nil {
		var wr proto.ElixirWaterGrassResp
		if pb.Unmarshal(wRaw, &wr) == nil {
			t.Logf("WATER ret=%d lands=%d water_left=%d", wr.GetRet(), len(wr.GetLand()), wr.GetUserInfo().GetWater())
			for _, l := range wr.GetLand()[:min(6, len(wr.GetLand()))] {
				t.Logf("  wet lot %2d: %s", l.GetLandId(), landLine(l))
			}
		}
	}
	r2 := enter("ENTER#2")
	m2 := map[int32]string{}
	if r2 != nil {
		m2 = dump(r2, "AFTER-WATER")
		for id, before := range m1 {
			if after, ok := m2[id]; ok && after != before {
				t.Logf("WATER-CHANGED lot %2d:\n  before %s\n  after  %s", id, before, after)
			}
		}
	}

	pb1, _ := pb.Marshal(&proto.ElixirPlantGrassReq{OneKey: pb.Bool(true), IsPlantElves: pb.Bool(true)})
	if pRaw := call(S_ELIXIR_PLANT, SERVER_ELIXIR_PLANT, pb1, "PLANT oneKey+elves"); pRaw != nil {
		var pr proto.ElixirPlantGrassResp
		if pb.Unmarshal(pRaw, &pr) == nil {
			t.Logf("PLANT ret=%d lands=%d", pr.GetRet(), len(pr.GetLand()))
			for _, l := range pr.GetLand()[:min(6, len(pr.GetLand()))] {
				t.Logf("  new lot %2d: %s", l.GetLandId(), landLine(l))
			}
		}
	}
	r3 := enter("ENTER#3")
	if r3 != nil {
		m3 := dump(r3, "AFTER-PLANT")
		for id, before := range m2 {
			if after, ok := m3[id]; ok && after != before {
				t.Logf("PLANT-CHANGED lot %2d: %s → %s", id, before, after)
			}
		}
	}

	// d. Dược Linh panel (235550 → 8ade): empty req body, read-only. Locks
	// the /30 counter path the owner asked about.
	if eRaw := call(S_ELIXIR_ELVES_ENTER, SERVER_ELIXIR_ELVES_INFO, []byte{}, "ELVES ENTER"); eRaw != nil {
		var er proto.ElixirElvesEnterResp
		if err := pb.Unmarshal(eRaw, &er); err != nil {
			t.Fatalf("8ade does not decode as ElixirElvesEnterResp: %v", err)
		}
		pd := er.GetElvesPlayerData()
		t.Logf("ELVES ret=%d todayGet=%d todaySteal=%d", er.GetRet(), pd.GetTodayGetCount(), pd.GetTodayStealCount())
	}
}
