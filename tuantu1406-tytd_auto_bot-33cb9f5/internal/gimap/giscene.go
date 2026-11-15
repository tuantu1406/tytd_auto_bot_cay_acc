package gimap

// God Island presentation constants — the Cocos client's own atlas13 sprite
// rects (decoded from its .bin), terrain colors, and the territory-hex slot
// rule. They were client-UI facts living only inside events.html's JS; the
// mini app must draw pixel-identical art from the SAME numbers, so they move
// here as the single source (events.html keeps its working copy untouched).
//
// Hex rule (client renderCom): an owned city paints its footprint tiles with
// atlas rect HexR[HexBase + abs(ci) % len(HexR)] — the hex art IS the union
// palette (slot = the same palette index the live frame's CI carries).

// GIScene is the God Island-only section of a StaticMap (json "gi").
type GIScene struct {
	Atlas    string         `json:"atlas"`    // via /events/mapart?b=gi
	TerrainR map[int][4]int `json:"terrainR"` // tile gid → atlas rect [x,y,w,h]; gid 5 (water) intentionally absent
	HexR     map[int][4]int `json:"hexR"`     // hex slot gid → atlas rect
	TerrainC map[int]string `json:"terrainC"` // fallback diamond colors for gids without art
	HexBase  int            `json:"hexBase"`  // hex rect = HexR[HexBase + |ci| % span]
}

const giAtlas13 = "vn/RemoteImmortalIsland_atlas13.png"

// Copied verbatim from the client's rects: water (gid 5) has no sprite and
// no entry, so the page must skip it exactly like the client does.
var giTerrainR = map[int][4]int{
	6: {1414, 0, 200, 187}, 7: {1212, 0, 200, 187}, 8: {1247, 189, 200, 187},
	9: {1045, 189, 200, 268}, 10: {1616, 0, 200, 234}, 11: {1010, 459, 200, 187},
	12: {1247, 378, 200, 248}, 13: {1449, 236, 200, 210}, 14: {808, 537, 200, 187},
	15: {1651, 236, 200, 237}, 16: {0, 448, 200, 218},
}

var giHexR = map[int][4]int{
	5: {1616, 997, 200, 172}, 6: {1616, 1171, 200, 172}, 7: {1414, 1144, 200, 172},
	8: {1414, 970, 200, 172}, 9: {1414, 796, 200, 172}, 10: {1010, 0, 200, 172},
	11: {1651, 649, 200, 172}, 12: {808, 363, 200, 172}, 13: {808, 0, 200, 172},
	14: {843, 174, 200, 172}, 15: {0, 668, 200, 172}, 16: {0, 842, 200, 172},
}

var giTerrainC = map[int]string{
	5: "#122b45", 6: "#39795c", 8: "#255c4a", 9: "#2b6951", 10: "#347258",
	11: "#8b6945", 12: "#9c7650", 13: "#aa8157", 14: "#aab9c3", 15: "#becbd2",
	16: "#d2dce0",
}

func giScene() *GIScene {
	return &GIScene{
		Atlas:    giAtlas13,
		TerrainR: giTerrainR,
		HexR:     giHexR,
		TerrainC: giTerrainC,
		HexBase:  5,
	}
}
