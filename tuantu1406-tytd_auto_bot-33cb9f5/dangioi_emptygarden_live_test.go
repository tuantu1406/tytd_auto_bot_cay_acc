//go:build live

package tytd_go

// PULL-ALL → PLANT-LEVEL-1 live test (owner 2026-10-21, acc <live-user>
// @710484, bug "thu hoạch hết lượt rồi không tự trồng lại, vườn trống trơn":
// "mày nhổ cả vườn đi rồi test trồng cây level 1 thôi cho nhanh có kết quả").
//
// Deliberately dumb so a result CANNOT hide:
//
//	1. ENTER — dump every lot (state/grass/lv/canAward/hadRecv).
//	2. Nhổ CẢ VƯỜN: elixirDelLots over every occupied lot (GET-then-DEL,
//	   echo-verified — production helper, nothing hand-rolled).
//	3. Trồng CÂY LEVEL 1: the four ordinary starter trees 860001–04 (the game's
//	   level-1 plants) that have a grassInfos ROW (available-seed law — a
//	   rowless id no-ops); fall back to the lowest held herbal id.
//	4. Fresh ENTER — VERIFY state=2 && grass==tree on the replanted lots.
//
// VERDICT-PULLREPLANT then says plainly whether the replant path works on
// this account at all (⇒ bug is timing/idle-rules in the loop) or dies at
// DEL (lots stay) / PLANT (no echo) / no plantable row (acc policy).
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 go test -tags "sqlcipher live" -count=1 -run TestDanGioiEmptyGardenLive -v -timeout 300s .

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

func TestDanGioiEmptyGardenLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710484
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
	time.Sleep(4 * time.Second) // login firehose + 8ae9 season push

	// ===== 1. field truth =====
	rsp, err := p.elixirEnterHome()
	if err != nil {
		t.Fatalf("ENTER: %v", err)
	}
	var occupied, empty []int32
	for _, l := range rsp.GetLands() {
		if l.GetState() == 0 && l.GetGrassId() == 0 {
			empty = append(empty, l.GetLandId())
			continue
		}
		occupied = append(occupied, l.GetLandId())
		t.Logf("LOT %d: state=%d grass=%d lv=%d canAward=%d hadRecv=%d elves=%d canRob=%v roundStart=%d",
			l.GetLandId(), l.GetState(), l.GetGrassId(), l.GetGrassLv(),
			l.GetCanAwardRound(), l.GetHadReceiveRound(), l.GetElvesNum(),
			l.GetCanRob(), l.GetRoundStartTime())
	}
	t.Logf("BEFORE: %d occupied, %d empty", len(occupied), len(empty))

	// ===== 2. nhổ CẢ VƯỜN (production helper, GET→DEL→echo-verify) =====
	pulled := 0
	if len(occupied) > 0 {
		pulled = p.elixirDelLots(occupied)
	}
	after := rsp
	if len(occupied) > 0 {
		if f, err := p.elixirEnterHome(); err == nil {
			after = f
		}
	}
	emptyNow := 0
	var still []int32
	for _, l := range after.GetLands() {
		if l.GetState() == 0 && l.GetGrassId() == 0 {
			emptyNow++
		} else {
			still = append(still, l.GetLandId())
		}
	}
	t.Logf("NHỔ: requested=%d verifiablyPulled=%d → field now %d empty, %d still occupied %v",
		len(occupied), pulled, emptyNow, len(still), still)

	// ===== 3. trồng cây level 1 =====
	held := map[int32]bool{}
	var rowParts []string
	for _, g := range after.GetUserInfo().GetGrassInfos() {
		held[g.GetGrassId()] = true
		rowParts = append(rowParts, fmt.Sprintf("%d×%d", g.GetGrassId(), g.GetNum()))
	}
	t.Logf("BAG ROWS (%d): %v", len(rowParts), rowParts)
	tree := int32(0)
	for _, c := range []int32{860001, 860002, 860003, 860004} { // level-1 starters
		if held[c] {
			tree = c
			break
		}
	}
	if tree == 0 { // fallback: lowest held herbal
		for id := range held {
			if elixirIsHerb(id) && (tree == 0 || id < tree) {
				tree = id
			}
		}
	}
	plantResult := "NO TREE (acc không có row cây level 1 lẫn herbal)"
	verified := 0
	if tree != 0 {
		// plant every empty lot, production packet + echo verify (elixirPlantPacketOK)
		ids := emptyNowIds(after)
		for i := 0; i < len(ids); i += elixirBatch {
			end := i + elixirBatch
			if end > len(ids) {
				end = len(ids)
			}
			batch := ids[i:end]
			infos := make([]*proto.ElixirPlantGrassInfo, 0, len(batch))
			for _, id := range batch {
				infos = append(infos, &proto.ElixirPlantGrassInfo{
					LandId: pb.Int32(id), GrassId: pb.Int32(tree)})
			}
			b, _ := pb.Marshal(&proto.ElixirPlantGrassReq{GrassInfo: infos, IsPlantElves: pb.Bool(true)})
			if p.elixirPlantPacketOK(b) {
				verified += len(batch)
				t.Logf("TRỒNG ✓ lô %d cây %d", batch[0], tree)
			} else {
				t.Logf("TRỒNG ✗ lô %d cây %d (ret!=0 hoặc no-op — xem log PLANT refused/no-op)", batch[0], tree)
			}
		}
		plantResult = fmt.Sprintf("planted tree=%d (%s) verifiedEchoed=%d", tree, elixirGrassName(tree), verified)
	}
	// ground truth
	final, err := p.elixirEnterHome()
	if err != nil {
		t.Fatalf("final ENTER: %v", err)
	}
	growing := 0
	for _, l := range final.GetLands() {
		if l.GetState() != 0 || l.GetGrassId() != 0 {
			growing++
			if l.GetState() != 2 && tree != 0 && l.GetGrassId() == tree {
				t.Logf("LOT %d planted but state=%d (not 2!)", l.GetLandId(), l.GetState())
			}
		}
	}
	fmt.Printf("VERDICT-PULLREPLANT: nhổ %d/%d lô → %s → ENTER cuối: %d lô đang mọc → %s\n",
		pulled, len(occupied), plantResult, growing,
		map[bool]string{true: "ĐƯỜNG TRỒNG CHẠY OK (bug nằm ở timing/idle-rule của loop)",
			false: "ĐƯỜNG TRỒNG CHẾT"}[growing > 0])
}

func emptyNowIds(rsp *proto.ElixirEnterHomeResp) []int32 {
	var ids []int32
	for _, l := range rsp.GetLands() {
		if l.GetState() == 0 && l.GetGrassId() == 0 {
			ids = append(ids, l.GetLandId())
		}
	}
	return ids
}
