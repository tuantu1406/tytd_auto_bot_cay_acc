package tytd_go

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Web UI: a server rendered dashboard inside the bot binary. Identity comes
// from Telegram only — the Mini App initData auto-login at / (the default
// route, so BotFather needs nothing but the domain; /miniapp is an alias) or
// the Login Widget at /auth/callback for plain browsers (see web_auth.go). A web
// session is just a handle onto the same ChatID the chat handlers use — every action
// below is a thin adapter over the very same primitives the Telegram handlers
// call (startPlayerLogin, model.*, GodIsland*), never a forked re-
// implementation. The server always listens but serves 503 until the admin
// turns it on with /web, and it must sit behind a TLS proxy: the Login Widget
// only works on an HTTPS domain registered with @BotFather.
//
// UI text is game-language Vietnamese and must never mention backend command
// names or protocol details (standing rule).

//go:embed webtmpl/*.html
var webTemplateFS embed.FS

// appkitJS is the shared mini-app base (camera/clock/heartbeat/orders) served
// at /webstatic/appkit.js; it ships with the binary, so the page-side cache
// TTL stays short and content changes are tied to a deploy.
//
//go:embed webtmpl/appkit.js
var appkitJS []byte

const (
	webCookieName  = "tytd_wsess"
	webSessionTTL  = 12 * time.Hour
	webDefaultBind = ":80"
	webMapTimeout  = 15 * time.Second // GodIslandMiniMap request timeout (same as /bonglai_map)
)

// ---- in-memory session store (bot restart = everyone re-logins) ----------

type webSession struct {
	chatID  int64
	csrf    string
	expires time.Time
}

type webSessionStore struct {
	mu sync.Mutex
	m  map[string]*webSession
}

var webSessions = &webSessionStore{m: map[string]*webSession{}}

func webRandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func (s *webSessionStore) create(chatID int64) (token string, sess *webSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.m { // opportunistic sweep
		if now.After(v.expires) {
			delete(s.m, k)
		}
	}
	token, sess = webRandomHex(32), &webSession{chatID: chatID, csrf: webRandomHex(16), expires: now.Add(webSessionTTL)}
	s.m[token] = sess
	return
}

func (s *webSessionStore) get(token string) (*webSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[token]
	if !ok || time.Now().After(v.expires) {
		delete(s.m, token)
		return nil, false
	}
	return v, true
}

func (s *webSessionStore) del(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, token)
}

// ---- per-IP rate limit for the login endpoints ----------------------------

type webLimiter struct {
	mu        sync.Mutex
	hits      map[string][]time.Time
	limit     int
	window    time.Duration
	lastSweep time.Time
}

func newWebLimiter(limit int, window time.Duration) *webLimiter {
	return &webLimiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

func (l *webLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	// Keys of never-revisited IPs would otherwise survive forever (audit
	// finding #6); one linear sweep per window keeps the map bounded.
	if now.Sub(l.lastSweep) > l.window {
		for k, ts := range l.hits {
			var live []time.Time
			for _, t := range ts {
				if now.Sub(t) < l.window {
					live = append(live, t)
				}
			}
			if len(live) == 0 {
				delete(l.hits, k)
			} else {
				l.hits[k] = live
			}
		}
		l.lastSweep = now
	}
	var kept []time.Time
	for _, t := range l.hits[ip] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[ip] = kept
		return false
	}
	l.hits[ip] = append(kept, now)
	return true
}

// ---- config accessors ------------------------------------------------------

func (t *TeleBot) webCfg(key string) string {
	c, err := model.GetConfig(t.ydb.GetDB(), key)
	if err != nil || c == nil {
		return ""
	}
	return c.Value
}

func (t *TeleBot) webEnabled() bool {
	v, err := strconv.ParseBool(t.webCfg(model.ConfigKeyWebEnabled))
	return err == nil && v
}

// ---- server ----------------------------------------------------------------

// webBind resolves the listen address: WEB_PORT (the deployment knob — a
// bare port like 8081, or host:port) → webDefaultBind on :80 (the servers
// run as root, so the dashboard is served on the plain web port). The older
// TYTD_WEB_BIND still wins, but it is a hidden local-testing override, not a
// user-facing knob. Anything malformed falls back to the default so a typo
// can never take the dashboard down on restart.
func webBind() string {
	if s := strings.TrimSpace(os.Getenv("TYTD_WEB_BIND")); s != "" {
		if _, _, err := net.SplitHostPort(s); err == nil {
			return s
		}
	}
	if s := strings.TrimSpace(os.Getenv("WEB_PORT")); s != "" {
		if h, p, err := net.SplitHostPort(s); err == nil { // allow "0.0.0.0:9000"
			if n, e := strconv.Atoi(p); e == nil && n >= 1 && n <= 65535 {
				return h + ":" + strconv.Itoa(n)
			}
		} else if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 65535 {
			return ":" + strconv.Itoa(n)
		}
		log.Printf("[web] invalid WEB_PORT=%q — falling back to %s", s, webDefaultBind)
	}
	return webDefaultBind
}

// StartWebServer serves the dashboard. It runs for the process lifetime;
// WEB_PORT moves the listen port, and /web status always shows the result.
func (t *TeleBot) StartWebServer() {
	bind := webBind()
	srv := &http.Server{Addr: bind, Handler: t.webRoutes(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("[web] listening on %s (503 until /web on)", bind)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("[web] server error: %v", err)
	}
}

func (t *TeleBot) webRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", t.webEnabledGate(t.webLoginPage))
	mux.HandleFunc("/miniapp", t.webEnabledGate(t.webMiniAppEntry))
	mux.HandleFunc("/miniapp/login", t.webEnabledGate(t.webMiniAppLogin))
	mux.HandleFunc("/auth/callback", t.webEnabledGate(t.webLoginCallback))
	mux.HandleFunc("/logout", t.webEnabledGate(t.authed(t.webLogout)))
	mux.HandleFunc("/sse", t.webEnabledGate(t.authed(t.webSSE)))
	if _, dbg := debugChatID(); dbg { // debugweb-only diagnostic
		mux.HandleFunc("/debug/acts", t.webDebugActs)
	}
	mux.HandleFunc("/", t.webEnabledGate(t.webRoot))
	mux.HandleFunc("/account", t.webEnabledGate(t.authed(t.webAccount)))
	mux.HandleFunc("/settings", t.webEnabledGate(t.authed(t.webSettings)))
	mux.HandleFunc("/schedules", t.webEnabledGate(t.authed(t.webSchedules)))
	mux.HandleFunc("/redeem", t.webEnabledGate(t.authed(t.webRedeem)))
	mux.HandleFunc("/toggle", t.webEnabledGate(t.authed(t.webToggle)))
	mux.HandleFunc("/members", t.webEnabledGate(t.authedLeader(t.webMembers)))
	mux.HandleFunc("/events", t.webEnabledGate(t.authedLeader(t.webEvents)))
	mux.HandleFunc("/events/order", t.webEnabledGate(t.authedLeader(t.webEventsOrder)))
	mux.HandleFunc("/events/map", t.webEnabledGate(t.authedLeader(t.webEventsMap)))
	mux.HandleFunc("/events/mapart", t.webEnabledGate(t.authedLeader(t.webEventMapArt)))
	mux.HandleFunc("/events/mapcity", t.webEnabledGate(t.authedLeader(t.webEventMapCity)))
	mux.HandleFunc("/events/mapsub", t.webEnabledGate(t.authedLeader(t.webEventMapSub)))
	mux.HandleFunc("/events/mapview", t.webEnabledGate(t.authedLeader(t.webKunlunMapView)))
	mux.HandleFunc("/events/mapstatic", t.webEnabledGate(t.authedLeader(t.webEventMapStatic)))
	mux.HandleFunc("/kunlun", t.webEnabledGate(t.authedLeader(t.webKunlun)))
	mux.HandleFunc("/luanhoi", t.webEnabledGate(t.authedLeader(t.webMiniApp("luanhoi", nil))))
	mux.HandleFunc("/seawarwar", t.webEnabledGate(t.authedLeader(t.webMiniApp("seawarwar", nil))))
	mux.HandleFunc("/bacminh", t.webEnabledGate(t.authedLeader(t.webBacMinh)))
	// Shared mini-app base script — one exact fixed path (no user-controlled
	// filename ever reaches the embed), short TTL so a deploy can rotate it.
	mux.HandleFunc("/webstatic/appkit.js", t.webEnabledGate(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(appkitJS)
	}))
	mux.HandleFunc("/thuongdo", t.webEnabledGate(t.authedLeader(t.webMiniApp("thuongdo", nil))))
	mux.HandleFunc("/ditich", t.webEnabledGate(t.authedLeader(t.webMiniApp("ditich", nil))))
	mux.HandleFunc("/cuuu", t.webEnabledGate(t.authedLeader(t.webMiniApp("cuuu", nil))))
	// One template, two boards: the page reads its variant from the data.
	mux.HandleFunc("/bonglai", t.webEnabledGate(t.authedLeader(t.webMiniApp("godisland", map[string]string{"which": "bonglai"}))))
	mux.HandleFunc("/thienma", t.webEnabledGate(t.authedLeader(t.webMiniApp("godisland", map[string]string{"which": "thienma"}))))
	return mux
}

// appURL is the absolute Mini App link for one board page ("/<path>"), or ""
// when web is off / no domain is registered yet (then no button is drawn —
// a relative URL would open the game website inside Telegram).
func (t *TeleBot) appURL(path string) string {
	if !t.webEnabled() {
		return ""
	}
	d := strings.TrimSpace(t.webCfg(model.ConfigKeyWebDomain))
	if d == "" {
		return ""
	}
	return "https://" + d + "/" + path
}

// kunlunAppURL is the absolute Mini App link for the Côn Lôn board.
func (t *TeleBot) kunlunAppURL() string { return t.appURL("kunlun") }

// bacminhAppURL is the absolute Mini App link for the Đỉnh Bắc Minh board
// (separate page, same engine).
func (t *TeleBot) bacminhAppURL() string { return t.appURL("bacminh") }

// webMiniApp builds the page handler for a board mini app. The HTML is never
// cached (one stale line against newly embedded art = broken board); every
// asset the page fetches afterwards is served immutable by mapart.
func (t *TeleBot) webMiniApp(page string, data any) func(http.ResponseWriter, *http.Request, int64, *model.User) {
	return func(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
		w.Header().Set("Cache-Control", "no-store")
		t.webRender(w, r, page, chatID, u, webCSRFOf(r), data)
	}
}

// webKunlun serves the Côn Lôn mini app page. The page itself is never
// cached (one stale HTML line against new embedded art = broken board);
// everything it loads afterwards is served immutable by the mapart ku arm.
func (t *TeleBot) webKunlun(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	w.Header().Set("Cache-Control", "no-store")
	t.webRender(w, r, "kunlun", chatID, u, webCSRFOf(r), nil)
}

// webBacMinh serves the Đỉnh Bắc Minh mini app page (same no-cache rule,
// mapart bm arm serves its assets).
func (t *TeleBot) webBacMinh(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	w.Header().Set("Cache-Control", "no-store")
	t.webRender(w, r, "bacminh", chatID, u, webCSRFOf(r), nil)
}

// webRoot is the default route, so BotFather only needs the bare domain:
// a valid cookie goes to the dashboard, anything else runs the Mini App
// auto-login dance — verify initData when a client attached it to the query,
// otherwise hand off to the /login page whose JS bridge picks the identity
// up from Telegram.WebApp and redirects back here.
func (t *TeleBot) webRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if s, _ := t.webSessionFrom(r); s != nil {
		t.authed(t.webDashboard)(w, r)
		return
	}
	t.webMiniAppEntry(w, r)
}

func (t *TeleBot) webEnabledGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, dbg := debugChatID(); dbg {
			next(w, r) // debugweb: TYTD_DEBUG_CHAT set — webEnabled is forced on
			return
		}
		if !t.webEnabled() {
			http.Error(w, "Giao diện web chưa được bật.", http.StatusServiceUnavailable)
			return
		}
		next(w, r)
	}
}

// ---- handler plumbing ------------------------------------------------------

// webAuthedHandler is a page handler that already has its visitor identified
// and gated (same checks as t.auth(): ban → account exists → VIP).
type webAuthedHandler func(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User)

func (t *TeleBot) webSessionFrom(r *http.Request) (*webSession, string) {
	c, err := r.Cookie(webCookieName)
	if err != nil || c.Value == "" {
		return nil, ""
	}
	s, _ := webSessions.get(c.Value)
	return s, c.Value
}

// webClientIP is the rate-limit key. Behind the required TLS proxy (which
// webSecure already trusts via X-Forwarded-Proto), RemoteAddr is always the
// proxy's address, so every real user would share one bucket — prefer the
// left-most X-Forwarded-For entry the proxy appended.
func webClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func (t *TeleBot) webSecure(r *http.Request) bool {
	return r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
}

// webViewerKey stashes the authenticated (chatID, *model.User) pair on the
// request in authed, so webRedirectMsg can rebuild the live state frame for a
// no-reload submit without threading those through every handler signature.
type webViewerKey struct{}

// webWantsJSON reports whether layout.html's no-reload interceptor submitted
// this form. Plain posts keep the classic POST→redirect→reload flow, so the
// whole page still works in a browser without JS.
func webWantsJSON(r *http.Request) bool { return r.Header.Get("X-Tytd-JSON") == "1" }

// webRedirectMsg ends a write. Headerless posts redirect (browser reloads the
// page and webFlash renders the message). A live post instead gets the same
// state frame the SSE stream sends plus the {ok,msg} the toast needs — so the
// page repaints from server truth WITHOUT navigating. Every write already
// routes through here, so this single branch makes them all live-capable.
func (t *TeleBot) webRedirectMsg(w http.ResponseWriter, r *http.Request, path, msg string) {
	if webWantsJSON(r) {
		if v := r.Context().Value(webViewerKey{}); v != nil {
			view := v.(*webViewModel)
			kind := "ok"
			text := msg
			if strings.HasPrefix(msg, "!!") {
				kind, text = "err", strings.TrimPrefix(msg, "!!")
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"ok": kind == "ok", "msg": text, "state": t.sseStateOf(view.ChatID, view.User),
			}); err != nil {
				log.Printf("[web] live json: %v", err)
			}
			return
		}
	}
	if msg != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path = path + sep + "m=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func (t *TeleBot) authed(h webAuthedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// debugweb bypass (web_debug.go): only while TYTD_DEBUG_CHAT is set.
		if chatID, dbg := debugChatID(); dbg {
			if u, msg := t.webGate(chatID); u != nil {
				r = r.WithContext(context.WithValue(r.Context(), webViewerKey{},
					&webViewModel{ChatID: chatID, User: u}))
				h(w, r, chatID, u)
				return
			} else {
				http.Error(w, msg, http.StatusForbidden)
				return
			}
		}
		s, cookie := t.webSessionFrom(r)
		if s == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost {
			if r.FormValue("csrf") != s.csrf {
				http.Error(w, "Yêu cầu bị lỗi (chống CSRF), hãy tải lại trang.", http.StatusBadRequest)
				return
			}
		}
		u, msg := t.webGate(s.chatID)
		if u == nil {
			webSessions.del(cookie)
			t.webClearCookie(w, r)
			t.webRedirectMsg(w, r, "/login", msg)
			return
		}
		// Carry the resolved viewer for webRedirectMsg's live branch.
		r = r.WithContext(context.WithValue(r.Context(), webViewerKey{},
			&webViewModel{ChatID: s.chatID, User: u}))
		h(w, r, s.chatID, u)
	}
}

func (t *TeleBot) authedLeader(h webAuthedHandler) http.HandlerFunc {
	return t.authed(func(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
		if !u.IsLeader {
			t.webRedirectMsg(w, r, "/", "Chỉ trưởng bang mới có trang này.")
			return
		}
		h(w, r, chatID, u)
	})
}

// webGate is the web port of t.auth(): banned → account exists → VIP
// unexpired (with the same zero-out side effect). It returns the account row
// or a Vietnamese message to show on /login.
func (t *TeleBot) webGate(chatID int64) (*model.User, string) {
	if model.IsBanned(t.ydb.GetDB(), chatID) {
		return nil, "Tài khoản của bạn bị cấm dùng bot. Vào group hỗ trợ để hỏi."
	}
	id := fmt.Sprintf("%d", chatID)
	if !model.UserExists(t.ydb.GetDB(), id) {
		return nil, "Tài khoản chưa được kích hoạt — hãy mở bot Telegram và nhấn /start trước."
	}
	u, err := model.GetUser(t.ydb.GetDB(), id)
	if err != nil {
		log.Printf("[web] gate get user %d: %v", chatID, err)
		return nil, "Lỗi server, thử lại sau."
	}
	if u.VipLevel > 0 && !u.VipExpireAt.Before(time.Now()) {
		return u, ""
	}
	if u.VipLevel > 0 {
		u.VipLevel = 0
		u.VipExpireAt = time.Now()
		model.CreateUser(t.ydb.GetDB(), u)
	}
	return nil, "Hết hạn VIP rồi đạo hữu — dùng /redeem trong bot Telegram để nhận 30 ngày miễn phí."
}

// ---- templates ---------------------------------------------------------------

var (
	webTmplOnce sync.Once
	webTmplSets map[string]*template.Template
)

var webTemplateFuncs = template.FuncMap{
	"sub": func(a, b int) int { return a - b },
	// schedIn pre-fills a schedule text input: stored CSV shown as
	// "10:00, 18:30", empty stays empty (the input's placeholder then
	// explains that blank means off — "Off" as a value would read like data).
	"schedIn": func(csv string) string {
		if csv == "" {
			return ""
		}
		return formatChuotTimes(csv)
	},
}

// webTmpl parses layout.html + one page file into a private set per page, so
// every page may reuse the {{define "content"}} name.
func webTmpl() map[string]*template.Template {
	webTmplOnce.Do(func() {
		webTmplSets = map[string]*template.Template{}
		pages, _ := webTemplateFS.ReadDir("webtmpl")
		for _, p := range pages {
			name := strings.TrimSuffix(p.Name(), ".html")
			if name == "layout" || name == "login" {
				continue // login is standalone; layout is the shared shell
			}
			tt := template.Must(template.New(name).Funcs(webTemplateFuncs).ParseFS(webTemplateFS,
				"webtmpl/layout.html", "webtmpl/"+p.Name()))
			webTmplSets[name] = tt
		}
	})
	return webTmplSets
}

// webViewModel is what every page template receives.
type webViewModel struct {
	Msg          string
	MsgKind      string // "" | ok | err
	Path         string // request path, so the nav can highlight the active tab
	ChatID       int64
	User         *model.User
	IsLeader     bool
	Running      bool
	OpenLK       bool // Luyện Khí Đại Hội (type 119) open on the live session
	OpenTayDu    bool // Tây Du Phục Ma (type 311) season open on the live session
	OpenThanLong bool // Luyện Khí Thần Long / ComposeBall (type 130) season open
	Toggles      map[string]bool
	Data         any
	CSRF         string
}

func (t *TeleBot) webFlash(r *http.Request) (string, string) {
	m := r.URL.Query().Get("m")
	if m == "" {
		return "", ""
	}
	kind := "ok"
	if strings.HasPrefix(m, "!!") {
		kind, m = "err", strings.TrimPrefix(m, "!!")
	}
	return m, kind
}

func (t *TeleBot) webRender(w http.ResponseWriter, r *http.Request, page string, chatID int64, u *model.User, csrf string, data any) {
	tt, ok := webTmpl()[page]
	if !ok {
		http.Error(w, "trang khong ton tai", http.StatusInternalServerError)
		return
	}
	running := false
	openLK := false
	openTayDu := false
	openThanLong := false
	if v, ok := t.session.Load(chatID); ok {
		running = sessionRunning(v)
		if p, ok := v.(*Player); ok && running {
			// ActList is in-memory only — "open" is knowable exactly when the
			// account is resident, which is also when we could act on it.
			if _, err := p.csActiveID(); err == nil {
				openLK = true
			}
			// Công tắc-card engine rows render only while their activity
			// season is open (owner 2026-11-09: "nút thần long + tây du phải
			// check activity đang chạy mới hiện") — own session only: these
			// buttons act on the viewer's own account.
			now := time.Now()
			openTayDu = webActOpen(p, 311, now)
			openThanLong = webActOpen(p, 130, now)
		}
	}
	msg, kind := t.webFlash(r)
	view := webViewModel{
		Msg: msg, MsgKind: kind, Path: r.URL.Path,
		ChatID: chatID, User: u, IsLeader: u.IsLeader, Running: running, OpenLK: openLK,
		OpenTayDu: openTayDu, OpenThanLong: openThanLong,
		Toggles: t.webTogglesOf(chatID),
		Data:    data, CSRF: csrf,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tt.ExecuteTemplate(w, "layout", view); err != nil {
		log.Printf("[web] render %s: %v", page, err)
	}
}

// webTogglesOf snapshots the runtime toggles of chatID's live session, or all
// false when no session is resident.
func (t *TeleBot) webTogglesOf(chatID int64) map[string]bool {
	m := map[string]bool{
		"chuot": false, "notify": false, "maohiem": false,
		"lucdao": false, "yeuthap": false, "huyetmach": false,
		"peach": false, "xave": false,
		"taydu": false, "thanlong": false,
	}
	v, ok := t.session.Load(chatID)
	if !ok {
		return m
	}
	p, ok := v.(*Player)
	if !ok || !p.Running {
		return m
	}
	m["chuot"], m["notify"], m["maohiem"] = p.ChuotRun, p.Notify, p.Maohiem
	m["lucdao"], m["yeuthap"] = p.Lucdao, p.Yeuthap
	m["huyetmach"] = p.HuyetMach
	m["peach"], m["xave"] = p.Peach, p.XaVe
	// Event engines whose on/off lives only on the Player (never persists a
	// shared game_uid, [[tytd-player-object-gates]]): Tây Du is a loop switch,
	// Thần Long is the one-shot ComposeBall run-in-flight flag.
	m["taydu"] = p.Xiyou
	m["thanlong"] = p.cbIsRunning()
	return m
}

// webCfgEvents maps each event config key (form field name) to its activity
// type; a config row renders only when the key is present in the open map.
var webCfgEvents = []struct {
	key string
	ty  int32
}{
	{"auto_thiendo", 150}, {"auto_thiendo_toc", 150}, {"auto_luanhoi", 172},
	{"auto_tiencanh", 186}, {"auto_thuongminh", 349},
	// Công tắc-card rows for the non-persist engines (owner 2026-11-09:
	// "nút thần long + tây du phải check activity đang chạy mới hiện") —
	// sw_ prefix because these gate switch ROWS, not config checkboxes.
	{"sw_taydu", 311}, {"sw_thanlong", 130},
}

// webActOpen reports whether one resident session's ActList carries the
// SEASON row of activity type ty with status active or reward — the same
// open rule as webOpenEventKeys (season window, not the daily play-hour).
func webActOpen(v any, ty int32, now time.Time) bool {
	var al *activityStore
	switch s := v.(type) {
	case *Player:
		if !sessionRunning(s) {
			return false
		}
		al = &s.ActList
	case *EventPlayer:
		if !sessionRunning(s) {
			return false
		}
		al = &s.ActList
	default:
		return false
	}
	for _, e := range al.byType(ty) {
		if st := e.status(now); st == "active" || st == "reward" {
			return true
		}
	}
	return false
}

// webOpenEventKeys reports which event config rows the viewer may see: an
// event is "open" when ANY resident session of the viewer's fleet (own
// Player + members + event accounts) carries its SEASON row in ActList with
// status active or reward — deliberately NOT the daily play-window, so an
// event that is open today but not yet at fighting hour still shows its
// config (owner 2026-11-09). No session online at all → empty map → every
// event row hides (the bot cannot know what the server announced).
func (t *TeleBot) webOpenEventKeys(chatID int64) map[string]bool {
	open := map[string]bool{}
	now := time.Now()
	seen := 0
	resident := func(v any) bool {
		switch s := v.(type) {
		case *Player:
			return sessionRunning(s)
		case *EventPlayer:
			return sessionRunning(s)
		}
		return false
	}
	check := func(v any) {
		if !resident(v) {
			return
		}
		seen++
		for _, ce := range webCfgEvents {
			if !open[ce.key] && webActOpen(v, ce.ty, now) {
				open[ce.key] = true
			}
		}
	}
	if v, ok := t.session.Load(chatID); ok {
		check(v)
	}
	leaderID := fmt.Sprintf("%d", chatID)
	if ms, err := model.GetMembersByLeader(t.ydb.GetDB(), leaderID); err == nil {
		for _, m := range ms {
			if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
				if v, ok := t.session.Load(id); ok {
					check(v)
				}
			}
		}
	}
	if fleet, err := t.eventFleetOf(leaderID); err == nil {
		for _, fr := range fleet {
			if id, err := strconv.ParseInt(fr.Ev.ID, 10, 64); err == nil {
				if v, ok := t.session.Load(id); ok {
					check(v)
				}
			}
		}
	}
	if seen == 0 {
		return map[string]bool{}
	}
	return open
}

func (t *TeleBot) webClearCookie(w http.ResponseWriter, r *http.Request) {
	// Attributes mirror setWebSessionCookie (None+Secure when HTTPS) so the
	// deletion is guaranteed to hit the cookie the mini app flows minted.
	sameSite := http.SameSiteLaxMode
	secure := t.webSecure(r)
	if secure {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{
		Name: webCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: sameSite, Secure: secure,
	})
}

// webLogout backs POST /logout (GET is inert: logging out must not be
// triggerable by a stray link).
func (t *TeleBot) webLogout(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	if r.Method != http.MethodPost {
		t.webRedirectMsg(w, r, "/", "")
		return
	}
	if s, cookie := t.webSessionFrom(r); s != nil {
		webSessions.del(cookie)
	}
	t.webClearCookie(w, r)
	t.webRedirectMsg(w, r, "/login", "Đã thoát khỏi trang web.")
}

// ---- /web admin command -----------------------------------------------------

// WebConfigHandler is the admin-only switchboard for the web UI:
// /web <on|off> just flips the switch — enough for Mini App auto-login, whose
// identity comes from initData signed with this binary's own token.
// /web <domain> <bot_username> [on|off] additionally records the Login Widget
// fallback for plain browsers (domain must match what @BotFather knows via
// /setdomain; bot_username is what the widget points at). The listener itself
// is always up — the flag alone decides whether it answers.
func (t *TeleBot) WebConfigHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	args := strings.Fields(message.Text)
	send := func(text string) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID, Text: text,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
	}
	if len(args) == 2 && args[1] == "status" {
		bind := webBind()
		// sha is a one-way fingerprint of the running bot token — safe to show,
		// and it lets an admin confirm the binary's token matches the bot the
		// Mini App was registered to (printf %s "$TOKEN" | sha256sum).
		sum := sha256.Sum256([]byte(t.Token))
		send(fmt.Sprintf("web: %s\ndomain: %s\nbot: @%s\nlisten: %s\ntoken: len=%d sha=%s…",
			t.webCfg(model.ConfigKeyWebEnabled),
			orNone(t.webCfg(model.ConfigKeyWebDomain)),
			orNone(t.webCfg(model.ConfigKeyWebBotUsername)),
			bind, len(t.Token), hex.EncodeToString(sum[:])[:8]))
		// Mini app buttons for every board page that exists in this binary.
		var rows [][]models.InlineKeyboardButton
		addApp := func(u, txt string) {
			if u == "" {
				return
			}
			rows = append(rows, []models.InlineKeyboardButton{{Text: txt, WebApp: &models.WebAppInfo{URL: u}}})
		}
		addApp(t.kunlunAppURL(), "🗺️ Côn Lôn trực tiếp")
		addApp(t.bacminhAppURL(), "🧊 Bắc Minh trực tiếp")
		addApp(t.appURL("ditich"), "🏯 Di Tích trực tiếp")
		addApp(t.appURL("cuuu"), "⛩️ Cửu U trực tiếp")
		addApp(t.appURL("bonglai"), "🌴 Bồng Lai trực tiếp")
		addApp(t.appURL("thienma"), "🔥 Thiên Ma trực tiếp")
		addApp(t.appURL("thuongdo"), "🛩️ Thương Đồ trực tiếp")
		if len(rows) > 0 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: message.Chat.ID, Text: "Map dựng bằng sprite game — pan/zoom, lệnh trực tiếp:",
				ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: rows},
			})
		}
		return
	}
	usage := "usage: /web on|off\n   hoặc /web <domain> <bot_username> [on|off]\nVD: /web on\n     /web tytd.example.com tytd_tool_bot on\n     /web status"
	db := t.ydb.GetDB()
	setState := func(enabled bool) {
		if err := model.SetConfig(db, model.ConfigKeyWebEnabled, strconv.FormatBool(enabled)); err != nil {
			send("failed: " + err.Error())
			return
		}
		state := "TẮT"
		if enabled {
			state = "BẬT"
		}
		note := ""
		if enabled && t.webCfg(model.ConfigKeyWebDomain) == "" {
			note = "\n(đăng nhập mini app tự động; nếu muốn cả nút đăng nhập cho trình duyệt thường: /web <domain> <bot>)"
		}
		send("Web " + state + note)
	}
	parseOnOff := func(s string) (bool, bool) {
		switch strings.ToLower(s) {
		case "on":
			return true, true
		case "off":
			return false, true
		}
		v, err := strconv.ParseBool(s)
		return v, err == nil
	}
	// /web on|off — plain switch. Mini App auto-login needs nothing else:
	// initData is signed with this binary's own bot token.
	if len(args) == 2 {
		on, ok := parseOnOff(args[1])
		if !ok {
			send(usage)
			return
		}
		setState(on)
		return
	}
	// /web <domain> <bot_username> [on|off] — record the browser widget
	// fallback; toggle only when on/off is given.
	if len(args) == 3 || len(args) == 4 {
		domain := strings.ToLower(strings.TrimSpace(args[1]))
		botUser := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(args[2]), "@"))
		if !strings.Contains(domain, ".") || botUser == "" {
			send("sai tham số — domain phải có dấu chấm, bot_username không rỗng")
			return
		}
		if err := model.SetConfig(db, model.ConfigKeyWebDomain, domain); err != nil {
			send("failed: " + err.Error())
			return
		}
		if err := model.SetConfig(db, model.ConfigKeyWebBotUsername, botUser); err != nil {
			send("failed: " + err.Error())
			return
		}
		if len(args) == 3 {
			cur := "TẮT"
			if t.webEnabled() {
				cur = "BẬT"
			}
			send("đã ghi domain + bot cho nút đăng nhập trình duyệt. trạng thái web hiện tại: " + cur)
			return
		}
		on, ok := parseOnOff(args[3])
		if !ok {
			send(usage)
			return
		}
		setState(on)
		send(fmt.Sprintf("Widget trình duyệt: https://%s (login bằng @%s)\nNhớ /setdomain trong @BotFather trỏ đúng domain.", domain, botUser))
		return
	}
	send(usage)
}

func orNone(s string) string {
	if s == "" {
		return "(chưa đặt)"
	}
	return s
}
