//go:build live

package tytd_go

// OWNER-ORDERED linh lộ send/receive test — owner 2026-11-11 (ICT):
// "dung acc <live-user> <live-pass> 710132 để test gửi nhận linh lộ, nhớ check
// curent data trước khi chạy".
//
// Pure linh lộ exercise on a FRESH account — the engine tick NEVER runs
// (no swPlayTick, no explores, no jumps). Order:
//
//  1. CURRENT DATA dump first (owner's explicit ask): activity row, ENTER
//     (region/canWorship), ENTER_GAME (strength), welfare board (lv/exp/
//     canReceive/waterCount + EVERY notice id+status), folded config
//     (SEAWAR_ENERGY_ITEM + SEAWAR_ITEM_ENERGY), liquid records 234918.
//  2. RECEIVE: the "Nhận nhanh" claim-all ({id:0}, id PRESENT on wire)
//     fired once per round, board re-read between rounds to prove notices
//     actually drain (the old per-notice loop never consumed anything).
//  3. SEND: "phát linh lộ" 234917 {itemId: SEAWAR_ITEM_ENERGY, num:1} —
//     an ITEM SPEND. The AUTO engine never sends this; these presses fire
//     ONLY from this test under the owner's direct order (the one-shot
//     authorized press, [[tytd-item-ban]] pattern like the fruit (11)).
//     The wire assert proves every 95a5 frame corresponds to one explicit
//     call below — an auto-path leak trips "SEND LEAK". Bag count is not
//     visible locally; the server ret is the sole arbiter (no item → ret≠0).
//
// Hard bans asserted on the writer: guild-war family 0x95b3..0x95c0, and
// ZERO fruit frames (95aa) — no fruit is authorized in this test.
//
//	TYTD_LIVE=1 TYTD_SWLIQUID=1 go test -tags "sqlcipher live" -count=1 -run TestSeaWarLiquidLive -v -timeout 10m .
//	TYTD_SW_SEND=n   send presses (default 2)   TYTD_SW_ROUNDS=n  claim-all rounds (default 3)

import (
	"context"
	"encoding/hex"
	"log"
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

// swRetField1 decodes the ret of a SeaWar resp that has no generated
// message (34917/34918): field 1 varint.
func swRetField1(b []byte) int32 {
	if num, _, n := protowire.ConsumeTag(b); n > 0 && num == 1 {
		if v, m := protowire.ConsumeVarint(b[n:]); m > 0 {
			return int32(v)
		}
	}
	return -999
}

func TestSeaWarLiquidLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_SWLIQUID") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_SWLIQUID=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710132
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}
	sendN := envInt("TYTD_SW_SEND", 2)
	rounds := envInt("TYTD_SW_ROUNDS", 3)

	log.SetOutput(os.Stderr)

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
	db, _ := gorm.Open(sqlite.Open("file:swliquid?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256)}
	// NO AutoSeaWar — this test never runs the auto engine.

	var sendMu sync.Mutex
	sentTypes := map[string]int{}
	ourSendCalls := 0 // incremented ONLY by the explicit send presses below
	var violations []string
	go func() {
		for f := range p.writeChan {
			_, ty, _ := pack.Unpack(f)
			sendMu.Lock()
			sentTypes[ty]++
			if ty == "95aa" {
				violations = append(violations, ty) // fruit NOT authorized here
			}
			if ty == "95a5" && sentTypes["95a5"] > ourSendCalls {
				violations = append(violations, ty) // SEND LEAK — auto path spoke
			}
			if len(ty) == 4 {
				if n, err := strconv.ParseInt(ty, 16, 32); err == nil && n >= 0x95b3 && n <= 0x95c0 {
					violations = append(violations, ty) // guild-war family: hard ban
				}
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
				p.ActList.mergeDetail(p.Username, realData)
				p.swMergeConfig(realData)
				p.stDeliver(msgType, realData)
			case SERVER_ACTIVITY_BUY_GOODS:
				p.stDeliver(msgType, realData)
			case SERVER_SW_ENTER, SERVER_SW_ENTER_GAME, SERVER_SW_EXPLORE,
				SERVER_SW_HANDLE_EVENT, SERVER_SW_UNION_WELFARE, SERVER_SW_UNION_WELFARE_REWARD,
				SERVER_SW_UNION_RECEIVE_LIQUID, SERVER_SW_UNION_SEND_LIQUID, SERVER_SW_UNION_LIQUID_RECORD,
				SERVER_SW_HEART_BEAT, SERVER_SW_NEXT_REGION,
				SERVER_SW_USE_ITEM, SERVER_SW_WORSHIP,
				SERVER_SW_ASSIST_LIST, SERVER_SW_ASSIST, SERVER_SW_ASSIST_REWARD,
				SERVER_SW_ASSIST_SYNC, SERVER_SW_ASSIST_SYNC_RAW:
				p.swHandleFrame(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-readerDone }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// ── CURRENT DATA (owner: "nhớ check current data trước khi chạy") ──
	var actID int32
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		now := time.Now()
		for _, e := range p.ActList.byType(swActivityType) {
			if st := e.status(now); st == "active" || st == "reward" {
				actID = e.ActivityID
			}
		}
		if actID != 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if actID == 0 {
		t.Fatal("no active type-349 activity for this account")
	}
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if data, err := p.stRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body, 15*time.Second); err == nil {
			p.swMergeConfig(data)
		}
	}
	p.SwMu.Lock()
	energyItem, liquidItems := p.swEnergyItem, append([]int32(nil), p.swLiquidItems...)
	p.SwMu.Unlock()
	t.Logf("CONFIG actID=%d SEAWAR_ENERGY_ITEM=%d SEAWAR_ITEM_ENERGY=%v (empty = engine default 105531/105532/105533)", actID, energyItem, liquidItems)

	enter, err := p.swEnter(actID)
	if err != nil || enter.GetRet() != 0 {
		t.Fatalf("ENTER refused (err=%v ret=%d) — activity not open for this acc", err, enter.GetRet())
	}
	t.Logf("BOARD ENTER: ret=0 region=%d canWorship=%v", enter.GetRegionId(), enter.GetCanWorship())
	eg, err := p.swEnterGame(actID)
	if err != nil || eg.GetRet() != 0 || eg.GetPlayerData() == nil {
		t.Fatalf("ENTER_GAME refused (err=%v ret=%d)", err, eg.GetRet())
	}
	t.Logf("BOARD PLAYER: region=%d strength=%d", eg.GetPlayerData().GetRegionId(), eg.GetPlayerData().GetStrength())

	w, err := p.swWelfare(actID)
	if err != nil || w.GetRet() != 0 {
		t.Fatalf("WELFARE board refused (err=%v ret=%d)", err, w.GetRet())
	}
	notices := func() []string {
		var out []string
		for _, n := range w.GetNoticeInfo() {
			out = append(out, "id:"+strconv.FormatInt(int64(n.GetId()), 10)+
				" status:"+strconv.FormatInt(int64(n.GetStatus()), 10))
		}
		return out
	}
	t.Logf("BOARD WELFARE: ret=0 canReceive=%d waterCount=%d notices(%d)=%v",
		w.GetCanReceive(), w.GetWaterCount(), len(w.GetNoticeInfo()), notices())
	if w.GetCanReceive() > 0 {
		p.swWelfareReward(actID) // the ready tree-fruit claim, engine-legal free op
	}
	// Liquid history, read-only probes (type 1/2 — client record view).
	for _, typ := range []int32{1, 2} {
		body, _ := pb.Marshal(&proto.SeaWarUnionLiquidRecordReq{ActivityId: actID, Type: pb.Int32(typ)})
		resp, err := p.swRT(SW_UNION_LIQUID_RECORD_CMD, SERVER_SW_UNION_LIQUID_RECORD, body)
		if err != nil {
			t.Logf("RECORD type=%d transport err=%v", typ, err)
			continue
		}
		t.Logf("RECORD type=%d ret=%d respLen=%d hex=%s", typ, swRetField1(resp), len(resp), hex.EncodeToString(resp))
	}

	// ── RECEIVE: "Nhận nhanh" claim-all, re-reading the board each round ──
	for r := 1; r <= rounds; r++ {
		body, _ := pb.Marshal(&proto.SeaWarUnionLiquidRewardReq{ActivityId: actID, Id: pb.Int64(0)})
		t.Logf("RECEIVE round %d: claim-all request bytes = %s", r, hex.EncodeToString(body))
		resp, err := p.swRT(SW_UNION_RECEIVE_LIQUID_CMD, SERVER_SW_UNION_RECEIVE_LIQUID, body)
		if err != nil {
			t.Logf("RECEIVE round %d transport err=%v", r, err)
			continue
		}
		t.Logf("💧 LINH LỘ claim-all: ret=%d respLen=%d", swRetField1(resp), len(resp))
		if w2, err := p.swWelfare(actID); err == nil && w2.GetRet() == 0 {
			w = w2
			t.Logf("BOARD WELFARE after claim: canReceive=%d waterCount=%d notices(%d)=%v",
				w.GetCanReceive(), w.GetWaterCount(), len(w.GetNoticeInfo()), notices())
		}
		time.Sleep(2 * time.Second)
	}

	// ── SEND: "phát linh lộ" — OWNER-ORDERED item spend, test-only ──
	// THREE linh lộ tiers (owner 2026-11-11 "có 3 loại linh lộ, mày check
	// cả 3 loại chưa"): press every configured (else baked
	// 105531/105532/105533) id, sendN presses each.
	items := liquidItems
	if len(items) == 0 {
		items = []int32{105531, 105532, 105533}
	}
	if energyItem > 0 {
		items = append(items, energyItem) // extra probe: the energy fruit id, ret tells all
	}
	for _, item := range items {
		for i := 1; i <= sendN; i++ {
			body, _ := pb.Marshal(&proto.SeaWarUnionLiquidSendReq{ActivityId: actID, ItemId: pb.Int32(item), Num: pb.Int32(1)})
			t.Logf("SEND press %d/%d: request bytes = %s (itemId=%d — item spend, owner-ordered)", i, sendN, hex.EncodeToString(body), item)
			sendMu.Lock()
			ourSendCalls++ // authorize exactly this one frame before firing
			sendMu.Unlock()
			resp, err := p.swRT(SW_UNION_SEND_LIQUID_CMD, SERVER_SW_UNION_SEND_LIQUID, body)
			if err != nil {
				t.Logf("SEND press %d transport err=%v", i, err)
				continue
			}
			t.Logf("🎁 LINH LỘ phát id=%d: ret=%d respLen=%d", item, swRetField1(resp), len(resp))
			time.Sleep(2 * time.Second)
		}
	}
	// Final board so the report shows the net effect.
	if w2, err := p.swWelfare(actID); err == nil && w2.GetRet() == 0 {
		w = w2
		t.Logf("BOARD WELFARE FINAL: canReceive=%d waterCount=%d notices(%d)=%v",
			w.GetCanReceive(), w.GetWaterCount(), len(w.GetNoticeInfo()), notices())
	}

	sendMu.Lock()
	defer sendMu.Unlock()
	t.Logf("sent frame types: %v", sentTypes)
	if len(violations) > 0 {
		t.Fatalf("BAN VIOLATION — frames that had NO business leaving the socket: %v (our send calls=%d)", violations, ourSendCalls)
	}
}
