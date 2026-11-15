//go:build live

package tytd_go

// SINGLE-ID CLAIM probe (owner 2026-10-17: "đan giới lỗi chưa thấy tự nhận
// thưởng nhiệm vụ ở dược tiên ghé thăm"). The board dump showed a perfect
// dichotomy over ~40 elixir rows: EVERY state=0 row has value<target and
// EVERY state=1 row has value>=target — no "done awaiting claim" row at all.
// Under the union-task reading (state=1 = claimed) that is impossible; it
// fits instead state=1 = completed (the server flips it the moment the task
// finishes), which would make the shipped gate (state!=1) skip the completing
// 502 and NEVER claim. The earlier batched 20503 answered one ret=9 for
// thirteen rows — an atomic batch refusal proves nothing per-id.
//
// This probe claims ONE id per 20503 and reads its own 01f7:
//   • candidates: state=1, value>=target, no selectItem, id in
//     elixirTaskStatic (so ONLY Đan-Giới rows — the ct2/ct560 "other place"
//     rows are never touched), preferring 53xxxx/55xxxx rows the shipped
//     binary never knew (impossible to have been claimed by the bot);
//   • controls: 510029 (claimed long ago by the old bot) → expect ret=9,
//     and any state=0+value>=target row if one exists.
// ret=0 + rewards on a bot-unknown row ⇒ state=1 = completed ⇒ gate must
// ignore state. ret=9 everywhere ⇒ state=1 = claimed and the complaint lies
// elsewhere.
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiClaimProbeLive -v -timeout 300s .

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

func TestDanGioiClaimProbeLive(t *testing.T) {
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
	if !got501 {
		t.Fatal("no 501 snapshot")
	}

	// candidates: Đan-Giới-owned rows (elixirTaskStatic — the ONLY ids ever
	// claimed here; ct2/ct560 "other place" rows are not in the table).
	type cand struct {
		id    int32
		value int64
		st    int32
		why   string
	}
	var cs []cand
	for id, e := range tasks {
		row, known := elixirTaskStatic[id]
		if !known || id < 510000 || id >= 560000 || e.hadSelect {
			continue
		}
		v, err := strconv.ParseInt(e.data.GetValue(), 10, 64)
		if err != nil {
			continue
		}
		st := e.data.GetState()
		if v < row.target {
			continue
		}
		why := fmt.Sprintf("state=1 unknown-to-old-bot")
		if id < 530000 {
			why = "state=1 known-to-old-bot (control)"
		}
		if st == 0 {
			why = "state=0 done-awaiting-claim (should never exist!)"
		}
		cs = append(cs, cand{id, v, st, why})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].id > cs[j].id }) // 55xxxx/53xxxx first
	if len(cs) > 8 {
		cs = cs[:8]
	}
	t.Logf("single-id claim candidates: %d", len(cs))
	for _, c := range cs {
		t.Logf("  T%d val=%d state=%d %s", c.id, c.value, c.st, c.why)
	}

	claimOne := func(id int32) (int32, string, map[int32]int32) {
		reqMSG, _ := pb.Marshal(&proto.TaskGetRewardReqMsg{TaskId: []int32{id}})
		write <- pack.Pack(TASK_GET_REWARD_CMD, reqMSG, uid)
		flipped := map[int32]int32{}
		ret := int32(-1)
		rewards := ""
		end := time.Now().Add(5 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				switch a[0] {
				case SERVER_TASK_GET_REWARD:
					raw, _ := hex.DecodeString(a[1])
					var rr proto.TaskGetRewardRespMsg
					if pb.Unmarshal(raw, &rr) == nil {
						ret = rr.GetRet()
						rewards = rr.GetRewards()
					}
					// keep folding 502s another second after the answer
					end2 := time.Now().Add(1 * time.Second)
					for time.Now().Before(end2) {
						select {
						case a2 := <-all:
							if a2[0] == SERVER_TASK_DATA_SYNC {
								raw2, _ := hex.DecodeString(a2[1])
								var m proto.TaskDataListMsg
								if pb.Unmarshal(raw2, &m) == nil {
									for _, td := range m.GetDataList() {
										flipped[td.GetTaskId()] = td.GetState()
									}
								}
							}
						case <-time.After(100 * time.Millisecond):
						}
					}
					return ret, rewards, flipped
				case SERVER_TASK_DATA_SYNC:
					raw, _ := hex.DecodeString(a[1])
					var m proto.TaskDataListMsg
					if pb.Unmarshal(raw, &m) == nil {
						for _, td := range m.GetDataList() {
							flipped[td.GetTaskId()] = td.GetState()
						}
					}
				}
			case <-time.After(150 * time.Millisecond):
			}
		}
		return ret, rewards, flipped
	}

	for _, c := range cs {
		ret, rewards, flipped := claimOne(c.id)
		t.Logf("CLAIM T%d (val=%d state=%d, %s): ret=%d rewards=%q flips=%v",
			c.id, c.value, c.st, c.why, ret, rewards, flipped)
		if st, ok := flipped[c.id]; ok {
			t.Logf("  T%d state %d → %d after claim", c.id, c.st, st)
		}
	}
}
