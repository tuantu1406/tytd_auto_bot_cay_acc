package tytd_go

// Game-account snatching on EVENT login (owner 2026-10-10): when a leader logs
// an event account whose game acc@server is ALREADY hanging somewhere else
// (the owner's Player session under the users-row key, or a twin EventPlayer
// under another event id), stopGameAccountSessions must take the old session
// down first — the game only keeps one connection per account@server, and a
// left-behind twin means the bot "ôm" buffers/goroutines for a dead socket
// (double memory per account → contributes to the OOM pressure).

import (
	"testing"
	"tytd_go/internal/model"
)

func TestStopGameAccountSessions(t *testing.T) {
	bot := newTestWebBot(t)
	db := bot.ydb.GetDB()

	// users row for the Player hang so its Running flag flip is observable.
	if err := model.CreateUser(db, &model.User{ID: "1234", Username: "accA", ServerID: 5, Running: true}); err != nil {
		t.Fatal(err)
	}
	// event row for the foreign EventPlayer twin.
	if err := model.CreateEventUser(db, &model.EventUser{ID: "8000000001", Username: "accA", ServerID: 5, Running: true, LeaderID: "999"}); err != nil {
		t.Fatal(err)
	}

	// 1) Same account, same server Player hang → must die.
	bot.session.Store(int64(1234), &Player{Running: true, Username: "accA", ServerID: 5})
	// 2) Foreign event twin, same acc@server → must die.
	bot.session.Store(int64(8000000001), &EventPlayer{Running: true, Username: "accA", ServerID: 5})
	// 3) Same account, DIFFERENT server → legitimate co-existence, keep.
	bot.session.Store(int64(2345), &Player{Running: true, Username: "accA", ServerID: 6})
	// 4) Different account → keep.
	bot.session.Store(int64(3456), &Player{Running: true, Username: "accB", ServerID: 5})
	// 5) Stale (not running) twin → keep as-is (cleanup belongs to its own path).
	bot.session.Store(int64(4567), &Player{Running: false, Username: "accA", ServerID: 5})

	// eventID's OWN key must never be touched by the scan.
	bot.session.Store(int64(8000000002), &EventPlayer{Running: true, Username: "accA", ServerID: 5})

	n := bot.stopGameAccountSessions("8000000002", "accA", 5)
	if n != 2 {
		t.Fatalf("stopped=%d want 2", n)
	}
	for _, k := range []int64{1234, 8000000001} {
		if _, ok := bot.session.Load(k); ok {
			t.Fatalf("session %d survived the snatch", k)
		}
	}
	for _, k := range []int64{2345, 3456, 4567, 8000000002} {
		if _, ok := bot.session.Load(k); !ok {
			t.Fatalf("session %d must NOT be touched (key/username/server/running mismatch or own key)", k)
		}
	}
	if u, err := model.GetUser(db, "1234"); err != nil || u.Running {
		t.Fatalf("users row 1234: Running=%v err=%v, want false", u.Running, err)
	}
	if u, err := model.GetEventUser(db, "8000000001"); err != nil || u.Running {
		t.Fatalf("event row 8000000001: Running=%v err=%v, want false", u.Running, err)
	}

	// Empty username (row half-migrated) must never mass-kill.
	bot.session.Store(int64(5678), &Player{Running: true, Username: "", ServerID: 5})
	if n := bot.stopGameAccountSessions("8000000003", "", 5); n != 0 {
		t.Fatalf("empty username stopped %d want 0", n)
	}
	if _, ok := bot.session.Load(int64(5678)); !ok {
		t.Fatal("empty-username guard failed: unrelated session was killed")
	}
}
