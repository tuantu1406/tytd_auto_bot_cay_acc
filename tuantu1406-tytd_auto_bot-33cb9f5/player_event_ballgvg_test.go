package tytd_go

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Ball GVG (Thượng Cổ Di Tích) pins: schema round-trips against the decoded
// protocol in ../remoteBallGVG, the cmd↔transport id mapping, the pure gates,
// the 03eb dual-use routing, and the join/death/seize notifications.

func TestBGCmdTransportMap(t *testing.T) {
	table := []struct{ cmd, server string }{
		{BALL_GVG_ENTER_ACT_CMD, SERVER_BALL_GVG_ENTER_ACT},
		{BALL_GVG_ENTER_ROOM_CMD, SERVER_BALL_GVG_ENTER_ROOM},
		{BALL_GVG_LEAVE_ROOM_CMD, SERVER_BALL_GVG_LEAVE_ROOM},
		{BALL_GVG_MOVE_CMD, SERVER_BALL_GVG_MOVE},
		{BALL_GVG_ATTACK_CMD, SERVER_BALL_GVG_ATTACK},
		{BALL_GVG_MARK_CMD, SERVER_BALL_GVG_MARK},
		{BALL_GVG_GET_PLACE_CMD, SERVER_BALL_GVG_GET_PLACE},
	}
	for _, tc := range table {
		id, err := strconv.ParseInt(tc.cmd, 16, 64)
		if err != nil {
			t.Fatalf("cmd %s: %v", tc.cmd, err)
		}
		want := fmt.Sprintf("%04x", id%10000)
		if tc.server != want {
			t.Fatalf("%s → %s want %s", tc.cmd, tc.server, want)
		}
	}
}

func TestBGEnterSnapshotRoundTrip(t *testing.T) {
	raw, err := pb.Marshal(&proto.BallGVGEnterActivityRespMsg{
		UserData: &proto.BallGVGUserDataTemp{
			ActivityId: 9061675,
			CampId:     pb.Int32(2),
			PlaceId:    pb.Int32(4003),
			AttackTime: pb.Int64(1758900000000),
			ReviveTime: pb.Int64(0),
			Property: &proto.BallGVGUserPropertyTemp{
				Hp: pb.String("123456.78901"), TotalHp: pb.String("200000.00000"),
				FightValue: pb.String("98765.43210"), AttackSpeed: pb.Int32(1500),
			},
		},
		Camp:  &proto.BallGVGCampTemp{CampId: 2, MajorPlaceId: pb.Int32(4001)},
		Score: []*proto.BallGVGCampScoreSyncTemp{{CampId: pb.Int32(2), Score: pb.Int64(420)}},
		PlaceData: &proto.BallGVGPlaceDataTemp{
			PlaceId: pb.Int32(4003),
			User: []*proto.BallGVGOtherUserTemp{{
				CampId: pb.Int32(3),
				PlayerData: &proto.BallGVGPlayerImage{PlayerBaseDataMsg: &proto.BallGVGPlayerBase{
					PlayerId: pb.Int64(77), NickName: pb.String("Đối Thủ"),
				}},
				Property: &proto.BallGVGUserPropertyTemp{Hp: pb.String("55.5")},
				Position: &proto.BallGVGPositionTemp{X: "1125.00000", Y: "900.00000", Time: 1758900000123},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var resp proto.BallGVGEnterActivityRespMsg
	if err := pb.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	ud := resp.GetUserData()
	if ud.GetActivityId() != 9061675 || ud.GetCampId() != 2 || ud.GetPlaceId() != 4003 {
		t.Fatalf("userData: %+v", ud)
	}
	if ud.GetProperty().GetHp() != "123456.78901" || ud.GetProperty().GetAttackSpeed() != 1500 {
		t.Fatalf("property: %+v", ud.GetProperty())
	}
	if resp.GetCamp().GetMajorPlaceId() != 4001 || resp.GetScore()[0].GetScore() != 420 {
		t.Fatalf("camp/score: %+v", &resp)
	}
	u := resp.GetPlaceData().GetUser()[0]
	if u.GetPlayerData().GetPlayerBaseDataMsg().GetNickName() != "Đối Thủ" ||
		u.GetPosition().GetX() != "1125.00000" {
		t.Fatalf("other user: %+v", u)
	}
}

func TestBGZeroHP(t *testing.T) {
	for _, tc := range []struct {
		hp   string
		zero bool
	}{
		{"0", true}, {"0.00000", true}, {" 0.0 ", true},
		{"12.5", false}, {"", false}, {"abc", false},
	} {
		if got := bgZeroHP(tc.hp); got != tc.zero {
			t.Fatalf("bgZeroHP(%q)=%v want %v", tc.hp, got, tc.zero)
		}
	}
}

func TestBGReviveLeft(t *testing.T) {
	now := time.Now()
	if _, waiting := bgReviveLeft(0, now); waiting {
		t.Fatal("reviveTime 0 must not be waiting")
	}
	if left, waiting := bgReviveLeft(now.Add(time.Minute).UnixMilli(), now); !waiting || left < 55*time.Second {
		t.Fatalf("future ms deadline: %v %v", left, waiting)
	}
	// seconds-epoch input normalizes the same way (1e12 cutoff, shared with
	// holy land).
	if _, waiting := bgReviveLeft(now.Add(time.Minute).Unix(), now); !waiting {
		t.Fatal("seconds deadline must normalize to waiting")
	}
	if _, waiting := bgReviveLeft(now.Add(-time.Minute).UnixMilli(), now); waiting {
		t.Fatal("past deadline must not be waiting")
	}
}

func TestBGAttackReady(t *testing.T) {
	now := time.Now()
	st := &ballGVGState{AttackTime: now.Add(-2 * time.Second).UnixMilli(), AttackSpeed: 1500}
	if !bgAttackReady(st, now) {
		t.Fatal("2s since attack with 1.5s speed must be ready")
	}
	if bgAttackReady(st, now.Add(-1500*time.Millisecond)) {
		t.Fatal("attack inside cooldown must not be ready")
	}
}

func TestBGAttackRefusal(t *testing.T) {
	now := time.Now()
	st := &ballGVGState{CampID: 1}
	mk := func(id int64, camp int32, hp string, revive int64) *bgRosterUser {
		return &bgRosterUser{PlayerID: id, CampID: camp, HP: hp, ReviveTime: revive}
	}
	table := []struct {
		name string
		u    *bgRosterUser
		want string // "" = allowed
	}{
		{"missing", nil, "không có trong phòng"},
		{"self", mk(9, 2, "100", 0), "không đánh chính mình"},
		{"friendly", mk(10, 1, "100", 0), "cùng phe"},
		{"dead hp", mk(10, 2, "0.00000", 0), "đang chết"},
		{"dead revive", mk(10, 2, "100", now.Add(time.Minute).UnixMilli()), "đang chết"},
		{"ok", mk(10, 2, "100", 0), ""},
	}
	for _, tc := range table {
		if got := bgAttackRefusal(st, 9, tc.u, now); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestBGShouldTryEnter(t *testing.T) {
	now := time.Now()
	joined := &ballGVGState{ActivityID: 5, Joined: true, lastEnterTry: time.Time{}}
	if bgShouldTryEnter(joined, now) {
		t.Fatal("joined account must not retry entry")
	}
	closed := &ballGVGState{}
	if bgShouldTryEnter(closed, now) {
		t.Fatal("no activity id must not retry entry")
	}
	fresh := &ballGVGState{ActivityID: 5, lastEnterTry: now.Add(-bgEnterRetryGap - time.Second)}
	if !bgShouldTryEnter(fresh, now) {
		t.Fatal("gap elapsed must allow a retry")
	}
	recent := &ballGVGState{ActivityID: 5, lastEnterTry: now.Add(-time.Second)}
	if bgShouldTryEnter(recent, now) {
		t.Fatal("recent attempt must pace out")
	}
}

func TestBGOrderGuards(t *testing.T) {
	// Stopped account: nothing runs.
	p := &EventPlayer{}
	if err := p.BallGVGEnterRoom(4001); err == nil {
		t.Fatal("stopped account accepted an order")
	}

	p.Running = true
	p.writeChan = make(chan []byte, 8)
	wireCtxEP(t, p)
	if err := p.BallGVGEnterRoom(4001); err == nil {
		t.Fatal("unannounced event accepted an order")
	}
	st := p.bgState()
	st.ActivityID = 555
	if err := p.BallGVGEnterRoom(4001); err == nil {
		t.Fatal("unjoined account accepted an order")
	}
	st.Joined = true
	st.ReviveTime = time.Now().Add(time.Minute).UnixMilli()
	if err := p.BallGVGEnterRoom(4001); err == nil {
		t.Fatal("dead account accepted a move order")
	}
	// Leaving the room is allowed even while dead.
	if err := p.BallGVGLeaveRoom(); err != nil {
		t.Fatalf("dead account refused leave-room: %v", err)
	}
	if got := frameFuncType(t, <-p.writeChan); got != BALL_GVG_LEAVE_ROOM_CMD {
		t.Fatalf("leave frame %s", got)
	}
	st.ReviveTime = 0
	if err := p.BallGVGEnterRoom(0); err == nil {
		t.Fatal("place id 0 accepted")
	}
	if err := p.BallGVGEnterRoom(4001); err != nil {
		t.Fatalf("valid order refused: %v", err)
	}
	if got := frameFuncType(t, <-p.writeChan); got != BALL_GVG_ENTER_ROOM_CMD {
		t.Fatalf("enter frame %s", got)
	}
	// Home before camp assignment is knowable refuses honestly.
	if err := p.BallGVGHome(); err == nil {
		t.Fatal("home without camp accepted")
	}
	st.CampID = 2
	if err := p.BallGVGHome(); err != nil {
		t.Fatalf("home refused: %v", err)
	}
	<-p.writeChan
}

func TestBGActivityListDiscovery(t *testing.T) {
	p := &EventPlayer{}
	push, err := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{
		{ActivityId: pb.Int32(555), Type: pb.Int32(99)},
		{ActivityId: pb.Int32(777), Type: pb.Int32(activityTypeBallGVG)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p.bgHandleActivityList(push, true)
	if p.bgState().ActivityID != 777 {
		t.Fatalf("type-14 id not learned: %+v", p.bg)
	}
	// Announced alone must NOT light the display surface (the old bug: the
	// event stays visible after it ends). No window entry yet → not open.
	if p.BallGVGEventOpen() {
		t.Fatal("announce without an active window must not count as open")
	}
	setActivities(&p.ActList, activityEntry{
		ActivityID: 777, Type: activityTypeBallGVG,
		BeginTime: time.Now().Add(-time.Hour).UnixMilli(),
		EndTime:   time.Now().Add(time.Hour).UnixMilli(),
	})
	if !p.BallGVGEventOpen() {
		t.Fatal("announced + active window must be open")
	}

	// A new instance resets residency and the once-per-window notify flag.
	p.bgState().Joined = true
	p.bgState().notifiedJoin = true
	push2, _ := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{
		{ActivityId: pb.Int32(888), Type: pb.Int32(activityTypeBallGVG)},
	}})
	p.bgHandleActivityList(push2, false)
	st := p.bgState()
	if st.ActivityID != 888 || st.Joined || st.notifiedJoin {
		t.Fatalf("instance change not reset: %+v", st)
	}

	// A full list WITHOUT type 14 withdraws the announcement.
	push3, _ := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{
		{ActivityId: pb.Int32(9), Type: pb.Int32(99)},
	}})
	p.bgHandleActivityList(push3, true)
	if p.BallGVGEventOpen() {
		t.Fatal("withdrawn event still open")
	}
}

func TestBGActivityDetailRouting(t *testing.T) {
	mkResp := func(actID int32) []byte {
		unique, _ := pb.Marshal(&proto.BallGVGConfigSyncMsg{
			PlaceConfig: []*proto.BallGVGPlaceConfigTemp{{Id: pb.Int32(4001), Name: pb.String("Uy Diên"), Type: pb.Int32(4)}},
		})
		mine, _ := pb.Marshal(&proto.BallGVGActivityDataTemp{CampId: pb.Int32(2)})
		raw, _ := pb.Marshal(&proto.RspGetActivityDetail{Activity: &proto.ActivityCommonData{
			ActivityId: pb.Int32(actID),
			DetailConfig: &proto.ActivityDetailConfig{
				UniqueConfig: unique,
			},
			PlayerData: mine,
		}})
		return raw
	}
	p := &EventPlayer{}
	p.bgState().ActivityID = 777

	// Ball GVG's own answer (activityId matches) must NOT fall through.
	fell := false
	p.bgActivityDetail(mkResp(777), func() { fell = true })
	if fell {
		t.Fatal("own activity detail fell through to the GI router")
	}
	st := p.bgState()
	if !st.ConfigFetched || st.Rooms[4001] == nil || st.CampID != 2 {
		t.Fatalf("detail not cached: %+v", st)
	}

	// Someone else's answer (God Island) must run the fallback untouched.
	p.bgActivityDetail(mkResp(999), func() { fell = true })
	if !fell {
		t.Fatal("foreign activity detail did not fall through")
	}
}

func TestBGEnterJoinAndNotifyOnce(t *testing.T) {
	p := &EventPlayer{Running: true, Notify: true, NotiChan: make(chan NotifyMGS, 8)}
	p.writeChan = make(chan []byte, 8)
	wireCtxEP(t, p)
	st := p.bgState()
	st.ActivityID = 555

	refused, _ := pb.Marshal(&proto.BallGVGEnterActivityRespMsg{
		Ret: pb.Int32(12), UserData: &proto.BallGVGUserDataTemp{ActivityId: 555},
	})
	p.bgHandleEnterAct(refused)
	if st.Joined || len(p.NotiChan) != 1 {
		t.Fatalf("first refusal: joined=%v notifs=%d", st.Joined, len(p.NotiChan))
	}
	if n := <-p.NotiChan; n.MSG == "" {
		t.Fatal("empty notify")
	}
	// Retries pace but must not re-notify within the same activityId.
	p.bgHandleEnterAct(refused)
	if len(p.NotiChan) != 0 {
		t.Fatalf("refusal notified twice: %d", len(p.NotiChan))
	}

	ok, _ := pb.Marshal(&proto.BallGVGEnterActivityRespMsg{
		UserData: &proto.BallGVGUserDataTemp{ActivityId: 555, CampId: pb.Int32(1), PlaceId: pb.Int32(4001)},
		Camp:     &proto.BallGVGCampTemp{CampId: 1, MajorPlaceId: pb.Int32(4001)},
		Score:    []*proto.BallGVGCampScoreSyncTemp{{CampId: pb.Int32(1), Score: pb.Int64(10)}},
		PlaceData: &proto.BallGVGPlaceDataTemp{PlaceId: pb.Int32(4001), User: []*proto.BallGVGOtherUserTemp{{
			CampId:     pb.Int32(1),
			PlayerData: &proto.BallGVGPlayerImage{PlayerBaseDataMsg: &proto.BallGVGPlayerBase{PlayerId: pb.Int64(5), NickName: pb.String("Đồng Minh")}},
			Property:   &proto.BallGVGUserPropertyTemp{Hp: pb.String("99")},
		}}},
	})
	p.bgHandleEnterAct(ok)
	if !st.Joined || st.CampID != 1 || st.PlaceID != 4001 || st.Scores[1] != 10 || st.Roster[5] == nil {
		t.Fatalf("join snapshot not applied: %+v", st)
	}
	// Alive in a room → exactly one cosmetic position report.
	if got := frameFuncType(t, <-p.writeChan); got != BALL_GVG_MOVE_CMD {
		t.Fatalf("expected position report, got %s", got)
	}
	// Same room again must not re-report.
	p.bgHandleEnterAct(ok)
	select {
	case f := <-p.writeChan:
		t.Fatalf("redundant frame %s", frameFuncType(t, f))
	default:
	}
}

func TestBGBeatenSyncDeath(t *testing.T) {
	p := &EventPlayer{Notify: true, NotiChan: make(chan NotifyMGS, 4)}
	sync, _ := pb.Marshal(&proto.BallGVGAttackedUserSyncMsg{
		AttackUserData: &proto.BallGVGOtherUserTemp{PlayerData: &proto.BallGVGPlayerImage{
			PlayerBaseDataMsg: &proto.BallGVGPlayerBase{PlayerId: pb.Int64(42), NickName: pb.String("Sát Thủ")}}},
		Attack: &proto.BallGVGAttackedTemp{AttackPlayerId: pb.Int64(42), AttackHp: pb.String("80"), AttackedHp: pb.String("0.00000")},
		UserData: &proto.BallGVGUserDataTemp{
			ReviveTime: pb.Int64(time.Now().Add(90 * time.Second).UnixMilli()),
			Property:   &proto.BallGVGUserPropertyTemp{Hp: pb.String("0.00000")},
		},
	})
	p.bgHandleBeatenSync(sync)
	if len(p.NotiChan) != 1 {
		t.Fatalf("death notifications: %d", len(p.NotiChan))
	}
	n := <-p.NotiChan
	for _, want := range []string{"☠️", "Sát Thủ", "hồi sinh"} {
		if !strings.Contains(n.MSG, want) {
			t.Fatalf("notify %q missing %q", n.MSG, want)
		}
	}
	if p.bgState().HP != "0.00000" {
		t.Fatalf("userData not replaced: %+v", p.bgState())
	}
}

func TestBGSeizeFlip(t *testing.T) {
	p := &EventPlayer{Notify: true, NotiChan: make(chan NotifyMGS, 8)}
	st := p.bgState()
	st.CampID = 1
	st.Owners = map[int32]int32{100: 2, 200: 1}

	mk := func(place, camp int32) []byte {
		raw, _ := pb.Marshal(&proto.BallGVGPlaceSeizeSyncMsg{PlaceId: pb.Int32(place), CampId: pb.Int32(camp)})
		return raw
	}
	p.bgHandleSeizeSync(mk(100, 1)) // we took it
	p.bgHandleSeizeSync(mk(200, 3)) // we lost it
	p.bgHandleSeizeSync(mk(100, 1)) // no-op repeat
	if len(p.NotiChan) != 2 {
		t.Fatalf("flip notifications: %d", len(p.NotiChan))
	}
	a, b := <-p.NotiChan, <-p.NotiChan
	if !strings.Contains(a.MSG, "chiếm được") || !strings.Contains(b.MSG, "mất cứ điểm") {
		t.Fatalf("flip wording: %q / %q", a.MSG, b.MSG)
	}
	if st.Owners[100] != 1 || st.Owners[200] != 3 {
		t.Fatalf("owners map: %v", st.Owners)
	}
}

func TestBGScoreCampReplace(t *testing.T) {
	p := &EventPlayer{}
	score, _ := pb.Marshal(&proto.BallGVGCampScoreSyncMsg{CampScore: []*proto.BallGVGCampScoreSyncTemp{
		{CampId: pb.Int32(1), Score: pb.Int64(5)}, {CampId: pb.Int32(2), Score: pb.Int64(7)},
	}})
	p.bgHandleScoreSync(score)
	score2, _ := pb.Marshal(&proto.BallGVGCampScoreSyncMsg{CampScore: []*proto.BallGVGCampScoreSyncTemp{
		{CampId: pb.Int32(2), Score: pb.Int64(9)},
	}})
	p.bgHandleScoreSync(score2)
	st := p.bgState()
	if _, had := st.Scores[1]; had {
		t.Fatalf("score sync must REPLACE the board, got %v", st.Scores)
	}
	if st.Scores[2] != 9 {
		t.Fatalf("scores: %v", st.Scores)
	}
	camp, _ := pb.Marshal(&proto.BallGVGCampSyncMsg{Camp: &proto.BallGVGCampTemp{CampId: 2, MajorPlaceId: pb.Int32(4007)}})
	p.bgHandleCampSync(camp)
	if st.MajorPlaceID != 4007 || st.CampID != 2 {
		t.Fatalf("camp sync: %+v", st)
	}
}

func TestBGDropAndJoinSync(t *testing.T) {
	p := &EventPlayer{}
	st := p.bgState()
	add, _ := pb.Marshal(&proto.BallGVGEnterPlaceSync{User: &proto.BallGVGOtherUserTemp{
		CampId:     pb.Int32(2),
		PlayerData: &proto.BallGVGPlayerImage{PlayerBaseDataMsg: &proto.BallGVGPlayerBase{PlayerId: pb.Int64(31), NickName: pb.String("X")}},
		Property:   &proto.BallGVGUserPropertyTemp{Hp: pb.String("10")},
	}})
	p.bgHandleJoinSync(add)
	if st.Roster[31] == nil || st.Roster[31].Name != "X" {
		t.Fatalf("join sync: %+v", st.Roster)
	}
	die, _ := pb.Marshal(&proto.BallGVGLeavePlaceSync{PlayerId: pb.Int64(31), Type: pb.Int32(2)})
	p.bgHandleDropSync(die) // died in place: stays listed, HP zero
	if e := st.Roster[31]; e == nil || !bgZeroHP(e.HP) {
		t.Fatalf("type-2 drop: %+v", st.Roster[31])
	}
	gone, _ := pb.Marshal(&proto.BallGVGLeavePlaceSync{PlayerId: pb.Int64(31), Type: pb.Int32(1)})
	p.bgHandleDropSync(gone) // removal drops it
	if st.Roster[31] != nil {
		t.Fatal("type-1 drop must remove the entry")
	}
}

// ---- auto-walk / auto-strike gates (bgAuto) ----

func TestBGRoutePointRing(t *testing.T) {
	// Shared road: same idx must give the same point for every account, the
	// ring must have exactly bgRoutePoints distinct stops, and negative or
	// oversized indices must wrap onto it.
	type pt struct{ x, y float64 }
	seen := map[pt]bool{}
	for i := 0; i < bgRoutePoints; i++ {
		x, y := bgRoutePoint(i, bgCentre, bgCentreYf, 165, 120)
		if d := math.Hypot(x-bgCentre, y-bgCentreYf); d < 100 || d > 200 {
			t.Fatalf("point %d off the ellipse: d=%v", i, d)
		}
		seen[pt{x, y}] = true
	}
	if len(seen) != bgRoutePoints {
		t.Fatalf("ring has %d distinct stops, want %d", len(seen), bgRoutePoints)
	}
	for _, i := range []int{-1, bgRoutePoints, bgRoutePoints + bgRoutePoints + 3} {
		wrapped := ((i % bgRoutePoints) + bgRoutePoints) % bgRoutePoints
		gx, gy := bgRoutePoint(i, bgCentre, bgCentreYf, 165, 120)
		wx, wy := bgRoutePoint(wrapped, bgCentre, bgCentreYf, 165, 120)
		if gx != wx || gy != wy {
			t.Fatalf("idx %d did not wrap to %d", i, wrapped)
		}
	}
}

func TestBGAutoAttackAllowed(t *testing.T) {
	now := time.Now()
	ready := func() *ballGVGState {
		return &ballGVGState{
			Joined: true, CampID: 1, PlaceID: 4001,
			HP: "100", AttackTime: now.Add(-time.Minute).UnixMilli(), AttackSpeed: 1,
			Rooms:  map[int32]*proto.BallGVGPlaceConfigTemp{4001: {Type: pb.Int32(4)}},
			Roster: map[int64]*bgRosterUser{},
		}
	}
	if !bgAutoAttackAllowed(ready(), now) {
		t.Fatal("open cooldown in a type-4 room must allow")
	}

	noConf := ready()
	delete(noConf.Rooms, 4001) // home/unknown room: config absent → never fire
	if bgAutoAttackAllowed(noConf, now) {
		t.Fatal("missing room config must block")
	}
	home := ready()
	home.Rooms[4001] = &proto.BallGVGPlaceConfigTemp{Type: pb.Int32(1)}
	if bgAutoAttackAllowed(home, now) {
		t.Fatal("non-capturable (type!=4) room must block")
	}
	lobby := ready()
	lobby.PlaceID = 0
	if bgAutoAttackAllowed(lobby, now) {
		t.Fatal("activity lobby must block")
	}
	dead := ready()
	dead.ReviveTime = now.Add(time.Minute).UnixMilli()
	if bgAutoAttackAllowed(dead, now) {
		t.Fatal("dead must block")
	}
	cold := ready()
	cold.AttackTime = now.UnixMilli()
	cold.AttackSpeed = 60000 // next attack a minute out
	if bgAutoAttackAllowed(cold, now) {
		t.Fatal("server cooldown must block")
	}
	floored := ready()
	floored.lastAttackSend = now.Add(-100 * time.Millisecond) // 1s local floor
	if bgAutoAttackAllowed(floored, now) {
		t.Fatal("send floor must block")
	}
}

func TestBGPickAutoTarget(t *testing.T) {
	now := time.Now()
	mk := func(id int64, camp int32, hp string, rt int64, x, y string) *bgRosterUser {
		return &bgRosterUser{PlayerID: id, CampID: camp, HP: hp, ReviveTime: rt, X: x, Y: y}
	}
	st := &ballGVGState{
		CampID: 1,
		Roster: map[int64]*bgRosterUser{
			10: mk(10, 1, "100", 0, "1125", "900"),                                // self, skipped
			11: mk(11, 1, "100", 0, "1130", "900"),                                // friendly, skipped
			12: mk(12, 2, "100", 0, "1140", "900"),                                // enemy, far
			13: mk(13, 2, "100", 0, "1126", "900"),                                // enemy, nearest
			14: mk(14, 2, "100", 0, "1127", "900"),                                // enemy, mid
			15: mk(15, 2, "0", 0, "1125", "900"),                                  // dead HP, skipped
			16: mk(16, 2, "100", now.Add(time.Minute).UnixMilli(), "1125", "900"), // revive wait, skipped
		},
	}
	if got := bgPickAutoTarget(st, 10, 1125, 900, now); got == nil || got.PlayerID != 13 {
		t.Fatalf("nearest alive enemy: got %v want 13", got)
	}

	// No positions seen: deterministic lowest-id survivor wins.
	posless := &ballGVGState{CampID: 1, Roster: map[int64]*bgRosterUser{
		31: mk(31, 2, "100", 0, "", ""),
		22: mk(22, 2, "100", 0, "", ""),
		25: mk(25, 3, "100", 0, "junk", "junk"), // unparseable, other camp, id 25
	}}
	if got := bgPickAutoTarget(posless, 10, 0, 0, now); got == nil || got.PlayerID != 22 {
		t.Fatalf("position-less fallback: got %v want 22", got)
	}

	// Everyone filtered out → nil (nothing to hit).
	empty := &ballGVGState{CampID: 1, Roster: map[int64]*bgRosterUser{
		10: mk(10, 1, "100", 0, "1", "1"),
	}}
	if got := bgPickAutoTarget(empty, 10, 0, 0, now); got != nil {
		t.Fatalf("all-friendlies must yield nil, got %v", got)
	}
}
