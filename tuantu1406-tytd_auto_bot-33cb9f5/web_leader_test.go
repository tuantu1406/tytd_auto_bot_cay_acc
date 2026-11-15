package tytd_go

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// seedLeader creates a leader-tier account (IsLeader + live VIP).
func seedLeader(t *testing.T, bot *TeleBot, id string) *model.User {
	t.Helper()
	u := &model.User{
		ID: id, Username: "leader" + id, Password: "pw", ServerID: 100,
		VipLevel: 1, VipExpireAt: time.Now().Add(30 * 24 * time.Hour),
		IsLeader: true,
	}
	if err := model.CreateUser(bot.ydb.GetDB(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

// seedMember creates a member owned by leaderID with the given login-fail age
// (zero = never failed) and VIP state.
func seedMember(t *testing.T, bot *TeleBot, id, leaderID string, vipDays int, lastFail time.Time) *model.User {
	t.Helper()
	u := &model.User{
		ID: id, Username: "m" + id, Password: "pw", ServerID: 100,
		VipLevel: 1, VipExpireAt: time.Now().Add(time.Duration(vipDays) * 24 * time.Hour),
		LeaderID: leaderID, LastLoginFail: lastFail,
	}
	if err := model.CreateUser(bot.ydb.GetDB(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

// flashOf decodes the ?m= flash out of a redirect Location.
func flashOf(t *testing.T, rec *httptest.ResponseRecorder) (string, bool) {
	t.Helper()
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("bad location %q: %v", loc, err)
	}
	m := u.Query().Get("m")
	return m, strings.HasPrefix(m, "!!")
}

func TestWebLeaderTierGating(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()

	// Plain member of the leader cannot see leader pages.
	seedLeader(t, bot, "8000")
	seedUser(t, bot, "8001", 30)
	cookie, _ := loginAs(t, bot, h, 8001)
	for _, path := range []string{"/members", "/events"} {
		rec := doGet(t, h, path, cookie)
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/?m=") {
			t.Fatalf("non-leader %s: code=%d loc=%q", path, rec.Code, rec.Header().Get("Location"))
		}
	}

	// Leader sees both pages.
	lcookie, _ := loginAs(t, bot, h, 8000)
	for _, path := range []string{"/members", "/events"} {
		if rec := doGet(t, h, path, lcookie); rec.Code != http.StatusOK {
			t.Fatalf("leader %s: code=%d", path, rec.Code)
		}
	}
}

func TestWebMemberOwnershipGate(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8100") // owner
	seedLeader(t, bot, "8101") // rival leader
	seedMember(t, bot, "8102", "8100", 30, time.Time{})
	cookie, csrf := loginAs(t, bot, h, 8101)

	// Rival leader must not touch 8102 through any per-member action.
	for _, action := range []string{"password", "autologin", "remove", "login", "config"} {
		form := url.Values{"action": {action}, "member_id": {"8102"}, "password": {"hijack"}}
		rec := doPost(t, h, "/members", cookie, csrf, form)
		if !strings.Contains(rec.Header().Get("Location"), "%21%21") {
			t.Fatalf("rival %s accepted: %q", action, rec.Header().Get("Location"))
		}
	}
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8102"); u.Password != "pw" {
		t.Fatal("rival changed the password")
	}
	if _, err := model.GetUser(bot.ydb.GetDB(), "8102"); err != nil {
		t.Fatal("rival deleted the member")
	}

	// Owner may.
	oCookie, oCSRF := loginAs(t, bot, h, 8100)
	form := url.Values{"action": {"password"}, "member_id": {"8102"}, "password": {"newpw"}}
	if rec := doPost(t, h, "/members", oCookie, oCSRF, form); strings.Contains(rec.Header().Get("Location"), "%21%21") {
		t.Fatalf("owner password change refused: %q", rec.Header().Get("Location"))
	}
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8102"); u.Password != "newpw" {
		t.Fatalf("owner password change not stored: %q", u.Password)
	}
}

// login_all must skip every ineligible member (cooldown / no VIP) without
// ever reaching startPlayerLogin — i.e. zero outbound logins in a test env.
func TestWebMemberLoginAllSkipsOffline(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8200")
	seedMember(t, bot, "8201", "8200", 30, time.Now())           // login-fail cooldown
	seedMember(t, bot, "8202", "8200", -1, time.Time{})          // expired VIP
	noVIP := seedMember(t, bot, "8203", "8200", 30, time.Time{}) // VipLevel == 0
	noVIP.VipLevel = 0
	model.CreateUser(bot.ydb.GetDB(), noVIP)

	cookie, csrf := loginAs(t, bot, h, 8200)
	rec := doPost(t, h, "/members", cookie, csrf, url.Values{"action": {"login_all"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login_all: code=%d", rec.Code)
	}
	msg, isErr := flashOf(t, rec)
	if isErr {
		t.Fatalf("login_all errored: %q", msg)
	}
	if !strings.HasPrefix(msg, "Đã cho 0 đệ tử vào game, 3 bỏ qua") {
		t.Fatalf("login_all started real logins or miscounted: %q", msg)
	}

	// chuot_on_all + logout_all run against fake sessions only.
	bot.session.Store(int64(8201), &Player{Running: true})
	rec = doPost(t, h, "/members", cookie, csrf, url.Values{"action": {"chuot_on_all"}})
	if msg, isErr = flashOf(t, rec); isErr {
		t.Fatalf("chuot_on_all: %q", msg)
	}
	if v, ok := bot.session.Load(int64(8201)); !ok || !v.(*Player).ChuotRun {
		t.Fatal("chuot_on_all did not reach the fake session")
	}
	rec = doPost(t, h, "/members", cookie, csrf, url.Values{"action": {"logout_all"}})
	if msg, isErr = flashOf(t, rec); isErr {
		t.Fatalf("logout_all: %q", msg)
	}
	if _, ok := bot.session.Load(int64(8201)); ok {
		t.Fatal("logout_all left the session running")
	}
}

// Per-member login button: every server-protection gate must refuse before
// anything reaches startPlayerLogin — zero outbound logins in a test env.
func TestWebMemberForceLoginGates(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8500")
	seedMember(t, bot, "8501", "8500", 30, time.Now())  // 3-minute login-fail cooldown
	seedMember(t, bot, "8502", "8500", -1, time.Time{}) // expired VIP
	cookie, csrf := loginAs(t, bot, h, 8500)

	// A cooldown refusal must never kick out the member's live session —
	// the fail stamp can be old while the current session is healthy.
	bot.session.Store(int64(8501), &Player{Running: true})
	rec := doPost(t, h, "/members", cookie, csrf, url.Values{"action": {"login"}, "member_id": {"8501"}})
	msg, isErr := flashOf(t, rec)
	if !isErr || !strings.Contains(msg, "đợi") {
		t.Fatalf("cooldown member accepted: %q", msg)
	}
	if v, ok := bot.session.Load(int64(8501)); !ok || !v.(*Player).Running {
		t.Fatal("cooldown refusal killed a running session")
	}

	rec = doPost(t, h, "/members", cookie, csrf, url.Values{"action": {"login"}, "member_id": {"8502"}})
	if msg, isErr = flashOf(t, rec); !isErr || !strings.Contains(msg, "VIP") {
		t.Fatalf("expired VIP member accepted: %q", msg)
	}
	if _, ok := bot.session.Load(int64(8502)); ok {
		t.Fatal("refused login started a session")
	}

	// Unknown member id refused.
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf, url.Values{"action": {"login"}, "member_id": {"9999"}})); !isErr {
		t.Fatal("unknown member accepted")
	}

	// Max-online cap: the 8501 fake fills the default cap of 1, so even an
	// eligible member is refused before the login core is reached.
	seedMember(t, bot, "8503", "8500", 30, time.Time{})
	rec = doPost(t, h, "/members", cookie, csrf, url.Values{"action": {"login"}, "member_id": {"8503"}})
	if msg, isErr = flashOf(t, rec); !isErr || !strings.Contains(msg, "đầy") {
		t.Fatalf("over-cap member accepted: %q", msg)
	}
	if _, ok := bot.session.Load(int64(8503)); ok {
		t.Fatal("over-cap refusal started a session")
	}
}

// Đổi MK on a member with a live session must update BOTH the DB row and the
// running player, so the next reconnect uses the new password.
func TestWebMemberPasswordLiveSession(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8400")
	seedMember(t, bot, "8401", "8400", 30, time.Time{})
	cookie, csrf := loginAs(t, bot, h, 8400)

	p := &Player{Password: "pw", Running: true}
	bot.session.Store(int64(8401), p)

	// Empty password refused, nothing touched.
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"password"}, "member_id": {"8401"}, "password": {""}})); !isErr {
		t.Fatal("empty password accepted")
	}

	rec := doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"password"}, "member_id": {"8401"}, "password": {"freshpw"}})
	if msg, isErr := flashOf(t, rec); isErr {
		t.Fatalf("password change refused: %q", msg)
	}
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8401"); u.Password != "freshpw" {
		t.Fatalf("DB password not updated: %q", u.Password)
	}
	if p.Password != "freshpw" {
		t.Fatalf("live player still holds the old password: %q", p.Password)
	}
}

// rttoggle: the leader flips the dashboard's runtime switches (chuot,
// maohiem, lucdao, yeuthap, huyetmach, chatcay, notify) on a member's LIVE
// session.
func TestWebMemberRuntimeToggle(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8450")
	seedMember(t, bot, "8451", "8450", 30, time.Time{})
	seedMember(t, bot, "8452", "8450", 30, time.Time{})
	cookie, csrf := loginAs(t, bot, h, 8450)

	// Member with NO live session: refused, and no phantom session born.
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"rttoggle"}, "member_id": {"8451"}, "feat": {"maohiem_on"}})); !isErr {
		t.Fatal("runtime toggle accepted without a live session")
	}

	p := &Player{Running: true, Maohiem: true}
	bot.session.Store(int64(8451), p)
	if msg, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"rttoggle"}, "member_id": {"8451"}, "feat": {"maohiem_off"}})); isErr {
		t.Fatalf("maohiem_off refused: %q", msg)
	}
	if p.Maohiem {
		t.Fatal("live member session still has Maohiem on")
	}
	// Huyetmach (added 2026-10-07, user: "cho đệ tử thêm huyết mạch giống acc chính").
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"rttoggle"}, "member_id": {"8451"}, "feat": {"huyetmach_on"}})); isErr {
		t.Fatal("huyetmach_on refused")
	}
	if !p.HuyetMach {
		t.Fatal("huyetmach_on did not set HuyetMach")
	}
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"rttoggle"}, "member_id": {"8451"}, "feat": {"huyetmach_off"}})); isErr {
		t.Fatal("huyetmach_off refused")
	}
	if p.HuyetMach {
		t.Fatal("huyetmach_off did not clear HuyetMach")
	}
	// The chatcay three-state cycle: BẬT → +đấu pháp → nghỉ.
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"rttoggle"}, "member_id": {"8451"}, "feat": {"chatcay_on"}})); isErr {
		t.Fatal("chatcay_on refused")
	}
	if !p.Peach || p.XaVe {
		t.Fatalf("chatcay_on: peach=%v xave=%v", p.Peach, p.XaVe)
	}
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"rttoggle"}, "member_id": {"8451"}, "feat": {"chatcay_xave_on"}})); isErr {
		t.Fatal("chatcay_xave_on refused")
	}
	if !p.Peach || !p.XaVe {
		t.Fatal("chatcay_xave_on did not set both")
	}
	// Junk feat rejected without touching the switch it would otherwise hit.
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"rttoggle"}, "member_id": {"8451"}, "feat": {"nuke_all_on"}})); !isErr {
		t.Fatal("unknown feat accepted")
	}
	// Ownership: another leader cannot toggle 8451.
	seedLeader(t, bot, "8453")
	oCookie, oCSRF := loginAs(t, bot, h, 8453)
	if _, isErr := flashOf(t, doPost(t, h, "/members", oCookie, oCSRF,
		url.Values{"action": {"rttoggle"}, "member_id": {"8451"}, "feat": {"chuot_on"}})); !isErr {
		t.Fatal("rival leader toggled a foreign member")
	}
	if p.ChuotRun {
		t.Fatal("refused toggle still flipped the switch")
	}
	// GET /members shows the live switch pills for the running member — and
	// every per-member switch form is data-live (fetch, no page reload) with
	// a data-flip so the page-local listener can repaint its label; the
	// listener itself must ship in the page script (parity with dashboard).
	rec := doGet(t, h, "/members", cookie)
	body := rec.Body.String()
	if !strings.Contains(body, `name="feat" value="maohiem_on"`) || !strings.Contains(body, "Yêu tháp:") ||
		!strings.Contains(body, "Huyết mạch:") {
		t.Fatal("members page missing the runtime toggle buttons")
	}
	if !strings.Contains(body, `data-live="1" data-flip="Mạo hiểm"`) {
		t.Fatal("per-member switch not live")
	}
	if !strings.Contains(body, "addEventListener('tytd:live'") {
		t.Fatal("members page missing the live repainter")
	}
}

// Leader-side Tây Du / Luyện Khí switches (owner 2026-11-09): per-member
// rttoggle flips + the bulk-all buttons in "Điều khiển cả bang đệ tử".
// Runtime-only law [[tytd-player-object-gates]]: these live on the live
// *Player, never the DB. Like the dashboard test, cbStartOnce must never
// actually launch (bare Player has no socket) — the Luyện Khí legs all ride
// a pre-set Running ledger into the refusal/busy paths.
func TestWebMemberTayduThanlong(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8460")
	seedMember(t, bot, "8461", "8460", 30, time.Time{})
	seedMember(t, bot, "8462", "8460", 30, time.Time{})
	cookie, csrf := loginAs(t, bot, h, 8460)

	p1 := &Player{Running: true}
	bot.session.Store(int64(8461), p1)

	// Owner 2026-11-09: engine switches are ACTIVITY-gated — running session
	// alone is NOT enough; no 311/130 season rows → no buttons, per-member
	// nor bulk.
	body := doGet(t, h, "/members", cookie).Body.String()
	for _, absent := range []string{`value="taydu_on_all"`, `value="thanlong_on_all"`, `data-flip="Tây Du"`, `value="thanlong_run"`} {
		if strings.Contains(body, absent) {
			t.Fatalf("members page shows engine button with nothing open: %q", absent)
		}
	}

	// Activities open (own ActList rows on the member's session, both twins
	// of webActOpen): the per-member buttons AND the bulk buttons appear
	// (bulk rides the fleet-wide open map — this member is in the fleet).
	nowMs := time.Now().UnixMilli()
	p1.ActList.entries = map[int32]activityEntry{
		900311: {ActivityID: 900311, Type: 311, BeginTime: nowMs - 3600_000, EndTime: nowMs + 86400_000},
		900130: {ActivityID: 900130, Type: 130, BeginTime: nowMs - 3600_000, EndTime: nowMs + 86400_000},
	}
	body = doGet(t, h, "/members", cookie).Body.String()
	for _, want := range []string{`value="taydu_on_all"`, `value="taydu_off_all"`, `value="thanlong_on_all"`, `data-flip="Tây Du"`, `value="thanlong_run"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("members page missing %q", want)
		}
	}

	// Per-member flip, same arm as /start_taydu.
	if _, isBad := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"rttoggle"}, "member_id": {"8461"}, "feat": {"taydu_on"}})); isBad {
		t.Fatal("rttoggle taydu_on refused")
	}
	if !p1.Xiyou {
		t.Fatal("rttoggle taydu_on did not arm Xiyou")
	}
	if _, isBad := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"rttoggle"}, "member_id": {"8461"}, "feat": {"taydu_off"}})); isBad {
		t.Fatal("rttoggle taydu_off refused")
	}
	if p1.Xiyou {
		t.Fatal("rttoggle taydu_off did not clear Xiyou")
	}

	// Per-member Luyện Khí refusal while a ván is in flight.
	p1.CbMu.Lock()
	p1.cb = &composeBallState{Running: true}
	p1.CbMu.Unlock()
	if _, isBad := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"rttoggle"}, "member_id": {"8461"}, "feat": {"thanlong_run"}})); !isBad {
		t.Fatal("rttoggle thanlong_run accepted while running")
	}

	// Bulk Tây Du: 8461 live flips, offline 8462 counted as skipped.
	msg, isBad := flashOf(t, doPost(t, h, "/members", cookie, csrf, url.Values{"action": {"taydu_on_all"}}))
	if isBad || !strings.Contains(msg, "Xong 1") || !strings.Contains(msg, "1 bỏ qua") {
		t.Fatalf("taydu_on_all: %q", msg)
	}
	if !p1.Xiyou {
		t.Fatal("taydu_on_all did not arm the live member")
	}

	// Bulk Luyện Khí: both ledgers pre-set Running → everything busy, none
	// starts, offline member counted separately.
	p2 := &Player{Running: true}
	p2.CbMu.Lock()
	p2.cb = &composeBallState{Running: true}
	p2.CbMu.Unlock()
	bot.session.Store(int64(8462), p2)
	if msg, isBad = flashOf(t, doPost(t, h, "/members", cookie, csrf, url.Values{"action": {"thanlong_on_all"}})); isBad || !strings.Contains(msg, "2 đang có ván chạy") {
		t.Fatalf("thanlong_on_all: %q", msg)
	}

	// Bulk tắt hết clears both live sessions.
	if msg, isBad = flashOf(t, doPost(t, h, "/members", cookie, csrf, url.Values{"action": {"taydu_off_all"}})); isBad || !strings.Contains(msg, "Xong 2") {
		t.Fatalf("taydu_off_all: %q", msg)
	}
	if p1.Xiyou || p2.Xiyou {
		t.Fatal("taydu_off_all left a member armed")
	}
}

// The expandable per-member config panel: thresholds/checkboxes/schedules
// persist, blank password keeps the old one, credential edits reset GameUID
// and patch the live session, and a bad schedule aborts the WHOLE save.
func TestWebMemberConfigForm(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8700")
	m := seedMember(t, bot, "8701", "8700", 30, time.Time{})
	m.LoginSchedule = "6:00"
	m.MinPeachLevel = 4
	m.GameUID = 4242
	model.CreateUser(bot.ydb.GetDB(), m)
	cookie, csrf := loginAs(t, bot, h, 8700)

	cfgForm := func(kv url.Values) url.Values {
		f := url.Values{
			"action": {"config"}, "member_id": {"8701"},
			"username": {"m8701"}, "server_id": {"100"}, "password": {""},
			"min_dao": {"4"}, "min_binh": {"2"}, "min_luuly": {"6"},
			"schedule_login": {"6:00"},
		}
		for k, v := range kv {
			f[k] = v
		}
		return f
	}

	// Bad schedule aborts everything: min_dao in the same form must NOT land.
	rec := doPost(t, h, "/members", cookie, csrf,
		cfgForm(url.Values{"min_dao": {"99"}, "schedule_login": {"25:00"}}))
	if msg, isErr := flashOf(t, rec); !isErr {
		t.Fatalf("bad schedule accepted: %q", msg)
	}
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8701"); u.MinPeachLevel != 4 || u.LoginSchedule != "6:00" {
		t.Fatal("half-applied a config save that failed validation")
	}

	// Valid save: thresholds + checkboxes + schedule changes, blank password
	// and unchanged creds → password and GameUID untouched.
	bot.session.Store(int64(8701), &Player{Running: true, ChuotMinDao: 4})
	rec = doPost(t, h, "/members", cookie, csrf, cfgForm(url.Values{
		"min_dao":        {"7"},
		"schedule_login": {""}, // blank clears, same grammar as chat
		"auto_login":     {"on"},
		"tu_linh":        {"on"},
		// Rendered event rows carry their shown_ marker (hidden row = event
		// closed on the server → the server must not parse the field).
		"shown_auto_thiendo": {"1"}, "auto_thiendo": {"on"},
		"shown_auto_thiendo_toc": {"1"}, "auto_thiendo_toc": {"on"},
	}))
	if msg, isErr := flashOf(t, rec); isErr {
		t.Fatalf("valid config refused: %q", msg)
	}
	u, _ := model.GetUser(bot.ydb.GetDB(), "8701")
	if u.MinPeachLevel != 7 || u.LoginSchedule != "" || !u.AutoLogin || !u.AutoGather || !u.AutoSkyTrade || !u.AutoSkyTradeSpeed {
		t.Fatalf("config not stored: %+v", u)
	}
	if u.Password != "pw" || u.GameUID != 4242 {
		t.Fatalf("blank-pw config touched creds: pw=%q uid=%d", u.Password, u.GameUID)
	}
	if v, _ := bot.session.Load(int64(8701)); v.(*Player).ChuotMinDao != 7 {
		t.Fatal("live player kept the old threshold")
	}

	// Credential edit: new password hits DB + live player, GameUID resets so
	// the next login re-selects with the new creds.
	rec = doPost(t, h, "/members", cookie, csrf,
		cfgForm(url.Values{"password": {"freshpw"}, "server_id": {"101"}}))
	if msg, isErr := flashOf(t, rec); isErr {
		t.Fatalf("cred config refused: %q", msg)
	}
	u, _ = model.GetUser(bot.ydb.GetDB(), "8701")
	if u.Password != "freshpw" || u.ServerID != 101 || u.GameUID != 1 {
		t.Fatalf("cred edit not applied: %+v", u)
	}
	if v, _ := bot.session.Load(int64(8701)); v.(*Player).Password != "freshpw" {
		t.Fatal("live player kept the old password")
	}
}

// Global Tụ Linh gate (/set_auto_tu_linh, mirroring auto_login): absent row
// and garbage values keep the historic "gathering allowed" default; only an
// explicit false closes the gate for every account.
func TestAutoTuLinhGateDefault(t *testing.T) {
	bot := newTestWebBot(t)
	g := bot.ydb.GetDB()
	if !model.IsAutoTuLinhGateOpen(g) {
		t.Fatal("missing config row must default to open (historic behaviour)")
	}
	model.SetConfig(g, model.ConfigKeyAutoTuLinh, "false")
	if model.IsAutoTuLinhGateOpen(g) {
		t.Fatal("explicit false must close the gate")
	}
	model.SetConfig(g, model.ConfigKeyAutoTuLinh, "1")
	if !model.IsAutoTuLinhGateOpen(g) {
		t.Fatal("truthy value must keep the gate open")
	}
	model.SetConfig(g, model.ConfigKeyAutoTuLinh, "maybe")
	if !model.IsAutoTuLinhGateOpen(g) {
		t.Fatal("garbage value must fall back to open, not closed")
	}
}

func TestWebEventOrderEnumerationAndValidation(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8300")
	for _, id := range []string{"9301", "9302"} { // 9-prefix event ids
		if err := model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
			ID: id, Username: "ev" + id, Password: "pw", ServerID: 100, LeaderID: "8300",
		}); err != nil {
			t.Fatal(err)
		}
	}
	cookie, csrf := loginAs(t, bot, h, 8300)

	// Validation happens before any enumeration: bad island / act / arg all
	// refuse with zero sessions loaded (and thus zero network).
	bad := []url.Values{
		{"which": {"nonsense"}, "act": {"move"}, "arg": {"5"}},
		{"which": {"bonglai"}, "act": {"teleport"}, "arg": {"5"}},
		{"which": {"bonglai"}, "act": {"move"}, "arg": {"0"}},
		{"which": {"bonglai"}, "act": {"move"}, "arg": {"abc"}},
	}
	for i, form := range bad {
		rec := doPost(t, h, "/events/order", cookie, csrf, form)
		if !strings.Contains(rec.Header().Get("Location"), "%21%21") {
			t.Fatalf("bad order %d accepted: %q", i, rec.Header().Get("Location"))
		}
	}

	// Valid order with nobody logged in: counted, not executed.
	form := url.Values{"which": {"thienma"}, "act": {"move"}, "arg": {"123"}}
	rec := doPost(t, h, "/events/order", cookie, csrf, form)
	msg, isErr := flashOf(t, rec)
	if isErr {
		t.Fatalf("valid order refused: %q", msg)
	}
	if !strings.Contains(msg, "0 acc") || !strings.Contains(msg, "2 bỏ qua") {
		t.Fatalf("order tally wrong: %q", msg)
	}
}

func TestWebEventRemoveOwnershipAndMapGate(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8400") // owner
	seedLeader(t, bot, "8401") // rival
	model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
		ID: "9401", Username: "ev1", Password: "pw", ServerID: 100, LeaderID: "8400",
	})
	model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
		ID: "9402", Username: "ev2", Password: "pw", ServerID: 100, LeaderID: "8401",
	})

	cookie, csrf := loginAs(t, bot, h, 8400)
	// Rival's event user is invisible to the owner.
	rec := doPost(t, h, "/events", cookie, csrf, url.Values{"action": {"remove"}, "member_id": {"9402"}})
	if !strings.Contains(rec.Header().Get("Location"), "%21%21") {
		t.Fatalf("removed a foreign event user: %q", rec.Header().Get("Location"))
	}
	// Own event user is removed (no session → forceStopEventUser no-ops).
	rec = doPost(t, h, "/events", cookie, csrf, url.Values{"action": {"remove"}, "member_id": {"9401"}})
	if strings.Contains(rec.Header().Get("Location"), "%21%21") {
		t.Fatalf("own removal refused: %q", rec.Header().Get("Location"))
	}
	if _, err := model.GetEventUser(bot.ydb.GetDB(), "9401"); err == nil {
		t.Fatal("event user survived removal")
	}

	// Minimap needs the leader's own running game session, and must answer
	// with an error STATUS (fetch-friendly), never a redirect.
	for _, tc := range []struct {
		q    string
		code int
	}{
		{"", http.StatusBadRequest},
		{"?which=nonsense", http.StatusBadRequest},
		{"?which=nonsense&fmt=json", http.StatusBadRequest},
		{"?which=bonglai", http.StatusConflict},
		// The clickable-city layer must fail the same friendly way as the
		// PNG when there is no session to ask.
		{"?which=bonglai&fmt=json", http.StatusConflict},
		{"?which=ditich", http.StatusConflict},
		{"?which=ditich&fmt=json", http.StatusConflict},
	} {
		rec := doGet(t, h, "/events/map"+tc.q, cookie)
		if rec.Code != tc.code {
			t.Fatalf("map %q: code=%d want=%d body=%q", tc.q, rec.Code, tc.code, rec.Body.String())
		}
		if rec.Header().Get("Location") != "" {
			t.Fatalf("map %q redirected: %q", tc.q, rec.Header().Get("Location"))
		}
	}
	// Côn Lôn and Cửu U are the documented exceptions to the never-redirect
	// contract: the SERVER-RAM policy (2026-10-06) moved their map views to
	// the browser-baked mini apps — /events/map must hand the browser over
	// and never run the Go compositor for a page view.
	rec = doGet(t, h, "/events/map?which=kunlun", cookie)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/kunlun" {
		t.Fatalf("kunlun map: code=%d loc=%q want 302 /kunlun", rec.Code, rec.Header().Get("Location"))
	}
	rec = doGet(t, h, "/events/map?which=cuuu", cookie)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/cuuu" {
		t.Fatalf("cuuu map: code=%d loc=%q want 302 /cuuu", rec.Code, rec.Header().Get("Location"))
	}
	// And mapart no longer serves a server-rendered live.png (the arm was
	// deleted with kunlunLivePNG — a request must 404, not render).
	rec = doGet(t, h, "/events/mapart?b=ku&p=live.png", cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("mapart ku/live.png: code=%d want 404 (server render removed)", rec.Code)
	}

	// A resting event user seeds the default-roster hide: 9403 is owned but
	// never logged in.
	model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
		ID: "9403", Username: "evrest", Password: "pw", ServerID: 100, LeaderID: "8400",
	})
	// Default roster: only the ACTIVE fleet — a resting account is hidden
	// (and 9401 removed / ev2 foreign stay invisible either way).
	rec = doGet(t, h, "/events", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("roster: code=%d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "ev2") {
		t.Fatal("foreign event user leaked into roster")
	}
	if strings.Contains(body, ">evrest<") {
		t.Fatal("resting event user shown in default roster")
	}
	if !strings.Contains(body, "/events?all=1") {
		t.Fatal("missing reveal link for resting accounts")
	}
	// ?all=1 reveals the resting fleet.
	rec = doGet(t, h, "/events?all=1", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("roster all: code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ">evrest<") {
		t.Fatal("?all=1 did not reveal resting event user")
	}
}

// Holy Land (which=cuuu) web surface: the third card only appears when
// one of OUR event sessions has the event announced; orders validate before
// enumeration; a running session receives the real frame.
func TestWebHolyLandCardOrderAndGate(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8600")
	if err := model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
		ID: "9601", Username: "evholy", Password: "pw", ServerID: 100, LeaderID: "8600",
	}); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginAs(t, bot, h, 8600)

	// No event session at all: the Holy Land card stays hidden (GI card too).
	// The static JS mentions 'cuuu', so assert on the card's <h1> only.
	if body := doGet(t, h, "/events", cookie).Body.String(); strings.Contains(body, "<h1>Cửu U Tranh Bá</h1>") {
		t.Fatal("Holy Land card shown with nothing open")
	}

	// Validation precedes enumeration: bad act / city id / sign all refuse
	// while zero sessions are loaded (zero network).
	bad := []url.Values{
		{"which": {"cuuu"}, "act": {"teleport"}, "arg": {"5"}},
		{"which": {"cuuu"}, "act": {"move"}, "arg": {"0"}},
		{"which": {"cuuu"}, "act": {"attack"}, "arg": {"abc"}},
		{"which": {"cuuu"}, "act": {"auto"}, "arg": {"-1"}},
	}
	for i, form := range bad {
		rec := doPost(t, h, "/events/order", cookie, csrf, form)
		if !strings.Contains(rec.Header().Get("Location"), "%21%21") {
			t.Fatalf("bad cuuu order %d accepted: %q", i, rec.Header().Get("Location"))
		}
	}

	// Valid orders with nobody logged in: counted, not executed.
	rec := doPost(t, h, "/events/order", cookie, csrf,
		url.Values{"which": {"cuuu"}, "act": {"move"}, "arg": {"77"}})
	msg, isErr := flashOf(t, rec)
	if isErr || !strings.Contains(msg, "0 acc") || !strings.Contains(msg, "1 bỏ qua") {
		t.Fatalf("cuuu move tally wrong: isErr=%v msg=%q", isErr, msg)
	}
	// auto 0 (off) is the one legal zero.
	rec = doPost(t, h, "/events/order", cookie, csrf,
		url.Values{"which": {"cuuu"}, "act": {"auto"}, "arg": {"0"}})
	if msg, isErr = flashOf(t, rec); isErr || !strings.Contains(msg, "Đã tắt treo máy") {
		t.Fatalf("cuuu auto-off refused: isErr=%v msg=%q", isErr, msg)
	}

	// Running event session with the event open: card + roster line appear,
	// and the order lands as a real frame on the session's socket.
	ep := &EventPlayer{Running: true}
	ep.writeChan = make(chan []byte, 8)
	wireCtxEP(t, ep)
	stt := ep.hlState()
	stt.IsOpen, stt.OpenKnown = true, true
	// The card gate needs a RUNNING round, not just the season flag.
	stt.ScheduleApply = []int64{hlLiveAnchor(time.Now())}
	bot.session.Store(int64(9601), ep)

	body := doGet(t, h, "/events", cookie).Body.String()
	if !strings.Contains(body, "<h1>Cửu U Tranh Bá</h1>") {
		t.Fatal("Holy Land card hidden while open")
	}
	if !strings.Contains(body, "Cửu U Tranh Bá:") {
		t.Fatal("roster line missing for open Holy Land account")
	}

	rec = doPost(t, h, "/events/order", cookie, csrf,
		url.Values{"which": {"cuuu"}, "act": {"move"}, "arg": {"1001"}})
	if msg, isErr = flashOf(t, rec); isErr || !strings.Contains(msg, "1 acc") {
		t.Fatalf("cuuu move to running acc failed: isErr=%v msg=%q", isErr, msg)
	}
	frame := <-ep.writeChan
	if got := frameFuncType(t, frame); got != HOLY_LAND_MOVE_CMD {
		t.Fatalf("cuuu move frame cmd %s want %s", got, HOLY_LAND_MOVE_CMD)
	}
}

// Ball GVG (which=ditich) web surface: same shape as the Holy Land test —
// hidden card, pre-enumeration validation (place ids numeric >0, attack needs
// a name), tally flash, and a real frame on a running open session.
func TestWebBallGVGCardOrderAndGate(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8700")
	if err := model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
		ID: "9701", Username: "evball", Password: "pw", ServerID: 100, LeaderID: "8700",
	}); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginAs(t, bot, h, 8700)

	// Nothing open: the static JS mentions 'ditich', so assert on <h1>.
	if body := doGet(t, h, "/events", cookie).Body.String(); strings.Contains(body, "<h1>Thượng Cổ Di Tích</h1>") {
		t.Fatal("Ball GVG card shown with nothing open")
	}

	// Validation before enumeration: zero sessions loaded, zero network.
	bad := []url.Values{
		{"which": {"ditich"}, "act": {"teleport"}, "arg": {"5"}},
		{"which": {"ditich"}, "act": {"enter"}, "arg": {"0"}},
		{"which": {"ditich"}, "act": {"enter"}, "arg": {"abc"}},
		{"which": {"ditich"}, "act": {"mark"}, "arg": {"-1"}},
		{"which": {"ditich"}, "act": {"attack"}, "arg": {"5"}}, // attack is no longer an act at all
	}
	for i, form := range bad {
		rec := doPost(t, h, "/events/order", cookie, csrf, form)
		if !strings.Contains(rec.Header().Get("Location"), "%21%21") {
			t.Fatalf("bad ditich order %d accepted: %q", i, rec.Header().Get("Location"))
		}
	}

	// Valid argless order with nobody logged in: counted, not executed.
	rec := doPost(t, h, "/events/order", cookie, csrf,
		url.Values{"which": {"ditich"}, "act": {"home"}})
	msg, isErr := flashOf(t, rec)
	if isErr || !strings.Contains(msg, "0 acc") || !strings.Contains(msg, "1 bỏ qua") {
		t.Fatalf("ditich home tally wrong: isErr=%v msg=%q", isErr, msg)
	}

	// Running event session with the event announced: card + roster line,
	// and the order lands as a real frame on the session's socket.
	ep := &EventPlayer{Running: true}
	ep.writeChan = make(chan []byte, 8)
	wireCtxEP(t, ep)
	st := ep.bgState()
	st.ActivityID = 555
	st.Joined = true
	// The card gates on the live WINDOW, not the announced id.
	setActivities(&ep.ActList, activityEntry{
		ActivityID: 555, Type: activityTypeBallGVG,
		BeginTime: time.Now().Add(-time.Hour).UnixMilli(),
		EndTime:   time.Now().Add(time.Hour).UnixMilli(),
	})
	bot.session.Store(int64(9701), ep)

	body := doGet(t, h, "/events", cookie).Body.String()
	if !strings.Contains(body, "<h1>Thượng Cổ Di Tích</h1>") {
		t.Fatal("Ball GVG card hidden while open")
	}
	if !strings.Contains(body, "Thượng Cổ:") {
		t.Fatal("roster line missing for open Ball GVG account")
	}

	rec = doPost(t, h, "/events/order", cookie, csrf,
		url.Values{"which": {"ditich"}, "act": {"enter"}, "arg": {"4001"}})
	if msg, isErr = flashOf(t, rec); isErr || !strings.Contains(msg, "1 acc") {
		t.Fatalf("ditich enter to running acc failed: isErr=%v msg=%q", isErr, msg)
	}
	frame := <-ep.writeChan
	if got := frameFuncType(t, frame); got != BALL_GVG_ENTER_ROOM_CMD {
		t.Fatalf("ditich enter frame cmd %s want %s", got, BALL_GVG_ENTER_ROOM_CMD)
	}
}

func TestWebMembersRosterShowsOwnOnly(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8500")
	seedLeader(t, bot, "8501")
	seedMember(t, bot, "8502", "8500", 30, time.Time{})
	seedMember(t, bot, "8503", "8501", 30, time.Time{})
	cookie, _ := loginAs(t, bot, h, 8500)
	rec := doGet(t, h, "/members", cookie)
	body := rec.Body.String()
	if !strings.Contains(body, "8502") {
		t.Fatal("own member missing from roster")
	}
	if strings.Contains(body, fmt.Sprintf(">%s<", "8503")) {
		t.Fatal("foreign member leaked into roster")
	}
}

// The map-stream endpoints inherit the leader gate, and both refuse bad
// input BEFORE touching sessions, subscriptions, or the CDN — the
// zero-outbound invariant starts at input validation.
func TestWebMapStreamGating(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8500")
	seedUser(t, bot, "8501", 30)

	mCookie, _ := loginAs(t, bot, h, 8501)
	for _, path := range []string{
		"/events/mapart?b=gi&p=images/city/3.png",
		"/events/mapsub?which=bonglai&on=1",
	} {
		rec := doGet(t, h, path, mCookie)
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/?m=") {
			t.Fatalf("non-leader %s: code=%d loc=%q", path, rec.Code, rec.Header().Get("Location"))
		}
	}

	lCookie, _ := loginAs(t, bot, h, 8500)
	// Unknown which: 400 and nothing registered — no subscription, no loop,
	// no game traffic for a bogus card name.
	rec := doGet(t, h, "/events/mapsub?which=skytrade&on=1", lCookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown which: code=%d", rec.Code)
	}
	if s := subEntry(8500, "skytrade"); s != nil && webMapSubs.alive(s) {
		t.Fatal("rejected which still registered a subscription")
	}

	// Unknown bundle / not-in-manifest path: 404 and the CDN is never hit.
	oldFetch := mapArtFetch
	mapArtFetch = func(string) ([]byte, error) {
		t.Error("CDN fetched for a rejected art path")
		return nil, nil
	}
	defer func() { mapArtFetch = oldFetch }()
	for _, q := range []string{
		"b=gi&p=images/city/9999.png", // not in the manifest
		"b=nope&p=images/city/1.png",  // unknown bundle
		"b=gi&p=..%2F..%2Fetc.png",    // traversal
	} {
		if rec := doGet(t, h, "/events/mapart?"+q, lCookie); rec.Code != http.StatusNotFound {
			t.Errorf("mapart %q: code=%d want 404", q, rec.Code)
		}
	}
}

// Fleet knowledge: the map streamer answers from the leader's and regular
// members' sessions — never the event accounts — so a card must appear when
// ANY of those (running) sessions was told about the event, even with every
// event account logged out. Foreign leaders' sessions must not count.
func TestWebEventsCardsFromFleetSessions(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8650")
	if err := model.CreateUser(bot.ydb.GetDB(), &model.User{
		ID: "9650", Username: "mem", Password: "pw", ServerID: 100, LeaderID: "8650",
	}); err != nil {
		t.Fatal(err)
	}
	cookie, _ := loginAs(t, bot, h, 8650)

	if body := doGet(t, h, "/events", cookie).Body.String(); strings.Contains(body, "<h1>Bồng Lai</h1>") {
		t.Fatal("Bồng Lai card shown while no session has seen the event")
	}

	// Leader's own session: Bồng Lai announced, Thượng Cổ Di Tích in its
	// active window (announce alone is NOT enough — the old bug).
	lp := &Player{Running: true}
	lp.GiActivityID = 500
	lp.BgActivityID = 900
	setActivities(&lp.ActList, activityEntry{
		ActivityID: 900, Type: activityTypeBallGVG,
		BeginTime: time.Now().Add(-time.Hour).UnixMilli(),
		EndTime:   time.Now().Add(time.Hour).UnixMilli(),
	})
	bot.session.Store(int64(8650), lp)
	// Member's session: Cửu U with a round RUNNING now — the schedule open
	// flag plus a live battle-window anchor (the ActList 346 season window
	// alone no longer opens the card, user bug 2026-10-06).
	now := time.Now()
	mp := &Player{Running: true, HlOpen: true, HlOpenKnown: true,
		HlScheduleApply: []int64{hlLiveAnchor(now)}}
	bot.session.Store(int64(9650), mp)
	// A foreign leader's session with Thiên Ma open — must not leak.
	fp := &Player{Running: true}
	fp.GiPeakActivityID = 501
	bot.session.Store(int64(7777), fp)

	body := doGet(t, h, "/events", cookie).Body.String()
	for _, want := range []string{"<h1>Bồng Lai</h1>", "<h1>Cửu U Tranh Bá</h1>", "<h1>Thượng Cổ Di Tích</h1>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("card hidden while a fleet session sees the event: %s", want)
		}
	}
	// The GI manual order forms are gone (owner 2026-10-18: the interactive
	// map IS the order UI) — no card may resurrect a POST form targeting the
	// two islands. The cuuu/ditich forms are untouched, so the scan is
	// scoped to the GI hidden inputs, not every /events/order form.
	for _, dead := range []string{`name="which" value="bonglai"`, `name="which" value="thienma"`} {
		if strings.Contains(body, dead) {
			t.Fatalf("manual GI order form is back (%s) — the map popup is the only order UI", dead)
		}
	}
	if strings.Contains(body, "<h1>Thiên Ma</h1>") {
		t.Fatal("foreign leader's Thiên Ma announcement leaked into my page")
	}

	// Stopped sessions know nothing: cards go back to the placeholder.
	lp.Running, mp.Running = false, false
	body = doGet(t, h, "/events", cookie).Body.String()
	if strings.Contains(body, "<h1>Bồng Lai</h1>") || strings.Contains(body, "<h1>Cửu U Tranh Bá</h1>") {
		t.Fatal("cards shown from non-running sessions")
	}
}

// The island status line on a Sự kiện row has ONE source (eventIslandLine):
// /events renders it into <span data-isl> and the SSE state frame pushes the
// identical string as evstat for the page to re-paint in place. And it shows
// ONLY islands the server opened for that acc — with nothing open the whole
// segment (separator included) must be gone, live and on reload (user
// 2026-10-06: "đang không mở Bồng Lai thì hiện Bồng Lai làm gì").
func TestWebEventsIslandStatusLive(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8700")
	if err := model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
		ID: "9701", Username: "evlive", Password: "pw", ServerID: 100, LeaderID: "8700",
	}); err != nil {
		t.Fatal(err)
	}
	// A running event session with NOTHING announced: zero island segments —
	// not even Bồng Lai, whose Telegram helper always answers a line.
	ep := &EventPlayer{Username: "evlive", Running: true}
	bot.session.Store(int64(9701), ep)
	cookie, _ := loginAs(t, bot, h, 8700)
	m, err := model.GetEventUser(bot.ydb.GetDB(), "9701")
	if err != nil {
		t.Fatal(err)
	}
	if got := bot.eventIslandLine(*m); got != "" {
		t.Fatalf("closed islands leaked into the line: %q", got)
	}
	if body := doGet(t, h, "/events", cookie).Body.String(); strings.Contains(body, "Bồng Lai:") {
		t.Fatal("closed Bồng Lai rendered on the row")
	}

	// Server announces both God Island variants → segments appear, page and
	// evstat carry the identical composed line (single-source contract).
	ep.GiEvents[GIEventNormal] = &giEvent{ActivityID: 901}
	ep.GiEvents[GIEventPeak] = &giEvent{ActivityID: 902}
	want := bot.eventIslandLine(*m)
	if !strings.Contains(want, "Bồng Lai:") || !strings.Contains(want, " · Thiên Ma:") {
		t.Fatalf("open islands missing from the line: %q", want)
	}
	body := doGet(t, h, "/events", cookie).Body.String()
	if !strings.Contains(body, `data-isl="9701"> · `+want+`</span>`) {
		t.Fatalf("rendered row does not carry the shared line %q", want)
	}
	u, err := model.GetUser(bot.ydb.GetDB(), "8700")
	if err != nil {
		t.Fatal(err)
	}
	st := bot.sseStateOf(8700, u)
	if got := st.EvStat["9701"]; got != want {
		t.Fatalf("evstat %q != rendered line %q", got, want)
	}
	// A leader's frame always carries both maps — an EMPTY evstat value is
	// how the client learns to WIPE the segment when an island closes (the
	// same non-nil rule the pill update relies on).
	ep.GiEvents[GIEventNormal], ep.GiEvents[GIEventPeak] = nil, nil
	st = bot.sseStateOf(8700, u)
	if got, ok := st.EvStat["9701"]; !ok || got != "" {
		t.Fatalf("closed islands must push empty string (ok=%v got=%q)", ok, got)
	}
	if st.Up == nil || st.EvStat == nil {
		t.Fatalf("leader frame must carry non-nil up+evstat: %+v", st)
	}
}

// The live (no-reload) branch is generic in webRedirectMsg: any handler that
// ends with a flash answers JSON + state when the interceptor's header is
// present. The members autologin flip proves the mechanism beyond /toggle:
// same action, JSON with the DB truth for header posts, plain 303 without.
func TestWebMembersActionLiveJSON(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	leaderID := int64(7201)
	seedLeader(t, bot, "7201")
	seedMember(t, bot, "9000000123", "7201", 30, time.Time{})
	cookie, csrf := loginAs(t, bot, h, leaderID)

	form := url.Values{"action": {"autologin"}, "member_id": {"9000000123"}}
	rec := doLivePost(t, h, "/members", cookie, csrf, form)
	if rec.Code != http.StatusOK {
		t.Fatalf("live autologin: code=%d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("live autologin not JSON: %v (%s)", err, rec.Body.String())
	}
	if body["ok"] != true || !strings.Contains(fmt.Sprint(body["msg"]), "tự động đăng nhập") {
		t.Fatalf("live autologin body: %v", body)
	}
	m, err := model.GetUser(bot.ydb.GetDB(), "9000000123")
	if err != nil || !m.AutoLogin {
		t.Fatalf("autologin not persisted: %v %v", m, err)
	}
	st, _ := body["state"].(map[string]any)
	if st == nil || st["toggles"] == nil || st["up"] == nil {
		t.Fatalf("leader state frame missing toggles/up: %v", body["state"])
	}

	// Second live click flips it back off (toggle semantics preserved).
	rec = doLivePost(t, h, "/members", cookie, csrf, form)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["ok"] != true {
		t.Fatalf("second live autologin: %v", rec.Body.String())
	}
	if m, _ := model.GetUser(bot.ydb.GetDB(), "9000000123"); m.AutoLogin {
		t.Fatal("autologin did not toggle back")
	}

	// Headerless: the classic redirect+flash flow is untouched.
	rec = doPost(t, h, "/members", cookie, csrf, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("plain autologin: code=%d", rec.Code)
	}
	if msg, isErr := flashOf(t, rec); isErr || !strings.Contains(msg, "tự động đăng nhập") {
		t.Fatalf("plain flash: %q", msg)
	}

	// Refusals come back as ok=false JSON too (foreign member id).
	rec = doLivePost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"autologin"}, "member_id": {"9999999999"}})
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["ok"] != false {
		t.Fatalf("foreign member live refusal: %s", rec.Body.String())
	}
}

// Leader-side giftcode for one member (web twin of /redeem <code> <user_id>,
// but gated to the leader's own roster).
func TestWebMemberRedeemGiftCode(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	leaderID := int64(7301)
	seedLeader(t, bot, "7301")
	seedMember(t, bot, "9000000321", "7301", 5, time.Time{})
	if err := model.CreateGiftCode(bot.ydb.GetDB(), "VIPTEST", 2); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginAs(t, bot, h, leaderID)

	before, err := model.GetUser(bot.ydb.GetDB(), "9000000321")
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"action": {"redeem"}, "member_id": {"9000000321"}, "code": {"VIPTEST"}}
	rec := doPost(t, h, "/members", cookie, csrf, form)
	if msg, isErr := flashOf(t, rec); isErr || !strings.Contains(msg, "VIP đã gia hạn") {
		t.Fatalf("redeem: %q", msg)
	}
	after, err := model.GetUser(bot.ydb.GetDB(), "9000000321")
	if err != nil {
		t.Fatal(err)
	}
	if !after.VipExpireAt.After(before.VipExpireAt) {
		t.Fatalf("VIP not extended: %v -> %v", before.VipExpireAt, after.VipExpireAt)
	}

	// One use per member per code (model owns the rule; surface must refuse).
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf, form)); !isErr {
		t.Fatal("double redeem accepted")
	}

	// Empty code and foreign member refuse.
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"redeem"}, "member_id": {"9000000321"}, "code": {"  "}})); !isErr {
		t.Fatal("empty code accepted")
	}
	seedMember(t, bot, "9000000322", "8888", 5, time.Time{}) // another leader's
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf,
		url.Values{"action": {"redeem"}, "member_id": {"9000000322"}, "code": {"VIPTEST"}})); !isErr {
		t.Fatal("foreign member redeem accepted")
	}
	if m, _ := model.GetUser(bot.ydb.GetDB(), "9000000322"); m.VipExpireAt.Equal(after.VipExpireAt) {
		t.Fatal("foreign member VIP changed anyway")
	}

	// The roster page actually renders the button.
	if rec := doGet(t, h, "/members", cookie); !strings.Contains(rec.Body.String(), "askCode(") {
		t.Fatal("roster missing Nạp mã button")
	}
}

// Côn Lôn HUD orders (which=kunlun, argless acts): tile acts still demand a
// passable arg, while tangtoc/hoisinh_item skip validation AND arg entirely —
// the item button is the ONE authorized item exception and must reach the
// socket as a real 219915 useItem=true frame.
func TestWebKunlunOrderHUDActs(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8420")
	if err := model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
		ID: "9421", Username: "evku", Password: "pw", ServerID: 100, LeaderID: "8420",
	}); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginAs(t, bot, h, 8420)

	// Tile-act validation unchanged: bad act / missing or void arg refuse
	// with zero sessions loaded.
	bad := []url.Values{
		{"which": {"kunlun"}, "act": {"teleport"}, "arg": {"5"}},
		{"which": {"kunlun"}, "act": {"march"}},
		{"which": {"kunlun"}, "act": {"march"}, "arg": {"abc"}},
		{"which": {"kunlun"}, "act": {"sweep"}, "arg": {"0"}}, // void cell
	}
	for i, form := range bad {
		rec := doPost(t, h, "/events/order", cookie, csrf, form)
		if !strings.Contains(rec.Header().Get("Location"), "%21%21") {
			t.Fatalf("bad kunlun order %d accepted: %q", i, rec.Header().Get("Location"))
		}
	}

	// Argless HUD acts need NO arg and are never "invalid": with nobody
	// logged in they only tally.
	for _, act := range []string{"tangtoc", "hoisinh_item"} {
		rec := doPost(t, h, "/events/order", cookie, csrf,
			url.Values{"which": {"kunlun"}, "act": {act}})
		msg, isErr := flashOf(t, rec)
		if isErr || !strings.Contains(msg, "0 acc") || !strings.Contains(msg, "1 bỏ qua") {
			t.Fatalf("kunlun %s tally wrong: isErr=%v msg=%q", act, isErr, msg)
		}
	}

	// Running event session inside an open Côn Lôn window: both acts land as
	// real frames.
	ep := &EventPlayer{Running: true}
	ep.writeChan = make(chan []byte, 8)
	wireCtxEP(t, ep)
	full, err := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{{
		ActivityId:    pb.Int32(777),
		Type:          pb.Int32(activityTypeKunlunWar),
		BeginShowTime: pb.Int64(time.Now().Add(-2 * time.Hour).UnixMilli()),
		BeginTime:     pb.Int64(time.Now().Add(-time.Hour).UnixMilli()),
		EndTime:       pb.Int64(time.Now().Add(time.Hour).UnixMilli()),
		EndShowTime:   pb.Int64(time.Now().Add(2 * time.Hour).UnixMilli()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ep.ActList.applyList("9421", full, true)
	ep.ku = &kuEventState{SceneIn: true, Point: 123, SpeedUp: 4}
	bot.session.Store(int64(9421), ep)

	rec := doPost(t, h, "/events/order", cookie, csrf,
		url.Values{"which": {"kunlun"}, "act": {"tangtoc"}})
	if msg, isErr := flashOf(t, rec); isErr || !strings.Contains(msg, "tăng tốc") {
		t.Fatalf("kunlun tangtoc flash wrong: isErr=%v msg=%q", isErr, msg)
	}
	frame := <-ep.writeChan
	if got := frameFuncType(t, frame); got != KUNLUN_ACCELERATE_CMD {
		t.Fatalf("tangtoc frame cmd %s want %s", got, KUNLUN_ACCELERATE_CMD)
	}
	var acc proto.KunlunWarAcclerateMoveReqMsg
	if pb.Unmarshal(frame[18:], &acc) != nil || acc.GetSpeedUpCount() != 4 {
		t.Fatalf("tangtoc body %+v", &acc)
	}

	ep.ku.Point = 0 // dead → the item revive is eligible
	rec = doPost(t, h, "/events/order", cookie, csrf,
		url.Values{"which": {"kunlun"}, "act": {"hoisinh_item"}})
	if msg, isErr := flashOf(t, rec); isErr || !strings.Contains(msg, "vật phẩm") {
		t.Fatalf("kunlun hoisinh_item flash wrong: isErr=%v msg=%q", isErr, msg)
	}
	frame = <-ep.writeChan
	if got := frameFuncType(t, frame); got != KUNLUN_BACK_HOME_CMD {
		t.Fatalf("hoisinh frame cmd %s want %s", got, KUNLUN_BACK_HOME_CMD)
	}
	var bh proto.KunlunWarBackHomeReq
	if pb.Unmarshal(frame[18:], &bh) != nil || !bh.GetUseItem() {
		t.Fatalf("hoisinh body %+v — want useItem=true", &bh)
	}

	// "Cắn quả" (219926 / KUNLUN_USE_ITEM_CMD, user-authorized item op).
	// v3 rule (customer fix 2026-10-07): the press ARMS A CHASE — Energy 0,
	// first-guess heal 5 ⇒ first bite num=2. A bag id the bot never saw
	// (harvested on the real client, or a restart) is no refusal: the op falls
	// back to kuFruitItemObserved with NO bag clamp (unknown basket → server
	// arbitrates).
	ep.ku.Point = 123 // alive again; Energy 0, FruitItem 0 → fallback id, num 2
	rec = doPost(t, h, "/events/order", cookie, csrf,
		url.Values{"which": {"kunlun"}, "act": {"anqua"}})
	if msg, isErr := flashOf(t, rec); isErr || !strings.Contains(msg, "cắn cho đủ 10") {
		t.Fatalf("kunlun anqua flash wrong: isErr=%v msg=%q", isErr, msg)
	}
	frame = <-ep.writeChan
	if got := frameFuncType(t, frame); got != KUNLUN_USE_ITEM_CMD {
		t.Fatalf("anqua frame cmd %s want %s", got, KUNLUN_USE_ITEM_CMD)
	}
	var ui proto.KunlunWarUseItemReqMsg
	if pb.Unmarshal(frame[18:], &ui) != nil || ui.GetActivityId() != 777 ||
		ui.GetItemId() != kuFruitItemObserved || ui.GetNum() != 2 {
		t.Fatalf("anqua body %+v — want {777,%d,2}", &ui, kuFruitItemObserved)
	}
	// The chase stays ARMED until a verdict lands — the server says energy
	// reached 10 (heal learned 5/fruit), so no duplicate packet and a fresh
	// press is legal again (a second press mid-chase answers 0 with no frame:
	// TestKuEatChaseLearnsHeal covers the continuation itself).
	v1, _ := pb.Marshal(&proto.KunlunWarUseItemRespMsg{Ret: pb.Int32(0), Energy: pb.Int32(10)})
	ep.kuHandleUseItem(v1)
	if ep.ku.eatChase || ep.ku.FruitHeal != 5 {
		t.Fatalf("verdict must settle the chase + learn heal: %+v", ep.ku)
	}
	// Receipt-folded bag: its id wins and the count CLAMPS the bite ("thiếu
	// quả thì cắn số còn lại thôi") — bag of 1 from 8 NL sends num=1.
	ok6, _ := pb.Marshal(&proto.KunlunWarGetSparPowerRespMsg{Ret: pb.Int32(0), Reward: pb.String("105248=1")})
	ep.kuHandleSparPower(ok6)
	if ep.ku.FruitItem != 105248 || ep.ku.FruitBag != 1 {
		t.Fatalf("receipt fold wrong: %+v", ep.ku)
	}
	ep.ku.Energy = 8 // fell below target again → press bites the last fruit
	doPost(t, h, "/events/order", cookie, csrf,
		url.Values{"which": {"kunlun"}, "act": {"anqua"}})
	frame = <-ep.writeChan
	if pb.Unmarshal(frame[18:], &ui) != nil || ui.GetItemId() != 105248 || ui.GetNum() != 1 {
		t.Fatalf("anqua bag-clamped body %+v — want itemId 105248 num 1", &ui)
	}
}

// GET /events/mapview — opening the /kunlun page (and every camera settle)
// asks the bot "who stands on the visible tiles, what is the real HP": the
// 219903 GAME_BLOCK_INFO rides the seated event account's socket, the window
// marches fold into the march ledger the SSE tick already animates, and the
// page gets names + city HP back as JSON.
func TestWebKunlunMapView(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8420")
	cookie, _ := loginAs(t, bot, h, 8420)

	// Nobody logged in → clean 502 (the page keeps its old view).
	if rec := doGet(t, h, "/events/mapview?x0=1&y0=1&x1=2&y1=2", cookie); rec.Code != http.StatusBadGateway {
		t.Fatalf("mapview nobody: code=%d", rec.Code)
	}
	// Missing bound → 400 before anything reaches a session.
	if rec := doGet(t, h, "/events/mapview?x0=1&y0=1&x1=2", cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("mapview missing arg: code=%d", rec.Code)
	}

	// Live event session inside an open Côn Lôn window, seated, with the
	// floor/group EnterActivity cache — the request goes out immediately.
	ep, frames := kuOpenEventPlayer(t)
	ep.ChatID = 8420
	ep.ku = &kuEventState{SceneIn: true,
		enterAct: &proto.KunlunWarEnterActivityResp{Floor: pb.Int32(10000), Group: pb.Int32(3)}}
	bot.session.Store(int64(9421), ep)

	type outcome struct{ rec *httptest.ResponseRecorder }
	done := make(chan outcome, 1)
	go func() { done <- outcome{doGet(t, h, "/events/mapview?x0=5&y0=6&x1=8&y1=9", cookie)} }()

	cmdLow, body := kuNextFrame(t, frames)
	if cmdLow != KUNLUN_GAME_BLOCK_INFO_CMD[4:] {
		t.Fatalf("mapview cmd %s want %s", cmdLow, KUNLUN_GAME_BLOCK_INFO_CMD[4:])
	}
	var req proto.KunlunWarGameBlockInfoReq
	if pb.Unmarshal(body, &req) != nil {
		t.Fatal("mapview body unreadable")
	}
	if req.GetActivityId() != 777 || req.GetFloorId() != 10000 || req.GetGroupId() != 3 {
		t.Fatalf("mapview req %+v — want act 777 floor 10000 group 3", &req)
	}
	// A 4×4 rect flattens to exactly 16 posIndex rows (y*61+x) — no stride
	// at this size — starting bottom-left-of-rect at 6*61+5.
	bv := req.GetBlockVec()
	if len(bv) != 16 || bv[0] != 6*61+5 || bv[15] != 9*61+8 {
		t.Fatalf("blockVec %v", bv)
	}

	now := time.Now().UnixMilli()
	raw, err := pb.Marshal(&proto.KunlunWarGameBlockInfoResp{
		Ret: pb.Int32(0), FloorId: pb.Int32(10000), GroupId: pb.Int32(3),
		BlockInfo: []*proto.KunlunWarBlockInfo{{
			Pos: pb.Int32(6*61 + 7),
			DisplayInfo: []*proto.KunlunWarPlayerDisplayInfo{{
				PlayerId: pb.Int64(5001), NickName: pb.String("Nguyễn A"),
				UnionId: pb.Int64(77), Power: pb.String("12.5M"),
			}},
		}},
		CityInfo: []*proto.KunlunWarGameCityInfo{{
			CityId: pb.Int32(1001), Hp: pb.Int32(1000), CurHp: pb.Int32(800),
			HaveFight: pb.Bool(true), UnionId: pb.Int64(77),
		}},
		RouteInfo: []*proto.KunlunWarGameRouteInfo{{
			PlayerId: pb.Int64(6001), FromPoint: pb.Int32(372), ToPoint: pb.Int32(433),
			Time: pb.Int64(now), Arrival: pb.Int64(now + 30000),
			PlayerInfo: &proto.KunlunWarPlayerInfo{UnionId: pb.Int64(77)},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ep.kuDeliverFrame(SERVER_KUNLUN_GAME_BLOCK_INFO, raw)

	rec := (<-done).rec
	if rec.Code != http.StatusOK {
		t.Fatalf("mapview answer: code=%d body=%q", rec.Code, rec.Body.String())
	}
	var view kuViewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("mapview json: %v (%q)", err, rec.Body.String())
	}
	if ps := view.Cells["373"]; len(ps) != 1 || ps[0].N != "Nguyễn A" || ps[0].P != "12.5M" || ps[0].U != 77 {
		t.Fatalf("cells %+v — want Nguyễn A · LC 12.5M on ô 373", view.Cells)
	}
	if c := view.Cities["1001"]; c.HP != 1000 || c.Cur != 800 || !c.Fight || c.Dead {
		t.Fatalf("cities %+v — want 800/1000 fighting", view.Cities)
	}
	// The window's march landed in the puller's ledger → the next SSE tick
	// animates it like any 219943 row.
	var seen bool
	for _, r := range ep.kuRoutesSnapshot(time.Now()) {
		if r.TP == 433 && r.FP == 372 {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("routes snapshot %+v — want 372→433 folded", ep.kuRoutesSnapshot(time.Now()))
	}
}

// The blockVec cap: an oversized rect must ride into ≤ kuViewMaxBlocks
// sampled entries, still covering the corners, and pos 0 never ships.
func TestKuViewBlocksCap(t *testing.T) {
	bv := kuViewBlocks(0, 0, 60, 60)
	if len(bv) == 0 || len(bv) > kuViewMaxBlocks {
		t.Fatalf("whole board: %d blocks (cap %d)", len(bv), kuViewMaxBlocks)
	}
	if bv[0] != 3 {
		t.Fatalf("whole board starts %d — want 3 (pos 0 skipped, step 3)", bv[0])
	}
	// Rects swap when inverted and clamp off-board without erroring out.
	if a, b := kuViewBlocks(8, 9, 5, 6), kuViewBlocks(-50, -50, 200, 200); len(a) != 16 || len(b) == 0 {
		t.Fatalf("inverted=%v clamped len=%d", a, len(b))
	}
	if len(kuViewBlocks(0, 0, 0, 0)) != 0 {
		t.Fatal("pos 0 must never ship")
	}
}

// Leader toggles Luân Hồi Bí Cảnh per member through the config panel:
// check stores it, uncheck clears it (the auto-loop re-reads the DB).
func TestWebMemberConfigLuanhoi(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8810")
	m := seedMember(t, bot, "8811", "8810", 30, time.Time{})
	m.MinPeachLevel, m.MinBottleLevel, m.MinLuuLyLevel = 4, 2, 6
	model.CreateUser(bot.ydb.GetDB(), m)
	cookie, csrf := loginAs(t, bot, h, 8810)

	post := func(on bool) {
		f := url.Values{
			"action": {"config"}, "member_id": {"8811"},
			"username": {"m8811"}, "server_id": {"100"}, "password": {""},
			"min_dao": {"4"}, "min_binh": {"2"}, "min_luuly": {"6"},
			// The panel renders this row only while 172 is open — a rendered
			// row always ships its marker (hidden row = server never
			// announced it = the server must not parse the field).
			"shown_auto_luanhoi": {"1"},
		}
		if on {
			f.Set("auto_luanhoi", "on")
		}
		if rec := doPost(t, h, "/members", cookie, csrf, f); rec.Code != http.StatusSeeOther {
			t.Fatalf("config POST(on=%v): code=%d", on, rec.Code)
		}
	}
	post(true)
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8811"); !u.AutoRebornTrial {
		t.Fatal("auto_luanhoi not stored for member")
	}
	post(false)
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8811"); u.AutoRebornTrial {
		t.Fatal("auto_luanhoi uncheck not stored for member")
	}
	// The panel renders the toggle reflecting the stored state — but only
	// while a resident ActList says the season is open (webOpenEventKeys).
	lp := &Player{Running: true}
	lp.ActList.entries = map[int32]activityEntry{
		900172: {ActivityID: 900172, Type: 172,
			BeginTime: time.Now().Add(-time.Hour).UnixMilli(), EndTime: time.Now().Add(24 * time.Hour).UnixMilli()},
	}
	bot.session.Store(int64(8810), lp)
	if rec := doGet(t, h, "/members", cookie); !strings.Contains(rec.Body.String(), `name="auto_luanhoi"`) {
		t.Fatal("members page missing the auto_luanhoi toggle")
	}
	// No open season → the row hides entirely (owner: ẩn hết khi chưa mở).
	bot.session.Delete(int64(8810))
	if rec := doGet(t, h, "/members", cookie); strings.Contains(rec.Body.String(), `name="auto_luanhoi"`) {
		t.Fatal("closed event config must not render")
	}
}

// Bulk config for the whole Đệ Tử roster (owner 2026-11-09 "acc chính có gì
// thì bulk có cái đó"): every member row gets the same numbers/switches, the
// live session is patched, and a member WITHOUT a session must not panic.
func TestWebMemberConfigAll(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8820")
	seedMember(t, bot, "8821", "8820", 30, time.Time{})
	seedMember(t, bot, "8822", "8820", 30, time.Time{})
	live := &Player{Running: true}
	bot.session.Store(int64(8821), live)
	cookie, csrf := loginAs(t, bot, h, 8820)

	form := url.Values{
		"action":  {"config_all"},
		"min_dao": {"5"}, "min_binh": {"3"}, "min_luuly": {"7"}, "min_tuling": {"1200"},
		"bottle_pref": {"on"}, "auto_login": {"on"}, "auto_start_chuot": {"on"}, "tu_linh": {"on"},
		"shown_auto_luanhoi": {"1"}, "auto_luanhoi": {"on"},
		"shown_auto_thuongminh": {"1"}, "auto_thuongminh": {"on"},
	}
	if msg, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf, form)); isErr {
		t.Fatalf("config_all refused: %q", msg)
	}
	for _, id := range []string{"8821", "8822"} {
		m, err := model.GetUser(bot.ydb.GetDB(), id)
		if err != nil {
			t.Fatal(err)
		}
		if m.MinPeachLevel != 5 || m.MinBottleLevel != 3 || m.MinLuuLyLevel != 7 || m.MinGatherIncome != 1200 {
			t.Fatalf("member %s numbers not applied: %+v", id, m)
		}
		if !m.BottlePrefered || !m.AutoLogin || !m.AutoMouse || !m.AutoGather {
			t.Fatalf("member %s switches not applied: %+v", id, m)
		}
		if !m.AutoRebornTrial || !m.AutoSeaWar {
			t.Fatalf("member %s event switches not applied: %+v", id, m)
		}
	}
	// (min_tuling stays DB-only: the gather loop re-reads it each minute.)
	if live.ChuotMinDao != 5 || !live.AutoSeaWar || !live.AutoGather {
		t.Fatalf("live session not patched: %+v", live)
	}

	// A bad number aborts before any row is written (all-or-nothing law).
	form.Set("min_dao", "-3")
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf, form)); !isErr {
		t.Fatal("negative min_dao accepted by config_all")
	}
	if m, _ := model.GetUser(bot.ydb.GetDB(), "8822"); m.MinPeachLevel != 5 {
		t.Fatalf("rejected bulk still wrote rows: %+v", m)
	}
}

// Marker rule on the per-member panel: a HIDDEN event row (no shown_ marker)
// must never flip that switch, while a rendered one behaves normally.
func TestWebMemberConfigMarkerRule(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8830")
	m := seedMember(t, bot, "8831", "8830", 30, time.Time{})
	m.MinPeachLevel, m.MinBottleLevel, m.MinLuuLyLevel = 4, 2, 6
	m.AutoRebornTrial, m.AutoSeaWar = true, true
	model.CreateUser(bot.ydb.GetDB(), m)
	cookie, csrf := loginAs(t, bot, h, 8830)

	base := url.Values{
		"action": {"config"}, "member_id": {"8831"},
		"username": {"m8831"}, "server_id": {"100"}, "password": {""},
		"min_dao": {"4"}, "min_binh": {"2"}, "min_luuly": {"6"},
	}
	// No markers at all → both event switches survive untouched.
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf, base)); isErr {
		t.Fatal("marker-less config refused")
	}
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8831"); !u.AutoRebornTrial || !u.AutoSeaWar {
		t.Fatalf("hidden rows clobbered the event switches: %+v", u)
	}
	// Only the marked one parses (and here it is unchecked → off).
	base.Set("shown_auto_luanhoi", "1")
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookie, csrf, base)); isErr {
		t.Fatal("marked config refused")
	}
	u, _ := model.GetUser(bot.ydb.GetDB(), "8831")
	if u.AutoRebornTrial {
		t.Fatal("marked row did not apply the un-tick")
	}
	if !u.AutoSeaWar {
		t.Fatal("unmarked row must survive")
	}
}

// Tab Sự kiện per-account + bulk config: off-switches persist on the
// EventUser row and patch the live EventPlayer immediately; a delegated (🤝)
// account is skipped by the bulk save (it has no EventUser row to gate).
// TestWebEventEatAll — the 🍏 card (owner 2026-11-09): renders only while
// the 349 season is open on the fleet; POST eat_all fires the drain on every
// RUNNING event session (deputies count — runtime order, no DB row) and
// counts only those. EPs get a real ctx + buffered writeChan so the fire-
// and-forget goroutines park on the seat and die with the test's cancel —
// the POST itself never waits on them (async order fire law).
func TestWebEventEatAll(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8870")
	for _, e := range []struct{ id, name string }{{"9871", "eat1"}, {"9872", "eat2"}} {
		if err := model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
			ID: e.id, Username: e.name, Password: "pw", ServerID: 100, LeaderID: "8870",
		}); err != nil {
			t.Fatal(err)
		}
	}
	dep := &model.User{ID: "9873", Username: "dep9", Password: "pw", ServerID: 100,
		VipLevel: 1, VipExpireAt: time.Now().Add(30 * 24 * time.Hour), DeputyOf: "8870"}
	if err := model.CreateUser(bot.ydb.GetDB(), dep); err != nil {
		t.Fatal(err)
	}
	mkEP := func(name string) *EventPlayer {
		ep := &EventPlayer{Username: name, Running: true}
		ctx, cancel := context.WithCancel(context.Background())
		ep.ctx = ctx
		t.Cleanup(cancel) // parks the fire-and-forget drain goroutines at exit
		ep.writeChan = make(chan []byte, 32)
		nowMs := time.Now().UnixMilli()
		ep.ActList.entries = map[int32]activityEntry{
			900349: {ActivityID: 900349, Type: 349, BeginTime: nowMs - 1000, EndTime: nowMs + 86400_000},
		}
		return ep
	}
	cookie, csrf := loginAs(t, bot, h, 8870)

	// Season closed → card hidden; POST still works but reports nobody hanging
	// (no "!!" — it's an honest empty result, not an error).
	if body := doGet(t, h, "/events", cookie).Body.String(); strings.Contains(body, `value="eat_all"`) {
		t.Fatal("eat_all card rendered while 349 closed")
	}
	if msg, isBad := flashOf(t, doPost(t, h, "/events", cookie, csrf, url.Values{"action": {"eat_all"}})); isBad || !strings.Contains(msg, "Chưa có") {
		t.Fatalf("eat_all with empty fleet: %q", msg)
	}

	// 9871 + deputy 9873 hang with the season open; 9872 stays offline.
	bot.session.Store(int64(9871), mkEP("eat1"))
	bot.session.Store(int64(9873), mkEP("dep9"))
	body := doGet(t, h, "/events", cookie).Body.String()
	if !strings.Contains(body, `value="eat_all"`) || !strings.Contains(body, "Cắn quả") {
		t.Fatal("eat_all card missing while 349 open")
	}
	msg, isBad := flashOf(t, doPost(t, h, "/events", cookie, csrf, url.Values{"action": {"eat_all"}}))
	if isBad || !strings.Contains(msg, "2 acc") {
		t.Fatalf("eat_all: %q", msg)
	}
}

func TestWebEventConfigAndBulk(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8840")
	if err := model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
		ID: "9841", Username: "ev1", Password: "pw", ServerID: 100, LeaderID: "8840",
	}); err != nil {
		t.Fatal(err)
	}
	if err := model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
		ID: "9842", Username: "ev2", Password: "pw", ServerID: 100, LeaderID: "8840",
	}); err != nil {
		t.Fatal(err)
	}
	// A delegated account: users row with deputy_of set (no EventUser).
	dep := &model.User{ID: "9843", Username: "dep1", Password: "pw", ServerID: 100,
		VipLevel: 1, VipExpireAt: time.Now().Add(30 * 24 * time.Hour), DeputyOf: "8840"}
	if err := model.CreateUser(bot.ydb.GetDB(), dep); err != nil {
		t.Fatal(err)
	}
	ep := &EventPlayer{Username: "ev1", Running: true}
	bot.session.Store(int64(9841), ep)
	cookie, csrf := loginAs(t, bot, h, 8840)

	// Panel renders only for the open season (349 open, 172 closed): the
	// per-acc form carries the Thương Minh switch but no Luân Hồi one.
	lp := &Player{Running: true}
	lp.ActList.entries = map[int32]activityEntry{
		900349: {ActivityID: 900349, Type: 349,
			BeginTime: time.Now().Add(-time.Hour).UnixMilli(), EndTime: time.Now().Add(24 * time.Hour).UnixMilli()},
	}
	bot.session.Store(int64(8840), lp)
	body := doGet(t, h, "/events", cookie).Body.String()
	if !strings.Contains(body, `id="ecfg-9841"`) || !strings.Contains(body, `name="auto_thuongminh"`) {
		t.Fatal("open-349 per-acc config panel missing")
	}
	if strings.Contains(body, `name="auto_luanhoi"`) {
		t.Fatal("closed 172 must not render on the event tab")
	}
	if strings.Contains(body, `id="ecfg-9843"`) {
		t.Fatal("delegated account must not get a config panel")
	}

	// Single account: turn Luân Hồi off, keep Thương Minh on.
	if msg, isErr := flashOf(t, doPost(t, h, "/events", cookie, csrf, url.Values{
		"action": {"config"}, "member_id": {"9841"},
		"shown_auto_luanhoi":    {"1"},
		"shown_auto_thuongminh": {"1"}, "auto_thuongminh": {"on"},
	})); isErr {
		t.Fatalf("event config refused: %q", msg)
	}
	m, err := model.GetEventUser(bot.ydb.GetDB(), "9841")
	if err != nil {
		t.Fatal(err)
	}
	if !m.OffRebornTrial || m.OffSeaWar {
		t.Fatalf("off-switches not stored: %+v", m)
	}
	if !ep.OffRbt || ep.OffSeaWar {
		t.Fatalf("live EventPlayer not patched: offRbt=%v offSeaWar=%v", ep.OffRbt, ep.OffSeaWar)
	}

	// Bulk: everyone except the delegated account.
	if msg, isErr := flashOf(t, doPost(t, h, "/events", cookie, csrf, url.Values{
		"action":             {"config_all"},
		"shown_auto_luanhoi": {"1"}, "auto_luanhoi": {"on"},
	})); isErr {
		t.Fatalf("event config_all refused: %q", msg)
	}
	for _, id := range []string{"9841", "9842"} {
		m, err := model.GetEventUser(bot.ydb.GetDB(), id)
		if err != nil {
			t.Fatal(err)
		}
		if m.OffRebornTrial || m.OffSeaWar {
			t.Fatalf("bulk left %s switched off: %+v", id, m)
		}
	}
	if ep.OffRbt || ep.OffSeaWar {
		t.Fatalf("bulk did not patch the live session: %+v", ep)
	}
	// The delegated users row is untouched by the event config (no such row).
	if _, err := model.GetEventUser(bot.ydb.GetDB(), "9843"); err == nil {
		t.Fatal("bulk invented an EventUser row for the delegated account")
	}

	// Foreign account is refused (a LEADER, so the tier gate is not the one
	// answering — the ownership check must be).
	seedLeader(t, bot, "8849")
	oCookie, oCSRF := loginAs(t, bot, h, 8849)
	if _, isErr := flashOf(t, doPost(t, h, "/events", oCookie, oCSRF, url.Values{
		"action": {"config"}, "member_id": {"9841"}, "shown_auto_luanhoi": {"1"},
	})); !isErr {
		t.Fatal("foreign event account accepted")
	}
}
