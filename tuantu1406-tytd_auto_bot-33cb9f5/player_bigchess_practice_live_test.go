//go:build live

package tytd_go

// TestLiveBigChessPracticePlay is the FREE soak-test: practice mode
// (S_BIG_CHESS_PRACTICE_ENTER_GAME 234121, move 234122, end 234123) costs no
// daily starts and no ingots, so the engine can be played, fed into the
// replay learner, and soak-tested without spending anything.
//
// Practice restrictions honored: practice move requests carry no power
// fields, so every recommendation is executed as a plain move (power flags
// dropped); deaths settle directly (no revive ladder).
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveBigChessPracticePlay -v -timeout 1800s .
//
// Env: TYTD_LIVE_SERVER (default 710547), TYTD_PRACTICE_RUNS (default 3),
// turns per run capped at 250.

import (
	"os"
	"testing"
	"time"
	"tytd_go/internal/bigchess"
)

func TestLiveBigChessPracticePlay(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveBcUser = liveUserEnv(t)
		liveBcPass = livePassEnv(t)
	)
	liveBcServer := envIntOr(t, "TYTD_LIVE_SERVER", 710547)
	runs := envIntOr(t, "TYTD_PRACTICE_RUNS", 3)

	s, err := bcDialLive(t, liveBcUser, liveBcPass, liveBcServer)
	if err != nil {
		t.Fatalf("live connect: %v", err)
	}
	defer s.cleanup()
	p := s.p

	reconnects, stalls, engineMoves, fallbackMoves, feedOK, feedFail := 0, 0, 0, 0, 0, 0
	reconnect := func() bool {
		reconnects++
		if reconnects > 6 {
			return false
		}
		s.cleanup()
		time.Sleep(3 * time.Second)
		ns, derr := bcDialLive(t, liveBcUser, liveBcPass, liveBcServer)
		if derr != nil {
			t.Logf("reconnect failed: %v", derr)
			return false
		}
		s, p = ns, ns.p
		t.Logf("reconnected (attempt %d)", reconnects)
		return true
	}

	for run := 1; run <= runs; run++ {
		if ev, ok := p.BigChessPracticeEnterGame(20 * time.Second); !ok {
			if bcStalled(ev) {
				stalls++
				if !reconnect() {
					t.Fatalf("stalled at practice enter and reconnect exhausted")
				}
				if _, ok := p.BigChessPracticeEnterGame(20 * time.Second); !ok {
					t.Fatalf("practice enter still failing after reconnect")
				}
			} else {
				t.Fatalf("practice enter refused: %v", ev["error"])
			}
		}
		t.Logf("practice run %d started", run)

		for turn := 0; turn < 250; turn++ {
			v, ok := p.BigChessState()
			if !ok {
				t.Fatalf("lost state")
			}
			st := v["state"].(bigchess.State)
			if st.GameStatus != bigchess.StatusRunning {
				t.Logf("practice run %d ended at step %d (status=%d)", run, st.Step, st.GameStatus)
				break
			}
			before := st
			x, y, _, _, _, src := enginePick(t, st) // practice: powers not on the wire
			if src == "none" {
				t.Logf("practice run %d turn %d: no move — settling", run, turn)
				break
			}
			view, ok := p.BigChessPracticeMove(x, y, 20*time.Second)
			if !ok {
				if bcStalled(view) {
					stalls++
					t.Logf("practice move stalled — reconnecting, fresh run")
					if !reconnect() {
						goto summary
					}
					break // outer: re-enter practice
				}
				if r, isR := view["ret"].(int32); isR {
					_, _ = enginePost("/v1/rejections", map[string]any{
						"state": bcEngineState(before),
						"action": map[string]any{"command": "S_BIG_CHESS_PRACTICE_MOVE", "params": map[string]any{
							"activityId": before.ActivityID, "x": x, "y": y,
						}},
						"result":    map[string]any{"ok": false, "ret": r},
						"sessionId": "practice-play:" + liveBcUser,
					})
				}
				t.Logf("practice run %d turn %d: %s move (%d,%d) refused: %v", run, turn, src, x, y, view["error"])
				continue
			}
			if src == "engine" {
				engineMoves++
			} else {
				fallbackMoves++
			}
			nst := view["state"].(bigchess.State)
			if turn%10 == 0 || nst.GameStatus != 0 {
				t.Logf("practice run %d turn %d (step %d, %s): (%d,%d) → score=%d pieces=%d status=%d",
					run, turn, nst.Step, src, x, y, nst.Score, len(nst.Board), nst.GameStatus)
			}
			if _, err := enginePost("/v1/transitions", map[string]any{
				"stateBefore": bcEngineState(before),
				"action": map[string]any{"command": "S_BIG_CHESS_PRACTICE_MOVE", "params": map[string]any{
					"activityId": nst.ActivityID, "x": x, "y": y,
				}},
				"result":     map[string]any{"ok": true, "ret": 0},
				"stateAfter": bcEngineState(nst),
				"sessionId":  "practice-play:" + liveBcUser,
			}); err != nil {
				feedFail++
			} else {
				feedOK++
			}
		}

		if ev, ok := p.BigChessPracticeEnd(20 * time.Second); ok {
			rst := ev["state"].(bigchess.State)
			t.Logf("practice run %d settled: score=%d rank=%d", run, rst.Score, rst.NowRank)
		} else {
			t.Logf("practice run %d end: %v", run, ev["error"])
			if bcStalled(ev) && !reconnect() {
				break
			}
		}
	}
summary:
	t.Logf("PRACTICE TOTAL engineMoves=%d fallback=%d stalls=%d reconnects=%d feeds ok=%d fail=%d",
		engineMoves, fallbackMoves, stalls, reconnects, feedOK, feedFail)
}
