//go:build live

package tytd_go

// HELD-PLANT control probe (pairs with TestDanGioiUnheldPlantLive, owner
// 2026-10-17 "acc đó không có hạt nhưng mày vẫn trồng"): planting a tree the
// bag does NOT hold answers ret=0 and silently no-ops (proved 2026-10-17 on
// <live-user>@710564 — PLANT 864024 → ret=0, lot stays {state=0 grass=0}).
// This probe pins the CONTROL half so the new local held-gate cannot mis-gate:
// plant a tree the bag DOES hold (an 860xxx task grass with num>0) on one
// free lot → must go state=2 grass=<tree>, and record whether num decreases
// (owner's model: planting is free, num = trees held, NOT consumed).
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiHeldPlantLive -v -timeout 300s .

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

func TestDanGioiHeldPlantLive(t *testing.T) {
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
	bag := func(ui *proto.ElixirUserInfo) map[int32]int32 {
		m := map[int32]int32{}
		for _, g := range ui.GetGrassInfos() {
			m[g.GetGrassId()] = g.GetNum()
		}
		return m
	}

	first := enter("ENTER-1")
	if first == nil {
		t.Fatal("no ENTER")
	}
	var empty []int32
	for _, l := range first.GetLands() {
		if l.GetState() != 2 {
			empty = append(empty, l.GetLandId())
		}
	}
	if len(empty) == 0 {
		t.Fatal("no empty lot to probe")
	}
	bag1 := bag(first.GetUserInfo())
	// pick a HELD task grass with the smallest stock (860003 was 60 on this
	// acc — most likely to show consumption, still plenty to spare).
	var cand []int32
	for id, n := range bag1 {
		if _, ok := elixirTaskGrassNames[id]; ok && n > 0 {
			cand = append(cand, id)
		}
	}
	sort.Slice(cand, func(i, j int) bool { return bag1[cand[i]] < bag1[cand[j]] })
	if len(cand) == 0 {
		t.Fatal("account holds no task grass at all")
	}
	tree, land := cand[0], empty[0]
	pb1, _ := pb.Marshal(&proto.ElixirPlantGrassReq{
		GrassInfo:    []*proto.ElixirPlantGrassInfo{{LandId: pb.Int32(land), GrassId: pb.Int32(tree)}},
		IsPlantElves: pb.Bool(true),
	})
	raw := call(S_ELIXIR_PLANT, SERVER_ELIXIR_PLANT, pb1, fmt.Sprintf("PLANT-held-%d", tree))
	ret, respLands := int32(-1), 0
	if raw != nil {
		var rsp proto.ElixirPlantGrassResp
		pb.Unmarshal(raw, &rsp)
		ret, respLands = rsp.GetRet(), len(rsp.GetLand())
	}
	t.Logf("PLANT held tree %d (bag num=%d) on land %d → ret=%d resp.land=%d",
		tree, bag1[tree], land, ret, respLands)

	second := enter("ENTER-2")
	if second == nil {
		t.Fatal("no ENTER-2")
	}
	for _, l := range second.GetLands() {
		if l.GetLandId() == land {
			t.Logf("ENTER-2 land %d: state=%d grass=%d canAward=%d → held-plant %s",
				land, l.GetState(), l.GetGrassId(), l.GetCanAwardRound(),
				map[bool]string{true: "WORKS (state=2)", false: "DID NOT TAKE"}[l.GetState() == 2 && l.GetGrassId() == tree])
		}
	}
	bag2 := bag(second.GetUserInfo())
	t.Logf("bag %d: %d → %d (consumed=%v)", tree, bag1[tree], bag2[tree], bag2[tree] != bag1[tree])

	// cleanup: pull the probe lot so the field is as we found it (DEL is free).
	b, _ := pb.Marshal(&proto.ElixirDelPlantGrassReq{LandId: []int32{land}})
	call(S_ELIXIR_DEL, SERVER_ELIXIR_DEL, b, "DEL-cleanup")
}
