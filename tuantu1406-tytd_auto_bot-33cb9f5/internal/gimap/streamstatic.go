package gimap

// StreamStatic is the never-changing half of a live map: the measured world
// box, the client's terrain tiles with their city footprints (for the
// territory-hex art), and the labeled cities. The browser fetches it once
// per card and merges the dynamic SSE StreamMap frames into it by city id —
// so the per-frame payload stays as small as before while the page renders
// the game's real scene.

import "fmt"

// StreamTile is one terrain tile in normalized (0..1) coordinates. G is the
// client's tile gid (the atlas13 sprite selector; gid 5 is water and stays
// unpainted). C names the city whose territory covers the tile — 0 for
// plain ground — and K marks the city's core tiles.
type StreamTile struct {
	G int     `json:"g"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	C uint64  `json:"c,omitempty"`
	K bool    `json:"k,omitempty"`
}

// StaticCity is the fixed geometry + label of one city; owner, color,
// fight and palette slot arrive on the live frames.
type StaticCity struct {
	ID   uint64  `json:"id"`
	Name string  `json:"name,omitempty"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	// P is the board cell the city stands on (posIndex), KunlunWar only —
	// it lets a clicked cell open the right city's popup.
	P    uint64  `json:"p,omitempty"`
	W    float64 `json:"w,omitempty"`
	H    float64 `json:"h,omitempty"`
	Icon string  `json:"icon,omitempty"`
	// Lab is the schematic marker label for icon-less maps (port numbers;
	// "" = plain waypoint dot). See StreamCity.Lab.
	Lab string `json:"lab,omitempty"`
}

// StaticMap is one card's full static scene. WorldW/WorldH are the world
// rectangle's size in client pixels (same units as the atlas rects), so the
// page multiplies normalized positions by them and draws the game's own
// sprite sizes unmodified. TileW/TileH give the diamond metrics needed for
// the client's exact terrain anchoring.
type StaticMap struct {
	Which  string       `json:"which"`
	Art    string       `json:"art,omitempty"`
	Bg     string       `json:"bg,omitempty"`
	Aspect float64      `json:"aspect"`
	WorldW float64      `json:"worldW"`
	WorldH float64      `json:"worldH"`
	TileW  float64      `json:"tileW,omitempty"`
	TileH  float64      `json:"tileH,omitempty"`
	Tiles  []StreamTile `json:"tiles,omitempty"`
	Cities []StaticCity `json:"cities"`
	Links  [][2]uint64  `json:"links,omitempty"`
	// Ku is KunlunWar-only: the geometry a browser needs to inverse-project
	// a tap on the board back onto a 61×61 board cell (see KuGrid).
	Ku *KuGrid `json:"ku,omitempty"`
	// Tc is Di Tích-only: the schematic slot ring + camp palette, so the
	// mini app can bake the board before any live frame arrives.
	Tc *TcScene `json:"tc,omitempty"`
	// GI is God-Island-only: the client atlas13 rects + terrain colors the
	// mini app blits with (see GIScene). events.html keeps its own working
	// JS copy; the mini app treats a missing field as "old binary" loudly.
	GI *GIScene `json:"gi,omitempty"`
}

// TcScene ships the Di Tích presentation constants — the same transform the
// live cities use (ballGVGGeomFor slots, normalized to the world box), plus
// the slot radius (of world width) and the game's camp palette.
type TcScene struct {
	Slots [][2]float64     `json:"slots"`
	SlotR float64          `json:"slotR"`
	Camps map[int32]string `json:"camps"`
}

// KuGrid ships the whole KunlunWar tile matrix in one compact string so the
// web map can turn any click into a posIndex and reject void/obstacle taps
// before ordering. OX/OY are the world box origin in kuTileCenter pixel
// space; the inverse is exactly the client's own coordinate solve:
// u=(px−(col−1)·hw)/hw = tx−ty, v=py/hh = tx+ty.
type KuGrid struct {
	Col  int     `json:"col"`
	Row  int     `json:"row"`
	HW   float64 `json:"hw"` // half tile width (client px)
	HH   float64 `json:"hh"` // half tile height (client px)
	OX   float64 `json:"ox"` // world box left   in kuTileCenter space
	OY   float64 `json:"oy"` // world box top    in kuTileCenter space
	Grid string  `json:"grid"`
}

// StreamStatic builds the static scene for one streamable map. Di Tích has
// no fixed client layout (its slot ring is schematic), so it reports an
// empty city set — the page keeps drawing it from the live frames.
func StreamStatic(which string) (*StaticMap, error) {
	switch which {
	case StreamBonglai:
		return giStatic(false)
	case StreamThienma:
		return giStatic(true)
	case StreamCuuu:
		return hlStatic()
	case StreamDitich:
		return ballGVGStatic()
	case StreamThuongdo:
		return SkyTradeStatic()
	case StreamKunlun:
		return kuStatic()
	case StreamBacMinh:
		return bacminhStatic()
	case StreamLuanHoi:
		return luanHoiStatic()
	case StreamSeaWarWar:
		return seaWarWarStatic()
	default:
		return nil, fmt.Errorf("unknown static map %q", which)
	}
}

func giStatic(peak bool) (*StaticMap, error) {
	m, err := LoadMapConfig()
	if peak {
		m, err = LoadMapConfigPeak()
	}
	if err != nil {
		return nil, err
	}
	originX, originY, logicalW, logicalH := giWorldBox(m)
	if logicalW <= 0 || logicalH <= 0 { // mirror the frame's fallback
		logicalW = float64(m.geom.Col)/2*(m.geom.TileWidth+100) + 50
		logicalH = float64(m.geom.Row)*m.geom.TileHeight + m.geom.TileHeight/2
		originX, originY = 0, 0
	}
	// Tile attribution in the client's own Item-index space: every city
	// claims its footprint, core tiles last so they win over neighbours'
	// overlap like the client's renderCom layering.
	type claim struct {
		id   uint64
		core bool
	}
	claims := make([]claim, len(m.detail))
	claimBy := func(idx []int32, id uint64, core bool) {
		for _, i := range idx {
			if i >= 0 && int(i) < len(claims) {
				claims[i] = claim{id, core}
			}
		}
	}
	for _, c := range m.cities {
		claimBy(c.Other, c.ID, false)
	}
	for _, c := range m.cities {
		claimBy(c.Core, c.ID, true)
	}

	out := &StaticMap{
		Which:  StreamBonglai,
		Art:    ArtGodIsland,
		Aspect: logicalW / logicalH,
		WorldW: logicalW, WorldH: logicalH,
		TileW: m.geom.TileWidth, TileH: m.geom.TileHeight,
		Tiles:  make([]StreamTile, 0, len(m.detail)),
		Cities: make([]StaticCity, 0, len(m.cities)),
	}
	out.GI = giScene() // same atlas13 sheet on both island variants
	if peak {
		out.Which = StreamThienma
		out.Bg = giBgPeak
	} else {
		out.Bg = giBgNormal
	}
	iconDir := "images/city/"
	if peak {
		iconDir = "images/city/peak/"
	}
	for i, t := range m.detail {
		out.Tiles = append(out.Tiles, StreamTile{
			G: t.GID,
			X: (t.X - originX) / logicalW,
			Y: (t.Y - originY) / logicalH,
			C: claims[i].id,
			K: claims[i].core,
		})
	}
	for _, c := range m.cities {
		icon := int(c.ID / 1000)
		if icon < 1 {
			icon = 1
		}
		if icon > maxGiCityIcon {
			icon = maxGiCityIcon
		}
		out.Cities = append(out.Cities, StaticCity{
			ID:   c.ID,
			Name: giCityName(c.ID),
			X:    (c.X - originX) / logicalW,
			Y:    (c.Y - originY) / logicalH,
			W:    c.Width / logicalW,
			H:    c.Height / logicalH,
			Icon: fmt.Sprintf("%s%d.png", iconDir, icon),
		})
	}
	return out, nil
}

func hlStatic() (*StaticMap, error) {
	m, err := loadHolyLandMap()
	if err != nil {
		return nil, err
	}
	// Exact client world box — same as the frames (see streamHolyLand), so
	// the browser's background grid and city fractions stay aligned.
	originX, originY := 0.0, 0.0
	logicalW, logicalH := hlWorldW, hlWorldH
	links := make([][2]uint64, 0, len(m.roads))
	for _, r := range m.roads {
		links = append(links, r.Conn)
	}
	out := &StaticMap{
		Which:  StreamCuuu,
		Art:    ArtHolyLand,
		Bg:     hlBg,
		Aspect: logicalW / logicalH,
		WorldW: logicalW, WorldH: logicalH,
		Cities: make([]StaticCity, 0, len(m.cities)),
		Links:  links,
	}
	for _, c := range m.cities {
		icon := int(c.ID / 1000)
		if icon < 1 {
			icon = 1
		}
		if icon > maxHlCityIcon {
			icon = maxHlCityIcon
		}
		out.Cities = append(out.Cities, StaticCity{
			ID:   c.ID,
			Name: hlCityName(c.ID),
			X:    (c.X - originX) / logicalW,
			Y:    (c.Y - originY) / logicalH,
			W:    c.W / logicalW,
			H:    c.H / logicalH,
			Icon: fmt.Sprintf("images/city/%d.png", icon),
		})
	}
	return out, nil
}

func ballGVGStatic() (*StaticMap, error) {
	g := ballGVGGeomFor(1400)
	slots := make([][2]float64, len(ballGVGSlots))
	for i := range ballGVGSlots {
		x, y, _ := g.slot(i)
		slots[i] = [2]float64{float64(x) / float64(g.width), float64(y) / float64(g.height)}
	}
	_, _, r := g.slot(0)
	camps := make(map[int32]string, len(ballGVGCampColors))
	for id, c := range ballGVGCampColors {
		camps[id] = hexOf(c)
	}
	return &StaticMap{
		Which:  StreamDitich,
		Aspect: float64(g.width) / float64(g.height),
		WorldW: float64(g.width), WorldH: float64(g.height),
		Cities: []StaticCity{},
		Tc:     &TcScene{Slots: slots, SlotR: float64(r) / float64(g.width), Camps: camps},
	}, nil
}

func max2(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
