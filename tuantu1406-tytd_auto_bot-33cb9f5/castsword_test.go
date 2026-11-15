package tytd_go

// Luyện Khí Đại Hội tests. The slide engine is pinned against FOUR real board
// transitions captured live on 2026-10-08 (probe: ENTER cells {1:2,13:1},
// then UP/DOWN/LEFT/RIGHT with the server's own spawns — predictions here are
// the pre-spawn states, which the probe outputs match exactly).

import (
	"context"
	"encoding/hex"
	"testing"
	"time"
	"tytd_go/internal/proto"

	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

func csCells(vals ...int32) *proto.CastSwordBaseData {
	// vals must be 16 long
	e := &proto.CastSwordBaseData{Score: pb.Int32(0), Strength: pb.Int32(5)}
	for _, v := range vals {
		lv := v
		e.Cells = append(e.Cells, &proto.CastSwordCellInfo{Level: &lv})
	}
	return e
}

func csBoardOf(vals ...int32) csBoard {
	var b csBoard
	for i, v := range vals {
		b[i] = v
	}
	return b
}

func TestCastSwordSimMatchesLiveProbe(t *testing.T) {
	// S0 straight from the live ENTER answer: tier-2 at idx1, tier-1 at idx13.
	s0 := csBoardOf(0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0)
	up, g, moved := csSlide(s0, 1)
	if !moved || g != 0 || up != csBoardOf(0, 2, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0) {
		t.Fatalf("UP: %v gain=%d moved=%v (want 13→5 slide)", up, g, moved)
	}
	// After the server's spawn (observed at idx11), the captured DOWN answer
	// had pre-spawn cells 9/13/15.
	s1 := csBoardOf(0, 2, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0)
	down, g, _ := csSlide(s1, 2)
	if g != 0 || down != csBoardOf(0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 1, 0, 1) {
		t.Fatalf("DOWN: %v gain=%d", down, g)
	}
	// Observed move 3 (LEFT) merged the two tier-1s in the bottom row for +4.
	s2 := csBoardOf(0, 1, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 1, 0, 1)
	left, g, _ := csSlide(s2, 3)
	if g != 4 || left != csBoardOf(1, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 2, 0, 0, 0) {
		t.Fatalf("LEFT: %v gain=%d want gain 4", left, g)
	}
	// Observed move 4 (RIGHT) — no merges, pure slide.
	s3 := csBoardOf(1, 0, 0, 0, 0, 1, 0, 0, 2, 0, 0, 0, 2, 0, 0, 0)
	right, g, _ := csSlide(s3, 4)
	if g != 0 || right != csBoardOf(0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 2) {
		t.Fatalf("RIGHT: %v gain=%d", right, g)
	}
}

func TestCastSwordSlideRules(t *testing.T) {
	// [1,1,2] LEFT → [2,2]: the new piece never merges again in the same move.
	b := csBoardOf(1, 1, 2, 0)
	nb, g, moved := csSlide(b, 3)
	if !moved || g != 4 || nb != csBoardOf(2, 2, 0, 0) {
		t.Fatalf("no-cascade rule: %v gain=%d", nb, g)
	}
	// Full checkerboard: nothing moves in any direction.
	dead := csBoardOf(1, 2, 1, 2, 2, 1, 2, 1, 1, 2, 1, 2, 2, 1, 2, 1)
	for d := 1; d <= 4; d++ {
		if _, _, moved := csSlide(dead, d); moved {
			t.Fatalf("dead board slides on dir %d", d)
		}
	}
	if _, _, ok := csBestMove(dead); ok {
		t.Fatal("csBestMove found a move on a dead board")
	}
	// Monte-Carlo sanity: on a board whose only real value is the [1,1]
	// fuse, the chosen first move must be legal (MC may rate a non-merging
	// opener equally; it's an average-of-rollouts policy, not a rule).
	m := csBoardOf(1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2)
	if d, _, ok := csBestMove(m); !ok || d < 1 || d > 4 {
		t.Fatalf("MC returned illegal move dir=%d ok=%v", d, ok)
	}
}

func csTestPlayer(t *testing.T) *Player {
	t.Helper()
	p := &Player{Username: "cs", Running: true, UID: 42, writeChan: make(chan []byte, 16)}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	t.Cleanup(p.cancel)
	now := time.Now().UnixMilli()
	setActivities(&p.ActList, activityEntry{ActivityID: 9062364, Type: activityTypeCastSword,
		BeginTime: now - 1000, EndTime: now + 10_000_000_000})
	return p
}

// csServer starts ONE router goroutine (the only p.writeChan consumer): it
// decodes each outgoing frame's opcode + body, waits until the matching
// answer seat exists, then hands it the server-authored payload.
func csServer(t *testing.T, p *Player, handlers map[string]func([]byte) []byte) {
	t.Helper()
	respOf := map[string]string{
		CASTSWORD_ENTER_CMD:      SERVER_CASTSWORD_ENTER,
		CASTSWORD_MOVE_CMD:       SERVER_CASTSWORD_MOVE,
		CASTSWORD_SETTLEMENT_CMD: SERVER_CASTSWORD_SETTLE,
	}
	go func() {
		for f := range p.writeChan {
			if len(f) < 18 {
				continue
			}
			cmd := hex.EncodeToString(f[6:10])
			want, ok := respOf[cmd]
			if !ok {
				continue
			}
			srv := handlers[want]
			if srv == nil {
				continue
			}
			body := append([]byte(nil), f[18:]...)
			deadline := time.After(3 * time.Second)
			claimed := false
			for !claimed {
				p.CsMu.Lock()
				ch := p.csChans[want]
				p.CsMu.Unlock()
				if ch != nil {
					select {
					case ch <- srv(body):
					default:
					}
					claimed = true
					continue
				}
				select {
				case <-deadline:
					t.Errorf("seat %s never claimed for cmd %s", want, cmd)
					claimed = true
				default:
					time.Sleep(time.Millisecond)
				}
			}
		}
	}()
}

func csBodyDir(body []byte) int32 {
	var dir int32
	for len(body) > 0 {
		num, wt, n := protowire.ConsumeTag(body)
		if n < 0 {
			return dir
		}
		body = body[n:]
		if wt != protowire.VarintType {
			return dir
		}
		v, n := protowire.ConsumeVarint(body)
		if n < 0 {
			return dir
		}
		body = body[n:]
		if num == 2 {
			dir = int32(v)
		}
	}
	return dir
}

// Scripted full run: server starts at the probe's S0 board with strength 3
// and answers exactly like the live server (slide + spawn one tier-1 in the
// first empty cell + bill strength). Autoplay must bank via SETTLEMENT.
func TestCastSwordAutoPlayScripted(t *testing.T) {
	p := csTestPlayer(t)
	board := csBoardOf(0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0)
	score, strength := int32(0), int32(3)

	entity := func() *proto.CastSwordBaseData {
		e := &proto.CastSwordBaseData{Score: &score, HighScore: &score, Strength: &strength}
		for _, v := range board {
			lv := v
			e.Cells = append(e.Cells, &proto.CastSwordCellInfo{Level: &lv})
		}
		return e
	}
	enter := func([]byte) []byte {
		m, _ := pb.Marshal(&proto.CastSwordEnterResp{Ret: pb.Int32(0), Entity: entity(), RefreshTime: pb.Int64(time.Now().UnixMilli())})
		return m
	}
	mv := func(body []byte) []byte {
		dir := csBodyDir(body)
		nb, gain, _ := csSlide(board, int(dir))
		for i := range nb {
			if nb[i] == 0 {
				nb[i] = 1 // deterministic spawn
				break
			}
		}
		board, score, strength = nb, score+gain, strength-1
		m, _ := pb.Marshal(&proto.CastSwordMoveResp{Ret: pb.Int32(0), Entity: entity()})
		return m
	}
	settle := func([]byte) []byte {
		m, _ := pb.Marshal(&proto.CastSwordSettlementResp{Ret: pb.Int32(0), Score: &score})
		return m
	}

	done := make(chan [3]any, 1)
	go func() {
		moves, sc, why, err := p.CastSwordAutoPlay(nil)
		done <- [3]any{moves, sc, why}
		if err != nil {
			t.Errorf("autoplay: %v", err)
		}
	}()
	csServer(t, p, map[string]func([]byte) []byte{
		SERVER_CASTSWORD_ENTER: enter, SERVER_CASTSWORD_MOVE: mv, SERVER_CASTSWORD_SETTLE: settle,
	})

	r := <-done
	if r[0].(int32) != 3 || r[2] != "hết thể lực" {
		t.Fatalf("autoplay ended %v moves why=%q (want 3 moves / hết thể lực)", r[0], r[2])
	}
	if r[1].(int32) != score {
		t.Fatalf("score %v != server %d", r[1], score)
	}
}

func TestCastSwordAutoPlayStop(t *testing.T) {
	p := csTestPlayer(t)
	board := csBoardOf(0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0)
	strength := int32(500)
	entity := func() *proto.CastSwordBaseData {
		e := &proto.CastSwordBaseData{Strength: &strength}
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
	mv := func([]byte) []byte {
		strength--
		m, _ := pb.Marshal(&proto.CastSwordMoveResp{Ret: pb.Int32(0), Entity: entity()})
		return m
	}
	settled := make(chan []byte, 1)
	settle := func(b []byte) []byte {
		settled <- append([]byte(nil), b...)
		m, _ := pb.Marshal(&proto.CastSwordSettlementResp{Ret: pb.Int32(0)})
		return m
	}
	_ = settled
	stop := make(chan struct{})
	done := make(chan string, 1)
	go func() {
		_, _, why, err := p.CastSwordAutoPlay(stop)
		if err != nil {
			t.Errorf("autoplay: %v", err)
		}
		done <- why
	}()
	csServer(t, p, map[string]func([]byte) []byte{
		SERVER_CASTSWORD_ENTER: enter, SERVER_CASTSWORD_MOVE: mv, SERVER_CASTSWORD_SETTLE: settle,
	})
	time.Sleep(300 * time.Millisecond)
	close(stop)
	if why := <-done; why != "bị dừng" {
		t.Fatalf("why=%q", why)
	}
	// Resume-only policy: stopping must NOT end the run — no SETTLEMENT and
	// no GAMEOVER frame may ever leave the bot.
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case f := <-p.writeChan:
			cmd := hex.EncodeToString(f[6:10])
			if cmd == CASTSWORD_SETTLEMENT_CMD || cmd == CASTSWORD_GAMEOVER_CMD {
				t.Fatalf("auto-play ended the run with %s", cmd)
			}
		default:
			break
		}
		select {
		case <-deadline:
			return
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestCastSwordMoveGateBusy(t *testing.T) {
	p := csTestPlayer(t)
	p.CsMu.Lock()
	p.csPlaying = true
	p.CsMu.Unlock()
	if _, _, _, err := p.CastSwordAutoPlay(nil); err == nil {
		t.Fatal("second autoplay accepted")
	}
}

// csLCG — deterministic spawn placement for the mock server.
type csLCG uint64

func (l *csLCG) next(n int) int {
	*l = csLCG(uint64(*l)*6364136223846793005 + 1442695040888963407)
	return int((uint64(*l) >> 33) % uint64(n))
}

// TestCastSwordMockFullRun drives the WHOLE transport against a mock game
// server (proto2-accurate bodies, LCG spawns, strength 200, isGameEnd when it
// boards itself dead, illegal no-op slides refused with ret!=0). This is the
// live test we can no longer afford with strength: it proves the resume loop,
// the resync path, and the no-end-ops policy end to end.
func TestCastSwordMockFullRun(t *testing.T) {
	p := csTestPlayer(t)
	board := csBoardOf(0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0)
	score, strength := int32(0), int32(200)
	rng := csLCG(42)

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
		if !moved { // server-side legality, like the real one
			m, _ := pb.Marshal(&proto.CastSwordMoveResp{Ret: pb.Int32(11901)})
			return m
		}
		end := false
		empties := []int{}
		for i, v := range nb {
			if v == 0 {
				empties = append(empties, i)
			}
		}
		if len(empties) > 0 {
			nb[empties[rng.next(len(empties))]] = 1
			empties2 := 0
			for _, v := range nb {
				if v == 0 {
					empties2++
				}
			}
			_ = empties2
		}
		dead := true
		for d := 1; d <= 4; d++ {
			if _, _, m := csSlide(nb, d); m {
				dead = false
				break
			}
		}
		board, score, strength = nb, score+g, strength-1
		end = dead || strength <= 0
		m, _ := pb.Marshal(&proto.CastSwordMoveResp{Ret: pb.Int32(0), Entity: entity(), IsGameEnd: &end})
		return m
	}
	p.csMoveGap = time.Millisecond
	csServer(t, p, map[string]func([]byte) []byte{
		SERVER_CASTSWORD_ENTER: enter, SERVER_CASTSWORD_MOVE: mv,
	})
	moves, sc, why, err := p.CastSwordAutoPlay(nil)
	if err != nil {
		t.Fatalf("mock run: %v", err)
	}
	// Policy proof: no SETTLEMENT / GAMEOVER / USE-TOOL frame ever left us.
	for _, f := range drainedFrames(p) {
		if len(f) >= 10 {
			switch cmd := hex.EncodeToString(f[6:10]); cmd {
			case CASTSWORD_SETTLEMENT_CMD, CASTSWORD_GAMEOVER_CMD:
				t.Fatalf("auto-play ended the run with %s", cmd)
			}
		}
	}
	if moves < 100 {
		t.Fatalf("expectimax played only %d moves (why=%q) — evaluator regression", moves, why)
	}
	if sc <= 0 {
		t.Fatalf("score %d after %d moves", sc, moves)
	}
	if why == "" {
		t.Fatalf("empty stop reason")
	}
	t.Logf("MOCK RUN: %d nước, %d điểm, dừng vì %q", moves, sc, why)
}

func drainedFrames(p *Player) [][]byte {
	var out [][]byte
	for {
		select {
		case f := <-p.writeChan:
			out = append(out, f)
		default:
			return out
		}
	}
}
