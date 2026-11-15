package tytd_go

// Union task (nhiệm vụ bang hội) static config: the GameTaskDB rows with
// type == 3, which is the list the client's MgrUnionTask shows
// (getTaskListByType(TASK_TYPE_3), whose five completeType-2 chop-count rows
// are 120001..120005). Targets follow the client's progress rule
// (TASK_PROTOCOL.md §Build the …list): completeType 2 is a plain quantity
// against completeParams[0].
//
// GameTaskDB  resources asset vn/json/GameTaskDB
//
//	dbb7aa84-32b0-4df4-91e4-339768a5f4a7.96869.json
//
// Decoded copy: ../remoteAll/GameTaskDB.decoded.json.
//
// All five pay the guild currencies 100030/100031/100032; they track the
// day's bang hội activity contributed by the player, so the claim decision
// is value >= target && state != 1 on the server-pushed task data.
//
// 120006 (completeType 129) is the watch-an-ad task: one watched ad is the
// whole progress, so its target is 1 and it is completed via S_WATCH_AD_TASK
// (see watchAdTask), never by chopping.
var unionTaskTargets = map[int32]int32{
	120001: 50,
	120002: 100,
	120003: 150,
	120004: 250,
	120005: 350, // the highest chop count in the list
	120006: 1,   // ad task: value >= 1 means "watched"
}

// unionAdTaskID is the completeType-129 ad task; deliberately absent from
// unionChopTaskIDs so it can never drive the auto-chop loop.
const unionAdTaskID int32 = 120006

// unionChopTaskIDs are the five completeType-2 chop-count tasks; an
// unclaimed one whose value < target is what keeps autoPeach chopping even
// when the user's Peach toggle is off.
var unionChopTaskIDs = []int32{120001, 120002, 120003, 120004, 120005}

// maxChopByDreamLv is EquipmentCreateDB[lv].costParam for build
// 4.0.00.40105 (TASK_PROTOCOL.md §Deriving the maximum times): the largest
// ReqDreamMsg.times the server accepts per request at dream level lv.
// 0 or not-yet-synced is 1 — always legal, resync raises it.
//
// The second official cap, GameConfigDB.DREAM_COST_RATE_LIMIT
// (1|1|1|1|1|1|120|150|180|210 = min server-open day per times), is skipped:
// every live server is past day 210, so dayMax is always 10 here. The server
// stays authoritative either way — a rejected times never retries oversized.
func maxChopByDreamLv(lv int32) int32 {
	switch {
	case lv <= 8:
		return 1 // includes the not-yet-synced 0
	case lv <= 11:
		return 2
	case lv <= 16:
		return 3
	case lv <= 18:
		return 4
	case lv <= 21:
		return 5
	case lv <= 32:
		return 6
	case lv <= 38:
		return 7
	case lv <= 44:
		return 8
	case lv <= 50:
		return 9
	default:
		return 10
	}
}
