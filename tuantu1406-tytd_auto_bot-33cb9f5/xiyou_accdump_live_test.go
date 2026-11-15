//go:build live

package tytd_go

// Quick full player-data dump for the stuck XiYou account: what is actually
// equipped (skills/passives/equipment/fabao) — the server-side boss sim
// verdict depends on this, not on the displayed power number.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 go test -tags "sqlcipher live" -count=1 -run TestXiYouAccDumpLive -v -timeout 180s .

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

func TestXiYouAccDumpLive(t *testing.T) {
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
		Username: user, Name: user, UID: uid, Notify: false,
		NotiChan: make(chan NotifyMGS, 256), writeChan: make(chan []byte, 256), ctx: ctx,
	}
	go func() {
		for f := range p.writeChan {
			_ = conn.WriteMessage(2, f)
		}
	}()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.write(pack.Pack(AUTH_CMD, authMSG, uid))
	var mu sync.Mutex
	var dataReplies [][]byte
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
			if ty == SERVER_XIYOU_DATA {
				dataReplies = append(dataReplies, append([]byte{}, d...))
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
	deadline := time.Now().Add(40 * time.Second)
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
		now := time.Now().UnixMilli()
		all := p.ActList.snapshot()
		t.Logf("actList total=%d at now=%d", len(all), now)
		for _, e := range all {
			t.Logf("  ACT type=%d id=%d begin=%d end=%d open=%v", e.Type, e.ActivityID, e.BeginTime, e.EndTime, e.BeginTime <= now && now < e.EndTime)
		}
		t.Fatal("type 311 not open")
	}
	mu.Lock()
	before := len(dataReplies)
	mu.Unlock()
	body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
	p.write(pack.Pack(XIYOU_DATA_INFO_CMD, body, uid))
	lim := time.Now().Add(10 * time.Second)
	var raw []byte
	for time.Now().Before(lim) && raw == nil {
		mu.Lock()
		if len(dataReplies) > before {
			raw = append([]byte{}, dataReplies[before]...)
		}
		mu.Unlock()
		if raw == nil {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if raw == nil {
		t.Fatal("no data reply")
	}
	var r proto.XiYouPlayerDataInfoResp
	if pb.Unmarshal(raw, &r) != nil || r.GetInfo() == nil {
		t.Fatal("unmarshal")
	}
	i := r.GetInfo()
	t.Logf("progress=%d realm=%d power=%s exp=%s mainCond=%d hero=%d", i.GetProgress(), i.GetRealmId(), i.GetPower(), i.GetExp(), i.GetMainConditonId(), i.GetHeroAppearanceId())
	t.Logf("useSkills=%v passiveSkillIds=%v", i.GetUseSkills(), i.GetPassiveSkillIds())
	t.Logf("equipment=%d unDeal=%d", len(i.GetEqupiments()), len(i.GetUnDealEquipments()))
	for _, e := range i.GetEqupiments() {
		t.Logf("  eq id=%d equipId=%d lv=%d quality=%d", e.GetId(), e.GetEquipmentId(), e.GetLevel(), e.GetQuality())
	}
	t.Logf("attrs:")
	for _, a := range i.GetAttributeList() {
		t.Logf("  attr %d=%s", a.GetType(), a.GetValue())
	}
	for _, it := range i.GetItems() {
		if it.GetItemCount() != 0 {
			t.Logf("  item %d x%d", it.GetItemId(), it.GetItemCount())
		}
	}
}
