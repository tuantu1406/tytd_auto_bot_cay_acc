//go:build live

package tytd_go

// MONSTER-ATTACK live probe for Tiên Cảnh Vị Ương (WeYound 186) — owner
// 2026-11-13 "đã tự mở ô nhưng mở phải ô có quái thì không đánh => đánh luôn".
//
// Reads (never guesses):
//  1. What does scene.player.unDealCell ACTUALLY carry on ENTER — do entries
//     have a pos? an event payload? the monster cell listed at all?
//  2. After a dig that reveals a monster, does the EXPLORE answer's player
//     msg carry the new event in unDealCell?
//  3. Does wyPickPending see it, does wyResolveEventLite fire DEAL_EVENT
//     ("558c"), and does the server accept (attackInfo gains our uid /
//     remainBloods drops)?
//  4. If the same monster is re-read through a FRESH ENTER (production's
//     next tick), is it listed then?
//
//	Owner 2026-11-13 also authorized THIS probe to bite the stamina fruit
//	(USE_ITEM 218605, item 105185) when the test account runs dry — the
//	production ban [[tytd-item-ban]] stands; only this live test may send it.
//
//	TYTD_LIVE=1 go test -tags "sqlcipher live" -count=1 -run TestTienCanhMonsterLive -v -timeout 420s .

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// wyUseItemAns — 218605 answer (18605). Not a production constant: the engine
// NEVER sends USE_ITEM; only this probe does (owner 2026-11-13).
const wyUseItemAns = "48ad"

func TestTienCanhMonsterLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710564
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}

	pt, err := request.LoginGPassport(user, pass)
	if err != nil {
		t.Fatal(err)
	}
	sd, uidF, err := request.LoginFuntap(pt)
	if err != nil {
		t.Fatal(err)
	}
	gu, err := request.GetServerURL(uidF, server)
	if err != nil {
		t.Fatal(err)
	}
	token, wsURL, uid, err := request.GetWSKey(gu, sd)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, _ := gorm.Open(sqlite.Open("file:tcm?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256),
		AutoTienCanh: true}

	var sendMu sync.Mutex
	sentTypes := map[string]int{}
	// 55ed (USE_ITEM) ALLOWED for this probe only — owner 2026-11-13 "hết thể
	// lực thì cắn quả"; production still never sends it [[tytd-item-ban]].
	banned := func(ty string) bool { return ty == "55f8" }
	var bannedSeen []string
	go func() {
		for f := range p.writeChan {
			_, ty, _ := pack.Unpack(f)
			sendMu.Lock()
			sentTypes[ty]++
			if banned(ty) {
				bannedSeen = append(bannedSeen, ty)
			}
			sendMu.Unlock()
			_ = conn.WriteMessage(2, f)
		}
	}()
	sent := func(ty string) int {
		sendMu.Lock()
		defer sendMu.Unlock()
		return sentTypes[ty]
	}

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			isMSG, msgType, realData := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.wyMergeConfig(realData)
				p.stDeliver(msgType, realData)
			case SERVER_ACTIVITY_BUY_GOODS:
				p.stDeliver(msgType, realData)
			case SERVER_WE_YOUND_ENTER_ACTIVITY, SERVER_WE_YOUND_ENTER, SERVER_WE_YOUND_EXPLORE,
				SERVER_WE_YOUND_MOVE, SERVER_WE_YOUND_DEAL_EVENT, SERVER_WE_YOUND_TRAP_SEEK,
				SERVER_WE_YOUND_ASSIST, SERVER_WE_YOUND_ASSIST_LOG,
				SERVER_WE_YOUND_WORSHIP, SERVER_WE_YOUND_REWARD_FLOOR, SERVER_WE_YOUND_GET_MONSTER_REWARD,
				SERVER_WE_YOUND_BONUS, SERVER_WE_YOUND_GET_BONUS_RESULT, SERVER_WE_YOUND_GET_BONUS_ITEM,
				SERVER_WE_YOUND_SEND_BONUS_ITEM, SERVER_WE_YOUND_UNION_GIFT_LIST, SERVER_WE_YOUND_RECEIVE_UNION_GIFT:
				p.wyHandleFrame(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-readerDone }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(30 * time.Second)
	var actID int32
	for time.Now().Before(deadline) {
		now := time.Now()
		for _, e := range p.ActList.byType(wyActivityType) {
			if e.status(now) == "active" {
				actID = e.ActivityID
			}
		}
		if actID != 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if actID == 0 {
		t.Fatal("no active type-186 activity")
	}
	t.Logf("live 186 activityId=%d uid=%d", actID, uid)

	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if data, err := p.wyRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err == nil {
			p.wyMergeConfig(data)
		} else {
			t.Logf("21003 pull failed (%v)", err)
		}
	}

	// ── observation helpers ──────────────────────────────────────────────
	dumpUnDeal := func(tag string, pd *proto.WeYoundPlayerMsg) int {
		if pd == nil {
			t.Logf("%s: pd=nil", tag)
			return 0
		}
		t.Logf("%s: unDealCell=%d (pos=%v hp=%d trapEnd=%d)", tag,
			len(pd.GetUnDealCell()), fmt.Sprint(pd.GetPos().GetX(), pd.GetPos().GetY()), pd.GetHp(), pd.GetTrapEndTime())
		for i, c := range pd.GetUnDealCell() {
			t.Logf("  unDeal[%d]: cellPosNil=%v pos=(%d,%d) type=%d reachable=%v eventNil=%v evID=%d cfg=%d evType=%d",
				i, c.GetPos() == nil, c.GetPos().GetX(), c.GetPos().GetY(), c.GetType(),
				c.GetReachable(), c.GetEvent() == nil,
				c.GetEvent().GetId(), c.GetEvent().GetConfigId(), c.GetEvent().GetEventType())
		}
		return len(pd.GetUnDealCell())
	}
	dumpMonsters := func(tag string, ms []*proto.WeYoundMonsterMsg) {
		for _, m := range ms {
			ai := make([]string, 0, len(m.GetAttackInfo()))
			for _, a := range m.GetAttackInfo() {
				ai = append(ai, fmt.Sprintf("%d:%dx", a.GetPlayerId(), a.GetAttackNum()))
			}
			t.Logf("%s: monster id=%d cfg=%d bloods=%s/%s defeat=%v explorePid=%d atk=[%v]",
				tag, m.GetId(), m.GetConfigId(), m.GetRemainBloods(), m.GetMaxBloods(),
				m.GetDefeat(), m.GetExplorePlayerId(), ai)
		}
	}
	newState := func(tag string) *wyState {
		en, err := p.wyEnter(actID, false)
		if err != nil || en.GetRet() != 0 {
			t.Fatalf("%s ENTER err=%v ret=%d", tag, err, en.GetRet())
		}
		sc := en.GetScene()
		st := &wyState{pd: sc.GetPlayer(), mapID: sc.GetMapId(), floor: sc.GetFloor(), memberSize: en.GetMemberSize()}
		if pp := st.pd.GetPos(); pp != nil {
			st.px, st.py = pp.GetX(), pp.GetY()
		}
		st.ingest(sc.GetView())
		st.ingestMonsters(sc.GetMonster())
		if !p.wySyncBoard(st) {
			t.Fatalf("%s: board sync failed", tag)
		}
		t.Logf("%s: floor=%d mapID=%d pos=(%d,%d) hp=%d", tag, st.floor, st.mapID, st.px, st.py, st.pd.GetHp())
		return st
	}
	tryAttackPendingMonsters := func(tag string, st *wyState) {
		actKey := fmt.Sprintf("%d:f%d", actID, st.floor)
		for k := 0; k < 5; k++ {
			cell, ev := p.wyPickPending(st, wyEvMonster, actKey)
			if cell == nil {
				t.Logf("%s: pickPending(monster) → nil (nothing pickable)", tag)
				return
			}
			cx, cy := cell.GetPos().GetX(), cell.GetPos().GetY()
			t.Logf("%s: picked monster ev id=%d cfg=%d at (%d,%d); player at (%d,%d)", tag, ev.id, ev.configId, cx, cy, st.px, st.py)
			before := sent("55ec") // DEAL_EVENT 218604 low16
			ok := p.wyResolveEventLite(actID, st, ev, cx, cy, actKey)
			t.Logf("%s: resolveEventLite ok=%v, DEAL_EVENT sent %d→%d, stuck=%v",
				tag, ok, before, sent("55ec"), p.wyIsStuck(actKey, cx, cy))
			if !ok {
				return
			}
		}
	}

	// ── 1. what does ENTER say right now ─────────────────────────────────
	st := newState("ENTER#1")
	dumpUnDeal("ENTER#1", st.pd)
	dumpMonsters("ENTER#1", sceneMonstersOf(p, actID))
	tryAttackPendingMonsters("ENTER#1", st)

	// biteFruit — USE_ITEM 218605 {activityId,itemId,num=1}. OWNER-AUTHORIZED
	// for THIS PROBE ONLY (2026-11-13 "hết thể lực thì cắn quả lấy thể lực mà
	// test"). The production engine still never sends it [[tytd-item-ban]].
	bites := 0
	biteFruit := func() int32 {
		p.WyMu.Lock()
		itemID := p.wyEnergyItem
		p.WyMu.Unlock()
		if itemID == 0 {
			itemID = 105185
		}
		var b []byte
		b = protowire.AppendTag(b, 1, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(actID))
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(itemID))
		b = protowire.AppendTag(b, 3, protowire.VarintType)
		b = protowire.AppendVarint(b, 1)
		// Fire-and-forget: the 18605 answer never rode its own funcType in
		// the first run (seat timed out although hp did rise) — the ENTER
		// that follows every bite is the oracle.
		p.writeChan <- pack.Pack(WE_YOUND_USE_ITEM_CMD, b, uid)
		bites++
		t.Logf("bite #%d: USE_ITEM(id=%d) sent", bites, itemID)
		return 0
	}

	// ── 1b. PRODUCTION sweep (owner 2026-11-13 "đánh luôn"): sweep attacks
	// EVERY live monster cell regardless of unDealCell ────────────────────
	sweepLive := func(tag string, st *wyState) {
		seen := map[*wyEventLite]bool{}
		for k := range st.grid.cells {
			x, y := k%st.grid.w, k/st.grid.w
			ev := st.grid.resolveEvent(x, y)
			if ev == nil || ev.evType != wyEvMonster || seen[ev] {
				continue
			}
			seen[ev] = true
			t.Logf("%s: monster cell (%d,%d) evID=%d objID=%d cfg=%d", tag, x, y, ev.id, ev.objId, ev.configId)
		}
		before := sent("55ec") // DEAL_EVENT 218604 low16
		ok := p.wySweepMonsters(actID, st)
		t.Logf("%s: wySweepMonsters ok=%v DEAL_EVENT sent %d→%d", tag, ok, before, sent("55ec"))
	}
	sweepLive("ENTER#1-sweep", st)

	// ── 2. dig until a monster tile opens, watch the EXPLORE answer ──────
	actKey := fmt.Sprintf("%d:f%d", actID, st.floor)
	deadFog := map[string]bool{}
	digs := 0
	for digs < 12 {
		for st.pd.GetHp() < 5 && bites < 12 {
			if biteFruit() != 0 {
				break
			}
			st = newState(fmt.Sprintf("post-bite#%d", bites))
		}
		if st.pd.GetHp() < 5 {
			t.Logf("hp=%d — vẫn thiếu thể lực sau %d cú cắn, dừng cuốc", st.pd.GetHp(), bites)
			break
		}
		fx, fy, ok := st.grid.nearestCanExplore(st.px, st.py, func(x, y int32) bool {
			return deadFog[cellKey(x, y)] || p.wyIsStuck(actKey, x, y)
		})
		if !ok {
			t.Logf("nearestCanExplore → none (floor done?)")
			break
		}
		if !p.wyWalkTo(actID, st, fx, fy) {
			deadFog[cellKey(fx, fy)] = true
			continue
		}
		ex, err := p.wyExplore(actID, fx, fy)
		digs++
		if err != nil || ex.GetRet() != 0 {
			t.Logf("explore (%d,%d) err=%v ret=%d", fx, fy, err, ex.GetRet())
			p.wyMarkStuck(actKey, fx, fy)
			continue
		}
		t.Logf("EXPLORE (%d,%d) #%d: ret=%d eventPresent=%v evID=%d cfg=%d evType=%d monsterPresent=%v rewards=%q playerPresent=%v",
			fx, fy, digs, ex.GetRet(), ex.GetEvent() != nil,
			ex.GetEvent().GetId(), ex.GetEvent().GetConfigId(), ex.GetEvent().GetEventType(),
			ex.GetMonster() != nil, ex.GetRewards(), ex.GetPlayer() != nil)
		for _, c := range ex.GetView() {
			t.Logf("  view cell (%d,%d): type=%d reachable=%v eventNil=%v evID=%d evType=%d root=%v",
				c.GetPos().GetX(), c.GetPos().GetY(), c.GetType(), c.GetReachable(),
				c.GetEvent() == nil, c.GetEvent().GetId(), c.GetEvent().GetEventType(), c.GetRoot() != nil)
		}
		st.ingest(ex.GetView())
		st.ingestMonsters(extractSceneMonster(ex))
		st.syncPlayer(ex.GetPlayer())
		n := dumpUnDeal(fmt.Sprintf("after-dig (%d,%d)", fx, fy), st.pd)
		gridEv := st.grid.resolveEvent(fx, fy)
		t.Logf("grid cell (%d,%d): type=%d event=%+v", fx, fy, st.grid.resolveType(fx, fy), gridEv)

		if ex.GetMonster() != nil || (gridEv != nil && gridEv.evType == wyEvMonster) {
			t.Logf("★ monster revealed at (%d,%d) — production would now run wyResolvePending", fx, fy)
			if n == 0 && ex.GetPlayer() != nil {
				t.Logf("★★ SMOKING GUN: explore answer's player has NO unDealCell — pending scan finds nothing this tick")
			}
			if ex.GetMonster() != nil {
				dumpMonsters("reveal", []*proto.WeYoundMonsterMsg{ex.GetMonster()})
			}
			tryAttackPendingMonsters("reveal-tick", st)
			sweepLive("reveal-sweep", st)

			// ── 3. simulate production's NEXT tick: fresh ENTER ──────────
			st2 := newState("ENTER#2 (next tick)")
			dumpUnDeal("ENTER#2", st2.pd)
			dumpMonsters("ENTER#2", sceneMonstersOf(p, actID))
			sweepLive("ENTER#2-sweep", st2)
			break
		}
	}

	// ── 4. verdict snapshot ───────────────────────────────────────────────
	st3 := newState("ENTER#3 (final)")
	dumpUnDeal("ENTER#3", st3.pd)
	dumpMonsters("ENTER#3", sceneMonstersOf(p, actID))
	sendMu.Lock()
	keys := make([]string, 0, len(sentTypes))
	for k, v := range sentTypes {
		keys = append(keys, fmt.Sprintf("%s×%d", k, v))
	}
	sendMu.Unlock()
	t.Logf("outbound: %v  DEAL_EVENT(55ec)=%d", keys, sent("55ec"))
	if len(bannedSeen) > 0 {
		t.Errorf("ITEM BAN VIOLATION: %v", bannedSeen)
	}
}

// sceneMonstersOf pulls the current scene monster list via a plain ENTER.
func sceneMonstersOf(p *Player, actID int32) []*proto.WeYoundMonsterMsg {
	en, err := p.wyEnter(actID, false)
	if err != nil || en.GetScene() == nil {
		return nil
	}
	return en.GetScene().GetMonster()
}
