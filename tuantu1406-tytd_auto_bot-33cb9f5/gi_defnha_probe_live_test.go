//go:build live

package tytd_go

// GOD ISLAND DEF-NHA LIVE PROBE (owner 2026-10-19, pairs with
// player_event_gidef.go) — the def-nha engine must "chỉ hồi vừa đủ máu để
// hồi sinh", but no proto field publishes the game's revive HP threshold.
// This probe measures it on a REAL corpse: it sits resident on the island
// (same traffic godIslandStay runs), and WHEN the account happens to lie
// dead with the revive countdown already passed — a genuine death, the
// probe NEVER orders suicide — it drinks the spiritual ball (206827) in a
// rising ladder {10, 50, 200, maxPower/4}, 5s apart, until curPower leaves
// 0, and prints VERDICT-DEFNHA with the first sip size that stood the unit
// up plus where the unit woke (location from the next 1aa4/1ac2 — does the
// revive put us home?). If the reservoir reads dry mid-ladder it bites ONE
// fruit (206837, owner-authorized for def-nha) and logs what one fruit
// pours (1ab5 ballCurPower).
//
// No corpse appears inside the window → clean SKIP verdict; the engine
// still runs on its adaptive ladder, this probe only tunes giDefSipFloor.
//
//	TYTD_LIVE=1 TYTD_GI_DEFNHA=1 [TYTD_GI_DEFNHA_SECS=180] \
//	  TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestGIDefnhaProbeLive -v -timeout 600s .

import (
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

func TestGIDefnhaProbeLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_GI_DEFNHA") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_GI_DEFNHA=1")
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
	window := 180 * time.Second
	if v := os.Getenv("TYTD_GI_DEFNHA_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			window = time.Duration(n) * time.Second
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

	writeChan := make(chan []byte, 256)
	go func() {
		for f := range writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()

	// Shared latest truth of the unit, folded from every source the wire
	// gives: 1aa4 INFO_LOAD answers (authoritative poll), 1ac2 self push,
	// 1aab sip answers, 1ab5 fruit answers.
	type unitState struct {
		powerCur  int64
		powerMax  int64
		powerOk   bool
		revive    int64
		location  int32
		home      int32
		ballCur   int64
		ballOk    bool
		weak      bool  // 1aa4 f19 deadWeakEffect present
		weakValue int64 // f19.value — the panel's revive requirement
		weakOK    bool
	}
	var mu sync.Mutex
	var st unitState
	var lastSipRet int32
	var lastSipPower int64

	// topFields walks ONE top-level protobuf field by number (wt2 payload
	// bytes / wt0 varint) — used to read 1aa4 field 19 deadWeakEffect,
	// which our Go proto has never modelled. GodIsland.decoded.proto:924
	// pins its shape {overlay=1, value=2, expireTime=3}.
	topFields := func(b []byte, num uint64) ([]byte, uint64, bool) {
		for len(b) > 0 {
			key, n := pbConsumeVarint(b)
			if n <= 0 {
				return nil, 0, false
			}
			b = b[n:]
			fnum, wt := key>>3, key&7
			switch wt {
			case 0:
				v, n := pbConsumeVarint(b)
				if n <= 0 {
					return nil, 0, false
				}
				b = b[n:]
				if fnum == num {
					return nil, v, true
				}
			case 1:
				if len(b) < 8 {
					return nil, 0, false
				}
				b = b[8:]
			case 2:
				l, n := pbConsumeVarint(b)
				if n <= 0 || uint64(len(b)) < uint64(n)+l {
					return nil, 0, false
				}
				b = b[n:]
				val := b[:l]
				b = b[l:]
				if fnum == num {
					return val, 0, true
				}
			case 5:
				if len(b) < 4 {
					return nil, 0, false
				}
				b = b[4:]
			default:
				return nil, 0, false
			}
		}
		return nil, 0, false
	}

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
			switch ty {
			case SERVER_GOD_ISLAND_INFO_LOAD:
				var r proto.GodIslandGameInfoRespMsg
				if pb.Unmarshal(d, &r) != nil || r.GetRet() != 0 {
					continue
				}
				// deadWeakEffect (1aa4 f19): present == the dead-weak gate
				// is on; value is THE number the ball panel prints as the
				// revive requirement (owner: "vào bóng linh lực sẽ thấy").
				weak, weakVal, weakOK := false, int64(0), false
				if raw, _, ok := topFields(d, 19); ok {
					weak = true
					if _, v, ok := topFields(raw, 2); ok {
						weakVal, weakOK = int64(v), true
					}
				}
				mu.Lock()
				if r.Home != nil {
					st.home = r.GetHome()
				}
				if r.Location != nil {
					st.location = r.GetLocation()
				}
				if r.ReviveTime != nil {
					st.revive = r.GetReviveTime()
				}
				if r.CurPower != nil {
					if v, ok := parsePower(r.GetCurPower()); ok {
						st.powerCur, st.powerOk = v, true
					}
				}
				if r.MaxPower != nil {
					if v, ok := parsePower(r.GetMaxPower()); ok {
						st.powerMax = v
					}
				}
				st.weak, st.weakValue, st.weakOK = weak, weakVal, weakOK
				mu.Unlock()
			case SERVER_GOD_ISLAND_MYGAMEINFO:
				var r proto.GodIslandGameMyGameInfoSyncMsg
				if pb.Unmarshal(d, &r) != nil {
					continue
				}
				mu.Lock()
				if r.Location != nil {
					st.location = r.GetLocation()
				}
				if r.CurPower != nil {
					if v, ok := parsePower(r.GetCurPower()); ok {
						st.powerCur, st.powerOk = v, true
					}
				}
				if r.MaxPower != nil {
					if v, ok := parsePower(r.GetMaxPower()); ok {
						st.powerMax = v
					}
				}
				mu.Unlock()
			case SERVER_GOD_ISLAND_BALL_INFO:
				// 1aaa ball panel — the ONLY ball source between drinks
				// (1aab/1ab5 only answer actual sips). Without this fold a
				// pre-dead corpse never learns its ball is full and the
				// ladder idles forever (live bug 2026-10-19).
				var r proto.GodIslandGameSpiritualBallInfoRespMsg
				if pb.Unmarshal(d, &r) != nil || r.GetRet() != 0 {
					continue
				}
				mu.Lock()
				if r.BallCurPower != nil {
					if v, ok := parsePower(r.GetBallCurPower()); ok {
						st.ballCur, st.ballOk = v, true
					}
				}
				if r.CurPower != nil {
					if v, ok := parsePower(r.GetCurPower()); ok {
						st.powerCur, st.powerOk = v, true
					}
				}
				mu.Unlock()
			case SERVER_GOD_ISLAND_USE_BALL:
				var r proto.GodIslandGameUseSpiritualBallRespMsg
				if pb.Unmarshal(d, &r) != nil {
					continue
				}
				mu.Lock()
				lastSipRet = r.GetRet()
				if r.CurPower != nil {
					if v, ok := parsePower(r.GetCurPower()); ok {
						st.powerCur, st.powerOk = v, true
					}
				}
				if r.BallCurPower != nil {
					if v, ok := parsePower(r.GetBallCurPower()); ok {
						st.ballCur, st.ballOk = v, true
					}
				}
				mu.Unlock()
				t.Logf("SIP-ANSWER ret=%d curPower=%s ballCurPower=%s",
					r.GetRet(), r.GetCurPower(), r.GetBallCurPower())
			case SERVER_GOD_ISLAND_USE_FRUIT:
				var r proto.GodIslandUseFruitResp
				if pb.Unmarshal(d, &r) != nil {
					continue
				}
				mu.Lock()
				if r.BallCurPower != nil {
					if v, ok := parsePower(r.GetBallCurPower()); ok {
						st.ballCur, st.ballOk = v, true
					}
				}
				mu.Unlock()
				t.Logf("FRUIT-ANSWER ret=%d ballCurPower=%s ballMaxPower=%s",
					r.GetRet(), r.GetBallCurPower(), r.GetBallMaxPower())
			}
		}
	}()
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for range tk.C {
			select {
			case writeChan <- pack.Pack(PING_CMD, []byte{}, uid):
			default:
			}
		}
	}()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)
	time.Sleep(5 * time.Second)

	// The probe needs the activity id of the island the account actually
	// sits on (INFO_LOAD answers ret=0 only for that variant). Discovery
	// via the auth firehose is racy against the reader goroutine above, so
	// the id is passed in explicitly — /my_event_members prints it.
	probeIDs := []int32{}
	if v := os.Getenv("TYTD_GI_DEFNHA_ACTIVITY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			probeIDs = append(probeIDs, int32(n))
		}
	}
	if len(probeIDs) == 0 {
		t.Logf("set TYTD_GI_DEFNHA_ACTIVITY=<activityId> to pin the island; probing blind is pointless — SKIP")
		fmt.Printf("VERDICT-DEFNHA: SKIP — no activity id (owner: /my_event_members shows the GI activity id, pass it via TYTD_GI_DEFNHA_ACTIVITY)\n")
		return
	}
	aid := probeIDs[0]

	deadline := time.Now().Add(window)
	tick := 0
	var deadSince time.Time
	var lastAct time.Time
	phase, expIdx := 0, 0
	var maxDeadCur int64 // widest cur observed while loc==0
	verdict := ""
	// H = the panel's revive requirement. deadWeakEffect.value rides every
	// 1aa4 (owner: the ball panel shows it); 200 was the value seen live
	// 2026-10-19 and doubles as the fallback hypothesis until a frame
	// lands. expT is the honest fallback sweep if both 2-point checks fail.
	hypH := int64(200)
	// Cross-death bisection driver (owner "phải xác định cho chính xác" +
	// "tao làm cho nó chết để mày test"): TYTD_GI_DEFNHA_TEST=<HP> pins the
	// exact single point to verify this run — the machine tops the corpse
	// up to <HP> (must stay dead ⇒ bracket lower leg) and +1 more (stood ⇒
	// width-1 EXACT). Each fresh corpse tests ONE point; I carry the
	// bracket between runs.
	if v := os.Getenv("TYTD_GI_DEFNHA_TEST"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			hypH = n + 1 // phase 0 tests n, phase 1 tests n+1
			t.Logf("SINGLE-POINT MODE: verifying threshold at HP=%d (phase0 dead@%d, phase1 stand@%d)", n, n, n+1)
		}
	}
	expT := []int64{250, 300, 400, 600, 1000, 2000}
	// Owner 2026-10-19: "sẽ tự về base ngay, bơm đủ máu là tự sống" — so
	// the true hypothesis after 2000 FAILED is FULL HP: mx-1 must stay
	// dead and mx must stand. These two rungs are appended once mx known.
	for time.Now().Before(deadline) {
		b, _ := pb.Marshal(&proto.GodIslandGameInfoReqMsg{ActivityId: aid})
		writeChan <- pack.Pack(GOD_ISLAND_INFO_LOAD_CMD, b, uid)
		if tick%20 == 0 {
			b, _ := pb.Marshal(&proto.GodIslandBaseInfoReqMsg{ActivityId: aid})
			writeChan <- pack.Pack(GOD_ISLAND_BASE_INFO_CMD, b, uid)
			b, _ = pb.Marshal(&proto.GodIslandHeartBeatReqMsg{ActivityId: aid})
			writeChan <- pack.Pack(GOD_ISLAND_HEART_BEAT_CMD, b, uid)
			b, _ = pb.Marshal(&proto.GodIslandGameSpiritualBallInfoReqMsg{ActivityId: aid})
			writeChan <- pack.Pack(GOD_ISLAND_BALL_INFO_CMD, b, uid)
		}

		mu.Lock()
		cur, mx, ok := st.powerCur, st.powerMax, st.powerOk
		rev, loc := st.revive, st.location
		reviveLeft := time.Duration(0)
		if rev > 0 {
			ms := rev
			if ms < 1_000_000_000_000 {
				ms *= 1000
			}
			if left := time.Until(time.UnixMilli(ms)); left > 0 {
				reviveLeft = left
			}
		}
		ballCur, ballOk := st.ballCur, st.ballOk
		sipRet, sipPow := lastSipRet, lastSipPower
		home := st.home
		weak, weakValue, weakOK := st.weak, st.weakValue, st.weakOK
		mu.Unlock()
		if weakOK && weakValue > 0 {
			hypH = weakValue // trust the server's own number over the default
		}

		t.Logf("TICK %d: power=%d/%d(ok=%v) reviveLeft=%v location=%d ball=%d(ok=%v)",
			tick, cur, mx, ok, reviveLeft.Round(time.Second), loc, ballCur, ballOk)

		if !ok {
			time.Sleep(3 * time.Second)
			tick++
			continue
		}
		// LIVE-FINDING 2026-10-19 (<live-user>@710564): a unit healed BELOW
		// the deadWeakEffect threshold keeps curPower>0 yet stays dead —
		// location stays 0 and the server keeps pushing reviveTime ahead.
		// So "standing" is location!=0, and the cooldown gate is dropped:
		// the server itself refuses (ret=6852) when a drink is illegal,
		// and a refused drink spends nothing.
		//
		// WHY NO BINARY SEARCH: drinking is ONE-WAY — HP can never go back
		// down, so on a single corpse classic bisection is impossible (you
		// can never re-test a lower mid). The exact protocol on one corpse
		// is 2-point verification of the panel's number H
		// (deadWeakEffect.value, default 200): top up to H-1 → MUST still
		// be dead, then +1 → MUST stand. dead@(H-1) + stood@H brackets the
		// threshold at width 1 ⇒ exact. Falsification paths (199 stood /
		// 200 stayed dead) print an honest bracket and demand a fresh
		// corpse for bisection ACROSS deaths.
		dead := loc == 0
		switch {
		case dead:
			if deadSince.IsZero() {
				deadSince = time.Now()
				t.Logf("CORPSE window opened (panel deadWeakEffect=%v value=%d) — starting 2-point verification H=%d",
					weak, weakValue, hypH)
			}
			if cur > maxDeadCur {
				maxDeadCur = cur // widest DEAD observation — the bracket's lower leg
			}
			if time.Since(lastAct) < 4*time.Second {
				break
			}
			// Calibration never bites fruit — the ball is already full and
			// fruit spending belongs to the engine only.
			if !ballOk || ballCur <= 0 {
				t.Logf("ball reads dry — waiting (probe does not spend fruit)")
				break
			}
			var target int64
			switch {
			case phase == 0:
				target = hypH - 1 // must still be dead here
			case phase == 1:
				target = hypH // must stand here
			default:
				rungs := expT
				if mx > 0 {
					rungs = append(append([]int64{}, expT...), mx-1, mx)
				}
				if expIdx >= len(rungs) {
					verdict = fmt.Sprintf("still DEAD at cur=%d (loc=0) through EVERY target up to maxPower=%d (panel H=%d) — gate is NOT HP at all; needs another mechanism (owner's client eyes)",
						cur, mx, hypH)
					break
				}
				target = rungs[expIdx]
			}
			if cur >= target {
				// The answer for this rung is already in: still loc=0 →
				// dead at ≥target confirmed → advance.
				t.Logf("confirmed DEAD at cur>=%d (loc=0)", target)
				if phase < 2 {
					phase++
				} else {
					expIdx++
				}
				break
			}
			sip := target - cur
			if sip > ballCur {
				sip = ballCur
			}
			mu.Lock()
			lastSipPower = sip
			mu.Unlock()
			b, _ := pb.Marshal(&proto.GodIslandGameUseSpiritualBallReqMsg{ActivityId: aid, UsePower: pb.Int64(sip)})
			writeChan <- pack.Pack(GOD_ISLAND_USE_BALL_CMD, b, uid)
			lastAct = time.Now()
			t.Logf("SIP rung %d: usePower=%d → cur=%d→%d (ball %d)", phase, sip, cur, target, ballCur)
		case loc != 0 && !deadSince.IsZero():
			// Stood up — bracket = (maxDeadCur, cur]. Width 1 ⇔ exact.
			if verdict == "" {
				exact := "NOT exact (width >1) — need a fresh corpse to bisect"
				if cur == maxDeadCur+1 {
					exact = fmt.Sprintf("EXACT: dead@%d + stood@%d ⇒ threshold = %d", maxDeadCur, cur, cur)
				}
				verdict = fmt.Sprintf("STOOD at curPower=%d (last sip %d → ret=%d), location=%d home=%d; widest dead observation cur=%d ⇒ threshold in (%d, %d]; %s; panel deadWeakEffect=%v value=%d",
					cur, sipPow, sipRet, loc, home, maxDeadCur, maxDeadCur, cur, exact, weak, weakValue)
			}
		}
		if verdict != "" && !deadSince.IsZero() {
			break
		}
		time.Sleep(3 * time.Second)
		tick++
	}

	if deadSince.IsZero() {
		fmt.Printf("VERDICT-DEFNHA: SKIP — no corpse window for activity %d in %v (unit stayed alive/offline); measure during a real death.\n",
			aid, window)
		return
	}
	if verdict == "" {
		verdict = fmt.Sprintf("window closed (%v) while still dead at cur=%d loc=0 (panel H=%d) — bracket (%d, H?] unresolved; fresh corpse needed for cross-death bisection",
			time.Since(deadSince).Round(time.Second), maxDeadCur, hypH, maxDeadCur)
	}
	fmt.Printf("VERDICT-DEFNHA: %s\n", verdict)
}
