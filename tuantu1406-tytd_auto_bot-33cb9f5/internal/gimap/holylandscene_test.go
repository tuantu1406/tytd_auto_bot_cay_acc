package gimap

// P3 pins for the /cuuu mini app: the scene bundle is the browser-side
// compositor's ONLY input, so its shape, its paint-order promises (cities
// sorted, roads in map order), and — critically — the rule that every
// referenced asset resolves through the same HLPackFile accessor the HTTP
// arm serves must be verified without a browser.

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestHolyLandSceneJSON(t *testing.T) {
	b1, err := HolyLandSceneJSON()
	if err != nil {
		t.Fatalf("scene: %v", err)
	}
	b2, err := HolyLandSceneJSON()
	if err != nil || string(b1) != string(b2) {
		t.Fatal("scene bytes unstable (breaks the day-cache tier)")
	}
	var sc hlSceneJSON
	if err := json.Unmarshal(b1, &sc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sc.WorldW != hlWorldW || sc.WorldH != hlWorldH {
		t.Fatalf("world box %v×%v want %v×%v", sc.WorldW, sc.WorldH, hlWorldW, hlWorldH)
	}
	if sc.Half != hlItemCityHalf {
		t.Fatalf("half = %v", sc.Half)
	}
	if len(sc.Palette) != len(hlColorVec) {
		t.Fatalf("palette %d want %d", len(sc.Palette), len(hlColorVec))
	}
	hex := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	for i, c := range sc.Palette {
		if !hex.MatchString(c) {
			t.Fatalf("palette[%d] = %q", i, c)
		}
	}
	if sc.Bg.Cols*sc.Bg.Rows != len(sc.Bg.Files) || sc.Bg.W == 0 {
		t.Fatalf("bg grid %+v files=%d", sc.Bg, len(sc.Bg.Files))
	}
	if sc.Plate == nil || sc.Plate.File == "" {
		t.Fatal("plate missing")
	}
	// Cities: ascending ids, sprite indices in range, names present for the
	// 12 main places.
	seen := map[uint64]bool{}
	for i, c := range sc.Cities {
		if i > 0 && sc.Cities[i-1].ID >= c.ID {
			t.Fatalf("cities not id-sorted at %d", i)
		}
		seen[c.ID] = true
		if c.Build >= len(sc.Builds) {
			t.Fatalf("city %d build index %d out of range", c.ID, c.Build)
		}
		if c.Build >= 0 {
			bm := sc.Builds[c.Build]
			if bm.File == "" || bm.W <= 0 {
				t.Fatalf("build %+v for city %d", bm, c.ID)
			}
		}
		if c.Fort >= len(sc.Forts) {
			t.Fatalf("city %d fort index %d out of range", c.ID, c.Fort)
		}
	}
	if len(sc.Cities) < 12 {
		t.Fatalf("cities = %d, expected the full board", len(sc.Cities))
	}
	// Roads connect real cities (the tint rule reads live ci by these ids).
	for _, r := range sc.Roads {
		if !seen[r.A] || !seen[r.B] {
			t.Fatalf("road %+v connects unknown city", r)
		}
	}
	// The HTTP arm only answers HLPackFile — every referenced name must.
	for _, f := range sc.Assets {
		if !HLPackHas(f) {
			t.Fatalf("asset %q not in embed pack", f)
		}
		if _, ok := HLPackFile(f); !ok {
			t.Fatalf("asset %q unresolvable via HLPackFile", f)
		}
	}
	// Traversal guard on the accessor itself.
	if _, ok := HLPackFile("../holylandscene.go"); ok {
		t.Fatal("HLPackFile resolved a traversal path")
	}
}

func TestHolyLandSceneTopLevelKeys(t *testing.T) {
	// The page reads these names literally — pin the wire contract.
	b, err := HolyLandSceneJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"worldW", "worldH", "half", "palette", "bg", "plate", "roads", "builds", "forts", "cities", "assets"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("scene JSON missing top-level %q", k)
		}
	}
}
