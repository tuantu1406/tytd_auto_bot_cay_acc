package tytd_go

// Đỉnh Bắc Minh page-bake mirror of kunlun_pagebake_test.go: the /bacminh
// page is generated from kunlun.html verbatim, so the SAME bake formulas run
// on the north scene bundle (col=119, box 2.4× larger). Emits
// /tmp/bacminh_pagebake.png for a desktop look — if tints/plates land off
// the roads HERE, the page math is wrong on the phone too. Live tints come
// from a SYNTHETIC cell fixture (no north live bin file is shipped); the
// numeric assertions below pin the geometry invariants so the test is
// meaningful even without a human looking at the PNG.

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

func TestBacMinhPageBake(t *testing.T) {
	scene := gimap.BacMinhSceneJSON()
	if len(scene) == 0 {
		t.Fatal("no north scene json")
	}
	var sc struct {
		HW    float64            `json:"hw"`
		HH    float64            `json:"hh"`
		Box   [4]float64         `json:"box"`
		Col   int                `json:"col"`
		Row   int                `json:"row"`
		Base  [3]int             `json:"base"`
		Void  [3]int             `json:"void"`
		Grid  string             `json:"grid"`
		Rects map[string][4]int  `json:"rects"`
	}
	if json.Unmarshal(scene, &sc) != nil || sc.Col != 119 || sc.Row != 119 || len(sc.Grid) != 119*119 {
		t.Fatalf("north scene json: col=%d", sc.Col)
	}
	hw, hh := sc.HW, sc.HH
	k := math.Min(1, 5000/sc.Box[2])
	k = math.Min(k, math.Sqrt(10500000/(sc.Box[2]*sc.Box[3]))) // KU_STATIC_AREA cap, same as the page
	W := int(math.Round(sc.Box[2] * k))
	H := int(math.Round(sc.Box[3] * k))
	if W <= 0 || H <= 0 {
		t.Fatalf("bake size %dx%d", W, H)
	}
	t.Logf("bake %dx%d k=%.3f", W, H, k)
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			img.Set(x, y, color.RGBA{byte(sc.Void[0]), byte(sc.Void[1]), byte(sc.Void[2]), 255})
		}
	}
	kuTC := func(tx, ty float64) (float64, float64) {
		return (tx-ty)*hw*k + float64(sc.Col-1)*hw*k - sc.Box[0]*k,
			((tx+ty)*hh - sc.Box[1]) * k
	}
	kuCC := func(tx, ty float64) (float64, float64) {
		x, y := kuTC(tx, ty)
		return x, y + hh*k
	}
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
	sheetB := gimap.BmSheetPNG()
	if len(sheetB) == 0 {
		t.Skip("no north sheet")
	}
	sh, err := png.Decode(bytes.NewReader(sheetB))
	if err != nil {
		t.Fatal(err)
	}
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
		top := y*k - hk
		if !raised {
			top = y*k + hh*k - hk/2
		}
		x0 := x*k - wk/2
		for py := 0; py < int(hk); py++ {
			for px := 0; px < int(wk); px++ {
				sx := r[0] + int(float64(r[2])*float64(px)/wk)
				sy := r[1] + int(float64(r[3])*float64(py)/hk)
				if sx >= sh.Bounds().Dx() || sy >= sh.Bounds().Dy() {
					continue
				}
				c := sh.At(sx, sy)
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
	if len(layers.Ground) < 5000 {
		t.Fatalf("north ground layer %d — scene export drifted", len(layers.Ground))
	}
	for _, sp := range layers.Ground {
		blit(sp[:], 255, false)
	}
	// Synthetic live cells on known gid-1 tiles (north live bins are not
	// shipped): the tint pass must land on the plate grid like kunlun's.
	rsp := &proto.KunlunWarMiniMapInfoRespMsg{
		CellInfo: []*proto.KunlunWarMiniCellInfo{
			{Index: pb.Int32(59*119 + 10), UnionId: pb.Int64(303)},
			{Index: pb.Int32(60*119 + 11), UnionId: pb.Int64(303)},
		},
	}
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
			blit(sp[:], 37, true)
		} else {
			blit(sp[:], 255, true)
		}
	}
	// Geometry pins (numeric, not visual): the centre castle (60,60) must
	// project INSIDE the bake near the middle; a corner tile must not.
	cx, cy := kuCC(60, 60)
	if cx < 0 || cx >= float64(W) || cy < 0 || cy >= float64(H) {
		t.Fatalf("centre city (%f,%f) outside bake %dx%d — box/anchor drifted", cx, cy, W, H)
	}
	if dx := math.Abs(cx - float64(W)/2); dx > sc.Box[2]*k*0.25 {
		t.Fatalf("centre city x=%f, W/2=%f — off by %f (>25%% of bake)", cx, float64(W)/2, dx)
	}
	f, _ := os.Create("/tmp/bacminh_pagebake.png")
	_ = png.Encode(f, img)
	_ = f.Close()
	t.Log("wrote /tmp/bacminh_pagebake.png")
}
