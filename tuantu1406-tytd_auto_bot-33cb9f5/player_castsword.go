package tytd_go

// Luyện Khí Đại Hội (remoteCastSword, activity type 119) — a 2048-style
// weapon-merge board played on the game server. Board is 4x4 ROW-MAJOR;
// every cell stores a tier (0 empty, n = weapon worth 2^n); a MOVE slides
// all pieces one direction (1=UP, 2=DOWN, 3=LEFT, 4=RIGHT — pinned by
// replaying the live probe's board transitions), equal neighbours fuse into
// one tier-(n+1) piece and the run score grows by 2^(n+1). The server owns
// the run: it charges 1 strength per move, secretly spawns fresh tier-1
// pieces, and ends the board when strength is gone.
//
// Requests are proto2 with REQUIRED fields (same trap bigchess documents:
// generated proto3 bodies drop zero values and the server silently ignores
// the packet), so bodies are hand-encoded here; answers parse with the
// generated types.
//
// Auto-play policy: FREE moves only. The tool ops (swap/up/break = items
// 105098..105101) and REFRESHSTRENGTH are never sent — permanent item ban
// ([[tytd-item-ban]]); the tool counts on BaseData are read-only display.

import (
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

const activityTypeCastSword int32 = 119

// csBoardSize: 4x4 row-major.
const csBoardSize = 16

// csMaxTier: the client's effect pool stops at merge11 ⇒ two tier-11 (2048)
// pieces do NOT fuse further — the server has no merge12.
const csMaxTier = 11

var (
	errCsBusy    = errors.New("another cast-sword action is already in flight for this account")
	errCsTimeout = errors.New("cast-sword: game server did not answer, retry after checking state")
	errCsGone    = errors.New("cast-sword: account connection dropped mid-request")
	errCsClosed  = errors.New("cast-sword: event not open for this account")
)

// csError carries the server's refusal code (ret != 0).
type csError struct {
	Cmd string
	Ret int32
}

func (e *csError) Error() string {
	return fmt.Sprintf("cast-sword: game server refused %s (ret=%d)", e.Cmd, e.Ret)
}

// csRunState is the last server-authoritative snapshot of one run.
type csRunState struct {
	ActivityID int32
	Cells      [csBoardSize]int32 // tiers, row-major
	Score      int32
	HighScore  int32
	Strength   int32 // remaining moves this window
	GameEnd    bool
	UpdatedAt  time.Time
}

func (p *Player) csState() *csRunState {
	p.CsMu.Lock()
	defer p.CsMu.Unlock()
	if p.cs == nil {
		p.cs = &csRunState{}
	}
	return p.cs
}

// csActiveID resolves the live Luyện Khí instance from the pushed activity
// list (0/err when closed).
func (p *Player) csActiveID() (int32, error) {
	now := time.Now().UnixMilli()
	for _, e := range p.ActList.byType(activityTypeCastSword) {
		if e.BeginTime <= now && now < e.EndTime {
			return e.ActivityID, nil
		}
	}
	return 0, errCsClosed
}

// ---- request bodies (hand-encoded: proto2 required fields) ---------------

func csBodyActivity(id int32) []byte {
	b := protowire.AppendTag(nil, 1, protowire.VarintType)
	return protowire.AppendVarint(b, uint64(uint32(id)))
}

func csMoveBody(id, dir int32) []byte {
	b := csBodyActivity(id)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	return protowire.AppendVarint(b, uint64(uint32(dir)))
}

// ---- answer seats (bcClaim/bcDeliver pattern) ------------------------------

func (p *Player) csClaim(msgType string) (chan []byte, error) {
	p.CsMu.Lock()
	defer p.CsMu.Unlock()
	if p.csChans == nil {
		p.csChans = map[string]chan []byte{}
	}
	if _, busy := p.csChans[msgType]; busy {
		return nil, errCsBusy
	}
	ch := make(chan []byte, 1)
	p.csChans[msgType] = ch
	return ch, nil
}

func (p *Player) csRelease(msgType string, ch chan []byte) {
	p.CsMu.Lock()
	defer p.CsMu.Unlock()
	if cur, ok := p.csChans[msgType]; ok && cur == ch {
		delete(p.csChans, msgType)
	}
}

// csDeliver hands one answer to its seat. Reader-goroutine only: never block.
func (p *Player) csDeliver(msgType string, data []byte) {
	p.CsMu.Lock()
	ch := p.csChans[msgType]
	p.CsMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

func (p *Player) csRoundTrip(cmd, wantType string, body []byte, timeout time.Duration) ([]byte, error) {
	ch, err := p.csClaim(wantType)
	if err != nil {
		return nil, err
	}
	defer p.csRelease(wantType, ch)
	p.write(pack.Pack(cmd, body, p.UID))
	var ctxDone <-chan struct{}
	if p.ctx != nil {
		ctxDone = p.ctx.Done()
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, errCsTimeout
	case <-ctxDone:
		return nil, errCsGone
	}
}

// ---- server round trips ----------------------------------------------------

func csApplyBase(st *csRunState, e *proto.CastSwordBaseData) {
	if e == nil {
		return
	}
	st.Score = e.GetScore()
	st.HighScore = e.GetHighScore()
	st.Strength = e.GetStrength()
	cells := e.GetCells()
	if len(cells) == csBoardSize {
		for i, c := range cells {
			st.Cells[i] = c.GetLevel()
		}
	}
	st.UpdatedAt = time.Now()
}

// RequestCastSwordEnter pulls the authoritative board (+score/strength).
func (p *Player) RequestCastSwordEnter() (*proto.CastSwordEnterResp, error) {
	id, err := p.csActiveID()
	if err != nil {
		return nil, err
	}
	data, err := p.csRoundTrip(CASTSWORD_ENTER_CMD, SERVER_CASTSWORD_ENTER, csBodyActivity(id), 15*time.Second)
	if err != nil {
		return nil, err
	}
	var rsp proto.CastSwordEnterResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, fmt.Errorf("cast-sword enter decode: %w", err)
	}
	if rsp.GetRet() != 0 {
		return &rsp, &csError{Cmd: "enter", Ret: rsp.GetRet()}
	}
	st := p.csState()
	p.CsMu.Lock()
	st.ActivityID = id
	csApplyBase(st, rsp.GetEntity())
	p.CsMu.Unlock()
	return &rsp, nil
}

// RequestCastSwordMove sends one slide and folds the answered board in.
func (p *Player) RequestCastSwordMove(dir int32) (*proto.CastSwordMoveResp, error) {
	id, err := p.csActiveID()
	if err != nil {
		return nil, err
	}
	data, err := p.csRoundTrip(CASTSWORD_MOVE_CMD, SERVER_CASTSWORD_MOVE, csMoveBody(id, dir), 15*time.Second)
	if err != nil {
		return nil, err
	}
	var rsp proto.CastSwordMoveResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, fmt.Errorf("cast-sword move decode: %w", err)
	}
	if rsp.GetRet() != 0 {
		return &rsp, &csError{Cmd: "move", Ret: rsp.GetRet()}
	}
	st := p.csState()
	p.CsMu.Lock()
	csApplyBase(st, rsp.GetEntity())
	if rsp.GetIsGameEnd() {
		st.GameEnd = true
	}
	p.CsMu.Unlock()
	return &rsp, nil
}

// RequestCastSwordSettlement ends the run and banks the score. AUTO-PLAY
// NEVER CALLS IT (user rule: no self-end) — kept only for a future explicit
// command if the owner ever asks for one.
func (p *Player) RequestCastSwordSettlement() (*proto.CastSwordSettlementResp, error) {
	id, err := p.csActiveID()
	if err != nil {
		return nil, err
	}
	data, err := p.csRoundTrip(CASTSWORD_SETTLEMENT_CMD, SERVER_CASTSWORD_SETTLE, csBodyActivity(id), 15*time.Second)
	if err != nil {
		return nil, err
	}
	var rsp proto.CastSwordSettlementResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, fmt.Errorf("cast-sword settlement decode: %w", err)
	}
	if rsp.GetRet() != 0 {
		return &rsp, &csError{Cmd: "settlement", Ret: rsp.GetRet()}
	}
	return &rsp, nil
}

// ---- pure board engine (unit-tested against the live probe transitions) --

type csBoard [csBoardSize]int32

// csSlide applies one classic-2048 move to a fresh copy. gain = score delta
// (merging two tier-n pieces pays 2^(n+1)); moved=false when nothing shifts.
func csSlide(b csBoard, dir int) (csBoard, int32, bool) {
	out := b
	var gain int32
	line := make([]int32, 4)
	slideLine := func(in []int32) ([4]int32, int32) {
		var res [4]int32
		var g int32
		vals := make([]int32, 0, 4)
		for _, v := range in {
			if v != 0 {
				vals = append(vals, v)
			}
		}
		w := 0
		for i := 0; i < len(vals); i++ {
			if i+1 < len(vals) && vals[i] == vals[i+1] && vals[i] < csMaxTier {
				res[w] = vals[i] + 1
				g += int32(1) << (vals[i] + 1)
				i++
			} else {
				res[w] = vals[i]
			}
			w++
		}
		return res, g
	}
	for k := 0; k < 4; k++ {
		for i := 0; i < 4; i++ {
			switch dir {
			case 1: // UP: column k read top→bottom
				line[i] = b[i*4+k]
			case 2: // DOWN: column k read bottom→top
				line[i] = b[(3-i)*4+k]
			case 3: // LEFT: row k left→right
				line[i] = b[k*4+i]
			case 4: // RIGHT: row k right→left
				line[i] = b[k*4+(3-i)]
			}
		}
		res, g := slideLine(line)
		gain += g
		for i := 0; i < 4; i++ {
			switch dir {
			case 1:
				out[i*4+k] = res[i]
			case 2:
				out[(3-i)*4+k] = res[i]
			case 3:
				out[k*4+i] = res[i]
			case 4:
				out[k*4+(3-i)] = res[i]
			}
		}
	}
	return out, gain, out != b
}

// csEval scores a board for the search (classic 2048 heuristics, all on the
// raw tier numbers):
//   - gradient: 4^rank corner matrix (Li & Hill), oriented so its brightest
//     corner follows the current biggest piece — this is what keeps the
//     snake tidy instead of scattering merges;
//   - monotonicity: per row/col the better of the two sweep directions;
//   - smoothness: absolute tier differences of orthogonal neighbours;
//   - freedom: empty cells (quadratic — a full board is a dead board).
func csEval(b csBoard) int64 {
	base := [16]int32{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	var grad, mono, smooth int64
	var empty int64
	linePen := func(vals [4]int32) int64 {
		// reward the better of increasing / decreasing sweeps
		var inc, dec int64
		for i := 0; i < 3; i++ {
			if vals[i+1] > vals[i] {
				inc += int64(vals[i+1] - vals[i])
			} else {
				dec += int64(vals[i] - vals[i+1])
			}
		}
		if inc > dec {
			return inc
		}
		return dec
	}
	for i, v := range b {
		r, c := i/4, i%4 // FIXED corner (top-left): flipping the matrix every
		// time the max tile moves shreds the snake structure — strong human
		// 4x4 play locks the ladder into one corner permanently.
		if v == 0 {
			empty++
			continue
		}
		grad += int64(1) << uint(base[r*4+c]-1) * int64(v) // linear-ish: a 2048 must NOT drown the open-cell term
		if i%4 < 3 && b[i+1] != 0 {
			smooth += int64(csAbs32(v - b[i+1]))
		}
		if i < csBoardSize-4 && b[i+4] != 0 {
			smooth += int64(csAbs32(v - b[i+4]))
		}
	}
	for k := 0; k < 4; k++ {
		var rw, cl [4]int32
		for i := 0; i < 4; i++ {
			rw[i] = b[k*4+i]
			cl[i] = b[i*4+k]
		}
		mono += linePen(rw) + linePen(cl)
	}
	return grad + mono*5000 - smooth*2000 + empty*empty*30000
}

func csAbs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// csExpectimax: MAX over our four slides, CHANCE over the server's hidden
// spawn (observed: exactly ONE tier-1 per move, any empty cell). depth counts
// remaining MAX plies. Accumulated fuse-pay enters the leaf directly.
func csExpectimax(b csBoard, depth int, gained int64) int64 {
	empties := 0
	for _, v := range b {
		if v == 0 {
			empties++
		}
	}
	if depth == 0 || empties == 0 {
		return csEval(b) + gained*4
	}
	best := int64(-1 << 62)
	any := false
	for d := 1; d <= 4; d++ {
		nb, g, moved := csSlide(b, d)
		if !moved {
			continue
		}
		any = true
		var val int64
		n := 0
		for i, v := range nb {
			if v != 0 {
				continue
			}
			sp := nb
			sp[i] = 1
			val += csExpectimax(sp, depth-1, gained+int64(g))
			n++
		}
		if n == 0 {
			val = csEval(nb) + (gained+int64(g))*4
		} else {
			val /= int64(n)
		}
		if val > best {
			best = val
		}
	}
	if !any {
		return int64(-1 << 60)
	}
	return best
}

// csBestMove is a MaxScore Monte-Carlo: for each legal first direction run
// N random playouts to board-end and average the FINAL score; pick the
// direction with the best average. This mirrors the classic 2048 MC solver
// (random-rollout, average-final-score) which out-plays greedy/heuristic
// search on 4x4 boards — rollouts "see" starvation deaths that shallow
// expectimax misses. rng must not be shared across goroutines.
// csRolloutCount = MC playouts per direction (prod 300; tests dial down for
// wall-clock — the policy ranking is identical, just noisier).
var csRolloutCount = 300

// csPolicy picks the move chooser for benchmarks: 0 = MC (production),
// 1 = expectimax + snake powers, 2 = MC with snake-heuristic rollouts.
var csPolicy = 1

// csSnakeW: classic Li & Hill style static — fixed snake powers of 4 over the
// 16 cells (row-major snake), so a tidy monotone ladder scores exponentially
// higher than any scatter of the same tiles. This is the prior humans carry.
var csSnakeW = func() [csBoardSize]int64 {
	var w [csBoardSize]int64
	path := [16]int{0, 1, 2, 3, 7, 6, 5, 4, 8, 9, 10, 11, 15, 14, 13, 12}
	for rank, cell := range path {
		w[cell] = int64(1) << uint(2*(15-rank)) // 4^(15-rank)
	}
	return w
}()

// csEvalSnake = snake-weighted structure + freedom.
func csEvalSnake(b csBoard) int64 {
	var sc, empty int64
	for i, v := range b {
		if v == 0 {
			empty++
			continue
		}
		sc += csSnakeW[i] * int64(v)
	}
	return sc + empty*empty*(1<<20)
}

// csD4: the 8 square symmetries as row-major index permutations. csEvalSnake
// pins the snake to one corner; humans walk the ladder into WHICHEVER corner
// the board fell toward, so the search should score a board by its best
// orientation. Score = max over symmetries of the plain snake eval.
var csD4 = func() [8][csBoardSize]int {
	var g [8][csBoardSize]int
	for k := 0; k < 8; k++ {
		rot := k % 4   // 90° rotations
		mir := k / 4   // then optional horizontal mirror
		for r := 0; r < 4; r++ {
			for c := 0; c < 4; c++ {
				rr, cc := r, c
				switch rot {
				case 1:
					rr, cc = c, 3-r
				case 2:
					rr, cc = 3-r, 3-c
				case 3:
					rr, cc = 3-c, r
				}
				if mir == 1 {
					cc = 3 - cc
				}
				g[k][r*4+c] = rr*4 + cc
			}
		}
	}
	return g
}()

// csSnakeSym toggles the symmetric eval. MEASURED WORSE (Lv11 0.70→0.40 on
// 10 identical seeds): max-over-orientations lets the search flip the board's
// "reference corner" every ply, so no stable corner ever forms. The fixed
// corner is what makes the ladder commit. Kept for experiments, OFF by default.
var csSnakeSym = false

func csEvalSnakeSym(b csBoard) int64 {
	if !csSnakeSym {
		return csEvalSnake(b)
	}
	best := int64(-1) << 62
	for k := 0; k < 8; k++ {
		var t csBoard
		for i := 0; i < csBoardSize; i++ {
			t[i] = b[csD4[k][i]]
		}
		if v := csEvalSnake(t); v > best {
			best = v
		}
	}
	return best
}

// csSpawnTiers / csSpawnPW: the server's spawn distribution, CALIBRATED from
// 107 live-recorded spawns (2026-10-08 live calibration run): positions are
// uniform over post-slide empty cells (χ²=17.2, df≈15 → consistent), tiers
// are {2:88.8%, 4:10.3%, 8:0.9%} = tier {1:.888, 2:.103, 3:.009}. PW = parts
// per 1000. Expectimax chance nodes branch over these, not just tier-1.
var (
	csSpawnTiers = [3]int32{1, 2, 3}
	csSpawnPW    = [3]int64{888, 103, 9}
)

// csSnakeDepth: MAX plies for the production snake search (3 keeps the whole
// 5000-move run comfortably interactive; benchmarks probe deeper).
var csSnakeDepth = 3

// csChanceSpawn averages the child value over the calibrated spawn model
// (one piece per move: cell uniform over empties, tier per csSpawnPW).
// depthLeft/gained are passed through to the recursion callback via closure
// arguments.
func csExpectimaxSnake(b csBoard, depth int, gained int64) int64 {
	if depth == 0 {
		return csEvalSnakeSym(b) + gained*(1<<24)
	}
	empties := 0
	for _, v := range b {
		if v == 0 {
			empties++
		}
	}
	if empties == 0 {
		for d := 1; d <= 4; d++ {
			if _, _, m := csSlide(b, d); m {
				empties = 1 // still playable via merges
				break
			}
		}
		if empties == 0 {
			return -(1 << 40) // survives the ×pw(≤1000) weighting at parents
		}
	}
	best := int64(-1) << 62
	for d := 1; d <= 4; d++ {
		nb, g, moved := csSlide(b, d)
		if !moved {
			continue
		}
		if depth == 1 {
			if v := csEvalSnakeSym(nb) + (gained+int64(g))*(1<<24); v > best {
				best = v
			}
			continue
		}
		var val int64
		var n int64 // weight denominator
		for i, v := range nb {
			if v != 0 {
				continue
			}
			for t, tier := range csSpawnTiers {
				sp := nb
				sp[i] = tier
				val += csSpawnPW[t] * csExpectimaxSnake(sp, depth-1, gained+int64(g))
			}
			n += 1000
		}
		if n == 0 {
			val = 1000 * csExpectimaxSnake(nb, depth-1, gained+int64(g))
			n = 1000
		}
		if v := val / n; v > best {
			best = v
		}
	}
	return best
}

func csBestMoveSnake(b csBoard) (dir int32, gain int32, ok bool) {
	best := int64(-1) << 62
	for d := int32(1); d <= 4; d++ {
		nb, g, moved := csSlide(b, int(d))
		if !moved {
			continue
		}
		var val, n int64
		for i, v := range nb {
			if v != 0 {
				continue
			}
			for t, tier := range csSpawnTiers {
				sp := nb
				sp[i] = tier
				val += csSpawnPW[t] * csExpectimaxSnake(sp, csSnakeDepth-1, int64(g))
			}
			n += 1000
		}
		if n == 0 {
			val = 1000 * (csEvalSnakeSym(nb) + int64(g)*(1<<24))
			n = 1000
		}
		if v := val / n; v > best {
			best, dir, gain, ok = v, d, g, true
		}
	}
	return
}

func csBestMoveRng(b csBoard, rng *rand.Rand) (dir int32, gain int32, ok bool) {
	rollouts := csRolloutCount
	best := float64(-1)
	for d := int32(1); d <= 4; d++ {
		nb, g, moved := csSlide(b, int(d))
		if !moved {
			continue
		}
		var total float64
		for r := 0; r < rollouts; r++ {
			if csPolicy == 2 {
				total += float64(csRolloutHeur(nb))
			} else {
				total += float64(csRollout(nb, rng))
			}
		}
		avg := total / float64(rollouts)
		if avg > best {
			best, dir, gain, ok = avg, d, g, true
		}
	}
	return
}

// csChoose is the autoplay entry (per-run rng; rand.Rand is not
// goroutine-safe across accounts).
func csChoose(b csBoard, rng *rand.Rand) (dir int32, gain int32, ok bool) {
	switch csPolicy {
	case 1:
		return csBestMoveSnake(b)
	case 3:
		return csBestMoveRot(b, csRotation)
	default:
		return csBestMoveRng(b, rng)
	}
}

// csRotation = the human "locked-corner" scheme: a repeating direction cycle
// (e.g. DOWN,LEFT,UP,LEFT pins the max tile to the bottom-left corner and
// forces tidy snakes). csBestMoveRot plays the first LEGAL move of the cycle,
// cycling to the next preferred direction when one is blocked — the classic
// long-lived 2048 strategy, deterministic and nearly free.
var csRotation = []int32{2, 3, 1, 3}

func csBestMoveRot(b csBoard, cycle []int32) (dir int32, gain int32, ok bool) {
	for _, d := range cycle {
		if nb, g, moved := csSlide(b, int(d)); moved {
			_ = nb
			return d, g, true
		}
	}
	for d := int32(1); d <= 4; d++ {
		if _, g, moved := csSlide(b, int(d)); moved {
			return d, g, true
		}
	}
	return 0, 0, false
}

// csBestMove routes through the policy selector with a fixed-seed rng (tests
// + benchmarks; deterministic).
func csBestMove(b csBoard) (dir int32, gain int32, ok bool) {
	if csPolicy == 1 {
		return csBestMoveSnake(b)
	}
	return csBestMoveRng(b, csDefaultRng)
}

// csRolloutHeur plays out with a 1-ply snake-eval greedy policy (tie → lowest
// dir index): the "competent player" model whose averages actually rank
// openings, unlike uniformly random rollouts.
func csRolloutHeur(b csBoard) int32 {
	score := int32(0)
	for {
		empties := 0
		for _, v := range b {
			if v == 0 {
				empties++
			}
		}
		if empties > 0 {
			// worst-case spawn for the snake policy: fill the brightest empty
			// cell it wants to keep free.
			worst, wv := -1, int64(-1)
			for i, v := range b {
				if v == 0 && csSnakeW[i] > wv {
					worst, wv = i, csSnakeW[i]
				}
			}
			b[worst] = 1
		}
		best, bd := int64(-1)<<60, int32(0)
		var bg int32
		for d := int32(1); d <= 4; d++ {
			nb, g, moved := csSlide(b, int(d))
			if !moved {
				continue
			}
			if v := csEvalSnake(nb) + int64(g)*(1<<24); v > best {
				best, bd, bg = v, d, g
			}
		}
		if bd == 0 {
			mt := int32(0)
			for _, v := range b {
				if v > mt {
					mt = v
				}
			}
			return score + (int32(1) << uint(mt))
		}
		b = func() csBoard { x, _, _ := csSlide(b, int(bd)); return x }()
		score += bg
	}
}

var csDefaultRng = rand.New(rand.NewSource(1))

// csRollout plays the board out to death and returns the accumulated
// fuse-pay plus a max-tier bonus. The rollout policy is a greedy/random MIX:
// 50% of the time it fuses with the highest-gain move, otherwise random —
// pure-random rollouts rate near-full boards far too pessimistically (every
// line "looks" like death), which starves the signal the main decision needs.
func csRollout(b csBoard, rng *rand.Rand) int32 {
	score := int32(0)
	for {
		// server spawn: one tier-1 into a random empty cell
		empties := 0
		for _, v := range b {
			if v == 0 {
				empties++
			}
		}
		if empties > 0 {
			k := rng.Intn(empties)
			for i := range b {
				if b[i] == 0 {
					if k == 0 {
						b[i] = 1
						break
					}
					k--
				}
			}
		}
		// pick a random legal direction
		var legal [4]int32
		n := 0
		for d := int32(1); d <= 4; d++ {
			if nb, _, moved := csSlide(b, int(d)); moved {
				_ = nb
				legal[n] = d
				n++
			}
		}
		if n == 0 {
			mt := int32(0)
			for _, v := range b {
				if v > mt {
					mt = v
				}
			}
			return score + (int32(1) << uint(mt))
		}
		var d int32
		if rng.Intn(2) == 0 {
			// greedy leg: biggest immediate fuse
			bg := int32(-1)
			for _, ld := range legal[:n] {
				if _, g, _ := csSlide(b, int(ld)); g > bg {
					bg, d = g, ld
				}
			}
		} else {
			d = legal[rng.Intn(n)]
		}
		var g int32
		b, g, _ = csSlide(b, int(d))
		score += g
	}
}

// csBoard returns the last answered board.
func (p *Player) csBoard() (csBoard, *csRunState) {
	st := p.csState()
	p.CsMu.Lock()
	defer p.CsMu.Unlock()
	return csBoard(st.Cells), st
}

// ---- auto-play -------------------------------------------------------------

// CastSwordAutoPlay runs the account's board to the end with FREE moves only:
// ENTER for the authoritative board, then repeatedly csBestMove → MOVE until
// strength runs out / the server ends the board / stop fires, and finally a
// SETTLEMENT so the score is banked. Returns moves played, the score reached
// and a short Vietnamese reason for stopping. Only one run per account at a
// time (csPlaying). The AI is deliberately deterministic and cheap — ~200
// moves of 4×4 lookahead is a lookup, not a search.
func (p *Player) CastSwordAutoPlay(stop <-chan struct{}) (int32, int32, string, error) {
	p.CsMu.Lock()
	if p.csPlaying {
		p.CsMu.Unlock()
		return 0, 0, "", errors.New("acc này đang tự chơi luyện khí rồi, đợi xong hoặc /lk_dung")
	}
	p.csPlaying = true
	p.CsMu.Unlock()
	defer func() {
		p.CsMu.Lock()
		p.csPlaying = false
		p.CsMu.Unlock()
	}()

	if _, err := p.RequestCastSwordEnter(); err != nil {
		return 0, 0, "", err
	}
	var ctxDone <-chan struct{}
	if p.ctx != nil {
		ctxDone = p.ctx.Done()
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano() ^ int64(p.UID)))
	_ = rng
	var moves int32
	resyncs := 0
	why := "xong ván"
	for {
		select {
		case <-stop:
			why = "bị dừng"
			goto settle
		case <-ctxDone:
			return moves, p.csState().Score, "rớt kết nối", nil
		default:
		}
		b, st := p.csBoard()
		if st.GameEnd {
			why = "hết bàn"
			goto settle
		}
		if st.Strength <= 0 {
			why = "hết thể lực"
			goto settle
		}
		dir, _, ok := csChoose(b, rng)
		if !ok {
			why = "bí thế"
			goto settle
		}
		if _, err := p.RequestCastSwordMove(dir); err != nil {
			if _, isRet := err.(*csError); isRet {
				why = "server từ chối"
				goto settle
			}
			// Transport timeout: the move may STILL have been applied
			// server-side (live 2026-10-08: 70 moves in, one answer lost).
			// Re-pull the authoritative board instead of trusting a guess;
			// give up only after repeated resync failures.
			resyncs++
			if resyncs > 3 {
				return moves, st.Score, "mất phản hồi liên tục", err
			}
			if _, e := p.RequestCastSwordEnter(); e != nil {
				return moves, st.Score, "mất kết nối ván", e
			}
			continue
		}
		resyncs = 0
		moves++
		// The client locks input ~800 ms per slide (isMoveOver timer); stay
		// slightly slower so the server never sees stacked moves. Tests set
		// csMoveGap to skip the wait.
		gap := p.csMoveGap
		if gap == 0 {
			gap = 700 * time.Millisecond
		}
		t := time.NewTimer(gap)
		select {
		case <-stop:
			t.Stop()
			why = "bị dừng"
			goto settle
		case <-ctxDone:
			t.Stop()
			return moves, p.csState().Score, "rớt kết nối", nil
		case <-t.C:
		}
	}
settle:
	// NO SETTLEMENT / GAMEOVER is ever sent by auto-play (user rule
	// 2026-10-08): entering a half-played board must CONTINUE it, and ending
	// the run is the owner's call. ENTER here is only the client's
	// "基础数据请求" (base-data read) — it never resets anything; the server
	// auto-banks at strength 0 anyway (live: SETTLEMENT then refuses
	// ret=11910). A manually stopped run simply stays open for /start_luyenkhi
	// (or /lk_choi) to resume later.
	st := p.csState()
	return moves, st.Score, why, nil
}

// csBoardText renders the board for Telegram status lines (tier numbers).
func csBoardText(b csBoard) string {
	var sb []byte
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			v := b[r*4+c]
			cell := "·"
			if v > 0 {
				cell = strconv.Itoa(int(int32(1) << v))
			}
			sb = append(sb, fmt.Sprintf("%-6s", cell)...)
		}
		sb = append(sb, '\n')
	}
	return string(sb)
}
