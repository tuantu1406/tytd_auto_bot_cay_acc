//go:build live

package tytd_go

// MULTI-ROUND TRUTH probe (owner 2026-10-18: "thu 36 lô chín NHIỀU LẦN (cách
// nhau 15 phút) rồi mới trồng — cây LÊN CẤP thì 1 lần trồng thu nhiều lần,
// code mày sai"). The engine's clear pass assumes a fully-collected plant
// AUTO-EMPTIES (live 2026-10-17 saw {canAward=1 hadReceive=1} → state=0). If
// a levelled tree instead answers canAwardRound=N>1, the lot STAYS state=2
// after each GET, waiting for the next round — and if DEL refuses such a
// lot, the field never empties: harvest re-fires every round (~15 min) and
// replant only happens when something else breaks the logjam. This probe
// settles it on the LIVE account, one lot at a time:
//
//	ENTER dump (every lot: state/grass/canAward/hadReceive/roundStart)
//	→ GET one ripe lot → ENTER the SAME lot again (state? roundStart moved?
//	  canAward left?) → DEL it → ENTER again (did the server let it go?)
//	→ final full dump + raw DEL answer bytes.
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiMultiRoundLive -v -timeout 300s .

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

func TestDanGioiMultiRoundLive(t *testing.T) {
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
		raw := call(S_ELIXIR_ENTER, SERVER_DANGIOI_INFO, func() []byte {
			b, _ := pb.Marshal(&proto.ElixirEnterHomeReq{PlayerId: pb.Int64(int64(uid))})
			return b
		}(), tag)
		if raw == nil {
			return nil
		}
		var rsp proto.ElixirEnterHomeResp
		if pb.Unmarshal(raw, &rsp) != nil {
			return nil
		}
		return &rsp
	}
	dump := func(rsp *proto.ElixirEnterHomeResp, tag string) map[int32]*proto.ElixirLandInfo {
		byID := map[int32]*proto.ElixirLandInfo{}
		if rsp == nil {
			t.Logf("%s: ENTER FAILED", tag)
			return byID
		}
		byState := map[int32]int{}
		ripe := []int32{}
		for _, l := range rsp.GetLands() {
			byState[l.GetState()]++
			byID[l.GetLandId()] = l
			if l.GetCanAwardRound() > 0 {
				ripe = append(ripe, l.GetLandId())
			}
		}
		var age int64 = -1
		if len(rsp.GetLands()) > 0 {
			age = (time.Now().UnixMilli() - rsp.GetLands()[0].GetRoundStartTime()) / 1000
		}
		t.Logf("%s: states=%v ripe(canAward>0)=%d lot1_age=%ds | %d lots",
			tag, byState, len(ripe), age, len(rsp.GetLands()))
		for _, id := range ripe {
			l := byID[id]
			t.Logf("  RIPE L%d grass=%d lv=%d state=%d canAward=%d hadRecv=%d roundStart=%d elves=%d",
				id, l.GetGrassId(), l.GetGrassLv(), l.GetState(), l.GetCanAwardRound(),
				l.GetHadReceiveRound(), l.GetRoundStartTime(), l.GetElvesNum())
		}
		return byID
	}

	rsp0 := enter("ENTER#0")
	if rsp0 == nil {
		t.Fatal("no ENTER")
	}
	before := dump(rsp0, "BEFORE")
	var target int32
	ripeIDs := make([]int32, 0, 8)
	for id, l := range before {
		if l.GetCanAwardRound() > 0 {
			ripeIDs = append(ripeIDs, id)
		}
	}
	sort.Slice(ripeIDs, func(i, j int) bool { return ripeIDs[i] < ripeIDs[j] })
	if len(ripeIDs) > 0 {
		target = ripeIDs[0]
	}

	// ---- GET one ripe lot, then WATCH that exact lot ------------------------
	if target == 0 {
		t.Log("no ripe lot to probe — dumping all lots so the state shape is still on record")
	} else {
		b, _ := pb.Marshal(&proto.ElixirGetGrassReq{LandId: []int32{target}})
		raw := call(S_ELIXIR_GET_GRASS, SERVER_ELIXIR_GET, b, fmt.Sprintf("GET L%d", target))
		if raw != nil {
			var gr proto.ElixirGetGrassResp
			if pb.Unmarshal(raw, &gr) == nil {
				t.Logf("GET L%d answer: ret=%d landEcho=%d", target, gr.GetRet(), len(gr.GetLand()))
				for _, l := range gr.GetLand() {
					t.Logf("  echo L%d state=%d canAward=%d hadRecv=%d roundStart=%d",
						l.GetLandId(), l.GetState(), l.GetCanAwardRound(), l.GetHadReceiveRound(), l.GetRoundStartTime())
				}
			}
		}
		time.Sleep(600 * time.Millisecond)
		rsp1 := enter("ENTER#1 (after GET)")
		after := dump(rsp1, "AFTER-GET")
		if l0, ok0 := before[target]; ok0 {
			if l1, ok1 := after[target]; ok1 {
				t.Logf(">>> LOT %d POST-GET VERDICT: state %d→%d | canAward %d→%d | hadRecv %d→%d | roundStart %d→%d",
					target, l0.GetState(), l1.GetState(), l0.GetCanAwardRound(), l1.GetCanAwardRound(),
					l0.GetHadReceiveRound(), l1.GetHadReceiveRound(), l0.GetRoundStartTime(), l1.GetRoundStartTime())
				switch {
				case l1.GetState() == 0:
					t.Log(">>> server AUTO-EMPTIED the lot — single-round shape, clear pass sees it as empty")
				case l1.GetState() == 2 && l1.GetCanAwardRound() > 0:
					t.Log(">>> STILL COLLECTABLE after GET — GET did not take the round!")
				case l1.GetState() == 2:
					t.Log(">>> LOT STAYS PLANTED (multi-round tree) — the clear pass MUST DEL it")
				}
			}
		}
		// ---- DEL that lot: does the server actually let it go? -------------
		b2, _ := pb.Marshal(&proto.ElixirDelPlantGrassReq{LandId: []int32{target}})
		raw2 := call(S_ELIXIR_DEL, SERVER_ELIXIR_DEL, b2, fmt.Sprintf("DEL L%d", target))
		if raw2 != nil {
			t.Logf("DEL L%d raw answer: %x", target, raw2)
			var dr proto.ElixirDelPlantGrassResp
			if pb.Unmarshal(raw2, &dr) == nil {
				t.Logf("DEL L%d: ret=%d landEcho=%d", target, dr.GetRet(), len(dr.GetLandInfo()))
			}
		}
		time.Sleep(600 * time.Millisecond)
		dump(enter("ENTER#2 (after DEL)"), "AFTER-DEL")
	}

	// ---- bag + elves context (why fillEmpty may refuse to plant) -----------
	if pd := func() *proto.ElixirElvesPlayerData {
		raw := call(S_ELIXIR_ELVES_ENTER, SERVER_ELIXIR_ELVES_INFO, []byte{}, "ELVES")
		if raw == nil {
			return nil
		}
		var er proto.ElixirElvesEnterResp
		if pb.Unmarshal(raw, &er) != nil {
			return nil
		}
		return er.GetElvesPlayerData()
	}(); pd != nil {
		t.Logf("elves todayGet=%d (cap 30)", pd.GetTodayGetCount())
	}
}
