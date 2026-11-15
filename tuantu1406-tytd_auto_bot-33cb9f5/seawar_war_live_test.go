//go:build live

package tytd_go

// OWNER-ORDERED war-board probe — owner 2026-11-11 ("vẽ map khiêu chiến chiến
// trường thương minh đi"). READS ONLY: 234931 challenge list, 234936 enter
// (view a base), 234937 room defenders, plus passive watch of the 8874/8875
// pushes. The attack packet 234938 (SW_CHALLENGE_BATTLE_CMD) NEVER leaves
// this socket — the writer hard-asserts it (ORDER-ONLY law: attacks are the
// owner's button press on /seawarwar, never a test or an auto path).
//
// Order: login → activity row + 21003 fold → ENTER/ENTER_GAME (resident) →
// CHALLENGE_LIST dump (unions, canBattleUnionIds, freeNormalTimes) → for the
// FIRST battleable union: ENTER_CHALLENGE (rooms) + CHALLENGE_INFO per room
// (defenders) — all read-only views. Wire hex of every answer is logged so
// the proto fold can be audited against the client.
//
//	TYTD_LIVE=1 TYTD_SWWAR=1 go test -tags "sqlcipher live" -count=1 -run TestSeaWarWarLive -v -timeout 8m .
//	TYTD_LIVE_USER / TYTD_LIVE_PASS / TYTD_LIVE_SERVER override creds.

import (
	"context"
	"encoding/hex"
	"log"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSeaWarWarLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_SWWAR") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_SWWAR=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710132
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}

	log.SetOutput(os.Stderr)

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
	db, _ := gorm.Open(sqlite.Open("file:swwar?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 256)}
	// NO AutoSeaWar — the auto engine never runs here.

	var sendMu sync.Mutex
	sentTypes := map[string]int{}
	// OWNER-ORDERED fire gate: only with TYTD_SWFIRE=1 does 234938 earn the
	// right to exist on this socket (replaying the sanctioned web order).
	fireMode := os.Getenv("TYTD_SWFIRE") != ""
	var violations []string
	go func() {
		for f := range p.writeChan {
			_, ty, _ := pack.Unpack(f)
			sendMu.Lock()
			sentTypes[ty]++
			if ty == "95aa" || (ty == SW_CHALLENGE_BATTLE_CMD && !fireMode) {
				violations = append(violations, ty) // fruit + attack: never here
			}
			if len(ty) == 4 {
				if n, err := strconv.ParseInt(ty, 16, 32); err == nil && n >= 0x95bb && n <= 0x95c0 {
					violations = append(violations, ty) // war family hard ban (incl. 95c0 declaration — union-level, bot never presses)
				}
			}
			sendMu.Unlock()
			_ = conn.WriteMessage(2, f)
		}
	}()
	readerDone := make(chan struct{})
	pushes := map[string]int{}
	go func() {
		defer close(readerDone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			isMSG, msgType, realData := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.ActList.mergeDetail(p.Username, realData)
				p.swMergeConfig(realData)
				p.stDeliver(msgType, realData)
			case SERVER_SW_CHALLENGE_INFO_SYNC, SERVER_SW_DECLARATION_INFO_SYNC:
				pushes[msgType]++
				t.Logf("PUSH %s len=%d hex=%s", msgType, len(realData), hex.EncodeToString(realData))
				p.swHandleFrame(msgType, realData)
			case SERVER_SW_ENTER, SERVER_SW_ENTER_GAME, SERVER_SW_EXPLORE,
				SERVER_SW_HANDLE_EVENT, SERVER_SW_UNION_WELFARE, SERVER_SW_UNION_WELFARE_REWARD,
				SERVER_SW_UNION_RECEIVE_LIQUID, SERVER_SW_UNION_SEND_LIQUID, SERVER_SW_UNION_LIQUID_RECORD,
				SERVER_SW_HEART_BEAT, SERVER_SW_NEXT_REGION,
				SERVER_SW_USE_ITEM, SERVER_SW_WORSHIP,
				SERVER_SW_ASSIST_LIST, SERVER_SW_ASSIST, SERVER_SW_ASSIST_REWARD,
				SERVER_SW_ASSIST_SYNC, SERVER_SW_ASSIST_SYNC_RAW,
				SERVER_SW_CHALLENGE_LIST, SERVER_SW_ENTER_CHALLENGE,
				SERVER_SW_CHALLENGE_INFO, SERVER_SW_CHALLENGE_BATTLE,
				SERVER_SW_DECLARATION:
				p.swHandleFrame(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-readerDone }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// ── activity row ──
	var actID int32
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		now := time.Now()
		for _, e := range p.ActList.byType(swActivityType) {
			if st := e.status(now); st == "active" || st == "reward" {
				actID = e.ActivityID
			}
		}
		if actID != 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if actID == 0 {
		t.Fatal("no active type-349 activity for this account")
	}
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if data, err := p.stRoundTrip(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body, 15*time.Second); err == nil {
			p.swMergeConfig(data)
		}
	}

	enter, err := p.swEnter(actID)
	if err != nil || enter.GetRet() != 0 {
		t.Fatalf("ENTER refused (err=%v ret=%d)", err, enter.GetRet())
	}
	if eg, err := p.swEnterGame(actID); err != nil || eg.GetRet() != 0 {
		t.Fatalf("ENTER_GAME refused (err=%v ret=%d)", err, eg.GetRet())
	}

	// ── CHALLENGE_LIST (234931, read-only) ──
	list, err := p.swChallengeList(actID)
	if err != nil {
		t.Fatalf("CHALLENGE_LIST transport: %v", err)
	}
	t.Logf("CHALLENGE_LIST: ret=%d unions=%d canBattle=%v freeNormal=%d lastNormal=%d lastAdvanced=%d",
		list.GetRet(), len(list.GetUnionMsg()), list.GetCanBattleUnionIds(),
		list.GetFreeNormalTimes(), list.GetLastNormalUnionId(), list.GetLastAdvancedUnionId())
	for i, m := range list.GetUnionMsg() {
		b := m.GetBase()
		if b == nil {
			continue
		}
		tg := ""
		for _, ti := range m.GetTargetInfos() {
			tg += strconv.FormatInt(ti.GetTargetUnionId(), 10) + "×" + strconv.Itoa(int(ti.GetCount())) + " "
		}
		t.Logf("  BASE %2d: union=%d name=%q flag=%d sv=%d master=%q rank=%d posRank=%d targets[%s]",
			i+1, b.GetUnionId(), b.GetName(), b.GetFlag(), b.GetServerId(), b.GetMasterName(),
			m.GetRank(), m.GetPositionRank(), tg)
	}
	if list.GetRet() != 0 {
		t.Fatalf("CHALLENGE_LIST ret=%d", list.GetRet())
	}
	t.Logf("CHALLENGE_LIST infos=%d raw=%s", len(list.GetInfos()), hex.EncodeToString(mustMarshalProbe(t, list)))
	for i, in := range list.GetInfos() {
		t.Logf("  INFO %2d: player=%d name=%q union=%q target=%d damage=%q bcRoom=%d bcPlayer=%d/%q",
			i+1, in.GetPlayerId(), in.GetName(), in.GetUnionName(), in.GetTargetUnionId(), in.GetDamage(),
			in.GetBeChallengedInfo().GetRoomId(),
			in.GetBeChallengedInfo().GetPlayerBaseDataMsg().GetPlayerId(),
			in.GetBeChallengedInfo().GetPlayerBaseDataMsg().GetNickName())
	}

	// ── ENTER_CHALLENGE + defenders, first battleable union (READS) ──
	var target int64
	for _, id := range list.GetCanBattleUnionIds() {
		if id != 0 {
			target = id
			break
		}
	}
	if target == 0 && len(list.GetUnionMsg()) > 0 {
		target = list.GetUnionMsg()[0].GetBase().GetUnionId()
	}
	if target == 0 {
		t.Log("no union to inspect — board empty for this acc")
	} else {
		ent, err := p.swEnterChallenge(actID, target)
		if err != nil {
			t.Fatalf("ENTER_CHALLENGE transport: %v", err)
		}
		t.Logf("ENTER_CHALLENGE union=%d: ret=%d declarationInfos=%d personalRank=%v",
			target, ent.GetRet(), len(ent.GetDeclarationInfos()),
			ent.GetPersonalRankInfo().GetRank())
		for i, d := range ent.GetDeclarationInfos() {
			t.Logf("  DECL %2d: room=%d type=%d times=%d player=%d/%q t=%d",
				i+1, d.GetRoomId(), d.GetType(), d.GetChallengeTimes(),
				d.GetPlayerBaseDataMsg().GetPlayerId(), d.GetPlayerBaseDataMsg().GetNickName(), d.GetTime())
		}
		if ent.GetRet() == 0 {
			// Probe: when declarationInfos is empty the client still opens
			// a room view — find which roomId the server answers (0? 1?).
			for _, rid := range []int32{0, 1, 2} {
				ri, err := p.swRoomInfo(actID, target, rid)
				if err != nil {
					t.Logf("PROBE ROOM %d transport: %v", rid, err)
					continue
				}
				t.Logf("PROBE ROOM %d: ret=%d defenders=%d hex=%s", rid, ri.GetRet(),
					len(ri.GetBeChallengedInfos()), hex.EncodeToString(mustMarshalProbe(t, ri)))
				for i, d := range ri.GetBeChallengedInfos() {
					t.Logf("  PDEF %2d: room=%d rank=%d defeated=%d player=%d/%q",
						i+1, d.GetRoomId(), d.GetRank(), d.GetDefeatedNum(),
						d.GetPlayerBaseDataMsg().GetPlayerId(), d.GetPlayerBaseDataMsg().GetNickName())
				}
			}
			for _, rid := range swWarRoomIDs(ent, 2) {
				ri, err := p.swRoomInfo(actID, target, rid)
				if err != nil {
					t.Logf("ROOM %d transport: %v", rid, err)
					continue
				}
				t.Logf("ROOM %d: ret=%d defenders=%d", rid, ri.GetRet(), len(ri.GetBeChallengedInfos()))
				for i, d := range ri.GetBeChallengedInfos() {
					t.Logf("  DEF %2d: room=%d rank=%d defeated=%d player=%d/%q",
						i+1, d.GetRoomId(), d.GetRank(), d.GetDefeatedNum(),
						d.GetPlayerBaseDataMsg().GetPlayerId(), d.GetPlayerBaseDataMsg().GetNickName())
				}
			}
			// Plan-only dry run: what WOULD be pressed if the owner tapped —
			// mirrors swWarOrderOne incl. the blind room sweep when the
			// ENTER answer carries no declared rooms.
			rooms := map[int32][]*proto.SeaWarBeChallengedInfo{}
			probeRoomIDs := swWarRoomIDs(ent, swWarMaxRooms)
			if len(probeRoomIDs) == 0 {
				for r := int32(1); r <= int32(swWarMaxRooms); r++ {
					probeRoomIDs = append(probeRoomIDs, r)
				}
			}
			for _, rid := range probeRoomIDs {
				if ri, err := p.swRoomInfo(actID, target, rid); err == nil && ri.GetRet() == 0 {
					rooms[rid] = ri.GetBeChallengedInfos()
				}
			}
			liveInfos := swWarInfosOf(list, target)
			t.Logf("  live infos rows vs union %d: %d", target, len(liveInfos))
			planned := swWarPlan(ent, rooms, liveInfos, target, swWarMaxRooms, swWarMaxPress, 0)
			for i, tg := range planned {
				t.Logf("  PLAN %d: room=%d player=%d/%q", i+1, tg.RoomID, tg.PlayerID, tg.Name)
			}
			// OWNER-ORDERED FIRE MODE (TYTD_SWFIRE=1, owner "dùng acc
			// <live-user> 710132" 2026-11-11): replay the exact web-order volley
			// — RE-ENTER before every press, only lit lệnh (challengeTimes>0),
			// 1s pacing, mirroring swWarOrderOne. DECLARATION is UNION-level
			// (owner: one member declares for the whole bang) — the bot never
			// sends 95c0; it stays hard-banned. Without the env gate 95ba
			// stays absolutely banned too.
			if os.Getenv("TYTD_SWFIRE") != "" {
				pressedF := map[int64]bool{}
				for round := 0; round < swWarMaxPress; round++ {
					entF, err := p.swEnterChallenge(actID, target)
					if err != nil {
						t.Logf("FIRE round %d: enter transport: %v", round+1, err)
						break
					}
					var tgF *swWarTarget
					for _, c := range swWarPlan(entF, rooms, liveInfos, target, swWarMaxRooms, swWarMaxPress, 0) {
						if !pressedF[c.PlayerID] {
							cc := c
							tgF = &cc
							break
						}
					}
					if tgF == nil {
						t.Logf("FIRE round %d: hết lệnh sáng (enter ret=%d declarations=%d)",
							round+1, entF.GetRet(), len(entF.GetDeclarationInfos()))
						break
					}
					pressedF[tgF.PlayerID] = true
					rsp, err := p.swChallenge(actID, target, tgF.RoomID, tgF.PlayerID)
					if err != nil {
						t.Logf("FIRE %d: room=%d player=%d/%q transport: %v", round+1, tgF.RoomID, tgF.PlayerID, tgF.Name, err)
						continue
					}
					t.Logf("FIRE %d: room=%d player=%d/%q ret=%d damage=%q unionScore=%d rewards=%q",
						round+1, tgF.RoomID, tgF.PlayerID, tgF.Name, rsp.GetRet(), rsp.GetDamage(), rsp.GetUnionScore(), rsp.GetRewards())
					time.Sleep(time.Second) // owner pacing "không cần vội"
				}
			}
		}
	}

	// Passive window: let 8874/8875 pushes fall in while idle.
	time.Sleep(6 * time.Second)
	t.Logf("pushes seen: %v", pushes)

	sendMu.Lock()
	defer sendMu.Unlock()
	t.Logf("sent frame types: %v", sentTypes)
	if len(violations) > 0 {
		t.Fatalf("BAN VIOLATION — frames that had NO business leaving the socket: %v", violations)
	}
}

func mustMarshalProbe(t *testing.T, m pb.Message) []byte {
	b, err := pb.Marshal(m)
	if err != nil {
		t.Logf("probe marshal: %v", err)
	}
	return b
}
