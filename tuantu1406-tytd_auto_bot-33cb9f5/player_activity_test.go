package tytd_go

import (
	"context"
	"sync"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Activity store pins: full vs incremental push semantics, the presence-only
// merge from 21003 answers, the client's phase windows, and the poll frame.

func actCfg(id, typ int32) *proto.ActivityMainConfig {
	return &proto.ActivityMainConfig{ActivityId: pb.Int32(id), Type: pb.Int32(typ)}
}

func TestActivityStoreApplyList(t *testing.T) {
	full, err := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{
		actCfg(300, 18), actCfg(100, 14), actCfg(200, 68),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var s activityStore
	s.applyList("t", full, true)
	snap := s.snapshot()
	if len(snap) != 3 || snap[0].ActivityID != 100 || snap[2].ActivityID != 300 {
		t.Fatalf("full list wrong: %+v", snap)
	}

	// Incremental: upserts only — 200 updates, 400 joins, 100 survives.
	inc, _ := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{
		actCfg(200, 333), actCfg(400, 346),
	}})
	s.applyList("t", inc, false)
	snap = s.snapshot()
	if len(snap) != 4 {
		t.Fatalf("incremental must upsert, not replace: %+v", snap)
	}
	if e, _ := s.entries[200]; e.Type != 333 {
		t.Fatalf("upsert did not refresh 200: %+v", e)
	}

	// A full list replaces: withdrawn activities disappear.
	full2, _ := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{actCfg(100, 14)}})
	s.applyList("t", full2, true)
	if snap = s.snapshot(); len(snap) != 1 || snap[0].ActivityID != 100 {
		t.Fatalf("full replace wrong: %+v", snap)
	}

	// Garbage payload leaves the store untouched.
	s.applyList("t", []byte{0xff, 0xff, 0xff}, true)
	if len(s.snapshot()) != 1 {
		t.Fatal("bad push must not wipe the store")
	}
}

func TestActivityStoreMergeDetail(t *testing.T) {
	var s activityStore
	full, _ := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{{
		ActivityId: pb.Int32(555), Type: pb.Int32(14), BeginTime: pb.Int64(1000), EndTime: pb.Int64(2000),
	}}})
	s.applyList("t", full, true)

	answer := func(id int32, withMC bool) []byte {
		var mc *proto.ActivityMainConfig
		if withMC {
			mc = &proto.ActivityMainConfig{ActivityId: pb.Int32(id)}
			if id == 555 { // refreshed window, type omitted (presence merge)
				mc.EndTime = pb.Int64(3000)
			} else {
				mc.Type = pb.Int32(9)
			}
		}
		b, err := pb.Marshal(&proto.RspGetActivityDetail{
			Ret: 0,
			Activity: &proto.ActivityCommonData{
				ActivityId: pb.Int32(id),
				DetailConfig: &proto.ActivityDetailConfig{
					CommonConfig: &proto.ActivityCommonConfig{MainConfig: mc},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	s.mergeDetail("t", answer(555, true))
	e := s.entries[555]
	if e.EndTime != 3000 || e.BeginTime != 1000 || e.Type != 14 {
		t.Fatalf("presence merge wrong: %+v", e)
	}

	// Unknown id WITH mainConfig: CREATES the row (server-authored answer =
	// the only legal backfill for a missed 21036 push — the main list
	// itself cannot be pulled).
	s.mergeDetail("t", answer(777, true))
	e777, ok := s.entries[777]
	if !ok || e777.Type != 9 {
		t.Fatalf("detail backfill must create the row: ok=%v %+v", ok, e777)
	}
	// No mainConfig for an unknown id: still a no-op (nothing server-authored
	// to create from).
	s.mergeDetail("t", func() []byte {
		b, err := pb.Marshal(&proto.RspGetActivityDetail{
			Ret: 0,
			Activity: &proto.ActivityCommonData{
				ActivityId:   pb.Int32(888),
				DetailConfig: &proto.ActivityDetailConfig{},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}())
	if _, ok := s.entries[888]; ok {
		t.Fatal("answer without mainConfig must not create rows")
	}
	s.mergeDetail("t", answer(555, false)) // no mainConfig: no-op
	s.mergeDetail("t", []byte{0xff})       // garbage: no panic, no change
	if e2 := s.entries[555]; e2.EndTime != 3000 {
		t.Fatalf("noise altered entry: %+v", e2)
	}

	// lastOf remembers the newest id per type across a full push that DROPS
	// it, and missingKnownIDs surfaces it for the poll backfill.
	if id, known := s.lastIDForType(14); !known || id != 555 {
		t.Fatalf("lastOf missed type 14: id=%d known=%v", id, known)
	}
	full2, _ := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{{
		ActivityId: pb.Int32(555), Type: pb.Int32(14), BeginTime: pb.Int64(1000), EndTime: pb.Int64(2000),
	}}})
	s.applyList("t", full2, true) // full push REPLACES: 777 gone, but remembered
	if id, known := s.lastIDForType(9); !known || id != 777 {
		t.Fatalf("lastOf must survive a full push: id=%d known=%v", id, known)
	}
	miss := s.missingKnownIDs()
	if len(miss) != 1 || miss[0] != 777 {
		t.Fatalf("missingKnownIDs wrong: %v", miss)
	}
	s.mergeDetail("t", answer(777, true)) // backfill lands
	if miss = s.missingKnownIDs(); len(miss) != 0 {
		t.Fatalf("backfilled id still missing: %v", miss)
	}
}

func TestActivityEntryStatus(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	e := activityEntry{BeginShowTime: 500_000, BeginTime: 900_000, EndTime: 1_000_000, EndShowTime: 1_500_000}
	if got := e.status(now); got != "reward" { // now == endTime, < endShowTime
		t.Fatalf("at endTime: got %s", got)
	}
	e2 := e
	e2.BeginTime, e2.EndTime = 1_100_000, 1_200_000 // now inside show window only
	if got := e2.status(now); got != "show" {
		t.Fatalf("in show window: got %s", got)
	}
	e3 := e
	e3.BeginShowTime, e3.BeginTime, e3.EndTime, e3.EndShowTime = 800_000, 900_000, 1_100_000, 1_200_000
	if got := e3.status(now); got != "active" {
		t.Fatalf("active: got %s", got)
	}
	e4 := e
	e4.BeginShowTime, e4.BeginTime, e4.EndTime, e4.EndShowTime = 1, 2, 3, 4
	if got := e4.status(now); got != "none" {
		t.Fatalf("long past: got %s", got)
	}
}

func TestActivityPollFrame(t *testing.T) {
	b, err := activityPollFrame(9061892)
	if err != nil {
		t.Fatal(err)
	}
	var req proto.ReqGetActivityDetail
	if err := pb.Unmarshal(b, &req); err != nil {
		t.Fatal(err)
	}
	if req.GetActivityId() != 9061892 || req.GetVersion() != "0" {
		t.Fatalf("poll frame wrong: %d %q", req.GetActivityId(), req.GetVersion())
	}
}

// Event-list poll (owner 2026-10-27 "thêm poll event list"): every
// activityWatchFast tick polls the WATCH-type rows + every vanished-but-
// known id (backfill); foreign rows are only pulled by the full sweep every
// activityPollInterval.
func TestActivityPollLoopWatchesAndBackfills(t *testing.T) {
	of, oi, os := activityWatchFast, activityPollInterval, activityPollStep
	activityWatchFast = 20 * time.Millisecond
	activityPollInterval = 500 * time.Millisecond
	activityPollStep = time.Millisecond
	defer func() {
		activityWatchFast, activityPollInterval, activityPollStep = of, oi, os
	}()

	var s activityStore
	mk := func(cfgs ...*proto.ActivityMainConfig) []byte {
		b, err := pb.Marshal(&proto.PushActivityList{MainConfig: cfgs})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	w150 := &proto.ActivityMainConfig{ActivityId: pb.Int32(900), Type: pb.Int32(150), BeginTime: pb.Int64(1), EndTime: pb.Int64(2)}
	w332 := &proto.ActivityMainConfig{ActivityId: pb.Int32(555), Type: pb.Int32(332), BeginTime: pb.Int64(1), EndTime: pb.Int64(2)}
	s.applyList("t", mk(w150, w332,
		&proto.ActivityMainConfig{ActivityId: pb.Int32(600), Type: pb.Int32(14), BeginTime: pb.Int64(1), EndTime: pb.Int64(2)},
	), true)
	// Day flip drops the 332 row (lastOf[332]=555 remembers it for backfill)
	// and keeps the foreign type-14 row — it must only ride the full sweep.
	s.applyList("t", mk(w150,
		&proto.ActivityMainConfig{ActivityId: pb.Int32(600), Type: pb.Int32(14), BeginTime: pb.Int64(1), EndTime: pb.Int64(2)},
	), true)

	var mu sync.Mutex
	var got []int32
	ctx, cancel := context.WithCancel(context.Background())
	go activityPollLoop("t", ctx, &s, func(id int32) {
		mu.Lock()
		got = append(got, id)
		mu.Unlock()
	})
	time.Sleep(150 * time.Millisecond) // fast ticks only (full sweep is 500ms out)
	mu.Lock()
	have := map[int32]int{}
	for _, id := range got {
		have[id]++
	}
	mu.Unlock()
	if have[900] == 0 {
		t.Fatalf("watch-type row 900 never polled fast: %v", got)
	}
	if have[555] == 0 {
		t.Fatalf("vanished known id 555 never backfilled: %v", got)
	}
	if have[600] != 0 {
		t.Fatalf("foreign row 600 polled before full sweep: %v", got)
	}
	time.Sleep(500 * time.Millisecond) // cross the full-sweep boundary
	mu.Lock()
	for _, id := range got {
		have[id]++
	}
	mu.Unlock()
	cancel()
	if have[600] == 0 {
		t.Fatalf("full sweep missed stored row 600: %v", got)
	}
}
