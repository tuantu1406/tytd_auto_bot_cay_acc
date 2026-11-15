package gimap

// BG-pad regression pin (owner review 2026-10-10): the client mapOffset
// (left 100 / top 200) rides the bg grid exactly like the city items —
// drawing the grid unpadded shifted every house ~1.5 tiles off its island
// (same "layer lệch" class as the old Côn Lôn bug). At scale 1 the strip
// above/left of the padded grid must be the flat canvas fill, while one
// pixel inside tile 1 must be painted art.

import (
	"testing")

func TestHLOffsetProbe(t *testing.T) {
	m, err := loadHolyLandMap()
	if err != nil {
		t.Fatal(err)
	}
	ttf, err := loadFont()
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := DecodeHolyLand(hlSamplePayload(t))
	if err != nil {
		t.Fatal(err)
	}
	img, err := renderHolyLand(m, overlay, ttf, int(hlWorldW))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(50, 100).RGBA()
	if r>>8 != 8 || g>>8 != 18 || b>>8 != 35 {
		t.Fatalf("(50,100) must be canvas fill outside the padded bg grid: %d,%d,%d", r>>8, g>>8, b>>8)
	}
	r2, g2, b2, _ := img.At(200, 300).RGBA() // inside tile 1 (starts at 100,200)
	if r2>>8 == 8 && g2>>8 == 18 && b2>>8 == 35 {
		t.Fatal("(200,300) must be painted tile 1 — bg pad broken")
	}
}
