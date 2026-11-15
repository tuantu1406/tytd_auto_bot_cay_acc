package tytd_go

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/db"
	"tytd_go/internal/model"
)

const testToken = "123456:TESTTOKENtesttoken"

// signLogin mints a widget payload the same way Telegram would, so tests
// exercise verifyTelegramLogin with real HMACs instead of mocks.
func signLogin(t *testing.T, token string, authDate int64, fields map[string]string) map[string]string {
	t.Helper()
	p := map[string]string{"auth_date": strconv.FormatInt(authDate, 10)}
	for k, v := range fields {
		p[k] = v
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + p[k]
	}
	secret := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(lines, "\n")))
	p["hash"] = hex.EncodeToString(mac.Sum(nil))
	return p
}

func TestVerifyTelegramLogin(t *testing.T) {
	now := time.Now().Unix()

	good := signLogin(t, testToken, now, map[string]string{
		"id": "42", "first_name": "Bi", "username": "bito",
	})
	id, err := verifyTelegramLogin(good, testToken)
	if err != nil || id != 42 {
		t.Fatalf("valid payload rejected: id=%d err=%v", id, err)
	}

	tampered := signLogin(t, testToken, now, map[string]string{"id": "42"})
	tampered["id"] = "43" // signed for 42, presented as 43
	if _, err := verifyTelegramLogin(tampered, testToken); err == nil {
		t.Fatal("tampered payload accepted")
	}

	if _, err := verifyTelegramLogin(good, "wrong-token"); err == nil {
		t.Fatal("payload signed for another bot accepted")
	}

	stale := signLogin(t, testToken, now-25*60*60, map[string]string{"id": "42"})
	if _, err := verifyTelegramLogin(stale, testToken); err == nil {
		t.Fatal("stale auth_date accepted")
	}

	fresh := signLogin(t, testToken, now-23*60*60, map[string]string{"id": "42"})
	if _, err := verifyTelegramLogin(fresh, testToken); err != nil {
		t.Fatalf("23h-old payload rejected: %v", err)
	}

	for _, missing := range []string{"id", "auth_date", "hash"} {
		p := signLogin(t, testToken, now, map[string]string{"id": "42"})
		delete(p, missing)
		if _, err := verifyTelegramLogin(p, testToken); err == nil {
			t.Fatalf("payload without %s accepted", missing)
		}
	}
}

// ---- integration harness ---------------------------------------------------

// newTestWebBot builds a TeleBot on a throwaway encrypted DB with the web UI
// fully configured. Callers seed users/config as needed.
func newTestWebBot(t *testing.T) *TeleBot {
	t.Helper()
	store, err := db.NewDB(filepath.Join(t.TempDir(), "data.dat"), getDK())
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := store.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	// Every httptest request shares one client IP, so the process-global
	// login limiter would otherwise accumulate hits across tests.
	webLoginLimiter = newWebLimiter(20, time.Minute)
	bot := &TeleBot{Token: testToken, ydb: store, notiChan: make(chan NotifyMGS, 16), autoStartChan: make(chan uint64, 16)}
	db2 := store.GetDB()
	if err := model.SetConfig(db2, model.ConfigKeyWebEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if err := model.SetConfig(db2, model.ConfigKeyWebDomain, "example.test"); err != nil {
		t.Fatal(err)
	}
	if err := model.SetConfig(db2, model.ConfigKeyWebBotUsername, "testbot"); err != nil {
		t.Fatal(err)
	}
	return bot
}

func seedUser(t *testing.T, bot *TeleBot, id string, vipDays int) *model.User {
	t.Helper()
	u := &model.User{
		ID: id, Username: "u" + id, Password: "pw", ServerID: 100,
		VipLevel: 1, VipExpireAt: time.Now().Add(time.Duration(vipDays) * 24 * time.Hour),
		MinBottleLevel: 2, MinPeachLevel: 4, MinLuuLyLevel: 6,
	}
	if err := model.CreateUser(bot.ydb.GetDB(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

// loginAs performs the full widget callback and returns the session cookie.
func loginAs(t *testing.T, bot *TeleBot, h http.Handler, chatID int64) (cookie, csrf string) {
	t.Helper()
	payload := signLogin(t, testToken, time.Now().Unix(), map[string]string{"id": strconv.FormatInt(chatID, 10)})
	q := url.Values{}
	for k, v := range payload {
		q.Set(k, v)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback code=%d body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/") {
		t.Fatalf("callback did not redirect home: %q", loc)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == webCookieName {
			cookie = ck.Value
		}
	}
	if cookie == "" {
		t.Fatalf("no session cookie, redirected to %q", loc)
	}
	s, ok := webSessions.get(cookie)
	if !ok {
		t.Fatal("session not stored")
	}
	return cookie, s.csrf
}

func doGet(t *testing.T, h http.Handler, path, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: webCookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doPost(t *testing.T, h http.Handler, path, cookie, csrf string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", csrf)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: webCookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWebLoginFlow(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()

	// Unauthenticated page access redirects to /login.
	if rec := doGet(t, h, "/", ""); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous /: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	// Login page renders with the configured bot.
	rec := doGet(t, h, "/login", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "testbot") {
		t.Fatalf("login page: code=%d", rec.Code)
	}

	chatID := int64(1234567890)
	seedUser(t, bot, fmt.Sprintf("%d", chatID), 30)
	cookie, _ := loginAs(t, bot, h, chatID)
	if rec := doGet(t, h, "/", cookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Bảng điều khiển") {
		t.Fatalf("dashboard: code=%d", rec.Code)
	}
}

func TestWebGateMatrix(t *testing.T) {
	// banned → refused
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	model.AddBannedUser(bot.ydb.GetDB(), 1001, "bi", "test")
	seedUser(t, bot, "1001", 30)
	payload := signLogin(t, testToken, time.Now().Unix(), map[string]string{"id": "1001"})
	q := url.Values{}
	for k, v := range payload {
		q.Set(k, v)
	}
	rec := doGet(t, h, "/auth/callback?"+q.Encode(), "")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("banned user not refused: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// nonexistent account → refused
	rec = doGet(t, h, "/auth/callback?"+signQuery(t, map[string]string{"id": "9999999"}), "")
	if !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("unknown account not refused: %q", rec.Header().Get("Location"))
	}

	// expired VIP → refused AND the row is zeroed (same side effect as t.auth)
	bot2 := newTestWebBot(t)
	h2 := bot2.webRoutes()
	seedUser(t, bot2, "2002", -5) // expired
	rec = doGet(t, h2, "/auth/callback?"+signQuery(t, map[string]string{"id": "2002"}), "")
	if !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("expired VIP not refused: %q", rec.Header().Get("Location"))
	}
	if u, _ := model.GetUser(bot2.ydb.GetDB(), "2002"); u.VipLevel != 0 {
		t.Fatalf("expired VIP not zeroed: level=%d", u.VipLevel)
	}
}

// signQuery is loginAs's payload builder for negative paths (no session check).
func signQuery(t *testing.T, fields map[string]string) string {
	t.Helper()
	p := signLogin(t, testToken, time.Now().Unix(), fields)
	q := url.Values{}
	for k, v := range p {
		q.Set(k, v)
	}
	return q.Encode()
}

// signInitData mints a Mini App initData query the same way Telegram's
// webview would: secret = HMAC-SHA256(key="WebAppData", msg=token).
func signInitData(t *testing.T, token string, authDate int64, fields map[string]string) string {
	t.Helper()
	p := map[string]string{"auth_date": strconv.FormatInt(authDate, 10)}
	for k, v := range fields {
		p[k] = v
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + p[k]
	}
	keyMac := hmac.New(sha256.New, []byte("WebAppData"))
	keyMac.Write([]byte(token))
	mac := hmac.New(sha256.New, keyMac.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	p["hash"] = hex.EncodeToString(mac.Sum(nil))
	q := url.Values{}
	for k, v := range p {
		q.Set(k, v)
	}
	return q.Encode()
}

func TestWebMiniAppAutoLogin(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(1500)
	seedUser(t, bot, "1500", 30)
	user := fmt.Sprintf(`{"id":%d,"first_name":"Bi"}`, chatID)

	// Valid initData → cookie + session, straight to the dashboard.
	rec := doGet(t, h, "/miniapp?"+signInitData(t, testToken, time.Now().Unix(), map[string]string{"user": user}), "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("miniapp entry: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var cookie string
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == webCookieName {
			cookie = ck.Value
		}
	}
	if cookie == "" {
		t.Fatal("miniapp entry set no session cookie")
	}
	if _, ok := webSessions.get(cookie); !ok {
		t.Fatal("miniapp session not stored")
	}
	if r := doGet(t, h, "/", cookie); r.Code != http.StatusOK {
		t.Fatalf("dashboard after miniapp login: code=%d", r.Code)
	}

	// Tampered user field in the signed payload → refused at /login.
	good := signInitData(t, testToken, time.Now().Unix(), map[string]string{"user": user})
	tampered := strings.Replace(good, "%22id%22%3A1500", "%22id%22%3A1501", 1)
	if tampered == good {
		t.Skip("encoding changed, nothing to tamper")
	}
	rec = doGet(t, h, "/miniapp?"+tampered, "")
	if !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("tampered initData accepted: %q", rec.Header().Get("Location"))
	}

	// Stale auth_date → refused.
	rec = doGet(t, h, "/miniapp?"+signInitData(t, testToken, time.Now().Unix()-25*3600, map[string]string{"user": user}), "")
	if !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("stale initData accepted: %q", rec.Header().Get("Location"))
	}

	// No initData at all (plain open) → refused.
	rec = doGet(t, h, "/miniapp", "")
	if !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("empty initData accepted: %q", rec.Header().Get("Location"))
	}

	// Returning visitor with a valid cookie rides through without re-auth.
	rec = doGet(t, h, "/miniapp", cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("cookie re-entry: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

// Telegram Web hosts the mini app in a CROSS-SITE iframe: a SameSite=Lax
// session cookie is never stored there, the JS bridge loops
// / → /login → POST /miniapp/login, and the shared login limiter answers
// 429 (owner report 2026-10-25 "mở mini app ở telegram web bị 429"). So an
// HTTPS mini app mint MUST carry SameSite=None+Secure.
func TestWebMiniAppCookieSameSiteNone(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedUser(t, bot, "1550", 30)
	user := `{"id":1550,"first_name":"Bi"}`
	req := httptest.NewRequest(http.MethodGet, "/miniapp?"+signInitData(t, testToken, time.Now().Unix(), map[string]string{"user": user}), nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("entry code=%d", rec.Code)
	}
	var found *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == webCookieName {
			found = ck
		}
	}
	if found == nil {
		t.Fatal("no session cookie minted")
	}
	if !found.Secure || found.SameSite != http.SameSiteNoneMode {
		t.Fatalf("mini app cookie must be None+Secure, got SameSite=%v Secure=%v", found.SameSite, found.Secure)
	}
	// Plain HTTP (local dev) must stay Lax — browsers reject None+!Secure.
	req = httptest.NewRequest(http.MethodGet, "/miniapp?"+signInitData(t, testToken, time.Now().Unix(), map[string]string{"user": user}), nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == webCookieName && (ck.Secure || ck.SameSite != http.SameSiteLaxMode) {
			t.Fatalf("insecure mint must stay Lax: SameSite=%v Secure=%v", ck.SameSite, ck.Secure)
		}
	}
	// A cookie-carried re-entry must NOT consume the login limiter (the
	// reload-happy Telegram Web frame otherwise burns the 429 budget).
	before := webLoginLimiter.hits["192.0.2.1"]
	req = httptest.NewRequest(http.MethodGet, "/miniapp", nil)
	req.AddCookie(&http.Cookie{Name: webCookieName, Value: found.Value})
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusSeeOther || rec2.Header().Get("Location") != "/" {
		t.Fatalf("cookie re-entry: code=%d loc=%q", rec2.Code, rec2.Header().Get("Location"))
	}
	if len(webLoginLimiter.hits["192.0.2.1"]) != len(before) {
		t.Fatal("cookie re-entry consumed limiter budget")
	}
}

// Signed Mini Apps carry a `signature` field that IS part of the hash's
// data-check string — live-verified: Telegram's own hash matched the dcs
// with signature included and not without it.
func TestVerifyTelegramInitDataSignedApp(t *testing.T) {
	p := map[string]string{
		"auth_date":     strconv.FormatInt(time.Now().Unix(), 10),
		"chat_instance": "6217283511978802998",
		"chat_type":     "sender",
		"signature":     "U0jrd4J_s3JLdtMI1EHp1b9zXZB9W4rTixCsSWq9xiAUAnbn5VZhci-E0PBVohpSEdb2eo3N4uuP40-w9CmDAA",
		"user":          `{"id":42,"first_name":"Taylor Squirt","username":"ZzzKARMAzzZ"}`,
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + p[k]
	}
	keyMac := hmac.New(sha256.New, []byte("WebAppData"))
	keyMac.Write([]byte(testToken))
	mac := hmac.New(sha256.New, keyMac.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	p["hash"] = hex.EncodeToString(mac.Sum(nil))

	id, err := verifyTelegramInitData(p, testToken)
	if err != nil || id != 42 {
		t.Fatalf("signed initData rejected: id=%d err=%v", id, err)
	}
}

func TestWebRootAutoLogin(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedUser(t, bot, "1700", 30)
	user := `{"id":1700,"first_name":"Bi"}`

	// Anonymous plain browser at the bare domain → login page.
	if rec := doGet(t, h, "/", ""); !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("anonymous root: loc=%q", rec.Header().Get("Location"))
	}

	// Query-carried initData at the root (clients that do attach it) →
	// cookie, then the dashboard on the follow-up request.
	rec := doGet(t, h, "/?"+signInitData(t, testToken, time.Now().Unix(), map[string]string{"user": user}), "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("root miniapp login: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var cookie string
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == webCookieName {
			cookie = ck.Value
		}
	}
	if cookie == "" {
		t.Fatal("root miniapp login set no cookie")
	}
	if r := doGet(t, h, "/", cookie); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "Bảng điều khiển") {
		t.Fatalf("root dashboard: code=%d", r.Code)
	}

	// Unknown paths no longer silently render the dashboard.
	if rec := doGet(t, h, "/nope", cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path: code=%d", rec.Code)
	}
}

func TestWebMiniAppJSLogin(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedUser(t, bot, "1600", 30)
	seedUser(t, bot, "1601", -5) // expired VIP
	user := `{"id":1600,"first_name":"Bi"}`

	// The JS bridge: POST the initData string exactly as telegram-web-app.js
	// exposes it → 200 + session cookie.
	good := signInitData(t, testToken, time.Now().Unix(), map[string]string{"user": user})
	form := url.Values{"init_data": {good}}
	rec := doPostNoCSRF(t, h, "/miniapp/login", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("js login: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var cookie string
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == webCookieName {
			cookie = ck.Value
		}
	}
	if cookie == "" {
		t.Fatal("js login set no session cookie")
	}
	if _, ok := webSessions.get(cookie); !ok {
		t.Fatal("js login session not stored")
	}
	if r := doGet(t, h, "/", cookie); r.Code != http.StatusOK {
		t.Fatalf("dashboard after js login: code=%d", r.Code)
	}

	// GET is refused; garbage, foreign signature, and expired-VIP users are
	// refused without a cookie.
	if rec := doGet(t, h, "/miniapp/login", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /miniapp/login: code=%d", rec.Code)
	}
	for name, body := range map[string]url.Values{
		"empty":      {"init_data": {""}},
		"garbage":    {"init_data": {"not=a=query&&&"}},
		"wrongkey":   {"init_data": {strings.Replace(good, "hash=", "hash=deadbeef", 1)}},
		"expiredvip": {"init_data": {signInitData(t, testToken, time.Now().Unix(), map[string]string{"user": `{"id":1601}`})}},
	} {
		rec := doPostNoCSRF(t, h, "/miniapp/login", body)
		if rec.Code == http.StatusOK {
			t.Fatalf("%s accepted", name)
		}
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == webCookieName && ck.Value != "" {
				t.Fatalf("%s set a cookie", name)
			}
		}
	}
}

// doPostNoCSRF posts a pre-session form (no cookie yet, so the authed CSRF
// check does not apply).
func doPostNoCSRF(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWebDisabledServes503(t *testing.T) {
	bot := newTestWebBot(t)
	model.SetConfig(bot.ydb.GetDB(), model.ConfigKeyWebEnabled, "false")
	h := bot.webRoutes()
	for _, path := range []string{"/", "/login", "/account"} {
		if rec := doGet(t, h, path, ""); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s while disabled: code=%d", path, rec.Code)
		}
	}
}

func TestWebCSRFAndLogout(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(5150)
	seedUser(t, bot, "5150", 30)
	cookie, csrf := loginAs(t, bot, h, chatID)

	// POST without a valid CSRF token is rejected outright.
	req := httptest.NewRequest(http.MethodPost, "/toggle", strings.NewReader("feat=chuot_on"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: webCookieName, Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("CSRF-less POST: code=%d", rec.Code)
	}

	// Valid POST runs.
	bot.session.Store(chatID, &Player{Running: true})
	if rec := doPost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {"chuot_on"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("good POST: code=%d", rec.Code)
	}

	// GET /logout must not terminate the session; POST does.
	if rec := doGet(t, h, "/logout", cookie); rec.Code == http.StatusOK {
		t.Fatal("GET /logout logged out")
	}
	_ = doPost(t, h, "/logout", cookie, csrf, nil)
	if _, ok := webSessions.get(cookie); ok {
		t.Fatal("POST /logout kept the session")
	}
}

func TestWebRateLimit(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	defer func() { webLoginLimiter = newWebLimiter(20, time.Minute) }()
	webLoginLimiter = newWebLimiter(2, time.Minute)
	for i := 0; i < 2; i++ {
		if rec := doGet(t, h, "/auth/callback?id=1", ""); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("limited too early (i=%d)", i)
		}
	}
	if rec := doGet(t, h, "/auth/callback?id=1", ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("limiter did not kick in: code=%d", rec.Code)
	}
}

func TestWebBind(t *testing.T) {
	for _, tc := range []struct{ webPort, testBind, want string }{
		{"", "", ":80"},       // nobody configured anything → default
		{"9999", "", ":9999"}, // the user-facing knob
		{"0.0.0.0:9000", "", "0.0.0.0:9000"},
		{"abc", "", ":80"}, // typo → default, never a broken listener
		{"0", "", ":80"},   // port out of range → default
		{"70000", "", ":80"},
		{"", "127.0.0.1:7777", "127.0.0.1:7777"}, // local-testing override still wins
		{"9999", "127.0.0.1:7777", "127.0.0.1:7777"},
	} {
		t.Setenv("WEB_PORT", tc.webPort)
		t.Setenv("TYTD_WEB_BIND", tc.testBind)
		if got := webBind(); got != tc.want {
			t.Fatalf("WEB_PORT=%q TYTD_WEB_BIND=%q → %q, want %q", tc.webPort, tc.testBind, got, tc.want)
		}
	}
}
