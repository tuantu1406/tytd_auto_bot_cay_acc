//go:build live

package tytd_go

// Herb-value ladder live check (owner 2026-10-18): "hàm lấy cây dược linh giá
// trị cao nhất sai rồi, acc tao đang có minh tiêu lôi giáp là ngon nhất 4
// điểm nhưng mày lại chọn xuân nhật hướng dương 2 điểm". The old ladder sorted
// non-trio herbs by id DESC — 864021 (Xuân Nhật Hướng Dương, quality 4, 2đ)
// beat 864020 (Minh Tiêu Lôi Giáp, quality 5, 4đ) for no reason. The fix
// ranks by mined ElixirHerbDB quality (trio slots 6/5/4) ×2 only for this
// season's trio. This probe logs IN on <live-user>@710564 (owner-authorized
// test acc), reads the REAL bag via ENTER (8aae) — read-only, NOTHING is
// planted — and pins the ladder the engine would use:
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 go test -tags "sqlcipher live" -count=1 -run TestDanGioiHerbValueLive -v -timeout 180s .

import (
	"encoding/hex"

	"os"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
)

func TestDanGioiHerbValueLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710564
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}
	pt, err := request.LoginGPassport(user, pass)
	if err != nil {
		t.Fatal(err)
	}
	sd, uidF, err := request.LoginFuntap(pt)
	if err != nil {
		t.Fatal(err)
	}
	gu, err := request.GetServerURL(uidF, server)
	if err != nil {
		t.Fatal(err)
	}
	token, wsURL, uid, err := request.GetWSKey(gu, sd)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	write := make(chan []byte, 256)
	go func() {
		for f := range write {
			_ = conn.WriteMessage(2, f)
		}
	}()
	all := make(chan [2]string, 8192)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			select {
			case all <- [2]string{ty, hex.EncodeToString(d)}:
			default:
			}
		}
	}()
	kwStop := make(chan struct{})
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-kwStop:
				return
			case <-tk.C:
				write <- pack.Pack(PING_CMD, []byte{}, uid)
			}
		}
	}()
	defer close(kwStop)
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	write <- pack.Pack(AUTH_CMD, authMSG, uid)

	season := int32(0)
	call := func(cmd, ansType string, body []byte, tag string) []byte {
		// Give the 8ae9 season push room to arrive first.
		end := time.Now().Add(3 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if a[0] == SERVER_ELIXIR_DRAW_SEASON {
					var s proto.ElixirElvesDrawSeasonSync
					if raw, _ := hex.DecodeString(a[1]); pb.Unmarshal(raw, &s) == nil {
						season = s.GetSeason()
					}
				}
			case <-time.After(150 * time.Millisecond):
			}
		}
		if cmd != "" {
			write <- pack.Pack(cmd, body, uid)
		}
		end = time.Now().Add(8 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if a[0] == ansType {
					raw, _ := hex.DecodeString(a[1])
					t.Logf("%s %s len=%d", tag, ansType, len(raw))
					return raw
				}
			case <-time.After(250 * time.Millisecond):
			}
		}
		t.Logf("!! %s: no %s answer", tag, ansType)
		return nil
	}

	b, _ := pb.Marshal(&proto.ElixirEnterHomeReq{PlayerId: pb.Int64(int64(uid))})
	raw := call(S_ELIXIR_ENTER, SERVER_DANGIOI_INFO, b, "ENTER")
	if raw == nil {
		t.Fatal("no 8aae")
	}
	var rsp proto.ElixirEnterHomeResp
	if err := pb.Unmarshal(raw, &rsp); err != nil {
		t.Fatalf("8aae decode: %v", err)
	}
	t.Logf("season=%d golden=%s", season, elixirHerbName(elixirGoldenGrass(season)))

	held := map[int32]bool{}
	for _, g := range rsp.GetUserInfo().GetGrassInfos() {
		if g.GetNum() > 0 {
			t.Logf("bag: %d (%s) ×%d q=%d", g.GetGrassId(), elixirGrassName(g.GetGrassId()),
				g.GetNum(), elixirGrassQuality(g.GetGrassId()))
			held[g.GetGrassId()] = true
		}
	}
	var grow []int32
	for _, l := range rsp.GetLands() {
		if l.GetGrassId() != 0 {
			grow = append(grow, l.GetGrassId())
		}
	}
	tiers := elixirPlantTiers(held, season, grow)
	for i, id := range tiers {
		t.Logf("LADDER #%d: %d %s (q%d)", i+1, id, elixirGrassName(id), elixirGrassQuality(id))
	}
	if len(tiers) == 0 {
		t.Log("bag holds no herbal tree — engine goes task-mode; ladder assertion skipped")
		return
	}
	// The bug of the day: 864020 (Minh Tiêu Lôi Giáp, 4đ) must come BEFORE
	// 864021 (Xuân Nhật Hướng Dương, 2đ) whenever BOTH are held.
	pos := map[int32]int{}
	for i, id := range tiers {
		pos[id] = i
	}
	if held[864020] && held[864021] && pos[864020] > pos[864021] {
		t.Errorf("REGRESSION: 864021 (2đ) ranked above 864020 (4đ): %v", tiers)
	}
	// Sanity: the ladder must be score-monotone (quality ×2 inside the trio).
	score := func(id int32) int32 {
		s := elixirGrassQuality(id)
		for _, t := range elixirSeasonTrio(season) {
			if t == id {
				s *= 2
			}
		}
		return s
	}
	for i := 1; i < len(tiers); i++ {
		if score(tiers[i-1]) < score(tiers[i]) {
			t.Errorf("ladder not sorted: #%d %s(score %d) before %s(score %d)",
				i, elixirGrassName(tiers[i-1]), score(tiers[i-1]),
				elixirGrassName(tiers[i]), score(tiers[i]))
		}
	}
	t.Logf("HEAD pick = %d %s — this is what the bot plants", tiers[0], elixirGrassName(tiers[0]))
}
