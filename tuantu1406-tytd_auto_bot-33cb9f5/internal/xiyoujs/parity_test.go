package xiyoujs

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// Parity gate: goja must reproduce the node harness (the cracked reference)
// BIT-EXACT on the same role/stage/seed. Roles are DATA_INFO dumps from
// live runs; stages are real boss rows.
func TestParityVsNode(t *testing.T) {
	roles := []string{"/tmp/xiyou_role_71056440126.json", "/tmp/xiyou_role_71056447890.json"}
	type tc struct {
		stage, seed int32
	}
	cases := []tc{
		{20107, 22643}, {20107, 999999}, {20207, 335745}, {20307, 651182},
		{190307, 897608}, {190307, 146119}, {20407, 424242},
	}
	for _, rf := range roles {
		raw, err := os.ReadFile(rf)
		if err != nil {
			t.Skipf("no role file %s (run a live test first)", rf)
		}
		for _, c := range cases {
			got, err := Sim(raw, c.stage, c.seed)
			if err != nil {
				t.Fatalf("goja Sim(role=%s stage=%d seed=%d): %v", rf, c.stage, c.seed, err)
			}
			cmd := exec.Command("node", "harness.js",
				"--role", rf, "--stage", itoa(c.stage), "--seed", itoa(c.seed))
			cmd.Dir = "/workspace/tytd/xiyou_sim"
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("node harness: %v", err)
			}
			var want struct {
				Result int     `json:"result"`
				Levels []int32 `json:"levels"`
				Ms     int     `json:"ms"`
				Frames int     `json:"frames"`
			}
			if err := json.Unmarshal(out, &want); err != nil {
				t.Fatalf("unmarshal %q: %v", out, err)
			}
			if got.Result != want.Result || got.Ms != want.Ms || got.Frames != want.Frames ||
				!equal(got.Levels, want.Levels) {
				t.Errorf("MISMATCH role=%s stage=%d seed=%d\n goja: %d %v ms=%d f=%d\n node: %d %v ms=%d f=%d",
					rf, c.stage, c.seed, got.Result, got.Levels, got.Ms, got.Frames,
					want.Result, want.Levels, want.Ms, want.Frames)
			} else {
				t.Logf("ok %s stage=%d seed=%d → result=%d levels=%v ms=%d", rf, c.stage, c.seed, got.Result, got.Levels, got.Ms)
			}
		}
	}
}

func equal(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func itoa(v int32) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
