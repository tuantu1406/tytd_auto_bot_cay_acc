package gimap

// holylandscene.go ships the /cuuu mini app its scene bundle: the exact
// static inputs of renderHolyLand (world box, bg grid, road sprites,
// footprint-build offsets, forts, nameplate, cities + Vietnamese names), so
// the browser replays the SAME compositor with canvas. Ownership-dependent
// painting (road/plate tint, owner text, fight disc, marches) is intentionally
// NOT baked — it arrives on the live SSE frames, whose city `color` already
// carries the hlUnionColor result (no re-derivation browser-side).
//
// Scene JSON is memoised (stable bytes → the day-cache tier works); pack
// files are served straight from the embedded FS with immutable cache, the
// same tiering as the kunlun bundle.

import (
	"encoding/json"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"sync"
)

const (
	hlSceneFortW  = 260.0
	hlSceneFortH  = 240.0
	hlSceneFortDy = 40.0 // art centered slightly above the city anchor
)

type hlSceneBG struct {
	W int `json:"w"`
	H int `json:"h"`
	// Px/Py = scene mapOffset: the client pads the bg itemVec like every
	// other layer; the browser bake must ride the same offset or houses
	// float off their islands (owner review 2026-10-10).
	Px    float64  `json:"px"`
	Py    float64  `json:"py"`
	Cols  int      `json:"cols"`
	Rows  int      `json:"rows"`
	Files []string `json:"files"`
}

type hlSceneRoad struct {
	File string  `json:"file"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
	A    uint64  `json:"a"` // connected city ids (tint rule: both share one non-zero union)
	B    uint64  `json:"b"`
}

type hlSceneBuild struct {
	Key  string  `json:"key"`
	File string  `json:"file"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
	OX   float64 `json:"ox"` // top-left offset vs item_city's top-left (hlItemCityHalf box)
	OY   float64 `json:"oy"`
}

type hlSceneCity struct {
	ID    uint64  `json:"id"`
	Name  string  `json:"name"`
	Type  int     `json:"type"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Build int     `json:"bi"` // index into builds, −1 = none
	Fort  int     `json:"fi"` // index into forts, −1 = none
}

type hlSceneJSON struct {
	WorldW float64 `json:"worldW"`
	WorldH float64 `json:"worldH"`
	// Palette mirrors hlColorVec: a city's live CI indexes it (mod 12) —
	// the browser replays index → color, never re-derives the order.
	Palette []string       `json:"palette"`
	Half    float64        `json:"half"` // item_city half size (225)
	FortW   float64        `json:"fortW"`
	FortH   float64        `json:"fortH"`
	FortDy  float64        `json:"fortDy"`
	Bg      hlSceneBG      `json:"bg"`
	Plate   *hlSpriteJSON  `json:"plate,omitempty"`
	Roads   []hlSceneRoad  `json:"roads"`
	Builds  []hlSceneBuild `json:"builds"`
	Forts   []hlSpriteJSON `json:"forts"`
	Cities  []hlSceneCity  `json:"cities"`
	Assets  []string       `json:"assets"`
}

type hlSpriteJSON struct {
	File string  `json:"file"`
	W    float64 `json:"w,omitempty"`
	H    float64 `json:"h,omitempty"`
}

var (
	hlSceneOnce sync.Once
	hlSceneVal  []byte
	hlSceneErr  error
)

// HolyLandSceneJSON builds the scene bundle bytes once per process.
func HolyLandSceneJSON() ([]byte, error) {
	hlSceneOnce.Do(func() { hlSceneVal, hlSceneErr = buildHolyLandScene() })
	return hlSceneVal, hlSceneErr
}

func buildHolyLandScene() ([]byte, error) {
	m, err := loadHolyLandMap()
	if err != nil {
		return nil, err
	}
	pk, err := loadHLPack()
	if err != nil {
		return nil, err
	}
	names := loadNames()
	out := &hlSceneJSON{
		WorldW: hlWorldW, WorldH: hlWorldH, Half: hlItemCityHalf,
		FortW: hlSceneFortW, FortH: hlSceneFortH, FortDy: hlSceneFortDy,
	}
	for _, c := range hlColorVec {
		out.Palette = append(out.Palette, hexOf(c))
	}
	assets := map[string]bool{}
	add := func(f string) {
		if f != "" {
			assets[f] = true
		}
	}
	bt := pk.man.BGTile
	for _, f := range bt.Files {
		if f != nil {
			out.Bg.Files = append(out.Bg.Files, *f)
			add(*f)
		}
	}
	out.Bg.W, out.Bg.H, out.Bg.Cols, out.Bg.Rows = bt.W, bt.H, bt.Cols, bt.Rows
	out.Bg.Px, out.Bg.Py = hlPadL, hlPadT

	// Road sprites, in the map file's own order (painter order preserved).
	for _, r := range m.roads {
		meta, ok := pk.man.Roads[strconv.Itoa(r.GID)]
		if !ok {
			continue
		}
		out.Roads = append(out.Roads, hlSceneRoad{
			File: meta.File, X: r.X, Y: r.Y, W: float64(meta.W), H: float64(meta.H),
			A: r.Conn[0], B: r.Conn[1],
		})
		add(meta.File)
	}

	// Footprint builds: resolve the FGUI pivot rule ONCE server-side so the
	// page just blits at (cityX−Half+ox, cityY−Half+oy) — identical pixels to
	// the Go compositor.
	// Only manifest builds some city type actually uses (stock pack also
	// carries variants the current map file never references).
	used := map[string]bool{}
	for _, c := range m.cities {
		if key, ok := hlBuildKey[c.Type]; ok {
			if _, exists := pk.man.Builds[key]; exists {
				used[key] = true
			}
		}
	}
	keys := make([]string, 0, len(used))
	for k := range used {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buildByKey := map[string]int{}
	for _, key := range keys {
		bm := pk.man.Builds[key]
		ox, oy := float64(0), float64(0)
		if len(bm.XY) >= 2 {
			ox, oy = float64(bm.XY[0]), float64(bm.XY[1])
			if bm.centered() {
				ox -= float64(bm.W) / 2
				oy -= float64(bm.H) / 2
			}
		}
		buildByKey[key] = len(out.Builds)
		out.Builds = append(out.Builds, hlSceneBuild{
			Key: key, File: bm.File, W: float64(bm.W), H: float64(bm.H), OX: ox, OY: oy,
		})
		add(bm.File)
	}

	fortByFile := map[string]int{}
	for _, c := range m.cities {
		bi, ok := buildByKey[hlBuildKey[c.Type]]
		if !ok {
			bi = -1
		}
		fi := -1
		if fm, ok := pk.man.Forts[strconv.Itoa(c.Type)]; ok {
			if idx, seen := fortByFile[fm.File]; seen {
				fi = idx
			} else {
				fi = len(out.Forts)
				fortByFile[fm.File] = fi
				out.Forts = append(out.Forts, hlSpriteJSON{File: fm.File})
				add(fm.File)
			}
		}
		label := names.HLCities[strconv.FormatUint(c.ID, 10)]
		if label == "" {
			if t, ok := names.HLTypes[strconv.Itoa(c.Type)]; ok {
				label = t
			}
		}
		out.Cities = append(out.Cities, hlSceneCity{
			ID: c.ID, Name: label, Type: c.Type, X: c.X, Y: c.Y, Build: bi, Fort: fi,
		})
	}
	sort.Slice(out.Cities, func(i, j int) bool { return out.Cities[i].ID < out.Cities[j].ID })

	if pk.man.Plate != nil {
		out.Plate = &hlSpriteJSON{File: pk.man.Plate.File, W: 230, H: 64}
		add(pk.man.Plate.File)
	}
	for f := range assets {
		out.Assets = append(out.Assets, f)
	}
	sort.Strings(out.Assets)
	return json.Marshal(out)
}

// HLPackFile serves one embedded Holy Land pack file by its bare name.
// Only base names resolve (no traversal) and only real pack members answer,
// so /events/mapart?b=hl&p=… can never read outside holyland/.
func HLPackFile(name string) ([]byte, bool) {
	if name == "" || name != path.Base(name) || name == "." || name == ".." {
		return nil, false
	}
	raw, err := holyLandPackFS.ReadFile("holyland/" + name)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// HLPackHas reports pack membership (tests).
func HLPackHas(name string) bool {
	_, err := fs.Stat(holyLandPackFS, "holyland/"+name)
	return err == nil
}
