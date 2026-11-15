//go:build live

package tytd_go

// ĐAN GIỚI SEASON PROBE — READ-ONLY live diagnosis for the owner report
// 2026-10-21: "đang lỗi đan giới không detect được dược linh mùa mới".
// The engine derives the season bonus tree from the baked arithmetic rule
// (golden = 864001+3*(season-1), trio {G,G+1,G+2}) and folds the season
// number from the passive push 8ae9. A NEW season breaks any baked
// assumption: new tree ids, a formula that stopped being arithmetic, an
// 8ae9 that never arrives, or the golden cap (season > 10 → 0). This probe
// sends NOTHING that mutates (no plant/harvest/cuốc/submit) and prints:
//
//	1. EVERY passive push that follows AUTH (ty + hex head) — did 8ae9
//	   actually arrive, and what does the login config frame (0010) carry?
//	2. p.ElixirSeason as the engine folded it.
//	3. The FULL bag dump grouped by family — which herbal ids (864xxx AND
//	   anything outside the old 864024 ceiling) the account holds.
//	4. The engine's own math: golden, trio, quality, tier ranking — side by
//	   side with the raw ids so a wrong formula is visible at a glance.
//
//	TYTD_LIVE=1 TYTD_ELIXIR_SEASON=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiSeasonProbeLive -v -timeout 5m .
//
//	VERDICT-SEASON: OK (engine math matches held ids) / FAIL (names the mismatch)

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
)

func TestDanGioiSeasonProbeLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR_SEASON") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR_SEASON=1")
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Player{
		Username:  user,
		Name:      user,
		UID:       uid,
		writeChan: make(chan []byte, 256),
		ctx:       ctx,
	}
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	type pushRec struct {
		ty   string
		hex  string
		size int
	}
	var pmu sync.Mutex
	var pushes []pushRec
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
			h := d
			if len(h) > 48 {
				h = h[:48]
			}
			pmu.Lock()
			pushes = append(pushes, pushRec{ty: ty, hex: hex.EncodeToString(h), size: len(d)})
			pmu.Unlock()
			p.elixirDeliver(ty, d)
		}
	}()
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				select {
				case p.writeChan <- pack.Pack(PING_CMD, []byte{}, uid):
				case <-ctx.Done():
				}
			}
		}
	}()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)
	time.Sleep(6 * time.Second) // let the login pushes land (8ae9, 0010, …)

	pmu.Lock()
	seenSeasonPush := false
	for _, r := range pushes {
		t.Logf("PUSH ty=%s size=%d head=%s", r.ty, r.size, r.hex)
		if r.ty == SERVER_ELIXIR_DRAW_SEASON {
			seenSeasonPush = true
		}
	}
	pmu.Unlock()

	p.ElixirMu.Lock()
	season := p.ElixirSeason
	p.ElixirMu.Unlock()
	t.Logf("ENGINE ElixirSeason=%d (8ae9 arrived: %v)", season, seenSeasonPush)

	rsp, err := p.elixirEnterHome()
	if err != nil {
		t.Fatalf("ENTER: %v", err)
	}
	var herbs, others []string
	// OWNER 2026-10-21: "acc đó có hạt giống dược linh, có thể mày check ở
	// túi cây chứ không phải túi hạt — click ô đất trống game show hết loại
	// hạt available". The old probe hid num==0 rows — but an AVAILABLE seed
	// may well ride grassInfos as a 0-count row (unlock list ≠ pile count).
	// Dump EVERY row this time.
	held := map[int32]bool{}
	for _, g := range rsp.GetUserInfo().GetGrassInfos() {
		s := fmt.Sprintf("%d×%d", g.GetGrassId(), g.GetNum())
		if elixirIsHerb(g.GetGrassId()) {
			herbs = append(herbs, s)
		} else {
			others = append(others, s)
		}
		if g.GetNum() > 0 {
			held[g.GetGrassId()] = true
		}
	}
	sort.Strings(herbs)
	sort.Strings(others)
	t.Logf("BAG herbal (864xxx family, ALL rows incl num=0): %v", herbs)
	t.Logf("BAG other families (ALL rows): %v", others)
	// Raw seed-list candidates: every 864xxx row regardless of num is an
	// "available seed" candidate for the empty-plot picker.
	var availHerb []int32
	for _, g := range rsp.GetUserInfo().GetGrassInfos() {
		if elixirIsHerb(g.GetGrassId()) {
			availHerb = append(availHerb, g.GetGrassId())
		}
	}
	sort.Slice(availHerb, func(i, j int) bool { return availHerb[i] < availHerb[j] })
	t.Logf("AVAILABLE herbal seeds (any num): %v", availHerb)
	var grow []int32
	for _, l := range rsp.GetLands() {
		if l.GetGrassId() != 0 {
			grow = append(grow, l.GetGrassId())
		}
	}
	t.Logf("GROWING on field: %v", grow)

	// Raw 8aae re-marshal (unknown fields included): if the seed picker is
	// backed by a list the proto does not model yet, the 864xxx varints are
	// STILL visible in the bytes — grep for c2/d2 patterns offline.
	if raw, err := pb.Marshal(rsp); err == nil {
		t.Logf("RAW 8aae size=%d head=%s", len(raw), hex.EncodeToString(raw[:min(256, len(raw))]))
	}

	golden := elixirGoldenGrass(season)
	trio := elixirSeasonTrio(season)
	t.Logf("ENGINE math @season %d: golden=%d trio=%v", season, golden, trio)
	t.Logf("ENGINE formula across seasons 1..15: ")
	for n := int32(1); n <= 15; n++ {
		g := elixirGoldenGrass(n)
		t.Logf("  season %2d → golden %d", n, g)
	}
	tiers := elixirPlantTiers(held, season, grow)
	var tierStr []string
	for _, t := range tiers {
		tierStr = append(tierStr, fmt.Sprintf("%d(q%d)", t, elixirGrassQuality(t)))
	}
	t.Logf("ENGINE picker tiers (best first): %v", tierStr)

	// The verdict compares the ENGINE's world with the SERVER's world.
	fail := ""
	if !seenSeasonPush {
		fail = fmt.Sprintf("8ae9 (%s) KHÔNG bao giờ tới sau AUTH — ElixirSeason=%d có thể là số cũ/mù", SERVER_ELIXIR_DRAW_SEASON, season)
	} else if golden == 0 && season > 0 {
		fail = fmt.Sprintf("season=%d mà golden=0 — công thức baked vỡ (cap? id mới?)", season)
	} else if len(herbs) > 0 && len(tiers) == 0 {
		fail = "túi có cây 864xxx mà ladder rỗng — held/quality gate sai"
	}
	// Season-9+ trees beyond the old baked ceiling (864024) are the smoking
	// gun for "dược linh mùa mới không detect": held ids no name table or
	// trio covers.
	for id := range held {
		if elixirIsHerb(id) && id > 864024 {
			t.Logf("NEW-SEASON HERB id %d (>%d) — beyond baked name table (display falls back to \"cây %d\")", id, 864024, id)
		}
	}
	if fail != "" {
		fmt.Printf("VERDICT-SEASON: FAIL — %s\n", fail)
		return
	}
	fmt.Printf("VERDICT-SEASON: OK — season=%d golden=%d trio=%v, ladder=%v khớp túi %v\n",
		season, golden, trio, tierStr, herbs)
}
