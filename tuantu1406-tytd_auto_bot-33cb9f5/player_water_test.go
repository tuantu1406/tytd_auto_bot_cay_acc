package tytd_go

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"
)

// Water windows are defined in VN time; the dev host runs UTC, so each
// boundary is checked with both an ICT-stamped and a UTC-stamped instant.
func TestElixirWindow(t *testing.T) {
	utcAt := func(h, m int) time.Time {
		return time.Date(2026, 9, 28, h, m, 0, 0, time.UTC)
	}
	vnAt := func(h, m int) time.Time {
		return time.Date(2026, 9, 28, h, m, 0, 0, ictZone)
	}
	cases := []struct {
		name string
		now  time.Time
		want int
	}{
		{"vn before first", vnAt(10, 59), -1},
		{"vn first start", vnAt(11, 0), 0},
		{"vn first end", vnAt(13, 59), 0},
		{"vn first closed", vnAt(14, 0), -1},
		{"vn gap", vnAt(16, 59), -1},
		{"vn second start", vnAt(17, 0), 1},
		{"vn second end", vnAt(20, 59), 1},
		{"vn second closed", vnAt(21, 0), -1},
		{"vn midnight", vnAt(0, 0), -1},
		// ICT = UTC+7: 11:00 ICT is 04:00 UTC, 21:00 ICT is 14:00 UTC.
		{"utc just before first", utcAt(3, 59), -1},
		{"utc first start", utcAt(4, 0), 0},
		{"utc first closed", utcAt(7, 0), -1},
		{"utc second start", utcAt(10, 0), 1},
		{"utc just before second closed", utcAt(13, 59), 1},
		{"utc second closed", utcAt(14, 0), -1},
	}
	for _, c := range cases {
		if got := elixirWindow(c.now); got != c.want {
			t.Errorf("%s: elixirWindow = %d, want %d", c.name, got, c.want)
		}
	}
}

// elixirDrain empties the write channel without checking frames.
func (p *Player) elixirDrain() {
	for {
		select {
		case <-p.writeChan:
		default:
			return
		}
	}
}

// Once per window per ICT day; the day rollover re-arms both slots. The
// first claim must equal the captured client request byte for byte.
func TestElixirClaimTick(t *testing.T) {
	p := &Player{Username: "u", UID: 0x000000108B4B5992, writeChan: make(chan []byte, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	t.Cleanup(cancel)

	var claimed [2]bool
	var claimedDay string
	vn := func(day, h, m int) time.Time {
		return time.Date(2026, 9, day, h, m, 0, 0, ictZone)
	}

	// Outside any window: quiet.
	if p.elixirClaimTick(vn(28, 10, 0), &claimed, &claimedDay) {
		t.Fatal("claimed outside window")
	}

	// 11:30 ICT day 28: first claim, exact wire match against the capture.
	if !p.elixirClaimTick(vn(28, 11, 30), &claimed, &claimedDay) {
		t.Fatal("no claim inside first window")
	}
	want, _ := hex.DecodeString("71ab0000001600039810000000108b4b599208001001")
	got := <-p.writeChan
	if !bytes.Equal(got, want) {
		t.Fatalf("frame = %s, want %s", hex.EncodeToString(got), hex.EncodeToString(want))
	}
	if ft := frameFuncType(t, got); ft != S_ELIXIR_WATER {
		t.Fatalf("funcType = %s, want %s", ft, S_ELIXIR_WATER)
	}

	// Re-fire inside the same window: nothing.
	if p.elixirClaimTick(vn(28, 12, 0), &claimed, &claimedDay) {
		t.Fatal("claimed twice in one window")
	}
	// 18:00 ICT same day: second slot claims — body must carry serving 2
	// (08 00 10 02), verified against the evening client capture.
	if !p.elixirClaimTick(vn(28, 18, 0), &claimed, &claimedDay) {
		t.Fatal("second slot did not claim")
	}
	wantPM, _ := hex.DecodeString("71ab0000001600039810000000108b4b599208001002")
	if got := <-p.writeChan; !bytes.Equal(got, wantPM) {
		t.Fatalf("evening frame = %s, want %s", hex.EncodeToString(got), hex.EncodeToString(wantPM))
	}
	p.elixirDrain()
	if p.elixirClaimTick(vn(28, 19, 0), &claimed, &claimedDay) {
		t.Fatal("claimed twice in second window")
	}
	// Next ICT day at 12:00: first slot re-arms.
	if !p.elixirClaimTick(vn(29, 12, 0), &claimed, &claimedDay) {
		t.Fatal("new day did not re-arm first slot")
	}
	if claimedDay != "2026-09-29" {
		t.Fatalf("day key = %q, want 2026-09-29", claimedDay)
	}
}
