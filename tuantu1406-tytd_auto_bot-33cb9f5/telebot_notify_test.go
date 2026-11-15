package tytd_go

import (
	"strings"
	"testing"
	"time"
	"tytd_go/internal/model"
)

// The kick notice must reach Telegram through the durable noti channel no
// matter who triggered the login (web closures are throwaway), and must name
// the account when it lands in a leader's chat serving many clones.
func TestKickNoticeRouting(t *testing.T) {
	if got := kickNoticeMsg(100, 100, "acct"); got != "bi da' ra roi` đạo hữu" {
		t.Fatalf("own-chat kick notice: %q", got)
	}
	got := kickNoticeMsg(100, 900, "acct")
	if !strings.Contains(got, "acct") || !strings.Contains(got, "bị đá ra rồi") {
		t.Fatalf("member kick notice must name the account: %q", got)
	}

	bot := &TeleBot{}
	done := make(chan struct{})
	go func() { bot.pushNotify(1, "x"); close(done) }() // nil channel: drop, never block
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pushNotify blocked on a nil channel")
	}

	bot.notiChan = make(chan NotifyMGS, 4)
	bot.pushNotify(42, "bi da' ra roi` đạo hữu")
	select {
	case m := <-bot.notiChan:
		if m.ChatID != 42 || m.MSG == "" {
			t.Fatalf("bad notify: %+v", m)
		}
	default:
		t.Fatal("pushNotify dropped a message with room in the buffer")
	}
}

// A member's game notices route to the leader's chat; a regular user (and
// any account without a leader) keeps its own. The member name is prefixed
// on every message, so one leader chat still reads per-account.
func TestNotifyChatFor(t *testing.T) {
	if got := notifyChatFor(&model.User{ID: "700", LeaderID: "500"}, 700); got != 500 {
		t.Fatalf("member routed to %d, want leader 500", got)
	}
	if got := notifyChatFor(&model.User{ID: "700"}, 700); got != 700 {
		t.Fatalf("regular user routed to %d, want own 700", got)
	}
	// Junk leader id falls back to the account's own chat, never 0.
	if got := notifyChatFor(&model.User{ID: "700", LeaderID: "not-a-number"}, 700); got != 700 {
		t.Fatalf("junk leader id routed to %d, want own 700", got)
	}
}
