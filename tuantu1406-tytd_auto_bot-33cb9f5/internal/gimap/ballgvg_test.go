package gimap

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func ballGVGTestMember(camp, num uint64) []byte {
	b := protowire.AppendTag(nil, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, camp)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	return protowire.AppendVarint(b, num)
}

func ballGVGTestPlace(id, owner uint64, members ...[]byte) []byte {
	b := protowire.AppendTag(nil, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, id)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, owner)
	for _, member := range members {
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendBytes(b, member)
	}
	return b
}

func ballGVGTestResponse(ret uint64, places ...[]byte) []byte {
	b := protowire.AppendTag(nil, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, ret)
	for _, place := range places {
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendBytes(b, place)
	}
	return b
}

func TestDecodeBallGVGPlaces(t *testing.T) {
	payload := ballGVGTestResponse(0,
		ballGVGTestPlace(7, 2, ballGVGTestMember(2, 4), ballGVGTestMember(1, 1)),
		ballGVGTestPlace(2, 0),
	)
	places, err := DecodeBallGVGPlaces(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 2 || places[0].PlaceID != 2 || places[1].PlaceID != 7 {
		t.Fatalf("places not sorted/decoded: %+v", places)
	}
	got := places[1]
	if got.SeizeCampID != 2 || len(got.CampMembers) != 2 || got.CampMembers[0].CampID != 1 || got.CampMembers[1].Num != 4 {
		t.Fatalf("room state mismatch: %+v", got)
	}
}

func TestRenderBallGVG(t *testing.T) {
	var places [][]byte
	for id := uint64(1); id <= 13; id++ {
		places = append(places, ballGVGTestPlace(id, (id-1)%3+1,
			ballGVGTestMember(1, id%4), ballGVGTestMember(2, (id+1)%4), ballGVGTestMember(3, (id+2)%4)))
	}
	img, err := RenderBallGVG(ballGVGTestResponse(0, places...), 720)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(img, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("not a PNG: %d bytes", len(img))
	}
}

func TestDecodeBallGVGPlacesRejectsRet(t *testing.T) {
	if _, err := DecodeBallGVGPlaces(ballGVGTestResponse(9)); err == nil {
		t.Fatal("expected non-zero ret error")
	}
}
