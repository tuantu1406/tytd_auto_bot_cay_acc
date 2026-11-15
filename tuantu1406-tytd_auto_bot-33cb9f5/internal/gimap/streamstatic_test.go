package gimap

// Static-scene pins for the event mini apps: /events/mapstatic is the scene
// vehicle, so every new board's extra field must survive the actual JSON
// encode the browser sees.

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestDitichStaticScene(t *testing.T) {
	st, err := StreamStatic(StreamDitich)
	if err != nil {
		t.Fatal(err)
	}
	if st.Tc == nil || len(st.Tc.Slots) != len(ballGVGSlots) {
		t.Fatalf("tc = %+v", st.Tc)
	}
	for i, s := range st.Tc.Slots {
		if s[0] <= 0 || s[0] >= 1 || s[1] <= 0 || s[1] >= 1 {
			t.Fatalf("slot %d not normalized: %v", i, s)
		}
	}
	// Slots must equal the LIVE frame transform exactly: ballGVGStatic and
	// streamBallGVG share g.slot, so a live city at index i lands on slot i.
	if st.Tc.SlotR <= 0 {
		t.Fatalf("slotR = %v", st.Tc.SlotR)
	}
	hexRe := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	for _, id := range []int32{1, 2, 3} {
		if !hexRe.MatchString(st.Tc.Camps[id]) {
			t.Fatalf("camp %d = %q", id, st.Tc.Camps[id])
		}
	}
	// The page reads .tc from JSON — the key must be exactly "tc" (never
	// "bg": StreamMap/StaticMap already use that for the background image).
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back["tc"]; !ok {
		t.Fatalf("no \"tc\" key in %s", raw)
	}
	// Live frames must not regress: bg string field untouched.
	var fr StreamMap
	if err := json.Unmarshal([]byte(`{"which":"ditich","aspect":1.25,"cities":[],"tc":{"place":4003,"r":300,"balls":[{"n":"a","c":1,"x":0.5,"y":0.5}]}}`), &fr); err != nil {
		t.Fatal(err)
	}
	if fr.Tc == nil || fr.Tc.Place != 4003 || len(fr.Tc.Balls) != 1 || fr.Tc.Balls[0].X != 0.5 {
		t.Fatalf("frame decode = %+v", fr.Tc)
	}
}
