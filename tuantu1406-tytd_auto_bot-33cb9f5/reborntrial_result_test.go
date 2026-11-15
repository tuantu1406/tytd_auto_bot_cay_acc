package tytd_go

import (
	"strings"
	"testing"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// The "+0 điểm" complaint (owner 2026-10-11): the server answers an
// exhausted-account challenge with ret=0 and all-zero results, and the old
// confirmation blamed it on the target. rbtResultLine must tell the three
// cases apart.
func TestRbtResultLine(t *testing.T) {
	mk := func(defeat int32, add, red int64, rewards string) *proto.RebornTrialChallengeResp {
		r := &proto.RebornTrialChallengeResp{}
		r.DefeatNum = pb.Int32(defeat)
		r.AddScore = pb.Int64(add)
		r.ReduceScore = pb.Int64(red)
		if rewards != "" {
			r.Rewards = pb.String(rewards)
		}
		return r
	}
	win := rbtResultLine("🕸ĐộngBànTơ", mk(3, 2100, 0, ""))
	if !strings.Contains(win, "hạ 3 lính") || !strings.Contains(win, "+2100") {
		t.Fatalf("win line wrong: %q", win)
	}
	if strings.Contains(win, "⚠️") {
		t.Fatalf("win flagged as empty: %q", win)
	}
	empty := rbtResultLine("X", mk(0, 0, 0, ""))
	if !strings.Contains(empty, "không hạ nổi") {
		t.Fatalf("all-zero line must explain the ±0: %q", empty)
	}
	lost := rbtResultLine("Y", mk(1, 500, 300, ""))
	if !strings.Contains(lost, "trừ 300") {
		t.Fatalf("reduceScore must be shown: %q", lost)
	}
}
