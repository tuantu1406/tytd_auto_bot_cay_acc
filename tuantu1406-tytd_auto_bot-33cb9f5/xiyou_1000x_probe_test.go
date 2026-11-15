//go:build live

package tytd_go

// TEMP probe (owner 2026-11-05): "chạy acc vietdart offline 1000 lần ải
// chính xem, nếu không qua lần nào thì chắc chắn là script sai".
// 1000 random seeds @ the wall row + CONTROL on recent already-crossed
// typ==2 rows (script must win those or the script itself is broken).
//
//	TYTD_XIYOU_DUMP=/tmp/xiyou_role_<uid>.json TYTD_XIYOU_WALL=280507 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiyou1000xProbe -v -timeout 1500s .
//	(dump/wall default to the 2026-11-05 case; dump = fresh DATA_INFO JSON
//	 from a live run, wall = the boss the account is stuck on)

import (
	"encoding/json"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"testing"
)

func TestXiyou1000xProbe(t *testing.T) {
	dump := "/tmp/xiyou_secret_role_71056440126.json"
	if v := os.Getenv("TYTD_XIYOU_DUMP"); v != "" {
		dump = v
	}
	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Skip("no full role dump")
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	prog := int32(m["progress"].(float64))
	t.Logf("role %s power=%v progress=%d", dump, m["power"], prog)

	wall := int32(190407)
	if v := os.Getenv("TYTD_XIYOU_WALL"); v != "" {
		n, _ := strconv.Atoi(v)
		wall = int32(n)
	}
	// controls: 3 most recent typ==2 rows BELOW the wall (crossed in reality)
	tbl := xiyouStageTable()
	var bosses []int32
	for r, st := range tbl {
		if st.typ == 2 && r < wall && st.next != 0 {
			bosses = append(bosses, r)
		}
	}
	sort.Slice(bosses, func(i, j int) bool { return bosses[i] > bosses[j] })
	if len(bosses) > 3 {
		bosses = bosses[:3]
	}
	t.Logf("controls: %v", bosses)

	for _, row := range append(bosses, wall) {
		n := 30
		if row == wall {
			n = 1000
		}
		wins := 0
		for i := 0; i < n; i++ {
			seed := rand.Int31n(1e6) + 1
			res, err := xiyouFight(raw, row, seed)
			if err != nil {
				t.Fatalf("row %d seed %d: %v", row, seed, err)
			}
			if res.Result == 2 {
				wins++
				if wins <= 5 {
					t.Logf("row %d seed %d: WIN (%dms)", row, seed, res.Ms)
				}
			}
		}
		verdict := "THUA SẠCH"
		if wins > 0 {
			verdict = "CÓ THẮNG"
		}
		t.Logf("row %d: %d/%d wins — %s", row, wins, n, verdict)
	}
}
