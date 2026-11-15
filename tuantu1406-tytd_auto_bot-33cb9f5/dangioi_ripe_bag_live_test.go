//go:build live

package tytd_go

// Ripe-semantics + bag-consumption probe (owner 2026-10-17 bug report:
// "cây chín mày không thu hoạch" + "không kiểm tra túi có hạt trước khi
// trồng"). Two questions, answered with live packets only:
//
//	A. WHICH field means RIPE? Dump every lot's {state, canAwardRound,
//	   hadReceiveRound, roundStartTime-vs-now}. Then GET every lot with
//	   canAwardRound>=1 (even ones the shipped gate can>had would skip) and
//	   re-ENTER: hadReceiveRound jumping + reward arriving = canAward is a
//	   PENDING counter (ripe ⇔ canAward>0), proving the shipped
//	   canAward>hadReceive gate starves every previously-harvested lot.
//
//	B. DOES PLANT CONSUME THE BAG? Record grassInfos.num per tree, plant ONE
//	   free lot with a tree that has num>0, re-ENTER → num decreased = the
//	   bag IS the planting ledger (owner's "hạt" = số cây trong túi); also
//	   try planting a tree with num==0 and see if the server refuses
//	   (ret≠0) or no-ops (ret=0, nothing planted) — either way gate locally.
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710547 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiRipeBagLive -v -timeout 300s .

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

func TestDanGioiRipeBagLive(t *testing.T) {
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
	t0 := time.Now().UnixMilli()
	dump := func(tag string, rsp *proto.ElixirEnterHomeResp) {
		byState := map[int32]int{}
		var pending, starved []int32
		for _, l := range rsp.GetLands() {
			byState[l.GetState()]++
			if l.GetCanAwardRound() >= 1 {
				pending = append(pending, l.GetLandId())
				if l.GetCanAwardRound() <= l.GetHadReceiveRound() {
					starved = append(starved, l.GetLandId()) // shipped gate would SKIP these
				}
			}
		}
		t.Logf("%s: states=%v | lots with canAward>=1: %v | of which STARVED by shipped can>had gate: %v",
			tag, byState, pending, starved)
		var sample []string
		for i, l := range rsp.GetLands() {
			if i >= 8 {
				break
			}
			sample = append(sample, fmt.Sprintf("L%d{s%d g%d a%d h%d t%+ds}",
				l.GetLandId(), l.GetState(), l.GetGrassId(), l.GetCanAwardRound(), l.GetHadReceiveRound(),
				(l.GetRoundStartTime()-t0)/1000))
		}
		t.Logf("%s sample: %v", tag, sample)
	}
	find := func(rsp *proto.ElixirEnterHomeResp, id int32) *proto.ElixirLandInfo {
		for _, l := range rsp.GetLands() {
			if l.GetLandId() == id {
				return l
			}
		}
		return nil
	}
	bag := func(ui *proto.ElixirUserInfo) map[int32]*proto.ElixirGrassInfo {
		m := map[int32]*proto.ElixirGrassInfo{}
		for _, g := range ui.GetGrassInfos() {
			m[g.GetGrassId()] = g
		}
		return m
	}

	first := enter("ENTER-1")
	if first == nil {
		t.Fatal("no ENTER")
	}
	dump("ENTER-1", first)
	bag1 := bag(first.GetUserInfo())
	var ids []int32
	for _, g := range first.GetUserInfo().GetGrassInfos() {
		ids = append(ids, g.GetGrassId())
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	var bagStr []string
	for _, id := range ids {
		if bag1[id].GetNum() > 0 {
			bagStr = append(bagStr, fmt.Sprintf("%d:%d", id, bag1[id].GetNum()))
		}
	}
	t.Logf("BAG-1 (num>0 trees): %v", bagStr)

	// A. harvest every lot with canAward>=1, including starved ones.
	var toGet []int32
	for _, l := range first.GetLands() {
		if l.GetCanAwardRound() >= 1 {
			toGet = append(toGet, l.GetLandId())
		}
	}
	for len(toGet) > 0 {
		n := 6
		if n > len(toGet) {
			n = len(toGet)
		}
		b, _ := pb.Marshal(&proto.ElixirGetGrassReq{LandId: toGet[:n]})
		raw := call(S_ELIXIR_GET_GRASS, SERVER_ELIXIR_GET, b, fmt.Sprintf("GET%v", toGet[:n]))
		if raw != nil {
			var rsp proto.ElixirGetGrassResp
			pb.Unmarshal(raw, &rsp)
			t.Logf("GET %v ret=%d land=%d grass=%d landRewards=%d", toGet[:n], rsp.GetRet(),
				len(rsp.GetLand()), len(rsp.GetGrassInfo()), len(rsp.GetLandRewards()))
		}
		toGet = toGet[n:]
	}

	second := enter("ENTER-2")
	if second == nil {
		t.Fatal("no ENTER-2")
	}
	dump("ENTER-2", second)
	for _, l := range second.GetLands() {
		if b1 := find(first, l.GetLandId()); b1 != nil && l.GetCanAwardRound() >= 1 {
			t.Logf("AFTER-GET L%d: canAward %d→%d hadRecv %d→%d",
				l.GetLandId(), b1.GetCanAwardRound(), l.GetCanAwardRound(),
				b1.GetHadReceiveRound(), l.GetHadReceiveRound())
		}
	}

	// B. plant one free lot with a num>0 tree, one with a num==0 tree.
	var empty []int32
	for _, l := range second.GetLands() {
		if l.GetState() != 2 {
			empty = append(empty, l.GetLandId())
		}
	}
	bag2 := bag(second.GetUserInfo())
	var has, lacks int32
	for _, id := range ids { // desc: newest first
		if id >= 864000 && id < 865000 {
			if bag2[id] != nil && bag2[id].GetNum() > 0 {
				has = id
			} else if elixirHerbNames[id] != "" {
				lacks = id
			}
		}
	}
	t.Logf("empty lots: %d; pick num>0 herb=%d, num==0 herb=%d", len(empty), has, lacks)
	type plantCase struct {
		name       string
		land, tree int32
	}
	var cases []plantCase
	if len(empty) >= 2 && has != 0 {
		cases = append(cases, plantCase{"plant-num>0", empty[0], has})
	}
	if len(empty) >= 2 && lacks != 0 {
		cases = append(cases, plantCase{"plant-num==0", empty[1], lacks})
	}
	for _, c := range cases {
		nBefore := int32(-1)
		if g := bag2[c.tree]; g != nil {
			nBefore = g.GetNum()
		}
		pb1, _ := pb.Marshal(&proto.ElixirPlantGrassReq{
			GrassInfo:    []*proto.ElixirPlantGrassInfo{{LandId: pb.Int32(c.land), GrassId: pb.Int32(c.tree)}},
			IsPlantElves: pb.Bool(true),
		})
		raw := call(S_ELIXIR_PLANT, SERVER_ELIXIR_PLANT, pb1, c.name)
		ret := int32(-1)
		if raw != nil {
			var rsp proto.ElixirPlantGrassResp
			pb.Unmarshal(raw, &rsp)
			ret = rsp.GetRet()
		}
		t.Logf("%s: land %d tree %d (bag had %d) → ret=%d", c.name, c.land, c.tree, nBefore, ret)
	}
	third := enter("ENTER-3")
	if third != nil {
		bag3 := bag(third.GetUserInfo())
		for _, c := range cases {
			n3 := int32(-1)
			if g := bag3[c.tree]; g != nil {
				n3 = g.GetNum()
			}
			nBefore := int32(-1)
			if g := bag2[c.tree]; g != nil {
				nBefore = g.GetNum()
			}
			if l := find(third, c.land); l != nil {
				t.Logf("%s RESULT: land %d state=%d grass=%d | bag %d: %d → %d",
					c.name, c.land, l.GetState(), l.GetGrassId(), c.tree, nBefore, n3)
			}
		}
	}
}
