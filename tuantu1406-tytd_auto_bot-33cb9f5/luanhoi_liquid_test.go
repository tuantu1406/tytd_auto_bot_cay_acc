package tytd_go

// Auto tree-water donation (owner 2026-10-10 "event này có 3 loại nước khác
// nhau, cần phát sạch cả 3 loại"). Same armed-by-config discipline as the
// energy fruit: ids arrive ONLY via the 21003 fold, no ids = the opcode
// never leaves. The loop mirrors the client (num=1 per press) and lets the
// server's refusal end each type.

import (
	"os"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func TestRbtLiquidFold(t *testing.T) {
	p := &Player{Username: "u"}
	detail, err := pb.Marshal(&proto.RspGetActivityDetail{Activity: &proto.ActivityCommonData{
		ActivityId: pb.Int32(1720),
		DetailConfig: &proto.ActivityDetailConfig{
			CommonConfig: &proto.ActivityCommonConfig{
				MainConfig: &proto.ActivityMainConfig{Type: pb.Int32(rbtActivityType)},
				ParamConfig: []*proto.ActivityParamConfig{
					{Param: pb.String("RENBORNTRIAL_ITEM_ENERGY"), Value: pb.String("100161|30; 100162|30;junk;100163|60")},
				},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p.rbtMergeConfig(detail)
	p.RbtMu.Lock()
	ids := append([]int32(nil), p.rbtLiquidIDs...)
	p.RbtMu.Unlock()
	if len(ids) != 3 || ids[0] != 100161 || ids[1] != 100162 || ids[2] != 100163 {
		t.Fatalf("folded %v, want [100161 100162 100163]", ids)
	}
}

func TestRbtLiquidGrantAllThreeTypes(t *testing.T) {
	p := rbtLoopPlayer(t, true)
	p.Notify = false
	p.RbtMu.Lock()
	p.rbtLiquidIDs = []int32{100161, 100162, 100163}
	p.RbtMu.Unlock()
	got := make(chan int, 1)
	go func() { got <- p.rbtLiquidGrant(1720) }()
	accepted := 0
	for _, wantID := range []int32{100161, 100162, 100163} {
		f := rbtExpectCmd(t, p, RBT_SEND_BONUS_ITEM_CMD, 3*time.Second)
		var rq proto.RebornTrialSendBonusItemReq
		if err := pb.Unmarshal(f[18:], &rq); err != nil {
			t.Fatal(err)
		}
		if rq.GetActivityId() != 1720 || rq.GetItemId() != wantID || rq.GetNum() != 1 {
			t.Fatalf("donate {1720,%d,1} expected, got {%d,%d,%d}", wantID, rq.GetActivityId(), rq.GetItemId(), rq.GetNum())
		}
		rbtAnswer(t, p, SERVER_RBT_SEND_BONUS_ITEM, &proto.RebornTrialSendBonusItemResp{Ret: pb.Int32(0)})
		accepted++
		// Second press of the same type: server says bag empty → type ends.
		f = rbtExpectCmd(t, p, RBT_SEND_BONUS_ITEM_CMD, 3*time.Second)
		rbtAnswer(t, p, SERVER_RBT_SEND_BONUS_ITEM, &proto.RebornTrialSendBonusItemResp{Ret: pb.Int32(17205)})
	}
	if n := <-got; n != accepted {
		t.Fatalf("grant total %d, want %d", n, accepted)
	}
	select {
	case f := <-p.writeChan:
		t.Fatalf("extra frame after refusals: %s", frameFuncType(t, f))
	default:
	}
}

func TestRbtLiquidGrantSleepsWithoutConfig(t *testing.T) {
	p := rbtLoopPlayer(t, true) // rbtLiquidIDs empty
	if n := p.rbtLiquidGrant(1720); n != 0 {
		t.Fatalf("untarmed grant donated %d", n)
	}
	select {
	case f := <-p.writeChan:
		t.Fatalf("untarmed grant sent a frame: %s", frameFuncType(t, f))
	default:
	}
}

// The BLIND tree cycle (owner 2026-11-07) donates FIRST, so notices created
// by our own donation can be claimed in the same cycle — harvest and water
// then fire with no board pre-read.
func TestRbtTreeCycleGrantsFirst(t *testing.T) {
	p := rbtLoopPlayer(t, true)
	p.RbtMu.Lock()
	p.rbtLiquidIDs = []int32{100161}
	p.RbtMu.Unlock()
	go p.rbtTreeCycle(1720)

	f := rbtExpectCmd(t, p, RBT_SEND_BONUS_ITEM_CMD, 3*time.Second)
	var rq proto.RebornTrialSendBonusItemReq
	if err := pb.Unmarshal(f[18:], &rq); err != nil || rq.GetItemId() != 100161 {
		t.Fatalf("bad first donate frame: %v", err)
	}
	rbtAnswer(t, p, SERVER_RBT_SEND_BONUS_ITEM, &proto.RebornTrialSendBonusItemResp{Ret: pb.Int32(0)})
	rbtExpectCmd(t, p, RBT_SEND_BONUS_ITEM_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_SEND_BONUS_ITEM, &proto.RebornTrialSendBonusItemResp{Ret: pb.Int32(1)})

	rbtExpectCmd(t, p, RBT_BONUS_RESULT_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_BONUS_RESULT, &proto.RebornTrialGetBonusResultResp{Ret: 17212})
	rbtExpectCmd(t, p, RBT_BONUS_ITEM_CMD, 3*time.Second)
	rbtAnswer(t, p, SERVER_RBT_BONUS_ITEM, &proto.RebornTrialGetBonusItemResp{Ret: 1})
}

// Only the sanctioned file may reference the donate request opcode — the
// ban-by-default story breaks the moment a second sender appears.
func TestRbtDonateOpcodeHasSingleSender(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, fi := range files {
		n := fi.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		src, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "RBT_SEND_BONUS_ITEM_CMD") {
			continue
		}
		if n != "command.go" && n != "player_reborntrial_energy.go" {
			t.Fatalf("%s references the donate opcode", n)
		}
	}
}

// TestRbtFightNoticeOpcodePinned locks the fight-notice push type to the
// LIVE-confirmed hex (2026-10-10, 710547: "4362" observed passively for
// unionmate hits and right after our own challenge, decoding clean as
// RebornTrialChallengeInfoSync). The previous "4352" was a naming-convention
// guess that never appeared on the wire — every /luanhoi fight ring was
// silently dead with it. If the server ever rotates this type, re-probe with
// TestRbtFightFeedLive (TYTD_RBT_FEED) and update the pin deliberately.
func TestRbtFightNoticeOpcodePinned(t *testing.T) {
	if SERVER_RBT_BATTLE_NOTIC != "4362" {
		t.Fatalf("fight-notice type drifted to %s — live truth is 4362 (re-probe before changing)", SERVER_RBT_BATTLE_NOTIC)
	}
}
