package tytd_go

import (
	"encoding/hex"
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// TestPushOrderFrameBytes pins the client frame the owner captured
// (2026-10-18): 71AB 0000 0016 | 000397FB (235515 ElixirPushOrderReq) |
// uid | PAYLOAD 08 01 10 00 = {type:1, index:0}. Our elixirSubmitOrders
// must emit EXACTLY these bytes — the client's own "hoàn thành nhiệm vụ"
// (đơn hàng Dược Tiên) request, verbatim.
func TestPushOrderFrameBytes(t *testing.T) {
	b, err := pb.Marshal(&proto.ElixirPushOrderReq{Type: pb.Int32(1), Index: pb.Int32(0)})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(b); got != "08011000" {
		t.Fatalf("payload = %s, want 08011000 (client verbatim)", got)
	}
}

// TestDelPlantFrameBytes pins the client frame the owner captured
// (2026-10-18): 71AB 0000 0016 | 00039811 (235537 ElixirDelPlantGrassReq) |
// uid | PAYLOAD 08 09 10 00 = {landId:[9], oneKey:false} — the client's own
// cuốc (铲) on ONE explicit lot, with the oneKey flag written EXPLICITLY as
// false. Our elixirDelLots must emit exactly these bytes (the old code
// omitted the flag; wire-equivalent but no longer byte-identical to the
// client). oneKey=true stays privilege-gated — TestElixirClearHarvestedLots
// fails the suite if any DEL ever carries it.
func TestDelPlantFrameBytes(t *testing.T) {
	b, err := pb.Marshal(&proto.ElixirDelPlantGrassReq{LandId: []int32{9}, OneKey: pb.Bool(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(b); got != "08091000" {
		t.Fatalf("payload = %s, want 08091000 (client verbatim)", got)
	}
	// BATCH: elixirDelLots sends up to elixirBatch landIds in ONE packet
	// (owner 2026-10-19: a mid-growth lot cuốc's fine too, so the sweep
	// batches them). All ids stay NON-packed (08..) AND the explicit
	// oneKey=false (10 00) is still emitted after the last id — proving var2
	// never gets dropped when the batch grows. Omitting OneKey would give
	// 08090a (no 10 00) — that must never happen in the live path.
	m, err := pb.Marshal(&proto.ElixirDelPlantGrassReq{LandId: []int32{9, 10}, OneKey: pb.Bool(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(m); got != "0809080a1000" {
		t.Fatalf("batch payload = %s, want 0809080a1000 (non-packed ids + var2=0)", got)
	}
}
