//go:build live

package tytd_go

// IDENTIFICATION probe (owner feature request 2026-10-17: auto-play "Tiên
// Cảnh Vị Ương" like Luân Hồi Bí Cảnh). The static reference bundle
// (remoteImmortalIsland) may or may not be THIS activity — the server itself
// is the ground truth. This probe logs in, captures the login activity-list
// push, and for EVERY active activity pulls its 21003 detail, printing the
// activity type/id plus every shop row whose NAME mentions "Tiên Cảnh Vị
// Ương" (and any other suspicious "Vị Ương"/"Điểm Vị Ương" rows). The winner
// gives: the activity TYPE number (the port's rbtActivityType equivalent),
// the live activityId, and the shop price shapes ("0" / "100000=N" tiên ngọc
// / numeric nạp) needed for the free+ad+jade auto-buy gate — all READ-ONLY,
// nothing is bought or spent.
//
//	TYTD_LIVE=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710564 \
//	  go test -tags "sqlcipher live" -count=1 -run TestTienCanhIdentLive -v -timeout 300s .

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
)

func TestTienCanhIdentLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1")
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

	// ---- login activity list
	var push proto.PushActivityList
	gotList := false
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case a := <-all:
			if a[0] == SERVER_ACTIVITY_LIST {
				raw, _ := hex.DecodeString(a[1])
				if pb.Unmarshal(raw, &push) == nil && len(push.GetMainConfig()) > 0 {
					gotList = true
				}
			}
		case <-time.After(200 * time.Millisecond):
		}
		if gotList {
			break
		}
	}
	if !gotList {
		t.Fatal("no activity list push")
	}
	now := time.Now()
	type act struct {
		typ, id int32
	}
	var active []act
	t.Logf("activity list: %d rows", len(push.GetMainConfig()))
	for _, c := range push.GetMainConfig() {
		e := entryFromConfig(c)
		st := e.status(now)
		if st == "active" {
			active = append(active, act{c.GetType(), c.GetActivityId()})
		}
		t.Logf("  act type=%d id=%d status=%s begin=%s end=%s",
			c.GetType(), c.GetActivityId(), st,
			time.UnixMilli(c.GetBeginTime()).In(ictZone).Format("01-02 15:04"),
			time.UnixMilli(c.GetEndTime()).In(ictZone).Format("01-02 15:04"))
	}

	// ---- 21003 detail per active activity; flag any row naming Vị Ương
	hits := 0
	for _, a := range active {
		body, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: a.id})
		write <- pack.Pack(ACTIVITY_GET_DATA_CMD, body, uid)
		end := time.Now().Add(6 * time.Second)
		for time.Now().Before(end) {
			select {
			case x := <-all:
				if x[0] != SERVER_ACTIVITY_DETAIL {
					continue
				}
				raw, _ := hex.DecodeString(x[1])
				var rsp proto.RspGetActivityDetail
				if pb.Unmarshal(raw, &rsp) != nil {
					continue
				}
				dc := rsp.GetActivity().GetDetailConfig()
				cc := dc.GetCommonConfig()
				if cc.GetMainConfig().GetType() != a.typ {
					continue // stale answer for an earlier request
				}
				var params []string
				for _, pc := range cc.GetParamConfig() {
					params = append(params, pc.GetParam()+"="+pc.GetValue())
				}
				var mallNames []string
				flagged := false
				for _, mc := range cc.GetMallConfig() {
					mt := mc.GetMallTempMsg()
					if mt == nil {
						continue
					}
					line := fmt.Sprintf("      gói id=%d %q price=%q param=%q buyLimit=%d",
						mt.GetId(), mt.GetName(), mt.GetPrice(), mt.GetParam(), mt.GetBuyLimit())
					mallNames = append(mallNames, line)
					if strings.Contains(mt.GetName(), "Vị Ương") || strings.Contains(mt.GetName(), "Tiên Cảnh") {
						flagged = true
					}
				}
				if flagged || strings.Contains(strings.Join(params, " "), "Vị Ương") {
					hits++
					t.Logf("*** VỊ ƯƠNG HIT: type=%d id=%d params=%v", a.typ, a.id, params)
					for _, l := range mallNames {
						t.Log(l)
					}
				} else if len(mallNames) > 0 {
					t.Logf("act type=%d id=%d shop (%d rows):", a.typ, a.id, len(mallNames))
					for _, l := range mallNames {
						t.Log(l)
					}
				} else {
					t.Logf("act type=%d id=%d no shop rows; params=%v", a.typ, a.id, params)
				}
				end = time.Now() // answer in hand, stop this activity's window
			case <-time.After(150 * time.Millisecond):
			}
		}
	}
	if hits == 0 {
		t.Logf("no shop/param row named 'Vị Ương'/'Tiên Cảnh' among %d active activities", len(active))
	} else {
		t.Logf("TOTAL Vị Ương activity hits: %d", hits)
	}
}
