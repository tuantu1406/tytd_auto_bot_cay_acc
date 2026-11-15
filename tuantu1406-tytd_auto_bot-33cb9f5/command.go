package tytd_go

const (
	AUTH_CMD                                      = "00004e21"
	PING_CMD                                      = "00004e23"
	S_GET_TOP_PLAYER_INFO                         = "00004e96"
	S_ATTRIBUTE_GET_SEPARATION_DATAA_MSG_LIST_REQ = "00004ef7"
	RECV_GIFT_CMD                                 = "000060e7"
	RECV_GIFT_CLAN_CMD                            = "000336aa"

	S_HOMELAND_EXPLORE_REFRESH    = "00005243"
	S_HOMELAND_EXPLORE            = "00005242"
	S_HOMELAND_MANAGE             = "0000523d"
	CHAYCHUOT_PHUCDIA_CMD         = "00005244"
	S_HOMELAND_ENTER              = "0000523C"
	S_HOMELAND_SYNC_HOMELAND_DATA = "00005248"

	VENTURE_CMD            = "00004fb2"
	ATTACK_YEUTHAP_CMD     = "0000511a"
	PICK_SKILL_YEUTHAP_CMD = "0000511c"
	LIKE_CMD               = "00034b22"
	S_CHAT_CAY             = "00004eeb"
	S_HUY_DO_CHAT_CAY      = "00004eea" // S_ATTRIBUTE_EQUIPMENT_DEAL_MSG (20202): huỷ đồ vừa rơi
	S_GET_UNDEAL_EQUIP     = "00004ef1" // S_ATTRIBUTE_GET_UNDEAL_EQUIPMENT_MSG (20209): hỏi list đồ chưa huỷ
	S_NHAN_THAN_COT        = "0003865a"
	S_GET_CLAN_INFO        = "00005656"

	S_LUCDAO_CMD           = "00006402"
	S_LAM_MOI_DAU_PHAP     = "00004fbb"
	S_KHIEU_CHIEN_DAU_PHAP = "00004fbc"
	S_TAM_BAO              = "00034c89"

	S_NHAN_QUA_CAY = "00004ef3"
	S_GIAM_CAY_1   = "00004eee"
	S_GIAM_CAY_2   = "00005670"

	S_SEND_GIFT = "000060e6"

	// Đan Giới / Linh Điền (Elixir) — full IDs 235502..; answers arrive as
	// id % 100000 in hex (8aae/8ab0/8ab1/8ab2). The old hand-made THUHOACH/
	// TRONGCAY pair turn out to have been GET_GRASS 235506 and PLANT_GRASS
	// 235504 all along.
	S_ELIXIR_ENTER       = "000397ee" // 235502 ElixirEnterHomeReq → 8aae
	S_ELIXIR_PLANT       = "000397f0" // 235504 ElixirPlantGrassReq (isPlantElves f3 = nút "thu hút dược linh") → 8ab0
	S_ELIXIR_WATER_GRASS = "000397f1" // 235505 ElixirWaterGrassReq (instant speed-up, OPTIONAL — plants self-grow) → 8ab1
	S_ELIXIR_GET_GRASS   = "000397f2" // 235506 ElixirGetGrassReq → 8ab2
	SERVER_ELIXIR_PLANT  = "8ab0"
	SERVER_ELIXIR_WATER  = "8ab1"
	SERVER_ELIXIR_GET    = "8ab2"
	// S_ELIXIR_DEL 235537 ElixirDelPlantGrassReq {landId[],oneKey}: "铲除"
	// nhổ cây đang lớn trên lô. oneKey bị Thẻ Đặc Quyền chặn (35520) như mọi
	// oneKey khác → luôn explicit landId. Cost (item hay free) đang probe live.
	S_ELIXIR_DEL      = "00039811" // → 8ad1
	SERVER_ELIXIR_DEL = "8ad1"
	// S_ELIXIR_PUSH_ORDER 235515 ElixirPushOrderReq {type,index}: "提交订单"
	// — nộp đơn hàng Dược Tiên. Free action (no item field); the server only
	// DEDUCTS the asked grass from the bag when it actually pays out, and
	// answers ret!=0 when the bag is short. type: destiny orders (owner
	// "dược tiên ghé thăm") and trans orders share the board — see
	// elixirSubmitOrders for the live-calibrated mapping. → resp 8abb
	// {ret,grassInfos,danYaoInfos,rewards}.
	S_ELIXIR_PUSH_ORDER      = "000397fb" // 235515 → 8abb
	SERVER_ELIXIR_PUSH_ORDER = "8abb"
	// S_ELIXIR_ELVES_ENTER 235550: bảng Dược Linh (chỉ đọc). todayGetCount
	// trong ElixirElvesPlayerData = bộ đếm thu-hút hôm nay trên cap 30/ngày.
	S_ELIXIR_ELVES_ENTER     = "0003981e" // → 8ade
	SERVER_ELIXIR_ELVES_INFO = "8ade"
	// 235561: server PUSH (no request) at login + season rollover — the
	// season number that selects the bonus grass 864001+3*(season-1).
	SERVER_ELIXIR_DRAW_SEASON = "8ae9"

	// S_ELIXIR_GET_TIME_WATER 235536: nhận suất nước miễn phí của linh điền.
	// Hai suất mỗi ngày theo giờ VN: 11:00–14:00 và 17:00–21:00. Body chuẩn
	// của client (bắt gói trực tiếp): {f1=0, f2=số suất} — sáng 11:00–14:00
	// gửi 08 00 10 01, chiều 17:00–21:00 gửi 08 00 10 02.
	S_ELIXIR_WATER = "00039810"

	S_TIENDO_TAMBAO = "00039233"

	// S_TREASUREAUCTION_ONE_KEY_EXPLORE 234038 — đào tất cả cứ điểm Tàng
	// Bảo Các một lượt. Body rỗng (đúng như client); câu trả lời về 84f6,
	// bot không đọc (server lặng lẽ từ chối ret=12 khi đã đào đủ).
	S_TREASUREAUCTION_ONE_KEY_EXPLORE = "00039236"

	// S_TREASUREAUCTION_HELPTREASURE 234007 — giúp đào Tàng Bảo Các.
	// Body {f1=1} (08 01); trả lời về 84D7, bot không đọc.
	S_TREASUREAUCTION_HELPTREASURE = "00039217"

	// S_PUPIL_TRAIN 211802 — đào tạo đồ đệ. Body {index=0} = đào TẤT CẢ ghế
	// (trường optional nên số 0 vẫn nằm trên wire: 08 00); trả lời về 2E1A,
	// bot không đọc.
	S_PUPIL_TRAIN = "00033b5a"

	// Chaos Law (Pháp Tắc Thái Hư, 2367xx). ENTER/PERCEIVE/ONE_KEY_HELP:
	// body rỗng. EVOLVE/EVOLVE_REWARD: body {id} của law chưa nhận thưởng.
	// Toàn bộ shape đã xác thực live 2026-10-02.
	S_CHAOS_LAW_ENTER         = "00039c9d" // → 8f5d
	S_CHAOS_LAW_PERCEIVE      = "00039c9e" // → 8f5e (+8f6b sync)
	S_CHAOS_LAW_EVOLVE        = "00039c9f" // → 8f5f
	S_CHAOS_LAW_EVOLVE_REWARD = "00039ca0" // → 8f60
	S_CHAOS_LAW_ONE_KEY_HELP  = "00039ca8" // → 8f68

	// Yue Bao (Tiên Ngọc Bảo Phủ, activity type 170). ENTER/INTERACTE body
	// {activityId}; DEPOSIT body {activityId, slot, amount}. Slot 1 = 1 day,
	// slot 3 = 3 days (live-proved 2026-10-02); INTERACTE collects all due.
	S_YUE_BAO_ENTER     = "00034fa9" // → 4269
	S_YUE_BAO_DEPOSIT   = "00034faa" // → 426a
	S_YUE_BAO_INTERACTE = "00034fab" // → 426b

	// Talent / Huyết Mạch (2062x). Requests full 20xxx ids; answers at
	// id-20000. RANDOM body {randomTimes}; DEAL body {3:{index,action},4:0}
	// with action 1 = xoá kết quả (frame thật của client).
	S_TALENT_RANDOM     = "0000508e" // → 026e
	S_TALENT_DEAL       = "0000508f" // → 026f
	S_TALENT_GET_UNDEAL = "00005091" // → 0271

	SERVER_TALENT_SYNC   = "026d" // 20621 login push (talentCreateLevel)
	SERVER_TALENT_RANDOM = "026e"
	SERVER_TALENT_DEAL   = "026f"
	SERVER_TALENT_UNDEAL = "0271"

	SERVER_YUE_BAO_ENTER     = "4269"
	SERVER_YUE_BAO_DEPOSIT   = "426a"
	SERVER_YUE_BAO_INTERACTE = "426b"
	SERVER_YUE_BAO_SYNC      = "426c" // 217004 push (not seat-routed)

	SERVER_CHAOS_LAW_ENTER         = "8f5d"
	SERVER_CHAOS_LAW_PERCEIVE      = "8f5e"
	SERVER_CHAOS_LAW_EVOLVE        = "8f5f"
	SERVER_CHAOS_LAW_EVOLVE_REWARD = "8f60"
	SERVER_CHAOS_LAW_ONE_KEY_HELP  = "8f68"
	SERVER_CHAOS_LAW_SYNC          = "8f6b" // 236715 sync player data (ignored)

	S_QUAY_TINHQUAI_QUANGCAO = "00005156" // S_SPIRIT_DRAW 20822 {drawTimes=1, isAd=true, isUseADTime=false}

	S_QUAY_TTHANTHONG_QUANGCAO = "00005f58" // S_MAGIC_DERIVATION 24408 {times=1, isAd=true, isUseADTime=false}

	S_TONGMON_QUANGCAO = "00033b66"

	// S_WATCH_AD_TASK 20019: completes the ad-watch condition of union task
	// 120006. The claim itself is TASK_GET_REWARD_CMD (20503) — the old
	// "S_BANG_NHANNHIEMVU_QUANGCAO = 00005017" alias here was that same claim
	// command fed a wrong body type, so the ad task could never complete.
	S_WATCH_AD_TASK = "00004e33"

	S_BANG_TRA_GIA = "00005696"

	// Union daily activity boxes (rương sôi nổi bang). S_BANG_SOINOI_INFO
	// 22117 is the panel snapshot (today's union vitality progress + the
	// milestone indices already claimed); S_BANG_NHANQUA_SOINOI 22118 claims
	// one milestone by actIndex (0-4). Both < 100000, so their answers arrive
	// as id % 10000.
	S_BANG_SOINOI_INFO    = "00005665" // 22117 ReqUnionDailyTask {}
	S_BANG_NHANQUA_SOINOI = "00005666" // 22118 ReqUnionGetDailyTask {actIndex}
)

const (
	SERVER_GIFT_CMD             = "12c8"
	SERVER_GIFT_CLAN_CMD        = "296b"
	SERVER_CHUOT_DANGKEO_CMD    = "041d"
	SERVER_LIST_ALL_PHUCDIA_CMD = "0422"
	SERVER_MY_HOMELAND_INFO     = "041c"
	SERVER_MY_MOUSE_INFO        = "041b"
	SERVER_VENTURE_RESULT       = "0192"
	SERVER_YEUTHAP_RESULT_CMD   = "02fa"
	SERVER_CLAN_INFO            = "084c"
	SERVER_CHAT_CAY             = "00cb" // RspDreamMsg (chặt cây)
	SERVER_GET_UNDEAL_EQUIP     = "00d1" // GetUnDealEquipmentMsgResp (20209 % 10000 = 209 = 0xd1)

	// Free-ad spin responses: 20822 % 10000 = 822, 24408 % 10000 = 4408.
	// Only ret is read from each (see SpiritDrawResp / MagicDerivationResp);
	// the daily free count itself is never decoded from the 20821/24400 sync
	// pushes — attempts are capped locally and the server stays the arbiter.
	SERVER_SPIRIT_DRAW       = "0336" // 20822 SpiritDrawResp
	SERVER_MAGIC_DERIVATION  = "1138" // 24408 MagicDerivationResp
	SERVER_DO_CHUA_HUY       = "00cf" // S_DREAM_DATA_SYNC (20207): dreamLv + pending-equipment list
	SERVER_LUCDAO_RESULT_CMD = "15e2"
	SERVER_CLAN_INFO_2       = "0836"

	SERVER_DANGIOI_INFO = "8aae"

	SERVER_BANG_TRA_GIA_INFO = "0875"

	SERVER_BANG_SOINOI_INFO    = "0845" // 22117 RspUnionDailyTask {ret, progress, taskList[]}
	SERVER_BANG_NHANQUA_SOINOI = "0846" // 22118 RspUnionGetDailyTask {ret, reward}
)

// God Island (đảo bồng lai) event. Request IDs and the incoming transport type
// of each response (type = id % 100000) come from
// ../remoteBlessFrog/STATIC_ANALYSIS.md / GOD_ISLAND_PROTOCOL.md.
const (
	GOD_ISLAND_BASE_INFO_CMD      = "000327d1" // 206801 {activityId}
	GOD_ISLAND_HEART_BEAT_CMD     = "000327e3" // 206819 {activityId}
	GOD_ISLAND_INFO_LOAD_CMD      = "000327e4" // 206820 {activityId}
	GOD_ISLAND_BALL_INFO_CMD      = "000327ea" // 206826 {activityId}
	GOD_ISLAND_USE_BALL_CMD       = "000327eb" // 206827 {activityId, usePower?}
	GOD_ISLAND_ATTACK_CMD         = "000327ee" // 206830 {activityId, targetId, enemyId?, isNpc}
	GOD_ISLAND_AUTO_ATK_CMD       = "000327ef" // 206831 {activityId, targetId, onlyNpc?}
	GOD_ISLAND_MOVE_CMD           = "000327f0" // 206832 {activityId, targetId}
	GOD_ISLAND_MINIMAP_CMD        = "000327f2" // 206834 {activityId, floor, group}
	GOD_ISLAND_CITY_INFO_CMD      = "000327e7" // 206823 {activityId, cityVec[], floorId, groupId} — people + marches view pull
	GOD_ISLAND_USE_FRUIT_CMD      = "000327f5" // 206837 {activityId, useCount}
	GOD_ISLAND_PLANT_INFO_CMD     = "000327f4" // 206836 {activityId}
	GOD_ISLAND_RECEIVE_FRUITS_CMD = "000327f6" // 206838 {activityId}
	GOD_ISLAND_PLANT_WATER_CMD    = "000327f7" // 206839 {activityId, id=noticeInfo.id}
	GOD_ISLAND_SEND_LIQUID_CMD    = "00032803" // 206851 {activityId, liquidId, sendNum}
	GOD_ISLAND_ACCELERATE_CMD     = "00032801" // 206849 {activityId, speedUpCount?}

	// ACTIVITY_GET_DATA_CMD returns the activity detail config, whose
	// commonConfig.paramConfig carries GODISLAND_ITEM_ENERGY — the valid
	// liquidId list for SEND_LIQUID.
	ACTIVITY_GET_DATA_CMD = "0000520b" // 21003 {activityId, version?}
	// 21005 ACTIVITY_BUY_MALL_GOODS — {activityId, mallId, count, isUseADTime}.
	// RebornTrial auto-buy ALLOWED rows (owner 2026-10-09): free/ad-watch
	// (price X=0) + Tiên Ngọc BC pack (price 100000=N, max = its buyLimit/day).
	// Nạp-tiền rows (numeric price, param "1|") must NEVER be sent.
	ACTIVITY_BUY_GOODS_CMD    = "0000520d"
	SERVER_ACTIVITY_BUY_GOODS = "03ed" // 1005 RspBuyActivityGoods
	// 21004 ACTIVITY_COND_REWARD — pure receive of a completed activity
	// milestone {activityId, conditionId}. Owner authorised 2026-10-09
	// ("thêm tự check mà nhận đi"): the bot claims every conditionConfig row
	// whose server-tracked value reached the param threshold and that is
	// not yet marked received. Costs nothing.
	ACTIVITY_COND_REWARD_CMD    = "00005204"
	SERVER_ACTIVITY_COND_REWARD = "03ec" // 1004 RspGetActivityConditionReward
	// 21045 — same shape, but for the cross-server UNION RECHARGE milestone
	// rows (conditionId 2xxxx band). Also a pure receive. Answer 0415 (ret).
	ACTIVITY_CROSS_RECHARGE_CMD    = "00005235"
	SERVER_ACTIVITY_CROSS_RECHARGE = "0415" // 1045 ReceiveCrossUnionRechargeRewardResp
	// 21033 GET_BATTLE_PASS_CONDITION_REWARD is what the RebornTrial panel
	// actually uses (client: reqGetActTokenReward with conditionId = the
	// milestone's TYPE; one call settles all ready rows of that type —
	// live-proved 2026-10-09: got count moved, later calls answer ret=210
	// "nothing to receive"). Single 21004 is silently ignored by this
	// server — never send it.
	ACTIVITY_TOKEN_REWARD_CMD    = "00005221"
	SERVER_ACTIVITY_TOKEN_REWARD = "0401" // 1025?? RspGetActivityConditionReward (observed type)
	// 21022 GET_CONDITION_REWARD_BY_ARR — the generic achievement-panel
	// claim (client: ActivityMgr.reqGetActRewardByArr; the "chi tiết cảnh
	// giới" ladder — CommonAchievementView with getAllRewardStatus()=true —
	// fires ONE batched call per click, JS mine 2026-11-07:
	// Protocol.S_ACTIVITY_GET_CONDITION_REWARD_BY_ARR=21022). Body
	// ReqGetActivityConditionRewardByArr{activityDataList:[{activityId,
	// conditionId}]}; answer RspGetActivityConditionReward on 1022.
	ACTIVITY_COND_REWARD_ARR_CMD    = "0000521e"
	SERVER_ACTIVITY_COND_REWARD_ARR = "03fe" // 1022 RspGetActivityConditionReward
	// 21006 GET_RANK_REWARD — pure receive of the season RANK settlement
	// reward (owner 2026-11-08 "thêm auto nhận reward"). Body
	// ReqGetRankReward{activityId, rankType}; rankType = the 21003
	// commonConfig.rankConfig entries' type field (live 172: 1 = cá nhân,
	// 2 = bang). Answer 03ee {ret, rewards} — LIVE-CONFIRMED 2026-11-08
	// (reborntrial_rank_live_test.go): rankType 1/2 paid real items, bogus
	// types answer ret=0 with empty rewards (server no-op). Costs nothing
	// [[tytd-item-ban]] — pure receive.
	ACTIVITY_RANK_REWARD_CMD    = "0000520e"
	SERVER_ACTIVITY_RANK_REWARD = "03ee" // 1006 RspGetRankReward
)

const (
	// Activity config pushes: 21001 PushActivityList (full list at login),
	// 21036 SyncActivityList (incremental). Both carry mainConfig[] entries
	// {activityId=1, type=2}; God Island is type 68 (333 = peak island).
	SERVER_ACTIVITY_LIST             = "03e9"
	SERVER_ACTIVITY_LIST_UPDATE      = "040c"
	SERVER_ACTIVITY_DETAIL           = "03eb" // 1003 RspGetActivityDetail (config incl. GODISLAND_ITEM_ENERGY)
	SERVER_GOD_ISLAND_BASE_INFO      = "1a91" // 6801 GodIslandBaseInfoRespMsg
	SERVER_GOD_ISLAND_PLANT_INFO     = "1ab4" // 6836 GodIslandGamePlantInfoRespMsg
	SERVER_GOD_ISLAND_RECEIVE_FRUITS = "1ab6" // 6838 GodIslandGamePlantReceiveFruitsRespMsg
	SERVER_GOD_ISLAND_PLANT_WATER    = "1ab7" // 6839 GodIslandGamePlantWaterRespMsg
	SERVER_GOD_ISLAND_SEND_LIQUID    = "1ac3" // 6851 GodIslandGameSendLiquidRespMsg
	SERVER_GOD_ISLAND_INFO_LOAD      = "1aa4" // 6820 GodIslandGameInfoRespMsg
	SERVER_GOD_ISLAND_BALL_INFO      = "1aaa" // 6826 GodIslandGameSpiritualBallInfoRespMsg
	SERVER_GOD_ISLAND_USE_BALL       = "1aab" // 6827 GodIslandGameUseSpiritualBallRespMsg
	SERVER_GOD_ISLAND_MINIMAP        = "1ab2" // 6834 GodIslandGameMiniMapInfoRespMsg
	SERVER_GOD_ISLAND_CITY_INFO      = "1aa7" // 6823 GodIslandGameCityInfoRespMsg (standing people + routes of asked cities)
	SERVER_GOD_ISLAND_ROUTE_SYNC     = "1aa6" // 6822 push GodIslandGameRouteInfoSyncMsg (march feed ≡ kunlun 219943)
	SERVER_GOD_ISLAND_CITY_CHANGE    = "1abe" // 6846 push GodIslandGameCityChangeSyncMsg (roster changed)
	SERVER_GOD_ISLAND_CITY_BATTLE    = "1ace" // 6862 push GodIslandGameCityBattleSyncMsg (per-city fight flags)
	SERVER_GOD_ISLAND_LINE_SYNC      = "1ac0" // 6848 push GodIslandGameLineInfoSyncMsg (battle lines — player names)
	SERVER_GOD_ISLAND_PLAYER_WIN     = "1abf" // 6847 push GodIslandGamePlayerWinSyncMsg (victory banner — names)
	SERVER_GOD_ISLAND_USE_FRUIT      = "1ab5" // 6837 GodIslandUseFruitResp
	SERVER_GOD_ISLAND_ACCELERATE     = "1ac1" // 6849 GodIslandGameAcclerateMoveRespMsg
	SERVER_GOD_ISLAND_MYGAMEINFO     = "1ac2" // 6850 push: my location/HP changed (revive, arrival...)
	SERVER_GOD_ISLAND_LEFT_OUT       = "1ac9" // 6857 push: a union left the island
	SERVER_GOD_ISLAND_BEEN_KILL      = "1acd" // 6861 push: we died in the island
	SERVER_GOD_ISLAND_AUTO_STOP      = "1acf" // 6863 push: auto battle stopped
)

// Holy Land (Cửu U / 九幽争霸) union-vs-union city event. Request IDs and
// the incoming transport type of each response (type = id % 100000) come from
// ../remoteHolyLand/HOLY_LAND_PROTOCOL.md. Unlike God Island there is NO
// activityId in any message — one context per account.
const (
	HOLY_LAND_BASE_INFO_CMD    = "0003325d" // 209501 {} → selfRank/group/unionId/canWorship
	HOLY_LAND_HEART_BEAT_CMD   = "00033270" // 209520 {} → rank
	HOLY_LAND_GAME_INFO_CMD    = "00033271" // 209521 {} → location/route/rebirth state
	HOLY_LAND_SCHEDULE_CMD     = "000332a2" // 209570 {} → isOpen/hasQuality/roundId
	HOLY_LAND_MOVE_CMD         = "00033275" // 209525 {targetId}
	HOLY_LAND_MINIMAP_CMD      = "00033276" // 209526 {gameType?,group?} (omit → our bracket)
	HOLY_LAND_CITY_INFO_CMD    = "00033272" // 209522 {cityVec,gameType,groupId} → city detail + FULL route snapshot
	HOLY_LAND_TARGET_LINE_CMD  = "00033274" // 209524 {cityId} → attackers/defenders queue at one city
	HOLY_LAND_ACCELERATE_CMD   = "0003327b" // 209531 {speedUpCount=current counter}
	HOLY_LAND_BACK_HOME_CMD    = "0003327c" // 209532 {useItem} rebirth (false=free)
	HOLY_LAND_ATTACK_CMD       = "0003327d" // 209533 {targetId,enemyId?,isNpc}
	HOLY_LAND_AUTO_ATK_CMD     = "00033281" // 209537 {targetId} (0=off) 托管
	HOLY_LAND_WORSHIP_CMD      = "00033267" // 209511 {} bái tế perk
	HOLY_LAND_RANK_REWARD_CMD  = "00033264" // 209508 {} hasReward?
	HOLY_LAND_CLAIM_REWARD_CMD = "00033265" // 209509 {} claim
)

const (
	SERVER_HOLY_LAND_BASE_INFO     = "251d" // 209501 HolyLandBaseInfoRespMsg
	SERVER_HOLY_LAND_RANK_REWARD   = "2524" // 209508 rank-reward state
	SERVER_HOLY_LAND_CLAIM_REWARD  = "2525" // 209509 claim result
	SERVER_HOLY_LAND_WORSHIP       = "2527" // 209511 worship result
	SERVER_HOLY_LAND_HEART_BEAT    = "2530" // 209520 heartbeat rank
	SERVER_HOLY_LAND_GAME_INFO     = "2531" // 209521 the state spine
	SERVER_HOLY_LAND_MOVE          = "2535" // 209525 move result
	SERVER_HOLY_LAND_MINIMAP       = "2536" // 209526 minimap city ownership
	SERVER_HOLY_LAND_CITY_INFO     = "2532" // 209522 city detail + marching routes snapshot
	SERVER_HOLY_LAND_TARGET_LINE   = "2534" // 209524 one city's attack/defend queue
	SERVER_HOLY_LAND_ACCELERATE    = "253b" // 209531 accelerate result
	SERVER_HOLY_LAND_BACK_HOME     = "253c" // 209532 rebirth result
	SERVER_HOLY_LAND_ATTACK        = "253d" // 209533 attack accepted
	SERVER_HOLY_LAND_AUTO_ATK      = "2541" // 209537 auto-attack accepted
	SERVER_HOLY_LAND_SCHEDULE      = "2562" // 209570 schedule/qualification
	SERVER_HOLY_LAND_CITY_CHANGE   = "254e" // 209550 push: city owners changed
	SERVER_HOLY_LAND_MYGAMEINFO    = "2551" // 209553 push: our location/power
	SERVER_HOLY_LAND_OCCUPY_CITY   = "2552" // 209554 push: a city was taken
	SERVER_HOLY_LAND_LEFT_OUT      = "2553" // 209555 push: union eliminated
	SERVER_HOLY_LAND_CITY_BATTLE   = "2555" // 209557 push: battle flags per city
	SERVER_HOLY_LAND_BEEN_KILL     = "2556" // 209558 push: we died
	SERVER_HOLY_LAND_ROUTE_INFO    = "2557" // 209559 push: march routes
	SERVER_HOLY_LAND_AUTO_STOP     = "2558" // 209560 push: 托管 ended
	SERVER_HOLY_LAND_SCHEDULE_PUSH = "2567" // 209575 push: schedule timestamps
)

// Ball GVG (Thượng Cổ Di Tích) camp-vs-camp room event. Request IDs and the
// incoming transport type of each response (type = id % 10000 for these
// 5-digit ids) come from ../remoteBallGVG/BALL_GVG_PROTOCOL.md. EVERY message
// echoes the live activityId (discovered from the activity list, system type
// 14 — the type itself is never sent as activityId).
const (
	BALL_GVG_ENTER_ACT_CMD  = "0000526f" // 21103 {activityId} → full snapshot; reconnect spine
	BALL_GVG_ENTER_ROOM_CMD = "00005270" // 21104 {activityId, placeId} (home = our camp's placeId)
	BALL_GVG_LEAVE_ROOM_CMD = "00005271" // 21105 {activityId} → back to lobby
	BALL_GVG_MOVE_CMD       = "00005272" // 21106 {activityId, position{x,y,dirX,dirY 5dp strings,time}}
	BALL_GVG_ATTACK_CMD     = "00005273" // 21107 {activityId, attackPlayerId} — server computes
	BALL_GVG_MARK_CMD       = "00005275" // 21109 {activityId, placeId} camp rally mark (major only)
	BALL_GVG_LEAVE_ACT_CMD  = "00005276" // 21110 EMPTY body — registry has no encoder, never marshal a proto here
	BALL_GVG_GET_PLACE_CMD  = "00005277" // 21111 {activityId} → rooms/owner/member counts
)

const (
	SERVER_BALL_GVG_ENTER_ACT   = "044f" // 1103 BallGVGEnterActivityRespMsg
	SERVER_BALL_GVG_ENTER_ROOM  = "0450" // 1104 BallGVGEnterPlaceRespMsg
	SERVER_BALL_GVG_LEAVE_ROOM  = "0451" // 1105 BallGVGLeavePlaceRespMsg
	SERVER_BALL_GVG_MOVE        = "0452" // 1106 BallGVGMoveRespMsg
	SERVER_BALL_GVG_ATTACK      = "0453" // 1107 BallGVGAttackRespMsg
	SERVER_BALL_GVG_MARK        = "0455" // 1109 BallGVGMarkPlaceRespMsg
	SERVER_BALL_GVG_GET_PLACE   = "0457" // 1111 BallGVGGetPlaceInfoRespMsg
	SERVER_BALL_GVG_SYNC_FIGHT  = "045c" // 1116 push: others fighting in room
	SERVER_BALL_GVG_SYNC_SCORE  = "045d" // 1117 push: REPLACE camp scoreboard
	SERVER_BALL_GVG_SYNC_PLACE  = "045e" // 1118 push: room positions batch
	SERVER_BALL_GVG_SYNC_CAMP   = "0460" // 1120 push: REPLACE camp metadata (mark)
	SERVER_BALL_GVG_SYNC_BEATEN = "0461" // 1121 push: WE were attacked (userData REPLACE)
	SERVER_BALL_GVG_SYNC_SEIZE  = "0462" // 1122 push: capture progress/flip
	SERVER_BALL_GVG_SYNC_JOIN   = "0463" // 1123 push: player entered room
	SERVER_BALL_GVG_SYNC_DROP   = "0464" // 1124 push: player left/died in room
	SERVER_BALL_GVG_END_DATA    = "0467" // 1127 push: round settlement
)

// ComposeBall ("đại hội luyện khí", activity TYPE 130 — the live activityId
// always comes from the activity list). Suika-style drop-and-merge: the
// client simulates physics locally and uploads the WHOLE board (balls +
// merges it witnessed) on every settle via COMPOSE; the server answers
// authoritative syncData. Incoming transport = id % 100000 (213001 → 13001 =
// 0x32c9). See /workspace/real-game-data/COMPOSE_BALL.md.
const (
	COMPOSE_BALL_ENTER_CMD    = "00034009" // 213001 {activityId} → board + next fruit
	COMPOSE_BALL_COMPOSE_CMD  = "0003400a" // 213002 board upload (drops and merges)
	COMPOSE_BALL_USE_ITEM_CMD = "0003400b" // 213003 {itemType 1 clear,2 swap,3 levelup,4 merge-all}
	COMPOSE_BALL_HP_ITEM_CMD  = "0003400c" // 213004 buy stamina {num}
	COMPOSE_BALL_SETTLE_CMD   = "0003400d" // 213005 end round → score/rank
)

const (
	SERVER_COMPOSE_BALL_ENTER    = "32c9" // 13001 EnterGameResp {ret,syncData}
	SERVER_COMPOSE_BALL_COMPOSE  = "32ca" // 13002 ComposeResp {ret,syncData,achieveData}
	SERVER_COMPOSE_BALL_USE_ITEM = "32cb" // 13003 UseItemResp
	SERVER_COMPOSE_BALL_HP_ITEM  = "32cc" // 13004 HpItemResp {ret,sync}
	SERVER_COMPOSE_BALL_SETTLE   = "32cd" // 13005 SettleResp {ret,score,rank,nextRankScoreGap,sync}
)

// KunlunWar ("崑崙崢覇" union city-war, activity TYPE 199 — same number as
// systemOpenId.KUN_LUN_WAR). Static 61×61 iso map, blocks keyed
// posIndex = y*61+x, 37 cities; unions occupy/attack. Incoming transport =
// id % 100000 (219901 → 19901 = 0x4dbd). See /workspace/remoteKunlunWar/
// KunlunWar.decoded.proto. Requests the bot may send (phase 1: read-only +
// transport):
const (
	KUNLUN_ENTER_ACTIVITY_CMD    = "00035afd" // 219901 {activityId} → rank/floor/group/settle
	KUNLUN_ENTER_GAME_CMD        = "00035afe" // 219902 {activityId} → personal scene snapshot
	KUNLUN_GAME_BLOCK_INFO_CMD   = "00035aff" // 219903 {activityId,blockVec,floorId,groupId} → view blocks+routes
	KUNLUN_MOVE_CMD              = "00035b00" // 219904 {activityId,targetPoint,attackCity}
	KUNLUN_EXPLORE_CMD           = "00035b01" // 219905 {activityId,targetPoint}
	KUNLUN_SWEEP_CMD             = "00035b02" // 219906 {activityId,num}
	KUNLUN_WORSHIP_CMD           = "00035b03" // 219907 {activityId}
	KUNLUN_GROUP_INFO_CMD        = "00035b05" // 219909 {activityId,groupId,floorId}
	KUNLUN_SPAR_INFO_CMD         = "00035b06" // 219910 {activityId} fruit-tree ripeness (canReceive)
	KUNLUN_SPAR_POWER_CMD        = "00035b07" // 219911 {activityId} claim the ripe fruit (server-decided reward)
	KUNLUN_GET_WELFARE_CMD       = "00035b08" // 219912 {activityId,id} collect an ally's spirit-liquid welfare & water the tree (id=0 = ALL; receiving is free — the SENDER spent the liquid in 219913, which we never send)
	KUNLUN_USE_ITEM_CMD          = "00035b16" // 219926 {activityId,itemId,num} eat a picked spirit fruit — USER-AUTHORIZED capture 2026-10-06; the only kunlun item op besides the revive
	KUNLUN_BACK_HOME_CMD         = "00035b0b" // 219915 {activityId,useItem} revive (false free · true item)
	KUNLUN_SIMPLE_BLOCK_INFO_CMD = "00035b0c" // 219916 ad-hoc block detail
	KUNLUN_HEART_BEAT_CMD        = "00035b0d" // 219917 {activityId} 15s keep-in-scene
	KUNLUN_ACCELERATE_CMD        = "00035b0e" // 219918 {activityId,speedUpCount=current counter} march speed-up
	KUNLUN_MINI_MAP_INFO_CMD     = "00035b0f" // 219919 {activityId,floor,group} → WHOLE map snapshot
	KUNLUN_LEAVE_CMD             = "00035b36" // 219958 leave game scene
)

// SERVER_KUNLUN_* — request answers 4dbd..4dcf/4df6 plus PUSH-ONLY syncs
// (no request of ours for these): 4ddb BlockSync, 4ddc CitySync, 4dd
// MyGameInfo, 4de4 BeenKill, 4de5 LeftOut, 4de6 CityBattle, 4de7 RouteInfo,
// 4de8 LineInfo, 4de9 SendBless, 4dea PlayerWin, 4dec AttackCity, 4ded
// RebirthCoin, 4dee GroupRank, 4df1 CommanderChange, 4df4 MarkBuild.
const (
	SERVER_KUNLUN_ENTER_ACTIVITY    = "4dbd"
	SERVER_KUNLUN_ENTER_GAME        = "4dbe"
	SERVER_KUNLUN_GAME_BLOCK_INFO   = "4dbf"
	SERVER_KUNLUN_MOVE              = "4dc0"
	SERVER_KUNLUN_EXPLORE           = "4dc1"
	SERVER_KUNLUN_SWEEP             = "4dc2"
	SERVER_KUNLUN_WORSHIP           = "4dc3"
	SERVER_KUNLUN_GROUP_INFO        = "4dc5"
	SERVER_KUNLUN_SPAR_INFO         = "4dc6" // 19910 answer
	SERVER_KUNLUN_SPAR_POWER        = "4dc7" // 19911 claim answer
	SERVER_KUNLUN_GET_WELFARE       = "4dc8" // 19912 water/collect answer
	SERVER_KUNLUN_USE_ITEM          = "4dd6" // 19926 fruit-eat answer (ret,energy,energyRecoverTime)
	SERVER_KUNLUN_BACK_HOME         = "4dbb" // 19915 revive answer
	SERVER_KUNLUN_SIMPLE_BLOCK_INFO = "4dcc"
	SERVER_KUNLUN_HEART_BEAT        = "4dcd"
	SERVER_KUNLUN_ACCELERATE        = "4dce" // 19918 accelerate answer
	SERVER_KUNLUN_MINI_MAP_INFO     = "4dcf"
	SERVER_KUNLUN_LEAVE             = "4df6"
	SERVER_KUNLUN_BLOCK_SYNC        = "4ddb" // 19931 push
	SERVER_KUNLUN_CITY_SYNC         = "4ddc" // 19932 push
	SERVER_KUNLUN_MY_GAME_SYNC      = "4ddd" // 19933 push
	SERVER_KUNLUN_BEEN_KILL_SYNC    = "4de4" // 19940 push
	SERVER_KUNLUN_LEFT_OUT_SYNC     = "4de5" // 19941 push
	SERVER_KUNLUN_CITY_BATTLE_SYNC  = "4de6" // 19942 push
	SERVER_KUNLUN_ROUTE_SYNC        = "4de7" // 19943 push
	SERVER_KUNLUN_LINE_SYNC         = "4de8" // 19944 push
	SERVER_KUNLUN_ATTACK_SYNC       = "4dec" // 19948 push
	SERVER_KUNLUN_GROUP_RANK_SYNC   = "4dee" // 19950 push
)

// Sky Trade (Tiên Vực Thương Đồ) trade-intelligence report. The event runs
// as activity type 150; EVERY message echoes the live activityId from the
// activity list (never the type). The server derives our ship's current port
// from session state, so the sequence is enter → enter map → report. Incoming
// transport = id % 100000 (215024 → 15024 = 0x3ab0). See
// ../remoteSkyTrade/SKY_TRADE_PROTOCOL.md.
const (
	SKY_TRADE_ENTER_CMD        = "000347d8" // 215000 {activityId} opens the session
	SKY_TRADE_ENTER_MAP_CMD    = "000347d9" // 215001 {activityId} loads the map state
	SKY_TRADE_SPEED_UP_CMD     = "000347db" // 215003 {activityId,num} burn num acceleration items
	SKY_TRADE_MOVE_CMD         = "000347dc" // 215004 {activityId,port} sail the idle ship
	SKY_TRADE_BUY_CMD          = "000347dd" // 215005 {activityId,port,goodsId,num,type} buy (type 3) AND sell (type 0)
	SKY_TRADE_MOVE_FINISH_CMD  = "000347e1" // 215009 {activityId} claim the arrival at port
	SKY_TRADE_REPORT_GOODS_CMD = "000347f0" // 215024 {activityId} publish current port's rare goods
)

const (
	SERVER_SKY_TRADE_ENTER        = "3a98" // 15000 SkyTradeEnterActivityResp
	SERVER_SKY_TRADE_ENTER_MAP    = "3a99" // 15001 SkyTradeEnterResp
	SERVER_SKY_TRADE_SPEED_UP     = "3a9b" // 15003 SkyTradeAddSpeedResp
	SERVER_SKY_TRADE_MOVE         = "3a9c" // 15004 SkyTradeGotoPortResp
	SERVER_SKY_TRADE_BUY          = "3a9d" // 15005 SkyTradeDealResp
	SERVER_SKY_TRADE_MOVE_FINISH  = "3aa1" // 15009 SkyTradeGetRewardResp
	SERVER_SKY_TRADE_REPORT_GOODS = "3ab0" // 15024 SkyTradeReportGoodsResp
)

// RebornTrial (Luân Hồi Bí Cảnh, systemOpenId 172): requests 217200..217251,
// incoming transport = id % 100000 (217200 → 17200 = 0x4330). Schema decoded
// live 2026-10-08 from the client's protobuf/RebornTrial asset — see
// ../remoteRebornTrial/RebornTrial.decoded.proto and [[tytd-reborntrial-172]].
const (
	RBT_ENTER_ACTIVITY_CMD  = "00035070" // 217200 {activityId} rank/round/union gate
	RBT_ENTER_CMD           = "00035071" // 217201 {activityId} load map state (Battle window)
	RBT_GROUP_INFO_CMD      = "00035072" // 217202 {activityId,groupId} cross-server union table
	RBT_WORSHIP_CMD         = "00035073" // 217203 {activityId} free worship, once per round
	RBT_EXPLORE_CMD         = "00035075" // 217205 {activityId} one caravan step (costs energy)
	RBT_SELECT_OPTION_CMD   = "00035076" // 217206 {activityId,selectIndex=option.order}
	RBT_USE_MAGIC_CMD       = "00035077" // 217207 CD-bound treasure ops (1 repair, 2 illusion, 3 suppress) — free, user authorised 2026-10-09
	RBT_USE_ITEM_CMD        = "00035078" // 217208 SPENDS ITEMS — bot never sends [[tytd-item-ban]]
	RBT_SKIP_EVENT_CMD      = "00035079" // 217209 {activityId} abandon pending event (free)
	RBT_BONUS_CMD           = "00035084" // 217220 {activityId} union-tree info (water board)
	RBT_BONUS_RESULT_CMD    = "00035085" // 217221 {activityId} receive union-tree fruits
	RBT_BONUS_ITEM_CMD      = "00035086" // 217222 {activityId,id} water/claim, id=0 = all
	RBT_SEND_BONUS_ITEM_CMD = "00035087" // 217223 {activityId,itemId,num} donate union-tree liquid — owner-authorised auto spend (phát sạch nước), ONLY ids folded from RENBORNTRIAL_ITEM_ENERGY
	RBT_GUESS_DATA_CMD      = "0003508a" // 217226 {activityId} guess board + claim flags
	RBT_GUESS_REWARD_CMD    = "0003508c" // 217228 {activityId} claim achieved guess rewards
	// 217230/217231/217232 (battle bracket / attack / keep-alive): the 19h
	// guild war is intentionally NOT automated — owner plays it by hand.
	RBT_AUTO_EXPLORE_CMD = "000350a3" // 217251 {activityId,settingItems} server-side loop
)

const (
	SERVER_RBT_ENTER_ACTIVITY  = "4330" // 17200 RebornTrialEnterActivityResp
	SERVER_RBT_ENTER           = "4331" // 17201 RebornTrialDataResp
	SERVER_RBT_WORSHIP         = "4333" // 17203 RebornTrialWorshipResp
	SERVER_RBT_EXPLORE         = "4335" // 17205 RebornTrialDataResp
	SERVER_RBT_SELECT_OPTION   = "4336" // 17206 RebornTrialSelectOptionResp
	SERVER_RBT_USE_MAGIC       = "4337" // 17207 RebornTrialUseMagicResp
	SERVER_RBT_SKIP_EVENT      = "4339" // 17209 RebornTrialSkipEventResp
	SERVER_RBT_BONUS           = "4344" // 17220 RebornTrialBonusResp
	SERVER_RBT_BONUS_RESULT    = "4345" // 17221 RebornTrialGetBonusResultResp
	SERVER_RBT_BONUS_ITEM      = "4346" // 17222 RebornTrialGetBonusItemResp
	SERVER_RBT_SEND_BONUS_ITEM = "4347" // 17223 RebornTrialSendBonusItemResp
	SERVER_RBT_GUESS_DATA      = "434a" // 17226 RebornTrialGuessDataResp
	SERVER_RBT_GUESS_REWARD    = "434c" // 17228 RebornTrialGuessRewardResp
	SERVER_RBT_AUTO_EXPLORE    = "4363" // 17251 RebornTrialAutoExploreRsp
	// Guild-war bracket (live opcode probe 2026-10-09 on actID 9062433:
	// 217230→434e full list, 217232→4350 ret0 heart-beat, 217233→4351
	// ChallengeResp refused ret=17210 for a nonsense target). These ride the
	// seat transport like every other 172 answer.
	RBT_BATTLE_ENTER_CMD          = "0003508e" // 217230 {activityId} bracket list
	RBT_BATTLE_UNIONPLAYER_CMD    = "0003508f" // 217231 {activityId,unionId} base members (live: answer 434f)
	RBT_BATTLE_HEART_CMD          = "00035090" // 217232 {activityId} scene keep-alive
	RBT_BATTLE_CHALLENGE_CMD      = "00035091" // 217233 {activityId,unionId} attack
	SERVER_RBT_BATTLE_ENTER       = "434e"     // RebornTrialChallengeListResp
	SERVER_RBT_BATTLE_UNIONPLAYER = "434f"     // RebornTrialUnionPlayerListResp (playerList = f7 on the wire)
	SERVER_RBT_BATTLE_HEART       = "4350"     // RebornTrialChallengeHeartBeatResp
	SERVER_RBT_BATTLE_CHALLENGE   = "4351"     // RebornTrialChallengeResp
	SERVER_RBT_BATTLE_NOTIC       = "4362"     // RebornTrialChallengeInfoSync push (S_REBORN_TRIAL_BATTLE_NOTIC_SYNC, client table has NO req side). LIVE-CONFIRMED 2026-10-10 on 710547: seen passively (unionmates' hits) and seconds after our own 217233 challenge, decoded clean (playerId/name/targetUnion/damage). The old "4352" was a req−20000 guess that NEVER appeared on the wire — that's why the /luanhoi fight ring was always empty ("vào không thấy đánh").
	SERVER_RBT_USE_ITEM           = "4338"     // 17208 RebornTrialUseItemResp (ticket cmd only)
)

// Tiên Cảnh Vị Ương (WeYound, systemOpenId 186): requests 218600..218638,
// incoming transport = id − 200000 (218600 → 18600 = 0x48a8) — the rule was
// LIVE-PROVED 2026-10-17 (tiencanh_opcode_live_test: 218600→48a8, 218601→
// 48a9, 218606→48ae, 218612→48b4, 218620→48bc, 218621→48bd, 218636→48cc —
// identical scheme to RebornTrial). Schema decoded from the client's
// protobuf/WeYound TextAsset (xor-1) — weyound.decoded.proto. The client
// CodeEnum's 218677..218699 "resp" codes are client-internal labels only;
// the wire carries req−200000.
const (
	WE_YOUND_ENTER_ACTIVITY_CMD     = "000355e8" // 218600 {activityId} rank/round/union gate
	WE_YOUND_ENTER_CMD              = "000355e9" // 218601 {activityId,enterNext} load map / next floor
	WE_YOUND_EXPLORE_CMD            = "000355ea" // 218602 {activityId,pos} clear one fog tile (costs hp)
	WE_YOUND_MOVE_CMD               = "000355eb" // 218603 {activityId,pos} walk an opened tile
	WE_YOUND_DEAL_EVENT_CMD         = "000355ec" // 218604 {activityId,pos,selectView,param1,param2}
	WE_YOUND_USE_ITEM_CMD           = "000355ed" // 218605 SPENDS ITEMS — stamina item owner-banned [[tytd-item-ban]]; NEVER SENT
	WE_YOUND_TRAP_SEEK_CMD          = "000355ee" // 218606 {activityId} free trap reveal
	WE_YOUND_ASSIST_LOG_CMD         = "000355f0" // 218608 {activityId} assist board poll (trapped mates + monsters needing help)
	WE_YOUND_ASSIST_CMD             = "000355ef" // 218607 {activityId,playerId[]} free trap-rescue (monsterId unused — client JS never sends it)
	WE_YOUND_WORSHIP_CMD            = "000355f4" // 218612 {activityId} free worship reward
	WE_YOUND_REWARD_FLOOR_CMD       = "000355f7" // 218615 {activityId,pos?,floor?} floor box claim
	WE_YOUND_AUTO_SWITCH_CMD        = "000355f8" // 218616 SPENDS AUTO_HANDLE_COST (200× item 100000) — NEVER SENT [[tytd-item-ban]]
	WE_YOUND_GET_MONSTER_REWARD_CMD = "000355f9" // 218617 {activityId,monsterId} claim killed-monster reward
	WE_YOUND_BONUS_CMD              = "000355fc" // 218620 {activityId} union-tree board
	WE_YOUND_GET_BONUS_RESULT_CMD   = "000355fd" // 218621 {activityId} harvest ripe tree fruit
	WE_YOUND_GET_BONUS_ITEM_CMD     = "000355fe" // 218622 {activityId,id} collect ready water notice
	WE_YOUND_SEND_BONUS_ITEM_CMD    = "000355ff" // 218623 {activityId,itemId,num} donate tree water — owner-authorised auto spend ("tự tưới cây"), ONLY ids folded from WeYoundActivity_ITEM_DONATE
	WE_YOUND_UNION_GIFT_LIST_CMD    = "0003560c" // 218636 {activityId} guild gift notices
	WE_YOUND_RECEIVE_UNION_GIFT_CMD = "0003560d" // 218637 {activityId,recordId} open one gift
)

const (
	SERVER_WE_YOUND_ENTER_ACTIVITY     = "48a8" // 18600 WeYoundEnterActivityResp
	SERVER_WE_YOUND_ENTER              = "48a9" // 18601 WeYoundEnterResp
	SERVER_WE_YOUND_EXPLORE            = "48aa" // 18602 WeYoundExploreResp
	SERVER_WE_YOUND_MOVE               = "48ab" // 18603 WeYoundMoveResp
	SERVER_WE_YOUND_DEAL_EVENT         = "48ac" // 18604 WeYoundDealEventResp
	SERVER_WE_YOUND_TRAP_SEEK          = "48ae" // 18606 WeYoundTrapSeekResp
	SERVER_WE_YOUND_ASSIST             = "48af" // 18607 WeYoundAssistResp
	SERVER_WE_YOUND_ASSIST_LOG         = "48b0" // 18608 WeYoundAssistLogResp
	SERVER_WE_YOUND_WORSHIP            = "48b4" // 18612 WeYoundWorshipResp
	SERVER_WE_YOUND_REWARD_FLOOR       = "48b7" // 18615 WeYoundRewardFloorResp
	SERVER_WE_YOUND_GET_MONSTER_REWARD = "48b9" // 18617 WeYoundGetMonsterRewardResp
	SERVER_WE_YOUND_BONUS              = "48bc" // 18620 WeYoundBonusResp
	SERVER_WE_YOUND_GET_BONUS_RESULT   = "48bd" // 18621 WeYoundGetBonusResultResp
	SERVER_WE_YOUND_GET_BONUS_ITEM     = "48be" // 18622 WeYoundGetBonusItemResp
	SERVER_WE_YOUND_SEND_BONUS_ITEM    = "48bf" // 18623 WeYoundSendBonusItemResp
	SERVER_WE_YOUND_UNION_GIFT_LIST    = "48cc" // 18636 WeYoundUnionGiftListResp
	SERVER_WE_YOUND_RECEIVE_UNION_GIFT = "48cd" // 18637 WeYoundReceiveUnionGiftResp
)

// Thương Minh Yêu Vực (CelestialSeaWar, activity type 349, client bundle
// remoteGoldPeach / FGUI RemoteCelestialSeaWar): requests 234901..234946,
// incoming transport = id − 200000 (234901 → 34901 = 0x8855) — same scheme
// as RebornTrial/WeYound. Schema mined 2026-11-09 from the client's
// protobuf/SeaWar TextAsset (xor-1) — csw_sim/SeaWar.proto +
// CelestialSeaWarMsgInfo.json, see [[tytd-seawar-port]].
const (
	SW_ENTER_CMD                = "00039595" // 234901 {activityId} gate: ret/canWorship/regionId
	SW_ENTER_GAME_CMD           = "00039596" // 234902 {activityId} load board: playerData + curEventData
	SW_EXPLORE_CMD              = "0003959a" // 234906 {activityId} one step (costs strength) → eventData
	SW_HANDLE_EVENT_CMD         = "0003959b" // 234907 {activityId,eventId,action,id} resolve current event
	SW_UNION_WELFARE_CMD        = "000395a2" // 234914 {activityId} union welfare board
	SW_UNION_WELFARE_REWARD_CMD = "000395a3" // 234915 {activityId} claim welfare when canReceive
	SW_UNION_RECEIVE_LIQUID_CMD = "000395a4" // 234916 {activityId,id} — id=0 = "Nhận nhanh" claim-all (id MUST be present on wire)
	SW_UNION_SEND_LIQUID_CMD    = "000395a5" // 234917 {activityId,itemId,num} phát linh lộ — SPENDS bag items: owner-order only, AUTO never
	SW_UNION_LIQUID_RECORD_CMD  = "000395a6" // 234918 {activityId,type} xem lịch sử linh lộ (read-only)
	SW_HEART_BEAT_CMD           = "000395a7" // 234919 {activityId,challengeState} scene keep-alive (>20 s)
	SW_NEXT_REGION_CMD          = "000395a8" // 234920 {activityId,nextRegionId,useItem=false} free advance
	SW_USE_ITEM_CMD             = "000395aa" // 234922 {activityId,num} SPENDS items (SEAWAR_ENERGY_ITEM) — BUTTON ONLY [[tytd-item-ban]]
	SW_ASSIST_LIST_CMD          = "0003959d" // 234909 {activityId} assist board: teammates needing help + own finished entries
	SW_ASSIST_CMD               = "0003959e" // 234910 {activityId,type,targetPlayerId,id} help a teammate (0=fight 1=box) — free
	SW_ASSIST_REWARD_CMD        = "000395a0" // 234912 {activityId,type,id} claim finished assist reward — free
	SW_WORSHIP_CMD              = "000395ae" // 234926 {activityId} free worship when canWorship
	// 234931..234944 (challenge/declaration guild-war family) is
	// intentionally NOT automated — ORDER-ONLY law (same as LHBC guild war):
	// these cmds leave the socket ONLY from the sanctioned web war-map order
	// (t.swOrderAttack → ep.swWarOrderOne), never from the auto engine.
	SW_CHALLENGE_LIST_CMD   = "000395b3" // 234931 {activityId} union bases + canBattleUnionIds — map pull
	SW_ENTER_CHALLENGE_CMD  = "000395b8" // 234936 {activityId,unionId} enter a union base → declarationInfos (rooms)
	SW_CHALLENGE_INFO_CMD   = "000395b9" // 234937 {activityId,unionId,roomId} room defenders (beChallengedInfos)
	SW_CHALLENGE_BATTLE_CMD = "000395ba" // 234938 {activityId,unionId,roomId,playerId} attack a defender — ORDER ONLY
	SW_DECLARATION_CMD      = "000395c0" // 234944 {activityId,unionId,type,isFree} declare war — ORDER ONLY
)

const (
	SERVER_SW_ENTER                = "8855" // 34901 SeaWarEnterResp
	SERVER_SW_ENTER_GAME           = "8856" // 34902 SeaWarEnterGameResp
	SERVER_SW_EXPLORE              = "885a" // 34906 SeaWarExploreResp
	SERVER_SW_HANDLE_EVENT         = "885b" // 34907 SeaWarHandleEventResp
	SERVER_SW_UNION_WELFARE        = "8862" // 34914 SeaWarUnionWelfareResp
	SERVER_SW_UNION_WELFARE_REWARD = "8863" // 34915 SeaWarUnionWelfareRewardResp
	SERVER_SW_UNION_RECEIVE_LIQUID = "8864" // 34916 SeaWarUnionLiquidRewardResp
	SERVER_SW_UNION_SEND_LIQUID    = "8865" // 34917 SeaWarUnionLiquidSendResp
	SERVER_SW_UNION_LIQUID_RECORD  = "8866" // 34918 SeaWarUnionLiquidRecordResp
	SERVER_SW_HEART_BEAT           = "8867" // 34919 SeaWarHeartBeatResp
	SERVER_SW_NEXT_REGION          = "8868" // 34920 SeaWarNextRegionResp
	SERVER_SW_USE_ITEM             = "886a" // 34922 SeaWarUseItemResp
	SERVER_SW_ASSIST_LIST          = "885d" // 34909 SeaWarAssistListResp
	SERVER_SW_ASSIST               = "885e" // 34910 SeaWarAssistRsp
	SERVER_SW_ASSIST_REWARD        = "8860" // 34912 SeaWarAssistRewardRsp
	SERVER_SW_ASSIST_SYNC          = "8861" // 34913 SeaWarAssistSync PUSH — board changed, rings the wake bell
	SERVER_SW_ASSIST_SYNC_RAW      = "95a1" // …server actually sends it as RAW low16 (live recv map, 2026-11-09) — both accepted
	SERVER_SW_WORSHIP              = "886e" // 34926 SeaWarWorshipResp
	// War family answers (req−2000000 low16): accepted by BOTH readers, the
	// web order seats its round-trips on these; pushes feed the map live.
	SERVER_SW_CHALLENGE_LIST        = "8873" // 34931 SeaWarChallengeListResp
	SERVER_SW_CHALLENGE_INFO_SYNC   = "8874" // 34932 SeaWarChallengeInfoSync PUSH
	SERVER_SW_DECLARATION_INFO_SYNC = "8875" // 34933 SeaWarDeclarationInfoSync PUSH
	SERVER_SW_ENTER_CHALLENGE       = "8878" // 34936 SeaWarEnterChallengeResp
	SERVER_SW_CHALLENGE_INFO        = "8879" // 34937 SeaWarRoomInfoResp (defenders)
	SERVER_SW_CHALLENGE_BATTLE      = "887a" // 34938 SeaWarChallengeResp (battle verdict)
	SERVER_SW_DECLARATION           = "8880" // 34944 SeaWarDeclarationResp (宣战 — the lệnh pick, MUST precede battle)
)

// Yard (vườn nhà) production chain: snapshot → make → collect. Request IDs
// and incoming types come from ../remoteAll/YARD_PRODUCTION_CHAIN.md.
const (
	YARD_ENTER_CMD       = "00034af9" // 215801 YardEnterReq {searchInfo[], targetPlayerId}
	YARD_DRAW_CMD        = "00034b0e" // 215822 YardDrawReq {isTen, isUseADTime, isADType, poolId, times}
	YARD_BUILD_MAKE_CMD  = "00034b11" // 215825 YardBuildMakeReq {uniqueId, buildId, productId, num, isCancel}
	YARD_GAIN_REWARD_CMD = "00034b13" // 215827 YardBuildGainRewardReq {uniqueId, buildId}
)

const (
	SERVER_YARD_ENTER       = "3db9" // 15801 YardEnterResp (yard snapshot)
	SERVER_YARD_DRAW        = "3dce" // 15822 YardDrawResp
	SERVER_YARD_MAKE        = "3dd1" // 15825 YardBuildMakeResp
	SERVER_YARD_GAIN_REWARD = "3dd3" // 15827 YardBuildGainRewardResp
	SERVER_YARD_STATUS_SYNC = "3de8" // 15848 push: one instance's detail replaced
	SERVER_YARD_MAKE_NUM    = "3de9" // 15849 push: production inventory (dan/grass/crop counts)
	SERVER_YARD_DATA_SYNC   = "3dea" // 15850 push: daily draw counters re-synced
)

// Union Bounty (yêu minh huyền thưởng — the guild "bang treo thuong" the
// first pass of this file had already spotted under those names). Requests
// and the incoming transport of each response (type = id % 100000) from
// ../remoteAll/UNION_BOUNTY_PROTOCOL.md / UNION_BOUNTY_FLOW.md. OPEN loads or
// generates today's event, DEAL type=1 starts its escort, GET_REWARD settles
// either kind once bountyInfo.finishReward says claimable.
const (
	UNION_BOUNTY_DEAL_CMD           = "00034265" // 213605 {curConfigId, type} — type 2 (abandon) is never sent
	UNION_BOUNTY_GET_REWARD_CMD     = "00034266" // 213606 {}
	UNION_BOUNTY_OPEN_EVENT_CMD     = "0003426e" // 213614 {}
	UNION_BOUNTY_HAVE_ESCORT_CMD    = "00034271" // 213617 {}
	UNION_BOUNTY_OPEN_MONSTER_CMD   = "00034273" // 213619 {playerId} — owner uses own id
	UNION_BOUNTY_ATTACK_MONSTER_CMD = "00034274" // 213620 {teamPlayerList[{playerId,pos}], bossId=event}
)

const (
	SERVER_UNION_BOUNTY_DEAL           = "3525" // 13605 {ret, playerData, myCart, bountyInfo}
	SERVER_UNION_BOUNTY_GET_REWARD     = "3526" // 13606 {ret, configId, reward}
	SERVER_UNION_BOUNTY_OPEN_EVENT     = "352e" // 13614 {ret, bountyInfo, playerData, isNewEvent}
	SERVER_UNION_BOUNTY_HAVE_ESCORT    = "3531" // 13617 {ret, exist}
	SERVER_UNION_BOUNTY_OPEN_MONSTER   = "3533" // 13619 {ret, event, battleMainList, monsterPower}
	SERVER_UNION_BOUNTY_ATTACK_MONSTER = "3534" // 13620 {ret, allBattleRecord, reward}
)

// Gather Energy (tụ linh trận). Request IDs and the incoming transport of
// each response (type = id % 100000) from
// ../remoteAll/GATHER_ENERGY_PROTOCOL.md. The open/join window is
// ENERGY_AREA_OPEN_TIME 600|720 — 10:00–22:00 ICT; outside it the server
// still runs live formations (and keeps paying them) but rejects opens and
// joins, so the auto loop keeps polling rewards and ads overnight.
const (
	GATHER_ENTER_NEW_CMD       = "00032899" // 207001 {} — full state, pending rewards, totals, getTimes
	GATHER_GET_AD_AWARD_CMD    = "000328ab" // 207019 {isUseADTime} — one opening item per watched ad
	GATHER_GET_REWARD_CMD      = "000328a3" // 207011 {id} — claim one queued finished-participation reward
	GATHER_FIRST_LIST_VIEW_CMD = "000328ac" // 207020 {offset, filterType} — compact formation list
	GATHER_INSIDE_VIEW_CMD     = "000328ae" // 207022 {id} — full formation state incl. five-slot team
	GATHER_ATTEND_NEW_CMD      = "000328b1" // 207025 {id} — join, server assigns the seat
)

const (
	SERVER_GATHER_SYNC        = "1b5a" // 7002 push: GatherEnergyMsg state update
	SERVER_GATHER_REWARD_SHOW = "1b62" // 7010 push: a participation ended → GET_REWARD its id
	SERVER_GATHER_ENTER_NEW   = "1b59" // 7001 GatherEnergyEnterNewResp
	SERVER_GATHER_AD_AWARD    = "1b6b" // 7019 GatherEnergyGetADAwardResp
	SERVER_GATHER_FIRST_LIST  = "1b6c" // 7020 GatherEnergyFirstListViewResp
	SERVER_GATHER_INSIDE      = "1b6e" // 7022 GatherEnergyInsideViewNewResp
	SERVER_GATHER_ATTEND      = "1b71" // 7025 GatherEnergyAttendNewResp
	SERVER_GATHER_GET_REWARD  = "1b63" // 7011 GatherEnergyRewardResp
)

// Task system (nhiệm vụ). Transport type follows the shared client rule
// id >= 100000 ? id % 100000 : id % 10000 (TASK_PROTOCOL.md): 20501..20503
// arrive as 501/502/503. 501/502 are unsolicited task-list pushes;
// GET_REWARD claims a batch of claimable task ids.
const (
	TASK_GET_REWARD_CMD = "00005017" // 20503 {taskId[]} — unpacked 08 per id
)

const (
	SERVER_TASK_DATA_SEND  = "01f5" // 20501 push: full TaskDataListMsg snapshot
	SERVER_TASK_DATA_SYNC  = "01f6" // 20502 push: per-task merge
	SERVER_TASK_GET_REWARD = "01f7" // 20503 {ret, rewards}; state updates arrive via 502
)

// Big Chess (Tiên Dịch Bài Cục, activity type 341) — AI-play transport for
// the MCP tools. Full IDs from ../remoteBigChess/BIG_CHESS_PROTOCOL.md;
// answers arrive with id % 100000 (234101 → 34101 → 0x8535). MCP tools serve
// ranked mode; the practice trio is free (no starts, no ingots) and wired for
// the engine playtest only.
const (
	BIG_CHESS_ENTER_CMD      = "00039275" // 234101 {activityId} lobby snapshot
	BIG_CHESS_ENTER_GAME_CMD = "00039276" // 234102 {activityId} start OR resume a run
	BIG_CHESS_MOVE_CMD       = "00039277" // 234103 {activityId,x,y,powerOn,powerCross,powerRemoveType}
	BIG_CHESS_END_CMD        = "00039278" // 234104 {activityId} settle the run
	BIG_CHESS_USE_ITEM_CMD   = "00039279" // 234105 {activityId,type} 0=revive 1=guide
	BIG_CHESS_USE_RANDOM_CMD = "0003927a" // 234106 {activityId} remove random pieces
	// Practice trio: free runs (no starts, no currency). Practice move
	// carries NO power fields (decoded.proto: activityId,x,y only).
	BIG_CHESS_PRACTICE_ENTER_GAME_CMD = "00039289" // 234121 {activityId}
	BIG_CHESS_PRACTICE_MOVE_CMD       = "0003928a" // 234122 {activityId,x,y}
	BIG_CHESS_PRACTICE_END_CMD        = "0003928b" // 234123 {activityId}
)

const (
	SERVER_BIG_CHESS_ENTER      = "8535" // 34101 ChineseChessEnterResp
	SERVER_BIG_CHESS_ENTER_GAME = "8536" // 34102 ChineseChessEnterGameResp
	SERVER_BIG_CHESS_MOVE       = "8537" // 34103 ChineseChessMoveResp
	SERVER_BIG_CHESS_END        = "8538" // 34104 ChineseChessEndResp
	SERVER_BIG_CHESS_USE_ITEM   = "8539" // 34105 ChineseChessUseItemResp
	SERVER_BIG_CHESS_USE_RANDOM = "853a" // 34106 ChineseChessUseRemoveItemResp

	SERVER_BIG_CHESS_PRACTICE_ENTER_GAME = "8549" // 34121 (ranked enter-game layout)
	SERVER_BIG_CHESS_PRACTICE_MOVE       = "854a" // 34122 ChineseChessPracticeMoveResp
	SERVER_BIG_CHESS_PRACTICE_END        = "854b" // 34123 ChineseChessPracticeEndResp (score=3!)
)

// Luyện Khí Đại Hội (remoteCastSword, a 2048-style 4x4 weapon-merge board;
// systemOpenId CASTSWORD=119). Opcodes from the client Protocol enum; the
// answer opcodes follow the house rule (request - 2000000), and the wire
// schema was recovered live (see internal/proto/tytd.proto + the live probe).
// USETOOL/REFRESHSTRENGTH are intentionally ABSENT: spending those is an
// item action under the permanent item ban.
const (
	CASTSWORD_ENTER_CMD        = "001eb2fd" // 2011901 {activityId} board+score+strength
	CASTSWORD_MOVE_CMD         = "001eb2fe" // 2011902 {activityId,direction 1UP 2DOWN 3LEFT 4RIGHT}
	CASTSWORD_SETTLEMENT_CMD   = "001eb2ff" // 2011903 {activityId} close the run / bank score
	CASTSWORD_GETREWARD_CMD    = "001eb301" // 2011905 {activityId} claim achievement rewards
	CASTSWORD_GAMEOVER_CMD     = "001eb303" // 2011907 {activityId}
	CASTSWORD_RANKSYNC_CMD     = "001eb304" // 2011908 {activityId}
	SERVER_CASTSWORD_ENTER     = "2e7d"     // 11901
	SERVER_CASTSWORD_MOVE      = "2e7e"     // 11902
	SERVER_CASTSWORD_SETTLE    = "2e7f"     // 11903
	SERVER_CASTSWORD_GETREWARD = "2e81"     // 11905
)
