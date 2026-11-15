//go:build live

package tytd_go

// Ảo Cảnh OFFLINE SIM with a live role pull (owner 2026-11-09 "test tây du
// ảo cảnh, chạy sim offline với acc <live-user> <live-pass> 710564").
//
// Read-only login: auth → wait type-311 → ONE DATA_INFO → protojson role
// (the exact shape xiyouSecretVisit feeds the goja session). NOTHING else
// goes on the wire — no UPDATE_SEED, no START/SELECT/KILL, zero vé 105286.
// All fights replay in internal/xiyoujs with fabricated doors + random
// seeds, mirroring xiyouSecretFeasibilityScan's orchestration to the letter
// (boss rebuild at progress+add ≥ 99.999, completed minus current room).
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 go test -tags "sqlcipher live" -count=1 -run TestXiYouSecretSimLive -v -timeout 900s .
//	  TYTD_XIYOU_SIM_N=30      runs per secret (default 30)
//	  TYTD_XIYOU_SIM_SECRETS=7,8  force secret ids (default: frontier-1..frontier+1)
//	Role also dumped to /tmp/xiyou_secret_role_<uid>.json for offline repro
//	(node harness.js / rerun the sim without another login).

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"
	"tytd_go/internal/xiyoujs"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	pb "google.golang.org/protobuf/proto"
)

// xiyouSecretOfflineRun = one full fabricated run (same mechanics the
// feasibility scan uses). Returns (won, rooms, levels).
func xiyouSecretOfflineRun(t *testing.T, role json.RawMessage, row *xiyoujs.SecretRow, seed int32) (bool, int, []int32) {
	t.Helper()
	pool := row.Templates
	if len(pool) == 0 {
		t.Fatalf("secret %d: empty pool", row.ID)
	}
	tl := row.Timelimit * 1000
	first := pool[rand.Intn(len(pool))]
	sid, err := xiyoujs.SecretCreate(role, first, seed, tl)
	if err != nil {
		t.Fatal(err)
	}
	defer xiyoujs.SecretDestroy(sid)
	won := false
	rooms := 0
	var levels []int32
	selected := []int32{first}
	for room := 0; room < xiyouSecretMaxRooms; room++ {
		st, err := xiyoujs.SecretRunStep(sid)
		if err != nil {
			return false, room, nil
		}
		rooms = room + 1
		if len(st.Levels) > 0 {
			levels = st.Levels
		}
		if st.Status == "end" {
			won = st.GameStatus == 2
			break
		}
		if st.Status != "room" {
			break // frame cap = not a win
		}
		pick := pool[rand.Intn(len(pool))]
		if xiyouParseProgress(st.Progress)+xiyouSecretAdd(xiyoujs.StarOfTemplate(pick)) >= 99.999 {
			exp, err := xiyoujs.SecretExport(sid)
			if err != nil {
				break
			}
			completed := selected
			if len(completed) > 0 {
				completed = completed[:len(completed)-1]
			}
			if err := xiyoujs.SecretRebuild(sid, exp, pool[0], pool[0], completed); err != nil {
				break
			}
		} else if err := xiyoujs.SecretEnterDoor(sid, pick); err != nil {
			break
		}
		selected = append(selected, pick)
	}
	return won, rooms, levels
}

func TestXiYouSecretSimLive(t *testing.T) {
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
	nRuns := 30
	if v := os.Getenv("TYTD_XIYOU_SIM_N"); v != "" {
		nRuns, _ = strconv.Atoi(v)
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
	var dataReplies [][]byte
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
			if ty == SERVER_XIYOU_DATA {
				mu.Lock()
				dataReplies = append(dataReplies, append([]byte{}, d...))
				mu.Unlock()
			}
			if ty == SERVER_ACTIVITY_LIST || ty == SERVER_ACTIVITY_LIST_UPDATE {
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
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
		t.Fatal("type 311 not open — Tây Du đang đóng, không lấy được role")
	}
	// ONE read-only DATA_INFO (the same pump xiyouSecretVisit fires)
	mu.Lock()
	before := len(dataReplies)
	mu.Unlock()
	body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
	p.write(pack.Pack(XIYOU_DATA_INFO_CMD, body, uid))
	var raw []byte
	lim := time.Now().Add(10 * time.Second)
	for time.Now().Before(lim) && raw == nil {
		mu.Lock()
		if len(dataReplies) > before {
			raw = append([]byte{}, dataReplies[before]...)
		}
		mu.Unlock()
		if raw == nil {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if raw == nil {
		t.Fatal("no DATA_INFO reply")
	}
	var r proto.XiYouPlayerDataInfoResp
	if pb.Unmarshal(raw, &r) != nil || r.GetInfo() == nil {
		t.Fatal("unmarshal DATA_INFO")
	}
	info := r.GetInfo()
	role, err := protojson.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(os.TempDir(), fmt.Sprintf("xiyou_secret_role_%d.json", uid)), role, 0o600)
	t.Logf("role: uid=%d actID=%d power=%s realm=%d frontier(currentSecretId)=%d dump=/tmp/xiyou_secret_role_%d.json",
		uid, actID, info.GetPower(), info.GetRealmId(), info.GetCurrentSecretId(), uid)

	// Which secrets to sim: forced list, else frontier-1..frontier+1.
	var ids []int32
	if v := os.Getenv("TYTD_XIYOU_SIM_SECRETS"); v != "" {
		for _, s := range strings.Split(v, ",") {
			n, _ := strconv.Atoi(strings.TrimSpace(s))
			ids = append(ids, int32(n))
		}
	} else {
		f := info.GetCurrentSecretId()
		if f < 1 {
			f = 1
		}
		for id := f - 1; id <= f+1; id++ {
			if id >= 1 {
				ids = append(ids, id)
			}
		}
	}
	pwf, _ := strconv.ParseFloat(info.GetPower(), 64)
	for _, id := range ids {
		row, err := xiyoujs.SecretRowOf(id)
		if err != nil {
			t.Logf("secret %d: không có dòng (%v) — bỏ", id, err)
			continue
		}
		gate := "OK"
		if pwf < float64(row.VerifyPower) {
			gate = fmt.Sprintf("DƯỚI GATE (power %s < %d) — client sẽ chặn Start", info.GetPower(), row.VerifyPower)
		}
		t.Logf("══ secret %d: verifyPower=%d timelimit=%ds pool=%v — gate: %s",
			id, row.VerifyPower, row.Timelimit, row.Templates, gate)
		wins := 0
		roomSum := 0
		var lastLevels []int32
		t0 := time.Now()
		for i := 1; i <= nRuns; i++ {
			seed := rand.Int31n(1_000_000) + 1
			won, rooms, levels := xiyouSecretOfflineRun(t, role, row, seed)
			if won {
				wins++
				roomSum += rooms
				if len(levels) > 0 {
					lastLevels = levels
				}
			}
			if i%10 == 0 {
				t.Logf("  secret %d: %d/%d thắng (%.1fs)", id, wins, i, time.Since(t0).Seconds())
			}
		}
		avg := 0.0
		if wins > 0 {
			avg = float64(roomSum) / float64(wins)
		}
		t.Logf("══ KẾT LUẬN secret %d: %d/%d thắng offline (%.0f%%), trung bình %.1f phòng/ván thắng, levels=%v — %s",
			id, wins, nRuns, 100*float64(wins)/float64(nRuns), avg, lastLevels,
			map[bool]string{true: "engine sẽ chạy ải này", false: "feasibility scan sẽ park ải này (acc quá yếu)"}[wins > 0])
	}
}
