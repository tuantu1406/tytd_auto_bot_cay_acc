//go:build live

package tytd_go

// LIVE PROBE (owner 2026-11-08): "thêm auto nhận reward" — the post-season
// RANK reward claim. Client-JS mine: btnGetHandler on the rank-reward view
// sends ActivityMgr.reqGetRankReward({activityId, rankType}) over
// Protocol.S_ACTIVITY_GET_RANK_REWARD=21006 (answer constant is the SAME
// name ⇒ hex NOT derivable by the req−20000 rule — observe it on the tap,
// the 4352 lesson). rankType values come from the activity's 21003
// commonConfig.rankConfig[].type — our ActivityCommonConfig has no
// rankConfig field yet, so this probe WALKS the raw unknown fields of the
// decoded 03eb commonConfig to find the array's field number and the
// entries' type ints. Then fires 21006 per type plus controls and dumps
// every answer. NOTHING is wired yet — this run decides the wire shape.
//
//	TYTD_LIVE=1 TYTD_RBT_RANK=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialRankRewardLive -v -timeout 300s .

import (
	"context"
	"encoding/hex"
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
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRebornTrialRankRewardLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_RANK") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_RANK=1")
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
	db, _ := gorm.Open(sqlite.Open("file:rbtrank?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid)}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true, Notify: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 256), writeChan: make(chan []byte, 256)}

	var tapMu sync.Mutex
	var tap []tapRec
	tapNote := func(mt string, d []byte) {
		tapMu.Lock()
		tap = append(tap, tapRec{mt, append([]byte(nil), d...), time.Now()})
		if len(tap) > 256 {
			tap = tap[len(tap)-256:]
		}
		tapMu.Unlock()
	}
	tapClear := func() {
		tapMu.Lock()
		tap = nil
		tapMu.Unlock()
	}

	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	readerDone := make(chan struct{})
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
			tapNote(msgType, realData)
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.ActList.mergeDetail(p.Username, realData)
				p.rbtMergeConfig(realData)
				p.stDeliver(msgType, realData)
			case SERVER_ACTIVITY_BUY_GOODS, SERVER_ACTIVITY_COND_REWARD,
				SERVER_ACTIVITY_CROSS_RECHARGE, SERVER_ACTIVITY_TOKEN_REWARD,
				SERVER_ACTIVITY_COND_REWARD_ARR:
				p.stDeliver(msgType, realData)
			default:
				p.rbtHandleFrame(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); cancel(); <-readerDone }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(30 * time.Second)
	var actID int32
	for time.Now().Before(deadline) {
		now := time.Now()
		for _, e := range p.ActList.byType(rbtActivityType) {
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
		t.Fatal("no usable type-172 activity on server", server)
	}
	t.Logf("live 172 activityId=%d", actID)

	rt := func(cmd, want string, body []byte) ([]byte, error) {
		return p.stRoundTrip(cmd, want, body, 6*time.Second)
	}
	if body, err := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID}); err == nil {
		if _, err := rt(ACTIVITY_GET_DATA_CMD, SERVER_ACTIVITY_DETAIL, body); err != nil {
			t.Logf("21003 pull err=%v", err)
		}
	}

	// ── walk the raw 03eb for the rankConfig array inside commonConfig ──
	// (our ActivityCommonConfig stops at field 4; whatever the server sent
	// beyond that sits in the unknown-bytes — the field number we see here
	// is the one to add to the proto.)
	var detailBytes []byte
	tapMu.Lock()
	for _, r := range tap {
		if r.msgType == SERVER_ACTIVITY_DETAIL {
			detailBytes = append([]byte(nil), r.data...)
		}
	}
	tapMu.Unlock()
	if len(detailBytes) > 0 {
		var rsp proto.RspGetActivityDetail
		if err := pb.Unmarshal(detailBytes, &rsp); err != nil {
			t.Logf("03eb decode err=%v", err)
		} else if act := rsp.GetActivity(); act != nil {
			unknown := act.ProtoReflect().GetUnknown()
			t.Logf("ActivityCommonData unknown fields: %s", hex.EncodeToString(unknown))
			if dc := act.GetDetailConfig(); dc != nil {
				t.Logf("ActivityDetailConfig unknown: %s", hex.EncodeToString(dc.ProtoReflect().GetUnknown()))
				if cc := dc.GetCommonConfig(); cc != nil {
					unk := cc.ProtoReflect().GetUnknown()
					t.Logf("CommonConfig unknown (%d bytes): %s", len(unk), hex.EncodeToString(unk))
					// Enumerate top-level unknown fields.
					b := unk
					for len(b) > 0 {
						num, wt, n := protowire.ConsumeTag(b)
						if n < 0 {
							break
						}
						b = b[n:]
						var raw []byte
						switch wt {
						case protowire.VarintType:
							v, n := protowire.ConsumeVarint(b)
							if n < 0 {
								b = nil
								break
							}
							t.Logf("  commonConfig unknown field %d varint %d", num, v)
							b = b[n:]
						case protowire.BytesType:
							raw, n = protowire.ConsumeBytes(b)
							if n < 0 {
								b = nil
								break
							}
							t.Logf("  commonConfig unknown field %d bytes(%d): %s", num, len(raw), hex.EncodeToString(raw))
							b = b[n:]
						default:
							b = nil
						}
					}
				}
			}
		}
	} else {
		t.Log("no 03eb bytes captured — cannot walk rankConfig")
	}

	// ── fire 21006 raw and OBSERVE the answer hex on the tap ──
	fire := func(cmd string, body []byte, label string) {
		tapClear()
		p.writeChan <- pack.Pack(cmd, body, uid)
		time.Sleep(3 * time.Second)
		tapMu.Lock()
		defer tapMu.Unlock()
		if len(tap) == 0 {
			t.Logf("%s: SILENT (no answer in 3s)", label)
			return
		}
		for _, r := range tap {
			t.Logf("%s → answer %s : %s", label, r.msgType, hex.EncodeToString(r.data))
		}
	}
	mk := func(rt int32) []byte {
		var b []byte
		b = protowire.AppendTag(b, 1, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(actID))
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(rt))
		return b
	}
	for _, rt := range []int32{0, 1, 2, 3, 4} {
		fire("0000520e", mk(rt), "21006 rankType="+strconv.Itoa(int(rt)))
	}
	// 21017 GET_RANK_STATE — same body, observe whether it answers at all
	// (claim-readiness read could replace blind fire later; for now just
	// record the hex).
	fire("00005211", mk(1), "21017 rankType=1")
}
