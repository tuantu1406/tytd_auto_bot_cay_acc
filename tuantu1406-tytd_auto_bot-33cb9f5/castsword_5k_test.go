package tytd_go

// One-off long run: mock server with 5000 strength (user request), transport
// E2E like TestCastSwordMockFullRun.
import (
	"fmt"
	"os"

	pb "google.golang.org/protobuf/proto"
	"testing"
	"time"
	"tytd_go/internal/proto"
)

func TestCastSwordMock5K(t *testing.T) {
	if os.Getenv("TYTD_MC_BENCH") == "" {
		t.Skip("set TYTD_MC_BENCH=1 (long MC benchmark, minutes)")
	}
	csRolloutCount = 64
	defer func() { csRolloutCount = 300 }()
	for _, seed := range []uint64{1234, 777} {
		run5K(t, seed)
	}
}

// TestCastSwordPolicies head-to-head on identical 5000-strength boards:
// policy 0 = MC random-rollout (current), 1 = expectimax + snake powers,
// 2 = MC with snake-heuristic rollouts. TYTD_MC_BENCH=1.
func TestCastSwordPolicies(t *testing.T) {
	if os.Getenv("TYTD_MC_BENCH") == "" {
		t.Skip("set TYTD_MC_BENCH=1")
	}
	csRolloutCount = 64
	defer func() { csRolloutCount = 300 }()
	seeds := []uint64{1234, 777, 20260, 5150, 99999, 31337, 2468, 8080}
	for pol := 0; pol <= 2; pol++ {
		csPolicy = pol
		tot, lv11tot, best := 0, 0, 0
		for _, sd := range seeds {
			r := run5K(t, sd)
			tot += r[0]
			lv11tot += r[1]
			if r[1] > best {
				best = r[1]
			}
		}
		fmt.Printf("POLICY %d: avg %.0f moves/ván, Lv11 avg %.2f, best %d\n",
			pol, float64(tot)/float64(len(seeds)), float64(lv11tot)/float64(len(seeds)), best)
	}
	csPolicy = 0
}

func run5K(t *testing.T, seed uint64) [2]int { return runN(t, seed, 5000) }

func runN(t *testing.T, seed uint64, strengthInit int32) [2]int {
	t.Helper()
	p := csTestPlayer(t)
	board := csBoardOf(0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0)
	score, strength := int32(0), strengthInit
	rng := csLCG(seed)
	lv11 := 0
	maxTile := func(b csBoard) int32 {
		m := int32(0)
		for _, v := range b {
			if v > m {
				m = v
			}
		}
		return m
	}
	entity := func() *proto.CastSwordBaseData {
		e := &proto.CastSwordBaseData{Score: &score, HighScore: &score, Strength: &strength}
		for _, v := range board {
			lv := v
			e.Cells = append(e.Cells, &proto.CastSwordCellInfo{Level: &lv})
		}
		return e
	}
	enter := func([]byte) []byte {
		m, _ := pb.Marshal(&proto.CastSwordEnterResp{Ret: pb.Int32(0), Entity: entity()})
		return m
	}
	mv := func(body []byte) []byte {
		dir := csBodyDir(body)
		nb, g, moved := csSlide(board, int(dir))
		if !moved {
			m, _ := pb.Marshal(&proto.CastSwordMoveResp{Ret: pb.Int32(11901)})
			return m
		}
		empties := []int{}
		for i, v := range nb {
			if v == 0 {
				empties = append(empties, i)
			}
		}
		if len(empties) > 0 {
			// CALIBRATED live 2026-10-08 (107 recorded spawns): position is
			// uniform over post-slide empties (χ²=17.2/df15, consistent);
			// tier is NOT always 1 — 88.8% tier-1, 10.3% tier-2, 0.9% tier-3.
			tier := int32(1)
			switch k := rng.next(1000); {
			case k >= 991:
				tier = 3
			case k >= 888:
				tier = 2
			}
			nb[empties[rng.next(len(empties))]] = tier
		}
		dead := true
		for d := 1; d <= 4; d++ {
			if _, _, m := csSlide(nb, d); m {
				dead = false
				break
			}
		}
		// count LV11 (2048) tiles CREATED this move = max(0, after-before)
		before := 0
		for _, v := range board {
			if v == 11 {
				before++
			}
		}
		after := 0
		for _, v := range nb {
			if v == 11 {
				after++
			}
		}
		if after > before {
			lv11 += after - before
		}
		board, score, strength = nb, score+g, strength-1
		end := dead || strength <= 0
		m, _ := pb.Marshal(&proto.CastSwordMoveResp{Ret: pb.Int32(0), Entity: entity(), IsGameEnd: &end})
		return m
	}
	p.csMoveGap = time.Millisecond
	csServer(t, p, map[string]func([]byte) []byte{SERVER_CASTSWORD_ENTER: enter, SERVER_CASTSWORD_MOVE: mv})
	moves, sc, why, err := p.CastSwordAutoPlay(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, st := p.csBoard()
	t.Logf("MOCK 5K: %d nước, %d điểm, dừng vì %q, maxTile=%d, strength còn %d, Lv11(2048) tạo ra: %d, trên bàn lúc cuối: %d",
		moves, sc, why, int32(1)<<uint(maxTile(st.Cells)), st.Strength, lv11, func() int {
			n := 0
			for _, v := range st.Cells {
				if v == 11 {
					n++
				}
			}
			return n
		}())
	return [2]int{int(moves), lv11}
}

// TestCastSwordPoliciesShort: 2 seeds x 2500 strength, MC vs snake-expectimax
// only. Minutes, not forever.
func TestCastSwordPoliciesShort(t *testing.T) {
	if os.Getenv("TYTD_MC_BENCH") == "" {
		t.Skip("set TYTD_MC_BENCH=1")
	}
	csRolloutCount = 64
	defer func() { csRolloutCount = 300; csPolicy = 0 }()
	for _, pol := range []int{0, 1} {
		csPolicy = pol
		tot, lv := 0, 0
		for _, sd := range []uint64{1234, 777} {
			r := runN(t, sd, 2500)
			tot += r[0]
			lv += r[1]
		}
		fmt.Printf("POLICY %d: avg %.0f nước, Lv11 avg %.1f\n", pol, float64(tot)/2, float64(lv)/2)
	}
	csPolicy = 0
}
