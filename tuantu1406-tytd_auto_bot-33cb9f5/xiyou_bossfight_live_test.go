//go:build live

package tytd_go

// TÂY DU PHỤC MA — owner-path boss fight test (owner 2026-11-04, sent 3×:
// "không tốn năng lượng, ải 5 thì mày phải gửi request lên server đánh, nó
// trả kết quả win thì mày mới gửi request qua ải được").
//
// Static-mined facts this test leans on (XiYou.decoded.proto + client JS):
//   - S_XIYOU_ATTACK 231101 resp carries ONLY ret → the "fight result" is
//     NOT in an ATTACK answer; the only main-line carrier of a battle
//     result is UPDATE_PROGRESS{levels} (PRNG-consumption checkpoints).
//   - Main-task claim = 21004 {activityId, conditionId} (reqGetActReward);
//     task types: 385 Vượt ải(param=boss row), 386 dùng nhân sâm(energy),
//     387 tách trang bị, 388 đánh bại N Tiểu Yêu, 396 đạt cấp N.
//   - Boss fight client flow = reqRoleData + UPDATE_SEED{activityId} →
//     local sim → UPDATE_PROGRESS(next, levels).
//
// Account is parked on boss row 10507 (next 20101) with mainConditonId=
// 100014 "Đánh bại 10 Tiểu Yêu" value=0. Plan:
//   A. dump state + task chain; claim every READY cond via 21004 (refuel
//      energy 105280 from task rewards);
//   B. ATTACK rounds on the boss row — do they tick the kill task? (server
//      counts ATTACK kills for the CURRENT row);
//   C. walk the sequential task chain with claims as far as it goes
//      (wear drops / realm-up / bless as needed) toward 100031
//      "Vượt ải 10507";
//   D. with the boss task current: proper UPDATE_SEED → UPDATE_PROGRESS
//      variants (empty levels, seed-guided levels) and log every ret.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouBossFightLive -v -timeout 600s .

import (
	"context"
	"os"
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

func TestXiYouBossFightLive(t *testing.T) {
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
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.write(pack.Pack(AUTH_CMD, authMSG, uid))

	var mu sync.Mutex
	var dataReplies, upReplies, atkReplies, seedReplies, detailReplies, claimReplies [][]byte
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
			case SERVER_XIYOU_ATTACK:
				atkReplies = append(atkReplies, append([]byte{}, d...))
			case "79a1": // 231137 UPDATE_SEED resp
				seedReplies = append(seedReplies, append([]byte{}, d...))
			case SERVER_ACTIVITY_DETAIL:
				detailReplies = append(detailReplies, append([]byte{}, d...))
			case SERVER_ACTIVITY_COND_REWARD, SERVER_ACTIVITY_TOKEN_REWARD:
				claimReplies = append(claimReplies, append([]byte{}, d...))
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

	deadline := time.Now().Add(20 * time.Second)
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
	state := func() (int32, int64, int64, int32) {
		body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
		d := ask(XIYOU_DATA_INFO_CMD, body, &dataReplies)
		if d == nil {
			return -1, 0, 0, -1
		}
		var r proto.XiYouPlayerDataInfoResp
		if pb.Unmarshal(d, &r) != nil || r.GetInfo() == nil {
			return -1, 0, 0, -1
		}
		pw, _ := strconv.ParseInt(r.GetInfo().GetPower(), 10, 64)
		var energy int64
		for _, it := range r.GetInfo().GetItems() {
			if it.GetItemId() == 105280 {
				energy = it.GetItemCount()
			}
		}
		return r.GetInfo().GetProgress(), pw, energy, r.GetInfo().GetMainConditonId()
	}
	type taskRow struct {
		desc  string
		value string
		got   bool
		ready bool
	}
	// dumpTasks re-fires 21003 and returns current conditionDataList,
	// marking ready = value >= config target (param[0]; type 385 target=1).
	dumpTasks := func() map[int32]taskRow {
		mu.Lock()
		before := len(detailReplies)
		mu.Unlock()
		body, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID})
		p.write(pack.Pack(ACTIVITY_GET_DATA_CMD, body, uid))
		d := waitNew(&detailReplies, before, 10*time.Second)
		out := map[int32]taskRow{}
		if d == nil {
			t.Logf("taskdump: no 21003 answer")
			return out
		}
		var rsp proto.RspGetActivityDetail
		if pb.Unmarshal(d, &rsp) != nil || rsp.GetActivity() == nil {
			return out
		}
		cfg := map[int32]*proto.ActivityConditionConfig{}
		if dc := rsp.GetActivity().GetDetailConfig(); dc != nil && dc.GetCommonConfig() != nil {
			for _, c := range dc.GetCommonConfig().GetConditionConfig() {
				cfg[c.GetConditionId()] = c
			}
		}
		for _, cd := range rsp.GetActivity().GetConditionDataList() {
			c := cfg[cd.GetConditionId()]
			row := taskRow{value: cd.GetValue(), got: cd.GetIsGetReward()}
			target := int64(1)
			if c != nil {
				row.desc = c.GetDesc()
				if c.GetType() != 385 && len(c.GetParam()) > 0 {
					target, _ = strconv.ParseInt(c.GetParam()[0], 10, 64)
				}
			}
			v, _ := strconv.ParseInt(row.value, 10, 64)
			row.ready = !row.got && v >= target && target > 0
			out[cd.GetConditionId()] = row
		}
		return out
	}
	claimCond := func(condID int32, token bool) (int32, string) {
		body, _ := pb.Marshal(&proto.ReqGetActivityConditionReward{ActivityId: pb.Int32(actID), ConditionId: pb.Int32(condID)})
		cmd, resp := ACTIVITY_COND_REWARD_CMD, "claim21004"
		if token {
			cmd, resp = ACTIVITY_TOKEN_REWARD_CMD, "claim21033"
		}
		d := ask(cmd, body, &claimReplies)
		if d == nil {
			return -998, resp + " timeout"
		}
		var r proto.RspGetActivityConditionReward
		if pb.Unmarshal(d, &r) != nil {
			return -999, resp + " unmarshal"
		}
		return r.GetRet(), r.GetRewards()
	}
	assertProgress := func(want int32, levels []int32) (int32, int32, string) {
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: want, CodeVersion: xiyouCodeVersion, Levels: levels,
		})
		d := ask(XIYOU_UPDATE_PROGRESS_CMD, body, &upReplies)
		if d == nil {
			return -998, 0, "timeout"
		}
		var r proto.XiYouUpdateProgressResp
		if pb.Unmarshal(d, &r) != nil {
			return -999, 0, "unmarshal"
		}
		return r.GetRet(), r.GetProgress(), r.GetReward()
	}

	// ── A. state + chain snapshot ──────────────────────────────────────────
	progress, power, energy, cond := state()
	t.Logf("STATE progress=%d power=%d energy=%d mainCond=%d", progress, power, energy, cond)
	tasks := dumpTasks()
	for _, id := range []int32{100014, 100015, 100016, 100017, 100018, 100031} {
		if s, ok := tasks[id]; ok {
			t.Logf("task %d: %+v", id, s)
		}
	}

	// ── B. claim anything READY (task rewards refuel energy 105280) ───────
	for id, s := range tasks {
		if id < 100000 || id > 100100 || !s.ready {
			continue
		}
		ret, rew := claimCond(id, false)
		t.Logf("claim 21004 cond=%d: ret=%d rewards=%q  (%+v)", id, ret, rew, s)
		if ret == -998 || ret == -999 {
			ret2, rew2 := claimCond(id, true)
			t.Logf("claim 21033 cond=%d: ret=%d rewards=%q", id, ret2, rew2)
		}
	}
	progress, power, energy, cond = state()
	t.Logf("after claims: progress=%d power=%d energy=%d mainCond=%d", progress, power, energy, cond)

	// ── C. ATTACK rounds on the boss row — does the kill task tick? ───────
	for round := 1; round <= 4; round++ {
		ab, _ := pb.Marshal(&proto.XiYouAttackReq{ActivityId: actID, Times: 5})
		d := ask(XIYOU_ATTACK_CMD, ab, &atkReplies)
		atkRet := int32(-998)
		if d != nil {
			var r proto.XiYouAttackResp
			if pb.Unmarshal(d, &r) == nil {
				atkRet = r.GetRet()
			}
		}
		time.Sleep(1500 * time.Millisecond)
		tasks = dumpTasks()
		_, _, energy, cond = state()
		t.Logf("attack round %d (times=5): atkRet=%d energy=%d cond=%d kill100014=%+v",
			round, atkRet, energy, cond, tasks[100014])
		time.Sleep(4 * time.Second)
	}

	// ── D. claim loop: walk the sequential chain while it advances ────────
	for step := 0; step < 12; step++ {
		tasks = dumpTasks()
		claimed := false
		for id, s := range tasks {
			if id < 100000 || id > 100100 || !s.ready {
				continue
			}
			ret, rew := claimCond(id, false)
			t.Logf("chain claim %d: ret=%d rewards=%q (%+v)", id, ret, rew, s)
			claimed = true
			break
		}
		_, _, energy, cond = state()
		t.Logf("chain step %d: mainCond=%d energy=%d", step, cond, energy)
		if cond == 100031 {
			t.Logf(">>> main task now 100031 (Vượt ải 10507)")
			break
		}
		if !claimed {
			t.Logf("chain stuck at cond=%d — nothing claimable (task: %+v)", cond, tasks[cond])
			break
		}
		time.Sleep(3 * time.Second)
	}

	// ── E. boss crossing via the client fight sequence ─────────────────────
	if progress == 10507 {
		variants := []struct {
			name   string
			pre    func()
			levels []int32
		}{
			{"plain", nil, nil},
			{"seed-then-empty", func() {
				sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
				d := ask("000386e1", sb, &seedReplies) // 231137
				if d == nil {
					t.Logf("seed: no resp")
					return
				}
				var r proto.XiYouUpdateSeedResp
				pb.Unmarshal(d, &r)
				t.Logf("UPDATE_SEED ret=%d seed=%d", r.GetRet(), r.GetSeed())
			}, nil},
			{"seed+attack-then-empty", func() {
				sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
				ask("000386e1", sb, &seedReplies)
				ab, _ := pb.Marshal(&proto.XiYouAttackReq{ActivityId: actID, Times: 1})
				ask(XIYOU_ATTACK_CMD, ab, &atkReplies)
			}, nil},
			{"seed+levels", func() {
				sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
				ask("000386e1", sb, &seedReplies)
			}, []int32{20, 120, 220, 320}},
			{"levels-only", nil, []int32{20, 120, 220, 320}},
		}
		for _, v := range variants {
			if v.pre != nil {
				v.pre()
			}
			ret, prog, rew := assertProgress(20101, v.levels)
			t.Logf("cross try [%s]: ret=%d respProgress=%d reward=%q", v.name, ret, prog, rew)
			if ret == 0 {
				t.Logf(">>> CROSSED 10507 via [%s] — owner path confirmed", v.name)
				break
			}
			time.Sleep(8 * time.Second)
		}
		progress, power, energy, cond = state()
		t.Logf("END progress=%d power=%d energy=%d cond=%d", progress, power, energy, cond)
	}
}
