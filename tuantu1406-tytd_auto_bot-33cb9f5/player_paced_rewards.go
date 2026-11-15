package tytd_go

import (
	"time"
	"tytd_go/internal/pack"
)

// Paced claim loop for the legacy tầm bảo bang login burst (S_TAM_BAO,
// empty body). Wire bytes are untouched — the old code fired three draws
// back-to-back at every login, so re-logged accounts spammed refusals and
// an account online all day never retried after the first burst. The loop
// sends the exact same call once per pacedClaimPeriod; the server still
// decides whether anything is free to take.
//
// Quảng cáo tổng môn stays the old login hardcode in autoDoSomethings —
// the server silently drops it when nothing is free.
//
// Same shape as autoUnionDaily (player_uniondaily.go).

const (
	pacedClaimFirstDelay = 22 * time.Second // after login traffic: yard 15 s, union daily 20 s, tasks 25 s
	pacedClaimPeriod     = 30 * time.Minute
)

// autoUnionTreasure draws one tầm bảo chip per period (the old burst fired
// three at login; a 30-minute cadence still picks up every free draw that
// accrues during the day).
func (p *Player) autoUnionTreasure() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(pacedClaimFirstDelay):
	}
	for {
		p.write(pack.Pack(S_TAM_BAO, []byte{}, p.UID))
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(pacedClaimPeriod):
		}
	}
}
