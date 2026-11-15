//go:build live

package tytd_go

// Live capability probe (user, 2026-10-04: "we can free merge, run many round
// merge in one drop, so we will never die"): does the server accept an
// upload where EXISTING echoed balls are MOVED into contact and merged —
// the claimed pair being unrelated to the fresh drop? Drop positions were
// already proven free (floating towers); this tests relocation of echoed
// balls + a merge claim the drop didn't cause.
//
// Phase 1: plain-drop until one config appears twice on the board (spread
//         floor placements, never touching).
// Phase 2: ONE upload: fresh drop D at the right end; the same-config pair
//         moved to touch at the left wall; merge claimed between them
//         (ids pair1+pair2, merged = syncId+1); every other ball repacked
//         to a collision-free floor lane. ret=0 ⇒ FREE MERGE CONFIRMED.
// NEVER settles, NO items.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveComposeBallFreeMerge -v -timeout 600s .

import (
	"context"
	"os"
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

func TestLiveComposeBallFreeMerge(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1")
	}
	var (
		fmUser   = liveUserEnv(t)
		fmPass   = livePassEnv(t)
		fmServer = 710564
	)
	passportToken, err := request.LoginGPassport(fmUser, fmPass)
	if err != nil {
		t.Fatal(err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatal(err)
	}
	gameURL, err := request.GetServerURL(userID, fmServer)
	if err != nil {
		t.Fatal(err)
	}
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
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
	db, _ := gorm.Open(sqlite.Open("file:cb_fm_live?mode=memory&cache=shared"), &gorm.Config{})
	if err := db.AutoMigrate(&model.User{}, &model.SystemConfig{}); err != nil {
		t.Fatal(err)
	}
	p := &Player{
		Username: fmUser, UID: uid, ChatID: int64(uid), Running: true,
		DB: db, conn: conn, ctx: ctx, cancel: cancel,
		NotiChan:  make(chan NotifyMGS, 64),
		writeChan: make(chan []byte, 64),
	}
	go func() {
		for frame := range p.writeChan {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
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
			switch ty {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, d, ty == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.cbMergeConfig(d)
			case SERVER_COMPOSE_BALL_ENTER, SERVER_COMPOSE_BALL_COMPOSE,
				SERVER_COMPOSE_BALL_USE_ITEM, SERVER_COMPOSE_BALL_HP_ITEM,
				SERVER_COMPOSE_BALL_SETTLE:
				p.cbDeliver(ty, d)
			}
		}
	}()
	defer func() { conn.Close(); <-rdone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(20 * time.Second)
	actID := int32(0)
	for actID == 0 && time.Now().Before(deadline) {
		actID = p.ComposeBallActivityID()
		if actID == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if actID == 0 {
		t.Skip("window closed")
	}
	p.cbRequestConfig(actID)
	time.Sleep(1500 * time.Millisecond)

	e, err := p.RequestComposeBallEnterGame(15 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if e.GetRet() != 0 {
		t.Fatalf("ENTER ret=%d", e.GetRet())
	}
	p.cbApplySync(e.GetSyncData())

	// ---- phase 1: spread plain drops until one config exists twice ----
	spreadX := []float64{44, 676, 160, 560, 280, 440, 360, 220}
	for n := 0; n < len(spreadX); n++ {
		p.CbMu.Lock()
		hp := p.cb.HP
		if hp < 1 {
			p.CbMu.Unlock()
			t.Fatalf("hp=0 during phase1 (rule: leave it, wait for regen) — score=%d", p.cb.Score)
		}
		cfg := p.cb.NextConfig
		r := cbRadius(p.cb, cbBall{Config: cfg})
		balls := make([]*proto.ComposeBallBallData, 0, len(p.cb.Balls)+1)
		for _, b := range p.cb.Balls {
			balls = append(balls, cbBallData(b))
		}
		drop := cbBall{ID: p.cb.NextID, Config: cfg, X: spreadX[n], Y: cbGroundFallback + r}
		balls = append(balls, cbBallData(drop))
		byCfg := map[int32]int32{}
		for _, b := range p.cb.Balls {
			byCfg[b.Config]++
		}
		p.CbMu.Unlock()
		if byCfg[cfg] >= 1 { // this drop makes it a pair
			resp, err := p.RequestComposeBallCompose(balls, nil, 15*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetRet() != 0 {
				t.Fatalf("phase1 plain drop ret=%d", resp.GetRet())
			}
			p.cbApplySync(resp.GetSyncData())
			t.Logf("phase1: PAIR formed cfg=%d after drop #%d", cfg, n+1)
			break
		}
		resp, err := p.RequestComposeBallCompose(balls, nil, 15*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if resp.GetRet() != 0 {
			t.Fatalf("phase1 plain drop #%d ret=%d", n+1, resp.GetRet())
		}
		p.cbApplySync(resp.GetSyncData())
	}

	// locate the duplicate pair + others
	p.CbMu.Lock()
	var pair [2]cbBall
	var others []cbBall
	seen := map[int32]cbBall{}
	found := false
	for _, b := range p.cb.Balls {
		o, dup := seen[b.Config]
		if dup && !found {
			pair = [2]cbBall{o, b}
			found = true
			continue
		}
		if dup {
			others = append(others, b)
			continue
		}
		seen[b.Config] = b
	}
	if !found {
		p.CbMu.Unlock()
		t.Fatal("no same-config pair after 8 drops (very unlikely)")
	}
	cfgP := pair[0].Config
	upCfg := p.cb.ConfigOf[p.cb.LevelOf[cfgP]+1]
	if upCfg == 0 {
		p.CbMu.Unlock()
		t.Fatal("the duplicate pair sits at the top tier — rerun (rare)")
	}
	ra, ur := cbRadius(p.cb, pair[0]), cbRadius(p.cb, cbBall{Config: upCfg})
	ground := cbGroundFallback
	scoreBefore := p.cb.Score
	nextID := p.cb.NextID
	dropCfg := p.cb.NextConfig
	p.CbMu.Unlock()

	// ---- phase 2: relocate the pair into contact at the left wall, claim
	// the merge, keep everything else collision-free, ride the fresh drop ----
	a1 := cbBall{ID: pair[0].ID, Config: cfgP, X: ra + cbWallPad + 4, Y: ground + ra}
	a2 := cbBall{ID: pair[1].ID, Config: cfgP, X: a1.X + 2*ra, Y: ground + ra}
	merged := cbBall{ID: nextID + 1, Config: upCfg, X: a1.X, Y: ground + ur}
	drop := cbBall{ID: nextID, Config: dropCfg, X: 690 - cbRadius(p.cb, cbBall{Config: dropCfg}), Y: ground + cbRadius(p.cb, cbBall{Config: dropCfg})}
	// repack the rest right of merged, left of drop, floor row, gap 12
	x := merged.X + ur + 60
	packed := make([]cbBall, 0, len(others))
	for _, o := range others {
		ro := cbRadius(p.cb, o)
		x += ro
		o.X, o.Y = x, ground+ro
		x += ro + 12
		packed = append(packed, o)
	}
	dropR := cbRadius(p.cb, cbBall{Config: dropCfg})
	if x+dropR+12 > drop.X-dropR {
		t.Fatalf("too many stray balls to repack without touching the drop zone (packEnd=%.0f dropX=%.0f)", x, drop.X)
	}
	t.Logf("FREEMERGE claim: pair %d(%d@%.0f)+%d(%d@%.0f) MOVED to contact (%.1f/%.1f), drop %d cfg%d at %.1f, merged %d cfg%d, others=%v, othersOriginal=%v",
		pair[0].ID, pair[0].Config, pair[0].X, pair[1].ID, pair[1].Config, pair[1].X, a1.X, a2.X,
		drop.ID, drop.Config, drop.X, merged.ID, merged.Config, packed, others)

	world := []cbBall{a1, a2, drop}
	world = append(world, packed...)
	proc := cbSnapshot(world) // live balls at the contact instant (pair touching)
	cpos := a1
	if a2.Y < a1.Y || (a2.Y == a1.Y && a2.ID < a1.ID) {
		cpos = a2
	}
	composes := []*proto.ComposeBallComposeData{{
		BallA: pb.Int32(a1.ID), BallB: pb.Int32(a2.ID),
		Ball:        cbBallData(cbBall{ID: merged.ID, Config: merged.Config, X: cpos.X, Y: cpos.Y}),
		ProcessBall: proc,
	}}
	final := []cbBall{merged}
	final = append(final, drop)
	final = append(final, packed...)
	fballs := make([]*proto.ComposeBallBallData, 0, len(final))
	for _, b := range final {
		fballs = append(fballs, cbBallData(b))
	}
	resp, err := p.RequestComposeBallCompose(fballs, composes, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetRet() != 0 {
		t.Fatalf("VERDICT: FREE MERGE REFUSED ret=%d (relocating echoed balls / unrelated merge claim NOT allowed)", resp.GetRet())
	}
	p.cbApplySync(resp.GetSyncData())
	t.Logf("VERDICT: FREE MERGE CONFIRMED — ret=0, score %d→%d (want +%d), echoed balls=%v",
		scoreBefore, p.cb.Score, p.cb.ScoreOf[p.cb.LevelOf[upCfg]], p.cb.Balls)
}
