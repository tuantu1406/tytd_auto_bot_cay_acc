package tytd_go

import (
	"fmt"
	"log"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Union Bounty (yêu minh huyền thưởng) escort loop, per
// ../remoteAll/UNION_BOUNTY_FLOW.md: keep the guild bounty board producing
// on its own — open today's event, claim it when the server says it is
// finishable, and start new escorts while daily attempts last.
//
// The server is the only trusted state, so one OPEN_BOUNTY_EVENT per cycle
// is the whole poll: a nonzero finishReward.escortCartConfigId is the
// claimable signal for BOTH escort and monster settlements, and the fresh
// playerData carries the authoritative attempt counters. Monster events
// (BountyBossDB ids) are fought solo: ATTACK_MONSTER is a server-simulated
// battle whose response already carries the record and reward, so the bot
// opens the room, attacks with just itself in the team, and settles through
// the same finishReward → GET_REWARD path. Attempt/open-window gating is a
// courtesy (union_bounty_static.go): a DEAL refused by the server (ret != 0,
// e.g. closed hours UNIONBOUNTY_OPEN_DURATION=8|24) is logged and retried
// next cycle, which keeps us honest about the server clock without us
// guessing the timezone.

const (
	unionBountyPollInterval = 10 * time.Minute // one OPEN per cycle
	dealBountyTypeStart     = int32(1)         // type 2 = abandon: never sent
)

func (p *Player) autoUnionBounty() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(20 * time.Second): // after login traffic and the yard's 15 s slot
	}
	for {
		p.write(pack.Pack(UNION_BOUNTY_OPEN_EVENT_CMD, []byte{}, p.UID))
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(unionBountyPollInterval):
		}
	}
}

// unionBountyLogOnce logs key at most once per login so a 10-minute poll
// does not bury the interesting lines; returns true when it logged.
func (p *Player) unionBountyLogOnce(key string) bool {
	p.UBMu.Lock()
	defer p.UBMu.Unlock()
	if p.ubLogged == nil {
		p.ubLogged = map[string]bool{}
	}
	if p.ubLogged[key] {
		return false
	}
	p.ubLogged[key] = true
	return true
}

// unionBountyHandleOpen is the decision hub (own goroutine per event; shared
// state lives under UBMu).
func (p *Player) unionBountyHandleOpen(data []byte) {
	var resp proto.UnionBountyOpenBountyEventResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HuyenThuong] bad open resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		// e.g. not in a union — likely permanent, so log each code once
		if p.unionBountyLogOnce(fmt.Sprintf("open:%d", resp.GetRet())) {
			log.Printf("[%s] [HuyenThuong] không mở được sự kiện: ret=%d", p.Username, resp.GetRet())
		}
		return
	}
	info := resp.GetBountyInfo()

	// 1. Claimable beats everything — works for escort AND monster events.
	if info.GetFinishReward().GetEscortCartConfigId() != 0 {
		p.write(pack.Pack(UNION_BOUNTY_GET_REWARD_CMD, []byte{}, p.UID))
		return
	}

	// 2. Monster event: probe the room to learn the bossId and team slots,
	// then attack solo. Re-attack pacing is unionBountyPollInterval/2 so a
	// chained OPEN landing right after a fight waits instead of re-firing.
	if unionBountyBossIDs[info.GetCurConfigId()] {
		p.UBMu.Lock()
		recent := time.Since(p.ubLastAtk) < unionBountyPollInterval/2
		p.ubWantMonster = !recent
		p.UBMu.Unlock()
		if recent {
			return
		}
		reqMSG, _ := pb.Marshal(&proto.UnionBountyOpenMonsterReq{PlayerId: pb.Int64(int64(p.UID))})
		p.write(pack.Pack(UNION_BOUNTY_OPEN_MONSTER_CMD, reqMSG, p.UID))
		return
	}

	// 3. Escort config: start one when idle and attempts remain.
	if !unionBountyEventIDs[info.GetCurConfigId()] {
		if info.GetCurConfigId() != 0 && p.unionBountyLogOnce("unknown") {
			log.Printf("[%s] [HuyenThuong] config %d không có trong bảng tĩnh — bỏ qua", p.Username, info.GetCurConfigId())
		}
		return
	}
	if resp.GetPlayerData().GetBountyTimes() >= unionBountyDailyTimes {
		return // day's attempts spent; silent until the daily reset
	}
	// Confirm no escort is already running (the snapshot that carries myCart
	// is ENTRY_MAP; HAVE_ESCORT is the cheap equivalent the flow doc endorses).
	p.UBMu.Lock()
	p.ubStartCfg = info.GetCurConfigId()
	p.UBMu.Unlock()
	p.write(pack.Pack(UNION_BOUNTY_HAVE_ESCORT_CMD, []byte{}, p.UID))
}

func (p *Player) unionBountyHandleHaveEscort(data []byte) {
	var resp proto.UnionBountyHaveEscortResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HuyenThuong] bad have-escort resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		if p.unionBountyLogOnce(fmt.Sprintf("have:%d", resp.GetRet())) {
			log.Printf("[%s] [HuyenThuong] kiểm tra áp tải lỗi: ret=%d", p.Username, resp.GetRet())
		}
		return
	}
	if resp.GetExist() {
		return // an escort already runs; the next OPEN settles it
	}
	p.UBMu.Lock()
	cfg := p.ubStartCfg
	p.ubStartCfg = 0
	p.UBMu.Unlock()
	if cfg == 0 {
		return // no event behind this answer (stray response)
	}
	reqMSG, _ := pb.Marshal(&proto.UnionBountyDealBountyReq{CurConfigId: cfg, Type: dealBountyTypeStart})
	p.write(pack.Pack(UNION_BOUNTY_DEAL_CMD, reqMSG, p.UID))
}

func (p *Player) unionBountyHandleDeal(data []byte) {
	var resp proto.UnionBountyDealBountyResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		return
	}
	if resp.GetRet() != 0 {
		// typically outside the 8–24 h open window or out of attempts;
		// the next OPEN re-evaluates from fresh server state.
		if p.unionBountyLogOnce(fmt.Sprintf("deal:%d", resp.GetRet())) {
			log.Printf("[%s] [HuyenThuong] lệnh áp tải bị từ chối: ret=%d", p.Username, resp.GetRet())
		}
	}
}

func (p *Player) unionBountyHandleReward(data []byte) {
	var resp proto.UnionBountyGetRewardEscortResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		return
	}
	if resp.GetRet() != 0 {
		if p.unionBountyLogOnce(fmt.Sprintf("reward:%d", resp.GetRet())) {
			log.Printf("[%s] [HuyenThuong] lệnh nhận thưởng bị từ chối: ret=%d", p.Username, resp.GetRet())
		}
		return
	}
	// Success is silent; reward lands server-side.
	// Chain straight into the next event instead of waiting a full cycle.
	p.write(pack.Pack(UNION_BOUNTY_OPEN_EVENT_CMD, []byte{}, p.UID))
}

func (p *Player) unionBountyHandleOpenMonster(data []byte) {
	var resp proto.UnionBountyOpenMonsterResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HuyenThuong] bad open-monster resp: %v", p.Username, err)
		return
	}
	p.UBMu.Lock()
	want := p.ubWantMonster
	p.ubWantMonster = false
	p.UBMu.Unlock()
	if !want {
		return // stray room snapshot — nobody asked for this fight
	}
	if resp.GetRet() != 0 {
		if p.unionBountyLogOnce(fmt.Sprintf("monster:%d", resp.GetRet())) {
			log.Printf("[%s] [HuyenThuong] không vào được phòng yêu quái: ret=%d", p.Username, resp.GetRet())
		}
		return
	}
	// Build the team the way the official client does: copy {playerId, pos}
	// of every left-side slot. Solo means we are the only one — if the room
	// somehow lists no players at all, attack from slot 1.
	team := []*proto.UnionBountyMonsterTeamPlayerMsg{}
	for _, b := range resp.GetBattleMainList() {
		if b.GetPlayerId() != 0 {
			team = append(team, &proto.UnionBountyMonsterTeamPlayerMsg{
				PlayerId: pb.Int64(b.GetPlayerId()),
				Pos:      pb.Int32(b.GetPos()),
			})
		}
	}
	if len(team) == 0 {
		team = []*proto.UnionBountyMonsterTeamPlayerMsg{
			{PlayerId: pb.Int64(int64(p.UID)), Pos: pb.Int32(1)},
		}
	}
	reqMSG, _ := pb.Marshal(&proto.UnionBountyAttackMonsterReq{
		TeamPlayerList: team,
		BossId:         pb.Int32(resp.GetEvent()),
	})
	p.write(pack.Pack(UNION_BOUNTY_ATTACK_MONSTER_CMD, reqMSG, p.UID))
}

func (p *Player) unionBountyHandleAttackMonster(data []byte) {
	var resp proto.UnionBountyAttackMonsterResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HuyenThuong] bad attack-monster resp: %v", p.Username, err)
		return
	}
	p.UBMu.Lock()
	p.ubLastAtk = time.Now()
	p.UBMu.Unlock()
	if resp.GetRet() != 0 {
		if p.unionBountyLogOnce(fmt.Sprintf("atk:%d", resp.GetRet())) {
			log.Printf("[%s] [HuyenThuong] đánh yêu quái bị từ chối: ret=%d", p.Username, resp.GetRet())
		}
		return
	}
	// Success is silent; the loot is in resp.GetReward() server-side.
	// The terminal settlement is still finishReward → GET_REWARD (the attack
	// reward above is only the battle's own loot), so re-read the event now
	// instead of waiting a full cycle for the claim.
	p.write(pack.Pack(UNION_BOUNTY_OPEN_EVENT_CMD, []byte{}, p.UID))
}
