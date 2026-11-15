package tytd_go

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Luân Hồi Bí Cảnh auto: phase calendar, window gate, qualification, and
// the in-window turn against injected answers — mirroring the caravan's
// test idiom (stLoopPlayer / stExpectCmd / stAnswer).

// forbidden reborn ops: USE_ITEM (217208), USE_MAGIC (217207), UP_LEVEL
// (217204), SEND_BONUS_ITEM (217223), GUESS_SELECT (217227) — the last is
// a betting decision, never a free op, but the bot abstains by design.
// 217207 USE_MAGIC allowed (cd-bound, free). 217223 SEND_BONUS_ITEM allowed
// since 2026-10-10 ONLY via rbtLiquidGrant — guarded by tests below: no
// other file may reference RBT_SEND_BONUS_ITEM_CMD, and the grant fires
// only with config-folded ids (empty in scripted tests → opcode never sent).
var rbtForbidden = []string{"00035078", "00035074", "0003508b"}

func rbtMemDB(t *testing.T, autoOn bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:lanhoi_%s_%v?mode=memory&cache=shared", t.Name(), autoOn)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.User{ID: "1", Username: "u", GameUID: 42, AutoRebornTrial: autoOn}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func rbtLoopPlayer(t *testing.T, autoOn bool) *Player {
	t.Helper()
	now := time.Now()
	p := &Player{
		Username:     "u",
		UID:          42,
		DB:           rbtMemDB(t, autoOn),
		ChatID:       7,
		Notify:       true,
		NotiChan:     make(chan NotifyMGS, 32),
		writeChan:    make(chan []byte, 64),
		rbtPoll:      80 * time.Millisecond,
		rbtStatusGap: 5 * time.Second, // heartbeat off for these tests
		rbtTreeGap:   time.Hour,       // tree on its own cadence — dedicated tests
		// Opt-in gate is a Player field (owner 2026-10-27).
		AutoRebornTrial: autoOn,
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	t.Cleanup(func() { cancel(); rbtWindowTest = nil })
	// Season activity opened 40 h ago → inside Battle round 1 (34h–46h).
	setActivities(&p.ActList, activityEntry{
		ActivityID: 1720, Type: rbtActivityType,
		BeginTime: now.Add(-40 * time.Hour).UnixMilli(),
		EndTime:   now.Add(50 * time.Hour).UnixMilli(),
	})
	// The shop sweep pulls 21003 when it has no table yet — that extra
	// frame would desync these loop tests' strict command asserts. Seed a
	// single FORBIDDEN recharge row (bare numeric price): the sweep then
	// has a table (no pull) and buys nothing from it — which also proves
	// money rows are never touched.
	p.rbtMallRows = []*proto.ActivityMallTempMsg{{
		Id: pb.Int32(180001632), Name: pb.String("Thần Vực Tiên Duyên"),
		Price: pb.String("12800"), Param: pb.String("1|0|0|0"),
	}}
	return p
}

func rbtExpectCmd(t *testing.T, p *Player, want string, within time.Duration) []byte {
	t.Helper()
	select {
	case f := <-p.writeChan:
		if ft := frameFuncType(t, f); ft != want {
			t.Fatalf("sent %s, want %s", ft, want)
		}
		return f
	case <-time.After(within):
		t.Fatalf("no %s frame within %v", want, within)
		return nil
	}
}

func rbtAnswer(t *testing.T, p *Player, msgType string, m pb.Message) {
	t.Helper()
	data, err := pb.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p.rbtHandleFrame(msgType, data)
}

// rbtDrainBlind consumes the BLIND maintenance arm the loop fires on every
// wake (owner 2026-11-07 "cần poll mà nhận, không cần đợi check"): harvest
// 217221 + claim-all water 217222 fire with NO board pre-read, and
// rbtCondSweep blind-pulls 21003 while the config fold is still empty.
// Every answer is a refusal → the arm is a no-op and the gameplay-scripted
// expectations start clean. (217223 grant never appears here — scripted
// tests never fold liquid ids, so the opcode stays config-armed-off.)
func rbtDrainBlind(t *testing.T, p *Player) {
	t.Helper()
	rbtExpectCmd(t, p, RBT_BONUS_RESULT_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_BONUS_RESULT, &proto.RebornTrialGetBonusResultResp{Ret: 17212}) // tree not ripe
	rbtExpectCmd(t, p, RBT_BONUS_ITEM_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_BONUS_ITEM, &proto.RebornTrialGetBonusItemResp{Ret: 1}) // nothing ready
	rbtExpectCmd(t, p, ACTIVITY_GET_DATA_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_ACTIVITY_DETAIL, &proto.RspGetActivityDetail{Ret: 0}) // folds nothing → sweep quiet
}

// rbAssertBodyAct checks the {activityId=1} varint field of a sent frame.
func rbAssertBodyAct(t *testing.T, frame []byte, act int32) {
	t.Helper()
	var req proto.RebornTrialActivityReq
	if err := pb.Unmarshal(frame[18:], &req); err != nil || req.GetActivityId() != act {
		t.Fatalf("body %x does not carry activityId %d", frame[18:], act)
	}
}

func TestRbtPhase(t *testing.T) {
	mk := func(off time.Duration) int64 { return time.Now().Add(off).UnixMilli() }
	// begin = 40h ago (set by rbtLoopPlayer fixture — reuse a local one):
	begin := mk(-40 * time.Hour)
	if st, round, _ := rbtPhase(begin, nil, time.Now()); st != rbtBattle || round != 1 {
		t.Fatalf("40h in: state %v round %d, want battle round 1", st, round)
	}
	begin = mk(-20 * time.Hour)
	if st, _, _ := rbtPhase(begin, nil, time.Now()); st != rbtPrep {
		t.Fatalf("20h in (SignIn): state %v, want prep", st)
	}
	begin = mk(-30 * time.Hour)
	if st, _, _ := rbtPhase(begin, nil, time.Now()); st != rbtPrep {
		t.Fatalf("30h in (Prepare): state %v, want prep", st)
	}
	begin = mk(-50 * time.Hour) // past R1 (46h), gap before R2 at 58h
	if st, _, _ := rbtPhase(begin, nil, time.Now()); st != rbtPrep {
		t.Fatalf("50h in (rest gap): state %v, want prep", st)
	}
	begin = mk(-90 * time.Hour) // inside R3 (82h–94h)
	if st, round, _ := rbtPhase(begin, nil, time.Now()); st != rbtBattle || round != 3 {
		t.Fatalf("90h in: state %v round %d, want battle round 3", st, round)
	}
	begin = mk(-80 * time.Hour) // rest gap between R2 and R3
	if st, _, _ := rbtPhase(begin, nil, time.Now()); st != rbtPrep {
		t.Fatalf("80h in (rest gap): state %v, want prep", st)
	}
	begin = mk(-100 * time.Hour)
	if st, _, _ := rbtPhase(begin, nil, time.Now()); st != rbtDone {
		t.Fatalf("100h in (season over): state %v, want done", st)
	}
}

func TestRbtDailyWindow(t *testing.T) {
	open, close_ := 19*3600, 22*3600
	in, next := rbtDailyWindow(time.Date(2026, 10, 8, 20, 0, 0, 0, ictZone), open, close_)
	if !in {
		t.Fatal("20:00 ICT must be inside the 19–22 window")
	}
	in, next = rbtDailyWindow(time.Date(2026, 10, 8, 23, 0, 0, 0, ictZone), open, close_)
	if in {
		t.Fatal("23:00 ICT must be outside the window")
	}
	if got := next.In(ictZone); got.Format("15:04") != "19:00" || got.Day() != 9 {
		t.Fatalf("next open %s, want 19:00 on the 9th", got)
	}
}

func TestRbtMergeConfig(t *testing.T) {
	p := &Player{Username: "u"}
	uc, err := pb.Marshal(&proto.RebornTrialConfigSync{
		ActivityId: 1720,
		TimeConfig: []*proto.RebornTrialTimeConfig{
			{TimeId: pb.Int32(1), Type: pb.Int32(3), BeginTime: pb.Int32(34), DurationTime: pb.Int32(12)},
			{TimeId: pb.Int32(2), Type: pb.Int32(1), BeginTime: pb.Int32(0), DurationTime: pb.Int32(24)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := pb.Marshal(&proto.RspGetActivityDetail{Activity: &proto.ActivityCommonData{
		ActivityId: pb.Int32(1720),
		DetailConfig: &proto.ActivityDetailConfig{
			CommonConfig: &proto.ActivityCommonConfig{
				MainConfig: &proto.ActivityMainConfig{Type: pb.Int32(rbtActivityType)},
				ParamConfig: []*proto.ActivityParamConfig{
					{Param: pb.String("RENBORNTRIAL_CHALLENGE_DURATION"), Value: pb.String("20|23")},
				},
			},
			UniqueConfig: uc,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p.rbtMergeConfig(detail)
	if o, c := p.rbtWindow(); o != 20*3600 || c != 23*3600 {
		t.Fatalf("window %d–%d, want 20h–23h", o, c)
	}
	// Live 2026-10-08: the server's uniqueConfig timeConfig is NOT hours —
	// folding it once mis-shifted the calendar (round 0 vs the server's
	// round 1). The loop must therefore run on the client default table
	// ONLY; a merge that changes it is a regression.
	if cfg := p.rbtCfg(); len(cfg) != 0 {
		t.Fatalf("phase table must stay empty (default calendar), got %v", cfg)
	}
}

// The full in-window turn: gate → map load → refused AUTO batch → resolve
// the affordable option only → march until the refusal → worship once →
// claims → and NOT a single item/magic/level/bet frame in sight.
func TestRbtAutoLoopTurn(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true)
	go p.rebornTrialAutoLoopAfter(0)

	rbtDrainBlind(t, p)

	ea := rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second)
	rbAssertBodyAct(t, ea, 1720)
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{
		Ret: 0, UnionId: pb.Int64(777), SelfRank: pb.Int32(5), Round: pb.Int32(1),
	})

	en := rbtExpectCmd(t, p, RBT_ENTER_CMD, 3*time.Second)
	rbAssertBodyAct(t, en, 1720)
	rbtAnswer(t, p, SERVER_RBT_ENTER, &proto.RebornTrialDataResp{Ret: 0, PlayerData: &proto.RebornTrialPlayerData{
		Level: pb.Int32(2), Exp: pb.Int64(50), Hp: pb.Int32(3), BattleNum: pb.Int32(0),
		Event: &proto.RebornTrialEvent{Stat: pb.Int32(0), Type: pb.Int32(1), Option: []*proto.RebornTrialEventOption{
			{Order: pb.Int32(1), CostExp: pb.Int64(100)}, // too rich — must be SKIPPED, never sent
			{Order: pb.Int32(2), CostExp: pb.Int64(0)},
		}},
	}})

	// AUTO refused (server gate): fall back to the manual march.
	au := rbtExpectCmd(t, p, RBT_AUTO_EXPLORE_CMD, 3*time.Second)
	var autoReq proto.RebornTrialAutoExploreReq
	if err := pb.Unmarshal(au[18:], &autoReq); err != nil || len(autoReq.GetSettingItems()) != 10 {
		t.Fatalf("auto body must carry the 10 client default rows, got %v", err)
	}
	rbtAnswer(t, p, SERVER_RBT_AUTO_EXPLORE, &proto.RebornTrialAutoExploreRsp{Ret: 21})

	sel := rbtExpectCmd(t, p, RBT_SELECT_OPTION_CMD, 3*time.Second)
	var sreq proto.RebornTrialSelectOptionReq
	if err := pb.Unmarshal(sel[18:], &sreq); err != nil || sreq.GetSelectIndex() != 2 {
		t.Fatalf("must choose affordable option 2, got %v %v", sreq.GetSelectIndex(), err)
	}
	rbtAnswer(t, p, SERVER_RBT_SELECT_OPTION, &proto.RebornTrialSelectOptionResp{
		Ret: 0, Exp: pb.Int64(50), Hp: pb.Int32(3), Event: &proto.RebornTrialEvent{Stat: pb.Int32(1)},
	})

	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: 0, PlayerData: &proto.RebornTrialPlayerData{
		Level: pb.Int32(2), Exp: pb.Int64(50), Hp: pb.Int32(2),
	}})
	rbtExpectCmd(t, p, RBT_EXPLORE_CMD, 3*time.Second) // first step answered ok → next step fires
	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: 7})

	rbtExpectCmd(t, p, RBT_WORSHIP_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_WORSHIP, &proto.RebornTrialWorshipResp{Ret: 0, Reward: pb.String("50 điểm")})
	rbtExpectCmd(t, p, RBT_GUESS_DATA_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_GUESS_DATA, &proto.RebornTrialGuessDataResp{Ret: 0, IsReceive: pb.Bool(true)})

	// Worship made it to the owner.
	deadline := time.After(3 * time.Second)
	sawWorship := false
	for !sawWorship {
		select {
		case n := <-p.NotiChan:
			if strings.Contains(n.MSG, "bái") {
				sawWorship = true
			}
		case <-deadline:
			t.Fatal("no worship notification")
		}
	}
}

// A second in-window tick must NOT worship again (once per round).
func TestRbtWorshipOncePerRound(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true)
	p.rbtWorshipped = map[string]bool{"1720:1": true}
	go p.rebornTrialAutoLoopAfter(0)

	rbtDrainBlind(t, p)
	rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{Ret: 0, UnionId: pb.Int64(777), Round: pb.Int32(1)})
	rbtExpectCmd(t, p, RBT_ENTER_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER, &proto.RebornTrialDataResp{Ret: 0, PlayerData: &proto.RebornTrialPlayerData{Hp: pb.Int32(1)}})
	rbtExpectCmd(t, p, RBT_AUTO_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_AUTO_EXPLORE, &proto.RebornTrialAutoExploreRsp{Ret: 30})
	rbtExpectCmd(t, p, RBT_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: 9})
	// WORSHIP skipped by the once-per-round flag; the tree lives on the
	// loop's own cadence now, so the tick ends at GUESS_DATA.
	rbtExpectCmd(t, p, RBT_GUESS_DATA_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_GUESS_DATA, &proto.RebornTrialGuessDataResp{Ret: 0})

	time.Sleep(250 * time.Millisecond) // let a second tick run through
	for {
		select {
		case f := <-p.writeChan:
			if ft := frameFuncType(t, f); ft == RBT_WORSHIP_CMD {
				t.Fatal("worship sent twice in one round")
			}
		default:
			return
		}
	}
}

// unionId 0 (no guild): report once, touch nothing else.
func TestRbtQualificationBlocked(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true)
	go p.rebornTrialAutoLoopAfter(0)

	rbtDrainBlind(t, p)
	rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{Ret: 0, UnionId: pb.Int64(0), SelfRank: pb.Int32(13)})

	n := <-p.NotiChan
	if !strings.Contains(n.MSG, "chưa có bang") {
		t.Fatalf("notify %q, want qualification notice", n.MSG)
	}
	time.Sleep(300 * time.Millisecond)
	for {
		select {
		case f := <-p.writeChan:
			if ft := frameFuncType(t, f); ft == RBT_ENTER_CMD || ft == RBT_EXPLORE_CMD {
				t.Fatal("map traffic while unqualified")
			}
		default:
			return
		}
	}
}

// Live 2026-10-09 proved the daily 19|22 param does NOT gate map play —
// what gates it is the ROUND. Inside a Battle round the loop must attempt
// play at any hour; outside (Prepare phase) it must stay off the map and
// only tend the tree.
func TestRbtRoundGatesNotHour(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return false } // hour gate would say no
	p := rbtLoopPlayer(t, true)
	go p.rebornTrialAutoLoopAfter(0)
	rbtDrainBlind(t, p)
	rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second) // round 1 → play despite the fake clock
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{Ret: 0, UnionId: pb.Int64(777), Round: pb.Int32(1)})
	rbtExpectCmd(t, p, RBT_ENTER_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER, &proto.RebornTrialDataResp{Ret: 15001}) // server itself refuses → quiet
}

func TestRbtPreparePhaseSilence(t *testing.T) {
	p := rbtLoopPlayer(t, true)
	// Re-anchor the fixture to the Prepare phase (begin 30h ago: 24–34h).
	setActivities(&p.ActList, activityEntry{
		ActivityID: 1720, Type: rbtActivityType,
		BeginTime: time.Now().Add(-30 * time.Hour).UnixMilli(),
		EndTime:   time.Now().Add(60 * time.Hour).UnixMilli(),
	})
	go p.rebornTrialAutoLoopAfter(0)
	rbtDrainBlind(t, p)
	time.Sleep(300 * time.Millisecond)
	select {
	case f := <-p.writeChan:
		t.Fatalf("map traffic during Prepare: %s", frameFuncType(t, f))
	default:
	}
}

// Opt-out gate: no traffic at all.
func TestRbtLoopOff(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, false)
	go p.rebornTrialAutoLoopAfter(0)
	time.Sleep(400 * time.Millisecond)
	select {
	case f := <-p.writeChan:
		t.Fatalf("traffic with switch off: %s", frameFuncType(t, f))
	default:
	}
}

// The hard item ban: every frame the loop ever sends must avoid the five
// spend/bet opcodes (asserted over the full scripted turn above).
func TestRbtNoForbiddenOps(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true)
	go p.rebornTrialAutoLoopAfter(0)
	seen := map[string]bool{}
	am := answers()
	am[RBT_BONUS_ITEM_CMD] = &proto.RebornTrialGetBonusItemResp{Ret: 1}
	am[ACTIVITY_GET_DATA_CMD] = &proto.RspGetActivityDetail{Ret: 0}
	am[RBT_ENTER_CMD] = &proto.RebornTrialDataResp{Ret: 0, PlayerData: &proto.RebornTrialPlayerData{Hp: pb.Int32(1)}}
	for i := 0; i < 40; i++ {
		select {
		case f := <-p.writeChan:
			ft := frameFuncType(t, f)
			seen[ft] = true
			for _, bad := range rbtForbidden {
				if ft == bad {
					t.Fatalf("FORBIDDEN opcode sent: %s", bad)
				}
			}
			if m, ok := am[ft]; ok && serverTypeFor(ft) != "" {
				rbtAnswer(t, p, serverTypeFor(ft), m)
			}
		case <-time.After(600 * time.Millisecond):
			i = 40 // drained: no more traffic expected
		}
	}
	if len(seen) < 3 {
		t.Fatalf("loop never played: %v", seen)
	}
}

// answers/serve helpers keep the scripted mock above readable.
func answers() map[string]pb.Message {
	return map[string]pb.Message{
		RBT_ENTER_ACTIVITY_CMD: &proto.RebornTrialEnterActivityResp{Ret: 0, UnionId: pb.Int64(777), Round: pb.Int32(1)},
		RBT_ENTER_CMD:          &proto.RebornTrialDataResp{Ret: 0, PlayerData: &proto.RebornTrialPlayerData{Hp: pb.Int32(0)}},
		RBT_AUTO_EXPLORE_CMD:   &proto.RebornTrialAutoExploreRsp{Ret: 30},
		RBT_EXPLORE_CMD:        &proto.RebornTrialDataResp{Ret: 9},
		RBT_WORSHIP_CMD:        &proto.RebornTrialWorshipResp{Ret: 17221},
		RBT_BONUS_RESULT_CMD:   &proto.RebornTrialGetBonusResultResp{Ret: 0},
		RBT_GUESS_DATA_CMD:     &proto.RebornTrialGuessDataResp{Ret: 0, IsReceive: pb.Bool(true)},
	}
}

func serverTypeFor(cmd string) string {
	switch cmd {
	case RBT_ENTER_ACTIVITY_CMD:
		return SERVER_RBT_ENTER_ACTIVITY
	case RBT_ENTER_CMD:
		return SERVER_RBT_ENTER
	case RBT_AUTO_EXPLORE_CMD:
		return SERVER_RBT_AUTO_EXPLORE
	case RBT_EXPLORE_CMD:
		return SERVER_RBT_EXPLORE
	case RBT_WORSHIP_CMD:
		return SERVER_RBT_WORSHIP
	case RBT_BONUS_RESULT_CMD:
		return SERVER_RBT_BONUS_RESULT
	case RBT_BONUS_ITEM_CMD:
		return SERVER_RBT_BONUS_ITEM
	case ACTIVITY_GET_DATA_CMD:
		return SERVER_ACTIVITY_DETAIL
	case RBT_GUESS_DATA_CMD:
		return SERVER_RBT_GUESS_DATA
	case RBT_BONUS_CMD:
		return SERVER_RBT_BONUS
	case ACTIVITY_COND_REWARD_ARR_CMD:
		return SERVER_ACTIVITY_COND_REWARD_ARR
	}
	return ""
}

var _ = context.Background

// The union tree, BLIND (owner 2026-11-07 "cần poll mà nhận, không cần đợi
// check"): harvest 217221 then collect every ready water notice with the
// client's "Nhận hết" (217222 id=0) — no 217220 board pre-read, the server's
// ret is the only arbiter. Pure receives — the donation opcode stays
// untouched (no liquid ids folded here).
func TestRbtTreeCycle(t *testing.T) {
	p := rbtLoopPlayer(t, true)
	go func() {
		p.rbtTreeCycle(1720)
	}()
	rbtExpectCmd(t, p, RBT_BONUS_RESULT_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_BONUS_RESULT, &proto.RebornTrialGetBonusResultResp{Ret: 0, Reward: pb.String("quả99"), Exp: pb.Int32(50), CanReceive: pb.Int32(0)})
	wa := rbtExpectCmd(t, p, RBT_BONUS_ITEM_CMD, 3*time.Second)
	var wreq proto.RebornTrialGetBonusItemReq
	if err := pb.Unmarshal(wa[18:], &wreq); err != nil || wreq.GetId() != 0 || wreq.GetActivityId() != 1720 {
		t.Fatalf("must press claim-all id=0, got %v %v", wreq.GetId(), err)
	}
	// THE law (2026-11-07, sonbinhsb): a proto3 marshal of Id:0 DROPS
	// field 2 and the server answers SILENT; the client's protobufjs
	// sends tag 2 = 0 explicitly (bytes 10 00) and gets claim-all. A
	// regression here silently kills "nhận cam lộ" — assert the bytes.
	if b := wa[18:]; len(b) < 2 || b[len(b)-2] != 0x10 || b[len(b)-1] != 0x00 {
		t.Fatalf("claim-all body must FORCE id=0 on the wire (…10 00), got %x", b)
	}
	rbtAnswer(t, p, SERVER_RBT_BONUS_ITEM, &proto.RebornTrialGetBonusItemResp{Ret: 0, Reward: pb.String("nuoc10")})

	saw := [2]string{}
	deadline := time.Now().Add(3 * time.Second)
	for (saw[0] == "" || saw[1] == "") && time.Now().Before(deadline) {
		select {
		case n := <-p.NotiChan:
			if strings.Contains(n.MSG, "thu quả") {
				saw[0] = n.MSG
			}
			if strings.Contains(n.MSG, "nước cây bang") {
				saw[1] = n.MSG
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if saw[0] == "" || saw[1] == "" {
		t.Fatalf("missing tree notifications: %q %q", saw[0], saw[1])
	}
	// No donation opcode was ever touched:
	for {
		select {
		case f := <-p.writeChan:
			t.Fatalf("unexpected extra frame %s", frameFuncType(t, f))
		default:
			return
		}
	}
}

// Wire-level lock for the "nhận cam lộ" law: pb.Marshal(RebornTrial…{Id:0})
// drops field 2 (proto3) → server SILENT. The shipping body must carry
// tag 2 = 0 literally (live: one such press claimed 510 notices).
func TestRbtBonusItemAllBodyForcesIdZero(t *testing.T) {
	b := rbtBonusItemAllBody(1720)
	if len(b) < 2 || b[len(b)-2] != 0x10 || b[len(b)-1] != 0x00 {
		t.Fatalf("body must end in 10 00 (id=0 PRESENT), got %x", b)
	}
	var rq proto.RebornTrialGetBonusItemReq
	if err := pb.Unmarshal(b, &rq); err != nil || rq.GetActivityId() != 1720 || rq.GetId() != 0 {
		t.Fatalf("body must decode {1720, 0}, got %+v (%v)", &rq, err)
	}
}

// The "chi tiết cảnh giới" ladder (type-307 rows) rides ONE batched 21022
// — the client's CommonAchievementView getAllRewardStatus()=true path
// (JS mine 2026-11-07). Blind law: every 307 row goes in the batch, even
// one already claimed (server's ordering/one-shot is the arbiter). ret=203
// = stopped at an unreached rung yet claimed its leading ready rungs, so
// the sweep must re-pull 21003 and name what flipped.
func TestRbtLadderSweepClaimsRungs(t *testing.T) {
	p := rbtLoopPlayer(t, true)
	rung := func(id, act int32, param string) *proto.ActivityConditionConfig {
		return &proto.ActivityConditionConfig{ConditionId: pb.Int32(id), ActivityId: pb.Int32(act),
			Type: pb.Int32(307), Desc: pb.String("Cảnh giới đạt {0}"), Param: []string{param}}
	}
	p.RbtMu.Lock()
	p.rbtCondCfgs = []*proto.ActivityConditionConfig{rung(10071, 1720, "1"), rung(10072, 1720, "3"), rung(10073, 1720, "5")}
	p.rbtCondData = map[int32]*proto.ActivityConditionData{
		10071: {ConditionId: pb.Int32(10071), IsGetReward: pb.Bool(true)},
		10072: {ConditionId: pb.Int32(10072), IsGetReward: pb.Bool(false)},
		10073: {ConditionId: pb.Int32(10073), IsGetReward: pb.Bool(false)},
	}
	p.RbtMu.Unlock()

	go p.rbtLadderSweep(1720)
	f := rbtExpectCmd(t, p, ACTIVITY_COND_REWARD_ARR_CMD, 3*time.Second)
	var rq proto.ReqGetActivityConditionRewardByArr
	if err := pb.Unmarshal(f[18:], &rq); err != nil {
		t.Fatalf("undecodable 21022 body: %v", err)
	}
	if len(rq.GetActivityDataList()) != 3 {
		t.Fatalf("blind batch must include EVERY 307 rung (server arbitrates), got %d", len(rq.GetActivityDataList()))
	}
	want := map[int32]bool{}
	for _, id := range []int32{10071, 10072, 10073} {
		want[id] = false
	}
	for _, r := range rq.GetActivityDataList() {
		_, ok := want[r.GetConditionId()]
		if r.GetActivityId() != 1720 || !ok {
			t.Fatalf("bad ref {%d,%d}", r.GetActivityId(), r.GetConditionId())
		}
		want[r.GetConditionId()] = true
	}
	for id, seen := range want {
		if !seen {
			t.Fatalf("rung %d missing from batch", id)
		}
	}
	// server stops at the first unreached rung after claiming 10072/10073
	rbtAnswer(t, p, SERVER_ACTIVITY_COND_REWARD_ARR, &proto.RspGetActivityConditionReward{Ret: pb.Int32(203)})

	f = rbtExpectCmd(t, p, ACTIVITY_GET_DATA_CMD, 3*time.Second)
	_ = f
	// simulate the reader fold (rbtAnswer alone does not merge configs):
	detail, err := pb.Marshal(&proto.RspGetActivityDetail{Activity: &proto.ActivityCommonData{
		ActivityId: pb.Int32(1720),
		DetailConfig: &proto.ActivityDetailConfig{
			CommonConfig: &proto.ActivityCommonConfig{
				MainConfig: &proto.ActivityMainConfig{Type: pb.Int32(rbtActivityType)},
			},
		},
		ConditionDataList: []*proto.ActivityConditionData{
			{ConditionId: pb.Int32(10071), IsGetReward: pb.Bool(true)},
			{ConditionId: pb.Int32(10072), IsGetReward: pb.Bool(true)},
			{ConditionId: pb.Int32(10073), IsGetReward: pb.Bool(true)},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p.rbtMergeConfig(detail)
	p.stDeliver(SERVER_ACTIVITY_DETAIL, detail)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case n := <-p.NotiChan:
			if strings.Contains(n.MSG, "quà cảnh giới") && strings.Contains(n.MSG, "2") {
				if !strings.Contains(n.MSG, "Cảnh giới đạt 3") || !strings.Contains(n.MSG, "Cảnh giới đạt 5") {
					t.Fatalf("notify must name the newly-claimed rungs: %q", n.MSG)
				}
				select {
				case f := <-p.writeChan:
					t.Fatalf("extra frame after ladder: %s", frameFuncType(t, f))
				default:
				}
				return
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("no cảnh-giới notify after ret=203 + isGetReward flips")
}

// Bet nodes are passable only by wagering; betting exp is authorised free
// (user 2026-10-09) and tinhnang.jpg says "chỉ chọn cái đắt tiền": with 10
// exp on hand the bot must still fire — no gate on bets (never SKIP a bet
// event) — and it starts with the HIGHEST wager.
func TestRbtBetBetsAnyway(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true)
	go p.rebornTrialAutoLoopAfter(0)

	rbtDrainBlind(t, p)

	rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{
		Ret: 0, UnionId: pb.Int64(777), SelfRank: pb.Int32(5), Round: pb.Int32(1),
	})
	rbtExpectCmd(t, p, RBT_ENTER_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER, &proto.RebornTrialDataResp{Ret: 0, PlayerData: &proto.RebornTrialPlayerData{
		Level: pb.Int32(3), Exp: pb.Int64(10), Hp: pb.Int32(3),
		Event: &proto.RebornTrialEvent{Stat: pb.Int32(0), Type: pb.Int32(7), Option: []*proto.RebornTrialEventOption{
			{Order: pb.Int32(0), CostExp: pb.Int64(500)},
			{Order: pb.Int32(1), CostExp: pb.Int64(90)},
		}},
	}})
	rbtExpectCmd(t, p, RBT_AUTO_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_AUTO_EXPLORE, &proto.RebornTrialAutoExploreRsp{Ret: 21})

	sel := rbtExpectCmd(t, p, RBT_SELECT_OPTION_CMD, 3*time.Second)
	var sreq proto.RebornTrialSelectOptionReq
	if err := pb.Unmarshal(sel[18:], &sreq); err != nil || sreq.GetSelectIndex() != 0 {
		t.Fatalf("bet must go highest-wager-first (order 0 @500), got %v %v", sreq.GetSelectIndex(), err)
	}
	rbtAnswer(t, p, SERVER_RBT_SELECT_OPTION, &proto.RebornTrialSelectOptionResp{
		Ret: 0, Exp: pb.Int64(0), Hp: pb.Int32(3), Event: &proto.RebornTrialEvent{Stat: pb.Int32(1), Type: pb.Int32(7)},
	})
	rbtExpectCmd(t, p, RBT_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: 7})
	rbtExpectCmd(t, p, RBT_WORSHIP_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_WORSHIP, &proto.RebornTrialWorshipResp{Ret: 0, Reward: pb.String("x")})
	rbtExpectCmd(t, p, RBT_GUESS_DATA_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_GUESS_DATA, &proto.RebornTrialGuessDataResp{Ret: 0, IsReceive: pb.Bool(true)})
}

// With the ItemConfig table known, the bot must spend every ready treasure:
// repair (type 1) the damaged never-repaired good, then illusion (type 2)
// the un-masked eligible one — each gated by the cd the server answers.
func TestRbtMagicSweep(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true)
	p.RbtMu.Lock()
	p.rbtItemMagic = map[int32]int32{5: 0, 9: 1} // goods 5 repairable, 9 is a treasure itself
	p.RbtMu.Unlock()
	go p.rebornTrialAutoLoopAfter(0)

	rbtDrainBlind(t, p)
	rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{
		Ret: 0, UnionId: pb.Int64(777), SelfRank: pb.Int32(5), Round: pb.Int32(1),
	})
	rbtExpectCmd(t, p, RBT_ENTER_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER, &proto.RebornTrialDataResp{Ret: 0, PlayerData: &proto.RebornTrialPlayerData{
		Level: pb.Int32(2), Exp: pb.Int64(10), Hp: pb.Int32(3),
		MagicItem: []*proto.RebornTrialMagicItem{{MagicType: pb.Int32(1), Cd: pb.Int32(0)}, {MagicType: pb.Int32(2), Cd: pb.Int32(0)}},
		GoodsItem: []*proto.RebornTrialGoodsItem{{Id: pb.Int32(11), GoodsId: pb.Int32(5), Status: pb.Int32(1), Quality: pb.Int32(3)}},
	}})
	rbtExpectCmd(t, p, RBT_AUTO_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_AUTO_EXPLORE, &proto.RebornTrialAutoExploreRsp{Ret: 21})

	want := [][2]int32{{1, 11}, {2, 11}}
	cds := []int32{600, 600}
	for i, w := range want {
		f := rbtExpectCmd(t, p, RBT_USE_MAGIC_CMD, 3*time.Second)
		var rq proto.RebornTrialUseMagicReq
		if err := pb.Unmarshal(f[18:], &rq); err != nil || rq.GetMagicType() != w[0] || rq.GetGoodsId() != w[1] {
			t.Fatalf("use #%d want {type %d goods %d}, got %+v %v", i, w[0], w[1], &rq, err)
		}
		gi := &proto.RebornTrialGoodsItem{Id: pb.Int32(11), GoodsId: pb.Int32(5), Status: pb.Int32(1), Quality: pb.Int32(3)}
		if w[0] == 1 {
			gi.MagicTimes = pb.Int32(1) // repair consumed
		} else {
			gi.MagicTimes = pb.Int32(1)
			gi.Mask = pb.Int32(1) // illusion consumed
		}
		rbtAnswer(t, p, SERVER_RBT_USE_MAGIC, &proto.RebornTrialUseMagicResp{
			Ret: 0, GoodsItem: gi, MagicItem: &proto.RebornTrialMagicItem{MagicType: pb.Int32(w[0]), Cd: pb.Int32(cds[i])},
		})
	}
	rbtExpectCmd(t, p, RBT_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: 7})
	rbtExpectCmd(t, p, RBT_WORSHIP_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_WORSHIP, &proto.RebornTrialWorshipResp{Ret: 0, Reward: pb.String("x")})
	rbtExpectCmd(t, p, RBT_GUESS_DATA_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_GUESS_DATA, &proto.RebornTrialGuessDataResp{Ret: 0, IsReceive: pb.Bool(true)})
}

// Live-repro (2026-10-09, luyện đan): SELECT can answer ret=0 with the
// event STILL pending and ledger fields omitted. That is a no-op, not a
// resolution — the bot must retire the option, try the next (which really
// settles), never zero its exp, and never reach SKIP.
func TestRbtSmeltNoOpRetires(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true)
	go p.rebornTrialAutoLoopAfter(0)

	rbtDrainBlind(t, p)
	rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{
		Ret: 0, UnionId: pb.Int64(777), SelfRank: pb.Int32(5), Round: pb.Int32(1),
	})
	rbtExpectCmd(t, p, RBT_ENTER_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER, &proto.RebornTrialDataResp{Ret: 0, PlayerData: &proto.RebornTrialPlayerData{
		Level: pb.Int32(1), Exp: pb.Int64(8321), Hp: pb.Int32(20),
		Event: &proto.RebornTrialEvent{Stat: pb.Int32(0), Type: pb.Int32(6), Option: []*proto.RebornTrialEventOption{
			{Order: pb.Int32(0), CostExp: pb.Int64(512)},
			{Order: pb.Int32(1), CostExp: pb.Int64(806)},
		}},
	}})
	rbtExpectCmd(t, p, RBT_AUTO_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_AUTO_EXPLORE, &proto.RebornTrialAutoExploreRsp{Ret: 21})

	// tinhnang.jpg "chỉ chọn cái đắt tiền": alchemy walks HIGHEST tier first.
	f0 := rbtExpectCmd(t, p, RBT_SELECT_OPTION_CMD, 3*time.Second)
	var r0 proto.RebornTrialSelectOptionReq
	if err := pb.Unmarshal(f0[18:], &r0); err != nil || r0.GetSelectIndex() != 1 {
		t.Fatalf("first pick must be the expensive option 1, got %v", &r0)
	}
	// The trap answer: ret=0, event STILL pending, exp omitted.
	rbtAnswer(t, p, SERVER_RBT_SELECT_OPTION, &proto.RebornTrialSelectOptionResp{
		Ret: 0, Event: &proto.RebornTrialEvent{Stat: pb.Int32(0), Type: pb.Int32(6)},
	})

	f1 := rbtExpectCmd(t, p, RBT_SELECT_OPTION_CMD, 3*time.Second)
	var r1 proto.RebornTrialSelectOptionReq
	if err := pb.Unmarshal(f1[18:], &r1); err != nil || r1.GetSelectIndex() != 0 {
		t.Fatalf("after a no-op the bot must try the cheaper option 0, got %v (%v)", &r1, err)
	}
	rbtAnswer(t, p, SERVER_RBT_SELECT_OPTION, &proto.RebornTrialSelectOptionResp{
		Ret: 0, Exp: pb.Int64(7515), Event: &proto.RebornTrialEvent{Stat: pb.Int32(1), Type: pb.Int32(6)},
	})

	rbtExpectCmd(t, p, RBT_EXPLORE_CMD, 3*time.Second) // road is open again — no SKIP frame ever sent
	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: 7})
	rbtExpectCmd(t, p, RBT_WORSHIP_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_WORSHIP, &proto.RebornTrialWorshipResp{Ret: 0, Reward: pb.String("x")})
	rbtExpectCmd(t, p, RBT_GUESS_DATA_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_GUESS_DATA, &proto.RebornTrialGuessDataResp{Ret: 0, IsReceive: pb.Bool(true)})
}

// Shop sweep buys exactly the authorised rows — free gift once, ad pack
// twice, tiên ngọc pack twice (owner cap 2026-10-09) — and NEVER a recharge
// pack, even with buyLimit remaining.
func TestRbtShopSweepBuysOnlyAuthorised(t *testing.T) {
	p := rbtLoopPlayer(t, true)
	row := func(id int32, price, reward, param string, limit int32) *proto.ActivityMallTempMsg {
		return &proto.ActivityMallTempMsg{Id: pb.Int32(id), Price: pb.String(price), Reward: pb.String(reward), Param: pb.String(param), BuyLimit: pb.Int32(limit)}
	}
	p.RbtMu.Lock()
	p.rbtMallRows = []*proto.ActivityMallTempMsg{
		row(400000003, "100000=0", "100004=20", "2|0|0|0", 1),
		row(410000034, "100000=0", "100159=2", "3|1800|0|0", 2),
		row(180001620, "600", "100000=60", "1|0|0|0", 1), // recharge — must never be sent
		row(350000049, "100000=398", "100159=3", "2|0|0|0", 2),
	}
	p.rbtMallBought = map[int32]int64{410000034: 1} // one ad already used today
	p.RbtMu.Unlock()

	go p.rbtShopSweep(1720)

	want := []int32{400000003, 410000034, 350000049, 350000049} // ad row: 1/2 used → 1 buy
	for _, mallID := range want {
		f := rbtExpectCmd(t, p, ACTIVITY_BUY_GOODS_CMD, 3*time.Second)
		var rq proto.ReqBuyActivityGoods
		if err := pb.Unmarshal(f[18:], &rq); err != nil || rq.GetMallId() != mallID || rq.GetCount() != 1 || rq.GetIsUseADTime() {
			t.Fatalf("want buy mall %d, got %+v (%v)", mallID, &rq, err)
		}
		rbtAnswer(t, p, SERVER_ACTIVITY_BUY_GOODS, &proto.RspBuyActivityGoods{Ret: 0, Rewards: pb.String("ok")})
	}
	// A refused buy must stop the row, not spin:
	select {
	case f := <-p.writeChan:
		t.Fatalf("unexpected extra frame %s", frameFuncType(t, f))
	case <-time.After(300 * time.Millisecond):
	}
}

// Owner rule 2026-10-09: rounds open 10:00 ICT sharp — the idle wait must
// land ON the boundary, not up to a full interval late; far boundaries stay
// chunked (so /auto_luanhoi false stays responsive).
func TestRbtWakeInLandsOnBoundary(t *testing.T) {
	if d := rbtWakeIn(0); d != rbtIdleWait {
		t.Fatalf("no boundary: %v, want idle wait", d)
	}
	far := time.Now().Add(3 * time.Hour).UnixMilli()
	if d := rbtWakeIn(far); d != rbtIdleWait {
		t.Fatalf("far boundary: %v, want capped idle wait", d)
	}
	near := time.Now().Add(2 * time.Minute).UnixMilli()
	d := rbtWakeIn(near)
	if d < 2*time.Minute || d > 2*time.Minute+4*time.Second {
		t.Fatalf("near boundary: %v, want ~2m03s", d)
	}
	past := time.Now().Add(-time.Minute).UnixMilli()
	if d := rbtWakeIn(past); d > time.Second {
		t.Fatalf("past boundary: %v, want immediate-ish", d)
	}
}

// Live 2026-10-09 (owner: "nạp bang vẫn còn 2 mốc chưa nhận"): the server's
// 21003 condition VALUES are stale ("0") for the union-recharge type rows —
// local gating would skip claimable rungs forever. The sweep therefore fires
// one 21033 BLIND per distinct type (server settles only genuinely-ready
// rows, ret≠0 when nothing; claiming is idempotent) and 21045 per-id for
// 2xxxx cross rows. Single 21004 is ignored by this server, never sent.
func TestRbtCondSweepClaimsReady(t *testing.T) {
	p := rbtLoopPlayer(t, true)
	cfg := func(id, act, typ int32, param string) *proto.ActivityConditionConfig {
		return &proto.ActivityConditionConfig{ConditionId: pb.Int32(id), ActivityId: pb.Int32(act), Type: pb.Int32(typ), Param: []string{param}}
	}
	p.RbtMu.Lock()
	p.rbtCondCfgs = []*proto.ActivityConditionConfig{
		cfg(10001, 1720, 251, "20"), cfg(10052, 1720, 251, "0"), cfg(10002, 1720, 251, "40"),
		cfg(10041, 1720, 84, "6"), // nạp bang rung — value "0" stale, per-id 21045
		cfg(20152, 1720, 84, "100"),
	}
	p.RbtMu.Unlock()

	go p.rbtCondSweep(1720)
	f := rbtExpectCmd(t, p, ACTIVITY_TOKEN_REWARD_CMD, 3*time.Second)
	var rq proto.ReqGetActivityConditionReward
	if err := pb.Unmarshal(f[18:], &rq); err != nil || rq.GetConditionId() != 251 {
		t.Fatalf("want 21033 type 251 first, got %+v (%v)", &rq, err)
	}
	rbtAnswer(t, p, SERVER_ACTIVITY_TOKEN_REWARD, &proto.RspGetActivityConditionReward{Ret: pb.Int32(0), Rewards: pb.String("100152=5")})
	f = rbtExpectCmd(t, p, ACTIVITY_CROSS_RECHARGE_CMD, 3*time.Second)
	var rq2 proto.ReceiveCrossUnionRechargeRewardReq
	if err := pb.Unmarshal(f[18:], &rq2); err != nil || rq2.GetConditionId() != 10041 || rq2.GetActivityId() != 1720 {
		t.Fatalf("want 21045 cond 10041 act 1720, got %+v (%v)", &rq2, err)
	}
	rbtAnswer(t, p, SERVER_ACTIVITY_CROSS_RECHARGE, &proto.RspGetActivityConditionReward{Ret: pb.Int32(0), Rewards: pb.String("105171=2")})
	f = rbtExpectCmd(t, p, ACTIVITY_CROSS_RECHARGE_CMD, 3*time.Second)
	if err := pb.Unmarshal(f[18:], &rq2); err != nil || rq2.GetConditionId() != 20152 {
		t.Fatalf("want 21045 cond 20152, got %+v (%v)", &rq2, err)
	}
	rbtAnswer(t, p, SERVER_ACTIVITY_CROSS_RECHARGE, &proto.RspGetActivityConditionReward{Ret: pb.Int32(2001)})
	select {
	case f := <-p.writeChan:
		t.Fatalf("unexpected extra claim frame %s", frameFuncType(t, f))
	case <-time.After(300 * time.Millisecond):
	}
	n := 0
	for {
		select {
		case msg := <-p.NotiChan:
			if strings.Contains(msg.MSG, "quà mốc") {
				n++
			}
		case <-time.After(200 * time.Millisecond):
			if n != 2 {
				t.Fatalf("want exactly 2 milestone notifies, got %d", n)
			}
			return
		}
	}
}

// ─── rbtRankSweep — 21006 season rank reward (owner 2026-11-08) ───────────

func TestRbtRankSweepNotifiesOnlyGrants(t *testing.T) {
	p := rbtLoopPlayer(t, true)
	p.RbtMu.Lock()
	p.rbtRankTypes = []int32{1, 2}
	p.RbtMu.Unlock()

	go p.rbtRankSweep(1720)
	f := rbtExpectCmd(t, p, ACTIVITY_RANK_REWARD_CMD, 3*time.Second)
	var rq proto.ReqGetRankReward
	if err := pb.Unmarshal(f[18:], &rq); err != nil || rq.GetActivityId() != 1720 || rq.GetRankType() != 1 {
		t.Fatalf("want 21006 act1720 rankType1, got %+v (%v)", &rq, err)
	}
	rbtAnswer(t, p, SERVER_ACTIVITY_RANK_REWARD, &proto.RspGetRankReward{Ret: pb.Int32(0), Rewards: pb.String("100152=2|100162=4")})
	f = rbtExpectCmd(t, p, ACTIVITY_RANK_REWARD_CMD, 3*time.Second)
	if err := pb.Unmarshal(f[18:], &rq); err != nil || rq.GetRankType() != 2 {
		t.Fatalf("want 21006 rankType2, got %+v (%v)", &rq, err)
	}
	// ret=0 with EMPTY rewards = server no-op (±0 lesson of 217233) — must
	// stay silent, only a real grant notifies.
	rbtAnswer(t, p, SERVER_ACTIVITY_RANK_REWARD, &proto.RspGetRankReward{Ret: pb.Int32(0)})
	select {
	case f := <-p.writeChan:
		t.Fatalf("unexpected extra frame %s", frameFuncType(t, f))
	case <-time.After(300 * time.Millisecond):
	}
	n := 0
	for {
		select {
		case msg := <-p.NotiChan:
			if strings.Contains(msg.MSG, "thưởng hạng") {
				n++
			}
		case <-time.After(200 * time.Millisecond):
			if n != 1 {
				t.Fatalf("want exactly 1 rank-grant notify, got %d", n)
			}
			return
		}
	}
}

// Config-armed like the liquid grant: no rankConfig folded ⇒ the opcode is
// never sent; and with types folded, ret≠0 refusals stay silent.
func TestRbtRankSweepConfigArmedAndRefusalsSilent(t *testing.T) {
	p := rbtLoopPlayer(t, true)
	p.rbtRankSweep(1720) // no rankConfig folded → returns without a frame
	select {
	case f := <-p.writeChan:
		t.Fatalf("un-armed sweep must send nothing, got %s", frameFuncType(t, f))
	case <-time.After(200 * time.Millisecond):
	}
	p.RbtMu.Lock()
	p.rbtRankTypes = []int32{1, 2}
	p.RbtMu.Unlock()
	go p.rbtRankSweep(1720)
	for _, want := range []int32{1, 2} {
		f := rbtExpectCmd(t, p, ACTIVITY_RANK_REWARD_CMD, 3*time.Second)
		var rq proto.ReqGetRankReward
		if err := pb.Unmarshal(f[18:], &rq); err != nil || rq.GetActivityId() != 1720 || rq.GetRankType() != want {
			t.Fatalf("want 21006 act1720 rankType%d, got %+v (%v)", want, &rq, err)
		}
		rbtAnswer(t, p, SERVER_ACTIVITY_RANK_REWARD, &proto.RspGetRankReward{Ret: pb.Int32(210)})
	}
	select {
	case msg := <-p.NotiChan:
		t.Fatalf("refusals must never notify, got %q", msg.MSG)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestRbtMergeConfigFoldsRankTypes(t *testing.T) {
	p := rbtLoopPlayer(t, true)
	detail, err := pb.Marshal(&proto.RspGetActivityDetail{Activity: &proto.ActivityCommonData{
		ActivityId: pb.Int32(1720),
		DetailConfig: &proto.ActivityDetailConfig{
			CommonConfig: &proto.ActivityCommonConfig{
				MainConfig: &proto.ActivityMainConfig{Type: pb.Int32(rbtActivityType)},
				RankConfig: []*proto.ActivityRankConfig{
					{Type: pb.Int32(1), Param: pb.String("1|1|0|0")},
					{Type: pb.Int32(1), Param: pb.String("2|2|0|0")},
					{Type: pb.Int32(2), Param: pb.String("1|1|0|0")},
				},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p.rbtMergeConfig(detail)
	p.RbtMu.Lock()
	got := append([]int32(nil), p.rbtRankTypes...)
	p.RbtMu.Unlock()
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("rankTypes %v, want [1 2] deduped", got)
	}
}
