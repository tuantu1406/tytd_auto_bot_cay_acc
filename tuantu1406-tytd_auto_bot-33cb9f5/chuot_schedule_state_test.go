package tytd_go

// Daily chuot schedule as a TIME RANGE at login (owner request 2026-10-09):
// with "stop 2:00, start 10:00", a login at 02:30 must keep the mouse OFF —
// the crontab-style one-shot marks are now interpreted as a persisted state.

import (
	"testing"
	"time"
)

func ictAt(h, m int) time.Time {
	// Any date works — the helper only reads hour:minute in UTC+7.
	return time.Date(2026, 10, 9, h-7, m, 0, 0, time.UTC) // UTC → +7 = h:m
}

func TestChuotLoginRunRanges(t *testing.T) {
	const off = "2:00"   // stop schedule
	const on = "10:00"   // start schedule
	cases := []struct {
		name       string
		start, stop string
		h, m       int
		want       bool
	}{
		{"02:30 inside off-window", on, off, 2, 30, false},
		{"01:59 before stop", on, off, 1, 59, true},
		{"02:00 exactly stop minute", on, off, 2, 0, false},
		{"09:59 still off", on, off, 9, 59, false}, // last event = 2:00 stop ⇒ off-window persists
		{"10:00 exactly start minute", on, off, 10, 0, true},
		{"23:00 after start", on, off, 23, 0, true},
		{"00:30 carry over from yesterday's start", on, off, 0, 30, true},
		{"no schedules → default on", "", "", 3, 0, true},
		{"legacy bare hours", "10", "2", 2, 30, false},
		{"24:00 alias midnight stop", "10:00", "24:00", 0, 30, false},
		{"only stop set: after it", "", "2:00", 5, 0, false},
		{"only stop set: before it (yesterday's stop persists)", "", "2:00", 1, 0, false},
		{"only start set: before it (yesterday's start persists)", "6:00", "", 3, 0, true},
		{"same-minute start+stop → stop wins (apply order)", "5:00", "5:00", 5, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := chuotLoginRun(true, c.start, c.stop, ictAt(c.h, c.m))
			if got != c.want {
				t.Fatalf("start=%q stop=%q at %02d:%02d: got %v want %v", c.start, c.stop, c.h, c.m, got, c.want)
			}
		})
	}
}

func TestChuotLoginRunDefaultOffAlwaysFalse(t *testing.T) {
	if chuotLoginRun(false, "10:00", "", ictAt(12, 0)) {
		t.Fatal("AutoMouse=false must never start the mouse")
	}
}
