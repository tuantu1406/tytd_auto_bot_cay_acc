package tytd_go

import (
	"context"
	"fmt"
	"log"
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
			return
		case msg := <-p.writeChan:
			p.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			err := p.conn.WriteMessage(2, msg)
			if err != nil {
				p.cancel()
				log.Println(p.Username, "can't write, disconnect")
				return
			}
			capturePacketFrame(p.Username, "out", msg)
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
	defer func() {
		p.Running = false
		p.notifyRunning()
	}()
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
				if p.ctx.Err() == nil {
					log.Printf("[EventReader] %s websocket read error: %v", p.Username, err)
				}
				p.cancel() // trigger shutdown
				return
			}
			capturePacketFrame(p.Username, "in", message)
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
