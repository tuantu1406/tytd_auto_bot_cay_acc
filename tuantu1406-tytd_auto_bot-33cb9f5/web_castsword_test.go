package tytd_go

// Web surface for Luyện Khí Đại Hội: the 🗡 button must render ONLY while the
// type-119 window is open on a RUNNING session (ActList is in-memory truth),
// on both the leader's member rows and the account's own dashboard; POST
// lkstart hands the board to the same resume-only auto-play engine as
// /start_luyenkhi (no item, no SETTLEMENT/GAMEOVER, ever).

import (
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// lkMock is a strength-2 scripted CastSword server (mirrors
// TestCastSwordAutoPlayScripted): the run ends "hết thể lực" fast so the web
// test exercises the whole goroutine including the stop notifications.
func lkMock(t *testing.T, p *Player) {
	t.Helper()
	board := csBoardOf(0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0)
	score, strength := int32(7), int32(2)
	entity := func() *proto.CastSwordBaseData {
		e := &proto.CastSwordBaseData{Score: &score, HighScore: &score, Strength: &strength}
		for _, v := range board {
			lv := v
			e.Cells = append(e.Cells, &proto.CastSwordCellInfo{Level: &lv})
		}
		return e
	}
	enter := func([]byte) []byte {
		m, _ := pb.Marshal(&proto.CastSwordEnterResp{Ret: pb.Int32(0), Entity: entity()})
		return m
	}
	mv := func(body []byte) []byte {
		dir := csBodyDir(body)
		nb, g, _ := csSlide(board, int(dir))
		for i := range nb {
			if nb[i] == 0 {
				nb[i] = 1
				break
			}
		}
		board, score, strength = nb, score+g, strength-1
		m, _ := pb.Marshal(&proto.CastSwordMoveResp{Ret: pb.Int32(0), Entity: entity()})
		return m
	}
	p.csMoveGap = time.Millisecond
	csServer(t, p, map[string]func([]byte) []byte{
		SERVER_CASTSWORD_ENTER: enter, SERVER_CASTSWORD_MOVE: mv,
	})
}

func TestWebLuyenKhiButtonVisibility(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8700")
	seedMember(t, bot, "8701", "8700", 30, time.Time{})
	cookie, _ := loginAs(t, bot, h, 8700)

	// Offline member: no activity truth exists, so no button.
	if body := doGet(t, h, "/members", cookie).Body.String(); strings.Contains(body, `value="lkstart"`) {
		t.Fatal("lkstart rendered for an offline member")
	}

	p := csTestPlayer(t)
	p.UID = 8701
	bot.session.Store(int64(8701), p)
	if body := doGet(t, h, "/members", cookie).Body.String(); !strings.Contains(body, `value="lkstart"`) {
		t.Fatal("lkstart missing while type-119 window is open")
	}

	// Same member, window CLOSED (ended): button disappears.
	now := time.Now().UnixMilli()
	setActivities(&p.ActList, activityEntry{ActivityID: 9062364, Type: activityTypeCastSword,
		BeginTime: now - 60_000, EndTime: now - 1000})
	if body := doGet(t, h, "/members", cookie).Body.String(); strings.Contains(body, `value="lkstart"`) {
		t.Fatal("lkstart still rendered after the activity window closed")
	}

	// Own dashboard: same gate on the account's own session.
	self := csTestPlayer(t)
	self.UID = 8700
	bot.session.Store(int64(8700), self)
	if body := doGet(t, h, "/", cookie).Body.String(); !strings.Contains(body, "Tự chơi Luyện Khí") {
		t.Fatal("dashboard missing the Luyện Khí button for its own open activity")
	}
}

func TestWebLuyenKhiStartAction(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8710")
	seedMember(t, bot, "8711", "8710", 30, time.Time{})
	cookie, csrf := loginAs(t, bot, h, 8710)

	// Offline member refused.
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"lkstart"}, "member_id": {"8711"}})); !isErr {
		t.Fatal("lkstart accepted for an offline member")
	}
	// Foreign member refused (ownership gate).
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"lkstart"}, "member_id": {"9999"}})); !isErr {
		t.Fatal("lkstart accepted for a member the leader does not own")
	}

	p := csTestPlayer(t)
	p.UID = 8711
	lkMock(t, p)
	bot.session.Store(int64(8711), p)
	msg, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"lkstart"}, "member_id": {"8711"}}))
	if isErr {
		t.Fatalf("lkstart refused: %q", msg)
	}
	if !strings.Contains(msg, "Luyện Khí") {
		t.Fatalf("flash unrelated: %q", msg)
	}

	// The run (strength 2) must finish on its own and notify BOTH the member
	// and the leader. Poll the notify channel itself — waiting on csPlaying
	// first would race the goroutine that sets it.
	seen := map[int64]string{}
	deadline := time.Now().Add(15 * time.Second)
	for len(seen) < 2 && time.Now().Before(deadline) {
		select {
		case n := <-bot.notiChan:
			seen[n.ChatID] = n.MSG
		case <-time.After(20 * time.Millisecond):
		}
	}
	for _, id := range []int64{8711, 8710} {
		msg, ok := seen[id]
		if !ok {
			t.Fatalf("no stop notification for %d: %v", id, seen)
		}
		if !strings.Contains(msg, "Luyện Khí: dừng (hết thể lực)") {
			t.Fatalf("notify for %d unexpected: %q", id, msg)
		}
	}
	p.CsMu.Lock()
	playing := p.csPlaying
	p.CsMu.Unlock()
	if playing {
		t.Fatal("autoplay still running after stop notifications")
	}
	for _, f := range drainedFrames(p) {
		if len(f) >= 10 {
			switch cmd := hex.EncodeToString(f[6:10]); cmd {
			case CASTSWORD_SETTLEMENT_CMD, CASTSWORD_GAMEOVER_CMD:
				t.Fatalf("web auto-play ended the run with %s", cmd)
			}
		}
	}
}

// TestWebLuyenKhiAllAction pins the leader's bulk 🗡 button (owner
// 2026-11-11): visible only while ≥1 online member has 119 open, fires the
// resume-only engine on every online+open member, counts busy/offline/closed
// instead of blocking, and never double-starts a member mid-ván.
func TestWebLuyenKhiAllAction(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8720")
	seedMember(t, bot, "8721", "8720", 30, time.Time{})
	seedMember(t, bot, "8722", "8720", 30, time.Time{}) // stays offline
	cookie, csrf := loginAs(t, bot, h, 8720)

	if body := doGet(t, h, "/members", cookie).Body.String(); strings.Contains(body, `value="lkstart_all"`) {
		t.Fatal("lkstart_all rendered with no online member")
	}

	on := csTestPlayer(t)
	on.UID = 8721
	lkMock(t, on)
	bot.session.Store(int64(8721), on)
	if body := doGet(t, h, "/members", cookie).Body.String(); !strings.Contains(body, `value="lkstart_all"`) {
		t.Fatal("lkstart_all hidden while an online member has 119 open")
	}

	// Busy member → counted, never double-started.
	on.CsMu.Lock()
	on.csPlaying = true
	on.CsMu.Unlock()
	if msg, _ := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"lkstart_all"}})); !strings.Contains(msg, "đang có ván") {
		t.Fatalf("busy member not counted: %q", msg)
	}
	on.CsMu.Lock()
	on.csPlaying = false
	on.CsMu.Unlock()

	// Online+open fires; offline member is the "chưa login" bucket.
	msg, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"lkstart_all"}}))
	if isErr || !strings.Contains(msg, "Đã giao 1") || !strings.Contains(msg, "1 chưa login") {
		t.Fatalf("bulk fire flash unexpected: %q (isErr=%v)", msg, isErr)
	}
	// Leader (8720) must learn the outcome of the member's run too.
	deadline := time.Now().Add(15 * time.Second)
	gotLeader := false
	for !gotLeader && time.Now().Before(deadline) {
		select {
		case n := <-bot.notiChan:
			if n.ChatID == 8720 && strings.Contains(n.MSG, "Luyện Khí: dừng") {
				gotLeader = true
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !gotLeader {
		t.Fatal("leader never notified about the member's run")
	}
}
