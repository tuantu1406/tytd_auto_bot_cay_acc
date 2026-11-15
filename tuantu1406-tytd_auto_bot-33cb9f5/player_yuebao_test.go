package tytd_go

// ybPickAmount: deposit the amount THIS account actually runs, read from
// its own ENTER history — not a blind 2200 for everyone (owner 2026-10-09).

import (
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func TestYbPickAmount(t *testing.T) {
	rec := func(ts int64, kind int32, amount int32) *proto.YueBaoRecord {
		return &proto.YueBaoRecord{Ts: pb.Int64(ts), Kind: pb.Int32(kind), Amount: pb.Int32(amount)}
	}
	cases := []struct {
		name string
		recs []*proto.YueBaoRecord
		want int32
	}{
		{"empty history → list price", nil, 2200},
		{"single plan", []*proto.YueBaoRecord{rec(100, 1, 1000)}, 1000},
		{"newest open wins", []*proto.YueBaoRecord{rec(300, 1, 500), rec(900, 1, 1500), rec(600, 1, 800)}, 1500},
		{"collect/withdraw kinds skipped", []*proto.YueBaoRecord{rec(900, 2, 700), rec(800, 3, 700), rec(500, 1, 1200)}, 1200},
		{"zero-amount opens ignored", []*proto.YueBaoRecord{rec(900, 1, 0), rec(800, 1, 600)}, 600},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ybPickAmount(c.recs); got != c.want {
				t.Fatalf("got %d want %d", got, c.want)
			}
		})
	}
}
