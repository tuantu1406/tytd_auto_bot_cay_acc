package tytd_go

// DebugWeb is a standalone, env-GATED web dashboard for map debugging with
// NO Telegram at all: no bot token is ever used, no getUpdates poll (safe to
// run next to the live bot from a COPY of its data dir). It exists because
// map bugs on the leader page are otherwise only visible after a full
// Telegram login round-trip on a phone (user ask 2026-10-05: "create a debug
// webmap version bypass telegram auth to test by yourself").
//
//	./debugweb  from the directory holding ./data.dat, with:
//	  TYTD_DEBUG_CHAT=787613537                  REQUIRED — every request is this chat
//	  TYTD_DEBUG_LOGIN="user|pass|710564"        optional — start a real game session now
//	  WEB_PORT=8091                              listen knob (same rules as the bot)
//
// Safety properties: with TYTD_DEBUG_CHAT unset nothing works (authed pages
// behave 100% normally, and DebugWeb itself refuses to start); the bypass
// lives in webEnabledGate/authed ONLY while the env var is set. POST pages
// still work via the CSRF token embedded in the page (the session carries an
// empty csrf, so debug POSTs need the form button, not a crafted curl).
// Writes it makes to the local DB copy: the user row (leader/VIP far-future
// expiry) and the login attempt bookkeeping — that is why it runs against a
// copy of data.dat, not the production one.

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"tytd_go/internal/db"
	"tytd_go/internal/model"
)

// debugChatID parses TYTD_DEBUG_CHAT; (0,false) when unset/invalid — the
// single switch all web-auth bypasses check.
func debugChatID() (int64, bool) {
	s := strings.TrimSpace(os.Getenv("TYTD_DEBUG_CHAT"))
	if s == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

func DebugWeb() {
	chatID, ok := debugChatID()
	if !ok {
		log.Fatal("[debugweb] TYTD_DEBUG_CHAT is required — refusing to serve an anonymous debug web")
	}
	startPprofListener()
	t := &TeleBot{}
	t.session = sync.Map{}
	t.notiChan = make(chan NotifyMGS, 1024)
	t.autoStartChan = make(chan uint64, 1024)
	var err error
	t.ydb, err = db.NewDB("./data.dat", getDK())
	if err != nil {
		log.Fatalf("[debugweb] db: %v", err)
	}
	d := t.ydb.GetDB()
	id := fmt.Sprintf("%d", chatID)
	u, err := model.GetUser(d, id)
	if err != nil {
		u = &model.User{ID: id}
	}
	u.IsLeader = true
	// webGate demands VipLevel > 0 too — without this a FRESH data.dat (the
	// normal debugweb state) 403s every authed page despite the far-future
	// expiry (level 1 = the same seed a brand-new /start user gets).
	u.VipLevel = 1
	u.VipExpireAt = time.Now().AddDate(1, 0, 0)
	u.LastLoginFail = time.Time{}
	if l := strings.TrimSpace(os.Getenv("TYTD_DEBUG_LOGIN")); l != "" {
		parts := strings.Split(l, "|")
		if len(parts) != 3 {
			log.Fatal(`[debugweb] TYTD_DEBUG_LOGIN="user|pass|serverID"`)
		}
		sid, _ := strconv.Atoi(parts[2])
		u.Username, u.Password, u.ServerID = parts[0], parts[1], sid
	}
	if u.Username == "" || u.Password == "" {
		log.Fatal("[debugweb] no game credentials on the row — set TYTD_DEBUG_LOGIN")
	}
	model.CreateUser(d, u)
	if !t.startPlayerLogin(chatID, func(text string) { log.Printf("[debugweb notify] %s", text) }) {
		log.Println("[debugweb] game login did not start — pages will honestly show idle")
	}
	log.Printf("[debugweb] serving chat %d with NO telegram auth on %s (local DB copy: %s)",
		chatID, webBind(), "./data.dat")
	t.StartWebServer()
}

// webDebugActs is a debugweb-only diagnostic (registered only under
// TYTD_DEBUG_CHAT): GET /debug/acts dumps the raw activityStore of every
// live session in the debug chat — the ground truth behind gates like
// bacminhWarOpen, which return "closed" both when the row is missing AND
// when its phase says otherwise (2026-11-03 Bắc Minh "event IS open" check).
func (t *TeleBot) webDebugActs(w http.ResponseWriter, r *http.Request) {
	chatID, ok := debugChatID()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "chat %d  now=%d\n", chatID, time.Now().UnixMilli())
	dump := func(who string, actList *activityStore, lastOf map[int32]int32) {
		fmt.Fprintf(w, "\n== %s ==\n", who)
		for _, e := range actList.snapshot() {
			fmt.Fprintf(w, "  id=%d type=%d child=%d status=%s begin=%s end=%s show=[%s,%s]\n",
				e.ActivityID, e.Type, e.ChildType, e.status(time.Now()),
				time.UnixMilli(e.BeginTime).In(ictZone).Format("01-02 15:04:05"),
				time.UnixMilli(e.EndTime).In(ictZone).Format("01-02 15:04:05"),
				time.UnixMilli(e.BeginShowTime).In(ictZone).Format("01-02 15:04"),
				time.UnixMilli(e.EndShowTime).In(ictZone).Format("01-02 15:04"))
		}
		if lastOf != nil {
			fmt.Fprintf(w, "  lastOf: %v\n", lastOf)
		}
	}
	if v, ok := t.session.Load(chatID); ok {
		if p, ok := v.(*Player); ok {
			fmt.Fprintf(w, "leader running=%v user=%s\n", p.Running, p.Username)
			p.ActList.mu.Lock()
			lo := map[int32]int32{}
			for k, v := range p.ActList.lastOf {
				lo[k] = v
			}
			p.ActList.mu.Unlock()
			dump("leader ActList", &p.ActList, lo)
		}
	}
	t.session.Range(func(k, v interface{}) bool {
		if ep, ok := v.(*EventPlayer); ok && ep.ChatID == chatID {
			fmt.Fprintf(w, "event acc id=%v running=%v user=%s\n", k, ep.Running, ep.Username)
			ep.ActList.mu.Lock()
			lo := map[int32]int32{}
			for kk, vv := range ep.ActList.lastOf {
				lo[kk] = vv
			}
			ep.ActList.mu.Unlock()
			dump("event ActList", &ep.ActList, lo)
			return true
		}
		if p, ok := v.(*Player); ok && k != chatID {
			// clone members share the leader's chat; show their type rows too
			for _, e := range p.ActList.snapshot() {
				if e.Type == 332 || e.Type == 199 {
					fmt.Fprintf(w, "member id=%v user=%s: id=%d type=%d status=%s\n",
						k, p.Username, e.ActivityID, e.Type, e.status(time.Now()))
				}
			}
		}
		return true
	})
}
