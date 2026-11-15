package tytd_go

/*
Auto Tiên Cảnh Vị Ương (WeYound, systemOpenId 186) — the /auto_tiencanh
feature, one loop per login, gated by the account's opt-in DB switch,
modeled on Luân Hồi Bí Cảnh [[tytd-reborntrial-172]].

Protocol decoded 2026-10-17 from the client's protobuf/WeYound TextAsset
(xor-1) — weyound.decoded.proto; the opcode transport was LIVE-PROVED the
same day (tiencanh_opcode_live_test): requests 218600.., answers = req −
200000 ("48a8"…), refuse family 15001 (window closed) / 18629 (worship
used) observed live on server 710564.

The game: a fog-explore roguelike map inside a daily window
(WeYoundActivity_EXPLORE_DURATION, live "10|22" ICT). EXPLORE (218602)
burns one stamina point (player.hp, tops at ORIGIN_ENERGY 30, recovers on
ENERGY_COLDTIME 900s) and clears a FOG tile: view cells + rewards + event
pop. Pending events (player.unDealCell) block the board and resolve via
DEAL_EVENT (218604) with the client's exact per-type body (verified in
RemoteWeYoundMgr doEventCase → do*Event):

	Monster 1 → DEAL_EVENT{pos, param1=1} attack, then GET_MONSTER_REWARD
	            (218617) once remainBloods ≤ 0 — free receives only.
	            Additionally the board-wide sweep (wySweepMonsters, owner law
	            2026-11-13 "đánh luôn") swings at EVERY live monster cell on
	            the shared floor each march pass — unDealCell only ever lists
	            events WE opened (live: 9 monsters up, unDealCell empty), and
	            attacks are free (no stamina, accepted at hp=0).
	Select  2 → DEAL_EVENT{pos, param1=monsterParam−1} — the client's own
	            auto path picks the HIGHEST tier (config eventConfig
	            monsterParam; refusals retire the event, never stall)
	Buff    3 → DEAL_EVENT{pos} (null params)
	Talk    7 → DEAL_EVENT{pos} (null params)
	Trap    4 → TRAP_SEEK (218606) — free reveal, no items
	Box     6 → rewards already rode the EXPLORE answer (draw) — nothing
	Light   5 → DEAL_EVENT{pos, param1=1, param2=1} try-once-then-retire
	            (multi-select puzzle — human UI picks areas; a refuse
	            retires the cell so the march never loops on it)

Movement mirrors the client: walk (MOVE 218603, pos = destination) to the
target cell first, then the op — the server keeps its own position and
refuses out-of-reach ops itself; the answers are the oracle, like the
caravan's snapshot-first rule. A reachable NextFloor tile (type 5) opens
the next floor via ENTER {enterNext:true} — the client's own
nextIsUnlock-on-sight rule. A refused EXPLORE ends the march until the
next poll (stamina spent or window closed; the refuse code tells which in
the log).

HARD RULES [[tytd-item-ban]] — owner 2026-10-17 "tự mua gói tự chạy tự
tưới cây (không cắn item tăng thể lực)":
  * USE_ITEM (218605) is NEVER sent. The stamina item (ENERGY_ITEM folded
    = 105185) is never burned; the message itself is not even modelled.
    AUTO_SWITCH (218616, costs 200× item 100000) and the PVP challenge
    family (218630..218635) are likewise never sent — guild war stays a
    human call, same ruling as RebornTrial.
  * SEND_BONUS_ITEM (218623) — "tự tưới cây" — is sent ONLY for the ids
    folded from WeYoundActivity_ITEM_DONATE ("105192|5;105193|10;105194|20"
    live), num=1 each press exactly like the client's UseItemView, stopping
    at the first refusal. Empty rbtLiquid-equivalent (no 21003 detail
    folded yet) = the opcode is NEVER sent (armed-by-config, same as
    rbtLiquidGrant).
  * Shop (21003/21005, shared generic opcodes): rbtMallAuto gate — FREE
    ("Phúc Lợi Miễn Phí"), AD ("Gói Video Cổ Vũ", param "3|1800|0|0") and
    TIÊN NGỌC rows (price "100000=N") only, each to its buyLimit/day.
    Money packs (numeric nạp price, param "1|…") are structurally never
    sent.

Free receives every tree cycle: BONUS board (218620) → GET_BONUS_RESULT
(218621, ripe fruit) → "Nhận nhanh" = GET_BONUS_ITEM (218622) with id=0
present on the wire — ONE press sweeps every ready notice (owner law
2026-11-13; per-notice ids are the fallback if the claim-all is refused),
guild gifts UNION_GIFT_LIST (218636) → RECEIVE_UNION_GIFT (218637), worship
(218612) once per round, and the floor box via REWARD_FLOOR (218615,
panel shape {floor:-1}).

NẠP BANG milestones (owner 2026-11-13 "thêm auto nhận thưởng nạp bang"):
wyCondSweep walks the 21003 conditionConfig rows folded for THIS activity —
per-id 21045 (cross-union recharge rungs, task type 84 / condID ≥ 20000) and
one BLIND 21033 per other task type, on the same tree cadence. The claim
doors are the shared generic opcodes (identical to Luân Hồi's rbtCondSweep);
stored condition values are stale zeros — NEVER gate locally, the server
settles only genuinely-ready rungs and claiming is idempotent.

HỖ TRỢ / assist (owner 2026-11-13 "khi đánh quái không chết, hệ thống sẽ tự
gọi hỗ trợ. thêm poll check có ai cần hỗ trợ thì hỗ trợ"): every 2 min
(wyAssistGap, runs outside the explore window too) wyAssistCycle polls the
assist board ASSIST_LOG (218608) and frees EVERY trapped union member with
one batch ASSIST (218607 {activityId, playerId[]}) — free, quota (assistNum)
server-managed, ret≠0 quiet. Monsters on the board need no extra packet:
the client JS never sends ASSIST's monsterId (always null); helping a
struggling monster = attacking it, which is exactly wySweepMonsters.

ĐÁ LINH VŨ (owner 2026-11-13 "có 3 loại đá linh vũ, đây là item được sử dụng
thoải mái"): the three donate ids (105192|5, 105193|10, 105194|20 — live
ITEM_DONATE fold) are free-to-spend — wyWaterTree dumps them all every cycle
(num=1 presses, per-type refusal = bag empty = stop). This is an explicit
item-ban exception [[tytd-item-ban]]; the stamina fruit (105185) is NOT —
only the live probe may bite it.

Every decision recomputes from a fresh ENTER, so a server push (floor
advance, window close, stamina drain) changes the next tick, not our
ledger.
*/

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const (
	wyActivityType = 186

	wyPoll        = time.Minute     // in-window decision cycle
	wyStatusGap   = 5 * time.Minute // heartbeat while actually playing
	wyReqTimeout  = 15 * time.Second
	wyStepBudget  = 60               // explores per tick — sanity cap, never a goal
	wyEventBudget = 30               // event resolves per tick
	wyTreeGap     = 10 * time.Minute // union-tree + shop + gifts cadence
	wyAssistGap   = 2 * time.Minute  // assist-board poll (cứu đồng đội mắc bẫy)

	wyLiquidGrantCap = 100 // bottles per type per cycle — sanity cap

	// Tile types (WeYoundTileType, client-proven):
	wyTileFog       = 1
	wyTileEvent     = 2
	wyTileBlank     = 3
	wyTileObstacle  = 4
	wyTileNextFloor = 5
	wyTileBase      = 7
	wyTileBoxNULL   = 8
	wyTileBox       = 9

	// Event types (WeYoundEventType):
	wyEvNull    = 0
	wyEvMonster = 1
	wyEvSelect  = 2
	wyEvBuff    = 3
	wyEvTrap    = 4
	wyEvLight   = 5
	wyEvBox     = 6
	wyEvTalk    = 7

	wyRetCloudPass = 18602 // explore refused: must advance floor first
	wyRetPhase     = 15001 // window/phase closed — stamina cannot fix it
	// (18628 = "monster already swung" — see wySweepMonsters; never gated on,
	// only logged, the ledger already forbids a second swing)
)

// ─── wire helpers ────────────────────────────────────────────────────────────

func (p *Player) wyRoundTrip(cmd, wantType string, body []byte) ([]byte, error) {
	return p.stRoundTrip(cmd, wantType, body, wyReqTimeout)
}

func wyActivityBody(actID int32) []byte {
	b, err := pb.Marshal(&proto.WeYoundEnterActivityReq{ActivityId: pb.Int32(actID)})
	if err != nil { // scalar — structurally impossible
		return nil
	}
	return b
}

func (p *Player) wyEnterActivity(actID int32) (*proto.WeYoundEnterActivityResp, error) {
	data, err := p.wyRoundTrip(WE_YOUND_ENTER_ACTIVITY_CMD, SERVER_WE_YOUND_ENTER_ACTIVITY, wyActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundEnterActivityResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) wyEnter(actID int32, enterNext bool) (*proto.WeYoundEnterResp, error) {
	body, err := pb.Marshal(&proto.WeYoundEnterReq{ActivityId: pb.Int32(actID), EnterNext: pb.Bool(enterNext)})
	if err != nil {
		return nil, err
	}
	data, err := p.wyRoundTrip(WE_YOUND_ENTER_CMD, SERVER_WE_YOUND_ENTER, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundEnterResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func wyPosBody(actID, x, y int32) []byte {
	body, err := pb.Marshal(&proto.WeYoundExploreReq{
		ActivityId: pb.Int32(actID),
		Pos:        &proto.WeYoundPosMsg{X: pb.Int32(x), Y: pb.Int32(y)},
	})
	if err != nil {
		return nil
	}
	return body
}

func (p *Player) wyMove(actID, x, y int32) (int32, error) {
	data, err := p.wyRoundTrip(WE_YOUND_MOVE_CMD, SERVER_WE_YOUND_MOVE, wyPosBody(actID, x, y))
	if err != nil {
		return -1, err
	}
	var rsp proto.WeYoundMoveResp
	if pb.Unmarshal(data, &rsp) != nil {
		return -1, fmt.Errorf("bad move resp")
	}
	return rsp.GetRet(), nil
}

func (p *Player) wyExplore(actID, x, y int32) (*proto.WeYoundExploreResp, error) {
	data, err := p.wyRoundTrip(WE_YOUND_EXPLORE_CMD, SERVER_WE_YOUND_EXPLORE, wyPosBody(actID, x, y))
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundExploreResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// wyDealEventLite mirrors reqDealEvent exactly: pos is REQUIRED, selectView
// is the light-area pick list (empty for every other event), param1/param2
// carry the per-event-type choice (see player_weyound_march.go).
func (p *Player) wyDealEventLite(actID, x, y int32, param1, param2 *int32,
	selectView []*proto.WeYoundPosMsg) (*proto.WeYoundDealEventResp, error) {
	req := &proto.WeYoundDealEventReq{
		ActivityId: pb.Int32(actID),
		Pos:        &proto.WeYoundPosMsg{X: pb.Int32(x), Y: pb.Int32(y)},
		SelectView: selectView,
		Param1:     param1,
		Param2:     param2,
	}
	body, err := pb.Marshal(req)
	if err != nil {
		return nil, err
	}
	data, err := p.wyRoundTrip(WE_YOUND_DEAL_EVENT_CMD, SERVER_WE_YOUND_DEAL_EVENT, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundDealEventResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) wyTrapSeek(actID int32) (int32, error) {
	data, err := p.wyRoundTrip(WE_YOUND_TRAP_SEEK_CMD, SERVER_WE_YOUND_TRAP_SEEK, wyActivityBody(actID))
	if err != nil {
		return -1, err
	}
	var rsp proto.WeYoundTrapSeekResp
	if pb.Unmarshal(data, &rsp) != nil {
		return -1, fmt.Errorf("bad trapseek resp")
	}
	return rsp.GetRet(), nil
}

// wyAssistLog polls the union assist board (218608): trapped members AND
// monsters whose attacker could not finish — the server auto-posts the call
// when an attack leaves the monster alive (owner 2026-11-13).
func (p *Player) wyAssistLog(actID int32) (*proto.WeYoundAssistLogResp, error) {
	body, err := pb.Marshal(&proto.WeYoundAssistLogReq{ActivityId: pb.Int32(actID)})
	if err != nil {
		return nil, err
	}
	data, err := p.wyRoundTrip(WE_YOUND_ASSIST_LOG_CMD, SERVER_WE_YOUND_ASSIST_LOG, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundAssistLogResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// wyAssist frees trapped union members (218607 {activityId, playerId[]}).
// FREE on the wire — the assistNum quota is server-managed. monsterId stays
// unset: the client JS never sends it (one call site, always null) — monster
// "assist" IS the DEAL_EVENT attack, i.e. wySweepMonsters' job.
func (p *Player) wyAssist(actID int32, playerIDs []int64) (*proto.WeYoundAssistResp, error) {
	body, err := pb.Marshal(&proto.WeYoundAssistReq{ActivityId: pb.Int32(actID), PlayerId: playerIDs})
	if err != nil {
		return nil, err
	}
	data, err := p.wyRoundTrip(WE_YOUND_ASSIST_CMD, SERVER_WE_YOUND_ASSIST, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundAssistResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) wyWorship(actID int32) (*proto.WeYoundWorshipResp, error) {
	data, err := p.wyRoundTrip(WE_YOUND_WORSHIP_CMD, SERVER_WE_YOUND_WORSHIP, wyActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundWorshipResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// wyRewardFloor claims the floor box in the panel shape the client itself
// uses (pos nil, floor −1 → "claim the current floor's box").
func (p *Player) wyRewardFloor(actID int32) (*proto.WeYoundRewardFloorResp, error) {
	body, err := pb.Marshal(&proto.WeYoundRewardFloorReq{ActivityId: pb.Int32(actID), Floor: pb.Int32(-1)})
	if err != nil {
		return nil, err
	}
	data, err := p.wyRoundTrip(WE_YOUND_REWARD_FLOOR_CMD, SERVER_WE_YOUND_REWARD_FLOOR, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundRewardFloorResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) wyGetMonsterReward(actID int32, monsterID int64) (*proto.WeYoundGetMonsterRewardResp, error) {
	body, err := pb.Marshal(&proto.WeYoundGetMonsterRewardReq{ActivityId: pb.Int32(actID), MonsterId: pb.Int64(monsterID)})
	if err != nil {
		return nil, err
	}
	data, err := p.wyRoundTrip(WE_YOUND_GET_MONSTER_REWARD_CMD, SERVER_WE_YOUND_GET_MONSTER_REWARD, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundGetMonsterRewardResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) wyBonusInfo(actID int32) (*proto.WeYoundBonusResp, error) {
	data, err := p.wyRoundTrip(WE_YOUND_BONUS_CMD, SERVER_WE_YOUND_BONUS, wyActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundBonusResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) wyBonusResult(actID int32) (*proto.WeYoundGetBonusResultResp, error) {
	data, err := p.wyRoundTrip(WE_YOUND_GET_BONUS_RESULT_CMD, SERVER_WE_YOUND_GET_BONUS_RESULT, wyActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundGetBonusResultResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) wyBonusItem(actID int32, id int64) (*proto.WeYoundGetBonusItemResp, error) {
	body, err := pb.Marshal(&proto.WeYoundGetBonusItemReq{ActivityId: pb.Int32(actID), Id: pb.Int64(id)})
	if err != nil {
		return nil, err
	}
	data, err := p.wyRoundTrip(WE_YOUND_GET_BONUS_ITEM_CMD, SERVER_WE_YOUND_GET_BONUS_ITEM, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundGetBonusItemResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// wyGrantWater donates one bottle (client sends num=1 per press). Ban-by-
// config: the CALLER iterates only folded wyLiquidIDs — empty means the
// opcode is never reached.
func (p *Player) wyGrantWater(actID, itemID int32) (int32, error) {
	body, err := pb.Marshal(&proto.WeYoundSendBonusItemReq{
		ActivityId: pb.Int32(actID), ItemId: pb.Int32(itemID), Num: pb.Int32(1),
	})
	if err != nil {
		return -1, err
	}
	data, err := p.wyRoundTrip(WE_YOUND_SEND_BONUS_ITEM_CMD, SERVER_WE_YOUND_SEND_BONUS_ITEM, body)
	if err != nil {
		return -1, err
	}
	var rsp proto.WeYoundSendBonusItemResp
	if pb.Unmarshal(data, &rsp) != nil {
		return -1, fmt.Errorf("bad sendbonus resp")
	}
	return rsp.GetRet(), nil
}

func (p *Player) wyUnionGiftList(actID int32) (*proto.WeYoundUnionGiftListResp, error) {
	data, err := p.wyRoundTrip(WE_YOUND_UNION_GIFT_LIST_CMD, SERVER_WE_YOUND_UNION_GIFT_LIST, wyActivityBody(actID))
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundUnionGiftListResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

func (p *Player) wyReceiveGift(actID int32, recordID int64) (*proto.WeYoundReceiveUnionGiftResp, error) {
	body, err := pb.Marshal(&proto.WeYoundReceiveUnionGiftReq{ActivityId: pb.Int32(actID), RecordId: pb.Int64(recordID)})
	if err != nil {
		return nil, err
	}
	data, err := p.wyRoundTrip(WE_YOUND_RECEIVE_UNION_GIFT_CMD, SERVER_WE_YOUND_RECEIVE_UNION_GIFT, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundReceiveUnionGiftResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// wyHandleFrame logs refusals then hands the answer to any waiting seat
// (same transport as the sky-trade / RebornTrial plumbing).
func (p *Player) wyHandleFrame(msgType string, data []byte) {
	switch msgType {
	case SERVER_WE_YOUND_ENTER:
		var rsp proto.WeYoundEnterResp
		if pb.Unmarshal(data, &rsp) == nil && rsp.GetRet() != 0 {
			log.Printf("[%s] [TCVU] vào bản đồ bị từ chối (code %d)", p.Username, rsp.GetRet())
		}
	case SERVER_WE_YOUND_ENTER_ACTIVITY:
		var rsp proto.WeYoundEnterActivityResp
		if pb.Unmarshal(data, &rsp) == nil && rsp.GetRet() != 0 {
			log.Printf("[%s] [TCVU] vào hoạt động bị từ chối (code %d)", p.Username, rsp.GetRet())
		}
	}
	p.stDeliver(msgType, data)
}

// ─── config folding (21003 answer) ───────────────────────────────────────────

// wyMergeConfig folds the daily explore window, the stamina-item id (ban
// bookkeeping), the tree-water donate ids, the mall table and the event
// config (Select auto-choice monsterParam) out of the hourly 21003 detail
// answer. Only rows whose mainConfig type is 186 are touched.
func (p *Player) wyMergeConfig(data []byte) {
	var rsp proto.RspGetActivityDetail
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return
	}
	dc := rsp.GetActivity().GetDetailConfig()
	cc := dc.GetCommonConfig()
	if cc.GetMainConfig().GetType() != wyActivityType {
		return
	}
	open, close_ := 0, 0
	var energyItem int32
	var liquidIDs []int32
	for _, pc := range cc.GetParamConfig() {
		switch pc.GetParam() {
		case "WeYoundActivity_EXPLORE_DURATION": // "10|22"
			parts := strings.Split(pc.GetValue(), "|")
			if len(parts) == 2 {
				if a, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && a >= 0 && a <= 24 {
					open = a * 3600
				}
				if b, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && b >= 0 && b <= 24 {
					close_ = b * 3600
				}
			}
		case "WeYoundActivity_ENERGY_ITEM": // "105185|2" — NEVER used, folded for the ban log
			if n, err := strconv.Atoi(strings.TrimSpace(strings.Split(pc.GetValue(), "|")[0])); err == nil && n > 0 {
				energyItem = int32(n)
			}
		case "WeYoundActivity_ITEM_DONATE": // "105192|5;105193|10;105194|20"
			for _, part := range strings.Split(pc.GetValue(), ";") {
				kv := strings.Split(strings.TrimSpace(part), "|")
				if n, err := strconv.Atoi(strings.TrimSpace(kv[0])); err == nil && n > 0 {
					liquidIDs = append(liquidIDs, int32(n))
				}
			}
		}
	}
	var mallRows []*proto.ActivityMallTempMsg
	for _, mc := range cc.GetMallConfig() {
		if mt := mc.GetMallTempMsg(); mt != nil {
			mallRows = append(mallRows, mt)
		}
	}
	condCfgs := cc.GetConditionConfig()
	bought := map[int32]int64{}
	for _, bc := range rsp.GetActivity().GetMallBuyCountList() {
		bought[bc.GetMallId()] = bc.GetCount()
	}
	eventCfg := map[int32]*proto.WeYoundEventConfigMsg{}
	mapCfg := map[int32]*proto.WeYoundMapConfigMsg{}
	buffType := map[int32]int32{}
	var sync proto.WeYoundConfigSync
	if len(dc.GetUniqueConfig()) > 0 && pb.Unmarshal(dc.GetUniqueConfig(), &sync) == nil {
		for _, ec := range sync.GetEventConfig() {
			eventCfg[ec.GetId()] = ec
		}
		// floor → CDN map json id + fog dig cost (the fog grid's anchor).
		for _, mc := range sync.GetMapConfig() {
			mapCfg[mc.GetFloor()] = mc
		}
		// id → BuffType (type 1 = FreeExplore, the stamina-gate exemption).
		for _, bc := range sync.GetBuffConfig() {
			buffType[bc.GetId()] = bc.GetType()
		}
	}
	p.WyMu.Lock()
	if open > 0 && close_ > open {
		p.wyOpenSec, p.wyCloseSec = open, close_
	}
	if energyItem > 0 {
		p.wyEnergyItem = energyItem
	}
	if len(liquidIDs) > 0 {
		p.wyLiquidIDs = liquidIDs
	}
	if len(mallRows) > 0 {
		p.wyMallRows = mallRows
	}
	if len(condCfgs) > 0 {
		p.wyCondCfgs = condCfgs // milestone ladders (incl. type-84 nạp-bang rungs)
	}
	p.wyMallBought = bought
	if len(eventCfg) > 0 {
		p.wyEventCfg = eventCfg
	}
	if len(mapCfg) > 0 {
		p.wyMapCfg = mapCfg
	}
	if len(buffType) > 0 {
		p.wyBuffType = buffType
	}
	p.WyMu.Unlock()
}

// wyActiveActivity is the live type-186 instance.
func (p *Player) wyActiveActivity() (int32, bool) {
	now := time.Now()
	for _, e := range p.ActList.byType(wyActivityType) {
		if e.status(now) == "active" {
			return e.ActivityID, true
		}
	}
	return 0, false
}

// wyWindow reports whether now (ICT [[tytd-times-utc7]]) is inside the
// daily explore window and when it next opens.
func (p *Player) wyWindow() (bool, time.Time) {
	p.WyMu.Lock()
	o, c := p.wyOpenSec, p.wyCloseSec
	p.WyMu.Unlock()
	if o == 0 || c == 0 {
		o, c = 10*3600, 22*3600 // live-folded default (EXPLORE_DURATION "10|22")
	}
	vn := time.Now().In(ictZone)
	mid := time.Date(vn.Year(), vn.Month(), vn.Day(), 0, 0, 0, 0, ictZone)
	secs := int(vn.Sub(mid).Seconds())
	open := mid.Add(time.Duration(o) * time.Second)
	if secs >= o && secs <= c {
		return true, open.AddDate(0, 0, 1)
	}
	if !open.After(vn) {
		open = open.AddDate(0, 0, 1)
	}
	return false, open
}

func (p *Player) wySleepCtx(d time.Duration) bool {
	select {
	case <-p.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// ─── shop (generic 21003/21005, rbtMallAuto gate) ────────────────────────────

// wyShopSweep buys every free/ad/jade shop row to its daily cap — money
// rows are structurally excluded by rbtMallAuto [[tytd-item-ban]].
func (p *Player) wyShopSweep(actID int32) {
	p.WyMu.Lock()
	empty := len(p.wyMallRows) == 0
	p.WyMu.Unlock()
	if empty {
		body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID})
		if err == nil {
			if _, err := p.wyRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err != nil {
				log.Printf("[%s] [TCVU] shop: không lấy được cấu hình cửa hàng (%v) — thử lại chu kỳ sau", p.Username, err)
				return
			}
		}
	}
	today := time.Now().In(ictZone).Format("2006-01-02")
	p.WyMu.Lock()
	rows := append([]*proto.ActivityMallTempMsg{}, p.wyMallRows...)
	bought := p.wyMallBought
	if p.wyMallDay != today || p.wyMallAdds == nil {
		p.wyMallDay = today
		p.wyMallAdds = map[int32]int64{}
	}
	adds := p.wyMallAdds
	p.WyMu.Unlock()
	if bought == nil {
		bought = map[int32]int64{}
	}
	for _, mt := range rows {
		limit, ad, jade := rbtMallAuto(mt)
		if limit == 0 {
			continue // money pack — never sent
		}
		used := bought[mt.GetId()] + adds[mt.GetId()]
		for used < limit {
			rsp, err := p.rbtBuyGoods(actID, mt.GetId())
			if err != nil || rsp.GetRet() != 0 {
				log.Printf("[%s] [TCVU] shop: không mua được %s (%d/%d): err=%v ret=%d — để lại chu kỳ sau",
					p.Username, mt.GetName(), used+1, limit, err, rsp.GetRet())
				break
			}
			p.WyMu.Lock()
			p.wyMallAdds[mt.GetId()]++
			p.WyMu.Unlock()
			used++
			kind := "quà miễn phí"
			if ad {
				kind = "gói xem quảng cáo"
			} else if jade {
				kind = "gói tiên ngọc"
			}
			p.sendNotify(fmt.Sprintf("🌄 Tiên Cảnh Vị Ương: đã mua %s (%s) %d/%d — quà: %s",
				kind, mt.GetName(), used, limit, rsp.GetRewards()))
		}
	}
}

// ─── union tree ("tự tưới cây") + guild gifts ────────────────────────────────

// wyBonusItemAll — the client's "Nhận nhanh": 218622 with id=0 CLAIMS EVERY
// ready notice in one press. The id field must be PRESENT on the wire —
// proto3 marshal of Id:0 drops it (the rbt 217222 lesson, [[tytd-reborntrial-172]]
// CAM-LỘ law), so the bytes "10 00" are appended by hand. Server refuses =
// caller falls back to the per-notice loop.
func (p *Player) wyBonusItemAll(actID int32) (*proto.WeYoundGetBonusItemResp, error) {
	body, err := pb.Marshal(&proto.WeYoundGetBonusItemReq{ActivityId: pb.Int32(actID)})
	if err != nil {
		return nil, err
	}
	body = append(body, 0x10, 0x00) // f2 id=0 present
	data, err := p.wyRoundTrip(WE_YOUND_GET_BONUS_ITEM_CMD, SERVER_WE_YOUND_GET_BONUS_ITEM, body)
	if err != nil {
		return nil, err
	}
	var rsp proto.WeYoundGetBonusItemResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, err
	}
	return &rsp, nil
}

// wyWaterTree donates EVERY tree-water bottle for all config-folded donate
// ids (owner "tự tưới cây" 2026-10-17), num=1 per press like the client's
// UseItemView, stopping at the first refusal (bag empty / cap). Empty
// wyLiquidIDs (no config yet) = the opcode is NEVER sent. Then harvests
// ripe fruit and collects ready water notices.
func (p *Player) wyWaterTree(actID int32) {
	p.WyMu.Lock()
	ids := append([]int32(nil), p.wyLiquidIDs...)
	p.WyMu.Unlock()
	total := 0
	for _, id := range ids {
		n := 0
		for ; n < wyLiquidGrantCap; n++ {
			ret, err := p.wyGrantWater(actID, id)
			if err != nil || ret != 0 {
				break
			}
		}
		if n > 0 {
			log.Printf("[%s] [TCVU] cây bang: phát %d chai nước id %d", p.Username, n, id)
			total += n
		}
	}
	if total > 0 {
		p.sendNotify(fmt.Sprintf("🌄 Tiên Cảnh Vị Ương: đã tưới cây bang %d chai (đủ %d loại)", total, len(ids)))
	}
	// "Nhận nhanh" — owner 2026-11-13: "ở chỗ nhận linh vũ có nút nhận nhanh.
	// định kì click vào đấy là được". Clicked BLIND every cycle, before any
	// board read (the board is display-only; the server is the sole arbiter —
	// ret≠0 just means nothing was ready). A ret=0 carrying a reward string
	// is the payout; log it loud so the periodic press is visible.
	claimAllOK := false
	if wi, err := p.wyBonusItemAll(actID); err == nil && wi.GetRet() == 0 {
		claimAllOK = true
		if rw := strings.TrimSpace(wi.GetReward()); rw != "" {
			log.Printf("[%s] [TCVU] cây bang: nhận nhanh — %s", p.Username, rw)
		}
	}
	info, err := p.wyBonusInfo(actID)
	if err != nil || info.GetRet() != 0 {
		return
	}
	if info.GetCanReceive() == 1 {
		if fr, err := p.wyBonusResult(actID); err == nil && fr.GetRet() == 0 {
			p.sendNotify(fmt.Sprintf("🌄 Tiên Cảnh Vị Ương: cây chín — thu quả: %s", strings.TrimSpace(fr.GetReward())))
		}
	}
	// Fallback only when the blind claim-all was REFUSED yet the board still
	// lists ready notices (server would not take id=0): one press per notice.
	if !claimAllOK {
		ready := 0
		for _, w := range info.GetInfo() {
			if w.GetStatus() != 0 || w.GetId() == 0 {
				continue
			}
			if wi, err := p.wyBonusItem(actID, w.GetId()); err == nil && wi.GetRet() == 0 {
				ready++
			}
		}
		if ready > 0 {
			log.Printf("[%s] [TCVU] cây bang: nhận %d suất nước (lẻ từng suất)", p.Username, ready)
		}
	}
}

// wyGiftCycle receives every openable guild gift notice.
func (p *Player) wyGiftCycle(actID int32) {
	l, err := p.wyUnionGiftList(actID)
	if err != nil || l.GetRet() != 0 {
		return
	}
	got := 0
	for _, g := range l.GetBlessingList() {
		if g.GetStatus() != 0 || g.GetRecordId() == 0 {
			continue
		}
		if r, err := p.wyReceiveGift(actID, g.GetRecordId()); err == nil && r.GetRet() == 0 {
			got++
		}
	}
	if got > 0 {
		log.Printf("[%s] [TCVU] quà bang: nhận %d hộp quà", p.Username, got)
	}
}

// ─── milestone ladders (nạp bang — owner law 2026-11-13) ─────────────────────
//
// "thêm auto nhận thưởng nạp bang" — every event ships its recharge-milestone
// ladder in the 21003 conditionConfig table; the claim doors are the SHARED
// generic opcodes (same as Luân Hồi [[tytd-reborntrial-172]]), not 186-specific:
//   - UNION-RECHARGE rows (task type 84 or conditionId ≥ 20000) → per-id
//     21045 {activityId, conditionId} (client UnionActivitiesItem shape).
//   - every other row → ONE BLIND 21033 per distinct type — the server settles
//     only genuinely-ready rungs and ret≠0 means "nothing ready" (21003's
//     stored condition VALUES are stale zeros for these rows — live-proven on
//     rbt 2026-10-09; never gate locally on them).
//
// Claiming is idempotent, so the sweep fires blind on the tree cadence with
// zero local bookkeeping; the server is the sole arbiter.

// wyClaimType settles every ready milestone of one task TYPE via 21033.
func (p *Player) wyClaimType(actID, typ int32) (string, error) {
	body, err := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(typ)})
	if err != nil {
		return "", err
	}
	data, err := p.wyRoundTrip(ACTIVITY_TOKEN_REWARD_CMD, SERVER_ACTIVITY_TOKEN_REWARD, body)
	if err != nil {
		return "", err
	}
	var rsp proto.RspGetActivityConditionReward
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return "", err
	}
	if rsp.GetRet() != 0 {
		return "", fmt.Errorf("ret %d", rsp.GetRet())
	}
	return rsp.GetRewards(), nil
}

// wyClaimCross receives one cross-server union-recharge milestone via 21045.
func (p *Player) wyClaimCross(actID, condID int32) (string, error) {
	body, err := pb.Marshal(&proto.ReceiveCrossUnionRechargeRewardReq{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(condID)})
	if err != nil {
		return "", err
	}
	data, err := p.wyRoundTrip(ACTIVITY_CROSS_RECHARGE_CMD, SERVER_ACTIVITY_CROSS_RECHARGE, body)
	if err != nil {
		return "", err
	}
	var rsp proto.RspGetActivityConditionReward
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return "", err
	}
	if rsp.GetRet() != 0 {
		return "", fmt.Errorf("ret %d", rsp.GetRet())
	}
	return rsp.GetRewards(), nil
}

// wyCondSweep walks the folded ladder rows: per-id 21045 for the nạp-bang
// (type 84 / 2xxxx) rungs, one blind 21033 per other task type. ret≠0 ⇒ quiet.
func (p *Player) wyCondSweep(actID int32) {
	p.WyMu.Lock()
	cfgs := append([]*proto.ActivityConditionConfig{}, p.wyCondCfgs...)
	p.WyMu.Unlock()
	if len(cfgs) == 0 {
		// Blind pull — never skip the sweep because the fold hasn't happened;
		// one 21003 ask arms every later pass (answer merges via the reader's
		// wyMergeConfig as well as this direct call).
		if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
			if data, err := p.wyRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err == nil {
				p.wyMergeConfig(data)
			}
		}
		p.WyMu.Lock()
		cfgs = append([]*proto.ActivityConditionConfig{}, p.wyCondCfgs...)
		p.WyMu.Unlock()
		if len(cfgs) == 0 {
			return
		}
	}
	seenType := map[int32]bool{}
	seenCross := map[int32]bool{}
	for _, cfg := range cfgs {
		cid := cfg.GetConditionId()
		act := cfg.GetActivityId()
		if act == 0 {
			act = actID
		}
		if cfg.GetType() == 84 || cid >= 20000 {
			if seenCross[cid] {
				continue
			}
			seenCross[cid] = true
			rewards, err := p.wyClaimCross(act, cid)
			if err != nil || rewards == "" {
				continue
			}
			name := cfg.GetDesc()
			if name == "" {
				name = cfg.GetTitle()
			}
			if len(cfg.GetParam()) > 0 {
				name = strings.ReplaceAll(name, "{0}", cfg.GetParam()[0])
			}
			p.sendNotify(fmt.Sprintf("🌄 Tiên Cảnh Vị Ương: nhận quà mốc nạp bang %s — %s", name, rewards))
			continue
		}
		if seenType[cfg.GetType()] {
			continue
		}
		seenType[cfg.GetType()] = true
		rewards, err := p.wyClaimType(act, cfg.GetType())
		if err != nil || rewards == "" {
			continue // ret≠0 / seat busy: nothing ready in this group — quiet
		}
		p.sendNotify(fmt.Sprintf("🌄 Tiên Cảnh Vị Ương: đã nhận quà mốc nhóm %d — %s", cfg.GetType(), rewards))
	}
}

// ─── assist poll (hỗ trợ — owner law 2026-11-13) ────────────────────────────
//
// "khi đánh quái không chết, hệ thống sẽ tự gọi hỗ trợ. thêm poll check có ai
// cần hỗ trợ thì hỗ trợ": every poll reads the assist board (218608) and
// frees EVERY trapped union member in one batch ASSIST (218607, client
// parity). Monsters on the board need no packet here — helping them means
// swinging at them, which wySweepMonsters already does for every live
// monster cell on the march. Blind fire: assistNum quota / "nobody trapped"
// are the server's call, ret≠0 = quiet, no local ledger.

func (p *Player) wyAssistCycle(actID int32) {
	lg, err := p.wyAssistLog(actID)
	if err != nil || lg.GetRet() != 0 {
		return
	}
	var ids []int64
	var who []string
	for _, m := range lg.GetTrapInfo() {
		ap := m.GetAppearance()
		pid := ap.GetPlayerId()
		if pid == 0 || pid == int64(p.UID) {
			continue
		}
		ids = append(ids, pid)
		nm := ap.GetNickName()
		if nm == "" {
			nm = strconv.FormatInt(pid, 10)
		}
		who = append(who, nm)
	}
	if len(ids) == 0 {
		return
	}
	rsp, err := p.wyAssist(actID, ids)
	if err != nil {
		return
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [TCVU] hỗ trợ cầu viện bị từ chối (code %d)", p.Username, rsp.GetRet())
		return
	}
	log.Printf("[%s] [TCVU] hỗ trợ %d đồng đội [%s] — thưởng %s, còn %d lượt hỗ trợ",
		p.Username, len(ids), strings.Join(who, ", "), rsp.GetRewards(), rsp.GetAssistNum())
}

// ─── gameplay tick ───────────────────────────────────────────────────────────

// wyTick drives one in-window wake. Returns false only for connection loss;
// refusals are logged and leave the loop on its normal pacing.
func (p *Player) wyTick(actID int32) bool {
	ea, err := p.wyEnterActivity(actID)
	if err != nil || ea.GetRet() != 0 {
		return true
	}
	if ea.GetUnionId() == 0 {
		key := fmt.Sprintf("%d:%d", actID, ea.GetRound())
		if p.wyQualNotified != key {
			p.wyQualNotified = key
			p.sendNotify("🌄 Tiên Cảnh Vị Ương: tài khoản chưa có bang (chưa đủ điều kiện vào cảnh) — bot chờ, không làm gì tốn kém.")
		}
		return true
	}
	enter, err := p.wyEnter(actID, false)
	if err != nil {
		return true
	}
	if enter.GetRet() != 0 {
		log.Printf("[%s] [TCVU] ENTER ret %d — chờ nhịp sau", p.Username, enter.GetRet())
		return true
	}
	scene := enter.GetScene()
	if scene == nil || scene.GetPlayer() == nil {
		return true
	}
	pd := scene.GetPlayer()

	// Worship: free, once per round (server refuses repeats anyway).
	if !ea.GetHasWorship() {
		wkey := fmt.Sprintf("%d:%d", actID, ea.GetRound())
		if p.wyWorshipped == nil {
			p.wyWorshipped = map[string]bool{}
		}
		if !p.wyWorshipped[wkey] {
			p.wyWorshipped[wkey] = true
			if w, err := p.wyWorship(actID); err == nil {
				if w.GetRet() == 0 {
					p.sendNotify(fmt.Sprintf("🌄 Tiên Cảnh Vị Ương: đã bái — thưởng: %s", strings.TrimSpace(w.GetReward())))
				} else {
					log.Printf("[%s] [TCVU] bái ret %d", p.Username, w.GetRet())
				}
			}
		}
	}

	// Floor box (panel claim shape): once per (actID,floor).
	floor := scene.GetFloor()
	if floor > 0 && !floorClaimed(pd, floor) {
		key := fmt.Sprintf("%d:f%d", actID, floor)
		if p.wyWorshipped == nil {
			p.wyWorshipped = map[string]bool{}
		}
		if !p.wyWorshipped[key] {
			p.wyWorshipped[key] = true
			if fr, err := p.wyRewardFloor(actID); err == nil {
				if fr.GetRet() == 0 && strings.TrimSpace(fr.GetRewards()) != "" {
					p.sendNotify(fmt.Sprintf("🌄 Tiên Cảnh Vị Ương: mở rương tầng %d — %s", floor, strings.TrimSpace(fr.GetRewards())))
				}
			}
		}
	}
	if floor != p.wyFloor {
		p.wyFloor = floor
		log.Printf("[%s] [TCVU] đang ở tầng %d (thể lực %d)", p.Username, floor, pd.GetHp())
	}

	// Claim every killed monster's reward (free receive).
	for _, m := range scene.GetMonster() {
		if m.GetId() == 0 {
			continue
		}
		if parseBlood(m.GetRemainBloods()) <= 0 && !m.GetExploreReward() {
			if r, err := p.wyGetMonsterReward(actID, m.GetId()); err == nil && r.GetRet() == 0 {
				if s := strings.TrimSpace(r.GetRewards()); s != "" {
					log.Printf("[%s] [TCVU] thu quái %d: %s", p.Username, m.GetId(), s)
				}
			}
		}
	}

	// ── the board: CDN base map ⊎ server view (client initTileData) ──
	// The wire view ONLY ever contains OPENED cells; Fog/Event/Obstacle are
	// client constructs painted from the static CDN json (see
	// player_weyound_map.go). Every march/puzzle decision below walks THAT
	// grid — getNearCanExplorePos, A*, the light pick — never the raw view.
	st := &wyState{
		pd: pd, mapID: scene.GetMapId(), floor: floor,
		memberSize: enter.GetMemberSize(),
	}
	if pp := pd.GetPos(); pp != nil {
		st.px, st.py = pp.GetX(), pp.GetY()
	} else {
		return true
	}
	st.ingest(scene.GetView())
	st.ingestMonsters(scene.GetMonster())
	if !p.wySyncBoard(st) {
		return true // unknown mapId / bad json — logged; never march on a guess
	}
	if p.wyGridDiagOnce(st) {
		log.Printf("[%s] [TCVU] %s", p.Username, wyGridDiag(st.grid))
	}

	p.wyResolvePending(actID, st)
	return p.wyFogMarch(actID, st)
}

// ─── loop ────────────────────────────────────────────────────────────────────

// tienCanhAutoLoop is the RebornTrial-loop counterpart for WeYound: the DB
// gate is re-read every wake (so /auto_tiencanh false stops it between
// ticks), the daily window is computed locally from the folded config but
// the server's refuse codes always overrule our calendar.
func (p *Player) tienCanhAutoLoop() {
	if !p.wySleepCtx(12 * time.Second) {
		return
	}
	lastState := ""
	var stateSince time.Time
	record := func(s string) {
		if s == lastState {
			return
		}
		lastState = s
		stateSince = time.Now()
		log.Printf("[%s] [TCVU] auto: %s", p.Username, s)
	}
	var nextStatus time.Time
	statusNow := ""
	var nextTree time.Time   // zero → first wake waters the tree immediately
	var nextAssist time.Time // zero → first wake polls the assist board
	beat := func() {
		if nextStatus.IsZero() {
			nextStatus = time.Now().Add(wyStatusGap)
			return
		}
		if time.Now().Before(nextStatus) {
			return
		}
		p.sendNotify(fmt.Sprintf("🌄 Tiên Cảnh Vị Ương: %s — trạng thái này %s",
			statusNow, stFmtDur(time.Since(stateSince))))
		nextStatus = time.Now().Add(wyStatusGap)
	}
	for {
		if !p.AutoTienCanh {
			record("tắt — bật bằng /auto_tiencanh true")
			nextStatus, statusNow = time.Time{}, ""
			if !p.wySleepCtx(time.Minute) {
				return
			}
			continue
		}
		actID, ok := p.wyActiveActivity()
		if !ok {
			record("chưa thấy Tiên Cảnh Vị Ương trong danh sách sự kiện")
			nextStatus, statusNow = time.Time{}, ""
			if !p.wySleepCtx(10 * time.Minute) {
				return
			}
			continue
		}
		// Tree + shop + guild gifts run on their own cadence (all day —
		// watering and free receives are outside the explore window).
		if time.Now().After(nextTree) {
			p.wyWaterTree(actID)
			p.wyShopSweep(actID)
			p.wyGiftCycle(actID)
			p.wyCondSweep(actID)
			nextTree = time.Now().Add(wyTreeGap)
		}
		// Assist board runs on its own faster clock — a trapped mate is
		// waiting RIGHT NOW, and the rescue is free (owner poll law).
		if time.Now().After(nextAssist) {
			p.wyAssistCycle(actID)
			nextAssist = time.Now().Add(wyAssistGap)
		}
		inWindow, nextOpen := p.wyWindow()
		if !inWindow {
			record(fmt.Sprintf("ngoài giờ khám phá (mở lại %s)", nextOpen.In(ictZone).Format("15:04")))
			nextStatus, statusNow = time.Time{}, ""
			d := time.Until(nextOpen)
			if d > time.Hour {
				d = time.Hour
			}
			if !p.wySleepCtx(d) {
				return
			}
			continue
		}
		statusNow = "đang cuốc bản đồ (tự do, không cắn thể lực)"
		record(statusNow)
		if !p.wyTick(actID) {
			return
		}
		beat()
		if !p.wySleepCtx(wyPoll) {
			return
		}
	}
}
