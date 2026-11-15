package tytd_go

import (
	"encoding/hex"
	"testing"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Union task auto-claim: progress/state filtering on the pushed task map
// and the batch claim request the poll emits.

func taskPush(t *testing.T, tasks ...*proto.TaskDataTempMsg) []byte {
	t.Helper()
	b, err := pb.Marshal(&proto.TaskDataListMsg{DataList: tasks})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestUnionTaskClaimableFilter(t *testing.T) {
	p := &Player{Username: "u", UID: 42}

	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120001), Value: pb.String("60"), State: pb.Int32(0)},  // done, claimable
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120002), Value: pb.String("120"), State: pb.Int32(1)}, // already claimed
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120003), Value: pb.String("149"), State: pb.Int32(0)}, // one short
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120004), Value: pb.String("999"), State: pb.Int32(0),
			SelectItem: []*proto.TaskSelectItemMsg{{Index: pb.Int32(1), SelectIndex: pb.Int32(0), SelectType: pb.Int32(2)}}}, // needs a manual pick
		&proto.TaskDataTempMsg{TaskId: pb.Int32(100001), Value: pb.String("999"), State: pb.Int32(0)}, // not a bang hội task
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120005), Value: pb.String("x"), State: pb.Int32(0)},   // unreadable progress
	), true)

	ids := p.unionTaskClaimable()
	if len(ids) != 1 || ids[0] != 120001 {
		t.Fatalf("claimable %v, want [120001]", ids)
	}

	// A 502 patch replaces the entry: 120003 reaches its target and
	// 120001 goes to claimed as the merge lands.
	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120003), Value: pb.String("150"), State: pb.Int32(0)},
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120001), Value: pb.String("60"), State: pb.Int32(1)},
	), false)
	ids = p.unionTaskClaimable()
	if len(ids) != 1 || ids[0] != 120003 {
		t.Fatalf("claimable after merge %v, want [120003]", ids)
	}

	// selectItem is sticky across patches that omit the field…
	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120004), Value: pb.String("999"), State: pb.Int32(0)},
	), false)
	if ids := p.unionTaskClaimable(); len(ids) != 1 || ids[0] != 120003 {
		t.Fatalf("selectItem lost on merge, claimable %v", ids)
	}
	// …but a fresh full snapshot re-decides (it replaces the whole map).
	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120003), Value: pb.String("150"), State: pb.Int32(0)},
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120004), Value: pb.String("999"), State: pb.Int32(0)},
	), true)
	ids = p.unionTaskClaimable()
	if len(ids) != 2 || ids[0] != 120003 || ids[1] != 120004 {
		t.Fatalf("after full re-snapshot claimable %v, want [120003 120004]", ids)
	}
}

func TestUnionTaskClaimBatchWire(t *testing.T) {
	p := &Player{Username: "u", UID: 42}
	p.writeChan = make(chan []byte, 16)
	wireCtx(t, p)

	p.taskHandleList(taskPush(t,
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120002), Value: pb.String("100"), State: pb.Int32(0)},
		&proto.TaskDataTempMsg{TaskId: pb.Int32(120001), Value: pb.String("50"), State: pb.Int32(0)},
	), true)
	ids := p.unionTaskClaimable()
	if len(ids) != 2 {
		t.Fatalf("claimable %v, want both", ids)
	}
	reqMSG, _ := pb.Marshal(&proto.TaskGetRewardReqMsg{TaskId: ids})
	p.write(packReqFrame(reqMSG))
	frame := <-p.writeChan
	if ft := frameFuncType(t, frame); ft != TASK_GET_REWARD_CMD {
		t.Fatalf("frame %s, want task get-reward", ft)
	}
	// The official (proto2) client writes repeated int32 unpacked: 08 per
	// id. 120001 → c1 a9 07, 120002 → c2 a9 07, ids sorted.
	if got := hex.EncodeToString(frame[18:]); got != "08c1a90708c2a907" {
		t.Fatalf("claim payload %s, want 08c1a90708c2a907", got)
	}

	// Refused claim: no follow-up frames. Accepted: rewards logged, and the
	// list state is expected to arrive via the next 502, so still nothing.
	p.taskHandleReward(ubBytes(t, &proto.TaskGetRewardRespMsg{Ret: pb.Int32(2)}))
	p.taskHandleReward(ubBytes(t, &proto.TaskGetRewardRespMsg{
		Ret: pb.Int32(0), Rewards: pb.String("100030=10|100031=100"),
	}))
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("claim responses queued %d frames, want 0", n)
	}
}

func packReqFrame(msg []byte) []byte {
	return pack.Pack(TASK_GET_REWARD_CMD, msg, 42)
}
