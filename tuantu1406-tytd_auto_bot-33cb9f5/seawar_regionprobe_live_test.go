//go:build live

package tytd_go

// READ-ONLY region probe (owner 2026-11-10: "fix lỗi thương minh đang không
// thấy tự lên tầng khi đã đủ điều kiện"). Pulls 21003, dumps EVERY region
// row (id, energy, consumeCondition exactly as the server sends it), then
// ENTER_GAME and dumps playerData: regionId, strength, eventCompleteVec —
// so the swRegionUnlocked verdict can be reconstructed offline. Sends NO
// mutating frames beyond enter/enter_game (both idempotent).
//
//	TYTD_LIVE=1 TYTD_SW_PROBE=1 \
//	  go test -tags "sqlcipher live" -count=1 -run TestSeaWarRegionProbeLive -v -timeout 180s .

import (
	"context"
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
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSeaWarRegionProbeLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_SW_PROBE") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_SW_PROBE=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710547
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
	db, _ := gorm.Open(sqlite.Open("file:swprobe?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: false,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256)}
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	var detailMu sync.Mutex
	var detailRaw []byte
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			isMSG, msgType, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.ActList.mergeDetail(p.Username, d)
				p.swMergeConfig(d)
				p.stDeliver(msgType, d)
				detailMu.Lock()
				detailRaw = append([]byte(nil), d...)
				detailMu.Unlock()
			case SERVER_SW_ENTER, SERVER_SW_ENTER_GAME, SERVER_SW_NEXT_REGION,
				SERVER_SW_HEART_BEAT:
				p.swHandleFrame(msgType, d)
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
		t.Fatal("no active type-349 activity")
	}
	t.Logf("live 349 activityId=%d", actID)

	// Fold the config, then dump the RAW region table. region ids are 10001+
	// (floor ids are NOT 1..5) — iterate the map, never a small int range.
	detailMu.Lock()
	raw := detailRaw
	detailMu.Unlock()
	if len(raw) == 0 {
		if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
			if data, err := p.stRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body, 15*time.Second); err == nil {
				p.swMergeConfig(data)
				raw = data
			} else {
				t.Logf("21003 pull failed: %v", err)
			}
		}
	}
	cfg := p.swCfg()
	t.Logf("=== REGION TABLE (%d rows) ===", len(cfg.regionEnergy))
	for id, energy := range cfg.regionEnergy {
		t.Logf("region %d: energy=%d consumeCondition=%q", id, energy, cfg.regionCond[id])
	}
	if len(raw) > 0 {
		var rsp proto.RspGetActivityDetail
		if pb.Unmarshal(raw, &rsp) == nil {
			if dc := rsp.GetActivity().GetDetailConfig(); len(dc.GetUniqueConfig()) > 0 {
				var sws proto.SeaWarConfigSync
				if pb.Unmarshal(dc.GetUniqueConfig(), &sws) == nil {
					t.Logf("=== EVENT CONFIG (%d rows) ===", len(sws.GetSeaWarEventConfigMsgs()))
					for _, ev := range sws.GetSeaWarEventConfigMsgs() {
						t.Logf("eventCfg id=%d eventType=%d param=%q", ev.GetId(), ev.GetEventType(), ev.GetParam())
					}
				}
			}
		}
	}

	// Idempotent enters, then dump the player's own progress vectors.
	if enter, err := p.swEnter(actID); err != nil {
		t.Fatalf("enter: %v", err)
	} else {
		t.Logf("ENTER ret=%d canWorship=%v region=%d", enter.GetRet(), enter.GetCanWorship(), enter.GetRegionId())
	}
	eg, err := p.swEnterGame(actID)
	if err != nil {
		t.Fatalf("enter_game: %v", err)
	}
	pd := eg.GetPlayerData()
	t.Logf("ENTER_GAME ret=%d regionId=%d strength=%d", eg.GetRet(), pd.GetRegionId(), pd.GetStrength())
	t.Logf("=== eventCompleteVec (%d) ===", len(pd.GetEventCompleteVec()))
	for _, c := range pd.GetEventCompleteVec() {
		t.Logf("eventId=%d num=%d", c.GetEventId(), c.GetNum())
	}
	// Verdict reconstruction — exactly what swPlayTick's advance block sees:
	region := pd.GetRegionId()
	eventOK, itemNeed := swRegionConditions(cfg, pd, region)
	t.Logf("verdict(cur=%d): eventOK=%v stoneNeed=%d stoneAllowed(floor3↑)=%v → jump to %d via %s",
		region, eventOK, itemNeed, swRegionStoneOK(cfg, region), region+1,
		map[bool]string{true: "MIỄN PHÍ (đủ quái)", false: "đá"}[eventOK])
}
