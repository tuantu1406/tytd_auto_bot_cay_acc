//go:build live

package tytd_go

// GOD ISLAND PEOPLE FIREHOSE PROBE (owner 2026-10-19: "cho tao dò bằng acc
// test") — hunt the two opcodes the Bồng Lai/Thiên Ma client must receive to
// draw other players: (i) the people-in-city roster (≡ Côn Lôn 219903) and
// (ii) the in-flight march feed (≡ 219943). GI currently has no known
// equivalent, so this probe is pure observation: it logs EVERY msgType +
// payload size + sample bytes seen on the wire while the account sits
// resident on the island (same residency traffic godIslandStay runs —
// INFO_LOAD 206820 every 3s, base/heartbeat/ball per 20th tick — nothing
// else is ever sent; item ban respected structurally).
//
// Frames whose type is NOT one of our requested answers are push candidates;
// the verdict section prints the ones inside the God Island opcode range
// (6801..6999) with sizes and hex samples so the port can be byte-pinned.
// If the window shows nobody (event closed / island empty) the probe says so
// plainly — it must not invent opcodes.
//
//	TYTD_LIVE=1 TYTD_GI_PROBE=1 [TYTD_GI_PROBE_SECS=240] \
//	  TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestGIPeopleProbeLive -v -timeout 600s .

import (
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

func TestGIPeopleProbeLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_GI_PROBE") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_GI_PROBE=1")
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
	window := 240 * time.Second
	if v := os.Getenv("TYTD_GI_PROBE_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			window = time.Duration(n) * time.Second
		}
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

	writeChan := make(chan []byte, 256)
	go func() {
		for f := range writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()

	type probeFrame struct {
		ty   string
		data []byte
	}
	var mu sync.Mutex
	var frames []probeFrame
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
			cp := append([]byte(nil), d...)
			mu.Lock()
			frames = append(frames, probeFrame{ty, cp})
			mu.Unlock()
		}
	}()
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for range tk.C {
			select {
			case writeChan <- pack.Pack(PING_CMD, []byte{}, uid):
			default:
			}
		}
	}()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)
	time.Sleep(5 * time.Second) // login firehose; activity list rides it

	// ---- discover open God Island variants from the activity pushes ----
	mu.Lock()
	var active []int32
	seenID := map[int32]bool{}
	for _, f := range frames {
		var push proto.PushActivityList
		if pb.Unmarshal(f.data, &push) != nil {
			continue
		}
		for _, mc := range push.GetMainConfig() {
			if (mc.GetType() == giActivityTypeGodIsland || mc.GetType() == giActivityTypePeakIsland) &&
				mc.GetActivityId() != 0 && !seenID[mc.GetActivityId()] {
				seenID[mc.GetActivityId()] = true
				active = append(active, mc.GetActivityId())
				t.Logf("ACTIVITY: God Island variant id=%d type=%d", mc.GetActivityId(), mc.GetType())
			}
		}
	}
	mu.Unlock()
	if len(active) == 0 {
		t.Logf("no 68/333 activity announced in the login push — probing passively anyway")
	}

	// ---- view-pull verification: the kunlun-219903 ≡ (206823 → 1aa7) ------
	// One CityInfo pull for the whole minimap city set: proves the request
	// shape and that the answer decodes (cityInfo headInfo = standing
	// people, routeInfo = marches). The richest 1aa7 is hex-dumped in full
	// so the port byte-pins the unmodelled head fields from a real capture.
	var citySample []byte
	if len(active) > 0 {
		aid := active[0]
		b, _ := pb.Marshal(&proto.GodIslandBaseInfoReqMsg{ActivityId: aid})
		writeChan <- pack.Pack(GOD_ISLAND_BASE_INFO_CMD, b, uid)
		time.Sleep(2 * time.Second)
		var floorID, groupID int32
		mu.Lock()
		for _, f := range frames {
			if f.ty != SERVER_GOD_ISLAND_BASE_INFO {
				continue
			}
			var bi proto.GodIslandBaseInfoRespMsg
			if pb.Unmarshal(f.data, &bi) == nil && bi.GetActivityId() == aid {
				floorID, groupID = bi.GetFloor(), bi.GetGroup() // last wins
			}
		}
		mu.Unlock()
		fl, gr := floorID, groupID
		b, _ = pb.Marshal(&proto.GodIslandGameMiniMapInfoReqMsg{ActivityId: aid, Floor: &fl, Group: &gr})
		writeChan <- pack.Pack(GOD_ISLAND_MINIMAP_CMD, b, uid)
		time.Sleep(2 * time.Second)
		var ask []int32
		seenC := map[int32]bool{}
		mu.Lock()
		for _, f := range frames {
			if f.ty != SERVER_GOD_ISLAND_MINIMAP {
				continue
			}
			var mm proto.GodIslandGameMiniMapInfoRespMsg
			if pb.Unmarshal(f.data, &mm) != nil {
				continue
			}
			ask = ask[:0]
			seenC = map[int32]bool{}
			for _, c := range mm.GetCityInfo() {
				if c.GetCityId() == 0 || seenC[c.GetCityId()] || len(ask) >= 40 {
					continue
				}
				seenC[c.GetCityId()] = true
				ask = append(ask, c.GetCityId())
			}
		}
		mu.Unlock()
		// The client asks TWO questions (STATIC_ANALYSIS.md): the fixed
		// home-block cityVec 1001..1016 (union-colour ordering) and, per
		// camera view, server-listed cities. Replaying all-40-at-once got
		// ret=6833, so mirror the real shapes exactly.
		pulls := [][]int32{
			{1001, 1006, 1002, 1010, 1011, 1012, 1005, 1004, 1003, 1007, 1009, 1008, 1013, 1014, 1015, 1016},
		}
		if len(ask) > 0 {
			pulls = append(pulls, ask)
		}
		for i, vec := range pulls {
			t.Logf("CITY-PULL#%d: activity=%d floor=%d group=%d asking %d cities %v", i+1, aid, floorID, groupID, len(vec), vec)
			b, _ = pb.Marshal(&proto.GodIslandGameCityInfoReqMsg{ActivityId: aid, CityVec: vec, FloorId: floorID, GroupId: groupID})
			writeChan <- pack.Pack(GOD_ISLAND_CITY_INFO_CMD, b, uid)
			time.Sleep(3 * time.Second)
		}
		mu.Lock()
		var answers [][]byte
		for _, f := range frames {
			if f.ty == SERVER_GOD_ISLAND_CITY_INFO {
				answers = append(answers, append([]byte(nil), f.data...))
			}
		}
		mu.Unlock()
		if len(answers) == 0 {
			t.Errorf("206823 pulled but NO 1aa7 answer arrived")
		}
		for _, a := range answers {
			var ci proto.GodIslandGameCityInfoRespMsg
			if err := pb.Unmarshal(a, &ci); err != nil {
				t.Errorf("1aa7 does NOT decode against the decoded-proto schema: %v", err)
				continue
			}
			heads := 0
			for _, c := range ci.GetCityInfo() {
				heads += len(c.GetHeadInfo())
			}
			t.Logf("1aa7 DECODES: ret=%d cities=%d heads=%d routes=%d",
				ci.GetRet(), len(ci.GetCityInfo()), heads, len(ci.GetRouteInfo()))
			if ci.GetRet() == 0 && len(a) > len(citySample) {
				citySample = a
			}
		}
		if citySample == nil {
			t.Logf("no ret=0 city answer — island may have no populated block for this account, or view needs the exact client cadence")
		}
	}

	// ---- resident observation: replay godIslandStay's OWN traffic only ----
	deadline := time.Now().Add(window)
	tick := 0
	for time.Now().Before(deadline) {
		for _, aid := range active {
			b, _ := pb.Marshal(&proto.GodIslandGameInfoReqMsg{ActivityId: aid})
			writeChan <- pack.Pack(GOD_ISLAND_INFO_LOAD_CMD, b, uid)
			if tick%20 == 0 {
				b, _ := pb.Marshal(&proto.GodIslandBaseInfoReqMsg{ActivityId: aid})
				writeChan <- pack.Pack(GOD_ISLAND_BASE_INFO_CMD, b, uid)
				b, _ = pb.Marshal(&proto.GodIslandHeartBeatReqMsg{ActivityId: aid})
				writeChan <- pack.Pack(GOD_ISLAND_HEART_BEAT_CMD, b, uid)
				b, _ = pb.Marshal(&proto.GodIslandGameSpiritualBallInfoReqMsg{ActivityId: aid})
				writeChan <- pack.Pack(GOD_ISLAND_BALL_INFO_CMD, b, uid)
			}
		}
		time.Sleep(3 * time.Second)
		tick++
	}

	// ---- summary ----
	asked := map[string]bool{ // answers our requests legitimately produce
		SERVER_GOD_ISLAND_INFO_LOAD: true, SERVER_GOD_ISLAND_BALL_INFO: true,
		SERVER_GOD_ISLAND_BASE_INFO: true, SERVER_GOD_ISLAND_MINIMAP: true,
		SERVER_GOD_ISLAND_CITY_INFO: true, // the 206823 view pull
	}
	knownPush := map[string]string{
		SERVER_GOD_ISLAND_MYGAMEINFO: "push mygameinfo (self)",
		SERVER_GOD_ISLAND_LEFT_OUT:   "push union left island",
		SERVER_GOD_ISLAND_BEEN_KILL:  "push we died",
		SERVER_GOD_ISLAND_AUTO_STOP:  "push auto battle stopped",
		SERVER_GOD_ISLAND_ROUTE_SYNC: "push march feed (≡ kunlun 219943) ← TARGET",
		SERVER_GOD_ISLAND_LINE_SYNC:  "push battle lines",
	}
	type stat struct {
		n, min, max, sum int
		sample           []byte
	}
	stats := map[string]*stat{}
	mu.Lock()
	for _, f := range frames {
		s := stats[f.ty]
		if s == nil {
			s = &stat{min: 1 << 30}
			stats[f.ty] = s
		}
		s.n++
		s.sum += len(f.data)
		if len(f.data) < s.min {
			s.min = len(f.data)
		}
		if len(f.data) > s.max {
			s.max = len(f.data)
		}
		if s.sample == nil || len(f.data) > len(s.sample) { // richest sample wins
			s.sample = f.data
		}
	}
	mu.Unlock()
	types := make([]string, 0, len(stats))
	for ty := range stats {
		types = append(types, ty)
	}
	sort.Slice(types, func(i, j int) bool { return stats[types[i]].n > stats[types[j]].n })

	fmt.Printf("=== GI FIREHOSE PROBE — %v resident window, %d frames, %d distinct types ===\n",
		window, len(frames), len(types))
	var candidates []string
	for _, ty := range types {
		s := stats[ty]
		tag := ""
		switch {
		case asked[ty]:
			tag = " (our request's answer)"
		case knownPush[ty] != "":
			tag = " (known: " + knownPush[ty] + ")"
		}
		sample := s.sample
		if len(sample) > 96 {
			sample = sample[:96]
		}
		fmt.Printf("  %s n=%-4d size min/avg/max=%d/%d/%d%s\n    hex[%d]: %s\n",
			ty, s.n, s.min, s.sum/s.n, s.max, tag, len(sample), hex.EncodeToString(sample))
		n, err := strconv.ParseInt(ty, 16, 32)
		if err == nil && n >= 0x1a91 && n <= 0x1b60 && !asked[ty] && knownPush[ty] == "" {
			candidates = append(candidates, ty)
		}
	}
	if citySample != nil {
		full := citySample
		if len(full) > 4096 {
			full = full[:4096]
		}
		fmt.Printf("=== 1aa7 full capture (%d bytes, port walks head fields from THIS) ===\n%s\n", len(citySample), hex.EncodeToString(full))
	}
	if stats[SERVER_GOD_ISLAND_ROUTE_SYNC] == nil {
		fmt.Printf("NOTE: march-feed push 1aa6 did NOT arrive during the window — either nobody marched, or the feed needs an un-discovered trigger\n")
	}
	if len(candidates) > 0 {
		fmt.Printf("VERDICT-PEOPLE-PROBE: %d UNSEEN God-Island-range push(es): %v — decode these against the kunlun 219903/219943 templates before porting\n",
			len(candidates), candidates)
	} else if len(active) == 0 {
		fmt.Printf("VERDICT-PEOPLE-PROBE: no 68/333 activity open right now and no island-range pushes — rerun while Bồng Lai/Thiên Ma is running with people on the map\n")
	} else {
		fmt.Printf("VERDICT-PEOPLE-PROBE: resident on island(s) %v for %v but saw NO unrequested island-range push — GI may only publish people on an explicit pull; capture what the REAL client sends (client js) before concluding\n",
			active, window)
	}
}
