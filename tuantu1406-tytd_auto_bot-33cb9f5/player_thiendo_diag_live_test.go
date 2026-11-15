//go:build live

package tytd_go

// Read-only TIÊN VỰC THƯƠNG ĐỒ (type 150) diagnosis for a reported "auto
// không chạy event" (customer <live-user>@710565, owner 2026-10-27). NEVER buys,
// sails, speed-ups or settles — only: login → AUTH → dump EVERY type-150
// activity row (status/begin/end/now-ICT) → stActiveActivity → stSnapshot
// (ENTER + ENTER_MAP are pure reads) and dump ret + ship + funds + goods
// count. That separates the four failure stories the loop can be stuck in:
// row absent (account not eligible / server 710565 lacks the event), row
// announced-but-future (window not open yet), active-but-snapshot-refused
// (server's own trading window), healthy (loop fine ⇒ the DB toggle is off
// on the customer's row — tell them /auto_thiendo_tienvuc true).
//
//	TYTD_LIVE=1 TYTD_ST_DIAG=1 [TYTD_LIVE_USER=... TYTD_LIVE_PASS=... TYTD_LIVE_SERVER=...] go test -count=1 -tags "sqlcipher live" -run TestLiveSkyTradeDiag -v -timeout 180s .

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLiveSkyTradeDiag(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ST_DIAG") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ST_DIAG=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710565
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}
	pt, err := request.LoginGPassport(user, pass)
	if err != nil {
		t.Fatalf("login passport: %v", err)
	}
	sd, uidF, err := request.LoginFuntap(pt)
	if err != nil {
		t.Fatalf("login funtap: %v", err)
	}
	gu, err := request.GetServerURL(uidF, server)
	if err != nil {
		t.Fatalf("server url: %v", err)
	}
	token, wsURL, uid, err := request.GetWSKey(gu, sd)
	if err != nil {
		t.Fatalf("ws key: %v", err)
	}
	t.Logf("acc %s uid=%d server=%d · %s", user, uid, server, time.Now().In(ictZone).Format("15:04:05 ICT"))
	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, _ := gorm.Open(sqlite.Open("file:st_diag?mode=memory&cache=shared"), &gorm.Config{})
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	p := &Player{
		Username: user, Name: user, UID: uid, ChatID: int64(uid),
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan:  make(chan NotifyMGS, 64),
		writeChan: make(chan []byte, 64),
	}
	go func() {
		for frame := range p.writeChan {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch ty {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.stMergeConfig(d)
			case SERVER_SKY_TRADE_ENTER, SERVER_SKY_TRADE_ENTER_MAP, SERVER_SKY_TRADE_SPEED_UP,
				SERVER_SKY_TRADE_MOVE, SERVER_SKY_TRADE_BUY, SERVER_SKY_TRADE_MOVE_FINISH,
				SERVER_SKY_TRADE_REPORT_GOODS:
				p.stHandleFrame(ty, d)
			}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// Give the login activity pushes a few beats to land.
	time.Sleep(5 * time.Second)
	now := time.Now()
	rows := p.ActList.byType(skyTradeActivityType)
	if len(rows) == 0 {
		fmt.Printf("VERDICT-STDIAG: KHÔNG có dòng type-150 nào trong actList — sự kiện không tồn tại trên server %d cho acc này (hoặc danh sách chưa về)\n", server)
		// Dump the full roster so we can eyeball near-miss types.
		for _, e := range p.ActList.snapshot() {
			t.Logf("act type=%d id=%d status=%s", e.Type, e.ActivityID, e.status(now))
		}
		return
	}
	for _, e := range rows {
		t.Logf("type-150 id=%d status=%s begin=%s end=%s",
			e.ActivityID, e.status(now),
			time.UnixMilli(e.BeginTime).In(ictZone).Format("15:04:05"),
			time.UnixMilli(e.EndTime).In(ictZone).Format("15:04:05"))
	}
	if begin, found := p.stNextOpening(now); found {
		t.Logf("cửa mở kế tiếp: %s", time.UnixMilli(begin).In(ictZone).Format("15:04:05 ICT"))
	}
	actID, ok := p.stActiveActivity()
	if !ok {
		fmt.Println("VERDICT-STDIAG: type-150 có trong danh sách nhưng CHƯA ACTIVE — loop đúng, chỉ là chưa tới giờ (10:00–22:00 ICT)")
		return
	}
	t.Logf("activity %d ACTIVE — gọi snapshot (read-only)", actID)
	snap, err := p.stSnapshot(actID, 20*time.Second)
	if err != nil {
		fmt.Printf("VERDICT-STDIAG: snapshot lỗi đường truyền: %v\n", err)
		return
	}
	if snap.GetRet() != 0 {
		fmt.Printf("VERDICT-STDIAG: hoạt động ACTIVE nhưng server trả ret=%d cho ENTER_MAP — cửa sổ giao dịch nội bộ đóng\n", snap.GetRet())
		return
	}
	pd := snap.GetPlayerData()
	ship := pd.GetAirshipInfo()
	fmt.Printf("VERDICT-STDIAG: SỰ KIỆN + CỬA SỐ的健康 — ret=0 funds=%d goods=%d event=%d ship=%v — engine đủ điều kiện chạy; nếu khách vẫn im thì DO toggle AutoSkyTrade=0 trên row của họ (/auto_thiendo_tienvuc true)\n",
		pd.GetAvailableFunds(), len(pd.GetGoods()), snap.GetEvent().GetEventType(), ship != nil)
	if ship == nil {
		return
	}
	t.Logf("ship: source=%d target=%d startTime=%d speeds=%d",
		ship.GetSourcePort(), ship.GetTargetPort(), ship.GetStartTime(), ship.GetSpeeds())

	// What the brain SEES (owner 2026-10-27: "có báo cáo port mà không thấy
	// chạy" — dump the exact inputs of skyTradeDecide + its verdict so the
	// blocking gate is named: rare-match, specialty anchor, price gap, or
	// wallet).
	here := stMarket(snap.GetPortGoods(), ship.GetSourcePort())
	t.Logf("đặc sản cảng hiện tại (%d) = %d · hold=%v", ship.GetSourcePort(),
		p.stSpecialGood(ship.GetSourcePort()), func() []int32 {
			var ids []int32
			for _, g := range pd.GetGoods() {
				ids = append(ids, g.GetGoodsId())
			}
			return ids
		}())
	for _, m := range snap.GetPortGoods() {
		t.Logf("cảng %d rare=%d hàng=%d", m.GetPortId(), m.GetRareGoodsId(), len(m.GetGoods()))
		if here != nil && m.GetPortId() == ship.GetSourcePort() {
			for _, g := range m.GetGoods() {
				t.Logf("  %s ×%d giá %d", stGoodsLabel(g.GetGoodsId()), g.GetNum(), g.GetPrice())
			}
		}
	}
	if ship.GetTargetPort() == 0 && snap.GetEvent().GetEventType() == 0 {
		plan := skyTradeDecide(ship.GetSourcePort(), pd.GetAvailableFunds(), pd.GetGoods(), snap.GetPortGoods(), p.stSpecialGood(ship.GetSourcePort()))
		fmt.Printf("VERDICT-DECIDE: %s (action=%d profit=%d dest=%d)\n", stPlanDesc(plan), plan.action, plan.profit, plan.destPort)
	}
}

// TestLiveSkyTradeRun drives the REAL loop (skyTradeAutoLoopAfter) on the
// customer's account with the opt-in row armed, so every fix path is walked
// for real: stuck arrival claim → claim + trade (2026-10-27). Notifications
// ride sendNotify behind the probe's Notify switch (owner 2026-10-27 killed
// forceNotify and the 5-minute progress heartbeat — only real events speak
// now). Buys/sails are the engine's own opt-in auto path (the customer
// enabled /auto_thiendo_tienvuc) — never items, never speed-ups (the speed
// toggle stays off on the row).
//
//	TYTD_LIVE=1 TYTD_ST_RUN=1 [TYTD_ST_RUN_SECS=120] [TYTD_LIVE_USER=... TYTD_LIVE_PASS=... TYTD_LIVE_SERVER=...] go test -count=1 -tags "sqlcipher live" -run TestLiveSkyTradeRun -v -timeout 300s .
func TestLiveSkyTradeRun(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ST_RUN") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ST_RUN=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710565
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}
	secs := 120
	if v := os.Getenv("TYTD_ST_RUN_SECS"); v != "" {
		n, _ := strconv.Atoi(v)
		secs = n
	}
	dbPath := "build/ohop1/data.dat"
	if v := os.Getenv("TYTD_ST_DB"); v != "" {
		dbPath = v
	}
	pt, err := request.LoginGPassport(user, pass)
	if err != nil {
		t.Fatalf("login passport: %v", err)
	}
	sd, uidF, err := request.LoginFuntap(pt)
	if err != nil {
		t.Fatalf("login funtap: %v", err)
	}
	gu, err := request.GetServerURL(uidF, server)
	if err != nil {
		t.Fatalf("server url: %v", err)
	}
	token, wsURL, uid, err := request.GetWSKey(gu, sd)
	if err != nil {
		t.Fatalf("ws key: %v", err)
	}
	t.Logf("acc %s uid=%d server=%d · loop chạy %ds từ %s", user, uid, server, secs, time.Now().In(ictZone).Format("15:04:05 ICT"))
	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The instance's EXISTING database file, opened STRICTLY READ-ONLY
	// (owner 2026-10-27 "sử dụng file database có sẵn (cấm sửa)"): mode=ro
	// makes a write physically impossible. The row only SEEDS the Player
	// gates here — after the refactor the loop reads the Player fields, so
	// seeding from the customer's actual row is what the deployed instance's
	// session carries.
	db, err := gorm.Open(sqlite.Open("file:"+dbPath+"?mode=ro&_pragma_key="+getDK()+"&_busy_timeout=30000"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open %s read-only: %v", dbPath, err)
	}
	var all []model.User
	if err := db.Find(&all).Error; err != nil {
		t.Fatalf("read users from %s: %v", dbPath, err)
	}
	found := false
	var seed *model.User
	for i, u := range all {
		if u.Username == user {
			found = true
			seed = &all[i]
			t.Logf("row thật trong %s: id=%s gameUID=%d server=%d AutoSkyTrade=%v AutoSkyTradeSpeed=%v VIP=%d running=%v",
				dbPath, u.ID, u.GameUID, u.ServerID, u.AutoSkyTrade, u.AutoSkyTradeSpeed, u.VipLevel, u.Running)
		}
	}
	if !found {
		fmt.Printf("VERDICT-DB: acc %s KHÔNG tồn tại trong DB ohop1 (%s, %d dòng users) — tài khoản này không do instance đó quản\n", user, dbPath, len(all))
	}
	p := &Player{
		Username: user, Name: user, UID: uid, ChatID: int64(uid),
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		// The loop notifies via sendNotify now (no forceNotify — owner
		// 2026-10-27); arm the switch so the probe observes what a notified
		// owner would actually see.
		Notify:    true,
		NotiChan:  make(chan NotifyMGS, 256),
		writeChan: make(chan []byte, 64),
		// Decision cadence shrunk so a 2-minute window shows several turns.
		stPoll: 20 * time.Second,
	}
	// Seed the Player-object gates exactly like a real login would
	// (seedAutoGates from the session's own row — never a game_uid query).
	if seed != nil {
		p.seedAutoGates(seed)
	}
	if os.Getenv("TYTD_ST_FORCE_ON") == "1" {
		p.AutoSkyTrade, p.AutoSkyTradeSpeed = true, true
		fmt.Println("VERDICT-DB: TYTD_ST_FORCE_ON=1 — gate bật CƯỠNG trên Player để quan sát vòng chạy (không phản ánh row thật)")
	}
	if !p.AutoSkyTrade {
		fmt.Printf("VERDICT-DB: gate AutoSkyTrade của phiên = OFF (theo row thật) — loop sẽ chỉ ghi 'tắt'; khách cần /auto_thiendo_tienvuc true\n")
	}
	go func() {
		for frame := range p.writeChan {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch ty {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.stMergeConfig(d)
			case SERVER_SKY_TRADE_ENTER, SERVER_SKY_TRADE_ENTER_MAP, SERVER_SKY_TRADE_SPEED_UP,
				SERVER_SKY_TRADE_MOVE, SERVER_SKY_TRADE_BUY, SERVER_SKY_TRADE_MOVE_FINISH,
				SERVER_SKY_TRADE_REPORT_GOODS:
				p.stHandleFrame(ty, d)
			}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// Let the login activity pushes land, then start the REAL loop.
	time.Sleep(5 * time.Second)
	go p.skyTradeAutoLoopAfter(2 * time.Second)

	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case n := <-p.NotiChan:
			fmt.Printf("NOTIFY: %s\n", n.MSG)
		case <-time.After(2 * time.Second):
		}
	}
	fmt.Println("VERDICT-RUN: hết cửa sổ — đối chiếu NOTIFY ở trên: phải CÓ claim 'cập cảng' (nếu có báo treo), và các tin bán/mua/xuất phát khi thuyền thật sự chạy (không còn heartbeat tiến độ — owner đã bỏ 2026-10-27)")
}
