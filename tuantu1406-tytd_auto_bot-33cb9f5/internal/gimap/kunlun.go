package gimap

// KunlunWar (崑崙崢覇) map — 61×61 isometric tile board. Static layout is
// embedded (kunlun_map.json, generated 2026-10-05 from the client package's
// json/mapOut.json: gid tiles, the 37 cities in the client's own mapOut
// order, and the real sprite layers Water/Floor/Block/Object/Cloud); live
// ownership comes from ONE proto.KunlunWarMiniMapInfoRespMsg (request
// 219919 answers the whole map: every city + every cell + union base rows).
//
// RenderKunlun is a SPRITE COMPOSITOR, not a schematic: terrain and city
// art are the client's own sprites (unpacked from RemoteKunlunWar.bin FGUI
// atlas rects into kunlun_sprites.png), placed with the client's exact
// anchor affine px = 6527 + 107·(tx−ty), py = 63·(tx+ty) (fit over the 3721
// base[] anchors, max error 0) and the same bottom-centre anchor rule the
// webmap verified for God Island (sprite bottom at tile bottom vertex).
// Depth order = bottom vertex ascending, so foreground tiles occlude city
// feet exactly like the game. Occupied cells are tinted union-colour over
// the ground layers, under decorations and cities.
//
// Union colors replay getUnionColor/indexOf over the client's mapOut CITY
// order: each union's slot = index of its first city, color = kuColorVec
// [slot % 12] — the same twelve rgb values the client ships (HolyLand's
// colorVec is byte-identical; reused).

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"sort"
	"strconv"
	"sync"

	"tytd_go/internal/proto"

	xdraw "golang.org/x/image/draw"
	pb "google.golang.org/protobuf/proto"
)

//go:embed kunlun_map.json
var kunlunMapJSON []byte

//go:embed kunlun_sprites.png
var kuSheetPNG []byte

//go:embed kunlun_sprites.json
var kuSheetManifest []byte

//go:embed kunlun_bg.jpg
var kuBgJPEG []byte

const (
	kuHalfW = 107.0 // tileWidth/2
	kuHalfH = 63.0  // tileHeight/2
)

// kuColorVec is the client's union palette (same twelve as hlColorVec).
var kuColorVec = []rgba{
	{R: 245, G: 85, B: 59, A: 255}, {R: 104, G: 133, B: 253, A: 255}, {R: 235, G: 114, B: 203, A: 255}, {R: 255, G: 244, B: 83, A: 255},
	{R: 231, G: 144, B: 89, A: 255}, {R: 164, G: 127, B: 246, A: 255}, {R: 45, G: 194, B: 253, A: 255}, {R: 0, G: 228, B: 209, A: 255},
	{R: 240, G: 216, B: 166, A: 255}, {R: 1, G: 160, B: 166, A: 255}, {R: 255, G: 182, B: 24, A: 255}, {R: 97, G: 218, B: 94, A: 255},
}

// kuCity is one of the 37 cities at its client mapOut anchor.
type kuCity struct {
	ID   int     `json:"id"`
	TX   float64 `json:"tx"`
	TY   float64 `json:"ty"`
	Img  string  `json:"img"`
	Flip int     `json:"flip"`
	W    int     `json:"w"`
	H    int     `json:"h"`
}

// kuEntry is one layer sprite from mapOut — decoded from the packed
// [tx, ty, "img", flipX, w, h] array form.
type kuEntry struct {
	TX   float64
	TY   float64
	Img  string
	Flip bool
	W, H int
}

func (e *kuEntry) UnmarshalJSON(b []byte) error {
	var a []any
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if len(a) < 6 {
		return fmt.Errorf("kunlun layer entry too short: %d", len(a))
	}
	f := func(v any) float64 { x, _ := v.(float64); return x }
	e.TX, e.TY = f(a[0]), f(a[1])
	e.Img, _ = a[2].(string)
	e.Flip = f(a[3]) != 0
	e.W, e.H = int(f(a[4])), int(f(a[5]))
	return nil
}

type kunlunMapFile struct {
	Col    int      `json:"col"`
	Row    int      `json:"row"`
	TileW  int      `json:"tileW"`
	TileH  int      `json:"tileH"`
	Tiles  [][]int  `json:"tiles"`
	Cities []kuCity `json:"cities"`
	// Anchor is the client base[] affine [originX, halfW, halfH]:
	// px = originX + halfW·(tx−ty), py = halfH·(tx+ty). Fit over all 3721
	// base anchors with max error 0.
	Anchor []float64 `json:"anchor"`
	Water  []kuEntry `json:"Water"`
	Floor  []kuEntry `json:"Floor"`
	Block  []kuEntry `json:"Block"`
	Object []kuEntry `json:"Object"`
	Cloud  []kuEntry `json:"Cloud"`

	// kuWorldBox memo (the layout is a process singleton — see kuMapOnce).
	// Recomputing ~5800 draw structs every 5 s SSE tick was audit finding #4.
	boxOnce sync.Once
	box     [4]float64

	// city-art dedup memo — see kuFloorEntries.
	cityKeysOnce sync.Once
	cityKeys     map[kuCityArtKey]bool
}

var (
	kuMapOnce sync.Once
	kuMap     *kunlunMapFile
	kuMapErr  error
)

func kunlunLayout() (*kunlunMapFile, error) {
	kuMapOnce.Do(func() {
		kuMap = &kunlunMapFile{}
		kuMapErr = json.Unmarshal(kunlunMapJSON, kuMap)
	})
	return kuMap, kuMapErr
}

// kuTileCenter maps tile coords to world px (origin shifted so min x = 0).
// It agrees with the base[] affine up to a 107 px shift, which the
// normalized frame/legend paths use; the sprite compositor uses
// kuAnchor directly.
func kuTileCenter(tx, ty float64, col int) (float64, float64) {
	return (tx-ty)*kuHalfW + float64(col-1)*kuHalfW, (tx + ty) * kuHalfH
}

// kuAnchor is the client's exact base[] affine for float tile coords.
func kuAnchor(mp *kunlunMapFile, tx, ty float64) (float64, float64) {
	if len(mp.Anchor) == 3 {
		return mp.Anchor[0] + mp.Anchor[1]*(tx-ty), mp.Anchor[2] * (tx + ty)
	}
	return kuTileCenter(tx, ty, mp.Col)
}

// kuOffX: the constant px shift between the client's base[] affine
// (kuAnchor space) and kuTileCenter space.
func kuOffX(mp *kunlunMapFile) float64 {
	if len(mp.Anchor) == 3 {
		return mp.Anchor[0] - float64(mp.Col-1)*kuHalfW
	}
	return 0
}

// kuWorldBox is the EXACT drawing box of the sprite compositor (RenderKunlun
// / RenderBacMinh), expressed in the kuTileCenter coordinates the
// static/stream layers normalize with — so the /kunlun mini app's browser
// bake lines up 1:1 with the sprite render. Cloud sprites are excluded (the
// compositor skips them). The box memo lives on mp (one layout singleton per
// board), the manifest load on pk (one sheet pack per board).
//
// Client ground truth (getPosByCoordinate = the diamond's TOP vertex; a cell
// spans [py, py+126] and CENTRES at py+kuHalfH). Two placement rules, straight
// from the client's init scripts:
//   - Ground plates (Water/Floor/Block, BlockItem tint, base slate): concentric
//     with the cell — centred on cellCenterY = py+kuHalfH (BlockItem does
//     o.y += tileHeight/2 before drawing).
//   - Raised plates (DecorateItem/CityItem): position.y = data.y − height/2,
//     anchor .5 → bottom edge exactly ON py, spanning [py−h, py].
func kuWorldBox(pk *kuSheetPack, mp *kunlunMapFile) (x0, y0, w, h float64) {
	mp.boxOnce.Do(func() { mp.box = kuWorldBoxCalc(pk, mp) })
	b := mp.box
	return b[0], b[1], b[2], b[3]
}

func kuWorldBoxCalc(pk *kuSheetPack, mp *kunlunMapFile) [4]float64 {
	pk.loadSheet()
	ground := append(kuFloorEntries(pk, mp, mp.Water), kuFloorEntries(pk, mp, mp.Floor)...)
	ground = append(ground, kuFloorEntries(pk, mp, mp.Block)...)
	deco := kuEntries(pk, mp, mp.Object)
	cities := kuEntries(pk, mp, cityEntries(mp))
	for i := range deco {
		deco[i].raised = true
	}
	for i := range cities {
		cities[i].raised = !pk.cityCentered
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	add := func(items []kuDraw) {
		for _, it := range items {
			minX = math.Min(minX, it.px-it.w/2)
			maxX = math.Max(maxX, it.px+it.w/2)
			top, bot := it.topBot()
			minY = math.Min(minY, top)
			maxY = math.Max(maxY, bot)
		}
	}
	add(ground)
	add(deco)
	add(cities)
	minX -= 4
	minY -= 4
	maxX += 4
	maxY += 4
	return [4]float64{minX - kuOffX(mp), minY, maxX - minX, maxY - minY}
}

// KunlunCityPoint is the posIndex of the block a city stands on — the Move
// target for web march/attack orders (the server alone validates the route).
func KunlunCityPoint(cityID uint64) (int32, bool) {
	mp, err := kunlunLayout()
	if err != nil {
		return 0, false
	}
	for _, c := range mp.Cities {
		if uint64(c.ID) == cityID {
			return int32(c.TY)*int32(mp.Col) + int32(c.TX), true
		}
	}
	return 0, false
}

// KuPassableGids are the tiles the client lets you stand on: event ground,
// city ground and bridges. Void (0) and obstacle (3) taps are rejected in
// the web map before an order is even sent; the server alone still
// validates the route itself (ret 19944 = unreachable).
func kuCellPassable(mp *kunlunMapFile, pos int64) bool {
	if pos < 0 || pos >= int64(mp.Col)*int64(mp.Row) {
		return false
	}
	switch mp.Tiles[pos/int64(mp.Col)][pos%int64(mp.Col)] {
	case 1, 2, 4:
		return true
	}
	return false
}

// KunlunCellPassable reports whether posIndex y*61+x is a marchable tile.
func KunlunCellPassable(pos int64) bool {
	mp, err := kunlunLayout()
	if err != nil {
		return false
	}
	return kuCellPassable(mp, pos)
}

// KunlunCityNameAt names the city standing on posIndex, if any.
func KunlunCityNameAt(pos int64) (string, bool) {
	mp, err := kunlunLayout()
	if err != nil {
		return "", false
	}
	names := kuCityNames()
	for _, c := range mp.Cities {
		if int64(c.TY)*int64(mp.Col)+int64(c.TX) == pos {
			if n := names[c.ID]; n != "" {
				return n, true
			}
			return fmt.Sprintf("Thành %d", c.ID), true
		}
	}
	return "", false
}

// kuUnionSlots assigns palette slots by first appearance in the client's
// city order, then by first appearance over the mini cellInfo rows.
func kuUnionSlots(mp *kunlunMapFile, rsp *proto.KunlunWarMiniMapInfoRespMsg) map[int64]int {
	slots := map[int64]int{}
	cityOwner := map[int]int64{}
	for _, c := range rsp.GetCityInfo() {
		cityOwner[int(c.GetCityId())] = c.GetUnionId()
	}
	for _, c := range mp.Cities {
		u := cityOwner[c.ID]
		if u == 0 {
			continue
		}
		if _, seen := slots[u]; !seen {
			slots[u] = len(slots)
		}
	}
	for _, cell := range rsp.GetCellInfo() {
		u := cell.GetUnionId()
		if u == 0 {
			continue
		}
		if _, seen := slots[u]; !seen {
			slots[u] = len(slots)
		}
	}
	return slots
}

// KuUnionSlot is the exported palette-slot lookup for live union ids — the
// /kunlun mini app colors marching routes by slot, exactly like the PNG
// tints occupied cells (kuUnionSlots order: first appearance in client city
// order, then mini cell rows). -1 = union not on this board.
func KuUnionSlot(miniPayload []byte, unionID int64) int {
	if unionID == 0 {
		return -1
	}
	rsp, err := DecodeKunlunMini(miniPayload)
	if err != nil {
		return -1
	}
	mp, err := kunlunLayout()
	if err != nil {
		return -1
	}
	if s, ok := kuUnionSlots(mp, rsp)[unionID]; ok {
		return s
	}
	return -1
}

func kuUnionColor(slots map[int64]int, u int64) (rgba, bool) {
	s, ok := slots[u]
	if !ok || u == 0 {
		return rgba{}, false
	}
	return kuColorVec[s%len(kuColorVec)], true
}

func kuColorHex(c rgba) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// kuOver alpha-composites src over dst at (x,y) within the rect.
func kuOver(dst *image.RGBA, x0, y0, x1, y1 int, c rgba) {
	if c.A == 0 {
		return
	}
	a := float64(c.A) / 255
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if x < 0 || y < 0 || x >= dst.Bounds().Dx() || y >= dst.Bounds().Dy() {
				continue
			}
			i := dst.PixOffset(x, y)
			d := dst.Pix[i : i+4 : i+4]
			d[0] = uint8(float64(c.R)*a + float64(d[0])*(1-a))
			d[1] = uint8(float64(c.G)*a + float64(d[1])*(1-a))
			d[2] = uint8(float64(c.B)*a + float64(d[2])*(1-a))
			d[3] = 255
		}
	}
}

// kuDiamond fills an iso diamond centred at (cx,cy).
func kuDiamond(dst *image.RGBA, cx, cy, hw, hh float64, c rgba, sc float64) {
	y0, y1 := int((cy-hh)*sc), int((cy+hh)*sc)
	for y := y0; y <= y1; y++ {
		fy := float64(y)/sc - cy
		hwY := hw * (1 - math.Abs(fy)/hh)
		if hwY <= 0 {
			continue
		}
		xa, xb := int((cx-hwY)*sc), int((cx+hwY)*sc)
		kuOver(dst, xa, y, xb, y+1, c)
	}
}

// kuDiamondLine outlines one iso cell in a solid colour — the game draws
// held cells with a crisp union border, a translucent fill alone reads as
// a stain on large boards.
func kuDiamondLine(dst *image.RGBA, cx, cy, hw, hh float64, c rgba) {
	vx := [4]float64{cx, cx + hw, cx, cx - hw}
	vy := [4]float64{cy - hh, cy, cy + hh, cy}
	for i := 0; i < 4; i++ {
		j := (i + 1) % 4
		kuSegLine(dst, vx[i], vy[i], vx[j], vy[j], c)
	}
}

func kuSegLine(dst *image.RGBA, x0, y0, x1, y1 float64, c rgba) {
	n := math.Max(math.Abs(x1-x0), math.Abs(y1-y0))
	if n < 1 {
		kuOver(dst, int(x0), int(y0), int(x0)+1, int(y0)+1, c)
		return
	}
	steps := int(math.Ceil(n))
	for s := 0; s <= steps; s++ {
		t := float64(s) / float64(steps)
		x, y := x0+(x1-x0)*t, y0+(y1-y0)*t
		kuOver(dst, int(math.Round(x)), int(math.Round(y)), int(math.Round(x))+1, int(math.Round(y))+1, c)
	}
}

// DecodeKunlunMini parses a 219919 answer.
func DecodeKunlunMini(payload []byte) (*proto.KunlunWarMiniMapInfoRespMsg, error) {
	var rsp proto.KunlunWarMiniMapInfoRespMsg
	if err := pb.Unmarshal(payload, &rsp); err != nil {
		return nil, fmt.Errorf("parse KunlunMiniMap: %w", err)
	}
	return &rsp, nil
}

// ---------------------------------------------------------------- sprite sheet

type kuSheetRect [4]int // x, y, w, h in sheet px

// kuSheetPack bundles EVERY mutable cache tied to ONE sprite sheet + its
// manifest, so the compositor serves several boards (Côn Lôn 61², Đỉnh Bắc
// Minh 119² — same engine, same opcodes, different embedded sheet) from one
// code path. The pxMu guards the decoded sheet and its flip copies. A
// sync.Once would pin the ≈19 MB (kunlun) / ≈54 MB (north) atlas for the
// whole process; the SERVER-RAM policy wants it ONLY during an explicit
// render (releasePixels), so the lazy flag is a mutex-guarded bool instead.
// Lock order across one pack's render caches is scaledMu → fadeMu → pxMu;
// never take them out of that order (packs are never held simultaneously).
type kuSheetPack struct {
	sheetPNG   []byte // embedded sheet bytes (served raw to the browser too)
	sheetManif []byte // embedded name → [x,y,w,h] manifest
	bgJPEG     []byte // embedded map background (never decoded — page bg only)

	// cityCentered flips the board's CityItem placement from kunlun's
	// raised plate (bottom edge on the tile anchor) to the client's item
	// rule used by the NORTH board: CENTRED on the cell, exactly like the
	// God-Island/Thiên Ma place plates the owner accepts as correct
	// ("thành vẽ lệch với chân đế… tham khảo thiên ma tiên vực", 2026-11-03).
	// Set on bmPackMain only — Côn Lôn stays byte-stable.
	cityCentered bool

	sheetOnce    sync.Once
	pxMu         sync.Mutex
	pixelsLoaded bool
	sheet        *image.RGBA
	sheetFlips   map[string]image.Image
	rects        map[string]kuSheetRect
	sheetBroken  bool

	fadeMu    sync.Mutex
	fadeCache map[string]image.Image

	scaledMu    sync.Mutex
	scaledCache map[string]*image.RGBA

	scratchMu sync.Mutex
	scratch   struct {
		img *image.RGBA
		buf bytes.Buffer
		enc png.Encoder
	}
}

// newKuSheetPack builds a pack with its caches in the same initial state the
// old package globals had (empty maps so release/reset never nil-derefs).
func newKuSheetPack(png, manifest, bg []byte) *kuSheetPack {
	return &kuSheetPack{
		sheetPNG:    png,
		sheetManif:  manifest,
		bgJPEG:      bg,
		fadeCache:   map[string]image.Image{},
		scaledCache: map[string]*image.RGBA{},
	}
}

// kuPackMain is Côn Lôn's sheet pack (kunlun_sprites.png embed). Đỉnh Bắc
// Minh's lives in bacminh.go as bmPackMain — same struct, its own caches.
var kuPackMain = newKuSheetPack(kuSheetPNG, kuSheetManifest, kuBgJPEG)

// loadSheet loads ONLY the rect manifest (a few tens of KB) — all the
// layout/box math and the SSE frames need. Decoding the atlas into RGBA is
// deferred to loadPixels, called only when sprites are actually blitted —
// since the 2026-10-06 SERVER-RAM policy that is ONLY the on-demand
// /kunlun_map & /bacminh_map Telegram photos (every web surface bakes
// client-side): on the user's 512 MB VPS the mini-app's map stream must not
// pay the decode just because kuWorldBox wanted sprite sizes.
//
// The bg JPEG is likewise deliberately never decoded: the compositor paints
// a flat night-sea fill instead of the 2022×1800 cloud photo (it mushed at
// board scale — the user's second "blur" complaint); the raw bytes are only
// ever served to the page's bg layer.
func (pk *kuSheetPack) loadSheet() {
	pk.sheetOnce.Do(func() {
		if err := json.Unmarshal(pk.sheetManif, &pk.rects); err != nil {
			pk.sheetBroken = true
		}
	})
}

// loadPixels decodes and returns the sprite sheet, or nil when the sheet is
// unusable — callers fall back to the flat renderer. Re-decodes after
// releasePixels (only an explicit render holds pixels between calls).
func (pk *kuSheetPack) loadPixels() *image.RGBA {
	pk.loadSheet()
	pk.pxMu.Lock()
	defer pk.pxMu.Unlock()
	return pk.pixelsLocked()
}

// pixelsLocked is the lazy decode; caller holds pk.pxMu.
func (pk *kuSheetPack) pixelsLocked() *image.RGBA {
	if pk.sheetBroken {
		return nil
	}
	if !pk.pixelsLoaded {
		im, err := png.Decode(bytes.NewReader(pk.sheetPNG))
		if err != nil {
			pk.sheetBroken = true
			return nil
		}
		rgb, ok := im.(*image.RGBA)
		if !ok {
			// image/png hands back *image.NRGBA for color-type-6 PNGs (PNG
			// alpha is by definition non-premultiplied), so this branch runs
			// on EVERY decode — it must not resample. Measured 2026-10-06:
			// CatmullRom "converting" 2048×2421 to the same size allocated
			// ≈190 MB of float windows on the user's 512 MB box. draw.Draw
			// does the NRGBA→premultiplied-RGBA conversion for the price of
			// the one destination buffer.
			rgb = image.NewRGBA(im.Bounds())
			draw.Draw(rgb, rgb.Bounds(), im, im.Bounds().Min, draw.Src)
		}
		pk.sheet = rgb
		pk.pixelsLoaded = true
	}
	return pk.sheet
}

// releasePixels drops every render-time pixel cache — decoded atlas, flips,
// fades and the scaled sprite cache — once the PNG is encoded. This is the
// SERVER-RAM policy: the Telegram photo is the only pixel consumer, and one
// photo must not leave its ~25 MB (kunlun) / ~60 MB (north) pinned on the
// 512 MB VPS. Takes the caches in the documented lock order.
func (pk *kuSheetPack) releasePixels() {
	pk.scaledMu.Lock()
	pk.fadeMu.Lock()
	pk.pxMu.Lock()
	pk.scaledCache = map[string]*image.RGBA{}
	pk.fadeCache = map[string]image.Image{}
	pk.sheet = nil
	pk.sheetFlips = nil
	pk.pixelsLoaded = false
	pk.pxMu.Unlock()
	pk.fadeMu.Unlock()
	pk.scaledMu.Unlock()
}

func (pk *kuSheetPack) spriteRect(name string) (kuSheetRect, bool) {
	pk.loadSheet()
	if pk.sheetBroken {
		return kuSheetRect{}, false
	}
	r, ok := pk.rects[name]
	if !ok || r[2] <= 0 || r[3] <= 0 {
		return kuSheetRect{}, false
	}
	return r, true
}

// spriteSrc returns the (possibly mirrored) source image for a name.
// It is a PIXEL consumer — it forces the full sheet decode by design.
func (pk *kuSheetPack) spriteSrc(name string, flip bool) (image.Image, image.Rectangle, bool) {
	r, ok := pk.spriteRect(name)
	if !ok {
		return nil, image.Rectangle{}, false
	}
	// One hold of pxMu over decode + flip-cache (the flip map used to be
	// written lock-free, racy against a second concurrent render).
	pk.pxMu.Lock()
	defer pk.pxMu.Unlock()
	px := pk.pixelsLocked()
	if px == nil {
		return nil, image.Rectangle{}, false
	}
	if !flip {
		return px, image.Rect(r[0], r[1], r[0]+r[2], r[1]+r[3]), true
	}
	if pk.sheetFlips == nil {
		pk.sheetFlips = map[string]image.Image{}
	}
	if f, ok := pk.sheetFlips[name]; ok {
		return f, f.Bounds(), true
	}
	sub := px.SubImage(image.Rect(r[0], r[1], r[0]+r[2], r[1]+r[3]))
	// ZERO-origin copy: callers take srcR from m.Bounds(), so both the writer
	// below and every reader agree (the old sub.Bounds()-sized copy relied on
	// PixOffset's Min-subtraction for BOTH ends — fragile by construction).
	m := image.NewRGBA(image.Rect(0, 0, sub.Bounds().Dx(), sub.Bounds().Dy()))
	// Byte-reversed row copies on Pix directly — the At/Set interface path
	// allocates a color value PER PIXEL (part of the 276 MB first-render
	// spike measured on the user's 512 MB box 2026-10-06).
	if s, ok := sub.(*image.RGBA); ok {
		sb := sub.Bounds()
		mw, mh := sb.Dx(), sb.Dy()
		// PixOffset yields the base of the sub-rect in whatever layout s uses
		// (SubImage of RGBA re-slices Pix to start AT Min; a raw RGBA keeps
		// Min-subtraction inside PixOffset) — adding Min's raw byte offset on
		// top double-counts and runs off the buffer.
		base := s.PixOffset(sb.Min.X, sb.Min.Y)
		for y := 0; y < mh; y++ {
			d := m.Pix[y*m.Stride : (y+1)*m.Stride]
			s0 := s.Pix[base+y*s.Stride:]
			for x := 0; x < mw; x++ {
				copy(d[x*4:x*4+4], s0[(mw-1-x)*4:(mw-1-x)*4+4])
			}
		}
	} else {
		sb := sub.Bounds()
		for y := 0; y < sb.Dy(); y++ {
			for x := 0; x < sb.Dx(); x++ {
				m.Set(x, y, sub.At(sb.Min.X+sb.Dx()-1-x, sb.Min.Y+y))
			}
		}
	}
	pk.sheetFlips[name] = m
	return m, m.Bounds(), true
}

// kuFade is the alpha multiplier for clutter decorations (mountain stones,
// trees) so the playable ground, occupied corridors, and city sprites read
// first — the user's "làm mờ bớt núi" on a live board.
const kuFade = 96 // ≈37 % opacity, in 255ths

// spriteFadeSrc is spriteSrc with every pixel's premultiplied RGBA
// scaled by kuFade. Cached per sprite (the Object layer reuses a few dozen
// atlases across its ~2000 entries), so the whole board costs one pass per
// unique sprite.
func (pk *kuSheetPack) spriteFadeSrc(name string, flip bool) (image.Image, image.Rectangle, bool) {
	key := name
	if flip {
		key += "|f"
	}
	pk.fadeMu.Lock()
	defer pk.fadeMu.Unlock()
	if m, ok := pk.fadeCache[key]; ok {
		return m, m.Bounds(), true
	}
	src, r, ok := pk.spriteSrc(name, flip)
	if !ok {
		return nil, image.Rectangle{}, false
	}
	m := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	// 8-bit premultiplied math on Pix (see the flip comment: the per-pixel
	// At() interface results were a big slice of the first-render spike).
	if s, ok := src.(*image.RGBA); ok {
		for y := 0; y < r.Dy(); y++ {
			sRow := s.Pix[(r.Min.Y+y)*s.Stride+r.Min.X*4:]
			dRow := m.Pix[y*m.Stride:]
			for x := 0; x < r.Dx(); x++ {
				sp := sRow[x*4 : x*4+4]
				dp := dRow[x*4 : x*4+4]
				dp[0] = uint8(uint32(sp[0]) * uint32(kuFade) / 255)
				dp[1] = uint8(uint32(sp[1]) * uint32(kuFade) / 255)
				dp[2] = uint8(uint32(sp[2]) * uint32(kuFade) / 255)
				dp[3] = uint8(uint32(sp[3]) * uint32(kuFade) / 255)
			}
		}
	} else {
		for y := 0; y < r.Dy(); y++ {
			for x := 0; x < r.Dx(); x++ {
				cr, cg, cb, ca := src.At(r.Min.X+x, r.Min.Y+y).RGBA()
				i := y*m.Stride + x*4
				m.Pix[i+0] = uint8(cr * uint32(kuFade) / 255 >> 8)
				m.Pix[i+1] = uint8(cg * uint32(kuFade) / 255 >> 8)
				m.Pix[i+2] = uint8(cb * uint32(kuFade) / 255 >> 8)
				m.Pix[i+3] = uint8(ca * uint32(kuFade) / 255 >> 8)
			}
		}
	}
	pk.fadeCache[key] = m
	return m, m.Bounds(), true
}

// ----------------------------------------------------------------- compositor

// scaledSprite returns the sprite pre-scaled to w×h (its destination
// rect's size, bleed included), caching per (name, flip, fade, size).
// Scaling EVERY DRAW with CatmullRom was the server's OOM: the whole board
// is ~6000 draws and the resampler allocates float intermediates per call —
// measured live 2026-10-06 at ≈800 MB TotalAlloc / 313 MB peak RSS for ONE
// render on the user's 512 MB VPS. The board reuses a few dozen atlases at
// a handful of sizes (sx-rounding gives each ≤3 size variants), so the cache
// shrinks the resampler calls per render to the unique few hundred and pins
// only ~1 MB of tiny images. Compositing onto the board is then plain
// draw.Over, pixel-identical to CatmullRom's own Over pass into a zeroed
// scratch (Src here == the old Over-onto-empty-dst math).
func (pk *kuSheetPack) scaledSprite(it kuDraw, w, h int) *image.RGBA {
	key := it.src
	if it.flip {
		key += "|f"
	}
	if it.fade {
		key += "|d"
	}
	key += "|" + strconv.Itoa(w) + "x" + strconv.Itoa(h)
	pk.scaledMu.Lock()
	defer pk.scaledMu.Unlock()
	if m, ok := pk.scaledCache[key]; ok {
		return m
	}
	var im image.Image
	var srcR image.Rectangle
	var ok bool
	if it.fade {
		im, srcR, ok = pk.spriteFadeSrc(it.src, it.flip)
	} else {
		im, srcR, ok = pk.spriteSrc(it.src, it.flip)
	}
	if !ok {
		return nil
	}
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	// Server boards always SHRINK sprites (webMapWidth 1400 over a 13k-px
	// world ⇒ scale ≈0.1), where a plain integer box average of the covered
	// source pixels matches CatmullRom's look at these ratios — without its
	// ~1 MB of float windows PER call (measured: 290 MB TotalAlloc for the
	// board even with size caching; the resampler only stays for genuine
	// upscales, which don't happen at the fixed widths).
	if w < srcR.Dx() || h < srcR.Dy() {
		kuBoxDown(m, im, srcR)
	} else {
		xdraw.CatmullRom.Scale(m, m.Bounds(), im, srcR, xdraw.Src, nil)
	}
	pk.scaledCache[key] = m
	return m
}

// kuBoxDown integer box-filters src[srcR] into dst (dst sized by its own
// Bounds). Works for any downscale; per-pixel cost is the covered source
// area only, zero scratch.
func kuBoxDown(dst *image.RGBA, src image.Image, srcR image.Rectangle) {
	sw, sh := srcR.Dx(), srcR.Dy()
	db := dst.Bounds()
	w, h := db.Dx(), db.Dy()
	// Compositor sources are always *image.RGBA (decoded sheet, or the
	// zero-origin flip/fade copies). Reading Pix keeps the one-time scaled
	// cache build allocation-free: src.At() returned a color value per pixel
	// through the interface and dominated the cold-render spike (see
	// kuScaledSprite). *257 keeps the sums in At's 16-bit scale so both
	// paths produce identical bytes.
	if s, ok := src.(*image.RGBA); ok {
		for y := 0; y < h; y++ {
			y0 := y * sh / h
			y1 := (y + 1) * sh / h
			if y1 <= y0 {
				y1 = y0 + 1
			}
			for x := 0; x < w; x++ {
				x0 := x * sw / w
				x1 := (x + 1) * sw / w
				if x1 <= x0 {
					x1 = x0 + 1
				}
				var r, g, b, a, n uint64
				for sy := y0; sy < y1; sy++ {
					row := s.Pix[(srcR.Min.Y+sy)*s.Stride+(srcR.Min.X+x0)*4:]
					for sx := 0; sx < x1-x0; sx++ {
						p := row[sx*4 : sx*4+4]
						r += uint64(p[0]) * 257
						g += uint64(p[1]) * 257
						b += uint64(p[2]) * 257
						a += uint64(p[3]) * 257
						n++
					}
				}
				i := y*dst.Stride + x*4
				dst.Pix[i+0] = uint8(r / n >> 8)
				dst.Pix[i+1] = uint8(g / n >> 8)
				dst.Pix[i+2] = uint8(b / n >> 8)
				dst.Pix[i+3] = uint8(a / n >> 8)
			}
		}
		return
	}
	for y := 0; y < h; y++ {
		y0 := y * sh / h
		y1 := (y + 1) * sh / h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < w; x++ {
			x0 := x * sw / w
			x1 := (x + 1) * sw / w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(srcR.Min.X+sx, srcR.Min.Y+sy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					b += uint64(cb)
					a += uint64(ca)
					n++
				}
			}
			i := y*dst.Stride + x*4
			dst.Pix[i+0] = uint8(r / n >> 8)
			dst.Pix[i+1] = uint8(g / n >> 8)
			dst.Pix[i+2] = uint8(b / n >> 8)
			dst.Pix[i+3] = uint8(a / n >> 8)
		}
	}
}

type kuDraw struct {
	px, py float64 // getPosByCoordinate anchor = the diamond's CENTER-X / TOP-Y (NOT centre)
	w, h   float64 // sprite size in world px
	src    string
	flip   bool
	fade   bool // draw at reduced alpha (mountain/tree clutter)
	raised bool // decor/city plate: BOTTOM edge on the anchor py, sprite spans [py−h, py] (DecorateItem/CityItem). Ground plates are false.
}

// cellCenterY is the ground cell CENTRE in the base affine: the client raises
// every ground/ownership/route element by a half tile from the anchor (BlockItem
// o.y += tileHeight/2, addRoute endpoint +th/2). getPosByCoordinate's py is the
// diamond TOP vertex, so the diamond spans [py, py+th] and centres at py+kuHalfH.
func (d kuDraw) cellCenterY() float64 { return d.py + kuHalfH }

// topBot returns the sprite's world-y [top,bottom] for the client's placement:
// ground plates centre on the cell (cellCenterY); raised plates hang from the
// anchor with their bottom edge exactly on py.
func (d kuDraw) topBot() (float64, float64) {
	if d.raised {
		return d.py - d.h, d.py
	}
	cy := d.cellCenterY()
	return cy - d.h/2, cy + d.h/2
}

// kuEntries converts one layer to draw items with sprite sizes resolved
// against the pack's rect manifest (layout/box math only — no pixels).
func kuEntries(pk *kuSheetPack, mp *kunlunMapFile, list []kuEntry) []kuDraw {
	out := make([]kuDraw, 0, len(list))
	for _, e := range list {
		w, h := float64(e.W), float64(e.H)
		if r, ok := pk.spriteRect(e.Img); ok {
			if w == 0 {
				w = float64(r[2])
			}
			if h == 0 {
				h = float64(r[3])
			}
		}
		if w <= 0 || h <= 0 {
			continue // unknown sprite (e.g. broken-variant name)
		}
		px, py := kuAnchor(mp, e.TX, e.TY)
		out = append(out, kuDraw{px: px, py: py, w: w, h: h, src: e.Img, flip: e.Flip})
	}
	return out
}

// ---------------------------------------------------------------- city-art dedup
//
// The north mapOut double-books its cầu bridges: the City table carries the
// 16 bridge-head cities (ids 7001–7016) whose img is the bridge art itself
// (decoration141/142/145/146), AND the 141/142 copies ALSO sit in the Floor
// layer at the same fractional tile with the same flip. Both rows describe
// ONE bridge: with the north board's cell-centred CityItem placement the two
// copies land on exactly the same pixels (the client simply overdraws), so
// the City row alone is kept. Blitting both copies through DIFFERENT
// placement rules (raised plate vs ground) ghosted every stair near the map
// centre (user's "duplicate mấy cái cầu gần giữa map", 2026-11-03). Côn Lôn
// has zero city↔Floor overlap, so the filter is a no-op there.

type kuCityArtKey struct {
	img  string
	flip bool
	qx   int64
	qy   int64
}

// q10 quantises a fractional tile coord to 0.1-tile buckets; adjacent
// buckets are also probed (see kuIsCityPlateArt) so a city/layer pair that
// straddles a bucket edge (mapOut carries both rounded and unrounded copies
// of the same coord, differing by ~0.005) still matches.
func q10(v float64) int64 { return int64(math.Round(v * 10)) }

func (mp *kunlunMapFile) cityArtKeys() map[kuCityArtKey]bool {
	mp.cityKeysOnce.Do(func() {
		mp.cityKeys = make(map[kuCityArtKey]bool, len(mp.Cities))
		for _, c := range mp.Cities {
			mp.cityKeys[kuCityArtKey{c.Img, c.Flip != 0, q10(c.TX), q10(c.TY)}] = true
		}
	})
	return mp.cityKeys
}

func kuIsCityPlateArt(keys map[kuCityArtKey]bool, e kuEntry) bool {
	qx, qy := q10(e.TX), q10(e.TY)
	for dx := int64(-1); dx <= 1; dx++ {
		for dy := int64(-1); dy <= 1; dy++ {
			if keys[kuCityArtKey{e.Img, e.Flip, qx + dx, qy + dy}] {
				return true
			}
		}
	}
	return false
}

// kuFloorEntries is kuEntries for the GROUND layers (Water/Floor/Block) with
// entries that duplicate a City-table plate dropped (see the dedup note
// above). Object and cityEntries stay on plain kuEntries.
func kuFloorEntries(pk *kuSheetPack, mp *kunlunMapFile, list []kuEntry) []kuDraw {
	keys := mp.cityArtKeys()
	skip := 0
	for _, e := range list {
		if kuIsCityPlateArt(keys, e) {
			skip++
		}
	}
	if skip == 0 {
		return kuEntries(pk, mp, list)
	}
	out := make([]kuEntry, 0, len(list)-skip)
	for _, e := range list {
		if !kuIsCityPlateArt(keys, e) {
			out = append(out, e)
		}
	}
	return kuEntries(pk, mp, out)
}

// RenderKunlun draws the whole-map ownership snapshot with the client's own
// sprites: terrain layers → union tint of occupied cells → decorations →
// city sprites with name plates. width = canvas px. Falls back to the flat
// schematic board if the sprite sheet cannot be decoded.
func RenderKunlun(payload []byte, width int) ([]byte, error) {
	return renderKunlun(payload, width, true)
}

// RenderKunlunBoard is the WEB background variant: the sprite board with
// faded decor, crisp occupied cells, and small city names — but WITHOUT the
// owner tags or the legend strip. Skipping the legend keeps the PNG's aspect
// exactly the world box (kuWorldBox), so the browser's 0..1 marker
// coordinates land on the painted board pixel-for-pixel.
func RenderKunlunBoard(payload []byte, width int) ([]byte, error) {
	return renderKunlun(payload, width, false)
}

// kuScratch (per pack) holds renderWar's two big allocations — the full
// canvas (1400×~900 RGBA ≈ 5 MB) and the PNG encoder's flate state (≈1.5 MB)
// — WITHIN one render. Since the 2026-10-06 memory audit it is emptied at
// the end of every render (web surfaces never render at all anymore; only
// the rare /kunlun_map or /bacminh_map pays the allocation, and nothing
// stays pinned between commands). The lock is TryLock'd: a concurrent render
// simply allocates its own instead of blocking.

func renderKunlun(payload []byte, width int, annot bool) ([]byte, error) {
	rsp, err := DecodeKunlunMini(payload)
	if err != nil {
		return nil, err
	}
	mp, err := kunlunLayout()
	if err != nil {
		return nil, err
	}
	return renderWar(kuPackMain, mp, rsp, width, annot)
}

// renderWar is the sprite compositor shared by every KunlunWar-engine board
// (Côn Lôn via kuPackMain, Đỉnh Bắc Minh via bmPackMain): layout + live
// minimap answer + that board's sheet pack in, PNG out. Behaviour is exactly
// the former renderKunlun body — only the caches moved onto pk.
func renderWar(pk *kuSheetPack, mp *kunlunMapFile, rsp *proto.KunlunWarMiniMapInfoRespMsg, width int, annot bool) ([]byte, error) {
	if width <= 0 {
		width = 1400
	}
	if pk.loadPixels() == nil {
		return renderKunlunFlat(mp, rsp, width, annot)
	}

	ground := append(kuFloorEntries(pk, mp, mp.Water), kuFloorEntries(pk, mp, mp.Floor)...)
	ground = append(ground, kuFloorEntries(pk, mp, mp.Block)...)
	deco := kuEntries(pk, mp, mp.Object) // mountain stones/trees: faded behind everything
	for i := range deco {
		deco[i].fade = true
	}
	// City PLATES were never actually blitted here (the loop below only drew
	// labels) — merged into the deco depth pass so castles paint like the
	// game client does. Decor stays RAISED (bottom edge on the tile anchor,
	// spanning [py−h, py]); city plates follow the board's rule: raised for
	// Côn Lôn, cell-CENTRED for the north board (pk.cityCentered — the same
	// placement the GI/Thiên Ma place plates use; "thành vẽ lệch với chân
	// đế", 2026-11-03). Ground plates (blit below) are centred on the cell.
	// See kuWorldBox / kuDraw.topBot.
	cities := kuEntries(pk, mp, cityEntries(mp))
	for i := range cities {
		cities[i].raised = !pk.cityCentered
	}
	deco = append(deco, cities...)
	// mp.Cloud (thousands of mist sprites spread over the WHOLE board) is a
	// scene fog effect that reduces to a milky haze at any downscale — skip.

	// Same box the browser layers normalize by (kuWorldBox).
	bx, by, bw, bh := kuWorldBox(pk, mp)
	offX := kuOffX(mp)
	minX, minY := bx+offX, by
	maxX, maxY := bx+offX+bw, by+bh

	legendH := 0
	if annot {
		legendH = 26 + 22*min(12, max(1, len(kuUnionSlots(mp, rsp))))
	}
	scale := float64(width) / (maxX - minX)
	height := int((maxY-minY)*scale) + legendH
	// Canvas from the scratch when free (first paint below is an opaque
	// full-canvas kuOver, so a dirty scratch is fine), else a fresh one.
	own := pk.scratchMu.TryLock()
	if own && pk.scratch.img != nil {
		if b := pk.scratch.img.Bounds(); b.Dx() != width || b.Dy() != height {
			pk.scratch.img = nil
		}
	}
	// (annot/web heights differ — only reuse exact fits)
	if own && pk.scratch.img == nil {
		pk.scratch.img = image.NewRGBA(image.Rect(0, 0, width, height))
	}
	img := pk.scratch.img
	if !own {
		img = image.NewRGBA(image.Rect(0, 0, width, height))
	}
	// Flat dark backdrop: the client's bmzd_mapbg sky photo is a 2022×1800
	// CLOUD image — scaled behind a 13k-wide world it turns to mush and was
	// the second "blur" the user saw. A night-sea fill keeps contrast.
	kuOver(img, 0, 0, width, height, rgba{R: 10, G: 14, B: 24, A: 255})

	sx := func(v float64) int { return int(math.Round((v - minX) * scale)) }
	sy := func(v float64) int { return int(math.Round((v - minY) * scale)) }

	blit := func(items []kuDraw) {
		sort.SliceStable(items, func(i, j int) bool {
			// Client depth = zIndex = data.y + data.height/2 (the sprite's own
			// centre Y); ties break left-to-right.
			ki, kj := items[i].py+items[i].h/2, items[j].py+items[j].h/2
			if ki != kj {
				return ki < kj
			}
			return items[i].px < items[j].px
		})
		for _, it := range items {
			dw := int(math.Round(it.w * scale))
			dh := int(math.Round(it.h * scale))
			if dw < 1 || dh < 1 {
				continue
			}
			// Client placement: ground plates centred on the cell
			// (py+kuHalfH); raised plates bottom-on the anchor [py−h, py].
			top, bot := it.topBot()
			// −1 px bleed on all sides: scale rounding leaves hairline seams
			// between adjacent ground plates, the client's atlases don't.
			dr := image.Rect(sx(it.px-it.w/2)-1, sy(top)-1,
				sx(it.px+it.w/2)+1, sy(bot)+1)
			scaled := pk.scaledSprite(it, dr.Dx(), dr.Dy())
			if scaled == nil {
				continue
			}
			draw.Draw(img, dr, scaled, image.Point{}, draw.Over)
		}
	}
	// Solid board base: the client paints ground plates OVER a backdrop
	// that supplies the stone body; without it the plates read as floating
	// decorations on black. Flat dark slate per passable tile.
	base := rgba{R: 26, G: 32, B: 42, A: 255}
	for ty := 0; ty < mp.Row; ty++ {
		for tx := 0; tx < mp.Col; tx++ {
			if g := mp.Tiles[ty][tx]; g != 1 && g != 2 && g != 4 {
				continue
			}
			px2, py2 := kuAnchor(mp, float64(tx), float64(ty))
			// Cell centre = tile anchor (diamond top vertex) + half a tile
			// DOWN (see kuWorldBox / kuDraw.cellCenterY).
			kuDiamond(img, float64(sx(px2)), float64(sy(py2))+kuHalfH*scale, kuHalfW*scale, kuHalfH*scale, base, 1)
		}
	}
	blit(ground)

	// Union tint of occupied cells (ground painted, decorations not).
	cellOwner := map[int]int64{}
	for _, c := range rsp.GetCellInfo() {
		if c.GetUnionId() != 0 {
			cellOwner[int(c.GetIndex())] = c.GetUnionId()
		}
	}
	slots := kuUnionSlots(mp, rsp)
	occupied := make([]int, 0, len(cellOwner))
	for pos := range cellOwner {
		occupied = append(occupied, pos)
	}
	sort.Ints(occupied) // deterministic pixel order (maps iterate randomly)
	for _, pos := range occupied {
		u := cellOwner[pos]
		tx := float64(pos % mp.Col)
		ty := float64(pos / mp.Col)
		px, py := kuAnchor(mp, tx, ty)
		if uc, ok := kuUnionColor(slots, u); ok {
			// Stronger fill + crisp border: the game outlines every held
			// cell (the red/cyan corridor grids in screenshots), and A=90
			// alone vanished against the faded decoration.
			edge := uc
			edge.A = 235
			uc.A = 150
			kuDiamond(img, float64(sx(px)), float64(sy(py))+kuHalfH*scale, kuHalfW*scale, kuHalfH*scale, uc, 1)
			kuDiamondLine(img, float64(sx(px)), float64(sy(py))+kuHalfH*scale, kuHalfW*scale, kuHalfH*scale, edge)
		}
	}

	blit(deco)

	// Cities on top: sprite + name plate + owner tag + fight dot.
	cityOwner := map[int]int64{}
	cityFight := map[int]bool{}
	for _, c := range rsp.GetCityInfo() {
		cityOwner[int(c.GetCityId())] = c.GetUnionId()
		cityFight[int(c.GetCityId())] = c.GetHaveFight()
	}
	names := kuCityNames()
	ub := map[int64]string{}
	for _, u := range rsp.GetBaseData() {
		ub[u.GetUnionId()] = u.GetName()
	}
	paint, ferr := newPainterFromFont(img)
	for _, c := range mp.Cities {
		px, py := kuAnchor(mp, c.TX, c.TY)
		// Plate bottom the labels/marker must hang from: the tile anchor
		// for kunlun's raised plates; the cell-centred plate's bottom edge
		// (anchor + hh + h/2) on the north board.
		bottom := py
		if pk.cityCentered {
			bottom = py + kuHalfH + float64(c.H)/2
		}
		if cityFight[c.ID] {
			// Marker just above the plate's bottom edge.
			kuDiamond(img, float64(sx(px)), float64(sy(bottom))-6, 5, 5, rgba{R: 255, G: 60, B: 40, A: 255}, 1)
		}
		if ferr != nil || paint == nil {
			continue
		}
		// City names are baked into BOTH renders now (small, under the
		// plate): the web board is a PNG, and the user asked for names on
		// it. Owner tags + legend stay on the full /kunlun_map variant.
		label := names[c.ID]
		if label == "" {
			label = fmt.Sprintf("%d", c.ID)
		}
		small := max(8, int(11*scale))
		nameY := sy(bottom) + 2 + small // under the plate's bottom edge
		_ = paint.text(sx(px), nameY, label, small, rgba{R: 235, G: 235, B: 235, A: 255}, true)
		if u := cityOwner[c.ID]; u != 0 && annot {
			if uc, ok := kuUnionColor(slots, u); ok {
				tag := ub[u]
				if tag == "" {
					tag = fmt.Sprintf("U%d", u%100000)
				}
				_ = paint.text(sx(px), nameY+small+4, "["+tag+"]", small, uc, true)
			}
		}
	}

	// Legend: union name (baseData) + color chip, sorted by slot.
	type lu struct {
		u int64
		s int
	}
	var lus []lu
	for u, s := range slots {
		lus = append(lus, lu{u, s})
	}
	sort.Slice(lus, func(i, j int) bool { return lus[i].s < lus[j].s })
	if annot && ferr == nil && paint != nil {
		y := int((maxY-minY)*scale) + 20
		for _, l := range lus {
			c, _ := kuUnionColor(slots, l.u)
			name := ub[l.u]
			if name == "" {
				name = fmt.Sprintf("union %d", l.u)
			}
			kuOver(img, 10, y-11, 24, y+1, c)
			_ = paint.text(30, y, name, 14, rgba{R: 235, G: 235, B: 235, A: 255}, false)
			y += 22
			if y > height-8 {
				break
			}
		}
	}
	// Encode into the scratch buffer and COPY the result out — the caller
	// owns the returned bytes outright (no live-PNG cache exists anymore).
	var out []byte
	var perr error
	if own {
		pk.scratch.buf.Reset()
		perr = pk.scratch.enc.Encode(&pk.scratch.buf, img)
		if perr == nil {
			out = make([]byte, pk.scratch.buf.Len())
			copy(out, pk.scratch.buf.Bytes())
		}
		// Release the scratch canvas too: web surfaces never render
		// anymore, so cross-render reuse only meant ~5 MB pinned between
		// the rare /kunlun_map commands (SERVER-RAM policy, audit #1).
		pk.scratch.img = nil
		pk.scratch.buf.Reset()
		pk.scratchMu.Unlock()
	} else {
		var buf bytes.Buffer
		perr = png.Encode(&buf, img)
		out = buf.Bytes()
	}
	// The encoded PNG is done with every pixel cache — drop them all so one
	// Telegram photo leaves nothing pinned (releasePixels).
	pk.releasePixels()
	if perr != nil {
		return nil, perr
	}
	return out, nil
}

func cityEntries(mp *kunlunMapFile) []kuEntry {
	out := make([]kuEntry, 0, len(mp.Cities))
	for _, c := range mp.Cities {
		out = append(out, kuEntry{TX: c.TX, TY: c.TY, Img: c.Img, Flip: bool(c.Flip != 0), W: c.W, H: c.H})
	}
	return out
}

func kuCityNames() map[int]string {
	t := loadNames()
	out := map[int]string{}
	for k, v := range t.KUCities {
		if n, err := strconv.Atoi(k); err == nil {
			out[n] = v
		}
	}
	return out
}

// renderKunlunFlat is the schematic fallback: passable tiles tinted by
// owner, city diamonds with ids, fight markers, legend. annot=false drops
// the baked labels + legend strip (web background mode — see RenderKunlunBoard).
func renderKunlunFlat(mp *kunlunMapFile, rsp *proto.KunlunWarMiniMapInfoRespMsg, width int, annot bool) ([]byte, error) {
	spanW := float64(mp.Col+mp.Row-2)*kuHalfW + 2*kuHalfW
	spanH := float64(mp.Col+mp.Row-2)*kuHalfH + 2*kuHalfH
	legendH := 0
	if annot {
		legendH = 26 + 22*min(12, max(1, len(kuUnionSlots(mp, rsp))))
	}
	scale := float64(width) / spanW
	height := int(spanH*scale) + legendH
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	kuOver(img, 0, 0, width, height, rgba{R: 14, G: 18, B: 24, A: 255})

	cellOwner := map[int]int64{}
	for _, c := range rsp.GetCellInfo() {
		cellOwner[int(c.GetIndex())] = c.GetUnionId()
	}
	slots := kuUnionSlots(mp, rsp)
	neutral := rgba{R: 58, G: 66, B: 80, A: 255}
	cityTile := rgba{R: 90, G: 102, B: 120, A: 255}
	px := func(tx, ty float64) (int, int) {
		x, y := kuTileCenter(tx, ty, mp.Col)
		return int(x * scale), int(y * scale)
	}
	for ty := 0; ty < mp.Row; ty++ {
		for tx := 0; tx < mp.Col; tx++ {
			gid := mp.Tiles[ty][tx]
			if gid != 1 && gid != 2 && gid != 4 {
				continue
			}
			col, row := px(float64(tx), float64(ty))
			c := neutral
			if gid == 2 {
				c = cityTile
			}
			if u := cellOwner[ty*mp.Col+tx]; u != 0 {
				if uc, ok := kuUnionColor(slots, u); ok {
					c = uc
					c.A = 200
				}
			}
			kuDiamond(img, float64(col), float64(row), kuHalfW*scale, kuHalfH*scale, c, 1)
		}
	}
	paint, ferr := newPainterFromFont(img)
	cityFight := map[int]bool{}
	cityOwner := map[int]int64{}
	for _, c := range rsp.GetCityInfo() {
		cityOwner[int(c.GetCityId())] = c.GetUnionId()
		cityFight[int(c.GetCityId())] = c.GetHaveFight()
	}
	for _, c := range mp.Cities {
		col, row := px(c.TX, c.TY)
		oc := rgba{R: 20, G: 24, B: 30, A: 230}
		if u := cityOwner[c.ID]; u != 0 {
			if uc, ok := kuUnionColor(slots, u); ok {
				oc = uc
			}
		}
		kuDiamond(img, float64(col), float64(row), kuHalfW*1.7*scale, kuHalfH*1.7*scale, oc, 1)
		if cityFight[c.ID] {
			kuDiamond(img, float64(col), float64(row)-kuHalfH*2.4*scale, 5, 5, rgba{R: 255, G: 60, B: 40, A: 255}, 1)
		}
		if annot && ferr == nil && paint != nil {
			_ = paint.text(col, row+4, fmt.Sprintf("%d", c.ID), max(9, int(12*scale)), rgba{R: 255, G: 255, B: 255, A: 255}, true)
		}
	}
	names := map[int64]string{}
	for _, u := range rsp.GetBaseData() {
		names[u.GetUnionId()] = u.GetName()
	}
	type lu struct {
		u int64
		s int
	}
	var lus []lu
	for u, s := range slots {
		lus = append(lus, lu{u, s})
	}
	sort.Slice(lus, func(i, j int) bool { return lus[i].s < lus[j].s })
	if annot && ferr == nil && paint != nil {
		y := int(spanH*scale) + 20
		for _, l := range lus {
			c, _ := kuUnionColor(slots, l.u)
			name := names[l.u]
			if name == "" {
				name = fmt.Sprintf("union %d", l.u)
			}
			kuOver(img, 10, y-11, 24, y+1, c)
			_ = paint.text(30, y, name, 14, rgba{R: 235, G: 235, B: 235, A: 255}, false)
			y += 22
			if y > height-8 {
				break
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func newPainterFromFont(img *image.RGBA) (*painter, error) {
	f, err := loadFont()
	if err != nil {
		return nil, err
	}
	return newPainter(img, f), nil
}

// kuStatic is KunlunWar's never-changing scene: the 61×61 gid tiles
// (voids omitted) and the 37 city anchors in normalized coords, with
// schematic id labels (city names live in the client localization bundles,
// not in mapOut). The browser merges live ownership frames by city id.
func kuStatic() (*StaticMap, error) {
	mp, err := kunlunLayout()
	if err != nil {
		return nil, err
	}
	return warStatic(kuPackMain, mp, StreamKunlun, "ku")
}

// warStatic is the engine-shared static builder behind kuStatic and
// bacminhStatic: same geometry, each board's own layout + sheet pack.
func warStatic(pk *kuSheetPack, mp *kunlunMapFile, which, art string) (*StaticMap, error) {
	x0, y0, spanW, spanH := kuWorldBox(pk, mp)
	out := &StaticMap{
		Which:  which,
		Art:    art, // sprite-composited background served live by mapart
		Aspect: spanW / spanH,
		WorldW: spanW, WorldH: spanH,
		TileW: kuHalfW * 2, TileH: kuHalfH * 2,
		Tiles:  make([]StreamTile, 0, mp.Col*mp.Row),
		Cities: make([]StaticCity, 0, len(mp.Cities)),
	}
	grid := make([]byte, 0, mp.Col*mp.Row)
	for ty := 0; ty < mp.Row; ty++ {
		for tx := 0; tx < mp.Col; tx++ {
			gid := mp.Tiles[ty][tx]
			grid = append(grid, byte('0'+gid))
			if gid == 0 {
				continue
			}
			x, y := kuTileCenter(float64(tx), float64(ty), mp.Col)
			out.Tiles = append(out.Tiles, StreamTile{G: gid, X: (x - x0) / spanW, Y: (y - y0) / spanH})
		}
	}
	// The web map ships the full matrix too: a tap anywhere inverse-projects
	// to a cell, and void/obstacle cells must be rejected client-side.
	out.Ku = &KuGrid{
		Col: mp.Col, Row: mp.Row, HW: kuHalfW, HH: kuHalfH,
		OX: x0, OY: y0, Grid: string(grid),
	}
	names := kuCityNames()
	for _, c := range mp.Cities {
		x, y := kuTileCenter(c.TX, c.TY, mp.Col)
		out.Cities = append(out.Cities, StaticCity{
			ID: uint64(c.ID), Name: names[c.ID],
			X: (x - x0) / spanW, Y: (y - y0) / spanH,
			P: uint64(int(c.TY)*mp.Col + int(c.TX)),
			W: 2 * kuHalfW * 1.7 / spanW, H: 2 * kuHalfH * 1.7 / spanH,
		})
	}
	return out, nil
}

// KunlunSlotsOf decodes a raw 219919 answer and returns the unionID →
// palette-slot map the PNG compositor and streamKunlun both use, so callers
// outside the package (the bot's own-fleet overlay) can translate a leader's
// union ids into the SAME slot numbers KuFrame.Cells ships. Slots depend on
// first-appearance order inside THIS payload — pair them with the frame
// built from the same bytes.
func KunlunSlotsOf(payload []byte) (map[int64]int, error) {
	rsp, err := DecodeKunlunMini(payload)
	if err != nil {
		return nil, err
	}
	mp, err := kunlunLayout()
	if err != nil {
		return nil, err
	}
	return kuUnionSlots(mp, rsp), nil
}

// streamKunlun is the browser frame for KunlunWar (which = "kunlun").
func streamKunlun(payload []byte) (*StreamMap, error) {
	rsp, err := DecodeKunlunMini(payload)
	if err != nil {
		return nil, err
	}
	mp, err := kunlunLayout()
	if err != nil {
		return nil, err
	}
	return warFrame(kuPackMain, mp, rsp, "ku")
}

// warFrame is the engine-shared live frame behind streamKunlun and
// streamBacMinh. art names the board's mapart bundle ("ku"/"bm").
func warFrame(pk *kuSheetPack, mp *kunlunMapFile, rsp *proto.KunlunWarMiniMapInfoRespMsg, art string) (*StreamMap, error) {
	slots := kuUnionSlots(mp, rsp)
	cityOwner := map[int]int64{}
	cityFight := map[int]bool{}
	for _, c := range rsp.GetCityInfo() {
		cityOwner[int(c.GetCityId())] = c.GetUnionId()
		cityFight[int(c.GetCityId())] = c.GetHaveFight()
	}
	names := map[int64]string{}
	for _, u := range rsp.GetBaseData() {
		names[u.GetUnionId()] = u.GetName()
	}
	x0, y0, spanW, spanH := kuWorldBox(pk, mp)
	// The events card uses the immutable embedded map background (SERVER-RAM
	// POLICY 2026-10-06: a page view must never trigger a server render — the
	// live tinted board lives in the mini app, baked in the browser).
	out := &StreamMap{Aspect: spanW / spanH, Art: art, Bg: "bg.jpg",
		Cities: make([]StreamCity, 0, len(mp.Cities))}
	cityNames := kuCityNames()
	for _, c := range mp.Cities {
		x, y := kuTileCenter(c.TX, c.TY, mp.Col)
		sc := StreamCity{ID: uint64(c.ID), X: (x - x0) / spanW, Y: (y - y0) / spanH, Name: cityNames[c.ID], Lab: fmt.Sprintf("%d", c.ID), Fight: cityFight[c.ID]}
		if u := cityOwner[c.ID]; u != 0 {
			if uc, ok := kuUnionColor(slots, u); ok {
				sc.Color = kuColorHex(uc)
				sc.Owner = names[u]
				s := slots[u]
				sc.CI = &s
			}
		}
		out.Cities = append(out.Cities, sc)
	}
	// Occupied cells for the /kunlun mini app's live tints — the exact set
	// the PNG compositor colors (slot = kuUnionSlots, palette = kuColorVec).
	if cells := rsp.GetCellInfo(); len(cells) > 0 {
		out.Ku = &KuFrame{Cells: make([]KuCell, 0, len(cells))}
		for _, cl := range cells {
			u, fight := cl.GetUnionId(), cl.GetHaveFight()
			if u == 0 && !fight {
				continue
			}
			s := -1 // contested empty tile — no tint, marker only
			if u != 0 {
				v, ok := slots[u]
				if !ok && !fight {
					continue // owned, slot unknown, nothing to mark
				}
				s = v
			}
			out.Ku.Cells = append(out.Ku.Cells, KuCell{P: int64(cl.GetIndex()), S: s, F: fight})
		}
		sort.Slice(out.Ku.Cells, func(i, j int) bool { return out.Ku.Cells[i].P < out.Ku.Cells[j].P })
	}
	return out, nil
}
