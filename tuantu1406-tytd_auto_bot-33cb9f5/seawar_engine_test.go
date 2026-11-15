package tytd_go

// Offline tests for the Thương Minh Yêu Vực (CelestialSeaWar 349) engine:
// wire shape (proto2-required presence!), forge priority (owner law
// "ưu tiên thuộc tính sát thương cuối"), region unlock, and a scripted
// swPlayTick against a fake swAPI — no network, no gate.
// See player_seawar.go + [[tytd-seawar-port]].

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// The deterministic suite never pays the engine's 1s pacing (owner's
// "không cần vội" applies to the live socket, not to unit runtime). Live
// runs (TYTD_LIVE set) keep the real sleeps.
func TestMain(m *testing.M) {
	if os.Getenv("TYTD_LIVE") == "" {
		swSleepFunc = func(time.Duration) {}
	}
	os.Exit(m.Run())
}

func TestSwWirePresence(t *testing.T) {
	// The server parses with proto2 semantics: fields marked required there
	// MUST be present on the wire even when zero-valued (false/0) —
	// rbtBonusItemAllBody precedent. These two are the trap.
	b, err := pb.Marshal(&proto.SeaWarNextRegionReq{ActivityId: 5, NextRegionId: 6, UseItem: pb.Bool(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(b); got != "080510061800" {
		t.Fatalf("NEXT_REGION wire = %s, want 0805100618 00 (useItem present)", got)
	}
	b, err = pb.Marshal(&proto.SeaWarHeartBeatReq{ActivityId: 5, ChallengeState: pb.Bool(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(b); got != "08051000" {
		t.Fatalf("HEART_BEAT wire = %s, want 08051000 (challengeState present)", got)
	}
	// Owner law 2026-11-11: every int that can be 0 on an outbound SeaWar
	// request is optional in the .proto AND always wrapper-set at the marshal
	// site, so the zero rides the wire. action=0 (box/forge open) + id=0
	// (monster/skip/mark) must both be PRESENT.
	b, _ = pb.Marshal(&proto.SeaWarHandleEventReq{ActivityId: 5, EventId: 9, Action: pb.Int32(0), Id: pb.Int64(0)})
	if got := hex.EncodeToString(b); got != "0805100918002000" {
		t.Fatalf("HANDLE_EVENT box wire = %s, want 0805100918002000 (action+id present)", got)
	}
	// ASSIST: type=0 = fight assist — the zero IS the meaning, must be there.
	b, _ = pb.Marshal(&proto.SeaWarAssistReq{ActivityId: 5, Type: pb.Int32(0), TargetPlayerId: pb.Int64(7), Id: pb.Int64(9)})
	if got := hex.EncodeToString(b); got != "0805100018072009" {
		t.Fatalf("ASSIST fight wire = %s, want 0805100018072009 (type=0 present)", got)
	}
	b, _ = pb.Marshal(&proto.SeaWarAssistRewardReq{ActivityId: 5, Type: pb.Int32(0), Id: pb.Int64(9)})
	if got := hex.EncodeToString(b); got != "080510001809" {
		t.Fatalf("ASSIST_REWARD fight wire = %s, want 080510001809 (type=0 present)", got)
	}
	// UNION_RECEIVE_LIQUID "Nhận nhanh" claim-all: id=0 MUST be present
	// (same rbtBonusItemAllBody trap — a bare int64 0 vanishes from the
	// wire and the server silently no-ops; live: per-notice claims never
	// consumed a single notice).
	b, _ = pb.Marshal(&proto.SeaWarUnionLiquidRewardReq{ActivityId: 5, Id: pb.Int64(0)})
	if got := hex.EncodeToString(b); got != "08051000" {
		t.Fatalf("LIQUID claim-all wire = %s, want 08051000 (id=0 present)", got)
	}
	// DECLARATION = the "chọn lệnh" press (宣战 234944) the client fires on
	// EVERY base tap before battle. type=0 (lệnh thường) and isFree are real
	// values → both wrapper-set, both MUST ride the wire (outbound presence
	// law): without them the server can't tell free-normal from paid.
	b, _ = pb.Marshal(&proto.SeaWarDeclarationReq{ActivityId: pb.Int32(5), UnionId: pb.Int64(7), Type: pb.Int32(0), IsFree: pb.Bool(true)})
	if got := hex.EncodeToString(b); got != "0805100718002001" {
		t.Fatalf("DECLARATION free-normal wire = %s, want 0805100718002001 (type=0+isFree present)", got)
	}
	// The twins marshal ONLY through wrapper-set sites; a bare 0 literal here
	// would produce the missing-field wire the server rejects:
	b, _ = pb.Marshal(&proto.SeaWarHandleEventReq{ActivityId: 5, EventId: 9})
	if got := hex.EncodeToString(b); got != "08051009" {
		t.Fatalf("no-wrapper shape = %s, want 08051009 (documents WHY wrappers are mandatory)", got)
	}
}

func TestSwForgePriority(t *testing.T) {
	if len(swFinalEffects) != 445 {
		t.Fatalf("baked final-damage set = %d, want 445", len(swFinalEffects))
	}
	if !swFinalEffects[20017] || !swFinalEffects[120075] {
		t.Fatal("spot-check: 20017/120075 must be final-damage")
	}
	if swFinalEffects[1] {
		t.Fatal("non-skill id must not be in the set")
	}
	if len(swFinalEffectValue) != 445 {
		t.Fatalf("baked stc value map = %d, want 445", len(swFinalEffectValue))
	}
	if swFinalEffectValue[20017] != 10 || swFinalEffectValue[20041] != 20 {
		t.Fatalf("stc values: 20017=%d (want 10) 20041=%d (want 20)",
			swFinalEffectValue[20017], swFinalEffectValue[20041])
	}
	balls := []*proto.SeaWarBuffBallMsg{
		{Uid: 101, ConfigId: 500}, // plain
		{Uid: 102, ConfigId: 501}, // final damage, value 10, LOWER type (old min-LV would take it)
		{Uid: 103, ConfigId: 502}, // final damage, value 20 — owner: "viên stc có thuộc tính cao"
	}
	cfg := swCfgView{
		ballEffect: map[int32]int32{500: 1, 501: 20017, 502: 20041},
		ballType:   map[int32]int32{500: 1, 501: 2, 502: 3},
	}
	got := swPickForgeBall(balls, cfg)
	// Both finals outrank the plain ball; between the finals the HIGHER
	// attribute value wins even against the client's min-type preference.
	if got == nil || got.GetUid() != 103 {
		t.Fatalf("pick = %+v, want uid 103 (stc with higher param value)", got)
	}
	// No config folded yet → everything 0, client min-LV: lowest type, then
	// lowest configId.
	got = swPickForgeBall(balls, swCfgView{})
	if got == nil || got.GetUid() != 101 {
		t.Fatalf("fallback pick = %+v, want uid 101 (min type 1)", got)
	}
	// Equal-value finals → lowest type (client AUTO_SELECT_MIN_LV tie-break).
	tie := []*proto.SeaWarBuffBallMsg{
		{Uid: 1, ConfigId: 501}, // 20017 v10 type 2
		{Uid: 2, ConfigId: 503}, // 20018 v10 type 4
	}
	tieCfg := swCfgView{
		ballEffect: map[int32]int32{501: 20017, 503: 20018},
		ballType:   map[int32]int32{501: 2, 503: 4},
	}
	if got := swPickForgeBall(tie, tieCfg); got == nil || got.GetUid() != 1 {
		t.Fatalf("final tie = %+v, want uid 1 (equal value → type 2 < type 4)", got)
	}
}

func TestSwCmpFightValue(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"100", "99", 1},    // longer numeric wins
		{"99", "100", -1},   //
		{"100", "100", 0},   //
		{"0099", "100", -1}, /* leading zeros stripped */
		{"2000", "1999", 1},
	}
	for _, c := range cases {
		if got := swCmpFightValue(c.a, c.b); (got > 0) != (c.want > 0) || (got == 0) != (c.want == 0) {
			t.Fatalf("cmp(%q,%q)=%d want sign %d", c.a, c.b, got, c.want)
		}
	}
}

// The OR law (owner 2026-11-10 + live probe): consumeCondition
// "monsterEvent=2|105403=n" — event side met ⇒ free jump; unmet but stone
// side present ⇒ paid jump only from floor 3 up. The old all()-check
// demanded the stone pair inside eventCompleteVec → free advance NEVER
// fired (the "kẹt tầng" bug this test pins).
func TestSwRegionConditions(t *testing.T) {
	cfg := swCfgView{
		regionCond: map[int32]string{
			10001: "10003=2|105403=1",
			10003: "10009=2|105403=2",
			10005: "0",
		},
		eventCfgIds: map[int32]bool{10003: true, 10009: true, 10007: true},
	}
	pd := &proto.SeaWarPlayerDataMsg{EventCompleteVec: []*proto.SeaWarEventCompleteDataMsg{
		{EventId: 10009, Num: 1}, // one short of the floor-3 requirement
	}}
	if ok, stone := swRegionConditions(cfg, pd, 10003); ok || stone != 2 {
		t.Fatalf("monsters short → eventOK false, stone side 2: got %v/%d", ok, stone)
	}
	pd.EventCompleteVec[0].Num = 2
	if ok, stone := swRegionConditions(cfg, pd, 10003); !ok || stone != 2 {
		t.Fatalf("monsters met → free jump available (stone count still reported): got %v/%d", ok, stone)
	}
	if ok, stone := swRegionConditions(cfg, pd, 10005); !ok || stone != 0 {
		t.Fatalf(`"0" condition → free advance: got %v/%d`, ok, stone)
	}
	if ok, stone := swRegionConditions(cfg, pd, 10004); ok || stone != 0 {
		t.Fatal("unknown region → no advance")
	}
	// classifier absent (config not folded yet): digit rule — 10003 ≤5 digits
	// = event, 105403 = item.
	bare := swCfgView{regionCond: map[int32]string{10001: "10003=2|105403=1"}}
	if ok, stone := swRegionConditions(bare, pd, 10001); ok || stone != 1 {
		t.Fatalf("fallback classifier: got %v/%d", ok, stone)
	}
}

// The stone law: floors 1→2 and 2→3 = monsters only; floor 3↑ may burn
// thương minh phi thạch.
func TestSwRegionStoneLaw(t *testing.T) {
	cfg := swCfgView{regionEnergy: map[int32]int32{
		10001: 1, 10002: 2, 10003: 3, 10004: 4, 10005: 5,
	}}
	if swRegionStoneOK(cfg, 10001) || swRegionStoneOK(cfg, 10002) {
		t.Fatal("floors 1→2, 2→3 must be earned with monsters — no stone")
	}
	for _, r := range []int32{10003, 10004} {
		if !swRegionStoneOK(cfg, r) {
			t.Fatalf("floor %d↑ may use stone", r)
		}
	}
	if swRegionStoneOK(cfg, 9999) {
		t.Fatal("unknown region → no stone")
	}
}

// TestSwDayPhase — the daily battle rhythm (owner 2026-11-11): day opens
// 10:00, morning farm 10:00–10:29, ABSOLUTE strength ban 10:30–16:29,
// evening dump from 16:30.
func TestSwDayPhase(t *testing.T) {
	day := func(h, m int) time.Time { return time.Date(2026, 11, 11, h, m, 0, 0, ictZone) }
	type c struct {
		h, m int
		want swPhase
	}
	for _, tc := range []c{
		{9, 59, swPhaseSleep},
		{10, 0, swPhaseFarm},
		{10, 29, swPhaseFarm},
		{10, 30, swPhaseHold},
		{12, 0, swPhaseHold},
		{16, 29, swPhaseHold},
		{16, 30, swPhaseDump},
		{22, 0, swPhaseDump},
	} {
		if got := swPhaseOf(day(tc.h, tc.m)); got != tc.want {
			t.Fatalf("phase(%02d:%02d) = %d, want %d", tc.h, tc.m, got, tc.want)
		}
	}
}

// TestSwFloor3Schedule — scripted ticks under the 2026-11-11 rhythm
// (owner): "đầu ngày 10h sáng UTC+7 sẽ chạy và lên tầng 3 rồi xả hết tl ở
// tầng 3 rồi nằm im; đến sau 4h30 chiều xả thoải mái, check điều kiện lên
// tầng sau mỗi lần dùng thể lực, đủ điều kiện là lên ngay".
func TestSwFloor3Schedule(t *testing.T) {
	f3cfg := swCfgView{
		regionEnergy: map[int32]int32{10001: 1, 10002: 2, 10003: 3, 10004: 4, 10005: 5},
		regionCond: map[int32]string{
			10001: "10001=1|105403=1",
			10002: "10002=1|105403=1",
			10003: "10009=2|105403=2",
			10004: "0",
		},
		eventCfgIds: map[int32]bool{10001: true, 10002: true, 10009: true},
	}
	met := []*proto.SeaWarEventCompleteDataMsg{{EventId: 10009, Num: 2}}
	// parked at floor 3, floor-4 condition already met, tl to burn
	fresh := func() *fakeSw {
		return &fakeSw{
			enter: &proto.SeaWarEnterResp{Ret: 0},
			eg: &proto.SeaWarEnterGameResp{Ret: 0,
				PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 3, RegionId: 10003,
					EventCompleteVec: met}},
			nextRegionResp: &proto.SeaWarNextRegionResp{Ret: 0,
				PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 0, RegionId: 10004}},
			cfg: f3cfg,
		}
	}
	at := func(h, m int) func() {
		old := swNowFunc
		fixed := time.Date(2026, 11, 11, h, m, 0, 0, ictZone)
		swNowFunc = func() time.Time { return fixed }
		return func() { swNowFunc = old }
	}
	explores := func(calls []string) int {
		n := 0
		for _, c := range calls {
			if c == "explore" {
				n++
			}
		}
		return n
	}

	// before 10:00 — no strength ops at all
	t.Run("trước 10h ngủ", func(t *testing.T) {
		defer at(9, 0)()
		f := fresh()
		f.eg.PlayerData.Strength = 20
		swPlayTick(f, 1)
		if n := explores(f.calls); n != 0 {
			t.Fatalf("explores = %d, want 0 before the day opens", n)
		}
		if len(f.nextRegions) != 0 {
			t.Fatalf("nextRegions = %v, want none before the day opens", f.nextRegions)
		}
	})

	// farm window: floor 3 with the condition met — XẢ HẾT tl at floor 3,
	// never jump to 4 ("nằm im" is about the jump, fighting continues)
	t.Run("sáng xả sạch ở tầng 3, không lên 4", func(t *testing.T) {
		defer at(10, 15)()
		f := fresh()
		f.eg.PlayerData.Strength = 20
		var q []*proto.SeaWarExploreResp
		for s := int32(17); s >= 2; s -= 3 {
			q = append(q, &proto.SeaWarExploreResp{Ret: 0,
				PlayerData: &proto.SeaWarPlayerDataMsg{Strength: s, RegionId: 10003, EventCompleteVec: met}})
		}
		f.exploreQ = q
		swPlayTick(f, 1)
		if n := explores(f.calls); n < 5 {
			t.Fatalf("explores = %d, want a full dump of 20 tl at floor 3", n)
		}
		if len(f.nextRegions) != 0 {
			t.Fatalf("nextRegions = %v, want none — floor 4 waits until 16:30", f.nextRegions)
		}
	})

	// farm window below floor 3 — climbing still runs (1→2→3 earned freely)
	t.Run("sáng vẫn leo 1 lên 2", func(t *testing.T) {
		defer at(10, 15)()
		f := fresh()
		f.eg.PlayerData = &proto.SeaWarPlayerDataMsg{Strength: 0, RegionId: 10001,
			EventCompleteVec: []*proto.SeaWarEventCompleteDataMsg{{EventId: 10001, Num: 1}}}
		f.nextRegionResp = &proto.SeaWarNextRegionResp{Ret: 0,
			PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 0, RegionId: 10002}}
		swPlayTick(f, 1)
		if len(f.nextRegions) != 1 || f.nextRegions[0] != 10002 {
			t.Fatalf("nextRegions = %v, want [10002] — morning climb allowed", f.nextRegions)
		}
	})

	// BLIND SPAM LAW, farm window: fight → press → server accepts → pd
	// advances → keep fighting on the NEW floor; the moment floor 3 is
	// reached parkedNow parks the account (no 10004 press before 16:30).
	// The climb is earned by fighting — no condition is ever read locally.
	t.Run("sáng spam nút leo lên tầng 3 rồi nằm", func(t *testing.T) {
		defer at(10, 15)()
		f := fresh()
		f.eg.PlayerData = &proto.SeaWarPlayerDataMsg{Strength: 6, RegionId: 10001,
			EventCompleteVec: []*proto.SeaWarEventCompleteDataMsg{{EventId: 10001, Num: 0}}}
		f.exploreQ = []*proto.SeaWarExploreResp{
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 3, RegionId: 10001}},
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 3, RegionId: 10002}},
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 0, RegionId: 10003}},
		}
		f.nextRegionResp = nil
		f.nextQ = []*proto.SeaWarNextRegionResp{
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 3, RegionId: 10002}},
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 3, RegionId: 10003}},
		}
		swPlayTick(f, 1)
		if n := explores(f.calls); n != 3 {
			t.Fatalf("explores = %d, want 3 (fight on 10001, 10002, 10003)", n)
		}
		if len(f.nextRegions) != 2 || f.nextRegions[0] != 10002 || f.nextRegions[1] != 10003 {
			t.Fatalf("nextRegions = %v, want [10002 10003] — one press per fight, park at floor 3", f.nextRegions)
		}
		if f.nextUseItem {
			t.Fatal("floors 1→2→3 are monsters-only: never press the stone door")
		}
	})

	// BLIND SPAM LAW: the climb fires in the SAME tick as the fight that
	// preceded it — no condition read anywhere, server accepts, pd advances.
	t.Run("sáng leo giữa chuỗi từ tầng 1", func(t *testing.T) {
		defer at(10, 15)()
		f := fresh()
		f.eg.PlayerData = &proto.SeaWarPlayerDataMsg{Strength: 6, RegionId: 10001,
			EventCompleteVec: []*proto.SeaWarEventCompleteDataMsg{{EventId: 10001, Num: 0}}}
		f.exploreQ = []*proto.SeaWarExploreResp{
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 3, RegionId: 10001,
				EventCompleteVec: []*proto.SeaWarEventCompleteDataMsg{{EventId: 10001, Num: 1}}}},
		}
		f.nextQ = []*proto.SeaWarNextRegionResp{{Ret: 0,
			PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 3, RegionId: 10002}}}
		swPlayTick(f, 1)
		if len(f.nextRegions) != 1 || f.nextRegions[0] != 10002 {
			t.Fatalf("nextRegions = %v, want [10002] same tick", f.nextRegions)
		}
	})

	// "trước 4h30 chiều chỉ được nằm max ở tầng 3": even an account pushed
	// to floor 4 manually (cond "0" ⇒ free door always open) must NOT be
	// jumped further by the bot before 16:30
	t.Run("trước 16h30 cấm nhảy từ tầng ≥3", func(t *testing.T) {
		defer at(10, 15)()
		f := fresh()
		f.eg.PlayerData = &proto.SeaWarPlayerDataMsg{Strength: 0, RegionId: 10004}
		swPlayTick(f, 1)
		if len(f.nextRegions) != 0 {
			t.Fatalf("nextRegions = %v, want none — floor ≥3 is a no-jump zone before 16:30", f.nextRegions)
		}
	})

	// "từ 10h30 đến 16h30 tuyệt đối không tiêu thể lực" (owner 2026-11-11):
	// noon, tl full, condition met — NOT a single explore fires, no jump
	// out of floor 3 either.
	t.Run("giữa ngày cấm tiêu tl", func(t *testing.T) {
		defer at(12, 0)()
		f := fresh()
		f.eg.PlayerData.Strength = 20
		swPlayTick(f, 1)
		if n := explores(f.calls); n != 0 {
			t.Fatalf("explores = %d, want 0 — 10:30–16:29 is an absolute strength ban", n)
		}
		if len(f.nextRegions) != 0 {
			t.Fatalf("nextRegions = %v, want none (floor 3 parks until 16:30)", f.nextRegions)
		}
	})

	// hold window below floor 3: still no explores, but a met condition may
	// free-jump (a jump costs no strength — the ban is on tiêu thể lực)
	t.Run("giữa ngày không đánh nhưng được nhảy free", func(t *testing.T) {
		defer at(15, 0)()
		f := fresh()
		f.eg.PlayerData = &proto.SeaWarPlayerDataMsg{Strength: 9, RegionId: 10001,
			EventCompleteVec: []*proto.SeaWarEventCompleteDataMsg{{EventId: 10001, Num: 1}}}
		f.nextRegionResp = &proto.SeaWarNextRegionResp{Ret: 0,
			PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 9, RegionId: 10002}}
		swPlayTick(f, 1)
		if n := explores(f.calls); n != 0 {
			t.Fatalf("explores = %d, want 0 — absolute ban covers fighting too", n)
		}
		if len(f.nextRegions) != 1 || f.nextRegions[0] != 10002 {
			t.Fatalf("nextRegions = %v, want [10002] — free climb costs no tl", f.nextRegions)
		}
	})

	// dump window, condition already met: the blind law probes ONE explore
	// (server refuses it — exhausted/closed is the server's call), then the
	// post-chain press climbs. No local condition read anywhere.
	t.Run("sau 16h30 đủ điều kiện là lên ngay", func(t *testing.T) {
		defer at(17, 0)()
		f := fresh()
		swPlayTick(f, 1)
		if len(f.nextRegions) != 1 || f.nextRegions[0] != 10004 {
			t.Fatalf("nextRegions = %v, want [10004] the moment the window opens", f.nextRegions)
		}
		if f.nextUseItem {
			t.Fatal("free door is pressed first — no stone before a free refusal")
		}
		if n := explores(f.calls); n != 1 {
			t.Fatalf("explores = %d, want 1 blind probe — server decides, not a local condition read", n)
		}
	})

	// BLIND SPAM LAW mid-chain: ONE fight at floor 3 → press immediately in
	// the same chain, server accepts, the fresh pd parks the ladder (its
	// returned strength is 0 → the chain stops on the server's number, not
	// on any local condition read).
	t.Run("đánh xong là bấm nút cùng nhịp", func(t *testing.T) {
		defer at(17, 0)()
		f := fresh()
		f.eg.PlayerData.Strength = 6
		f.eg.PlayerData.EventCompleteVec = []*proto.SeaWarEventCompleteDataMsg{{EventId: 10009, Num: 1}}
		f.exploreQ = []*proto.SeaWarExploreResp{
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 3, RegionId: 10003, EventCompleteVec: met}},
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 0, RegionId: 10003, EventCompleteVec: met}},
		}
		swPlayTick(f, 1)
		if n := explores(f.calls); n != 1 {
			t.Fatalf("explores = %d, want exactly 1 — stop the instant the condition lands", n)
		}
		if len(f.nextRegions) != 1 || f.nextRegions[0] != 10004 {
			t.Fatalf("nextRegions = %v, want [10004] same tick", f.nextRegions)
		}
	})
}

// ─── scripted tick ───────────────────────────────────────────────────────────

type fakeSw struct {
	calls []string
	// scripted answers
	enter       *proto.SeaWarEnterResp
	eg          *proto.SeaWarEnterGameResp
	exploreQ    []*proto.SeaWarExploreResp // popped in order; empty → ret=1
	handles     []func(actID, evID, action int32, id int64) *proto.SeaWarHandleEventResp
	handleCalls []struct {
		evID, action int32
		id           int64
	}
	welfare        *proto.SeaWarUnionWelfareResp
	nextRegionResp *proto.SeaWarNextRegionResp
	// nextQ, when set, is popped per swNextRegion call — scripts an
	// ascending climb (each press lands the account one floor higher),
	// falling back to nextRegionResp when drained.
	nextQ []*proto.SeaWarNextRegionResp

	worship          int
	welfareReward    int
	liquids          []int64
	condSweeps       int
	sends            []int32
	sendRetQ         []int32
	liquidItems      []int32
	pausedUntil      time.Time
	donatePauses     int
	hearts           int
	nextRegions      []int32
	nextUseItem      bool
	exhaustNotices   []int32
	cfg              swCfgView
	worshipDay, exhK string

	// assist + shop doubles
	selfID      int64
	assist      *proto.SeaWarAssistListResp
	assistCalls []struct{ typ int32 }
	assistFull  []struct {
		typ        int32
		target, id int64
	}
	rewardCalls []struct {
		typ int32
		id  int64
	}
	mallRows    []*proto.ActivityMallTempMsg
	mallBought  map[int32]int64
	mallPulls   int
	buys        []int32
	mallAdds    map[int32]int64
	shopNotices []string
	refuseRows  map[int32]bool
	failed      map[int32]bool
	shopFails   []int32
}

func (f *fakeSw) swSelf() int64 { return f.selfID }
func (f *fakeSw) swAssistList(int32) (*proto.SeaWarAssistListResp, error) {
	return f.assist, nil
}
func (f *fakeSw) swAssist(_ int32, typ int32, target, id int64) (*proto.SeaWarAssistRsp, error) {
	f.assistCalls = append(f.assistCalls, struct{ typ int32 }{typ})
	f.assistFull = append(f.assistFull, struct {
		typ        int32
		target, id int64
	}{typ, target, id})
	return &proto.SeaWarAssistRsp{Ret: 0}, nil
}
func (f *fakeSw) swAssistReward(_ int32, typ int32, id int64) (*proto.SeaWarAssistRewardRsp, error) {
	f.rewardCalls = append(f.rewardCalls, struct {
		typ int32
		id  int64
	}{typ, id})
	return &proto.SeaWarAssistRewardRsp{Ret: 0}, nil
}
func (f *fakeSw) swMall() ([]*proto.ActivityMallTempMsg, map[int32]int64) {
	merged := map[int32]int64{}
	for id, n := range f.mallBought {
		merged[id] = n
	}
	for id, n := range f.mallAdds {
		merged[id] += n
	}
	return append([]*proto.ActivityMallTempMsg{}, f.mallRows...), merged
}
func (f *fakeSw) swPullMall() bool {
	f.mallPulls++
	return f.mallRows != nil
}
func (f *fakeSw) swBuyMall(_ int32, mallID int32) (*proto.RspBuyActivityGoods, error) {
	f.buys = append(f.buys, mallID)
	if f.refuseRows[mallID] {
		return &proto.RspBuyActivityGoods{Ret: 307}, nil
	}
	return &proto.RspBuyActivityGoods{Ret: 0}, nil
}
func (f *fakeSw) swMarkMallBought(mallID int32) {
	if f.mallAdds == nil {
		f.mallAdds = map[int32]int64{}
	}
	f.mallAdds[mallID]++
}
func (f *fakeSw) swShopNotify(text string) { f.shopNotices = append(f.shopNotices, text) }
func (f *fakeSw) swShopReady(mallID int32) bool {
	return !f.failed[mallID]
}
func (f *fakeSw) swMarkShopFail(mallID int32) {
	f.shopFails = append(f.shopFails, mallID)
	if f.failed == nil {
		f.failed = map[int32]bool{}
	}
	f.failed[mallID] = true
}

func (f *fakeSw) swName() string           { return "test" }
func (f *fakeSw) swLog(_ string, _ ...any) {}
func (f *fakeSw) swEnter(int32) (*proto.SeaWarEnterResp, error) {
	f.calls = append(f.calls, "enter")
	return f.enter, nil
}
func (f *fakeSw) swEnterGame(int32) (*proto.SeaWarEnterGameResp, error) {
	f.calls = append(f.calls, "entergame")
	return f.eg, nil
}
func (f *fakeSw) swExplore(int32) (*proto.SeaWarExploreResp, error) {
	f.calls = append(f.calls, "explore")
	if len(f.exploreQ) == 0 {
		return &proto.SeaWarExploreResp{Ret: 1}, nil // refuse
	}
	r := f.exploreQ[0]
	f.exploreQ = f.exploreQ[1:]
	return r, nil
}
func (f *fakeSw) swHandle(actID, evID, action int32, id int64) (*proto.SeaWarHandleEventResp, error) {
	f.handleCalls = append(f.handleCalls, struct {
		evID, action int32
		id           int64
	}{evID, action, id})
	i := len(f.handleCalls) - 1
	if i < len(f.handles) {
		return f.handles[i](actID, evID, action, id), nil
	}
	return &proto.SeaWarHandleEventResp{Ret: 1}, nil
}
func (f *fakeSw) swWorship(int32) { f.worship++; f.calls = append(f.calls, "worship") }
func (f *fakeSw) swWelfare(int32) (*proto.SeaWarUnionWelfareResp, error) {
	f.calls = append(f.calls, "welfare")
	return f.welfare, nil
}
func (f *fakeSw) swWelfareReward(int32) { f.welfareReward++ }
func (f *fakeSw) swCondSweep(_ int32)   { f.condSweeps++ }

// donate: sendRetQ pops per press; exhausted = 101 (bag empty) so the
// default fake stops after the scripted successes.
func (f *fakeSw) swSendLiquid(_ int32, itemID int32) (int32, error) {
	f.sends = append(f.sends, itemID)
	if len(f.sendRetQ) > 0 {
		r := f.sendRetQ[0]
		f.sendRetQ = f.sendRetQ[1:]
		return r, nil
	}
	return 101, nil
}

func (f *fakeSw) swLiquidItemIDs() []int32 {
	if len(f.liquidItems) > 0 {
		return f.liquidItems
	}
	return []int32{105531}
}

func (f *fakeSw) swDonatePaused(now time.Time) bool { return now.Before(f.pausedUntil) }
func (f *fakeSw) swDonatePause(until time.Time) {
	f.pausedUntil = until
	f.donatePauses++
}

func (f *fakeSw) swReceiveLiquid(_ int32, id int64) {
	f.liquids = append(f.liquids, id)
}
func (f *fakeSw) swHeartBeat(int32) { f.hearts++ }
func (f *fakeSw) swNextRegion(_ int32, next int32, useItem bool) (*proto.SeaWarNextRegionResp, error) {
	f.calls = append(f.calls, "next")
	f.nextRegions = append(f.nextRegions, next)
	f.nextUseItem = useItem
	if len(f.nextQ) > 0 {
		r := f.nextQ[0]
		f.nextQ = f.nextQ[1:]
		return r, nil
	}
	return f.nextRegionResp, nil
}
func (f *fakeSw) swCfg() swCfgView { return f.cfg }
func (f *fakeSw) swMarkWorship(day string) bool {
	if f.worshipDay == day {
		return false
	}
	f.worshipDay = day
	return true
}
func (f *fakeSw) swMarkExhaust(key string) bool {
	if f.exhK == key {
		return false
	}
	f.exhK = key
	return true
}
func (f *fakeSw) swExhausted(region int32) { f.exhaustNotices = append(f.exhaustNotices, region) }

// Owner law 2026-11-11: "nếu đánh thua thì gọi đồng đội hỗ trợ" — a failed
// kill raises the union mark through the client's btn_mark flow:
// REQUEST_MARK (action 4) then MARK (action 1), MARK only after REQUEST_MARK
// ret==0, an already-marked target (assistId≠0) is never re-marked, and a
// refused mark never skips the event.
func TestSwPvPMarkOnLoss(t *testing.T) {
	pinSwNow(t)
	mk := func(pl ...*proto.SeaWarEvent_ChallengePlayerData) []byte {
		b, err := pb.Marshal(&proto.SeaWarEvent_ChallengePlayer{PlayerData: pl})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	resp := func(rsps ...*proto.SeaWarHandleEventResp) []func(a, e, ac int32, id int64) *proto.SeaWarHandleEventResp {
		out := make([]func(a, e, ac int32, id int64) *proto.SeaWarHandleEventResp, len(rsps))
		for i, r := range rsps {
			r := r
			out[i] = func(a, e, ac int32, id int64) *proto.SeaWarHandleEventResp { return r }
		}
		return out
	}
	weak := func(hp string, hadAtk bool, assist int64) *proto.SeaWarEvent_ChallengePlayerData {
		return &proto.SeaWarEvent_ChallengePlayerData{
			PlayerId: 22, FightValue: "90", NickName: "weak", Hp: hp, MaxHp: "900",
			IsHadAtk: hadAtk, AssistId: assist,
		}
	}
	newF := func(info []byte, handles []func(a, e, ac int32, id int64) *proto.SeaWarHandleEventResp) *fakeSw {
		return &fakeSw{
			enter: &proto.SeaWarEnterResp{Ret: 0},
			eg: &proto.SeaWarEnterGameResp{Ret: 0,
				PlayerData:   &proto.SeaWarPlayerDataMsg{Strength: 0, RegionId: 1},
				CurEventData: &proto.SeaWarEventDataMsg{EventType: swEvPlayer, EventId: 60, EventInfo: info}},
			welfare: &proto.SeaWarUnionWelfareResp{Ret: 1},
			handles: handles,
			cfg:     swCfgView{regionCond: map[int32]string{1: ""}},
		}
	}
	type hcall = struct {
		evID, action int32
		id           int64
	}
	want := func(got []hcall, want []hcall) string {
		if len(got) != len(want) {
			return fmt.Sprintf("handles = %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				return fmt.Sprintf("handle[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
		return ""
	}

	// THUA: challenge answers the same event with the target alive (hp 500)
	// → request-mark + mark fire, then the lone-hit rule skips the event.
	alive := newF(mk(weak("900", false, 0)), resp(
		&proto.SeaWarHandleEventResp{Ret: 0, EventData: &proto.SeaWarEventDataMsg{EventType: swEvPlayer, EventId: 60,
			EventInfo: mk(weak("500", true, 0))}}, // challenge: target survives
		&proto.SeaWarHandleEventResp{Ret: 0}, // REQUEST_MARK ok
		&proto.SeaWarHandleEventResp{Ret: 0}, // MARK ok
		&proto.SeaWarHandleEventResp{Ret: 0}, // lone-hit skip accepted
	))
	swPlayTick(alive, 1)
	if msg := want(alive.handleCalls, []hcall{
		{60, swActionChallenge, 22}, {60, swActionRequestMark, 0}, {60, swActionMark, 0},
		{60, swActionSkip, 0}, // lone opponent isHadAtk → client PASS
	}); msg != "" {
		t.Fatal(msg)
	}

	// THẮNG: no eventData back = target killed — no mark, event done.
	win := newF(mk(weak("900", false, 0)), resp(
		&proto.SeaWarHandleEventResp{Ret: 0}, // challenge accepted, event finished
	))
	swPlayTick(win, 1)
	if msg := want(win.handleCalls, []hcall{{60, swActionChallenge, 22}}); msg != "" {
		t.Fatal(msg)
	}

	// ĐÃ GỌI rồi (assistId≠0): thua nhưng không đánh dấu lại.
	marked := newF(mk(weak("900", false, 77)), resp(
		&proto.SeaWarHandleEventResp{Ret: 0, EventData: &proto.SeaWarEventDataMsg{EventType: swEvPlayer, EventId: 60,
			EventInfo: mk(weak("500", true, 77))}},
		&proto.SeaWarHandleEventResp{Ret: 0}, // lone-hit skip accepted
	))
	swPlayTick(marked, 1)
	if msg := want(marked.handleCalls, []hcall{
		{60, swActionChallenge, 22}, {60, swActionSkip, 0},
	}); msg != "" {
		t.Fatal(msg)
	}

	// REQUEST_MARK bị từ chối → KHÔNG gửi MARK, và event KHÔNG bị skip
	// (mark fail chỉ log).
	refused := newF(mk(weak("900", false, 0)), resp(
		&proto.SeaWarHandleEventResp{Ret: 0, EventData: &proto.SeaWarEventDataMsg{EventType: swEvPlayer, EventId: 60,
			EventInfo: mk(weak("500", true, 0))}},
		&proto.SeaWarHandleEventResp{Ret: 9}, // REQUEST_MARK refused
		&proto.SeaWarHandleEventResp{Ret: 0}, // lone-hit skip accepted (mark fail must NOT skip the event)
	))
	swPlayTick(refused, 1)
	if msg := want(refused.handleCalls, []hcall{
		{60, swActionChallenge, 22}, {60, swActionRequestMark, 0}, {60, swActionSkip, 0},
	}); msg != "" {
		t.Fatal(msg)
	}
}

func okHandles(n int) []func(a, e, ac int32, id int64) *proto.SeaWarHandleEventResp {
	out := make([]func(a, e, ac int32, id int64) *proto.SeaWarHandleEventResp, n)
	for i := range out {
		out[i] = func(a, e, ac int32, id int64) *proto.SeaWarHandleEventResp {
			return &proto.SeaWarHandleEventResp{Ret: 0}
		}
	}
	return out
}

// pinSwNow fixes the engine clock into the farm window so tick tests are
// deterministic no matter what hour the suite actually runs at (the 2026-11-11
// rhythm puts strength ops to sleep before 10:00 ICT).
func pinSwNow(t *testing.T) {
	old := swNowFunc
	fixed := time.Date(2026, 11, 11, 10, 15, 0, 0, ictZone)
	swNowFunc = func() time.Time { return fixed }
	t.Cleanup(func() { swNowFunc = old })
}

func TestSwPlayTickFlow(t *testing.T) {
	pinSwNow(t)
	forgeInfo, err := pb.Marshal(&proto.SeaWarEvent_ForgeGem{BallList: []*proto.SeaWarBuffBallMsg{
		{Uid: 7, ConfigId: 500}, {Uid: 8, ConfigId: 501},
	}})
	if err != nil {
		t.Fatal(err)
	}
	pvpInfo, err := pb.Marshal(&proto.SeaWarEvent_ChallengePlayer{PlayerData: []*proto.SeaWarEvent_ChallengePlayerData{
		{PlayerId: 11, FightValue: "999999999", NickName: "big"},
		{PlayerId: 12, FightValue: "100", NickName: "small"},
		{PlayerId: 13, FightValue: "50", NickName: "tiny", IsHadAtk: true}, // already hit
	}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSw{
		enter: &proto.SeaWarEnterResp{Ret: 0, CanWorship: true},
		eg: &proto.SeaWarEnterGameResp{Ret: 0,
			PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 5, RegionId: 1},
			CurEventData: &proto.SeaWarEventDataMsg{ // pending gift → action=2
				EventType: swEvGift, EventId: 40}},
		exploreQ: []*proto.SeaWarExploreResp{
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 4, RegionId: 1},
				EventData: &proto.SeaWarEventDataMsg{EventType: swEvForge, EventId: 41, EventInfo: forgeInfo}},
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 3, RegionId: 1},
				EventData: &proto.SeaWarEventDataMsg{EventType: swEvPlayer, EventId: 42, EventInfo: pvpInfo}},
			{Ret: 0, PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 0, RegionId: 1}},
		},
		handles: okHandles(3),
		welfare: &proto.SeaWarUnionWelfareResp{Ret: 0, CanReceive: 1,
			NoticeInfo: []*proto.SeaWarUnionWelfareNoticeInfo{
				{Id: 91}, {Id: 92},
				{Id: 93, Status: 1}, // already claimed — client grays it, never claim
			}},
		nextRegionResp: &proto.SeaWarNextRegionResp{Ret: 1},
		cfg: swCfgView{
			ballEffect:  map[int32]int32{500: 1, 501: 20017},
			ballType:    map[int32]int32{500: 1, 501: 1},
			regionCond:  map[int32]string{1: ""},
			helpBoxNeed: 3,
		},
		selfID: 1000,
		assist: &proto.SeaWarAssistListResp{Ret: 0, Data: []*proto.SeaWarAssistData{
			// teammate fight still breathing → join the assist
			{Type: swAssistFight, AssistInfos: &proto.SeaWarAssistMsg{AssistId: 7001, PlayerId: 2001, Hp: "500", NickName: "a"}},
			// MY fight finished unclaimed → claim (never assist myself)
			{Type: swAssistFight, AssistInfos: &proto.SeaWarAssistMsg{AssistId: 7002, PlayerId: 1000, Hp: "0"}},
			// teammate fight, hp missing → NOT finished (client: t.hp && ≤0) → assist
			{Type: swAssistFight, AssistInfos: &proto.SeaWarAssistMsg{AssistId: 7003, PlayerId: 2002, Hp: ""}},
			// teammate box 2/3 helpers → one help tap
			{Type: swAssistBox, HelpBoxInfos: &proto.SeaWarHelpBoxMsg{AssistId: 8001, PlayerId: 2003,
				HelpUsers: []*proto.SeaWarPlayerHeadRef{{}, {}}}},
			// MY box filled unclaimed → claim
			{Type: swAssistBox, HelpBoxInfos: &proto.SeaWarHelpBoxMsg{AssistId: 8002, PlayerId: 1000,
				HelpUsers: []*proto.SeaWarPlayerHeadRef{{}, {}, {}}}},
		}},
		mallRows: []*proto.ActivityMallTempMsg{
			{Id: pb.Int32(1), Name: pb.String("Phúc Lợi Miễn Phí"), Price: pb.String("0"), BuyLimit: pb.Int32(1)},
			{Id: pb.Int32(2), Name: pb.String("Gói Video Cổ Vũ"), Price: pb.String("0"), Param: pb.String("3|1800|0|0"), BuyLimit: pb.Int32(2)},
			{Id: pb.Int32(3), Name: pb.String("Gói Tiên Ngọc"), Price: pb.String("100000=TienNgoc"), BuyLimit: pb.Int32(1)},
			{Id: pb.Int32(4), Name: pb.String("Nạp Thẻ"), Price: pb.String("299000=NgocRubis"), BuyLimit: pb.Int32(5)}, // money — NEVER
		},
	}
	swPlayTick(f, 9063858)

	// gift skipped first (pending board event)
	if len(f.handleCalls) < 3 {
		t.Fatalf("handle calls = %d, want ≥3", len(f.handleCalls))
	}
	if c := f.handleCalls[0]; c.evID != 40 || c.action != swActionSkip {
		t.Fatalf("gift handle = %+v, want ev 40 action 2", c)
	}
	// forge picked the FINAL-DAMAGE ball uid 8 (configId 501 → effect 20017)
	if c := f.handleCalls[1]; c.evID != 41 || c.action != 0 || c.id != 8 {
		t.Fatalf("forge handle = %+v, want ev 41 id 8", c)
	}
	// PVP attacked the weakest OVERALL opponent (13, "50") — even though
	// isHadAtk — not 12 ("100"), not 11 (owner 2026-11-11 "cứ gặp người là
	// pick người yếu nhất đánh" = client AUTO_SELECT_MIN_POWER).
	if c := f.handleCalls[2]; c.evID != 42 || c.action != swActionChallenge || c.id != 13 {
		t.Fatalf("pvp handle = %+v, want ev 42 action 3 id 13", c)
	}
	if f.worship != 1 {
		t.Fatalf("worship = %d, want 1 (canWorship + once/day)", f.worship)
	}
	if f.welfareReward != 1 {
		t.Fatalf("welfareReward = %d, want 1 (canReceive>0)", f.welfareReward)
	}
	// "Nhận nhanh" claim-all (owner 2026-11-11): ONE id=0 press per tick —
	// never per-notice ids (the old 91/92 loop never consumed anything).
	if len(f.liquids) != 1 || f.liquids[0] != 0 {
		t.Fatalf("liquids = %v, want [0] (claim-all once per tick)", f.liquids)
	}
	if f.condSweeps != 1 {
		t.Fatalf("condSweeps = %d, want 1 (nạp-bang sweep every tick; cadence lives in the twins)", f.condSweeps)
	}
	// PHÁT linh lộ: blind presses until the first refusal — fake answers a
	// single ret=0 then 101 → exactly one item spent, loop stopped.
	if len(f.sends) != 1 || f.sends[0] != 105531 {
		t.Fatalf("sends = %v, want [105531] (phát until server stop)", f.sends)
	}
	// BLIND SPAM LAW: one free press after EVERY successful explore (3 here
	// — the scripted queue serves three fights); the fake's static resp has
	// no playerData so the floor never actually moves (refreshPD keeps the
	// scripted eg), hence every press targets region 2. No stone below
	// floor 3, ever.
	if len(f.nextRegions) != 3 || f.nextRegions[0] != 2 || f.nextRegions[1] != 2 || f.nextRegions[2] != 2 {
		t.Fatalf("nextRegions = %v, want [2 2 2] — one free press per fight", f.nextRegions)
	}
	if f.nextUseItem {
		t.Fatal("free advance must send useItem=false")
	}
	if f.hearts != 1 {
		t.Fatalf("hearts = %d, want 1", f.hearts)
	}
	// strength hit 0 and the chain stopped on refusal (budget not hit) →
	// exactly one exhaustion notice for this day+region.
	if len(f.exhaustNotices) != 1 || f.exhaustNotices[0] != 1 {
		t.Fatalf("exhaustNotices = %v, want [1]", f.exhaustNotices)
	}
	// Assists: teammate fight 7001, teammate fight with missing hp 7003
	// (NOT finished), teammate short box 8001 — my own entries (7002/8002)
	// are never assisted.
	wantAssist := []struct {
		typ        int32
		target, id int64
	}{{swAssistFight, 2001, 7001}, {swAssistFight, 2002, 7003}, {swAssistBox, 2003, 8001}}
	if len(f.assistFull) != len(wantAssist) {
		t.Fatalf("assist calls = %+v, want %+v", f.assistFull, wantAssist)
	}
	for i, w := range wantAssist {
		if f.assistFull[i] != w {
			t.Fatalf("assist[%d] = %+v, want %+v", i, f.assistFull[i], w)
		}
	}
	// Claims: my finished fight 7002 (hp "0") + my filled box 8002.
	if len(f.rewardCalls) != 2 || f.rewardCalls[0] != (struct {
		typ int32
		id  int64
	}{swAssistFight, 7002}) || f.rewardCalls[1] != (struct {
		typ int32
		id  int64
	}{swAssistBox, 8002}) {
		t.Fatalf("rewardCalls = %+v, want [{0 7002} {1 8002}]", f.rewardCalls)
	}
	// Shop: free 1×, ad 2×, jade 1× — paid row never bought; rows were
	// pre-folded so no extra 21003 pull; 4 success alerts.
	wantBuys := []int32{1, 2, 2, 3}
	if len(f.buys) != len(wantBuys) {
		t.Fatalf("buys = %v, want %v", f.buys, wantBuys)
	}
	for i, w := range wantBuys {
		if f.buys[i] != w {
			t.Fatalf("buys = %v, want %v", f.buys, wantBuys)
		}
	}
	if f.mallPulls != 0 {
		t.Fatalf("mallPulls = %d, want 0 (rows already folded)", f.mallPulls)
	}
	if len(f.shopNotices) != 4 {
		t.Fatalf("shopNotices = %v, want 4", f.shopNotices)
	}
	// Second tick same day: worship + exhaust notices deduped, daily shop
	// caps already met (no re-buy), assists re-fire blind (server arbitrates).
	f.enter.CanWorship = true
	swPlayTick(f, 9063858)
	if f.worship != 1 || len(f.exhaustNotices) != 1 {
		t.Fatalf("day dedup broken: worship=%d exhaust=%v", f.worship, f.exhaustNotices)
	}
	if len(f.buys) != 4 {
		t.Fatalf("second tick re-bought past daily cap: %v", f.buys)
	}
	if len(f.assistCalls) != 6 {
		t.Fatalf("assist blind-fire = %d, want 6 (2 ticks × 3)", len(f.assistCalls))
	}
}

// Wake bell: a 234913 push cuts the in-window sleep short, but the floor
// swMinTick keeps pushes from hot-looping the engine.
func TestSwWakeBell(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Player{Username: "t", ctx: ctx}
	// No wake: full sleep.
	start := time.Now()
	if !p.swSleepPoll(60*time.Millisecond, start.Add(-swMinTick)) {
		t.Fatal("sleep died without reason")
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("returned early without a wake")
	}
	// Wake pending (push arrived during the tick): sleep returns fast.
	p.swPingWake()
	start = time.Now()
	if !p.swSleepPoll(30*time.Second, start.Add(-swMinTick)) {
		t.Fatal("sleep died without reason")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("wake not honored: slept %v", d)
	}
	// Floor: wake right at tick start still waits out swMinTick.
	p.swPingWake()
	start = time.Now()
	if !p.swSleepPoll(30*time.Second, start) {
		t.Fatal("sleep died without reason")
	}
	if d := time.Since(start); d < swMinTick/2 {
		t.Fatalf("hot-loop: woke after %v (< floor/2)", d)
	}
	cancel()
	if p.swSleepPoll(time.Second, time.Now()) {
		t.Fatal("want false after cancel")
	}
}

// A refused row (ladder lock ret=307 live) rests swShopFailWait instead of
// being re-fired every tick; other rows keep buying.
func TestSwShopFailSuppress(t *testing.T) {
	f := &fakeSw{
		mallRows: []*proto.ActivityMallTempMsg{
			{Id: pb.Int32(1), Name: pb.String("Phúc Lợi Miễn Phí"), Price: pb.String("0"), BuyLimit: pb.Int32(1)},
			{Id: pb.Int32(2), Name: pb.String("Ưu Đãi Phúc Lợi 2"), Price: pb.String("100000=0"), Param: pb.String("8|0|0|0"), BuyLimit: pb.Int32(1)},
		},
		refuseRows: map[int32]bool{2: true},
	}
	swShopSweep(f, 9063858)
	if len(f.buys) != 2 || f.buys[0] != 1 || f.buys[1] != 2 {
		t.Fatalf("buys = %v, want [1 2]", f.buys)
	}
	if len(f.shopFails) != 1 || f.shopFails[0] != 2 {
		t.Fatalf("shopFails = %v, want [2]", f.shopFails)
	}
	// Ticks 2+ same day: the refused ladder rung rests, no new frames for it.
	swShopSweep(f, 9063858)
	swShopSweep(f, 9063858)
	if len(f.buys) != 2 || len(f.shopFails) != 1 {
		t.Fatalf("refused row re-fired: buys=%v shopFails=%v", f.buys, f.shopFails)
	}
	if len(f.shopNotices) != 1 {
		t.Fatalf("shopNotices = %v, want 1 (only the free pack)", f.shopNotices)
	}
}

func TestSwPlayTickRefusalClearsEvent(t *testing.T) {
	f := &fakeSw{
		enter: &proto.SeaWarEnterResp{Ret: 0},
		eg: &proto.SeaWarEnterGameResp{Ret: 0,
			PlayerData: &proto.SeaWarPlayerDataMsg{Strength: 0, RegionId: 1}},
		welfare: &proto.SeaWarUnionWelfareResp{Ret: 1},
	}
	// A monster event that the server refuses: expect handle(3) THEN
	// handle(2 skip) on the same eventId — the chain must not loop.
	f.eg.CurEventData = &proto.SeaWarEventDataMsg{EventType: swEvMonster, EventId: 55}
	f.handles = []func(a, e, ac int32, id int64) *proto.SeaWarHandleEventResp{
		func(a, e, ac int32, id int64) *proto.SeaWarHandleEventResp { // fight refused
			return &proto.SeaWarHandleEventResp{Ret: 7}
		},
		func(a, e, ac int32, id int64) *proto.SeaWarHandleEventResp { // skip accepted
			return &proto.SeaWarHandleEventResp{Ret: 0}
		},
	}
	swPlayTick(f, 1)
	if len(f.handleCalls) != 2 {
		t.Fatalf("handles = %d, want exactly 2 (challenge then skip)", len(f.handleCalls))
	}
	if f.handleCalls[0].action != swActionChallenge || f.handleCalls[1].action != swActionSkip {
		t.Fatalf("actions = %d,%d want 3,2", f.handleCalls[0].action, f.handleCalls[1].action)
	}
	if f.handleCalls[1].evID != 55 {
		t.Fatalf("skip target = %d, want 55", f.handleCalls[1].evID)
	}
}

func TestSwEnterRefusedStopsTick(t *testing.T) {
	f := &fakeSw{enter: &proto.SeaWarEnterResp{Ret: 15001}}
	swPlayTick(f, 1)
	for _, c := range f.calls {
		if c == "explore" || c == "welfare" {
			t.Fatalf("tick proceeded past refused ENTER: %v", f.calls)
		}
	}
}

// TestSwEatAll — the web card's drain loop (owner 2026-11-09): rounds of
// 234922 num=20 on the swRT seat, stopping the moment the SERVER refuses
// (blind-fire law — ret≠0 = bag dry, the bot never counts a bag it does not
// track). Verifies exactly-3 requests (ret0, ret0, ret201 → stop), busy
// release, and mute release. Responder rides swHandleFrameEP like the live
// reader would.
func TestSwEatAll(t *testing.T) {
	p := &EventPlayer{Username: "eat", Running: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	p.writeChan = make(chan []byte, 32)
	nowMs := time.Now().UnixMilli()
	p.ActList.entries = map[int32]activityEntry{
		900349: {ActivityID: 900349, Type: 349, BeginTime: nowMs - 1000, EndTime: nowMs + 60000},
	}
	var got atomic.Int32
	go func() {
		for range p.writeChan {
			n := got.Add(1)
			var rsp proto.SeaWarUseItemResp
			if n == 3 {
				rsp.Ret = 201 // server: hết quả
			}
			data, _ := pb.Marshal(&rsp)
			p.swHandleFrameEP(SERVER_SW_USE_ITEM, data)
		}
	}()
	p.swEatAll()
	// second press while in flight must no-op (busy CAS)
	p.swEatAll()
	deadline := time.Now().Add(20 * time.Second) // > swReqTimeout, so a stuck seat fails loud
	for p.swEatBusy.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.swEatBusy.Load() {
		t.Fatal("eat loop did not finish")
	}
	if n := got.Load(); n != 3 {
		t.Fatalf("requests sent = %d, want exactly 3 (stop at first ret≠0)", n)
	}
	if p.swEatMute.Load() {
		t.Fatal("alert mute left on after the loop")
	}
}

// TestSwReaderDispatchLists pins that every SeaWar answer the auto engine
// round-trips is actually routed by BOTH production readers. A missing
// constant here is the 2026-11-11 phát-linh-lộ bug in disguise: 8865
// (UNION_SEND_LIQUID) was absent from both reader lists, so every donate
// press was dropped by the reader, swRT timed out, and the AUTO donate
// silently never fired — while every live test kept working because each
// test carries its own reader switch (same class as the rbt 03fe miss).
// TestSwDonateLiquidV2 pins the donate-burst law (owner 2026-11-11
// "1 phút phát được có 1 linh lộ, phát cần phát hết luôn"): only 101
// (bag dry) stops instantly; every OTHER refusal is the server's donate
// cooldown — the loop waits and presses on, so a full bag drains within
// the round instead of 1 item per engine tick. A round with zero
// successes parks the path (no per-tail stalling) and while parked the
// path presses nothing at all.
func TestSwDonateLiquidV2(t *testing.T) {
	// 1) bag dry immediately → exactly one probe, no park.
	f := &fakeSw{}
	f.sendRetQ = []int32{101}
	swDonateLiquid(f, 5)
	if len(f.sends) != 1 || f.donatePauses != 0 {
		t.Fatalf("bag-dry: presses=%d pauses=%d, want 1/0", len(f.sends), f.donatePauses)
	}

	// 2) interleaved cooldowns between successes → all presses happen,
	// no park (short refusals never accumulate past the reset).
	f = &fakeSw{}
	f.sendRetQ = []int32{0, 7, 0, 7, 0, 101}
	swDonateLiquid(f, 5)
	if len(f.sends) != 6 || f.donatePauses != 0 {
		t.Fatalf("cooldown-interleave: presses=%d pauses=%d, want 6/0", len(f.sends), f.donatePauses)
	}

	// 3) long cooldown (10 consecutive refusals < 12 tolerated) then the
	// cooldown expires → burst CONTINUES across it.
	f = &fakeSw{}
	f.sendRetQ = []int32{0, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 0, 101}
	swDonateLiquid(f, 5)
	if len(f.sends) != 13 || f.donatePauses != 0 {
		t.Fatalf("long-cooldown: presses=%d pauses=%d, want 13/0", len(f.sends), f.donatePauses)
	}

	// 4) all-refusal round (cap / no union) → stop after 3 probes, park;
	// while parked the path is fully silent.
	f = &fakeSw{}
	f.sendRetQ = []int32{7, 7, 7, 7, 7}
	swDonateLiquid(f, 5)
	if len(f.sends) != 3 || f.donatePauses != 1 || f.pausedUntil.IsZero() {
		t.Fatalf("all-refusal: presses=%d pauses=%d until=%v, want 3/1/set", len(f.sends), f.donatePauses, f.pausedUntil)
	}
	swDonateLiquid(f, 5)
	if len(f.sends) != 3 {
		t.Fatalf("parked path still pressed: %d presses", len(f.sends))
	}

	// 5) sustained refusals WITH earlier successes (hit the union cap
	// mid-burst) → tolerated up to swDonateRefuseTries then park.
	f = &fakeSw{}
	rets := []int32{0}
	for i := 0; i < swDonateRefuseTries; i++ {
		rets = append(rets, 7)
	}
	f.sendRetQ = rets
	swDonateLiquid(f, 5)
	if len(f.sends) != 1+swDonateRefuseTries || f.donatePauses != 1 {
		t.Fatalf("mid-burst-cap: presses=%d pauses=%d, want %d/1",
			len(f.sends), f.donatePauses, 1+swDonateRefuseTries)
	}
}

// TestSwDonateLiquidThreeTypes pins the 3-tier law (owner 2026-11-11
// "có 3 loại linh lộ, mày check cả 3 loại chưa"): 101 only means THAT
// type's bag is dry — the pass must move on to the next tier instead of
// stopping (the old single-item loop never touched 105532/105533 once
// 105531 answered 101).
func TestSwDonateLiquidThreeTypes(t *testing.T) {
	// tier1 dry → tier2 has stock (1 success then dry) → tier3 dry.
	f := &fakeSw{}
	f.liquidItems = []int32{105531, 105532, 105533}
	f.sendRetQ = []int32{101, 0, 101, 101}
	swDonateLiquid(f, 5)
	want := []int32{105531, 105532, 105532, 105533}
	if len(f.sends) != len(want) {
		t.Fatalf("presses=%v, want %v", f.sends, want)
	}
	for i := range want {
		if f.sends[i] != want[i] {
			t.Fatalf("press %d = %d, want %d (whole sends=%v)", i, f.sends[i], want[i], f.sends)
		}
	}
	if f.donatePauses != 0 {
		t.Fatalf("partial stock must NOT park the path, pauses=%d", f.donatePauses)
	}

	// ALL three tiers dry → three probes, no park (nothing is being
	// refused; the account just has no linh lộ).
	f = &fakeSw{}
	f.liquidItems = []int32{105531, 105532, 105533}
	f.sendRetQ = []int32{101, 101, 101}
	swDonateLiquid(f, 5)
	if len(f.sends) != 3 || f.donatePauses != 0 {
		t.Fatalf("all-dry: presses=%v pauses=%d, want 3 probes/0 pauses", f.sends, f.donatePauses)
	}
}

func TestSwReaderDispatchLists(t *testing.T) {
	answers := []string{
		"SERVER_SW_ENTER", "SERVER_SW_ENTER_GAME", "SERVER_SW_EXPLORE",
		"SERVER_SW_HANDLE_EVENT", "SERVER_SW_UNION_WELFARE", "SERVER_SW_UNION_WELFARE_REWARD",
		"SERVER_SW_UNION_RECEIVE_LIQUID", "SERVER_SW_UNION_SEND_LIQUID", "SERVER_SW_UNION_LIQUID_RECORD",
		"SERVER_SW_HEART_BEAT", "SERVER_SW_NEXT_REGION", "SERVER_SW_USE_ITEM", "SERVER_SW_WORSHIP",
		"SERVER_SW_ASSIST_LIST", "SERVER_SW_ASSIST", "SERVER_SW_ASSIST_REWARD",
		"SERVER_SW_ASSIST_SYNC",
		// war family (2026-11-11): the web war-map order seats its
		// round-trips on these — same reader-list law.
		"SERVER_SW_CHALLENGE_LIST", "SERVER_SW_CHALLENGE_INFO_SYNC", "SERVER_SW_DECLARATION_INFO_SYNC",
		"SERVER_SW_ENTER_CHALLENGE", "SERVER_SW_CHALLENGE_INFO", "SERVER_SW_CHALLENGE_BATTLE",
		"SERVER_SW_DECLARATION",
	}
	for _, tc := range []struct{ file, handler string }{
		{"player.go", "p.swHandleFrame(msgType, realData)"},
		{"player_event.go", "p.swHandleFrameEP(msgType, realData)"},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		s := string(src)
		start := strings.Index(s, "case SERVER_SW_ENTER,")
		if start < 0 {
			t.Fatalf("%s: sw dispatch case block not found", tc.file)
		}
		end := strings.Index(s[start:], tc.handler)
		if end < 0 {
			t.Fatalf("%s: sw handler not found", tc.file)
		}
		block := s[start : start+end]
		for _, name := range answers {
			if !strings.Contains(block, name) {
				t.Errorf("%s: reader list missing %s — its swRT would time out silently", tc.file, name)
			}
		}
	}
}

func TestSwEventConstants(t *testing.T) {
	// opcode/const sanity baked into the reader lists (bump together)
	if swActivityType != 349 {
		t.Fatal("type changed?")
	}
	if fmt.Sprint(swEvForge) != "5" || fmt.Sprint(swActionSkip) != "2" {
		t.Fatal("event/action enum drift vs client ECelestialSeaWarEventAction")
	}
}

// TestSwWarPlan pins the war-map attack planner — the LỆNH DIM LAW v2 (owner
// 2026-11-11 "danh sách đánh rồi vẫn hiện nhưng tối màu → response có trường
// đánh rồi" + "tao thấy mày vẫn đánh theo lệnh thứ 2"):
// declarationInfos.challengeTimes COUNTS ATTACKS MADE (client ++ after each
// win), so a row is LIT (đánh được) at challengeTimes==0 and DIM (đã đánh,
// tối màu) once the counter grows — type 1 (cao cấp) is lit while
// times<SEAWAR_HIGH_PVP_TIMES. PRIMARY plan = lit lệnh rows only; dim rows
// NEVER plan. Only with no lit lệnh at all do the blind discovery rows
// (234931 live infos, then 234937 roomInfo defenders) plan. One player is
// never pressed twice; rooms/presses bounded (BLIND-ORDER law: no
// ACCOUNT-level gate, server ret sole arbiter).
func TestSwWarPlan(t *testing.T) {
	mkDecl := func(pid int64, room int32, times int32) *proto.SeaWarDeclarationInfo {
		return &proto.SeaWarDeclarationInfo{
			PlayerBaseDataMsg: &proto.SeaWarPlayerBaseData{PlayerId: pid, NickName: fmt.Sprintf("p%d", pid)},
			ChallengeTimes:    times,
			RoomId:            room,
		}
	}
	mkDef := func(pid int64) *proto.SeaWarBeChallengedInfo {
		return &proto.SeaWarBeChallengedInfo{
			PlayerBaseDataMsg: &proto.SeaWarPlayerBaseData{PlayerId: pid, NickName: fmt.Sprintf("d%d", pid)},
		}
	}
	mkLive := func(pid int64, room int32, target int64) *proto.SeaWarChallengeInfoSync {
		return &proto.SeaWarChallengeInfoSync{
			TargetUnionId: target,
			BeChallengedInfo: &proto.SeaWarBeChallengedInfo{
				RoomId:            room,
				PlayerBaseDataMsg: &proto.SeaWarPlayerBaseData{PlayerId: pid, NickName: fmt.Sprintf("l%d", pid)},
			},
		}
	}

	// 1) lit lệnh rows win outright; the DIM row (times>0, "đã đánh rồi")
	// never plans, and roomInfo rows are NOT mixed in while lit rows exist.
	// v2: lit = times==0, dim = times>0.
	ent := &proto.SeaWarEnterChallengeResp{
		DeclarationInfos: []*proto.SeaWarDeclarationInfo{
			mkDecl(100, 1, 0), mkDecl(101, 1, 2), mkDecl(102, 2, 0), mkDecl(103, 2, 1),
		},
	}
	targets := swWarPlan(ent, map[int32][]*proto.SeaWarBeChallengedInfo{
		1: {mkDef(200)}, 2: {mkDef(201)},
	}, nil, 7100000001, swWarMaxRooms, swWarMaxPress, 0)
	want := []swWarTarget{
		{RoomID: 1, PlayerID: 100, Name: "p100"},
		{RoomID: 2, PlayerID: 102, Name: "p102"},
	}
	if len(targets) != len(want) {
		t.Fatalf("lit-only plan = %+v, want %+v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("target %d = %+v, want %+v", i, targets[i], want[i])
		}
	}

	// 2) all-dim board (every row times>0) → no lit lệnh → blind fallback:
	// live rows first, then roomInfo defenders; the dim declaration rows
	// still never plan.
	entAllDim := &proto.SeaWarEnterChallengeResp{
		DeclarationInfos: []*proto.SeaWarDeclarationInfo{mkDecl(100, 1, 1), mkDecl(101, 1, 2)},
	}
	targets = swWarPlan(entAllDim, map[int32][]*proto.SeaWarBeChallengedInfo{
		1: {mkDef(200)},
	}, []*proto.SeaWarChallengeInfoSync{mkLive(201, 1, 7100000001)}, 7100000001, swWarMaxRooms, swWarMaxPress, 0)
	want = []swWarTarget{
		{RoomID: 1, PlayerID: 201, Name: "l201"},
		{RoomID: 1, PlayerID: 200, Name: "d200"},
	}
	if len(targets) != len(want) {
		t.Fatalf("all-dim fallback = %+v, want %+v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("fallback %d = %+v, want %+v", i, targets[i], want[i])
		}
	}

	// 3) dedupe: the same lit player (times==0) listed twice plans once.
	targets = swWarPlan(&proto.SeaWarEnterChallengeResp{DeclarationInfos: []*proto.SeaWarDeclarationInfo{
		mkDecl(100, 1, 0), mkDecl(100, 1, 0), mkDecl(100, 2, 0),
	}}, nil, nil, 7100000001, swWarMaxRooms, swWarMaxPress, 0)
	if len(targets) != 1 || targets[0].PlayerID != 100 {
		t.Fatalf("dedupe: %+v", targets)
	}

	// 4) caps: rooms past swWarMaxRooms never enter; presses bounded. Rows are
	// lit (times==0) so they reach the cap logic.
	entBig := &proto.SeaWarEnterChallengeResp{}
	for r := int32(1); r <= 9; r++ {
		entBig.DeclarationInfos = append(entBig.DeclarationInfos, mkDecl(int64(300+r), r, 0))
	}
	targets = swWarPlan(entBig, nil, nil, 7100000001, swWarMaxRooms, swWarMaxPress, 0)
	if len(targets) != swWarMaxRooms {
		t.Fatalf("room cap: %+v", targets)
	}
	manyLit := &proto.SeaWarEnterChallengeResp{}
	for i := 0; i < swWarMaxPress+3; i++ {
		manyLit.DeclarationInfos = append(manyLit.DeclarationInfos, mkDecl(int64(400+i), 1, 0))
	}
	targets = swWarPlan(manyLit, nil, nil, 7100000001, swWarMaxRooms, swWarMaxPress, 0)
	if len(targets) != swWarMaxPress {
		t.Fatalf("press cap: %d targets", len(targets))
	}

	// 5) garbage never plans: nil ent, playerId 0, empty everything.
	if swWarPlan(nil, nil, nil, 7100000001, swWarMaxRooms, swWarMaxPress, 0) != nil {
		t.Fatal("nil enter resp must plan nothing")
	}
	entZero := &proto.SeaWarEnterChallengeResp{DeclarationInfos: []*proto.SeaWarDeclarationInfo{mkDecl(0, 1, 5)}}
	if len(swWarPlan(entZero, nil, nil, 7100000001, swWarMaxRooms, swWarMaxPress, 0)) != 0 {
		t.Fatal("playerId 0 rows must be skipped")
	}

	// 6) LIVE ROWS law: with NO declaration rows at all (live probe
	// <live-user>@710132 outside the war window: ENTER ret=0 declarations=0),
	// infos aimed at THIS union plan; other unions / pid 0 never do.
	live := []*proto.SeaWarChallengeInfoSync{
		mkLive(500, 1, 7100000001),
		mkLive(501, 1, 9999999999),
		mkLive(0, 2, 7100000001),
	}
	targets = swWarPlan(&proto.SeaWarEnterChallengeResp{}, nil, live, 7100000001, swWarMaxRooms, swWarMaxPress, 0)
	if len(targets) != 1 || targets[0] != (swWarTarget{RoomID: 1, PlayerID: 500, Name: "l500"}) {
		t.Fatalf("live-infos plan = %+v, want one target room1/500", targets)
	}
}
