package tytd_go

import (
	"fmt"
	"os"
	"testing"
)

// TestCastSwordRot8 compares rotation cycles (the human locked-corner
// scheme) vs snake-expectimax over 100 seeds x 5000 strength.
func TestCastSwordRot8(t *testing.T) {
	if os.Getenv("TYTD_MC_BENCH") == "" {
		t.Skip("TYTD_MC_BENCH=1")
	}
	seeds := make([]uint64, 0, 100)
	for i := uint64(1); i <= 100; i++ {
		seeds = append(seeds, i*104729+i*i*7919)
	}
	type cand struct {
		name string
		pol  int
		rot  []int32
	}
	cands := []cand{
		{"snake-exp", 1, nil},
		{"rot D-L-U-L", 3, []int32{2, 3, 1, 3}},
		{"rot L-D-R-D", 3, []int32{3, 2, 4, 2}},
		{"rot U-L-D-L", 3, []int32{1, 3, 2, 3}},
		{"rot L-U-L-D", 3, []int32{3, 1, 3, 2}},
	}
	for _, c := range cands {
		csPolicy, csRotation = c.pol, c.rot
		if c.rot == nil {
			csRotation = []int32{2, 3, 1, 3}
		}
		tot, lv, best := 0, 0, 0
		for _, sd := range seeds {
			r := runN(t, sd, 5000)
			tot += r[0]
			lv += r[1]
			if r[1] > best {
				best = r[1]
			}
		}
		fmt.Printf("ROT-BENCH %-12s: Lv11 avg %.2f/ván, best %d, avg nước %.0f\n",
			c.name, float64(lv)/float64(len(seeds)), best, float64(tot)/float64(len(seeds)))
	}
	csPolicy = 1
}

// Snake-expectimax (production policy) over 8 seeds x 5000 strength.
func TestCastSwordSnake8(t *testing.T) {
	if os.Getenv("TYTD_MC_BENCH") == "" {
		t.Skip("TYTD_MC_BENCH=1")
	}
	tot, lv, best, n := 0, 0, 0, 100
	dist := map[int]int{}
	for _, sd := range func() []uint64 {
		var seeds []uint64
		for i := uint64(1); i <= 100; i++ {
			seeds = append(seeds, i*104729+i*i*7919)
		}
		return seeds
	}() {
		r := runN(t, sd, 5000)
		tot += r[0]
		lv += r[1]
		if r[1] > best {
			best = r[1]
		}
		dist[r[1]]++
	}
	fmt.Printf("SNAKE %d seeds x5000: Lv11 tổng %d, avg %.2f/ván, best %d, avg nước %.0f\n  phân bố Lv11: %v\n",
		n, lv, float64(lv)/float64(n), best, float64(tot)/float64(n), dist)
}
