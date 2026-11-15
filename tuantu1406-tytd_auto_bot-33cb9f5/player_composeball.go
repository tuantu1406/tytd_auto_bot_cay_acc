package tytd_go

// ComposeBall ("đại hội luyện khí" — suika drop-and-merge, activity TYPE
// 130) player for regular sessions. NOT a background loop (user directive
// 2026-10-04): a run starts ONLY when the user calls /start_thanlong, plays
// exactly one round and ends on hp=0 (stop immediately — user rule 2026-10-04
// final: "if hp=0 just stop"), GameOver (settle), a failure return code, the
// day lock, the upload cap, or a board with no legal placement even after FULL
// repack (leave it, notify, resumable via /start_thanlong) — always NOTIFY, no
// SETTLE on non-Game-Over stops (items are never used).
//
// ANTI-CHEAT MODEL (user, 2026-10-04): the server CROSS-CHECKS the board
// against the drops it authorised — a client that only spent two level-1
// drops can never push a board containing one level-3 ball. The planner
// here is therefore a strict ledger: every ball in an upload is either a
// drop paid for with hp or the product of an explicit composeData merge
// (two same-level balls out, one next-level ball in). Level counts
// conserve exactly, drop by drop. Live verification:
// TestLiveComposeBallAntiCheat.
//
// PLANNER RULES (user, 2026-10-04): after every drop, ALWAYS merge — the
// cascade is recursive: each merged ball that lands touching another
// same-config ball merges again, all claimed in the SAME upload (successive
// processBall snapshots). The game-over threshold LINE GATE IS DISABLED
// (user, "just disable the threadhold gate"): the packer places freely and
// the server decides GameOver (→ SETTLE + notify). "the friction is very
// small" — placement physics is the frictionless roll in cbDropAt. The line
// formula itself (cbLineY) is kept for logging only.
//
// Protocol: requests 213001..213005 → responses 32c9..32cd (%100000 rule);
// schema recovered from the CDN text asset (see internal/proto/tytd.proto).
// Seats follow the Big Chess bcChans pattern: one outstanding request per
// response type.

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const (
	activityTypeComposeBall int32 = 130

	cbCheckSalt = "!dG7s%ngSL5i8SsbBGiZri5^-JwgUifM+_sNRnJ)vbUQ*-G6)c2PhY=UwV1@6P6dtQvMzBu7(oLpU7lRx#q8IL0PK8XQsdecz7*_A0JMpKxHz8ZLqlN*TASXh6SC=5Q&_t7ZXt)Gv&jo1T$6V-ex7&4kJr#0)_&!"

	cbRTTimeout = 10 * time.Second

	// cbBeat: the 1-second beat the owner ordered 2026-10-25 ("server chưa
	// trả info event thì mày đã chạy, và mày chạy liên tục làm lỗi, nên
	// delay 1s") — paces ENTER, re-ENTERs and every upload so the run never
	// hammers the socket with a tight loop.
	cbBeat = time.Second
	// cbEnterTries: how many ENTERs to spend chasing a usable Linh Khí fold.
	// Owner bug 2026-10-25: ENTER answered hp=0 and the run died at once —
	// manually leaving and re-entering fixed it, so the run now re-enters
	// (ENTER loads the SAME board, never a restart) instead of quitting.
	cbEnterTries = 3
)

const (
	cbMaxUploadsTotal = 400 // one run's upload cap (hp runs out ~20-40 drops anyway)
	cbMaxMerges       = 64  // cascade planner safety cap
	cbLineMargin      = 5.0 // keep ball tops this far under the game-over line

	// Container geometry, live-probed on server 710547 (2026-10-04): ball
	// centres x∈(0..709] pass position validation, negative x answers 13017;
	// the floor line is ground = echoY − width/2 (cfg104 echoed -792.71637 at
	// width 119 → ground -852.21637). cbGroundFallback seeds a brand-new
	// round before any echo ball exists to anchor from.
	cbWallL          = 0.0
	cbWallPad        = 4.0
	cbRightWall      = 720.0
	cbGroundFallback = -979.74426
)

// cbBall is the ledger's board entry (physics-free; positions are px).
type cbBall struct {
	ID     int32
	Config int32
	X, Y   float64
}

type composeBallState struct {
	ActivityID int32
	CfgDone    bool
	CfgAt      time.Time

	LevelOf  map[int32]int32 // configId → merge level
	ConfigOf map[int32]int32 // level → configId
	ScoreOf  map[int32]int32 // level → score of forming it
	WidthOf  map[int32]int32 // level → ball diameter (px)

	Balls  []cbBall // authoritative board as last echoed by the server
	Ground float64  // floor line y: a resting ball's centre = Ground + width/2 (0 = unknown)
	// Right: inner right wall derived from echoes — LIVE PROOF 2026-10-04
	// (<live-user>/710564): balls echo with x + r up to 749.9, so the 720
	// constant is only a fallback; the real container is per-session.
	Right      float64 // max echoed (x + radius) over all balls (0 = unknown)
	NextID     int32   // server's next-ball counter
	NextConfig int32
	HP, CostHP int32
	HPTime     int64
	Score      int32
	MaxScore   int32
	GameOver   bool
	Have       bool   // board established for the current round
	Running    bool   // one-shot run in flight (set by cbStartOnce, cleared on exit)
	LockDay    string // ICT date the server closed this account's compose (own SETTLE, or a
	// 13002/13017 refusal — live probes 2026-10-04 show the whole day stays locked after)
}

// cbState returns (creating on demand) the player's compose-ball ledger.
func (p *Player) cbState() *composeBallState {
	p.CbMu.Lock()
	defer p.CbMu.Unlock()
	if p.cb == nil {
		p.cb = &composeBallState{}
	}
	return p.cb
}

// cbIsRunning reports whether a one-shot run is in flight (the Công tắc
// card's pill — the run has no OFF switch; truth is " ván đang chạy").
func (p *Player) cbIsRunning() bool {
	p.CbMu.Lock()
	defer p.CbMu.Unlock()
	return p.cb != nil && p.cb.Running
}

// cbStartOnce launches ONE one-shot ComposeBall run (user directive: it runs
// only when the user calls /start_thanlong, never as a background loop).
// Returns (started, busy): busy = CbMu was still held elsewhere and the
// command must not queue behind it. hp=0 stops the run at once (board kept,
// resumable); it ends (notify, no settle except GameOver) on hp=0, GameOver
// (settle+notify), a failure ret, the lock, or the cap.
//
// TryLock, not Lock: this runs on the TELEGRAM HANDLER goroutine, and the
// deployed 2026-10 binary planned the repack WHILE holding CbMu — one run
// mid-plan made /start_thanlong park on the mutex forever and the caller got
// no reply at all (customer report 2026-10-25 "/start_thanlong không trả
// lời"). The planner now runs off-lock (cbPlanUnlocked); TryLock is the
// belt-and-braces so no future lock holder can ever mute the command.
func (p *Player) cbStartOnce() (started, busy bool) {
	st := p.cbState()
	if !p.CbMu.TryLock() {
		log.Printf("[%s] [LuyenKhi] cbStartOnce: CbMu đang bận — báo busy, không xếp hàng sau lock", p.Username)
		return false, true
	}
	if st.Running {
		p.CbMu.Unlock()
		return false, false
	}
	st.Running = true
	p.CbMu.Unlock()
	go func() {
		defer func() {
			p.CbMu.Lock()
			st.Running = false
			p.CbMu.Unlock()
		}()
		p.cbRunOnce()
	}()
	return true, false
}

// cbPause waits d, ctx-aware: false = the session died mid-wait (the run
// must then bail, not keep pushing packets into a dead socket).
func (p *Player) cbPause(d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-p.ctx.Done():
		return false
	}
}

// cbRunOut reports whether the ledger says the run should not upload again.
// One drop costs ONE Linh Khí (user, 2026-10-04; live-proven the same day: a
// drop was ACCEPTED at hp=62 while the server sent costHp=63 — costHp is NOT
// the drop price and composes are not gated on hp>=costHp; it is some
// server-side bookkeeping value that merely ticked 63→64 with the drop).
func (st *composeBallState) cbRunOut() (over, noHP bool) {
	over = st.GameOver || !st.Have
	noHP = st.HP < 1
	return
}

// ---- activity id + config --------------------------------------------------

// ComposeBallActivityID is the live instance id of an OPEN ComposeBall
// window (0 = closed). The generic activityStore already tracks type-130
// entries from the login push / 21036 syncs.
func (p *Player) ComposeBallActivityID() int32 {
	now := time.Now()
	for _, e := range p.ActList.byType(activityTypeComposeBall) {
		if e.status(now) == "active" {
			return e.ActivityID
		}
	}
	return 0
}

// cbRequestConfig pulls the activity detail (21003) at most once per 10 min
// until the config table parses; the answer folds in via cbMergeConfig.
func (p *Player) cbRequestConfig(id int32) {
	st := p.cbState()
	p.CbMu.Lock()
	fresh := st.CfgDone || time.Since(st.CfgAt) < 10*time.Minute
	st.CfgAt = time.Now()
	p.CbMu.Unlock()
	if fresh {
		return
	}
	payload, err := activityPollFrame(id)
	if err != nil {
		return
	}
	p.write(pack.Pack(ACTIVITY_GET_DATA_CMD, payload, p.UID))
}

// cbMergeConfig consumes every SERVER_ACTIVITY_DETAIL answer; only type-130
// rows carry the ComposeBallConfigSync table in uniqueConfig: level ↔
// configId ↔ score/width per ball tier.
func (p *Player) cbMergeConfig(data []byte) {
	var rsp proto.RspGetActivityDetail
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return
	}
	dc := rsp.GetActivity().GetDetailConfig()
	if dc.GetCommonConfig().GetMainConfig().GetType() != activityTypeComposeBall {
		return
	}
	var sync proto.ComposeBallConfigSync
	if err := pb.Unmarshal(dc.GetUniqueConfig(), &sync); err != nil || len(sync.GetConfigs()) == 0 {
		return
	}
	st := p.cbState()
	p.CbMu.Lock()
	st.LevelOf, st.ConfigOf = map[int32]int32{}, map[int32]int32{}
	st.ScoreOf, st.WidthOf = map[int32]int32{}, map[int32]int32{}
	for _, c := range sync.GetConfigs() {
		if c.GetId() == 0 || c.GetLevel() == 0 {
			continue
		}
		st.LevelOf[c.GetId()] = c.GetLevel()
		if _, dup := st.ConfigOf[c.GetLevel()]; !dup {
			st.ConfigOf[c.GetLevel()] = c.GetId()
		}
		st.ScoreOf[c.GetLevel()] = c.GetScore()
		st.WidthOf[c.GetLevel()] = c.GetWidth()
	}
	st.CfgDone = true
	p.CbMu.Unlock()
	log.Printf("[%s] [LuyenKhi] config: %d tiers", p.Username, len(sync.GetConfigs()))
}

// ---- answer seats (Big Chess bcClaim/bcRelease/bcDeliver pattern) --------

var (
	errCbBusy    = fmt.Errorf("có yêu cầu luyện khí đang chờ, thử lại sau nhé")
	errCbTimeout = fmt.Errorf("server không trả lời luyện khí, thử lại sau")
	errCbGone    = fmt.Errorf("rớt kết nối server rồi đạo hữu")
)

func (p *Player) cbClaim(msgType string) (chan []byte, error) {
	p.CbMu.Lock()
	defer p.CbMu.Unlock()
	if p.cbChans == nil {
		p.cbChans = map[string]chan []byte{}
	}
	if _, busy := p.cbChans[msgType]; busy {
		return nil, errCbBusy
	}
	ch := make(chan []byte, 1)
	p.cbChans[msgType] = ch
	return ch, nil
}

func (p *Player) cbRelease(msgType string, ch chan []byte) {
	p.CbMu.Lock()
	defer p.CbMu.Unlock()
	if cur, ok := p.cbChans[msgType]; ok && cur == ch {
		delete(p.cbChans, msgType)
	}
}

// cbDeliver hands one answer to the seat waiting for its type, if any.
// Reader goroutine — must never block.
func (p *Player) cbDeliver(msgType string, data []byte) {
	p.CbMu.Lock()
	ch := p.cbChans[msgType]
	p.CbMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

func (p *Player) cbRoundTrip(cmd, wantType string, body []byte, timeout time.Duration) ([]byte, error) {
	ch, err := p.cbClaim(wantType)
	if err != nil {
		return nil, err
	}
	defer p.cbRelease(wantType, ch)
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
	case <-p.ctx.Done():
		return nil, errCbGone
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, errCbTimeout
	case <-p.ctx.Done():
		return nil, errCbGone
	}
}

// ---- wire requests ---------------------------------------------------------

func (p *Player) RequestComposeBallEnterGame(timeout time.Duration) (*proto.ComposeBallEnterGameResp, error) {
	id := p.ComposeBallActivityID()
	if id == 0 {
		return nil, fmt.Errorf("đại hội luyện khí chưa mở")
	}
	body, _ := pb.Marshal(&proto.ComposeBallEnterGameReq{ActivityId: &id})
	raw, err := p.cbRoundTrip(COMPOSE_BALL_ENTER_CMD, SERVER_COMPOSE_BALL_ENTER, body, timeout)
	if err != nil {
		return nil, err
	}
	var resp proto.ComposeBallEnterGameResp
	if err := pb.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RequestComposeBallCompose uploads the whole board (drops + witnessed
// merges). balls must be the FINAL board state, in upload order.
func (p *Player) RequestComposeBallCompose(balls []*proto.ComposeBallBallData, composes []*proto.ComposeBallComposeData, timeout time.Duration) (*proto.ComposeBallComposeResp, error) {
	id := p.ComposeBallActivityID()
	if id == 0 {
		return nil, fmt.Errorf("đại hội luyện khí chưa mở")
	}
	body, _ := pb.Marshal(&proto.ComposeBallComposeReq{
		ActivityId:  &id,
		Balls:       balls,
		ComposeData: composes,
		BallsCheck:  pb.String(cbComputeCheck(balls)),
	})
	raw, err := p.cbRoundTrip(COMPOSE_BALL_COMPOSE_CMD, SERVER_COMPOSE_BALL_COMPOSE, body, timeout)
	if err != nil {
		return nil, err
	}
	var resp proto.ComposeBallComposeResp
	if err := pb.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (p *Player) RequestComposeBallSettle(timeout time.Duration) (*proto.ComposeBallSettleResp, error) {
	id := p.ComposeBallActivityID()
	if id == 0 {
		return nil, fmt.Errorf("đại hội luyện khí chưa mở")
	}
	body, _ := pb.Marshal(&proto.ComposeBallSettleReq{ActivityId: &id})
	raw, err := p.cbRoundTrip(COMPOSE_BALL_SETTLE_CMD, SERVER_COMPOSE_BALL_SETTLE, body, timeout)
	if err != nil {
		return nil, err
	}
	var resp proto.ComposeBallSettleResp
	if err := pb.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (p *Player) RequestComposeBallHpItem(num int32, timeout time.Duration) (*proto.ComposeBallHpItemResp, error) {
	id := p.ComposeBallActivityID()
	if id == 0 {
		return nil, fmt.Errorf("đại hội luyện khí chưa mở")
	}
	body, _ := pb.Marshal(&proto.ComposeBallHpItemReq{ActivityId: &id, Num: &num})
	raw, err := p.cbRoundTrip(COMPOSE_BALL_HP_ITEM_CMD, SERVER_COMPOSE_BALL_HP_ITEM, body, timeout)
	if err != nil {
		return nil, err
	}
	var resp proto.ComposeBallHpItemResp
	if err := pb.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (p *Player) RequestComposeBallUseItem(itemType, idA, idB int32, timeout time.Duration) (*proto.ComposeBallUseItemResp, error) {
	id := p.ComposeBallActivityID()
	if id == 0 {
		return nil, fmt.Errorf("đại hội luyện khí chưa mở")
	}
	p.CbMu.Lock()
	var balls []*proto.ComposeBallBallData
	var composes []*proto.ComposeBallComposeData
	if p.cb != nil {
		for _, b := range p.cb.Balls { // itemType>1 may want targets: reuse current ledger
			balls = append(balls, cbBallData(b))
		}
	}
	p.CbMu.Unlock()
	req := &proto.ComposeBallUseItemReq{
		ActivityId: &id, ItemType: &itemType,
		Balls: balls, BallsCheck: pb.String(cbComputeCheck(balls)),
	}
	if idA != 0 {
		req.IdA = &idA
	}
	if idB != 0 {
		req.IdB = &idB
	}
	if itemType == 4 { // merge-all: the server wants the whole chain too
		req.ComposeData = composes
	}
	body, _ := pb.Marshal(req)
	raw, err := p.cbRoundTrip(COMPOSE_BALL_USE_ITEM_CMD, SERVER_COMPOSE_BALL_USE_ITEM, body, timeout)
	if err != nil {
		return nil, err
	}
	var resp proto.ComposeBallUseItemResp
	if err := pb.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ---- ballsCheck -------------------------------------------------------------

// cbCheckString is the client formula (RemoteComposeBallGameView.reqCompose,
// verbatim): iterate the SUBMITTED balls list in order, concatenate every
// position string, md5(SALT + concat). The client calls getCurFruits() twice
// (balls + check) so the order is the fruitList insertion order — board
// order, NOT id-sorted. The upload must keep one stable order for both.
func cbCheckString(balls []*proto.ComposeBallBallData) string {
	var o []byte
	for _, b := range balls {
		for _, pos := range b.GetPosition() {
			o = append(o, pos...)
		}
	}
	return string(o)
}

func cbComputeCheck(balls []*proto.ComposeBallBallData) string {
	sum := md5.Sum([]byte(cbCheckSalt + cbCheckString(balls)))
	return hex.EncodeToString(sum[:])
}

func cbFix5(v float64) string { return strconv.FormatFloat(v, 'f', 5, 64) }

func cbBallData(b cbBall) *proto.ComposeBallBallData {
	return &proto.ComposeBallBallData{
		Id:       &b.ID,
		ConfigId: &b.Config,
		Position: []string{cbFix5(b.X), cbFix5(b.Y)},
	}
}

// cbSnapshot is the processBall serialization the client's getInfo/getInComData
// produce at a contact instant: every live ball with position + zero velocity
// + scale 1.0 (proven accepted on 710564).
func cbSnapshot(world []cbBall) []*proto.ComposeBallBallData {
	proc := make([]*proto.ComposeBallBallData, 0, len(world))
	for _, b := range world {
		pd := cbBallData(b)
		pd.LinearVelocity = []string{cbFix5(0), cbFix5(0)}
		pd.Scale = pb.String(cbFix5(1))
		proc = append(proc, pd)
	}
	return proc
}

// ---- the proven drop+merge upload format ------------------------------------
//
// Verified live 2026-10-04 (server 710564, honest merge ret=0) and matching
// RemoteComposeBallGameView.reqCompose / RemoteComposeBallSceneItem.
// checkContact from the real client:
//
//   • ONE fresh drop per upload: id = syncData.id (the server's reserved
//     counter), configId = syncData.configId. Mismatch → 13002/13017.
//   • Every same-config contact (the drop's, then each merged ball's — the
//     cascade is RECURSIVE) rides along as one ComposeData entry:
//     {ballA = older ball id, ballB = newer id, ball = {merged id =
//     syncData.id+1 / +2 / …, config = cfg+1, position = the lower parent's
//     position}, processBall = snapshot of EVERY live ball at THAT merge's
//     contact instant INCLUDING the ball about to vanish}.
//   • `balls` = the final board: drop and all consumed parents absent, merged
//     balls at their settled positions. Position strings only (toFixed(5) of
//     raw coords).
//   • ballsCheck = md5(SALT + concat of every position string of `balls`,
//     submitted order).
//   • Drop positions themselves are NOT physics-checked (floating boards are
//     echoed verbatim) — the frame only has to be internally consistent.

// cbLineY is the game-over threshold line y (RemoteComposeBallGameView: the
// line sits container.height/3 below the container top at y≈0, and
// ground = top − height).
// cbLineLimit is the highest ball TOP the packer may use.
// User directive 2026-10-04: "just disable the threadhold gate" — the planner
// no longer refuses placements that cross the game-over line; it packs the
// board as dense as geometry allows and lets the SERVER decide: if a rest
// really breaches the line the server flips GameOver, which cbRunOnce closes
// with SETTLE + notify (items are still never used).
func cbLineLimit(lineY float64) float64 { return math.Inf(1) }

func cbLineY(ground float64) float64 {
	return ground + 2*(-ground)/3
}

// cbPlanLocked returns ONE upload built on the FREE-MERGE model — the
// server does NOT track position continuity: proven live 2026-10-04
// (TestLiveComposeBallFreeMerge): two echoed balls 632px apart were MOVED
// into contact, their merge claimed (ret=0, score paid), with the fresh drop
// unrelated to the claim. So every upload REPACKS the whole board:
//
//	claim a merge for EVERY same-config pair until each config is a singleton
//	(highest level first — chains fall out naturally), the fresh drop riding
//	along in every processBall snapshot, then lay the survivors out
//	bottom-up/left-right in packed rows (RemoteComposeBallGameView
//	lineHeight rule: no ball top may reach the game-over line).
//
// Merged ids consume syncData.id+1, +2, … one per claim. This is the "never
// die" engine (user 2026-10-04): merges FREE floor width every round
// (2·2r in, ~2·1.35r out), so the board stays playable until hp=0; the
// planner only refuses when even perfect repacking cannot fit the survivors
// under the line — which packing singletons 101..108 shows is essentially
// unreachable (3 rows all clear −326 on the 979-high board).
func cbPlan(st *composeBallState) ([]*proto.ComposeBallBallData, []*proto.ComposeBallComposeData, bool) {
	if st == nil || !st.Have || st.GameOver {
		return nil, nil, false
	}
	cfg := st.NextConfig
	if cfg == 0 || st.LevelOf[cfg] == 0 {
		return nil, nil, false // no config table yet, or no next fruit
	}
	ground := st.Ground
	if ground == 0 {
		ground = cbGroundFallback // fresh round: pick our own consistent frame
	}
	lineY := cbLineY(ground)

	// 1. Merge claims + staging, REPAIRED incrementally: the claim timeline
	//    (recursive: chains merge each fresh ball straight up) is rebuilt
	//    without any pair whose snapshot cannot be staged under the line —
	//    one cramped pair must never sink the whole upload (only hp=0 or an
	//    unpicklable board ends the run).
	base := append([]cbBall(nil), st.Balls...)
	base = append(base, cbBall{ID: st.NextID, Config: cfg})
	type cbClaim struct{ a, b, merged, upCfg int32 }
	banned := map[[2]int32]bool{}
	for attempt := 0; attempt <= cbMaxMerges; attempt++ {
		// greedy claim build, skipping banned pairs
		pool := append([]cbBall(nil), base...)
		var claims []cbClaim
		nextMerge := st.NextID + 1
		for it := 0; it < cbMaxMerges; it++ {
			counts := map[int32][]cbBall{}
			for _, b := range pool {
				counts[b.Config] = append(counts[b.Config], b)
			}
			bestCfg, bestLv := int32(0), int32(0)
			for c, bs := range counts {
				lv := st.LevelOf[c]
				if len(bs) < 2 || lv <= bestLv || st.ConfigOf[lv+1] == 0 {
					continue
				}
				for i := 0; i < len(bs); i++ {
					for j := i + 1; j < len(bs); j++ {
						x, y := bs[i], bs[j]
						if x.ID > y.ID {
							x, y = y, x
						}
						if !banned[[2]int32{x.ID, y.ID}] {
							bestCfg, bestLv = c, lv
						}
					}
				}
			}
			if bestCfg == 0 {
				break // every config is a singleton (or a banned/top-tier pair)
			}
			bs := counts[bestCfg]
			var a, b cbBall
			found := false
			for i := 0; i < len(bs) && !found; i++ {
				for j := i + 1; j < len(bs) && !found; j++ {
					x, y := bs[i], bs[j]
					if x.ID > y.ID {
						x, y = y, x
					}
					if !banned[[2]int32{x.ID, y.ID}] {
						a, b, found = x, y, true
					}
				}
			}
			claims = append(claims, cbClaim{a: a.ID, b: b.ID, merged: nextMerge, upCfg: st.ConfigOf[bestLv+1]})
			nextMerge++
			live := make([]cbBall, 0, len(pool))
			for _, q := range pool {
				if q.ID != a.ID && q.ID != b.ID {
					live = append(live, q)
				}
			}
			pool = append(live, cbBall{ID: claims[len(claims)-1].merged, Config: claims[len(claims)-1].upCfg})
		}

		// 2. Snapshots: replay the claim timeline; each snapshot packs the
		//    live set with the claiming pair FORCED into exact contact.
		live := append([]cbBall(nil), base...)
		var composes []*proto.ComposeBallComposeData
		failIdx := -1
		for k, cl := range claims {
			placed, ok := cbPack(st, live, [2]int32{cl.a, cl.b}, ground, lineY)
			if !ok {
				failIdx = k
				break
			}
			byID := map[int32]cbBall{}
			for _, q := range placed {
				byID[q.ID] = q
			}
			aB, bB := byID[cl.a], byID[cl.b]
			cpos := aB
			if bB.Y < aB.Y || (bB.Y == aB.Y && bB.ID < aB.ID) {
				cpos = bB // lower parent; equal y (one row) → the older ball
			}
			composes = append(composes, &proto.ComposeBallComposeData{
				BallA:       pb.Int32(aB.ID),
				BallB:       pb.Int32(bB.ID),
				Ball:        cbBallData(cbBall{ID: cl.merged, Config: cl.upCfg, X: cpos.X, Y: cpos.Y}),
				ProcessBall: cbSnapshot(placed),
			})
			nxt := make([]cbBall, 0, len(live))
			for _, q := range live {
				if q.ID != cl.a && q.ID != cl.b {
					nxt = append(nxt, q)
				}
			}
			live = append(nxt, cbBall{ID: cl.merged, Config: cl.upCfg})
		}
		if failIdx >= 0 {
			// This pair cannot be staged alongside the rest — drop it from
			// the plan and retry the cascade without it.
			banned[[2]int32{claims[failIdx].a, claims[failIdx].b}] = true
			continue
		}

		// 3. Final board: the survivors packed bottom-up, left-to-right.
		final, ok := cbPack(st, live, [2]int32{}, ground, lineY)
		if ok {
			balls := make([]*proto.ComposeBallBallData, 0, len(final))
			for _, b := range final {
				balls = append(balls, cbBallData(b))
			}
			return balls, composes, true
		}
		if len(claims) == 0 {
			return nil, nil, false // even the plain drop cannot be placed under the line
		}
		// too many merges left the survivors pack impossible: shed the last
		// claim and try a less aggressive merge plan.
		last := claims[len(claims)-1]
		banned[[2]int32{last.a, last.b}] = true
	}
	return nil, nil, false
}

// cbPlanLocked = cbPlan over the LIVE ledger; the caller must hold CbMu
// (tests + any caller already sitting on the lock). Production runs use
// cbPlanUnlocked.
func (p *Player) cbPlanLocked() ([]*proto.ComposeBallBallData, []*proto.ComposeBallComposeData, bool) {
	return cbPlan(p.cb)
}

// cbPlanUnlocked snapshots the planner's inputs under CbMu and plans
// WITHOUT holding it, so the ws reader goroutine (cbDeliver/cbApplySync both
// need CbMu) never parks behind a heavy repack. Map fields are swapped
// wholesale by cbMergeConfig under the lock (never mutated in place, so the
// snapshot may read the old maps); Balls is COPIED because cbApplySync
// rewrites its backing array in place.
func (p *Player) cbPlanUnlocked() ([]*proto.ComposeBallBallData, []*proto.ComposeBallComposeData, bool) {
	p.CbMu.Lock()
	if p.cb == nil {
		p.CbMu.Unlock()
		return nil, nil, false
	}
	snap := *p.cb
	snap.Balls = append([]cbBall(nil), p.cb.Balls...)
	p.CbMu.Unlock()
	return cbPlan(&snap)
}

// cbPack builds a legal snapshot/final board from live, PRESERVING the
// server's echoed poses wherever they are already valid — the echo IS the
// densest packing (rebuilding from scratch is what made the 710547 dense
// boards unsolvable, while the user screenshot proved room remained):
// every echoed ball whose pose sits in the walls, under the game-over line,
// resting on the surface under it, and clear of its neighbours stays EXACTLY
// where the server put it. Only balls without a usable echo — the fresh
// drop, merged products, and any legacy hoverer — are re-dropped largest-
// first to the LOWEST tangent rest pose (nothing hovers, nothing overlaps:
// rest y is the max tangent surface over every other ball). The merge
// claim's pinned pair is staged as ONE CONTACT UNIT on top of that base:
// anchor rests lowest-first, partner rests at exact tangency beside it.
// Positions we fabricate are validated; the server only checks internal
// frame consistency (proven 2026-10-04). Returns false only when even this
// cannot stay inside the walls and under the line.
func cbPack(st *composeBallState, live []cbBall, first [2]int32, ground, lineY float64) ([]cbBall, bool) {
	// Heaviest-first lets big blockers claim the low nests before the small
	// balls fill them; small-first saves the nests for the tiny balls that
	// alone can thread between crowns. Dense boards need different orders at
	// different densities (710547 regressions), so try both.
	// Heaviest-first lets big blockers claim the low nests before the small
	// balls fill them; small-first saves the nests for the tiny balls that
	// alone can thread between crowns. Dense boards need different orders at
	// different densities (710547 regressions), so try both.
	for _, asc := range [2]bool{false, true} {
		if out, ok := cbPackOrder(st, live, first, ground, lineY, asc, false); ok {
			return out, true
		}
	}
	// FULL REPACK fallback: relocation is PROVEN free live (free-merge + the
	// grid-repack era drove boards to 20880), and "many free space but still
	// stop" regressions (user, 1000018888.png: two 105s unmergeable only
	// because the six echoes around them were frozen) came from the keep
	// policy, not geometry. If keeping echoes can't stage the board, rebuild
	// it from scratch — every ball re-dropped under the line.
	for _, asc := range [2]bool{false, true} {
		if out, ok := cbPackOrder(st, live, first, ground, lineY, asc, true); ok {
			return out, true
		}
	}
	return nil, false
}

func cbPackOrder(st *composeBallState, live []cbBall, first [2]int32, ground, lineY float64, pendAsc, repack bool) ([]cbBall, bool) {
	rOf := func(c int32) float64 { return cbRadius(st, cbBall{Config: c}) }
	limit := cbLineLimit(lineY)
	edge := cbRightWall
	if st.Right > edge {
		edge = st.Right
	}
	isPinned := func(id int32) bool { return id != 0 && (id == first[0] || id == first[1]) }

	var cands, pend []cbBall
	for _, b := range live {
		if isPinned(b.ID) {
			continue
		}
		if (b.X == 0 && b.Y == 0) || repack {
			pend = append(pend, b) // fresh drop / merged: no echo yet
			continue
		}
		cands = append(cands, b)
	}
	sort.SliceStable(cands, func(i, j int) bool { return rOf(cands[i].Config) > rOf(cands[j].Config) })

	atRest := func(b cbBall, others []cbBall) bool {
		r := rOf(b.Config)
		if b.Y+r >= limit {
			return false
		}
		supported := math.Abs(b.Y-(ground+r)) < 1.0 // on the floor
		atWall := b.X <= cbWallPad+r+0.5 || b.X >= edge-cbWallPad-r-0.5
		lower := 0
		for _, o := range others {
			s := r + cbRadius(st, o)
			d := math.Hypot(b.X-o.X, b.Y-o.Y)
			if d < s-1.0 {
				return false // genuine interpenetration
			}
			if math.Abs(d-s) < 1.0 && o.Y < b.Y-0.02 {
				lower++ // tangent contact with a ball under it
			}
		}
		// The server does NOT simulate our submitted poses (free-merge is
		// proven live) — its physics has friction and echoes come back resting
		// on single flank contacts (710547 dense boards: 848/850 sit on one
		// flank). A pose is KEPT when it is inside the walls, clears the line,
		// penetrates nothing and has real support: floor, any tangent ball
		// below, or wall. Re-dropping a friction-wedged ball instead DESTROYS
		// a layout the server already accepted — the dense-board regression
		// came from exactly this eviction storm.
		supported = supported || lower >= 1 || atWall
		// Echoed positions are toFixed(5) rounded — tangencies come back
		// tenths of a px apart (710547 case3: 441/850 d=163.9 vs 164.0); the
		// ±1.0 px windows above keep those poses instead of needlessly
		// re-dropping them. A ball touching nothing (hovering garbage) still
		// falls out and gets re-settled.
		return supported
	}
	kept := make([]cbBall, 0, len(live))
	for _, b := range cands {
		if atRest(b, kept) {
			kept = append(kept, b)
		} else {
			pend = append(pend, b) // legacy hoverer/overlap: must re-settle
		}
	}
	// Heaviest-first lets big blockers claim the low nests first; small-first
	// saves those nests for the tiny balls that alone can thread between
	// crowns — both orders are tried (dense 710547 boards need different
	// orders at different densities).
	sort.SliceStable(pend, func(i, j int) bool {
		if pendAsc {
			return rOf(pend[i].Config) < rOf(pend[j].Config)
		}
		return rOf(pend[i].Config) > rOf(pend[j].Config)
	})
	// lowestRest drops cfg from every candidate x and keeps the LOWEST final
	// pose after the frictionless slide — a ball that lands on a flank ROLLS
	// into the nearest valley/gap/wall instead of being stuck where a purely
	// vertical scan would park it (the screenshot board's "free space" only
	// becomes reachable once balls roll).
	lowestRest := func(cfg int32, pile []cbBall) (cbBall, bool) {
		r := rOf(cfg)
		best, okAny := cbBall{}, false
		for x := cbWallPad + r; x <= edge-cbWallPad-r; x += 12 {
			q, ok := cbDropAt(st, pile, cfg, x, ground, lineY, edge)
			if ok && (!okAny || q.Y < best.Y) {
				best, okAny = q, true
			}
		}
		return best, okAny
	}
	// stagePair puts the pinned claim pair on `base` as one contact unit:
	// anchor re-dropped lowest-first, partner CONSTRUCTED at exact tangency
	// on the anchor's contact circle (angles all around, φ from vertical).
	// The partner is never re-dropped: the live server holds balls on flank
	// wedges (its physics has friction — echoed boards prove it) and it does
	// not simulate submitted poses, so a lean-on contact is a legal static
	// frame, while frictionless re-dropping would roll the partner off the
	// anchor and break every dense-board claim. Walls, floor, line, overlap.
	pairOn := func(base []cbBall) (cbBall, cbBall, bool) {
		byID := map[int32]cbBall{}
		for _, b := range live {
			byID[b.ID] = b
		}
		a, b := byID[first[0]], byID[first[1]]
		ra, rb := rOf(a.Config), rOf(b.Config)
		s := ra + rb
		bestTop := math.Inf(1)
		var bA, bB cbBall
		ok := false
		for ax := cbWallPad + ra; ax <= edge-cbWallPad-ra; ax += 12 {
			qa, okA := cbDropAt(st, base, a.Config, ax, ground, lineY, edge)
			if !okA {
				continue
			}
			withA := append(append([]cbBall(nil), base...), cbBall{ID: a.ID, Config: a.Config, X: qa.X, Y: qa.Y})
			for _, side := range [2]float64{-1, 1} {
				for _, phi := range []float64{0, 20, 40, 60, 80, 100, 120, 140, 160, 180} {
					pxb := qa.X + side*s*math.Sin(phi*math.Pi/180)
					pyb := qa.Y + s*math.Cos(phi*math.Pi/180)
					if pxb < cbWallPad+rb || pxb > edge-cbWallPad-rb {
						continue
					}
					if pyb+rb >= limit || pyb < ground+rb-0.01 {
						continue
					}
					clash := false
					for _, o := range withA {
						so := rb + cbRadius(st, o)
						if math.Hypot(pxb-o.X, pyb-o.Y) < so-0.01 {
							clash = true
							break
						}
					}
					if clash {
						continue
					}
					if top := math.Max(qa.Y+ra, pyb+rb); top < bestTop {
						bestTop = top
						bA = cbBall{ID: a.ID, Config: a.Config, X: qa.X, Y: qa.Y}
						bB = cbBall{ID: b.ID, Config: b.Config, X: pxb, Y: pyb}
						ok = true
					}
				}
			}
		}
		return bA, bB, ok
	}
	isClaim := first[0] != 0 && first[1] != 0
	if repack && isClaim {
		// In repack mode the CLAIM pair is staged FIRST — it owns the lowest
		// real estate (the floor), and the rest of the board is rebuilt around
		// it. Staging it last was why 1000018888.png "had no room" for the two
		// 105s: the re-dropped balls filled every nest before the pair looked.
		bA, bB, ok := pairOn(kept)
		if !ok {
			return nil, false
		}
		kept = append(kept, bA, bB)
	}
	for _, b := range pend {
		q, ok := lowestRest(b.Config, kept)
		if !ok {
			return nil, false // the pile genuinely cannot carry this ball
		}
		q.ID = b.ID
		kept = append(kept, q)
	}
	if isClaim && !repack {
		bA, bB, ok := pairOn(kept)
		if !ok {
			return nil, false // no contact staging for this pair under the line
		}
		kept = append(kept, bA, bB)
	}
	return kept, true
}

// cbDropAt simulates a FRICTIONLESS drop of cfg at x: the ball falls onto
// the highest surface below it; if it lands balanced on a single ball (no
// friction to hold it) it slides downhill in 2px steps until it rests on the
// ground (rolling the last pixels into contact with its nearest neighbour),
// wedges against a wall, or nests in a two-ball valley. Slide ping-pong
// between two crowns (no valley to drain into) falls back on the lowest
// supported pose seen — a legitimate tangency. Refuses poses whose top
// reaches the game-over line.
func cbDropAt(st *composeBallState, stack []cbBall, cfg int32, x, ground, lineY, edge float64) (cbBall, bool) {
	r := cbRadius(st, cbBall{Config: cfg})
	limit := cbLineLimit(lineY)
	lo, hi := cbWallPad+r, edge-cbWallPad-r
	if lo > hi {
		return cbBall{}, false
	}
	clamp := func(v float64) float64 {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	cur := clamp(x)
	var best cbBall
	bestOK := false
	reversals := 0
	var dir float64
	for i := 0; i < 240; i++ {
		y := cbSurfY(st, stack, cfg, cur, ground)
		if y <= ground+r+0.01 {
			// Reached the floor: roll the last pixels into contact with the
			// nearest neighbour (the 2px slide overshoots exact tangency).
			if y+r >= limit {
				return cbBall{}, false // a floor-resting ball already breaches the line
			}
			for _, f := range stack {
				s := r + cbRadius(st, f)
				dx := cur - f.X
				if ad := math.Abs(dx); ad < s+8 && ad >= s-0.05 {
					tentative := clamp(f.X + math.Copysign(s, dx))
					if cbSurfY(st, stack, cfg, tentative, ground) <= ground+r+0.01 {
						return cbBall{Config: cfg, X: tentative, Y: ground + r}, true
					}
				}
			}
			return cbBall{Config: cfg, X: cur, Y: y}, true
		}
		nsup := 0
		for _, f := range stack {
			s := r + cbRadius(st, f)
			if math.Abs(math.Hypot(cur-f.X, y-f.Y)-s) < 0.05 {
				nsup++
			}
		}
		if y+r < limit {
			switch {
			case nsup >= 2:
				return cbBall{Config: cfg, X: cur, Y: y}, true // valley nest
			case nsup == 1:
				if !bestOK || y < best.Y {
					best, bestOK = cbBall{Config: cfg, X: cur, Y: y}, true
				}
				if cur <= lo+0.01 || cur >= hi-0.01 {
					return cbBall{Config: cfg, X: cur, Y: y}, true // wedged at wall
				}
			}
		}
		// Not a legal rest (or the pose breaches the line): slide 2px
		// downhill toward whichever neighbour is lower. A line-breaching
		// landing must be PASSED THROUGH — the valley below the line may be
		// around the next crown (dense 710547 boards need exactly this).
		nl, nr := clamp(cur-2), clamp(cur+2)
		yL, yR := cbSurfY(st, stack, cfg, nl, ground), cbSurfY(st, stack, cfg, nr, ground)
		var nd float64
		switch {
		case nl < cur && yL <= yR && yL < y:
			nd = -2
		case nr > cur && yR < yL && yR < y:
			nd = 2
		case yL < y:
			nd = -2
		case yR < y:
			nd = 2
		}
		if nd == 0 { // local minimum: accept only if legal
			if y+r < limit {
				if nsup >= 1 || bestOK {
					if bestOK && y > best.Y {
						return best, true
					}
					return cbBall{Config: cfg, X: cur, Y: y}, true
				}
			}
			if bestOK {
				return best, true
			}
			return cbBall{}, false
		}
		if dir != 0 && nd != dir {
			reversals++
			if reversals >= 3 && bestOK {
				return best, true // ping-pong: settle on the best contact seen
			}
		}
		dir = nd
		cur = clamp(cur + nd)
	}
	if bestOK {
		return best, true
	}
	return cbBall{}, false
}

// cbSurfY is the lowest legal centre height for cfg at x: the floor line or
// the highest tangent surface the vertical drop meets over the placed pile.
func cbSurfY(st *composeBallState, stack []cbBall, cfg int32, x, ground float64) float64 {
	r := cbRadius(st, cbBall{Config: cfg})
	y := ground + r
	for _, f := range stack {
		s := r + cbRadius(st, f)
		dx := x - f.X
		if dx >= s || dx <= -s {
			continue
		}
		if fy := f.Y + math.Sqrt(s*s-dx*dx); fy > y {
			y = fy
		}
	}
	return y
}

// cbRadius is the TRUE ball radius (px) from the config table: width is the
// ball diameter in the same units as board positions (proven on live 710564:
// a cfg-101 drop at ground+30 echoed back at exactly that y).
func cbRadius(st *composeBallState, b cbBall) float64 {
	if w := st.WidthOf[st.LevelOf[b.Config]]; w > 0 {
		return float64(w) / 2
	}
	return 25
}

// ---- authoritative sync application -------------------------------------------

func (p *Player) cbApplySync(sync *proto.ComposeBallSyncData) {
	if sync == nil {
		return
	}
	st := p.cbState()
	p.CbMu.Lock()
	defer p.CbMu.Unlock()
	if bs := sync.GetBalls(); bs != nil {
		st.Balls = st.Balls[:0]
		for _, b := range bs {
			e := cbBall{ID: b.GetId(), Config: b.GetConfigId()}
			if ps := b.GetPosition(); len(ps) >= 2 {
				e.X, _ = strconv.ParseFloat(ps[0], 64)
				e.Y, _ = strconv.ParseFloat(ps[1], 64)
			}
			st.Balls = append(st.Balls, e)
			if b.GetId() >= st.NextID {
				st.NextID = b.GetId() + 1
			}
			// Floor-resting echo balls anchor the ground line: the LOWEST
			// (y − true radius) the server ever reported is the floor.
			if g := e.Y - cbRadius(st, e); st.Ground == 0 || g < st.Ground {
				st.Ground = g
			}
			if rr := e.X + cbRadius(st, e); rr > st.Right {
				st.Right = rr
			}
		}
	}
	// An empty fresh board echoes NO balls list (nil, not empty) — the round
	// is still established the moment the server hands out counters. Live
	// probe 2026-10-04: treating that as "no board" settled a 0-score round
	// seconds after ENTER.
	if sync.Id != nil {
		st.Have = true
	}
	st.GameOver = sync.GetGameOver()
	if sync.Id != nil {
		st.NextID = sync.GetId() // the server's reserved drop id (exact)
	}
	if sync.Id != nil && len(st.Balls) > 0 {
		mx := int32(0)
		for _, b := range st.Balls {
			if b.ID > mx {
				mx = b.ID
			}
		}
		st.NextID = max(st.NextID, mx+1) // never issue an id the server already used
	}
	if sync.ConfigId != nil {
		st.NextConfig = sync.GetConfigId()
	}
	if u := sync.GetSyncData(); u != nil {
		st.HP, st.CostHP, st.HPTime = u.GetHp(), u.GetCostHp(), u.GetHpTime()
		st.Score, st.MaxScore = u.GetScore(), u.GetMaxScore()
	}
}

// ---- the one-shot run -------------------------------------------------------

// cbRunOnce plays exactly ONE ComposeBall round to its stopping point:
// ENTER once (loads the SAME board — never a restart), then drop+cascade
// uploads continuously. Linh Khí regen is ~1 per 2s and hp=0 drops are
// accepted, so out-of-HP is a WAIT, not a stop (user 2026-10-04: "must not
// restart game, only continue play"). The run ends only on: a failure return
// code (NOTIFY then STOP, no settle — user rule), GameOver (settle+notify),
// no legal placement under the line (leave board + notify, resumable), the
// day lock, or the safety upload cap. Items are NEVER used.
func (p *Player) cbRunOnce() {
	id := p.ComposeBallActivityID()
	if id == 0 {
		p.forceNotify("Đại Hội Luyện Khí chưa mở (khung 10:00–22:00), đạo hữu thử lại sau nhé.")
		return
	}
	todayICT := time.Now().In(ictZone).Format("2006-01-02")
	st := p.cbState()
	p.CbMu.Lock()
	locked := st.LockDay == todayICT
	p.CbMu.Unlock()
	if locked {
		p.forceNotify("Luyện Khí: server đã khóa bảng của đạo hữu hôm nay, mai chơi tiếp nhé.")
		return
	}
	p.cbRequestConfig(id)
	// Owner 2026-10-25: "server chưa trả info event thì mày đã chạy" — wait
	// for the 21003 tier table to ACTUALLY land (the reader folds it into
	// CfgDone) before ENTER; without it the planner is blind. Capped, so a
	// dead server cannot park the run forever.
	cfgDeadline := time.Now().Add(6 * time.Second)
	for {
		p.CbMu.Lock()
		done := st.CfgDone
		p.CbMu.Unlock()
		if done || time.Now().After(cfgDeadline) || !p.cbPause(250*time.Millisecond) {
			break
		}
	}
	if !p.cbPause(cbBeat) {
		return
	}

	// ENTER — with the owner's manual cure automated: "nhận 0 hp, vào ra vào
	// lại là được" → an ENTER whose answer carries no Linh Khí fold (user
	// sync missing or hp=0) is retried cbEnterTries times, one beat apart.
	// ENTER loads the SAME board (never a restart — user rule 2026-10-04)
	// and spends nothing, so the extra attempts are harmless even when the
	// zero is genuine; the run then stops on hp=0 exactly as the law says.
	for attempt := 1; attempt <= cbEnterTries; attempt++ {
		resp, err := p.RequestComposeBallEnterGame(cbRTTimeout)
		if err != nil {
			log.Printf("[%s] [LuyenKhi] enter: %v", p.Username, err)
			p.forceNotify("Luyện Khí: không vào được đại hội (" + err.Error() + ")")
			return
		}
		if resp.GetRet() != 0 {
			log.Printf("[%s] [LuyenKhi] enter ret=%d", p.Username, resp.GetRet())
			p.forceNotify(fmt.Sprintf("Luyện Khí: vào bàn bị lỗi (ret=%d)", resp.GetRet()))
			return
		}
		p.CbMu.Lock()
		st.ActivityID = id
		p.CbMu.Unlock()
		p.cbApplySync(resp.GetSyncData())
		us := resp.GetSyncData().GetSyncData()
		p.CbMu.Lock()
		hp := st.HP
		p.CbMu.Unlock()
		if us != nil && hp >= 1 {
			break // server really handed us the round + Linh Khí
		}
		log.Printf("[%s] [LuyenKhi] ENTER lần %d/%d chưa có Linh Khí (userSync=%v hp=%d) — vào lại sau 1s",
			p.Username, attempt, cbEnterTries, us != nil, hp)
		if attempt == cbEnterTries {
			break // last try spent — fall through, the hp=0 law decides below
		}
		if !p.cbPause(cbBeat) {
			return
		}
	}

	for n := 0; n < cbMaxUploadsTotal; n++ {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		p.CbMu.Lock()
		over, noHP := st.cbRunOut()
		hp, cost, score := st.HP, st.CostHP, st.Score
		p.CbMu.Unlock()
		if over {
			p.cbSettleAndNotify(id)
			return
		}
		if noHP {
			// User rule 2026-10-04 (final): hp=0 → STOP right away. No
			// waiting, no restart, no settle — the board and its points stay
			// exactly as the server has them; play again later with
			// /start_thanlong once Linh Khí has regenerated.
			log.Printf("[%s] [LuyenKhi] hp=%d (costHp=%d) — dừng ngay, giữ nguyên ván", p.Username, hp, cost)
			p.forceNotify(fmt.Sprintf("Luyện Khí: hết Linh Khí (hp=0) — dừng ở %d điểm, ván giữ nguyên. Khi Linh Khí hồi thì /start_thanlong chơi tiếp.", score))
			return
		}
		// Planned OUTSIDE CbMu (snapshot): the repack can grind for a while
		// on dense boards, and holding the lock would park the ws READER
		// (cbDeliver/cbApplySync need it) — a stalled reader is how the
		// server drops the socket ("bật thần long thỉnh thoảng rớt kết nối",
		// owner 2026-10-25).
		balls, composes, ok := p.cbPlanUnlocked()
		if !ok {
			if st.NextConfig == 0 {
				// The echo never named a next fruit — the server says there
				// is no live round for us; do NOT settle a board we can see.
				log.Printf("[%s] [LuyenKhi] plan=false, NextConfig=0 — không nộp điểm", p.Username)
				p.forceNotify("Luyện Khí: server không giao bi kế tiếp, để ván nguyên đó nhé.")
				return
			}
			// No legal placement under the line. NEVER settle here (user,
			// 2026-10-04): only hp=0 ends a run — leave the board intact,
			// Linh Khí regenerates, /start_thanlong continues the SAME round.
			var dump []cbBall
			p.CbMu.Lock()
			dump = append(dump, st.Balls...)
			nextCfg, gnd, ln := st.NextConfig, st.Ground, cbLineY(st.Ground)
			if gnd == 0 {
				ln = cbLineY(cbGroundFallback)
			}
			p.CbMu.Unlock()
			log.Printf("[%s] [LuyenKhi] hết chỗ hợp lệ dưới vạch (nextCfg=%d line=%.1f balls=%v) — để ván, KHÔNG nộp",
				p.Username, nextCfg, ln, dump)
			p.forceNotify(fmt.Sprintf("Luyện Khí: bàn tạm kẹt (bi tới %d không còn chỗ dưới vạch) — để nguyên ván, chờ hồi Linh Khí rồi /start_thanlong tiếp (điểm %d).", nextCfg, score))
			return
		}
		cresp, err := p.RequestComposeBallCompose(balls, composes, cbRTTimeout)
		if err != nil {
			log.Printf("[%s] [LuyenKhi] compose: %v", p.Username, err)
			return
		}
		if ret := cresp.GetRet(); ret != 0 {
			if ret == 13017 || ret == 13002 {
				p.CbMu.Lock()
				st.LockDay = todayICT
				p.CbMu.Unlock()
				log.Printf("[%s] [LuyenKhi] server khóa bảng hôm nay (ret=%d)", p.Username, ret)
				p.forceNotify("Luyện Khí: server từ chối bảng hôm nay (đã nộp hoặc khóa ngày), để bàn đó cho tới mai.")
				return
			}
			// User rule 2026-10-04: any failure return code ⇒ NOTIFY, then
			// STOP — no retry loop, no settle, the board stays as the server
			// last echoed it (sync adopted first so a later run continues
			// from the authoritative state).
			p.cbApplySync(cresp.GetSyncData())
			p.CbMu.Lock()
			hpX, scoreX := st.HP, st.Score
			p.CbMu.Unlock()
			log.Printf("[%s] [LuyenKhi] compose ret=%d — dừng theo luật (không nộp, không vào lại): hp=%d score=%d", p.Username, ret, hpX, scoreX)
			p.forceNotify(fmt.Sprintf("Luyện Khí: server trả về lỗi (ret=%d) — dừng an toàn ở %d điểm, ván vẫn giữ nguyên.", ret, scoreX))
			return
		}
		p.cbApplySync(cresp.GetSyncData())
		p.CbMu.Lock()
		log.Printf("[%s] [LuyenKhi] upload #%d +%d merge(s): hp=%d (costHp=%d) score=%d balls=%d",
			p.Username, n+1, len(composes), st.HP, st.CostHP, st.Score, len(st.Balls))
		p.CbMu.Unlock()
		// Owner 2026-10-25: 1s between uploads — no tight-loop hammering.
		if !p.cbPause(cbBeat) {
			return
		}
	}
	// Safety cap hit, not a game end: leave the round intact (settle would
	// only be correct on GameOver).
	p.forceNotify(fmt.Sprintf("Luyện Khí: chạm giới hạn an toàn %d lượt thả — để ván nguyên đó, điểm %d.", cbMaxUploadsTotal, st.Score))
}

// cbSettleAndNotify ends the round, reports score+rank, and sets the
// same-day compose lock (the server refuses every compose after a settle).
func (p *Player) cbSettleAndNotify(id int32) {
	resp, err := p.RequestComposeBallSettle(cbRTTimeout)
	if err != nil {
		log.Printf("[%s] [LuyenKhi] settle: %v", p.Username, err)
		p.forceNotify("Luyện Khí: nộp điểm lỗi (" + err.Error() + ")")
		return
	}
	if resp.GetRet() != 0 {
		log.Printf("[%s] [LuyenKhi] settle ret=%d", p.Username, resp.GetRet())
		p.forceNotify(fmt.Sprintf("Luyện Khí: nộp điểm bị lỗi (ret=%d)", resp.GetRet()))
		return
	}
	p.CbMu.Lock()
	p.cb.Have = false
	p.cb.LockDay = time.Now().In(ictZone).Format("2006-01-02") // compose locked until tomorrow
	p.cb.Score = resp.GetScore()
	if s := resp.GetSync(); s != nil {
		p.cb.MaxScore = s.GetMaxScore()
		p.cb.HP = s.GetHp()
	}
	score, maxScore := p.cb.Score, p.cb.MaxScore
	p.CbMu.Unlock()
	log.Printf("[%s] [LuyenKhi] settle: %d điểm (hạng %d, cao %d)", p.Username, score, resp.GetRank(), maxScore)
	p.forceNotify(fmt.Sprintf("Luyện Khí hoàn tất: %d điểm (cao nhất %d).", score, maxScore))
}
