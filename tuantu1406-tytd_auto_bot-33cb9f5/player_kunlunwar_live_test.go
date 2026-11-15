//go:build live

package tytd_go

// Live read-only probe for a real Côn Lôn window (activity type 199):
// EnterActivity → config poll (phase windows) → whole-map MiniMap (rendered
// to /tmp/kunlun_live.png) → group roster → EnterGame/Leave (harmless scene
// touch). Nothing here marches, explores or attacks.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestKunlunWarLive -v -timeout 180s .

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// kuRSS logs Go heap + kernel RSS at a labelled step — the "still oom on map
// access" investigation (user's 512 MB VPS). Run under a simulated box with
// GOMEMLIMIT=280MiB to watch GC behaviour, not just allocations.
func kuRSS(t *testing.T, tag string) {
	t.Helper()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	rss, hwm := "?", "?"
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				rss = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(line, "VmHWM:"); ok {
				hwm = strings.TrimSpace(v)
			}
		}
	}
	t.Logf("MEM[%s] heap=%.1fMB total-alloc=%.1fMB sys=%.1fMB gc=%d VmRSS=%s VmHWM=%s",
		tag, float64(ms.HeapAlloc)/1048576, float64(ms.TotalAlloc)/1048576,
		float64(ms.Sys)/1048576, ms.NumGC, rss, hwm)
}

func TestKunlunWarLive(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, _ := gorm.Open(sqlite.Open("file:kunlun_live?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&model.User{}, &model.SystemConfig{})
	p := &Player{Username: user, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel, Notify: true,
		NotiChan: make(chan NotifyMGS, 64), writeChan: make(chan []byte, 64)}
	go func() {
		for f := range p.writeChan {
			if err := conn.WriteMessage(2, f); err != nil {
				return
			}
		}
	}()
	raw := make(chan string, 512)
	rdone := make(chan struct{})
	go func() {
		defer close(rdone)
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
			case raw <- ty:
			default:
			}
			switch ty {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.kuMergeConfig(d)
			case SERVER_KUNLUN_ENTER_ACTIVITY, SERVER_KUNLUN_ENTER_GAME, SERVER_KUNLUN_LEAVE,
				SERVER_KUNLUN_GROUP_INFO, SERVER_KUNLUN_MINI_MAP_INFO, SERVER_KUNLUN_HEART_BEAT,
				SERVER_KUNLUN_SPAR_INFO, SERVER_KUNLUN_SPAR_POWER, SERVER_KUNLUN_USE_ITEM,
				SERVER_KUNLUN_GET_WELFARE,
				SERVER_KUNLUN_BLOCK_SYNC, SERVER_KUNLUN_CITY_SYNC, SERVER_KUNLUN_MY_GAME_SYNC,
				SERVER_KUNLUN_BEEN_KILL_SYNC, SERVER_KUNLUN_LEFT_OUT_SYNC, SERVER_KUNLUN_CITY_BATTLE_SYNC,
				SERVER_KUNLUN_ROUTE_SYNC, SERVER_KUNLUN_LINE_SYNC, SERVER_KUNLUN_ATTACK_SYNC,
				SERVER_KUNLUN_GROUP_RANK_SYNC, SERVER_KUNLUN_GAME_BLOCK_INFO:
				p.kuDeliver(ty, d)
			}
		}
	}()
	defer func() { conn.Close(); <-rdone }()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	actID := int32(0)
	for end := time.Now().Add(25 * time.Second); time.Now().Before(end) && actID == 0; {
		actID = p.KunlunWarActivityID()
		if actID == 0 {
			time.Sleep(400 * time.Millisecond)
		}
	}
	if actID == 0 {
		t.Skip("no KunlunWar (type 199) window in the activity list")
	}
	t.Logf("ACTLIST activityId=%d", actID)
	kuRSS(t, "baseline after login")

	// Phase windows live in the 21003 uniqueConfig.
	payload, err := activityPollFrame(actID)
	if err == nil {
		p.writeChan <- pack.Pack(ACTIVITY_GET_DATA_CMD, payload, uid)
	}
	time.Sleep(1500 * time.Millisecond)

	ent, err := p.RequestKunlunEnterActivity(15 * time.Second)
	if err != nil {
		t.Fatalf("EnterActivity: %v", err)
	}
	t.Logf("ENTER_ACTIVITY ret=%d round=%d floor=%d group=%d rank=%d union=%d",
		ent.GetRet(), ent.GetRound(), ent.GetFloor(), ent.GetGroup(), ent.GetSelfRank(), ent.GetUnionId())
	st := p.kuState()
	p.KuMu.Lock()
	phase := st.kuPhase(time.Now())
	cfg := st.Cfg != nil
	p.KuMu.Unlock()
	t.Logf("CONFIG merged=%v phase=%s", cfg, phase)

	mini, err := p.RequestKunlunMiniMap(15 * time.Second)
	if err != nil {
		t.Fatalf("MiniMap: %v", err)
	}
	rsp, err := gimap.DecodeKunlunMini(mini)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MINIMAP cities=%d cells=%d unions=%d", len(rsp.GetCityInfo()), len(rsp.GetCellInfo()), len(rsp.GetBaseData()))
	for _, u := range rsp.GetBaseData() {
		t.Logf("UNION %d %q flag=%d", u.GetUnionId(), u.GetName(), u.GetFlag())
	}
	owned := 0
	for _, c := range rsp.GetCellInfo() {
		if c.GetUnionId() != 0 {
			owned++
		}
	}
	t.Logf("MINIMAP occupied-cells=%d", owned)
	_ = os.WriteFile("/tmp/kunlun_live_mini.bin", mini, 0o644)
	kuRSS(t, "after minimap payload="+strconv.Itoa(len(mini))+"B decoded")
	// Mini-app steady state: mapStreamLoop ticks gimap.Stream every 5 s.
	// 40 ticks = a 3.3-minute watch; heap must NOT ratchet (leak check).
	for i := 0; i < 40; i++ {
		if _, e := gimap.Stream(gimap.StreamKunlun, mini); e != nil {
			t.Fatalf("Stream tick %d: %v", i, e)
		}
	}
	runtime.GC()
	kuRSS(t, "after 40 stream ticks (GC)")
	_ = gimap.KunlunSceneJSON() // must NOT decode sheet pixels
	kuRSS(t, "after scene.json")
	if img, err := gimap.RenderKunlun(mini, 1400); err == nil {
		_ = os.WriteFile("/tmp/kunlun_live.png", img, 0o644)
		t.Logf("PNG written /tmp/kunlun_live.png (%d bytes)", len(img))
	} else {
		t.Errorf("render: %v", err)
	}
	kuRSS(t, "after RenderKunlun (sheet pixels decoded here)")
	runtime.GC()
	kuRSS(t, "post-render GC (steady floor)")

	if g, e := p.RequestKunlunGroupInfo(ent.GetGroup(), ent.GetFloor(), 15*time.Second); e != nil {
		t.Logf("GroupInfo: %v", e)
	} else {
		for _, gi := range g.GetGroupInfo() {
			t.Logf("GROUP union=%d name=%q rank=%d cityScore=%d master=%q friend=%q", gi.GetUnionId(), gi.GetUnionName(), gi.GetRank(), gi.GetCityScore(), gi.GetMasterName(), gi.GetFriendUnionName())
		}
	}

	game, err := p.RequestKunlunEnterGame(15 * time.Second)
	if err != nil {
		t.Logf("EnterGame: %v", err)
	} else {
		t.Logf("ENTER_GAME ret=%d home=%d energy=%d pos=%d leftout=%v",
			game.GetRet(), game.GetHome(), game.GetEnergy(), game.GetMyPlayerInfo().GetPos(), game.GetLeftout())
		// 219903 probe while seated: does the server EVER populate displayInfo
		// (who stands where) and city HP? If it answers empty, the "no people"
		// report is a server truth, not a bot bug — and the page chip proves it.
		if pos := game.GetMyPlayerInfo().GetPos(); pos > 0 {
			const cols = int32(61)
			tx, ty := pos%cols, pos/cols
			var blocks []int32
			for dy := int32(-3); dy <= 3; dy++ {
				for dx := int32(-3); dx <= 3; dx++ {
					x, y := tx+dx, ty+dy
					if x < 0 || y < 0 || x >= cols || y >= cols {
						continue
					}
					blocks = append(blocks, y*cols+x)
				}
			}
			if bi, e := p.RequestKunlunGameBlockInfo(blocks, 20*time.Second); e != nil {
				t.Logf("BLOCK_INFO: %v", e)
			} else {
				who, nCity := 0, len(bi.GetCityInfo())
				for _, b := range bi.GetBlockInfo() {
					who += len(b.GetDisplayInfo())
				}
				t.Logf("BLOCK_INFO req=%d blocks=%d displayInfo=%d cityInfo=%d",
					len(blocks), len(bi.GetBlockInfo()), who, nCity)
				for _, b := range bi.GetBlockInfo() {
					for _, d := range b.GetDisplayInfo() {
						t.Logf("STAND pos=%d %q union=%d power=%s", b.GetPos(), d.GetNickName(), d.GetUnionId(), d.GetPower())
					}
					for _, ub := range b.GetUnionBlockData() {
						t.Logf("PROG pos=%d union=%d occupy=%d", b.GetPos(), ub.GetUnionId(), ub.GetOccupyProgress())
					}
				}
				for _, c := range bi.GetCityInfo() {
					t.Logf("CITY %d hp=%d cur=%d dead=%v fight=%v union=%d",
						c.GetCityId(), c.GetHp(), c.GetCurHp(), c.GetDestroyed(), c.GetHaveFight(), c.GetUnionId())
				}
			}
		}
		// Fruit tree (219910 canReceive → 219911 claim → 219926 eat). The
		// user's own packet capture (2026-10-06) proved "cắn quả" is the
		// ITEM USE of the claimed fruit — one of the two authorized kunlun
		// item ops. itemId only ever comes from real server data: this
		// account's 19911 reward string, or the observed 105248.
		fruitID := int32(105248) // seen live in this activity's 19911 reward AND the user capture
		actID := p.KunlunWarActivityID()
		// Full 219910 (what kuTreeTick reads): exp, per-notice status, canReceive.
		sparBody, _ := pb.Marshal(&proto.KunlunWarSparInfoReqMsg{ActivityId: &actID})
		cr := int32(-1)
		if raw, e := p.kuRoundTrip(KUNLUN_SPAR_INFO_CMD, SERVER_KUNLUN_SPAR_INFO, sparBody, 15*time.Second); e != nil {
			t.Logf("SPAR_INFO: %v", e)
		} else {
			var si proto.KunlunWarSparInfoRespMsg
			if e := pb.Unmarshal(raw, &si); e != nil {
				t.Fatalf("SPAR_INFO decode: %v", e)
			}
			cr = si.GetCanReceive()
			pending := 0
			for _, w := range si.GetInfo() {
				if w.GetStatus() == 0 {
					pending++
				}
				t.Logf("SPAR_INFO notice name=%q item=%d id=%d status=%d time=%d",
					w.GetName(), w.GetItemId(), w.GetId(), w.GetStatus(), w.GetTime())
			}
			t.Logf("SPAR_INFO exp=%d canReceive=%d pending-notices=%d", si.GetExp(), cr, pending)
			// 219912 watering (kuTreeTick's auto-collect): id=0 = ALL, receive-side
			// is free — the ally who SENT the liquid paid, never mirrored here.
			if pending > 0 {
				zero := int64(0)
				wBody, _ := pb.Marshal(&proto.KunlunWarGetWelfareReqMsg{ActivityId: &actID, Id: &zero})
				if raw, e := p.kuRoundTrip(KUNLUN_GET_WELFARE_CMD, SERVER_KUNLUN_GET_WELFARE, wBody, 15*time.Second); e != nil {
					t.Errorf("GET_WELFARE: %v (wire-tag guess unproven!)", e)
				} else {
					var gw proto.KunlunWarGetWelfareRespMsg
					if e := pb.Unmarshal(raw, &gw); e != nil {
						t.Fatalf("GET_WELFARE decode: %v", e)
					}
					t.Logf("GET_WELFARE ret=%d reward=%q exp=%d canReceive=%d",
						gw.GetRet(), gw.GetReward(), gw.GetExp(), gw.GetCanReceive())
				}
			}
		}
		if cr == 1 {
			if sp, e := p.RequestKunlunSparPower(15 * time.Second); e != nil {
				t.Logf("SPAR_POWER: %v", e)
			} else {
				rew := ""
				if sp.Reward != nil {
					rew = *sp.Reward
				}
				t.Logf("SPAR_POWER ret=%d reward=%q exp=%d canReceive=%d",
					sp.GetRet(), rew, sp.GetExp(), sp.GetCanReceive())
				if id := kuRewardItemID(rew); id != 0 {
					fruitID = id
				}
			}
		}
		if ui, e := p.RequestKunlunUseItem(fruitID, 1, 15*time.Second); e != nil {
			t.Logf("USE_ITEM: %v", e)
		} else {
			t.Logf("USE_ITEM item=%d ret=%d energy=%d recoverAt=%d",
				fruitID, ui.GetRet(), ui.GetEnergy(), ui.GetEnergyRecoverTime())
		}
		if err := p.RequestKunlunLeave(10 * time.Second); err != nil {
			t.Logf("Leave: %v", err)
		}
	}
	t.Logf("STATUS %v", p.KunlunStatus(time.Now()))
}
