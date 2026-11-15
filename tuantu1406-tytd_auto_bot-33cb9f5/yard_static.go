package tytd_go

// Static Wish Pool (hồ nguyện vọng) selection tables, extracted verbatim
// from the game's own client configuration (web build 4.0.00.40105, VN):
//
//   GameItemsDB            resources asset vn/json/GameItemsDB
//     8c355c8b-2b11-4cb3-af09-1b9f885c7704.22849.json  (sha256 2e30b192…)
//   YardBuildingLevelDB    resources asset vn/json/YardBuildingLevelDB
//     145b2a37-532b-42c0-920a-6f33abff9821.a21fb.json  (sha256 a1631f44…)
//
// Both decoded copies are archived under ../remoteAll/ (*.asset.json).
// The client exposes YardBuildingLevelDB under the alias
// DBMgr.YardBuildingUpgradeDB (index.5d668.pretty.js:57000), which is the
// name the Wish Pool model reads.
//
// Selection logic mirrors the client exactly (index.f346e.pretty.js:11651
// crop/unlock table init, :11799 getUnlockCropNeedLv, :6393 the UI that
// defaults the picker to the highest unlocked crop and the slider to the
// per-level max batch):
//
//   crops       = GameItemsDB rows with type == 43 (CROP);
//                 cropLv = Number(params.split("|")[0]), productId = id
//   thresholds  = YardBuildingLevelDB rows with type == 4 (WISHPOOL);
//                 threshold[level] = Param field 0, maxBatch[level] = Param
//                 field 1 ("threshold|maxNum")
//   requiredWishLevel(cropLv) = smallest level whose threshold >= cropLv
//   product     = unlocked crop with the highest cropLv at the building's
//                 level (ties: higher productId, matching the client's
//                 numeric-key iteration + stable sort), num = maxBatch[level]

// yardCropTiers maps a Wish Pool product id to its crop progression tier
// (GameItemsDB type-43 rows; params field 0).
var yardCropTiers = map[int32]int32{
	400001: 1, 400002: 2, 400003: 3, 400004: 4, 400005: 5,
	400006: 6, 400007: 7, 400008: 8, 400009: 9, 400010: 10,
	400011: 11, 400012: 12, 400013: 13, 400014: 14, 400015: 15,
	400016: 16, 400017: 17, 400018: 18, 400019: 19, 400020: 20,
	400021: 21, 400022: 22, 400023: 23, 400024: 24, 400025: 25,
	400026: 26, 400027: 27, 400028: 28,
}

// yardWishPoolThreshold maps a Wish Pool level to the crop tier it unlocks
// (YardBuildingLevelDB type-4 rows, Param field 0).
var yardWishPoolThreshold = map[int32]int32{
	1: 1, 2: 2, 3: 3, 4: 4, 5: 5,
	6: 6, 7: 7, 8: 8, 9: 9, 10: 10,
	11: 11, 12: 12, 13: 13, 14: 14, 15: 15,
	16: 16, 17: 16, 18: 17, 19: 17, 20: 18,
	21: 18, 22: 19, 23: 19, 24: 20, 25: 20,
	26: 21, 27: 21, 28: 21, 29: 22, 30: 22,
	31: 22, 32: 23, 33: 23, 34: 23, 35: 24,
	36: 24, 37: 24, 38: 25, 39: 25, 40: 25,
	41: 26, 42: 26, 43: 26, 44: 27, 45: 27,
	46: 27, 47: 28, 48: 28, 49: 28, 50: 28,
}

// yardWishPoolBatch maps a Wish Pool level to its maximum batch size
// (YardBuildingLevelDB type-4 rows, Param field 1) — the client's slider
// default when opening the make panel.
var yardWishPoolBatch = map[int32]int32{
	1: 48, 2: 60, 3: 72, 4: 84, 5: 96,
	6: 108, 7: 120, 8: 126, 9: 132, 10: 138,
	11: 144, 12: 146, 13: 148, 14: 151, 15: 153,
	16: 156, 17: 158, 18: 160, 19: 163, 20: 165,
	21: 168, 22: 170, 23: 172, 24: 175, 25: 177,
	26: 180, 27: 182, 28: 184, 29: 187, 30: 189,
	31: 192, 32: 194, 33: 196, 34: 199, 35: 201,
	36: 204, 37: 206, 38: 208, 39: 211, 40: 213,
	41: 216, 42: 218, 43: 220, 44: 223, 45: 225,
	46: 228, 47: 230, 48: 232, 49: 235, 50: 237,
}

// yardRequiredWishLevel is the client's getUnlockCropNeedLv: the lowest
// Wish Pool level whose unlock threshold covers cropLv (999 = never).
func yardRequiredWishLevel(cropLv int32) int32 {
	required := int32(999)
	for level, threshold := range yardWishPoolThreshold {
		if cropLv <= threshold && level < required {
			required = level
		}
	}
	return required
}

// yardHighestWishProduct picks what the client's make panel has selected by
// default for a Wish Pool at wishLv: the highest-tier unlocked crop (ties
// decided by productId order, as in the client) in the level's maximum
// batch size. ok=false when the level is not in the tables (level 0 from a
// partial snapshot, or a level past the shipped cap).
func yardHighestWishProduct(wishLv int32) (productID, num int32, ok bool) {
	batch, known := yardWishPoolBatch[wishLv]
	if !known || batch <= 0 {
		return 0, 0, false
	}
	bestID, bestLv := int32(0), int32(-1)
	for id, tier := range yardCropTiers {
		if yardRequiredWishLevel(tier) > wishLv {
			continue
		}
		// sort by (cropLv, id) ascending, keep the last — the JS client
		// iterates GameItemsDB in numeric-id order with a stable sort.
		if tier > bestLv || (tier == bestLv && id > bestID) {
			bestID, bestLv = id, tier
		}
	}
	if bestID == 0 {
		return 0, 0, false
	}
	return bestID, batch, true
}
