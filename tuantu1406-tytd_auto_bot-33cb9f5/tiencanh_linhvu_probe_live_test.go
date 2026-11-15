//go:build live

package tytd_go

// READ-ONLY discovery probe for the owner request 2026-11-13 "thêm auto phát
// đá linh vũ + nhận nhanh, có 3 loại đá linh vũ": dump EVERY type-186
// paramConfig row (param name + value), the folded donate ids, the full
// bonus-board board (notice Name/itemId/status) and mall row names — the
// "đá linh vũ" trio should surface there (ITEM_DONATE "105192|5;105193|10;
// 105194|20" is exactly 3 ids). No state-changing op is sent.
//
//	TYTD_LIVE=1 go test -tags "sqlcipher live" -count=1 -run TestTienCanhLinhVuProbe -v -timeout 240s .

import (
	"context"
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

func TestTienCanhLinhVuProbe(t *testing.T) {
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
	db, _ := gorm.Open(sqlite.Open("file:tcv?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256)}
	go func() {
		for f := range p.writeChan {
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
			case SERVER_WE_YOUND_BONUS, SERVER_WE_YOUND_ENTER_ACTIVITY,
				SERVER_WE_YOUND_SEND_BONUS_ITEM, SERVER_WE_YOUND_GET_BONUS_RESULT,
				SERVER_WE_YOUND_GET_BONUS_ITEM:
				p.stDeliver(msgType, realData)
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

	body, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID})
	data, err := p.wyRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body)
	if err != nil {
		t.Fatal(err)
	}
	p.wyMergeConfig(data)
	var rsp proto.RspGetActivityDetail
	_ = pb.Unmarshal(data, &rsp)
	dc := rsp.GetActivity().GetDetailConfig()
	for _, pc := range dc.GetCommonConfig().GetParamConfig() {
		t.Logf("PARAM %s = %s", pc.GetParam(), pc.GetValue())
	}
	for _, mc := range dc.GetCommonConfig().GetMallConfig() {
		if mt := mc.GetMallTempMsg(); mt != nil {
			t.Logf("MALL id=%d name=%q price=%q param=%q buyLimit=%d", mt.GetId(), mt.GetName(), mt.GetPrice(), mt.GetParam(), mt.GetBuyLimit())
		}
	}
	p.WyMu.Lock()
	t.Logf("folded liquidIDs=%v energyItem=%d", p.wyLiquidIDs, p.wyEnergyItem)
	p.WyMu.Unlock()

	info, err := p.wyBonusInfo(actID)
	if err != nil {
		t.Logf("218620 err=%v", err)
	} else {
		t.Logf("BONUS ret=%d exp=%d canReceive=%d notices=%d", info.GetRet(), info.GetExp(), info.GetCanReceive(), len(info.GetInfo()))
		for i, w := range info.GetInfo() {
			t.Logf("  notice[%d]: name=%q itemId=%d id=%d status=%d time=%d", i, w.GetName(), w.GetItemId(), w.GetId(), w.GetStatus(), w.GetTime())
		}
	}

	// ── OWNER-AUTHORIZED 2026-11-13: phát cả 3 loại đá linh vũ (dùng thoải
	// mái) + Nhận nhanh ── this calls the real production tree cycle.
	p.wyWaterTree(actID)
	info2, err := p.wyBonusInfo(actID)
	if err == nil {
		ready := 0
		for _, w := range info2.GetInfo() {
			if w.GetStatus() == 0 && w.GetId() != 0 {
				ready++
			}
		}
		t.Logf("AFTER water-tree: notices=%d ready=%d canReceive=%d", len(info2.GetInfo()), ready, info2.GetCanReceive())
	}
}
