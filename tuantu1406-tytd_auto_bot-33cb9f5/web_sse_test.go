package tytd_go

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/model"
)

func TestSSEHubFanout(t *testing.T) {
	h := &sseHub{byChat: map[int64]map[*sseClient]bool{}}
	a := h.subscribe(7)
	b := h.subscribe(7)
	c := h.subscribe(8)

	h.push(7, "notify", map[string]string{"text": "hello"})
	for _, cl := range []*sseClient{a, b} {
		select {
		case m := <-cl.ch:
			if m.event != "notify" || !strings.Contains(m.data, "hello") {
				t.Fatalf("bad frame: %+v", m)
			}
		default:
			t.Fatal("subscribed client missed the frame")
		}
	}
	select {
	case m := <-c.ch:
		t.Fatalf("chat 8 got chat 7's frame: %+v", m)
	default:
	}

	// A client that stopped reading must never block the producer.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			h.push(7, "notify", "x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("push blocked on a full client buffer")
	}

	h.unsubscribe(a)
	h.unsubscribe(b)
	h.mu.Lock()
	_, still := h.byChat[7]
	h.mu.Unlock()
	if still {
		t.Fatal("chat bucket left behind after all clients unsubscribed")
	}
	_ = c
}

func TestSSEGates(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	if rec := doGet(t, h, "/sse", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("anonymous /sse: code=%d", rec.Code)
	}
	if err := model.SetConfig(bot.ydb.GetDB(), model.ConfigKeyWebEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	if rec := doGet(t, h, "/sse", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled /sse: code=%d", rec.Code)
	}
}

type sseFrame struct{ event, data string }

// readFrame blocks until one full SSE frame arrives (or the deadline kills
// the test). Comment lines (heartbeats) and retry: lines are skipped.
func readFrame(t *testing.T, br *bufio.Reader, d time.Duration) sseFrame {
	t.Helper()
	type res struct {
		f   sseFrame
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var f sseFrame
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ch <- res{f, err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if f.event != "" || f.data != "" {
					ch <- res{f, nil}
					return
				}
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				f.event = line[len("event: "):]
			} else if strings.HasPrefix(line, "data: ") {
				f.data = line[len("data: "):]
			}
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read SSE frame: %v", r.err)
		}
		return r.f
	case <-time.After(d):
		t.Fatal("timeout waiting for SSE frame")
		return sseFrame{}
	}
}

// The whole live wire: connect with the session cookie, get an immediate
// state snapshot reflecting the live player, then a hub push (what the
// Telegram notify loop does) must surface as a notify frame.
func TestSSEStreamStateAndNotify(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(7050)
	seedUser(t, bot, "7050", 30)
	bot.session.Store(chatID, &Player{Running: true, ChuotRun: true, Peach: true, XaVe: true})
	cookie, _ := loginAs(t, bot, h, chatID)

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: webCookieName, Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type=%q", ct)
	}
	br := bufio.NewReader(resp.Body)

	f := readFrame(t, br, 5*time.Second)
	if f.event != "state" {
		t.Fatalf("first frame event=%q data=%q", f.event, f.data)
	}
	var st sseState
	if err := json.Unmarshal([]byte(f.data), &st); err != nil {
		t.Fatalf("state frame not json: %v (%s)", err, f.data)
	}
	if !st.Running || !st.Toggles["chuot"] || !st.Toggles["peach"] || !st.Toggles["xave"] {
		t.Fatalf("state snapshot wrong: %+v", st)
	}

	webSSEHub.push(chatID, "notify", map[string]string{"text": "acc bi da ra roi"})
	f = readFrame(t, br, 5*time.Second)
	if f.event != "notify" || !strings.Contains(f.data, "bi da ra roi") {
		t.Fatalf("notify frame: event=%q data=%q", f.event, f.data)
	}
}

// The push-on-change contract: a state change must reach the browser without
// waiting for the 5 s tick — (a) a member coming up lands on the LEADER's
// stream (pushWebState routes member ids to the owner), with the fleet count
// and the up-map row pill both updated, and (b) a web toggle pushes back to
// the toggler's own stream. Each frame is awaited with a 2 s deadline, well
// under the tick interval, so a missing push fails the test rather than
// passing via the tick.
func TestSSEPushOnStateChange(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	leaderID := int64(8700)
	seedLeader(t, bot, "8700")
	seedMember(t, bot, "8701", "8700", 30, time.Time{})
	cookie, csrf := loginAs(t, bot, h, leaderID)

	srv := httptest.NewServer(h)
	defer srv.Close()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: webCookieName, Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	waitState := func(what string, ok func(sseState) bool) sseState {
		t.Helper()
		for {
			f := readFrame(t, br, 2*time.Second)
			if f.event != "state" {
				continue
			}
			var st sseState
			if err := json.Unmarshal([]byte(f.data), &st); err != nil {
				t.Fatalf("state frame not json: %v (%s)", err, f.data)
			}
			if ok(st) {
				return st
			}
		}
	}

	f := readFrame(t, br, 5*time.Second)
	if f.event != "state" {
		t.Fatalf("first frame event=%q", f.event)
	}
	var first sseState
	if err := json.Unmarshal([]byte(f.data), &first); err != nil {
		t.Fatal(err)
	}
	if first.Running || (first.Fleet != nil && first.Fleet.MembersUp != 0) {
		t.Fatalf("initial snapshot should be all-off: %+v", first)
	}
	if first.Up == nil {
		t.Fatal("leader frame must carry a non-nil up map (empty = all rows off)")
	}

	// Member session comes up (what Run() does on game login success).
	bot.session.Store(int64(8701), &Player{Running: true})
	bot.pushWebState(8701)
	st := waitState("member up", func(s sseState) bool {
		return s.Fleet != nil && s.Fleet.MembersUp == 1 && s.Up["8701"]
	})
	if st.Up == nil || !st.Up["8701"] {
		t.Fatalf("up map missing member: %+v", st)
	}

	// The leader's own toggle round-trips through pushWebState.
	bot.session.Store(leaderID, &Player{Running: true})
	if rec := doPost(t, h, "/toggle", cookie, csrf, map[string][]string{"feat": {"chuot_on"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("toggle POST: code=%d", rec.Code)
	}
	waitState("chuot on", func(s sseState) bool { return s.Toggles["chuot"] })
}
