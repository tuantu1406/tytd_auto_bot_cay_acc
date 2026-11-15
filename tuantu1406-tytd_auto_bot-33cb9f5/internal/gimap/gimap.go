// Package gimap renders the God Island (đảo bồng lai) minimap into a PNG.
//
// Geometry (isometric tiles, city anchors) comes from the game CDN's
// mapOutList.json; city ownership comes from a live
// proto.GodIslandGameMiniMapInfoRespMsg payload decoded with the generated
// protobuf types. Port of ../remoteBlessFrog/cmd/maprender, with the CLI and
// the hand-rolled protobuf wire parser replaced by internal/proto decoding and
// an in-process API: fetch config once, render payloads on demand.
package gimap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"tytd_go/internal/proto"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/text/unicode/norm"
	pb "google.golang.org/protobuf/proto"
)

// The two God Island variants ship different geometry assets; each is fetched
// once and cached (see LoadMapConfig / LoadMapConfigPeak).
const (
	MapConfigURL     = "https://cdn-xddq-vn.hwrescdn.com/71004001/remoteAssets/remoteImmortalIsland/906e890b97/json/mapOutList.json"
	MapConfigURLPeak = "https://cdn-xddq-vn.hwrescdn.com/71004001/remoteAssets/remoteImmortalIsland/906e890b97/json/mapOutListPeak.json"
)

type rgba = color.RGBA

var (
	background = rgba{R: 8, G: 18, B: 35, A: 255}
	white      = rgba{R: 239, G: 247, B: 252, A: 255}
	dimText    = rgba{R: 144, G: 177, B: 204, A: 255}
	panel      = rgba{R: 4, G: 11, B: 22, A: 235}
	gray       = rgba{R: 105, G: 123, B: 140, A: 255}
)

type mapJSON struct {
	Type       int      `json:"type"`
	Row        int      `json:"row"`
	Col        int      `json:"col"`
	TileHeight float64  `json:"tileHeight"`
	TileWidth  float64  `json:"tileWidth"`
	BaseItem   []string `json:"baseItem"`
	Item       []string `json:"item"`
	City       []string `json:"city"`
}

type tile struct {
	GID  int
	X, Y float64
}

type city struct {
	GID  int
	ID   uint64
	X, Y float64
	// Core/Other index into the Item tile list (client's own indices):
	// the footprint whose tiles get the union's territory-hex art, with
	// Core drawn a shade heavier like the client's renderCom.
	Core          []int32
	Other         []int32
	Width, Height float64
}

// cityState is the per-city overlay from the server minimap response.
type cityState struct {
	Owner     string
	UnionID   uint64
	HaveFight bool
}

// parsedMap is the cached, parsed CDN config.
type parsedMap struct {
	geom    mapJSON
	base    []tile
	detail  []tile
	cities  []city
	fetched time.Time
}

const mapConfigTTL = 24 * time.Hour

var (
	mapMu        sync.Mutex
	mapCache     *parsedMap
	mapCachePeak *parsedMap
	mapClient    = &http.Client{Timeout: 20 * time.Second}
)

// LoadMapConfig fetches (or returns the cached) normal-island (Bồng Lai)
// mapOutList config.
func LoadMapConfig() (*parsedMap, error) {
	return loadMapConfig(&mapCache, MapConfigURL)
}

// LoadMapConfigPeak fetches (or returns the cached) peak-island (Thiên Ma)
// mapOutListPeak config. The two variants share the layout but ship different
// geometry, so each asset has its own cache slot.
func LoadMapConfigPeak() (*parsedMap, error) {
	return loadMapConfig(&mapCachePeak, MapConfigURLPeak)
}

// loadMapConfig is the shared fetch+cache behind LoadMapConfig/
// LoadMapConfigPeak. The CDN asset version-changes rarely; it is re-fetched
// after mapConfigTTL or on renderer restart. Failures are not cached.
func loadMapConfig(slot **parsedMap, url string) (*parsedMap, error) {
	mapMu.Lock()
	defer mapMu.Unlock()
	if *slot != nil && time.Since((*slot).fetched) < mapConfigTTL {
		return *slot, nil
	}
	m, base, detail, cities, err := fetchAndParseMap(url)
	if err != nil {
		return nil, err
	}
	*slot = &parsedMap{geom: m, base: base, detail: detail, cities: cities, fetched: time.Now()}
	return *slot, nil
}

func fetchAndParseMap(url string) (mapJSON, []tile, []tile, []city, error) {
	var m mapJSON
	data, err := httpGet(url)
	if err != nil {
		return m, nil, nil, nil, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, nil, nil, nil, err
	}
	parseTiles := func(entries []string) ([]tile, error) {
		out := make([]tile, 0, len(entries))
		for _, entry := range entries {
			parts := strings.Split(entry, "|")
			if len(parts) != 3 {
				return nil, fmt.Errorf("invalid tile entry %q", entry)
			}
			gid, err := strconv.Atoi(parts[0])
			if err != nil {
				return nil, err
			}
			x, err := strconv.ParseFloat(parts[1], 64)
			if err != nil {
				return nil, err
			}
			y, err := strconv.ParseFloat(parts[2], 64)
			if err != nil {
				return nil, err
			}
			out = append(out, tile{GID: gid, X: x, Y: y})
		}
		return out, nil
	}
	base, err := parseTiles(m.BaseItem)
	if err != nil {
		return m, nil, nil, nil, err
	}
	detail, err := parseTiles(m.Item)
	if err != nil {
		return m, nil, nil, nil, err
	}
	outCities := make([]city, 0, len(m.City))
	for _, entry := range m.City {
		parts := strings.Split(entry, "|")
		if len(parts) != 8 {
			return m, nil, nil, nil, fmt.Errorf("invalid city entry %q", entry)
		}
		gid, err := strconv.Atoi(parts[0])
		if err != nil {
			return m, nil, nil, nil, err
		}
		idPart := strings.SplitN(parts[1], "-", 2)[0]
		id, err := strconv.ParseUint(idPart, 10, 64)
		if err != nil {
			return m, nil, nil, nil, err
		}
		values := make([]float64, 4)
		for i := range values {
			values[i], err = strconv.ParseFloat(parts[i+2], 64)
			if err != nil {
				return m, nil, nil, nil, err
			}
		}
		// "cityId-<core tile indices>;<other tile indices>" — the city's
		// tile footprint, used for the client's territory-hex art. A city
		// without the suffix simply claims no tiles.
		var core, other []int32
		if dash := strings.IndexByte(parts[1], '-'); dash >= 0 {
			parseIdxs := func(s string) []int32 {
				var out []int32
				for _, n := range strings.Split(s, ",") {
					if v, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
						out = append(out, int32(v))
					}
				}
				return out
			}
			groups := strings.SplitN(parts[1][dash+1:], ";", 2)
			core = parseIdxs(groups[0])
			if len(groups) > 1 {
				other = parseIdxs(groups[1])
			}
		}
		outCities = append(outCities, city{GID: gid, ID: id, X: values[0], Y: values[1], Core: core, Other: other, Width: values[2], Height: values[3]})
	}
	return m, base, detail, outCities, nil
}

func httpGet(url string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "god-island-map-renderer/1.0")
	response, err := mapClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: %s", url, response.Status)
	}
	const maxMapBytes = 16 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxMapBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMapBytes {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes", url, maxMapBytes)
	}
	return data, nil
}

// decodeCities turns a GodIslandGameMiniMapInfoRespMsg payload (the data part
// of transport 1ab2) into the ownership overlay keyed by decimal city id.
func decodeCities(payload []byte) (map[string]cityState, error) {
	var resp proto.GodIslandGameMiniMapInfoRespMsg
	if err := pb.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("decode minimap response: %w", err)
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
		// baseData presence marks the city as owned; field 1 is the union id,
		// field 4 the union name.
		if base := c.GetBaseData(); base != nil {
			state.UnionID = uint64(base.GetUnionId())
			state.Owner = base.GetUnionName()
		}
		states[strconv.FormatUint(uint64(c.GetCityId()), 10)] = state
	}
	return states, nil
}

// ---------------------------------------------------------------------------
// Font
// ---------------------------------------------------------------------------

var (
	fontOnce sync.Once
	fontFace *opentype.Font
	fontErr  error
)

// loadFont resolves a Vietnamese-capable system font once and parses it.
func loadFont() (*opentype.Font, error) {
	fontOnce.Do(func() {
		file := ""
		for _, candidate := range []string{
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
			"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
			"/usr/share/fonts/opentype/noto/NotoSans-Regular.ttf",
		} {
			if _, err := os.Stat(candidate); err == nil {
				file = candidate
				break
			}
		}
		if file == "" {
			fontErr = errors.New("no default font found")
			return
		}
		data, err := os.ReadFile(file)
		if err != nil {
			fontErr = err
			return
		}
		fontFace, fontErr = opentype.Parse(data)
	})
	return fontFace, fontErr
}

type painter struct {
	img   *image.RGBA
	ttf   *opentype.Font
	faces map[int]font.Face
}

func newPainter(img *image.RGBA, ttf *opentype.Font) *painter {
	return &painter{img: img, ttf: ttf, faces: make(map[int]font.Face)}
}

func (p *painter) face(size int) (font.Face, error) {
	if f := p.faces[size]; f != nil {
		return f, nil
	}
	f, err := opentype.NewFace(p.ttf, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	p.faces[size] = f
	return f, nil
}

func (p *painter) text(x, y int, text string, size int, c rgba, centered bool) error {
	f, err := p.face(size)
	if err != nil {
		return err
	}
	d := font.Drawer{Dst: p.img, Src: image.NewUniform(c), Face: f}
	if centered {
		x -= d.MeasureString(text).Round() / 2
	}
	d.Dot = fixed.P(x, y)
	d.DrawString(text)
	return nil
}

func (p *painter) measure(text string, size int) int {
	f, err := p.face(size)
	if err != nil {
		return 0
	}
	return font.MeasureString(f, text).Round()
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// Render decodes a normal-island (Bồng Lai) minimap response payload and
// returns the finished map as PNG bytes. width is the output width in pixels
// (minimum 800).
func Render(payload []byte, width int) ([]byte, error) {
	m, err := LoadMapConfig()
	if err != nil {
		return nil, fmt.Errorf("load map config: %w", err)
	}
	return renderPayload(payload, width, m)
}

// RenderPeak is Render for the peak island (Thiên Ma): same rendering, the
// peak geometry asset instead of the normal one.
func RenderPeak(payload []byte, width int) ([]byte, error) {
	m, err := LoadMapConfigPeak()
	if err != nil {
		return nil, fmt.Errorf("load peak map config: %w", err)
	}
	return renderPayload(payload, width, m)
}

func renderPayload(payload []byte, width int, m *parsedMap) ([]byte, error) {
	if width < 800 {
		width = 800
	}
	overlay, err := decodeCities(payload)
	if err != nil {
		return nil, err
	}
	ttf, err := loadFont()
	if err != nil {
		return nil, fmt.Errorf("font: %w", err)
	}
	// The render mutates its city slice (tier sort); copy so the cache is safe.
	cities := make([]city, len(m.cities))
	copy(cities, m.cities)
	img := render(m.geom, m.base, m.detail, cities, overlay, ttf, width)
	var buf bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// xform maps logical map coordinates to pixels of the width-px output. Both
// the PNG renderer and the interactive Layout API run through it, so an
// overlay placed on the PNG lands exactly on the drawn circle.
type xform struct {
	s      float64 // px() scale: width / 1400
	scale  float64 // logical → pixel
	offX   float64
	offY   float64
	width  int
	height int
}

func makeXform(m mapJSON, width int) xform {
	if width < 800 {
		width = 800
	}
	xf := xform{s: float64(width) / 1400, width: width, height: int(math.Round(float64(width) * 1.14))}
	mapLeft, mapTop := xf.px(35), xf.px(105)
	// Keep bottom room for the larger owner labels so they never sit on the
	// OWNER SUMMARY panel.
	mapWidth, mapHeight := xf.px(1330), xf.px(1150)
	logicalW := float64(m.Col)/2*(m.TileWidth+100) + 50
	logicalH := float64(m.Row)*m.TileHeight + m.TileHeight/2
	xf.scale = math.Min(float64(mapWidth)/logicalW, float64(mapHeight)/logicalH)
	xf.offX = float64(mapLeft) + (float64(mapWidth)-logicalW*xf.scale)/2
	xf.offY = float64(mapTop) + (float64(mapHeight)-logicalH*xf.scale)/2
	return xf
}

// px converts a 1400-px design-space length to output pixels.
func (x xform) px(v float64) int { return int(math.Round(v * x.s)) }

// point maps a logical (mapOutList) coordinate to an output pixel.
func (x xform) point(xVal, yVal float64) (int, int) {
	return int(math.Round(x.offX + xVal*x.scale)), int(math.Round(x.offY + yVal*x.scale))
}

// cityRadius is the marker circle radius for one city (before the white
// outline the renderer adds around it).
func (x xform) cityRadius(id uint64) int {
	radiusBase := map[int]int{1: 36, 2: 29, 3: 32, 4: 39, 5: 44, 6: 32}[int(id/1000)]
	if radiusBase == 0 {
		radiusBase = 29
	}
	return x.px(float64(radiusBase))
}

// CityPoint is one city in pixel coordinates of the PNG that Render/
// RenderPeak produce at the same width — the anchor for an HTML click
// overlay on top of that image. Owner is the union name ("" = unoccupied).
type CityPoint struct {
	ID    uint64 `json:"id"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	R     int    `json:"r"`
	Owner string `json:"owner,omitempty"`
	Fight bool   `json:"fight,omitempty"`
}

// MapLayout is the interactive twin of Render/RenderPeak: instead of pixels
// it answers where every city sits and who holds it.
type MapLayout struct {
	Width  int         `json:"w"`
	Height int         `json:"h"`
	Cities []CityPoint `json:"cities"`
}

// Layout decodes a minimap payload like Render does but returns the city
// overlay in output-pixel coordinates (identical transform as the PNG at the
// same width). Cities are sorted by id for a stable answer.
func Layout(payload []byte, width int, peak bool) (*MapLayout, error) {
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
	xf := makeXform(m.geom, width)
	out := &MapLayout{Width: xf.width, Height: xf.height, Cities: make([]CityPoint, 0, len(m.cities))}
	for _, c := range m.cities {
		state := overlay[strconv.FormatUint(c.ID, 10)]
		x, y := xf.point(c.X, c.Y)
		// +2px white ring: the hit target covers everything the eye sees.
		out.Cities = append(out.Cities, CityPoint{
			ID: c.ID, X: x, Y: y, R: xf.cityRadius(c.ID) + xf.px(2),
			Owner: state.Owner, Fight: state.HaveFight,
		})
	}
	sort.Slice(out.Cities, func(i, j int) bool { return out.Cities[i].ID < out.Cities[j].ID })
	return out, nil
}

func render(m mapJSON, base, detail []tile, cities []city, overlay map[string]cityState, ttf *opentype.Font, width int) *image.RGBA {
	xf := makeXform(m, width)
	height := xf.height
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fillRect(img, img.Bounds(), background)
	p := newPainter(img, ttf)

	px := xf.px
	_ = p.text(px(45), px(53), "GOD ISLAND — MAP", px(34), white, false)
	subtitle := fmt.Sprintf("%d cities  •  live ownership", len(cities))
	_ = p.text(px(47), px(82), subtitle, px(15), dimText, false)

	point := xf.point

	tileColors := map[int]rgba{1: {R: 15, G: 38, B: 59, A: 255}, 2: {R: 21, G: 86, B: 86, A: 255}, 4: {R: 44, G: 104, B: 79, A: 255}}
	tw, th := int(m.TileWidth*xf.scale), int(m.TileHeight*xf.scale)
	for _, t := range base {
		x, y := point(t.X, t.Y)
		c, ok := tileColors[t.GID]
		if !ok {
			c = rgba{R: 30, G: 72, B: 78, A: 255}
		}
		fillDiamond(img, x, y, tw, th, c)
	}
	for _, t := range detail {
		if t.GID == 5 {
			continue
		}
		x, y := point(t.X, t.Y)
		fillCircle(img, x, y, max(1, px(2)), rgba{R: 91, G: 203, B: 158, A: 255})
	}

	ownerColors := make(map[string]rgba)
	for _, state := range overlay {
		if state.Owner != "" {
			ownerColors[state.Owner] = colorForOwner(state.Owner)
		}
	}

	sort.SliceStable(cities, func(i, j int) bool { return cities[i].ID/1000 < cities[j].ID/1000 })
	for _, c := range cities {
		state := overlay[strconv.FormatUint(c.ID, 10)]
		markerColor := gray
		if state.Owner != "" {
			markerColor = ownerColors[state.Owner]
		}
		tier := int(c.ID / 1000)
		// Circles are sized around the id text (~1.4x bigger now) so the
		// number still fits inside on a 1200px-wide render.
		radiusBase := map[int]int{1: 36, 2: 29, 3: 32, 4: 39, 5: 44, 6: 32}[tier]
		if radiusBase == 0 {
			radiusBase = 29
		}
		radius := px(float64(radiusBase))
		x, y := point(c.X, c.Y)
		if state.HaveFight {
			fillCircle(img, x, y, radius+px(5), rgba{R: 255, G: 66, B: 75, A: 255})
		}
		fillCircle(img, x, y, radius+px(2), white)
		fillCircle(img, x, y, radius, markerColor)
		idText := strconv.FormatUint(c.ID, 10)
		idSize := px(25)
		if tier != 2 {
			idSize = px(29)
		}
		_ = p.text(x, y+idSize/3, idText, idSize, rgba{R: 5, G: 13, B: 25, A: 255}, true)

		owner := state.Owner
		if owner == "" {
			owner = "Unoccupied"
		}
		ownerSize := px(14)
		owner = displayText(p, owner, ownerSize)
		owner = fitText(p, owner, ownerSize, px(150))
		labelW := p.measure(owner, ownerSize) + px(14)
		labelY := y + radius + px(6)
		fillRoundedRect(img, image.Rect(x-labelW/2, labelY, x+labelW/2, labelY+px(24)), px(5), panel)
		_ = p.text(x, labelY+px(17), owner, ownerSize, white, true)
	}

	counts := make(map[string]int)
	for _, state := range overlay {
		if state.Owner == "" {
			counts["Unoccupied"]++
		} else {
			counts[state.Owner]++
		}
	}
	legendY := height - px(115)
	_ = p.text(px(45), legendY, "OWNER SUMMARY", px(15), white, false)
	type ownerCount struct {
		Name  string
		Count int
	}
	ordered := make([]ownerCount, 0, len(counts))
	for name, count := range counts {
		ordered = append(ordered, ownerCount{name, count})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Count != ordered[j].Count {
			return ordered[i].Count > ordered[j].Count
		}
		return ordered[i].Name < ordered[j].Name
	})
	legendX := px(45)
	for i, item := range ordered {
		if i >= 5 {
			break
		}
		c := gray
		if item.Name != "Unoccupied" {
			c = ownerColors[item.Name]
		}
		fillCircle(img, legendX+px(6), legendY+px(26), px(6), c)
		name := displayText(p, item.Name, px(11))
		label := fmt.Sprintf("%s: %d", fitText(p, name, px(11), px(140)), item.Count)
		_ = p.text(legendX+px(18), legendY+px(30), label, px(11), dimText, false)
		legendX += px(225)
	}
	footer := "Geometry: mapOutList.json  •  City state: live protobuf  •  PNG generated by Go"
	_ = p.text(px(45), height-px(25), footer, px(10), dimText, false)
	return img
}

func fitText(p *painter, text string, size, maxWidth int) string {
	if p.measure(text, size) <= maxWidth {
		return text
	}
	runes := []rune(text)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		candidate := string(runes) + "…"
		if p.measure(candidate, size) <= maxWidth {
			return candidate
		}
	}
	return "…"
}

// displayText performs compatibility normalization (for example, mathematical
// monospace K/T/V becomes ordinary K/T/V), removes variation selectors, and
// drops glyphs unavailable in the selected font. The decoded owner string in
// memory is never changed; this affects only the pixels written to the PNG.
func displayText(p *painter, text string, size int) string {
	f, err := p.face(size)
	if err != nil {
		return text
	}
	var out strings.Builder
	for _, r := range norm.NFKC.String(text) {
		if r == '\ufe0e' || r == '\ufe0f' {
			continue
		}
		if _, ok := f.GlyphAdvance(r); ok {
			out.WriteRune(r)
		}
	}
	clean := strings.TrimSpace(out.String())
	if clean == "" && text != "" {
		return "?"
	}
	return clean
}

func colorForOwner(owner string) rgba {
	h := fnv.New32a()
	_, _ = h.Write([]byte(owner))
	hue := float64(h.Sum32()%360) / 60
	s, v := 0.62, 0.93
	c := v * s
	x := c * (1 - math.Abs(math.Mod(hue, 2)-1))
	m := v - c
	var r, g, b float64
	switch int(hue) {
	case 0:
		r, g, b = c, x, 0
	case 1:
		r, g, b = x, c, 0
	case 2:
		r, g, b = 0, c, x
	case 3:
		r, g, b = 0, x, c
	case 4:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return rgba{R: uint8((r + m) * 255), G: uint8((g + m) * 255), B: uint8((b + m) * 255), A: 255}
}

func fillRect(img *image.RGBA, rect image.Rectangle, c rgba) {
	rect = rect.Intersect(img.Bounds())
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func fillCircle(img *image.RGBA, cx, cy, radius int, c rgba) {
	r2 := radius * radius
	for y := -radius; y <= radius; y++ {
		xSpan := int(math.Sqrt(float64(r2 - y*y)))
		fillRect(img, image.Rect(cx-xSpan, cy+y, cx+xSpan+1, cy+y+1), c)
	}
}

func fillRoundedRect(img *image.RGBA, rect image.Rectangle, radius int, c rgba) {
	if radius <= 0 {
		fillRect(img, rect, c)
		return
	}
	fillRect(img, image.Rect(rect.Min.X+radius, rect.Min.Y, rect.Max.X-radius, rect.Max.Y), c)
	fillRect(img, image.Rect(rect.Min.X, rect.Min.Y+radius, rect.Max.X, rect.Max.Y-radius), c)
	fillCircle(img, rect.Min.X+radius, rect.Min.Y+radius, radius, c)
	fillCircle(img, rect.Max.X-radius-1, rect.Min.Y+radius, radius, c)
	fillCircle(img, rect.Min.X+radius, rect.Max.Y-radius-1, radius, c)
	fillCircle(img, rect.Max.X-radius-1, rect.Max.Y-radius-1, radius, c)
}

func fillDiamond(img *image.RGBA, cx, cy, width, height int, c rgba) {
	if width <= 0 || height <= 0 {
		return
	}
	halfW, halfH := width/2, height/2
	for dy := -halfH; dy <= halfH; dy++ {
		ratio := 1 - math.Abs(float64(dy))/float64(max(1, halfH))
		span := int(float64(halfW) * ratio)
		fillRect(img, image.Rect(cx-span, cy+dy, cx+span+1, cy+dy+1), c)
	}
}
