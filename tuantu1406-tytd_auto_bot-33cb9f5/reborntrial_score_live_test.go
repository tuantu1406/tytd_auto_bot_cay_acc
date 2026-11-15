//go:build live

package tytd_go

// Live ±0-điểm probe (owner 2026-10-11: "bang chiến trong luân hồi bí cảnh,
// sao tao đánh vẫn +- 0 điểm" + "lỗi: đánh tùm lum"). The web/Telegram attack
// path shows "+addScore điểm" — every observation so far printed +0 even when
// the bracket moved, and the owner reports hits landing on wrong bases.
// This probe answers with raw wire truth, spending EXACTLY ONE attempt:
//
//	a. full bracket dump BEFORE (score/reduceBlood/hasDefeated/shield/position),
//	b. one 217233 vs a chosen valid base + FULL ChallengeResp decode (f5,f6,
//	   f7 entries, f8 — incl. entries our generated proto drops),
//	c. the 4362 fight syncs our own hit produces (leftHp/finalFame after),
//	d. full bracket diff AFTER — per node, so a hit "đánh tùm lum" (changing
//	   bases we never requested) is unmistakable.
//
//	TYTD_LIVE=1 TYTD_RBT_SCORE=1 go test -tags "sqlcipher live" -count=1 -run TestRbtScoreZeroLive -v -timeout 420s .

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRbtScoreZeroLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_SCORE") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_SCORE=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710547 // owner's watched bracket server
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}
	targetPick, _ := strconv.Atoi(os.Getenv("TYTD_RBT_TARGET")) // optional 1-based bracket rank to attack

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
	db, _ := gorm.Open(sqlite.Open("file:rbtscore?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid)}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256)}
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	all := make(chan [2]string, 8192)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			if ty == SERVER_ACTIVITY_LIST || ty == SERVER_ACTIVITY_LIST_UPDATE {
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			}
			select {
			case all <- [2]string{ty, hex.EncodeToString(d)}:
			default:
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-done }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(p.ActList.snapshot()) == 0 {
		time.Sleep(300 * time.Millisecond)
	}
	now := time.Now()
	var actID int32
	for _, e := range p.ActList.snapshot() {
		if e.Type == rbtType && e.status(now) == "active" {
			actID = e.ActivityID
		}
	}
	if actID == 0 {
		t.Fatal("no active 172 round right now")
	}
	t.Logf("actID=%d", actID)

	mine := map[string]bool{
		SERVER_RBT_ENTER_ACTIVITY: true, SERVER_RBT_ENTER: true,
		SERVER_RBT_BATTLE_ENTER: true, SERVER_RBT_BATTLE_CHALLENGE: true,
		SERVER_RBT_BATTLE_UNIONPLAYER: true,
	}
	collectSyncs := func(dur time.Duration) []*proto.RebornTrialChallengeInfoSync {
		syncs := []*proto.RebornTrialChallengeInfoSync{}
		end := time.Now().Add(dur)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if mine[a[0]] {
					continue
				}
				raw, _ := hex.DecodeString(a[1])
				if a[0] == SERVER_RBT_BATTLE_NOTIC {
					var s proto.RebornTrialChallengeInfoSync
					if pb.Unmarshal(raw, &s) == nil {
						syncs = append(syncs, &s)
					}
				}
			case <-time.After(200 * time.Millisecond):
			}
		}
		return syncs
	}
	call := func(dec int, body []byte, tag, ansType string) []byte {
		cmd := fmt.Sprintf("%08x", dec)
		p.writeChan <- pack.Pack(cmd, body, uid)
		end := time.Now().Add(5 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				raw, _ := hex.DecodeString(a[1])
				if a[0] == ansType {
					return raw
				}
			case <-time.After(200 * time.Millisecond):
			}
		}
		return nil
	}
	body := func(extra func([]byte) []byte) []byte {
		b := rbtBody(actID)
		if extra != nil {
			b = extra(b)
		}
		return b
	}

	// Enter + identity
	d := call(217200, body(nil), "ENTER_ACTIVITY", SERVER_RBT_ENTER_ACTIVITY)
	var selfUnion int64
	var selfName string
	if d != nil {
		var ea proto.RebornTrialEnterActivityResp
		if pb.Unmarshal(d, &ea) == nil {
			selfUnion, selfName = ea.GetUnionId(), ea.GetUnionName()
		}
	}
	d = call(217201, body(nil), "ENTER", SERVER_RBT_ENTER)
	var myExp int64
	var battleNum int32
	if d != nil {
		var en proto.RebornTrialDataResp
		if pb.Unmarshal(d, &en) == nil && en.GetPlayerData() != nil {
			pd := en.GetPlayerData()
			battleNum = pd.GetBattleNum()
			myExp = pd.GetExp()
			t.Logf("me: level=%d energy=%d battleNum=%d recover=%d exp=%d",
				pd.GetLevel(), pd.GetHp(), pd.GetBattleNum(), pd.GetBattleRecoverTime(), pd.GetExp())
		}
	}
	t.Logf("self union=%d name=%q", selfUnion, selfName)
	if selfUnion == 0 {
		t.Fatal("guildless — no bracket war possible")
	}

	pullBracket := func(tag string) []*proto.RebornTrialUnionChallengeMsg {
		d := call(217230, body(nil), tag, SERVER_RBT_BATTLE_ENTER)
		if d == nil {
			return nil
		}
		var r proto.RebornTrialChallengeListResp
		if pb.Unmarshal(d, &r) != nil {
			return nil
		}
		return r.GetUnionMsg()
	}
	dumpBracket := func(tag string, list []*proto.RebornTrialUnionChallengeMsg) map[int64][2]int64 {
		m := map[int64][2]int64{}
		for _, x := range list {
			b := x.GetBase()
			if b == nil {
				continue
			}
			m[b.GetUnionId()] = [2]int64{b.GetScore(), x.GetReduceBlood()}
			t.Logf("%s | pos=%2d rank=%2d %-20s id=%d score=%d bloodRed=%d def=%v shield=%d self=%v",
				tag, x.GetPosition(), x.GetRank(), b.GetName(), b.GetUnionId(), b.GetScore(),
				x.GetReduceBlood(), x.GetHasDefeated(), x.GetShieldTriggerTime(), b.GetUnionId() == selfUnion)
		}
		return m
	}

	before := pullBracket("BEFORE")
	if before == nil {
		t.Fatal("no bracket")
	}
	beforeM := dumpBracket("BEFORE", before)

	// Choose target: by rank arg or first valid rival (not defeated, no shield)
	var tgt *proto.RebornTrialUnionChallengeMsg
	if targetPick > 0 {
		i := 0
		for _, x := range before {
			b := x.GetBase()
			if b == nil || b.GetUnionId() == selfUnion {
				continue
			}
			i++
			if i == targetPick {
				tgt = x
			}
		}
	} else {
		for _, x := range before {
			b := x.GetBase()
			if b == nil || b.GetUnionId() == selfUnion {
				continue
			}
			if !x.GetHasDefeated() && x.GetShieldTriggerTime() == 0 {
				tgt = x
				break
			}
		}
	}
	if tgt == nil {
		t.Fatal("no target base found")
	}
	tid := tgt.GetBase().GetUnionId()
	t.Logf("ATTACKING pos=%d rank=%d %s id=%d (score=%d bloodRed=%d def=%v)",
		tgt.GetPosition(), tgt.GetRank(), tgt.GetBase().GetName(), tid, tgt.GetBase().GetScore(), tgt.GetReduceBlood(), tgt.GetHasDefeated())

	// ONE challenge — the whole point of this probe.
	d = call(217233, body(func(b []byte) []byte {
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		return protowire.AppendVarint(b, uint64(tid))
	}), "CHALLENGE", SERVER_RBT_BATTLE_CHALLENGE)
	if d == nil {
		t.Fatal("no 4351 answer")
	}
	t.Log("=== ChallengeResp RAW ===")
	walkWire(t, d, "  ", 0)
	var cr proto.RebornTrialChallengeResp
	if pb.Unmarshal(d, &cr) == nil {
		t.Logf("DECODED ret=%d free=%d recover=%d addScore=%d reduceScore=%d defeatNum=%d rewards=%q",
			cr.GetRet(), cr.GetFree(), cr.GetBattleRecoverTime(), cr.GetAddScore(),
			cr.GetReduceScore(), cr.GetDefeatNum(), cr.GetRewards())
	}
	for i, entry := range rawBytesFields(d, 7) {
		t.Logf("--- result[%d] RAW ---", i)
		walkWire(t, entry, "    ", 1)
	}

	// Our own hit's sync + anything moving in this window.
	syncs := collectSyncs(10 * time.Second)
	for _, s := range syncs {
		tu := s.GetTargetUnion()
		t.Logf("SYNC attacker=%q(pid %d) union=%q target=%d leftHp=%d finalFame=%d hasDef=%v dmg=%d",
			s.GetName(), s.GetPlayerId(), s.GetUnionName(), tu.GetUnionId(), tu.GetLeftHp(),
			tu.GetFinalFame(), tu.GetHasDefeated(), s.GetDamage())
	}

	after := pullBracket("AFTER")
	afterM := dumpBracket("AFTER", after)
	t.Log("=== DIFF (every base whose score/blood changed) ===")
	changed, changedTarget, changedSelf := 0, false, false
	for id, b := range beforeM {
		a, ok := afterM[id]
		if !ok {
			continue
		}
		if a[0] != b[0] || a[1] != b[1] {
			changed++
			who := ""
			if id == tid {
				who += " <<TARGET"
				changedTarget = true
			}
			if id == selfUnion {
				who += " <<SELF"
				changedSelf = true
			}
			t.Logf("  id=%d score %d→%d (%+d) | blood %d→%d (%+d)%s",
				id, b[0], a[0], a[0]-b[0], b[1], a[1], a[1]-b[1], who)
		}
	}
	if changed == 0 {
		t.Log("  (nothing moved — allies idle or scoring is decoupled)")
	}
	t.Logf("VERDICT: ourChallengeScore=%d±%d targetMoved=%v selfMoved=%v totalMovedBases=%d battleNumBefore=%d myExp=%d",
		cr.GetAddScore(), cr.GetReduceScore(), changedTarget, changedSelf, changed, battleNum, myExp)
}
