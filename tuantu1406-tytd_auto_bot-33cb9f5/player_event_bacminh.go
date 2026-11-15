package tytd_go

// Event-account side of Đỉnh Bắc Minh (type 332) — the event mirror of
// player_event_kunlunwar.go, on the NORTH board. Same engine (opcode family
// 219xxx, server routes by activityId) and the same minimal SCENE transport
// role: EnterGame + 15s heartbeat keep the account seated, 219933 tracks the
// stand-point, /bacminh_follow drives Move.
//
// Differences from Côn Lôn are deliberate and user-approved:
//   - ⚡Tăng tốc (219918) is pressed straight with NO confirm — the server
//     spends one accel item when the bag holds NORTHPOLEWAR_ACCEL_ITEM and
//     only falls back to gems otherwise (user: "tăng tốc thì cứ bấm thôi
//     khỏi cần hỏi").
//   - 🍈Cắn quả (219926 USE_ITEM, wire "00035b16") is authorized for the
//     north board too, button-press-only exactly like Côn Lôn (user
//     2026-10-07: "Thêm cắn quả như Côn Lôn" + packet capture
//     {activityId 9062374, itemId 105363, num 1} → bmFruitItemObserved).
//   - Revive (219915): auto loop stays FREE (useItem=false); the manual
//     path takes [item] like Côn Lôn — user-authorized 2026-10-07 ("Bật như
//     Côn Lôn"), server deducts its KUNLUNWAR_ITEM_REVIVE item (105368 per
//     this window's 21003). The free tree ops (219910 read, 219911 claim,
//     219912 collect-all) mirror kunlun's auto loop.
//   - posIndex decomposes over gimap.BacMinhCols() (119² grid), never 61.
//
// Transport invariant (see warKindEP): this account sits in ONE war scene at
// a time; joining the north scene refuses while Côn Lôn holds the seat and
// vice versa, because both boards answer on the same SERVER_KUNLUN_* hexes.

import (
	"errors"
	"fmt"
	"log"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// bacminhEventState is one event account's north scene state (guard: BmMu).
// Shape mirrors kuEventState, fruit/bag tracking included — the north gained
// the authorized eat op on 2026-10-07.
type bacminhEventState struct {
	ActivityID   int32
	SceneIn      bool
	heartStarted bool
	Point        int32 // own posIndex; 0 = beenKill (client convention)
	HomeCity     int32
	Energy       int32
	// FruitItem is the north spirit-fruit item id OBSERVED in this account's
	// own 19911 claim reward ("id=count"). 0 = never seen this run: the eat
	// press falls back to bmFruitItemObserved (user capture 2026-10-07).
	// FruitBag = estimated bag count while the bot runs (+=count from EVERY
	// receipt naming the fruit — 19911 claim AND 19912 welfare; -=eatSendNum
	// per ret=0 19926 verdict); trusted only when FruitItem!=0 (user
	// 2026-10-07: "lưu hp hiện tại, số quả trong giỏ"). v3 chase fields fix
	// the customer report "cắn có 2 quả trong khi túi vẫn còn" (see
	// KunlunWarEatFruitToTen) — the north heals ≠5 NL/fruit, so the loop
	// learns FruitHeal from verdicts and bites until Energy reaches 10.
	FruitItem      int32
	FruitBag       int32
	FruitHeal      int32
	eatSendNum     int32
	eatBase        int32
	eatChase       bool
	eatChaseAt     time.Time // chase arm stamp — stale presses re-arm (see kuEventState.eatChaseAt)
	eatRounds      int
	enterCh        chan []byte // one-shot broadcast seat for the EnterGame answer
	enterErr       error
	seats          map[string]chan []byte // one-shot seats per response msgType
	enterAct       *proto.KunlunWarEnterActivityResp
	Routes         map[int64]kuRouteRec
	MyUnion        int64
	MiniRaw        []byte                               // last 219919 answer (slot/name source)
	Unions         map[int64]*proto.KunlunUnionBaseData // union id → name
	ReviveAt       int64
	treeAt         time.Time
	lastRebirthTry time.Time
	rebirthTries   int
	manualRebirth  bool // the next 4dbb answer belongs to a user command
	freeUsed       int32
	itemUsed       int32
	SpeedUp        int32 // server's speed-up counter — validation baseline for 219918
}

func (p *EventPlayer) bmStateEP() *bacminhEventState {
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	if p.bm == nil {
		p.bm = &bacminhEventState{}
	}
	return p.bm
}

// setWarKindEP records which war board this session's SERVER_KUNLUN_*
// answers belong to (guard: KuMu — same field-locking as Player.warKind).
func (p *EventPlayer) setWarKindEP(kind string) {
	p.KuMu.Lock()
	p.warKindEP = kind
	p.KuMu.Unlock()
}

// bmSeatedEP / kuSeatedEP are the cross-board seat guards.
func (p *EventPlayer) bmSeatedEP() bool {
	st := p.bmStateEP()
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	return st.SceneIn
}

func (p *EventPlayer) kuSeatedEP() bool {
	p.KuMu.Lock()
	in := p.ku != nil && p.ku.SceneIn
	p.KuMu.Unlock()
	return in
}

// BmActivityID is the live type-332 instance id (0 = closed).
func (p *EventPlayer) BmActivityID() int32 {
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeBacMinh) {
		if e.status(now) == "active" {
			return e.ActivityID
		}
	}
	return 0
}

// BacMinhWarEventOpen gates the /bacminh_follow broadcast rows.
func (p *EventPlayer) BacMinhWarEventOpen() bool { return p.BmActivityID() != 0 }

// BmStatus is the per-account north row for the member tables (mirrors
// KunlunStatus; cell decomp uses the baked 119 width).
func (p *EventPlayer) BmStatus() string {
	if !p.Running {
		return "⏹️ chưa login"
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	if st.ActivityID == 0 {
		return "⏳ Đỉnh Bắc Minh chưa mở"
	}
	if !st.SceneIn {
		return "🧊 Bắc Minh: ngoài map (/bacminh_follow để vào)"
	}
	if st.Point == 0 {
		if st.rebirthTries >= kuMaxRebirthTries {
			return "☠️ Bắc Minh: quân đang chết (auto free hết lượt thử — /bacminh_hoisinh hoặc hồi trong game)"
		}
		return "☠️ Bắc Minh: quân đang chết · đang tự hồi sinh miễn phí"
	}
	cols := gimap.BacMinhCols()
	if cols <= 0 {
		cols = 1
	}
	bag := "" // giỏ quả chỉ hiện khi bot đếm được (nhìn thấy receipt 19911)
	if st.FruitItem != 0 {
		bag = fmt.Sprintf(" · 🍈 %d", st.FruitBag)
	}
	return fmt.Sprintf("🧊 Bắc Minh: ô %d (%d,%d) · NL %d · nhà %d%s",
		st.Point, st.Point%int32(cols), st.Point/int32(cols), st.Energy, st.HomeCity, bag)
}

// bmRoundTripEP claims a seat for one response type, fires the request and
// waits — flipping warKindEP to the north side BEFORE the write (shared-hex
// routing; see player_event_kunlunwar kuRoundTrip's comment).
func (p *EventPlayer) bmRoundTrip(cmd, wantType string, body []byte, timeout time.Duration) ([]byte, error) {
	st := p.bmStateEP()
	p.BmMu.Lock()
	if st.seats == nil {
		st.seats = map[string]chan []byte{}
	}
	if st.seats[wantType] != nil {
		p.BmMu.Unlock()
		return nil, errors.New("có yêu cầu Đỉnh Bắc Minh đang chờ, thử lại sau")
	}
	ch := make(chan []byte, 1)
	st.seats[wantType] = ch
	p.BmMu.Unlock()
	defer func() {
		p.BmMu.Lock()
		if st.seats[wantType] == ch {
			delete(st.seats, wantType)
		}
		p.BmMu.Unlock()
	}()
	p.setWarKindEP(warKindBacMinh)
	p.bmSendMu.Lock()
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
		p.bmSendMu.Unlock()
	default:
		p.bmSendMu.Unlock()
		return nil, errBmGone
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, errBmTimeout
	case <-p.ctx.Done():
		return nil, errBmGone
	}
}

// bmDeliverFrame hands a north response to its waiting seat.
func (p *EventPlayer) bmDeliverFrame(wantType string, data []byte) {
	st := p.bmStateEP()
	p.BmMu.Lock()
	ch := st.seats[wantType]
	p.BmMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// bmDeliver is the event reader's north entry point (routed by warKindEP in
// eventWsHandler): seats get their answer, pushes fold into the ledger.
// The fold set mirrors kuDeliverFrame's push handlers; USE_ITEM answers fold
// the authorized 🍈eat verdict (same shape as Côn Lôn's).
func (p *EventPlayer) bmDeliver(msgType string, data []byte) {
	switch msgType {
	case SERVER_KUNLUN_ENTER_ACTIVITY, SERVER_KUNLUN_MINI_MAP_INFO,
		SERVER_KUNLUN_GAME_BLOCK_INFO, SERVER_KUNLUN_SPAR_INFO:
		p.bmDeliverFrame(msgType, data)
	case SERVER_KUNLUN_ENTER_GAME:
		p.bmHandleEnterGame(data)
	case SERVER_KUNLUN_MY_GAME_SYNC:
		p.bmHandleMyGameInfo(data)
	case SERVER_KUNLUN_ROUTE_SYNC:
		p.bmFoldRoutes(data)
	case SERVER_KUNLUN_LINE_SYNC:
		p.bmFoldLines(data)
	case SERVER_KUNLUN_SPAR_POWER:
		p.bmHandleSparPower(data)
	case SERVER_KUNLUN_GET_WELFARE:
		p.bmHandleWelfare(data)
	case SERVER_KUNLUN_USE_ITEM:
		p.bmHandleUseItem(data)
	case SERVER_KUNLUN_MOVE:
		p.bmHandleMove(data)
	case SERVER_KUNLUN_EXPLORE:
		p.bmHandleExplore(data)
	case SERVER_KUNLUN_SWEEP:
		p.bmHandleSweep(data)
	case SERVER_KUNLUN_ACCELERATE:
		p.bmHandleAccelerate(data)
	case SERVER_KUNLUN_BEEN_KILL_SYNC:
		p.bmHandleBeenKill(data)
	case SERVER_KUNLUN_BACK_HOME:
		p.bmHandleBackHome(data)
	}
}

// bmRefreshEnter caches floor+group for the 219919 pairing rule (event
// mirror of kuRefreshEnter).
func (p *EventPlayer) bmRefreshEnter(timeout time.Duration) error {
	id := p.BmActivityID()
	if id == 0 {
		return errBmClosed
	}
	body, _ := pb.Marshal(&proto.KunlunWarEnterActivityReq{ActivityId: &id})
	raw, err := p.bmRoundTrip(KUNLUN_ENTER_ACTIVITY_CMD, SERVER_KUNLUN_ENTER_ACTIVITY, body, timeout)
	if err != nil {
		return err
	}
	var rsp proto.KunlunWarEnterActivityResp
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		return err
	}
	if rsp.GetRet() != 0 {
		return fmt.Errorf("Bắc Minh EnterActivity ret=%d", rsp.GetRet())
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	st.ActivityID = id
	st.enterAct = &rsp
	p.BmMu.Unlock()
	return nil
}

// RequestBacMinhMiniMap is the event-account north mapstream pull.
func (p *EventPlayer) RequestBacMinhMiniMap(timeout time.Duration) ([]byte, error) {
	id := p.BmActivityID()
	if id == 0 {
		return nil, errBmClosed
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	floor, group := int32(0), int32(0)
	if st.enterAct != nil {
		floor, group = st.enterAct.GetFloor(), st.enterAct.GetGroup()
	}
	p.BmMu.Unlock()
	if group == 0 {
		if err := p.bmRefreshEnter(timeout); err != nil {
			return nil, err
		}
		p.BmMu.Lock()
		if st.enterAct != nil {
			floor, group = st.enterAct.GetFloor(), st.enterAct.GetGroup()
		}
		p.BmMu.Unlock()
	}
	body, _ := pb.Marshal(&proto.KunlunWarMiniMapInfoReqMsg{ActivityId: &id, Floor: &floor, Group: &group})
	raw, err := p.bmRoundTrip(KUNLUN_MINI_MAP_INFO_CMD, SERVER_KUNLUN_MINI_MAP_INFO, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarMiniMapInfoRespMsg
	if pb.Unmarshal(raw, &rsp) == nil {
		p.BmMu.Lock()
		st.MiniRaw = raw
		if st.Unions == nil {
			st.Unions = map[int64]*proto.KunlunUnionBaseData{}
		}
		for _, u := range rsp.GetBaseData() {
			st.Unions[u.GetUnionId()] = u
		}
		p.BmMu.Unlock()
	}
	return raw, nil
}

// RequestBacMinhGameBlockInfo is the north view-window detail pull (219903),
// seat ensured first — the /events/mapview w=bm detail arm for accounts
// that sit in the scene (mirror of RequestKunlunGameBlockInfo).
func (p *EventPlayer) RequestBacMinhGameBlockInfo(blocks []int32, timeout time.Duration) (*proto.KunlunWarGameBlockInfoResp, error) {
	if len(blocks) == 0 {
		return nil, fmt.Errorf("không có ô nào để hỏi")
	}
	id := p.BmActivityID()
	if id == 0 {
		return nil, errBmClosed
	}
	if err := p.bmEnsureScene(id, 8*time.Second); err != nil {
		return nil, err
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	floor, group := int32(0), int32(0)
	if st.enterAct != nil {
		floor, group = st.enterAct.GetFloor(), st.enterAct.GetGroup()
	}
	p.BmMu.Unlock()
	if group == 0 {
		if err := p.bmRefreshEnter(timeout); err != nil {
			return nil, err
		}
		p.BmMu.Lock()
		if st.enterAct != nil {
			floor, group = st.enterAct.GetFloor(), st.enterAct.GetGroup()
		}
		p.BmMu.Unlock()
	}
	body, _ := pb.Marshal(&proto.KunlunWarGameBlockInfoReq{
		ActivityId: &id, BlockVec: blocks, FloorId: &floor, GroupId: &group,
	})
	raw, err := p.bmRoundTrip(KUNLUN_GAME_BLOCK_INFO_CMD, SERVER_KUNLUN_GAME_BLOCK_INFO, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarGameBlockInfoResp
	if pb.Unmarshal(raw, &rsp) != nil {
		return nil, fmt.Errorf("trả lời Bắc Minh block-info không đọc được")
	}
	if rsp.GetRet() != 0 {
		return nil, fmt.Errorf("Bắc Minh block info ret=%d", rsp.GetRet())
	}
	p.BmMu.Lock()
	if st.Routes == nil {
		st.Routes = map[int64]kuRouteRec{}
	}
	if n := kuFoldRouteInfos(st.Routes, rsp.GetRouteInfo()); n > 0 {
		debugf("[%s] [BacMinh] 219903: %d march(s) folded, ledger=%d", p.Username, n, len(st.Routes))
	}
	kuPruneRoutes(st.Routes)
	p.BmMu.Unlock()
	return &rsp, nil
}

// RequestBacMinhSparInfo pulls the north tree ripeness flag (219910) —
// the mapview arm that lights the auto-tree status without the page
// speaking protocol (mirror of RequestKunlunSparInfo; the claim itself
// stays bot-side in bmTreeTick, the page never presses 219911).
func (p *EventPlayer) RequestBacMinhSparInfo(timeout time.Duration) (int32, error) {
	actID := p.BmActivityID()
	if actID == 0 {
		return 0, errBmClosed
	}
	rsp, err := p.bmSparInfo(actID, timeout)
	if err != nil {
		return 0, err
	}
	return rsp.GetCanReceive(), nil
}

func (p *EventPlayer) bmSparInfo(actID int32, timeout time.Duration) (*proto.KunlunWarSparInfoRespMsg, error) {
	body, _ := pb.Marshal(&proto.KunlunWarSparInfoReqMsg{ActivityId: &actID})
	raw, err := p.bmRoundTrip(KUNLUN_SPAR_INFO_CMD, SERVER_KUNLUN_SPAR_INFO, body, timeout)
	if err != nil {
		return nil, err
	}
	var rsp proto.KunlunWarSparInfoRespMsg
	if pb.Unmarshal(raw, &rsp) != nil {
		return nil, fmt.Errorf("trả lời Bắc Minh spar-info không đọc được")
	}
	return &rsp, nil
}

func (p *EventPlayer) bmSend(cmd string, msg pb.Message) {
	payload, err := pb.Marshal(msg)
	if err != nil {
		log.Printf("[%s] [BacMinh] marshal %s: %v", p.Username, cmd, err)
		return
	}
	p.bmSendMu.Lock()
	p.write(pack.Pack(cmd, payload, p.UID))
	p.bmSendMu.Unlock()
}

// ---- reader-goroutine handlers ----------------------------------------------

func (p *EventPlayer) bmHandleEnterGame(data []byte) {
	var rsp proto.KunlunWarEnterGameResp
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	if rsp.GetRet() != 0 {
		st.enterErr = fmt.Errorf("vào trận Đỉnh Bắc Minh thất bại (ret=%d)", rsp.GetRet())
	} else {
		st.SceneIn = true
		st.Point = rsp.GetMyPlayerInfo().GetPos()
		st.HomeCity = rsp.GetHome()
		st.Energy = rsp.GetEnergy()
		st.SpeedUp = rsp.GetSpeedUpCount()
		if u := rsp.GetMyPlayerInfo().GetUnionId(); u != 0 {
			st.MyUnion = u
		}
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
	p.BmMu.Unlock()
	if startHeart {
		go p.bmHeartbeatLoop(actID)
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [BacMinh] EnterGame ret=%d", p.Username, rsp.GetRet())
	}
}

func (p *EventPlayer) bmHeartbeatLoop(actID int32) {
	t := time.NewTicker(kuHeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
			p.bmSend(KUNLUN_HEART_BEAT_CMD, &proto.KunlunWarHeartBeatReqMsg{ActivityId: &actID})
			p.bmTryAutoRebirth(time.Now())
			go p.bmTreeTick(time.Now())
		}
	}
}

// bmTryAutoRebirth — FREE revive only (219915 useItem=false). There is no
// item revive on this board (standing item ban; Côn Lôn's item exception
// does NOT extend here).
func (p *EventPlayer) bmTryAutoRebirth(now time.Time) {
	st := p.bmStateEP()
	p.BmMu.Lock()
	fire := st.SceneIn && st.Point == 0 && st.ReviveAt > 0 &&
		st.rebirthTries < kuMaxRebirthTries && now.Sub(st.lastRebirthTry) >= kuRebirthBackoff
	if fire {
		st.rebirthTries++
		st.lastRebirthTry = now
	}
	try := st.rebirthTries
	p.BmMu.Unlock()
	if !fire {
		return
	}
	actID := p.BmActivityID()
	if actID == 0 {
		return
	}
	useItem := false
	p.bmSend(KUNLUN_BACK_HOME_CMD, &proto.KunlunWarBackHomeReq{ActivityId: &actID, UseItem: &useItem})
	log.Printf("[%s] [BacMinh] tự hồi sinh MIỄN PHÍ (lần %d/%d)", p.Username, try, kuMaxRebirthTries)
}

func (p *EventPlayer) bmHandleMyGameInfo(data []byte) {
	var rsp proto.KunlunWarMyGameInfoSyncMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	changed := st.Point != rsp.GetLocation()
	st.Point = rsp.GetLocation()
	if st.Point == 0 {
		if st.ReviveAt == 0 {
			st.ReviveAt = time.Now().UnixMilli()
			st.rebirthTries = 0
		}
	} else {
		st.ReviveAt = 0
		st.rebirthTries = 0
	}
	p.BmMu.Unlock()
	if changed {
		debugf("[%s] [BacMinh] vị trí %d", p.Username, st.Point)
	}
}

func (p *EventPlayer) bmHandleMove(data []byte) {
	var rsp proto.KunlunWarMoveResp
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		to := int32(0)
		if r := rsp.GetRouteInfo(); r != nil {
			to = r.GetToPoint()
		}
		log.Printf("[%s] [BacMinh]Move ret=%d (đích %d)", p.Username, rsp.GetRet(), to)
		return
	}
	if ri := rsp.GetRouteInfo(); ri != nil && ri.GetPlayerId() != 0 {
		st := p.bmStateEP()
		p.BmMu.Lock()
		if st.Routes == nil {
			st.Routes = map[int64]kuRouteRec{}
		}
		st.Routes[ri.GetPlayerId()] = kuRouteRec{
			From: ri.GetFromPoint(), To: ri.GetToPoint(),
			Time: ri.GetTime(), Arrival: ri.GetArrival(), UnionID: st.MyUnion,
		}
		p.BmMu.Unlock()
	}
	log.Printf("[%s] [BacMinh] hành quân tới %d, tới nơi sau %dms", p.Username,
		rsp.GetRouteInfo().GetToPoint(), rsp.GetRouteInfo().GetArrival()-rsp.GetRouteInfo().GetTime())
}

func (p *EventPlayer) bmFoldRoutes(data []byte) {
	st := p.bmStateEP()
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	if st.Routes == nil {
		st.Routes = map[int64]kuRouteRec{}
	}
	if n := kuBuildRouteRecs(st.Routes, data); n > 0 {
		debugf("[%s] [BacMinh] 219943: %d march(s) folded, ledger=%d", p.Username, n, len(st.Routes))
	}
	kuPruneRoutes(st.Routes)
}

func (p *EventPlayer) bmFoldLines(data []byte) {
	var rsp proto.KunlunWarGameLineInfoSyncMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	if st.Routes == nil {
		st.Routes = map[int64]kuRouteRec{}
	}
	// kuFoldLineInfos keys marches by player id — ledger-generic, but its
	// city-name attribution is kunlun-table based; north-only city ids just
	// miss the name lookup (accepted v1).
	if n := kuFoldLineInfos(st.Routes, rsp.GetLineInfo()); n > 0 {
		debugf("[%s] [BacMinh] 219944: %d home-march(es) folded, ledger=%d", p.Username, n, len(st.Routes))
	}
	kuPruneRoutes(st.Routes)
}

// bmRoutesSnapshot feeds the /bacminh mapstream merge across seated accounts.
func (p *EventPlayer) bmRoutesSnapshot(now time.Time) []gimap.KuRoute {
	st := p.bmStateEP()
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	return kuRoutesFrom(st.Routes, st.MyUnion, int64(p.UID), st.MiniRaw, st.Unions, now)
}

func (p *EventPlayer) bmHandleBeenKill(data []byte) {
	var rsp proto.KunlunWarBeenKillSyncMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetTargetId() != 0 && int64(p.UID) != rsp.GetTargetId() {
		return
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	st.Point = 0
	st.ReviveAt = rsp.GetReviveTime()
	st.rebirthTries = 0
	if tid := rsp.GetTargetId(); tid != 0 {
		delete(st.Routes, tid)
	}
	p.BmMu.Unlock()
	by := rsp.GetTargetName()
	if by == "" {
		if rsp.GetKillByNPc() {
			by = "NPC"
		} else {
			by = "?"
		}
	}
	log.Printf("[%s] [BacMinh] tử trận vì %s, hồi sinh %d", p.Username, by, rsp.GetReviveTime())
	p.bmTryAutoRebirth(time.Now())
}

// ---- orders ----------------------------------------------------------------

// bmEnsureScene joins the north scene if not already inside. Refused while
// Côn Lôn seats the account (transport invariant). warKindEP flips to the
// north side before the EnterGame write.
func (p *EventPlayer) bmEnsureScene(actID int32, to time.Duration) error {
	if p.kuSeatedEP() {
		return fmt.Errorf("acc đang ngồi trong trận Côn Lôn — rời Côn Lôn trước đã")
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	if st.SceneIn {
		p.BmMu.Unlock()
		return nil
	}
	st.ActivityID = actID
	if st.enterCh == nil {
		st.enterCh = make(chan []byte)
		st.enterErr = nil
	}
	ch := st.enterCh
	p.BmMu.Unlock()
	p.setWarKindEP(warKindBacMinh)
	p.bmSend(KUNLUN_ENTER_GAME_CMD, &proto.KunlunWarEnterGameReq{ActivityId: &actID})
	select {
	case <-ch:
		p.BmMu.Lock()
		err := st.enterErr
		p.BmMu.Unlock()
		return err
	case <-time.After(to):
		return errBmTimeout
	case <-p.ctx.Done():
		return errBmGone
	}
}

// BacMinhWarMoveTo marches this account to a north block posIndex.
func (p *EventPlayer) BacMinhWarMoveTo(point int32) error {
	return p.BacMinhWarMoveToPoint(point, false)
}

// BacMinhWarMoveToPoint marches to posIndex y*Cols+x (grid from
// gimap.BacMinhCols()); attackCity marks a city assault (gimap.
// BacMinhCityPoint resolves the block). Server judges reachability.
func (p *EventPlayer) BacMinhWarMoveToPoint(point int32, attackCity bool) error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.BmActivityID()
	if actID == 0 {
		return errBmClosed
	}
	if point <= 0 {
		return fmt.Errorf("block %d không hợp lệ", point)
	}
	if err := p.bmEnsureScene(actID, 8*time.Second); err != nil {
		return err
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	cur, joined := st.Point, st.SceneIn
	p.BmMu.Unlock()
	if !joined {
		return errBmTimeout
	}
	if cur == 0 {
		return fmt.Errorf("quân đang chết chờ hồi sinh (về /bacminh hoặc hồi sinh trong game)")
	}
	if cur == point {
		return nil
	}
	p.bmSend(KUNLUN_MOVE_CMD, &proto.KunlunWarMoveReq{ActivityId: &actID, TargetPoint: &point, AttackCity: &attackCity})
	if attackCity {
		log.Printf("[%s] [BacMinh] tấn công → block %d", p.Username, point)
	} else {
		log.Printf("[%s] [BacMinh] follow bang chủ → block %d", p.Username, point)
	}
	return nil
}

// bmScenePos reports the live stand-point + union for the mini app's own-side
// overlays (mirror of kuScenePos).
func (p *EventPlayer) bmScenePos() (int32, int64) {
	st := p.bmStateEP()
	p.BmMu.Lock()
	defer p.BmMu.Unlock()
	if !st.SceneIn {
		return 0, 0
	}
	return st.Point, st.MyUnion
}

// bmGateStand — shared transport gate of the scene orders (see kuGateStand:
// position/death checks REMOVED per user directive 2026-10-07, the server
// alone judges every account).
func (p *EventPlayer) bmGateStand(point int32) error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.BmActivityID()
	if actID == 0 {
		return errBmClosed
	}
	if point <= 0 {
		return fmt.Errorf("block %d không hợp lệ", point)
	}
	return p.bmEnsureScene(actID, 8*time.Second)
}

// BacMinhWarExplorePoint explores the army's tile (219905).
func (p *EventPlayer) BacMinhWarExplorePoint(point int32) error {
	if err := p.bmGateStand(point); err != nil {
		return err
	}
	actID := p.BmActivityID()
	p.bmSend(KUNLUN_EXPLORE_CMD, &proto.KunlunWarExploreReq{ActivityId: &actID, TargetPoint: &point})
	log.Printf("[%s] [BacMinh] khám phá ô %d", p.Username, point)
	return nil
}

// BacMinhWarSweepHere sweeps the army's tile (219906).
func (p *EventPlayer) BacMinhWarSweepHere(point int32) error {
	if err := p.bmGateStand(point); err != nil {
		return err
	}
	actID := p.BmActivityID()
	num := int32(1)
	p.bmSend(KUNLUN_SWEEP_CMD, &proto.KunlunWarSweepReq{ActivityId: &actID, Num: &num})
	log.Printf("[%s] [BacMinh] càn quét ô %d", p.Username, point)
	return nil
}

func (p *EventPlayer) bmHandleExplore(data []byte) {
	var rsp proto.KunlunWarExploreResp
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [BacMinh]Explore ret=%d", p.Username, rsp.GetRet())
		return
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	st.Energy = rsp.GetEnergy()
	p.BmMu.Unlock()
	log.Printf("[%s] [BacMinh] khám phá xong · chiếm %d%% · NL còn %d · thưởng %s",
		p.Username, rsp.GetOccupyProgress(), rsp.GetEnergy(), rsp.GetReward())
}

func (p *EventPlayer) bmHandleSweep(data []byte) {
	var rsp proto.KunlunWarSweepResp
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [BacMinh]Sweep ret=%d", p.Username, rsp.GetRet())
		return
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	st.Energy = rsp.GetEnergy()
	p.BmMu.Unlock()
	log.Printf("[%s] [BacMinh] càn quét xong · NL còn %d · thưởng %s",
		p.Username, rsp.GetEnergy(), rsp.GetReward())
}

// ---- march speed-up (219918) -------------------------------------------------

// BmAccelerate buys one march speed-up — user-authorized PRESS-THRU
// ("tăng tốc thì cứ bấm thôi khỏi cần hỏi"): no confirm and no bag pre-check.
// The server's own cost model runs behind this op: it spends one
// NORTHPOLEWAR_ACCEL_ITEM while the bag holds it and only falls back to the
// gem config (KUNLUNWAR_ACCELERATE_COST) when out — the item/gem deduction
// is the game's designed behaviour, not the standing item ban's target. The
// verdict (and which currency it spent, via CostInfo) lands in the log.
func (p *EventPlayer) BmAccelerate() error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.BmActivityID()
	if actID == 0 {
		return errBmClosed
	}
	if err := p.bmEnsureScene(actID, 8*time.Second); err != nil {
		return err
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	cur, count := st.Point, st.SpeedUp
	p.BmMu.Unlock()
	if cur == 0 {
		return fmt.Errorf("quân đang chết chờ hồi sinh")
	}
	p.bmSend(KUNLUN_ACCELERATE_CMD, &proto.KunlunWarAcclerateMoveReqMsg{
		ActivityId:   &actID,
		SpeedUpCount: &count,
	})
	log.Printf("[%s] [BacMinh] nhận lệnh tăng tốc hành quân (speedUpCount=%d)", p.Username, count)
	return nil
}

func (p *EventPlayer) bmHandleAccelerate(data []byte) {
	var rsp proto.KunlunWarAcclerateMoveRespMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [BacMinh]Tăng tốc ret=%d", p.Username, rsp.GetRet())
		return
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	st.SpeedUp = rsp.GetSpeedUpCount()
	p.BmMu.Unlock()
	cost := rsp.GetCostInfo()
	if cost == "" {
		cost = "không rõ giá"
	}
	log.Printf("[%s] [BacMinh] tăng tốc hành quân · speedUp=%d · giá: %s",
		p.Username, rsp.GetSpeedUpCount(), cost)
}

// ---- guild tree (FREE auto ops only) ------------------------------------------

// bmTreeTick mirrors kuTreeTick: read (219910) → collect pending ally
// liquid (219912 {id:0} — collecting is receive-side FREE; SEND_WELFARE
// 219913 is NEVER sent) → claim ripe fruit (219911, server-decided payout,
// no item spent). North has NO eat-fruit (219926) path at all — the standing
// item ban holds; Côn Lôn's fruit exception is not ported.
func (p *EventPlayer) bmTreeTick(now time.Time) {
	if !p.Running || p.ctx == nil {
		return
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	if !st.treeAt.IsZero() && now.Sub(st.treeAt) < kuTreeEvery {
		p.BmMu.Unlock()
		return
	}
	st.treeAt = now
	p.BmMu.Unlock()
	actID := p.BmActivityID()
	if actID == 0 {
		return
	}
	rsp, err := p.bmSparInfo(actID, 8*time.Second)
	if err != nil {
		debugf("[%s] [BacMinh] poll cây bang: %v", p.Username, err)
		return
	}
	pending := 0
	for _, w := range rsp.GetInfo() {
		if w.GetStatus() == 0 {
			pending++
		}
	}
	if pending > 0 {
		id := int64(0)
		p.bmSend(KUNLUN_GET_WELFARE_CMD, &proto.KunlunWarGetWelfareReqMsg{ActivityId: &actID, Id: &id})
		log.Printf("[%s] [BacMinh] tự tưới cây bang · %d phần quà đồng bang đang chờ", p.Username, pending)
	}
	if rsp.GetCanReceive() == 1 {
		p.bmSend(KUNLUN_SPAR_POWER_CMD, &proto.KunlunWarGetSparPowerReqMsg{ActivityId: &actID})
		log.Printf("[%s] [BacMinh] quà cây đã chín → tự hái", p.Username)
	}
}

// BacMinhWarClaimFruit is the manual 🍈Hái quả press (219911, server-decided
// payout, no item spent — the standing item ban is untouched; bmTreeTick
// already auto-claims, this is the instant button twin of KunlunWarClaimFruit).
// No scene gate: the tree is activity-level; a non-ripe claim refuses in
// bmHandleSparPower's log.
func (p *EventPlayer) BacMinhWarClaimFruit() error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.BmActivityID()
	if actID == 0 {
		return errBmClosed
	}
	p.bmSend(KUNLUN_SPAR_POWER_CMD, &proto.KunlunWarGetSparPowerReqMsg{ActivityId: &actID})
	log.Printf("[%s] [BacMinh] gửi lệnh hái quả Bắc Minh", p.Username)
	return nil
}

func (p *EventPlayer) bmHandleSparPower(data []byte) {
	var rsp proto.KunlunWarGetSparPowerRespMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [BacMinh]Hái quả ret=%d (chưa có quả chín)", p.Username, rsp.GetRet())
		return
	}
	log.Printf("[%s] [BacMinh] hái quả xong · thưởng %s · exp +%d · quả tới canReceive=%d",
		p.Username, rsp.GetReward(), rsp.GetExp(), rsp.GetCanReceive())
	// The reward string IS the bag receipt — the only honest source of the
	// north fruit item id AND count for the 219926 eat press.
	if id := kuRewardItemID(rsp.GetReward()); id != 0 {
		cnt := kuRewardCountFor(rsp.GetReward(), id) // sum EVERY matching pair
		st := p.bmStateEP()
		p.BmMu.Lock()
		if st.FruitItem != id {
			st.FruitBag = 0 // khác id = khác giỏ; đếm lại từ đầu
		}
		st.FruitItem = id
		st.FruitBag += cnt
		p.BmMu.Unlock()
	}
}

// bmFruitItemObserved is the Đỉnh Bắc Minh spirit-fruit item id seen LIVE in
// the user's own 219926 packet capture (2026-10-07, activityId 9062374 →
// itemId 105363 num 1) — the exact Côn Lôn precedent (105248): observed,
// never invented. Fallback when this account's bag id is empty (harvested in
// the real client, or a restart forgot it); the server stays the arbiter —
// an account with no fruit just answers ret≠0 in its log, nothing is spent.
const bmFruitItemObserved = int32(105363)

// BacMinhEatFruit "cắn quả" on the north board (219926) — user-authorized
// 2026-10-07 ("Thêm cắn quả như Côn Lôn"), button-press-only like Côn Lôn:
// the recovery amount is the server's item config, the eat works from
// anywhere (no place field in the packet), and NOTHING else may spend items
// here. Item id: the account's own 19911 reward receipt when the bot
// harvested this run, else bmFruitItemObserved (the user's capture).
func (p *EventPlayer) BacMinhEatFruit(num int32) error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.BmActivityID()
	if actID == 0 {
		return errBmClosed
	}
	if num <= 0 {
		return nil
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	item := st.FruitItem
	st.eatSendNum = num // the ret=0 verdict deducts EXACTLY this from FruitBag
	p.BmMu.Unlock()
	if item == 0 {
		item = bmFruitItemObserved // acc hái bằng client thật / restart mất bag id — server quyết
	}
	p.bmSend(KUNLUN_USE_ITEM_CMD, &proto.KunlunWarUseItemReqMsg{ActivityId: &actID, ItemId: &item, Num: &num})
	log.Printf("[%s] [BacMinh] gửi lệnh cắn %d quả (item %d)", p.Username, num, item)
	return nil
}

// bmEatStep — north twin of kuEatStep: computes ONE bite (size = ceil(needs /
// learned heal, first guess kuFruitHeal), clamped by the counted bag) and sends
// it. Returns the num actually sent (0 = nothing to do). Safe from verdict
// handlers — fires at most one packet. The v3 chase exists because the customer
// report of 2026-10-07: the north's real heal ≠ 5 NL/fruit, so the old v2 cap
// stopped at 2 bites while the bag still had fruit.
func (p *EventPlayer) bmEatStep() int32 {
	if !p.Running {
		return 0
	}
	if p.BmActivityID() == 0 {
		return 0
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	e := st.Energy
	if e < 0 {
		e = 0
	}
	if e >= kuFruitTarget {
		st.eatChase = false
		p.BmMu.Unlock()
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
			p.BmMu.Unlock()
			debugf("[%s] [BacMinh] cắn quả: giỏ hết quả — dừng", p.Username)
			return 0
		}
		if num > st.FruitBag {
			num = st.FruitBag
		}
	}
	st.eatBase = e
	p.BmMu.Unlock()
	if err := p.BacMinhEatFruit(num); err != nil {
		return 0
	}
	return num
}

// BacMinhEatFruitToTen — exact north mirror of KunlunWarEatFruitToTen v3
// (customer report 2026-10-07 "cắn có 2 quả trong khi túi vẫn còn"): the press
// arms a BOUNDED chase — one bite at a time, sized from the account's own
// LEARNED heal, clamped by the counted bag (19911 claim AND 19912 welfare
// receipts), until Energy ≥ 10, the bag reads empty, a ret≠0 lands, or
// kuEatMaxRounds bites fired. Already-≥10 accounts skip with NO packet; a
// second press while a chase is in flight never duplicates commands.
func (p *EventPlayer) BacMinhEatFruitToTen() (int32, error) {
	if !p.Running {
		return 0, fmt.Errorf("chưa login")
	}
	if p.BmActivityID() == 0 {
		return 0, errBmClosed
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	if st.eatChase && !st.eatChaseAt.IsZero() && time.Since(st.eatChaseAt) > kuEatChaseStuck {
		// Chase không bao giờ thấy verdict 4dd6 (lạc sang board khác / server
		// im lặng) — nếu không cho bấm lại thì nút chết vĩnh viễn tới restart
		// (báo cáo khách 2026-11-04 "nút cắn quả không work").
		log.Printf("[%s] [BacMinh] cắn quả: chase kẹt quá %v không thấy verdict — bấm lại nạp chase", p.Username, kuEatChaseStuck)
		st.eatChase = false
		st.eatSendNum = 0
	}
	if st.eatChase { // một press đang tự nạp dở — đừng nhân đôi lệnh
		p.BmMu.Unlock()
		return 0, nil
	}
	if st.Energy >= kuFruitTarget {
		p.BmMu.Unlock()
		return 0, nil // đủ 10 rồi — "bé hơn thì thôi"
	}
	st.eatChase = true
	st.eatChaseAt = time.Now()
	st.eatRounds = 0
	p.BmMu.Unlock()
	return p.bmEatStep(), nil
}

// bmHandleUseItem folds the eat verdict (client Mgr.showAddStrengthGood: a
// ret=0 answer refreshes energy + recover clock), does the bag bookkeeping for
// EXACTLY the in-flight num, LEARNS the north's real heal-per-fruit from the
// energy delta (the v2 bug: north heals ≠5, the arithmetic cap under-ate), and
// continues the bounded chase when the target is not reached yet.
func (p *EventPlayer) bmHandleUseItem(data []byte) {
	var rsp proto.KunlunWarUseItemRespMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	if rsp.GetRet() != 0 {
		st.eatChase = false // server nói không là thôi — không nhỏ giọt item
		st.eatSendNum = 0
		p.BmMu.Unlock()
		log.Printf("[%s] [BacMinh]Cắn quả ret=%d (hết quả / chưa tới lượt)", p.Username, rsp.GetRet())
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
	p.BmMu.Unlock()
	log.Printf("[%s] [BacMinh] cắn quả xong · năng lượng now=%d · giỏ còn ~%d quả · hồi lúc %d",
		p.Username, rsp.GetEnergy(), st.FruitBag, rsp.GetEnergyRecoverTime())
	if cont {
		go p.bmEatStep() // "cắn cho đủ 10" — thêm một phát theo NL vừa được server mộc
	}
}

func (p *EventPlayer) bmHandleWelfare(data []byte) {
	var rsp proto.KunlunWarGetWelfareRespMsg
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	if rsp.GetRet() != 0 {
		debugf("[%s] [BacMinh] tưới cây ret=%d", p.Username, rsp.GetRet())
		return
	}
	log.Printf("[%s] [BacMinh] tưới cây bang xong · thưởng %s · exp +%d · quả chín=%d",
		p.Username, rsp.GetReward(), rsp.GetExp(), rsp.GetCanReceive())
	// Welfare rewards CAN carry spirit fruit — fold them into the bag, but
	// only when the ledger is already trusted (FruitItem!=0); the untrusted
	// bag never clamps, so an unknown-id reward can't starve the chase loop
	// (customer report 2026-10-07, mirror of kuHandleWelfare).
	st := p.bmStateEP()
	p.BmMu.Lock()
	if st.FruitItem != 0 {
		st.FruitBag += kuRewardCountFor(rsp.GetReward(), st.FruitItem)
	}
	p.BmMu.Unlock()
}

// ---- revive (FREE only) --------------------------------------------------------

// BacMinhWarRebirth is the manual revive behind /bacminh_hoisinh [item].
// useItem=true is the NHANH (instant) revive — same 219915 useItem=true the
// Côn Lôn ❤ button sends; the server deducts the revive item by its own
// config (the 332 window's 21003 detail carries KUNLUNWAR_ITEM_REVIVE=105368,
// the packet never names the item). User-authorized 2026-10-07 for the north
// board too ("Bật như Côn Lôn"); like Côn Lôn it fires ONLY from an explicit
// command/button — the auto loop stays useItem=false.
func (p *EventPlayer) BacMinhWarRebirth(useItem bool) error {
	if !p.Running {
		return fmt.Errorf("chưa login")
	}
	actID := p.BmActivityID()
	if actID == 0 {
		return errBmClosed
	}
	if err := p.bmEnsureScene(actID, 8*time.Second); err != nil {
		return err
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
	if st.Point != 0 {
		p.BmMu.Unlock()
		return fmt.Errorf("quân vẫn đang sống")
	}
	st.manualRebirth = true
	st.lastRebirthTry = time.Now()
	p.BmMu.Unlock()
	p.bmSend(KUNLUN_BACK_HOME_CMD, &proto.KunlunWarBackHomeReq{ActivityId: &actID, UseItem: &useItem})
	if useItem {
		log.Printf("[%s] [BacMinh] nhận lệnh hồi sinh bằng ITEM", p.Username)
	} else {
		log.Printf("[%s] [BacMinh] nhận lệnh hồi sinh miễn phí", p.Username)
	}
	return nil
}

func (p *EventPlayer) bmHandleBackHome(data []byte) {
	var rsp proto.KunlunWarBackHomeResp
	if pb.Unmarshal(data, &rsp) != nil {
		return
	}
	st := p.bmStateEP()
	p.BmMu.Lock()
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
	p.BmMu.Unlock()
	if rsp.GetRet() != 0 {
		log.Printf("[%s] [BacMinh]BackHome ret=%d", p.Username, rsp.GetRet())
		if manual {
			p.sendNotify(fmt.Sprintf("⚠️ [Đỉnh Bắc Minh] hồi sinh thất bại (lỗi %d)", rsp.GetRet()))
		}
		return
	}
	log.Printf("[%s] [BacMinh] hồi sinh thành công · đã dùng %d free", p.Username, st.freeUsed)
	if manual {
		p.sendNotify("✅ [Đỉnh Bắc Minh] hồi sinh thành công")
	}
}
