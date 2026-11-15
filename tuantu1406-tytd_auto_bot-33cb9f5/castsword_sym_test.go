package tytd_go

// Symmetric snake eval head-to-head: does letting the ladder live in ANY of
// the 8 corner/snake orientations beat the fixed top-left one? Same seeds,
// depth 3, calibrated spawn model. TYTD_MC_BENCH=1.
import (
	"fmt"
	"os"
	"testing"
)

func TestCastSwordSym(t *testing.T) {
	if os.Getenv("TYTD_MC_BENCH") == "" {
		t.Skip("TYTD_MC_BENCH=1")
	}
	var seeds []uint64
	for i := uint64(1); i <= 10; i++ {
		seeds = append(seeds, i*104729+i*i*7919)
	}
	for _, sym := range []bool{false, true} {
		csSnakeSym = sym
		tot, lv, best := 0, 0, 0
		for _, sd := range seeds {
			r := runN(t, sd, 5000)
			tot += r[0]
			lv += r[1]
			if r[1] > best {
				best = r[1]
			}
		}
		fmt.Printf("SYM=%-5v (10 seeds): Lv11 avg %.2f/ván, best %d, avg nước %.0f\n",
			sym, float64(lv)/float64(len(seeds)), best, float64(tot)/float64(len(seeds)))
	}
	csSnakeSym = false
}
