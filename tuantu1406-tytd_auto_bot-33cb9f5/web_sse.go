package tytd_go

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
	"tytd_go/internal/model"
)

// Server-Sent Events: the web page's live wire. One long-lived GET /sse per
// browser session pushes, without any client polling:
//
//	event: state   — full dashboard/fleet snapshot every 5 s (running pill,
//	                 toggles, VIP, member/event fleet counts, per-event-account
//	                 island status lines — the /events rows re-paint in place)
//	event: notify  — every game notification as it goes out on Telegram
//	                 (hooked into the notiChan drain loop), shown as a toast
//	event: expire  — the account lost its gate (VIP lapsed / banned): the page
//	                 redirects itself to /login
//
// Identity is the same tytd_wsess cookie as every other page (EventSource is
// same-origin, so it rides along). The stream also emits an SSE comment
// heartbeat every 25 s so TLS tunnels (cloudflared) never reap an idle line.
// The fan-out hub is process-global and non-blocking: a slow or vanished
// browser drops frames instead of ever slowing the game bots down.

type sseMsg struct {
	event string
	data  string
}

type sseClient struct {
	chatID int64
	ch     chan sseMsg
}

type sseHub struct {
	mu     sync.Mutex
	byChat map[int64]map[*sseClient]bool
}

var webSSEHub = &sseHub{byChat: map[int64]map[*sseClient]bool{}}

func (h *sseHub) subscribe(chatID int64) *sseClient {
	c := &sseClient{chatID: chatID, ch: make(chan sseMsg, 32)}
	h.mu.Lock()
	if h.byChat[chatID] == nil {
		h.byChat[chatID] = map[*sseClient]bool{}
	}
	h.byChat[chatID][c] = true
	h.mu.Unlock()
	return c
}

func (h *sseHub) unsubscribe(c *sseClient) {
	h.mu.Lock()
	if set := h.byChat[c.chatID]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.byChat, c.chatID)
		}
	}
	h.mu.Unlock()
	// c.ch is intentionally not closed: after removal no producer can reach
	// it, and a pump still parked on receive would wake with a zero sseMsg
	// the writer loop has already stopped using. GC reclaims both.
}

// push queues one event to every live stream of chatID. Never blocks: a
// client whose browser stopped reading (32-deep buffer full) simply misses
// frames; the next state tick resyncs it.
func (h *sseHub) push(chatID int64, event string, v any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.byChat[chatID]) == 0 {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	for c := range h.byChat[chatID] {
		select {
		case c.ch <- sseMsg{event: event, data: string(b)}:
		default:
		}
	}
}

// sseState is the JSON body of one state frame.
type sseState struct {
	Running   bool            `json:"running"`
	Toggles   map[string]bool `json:"toggles"`
	Vip       int             `json:"vip"`
	VipExpire string          `json:"vip_expire,omitempty"`
	GameUID   uint64          `json:"uid,omitempty"`
	Server    int             `json:"server"`
	Fleet     *sseFleet       `json:"fleet,omitempty"`
	// Up lists the account ids (members + event accounts) whose session is
	// currently running, so the Đệ tử/Sự kiện row pills re-paint live. For a
	// leader it is always non-nil — no omitempty here on purpose: an empty
	// map MUST reach the browser, or rows could never flip back to off.
	// null (non-leaders) makes the client skip row updates.
	Up map[string]bool `json:"up"`
	// EvStat maps each event account id to its composed island status line
	// ("Bồng Lai: … · Côn Lôn: …") — the exact string the /events template
	// renders, from the same eventIslandLine helper — so the Sự kiện rows
	// re-paint in place instead of waiting for a page reload. Same
	// leader-only/non-nil rule as Up; built from in-memory session state
	// only, so the 5 s tick costs nothing server-side.
	EvStat map[string]string `json:"evstat"`
}

type sseFleet struct {
	Members   int `json:"members"`
	MembersUp int `json:"members_up"`
	Events    int `json:"events"`
	EventsUp  int `json:"events_up"`
}

func (t *TeleBot) sseStateOf(chatID int64, u *model.User) sseState {
	st := sseState{
		Toggles: t.webTogglesOf(chatID),
		Vip:     u.VipLevel,
		GameUID: u.GameUID,
		Server:  u.ServerID,
	}
	if !u.VipExpireAt.IsZero() {
		st.VipExpire = u.VipExpireAt.In(ictZone).Format("15:04 02/01/2006")
	}
	up := map[string]bool{}
	if v, ok := t.session.Load(chatID); ok {
		st.Running = sessionRunning(v)
	}
	if u.IsLeader {
		f := &sseFleet{}
		db := t.ydb.GetDB()
		leaderID := fmt.Sprintf("%d", chatID)
		if ms, err := model.GetMembersByLeader(db, leaderID); err == nil {
			f.Members = len(ms)
			for _, m := range ms {
				if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
					if v, ok := t.session.Load(id); ok && sessionRunning(v) {
						f.MembersUp++
						up[m.ID] = true
					}
				}
			}
		}
		ev := map[string]string{}
		// eventFleetOf = own event_users + delegated event accounts, so the
		// delegate's Sự kiện row pills and island-status lines also repaint
		// live (their account id keys both maps, exactly like a real event
		// account — a delegated row is NOT in the Đệ tử up map).
		if fleet, err := t.eventFleetOf(leaderID); err == nil {
			f.Events = len(fleet)
			for _, fr := range fleet {
				if id, err := strconv.ParseInt(fr.Ev.ID, 10, 64); err == nil {
					if v, ok := t.session.Load(id); ok && sessionRunning(v) {
						f.EventsUp++
						up[fr.Ev.ID] = true
					}
				}
				ev[fr.Ev.ID] = t.eventIslandLine(fr.Ev)
			}
		}
		st.Fleet = f
		st.Up = up     // always non-nil for a leader: an empty map must repaint rows off
		st.EvStat = ev // same rule for the island status lines
	}
	return st
}

// has reports whether chatID has at least one live browser stream.
func (h *sseHub) has(chatID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.byChat[chatID]) > 0
}

// pushWebState recomputes the state frame and fans it out to chatID's browser
// the instant server-side state changes (login success, logout/kick, toggles,
// VIP redeem) — the browser must never wait for the 5 s tick to learn that
// something it just did took effect. accountID may be the viewer itself, a
// clone member, or an event account; the frame goes to the OWNER chat (the
// leader for member/event accounts), because that is whose browser is
// watching. No-op (cheap) when nobody has a stream open.
func (t *TeleBot) pushWebState(accountID int64) {
	db := t.ydb.GetDB()
	key := strconv.FormatInt(accountID, 10)
	viewer := accountID
	if u, err := model.GetUser(db, key); err == nil && u.LeaderID != "" {
		if id, err := strconv.ParseInt(u.LeaderID, 10, 64); err == nil {
			viewer = id
		}
	} else if eu, err := model.GetEventUser(db, key); err == nil && eu.LeaderID != "" {
		if id, err := strconv.ParseInt(eu.LeaderID, 10, 64); err == nil {
			viewer = id
		}
	}
	if !webSSEHub.has(viewer) {
		return
	}
	cu, err := model.GetUser(db, strconv.FormatInt(viewer, 10))
	if err != nil {
		return
	}
	webSSEHub.push(viewer, "state", t.sseStateOf(viewer, cu))
}

const (
	sseStateTick     = 5 * time.Second
	sseHeartbeat     = 25 * time.Second
	sseDialRetry     = 3 * time.Second
	sseWriteDeadline = 10 * time.Second
)

// webSSE serves GET /sse. Reached through authed(), so the visitor already
// holds a live session and passed webGate once; the 5 s tick re-runs webGate
// and cuts the stream with an expire frame if that ever flips (VIP lapse,
// ban, deletion).
func (t *TeleBot) webSSE(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "trình duyệt không hỗ trợ kết nối trực tiếp", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Ask intermediate proxies not to buffer the stream.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	c := webSSEHub.subscribe(chatID)
	defer webSSEHub.unsubscribe(c)

	ctx := r.Context()
	send := func(event, data string) bool {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(sseWriteDeadline))
		if _, err := fmt.Fprintf(w, "retry: %d\nevent: %s\ndata: %s\n\n", int(sseDialRetry/time.Millisecond), event, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	ping := func() bool {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(sseWriteDeadline))
		if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !t.sendState(w, flusher, chatID, u, send) {
		return
	}
	stateTick := time.NewTicker(sseStateTick)
	defer stateTick.Stop()
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case m := <-c.ch:
			if !send(m.event, m.data) {
				return
			}
		case <-stateTick.C:
			cu, _ := t.webGate(chatID)
			if cu == nil {
				send("expire", `{"reason":"gate"}`)
				return
			}
			if !t.sendState(w, flusher, chatID, cu, send) {
				return
			}
		case <-heartbeat.C:
			if !ping() {
				return
			}
		}
	}
}

func (t *TeleBot) sendState(w http.ResponseWriter, flusher http.Flusher, chatID int64, u *model.User, send func(string, string) bool) bool {
	b, err := json.Marshal(t.sseStateOf(chatID, u))
	if err != nil {
		log.Printf("[web] sse state %d: %v", chatID, err)
		return true // a bad snapshot must not kill the stream
	}
	return send("state", string(b))
}
