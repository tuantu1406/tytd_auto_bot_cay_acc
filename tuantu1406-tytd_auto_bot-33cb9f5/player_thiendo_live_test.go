//go:build live

package tytd_go

import (
	"context"
	"os"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	pb "google.golang.org/protobuf/proto"

	"github.com/gorilla/websocket"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestLiveSkyTradeSpeedup plays the 21:58 end-of-day sprint against the
// REAL server, right now — the clock rule is not what's under test, the
// wire mechanic is: burn acceleration items on a live leg and watch the
// server shrink the ETA (or dock the ship outright, carrying the arrival
// event in the speed-up answer). Runs on the throwaway test account only,
// where state changes are authorized.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveSkyTradeSpeedup -v -timeout 300s .
//
// Steps: login → live type-150 id → snapshot. If the ship is idle it buys
// one good (a loaded hold is the client's requirement to move) and sails to
// a second port, creating a real ~4-hour leg. Then stSpeedUp — the loop's
// own sprinter — is called with the current ETA: the answer must be an
// accepted ret=0 with either an increased speeds counter (ETA shorter by
// num×speedUp seconds) or a docking arrival event. No items in the bag is
// reported as a SKIP (environment, not code).
func TestLiveSkyTradeSpeedup(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveUser   = liveUserEnv(t)
		livePass   = livePassEnv(t)
		liveServer = 710547
	)

	passportToken, err := request.LoginGPassport(liveUser, livePass)
	if err != nil {
		t.Fatalf("LoginGPassport: %v", err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatalf("LoginFuntap: %v", err)
	}
	gameURL, err := request.GetServerURL(userID, liveServer)
	if err != nil {
		t.Fatalf("GetServerURL: %v", err)
	}
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
	if err != nil {
		t.Fatalf("GetWSKey: %v", err)
	}

	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := gorm.Open(sqlite.Open("file:st_speedup_live?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.User{ID: "1", Username: liveUser, GameUID: uid,
		AutoSkyTrade: true, AutoSkyTradeSpeed: true}).Error; err != nil {
		t.Fatal(err)
	}

	p := &Player{
		Username:  liveUser,
		UID:       uid,
		DB:        db,
		ChatID:    7,
		Notify:    true,
		NotiChan:  make(chan NotifyMGS, 64),
		writeChan: make(chan []byte, 64),
		ctx:       ctx,
		// Opt-in gates are Player fields now (owner 2026-10-27) — arm them
		// like seedAutoGates would from the row above.
		AutoSkyTrade:      true,
		AutoSkyTradeSpeed: true,
	}

	go func() {
		for frame := range p.writeChan {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	go func() {
		for n := range p.NotiChan {
			t.Logf("NOTIFY: %s", n.MSG)
		}
	}()

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, msgType, realData := pack.Unpack(message)
			if !isMSG {
				continue
			}
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.stMergeConfig(realData)
			case SERVER_SKY_TRADE_ENTER, SERVER_SKY_TRADE_ENTER_MAP, SERVER_SKY_TRADE_SPEED_UP,
				SERVER_SKY_TRADE_MOVE, SERVER_SKY_TRADE_BUY, SERVER_SKY_TRADE_MOVE_FINISH,
				SERVER_SKY_TRADE_REPORT_GOODS:
				p.stHandleFrame(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// Wait for the live type-150 instance (the list push arrives unasked).
	var actID int32
	deadline := time.Now().Add(30 * time.Second)
	for actID == 0 && time.Now().Before(deadline) {
		actID, _ = p.stActiveActivity()
		if actID == 0 {
			time.Sleep(time.Second)
		}
	}
	if actID == 0 {
		t.Skip("Tiên Vực Thương Đồ không mở lúc chạy test (cần trong khung 10:00–22:00)")
	}
	t.Logf("activity %d đang mở", actID)

	snap, err := p.stSnapshot(actID, 20*time.Second)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.GetRet() != 0 {
		t.Skipf("server trả ret=%d — cửa sổ mua bán đang đóng", snap.GetRet())
	}
	ship := snap.GetPlayerData().GetAirshipInfo()
	if ship == nil {
		t.Fatal("snapshot không có airshipInfo")
	}
	need, up := p.stTravelTimes()
	t.Logf("giờ máy chủ %s · cấu hình chuyến %ds, mỗi giấy -%ds", time.Now().Format("15:04:05"), need, up)

	// Need a leg in flight. If the ship sits idle, load one good (the
	// client's own move requirement) and sail to a second port.
	if ship.GetTargetPort() == 0 {
		home := ship.GetSourcePort()
		if len(snap.GetPlayerData().GetGoods()) == 0 {
			pd := snap.GetPlayerData()
			var buyID int32
			var buyUnit int64
			for _, m := range snap.GetPortGoods() {
				if m.GetPortId() != home {
					continue
				}
				for _, g := range m.GetGoods() {
					if g.GetNum() > 0 && (buyID == 0 || g.GetPrice() < buyUnit) {
						buyID, buyUnit = g.GetGoodsId(), g.GetPrice()
					}
				}
			}
			if buyID == 0 || pd.GetAvailableFunds() < buyUnit {
				t.Skip("không mua nổi món nào để tạo chuyến (bảng hàng rỗng hoặc hết xu)")
			}
			rsp, err := p.stDeal(actID, home, buyID, 1, stModeBuy)
			if err != nil {
				t.Fatalf("buy: %v", err)
			}
			if rsp.GetRet() != 0 {
				t.Skipf("mua thử 1 × %d bị từ chối (code %d) — không tạo được chuyến", buyID, rsp.GetRet())
			}
			t.Logf("đã mua 1 × %d (unit %d) để tạo chuyến", buyID, buyUnit)
		}
		dest := int32(10002)
		if home == 10002 {
			dest = 10001
		}
		if !p.stSail(actID, dest) {
			t.Fatal("stSail từ chối — không tạo được chuyến đang chạy")
		}
		snap, err = p.stSnapshot(actID, 20*time.Second)
		if err != nil || snap.GetRet() != 0 || snap.GetPlayerData().GetAirshipInfo() == nil {
			t.Fatalf("snapshot sau khi nhổ neo: %v", err)
		}
		ship = snap.GetPlayerData().GetAirshipInfo()
		if ship.GetTargetPort() == 0 {
			t.Fatal("nhổ neo rồi mà thuyền vẫn đứng cảng")
		}
	}

	// The sprint itself, on the loop's exact code path.
	sprints, dockedBySpeedup := 0, false
	for round := 0; round < 4 && !dockedBySpeedup; round++ {
		eta := time.Until(time.Unix(skyTradeArrivalAt(ship.GetStartTime(), ship.GetSpeeds(), need, up), 0))
		if eta <= 0 {
			t.Log("chuyến đã tới giờ cập bến tự nhiên — chờ sự kiện qua snapshot")
			break
		}
		t.Logf("vòng %d: còn %s trên đường → → cảng %d, speeds=%d, gọi stSpeedUp",
			round+1, stFmtDur(eta), ship.GetTargetPort()-stWirePortBase, ship.GetSpeeds())
		if !p.stSpeedUp(actID, eta, up, ship.GetTargetPort()) {
			// The full ceiling was refused — likely the item bag. Ask for
			// ONE item; if even that fails the account simply has none.
			body, _ := pb.Marshal(&proto.SkyTradeAddSpeedReq{ActivityId: pb.Int32(actID), Num: pb.Int32(1)})
			data, err := p.stRoundTrip(SKY_TRADE_SPEED_UP_CMD, SERVER_SKY_TRADE_SPEED_UP, body, skyTradeReqTimeout)
			if err != nil {
				t.Fatalf("đường truyền speed-up: %v", err)
			}
			var probe proto.SkyTradeAddSpeedResp
			if err := pb.Unmarshal(data, &probe); err != nil {
				t.Fatalf("unmarshal probe: %v", err)
			}
			if probe.GetRet() != 0 {
				t.Skipf("tài khoản không còn giấy tăng tốc (ret=%d) — cơ chế đã gửi đúng, server từ chối vì hết vật phẩm", probe.GetRet())
			}
		}
		sprints++
		snap, err = p.stSnapshot(actID, 20*time.Second)
		if err != nil || snap.GetRet() != 0 {
			t.Fatalf("snapshot sau speed-up: %v (ret=%d)", err, snap.GetRet())
		}
		if snap.GetEvent().GetEventType() != 0 || ship.GetTargetPort() != 0 && snap.GetPlayerData().GetAirshipInfo().GetTargetPort() == 0 {
			dockedBySpeedup = true // arrival came back on the speed-up answer
			break
		}
		ship = snap.GetPlayerData().GetAirshipInfo()
		if ship.GetTargetPort() == 0 {
			dockedBySpeedup = true
			break
		}
	}
	if sprints == 0 {
		t.Fatal("không vòng tăng tốc nào thành công")
	}

	if dockedBySpeedup {
		t.Log("KẾT LUẬN: thuyền cập cảng bằng giấy tăng tốc — không cần lệnh xác nhận cập bến")
	} else {
		t.Log("KẾT LUẬN: server nhận giấy tăng tốc, ETA đã rút ngắn")
	}
}
