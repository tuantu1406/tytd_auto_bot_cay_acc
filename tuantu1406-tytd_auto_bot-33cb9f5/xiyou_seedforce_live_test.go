//go:build live

package tytd_go

// TEMP probe (owner 2026-11-05): can the CLIENT force the seed the server
// replays boss fights with? Sends UPDATE_SEED (231137) with the hidden
// seed=2 field (not in the client stub — client sends {}) and checks whether
// resp.seed ECHOES our value.
//
//	TYTD_LIVE=1 TYTD_XIYOU=1 go test -tags "sqlcipher live" -count=1 -run TestXiyouSeedForceLive -v -timeout 120s .

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
)

func TestXiyouSeedForceLive(t *testing.T) {
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

	seedCh := make(chan *proto.XiYouUpdateSeedResp, 4)
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
			if ty == SERVER_XIYOU_UPDATE_SEED {
				var r proto.XiYouUpdateSeedResp
				if pb.Unmarshal(d, &r) == nil {
					select {
					case seedCh <- &r:
					default:
					}
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

	const want = int32(610725) // the offline-proven winning seed @190407
	variants := []struct {
		name string
		req  *proto.XiYouUpdateSeedReq
	}{
		{"baseline {}", &proto.XiYouUpdateSeedReq{}},
		{"seed-only", &proto.XiYouUpdateSeedReq{Seed: want}},
		{"act+seed", &proto.XiYouUpdateSeedReq{ActivityId: 9063637, Seed: want}},
		{"seed-only again", &proto.XiYouUpdateSeedReq{Seed: want + 1}},
	}
	for _, v := range variants {
		got := ""
		for try := 0; try < 4; try++ {
			b, _ := pb.Marshal(v.req)
			p.write(pack.Pack(XIYOU_UPDATE_SEED_CMD, b, p.UID))
			select {
			case r := <-seedCh:
				if r.GetRet() == 0 {
					got = "ret=0 seed=" + strconv.Itoa(int(r.GetSeed()))
					if r.GetSeed() == v.req.GetSeed() && v.req.GetSeed() != 0 {
						got += " ← ECHO (client-forced!)"
					}
					if r.GetSeed() == 0 && v.req.GetSeed() != 0 {
						got += " ← seed=0?!"
					}
				} else {
					got = "ret=" + strconv.Itoa(int(r.GetRet())) + " (rate?)"
				}
			case <-time.After(3 * time.Second):
				got = "TIMEOUT"
			}
			if got != "" && got != "ret=30 (rate?)" {
				break
			}
			time.Sleep(1500 * time.Millisecond)
		}
		t.Logf("VARIANT %-16s req.seed=%-7d → %s", v.name, v.req.GetSeed(), got)
	}
}
