//go:build live

package tytd_go

// PLANT-FLAGS live probe (owner 2026-10-23, on <live-user>@710565: "đất
// trống không tự trồng. thu hoạch xong vứt đất đấy, tao trồng bằng tay thì
// được"). The trace showed the engine DOES fire PLANT for every empty lot
// (bestElf=864024, bag=44 rows, elves 0/30) and the server answers ret=0
// with an EMPTY echo on every single one = silent no-op — while the owner's
// manual client plant works. Only three things differ between our packet and
// the client's: which TREE is picked and the two flags {oneKey,
// isPlantElves}. This probe plants up to FOUR different empty lots, one per
// experiment, to pin which combo the server honours on THIS account:
//
//	A cỏ thường (held)  isPlantElves=false oneKey=false
//	B cỏ thường (held)  isPlantElves=true  oneKey=false
//	C cây dược linh    isPlantElves=false oneKey=false
//	D cây dược linh    isPlantElves=true  oneKey=false   ← what the engine sends
//
// Each answer is logged RAW (hex + parsed ret + echoed lands), then a fresh
// ENTER gives ground truth: which lots actually hold a tree afterwards.
// Planting is free (AVAILABLE-SEED LAW), max 4 real trees — harmless.
// NEVER waters, NEVER spends an item, NEVER oneKey.
//
//	TYTD_LIVE=1 TYTD_ELIXIR_PF=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710565 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiPlantFlagsLive -v -timeout 180s .

import (
	"context"
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

func TestDanGioiPlantFlagsLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR_PF") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR_PF=1")
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
		Notify:    true,
		NotiChan:  make(chan NotifyMGS, 256),
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
	ui := rsp.GetUserInfo()
	t.Logf("elvesOpenOff=%v water=%d", ui.GetElvesOpenOff(), ui.GetWater())

	// held rows: ordinary (non-herb) + herbal, rows present = plantable.
	var ordinary, herbal []int32
	for _, gi := range ui.GetGrassInfos() {
		if elixirIsHerb(gi.GetGrassId()) {
			herbal = append(herbal, gi.GetGrassId())
		} else {
			ordinary = append(ordinary, gi.GetGrassId())
		}
	}
	sort.Slice(ordinary, func(i, j int) bool { return ordinary[i] < ordinary[j] })
	sort.Slice(herbal, func(i, j int) bool { return herbal[i] > herbal[j] }) // highest first = bestElf-like
	if len(ordinary) == 0 || len(herbal) == 0 {
		t.Fatalf("acc thiếu hàng đợi thử: ordinary=%v herbal=%v", ordinary, herbal)
	}
	grassO, grassH := ordinary[0], herbal[0]
	t.Logf("thử cỏ thường %d và dược linh %d (rows present)", grassO, grassH)

	// free lots
	var free []int32
	for _, l := range rsp.GetLands() {
		if l.GetState() == 0 && l.GetGrassId() == 0 {
			free = append(free, l.GetLandId())
		}
	}
	if len(free) < 4 {
		t.Logf("CHỈ có %d lô trống (cần 4) — chạy cầm chừng số có", len(free))
	}
	if len(free) == 0 {
		t.Fatalf("không còn lô trống nào để thí nghiệm")
	}

	type exp struct {
		name  string
		grass int32
		elves bool
	}
	exps := []exp{
		{"A cỏ-thường elves=FALSE", grassO, false},
		{"B cỏ-thường elves=TRUE", grassO, true},
		{"C dược-linh elves=FALSE", grassH, false},
		{"D dược-linh elves=TRUE (bot đang bắn)", grassH, true},
	}
	used := map[int32]bool{}
	results := map[string]bool{}
	for _, e := range exps {
		lot := int32(0)
		for _, f := range free {
			if !used[f] {
				lot = f
				break
			}
		}
		if lot == 0 {
			t.Logf("%s: SKIP — hết lô trống", e.name)
			continue
		}
		used[lot] = true
		b, _ := pb.Marshal(&proto.ElixirPlantGrassReq{
			GrassInfo:    []*proto.ElixirPlantGrassInfo{{LandId: pb.Int32(lot), GrassId: pb.Int32(e.grass)}},
			OneKey:       pb.Bool(false),
			IsPlantElves: pb.Bool(e.elves)})
		data, err := p.elixirRoundTrip(S_ELIXIR_PLANT, SERVER_ELIXIR_PLANT, b)
		if err != nil {
			t.Errorf("%s lot %d: round-trip %v", e.name, lot, err)
			results[e.name] = false
			continue
		}
		var pr proto.ElixirPlantGrassResp
		pb.Unmarshal(data, &pr)
		echoed := map[int32]int32{}
		for _, l := range pr.GetLand() {
			echoed[l.GetLandId()] = l.GetGrassId()
		}
		ok := pr.GetRet() == 0 && echoed[lot] == e.grass
		t.Logf("%s: lot=%d grass=%d ret=%d land=%d → %s | RAW % x",
			e.name, lot, e.grass, pr.GetRet(), len(pr.GetLand()),
			map[bool]string{true: "ECHO (server nhận trồng)", false: "NO-OP (echo rỗng)"}[ok], data)
		results[e.name] = ok
		time.Sleep(400 * time.Millisecond)
	}

	// ground truth: fresh ENTER, which experimented lots now hold trees?
	after, err := p.elixirEnterHome()
	if err != nil {
		t.Fatalf("ENTER audit: %v", err)
	}
	state := map[int32]*proto.ElixirLandInfo{}
	for _, l := range after.GetLands() {
		state[l.GetLandId()] = l
	}
	lines := ""
	for _, f := range free {
		if !used[f] {
			continue
		}
		l := state[f]
		lines += fmt.Sprintf(" lô %d: state=%d grass=%d |", f, l.GetState(), l.GetGrassId())
	}
	t.Logf("GROUND TRUTH:%s", lines)
	for _, e := range exps {
		fmt.Printf("VERDICT-PLANTFLAGS: %s → %s\n", e.name,
			map[bool]string{true: "SERVER TRỒNG (echo đủ)", false: "SERVER NO-OP (im lặng)"}[results[e.name]])
	}
}
