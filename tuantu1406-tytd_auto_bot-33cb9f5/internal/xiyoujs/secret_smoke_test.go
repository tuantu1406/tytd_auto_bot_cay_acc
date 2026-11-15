package xiyoujs

import (
	"encoding/json"
	"os"
	"testing"
)

// Smoke: full ảo cảnh run offline against secret 1 with the vietdart dump
// (power 7.2M >> verifyPower 125k → expect win). Mirrors the real-run
// orchestration: Create → Step → (EnterDoor | Export+Rebuild) → Step → win.
func TestSecretOfflineRun(t *testing.T) {
	raw, err := os.ReadFile("/tmp/xiyou_role_71056440126.json")
	if err != nil {
		t.Skip("no role dump")
	}
	var info map[string]interface{}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	row, err := SecretRowOf(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("secret1: verifyPower=%d tl=%d doors=%v", row.VerifyPower, row.Timelimit, row.Templates)

	const seed = int32(424242)
	// offline door path: random from the pool (server picks doors in real runs)
	tpls := row.Templates
	first := tpls[0]
	sid, err := SecretCreate(raw, first, seed, row.Timelimit*1000)
	if err != nil {
		t.Fatal(err)
	}
	defer SecretDestroy(sid)
	selected := []int32{first}
	var fightData string
	var levels []int32
	for room := 0; room < 12; room++ {
		st, err := SecretRunStep(sid)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("room %d: status=%s gs=%d progress=%s frames=%d ms=%d levels=%v", room, st.Status, st.GameStatus, st.Progress, st.Frames, st.Ms, st.Levels)
		if st.Status == "end" {
			if st.GameStatus == 2 {
				levels = st.Levels
				t.Logf("WIN after %d rooms", room+1)
			} else {
				t.Fatalf("LOSS gameStatus=%d", st.GameStatus)
			}
			break
		}
		if st.Status != "room" {
			t.Fatalf("frame cap")
		}
		// pick next door (lower star) from pool, excluding recent
		next := tpls[(room+1)%len(tpls)]
		// predict boss room: progress + add(star(next)) >= 100
		add := float64(StarOfTemplate(next)) * 0 // placeholder; use client-side check via next step
		_ = add
		exp, err := SecretExport(sid)
		if err != nil {
			t.Fatal(err)
		}
		// heuristic: boss room = when progress >= 67 (3 rooms done) → rebuild
		var prog float64
		fmtSscan(st.Progress, &prog)
		if prog >= 67 {
			if err := SecretRebuild(sid, exp, next, tpls[0], selected); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := SecretEnterDoor(sid, next); err != nil {
				t.Fatal(err)
			}
			fightData = exp // candidate; real orchestration re-exports at boss boundary
		}
		selected = append(selected, next)
	}
	if len(levels) == 0 {
		t.Fatal("no win")
	}
	if len(fightData) == 0 {
		// fightData must be captured at the boss-boundary export
		t.Fatal("empty fightData")
	}
}

func fmtSscan(s string, f *float64) {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			s = s[:i]
			break
		}
	}
	var v int
	for _, c := range s {
		v = v*10 + int(c-'0')
	}
	*f = float64(v)
}
