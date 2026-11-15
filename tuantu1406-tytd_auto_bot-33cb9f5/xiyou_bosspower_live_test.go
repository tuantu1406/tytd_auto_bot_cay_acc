//go:build live

package tytd_go

// TÂY DU PHỤC MA — decisive boss gate test (owner 2026-11-04: "ải nhỏ cuối
// mỗi cấp thì không được [lừa server], kiểm tra cho kĩ").
//
// Accumulated live evidence says the gate on CROSSING a boss row (UPDATE_
// PROGRESS from type=2 row) is power >= that row's MONSTER combatPower
// (StageDB combatPower column, not the client-side verifyPower):
//
//	crossed: 10207(cp 26844), 10307(cp 36545), 10407(cp 54406) — power 68053
//	blocked: 10507(cp 73815) — power 68053 < 73815 (ret 31170/31171 forever,
//	       survives cooldown waits AND kill rounds)
//
// This test proves causality: the account sits parked on 10507. Raise power
// (wear the pending equipment drops — WEAR 231103 — then REALM_UP 231125 if
// needed) past 73815, then retry the crossing with cooldown-safe spacing.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 \
//	  go test -tags "sqlcipher live" -count=1 -run TestXiYouBossPowerGateLive -v -timeout 600s .

import (
	"context"
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

func TestXiYouBossPowerGateLive(t *testing.T) {
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
	var dataReplies, upReplies, wearReplies, realmReplies [][]byte
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
			case "797f": // 231103 WEAR resp
				wearReplies = append(wearReplies, append([]byte{}, d...))
			case "7995": // 231125 REALM_UP resp
				realmReplies = append(realmReplies, append([]byte{}, d...))
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
	// state via DATA_INFO; returns progress, power, energy, unDeal list
	state := func() (int32, int64, int64, []*proto.XiYouUnDealEquipmentDataMsg) {
		mu.Lock()
		before := len(dataReplies)
		mu.Unlock()
		body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
		p.write(pack.Pack(XIYOU_DATA_INFO_CMD, body, uid))
		d := waitNew(&dataReplies, before, 8*time.Second)
		if d == nil {
			return -1, 0, 0, nil
		}
		var r proto.XiYouPlayerDataInfoResp
		if pb.Unmarshal(d, &r) != nil || r.GetInfo() == nil {
			return -1, 0, 0, nil
		}
		pw, _ := strconv.ParseInt(r.GetInfo().GetPower(), 10, 64)
		var energy int64
		for _, it := range r.GetInfo().GetItems() {
			if it.GetItemId() == 105280 {
				energy = it.GetItemCount()
			}
		}
		return r.GetInfo().GetProgress(), pw, energy, r.GetInfo().GetUnDealEquipments()
	}
	assertProgress := func(want int32) (int32, string) {
		mu.Lock()
		before := len(upReplies)
		mu.Unlock()
		body, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: want, CodeVersion: xiyouCodeVersion,
		})
		p.write(pack.Pack(XIYOU_UPDATE_PROGRESS_CMD, body, uid))
		d := waitNew(&upReplies, before, 8*time.Second)
		if d == nil {
			return -998, "timeout"
		}
		var r proto.XiYouUpdateProgressResp
		if pb.Unmarshal(d, &r) != nil {
			return -999, "unmarshal"
		}
		return r.GetRet(), r.GetReward()
	}

	progress, power, energy, undeal := state()
	t.Logf("STATE progress=%d power=%d energy105280=%d unDealEquip=%d", progress, power, energy, len(undeal))
	stages := xiyouLoadStages(t)
	row, ok := stages[progress]
	if !ok {
		t.Fatalf("progress %d not in StageDB", progress)
	}
	t.Logf("row %d type=%d next=%d (gate hypothesis: cross blocked iff type==2 && power < combatPower — combatPower not in probe table, expected 73815@10507)",
		progress, row.Type, row.NextStage)

	// ── A. wide-spacing asserts rule out the cooldown for good ───────────
	if row.Type == 2 {
		for i := 0; i < 3; i++ {
			ret, rew := assertProgress(row.NextStage)
			t.Logf("wide-spacing assert %d→%d (try %d): ret=%d reward=%q", progress, row.NextStage, i+1, ret, rew)
			if ret == 0 {
				t.Logf(">>> crossed without raising power — power gate hypothesis WRONG")
				return
			}
			time.Sleep(10 * time.Second)
		}
	}

	// ── B. raise power: wear best pending drops, then realm-up ────────────
	const target int64 = 73815
	for attempt := 0; power < target && attempt < 30; attempt++ {
		if len(undeal) == 0 {
			// generate drops
			rand, _ := pb.Marshal(&proto.XiYouRandEquipmentReq{ActivityId: actID, Num: 5})
			p.write(pack.Pack(XIYOU_RAND_EQUIPMENT_CMD, rand, uid))
			time.Sleep(2 * time.Second)
			_, power, energy, undeal = state()
			t.Logf("attempt %d: rand'd, power=%d energy=%d unDeal=%d", attempt, power, energy, len(undeal))
			continue
		}
		// wear the pending drop with the biggest power value
		sort.SliceStable(undeal, func(i, j int) bool {
			a, _ := strconv.ParseInt(undeal[i].GetPower(), 10, 64)
			b, _ := strconv.ParseInt(undeal[j].GetPower(), 10, 64)
			return a > b
		})
		best := undeal[0]
		mu.Lock()
		before := len(wearReplies)
		mu.Unlock()
		body, _ := pb.Marshal(&proto.XiYouWearEquipmentReq{ActivityId: actID, Id: best.GetEquipment().GetId()})
		p.write(pack.Pack("000386bf", body, uid)) // 231103
		d := waitNew(&wearReplies, before, 6*time.Second)
		var wret int32
		if d != nil {
			var r proto.XiYouWearEquipmentResp
			if pb.Unmarshal(d, &r) == nil {
				wret = r.GetRet()
			}
		} else {
			wret = -998
		}
		_, power, energy, undeal = state()
		t.Logf("attempt %d: wear id=%d (pow=%s) ret=%d → power=%d energy=%d unDeal=%d",
			attempt, best.GetEquipment().GetId(), best.GetPower(), wret, power, energy, len(undeal))
		if wret != 0 && wret != -998 {
			// this drop can't be worn — drop it from the working list
			undeal = undeal[1:]
		} else if len(undeal) > 1 {
			undeal = undeal[1:]
		}
	}
	t.Logf("after wearing: power=%d (target %d)", power, target)

	// ── C. retry crossing ─────────────────────────────────────────────────
	ret, rew := assertProgress(row.NextStage)
	t.Logf(">>> CROSS retry at power=%d (monster combatPower 73815): ret=%d reward=%q", power, ret, rew)
	if ret == 0 {
		t.Logf(">>> CONFIRMED: boss crossing gates on power >= combatPower — unlocked once power crossed the line")
		return
	}
	// realm-up as second lever
	for i := 0; i < 10; i++ {
		mu.Lock()
		before := len(realmReplies)
		mu.Unlock()
		body, _ := pb.Marshal(&proto.XiYouRealmUpLevReq{ActivityId: actID})
		p.write(pack.Pack("000386d5", body, uid)) // 231125
		waitNew(&realmReplies, before, 5*time.Second)
		_, power, _, _ = state()
		ret, rew = assertProgress(row.NextStage)
		t.Logf("realm-up %d: power=%d assert ret=%d reward=%q", i+1, power, ret, rew)
		if ret == 0 {
			t.Logf(">>> CONFIRMED via realm-up: power=%d now >= gate", power)
			return
		}
		time.Sleep(6 * time.Second)
	}
	t.Errorf("never unlocked — power=%d; gate is NOT simply power>=combatPower", power)
}
