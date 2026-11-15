//go:build live

package tytd_go

// Live driver (owner 2026-11-05): "thử bruteforce item, mày nhận được seed,
// đổi skill của acc đi rồi test offline, nếu đúng thì update skill đó lên
// acc thật và onehit qua ải".
//
// Pipeline: DATA_INFO → server seed S (held — no engine loop runs here) →
// hill-climb FREE loadout variants (SKILL_WEAR_RUNIC choices among the acc's
// OWNED runicDatas only — never an upgrade op, item-ban law) against row
// progress with seed S → apply winning runes via wear ops → re-DATA_INFO
// (server-as-truth) re-sim on S must still WIN → UPDATE_PROGRESS claim with
// the re-sim levels (one-shot).
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 go test -tags "sqlcipher live" -count=1 -run TestXiyouBruteForceLive -v -timeout 900s .
//	TYTD_BF_BUDGET=400   sim budget (default 400)

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
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

const (
	bfCmdWearSkill   = "000386c1" // 231105 {activityId, skillId(magicId)}
	bfRplWearSkill   = "7981"
	bfCmdWearPassive = "000386c2" // 231106 {activityId, passiveSkillIds[]}
	bfRplWearPassive = "7982"
	bfCmdWearRunic   = "000386c3" // 231107 {activityId, skillId, runicId}
	bfRplWearRunic   = "7983"
	bfRplUpdProg     = "7989"
	bfRplData        = "798c"
	bfRplSeed        = "79a1"
)

func TestXiyouBruteForceLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_XIYOU") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_XIYOU=1")
	}
	budget := 150 // per seed
	if v := os.Getenv("TYTD_BF_BUDGET"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			budget = n
		}
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

	dataCh := make(chan *proto.XiYouPlayerDataMsg, 4)
	seedCh := make(chan *proto.XiYouUpdateSeedResp, 4)
	wearCh := make(chan *proto.XiYouWearSkillRunicResp, 8)
	skillCh := make(chan *proto.XiYouWearSkillResp, 8)
	passCh := make(chan *proto.XiYouWearPassiveSkillResp, 8)
	progCh := make(chan *proto.XiYouUpdateProgressResp, 4)
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
			case bfRplData:
				var r proto.XiYouPlayerDataInfoResp
				if pb.Unmarshal(d, &r) == nil && r.GetRet() == 0 && r.GetInfo() != nil {
					push(dataCh, r.GetInfo())
				}
			case bfRplSeed:
				var r proto.XiYouUpdateSeedResp
				if pb.Unmarshal(d, &r) == nil {
					push(seedCh, &r)
				}
			case bfRplWearRunic:
				var r proto.XiYouWearSkillRunicResp
				if pb.Unmarshal(d, &r) == nil {
					push(wearCh, &r)
				}
			case bfRplWearSkill:
				var r proto.XiYouWearSkillResp
				if pb.Unmarshal(d, &r) == nil {
					push(skillCh, &r)
				}
			case bfRplWearPassive:
				var r proto.XiYouWearPassiveSkillResp
				if pb.Unmarshal(d, &r) == nil {
					push(passCh, &r)
				}
			case bfRplUpdProg:
				var r proto.XiYouUpdateProgressResp
				if pb.Unmarshal(d, &r) == nil {
					push(progCh, &r)
				}
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
	go func() {
		for range p.NotiChan {
		}
	}()
	time.Sleep(2 * time.Second)

	const actID = int32(9063637) // vietdart activity id (fold-verified 2026-11-05)
	getInfo := func() *proto.XiYouPlayerDataMsg {
		b, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
		p.write(pack.Pack(XIYOU_DATA_INFO_CMD, b, p.UID))
		select {
		case m := <-dataCh:
			return m
		case <-time.After(10 * time.Second):
			return nil
		}
	}
	info := getInfo()
	if info == nil {
		t.Fatal("no DATA_INFO")
	}
	row := info.GetProgress()
	st, ok := xiyouStageTable()[row]
	if !ok || st.next == 0 {
		t.Fatalf("no stage row %d", row)
	}
	t.Logf("wall: ải %d → %d (lực %s)", row, st.next, info.GetPower())

	js0, err := protojson.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}

	// ── 2. hill-climb FREE loadout variants per drawn seed ──
	type skillNode struct {
		skillID      int32
		currentRunic int32
		ownedRuneics []int32
	}
	role := map[string]interface{}{}
	if err := json.Unmarshal(js0, &role); err != nil {
		t.Fatal(err)
	}
	skillsOf := func(r map[string]interface{}) []*skillNode {
		out := []*skillNode{}
		sl, _ := r["skills"].([]interface{})
		for _, s := range sl {
			sm, _ := s.(map[string]interface{})
			if sm == nil {
				continue
			}
			n := &skillNode{skillID: int32(sm["skillId"].(float64))}
			if c, ok := sm["currentRunicId"].(float64); ok {
				n.currentRunic = int32(c)
			}
			rd, _ := sm["runicDatas"].([]interface{})
			for _, rdd := range rd {
				rm, _ := rdd.(map[string]interface{})
				if rm == nil {
					continue
				}
				n.ownedRuneics = append(n.ownedRuneics, int32(rm["runicId"].(float64)))
			}
			out = append(out, n)
		}
		return out
	}
	setRunic := func(r map[string]interface{}, skill, runic int32) {
		sl, _ := r["skills"].([]interface{})
		for _, s := range sl {
			sm, _ := s.(map[string]interface{})
			if sm != nil && int32(sm["skillId"].(float64)) == skill {
				if runic == 0 {
					delete(sm, "currentRunicId")
				} else {
					sm["currentRunicId"] = float64(runic)
				}
				return
			}
		}
	}
	marshal := func(r map[string]interface{}) json.RawMessage {
		b, _ := json.Marshal(r)
		return b
	}

	baseline := skillsOf(role)
	// free DPS fix first: skill owns rune stones but wears none → wear first
	// owned (goes into the apply plan as a diff).
	for _, n := range baseline {
		if n.currentRunic == 0 && len(n.ownedRuneics) > 0 {
			setRunic(role, n.skillID, n.ownedRuneics[0])
			t.Logf("mặc định lắp ngọc %d cho skill %d (đang để trống)", n.ownedRuneics[0], n.skillID)
		}
	}
	// ── three FREE levers (client anchors: buildRoleData consumes
	// useSkills→XiYouMagicDB.skillId, passiveSkillIds→bonusId buffs,
	// skills[].currentRunicId→runeStone bonuses) ──
	var mutable []int32
	ownedBy := map[int32][]int32{}
	for _, n := range skillsOf(role) {
		if len(n.ownedRuneics) > 0 {
			mutable = append(mutable, n.skillID)
			ownedBy[n.skillID] = n.ownedRuneics
		}
	}
	// actives: one magic worn per type 1–5, candidate = unlocked (unlock ≤ realmId)
	realm := int32(0)
	if v, ok := role["realmId"].(float64); ok {
		realm = int32(v)
	}
	actByType := map[int32][]int32{}
	var passivePool []int32
	for _, m := range xiyoujs.MagicRows() {
		if m.Unlock > realm {
			continue
		}
		switch {
		case m.Type >= 1 && m.Type <= 5:
			actByType[m.Type] = append(actByType[m.Type], m.ID)
		case m.Type == 6:
			passivePool = append(passivePool, m.ID)
		}
	}
	t.Logf("pool: %d skill đổi ngọc, %d loại active, %d passive (realm %d)",
		len(mutable), len(actByType), len(passivePool), realm)
	if len(mutable) == 0 && len(actByType) == 0 && len(passivePool) == 0 {
		t.Fatal("acc không có lever nào — ngọc/active/passive đều trống")
	}

	activeOf := func(r map[string]interface{}) map[int32]int32 { // type → magicId
		out := map[int32]int32{}
		us, _ := r["useSkills"].([]interface{})
		for _, u := range us {
			if id := int32(u.(float64)); id > 0 {
				out[id/1000] = id
			}
		}
		return out
	}
	setActive := func(r map[string]interface{}, typ, magic int32) {
		cur := activeOf(r)
		cur[typ] = magic
		arr := []interface{}{}
		for tp := int32(1); tp <= 5; tp++ {
			if v, ok := cur[tp]; ok {
				arr = append(arr, float64(v))
			}
		}
		r["useSkills"] = arr
	}
	passiveOf := func(r map[string]interface{}) []int32 {
		var out []int32
		ps, _ := r["passiveSkillIds"].([]interface{})
		for _, p := range ps {
			out = append(out, int32(p.(float64)))
		}
		return out
	}

	pick := func(r map[string]interface{}) map[string]interface{} {
		c := map[string]interface{}{}
		b, _ := json.Marshal(r)
		_ = json.Unmarshal(b, &c)
		return c
	}
	mutate := func(from map[string]interface{}) map[string]interface{} {
		c := pick(from)
		k := 1 + rand.Intn(3)
		for i := 0; i < k; i++ {
			lever := rand.Intn(3)
			// levers with <2 candidates can't mutate — fall through
			if (lever == 0 && len(mutable) == 0) ||
				(lever == 1 && len(actByType) == 0) ||
				(lever == 2 && len(passivePool) < 2) {
				continue
			}
			switch lever {
			case 0: // reroll one skill's worn rune among OWNED stones
				id := mutable[rand.Intn(len(mutable))]
				opts := ownedBy[id]
				setRunic(c, id, opts[rand.Intn(len(opts))])
			case 1: // swap active magic within its type (unlocked pool)
				tps := make([]int32, 0, len(actByType))
				for tp := range actByType {
					tps = append(tps, tp)
				}
				tp := tps[rand.Intn(len(tps))]
				opts := actByType[tp]
				setActive(c, tp, opts[rand.Intn(len(opts))])
			case 2: // swap one passive slot for another unlocked passive
				cur := passiveOf(c)
				if len(cur) == 0 {
					cur = []int32{passivePool[0]}
				}
				slot := rand.Intn(len(cur))
				cur[slot] = passivePool[rand.Intn(len(passivePool))]
				seen := map[int32]bool{}
				arr := []interface{}{}
				for _, v := range cur {
					if !seen[v] {
						seen[v] = true
						arr = append(arr, float64(v))
					}
				}
				c["passiveSkillIds"] = arr
			}
		}
		return c
	}

	// seed loop: a server seed costs nothing to draw (boss fights are free);
	// hill-climb each with a small budget, move on when it's a dead seed.
	maxSeeds := 10
	if v := os.Getenv("TYTD_BF_SEEDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxSeeds = n
		}
	}
	var winner map[string]interface{}
	var seed int32
	totalSims := 0
	for attempt := 1; attempt <= maxSeeds && winner == nil; attempt++ {
		seed = 0
		for try := 0; try < 8 && seed == 0; try++ {
			sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
			p.write(pack.Pack(XIYOU_UPDATE_SEED_CMD, sb, p.UID))
			select {
			case r := <-seedCh:
				if r.GetRet() == 0 {
					seed = r.GetSeed()
				}
			case <-time.After(3 * time.Second):
			}
			if seed == 0 {
				time.Sleep(1500 * time.Millisecond)
			}
		}
		if seed == 0 {
			t.Fatal("no seed from server")
		}
		t.Logf("── seed %d (lượt %d/%d) ──", seed, attempt, maxSeeds)
		best := role
		bestScore, bestHp, bestDead, bestName, bestMs := 1e30, 0.0, true, "", 0
		// top-5 parent archive: greedy best-only hill-climb plateaus in a
		// local opt (1.5M-hp-short seeds need cross-breeding to flip).
		type parent struct {
			role  map[string]interface{}
			score float64
		}
		var archive []parent
		for s := 0; s < budget; s++ {
			cand := best
			if s%10 == 0 {
				cand = role // explore from baseline
			} else if len(archive) > 1 && rand.Intn(3) == 0 {
				cand = archive[rand.Intn(len(archive))].role // cross-breed
			}
			v := mutate(cand)
			res, err := xiyouFight(marshal(v), row, seed)
			totalSims++
			if err != nil {
				t.Fatalf("sim: %v", err)
			}
			if res.Result == 2 {
				winner = v
				t.Logf("WIN offline at sim %d (%dms) với seed %d", s+1, res.Ms, seed)
				break
			}
			// enemy hp left + penalty if player died — trade kills replay as
			// RoleDie on the server too. 1e12 ≫ boss hp scale (~5e7) so dead
			// configs never outrank alive ones; -Ms breaks dead-tier ties
			// toward "player survived longest" = closest to a real win.
			score := res.EnemyHpLeft
			if res.Result == 1 {
				score += 1e12 - float64(res.Ms)
			}
			if score < bestScore {
				bestScore, best, bestHp, bestDead = score, v, res.EnemyHpLeft, res.Result == 1
				bestName, bestMs = res.ResultName, res.Ms
				archive = append(archive, parent{v, score})
				sort.Slice(archive, func(i, j int) bool { return archive[i].score < archive[j].score })
				if len(archive) > 5 {
					archive = archive[:5]
				}
				if s%40 == 39 {
					t.Logf("  sim %d: boss còn %.0f hp (%s %dms player chết=%v)", s+1, bestHp, bestName, bestMs, bestDead)
				}
			}
		}
		if winner == nil {
			t.Logf("seed %d cụt (best: boss còn %.0f hp, %s %dms, player chết=%v) — rút seed khác", seed, bestHp, bestName, bestMs, bestDead)
			time.Sleep(1300 * time.Millisecond) // ret=30 pacing
		}
	}
	if winner == nil {
		t.Fatalf("%d seed / %d sim — không loadout nào thắng; ải này cần lên lực, không phải đổi đồ", maxSeeds, totalSims)
	}

	// ── 3. diff + apply via FREE wear ops ──
	base := map[int32]int32{}
	for _, n := range baseline {
		base[n.skillID] = n.currentRunic
	}
	var plan [][2]int32 // {skillId, runicId}
	for _, n := range skillsOf(winner) {
		if n.currentRunic != base[n.skillID] {
			plan = append(plan, [2]int32{n.skillID, n.currentRunic})
		}
	}
	baseAct := activeOf(role)
	var actPlan []int32 // magicIds to wear
	for tp, want := range activeOf(winner) {
		if baseAct[tp] != want {
			actPlan = append(actPlan, want)
		}
	}
	sort.Slice(actPlan, func(i, j int) bool { return actPlan[i] < actPlan[j] })
	basePass, winPass := passiveOf(role), passiveOf(winner)
	srt := func(a []int32) string {
		b := append([]int32{}, a...)
		sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
		return fmt.Sprint(b)
	}
	passChanged := srt(basePass) != srt(winPass)
	t.Logf("plan: %d WEAR_SKILL + %s + %d SKILL_WEAR_RUNIC (toàn bộ free)",
		len(actPlan), map[bool]string{true: "1 WEAR_PASSIVE", false: "0 passives"}[passChanged], len(plan))

	for _, mid := range actPlan {
		wb, _ := pb.Marshal(&proto.XiYouWearSkillReq{ActivityId: actID, SkillId: mid})
		p.write(pack.Pack(bfCmdWearSkill, wb, p.UID))
		select {
		case r := <-skillCh:
			if r.GetRet() != 0 {
				t.Fatalf("WEAR_SKILL magic %d → ret=%d", mid, r.GetRet())
			}
			t.Logf("wear active %d OK (useSkills=%v)", mid, r.GetUseSkillIds())
		case <-time.After(10 * time.Second):
			t.Fatalf("WEAR_SKILL magic %d im lặng", mid)
		}
	}
	if passChanged {
		req := &proto.XiYouWearPassiveSkillReq{ActivityId: actID}
		req.PassiveSkillIds = winPass
		wb, _ := pb.Marshal(req)
		p.write(pack.Pack(bfCmdWearPassive, wb, p.UID))
		select {
		case r := <-passCh:
			if r.GetRet() != 0 {
				t.Fatalf("WEAR_PASSIVE %v → ret=%d", winPass, r.GetRet())
			}
			t.Logf("wear passives OK (%v)", r.GetPassiveSkillIds())
		case <-time.After(10 * time.Second):
			t.Fatal("WEAR_PASSIVE im lặng")
		}
	}
	for _, pr := range plan {
		wb, _ := pb.Marshal(&proto.XiYouSkillWearRunicReq{ActivityId: actID, SkillId: pr[0], RunicId: pr[1]})
		p.write(pack.Pack(bfCmdWearRunic, wb, p.UID))
		select {
		case r := <-wearCh:
			if r.GetRet() != 0 {
				t.Fatalf("WEAR_RUNIC skill %d runic %d → ret=%d", pr[0], pr[1], r.GetRet())
			}
			t.Logf("wear skill %d ← runic %d OK", pr[0], pr[1])
		case <-time.After(10 * time.Second):
			t.Fatalf("WEAR_RUNIC skill %d im lặng", pr[0])
		}
	}

	// ── 4. server-as-truth re-sim on the SAME held seed ──
	info2 := getInfo()
	if info2 == nil {
		t.Fatal("re-DATA_INFO timeout")
	}
	js2, _ := protojson.Marshal(info2)
	res, err := xiyouFight(js2, row, seed)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != 2 {
		t.Fatalf("server state không thắng nữa (result=%d) — KHÔNG claim", res.Result)
	}
	t.Logf("re-sim server-truth với seed %d: WIN (%dms) — bắn claim", seed, res.Ms)

	// ── 5. one-hit claim ──
	up, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
		ActivityId: actID, Progress: st.next, CodeVersion: xiyouCodeVersion, Levels: res.Levels,
	})
	p.write(pack.Pack(XIYOU_UPDATE_PROGRESS_CMD, up, p.UID))
	select {
	case r := <-progCh:
		if r.GetRet() == 0 {
			t.Logf("★ QUA ẢI %d → %d, reward=%s (one-hit, seed %d)", row, r.GetProgress(), r.GetReward(), seed)
		} else {
			t.Fatalf("claim ret=%d (seed bị xoay sau wear? — cần pipeline lại)", r.GetRet())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("claim im lặng")
	}

}

func push[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
	}
}
