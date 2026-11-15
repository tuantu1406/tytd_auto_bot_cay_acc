package tytd_go

// Owner strategy table tests (tinhnang.jpg, 2026-10-09): the per-type option
// cycles, the expensive-first rule for bet/alchemy, and the fan guarantee —
// a preference only REORDERS candidates, never filters them.

import (
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func lhOpts(orders []int32, costs []int64) []*proto.RebornTrialEventOption {
	out := make([]*proto.RebornTrialEventOption, 0, len(orders))
	for i, o := range orders {
		out = append(out, &proto.RebornTrialEventOption{Order: pb.Int32(o), CostExp: pb.Int64(costs[i])})
	}
	return out
}

func TestRbtStrategyPreferCycles(t *testing.T) {
	seqs := map[int32]int{}
	// Rescue = "2 2 3": successive encounters cycle 2, 2, 3, 2, …
	var got []int32
	for i := 0; i < 5; i++ {
		got = append(got, rbtPreferOrder(seqs, rbtEvRescue))
	}
	want := []int32{2, 2, 3, 2, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rescue cycle %v want %v", got, want)
		}
	}
	if p := rbtPreferOrder(seqs, 99); p != 0 {
		t.Fatalf("unlisted type got prefer %d, want 0 (plain order fan)", p)
	}
	// nil map must not panic (stateless = first number forever)
	if p := rbtPreferOrder(nil, rbtEvSuppress); p != 1 {
		t.Fatalf("suppress nil-map prefer %d", p)
	}
}

func TestRbtStrategySortOrders(t *testing.T) {
	opts := lhOpts([]int32{0, 1, 2}, []int64{100, 20, 50})
	rbtStrategySort(opts, 2, false) // prefer option #2 → 0-based order 1 first
	if opts[0].GetOrder() != 1 || opts[1].GetOrder() != 0 || opts[2].GetOrder() != 2 {
		t.Fatalf("prefer sort got %d/%d/%d", opts[0].GetOrder(), opts[1].GetOrder(), opts[2].GetOrder())
	}
	// expensive-first ignores preference and sorts by wager descending
	rbtStrategySort(opts, 2, true)
	if opts[0].GetCostExp() != 100 || opts[2].GetCostExp() != 20 {
		t.Fatalf("expensive sort got costs %d/%d/%d", opts[0].GetCostExp(), opts[1].GetCostExp(), opts[2].GetCostExp())
	}
	// preference pointing at a nonexistent option keeps plain order fan
	rbtStrategySort(opts, 9, false)
	if !(opts[0].GetOrder() == 0 && opts[2].GetOrder() == 2) {
		t.Fatal("missing preference should fall back to ascending order")
	}
}

func TestRbtStrategyExpensiveMap(t *testing.T) {
	// "đánh bạc bằng đá và thuật giả kim — chỉ chọn cái đắt tiền"
	if !rbtEvExpensive[rbtEvBet] || !rbtEvExpensive[rbtEvAlchemy] {
		t.Fatal("bet & alchemy must wager the highest affordable exp")
	}
	if rbtEvExpensive[rbtEvRescue] || rbtEvExpensive[rbtEvSell] {
		t.Fatal("non-wager types must not force expensive-first")
	}
}

func TestRbtStrategyTableMatchesImage(t *testing.T) {
	// Numbers transcribed from tinhnang.jpg (first variant per row).
	checks := map[int32][]int32{
		rbtEvBuy:      {1},
		rbtEvSell:     {1, 2, 3},
		rbtEvSuppress: {1, 1, 1},
		rbtEvRescue:   {2, 2, 3},
	}
	for typ, want := range checks {
		got := rbtStrategySeq[typ]
		if len(got) != len(want) {
			t.Fatalf("type %d: %v want %v", typ, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("type %d: %v want %v", typ, got, want)
			}
		}
	}
	// Fake trades: owner 2026-10-09 — auto CHỌN LỪA ĐẢO = cửa 1 (order 0,
	// the no-price door; client-proven the priced honest door is order 1).
	if got := rbtStrategySeq[20]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("BuyFake must auto-pick the cheat door: %v", got)
	}
	if got := rbtStrategySeq[21]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("SellFake must auto-pick the cheat door: %v", got)
	}
}
