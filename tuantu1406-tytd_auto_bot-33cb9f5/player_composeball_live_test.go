//go:build live

package tytd_go

// Live verification of the ComposeBall anti-cheat model (user, 2026-10-04):
// "the server cross-checks — if it drop only 2 level 1 ball, client cannot
// push to server a map contain one level 3 ball".
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveComposeBallAntiCheat -v -timeout 300s .
//
// FORMAT SOLVED 2026-10-04 (server 710564: honest drop+merge accepted
// ret=0), matching RemoteComposeBallGameView.reqCompose /
// RemoteComposeBallSceneItem.checkContact extracted from the real client:
//   - Every compose rides a fresh drop: id = syncData.id, config =
//     syncData.configId (the server pre-assigns both; mismatch → 13017).
//   - The drop merges on contact; the merged ball takes id = syncData.id+1
//     (client fruitId counter) and is claimed at the LOWER/SLOWER parent's
//     resting position in composeData.ball.
//   - processBall = getInfo() of EVERY live ball at the contact instant —
//     INCLUDING the drop about to vanish — {id, configId, position, scale,
//     linearVelocity} each.
//   - balls = the final board: parents and drop NOT present, merged present
//     at its settled rest height (ground + width/2); {id, configId,
//     position} toFixed(5) strings only.
//   - ballsCheck = md5(SALT + concat of every position string of `balls`,
//     submitted order).
//   - Other live rules: ball id < counter → 13002; overlap → 13003; no-drop
//     syncs / invalid placement → 13017; SETTLE locks compose with 13017 for
//     the rest of the day (710547) — so this test NEVER settles.
//
// The test replays THE IDENTICAL request twice more against the same live
// state, changing only the claim:
//   CHEAT: merged config = L+2 instead of L+1 (must be refused).
//   RAW  : identical fabricated board, NO composeData (the user's literal
//          scenario; must be refused by the drop-ledger cross-check).
//
// User rules: NO items, NO SETTLE.

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLiveComposeBallAntiCheat(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveUser   = liveUserEnv(t)
		livePass   = livePassEnv(t)
		liveServer = 710564
	)

	passportToken, err := request.LoginGPassport(liveUser, livePass)
	if err != nil {
		t.Fatalf("LoginGPassport: %v", err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatalf("LoginFuntap: %v", err)
	}
	gameURL, err := request.GetServerURL(userID, liveServer)
	if err != nil {
		t.Fatalf("GetServerURL: %v", err)
	}
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
	if err != nil {
		t.Fatalf("GetWSKey: %v", err)
	}

	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := gorm.Open(sqlite.Open("file:cb_anticheat_live?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.SystemConfig{}); err != nil {
		t.Fatal(err)
	}

	p := &Player{
		Username: liveUser, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan:  make(chan NotifyMGS, 64),
		writeChan: make(chan []byte, 64),
	}
	go func() {
		for frame := range p.writeChan {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, msgType, realData := pack.Unpack(message)
			if !isMSG {
				continue
			}
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.cbMergeConfig(realData)
			case SERVER_COMPOSE_BALL_ENTER, SERVER_COMPOSE_BALL_COMPOSE,
				SERVER_COMPOSE_BALL_USE_ITEM, SERVER_COMPOSE_BALL_HP_ITEM,
				SERVER_COMPOSE_BALL_SETTLE:
				p.cbDeliver(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(20 * time.Second)
	actID := int32(0)
	for actID == 0 && time.Now().Before(deadline) {
		actID = p.ComposeBallActivityID()
		if actID == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if actID == 0 {
		t.Skip("no ACTIVE type-130 window in the pushed activity list — re-run inside the window")
	}
	t.Logf("compose ball live activityId = %d", actID)

	p.cbRequestConfig(actID)
	cfgOK := false
	for try := 0; try < 20 && !cfgOK; try++ {
		func() {
			p.CbMu.Lock()
			defer p.CbMu.Unlock()
			cfgOK = p.cb != nil && p.cb.CfgDone
		}()
		if !cfgOK {
			time.Sleep(400 * time.Millisecond)
			p.cbRequestConfig(actID)
		}
	}
	if !cfgOK {
		t.Fatal("tier table never arrived")
	}

	enter, err := p.RequestComposeBallEnterGame(15 * time.Second)
	if err != nil {
		t.Fatalf("ENTER transport: %v", err)
	}
	if enter.GetRet() != 0 {
		t.Skipf("ENTER refused live (ret=%d) — outside the compose window or not eligible", enter.GetRet())
	}
	for tries := 0; tries < 5 && enter.GetSyncData().GetGameOver(); tries++ {
		enter, err = p.RequestComposeBallEnterGame(15 * time.Second)
		if err != nil {
			t.Fatalf("re-ENTER transport: %v", err)
		}
	}
	board := enter.GetSyncData().GetBalls()
	p.cbApplySync(enter.GetSyncData())
	hp, costHp := enter.GetSyncData().GetSyncData().GetHp(), enter.GetSyncData().GetSyncData().GetCostHp()
	t.Logf("ENTER ok: %d balls, nextCfg=%d nextId=%d hp=%d costHp=%d", len(board),
		enter.GetSyncData().GetConfigId(), enter.GetSyncData().GetId(), hp, costHp)
	for _, bb := range board {
		t.Logf("pre-existing ball: id=%d cfg=%d pos=%v", bb.GetId(), bb.GetConfigId(), bb.GetPosition())
	}
	for _, c := range enter.GetSyncData().GetCount() {
		t.Logf("server drop ledger: cfg=%d count=%d", c.GetConfigId(), c.GetCount())
	}

	p.CbMu.Lock()
	st := &composeBallState{}
	*st = *p.cb
	p.CbMu.Unlock()
	rOf := func(cfg int32) float64 { return float64(st.WidthOf[st.LevelOf[cfg]]) / 2 }
	if st.WidthOf[4] == 0 {
		t.Fatal("config missing tier-4 width — cannot anchor ground line")
	}
	ground := anchorYCfg104 - float64(st.WidthOf[4])/2
	if g := p.cb.Ground; g != 0 {
		ground = g // cbApplySync already computed the lowest observed rest
	}
	t.Logf("ground line y=%.5f", ground)

	// Fresh counters: the server ADVANCES configId/id per accepted response.
	curCfg := enter.GetSyncData().GetConfigId()
	if curCfg == 0 {
		curCfg = st.ConfigOf[1]
	}
	nextID := enter.GetSyncData().GetId()
	if nextID < 1 {
		nextID = 1
	}

	adopt := func(resp *proto.ComposeBallComposeResp) {
		sd := resp.GetSyncData()
		if bs := sd.GetBalls(); len(bs) > 0 {
			board = bs
		}
		if nid := sd.GetId(); nid > nextID {
			nextID = nid
		}
		if c := sd.GetConfigId(); c != 0 {
			curCfg = c
		}
		if u := sd.GetSyncData(); u != nil {
			hp, costHp = u.GetHp(), u.GetCostHp()
		}
	}
	upload := func(balls []*proto.ComposeBallBallData, composes []*proto.ComposeBallComposeData) (int32, bool) {
		t.Helper()
		resp, err := p.RequestComposeBallCompose(balls, composes, 15*time.Second)
		if err != nil {
			t.Fatalf("compose transport: %v", err)
		}
		if resp.GetRet() == 0 {
			adopt(resp)
			return 0, true
		}
		return resp.GetRet(), false
	}

	// free2D: a ball at (x,y,r) touches nothing (0.05 px slop).
	free2D := func(x, y, r float64) bool {
		for _, e := range board {
			ex, ey := cbXY(e)
			er := rOf(e.GetConfigId())
			dx, dy := x-ex, y-ey
			if dx*dx+dy*dy < (r+er-0.05)*(r+er-0.05) {
				return false
			}
		}
		return true
	}
	// dropSpots lists legal resting positions: floor scan, then resting ON
	// TOP of every ball (the pile grows; real clients stack).
	dropSpots := func(r float64) [][2]float64 {
		var out [][2]float64
		for x := 20.0; x <= 700; x += 20 {
			if x < r+6 || x > 714-r {
				continue
			}
			if free2D(x, ground+r, r) {
				out = append(out, [2]float64{x, ground + r})
			}
		}
		for _, e := range board {
			ex, ey := cbXY(e)
			er := rOf(e.GetConfigId())
			if free2D(ex, ey+er+r, r) {
				out = append(out, [2]float64{ex, ey + er + r})
			}
			// wedged on a neighbour at proper contact distance
			for _, o := range board {
				if o.GetId() == e.GetId() {
					continue
				}
				ox, oy := cbXY(o)
				or := rOf(o.GetConfigId())
				dd := r + or
				dy := oy - ey
				if math.Abs(dy) < dd {
					continue
				}
				dx2 := dd*dd - dy*dy
				if dx2 <= 0 {
					continue
				}
				dx := math.Sqrt(dx2)
				for _, s := range [][2]float64{{ox + dx, oy}, {ox - dx, oy}} {
					if s[0] > r+6 && s[0] < 714-r && free2D(s[0], s[1], r) {
						out = append(out, s)
					}
				}
			}
		}
		return out
	}
	payDrop := func() {
		t.Helper()
		if hp < costHp {
			t.Fatalf("hp %d < costHp %d and items are off-limits by rule — stop", hp, costHp)
		}
		r := rOf(curCfg)
		spots := dropSpots(r)
		// AIM the drop: when curCfg equals an existing FLOOR ball, land it
		// at exact contact beside that ball (creating the mergeable pair
		// directly) if the contact spot and the merged ball's floor slot
		// both fit. Otherwise any floor spot.
		for _, e := range board {
			if e.GetConfigId() != curCfg {
				continue
			}
			ex, ey := cbXY(e)
			if math.Abs(ey-(ground+r)) > 0.5 {
				continue
			}
			mfc := rOf(st.ConfigOf[st.LevelOf[curCfg]+1])
			for _, cand := range []float64{ex + 2*r, ex - 2*r} {
				if cand >= r+6 && cand <= 714-r && free2D(cand, ground+r, r) && free2D(ex, ground+mfc, mfc) {
					spots = append([][2]float64{{cand, ground + r}}, spots...)
					break
				}
			}
			break
		}
		// prefer floor spots (index where y == ground+r)
		for i := range spots {
			if math.Abs(spots[i][1]-(ground+r)) < 0.5 {
				spots[0], spots[i] = spots[i], spots[0]
				break
			}
		}
		if len(spots) == 0 {
			t.Fatalf("no resting spot for cfg=%d (board=%d balls, hp=%d, pile may be full)", curCfg, len(board), hp)
		}
		s := spots[0]
		d := &proto.ComposeBallBallData{
			Id: pb.Int32(nextID), ConfigId: pb.Int32(curCfg),
			Position:       []string{cbFix5(s[0]), cbFix5(s[1])},
			LinearVelocity: []string{"0.00000", "0.00000"},
			Scale:          pb.String("1.00000"),
		}
		ret, ok := upload(append(cbCopyBalls(board), d), nil)
		if ok {
			t.Logf("drop accepted: cfg=%d pos=%v (nextCfg=%d nextId=%d hp=%d)", curCfg, d.GetPosition(), curCfg, nextID, hp)
			return
		}
		t.Fatalf("drop refused cfg=%d pos=%v ret=%d", curCfg, d.GetPosition(), ret)
	}

	// oneShotMerge performs the ACCEPT-FORMAT request (proven ret=0):
	// drop the pre-assigned ball beside an existing same-config ball and
	// claim their merge into targetCfg. withClaim=false turns it into the
	// user's literal RAW PUSH (same fabricated board, no composeData).
	// Returns ret (−1 = not stageable, caller should payDrop).
	oneShotMerge := func(targetCfg int32, withClaim bool) (int32, bool) {
		t.Helper()
		r := rOf(curCfg)
		// FLOOR-ONLY staging: the merged ball's final rest position must be
		// free (pile physics after a stacked merge is unpredictable — the
		// accepted honest run used a floor pair). Look for a floor spot at
		// exact contact with a same-cfg floor ball whose merged-sized floor
		// slot is free too.
		var mate *proto.ComposeBallBallData
		var mxy [2]float64
		mr := rOf(targetCfg)
		for _, e := range board {
			if e.GetConfigId() != curCfg {
				continue
			}
			ex, ey := cbXY(e)
			if math.Abs(ey-(ground+r)) > 0.5 {
				continue // mate must itself be on the floor
			}
			for _, cand := range []float64{ex + 2*r, ex - 2*r} {
				if cand < r+6 || cand > 714-r || !free2D(cand, ground+r, r) {
					continue
				}
				if free2D(ex, ground+mr, mr) && math.Abs(ex-714+mr) < 0 {
					_ = ex
				}
				if !free2D(ex, ground+mr, mr) {
					continue
				}
				mate, mxy = e, [2]float64{cand, ground + r}
				break
			}
			if mate != nil {
				break
			}
		}
		if mate == nil {
			return -1, false
		}
		// processBall: every live ball at the contact instant, all-rest
		// (sleeping pile → velocities 0), plus the fresh drop ball.
		proc := make([]*proto.ComposeBallBallData, 0, len(board)+1)
		for _, e := range board {
			p0 := pb.Clone(e).(*proto.ComposeBallBallData) // AS ECHOED — a
			// stacked ball's real rest y is on top of its support, not the
			// floor; flattening it creates snapshot overlaps → 13003.
			p0.LinearVelocity = []string{"0.00000", "0.00000"}
			p0.Scale = pb.String("1.00000")
			proc = append(proc, p0)
		}
		d := &proto.ComposeBallBallData{
			Id: pb.Int32(nextID), ConfigId: pb.Int32(curCfg),
			Position:       []string{cbFix5(mxy[0]), cbFix5(mxy[1])},
			LinearVelocity: []string{"0.00000", "0.00000"},
			Scale:          pb.String("1.00000"),
		}
		proc = append(proc, d)
		// Merged ball: claimed at the SLOWER parent's resting position
		// (the at-rest mate — the drop is the faster contact ball).
		mpos, _ := cbXY(mate)
		m := &proto.ComposeBallBallData{
			Id: pb.Int32(nextID + 1), ConfigId: pb.Int32(targetCfg),
			Position: []string{cbFix5(mpos), cbFix5(ground + rOf(mate.GetConfigId()))},
		}
		// balls: final board at rest — mate gone (drop and its own id vanish
		// too), merged appended settled.
		nb := make([]*proto.ComposeBallBallData, 0, len(board))
		for _, e := range board {
			if e.GetId() == mate.GetId() {
				continue
			}
			nb = append(nb, pb.Clone(e).(*proto.ComposeBallBallData))
		}
		nb = append(nb, &proto.ComposeBallBallData{
			Id: pb.Int32(m.GetId()), ConfigId: pb.Int32(m.GetConfigId()),
			Position: []string{cbFix5(mpos), cbFix5(ground + mr)},
		})
		var composes []*proto.ComposeBallComposeData
		if withClaim {
			composes = []*proto.ComposeBallComposeData{{
				BallA: mate.Id, BallB: d.Id, Ball: m, ProcessBall: proc,
			}}
		}
		t.Logf("oneShot(cfg=%d claim=%v): mate=%d@(%s,%s) drop@(%s,%s) merged=%d claimed=(%s,%s) final=(%s,%s); board=%s",
			targetCfg, withClaim, mate.GetId(), mate.GetPosition()[0], mate.GetPosition()[1],
			d.GetPosition()[0], d.GetPosition()[1], m.GetId(), m.GetPosition()[0], m.GetPosition()[1],
			nb[len(nb)-1].GetPosition()[0], nb[len(nb)-1].GetPosition()[1], func() string {
				s := ""
				for _, e := range board {
					s += fmt.Sprintf("[%d/%d@%s,%s]", e.GetId(), e.GetConfigId(), e.GetPosition()[0], e.GetPosition()[1])
				}
				return s
			}())
		return upload(nb, composes)
	}

	// ── HONEST: drop + merge into L+1, the proven-accepted format ─────────
	var honestOK bool
	for rounds := 0; rounds < 20 && !honestOK; rounds++ {
		lv := st.LevelOf[curCfg]
		target := st.ConfigOf[lv+1]
		if target == 0 {
			break
		}
		ret, ok := oneShotMerge(target, true)
		if ret < 0 {
			payDrop()
			continue
		}
		honestOK = ok
		if !ok {
			t.Fatalf("HONEST one-shot refused ret=%d — the format stopped working or the board moved; aborting contrast", ret)
		}
		t.Logf("HONEST drop+merge L%d → L%d ACCEPTED (ret=0) — proven client format live-confirmed", lv, lv+1)
	}
	if !honestOK {
		t.Fatal("honest merge (proven format) refused — cannot run the contrast")
	}

	// ── CHEAT (identical shape, merged config = L+2) + RAW PUSH ───────────
	for rounds := 0; rounds < 24; rounds++ {
		lv := st.LevelOf[curCfg]
		jump := st.ConfigOf[lv+2]
		if jump == 0 {
			break
		}
		ret, ok := oneShotMerge(jump, true)
		if ret < 0 {
			payDrop()
			continue
		}
		if ok {
			t.Errorf("ANTI-CHEAT NOT ENFORCED: server accepted L%d+L%d → L%d (ret=0) with the proven-honest shape — conservation is NOT checked", lv, lv, lv+2)
		} else {
			t.Logf("CHEAT (identical shape, merged config = L%d) REFUSED ret=%d", lv+2, ret)
		}
		// RAW PUSH: the same fabricated board, NO composeData at all — the
		// user's literal "push a map contain one level-(L+2) ball".
		ret2, ok2 := oneShotMerge(jump, false)
		if ok2 {
			t.Errorf("RAW PUSH ACCEPTED (ret=0): a level-%d ball appeared with no merge claim its drop ledger cannot justify", lv+2)
		} else {
			t.Logf("RAW PUSH (level-%d ball, NO merge claim) REFUSED ret=%d", lv+2, ret2)
		}
		if !ok && !ok2 {
			t.Logf("VERDICT: server cross-check CONFIRMED exactly as the user stated — the honest L+1 merge with the proven format is accepted (ret=0) while the SAME shape claiming L+2 is refused, and the raw fabricated board with no claim is refused too.")
		}
		// NOTE: deliberately no SETTLE — it locks the account's compose for the day.
		return
	}
	t.Errorf("could not stage a drop-mergeable same-config ball for the cheat stage")
}

// ---- test-local board helpers (proto-typed) ---------------------------------

func cbSameLevelPair(p *Player, board []*proto.ComposeBallBallData) (*proto.ComposeBallBallData, *proto.ComposeBallBallData, int32) {
	p.CbMu.Lock()
	defer p.CbMu.Unlock()
	for i := range board {
		for j := i + 1; j < len(board); j++ {
			if board[i].GetConfigId() == board[j].GetConfigId() {
				if lv := p.cb.LevelOf[board[i].GetConfigId()]; lv > 0 {
					return board[i], board[j], lv
				}
			}
		}
	}
	return nil, nil, 0
}

func cbCopyBalls(in []*proto.ComposeBallBallData) []*proto.ComposeBallBallData {
	out := make([]*proto.ComposeBallBallData, 0, len(in))
	for _, b := range in {
		out = append(out, pb.Clone(b).(*proto.ComposeBallBallData))
	}
	return out
}

func cbWithout(board []*proto.ComposeBallBallData, drop ...*proto.ComposeBallBallData) []*proto.ComposeBallBallData {
	out := cbCopyBalls(board)
	res := out[:0]
	for _, b := range out {
		skip := false
		for _, d := range drop {
			if b.GetId() == d.GetId() {
				skip = true
			}
		}
		if !skip {
			res = append(res, b)
		}
	}
	return res
}

// ---- position helpers -------------------------------------------------------

func cbXY1(b *proto.ComposeBallBallData) float64 { x, _ := cbXY(b); return x }

func cbXY(b *proto.ComposeBallBallData) (float64, float64) {
	var x, y float64
	if ps := b.GetPosition(); len(ps) >= 2 {
		x, _ = strconv.ParseFloat(ps[0], 64)
		y, _ = strconv.ParseFloat(ps[1], 64)
	}
	return x, y
}

func cbMidX(a, b *proto.ComposeBallBallData) string {
	ax, _ := cbXY(a)
	bx, _ := cbXY(b)
	return cbFix5((ax + bx) / 2)
}
