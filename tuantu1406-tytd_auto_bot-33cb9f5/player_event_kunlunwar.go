package tytd_go

// Event-account side of Côn Lôn Tranh Bá (KunlunWar, type 199) — the
// minimal SCENE transport one-shot orders need: EnterGame (with the 15s
// heartbeat that keeps the account in the scene), self-position updates
// (219933) and Move orders (219904). Unlike the leader's Player.ku there is
// no board cache here: the map still streams through the main account;
// event accounts only "be somewhere" on command (/kunlun_follow).

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// kuHeartbeatEvery is the client's own keep-in-scene cadence (219917).
const kuHeartbeatEvery = 15 * time.Second

// Auto free-rebirth cadence (mirror of HolyLand's rebirth loop). The
// client gates its free revive button on backHomeMsg.time +
// KUNLUNWAR_FREE_REVIVE_CD — an ACTIVITY CONFIG value the client fetches
// separately, so the bot cannot precompute the deadline and instead probes
// with server-rejected free 219915s on a backoff (a too-early free revive
// costs nothing). kuMaxRebirthTries bounds the loop at ~10 min of probing;
// after that the status row keeps showing ☠️ until a push flips the point.
const (
	kuRebirthBackoff  = 30 * time.Second
	kuMaxRebirthTries = 20
)

// kuEventState is one event account's Côn Lôn scene state (guard: KuMu).
type kuEventState struct {
	ActivityID   int32
	SceneIn      bool  // EnterGame accepted
	heartStarted bool  // heartbeat goroutine already running
	Point        int32 // own posIndex; 0 = beenKill (client convention)
	HomeCity     int32 // EnterGameResp.home
	Energy       int32
	// FruitItem is the spirit-fruit item id OBSERVED in this account's own
	// 19911 claim reward ("105248=6") — preferred when known. 0 = never seen
	// this run (harvested on the real client, or restart cleared it): the eat
	// op then falls back to kuFruitItemObserved and lets the server arbitrate.
	FruitItem int32
	// FruitBag is the ESTIMATED fruit count in the bag while the bot runs:
	// +=count on EVERY receipt that names the fruit (19911 claim AND 19912
	// welfare — both pay "id=count|…" strings), -=eatSendNum on each ret=0
	// 19926 answer. Bag is only trusted when FruitItem!=0.
	// v3 eat loop (customer bug 2026-10-07 "cắn có 2 quả trong khi túi vẫn
	// còn": the old hard cap of 2 assumed 5 NL/fruit and the north heals
	// less, so one press stopped below the target): eatSendNum = in-flight
	// packet num (deducted on success), eatBase = energy before it,
	// eatChase keeps biting (bounded kuEatMaxRounds) until Energy reaches
	// kuFruitTarget, and FruitHeal is the LEARNED heal-per-fruit from the
	// verdicts — guess kuFruitHeal until the first observed sample.
	FruitBag   int32
	FruitHeal  int32
	eatSendNum int32
	eatBase    int32
	eatChase   bool
	// eatChaseAt stamps when the current chase armed. The chase is cleared
	// ONLY by the 4dd6 verdict — if that answer is ever lost (account seated
	// on the other board mis-routes it, or the server stays silent for an
	// out-of-scene eat), eatChase would stay true forever and every later
	// press would silently no-op: the 2026-11-04 customer report "nút cắn
	// quả không work" on both boards. A press that finds a chase older than
	// kuEatChaseStuck re-arms instead of dying (audit 2026-11-04).
	eatChaseAt time.Time
	eatRounds  int
	enterCh    chan []byte // one-shot broadcast seat for the EnterGame answer
	enterErr   error
	// seats parks one reply channel per response msgType so an account can
	// answer the 219919 mapstream tick without an *Player session (event-
	// only fleets — bug report 2026-10-05: "not see kunlun log" at all,
	// because only regular sessions could pull and none was logged in).
	seats    map[string]chan []byte
	enterAct *proto.KunlunWarEnterActivityResp
	// Live-march ledger for the /kunlun mini app: this account sits IN the
	// scene (heartbeat), so 219943 pushes stream past it constantly — even
	// though the board snapshot itself comes from the 219919 pull below.
	Routes  map[int64]kuRouteRec
	MyUnion int64
	MiniRaw []byte                               // last 219919 answer (slot/name source)
	Unions  map[int64]*proto.KunlunUnionBaseData // union id → name
	// Rebirth bookkeeping (client BackHome popup 复活, 219915): ReviveAt is
	// the server-ms stamp of the death (219940 reviveTime, seeded from
	// EnterGame's backHomeMsg.time when the army was already dead at join);
	// 0 = alive / no death record. freeUsed/itemUsed mirror the server's
	// daily reborn counters (they count USES, capped by an activity config
	// the bot does not receive — the server is the budget judge).
	ReviveAt int64
	// treeAt is the last fruit-tree auto poll stamp (kuTreeTick, user
	// 2026-10-07: "auto nhận quà cây bang với tưới cây"). Zero = never.
	treeAt         time.Time
	lastRebirthTry time.Time
	rebirthTries   int
	manualRebirth  bool // the next 4dbb answer belongs to a user command
	freeUsed       int32
	itemUsed       int32
	// SpeedUp is the client's march speed-up counter (EnterGame field 5,
	// echoed back incremented by the 19918 answer). The accelerate request
	// must carry it as the server's validation baseline — same protocol
	// shape as Holy Land's 209531.
	SpeedUp int32
}

func (p *EventPlayer) kuState() *kuEventState {
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	if p.ku == nil {
		p.ku = &kuEventState{}
	}
	return p.ku
}

// KuKunlunActivityID is the live type-199 instance id (0 = closed) — the
// same activity-list window the regular-account gate uses.
func (p *EventPlayer) KuKunlunActivityID() int32 {
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeKunlunWar) {
		if e.status(now) == "active" {
			return e.ActivityID
		}
	}
	return 0
}

// KunlunWarEventOpen gates the /kunlun_follow broadcast rows.
func (p *EventPlayer) KunlunWarEventOpen() bool { return p.KuKunlunActivityID() != 0 }

// KunlunStatus is the one-line per-account view used by the member tables:
// everything lives in the scene state the EnterGame/219933 pushes keep warm
// (Point 0 is the client's beenKill convention), so there is no DB fallback.
func (p *EventPlayer) KunlunStatus() string {
	if !p.Running {
		return "⏹️ chưa login"
	}
	st := p.kuState()
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	if st.ActivityID == 0 {
		return "⏳ Côn Lôn chưa mở"
	}
	if !st.SceneIn {
		return "🏔 Côn Lôn: ngoài map (/kunlun_follow để vào)"
	}
	if st.Point == 0 {
		if st.rebirthTries >= kuMaxRebirthTries {
			return "☠️ Côn Lôn: quân đang chết (auto free hết lượt thử — /kunlun_hoisinh hoặc hồi trong game)"
		}
		return "☠️ Côn Lôn: quân đang chết · đang tự hồi sinh miễn phí"
	}
	bag := "" // giỏ quả chỉ hiện khi bot đếm được (nhìn thấy receipt 19911)
	if st.FruitItem != 0 {
		bag = fmt.Sprintf(" · 🍈 %d", st.FruitBag)
	}
	return fmt.Sprintf("🏔 Côn Lôn: ô %d (%d,%d) · NL %d · nhà %d%s",
		st.Point, st.Point%61, st.Point/61, st.Energy, st.HomeCity, bag)
}

// kuRoundTrip claims the seat for one response type, fires the request and
// waits — the EventPlayer mirror of Player.kuRoundTrip (same errors).
func (p *EventPlayer) kuRoundTrip(cmd, wantType string, body []byte, timeout time.Duration) ([]byte, error) {
	st := p.kuState()
	p.KuMu.Lock()
	if st.seats == nil {
		st.seats = map[string]chan []byte{}
	}
	if st.seats[wantType] != nil {
		p.KuMu.Unlock()
		return nil, errors.New("có yêu cầu Côn Lôn đang chờ, thử lại sau")
	}
	ch := make(chan []byte, 1)
	st.seats[wantType] = ch
	p.KuMu.Unlock()
	defer func() {
		p.KuMu.Lock()
		if st.seats[wantType] == ch {
			delete(st.seats, wantType)
		}
		p.KuMu.Unlock()
	}()
	// Claim the kunlun side of the SERVER_KUNLUN_* shared-hex routing BEFORE
	// the write (event-account twin of Player.kuRoundTrip — Đỉnh Bắc Minh
	// answers on the same hexes and the reader dispatches by warKindEP).
	p.setWarKindEP(warKindKunlun)
	p.kuSendMu.Lock()
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
		p.kuSendMu.Unlock()
	default:
		p.kuSendMu.Unlock()
		return nil, errKuGone
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, errKuTimeout
	case <-p.ctx.Done():
		return nil, errKuGone
	}
}

// kuDeliverFrame hands a response to the waiting seat (wsHandler goroutine).
// Unclaimed answers are dropped — same no-log idiom as the other pushes.
func (p *EventPlayer) kuDeliverFrame(wantType string, data []byte) {
	st := p.kuState()
	p.KuMu.Lock()
	ch := st.seats[wantType]
	p.KuMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// RequestKunlunEnterActivity caches floor+group — the 219919 request must
// carry BOTH or the server answers a silently EMPTY board (live 710564).
func (p *EventPlayer) kuRefreshEnter(timeout time.Duration) error {
	id := p.KuKunlunActivityID()
	if id == 0 {
		return errKuClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarEnterActivityReq{ActivityId: &id})
	raw, err := p.kuRoundTrip(KUNLUN_ENTER_ACTIVITY_CMD, SERVER_KUNLUN_ENTER_ACTIVITY, body, timeout)
	if err != nil {
		return err
	}
	var rsp proto.KunlunWarEnterActivityResp
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		return err
	}
	if rsp.GetRet() != 0 {
		return fmt.Errorf("Côn Lôn EnterActivity ret=%d", rsp.GetRet())
	}
	st := p.kuState()
	p.KuMu.Lock()
	st.enterAct = &rsp
	p.KuMu.Unlock()
	return nil
}

// RequestKunlunMiniMap is the event-account mapstream pull: whole-board
// 219919 snapshot for OUR floor+group, EnterActivity asked on demand.
func (p *EventPlayer) RequestKunlunMiniMap(timeout time.Duration) ([]byte, error) {
	id := p.KuKunlunActivityID()
	if id == 0 {
		return nil, errKuClosed
	}
	st := p.kuState()
	p.KuMu.Lock()
	floor, group := int32(0), int32(0)
	if st.enterAct != nil {
		floor, group = st.enterAct.GetFloor(), st.enterAct.GetGroup()
	}
	p.KuMu.Unlock()
	if group == 0 {
		if err := p.kuRefreshEnter(timeout); err != nil {
			return nil, err
		}
		p.KuMu.Lock()
		if st.enterAct != nil {
			floor, group = st.enterAct.GetFloor(), st.enterAct.GetGroup()
		}
		p.KuMu.Unlock()
	}
	body, _ := pb.Marshal(&proto.KunlunWarMiniMapInfoReqMsg{ActivityId: &id, Floor: &floor, Group: &group})
	raw, err := p.kuRoundTrip(KUNLUN_MINI_MAP_INFO_CMD, SERVER_KUNLUN_MINI_MAP_INFO, body, timeout)
	if err != nil {
		return nil, err
	}
	// Keep the answer for the route ledger's slot/name lookups (the mini
	// app frame colors marches with the SAME palette slots the PNG tints).
	var rsp proto.KunlunWarMiniMapInfoRespMsg
	if pb.Unmarshal(raw, &rsp) == nil {
		st := p.kuState()
		p.KuMu.Lock()
		st.MiniRaw = raw
		if st.Unions == nil {
			st.Unions = map[int64]*proto.KunlunUnionBaseData{}
		}
		for _, u := range rsp.GetBaseData() {
			st.Unions[u.GetUnionId()] = u
		}
		p.KuMu.Unlock()
	}
	return raw, nil
}

// RequestKunlunGameBlockInfo is the event account's view-window detail pull
// (219903): per-tile displayInfo (WHO stands where, with nicknames), the
// cities' real HP, and every march inside the window. The scene seat is
// ensured first — an event account sitting in the scene is the intended
// state. The route rows fold into the same ledger the 219943 push maintains
// (plus the 5 s staleness prune), so they reach the /kunlun page through the
// existing SSE tick.
func (p *EventPlayer) RequestKunlunGameBlockInfo(blocks []int32, timeout time.Duration) (*proto.KunlunWarGameBlockInfoResp, error) {
	if len(blocks) == 0 {
		return nil, fmt.Errorf("không có ô nào để hỏi")
	}
	id := p.KuKunlunActivityID()
	if id == 0 {
		return nil, errKuClosed
	}
	if err := p.kuEnsureScene(id, 8*time.Second); err != nil {
		return nil, err
	}
	st := p.kuState()
	p.KuMu.Lock()
	floor, group := int32(0), int32(0)
	if st.enterAct != nil {
		floor, group = st.enterAct.GetFloor(), st.enterAct.GetGroup()
	}
	p.KuMu.Unlock()
	if group == 0 {
		if err := p.kuRefreshEnter(timeout); err != nil {
			return nil, err
		}
		p.KuMu.Lock()
		if st.enterAct != nil {
			floor, group = st.enterAct.GetFloor(), st.enterAct.GetGroup()
		}
		p.KuMu.Unlock()
	}
	body, _ := pb.Marshal(&proto.KunlunWarGameBlockInfoReq{
		ActivityId: &id, BlockVec: blocks, FloorId: &floor, GroupId: &group,
	})
	raw, err := p.kuRoundTrip(KUNLUN_GAME_BLOCK_INFO_CMD, SERVER_KUNLUN_GAME_BLOCK_INFO, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarGameBlockInfoResp
	if pb.Unmarshal(raw, &rsp) != nil {
		return nil, fmt.Errorf("trả lời Côn Lôn block-info không đọc được")
	}
	if rsp.GetRet() != 0 {
		return nil, fmt.Errorf("Côn Lôn block info ret=%d", rsp.GetRet())
	}
	p.KuMu.Lock()
	if st.Routes == nil {
		st.Routes = map[int64]kuRouteRec{}
	}
	if n := kuFoldRouteInfos(st.Routes, rsp.GetRouteInfo()); n > 0 {
		debugf("[%s] [Kunlun] 219903: %d march(s) folded, ledger=%d", p.Username, n, len(st.Routes))
	}
	kuPruneRoutes(st.Routes)
	p.KuMu.Unlock()
	return &rsp, nil
}

func (p *EventPlayer) kuSend(cmd string, msg pb.Message) {
	payload, err := pb.Marshal(msg)
	if err != nil {
		log.Printf("[%s] [Kunlun] marshal %s: %v", p.Username, cmd, err)
		return
	}
	p.kuSendMu.Lock()
	p.write(pack.Pack(cmd, payload, p.UID))
	p.kuSendMu.Unlock()
}

// ---- reader-goroutine handlers ----------------------------------------------

func (p *EventPlayer) kuHandleEnterGame(data []byte) {
	var rsp proto.KunlunWarEnterGameResp
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	if rsp.GetRet() != 0 {
		st.enterErr = fmt.Errorf("vào trận Côn Lôn thất bại (ret=%d)", rsp.GetRet())
	} else {
		st.SceneIn = true
		st.Point = rsp.GetMyPlayerInfo().GetPos()
		st.HomeCity = rsp.GetHome()
		st.Energy = rsp.GetEnergy()
		st.SpeedUp = rsp.GetSpeedUpCount()
		if u := rsp.GetMyPlayerInfo().GetUnionId(); u != 0 {
			st.MyUnion = u
		}
		// Joining while dead: myPlayerInfo carries the death context
		// (backHomeMsg.time = stamp of death) — seed the rebirth loop.
		if bm := rsp.GetMyPlayerInfo().GetBackHomeMsg(); bm != nil {
			st.ReviveAt = bm.GetTime()
			st.rebirthTries = 0
			st.freeUsed = bm.GetFreeRebirthCount()
			st.itemUsed = bm.GetItemRebirthCount()
		} else {
			st.ReviveAt = 0
		}
	}
	if st.enterCh != nil {
		close(st.enterCh)
		st.enterCh = nil
	}
	startHeart := rsp.GetRet() == 0 && !st.heartStarted
	actID := st.ActivityID
	if startHeart {
		st.heartStarted = true
	}
	p.KuMu.Unlock()
	if startHeart {
		go p.kuHeartbeatLoop(actID)
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [Kunlun] EnterGame ret=%d", p.Username, rsp.GetRet())
	}
}

func (p *EventPlayer) kuHeartbeatLoop(actID int32) {
	t := time.NewTicker(kuHeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
			p.kuSend(KUNLUN_HEART_BEAT_CMD, &proto.KunlunWarHeartBeatReqMsg{ActivityId: &actID})
			p.kuTryAutoRebirth(time.Now())
			// Guild-tree auto (user 2026-10-07): own goroutine + treeAt gate,
			// so the blocking 219910 read never delays a heartbeat.
			go p.kuTreeTick(time.Now())
		}
	}
}

// kuTryAutoRebirth fires the FREE revive (219915 useItem=false — NEVER an
// item, the standing item ban holds here) once the army is dead in-scene and
// there is a death stamp. Gate + backoff mirror HolyLand's rebirth loop;
// with KUNLUNWAR_FREE_REVIVE_CD unavailable the first probe may legitimately
// draw a server refusal — that is logged, retried on the backoff, capped.
func (p *EventPlayer) kuTryAutoRebirth(now time.Time) {
	st := p.kuState()
	p.KuMu.Lock()
	fire := st.SceneIn && st.Point == 0 && st.ReviveAt > 0 &&
		st.rebirthTries < kuMaxRebirthTries && now.Sub(st.lastRebirthTry) >= kuRebirthBackoff
	if fire {
		st.rebirthTries++
		st.lastRebirthTry = now
	}
	try := st.rebirthTries
	p.KuMu.Unlock()
	if !fire {
		return
	}
	actID := p.KuKunlunActivityID()
	if actID == 0 {
		return
	}
	useItem := false
	p.kuSend(KUNLUN_BACK_HOME_CMD, &proto.KunlunWarBackHomeReq{ActivityId: &actID, UseItem: &useItem})
	log.Printf("[%s] [Kunlun] tự hồi sinh MIỄN PHÍ (lần %d/%d)", p.Username, try, kuMaxRebirthTries)
}

// kuHandleMyGameInfo keeps the authoritative stand-point (219933). The
// client treats pos 0 as beenKill, so 0 is stored, not skipped.
func (p *EventPlayer) kuHandleMyGameInfo(data []byte) {
	var rsp proto.KunlunWarMyGameInfoSyncMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	changed := st.Point != rsp.GetLocation()
	st.Point = rsp.GetLocation()
	if st.Point == 0 {
		if st.ReviveAt == 0 {
			// Dead without a BeenKill push (or one we could not attribute):
			// stamp now so the auto-rebirth loop knows the death is real.
			st.ReviveAt = time.Now().UnixMilli()
			st.rebirthTries = 0
		}
	} else {
		st.ReviveAt = 0 // army is back on the board — rebirth cycle over
		st.rebirthTries = 0
	}
	p.KuMu.Unlock()
	if changed {
		debugf("[%s] [Kunlun] vị trí %d", p.Username, st.Point)
	}
}

// kuHandleMove logs the server's verdict on a march; unreachable targets
// (ret 19944) and energy refusals land here, never in the order's return.
func (p *EventPlayer) kuHandleMove(data []byte) {
	var rsp proto.KunlunWarMoveResp
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		to := int32(0)
		if r := rsp.GetRouteInfo(); r != nil {
			to = r.GetToPoint()
		}
		log.Printf("[%s] [Kunlun]Move ret=%d (đích %d)", p.Username, rsp.GetRet(), to)
		return
	}
	// Accepting the march immediately puts OUR line on everyone's board;
	// the 219943 broadcast that echoes it back arrives with the same fields,
	// so seeding the ledger here closes the gap between order and animation.
	if ri := rsp.GetRouteInfo(); ri != nil && ri.GetPlayerId() != 0 {
		st := p.kuState()
		p.KuMu.Lock()
		if st.Routes == nil {
			st.Routes = map[int64]kuRouteRec{}
		}
		st.Routes[ri.GetPlayerId()] = kuRouteRec{
			From: ri.GetFromPoint(), To: ri.GetToPoint(),
			Time: ri.GetTime(), Arrival: ri.GetArrival(), UnionID: st.MyUnion,
		}
		p.KuMu.Unlock()
	}
	log.Printf("[%s] [Kunlun] hành quân tới %d, tới nơi sau %dms", p.Username,
		rsp.GetRouteInfo().GetToPoint(), rsp.GetRouteInfo().GetArrival()-rsp.GetRouteInfo().GetTime())
}

// kuFoldRoutes parks the 219943 live-march broadcast into the mini app
// ledger. Reader-goroutine safe: never blocks.
func (p *EventPlayer) kuFoldRoutes(data []byte) {
	st := p.kuState()
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	if st.Routes == nil {
		st.Routes = map[int64]kuRouteRec{}
	}
	if n := kuBuildRouteRecs(st.Routes, data); n > 0 {
		debugf("[%s] [Kunlun] 219943: %d march(s) folded, ledger=%d", p.Username, n, len(st.Routes))
	}
	kuPruneRoutes(st.Routes)
}

// kuFoldLines merges 219944 queue rows into this account's march ledger —
// the synthesized march-HOME legs 219943 omits (see kuFoldLineInfos). Feeds
// the same ledger mapstream merges across seated accounts.
func (p *EventPlayer) kuFoldLines(data []byte) {
	var rsp proto.KunlunWarGameLineInfoSyncMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	if st.Routes == nil {
		st.Routes = map[int64]kuRouteRec{}
	}
	if n := kuFoldLineInfos(st.Routes, rsp.GetLineInfo()); n > 0 {
		debugf("[%s] [Kunlun] 219944: %d home-march(es) folded, ledger=%d", p.Username, n, len(st.Routes))
	}
	kuPruneRoutes(st.Routes)
}

// kuRoutesSnapshot is the event account's march ledger for the mapstream
// frame — the regular-account twin above, shared fold logic.
func (p *EventPlayer) kuRoutesSnapshot(now time.Time) []gimap.KuRoute {
	st := p.kuState()
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	return kuRoutesFrom(st.Routes, st.MyUnion, int64(p.UID), st.MiniRaw, st.Unions, now)
}

func (p *EventPlayer) kuHandleBeenKill(data []byte) {
	var rsp proto.KunlunWarBeenKillSyncMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	// Only a death of THIS account zeroes the ledger — the push can carry
	// other players' kills too (targetId attribution is unverified live).
	if rsp.GetTargetId() != 0 && int64(p.UID) != rsp.GetTargetId() {
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	st.Point = 0
	st.ReviveAt = rsp.GetReviveTime()
	st.rebirthTries = 0
	if tid := rsp.GetTargetId(); tid != 0 {
		delete(st.Routes, tid) // a dead army's march vanishes from the board
	}
	p.KuMu.Unlock()
	by := rsp.GetTargetName()
	if by == "" {
		if rsp.GetKillByNPc() {
			by = "NPC"
		} else {
			by = "?"
		}
	}
	log.Printf("[%s] [Kunlun] tử trận vì %s, hồi sinh %d", p.Username, by, rsp.GetReviveTime())
	p.kuTryAutoRebirth(time.Now()) // free revive now when the stamp says due
}

// ---- orders ----------------------------------------------------------------

// kuEnsureScene joins the game scene if not already inside, waiting (bounded)
// for the 219902 answer. The broadcast enterCh wakes every concurrent caller.
// Refused while the account sits in the Đỉnh Bắc Minh scene: both boards
// answer on the same SERVER_KUNLUN_* hexes, a second war scene would
// cross-deliver answers (transport invariant — see warKindEP).
func (p *EventPlayer) kuEnsureScene(actID int32, to time.Duration) error {
	if p.bmSeatedEP() {
		return fmt.Errorf("acc đang ngồi trong trận Đỉnh Bắc Minh — rời Bắc Minh trước đã")
	}
	st := p.kuState()
	p.KuMu.Lock()
	if st.SceneIn {
		p.KuMu.Unlock()
		return nil
	}
	st.ActivityID = actID
	if st.enterCh == nil {
		st.enterCh = make(chan []byte)
		st.enterErr = nil
	}
	ch := st.enterCh
	p.KuMu.Unlock()
	p.setWarKindEP(warKindKunlun)
	p.kuSend(KUNLUN_ENTER_GAME_CMD, &proto.KunlunWarEnterGameReq{ActivityId: &actID})
	select {
	case <-ch:
		p.KuMu.Lock()
		err := st.enterErr
		p.KuMu.Unlock()
		return err
	case <-time.After(to):
		return errKuTimeout
	case <-p.ctx.Done():
		return errKuGone
	}
}

// KunlunWarMoveTo marches this account to block posIndex (server validates
// adjacency — an unreachable order fails later in kuHandleMove's log). A
// dead account (stand-point 0 after a joined scene) refuses here instead of
// burning a doomed request.
func (p *EventPlayer) KunlunWarMoveTo(point int32) error {
	return p.KunlunWarMoveToPoint(point, false)
}

// KunlunWarMoveToPoint marches to posIndex y*61+x; attackCity marks the move
// as a city assault (the web map's ⚔ button — gimap.KunlunCityPoint turns a
// city id into its block). The server is the reachability judge (ret 19944
// shows in this account's log via kuHandleMove).
func (p *EventPlayer) KunlunWarMoveToPoint(point int32, attackCity bool) error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.KuKunlunActivityID()
	if actID == 0 {
		return errKuClosed
	}
	if point <= 0 {
		return fmt.Errorf("block %d không hợp lệ", point)
	}
	if err := p.kuEnsureScene(actID, 8*time.Second); err != nil {
		return err
	}
	st := p.kuState()
	p.KuMu.Lock()
	cur, joined := st.Point, st.SceneIn
	p.KuMu.Unlock()
	if !joined {
		return errKuTimeout
	}
	if cur == 0 {
		return fmt.Errorf("quân đang chết chờ hồi sinh (về /kunlun hoặc hồi sinh trong game)")
	}
	if cur == point {
		return nil // already with the leader
	}
	p.kuSend(KUNLUN_MOVE_CMD, &proto.KunlunWarMoveReq{ActivityId: &actID, TargetPoint: &point, AttackCity: &attackCity})
	if attackCity {
		log.Printf("[%s] [Kunlun] tấn công → block %d", p.Username, point)
	} else {
		log.Printf("[%s] [Kunlun] follow bang chủ → block %d", p.Username, point)
	}
	return nil
}

// kuScenePos reports the live stand-point (0 = out of scene or dead) and the
// union id for the mini app's own-side overlays (KuFrame.Mine/MySlots).
func (p *EventPlayer) kuScenePos() (int32, int64) {
	st := p.kuState()
	p.KuMu.Lock()
	defer p.KuMu.Unlock()
	if !st.SceneIn {
		return 0, 0
	}
	return st.Point, st.MyUnion
}

// kuGateStand is the shared transport gate of the two scene orders (explore
// 219905, sweep 219906). POSITION IS NO LONGER CHECKED (user directive
// 2026-10-07: "click vào ô đều có thể chọn full option … không cần kiểm tra
// điều kiện acc đang đứng ở đâu") — every running account receives the order
// and the SERVER judges it (verdicts land in that account's log via
// kuHandleExplore/kuHandleSweep). What remains is only what the wire needs:
// login, an open window, a valid cell, and a seat (ensureScene). NOTE the
// sweep request still carries no target field server-side, so an army
// standing elsewhere sweeps its OWN tile — that is the server's semantics,
// now deliberately accepted.
func (p *EventPlayer) kuGateStand(point int32) error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.KuKunlunActivityID()
	if actID == 0 {
		return errKuClosed
	}
	if point <= 0 {
		return fmt.Errorf("block %d không hợp lệ", point)
	}
	return p.kuEnsureScene(actID, 8*time.Second)
}

// KunlunWarExplorePoint explores the tile the army stands on (219905). The
// game's radial menu shows Khám phá on exactly this tile; the server answers
// the verdict later via kuHandleExplore.
func (p *EventPlayer) KunlunWarExplorePoint(point int32) error {
	if err := p.kuGateStand(point); err != nil {
		return err
	}
	actID := p.KuKunlunActivityID()
	p.kuSend(KUNLUN_EXPLORE_CMD, &proto.KunlunWarExploreReq{ActivityId: &actID, TargetPoint: &point})
	log.Printf("[%s] [Kunlun] khám phá ô %d", p.Username, point)
	return nil
}

// KunlunWarSweepHere sweeps the tile the army stands on (219906, num = sweep
// count). Same standing gate as explore — see kuGateStand for why a target
// field cannot simply be added here.
func (p *EventPlayer) KunlunWarSweepHere(point int32) error {
	if err := p.kuGateStand(point); err != nil {
		return err
	}
	actID := p.KuKunlunActivityID()
	num := int32(1)
	p.kuSend(KUNLUN_SWEEP_CMD, &proto.KunlunWarSweepReq{ActivityId: &actID, Num: &num})
	log.Printf("[%s] [Kunlun] càn quét ô %d", p.Username, point)
	return nil
}

// kuHandleExplore logs an explore verdict (ret≠0 covers cooldown/energy) and
// folds the authoritative energy back into the scene state.
func (p *EventPlayer) kuHandleExplore(data []byte) {
	var rsp proto.KunlunWarExploreResp
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [Kunlun]Explore ret=%d", p.Username, rsp.GetRet())
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	st.Energy = rsp.GetEnergy()
	p.KuMu.Unlock()
	log.Printf("[%s] [Kunlun] khám phá xong · chiếm %d%% · NL còn %d · thưởng %s",
		p.Username, rsp.GetOccupyProgress(), rsp.GetEnergy(), rsp.GetReward())
}

// kuHandleSweep logs a sweep verdict; rewards/energy land in the same field
// names as explore's response (minus the occupy-progress tick).
func (p *EventPlayer) kuHandleSweep(data []byte) {
	var rsp proto.KunlunWarSweepResp
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [Kunlun]Sweep ret=%d", p.Username, rsp.GetRet())
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	st.Energy = rsp.GetEnergy()
	p.KuMu.Unlock()
	log.Printf("[%s] [Kunlun] càn quét xong · NL còn %d · thưởng %s",
		p.Username, rsp.GetEnergy(), rsp.GetReward())
}

// ---- march speed-up (219918, client "加速" on a marching army) ---------------

// KuAccelerate buys one march speed-up for this account (web ⚡ button —
// the game's own op, no item involved: the request only echoes the current
// speed-up counter as the server's validation baseline, exactly like Holy
// Land's 209531). Meaningful only while the army is marching; a standing or
// dead army simply gets a refused 19918 (stays in the log), so we refuse up
// front only for the states we can actually see.
func (p *EventPlayer) KuAccelerate() error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.KuKunlunActivityID()
	if actID == 0 {
		return errKuClosed
	}
	if err := p.kuEnsureScene(actID, 8*time.Second); err != nil {
		return err
	}
	st := p.kuState()
	p.KuMu.Lock()
	cur, count := st.Point, st.SpeedUp
	p.KuMu.Unlock()
	if cur == 0 {
		return fmt.Errorf("quân đang chết chờ hồi sinh")
	}
	p.kuSend(KUNLUN_ACCELERATE_CMD, &proto.KunlunWarAcclerateMoveReqMsg{
		ActivityId:   &actID,
		SpeedUpCount: &count,
	})
	log.Printf("[%s] [Kunlun] nhận lệnh tăng tốc hành quân (speedUpCount=%d)", p.Username, count)
	return nil
}

// kuHandleAccelerate folds the speed-up verdict (19918). Refusals are
// routine (army not marching / counter mismatch) and stay in the log; a
// success advances the counter the next request must echo.
func (p *EventPlayer) kuHandleAccelerate(data []byte) {
	var rsp proto.KunlunWarAcclerateMoveRespMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [Kunlun]Tăng tốc ret=%d", p.Username, rsp.GetRet())
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	st.SpeedUp = rsp.GetSpeedUpCount()
	p.KuMu.Unlock()
	log.Printf("[%s] [Kunlun] tăng tốc hành quân · speedUp=%d · %s",
		p.Username, rsp.GetSpeedUpCount(), rsp.GetCostInfo())
}

// ---- fruit tree (SPAR_INFO 219910 / GET_SPAR_POWER 219911, 天地灵石) ----------

// KunlunWarClaimFruit asks the server to harvest this account's ripe spirit
// fruit (the web 🍈 button, the client's reqReceiveFruitsMsg). The request
// carries ONLY activityId — the payout is entirely server-decided, which is
// why no "5 HP per bite" parameter exists. No item is spent anywhere (the
// item routes GET/SEND_WELFARE 219912/13 and USE_ITEM 219926 stay under the
// standing item ban), so this op is safe to fire from a button press. No
// scene gate: the tree is activity-level; a non-ripe claim simply refuses in
// kuHandleSparPower.
func (p *EventPlayer) KunlunWarClaimFruit() error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.KuKunlunActivityID()
	if actID == 0 {
		return errKuClosed
	}
	p.kuSend(KUNLUN_SPAR_POWER_CMD, &proto.KunlunWarGetSparPowerReqMsg{ActivityId: &actID})
	log.Printf("[%s] [Kunlun] gửi lệnh hái quả Côn Lôn", p.Username)
	return nil
}

// kuHandleSparPower folds the harvest verdict. Ret≠0 = nothing ripe yet —
// routine, stays in the log.
func (p *EventPlayer) kuHandleSparPower(data []byte) {
	var rsp proto.KunlunWarGetSparPowerRespMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [Kunlun]Hái quả ret=%d (chưa có quả chín)", p.Username, rsp.GetRet())
		return
	}
	log.Printf("[%s] [Kunlun] hái quả xong · thưởng %s · exp +%d · quả tới canReceive=%d",
		p.Username, rsp.GetReward(), rsp.GetExp(), rsp.GetCanReceive())
	// The reward string IS the bag receipt ("itemId=count|…") — the only
	// honest source of both the fruit item id AND the count added to the bag
	// for the 219926 eat op.
	if id := kuRewardItemID(rsp.GetReward()); id != 0 {
		cnt := kuRewardCountFor(rsp.GetReward(), id) // sum EVERY matching pair
		st := p.kuState()
		p.KuMu.Lock()
		if st.FruitItem != id {
			st.FruitBag = 0 // different fruit id = a different basket; count fresh
		}
		st.FruitItem = id
		st.FruitBag += cnt
		p.KuMu.Unlock()
	}
}

// kuFruitItemObserved is this activity's spirit-fruit item id, seen LIVE in a
// 19911 reward string ("105248=6") and in the user's own 219926 packet
// capture — observed, never invented. Used as the fallback when the in-memory
// bag id is empty (the account picked its fruit in the real client, or a bot
// restart forgot it); the server stays the arbiter — an account with no fruit
// just answers ret≠0 in its log and nothing is spent.
const kuFruitItemObserved = int32(105248)

// kuRewardItemID takes the first "id=count" pair out of a 19911 reward string
// ("105248=6", possibly ";…"-joined). 0 = nothing parseable — the eat op then
// falls back to kuFruitItemObserved.
func kuRewardItemID(reward string) int32 {
	seg := reward
	if i := strings.IndexAny(seg, ";,"); i >= 0 {
		seg = seg[:i]
	}
	i := strings.IndexByte(seg, '=')
	if i <= 0 {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(seg[:i]), 10, 32)
	if err != nil || n <= 0 {
		return 0
	}
	return int32(n)
}

// kuRewardCountFor sums EVERY "id=count" pair matching id in a reward string
// (19911 AND 19912 both pay multi-item "105248=6|100009=4|…" strings — the
// customer's "túi vẫn còn mà chỉ cắn 2" partly was fruits arriving through the
// welfare channel that the bag ledger had never seen).
func kuRewardCountFor(reward string, id int32) int32 {
	if id == 0 || reward == "" {
		return 0
	}
	total := int32(0)
	for _, seg := range strings.Split(reward, "|") {
		i := strings.IndexByte(seg, '=')
		if i <= 0 {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(seg[:i]), 10, 32)
		if err != nil || n != int64(id) {
			continue
		}
		c, err := strconv.ParseInt(strings.TrimSpace(seg[i+1:]), 10, 32)
		if err != nil || c <= 0 {
			continue
		}
		total += int32(c)
	}
	return total
}

// KunlunWarEatFruit "cắn quả": use N spirit fruits (219926) — the recovery
// amount is the SERVER's item config (the packet carries no HP value), so
// "5hp/phát" lives game-side, not here. Item id: the account's own 19911
// reward when the bot harvested it this run, else kuFruitItemObserved (user
// packet capture 2026-10-06 authorized this exact op; every other item path
// stays banned). NO position condition anywhere — the eat packet carries no
// place field and the client opens this from the activity panel, so one press
// bites from ANYWHERE on the map: standing, marching, or waiting at home
// (user 2026-10-06: "đứng đâu cũng cắn quả được, không cần về nhà").
func (p *EventPlayer) KunlunWarEatFruit(num int32) error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.KuKunlunActivityID()
	if actID == 0 {
		return errKuClosed
	}
	if num <= 0 {
		return nil
	}
	st := p.kuState()
	p.KuMu.Lock()
	item := st.FruitItem
	st.eatSendNum = num // the ret=0 verdict deducts EXACTLY this from FruitBag
	p.KuMu.Unlock()
	if item == 0 {
		item = kuFruitItemObserved // acc hái bằng client thật / restart mất bag id — server quyết
	}
	p.kuSend(KUNLUN_USE_ITEM_CMD, &proto.KunlunWarUseItemReqMsg{ActivityId: &actID, ItemId: &item, Num: &num})
	log.Printf("[%s] [Kunlun] gửi lệnh cắn %d quả (item %d)", p.Username, num, item)
	return nil
}

// Fruit top-up rule for the 🍈Cắn quả button (user 2026-10-07): "acc nào <10
// hp sẽ cắn cho đủ 10, bé hơn thì thôi". v3 after the customer report of the
// same day ("cắn có 2 quả trong khi túi vẫn còn"): the v2 hard cap of 2 rested
// on the 5-NL/fruit guess and STOPPED SHORT when the server heals less — so the
// press now CHASES the target: one bite packet at a time, each sized from the
// account's own LEARNED heal (kuFruitHeal is only the first guess; every ret=0
// verdict divides the observed energy gain by its num into FruitHeal), clamped
// by the counted bag, until Energy ≥ kuFruitTarget, the counted bag is empty,
// a refusal lands, or kuEatMaxRounds bites were fired (never an unbounded item
// drip). Accounts already ≥10 are skipped with NO packet.
const (
	kuFruitHeal     = int32(5)
	kuFruitTarget   = int32(10)
	kuEatMaxRounds  = 6
	kuEatMaxPerSend = int32(20)
	// kuEatChaseStuck bounds how long a chase may sit without its 4dd6
	// verdict before the next press re-arms it (see kuEventState.eatChaseAt;
	// 6 rounds of bite+verdict land well inside this even on a slow link).
	kuEatChaseStuck = 20 * time.Second
)

// kuEatStep computes ONE bite (size = ceil(needs / learned heal), clamped by
// the counted bag) and sends it. Returns the num actually sent (0 = nothing to
// do). Safe to call from verdict handlers — fires at most one packet.
func (p *EventPlayer) kuEatStep() int32 {
	if !p.Running {
		return 0
	}
	if p.KuKunlunActivityID() == 0 {
		return 0
	}
	st := p.kuState()
	p.KuMu.Lock()
	e := st.Energy
	if e < 0 {
		e = 0
	}
	if e >= kuFruitTarget {
		st.eatChase = false
		p.KuMu.Unlock()
		return 0
	}
	heal := st.FruitHeal
	if heal <= 0 {
		heal = kuFruitHeal // chưa quan sát được: assume 5 NL/trái (luật gốc của user)
	}
	num := (kuFruitTarget - e + heal - 1) / heal
	if num > kuEatMaxPerSend {
		num = kuEatMaxPerSend
	}
	if st.FruitItem != 0 { // bag counted this run → "thiếu quả thì cắn số còn lại"
		if st.FruitBag <= 0 {
			st.eatChase = false
			p.KuMu.Unlock()
			debugf("[%s] [Kunlun] cắn quả: giỏ hết quả — dừng", p.Username)
			return 0
		}
		if num > st.FruitBag {
			num = st.FruitBag
		}
	}
	st.eatBase = e
	p.KuMu.Unlock()
	if err := p.KunlunWarEatFruit(num); err != nil {
		return 0
	}
	return num
}

func (p *EventPlayer) KunlunWarEatFruitToTen() (int32, error) {
	if !p.Running {
		return 0, fmt.Errorf("chưa login")
	}
	if p.KuKunlunActivityID() == 0 {
		return 0, errKuClosed
	}
	st := p.kuState()
	p.KuMu.Lock()
	if st.eatChase && !st.eatChaseAt.IsZero() && time.Since(st.eatChaseAt) > kuEatChaseStuck {
		// Chase không bao giờ thấy verdict 4dd6 (lạc sang board khác / server
		// im lặng) — nếu không cho bấm lại thì nút chết vĩnh viễn tới restart
		// (báo cáo khách 2026-11-04 "nút cắn quả không work").
		log.Printf("[%s] [Kunlun] cắn quả: chase kẹt quá %v không thấy verdict — bấm lại nạp chase", p.Username, kuEatChaseStuck)
		st.eatChase = false
		st.eatSendNum = 0
	}
	if st.eatChase { // một press đang tự nạp dở — đừng nhân đôi lệnh
		p.KuMu.Unlock()
		return 0, nil
	}
	if st.Energy >= kuFruitTarget {
		p.KuMu.Unlock()
		return 0, nil // đủ 10 rồi — "bé hơn thì thôi"
	}
	st.eatChase = true
	st.eatChaseAt = time.Now()
	st.eatRounds = 0
	p.KuMu.Unlock()
	return p.kuEatStep(), nil
}

// kuHandleUseItem folds the eat verdict (client Mgr.showAddStrengthGood: a
// ret=0 answer refreshes the account's energy + recover clock), does the bag
// bookkeeping for EXACTLY the in-flight num, learns the heal-per-fruit, and
// continues the chase loop when the target is not reached yet.
func (p *EventPlayer) kuHandleUseItem(data []byte) {
	var rsp proto.KunlunWarUseItemRespMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	if rsp.GetRet() != 0 {
		st.eatChase = false // server nói không là thôi — không nhỏ giọt item
		st.eatSendNum = 0
		p.KuMu.Unlock()
		log.Printf("[%s] [Kunlun]Cắn quả ret=%d (hết quả / chưa tới lượt)", p.Username, rsp.GetRet())
		return
	}
	n := st.eatSendNum
	healObs := rsp.GetEnergy() - st.eatBase
	st.Energy = rsp.GetEnergy()
	if st.FruitItem != 0 && n > 0 {
		st.FruitBag -= n // receipt-confirmed consumption (estimate floor 0)
		if st.FruitBag < 0 {
			st.FruitBag = 0
		}
	}
	if n > 0 && healObs > 0 {
		if unit := healObs / n; unit >= 1 {
			st.FruitHeal = unit // learned: packet num N + energy gain G ⇒ G/N per fruit
		}
	}
	st.eatSendNum = 0
	cont := st.eatChase && st.Energy < kuFruitTarget
	if cont {
		st.eatRounds++
		if st.eatRounds >= kuEatMaxRounds {
			st.eatChase = false
			cont = false
		}
	} else {
		st.eatChase = false
	}
	p.KuMu.Unlock()
	log.Printf("[%s] [Kunlun] cắn quả xong · năng lượng now=%d · giỏ còn ~%d quả · hồi lúc %d",
		p.Username, rsp.GetEnergy(), st.FruitBag, rsp.GetEnergyRecoverTime())
	if cont {
		go p.kuEatStep() // "cắn cho đủ 10" — thêm một phát theo NL vừa được server mộc
	}
}

// RequestKunlunSparInfo pulls the tree's ripeness flag (219910). Used by
// /events/mapview to light the 🍈 button without the page round-tripping the
// game protocol itself. Returns canReceive (1 = ripe).
func (p *EventPlayer) RequestKunlunSparInfo(timeout time.Duration) (int32, error) {
	actID := p.KuKunlunActivityID()
	if actID == 0 {
		return 0, errKuClosed
	}
	rsp, err := p.kuSparInfo(actID, timeout)
	if err != nil {
		return 0, err
	}
	return rsp.GetCanReceive(), nil
}

// kuSparInfo is the raw 219910 round-trip shared by the mapview gate and the
// auto tree loop (full answer: exp, canReceive, pending welfare notices).
func (p *EventPlayer) kuSparInfo(actID int32, timeout time.Duration) (*proto.KunlunWarSparInfoRespMsg, error) {
	body, _ := pb.Marshal(&proto.KunlunWarSparInfoReqMsg{ActivityId: &actID})
	raw, err := p.kuRoundTrip(KUNLUN_SPAR_INFO_CMD, SERVER_KUNLUN_SPAR_INFO, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarSparInfoRespMsg
	if pb.Unmarshal(raw, &rsp) != nil {
		return nil, fmt.Errorf("trả lời Côn Lôn spar-info không đọc được")
	}
	return &rsp, nil
}

// kuTreeEvery is the fruit-tree auto rhythm — user 2026-10-07: "côn lôn auto
// nhận quà cây bang với tưới cây, cần ấn nút cắn quả để hồi máu thôi".
const kuTreeEvery = 60 * time.Second

// kuTreeTick auto-harvests the guild tree (天地灵石). It reads the tree
// (219910), then:
//   - pending ally liquid (info[].status==0) → GET_WELFARE {id:0}, the
//     client's own "collect ALL" (PlantView gotBtn). COLLECTING is free —
//     the ally who SENT the liquid spent the item (SEND_WELFARE 219913),
//     which the bot never sends; the standing item ban is untouched here.
//   - ripe fruit (canReceive==1) → GET_SPAR_POWER 219911, exactly what the
//     🍈 Hái quả button sends (server-decided payout, no item spent).
//
// Only "Cắn quả" (USE_ITEM 219926 eating for HP) stays a manual press, as
// the user asked. Runs on its own goroutine from the heartbeat so the
// blocking round-trip never delays a 219917 beat; treeAt is stamped BEFORE
// the round-trip so overlapping ticks cannot double-send.
func (p *EventPlayer) kuTreeTick(now time.Time) {
	if !p.Running || p.ctx == nil {
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	if !st.treeAt.IsZero() && now.Sub(st.treeAt) < kuTreeEvery {
		p.KuMu.Unlock()
		return
	}
	st.treeAt = now
	p.KuMu.Unlock()
	actID := p.KuKunlunActivityID()
	if actID == 0 {
		return
	}
	rsp, err := p.kuSparInfo(actID, 8*time.Second)
	if err != nil {
		debugf("[%s] [Kunlun] poll cây bang: %v", p.Username, err)
		return
	}
	pending := 0
	for _, w := range rsp.GetInfo() {
		if w.GetStatus() == 0 {
			pending++
		}
	}
	if pending > 0 {
		id := int64(0) // 0 = collect ALL pending notices (client's gotBtn)
		p.kuSend(KUNLUN_GET_WELFARE_CMD, &proto.KunlunWarGetWelfareReqMsg{ActivityId: &actID, Id: &id})
		log.Printf("[%s] [Kunlun] tự tưới cây bang · %d phần quà đồng bang đang chờ", p.Username, pending)
	}
	if rsp.GetCanReceive() == 1 {
		p.kuSend(KUNLUN_SPAR_POWER_CMD, &proto.KunlunWarGetSparPowerReqMsg{ActivityId: &actID})
		log.Printf("[%s] [Kunlun] quà cây đã chín → tự hái", p.Username)
	}
}

// kuHandleWelfare folds the 19912 watering verdict. ret≠0 = nothing was
// pending (a teammate took it first) — routine, stays out of the log.
func (p *EventPlayer) kuHandleWelfare(data []byte) {
	var rsp proto.KunlunWarGetWelfareRespMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		debugf("[%s] [Kunlun] tưới cây ret=%d", p.Username, rsp.GetRet())
		return
	}
	log.Printf("[%s] [Kunlun] tưới cây bang xong · thưởng %s · exp +%d · quả chín=%d",
		p.Username, rsp.GetReward(), rsp.GetExp(), rsp.GetCanReceive())
	// Welfare rewards CAN carry spirit fruit ("105248=6|…") — fold them into
	// the bag, but only when the ledger is already trusted (FruitItem!=0);
	// mixing counted and unknown history would fake a small bag and starve
	// the chase loop (customer report 2026-10-07).
	st := p.kuState()
	p.KuMu.Lock()
	if st.FruitItem != 0 {
		st.FruitBag += kuRewardCountFor(rsp.GetReward(), st.FruitItem)
	}
	p.KuMu.Unlock()
}

// ---- revive (BackHome 219915, client popup 复活) ------------------------------

// KunlunWarRebirth is the manual revive behind /kunlun_hoisinh: useItem=true
// SPENDS one revive item and only ever reaches here through that explicit
// command (the standing item ban otherwise holds — the auto loop only ever
// sends useItem=false). The army must actually be dead; the server answers
// async via kuHandleBackHome, which notifies the leader when the order
// belonged to a command.
func (p *EventPlayer) KunlunWarRebirth(useItem bool) error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.KuKunlunActivityID()
	if actID == 0 {
		return errKuClosed
	}
	if err := p.kuEnsureScene(actID, 8*time.Second); err != nil {
		return err
	}
	st := p.kuState()
	p.KuMu.Lock()
	if st.Point != 0 {
		p.KuMu.Unlock()
		return fmt.Errorf("quân vẫn đang sống")
	}
	st.manualRebirth = true
	st.lastRebirthTry = time.Now() // pause the auto probe while this is in flight
	p.KuMu.Unlock()
	p.kuSend(KUNLUN_BACK_HOME_CMD, &proto.KunlunWarBackHomeReq{ActivityId: &actID, UseItem: &useItem})
	if useItem {
		log.Printf("[%s] [Kunlun] nhận lệnh hồi sinh bằng ITEM", p.Username)
	} else {
		log.Printf("[%s] [Kunlun] nhận lệnh hồi sinh miễn phí", p.Username)
	}
	return nil
}

// kuHandleBackHome reconciles the revive verdict (4dbb). A REFUSAL of an
// auto probe is routine (free CD is an activity config the bot never
// receives, so early probes draw server rejections by design) and stays in
// the log; the verdict of an order that came from /kunlun_hoisinh is
// forwarded to the leader. Success clears the death bookkeeping optimistically
// — the army's return to the board lands with 219933.
func (p *EventPlayer) kuHandleBackHome(data []byte) {
	var rsp proto.KunlunWarBackHomeResp
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	st := p.kuState()
	p.KuMu.Lock()
	manual := st.manualRebirth
	st.manualRebirth = false
	if rsp.GetRet() == 0 {
		st.ReviveAt = 0
		st.rebirthTries = 0
	}
	if bm := rsp.GetBackHomeMsg(); bm != nil {
		st.freeUsed = bm.GetFreeRebirthCount()
		st.itemUsed = bm.GetItemRebirthCount()
	}
	p.KuMu.Unlock()
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [Kunlun]BackHome ret=%d", p.Username, rsp.GetRet())
		if manual {
			p.sendNotify(fmt.Sprintf("⚠️ [Côn Lôn] hồi sinh thất bại (lỗi %d)", rsp.GetRet()))
		}
		return
	}
	log.Printf("[%s] [Kunlun] hồi sinh thành công · đã dùng %d free / %d item",
		p.Username, st.freeUsed, st.itemUsed)
	if manual {
		p.sendNotify("✅ [Côn Lôn] hồi sinh thành công")
	}
}
