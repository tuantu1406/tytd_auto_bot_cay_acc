package gimap

// Stream layouts are the browser renderer's payload: the same live ownership
// the PNG renderers draw, but in normalized (0..1) coordinates so the page
// can place the GAME's own art (background + city sprites from the CDN,
// served through our art proxy) at any container size. Positions match the
// PNG geometry in shape and aspect; only the coordinate space differs. Text
// stays user-facing game Vietnamese; protocol details never appear here.

import (
	"fmt"
	"image/color"
	"math"
	"sort"
	"strconv"
)

// Stream map identifiers — the same "which" values the web layer uses.
const (
	StreamBonglai  = "bonglai"  // God Island (Bồng Lai)
	StreamThienma  = "thienma"  // God Island peak (Thiên Ma)
	StreamCuuu     = "cuuu"     // Holy Land (Cửu U)
	StreamDitich   = "ditich"   // Ball GVG (Thượng Cổ Di Tích)
	StreamThuongdo = "thuongdo" // Sky Trade (Tiên Vực Thương Đồ)
	StreamKunlun   = "kunlun"   // KunlunWar (崑崙崢覇)
	StreamBacMinh  = "bacminh"  // Đỉnh Bắc Minh (北冥之巅) — same engine, type 332
	StreamLuanHoi  = "luanhoi"  // Luân Hồi Bí Cảnh guild-war bracket (type 172)
	StreamSeaWarWar = "seawarwar" // Thương Minh Yêu Vực chiến trường (type 349, 234931 board)
)

// Art bundles the browser may load through the art proxy. Empty Art means
// the map has no CDN art and is drawn as a styled schematic.
const (
	ArtGodIsland  = "gi"
	ArtHolyLand   = "hl"
	giBgNormal    = "images/ImmortalIslandBg.jpg"
	giBgPeak      = "images/ImmortalIslandBg_peak.jpg"
	hlBg          = "images/bgItem/sdzf_bg1.jpg"
	maxGiCityIcon = 5
	maxHlCityIcon = 7
)

// StreamCity is one live city in normalized container coordinates (0..1 on
// both axes). Icon is a game-art path for the proxy ("" = draw with CSS);
// Color is the owner's hex ring color ("" = unowned). W/H is the client's
// own sprite size over the same box, so the page draws buildings at true
// relative scale (0/omitted where no per-building size exists).
type StreamCity struct {
	ID    uint64  `json:"id"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w,omitempty"`
	H     float64 `json:"h,omitempty"`
	Icon  string  `json:"icon,omitempty"`
	Owner string  `json:"owner,omitempty"`
	Color string  `json:"color,omitempty"`
	// CI is the union's palette slot — the same first-city-index the game
	// client derives in getUnionColor, so the browser draws each union's
	// territory with the same z_hexagon sprite the game uses. Nil = unowned.
	CI *int `json:"ci,omitempty"`
	// Name is the client's localized place name (LanguageWordDB).
	Name string `json:"name,omitempty"`
	// Lab overrides the schematic label the page paints inside a no-icon
	// marker (port numbers, ship glyphs; "" renders an empty dot). Maps
	// without icons ignore it and fall back to the id.
	Lab   string `json:"lab,omitempty"`
	Fight bool   `json:"fight,omitempty"`
	Info  string `json:"info,omitempty"`
}

// unionColorSlots replicates the client's getUnionColor: walking the cities
// in the client's own mapOutList order, a union's slot is the index of its
// first city; unknown unions fall back to slot 0 exactly like the client's
// "insert failed" path.
func unionColorSlots(cities []city, overlay map[string]cityState) map[uint64]int {
	slots := map[uint64]int{}
	for i, c := range cities {
		st := overlay[strconv.FormatUint(c.ID, 10)]
		if st.UnionID == 0 {
			continue
		}
		if _, seen := slots[st.UnionID]; !seen {
			slots[st.UnionID] = i
		}
	}
	return slots
}

// StreamMap is one frame of live map geometry for the game-art renderer.
// Aspect is the container's width/height ratio the coordinates assume;
// Links are city-id pairs referencing Cities (roads), absent where the game
// defines no connections.
type StreamMap struct {
	Which  string       `json:"which"`
	Art    string       `json:"art,omitempty"`
	Bg     string       `json:"bg,omitempty"`
	Aspect float64      `json:"aspect"`
	Cities []StreamCity `json:"cities"`
	Links  [][2]uint64  `json:"links,omitempty"`
	// Routes (Cửu U only): live marches between city centers. The browser
	// interpolates the marker along From→To between Start and Arrive (unix
	// milliseconds, the game server's clock).
	Routes []StreamRoute `json:"routes,omitempty"`
	// Ku carries KunlunWar's board-cell extras (which="kunlun" only): the
	// union-occupied cell tints, the live march ledger the /kunlun mini app
	// animates, and the battle phase countdown.
	Ku *KuFrame `json:"ku,omitempty"`
	// Tc carries the Di Tích room roster (which="ditich" only): every player
	// the PULLING event account sees inside its current room.
	Tc *TcFrame `json:"tc,omitempty"`
	// Lh carries the Luân Hồi Bí Cảnh bracket (which="luanhoi" only): rival
	// bases on the 3000×1800 battle strip + this account's attack counters.
	Lh *LhFrame `json:"lh,omitempty"`
	// Gi carries the God Island people feed (which="bonglai"/"thienma"):
	// live marches between cities, folded by the bot from the 1aa6 push +
	// every 1aa7 view answer. The browser interpolates the badge along
	// FC→TC city centres between T and AR (server ms) and asks
	// /events/mapview?w=gi on camera stop for the standing-people dots.
	Gi *GiFrame `json:"gi,omitempty"`
	// Sw carries the Thương Minh Yêu Vực war board (which="seawarwar" only):
	// rival union bases as houses on our own grid — the /seawarwar mini app
	// draws them schematic-style (no CDN art mined for this scene).
	Sw *SwFrame `json:"sw,omitempty"`
}

// GiFrame is the live people payload of the islands (kunlun KuFrame twin).
type GiFrame struct {
	Routes []GiRoute `json:"routes,omitempty"`
	// Cities carries the last REAL HP the server ever heard per cityId
	// (decimal key) — the 206819 minimap proto has no HP fields, so bars
	// ride this instead (owner 2026-10-19: "thiếu show hp của city").
	Cities map[string]GiCityHP `json:"cities,omitempty"`
}

// GiCityHP is one city's last-known standing HP (1aa7/1abe cityInfo rows).
// Dead marks the destroyed state the client draws as rubble.
type GiCityHP struct {
	HP   int32 `json:"hp"`
	Cur  int32 `json:"cur"`
	Dead bool  `json:"dead,omitempty"`
}

// GiRoute is one in-flight march between two city ids (the GI board has no
// tile grid for marches — the client draws routes between city sprites).
// O marks the march as the VIEWER's side (own union, decided server-side
// where the event accounts' unions are known) so the page paints it green.
type GiRoute struct {
	FC int32  `json:"fc"` // from cityId
	TC int32  `json:"tc"` // to cityId
	T  int64  `json:"t"`  // depart, server ms
	AR int64  `json:"ar"` // arrival, server ms
	N  string `json:"n,omitempty"`
	O  bool   `json:"o,omitempty"`
}

// TcFrame is the live half of the Di Tích mini app payload: the puller's
// per-player view of its current room. X/Y arrive from the game as arena
// pixels; we normalize through the client's own elliptic walk ring (centre
// 1125/900, semi-axes radius·0.55 / radius·0.40 — bgAutoStep's route), so a
// walker sits exactly on the ring the client walks. R==0 means the room's
// walk radius was unknown (cold config): every ball then carries the −1
// sentinel and the page degrades to a roster LIST in the room popup.
type TcFrame struct {
	Place int32    `json:"place"` // room the puller stands in
	Name  string   `json:"nm,omitempty"`
	R     float64  `json:"r,omitempty"`
	Balls []TcBall `json:"balls,omitempty"`
}

type TcBall struct {
	N  string  `json:"n"`            // in-game name
	C  int32   `json:"c"`            // camp id; page paints camps[c]
	HP string  `json:"hp,omitempty"` // raw HP string from the game
	D  bool    `json:"d,omitempty"`  // dead/reviving (bgRosterUser.alive)
	Rv int64   `json:"rv,omitempty"` // revive deadline, server ms (when D)
	X  float64 `json:"x"`            // normalized 0.05..0.95, −1 = unknown
	Y  float64 `json:"y"`
}

// KuFrame is the live half of the KunlunWar mini app payload; the static
// half (grid, sprites, layers) ships once via KunlunSceneJSON.
type KuFrame struct {
	// Cells are occupied board tiles: posIndex + union palette slot
	// (browser paints palette[slot % len(palette)] like the PNG tint).
	Cells []KuCell `json:"cells,omitempty"`
	// Routes are live marches between posIndex cells (client 219943 push
	// folded by the bot). K is the client line-art variant:
	// 1 enemy, 2 self, 3 own union, 4 heading home.
	Routes []KuRoute `json:"routes,omitempty"`
	Phase  *KuPhase  `json:"phase,omitempty"`
	// Mine lists the cells where the VIEWING leader's own event accounts
	// stand right now (posIndex). The /kunlun radial menu offers Khám phá /
	// Càn quét exactly there — both orders act on the army's current tile.
	Mine []int64 `json:"mine,omitempty"`
	// MySlots are palette slots (KuCell.S values) owned by the viewing
	// leader's unions — a cell tinted with one of them is "ô đã chiếm" of
	// our own side and offers Hành quân to reinforce.
	MySlots []int `json:"myslots,omitempty"`
}

type KuCell struct {
	P int64 `json:"p"` // posIndex = ty*61+tx
	// S is the union palette slot; −1 = the tile has no owning union in
	// this payload but IS fought over (the mini map flags fight on empty
	// tiles too — they carry no tint, only the browser's combat marker).
	S int `json:"s"`
	// F marks a tile with live combat on it (219919 per-tile haveFight) —
	// where OTHER players are fighting, block-level, not just cities.
	F bool `json:"f,omitempty"`
}

type KuRoute struct {
	FP int32  `json:"fp"` // from posIndex
	TP int32  `json:"tp"` // to posIndex
	T  int64  `json:"t"`  // depart, server ms
	AR int64  `json:"ar"` // arrival, server ms
	K  int8   `json:"k"`  // line kind (see KuFrame)
	S  int    `json:"s"`  // owner union palette slot (-1 = unknown)
	N  string `json:"n,omitempty"`
}

type KuPhase struct {
	S   string `json:"s"`   // SignUp/Prepare/Battle/Reward
	N   string `json:"n"`   // Vietnamese label
	End int64  `json:"end"` // window end, unix ms
}

type StreamRoute struct {
	From   uint64 `json:"from"`
	To     uint64 `json:"to"`
	Name   string `json:"name,omitempty"`
	Union  uint64 `json:"union,omitempty"`
	Color  string `json:"color,omitempty"`
	Start  int64  `json:"start"`
	Arrive int64  `json:"arrive"`
}

// Stream builds the game-art layout for one live minimap payload. which is
// one of the Stream* constants; payload is the raw server response for that
// activity (same bytes Render*/Layout* take).
func Stream(which string, payload []byte) (*StreamMap, error) {
	m, err := StreamOf(which, payload)
	if err != nil {
		return nil, err
	}
	m.Which = which // the page dispatches frames by this — never leave it empty
	return m, nil
}

// StreamOf is Stream without stamping Which, for callers that re-wrap the
// frame (e.g. HolyLandStream attaching the route snapshot).
func StreamOf(which string, payload []byte) (*StreamMap, error) {
	switch which {
	case StreamBonglai:
		return streamGodIsland(payload, false)
	case StreamThienma:
		return streamGodIsland(payload, true)
	case StreamCuuu:
		return streamHolyLand(payload, nil)
	case StreamDitich:
		return streamBallGVG(payload)
	case StreamThuongdo:
		return SkyTradeStream(payload)
	case StreamKunlun:
		return streamKunlun(payload)
	case StreamBacMinh:
		return streamBacMinh(payload)
	default:
		return nil, fmt.Errorf("unknown stream map %q", which)
	}
}

// worldBox accumulates axis-aligned rectangles (center + half-size) into a
// bounding box over the client's world coordinates.
type worldBox struct {
	seen                   bool
	minX, minY, maxX, maxY float64
}

func (b *worldBox) add(cx, cy, halfW, halfH float64) {
	if !b.seen {
		b.seen = true
		b.minX, b.minY, b.maxX, b.maxY = cx-halfW, cy-halfH, cx+halfW, cy+halfH
		return
	}
	b.minX = math.Min(b.minX, cx-halfW)
	b.maxX = math.Max(b.maxX, cx+halfW)
	b.minY = math.Min(b.minY, cy-halfH)
	b.maxY = math.Max(b.maxY, cy+halfH)
}

// giWorldBox measures the God Island (and peak) world rectangle: every tile
// diamond and city sprite from the client's mapOutList, padded on all sides
// by the largest half-sprite so the biggest building at any edge still fits.
func giWorldBox(m *parsedMap) (originX, originY, w, h float64) {
	box := &worldBox{}
	maxHalf := 0.0
	addTiles := func(tiles []tile) {
		for _, t := range tiles {
			box.add(t.X, t.Y, m.geom.TileWidth/2, m.geom.TileHeight/2)
		}
	}
	addTiles(m.base)
	addTiles(m.detail)
	for _, c := range m.cities {
		box.add(c.X, c.Y, c.Width/2, c.Height/2)
		maxHalf = math.Max(maxHalf, math.Max(c.Width, c.Height)/2)
	}
	if !box.seen {
		return 0, 0, 0, 0 // caller falls back to the synthetic grid extent
	}
	return box.minX - maxHalf, box.minY - maxHalf,
		(box.maxX - box.minX) + 2*maxHalf, (box.maxY - box.minY) + 2*maxHalf
}

func streamGodIsland(payload []byte, peak bool) (*StreamMap, error) {
	m, err := LoadMapConfig()
	if peak {
		m, err = LoadMapConfigPeak()
	}
	if err != nil {
		which := "map config"
		if peak {
			which = "peak map config"
		}
		return nil, fmt.Errorf("load %s: %w", which, err)
	}
	overlay, err := decodeCities(payload)
	if err != nil {
		return nil, err
	}
	// The client's own world rectangle, measured from its layout JSON: every
	// tile diamond and city sprite extent, padded by the largest half-sprite
	// so edge buildings stay inside. Normalizing on it (rather than the PNG's
	// synthetic grid extent) gives the browser the game's true proportions
	// and per-building sizes.
	originX, originY, logicalW, logicalH := giWorldBox(m)
	if logicalW <= 0 || logicalH <= 0 { // never on a real CDN config, but stay sane
		logicalW = float64(m.geom.Col)/2*(m.geom.TileWidth+100) + 50
		logicalH = float64(m.geom.Row)*m.geom.TileHeight + m.geom.TileHeight/2
		originX, originY = 0, 0
	}
	out := &StreamMap{
		Aspect: logicalW / logicalH,
		Cities: make([]StreamCity, 0, len(m.cities)),
	}
	out.Art = ArtGodIsland
	iconDir := "images/city/"
	if peak {
		out.Bg = giBgPeak
		iconDir = "images/city/peak/"
	} else {
		out.Bg = giBgNormal
	}
	slots := unionColorSlots(m.cities, overlay)
	for _, c := range m.cities {
		state := overlay[strconv.FormatUint(c.ID, 10)]
		icon := int(c.ID / 1000)
		if icon < 1 {
			icon = 1
		}
		if icon > maxGiCityIcon {
			icon = maxGiCityIcon
		}
		city := StreamCity{
			ID:    c.ID,
			X:     (c.X - originX) / logicalW,
			Y:     (c.Y - originY) / logicalH,
			W:     c.Width / logicalW,
			H:     c.Height / logicalH,
			Icon:  fmt.Sprintf("%s%d.png", iconDir, icon),
			Owner: state.Owner,
			Color: ownerHex(state.Owner),
			Name:  giCityName(c.ID),
			Fight: state.HaveFight,
		}
		if state.UnionID != 0 {
			ci := slots[state.UnionID] // missing slot reads as the client's 0 fallback
			city.CI = &ci
		}
		out.Cities = append(out.Cities, city)
	}
	sort.Slice(out.Cities, func(i, j int) bool { return out.Cities[i].ID < out.Cities[j].ID })
	return out, nil
}

// HolyLandStream is the Cửu U frame with marching routes attached: minimap
// payload plus the optional 209522 route-snapshot answer (nil blob = no
// route data, e.g. pulled from a non-resident account).
func HolyLandStream(minimap, routesBlob []byte) (*StreamMap, error) {
	m, err := streamHolyLand(minimap, routesBlob)
	if err != nil {
		return nil, err
	}
	m.Which = StreamCuuu // the page dispatches frames by this — never leave it empty
	return m, nil
}

func streamHolyLand(payload, routesBlob []byte) (*StreamMap, error) {
	m, err := loadHolyLandMap()
	if err != nil {
		return nil, err
	}
	overlay, err := DecodeHolyLand(payload)
	if err != nil {
		return nil, err
	}
	// Exact client world box (HolyLandMapModel.getMapSize, padded coords):
	// the browser's 5×2 background grid maps 1:1 onto these fractions.
	originX, originY := 0.0, 0.0
	logicalW, logicalH := hlWorldW, hlWorldH
	links := make([][2]uint64, 0, len(m.roads))
	for _, r := range m.roads {
		links = append(links, r.Conn)
	}
	out := &StreamMap{
		Aspect: logicalW / logicalH,
		Art:    ArtHolyLand,
		Bg:     hlBg,
		Cities: make([]StreamCity, 0, len(m.cities)),
		Links:  links,
	}
	unionSeq := hlUnionOrder(overlay)
	for _, c := range m.cities {
		state := overlay[strconv.FormatUint(c.ID, 10)]
		icon := int(c.ID / 1000)
		if icon < 1 {
			icon = 1
		}
		if icon > maxHlCityIcon {
			icon = maxHlCityIcon
		}
		color := ""
		var ci *int
		if state.Owner != "" {
			color = hlColorHex(unionSeq, state.UnionID)
			// CI = the union's slot in the first-occurrence order (−1 for a
			// union that never holds a main city). The /cuuu mini app needs
			// it to replay the client road rule EXACTLY — tint only when both
			// endpoints share one union, where two distinct stone-gray unions
			// must NOT light a road (raw color equality would say otherwise).
			idx := -1
			for i, s := range unionSeq {
				if s == state.UnionID {
					idx = i
					break
				}
			}
			ci = &idx
		}
		out.Cities = append(out.Cities, StreamCity{
			ID:    c.ID,
			X:     (c.X - originX) / logicalW,
			Y:     (c.Y - originY) / logicalH,
			W:     c.W / logicalW,
			H:     c.H / logicalH,
			Icon:  fmt.Sprintf("images/city/%d.png", icon),
			Owner: state.Owner,
			Color: color,
			CI:    ci,
			Name:  hlCityName(c.ID),
			Fight: state.HaveFight,
		})
	}
	sort.Slice(out.Cities, func(i, j int) bool { return out.Cities[i].ID < out.Cities[j].ID })
	known := make(map[uint64]bool, len(out.Cities))
	for _, c := range out.Cities {
		known[c.ID] = true
	}
	out.Routes = hlDecodeRoutes(routesBlob, unionSeq, known)
	return out, nil
}

func streamBallGVG(payload []byte) (*StreamMap, error) {
	places, err := DecodeBallGVGPlaces(payload)
	if err != nil {
		return nil, err
	}
	// Normalize through the same schematic transform the PNG uses, so the
	// browser's ring of slots matches the picture Telegram users see.
	g := ballGVGGeomFor(1400)
	out := &StreamMap{
		Aspect: float64(g.width) / float64(g.height),
		Cities: make([]StreamCity, 0, len(places)),
	}
	for i, place := range places {
		x, y, _ := g.slot(i)
		city := StreamCity{
			ID:   uint64(place.PlaceID),
			X:    float64(x) / float64(g.width),
			Y:    float64(y) / float64(g.height),
			Info: ballGVGMemberLabel(place.CampMembers),
		}
		if place.SeizeCampID > 0 {
			city.Owner = fmt.Sprintf("Phe %d", place.SeizeCampID)
			city.Color = campHex(place.SeizeCampID)
		}
		out.Cities = append(out.Cities, city)
	}
	return out, nil
}

// ownerHex is the ring color for one union name — the same hash color the
// PNG renderer paints, as an HTML-ready #rrggbb ("" for unowned).
func ownerHex(owner string) string {
	if owner == "" {
		return ""
	}
	c := colorForOwner(owner)
	return hexOf(c)
}

func campHex(campID int32) string {
	if c, ok := ballGVGCampColors[campID]; ok {
		return hexOf(c)
	}
	return ""
}

func hexOf(c color.RGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}
