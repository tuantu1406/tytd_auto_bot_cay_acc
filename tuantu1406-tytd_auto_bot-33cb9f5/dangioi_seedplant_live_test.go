//go:build live

package tytd_go

// ĐAN GIỚI SEED-AVAILABILITY PROBE (owner 2026-10-21: "acc đó có hạt giống
// dược linh, có thể mày check ở túi cây chứ không phải túi hạt — click ô
// đất trống game show hết loại hạt available"). The season probe found
// <live-user>'s grassInfos carry `864024×0` — a CURRENT-trio herbal row the
// engine ignores because its held gate demands num>0. Planting is free
// (live-proven 2026-10-17), so the real question is binary:
//
//	does the server accept PLANT of a herbal id that rides grassInfos with
//	num==0 (echo present), or only rows with num>0?
//
// This test answers it on the live field and restores the board:
//
//	ENTER → pick the unlocked-but-zero herbal row (num==0) → make ONE lot
//	free (harvest any ripe lot; else cuốc one NON-herbal growing lot via the
//	engine's own DEL pass) → PLANT the zero-count herbal via the engine's
//	exact packet (elixirPlantPacketOK) → fresh ENTER: echo means AVAILABLE
//	(the held gate must key on ROW PRESENCE, not count) → restore: cuốc the
//	test lot and replant what stood there before (free).
//
//	VERDICT-SEEDPLANT: AVAILABLE / NOOP (row presence is NOT unlock) /
//	                  SKIP (nothing to test)
//
//	TYTD_LIVE=1 TYTD_ELIXIR_SEED=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiSeedPlantLive -v -timeout 10m .

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

func TestDanGioiSeedPlantLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR_SEED") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR_SEED=1")
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
	// The target: a herbal id that rides grassInfos with num==0 (available
	// seed per the owner's picker observation), preferring the current
	// season's trio.
	p.ElixirMu.Lock()
	season := p.ElixirSeason
	p.ElixirMu.Unlock()
	trio := map[int32]bool{}
	for _, t := range elixirSeasonTrio(season) {
		trio[t] = true
	}
	target := int32(0)
	for _, g := range rsp.GetUserInfo().GetGrassInfos() {
		if g.GetNum() == 0 && elixirIsHerb(g.GetGrassId()) {
			if target == 0 || (trio[g.GetGrassId()] && !trio[target]) {
				target = g.GetGrassId()
			}
		}
	}
	if target == 0 {
		fmt.Printf("VERDICT-SEEDPLANT: SKIP — không có dòng dược linh num==0 nào để test\n")
		return
	}
	t.Logf("TARGET seed %d (%s) — row present, num=0, season %d trio=%v", target, elixirGrassName(target), season, trio[target])

	lot := int32(0)
	var wasTree int32 // what stood on the lot before we touched it
	for _, l := range rsp.GetLands() {
		if l.GetState() == 0 && l.GetGrassId() == 0 {
			lot = l.GetLandId()
			break
		}
	}
	if lot == 0 {
		// No free lot: harvest every ripe lot first (free rewards), then cuốc
		// exactly one NON-herbal growing lot with the engine's own DEL pass.
		if ids := p.elixirHarvest(rsp); len(ids) > 0 {
			time.Sleep(600 * time.Millisecond)
			r2, err := p.elixirEnterHome()
			if err == nil {
				rsp = r2
			}
		}
		for _, l := range rsp.GetLands() {
			if l.GetState() == 0 && l.GetGrassId() == 0 {
				lot = l.GetLandId()
				break
			}
		}
	}
	if lot == 0 {
		for _, l := range rsp.GetLands() {
			if l.GetGrassId() != 0 && !elixirIsHerb(l.GetGrassId()) {
				lot, wasTree = l.GetLandId(), l.GetGrassId()
				break
			}
		}
		if lot == 0 {
			fmt.Printf("VERDICT-SEEDPLANT: SKIP — không tạo được lô trống (mọi lô đều đang trồng dược linh)\n")
			return
		}
		// GET-BEFORE-DEL law: DEL on a lot with uncollected canAward rounds
		// is a silent no-op — collect this lot's ripe rounds FIRST (free
		// rewards), then cuốc.
		for attempt := 0; attempt < 4; attempt++ {
			for _, l := range rsp.GetLands() {
				if l.GetLandId() == lot && l.GetCanAwardRound() > 0 {
					gb, _ := pb.Marshal(&proto.ElixirGetGrassReq{LandId: []int32{lot}})
					if data, gerr := p.elixirRoundTrip(S_ELIXIR_GET_GRASS, SERVER_ELIXIR_GET, gb); gerr == nil {
						t.Logf("GET lô %d trước cuốc: ret=%x", lot, data[:min(4, len(data))])
					}
				}
			}
			time.Sleep(400 * time.Millisecond)
			if n := p.elixirDelLots([]int32{lot}); n > 0 {
				break
			}
			t.Logf("cuốc lô %d lần %d chưa ăn — GET lại rồi thử", lot, attempt+1)
			if f, ferr := p.elixirEnterHome(); ferr == nil {
				rsp = f
			}
		}
		f, err := p.elixirEnterHome()
		if err == nil {
			rsp = f
		}
		ok := false
		for _, l := range rsp.GetLands() {
			if l.GetLandId() == lot {
				ok = l.GetState() == 0 && l.GetGrassId() == 0
			}
		}
		if !ok {
			fmt.Printf("VERDICT-SEEDPLANT: SKIP — lô %d cuốc không trống, không dám đụng lô khác\n", lot)
			return
		}
		t.Logf("lô %d (cây cũ %d) đã cuốc trống để test", lot, wasTree)
	}

	// THE test: plant the zero-count herbal with the engine's exact packet.
	infos := []*proto.ElixirPlantGrassInfo{{LandId: pb.Int32(lot), GrassId: pb.Int32(target)}}
	body, _ := pb.Marshal(&proto.ElixirPlantGrassReq{GrassInfo: infos, IsPlantElves: pb.Bool(true)})
	plantOK := p.elixirPlantPacketOK(body)
	time.Sleep(600 * time.Millisecond)
	after, err := p.elixirEnterHome()
	if err != nil {
		t.Fatalf("ENTER after plant: %v", err)
	}
	serverState := ""
	for _, l := range after.GetLands() {
		if l.GetLandId() == lot {
			serverState = fmt.Sprintf("state=%d grass=%d", l.GetState(), l.GetGrassId())
		}
	}
	t.Logf("PLANT %d → lot %d: echoOK=%v, server says %s", target, lot, plantOK, serverState)

	// Restore the board: cuốc the test lot (bare DEL — the test tree was
	// planted fresh this run, no ripe round to lose) and put the old tree
	// back if there was one (planting is free).
	p.elixirDelLots([]int32{lot})
	if wasTree != 0 {
		rbody, _ := pb.Marshal(&proto.ElixirPlantGrassReq{
			GrassInfo:    []*proto.ElixirPlantGrassInfo{{LandId: pb.Int32(lot), GrassId: pb.Int32(wasTree)}},
			IsPlantElves: pb.Bool(true)})
		if p.elixirPlantPacketOK(rbody) {
			t.Logf("đã trồng lại cây cũ %d vào lô %d", wasTree, lot)
		} else {
			t.Logf("CẢNH BÁO: không trồng lại được cây cũ %d vào lô %d — kiểm tra tay", wasTree, lot)
		}
	}

	if plantOK {
		fmt.Printf("VERDICT-SEEDPLANT: AVAILABLE — server nhận trồng hạt num==0 (%d, %s): held gate phải sửa = DÒNG CÓ MẶT, không phải num>0\n", target, elixirGrassName(target))
		return
	}
	fmt.Printf("VERDICT-SEEDPLANT: NOOP — trồng %d (num==0) không echo: dòng num==0 KHÔNG phải unlock, nguồn 'hạt available' nằm chỗ khác\n", target)
}
