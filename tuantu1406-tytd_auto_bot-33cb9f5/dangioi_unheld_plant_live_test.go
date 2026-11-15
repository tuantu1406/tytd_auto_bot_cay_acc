//go:build live

package tytd_go

// UNHELD-PLANT probe (owner 2026-10-17 bug report: "acc đó không có hạt
// nhưng mày vẫn trồng, check chỗ dược linh"). <live-user>'s bag held NO 864xxx
// herbal tree at all (BAG-1 = only 860xxx/862xxx ordinary), yet the ladder
// tries the season trio 864024/23/22 UNCONDITIONALLY (elixirPlantTiers appends
// elixirSeasonTrio with no held-gate) and treats grassInfos as held even for
// num==0. Two questions, live packets only:
//
//	A. Does the bag hold ANY 864xxx with num>0? (If none, every ladder send is
//	   a plant of a tree the account does not own.)
//	B. Try PLANT the golden trio tree (864024) on one free lot and re-ENTER:
//	   state=2 grass=864024  → the server ACCEPTS planting an UNHELD tree, so
//	     the "still plants without seed" is REAL and the bot must gate locally;
//	   ret!=0 / lot stays 0 → the server refuses, the bot only *sends* junk
//	     (notify already suppressed by planted==0), still worth gating to stop
//	     the pointless packets.
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiUnheldPlantLive -v -timeout 300s .

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

func TestDanGioiUnheldPlantLive(t *testing.T) {
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

	first := enter("ENTER-1")
	if first == nil {
		t.Fatal("no ENTER")
	}
	// A. bag census: every herbal (864xxx) tree and its num, plus the golden
	// trio the ladder would try first.
	var herbStr []string
	anyHeldHerb := false
	for _, g := range first.GetUserInfo().GetGrassInfos() {
		if elixirIsHerb(g.GetGrassId()) {
			herbStr = append(herbStr, fmt.Sprintf("%d:num%d", g.GetGrassId(), g.GetNum()))
			if g.GetNum() > 0 {
				anyHeldHerb = true
			}
		}
	}
	sort.Strings(herbStr)
	// Season 8 = what this acc's passive 8ae9 folds live (golden 864022).
	const season = int32(8)
	trio := elixirSeasonTrio(season)
	t.Logf("A. bag herbal trees: %v | anyHeldHerb(num>0)=%v | ladder would try trio %v (golden=%d)",
		herbStr, anyHeldHerb, trio, elixirGoldenGrass(season))

	// B. plant the tier-3 trio tree (which the acc does NOT hold) on one free lot.
	var empty []int32
	for _, l := range first.GetLands() {
		if l.GetState() != 2 {
			empty = append(empty, l.GetLandId())
		}
	}
	if len(empty) == 0 {
		t.Fatal("no empty lot to probe")
	}
	unheld := trio[2] // tier 3 = best = what the ladder tries first
	pb1, _ := pb.Marshal(&proto.ElixirPlantGrassReq{
		GrassInfo:    []*proto.ElixirPlantGrassInfo{{LandId: pb.Int32(empty[0]), GrassId: pb.Int32(unheld)}},
		IsPlantElves: pb.Bool(true),
	})
	raw := call(S_ELIXIR_PLANT, SERVER_ELIXIR_PLANT, pb1, fmt.Sprintf("PLANT-unheld-%d", unheld))
	ret, respLands := int32(-1), -1
	if raw != nil {
		var rsp proto.ElixirPlantGrassResp
		pb.Unmarshal(raw, &rsp)
		ret, respLands = rsp.GetRet(), len(rsp.GetLand())
	}
	t.Logf("B. PLANT land %d tree %d (acc does NOT hold it) → ret=%d resp.land=%d", empty[0], unheld, ret, respLands)

	third := enter("ENTER-3")
	if third != nil {
		for _, l := range third.GetLands() {
			if l.GetLandId() == empty[0] {
				t.Logf("ENTER-3 probed lot %d: state=%d grass=%d → %s",
					empty[0], l.GetState(), l.GetGrassId(),
					map[bool]string{true: "SERVER ACCEPTED UNHELD TREE (bug real — gate locally)",
						false: "server refused / lot empty (bot only sends junk)"}[l.GetGrassId() == unheld])
			}
		}
	}
}
