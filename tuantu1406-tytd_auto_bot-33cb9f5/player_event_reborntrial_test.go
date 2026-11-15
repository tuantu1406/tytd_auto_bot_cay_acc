package tytd_go

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"tytd_go/internal/proto"

	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

// The event-account engine must never send an op the Player engine forbids:
// USE_ITEM/UP_LEVEL/SEND_BONUS_ITEM/GUESS_SELECT, the USE_MAGIC item variant
// and — live-proved dead for 172 — the generic 21004 condition-reward command.
func TestEventRbtSourceSendsNoForbiddenOpcodes(t *testing.T) {
	src, err := os.ReadFile("player_event_reborntrial.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, bad := range rbtForbidden {
		if strings.Contains(s, bad) {
			t.Fatalf("forbidden opcode %s referenced by the event engine", bad)
		}
	}
	// 21004 is server-ignored for 172 (live-proved); claims ride 21033/21045.
	if strings.Contains(s, ACTIVITY_COND_REWARD_CMD) {
		t.Fatalf("command %s must never appear in the event engine", ACTIVITY_COND_REWARD_CMD)
	}
	// The challenge ticket stays command-only [[tytd-item-ban]] — event
	// accounts may auto-EAT the energy fruit (owner 2026-10-09) but the
	// engine source must never reach the ticket path.
	if strings.Contains(s, "rbtUseTicket") {
		t.Fatalf("challenge-ticket use must never appear in the event engine")
	}
}

// Transport roundtrip: rbtRT packs a seat-keyed request, the reader-side
// rbtHandleFrameEP delivers the matching answer; a second request while one
// is in flight is refused (the loop is single-seat), and ctx loss surfaces as
// the gone error.
func TestEventRbtTransportRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &EventPlayer{Username: "evrbt", UID: 4272, Running: true,
		ctx: ctx, cancel: cancel, writeChan: make(chan []byte, 8),
		rbtWorshipped: map[string]bool{}}
	frames := make(chan []byte, 8)
	go func() {
		for f := range p.writeChan {
			select {
			case frames <- f:
			default:
			}
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := p.rbtEnterActivity(1720)
		done <- err
	}()

	ty, body := kuNextFrame(t, frames)
	if ty != RBT_ENTER_ACTIVITY_CMD[4:] {
		t.Fatalf("frame %s, want EnterActivity", ty)
	}
	if want := rbtActivityBody(1720); hex.EncodeToString(body) != hex.EncodeToString(want) {
		t.Fatalf("EnterActivity body %x, want %x", body, want)
	}
	// Busy: the seat for THAT answer type is still claimed.
	if _, err := p.rbtRT(RBT_ENTER_ACTIVITY_CMD, SERVER_RBT_ENTER_ACTIVITY, []byte{}); !errors.Is(err, errRbtBusy) {
		t.Fatalf("second in-flight request err=%v, want busy", err)
	}
	ans, _ := pb.Marshal(&proto.RebornTrialEnterActivityResp{Ret: 0, UnionId: pb.Int64(7)})
	p.rbtHandleFrameEP(SERVER_RBT_ENTER_ACTIVITY, ans)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("enter roundtrip: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("answer never reached the seat")
	}
	// Seat released once the request returned.
	p.RbtMu.Lock()
	n := len(p.rbtChans)
	p.RbtMu.Unlock()
	if n != 0 {
		t.Fatalf("seats still claimed after return: %d", n)
	}

	// Connection loss (Run returns and cancels) must unblock the loop.
	p.cancel()
	if _, err := p.rbtRT(RBT_ENTER_CMD, SERVER_RBT_ENTER, []byte{}); !errors.Is(err, errRbtGone) {
		t.Fatalf("after cancel err=%v, want gone", err)
	}
}

// The frame group the event reader must dispatch into rbtHandleFrameEP stays
// exactly the Player list — a new 172 answer type added on one side has to be
// added on the other (compile-time proof lives in player_event.go itself;
// this asserts the answer-hex set the seats key on).
func TestEventRbtAnswerHexesAreReal(t *testing.T) {
	seen := map[string]bool{}
	for _, h := range []string{
		SERVER_RBT_ENTER_ACTIVITY, SERVER_RBT_ENTER, SERVER_RBT_WORSHIP, SERVER_RBT_EXPLORE,
		SERVER_RBT_SELECT_OPTION, SERVER_RBT_SKIP_EVENT, SERVER_RBT_BONUS, SERVER_RBT_BONUS_RESULT,
		SERVER_RBT_BONUS_ITEM, SERVER_RBT_GUESS_DATA, SERVER_RBT_GUESS_REWARD,
		SERVER_RBT_AUTO_EXPLORE, SERVER_RBT_USE_MAGIC,
		SERVER_ACTIVITY_BUY_GOODS, SERVER_ACTIVITY_COND_REWARD, SERVER_ACTIVITY_COND_REWARD_ARR,
		SERVER_ACTIVITY_RANK_REWARD, SERVER_ACTIVITY_CROSS_RECHARGE, SERVER_ACTIVITY_TOKEN_REWARD,
	} {
		if b, err := hex.DecodeString(h); err != nil || len(b) != 2 {
			t.Fatalf("bad answer hex %s", h)
		}
		seen[h] = true
	}
	if len(seen) != 19 {
		t.Fatalf("dispatch hex set changed: %d entries", len(seen))
	}
}

// Login = play, no DB: with the 172 activity absent the loop only logs its
// waiting state and parks; cancelling (logout) must return it promptly.
func TestEventRbtLoopRunsAndStopsWithoutDB(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &EventPlayer{Username: "evrbt2", UID: 4273, Running: true,
		ctx: ctx, cancel: cancel, writeChan: make(chan []byte, 8),
		rbtWorshipped: map[string]bool{}}
	ret := make(chan struct{})
	go func() {
		p.rebornTrialEventLoop()
		close(ret)
	}()
	time.Sleep(50 * time.Millisecond) // past the 8s initial sleep? no —
	cancel()                          // sleepCtx returns false immediately
	select {
	case <-ret:
	case <-time.After(9 * time.Second):
		t.Fatal("loop did not stop after ctx cancel")
	}
}

// ─── bang chiến ORDER law (owner 2026-11-08 "đánh bang chiến chỉ đánh ở
// event thôi, theo lệnh của player trên mini map") ─────────────────────────
// The ONLY combatants are event accounts and they attack ONLY on the
// player's ⚔️ map order → EP.rbtWarOrderOne. No timer-driven auto-fight
// exists anywhere (the Player loop never touches 217233 — see the
// rbtForbidden scans). FREE attempts only: fresh ENTER refreshes the meter
// (pure read), a 0 reading refuses locally, tickets stay command-only
// "/luanhoi_ve" [[tytd-item-ban]].

// warChallengeTarget pulls field 2 (unionId) off a 217233 body.
func warChallengeTarget(t *testing.T, body []byte) int64 {
	t.Helper()
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			t.Fatal("bad challenge frame")
		}
		body = body[n:]
		v, n := protowire.ConsumeVarint(body)
		if n < 0 {
			t.Fatal("bad challenge frame")
		}
		body = body[n:]
		if num == 2 && typ == protowire.VarintType {
			return int64(v)
		}
	}
	t.Fatal("no unionId on challenge frame")
	return 0
}

func eventRbtWarFixture(t *testing.T, clanID uint64) (*EventPlayer, chan []byte) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := &EventPlayer{Username: "evwar", UID: 4280, Running: true, ClanID: clanID,
		ctx: ctx, cancel: cancel, writeChan: make(chan []byte, 8),
		rbtWorshipped: map[string]bool{}}
	frames := make(chan []byte, 8)
	go func() {
		for f := range p.writeChan {
			select {
			case frames <- f:
			default:
			}
		}
	}()
	setActivities(&p.ActList, activityEntry{
		ActivityID: 1720, Type: rbtActivityType,
		BeginTime: time.Now().Add(-time.Hour).UnixMilli(),
		EndTime:   time.Now().Add(24 * time.Hour).UnixMilli(),
	})
	return p, frames
}

func TestEventRbtWarOrderFiresWithAttempts(t *testing.T) {
	p, frames := eventRbtWarFixture(t, 22)
	rspc := make(chan *proto.RebornTrialChallengeResp, 1)
	errc := make(chan error, 1)
	go func() {
		rsp, err := p.rbtWarOrderOne(55)
		rspc <- rsp
		errc <- err
	}()
	// attempt gate: ENTER first (pure read), server says 2 left.
	if ty, _ := kuNextFrame(t, frames); ty != RBT_ENTER_CMD[4:] {
		t.Fatalf("gate frame %s, want ENTER", ty)
	}
	ans, _ := pb.Marshal(&proto.RebornTrialDataResp{
		PlayerData: &proto.RebornTrialPlayerData{BattleNum: pb.Int32(2)}})
	p.rbtHandleFrameEP(SERVER_RBT_ENTER, ans)
	ty, body := kuNextFrame(t, frames)
	if ty != RBT_BATTLE_CHALLENGE_CMD[4:] {
		t.Fatalf("order frame %s, want CHALLENGE", ty)
	}
	if got := warChallengeTarget(t, body); got != 55 {
		t.Fatalf("challenge target %d, want the player-ordered 55", got)
	}
	ans, _ = pb.Marshal(&proto.RebornTrialChallengeResp{
		Ret: 0, Free: pb.Int32(1), DefeatNum: pb.Int32(4), AddScore: pb.Int64(7)})
	p.rbtHandleFrameEP(SERVER_RBT_BATTLE_CHALLENGE, ans)
	if err := <-errc; err != nil {
		t.Fatalf("war order: %v", err)
	}
	rsp := <-rspc
	if rsp.GetDefeatNum() != 4 || rsp.GetAddScore() != 7 {
		t.Fatalf("answer lost: %+v", rsp)
	}
	// rsp.free is the attempt truth the client trusts (folds into the meter)
	p.RbtMu.Lock()
	num := p.rbtBattleNum
	p.RbtMu.Unlock()
	if num != 1 {
		t.Fatalf("battleNum %d after challenge, want server free=1", num)
	}
	select {
	case f := <-frames:
		t.Fatalf("extra frame after one order: %x", f[:12])
	default:
	}
}

func TestEventRbtWarOrderRefusesWithoutAttempts(t *testing.T) {
	p, frames := eventRbtWarFixture(t, 22)
	errc := make(chan error, 1)
	go func() {
		_, err := p.rbtWarOrderOne(55)
		errc <- err
	}()
	if ty, _ := kuNextFrame(t, frames); ty != RBT_ENTER_CMD[4:] {
		t.Fatalf("gate frame %s, want ENTER", ty)
	}
	ans, _ := pb.Marshal(&proto.RebornTrialDataResp{
		PlayerData: &proto.RebornTrialPlayerData{BattleNum: pb.Int32(0)}})
	p.rbtHandleFrameEP(SERVER_RBT_ENTER, ans)
	err := <-errc
	if err == nil || !strings.Contains(err.Error(), "hết lượt") {
		t.Fatalf("err %v, want hết-lượt refusal", err)
	}
	// NEVER a challenge at 0 attempts (server would no-op ret=0 all-zero —
	// the live-proven ±0 trap) and NEVER a ticket bite.
	select {
	case f := <-frames:
		t.Fatalf("fired a frame with 0 attempts: %x", f[:12])
	case <-time.After(200 * time.Millisecond):
	}
}

func TestEventRbtWarOrderNeverHitsOwnBase(t *testing.T) {
	p, frames := eventRbtWarFixture(t, 22)
	if _, err := p.rbtWarOrderOne(22); err == nil || !strings.Contains(err.Error(), "bang của acc") {
		t.Fatalf("own-base err %v, want refusal", err)
	}
	select {
	case f := <-frames:
		t.Fatalf("own base sent a frame: %x", f[:12])
	default:
	}
}

func TestEventRbtWarOrderAsleepWithoutActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &EventPlayer{Username: "evwar2", UID: 4281, Running: true, ClanID: 22,
		ctx: ctx, cancel: cancel, writeChan: make(chan []byte, 8),
		rbtWorshipped: map[string]bool{}}
	if _, err := p.rbtWarOrderOne(55); err == nil || !strings.Contains(err.Error(), "chưa thấy") {
		t.Fatalf("err %v, want activity-closed refusal", err)
	}
}

// rbtRankSweep (21006 season rank reward, owner 2026-11-08 "thêm auto nhận
// reward… cả player lẫn player_event"): one blind 21006 per folded type,
// ret=0+rewards notifies, ret=0+empty stays silent.
func TestEventRbtRankSweepFires(t *testing.T) {
	p, frames := eventRbtWarFixture(t, 22)
	p.Notify = true
	p.NotiChan = make(chan NotifyMGS, 8)
	p.RbtMu.Lock()
	p.rbtRankTypes = []int32{1}
	p.RbtMu.Unlock()
	go p.rbtRankSweep(1720)
	ty, body := kuNextFrame(t, frames)
	if ty != ACTIVITY_RANK_REWARD_CMD[4:] {
		t.Fatalf("frame %s, want %s", ty, ACTIVITY_RANK_REWARD_CMD[4:])
	}
	var rq proto.ReqGetRankReward
	if err := pb.Unmarshal(body, &rq); err != nil || rq.GetActivityId() != 1720 || rq.GetRankType() != 1 {
		t.Fatalf("want 21006 act1720 type1, got %+v (%v)", &rq, err)
	}
	ans, _ := pb.Marshal(&proto.RspGetRankReward{Ret: pb.Int32(0), Rewards: pb.String("100004=60|100025=15")})
	p.rbtHandleFrameEP(SERVER_ACTIVITY_RANK_REWARD, ans)
	select {
	case msg := <-p.NotiChan:
		if !strings.Contains(msg.MSG, "thưởng hạng") {
			t.Fatalf("unexpected notify %q", msg.MSG)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("grant never notified")
	}
	// empty-rewards ret=0 on the next type = no-op, no notify.
	p.RbtMu.Lock()
	p.rbtRankTypes = []int32{2}
	p.RbtMu.Unlock()
	go p.rbtRankSweep(1720)
	ty, _ = kuNextFrame(t, frames)
	if ty != ACTIVITY_RANK_REWARD_CMD[4:] {
		t.Fatalf("frame %s, want %s", ty, ACTIVITY_RANK_REWARD_CMD[4:])
	}
	ans, _ = pb.Marshal(&proto.RspGetRankReward{Ret: pb.Int32(0)})
	p.rbtHandleFrameEP(SERVER_ACTIVITY_RANK_REWARD, ans)
	select {
	case msg := <-p.NotiChan:
		t.Fatalf("no-op must not notify, got %q", msg.MSG)
	case <-time.After(300 * time.Millisecond):
	}
}
