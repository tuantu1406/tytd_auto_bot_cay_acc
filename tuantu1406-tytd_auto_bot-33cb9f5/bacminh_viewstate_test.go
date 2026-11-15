package tytd_go

// North view-window plumbing: bmViewBlocks must clamp/sample on the BAKED
// 119 grid (posIndex = y*119+x — the same decomposition the follow message
// and page use), and the shared kuViewAnswer flatten reads north positions
// unchanged (it is position-agnostic — keys are raw server posIndex).

import (
	"testing"
)

func TestBmViewBlocksGrid119(t *testing.T) {
	blocks := bmViewBlocks(0, 0, 500, 500) // oversized rect → sampled, clamped
	if len(blocks) == 0 || len(blocks) > kuViewMaxBlocks {
		t.Fatalf("sampled %d blocks, want 1..%d", len(blocks), kuViewMaxBlocks)
	}
	cols := 119
	seen := map[int32]bool{}
	for _, p := range blocks {
		if p <= 0 || int(p) >= cols*cols {
			t.Fatalf("block %d outside 119²", p)
		}
		if seen[p] {
			t.Fatalf("duplicate block %d", p)
		}
		seen[p] = true
	}
	// Exact small rect: no sampling, dense row-major over y*119+x.
	tight := bmViewBlocks(10, 20, 11, 21)
	want := []int32{20*119 + 10, 20*119 + 11, 21*119 + 10, 21*119 + 11}
	if len(tight) != len(want) {
		t.Fatalf("tight rect %v want %v", tight, want)
	}
	for i := range want {
		if tight[i] != want[i] {
			t.Fatalf("tight[%d]=%d want %d", i, tight[i], want[i])
		}
	}
	// A fully-outside rect clamps onto (0,0) only — and (0,0) is posIndex 0,
	// which the sampler drops (p > 0 guard), so the answer is EMPTY, never a
	// negative/zero-position row that would ask the server for a void tile.
	if got := bmViewBlocks(-5, -5, -1, -1); len(got) != 0 {
		t.Fatalf("outside rect produced %v", got)
	}
}
