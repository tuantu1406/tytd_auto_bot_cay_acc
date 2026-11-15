package tytd_go

// Union Bounty (yêu minh huyền thưởng) static config, extracted from the
// game's own client configuration (web build 4.0.00.40105, VN). The server
// snapshot only carries a raw curConfigId; the client resolves it against
// these two tables to tell a fight-monster event from a startable escort
// (UNION_BOUNTY_FLOW.md §2), and gates the day on a global config number:
//
//   BountyBossDB    resources asset vn/json/BountyBossDB
//     0f8e2b37-f2b4-4709-b4f6-f42760dd4bde.ce2d8.json   (monster events)
//   BountyEventDB   resources asset vn/json/BountyEventDB
//     07989101-3fd2-4c79-9fa4-939dbe91f6fc.89312.json   (escort configs)
//   GameConfigDB    resources asset vn/json/GameConfigDB
//     905a4310-d0ec-4a5b-8f35-e47396b6d5f0.3f049.json   (sha256 a960ce0e…)
//
// Decoded copies live under ../remoteAll/ (Bounty*DB.decoded.json,
// GameConfigDB.asset.json). Re-extract rather than re-guess if the config
// version changes.

// unionBountyBossIDs: BountyBossDB ids — curConfigId values whose event is a
// monster battle. ATTACK_MONSTER is server-simulated, so the bot fights it
// solo and claims the settlement itself through finishReward → GET_REWARD.
var unionBountyBossIDs = map[int32]bool{
	10001: true, 10002: true, 10003: true, 10004: true, 10005: true,
	10006: true, 10007: true, 10008: true, 10009: true, 10010: true,
	10011: true, 10012: true, 10013: true, 10014: true, 10015: true,
	10016: true, 10017: true, 10018: true, 10019: true, 10020: true,
	10021: true, 10022: true, 10023: true, 10024: true, 10025: true,
	10026: true, 10027: true, 10028: true, 10029: true, 10030: true,
	10031: true, 10032: true, 10033: true, 10034: true, 10035: true,
	10036: true, 10037: true, 10038: true, 10039: true,
}

// unionBountyEventIDs: BountyEventDB ids — curConfigId values that are
// escort/adventure configs, startable with DEAL_BOUNTY type=1. Escort travel
// is UNIONBOUNTY_TRANSFER_TIME = 14400 s (4 h) at every tier, paused outside
// the 8–24 h open window — which is why the loop trusts the server's
// finishReward rather than any local timer.
var unionBountyEventIDs = map[int32]bool{
	1001: true, 1002: true, 1003: true, 1004: true, 1005: true,
	1006: true, 1007: true, 1008: true, 1009: true, 1010: true,
	1011: true, 1012: true, 1013: true, 1014: true, 1015: true,
	2001: true, 2002: true, 2003: true, 2004: true, 2005: true,
	2006: true, 2007: true, 2008: true, 2009: true, 2010: true,
}

// unionBountyDailyTimes: GameConfigDB UNIONBOUNTY_DAILY_TIMES — escort
// starts per day. Only a client-side courtesy: the server refuses beyond it
// and the refusal (ret != 0) is logged and ignored.
const unionBountyDailyTimes = int32(2)
