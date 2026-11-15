package gimap

import (
	"bytes"
	"testing"

	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// TestRenderEndToEnd drives the full pipeline: synthesize a
// GodIslandGameMiniMapInfoRespMsg (owned city with Vietnamese union name,
// contested city, unowned city), fetch the real CDN geometry, render, and
// check we get a PNG back. Skipped when the CDN is unreachable.
func TestRenderEndToEnd(t *testing.T) {
	payload, err := pb.Marshal(&proto.GodIslandGameMiniMapInfoRespMsg{
		Ret: 0,
		CityInfo: []*proto.GodIslandGameMiniCityInfo{
			{
				CityId: pb.Int32(1002),
				BaseData: &proto.GodIslandUnionOwnerInfo{
					UnionId:   pb.Int64(7105620235),
					UnionName: pb.String("Bang thử nghiệm"),
				},
			},
			{CityId: pb.Int32(2003), HaveFight: pb.Bool(true)},
			{CityId: pb.Int32(5001)},
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := LoadMapConfig(); err != nil {
		t.Skipf("CDN unreachable: %v", err)
	}
	img, err := Render(payload, 1000)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.HasPrefix(img, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("output is not a PNG (%d bytes)", len(img))
	}
	t.Logf("rendered PNG: %d bytes", len(img))
}

// TestLayoutAnchorsCities is the end-to-end for the clickable overlay: the
// same payload that produced the PNG must place every requested city inside
// the frame, sorted by id, with the owner/fight flags carried through, and a
// hit radius that never collapses to zero. Skipped when the CDN is
// unreachable (same as TestRenderEndToEnd).
func TestLayoutAnchorsCities(t *testing.T) {
	payload, err := pb.Marshal(&proto.GodIslandGameMiniMapInfoRespMsg{
		Ret: 0,
		CityInfo: []*proto.GodIslandGameMiniCityInfo{
			{
				CityId: pb.Int32(1002),
				BaseData: &proto.GodIslandUnionOwnerInfo{
					UnionId:   pb.Int64(7105620235),
					UnionName: pb.String("Bang thử nghiệm"),
				},
			},
			{CityId: pb.Int32(2003), HaveFight: pb.Bool(true)},
			{CityId: pb.Int32(5001)},
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := LoadMapConfig(); err != nil {
		t.Skipf("CDN unreachable: %v", err)
	}
	lay, err := Layout(payload, 1400, false)
	if err != nil {
		t.Fatalf("Layout: %v", err)
	}
	if lay.Width != 1400 || lay.Height <= 0 {
		t.Fatalf("frame %dx%d, want width 1400 and positive height", lay.Width, lay.Height)
	}
	seen := map[uint64]CityPoint{}
	for i, c := range lay.Cities {
		if i > 0 && c.ID <= lay.Cities[i-1].ID {
			t.Fatalf("cities not sorted by id at %d: %d after %d", i, c.ID, lay.Cities[i-1].ID)
		}
		if c.X < 0 || c.X > lay.Width || c.Y < 0 || c.Y > lay.Height {
			t.Fatalf("city %d at (%d,%d) outside %dx%d", c.ID, c.X, c.Y, lay.Width, lay.Height)
		}
		if c.R < 1 {
			t.Fatalf("city %d radius %d, want >= 1", c.ID, c.R)
		}
		seen[c.ID] = c
	}
	for _, id := range []uint64{1002, 2003, 5001} {
		if _, ok := seen[id]; !ok {
			t.Fatalf("city %d missing from layout (CDN geometry changed?)", id)
		}
	}
	if got := seen[1002].Owner; got != "Bang thử nghiệm" {
		t.Fatalf("city 1002 owner %q", got)
	}
	if !seen[2003].Fight {
		t.Fatal("city 2003 lost its fight flag")
	}
	if seen[5001].Owner != "" || seen[5001].Fight {
		t.Fatalf("unowned city 5001 carries state: %+v", seen[5001])
	}
}

func TestDecodeCitiesRejectsErrorRet(t *testing.T) {
	payload, err := pb.Marshal(&proto.GodIslandGameMiniMapInfoRespMsg{Ret: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCities(payload); err == nil {
		t.Fatal("expected error for non-zero ret")
	}
}
