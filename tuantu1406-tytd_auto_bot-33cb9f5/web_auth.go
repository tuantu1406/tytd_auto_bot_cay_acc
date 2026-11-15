package tytd_go

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"

	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/model"
)

// Telegram Login Widget auth. The widget hands back id/auth_date/hash and
// friends; the payload is authentic iff hash equals HMAC-SHA256 keyed by
// SHA256(bot token) over the sorted "key=value" lines (Telegram's
// data_check_string recipe). We never trust anything else about the visitor:
// webGate then applies the exact same ban/existence/VIP checks the chat uses,
// so a web login can never be worth more than a Telegram one.

// webLoginLimiter: a hash guesser is offline anyway, but cap the callback
// endpoint per IP regardless. 60/min (was 20): one legit Telegram-Web mini
// app open legitimately burns several hits (entry + bridge POST + retries
// after iframe reloads) and many customers sit behind one NAT — 20 made
// ordinary opens 429 (owner report 2026-10-25).
var webLoginLimiter = newWebLimiter(60, time.Minute)

// setWebSessionCookie mints the session cookie for the mini app flows.
// SameSite=None+Secure is mandatory there: Telegram Web hosts the mini app
// in a CROSS-SITE iframe (web.telegram.org embedding our origin), and a
// SameSite=Lax cookie is not stored in third-party context at all — the JS
// bridge then looped POSTing /miniapp/login (cookie never stuck, / kept
// bouncing to /login) until webLoginLimiter answered 429. Mini apps are
// HTTPS by Telegram's own requirement, so None+Secure is always valid in
// production; a non-secure request (local dev) keeps Lax — browsers reject
// None without Secure.
func (t *TeleBot) setWebSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	sameSite := http.SameSiteLaxMode
	secure := t.webSecure(r)
	if secure {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{
		Name: webCookieName, Value: token, Path: "/",
		MaxAge:   int(webSessionTTL / time.Second),
		HttpOnly: true, SameSite: sameSite, Secure: secure,
	})
}

// verifyTelegramLogin checks one widget payload against botToken and returns
// the Telegram user id. It is pure (no DB, no clock skew beyond auth_date) so
// tests can drive it with a fake token.
func verifyTelegramLogin(params map[string]string, botToken string) (int64, error) {
	for _, k := range []string{"id", "auth_date", "hash"} {
		if params[k] == "" {
			return 0, fmt.Errorf("thiếu trường %s", k)
		}
	}
	// Everything except hash goes into the data-check string — including a
	// domain-verification signature, if present.
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "hash" && params[k] != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + params[k]
	}
	dataCheckString := strings.Join(lines, "\n")

	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(dataCheckString))
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(params["hash"])) {
		log.Printf("[web] widget hash mismatch: got=%s want=%s token_len=%d dcs=%q",
			params["hash"], expected, len(botToken), dataCheckString)
		return 0, fmt.Errorf("hash không hợp lệ")
	}

	authDate, err := strconv.ParseInt(params["auth_date"], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("auth_date không hợp lệ")
	}
	if time.Now().Unix()-authDate > 24*60*60 {
		return 0, fmt.Errorf("phiên đăng nhập đã cũ, thử lại")
	}

	id, err := strconv.ParseInt(params["id"], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("id không hợp lệ")
	}
	return id, nil
}

// verifyTelegramInitData checks the initData of a Telegram Mini App. The
// signature recipe differs from the Login Widget only in the key:
// secret = HMAC-SHA256(key="WebAppData", msg=bot_token) — "WebAppData" is
// the HMAC key and the raw token the message, NOT the reverse (proven
// against a live Telegram-signed payload whose hash matched this derivation
// bit-for-bit and no plausible alternative). All fields except hash —
// including a domain-verification signature — go into the sorted data-check
// string. Returns the Telegram user id embedded in the "user" JSON field.
func verifyTelegramInitData(params map[string]string, botToken string) (int64, error) {
	for _, k := range []string{"auth_date", "hash", "user"} {
		if params[k] == "" {
			return 0, fmt.Errorf("thiếu trường %s", k)
		}
	}
	// Everything except hash goes into the data-check string — including a
	// domain-verification signature, if present (live-verified).
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "hash" && params[k] != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + params[k]
	}
	dataCheckString := strings.Join(lines, "\n")

	keyMac := hmac.New(sha256.New, []byte("WebAppData"))
	keyMac.Write([]byte(botToken))
	secretKey := keyMac.Sum(nil)

	mac := hmac.New(sha256.New, secretKey)
	mac.Write([]byte(dataCheckString))
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(params["hash"])) {
		// Internal diagnostics only (standing rule: protocol detail may live
		// in logs, never in user-facing text). A mismatch is almost always a
		// wrong bot token or a double-decoded value, and both are obvious at
		// a glance once the raw dcs/expected pair is on screen.
		log.Printf("[web] initData hash mismatch: got=%s want=%s token_len=%d dcs=%q",
			params["hash"], expected, len(botToken), dataCheckString)
		return 0, fmt.Errorf("initData không hợp lệ")
	}

	authDate, err := strconv.ParseInt(params["auth_date"], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("auth_date không hợp lệ")
	}
	if time.Now().Unix()-authDate > 24*60*60 {
		return 0, fmt.Errorf("phiên mở app đã cũ, hãy mở lại từ bot")
	}

	var u struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(params["user"]), &u); err != nil || u.ID == 0 {
		return 0, fmt.Errorf("user trong initData không hợp lệ")
	}
	return u.ID, nil
}

// webMiniAppEntry is the Mini App landing URL (BotFather menu button → this):
// Telegram hands over initData in the query, we verify it and mint the very
// same session cookie the widget flow uses — zero login clicks.
func (t *TeleBot) webMiniAppEntry(w http.ResponseWriter, r *http.Request) {
	// A returning visitor inside the webview already holds the cookie — skip
	// re-verification AND the login limiter: a cookie-carried open costs
	// nothing, so reload-happy Telegram Web frames cannot eat the budget.
	if s, _ := t.webSessionFrom(r); s != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	ip := webClientIP(r)
	if !webLoginLimiter.allow(ip) {
		http.Error(w, "quá nhiều lần thử, chờ chút nhé", http.StatusTooManyRequests)
		return
	}
	// No domain/bot config needed here: initData is verified against the bot
	// token baked into this binary, and the JS bridge calls back on the same
	// origin. The domain+bot config only matters for the browser widget
	// fallback (/login button → /auth/callback).
	params := webTelegramParams(r)
	// The usual webview open carries no signed initData in the query at all
	// (only tgWebApp* metadata) — that is not an error, the login page's JS
	// bridge (/miniapp/login) picks the identity up from Telegram.WebApp.
	if params["hash"] == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	chatID, err := verifyTelegramInitData(params, t.Token)
	if err != nil {
		t.webRedirectMsg(w, r, "/login", "!!"+err.Error())
		return
	}
	u, msg := t.webGate(chatID)
	if u == nil {
		t.webRedirectMsg(w, r, "/login", "!!"+msg)
		return
	}
	token, _ := webSessions.create(chatID)
	t.setWebSessionCookie(w, r, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// webMiniAppLogin is the JS bridge for the Mini App: the initial document
// load inside Telegram's webview does NOT carry the signed user fields in the
// query string (only tgWebApp* metadata) — those live in Telegram.WebApp.initData
// and are reachable only from page JavaScript. login.html POSTs that string
// here; we validate it exactly like a query-carried initData and mint the
// session cookie.
func (t *TeleBot) webMiniAppLogin(w http.ResponseWriter, r *http.Request) {
	ip := webClientIP(r)
	if !webLoginLimiter.allow(ip) {
		http.Error(w, "quá nhiều lần thử, chờ chút nhé", http.StatusTooManyRequests)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "post only", http.StatusMethodNotAllowed)
		return
	}
	raw := r.FormValue("init_data")
	params, err := url.ParseQuery(raw)
	if err != nil || len(params) == 0 {
		log.Printf("[web] miniapp login: unparseable init_data (len=%d, keys=%d): %v", len(raw), len(params), err)
		http.Error(w, "initData không hợp lệ", http.StatusBadRequest)
		return
	}
	flat := map[string]string{}
	for k, v := range params {
		if len(v) > 0 {
			flat[k] = v[0]
		}
	}
	chatID, err := verifyTelegramInitData(flat, t.Token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	u, msg := t.webGate(chatID)
	if u == nil {
		http.Error(w, msg, http.StatusForbidden)
		return
	}
	token, _ := webSessions.create(chatID)
	t.setWebSessionCookie(w, r, token)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

// webTelegramParams flattens the widget query values (first value per key).
func webTelegramParams(r *http.Request) map[string]string {
	m := map[string]string{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m
}

// webLoginPage renders the standalone login page with the widget button.
func (t *TeleBot) webLoginPage(w http.ResponseWriter, r *http.Request) {
	if s, _ := t.webSessionFrom(r); s != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	botUser := t.webCfg(model.ConfigKeyWebBotUsername)
	domain := t.webCfg(model.ConfigKeyWebDomain)
	msg, kind := t.webFlash(r)
	tt := template.Must(template.ParseFS(webTemplateFS, "webtmpl/login.html"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := tt.ExecuteTemplate(w, "login", map[string]any{
		"BotUsername": botUser,
		"Domain":      domain,
		"Msg":         msg,
		"MsgKind":     kind,
	})
	if err != nil {
		log.Printf("[web] login render: %v", err)
	}
}

// webLoginCallback is the widget's landing URL: validate, gate, mint session.
func (t *TeleBot) webLoginCallback(w http.ResponseWriter, r *http.Request) {
	ip := webClientIP(r)
	if !webLoginLimiter.allow(ip) {
		http.Error(w, "quá nhiều lần thử, chờ chút nhé", http.StatusTooManyRequests)
		return
	}
	botUser := t.webCfg(model.ConfigKeyWebBotUsername)
	if botUser == "" || t.webCfg(model.ConfigKeyWebDomain) == "" {
		http.Error(w, "Web chưa cấu hình bot/domain (admin chạy: /web <domain> <bot> on)", http.StatusServiceUnavailable)
		return
	}

	chatID, err := verifyTelegramLogin(webTelegramParams(r), t.Token)
	if err != nil {
		t.webRedirectMsg(w, r, "/login", "!!"+err.Error())
		return
	}
	u, msg := t.webGate(chatID)
	if u == nil {
		t.webRedirectMsg(w, r, "/login", "!!"+msg)
		return
	}
	token, _ := webSessions.create(chatID)
	http.SetCookie(w, &http.Cookie{
		Name: webCookieName, Value: token, Path: "/",
		MaxAge:   int(webSessionTTL / time.Second),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: t.webSecure(r),
	})
	t.webRedirectMsg(w, r, "/", "Chào mừng đạo hữu quay lại 👋")
}
