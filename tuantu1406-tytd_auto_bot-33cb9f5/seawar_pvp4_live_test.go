//go:build live

package tytd_go

// OWNER-ORDERED live PVP hunt — owner 2026-11-10 (ICT):
// "cầm acc đó lên tầng 4 mà gặp người đi, hết thể lực thì cắn quả mà đánh tiếp".
//
// Drives <live-user>@710547 (actID 9063858) through the production swPlayTick
// until it reaches floor 4 (region 10004) and engages player events, and
// when strength runs out it fires the energy fruit (234922 USE_ITEM) on the
// OWNER'S DIRECT ORDER — the one-shot authorized press of
// [[tytd-item-ban]] exception (11). The AUTO engine still never calls
// swUseItemOrder; this test only ever fires fruit from its own loop, and it
// HARD-ASSERTS every 95aa frame on the wire corresponds to one of its own
// explicit calls (an auto-path leak would trip "UNAUTHORIZED fruit frame").
// The guild-war family 0x95b3..0x95c0 stays hard-banned.
//
// Detection is wire-level via a test-only tap (p.swTap, nil in production):
//   - "gặp người"  = 959b handle frame carrying action=CHALLENGE
//   - "gọi đồng đội" = 959b frame carrying action=REQUEST_MARK
//
// One foreground chunk is time-boxed (default 8 min) so each `go test` call
// returns: TYTD_SEAWAR_MIN=minutes per run, TYTD_SEAWAR_FRUIT=fruit budget
// this run (max 20), TYTD_SEAWAR_ROUNDS=max rounds. Region and strength are
// server-side state — re-run the chunk to continue the hunt.
//
//	TYTD_LIVE=1 TYTD_SEAWAR_PVP=1 go test -tags "sqlcipher live" -count=1 -run TestSeaWarPVP4Live -v -timeout 15m .

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSeaWarPVP4Live(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_SEAWAR_PVP") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_SEAWAR_PVP=1")
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
	maxFruit := envInt("TYTD_SEAWAR_FRUIT", 5)    // fruit units this run (num per bite)
	totalFruit := envInt("TYTD_SEAWAR_TOTAL", 20) // hard cap per session
	// TYTD_SEAWAR_PINH pins the ENGINE clock (test-only swNowFunc override,
	// same trick as at() in the unit suite) so the dump/farm law paths can be
	// exercised outside their real ICT window. The server knows nothing about
	// bot phases — the activity itself must still be genuinely open.
	if h := os.Getenv("TYTD_SEAWAR_PINH"); h != "" {
		hh, err := strconv.Atoi(h)
		if err != nil || hh < 0 || hh > 23 {
			t.Fatalf("bad TYTD_SEAWAR_PINH=%q", h)
		}
		old := swNowFunc
		swNowFunc = func() time.Time {
			return time.Date(2026, 11, 11, hh, 5, 0, 0, ictZone)
		}
		defer func() { swNowFunc = old }()
		t.Logf("⏰ engine clock pinned to %02d:05 ICT (dump-window rehearsal)", hh)
	}
	maxRounds := envInt("TYTD_SEAWAR_ROUNDS", 12)
	chunkMin := envInt("TYTD_SEAWAR_MIN", 8)
	if maxFruit > 20 {
		maxFruit = 20
	}

	log.SetOutput(os.Stderr)

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
	db, _ := gorm.Open(sqlite.Open("file:swpvp4?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256),
		AutoSeaWar: true}

	// ── wire accounting ──
	var sendMu sync.Mutex
	sentTypes := map[string]int{}
	ourFruitCalls := 0 // incremented ONLY by our own authorized swUseItemOrder below
	var unauthFruit []string
	// eventId → eventType, learned from enter_game/explore/handle RESPONSES
	// (ECelestialSeaWarEventType: 1=monster 2=player 3=box 4=helpBox 5=forge
	// 6=gift 7=spring). Only the response carries the type; the handle
	// request carries just the id — without this map a monster challenge is
	// indistinguishable from a player one on the wire.
	var evMu sync.Mutex
	evTypes := map[int32]int32{}
	learn := func(ev *proto.SeaWarEventDataMsg) {
		if ev == nil || ev.GetEventId() == 0 {
			return
		}
		evMu.Lock()
		evTypes[ev.GetEventId()] = ev.GetEventType()
		evMu.Unlock()
	}
	var pvpFrames, monsterFrames, markFrames, assistFrames, liquidFrames, condFrames, donateFrames int32
	swTap = func(ty string, req, resp []byte) {
		switch ty {
		case SERVER_SW_ENTER_GAME:
			var r proto.SeaWarEnterGameResp
			if pb.Unmarshal(resp, &r) == nil {
				learn(r.GetCurEventData())
			}
		case SERVER_SW_EXPLORE:
			var r proto.SeaWarExploreResp
			if pb.Unmarshal(resp, &r) == nil {
				learn(r.GetEventData())
			}
		case SERVER_SW_HANDLE_EVENT:
			var rq proto.SeaWarHandleEventReq
			if pb.Unmarshal(req, &rq) != nil {
				return
			}
			var rs proto.SeaWarHandleEventResp
			if pb.Unmarshal(resp, &rs) == nil {
				learn(rs.GetEventData())
			}
			evMu.Lock()
			typ := evTypes[rq.GetEventId()]
			evMu.Unlock()
			switch rq.GetAction() {
			case 3:
				if typ == 2 {
					atomic.AddInt32(&pvpFrames, 1)
					t.Logf("⚔️  ĐÁNH NGƯỜI: challenge frame eventId=%d", rq.GetEventId())
				} else {
					atomic.AddInt32(&monsterFrames, 1)
				}
			case 4:
				atomic.AddInt32(&markFrames, 1)
				t.Logf("📣 REQUEST_MARK frame (chưa hạ được — gọi đồng đội): eventId=%d", rq.GetEventId())
			}
		case SERVER_SW_ASSIST_REWARD:
			atomic.AddInt32(&assistFrames, 1)
			var r proto.SeaWarAssistRewardReq
			if pb.Unmarshal(req, &r) == nil {
				t.Logf("🤝 ASSIST_REWARD frame: type=%d id=%d", r.GetType(), r.GetId())
			}
		case SERVER_SW_UNION_SEND_LIQUID:
			// AUTO PHÁT linh lộ (owner "phát hết linh lộ đang có"): ret=0 =
			// donated one 105531; the run must stop at the first ret≠0.
			var rs proto.SeaWarUnionLiquidSendResp
			pb.Unmarshal(resp, &rs)
			atomic.AddInt32(&donateFrames, 1)
			t.Logf("🎁 PHÁT linh lộ: ret=%d", rs.GetRet())
		case SERVER_ACTIVITY_CROSS_RECHARGE:
			// Nạp-bang milestone rung (21045 → 0415).
			var rq proto.ReceiveCrossUnionRechargeRewardReq
			var rs proto.RspGetActivityConditionReward
			pb.Unmarshal(req, &rq)
			pb.Unmarshal(resp, &rs)
			atomic.AddInt32(&condFrames, 1)
			t.Logf("🎁 MỐC NẠP BANG: act=%d cond=%d ret=%d rewards=%q",
				rq.GetActivityId(), rq.GetConditionId(), rs.GetRet(), rs.GetRewards())
		case SERVER_SW_UNION_RECEIVE_LIQUID:
			// Linh lộ "Nhận nhanh" accounting: ONE claim-all (id=0) per tick
			// with a real ret — the old per-notice storm (341×, ret silent,
			// nothing consumed) must be gone.
			atomic.AddInt32(&liquidFrames, 1)
			var rq proto.SeaWarUnionLiquidRewardReq
			pb.Unmarshal(req, &rq)
			ret := int32(-999) // no generated Resp for 34916 — decode field 1 by hand
			if num, _, n := protowire.ConsumeTag(resp); n > 0 && num == 1 {
				if v, m := protowire.ConsumeVarint(resp[n:]); m > 0 {
					ret = int32(v)
				}
			}
			t.Logf("💧 LINH LỘ claim-all: id=%d present=%v ret=%d respLen=%d",
				rq.GetId(), rq.Id != nil, ret, len(resp))
		}
	}
	go func() {
		for f := range p.writeChan {
			_, ty, _ := pack.Unpack(f)
			sendMu.Lock()
			sentTypes[ty]++
			if ty == "95aa" && sentTypes["95aa"] > ourFruitCalls {
				unauthFruit = append(unauthFruit, ty) // auto path leaked a fruit frame
			}
			if len(ty) == 4 {
				if n, err := strconv.ParseInt(ty, 16, 32); err == nil && n >= 0x95b3 && n <= 0x95c0 {
					unauthFruit = append(unauthFruit, ty) // guild-war family: hard ban
				}
			}
			sendMu.Unlock()
			_ = conn.WriteMessage(2, f)
		}
	}()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			isMSG, msgType, realData := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.ActList.mergeDetail(p.Username, realData)
				p.swMergeConfig(realData)
				p.stDeliver(msgType, realData)
			case SERVER_ACTIVITY_BUY_GOODS, SERVER_ACTIVITY_CROSS_RECHARGE, SERVER_SW_UNION_SEND_LIQUID:
				p.stDeliver(msgType, realData)
			case SERVER_SW_ENTER, SERVER_SW_ENTER_GAME, SERVER_SW_EXPLORE,
				SERVER_SW_HANDLE_EVENT, SERVER_SW_UNION_WELFARE, SERVER_SW_UNION_WELFARE_REWARD,
				SERVER_SW_UNION_RECEIVE_LIQUID, SERVER_SW_HEART_BEAT, SERVER_SW_NEXT_REGION,
				SERVER_SW_USE_ITEM, SERVER_SW_WORSHIP,
				SERVER_SW_ASSIST_LIST, SERVER_SW_ASSIST, SERVER_SW_ASSIST_REWARD,
				SERVER_SW_ASSIST_SYNC, SERVER_SW_ASSIST_SYNC_RAW:
				p.swHandleFrame(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-readerDone }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(30 * time.Second)
	var actID int32
	for time.Now().Before(deadline) {
		now := time.Now()
		for _, e := range p.ActList.byType(swActivityType) {
			if st := e.status(now); st == "active" || st == "reward" {
				actID = e.ActivityID
			}
		}
		if actID != 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if actID == 0 {
		t.Fatal("no active type-349 activity")
	}
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if data, err := p.stRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body, 15*time.Second); err == nil {
			p.swMergeConfig(data)
		}
	}
	p.SwMu.Lock()
	t.Logf("config folded: energyItem=%d balls=%d regions=%d — fruit budget=%d×%d (cap %d), chunk=%dm, rounds=%d",
		p.swEnergyItem, len(p.swBallEffect), len(p.swRegionEnergy), maxFruit, 1, totalFruit, chunkMin, maxRounds)
	p.SwMu.Unlock()

	fruitEaten := 0
	roundStart := time.Now()
	goalHit := false
	for round := 1; round <= maxRounds; round++ {
		eg, err := p.swEnterGame(actID)
		if err != nil || eg.GetRet() != 0 {
			t.Logf("round %d: enter_game not ready (err=%v ret=%d) — retrying tick anyway", round, err, eg.GetRet())
		}
		region, strength := int32(0), int32(0)
		if pd := eg.GetPlayerData(); pd != nil {
			region, strength = pd.GetRegionId(), pd.GetStrength()
		}
		t.Logf("round %d: region=%d strength=%d fruit=%d/%d ⚔️%v 📣%v 🤝%v elapsed=%dm%ds",
			round, region, strength, fruitEaten, totalFruit,
			atomic.LoadInt32(&pvpFrames) > 0, atomic.LoadInt32(&markFrames) > 0,
			atomic.LoadInt32(&assistFrames) > 0,
			int(time.Since(roundStart).Minutes()), int(time.Since(roundStart).Seconds())%60)

		// Fresh exhaustion notice every round (engine dedupes by day+region;
		// clearing the key is a test-only affordance for cleaner logs).
		p.SwMu.Lock()
		p.swExhaustKey = ""
		p.SwMu.Unlock()

		swPlayTick(p, actID)

		// Re-read the board after the production tick.
		eg, err = p.swEnterGame(actID)
		if err != nil || eg.GetRet() != 0 || eg.GetPlayerData() == nil {
			t.Logf("round %d post-tick read failed (err=%v ret=%d) — sleeping 20s", round, err, eg.GetRet())
			time.Sleep(20 * time.Second)
			continue
		}
		pd := eg.GetPlayerData()
		region, strength = pd.GetRegionId(), pd.GetStrength()
		t.Logf("round %d after tick: region=%d strength=%d", round, region, strength)

		if region >= 10004 && (atomic.LoadInt32(&pvpFrames) > 0 || atomic.LoadInt32(&markFrames) > 0) {
			goalHit = true
			t.Logf("✅ GOAL: floor 4 reached AND player engagement happened")
			break
		}

		// Time box the chunk so the foreground call returns on its own.
		if time.Since(roundStart) >= time.Duration(chunkMin)*time.Minute {
			t.Logf("⏱️ chunk window (%dm) reached — re-run to continue the hunt", chunkMin)
			break
		}

		// Owner's order: "hết thể lực thì cắn quả mà đánh tiếp".
		// "Hết thể lực" = không còn đủ cho một lượt khám phá (live: acc kẹt
		// strength=1 < cost tầng 3 — explore bị server từ chối vĩnh viễn).
		need := int32(1)
		if v := p.swCfg().regionEnergy[region]; v > 0 {
			need = v
		}
		if strength < need {
			if fruitEaten >= totalFruit {
				t.Logf("🍏 fruit budget (%d) exhausted — stopping per cap", totalFruit)
				break
			}
			n := int32(maxFruit)
			if rem := totalFruit - fruitEaten; n > int32(rem) {
				n = int32(rem)
			}
			sendMu.Lock()
			ourFruitCalls++ // authorize exactly this one frame before firing
			sendMu.Unlock()
			if err := p.swUseItemOrder(n); err != nil {
				t.Logf("🍏 fruit order failed: %v", err)
			} else {
				fruitEaten += int(n)
				t.Logf("🍏 bit %d energy fruits (total %d/%d) — resuming", n, fruitEaten, totalFruit)
			}
			time.Sleep(3 * time.Second)
			continue
		}
		time.Sleep(5 * time.Second)
	}

	enter, _ := p.swEnter(actID)
	t.Logf("final ENTER: ret=%d region=%d", enter.GetRet(), enter.GetRegionId())
	p.SwMu.Lock()
	t.Logf("forge picks: %d balls used", len(p.swBallEffect))
	p.SwMu.Unlock()
	t.Logf("🎁 PHÁT linh lộ: %d presses (dừng ở ret≠0 đầu tiên)",
		atomic.LoadInt32(&donateFrames))
	t.Logf("🎁 NẠP BANG: %d rung frames (21045, server ret là trọng tài)",
		atomic.LoadInt32(&condFrames))
	t.Logf("💧 LINH LỘ: %d claim-all frames", atomic.LoadInt32(&liquidFrames))
	t.Logf("sent frame types: %v", sentTypes)
	if n := atomic.LoadInt32(&pvpFrames); n > 0 {
		t.Logf("🎯 GẶP NGƯỜI: %d challenge lên người (quái: %d)", n, atomic.LoadInt32(&monsterFrames))
	} else {
		t.Logf("chưa gặp người trong chunk này (quái: %d)", atomic.LoadInt32(&monsterFrames))
	}
	if atomic.LoadInt32(&markFrames) > 0 {
		t.Logf("🎯 CẦU VIỆN: %d REQUEST_MARK frames", atomic.LoadInt32(&markFrames))
	}
	if atomic.LoadInt32(&assistFrames) > 0 {
		t.Logf("🎯 HỖ TRỢ: %d assist-reward frames", atomic.LoadInt32(&assistFrames))
	}
	sendMu.Lock()
	defer sendMu.Unlock()
	if len(unauthFruit) > 0 {
		t.Fatalf("BAN VIOLATION — frames that had NO business leaving the socket: %v (fruit calls were %d)", unauthFruit, ourFruitCalls)
	}
	if !goalHit {
		t.Logf("chunk finished without the goal — re-run to continue (server keeps region/strength)")
	}
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
