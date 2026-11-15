package tytd_go

import "testing"

// Regressions for the 2026-10-04 710547 stops. (1) Two top-tier 111s never
// merge — the rest must still pack around them (notch geometry, not rows).
// (2) A merge pair that misses the shelf edge by 1px must still stage (pair
// notch slot) or the planner must fall back to a plain drop, never die.
func TestCBStuckRepro(t *testing.T) {
	st := cbTestState()
	st.Ground = -979.78986
	st.Balls = []cbBall{
		{ID: 801, Config: 105, X: 623, Y: -603.78986},
		{ID: 803, Config: 102, X: 44.5, Y: -448.28986},
		{ID: 739, Config: 109, X: 154, Y: -829.78986},
		{ID: 774, Config: 108, X: 429.5, Y: -854.28986},
		{ID: 665, Config: 111, X: 459, Y: -591.78986},
		{ID: 441, Config: 111, X: 283, Y: -591.78986},
		{ID: 798, Config: 107, X: 99.5, Y: -584.28986},
	}
	st.NextID = 804
	st.NextConfig = 105
	p := cbTestPlayer(st)
	balls, composes, ok := p.cbPlanLocked()
	if !ok {
		t.Fatal("must chain 105+105→106 and keep playing around the 111 blockers")
	}
	t.Logf("case1: balls=%d composes=%d", len(balls), len(composes))

	// case 2: the exact post-chain board + next 102 (pair misses shelf by 1px)
	st2 := cbTestState()
	st2.Ground = -979.78986
	st2.Balls = []cbBall{
		{ID: 803, Config: 102, X: 595.5, Y: -939.28986},
		{ID: 739, Config: 109, X: 154, Y: -829.78986},
		{ID: 805, Config: 106, X: 291.6673, Y: -477.83048},
		{ID: 774, Config: 108, X: 429.5, Y: -854.28986},
		{ID: 665, Config: 111, X: 512.40257, Y: -657.54278},
		{ID: 441, Config: 111, X: 157.56595, Y: -591.81658},
		{ID: 798, Config: 107, X: 328.90258, Y: -657.51293},
	}
	st2.NextID = 806
	st2.NextConfig = 102
	p2 := cbTestPlayer(st2)
	balls2, composes2, ok2 := p2.cbPlanLocked()
	if !ok2 {
		t.Fatal("102 drop must upload (claimed pair notch-staged or plain fallback), never die")
	}
	t.Logf("case2: balls=%d composes=%d", len(balls2), len(composes2))

	// case 3: the dense 20880-point board (14 balls, 4×105 + 3×104 among
	// big blockers): claim staging must shed the cramped pair, not die.
	st3 := cbTestState()
	st3.Ground = -979.78986
	st3.Balls = []cbBall{
		{ID: 834, Config: 107, X: 499.5, Y: -884.28986},
		{ID: 842, Config: 105, X: 227.86898, Y: -504.82391},
		{ID: 845, Config: 104, X: 654.5, Y: -920.28986},
		{ID: 847, Config: 103, X: 120.58175, Y: -432.30013},
		{ID: 848, Config: 101, X: 595.35238, Y: -671.77049},
		{ID: 849, Config: 104, X: 97.8555, Y: -542.99122},
		{ID: 817, Config: 110, X: 204, Y: -779.78986},
		{ID: 850, Config: 105, X: 629.29181, Y: -772.19018},
		{ID: 851, Config: 105, X: 547.98272, Y: -551.91134},
		{ID: 852, Config: 105, X: 475.03648, Y: -418.559},
		{ID: 853, Config: 104, X: 331.25511, Y: -417.23648},
		{ID: 854, Config: 105, X: 626.99612, Y: -422.06187},
		{ID: 665, Config: 111, X: 384.01142, Y: -554.97907},
		{ID: 441, Config: 111, X: 481.21203, Y: -701.70344},
	}
	st3.NextID = 855
	st3.NextConfig = 105
	p3 := cbTestPlayer(st3)
	balls3, composes3, ok3 := p3.cbPlanLocked()
	// With the line gate disabled (user directive 2026-10-04) even this dense
	// board must always upload — balls may rest above the old cbLineY and the
	// SERVER declares GameOver if that really ends the round.
	if !ok3 {
		t.Fatalf("case3: with the line gate disabled there is always a rest pose: %d/%d", len(balls3), len(composes3))
	}
	t.Logf("case3: balls=%d composes=%d", len(balls3), len(composes3))

	// case 4: the 7520-point 710564 board from screenshot 1000018888.png
	// (live stop log 2026-10-04). Two 105s on board + next 105: the old
	// echo-preserving packer STOPPED here ("no rolling rest") because the
	// six surrounding echoes were frozen — but a FULL REPACK fits (110+109
	// row 1, 108/106/105 row 2, 105/104/103 row 3) and merges the pair.
	// User: "there 2 bug ... 2: have 2 same pet, why not merge it?"
	st4 := cbTestState()
	st4.Ground = -979.74426
	st4.Balls = []cbBall{
		{ID: 256, Config: 109, X: 554, Y: -829.74426},
		{ID: 161, Config: 110, X: 204, Y: -779.74426},
		{ID: 322, Config: 104, X: 307.5, Y: -541.77788},
		{ID: 308, Config: 106, X: 92, Y: -514.41428},
		{ID: 325, Config: 103, X: 225.5, Y: -464.02828},
		{ID: 326, Config: 105, X: 354, Y: -414.13166},
		{ID: 300, Config: 108, X: 491.5, Y: -561.42728},
		{ID: 318, Config: 105, X: 640, Y: -425.2289},
	}
	st4.NextID = 327
	st4.NextConfig = 105
	p4 := cbTestPlayer(st4)
	balls4, composes4, ok4 := p4.cbPlanLocked()
	if !ok4 {
		t.Fatal("1000018888.png board MUST merge the two 105s via full repack — free space above the line is irrelevant, the repacked rows fit under it")
	}
	t.Logf("case4: balls=%d composes=%d", len(balls4), len(composes4))
	// Walls remain a hard invariant (outside them the server rejects). The
	// line gate is DISABLED by user directive, so tops may cross cbLineY.
	for _, b := range balls4 {
		x, _ := parseCBPos(t, b)
		r := float64(st4.WidthOf[st4.LevelOf[b.GetConfigId()]]) / 2
		if x < 4+r || x > 720-4-r {
			t.Fatalf("ball %d x=%.1f outside walls", b.GetId(), x)
		}
	}

	// case 5: <live-user> live board 2026-10-04 (score 17160, 22 balls, drop
	// 103). The session's right wall echoes at x+r≈749.9 — the 720 constant
	// made every wall-hugging echo "illegal" and the repack cascade-failed.
	st5 := cbTestState()
	st5.Ground = -979.5105
	st5.Right = 749.85754
	st5.Balls = []cbBall{
		{ID: 769, Config: 106, X: 547.34875, Y: -402.21432},
		{ID: 772, Config: 102, X: 709.24701, Y: -346.62128},
		{ID: 742, Config: 108, X: 125.73337, Y: -404.03464},
		{ID: 775, Config: 104, X: 632.41907, Y: -282.06476},
		{ID: 776, Config: 101, X: 719.85754, Y: -264.73834},
		{ID: 9, Config: 102, X: 242.57735, Y: -939.00598},
		{ID: 746, Config: 102, X: 40.74427, Y: -261.76749},
		{ID: 522, Config: 104, X: 324.43039, Y: -882.04913},
		{ID: 716, Config: 101, X: 719.74402, Y: -777.31641},
		{ID: 524, Config: 102, X: 543.22278, Y: -785.5614},
		{ID: 588, Config: 106, X: 88.25464, Y: -784.10364},
		{ID: 750, Config: 106, X: 661.75879, Y: -588.89771},
		{ID: 751, Config: 101, X: 567.94244, Y: -517.97992},
		{ID: 689, Config: 101, X: 220.09953, Y: -872.41791},
		{ID: 501, Config: 111, X: 471.32648, Y: -891.79077},
		{ID: 727, Config: 110, X: 364.62744, Y: -624.67877},
		{ID: 728, Config: 107, X: 654.29834, Y: -884.18884},
		{ID: 506, Config: 104, X: 144.6613, Y: -920.10437},
		{ID: 538, Config: 102, X: 40.7649, Y: -939.0105},
		{ID: 763, Config: 104, X: 690.11285, Y: -444.47952},
		{ID: 733, Config: 104, X: 627.271, Y: -731.90625},
		{ID: 607, Config: 103, X: 53.75525, Y: -647.1402},
	}
	st5.NextID = 777
	st5.NextConfig = 103
	p5 := cbTestPlayer(st5)
	balls5, composes5, ok5 := p5.cbPlanLocked()
	if !ok5 {
		t.Fatal("case5: 22-ball wall-hugging board must plan (drop 103) once the right edge is derived from echoes")
	}
	t.Logf("case5: balls=%d composes=%d", len(balls5), len(composes5))
}
