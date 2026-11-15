//go:build live

package tytd_go

// Cửu U (Holy Land, activity type 346) schedule probe: log in with the
// throwaway account, wait for the login ActList push, and print the
// type-346 windows (plus every currently-active window for context) with
// Vietnam-time stamps.
//
//	TYTD_LIVE=1 [TYTD_LIVE_SERVER=710564] go test -count=1 -tags "sqlcipher live" -run TestLiveCuuuOpen -v -timeout 240s .

import (
	"os"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func TestLiveCuuuOpen(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("TYTD_LIVE=1 to run")
	}
	server := 710547
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		server, _ = strconv.Atoi(v)
	}
	s, err := chaosDialLive(t, liveUserEnv(t), livePassEnv(t), server)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer s.cleanup()
	chaosWaitLogin(t, s, 40*time.Second)

	fmtTime := func(ms int64) string {
		return time.UnixMilli(ms).In(ictZone).Format("02/01 15:04")
	}
	now := time.Now()
	t.Logf("now (UTC+7): %s — server %d", now.In(ictZone).Format("02/01 15:04"), server)
	found := false
	for _, e := range s.p.ActList.snapshot() {
		if e.Type == hlActivityTypeHolyLand {
			found = true
			t.Logf("CỬU U act=%d type=%d child=%d status=%s window=%s→%s (show %s→%s)",
				e.ActivityID, e.Type, e.ChildType, e.status(now),
				fmtTime(e.BeginTime), fmtTime(e.EndTime),
				fmtTime(e.BeginShowTime), fmtTime(e.EndShowTime))
		}
	}
	if !found {
		t.Log("no type-346 entry in ActList at all")
	}

	// The client's real open-gate is the schedule sync (209570 → 2562), not
	// the ActList: it carries isOpen/isApply/hasQuality/roundId and the
	// round timestamps. Ask for it the same way HolyLandMgr does.
	s.p.writeChan <- pack.Pack(HOLY_LAND_SCHEDULE_CMD, []byte{}, s.p.UID)
	deadline := time.After(15 * time.Second)
	for done := false; !done; {
		select {
		case f := <-s.raw:
			switch f.msgType {
			case SERVER_HOLY_LAND_SCHEDULE:
				var resp proto.HolyLandBattleApplyDataSyncMsg
				if err := pb.Unmarshal(f.data, &resp); err != nil {
					t.Logf("2562 unparsed: %s", chaosWire(f.data, 1))
					break
				}
				t.Logf("SCHEDULE 2562: ret=%d open=%v apply=%v quality=%v round=%d cross=%v",
					resp.GetRet(), resp.GetIsOpen(), resp.GetIsApply(),
					resp.GetHasQuality(), resp.GetRoundId(), resp.GetNotFoundCrossId())
				for i, ts := range resp.GetScheduleTimestamp() {
					t.Logf("  round[%d] = %s", i, fmtTime(ts))
				}
				// Exercise the shipped gate with the real answer bytes.
				s.p.hlHandleScheduleMsg(f.data)
				t.Logf("gate after 2562: holyLandEventOpen=%v", holyLandEventOpen(s.p))
				done = true
			case SERVER_HOLY_LAND_SCHEDULE_PUSH:
				var push proto.HolyLandBattleTimeStampsDataSyncMsg
				if pb.Unmarshal(f.data, &push) == nil {
					t.Logf("2567 push ret=%d %d anchor(s)", push.GetRet(), len(push.GetScheduleTimestamp()))
					for i, ts := range push.GetScheduleTimestamp() {
						t.Logf("  push[%d] = %s", i, fmtTime(ts))
					}
				}
			}
		case <-deadline:
			t.Log("2562 never answered (server has no schedule for this account?)")
			done = true
		}
	}
}

// TestLiveCuuuMapRender fetches the real minimap and runs the shipped
// compositor over it — the visual acceptance check for the 99% renderer.
//
//	TYTD_LIVE=1 [TYTD_LIVE_SERVER=710564] go test -count=1 -tags "sqlcipher live" -run TestLiveCuuuMapRender -v -timeout 240s .
func TestLiveCuuuMapRender(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("TYTD_LIVE=1 to run")
	}
	server := 710547
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		server, _ = strconv.Atoi(v)
	}
	s, err := chaosDialLive(t, liveUserEnv(t), livePassEnv(t), server)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer s.cleanup()
	chaosWaitLogin(t, s, 40*time.Second)

	// The client opens the event with base info (209501) before asking the
	// map; a bare 209526 answers ret=9505 (rejection) without that handshake.
	s.p.writeChan <- pack.Pack(HOLY_LAND_BASE_INFO_CMD, []byte{}, s.p.UID)
	baseDeadline := time.After(10 * time.Second)
	group := int32(0)
	gameType := int32(0)
baseWait:
	for {
		select {
		case f := <-s.raw:
			if f.msgType == SERVER_HOLY_LAND_BASE_INFO {
				var bi proto.HolyLandBaseInfoRespMsg
				if pb.Unmarshal(f.data, &bi) == nil {
					group, gameType = bi.GetGroup(), bi.GetGameType()
					t.Logf("251d base: ret=%d group=%d gameType=%d union=%d", bi.GetRet(), bi.GetGroup(), bi.GetGameType(), bi.GetUnionId())
				}
				break baseWait
			}
		case <-baseDeadline:
			t.Log("no 251d; trying 209526 bare anyway")
			break baseWait
		}
	}

	// HolyLandGameMgr residency: the stay loop polls 209521 as the state
	// spine before any map request is answered.
	s.p.writeChan <- pack.Pack(HOLY_LAND_GAME_INFO_CMD, []byte{}, s.p.UID)
	giDeadline := time.After(10 * time.Second)
giWait:
	for {
		select {
		case f := <-s.raw:
			if f.msgType == SERVER_HOLY_LAND_GAME_INFO {
				var gi proto.HolyLandGameInfoRespMsg
				if pb.Unmarshal(f.data, &gi) == nil {
					t.Logf("2531 gameinfo: ret=%d location=%d realms=%d power=%d", gi.GetRet(), gi.GetLocation(), gi.GetRealmsType(), gi.GetCurPower())
				}
				break giWait
			}
		case <-giDeadline:
			t.Log("no 2531 within 10s")
			break giWait
		}
	}

	// Probe which request shape the server accepts (ret=9505 rejection on the
	// bare form): (gameType, group) combinations, first ret==0 wins.
	type probe struct {
		name  string
		gt, g *int32
	}
	gt1, gt2 := pb.Int32(gameType), pb.Int32(2)
	gg := pb.Int32(group)
	probes := []probe{
		{"clientShape(gt,group)", gt1, gg},
		{"gt1+group", pb.Int32(1), gg},
		{"bare+group", nil, gg},
		{"gt2+group", gt2, gg},
		{"bare", nil, nil},
	}
	sawReject := false
	for i, pr := range probes {
		body, _ := pb.Marshal(&proto.HolyLandGameMiniMapInfoReqMsg{GameType: pr.gt, Group: pr.g})
		s.p.writeChan <- pack.Pack(HOLY_LAND_MINIMAP_CMD, body, s.p.UID)
		deadline := time.After(8 * time.Second)
		var payload []byte
		bad := false
	collect:
		for {
			select {
			case f := <-s.raw:
				if f.msgType != SERVER_HOLY_LAND_MINIMAP {
					continue
				}
				if _, err := gimap.DecodeHolyLand(f.data); err != nil {
					t.Logf("minimap %s: %v", pr.name, err)
					bad = true
					continue collect
				}
				payload = f.data
				break collect
			case <-deadline:
				break collect
			}
		}
		if bad && i < len(probes)-1 {
			continue
		}
		sawReject = sawReject || bad
		if payload == nil {
			if sawReject {
				// ret=9505 on every shape (and on 209521) = this account has
				// no battlefield residency yet: the server answers map/game
				// queries only for unions that actually entered Cửu U. The
				// renderer is covered by the synthetic-payload tests in
				// internal/gimap; run this once with a resident account for
				// visual acceptance.
				t.Skip("minimap kept answering ret=9505 — account not resident in the holyland battlefield")
			}
			t.Fatal("no accepted minimap answer")
		}
		states, err := gimap.DecodeHolyLand(payload)
		if err != nil {
			t.Fatalf("decode live minimap: %v", err)
		}
		t.Logf("live overlay (%s): %d cities, %d owned", pr.name, len(states), func() (n int) {
			for _, st := range states {
				if st.Owner != "" {
					n++
				}
			}
			return
		}())
		pngBytes, err := gimap.RenderHolyLand(payload, 1400)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if err := os.WriteFile("/tmp/hl_live.png", pngBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rendered /tmp/hl_live.png (%d bytes)", len(pngBytes))
		return
	}
}
