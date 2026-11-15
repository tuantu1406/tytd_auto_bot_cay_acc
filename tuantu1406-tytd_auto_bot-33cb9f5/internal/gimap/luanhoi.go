package gimap

// Luân Hồi Bí Cảnh (RebornTrial 172) — guild-war bracket map.
//
// The game's battle scene is a 4-tile horizontal strip (client MAP_BG_COUNT=4,
// MAP_BG_WIDTH=750, MAP_BG_HEIGHT=1800) with one union base per bracket slot
// (ChallengeListResp unionMsg[].position → FGUI posItem{n}). We ship the same
// background art (stitched once by tools/luanhoi_art.py, embed below) and the
// client's own base building sprite (mountainIcon, atlas bleed removed).
//
// Node placement: the client hides the real posItem coordinates inside its
// FGUI scene binary, so LuanHoiAnchors is our own layout — a two-wave path
// that walks the valley roads painted in the bg, rank 1 → 16 bottom-left to
// top-right. Both the browser page (scene.json) and the on-demand PNG bake
// through these exact constants, so what the user reviews in the preview is
// what the page draws.
//
// Union colors follow the house 12-color palette (same vector as kunlun/
// holyland); the FRAME carries the derived hex per node (slot = unionId mod
// 12 — stable per union across refreshes; the game itself never tints bases).

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"tytd_go/internal/proto"

	xdraw "golang.org/x/image/draw"
)

//go:embed luanhoi_bg.jpg
var luanHoiBgJPEG []byte

//go:embed luanhoi_base.png
var luanHoiBasePNG []byte

// LuanHoiWorldW/H is the stitched battle canvas (4×750 × 1800).
const (
	LuanHoiWorldW = 3000
	LuanHoiWorldH = 1800
	// LuanHoiBaseW is the base building draw width in world pixels (sprite
	// is 406×371; anchor = bottom-center of the drawn sprite).
	LuanHoiBaseW = 360
)

// LuanHoiAnchors are the up-to-16 bracket slots in world coordinates, index
// 0 = position 1. Spread along the bg's painted valley roads.
var LuanHoiAnchors = [16][2]float64{
	{390, 1560}, {300, 800}, {900, 540}, {760, 1200},
	{1250, 1540}, {1150, 820}, {1580, 560}, {1490, 1240},
	{2050, 1420}, {1830, 920}, {2130, 660}, {2400, 500},
	{2420, 1120}, {2620, 1400}, {2760, 960}, {2780, 560},
}

// LuanHoiAnchor returns the slot for a 1-based bracket position; positions
// beyond 16 wrap into a small vertical offset instead of stacking exactly.
func LuanHoiAnchor(pos int) (float64, float64) {
	if pos < 1 {
		pos = 1
	}
	a := LuanHoiAnchors[(pos-1)%16]
	if pos > 16 {
		a[1] -= 90 * float64((pos-1)/16)
	}
	return a[0], a[1]
}

// LuanHoiPalette is the shared 12-color union palette (hex strings, browser
// fills the ring/nameplate from it).
func LuanHoiPalette() []string {
	out := make([]string, 0, len(kuColorVec))
	for _, c := range kuColorVec {
		out = append(out, kuColorHex(c))
	}
	return out
}

// LuanHoiColorHex picks a union's palette color (stable slot = unionId mod 12).
func LuanHoiColorHex(unionID int64) string {
	i := unionID % int64(len(kuColorVec))
	if i < 0 {
		i += int64(len(kuColorVec))
	}
	return kuColorHex(kuColorVec[i])
}

// luanHoiScene is the browser's bake contract — field order matters only for
// humans reading scene.json through the art proxy.
type luanHoiScene struct {
	WorldW  int        `json:"worldW"`
	WorldH  int        `json:"worldH"`
	BG      string     `json:"bg"`
	Base    string     `json:"base"`
	BaseW   int        `json:"baseW"`
	BaseH   int        `json:"baseH"`
	DrawW   int        `json:"drawW"`
	Anchors [16][2]int `json:"anchors"`
	Palette []string   `json:"palette"`
}

// LuanHoiSceneJSON serves mapart b=lh&p=scene.json.
func LuanHoiSceneJSON() ([]byte, error) {
	sc := luanHoiScene{
		WorldW: LuanHoiWorldW, WorldH: LuanHoiWorldH,
		BG: "bg.jpg", Base: "base.png",
		BaseW: 406, BaseH: 371, DrawW: LuanHoiBaseW,
		Palette: LuanHoiPalette(),
	}
	for i, a := range LuanHoiAnchors {
		sc.Anchors[i] = [2]int{int(a[0]), int(a[1])}
	}
	return json.Marshal(sc)
}

// LuanHoiArt serves the embedded bundle files (mapart b=lh arm).
func LuanHoiArt(path string) ([]byte, bool) {
	switch path {
	case "bg.jpg":
		return luanHoiBgJPEG, true
	case "base.png":
		return luanHoiBasePNG, true
	}
	return nil, false
}

// LuanHoiNode is one base in a preview frame (the browser's live nodes ride
// the SSE payload built in the root package; this is the renderer's input).
type LuanHoiNode struct {
	ID     int64
	Name   string
	Score  int64
	Rank   int
	Pos    int
	LeftHP int64
	Shield bool
	Defeat bool
	IsSelf bool
	// AtkName/AtkCount: newest attacker seen in the fight ring (preview
	// parity with the browser's red rings).
	AtkName  string
	AtkCount int
}

// RenderLuanHoi composes the bracket onto the real battle background (PNG).
// On-demand only (SERVER-RAM policy): the live page composites in-browser.
func RenderLuanHoi(nodes []LuanHoiNode, title string, width int) ([]byte, error) {
	if width < 900 {
		width = 900
	}
	bg, err := jpeg.Decode(bytes.NewReader(luanHoiBgJPEG))
	if err != nil {
		return nil, fmt.Errorf("bg decode: %w", err)
	}
	base, err := png.Decode(bytes.NewReader(luanHoiBasePNG))
	if err != nil {
		return nil, fmt.Errorf("base decode: %w", err)
	}
	scale := float64(width) / float64(LuanHoiWorldW)
	dst := image.NewRGBA(image.Rect(0, 0, width, int(float64(LuanHoiWorldH)*scale)))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), bg, bg.Bounds(), xdraw.Src, nil)

	ttf, err := loadFont()
	if err != nil {
		return nil, fmt.Errorf("font: %w", err)
	}
	p := newPainter(dst, ttf)

	// ground shadow ellipse under each base (anchors the sprite on the art)
	shadow := rgba{R: 8, G: 12, B: 20, A: 90}
	white := rgba{R: 255, G: 255, B: 255, A: 255}
	for _, n := range nodes {
		ax, ay := LuanHoiAnchor(n.Pos)
		cx, cy := ax*scale, ay*scale
		rx, ry := float64(210)*scale, float64(34)*scale
		for py := int(cy - ry); py <= int(cy+ry); py++ {
			fy := (float64(py) - cy) / ry
			half := rx * (1 - fy*fy)
			if half < 1 {
				continue
			}
			p.fillPill(int(cx-half), py, int(cx+half), py+1, shadow)
		}
		dw := int(float64(LuanHoiBaseW) * scale)
		dh := int(float64(371) / 406 * float64(LuanHoiBaseW) * scale)
		dr := image.Rect(int(cx)-dw/2, int(cy)-dh, int(cx)+dw/2, int(cy))
		xdraw.ApproxBiLinear.Scale(dst, dr, base, base.Bounds(), xdraw.Over, nil)

		// union color ring chip + labels (rank · name / score / hp)
		col := colorNRGBA(LuanHoiColorHex(n.ID))
		if n.IsSelf {
			col = rgba{R: 255, G: 236, B: 120, A: 255}
		}
		px := int(cx)
		pyTop := dr.Min.Y - int(18*scale)
		// dark pill behind the name for readability over the light art
		name := fmt.Sprintf("%d. %s", n.Rank, n.Name)
		if n.Defeat {
			name += " (thua)"
		}
		if n.Shield {
			name += " [khiên]"
		}
		tw := p.measure(name, max(11, int(26*scale)))
		p.fillPill(px-tw/2-6, pyTop-int(30*scale), px+tw/2+6, pyTop+int(6*scale), rgba{R: 10, G: 14, B: 22, A: 190})
		// color chip in the pill corner
		for yy := pyTop - int(28*scale); yy <= pyTop-int(6*scale); yy++ {
			for xx := px - tw/2 - 4; xx <= px-tw/2+int(10*scale); xx++ {
				if xx >= 0 && yy >= 0 && xx < dst.Bounds().Dx() && yy < dst.Bounds().Dy() {
					dst.SetRGBA(xx, yy, col)
				}
			}
		}
		_ = p.text(px, pyTop-int(12*scale), name, max(11, int(26*scale)), white, true)
		if n.AtkCount > 0 {
			// red war ring: a slightly larger ellipse OUTLINE around the
			// base's ground shadow (Côn Lôn fight-disc language). Two side
			// ticks per scanline of the enlarged ellipse; no caps — those
			// read as straight bars.
			ring := rgba{R: 255, G: 70, B: 85, A: 220}
			rx2, ry2 := rx+10*scale, ry+6*scale
			for yy := int(cy - ry2); yy <= int(cy+ry2); yy++ {
				fy := (float64(yy) - cy) / ry2
				half := rx2 * (1 - fy*fy)
				if half < 1 {
					continue
				}
				t := max(2, int(5*scale))
				p.fillPill(int(cx-half), yy, int(cx-half)+t, yy+1, ring)
				p.fillPill(int(cx+half)-t, yy, int(cx+half), yy+1, ring)
			}
			badge := fmt.Sprintf("bị đánh ×%d", n.AtkCount)
			if n.AtkName != "" {
				badge = fmt.Sprintf("%s ×%d", n.AtkName, n.AtkCount)
			}
			bs := max(10, int(20*scale))
			bw := p.measure(badge, bs)
			p.fillPill(px-bw/2-5, int(cy)+int(ry)+int(6*scale), px+bw/2+5, int(cy)+int(ry)+int(34*scale), rgba{R: 74, G: 10, B: 18, A: 215})
			_ = p.text(px, int(cy)+int(ry)+int(26*scale), badge, bs, white, true)
		}
		sub := fmt.Sprintf("điểm %d · máu %d", n.Score, n.LeftHP)
		sw := p.measure(sub, max(10, int(20*scale)))
		p.fillPill(px-sw/2-5, pyTop-int(6*scale), px+sw/2+5, pyTop+int(20*scale), rgba{R: 10, G: 14, B: 22, A: 150})
		_ = p.text(px, pyTop+int(12*scale), sub, max(10, int(20*scale)), rgba{R: 223, G: 232, B: 242, A: 255}, true)
	}
	if title != "" {
		p.fillPill(width/2-260, 10, width/2+260, 54, rgba{R: 10, G: 14, B: 22, A: 190})
		_ = p.text(width/2, 42, title, 24, white, true)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func colorNRGBA(hex string) rgba {
	var r, g, b int
	fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	return rgba{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
}

// fillPill paints a translucent rounded-enough rect (plain rect: z-order is
// text-behind, the exact radius never matters in previews).
func (p *painter) fillPill(x0, y0, x1, y1 int, c rgba) {
	x0 = max(x0, 0)
	y0 = max(y0, 0)
	x1 = min(x1, p.img.Bounds().Dx())
	y1 = min(y1, p.img.Bounds().Dy())
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			dst := p.img.RGBAAt(x, y)
			a := uint32(c.A)
			dst.R = uint8((uint32(c.R)*a + uint32(dst.R)*(255-a)) / 255)
			dst.G = uint8((uint32(c.G)*a + uint32(dst.G)*(255-a)) / 255)
			dst.B = uint8((uint32(c.B)*a + uint32(dst.B)*(255-a)) / 255)
			dst.A = 255
			p.img.SetRGBA(x, y, dst)
		}
	}
}

// ─── live frame (SSE payload) ────────────────────────────────────────────────

// LhNode is one base in the bracket, positioned with the SAME anchor table
// the page bakes (x/y normalized to the 3000×1800 world).
type LhNode struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	SV    int64   `json:"sv,omitempty"`
	Score int64   `json:"score"`
	Rank  int     `json:"rank,omitempty"`
	Pos   int     `json:"pos"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	CI    int     `json:"ci"`
	Color string  `json:"color,omitempty"`
	// No omitempty: 0 blood lost (round start) must ride as 0, not vanish
	// into "máu undefined" on the web layer.
	HP     int64 `json:"hp"`
	Shield bool  `json:"shield,omitempty"`
	Defeat bool  `json:"defeat,omitempty"`
	Self   bool  `json:"self,omitempty"`
	Beaten int64 `json:"beaten,omitempty"` // last attack ms (server clock)
	// Atk: recent fight pushes against THIS base (newest first, capped) —
	// the map's "thấy người đánh" layer, Côn Lôn style.
	Atk []LhAtk `json:"atk,omitempty"`
}

// LhAtk is one recent hit: attacker name, damage, seconds ago.
type LhAtk struct {
	N   string `json:"n"`
	D   int64  `json:"d"`
	Ago int64  `json:"ago"`
}

// LhFightIn is a raw fight notice fed to LuanHoiStream (root package owns
// the ring; gimap only folds it onto nodes).
type LhFightIn struct {
	Name     string
	TargetID int64
	Damage   int64
	AtMilli  int64
}

// LhFrame is the which="luanhoi" extras the /luanhoi mini app draws.
type LhFrame struct {
	Round     int32    `json:"round,omitempty"`
	BattleNum int32    `json:"battleNum"`
	Free      int32    `json:"free,omitempty"`
	RecoverAt int64    `json:"recoverAt,omitempty"`
	Nodes     []LhNode `json:"nodes"`
}

// LuanHoiStream folds the 434e bracket into a StreamMap. selfUnion is the
// pulling account's union (0 when guildless — then no node is highlighted).
// shieldWindow is how fresh a shieldTriggerTime must be to still light the
// shield badge (game never told us the duration; one hour is our display
// rule, honestly labelled in the popup as "khiên gần đây").
func LuanHoiStream(msgs []*proto.RebornTrialUnionChallengeMsg, selfUnion int64,
	round, battleNum, free int32, recoverAt, nowMilli int64, fights []LhFightIn) *StreamMap {
	byTarget := map[int64][]LhAtk{}
	for _, f := range fights {
		ago := (nowMilli - f.AtMilli) / 1000
		if ago < 0 || ago > 1800 || len(byTarget[f.TargetID]) >= 5 {
			continue
		}
		byTarget[f.TargetID] = append([]LhAtk{{N: f.Name, D: f.Damage, Ago: ago}}, byTarget[f.TargetID]...)
	}
	f := &LhFrame{Round: round, BattleNum: battleNum, Free: free, RecoverAt: recoverAt}
	for _, m := range msgs {
		b := m.GetBase()
		if b == nil || b.GetUnionId() == 0 {
			continue
		}
		pos := int(m.GetPosition())
		if pos == 0 {
			pos = int(m.GetRank())
		}
		if pos < 1 {
			pos = 1
		}
		ax, ay := LuanHoiAnchor(pos)
		nid := b.GetUnionId()
		f.Nodes = append(f.Nodes, LhNode{
			ID: nid, Name: b.GetName(), SV: b.GetServerId(), Score: b.GetScore(),
			Rank: int(m.GetRank()), Pos: pos,
			X: ax / LuanHoiWorldW, Y: ay / LuanHoiWorldH,
			CI:     int(nid % 12),
			Color:  LuanHoiColorHex(nid),
			HP:     m.GetReduceBlood(),
			Shield: m.GetShieldTriggerTime() > 0 && nowMilli-m.GetShieldTriggerTime() < 3600*1000,
			Defeat: m.GetHasDefeated(),
			Self:   selfUnion != 0 && nid == selfUnion,
			Beaten: m.GetLastBeAttackTime(),
			Atk:    byTarget[nid],
		})
	}
	return &StreamMap{Which: StreamLuanHoi, Aspect: float64(LuanHoiWorldW) / float64(LuanHoiWorldH), Cities: nil, Lh: f}
}

// luanHoiStatic ships the fixed anchor dots (id = bracket position) so the
// events card can paint the board frame before any live node arrives.
func luanHoiStatic() (*StaticMap, error) {
	sm := &StaticMap{
		Which: StreamLuanHoi, Aspect: float64(LuanHoiWorldW) / float64(LuanHoiWorldH),
		WorldW: LuanHoiWorldW, WorldH: LuanHoiWorldH,
	}
	for i, a := range LuanHoiAnchors {
		sm.Cities = append(sm.Cities, StaticCity{
			ID: uint64(i + 1), X: a[0] / LuanHoiWorldW, Y: a[1] / LuanHoiWorldH,
		})
	}
	return sm, nil
}
