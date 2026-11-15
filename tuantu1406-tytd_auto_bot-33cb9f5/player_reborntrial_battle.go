package tytd_go

/*
Luân Hồi Bí Cảnh — guild war bracket reads + the owner's ticket command.

BANG CHIẾN FIGHTING LAW (owner 2026-11-08 "đánh bang chiến chỉ đánh ở event
thôi, theo lệnh của player trên mini map"): the ONLY combatants are the
leader's EVENT accounts, and they fight ONLY on the player's order from the
mini map (⚔️ in the base popup → /events/order which=luanhoi act=attack,
and the Telegram /luanhoi_attack command, which fires the same order). No
timer-driven auto-fight exists: the earlier AUTO sweep of 2026-11-07 was
retracted the same day. The regular account's loop never attacks — this
file keeps only the bracket reads the map/commands need, the honest result
line, and the command-only ticket bite.

Opcodes live-probed 2026-10-09 (actID 9062433): 217230→434e bracket list,
217232→4350 heart-beat ret=0, 217233→4351 ChallengeResp (invalid target
refused ret=17210, cost nothing). USE_ITEM (217208→4338) is a PERMANENT
item-ban exception authorised ONLY as the explicit "/luanhoi_ve" command —
the ticket (RENBORNTRIAL_CHALLENGE_ITEM, default 100160) buys challenge
attempts — distinct from the energy fruit (100159) which the auto loop eats
on march refusals (owner-authorised 2026-10-09, rbtEatEnergy).
*/

import (
	"fmt"
	"time"
	"tytd_go/internal/proto"

	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

// rbtBattleList pulls the round's bracket (every rival base: id, name,
// server, score, rank, reduceBlood, shield, position) and caches the newest
// snapshot for the map page and the attack picker.
func (p *Player) rbtBattleList(actID int32) (*proto.RebornTrialChallengeListResp, error) {
	data, err := p.rbtRoundTrip(RBT_BATTLE_ENTER_CMD, SERVER_RBT_BATTLE_ENTER, rbtActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialChallengeListResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	if rsp.GetRet() == 0 {
		p.RbtMu.Lock()
		p.rbtBattle = rsp.GetUnionMsg()
		p.rbtBattleAct = actID
		p.rbtBattleAt = time.Now()
		p.RbtMu.Unlock()
	}
	return &rsp, nil
}

// rbtBattleSnapshot returns the cached bracket (nil before the first pull).
func (p *Player) rbtBattleSnapshot() ([]*proto.RebornTrialUnionChallengeMsg, int32, time.Time) {
	p.RbtMu.Lock()
	defer p.RbtMu.Unlock()
	return p.rbtBattle, p.rbtBattleAct, p.rbtBattleAt
}

// rbtUnionPlayers lists who currently guards one base (217231→434f; on the
// wire the list rides field 7). Pure read — no cost, no state.
func (p *Player) rbtUnionPlayers(actID int32, unionID int64) ([]*proto.RebornTrialUnionBattlePlayerMsg, error) {
	body := rbtActivityBody(actID)
	body = protowire.AppendTag(body, 2, protowire.VarintType)
	body = protowire.AppendVarint(body, uint64(unionID))
	data, err := p.rbtRoundTrip(RBT_BATTLE_UNIONPLAYER_CMD, SERVER_RBT_BATTLE_UNIONPLAYER, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialUnionPlayerListResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	if rsp.GetRet() != 0 {
		return nil, fmt.Errorf("server trả lời ret=%d", rsp.GetRet())
	}
	return rsp.GetPlayerList(), nil
}

// rbtResultLine renders the honest outcome of a challenge for both the
// Telegram and web confirmations: attempts are not the only way to score 0 —
// a fight that defeats nobody legitimately yields +0/−0 (client shows the
// same). Never print a bare "+0 điểm" without saying which case it is.
// The game server happily ACCEPTS 217233 with zero attempts left — ret=0
// but all-zero, a silent no-op (live-proven 2026-10-11); callers gate
// attempts first (EP rbtWarOrderOne refreshes the meter via ENTER, a pure
// read, the way the real client's reqBattle gate does).
func rbtResultLine(name string, rsp *proto.RebornTrialChallengeResp) string {
	txt := fmt.Sprintf("⚔️ Đã tấn công %s — hạ %d lính, +%d điểm",
		name, rsp.GetDefeatNum(), rsp.GetAddScore())
	if rsp.GetReduceScore() > 0 {
		txt += fmt.Sprintf(", bang bị trừ %d", rsp.GetReduceScore())
	}
	if rsp.GetDefeatNum() == 0 && rsp.GetAddScore() == 0 && rsp.GetReduceScore() == 0 {
		txt += " — ⚠️ không hạ nổi lính nào nên game không cộng/trừ điểm (đừng bấm dây chuyền, xem acc nào mạnh hơn mà đánh)"
	}
	if rsp.Rewards != nil && rsp.GetRewards() != "" {
		txt += " · thưởng " + rsp.GetRewards()
	}
	return txt
}

// rbtUseTicket "cắn vé": spends num challenge tickets from the bag for
// +num attack attempts. Command-only [[tytd-item-ban]].
func (p *Player) rbtUseTicket(actID int32, num int) (*proto.RebornTrialUseItemResp, error) {
	itemID := int32(100160)
	p.RbtMu.Lock()
	if p.rbtChallengeItem > 0 {
		itemID = p.rbtChallengeItem
	}
	p.RbtMu.Unlock()
	body := rbtActivityBody(actID)
	body = protowire.AppendTag(body, 2, protowire.VarintType)
	body = protowire.AppendVarint(body, uint64(itemID))
	body = protowire.AppendTag(body, 3, protowire.VarintType)
	body = protowire.AppendVarint(body, uint64(num))
	data, err := p.rbtRoundTrip(RBT_USE_ITEM_CMD, SERVER_RBT_USE_ITEM, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.RebornTrialUseItemResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	if rsp.GetRet() == 0 && rsp.BattleNum != nil {
		p.RbtMu.Lock()
		p.rbtBattleNum = rsp.GetBattleNum()
		p.RbtMu.Unlock()
	}
	return &rsp, nil
}
