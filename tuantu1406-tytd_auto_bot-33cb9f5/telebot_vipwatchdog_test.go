package tytd_go

// VIP watchdog (owner 2026-11-08): online *Player whose DB VIP lapsed is
// cancelled + evicted + Running cleared + owner notified; valid VIP stays;
// *EventPlayer (the event-account exclusion) is NEVER touched, even with
// the user row expired.

import (
	"context"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/model"
)

func vipDogUser(t *testing.T, bot *TeleBot, id string, expire time.Time, level int) {
	t.Helper()
	u := &model.User{ID: id, Username: "acc" + id, ServerID: 1, VipLevel: level, VipExpireAt: expire, Running: true}
	if err := model.CreateUser(bot.ydb.GetDB(), u); err != nil {
		t.Fatal(err)
	}
}

func TestVipWatchdogEvictsExpired(t *testing.T) {
	bot := newTestWebBot(t)
	vipDogUser(t, bot, "12345", time.Now().Add(-time.Hour), 1)
	ctx, cancel := context.WithCancel(context.Background())
	p := &Player{Username: "acc12345", ChatID: 12345, Running: true, ctx: ctx, cancel: cancel}
	bot.session.Store(int64(12345), p)

	bot.vipWatchdogTick()

	select {
	case <-ctx.Done():
	default:
		t.Fatal("expired player was not cancelled")
	}
	if _, ok := bot.session.Load(int64(12345)); ok {
		t.Fatal("session entry survived the eviction")
	}
	u, err := model.GetUser(bot.ydb.GetDB(), "12345")
	if err != nil || u.Running {
		t.Fatalf("DB Running not cleared: %+v (%v)", u, err)
	}
	select {
	case msg := <-bot.notiChan:
		if !strings.Contains(msg.MSG, "hết VIP") || msg.ChatID != 12345 {
			t.Fatalf("bad notice %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("owner never notified")
	}
}

func TestVipWatchdogKeepsValidAndDeputyEvent(t *testing.T) {
	bot := newTestWebBot(t)
	// valid VIP player — must survive
	vipDogUser(t, bot, "22222", time.Now().Add(time.Hour), 1)
	ctx, cancel := context.WithCancel(context.Background())
	good := &Player{Username: "good", ChatID: 22222, Running: true, ctx: ctx, cancel: cancel}
	bot.session.Store(int64(22222), good)
	// delegated EventPlayer under a user key with an EXPIRED row — the
	// event exclusion law: never touched even though the row says expired.
	vipDogUser(t, bot, "33333", time.Now().Add(-time.Minute), 1)
	ectx, ecancel := context.WithCancel(context.Background())
	ep := &EventPlayer{Username: "ev", ChatID: 33333, Running: true, ctx: ectx, cancel: ecancel}
	bot.session.Store("33333", ep)
	// string-keyed member with an expired VIP — kicked (id is a numeric
	// string key, not int64: exercises the %v key path)
	vipDogUser(t, bot, "987654", time.Now().Add(-time.Hour), 1)
	mctx, mcancel := context.WithCancel(context.Background())
	member := &Player{Username: "mem", ChatID: 22222, Running: true, ctx: mctx, cancel: mcancel}
	bot.session.Store("987654", member)

	bot.vipWatchdogTick()

	select {
	case <-ctx.Done():
		t.Fatal("valid VIP player must not be cancelled")
	default:
	}
	if _, ok := bot.session.Load(int64(22222)); !ok {
		t.Fatal("valid player lost its session")
	}
	select {
	case <-ectx.Done():
		t.Fatal("event account must NEVER be evicted by the VIP watchdog")
	default:
	}
	if _, ok := bot.session.Load("33333"); !ok {
		t.Fatal("event session dropped")
	}
	select {
	case <-mctx.Done():
	default:
		t.Fatal("expired member was not cancelled")
	}
	if _, ok := bot.session.Load("987654"); ok {
		t.Fatal("member session entry survived")
	}
}
