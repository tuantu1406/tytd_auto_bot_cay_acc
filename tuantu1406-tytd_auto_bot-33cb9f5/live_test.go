//go:build live

package tytd_go

import (
	"fmt"
	"os"
	"testing"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	pb "google.golang.org/protobuf/proto"

	"github.com/gorilla/websocket"
)

// TestLiveGodIsland exercises the full live stack against the real server
// with the leader's own test account: gPassport/funtap login → ws auth →
// activity-list push (giHandleActivityList) → base info + game info poll
// (giHandleBaseInfo/giHandleGameInfo) → ball info dump → one heal draw →
// minimap round-trip rendered by gimap. The heal draw consumes ball power on
// the leader's own throwaway test account; everything else is read-only —
// it never sends move/attack/fruit/speed-up orders.
//
// Gated behind TYTD_LIVE=1 so normal `go test` stays offline:
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveGodIsland -v -timeout 180s .
func TestLiveGodIsland(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveUser   = liveUserEnv(t)
		livePass   = livePassEnv(t)
		liveServer = 710564
	)

	// --- login chain (real request package) ---
	passportToken, err := request.LoginGPassport(liveUser, livePass)
	if err != nil {
		t.Fatalf("LoginGPassport: %v", err)
	}
	t.Log("passport ok")
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatalf("LoginFuntap: %v", err)
	}
	t.Logf("funtap ok, userID=%s", userID)
	gameURL, err := request.GetServerURL(userID, liveServer)
	if err != nil {
		t.Fatalf("GetServerURL: %v", err)
	}
	t.Logf("server url ok: %s", gameURL)
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
	if err != nil {
		t.Fatalf("GetWSKey: %v", err)
	}
	t.Logf("ws key ok, uid=%d", uid)

	// --- websocket + auth (same frames as EventPlayer.Run) ---
	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// gorilla allows one writer at a time: funnel every frame through this.
	wrCh := make(chan []byte, 16)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for frame := range wrCh {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	defer func() { close(wrCh); <-writerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	wrCh <- pack.Pack(AUTH_CMD, authMSG, uid)

	p := &EventPlayer{Username: liveUser, UID: uid, Running: true}
	// This test only drives the normal (Bồng Lai) variant: it sends its own
	// INFO_LOAD/BALL_INFO frames outside giSend, so the FIFO queue stays
	// empty and we attribute those responses to this slot directly.
	ev := p.giSlot(GIEventNormal)

	infoCH := make(chan []byte, 4)
	ballCH := make(chan []byte, 1)
	useCH := make(chan []byte, 1)
	mapCH := make(chan []byte, 1)
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
				p.giHandleActivityList(realData)
			case SERVER_GOD_ISLAND_BASE_INFO:
				p.giHandleBaseInfo(realData)
			case SERVER_GOD_ISLAND_INFO_LOAD:
				p.giHandleGameInfo(ev, realData)
				select {
				case infoCH <- realData:
				default:
				}
			case SERVER_GOD_ISLAND_BALL_INFO:
				p.giHandleBallInfo(ev, realData)
				select {
				case ballCH <- realData:
				default:
				}
			case SERVER_GOD_ISLAND_USE_BALL:
				select {
				case useCH <- realData:
				default:
				}
			case SERVER_GOD_ISLAND_MINIMAP:
				select {
				case mapCH <- realData:
				default:
				}
			}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()

	// keepalive while we wait
	pingQuit := make(chan struct{})
	go func() {
		for {
			select {
			case <-pingQuit:
				return
			case <-time.After(5 * time.Second):
			}
			select {
			case wrCh <- pack.Pack(PING_CMD, []byte{}, uid):
			default:
			}
		}
	}()
	defer close(pingQuit)

	// --- wait for the activity push to reveal the God Island activity ---
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if activityIDOf(p) != 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	activityID := activityIDOf(p)
	if activityID == 0 {
		t.Skip("login + push ok, but Đảo Bồng Lai is not open on this server right now")
	}
	t.Logf("God Island activity id = %d", activityID)
	if peakID := activityIDOfPeak(p); peakID != 0 {
		t.Logf("Thiên Ma (peak) activity id = %d — both events open at once", peakID)
	}

	// --- base info (group/floor/union) ---
	baseMSG, _ := pb.Marshal(&proto.GodIslandBaseInfoReqMsg{ActivityId: activityID})
	wrCh <- pack.Pack(GOD_ISLAND_BASE_INFO_CMD, baseMSG, uid)
	time.Sleep(3 * time.Second)
	p.GodIslandMu.Lock()
	t.Logf("base info: group=%d floor=%d union=%d", ev.Group, ev.Floor, ev.Union)
	p.GodIslandMu.Unlock()

	// --- game info poll (feeds /bonglai_tangtoc's speedUpCount cache) ---
	infoMSG, _ := pb.Marshal(&proto.GodIslandGameInfoReqMsg{ActivityId: activityID})
	wrCh <- pack.Pack(GOD_ISLAND_INFO_LOAD_CMD, infoMSG, uid)
	select {
	case data := <-infoCH:
		var resp proto.GodIslandGameInfoRespMsg
		if err := pb.Unmarshal(data, &resp); err != nil {
			t.Fatalf("game info decode: %v", err)
		}
		t.Logf("game info: ret=%d speedUpCount=%v (cached=%d) curPower=%v maxPower=%v location=%v leftout=%v reviveTime=%v",
			resp.GetRet(), resp.GetSpeedUpCount(), speedUpOf(p),
			resp.GetCurPower(), resp.GetMaxPower(),
			resp.GetLocation(), resp.GetLeftout(), resp.GetReviveTime())
	case <-time.After(15 * time.Second):
		t.Fatal("server did not answer INFO_LOAD")
	}

	// --- ball info: dump the raw power strings to settle which side of the
	// ball (cur vs max-cur) is the drinkable amount when it reads "full". ---
	ballReqMSG, _ := pb.Marshal(&proto.GodIslandGameSpiritualBallInfoReqMsg{ActivityId: activityID})
	wrCh <- pack.Pack(GOD_ISLAND_BALL_INFO_CMD, ballReqMSG, uid)
	select {
	case data := <-ballCH:
		var resp proto.GodIslandGameSpiritualBallInfoRespMsg
		if err := pb.Unmarshal(data, &resp); err != nil {
			t.Fatalf("ball info decode: %v", err)
		}
		t.Logf("ball info: ret=%d curPower=%v maxPower=%v ballCurPower=%v ballMaxPower=%v",
			resp.GetRet(), resp.GetCurPower(), resp.GetMaxPower(),
			resp.GetBallCurPower(), resp.GetBallMaxPower())
	case <-time.After(15 * time.Second):
		t.Fatal("server did not answer BALL_INFO")
	}

	// --- heal probe through the real /bonglai_hoimau code path
	// (state-changing, run only on this throwaway test account). Answers:
	// does the server accept a draw while the unit is dead (location==0),
	// what ret does it answer with, and does anything about the dead state
	// (location/curPower/reviveTime) change afterwards? ---
	p.writeChan = wrCh
	healErr := p.GodIslandHealPower(GIEventNormal)
	t.Logf("GodIslandHealPower: %v", healErr)
	if healErr == nil {
		select {
		case data := <-useCH:
			var resp proto.GodIslandGameUseSpiritualBallRespMsg
			if err := pb.Unmarshal(data, &resp); err != nil {
				t.Fatalf("use ball decode: %v", err)
			}
			t.Logf("use ball resp: ret=%d curPower=%v ballCurPower=%v",
				resp.GetRet(), resp.GetCurPower(), resp.GetBallCurPower())
		case <-time.After(15 * time.Second):
			t.Fatal("server did not answer USE_BALL")
		}
	}

	// --- post-heal state: did location/curPower/reviveTime move? ---
	infoMSG2, _ := pb.Marshal(&proto.GodIslandGameInfoReqMsg{ActivityId: activityID})
	wrCh <- pack.Pack(GOD_ISLAND_INFO_LOAD_CMD, infoMSG2, uid)
	select {
	case data := <-infoCH:
		var resp proto.GodIslandGameInfoRespMsg
		if err := pb.Unmarshal(data, &resp); err != nil {
			t.Fatalf("post-heal game info decode: %v", err)
		}
		t.Logf("post-heal info: curPower=%v maxPower=%v location=%v leftout=%v reviveTime=%v",
			resp.GetCurPower(), resp.GetMaxPower(),
			resp.GetLocation(), resp.GetLeftout(), resp.GetReviveTime())
	case <-time.After(15 * time.Second):
		t.Fatal("server did not answer post-heal INFO_LOAD")
	}

	// --- minimap round-trip + PNG render (/bonglai_map data path) ---
	mapMSG, _ := pb.Marshal(&proto.GodIslandGameMiniMapInfoReqMsg{ActivityId: activityID})
	wrCh <- pack.Pack(GOD_ISLAND_MINIMAP_CMD, mapMSG, uid)
	var mapPayload []byte
	select {
	case mapPayload = <-mapCH:
	case <-time.After(20 * time.Second):
		t.Fatal("server did not answer MINIMAP")
	}
	var mm proto.GodIslandGameMiniMapInfoRespMsg
	if err := pb.Unmarshal(mapPayload, &mm); err != nil {
		t.Fatalf("minimap decode: %v", err)
	}
	if mm.GetRet() != 0 {
		// 6816: the server only answers the bracket minimap for residents;
		// INFO_LOAD above (ret=6842) shows we're not in the island right now.
		t.Skipf("minimap rejected (ret=%d) — account not resident on the island right now", mm.GetRet())
	}
	t.Logf("minimap: ret=%d cities=%d", mm.GetRet(), len(mm.GetCityInfo()))

	img, err := gimap.Render(mapPayload, 1200)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	outPath := fmt.Sprintf("/tmp/bonglai_map_%d_%d.png", uid, time.Now().Unix())
	if err := os.WriteFile(outPath, img, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	t.Logf("rendered %s (%d bytes PNG)", outPath, len(img))
}

// activityIDOf / activityIDOfPeak / speedUpOf read one event slot's
// EventPlayer state under its lock.
func activityIDOf(p *EventPlayer) int32 {
	p.GodIslandMu.Lock()
	defer p.GodIslandMu.Unlock()
	if ev := p.GiEvents[GIEventNormal]; ev != nil {
		return ev.ActivityID
	}
	return 0
}

func activityIDOfPeak(p *EventPlayer) int32 {
	p.GodIslandMu.Lock()
	defer p.GodIslandMu.Unlock()
	if ev := p.GiEvents[GIEventPeak]; ev != nil {
		return ev.ActivityID
	}
	return 0
}

func speedUpOf(p *EventPlayer) int32 {
	p.GodIslandMu.Lock()
	defer p.GodIslandMu.Unlock()
	if ev := p.GiEvents[GIEventNormal]; ev != nil {
		return ev.SpeedUp
	}
	return 0
}

// TestLiveYard runs the real autoYard decision path against the server on
// the leader's throwaway test account: login → auth → one YardEnterReq
// snapshot (dumped in full), then the production yardHandleEnter on the same
// bytes — so whatever collect/make orders the bot WOULD send go out for real
// and their rets come back logged. A started Wish Pool batch simply keeps
// running on this test account afterwards, which is the intended steady
// state. Read-mostly: at most one GAIN_REWARD per producing instance (a
// no-op when nothing is ripe) and at most one BUILD_MAKE per idle pool.
//
// Gated behind TYTD_LIVE=1 like TestLiveGodIsland:
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveYard -v -timeout 180s .
func TestLiveYard(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	// 710564 answers YARD_ENTER with an empty stub area (yard feature not
	// enabled there); the yard runs on 710547 for this account.
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

	wrCh := make(chan []byte, 32)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for frame := range wrCh {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	defer func() { close(wrCh); <-writerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	wrCh <- pack.Pack(AUTH_CMD, authMSG, uid)

	p := &Player{Username: liveUser, UID: uid}
	p.writeChan = wrCh // yardHandleEnter's orders go straight onto the socket

	enterCH := make(chan []byte, 4)
	respN := make(chan string, 16)
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
			case SERVER_YARD_ENTER:
				select {
				case enterCH <- realData:
				default:
				}
			case SERVER_YARD_MAKE:
				var r proto.YardBuildMakeResp
				if err := pb.Unmarshal(realData, &r); err == nil {
					respN <- fmt.Sprintf("make ret=%d rewards=%q", r.GetRet(), r.GetRewards())
				}
			case SERVER_YARD_GAIN_REWARD:
				var r proto.YardBuildGainRewardResp
				if err := pb.Unmarshal(realData, &r); err == nil {
					respN <- fmt.Sprintf("gain ret=%d rewards=%q extra=%q inspire=%q",
						r.GetRet(), r.GetRewards(), r.GetExtraRewards(), r.GetInspireReward())
				}
			}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()

	pingQuit := make(chan struct{})
	go func() {
		for {
			select {
			case <-pingQuit:
				return
			case <-time.After(5 * time.Second):
			}
			select {
			case wrCh <- pack.Pack(PING_CMD, []byte{}, uid):
			default:
			}
		}
	}()
	defer close(pingQuit)

	// --- snapshot 1: dump the raw yard, then let the production handler
	// decide and act on exactly these bytes ---
	time.Sleep(3 * time.Second) // let auth settle
	enterMSG, _ := pb.Marshal(&proto.YardEnterReq{TargetPlayerId: pb.Int64(int64(uid))})
	wrCh <- pack.Pack(YARD_ENTER_CMD, enterMSG, uid)
	var snap []byte
	select {
	case snap = <-enterCH:
	case <-time.After(20 * time.Second):
		t.Fatal("server did not answer YARD_ENTER")
	}
	var resp proto.YardEnterResp
	if err := pb.Unmarshal(snap, &resp); err != nil {
		t.Fatalf("enter resp decode: %v", err)
	}
	if len(snap) < 512 {
		t.Logf("raw enter payload (%d bytes): %x", len(snap), snap)
	} else {
		t.Logf("enter payload %d bytes (first 512): %x …", len(snap), snap[:512])
	}
	if resp.GetRet() != 0 {
		t.Skipf("yard enter ret=%d — yard not available for this account", resp.GetRet())
	}
	now := time.Now().UnixMilli()
	for _, area := range resp.GetAreaInfo() {
		for _, b := range area.GetBuildingMsg() {
			cell, d := b.GetYardCellMsg(), b.GetYardBuildDetailMsg()
			if cell == nil || d == nil {
				continue
			}
			ripe := ""
			if d.GetEndTime() > 0 && now >= d.GetEndTime() {
				ripe = " RIPE"
			}
			t.Logf("area=%d uid=%d buildId=%d lv=%d status=%d product=%d total=%d start=%d end=%d%s",
				area.GetAreaId(), cell.GetUniqueId(), cell.GetBuildId(), d.GetLevel(),
				d.GetStatus(), d.GetProductId(), d.GetTotalNum(), d.GetStartTime(), d.GetEndTime(), ripe)
		}
	}

	p.yardHandleEnter(snap)

	// --- collect every make/gain ret the server sends back for a few seconds ---
	deadline := time.After(8 * time.Second)
	n := 0
	for done := false; !done; {
		select {
		case s := <-respN:
			n++
			t.Logf("server answered: %s", s)
		case <-deadline:
			done = true
		}
	}
	t.Logf("%d make/gain responses", n)

	// --- snapshot 2: did the state move (idle pool now making)? ---
	enterMSG2, _ := pb.Marshal(&proto.YardEnterReq{TargetPlayerId: pb.Int64(int64(uid))})
	wrCh <- pack.Pack(YARD_ENTER_CMD, enterMSG2, uid)
	select {
	case snap2 := <-enterCH:
		var resp2 proto.YardEnterResp
		if err := pb.Unmarshal(snap2, &resp2); err == nil {
			for _, area := range resp2.GetAreaInfo() {
				for _, b := range area.GetBuildingMsg() {
					cell, d := b.GetYardCellMsg(), b.GetYardBuildDetailMsg()
					if cell == nil || d == nil || d.GetStatus() != 1 {
						continue
					}
					t.Logf("now making: uid=%d buildId=%d lv=%d product=%d total=%d start=%d",
						cell.GetUniqueId(), cell.GetBuildId(), d.GetLevel(), d.GetProductId(),
						d.GetTotalNum(), d.GetStartTime())
				}
			}
		}
	case <-time.After(20 * time.Second):
		t.Fatal("server did not answer second YARD_ENTER")
	}
}

// TestLiveUnionBounty probes the union bounty (yêu minh huyền thưởng) chain
// with the same throwaway account: OPEN the event, dump the authoritative
// bountyInfo/playerData, then let unionBountyHandleOpen decide and act on
// exactly those bytes (claim / probe-then-start / skip monster). A nonzero
// OPEN ret (e.g. account not in a union) is reported but not a test failure:
// it proves the command routing works.
func TestLiveUnionBounty(t *testing.T) {
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

	// Writer funnel: snapshot what the handlers put on the wire, then forward.
	wrCh := make(chan []byte, 32)
	sentCH := make(chan string, 16)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for frame := range wrCh {
			switch frameFuncType(t, frame) {
			case UNION_BOUNTY_OPEN_EVENT_CMD:
				sentCH <- "OPEN"
			case UNION_BOUNTY_DEAL_CMD:
				sentCH <- fmt.Sprintf("DEAL %x", frame[18:])
			case UNION_BOUNTY_GET_REWARD_CMD:
				sentCH <- "GET_REWARD"
			case UNION_BOUNTY_HAVE_ESCORT_CMD:
				sentCH <- "HAVE_ESCORT"
			}
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	defer func() { close(wrCh); <-writerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	wrCh <- pack.Pack(AUTH_CMD, authMSG, uid)

	p := &Player{Username: liveUser, UID: uid}
	p.writeChan = wrCh // the bounty handlers' orders go straight onto the socket

	openCH := make(chan []byte, 4)
	monsterCH := make(chan []byte, 4)
	respN := make(chan string, 16)
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
			case SERVER_UNION_BOUNTY_OPEN_EVENT:
				select {
				case openCH <- realData:
				default:
				}
			case SERVER_UNION_BOUNTY_OPEN_MONSTER:
				select {
				case monsterCH <- realData:
				default:
				}
			case SERVER_UNION_BOUNTY_ATTACK_MONSTER:
				var r proto.UnionBountyAttackMonsterResp
				if err := pb.Unmarshal(realData, &r); err == nil {
					respN <- fmt.Sprintf("attack ret=%d reward=%q", r.GetRet(), r.GetReward())
				}
			case SERVER_UNION_BOUNTY_HAVE_ESCORT:
				var r proto.UnionBountyHaveEscortResp
				if err := pb.Unmarshal(realData, &r); err == nil {
					respN <- fmt.Sprintf("have-escort ret=%d exist=%v", r.GetRet(), r.GetExist())
				}
			case SERVER_UNION_BOUNTY_DEAL:
				var r proto.UnionBountyDealBountyResp
				if err := pb.Unmarshal(realData, &r); err == nil {
					respN <- fmt.Sprintf("deal ret=%d myCartCfg=%d", r.GetRet(), r.GetMyCart().GetConfigId())
				}
			case SERVER_UNION_BOUNTY_GET_REWARD:
				var r proto.UnionBountyGetRewardEscortResp
				if err := pb.Unmarshal(realData, &r); err == nil {
					respN <- fmt.Sprintf("get-reward ret=%d cfg=%d reward=%q",
						r.GetRet(), r.GetConfigId(), r.GetReward())
				}
			}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()

	pingQuit := make(chan struct{})
	go func() {
		for {
			select {
			case <-pingQuit:
				return
			case <-time.After(5 * time.Second):
			}
			select {
			case wrCh <- pack.Pack(PING_CMD, []byte{}, uid):
			default:
			}
		}
	}()
	defer close(pingQuit)

	time.Sleep(3 * time.Second) // let auth settle
	wrCh <- pack.Pack(UNION_BOUNTY_OPEN_EVENT_CMD, []byte{}, uid)
	var raw []byte
	select {
	case raw = <-openCH:
	case <-time.After(20 * time.Second):
		t.Fatal("server did not answer OPEN_BOUNTY_EVENT")
	}
	var resp proto.UnionBountyOpenBountyEventResp
	if err := pb.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("open resp decode: %v (raw %x)", err, raw)
	}
	info := resp.GetBountyInfo()
	t.Logf("raw=%x", raw)
	t.Logf("OPEN ret=%d curConfigId=%d finishRewardCfg=%d bountyTimes=%d/%d isNewEvent=%v",
		resp.GetRet(), info.GetCurConfigId(), info.GetFinishReward().GetEscortCartConfigId(),
		resp.GetPlayerData().GetBountyTimes(), unionBountyDailyTimes, resp.GetIsNewEvent())
	kind := "khác"
	switch {
	case unionBountyBossIDs[info.GetCurConfigId()]:
		kind = "yêu quái (BountyBossDB)"
	case unionBountyEventIDs[info.GetCurConfigId()]:
		kind = "áp tải (BountyEventDB)"
	}
	t.Logf("phân loại curConfigId=%d → %s", info.GetCurConfigId(), kind)

	// Let the real handler decide and act on exactly these bytes, then watch
	// one full exchange (probe → deal / claim) land on the wire.
	p.unionBountyHandleOpen(raw)
	for done := false; !done; {
		select {
		case s := <-sentCH:
			t.Logf("bot gửi: %s", s)
		case s := <-respN:
			t.Logf("server: %s", s)
		case <-time.After(8 * time.Second):
			done = true
		}
	}
	// Drain anything the chained OPEN (after a real claim) answered.
	for n := 0; n < 2; n++ {
		select {
		case raw2 := <-openCH:
			var r2 proto.UnionBountyOpenBountyEventResp
			if err := pb.Unmarshal(raw2, &r2); err == nil {
				i2 := r2.GetBountyInfo()
				t.Logf("OPEN lần 2: ret=%d curConfigId=%d finishRewardCfg=%d bountyTimes=%d",
					r2.GetRet(), i2.GetCurConfigId(), i2.GetFinishReward().GetEscortCartConfigId(),
					r2.GetPlayerData().GetBountyTimes())
			}
			p.unionBountyHandleOpen(raw2)
		case s := <-sentCH:
			t.Logf("bot gửi: %s", s)
		case s := <-respN:
			t.Logf("server: %s", s)
		case <-time.After(5 * time.Second):
			n = 2
		}
	}

	// Validate the monster-room command routing and response decoding
	// independently of today's event type. With an escort event on the board
	// a ret!=0 answer is expected — it still proves the round-trip works.
	probe, _ := pb.Marshal(&proto.UnionBountyOpenMonsterReq{PlayerId: pb.Int64(int64(uid))})
	wrCh <- pack.Pack(UNION_BOUNTY_OPEN_MONSTER_CMD, probe, uid)
	select {
	case raw3 := <-monsterCH:
		var r3 proto.UnionBountyOpenMonsterResp
		if err := pb.Unmarshal(raw3, &r3); err != nil {
			t.Fatalf("open-monster resp decode: %v (raw %x)", err, raw3)
		}
		t.Logf("OPEN_MONSTER ret=%d event=%d teamSlots=%d power=%q",
			r3.GetRet(), r3.GetEvent(), len(r3.GetBattleMainList()), r3.GetMonsterPower())
	case <-time.After(15 * time.Second):
		t.Fatal("server did not answer OPEN_MONSTER")
	}
}

// TestLiveUnionTask proves the task pushes (501/502), the claimable filter,
// and the batch claim (503) against the live server. Login alone triggers
// the 501 snapshot; the 25 s first delay of autoUnionTask matches the manual
// claim timing here.
func TestLiveUnionTask(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	liveUser, livePass := liveUserEnv(t), livePassEnv(t)
	if u := os.Getenv("TYTD_LIVE_USER"); u != "" {
		liveUser = u
	}
	if pw := os.Getenv("TYTD_LIVE_PASS"); pw != "" {
		livePass = pw
	}
	liveServer := 710547
	if s := os.Getenv("TYTD_LIVE_SERVER"); s != "" {
		if _, err := fmt.Sscanf(s, "%d", &liveServer); err != nil {
			t.Fatalf("bad TYTD_LIVE_SERVER %q: %v", s, err)
		}
	}
	t.Logf("login %s @ server %d", liveUser, liveServer)

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

	wrCh := make(chan []byte, 32)
	sentCH := make(chan string, 16)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for frame := range wrCh {
			if frameFuncType(t, frame) == TASK_GET_REWARD_CMD {
				sentCH <- fmt.Sprintf("CLAIM %x", frame[18:])
			}
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	defer func() { close(wrCh); <-writerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	wrCh <- pack.Pack(AUTH_CMD, authMSG, uid)

	p := &Player{Username: liveUser, UID: uid}
	p.writeChan = wrCh

	fullCH := make(chan []byte, 1)
	syncCH := make(chan []byte, 16)
	respN := make(chan string, 16)
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
			case SERVER_TASK_DATA_SEND:
				select {
				case fullCH <- realData:
				default:
				}
			case SERVER_TASK_DATA_SYNC:
				select {
				case syncCH <- realData:
				default:
				}
			case SERVER_TASK_GET_REWARD:
				var r proto.TaskGetRewardRespMsg
				if err := pb.Unmarshal(realData, &r); err == nil {
					respN <- fmt.Sprintf("claim ret=%d rewards=%q", r.GetRet(), r.GetRewards())
				}
			}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()

	pingQuit := make(chan struct{})
	go func() {
		for {
			select {
			case <-pingQuit:
				return
			case <-time.After(5 * time.Second):
				if err := conn.WriteMessage(2, pack.Pack(PING_CMD, nil, uid)); err != nil {
					return
				}
			}
		}
	}()
	defer close(pingQuit)

	var raw []byte
	select {
	case raw = <-fullCH:
	case <-time.After(20 * time.Second):
		t.Fatal("no task snapshot (501) within 20s")
	}
	p.taskHandleList(raw, true)
	var snap proto.TaskDataListMsg
	if err := pb.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("bad 501: %v", err)
	}
	t.Logf("snapshot: %d tasks", len(snap.GetDataList()))
	for _, td := range snap.GetDataList() {
		if _, ok := unionTaskTargets[td.GetTaskId()]; ok {
			t.Logf("union task %d: value=%s state=%d", td.GetTaskId(), td.GetValue(), td.GetState())
		}
	}

	// Drain sync patches like the first 25 s of a session would.
	deadline := time.After(5 * time.Second)
drain:
	for {
		select {
		case patch := <-syncCH:
			p.taskHandleList(patch, false)
		case <-deadline:
			break drain
		}
	}

	probeMode := false
	ids := p.unionTaskClaimable()
	if len(ids) == 0 {
		// Nothing to claim: probe the response path anyway by re-claiming
		// one already-claimed task — the server must refuse and the refusal
		// must decode as 503, proving the 01f7 routing on the real wire.
		probe := int32(0)
		for _, td := range snap.GetDataList() {
			if _, ok := unionTaskTargets[td.GetTaskId()]; ok && td.GetState() == 1 {
				probe = td.GetTaskId()
				break
			}
		}
		if probe == 0 {
			t.Log("nothing claimable and no claimed task to probe — filter path verified")
			return
		}
		t.Logf("no claimable task; probing claim of claimed id %d (expect refusal)", probe)
		ids = []int32{probe}
		probeMode = true
	}
	t.Logf("claiming ids %v", ids)
	reqMSG, err := pb.Marshal(&proto.TaskGetRewardReqMsg{TaskId: ids})
	if err != nil {
		t.Fatalf("marshal claim: %v", err)
	}
	p.write(pack.Pack(TASK_GET_REWARD_CMD, reqMSG, uid))
	select {
	case s := <-sentCH:
		t.Logf("sent %s", s)
	case <-time.After(3 * time.Second):
	}
	select {
	case s := <-respN:
		t.Logf("server: %s", s)
	case <-time.After(10 * time.Second):
		t.Fatal("no claim response (503) within 10s")
	}

	if probeMode {
		return
	}
	// A real claim flips state via the following 502 push (the response
	// itself carries no list) — watch the ids land at state=1.
	pending := map[int32]bool{}
	for _, id := range ids {
		pending[id] = true
	}
	flipDeadline := time.After(8 * time.Second)
	for len(pending) > 0 {
		select {
		case patch := <-syncCH:
			var m proto.TaskDataListMsg
			if pb.Unmarshal(patch, &m) != nil {
				continue
			}
			for _, td := range m.GetDataList() {
				if pending[td.GetTaskId()] {
					t.Logf("sync: task %d value=%s state=%d", td.GetTaskId(), td.GetValue(), td.GetState())
					if td.GetState() == 1 {
						delete(pending, td.GetTaskId())
					}
				}
			}
		case <-flipDeadline:
			for id := range pending {
				t.Errorf("task %d still not marked claimed by 502", id)
			}
			return
		}
	}
}
