package tytd_go

// Throwaway offline benchmark of csBestMove (expectimax): self-play vs a
// scripted server (1 tier-1 spawn per move, deterministic LCG slot choice).
// go test -run TestCastSwordBench -v .

import (
	"fmt"
	"os"
	"testing"
)

func TestCastSwordBench(t *testing.T) {
	if os.Getenv("TYTD_MC_BENCH") == "" {
		t.Skip("set TYTD_MC_BENCH=1 (multi-minute MC benchmark)")
	}
	games, moves, maxTier := 8, 0, int32(0)
	sumMoves := 0
	seed := uint64(88172645463325252)
	next := func(n uint64) uint64 { // splitmix64
		seed += n
		z := seed
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		return z ^ (z >> 31)
	}
	for g := 0; g < games; g++ {
		var b csBoard
		// fresh run like ENTER: two tier-1 tiles
		b[0], b[5] = 1, 1
		m := 0
		for {
			d, _, ok := csBestMove(b)
			if !ok {
				break
			}
			nb, _, _ := csSlide(b, int(d))
			empties := []int{}
			for i, v := range nb {
				if v == 0 {
					empties = append(empties, i)
				}
			}
			if len(empties) > 0 {
				nb[empties[next(0)%uint64(len(empties))]] = 1
			}
			b = nb
			m++
			if m > 4000 {
				break
			}
		}
		for _, v := range b {
			if v > maxTier {
				maxTier = v
			}
		}
		sumMoves += m
		if m > moves {
			moves = m
		}
	}
	fmt.Printf("BENCH expectimax: %d games avg=%.0f moves best=%d maxTile=%d\n",
		games, float64(sumMoves)/float64(games), moves, int32(1)<<uint(maxTier))
}
