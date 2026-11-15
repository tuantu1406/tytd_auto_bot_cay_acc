package tytd_go

// VIP WATCHDOG (owner 2026-11-08): "acc đang onl ai quá hạn vip thì đá
// ra, 10' check 1 lần, nhớ exclude acc event". The login gates (t.auth /
// webGate / the leader fleet loop) already refuse EXPIRED accounts, but
// nothing evicted an account whose VIP lapsed WHILE online — it kept
// playing forever. Every 10 minutes this sweeps t.session: every live
// *Player whose DB row says VipExpireAt passed (or VipLevel==0) is
// cancelled, dropped from the session map, marked not-running, its web
// state re-pushed, and the owner is notified with the /redeem hint.
// Event accounts are excluded by type: sessions hold *Player and
// *EventPlayer and only the Player branch is ever touched — event_users
// have no VIP at all (telebot_ai.go: "không cần VIP"), and the delegated
// (uỷ thác) EventPlayer under a user key is likewise untouched.
// The VIP truth is read fresh from the DB each tick — session Player
// fields are a login-time snapshot, the DB is the source of record (same
// law as the login gates and the fleet loop's VipExpireAt/VipLevel check).

import (
	"fmt"
	"log"
	"strconv"
	"time"
	"tytd_go/internal/model"
)

const vipWatchdogGap = 10 * time.Minute

// startVipWatchdog launches the 10-minute eviction loop (called from
// TeleBot.Run next to the chuot scheduler).
func (t *TeleBot) startVipWatchdog() {
	go func() {
		for {
			time.Sleep(vipWatchdogGap)
			t.vipWatchdogTick()
		}
	}()
}

func (t *TeleBot) vipWatchdogTick() {
	now := time.Now()
	t.session.Range(func(key, val any) bool {
		p, isP := val.(*Player)
		if !isP || !p.Running {
			return true // *EventPlayer (event + deputy) → excluded by law
		}
		idStr := fmt.Sprintf("%v", key)
		u, err := model.GetUser(t.ydb.GetDB(), idStr)
		if err != nil {
			return true // vanished row: the disconnect paths already clean it
		}
		if u.VipLevel != 0 && !u.VipExpireAt.Before(now) {
			return true // still VIP — nothing to do
		}
		log.Printf("[vip_watchdog] đá %s (id %s): hết VIP (expire %v level %d)",
			p.Username, idStr, u.VipExpireAt.In(ictZone).Format("02/01 15:04"), u.VipLevel)
		if p.cancel != nil {
			p.cancel()
		}
		t.sessionDel(key, p)
		u.Running = false
		model.CreateUser(t.ydb.GetDB(), u)
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			t.pushWebState(id)
		}
		t.pushNotify(p.ChatID, fmt.Sprintf(
			"⏰ Acc %s hết VIP — bot đã đá khỏi game. /redeem <code> để chơi tiếp.",
			p.Username))
		return true
	})
}
