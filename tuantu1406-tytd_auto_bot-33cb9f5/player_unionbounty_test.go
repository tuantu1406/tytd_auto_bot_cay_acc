package tytd_go

import (
	"encoding/hex"
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Union Bounty decision handlers, driven like the reader goroutine does and
// asserted on the frames that reach writeChan.

func ubBytes(t *testing.T, m pb.Message) []byte {
	t.Helper()
	b, err := pb.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestUnionBountyOpenDecisions(t *testing.T) {
	p := &Player{Username: "u", UID: 42}
	p.writeChan = make(chan []byte, 16)
	wireCtx(t, p)

	// ret != 0 (e.g. not in a union): silence.
	p.unionBountyHandleOpen(ubBytes(t, &proto.UnionBountyOpenBountyEventResp{Ret: pb.Int32(9)}))
	// Garbage must not panic.
	p.unionBountyHandleOpen([]byte{0xff, 0xff, 0xff})
	// Config unknown to both static tables: keep the raw id, guess nothing.
	p.unionBountyHandleOpen(ubBytes(t, &proto.UnionBountyOpenBountyEventResp{
		Ret: pb.Int32(0),
		BountyInfo: &proto.UnionBountyBountyInfo{
			CurConfigId: pb.Int32(77777),
		},
	}))
	// Escort config but the day's attempts (UNIONBOUNTY_DAILY_TIMES=2) are gone.
	p.unionBountyHandleOpen(ubBytes(t, &proto.UnionBountyOpenBountyEventResp{
		Ret:        pb.Int32(0),
		BountyInfo: &proto.UnionBountyBountyInfo{CurConfigId: pb.Int32(1001)},
		PlayerData: &proto.UnionBountyPlayerDataMsg{BountyTimes: pb.Int32(2)},
	}))
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("queued %d frames, want 0 (nothing startable/claimable)", n)
	}

	// Claimable (escort OR monster settlement): exactly a GET_REWARD frame.
	p.unionBountyHandleOpen(ubBytes(t, &proto.UnionBountyOpenBountyEventResp{
		Ret: pb.Int32(0),
		BountyInfo: &proto.UnionBountyBountyInfo{
			CurConfigId:  pb.Int32(1001),
			FinishReward: &proto.UnionBountyFinishReward{EscortCartConfigId: pb.Int32(1001)},
		},
	}))
	if len(p.writeChan) != 1 {
		t.Fatalf("claimable event queued %d frames, want 1", len(p.writeChan))
	}
	if ft := frameFuncType(t, <-p.writeChan); ft != UNION_BOUNTY_GET_REWARD_CMD {
		t.Fatalf("frame %s, want get-reward", ft)
	}

	// Startable escort: probe HAVE_ESCORT before committing.
	p.unionBountyHandleOpen(ubBytes(t, &proto.UnionBountyOpenBountyEventResp{
		Ret:        pb.Int32(0),
		BountyInfo: &proto.UnionBountyBountyInfo{CurConfigId: pb.Int32(1001)},
		PlayerData: &proto.UnionBountyPlayerDataMsg{BountyTimes: pb.Int32(1)},
	}))
	if len(p.writeChan) != 1 {
		t.Fatalf("queued %d frames, want the have-escort probe", len(p.writeChan))
	}
	if ft := frameFuncType(t, <-p.writeChan); ft != UNION_BOUNTY_HAVE_ESCORT_CMD {
		t.Fatalf("frame %s, want have-escort", ft)
	}

	// An escort is already on the road → no second one.
	p.unionBountyHandleHaveEscort(ubBytes(t, &proto.UnionBountyHaveEscortResp{
		Ret: pb.Int32(0), Exist: pb.Bool(true),
	}))
	if len(p.writeChan) != 0 {
		t.Fatalf("started a second escort while one was running")
	}

	// Idle: DEAL_BOUNTY {curConfigId:1001, type:1} — the flow doc pins the
	// wire bytes for 1001 as 08 E9 07 10 01.
	p.unionBountyHandleHaveEscort(ubBytes(t, &proto.UnionBountyHaveEscortResp{Ret: pb.Int32(0)}))
	if len(p.writeChan) != 1 {
		t.Fatalf("queued %d frames, want the deal", len(p.writeChan))
	}
	frame := <-p.writeChan
	if ft := frameFuncType(t, frame); ft != UNION_BOUNTY_DEAL_CMD {
		t.Fatalf("frame %s, want deal", ft)
	}
	if got := hex.EncodeToString(frame[18:]); got != "08e9071001" {
		t.Fatalf("deal payload %s, want 08e9071001 (doc capture)", got)
	}
	// The stash was consumed: a stray have-escort "no" must not re-deal.
	p.unionBountyHandleHaveEscort(ubBytes(t, &proto.UnionBountyHaveEscortResp{Ret: pb.Int32(0)}))
	if len(p.writeChan) != 0 {
		t.Fatal("stray have-escort answer started an escort")
	}
}

func TestUnionBountyDealAndReward(t *testing.T) {
	p := &Player{Username: "u", UID: 42}
	p.writeChan = make(chan []byte, 8)
	wireCtx(t, p)

	// A refused deal is just logged; state comes from the next OPEN.
	p.unionBountyHandleDeal(ubBytes(t, &proto.UnionBountyDealBountyResp{Ret: pb.Int32(3)}))
	p.unionBountyHandleDeal(ubBytes(t, &proto.UnionBountyDealBountyResp{
		Ret: pb.Int32(0), MyCart: &proto.UnionBountyEscortCartMsg{ConfigId: pb.Int32(1001)},
	}))
	if len(p.writeChan) != 0 {
		t.Fatalf("deal answers queued %d frames", len(p.writeChan))
	}

	// A refused claim must not chain into OPEN.
	p.unionBountyHandleReward(ubBytes(t, &proto.UnionBountyGetRewardEscortResp{Ret: pb.Int32(4)}))
	if len(p.writeChan) != 0 {
		t.Fatalf("refused claim queued %d frames", len(p.writeChan))
	}

	// A successful claim chains straight into the next event.
	p.unionBountyHandleReward(ubBytes(t, &proto.UnionBountyGetRewardEscortResp{
		Ret: pb.Int32(0), ConfigId: pb.Int32(1001), Reward: pb.String("100032=640"),
	}))
	if len(p.writeChan) != 1 {
		t.Fatalf("successful claim queued %d frames, want the chained OPEN", len(p.writeChan))
	}
	if ft := frameFuncType(t, <-p.writeChan); ft != UNION_BOUNTY_OPEN_EVENT_CMD {
		t.Fatalf("frame %s, want OPEN", ft)
	}
}

// Monster events are fought solo: boss OPEN → room probe → ATTACK with our
// own slot, and the settlement rides the existing claim path.
func TestUnionBountySoloMonsterFight(t *testing.T) {
	p := &Player{Username: "u", UID: 42}
	p.writeChan = make(chan []byte, 16)
	wireCtx(t, p)
	bossOpen := ubBytes(t, &proto.UnionBountyOpenBountyEventResp{
		Ret:        pb.Int32(0),
		BountyInfo: &proto.UnionBountyBountyInfo{CurConfigId: pb.Int32(10001)}, // BountyBossDB
	})

	// Boss event: probe the room for the bossId and team slots.
	p.unionBountyHandleOpen(bossOpen)
	if len(p.writeChan) != 1 {
		t.Fatalf("boss event queued %d frames, want the room probe", len(p.writeChan))
	}
	if ft := frameFuncType(t, <-p.writeChan); ft != UNION_BOUNTY_OPEN_MONSTER_CMD {
		t.Fatalf("frame %s, want open-monster", ft)
	}

	// Garbage must not panic; a refused room sends nothing; a stray snapshot
	// (probe already consumed) is ignored.
	p.unionBountyHandleOpenMonster([]byte{0xff, 0xff, 0xff})
	if len(p.writeChan) != 0 {
		t.Fatal("garbage open-monster queued a frame")
	}
	room := ubBytes(t, &proto.UnionBountyOpenMonsterResp{
		Ret:   pb.Int32(0),
		Event: pb.Int32(10001),
		BattleMainList: []*proto.UnionBountyBattleMain{
			{ObjId: pb.Int32(5), PlayerId: pb.Int64(42), Pos: pb.Int32(2)},
		},
	})
	p.unionBountyHandleOpenMonster(ubBytes(t, &proto.UnionBountyOpenMonsterResp{Ret: pb.Int32(8)}))
	// ret!=0 consumed the probe; feeding the real room now must stay silent.
	p.unionBountyHandleOpenMonster(room)
	if len(p.writeChan) != 0 {
		t.Fatal("refused/stray open-monster queued a frame")
	}

	// Re-arm the probe and attack: the request copies {playerId,pos} from the
	// room (42 at pos 2) with bossId = room event.
	p.unionBountyHandleOpen(bossOpen)
	<-p.writeChan
	p.unionBountyHandleOpenMonster(room)
	if len(p.writeChan) != 1 {
		t.Fatalf("queued %d frames, want the attack", len(p.writeChan))
	}
	frame := <-p.writeChan
	if ft := frameFuncType(t, frame); ft != UNION_BOUNTY_ATTACK_MONSTER_CMD {
		t.Fatalf("frame %s, want attack-monster", ft)
	}
	// entry {playerId 42 → 08 2a, pos 2 → 10 02} in field 1, bossId 10001
	// (varint 91 4e) in field 2.
	if got := hex.EncodeToString(frame[18:]); got != "0a04082a100210914e" {
		t.Fatalf("attack payload %s, want 0a04082a100210914e", got)
	}

	// Refused attack: no chained OPEN. Successful attack: exactly one.
	p.unionBountyHandleAttackMonster(ubBytes(t, &proto.UnionBountyAttackMonsterResp{Ret: pb.Int32(6)}))
	if len(p.writeChan) != 0 {
		t.Fatal("refused attack chained an OPEN")
	}
	p.unionBountyHandleAttackMonster(ubBytes(t, &proto.UnionBountyAttackMonsterResp{
		Ret: pb.Int32(0), Reward: pb.String("100032=10"),
	}))
	if len(p.writeChan) != 1 {
		t.Fatalf("successful attack queued %d frames, want the chained OPEN", len(p.writeChan))
	}
	if ft := frameFuncType(t, <-p.writeChan); ft != UNION_BOUNTY_OPEN_EVENT_CMD {
		t.Fatalf("frame %s, want OPEN", ft)
	}

	// Pacing: the chained OPEN sees the same boss but the attack just ran —
	// fire again only after pollInterval/2, so this one stays silent.
	p.unionBountyHandleOpen(bossOpen)
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("re-attacked %d times right after attacking, want 0", n)
	}
}

// The static ID sets match the shipped tables (and the 1↔2 / boss↔event ID
// ranges that let a raw curConfigId be classified at all).
func TestUnionBountyStaticIDs(t *testing.T) {
	for _, id := range []int32{1001, 1015, 2001, 2010} {
		if !unionBountyEventIDs[id] {
			t.Fatalf("event id %d missing", id)
		}
	}
	for _, id := range []int32{10001, 10039} {
		if !unionBountyBossIDs[id] {
			t.Fatalf("boss id %d missing", id)
		}
	}
	if unionBountyEventIDs[10001] || unionBountyBossIDs[1001] {
		t.Fatal("boss/event ID ranges must not overlap")
	}
}
