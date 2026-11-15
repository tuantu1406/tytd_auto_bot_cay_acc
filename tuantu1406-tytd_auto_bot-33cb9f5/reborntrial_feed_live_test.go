//go:build live

package tytd_go

// READ-mostly live probe for the "thấy người đánh" feed of Luân Hồi Bí Cảnh
// (owner 2026-10-10: "khiêu chiến trong luân hồi lỗi rồi, vào ko thấy đánh,
// mày tự test tự sửa đi"). Open questions answered on the wire:
//
//  1. Which TYPE hex carries RebornTrialChallengeInfoSync pushes? Our
//     SERVER_RBT_BATTLE_NOTIC="4352" was derived from the req−200000
//     convention and NEVER SEEN LIVE — if the real fight sync rides another
//     type, the /luanhoi ring is empty forever and looks exactly like
//     "không thấy đánh".
//  2. Does a REAL challenge (1 attempt, NO items) resolve server-side:
//     ChallengeResp{addScore/reduceScore/result[7]} + bracket re-pull diff.
//
// Cost discipline: exactly ONE challenge against the lowest-score rival base.
//
//	TYTD_LIVE=1 TYTD_RBT_FEED=1 go test -tags "sqlcipher live" -count=1 -run TestRbtFightFeedLive -v -timeout 420s .

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

func TestRbtFightFeedLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_FEED") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_FEED=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710547 // the server whose bracket the owner watches on /luanhoi
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
	db, _ := gorm.Open(sqlite.Open("file:rbtfeed?mode=memory&cache=shared"), &gorm.Config{})
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
		if e.Type == rbtActivityType && e.status(now) == "active" {
			actID = e.ActivityID
		}
	}
	if actID == 0 {
		t.Fatal("no active 172 round right now — rerun inside a battle round (10:00–22:00 ICT)")
	}
	t.Logf("172 actID=%d", actID)

	pending := map[string]int{}
	syncCandidates := map[string]bool{}
	drain := func(d time.Duration, mine map[string]bool) {
		end := time.Now().Add(d)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				pending[a[0]]++
				raw, _ := hex.DecodeString(a[1])
				kind := "PUSH "
				if mine[a[0]] {
					kind = "ANSW"
				}
				t.Logf("%s %s len=%d", kind, a[0], len(raw))
				walkWire(t, raw, "  ", 0)
				if !mine[a[0]] {
					var probe proto.RebornTrialChallengeInfoSync
					if pb.Unmarshal(raw, &probe) == nil && len(raw) > 8 &&
						(probe.GetPlayerId() != 0 || probe.GetTargetUnion() != nil) {
						syncCandidates[a[0]] = true
						t.Logf(">>> FIGHT-SYNC SHAPE on unrequested type %s: player=%d name=%q union=%q target=%d damage=%d",
							a[0], probe.GetPlayerId(), probe.GetName(), probe.GetUnionName(),
							probe.GetTargetUnion().GetUnionId(), probe.GetDamage())
					}
				}
			case <-time.After(300 * time.Millisecond):
			}
		}
	}

	call := func(dec int, body []byte, tag string, ansType string) []byte {
		cmd := fmt.Sprintf("%08x", dec)
		t.Logf("SEND %s (%s)", tag, cmd)
		p.writeChan <- pack.Pack(cmd, body, uid)
		end := time.Now().Add(4 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				raw, _ := hex.DecodeString(a[1])
				if a[0] == ansType {
					pending[a[0]]++
					return raw
				}
				pending[a[0]]++
				walkWire(t, raw, "  ", 0)
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

	// Enter activity + map (free), read our union + battleNum.
	d := call(217200, body(nil), "ENTER_ACTIVITY", SERVER_RBT_ENTER_ACTIVITY)
	var selfUnion int64
	var selfName string
	if d != nil {
		var ea proto.RebornTrialEnterActivityResp
		if pb.Unmarshal(d, &ea) == nil {
			selfUnion = ea.GetUnionId()
			selfName = ea.GetUnionName()
		}
	}
	d = call(217201, body(nil), "ENTER", SERVER_RBT_ENTER)
	if d != nil {
		var en proto.RebornTrialDataResp
		if pb.Unmarshal(d, &en) == nil && en.GetPlayerData() != nil {
			pd := en.GetPlayerData()
			t.Logf("playerData: level=%d hp(energy)=%d battleNum=%d recover=%d exp=%d",
				pd.GetLevel(), pd.GetHp(), pd.GetBattleNum(), pd.GetBattleRecoverTime(), pd.GetExp())
			if pd.GetBattleNum() <= 0 {
				t.Logf("!! battleNum=0 — account has NO attack attempts; challenge will be refused (17205=exhausted)")
			}
		}
	}
	t.Logf("self union=%d name=%q", selfUnion, selfName)
	if selfUnion == 0 {
		t.Fatal("unionId=0 — account guildless? cannot challenge")
	}

	// Bracket (217230) → pick lowest-score rival base as the one target.
	pullBracket := func() []*proto.RebornTrialUnionChallengeMsg {
		d := call(217230, body(nil), "BATTLE_ENTER", SERVER_RBT_BATTLE_ENTER)
		if d == nil {
			return nil
		}
		var r proto.RebornTrialChallengeListResp
		if pb.Unmarshal(d, &r) != nil {
			return nil
		}
		return r.GetUnionMsg()
	}
	before := pullBracket()
	if before == nil {
		t.Fatal("no bracket")
	}
	// min-score rival pick (chip the weakest base — least controversial hit)
	var target int64
	var targetName string
	var bestScore int64
	for _, m := range before {
		b := m.GetBase()
		if b == nil || b.GetUnionId() == 0 || b.GetUnionId() == selfUnion {
			continue
		}
		if target == 0 || b.GetScore() < bestScore {
			target, targetName, bestScore = b.GetUnionId(), b.GetName(), b.GetScore()
		}
	}
	t.Logf("target union=%d (%s) of %d bracket bases", target, targetName, len(before))

	// Passive window BEFORE the challenge: other unions fight all day — if
	// fight syncs ride the wire, we catch the type without spending anything.
	t.Log("passive listen 20s before challenge")
	drain(20*time.Second, map[string]bool{
		SERVER_RBT_ENTER_ACTIVITY: true, SERVER_RBT_ENTER: true, SERVER_RBT_BATTLE_ENTER: true,
	})

	// ONE real challenge (1 attempt; owner-ordered self-test 2026-10-10).
	d = call(217233, body(func(b []byte) []byte {
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		return protowire.AppendVarint(b, uint64(target))
	}), "CHALLENGE", SERVER_RBT_BATTLE_CHALLENGE)
	if d == nil {
		t.Log("!! no 4351 answer at all within 4s — challenge frame lost?")
	} else {
		var cr proto.RebornTrialChallengeResp
		if pb.Unmarshal(d, &cr) == nil {
			t.Logf("CHALLENGE ANSWER ret=%d free=%d recover=%d addScore=%d reduceScore=%d defeatNum=%d rewards=%q",
				cr.GetRet(), cr.GetFree(), cr.GetBattleRecoverTime(), cr.GetAddScore(),
				cr.GetReduceScore(), cr.GetDefeatNum(), cr.GetRewards())
		}
		// result=f7 is not in our generated proto — walk it raw: repeated
		// RebornTrialUnionBattlePlayerMsg (nickName=f4), the hit contributors.
		for _, entry := range rawBytesFields(d, 7) {
			var nick string
			for _, sf := range rawBytesFields(entry, 4) {
				nick = string(sf)
			}
			t.Logf("  result-entry nick=%q", nick)
		}
	}
	// fight sync for our OWN hit may ride right after the answer:
	drain(8*time.Second, map[string]bool{SERVER_RBT_BATTLE_CHALLENGE: true, SERVER_RBT_BATTLE_ENTER: true})

	// Proof-of-fight: re-pull and diff target blood + scores vs `before`.
	after := pullBracket()
	get := func(list []*proto.RebornTrialUnionChallengeMsg, uid int64) (score int64, blood int64) {
		for _, m := range list {
			b := m.GetBase()
			if b != nil && b.GetUnionId() == uid {
				return b.GetScore(), m.GetReduceBlood()
			}
		}
		return -1, -1
	}
	os0, ob0 := get(before, selfUnion)
	ts0, tb0 := get(before, target)
	os1, ob1 := get(after, selfUnion)
	ts1, tb1 := get(after, target)
	t.Logf("SELF %s→%s điểm | blood %d→%d", fmt.Sprint(os0), fmt.Sprint(os1), ob0, ob1)
	t.Logf("TARGET %s→%s điểm | blood %d→%d", fmt.Sprint(ts0), fmt.Sprint(ts1), tb0, tb1)

	t.Log("=== frame tally ===")
	for ty, n := range pending {
		mark := ""
		if ty == SERVER_RBT_BATTLE_NOTIC {
			mark = "  <<<< 4352 CONFIRMED ALIVE"
		}
		t.Logf("type %s seen %d%s", ty, n, mark)
	}
	for ty := range syncCandidates {
		t.Logf(">>> fight-sync push candidate type: %s", ty)
	}
}

// rawBytesFields returns the payloads of every length-delimited field numbered
// want in b — used because our regenerated proto omits some fields (e.g.
// ChallengeResp.result=7) and the wire keeps them.
func rawBytesFields(b []byte, want int) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(b)
		if tagLen < 0 {
			break
		}
		_, _, flen := protowire.ConsumeField(b)
		if flen < 0 {
			break
		}
		if int(num) == want && typ == protowire.BytesType {
			if v, n := protowire.ConsumeBytes(b[tagLen:]); n >= 0 {
				out = append(out, v)
			}
		}
		b = b[flen:]
	}
	return out
}
