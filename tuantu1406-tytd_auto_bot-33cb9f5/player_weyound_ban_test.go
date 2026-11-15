package tytd_go

// Source-scan guard for the WeYound item ban [[tytd-item-ban]]: the engine
// files may only reach the wire through the allowlisted command constants.
// USE_ITEM (218605 = 000355ed), AUTO_SWITCH (218616 = 000355f8) and the PVP
// challenge family (218630..218635 = 00035606..0003560b) must NEVER appear
// in any engine file — not as a constant reference, not as a literal hex.
// This is the static twin of the live ban assertion in
// tiencanh_engine_live_test.go (which watches the actual outbound frames).

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var wyEngineFiles = []string{
	"player_weyound.go",
	"player_weyound_map.go",
	"player_weyound_march.go",
}

// every WE_YOUND_* request constant the engine is allowed to put on the wire
var wyAllowedCmdConsts = map[string]bool{
	"WE_YOUND_ENTER_ACTIVITY_CMD":     true, // 218600
	"WE_YOUND_ENTER_CMD":              true, // 218601
	"WE_YOUND_EXPLORE_CMD":            true, // 218602
	"WE_YOUND_MOVE_CMD":               true, // 218603
	"WE_YOUND_DEAL_EVENT_CMD":         true, // 218604
	"WE_YOUND_TRAP_SEEK_CMD":          true, // 218606
	"WE_YOUND_ASSIST_CMD":             true, // 218607 trap-rescue of mates, free (owner law 2026-11-13); monsterId never sent
	"WE_YOUND_ASSIST_LOG_CMD":         true, // 218608 assist board poll, free
	"WE_YOUND_WORSHIP_CMD":            true, // 218612
	"WE_YOUND_REWARD_FLOOR_CMD":       true, // 218615
	"WE_YOUND_GET_MONSTER_REWARD_CMD": true, // 218617
	"WE_YOUND_BONUS_CMD":              true, // 218620
	"WE_YOUND_GET_BONUS_RESULT_CMD":   true, // 218621
	"WE_YOUND_GET_BONUS_ITEM_CMD":     true, // 218622
	"WE_YOUND_SEND_BONUS_ITEM_CMD":    true, // 218623 donate only, ids gated
	"WE_YOUND_UNION_GIFT_LIST_CMD":    true, // 218636
	"WE_YOUND_RECEIVE_UNION_GIFT_CMD": true, // 218637
	// generic activity-data fetch used by the shop sweep config pull
	"ACTIVITY_GET_DATA_CMD": true,
}

func TestWyBanSourceScan(t *testing.T) {
	cmdRef := regexp.MustCompile(`\bWE_YOUND_[A-Z_]+_CMD\b`)
	hexLit := regexp.MustCompile(`"00035[0-9a-f]{3}"`)
	for _, f := range wyEngineFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range cmdRef.FindAllString(string(src), -1) {
			if !wyAllowedCmdConsts[m] {
				t.Errorf("%s references forbidden constant %s", f, m)
			}
		}
		for _, m := range hexLit.FindAllString(string(src), -1) {
			n, err := strconv.ParseInt(strings.Trim(m, `"`)[4:], 16, 32)
			if err != nil {
				t.Errorf("%s unparsable opcode literal %s", f, m)
				continue
			}
			// banned: USE_ITEM, AUTO_SWITCH, PVP challenge family
			if n == 0x55ed || n == 0x55f8 || (n >= 0x5606 && n <= 0x560b) {
				t.Errorf("%s hard-codes banned opcode %s [[tytd-item-ban]]", f, m)
			}
			if !wyAllowedCmdHexes[int32(n)] {
				t.Errorf("%s hard-codes opcode literal %s instead of an allowlisted constant", f, m)
			}
		}
	}
}

// the numeric identity of the allowlist, kept honest against command.go by
// the live test; here it only guards against a raw hex sneaking in that no
// constant covers.
var wyAllowedCmdHexes = map[int32]bool{
	0x55e8: true, 0x55e9: true, 0x55ea: true, 0x55eb: true, 0x55ec: true,
	0x55ee: true, 0x55f4: true, 0x55f7: true, 0x55f9: true, 0x55fc: true,
	0x55fd: true, 0x55fe: true, 0x55ff: true, 0x560c: true, 0x560d: true,
	// ACTIVITY_GET_DATA_CMD (generic 21003) rides the same transport for
	// the shop config pull — its hex comes from command.go, match loosely:
	// hex literals of OTHER events are out of scope for this ban file set.
}
