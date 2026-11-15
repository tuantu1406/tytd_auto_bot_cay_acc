//go:build live

package tytd_go

// Event-account LHBC diagnostic: log in like a real event user (full Run():
// auth → activity push → rebornTrialEventLoop) and print exactly where the
// auto engine stands — is 172 in the pushed list, what phase does the
// calendar say, does ENTER_ACTIVITY answer. The loop's own [LHBC] log lines
// stream with -v.
//
//	TYTD_LIVE=1 TYTD_RBT_EP=1 go test -tags "sqlcipher live" -count=1 -run TestRebornTrialEPLive -v -timeout 180s .

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRebornTrialEPLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_RBT_EP") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_RBT_EP=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710402
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}
	db, err := gorm.Open(sqlite.Open("file:rbtep?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.EventUser{}); err != nil {
		t.Fatal(err)
	}
	p := &EventPlayer{
		Username: user, Password: pass, ServerID: server,
		UID: 1, ChatID: 7, DB: db, Notify: true,
		NotiChan: make(chan NotifyMGS, 256),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx
	go func() {
		if !p.Run() {
			log.Printf("EP RUN returned false (login failed)")
		}
	}()
	for i := 0; i < 40 && !p.Running; i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if !p.Running {
		t.Fatal("event account never came online")
	}
	fmt.Printf("== EP %s@%d online, gameUID %d\n", user, server, p.UID)

	// Wait for the activity push and report every type-172 row the server sent.
	var actID int32
	for i := 0; i < 60; i++ {
		all := p.ActList.snapshot()
		if i == 59 || actID == 0 {
			fmt.Printf("  t+%ds list rows=%d\n", i, len(all))
			if actID == 0 {
				for _, e := range all {
					if e.Type == rbtActivityType || e.Type == 68 || e.Type == 199 {
						fmt.Printf("    row act %d type %d status %s\n", e.ActivityID, e.Type, e.status(time.Now()))
					}
				}
			}
		}
		for _, e := range p.ActList.byType(rbtActivityType) {
			st := e.status(time.Now())
			fmt.Printf("  act %d type %d begin %s end %s status=%s\n",
				e.ActivityID, e.Type,
				time.UnixMilli(e.BeginTime).Format("01-02 15:04"),
				time.UnixMilli(e.EndTime).Format("01-02 15:04"), st)
			if st == "active" || st == "reward" {
				actID = e.ActivityID
			}
		}
		if actID != 0 {
			break
		}
		time.Sleep(time.Second)
	}
	if actID == 0 {
		fmt.Println("== 172 KHÔNG có trong danh sách sự kiện — lỗi nằm ở push 21001/actList")
		return
	}
	_, begin, ok := p.rbtActiveActivity()
	fmt.Printf("== rbtActiveActivity: actID %d begin %v ok=%v\n", actID, time.UnixMilli(begin), ok)
	st, round, nextBound := rbtPhase(begin, p.rbtCfg(), time.Now())
	fmt.Printf("== rbtPhase: state=%d round=%d nextBound=%d (%s) — cfg rows folded=%d\n",
		st, round, nextBound, time.UnixMilli(nextBound).Format("01-02 15:04"), len(p.rbtCfg()))
	p.RbtMu.Lock()
	num, free, recov, roundNo := p.rbtBattleNum, p.rbtFreeAttempts, p.rbtBattleRecov, p.rbtRoundNo
	p.RbtMu.Unlock()
	fmt.Printf("== meter: battleNum=%d free=%d recov=%d roundNo=%d\n", num, free, recov, roundNo)

	// Direct gate probe (outside the loop's phase decision): what does the
	// server itself say about this account's qualification?
	ea, err := p.rbtEnterActivity(actID)
	if err != nil {
		fmt.Printf("== EnterActivity ERR: %v\n", err)
	} else {
		fmt.Printf("== EnterActivity ret=%d unionId=%d rank=%d round=%d\n",
			ea.GetRet(), ea.GetUnionId(), ea.GetSelfRank(), ea.GetRound())
	}
	en, err := p.rbtEnter(actID)
	if err != nil {
		fmt.Printf("== ENTER ERR: %v\n", err)
	} else {
		pd := en.GetPlayerData()
		fmt.Printf("== ENTER ret=%d", en.GetRet())
		if pd != nil {
			fmt.Printf(" level=%d exp=%d hp=%d battleNum=%d", pd.GetLevel(), pd.GetExp(), pd.GetHp(), pd.GetBattleNum())
		}
		fmt.Println()
	}
	// Let the loop tick at least once (8s initial sleep + immediate first pass).
	fmt.Println("== chờ 60s cho loop tick — xem log [LHBC] ở trên…")
	for i := 0; i < 12; i++ {
		time.Sleep(5 * time.Second)
		p.RbtMu.Lock()
		var seats []string
		for k := range p.rbtChans {
			seats = append(seats, k)
		}
		p.RbtMu.Unlock()
		fmt.Printf("  t+%ds seats-waiting=%v\n", (i+1)*5, seats)
	}
}
