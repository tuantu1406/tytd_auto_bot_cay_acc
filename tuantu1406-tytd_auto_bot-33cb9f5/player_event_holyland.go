package tytd_go

// Holy Land (Cửu U / 九幽争霸) — union-vs-union city capture on event
// accounts. The state machine mirrors the God Island integration but is
// simpler: the protocol carries NO activityId anywhere, so each account has
// exactly one Holy Land context (no per-event slots, no FIFO response
// attribution — a response of a given type can only belong to our one state).
//
// Like God Island, nothing combat-related happens on its own: moves, attacks
// and 托管 auto-attacks fire only from leader commands (/cuuu_*). The
// background loop only polls state, keeps the residency heartbeat, runs the
// two pure perks (worship bái tế + union rank-reward claim) and — the one
// authorized auto-action — free rebirth once the death cooldown elapsed and a
// free count is left. Item rebirth is manual-only.
//
// Protocol reference: ../remoteHolyLand/HOLY_LAND_PROTOCOL.md; message types
// live in internal/proto/tytd.proto (HolyLand* block).

import (
	"fmt"
	"log"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// ActivityMainConfig.type of Holy Land in the activity list (client reads the
// same type to draw the event icon). It is only a second trigger — the
// schedule poll (209570) is authoritative, and since 2026-10-06 even IT is
// only half the truth: see hlRunningNow.
const hlActivityTypeHolyLand = 346

// Holy Land battle window inside each scheduled day, from the game's own
// GameConfigDB (decoded copy ../remoteAll/GameConfigDB.asset.json — same bake
// source as union_bounty_static.go): HOLYLAND_FIGHT_BEGIN_TIME=1200 min and
// HOLYLAND_FIGHT_DURATION=60 min → 20:00–21:00 on the anchored day. The
// scheduleTimestamp entries the server pushes ARE absolute day-anchor ms
// values, so the check only adds offsets — no local timezone math (the
// 20:00 ICT placement is the server's own anchoring, same as the client's
// getBattleBeginTimeMs/getBattleDurationTimeMs).
const (
	hlFightBeginMs    = 1200 * 60 * 1000
	hlFightDurationMs = 60 * 60 * 1000
)

const (
	hlInfoPollSeconds = 3           // game info cadence (state spine)
	hlHeavyTicks      = 20          // ~60 s: base info + heartbeat + perk cycle
	hlRebirthBackoff  = time.Minute // floor between rebirth attempts
	// hlMaxRebirthTries bounds the auto free-rebirth probe loop per death
	// (kunlun law: the SERVER is the budget judge — bounded probes, not a
	// locally-gated counter). 1 min backoff × 10 ≈ 10 min of probing.
	hlMaxRebirthTries = 10
)

// holyLandState is the entire Holy Land state of one event account. Every
// field is guarded by EventPlayer.HolyMu.
type holyLandState struct {
	// Base info (209501): bracket + perks readiness.
	Group      int32
	GameType   int32 // 1/2 weekly normal/senior, 3/4 monthly
	Union      int64 // our union on the field (0 = not reported yet)
	SelfRank   int32
	CanWorship bool
	// WorshipSent / RewardClaimSent are edge trackers so a perk fires once
	// per availability window instead of on every 60 s poll; both reset when
	// the server stops advertising the perk.
	WorshipSent bool

	// Schedule (209570/209575): authoritative open/qualification state.
	IsOpen bool
	// OpenKnown: 209570 answered at least once — schedule then outranks
	// the ActList 346 season row for DISPLAY gates (see Player.HlOpenKnown).
	OpenKnown  bool
	HasQuality bool
	IsApply    bool
	RoundId    int32
	// Day anchors (ms) of the scheduled battle days — 2562 field 6
	// (ScheduleApply) and 2567 field 2 (ScheduleTs). The client resolves
	// "is a round happening now" from these, preferring battleApplyData
	// (getScheduleTimestamp: apply ?? timestamps ?? [0,0,0,0,0]); we keep
	// the same precedence. isOpen alone does NOT mean running: it stays
	// true across the season (user bug 2026-10-06). Guarded by HolyMu.
	ScheduleApply []int64
	ScheduleTs    []int64

	// Game info spine (209521 + MYGAMEINFO/BEEN_KILL pushes).
	Location   int32 // current city (0 while not on the map)
	Dead       bool  // server sent a kill record for us
	LeftOut    bool  // our union eliminated
	Moving     bool
	RouteTo    int32
	SpeedUp    int32 // speed-up counter; /tangtoc echoes it as baseline
	AutoAtk    int32 // 托管 target city (0 = off)
	CurPower   int32
	AttackCd   int64
	FightValue string
	// RebirthTime is when free rebirth unlocks (epoch s or ms, normalized at
	// read). Only meaningful while Dead. Killer fields feed the notify.
	RebirthTime int64
	KillerName  string
	KilledByNPC bool
	// Rebirth counts from game info / back-home responses; 0 until first poll.
	// These mirror the SERVER's counters (kunlun's identical fields proved to
	// count USES, not remaining — semantics never trusted as a gate). The
	// FREE route is server-arbitrated (bounded probes, hlMaxRebirthTries);
	// ItemRebirth>0 stays as the courtesy guard on the manual ITEM route
	// (item ban — an item spend stays behind an explicit owner-approved
	// button/command, and here also behind a server-reported count).
	FreeRebirth int32
	ItemRebirth int32
	// rebirthTries bounds the auto probe loop for the CURRENT death (reset on
	// each BEEN_KILL, on back-home success, and when game info says alive).
	rebirthTries int
	// manualRebirth: the next back-home answer belongs to a leader command
	// (/cuuu_hoisinh, HUD ❤/🕊) — only then does a refusal reach the leader
	// as a notify; routine auto-probe rejections stay in the log.
	manualRebirth bool

	// Rank-reward perk (209508/209509).
	HasReward       bool
	UnionRank       int32
	RewardClaimSent bool

	// lastRebirthTry stamps the previous auto/manual-eligible rebirth send so
	// a refused or not-yet-processed attempt cannot spam the server.
	lastRebirthTry time.Time
}

// hlState returns the (lazily created) state slot. The returned pointer is
// stable; its FIELDS still require HolyMu — same convention as giSlot.
func (p *EventPlayer) hlState() *holyLandState {
	p.HolyMu.Lock()
	defer p.HolyMu.Unlock()
	if p.hl == nil {
		p.hl = &holyLandState{}
	}
	return p.hl
}

// hlSend marshals msg and puts the frame on the wire. There is no response
// queue: with one context per account, every response of a tracked type
// belongs to this state by construction. hlSendMu keeps frames written in
// call order like giSendMu does.
func (p *EventPlayer) hlSend(cmd string, msg pb.Message) {
	payload, err := pb.Marshal(msg)
	if err != nil {
		log.Printf("[%s] [HolyLand] marshal %s: %v", p.Username, cmd, err)
		return
	}
	p.hlSendMu.Lock()
	p.write(pack.Pack(cmd, payload, p.UID))
	p.hlSendMu.Unlock()
}

// hlRebirthLeft normalizes the rebirth deadline (seconds vs ms at 1e12 like
// giEvent.reviveLeft) and reports the remaining wait, if any.
func hlRebirthLeft(rebirthTime int64, now time.Time) (time.Duration, bool) {
	if rebirthTime <= 0 {
		return 0, false
	}
	ms := rebirthTime
	if ms < 1_000_000_000_000 {
		ms *= 1000
	}
	if left := time.Until(time.UnixMilli(ms)); left > 0 {
		return left, true
	}
	return 0, false
}

// hlShouldAutoRebirth is the pure auto-rebirth gate: resident (not
// eliminated), the server's own kill record says we are dead, its cooldown
// (if known) has elapsed, the bounded probe budget for this death remains,
// and the backoff window passed. The free counter deliberately does NOT gate
// this (customer bug 2026-10-24 "hồi sinh free không work"): the field rides
// 209521 optional #15 only, and kunlun's twin field proved to count USES —
// gating on it silently blocked every revive when the server omitted it.
// Kunlun's live-proven law: bounded probes, the SERVER is the budget judge.
// An unknown cooldown (RebirthTime==0) counts as unlocked: Dead only ever
// becomes true from server-declared death (RebirthData / BEEN_KILL), so the
// probe cannot resurrect a living army — a wrong guess just draws ret≠0.
// Item rebirth is never automatic. Extracted for table tests.
func hlShouldAutoRebirth(st *holyLandState, now time.Time) bool {
	if st.LeftOut || !st.Dead {
		return false
	}
	if st.rebirthTries >= hlMaxRebirthTries {
		return false
	}
	if _, waiting := hlRebirthLeft(st.RebirthTime, now); waiting {
		return false
	}
	return now.Sub(st.lastRebirthTry) >= hlRebirthBackoff
}

// hlTryAutoRebirth re-checks the gate under the lock and fires the free
// rebirth (209532 useItem=false). Called from the been-kill push and every
// stay tick; the stamp makes repeats harmless.
func (p *EventPlayer) hlTryAutoRebirth(now time.Time) {
	st := p.hlState()
	p.HolyMu.Lock()
	fire := hlShouldAutoRebirth(st, now)
	if fire {
		st.lastRebirthTry = now
		st.rebirthTries++
	}
	try := st.rebirthTries
	p.HolyMu.Unlock()
	if !fire {
		return
	}
	log.Printf("[%s] [HolyLand] auto free rebirth (lần %d/%d)", p.Username, try, hlMaxRebirthTries)
	p.hlSend(HOLY_LAND_BACK_HOME_CMD, &proto.HolyLandGameBackHomeReq{UseItem: pb.Bool(false)})
}

// holyLandStay is the account's only background Holy Land work. The schedule
// (209570, empty request) is polled on EVERY tick — it is authoritative for
// open/qualification, so we never depend solely on the activity-list type.
// While the event is open (schedule isOpen or the activity announcement) the
// game info spine (209521) is polled every tick; every ~60 s also base info +
// heartbeat (client cadence 15 s; 60 s keeps residency while staying light)
// and the perk cycle. Auto free rebirth is checked every tick. No attacking,
// no moving, no target picking — those are leader commands.
func (p *EventPlayer) holyLandStay() {
	tick := 0
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(hlInfoPollSeconds * time.Second):
		}

		p.hlSend(HOLY_LAND_SCHEDULE_CMD, &proto.HolyLandBattleApplyDataSyncReq{})

		st := p.hlState()
		p.HolyMu.Lock()
		open := p.hlOpen || st.IsOpen
		p.HolyMu.Unlock()

		if open {
			p.hlSend(HOLY_LAND_GAME_INFO_CMD, &proto.HolyLandGameInfoReqMsg{})

			if tick%hlHeavyTicks == 0 {
				p.hlSend(HOLY_LAND_BASE_INFO_CMD, &proto.HolyLandBaseInfoReqMsg{})
				p.hlSend(HOLY_LAND_HEART_BEAT_CMD, &proto.HolyLandHeartBeatReqMsg{})
				// Perks (non-combat upkeep, like the God Island plant cycle):
				// worship when offered, rank-reward claim when owed.
				p.HolyMu.Lock()
				worship := st.CanWorship && !st.WorshipSent
				if worship {
					st.WorshipSent = true
				}
				p.HolyMu.Unlock()
				if worship {
					log.Printf("[%s] [HolyLand] worship (bái tế)", p.Username)
					p.hlSend(HOLY_LAND_WORSHIP_CMD, &proto.HolyLandWorshipReq{})
				}
				p.hlSend(HOLY_LAND_RANK_REWARD_CMD, &proto.HolyLandPlayerUnionRankRewardReqMsg{})
			}
		}

		p.hlTryAutoRebirth(time.Now())
		tick++
	}
}

// ---------------------------------------------------------------------------
// Response / push handlers (eventWsHandler reader goroutine).
// ---------------------------------------------------------------------------

// hlHandleActivityList flags the Holy Land announcement. Full lists (login
// push) may also withdraw it; incremental syncs only ever add.
func (p *EventPlayer) hlHandleActivityList(data []byte, full bool) {
	var push proto.PushActivityList
	if err := pb.Unmarshal(data, &push); err != nil {
		return // giHandleActivityList logs the malformed frame already
	}
	present := false
	for _, cfg := range push.GetMainConfig() {
		if cfg.GetType() == hlActivityTypeHolyLand {
			present = true
			break
		}
	}
	p.HolyMu.Lock()
	if full {
		p.hlOpen = present
	} else if present {
		p.hlOpen = true
	}
	p.HolyMu.Unlock()
}

// hlHandleSchedule parses the 209570 answer — the open gate. ret != 0 means
// the server has no schedule for us (event closed / not eligible): IsOpen
// goes false, which also stops the game-info poll until it flips back.
func (p *EventPlayer) hlHandleSchedule(data []byte) {
	var resp proto.HolyLandBattleApplyDataSyncMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad schedule: %v", p.Username, err)
		return
	}
	stt := p.hlState()
	p.HolyMu.Lock()
	was := stt.IsOpen
	if resp.GetRet() == 0 {
		stt.IsOpen = resp.GetIsOpen()
		stt.HasQuality = resp.GetHasQuality()
		stt.IsApply = resp.GetIsApply()
		stt.RoundId = resp.GetRoundId()
		stt.ScheduleApply = resp.GetScheduleTimestamp()
	} else {
		// No schedule for us at all: open AND the day anchors go stale
		// together — keeping anchors here would let a closed account
		// re-open the display the moment the season clock hits 20:00.
		stt.IsOpen = false
		stt.ScheduleApply = nil
		stt.ScheduleTs = nil
	}
	stt.OpenKnown = true
	isOpen := stt.IsOpen
	p.HolyMu.Unlock()
	if was != isOpen {
		log.Printf("[%s] [HolyLand] event open=%v (round %d, apply=%v, quality=%v)",
			p.Username, isOpen, stt.RoundId, stt.IsApply, stt.HasQuality)
	}
}

// hlHandleSchedulePush caches the 209575 timestamp refresh.
// hlHandleSchedulePush consumes the 209575 timestamp refresh. The server
// sends one after EVERY 209570 poll our 3 s stay loop does, so this is
// routine echo traffic — logged only when the payload is unreadable.
func (p *EventPlayer) hlHandleSchedulePush(data []byte) {
	var push proto.HolyLandBattleTimeStampsDataSyncMsg
	if err := pb.Unmarshal(data, &push); err != nil {
		log.Printf("[%s] [HolyLand] bad schedule push: %v", p.Username, err)
		return
	}
	// The day anchors also ride this push (2567 field 2). Mirror the
	// client's precedence (battleApplyData ?? battleTimeStampsData):
	// the 2562 answer wins whenever it has supplied them.
	st := p.hlState()
	p.HolyMu.Lock()
	if push.GetRet() == 0 && len(st.ScheduleApply) == 0 && len(push.GetScheduleTimestamp()) > 0 {
		st.ScheduleTs = push.GetScheduleTimestamp()
	}
	p.HolyMu.Unlock()
}

// hlHandleBaseInfo caches bracket identity + worship availability. The
// WorshipSent edge resets whenever the server says canWorship=false, so the
// next true wins a fresh send.
func (p *EventPlayer) hlHandleBaseInfo(data []byte) {
	var resp proto.HolyLandBaseInfoRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad base info: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	st.SelfRank = resp.GetSelfRank()
	st.Group = resp.GetGroup()
	st.GameType = resp.GetGameType()
	if resp.GetUnionId() != 0 {
		st.Union = resp.GetUnionId()
	}
	st.CanWorship = resp.GetCanWorship()
	if !st.CanWorship {
		st.WorshipSent = false
	}
	p.HolyMu.Unlock()
}

// hlHandleGameInfo is the state spine (209521). ret != 0 (NOT RESIDENT in
// the scene, kicked, …) only skips the state merge — it must NOT touch the
// open flag: open/closed belongs to the schedule poll alone. A registered
// account that never entered the map answers 209521 with a ret error on
// EVERY tick; closing IsOpen here made the gate flip-flop every 3 s and the
// Cửu U rows/card blink away while the event was really open (live log
// 2026-10-05 07:26+, "event open=true" re-logging every poll). RebirthData
// presence — not location==0 — is what marks us dead: an account resident
// on the field but never deployed also reads location 0.
func (p *EventPlayer) hlHandleGameInfo(data []byte) {
	var resp proto.HolyLandGameInfoRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad game info: %v", p.Username, err)
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	if resp.GetRet() != 0 {
		p.HolyMu.Unlock()
		return
	}
	st.Location = resp.GetLocation()
	st.LeftOut = resp.GetLeftOut()
	st.SpeedUp = resp.GetSpeedUpCount()
	st.CurPower = resp.GetCurPower()
	st.AttackCd = resp.GetAttackCd()
	st.FreeRebirth = resp.GetFreeRebirthCount()
	st.ItemRebirth = resp.GetItemRebirthCount()
	st.AutoAtk = resp.GetAutoAtkCityId()
	st.FightValue = resp.GetFightValue()
	// myRouteInfo is a presence signal (same convention as giEvent.Moving).
	st.Moving = resp.MyRouteInfo != nil
	if r := resp.MyRouteInfo; r != nil {
		st.RouteTo = r.GetToCityId()
	} else {
		st.RouteTo = 0
	}
	if r := resp.RebirthData; r != nil {
		st.Dead = true
		if r.RebirthTime != nil {
			st.RebirthTime = r.GetRebirthTime()
		} else if st.RebirthTime == 0 {
			// Death declared without a cooldown — stamp now so the auto loop
			// treats it as a real death and probes (kunlun's 219933 pos==0
			// pattern); the server stays the eligibility judge.
			st.RebirthTime = time.Now().UnixMilli()
		}
		st.KilledByNPC = r.GetKillByNPC()
		if n := r.GetTargetName(); n != "" {
			st.KillerName = n
		}
	} else {
		st.Dead = false
		st.RebirthTime = 0
		st.KillerName = ""
		st.rebirthTries = 0 // back on the board — probe budget re-armed
	}
	p.HolyMu.Unlock()
}

// hlHandleAccelerate stores the server-echoed incremented counter so back to
// back /tangtoc calls carry the right baseline without waiting for a poll.
func (p *EventPlayer) hlHandleAccelerate(data []byte) {
	var resp proto.HolyLandGameAcclerateMoveRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad accelerate resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 || resp.SpeedUpCount == nil {
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	st.SpeedUp = resp.GetSpeedUpCount()
	p.HolyMu.Unlock()
}

// hlHandleAutoAtk caches the echoed 托管 target (0 = off) and reports refusals.
func (p *EventPlayer) hlHandleAutoAtk(data []byte) {
	var resp proto.HolyLandGameAutoAttackRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad auto-attack resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [HolyLand] auto-attack refused by server: ret=%d", p.Username, resp.GetRet())
		p.sendNotify(fmt.Sprintf("⚠️ [Cửu U] treo máy tấn công bị từ chối (lỗi %d)", resp.GetRet()))
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	st.AutoAtk = resp.GetTargetId()
	p.HolyMu.Unlock()
}

// hlHandleBackHome reconciles a rebirth (free or item): success clears the
// dead flag optimistically (the next game-info poll confirms) and refreshes
// both counters; refusal goes to the leader in game language.
func (p *EventPlayer) hlHandleBackHome(data []byte) {
	var resp proto.HolyLandGameBackHomeRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad back-home resp: %v", p.Username, err)
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	manual := st.manualRebirth
	st.manualRebirth = false
	if resp.GetRet() != 0 {
		p.HolyMu.Unlock()
		// A refusal of an AUTO probe is routine (the free budget is a server
		// config the bot never receives — probes draw rejections by design,
		// kunlun same); only a command's refusal notifies the leader.
		log.Printf("[%s] [HolyLand] rebirth refused: ret=%d", p.Username, resp.GetRet())
		if manual {
			p.sendNotify(fmt.Sprintf("⚠️ [Cửu U] hồi sinh thất bại (lỗi %d)", resp.GetRet()))
		}
		return
	}
	st.Dead = false
	st.RebirthTime = 0
	st.rebirthTries = 0
	if resp.FreeRebirthCount != nil {
		st.FreeRebirth = resp.GetFreeRebirthCount()
	}
	if resp.ItemRebirthCount != nil {
		st.ItemRebirth = resp.GetItemRebirthCount()
	}
	p.HolyMu.Unlock()
	p.sendNotify("🔄 [Cửu U] quân đã hồi sinh")
}

// hlHandleMoveRet / hlHandleAttackRet cover the two fire-and-forget orders:
// accepted needs nothing (game info confirms), refused must not pass silently.
func (p *EventPlayer) hlHandleMoveRet(data []byte) {
	var resp proto.HolyLandGameMoveRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad move resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [HolyLand] move refused: ret=%d", p.Username, resp.GetRet())
		p.sendNotify(fmt.Sprintf("⚠️ [Cửu U] hành quân bị từ chối (lỗi %d)", resp.GetRet()))
	}
}

func (p *EventPlayer) hlHandleAttackRet(data []byte) {
	var resp proto.HolyLandGameAtkRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad attack resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [HolyLand] attack refused: ret=%d", p.Username, resp.GetRet())
		p.sendNotify(fmt.Sprintf("⚠️ [Cửu U] tấn công bị từ chối (lỗi %d)", resp.GetRet()))
	}
}

// hlHandleWorship reports the bái tế perk result.
func (p *EventPlayer) hlHandleWorship(data []byte) {
	var resp proto.HolyLandWorshipRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad worship resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [HolyLand] worship refused: ret=%d", p.Username, resp.GetRet())
		return
	}
	log.Printf("[%s] [HolyLand] worship ok, reward=%q", p.Username, resp.GetReward())
	p.sendNotify("🙏 [Cửu U] bái tế thành công")
}

// hlHandleRankRewardState caches the rank-reward perk state and claims once
// per availability window (RewardClaimSent edge, resets with hasReward).
func (p *EventPlayer) hlHandleRankRewardState(data []byte) {
	var resp proto.HolyLandPlayerUnionRankRewardRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad rank-reward state: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	st.UnionRank = resp.GetUnionRank()
	st.HasReward = resp.GetHasReward()
	claim := st.HasReward && !st.RewardClaimSent
	if claim {
		st.RewardClaimSent = true
	}
	if !st.HasReward {
		st.RewardClaimSent = false
	}
	rank := st.UnionRank
	p.HolyMu.Unlock()
	if claim {
		log.Printf("[%s] [HolyLand] claiming union rank reward (rank %d)", p.Username, rank)
		p.hlSend(HOLY_LAND_CLAIM_REWARD_CMD, &proto.HolyLandGetPlayerUnionRankRewardReqMsg{})
	}
}

func (p *EventPlayer) hlHandleClaimReward(data []byte) {
	var resp proto.HolyLandGetPlayerUnionRankRewardRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [HolyLand] bad claim-reward resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [HolyLand] rank-reward claim refused: ret=%d", p.Username, resp.GetRet())
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	rank := st.UnionRank
	p.HolyMu.Unlock()
	log.Printf("[%s] [HolyLand] rank reward claimed, reward=%q", p.Username, resp.GetReward())
	p.sendNotify(fmt.Sprintf("🎁 [Cửu U] đã nhận thưởng xếp hạng bang (hạng %d)", rank))
}

// hlHandleMyGameInfoSync refreshes the live position/power (arrival, revive,
// power ticks) between game-info polls.
func (p *EventPlayer) hlHandleMyGameInfoSync(data []byte) {
	var sync proto.HolyLandGameMyGameInfoSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [HolyLand] bad mygameinfo sync: %v", p.Username, err)
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	if sync.Location != nil {
		st.Location = sync.GetLocation()
	}
	if sync.CurPower != nil {
		st.CurPower = sync.GetCurPower()
	}
	if sync.AttackCd != nil {
		st.AttackCd = sync.GetAttackCd()
	}
	if sync.FightValue != nil {
		st.FightValue = sync.GetFightValue()
	}
	if st.Location != 0 {
		st.Dead = false
		st.rebirthTries = 0 // back on the board — probe budget re-armed
	}
	p.HolyMu.Unlock()
}

// hlHandleBeenKillSync records our death with the exact free-rebirth
// unlock time and notifies the leader; the auto-rebirth check runs inline so
// an elapsed cooldown (instant unlock cases) reacts in milliseconds instead
// of waiting for the next tick.
func (p *EventPlayer) hlHandleBeenKillSync(data []byte) {
	var sync proto.HolyLandBeenKillSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [HolyLand] bad been-kill sync: %v", p.Username, err)
		return
	}
	who := "quái"
	if !sync.GetKillByNPC() {
		who = "đối thủ"
		if n := sync.GetTargetName(); n != "" {
			who = n
		}
	}
	st := p.hlState()
	p.HolyMu.Lock()
	st.Dead = true
	st.Moving = false
	st.RouteTo = 0
	if sync.RebirthTime != nil {
		st.RebirthTime = sync.GetRebirthTime()
	} else if st.RebirthTime == 0 {
		st.RebirthTime = time.Now().UnixMilli() // kill without a cooldown — see hlHandleGameInfo
	}
	st.rebirthTries = 0 // fresh death → fresh probe budget
	left, waiting := hlRebirthLeft(st.RebirthTime, time.Now())
	p.HolyMu.Unlock()

	msg := fmt.Sprintf("☠️ [Cửu U] quân bị %s hạ tại thành %d", who, sync.GetCityId())
	if waiting {
		msg += fmt.Sprintf(", hồi sinh sau %dm%02ds", int(left.Minutes()), int(left.Seconds())%60)
	} else {
		msg += " (tự hồi sinh)"
	}
	log.Printf("[%s] [HolyLand] been kill: city=%d byNPC=%v rebirthTime=%d",
		p.Username, sync.GetCityId(), sync.GetKillByNPC(), sync.GetRebirthTime())
	p.sendNotify(msg)
	p.hlTryAutoRebirth(time.Now())
}

// hlHandleOccupyCity notifies only our union's captures; other unions are
// log noise the leader does not need.
func (p *EventPlayer) hlHandleOccupyCity(data []byte) {
	var sync proto.HolyLandGameOccupyCitySyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [HolyLand] bad occupy-city sync: %v", p.Username, err)
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	ours := sync.GetUnionId() != 0 && sync.GetUnionId() == st.Union
	p.HolyMu.Unlock()
	if ours {
		p.sendNotify(fmt.Sprintf("🏯 [Cửu U] bang chiếm được thành %d", sync.GetCityId()))
	} else {
		log.Printf("[%s] [HolyLand] city %d taken by union %d", p.Username, sync.GetCityId(), sync.GetUnionId())
	}
}

// hlHandleLeftOut marks our elimination. Foreign unions are log-only.
func (p *EventPlayer) hlHandleLeftOut(data []byte) {
	var sync proto.HolyLandLeftOutSyncInfo
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [HolyLand] bad left-out sync: %v", p.Username, err)
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	ours := sync.GetUnionId() != 0 && sync.GetUnionId() == st.Union
	if ours {
		st.LeftOut = true
		st.Dead = false
		st.Moving = false
		st.RouteTo = 0
		st.Location = 0
	}
	p.HolyMu.Unlock()
	if ours {
		log.Printf("[%s] [HolyLand] union %d left out", p.Username, sync.GetUnionId())
		p.sendNotify("🚪 [Cửu U] bang đã bị loại khỏi Cửu U")
	}
}

// hlHandleAutoStop reports the server ending an ordered 托管.
func (p *EventPlayer) hlHandleAutoStop(data []byte) {
	var sync proto.HolyLandGameAutoBattleStopSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [HolyLand] bad auto-stop sync: %v", p.Username, err)
		return
	}
	st := p.hlState()
	p.HolyMu.Lock()
	st.AutoAtk = 0
	p.HolyMu.Unlock()
	log.Printf("[%s] [HolyLand] auto battle stopped by server", p.Username)
	p.sendNotify("⚔️ [Cửu U] treo máy tấn công đã dừng")
}

// ---------------------------------------------------------------------------
// Leader-command guards + public API (user-facing errors in game language).
// ---------------------------------------------------------------------------

// hlRunningNow answers the DISPLAY question "is a Cửu U round happening
// right now": the server must say open AND nowMs must lie inside the battle
// window (day + 20:00 → day + 21:00) of one of the server-anchored schedule
// days. isOpen alone cannot answer this: the client proves 2562 keeps it true
// across the season (getHolyLandState resolves the real stage from
// scheduleTimestamp + the GameConfigDB fight offsets), and the ActList 346 row
// is the season window too — both stayed "true/active" long after the round
// had ended (user bug 2026-10-06: "cửu u đang không chạy sao mày vẫn hiện").
// Without day anchors there is nothing honest to show, so the gate stays
// closed — mirroring the client's default [0,0,0,0,0] → NotOpen.
func hlRunningNow(open bool, apply, ts []int64, nowMs int64) bool {
	if !open {
		return false
	}
	sched := apply
	if len(sched) == 0 {
		sched = ts
	}
	for _, day := range sched {
		if day <= 0 {
			continue
		}
		begin := day + hlFightBeginMs
		if nowMs >= begin && nowMs < begin+hlFightDurationMs {
			return true
		}
	}
	return false
}

// hlNextBeginMs is the countdown twin of hlRunningNow: the EARLIEST battle
// start strictly in the future, but only when it lands within the next 24 h
// — a stale or far-future anchor must never render a countdown. 0 = nothing
// to show.
func hlNextBeginMs(apply, ts []int64, nowMs int64) int64 {
	sched := apply
	if len(sched) == 0 {
		sched = ts
	}
	const dayMs = 24 * 60 * 60 * 1000
	var best int64
	for _, day := range sched {
		if day <= 0 {
			continue
		}
		begin := day + hlFightBeginMs
		if begin > nowMs && begin-nowMs <= dayMs && (best == 0 || begin < best) {
			best = begin
		}
	}
	return best
}

// HolyLandEventOpen reports whether a Cửu U round is happening NOW for this
// account — /my_event_members and the web card/row use it to hide the segment
// outside the actual battle window, not just outside the season.
func (p *EventPlayer) HolyLandEventOpen() bool {
	p.HolyMu.Lock()
	defer p.HolyMu.Unlock()
	if p.hl == nil {
		return false
	}
	open := p.hl.IsOpen
	if !p.hl.OpenKnown {
		open = p.hlOpen || p.hl.IsOpen
	}
	return hlRunningNow(open, p.hl.ScheduleApply, p.hl.ScheduleTs, time.Now().UnixMilli())
}

// HolyLandEventUpcoming returns the next battle start (epoch ms) when Cửu U
// is SCHEDULED but not fighting yet — the events-tab countdown gate. 0 when
// a round is running (the real card owns the slot) or no start lands within
// 24 h.
func (p *EventPlayer) HolyLandEventUpcoming() int64 {
	p.HolyMu.Lock()
	if p.hl == nil {
		p.HolyMu.Unlock()
		return 0
	}
	open := p.hl.IsOpen
	if !p.hl.OpenKnown {
		open = p.hlOpen || p.hl.IsOpen
	}
	apply, ts := p.hl.ScheduleApply, p.hl.ScheduleTs
	p.HolyMu.Unlock()
	if !open {
		return 0
	}
	nowMs := time.Now().UnixMilli()
	if hlRunningNow(open, apply, ts, nowMs) {
		return 0
	}
	return hlNextBeginMs(apply, ts, nowMs)
}

// holyLandOrder validates an order can run now: logged in, event open, union
// still in the field, unit alive. Returns the state slot for pre-checks.
func (p *EventPlayer) holyLandOrder() (*holyLandState, error) {
	if !p.Running {
		return nil, fmt.Errorf("chưa login")
	}
	st := p.hlState()
	p.HolyMu.Lock()
	open := p.hlOpen || st.IsOpen
	leftOut, dead := st.LeftOut, st.Dead
	rebirthTime := st.RebirthTime
	p.HolyMu.Unlock()
	if !open {
		return nil, fmt.Errorf("sự kiện Cửu U chưa mở")
	}
	if leftOut {
		return nil, fmt.Errorf("bang đã bị loại khỏi Cửu U")
	}
	if dead {
		if left, waiting := hlRebirthLeft(rebirthTime, time.Now()); waiting {
			return nil, fmt.Errorf("quân đang chết, chờ hồi sinh sau %dm%02ds",
				int(left.Minutes()), int(left.Seconds())%60)
		}
		return nil, fmt.Errorf("quân đang chết, chờ hồi sinh")
	}
	return st, nil
}

// HolyLandMoveTo marches to cityID without fighting (/cuuu_move).
func (p *EventPlayer) HolyLandMoveTo(cityID int32) error {
	if cityID <= 0 {
		return fmt.Errorf("thành không hợp lệ")
	}
	if _, err := p.holyLandOrder(); err != nil {
		return err
	}
	p.hlSend(HOLY_LAND_MOVE_CMD, &proto.HolyLandGameMoveReqMsg{TargetId: cityID})
	log.Println(p.Username, "[HolyLand] nhận lệnh hành quân tới thành", cityID)
	return nil
}

// HolyLandAttackTo strikes cityID once (/cuuu_attack / cuuu_attacknpc):
// npc=true fights only the city guards, npc=false also engages the union
// players defending it (friendly fire on other leaders' accounts is the
// leader's call, same policy as God Island).
func (p *EventPlayer) HolyLandAttackTo(cityID int32, npc bool) error {
	if cityID <= 0 {
		return fmt.Errorf("thành không hợp lệ")
	}
	if _, err := p.holyLandOrder(); err != nil {
		return err
	}
	p.hlSend(HOLY_LAND_ATTACK_CMD, &proto.HolyLandGameAtkReqMsg{
		TargetId: pb.Int32(cityID),
		IsNpc:    pb.Bool(npc),
	})
	log.Println(p.Username, "[HolyLand] nhận lệnh tấn công thành", cityID, "npcOnly =", npc)
	return nil
}

// HolyLandAutoAttack toggles the 托管 auto-attack on cityID (0 = off,
// /cuuu_auto). Turning it off is always allowed, even while dead.
func (p *EventPlayer) HolyLandAutoAttack(cityID int32) error {
	if cityID == 0 {
		p.hlSend(HOLY_LAND_AUTO_ATK_CMD, &proto.HolyLandGameAutoAttackReqMsg{TargetId: pb.Int32(0)})
		log.Println(p.Username, "[HolyLand] tắt treo máy tấn công")
		return nil
	}
	if _, err := p.holyLandOrder(); err != nil {
		return err
	}
	p.hlSend(HOLY_LAND_AUTO_ATK_CMD, &proto.HolyLandGameAutoAttackReqMsg{TargetId: pb.Int32(cityID)})
	log.Println(p.Username, "[HolyLand] nhận lệnh treo máy tấn công thành", cityID)
	return nil
}

// HolyLandAccelerate buys one march speed-up (/cuuu_tangtoc). The request
// must carry our CURRENT speed-up counter as the server's validation
// baseline (echoed back incremented); only meaningful while marching.
func (p *EventPlayer) HolyLandAccelerate() error {
	if _, err := p.holyLandOrder(); err != nil {
		return err
	}
	st := p.hlState()
	p.HolyMu.Lock()
	moving, count := st.Moving, st.SpeedUp
	p.HolyMu.Unlock()
	if !moving {
		return fmt.Errorf("quân đang không hành quân (/cuuu_move trước)")
	}
	p.hlSend(HOLY_LAND_ACCELERATE_CMD, &proto.HolyLandGameAcclerateMoveReqMsg{
		SpeedUpCount: pb.Int32(count),
	})
	log.Println(p.Username, "[HolyLand] nhận lệnh tăng tốc, speedUpCount =", count)
	return nil
}

// HolyLandRebirth brings the unit back (/cuuu_hoisinh [item], HUD 🕊/❤).
// The free route waits only for the server's cooldown; the free COUNT never
// blocks (see hlShouldAutoRebirth — server arbitrates). The item route is
// manual-only and stays behind the server-reported item count (item ban).
func (p *EventPlayer) HolyLandRebirth(useItem bool) error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	st := p.hlState()
	p.HolyMu.Lock()
	open := p.hlOpen || st.IsOpen
	dead, leftOut := st.Dead, st.LeftOut
	rebirthTime, item := st.RebirthTime, st.ItemRebirth
	p.HolyMu.Unlock()
	if !open {
		return fmt.Errorf("sự kiện Cửu U chưa mở")
	}
	if leftOut {
		return fmt.Errorf("bang đã bị loại khỏi Cửu U")
	}
	if !dead {
		return fmt.Errorf("quân vẫn đang sống")
	}
	if useItem {
		if item <= 0 {
			return fmt.Errorf("không còn vật phẩm hồi sinh")
		}
	} else if left, waiting := hlRebirthLeft(rebirthTime, time.Now()); waiting {
		return fmt.Errorf("chưa tới giờ hồi sinh, đợi %dm%02ds",
			int(left.Minutes()), int(left.Seconds())%60)
	}
	// The free count deliberately does NOT gate the manual free route either
	// (customer bug 2026-10-24): a never-folded counter used to refuse every
	// press with "hết lượt". The server judges the budget — its refusal lands
	// on the leader via hlHandleBackHome (manualRebirth below).
	p.HolyMu.Lock()
	st.lastRebirthTry = time.Now() // pause the auto probe while this is in flight
	st.manualRebirth = true
	p.HolyMu.Unlock()
	p.hlSend(HOLY_LAND_BACK_HOME_CMD, &proto.HolyLandGameBackHomeReq{UseItem: pb.Bool(useItem)})
	log.Println(p.Username, "[HolyLand] nhận lệnh hồi sinh, useItem =", useItem)
	return nil
}

// HolyLandStatus renders the one-line Vietnamese status shown in
// /my_event_members and the web card.
func (p *EventPlayer) HolyLandStatus() string {
	if !p.Running {
		return "⏹️ chưa login"
	}
	st := p.hlState()
	p.HolyMu.Lock()
	defer p.HolyMu.Unlock()
	if st.OpenKnown {
		if !st.IsOpen {
			return "⏳ Cửu U chưa mở"
		}
	} else if !p.hlOpen && !st.IsOpen {
		return "⏳ Cửu U chưa mở"
	}
	if st.LeftOut {
		return "🚪 đã bị loại khỏi Cửu U"
	}
	if st.Dead {
		if left, waiting := hlRebirthLeft(st.RebirthTime, time.Now()); waiting {
			return fmt.Sprintf("☠️ đang chết, hồi sinh sau %dm%02ds",
				int(left.Minutes()), int(left.Seconds())%60)
		}
		return "☠️ đang chờ hồi sinh"
	}
	base := ""
	if st.AutoAtk != 0 {
		base = fmt.Sprintf(" · treo máy ở thành %d", st.AutoAtk)
	}
	if st.Moving {
		if st.RouteTo != 0 {
			return fmt.Sprintf("🏯 đang hành quân tới thành %d%s", st.RouteTo, base)
		}
		return "🏯 đang hành quân" + base
	}
	if st.Location != 0 {
		return fmt.Sprintf("🏯 đang đóng ở thành %d%s", st.Location, base)
	}
	return "🏯 đang chạy" + base
}

// ---------------------------------------------------------------------------
// Web map pull — the leader's /events page streams Cửu U through this
// account when its own session is offline or not battlefield-resident.

// eventHlOpen mirrors holyLandEventOpen for event accounts: a round must be
// running now (schedule/announcement open AND inside the server-anchored
// battle window) — pulling map frames for an event the display hides would
// be pure waste. Same resolution as HolyLandEventOpen above.
func eventHlOpen(p *EventPlayer) bool {
	p.HolyMu.Lock()
	defer p.HolyMu.Unlock()
	if p.hl == nil {
		return false // no schedule state yet → no day anchors → cannot prove "running"
	}
	open := p.hl.IsOpen
	if !p.hl.OpenKnown {
		open = p.hlOpen || p.hl.IsOpen
	}
	return hlRunningNow(open, p.hl.ScheduleApply, p.hl.ScheduleTs, time.Now().UnixMilli())
}

// eventHlEligible is the map-pull gate: event open and the one-shot seat
// free, so a stream pull never collides with the account's own traffic (and
// vice versa).
func eventHlEligible(p *EventPlayer) bool {
	p.HolyMu.Lock()
	busy := p.hlMapPending != nil
	p.HolyMu.Unlock()
	return !busy && eventHlOpen(p)
}

// hlDeliverMiniMap routes a 209526 answer into the waiting seat (reader
// goroutine; mirrors Player.hlDeliverMiniMap).
func (p *EventPlayer) hlDeliverMiniMap(data []byte) {
	p.HolyMu.Lock()
	ch := p.hlMapPending
	p.HolyMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// RequestHolyLandMiniMap pulls the one-shot battlefield minimap on an event
// account — the exact request the stay loop's sibling protocol speaks, with
// the leader's own RequestHolyLandMiniMap semantics (busy refusal, timeout,
// ctx). gameType/group omitted: the server answers our bracket.
func (p *EventPlayer) RequestHolyLandMiniMap(timeout time.Duration) ([]byte, error) {
	p.HolyMu.Lock()
	if p.hlMapPending != nil {
		p.HolyMu.Unlock()
		return nil, fmt.Errorf("có yêu cầu map đang chờ, thử lại sau nhé")
	}
	ch := make(chan []byte, 1)
	p.hlMapPending = ch
	p.HolyMu.Unlock()
	defer func() {
		p.HolyMu.Lock()
		if p.hlMapPending == ch {
			p.hlMapPending = nil
		}
		p.HolyMu.Unlock()
	}()

	mapMSG, _ := pb.Marshal(&proto.HolyLandGameMiniMapInfoReqMsg{})
	select {
	case p.writeChan <- pack.Pack(HOLY_LAND_MINIMAP_CMD, mapMSG, p.UID):
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}

	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("server không trả lời map, thử lại sau")
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
}

// hlSeatRoundTrip — the event-account twin of Player.hlSeatRoundTrip.
func (p *EventPlayer) hlSeatRoundTrip(seat *chan []byte, cmd string, body []byte, timeout time.Duration) ([]byte, error) {
	p.HolyMu.Lock()
	if *seat != nil {
		p.HolyMu.Unlock()
		return nil, fmt.Errorf("có yêu cầu map đang chờ, thử lại sau nhé")
	}
	ch := make(chan []byte, 1)
	*seat = ch
	p.HolyMu.Unlock()
	defer func() {
		p.HolyMu.Lock()
		if *seat == ch {
			*seat = nil
		}
		p.HolyMu.Unlock()
	}()

	// ctx-aware send: a half-dead connection (writer goroutine exited,
	// writeChan full) would otherwise block here forever and freeze the
	// whole web map streamer loop.
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("server không trả lời, thử lại sau")
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
}

// hlBracket returns the cached (gameType, group) identity from the base-info
// answer — 209522 refuses without them and this account's stay loop keeps
// them fresh.
func (p *EventPlayer) hlBracket() (gameType, group int32) {
	st := p.hlState()
	p.HolyMu.Lock()
	gt, g := st.GameType, st.Group
	p.HolyMu.Unlock()
	return gt, g
}

// RequestHolyLandCityRoutes pulls the whole-bracket marching-route snapshot
// (209522). Event accounts are the battlefield residents, so this answers
// exactly when the leader's own account would refuse with ret=9505.
func (p *EventPlayer) RequestHolyLandCityRoutes(timeout time.Duration) ([]byte, error) {
	gt, g := p.hlBracket()
	if gt == 0 || g == 0 {
		return nil, fmt.Errorf("chưa có danh tính giải (base info chưa về)")
	}
	body, _ := pb.Marshal(&proto.HolyLandGameCityInfoReqMsg{
		CityVec: hlAllCityIDs(), GameType: &gt, GroupId: &g,
	})
	return p.hlSeatRoundTrip(&p.hlRoutePending, HOLY_LAND_CITY_INFO_CMD, body, timeout)
}

// RequestHolyLandCityLine pulls one city's attacker/defender queue (209524).
func (p *EventPlayer) RequestHolyLandCityLine(cityID int32, timeout time.Duration) ([]byte, error) {
	body, _ := pb.Marshal(&proto.HolyLandGameTargetCityLineInfoReqMsg{CityId: &cityID})
	return p.hlSeatRoundTrip(&p.hlLinePending, HOLY_LAND_TARGET_LINE_CMD, body, timeout)
}

func (p *EventPlayer) hlDeliverRoutes(data []byte) {
	p.HolyMu.Lock()
	ch := p.hlRoutePending
	p.HolyMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

func (p *EventPlayer) hlDeliverLine(data []byte) {
	p.HolyMu.Lock()
	ch := p.hlLinePending
	p.HolyMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}
