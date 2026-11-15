//go:build live

package tytd_go

// One-shot read-only probe: pull 21003 for the active 172 activity and
// raw-walk the wire tree so MallTempMsg field numbers can be fixed from
// the real server data (id/price/reward/buyLimit/param/name).
//
//	TYTD_LIVE=1 TYTD_RBT_MALL=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialMallLive -v -timeout 120s .

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
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

// walkWire prints a generic protobuf wire tree: "f<no>(<wt>)" and strings (truncated).
func walkWire(t *testing.T, b []byte, prefix string, depth int) {
	if depth > 8 {
		return
	}
	i := 0
	for i < len(b) {
		key, n := pbDecodeVarint(b[i:])
		if n == 0 {
			t.Logf("%s<truncated @%d>", prefix, i)
			return
		}
		i += n
		fn, wt := int(key>>3), int(key&7)
		switch wt {
		case 0:
			v, n := pbDecodeVarint(b[i:])
			if n == 0 {
				return
			}
			i += n
			t.Logf("%sf%d=%d", prefix, fn, v)
		case 2:
			l, n := pbDecodeVarint(b[i:])
			if n == 0 {
				return
			}
			i += n
			if i+int(l) > len(b) {
				return
			}
			seg := b[i : i+int(l)]
			i += int(l)
			if s := string(seg); isMostlyPrintable(s) && len(s) < 300 {
				t.Logf("%sf%d=%q", prefix, fn, s)
			} else {
				t.Logf("%sf%d(bytes %d):", prefix, fn, len(seg))
				walkWire(t, seg, prefix+"  ", depth+1)
			}
		case 5:
			i += 4
			t.Logf("%sf%d(fixed32 %d)", prefix, fn, uint32(segLE(b[i-4:i])))
		case 1:
			i += 8
			t.Logf("%sf%d(fixed64)", prefix, fn)
		default:
			t.Logf("%s<bad wt %d>", prefix, wt)
			return
		}
	}
}

func pbDecodeVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7F) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

func segLE(b []byte) uint64 {
	var v uint64
	for i, x := range b {
		v |= uint64(x) << (8 * i)
	}
	return v
}

func isMostlyPrintable(s string) bool {
	n := 0
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			n++
		}
	}
	return len(s) > 0 && n*4 >= len(s)*3
}

func TestRebornTrialMallLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_MALL") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_MALL=1")
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
	db, _ := gorm.Open(sqlite.Open("file:rbtmall?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{})
	_ = db.Create(&model.User{ID: "1", Username: user, GameUID: uint64(uid)}).Error
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 64)}
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
	deadline := time.Now().Add(25 * time.Second)
	var actID int32
	for time.Now().Before(deadline) && actID == 0 {
		actID, _, _ = p.rbtActiveActivity()
		time.Sleep(500 * time.Millisecond)
	}
	if actID == 0 {
		t.Fatal("no active 172")
	}
	req, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: actID})
	p.writeChan <- pack.Pack(ACTIVITY_GET_DATA_CMD, req, uid)
	got := 0
	for time.Now().Before(deadline) && got < 1 {
		select {
		case a := <-all:
			if a[0] != SERVER_ACTIVITY_DETAIL {
				continue
			}
			raw, _ := hex.DecodeString(a[1])
			var rsp proto.RspGetActivityDetail
			if pb.Unmarshal(raw, &rsp) != nil {
				continue
			}
			if rsp.GetActivity().GetActivityId() != actID {
				continue
			}
			got++
			t.Logf("== mall table == mainType=%d actId=%d",
				rsp.GetActivity().GetDetailConfig().GetCommonConfig().GetMainConfig().GetType(),
				rsp.GetActivity().GetActivityId())
			for _, bc := range rsp.GetActivity().GetMallBuyCountList() {
				t.Logf("BOUGHT mallId=%d count=%d", bc.GetMallId(), bc.GetCount())
			}
			for _, cd := range rsp.GetActivity().GetDetailConfig().GetCommonConfig().GetConditionConfig() {
				t.Logf("COND cfg actId=%d condId=%d type=%d param=%v rewards=%q desc=%q",
					cd.GetActivityId(), cd.GetConditionId(), cd.GetType(), cd.GetParam(), cd.GetRewards(), cd.GetDesc())
			}
			for _, cd := range rsp.GetActivity().GetConditionDataList() {
				t.Logf("COND data condId=%d value=%q got=%v", cd.GetConditionId(), cd.GetValue(), cd.GetIsGetReward())
			}
			for _, mc := range rsp.GetActivity().GetDetailConfig().GetCommonConfig().GetMallConfig() {
				mt := mc.GetMallTempMsg()
				ad := "paid"
				if mt.GetPrice() == "0" || strings.HasSuffix(mt.GetPrice(), "=0") {
					ad = "FREE"
				}
				if strings.HasPrefix(mt.GetParam(), "3|") {
					ad += "/AD"
				}
				if strings.HasPrefix(mt.GetPrice(), "100000=") {
					ad += "/XianYu"
				}
				t.Logf("MALL id=%d price=%q reward=%q limitType=%d limit=%d param=%q name=%q [%s]",
					mt.GetId(), mt.GetPrice(), mt.GetReward(), mt.GetBuyLimitType(), mt.GetBuyLimit(), mt.GetParam(), mt.GetName(), ad)
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if got == 0 {
		t.Fatal("no 21003 answer")
	}
	_ = fmt.Sprint(strings.TrimSpace(""))
}
