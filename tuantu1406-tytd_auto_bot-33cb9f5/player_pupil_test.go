package tytd_go

import (
	"bytes"
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// index is `optional` precisely so the train-all-seats call carries an
// explicit zero (08 00) instead of proto3 dropping the field entirely.
func TestPupilTrainAllSeatsWire(t *testing.T) {
	msg, err := pb.Marshal(&proto.PupilTrainReq{Index: pb.Int32(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(msg, []byte{0x08, 0x00}) {
		t.Fatalf("train-all body = % x, want 08 00", msg)
	}
}
