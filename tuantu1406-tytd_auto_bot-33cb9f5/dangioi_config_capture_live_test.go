//go:build live

package tytd_go

// Read-only "what does the server push about Dược Linh value + season x2"
// capture (owner 2026-10-16). The doubled-grass list and per-herb value are
// NOT in the proto we mined — they ride a server config push at login
// (JS: ElixirElvesDrawConfigSync / ElixirElvesDrawSeasonSync / ElvesCountSync).
// This test logs in, sends NOTHING but auth+ping, and for ~8s records every
// frame: prints a type histogram plus the raw hex of every Elixir-family
// answer (8acx/8adx = 0x8ac0..0x8aff ⇒ 235xxx%100000) so the config push can
// be decoded offline. Nothing is mutated.
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=... TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiConfigCaptureLive -v -timeout 120s .

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

func TestDanGioiConfigCaptureLive(t *testing.T) {
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
	all := make(chan [2]string, 65536)
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

	// Collect the whole login firehose for 9s without sending any command.
	// Every frame is ALSO saved raw (type.hex per frame under /tmp/dg_frames)
	// so server-config pushes (key-value strings) can be grepped offline.
	dir := os.Getenv("TYTD_ELIXIR_FRAMEDIR")
	if dir == "" {
		dir = "/tmp/dg_frames"
	}
	os.MkdirAll(dir, 0o755)
	os.RemoveAll(dir) // fresh capture each run
	os.MkdirAll(dir, 0o755)
	counts := map[string]int{}
	elixirRaw := map[string][]string{} // type → list of hex payloads
	end := time.Now().Add(9 * time.Second)
	seq := 0
	for time.Now().Before(end) {
		select {
		case a := <-all:
			seq++
			counts[a[0]]++
			os.WriteFile(dir+"/"+fmt.Sprintf("%03d_%s.hex", seq, a[0]), []byte(a[1]), 0o644)
			// Elixir family answers live in 0x8aae..0x8b0b (235xxx % 100000),
			// so both 8axx and 8bxx prefixes.
			if len(a[0]) == 4 && (a[0][:2] == "8a" || a[0][:2] == "8b") {
				elixirRaw[a[0]] = append(elixirRaw[a[0]], a[1])
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Logf("saved %d frames under %s", seq, dir)

	var types []string
	for ty := range counts {
		types = append(types, ty)
	}
	sort.Strings(types)
	t.Logf("=== %d frame types during login ===", len(types))
	for _, ty := range types {
		t.Logf("  %s x%d", ty, counts[ty])
	}
	t.Logf("=== raw Elixir-family pushes ===")
	for _, ty := range types {
		if payloads, ok := elixirRaw[ty]; ok {
			for i, hx := range payloads {
				t.Logf("  push %s #%d len=%d:\n    %s", ty, i, len(hx)/2, hx)
			}
		}
	}
}
