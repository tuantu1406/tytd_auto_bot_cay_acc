//go:build live

package tytd_go

// LIVE check of the tinhnang.jpg strategy table (owner "live test chưa"
// 2026-10-09). Runs the real auto loop against the test account and pairs
// every pending event (type + option costs, straight off ENTER answers) with
// the FIRST 217206 the engine fires for it — exactly what the table governs:
//   Bet(7)/Alchemy(6): first pick must be the HIGHEST cost that passes the
//     gate (bet has no gate, so plain max);
//   Suppress(1-entry {1,1,1}) and Buy {1}: first pick must be option 1;
//   Rescue {2,2,3} / Sell {1,2,3}: logged against the expected cycle (the
//     per-encounter cursor can legitimately differ when events resolve in
//     other paths, so these are reported, not failed).
// Refusal/no-op fan retries after the FIRST pick are ignored — the table
// only decides what is tried first.
//
//	TYTD_LIVE=1 TYTD_RBT_STRATEGY=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialStrategyLive -v -timeout 420s .

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
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type lhEvSnap struct {
	typ   int32
	exp   int64
	costs map[int32]int64 // option order → costExp
}

func TestRebornTrialStrategyLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_STRATEGY") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_STRATEGY=1")
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
	db, _ := gorm.Open(sqlite.Open("file:rbtstrat?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid), AutoRebornTrial: true}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel, Notify: true,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 64),
		rbtPoll: 2 * time.Second, AutoRebornTrial: true} // Player-object gate (owner 2026-10-27)
	out := make(chan [2]string, 4096)
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
			if isMSG, ty, d := pack.Unpack(f); isMSG {
				select {
				case out <- [2]string{ty, hex.EncodeToString(d)}:
				default:
				}
			}
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
			switch ty {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			case SERVER_RBT_ENTER_ACTIVITY, SERVER_RBT_ENTER, SERVER_RBT_WORSHIP, SERVER_RBT_EXPLORE,
				SERVER_RBT_SELECT_OPTION, SERVER_RBT_SKIP_EVENT, SERVER_RBT_BONUS, SERVER_RBT_BONUS_RESULT, SERVER_RBT_BONUS_ITEM, SERVER_RBT_SEND_BONUS_ITEM,
				SERVER_RBT_GUESS_DATA, SERVER_RBT_GUESS_REWARD, SERVER_RBT_AUTO_EXPLORE:
				p.rbtHandleFrame(ty, d)
			case SERVER_ACTIVITY_BUY_GOODS, SERVER_ACTIVITY_COND_REWARD, SERVER_ACTIVITY_CROSS_RECHARGE, SERVER_ACTIVITY_TOKEN_REWARD:
				p.rbtHandleFrame(ty, d)
			case "03eb":
				p.rbtMergeConfig(d)
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

	rbtLive := func() bool {
		now := time.Now()
		for _, e := range p.ActList.byType(rbtActivityType) {
			if e.status(now) == "active" {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && !rbtLive() {
		time.Sleep(500 * time.Millisecond)
	}
	if !rbtLive() {
		t.Skip("type 172 not active in the list")
	}
	actID, beginMs, _ := p.rbtActiveActivity()
	state, round, _ := rbtPhase(beginMs, p.rbtCfg(), time.Now())
	if state != rbtBattle {
		t.Logf("phase %d round %d — không phải battle: engine sẽ bị refuse; chỉ theo dõi được ENTER flow", state, round)
	} else {
		t.Logf("round %d đang mở — chạy loop thật", round)
	}
	go p.rebornTrialAutoLoopAfter(1 * time.Second)

	// Pair pending events with their first select.
	var pend *lhEvSnap
	selSeen := false
	type rec struct {
		typ, order, cost, wantCost int64
		ok                         bool
		note                       string
	}
	var recs []rec
	seqCnt := map[int32]int{}

	snapFrom := func(pd *proto.RebornTrialPlayerData) *lhEvSnap {
		ev := pd.GetEvent()
		if ev == nil || ev.GetStat() != 0 || len(ev.GetOption()) == 0 {
			return nil
		}
		s := &lhEvSnap{typ: ev.GetType(), exp: pd.GetExp(), costs: map[int32]int64{}}
		for _, o := range ev.GetOption() {
			s.costs[o.GetOrder()] = o.GetCostExp()
		}
		return s
	}
	updateExp := func(pd *proto.RebornTrialPlayerData) {
		if pend != nil && pd.GetExp() > 0 {
			pend.exp = pd.GetExp()
		}
	}

	end := time.Now().Add(4 * time.Minute)
	for time.Now().Before(end) {
		select {
		case a := <-all:
			raw, _ := hex.DecodeString(a[1])
			switch a[0] {
			case SERVER_RBT_ENTER, SERVER_RBT_AUTO_EXPLORE:
				var rsp proto.RebornTrialDataResp
				if pb.Unmarshal(raw, &rsp) == nil {
					if rsp.GetRet() == 0 && rsp.PlayerData != nil {
						if s := snapFrom(rsp.GetPlayerData()); s != nil {
							pend, selSeen = s, false
						} else {
							pend = nil
						}
						updateExp(rsp.GetPlayerData())
					}
				}
			case SERVER_RBT_SELECT_OPTION:
				var rsp proto.RebornTrialSelectOptionResp
				if pb.Unmarshal(raw, &rsp) == nil {
					if pend != nil && rsp.GetExp() > 0 {
						pend.exp = rsp.GetExp()
					}
					if ev := rsp.GetEvent(); ev == nil || ev.GetStat() != 0 {
						pend = nil // resolved / gone
					}
				}
			case SERVER_RBT_SKIP_EVENT:
				pend = nil
			}
		case a := <-out:
			// Unpack reports only the LOW two funcType bytes: outbound 217206
			// rides as "5076", not the full "00035076" constant.
			if a[0] != RBT_SELECT_OPTION_CMD[4:] || pend == nil || selSeen {
				continue
			}
			selSeen = true
			raw, _ := hex.DecodeString(a[1])
			var rq proto.RebornTrialSelectOptionReq
			if pb.Unmarshal(raw, &rq) != nil {
				continue
			}
			ord := rq.GetSelectIndex()
			got := pend.costs[ord]
			r := rec{typ: int64(pend.typ), order: int64(ord), cost: got}
			switch {
			case rbtEvExpensive[pend.typ]:
				// must be the highest affordable (bet: no gate → highest)
				want := int64(-1)
				wantOrder := int32(-1)
				for o, c := range pend.costs {
					if c > want && (pend.typ != rbtEvAlchemy || c <= pend.exp) {
						want, wantOrder = c, o
					}
				}
				r.wantCost = want
				r.ok = wantOrder == ord
				r.note = "đắt nhất"
			case pend.typ == rbtEvRelieve:
				r.note = "độ kiếp — tự path riêng, khỏi chấm"
				r.ok = true
			default:
				if seq, known := rbtStrategySeq[pend.typ]; known {
					n := seqCnt[pend.typ]
					seqCnt[pend.typ]++
					want1 := int64(seq[n%len(seq)])
					r.note = fmt.Sprintf("seq[%d]=%d", n, want1)
					r.ok = int64(ord)+1 == want1
				} else {
					r.note = "type ngoài bảng — fan cũ"
					r.ok = true
				}
			}
			recs = append(recs, r)
			t.Logf("EVENT type %d: chọn order %d (cost %d) — %s, %s", pend.typ, ord, got, r.note, map[bool]string{true: "ĐÚNG", false: "SAI"}[r.ok])
		case <-ctx.Done():
			end = time.Now()
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	if actID > 0 {
		t.Logf("(actID %d)", actID)
	}
	fmt.Printf("=== strategy live records: %d ===\n", len(recs))
	fails := 0
	for _, r := range recs {
		line := fmt.Sprintf("type %d pick order %d cost %d — %s %s", r.typ, r.order, r.cost, r.note, map[bool]string{true: "OK", false: "SAI"}[r.ok])
		fmt.Println(line)
		if !r.ok && (rbtEvExpensive[int32(r.typ)] || r.typ == rbtEvSuppress || r.typ == rbtEvBuy) {
			fails++ // only hard-guaranteed rules fail the run; cycle types reported
		}
	}
	if len(recs) == 0 {
		t.Log("KHÔNG gặp event nào trong 4 phút (round đóng/hp hết?) — table chưa được verify live, chạy lại trong round mở")
		return
	}
	if fails > 0 {
		t.Fatalf("%d lần chọn SAI luật đắt nhất/luôn-1", fails)
	}
}
