//go:build live

package tytd_go

// TÂY DU PHỤC MA — boss crossing with the REAL client sim levels.
//
// Chain: DATA_INFO(231116) → UPDATE_SEED(231137) → dump info JSON → node
// harness (/workspace/tytd/xiyou_sim/harness.js runs the game's own combat
// modules with the real DBs) → UPDATE_PROGRESS(next, codeVersion, levels)
// after the simulated fight duration.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouBossSimLive -v -timeout 900s .

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	pb "google.golang.org/protobuf/proto"
)

type simOut struct {
	Result     int     `json:"result"`
	ResultName string  `json:"resultName"`
	Levels     []int32 `json:"levels"`
	Frames     int     `json:"frames"`
	Ms         int     `json:"ms"`
	PlayerHp   string  `json:"playerHp"`
	Seed       int32   `json:"seed"`
}

func TestXiYouBossSimLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_XIYOU") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_XIYOU=1")
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
		Username: user, Name: user, UID: uid, Notify: false,
		NotiChan: make(chan NotifyMGS, 256), writeChan: make(chan []byte, 256), ctx: ctx,
	}
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.write(pack.Pack(AUTH_CMD, authMSG, uid))
	var mu sync.Mutex
	var dataReplies, upReplies, seedReplies [][]byte
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
			mu.Lock()
			switch ty {
			case SERVER_XIYOU_DATA:
				dataReplies = append(dataReplies, append([]byte{}, d...))
			case SERVER_XIYOU_UPD_PROGRESS:
				upReplies = append(upReplies, append([]byte{}, d...))
			case "79a1":
				seedReplies = append(seedReplies, append([]byte{}, d...))
			}
			mu.Unlock()
			if ty == SERVER_ACTIVITY_LIST || ty == SERVER_ACTIVITY_LIST_UPDATE {
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			}
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
				p.write(pack.Pack(PING_CMD, []byte{}, uid))
			}
		}
	}()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) && len(p.ActList.byType(XIYOU_ACTIVITY_TYPE)) == 0 {
		time.Sleep(300 * time.Millisecond)
	}
	var actID int32
	nowMs := time.Now().UnixMilli()
	for _, e := range p.ActList.byType(XIYOU_ACTIVITY_TYPE) {
		if e.BeginTime <= nowMs && nowMs < e.EndTime {
			actID = e.ActivityID
		}
	}
	if actID == 0 {
		t.Fatal("type 311 not open")
	}
	waitNew := func(repls *[][]byte, before int, timeout time.Duration) []byte {
		lim := time.Now().Add(timeout)
		for time.Now().Before(lim) {
			mu.Lock()
			var d []byte
			if len(*repls) > before {
				d = append([]byte{}, (*repls)[before]...)
			}
			mu.Unlock()
			if d != nil {
				return d
			}
			time.Sleep(200 * time.Millisecond)
		}
		return nil
	}
	ask := func(cmd string, body []byte, repls *[][]byte) []byte {
		mu.Lock()
		before := len(*repls)
		mu.Unlock()
		p.write(pack.Pack(cmd, body, uid))
		return waitNew(repls, before, 8*time.Second)
	}

	// ── roleData (full info for the sim)
	db, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
	d := ask(XIYOU_DATA_INFO_CMD, db, &dataReplies)
	if d == nil {
		t.Fatal("no DATA_INFO reply")
	}
	var r proto.XiYouPlayerDataInfoResp
	if pb.Unmarshal(d, &r) != nil || r.GetInfo() == nil {
		t.Fatal("DATA_INFO unmarshal")
	}
	info := r.GetInfo()
	cur := info.GetProgress()
	st, ok := xiyouStageTable()[cur]
	if !ok || st.next == 0 {
		t.Fatalf("no stage row for progress=%d", cur)
	}
	t.Logf("account %s on boss row %d → target %d (power=%s)", user, cur, st.next, info.GetPower())

	js, err := protojson.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(os.TempDir(), "xiyou_role_sim.json")
	if err := os.WriteFile(tmp, js, 0o600); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
		sd := ask("000386e1", sb, &seedReplies)
		seed := int32(0)
		if sd != nil {
			var sr proto.XiYouUpdateSeedResp
			pb.Unmarshal(sd, &sr)
			seed = sr.GetSeed()
		}
		if seed == 0 {
			t.Logf("attempt %d: no seed, retry", attempt)
			time.Sleep(10 * time.Second)
			continue
		}
		// run the game's own combat sim for this exact seed
		cmd := exec.Command("node", "harness.js",
			"--role", tmp, "--stage", strconv.Itoa(int(cur)), "--seed", strconv.Itoa(int(seed)))
		cmd.Dir = "/workspace/tytd/xiyou_sim"
		out, err := cmd.Output()
		if err != nil {
			if ee, ok2 := err.(*exec.ExitError); ok2 {
				t.Logf("harness stderr: %s", string(ee.Stderr))
			}
			t.Fatalf("harness: %v", err)
		}
		var so simOut
		if err := json.Unmarshal(out, &so); err != nil {
			t.Fatalf("harness output %q: %v", string(out), err)
		}
		t.Logf("attempt %d seed=%d sim: %s frames=%d ms=%d levels=%v", attempt, seed, so.ResultName, so.Frames, so.Ms, so.Levels)
		if so.Result != 2 { // AllEnemyDie = win
			t.Logf("attempt %d: sim says LOSS (%s) — not claiming", attempt, so.ResultName)
			time.Sleep(12 * time.Second)
			continue
		}
		// respect the fight's real duration before claiming (server may watch seed→claim gap)
		sleepFor := time.Duration(so.Ms) * time.Millisecond
		if sleepFor > 50*time.Second {
			sleepFor = 50 * time.Second
		}
		t.Logf("attempt %d: waiting out %v of fight time, then asserting %d→%d", attempt, sleepFor, cur, st.next)
		time.Sleep(sleepFor)
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: st.next, CodeVersion: xiyouCodeVersion, Levels: so.Levels,
		})
		d = ask(XIYOU_UPDATE_PROGRESS_CMD, body, &upReplies)
		ret, prog, rew := int32(-998), int32(0), ""
		if d != nil {
			var pr proto.XiYouUpdateProgressResp
			if pb.Unmarshal(d, &pr) == nil {
				ret, prog, rew = pr.GetRet(), pr.GetProgress(), pr.GetReward()
			}
		}
		t.Logf("attempt %d: ret=%d respProg=%d reward=%q", attempt, ret, prog, rew)
		if ret == 0 {
			t.Logf(">>> CROSSED %d→%d with SIM-EXACT levels — boss gate cracked", cur, st.next)
			return
		}
		time.Sleep(12 * time.Second)
	}
	t.Errorf("sim-exact levels still refused — server validates more than levels")
}
