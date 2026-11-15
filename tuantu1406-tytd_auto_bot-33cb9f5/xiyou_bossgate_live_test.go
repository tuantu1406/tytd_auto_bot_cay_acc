//go:build live

package tytd_go

// TÂY DU PHỤC MA boss-gate live test — settles the owner dispute (2026-11-04:
// "mỗi cấp có 5 ải nhỏ, mấy ải nhỏ đầu thì có thể lừa server, nhưng ải nhỏ
// cuối mỗi cấp thì không được, kiểm tra cho kĩ").
//
// Hypothesis under test: UPDATE_PROGRESS is gated on the StageDB verifyPower
// column — normal rows have verifyPower=0 (always pass = "lừa được"), boss
// rows carry a real threshold (fail once account power drops below it).
// The earlier probe only ever crossed bosses whose verifyPower (6k–24k) sat
// far BELOW the account's power (~68k), so it proved nothing about the gate.
//
// Plan (drives UPDATE_PROGRESS manually — engine stays off, p.Xiyou=false):
//
//	1. DATA_INFO → server truth: progress + power;
//	2. walk the stage chain asserting nextStage; log every ret TOGETHER with
//	   the row's verifyPower vs live power — the smoking-gun table;
//	3. stop when a boss assert is refused (ret!=0) → owner right, gate found;
//	   or walk ~60 asserts → gate not on verifyPower;
//	4. ILLEGAL-SKIP control: assert progress two rows ahead of the legit
//	   next — distinguishes "server validates the chain" from "blind trust
//	   except the power gate";
//	5. re-read DATA_INFO: does progress persist server-side after a refused
//	   boss assert (i.e. server really kept us on the boss row)?
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouBossGateLive -v -timeout 300s .

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

func TestXiYouBossGateLive(t *testing.T) {
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
	upReplies := [][]byte{}
	dataReplies := [][]byte{}
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
			case SERVER_XIYOU_UPD_PROGRESS:
				mu.Lock()
				upReplies = append(upReplies, append([]byte{}, d...))
				mu.Unlock()
			case SERVER_XIYOU_DATA:
				mu.Lock()
				dataReplies = append(dataReplies, append([]byte{}, d...))
				mu.Unlock()
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
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
	if len(p.ActList.byType(XIYOU_ACTIVITY_TYPE)) == 0 {
		t.Fatal("activity type 311 not open — nothing to test")
	}
	nowMs := time.Now().UnixMilli()
	var actID int32
	for _, e := range p.ActList.byType(XIYOU_ACTIVITY_TYPE) {
		if e.BeginTime <= nowMs && nowMs < e.EndTime {
			actID = e.ActivityID
		}
	}
	if actID == 0 {
		t.Fatal("type 311 entry not inside its battle window")
	}

	readState := func() (progress int32, power int64) {
		mu.Lock()
		before := len(dataReplies)
		mu.Unlock()
		body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
		p.write(pack.Pack(XIYOU_DATA_INFO_CMD, body, uid))
		lim := time.Now().Add(6 * time.Second)
		for time.Now().Before(lim) {
			mu.Lock()
			got := len(dataReplies) > before
			var d []byte
			if got {
				d = append([]byte{}, dataReplies[len(dataReplies)-1]...)
			}
			mu.Unlock()
			if got {
				var r proto.XiYouPlayerDataInfoResp
				if pb.Unmarshal(d, &r) == nil && r.GetRet() == 0 && r.GetInfo() != nil {
					pw, _ := strconv.ParseInt(r.GetInfo().GetPower(), 10, 64)
					return r.GetInfo().GetProgress(), pw
				}
				return -1, 0
			}
			time.Sleep(200 * time.Millisecond)
		}
		return -1, 0
	}

	// assert progress=want; wait for the NEW 7989 reply; return its ret/reward.
	assertProgress := func(want int32) (int32, string) {
		mu.Lock()
		before := len(upReplies)
		mu.Unlock()
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: want, CodeVersion: xiyouCodeVersion,
		})
		p.write(pack.Pack(XIYOU_UPDATE_PROGRESS_CMD, body, uid))
		lim := time.Now().Add(6 * time.Second)
		for time.Now().Before(lim) {
			mu.Lock()
			n := len(upReplies)
			var d []byte
			if n > before {
				d = append([]byte{}, upReplies[before]...)
			}
			mu.Unlock()
			if d != nil {
				var r proto.XiYouUpdateProgressResp
				if pb.Unmarshal(d, &r) == nil {
					return r.GetRet(), r.GetReward()
				}
				return -999, "unmarshal-fail"
			}
			time.Sleep(200 * time.Millisecond)
		}
		return -998, "timeout"
	}

	progress, power := readState()
	if progress <= 0 {
		t.Fatal("no DATA_INFO state")
	}
	t.Logf("START progress=%d power=%d", progress, power)

	stages := xiyouLoadStages(t)

	// ── PRE. verifyPower gate test at a boss WAY above account power ──────
	// Client gates boss challenge on power >= verifyPower (index.pretty.js
	// 16587/16677/19059 — shows Text_202 "power not enough"). Does the
	// SERVER re-check? Jump straight onto chapter-3 boss 30407
	// (verifyPower 69625 > power 68053) and try to cross it. The jump
	// attempt doubles as the chain-validation control (illegal skip).
	{
		const30407 := int32(30407)
		ret, _ := assertProgress(const30407) // jump from wherever we are — illegal if chain validated
		t.Logf("JUMP-onto-boss-30407 (illegal skip control) ret=%d", ret)
		if ret == 0 {
			cur, _ := readState()
			t.Logf("server progress after jump = %d (chain NOT validated if 30407)", cur)
			if cur == const30407 {
				time.Sleep(6 * time.Second) // clear the ~4s progress cooldown
				ret2, rew2 := assertProgress(stages[const30407].NextStage)
				t.Logf(">>> CROSS boss 30407 (verifyPower=%d) with power=%d: ret=%d reward=%q",
					stages[const30407].VerifyPower, power, ret2, rew2)
				if ret2 != 0 {
					// rule out cooldown: wait longer, retry once
					time.Sleep(8 * time.Second)
					ret3, rew3 := assertProgress(stages[const30407].NextStage)
					t.Logf(">>> retry after 8s: ret=%d reward=%q (nonzero ⇒ SERVER ENFORCES verifyPower)", ret3, rew3)
				} else {
					t.Logf(">>> boss crossed despite power < verifyPower — server does NOT enforce it")
				}
				// park the account back near its real progress: walk back is
				// impossible; leave it — owner designated this acc for testing.
			}
		} else {
			t.Logf(">>> jump refused (ret=%d) — server validates the chain; walking instead", ret)
		}
		// reset to server truth for the walk phase
		progress, power = readState()
		if progress <= 0 {
			t.Fatal("no state after PRE phase")
		}
		t.Logf("RESUME progress=%d power=%d", progress, power)
	}

	// ── 0. COOLDOWN-vs-KILLS discriminator ────────────────────────────────
	// Two observations left two hypotheses alive:
	//   (a) server requires kills (ATTACK/RAND) since the last accepted
	//       UPDATE_PROGRESS;
	//   (b) server enforces a min interval between accepted UPDATE_PROGRESS
	//       (the successful "kill round" may have just burned ~5s of wall
	//       clock). Discriminate: retry the refused assert with WAITING
	//       ONLY — never ATTACK — and see if pure elapsed time unlocks it.
	// Walk step-by-step; on the first refusal retry every 2s WITHOUT kills.
	cur := progress
	for i := 0; i < 8; i++ {
		row, ok := stages[cur]
		if !ok || row.NextStage == 0 {
			t.Logf("chain tail at %d", cur)
			return
		}
		t0 := time.Now()
		ret, reward := assertProgress(row.NextStage)
		t.Logf("step %d→%d type=%d verifyPower=%d ret=%d reward=%q", cur, row.NextStage, row.Type, row.VerifyPower, ret, reward)
		if ret == 0 {
			cur = row.NextStage
			continue
		}
		// refused — pure-wait retries, NO ATTACK/RAND fired
		unlockedByWait := false
		for round := 1; round <= 8; round++ {
			time.Sleep(2 * time.Second)
			ret2, rew2 := assertProgress(row.NextStage)
			t.Logf("pure-wait retry %d (%.1fs since first try): ret=%d reward=%q",
				round, time.Since(t0).Seconds(), ret2, rew2)
			if ret2 == 0 {
				unlockedByWait = true
				cur = row.NextStage
				break
			}
		}
		if unlockedByWait {
			t.Logf(">>> UNLOCKED BY WAITING ONLY (no kills) — gate (b): progress-update cooldown ~%.0fs, at row type=%d",
				time.Since(t0).Seconds(), row.Type)
		} else {
			t.Logf(">>> STAYED LOCKED through pure waiting — gate (a): needs actual kills, at row type=%d", row.Type)
			// feed kills and confirm that's what unlocks
			for round := 1; round <= 6; round++ {
				atk, _ := pb.Marshal(&proto.XiYouAttackReq{ActivityId: actID, Times: xiyouAttackTimes})
				p.write(pack.Pack(XIYOU_ATTACK_CMD, atk, uid))
				rand, _ := pb.Marshal(&proto.XiYouRandEquipmentReq{ActivityId: actID, Num: xiyouRandNum})
				p.write(pack.Pack(XIYOU_RAND_EQUIPMENT_CMD, rand, uid))
				time.Sleep(1 * time.Second)
				ret3, _ := assertProgress(row.NextStage)
				t.Logf("kill-round %d: ret=%d", round, ret3)
				if ret3 == 0 {
					cur = row.NextStage
					break
				}
			}
		}
	}
	t.Logf("discriminator walk ended at %d", cur)
	return
}
