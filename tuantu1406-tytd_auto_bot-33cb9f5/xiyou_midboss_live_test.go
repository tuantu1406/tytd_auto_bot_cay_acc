//go:build live

package tytd_go

// TÂY DU PHỤC MA — "đứng TRÊN ải boss giữa chương thì chỉ gửi là qua?"
// Owner's claim 2026-11-05: 19-3 (190307) is a mid-chapter boss, so a plain
// UPDATE_PROGRESS should cross it without a fight. Earlier blind asserts
// from 190307 answered 31170/31171 — this probe isolates why, with clean
// spacing (>4s apart, no interleaved claims):
//
//	A: blind assert 190401 (no levels, no seed) — the owner's "chỉ gửi" test
//	B: blind assert 190402 (skip TWO rows — does the server auto-skip ahead?)
//	C: UPDATE_SEED first, then blind assert 190401 (seed handshake present?)
//	D: UPDATE_SEED → run the client sim → assert with sim-exact levels
//	   (the proven chapter-boss path) — also reports WIN/LOSS verdict.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouMidBossLive -v -timeout 600s .

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

func TestXiYouMidBossLive(t *testing.T) {
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
			case SERVER_XIYOU_UPDATE_SEED:
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
	report := func(tag string, d []byte) int32 {
		ret, prog, rew := int32(-998), int32(0), ""
		if d != nil {
			var pr proto.XiYouUpdateProgressResp
			if pb.Unmarshal(d, &pr) == nil {
				ret, prog, rew = pr.GetRet(), pr.GetProgress(), pr.GetReward()
			}
		}
		t.Logf("%s: ret=%d respProg=%d reward=%q", tag, ret, prog, rew)
		return ret
	}

	// current state
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
	t.Logf("account %s stands on row %d (typ=%d) → next %d, power=%s", user, cur, st.typ, st.next, info.GetPower())

	// ── A: owner's "chỉ gửi là qua" — blind assert, clean 6s spacing
	time.Sleep(6 * time.Second)
	body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
		ActivityId: actID, Progress: st.next, CodeVersion: xiyouCodeVersion,
	})
	retA := report("A blind→next", ask(XIYOU_UPDATE_PROGRESS_CMD, body, &upReplies))

	// ── B: claim TWO rows ahead (auto-skip-ahead test)
	time.Sleep(6 * time.Second)
	if nxt, ok2 := xiyouStageTable()[st.next]; ok2 {
		body2, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: nxt.next, CodeVersion: xiyouCodeVersion,
		})
		report("B blind→next+1", ask(XIYOU_UPDATE_PROGRESS_CMD, body2, &upReplies))
	}

	// ── C: seed handshake then blind assert
	time.Sleep(6 * time.Second)
	sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
	seedRep := ask(XIYOU_UPDATE_SEED_CMD, sb, &seedReplies)
	seed := int32(0)
	if seedRep != nil {
		var sr proto.XiYouUpdateSeedResp
		pb.Unmarshal(seedRep, &sr)
		seed = sr.GetSeed()
	}
	t.Logf("C: UPDATE_SEED → seed=%d", seed)
	time.Sleep(6 * time.Second)
	retC := report("C seed+blind→next", ask(XIYOU_UPDATE_PROGRESS_CMD, body, &upReplies))

	if retA == 0 || retC == 0 {
		t.Logf(">>> OWNER WAS RIGHT: mid-chapter boss crosses without a fight")
		return
	}

	// ── D: full sim path (proven chapter-boss crossing)
	js, err := protojson.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(os.TempDir(), "xiyou_role_mid.json")
	if err := os.WriteFile(tmp, js, 0o600); err != nil {
		t.Fatal(err)
	}
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
		// take last { line
		lines := splitLast(out)
		if err2 := json.Unmarshal(lines, &so); err2 != nil {
			t.Fatalf("harness output %q: %v", string(out), err)
		}
	}
	t.Logf("D sim: %s frames=%d ms=%d levels=%v", so.ResultName, so.Frames, so.Ms, so.Levels)
	if so.Result != 2 {
		t.Logf(">>> D: sim THUA — acc thật không qua nổi ải này (server gate là thật)")
		return
	}
	sleepFor := time.Duration(so.Ms) * time.Millisecond
	if sleepFor > 50*time.Second {
		sleepFor = 50 * time.Second
	}
	time.Sleep(sleepFor)
	bodyD, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
		ActivityId: actID, Progress: st.next, CodeVersion: xiyouCodeVersion, Levels: so.Levels,
	})
	retD := report("D sim-levels→next", ask(XIYOU_UPDATE_PROGRESS_CMD, bodyD, &upReplies))
	if retD == 0 {
		t.Logf(">>> CROSSED %d→%d via sim — mid-chapter boss standing-on-it NEEDS the fight", cur, st.next)
	}
}

func splitLast(out []byte) []byte {
	start := 0
	for i, b := range out {
		if b == '{' {
			start = i
		}
	}
	return out[start:]
}
