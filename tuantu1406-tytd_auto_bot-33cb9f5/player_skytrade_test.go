package tytd_go

import (
	"fmt"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// SkyTrade auto-report pins: the wire constants derived from the decimal
// ids, the request frame, and the pure login/start-time planner.

func TestSkyTradeWireIDs(t *testing.T) {
	cases := []struct {
		id        int32
		cmd       string
		transport string
	}{
		{215000, SKY_TRADE_ENTER_CMD, SERVER_SKY_TRADE_ENTER},
		{215001, SKY_TRADE_ENTER_MAP_CMD, SERVER_SKY_TRADE_ENTER_MAP},
		{215024, SKY_TRADE_REPORT_GOODS_CMD, SERVER_SKY_TRADE_REPORT_GOODS},
	}
	for _, c := range cases {
		if got := fmt.Sprintf("%08x", c.id); got != c.cmd {
			t.Errorf("cmd for %d: got %s want %s", c.id, got, c.cmd)
		}
		if got := fmt.Sprintf("%x", c.id%100000); got != c.transport {
			t.Errorf("transport for %d: got %s want %s", c.id, got, c.transport)
		}
	}
}

func TestSkyTradeReqFrame(t *testing.T) {
	b, err := pb.Marshal(&proto.SkyTradeActivityReq{ActivityId: pb.Int32(9061892)})
	if err != nil {
		t.Fatal(err)
	}
	var req proto.SkyTradeActivityReq
	if err := pb.Unmarshal(b, &req); err != nil || req.GetActivityId() != 9061892 {
		t.Fatalf("round-trip wrong: %v %d", err, req.GetActivityId())
	}
}

func TestSkyTradePlan(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	idle := now.Add(skyTradeIdleRecheck)
	e := func(id int32, begin, end int64) activityEntry {
		return activityEntry{ActivityID: id, BeginTime: begin, EndTime: end}
	}

	// Active and not yet reported → report; nothing else pending → idle wake.
	rep, wake := skyTradePlan([]activityEntry{e(500, 900_000, 2_000_000)}, now, nil)
	if len(rep) != 1 || rep[0] != 500 || !wake.Equal(idle) {
		t.Fatalf("active: %v %v", rep, wake)
	}

	// Reported this login → quiet.
	rep, _ = skyTradePlan([]activityEntry{e(500, 900_000, 2_000_000)}, now, map[int32]bool{500: true})
	if len(rep) != 0 {
		t.Fatalf("done entry re-reported: %v", rep)
	}

	// Future start (show phase or not yet listed-early) → precise wake, no report.
	rep, wake = skyTradePlan([]activityEntry{e(600, 1_200_000, 9_000_000)}, now, nil)
	if len(rep) != 0 || !wake.Equal(time.UnixMilli(1_200_000)) {
		t.Fatalf("future: %v %v", rep, wake)
	}

	// Reward phase (now ≥ endTime) and long-past entries report nothing and
	// do not schedule a wake.
	rep, wake = skyTradePlan([]activityEntry{
		e(700, 500_000, 1_000_000), // ends exactly now → reward side, over
		e(800, 1, 2),
	}, now, nil)
	if len(rep) != 0 || !wake.Equal(idle) {
		t.Fatalf("past: %v %v", rep, wake)
	}

	// Mixed: all active-unreported reported; wake pinned to the EARLIEST
	// future beginTime.
	rep, wake = skyTradePlan([]activityEntry{
		e(500, 900_000, 2_000_000),
		e(501, 950_000, 3_000_000),
		e(600, 1_100_000, 9_000_000),
		e(601, 1_050_000, 9_000_000),
		e(700, 1, 2),
	}, now, map[int32]bool{501: true})
	if len(rep) != 1 || rep[0] != 500 {
		t.Fatalf("mixed reports: %v", rep)
	}
	if !wake.Equal(time.UnixMilli(1_050_000)) {
		t.Fatalf("mixed wake: %v", wake)
	}
}
