//go:build live

package tytd_go

// TASK-BOARD TRUTH probe (owner 2026-10-17 bug report: "30/30 rồi vẫn không
// nhổ đi trồng cây nhiệm vụ; đất trống cũng không tự trồng; acc test đang có
// tiến độ 0/40, có 3 nhiệm vụ hoàn thành rồi mà bot không tự nhận — nhiệm vụ
// ở chỗ Dược Tiên ghé thăm/đơn hàng Dược Tiên"). The engine sees ONLY
// {taskId,value,state,selectItem} (501/502 pushes) and matches them against
// the baked elixirTaskStatic — if the server's ids/targets differ, or a
// completed task carries selectItem (hadSelect), the claim/plan gates die
// silently. This probe dumps exactly what the bot sees and replays every
// gate locally:
//
//   1. full task list (501 snapshot + 502 merges), every id 5xxxxx, with
//      inTable / cType / target / value / state / selectItem per row;
//   2. the field (8aae) + the Dược Linh panel (todayGet);
//   3. the ELIXIRTASKGRASS pick replay (cType496, !hadSelect, state!=1,
//      value<target, tree held) and the CLAIM replay (known id, !hadSelect,
//      state!=1, value>=target) — what the loop WOULD send this tick.
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiTaskBoardLive -v -timeout 300s .

import (
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
)

func TestDanGioiTaskBoardLive(t *testing.T) {
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
	all := make(chan [2]string, 16384)
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

	call := func(cmd, ansType string, body []byte, tag string) []byte {
		end := time.Now().Add(3 * time.Second)
		for time.Now().Before(end) {
			select {
			case <-all:
			case <-time.After(150 * time.Millisecond):
			}
		}
		write <- pack.Pack(cmd, body, uid)
		end = time.Now().Add(8 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if a[0] == ansType {
					raw, _ := hex.DecodeString(a[1])
					return raw
				}
			case <-time.After(250 * time.Millisecond):
			}
		}
		t.Logf("!! %s: no %s answer", tag, ansType)
		return nil
	}

	// ---- harvest the task pushes (501 snapshot, then a few seconds of 502s)
	tasks := map[int32]*taskEntry{}
	merge := func(raw []byte, full bool) {
		var msg proto.TaskDataListMsg
		if pb.Unmarshal(raw, &msg) != nil {
			return
		}
		if full {
			tasks = map[int32]*taskEntry{}
		}
		for _, td := range msg.GetDataList() {
			if td.GetTaskId() == 0 {
				continue
			}
			e := &taskEntry{data: td, hadSelect: len(td.GetSelectItem()) > 0}
			if old := tasks[td.GetTaskId()]; old != nil {
				e.hadSelect = old.hadSelect || e.hadSelect
			}
			tasks[td.GetTaskId()] = e
		}
	}
	got501 := false
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case a := <-all:
			raw, _ := hex.DecodeString(a[1])
			switch a[0] {
			case SERVER_TASK_DATA_SEND:
				merge(raw, true)
				got501 = true
			case SERVER_TASK_DATA_SYNC:
				if got501 {
					merge(raw, false)
				}
			}
		case <-time.After(200 * time.Millisecond):
		}
		if got501 && len(tasks) > 0 {
			// one more second to fold trailing 502s, then stop
			end := time.Now().Add(1200 * time.Millisecond)
			for time.Now().Before(end) {
				select {
				case a := <-all:
					if a[0] == SERVER_TASK_DATA_SYNC {
						raw, _ := hex.DecodeString(a[1])
						merge(raw, false)
					}
				case <-time.After(100 * time.Millisecond):
				}
			}
			break
		}
	}
	t.Logf("task list: got501=%v total tasks=%d", got501, len(tasks))

	// ---- the field + the elves counter
	raw := call(S_ELIXIR_ENTER, SERVER_DANGIOI_INFO, func() []byte {
		b, _ := pb.Marshal(&proto.ElixirEnterHomeReq{PlayerId: pb.Int64(int64(uid))})
		return b
	}(), "ENTER")
	// KEEP folding 502s for a few seconds after the ENTER — opening the
	// field may trigger the server to create/push today's 51xxxx dailies.
	postEnd := time.Now().Add(4 * time.Second)
	for time.Now().Before(postEnd) {
		select {
		case a := <-all:
			if a[0] == SERVER_TASK_DATA_SYNC {
				raw2, _ := hex.DecodeString(a[1])
				merge(raw2, false)
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if raw == nil {
		t.Fatal("no ENTER")
	}
	var rsp proto.ElixirEnterHomeResp
	if pb.Unmarshal(raw, &rsp) != nil {
		t.Fatal("bad 8aae")
	}
	byState := map[int32]int{}
	for _, l := range rsp.GetLands() {
		byState[l.GetState()]++
	}
	held := map[int32]bool{}
	bag := map[int32]int32{}
	var bagStr []string
	for _, g := range rsp.GetUserInfo().GetGrassInfos() {
		if g.GetNum() > 0 {
			held[g.GetGrassId()] = true
			bag[g.GetGrassId()] = g.GetNum()
		}
		if g.GetGrassId() >= 860000 && g.GetGrassId() < 861000 {
			bagStr = append(bagStr, fmt.Sprintf("%d:%d", g.GetGrassId(), g.GetNum()))
		}
	}
	raw = call(S_ELIXIR_ELVES_ENTER, SERVER_ELIXIR_ELVES_INFO, []byte{}, "ELVES")
	var today int32
	if raw != nil {
		var er proto.ElixirElvesEnterResp
		if pb.Unmarshal(raw, &er) == nil && er.GetElvesPlayerData() != nil {
			today = er.GetElvesPlayerData().GetTodayGetCount()
		}
	}
	t.Logf("field states=%v | todayGet=%d | task-grass bag: %v", byState, today, bagStr)

	// ---- 1b. the ĐƠN HÀNG DƯỢC TIÊN panel + battle curTask riding 8aae.
	// Owner's "nhiệm vụ ở chỗ dược tiên ghé thăm/đơn hàng dược tiên" board:
	// the ct506 rows count COMPLETED orders, so the board only moves when
	// orders of the right trees get submitted — dump what the panel asks.
	ord := rsp.GetOrderInfo()
	if ord == nil {
		t.Log("orders: 8aae carried NO orderInfo")
	} else {
		t.Logf("orders: destiny=%d pet=%d trans=%d todayDestiny=%d todayPet=%d",
			len(ord.GetDestinyOrderInfos()), len(ord.GetPetOrderInfos()),
			len(ord.GetTransOrderInfos()), ord.GetTodayDestinyCompleteTimes(), ord.GetTodayPetCompleteTimes())
		for i, d := range ord.GetDestinyOrderInfos() {
			var need []string
			for _, g := range d.GetGrassInfos() {
				need = append(need, fmt.Sprintf("%d×%d", g.GetGrassId(), g.GetNum()))
			}
			t.Logf("  destiny[%d] id=%d needs=%v", i, d.GetDestinyId(), need)
		}
		for i, tr := range ord.GetTransOrderInfos() {
			var need []string
			for _, g := range tr.GetGrassInfos() {
				need = append(need, fmt.Sprintf("%d×%d", g.GetGrassId(), g.GetNum()))
			}
			t.Logf("  trans[%d] over=%v needs=%v", i, tr.GetIsOver(), need)
		}
	}
	if ct := rsp.GetCurTask(); ct != nil {
		t.Logf("battle curTask: taskId=%d configId=%d prog=%d slot=%d params=%q",
			ct.GetTaskId(), ct.GetConfigId(), ct.GetCurProgress(), ct.GetSlotId(), ct.GetParams())
	} else {
		t.Log("battle curTask: none on 8aae")
	}

	// ---- 1. dump every elixir-range task the server sent, with the baked
	// table's view of it.
	var ids []int32
	for id := range tasks {
		if id >= 510000 && id < 560000 { // the WHOLE elixir space the server
			// actually uses (51/52 baked; 53 tích-lũy-Đan-Giới & 55 đơn hàng
			// Dược Tiên proved to exist live 2026-10-17 but were never baked).
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	t.Logf("elixir-range task ids on the server: %d", len(ids))
	for _, id := range ids {
		e := tasks[id]
		row, known := elixirTaskStatic[id]
		_, perr := strconv.ParseInt(e.data.GetValue(), 10, 64)
		t.Logf("T%d val=%q state=%d sel=%d | inTable=%v cType=%d target=%d grass=%d parseErr=%v",
			id, e.data.GetValue(), e.data.GetState(), len(e.data.GetSelectItem()),
			known, row.cType, row.target, row.grass, perr)
	}
	// server tasks OUTSIDE the baked range that could be elixir (names are
	// server-side; we only list ids the baked table knows vs not)
	var unknown []int32
	for id := range tasks {
		if _, ok := elixirTaskStatic[id]; !ok && id > 500000 && id < 600000 {
			unknown = append(unknown, id)
		}
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })
	t.Logf("tasks 5xxxxx NOT in baked table: %v", unknown)

	// ---- 2/3. replay the engine's two gates over the REAL data.
	var best int64 = 1 << 40
	pick, pickDesc := int32(0), ""
	var claim []int32
	// blocked diagnostics
	type blk struct {
		id    int32
		why   string
		value int64
		row   elixirTaskRow
	}
	var blocked496 []blk
	for id, e := range tasks {
		row, known := elixirTaskStatic[id]
		if !known {
			continue
		}
		v, perr := strconv.ParseInt(e.data.GetValue(), 10, 64)
		switch {
		case e.hadSelect:
			blocked496 = append(blocked496, blk{id, "hadSelect", v, row})
		case e.data.GetState() == 1:
			blocked496 = append(blocked496, blk{id, "state=1", v, row})
		}
		if !e.hadSelect && e.data.GetState() != 1 && perr == nil && v >= row.target {
			claim = append(claim, id)
		}
		if row.cType != 496 || e.hadSelect || e.data.GetState() == 1 || perr != nil {
			continue
		}
		if v >= row.target {
			continue
		}
		if !held[row.grass] {
			blocked496 = append(blocked496, blk{id, fmt.Sprintf("tree %d NOT held", row.grass), v, row})
			continue
		}
		if gap := row.target - v; gap < best {
			best = gap
			pick = row.grass
			pickDesc = fmt.Sprintf("T%d thu %d× %s (đã %d/%d)", id, row.target, elixirGrassName(row.grass), v, row.target)
		}
	}
	sort.Slice(blocked496, func(i, j int) bool { return blocked496[i].id < blocked496[j].id })
	for _, b := range blocked496 {
		t.Logf("496-blocked T%d cType=%d val=%d why=%s", b.id, b.row.cType, b.value, b.why)
	}
	sort.Slice(claim, func(i, j int) bool { return claim[i] < claim[j] })
	t.Logf("PICK (what elixirTaskGrass would plant): %d %q", pick, pickDesc)
	t.Logf("CLAIM (what elixirClaimTaskRewards would send 20503 for): %v", claim)
	// order-board fallback (bugs 1&2 fix): what elixirFillEmpty plants now
	// when the 496 board is silent — destiny/trans need among held trees.
	og, ogDesc := elixirOrderGrass(&rsp, bag)
	t.Logf("ORDER-PICK (fallback when pick=0): %d %q", og, ogDesc)
	if pick == 0 && og == 0 {
		t.Log(">>> pick=0 — the loop plants NOTHING; todayGet, held map and blocked list above say why")
	}

	// ---- 4. LIVE 20503 experiment (owner 2026-10-17: "có 3 nhiệm vụ hoàn
	// thành rồi mà bot cũng không tự nhận"). EVERY value>=target row on this
	// acc carried state=1 — and our gate SKIPS state=1 believing it means
	// "claimed" (union-task semantics). Either the owner is looking at rows
	// that are done-awaiting-claim with state=1 (gate is wrong) or they are
	// genuinely claimed (gate fine). One batched claim settles it: the
	// server is the arbiter — a real claim pays, a claimed one refuses.
	var probeIDs []int32
	probeIDs = append(probeIDs, claim...)
	var s1 []int32
	for id, e := range tasks {
		if id < 510000 || id >= 560000 {
			continue
		}
		row, known := elixirTaskStatic[id]
		v, err := strconv.ParseInt(e.data.GetValue(), 10, 64)
		if known && !e.hadSelect && e.data.GetState() == 1 && err == nil && v >= row.target {
			s1 = append(s1, id)
		}
	}
	sort.Slice(s1, func(i, j int) bool { return s1[i] < s1[j] })
	probeIDs = append(probeIDs, s1...)
	t.Logf("STATE1 done rows (gate-skipped, now probed): %v", s1)
	if len(probeIDs) > 0 {
		reqMSG, _ := pb.Marshal(&proto.TaskGetRewardReqMsg{TaskId: probeIDs})
		write <- pack.Pack(TASK_GET_REWARD_CMD, reqMSG, uid)
		t.Logf("20503 sent for %v (claim-gate + state=1 experiment)", probeIDs)
		end := time.Now().Add(8 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if a[0] == SERVER_TASK_GET_REWARD {
					raw2, _ := hex.DecodeString(a[1])
					var rr proto.TaskGetRewardRespMsg
					if pb.Unmarshal(raw2, &rr) == nil {
						t.Logf("01f7 answer: ret=%d rewards=%q", rr.GetRet(), rr.GetRewards())
					}
				}
			case <-time.After(150 * time.Millisecond):
			}
		}
		// fold the follow-up 502s — which ids flipped state?
		end = time.Now().Add(4 * time.Second)
		flipped := map[int32]int32{}
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if a[0] == SERVER_TASK_DATA_SYNC {
					raw2, _ := hex.DecodeString(a[1])
					var msg proto.TaskDataListMsg
					if pb.Unmarshal(raw2, &msg) == nil {
						for _, td := range msg.GetDataList() {
							flipped[td.GetTaskId()] = td.GetState()
						}
					}
				}
			case <-time.After(100 * time.Millisecond):
			}
		}
		for _, id := range probeIDs {
			if st, ok := flipped[id]; ok {
				t.Logf("post-claim 502: T%d state=%d (was %d)", id, st, tasks[id].data.GetState())
			}
		}
	}
}
