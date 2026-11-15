package tytd_go

// Faithful port of the /kunlun PAGE bake (webtmpl/kunlun.html kuBake+kuTints
// + kuTC/kuFitK/kuDrawSprite/kuDiamond) so the alignment can be looked at on
// a desktop — phone canvas pixels are otherwise unverifiable here.
// Emits /tmp/pagebake.png: ground bake + live cell tints in the exact page
// formulas. If tints land off the roads HERE, the page math is wrong.

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"testing"
	"tytd_go/internal/gimap"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func TestPageBakeMatchesGame(t *testing.T) {
	scene := gimap.KunlunSceneJSON()
	var sc struct {
		HW float64        `json:"hw"`
		HH float64        `json:"hh"`
		Box    [4]float64     `json:"box"`
		Col    int            `json:"col"`
		Row    int            `json:"row"`
		Base   [3]int         `json:"base"`
		Void   [3]int         `json:"void"`
		Grid   string         `json:"grid"`
		Rects  map[string][4]int `json:"rects"`
	}
	if json.Unmarshal(scene, &sc) != nil || sc.Col == 0 {
		t.Fatal("scene json")
	}
	hw, hh := sc.HW, sc.HH
	k := math.Min(1, 5000/sc.Box[2])
	k = math.Min(k, math.Sqrt(10500000/(sc.Box[2]*sc.Box[3])))
	W := int(math.Round(sc.Box[2] * k))
	H := int(math.Round(sc.Box[3] * k))
	t.Logf("bake %dx%d k=%.3f", W, H, k)
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			img.Set(x, y, color.RGBA{byte(sc.Void[0]), byte(sc.Void[1]), byte(sc.Void[2]), 255})
		}
	}
	// kuTC = tile ANCHOR (diamond TOP vertex); kuCC = cell centre (anchor + hh).
	kuTC := func(tx, ty float64) (float64, float64) {
		return (tx - ty) * hw * k + float64(sc.Col-1)*hw*k - sc.Box[0]*k,
			((tx + ty) * hh - sc.Box[1]) * k
	}
	kuCC := func(tx, ty float64) (float64, float64) {
		x, y := kuTC(tx, ty)
		return x, y + hh*k
	}
	// Blend, don't overwrite: the page paints these diamonds with globalAlpha
	// fills, so an img.Set here (which just stores the half-transparent
	// colour) would mask exactly the layer alignment being verified.
	diamond := func(cx, cy, dhw, dhh float64, col color.RGBA) {
		n := uint32(col.A)
		for dy := -dhh; dy <= dhh; dy++ {
			xw := dhw * (1 - math.Abs(dy)/dhh)
			for dx := -xw; dx <= xw; dx++ {
				x, y := int(cx+dx), int(cy+dy)
				if x < 0 || y < 0 || x >= W || y >= H {
					continue
				}
				dr, dg, db, _ := img.At(x, y).RGBA()
				img.Set(x, y, color.RGBA{
					R: uint8((uint32(col.R)*n + (dr>>8)*(255-n)) / 255),
					G: uint8((uint32(col.G)*n + (dg>>8)*(255-n)) / 255),
					B: uint8((uint32(col.B)*n + (db>>8)*(255-n)) / 255),
					A: 255,
				})
			}
		}
	}
	for ty := 0; ty < sc.Row; ty++ {
		for tx := 0; tx < sc.Col; tx++ {
			g := sc.Grid[ty*sc.Col+tx] - '0'
			if g != 1 && g != 2 && g != 4 {
				continue
			}
			px, py := kuCC(float64(tx), float64(ty))
			diamond(px, py, hw*k, hh*k, color.RGBA{byte(sc.Base[0]), byte(sc.Base[1]), byte(sc.Base[2]), 255})
		}
	}
	sheetB := gimap.KuSheetPNG()
	if len(sheetB) == 0 {
		t.Skip("no sheet")
	}
	sh, err := png.Decode(bytes.NewReader(sheetB))
	if err != nil {
		t.Fatal(err)
	}
	shr := sh
	blit := func(sp []json.RawMessage, alpha uint8, raised bool) {
		if len(sp) < 6 {
			return
		}
		var x, y, w, h, fl float64
		var name string
		_ = json.Unmarshal(sp[0], &x)
		_ = json.Unmarshal(sp[1], &y)
		_ = json.Unmarshal(sp[2], &w)
		_ = json.Unmarshal(sp[3], &h)
		_ = json.Unmarshal(sp[4], &name)
		_ = json.Unmarshal(sp[5], &fl)
		r, ok := sc.Rects[name]
		if !ok || r[2] <= 0 || r[3] <= 0 {
			return
		}
		wk, hk := w*k, h*k
		// client rule: raised plates hang from the anchor (bottom edge ON y);
		// ground plates centre on the cell (anchor + hh).
		top := y*k - hk
		if !raised {
			top = y*k + hh*k - hk/2
		}
		x0 := x*k - wk/2
		for py := 0; py < int(hk); py++ {
			for px := 0; px < int(wk); px++ {
				sx := r[0] + int(float64(r[2])*float64(px)/wk)
				sy := r[1] + int(float64(r[3])*float64(py)/hk)
				c := shr.At(sx, sy)
				cc := color.NRGBAModel.Convert(c).(color.NRGBA)
				if cc.A == 0 {
					continue
				}
				tx := int(x0) + px
				ty := int(top) + py
				if tx < 0 || ty < 0 || tx >= W || ty >= H {
					continue
				}
				n := uint32(cc.A) * uint32(alpha) / 255
				dr, dg, db, _ := img.At(tx, ty).RGBA()
				// NRGBA (unpremultiplied) channels directly — cc.RGBA() hands
				// back 16-bit PREMULIPLIED values and the /255 blend below
				// truncates them to rainbow noise (old test bug).
				sr, sg, sb := uint32(cc.R), uint32(cc.G), uint32(cc.B)
				img.Set(tx, ty, color.RGBA{
					R: uint8((sr*n + (dr>>8)*(255-n)) / 255),
					G: uint8((sg*n + (dg>>8)*(255-n)) / 255),
					B: uint8((sb*n + (db>>8)*(255-n)) / 255),
					A: 255,
				})
			}
		}
	}
	var layers struct {
		Ground [][6]json.RawMessage `json:"ground"`
		Deco   [][6]json.RawMessage `json:"deco"`
	}
	_ = json.Unmarshal(mustField(t, scene, "layers"), &layers)
	for _, sp := range layers.Ground {
		blit(sp[:], 255, false) // ground: centred on the cell
	}
	bin, err := os.ReadFile("/tmp/kunlun_live_mini.bin")
	if err != nil {
		t.Skip("no live bin")
	}
	var rsp proto.KunlunWarMiniMapInfoRespMsg
	if pb.Unmarshal(bin, &rsp) != nil {
		t.Fatal("bin")
	}
	// Tints wash the ground and sit UNDER the raised plates — the page paints
	// them on the live canvas between the two bake passes (kuBake → kuTints).
	green := color.RGBA{0x3f, 0xd8, 0x73, 150}
	for _, cl := range rsp.GetCellInfo() {
		if cl.GetUnionId() == 0 {
			continue
		}
		i := float64(cl.GetIndex())
		tx := math.Mod(i, float64(sc.Col))
		ty := math.Floor(i / float64(sc.Col))
		px, py := kuCC(tx, ty)
		diamond(px, py, hw*k, hh*k, green)
	}
	for _, sp := range layers.Deco {
		var fl float64
		_ = json.Unmarshal(sp[5], &fl)
		if int(fl)&4 != 0 {
			blit(sp[:], 37, true) // faded decor: raised plate
		} else {
			blit(sp[:], 255, true) // city plate: raised
		}
	}
	f, _ := os.Create("/tmp/pagebake.png")
	_ = png.Encode(f, img)
	_ = f.Close()
	t.Log("wrote /tmp/pagebake.png")
}

func mustField(t *testing.T, scene []byte, field string) []byte {
	t.Helper()
	var v map[string]json.RawMessage
	if json.Unmarshal(scene, &v) != nil {
		t.Fatal("scene")
	}
	return v[field]
}
