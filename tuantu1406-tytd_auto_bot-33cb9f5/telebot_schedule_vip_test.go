package tytd_go

// VIP law on the auto-login paths (owner 2026-11-11: "có khách báo acc hết
// vip rồi nhưng bot vẫn auto login"). The daily login schedule was the ONE
// auto-login path without a VIP gate (autoStartChan has had its
// disable+notify since 2026-10-27; fleet/web-leader skip in their own loops).
// Fixed at two levels, pinned here:
//
//  1. applyLoginSchedules: expired-VIP row → login_schedule is CLEARED (the
//     trigger turns itself off, one 🛑 notify — same law as auto_login) and
//     NO login starts. Banned row → skipped, schedule left in place.
//  2. startPlayerLogin: ban+VIP refuse at the chokepoint, before the session
//     store — defence-in-depth for every caller (scheduler, web, debugweb).
//
// Every case seeds ineligible rows only, so nothing can reach the network:
// the gate fires before p.Run() is ever spawned.

import (
	"strings"
	"testing"
	"tytd_go/internal/model"
)

func TestApplyLoginSchedulesExpiredVIPDisablesSchedule(t *testing.T) {
	bot := newTestWebBot(t)
	u := seedUser(t, bot, "9101", -1) // VipLevel 1, expired yesterday
	if err := model.SetLoginSchedule(bot.ydb.GetDB(), u.ID, "7:00"); err != nil {
		t.Fatal(err)
	}

	bot.applyLoginSchedules(7, 0)

	if got, err := model.GetUser(bot.ydb.GetDB(), "9101"); err != nil {
		t.Fatal(err)
	} else if got.LoginSchedule != "" {
		t.Fatalf("expired-VIP schedule not disabled: %q", got.LoginSchedule)
	}
	select {
	case n := <-bot.notiChan:
		if !strings.Contains(n.MSG, "🛑") || !strings.Contains(n.MSG, "VIP") {
			t.Fatalf("notify not the VIP-disable message: %q", n.MSG)
		}
		if n.ChatID != 9101 {
			t.Fatalf("notify to wrong chat: %d", n.ChatID)
		}
	default:
		t.Fatal("no 🛑 notify pushed when the expired-VIP schedule fired")
	}
	if _, ok := bot.session.Load(int64(9101)); ok {
		t.Fatal("expired-VIP account got a session")
	}
}

func TestApplyLoginSchedulesBannedSkipped(t *testing.T) {
	bot := newTestWebBot(t)
	u := seedUser(t, bot, "9102", 30) // healthy VIP, but banned
	if err := model.SetLoginSchedule(bot.ydb.GetDB(), u.ID, "7:00"); err != nil {
		t.Fatal(err)
	}
	if err := model.AddBannedUser(bot.ydb.GetDB(), 9102, u.Username, "test"); err != nil {
		t.Fatal(err)
	}

	bot.applyLoginSchedules(7, 0)

	if _, ok := bot.session.Load(int64(9102)); ok {
		t.Fatal("banned account got a session")
	}
	// Ban is not an expiry: the schedule stays for when the ban lifts —
	// only the VIP law auto-clears triggers.
	if got, err := model.GetUser(bot.ydb.GetDB(), "9102"); err != nil {
		t.Fatal(err)
	} else if got.LoginSchedule != "7:00" {
		t.Fatalf("banned user's schedule wrongly cleared: %q", got.LoginSchedule)
	}
	select {
	case n := <-bot.notiChan:
		t.Fatalf("banned skip must be silent, got: %q", n.MSG)
	default:
	}
}

func TestStartPlayerLoginRefusesExpiredAndZeroVIP(t *testing.T) {
	bot := newTestWebBot(t)
	zero := seedUser(t, bot, "9103", 30)
	zero.VipLevel = 0
	if err := model.CreateUser(bot.ydb.GetDB(), zero); err != nil {
		t.Fatal(err)
	}
	seedUser(t, bot, "9104", -1) // expired
	banned := seedUser(t, bot, "9105", 30)
	if err := model.AddBannedUser(bot.ydb.GetDB(), 9105, banned.Username, "test"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id   int64
		want string // substring the refusal must carry
	}{
		{9103, "VIP"},     // VipLevel 0
		{9104, "VIP"},     // expired
		{9105, "cấm"},     // banned
	} {
		var msg string
		if bot.startPlayerLogin(tc.id, func(text string) { msg = text }) {
			t.Fatalf("startPlayerLogin accepted %d despite the gate", tc.id)
		}
		if !strings.Contains(msg, tc.want) {
			t.Fatalf("refusal for %d lacks %q: %q", tc.id, tc.want, msg)
		}
		if _, ok := bot.session.Load(tc.id); ok {
			t.Fatalf("%d got a session past the gate", tc.id)
		}
	}
}
