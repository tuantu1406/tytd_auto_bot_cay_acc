//go:build live

package tytd_go

// Read-only Đan Giới herb audit (owner 2026-10-16, acc <live-user>): pull
// ENTER once + the Dược Linh panel once and print EXACTLY which of the
// account's trees can roll Dược Linh (the 864xxx family, filtered through the
// production elixirIsHerb/elixirHerbName helpers) plus the /30 counter — so
// we can eyeball the game's real data against our ported rule. No WATER, no
// PLANT: nothing on the account is mutated.
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=... TYTD_LIVE_SERVER=710547 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiHerbsLive -v -timeout 240s .

import (
	"encoding/hex"
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

func TestDanGioiHerbsLive(t *testing.T) {
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

	call := func(cmd, ansType string, body []byte, tag string) []byte {
		end := time.Now().Add(3 * time.Second)
		for time.Now().Before(end) {
			select {
			case <-all:
			case <-time.After(150 * time.Millisecond):
			}
		}
		write <- pack.Pack(cmd, body, uid)
		seen := map[string]int{}
		end = time.Now().Add(8 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if a[0] == ansType {
					raw, _ := hex.DecodeString(a[1])
					t.Logf("%s %s len=%d", tag, ansType, len(raw))
					return raw
				}
				seen[a[0]]++
			case <-time.After(250 * time.Millisecond):
				if len(seen) > 0 {
					break
				}
			}
		}
		t.Logf("!! %s: no %s answer; other frames while waiting:", tag, ansType)
		for ty, n := range seen {
			t.Logf("   %s x%d", ty, n)
		}
		return nil
	}

	b, _ := pb.Marshal(&proto.ElixirEnterHomeReq{PlayerId: pb.Int64(int64(uid))})
	raw := call(S_ELIXIR_ENTER, SERVER_DANGIOI_INFO, b, "ENTER")
	if raw == nil {
		t.Fatal("no 8aae")
	}
	var rsp proto.ElixirEnterHomeResp
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		t.Fatalf("8aae decode: %v", err)
	}
	ui := rsp.GetUserInfo()
	t.Logf("ACC %s@%d uid=%d · nước=%d bucket=%d elvesOpenOff=%v · %d loại cây, %d lô",
		user, server, uid, ui.GetWater(), ui.GetWaterBucket(), ui.GetElvesOpenOff(),
		len(ui.GetGrassInfos()), len(rsp.GetLands()))

	// Every tree the account owns, flagged herb / ordinary.
	type row struct {
		id  int32
		lv  int32
		num int32
	}
	var herbs, ordinary []row
	for _, g := range ui.GetGrassInfos() {
		r := row{g.GetGrassId(), g.GetLv(), g.GetNum()}
		if elixirIsHerb(r.id) {
			herbs = append(herbs, r)
		} else {
			ordinary = append(ordinary, r)
		}
	}
	sort.Slice(herbs, func(i, j int) bool { return herbs[i].id < herbs[j].id })
	sort.Slice(ordinary, func(i, j int) bool { return ordinary[i].id < ordinary[j].id })

	t.Logf("== CÂY CÓ THỂ RA DƯỢC LINH (họ 864xxx) — %d loại ==", len(herbs))
	var withSeed int
	for _, h := range herbs {
		flag := "hạt 0 (không trồng được)"
		if h.num > 0 {
			flag = ""
			withSeed++
		}
		t.Logf("  🌿 %d %-28s lv%-2d  %6d hạt %s", h.id, elixirHerbName(h.id), h.lv, h.num, flag)
	}
	t.Logf("   → %d loại có hạt để trồng", withSeed)

	// Sanity: no non-864xxx should ever be counted as a herb.
	for _, o := range ordinary {
		if elixirIsHerb(o.id) {
			t.Errorf("classification leak: %d flagged herb but outside 864xxx", o.id)
		}
	}
	t.Logf("== CÂY THƯỜNG (không ra dược linh) — %d loại ==", len(ordinary))
	for _, o := range ordinary {
		t.Logf("  ·  %d %-28s lv%-2d  %6d hạt", o.id, elixirHerbName(o.id), o.lv, o.num)
	}

	// What's planted right now.
	planted := map[int32]int{}
	for _, l := range rsp.GetLands() {
		if id := l.GetGrassId(); id != 0 {
			planted[id]++
		}
	}
	var pids []int32
	for id := range planted {
		pids = append(pids, id)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	t.Logf("== ĐANG TRỒNG %d lô ==", len(rsp.GetLands()))
	for _, id := range pids {
		kind := "cây thường"
		if elixirIsHerb(id) {
			kind = "CÓ dược linh"
		}
		t.Logf("  %d %-28s ×%d lô — %s", id, elixirHerbName(id), planted[id], kind)
	}

	// /30 counter.
	if eRaw := call(S_ELIXIR_ELVES_ENTER, SERVER_ELIXIR_ELVES_INFO, []byte{}, "ELVES"); eRaw != nil {
		var er proto.ElixirElvesEnterResp
		if err := pb.Unmarshal(eRaw, &er); err != nil {
			t.Fatalf("8ade decode: %v", err)
		}
		pd := er.GetElvesPlayerData()
		t.Logf("== DƯỢC LINH HÔM NAY == ret=%d thu=%d/30 trộm=%d", er.GetRet(), pd.GetTodayGetCount(), pd.GetTodayStealCount())
	}
}
