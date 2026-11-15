package tytd_go

// Unit tests for the true-size rest-state planner (user directive 2026-10-04:
// "spawn ball then merge recursive 2 same ball then draw it then send to
// server, must true ball size"). Deterministic — no live server.

import (
	"context"
	"fmt"
	"math"
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func cbTestState() *composeBallState {
	st := &composeBallState{
		LevelOf:    map[int32]int32{},
		ConfigOf:   map[int32]int32{},
		ScoreOf:    map[int32]int32{},
		WidthOf:    map[int32]int32{},
		Ground:     cbGroundFallback,
		NextID:     10,
		Have:       true,
		NextConfig: 101,
	}
	// The live tier table from server 710547 (2026-10-04 probe).
	widths := map[int32]int32{101: 60, 102: 81, 103: 107, 104: 119, 105: 152, 106: 176, 107: 191, 108: 251, 109: 300, 110: 400, 111: 176}
	for i, w := range []int32{101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111} {
		lv := int32(i + 1)
		st.ConfigOf[lv] = w
		st.LevelOf[w] = lv
		st.WidthOf[lv] = widths[w]
	}
	return st
}

func cbTestPlayer(st *composeBallState) *Player {
	ctx, cancel := context.WithCancel(context.Background())
	t := &Player{Username: "unit", ctx: ctx, cancel: cancel, cb: st}
	return t
}

// TestCBPlanEmptySpawn: first drop on an empty board sits at the true floor
// rest height ground + width/2.
func TestCBPlanEmptySpawn(t *testing.T) {
	st := cbTestState()
	p := cbTestPlayer(st)
	balls, composes, ok := p.cbPlanLocked()
	if !ok {
		t.Fatal("empty board must accept a drop")
	}
	if len(balls) != 1 || len(composes) != 0 {
		t.Fatalf("want 1 ball 0 merges, got %d/%d", len(balls), len(composes))
	}
	wantY := cbGroundFallback + 30 // cfg101 width 60
	if balls[0].GetPosition()[1] != cbFix5(wantY) {
		t.Fatalf("spawn y=%s want %s", balls[0].GetPosition()[1], cbFix5(wantY))
	}
	x, _ := parseCBPos(t, balls[0])
	if x < 4+30-1 || x > 720-4-30+1 {
		t.Fatalf("spawn x=%s outside walls", balls[0].GetPosition()[0])
	}
}

func parseCBPos(t *testing.T, b *proto.ComposeBallBallData) (float64, float64) {
	t.Helper()
	ps := b.GetPosition()
	if len(ps) != 2 {
		t.Fatalf("ball %d pos %v", b.GetId(), ps)
	}
	var x, y float64
	x, _ = parseCB(ps[0])
	y, _ = parseCB(ps[1])
	return x, y
}

func parseCB(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscan(s, &f)
	return f, err
}

// TestCBPlanPairCascade: dropping the next fruit exactly onto a same-level
// ball's contact point must merge them (two out, one in) into L+1 resting on
// the floor at the parents' midpoint — the conservation shape the server
// cross-checks.
func TestCBPlanPairCascade(t *testing.T) {
	st := cbTestState()
	st.Balls = []cbBall{{ID: 5, Config: 101, X: 300, Y: cbGroundFallback + 30}}
	st.NextID = 10
	p := cbTestPlayer(st)
	balls, composes, ok := p.cbPlanLocked()
	if !ok {
		t.Fatal("pair must plan")
	}
	if len(composes) != 1 {
		t.Fatalf("want 1 cascade merge, got %d", len(composes))
	}
	c := composes[0]
	if (c.GetBallA() == 5) == (c.GetBallB() == 5) {
		t.Fatalf("merge must consume the old ball 5: %d+%d", c.GetBallA(), c.GetBallB())
	}
	if c.GetBall().GetConfigId() != 102 {
		t.Fatalf("merged cfg %d, want 102", c.GetBall().GetConfigId())
	}
	if len(balls) != 1 {
		t.Fatalf("2 out 1 in → want 1 ball on board, got %d", len(balls))
	}
	if balls[0].GetConfigId() != 102 || balls[0].GetId() != 11 { // merged = syncId+1
		t.Fatalf("final ball id=%d cfg=%d", balls[0].GetId(), balls[0].GetConfigId())
	}
	wantY := cbGroundFallback + 40.5 // cfg102 width 81
	if balls[0].GetPosition()[1] != cbFix5(wantY) {
		t.Fatalf("merged y=%s want %s (true size on floor)", balls[0].GetPosition()[1], cbFix5(wantY))
	}
}

// TestCBPlanRecursiveCascade (user directive 2026-10-04: "your code missing
// merge recursive — alway merge"): one drop must claim the WHOLE cascade in
// one upload. Board = L1 mate + L2 ball; the drop merges the mate, the fresh
// L2 lands touching the old L2 and merges again → two ComposeData entries
// with merged ids syncId+1 and syncId+2, and the final board holds only the
// L3. Conservation drops→merges is exactly 2-out-1-in per claim.
func TestCBPlanRecursiveCascade(t *testing.T) {
	st := cbTestState()
	st.Balls = []cbBall{
		{ID: 5, Config: 101, X: 300, Y: cbGroundFallback + 30},   // L1 mate
		{ID: 6, Config: 102, X: 381, Y: cbGroundFallback + 40.5}, // L2 chain target (381−300−? contact at 81)
	}
	st.NextID = 10
	st.NextConfig = 101
	p := cbTestPlayer(st)
	balls, composes, ok := p.cbPlanLocked()
	if !ok {
		t.Fatal("cascade board must plan")
	}
	if len(composes) != 2 {
		t.Fatalf("want 2 recursive merges in ONE upload, got %d", len(composes))
	}
	c0, c1 := composes[0], composes[1]
	if c0.GetBall().GetId() != 11 || c0.GetBall().GetConfigId() != 102 {
		t.Fatalf("merge#1 = id%d cfg%d, want 11/102 (syncId+1)", c0.GetBall().GetId(), c0.GetBall().GetConfigId())
	}
	if (c0.GetBallA() == 5) != (c0.GetBallB() == 10) || (c0.GetBallA() == c0.GetBallB()) {
		t.Fatalf("merge#1 must consume mate 5 + drop 10: %d+%d", c0.GetBallA(), c0.GetBallB())
	}
	if len(c0.GetProcessBall()) != 3 { // mate, old-L2, drop — all live at contact
		t.Fatalf("merge#1 processBall %d balls, want 3", len(c0.GetProcessBall()))
	}
	if c1.GetBall().GetId() != 12 || c1.GetBall().GetConfigId() != 103 {
		t.Fatalf("merge#2 = id%d cfg%d, want 12/103 (syncId+2)", c1.GetBall().GetId(), c1.GetBall().GetConfigId())
	}
	if c1.GetBallA() != 6 || c1.GetBallB() != 11 {
		t.Fatalf("merge#2 must consume old L2 (6) + fresh L2 (11), got %d+%d", c1.GetBallA(), c1.GetBallB())
	}
	if len(c1.GetProcessBall()) != 2 { // drop & L1 mate gone by now
		t.Fatalf("merge#2 processBall %d balls, want 2", len(c1.GetProcessBall()))
	}
	if len(balls) != 1 {
		t.Fatalf("final board %d balls, want only the L3", len(balls))
	}
	if balls[0].GetId() != 12 || balls[0].GetConfigId() != 103 {
		t.Fatalf("final ball id%d cfg%d, want 12/103", balls[0].GetId(), balls[0].GetConfigId())
	}
	wantY := cbGroundFallback + 53.5 // L3 true size on the floor
	if balls[0].GetPosition()[1] != cbFix5(wantY) {
		t.Fatalf("L3 y=%s want %s", balls[0].GetPosition()[1], cbFix5(wantY))
	}
}

// TestCBPlanLineCap: the line FORMULA (y = ground + 2/3·height — confirmed
// pixel-exact by user screenshot 1000018887.png) is still what GameOver
// would be computed from, and cbLineY must keep it for logs/settle checks.
// The line GATE is disabled by user directive ("just disable the threadhold
// gate"): even with a container so shallow every crown is above cbLineY, the
// planner must PLAN and let the server decide.
func TestCBPlanLineCap(t *testing.T) {
	if got := cbLineY(cbGroundFallback); math.Abs(got-(-979.74426*1.0/3)) > 1e-4 {
		t.Fatalf("line on 564-style board %.5f want %.5f", got, -979.74426/3)
	}
	st := cbTestState()
	st.Ground = -60 // height 60 → line at -20; floor-rest L1 top = 0 crosses it
	p := cbTestPlayer(st)
	if _, _, ok := p.cbPlanLocked(); !ok {
		t.Fatal("line gate is DISABLED — must plan anyway, server decides GameOver")
	}
}

// TestCBPlanNoSameConfigContact: after planning, no two SAME-config balls may
// still be within contact distance (unmerged pair = planner bug), and no two
// balls may overlap beyond touching.
func TestCBPlanNoSameConfigContact(t *testing.T) {
	st := cbTestState()
	// Ladder of L1 balls at contact spacing on both sides — the cascade must
	// consume pairs, never leave same-config contact.
	st.Balls = []cbBall{
		{ID: 1, Config: 101, X: 200, Y: cbGroundFallback + 30},
		{ID: 2, Config: 101, X: 260, Y: cbGroundFallback + 30}, // contact pair → merge
		{ID: 3, Config: 103, X: 450, Y: cbGroundFallback + 53.5},
	}
	st.NextID = 100
	p := cbTestPlayer(st)
	balls, _, ok := p.cbPlanLocked()
	if !ok {
		t.Fatal("board has room: plan expected")
	}
	type bc struct {
		id   int32
		cfg  int32
		x, y float64
	}
	var got []bc
	for _, b := range balls {
		x, y := parseCBPos(t, b)
		got = append(got, bc{b.GetId(), b.GetConfigId(), x, y})
	}
	for i := range got {
		for j := i + 1; j < len(got); j++ {
			ri := float64(st.WidthOf[st.LevelOf[got[i].cfg]]) / 2
			rj := float64(st.WidthOf[st.LevelOf[got[j].cfg]]) / 2
			d := math.Abs(got[i].x - got[j].x)
			if got[i].cfg == got[j].cfg && d <= (ri+rj)*1.02+0.01 {
				t.Fatalf("planner left same-config contact pair %.0f: %.0f (%.1f ≤ %.1f)",
					got[i].x, got[j].x, d, (ri+rj)*1.02)
			}
			if d < ri+rj-0.06 {
				t.Fatalf("planner overlapped %d/%d: %.0f vs %.0f (min %.1f)", got[i].id, got[j].id, got[i].x, got[j].x, ri+rj)
			}
		}
	}
}

// TestCBPlanRepackFits: the FREE-MERGE engine repacks the whole board every
// upload (relocation proven accepted live — TestLiveComposeBallFreeMerge),
// so a "beside it there is no room" board is no longer a death: cfg110 +
// cfg106 simply pack left→right on one row.
func TestCBPlanRepackFits(t *testing.T) {
	st := cbTestState()
	st.Balls = []cbBall{{ID: 7, Config: 110, X: 360, Y: cbGroundFallback + 200}}
	st.NextConfig = 106
	p := cbTestPlayer(st)
	balls, _, ok := p.cbPlanLocked()
	if !ok {
		t.Fatal("repack must fit 110+106 on one row (width 400+176 ≤ 712)")
	}
	if len(balls) != 2 {
		t.Fatalf("want 2 balls, got %d", len(balls))
	}
	// Echo-preserving policy: the 110 KEEPS its server pose (x=360, floor),
	// the fresh 106 takes the lowest free rest (floor beside it). What matters
	// is the invariant: both rest, both clear the line, neither overlaps.
	if balls[0].GetId() != 7 {
		t.Fatalf("first ball id %d, want 7", balls[0].GetId())
	}
	x0, y0 := parseCBPos(t, balls[0])
	if y0 != cbGroundFallback+200 || x0 != 360 {
		t.Fatalf("echoed 110 moved to (%.1f,%.1f) — echoes must be preserved", x0, y0)
	}
	drop := balls[1]
	if drop.GetId() != 10 {
		t.Fatalf("drop id %d, want 10", drop.GetId())
	}
	xd, yd := parseCBPos(t, drop)
	floor := math.Abs(yd-(cbGroundFallback+88)) < 0.01
	tangent := math.Abs(math.Hypot(xd-x0, yd-y0)-288) < 0.6
	if !floor && !tangent {
		t.Fatalf("106 drop (%.1f,%.1f) hovers: neither floor rest nor tangent contact", xd, yd)
	}
}

// TestCBGroundFromEcho: cbApplySync anchors the ground line at the lowest
// (echoY − true radius) it ever sees — the cfg-104 live anchor must land on
// -852.21637.
func TestCBGroundFromEcho(t *testing.T) {
	p := cbTestPlayer(&composeBallState{})
	sync := &proto.ComposeBallSyncData{
		Id: pb.Int32(209),
		Balls: []*proto.ComposeBallBallData{
			{Id: pb.Int32(208), ConfigId: pb.Int32(104), Position: []string{cbFix5(120), cbFix5(anchorYCfg104)}},
			{Id: pb.Int32(209), ConfigId: pb.Int32(104), Position: []string{cbFix5(380), cbFix5(anchorYCfg104)}},
		},
	}
	// The tier table must be in place first (it defines the radii).
	p.CbMu.Lock()
	p.cb = cbTestState()
	p.cb.Balls, p.cb.Ground = nil, 0
	p.CbMu.Unlock()
	p.cbApplySync(sync)
	want := anchorYCfg104 - 59.5 // cfg104 at rest: ground = echoY − width/2
	if g := p.cb.Ground; math.Abs(g-want) > 1e-6 {
		t.Fatalf("ground %.5f want %.5f", g, want)
	}
}

// anchorYCfg104 is a recorded server echo: a config-104 ball at rest (both
// servers, 2026-10-04). (Moved here from the live-tagged file, 2026-11-10.)
const anchorYCfg104 = -792.71637
