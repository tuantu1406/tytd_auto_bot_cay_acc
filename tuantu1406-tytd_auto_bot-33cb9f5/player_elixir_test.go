package tytd_go

// Đan Giới / Elixir engine tests. Golden test pins the wire contract of a
// REAL 8aae frame (testdata/dangioi_8aae.hex, captured by
// TestDanGioiProbeLive). Policy tests script the seat round-trips and assert
// the OWNER FLOW 2026-10-22 ("giờ chỉ làm chứ không nhận"): harvest ripe lots
// EXPLICITLY by landId (oneKey is privilege-card-gated, ret 35520) and cuốc
// each lot the SAME beat as its own GET (no ENTER in between — revival-on-
// read law), then replant free slots ONLY (never cuốc a growing tree): open
// 496 task tree first, all-tasks-full → rarest tree in the bag. NO claims,
// NO order submissions, NO husk sweep in the tick. NEVER water (server:
// watering removed, 35568). No speed item, ever.
// [[tytd-dangioi-elixir]]

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func newElixirPlayer() (*Player, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Player{
		Username:  "u",
		UID:       42,
		Notify:    true,
		NotiChan:  make(chan NotifyMGS, 32),
		writeChan: make(chan []byte, 64),
		ctx:       ctx,
	}
	return p, cancel
}

func zeroRet(m pb.Message) []byte { // ret==0 answers only (engine reads f1)
	b, _ := pb.Marshal(m)
	return b
}

// delEchoFrom builds the DEL answer a REAL pull produces: ret==0 PLUS one
// freed ElixirLandInfo per requested landId (live 2026-10-18: a canAward>0
// no-op answers ret==0 with an EMPTY landInfo and the lot stays state=2 —
// elixirDelLots now refuses to count that as pulled). Fakes feed it the DEL
// request body so the echo covers exactly the lots being asked for.
func delEchoFrom(body []byte) []byte {
	var req proto.ElixirDelPlantGrassReq
	if pb.Unmarshal(body, &req) != nil {
		return zeroRet(&proto.ElixirDelPlantGrassResp{Ret: pb.Int32(0)})
	}
	infos := make([]*proto.ElixirLandInfo, 0, len(req.GetLandId()))
	for _, id := range req.GetLandId() {
		infos = append(infos, &proto.ElixirLandInfo{LandId: pb.Int32(id), State: pb.Int32(0)})
	}
	b, _ := pb.Marshal(&proto.ElixirDelPlantGrassResp{Ret: pb.Int32(0), LandInfo: infos})
	return b
}

// serveUntil answers frames until the player's ctx dies — range-over-open-
// channel would leak the goroutine and hang `<-done` style waits.
func serveUntil(ctx context.Context, ch chan []byte, t *testing.T, fn func([]byte) bool) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			return
		case f, ok := <-ch:
			if !ok {
				return
			}
			if fn(f) {
				return
			}
		}
	}
}

// TestElixirPlantTiers pins the ladder: ONLY HELD trees ever enter (live
// 2026-10-17: unheld PLANT = ret=0 silent no-op — the old unconditional trio
// made <live-user> "plant" 864024 it never owned). Ranking = intrinsic quality
// (6,5,4 across each trio; the season-golden id is always q6 — live
// ElixirHerbDB rows mined 2026-10-18) × 2 ONLY for THIS season's trio (owner:
// "chỉ có cây mùa này được tính x2, còn các cây mua trước thì không x2 nhưng
// vẫn tính theo độ xịn của nó"), best first; any trio tree outranks any
// ordinary herb (min 4×2=8 > max q6). Ordinary grass NEVER enters the list,
// and an account holding only ordinary grass gets NOTHING (owner: no herbal
// tree = harvest-only, lots stay empty).
func TestElixirPlantTiers(t *testing.T) {
	// holds the whole trio → golden q6×2=12 first, then q5×2=10, q4×2=8, then
	// own herbs by quality (864020 q5 before 864018 q4 — NOT by id). The
	// growing tree 864015 is NOT held → excluded (replanting it would no-op).
	held := map[int32]bool{864018: true, 864020: true, 864022: true, 864023: true, 864024: true, 860001: true}
	got := elixirPlantTiers(held, 8, []int32{864015})
	want := []int32{864022, 864023, 864024, 864020, 864018}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("tiers = %v, want %v", got, want)
	}
	// holds NO trio (<live-user>'s live shape was even worse: no herb at all) →
	// ladder = own held herbs by quality (the bug of the day: 864021 q2đ must
	// NEVER outrank 864020 q4đ just for having a bigger id); the unheld trio
	// must NOT appear, and the unheld growing tree 864015 is out too.
	got = elixirPlantTiers(map[int32]bool{864018: true, 864020: true, 864021: true, 860001: true}, 8, []int32{864015})
	// q5 864020 first; the q4 tie goes to the NEWER id 864021 over 864018.
	want = []int32{864020, 864021, 864018}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("no-trio tiers = %v, want %v (quality order: 864020 first)", got, want)
	}
	// Same-quality tie = newest id first (owner: of his two q5 trees the
	// NEWER 864020 Minh Tiêu Lôi Giáp is "ngon nhất", not 864017).
	got = elixirPlantTiers(map[int32]bool{864017: true, 864020: true}, 0, nil)
	if fmt.Sprint(got) != fmt.Sprint([]int32{864020, 864017}) {
		t.Errorf("q5 tie = %v, want [864020 864017]", got)
	}
	// like-for-like growing tree NOT held → excluded (replanting no-ops).
	got = elixirPlantTiers(map[int32]bool{864018: true}, 0, []int32{864015})
	if fmt.Sprint(got) != fmt.Sprint([]int32{864018}) {
		t.Errorf("unheld-growing tiers = %v, want [864018]", got)
	}
	// no season learned yet → NO x2 anywhere, ladder = held herbs by quality:
	// 864017 q5 beats 864021 q4 (old code had id-desc → 864021 first). The
	// growing tree 864021 is held → already in, deduped (no double entry).
	got = elixirPlantTiers(map[int32]bool{864017: true, 864021: true}, 0, []int32{864021})
	if fmt.Sprint(got) != fmt.Sprint([]int32{864017, 864021}) {
		t.Errorf("no-season tiers = %v, want [864017 864021]", got)
	}
	// ordinary-only account, no season → NOTHING (never plant ordinary).
	if got := elixirPlantTiers(map[int32]bool{862001: true}, 0, nil); len(got) != 0 {
		t.Errorf("ordinary-only tiers = %v, want empty", got)
	}
	// herbal bag EMPTY but a herb is growing (live <live-user> shape) → NOTHING.
	if got := elixirPlantTiers(map[int32]bool{}, 8, []int32{864022}); len(got) != 0 {
		t.Errorf("empty-bag tiers = %v, want empty", got)
	}
	// Quality itself: golden trio slot = 6, +1 = 5, +2 = 4 (owner's điểm
	// scale: 4đ = q5, 2đ = q4), and non-herbs score 0.
	if elixirGrassQuality(864020) != 5 || elixirGrassQuality(864021) != 4 || elixirGrassQuality(864022) != 6 {
		t.Errorf("quality = 5/4/6 expected, got %d/%d/%d",
			elixirGrassQuality(864020), elixirGrassQuality(864021), elixirGrassQuality(864022))
	}
	if elixirGrassQuality(860001) != 0 {
		t.Errorf("ordinary grass must score 0, got %d", elixirGrassQuality(860001))
	}
}

// TestElixirTrioValueOrder pins trio order = value tier 1,2,3 from
// ELIXIR_PERI_BONUS (910022|910023|910024-0) → trees G, G+1, G+2.
func TestElixirTrioValueOrder(t *testing.T) {
	tr := elixirSeasonTrio(8)
	if fmt.Sprint(tr) != fmt.Sprint([]int32{864022, 864023, 864024}) {
		t.Fatalf("trio = %v want [864022 864023 864024]", tr)
	}
	if len(elixirSeasonTrio(0)) != 0 || len(elixirSeasonTrio(334)) != 0 {
		t.Error("out-of-range season must yield no trio")
	}
	// Season 11 must have a trio (the old cap-10 blanked it — owner bug
	// 2026-10-21 "không detect được dược linh mùa mới").
	if fmt.Sprint(elixirSeasonTrio(11)) != fmt.Sprint([]int32{864031, 864032, 864033}) {
		t.Errorf("season 11 trio must exist, got %v", elixirSeasonTrio(11))
	}
}

func TestElixirGoldenGrass(t *testing.T) {
	if g := elixirGoldenGrass(8); g != 864022 { // live season-8 check (Chiếu Mệnh)
		t.Errorf("season 8 = %d, want 864022", g)
	}
	if g := elixirGoldenGrass(1); g != 864001 {
		t.Errorf("season 1 = %d, want 864001", g)
	}
	if g := elixirGoldenGrass(0) + elixirGoldenGrass(334); g != 0 {
		t.Errorf("out-of-range seasons must yield 0, got %d", g)
	}
	if g := elixirGoldenGrass(11); g != 864031 {
		t.Errorf("season 11 = %d, want 864031 (old cap-10 was the reported bug)", g)
	}
}

// plantFake answers PLANT like the LIVE server (2026-10-17 dual probes on
// <live-user>@710564): a held tree → ret=0 with the lot echoed back (state=2);
// an UNHELD tree → ret=0 with an EMPTY land list = silent no-op, the lot
// stays state=0 (NOT ret=35520 — that code is only the oneKey privilege
// refusal). The engine must treat a missing echo as failure and fall tiers.
func plantFake(accepted map[int32]bool) func(f []byte) (wantType string, body []byte, ok bool) {
	return func(f []byte) (string, []byte, bool) {
		var req proto.ElixirPlantGrassReq
		pb.Unmarshal(f, &req)
		if !req.GetIsPlantElves() || req.GetOneKey() {
			b, _ := pb.Marshal(&proto.ElixirPlantGrassResp{Ret: pb.Int32(35520)})
			return SERVER_ELIXIR_PLANT, b, true
		}
		all := true
		for _, gi := range req.GetGrassInfo() {
			if !accepted[gi.GetGrassId()] {
				all = false
			}
		}
		if !all {
			// unheld: ret=0, NO land echo (live unheld-plant behaviour)
			b, _ := pb.Marshal(&proto.ElixirPlantGrassResp{Ret: pb.Int32(0)})
			return SERVER_ELIXIR_PLANT, b, true
		}
		lands := make([]*proto.ElixirLandInfo, 0, len(req.GetGrassInfo()))
		for _, gi := range req.GetGrassInfo() {
			lands = append(lands, &proto.ElixirLandInfo{
				LandId: pb.Int32(gi.GetLandId()), State: pb.Int32(2), GrassId: pb.Int32(gi.GetGrassId())})
		}
		b, _ := pb.Marshal(&proto.ElixirPlantGrassResp{Ret: pb.Int32(0), Land: lands})
		return SERVER_ELIXIR_PLANT, b, true
	}
}

// mkTask builds one p.tasks entry the way taskSync would (hadSelect sticky
// flag derived from selectItem).
func mkTask(id int32, value string, state int32) *taskEntry {
	return &taskEntry{data: &proto.TaskDataTempMsg{
		TaskId: pb.Int32(id), Value: pb.String(value), State: pb.Int32(state)}}
}

// TestElixirTaskGrassEasiestFirst pins the post-cap pick (owner 2026-10-16
// "làm nhiệm vụ dễ nhất trước"; held-gate 2026-10-17): among OPEN cType-496
// Linh Điền tasks the smallest remaining gap wins — but ONLY among trees the
// bag holds (num>0): an unheld task tree is a silent ret=0 no-op on the live
// server, so it must lose to the next held task rather than burn the field.
// Done/claimed/select-pending and non-496 rows are invisible to it; unsynced
// board = 0.
func TestElixirTaskGrassEasiestFirst(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	held := map[int32]bool{860001: true, 860003: true}
	if g, d := p.elixirTaskGrass(held); g != 0 || d != "" {
		t.Fatalf("unsynced board: got %d %q, want idle", g, d)
	}
	p.TaskMu.Lock()
	p.tasks = map[int32]*taskEntry{
		510002: mkTask(510002, "60", 0),                                                                              // 80× 860001, gap 20  ← easiest
		510013: mkTask(510013, "10", 0),                                                                              // 80× 860003, gap 70
		510020: mkTask(510020, "80", 2),                                                                              // done → claim pass, not here
		510007: mkTask(510007, "79", 1),                                                                              // 1 short of target → open but unheld later; state never read
		520008: {data: &proto.TaskDataTempMsg{TaskId: pb.Int32(520008), Value: pb.String("10"), State: pb.Int32(0)}}, // cType 497, not plantable
		12345:  mkTask(12345, "0", 0),                                                                                // foreign task id
	}
	p.taskSynced = true
	p.TaskMu.Unlock()
	g, d := p.elixirTaskGrass(held)
	if g != 860001 {
		t.Errorf("grass=%d want 860001 (smallest gap 20), desc %q", g, d)
	}
	if !strings.Contains(d, "Bạch Linh Chỉ") || !strings.Contains(d, "60/80") {
		t.Errorf("desc %q must name the tree vi-style with progress", d)
	}
	// Bag lacks the easiest task's tree (<live-user> shape) → skip it, the next
	// HELD task wins; nothing held at all → idle (never no-op-plant junk).
	if g, _ := p.elixirTaskGrass(map[int32]bool{860003: true}); g != 860003 {
		t.Errorf("unheld-easiest: grass=%d want 860003 (860001 not held)", g)
	}
	if g, _ := p.elixirTaskGrass(map[int32]bool{862005: true}); g != 0 {
		t.Errorf("nothing held: grass=%d want 0 (field stays idle)", g)
	}
}

// TestElixirOrderGrass pins the ĐƠN HÀNG DƯỢC TIÊN picker under the OWNER
// LAW 2026-10-18: rank ORDERS by smallest POOLED deficit, never plant a tree
// the bag already drowns in. Bag shape is <live-user>'s live board (the bug
// report: "cây đang có rất nhiều nhưng mày vẫn trồng") — 860008 at 10862
// while the old need-ranking picker kept planting it.
func TestElixirOrderGrass(t *testing.T) {
	mkOrder := func(id int32, need ...int32) *proto.ElixirDestinyOrderInfo {
		var gi []*proto.ElixirGrassInfo
		for _, n := range need {
			gi = append(gi, &proto.ElixirGrassInfo{GrassId: pb.Int32(n), Num: pb.Int32(80)})
		}
		return &proto.ElixirDestinyOrderInfo{DestinyId: pb.Int32(id), GrassInfos: gi}
	}
	// Live <live-user>@710564 board (probe 2026-10-18): five destiny orders,
	// three gated behind 861008/862005 which here have NO ROW in grassInfos
	// (never unlocked = the true silent no-op, available-seed law 2026-10-21
	// — rows below carry the bag counts; ×0 rows would make them REACHABLE,
	// pinned by the dedicated case at the bottom); 860003 at bag 60 vs 120 →
	// the only reachable order (10042) is the closest-to-completion pick.
	unlockedRows := func(counts map[int32]int32) *proto.ElixirUserInfo {
		var rows []*proto.ElixirGrassInfo
		for id, n := range counts {
			rows = append(rows, &proto.ElixirGrassInfo{GrassId: pb.Int32(id), Num: pb.Int32(n)})
		}
		return &proto.ElixirUserInfo{GrassInfos: rows}
	}
	rsp := &proto.ElixirEnterHomeResp{UserInfo: unlockedRows(map[int32]int32{
		860009: 2268, 860008: 10862, 860005: 14, 860004: 532,
		860006: 9872, 860001: 4706, 860003: 60, 860002: 7564}),
		OrderInfo: &proto.ElixirOrderInfo{
			DestinyOrderInfos: []*proto.ElixirDestinyOrderInfo{
				{DestinyId: pb.Int32(10017), GrassInfos: []*proto.ElixirGrassInfo{
					{GrassId: pb.Int32(861008), Num: pb.Int32(80)}, {GrassId: pb.Int32(860008), Num: pb.Int32(80)}}},
				{DestinyId: pb.Int32(10016), GrassInfos: []*proto.ElixirGrassInfo{
					{GrassId: pb.Int32(860005), Num: pb.Int32(120)}, {GrassId: pb.Int32(862005), Num: pb.Int32(120)}}},
				{DestinyId: pb.Int32(10059), GrassInfos: []*proto.ElixirGrassInfo{
					{GrassId: pb.Int32(861008), Num: pb.Int32(120)}, {GrassId: pb.Int32(860002), Num: pb.Int32(120)}}},
				{DestinyId: pb.Int32(10042), GrassInfos: []*proto.ElixirGrassInfo{
					{GrassId: pb.Int32(860008), Num: pb.Int32(120)}, {GrassId: pb.Int32(860003), Num: pb.Int32(120)}}},
				{DestinyId: pb.Int32(10014), GrassInfos: []*proto.ElixirGrassInfo{
					{GrassId: pb.Int32(860008), Num: pb.Int32(160)}, {GrassId: pb.Int32(861008), Num: pb.Int32(160)}, {GrassId: pb.Int32(860002), Num: pb.Int32(160)}}},
			},
		}}
	bag := map[int32]int32{860009: 2268, 860008: 10862, 860005: 14, 860004: 532,
		860006: 9872, 860001: 4706, 860003: 60, 860002: 7564}
	g, desc := elixirOrderGrass(rsp, bag)
	// 10042 is the ONLY reachable order with a deficit (860003 short 60 of
	// 120). 860008 — despite being demanded 360× total — sits at bag 10862
	// (deficit 0) and must NEVER be picked again (that WAS the bug).
	if g != 860003 {
		t.Errorf("grass=%d want 860003 (closest-to-completion order 10042)", g)
	}
	if !strings.Contains(desc, "sắp đủ") || !strings.Contains(desc, "Thanh Nguyên Thảo") || !strings.Contains(desc, "60/120") {
		t.Errorf("desc=%q want 'sắp đủ' + tree name + bag/need progress", desc)
	}
	// Payable-everything → 0: submitOrders pays these, planting would be the
	// "cây đang có rất nhiều nhưng vẫn trồng" bug again.
	bagFull := map[int32]int32{860001: 500, 860002: 500}
	rspPay := &proto.ElixirEnterHomeResp{
		UserInfo: unlockedRows(bagFull),
		OrderInfo: &proto.ElixirOrderInfo{
			DestinyOrderInfos: []*proto.ElixirDestinyOrderInfo{mkOrder(1, 860001, 860002)},
		}}
	if g2, _ := elixirOrderGrass(rspPay, bagFull); g2 != 0 {
		t.Errorf("payable board picked %d, want 0 (submitOrders' job)", g2)
	}
	// NO-ROW board → 0: demanded trees absent from grassInfos were NEVER
	// unlocked — their PLANT is the silent no-op (2026-10-17), no matter how
	// full the passed bag claims to be. Never plant junk.
	if g3, _ := elixirOrderGrass(&proto.ElixirEnterHomeResp{OrderInfo: rspPay.OrderInfo}, bagFull); g3 != 0 {
		t.Errorf("rowless board picked %d, want 0", g3)
	}
	// POOLED need: two orders × 80 of the same tree vs bag 100 → deficit 60
	// — NOT "payable" (one order alone would look payable from the bag).
	rspPool := &proto.ElixirEnterHomeResp{
		UserInfo: unlockedRows(map[int32]int32{860001: 100}),
		OrderInfo: &proto.ElixirOrderInfo{
			DestinyOrderInfos: []*proto.ElixirDestinyOrderInfo{
				mkOrder(1, 860001), mkOrder(2, 860001),
			},
		}}
	if g4, d4 := elixirOrderGrass(rspPool, map[int32]int32{860001: 100}); g4 != 860001 || !strings.Contains(d4, "60") {
		t.Errorf("pooled pick %d %q want 860001 short 60 (2×80 − bag 100)", g4, d4)
	}
	// Closest wins: order A short 5 total, order B short 90 → plant A's tree.
	rspClose := &proto.ElixirEnterHomeResp{
		UserInfo: unlockedRows(map[int32]int32{860002: 75, 860004: 10}),
		OrderInfo: &proto.ElixirOrderInfo{
			DestinyOrderInfos: []*proto.ElixirDestinyOrderInfo{
				{DestinyId: pb.Int32(11), GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(860002), Num: pb.Int32(80)}}},
				{DestinyId: pb.Int32(12), GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(860004), Num: pb.Int32(200)}}},
			},
		}}
	if g5, _ := elixirOrderGrass(rspClose, map[int32]int32{860002: 75, 860004: 10}); g5 != 860002 {
		t.Errorf("closest pick %d want 860002 (short 5 < short 190)", g5)
	}
	// Trans orders ride the same ranking when open; finished ones are ignored.
	rspTrans := &proto.ElixirEnterHomeResp{
		UserInfo: unlockedRows(map[int32]int32{860003: 10, 860007: 1}),
		OrderInfo: &proto.ElixirOrderInfo{
			TransOrderInfos: []*proto.ElixirTransOrderInfo{
				{GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(860007), Num: pb.Int32(500)}}, IsOver: pb.Bool(true)},
				{GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(860003), Num: pb.Int32(40)}}, IsOver: pb.Bool(false)},
			},
		}}
	if g6, _ := elixirOrderGrass(rspTrans, map[int32]int32{860003: 10, 860007: 1}); g6 != 860003 {
		t.Errorf("trans pick %d want 860003 (open trans only)", g6)
	}

	// ×0 ROW = UNLOCKED (owner law 2026-10-21, repeated: "cây hiện ở bảng
	// nhiệm vụ dược tiên thì chắc chắn mày có hạt và có thể trồng"; live
	// PROVEN: <live-user> carried `861008×0`/`864024×0` and PLANT of the
	// zero-count row grew). The old num>0 gate called those orders
	// unreachable. Discriminator: saturate 860003 so order 10042 is payable,
	// then the SAME board picks order 10016 (860005+862005) iff both its
	// trees carry rows — ×0 counts. Bag saturates 860008 too, so every
	// other reachable order is payable/idle and 10016's 226-deficit wins.
	bagSat := map[int32]int32{860009: 2268, 860008: 10862, 860005: 14, 860004: 532,
		860006: 9872, 860001: 4706, 860003: 500, 860002: 7564}
	rowsX0 := unlockedRows(bagSat)
	rowsX0.GrassInfos = append(rowsX0.GrassInfos,
		&proto.ElixirGrassInfo{GrassId: pb.Int32(861008), Num: pb.Int32(0)},
		&proto.ElixirGrassInfo{GrassId: pb.Int32(862005), Num: pb.Int32(0)})
	g7, d7 := elixirOrderGrass(&proto.ElixirEnterHomeResp{UserInfo: rowsX0, OrderInfo: rsp.OrderInfo}, bagSat)
	if g7 != 862005 {
		t.Errorf("×0-row pick %d want 862005 (order 10016 unlocked by its ×0 rows, biggest deficit 120)", g7)
	}
	if !strings.Contains(d7, "sắp đủ") {
		t.Errorf("desc=%q want closest-order desc", d7)
	}
	// Same bag, rows ABSENT → 10016 stays unreachable and every remaining
	// reachable order is payable → idle (the 2026-10-17 no-op boundary).
	if g8, _ := elixirOrderGrass(&proto.ElixirEnterHomeResp{
		UserInfo: unlockedRows(bagSat), OrderInfo: rsp.OrderInfo}, bagSat); g8 != 0 {
		t.Errorf("rowless-862005 pick %d want 0 (never unlocked = silent no-op)", g8)
	}
}

// TestElixirLeastBagGrass pins the pure picker: smallest bag count wins,
// ties → smaller grass id; empty/zero-only bag → 0 (idle honestly).
func TestElixirLeastBagGrass(t *testing.T) {
	if g, _ := elixirLeastBagGrass(map[int32]int32{860004: 260, 860008: 12, 861008: 12}); g != 860008 {
		t.Fatalf("tie 12 picked %d, want smaller id 860008", g)
	}
	if g, _ := elixirLeastBagGrass(map[int32]int32{860004: 260, 860008: 3}); g != 860008 {
		t.Fatalf("picked %d, want rarest 860008", g)
	}
	if g, _ := elixirLeastBagGrass(map[int32]int32{}); g != 0 {
		t.Fatalf("empty bag picked %d, want 0", g)
	}
	if g, _ := elixirLeastBagGrass(map[int32]int32{860008: 0}); g != 0 {
		t.Fatalf("zero-only bag picked %d, want 0", g)
	}
}

// TestElixirClaimTaskRewards pins the claim gate per the OWNER model
// (2026-10-17): done = value >= target, state NEVER read (it mirrors
// completion, not payment — the old state!=1 gate starved the whole board),
// KNOWN Linh Điền id, no unresolved select-pick. Foreign ids, short values,
// garbage values, unsynced board send nothing. Sends are PER-ID (the batch
// is atomic; one paid row poisoned every ret=9 before), and the (id,value)
// ledger must not re-fire the same progress — a value change re-arms.
// OWNER 2026-10-18 ("báo nhận 14 quà mà vào thấy có vài nhiệm vụ"): the
// RETURN counts only ret==0 answers — an already-claimed row (server ret=9
// "Đã nhận quà nhiệm vụ") is sent once, ledger-marked, but NEVER counted
// as received. The fake answers 01f7 per id and plays ret=9 for 510003.
func TestElixirClaimTaskRewards(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	// unsynced board never fires.
	p.TaskMu.Lock()
	p.tasks = map[int32]*taskEntry{510002: mkTask(510002, "80", 0)}
	p.TaskMu.Unlock()
	if ids := p.elixirClaimTaskRewards(); ids != nil {
		t.Fatalf("unsynced board claimed %v", ids)
	}
	p.TaskMu.Lock()
	p.tasks = map[int32]*taskEntry{
		510002: mkTask(510002, "80", 0), // done → claim
		510003: mkTask(510003, "6", 2),  // done → claim (server: already paid, ret=9)
		510007: mkTask(510007, "79", 0), // below target
		510013: mkTask(510013, "80", 1), // state=1 is DONE, not claimed → claim (the old bug)
		510020: {data: &proto.TaskDataTempMsg{TaskId: pb.Int32(510020), Value: pb.String("80"), State: pb.Int32(0)},
			hadSelect: true}, // reward pick unresolved → never auto-claim
		999001: mkTask(999001, "999", 0),   // foreign id (union task etc.)
		510023: mkTask(510023, "n/a", 0),   // unreadable value → server's call, stay quiet
		550008: mkTask(550008, "40", 1),    // ct506 40/40 ladder → owner 2026-10-21: sau max (5) KHÔNG nhận bậc nào
		530003: mkTask(530003, "100", 1),   // ct506 100 đơn hàng → ladder=100 ≥ 5: nghỉ hết
		550005: mkTask(550005, "25", 1),    // ct506 — ladder ≥ 5 → KHÔNG nhận (kể cả bậc ≤ cap)
		530008: mkTask(530008, "20000", 0), // ct2 "Chặt cây" — NOT Đan Giới: never
		530013: mkTask(530013, "3000", 0),  // ct560 Tiên Ngọc — NOT Đan Giới: never
	}
	p.taskSynced = true
	p.elixirClaimTried = nil
	p.TaskMu.Unlock()

	got := make(chan []byte, 16)
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		if frameFuncType(t, f) == TASK_GET_REWARD_CMD {
			b := append([]byte(nil), f[18:]...)
			got <- b
			var req proto.TaskGetRewardReqMsg
			if pb.Unmarshal(b, &req) == nil && len(req.GetTaskId()) == 1 {
				ret := int32(0)
				if req.GetTaskId()[0] == 510003 { // owner already claimed this in-game
					ret = 9
				}
				ans, _ := pb.Marshal(&proto.TaskGetRewardRespMsg{Ret: pb.Int32(ret)})
				p.stDeliver(SERVER_TASK_GET_REWARD, ans)
			}
		}
		return false
	})
	ids := p.elixirClaimTaskRewards()
	// 510003 was SENT but answered ret=9 → NOT counted (the "14 quà" lie).
	// ALL ct506 rungs are owner-only now: the ladder reads 100 ≥ cap 5 →
	// "sau khi đến max, không nhận thưởng nhiệm vụ nữa" (owner 2026-10-21).
	want := []int32{510002, 510013}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("received %v want %v (ret=9 row must not count)", ids, want)
	}
	time.Sleep(50 * time.Millisecond) // let the fake grab the frames
	// ledger: same values again → nothing re-fires (ret=9 rows included —
	// the mark stands even though the row never counted as received);
	if ids := p.elixirClaimTaskRewards(); ids != nil {
		t.Errorf("second pass re-fired %v — (id,value) ledger broken", ids)
	}
	// …but a value change re-arms that row only.
	p.TaskMu.Lock()
	p.tasks[510007] = mkTask(510007, "80", 1) // just tipped over target, state=1
	p.TaskMu.Unlock()
	if ids := p.elixirClaimTaskRewards(); fmt.Sprint(ids) != fmt.Sprint([]int32{510007}) {
		t.Errorf("re-arm claimed %v want [510007]", ids)
	}
	// one request per id (atomic batch → per-id so ret=9 can't poison siblings)
	seen := []int32{}
	drain := time.NewTimer(300 * time.Millisecond)
	for counting := true; counting; {
		select {
		case b := <-got:
			var req proto.TaskGetRewardReqMsg
			if err := pb.Unmarshal(b, &req); err != nil {
				t.Fatal(err)
			}
			if len(req.GetTaskId()) != 1 {
				t.Errorf("batched %v — must be one id per request", req.GetTaskId())
			}
			seen = append(seen, req.GetTaskId()...)
		case <-drain.C:
			counting = false
		}
	}
	// 510003 WAS sent (once), just not counted as received; ids go out
	// sorted; ct506 rungs never even get a frame once the ladder ≥ 5.
	allWant := []int32{510002, 510003, 510013, 510007}
	if fmt.Sprint(seen) != fmt.Sprint(allWant) {
		t.Errorf("frames %v want %v", seen, allWant)
	}
}

// TestElixirSubmitOrders pins the owner directive "bot phải tự nộp đơn hàng"
// (2026-10-17): destiny orders the bag can already pay go out as 235515
// {type=1,index}; short/unheld orders never fire; the local bag is kept
// honest across orders in ONE tick (payout grassInfos fold + need
// subtraction), so two orders sharing a tree can't both pass the gate on
// one pile. Refusals (ret!=0) record the ledger key, successes clear it.
func TestElixirSubmitOrders(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	bag := &proto.ElixirUserInfo{GrassInfos: []*proto.ElixirGrassInfo{
		{GrassId: pb.Int32(860009), Num: pb.Int32(100)}, // one pile, two orders want 80 each
		{GrassId: pb.Int32(860004), Num: pb.Int32(260)},
	}}
	rsp := &proto.ElixirEnterHomeResp{Ret: pb.Int32(0), UserInfo: bag,
		OrderInfo: &proto.ElixirOrderInfo{DestinyOrderInfos: []*proto.ElixirDestinyOrderInfo{
			{DestinyId: pb.Int32(10023), GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(860009), Num: pb.Int32(80)}}}, // idx0: AFTER idx1 pays, the pile is short → skip
			{DestinyId: pb.Int32(10010), GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(860009), Num: pb.Int32(80)}}}, // idx1: pays first (descending)
			{DestinyId: pb.Int32(10011), GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(862006), Num: pb.Int32(80)}}}, // idx2 unheld → skip
			{DestinyId: pb.Int32(10024), GrassInfos: []*proto.ElixirGrassInfo{
				{GrassId: pb.Int32(860004), Num: pb.Int32(80)}, {GrassId: pb.Int32(860004), Num: pb.Int32(20)}}}, // idx3 sums to 100 ≤ 260 → pays
		}}}
	type reqRec struct {
		typ, idx int32
	}
	reqs := make(chan reqRec, 16)
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		if frameFuncType(t, f) != S_ELIXIR_PUSH_ORDER {
			return false
		}
		var req proto.ElixirPushOrderReq
		if pb.Unmarshal(f[18:], &req) != nil {
			return false
		}
		reqs <- reqRec{req.GetType(), req.GetIndex()}
		var b []byte
		switch req.GetIndex() {
		case 0: // must NOT fire once idx0 lost the shared pile to idx1
			b, _ = pb.Marshal(&proto.ElixirPushOrderResp{Ret: pb.Int32(9)})
		case 1: // pays first (descending): takes 80 of the 100-pile
			b, _ = pb.Marshal(&proto.ElixirPushOrderResp{Ret: pb.Int32(0),
				GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(860009), Num: pb.Int32(20)}},
				Rewards:    pb.String("dan@1")})
		case 3:
			b, _ = pb.Marshal(&proto.ElixirPushOrderResp{Ret: pb.Int32(0),
				GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(860004), Num: pb.Int32(160)}}})
		default: // must never be reached: unheld orders must not fire
			b, _ = pb.Marshal(&proto.ElixirPushOrderResp{Ret: pb.Int32(9)})
		}
		p.elixirDeliver(SERVER_ELIXIR_PUSH_ORDER, b)
		return false
	})
	n, desc := p.elixirSubmitOrders(rsp)
	if n != 2 {
		t.Fatalf("submitted=%d want 2 (idx0 + idx3)", n)
	}
	if !strings.Contains(desc, "860009") && !strings.Contains(desc, "Huyền Hoàng") {
		// desc names the FIRST submitted order — either the grass name or raw id is fine
		t.Logf("desc=%q (informational)", desc)
	}
	seen := []reqRec{}
	drain := time.NewTimer(300 * time.Millisecond)
loop:
	for {
		select {
		case r := <-reqs:
			seen = append(seen, r)
		case <-drain.C:
			break loop
		}
	}
	// highest slot first (live-proven board-shift rule): idx3, then idx1 —
	// idx1 pays the shared pile before idx0 even gets checked, so idx0 is
	// short and never fires; idx2 is unheld and never fires.
	if fmt.Sprint(seen) != fmt.Sprint([]reqRec{{1, 3}, {1, 1}}) {
		t.Fatalf("frames %v want [{1 3} {1 1}] — type must be 1 (destiny), descending, short/unheld must never fire", seen)
	}
}

// TestElixirSubmitOrdersRefusedLedger: a refused-but-ready order knocks at
// most once per content — the ledger key "idx:signature" keeps the 1-minute
// loop from re-firing 235515 forever; a changed order (new grass counts on
// the board) re-arms.
func TestElixirSubmitOrdersRefusedLedger(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	bag := &proto.ElixirUserInfo{GrassInfos: []*proto.ElixirGrassInfo{
		{GrassId: pb.Int32(860004), Num: pb.Int32(260)},
	}}
	mkRsp := func(need int32) *proto.ElixirEnterHomeResp {
		return &proto.ElixirEnterHomeResp{Ret: pb.Int32(0), UserInfo: bag,
			OrderInfo: &proto.ElixirOrderInfo{DestinyOrderInfos: []*proto.ElixirDestinyOrderInfo{
				{DestinyId: pb.Int32(10024), GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(860004), Num: pb.Int32(need)}}},
			}}}
	}
	hits := make(chan int32, 16)
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		if frameFuncType(t, f) != S_ELIXIR_PUSH_ORDER {
			return false
		}
		var req proto.ElixirPushOrderReq
		pb.Unmarshal(f[18:], &req)
		hits <- req.GetIndex()
		b, _ := pb.Marshal(&proto.ElixirPushOrderResp{Ret: pb.Int32(5)}) // refuse (e.g. daily destiny cap)
		p.elixirDeliver(SERVER_ELIXIR_PUSH_ORDER, b)
		return false
	})
	if n, _ := p.elixirSubmitOrders(mkRsp(80)); n != 0 {
		t.Fatalf("refused order reported submitted=%d", n)
	}
	// the refusal DID fire once (ret=5 refused): consume that frame.
	select {
	case idx := <-hits:
		if idx != 0 {
			t.Errorf("first fire index=%d want 0", idx)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("ready order never fired")
	}
	if n, _ := p.elixirSubmitOrders(mkRsp(80)); n != 0 {
		t.Fatalf("ledger let the refused order re-fire (n=%d)", n)
	}
	select {
	case idx := <-hits:
		t.Fatalf("refused order re-fired without content change (index=%d)", idx)
	case <-time.After(150 * time.Millisecond):
	}
	// same order, NEW content (new grass asked) → re-armed: the frame must
	// appear again (n stays 0 — the fake still refuses it, that's not the point).
	p.elixirSubmitOrders(mkRsp(120))
	select {
	case idx := <-hits:
		if idx != 0 {
			t.Errorf("re-arm fire index=%d want 0", idx)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("content change did not re-arm the ledger")
	}
}

// TestElixirClaimLadderCap5 pins the claim half of the 2026-10-21 cap (5):
// BELOW the milestone the bot still climbs — ct506 rung 510016 (target 1)
// claims while the ladder sits at 4; AT the milestone (value 5) the gate
// closes for EVERY rung — "sau khi đến max, không nhận thưởng nhiệm vụ nữa"
// — while ordinary (496/51xxxx) task claims keep flowing untouched.
func TestElixirClaimLadderCap5(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		if frameFuncType(t, f) == TASK_GET_REWARD_CMD {
			var req proto.TaskGetRewardReqMsg
			if pb.Unmarshal(f[18:], &req) == nil && len(req.GetTaskId()) == 1 {
				ans, _ := pb.Marshal(&proto.TaskGetRewardRespMsg{Ret: pb.Int32(0)})
				p.stDeliver(SERVER_TASK_GET_REWARD, ans)
			}
		}
		return false
	})
	// ladder = 4: rung 510016 (target 1, value 4) claims, the 51xxxx daily too.
	p.TaskMu.Lock()
	p.tasks = map[int32]*taskEntry{
		510016: mkTask(510016, "4", 0),
		510002: mkTask(510002, "80", 0),
	}
	p.taskSynced = true
	p.elixirClaimTried = nil
	p.TaskMu.Unlock()
	if ids := p.elixirClaimTaskRewards(); fmt.Sprint(ids) != fmt.Sprint([]int32{510002, 510016}) {
		t.Fatalf("ladder=4 claimed %v, want [510002 510016] (dưới max vẫn leo bậc ≤ 5)", ids)
	}
	// ladder = 5 (510016 tipped to 5): gate slams shut for ALL ct506 rungs —
	// even the completed target-1 one — while the 51xxxx daily still claims.
	// (Ledger NOT cleared: 510002's phase-1 mark keeps it from re-firing;
	// the gate itself must be what stops 510016's re-arm on value 4→5.)
	p.TaskMu.Lock()
	p.tasks[510016] = mkTask(510016, "5", 0)
	p.tasks[510003] = mkTask(510003, "6", 0) // cType 496 daily, done
	p.TaskMu.Unlock()
	if ids := p.elixirClaimTaskRewards(); fmt.Sprint(ids) != fmt.Sprint([]int32{510003}) {
		t.Fatalf("ladder=5 claimed %v, want [510003] only (đến max: nghỉ mọi bậc ct506, nhiệm vụ thường vẫn nhận)", ids)
	}
}

// TestElixirSubmitOrdersMilestoneCap5 pins OWNER DIRECTIVE 2026-10-21
// (cap chốt ở 5): "chuyển giới hạn max nhiệm vụ hoàn thành từ 25 về 5".
// The gate reads the SERVER's ct506 ladder value (max across the 506
// rows): at/over 5 → elixirSubmitOrders fires NOTHING even though the bag
// can pay; at 4 → submits normally; with no ladder row at all → NO gate
// (silence never blocks the bot — the server must prove the milestone).
func TestElixirSubmitOrdersMilestoneCap5(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	bag := &proto.ElixirUserInfo{GrassInfos: []*proto.ElixirGrassInfo{
		{GrassId: pb.Int32(860004), Num: pb.Int32(260)},
	}}
	rsp := &proto.ElixirEnterHomeResp{Ret: pb.Int32(0), UserInfo: bag,
		OrderInfo: &proto.ElixirOrderInfo{DestinyOrderInfos: []*proto.ElixirDestinyOrderInfo{
			{DestinyId: pb.Int32(10024), GrassInfos: []*proto.ElixirGrassInfo{
				{GrassId: pb.Int32(860004), Num: pb.Int32(80)}}},
		}}}
	hits := make(chan int32, 16)
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		if frameFuncType(t, f) != S_ELIXIR_PUSH_ORDER {
			return false
		}
		var req proto.ElixirPushOrderReq
		pb.Unmarshal(f[18:], &req)
		hits <- req.GetIndex()
		b, _ := pb.Marshal(&proto.ElixirPushOrderResp{Ret: pb.Int32(0),
			GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(860004), Num: pb.Int32(180)}}})
		p.elixirDeliver(SERVER_ELIXIR_PUSH_ORDER, b)
		return false
	})
	setLadder := func(v string) {
		p.TaskMu.Lock()
		p.tasks = map[int32]*taskEntry{550004: mkTask(550004, v, 0)}
		p.taskSynced = true
		p.TaskMu.Unlock()
	}
	// Ladder at 5 (row shows target 20 but server value 5 — value rules)
	// → dead silence, ZERO frames.
	setLadder("5")
	if n, d := p.elixirSubmitOrders(rsp); n != 0 || d != "" {
		t.Fatalf("ladder=5 submitted %d %q — cap broken, must be (0,\"\")", n, d)
	}
	select {
	case idx := <-hits:
		t.Fatalf("ladder=5 still fired a submit frame (index=%d)", idx)
	case <-time.After(150 * time.Millisecond):
	}
	// Ladder at 4 → pays normally.
	setLadder("4")
	if n, _ := p.elixirSubmitOrders(rsp); n != 1 {
		t.Fatalf("ladder=4 submitted=%d want 1", n)
	}
	select {
	case <-hits:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("ladder=4 order never fired")
	}
	// No ladder row at all → NO gate (missing data never blocks).
	p.TaskMu.Lock()
	p.tasks = map[int32]*taskEntry{}
	p.TaskMu.Unlock()
	p.elixirOrderTried = nil // fresh ledger: same content may knock again
	if n, _ := p.elixirSubmitOrders(rsp); n != 1 {
		t.Fatalf("no ladder row submitted=%d want 1 (silence must not gate)", n)
	}
	select {
	case <-hits:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("no-ladder order never fired")
	}
}

// TestElixirSeasonFold proves the pump-side fold of the passive 8ae9 push.
func TestElixirSeasonFold(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	b, _ := pb.Marshal(&proto.ElixirElvesDrawSeasonSync{Season: pb.Int32(8)})
	p.elixirDeliver(SERVER_ELIXIR_DRAW_SEASON, b)
	p.ElixirMu.Lock()
	season := p.ElixirSeason
	p.ElixirMu.Unlock()
	if season != 8 {
		t.Fatalf("season=%d want 8", season)
	}
	// zero-season garbage must not clobber a good value
	p.elixirDeliver(SERVER_ELIXIR_DRAW_SEASON, []byte{0x08, 0x00})
	p.ElixirMu.Lock()
	season = p.ElixirSeason
	p.ElixirMu.Unlock()
	if season != 8 {
		t.Fatalf("garbage clobbered season to %d", season)
	}
}

// TestElixirGoldenGrassBeyondOldCap pins the 2026-10-21 owner fix: the old
// hard cap (season > 10 → 0) silently blinded the trio from season 11 on —
// "đan giới không detect được dược linh mùa mới". The 864xxx family holds
// 333 seasons of trios; only beyond that (and season < 1) is golden 0.
func TestElixirGoldenGrassBeyondOldCap(t *testing.T) {
	for n, want := range map[int32]int32{1: 864001, 8: 864022, 9: 864025, 10: 864028, 11: 864031, 20: 864058, 333: 864997} {
		if g := elixirGoldenGrass(n); g != want {
			t.Errorf("golden(%d)=%d want %d", n, g, want)
		}
	}
	for _, n := range []int32{0, -1, 334, 999} {
		if g := elixirGoldenGrass(n); g != 0 {
			t.Errorf("golden(%d)=%d want 0 (outside family)", n, g)
		}
	}
	if fmt.Sprint(elixirSeasonTrio(11)) != fmt.Sprint([]int32{864031, 864032, 864033}) {
		t.Errorf("trio(11)=%v — season 11 must have its own trio", elixirSeasonTrio(11))
	}
}

// TestElixirPredictSeason pins the pure stale-season self-heal: inside the
// 8ae9 window (or with a garbage window) the folded number stands; past the
// end the season advances by whole elapsed periods (15-day cadence, live
// decoded), capped at the family ceiling.
func TestElixirPredictSeason(t *testing.T) {
	const day = int64(86400000)
	start, end := int64(1000)*day, int64(1015)*day // 15-day window
	if got := elixirPredictSeason(8, start, end, end); got != 8 {
		t.Errorf("inside/at window → %d want 8", got)
	}
	if got := elixirPredictSeason(8, start, end, end+day); got != 9 {
		t.Errorf("1 day past end → %d want 9", got)
	}
	if got := elixirPredictSeason(8, start, end, end+14*day); got != 9 {
		t.Errorf("almost 1 period past end → %d want 9 (floor)", got)
	}
	if got := elixirPredictSeason(8, start, end, end+16*day); got != 10 {
		t.Errorf("1 period + 1 day past end → %d want 10", got)
	}
	if got := elixirPredictSeason(8, start, end, end+40*day); got != 11 {
		t.Errorf("3 periods minus → %d want 11 (floor, not round)", got)
	}
	// garbage windows never guess
	for _, w := range [][2]int64{{0, 0}, {start, 0}, {end, start}} {
		if got := elixirPredictSeason(8, w[0], w[1], end+100*day); got != 8 {
			t.Errorf("window %+v → %d want 8 (no window, no guess)", w, got)
		}
	}
	if got := elixirPredictSeason(332, start, end, end+10*day); got != 333 {
		t.Errorf("cap: %d want 333", got)
	}
	// A full live-shaped replay: fold 8ae9 season 8 whose window already
	// died 15 days ago → elixirCurSeason predicts 9; a fresh push re-bases.
	p := &Player{Username: "t"}
	const dayMs = int64(86400000)
	now := time.Now().UnixMilli()
	// season-8 window [now-30d, now-15d]; today is exactly one full 15-day
	// period past its end → half-open floor lands on season 10.
	b, _ := pb.Marshal(&proto.ElixirElvesDrawSeasonSync{
		Season: pb.Int32(8), StartTime: pb.Int64(now - 30*dayMs), EndTime: pb.Int64(now - 15*dayMs)})
	p.elixirDeliver(SERVER_ELIXIR_DRAW_SEASON, b)
	if got := p.elixirCurSeason(); got != 10 {
		t.Fatalf("expired window (2 periods on): curSeason=%d want 10 (self-heal dead)", got)
	}
	p.ElixirMu.Lock()
	folded := p.ElixirSeason
	p.ElixirMu.Unlock()
	if folded != 8 {
		t.Fatalf("folded value must stay 8 (prediction never clobbers the fold), got %d", folded)
	}
	b2, _ := pb.Marshal(&proto.ElixirElvesDrawSeasonSync{
		Season: pb.Int32(10), StartTime: pb.Int64(now - 2*dayMs), EndTime: pb.Int64(now + 13*dayMs)})
	p.elixirDeliver(SERVER_ELIXIR_DRAW_SEASON, b2)
	if got := p.elixirCurSeason(); got != 10 {
		t.Fatalf("fresh push must re-base: got %d want 10", got)
	}
}

// TestElixirLoopSendsNoWater: WATER (000397f1) must NEVER leave the engine —
// the live server removed watering outright (35568).
func TestElixirLoopSendsNoWater(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	enter := &proto.ElixirEnterHomeResp{
		Ret:      pb.Int32(0),
		UserInfo: &proto.ElixirUserInfo{Water: pb.Int32(999), GrassInfos: []*proto.ElixirGrassInfo{{GrassId: pb.Int32(864018), Num: pb.Int32(4)}}},
		Lands:    []*proto.ElixirLandInfo{{LandId: pb.Int32(1), State: pb.Int32(0)}},
	}
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		switch frameFuncType(t, f) {
		case S_ELIXIR_WATER_GRASS:
			t.Errorf("ENGINE SENT WATER — watering is dead protocol")
		case S_ELIXIR_ENTER:
			b, _ := pb.Marshal(enter)
			p.elixirDeliver(SERVER_DANGIOI_INFO, b)
		case S_ELIXIR_ELVES_ENTER:
			b, _ := pb.Marshal(&proto.ElixirElvesEnterResp{ElvesPlayerData: &proto.ElixirElvesPlayerData{TodayGetCount: pb.Int32(3)}})
			p.elixirDeliver(SERVER_ELIXIR_ELVES_INFO, b)
		case S_ELIXIR_GET_GRASS:
			p.elixirDeliver(SERVER_ELIXIR_GET, zeroRet(&proto.ElixirGetGrassResp{Ret: pb.Int32(0)}))
		case S_ELIXIR_PLANT:
			// echo the planted lot like the live server — ret=0 with an EMPTY
			// land list is the unheld no-op and must NOT count as planted.
			p.elixirDeliver(SERVER_ELIXIR_PLANT, zeroRet(&proto.ElixirPlantGrassResp{Ret: pb.Int32(0),
				Land: []*proto.ElixirLandInfo{{LandId: pb.Int32(1), State: pb.Int32(2), GrassId: pb.Int32(864018)}}}))
		}
		return false
	})
	p.ElixirMu.Lock()
	p.ElixirSeason = 8
	p.ElixirMu.Unlock()
	go p.danGioiAutoLoop() // always-on now — no DB gate to seed
	select {
	case n := <-p.NotiChan:
		if !strings.Contains(n.MSG, "trồng 1 lô") {
			t.Errorf("notify %q", n.MSG)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("loop never planted")
	}
	cancel()
}

// TestElixirSnapshotRenders pins the two answers the owner asked for:
// the /30 Dược Linh counter (from the read-only 8ade panel) and which of
// the account's trees can roll Dược Linh at all (only the 864xxx family —
// game's own item text pairs every 91xxx Dược Linh with its 864xxx tree).
func TestElixirSnapshotRenders(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	enter := &proto.ElixirEnterHomeResp{
		Ret: pb.Int32(0),
		UserInfo: &proto.ElixirUserInfo{
			Water: pb.Int32(29), WaterBucket: pb.Int32(0), ElvesOpenOff: pb.Bool(true),
			GrassInfos: []*proto.ElixirGrassInfo{
				{GrassId: pb.Int32(864021), Lv: pb.Int32(15), Num: pb.Int32(129696)}, // herb, has seeds
				{GrassId: pb.Int32(860001), Lv: pb.Int32(10), Num: pb.Int32(8)},      // not a herb
				{GrassId: pb.Int32(864003), Lv: pb.Int32(8), Num: pb.Int32(0)},       // herb ×0 — STILL unlocked (row present)
			},
		},
		Lands: []*proto.ElixirLandInfo{
			{LandId: pb.Int32(1), State: pb.Int32(2), GrassId: pb.Int32(864021), CanAwardElvesNum: pb.Int32(3)},
			{LandId: pb.Int32(2), State: pb.Int32(2), GrassId: pb.Int32(864021)},
		},
	}
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		switch frameFuncType(t, f) {
		case S_ELIXIR_ENTER:
			b, _ := pb.Marshal(enter)
			p.elixirDeliver(SERVER_DANGIOI_INFO, b)
		case S_ELIXIR_ELVES_ENTER:
			b, _ := pb.Marshal(&proto.ElixirElvesEnterResp{
				Ret:             pb.Int32(0),
				ElvesPlayerData: &proto.ElixirElvesPlayerData{TodayGetCount: pb.Int32(12)},
			})
			p.elixirDeliver(SERVER_ELIXIR_ELVES_INFO, b)
		}
		return false
	})
	out, err := p.elixirSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"dược linh hôm nay: 12/30",
		"Xuân Nhật Hướng Dương×2 lô",               // planted herbal lots
		"Xuân Nhật Hướng Dương (lv15, 129696 cây)", // held herb seeds
		"(lv8, 0 cây)", // ×0 herb STILL listed — row present = unlocked (owner law 2026-10-21)
		"mùa 0",        // no season push yet
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Bạch Linh Chỉ") {
		t.Errorf("non-herb tree leaked into the herb list:\n%s", out)
	}
}

func TestElixirIsHerb(t *testing.T) {
	for id, want := range map[int32]bool{
		864021: true, 864001: true, 864999: true, // Dược Linh family
		860001: false, 862021: false, 863021: false, 865001: false, // ordinary grass
		0: false,
	} {
		if got := elixirIsHerb(id); got != want {
			t.Errorf("elixirIsHerb(%d)=%v want %v", id, got, want)
		}
	}
}

// TestDanGioiGoldenEnterHome pins that a real captured 8aae still decodes
// with the ported schema. The corpus file is written by TestDanGioiProbeLive
// on its first successful ENTER.
func TestDanGioiGoldenEnterHome(t *testing.T) {
	raw, err := os.ReadFile("testdata/dangioi_8aae.hex")
	if err != nil {
		t.Skip("no golden capture yet — run TestDanGioiProbeLive")
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("golden hex corrupt: %v", err)
	}
	var rsp proto.ElixirEnterHomeResp
	if err := pb.Unmarshal(b, &rsp); err != nil {
		t.Fatalf("golden 8aae does not decode: %v", err)
	}
	if rsp.GetRet() != 0 {
		t.Errorf("golden ret=%d, want 0", rsp.GetRet())
	}
	if n := len(rsp.GetLands()); n < 30 {
		t.Errorf("golden lands=%d, want the full field (≥30)", n)
	}
	if rsp.GetUserInfo() == nil {
		t.Fatal("golden has no userInfo")
	}
	for _, l := range rsp.GetLands() {
		if l.GetLandId() == 0 {
			t.Error("land with landId=0")
		}
	}
}

func TestElixirGrassName(t *testing.T) {
	if got := elixirGrassName(864024); got != "Huyền Cốt Sâm Vương" {
		t.Errorf("herb name = %q", got)
	}
	if got := elixirGrassName(860001); got != "Bạch Linh Chỉ" {
		t.Errorf("task-tree name = %q", got)
	}
	if got := elixirGrassName(999999); got != "cây 999999" {
		t.Errorf("unknown fallback = %q", got)
	}
}

// ===== owner flow 2026-10-23 pins (pure event-driven, NO cuốc) =====

// TestElixirHarvestNoCuoc pins loop #1 under the no-cuốc law (owner
// 2026-10-23: "giờ không làm nhiệm vụ nữa nên không cuốc nữa"): per ripe lot
// ONE GET — and NOT A SINGLE 235537 (DEL) frame may ever leave, plus none of
// its GET/ENTER neighbours. Unripe lots (canAward==0, hadRecv>0) and empty
// lots are invisible ("cây chưa chín hoặc ruộng trống continue luôn"). A GET
// the server refuses (ret!=0) must NOT be recorded. The returned map is
// landId→grassId — the "cây cũ trùng id đã thu hoạch ở ô đó" the plant loop
// replants. Single-round plants auto-empty on the GET; multi-round trees
// stay standing and re-ripen on their own — nothing needs digging.
func TestElixirHarvestNoCuoc(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	rsp := &proto.ElixirEnterHomeResp{Ret: pb.Int32(0), Lands: []*proto.ElixirLandInfo{
		{LandId: pb.Int32(1), State: pb.Int32(2), GrassId: pb.Int32(864020), CanAwardRound: pb.Int32(3), HadReceiveRound: pb.Int32(2)},
		{LandId: pb.Int32(2), State: pb.Int32(2), GrassId: pb.Int32(864020), CanAwardRound: pb.Int32(0), HadReceiveRound: pb.Int32(5)}, // mid-round → untouched
		{LandId: pb.Int32(3), State: pb.Int32(2), GrassId: pb.Int32(860001), CanAwardRound: pb.Int32(1), HadReceiveRound: pb.Int32(1)},
		{LandId: pb.Int32(4), State: pb.Int32(0)},                                                        // empty → untouched
		{LandId: pb.Int32(5), State: pb.Int32(2), GrassId: pb.Int32(864018), CanAwardRound: pb.Int32(1)}, // GET refused → not recorded
	}}
	var seq []string
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		switch frameFuncType(t, f) {
		case S_ELIXIR_GET_GRASS:
			var req proto.ElixirGetGrassReq
			pb.Unmarshal(f[18:], &req)
			if req.GetOneKey() || len(req.GetLandId()) != 1 {
				t.Errorf("GET not per-lot explicit: %+v", &req)
			}
			lot := req.GetLandId()[0]
			seq = append(seq, fmt.Sprintf("GET%d", lot))
			ret := int32(0)
			if lot == 5 {
				ret = 5
			}
			p.elixirDeliver(SERVER_ELIXIR_GET, zeroRet(&proto.ElixirGetGrassResp{Ret: pb.Int32(ret)}))
		case S_ELIXIR_DEL:
			t.Errorf("235537 frame sent — the engine NEVER cuốc's anymore (owner 2026-10-23)")
		case S_ELIXIR_ENTER:
			seq = append(seq, "ENTER") // must never appear here
		}
		return false
	})
	harvested := p.elixirHarvest(rsp)
	if len(harvested) != 2 || harvested[1] != 864020 || harvested[3] != 860001 {
		t.Errorf("harvested %v want {1:864020 3:860001} (lot 5's GET was refused)", harvested)
	}
	want := fmt.Sprint([]string{"GET1", "GET3", "GET5"})
	if fmt.Sprint(seq) != want {
		t.Errorf("frame order\n got %v\nwant %v — per-lot GET only: NO DEL, no ENTER, no frames for unripe/empty lots", seq, want)
	}
}

// TestElixirPlantLoopOldThenRandom pins loop #2 under the AUTO-DƯỢC-LINH
// REMOVAL (owner 2026-10-23: "bỏ auto dược linh đi, khỏi phải check số lượng
// dược linh với cây dược linh"): NO elf branch, NO counters — the just-GET'd
// lot replants the SAME id it was harvested with ("trồng cây cũ trùng id đã
// thu hoạch ở ô đó") and that branch runs FIRST, before any random draw,
// even when the bag holds a golden herbal row (864022×0): lot 3 must land on
// 860001, never on the herb — proof the bestElf preference is GONE. The
// frame-empty lot gets a shuffled random pick from the bag (herbs may still
// be drawn — they are just plain members now). The growing lot is invisible;
// the loop NEVER sends GET/DEL/ENTER ("8 chỉ trồng"). Per-lot, elves flag
// stays on (the server honours it either way), oneKey off.
func TestElixirPlantLoopOldThenRandom(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	p.ElixirGardenMu.Lock()
	p.ElixirBagRows = map[int32]int32{860001: 9, 864022: 0} // herbal row present but gets NO priority
	p.ElixirGardenMu.Unlock()
	rsp := &proto.ElixirEnterHomeResp{
		Ret: pb.Int32(0),
		Lands: []*proto.ElixirLandInfo{
			{LandId: pb.Int32(1), State: pb.Int32(0)},                                                          // empty → random
			{LandId: pb.Int32(2), State: pb.Int32(2), GrassId: pb.Int32(864018)},                               // growing → bỏ qua
			{LandId: pb.Int32(3), State: pb.Int32(2), GrassId: pb.Int32(860001), HadReceiveRound: pb.Int32(4)}, // just GET'd → cây cũ
		},
	}
	fake := plantFake(map[int32]bool{860001: true, 864022: true})
	var got []string
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		switch frameFuncType(t, f) {
		case S_ELIXIR_PLANT:
			var req proto.ElixirPlantGrassReq
			pb.Unmarshal(f[18:], &req)
			if !req.GetIsPlantElves() || req.GetOneKey() || len(req.GetGrassInfo()) != 1 {
				t.Errorf("plant not per-lot explicit+elves: %+v", &req)
			}
			gi := req.GetGrassInfo()[0]
			got = append(got, fmt.Sprintf("%d:%d", gi.GetLandId(), gi.GetGrassId()))
			wt, b, _ := fake(f[18:])
			p.elixirDeliver(wt, b)
		case S_ELIXIR_DEL, S_ELIXIR_GET_GRASS, S_ELIXIR_ENTER:
			t.Errorf("plant loop sent %s — it must ONLY plant (owner: 8 chỉ trồng, và không cuốc nữa)", frameFuncType(t, f))
		}
		return false
	})
	planted, nOld, nRand := p.elixirPlantLoop(rsp, map[int32]int32{3: 860001})
	if planted != 2 || nOld != 1 || nRand != 1 {
		t.Errorf("planted=%d old=%d rand=%d want 2/1/1", planted, nOld, nRand)
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{"1:860001", "3:860001"}) &&
		fmt.Sprint(got) != fmt.Sprint([]string{"1:864022", "3:860001"}) {
		t.Errorf("packets %v want lot 3 on its own 860001 (cây-cũ first, no elf preference) and lot 1 on any bag row", got)
	}
}

// TestElixirPlantLoopCayCuWins pins the same chain on a full field: the
// just-GET'd lot replants the SAME id ("trồng cây cũ trùng id đã thu hoạch
// ở ô đó"), lots empty from the start get a RANDOM row from the bag.
// Echo-accept counts only; a refused plant is never counted.
func TestElixirPlantLoopCayCuWins(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	p.ElixirGardenMu.Lock()
	p.ElixirBagRows = map[int32]int32{860001: 5, 860003: 2}
	p.ElixirGardenMu.Unlock()
	rsp := &proto.ElixirEnterHomeResp{
		Ret: pb.Int32(0),
		Lands: []*proto.ElixirLandInfo{
			{LandId: pb.Int32(1), State: pb.Int32(0)},                                                          // empty from start → random
			{LandId: pb.Int32(2), State: pb.Int32(0)},                                                          // empty from start → random
			{LandId: pb.Int32(3), State: pb.Int32(2), GrassId: pb.Int32(860003), HadReceiveRound: pb.Int32(1)}, // just GET'd → same id
		},
	}
	fake := plantFake(map[int32]bool{860001: true, 860003: true})
	got := map[int32]int32{}
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		if frameFuncType(t, f) == S_ELIXIR_PLANT {
			var req proto.ElixirPlantGrassReq
			pb.Unmarshal(f[18:], &req)
			gi := req.GetGrassInfo()[0]
			got[gi.GetLandId()] = gi.GetGrassId()
			wt, b, _ := fake(f[18:])
			p.elixirDeliver(wt, b)
		}
		return false
	})
	planted, nOld, nRand := p.elixirPlantLoop(rsp, map[int32]int32{3: 860003})
	if planted != 3 || nOld != 1 || nRand != 2 {
		t.Fatalf("planted=%d old=%d rand=%d want 3/1/2", planted, nOld, nRand)
	}
	if got[3] != 860003 {
		t.Errorf("lot 3 = %d, want its own harvested id 860003", got[3])
	}
	for _, lot := range []int32{1, 2} {
		if g := got[lot]; g != 860001 && g != 860003 {
			t.Errorf("lot %d = %d, want a bag row (860001/860003)", lot, g)
		}
	}
}

// TestElixirPlantLoopDeadTreeFallback pins the live fix of 2026-10-23
// (PROVEN on <live-user>@710565, TestDanGioiPlantFlagsLive): the server
// answers PLANT of an account-gated tree (herbal while Dược Linh is OFF —
// elvesOpenOff=true — with 42 seeds HELD and the row present) with ret=0 +
// EMPTY echo. The lot must NOT be abandoned (owner: "đất trống không tự
// trồng. thu hoạch xong vứt đất đấy"): a refusal FALLS THROUGH the chain
// cây cũ → random túi until a cỏ thường echoes. Each refused tree costs AT
// MOST ONE packet per pass (the dead set), not one per lot. The random
// shuffle may draw the gated 864024 — it dies after its single attempt too.
func TestElixirPlantLoopDeadTreeFallback(t *testing.T) {
	p, cancel := newElixirPlayer()
	defer cancel()
	p.ElixirGardenMu.Lock()
	p.ElixirBagRows = map[int32]int32{864024: 42, 860001: 955, 860003: 196}
	p.ElixirGardenMu.Unlock()
	rsp := &proto.ElixirEnterHomeResp{
		Ret: pb.Int32(0),
		Lands: []*proto.ElixirLandInfo{
			{LandId: pb.Int32(10), State: pb.Int32(0)},                                                          // empty → falls to random
			{LandId: pb.Int32(13), State: pb.Int32(0)},                                                          // empty → falls to random
			{LandId: pb.Int32(21), State: pb.Int32(2), GrassId: pb.Int32(860011), HadReceiveRound: pb.Int32(2)}, // just GET'd, old id also gated
		},
	}
	fake := plantFake(map[int32]bool{860001: true, 860003: true}) // 864024/860011 = silent no-ops
	var attempts []string
	go serveUntil(p.ctx, p.writeChan, t, func(f []byte) bool {
		if frameFuncType(t, f) == S_ELIXIR_PLANT {
			var req proto.ElixirPlantGrassReq
			pb.Unmarshal(f[18:], &req)
			gi := req.GetGrassInfo()[0]
			attempts = append(attempts, fmt.Sprintf("%d:%d", gi.GetLandId(), gi.GetGrassId()))
			wt, b, _ := fake(f[18:])
			p.elixirDeliver(wt, b)
		}
		return false
	})
	planted, nOld, nRand := p.elixirPlantLoop(rsp, map[int32]int32{21: 860011})
	if planted != 3 || nOld != 0 || nRand != 3 {
		t.Fatalf("planted=%d old=%d rand=%d want 3/0/3 (gated trees must fall through to cỏ thường)", planted, nOld, nRand)
	}
	nHerbF, nOldF := 0, 0
	for _, a := range attempts {
		if strings.HasSuffix(a, ":864024") {
			nHerbF++
		}
		if strings.HasSuffix(a, ":860011") {
			nOldF++
		}
	}
	if nHerbF > 1 {
		t.Errorf("gated herbal 864024 attempted %d times across the pass, want ≤1 (dead set)", nHerbF)
	}
	if nOldF != 1 {
		t.Errorf("gated old id 860011 attempted %d times, want exactly 1", nOldF)
	}
}
