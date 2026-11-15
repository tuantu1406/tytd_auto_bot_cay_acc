package tytd_go

// Depth probe on the CALIBRATED spawn model (uniform empty cell + tier
// 888/103/9): is the snake expectimax eval-limited or lookahead-limited?
// TYTD_MC_BENCH=1. depth 4 costs ~20x/move more than depth 3, so it runs on
// fewer seeds by design — this ranks, it does not certify.
import (
	"fmt"
	"os"
	"testing"
)

func TestCastSwordDepth(t *testing.T) {
	if os.Getenv("TYTD_MC_BENCH") == "" {
		t.Skip("TYTD_MC_BENCH=1")
	}
	seeds := func(n int) []uint64 {
		var s []uint64
		for i := uint64(1); i <= uint64(n); i++ {
			s = append(s, i*104729+i*i*7919)
		}
		return s
	}
	for _, d := range []struct {
		depth int
		n     int
	}{
		{3, 20},
		{4, 5},
	} {
		csSnakeDepth = d.depth
		tot, lv, best := 0, 0, 0
		for _, sd := range seeds(d.n) {
			r := runN(t, sd, 5000)
			tot += r[0]
			lv += r[1]
			if r[1] > best {
				best = r[1]
			}
		}
		fmt.Printf("DEPTH %d (%2d seeds): Lv11 avg %.2f/ván, best %d, avg nước %.0f\n",
			d.depth, d.n, float64(lv)/float64(d.n), best, float64(tot)/float64(d.n))
	}
	csSnakeDepth = 3
}
