package gimap

// KunlunSceneJSON exports the WHOLE static KunlunWar scene — the grid, the
// exact sprite layers, the sheet rects and the client geometry constants —
// as one memoised JSON bundle for the /kunlun mini app. The browser draws
// the same compositor pass the Go PNG does: ground sprites (centred on the
// cell) → live cell tints → faded decor + city plates (raised: bottom edge
// on the anchor), depth-sorted by centre-y = anchor + h/2 like the client's
// zIndex. Sprite boxes here are pre-projected into kuTileCenter space minus
// the world-box origin, so every coordinate matches the /events/mapstatic
// normalization the mini app clicks against.
//
// Loose line/minimap art (client klzb_* pngs) is embedded under kunlunart/
// and served through the mapart ku arm alongside the sheet.

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"image/png"
	"sort"
	"sync"
)

//go:embed kunlunart/*
var kuArtFS embed.FS

// kuArtFile returns an embedded loose art file by its bare name.
func kuArtFile(name string) ([]byte, bool) {
	b, err := kuArtFS.ReadFile("kunlunart/" + name)
	return b, err == nil
}

// KuArtNames lists the bare names of every embedded loose art file, so the
// web layer can build its mapart allow-list straight from the embed (never a
// hand-maintained copy that drifts from what's actually shipped).
func KuArtNames() []string {
	ents, err := kuArtFS.ReadDir("kunlunart")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// KuArtFile returns one embedded loose art file (line/minimap png) by bare
// name, exported for the mapart ku arm.
func KuArtFile(name string) ([]byte, bool) { return kuArtFile(name) }

// KuLineNames returns the five march-line pngs in client kind order (index 0
// = kind 1 = enemy), which is exactly what the mini app indexes preloaded
// images by when it draws a KuRoute.K.
func KuLineNames() []string { return kuSpriteKinds[1:] }

// KuSheetPNG / KuBgJPEG expose the embedded sprite sheet and map background
// so the browser fetches the SAME bytes the Go compositor draws from.
func KuSheetPNG() []byte { return kuSheetPNG }
func KuBgJPEG() []byte   { return kuBgJPEG }

// kuSceneSprite is one layer entry as the browser consumes it:
// [x, y, w, h, img, flags] — x/y is the sprite's horizontal centre / the
// TILE ANCHOR (getPosByCoordinate = the diamond's TOP vertex). Two placement
// rules, selected by which layer array the entry sits in: ground layers draw
// centred on the cell (anchor + hh), while raised plates (decor + city) hang
// UP from the anchor (DecorateItem/CityItem: bottom edge on the anchor, span
// [y−h, y]) so top = y − h. flags bit0=flipX, bit1=city plate (raised),
// bit2=faded decor.
type kuSceneSprite struct {
	X, Y, W, H float64
	Img        string
	Flags      int
}

func (s kuSceneSprite) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{s.X, s.Y, s.W, s.H, s.Img, s.Flags})
}

func (s *kuSceneSprite) UnmarshalJSON(b []byte) error {
	var a []any
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if len(a) != 6 {
		return fmt.Errorf("kuSceneSprite: want 6 fields, got %d", len(a))
	}
	num := func(v any) (float64, error) {
		f, ok := v.(float64)
		if !ok {
			return 0, fmt.Errorf("kuSceneSprite: want number, got %T", v)
		}
		return f, nil
	}
	var err error
	if s.X, err = num(a[0]); err != nil {
		return err
	}
	if s.Y, err = num(a[1]); err != nil {
		return err
	}
	if s.W, err = num(a[2]); err != nil {
		return err
	}
	if s.H, err = num(a[3]); err != nil {
		return err
	}
	img, ok := a[4].(string)
	if !ok {
		return fmt.Errorf("kuSceneSprite: want img string, got %T", a[4])
	}
	s.Img = img
	f, err := num(a[5])
	if err != nil {
		return err
	}
	s.Flags = int(f)
	return nil
}

type kuSceneCity struct {
	ID   int     `json:"id"`
	TX   float64 `json:"tx"`
	TY   float64 `json:"ty"`
	Name string  `json:"name"`
	Rank int     `json:"rank"` // client tier rule cityId % 1000
	Pos  int64   `json:"pos"`  // posIndex the city stands on
	// Boff shifts the name label below the tile anchor to the plate's
	// bottom edge on cell-centred boards (north): anchor + hh + h/2.
	// Omitted (0) on kunlun — its plates hang on the anchor and its scene
	// JSON stays byte-stable.
	Boff float64 `json:"boff,omitempty"`
}

// kuScene is the JSON document KunlunSceneJSON returns.
type kuScene struct {
	HW      float64           `json:"hw"`
	HH      float64           `json:"hh"`
	Col     int               `json:"col"`
	Row     int               `json:"row"`
	Box     [4]float64        `json:"box"` // x0, y0, w, h in kuTileCenter space
	Palette []string          `json:"palette"`
	Fade    int               `json:"fade"` // decor alpha, 255ths
	Base    [3]int            `json:"base"` // passable-tile slate fill
	Void    [3]int            `json:"void"` // backdrop under everything
	Grid    string            `json:"grid"` // col*row gid digits
	Layers  kuSceneLayers     `json:"layers"`
	Cities  []kuSceneCity     `json:"cities"`
	Rects   map[string][4]int `json:"rects"` // sprite name → x,y,w,h in sheet
	Sheet   [2]int            `json:"sheet"` // sheet png size
	Assets  kuSceneAssets     `json:"assets"`
}

type kuSceneLayers struct {
	Ground []kuSceneSprite `json:"ground"` // Water+Floor+Block
	Deco   []kuSceneSprite `json:"deco"`   // Object (faded) + city plates (bottom)
}

type kuSceneAssets struct {
	Sheet    string   `json:"sheet"`
	Bg       string   `json:"bg"`
	Lines    []string `json:"lines"`    // index 0..4 → kind 1..5 art variants
	Smallmap string   `json:"smallmap"` //
}

// kuSpriteKind maps a client line-kind code (KuRoute.K, from the LineItem
// enum: 1 enemy / 2 self / 3 own union / 4 home / 5 friendly union) onto the
// embedded art file. The browser just indexes its preloaded line images.
var kuSpriteKinds = []string{"", "klzb_imgLine1.png", "klzb_imgLine2.png", "klzb_imgLine3.png", "klzb_imgLine4.png", "klzb_imgLine5.png"}

var (
	kuSceneOnce  sync.Once
	kuSceneBytes []byte
)

// KunlunSceneJSON returns the static scene bundle (memoised; nil only if
// the embedded layout cannot be parsed, which unit tests make impossible).
func KunlunSceneJSON() []byte {
	kuSceneOnce.Do(func() {
		b, err := buildKunlunScene()
		if err != nil {
			return
		}
		kuSceneBytes = b
	})
	return kuSceneBytes
}

func buildKunlunScene() ([]byte, error) {
	mp, err := kunlunLayout()
	if err != nil {
		return nil, err
	}
	return buildWarScene(kuPackMain, mp, "klzb_smallmap.png")
}

// buildWarScene is the engine-shared scene exporter behind buildKunlunScene
// and buildBacMinhScene: one board's layout + sheet pack in, one browser
// bake document out. smallmap names that board's embedded minimap art.
func buildWarScene(pk *kuSheetPack, mp *kunlunMapFile, smallmap string) ([]byte, error) {
	x0, y0, spanW, spanH := kuWorldBox(pk, mp) // loads the rect manifest
	offX := kuOffX(mp)

	// kuDraw.px/py are in kuAnchor space; the page lives in kuTileCenter
	// space minus the box origin (see kuStatic). Y stays the tile ANCHOR (the
	// diamond's TOP vertex). The browser distinguishes the two client placement
	// rules by which array a sprite is in: ground layers draw centred on the
	// cell (anchor + hh), deco/city draw top = y − h (bottom edge on the
	// anchor). Flags: bit0=flip, bit1=city plate, bit2=faded decor.
	toSprites := func(draws []kuDraw, flags int) []kuSceneSprite {
		out := make([]kuSceneSprite, 0, len(draws))
		for _, d := range draws {
			f := flags | boolFlag(d.flip)
			out = append(out, kuSceneSprite{
				X: d.px - offX - x0, Y: d.py - y0,
				W: d.w, H: d.h, Img: d.src, Flags: f,
			})
		}
		return out
	}

	ground := kuFloorEntries(pk, mp, mp.Water)
	ground = append(ground, kuFloorEntries(pk, mp, mp.Floor)...)
	ground = append(ground, kuFloorEntries(pk, mp, mp.Block)...)
	deco := toSprites(kuEntries(pk, mp, mp.Object), 4)
	cityDraws := toSprites(kuEntries(pk, mp, cityEntries(mp)), 2)
	if pk.cityCentered {
		// North board: castles sit CENTRED on their cell (the GI/Thiên Ma
		// place-plate rule). The browser's plate rule is top = y−h, so
		// baking Y = anchor + hh + h/2 puts the plate's centre on the cell
		// centre — and the depth sort below (Y+H/2) then orders by true
		// centre-y like the client's zIndex.
		for i := range cityDraws {
			cityDraws[i].Y += kuHalfH + cityDraws[i].H/2
		}
	}
	deco = append(deco, cityDraws...)
	sort.SliceStable(deco, func(i, j int) bool {
		// Client depth = zIndex = data.y + data.height/2 (the plate's own
		// centre Y), NOT its anchor; ties break left-to-right.
		cyI, cyJ := deco[i].Y+deco[i].H/2, deco[j].Y+deco[j].H/2
		if cyI != cyJ {
			return cyI < cyJ
		}
		return deco[i].X < deco[j].X
	})

	grid := make([]byte, 0, mp.Col*mp.Row)
	for ty := 0; ty < mp.Row; ty++ {
		for tx := 0; tx < mp.Col; tx++ {
			grid = append(grid, byte('0'+mp.Tiles[ty][tx]))
		}
	}
	names := kuCityNames()
	cities := make([]kuSceneCity, 0, len(mp.Cities))
	for _, c := range mp.Cities {
		sc := kuSceneCity{
			ID: c.ID, TX: c.TX, TY: c.TY, Name: names[c.ID],
			Rank: c.ID % 1000, Pos: int64(c.TY)*int64(mp.Col) + int64(c.TX),
		}
		if pk.cityCentered {
			sc.Boff = kuHalfH + float64(c.H)/2 // name hangs under the plate bottom
		}
		cities = append(cities, sc)
	}
	rects := make(map[string][4]int, len(pk.rects))
	for k, r := range pk.rects {
		rects[k] = [4]int(r)
	}
	sc := &kuScene{
		HW: kuHalfW, HH: kuHalfH, Col: mp.Col, Row: mp.Row,
		Box:     [4]float64{x0, y0, spanW, spanH},
		Palette: make([]string, 0, len(kuColorVec)),
		Fade:    kuFade,
		Base:    [3]int{26, 32, 42},
		Void:    [3]int{10, 14, 24},
		Grid:    string(grid),
		Layers:  kuSceneLayers{Ground: toSprites(ground, 0), Deco: deco},
		Cities:  cities,
		Rects:   rects,
		Sheet:   sheetSize(pk),
		Assets: kuSceneAssets{
			Sheet: "sheet.png", Bg: "bg.jpg",
			Lines:    kuSpriteKinds[1:],
			Smallmap: smallmap,
		},
	}
	for _, c := range kuColorVec {
		sc.Palette = append(sc.Palette, kuColorHex(c))
	}
	return json.Marshal(sc)
}

// sheetSize reports the pack's embedded sheet pixel size (the browser needs
// it to build blit rects). Header-only DecodeConfig — scene building must NOT
// force the ≈19 MB (kunlun) / ≈54 MB (north) pixel decode (the mini-app never
// asks Go to blit).
func sheetSize(pk *kuSheetPack) [2]int {
	if c, err := png.DecodeConfig(bytes.NewReader(pk.sheetPNG)); err == nil {
		return [2]int{c.Width, c.Height}
	}
	return [2]int{2048, 2421}
}

func boolFlag(b bool) int {
	if b {
		return 1
	}
	return 0
}
