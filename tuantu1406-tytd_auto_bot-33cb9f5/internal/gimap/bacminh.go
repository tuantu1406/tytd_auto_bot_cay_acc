package gimap

// Đỉnh Bắc Minh (北冥之巅, client prefix bmzd_) — the SECOND board on the
// KunlunWar engine: activity type 332, same wire opcodes (219xxx routed by
// activityId), same tile geometry (214×126), but its own 119×119 grid
// (kunlun is 61×61 — the north map is 3.8× the area), its own anchor affine
// px = 12733 + 107·(tx−ty), py = 63·(tx+ty) (fit over all 14161 base[]
// anchors, max error 0 — see tools/bacminh_bake.py), 81 cities, and its own
// sprite sheet. The castles are NOT the Côn Lôn FGUI art (the old kunlun
// alias bake rendered the wrong houses, user report 2026-11-03): the client
// draws every north castle as an animated spine skeleton (spine/northCity/)
// chosen by floor(id/1000)+id%1000 per RemoteKunlunWarCityItem.initNorthCity
// — 1xxx city1, 2xxx cityUp2/citydown2 (m>2), 3xxx city3 (flip INVERTED),
// 4xxx city4, 5xxx cityUp5/citydown5 (m>8), 6xxx and 8xxx cityUp6/citydown6
// (m>8) — baked offline as native-scale stills by /tmp spine_render.py and
// packed by tools/bacminh_sprites.py (NORTH_DIR). The sheet's non-city art
// (bridges, ground, decor) still comes from the RemoteKunlunWar FGUI bundle.
//
// Everything engine-shared (compositor, union-slot rule, palette, placement
// rules, stream frames, scene export) lives in kunlun.go operating on a
// kuSheetPack + a *kunlunMapFile — this file only binds the north board's
// embeds to that machinery. SERVER-RAM policy is doubled down here: the
// north sheet decodes to ≈54 MB RGBA (2× kunlun), so it is ONLY ever decoded
// inside RenderBacMinh* and released the moment the PNG is encoded
// (renderWar ends in pk.releasePixels).

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"tytd_go/internal/proto"
)

//go:embed bacminh_map.json
var bacminhMapJSON []byte

//go:embed bacminh_sprites.png
var bmSheetPNG []byte

//go:embed bacminh_sprites.json
var bmSheetManifest []byte

//go:embed bacminh_bg.jpg
var bmBgJPEG []byte

// bmPackMain is the north board's sheet pack — its own rect manifest, pixel
// decode and render caches, never shared with Côn Lôn's kuPackMain.
var bmPackMain = func() *kuSheetPack {
	p := newKuSheetPack(bmSheetPNG, bmSheetManifest, bmBgJPEG)
	// The north client draws its castles like GI/Thiên Ma place plates —
	// centred on the cell, not hanging off the tile anchor (the owner's
	// "thành vẽ lệch với chân đế" report, 2026-11-03).
	p.cityCentered = true
	return p
}()

var (
	bmMapOnce sync.Once
	bmMap     *kunlunMapFile
	bmMapErr  error
)

// bacminhLayout parses the embedded north layout once (same schema as
// kunlun_map.json — one decode type, two boards).
func bacminhLayout() (*kunlunMapFile, error) {
	bmMapOnce.Do(func() {
		bmMap = &kunlunMapFile{}
		bmMapErr = json.Unmarshal(bacminhMapJSON, bmMap)
	})
	return bmMap, bmMapErr
}

// BacMinhCols reports the north grid width (119 for the current bake). The
// bot layer MUST derive posIndex decompositions from this rather than a
// hard-coded constant — the client has resized these grids before.
func BacMinhCols() int {
	mp, err := bacminhLayout()
	if err != nil {
		return 0
	}
	return mp.Col
}

// DecodeBacMinhMini parses a 219919 answer for the north window. The wire
// message is identical to Côn Lôn's — same proto, one decode.
func DecodeBacMinhMini(payload []byte) (*proto.KunlunWarMiniMapInfoRespMsg, error) {
	return DecodeKunlunMini(payload)
}

// BacMinhCellPassable reports whether posIndex y*Col+x is a marchable north
// tile (event ground, city ground, bridge — the KuPassableGids gids).
func BacMinhCellPassable(pos int64) bool {
	mp, err := bacminhLayout()
	if err != nil {
		return false
	}
	return kuCellPassable(mp, pos)
}

// BacMinhCityPoint is the posIndex of the block a north city stands on — the
// Move target for web march/attack orders (the server alone validates the
// route).
func BacMinhCityPoint(cityID uint64) (int32, bool) {
	mp, err := bacminhLayout()
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

// BacMinhCityNameAt names the north city standing on posIndex, if any. The
// north city ids reuse the KunlunWar localization table; missing ids render
// as "Thành <id>".
func BacMinhCityNameAt(pos int64) (string, bool) {
	mp, err := bacminhLayout()
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

// BacMinhUnionSlot is the exported north palette-slot lookup (see
// KuUnionSlot): the /bacminh mini app colors marching routes by slot over
// the SAME twelve-color palette — the engine rule is per-board, keyed by
// this board's client city order. -1 = union not on this board.
func BacMinhUnionSlot(miniPayload []byte, unionID int64) int {
	if unionID == 0 {
		return -1
	}
	rsp, err := DecodeBacMinhMini(miniPayload)
	if err != nil {
		return -1
	}
	mp, err := bacminhLayout()
	if err != nil {
		return -1
	}
	if s, ok := kuUnionSlots(mp, rsp)[unionID]; ok {
		return s
	}
	return -1
}

// BacMinhSlotsOf decodes a raw north 219919 answer and returns the unionID →
// palette-slot map (see KunlunSlotsOf — pair with frames built from the
// SAME bytes).
func BacMinhSlotsOf(payload []byte) (map[int64]int, error) {
	rsp, err := DecodeBacMinhMini(payload)
	if err != nil {
		return nil, err
	}
	mp, err := bacminhLayout()
	if err != nil {
		return nil, err
	}
	return kuUnionSlots(mp, rsp), nil
}

// RenderBacMinh draws the north board with the client's own sprites (see
// RenderKunlun — same compositor, north layout + sheet pack). width = canvas
// px. The only web-facing pixel consumer left; it releases every decoded
// cache before returning (SERVER-RAM policy, the user's OOM warning: the
// north sheet decode is ~2× Côn Lôn's).
func RenderBacMinh(payload []byte, width int) ([]byte, error) {
	return renderBacMinh(payload, width, true)
}

// RenderBacMinhBoard is the web background variant without owner tags or the
// legend strip (see RenderKunlunBoard).
func RenderBacMinhBoard(payload []byte, width int) ([]byte, error) {
	return renderBacMinh(payload, width, false)
}

func renderBacMinh(payload []byte, width int, annot bool) ([]byte, error) {
	rsp, err := DecodeBacMinhMini(payload)
	if err != nil {
		return nil, err
	}
	mp, err := bacminhLayout()
	if err != nil {
		return nil, err
	}
	return renderWar(bmPackMain, mp, rsp, width, annot)
}

// bacminhStatic is the never-changing north scene for the events card /
// mapstatic arm (see kuStatic).
func bacminhStatic() (*StaticMap, error) {
	mp, err := bacminhLayout()
	if err != nil {
		return nil, err
	}
	return warStatic(bmPackMain, mp, StreamBacMinh, "bm")
}

// streamBacMinh is the browser frame for Đỉnh Bắc Minh (which = "bacminh").
func streamBacMinh(payload []byte) (*StreamMap, error) {
	rsp, err := DecodeBacMinhMini(payload)
	if err != nil {
		return nil, err
	}
	mp, err := bacminhLayout()
	if err != nil {
		return nil, err
	}
	return warFrame(bmPackMain, mp, rsp, "bm")
}

// BmSheetPNG / BmBgJPEG expose the embedded north sheet and map background
// so the browser fetches the SAME bytes the Go compositor draws from.
func BmSheetPNG() []byte { return bmSheetPNG }
func BmBgJPEG() []byte   { return bmBgJPEG }
