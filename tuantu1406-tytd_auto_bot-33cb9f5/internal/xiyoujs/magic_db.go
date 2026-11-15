package xiyoujs

// Go-side read-only view over XiYouMagicDB — the loadout brute-forcer needs
// the candidate pools: type 1–5 = active magics (one worn per type, gated by
// unlock ≤ realmId), type 6 = passive magics (pick 3, same gate). buildRoleData
// consumes useSkills (magic ids → skillId AI list) and passiveSkillIds
// (bonusId → attribute buffs) straight from the role msg.

import "sync"

// MagicRow is one XiYouMagicDB row (only the brute-forcer's fields).
type MagicRow struct {
	ID      int32
	Type    int32 // 1–5 active, 6 passive
	SkillID int32 // active: AI skill id; 0 for passive
	BonusID int32 // passive: XiYouAtrributeDB id
	Unlock  int32 // realm (curLv) gate
}

var (
	magicOnce sync.Once
	magicRows []*MagicRow
)

func loadMagicDB() {
	h, rows, err := tableRows("XiYouMagicDB.json", "XiYouMagicDB")
	if err != nil {
		return
	}
	iID, iTy, iSk, iB, iU := col(h, "id"), col(h, "type"), col(h, "skillId"), col(h, "bonusId"), col(h, "unlock")
	if iID < 0 || iTy < 0 {
		return
	}
	for _, r := range rows {
		m := &MagicRow{ID: int32(toF(r[iID])), Type: int32(toF(r[iTy]))}
		if iSk >= 0 {
			m.SkillID = int32(toF(r[iSk]))
		}
		if iB >= 0 {
			m.BonusID = int32(toF(r[iB]))
		}
		if iU >= 0 {
			m.Unlock = int32(toF(r[iU]))
		}
		magicRows = append(magicRows, m)
	}
}

// MagicRows returns all XiYouMagicDB rows (parsed once).
func MagicRows() []*MagicRow {
	magicOnce.Do(loadMagicDB)
	return magicRows
}
