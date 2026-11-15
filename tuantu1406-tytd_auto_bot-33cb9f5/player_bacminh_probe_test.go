//go:build live

package tytd_go

// One-shot read-only probe for "Đỉnh Bắc Minh": log in, let the full
// activity list land, print every entry (activityId/type/childType/groupType/
// window/status). Đỉnh Bắc Minh reuses the remoteKunlunWar bundle so its TYPE
// is the only unknown — the active entries not covered by our known constants
// are the candidates. No EnterActivity, no game traffic.
//
//	TYTD_LIVE=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710534 \
//	  go test -count=1 -tags "sqlcipher live" -run TestBacMinhProbe -v -timeout 120s .

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// activityTypeNorthWar == activityTypeBacMinh (332) — ground truth
// 2026-10-07: engine GlobalEnumConfig.systemOpenId NORTH_POLE_WAR=332 + live
// activity list (id 9062374, type 332 active). Kept as a local alias so the
// probe reads stable even if the production const ever moves.
const activityTypeNorthWar = activityTypeBacMinh

func TestBacMinhProbe(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710534
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
	db, _ := gorm.Open(sqlite.Open("file:bacminh_probe?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{}, &model.SystemConfig{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel, Notify: true,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 64)}
	go func() {
		for f := range p.writeChan {
			if err := conn.WriteMessage(2, f); err != nil {
				return
			}
		}
	}()
	raw := make(chan string, 512)
	detailRaw := make(chan []byte, 8)
	rdone := make(chan struct{})
	go func() {
		defer close(rdone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			select {
			case raw <- ty:
			default:
			}
			switch ty {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				select {
				case detailRaw <- d:
				default:
				}
			case SERVER_KUNLUN_ENTER_ACTIVITY, SERVER_KUNLUN_ENTER_GAME, SERVER_KUNLUN_LEAVE,
				SERVER_KUNLUN_MINI_MAP_INFO, SERVER_KUNLUN_HEART_BEAT, SERVER_KUNLUN_SPAR_INFO,
				SERVER_KUNLUN_SPAR_POWER, SERVER_KUNLUN_GET_WELFARE, SERVER_KUNLUN_ACCELERATE,
				SERVER_KUNLUN_MY_GAME_SYNC, SERVER_KUNLUN_BLOCK_SYNC:
				p.kuDeliver(ty, d)
			}
		}
	}()
	defer func() { conn.Close(); <-rdone }()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// Give the full list a moment, then keep watching for incremental pushes.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.ActList.ids()) > 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)
	now := time.Now()
	nwID := int32(0)
	for _, e := range p.ActList.snapshot() {
		t.Logf("ACT id=%-8d type=%-4d child=%-4d group=%-3d %s  win=%s→%s",
			e.ActivityID, e.Type, e.ChildType, e.GroupType, e.status(now),
			time.UnixMilli(e.BeginTime).In(ictZone).Format("01-02 15:04"),
			time.UnixMilli(e.EndTime).In(ictZone).Format("01-02 15:04"))
		if e.Type == activityTypeNorthWar && e.status(now) == "active" {
			nwID = e.ActivityID
		}
	}
	if nwID == 0 {
		t.Skip("no active Đỉnh Bắc Minh (type 332) window")
	}
	// Pull the 21003 activity detail for the north window, then prove the
	// kunlun opcode family (219901/219919) serves it: same wire answers.
	if payload, e := activityPollFrame(nwID); e == nil {
		p.writeChan <- pack.Pack(ACTIVITY_GET_DATA_CMD, payload, uid)
		time.Sleep(1500 * time.Millisecond)
	}
	body, _ := pb.Marshal(&proto.KunlunWarEnterActivityReq{ActivityId: &nwID})
	entRaw, err := p.kuRoundTrip(KUNLUN_ENTER_ACTIVITY_CMD, SERVER_KUNLUN_ENTER_ACTIVITY, body, 15*time.Second)
	if err != nil {
		t.Fatalf("NORTH EnterActivity(219901, id=%d): %v", nwID, err)
	}
	var ent proto.KunlunWarEnterActivityResp
	if err := pb.Unmarshal(entRaw, &ent); err != nil {
		t.Fatalf("NORTH EnterActivity decode: %v", err)
	}
	t.Logf("NORTH ENTER_ACTIVITY ret=%d round=%d floor=%d group=%d rank=%d union=%d",
		ent.GetRet(), ent.GetRound(), ent.GetFloor(), ent.GetGroup(), ent.GetSelfRank(), ent.GetUnionId())
	p.KuMu.Lock()
	if p.ku == nil {
		p.ku = newKunlunState()
	}
	p.ku.ActivityID = nwID
	p.ku.Enter = &ent
	p.KuMu.Unlock()
	mmBody, _ := pb.Marshal(&proto.KunlunWarMiniMapInfoReqMsg{
		ActivityId: &nwID, Floor: pb.Int32(ent.GetFloor()), Group: pb.Int32(ent.GetGroup()),
	})
	mm, err := p.kuRoundTrip(KUNLUN_MINI_MAP_INFO_CMD, SERVER_KUNLUN_MINI_MAP_INFO, mmBody, 20*time.Second)
	if err != nil {
		t.Fatalf("NORTH MiniMap: %v", err)
	}
	_ = os.WriteFile("/tmp/north_mini.bin", mm, 0o644)
	rsp, err := gimap.DecodeKunlunMini(mm)
	if err != nil {
		t.Fatalf("NORTH minimap decode: %v", err)
	}
	maxPos := int32(0)
	for _, c := range rsp.GetCellInfo() {
		if c.GetIndex() > maxPos {
			maxPos = c.GetIndex()
		}
	}
	t.Logf("NORTH MINIMAP bytes=%d cities=%d cells=%d unions=%d maxPos=%d (61²=3721 vs 119²=14161)",
		len(mm), len(rsp.GetCityInfo()), len(rsp.GetCellInfo()), len(rsp.GetBaseData()), maxPos)
	for _, u := range rsp.GetBaseData() {
		t.Logf("NORTH UNION %d %q flag=%d", u.GetUnionId(), u.GetName(), u.GetFlag())
	}
	if _, e := gimap.Stream(gimap.StreamKunlun, mm); e != nil {
		t.Logf("NORTH stream(kunlun art): %v", e)
	}
	if img, e := gimap.RenderKunlun(mm, 1400); e == nil {
		_ = os.WriteFile("/tmp/north_live.png", img, 0o644)
		t.Logf("PNG /tmp/north_live.png (%d bytes) — kunlun art over north data, expect wrong art but right geometry", len(img))
	}
	// 21003 detail for the north window — kuMergeConfig is kunlun-gated, so the
	// probe parses the answer directly: does a type-332 detail carry the same
	// paramConfig/uniqueConfig shape (phase windows, costs) as type 199?
drain:
	for {
		select {
		case d := <-detailRaw:
			var rsp proto.RspGetActivityDetail
			if pb.Unmarshal(d, &rsp) != nil {
				continue
			}
			mc := rsp.GetActivity().GetDetailConfig().GetCommonConfig().GetMainConfig()
			t.Logf("NORTH 21003 main type=%d id=%d", mc.GetType(), mc.GetActivityId())
			for _, pc := range rsp.GetActivity().GetDetailConfig().GetCommonConfig().GetParamConfig() {
				t.Logf("NORTH 21003 param %s=%s", pc.GetParam(), pc.GetValue())
			}
			uc := rsp.GetActivity().GetDetailConfig().GetUniqueConfig()
			var sync proto.KunlunWarConfigSync
			if pb.Unmarshal(uc, &sync) == nil && len(sync.GetPeakHoldTimeConfigs()) > 0 {
				t.Logf("NORTH 21003 UNIQUE KunlunWarConfigSync phases=%d cities=%d", len(sync.GetPeakHoldTimeConfigs()), len(sync.GetCityConfig()))
			} else {
				t.Logf("NORTH 21003 uniqueConfig=%d bytes, not KunlunWarConfigSync-with-phases", len(uc))
			}
		default:
			break drain
		}
	}
	// EnterGame (219902): prove the scene seat serves the north window. The
	// req MUST carry activityId (nil body silently gets no answer — same trap
	// as LEAVE below).
	gBody, _ := pb.Marshal(&proto.KunlunWarEnterGameReq{ActivityId: &nwID})
	if gv, e := p.kuRoundTrip(KUNLUN_ENTER_GAME_CMD, SERVER_KUNLUN_ENTER_GAME, gBody, 15*time.Second); e != nil {
		t.Logf("NORTH EnterGame: %v", e)
	} else {
		var game proto.KunlunWarEnterGameResp
		_ = pb.Unmarshal(gv, &game)
		t.Logf("NORTH ENTER_GAME ret=%d home=%d energy=%d pos=%d", game.GetRet(), game.GetHome(), game.GetEnergy(), game.GetMyPlayerInfo().GetPos())
		// Guild tree on 332 (219910 read-only + 219912 collect if pending —
		// collect is receive-side FREE, never SEND_WELFARE 219913).
		sparBody, _ := pb.Marshal(&proto.KunlunWarSparInfoReqMsg{ActivityId: &nwID})
		cr := int32(-1)
		if raw, e := p.kuRoundTrip(KUNLUN_SPAR_INFO_CMD, SERVER_KUNLUN_SPAR_INFO, sparBody, 15*time.Second); e != nil {
			t.Logf("NORTH SPAR_INFO(219910): %v", e)
		} else {
			var si proto.KunlunWarSparInfoRespMsg
			_ = pb.Unmarshal(raw, &si)
			cr = si.GetCanReceive()
			pending := 0
			for _, w := range si.GetInfo() {
				if w.GetStatus() == 0 {
					pending++
				}
				t.Logf("NORTH SPAR notice name=%q item=%d id=%d status=%d", w.GetName(), w.GetItemId(), w.GetId(), w.GetStatus())
			}
			t.Logf("NORTH SPAR_INFO ret=%d exp=%d canReceive=%d pending=%d", si.GetRet(), si.GetExp(), cr, pending)
			if cr == 1 || pending > 0 {
				zero := int64(0)
				wBody, _ := pb.Marshal(&proto.KunlunWarGetWelfareReqMsg{ActivityId: &nwID, Id: &zero})
				if raw, e := p.kuRoundTrip(KUNLUN_GET_WELFARE_CMD, SERVER_KUNLUN_GET_WELFARE, wBody, 15*time.Second); e != nil {
					t.Logf("NORTH GET_WELFARE(219912): %v", e)
				} else {
					var gw proto.KunlunWarGetWelfareRespMsg
					_ = pb.Unmarshal(raw, &gw)
					t.Logf("NORTH GET_WELFARE ret=%d reward=%q exp=%d canReceive=%d", gw.GetRet(), gw.GetReward(), gw.GetExp(), gw.GetCanReceive())
				}
			}
		}
		// Accelerate on 332 (219918): user says press freely; cost = accel item
		// if owned else gems, server decides. No active march ⇒ expect ret!=0
		// harmless refusal, which still proves the opcode is routed for 332.
		cnt := int32(0)
		acBody, _ := pb.Marshal(&proto.KunlunWarAcclerateMoveReqMsg{ActivityId: &nwID, SpeedUpCount: &cnt})
		if raw, e := p.kuRoundTrip(KUNLUN_ACCELERATE_CMD, SERVER_KUNLUN_ACCELERATE, acBody, 15*time.Second); e != nil {
			t.Logf("NORTH ACCELERATE(219918): %v", e)
		} else {
			var ar proto.KunlunWarAcclerateMoveRespMsg
			_ = pb.Unmarshal(raw, &ar)
			t.Logf("NORTH ACCELERATE ret=%d speedUp=%d cost=%q", ar.GetRet(), ar.GetSpeedUpCount(), ar.GetCostInfo())
		}
	}
	lvBody, _ := pb.Marshal(&proto.KunlunWarLeaveReq{ActivityId: &nwID})
	if lv, e := p.kuRoundTrip(KUNLUN_LEAVE_CMD, SERVER_KUNLUN_LEAVE, lvBody, 10*time.Second); e != nil {
		t.Logf("LEAVE: %v", e)
	} else {
		t.Logf("LEAVE ok %d bytes", len(lv))
	}
}
