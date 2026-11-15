//go:build live

package tytd_go

// OWNER CUỐC-2-Ô CHECK (2026-10-23 @<live-user>/710565): the field sat
// 36/36 state=2 canAward=0 hadRecv=0 (lv5 "chết đứng"). Dig EXACTLY 2 of
// them with the production bare-del path (elixirDelLots — no defensive GET,
// echo-verified) and re-ENTER to see what the server really does to a lot
// nobody ever harvested. Restores nothing — the loop replants next tick.
//
//	TYTD_LIVE=1 TYTD_ELIXIR_CUOC2=1 TYTD_LIVE_USER=... TYTD_LIVE_PASS=... TYTD_LIVE_SERVER=... \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiCuoc2Live -v -timeout 5m .

import (
	"context"
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

func TestDanGioiCuoc2Live(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR_CUOC2") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR_CUOC2=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710565
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Player{
		Username:  user,
		Name:      user,
		UID:       uid,
		writeChan: make(chan []byte, 256),
		ctx:       ctx,
	}
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
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
			p.elixirDeliver(ty, d)
		}
	}()
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				select {
				case p.writeChan <- pack.Pack(PING_CMD, []byte{}, uid):
				case <-ctx.Done():
				}
			}
		}
	}()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)
	time.Sleep(3 * time.Second)

	rsp, err := p.elixirEnterHome()
	if err != nil {
		t.Fatalf("ENTER: %v", err)
	}
	var targets []int32
	for _, l := range rsp.GetLands() {
		if l.GetState() != 0 || l.GetGrassId() != 0 {
			targets = append(targets, l.GetLandId())
			t.Logf("TRƯỚC lô %d: state=%d grass=%d lv=%d canAward=%d hadRecv=%d",
				l.GetLandId(), l.GetState(), l.GetGrassId(), l.GetGrassLv(),
				l.GetCanAwardRound(), l.GetHadReceiveRound())
			if len(targets) == 2 {
				break
			}
		}
	}
	if len(targets) == 0 {
		fmt.Printf("VERDICT-CUOC2: SKIP — vườn trống, không có ô để cuốc\n")
		return
	}
	// GET each target first (the engine's own single-lot GET) — THE question:
	// does GET clear canAward to 0 on a multi-round lv5 tree, or does it
	// leave rounds behind (which would make the engine's same-tick DEL a
	// permanent silent no-op = the owner's "thu N lô, cuốc 0 lô")?
	for _, id := range targets {
		gb, _ := pb.Marshal(&proto.ElixirGetGrassReq{LandId: []int32{id}})
		data, gerr := p.elixirRoundTrip(S_ELIXIR_GET_GRASS, SERVER_ELIXIR_GET, gb)
		if gerr != nil {
			t.Logf("GET lô %d ERR: %v", id, gerr)
			continue
		}
		var gr proto.ElixirGetGrassResp
		_ = pb.Unmarshal(data, &gr)
		t.Logf("GET lô %d: ret=%d", id, gr.GetRet())
	}
	time.Sleep(600 * time.Millisecond)
	mid, err := p.elixirEnterHome()
	if err != nil {
		t.Fatalf("ENTER sau GET: %v", err)
	}
	for _, l := range mid.GetLands() {
		for _, id := range targets {
			if l.GetLandId() == id {
				t.Logf("SAU-GET lô %d: state=%d grass=%d lv=%d canAward=%d hadRecv=%d",
					id, l.GetState(), l.GetGrassId(), l.GetGrassLv(),
					l.GetCanAwardRound(), l.GetHadReceiveRound())
			}
		}
	}
	n := p.elixirDelLots(targets)
	t.Logf("elixirDelLots(%v) → verifiably pulled=%d", targets, n)
	time.Sleep(600 * time.Millisecond)
	after, err := p.elixirEnterHome()
	if err != nil {
		t.Fatalf("ENTER sau cuốc: %v", err)
	}
	want := map[int32]bool{}
	for _, id := range targets {
		want[id] = true
	}
	for _, l := range after.GetLands() {
		if want[l.GetLandId()] {
			t.Logf("SAU lô %d: state=%d grass=%d lv=%d canAward=%d hadRecv=%d",
				l.GetLandId(), l.GetState(), l.GetGrassId(), l.GetGrassLv(),
				l.GetCanAwardRound(), l.GetHadReceiveRound())
		}
	}
	fmt.Printf("VERDICT-CUOC2: cuốc %d/%d ô (production bare DEL, không GET)\n", n, len(targets))
}
