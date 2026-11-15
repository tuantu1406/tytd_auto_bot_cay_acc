package tytd_go

import (
	"log"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Union daily activity boxes (rương sôi nổi bang). The old code fired the
// claim (actIndex=4, PhanThan{Id:4}) blind on every login: re-logged
// accounts spammed refusals and an account online all day never retried.
// autoUnionDaily keeps the exact same wire call but sends it once per
// unionDailyClaimPeriod — the server stays the arbiter of whether the box
// is open, we just stop bursting it.

const (
	unionDailyFirstDelay  = 20 * time.Second // after login traffic: yard starts at 15 s, tasks at 25 s
	unionDailyClaimPeriod = 6 * time.Hour
)

// unionDailyClaimMSG builds the claim body — byte-identical to the old
// PhanThan{Id:4} burst payload.
func unionDailyClaimMSG() []byte {
	m, _ := pb.Marshal(&proto.ReqUnionGetDailyTask{ActIndex: pb.Int32(4)})
	return m
}

func (p *Player) autoUnionDaily() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(unionDailyFirstDelay):
	}
	for {
		p.write(pack.Pack(S_BANG_NHANQUA_SOINOI, unionDailyClaimMSG(), p.UID))
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(unionDailyClaimPeriod):
		}
	}
}

// unionDailyHandleClaim logs the 22118 answer. A refusal (box not reached
// yet, no bang hội, shield) logs once per code per login — the call repeats
// every 6 h and a repeating code must not turn the log into a drumbeat.
func (p *Player) unionDailyHandleClaim(data []byte) {
	var resp proto.RspUnionGetDailyTask
	if err := pb.Unmarshal(data, &resp); err != nil {
		return
	}
	p.UnionDailyMu.Lock()
	defer p.UnionDailyMu.Unlock()
	if p.unionDailyLogged == nil {
		p.unionDailyLogged = map[int32]bool{}
	}
	if resp.GetRet() != 0 {
		if p.unionDailyLogged[resp.GetRet()] {
			return
		}
		p.unionDailyLogged[resp.GetRet()] = true
		log.Printf("[%s] [SôiNổi] nhận rương bị từ chối (ret=%d)", p.Username, resp.GetRet())
		return
	}
	log.Printf("[%s] [SôiNổi] đã nhận rương: %s", p.Username, resp.GetReward())
}
