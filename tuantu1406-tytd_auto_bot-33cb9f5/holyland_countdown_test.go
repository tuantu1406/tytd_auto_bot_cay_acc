package tytd_go

// Cửu U pre-open countdown (owner 2026-10-10: the game already counts down
// while the events tab shows nothing — show a "sắp mở" card so users log in
// early and dive in at open). The card may ONLY appear when the server's own
// anchor lands inside the next 24 h, and must hand over to the real card the
// moment the round runs.

import (
	"testing"
	"time"
)

func TestHlNextBeginMs(t *testing.T) {
	now := int64(1_760_000_000_000) // arbitrary epoch
	day := int64(hlFightBeginMs)          // begin = day + 20h
	cases := []struct {
		name  string
		apply []int64
		ts    []int64
		want  int64
	}{
		{"round RUNNING today + next day → next", []int64{now - 30*60_000 - day, now + 24*3600_000 - day}, nil, now + 24*3600_000},
		{"earliest future picked", []int64{now + 2*3600_000 - day, now + 5*3600_000 - day}, nil, now + 2*3600_000},
		{"past-only → nothing", []int64{now - 10*day}, nil, 0},
		{"beyond 24h → nothing (stale anchor guard)", []int64{now + 30*3600_000 - day}, nil, 0},
		{"ts fallback when apply empty", nil, []int64{now + 3600_000 - day}, now + 3600_000},
		{"junk days ignored", []int64{0, -5, now + 3600_000 - day}, nil, now + 3600_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hlNextBeginMs(c.apply, c.ts, now); got != c.want {
				t.Fatalf("got %d want %d", got, c.want)
			}
		})
	}
}

func TestHolyStartICT(t *testing.T) {
	if s := holyStartICT(0); s != "" {
		t.Fatalf("no-start must render empty, got %q", s)
	}
	ms := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC).UnixMilli() // 19:00 ICT
	if s := holyStartICT(ms); s != "19:00 ngày 10/10" {
		t.Fatalf("ICT formatting: %q", s)
	}
}
