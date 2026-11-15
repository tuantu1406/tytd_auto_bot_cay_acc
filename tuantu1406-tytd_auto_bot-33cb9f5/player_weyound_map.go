package tytd_go

/*
WeYound FOG GRID (2026-10-18) — why this file exists: the live probe proved
the server's ENTER/EXPLORE view ONLY ever contains OPENED cells — Fog /
Event / Obstacle tiles NEVER ride the wire. The client builds its board from
a STATIC CDN map json (getMapJsonUrl → remoteAssets/remoteWeYound/<ver>/
json/map<mapId>.json, weyound bundle) and overlays the server view on top
(initTileData: view cells win, base paints what the view omits). Every
client decision — getNearCanExplorePos BFS, AStarPathUtil.FindPath, the
light puzzle pick — walks THAT grid, not the wire view. So the bot must too.
This file is a faithful port of:

  * initTileData (both variants) — normal maps: painted cell keeps its type
    only for {Blank3, Obstacle4, NextFloor5, Base7}, everything else is
    Fog; bonus maps: painted cell is RewardBox(9) while walk-index ≤
    memberSize, else RewardBoxNULL(8); server view cells replace base.
  * resolveTileType / getEventData — root tiles resolve through their root.
  * IsObstract — passable iff resolved type ∈ {Base7, Blank3, NextFloor5}
    and no event, EXCEPT Trap events never obstruct.
  * getNearCanExplorePos — 4-neighbour BFS d≤200; first Fog tile with a
    known non-Fog non-Obstacle event-free 4-neighbour (isFogTileReachable).
  * getFogPosSurroundedByFog — fog tile with the most fog neighbours
    (bucket min(n,4) high→low), nearest manhattan to the player.
  * AStarPathUtil.FindPath — 4-dir A*, Manhattan h, obstructed nodes are
    skipped unless they are the goal and allowGoal (the march target may be
    Fog/Event); getMovePath retries against a same-uid sibling tile when an
    Event target dead-ends.

The live map ids (0=bonusMap, 101, 102, 103 — the mapConfig rows seen live
on server 710564) are embedded from the CDN. An unknown mapId (config
drift) yields nil and the tick REFUSES to march with a visible log — the
bot never guesses a layout.
*/

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
)

//go:embed weyound_maps/bonusMap.json
var wyMapJSON0 []byte

//go:embed weyound_maps/map101.json
var wyMapJSON101 []byte

//go:embed weyound_maps/map102.json
var wyMapJSON102 []byte

//go:embed weyound_maps/map103.json
var wyMapJSON103 []byte

var (
	wyMapMu     sync.Mutex
	wyMapParsed = map[string][][]int32{} // "mapId:memberSize" → rows of base types
)

// wyMapBase loads (and caches) the base type grid of one CDN map json,
// painting cells exactly like the client's initTileData variant for that
// map. nil = unknown mapId (config drift) — caller must not march.
func wyMapBase(mapId int, memberSize int32) [][]int32 {
	key := strconv.Itoa(mapId) + ":" + strconv.Itoa(int(memberSize))
	wyMapMu.Lock()
	defer wyMapMu.Unlock()
	if g, ok := wyMapParsed[key]; ok {
		return g
	}
	var raw []byte
	switch mapId {
	case 0:
		raw = wyMapJSON0
	case 101:
		raw = wyMapJSON101
	case 102:
		raw = wyMapJSON102
	case 103:
		raw = wyMapJSON103
	default:
		return nil
	}
	var doc struct {
		Row    int      `json:"row"`
		Col    int      `json:"col"`
		MapVec []string `json:"mapVec"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Row <= 0 || doc.Col <= 0 {
		log.Printf("[TCVU] map json %d hỏng (%v) — không cuốc được tầng này", mapId, err)
		return nil
	}
	if len(doc.MapVec) != doc.Row {
		log.Printf("[TCVU] map json %d: mapVec %d dòng ≠ row %d — bỏ qua", mapId, len(doc.MapVec), doc.Row)
		return nil
	}
	grid := make([][]int32, doc.Row) // grid[y][x]
	for y, line := range doc.MapVec {
		parts := strings.Split(line, ",")
		if len(parts) < doc.Col {
			log.Printf("[TCVU] map json %d: dòng %d có %d ô < col %d — bỏ qua", mapId, y, len(parts), doc.Col)
			return nil
		}
		row := make([]int32, doc.Col)
		for x := 0; x < doc.Col; x++ {
			v, err := strconv.Atoi(strings.TrimSpace(parts[x]))
			if err != nil || v <= 0 {
				continue // 0 → not part of the playable grid
			}
			if mapId == 0 {
				// bonus floor (client WeYoundBonusTileMapModel): every
				// painted cell is a reward box, walk-order ≤ memberSize
				// ripe, the rest NULL boxes.
				if v <= int(memberSize) {
					row[x] = wyTileBox
				} else {
					row[x] = wyTileBoxNULL
				}
				continue
			}
			switch v {
			case wyTileBlank, wyTileObstacle, wyTileNextFloor, wyTileBase:
				row[x] = int32(v)
			default:
				row[x] = wyTileFog
			}
		}
		grid[y] = row
	}
	wyMapParsed[key] = grid
	return grid
}

// ─── display grid (base ∪ server view) ───────────────────────────────────────

// wyCell is one display tile: base type/equipment, possibly replaced whole
// by a server view cell (initTileData takes {type,event,reachable,root}
// from the view cell verbatim — event and root always arrive together).
type wyCell struct {
	x, y      int32
	typ       int32
	event     *wyEventLite
	reachable bool
	root      *wyPosLite
}

type wyEventLite struct {
	id       int64
	objId    int64 // WeYoundEventMsg.objId — for Monster events the live probe
	// (2026-11-13) shows cell ev.id (9717) ≠ monster msg Id (104189): objId is
	// the only candidate key back to the scene monster record (configId match
	// is the fallback).
	configId int32
	evType   int32
}

type wyPosLite struct{ x, y int32 }

type wyGrid struct {
	w, h  int32             // w=col, h=row; cells keyed y*w+x
	cells map[int32]*wyCell
}

// wyViewCell is the flat form of one server view cell (decoupled from proto
// so the pure board tests don't need protobuf).
type wyViewCell struct {
	x, y      int32
	typ       int32
	reachable bool
	event     *wyEventLite
	root      *wyPosLite
}

// wyBuildGrid merges a base grid with the server view (client initTileData:
// view cells win entirely over base, base paints what the view omits;
// out-of-map view cells are dropped like the client's "传入数据异常1").
func wyBuildGrid(base [][]int32, view []*wyViewCell) *wyGrid {
	if base == nil {
		return nil
	}
	g := &wyGrid{h: int32(len(base)), cells: map[int32]*wyCell{}}
	for y := int32(0); y < g.h; y++ {
		if g.w < int32(len(base[y])) {
			g.w = int32(len(base[y]))
		}
	}
	for y := int32(0); y < g.h; y++ {
		for x := int32(0); x < int32(len(base[y])); x++ {
			t := base[y][x]
			if t <= 0 {
				continue
			}
			g.cells[wyGk(x, y, g.w)] = &wyCell{x: x, y: y, typ: t}
		}
	}
	for _, v := range view {
		if v == nil || v.x < 0 || v.y < 0 || v.x >= g.w || v.y >= g.h {
			continue
		}
		g.cells[wyGk(v.x, v.y, g.w)] = &wyCell{x: v.x, y: v.y, typ: v.typ,
			reachable: v.reachable, event: v.event, root: v.root}
	}
	return g
}

func wyGk(x, y, w int32) int32 { return y*w + x }

func (g *wyGrid) at(x, y int32) *wyCell {
	if g == nil || x < 0 || y < 0 || x >= g.w || y >= g.h {
		return nil
	}
	return g.cells[wyGk(x, y, g.w)]
}

// resolveCell walks a root tile through to its root cell (client resolve-
// TileType/IsObstract/getEventData: type AND event read from the root cell
// when present).
func (g *wyGrid) resolveCell(c *wyCell) *wyCell {
	if c == nil {
		return nil
	}
	if c.root != nil {
		if rc := g.at(c.root.x, c.root.y); rc != nil {
			return rc
		}
	}
	return c
}

// resolveType — client resolveTileType; 0 means "no such tile" (nil).
func (g *wyGrid) resolveType(x, y int32) int32 {
	c := g.at(x, y)
	if c == nil {
		return 0
	}
	return g.resolveCell(c).typ
}

// resolveEvent — client getEventData (root-aware).
func (g *wyGrid) resolveEvent(x, y int32) *wyEventLite {
	c := g.at(x, y)
	if c == nil {
		return nil
	}
	return g.resolveCell(c).event
}

func (g *wyGrid) hasEvent(x, y int32) bool { return g.resolveEvent(x, y) != nil }

// walkable — !IsObstract: resolved type ∈ {Base, Blank, NextFloor} and no
// event, EXCEPT Trap events never obstruct.
func (g *wyGrid) walkable(x, y int32) bool {
	c := g.at(x, y)
	if c == nil {
		return false
	}
	r := g.resolveCell(c)
	switch r.typ {
	case wyTileBase, wyTileBlank, wyTileNextFloor:
	default:
		return false
	}
	if r.event != nil && r.event.evType != wyEvTrap {
		return false
	}
	return true
}

var wyDirs = [4][2]int32{{0, 1}, {0, -1}, {1, 0}, {-1, 0}}

// isFogReachable — client isFogTileReachable: any 4-neighbour is a known,
// non-Fog, non-Obstacle, event-free tile.
func (g *wyGrid) isFogReachable(x, y int32) bool {
	for _, d := range wyDirs {
		nx, ny := x+d[0], y+d[1]
		t := g.resolveType(nx, ny)
		if t == 0 {
			continue
		}
		if t != wyTileFog && t != wyTileObstacle && !g.hasEvent(nx, ny) {
			return true
		}
	}
	return false
}

// nearestCanExplore — client getNearCanExplorePos: 4-neighbour BFS (d≤200)
// over known non-Obstacle tiles from the player; the FIRST Fog tile whose
// neighbourhood contains a passable event-free tile wins. skip() vetoes
// cells the caller already tried (dead-end memory across one tick).
func (g *wyGrid) nearestCanExplore(px, py int32, skip func(x, y int32) bool) (int32, int32, bool) {
	if g == nil || g.at(px, py) == nil {
		return 0, 0, false
	}
	type node struct{ x, y, d int32 }
	visited := map[int32]bool{wyGk(px, py, g.w): true}
	queue := []node{{px, py, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.d > 200 {
			break
		}
		for _, dr := range wyDirs {
			nx, ny := cur.x+dr[0], cur.y+dr[1]
			k := wyGk(nx, ny, g.w)
			if visited[k] {
				continue
			}
			visited[k] = true
			t := g.resolveType(nx, ny)
			if t == 0 {
				continue
			}
			if t == wyTileFog {
				if g.isFogReachable(nx, ny) && (skip == nil || !skip(nx, ny)) {
					return nx, ny, true
				}
				continue
			}
			if t != wyTileObstacle {
				queue = append(queue, node{nx, ny, cur.d + 1})
			}
		}
	}
	return 0, 0, false
}

// fogPosSurrounded — client getFogPosSurroundedByFog: bucket all Fog tiles
// by min(#fog-neighbours,4), take the fullest bucket, nearest manhattan to
// (px,py) within it. Drives the Light puzzle selectView pick.
func (g *wyGrid) fogPosSurrounded(px, py int32) (int32, int32, bool) {
	if g == nil {
		return 0, 0, false
	}
	var best [5]*wyPosLite
	var bestDist [5]int64
	for k := range bestDist {
		bestDist[k] = math.MaxInt64
	}
	for k := range g.cells {
		x, y := k%g.w, k/g.w
		if g.resolveType(x, y) != wyTileFog {
			continue
		}
		n := 0
		for _, d := range wyDirs {
			if g.resolveType(x+d[0], y+d[1]) == wyTileFog {
				n++
			}
		}
		if n > 4 {
			n = 4
		}
		dist := int64(wyAbs32(px-x) + wyAbs32(py-y))
		if dist < bestDist[n] {
			bestDist[n] = dist
			best[n] = &wyPosLite{x, y}
		}
	}
	for b := 4; b >= 0; b-- {
		if best[b] != nil {
			return best[b].x, best[b].y, true
		}
	}
	return 0, 0, false
}

func wyAbs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// findPath — AStarPathUtil.FindPath: 4-dir A*, Manhattan h; obstructed
// nodes are skipped unless allowGoal and they are the goal (the march
// target may itself be Fog/Event). Returns the FULL path incl. start and
// goal (client semantics), nil when none.
func (g *wyGrid) findPath(sx, sy, tx, ty int32, allowGoal bool) [][2]int32 {
	if g == nil || g.at(sx, sy) == nil || g.at(tx, ty) == nil {
		return nil
	}
	if !allowGoal && !g.walkable(tx, ty) {
		return nil
	}
	if sx == tx && sy == ty {
		return [][2]int32{{sx, sy}, {sx, sy}}
	}
	type astar struct {
		x, y   int32
		g, h   int32
		parent *astar
	}
	key := func(x, y int32) int32 { return wyGk(x, y, g.w) }
	start := &astar{x: sx, y: sy, h: wyAbs32(sx-tx) + wyAbs32(sy-ty)}
	open := []*astar{start}
	inOpen := map[int32]bool{key(sx, sy): true}
	inClosed := map[int32]bool{}
	for len(open) > 0 {
		bi := 0
		for i := range open {
			if open[i].g+open[i].h < open[bi].g+open[bi].h {
				bi = i
			}
		}
		cur := open[bi]
		open = append(open[:bi], open[bi+1:]...)
		delete(inOpen, key(cur.x, cur.y))
		inClosed[key(cur.x, cur.y)] = true
		if cur.x == tx && cur.y == ty {
			var out [][2]int32
			for n := cur; n != nil; n = n.parent {
				out = append(out, [2]int32{n.x, n.y})
			}
			for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
				out[i], out[j] = out[j], out[i]
			}
			return out
		}
		for _, d := range wyDirs {
			nx, ny := cur.x+d[0], cur.y+d[1]
			if g.at(nx, ny) == nil {
				continue
			}
			k := key(nx, ny)
			if inClosed[k] || inOpen[k] {
				continue
			}
			if !g.walkable(nx, ny) && !(allowGoal && nx == tx && ny == ty) {
				continue
			}
			n := &astar{x: nx, y: ny, g: cur.g + 1, h: wyAbs32(nx-tx) + wyAbs32(ny-ty), parent: cur}
			open = append(open, n)
			inOpen[k] = true
		}
	}
	return nil
}

// movePath — client getMovePath: A* to the target; a dead-end at an Event
// tile whose root is set retries against any sibling tile sharing the same
// event uid (multi-cell events).
func (g *wyGrid) movePath(sx, sy, tx, ty int32) [][2]int32 {
	p := g.findPath(sx, sy, tx, ty, true)
	if len(p) >= 2 {
		return p
	}
	c := g.at(tx, ty)
	if c == nil || g.resolveCell(c).typ != wyTileEvent || c.event == nil {
		return p
	}
	for k := range g.cells {
		ox, oy := k%g.w, k/g.w
		if ox == tx && oy == ty {
			continue
		}
		oc := g.cells[k]
		if oc.event == nil || oc.event.id != c.event.id {
			continue
		}
		if p2 := g.findPath(sx, sy, ox, oy, true); len(p2) >= 2 {
			return p2
		}
	}
	return p
}

// hasNextFloorTile — client nextIsUnlock sight rule: ANY tile in the
// resolved display grid carrying type NextFloor (view-opened exit).
func (g *wyGrid) hasNextFloorTile() bool {
	if g == nil {
		return false
	}
	for k := range g.cells {
		if g.resolveType(k%g.w, k/g.w) == wyTileNextFloor {
			return true
		}
	}
	return false
}

// trapCellAt — the trap event under the player's feet (client traps the
// player IN PLACE; doBreakTrap DEAL_EVENTs at the player position).
func (g *wyGrid) trapCellAt(px, py int32) *wyEventLite {
	if g == nil {
		return nil
	}
	if ev := g.resolveEvent(px, py); ev != nil && ev.evType == wyEvTrap {
		return ev
	}
	return nil
}

func wyGridDiag(g *wyGrid) string {
	if g == nil {
		return "grid=nil"
	}
	types := map[int32]int{}
	for k := range g.cells {
		types[g.resolveType(k%g.w, k/g.w)]++
	}
	return fmt.Sprintf("grid=%dx%d cells=%d types%v", g.w, g.h, len(g.cells), types)
}
