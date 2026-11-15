package tytd_go

import (
	"bytes"
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// The paced claim is the same wire call the old login burst used:
// ReqUnionGetDailyTask{ActIndex:4} is byte-identical to PhanThan{Id:4}.
// Only the cadence changed.
func TestUnionDailyWireCompat(t *testing.T) {
	if S_BANG_SOINOI_INFO != "00005665" || S_BANG_NHANQUA_SOINOI != "00005666" {
		t.Fatalf("cmd constants moved: %s %s", S_BANG_SOINOI_INFO, S_BANG_NHANQUA_SOINOI)
	}
	if SERVER_BANG_NHANQUA_SOINOI != "0846" {
		t.Fatalf("transport mapping wrong: %s", SERVER_BANG_NHANQUA_SOINOI)
	}
	var old proto.PhanThan
	old.Id = 4
	oldMSG, err := pb.Marshal(&old)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unionDailyClaimMSG(), oldMSG) {
		t.Fatalf("paced claim %x != old blind body %x", unionDailyClaimMSG(), oldMSG)
	}
}

// Handler must parse both answers without touching the wire (the loop
// owns all sends) and never panic on the refused path.
func TestUnionDailyHandleClaim(t *testing.T) {
	p := &Player{Username: "u", UID: 42}
	refused, _ := pb.Marshal(&proto.RspUnionGetDailyTask{Ret: pb.Int32(5)})
	ok, _ := pb.Marshal(&proto.RspUnionGetDailyTask{Ret: pb.Int32(0), Reward: pb.String("100025=2")})
	p.unionDailyHandleClaim(refused)
	p.unionDailyHandleClaim(refused) // dedup path
	p.unionDailyHandleClaim(ok)
	p.unionDailyHandleClaim([]byte{0xff, 0xff}) // garbage: ignored
	if len(p.writeChan) != 0 {
		t.Fatalf("handler wrote %d frames", len(p.writeChan))
	}
}
