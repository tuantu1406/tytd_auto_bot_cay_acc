//go:build live

package tytd_go

// HARVEST→TRỒNG tree-lint live test (owner 2026-10-19: "chạy test xem sau
// khi thu hoạch cây thì bot có tự đào đi trồng cây không"; NO-CUỐC LAW
// 2026-10-23: "giờ không làm nhiệm vụ nữa nên không cuốc nữa").
//
// The unit loop-tests run on fake servers that echo whatever they're handed.
// This one drives the REAL engine functions against a REAL account — a live
// Player is wired to the socket and we replay EXACTLY one tick of
// danGioiAutoLoop (no loop, no minute-sleep), pure event-driven flow
// 2026-10-23:
//
//	danGioiTickOnce() = blind ENTER (235502) ONLY (auto Dược Linh dropped
//	2026-10-23) → the 8aae HANDLER sleeps 1s then runs the garden pass:
//	loop #1 hái (per lot GET only — 235537 is NEVER sent anymore) →
//	loop #2 trồng.
//
// It then VERIFIES the owner's law end to end:
//
//	1. a real harvest happened (>=1 ripe lot collected);
//	2. every harvested lot is either freshly REPLANTED (single-round plant
//	   auto-empties on the GET) or legitimately STILL STANDING (multi-round
//	   tree, hadRecv>0 — it re-ripens on the server's own round timer; NOT
//	   a failure since the cuốc was dropped);
//	3. no harvested lot came back EMPTY-and-never-replanted, and we report
//	   what the plant loop picked (cây cũ / random).
//
// If the account currently has nothing ripe, we say so plainly (nothing to
// harvest ⇒ the bot correctly has nothing to replant this tick).
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiHarvestReplantLive -v -timeout 300s .

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
)

func TestDanGioiHarvestReplantLive(t *testing.T) {
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
	// Writer: everything the engine puts on writeChan goes to the socket
	// (single writer — the keeper ping rides the same channel).
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	// Reader: every server MSG is fed to the REAL pump (elixirDeliver), which
	// is exactly what fills the st seats the engine round-trips block on and
	// folds the passive season push.
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
	// Keep-alive (5s) through the same writer.
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
	// Capture the bot's OWN notify lines so we can report what it claims it did.
	var nmu sync.Mutex
	var notes []string
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case n := <-p.NotiChan:
				nmu.Lock()
				notes = append(notes, n.MSG)
				nmu.Unlock()
			}
		}
	}()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)
	// Let the login firehose settle so the passive 8ae9 season push lands.
	time.Sleep(3 * time.Second)

	// ===== run ONE REAL production tick (owner event-driven 2026-10-23: the
	// tick fires ENTER ONLY — auto Dược Linh dropped same day; the 8aae
	// HANDLER harvested+replanted and this call returns its report) =====
	res, keep := p.danGioiTickOnce()
	if !keep {
		t.Fatalf("session ctx chết giữa tick")
	}
	if res.EnterErr != nil {
		t.Fatalf("tick: %v", res.EnterErr)
	}
	t.Logf("TICK: hái=%v trồng=%d %q", res.Harvested, res.Planted, res.PlantDesc)
	if len(res.Harvested) == 0 {
		fmt.Printf("VERDICT-HARVEST-REPLANT: nothing ripe right now — nothing to replant this tick (bot correct)\n")
		return
	}

	// Audit with a fresh ENTER, under the NO-CUỐC law (2026-10-23): a
	// harvested lot is HEALTHY if it came back freshly REPLANTED (state≠0,
	// grass≠0, hadRecv==0 — single-round plant auto-emptied on the GET) OR
	// still STANDING as a multi-round tree (hadRecv>0 — it keeps producing
	// on the server's own round timer; we stopped digging precisely so it
	// can). FAIL only on a lot that is EMPTY (state==0) yet never replanted.
	after, err := p.elixirEnterHome()
	if err != nil {
		t.Fatalf("ENTER audit: %v", err)
	}
	standing, notReplanted, replanted := 0, 0, 0
	{
		want := map[int32]bool{}
		for _, id := range res.Harvested {
			want[id] = true
		}
		for _, l := range after.GetLands() {
			if !want[l.GetLandId()] {
				continue
			}
			if l.GetState() == 0 || l.GetGrassId() == 0 {
				notReplanted++
				t.Logf("  lot %d: trống mà KHÔNG được trồng lại (state=%d grass=%d)", l.GetLandId(), l.GetState(), l.GetGrassId())
			} else if l.GetHadReceiveRound() > 0 {
				standing++
				t.Logf("  lot %d: cây đa vụ vẫn đứng state=%d grass=%d hadRecv=%d — đúng luật không cuốc, tự round tiếp", l.GetLandId(), l.GetState(), l.GetGrassId(), l.GetHadReceiveRound())
			} else {
				replanted++
			}
		}
	}

	nmu.Lock()
	for _, s := range notes {
		t.Logf("BOT-NOTIFY: %s", s)
	}
	nmu.Unlock()

	// ===== the owner's law, asserted =====
	if notReplanted > 0 {
		t.Errorf("bot harvested %d lots but %d came back EMPTY and never replanted", len(res.Harvested), notReplanted)
	}
	fmt.Printf("VERDICT-HARVEST-REPLANT: thu %d → bot báo trồng %d (audit xác nhận %d mới trồng) | %d lô đa vụ đứng tiếp [%s] → %s\n",
		len(res.Harvested), res.Planted, replanted, standing, res.PlantDesc,
		map[bool]string{true: "BOT hái rồi trồng đúng luật (không cuốc)", false: "FAIL"}[notReplanted == 0])
}
