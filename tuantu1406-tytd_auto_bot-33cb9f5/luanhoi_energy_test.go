package tytd_go

// Auto energy-fruit tests (owner 2026-10-09 "tự động cắn quả tăng thể lực
// đi"). The eat is armed ONLY by a folded config id — the whole point of
// these tests: with no config the ban is intact (no USE_ITEM ever leaves);
// with config, exactly the energy item is spent, once per refusal, and the
// march resumes; 15001 (round closed) must not eat.

import (
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func rbtEnterFrame(hp int32) *proto.RebornTrialDataResp {
	return &proto.RebornTrialDataResp{Ret: 0, PlayerData: &proto.RebornTrialPlayerData{
		Level: pb.Int32(2), Exp: pb.Int64(500), Hp: pb.Int32(hp),
	}}
}

func TestRbtEnergyEatsAfterRefuse(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true)
	p.RbtMu.Lock()
	p.rbtEnergyItem = 100159
	p.rbtStaminaLimit = 2 // one fruit (value=2) reaches the cap
	p.RbtMu.Unlock()
	go p.rebornTrialAutoLoopAfter(0)

	rbtDrainBlind(t, p)
	rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{
		Ret: 0, UnionId: pb.Int64(777), SelfRank: pb.Int32(5), Round: pb.Int32(1),
	})
	rbtExpectCmd(t, p, RBT_ENTER_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER, rbtEnterFrame(0))
	rbtExpectCmd(t, p, RBT_AUTO_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_AUTO_EXPLORE, &proto.RebornTrialAutoExploreRsp{Ret: 21})

	// march refuses on empty stamina → ONE fruit, exactly the energy item.
	rbtExpectCmd(t, p, RBT_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: 9})
	f := rbtExpectCmd(t, p, RBT_USE_ITEM_CMD, 3*time.Second)
	var rq proto.RebornTrialUseItemReq
	if err := pb.Unmarshal(f[18:], &rq); err != nil {
		t.Fatal(err)
	}
	if rq.GetItemId() != 100159 || rq.GetNum() != 1 {
		t.Fatalf("eat must send {item 100159, num 1}, got {%d,%d}", rq.GetItemId(), rq.GetNum())
	}
	rbtAnswer(t, p, SERVER_RBT_USE_ITEM, &proto.RebornTrialUseItemResp{
		Ret: 0, Value: pb.Int32(2), LastRecoveryTime: pb.Int64(1234),
	})
	recov := int64(0)
	for w := 0; w < 40 && recov == 0; w++ {
		time.Sleep(50 * time.Millisecond)
		p.RbtMu.Lock()
		recov = p.rbtEnergyRecovAt
		p.RbtMu.Unlock()
	}
	if recov != 1234 {
		t.Fatalf("recovery clock not folded: %d", recov)
	}
	// full now: march retried, refused again — must NOT eat a second time
	rbtExpectCmd(t, p, RBT_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: 9})
	rbtExpectCmd(t, p, RBT_WORSHIP_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_WORSHIP, &proto.RebornTrialWorshipResp{Ret: 17221})
	rbtExpectCmd(t, p, RBT_GUESS_DATA_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_GUESS_DATA, &proto.RebornTrialGuessDataResp{Ret: 0, IsReceive: pb.Bool(true)})
}

// Without a folded config id the ban stands: a refused march goes straight
// to WORSHIP — any USE_ITEM frame fails the expect below.
func TestRbtEnergyAsleepWithoutConfig(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true) // rbtEnergyItem = 0
	go p.rebornTrialAutoLoopAfter(0)

	rbtDrainBlind(t, p)
	rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{
		Ret: 0, UnionId: pb.Int64(777), SelfRank: pb.Int32(5), Round: pb.Int32(1),
	})
	rbtExpectCmd(t, p, RBT_ENTER_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER, rbtEnterFrame(0))
	rbtExpectCmd(t, p, RBT_AUTO_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_AUTO_EXPLORE, &proto.RebornTrialAutoExploreRsp{Ret: 21})
	rbtExpectCmd(t, p, RBT_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: 9})
	rbtExpectCmd(t, p, RBT_WORSHIP_CMD, 3*time.Second) // ← would be USE_ITEM if the ban leaked
	rbtAnswer(t, p, SERVER_RBT_WORSHIP, &proto.RebornTrialWorshipResp{Ret: 17221})
	rbtExpectCmd(t, p, RBT_GUESS_DATA_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_GUESS_DATA, &proto.RebornTrialGuessDataResp{Ret: 0, IsReceive: pb.Bool(true)})
}

// 15001 is the round closing, not hunger — fruit cannot fix it: no eat even
// with an armed config.
func TestRbtEnergySkips15001(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true)
	p.RbtMu.Lock()
	p.rbtEnergyItem = 100159
	p.rbtStaminaLimit = 20
	p.RbtMu.Unlock()
	go p.rebornTrialAutoLoopAfter(0)

	rbtDrainBlind(t, p)
	rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{
		Ret: 0, UnionId: pb.Int64(777), SelfRank: pb.Int32(5), Round: pb.Int32(1),
	})
	rbtExpectCmd(t, p, RBT_ENTER_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER, rbtEnterFrame(3))
	rbtExpectCmd(t, p, RBT_AUTO_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_AUTO_EXPLORE, &proto.RebornTrialAutoExploreRsp{Ret: 21})
	rbtExpectCmd(t, p, RBT_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: rbtRetPhaseClosed})
	rbtExpectCmd(t, p, RBT_WORSHIP_CMD, 3*time.Second) // ← no USE_ITEM between
	rbtAnswer(t, p, SERVER_RBT_WORSHIP, &proto.RebornTrialWorshipResp{Ret: 17221})
	rbtExpectCmd(t, p, RBT_GUESS_DATA_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_GUESS_DATA, &proto.RebornTrialGuessDataResp{Ret: 0, IsReceive: pb.Bool(true)})
}

// An empty bag (refused USE_ITEM) must stop the chain after ONE attempt —
// no retry storm.
func TestRbtEnergyStopsOnRefusal(t *testing.T) {
	rbtWindowTest = func(time.Time) bool { return true }
	p := rbtLoopPlayer(t, true)
	p.RbtMu.Lock()
	p.rbtEnergyItem = 100159
	p.rbtStaminaLimit = 20
	p.RbtMu.Unlock()
	go p.rebornTrialAutoLoopAfter(0)

	rbtDrainBlind(t, p)
	rbtExpectCmd(t, p, RBT_ENTER_ACTIVITY_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER_ACTIVITY, &proto.RebornTrialEnterActivityResp{
		Ret: 0, UnionId: pb.Int64(777), SelfRank: pb.Int32(5), Round: pb.Int32(1),
	})
	rbtExpectCmd(t, p, RBT_ENTER_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_ENTER, rbtEnterFrame(1))
	rbtExpectCmd(t, p, RBT_AUTO_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_AUTO_EXPLORE, &proto.RebornTrialAutoExploreRsp{Ret: 21})
	rbtExpectCmd(t, p, RBT_EXPLORE_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_EXPLORE, &proto.RebornTrialDataResp{Ret: 9})
	rbtExpectCmd(t, p, RBT_USE_ITEM_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_USE_ITEM, &proto.RebornTrialUseItemResp{Ret: 17205}) // bag empty
	rbtExpectCmd(t, p, RBT_WORSHIP_CMD, 3*time.Second)                              // ← straight out, no second fruit
	rbtAnswer(t, p, SERVER_RBT_WORSHIP, &proto.RebornTrialWorshipResp{Ret: 17221})
	rbtExpectCmd(t, p, RBT_GUESS_DATA_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_GUESS_DATA, &proto.RebornTrialGuessDataResp{Ret: 0, IsReceive: pb.Bool(true)})
}
