package tytd_go

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"tytd_go/internal/bigchess"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	pb "google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/gorilla/websocket"
)

var (
	BLACK_LIST_CLAN = []uint64{
		744983,
	}
)

type Player struct {
	Username   string
	Password   string
	ServerID   int
	ChatID     int64
	UID        uint64
	Name       string
	ClanID     uint64
	ClanName   string
	ClanMember map[uint64]string
	Running    bool
	Peach      bool
	XaVe       bool
	Maohiem    bool
	Xiyou      bool // /start_taydu — auto spam đánh ải chính tuyến Tây Du Phục Ma (type 311)
	// xiyou carries the loop↔deliver shared state (atomic — sender and
	// reader run on separate goroutines; player_xiyou.go).
	xiyou          xiyouState
	Lucdao         bool
	Yeuthap        bool
	Phanthan       bool
	HuyetMach      bool // /start_huyet_mach — auto roll + xoá kết quả Huyết Mạch
	GrindRun       bool // /start_lv100 — auto cày acc lên lv 100
	VipLevel       int
	Notify         bool
	NotiChan       chan NotifyMGS
	AutoStartChan  chan uint64
	ChuotRun       bool
	ChuotTotal     int
	ChuotDoing     int
	ChuotHP        int
	ChuotMinBinh   int
	ChuotMinDao    int
	ChuotMinLuuly  int
	BottlePrefered bool

	// OnRunningChange, set by startPlayerLogin, asks the web layer to re-push
	// this account's SSE state frame. Called after every Running flip from the
	// game goroutine — must never block, so always go through notifyRunning.
	OnRunningChange func()

	// ActList is this account's live activity list (see player_activity.go):
	// filled by the login push (21001), upserted by 21036, refreshed hourly
	// by 21003 pulls via activityRefresh. Fully self-synchronized.
	ActList activityStore

	// God Island: the activity ids of the two island variants (Bồng Lai = 68,
	// peak Thiên Ma = 333) from the server activity push, and the one
	// outstanding /bonglai_map or /thienma_map minimap request (one channel —
	// both variants answer the same incoming type, so only one request may be
	// in flight). Written by wsHandler, read by RequestGodIslandMiniMap;
	// guard with GiMu.
	GiMu             sync.Mutex
	GiActivityID     int32
	GiPeakActivityID int32
	GiPending        chan []byte

	// BallGVG (Thượng Cổ Di Tích, activity type 14): live activity id from
	// the generic activity push and one outstanding 21111 room-map request.
	// Guard with BgMu; the websocket reader only delivers into the buffered
	// channel and never waits for the Telegram renderer.
	BgMu sync.Mutex
	// BgActivityID is the live instance id learned from announcements.
	BgActivityID int32
	BgPending    chan []byte
	// BgClosedUntil: the server refused the board (ret 2100) until this
	// moment even while the activity list advertises a window; the web map
	// streamer holds off pulling until it passes. See bgMarkClosed.
	BgClosedUntil time.Time

	// Holy Land (Cửu U): the one outstanding /cuuu_map minimap
	// request. The request carries no activity id (the server answers for
	// our bracket), so this needs no announcement tracking. Written by
	// wsHandler, read by RequestHolyLandMiniMap; guard with HlMu.
	HlMu           sync.Mutex
	HlPending      chan []byte
	HlRoutePending chan []byte // 209522 routes snapshot seat (web stream)
	HlLinePending  chan []byte // 209524 city queue seat (city click)
	// Cached open gate from the schedule sync (209570 → 2562). Live probe
	// 2026-10-03: the event runs WITHOUT any type-346 ActList entry, so
	// autoCuuuSchedule keeps this fresh — it is the only reliable signal.
	HlOpen bool
	// HlOpenKnown: the 209570 schedule answered at least once (either
	// way) — from then on it is the ONLY source of truth for the Cửu U
	// gates. The ActList 346 row is a SEASON window: it stays "active"
	// between rounds and kept the web card + /cuuu_map alive after the
	// battle had closed (user bug report 2026-10-05).
	HlOpenKnown bool
	HlRoundId   int32
	// Day anchors (ms) of the scheduled battle days — 2562 field 6 and the
	// 2567 push field 2. holyLandEventOpen needs them to tell "running now"
	// from "open all season" (see hlRunningNow). Guard with HlMu.
	HlScheduleApply []int64
	HlScheduleTs    []int64

	// KunlunWar ("崑崙崢覇", activity type 199): union city-war transport +
	// board cache. Answers are request/response with one seat per incoming
	// type (cbChans/bcChans pattern); push syncs fold straight into ku.
	// Guard KuMu.
	KuMu    sync.Mutex
	kuChans map[string]chan []byte
	ku      *kunlunState
	// warKind names the war scene this socket currently occupies
	// ("ku" | "bm", default "ku"). Đỉnh Bắc Minh answers on the EXACT same
	// SERVER_KUNLUN_* hexes as Côn Lôn — the wire cannot tell the two apart
	// — so one socket may sit in at most ONE war scene at a time and the
	// reader routes every kunlun-family answer by this flag. Guard with KuMu.
	warKind string
	// Đỉnh Bắc Minh ("北冥之巅", activity type 332): same transport, own
	// 119×119 map — a SEPARATE event sharing the kunlun opcode family, so it
	// gets its own ledger/seats (same pattern as ku). Guard with BmMu.
	BmMu    sync.Mutex
	bmChans map[string]chan []byte
	bm      *bacminhState

	// ComposeBall ("đại hội luyện khí", activity type 130): suika-style
	// board game whose answers are request/response — one seat per incoming
	// type (Big Chess bcChans pattern). The whole board ledger lives in cb.
	// Guard everything below with CbMu.
	CbMu    sync.Mutex
	cbChans map[string]chan []byte
	cb      *composeBallState

	// Sky Trade (Tiên Vực Thương Đồ, activity type 150): sky-trade
	// answers are request/response, so each incoming type reserves at
	// most one seat — the web map pull and the auto-trader share them and
	// simply back off when the seat is taken. Answers are routed here by
	// wsHandler through stHandleFrame. Guard with StMu.
	StMu    sync.Mutex
	stChans map[string]chan []byte

	// Đan Giới (Elixir): the season number rides the passive 8ae9 push at
	// login (and on rollover) — folded by elixirDeliver, read by the loop to
	// pick the season-bonus tree (864001+3*(N-1)). Guard with ElixirMu.
	ElixirMu     sync.Mutex
	ElixirSeason int32
	// 8ae9 season window (fields 2/3, UNIX MILLIS — live-decoded 2026-10-21:
	// season 8 = 1791997200000→1793293200000, a 15-day period). A session
	// that logged in before a rollover may never see a fresh 8ae9 (the
	// server does not reliably re-push while online) — elixirCurSeason then
	// PREDICTS the live season from this window instead of planting by a
	// stale trio (owner 2026-10-21: "đan giới không detect được dược linh
	// mùa mới"). ElixirPredLogged dedups the rollover log.
	ElixirSeasonStart int64
	ElixirSeasonEnd   int64
	ElixirPredLogged  int32
	// elixirOrderTried remembers destiny orders already auto-submitted-and-
	// refused (key "index:grassId=num;…", ElixirMu) so a short/refused order
	// is retried at most once per content change, not every minute; a payout
	// success deletes the key (the replacement order gets its own verdict).
	elixirOrderTried map[string]bool

	// ── Đan Giới: thuần event-driven (owner 2026-10-23) ──────────────────
	// "player.go đã gọi handler của đan giới rồi sao trong danGioiTickOnce
	// còn handler khác — chuyển về thuần event-driven cho tao"; auto Dược
	// Linh DROPPED same day ("bỏ auto dược linh đi, khỏi phải check số lượng
	// dược linh với cây dược linh"). The tick BLIND-FIRES ENTER (235502)
	// once a minute and owns NOTHING else; the 8aae HANDLER is the brain:
	// folds these vars, sleeps 1s, then runs harvest-loop + plant-loop over
	// the frame (ô vừa hái → cây cũ trùng id; ô trống → random túi; server
	// no-op falls through to the next pick). All guarded by ElixirGardenMu.
	// Frames with no lands (delta pushes) never fold.
	ElixirGardenMu sync.Mutex
	ElixirGarden   map[int32]*proto.ElixirLandInfo // landId → lot
	ElixirHome     *proto.ElixirEnterHomeResp      // last full 8aae
	ElixirBagRows  map[int32]int32                 // "danh sách available hạt trong túi": grassInfos rows, ×0 rows included (planting is free — AVAILABLE-SEED LAW)
	ElixirGardenAt time.Time
	// elixirPassRes is the last garden-pass report (what the tick returns);
	// elixirPassMu (TryLock) keeps passes serial — one pass per garden,
	// never two GET/DEL storms in flight. Signals (buffered 1, lazy-made
	// under ElixirGardenMu): elixirGardenSig = "vườn mới fold xong" (waits
	// of elixirEnterHome), elixirPassSig = "pass xong" (waits of
	// danGioiTickOnce).
	elixirPassRes   danGioiTickResult
	elixirPassMu    sync.Mutex
	elixirGardenSig chan struct{}
	elixirPassSig   chan struct{}

	// Event-delivered travel timing (seconds): RUNBUSINESS_MOVE_TIME and
	// RUNBUSINESS_ACCELATE_ITEM_EFFECT parsed off the 21003 detail push.
	// Zero means "not parsed yet" and callers fall back to the client
	// defaults. Guard with StMu.
	stMoveNeedSec int64
	stSpeedUpSec  int64

	// stSpecials maps wire port id → that port's SPECIAL good (its own
	// produce, specialGoodId from portConfig inside 21003's uniqueConfig).
	// An empty-hold run anchors on it: the planner waits for a port whose
	// reported rare is exactly the current port's specialty, then starts
	// the profit calc. Empty until the detail config lands; plain
	// scarce-matching applies meanwhile. Guard with StMu.
	stSpecials map[int32]int32

	// Big Chess (Tiên Dịch Bài Cục, activity type 341): AI-play relay for
	// the MCP tools. Answers are request/response like Sky Trade, so each
	// incoming transport type reserves at most one seat (bcChans, written by
	// wsHandler through bcDeliver); bcState is the authoritative snapshot
	// assembled from the ranked-mode answers. Guard both with BcMu.
	BcMu      sync.Mutex
	bcChans   map[string]chan []byte
	bcState   *bigchess.State
	bcCfgAt   time.Time // last 21003 config pull attempt (rate-limit)
	bcCfgDone bool      // chessConfigList + BIGCHESS_* params landed

	// Luyện Khí Đại Hội (CastSword, activity type 119, opcodes 20119xx):
	// request/response answers reserve at most one seat per transport type
	// (bcChans pattern, written by wsHandler through csDeliver). cs is the
	// last SERVER-AUTHORITATIVE board + run stats; csPlaying/csStop guard the
	// single auto-play goroutine. Guard all of it with CsMu.
	CsMu      sync.Mutex
	csChans   map[string]chan []byte
	cs        *csRunState
	csPlaying bool
	csStop    chan struct{}

	// csMoveGap overrides the per-slide wait (tests); zero = production 700ms.
	csMoveGap time.Duration

	// Chaos Law (Pháp Tắc Thái Hư, 2367xx): request/response answers reserve
	// at most one seat per transport type (clChans, written by wsHandler
	// through clDeliver). clClaimed remembers law id → claimed endTime so
	// a finished law is never re-claimed this session. Guard with ClMu.
	ClMu      sync.Mutex
	clChans   map[string]chan []byte
	clClaimed map[int32]int64

	// Huyết Mạch (Talent 2062x): talentCreateLevel learned from the 026d
	// login sync caps the roll multiplier. Guard with HmMu.
	HmMu           sync.Mutex
	hmLevel        int32
	hmLevelSeen    bool
	hmRollsDone    int64 // successful rolls this session (loop-owned)
	hmDeletesDone  int64 // deleted results this session (loop-owned)
	hmNextStatusAt int64 // loop-owned: next hmDeletesDone threshold for the per-500 status notify

	// Per-event auto-play opt-in gates. Seeded from the session's OWN row
	// (keyed by account/chat id) at login and patched in place by the
	// /auto_* commands and the settings pages while the session lives —
	// event loops read ONLY these fields, never the DB mid-loop (owner
	// 2026-10-27 "tất cả các event chỉ được check trong object player"):
	// a DB lookup by game_uid is WRONG when the same game account is
	// configured by several people (many rows share one game_uid), so the
	// toggle that governs THIS session must travel on the session itself.
	AutoSkyTrade      bool // Thương Đồ Tiên Vực caravan (type 150)
	AutoSkyTradeSpeed bool // its 21:58 end-of-day speed-up sprint
	AutoGather        bool // Tụ Linh
	AutoRebornTrial   bool // Luân Hồi Bí Cảnh (type 172)
	AutoTienCanh      bool // Tiên Cảnh Vị Ương (type 186)
	AutoSeaWar        bool // Thương Minh Yêu Vực (type 349)

	// Thương Minh Yêu Vực (CelestialSeaWar 349, player_seawar.go). SwMu
	// guards the tables folded from the 21003 answer by swMergeConfig
	// (soulBall effect/type → forge priority, region energy/unlock) and the
	// once-per-day worship/exhaust keys. Loop-owned otherwise (swPlayTick is
	// the only caller; the button touches just the write lane).
	SwMu          sync.Mutex
	swEnergyItem  int32   // SEAWAR_ENERGY_ITEM (default 105402) — BUTTON only
	swLiquidItems []int32 // SEAWAR_ITEM_ENERGY (rbt-style fold) — donate ids for linh lộ phát;
	//                      config only: SEND_LIQUID spends bag items → item
	//                      ban, the AUTO engine never sends it
	swCondCfgs []*proto.ActivityConditionConfig // 21003 condition rows —
	//                      nạp-bang rungs (type 84 / 2xxxx) claim via 21045
	swCondNext time.Time // 5-min cadence gate for the nạp-bang sweep
	// swDonatePauseUntil parks the donate burst after an all-refusal round
	// (swDonateLiquid v2) so a capped account doesn't stall every tick.
	swDonatePauseUntil time.Time
	// swAttacks is the war map's fight ring: recent 234932 challenge pushes
	// (reader writes, web war view reads — SwMu guards both).
	swAttacks      []swAttackRec
	swBallEffect   map[int32]int32
	swBallType     map[int32]int32
	swRegionEnergy map[int32]int32
	swRegionCond   map[int32]string
	swEventCfgIds  map[int32]bool // SeaWarEventConfigMsgs ids (consumeCondition classifier)
	swWorshipDay   string
	swExhaustKey   string
	swHelpBoxNeed  int32 // OPEN_HELP_BOX param (helpers needed to fill a box)
	swHighPvp      int32 // SEAWAR_HIGH_PVP_TIMES — attacks per lệnh cao cấp (lệnh thường = 1)
	// 21005 shop (owner 2026-11-09: auto-buy quảng cáo + tiên ngọc rows):
	// rows folded from 21003, server bought-counts + local same-day adds.
	swMallRows   []*proto.ActivityMallTempMsg
	swMallBought map[int32]int64
	swMallDay    string
	swMallAdds   map[int32]int64
	// refused rows rest swShopFailWait (ladder lock 307 live, ad cooldown).
	swShopFails map[int32]time.Time
	swShopDay   string
	// wake bell: SeaWarAssistSync push (234913) cuts the in-tick sleep short
	// so a teammate's call is answered within swMinTick, not swPoll.
	swWake chan struct{}

	// stPoll overrides the auto-caravan decision cycle (tests); zero uses
	// the production default. Written before the loop starts.
	stPoll time.Duration

	// stSprintTest overrides the end-of-day acceleration clock (tests); nil
	// uses the 21:58 rule of stSprintArmed. Written before the loop starts.
	stSprintTest func(time.Time) bool

	// stLastRunDay pins the pre-close random-leg gamble to once per event
	// day (loop-owned like stSprintTest — only skyTradeAutoLoop touches it).
	stLastRunDay string

	// stSprintBurnDay is the ICT date whose ONE end-of-day speed-up has
	// already been spent: after it docks the ship, normal trading is over
	// for the day (loop-owned).
	stSprintBurnDay string

	// stOpenSettle pads the alarm for an announced event open (tests);
	// zero uses 15 s. Written before the loop starts.
	stOpenSettle time.Duration

	// rbtPoll and rbtStatusGap override the Luân Hồi Bí Cảnh (RebornTrial
	// 172) decision cycle and status heartbeat (tests); zero keeps the
	// production defaults. rbtTreeGap overrides the union-tree cadence.
	// Written before the loop starts.
	rbtPoll      time.Duration
	rbtStatusGap time.Duration
	rbtTreeGap   time.Duration

	// RbtMu guards the config folded from the 21003 answer (written by the
	// reader goroutine in rbtMergeConfig, read by the auto loop).
	RbtMu sync.Mutex
	// rbtDailyOpenSec/rbtDailyCloseSec are the seconds-from-server-midnight
	// battle window (RENBORNTRIAL_CHALLENGE_DURATION "19|22" default).
	rbtDailyOpenSec  int
	rbtDailyCloseSec int
	// rbtTimeCfg holds the phase table (type, begin-hours, duration-hours)
	// folded from uniqueConfig; empty until the config lands — then the
	// client's own default table stands in (rbtDefaultTimeCfg).
	rbtTimeCfg [][3]int

	// Loop-owned (only rebornTrialAutoLoop touches): worship is capped at
	// once per (activityID,round); qualNotified dedupes "chưa đủ điều kiện"
	// notices per (activityID,round). The regular account never fights bang
	// chiến — event accounts attack on the owner's ⚔️ map order
	// [[tytd-reborntrial-172]].
	rbtWorshipped   map[string]bool
	rbtQualNotified string
	// rbtAutoUsed is the "actID:round" key of the one AUTO_EXPLORE batch we
	// are allowed per round — AUTO has a DAILY USE COUNT
	// (RENBORNTRIAL_AUTO_PROCESS_LIMIT) and polling it every minute burns it
	// (live 2026-10-09: ret 17204 = spent).
	rbtAutoUsed string
	// Treasure-op eligibility tables folded from uniqueConfig ItemConfig
	// (isMagic) and the two activity param lists (repair/fake quality
	// gates) — read by the manual magic sweep.
	rbtItemMagic   map[int32]int32
	rbtRepairLimit []int32
	rbtFakeLimit   []int32
	// Shop (21003 mallConfig + mallBuyCountList, folded in rbtMergeConfig)
	// and today's locally-added buys per ICT date for the auto-shop sweep.
	rbtMallRows   []*proto.ActivityMallTempMsg
	rbtMallBought map[int32]int64
	rbtMallAdds   map[int32]int64
	rbtMallDay    string
	// Condition milestones (21003 conditionConfig + conditionDataList):
	// the auto-claim sweep receives completed ones via 21004 (2xxxx band
	// rows go through 21045 instead). rbtCondGot only suppresses double
	// sends between hourly merges; every merge resets it from the server's
	// isGetReward flags.
	rbtCondCfgs []*proto.ActivityConditionConfig
	rbtCondData map[int32]*proto.ActivityConditionData
	rbtCondGot  map[int32]bool
	// Guild-war bracket (217230 answer, folded by rbtBattleList in
	// player_reborntrial_battle.go): the latest snapshot + the activity it
	// belongs to + its fetch time. Read by the /luanhoi map stream and the
	// attack command's target picker; guarded by RbtMu like the rest.
	rbtBattle    []*proto.RebornTrialUnionChallengeMsg
	rbtBattleAct int32
	rbtBattleAt  time.Time
	// Live counters surfaced on the map: challenges left (playerData
	// battleNum, refreshed every march tick and by the ticket/attack
	// commands) and the round number EnterActivity reports.
	rbtBattleNum    int32
	rbtBattleRecov  int64
	rbtRoundNo      int32
	rbtFreeAttempts int32
	// rbtAttacks: recent 217234 fight pushes ("X đánh base Y N dmg") — the
	// reader appends (rbtNoteFight), the /luanhoi map draws red rings +
	// popup lines. Ring-capped; guarded by RbtMu.
	rbtAttacks []rbtAttackRec
	// Ticket item id for "/luanhoi_ve" (RENBORNTRIAL_CHALLENGE_ITEM, client
	// default 100160; real value folded in rbtMergeConfig).
	rbtChallengeItem int32
	// rbtEvSeq: tinhnang.jpg option-cycle cursors per event type — only the
	// march loop reads/writes it, no lock needed.
	rbtEvSeq map[int32]int
	// Energy fruit (auto eat, owner-authorised 2026-10-09 [[tytd-item-ban]]):
	// item id + cap folded from 21003 detail; 0 item = NEVER eat.
	// rbtEnergyRecovAt mirrors the server's natural-recovery clock. Guard
	// with RbtMu.
	rbtEnergyItem    int32
	rbtStaminaLimit  int32
	rbtEnergyRecovAt int64
	// rbtLiquidIDs: the 3 union-tree water item ids folded from
	// RENBORNTRIAL_ITEM_ENERGY ("id|value;...") — rbtLiquidGrant donates them;
	// EMPTY = never donate (config not folded yet). Guard with RbtMu.
	rbtLiquidIDs []int32

	// rbtRankTypes: distinct rankConfig[].type keys folded from the 21003
	// commonConfig (live 172: 1 = cá nhân, 2 = bang) — rbtRankSweep claims
	// the season rank reward (21006) per key. EMPTY (config not folded
	// yet) = opcode never sent. Guard with RbtMu.
	rbtRankTypes []int32

	// ─── Tiên Cảnh Vị Ương (WeYound, systemOpenId 186) ───────────────────────
	// WyMu guards the config folded from the 21003 answer + the loop bookkeeping
	// the reader goroutine and the auto loop share.
	WyMu sync.Mutex
	// Daily explore window (WeYoundActivity_EXPLORE_DURATION "10|22" default),
	// seconds from ICT midnight. The client only opens the map inside it.
	wyOpenSec, wyCloseSec int
	// Stamina item id (ENERGY_ITEM 105185) — folded so the ban is airtight:
	// the loop compares against it and NEVER sends USE_ITEM. 0 until config.
	wyEnergyItem int32
	// Shop rows (21003 mallConfig) + today's locally-added buys, ICT-keyed.
	wyMallRows   []*proto.ActivityMallTempMsg
	wyMallBought map[int32]int64
	wyMallAdds   map[int32]int64
	wyMallDay    string
	// Tree-water donate ids (WeYoundActivity_ITEM_DONATE "id|v;id|v;…") —
	// EMPTY until config lands; donate is armed-by-config only.
	wyLiquidIDs []int32
	// Milestone ladder rows (21003 conditionConfig) — folded for the blind
	// 21033/21045 "nạp bang" sweep. Guard WyMu.
	wyCondCfgs []*proto.ActivityConditionConfig
	// Select-event auto-choice: the client sends param1 = monsterParam−1 read
	// from the folded eventConfig table (id → row). Guard with WyMu.
	wyEventCfg map[int32]*proto.WeYoundEventConfigMsg
	// floor → CDN map row (mapConfig): which static json the client builds
	// its fog grid from + the stamina cost of one fog explore. Guard WyMu.
	wyMapCfg map[int32]*proto.WeYoundMapConfigMsg
	// buffCfg id → client BuffType (1 = FreeExplore exempts the explore
	// stamina gate — the ONLY way a 0-stamina account may still dig).
	wyBuffType map[int32]int32
	// Loop-owned bookkeeping (only tienCanhAutoLoop touches — no lock):
	// worship once per (actID,round); qualNotified dedupes "chưa đủ kiện"
	// notices per (actID,round); explored cells + retired unresolvable events
	// persist for the activity so a re-ENTER doesn't re-walk dead ends.
	wyWorshipped   map[string]bool
	wyQualNotified string
	wyDiagLogged   string          // last "mapId:fN" whose grid histogram logged
	wyExplored     map[string]bool // actID → "x,y" cleared
	wyStuck        map[string]bool // actID → "x,y" event we gave up on
	wyAttked       map[string]bool // actID:floor → monster event cell we already swung at (đánh-luận sweep, one per round)
	wyFloor        int32           // last observed floor (progress log dedupe)

	// Yard (vườn): last observed production recipe per building instance
	// (uniqueId → productId + batch size). autoYard normally picks the crop
	// the client would preselect from the static level tables
	// (yard_static.go); this cache is the fallback for instances whose level
	// the tables can't answer. Guard with YardMu.
	YardMu      sync.Mutex
	YardRecipes map[int64]*yardRecipe
	yardHinted  map[int64]bool // instances we already logged "learn a recipe" for
	yardDraw    yardDrawState  // free + ad draw pacing; see player_yard.go

	// Union daily activity boxes (rương sôi nổi bang): one paced claim per
	// 6 h — see player_uniondaily.go. UnionDailyMu guards the log-once
	// refusal codes.
	UnionDailyMu     sync.Mutex
	unionDailyLogged map[int32]bool

	// Union Bounty (yêu minh huyền thưởng): the escort start waiting on the
	// HAVE_ESCORT answer, and log-dedup keys for the 10-minute poll.
	// Guard with UBMu.
	UBMu          sync.Mutex
	ubStartCfg    int32
	ubWantMonster bool // an OPEN_MONSTER probe is outstanding
	ubLastAtk     time.Time
	ubLogged      map[string]bool

	// Task system (nhiệm vụ): server-pushed task map (501 full / 502 merge)
	// feeding the union task auto-claim, and log-dedup keys. Guard with
	// TaskMu. taskSynced flips with the first full 501 — until then nothing
	// is known and the task-driven chop must not run. taskKick (1-deep, made
	// in Run) is poked when a push turns a task claimable, so the claim does
	// not wait for the 10-min poll.
	TaskMu      sync.Mutex
	tasks       map[int32]*taskEntry
	taskLogged  map[string]bool
	taskSynced  bool
	taskKick    chan struct{}
	lastWatchAd time.Time
	// elixirClaimTried remembers the last progress value each Đan-Giới task
	// was claimed at (id → value, TaskMu-guarded): one 20503 per (id,value)
	// per snapshot instead of one per minute. Any value change — or a fresh
	// 501 (login/daily reset, cleared in taskHandleList) — re-arms the row.
	// Completion here is the client's bag check (owner model), so value>=
	// target is the ONLY done test; state is never read.
	elixirClaimTried map[int32]string

	// Tree level (cây mộng): dreamLv cached from the S_DREAM_DATA_SYNC login
	// push; it caps how many trees one chop request may ask for
	// (maxChopByDreamLv). dreamKnown separates "level 0" from "push not seen
	// yet". chopStop marks a hard chop rejection (out of peach / overload
	// give-up) that must pause even task-driven chopping; any successful chop
	// clears it. Guard with dreamMu.
	dreamMu    sync.Mutex
	dreamLv    int32
	dreamKnown bool
	chopStop   bool

	// Chop stall recovery. Errors 517/532 on SERVER_CHAT_CAY mean the server
	// still holds drops from an EARLIER chop whose huỷ never completed (the
	// S_HUY_DO_CHAT_CAY answer is what actually destroys them, and funtap
	// drops it when overloaded). peachPending are ids we sent to huỷ without
	// a later accepted chop proving they cleared; errPeach counts consecutive
	// stalls that found nothing to clear (5 then the known back-off).
	// Guard with dreamMu.
	peachPending []int32
	errPeach     int

	// Free ad spins (tinh quái + thần thông): GameConfigDB caps each at
	// SPIRIT/MAGIC_AD_LIMIT=2 per day with a 10 s cooldown. The used-count
	// pushes (20821/24400) have no public schema, so attempts (never more
	// than the cap) are tracked per ICT day here and the server's ret stays
	// the arbiter. Guard with spinMu.
	spinMu     sync.Mutex
	spiritSpin spinState
	magicSpin  spinState

	// Gather Energy (tụ linh trận): board open 10:00–22:00 ICT
	// (ENERGY_AREA_OPEN_TIME 600|720). Ad claims capped by the server's
	// getTimes (ENERGY_AREA_AD_LIMIT=3). Joining is time-of-day driven —
	// see player_gather.go; rewards/ads keep flowing overnight while the
	// board is shut. Guard with gatherMu.
	gatherMu      sync.Mutex
	gatherDay     string // ICT date the last ENTER_NEW belonged to
	gatherKnown   bool   // an ENTER_NEW answer has been installed
	gatherEnterAt time.Time
	gatherAdTimes int32 // server-authoritative ad claims used today
	gatherAdLast  time.Time
	gatherAttend  int32   // formations currently held
	gatherTotal   int32   // server-defined join-seat maximum (ENTER_NEW.totalAttendNum: 1, or 2 with extra slots)
	gatherPending []int64 // finished-reward ids awaiting GET_REWARD
	gatherClaimAt time.Time
	gatherListAt  time.Time
	gatherWantID  int64 // candidate under INSIDE_VIEW re-check
	gatherWantAt  time.Time

	conn          *websocket.Conn
	ctx           context.Context
	cancel        context.CancelFunc
	writeChan     chan []byte
	lifecycleMu   sync.Mutex
	stopRequested bool
	DB            *gorm.DB

	// dbID is the users-row primary key this session belongs to, captured
	// at login while p.UID still holds the session-key id (doLogin replaces
	// it with the game uid right after). Only the reader goroutine touches
	// nameSaved, so the nickname dedup needs no lock.
	dbID      string
	nameSaved string
}

func (p *Player) doLogin() (string, string, error) {
	passportToken, err := request.LoginGPassport(p.Username, p.Password)
	if err != nil {
		return "", "", err
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		return "", "", err
	}
	gameURL, err := request.GetServerURL(userID, p.ServerID)
	if err != nil {
		return "", "", err
	}
	token, wsURL, pid, err := request.GetWSKey(gameURL, sessionData)
	if err != nil {
		return "", "", err
	}
	// p.UID is still the account-id string as a number here (== the users
	// row primary key); remember it before the game uid takes over.
	p.dbID = fmt.Sprintf("%d", p.UID)
	if p.DB != nil {
		model.UpdateGameUID(p.DB, p.dbID, uint64(pid))
	}
	p.UID = pid

	return token, wsURL, nil
}

func (p *Player) login() (string, string, error) {
	type result struct {
		token string
		wsURL string
		err   error
	}
	c := make(chan result, 1)
	go func() {
		token, wsURL, err := p.doLogin()
		c <- result{token: token, wsURL: wsURL, err: err}
	}()

	select {
	case res := <-c:
		return res.token, res.wsURL, res.err
	case <-time.After(30 * time.Second):
		return "", "", fmt.Errorf("login timeout")
	}
}

// storeIngameName mirrors the sanitized nickname into this account's users
// row. Reader-goroutine only (nameSaved has no lock); the dedup keeps the
// every-login push from re-writing an unchanged value, and a failed write
// forgets the value so the next push retries.
func (p *Player) storeIngameName() {
	if p.DB == nil || p.dbID == "" || p.Name == "" || p.Name == p.nameSaved {
		return
	}
	if err := model.UpdateUserIngameName(p.DB, p.dbID, p.Name); err != nil {
		log.Printf("[%s] lưu tên trong game thất bại: %v", p.Username, err)
		return
	}
	p.nameSaved = p.Name
}

// wsReadTimeout bounds how long the reader waits between inbound frames.
// The 5 s app-level ping is answered by the server whenever the socket is
// really alive, so silence this long means a half-open TCP (NAT/LB idle
// drop): without a deadline wsHandler parks in the kernel forever, Run()
// parks in wsKeepAlive's write, and every auto* goroutine piles onto the
// full writeChan — the whole account leaks (audit 2026-10-06, finding #1).
const wsReadTimeout = 120 * time.Second

// write queues one frame. It MUST stay ctx-aware: with a bare send, a caller
// (26+ auto* loops, command handlers) blocks forever once a wedged socket
// fills the 16-slot buffer, and the goroutine survives even after cancel().
func (p *Player) write(msg []byte) {
	if p.ctx == nil {
		return // never (or no longer) connected: dropping is the old panic-free behaviour, no worse
	}
	select {
	case p.writeChan <- msg:
	case <-p.ctx.Done():
	}
}

func (p *Player) doWrite() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case msg := <-p.writeChan:
			// A half-open peer makes WriteMessage block until the kernel
			// gives up (minutes-hours); the deadline turns that into a fast
			// error so the account unwinds instead of parking here.
			p.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			err := p.conn.WriteMessage(2, msg)
			if err != nil {
				p.cancel()
				log.Printf("[%s] websocket write error: %v", p.Username, err)
				return
			}
		}
	}
}

// notifyRunning fires OnRunningChange on its own goroutine: the game loop
// must never wait on a browser. Safe on a nil hook (bot-only builds/tests).
func (p *Player) notifyRunning() {
	if p.OnRunningChange != nil {
		fn := p.OnRunningChange
		go fn()
	}
}

func (p *Player) Run() bool {
	p.lifecycleMu.Lock()
	stopRequested := p.stopRequested
	p.lifecycleMu.Unlock()
	if stopRequested {
		return true
	}

	token, wsURL, err := p.login()
	if err != nil {
		log.Printf("[%s] ĐĂNG NHẬP THẤT BẠI (chat %d, server %d): %v", p.Username, p.ChatID, p.ServerID, err)
		p.Notify = true
		p.sendNotify("đăng nhập thất bại: " + err.Error())
		return false
	}
	p.lifecycleMu.Lock()
	stopRequested = p.stopRequested
	p.lifecycleMu.Unlock()
	if stopRequested {
		return true
	}
	p.Running = true
	p.notifyRunning()
	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	c, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		log.Printf("[%s] KẾT NỐI WEBSOCKET THẤT BẠI (chat %d, server %d): %v", p.Username, p.ChatID, p.ServerID, err)
		p.Running = false
		p.notifyRunning()
		return false
	}
	defer c.Close()
	defer func() {
		p.Running = false
		p.notifyRunning()
	}()
	// Control-frame handlers refresh the read deadline too, so a live socket
	// that only ever sees pong traffic is never mistaken for half-open.
	c.SetPingHandler(func(string) error {
		return c.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})
	c.SetPongHandler(func(string) error {
		return c.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.lifecycleMu.Lock()
	p.conn = c
	p.ctx = ctx
	p.cancel = cancel
	stopRequested = p.stopRequested
	p.lifecycleMu.Unlock()
	if stopRequested {
		cancel()
		return true
	}
	p.writeChan = make(chan []byte, 16)
	go p.doWrite()

	authStruct := &proto.Auth{
		Token:  token,
		Locale: "vi_vn",
		Status: pb.Bool(false),
	}
	authMSG, _ := pb.Marshal(authStruct)
	// send auth msg
	p.write(pack.Pack(AUTH_CMD, authMSG, p.UID))

	p.taskKick = make(chan struct{}, 1)
	go p.wsHandler()
	settle := time.NewTimer(5 * time.Second)
	select {
	case <-ctx.Done():
		settle.Stop()
		return true
	case <-settle.C:
	}
	if ctx.Err() != nil {
		return true
	}
	go p.autoDoSomethings()
	go p.autoCheckInfoClan()
	go p.autoChuot()
	go p.autoMaohiem()
	go p.autoXiyou()
	go p.autoXiyouSecret()
	go p.autoYeuthap()
	go p.autoPeach()
	go p.autoThancot()
	go p.autoLucdao()
	go p.autoNhanQuaCay()
	go p.autoGiamCay()
	go p.autoSendGift()
	go p.danGioiAutoLoop()
	go p.autoNhanTienDoTamBao()
	go p.autoPupilTrain()
	go p.autoPhapTacThaiSo()
	go p.autoYueBao()
	go p.autoHuyetMach()
	go p.autoCuuuSchedule()
	go p.autoYard()
	go p.autoUnionBounty()
	go p.autoUnionTask()
	go p.autoFreeSpin()
	go p.autoGatherEnergy()
	go p.autoUnionDaily()
	go p.autoUnionTreasure()
	go p.autoElixirWater()
	go p.activityRefresh()
	go p.skyTradeReportLoop()
	go p.skyTradeAutoLoop()
	go p.rebornTrialAutoLoop()
	go p.tienCanhAutoLoop()
	go p.seaWarAutoLoop()
	go p.autoGrindLoop()

	p.wsKeepAlive()
	return true
}

func (p *Player) wsKeepAlive() {
	for {
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			p.write(pack.Pack(PING_CMD, []byte{}, p.UID))
		}
	}
}

// StopConnection closes the live game socket and is safe to call while Run is
// still logging in. If the connection is not installed yet, Run observes the
// request immediately after dialing and closes it before starting workers.
func (p *Player) StopConnection() {
	p.lifecycleMu.Lock()
	p.stopRequested = true
	cancel, conn := p.cancel, p.conn
	p.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

func (p *Player) wsHandler() {
	for {
		select {
		case <-p.ctx.Done():
			log.Println("[Reader] Context done")
			return
		default:
			p.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
			_, message, err := p.conn.ReadMessage()
			if err != nil {
				if p.ctx.Err() == nil {
					log.Printf("[%s] websocket read error: %v", p.Username, err)
				}
				p.cancel() // trigger shutdown
				return
			}
			isMSG, msgType, realData := pack.Unpack(message)
			if !isMSG {
				log.Println("not msg")
				continue
			}
			if os.Getenv("TYTD_DEBUG_RAWMSG") != "" {
				log.Printf("[rawmsg] %s len=%d", msgType, len(realData))
			}
			//log.Println(msgType)
			//hexString := hex.EncodeToString(realData)
			//log.Println("Hexadecimal string:", hexString)
			// Handlers that write frames or sendNotify can block (writeChan
			// drains at socket speed; a stalled TCP send parks doWrite). They
			// run in their own goroutine so one slow handler never queues
			// behind the reader — same convention as the legacy go func cases.
			switch msgType {
			case "00d9":
				go func() {
					p.write(pack.Pack(S_GET_TOP_PLAYER_INFO, []byte{}, p.UID))
					p.write(pack.Pack(S_ATTRIBUTE_GET_SEPARATION_DATAA_MSG_LIST_REQ, []byte{}, p.UID))
				}()
			case "0065":
				var info proto.PlayerInfo
				err := pb.Unmarshal(realData, &info)
				if err != nil {
					continue
				}
				// The nickname is player-chosen UTF-8: keep only what a
				// browser or terminal will faithfully repaint (see
				// model.SanitizeIngameName) and mirror it into the users row.
				p.Name = model.SanitizeIngameName(info.UnicodeStr)
				p.storeIngameName()

			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
				p.giHandleActivityList(realData)
				p.bgHandleActivityList(realData)

			case SERVER_ACTIVITY_DETAIL:
				// Hourly 21003 pull answers (and any unsolicited detail
				// sync) refresh the stored entry's window fields.
				p.ActList.mergeDetail(p.Username, realData)
				// The same answer carries the Sky Trade travel timing.
				p.stMergeConfig(realData)
				// And, for type-341 rows, the Big Chess piece-score table.
				p.bcMergeConfig(realData)
				// And, for type-130 rows, the ComposeBall tier table.
				p.cbMergeConfig(realData)
				// And, for type-199 rows, the KunlunWar phase/city config.
				p.kuMergeConfig(realData)
				// And, for type-332 rows, the Đỉnh Bắc Minh config.
				p.bmMergeConfig(realData)
				// And, for type-172 rows, the Luân Hồi Bí Cảnh window/phase config.
				p.rbtMergeConfig(realData)
				// And, for type-186 rows, the Tiên Cảnh Vị Ương window/water/shop config.
				p.wyMergeConfig(realData)
				// And, for type-349 rows, the Thương Minh Yêu Vực forge/region config.
				p.swMergeConfig(realData)
				// The 172 shop sweep round-trips this command to fetch its
				// mall table; deliver so its seat resolves (no other system
				// ever claims this type).
				p.stDeliver(SERVER_ACTIVITY_DETAIL, realData)

			case SERVER_KUNLUN_ENTER_ACTIVITY, SERVER_KUNLUN_ENTER_GAME, SERVER_KUNLUN_GAME_BLOCK_INFO,
				SERVER_KUNLUN_MOVE, SERVER_KUNLUN_EXPLORE, SERVER_KUNLUN_SWEEP, SERVER_KUNLUN_WORSHIP,
				SERVER_KUNLUN_GROUP_INFO, SERVER_KUNLUN_SIMPLE_BLOCK_INFO, SERVER_KUNLUN_HEART_BEAT,
				SERVER_KUNLUN_MINI_MAP_INFO, SERVER_KUNLUN_LEAVE,
				// The fruit tree (live-probe 2026-10-06: 4dc6 timed out HERE,
				// not on the server — the reader never dispatched it).
				SERVER_KUNLUN_SPAR_INFO, SERVER_KUNLUN_SPAR_POWER, SERVER_KUNLUN_USE_ITEM,
				SERVER_KUNLUN_BLOCK_SYNC, SERVER_KUNLUN_CITY_SYNC, SERVER_KUNLUN_MY_GAME_SYNC,
				SERVER_KUNLUN_BEEN_KILL_SYNC, SERVER_KUNLUN_LEFT_OUT_SYNC, SERVER_KUNLUN_CITY_BATTLE_SYNC,
				SERVER_KUNLUN_ROUTE_SYNC, SERVER_KUNLUN_LINE_SYNC, SERVER_KUNLUN_ATTACK_SYNC,
				SERVER_KUNLUN_GROUP_RANK_SYNC:
				// Côn Lôn and Đỉnh Bắc Minh answer on identical hexes — the
				// socket can hold only one war scene, so route by the seat
				// the last successful enter left behind (see player.go warKind).
				p.KuMu.Lock()
				wk := p.warKind
				p.KuMu.Unlock()
				if wk == warKindBacMinh {
					p.bmDeliver(msgType, realData)
				} else {
					p.kuDeliver(msgType, realData)
				}

			case SERVER_COMPOSE_BALL_ENTER, SERVER_COMPOSE_BALL_COMPOSE,
				SERVER_COMPOSE_BALL_USE_ITEM, SERVER_COMPOSE_BALL_HP_ITEM,
				SERVER_COMPOSE_BALL_SETTLE:
				p.cbDeliver(msgType, realData)

			case SERVER_SKY_TRADE_ENTER, SERVER_SKY_TRADE_ENTER_MAP, SERVER_SKY_TRADE_REPORT_GOODS,
				SERVER_SKY_TRADE_SPEED_UP, SERVER_SKY_TRADE_MOVE, SERVER_SKY_TRADE_BUY, SERVER_SKY_TRADE_MOVE_FINISH:
				p.stHandleFrame(msgType, realData)

			case SERVER_ACTIVITY_BUY_GOODS, SERVER_ACTIVITY_COND_REWARD, SERVER_ACTIVITY_COND_REWARD_ARR, SERVER_ACTIVITY_RANK_REWARD, SERVER_ACTIVITY_CROSS_RECHARGE, SERVER_ACTIVITY_TOKEN_REWARD:
				// 21005/21004/21045 answers ride the same seat plumbing
				// (shop + condition-claim sweeps).
				p.rbtHandleFrame(msgType, realData)
			case SERVER_RBT_ENTER_ACTIVITY, SERVER_RBT_ENTER, SERVER_RBT_WORSHIP, SERVER_RBT_EXPLORE,
				SERVER_RBT_SELECT_OPTION, SERVER_RBT_SKIP_EVENT, SERVER_RBT_BONUS, SERVER_RBT_BONUS_RESULT, SERVER_RBT_BONUS_ITEM,
				SERVER_RBT_GUESS_DATA, SERVER_RBT_GUESS_REWARD, SERVER_RBT_AUTO_EXPLORE, SERVER_RBT_USE_MAGIC,
				SERVER_RBT_BATTLE_ENTER, SERVER_RBT_BATTLE_HEART, SERVER_RBT_BATTLE_CHALLENGE,
				SERVER_RBT_BATTLE_UNIONPLAYER, SERVER_RBT_BATTLE_NOTIC, SERVER_RBT_USE_ITEM,
				SERVER_RBT_SEND_BONUS_ITEM:
				p.rbtHandleFrame(msgType, realData)

			case SERVER_WE_YOUND_ENTER_ACTIVITY, SERVER_WE_YOUND_ENTER, SERVER_WE_YOUND_EXPLORE,
				SERVER_WE_YOUND_MOVE, SERVER_WE_YOUND_DEAL_EVENT, SERVER_WE_YOUND_TRAP_SEEK,
				SERVER_WE_YOUND_ASSIST, SERVER_WE_YOUND_ASSIST_LOG,
				SERVER_WE_YOUND_WORSHIP, SERVER_WE_YOUND_REWARD_FLOOR, SERVER_WE_YOUND_GET_MONSTER_REWARD,
				SERVER_WE_YOUND_BONUS, SERVER_WE_YOUND_GET_BONUS_RESULT, SERVER_WE_YOUND_GET_BONUS_ITEM,
				SERVER_WE_YOUND_SEND_BONUS_ITEM, SERVER_WE_YOUND_UNION_GIFT_LIST, SERVER_WE_YOUND_RECEIVE_UNION_GIFT:
				p.wyHandleFrame(msgType, realData)

			// Thương Minh Yêu Vực: every 349 answer rides the sky-trade seat
			// plumbing (swRT = stRoundTrip; the auto loop is the only
			// round-trip caller).
			case SERVER_SW_ENTER, SERVER_SW_ENTER_GAME, SERVER_SW_EXPLORE,
				SERVER_SW_HANDLE_EVENT, SERVER_SW_UNION_WELFARE, SERVER_SW_UNION_WELFARE_REWARD,
				SERVER_SW_UNION_RECEIVE_LIQUID, SERVER_SW_UNION_SEND_LIQUID, SERVER_SW_UNION_LIQUID_RECORD,
				SERVER_SW_HEART_BEAT, SERVER_SW_NEXT_REGION,
				SERVER_SW_USE_ITEM, SERVER_SW_WORSHIP,
				SERVER_SW_ASSIST_LIST, SERVER_SW_ASSIST, SERVER_SW_ASSIST_REWARD,
				SERVER_SW_ASSIST_SYNC, SERVER_SW_ASSIST_SYNC_RAW,
				SERVER_SW_CHALLENGE_LIST, SERVER_SW_CHALLENGE_INFO_SYNC, SERVER_SW_DECLARATION_INFO_SYNC,
				SERVER_SW_ENTER_CHALLENGE, SERVER_SW_CHALLENGE_INFO, SERVER_SW_CHALLENGE_BATTLE, SERVER_SW_DECLARATION:
				p.swHandleFrame(msgType, realData)

			case SERVER_GOD_ISLAND_MINIMAP:
				p.giDeliverMiniMap(realData)

			case SERVER_BALL_GVG_GET_PLACE:
				p.bgDeliverMap(realData)

			case SERVER_HOLY_LAND_MINIMAP:
				p.hlDeliverMiniMap(realData)

			case SERVER_HOLY_LAND_CITY_INFO:
				p.hlDeliverRoutes(realData)

			case SERVER_HOLY_LAND_TARGET_LINE:
				p.hlDeliverLine(realData)

			case SERVER_HOLY_LAND_SCHEDULE:
				p.hlHandleScheduleMsg(realData)

			case SERVER_HOLY_LAND_SCHEDULE_PUSH:
				p.hlHandleSchedulePushMsg(realData)

			case SERVER_BIG_CHESS_ENTER, SERVER_BIG_CHESS_ENTER_GAME, SERVER_BIG_CHESS_MOVE,
				SERVER_BIG_CHESS_END, SERVER_BIG_CHESS_USE_ITEM, SERVER_BIG_CHESS_USE_RANDOM,
				SERVER_BIG_CHESS_PRACTICE_ENTER_GAME, SERVER_BIG_CHESS_PRACTICE_MOVE,
				SERVER_BIG_CHESS_PRACTICE_END:
				p.bcDeliver(msgType, realData)

			case SERVER_CASTSWORD_ENTER, SERVER_CASTSWORD_MOVE,
				SERVER_CASTSWORD_SETTLE, SERVER_CASTSWORD_GETREWARD:
				p.csDeliver(msgType, realData)

			case SERVER_CHAOS_LAW_ENTER, SERVER_CHAOS_LAW_PERCEIVE, SERVER_CHAOS_LAW_EVOLVE,
				SERVER_CHAOS_LAW_EVOLVE_REWARD, SERVER_CHAOS_LAW_ONE_KEY_HELP,
				SERVER_YUE_BAO_ENTER, SERVER_YUE_BAO_DEPOSIT, SERVER_YUE_BAO_INTERACTE,
				SERVER_TALENT_RANDOM, SERVER_TALENT_DEAL, SERVER_TALENT_UNDEAL:
				p.clDeliver(msgType, realData)

			case SERVER_TALENT_SYNC:
				p.hmHandleSync(realData)

			case SERVER_YARD_ENTER:
				go p.yardHandleEnter(realData)

			case SERVER_YARD_DRAW:
				go p.yardHandleDraw(realData)

			case SERVER_YARD_DATA_SYNC:
				go p.yardHandleDataSync(realData)

			case SERVER_YARD_MAKE:
				var resp proto.YardBuildMakeResp
				if err := pb.Unmarshal(realData, &resp); err == nil && resp.GetRet() != 0 {
					// a refused make stalls the loop — surface it; success is
					// silent and shows up in the next ENTER snapshot
					log.Printf("[%s] [Yard] lệnh sản xuất bị từ chối: ret=%d", p.Username, resp.GetRet())
				}

			case SERVER_UNION_BOUNTY_OPEN_EVENT:
				go p.unionBountyHandleOpen(realData)

			case SERVER_UNION_BOUNTY_HAVE_ESCORT:
				go p.unionBountyHandleHaveEscort(realData)

			case SERVER_UNION_BOUNTY_DEAL:
				go p.unionBountyHandleDeal(realData)

			case SERVER_UNION_BOUNTY_GET_REWARD:
				go p.unionBountyHandleReward(realData)

			case SERVER_UNION_BOUNTY_OPEN_MONSTER:
				go p.unionBountyHandleOpenMonster(realData)

			case SERVER_UNION_BOUNTY_ATTACK_MONSTER:
				go p.unionBountyHandleAttackMonster(realData)

			// Task handlers stay on the reader: they never write and hold
			// TaskMu for microseconds, and the 501 snapshot must merge before
			// any 502 patch lands. Off-thread dispatch would only add races.
			case SERVER_TASK_DATA_SEND:
				p.taskHandleList(realData, true)

			case SERVER_TASK_DATA_SYNC:
				p.taskHandleList(realData, false)

			case SERVER_TASK_GET_REWARD:
				p.taskHandleReward(realData)
				// The elixir claim pass listens per-id (20503→01f7 seat);
				// stDeliver is a non-blocking shove into a buffered seat —
				// safe on the reader, invisible to every other consumer.
				p.stDeliver(msgType, realData)

			case SERVER_DO_CHUA_HUY:
				go p.handleDreamSync(realData)

			case SERVER_SPIRIT_DRAW:
				p.spinHandle(SPIN_SPIRIT, realData)

			case SERVER_MAGIC_DERIVATION:
				p.spinHandle(SPIN_MAGIC, realData)

			case SERVER_GATHER_SYNC:
				p.gatherHandleSync(realData)

			case SERVER_GATHER_REWARD_SHOW:
				p.gatherHandleRewardShow(realData)

			case SERVER_GATHER_ENTER_NEW:
				p.gatherHandleEnter(realData)

			case SERVER_GATHER_AD_AWARD:
				p.gatherHandleAd(realData)

			case SERVER_GATHER_GET_REWARD:
				p.gatherHandleClaim(realData)

			case SERVER_GATHER_ATTEND:
				p.gatherHandleAttend(realData)

			case SERVER_GATHER_FIRST_LIST:
				go p.gatherHandleList(realData, time.Now())

			case SERVER_GATHER_INSIDE:
				go p.gatherHandleInside(realData, time.Now())

			case SERVER_BANG_TRA_GIA_INFO:
				go func() {
					var tmp proto.PhanThan
					err := pb.Unmarshal(realData, &tmp)
					if err != nil {
						log.Println(err.Error())
						return
					}
					tmpMsg, _ := pb.Marshal(&tmp)
					p.write(pack.Pack(S_BANG_TRA_GIA, tmpMsg, p.UID))
				}()

			case SERVER_CLAN_INFO:
				go func() {
					var info proto.MClanInfo
					err := pb.Unmarshal(realData, &info)
					if err != nil {
						log.Println("unmarshal clan info error", err.Error(), realData)
						return
					}
					if info.Info == nil {
						log.Println("clan info missing", realData)
						return
					}
					p.ClanName = info.Info.Name
					p.ClanID = info.Info.Id
					isBlacklisted := false
					if p.DB != nil {
						isBlacklisted = model.IsClanBlacklisted(p.DB, p.ClanID)
					} else {
						isBlacklisted = containsUint64(BLACK_LIST_CLAN, p.ClanID)
					}

					if isBlacklisted {
						p.Running = false
						p.notifyRunning()
						p.Notify = true
						p.sendNotify("Bang này đã bị khoá mõm, cút sang bang khác để được dùng bot")
						p.cancel()
						return
					}
					// log.Println("clan info", info.Member)
					for i := range info.Member {
						//log.Println(info.Member[i].Member.Id)
						// p.ClanMember[info.Member[i].Member.Id] = info.Member[i].Member.Name
						// log.Println(info.Member[i].LastActive)
						if info.Member[i] != nil && info.Member[i].Member != nil && p.DB != nil && info.Member[i].LastActive > 0 {
							// log.Println("member not online", info.Member[i].Member.Id)
							// p.Notify = true
							// p.sendNotify("member offline:" + info.Member[i].Member.Name)
							if _, ok := model.UserForAutoLogin(p.DB, info.Member[i].Member.Id); ok {
								log.Println("auto login", info.Member[i].Member.Name)
								p.AutoStartChan <- info.Member[i].Member.Id
							}
						}
					}
				}()
			case SERVER_DANGIOI_INFO, SERVER_ELIXIR_PLANT, SERVER_ELIXIR_WATER, SERVER_ELIXIR_GET, SERVER_ELIXIR_DEL, SERVER_ELIXIR_ELVES_INFO, SERVER_ELIXIR_DRAW_SEASON, SERVER_ELIXIR_PUSH_ORDER:
				go p.elixirDeliver(msgType, realData)

			case SERVER_XIYOU_DATA, SERVER_XIYOU_SYNC, SERVER_XIYOU_ATTACK, SERVER_XIYOU_RAND_EQUIP, SERVER_XIYOU_RESOLVE, SERVER_XIYOU_UPD_PROGRESS, SERVER_XIYOU_UPDATE_SEED,
				SERVER_XIYOU_SECRET_START, SERVER_XIYOU_SECRET_SELECT, SERVER_XIYOU_SECRET_KILL, SERVER_XIYOU_SECRET_SWEEP:
				go p.xiyouDeliver(msgType, realData)

			case SERVER_CLAN_INFO_2:
				go func() {
					var info proto.ClanInfoResponse
					err := pb.Unmarshal(realData, &info)
					if err != nil {
						return
					}
					// log.Println(info.ClanData.Member)
					if info.ClanData == nil {
						return
					}
					for i := range info.ClanData.Member {
						if info.ClanData.Member[i] != nil && info.ClanData.Member[i].Member != nil && p.DB != nil && info.ClanData.Member[i].LastActive > 0 {
							// log.Println("member not online", info.ClanData.Member[i].Member.Id)
							if _, ok := model.UserForAutoLogin(p.DB, info.ClanData.Member[i].Member.Id); ok {
								log.Println("auto login", info.ClanData.Member[i].Member.Id)
								p.AutoStartChan <- info.ClanData.Member[i].Member.Id
							}
						}
					}
				}()
			case SERVER_GIFT_CMD:
				go func() {
					var giftList proto.GiftList
					err := pb.Unmarshal(realData, &giftList)
					if err != nil {
						return
					}
					// log.Println(p.Username, "nhan gift", len(giftList.Entries))
					p.sendNotify("[" + strconv.Itoa(len(giftList.Entries)) + "] bú ngọc đạo hữu")
					for _, gift := range giftList.Entries {
						if gift == nil {
							continue
						}
						time.Sleep(time.Second * 3)
						var codeStruct proto.GiftCode
						codeStruct.Code = gift.GiftCode
						codeStruct.Status = true
						codeStruct.Type = gift.GiftType
						codeMSG, _ := pb.Marshal(&codeStruct)
						p.write(pack.Pack(RECV_GIFT_CMD, codeMSG, p.UID))
					}
				}()
			case SERVER_GIFT_CLAN_CMD:
				go func() {
					var giftList proto.GiftList
					err := pb.Unmarshal(realData, &giftList)
					if err != nil {
						log.Println("unmarshal gift list error", err.Error(), realData)
						return
					}
					// log.Println(p.Username, "nhan gift", len(giftList.Entries))
					p.sendNotify("[" + strconv.Itoa(len(giftList.Entries)) + "] bú ngọc đạo hữu")
					for _, gift := range giftList.Entries {
						if gift == nil {
							continue
						}
						time.Sleep(time.Second * 3)
						var codeStruct proto.PhatNgoc
						codeStruct.Msg = gift.GiftCode
						codeMSG, _ := pb.Marshal(&codeStruct)
						p.write(pack.Pack(RECV_GIFT_CLAN_CMD, codeMSG, p.UID))
					}
				}()
			case SERVER_CHUOT_DANGKEO_CMD:
				go func() {
					// hexString := hex.EncodeToString(realData)
					// log.Println("Hexadecimal string:", hexString)
					var dsChuot proto.DanhSachChuotDangkeo
					err := pb.Unmarshal(realData, &dsChuot)
					if err != nil {
						return
					}
					if dsChuot.Entries == nil {
						return
					}
					p.ChuotDoing = len(dsChuot.Entries)
					now := time.Now().UTC().UnixMilli()
					for _, chuot := range dsChuot.Entries {
						if chuot == nil {
							continue
						}
						if chuot.Timestamp > uint64(now)+60*60*1000*8 {
							// huy chuot
							var chayChuotStruct proto.ChayChuot
							chayChuotStruct.Id = chuot.Id
							chayChuotStruct.Offset = pb.Int32(chuot.Offset)
							chayChuotStruct.Oke = pb.Bool(false)
							chayChuotMSG, _ := pb.Marshal(&chayChuotStruct)
							p.write(pack.Pack(CHAYCHUOT_PHUCDIA_CMD, chayChuotMSG, p.UID))
							time.Sleep(time.Second)
							// chay lai chuot
							// chayChuotStruct.Oke = pb.Bool(true)
							// chayChuotMSG, _ = pb.Marshal(&chayChuotStruct)
							// p.write(pack.Pack(CHAYCHUOT_PHUCDIA_CMD, chayChuotMSG, p.UID))
						}
					}
				}()
			case SERVER_LIST_ALL_PHUCDIA_CMD:
				go func() {
					//hexString := hex.EncodeToString(realData)
					//log.Println("Hexadecimal string:", hexString)
					//log.Println(p.ChuotTotal)
					if p.ChuotDoing == p.ChuotTotal {
						return
					}
					var pdAll proto.AllPhucDia
					err := pb.Unmarshal(realData, &pdAll)
					if err != nil {
						return
					}
					if pdAll.Info == nil {
						return
					}
					arrBox := []Box{}
					minLevel := map[string]int{
						"bottle": p.ChuotMinBinh,
						"peach":  p.ChuotMinDao,
						"luuly":  p.ChuotMinLuuly,
					}
					// if p.ChuotHP == 11 {
					// 	minLevel = map[string]int{
					// 	"bottle": 1,
					// 	"peach":  3,
					// 	"luuly":  5,
					// 	}
					// }
					priority := map[string]int{
						"bottle": 1,
						"peach":  2,
						"luuly":  3,
					}
					//log.Println("ben canh co:", len(pdAll.Info.Phucdiabencanh))
					if pdAll.Info != nil {
						for _, pd := range pdAll.Info.Phucdiabencanh {
							if pd == nil || pd.Dangkeo || pd.Cinfo == nil {
								continue
							}
							arrBox = append(arrBox, Box{
								BoxID: pd.Cinfo.Id,
								Items: pd.Items,
							})
						}
					}

					//log.Println("box:", len(arrBox))
					top := GetTopItems(arrBox, minLevel, priority, 10, p.BottlePrefered)
					for _, c := range top {
						var chayChuotStruct proto.ChayChuot
						chayChuotStruct.Id = c.BoxID
						chayChuotStruct.Offset = pb.Int32(int32(c.Index))
						chayChuotStruct.Oke = pb.Bool(true)
						chayChuotMSG, _ := pb.Marshal(&chayChuotStruct)
						p.write(pack.Pack(CHAYCHUOT_PHUCDIA_CMD, chayChuotMSG, p.UID))
						time.Sleep(time.Second)
						// p.sendNotify("chuot keo " + c.Type + " level: " + strconv.Itoa(c.Level))
					}

					// for _, pd := range pdAll.Info.Phucdiadoidich {
					// 	if pd.Dangkeo {
					// 		continue
					// 	}
					// 	index := indexOfMaxInValueRange(pd.Items, 10009, 10012)
					// 	if index == -1 {
					// 		index = indexOfMaxInValueRange(pd.Items, 10032, 10033)
					// 	}
					// 	if index == -1 {
					// 		continue
					// 	}
					// 	p.sendNotify("chuot keo roi đạo hữu")
					// 	var chayChuotStruct proto.ChayChuot
					// 	chayChuotStruct.Id = pd.Cinfo.Id
					// 	chayChuotStruct.Offset = pb.Int32(int32(index))
					// 	chayChuotStruct.Oke = pb.Bool(true)
					// 	chayChuotMSG, _ := pb.Marshal(&chayChuotStruct)
					// 	p.write(pack.Pack(CHAYCHUOT_PHUCDIA_CMD, chayChuotMSG, p.UID))
					// }
				}()
			case SERVER_MY_MOUSE_INFO:
				//hexString := hex.EncodeToString(realData)
				//log.Println("Hexadecimal string:", hexString)
				var mouse proto.MouseInfo
				err := pb.Unmarshal(realData, &mouse)
				if err != nil {
					continue
				}
				p.ChuotTotal = int(mouse.Total)
				if p.ChuotTotal == 0 {
					p.ChuotTotal = int(mouse.Total)
				}
				if p.ChuotHP != int(mouse.Hp) {
					p.ChuotHP = int(mouse.Hp)
				}
			case SERVER_CHAT_CAY:
				go p.handleChatCay(realData)

			case SERVER_GET_UNDEAL_EQUIP:
				go p.handleUnDealEquip(realData)
			case SERVER_VENTURE_RESULT:
				go func() {
					var res proto.VentureResult
					if pb.Unmarshal(realData, &res) != nil {
						return
					}
					if res.Oke {
						p.sendNotify("dkm qua mạo hiểm rồi đạo hữu")
					}
				}()
			case SERVER_LUCDAO_RESULT_CMD:
				go func() {
					var res proto.LucDaoResult
					if pb.Unmarshal(realData, &res) != nil {
						return
					}
					if res.Value != "" {
						p.sendNotify("dkm qua lục đạo rồi đạo hữu")
					}
				}()
			case SERVER_YEUTHAP_RESULT_CMD:
				go func() {
					var res proto.YeuThap
					if pb.Unmarshal(realData, &res) != nil {
						return
					}
					if res.Oke {
						p.sendNotify("dkm qua yêu tháp rồi đạo hữu")
						return
					}
					if res.Error == 902 {
						var skillStruct proto.PickSkillYeuThap
						skillStruct.Skill = -1
						skillMSG, _ := pb.Marshal(&skillStruct)
						p.write(pack.Pack(PICK_SKILL_YEUTHAP_CMD, skillMSG, p.UID))
					}
				}()
			case SERVER_BANG_NHANQUA_SOINOI:
				go p.unionDailyHandleClaim(realData)
			}
		}
	}
}

func (p *Player) autoCheckInfoClan() {
	for {
		select {
		case <-p.ctx.Done():
			// log.Println(p.Username, "ngung check info clan")
			return
		default:
			p.write(pack.Pack(S_GET_CLAN_INFO, []byte{}, p.UID))
			time.Sleep(120 * time.Second)
		}
	}
}

func (p *Player) autoSendGift() {
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			var ldStruct proto.PhatNgoc
			ldStruct.Msg = "SendGift_Slogan_1"
			ldMSG, _ := pb.Marshal(&ldStruct)
			p.write(pack.Pack(S_SEND_GIFT, ldMSG, p.UID))
			time.Sleep(5 * time.Minute)
		}
	}
}

func (p *Player) autoChuot() {
	count := 0
	for {
		select {
		case <-p.ctx.Done():
			// log.Println(p.Username, "ngung chay chuot")
			return
		default:
			// log.Println("tong cuot", p.ChuotTotal)
			if !p.ChuotRun {
				time.Sleep(10 * time.Second)
				continue
			}
			var op = 0
			if p.BottlePrefered {
				op = 3
			}
			count += 1
			if count > 32 {
				if p.ChuotTotal > p.ChuotDoing+op {
					p.write(pack.Pack(S_HOMELAND_EXPLORE_REFRESH, []byte{}, p.UID))
					count = 0
					time.Sleep(3 * time.Second)
					p.sendNotify("Chuột đang chạy: " + strconv.Itoa(p.ChuotDoing) + " HP:" + strconv.Itoa(p.ChuotHP))
				}
			}
			p.write(pack.Pack(S_HOMELAND_MANAGE, []byte{}, p.UID))
			time.Sleep(4 * time.Second)
			p.write(pack.Pack(S_HOMELAND_EXPLORE, []byte{}, p.UID))
			time.Sleep(6 * time.Second)
		}
	}
}

func (p *Player) autoMaohiem() {
	for {
		select {
		case <-p.ctx.Done():
			// log.Println(p.Username, "ngung mao hiem")
			return
		default:
			// The level-100 orchestrator owns adventure pacing while it is on.
			// Running both loops sends duplicate VENTURE_CMD frames; the CLI
			// does not have a VIP level, which used to make this loop spin hot.
			if p.GrindRun {
				time.Sleep(3 * time.Second)
				continue
			}
			if !p.Maohiem {
				time.Sleep(10 * time.Second)
				continue
			}
			p.write(pack.Pack(VENTURE_CMD, []byte{}, p.UID))
			wait := 10 * time.Second
			if p.VipLevel > 1 {
				wait = 3 * time.Second
			}
			time.Sleep(wait)
		}
	}
}

func (p *Player) autoLucdao() {
	var ldStruct proto.LucDaoStart
	ldStruct.Oke = true
	ldMSG, _ := pb.Marshal(&ldStruct)
	for {
		select {
		case <-p.ctx.Done():
			// log.Println(p.Username, "ngung luc dao")
			return
		default:
			if !p.Lucdao {
				time.Sleep(10 * time.Second)
				continue
			}
			p.write(pack.Pack(S_LUCDAO_CMD, ldMSG, p.UID))
			if p.VipLevel > 1 {
				time.Sleep(3 * time.Second)
			}
			if p.VipLevel == 1 {
				time.Sleep(10 * time.Second)
			}
		}
	}
}

func (p *Player) autoYeuthap() {
	for {
		select {
		case <-p.ctx.Done():
			// log.Println(p.Username, "ngung mao hiem")
			return
		default:
			if !p.Yeuthap {
				time.Sleep(10 * time.Second)
				continue
			}
			p.write(pack.Pack(ATTACK_YEUTHAP_CMD, []byte{}, p.UID))
			if p.VipLevel > 1 {
				time.Sleep(3 * time.Second)
			}
			if p.VipLevel == 1 {
				time.Sleep(10 * time.Second)
			}
		}
	}
}

// autoPeach chops the tree (S_ATTRIBUTE_DREAM_MSG). Chops per request are the
// account's own dream-level cap (maxChopByDreamLv over the cached
// S_DREAM_DATA_SYNC level), not a fixed 10: a bigger number is rejected by
// the server, which silently froze chopping forever on lower-level accounts.
// Chopping also runs while the Peach toggle is off as long as an unclaimed
// union chop task is still short of its target (up to 350 chops), and stops
// the moment the server says the peach is gone or it is overloaded.
func (p *Player) autoPeach() {
	count := 0
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		// autoGrindLoop already chops with the dream-level cap and processes
		// equipment drops. Keep this older toggle loop from doubling its frames.
		if p.GrindRun {
			time.Sleep(3 * time.Second)
			continue
		}
		if !p.Peach && !p.taskDrivenChopOK() {
			count = 0
			time.Sleep(10 * time.Second)
			continue
		}
		SL := p.maxChopTimes()
		var ck proto.ChatCay
		ck.Zz = pb.Int32(1)
		ck.Oke = SL
		ckMSG, _ := pb.Marshal(&ck)

		prev := count
		count += int(SL)
		if count/100 != prev/100 {
			p.sendNotify("Chặt kây " + strconv.Itoa(count))
		}
		p.write(pack.Pack(S_CHAT_CAY, ckMSG, p.UID))
		time.Sleep(3 * time.Second)
	}
}

// taskDrivenChopOK: a pending union chop task may drive chopping even with
// the Peach toggle off, unless the server already rejected chopping outright.
func (p *Player) taskDrivenChopOK() bool {
	if !p.unionChopPending() {
		return false
	}
	p.dreamMu.Lock()
	defer p.dreamMu.Unlock()
	return !p.chopStop
}

func (p *Player) setChopStop(stop bool) {
	p.dreamMu.Lock()
	p.chopStop = stop
	p.dreamMu.Unlock()
}

// maxChopTimes is the cached dream level's cap; before the first
// S_DREAM_DATA_SYNC lands it answers 1 — the one value every level accepts.
func (p *Player) maxChopTimes() int32 {
	p.dreamMu.Lock()
	lv := p.dreamLv
	p.dreamMu.Unlock()
	return maxChopByDreamLv(lv)
}

// handleDreamSync handles the 00cf push (S_DREAM_DATA_SYNC, a login sync):
// it caches dreamLv — the chop-per-request cap, refreshed here on every
// level change — and cancels the pending drop equipment exactly as the old
// inline handler did (that list is field 2 of this same message, modelled by
// DoChuaHuyResponse).
func (p *Player) handleDreamSync(data []byte) {
	var dd proto.DreamDataMsg
	if err := pb.Unmarshal(data, &dd); err == nil && dd.DreamLv != nil {
		p.dreamMu.Lock()
		p.dreamLv = dd.GetDreamLv()
		first := !p.dreamKnown
		p.dreamKnown = true
		p.dreamMu.Unlock()
		if first {
			log.Printf("[%s] [Dream] dreamLv=%d (cap %d/request)", p.Username, dd.GetDreamLv(), maxChopByDreamLv(dd.GetDreamLv()))
		}
	}
	var ck proto.DoChuaHuyResponse
	err := pb.Unmarshal(data, &ck)
	if err != nil {
		log.Println(err.Error())
		return
	}
	var ids []int32
	for _, l := range ck.L1 {
		if l.L2 == nil {
			continue
		}
		ids = append(ids, l.L2.Id)
	}
	p.huyDoChatCay(ids) // no-op when the list is empty
	p.setPeachPending(ids)
}

// huyDoChatCay sends the destroy confirmations the server demands for every
// drop (S_ATTRIBUTE_EQUIPMENT_DEAL_MSG). Until one lands, its chop stays
// "undealt" and the next chop is rejected with 517/532.
func (p *Player) huyDoChatCay(ids []int32) {
	if len(ids) == 0 {
		return
	}
	var hd proto.HuyDoChatCay
	hd.Oke = 1
	hd.Id = append(hd.Id, ids...)
	hdMSG, _ := pb.Marshal(&hd)
	p.write(pack.Pack(S_HUY_DO_CHAT_CAY, hdMSG, p.UID))
}

// setPeachPending records the ids whose huỷ is awaiting proof of delivery:
// the next accepted chop is exactly that proof (the server only accepts with
// a clear board), so it replaces the list instead of growing it.
func (p *Player) setPeachPending(ids []int32) {
	p.dreamMu.Lock()
	p.peachPending = append([]int32(nil), ids...)
	p.dreamMu.Unlock()
}

func (p *Player) peachPendingIDs() []int32 {
	p.dreamMu.Lock()
	defer p.dreamMu.Unlock()
	return append([]int32(nil), p.peachPending...)
}

// handleChatCay answers one chop round (SERVER_CHAT_CAY). 517/532 mean drops
// from an EARLIER chop were never destroyed — the usual cause being a huỷ
// answer funtap dropped under load. Instead of giving up, re-send the huỷ for
// the tracked pending ids and ask the server for its authoritative pending
// list (same cleanup the 00cf login sync performs); the classic overload
// back-off now only triggers after 5 consecutive stalls that found nothing
// to clear.
func (p *Player) handleChatCay(data []byte) {
	var ck proto.ChatCayResponse
	if err := pb.Unmarshal(data, &ck); err != nil {
		return
	}
	if ck.Error != 0 {
		log.Println(p.Name, ck.Error)
		if ck.Error == 532 || ck.Error == 517 {
			p.huyDoChatCay(p.peachPendingIDs()) // retry any lost huỷ first
			p.dreamMu.Lock()
			p.errPeach++
			giveUp := p.errPeach > 5
			if giveUp {
				p.errPeach = 0
			}
			p.dreamMu.Unlock()
			if giveUp {
				p.Peach = false
				p.setChopStop(true) // even task-driven chopping must back off
				p.sendNotify("Lỗi huỷ đồ do funtap quá tải, đợi lát nữa /logout rồi /login lại để chặt kây tiếp nhé đạo hữu")
				return
			}
			p.write(pack.Pack(S_GET_UNDEAL_EQUIP, []byte{}, p.UID))
			return
		}
		if ck.Error == 101 {
			p.Peach = false
			p.setChopStop(true) // no peach: task chopping would just loop on 101
			p.sendNotify("hết đào rồi nhé đạo hữu")
			return
		}
		return
	}
	p.setChopStop(false) // any accepted chop re-arms chopping
	p.dreamMu.Lock()
	p.errPeach = 0 // the board was clear: stall streak broken
	p.dreamMu.Unlock()

	var ids []int32
	for _, l := range ck.L1 {
		for _, i := range l.Items {
			if i != "" {
				if strings.Contains(i, "100026") {
					p.sendNotify("bú 1 vé khiêu chiến")
					if p.XaVe {
						p.write(pack.Pack(S_LAM_MOI_DAU_PHAP, []byte{}, p.UID))
						time.Sleep(1 * time.Second)
						var muc proto.KhieuChienDauPhap
						muc.Id = pb.Int32(0)
						mucMSG, _ := pb.Marshal(&muc)
						p.write(pack.Pack(S_KHIEU_CHIEN_DAU_PHAP, mucMSG, p.UID))
					}
				}
			}
		}
		if l.L2 == nil || l.L2.L3 == nil {
			continue
		}
		ids = append(ids, l.L2.L3.Id)
	}
	if p.GrindRun {
		p.processDropEquipments(ids)
	} else {
		p.huyDoChatCay(ids)
	}
	p.setPeachPending(ids)
}

// handleUnDealEquip consumes the server's authoritative pending-drop list
// (GetUnDealEquipmentMsgResp — field-compatible with the 00cf list this same
// pipeline already clears at login). Items found here are exactly the ones
// wedging the chop loop: huỷ them now and reset the stall streak, because
// recovery is making progress. An empty answer clears nothing and leaves the
// back-off counter intact, so a genuine overload still retires after 5 tries.
func (p *Player) handleUnDealEquip(data []byte) {
	var ck proto.DoChuaHuyResponse
	if err := pb.Unmarshal(data, &ck); err != nil {
		return
	}
	var ids []int32
	for _, l := range ck.L1 {
		if l.L2 == nil {
			continue
		}
		ids = append(ids, l.L2.Id)
	}
	if len(ids) == 0 {
		return
	}
	log.Printf("[%s] [Chop] %d món đồ chưa huỷ bị sót, huỷ lại để chặt tiếp", p.Username, len(ids))
	p.huyDoChatCay(ids)
	p.setPeachPending(ids)
	p.dreamMu.Lock()
	p.errPeach = 0
	p.dreamMu.Unlock()
}

func (p *Player) autoThancot() {
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			p.write(pack.Pack(S_NHAN_THAN_COT, []byte{}, p.UID))
			time.Sleep(30 * time.Minute)
		}
	}
}

func (p *Player) autoNhanQuaCay() {
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			var muc proto.KhieuChienDauPhap
			muc.Id = pb.Int32(0)
			mucMSG, _ := pb.Marshal(&muc)
			p.write(pack.Pack(S_NHAN_QUA_CAY, mucMSG, p.UID))
			time.Sleep(30 * time.Minute)
		}
	}
}

func (p *Player) autoGiamCay() {
	var giamcay proto.GiamCay
	giamcay.Var1 = 1
	giamcay.Var2 = 1
	giamcay.Var3 = pb.Int32(0)
	giamcayMSG, _ := pb.Marshal(&giamcay)
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			p.write(pack.Pack(S_GIAM_CAY_1, giamcayMSG, p.UID))
			time.Sleep(1 * time.Second)
			p.write(pack.Pack(S_GIAM_CAY_2, []byte{}, p.UID))
			time.Sleep(31 * time.Minute)
		}
	}
}

func (p *Player) autoNhanTienDoTamBao() {
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			// Claim Thiền Đo Tầm Bảo, then help + one-key dig the treasure
			// auction 5 s later each. Fire-and-forget: the answers (84D7 /
			// 84f6) are ignored — when there is nothing to help or the
			// account has already dug everything the server simply refuses
			// (e.g. ret=12), which costs nothing.
			p.write(pack.Pack(S_TIENDO_TAMBAO, []byte{}, p.UID))
			time.Sleep(5 * time.Second)
			help := proto.TreasureAuctionHelpReq{Type: 1}
			helpMSG, _ := pb.Marshal(&help)
			p.write(pack.Pack(S_TREASUREAUCTION_HELPTREASURE, helpMSG, p.UID))
			time.Sleep(5 * time.Second)
			p.write(pack.Pack(S_TREASUREAUCTION_ONE_KEY_EXPLORE, []byte{}, p.UID))
			time.Sleep(120 * time.Minute)
		}
	}
}

// autoPupilTrain fires S_PUPIL_TRAIN (211802 = 00 03 3B 5A) every 3 min
// with index=0, which the server reads as TRAIN ALL SEATS. index is
// `optional` in the proto so the zero is really on the wire (body 08 00),
// distinct from "field absent". Blind fire: the answer (2E1A) is never
// read — nothing trainable is simply a ret≠0 that costs nothing, and
// per-seat attempts refit on PUPIL_ENERGY_RECOVER_TIME anyway.
func (p *Player) autoPupilTrain() {
	train := proto.PupilTrainReq{Index: pb.Int32(0)}
	trainMSG, _ := pb.Marshal(&train)
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			p.write(pack.Pack(S_PUPIL_TRAIN, trainMSG, p.UID))
			time.Sleep(3 * time.Minute)
		}
	}
}

func (p *Player) autoDoSomethings() {
	// like boss
	// var likeStruct proto.LikeTienCu
	// likeStruct.Id = 71054944131
	// likeStruct.Oke = true
	// likeMSG, _ := pb.Marshal(&likeStruct)
	// p.write(pack.Pack(LIKE_CMD, likeMSG, p.UID))

	// Tầm bảo bang is NOT fired here any more: the old code burst three
	// draws every login. autoUnionTreasure (player_paced_rewards.go) sends
	// the same call once per 30 min instead.

	// The free quay tinh quai / than thong spins are NOT fired here any
	// more: a login-time burst missed the daily quota whenever the account
	// was already past its 10 s ad cooldown, and re-logged accounts spammed
	// rejections. autoFreeSpin polls the per-day budget instead.

	// Rương sôi nổi bang is NOT fired here any more: the old code claimed
	// actIndex=4 blind at every login. autoUnionDaily (player_uniondaily.go)
	// sends the identical claim once per 6 h.

	// nhan quang cao tong mon — kept exactly as the old login hardcode: the
	// server silently drops it when nothing is free, so a blind pair at
	// login costs nothing extra.
	var tongmonquangcaoStruct proto.BangTreoThuongNhiemVu
	tongmonquangcaoStruct.Id = pb.Int32(0)
	tongmonquangcaoMsg, _ := pb.Marshal(&tongmonquangcaoStruct)
	p.write(pack.Pack(S_TONGMON_QUANGCAO, tongmonquangcaoMsg, p.UID))
	time.Sleep(time.Second * 3)
	p.write(pack.Pack(S_TONGMON_QUANGCAO, tongmonquangcaoMsg, p.UID))
	time.Sleep(time.Second * 3)

	// Nhiệm vụ quảng cáo bang (120006) is deliberately NOT handled here: the
	// old code re-marshalled the tong-mon message — still Id=0, the mutation
	// after Marshal never reached the bytes — onto 20503, which expects a
	// task-id list, and never sent the required ad-completion request at all.
	// watchAdTask (player_task.go) completes it via S_WATCH_AD_TASK first, and
	// the batch claim then picks it up like any other finished union task.
}

func (p *Player) sendNotify(msg string) {
	if p.Notify {
		name := p.Name
		if name == "" {
			// Login-failure notifications fire before the in-game nickname is
			// known; fall back to the login username so the line never starts
			// with a stray ": ".
			name = p.Username
		}
		p.NotiChan <- NotifyMGS{
			MSG:    name + ": " + msg,
			ChatID: p.ChatID,
		}
	}
}

// forceNotify delivers a message regardless of p.Notify. Regular Player
// sessions are built with Notify=false (telebot_auth.go) and only flip it via
// /notify or the web toggles — so a per-command engine run (e.g.
// /start_thanlong) that reports ONLY through sendNotify is totally silent for
// customers who never enabled notifications (customer report 2026-10-25
// "/start_thanlong nhưng không trả lời"). NotiChan is the shared t.notiChan,
// so this reaches the same chat. Like EventPlayer.forceNotify /
// TeleBot.pushNotify it never blocks: a full buffer drops the notice rather
// than wedging the game loops.
func (p *Player) forceNotify(msg string) {
	if p.NotiChan == nil {
		return
	}
	name := p.Name
	if name == "" {
		name = p.Username
	}
	select {
	case p.NotiChan <- NotifyMGS{MSG: name + ": " + msg, ChatID: p.ChatID}:
	default:
	}
}

func indexOfMaxInValueRange(arr []int32, minVal, maxVal int32) int {
	maxIndex := -1
	var currentMax int32 = -2147483648 // Smallest int32 value

	for i, num := range arr {
		if num >= minVal && num <= maxVal && num > currentMax {
			currentMax = num
			maxIndex = i
		}
	}

	return maxIndex
}

type Candidate struct {
	BoxID uint64
	Index int
	Type  string
	Level int
}

type Box struct {
	BoxID uint64
	Items []int32
}

// DecodeTypeID maps an ID -> (Type, Level, ok)
func DecodeTypeID(id int32) (string, int, bool) {
	switch {
	case id >= 10008 && id <= 10012:
		return "bottle", int(id) - 10007, true
	case id >= 10029 && id <= 10033:
		return "peach", int(id) - 10028, true
	case id >= 10015 && id <= 10019:
		return "luuly", int(id) - 10014, true
	default:
		return "", 0, false
	}
}

func GetTopItems(boxes []Box, minLevel, priority map[string]int, topN int, BottlePrefered bool) []Candidate {
	var candidates []Candidate

	for _, box := range boxes {
		for idx, it := range box.Items {
			typ, lvl, ok := DecodeTypeID(it)
			if !ok {
				continue
			}
			if min, has := minLevel[typ]; has && lvl >= min {
				if typ == "" {
					continue
				}
				candidates = append(candidates, Candidate{
					BoxID: box.BoxID,
					Index: idx,
					Type:  typ,
					Level: lvl,
				})
			}
		}
	}

	// sort by priority first, then level desc
	sort.Slice(candidates, func(i, j int) bool {
		if BottlePrefered {
			pi := priority[candidates[i].Type]
			pj := priority[candidates[j].Type]

			if pi != pj {
				return pi < pj // lower priority number = higher rank
			}
		}

		if candidates[i].Level != candidates[j].Level {
			return candidates[i].Level > candidates[j].Level
		}
		if candidates[i].BoxID != candidates[j].BoxID {
			return candidates[i].BoxID < candidates[j].BoxID
		}
		return candidates[i].Index < candidates[j].Index
	})

	// trim to topN
	if len(candidates) > topN {
		return candidates[:topN]
	}
	return candidates
}

func containsUint64(arr []uint64, target uint64) bool {
	for _, value := range arr {
		if value == target {
			return true
		}
	}
	return false
}
