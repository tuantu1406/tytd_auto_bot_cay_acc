package tytd_go

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	pb "google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/gorilla/websocket"
)

// EventPlayer is a clone of Player for event_user accounts (model.EventUser).
// It performs the same login/websocket/auth/keepalive flow but WITHOUT the
// auto-play goroutines Player.Run starts:
//
//	go p.autoDoSomethings()
//	go p.autoCheckInfoClan()
//	go p.autoChuot()
//	go p.autoMaohiem()
//	go p.autoYeuthap()
//	go p.autoPeach()
//	go p.autoThancot()
//	go p.autoLucdao()
//	go p.autoNhanQuaCay()
//	go p.autoGiamCay()
//	go p.autoSendGift()
//	go p.autoDanGioi()
//	go p.autoNhanTienDoTamBao()
//
// An event account just stays online; eventWsHandler processes event messages
// only. For God Island it stays resident in the activity (godIslandStay) and
// keeps the plant garden tidy on its own (liquid collect/donate, fruit claim —
// giPlantCycle); it never picks targets or attacks on its own — moving and
// fighting happen only when the leader orders it (/bonglai_* for Đảo Bồng
// Lai, /thienma_* for Đảo Thiên Ma — the two God Island events share one
// protocol and can be open simultaneously, see giEvent). Fields used
// exclusively by the removed auto functions
// (VIP-scaled timers, chuot state, peach/maohiem toggles, clan member cache)
// are dropped from the clone.
type EventPlayer struct {
	Username  string
	Password  string
	ServerID  int
	ChatID    int64
	UID       uint64
	Name      string
	ClanID    uint64
	ClanName  string
	Running   bool
	Notify    bool
	NotiChan  chan NotifyMGS
	conn      *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	writeChan chan []byte
	DB        *gorm.DB

	// dbID/nameSaved: same in-game-name bookkeeping as Player (the event
	// reader is a single goroutine, so nameSaved needs no lock).
	dbID      string
	nameSaved string

	// deputy: this session runs on a USERS row (an account delegated for
	// events only, see model.User.DeputyOf) instead of an event_users row —
	// storeIngameName must write the users table, not event_users. Normal
	// event accounts leave it false.
	deputy bool

	// ActList is this account's live activity list (see player_activity.go):
	// filled by the login push (21001), upserted by 21036, refreshed hourly
	// by 21003 pulls via activityRefresh. Fully self-synchronized.
	ActList activityStore

	// Luân Hồi Bí Cảnh (RebornTrial 172) — event-account clone of the Player
	// engine (player_event_reborntrial.go). NO DB opt-in: Run() starts the
	// loop the moment the account is online. RbtMu guards the config folded
	// from the 21003 answer (rbtMergeConfig below is an EventPlayer method
	// mirroring the Player one; the reader calls it inside the
	// SERVER_ACTIVITY_DETAIL case). rbtChans is the request/answer seat map
	// (answer-hex → seat) used by rbtRT; the loop goroutine is the only
	// caller. Loop-owned state (worship/auto/qual/mall/cond) matches the
	// Player fields one-for-one — see player.go for the semantics.
	RbtMu    sync.Mutex
	rbtChans map[string]chan []byte

	// Thương Minh Yêu Vực (CelestialSeaWar 349) — event-account clone of
	// the Player engine (player_event_seawar.go), sharing the swPlayTick
	// core. NO DB opt-in: Run() starts the loop the moment the account is
	// online. SwMu guards the 21003-folded tables + once-per-day keys;
	// swChans is the answer-hex seat map used by swRT (the loop goroutine
	// is the only caller; the /thuongminh_suc button is fire-and-forget).
	SwMu               sync.Mutex
	swChans            map[string]chan []byte
	swEnergyItem       int32
	swBallEffect       map[int32]int32
	swBallType         map[int32]int32
	swRegionEnergy     map[int32]int32
	swRegionCond       map[int32]string
	swEventCfgIds      map[int32]bool // SeaWarEventConfigMsgs ids (consumeCondition classifier)
	swWorshipDay       string
	swExhaustKey       string
	swHelpBoxNeed      int32
	swHighPvp          int32 // SEAWAR_HIGH_PVP_TIMES — attacks per lệnh cao cấp (lệnh thường = 1)
	swMallRows         []*proto.ActivityMallTempMsg
	swMallBought       map[int32]int64
	swLiquidItems      []int32                          // the 3 linh lộ donate ids (SEAWAR_ITEM_ENERGY fold, else baked 105531/105532/105533)
	swCondCfgs         []*proto.ActivityConditionConfig // nạp-bang rungs → 21045
	swCondNext         time.Time                        // sweep cadence gate
	swDonatePauseUntil time.Time                        // donate-burst park (swDonateLiquid v2)
	swAttacks          []swAttackRec                    // war-map fight ring (234932 pushes)
	swMallDay          string
	swMallAdds         map[int32]int64
	swShopFails        map[int32]time.Time
	swShopDay          string
	swWake             chan struct{}

	// Leader off-switches (tab Sự kiện, 2026-11-09): seeded from the
	// EventUser row at login, patched live by webEventAction config —
	// single bools, loop reads lock-free (same law as Player Running gates).
	OffRbt    bool // Luân Hồi Bí Cảnh 172
	OffSeaWar bool // Thương Minh Yêu Vực 349

	// "Cắn quả" web card (tab Sự kiện, owner 2026-11-09): swEatBusy guards
	// one eat-all loop per account (presses while eating are no-ops);
	// swEatMute silences the per-refusal alert while the loop runs — the
	// server's stop-when-empty verdict is expected, not an incident.
	swEatBusy atomic.Bool
	swEatMute atomic.Bool

	rbtPoll      time.Duration
	rbtStatusGap time.Duration
	rbtTreeGap   time.Duration

	rbtDailyOpenSec  int
	rbtDailyCloseSec int
	rbtTimeCfg       [][3]int

	rbtWorshipped   map[string]bool
	rbtQualNotified string
	rbtAutoUsed     string

	rbtItemMagic   map[int32]int32
	rbtRepairLimit []int32
	rbtFakeLimit   []int32

	rbtMallRows   []*proto.ActivityMallTempMsg
	rbtMallBought map[int32]int64
	rbtMallAdds   map[int32]int64
	rbtMallDay    string

	rbtCondCfgs []*proto.ActivityConditionConfig
	rbtCondData map[int32]*proto.ActivityConditionData
	rbtCondGot  map[int32]bool
	// Ticket item id folded from the activity config (kept for parity with
	// the shared rbtMergeConfig; the ticket command itself lives on the main
	// Player — event accounts attack through their own union bracket).
	rbtChallengeItem int32
	rbtBattleNum     int32
	rbtFreeAttempts  int32
	rbtBattleRecov   int64
	rbtRoundNo       int32
	// rbtEvSeq: tinhnang.jpg option-cycle cursors per event type — only the
	// march loop reads/writes it, no lock needed.
	rbtEvSeq map[int32]int
	// Energy fruit (auto eat, owner-authorised 2026-10-09 [[tytd-item-ban]]):
	// 0 item = NEVER eat. Guard with RbtMu.
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

	// God Island state: one giEvent slot per event variant (Bồng Lai and
	// Thiên Ma run the same protocol concurrently, so NOTHING is stored
	// per-account). Written by eventWsHandler (reader goroutine), read by
	// godIslandStay and the GodIsland* commands; guard every access —
	// including the slots' fields — with GodIslandMu.
	GodIslandMu sync.Mutex
	GiEvents    [giEventCount]*giEvent
	// giMiniPending is the one-shot web-map seat (RequestGodIslandMiniMap),
	// guarded by GodIslandMu — the streamer pulls island minimaps through
	// event accounts when the leader's own session is offline.
	giMiniPending chan []byte
	// giCityPending is the one-shot seat of the people-in-city pull
	// (RequestGodIslandCityInfo, 206823 → 1aa7), guarded by GodIslandMu —
	// the /events/mapview twin for the islands feeds it on camera stop.
	giCityPending chan []byte
	// giNames is the persistent playerId→name cache (1aa6 marches, 1ac0
	// battle lines, 1abf win banners all carry real names; the standing
	// roster does not). Guarded by GodIslandMu; see giFoldName.
	giNames map[int64]giNameRec
	// Response attribution: most God Island responses do not echo the
	// activityId, but the server answers one connection strictly in request
	// order — so each tracked request enqueues its event here and the next
	// response of that type belongs to the oldest entry. giSendMu pairs the
	// enqueue with the frame write so queue order matches wire order.
	giPending map[string][]*giEvent
	giSendMu  sync.Mutex

	// Holy Land (Cửu U) state: NO activityId exists anywhere in that
	// protocol, so an account carries exactly ONE context — no slots, no
	// FIFO attribution. Written by eventWsHandler and holyLandStay, read by
	// the HolyLand* commands; guard every field with HolyMu. hlOpen flags the
	// activity-list announcement (type 346); the schedule's isOpen (in hl)
	// is the authoritative gate.
	HolyMu   sync.Mutex
	hl       *holyLandState
	hlOpen   bool
	hlSendMu sync.Mutex
	// hlMapPending is the one-shot seat for a web map pull
	// (RequestHolyLandMiniMap), guarded by HolyMu — the event account is
	// usually the only resident of the Cửu U battlefield, so the leader's
	// /events page streams the map through it. hlRoutePending/hlLinePending
	// are the sibling seats for the 209522 route snapshot and the 209524
	// city queue (marching animation + city click on the same page).
	hlMapPending   chan []byte
	hlRoutePending chan []byte
	hlLinePending  chan []byte

	// Ball GVG (Thượng Cổ Di Tích) camp-vs-camp state. EVERY message echoes
	// the live activityId (learned from the activity list, type 14), so
	// responses attribute directly — no slots, no FIFO queue. Written by
	// eventWsHandler and bgStay, read by the BallGVG* commands; guard every
	// field with BgMu.
	BgMu     sync.Mutex
	bg       *ballGVGState
	bgSendMu sync.Mutex
	// bgMiniPending is the one-shot web-map seat (RequestBallGVGMap),
	// guarded by BgMu — Di Tích streams through event accounts too.
	bgMiniPending chan []byte

	// KunlunWar (Côn Lôn Tranh Bá, type 199) — the minimal SCENE
	// TRANSPORT that /kunlun_follow needs: EnterGame + 15s heartbeat, own
	// position from 219933, Move orders. No map cache, no autopilot here
	// (the board lives on the leader's Player.ku). Written by
	// eventWsHandler and kuEnsureScene, read by KunlunWar* orders; guard
	// with KuMu.
	//
	// warKindEP ("ku"|"bm", empty = ku) is the event-account twin of
	// Player.warKind: Đỉnh Bắc Minh answers on the SAME SERVER_KUNLUN_*
	// hexes (server routes by activityId only), so a session seats in ONE
	// war scene at a time and the reader dispatches by this flag. Set on
	// EnterActivity/EnterGame success of each event, never needs clearing
	// — the other event's join flips it back. Lock order across the two
	// boards: BmMu → KuMu, never reverse.
	KuMu      sync.Mutex
	ku        *kuEventState
	kuSendMu  sync.Mutex
	warKindEP string

	// Đỉnh Bắc Minh (type 332) — the SAME scene transport on the twin
	// board; guard with BmMu (see warKindEP for the seating invariant).
	BmMu     sync.Mutex
	bm       *bacminhEventState
	bmSendMu sync.Mutex

	// OnRunningChange mirrors Player: the web SSE push hook (see there).
	OnRunningChange func()
}

// eatPendingEP reports whether a "cắn quả" bite is in flight on each board —
// the 4dd6 verdict router (see the reader) delivers to the board that fired
// the bite, because the fire-and-forget eat send never flips warKindEP.
// Each board is read under its own lock, never nested.
func (p *EventPlayer) eatPendingEP() (kuPend, bmPend bool) {
	p.KuMu.Lock()
	kuPend = p.ku != nil && p.ku.eatSendNum > 0
	p.KuMu.Unlock()
	p.BmMu.Lock()
	bmPend = p.bm != nil && p.bm.eatSendNum > 0
	p.BmMu.Unlock()
	return
}

// notifyRunning mirrors Player.notifyRunning — fire-and-forget web push.
func (p *EventPlayer) notifyRunning() {
	if p.OnRunningChange != nil {
		fn := p.OnRunningChange
		go fn()
	}
}

// storeIngameName mirrors the sanitized nickname into this account's
// event_users row (reader goroutine only; see Player.storeIngameName).
func (p *EventPlayer) storeIngameName() {
	if p.DB == nil || p.dbID == "" || p.Name == "" || p.Name == p.nameSaved {
		return
	}
	// A delegated event account lives on a users row (model.User.DeputyOf), so
	// its nickname persists there; a normal event account writes event_users.
	var err error
	if p.deputy {
		err = model.UpdateUserIngameName(p.DB, p.dbID, p.Name)
	} else {
		err = model.UpdateEventUserIngameName(p.DB, p.dbID, p.Name)
	}
	if err != nil {
		log.Printf("[%s] lưu tên trong game thất bại: %v", p.Username, err)
		return
	}
	p.nameSaved = p.Name
}

func (p *EventPlayer) doLogin() (string, string, error) {
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
	// event_user logs in only when the leader calls it (no clan-based auto
	// login), so the game UID is kept in memory only, never persisted.
	// Remember the row key first — it is the session id while p.UID still
	// holds it (see Player.dbID).
	p.dbID = fmt.Sprintf("%d", p.UID)
	p.UID = pid

	return token, wsURL, nil
}

func (p *EventPlayer) login() (string, string, error) {
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

// write is the ctx-aware clone of Player.write — see the finding #1 comment
// there (wedged socket used to leak every sender forever).
func (p *EventPlayer) write(msg []byte) {
	if p.ctx == nil {
		return
	}
	select {
	case p.writeChan <- msg:
	case <-p.ctx.Done():
	}
}

func (p *EventPlayer) doWrite() {
	for {
		select {
		case <-p.ctx.Done():
			log.Println(p.Username, "server disconnected")
			return
		case msg := <-p.writeChan:
			p.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			err := p.conn.WriteMessage(2, msg)
			if err != nil {
				p.cancel()
				log.Println(p.Username, "can't write, disconnect")
				return
			}
		}
	}
}

func (p *EventPlayer) Run() bool {
	token, wsURL, err := p.login()
	if err != nil {
		log.Printf("[event %s] ĐĂNG NHẬP THẤT BẠI (leader chat %d, server %d): %v", p.Username, p.ChatID, p.ServerID, err)
		p.Notify = true
		p.sendNotify("đăng nhập thất bại: " + err.Error())
		return false
	}
	p.Running = true
	p.notifyRunning()
	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	c, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		log.Printf("[event %s] KẾT NỐI WEBSOCKET THẤT BẠI (leader chat %d, server %d): %v", p.Username, p.ChatID, p.ServerID, err)
		p.Running = false
		p.notifyRunning()
		return false
	}
	defer c.Close()
	c.SetPingHandler(func(string) error {
		return c.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})
	c.SetPongHandler(func(string) error {
		return c.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.conn = c
	p.ctx = ctx
	p.cancel = cancel
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

	go p.eventWsHandler()
	time.Sleep(5 * time.Second)
	// event_user: the auto* goroutines from Player.Run are intentionally not
	// started here (see the EventPlayer doc comment for the full list). The
	// only background work is staying resident in the God Island, Holy Land
	// and Ball GVG events; God Island / Holy Land combat comes from leader
	// commands only, while Ball GVG auto-walks and auto-strikes inside
	// battlefield rooms (bgAuto — the stock client's own behaviour).
	go p.godIslandStay()
	go p.holyLandStay()
	go p.bgStay()
	go p.bgAuto()
	go p.activityRefresh()
	// Luân Hồi Bí Cảnh: no opt-in switch for event accounts — being logged in
	// IS the consent (owner 2026-10-09: "user event chỉ cần login là chạy").
	go p.rebornTrialEventLoop()
	go p.seaWarEventLoop()

	p.wsKeepAlive()
	return true
}

func (p *EventPlayer) wsKeepAlive() {
	for {
		select {
		case <-p.ctx.Done():
			log.Println(p.Username, "server disconnected")
			return
		default:
			time.Sleep(5 * time.Second)
			p.write(pack.Pack(PING_CMD, []byte{}, p.UID))
		}
	}
}

// eventWsHandler is the cloned wsHandler for event_user accounts. It keeps the
// connection drained (so the server never sees a stuck client) but, by design,
// reacts to event messages only; all regular game traffic is ignored.
func (p *EventPlayer) eventWsHandler() {
	for {
		select {
		case <-p.ctx.Done():
			log.Println("[EventReader] Context done", p.Username)
			return
		default:
			p.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
			_, message, err := p.conn.ReadMessage()
			if err != nil {
				log.Printf("[EventReader] Read error: %v", err)
				p.cancel() // trigger shutdown
				return
			}
			isMSG, msgType, realData := pack.Unpack(message)
			if !isMSG {
				continue
			}

			switch msgType {
			case "0065":
				var info proto.PlayerInfo
				if err := pb.Unmarshal(realData, &info); err != nil {
					continue
				}
				// Same nickname pipeline as the regular reader (see
				// model.SanitizeIngameName), stored in event_users.
				p.Name = model.SanitizeIngameName(info.UnicodeStr)
				p.storeIngameName()

			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
				p.giHandleActivityList(realData)
				// The same pushes announce Holy Land (activity type 346);
				// only full lists may withdraw the announcement.
				p.hlHandleActivityList(realData, msgType == SERVER_ACTIVITY_LIST)
				// …and Ball GVG (activity type 14 → live activityId).
				p.bgHandleActivityList(realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_GOD_ISLAND_MINIMAP:
				p.giDeliverMiniMap(realData) // web map seat (player_event_mapstream.go)
			// People-on-map feeds (player_event_gipeople.go): 1aa6 marches and
			// 1abe/1ace city pushes carry no activityId → giClaim policy;
			// 1aa7 answers the mapview seat, claimed via the giSend FIFO.
			case SERVER_GOD_ISLAND_ROUTE_SYNC:
				p.giHandleRouteSync(realData)
			case SERVER_GOD_ISLAND_CITY_INFO:
				p.giRoute(SERVER_GOD_ISLAND_CITY_INFO, realData, (*EventPlayer).giHandleCityInfo)
			case SERVER_GOD_ISLAND_CITY_CHANGE:
				p.giHandleCityChange(realData)
			case SERVER_GOD_ISLAND_CITY_BATTLE:
				p.giHandleCityBattle(realData)
			// Named pushes (1ac0 battle lines, 1abf win banners): folded
			// into the persistent giNames cache so standing people resolve
			// to real names, not ids.
			case SERVER_GOD_ISLAND_LINE_SYNC:
				p.giHandleLineSync(realData)
			case SERVER_GOD_ISLAND_PLAYER_WIN:
				p.giHandlePlayerWin(realData)
			case SERVER_GOD_ISLAND_BASE_INFO:
				p.giHandleBaseInfo(realData) // self-attributes via its activityId
			case SERVER_GOD_ISLAND_INFO_LOAD:
				p.giRoute(SERVER_GOD_ISLAND_INFO_LOAD, realData, (*EventPlayer).giHandleGameInfo)
			case SERVER_GOD_ISLAND_BALL_INFO:
				p.giRoute(SERVER_GOD_ISLAND_BALL_INFO, realData, (*EventPlayer).giHandleBallInfo)
			case SERVER_GOD_ISLAND_USE_FRUIT:
				p.giRoute(SERVER_GOD_ISLAND_USE_FRUIT, realData, (*EventPlayer).giHandleUseFruitResp)
			case SERVER_GOD_ISLAND_USE_BALL:
				p.giRoute(SERVER_GOD_ISLAND_USE_BALL, realData, (*EventPlayer).giHandleUseBallResp)
			case SERVER_GOD_ISLAND_ACCELERATE:
				p.giRoute(SERVER_GOD_ISLAND_ACCELERATE, realData, (*EventPlayer).giHandleAccelerateResp)
			case SERVER_ACTIVITY_DETAIL:
				// Every 21003 answer (event pulls and the hourly activity
				// poll alike) re-carries its mainConfig — fold it into the
				// stored list first, then let the event owners consume it:
				// 21003 is shared, God Island attributes its answer via the
				// FIFO queue, but Ball GVG's answer names its activityId —
				// route that one straight to the Ball GVG slot (bgActivityDetail).
				p.ActList.mergeDetail(p.Username, realData)
				// The Luân Hồi Bí Cảnh engine folds its own slice of the same
				// answer (mall + milestone + phase tables) and takes delivery
				// of the raw bytes if one of its seats waits on 21003.
				p.rbtMergeConfig(realData)
				p.rbtHandleFrameEP(SERVER_ACTIVITY_DETAIL, realData)
				// Thương Minh Yêu Vực folds its forge/region tables from the
				// same shared answer (type-guarded inside) — and its shop
				// sweep may hold the 21003 seat too.
				p.swMergeConfig(realData)
				p.swHandleFrameEP(SERVER_ACTIVITY_DETAIL, realData)
				p.bgActivityDetail(realData, func() {
					p.giRoute(SERVER_ACTIVITY_DETAIL, realData, (*EventPlayer).giHandleActivityDetail)
				})
			case SERVER_GOD_ISLAND_PLANT_INFO:
				p.giRoute(SERVER_GOD_ISLAND_PLANT_INFO, realData, (*EventPlayer).giHandlePlantInfo)
			case SERVER_GOD_ISLAND_PLANT_WATER:
				p.giRoute(SERVER_GOD_ISLAND_PLANT_WATER, realData, (*EventPlayer).giHandlePlantWater)
			case SERVER_GOD_ISLAND_RECEIVE_FRUITS:
				p.giRoute(SERVER_GOD_ISLAND_RECEIVE_FRUITS, realData, (*EventPlayer).giHandleReceiveFruits)
			case SERVER_GOD_ISLAND_SEND_LIQUID:
				p.giRoute(SERVER_GOD_ISLAND_SEND_LIQUID, realData, (*EventPlayer).giHandleSendLiquid)
			// pushes (1ac2/1ac9/1acd/1acf) all carry the activityId — strict
			// lookup, foreign events silently ignored.
			case SERVER_GOD_ISLAND_MYGAMEINFO:
				p.giHandleMyGameInfoSync(realData)
			case SERVER_GOD_ISLAND_LEFT_OUT:
				p.giHandleLeftOut(realData)
			case SERVER_GOD_ISLAND_BEEN_KILL:
				p.giHandleBeenKillSync(realData)
			case SERVER_GOD_ISLAND_AUTO_STOP:
				p.giHandleAutoBattleStop(realData)
			// Holy Land (Cửu U): no activityId anywhere in this protocol,
			// so every frame maps straight to the account's one context.
			case SERVER_HOLY_LAND_MINIMAP:
				p.hlDeliverMiniMap(realData)
			case SERVER_HOLY_LAND_CITY_INFO:
				p.hlDeliverRoutes(realData)
			case SERVER_HOLY_LAND_TARGET_LINE:
				p.hlDeliverLine(realData)
			case SERVER_HOLY_LAND_SCHEDULE:
				p.hlHandleSchedule(realData)
			case SERVER_HOLY_LAND_BASE_INFO:
				p.hlHandleBaseInfo(realData)
			case SERVER_HOLY_LAND_GAME_INFO:
				p.hlHandleGameInfo(realData)
			case SERVER_HOLY_LAND_MOVE:
				p.hlHandleMoveRet(realData)
			case SERVER_HOLY_LAND_ATTACK:
				p.hlHandleAttackRet(realData)
			case SERVER_HOLY_LAND_AUTO_ATK:
				p.hlHandleAutoAtk(realData)
			case SERVER_HOLY_LAND_ACCELERATE:
				p.hlHandleAccelerate(realData)
			case SERVER_HOLY_LAND_BACK_HOME:
				p.hlHandleBackHome(realData)
			case SERVER_HOLY_LAND_WORSHIP:
				p.hlHandleWorship(realData)
			case SERVER_HOLY_LAND_RANK_REWARD:
				p.hlHandleRankRewardState(realData)
			case SERVER_HOLY_LAND_CLAIM_REWARD:
				p.hlHandleClaimReward(realData)
			case SERVER_HOLY_LAND_MYGAMEINFO:
				p.hlHandleMyGameInfoSync(realData)
			case SERVER_HOLY_LAND_OCCUPY_CITY:
				p.hlHandleOccupyCity(realData)
			case SERVER_HOLY_LAND_LEFT_OUT:
				p.hlHandleLeftOut(realData)
			case SERVER_HOLY_LAND_BEEN_KILL:
				p.hlHandleBeenKillSync(realData)
			case SERVER_HOLY_LAND_AUTO_STOP:
				p.hlHandleAutoStop(realData)
			case SERVER_HOLY_LAND_SCHEDULE_PUSH:
				p.hlHandleSchedulePush(realData)
			// Ball GVG (Thượng Cổ Di Tích): every frame names its activityId,
			// so frames for other activity instances are dropped by lookup.
			case SERVER_BALL_GVG_ENTER_ACT:
				p.bgHandleEnterAct(realData)
			case SERVER_BALL_GVG_ENTER_ROOM:
				p.bgHandleEnterRoom(realData)
			case SERVER_BALL_GVG_LEAVE_ROOM:
				p.bgHandleLeaveRoom(realData)
			case SERVER_BALL_GVG_MOVE:
				p.bgHandleMoveRet(realData)
			case SERVER_BALL_GVG_ATTACK:
				p.bgHandleAttackRet(realData)
			case SERVER_BALL_GVG_MARK:
				p.bgHandleMarkRet(realData)
			case SERVER_BALL_GVG_GET_PLACE:
				p.bgHandlePlaceInfo(realData)
				p.bgDeliverMap(realData) // also feed the web map seat (no-op without one)
			case SERVER_BALL_GVG_SYNC_FIGHT:
				p.bgHandleFightSync(realData)
			case SERVER_BALL_GVG_SYNC_SCORE:
				p.bgHandleScoreSync(realData)
			case SERVER_BALL_GVG_SYNC_PLACE:
				p.bgHandlePositionSync(realData)
			case SERVER_BALL_GVG_SYNC_CAMP:
				p.bgHandleCampSync(realData)
			case SERVER_BALL_GVG_SYNC_BEATEN:
				p.bgHandleBeatenSync(realData)
			case SERVER_BALL_GVG_SYNC_SEIZE:
				p.bgHandleSeizeSync(realData)
			case SERVER_BALL_GVG_SYNC_JOIN:
				p.bgHandleJoinSync(realData)
			case SERVER_BALL_GVG_SYNC_DROP:
				p.bgHandleDropSync(realData)
			case SERVER_BALL_GVG_END_DATA:
				p.bgHandleEndData(realData)
			// KunlunWar engine (Côn Lôn type 199 + Đỉnh Bắc Minh type 332) —
			// BOTH boards answer with these exact hexes; the session's
			// warKindEP says which one it currently seats in (transport
			// invariant: one war scene per account). /kunlun_follow and
			// /bacminh_follow frames plus the request answers behind the
			// event-account minimap pulls (219901/219919 → seats):
			// event-only fleets stream through these sessions (bug report
			// 2026-10-05).
			case SERVER_KUNLUN_ENTER_ACTIVITY, SERVER_KUNLUN_MINI_MAP_INFO,
				SERVER_KUNLUN_GAME_BLOCK_INFO, SERVER_KUNLUN_ENTER_GAME,
				SERVER_KUNLUN_MY_GAME_SYNC, SERVER_KUNLUN_ROUTE_SYNC,
				SERVER_KUNLUN_LINE_SYNC, SERVER_KUNLUN_SPAR_INFO,
				SERVER_KUNLUN_SPAR_POWER, SERVER_KUNLUN_GET_WELFARE,
				SERVER_KUNLUN_USE_ITEM, SERVER_KUNLUN_MOVE,
				SERVER_KUNLUN_EXPLORE, SERVER_KUNLUN_SWEEP,
				SERVER_KUNLUN_ACCELERATE, SERVER_KUNLUN_BEEN_KILL_SYNC,
				SERVER_KUNLUN_BACK_HOME:
				// The eat verdict (4dd6) goes to the board that has a bite
				// IN FLIGHT, not to whatever board warKindEP last seated:
				// the fire-and-forget eat send never flips warKindEP (flipping
				// it would mis-route OTHER in-flight round-trips), so a north
				// bite by an account seated on the kunlun board used to fold
				// its verdict into the wrong state — the north chase never
				// cleared and its 🍈 button died silently forever (customer
				// report 2026-11-04 "bắc minh và côn lôn nút cắn quả không
				// work"). eatPendingEP answers under each board's own lock.
				if msgType == SERVER_KUNLUN_USE_ITEM {
					kuPend, bmPend := p.eatPendingEP()
					if bmPend != kuPend {
						if bmPend {
							p.bmHandleUseItem(realData)
						} else {
							p.kuHandleUseItem(realData)
						}
						break
					}
				}
				p.KuMu.Lock()
				wk := p.warKindEP
				p.KuMu.Unlock()
				if wk == warKindBacMinh {
					p.bmDeliver(msgType, realData)
					break
				}
				switch msgType {
				case SERVER_KUNLUN_ENTER_ACTIVITY:
					p.kuDeliverFrame(SERVER_KUNLUN_ENTER_ACTIVITY, realData)
				case SERVER_KUNLUN_MINI_MAP_INFO:
					p.kuDeliverFrame(SERVER_KUNLUN_MINI_MAP_INFO, realData)
				case SERVER_KUNLUN_GAME_BLOCK_INFO:
					p.kuDeliverFrame(SERVER_KUNLUN_GAME_BLOCK_INFO, realData)
				case SERVER_KUNLUN_ENTER_GAME:
					p.kuHandleEnterGame(realData)
				case SERVER_KUNLUN_MY_GAME_SYNC:
					p.kuHandleMyGameInfo(realData)
				case SERVER_KUNLUN_ROUTE_SYNC:
					p.kuFoldRoutes(realData)
				case SERVER_KUNLUN_LINE_SYNC:
					p.kuFoldLines(realData)
				case SERVER_KUNLUN_SPAR_INFO:
					p.kuDeliverFrame(SERVER_KUNLUN_SPAR_INFO, realData)
				case SERVER_KUNLUN_SPAR_POWER:
					p.kuHandleSparPower(realData)
				case SERVER_KUNLUN_GET_WELFARE:
					p.kuHandleWelfare(realData)
				case SERVER_KUNLUN_USE_ITEM:
					p.kuHandleUseItem(realData)
				case SERVER_KUNLUN_MOVE:
					p.kuHandleMove(realData)
				case SERVER_KUNLUN_EXPLORE:
					p.kuHandleExplore(realData)
				case SERVER_KUNLUN_SWEEP:
					p.kuHandleSweep(realData)
				case SERVER_KUNLUN_ACCELERATE:
					p.kuHandleAccelerate(realData)
				case SERVER_KUNLUN_BEEN_KILL_SYNC:
					p.kuHandleBeenKill(realData)
				case SERVER_KUNLUN_BACK_HOME:
					p.kuHandleBackHome(realData)
				}
			// Luân Hồi Bí Cảnh: every 172 answer plus the generic activity
			// shop/milestone answers ride seats claimed by rbtRT (the auto
			// loop is the only caller; one in-flight request at a time).
			case SERVER_RBT_ENTER_ACTIVITY, SERVER_RBT_ENTER, SERVER_RBT_WORSHIP, SERVER_RBT_EXPLORE,
				SERVER_RBT_SELECT_OPTION, SERVER_RBT_SKIP_EVENT, SERVER_RBT_BONUS, SERVER_RBT_BONUS_RESULT, SERVER_RBT_BONUS_ITEM,
				SERVER_RBT_GUESS_DATA, SERVER_RBT_GUESS_REWARD, SERVER_RBT_AUTO_EXPLORE, SERVER_RBT_USE_MAGIC,
				SERVER_RBT_BATTLE_ENTER, SERVER_RBT_BATTLE_HEART, SERVER_RBT_BATTLE_CHALLENGE,
				SERVER_RBT_BATTLE_UNIONPLAYER, SERVER_RBT_BATTLE_NOTIC, SERVER_RBT_USE_ITEM,
				SERVER_RBT_SEND_BONUS_ITEM,
				SERVER_ACTIVITY_COND_REWARD, SERVER_ACTIVITY_COND_REWARD_ARR, SERVER_ACTIVITY_RANK_REWARD, SERVER_ACTIVITY_CROSS_RECHARGE, SERVER_ACTIVITY_TOKEN_REWARD:
				p.rbtHandleFrameEP(msgType, realData)
			// Thương Minh Yêu Vực: every 349 answer rides the swRT seats
			// (auto loop + the sanctioned web war order are the callers).
			case SERVER_SW_ENTER, SERVER_SW_ENTER_GAME, SERVER_SW_EXPLORE,
				SERVER_SW_HANDLE_EVENT, SERVER_SW_UNION_WELFARE, SERVER_SW_UNION_WELFARE_REWARD,
				SERVER_SW_UNION_RECEIVE_LIQUID, SERVER_SW_UNION_SEND_LIQUID, SERVER_SW_UNION_LIQUID_RECORD,
				SERVER_SW_HEART_BEAT, SERVER_SW_NEXT_REGION,
				SERVER_SW_USE_ITEM, SERVER_SW_WORSHIP,
				SERVER_SW_ASSIST_LIST, SERVER_SW_ASSIST, SERVER_SW_ASSIST_REWARD,
				SERVER_SW_ASSIST_SYNC, SERVER_SW_ASSIST_SYNC_RAW,
				SERVER_SW_CHALLENGE_LIST, SERVER_SW_CHALLENGE_INFO_SYNC, SERVER_SW_DECLARATION_INFO_SYNC,
				SERVER_SW_ENTER_CHALLENGE, SERVER_SW_CHALLENGE_INFO, SERVER_SW_CHALLENGE_BATTLE, SERVER_SW_DECLARATION:
				p.swHandleFrameEP(msgType, realData)
			// 1005 is SHARED between the Luân Hồi auto-loop, the God Island
			// shop auto-buy and the Thương Minh shop sweep: each in-flight
			// owner takes its own seat, the frame rides all three delivery
			// lanes (each is a no-op without a registered seat).
			case SERVER_ACTIVITY_BUY_GOODS:
				if !p.giDeliverBuyGoods(realData) {
					p.rbtHandleFrameEP(msgType, realData)
				}
				p.swHandleFrameEP(SERVER_ACTIVITY_BUY_GOODS, realData)
				// TODO: handle further event messages here. Dispatch on msgType
				// using the SERVER_* constants from command.go (add new ones as
				// event protocol messages are identified); reply via
				// p.write(pack.Pack(...)).
			}
		}
	}
}

// ---------------------------------------------------------------------------
// God Island events — command-driven, no auto-play.
//
// The game runs TWO variants of the same activity concurrently, same
// protocol, different activityId/config/resources:
//
//	GIEventNormal  ActivityMainConfig.type 68   — Đảo Bồng Lai (v1)
//	GIEventPeak    ActivityMainConfig.type 333  — Đảo Thiên Ma (v2, peak)
//
// All state is therefore per-event (giEvent); all movement/attacking happens
// only when the leader issues /bonglai_* or /thienma_* (GodIslandMoveTo etc.
// take the event selector). The account stays resident in every open variant
// (godIslandStay) and keeps each plant garden tidy on its own (giPlantCycle).
//
// Response attribution: responses generally carry only ret + payload, no
// activityId, but the server answers one connection strictly in request
// order — so each tracked request enqueues its event (giSend) and the next
// response of that type belongs to the oldest entry (giClaim). The base-info
// response and all event pushes DO carry the activityId and self-attribute
// (giEventOf).
//
// Protocol reference: ../remoteBlessFrog/STATIC_ANALYSIS.md and
// GOD_ISLAND_PROTOCOL.md; message types live in internal/proto/tytd.proto.
// ---------------------------------------------------------------------------

// ActivityMainConfig.type values of the two God Island variants.
const (
	giActivityTypeGodIsland  = 68
	giActivityTypePeakIsland = 333
)

// Event selectors accepted by every public GodIsland* command.
const (
	GIEventNormal = iota // Đảo Bồng Lai
	GIEventPeak          // Đảo Thiên Ma
	giEventCount
)

// giHomeCityID is the fallback base city assumed by the order validation
// until the INFO_LOAD poll reports the account's real home (giEvent.Home) —
// which differs between the normal and the peak island.
const giHomeCityID = 6001

// giEvent is the complete per-event God Island state. Every field is guarded
// by EventPlayer.GodIslandMu (never a per-event lock).
type giEvent struct {
	ActivityID int32 // 0 = the server has not announced this event yet
	Type       int32 // ActivityMainConfig.type (68 / 333)
	Name       string
	CmdPrefix  string // telegram command prefix ("/bonglai_" / "/thienma_")

	Union      int64 // our union in this island (0 = none), from base info
	Group      int32 // bracket group, from base info
	Floor      int32 // bracket floor, from base info
	Home       int32 // base city from INFO_LOAD (0 = unknown yet)
	Location   int32 // current city
	LeftOut    bool  // kicked out of the island
	ReviveTime int64 // revive deadline, epoch ms (or s, normalized at read time; 0 = alive)
	// March state: presence of myRouteInfo in the INFO_LOAD snapshot. The
	// server only accepts accelerate moves while a route exists (MOVE
	// creates it, arrival/return clears it).
	Moving  bool
	RouteTo int32
	// last speedUpCount seen; /tangtoc sends it as the server's validation
	// baseline, the 1ac1 response echoes the incremented value and the 3s
	// INFO_LOAD poll refreshes it.
	SpeedUp int32
	// Manual ⚡ stamp (owner 2026-10-21 async-order round). A human press
	// stamps it; giHandleAccelerateResp consumes it to (a) lightly alert the
	// leader when the server REFUSES an explicitly clicked press (auto paths
	// never stamp → never spam) and (b) clear it so the next press may fire.
	// A repeat press inside giAccelDedupGap is swallowed silently: its
	// answer is still in flight, so it would echo the pre-increment baseline
	// and draw a refusal — one root of the "20 acc bấm tăng tốc, chỉ 3-4
	// chạy" report (frantic re-clicks poisoned the remaining accounts).
	AccelPressAt time.Time

	// Blood-ball power cached from SPIRITUALBALL_INFO polls and USE_FRUIT /
	// USE_SPIRITUALBALL responses; /hoimau draws from here. Live-verified
	// semantics: BallCur is the linh lực RESERVOIR (drained 1:1 by usePower
	// when drinking), BallMax is the per-draw ceiling — the reservoir can
	// read far larger than it. Known flips true once any power figure
	// parsed, so "0" stays meaningful. Neither field is the unit's HP.
	BallCur   int64
	BallMax   int64
	BallKnown bool
	// Unit HP (curPower/maxPower) from INFO_LOAD fields 12/13 and the
	// BALL_INFO / USE_BALL responses. Only this can answer "is the unit
	// actually at full blood": a dead unit can still report a non-empty
	// ball, which is why checking the ball for full-HP was wrong.
	PowerCur   int64
	PowerMax   int64
	PowerKnown bool

	// Plant garden: liquid item ids from THIS event's GODISLAND_ITEM_ENERGY
	// config (the only valid SEND_LIQUID liquidIds for it), fetched once via
	// ACTIVITY_GET_DATA, and the last seen canReceive flag so ripe fruit is
	// claimed once instead of on every water/plant-info response.
	LiquidIDs       []int32
	PlantCanReceive int32

	// Shop (owner 2026-10-19): the mall table + today's bought counts the
	// 21003 answer re-carries for this event. giShopSweep buys ONE unit per
	// sweep of every row that passes the rbtMallAuto gate (free / ad-watch /
	// tiên ngọc) — money rows get limit 0 there, so no path from a recharge
	// row to a 21005 send exists. MallAdds counts this bot's own buys since
	// the ICT day boundary; MallPending is the FIFO of mallIds whose 1005
	// answer is still out — RspBuyActivityGoods carries no mallId, so the
	// queue (server answers in request order) names the row an answer
	// belongs to. ShopLast throttles the sweeps (giShopSweepGap).
	MallRows    []*proto.ActivityMallTempMsg
	MallBought  map[int32]int64
	MallAdds    map[int32]int64
	MallDay     string
	MallPending []int32
	ShopLast    time.Time

	// People on the map (owner 2026-10-19 "hiển thị người trong thành, hiển
	// thị người đang chạy giống như côn lôn"; opcodes proven same day):
	// Routes is the march ledger keyed playerId, folded from the 1aa6 push
	// and every 1aa7 routeInfo answer (≡ kunlun st.Routes); Roster caches the
	// last full standing row per city from 1aa7 answers + 1abe pushes;
	// FightSeen marks cities the 1ace push flagged as battling (cityId →
	// server ms last seen true — false deletes the entry). All guarded by
	// GodIslandMu with the rest of the slot.
	Routes    map[int64]giRouteRec
	Roster    map[int32]giCityRow
	FightSeen map[int32]int64

	// Def nhà (owner 2026-10-19, player_event_gidef.go): DeadCity is the
	// city 1acd stamped at death (the engine only revives units that died
	// AT the home base). The Def* stamps throttle the auto engine:
	// DefLastAct one action per giDefActGap, DefLastFruit one fruit bite
	// per giDefFruitGap, DefWaitSince the first tick this unit was seen
	// dead-needing-heal (rotation order "lần lượt"), DefAlive the last
	// alive verdict for edge-detecting the stood-up notification. All under
	// GodIslandMu with the rest of the slot.
	DeadCity     int32
	DefLastAct   time.Time
	DefLastFruit time.Time
	DefWaitSince time.Time
	DefAlive     bool
}

// giSlot returns the state slot of one event variant, creating it on first
// use. Slots are never removed, so the returned pointer stays valid.
func (p *EventPlayer) giSlot(which int) *giEvent {
	p.GodIslandMu.Lock()
	defer p.GodIslandMu.Unlock()
	return p.giSlotLocked(which)
}

// giSlotLocked backs giSlot; caller holds GodIslandMu.
func (p *EventPlayer) giSlotLocked(which int) *giEvent {
	if which < 0 || which >= giEventCount {
		return nil
	}
	if p.GiEvents[which] == nil {
		ev := &giEvent{Type: giActivityTypeGodIsland, Name: "Bồng Lai", CmdPrefix: "/bonglai_"}
		if which == GIEventPeak {
			ev = &giEvent{Type: giActivityTypePeakIsland, Name: "Thiên Ma", CmdPrefix: "/thienma_"}
		}
		p.GiEvents[which] = ev
	}
	return p.GiEvents[which]
}

// giByActivityIDLocked maps a self-describing frame's activityId to its
// slot; caller holds GodIslandMu.
func (p *EventPlayer) giByActivityIDLocked(activityID int32) *giEvent {
	if activityID == 0 {
		return nil
	}
	for _, ev := range p.GiEvents {
		if ev != nil && ev.ActivityID == activityID {
			return ev
		}
	}
	return nil
}

// giEventOf resolves a push's activityId strictly; frames of foreign or
// unknown events return nil.
func (p *EventPlayer) giEventOf(activityID int32) *giEvent {
	p.GodIslandMu.Lock()
	defer p.GodIslandMu.Unlock()
	return p.giByActivityIDLocked(activityID)
}

// giSend marshals msg and puts the frame on the wire. When respType is non-
// empty it also registers ev as the event awaiting the next response of that
// incoming type — giSendMu keeps the queue-append and the frame write
// adjacent, so the FIFO queue order equals the wire order the server sees.
// respType "" is for requests whose response we don't consume (move, auto
// attack, heartbeat, base info — the latter self-attributes).
func (p *EventPlayer) giSend(ev *giEvent, cmd, respType string, msg pb.Message) {
	payload, err := pb.Marshal(msg)
	if err != nil {
		log.Printf("[%s] [GodIsland] marshal %s: %v", p.Username, cmd, err)
		return
	}
	p.giSendMu.Lock()
	if respType != "" {
		p.GodIslandMu.Lock()
		if p.giPending == nil {
			p.giPending = map[string][]*giEvent{}
		}
		// A dropped server answer would otherwise grow the queue for every
		// repeat click (audit finding #7): cap per type, oldest-first — the
		// stale head is the one nobody will ever claim.
		if q := p.giPending[respType]; len(q) >= 16 {
			p.giPending[respType] = q[1:]
		}
		p.giPending[respType] = append(p.giPending[respType], ev)
		p.GodIslandMu.Unlock()
	}
	p.write(pack.Pack(cmd, payload, p.UID))
	p.giSendMu.Unlock()
}

// giClaim attributes one response frame of respType to an event: pop the
// oldest pending entry (the server answers in request order). With an empty
// queue — an unrequested response — attribute it only when exactly one event
// is open; with both events open such a frame is ambiguous and dropped.
func (p *EventPlayer) giClaim(respType string) *giEvent {
	p.GodIslandMu.Lock()
	defer p.GodIslandMu.Unlock()
	q := p.giPending[respType]
	if len(q) > 0 {
		ev := q[0]
		if len(q) == 1 {
			delete(p.giPending, respType)
		} else {
			p.giPending[respType] = q[1:]
		}
		return ev
	}
	var found *giEvent
	for _, ev := range p.GiEvents {
		if ev != nil && ev.ActivityID != 0 {
			if found != nil {
				return nil
			}
			found = ev
		}
	}
	return found
}

// giRoute attributes one response frame and runs its per-event handler.
// Handlers receive an unlocked player and do their own locking.
func (p *EventPlayer) giRoute(respType string, data []byte, h func(*EventPlayer, *giEvent, []byte)) {
	ev := p.giClaim(respType)
	if ev == nil {
		log.Printf("[%s] [GodIsland] cannot attribute response %s to any event — dropped",
			p.Username, respType)
		return
	}
	h(p, ev, data)
}

// giHandleActivityList parses PushActivityList/SyncActivityList, both laid out
// as { repeated ActivityMainConfig mainConfig = 1 } with each entry carrying
// { activityId = 1, type = 2 }. It records the activityId of BOTH God Island
// variants (68 = Bồng Lai, 333 = Thiên Ma); the server pushes the full list
// right after login.
func (p *EventPlayer) giHandleActivityList(data []byte) {
	var push proto.PushActivityList
	if err := pb.Unmarshal(data, &push); err != nil {
		log.Printf("[%s] [GodIsland] bad activity list push: %v", p.Username, err)
		return
	}
	var normalID, peakID int32
	for _, cfg := range push.GetMainConfig() {
		switch cfg.GetType() {
		case giActivityTypeGodIsland:
			normalID = cfg.GetActivityId()
		case giActivityTypePeakIsland:
			peakID = cfg.GetActivityId()
		}
	}
	p.GodIslandMu.Lock()
	for which, id := range map[int]int32{GIEventNormal: normalID, GIEventPeak: peakID} {
		if id == 0 {
			continue
		}
		ev := p.giSlotLocked(which)
		if ev.ActivityID != id {
			ev.ActivityID = id
			log.Printf("[%s] [GodIsland] %s activity %d", p.Username, ev.Name, id)
		}
	}
	p.GodIslandMu.Unlock()
}

// giHandleBaseInfo parses GodIslandBaseInfoRespMsg, which carries its
// activityId — no FIFO queue needed. ret != 0 (event not open, no bracket
// assigned yet, ...) leaves the previous state untouched; godIslandStay keeps
// polling and the values show up once the bracket exists.
func (p *EventPlayer) giHandleBaseInfo(data []byte) {
	var resp proto.GodIslandBaseInfoRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad base info: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		return
	}
	ev := p.giEventOf(resp.GetActivityId())
	if ev == nil {
		return // an event variant we don't track
	}
	p.GodIslandMu.Lock()
	ev.Group = resp.GetGroup()
	ev.Floor = resp.GetFloor()
	ev.Union = resp.GetUnionId()
	p.GodIslandMu.Unlock()
}

// The residency poll cadences: every giInfoPollSeconds ask the server for
// game info (206820 — its speedUpCount feeds /tangtoc), and every
// giHeartBeatTicks-th poll also refreshes base info + heart beat + ball info,
// the once-a-minute residency keep-alive godIslandStay used before.
const (
	giInfoPollSeconds = 3
	giHeartBeatTicks  = 20
	// giShopSweepGap throttles the shop auto-buy: one sweep per event per
	// gap (daily caps are 1–2, so a couple of sweeps fill them).
	giShopSweepGap = 10 * time.Minute
)

// godIslandStay is the only background God Island work an event account does:
// for EVERY announced variant (Bồng Lai, Thiên Ma) it polls game info every
// giInfoPollSeconds and heartbeats every minute, which keeps us resident on
// that island. Each heartbeat tick also runs that event's plant cycle
// (giPlantCycle: collect pending liquid, claim ripe fruit, donate liquid —
// leader-requested upkeep, no combat). No target picking, no attacking —
// those are leader-command driven (GodIslandMoveTo).
func (p *EventPlayer) godIslandStay() {
	tick := 0
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(giInfoPollSeconds * time.Second):
		}

		for which := 0; which < giEventCount; which++ {
			p.GodIslandMu.Lock()
			ev := p.GiEvents[which]
			activityID := int32(0)
			if ev != nil {
				activityID = ev.ActivityID
			}
			p.GodIslandMu.Unlock()
			if activityID == 0 {
				// This variant not open (or its push hasn't arrived); keep
				// waiting for the next tick.
				continue
			}

			p.giSend(ev, GOD_ISLAND_INFO_LOAD_CMD, SERVER_GOD_ISLAND_INFO_LOAD,
				&proto.GodIslandGameInfoReqMsg{ActivityId: activityID})

			// Def nhà (owner 2026-10-19): while the home base fights and no
			// ours stands there, revive our corpses one at a time with
			// sips + fruit — every tick, self-throttled inside.
			p.giDefHome(which)

			if tick%giHeartBeatTicks == 0 {
				p.giSend(ev, GOD_ISLAND_BASE_INFO_CMD, "",
					&proto.GodIslandBaseInfoReqMsg{ActivityId: activityID})
				p.giSend(ev, GOD_ISLAND_HEART_BEAT_CMD, "",
					&proto.GodIslandHeartBeatReqMsg{ActivityId: activityID})
				p.giSend(ev, GOD_ISLAND_BALL_INFO_CMD, SERVER_GOD_ISLAND_BALL_INFO,
					&proto.GodIslandGameSpiritualBallInfoReqMsg{ActivityId: activityID})
				// Plant garden upkeep: the first tick lands right after
				// entering the island, then one cycle per minute.
				p.giPlantCycle(ev)
				// Shop auto-buy (owner 2026-10-19): free / ad-watch /
				// tiên-ngọc rows only, throttled to one sweep per
				// giShopSweepGap inside giShopSweep itself.
				p.giShopSweep(ev)
			}
		}
		tick++
	}
}

// giHandleGameInfo parses GodIslandGameInfoRespMsg (206820). ret != 0 leaves
// the cache untouched; each optional field updates its cache entry only when
// the server actually sent it (home is the base city for the order
// validation, speedUpCount feeds GodIslandAccelerate, the rest feeds
// GodIslandStatus for /my_event_members).
func (p *EventPlayer) giHandleGameInfo(ev *giEvent, data []byte) {
	var resp proto.GodIslandGameInfoRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad game info: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		return
	}
	p.GodIslandMu.Lock()
	if resp.Home != nil && resp.GetHome() != 0 {
		ev.Home = resp.GetHome()
	}
	if resp.SpeedUpCount != nil {
		ev.SpeedUp = resp.GetSpeedUpCount()
	}
	if resp.Location != nil {
		ev.Location = resp.GetLocation()
	}
	if resp.Leftout != nil {
		ev.LeftOut = resp.GetLeftout()
	}
	if resp.ReviveTime != nil {
		ev.ReviveTime = resp.GetReviveTime()
	}
	giSetPower(&ev.PowerKnown, &ev.PowerCur, &ev.PowerMax, resp.CurPower, resp.MaxPower)
	// myRouteInfo is a presence signal, not an optional-diff field: the
	// snapshot either carries our route or it does not, so assign always.
	ev.Moving = resp.MyRouteInfo != nil
	if r := resp.MyRouteInfo; r != nil {
		ev.RouteTo = r.GetToCityId()
	} else {
		ev.RouteTo = 0
	}
	p.GodIslandMu.Unlock()
}

// giHandleAccelerateResp parses GodIslandGameAcclerateMoveRespMsg (206849
// answer, incoming 1ac1). The server increments the count itself and echoes
// the new value; we fold the echo whenever it is present — ret=0 or not —
// because the server's own echo is what heals a drifted baseline;
// the OLD code dropped every refusal silently, so a desynced SpeedUp could
// only be fixed by an INFO_LOAD that happened to carry the field, and the
// owner-facing symptom was "bấm tăng tốc không chạy" with zero trace. An
// answer also closes the press: the stamp clears so the button may fire
// again immediately, and a refusal inside the manual window gets ONE light
// leader alert (auto paths never stamp → never alert).
func (p *EventPlayer) giHandleAccelerateResp(ev *giEvent, data []byte) {
	var resp proto.GodIslandGameAcclerateMoveRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad accelerate resp: %v", p.Username, err)
		return
	}
	p.GodIslandMu.Lock()
	manual := time.Since(ev.AccelPressAt) < giAccelAlertWindow
	ev.AccelPressAt = time.Time{}
	if resp.SpeedUpCount != nil {
		ev.SpeedUp = resp.GetSpeedUpCount()
	}
	ret := resp.GetRet()
	count := resp.GetSpeedUpCount()
	p.GodIslandMu.Unlock()
	if ret != 0 {
		log.Printf("[%s] [GodIsland/%s]Tăng tốc ret=%d speedUp=%d", p.Username, ev.Name, ret, count)
		if manual {
			p.forceNotify(fmt.Sprintf("⚡ %s: server từ chối tăng tốc (ret=%d) — quân có thể đã về thành", ev.Name, ret))
		}
	}
}

// GodIslandEventOpen reports whether the server announced this event variant
// — /my_event_members uses it to decide whether the line is worth showing.
func (p *EventPlayer) GodIslandEventOpen(which int) bool {
	p.GodIslandMu.Lock()
	defer p.GodIslandMu.Unlock()
	ev := p.GiEvents[which]
	return ev != nil && ev.ActivityID != 0
}

// GodIslandStatus renders the one-line Vietnamese status of ONE event variant
// shown in /my_event_members. The revive deadline may arrive in seconds or
// ms; any value below 1e12 is treated as seconds.
func (p *EventPlayer) GodIslandStatus(which int) string {
	if !p.Running {
		return "⏹️ chưa login"
	}
	p.GodIslandMu.Lock()
	defer p.GodIslandMu.Unlock()
	ev := p.giSlotLocked(which)
	if ev == nil || ev.ActivityID == 0 {
		return "⏳ chưa vào đảo"
	}
	if ev.LeftOut {
		return "🚪 đã bị loại khỏi đảo"
	}
	if left, dead := ev.reviveLeft(); dead {
		return fmt.Sprintf("☠️ đang chết, hồi sinh sau %dm%02ds",
			int(left.Minutes()), int(left.Seconds())%60)
	}
	if ev.Moving {
		if ev.RouteTo != 0 {
			return fmt.Sprintf("🏝️ đang hành quân tới thành %d", ev.RouteTo)
		}
		return "🏝️ đang hành quân"
	}
	if ev.Location != 0 {
		return fmt.Sprintf("🏝️ đang đóng ở thành %d", ev.Location)
	}
	return "🏝️ đang chạy"
}

// reviveLeft reports how long until the unit revives and whether it is
// dead now: the server's own reviveTime countdown, not yet elapsed. The
// deadline may arrive in seconds or ms; values below 1e12 read as seconds.
// Caller holds GodIslandMu. The ball cannot be drunk while dead (server
// answers ret != 0), so heal checks this before ordering anything.
func (ev *giEvent) reviveLeft() (time.Duration, bool) {
	if ev.ReviveTime <= 0 {
		return 0, false
	}
	ms := ev.ReviveTime
	if ms < 1_000_000_000_000 {
		ms *= 1000
	}
	if left := time.Until(time.UnixMilli(ms)); left > 0 {
		return left, true
	}
	return 0, false
}

// parsePower converts the protocol's decimal power strings to int64.
func parsePower(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}

// giSetPower parses one cur/max string pair into a power cache; known flips
// true on the first successful parse so a legitimate 0 stays distinguishable
// from "never seen". nil fields are left untouched. Caller holds GodIslandMu.
func giSetPower(known *bool, cur, max *int64, curStr, maxStr *string) {
	if curStr != nil {
		if v, ok := parsePower(*curStr); ok {
			*cur = v
			*known = true
		}
	}
	if maxStr != nil {
		if v, ok := parsePower(*maxStr); ok {
			*max = v
			*known = true
		}
	}
}

// giCacheBallPower stores one event's blood-ball resource figures (locks
// GodIslandMu).
func (p *EventPlayer) giCacheBallPower(ev *giEvent, cur, max *string) {
	p.GodIslandMu.Lock()
	giSetPower(&ev.BallKnown, &ev.BallCur, &ev.BallMax, cur, max)
	p.GodIslandMu.Unlock()
}

// giCachePower stores one event's unit HP figures (locks GodIslandMu).
func (p *EventPlayer) giCachePower(ev *giEvent, cur, max *string) {
	p.GodIslandMu.Lock()
	giSetPower(&ev.PowerKnown, &ev.PowerCur, &ev.PowerMax, cur, max)
	p.GodIslandMu.Unlock()
}

// giHandleBallInfo caches blood-ball power from
// GodIslandGameSpiritualBallInfoRespMsg (206826), polled with the heartbeat.
func (p *EventPlayer) giHandleBallInfo(ev *giEvent, data []byte) {
	var resp proto.GodIslandGameSpiritualBallInfoRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad ball info: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		return
	}
	p.giCacheBallPower(ev, resp.BallCurPower, resp.BallMaxPower)
	p.giCachePower(ev, resp.CurPower, resp.MaxPower)
}

// giHandleUseFruitResp refreshes the same cache from GodIslandUseFruitResp
// (6837): /canqua feeds the ball, so this keeps /hoimau current right after
// the feed without waiting for the next poll.
func (p *EventPlayer) giHandleUseFruitResp(ev *giEvent, data []byte) {
	var resp proto.GodIslandUseFruitResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad use-fruit resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		return
	}
	p.giCacheBallPower(ev, resp.BallCurPower, resp.BallMaxPower)
}

// giHandleUseBallResp reconciles both sides of a heal from
// GodIslandGameUseSpiritualBallRespMsg (6827): ballCurPower shows what the
// ball holds after the draw, curPower what the unit's HP became.
func (p *EventPlayer) giHandleUseBallResp(ev *giEvent, data []byte) {
	var resp proto.GodIslandGameUseSpiritualBallRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad use-ball resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [GodIsland/%s] use-ball refused by server: ret=%d",
			p.Username, ev.Name, resp.GetRet())
		return
	}
	p.GodIslandMu.Lock()
	giSetPower(&ev.BallKnown, &ev.BallCur, &ev.BallMax, resp.BallCurPower, nil)
	giSetPower(&ev.PowerKnown, &ev.PowerCur, &ev.PowerMax, resp.CurPower, nil)
	p.GodIslandMu.Unlock()
}

// ---------------------------------------------------------------------------
// Plant garden (vườn linh thảo) — auto-run per event once resident there.
//
// Per plant cycle (godIslandStay heartbeat tick): PLANT_INFO asks for the
// garden state, its response handler collects every pending liquid notice
// one by one (PLANT_WATER, noticeInfo.id — NOT the liquidId), ripe fruit is
// claimed (RECEIVE_FRUITS), and one of each GODISLAND_ITEM_ENERGY liquid item
// is donated (SEND_LIQUID sendNum=1, same as the client's send button). The
// liquidId list itself comes from THAT event's ACTIVITY_GET_DATA config,
// fetched once — Bồng Lai and Thiên Ma can configure different items.
// ---------------------------------------------------------------------------

// giParseLiquidIDs parses a GODISLAND_ITEM_ENERGY value
// "liquidItemId|energyValue;..." into the item id list (client parses the
// same config at index.5d668.pretty.js:106937).
func giParseLiquidIDs(cfg string) []int32 {
	var ids []int32
	for _, pair := range strings.Split(cfg, ";") {
		kv := strings.Split(pair, "|")
		if len(kv) != 2 {
			continue
		}
		id, err := strconv.Atoi(kv[0])
		if err != nil || id <= 0 {
			continue
		}
		ids = append(ids, int32(id))
	}
	return ids
}

// giHandleActivityDetail parses RspGetActivityDetail (incoming 03eb) and
// caches two slices of that event's config: the GODISLAND_ITEM_ENERGY liquid
// ids (first time only) and the shop table + today's bought counts the same
// answer re-carries (mallConfig / mallBuyCountList — same fold as
// rbtMergeConfig). The bought map is only trusted together with a non-empty
// rows table, so a partial answer can never wipe the bought baseline and let
// a sweep over-buy past the daily cap.
func (p *EventPlayer) giHandleActivityDetail(ev *giEvent, data []byte) {
	var resp proto.RspGetActivityDetail
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad activity detail: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		// Owner 2026-10-20 ("tao thấy chưa tự mua gói"): a refused detail
		// ask used to die silently — the shop then never learns its table.
		log.Printf("[%s] [GodIsland] activity detail ret=%d (act %d) — shop chưa đọc được",
			p.Username, resp.GetRet(), resp.GetActivity().GetActivityId())
		return
	}
	var cfg string
	for _, pc := range resp.GetActivity().GetDetailConfig().GetCommonConfig().GetParamConfig() {
		if pc.GetParam() == "GODISLAND_ITEM_ENERGY" {
			cfg = pc.GetValue()
		}
	}
	ids := giParseLiquidIDs(cfg)
	var mallRows []*proto.ActivityMallTempMsg
	for _, mc := range resp.GetActivity().GetDetailConfig().GetCommonConfig().GetMallConfig() {
		if mt := mc.GetMallTempMsg(); mt != nil {
			mallRows = append(mallRows, mt)
		}
	}
	bought := map[int32]int64{}
	for _, bc := range resp.GetActivity().GetMallBuyCountList() {
		bought[bc.GetMallId()] = bc.GetCount()
	}
	p.GodIslandMu.Lock()
	changed := len(ev.LiquidIDs) == 0 && len(ids) > 0
	if changed {
		ev.LiquidIDs = ids
	}
	if len(mallRows) > 0 {
		ev.MallRows = mallRows
		ev.MallBought = bought
	}
	p.GodIslandMu.Unlock()
	if changed {
		log.Printf("[%s] [GodIsland/%s] liquid items: %v", p.Username, ev.Name, ids)
	}
	if len(mallRows) > 0 {
		log.Printf("[%s] [GodIsland/%s] shop: %d gói, đã mua hôm nay %v", p.Username, ev.Name, len(mallRows), bought)
	}
}

// giPlantCycle runs one plant cycle for one event: garden poll (which
// triggers the collect/claim chain through giHandlePlantInfo) plus one
// donation of each of its configured liquid items. Only called from
// godIslandStay.
func (p *EventPlayer) giPlantCycle(ev *giEvent) {
	p.GodIslandMu.Lock()
	activityID := ev.ActivityID
	ids := append([]int32(nil), ev.LiquidIDs...)
	p.GodIslandMu.Unlock()
	if activityID == 0 {
		return
	}
	p.giSend(ev, GOD_ISLAND_PLANT_INFO_CMD, SERVER_GOD_ISLAND_PLANT_INFO,
		&proto.GodIslandGamePlantInfoReqMsg{ActivityId: activityID})
	if len(ids) == 0 {
		// liquidIds still unknown for this event — (re)ask for its activity
		// config; the next cycle donates once giHandleActivityDetail cached
		// them.
		p.giSend(ev, ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL,
			&proto.ReqGetActivityDetail{ActivityId: activityID})
		return
	}
	for _, lid := range ids {
		p.giSend(ev, GOD_ISLAND_SEND_LIQUID_CMD, SERVER_GOD_ISLAND_SEND_LIQUID,
			&proto.GodIslandGameSendLiquidReqMsg{ActivityId: activityID, LiquidId: lid, SendNum: 1})
	}
}

// giShopSweep is the God Island shop upkeep (owner 2026-10-19): every
// giShopSweepGap it buys ONE unit of every auto-buyable row up to its daily
// cap. rbtMallAuto is the single gate — free ("0"/"X=0"), ad-watch (param
// "3|") and tiên-ngọc ("100000=") rows pass; every recharge row gets limit 0
// there, so no code path from a money row to a 21005 send exists (same
// authorisation shape as Luân Hồi). While the shop table is still unknown the
// sweep spends one 21003 ask instead; giHandleActivityDetail folds the rows
// and the next sweep buys. The server is the oracle for ad cooldowns and
// tiên-ngọc balance: a refused buy is simply retried next sweep.
func (p *EventPlayer) giShopSweep(ev *giEvent) {
	p.GodIslandMu.Lock()
	activityID := ev.ActivityID
	rows := append([]*proto.ActivityMallTempMsg(nil), ev.MallRows...)
	if time.Since(ev.ShopLast) < giShopSweepGap {
		p.GodIslandMu.Unlock()
		return
	}
	ev.ShopLast = time.Now()
	p.GodIslandMu.Unlock()
	if activityID == 0 {
		return
	}
	if len(rows) == 0 {
		log.Printf("[%s] [GodIsland/%s] shop: chưa có bảng gói, hỏi 21003 (act %d)", p.Username, ev.Name, activityID)
		p.giSend(ev, ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL,
			&proto.ReqGetActivityDetail{ActivityId: activityID})
		return
	}
	today := time.Now().In(ictZone).Format("2006-01-02")
	p.GodIslandMu.Lock()
	if ev.MallDay != today || ev.MallAdds == nil {
		ev.MallDay = today
		ev.MallAdds = map[int32]int64{}
	}
	adds, bought := ev.MallAdds, ev.MallBought
	p.GodIslandMu.Unlock()
	if bought == nil {
		bought = map[int32]int64{}
	}
	for _, mt := range rows {
		limit, _, _ := rbtMallAuto(mt)
		if limit == 0 {
			continue // recharge row — bot never buys (item ban)
		}
		if bought[mt.GetId()]+adds[mt.GetId()] >= limit {
			continue // daily cap reached (server counts + our own buys)
		}
		// One unit per sweep keeps the wire quiet and lets ad cooldowns
		// settle between attempts; the small caps catch up within a couple
		// of sweeps.
		p.GodIslandMu.Lock()
		if len(ev.MallPending) >= 16 {
			p.GodIslandMu.Unlock()
			continue // answers from an earlier sweep never landed — stop queueing
		}
		ev.MallPending = append(ev.MallPending, mt.GetId())
		p.GodIslandMu.Unlock()
		log.Printf("[%s] [GodIsland/%s] shop: mua 1× mallId=%d (hạn %d, đã %d+%d)", p.Username, ev.Name,
			mt.GetId(), limit, bought[mt.GetId()], adds[mt.GetId()])
		p.giSend(ev, ACTIVITY_BUY_GOODS_CMD, SERVER_ACTIVITY_BUY_GOODS,
			&proto.ReqBuyActivityGoods{ActivityId: activityID, MallId: mt.GetId(), Count: 1})
	}
}

// giDeliverBuyGoods attributes a generic 1005 (RspBuyActivityGoods) answer
// to an in-flight God Island shop buy. 03ed is SHARED with the Luân Hồi
// auto-loop: no GI buy pending → return false so the dispatcher hands the
// frame to rbtHandleFrameEP. The answer carries no mallId, so MallPending —
// FIFO, the server answers in request order — names the row. Success bumps
// MallAdds and notifies; a refusal is expected noise (cooldown / no jade)
// and just retries next sweep.
func (p *EventPlayer) giDeliverBuyGoods(data []byte) bool {
	p.GodIslandMu.Lock()
	var ev *giEvent
	for _, e := range p.GiEvents {
		if e != nil && len(e.MallPending) > 0 {
			ev = e
			break
		}
	}
	if ev == nil {
		p.GodIslandMu.Unlock()
		return false
	}
	mallID := ev.MallPending[0]
	if len(ev.MallPending) == 1 {
		ev.MallPending = nil
	} else {
		ev.MallPending = ev.MallPending[1:]
	}
	p.GodIslandMu.Unlock()

	var rsp proto.RspBuyActivityGoods
	if err := pb.Unmarshal(data, &rsp); err != nil {
		log.Printf("[%s] [GodIsland] bad buy answer: %v", p.Username, err)
		return true
	}
	if rsp.GetRet() != 0 {
		debugf("[%s] [GodIsland/%s] shop: mall %d bị từ chối ret=%d — thử lại cycle sau",
			p.Username, ev.Name, mallID, rsp.GetRet())
		return true
	}
	kind, name := "quà miễn phí", ""
	p.GodIslandMu.Lock()
	if ev.MallAdds == nil {
		ev.MallAdds = map[int32]int64{}
	}
	ev.MallAdds[mallID]++
	for _, mt := range ev.MallRows {
		if mt.GetId() == mallID {
			name = mt.GetName()
			_, ad, jade := rbtMallAuto(mt)
			switch {
			case ad:
				kind = "gói xem quảng cáo"
			case jade:
				kind = "gói tiên ngọc"
			}
			break
		}
	}
	p.GodIslandMu.Unlock()
	p.sendNotify(fmt.Sprintf("🏝️ [%s] đã mua %s (%s): %s", ev.Name, kind, name, rsp.GetRewards()))
	return true
}

// giHandlePlantInfo collects every still-pending liquid notice of this
// event's garden by its id. The client offers an id=0 "collect all" only once
// GODISLAND_QUICK_CLICK is reached, so we always water the notices one by one
// — it works regardless.
func (p *EventPlayer) giHandlePlantInfo(ev *giEvent, data []byte) {
	var resp proto.GodIslandGamePlantInfoRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad plant info: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		return
	}
	p.GodIslandMu.Lock()
	activityID := ev.ActivityID
	p.GodIslandMu.Unlock()

	p.giUpdatePlantCanReceive(ev, resp.CanReceive)
	for _, n := range resp.GetNoticeInfo() {
		if n.GetStatus() != 0 || n.GetId() == 0 {
			continue
		}
		p.giSend(ev, GOD_ISLAND_PLANT_WATER_CMD, SERVER_GOD_ISLAND_PLANT_WATER,
			&proto.GodIslandGamePlantWaterReqMsg{ActivityId: activityID, Id: n.GetId()})
	}
}

// giUpdatePlantCanReceive stores the latest canReceive flag of one event's
// garden and reports whether it just flipped to ripe, so the claim fires
// once, not once per water response while the garden stays ripe.
func (p *EventPlayer) giUpdatePlantCanReceive(ev *giEvent, canReceive *int32) (justRipe bool) {
	if canReceive == nil {
		return false
	}
	p.GodIslandMu.Lock()
	justRipe = *canReceive == 1 && ev.PlantCanReceive != 1
	ev.PlantCanReceive = *canReceive
	p.GodIslandMu.Unlock()
	return justRipe
}

// giSendPlantFruits claims the completed plant reward (206838) of one event.
func (p *EventPlayer) giSendPlantFruits(ev *giEvent) {
	p.GodIslandMu.Lock()
	activityID := ev.ActivityID
	p.GodIslandMu.Unlock()
	if activityID == 0 {
		return
	}
	p.giSend(ev, GOD_ISLAND_RECEIVE_FRUITS_CMD, SERVER_GOD_ISLAND_RECEIVE_FRUITS,
		&proto.GodIslandGamePlantReceiveFruitsReqMsg{ActivityId: activityID})
}

// giHandlePlantWater reconciles one collected notice: the reward strings and
// the updated lv/exp/canReceive/waterCount of the garden.
func (p *EventPlayer) giHandlePlantWater(ev *giEvent, data []byte) {
	var resp proto.GodIslandGamePlantWaterRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad plant water resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [GodIsland/%s] water refused by server: ret=%d",
			p.Username, ev.Name, resp.GetRet())
		return
	}
	if p.giUpdatePlantCanReceive(ev, resp.CanReceive) {
		p.giSendPlantFruits(ev)
	}
}

// giHandleReceiveFruits logs the claimed garden reward (reward is a
// "itemId=count" string) and stores the post-claim canReceive (0 = reset).
func (p *EventPlayer) giHandleReceiveFruits(ev *giEvent, data []byte) {
	var resp proto.GodIslandGamePlantReceiveFruitsRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad receive-fruits resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [GodIsland/%s] receive-fruits refused by server: ret=%d",
			p.Username, ev.Name, resp.GetRet())
		return
	}
	log.Printf("[%s] [GodIsland/%s] vườn linh thảo nhận quả: %s (lv=%d)",
		p.Username, ev.Name, resp.GetReward(), resp.GetLv())
	p.giUpdatePlantCanReceive(ev, resp.CanReceive)
}

// giHandleSendLiquid records a donation refusal (ret=125 = no item left in
// the bag — the client checks its bag before sending, we let the server
// answer and just stay quiet about it).
func (p *EventPlayer) giHandleSendLiquid(ev *giEvent, data []byte) {
	var resp proto.GodIslandGameSendLiquidRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [GodIsland] bad send-liquid resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [GodIsland/%s] send-liquid refused by server: ret=%d",
			p.Username, ev.Name, resp.GetRet())
	}
}

// ---------------------------------------------------------------------------
// God Island server event pushes (1ac2/1ac9/1acd/1acf) — schemas from
// ../remoteImmortalIsland/GOD_ISLAND_PROTOCOL.md. All of them carry the
// activityId, so each updates exactly its own event's caches between the
// INFO_LOAD polls and tells the leader when that island's state changes.
// ---------------------------------------------------------------------------

// giHandleMyGameInfoSync applies MYGAMEINFO_SYNC (206850) — the server's own
// push of our island state. Per the remoteImmortalIsland analysis this is the
// push that confirms revive/arrival transitions, so caches move on it instead
// of waiting for the next 3s INFO_LOAD poll.
func (p *EventPlayer) giHandleMyGameInfoSync(data []byte) {
	var sync proto.GodIslandGameMyGameInfoSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [GodIsland] bad mygameinfo sync: %v", p.Username, err)
		return
	}
	ev := p.giEventOf(sync.GetActivityId())
	if ev == nil {
		return // a foreign event instance (other than our two)
	}
	p.GodIslandMu.Lock()
	if sync.Location != nil {
		ev.Location = sync.GetLocation()
	}
	giSetPower(&ev.PowerKnown, &ev.PowerCur, &ev.PowerMax, sync.CurPower, sync.MaxPower)
	p.GodIslandMu.Unlock()
}

// giHandleBeenKillSync records BEEN_KILL_SYNC (206861): our unit died on one
// island. The push carries the fresh revive deadline, so the heal/revive
// countdown is exact from the moment of death; the leader gets a
// game-language notification naming the island.
func (p *EventPlayer) giHandleBeenKillSync(data []byte) {
	var sync proto.GodIslandBeenKillSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [GodIsland] bad been-kill sync: %v", p.Username, err)
		return
	}
	ev := p.giEventOf(sync.GetActivityId())
	if ev == nil {
		return
	}
	who := "quái"
	if !sync.GetKillByNPc() {
		who = "đối thủ"
		if n := sync.GetTargetName(); n != "" {
			who = n
		}
	}
	p.GodIslandMu.Lock()
	ev.Location = 0
	ev.Moving = false
	ev.RouteTo = 0
	// Def-nha arbiter input: where the corpse lies, and a fresh rotation
	// clock (the old alive verdict / wait stamp belong to the past life).
	ev.DeadCity = sync.GetCityId()
	ev.DefAlive = false
	ev.DefWaitSince = time.Time{}
	if sync.ReviveTime != nil {
		ev.ReviveTime = sync.GetReviveTime()
	}
	left, dead := ev.reviveLeft()
	p.GodIslandMu.Unlock()

	msg := fmt.Sprintf("☠️ [%s] quân bị %s hạ tại thành %d", ev.Name, who, sync.GetCityId())
	if dead {
		msg += fmt.Sprintf(", hồi sinh sau %dm%02ds", int(left.Minutes()), int(left.Seconds())%60)
	}
	log.Printf("[%s] [GodIsland/%s] been kill: city=%d byNPC=%v reviveTime=%d",
		p.Username, ev.Name, sync.GetCityId(), sync.GetKillByNPc(), sync.GetReviveTime())
	p.sendNotify(msg)
}

// giHandleAutoBattleStop reports AUTO_BATTLE_STOP_SYNC (206863): the server
// ended an ordered auto-attack on one island, which the leader should know
// about.
func (p *EventPlayer) giHandleAutoBattleStop(data []byte) {
	var sync proto.GodIslandAutoBattleStopSyncMsg
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [GodIsland] bad auto-stop sync: %v", p.Username, err)
		return
	}
	ev := p.giEventOf(sync.GetActivityId())
	if ev == nil {
		return
	}
	log.Printf("[%s] [GodIsland/%s] auto battle stopped by server", p.Username, ev.Name)
	p.sendNotify(fmt.Sprintf("⚔️ [%s] tự động tấn công đã dừng", ev.Name))
}

// giHandleLeftOut handles LEFT_OUT_SYNC (206857): a union left one island.
// When it is ours there, that residency is gone — clear that event's state
// and tell the leader; other unions are only worth a log. (The union id is
// account-wide, so the push's activityId is what picks the island.)
func (p *EventPlayer) giHandleLeftOut(data []byte) {
	var sync proto.GodIslandLeftOutSyncInfo
	if err := pb.Unmarshal(data, &sync); err != nil {
		log.Printf("[%s] [GodIsland] bad left-out sync: %v", p.Username, err)
		return
	}
	ev := p.giEventOf(sync.GetActivityId())
	if ev == nil {
		return
	}
	p.GodIslandMu.Lock()
	ours := sync.GetUnionId() != 0 && sync.GetUnionId() == ev.Union
	if ours {
		ev.LeftOut = true
		ev.Location = 0
		ev.Moving = false
		ev.RouteTo = 0
	}
	p.GodIslandMu.Unlock()
	if ours {
		log.Printf("[%s] [GodIsland/%s] left island (union %d)", p.Username, ev.Name, sync.GetUnionId())
		p.sendNotify(fmt.Sprintf("🏝️ [%s] quân đã rời khỏi đảo", ev.Name))
	}
}

// godIslandActivity checks the account is logged in and ONE event variant is
// open, returning its state slot and activity id. Errors are user-facing.
func (p *EventPlayer) godIslandActivity(which int) (*giEvent, int32, error) {
	if !p.Running {
		return nil, 0, fmt.Errorf("chưa login")
	}
	ev := p.giSlot(which)
	if ev == nil {
		return nil, 0, fmt.Errorf("sự kiện không hợp lệ")
	}
	p.GodIslandMu.Lock()
	activityID := ev.ActivityID
	p.GodIslandMu.Unlock()
	if activityID == 0 {
		return nil, 0, fmt.Errorf("sự kiện đảo %s chưa mở (chưa có activity id)", ev.Name)
	}
	return ev, activityID, nil
}

// godIslandOrder validates that this account can obey a God Island order for
// cityID on one event variant and returns the slot + activity id to run it
// under. Errors are user-facing. Marching HOME is a legal order (owner
// 2026-10-19 "hành quân cho phép hành quân về nhà") — only ATTACKING the
// home city is refused, via godIslandNotHome on the attack path.
func (p *EventPlayer) godIslandOrder(which int, cityID int32) (*giEvent, int32, error) {
	ev, activityID, err := p.godIslandActivity(which)
	if err != nil {
		return nil, 0, err
	}
	if cityID <= 0 {
		return nil, 0, fmt.Errorf("city id không hợp lệ")
	}
	return ev, activityID, nil
}

// godIslandNotHome refuses attacks on the island's own base city — the
// home-city id comes from INFO_LOAD once reported (the two islands differ),
// falling back to the known normal-island one before that.
func (p *EventPlayer) godIslandNotHome(ev *giEvent, cityID int32) error {
	p.GodIslandMu.Lock()
	home := ev.Home
	p.GodIslandMu.Unlock()
	if home == 0 {
		home = giHomeCityID
	}
	if cityID == home {
		return fmt.Errorf("thành %d là nhà, không đánh được", cityID)
	}
	return nil
}

// godIslandMove queues the bare march frame (206832). Callers hold no lock;
// doWrite serializes the socket so frames reach the server in call order.
func (p *EventPlayer) godIslandMove(ev *giEvent, activityID, cityID int32) {
	p.giSend(ev, GOD_ISLAND_MOVE_CMD, "", &proto.GodIslandGameMoveReqMsg{
		ActivityId: activityID,
		TargetId:   cityID,
	})
}

// GodIslandMoveTo executes /bonglai_move|/thienma_move: march to cityID in
// one event variant, no fighting.
func (p *EventPlayer) GodIslandMoveTo(which int, cityID int32) error {
	ev, activityID, err := p.godIslandOrder(which, cityID)
	if err != nil {
		return err
	}
	p.godIslandMove(ev, activityID, cityID)
	log.Println(p.Username, "[GodIsland/"+ev.Name+"] nhận lệnh hành quân tới thành", cityID)
	return nil
}

// GodIslandAttackTo executes /bonglai_attack[_thu_ve] |
// /thienma_attack[_thu_ve]: march to cityID on one event variant, then start
// auto-attacking it. onlyNpc=true fights only the NPC guards (thủ vệ,
// ..._attack_thuve); onlyNpc=false also engages the union players present
// (..._attack — friendly fire on other players' unions, including this same
// leader's own event accounts, is the leader's problem).
func (p *EventPlayer) GodIslandAttackTo(which int, cityID int32, onlyNpc bool) error {
	ev, activityID, err := p.godIslandOrder(which, cityID)
	if err != nil {
		return err
	}
	if err := p.godIslandNotHome(ev, cityID); err != nil {
		return err
	}
	p.godIslandMove(ev, activityID, cityID)

	p.giSend(ev, GOD_ISLAND_AUTO_ATK_CMD, "", &proto.GodIslandGameAutoAttackReqMsg{
		ActivityId: activityID,
		TargetId:   cityID,
		OnlyNpc:    pb.Bool(onlyNpc),
	})

	log.Println(p.Username, "[GodIsland/"+ev.Name+"] nhận lệnh tấn công thành", cityID, "onlyNpc =", onlyNpc)
	return nil
}

// GodIslandUseFruit executes /bonglai_canqua|/thienma_canqua: consume count
// of this account's fruits of ONE event variant to boost its power
// (S_GOD_ISLAND_GAME_USE_FRUIT, 206837 GodIslandUseFruitReq{activityId,
// useCount}). Fire and forget like the other orders; the 1ab5 response
// carries ret + new ball power which we don't act on — an invalid count is
// the server's to reject.
func (p *EventPlayer) GodIslandUseFruit(which int, count int32) error {
	ev, activityID, err := p.godIslandActivity(which)
	if err != nil {
		return err
	}
	if count <= 0 {
		return fmt.Errorf("số quả không hợp lệ")
	}
	p.giSend(ev, GOD_ISLAND_USE_FRUIT_CMD, SERVER_GOD_ISLAND_USE_FRUIT, &proto.GodIslandUseFruitReq{
		ActivityId: activityID,
		UseCount:   count,
	})
	log.Println(p.Username, "[GodIsland/"+ev.Name+"] nhận lệnh dùng", count, "quả")
	return nil
}

// GodIslandHealPower executes /bonglai_hoimau|/thienma_hoimau: drink from the
// blood ball of ONE event variant (S_GOD_ISLAND_GAME_USE_SPIRITUALBALL,
// 206827 GodIslandGameUseSpiritualBallReqMsg{activityId, usePower}).
// Live-verified semantics: usePower drains the ball reservoir (ballCurPower)
// 1:1 and raises the unit's HP (curPower) 1:1, and ballMaxPower is a per-draw
// ceiling the reservoir may exceed. So the draw is min(HP need, reservoir,
// per-draw ceiling). "máu đã đầy" comes from HP alone; an empty reservoir gets
// its own message — conflating them once told leaders a full ball was dry. A
// dead unit refuses up-front with its revive countdown: the server rejects
// the drink until reviveTime passes (live: ret=6852, no state change), and a
// marching unit refuses too — both mirror the client's drink-button gates.
// Caches come from the ball-info poll, the 3s INFO_LOAD poll, and every
// ..._canqua response, so right after login a retry may be needed. No
// optimistic bumps: the 6827 answer reconciles both caches.
func (p *EventPlayer) GodIslandHealPower(which int) error {
	ev, activityID, err := p.godIslandActivity(which)
	if err != nil {
		return err
	}
	p.GodIslandMu.Lock()
	if left, dead := ev.reviveLeft(); dead {
		p.GodIslandMu.Unlock()
		return fmt.Errorf("quân đang chết, chờ hồi sinh sau %dm%02ds",
			int(left.Minutes()), int(left.Seconds())%60)
	}
	// Same precondition the client's drink button enforces: drinking needs a
	// stationary unit (or a corpse past its revive cooldown), never a march.
	if ev.Moving {
		p.GodIslandMu.Unlock()
		return fmt.Errorf("quân đang hành quân, không hồi máu được")
	}
	ballKnown, pool, perDraw := ev.BallKnown, ev.BallCur, ev.BallMax
	powerKnown, powCur, powMax := ev.PowerKnown, ev.PowerCur, ev.PowerMax
	p.GodIslandMu.Unlock()
	if !ballKnown {
		return fmt.Errorf("chưa đọc được thông số máu, đợi chút rồi thử lại")
	}
	usePower := pool
	if usePower <= 0 {
		return fmt.Errorf("linh lực cầu đã cạn, hãy %scanqua trước", ev.CmdPrefix)
	}
	if powerKnown {
		need := powMax - powCur
		if need <= 0 {
			return fmt.Errorf("máu đã đầy")
		}
		if need < usePower {
			usePower = need
		}
	}
	if perDraw > 0 && usePower > perDraw {
		usePower = perDraw
	}
	p.giSend(ev, GOD_ISLAND_USE_BALL_CMD, SERVER_GOD_ISLAND_USE_BALL,
		&proto.GodIslandGameUseSpiritualBallReqMsg{
			ActivityId: activityID,
			UsePower:   pb.Int64(usePower),
		})
	log.Println(p.Username, "[GodIsland/"+ev.Name+"] nhận lệnh hồi máu, usePower =", usePower)
	return nil
}

// GodIslandAccelerate executes /bonglai_tangtoc|/thienma_tangtoc: buy one
// more move speed-up on ONE event variant
// (S_GOD_ISLAND_GAME_ACCELERATE_MOVE, 206849
// GodIslandGameAcclerateMoveReqMsg{activityId, speedUpCount}). The request
// carries the CURRENT speedUpCount as the server's validation baseline; the
// server increments it and echoes the new count in the 1ac1 response
// (giHandleAccelerateResp), which refreshes the cache — the 3s INFO_LOAD poll
// is the other writer. Only meaningful while marching: the server requires
// myRouteInfo, which a prior MOVE creates, so we refuse up-front too.
// giAccelDedupGap is the double-press guard: while a press's 1ac1 answer is
// still in flight (or simply seconds away), a second press would echo the
// stale baseline and be refused by the server — and a refusal of a HUMAN
// press now alerts the leader, so re-click spam would alert-spam too.
const giAccelDedupGap = 2 * time.Second

// giAccelAlertWindow is how long after a press its refusal still counts as
// "the button the human just clicked failed" (alert worthy). Past it, a late
// or stray 1ac1 is engine noise and only hits the log.
const giAccelAlertWindow = 10 * time.Second

func (p *EventPlayer) GodIslandAccelerate(which int) error {
	ev, activityID, err := p.godIslandActivity(which)
	if err != nil {
		return err
	}
	p.GodIslandMu.Lock()
	moving := ev.Moving
	count := ev.SpeedUp
	// standing army first — that refusal is REAL and must surface even
	// inside the dedup gap; only a marching re-click is engine-swallowed.
	if !moving {
		p.GodIslandMu.Unlock()
		return fmt.Errorf("quân đang không hành quân (%smove trước)", ev.CmdPrefix)
	}
	if time.Since(ev.AccelPressAt) < giAccelDedupGap {
		p.GodIslandMu.Unlock()
		debugf("[%s] [GodIsland/%s] bỏ lệnh tăng tốc trùng (answer trước còn đang chờ)", p.Username, ev.Name)
		return nil
	}
	ev.AccelPressAt = time.Now()
	p.GodIslandMu.Unlock()

	p.giSend(ev, GOD_ISLAND_ACCELERATE_CMD, SERVER_GOD_ISLAND_ACCELERATE,
		&proto.GodIslandGameAcclerateMoveReqMsg{
			ActivityId:   activityID,
			SpeedUpCount: pb.Int32(count),
		})
	log.Println(p.Username, "[GodIsland/"+ev.Name+"] nhận lệnh tăng tốc, speedUpCount =", count)
	return nil
}

// forceNotify is sendNotify WITHOUT the p.Notify gate — for alerts the leader
// must always receive even while the account chats silently (owner
// 2026-10-21 async-order round: a button press the leader clicked himself
// and the server refused must surface, success or silence otherwise). The
// EventPlayer session is built with Notify=false (telebot_event.go) but its
// NotiChan is the shared t.notiChan, so this is the leader-chat path. Like
// TeleBot.pushNotify it never blocks: a full buffer drops the notice rather
// than wedging the caller.
func (p *EventPlayer) forceNotify(msg string) {
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

func (p *EventPlayer) sendNotify(msg string) {
	if p.Notify {
		name := p.Name
		if name == "" {
			name = p.Username
		}
		p.NotiChan <- NotifyMGS{
			MSG:    name + ": " + msg,
			ChatID: p.ChatID,
		}
	}
}
