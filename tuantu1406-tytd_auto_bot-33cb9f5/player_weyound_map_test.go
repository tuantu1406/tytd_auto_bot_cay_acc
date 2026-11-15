package tytd_go

// Pure-Go proofs for the WeYound fog grid — the client-verbatim rules the
// march depends on (CDN json parse, initTileData merge, A*, BFS fog pick,
// light pick). These run offline; the live pass re-proves them on the wire.

import (
	"testing"
)

func TestWyMapBaseNormal(t *testing.T) {
	g := wyMapBase(101, 0)
	if g == nil {
		t.Fatal("map101 missing")
	}
	if len(g) != 40 {
		t.Fatalf("rows=%d want 40", len(g))
	}
	for _, row := range g {
		if len(row) != 56 {
			t.Fatalf("cols=%d want 56", len(row))
		}
	}
	// ground truth from the CDN json: 1569 zeros, 662 painted→Fog, 9 Base.
	var zero, fog, base int
	for y := range g {
		for x := range g[y] {
			switch g[y][x] {
			case 0:
				zero++
			case wyTileFog:
				fog++
			case wyTileBase:
				base++
			}
		}
	}
	if zero != 1569 || fog != 662 || base != 9 {
		t.Fatalf("histogram zero=%d fog=%d base=%d want 1569/662/9", zero, fog, base)
	}
	if wyMapBase(999, 0) != nil {
		t.Fatal("unknown mapId must be nil (never guess a layout)")
	}
	for _, id := range []int{102, 103} {
		if wyMapBase(id, 0) == nil {
			t.Fatalf("map%d missing", id)
		}
	}
}

func TestWyMapBaseBonusBoxes(t *testing.T) {
	g := wyMapBase(0, 76)
	if g == nil {
		t.Fatal("bonusMap missing")
	}
	// walk-order ≤ memberSize → RewardBox(9), above → RewardBoxNULL(8);
	// the json paints ids 1..76, so memberSize 76 makes ALL boxes ripe.
	var ripe, null, empty int
	for y := range g {
		for x := range g[y] {
			switch g[y][x] {
			case wyTileBox:
				ripe++
			case wyTileBoxNULL:
				null++
			case 0:
				empty++
			default:
				t.Fatalf("unexpected type %d at (%d,%d)", g[y][x], x, y)
			}
		}
	}
	if ripe != 76 || null != 0 {
		t.Fatalf("memberSize=76: ripe=%d null=%d want 76/0", ripe, null)
	}
	if g2 := wyMapBase(0, 10); g2 == nil {
		t.Fatal("memberSize variant missing")
	} else {
		var r, n int
		for y := range g2 {
			for x := range g2[y] {
				if g2[y][x] == wyTileBox {
					r++
				}
				if g2[y][x] == wyTileBoxNULL {
					n++
				}
			}
		}
		if r != 10 || n != 66 {
			t.Fatalf("memberSize=10: ripe=%d null=%d want 10/66", r, n)
		}
	}
}

func handGrid(w, h int32, types map[[2]int32]int32) *wyGrid {
	g := &wyGrid{w: w, h: h, cells: map[int32]*wyCell{}}
	for k, ty := range types {
		g.cells[wyGk(k[0], k[1], w)] = &wyCell{x: k[0], y: k[1], typ: ty}
	}
	return g
}

func TestWyBuildMerge(t *testing.T) {
	base := [][]int32{
		{wyTileFog, wyTileFog, 0},
		{wyTileFog, wyTileBlank, 0},
	}
	view := []*wyViewCell{
		{x: 0, y: 0, typ: wyTileBlank}, // opens a fog tile
		{x: 1, y: 0, typ: wyTileEvent, event: &wyEventLite{id: 9, evType: wyEvMonster}},
		{x: 99, y: 99, typ: wyTileBlank}, // out of map — dropped
	}
	g := wyBuildGrid(base, view)
	if g == nil {
		t.Fatal("nil grid")
	}
	if g.resolveType(0, 0) != wyTileBlank {
		t.Fatal("view must replace base")
	}
	if g.resolveType(1, 0) != wyTileEvent || !g.hasEvent(1, 0) {
		t.Fatal("view event lost")
	}
	if g.at(99, 99) != nil {
		t.Fatal("out-of-map view cell must be dropped")
	}
	if g.resolveType(0, 1) != wyTileFog {
		t.Fatal("base fog must survive where view is silent")
	}
	if g.resolveType(2, 0) != 0 || g.at(2, 0) != nil {
		t.Fatal("unpainted cell must be unknown")
	}
}

func TestWyRootResolution(t *testing.T) {
	g := &wyGrid{w: 3, h: 1, cells: map[int32]*wyCell{}}
	ev := &wyEventLite{id: 42, evType: wyEvMonster}
	g.cells[wyGk(0, 0, 3)] = &wyCell{x: 0, y: 0, typ: wyTileEvent, event: ev}
	g.cells[wyGk(1, 0, 3)] = &wyCell{x: 1, y: 0, typ: wyTileEvent, event: ev, root: &wyPosLite{0, 0}}
	if g.resolveType(1, 0) != wyTileEvent || g.resolveEvent(1, 0) != ev {
		t.Fatal("root tile must resolve through root cell")
	}
	if g.walkable(0, 0) || g.walkable(1, 0) {
		t.Fatal("root-resolved monster event must obstruct both tiles")
	}
}

func TestWyWalkableTrapExempt(t *testing.T) {
	g := handGrid(2, 1, map[[2]int32]int32{{0, 0}: wyTileBlank, {1, 0}: wyTileBlank})
	g.cells[wyGk(0, 0, 2)].event = &wyEventLite{id: 1, evType: wyEvTrap}
	g.cells[wyGk(1, 0, 2)].event = &wyEventLite{id: 2, evType: wyEvSelect}
	if !g.walkable(0, 0) {
		t.Fatal("Trap events never obstruct (client IsObstract)")
	}
	if g.walkable(1, 0) {
		t.Fatal("Select event must obstruct")
	}
}

func TestWyNearestCanExplore(t *testing.T) {
	//   F F F F
	//   F B B F     B = blank (walkable), F = fog
	//   F B F F
	// fog at (0,1) neighbours (1,1) B → reachable; fog (2,2) is boxed by fog
	// + blank at (1,2) → also reachable. BFS from (1,1) must return SOME
	// fog tile, never an obstacle-locked one.
	g := handGrid(4, 3, map[[2]int32]int32{
		{0, 0}: wyTileFog, {1, 0}: wyTileFog, {2, 0}: wyTileFog, {3, 0}: wyTileFog,
		{0, 1}: wyTileFog, {1, 1}: wyTileBlank, {2, 1}: wyTileBlank, {3, 1}: wyTileFog,
		{0, 2}: wyTileFog, {1, 2}: wyTileBlank, {2, 2}: wyTileFog, {3, 2}: wyTileFog,
	})
	x, y, ok := g.nearestCanExplore(1, 1, nil)
	if !ok {
		t.Fatal("no fog found")
	}
	if g.resolveType(x, y) != wyTileFog {
		t.Fatalf("returned (%d,%d) type %d — not fog", x, y, g.resolveType(x, y))
	}
	if !g.isFogReachable(x, y) {
		t.Fatalf("returned (%d,%d) not reachable", x, y)
	}
	// skip everything → no result, march must stop
	if _, _, ok := g.nearestCanExplore(1, 1, func(a, b int32) bool { return true }); ok {
		t.Fatal("skip veto ignored")
	}
	// obstacle wall: fog behind Obstacle-only border is unreachable
	g2 := handGrid(3, 1, map[[2]int32]int32{{0, 0}: wyTileBlank, {1, 0}: wyTileObstacle, {2, 0}: wyTileFog})
	if _, _, ok := g2.nearestCanExplore(0, 0, nil); ok {
		t.Fatal("must not path through Obstacle")
	}
}

func TestWyFogPosSurrounded(t *testing.T) {
	// 3×3 fog block at (0..2, 0..2) next to player (3,3) plus one lonely
	// fog at (0,5): the surrounded pick prefers the block's most
	// fog-neighboured tile — the center (1,1) has 4 fog neighbours.
	g := handGrid(6, 6, map[[2]int32]int32{})
	for yy := int32(0); yy < 3; yy++ {
		for xx := int32(0); xx < 3; xx++ {
			g.cells[wyGk(xx, yy, 6)] = &wyCell{x: xx, y: yy, typ: wyTileFog}
		}
	}
	g.cells[wyGk(0, 5, 6)] = &wyCell{x: 0, y: 5, typ: wyTileFog}
	g.cells[wyGk(3, 3, 6)] = &wyCell{x: 3, y: 3, typ: wyTileBlank}
	x, y, ok := g.fogPosSurrounded(3, 3)
	if !ok {
		t.Fatal("no fog")
	}
	if x != 1 || y != 1 {
		t.Fatalf("picked (%d,%d), want the 4-neighbour fog center (1,1)", x, y)
	}
}

func TestWyFindPath(t *testing.T) {
	//   B B F
	//   B O F      O = obstacle
	//   B B F      player (0,2) → goal fog (2,0): must A* around the wall.
	g := handGrid(3, 3, map[[2]int32]int32{
		{0, 0}: wyTileBlank, {1, 0}: wyTileBlank, {2, 0}: wyTileFog,
		{0, 1}: wyTileBlank, {1, 1}: wyTileObstacle, {2, 1}: wyTileFog,
		{0, 2}: wyTileBlank, {1, 2}: wyTileBlank, {2, 2}: wyTileFog,
	})
	p := g.findPath(0, 2, 2, 0, true)
	if len(p) < 2 || p[len(p)-1] != [2]int32{2, 0} {
		t.Fatalf("no path to fog goal: %v", p)
	}
	for _, st := range p[1 : len(p)-1] { // intermediates must be walkable
		if !g.walkable(st[0], st[1]) {
			t.Fatalf("path crosses (%d,%d)", st[0], st[1])
		}
	}
	if p2 := g.findPath(0, 2, 2, 0, false); p2 != nil {
		t.Fatal("allowGoal=false must refuse Fog goal (IsObstract goal)")
	}
	// fully walled goal → nil
	g3 := handGrid(3, 3, map[[2]int32]int32{{0, 0}: wyTileBlank, {1, 0}: wyTileObstacle, {0, 1}: wyTileObstacle})
	if p3 := g3.findPath(0, 0, 1, 0, true); len(p3) < 2 {
		t.Fatalf("allowGoal must reach an adjacent obstructed goal: %v", p3)
	}
	if p4 := g3.findPath(0, 0, 2, 2, true); p4 != nil {
		t.Fatal("unreachable must be nil")
	}
}

func TestWyMovePathSibling(t *testing.T) {
	// Event (2 tiles share uid 7); goal tile (2,0) is walled off, its
	// sibling (1,1) is reachable → getMovePath retries and finds it.
	ev := &wyEventLite{id: 7, evType: wyEvMonster}
	g := &wyGrid{w: 3, h: 2, cells: map[int32]*wyCell{}}
	set := func(x, y, t int32) *wyCell {
		c := &wyCell{x: x, y: y, typ: t}
		g.cells[wyGk(x, y, 3)] = c
		return c
	}
	set(0, 0, wyTileBlank)
	set(0, 1, wyTileBlank)
	set(1, 0, wyTileObstacle)
	set(2, 0, wyTileEvent).event = ev
	set(2, 1, wyTileObstacle)
	c := set(1, 1, wyTileEvent)
	c.event = ev
	p := g.movePath(0, 0, 2, 0)
	if len(p) < 2 {
		t.Fatalf("sibling retry failed: %v", p)
	}
	last := p[len(p)-1]
	if last != [2]int32{1, 1} {
		t.Fatalf("expected sibling (1,1), got %v", last)
	}
}

func TestWyHasNextFloorSight(t *testing.T) {
	g := handGrid(2, 2, map[[2]int32]int32{{0, 0}: wyTileBlank, {1, 1}: wyTileNextFloor})
	if !g.hasNextFloorTile() {
		t.Fatal("view-sighted NextFloor must unlock")
	}
	g2 := handGrid(2, 1, map[[2]int32]int32{{0, 0}: wyTileBlank, {1, 0}: wyTileFog})
	if g2.hasNextFloorTile() {
		t.Fatal("base-only NextFloor (never in view) must stay locked")
	}
}
