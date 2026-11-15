//go:build live

package tytd_go

// ENTER-SEAT PUSH PROBE (owner 2026-10-23: "sao ENTER có lúc server trả về
// 0 lô được, có phải proto sai không"). Answer with evidence, not theory:
// fire N ENTERs back-to-back on a live session (highest push pressure is
// right after login) and classify every 8aae frame that reaches the seat:
// lands>0 = real answer; lands==0 = the unsolicited garden SYNC (hex head
// printed by elixirEnterHome). proto-said would fail EVERY parse/decode
// deterministically — a mix proves the frames differ, not the table.
//
//	TYTD_LIVE=1 TYTD_ELIXIR_BURST=1 TYTD_LIVE_USER=... TYTD_LIVE_PASS=... TYTD_LIVE_SERVER=... \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiEnterBurstLive -v -timeout 5m .

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

func TestDanGioiEnterBurstLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR_BURST") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR_BURST=1")
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
	n := 12
	if v := os.Getenv("TYTD_ELIXIR_BURST_N"); v != "" {
		if k, _ := strconv.Atoi(v); k > 0 {
			n = k
		}
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

	full, zero, errs := 0, 0, 0
	for i := 0; i < n; i++ {
		rsp, err := p.elixirEnterHome()
		if err != nil {
			errs++
			t.Logf("ENTER %d: ERR %v", i+1, err)
			continue
		}
		if len(rsp.GetLands()) > 0 {
			full++
		} else {
			zero++
		}
		time.Sleep(300 * time.Millisecond)
	}
	fmt.Printf("VERDICT-BURST: %d ENTER → %d đủ 36 lô, %d vẫn 0 lô (đã retry), %d err — PUSH lạc ghế %v, proto %v\n",
		n, full, zero, errs,
		map[bool]string{true: "CHỨNG MINH", false: "không thấy"}[zero+errs > 0],
		map[bool]string{true: "SAI", false: "ĐÚNG (giải mã 36/36 ổn định)"}[full == 0])
}
