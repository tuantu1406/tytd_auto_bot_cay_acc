package tytd_go

// KunlunWar transport tests — opcode arithmetic (the %200000 hex mapping is
// the only thing that can silently break the whole port), phase windows
// replayed from a synthetic peakHoldTimeConfigs, push folding into the board
// cache, and the per-cmd answer seats.

import (
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func kuHex(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.ParseInt(s, 16, 32)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return int(n)
}

func TestKuCommandOpcodes(t *testing.T) {
	reqs := map[string]int{
		KUNLUN_ENTER_ACTIVITY_CMD:    219901,
		KUNLUN_ENTER_GAME_CMD:        219902,
		KUNLUN_GAME_BLOCK_INFO_CMD:   219903,
		KUNLUN_MOVE_CMD:              219904,
		KUNLUN_EXPLORE_CMD:           219905,
		KUNLUN_SWEEP_CMD:             219906,
		KUNLUN_WORSHIP_CMD:           219907,
		KUNLUN_GROUP_INFO_CMD:        219909,
		KUNLUN_SIMPLE_BLOCK_INFO_CMD: 219916,
		KUNLUN_HEART_BEAT_CMD:        219917,
		KUNLUN_MINI_MAP_INFO_CMD:     219919,
		KUNLUN_LEAVE_CMD:             219958,
	}
	for cmd, want := range reqs {
		if got := kuHex(t, cmd); got != want {
			t.Errorf("cmd %s = %d, want %d", cmd, got, want)
		}
	}
	answers := map[string]string{ // response type = request % 200000
		SERVER_KUNLUN_ENTER_ACTIVITY:    KUNLUN_ENTER_ACTIVITY_CMD,
		SERVER_KUNLUN_ENTER_GAME:        KUNLUN_ENTER_GAME_CMD,
		SERVER_KUNLUN_GAME_BLOCK_INFO:   KUNLUN_GAME_BLOCK_INFO_CMD,
		SERVER_KUNLUN_MOVE:              KUNLUN_MOVE_CMD,
		SERVER_KUNLUN_EXPLORE:           KUNLUN_EXPLORE_CMD,
		SERVER_KUNLUN_SWEEP:             KUNLUN_SWEEP_CMD,
		SERVER_KUNLUN_WORSHIP:           KUNLUN_WORSHIP_CMD,
		SERVER_KUNLUN_GROUP_INFO:        KUNLUN_GROUP_INFO_CMD,
		SERVER_KUNLUN_SIMPLE_BLOCK_INFO: KUNLUN_SIMPLE_BLOCK_INFO_CMD,
		SERVER_KUNLUN_HEART_BEAT:        KUNLUN_HEART_BEAT_CMD,
		SERVER_KUNLUN_MINI_MAP_INFO:     KUNLUN_MINI_MAP_INFO_CMD,
		SERVER_KUNLUN_LEAVE:             KUNLUN_LEAVE_CMD,
	}
	for srv, cmd := range answers {
		if want := kuHex(t, cmd) % 200000; kuHex(t, srv) != want {
			t.Errorf("answer %s = %d, want %d (req %s)", srv, kuHex(t, srv), want, cmd)
		}
	}
	pushes := map[string]int{
		SERVER_KUNLUN_BLOCK_SYNC:       19931,
		SERVER_KUNLUN_CITY_SYNC:        19932,
		SERVER_KUNLUN_MY_GAME_SYNC:     19933,
		SERVER_KUNLUN_BEEN_KILL_SYNC:   19940,
		SERVER_KUNLUN_LEFT_OUT_SYNC:    19941,
		SERVER_KUNLUN_CITY_BATTLE_SYNC: 19942,
		SERVER_KUNLUN_ROUTE_SYNC:       19943,
		SERVER_KUNLUN_LINE_SYNC:        19944,
		SERVER_KUNLUN_ATTACK_SYNC:      19948,
		SERVER_KUNLUN_GROUP_RANK_SYNC:  19950,
	}
	for srv, want := range pushes {
		if got := kuHex(t, srv); got != want {
			t.Errorf("push %s = %d, want %d", srv, got, want)
		}
	}
}

func TestKuPhase(t *testing.T) {
	begin := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).UnixMilli()
	mk := func(typ int32, b, d int32) *proto.KunlunPeakHoldTimeConfig {
		return &proto.KunlunPeakHoldTimeConfig{Type: &typ, BeginTime: &b, DurationTime: &d}
	}
	st := &kunlunState{
		BeginTime: begin,
		Cfg: &proto.KunlunWarConfigSync{PeakHoldTimeConfigs: []*proto.KunlunPeakHoldTimeConfig{
			mk(1, 0, 1), // SignUp: h0–h1
			mk(2, 2, 1), // Prepare:  h2–h3
			mk(3, 4, 2), // Battle:   h4–h6
		}},
	}
	cases := []struct {
		hours float64
		want  string
	}{
		{-1, "not_open"},
		{0.5, "signup"},
		{1.5, "prepare"}, // gap between configured windows
		{2.5, "prepare"},
		{4.5, "battle"},
		{6 + 2.0/60, "settle"}, // inside the lastEnd h6 + 300s window
		{6 + 1.0 / 6, "reward"},
	}
	for _, tc := range cases {
		now := time.UnixMilli(begin + int64(tc.hours*float64(time.Hour/time.Millisecond)))
		if got := st.kuPhase(now); got != tc.want {
			t.Errorf("phase at +%fh = %q, want %q", tc.hours, got, tc.want)
		}
	}
	if got := (&kunlunState{}).kuPhase(time.Now()); got != "unknown" {
		t.Errorf("no cfg = %q, want unknown", got)
	}
}

func TestKuFoldAndDeliver(t *testing.T) {
	p := &Player{Username: "tester"}
	idx := int32(32*61 + 32)
	mini, err := pb.Marshal(&proto.KunlunWarMiniMapInfoRespMsg{
		CityInfo: []*proto.KunlunWarMiniCityInfo{{CityId: pb.Int32(1001), UnionId: pb.Int64(101)}},
		CellInfo: []*proto.KunlunWarMiniCellInfo{{Index: &idx, UnionId: pb.Int64(101)}},
		BaseData: []*proto.KunlunUnionBaseData{{UnionId: pb.Int64(101), Name: pb.String("Aethel")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A claimed seat receives the frame AND the cache folds it (both halves
	// of kuDeliver's contract).
	ch, err := p.kuClaim(SERVER_KUNLUN_MINI_MAP_INFO)
	if err != nil {
		t.Fatal(err)
	}
	if !p.kuSeatBusy(SERVER_KUNLUN_MINI_MAP_INFO) {
		t.Fatal("seat busy check failed")
	}
	if _, err := p.kuClaim(SERVER_KUNLUN_MINI_MAP_INFO); err != errKuBusy {
		t.Fatalf("second claim = %v, want errKuBusy", err)
	}
	p.kuDeliver(SERVER_KUNLUN_MINI_MAP_INFO, mini)
	if got := <-ch; string(got) != string(mini) {
		t.Fatal("seat got a different frame")
	}
	p.kuRelease(SERVER_KUNLUN_MINI_MAP_INFO, ch)
	if p.kuSeatBusy(SERVER_KUNLUN_MINI_MAP_INFO) {
		t.Fatal("seat still busy after release")
	}
	st := p.kuState()
	if st.Mini == nil || st.CityOwner[1001] == nil || st.CellOwner[idx] == nil || st.Unions[101] == nil {
		t.Fatalf("mini fold incomplete: %+v", st)
	}

	// Push-only frames fold with NO seat waiting.
	bs, _ := pb.Marshal(&proto.KunlunWarBlockSync{BlockInfo: []*proto.KunlunWarBlockInfo{{
		Pos: pb.Int32(100),
		UnionBlockData: []*proto.KunlunWarUnionBlockData{{
			UnionId: pb.Int64(202), OccupyProgress: pb.Int32(7),
		}},
	}}})
	p.kuDeliver(SERVER_KUNLUN_BLOCK_SYNC, bs)
	if st.CellOwner[100].GetUnionId() != 202 {
		t.Fatalf("block sync not folded: %+v", st.CellOwner[100])
	}
	cs, _ := pb.Marshal(&proto.KunlunWarCitySync{CityInfo: []*proto.KunlunWarGameCityInfo{{
		CityId: pb.Int32(1001), UnionId: pb.Int64(303), HaveFight: pb.Bool(true),
	}}})
	p.kuDeliver(SERVER_KUNLUN_CITY_SYNC, cs)
	if st.CityOwner[1001].GetUnionId() != 303 || !st.CityOwner[1001].GetHaveFight() {
		t.Fatalf("city sync not folded: %+v", st.CityOwner[1001])
	}
	mg, _ := pb.Marshal(&proto.KunlunWarMyGameInfoSyncMsg{Location: pb.Int32(2016)})
	p.kuDeliver(SERVER_KUNLUN_MY_GAME_SYNC, mg)
	if st.SelfPoint != 2016 {
		t.Fatalf("my-game sync not folded: SelfPoint=%d", st.SelfPoint)
	}
	lo, _ := pb.Marshal(&proto.KunlunWarLeftOutSyncInfo{BlockIndex: []int32{100}})
	p.kuDeliver(SERVER_KUNLUN_LEFT_OUT_SYNC, lo)
	if st.CellOwner[100] != nil {
		t.Fatal("left-out cell not dropped")
	}
}

func TestKunlunStatusShape(t *testing.T) {
	p := &Player{Username: "tester"}
	st := p.kuState()
	id := int32(555)
	st.ActivityID = id
	st.BeginTime = time.Now().Add(-time.Hour).UnixMilli()
	st.Enter = &proto.KunlunWarEnterActivityResp{Floor: pb.Int32(2), SelfRank: pb.Int32(9), UnionId: pb.Int64(101)}
	st.Mini = &proto.KunlunWarMiniMapInfoRespMsg{
		CityInfo: []*proto.KunlunWarMiniCityInfo{
			{CityId: pb.Int32(1001), UnionId: pb.Int64(101)},
			{CityId: pb.Int32(2001), UnionId: pb.Int64(202)},
		},
	}
	got := p.KunlunStatus(time.Now())
	if got["activityId"] != int32(555) || got["floor"] != int32(2) || got["rank"] != int32(9) {
		t.Fatalf("status %+v", got)
	}
	if got["citiesHeld"] != 1 {
		t.Fatalf("citiesHeld %v, want 1", got["citiesHeld"])
	}
}
