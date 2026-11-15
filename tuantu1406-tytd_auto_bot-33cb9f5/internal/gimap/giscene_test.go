package gimap

// P4 pins: the God Island mini app draws straight from the static scene's
// "gi" section, so the atlas rect tables must arrive intact (they were
// client-UI constants hardcoded in events.html JS — a lost or re-keyed entry
// silently draws the wrong mountain there, and now here too).

import (
	"encoding/json"
	"testing"
)

func TestGodIslandSceneInStatic(t *testing.T) {
	for _, which := range []string{StreamBonglai, StreamThienma} {
		sm, err := StreamStatic(which)
		if err != nil {
			t.Fatalf("%s: %v", which, err)
		}
		if sm.GI == nil || sm.GI.Atlas != giAtlas13 {
			t.Fatalf("%s: gi section missing/wrong atlas: %+v", which, sm.GI)
		}
		// The exact client rects, spot-checked — not just "non-empty".
		if got := sm.GI.TerrainR[6]; got != [4]int{1414, 0, 200, 187} {
			t.Fatalf("terrainR[6] = %v", got)
		}
		if got := sm.GI.HexR[16]; got != [4]int{0, 842, 200, 172} {
			t.Fatalf("hexR[16] = %v", got)
		}
		if _, water := sm.GI.TerrainR[5]; water {
			t.Fatal("water gid must have NO terrain sprite (client skips it)")
		}
		if sm.GI.TerrainC[5] != "#122b45" || sm.GI.HexBase != 5 {
			t.Fatalf("water color/base: %v %v", sm.GI.TerrainC[5], sm.GI.HexBase)
		}
		// The hex-slot rule depends on the span being exactly 12.
		if len(sm.GI.HexR) != 12 {
			t.Fatalf("hex span %d, page formula |ci|%%12 assumes 12", len(sm.GI.HexR))
		}
		for _, gid := range []int{6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16} {
			if _, ok := sm.GI.TerrainR[gid]; !ok {
				t.Fatalf("terrainR missing gid %d", gid)
			}
		}
		// Wire contract: json key + numeric-string map keys (page looks up
		// terrainR[t.g] with a JS number).
		b, err := json.Marshal(sm)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		raw, ok := m["gi"]
		if !ok {
			t.Fatalf("%s: static JSON has no top-level \"gi\"", which)
		}
		var gi GIScene
		if err := json.Unmarshal(raw, &gi); err != nil {
			t.Fatalf("gi round-trip: %v", err)
		}
		if gi.TerrainR[9] != sm.GI.TerrainR[9] {
			t.Fatal("terrainR lost in JSON round-trip")
		}
	}
}
