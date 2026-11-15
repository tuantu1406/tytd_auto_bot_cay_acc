//go:build live

package tytd_go

// FULL-ENGINE live test for Tiên Cảnh Vị Ương (WeYound 186) — owner 2026-10-18
// "tiên cảnh vị ương đang mở, vào test đi, test bằng acc sonbinhsb".
//
// Drives the REAL engine on a live session (same harness as
// reborntrial_score_live_test): real Player + armed Player-field gate
// (AutoTienCanh=true, owner 2026-10-27),
// one full loop pass = wyWaterTree → wyShopSweep → wyGiftCycle → wyTick, with
// every WeYound frame routed through the production handlers (wyHandleFrame /
// wyMergeConfig) so the round-trip seats, config folding and notifies all run
// exactly as in production. It also asserts the HARD item ban: the writer
// goroutine inspects EVERY outbound frame and the test fails if USE_ITEM
// (000355ed), AUTO_SWITCH (000355f8) or any PVP challenge code ever leaves.
//
//	TYTD_LIVE=1 go test -tags "sqlcipher live" -count=1 -run TestTienCanhEngineLive -v -timeout 420s .

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

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestTienCanhEngineLive(t *testing.T) {
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
	db, _ := gorm.Open(sqlite.Open("file:tclive?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid), AutoTienCanh: true}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256),
		AutoTienCanh: true} // Player-object gate (owner 2026-10-27)

	// ── ban bookkeeping: every outbound frame type is recorded + checked ──
	var sendMu sync.Mutex
	sentTypes := map[string]int{}
	// pack.Unpack reports the LAST 2 bytes of the opcode (transport only
	// carries 2 funcType bytes), so the ban set is keyed in that scheme.
	// 0x55E8 = 218600, so: USE_ITEM 218605→"55ed", AUTO_SWITCH 218616→
	// "55f8", PVP challenges 218630..218635 → 0x5606..0x560b. Gifts
	// 218636/218637 = 0x560c/0x560d are ALLOWED — RECEIVE is a free receive.
	banned := func(ty string) bool {
		if ty == "55ed" || ty == "55f8" {
			return true
		}
		if len(ty) == 4 {
			if n, err := strconv.ParseInt(ty, 16, 32); err == nil && n >= 0x5606 && n <= 0x560b {
				return true
			}
		}
		return false
	}
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

	// ── wait for the live 186 window ──
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
		t.Fatal("no active type-186 activity (owner said it IS open)")
	}
	t.Logf("live 186 activityId=%d", actID)

	// Fold config FIRST (window / donate ids / mall rows / event cfg) exactly
	// like the hourly pull would.
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if data, err := p.wyRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err == nil {
			p.wyMergeConfig(data)
		} else {
			t.Logf("21003 detail pull failed (%v) — engine continues blind-safe", err)
		}
	}
	p.WyMu.Lock()
	t.Logf("config folded: window=%d..%d energyItem=%d liquidIDs=%v mallRows=%d eventCfg=%d",
		p.wyOpenSec, p.wyCloseSec, p.wyEnergyItem, p.wyLiquidIDs, len(p.wyMallRows), len(p.wyEventCfg))
	p.WyMu.Unlock()

	// ── one full loop pass, production order ──
	// rawView walks the RAW 48a9 scene (field walk) and histograms f5 cells —
	// the generated-proto view must match this, or our field numbers drifted.
	rawView := func(tag string) {
		body, _ := pb.Marshal(&proto.WeYoundEnterReq{ActivityId: pb.Int32(actID)})
		data, err := p.wyRoundTrip(WE_YOUND_ENTER_CMD, SERVER_WE_YOUND_ENTER, body)
		if err != nil {
			t.Logf("rawView %s: %v", tag, err)
			return
		}
		var rsp proto.WeYoundEnterResp
		_ = pb.Unmarshal(data, &rsp)
		sc := rsp.GetScene()
		vc := 0
		if sc != nil {
			vc = len(sc.GetView())
		}
		// protowire: enter → f2 scene → f5 cells
		f5 := 0
		types := map[int32]int{}
		var px, py, hp, floor int32
		for b := data; len(b) > 0; {
			num, wt, n := protowire.ConsumeField(b)
			if n <= 0 {
				break
			}
			if num == 2 && wt == protowire.BytesType {
				sb, _ := protowire.ConsumeBytes(b[1:])
				for s := sb; len(s) > 0; {
					sn, swt, sn2 := protowire.ConsumeField(s)
					if sn2 <= 0 {
						break
					}
					if sn == 5 && swt == protowire.BytesType {
						cb, _ := protowire.ConsumeBytes(s[1:])
						var c proto.WeYoundCellMsg
						if pb.Unmarshal(cb, &c) == nil {
							f5++
							types[c.GetType()]++
						}
					}
					s = s[sn2:]
				}
			}
			b = b[n:]
		}
		if sc != nil {
			floor = sc.GetFloor()
			if pd := sc.GetPlayer(); pd != nil {
				if pp := pd.GetPos(); pp != nil {
					px, py = pp.GetX(), pp.GetY()
				}
				hp = pd.GetHp()
			}
		}
		t.Logf("rawView %s: proto-view=%d raw-f5=%d types%v floor=%d pos=(%d,%d) hp=%d rawlen=%d",
			tag, vc, f5, types, floor, px, py, hp, len(data))
	}

	rawView("FRESH")
	p.wyWaterTree(actID)
	p.wyShopSweep(actID)
	p.wyGiftCycle(actID)
	rawView("AFTER-TREE-SHOP-GIFT")
	inWindow, nextOpen := p.wyWindow()
	t.Logf("explore window open=%v next=%s", inWindow, nextOpen.In(ictZone).Format("15:04"))
	ok := p.wyTick(actID)
	t.Logf("wyTick keep-alive=%v", ok)
	rawView("AFTER-TICK")

	// ── board readout after the pass ──
	enter, err := p.wyEnter(actID, false)
	if err != nil || enter.GetRet() != 0 {
		t.Fatalf("post-tick ENTER err=%v ret=%d", err, enter.GetRet())
	}
	sc := enter.GetScene()
	if sc == nil || sc.GetPlayer() == nil {
		t.Fatal("no scene/player in post-tick ENTER")
	}
	pd := sc.GetPlayer()
	t.Logf("AFTER: floor=%d pos=(%d,%d) hp=%d view=%d monsters=%d unDeal=%d explored=%d",
		sc.GetFloor(), pd.GetPos().GetX(), pd.GetPos().GetY(), pd.GetHp(),
		len(sc.GetView()), len(sc.GetMonster()), len(pd.GetUnDealCell()), len(p.wyExplored))
	alive := 0
	for _, m := range sc.GetMonster() {
		if parseBlood(m.GetRemainBloods()) > 0 {
			alive++
		}
	}
	t.Logf("monsters alive=%d/%d", alive, len(sc.GetMonster()))

	// ── notifies the pass produced ──
	for {
		select {
		case n := <-p.NotiChan:
			t.Logf("NOTIFY: %s", n.MSG)
		default:
			goto done
		}
	}
done:
	sendMu.Lock()
	defer sendMu.Unlock()
	keys := make([]string, 0, len(sentTypes))
	for k, v := range sentTypes {
		keys = append(keys, fmt.Sprintf("%s×%d", k, v))
	}
	t.Logf("outbound frames: %v", keys)
	if len(bannedSeen) > 0 {
		t.Errorf("ITEM BAN VIOLATION — banned frames sent: %v", bannedSeen)
	}
}
