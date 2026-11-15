//go:build live

package tytd_go

// Live spawn-model calibration for Luyện Khí Đại Hội (remoteCastSword, type
// 119). The offline benchmarks assume the server spawns exactly ONE tier-1
// into a uniformly random empty cell after every slide — that assumption was
// NEVER verified (only 4 samples from the first probe). This test plays with
// the production policy and, per move, compares the locally-computed pre-spawn
// slide against the server's answered board; the difference is the server's
// actual spawn. It prints a per-move line plus an observed-vs-uniform summary
// so the mock can be recalibrated honestly.
//
//	TYTD_LIVE=1 TYTD_CS_CAL=1 go test -count=1 -tags "sqlcipher live" -run TestCastSwordLiveCalibrate -v -timeout 420s .

import (
	"context"
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

func csBoardStr(b csBoard) string {
	s := make([]byte, 16)
	for i, v := range b {
		if v > 9 {
			s[i] = '#'
		} else {
			s[i] = byte('0' + v)
		}
	}
	return string(s)
}

func TestCastSwordLiveCalibrate(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_CS_CAL") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_CS_CAL=1")
	}
	maxMoves := 240
	if v := os.Getenv("TYTD_CS_CAL_N"); v != "" {
		maxMoves, _ = strconv.Atoi(v)
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
	db, _ := gorm.Open(sqlite.Open("file:castsword_cal?mode=memory&cache=shared"), &gorm.Config{})
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
			case SERVER_CASTSWORD_ENTER, SERVER_CASTSWORD_MOVE,
				SERVER_CASTSWORD_SETTLE, SERVER_CASTSWORD_GETREWARD:
				p.csDeliver(ty, d)
			}
		}
	}()
	defer func() { conn.Close(); <-done }()
	go p.wsKeepAlive()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := p.csActiveID(); err == nil {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if _, err := p.csActiveID(); err != nil {
		t.Skip("Luyện Khí (type 119) not open for this account")
	}
	rsp, err := p.RequestCastSwordEnter()
	if err != nil {
		t.Fatalf("ENTER: %v", err)
	}
	startStr := rsp.GetEntity().GetStrength()
	t.Logf("ENTER: score=%d strength=%d cells=%s", rsp.GetEntity().GetScore(),
		startStr, csBoardStr(func() csBoard { b, _ := p.csBoard(); return b }()))

	obs := [16]int{}     // spawns actually observed in each cell
	exp := [16]float64{} // expected count under uniform-over-empties
	tierHist := map[int32]int{}
	movesWithSpawn, movesNoSpawn, movesMulti := 0, 0, 0
	strDeltaSum := int32(0)
	prevStr := startStr
	rng := csDefaultRng // same chooser as production policy
	n := 0
	for ; n < maxMoves; n++ {
		b, st := p.csBoard()
		if st.GameEnd || st.Strength <= 0 {
			t.Logf("STOP at %d moves: gameEnd=%v strength=%d", n, st.GameEnd, st.Strength)
			break
		}
		dir, _, ok := csChoose(b, rng)
		if !ok {
			t.Logf("STOP at %d moves: bí thế (board dead per our model)", n)
			break
		}
		slid, _, _ := csSlide(b, int(dir))
		if _, err := p.RequestCastSwordMove(dir); err != nil {
			t.Logf("STOP at %d moves: move err %v", n, err)
			break
		}
		post, st2 := p.csBoard()
		// spawn diff: cells where post has a piece slid does not
		var diff [16]int
		k := 0
		for i := range post {
			if post[i] != slid[i] {
				diff[k%16] = i
				k++
			}
		}
		spawns := diff[:k]
		// Live truth (107 spawns): one piece per move, tier 1/2/3 ≈ 888/103/9
		// permille. Anything else (multi-diff, spawn-on-occupied) is anomalous.
		switch {
		case k == 0:
			movesNoSpawn++
		case k == 1 && slid[spawns[0]] == 0 && post[spawns[0]] >= 1 && post[spawns[0]] <= 3:
			movesWithSpawn++
			obs[spawns[0]]++
			tierHist[post[spawns[0]]]++
		default:
			movesMulti++
			// anomalous: log raw for manual review
			t.Logf("ANOM m=%d dir=%d slid=%s post=%s diff=%v", n, dir, csBoardStr(slid), csBoardStr(post), spawns)
		}
		empties := 0
		for _, v := range slid {
			if v == 0 {
				empties++
			}
		}
		if empties > 0 {
			for i, v := range slid {
				if v == 0 {
					exp[i] += 1.0 / float64(empties)
				}
			}
		}
		strDeltaSum += prevStr - st2.Strength
		prevStr = st2.Strength
		t.Logf("CAL m=%d dir=%d spawn=%v empties=%d str=%d slid=%s post=%s",
			n, dir, spawns, empties, st2.Strength, csBoardStr(slid), csBoardStr(post))
		time.Sleep(750 * time.Millisecond) // stay inside the server's move lock
	}
	t.Logf("SUMMARY moves=%d spawn1=%d spawn0=%d spawnN=%d strength: %d→%d billed=%d (~%.2f/move)",
		n, movesWithSpawn, movesNoSpawn, movesMulti, startStr, prevStr, strDeltaSum,
		float64(strDeltaSum)/float64(max(1, n)))
	t.Logf("cell  obs  exp-uniform")
	var totExp float64
	totObs := 0
	for i := 0; i < 16; i++ {
		t.Logf("%3d %5d %8.2f", i, obs[i], exp[i])
		totObs += obs[i]
		totExp += exp[i]
	}
	chi := 0.0
	for i := 0; i < 16; i++ {
		if exp[i] > 0 {
			d := float64(obs[i]) - exp[i]
			chi += d * d / exp[i]
		}
	}
	t.Logf("total obs=%d exp=%.1f chi2=%.1f (df≈15; <25 ≈ uniform, >55 = strongly non-uniform)", totObs, totExp, chi)
}
