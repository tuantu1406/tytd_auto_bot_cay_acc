package gimap

// Holy Land (Cửu U) map rendering — a client-faithful compositor. The 34-city
// / 50-road topology is static (shipped with the game client) and embedded
// here; only ownership/fight flags are live, from a
// proto.HolyLandGameMiniMapInfoRespMsg payload.
//
// The renderer replays the real HolyLandMapScene layer stack with the
// client's own sprite art (extracted from RemoteHolyLand.fgui + CDN tiles,
// packed under holyland/): background islands (5×2 grid) → union-tinted road
// ribbons (sprite per road segment, hidden unless both endpoints share one
// non-zero union) → city footprint plates (tinted by union) → fortress icons
// (images/city/{type}.png) → nameplates with Vietnamese place names and the
// owner's union color. Geometry follows the client exactly: object
// coordinates are mapOut.json raw + SCENE_PADDIN (left 100 / top 200); the
// unpadded world is HolyLandMapModel.getMapSize() = col/2·150+75+200 ×
// row·150+75+225 = 3950×3900, bg grid NOT padded, objects padded.
//
// Union colors replay getLineColor/indexOf semantics over unionIdVec: main
// cities 1001..1012 scanned in ascending id, each owner pushed on first
// occurrence (0 = unowned included), color = hlColorVec[index % 12].

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"path"
	"sort"
	"strconv"
	"sync"

	"tytd_go/internal/proto"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font/opentype"
	pb "google.golang.org/protobuf/proto"
)

//go:embed holy_land_map.json
var holyLandMapJSON []byte

//go:embed all:holyland
var holyLandPackFS embed.FS

// Client scene constants (HolyLandMapModel / SCENE_PADDIN).
const (
	hlPadL         = 100.0
	hlPadT         = 200.0
	hlWorldW       = 3950.0 // col/2*150 + 75 + 200 (col=49)
	hlWorldH       = 3900.0 // row*150 + 75 + 225 (row=24)
	hlItemCityHalf = 225.0  // item_city is 450×450 with pivot .5 → obj xy is the center
)

// hlColorVec mirrors HolyLandGlobalManager.colorVec.
var hlColorVec = []rgba{
	{R: 245, G: 85, B: 59, A: 255}, {R: 104, G: 133, B: 253, A: 255}, {R: 235, G: 114, B: 203, A: 255}, {R: 255, G: 244, B: 83, A: 255},
	{R: 231, G: 144, B: 89, A: 255}, {R: 164, G: 127, B: 246, A: 255}, {R: 45, G: 194, B: 253, A: 255}, {R: 0, G: 228, B: 209, A: 255},
	{R: 240, G: 216, B: 166, A: 255}, {R: 1, G: 160, B: 166, A: 255}, {R: 255, G: 182, B: 24, A: 255}, {R: 97, G: 218, B: 94, A: 255},
}

type hlMapFile struct {
	Dimensions struct {
		Row        int     `json:"row"`
		Col        int     `json:"col"`
		TileWidth  float64 `json:"tileWidth"`
		TileHeight float64 `json:"tileHeight"`
	} `json:"dimensions"`
	Cities []struct {
		CityID int     `json:"cityId"`
		Type   int     `json:"type"`
		GID    int     `json:"gid"`
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"cities"`
	Roads []struct {
		RoadID   int     `json:"roadId"`
		GID      int     `json:"gid"`
		Connects [2]int  `json:"connects"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		Width    float64 `json:"width"`
		Height   float64 `json:"height"`
	} `json:"roads"`
}

// hlCity / hlRoad carry client-space geometry (padded world coordinates).
type hlCity struct {
	ID   uint64
	Type int
	X    float64 // center, padded world space
	Y    float64
	W    float64
	H    float64
}

type hlRoad struct {
	GID  int
	Conn [2]uint64
	X    float64 // sprite center, padded world space
	Y    float64
}

type holyLandMap struct {
	cities []hlCity
	roads  []hlRoad
}

var (
	hlOnce sync.Once
	hlMap  *holyLandMap
	hlErr  error
)

func loadHolyLandMap() (*holyLandMap, error) {
	hlOnce.Do(func() {
		var f hlMapFile
		if err := json.Unmarshal(holyLandMapJSON, &f); err != nil {
			hlErr = fmt.Errorf("parse embedded holy_land_map.json: %w", err)
			return
		}
		if len(f.Cities) == 0 {
			hlErr = errors.New("holy_land_map.json carries no cities")
			return
		}
		m := &holyLandMap{}
		for _, c := range f.Cities {
			m.cities = append(m.cities, hlCity{
				ID: uint64(c.CityID), Type: c.Type,
				X: c.X + hlPadL, Y: c.Y + hlPadT,
				W: c.Width, H: c.Height,
			})
		}
		for _, r := range f.Roads {
			m.roads = append(m.roads, hlRoad{
				GID: r.GID, Conn: [2]uint64{uint64(r.Connects[0]), uint64(r.Connects[1])},
				X: r.X + hlPadL, Y: r.Y + hlPadT,
			})
		}
		sort.Slice(m.cities, func(i, j int) bool { return m.cities[i].Y < m.cities[j].Y })
		hlMap = m
	})
	return hlMap, hlErr
}

// hlScale maps output width to the world→pixel factor (uniform x/y).
func hlScale(width int) float64 {
	if width < 800 {
		width = 800
	}
	return float64(width) / hlWorldW
}

// ---------------------------------------------------------------------------
// sprite pack (embed FS)

type hlBuildMeta struct {
	File  string          `json:"file"`
	W     int             `json:"w"`
	H     int             `json:"h"`
	Pivot json.RawMessage `json:"pivot"` // [x, y, asAnchor] or null
	XY    []int           `json:"xy"`    // child position relative to item_city top-left
}

// centered reports whether the FGUI pivot anchors the sprite by its center
// (client pivot(0.5,0.5,true)); otherwise xy is the sprite's top-left.
func (b hlBuildMeta) centered() bool {
	if len(b.Pivot) == 0 || string(b.Pivot) == "null" {
		return false
	}
	var arr []any
	if err := json.Unmarshal(b.Pivot, &arr); err != nil || len(arr) < 2 {
		return false
	}
	x, ok1 := arr[0].(float64)
	y, ok2 := arr[1].(float64)
	return ok1 && ok2 && x > 0 && y > 0
}

type hlSpriteMeta struct {
	File string `json:"file"`
	W    int    `json:"w"`
	H    int    `json:"h"`
	XY   []int  `json:"xy"`
	Size []int  `json:"size"`
}

type hlManifest struct {
	BGTile struct {
		W     int       `json:"w"`
		H     int       `json:"h"`
		Cols  int       `json:"cols"`
		Rows  int       `json:"rows"`
		Files []*string `json:"files"`
	} `json:"bgTile"`
	Roads  map[string]hlSpriteMeta `json:"roads"`
	Builds map[string]hlBuildMeta  `json:"builds"`
	Forts  map[string]hlSpriteMeta `json:"forts"`
	Ruins  *hlSpriteMeta           `json:"ruins"`
	Plate  *hlSpriteMeta           `json:"plate"`
}

type hlPack struct {
	man     hlManifest
	mu      sync.Mutex
	sprites map[string]image.Image // decoded by file name
	tints   map[tintColor]*image.RGBA
}

type tintColor struct {
	file       string
	r, g, b, a uint8
}

var (
	hlPackOnce sync.Once
	hlPackVal  *hlPack
	hlPackErr  error
)

func loadHLPack() (*hlPack, error) {
	hlPackOnce.Do(func() {
		raw, err := holyLandPackFS.ReadFile("holyland/manifest.json")
		if err != nil {
			hlPackErr = fmt.Errorf("holyland manifest: %w", err)
			return
		}
		pk := &hlPack{sprites: map[string]image.Image{}, tints: map[tintColor]*image.RGBA{}}
		if err := json.Unmarshal(raw, &pk.man); err != nil {
			hlPackErr = fmt.Errorf("holyland manifest: %w", err)
			return
		}
		hlPackVal = pk
	})
	return hlPackVal, hlPackErr
}

// img returns the decoded sprite for a pack file name (PNG or JPG).
func (pk *hlPack) img(file string) (image.Image, error) {
	pk.mu.Lock()
	defer pk.mu.Unlock()
	if im, ok := pk.sprites[file]; ok {
		return im, nil
	}
	raw, err := holyLandPackFS.ReadFile("holyland/" + file)
	if err != nil {
		return nil, fmt.Errorf("holyland sprite %s: %w", file, err)
	}
	var im image.Image
	switch path.Ext(file) {
	case ".jpg", ".jpeg":
		im, err = jpeg.Decode(bytes.NewReader(raw))
	default:
		im, err = png.Decode(bytes.NewReader(raw))
	}
	if err != nil {
		return nil, fmt.Errorf("decode holyland sprite %s: %w", file, err)
	}
	pk.sprites[file] = im
	return im, nil
}

// tint recolors a sprite to (r,g,b) with global alpha a, caching per combo.
func (pk *hlPack) tint(file string, c rgba, alpha uint8) (*image.RGBA, error) {
	key := tintColor{file: file, r: c.R, g: c.G, b: c.B, a: alpha}
	pk.mu.Lock()
	if t, ok := pk.tints[key]; ok {
		pk.mu.Unlock()
		return t, nil
	}
	// Insurance bound: keys are naturally file×union×alpha (≤ a few hundred),
	// so this only trips on a pathological push stream — and then it costs a
	// re-tint pass, not the season (audit 2026-10-06).
	if len(pk.tints) > 256 {
		pk.tints = map[tintColor]*image.RGBA{}
	}
	pk.mu.Unlock()
	src, err := pk.img(file)
	if err != nil {
		return nil, err
	}
	sb := src.Bounds()
	out := image.NewRGBA(sb)
	for y := sb.Min.Y; y < sb.Max.Y; y++ {
		for x := sb.Min.X; x < sb.Max.X; x++ {
			_, _, _, a := src.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			na := uint8(uint(a>>8) * uint(alpha) / 255)
			// image.RGBA is premultiplied-alpha — write channels scaled by na.
			out.SetRGBA(x, y, rgba{
				R: uint8(uint(c.R) * uint(na) / 255),
				G: uint8(uint(c.G) * uint(na) / 255),
				B: uint8(uint(c.B) * uint(na) / 255),
				A: na,
			})
		}
	}
	pk.mu.Lock()
	pk.tints[key] = out
	pk.mu.Unlock()
	return out, nil
}

// hlBlit scales a source image into a world-space box (already scaled by s).
func hlBlit(dst *image.RGBA, src image.Image, x0, y0, x1, y1 int) {
	dr := image.Rect(x0, y0, x1, y1)
	if dr.Dx() <= 0 || dr.Dy() <= 0 {
		return
	}
	xdraw.ApproxBiLinear.Scale(dst, dr, src, src.Bounds(), xdraw.Over, nil)
}

// hlDiscRGBA is a soft 128-px disc (center opaque, edge fades) for markers.
func hlDiscRGBA() *image.RGBA {
	const n = 128
	d := image.NewRGBA(image.Rect(0, 0, n, n))
	r := float64(n) / 2
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)+0.5-r, float64(y)+0.5-r
			dist := math.Sqrt(dx*dx+dy*dy) / r
			if dist >= 1 {
				continue
			}
			edge := (1 - dist) * n / 4
			a := uint8(255)
			if edge < 255 {
				a = uint8(edge)
			}
			d.SetRGBA(x, y, rgba{
				R: uint8(255 * uint(a) / 255),
				G: uint8(70 * uint(a) / 255),
				B: uint8(85 * uint(a) / 255),
				A: a,
			})
		}
	}
	return d
}

// ---------------------------------------------------------------------------
// union color rule (client getLineColor / unionIdVec)

// hlUnionOrder builds the first-occurrence color order: main cities
// 1001..1012 ascending, each owner pushed once (0 included — the client
// pushes it too, shifting every later color).
func hlUnionOrder(overlay map[string]cityState) []uint64 {
	seq := []uint64{}
	for id := uint64(1001); id <= 1012; id++ {
		u := overlay[strconv.FormatUint(id, 10)].UnionID
		found := false
		for _, s := range seq {
			if s == u {
				found = true
				break
			}
		}
		if !found {
			seq = append(seq, u)
		}
	}
	return seq
}

// hlUnionColor returns the client line color for a union id, or a pale stone
// gray fallback for unions that never appear among the main cities.
func hlUnionColor(seq []uint64, u uint64) rgba {
	for i, s := range seq {
		if s == u {
			return hlColorVec[i%len(hlColorVec)]
		}
	}
	return rgba{R: 207, G: 216, B: 232, A: 255}
}

// hlColorHex is hlUnionColor in "#rrggbb" form for the browser stream.
func hlColorHex(seq []uint64, u uint64) string {
	c := hlUnionColor(seq, u)
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

var hlBuildKey = map[int]string{1: "1", 2: "2", 3: "3", 4: "4"}

// ---------------------------------------------------------------------------
// public API

// DecodeHolyLand turns a HolyLandGameMiniMapInfoRespMsg payload (transport
// 2536) into the ownership overlay keyed by decimal city id. The minimap
// owner record is UnionBaseData: field 1 = union id, field 4 = union name;
// baseData presence marks the city as owned.
func DecodeHolyLand(payload []byte) (map[string]cityState, error) {
	var resp proto.HolyLandGameMiniMapInfoRespMsg
	if err := pb.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("decode holy land minimap response: %w", err)
	}
	if resp.GetRet() != 0 {
		return nil, fmt.Errorf("server rejected minimap request (ret=%d)", resp.GetRet())
	}
	cities := resp.GetCityInfo()
	if len(cities) == 0 {
		return nil, errors.New("minimap response carries no city records")
	}
	states := make(map[string]cityState, len(cities))
	for _, c := range cities {
		state := cityState{HaveFight: c.GetHaveFight()}
		if base := c.GetBaseData(); base != nil {
			state.UnionID = uint64(base.GetUnionId())
			state.Owner = base.GetName()
		}
		states[strconv.FormatUint(uint64(c.GetCityId()), 10)] = state
	}
	return states, nil
}

// LayoutHolyLand is the interactive twin of RenderHolyLand: city anchors in
// output-pixel coordinates (identical transform to the PNG at the same
// width), sorted by id for a stable answer.
func LayoutHolyLand(payload []byte, width int) (*MapLayout, error) {
	m, err := loadHolyLandMap()
	if err != nil {
		return nil, err
	}
	overlay, err := DecodeHolyLand(payload)
	if err != nil {
		return nil, err
	}
	s := hlScale(width)
	height := int(math.Round(hlWorldH * s))
	out := &MapLayout{Width: width, Height: height, Cities: make([]CityPoint, 0, len(m.cities))}
	r := max(16, int(150*s))
	for _, c := range m.cities {
		state := overlay[strconv.FormatUint(c.ID, 10)]
		out.Cities = append(out.Cities, CityPoint{
			ID: c.ID, X: int(math.Round(c.X * s)), Y: int(math.Round(c.Y * s)), R: r,
			Owner: state.Owner, Fight: state.HaveFight,
		})
	}
	sort.Slice(out.Cities, func(i, j int) bool { return out.Cities[i].ID < out.Cities[j].ID })
	return out, nil
}

// RenderHolyLand decodes a minimap payload and returns the finished map as
// PNG bytes. width is the output width in pixels (minimum 800).
func RenderHolyLand(payload []byte, width int) ([]byte, error) {
	m, err := loadHolyLandMap()
	if err != nil {
		return nil, err
	}
	overlay, err := DecodeHolyLand(payload)
	if err != nil {
		return nil, err
	}
	ttf, err := loadFont()
	if err != nil {
		return nil, fmt.Errorf("font: %w", err)
	}
	img, err := renderHolyLand(m, overlay, ttf, width)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, err
	}
	// SERVER-RAM policy: /cuuu_map is the ONLY pixel consumer of the pack
	// now — release the decoded sprites and their tint derivatives, or one
	// Telegram photo pins ~25 MB forever on the 512 MB VPS. The next explicit
	// command re-decodes (render-holyland.go:235 build never touches pixels).
	if pk, err := loadHLPack(); err == nil {
		pk.release()
	}
	return buf.Bytes(), nil
}

// release drops the decoded pixel caches (manifest and error state stay).
// See RenderHolyLand: on-demand rendering must not leave the atlas pinned.
func (pk *hlPack) release() {
	pk.mu.Lock()
	defer pk.mu.Unlock()
	pk.sprites = map[string]image.Image{}
	pk.tints = map[tintColor]*image.RGBA{}
}

// renderHolyLand composites the client's layer stack at output scale s.
func renderHolyLand(m *holyLandMap, overlay map[string]cityState, ttf *opentype.Font, width int) (*image.RGBA, error) {
	if width < 800 {
		width = 800
	}
	pk, err := loadHLPack()
	if err != nil {
		return nil, err
	}
	s := hlScale(width)
	height := int(math.Round(hlWorldH * s))
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fillRect(img, img.Bounds(), background)
	X := func(v float64) int { return int(math.Round(v * s)) }

	// 1. background islands — 5×2 grid of 750×1800 tiles at the scene
	// pad (client mapOffset rides bg AND items — one coordinate space).
	bt := pk.man.BGTile
	if bt.W > 0 && bt.H > 0 && bt.Cols > 0 && bt.Rows > 0 {
		for i, f := range bt.Files {
			if f == nil {
				continue
			}
			im, err := pk.img(*f)
			if err != nil {
				return nil, err
			}
			// The client applies mapOffset (left 100 / top 200) to the bg
			// itemVec EXACTLY like to the city items — the grid rides the
			// same pad. Drawing it unpadded shifted every house off its
			// island by ~1.5 tiles (owner review 2026-10-10, the same
			// "layer lệch" class as the old Côn Lôn bug).
			o, n := i%bt.Cols, i/bt.Cols
			hlBlit(img, im, X(float64(o)*float64(bt.W)+hlPadL), X(float64(n)*float64(bt.H)+hlPadT),
				X(float64(o+1)*float64(bt.W)+hlPadL), X(float64(n+1)*float64(bt.H)+hlPadT))
		}
	}

	unionSeq := hlUnionOrder(overlay)
	ownerOf := func(id uint64) (uint64, string) {
		st := overlay[strconv.FormatUint(id, 10)]
		return st.UnionID, st.Owner
	}

	// 2. roads — visible only when both endpoints share one non-zero union,
	// tinted getLineColor at half alpha (client e.visible/color gate + alpha .5).
	for _, r := range m.roads {
		meta, ok := pk.man.Roads[strconv.Itoa(r.GID)]
		if !ok {
			continue
		}
		ua, _ := ownerOf(r.Conn[0])
		ub, _ := ownerOf(r.Conn[1])
		if ua == 0 || ua != ub {
			continue
		}
		tinted, err := pk.tint(meta.File, hlUnionColor(unionSeq, ua), 127)
		if err != nil {
			return nil, err
		}
		hw, hh := float64(meta.W)/2, float64(meta.H)/2
		hlBlit(img, tinted, X(r.X-hw), X(r.Y-hh), X(r.X+hw), X(r.Y+hh))
	}

	// 3. cities — footprint plate (union tint over stone art), fortress icon,
	// fight marker; painter's-algorithm bottom-up so lower islands overlap.
	// Sprite bottom edge per city (world px, relative to the city anchor) —
	// the nameplate hangs from the ART's foot, not a fixed offset: the tall
	// tế đàn pagoda sat ~140 px above a hardcoded plate (owner review
	// 2026-10-10 "tên ở xa chân"), while small mines matched the old value.
	plateFeet := make(map[uint64]float64, len(m.cities))
	for _, c := range m.cities {
		u, _ := ownerOf(c.ID)
		col := rgba{R: 207, G: 216, B: 232, A: 255}
		if u != 0 {
			col = hlUnionColor(unionSeq, u)
		}
		feet := 0.0
		if key, ok := hlBuildKey[c.Type]; ok {
			if bm, ok := pk.man.Builds[key]; ok {
				// The junction art is baked colorful (road stroke palette),
				// so it must always be recolored: union color when owned,
				// a faint stone gray when not.
				var spr *image.RGBA
				var err error
				if u != 0 {
					spr, err = pk.tint(bm.File, col, 178)
				} else {
					spr, err = pk.tint(bm.File, rgba{R: 207, G: 216, B: 232, A: 255}, 70)
				}
				if err != nil {
					return nil, err
				}
				// child xy is relative to item_city's top-left; pivot .5 means
				// the sprite is centered there, otherwise xy is its top-left.
				cx0, cy0 := c.X-hlItemCityHalf, c.Y-hlItemCityHalf
				var px0, py0 float64
				if bm.centered() {
					px0 = float64(bm.XY[0]) - float64(bm.W)/2
					py0 = float64(bm.XY[1]) - float64(bm.H)/2
				} else {
					px0, py0 = float64(bm.XY[0]), float64(bm.XY[1])
				}
				hlBlit(img, spr, X(cx0+px0), X(cy0+py0),
					X(cx0+px0)+int(math.Round(float64(bm.W)*s)), X(cy0+py0)+int(math.Round(float64(bm.H)*s)))
				feet = math.Max(feet, py0+float64(bm.H)+hlItemCityHalf)
			}
		}
		if fm, ok := pk.man.Forts[strconv.Itoa(c.Type)]; ok {
			fort, err := pk.img(fm.File)
			if err != nil {
				return nil, err
			}
			// Loader art 260×240 world px centered slightly above the anchor
			// (matches the validated client look).
			fw, fh := 260.0, 240.0
			hlBlit(img, fort, X(c.X-fw/2), X(c.Y-40-fh/2), X(c.X+fw/2), X(c.Y-40+fh/2))
			feet = math.Max(feet, -40+fh/2)
		}
		if st := overlay[strconv.FormatUint(c.ID, 10)]; st.HaveFight {
			const d = 420.0
			hlBlit(img, hlDiscRGBA(), X(c.X-d/2), X(c.Y-40-d/2), X(c.X+d/2), X(c.Y-40+d/2))
		}
		if feet < 90 { // never ride over the art; keep the validated mine gap
			feet = 90
		}
		plateFeet[c.ID] = feet
	}

	// 4. nameplates on top (client draws item_cityUI as its own layer):
	// plate bg, Vietnamese place name, union pennant + owner in union color.
	p := newPainter(img, ttf)
	names := loadNames().HLCities
	types := loadNames().HLTypes
	plateW, plateH := 230.0, 64.0
	nameSize := max(10, int(math.Round(40*s)))
	ownerSize := max(9, int(math.Round(30*s)))
	for _, c := range m.cities {
		u, owner := ownerOf(c.ID)
		cx, cy := c.X*s, c.Y*s
		feet := plateFeet[c.ID]
		if feet == 0 {
			feet = 185
		}
		if pk.man.Plate != nil {
			plate, err := pk.img(pk.man.Plate.File)
			if err != nil {
				return nil, err
			}
			hlBlit(img, plate, int((cx - plateW/2)), int(cy+feet*s), int(cx+plateW/2), int(cy+feet*s+plateH*s))
		}
		label := names[strconv.FormatUint(c.ID, 10)]
		if label == "" {
			if t, ok := types[strconv.Itoa(c.Type)]; ok {
				label = t
			}
		}
		if label != "" {
			label = displayText(p, label, nameSize)
			label = fitText(p, label, nameSize, int(220*s))
			_ = p.text(int(cx), int(cy+feet*s+plateH*s*0.66), label, nameSize, rgba{R: 255, G: 255, B: 255, A: 255}, true)
		}
		if u == 0 || owner == "" {
			continue
		}
		col := hlUnionColor(unionSeq, u)
		// Pennant: small union-colored tab at the plate's left (client
		// unionFlag chip).
		tab := image.NewRGBA(image.Rect(0, 0, 4, 4))
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				tab.SetRGBA(x, y, col)
			}
		}
		hlBlit(img, tab, int(cx-91*s), int(cy+(feet-20)*s), int(cx-61*s), int(cy+(feet+10)*s))
		owner = displayText(p, owner, ownerSize)
		owner = fitText(p, owner, ownerSize, int(300*s))
		_ = p.text(int(cx), int(cy+(feet+plateH+14)*s), owner, ownerSize, col, true)
	}
	return img, nil
}

// hlDecodeRoutes turns a 209522 answer into browser march markers: endpoints
// must be real map cities (the client's model keeps stale playerRouteInfo
// entries otherwise), and the marker color replays the client's union line
// rule over the minimap overlay's color order.
func hlDecodeRoutes(blob []byte, unionSeq []uint64, known map[uint64]bool) []StreamRoute {
	if len(blob) == 0 {
		return nil
	}
	var resp proto.HolyLandGameCityInfoRespMsg
	if err := pb.Unmarshal(blob, &resp); err != nil || resp.GetRet() != 0 {
		return nil
	}
	var out []StreamRoute
	for _, r := range resp.GetRouteInfo() {
		from, to := uint64(r.GetFromCityId()), uint64(r.GetToCityId())
		if from == 0 || to == 0 || from == to || !known[from] || !known[to] {
			continue
		}
		color := hlColorHex(unionSeq, uint64(r.GetUnionId()))
		out = append(out, StreamRoute{
			From: from, To: to, Name: r.GetName(), Union: uint64(r.GetUnionId()),
			Color: color, Start: r.GetTime(), Arrive: r.GetArrival(),
		})
	}
	return out
}
