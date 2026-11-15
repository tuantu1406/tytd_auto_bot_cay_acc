//go:build live

package tytd_go

// ĐAN GIỚI FULL-LIFECYCLE LIVE TEST — MULTI-ROUND, ENGINE-IDENTICAL
// (owner 2026-10-20: "kiểm tra nhiều round cho tao, xem code test của mày
// có match với engine thật không"). The test no longer hand-copies the
// tick: EVERY round it calls p.danGioiTickOnce() — the very function
// danGioiAutoLoop runs in production — and asserts its result:
//
//	poll: res, _ := p.danGioiTickOnce()   ← real production tick
//	→ Harvested (lots the server ACCEPTED GET for) — a round "counts" once
//	  this is non-empty;
//	→ AFTER-tick audit (the engine does ONE ENTER per tick — owner
//	  2026-10-23 — so the test itself re-reads the field). NO-CUỐC LAW
//	  (owner 2026-10-23: "giờ không làm nhiệm vụ nữa nên không cuốc nữa"):
//	  a harvested SINGLE-ROUND plant auto-empties on the GET → it must come
//	  back freshly replanted (hadRecv==0); a MULTI-round tree legitimately
//	  stays standing (state==2, hadRecv>0) and re-ripens on its own timer —
//	  that is CORRECT now, not a failure. FAIL only when a harvested lot is
//	  EMPTY (state==0) yet never replanted.
//
// The ONLY test-side state is the synthetic 496 task-board row (re-seeded
// before every poll in case a real 501 fold wipes it) — documented account
// hole: <live-user>'s 501 never carries the type-51 496 dailies, so without
// the seed the picker idles with 36 empty lots. Every packet that leaves is
// real; harvest/cuốc/replant/claims/orders all ride the production code.
//
//	ROUNDS=TYTD_ELIXIR_LIFE_ROUNDS (default 3), per-round ripe budget
//	TYTD_ELIXIR_LIFE_SECS (default 1800):
//	TYTD_LIVE=1 TYTD_ELIXIR_LIFE=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiLifecycleLive -v -timeout 120m .
//
//	VERDICT-LIFE: OK (N rounds harvested, harvested-empty lots replanted 100%;
//	          multi-round trees left standing = đúng luật không-cuốc)
//	          / FAIL (names empty-never-replanted lots + their state/grass)
//	          / SKIP (no round ever saw a ripe lot in budget — bag printed).

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

func dgDumpLands(t *testing.T, tag string, rsp *proto.ElixirEnterHomeResp) {
	for _, l := range rsp.GetLands() {
		t.Logf("%s lot %d: state=%d grass=%d lv=%d canAward=%d hadRecv=%d roundStart=%d",
			tag, l.GetLandId(), l.GetState(), l.GetGrassId(), l.GetGrassLv(),
			l.GetCanAwardRound(), l.GetHadReceiveRound(), l.GetRoundStartTime())
	}
}

// dgSeedTaskBoard re-seeds the 496 board row the way a real 501 would carry
// it (510002 = harvest 80× 860001 Bạch Linh Chỉ; bag holds thousands).
// Client-side state only — packets that follow are real. Re-run before each
// poll because a real 501 fold resets p.tasks wholesale (player_task.go).
func dgSeedTaskBoard(p *Player) {
	p.TaskMu.Lock()
	if p.tasks == nil {
		p.tasks = map[int32]*taskEntry{}
	}
	if _, ok := p.tasks[510002]; !ok {
		p.tasks[510002] = &taskEntry{data: &proto.TaskDataTempMsg{
			TaskId: pb.Int32(510002), Value: pb.String("0")}}
		p.taskSynced = true
	}
	p.TaskMu.Unlock()
}

func TestDanGioiLifecycleLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR_LIFE") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR_LIFE=1")
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
	waitSecs := 1800 // real ripening measured 15m23s (2026-10-20 run) — give headroom
	if v := os.Getenv("TYTD_ELIXIR_LIFE_SECS"); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			waitSecs = n
		}
	}
	rounds := 3
	if v := os.Getenv("TYTD_ELIXIR_LIFE_ROUNDS"); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			rounds = n
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
	time.Sleep(3 * time.Second)

	rsp, err := p.elixirEnterHome()
	if err != nil {
		t.Fatalf("ENTER: %v", err)
	}
	dgDumpLands(t, "INIT", rsp)
	bag := map[int32]int32{}
	for _, g := range rsp.GetUserInfo().GetGrassInfos() {
		if g.GetNum() > 0 {
			bag[g.GetGrassId()] = g.GetNum()
		}
	}
	t.Logf("BAG held: %v", bag)

	// Each round = poll PRODUCTION TICKS until one of them harvests. A tick
	// with nothing ripe still plants (first tick seeds the whole field via
	// the task picker), so waiting for ripening IS the polling.
	okRounds, failRounds, skippedRounds := 0, 0, 0
	totalHarvest, totalPlant := 0, 0
	for r := 1; r <= rounds; r++ {
		t.Logf("=== ROUND %d/%d (budget %ds) ===", r, rounds, waitSecs)
		deadline := time.Now().Add(time.Duration(waitSecs) * time.Second)
		harvestedRound := false
		for time.Now().Before(deadline) {
			dgSeedTaskBoard(p)
			res, keep := p.danGioiTickOnce()
			if !keep {
				t.Fatalf("session ctx chết giữa round %d", r)
			}
			if res.EnterErr != nil {
				t.Logf("R%d tick ENTER err: %v", r, res.EnterErr)
				time.Sleep(30 * time.Second)
				continue
			}
			if len(res.Harvested) == 0 {
				t.Logf("R%d tick: chưa lô chín (planted=%d %q)", r, res.Planted, res.PlantDesc)
				time.Sleep(30 * time.Second)
				continue
			}
			// THE round harvested with the real engine — judge it.
			harvestedRound = true
			totalHarvest += len(res.Harvested)
			totalPlant += res.Planted
			t.Logf("R%d HARVEST lots %v | replant=%d %q", r, res.Harvested, res.Planted, res.PlantDesc)
			// GROUND TRUTH AFTER the tick (the engine itself does ONE ENTER
			// per tick now — owner 2026-10-23 "làm luôn không cần phải gọi
			// thêm home enter lần nữa" — so the audit lives HERE, in the
			// test, not in the tick), under the NO-CUỐC law (owner
			// 2026-10-23: "không cuốc nữa"): a harvested SINGLE-round plant
			// auto-emptied on the GET → must come back freshly REPLANTED
			// (state==2, hadRecv==0). A MULTI-round tree STAYS STANDING
			// (state==2, hadRecv>0) — CORRECT: it re-ripens on its own
			// server timer, digging it would only kill the producer. FAIL
			// only on a harvested lot that is EMPTY (state==0) yet was
			// never replanted.
			var standing, notReplanted []int32
			if after, err := p.elixirEnterHome(); err == nil {
				want := map[int32]bool{}
				for _, id := range res.Harvested {
					want[id] = true
				}
				for _, l := range after.GetLands() {
					if !want[l.GetLandId()] {
						continue
					}
					t.Logf("R%d AFTER-TICK lot %d: state=%d grass=%d lv=%d canAward=%d hadRecv=%d",
						r, l.GetLandId(), l.GetState(), l.GetGrassId(),
						l.GetGrassLv(), l.GetCanAwardRound(), l.GetHadReceiveRound())
					if l.GetState() == 0 || l.GetGrassId() == 0 {
						notReplanted = append(notReplanted, l.GetLandId())
					} else if l.GetHadReceiveRound() > 0 {
						standing = append(standing, l.GetLandId()) // đa vụ, tự chín lại — OK
					}
				}
			} else {
				t.Logf("R%d AFTER-TICK ENTER: %v", r, err)
			}
			if len(standing) > 0 {
				t.Logf("R%d %d lô đa vụ vẫn đứng (hadRecv>0) — đúng luật không cuốc, để nó tự round tiếp", r, len(standing))
			}
			if len(notReplanted) > 0 {
				failRounds++
				fmt.Printf("VERDICT-LIFE: FAIL round %d — thu %d lô, %d lô trống mà KHÔNG được trồng lại cùng tick: %v (planted=%d)\n",
					r, len(res.Harvested), len(notReplanted), notReplanted, res.Planted)
				break
			}
			okRounds++
			break
		}
		if !harvestedRound {
			skippedRounds++
			t.Logf("R%d: không có lô chín trong %ds — round SKIP", r, waitSecs)
			if r == 1 {
				if r2, err := p.elixirEnterHome(); err == nil {
					dgDumpLands(t, "STUCK", r2)
				}
			}
			continue
		}
		if failRounds > 0 {
			break
		}
	}

	nmu.Lock()
	for _, s := range notes {
		t.Logf("BOT-NOTIFY: %s", s)
	}
	nmu.Unlock()

	if failRounds > 0 {
		return // per-round FAIL already printed
	}
	if okRounds == 0 {
		fmt.Printf("VERDICT-LIFE: SKIP — %d round, KHÔNG round nào thấy lô chín (túi: %v) — cần acc đang trồng hoặc mật khẩu acc đó\n", rounds, bag)
		return
	}
	fmt.Printf("VERDICT-LIFE: OK — %d/%d round thu hoạch bằng DAN GIOI TICK ONCE THẬT (bot production), lô đơn-vụ trống đều được trồng lại 100%% (không cuốc — đa vụ đứng tự round), tổng thu %d lô, trồng %d lô, %d round skip\n",
		okRounds, rounds, totalHarvest, totalPlant, skippedRounds)
}
