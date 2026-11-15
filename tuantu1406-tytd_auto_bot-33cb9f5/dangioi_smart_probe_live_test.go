//go:build live

package tytd_go

// Smart-plant semantics probe (owner 2026-10-16): the oneKey refusals decode
// to real error texts (LanguageWordDB): 35520 = "không có Thẻ Đặc Quyền"
// (one-key ops may be privilege-card gated!) and 35568 = "chức năng tưới đã
// bị gỡ". The OLD bot planted/harvested PER-LAND and worked (test acc shows
// 23 received rounds), so the smart-plant engine must use explicit landIds.
// This probe, on <live-user>@710564 (owner-authorized):
//	1. ENTER → land state histogram (find the EMPTY state number),
//	2. ELVES panel → today's /30,
//	3. explicit per-land GET on lots where canAwardRound>hadReceiveRound
//	   (proves per-land harvest rides no card),
//	4. explicit PLANT (one packet, grassInfo list, isPlantElves=true) on the
//	   EMPTY-state lots with the season-bonus grass 864001+3*(season-1) when
//	   the account holds its seeds, else the best owned 864xxx with seeds,
//	5. ENTER again → diff proves what "watered/planted/elves" look like.
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=... TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiSmartProbeLive -v -timeout 240s .

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

func TestDanGioiSmartProbeLive(t *testing.T) {
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

	season := int32(0)
	call := func(cmd, ansType string, body []byte, tag string) []byte {
		end := time.Now().Add(3 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if a[0] == SERVER_ELIXIR_DRAW_SEASON { // 8ae9 login push
					var s proto.ElixirElvesDrawSeasonSync
					if raw, _ := hex.DecodeString(a[1]); pb.Unmarshal(raw, &s) == nil {
						season = s.GetSeason()
					}
				}
			case <-time.After(150 * time.Millisecond):
			}
		}
		if cmd != "" {
			write <- pack.Pack(cmd, body, uid)
		}
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
			t.Fatalf("%s decode: %v", tag, err)
		}
		return &rsp
	}
	landLine := func(l *proto.ElixirLandInfo) string {
		return fmt.Sprintf("st=%d g=%d lv=%d t=%d award=%d recv=%d elves=%d canElves=%d",
			l.GetState(), l.GetGrassId(), l.GetGrassLv(), l.GetRoundStartTime(),
			l.GetCanAwardRound(), l.GetHadReceiveRound(), l.GetElvesNum(), l.GetCanAwardElvesNum())
	}
	dump := func(rsp *proto.ElixirEnterHomeResp, tag string) map[int32]string {
		m := map[int32]string{}
		hist := map[int32]int{}
		for _, l := range rsp.GetLands() {
			m[l.GetLandId()] = landLine(l)
			hist[l.GetState()]++
		}
		t.Logf("%s: season=%d water=%d · states %v (%d lots)", tag, season,
			rsp.GetUserInfo().GetWater(), hist, len(m))
		return m
	}

	r1 := enter("ENTER#1")
	if r1 == nil {
		t.Fatal("no 8aae")
	}
	m1 := dump(r1, "BEFORE")
	call("", SERVER_ELIXIR_ELVES_INFO, []byte{}, "ELVES#1")

	seeds := map[int32]int32{} // owned herb seeds
	for _, g := range r1.GetUserInfo().GetGrassInfos() {
		if elixirIsHerb(g.GetGrassId()) && g.GetNum() > 0 {
			seeds[g.GetGrassId()] = g.GetNum()
		}
	}
	golden := elixirGoldenGrass(season)
	if _, ok := seeds[golden]; !ok && len(seeds) > 0 {
		b := golden
		golden = 0
		for id := range seeds {
			if id > b {
				b = id
			}
		}
		golden = b
		t.Logf("no golden %d seeds → best owned %d", b, golden)
	}
	t.Logf("PLANT TARGET grass=%d seeds=%d (golden=%d)", golden, seeds[golden], golden)

	// explicit per-land GET on up to 3 harvestable lots
	var ripe []*proto.ElixirLandInfo
	for _, l := range r1.GetLands() {
		if l.GetCanAwardRound() > l.GetHadReceiveRound() {
			ripe = append(ripe, l)
		}
	}
	t.Logf("ripe lots: %d", len(ripe))
	for i, l := range ripe {
		if i >= 3 {
			break
		}
		gb, _ := pb.Marshal(&proto.ElixirGetGrassReq{LandId: []int32{l.GetLandId()}})
		if gRaw := call(S_ELIXIR_GET_GRASS, SERVER_ELIXIR_GET, gb, fmt.Sprintf("GET land%d", l.GetLandId())); gRaw != nil {
			var gr proto.ElixirGetGrassResp
			if pb.Unmarshal(gRaw, &gr) == nil {
				t.Logf("  GET ret=%d rewards=%d elves=%d", gr.GetRet(), len(gr.GetLandRewards()),
					func() int {
						n := 0
						for _, x := range gr.GetLand() {
							n += int(x.GetCanAwardElvesNum())
						}
						return n
					}())
			}
		}
	}

	// explicit PLANT (one packet) on EMPTY-state lots (state 0 or 1 — whatever
	// exists that is not growing=2). If none, report (all lots growing).
	if golden != 0 {
		var infos []*proto.ElixirPlantGrassInfo
		for _, l := range r1.GetLands() {
			if l.GetState() != 2 && len(infos) < 12 {
				infos = append(infos, &proto.ElixirPlantGrassInfo{
					LandId: pb.Int32(l.GetLandId()), GrassId: pb.Int32(golden)})
			}
		}
		if len(infos) == 0 {
			t.Log("no non-growing lots to plant on (all state=2)")
		} else {
			pb1, _ := pb.Marshal(&proto.ElixirPlantGrassReq{GrassInfo: infos, IsPlantElves: pb.Bool(true)})
			if pRaw := call(S_ELIXIR_PLANT, SERVER_ELIXIR_PLANT, pb1, fmt.Sprintf("PLANT %d lots explicit", len(infos))); pRaw != nil {
				var pr proto.ElixirPlantGrassResp
				if pb.Unmarshal(pRaw, &pr) == nil {
					t.Logf("PLANT ret=%d lands=%d", pr.GetRet(), len(pr.GetLand()))
					for _, l := range pr.GetLand()[:min(6, len(pr.GetLand()))] {
						t.Logf("  new %2d: %s", l.GetLandId(), landLine(l))
					}
				}
			}
		}
	}
	r3 := enter("ENTER#3")
	if r3 != nil {
		m3 := dump(r3, "AFTER-PLANT")
		for id, before := range m1 {
			if after, ok := m3[id]; ok && after != before {
				t.Logf("CHANGED lot %2d:\n  before %s\n  after  %s", id, before, after)
			}
		}
	}
	call(S_ELIXIR_ELVES_ENTER, SERVER_ELIXIR_ELVES_INFO, []byte{}, "ELVES#2")
}
