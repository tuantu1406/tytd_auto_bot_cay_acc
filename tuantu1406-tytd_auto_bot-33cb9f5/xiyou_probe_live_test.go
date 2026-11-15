//go:build live

package tytd_go

// TÂY DU PHỤC MA (XiYou 231xxx) live probe — read-mostly discovery pass on a
// REAL account (owner 2026-11-04: "dùng acc <live-user>/.../710564 để phân
// tích"). NOTHING here is the engine; the engine (player_xiyou.go) is
// event-driven and never round-trips. This test exists to answer:
//
//	1. which activityId of the live list is the XiYou activity (blind-fire
//	   PLAYER_DATA_INFO 231116 at every open activity, ret==0 wins);
//	2. what the server says about the account (progress/power/energy items);
//	3. ret codes of the one-shot main-loop commands: ATTACK 231101,
//	   RAND_EQUIPMENT 231102, UPDATE_PROGRESS 231113 (does the server trust
//	   a client-asserted progress, incl. PAST a boss row?).
//
// Wire facts (from /workspace/remoteAll/taydu + pack): request opcode is the
// full 231xxx id big-endian ("000386cc"-style), answers arrive on id%100000
// as a 2-byte dispatch type (elixir 235502→"8aae" pattern).
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouProbeLive -v -timeout 180s .

import (
	"context"
	"encoding/json"
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

// XiYou wire opcodes live in player_xiyou.go (shared with the engine).

// xiyouStageRow — minimal XiYouStageDB row (from the decoded CDN table).
type xiyouStageRow struct {
	Type        int32 `json:"type"`
	NextStage   int32 `json:"nextStage"`
	VerifyPower int32 `json:"verifyPower"`
}

func xiyouLoadStages(t *testing.T) map[int32]xiyouStageRow {
	t.Helper()
	raw, err := os.ReadFile("/workspace/remoteAll/taydu/XiYouStageDB.decoded.json")
	if err != nil {
		t.Fatalf("read StageDB: %v", err)
	}
	var wrap struct {
		XiYouStageDB []json.RawMessage `json:"XiYouStageDB"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		t.Fatalf("StageDB json: %v", err)
	}
	if len(wrap.XiYouStageDB) < 3 {
		t.Fatal("StageDB too short")
	}
	var cols []string
	if err := json.Unmarshal(wrap.XiYouStageDB[1], &cols); err != nil {
		t.Fatalf("StageDB cols: %v", err)
	}
	idx := map[string]int{}
	for i, c := range cols {
		idx[c] = i
	}
	out := map[int32]xiyouStageRow{}
	for _, rm := range wrap.XiYouStageDB[2:] {
		var row []interface{}
		if err := json.Unmarshal(rm, &row); err != nil {
			continue
		}
		get := func(c string) int32 {
			f, _ := row[idx[c]].(float64)
			return int32(f)
		}
		out[get("id")] = xiyouStageRow{
			Type: get("type"), NextStage: get("nextStage"), VerifyPower: get("verifyPower"),
		}
	}
	if len(out) == 0 {
		t.Fatal("StageDB empty after parse")
	}
	return out
}

func TestXiYouProbeLive(t *testing.T) {
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
	_ = token
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
		Notify:    false,
		NotiChan:  make(chan NotifyMGS, 256),
		writeChan: make(chan []byte, 256),
		ctx:       ctx,
	}
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	// ── event-driven capture ─────────────────────────────────────────────
	var mu sync.Mutex
	xiyouReplies := map[string][][]byte{} // dispatch type → payloads
	fire := func(cmd string, body []byte) {
		select {
		case p.writeChan <- pack.Pack(cmd, body, p.UID):
		case <-ctx.Done():
		}
	}
	// AUTH — same packet the real login sends (player.go doLogin); without it
	// the server never pushes the activity list.
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	fire(AUTH_CMD, authMSG)
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
			switch ty {
			case SERVER_XIYOU_DATA, SERVER_XIYOU_SYNC, SERVER_XIYOU_ATTACK,
				SERVER_XIYOU_RAND_EQUIP, SERVER_XIYOU_UPD_PROGRESS, SERVER_XIYOU_RESOLVE:
				mu.Lock()
				xiyouReplies[ty] = append(xiyouReplies[ty], append([]byte{}, d...))
				mu.Unlock()
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			}
		}
	}()
	// keep-alive
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				fire(PING_CMD, []byte{})
			}
		}
	}()

	// let the login activity push land
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.ActList.snapshot()) > 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	// ── 1. find the XiYou activityId: blind DATA_INFO at every open act ──
	entries := p.ActList.snapshot()
	nowMs := time.Now().UnixMilli()
	var open []int32
	for _, e := range entries {
		if e.BeginTime <= nowMs && nowMs < e.EndTime {
			open = append(open, e.ActivityID)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ActivityID < entries[j].ActivityID })
	t.Logf("activities total=%d open=%v", len(entries), open)
	if len(open) == 0 {
		t.Fatal("no activity inside its battle window right now — nothing to probe")
	}
	// The resp carries NO activityId echo — attribute serially: one candidate
	// at a time, wait for a NEW payload, keep it if ret==0 && info!=nil.
	var actID int32
	var info *proto.XiYouPlayerDataMsg
	for _, id := range open {
		mu.Lock()
		before := len(xiyouReplies[SERVER_XIYOU_DATA])
		mu.Unlock()
		body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: id})
		fire(XIYOU_DATA_INFO_CMD, body)
		time.Sleep(1500 * time.Millisecond)
		mu.Lock()
		newOnes := append([][]byte{}, xiyouReplies[SERVER_XIYOU_DATA][before:]...)
		mu.Unlock()
		for _, d := range newOnes {
			var r proto.XiYouPlayerDataInfoResp
			if err := pb.Unmarshal(d, &r); err != nil {
				t.Logf("data_info unmarshal: %v", err)
				continue
			}
			t.Logf("DATA_INFO act=%d ret=%d info=%v", id, r.GetRet(), r.GetInfo() != nil)
			if r.GetRet() == 0 && r.GetInfo() != nil && actID == 0 {
				actID = id
				info = r.GetInfo()
			}
		}
		if actID != 0 {
			break
		}
	}
	if info == nil {
		t.Fatal("no DATA_INFO ret=0 with info — XiYou activity not open on this account?")
	}
	if actID == 0 {
		t.Fatal("could not attribute the ret=0 info to an activityId")
	}
	for _, e := range entries {
		if e.ActivityID == actID {
			t.Logf("XIYOU ENTRY type=%d childType=%d group=%d begin=%d end=%d",
				e.Type, e.ChildType, e.GroupType, e.BeginTime, e.EndTime)
		}
	}
	t.Logf("XIYOU ACTIVITY %d — progress=%d realm=%d power=%s exp=%s todayFree=%d items=%d",
		actID, info.GetProgress(), info.GetRealmId(), info.GetPower(), info.GetExp(),
		info.GetTodayFreeTime(), len(info.GetItems()))
	for _, it := range info.GetItems() {
		t.Logf("  item %d x%d", it.GetItemId(), it.GetItemCount())
	}

	stages := xiyouLoadStages(t)
	row := func(id int32) (xiyouStageRow, bool) { r, ok := stages[id]; return r, ok }

	cur, ok := row(info.GetProgress())
	if !ok {
		t.Fatalf("progress %d not in StageDB", info.GetProgress())
	}
	t.Logf("stage %d: type=%d next=%d", info.GetProgress(), cur.Type, cur.NextStage)

	// ── 3. one-shot command probes, printed rets only ────────────────────
	fireAtk := func(times int32) {
		body, _ := pb.Marshal(&proto.XiYouAttackReq{ActivityId: actID, Times: times})
		fire(XIYOU_ATTACK_CMD, body)
	}
	fireRand := func(num int32) {
		body, _ := pb.Marshal(&proto.XiYouRandEquipmentReq{ActivityId: actID, Num: num})
		fire(XIYOU_RAND_EQUIPMENT_CMD, body)
	}

	// official backAuto single tick: ATTACK times=5, RAND num=2
	fireAtk(5)
	fireRand(2)
	time.Sleep(4 * time.Second)
	mu.Lock()
	atks := append([][]byte{}, xiyouReplies[SERVER_XIYOU_ATTACK]...)
	rands := append([][]byte{}, xiyouReplies[SERVER_XIYOU_RAND_EQUIP]...)
	mu.Unlock()
	for _, d := range atks {
		var r proto.XiYouAttackResp
		pb.Unmarshal(d, &r)
		t.Logf("ATTACK ret=%d", r.GetRet())
	}
	for _, d := range rands {
		var r proto.XiYouRandEquipmentResp
		pb.Unmarshal(d, &r)
		t.Logf("RAND_EQUIPMENT ret=%d drops=%d realm=%d exp=%s", r.GetRet(), len(r.GetDropReward()), r.GetRealmId(), r.GetExp())
	}

	// progress bump (client asserts next stage; server either trusts or not)
	if cur.Type != 2 {
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: cur.NextStage, CodeVersion: "1.0.4",
		})
		fire(XIYOU_UPDATE_PROGRESS_CMD, body)
		time.Sleep(3 * time.Second)
		mu.Lock()
		ups := append([][]byte{}, xiyouReplies[SERVER_XIYOU_UPD_PROGRESS]...)
		mu.Unlock()
		for _, d := range ups {
			var r proto.XiYouUpdateProgressResp
			pb.Unmarshal(d, &r)
			t.Logf("UPDATE_PROGRESS %d→ ret=%d progress=%d reward=%q", cur.NextStage, r.GetRet(), r.GetProgress(), r.GetReward())
		}
	} else {
		t.Logf("on boss row %d — probing progress PAST boss (client only does this after a real fight)", info.GetProgress())
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: cur.NextStage, CodeVersion: "1.0.4",
		})
		fire(XIYOU_UPDATE_PROGRESS_CMD, body)
		time.Sleep(3 * time.Second)
		mu.Lock()
		ups := append([][]byte{}, xiyouReplies[SERVER_XIYOU_UPD_PROGRESS]...)
		mu.Unlock()
		for _, d := range ups {
			var r proto.XiYouUpdateProgressResp
			pb.Unmarshal(d, &r)
			t.Logf("UPDATE_PROGRESS past-boss→%d ret=%d progress=%d reward=%q", cur.NextStage, r.GetRet(), r.GetProgress(), r.GetReward())
		}
	}

	// re-read to see whether the server actually moved us
	body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
	fire(XIYOU_DATA_INFO_CMD, body)
	time.Sleep(3 * time.Second)
	mu.Lock()
	tail := append([][]byte{}, xiyouReplies[SERVER_XIYOU_DATA]...)
	mu.Unlock()
	if n := len(tail); n > 0 {
		var r proto.XiYouPlayerDataInfoResp
		if pb.Unmarshal(tail[n-1], &r) == nil && r.GetInfo() != nil {
			t.Logf("AFTER: progress=%d power=%s exp=%s", r.GetInfo().GetProgress(), r.GetInfo().GetPower(), r.GetInfo().GetExp())
		}
	}
}
