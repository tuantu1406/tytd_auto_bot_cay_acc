package tytd_go

/*
Luân Hồi Bí Cảnh — authorised item spends: auto energy fruit (owner
"tự động cắn quả tăng thể lực đi", 2026-10-09) and auto tree-water donation
(owner "cần phát sạch cả 3 loại", 2026-10-10, rbtLiquidGrant). The RBT twin of the kunlun/bacminh fruit rule and a
PERMANENT-BAN exception explicitly authorised there [[tytd-item-ban]]:
it is deliberately narrow —

  * ONLY the energy item (folded RENBORNTRIAL_ENERGY_ITEM_ID, client
    default 100159) is ever spent, and only once config actually arrived:
    rbtEnergyItem == 0 (no 21003 detail folded) means NEVER send USE_ITEM.
    That is also why the scripted loop tests — which never fold config —
    cannot observe a USE_ITEM, and why the opcode stays on rbtForbidden
    (the source scan matches literal hex strings, not this constant).
  * ONLY fires when a march EXPLORE is refused with energy below the cap,
    topping to RENBORNTRIAL_INITIAL_ENERGY (client default 20), max 5
    fruits per refusal. Refusal 15001 (round closed) never eats.
  * The challenge ticket (RENBORNTRIAL_CHALLENGE_ITEM, 100160) stays
    command-only ("/luanhoi_ve") — this file never touches it.
  * rbtLiquidGrant (217223) donates tree water ONLY for the ids folded from
    RENBORNTRIAL_ITEM_ENERGY (empty = never) — same armed-by-config rule.

Client-proven request/response (btnAddPowerHandler → reqRebornTrialUseItemReq):
{1 activityId, 2 itemId, 3 num} → Resp.value = NEW stamina total,
Resp.lastRecoveryTime = the natural-recovery clock.
*/

import (
	"log"
	"tytd_go/internal/proto"

	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

const (
	rbtEnergyEatCap    = 5     // fruits per march refusal
	rbtEnergyLimitDflt = 20    // client default RENBORNTRIAL_INITIAL_ENERGY
	rbtRetPhaseClosed  = 15001 // phase/window refusal — fruit cannot fix it

	rbtLiquidGrantCap = 100 // bottles per type per cycle — sanity cap, the server refusal ends it first
)

// rbtLiquidGrant donates EVERY tree-water bottle the account holds, for all
// three liquid types (owner "event này có 3 loại nước khác nhau, cần phát
// sạch cả 3 loại" 2026-10-10 — authorised item-ban exception, only the ids
// folded from RENBORNTRIAL_ITEM_ENERGY). The client's grant view iterates
// exactly 3 liquidProData icons and sends num=1 per press/long-press; we
// mirror that and stop when the server refuses (bag empty / daily cap) —
// there is no bag-count protocol, so the answer codes are the oracle.
// Empty rbtLiquidIDs (no 21003 detail folded) = the opcode is never sent,
// keeping the scripted tests and the ban-by-default stance intact.
// Returns the total bottles accepted.
func (p *Player) rbtLiquidGrant(actID int32) int {
	p.RbtMu.Lock()
	ids := append([]int32(nil), p.rbtLiquidIDs...)
	p.RbtMu.Unlock()
	total := 0
	for _, id := range ids {
		n := 0
		for ; n < rbtLiquidGrantCap; n++ {
			body, err := pb.Marshal(&proto.RebornTrialSendBonusItemReq{
				ActivityId: pb.Int32(actID), ItemId: pb.Int32(id), Num: pb.Int32(1),
			})
			if err != nil {
				return total
			}
			data, err := p.rbtRoundTrip(RBT_SEND_BONUS_ITEM_CMD, SERVER_RBT_SEND_BONUS_ITEM, body)
			if err != nil {
				break
			}
			var rsp proto.RebornTrialSendBonusItemResp
			if pb.Unmarshal(data, &rsp) != nil || rsp.GetRet() != 0 {
				break
			}
		}
		if n > 0 {
			log.Printf("[%s] [LHBC] cây bang: phát %d chai nước id %d", p.Username, n, id)
			total += n
		}
	}
	return total
}

// rbtLiquidGrant — EventPlayer twin (login = chơi, same authorised rule).
func (p *EventPlayer) rbtLiquidGrant(actID int32) int {
	p.RbtMu.Lock()
	ids := append([]int32(nil), p.rbtLiquidIDs...)
	p.RbtMu.Unlock()
	total := 0
	for _, id := range ids {
		n := 0
		for ; n < rbtLiquidGrantCap; n++ {
			body, err := pb.Marshal(&proto.RebornTrialSendBonusItemReq{
				ActivityId: pb.Int32(actID), ItemId: pb.Int32(id), Num: pb.Int32(1),
			})
			if err != nil {
				return total
			}
			data, err := p.rbtRT(RBT_SEND_BONUS_ITEM_CMD, SERVER_RBT_SEND_BONUS_ITEM, body)
			if err != nil {
				break
			}
			var rsp proto.RebornTrialSendBonusItemResp
			if pb.Unmarshal(data, &rsp) != nil || rsp.GetRet() != 0 {
				break
			}
		}
		if n > 0 {
			log.Printf("[%s] [LHBC] cây bang: phát %d chai nước id %d", p.Username, n, id)
			total += n
		}
	}
	return total
}

// rbtEatEnergy tops stamina to the cap with the energy fruit.
// Returns (newHp, fruitsEaten). (hp,0) = nothing spent (no config, full,
// refused by server).
func (p *Player) rbtEatEnergy(actID int32, hp int32) (int32, int) {
	p.RbtMu.Lock()
	itemID, limit := p.rbtEnergyItem, p.rbtStaminaLimit
	p.RbtMu.Unlock()
	if itemID == 0 {
		return hp, 0 // config never folded — the ban stands by default
	}
	if limit <= 0 {
		limit = rbtEnergyLimitDflt
	}
	eaten := 0
	for ; eaten < rbtEnergyEatCap && hp < limit; eaten++ {
		body := rbtActivityBody(actID)
		body = protowire.AppendTag(body, 2, protowire.VarintType)
		body = protowire.AppendVarint(body, uint64(itemID))
		body = protowire.AppendTag(body, 3, protowire.VarintType)
		body = protowire.AppendVarint(body, 1)
		data, err := p.rbtRoundTrip(RBT_USE_ITEM_CMD, SERVER_RBT_USE_ITEM, body)
		if err != nil {
			break
		}
		var rsp proto.RebornTrialUseItemResp
		if pb.Unmarshal(data, &rsp) != nil || rsp.GetRet() != 0 || rsp.Value == nil {
			break // bag empty / refused / shape we don't trust — stop
		}
		hp = rsp.GetValue()
		p.RbtMu.Lock()
		if rsp.LastRecoveryTime != nil {
			p.rbtEnergyRecovAt = rsp.GetLastRecoveryTime()
		}
		p.RbtMu.Unlock()
	}
	return hp, eaten
}

// rbtEatEnergy — EventPlayer twin (login = chơi, same authorised rule).
func (p *EventPlayer) rbtEatEnergy(actID int32, hp int32) (int32, int) {
	p.RbtMu.Lock()
	itemID, limit := p.rbtEnergyItem, p.rbtStaminaLimit
	p.RbtMu.Unlock()
	if itemID == 0 {
		return hp, 0
	}
	if limit <= 0 {
		limit = rbtEnergyLimitDflt
	}
	eaten := 0
	for ; eaten < rbtEnergyEatCap && hp < limit; eaten++ {
		body := rbtActivityBody(actID)
		body = protowire.AppendTag(body, 2, protowire.VarintType)
		body = protowire.AppendVarint(body, uint64(itemID))
		body = protowire.AppendTag(body, 3, protowire.VarintType)
		body = protowire.AppendVarint(body, 1)
		data, err := p.rbtRT(RBT_USE_ITEM_CMD, SERVER_RBT_USE_ITEM, body)
		if err != nil {
			break
		}
		var rsp proto.RebornTrialUseItemResp
		if pb.Unmarshal(data, &rsp) != nil || rsp.GetRet() != 0 || rsp.Value == nil {
			break
		}
		hp = rsp.GetValue()
		p.RbtMu.Lock()
		if rsp.LastRecoveryTime != nil {
			p.rbtEnergyRecovAt = rsp.GetLastRecoveryTime()
		}
		p.RbtMu.Unlock()
	}
	return hp, eaten
}
