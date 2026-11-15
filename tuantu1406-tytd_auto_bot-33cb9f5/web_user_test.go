package tytd_go

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/model"
)

func TestWebToggleFlipsLivePlayer(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(7001)
	seedUser(t, bot, "7001", 30)
	cookie, csrf := loginAs(t, bot, h, chatID)

	// No resident session → refusal, nothing crashes.
	rec := doPost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {"chuot_on"}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "%21%21") {
		t.Fatalf("toggle without session: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	p := &Player{Running: true}
	bot.session.Store(chatID, p)
	cases := []struct {
		feat string
		get  func() bool
	}{
		{"chuot_on", func() bool { return p.ChuotRun }},
		{"notify_on", func() bool { return p.Notify }},
		{"maohiem_on", func() bool { return p.Maohiem }},
		{"lucdao_on", func() bool { return p.Lucdao }},
		{"yeuthap_on", func() bool { return p.Yeuthap }},
		{"huyetmach_on", func() bool { return p.HuyetMach }},
		{"chatcay_xave_on", func() bool { return p.Peach && p.XaVe }},
	}
	for _, c := range cases {
		rec = doPost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {c.feat}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: code=%d", c.feat, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Location"), "okey") {
			t.Fatalf("%s: unexpected flash %q", c.feat, rec.Header().Get("Location"))
		}
		if !c.get() {
			t.Fatalf("%s did not flip state", c.feat)
		}
	}
	for _, c := range []struct {
		feat string
		get  func() bool
	}{
		{"chuot_off", func() bool { return !p.ChuotRun }},
		{"huyetmach_off", func() bool { return !p.HuyetMach }},
		{"chatcay_off", func() bool { return !p.Peach && !p.XaVe }},
	} {
		doPost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {c.feat}})
		if !c.get() {
			t.Fatalf("%s did not clear state", c.feat)
		}
	}

	// Stopped session refuses like the chat handler does.
	p.Running = false
	if rec := doPost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {"chuot_on"}}); !strings.Contains(rec.Header().Get("Location"), "%21%21") {
		t.Fatalf("toggle on stopped player accepted: %q", rec.Header().Get("Location"))
	}
}

// Công tắc rows for the NON-persist event engines (owner 2026-11-09: "mấy
// cái nút on/off không persist của event giống như start_thanlong hay
// start_taydu thì cũng cho vào công tắc luôn"): Tây Du = plain flip of
// p.Xiyou (armed via armXiyou exactly like /start_taydu), Luyện Khí Thần
// Long = ONE-SHOT action — no off switch, the pill only mirrors the in-flight
// ván (cbIsRunning). Tests never let cbStartOnce actually launch: a bare
// Player has no socket, so cbRunOnce would panic in its goroutine — every
// thanlong POST here rides a pre-set Running ledger into the refusal path.
func TestWebToggleTayduThanlong(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(7011)
	seedUser(t, bot, "7011", 30)
	cookie, csrf := loginAs(t, bot, h, chatID)

	// Owner 2026-11-09: the engine rows are ACTIVITY-gated — no session (or
	// no 311/130 season row) → both rows hidden.
	body := doGet(t, h, "/", cookie).Body.String()
	for _, absent := range []string{`id="tog-taydu"`, `id="tog-thanlong"`, "Tây Du Phục Ma", "Luyện Khí Thần Long"} {
		if strings.Contains(body, absent) {
			t.Fatalf("dashboard shows engine row with nothing open: %q", absent)
		}
	}

	p := &Player{Running: true}
	p.ActList.entries = map[int32]activityEntry{
		900311: {ActivityID: 900311, Type: 311, BeginTime: time.Now().UnixMilli() - 3600_000, EndTime: time.Now().UnixMilli() + 86400_000},
		900130: {ActivityID: 900130, Type: 130, BeginTime: time.Now().UnixMilli() - 3600_000, EndTime: time.Now().UnixMilli() + 86400_000},
	}
	bot.session.Store(chatID, p)
	body = doGet(t, h, "/", cookie).Body.String()
	for _, want := range []string{`id="tog-taydu"`, `id="tog-thanlong"`, `value="thanlong_run"`, "Tây Du Phục Ma", "Luyện Khí Thần Long"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing Công tắc piece %q", want)
		}
	}
	if _, isBad := flashOf(t, doPost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {"taydu_on"}})); isBad {
		t.Fatal("taydu_on refused")
	}
	if !p.Xiyou {
		t.Fatal("taydu_on did not arm Xiyou")
	}
	if body = doGet(t, h, "/", cookie).Body.String(); !strings.Contains(body, `id="tog-taydu" class="pill on"`) {
		t.Fatal("taydu pill not BẬT after arm")
	}
	if _, isBad := flashOf(t, doPost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {"taydu_off"}})); isBad {
		t.Fatal("taydu_off refused")
	}
	if p.Xiyou {
		t.Fatal("taydu_off did not clear Xiyou")
	}

	// Ván in flight: button refuses instead of queueing a second run.
	p.CbMu.Lock()
	p.cb = &composeBallState{Running: true}
	p.CbMu.Unlock()
	if _, isBad := flashOf(t, doPost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {"thanlong_run"}})); !isBad {
		t.Fatal("thanlong_run accepted while a ván is running")
	}
	if body = doGet(t, h, "/", cookie).Body.String(); !strings.Contains(body, `id="tog-thanlong" class="pill on"`) {
		t.Fatal("thanlong pill not ĐANG CHƠI while the ván runs")
	}
}

func TestWebSettingsPersistsAndPatches(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(7002)
	seedUser(t, bot, "7002", 30)
	cookie, csrf := loginAs(t, bot, h, chatID)
	p := &Player{Running: true}
	bot.session.Store(chatID, p)
	// The event rows render only while their season row sits active in a
	// resident ActList (webOpenEventKeys) — open 150+172+349 for this page.
	now := time.Now().UnixMilli()
	p.ActList.entries = map[int32]activityEntry{}
	p.ActList.entries[900150] = activityEntry{ActivityID: 900150, Type: 150, BeginTime: now - 3600_000, EndTime: now + 86400_000}
	p.ActList.entries[900172] = activityEntry{ActivityID: 900172, Type: 172, BeginTime: now - 3600_000, EndTime: now + 86400_000}
	p.ActList.entries[900349] = activityEntry{ActivityID: 900349, Type: 349, BeginTime: now - 3600_000, EndTime: now + 86400_000}

	form := url.Values{
		"min_dao": {"9"}, "min_binh": {"8"}, "min_luuly": {"7"},
		"bottle_pref": {"on"}, "auto_login": {"on"}, "auto_start_chuot": {"on"}, "tu_linh": {"on"},
		"shown_auto_thiendo": {"1"}, "auto_thiendo": {"on"},
		"shown_auto_thiendo_toc": {"1"}, "auto_thiendo_toc": {"on"},
		"shown_auto_luanhoi": {"1"}, "auto_luanhoi": {"on"},
		"shown_auto_thuongminh": {"1"}, "auto_thuongminh": {"on"},
	}
	if rec := doPost(t, h, "/settings", cookie, csrf, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("settings POST: code=%d", rec.Code)
	}
	u, err := model.GetUser(bot.ydb.GetDB(), "7002")
	if err != nil {
		t.Fatal(err)
	}
	if u.MinPeachLevel != 9 || u.MinBottleLevel != 8 || u.MinLuuLyLevel != 7 {
		t.Fatalf("min levels not stored: %+v", u)
	}
	if !u.AutoLogin || !u.AutoMouse || !u.BottlePrefered || !u.AutoGather || !u.AutoSkyTrade || !u.AutoSkyTradeSpeed || !u.AutoRebornTrial || !u.AutoSeaWar {
		t.Fatalf("checkboxes not stored: %+v", u)
	}
	if !p.AutoSeaWar || !p.AutoRebornTrial {
		t.Fatal("live event gates not synced on save")
	}

	// Un-ticking the permanent Tụ Linh and auto-caravan boxes stores them
	// off (both loops re-read the DB each minute, so no live-session patch
	// is expected).
	form.Del("tu_linh")
	form.Del("auto_thiendo")
	form.Del("auto_thiendo_toc")
	form.Del("auto_luanhoi")
	form.Del("auto_thuongminh")
	if rec := doPost(t, h, "/settings", cookie, csrf, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("settings re-POST: code=%d", rec.Code)
	}
	if u, err = model.GetUser(bot.ydb.GetDB(), "7002"); err != nil {
		t.Fatal(err)
	}
	if u.AutoGather {
		t.Fatal("tu_linh uncheck not stored")
	}
	if u.AutoSkyTrade {
		t.Fatal("auto_thiendo uncheck not stored")
	}
	if u.AutoSkyTradeSpeed {
		t.Fatal("auto_thiendo_toc uncheck not stored")
	}
	if u.AutoRebornTrial {
		t.Fatal("auto_luanhoi uncheck not stored")
	}
	if u.AutoSeaWar {
		t.Fatal("auto_thuongminh uncheck not stored")
	}
	if p.AutoSeaWar {
		t.Fatal("live AutoSeaWar not synced on the un-tick save")
	}
	form.Set("tu_linh", "on")
	if p.ChuotMinDao != 9 || p.ChuotMinBinh != 8 || p.ChuotMinLuuly != 7 || !p.BottlePrefered {
		t.Fatalf("live player not patched: %+v", p)
	}

	// The page renders the caravan checkbox reflecting the stored state
	// (off after the un-tick save above — no re-check in this form path).
	if rec := doGet(t, h, "/settings", cookie); !strings.Contains(rec.Body.String(), `name="auto_thiendo"`) ||
		strings.Contains(rec.Body.String(), `name="auto_thiendo" checked`) ||
		!strings.Contains(rec.Body.String(), `name="auto_thiendo_toc"`) ||
		strings.Contains(rec.Body.String(), `name="auto_thiendo_toc" checked`) {
		t.Fatalf("settings page caravan checkbox wrong: code=%d", rec.Code)
	}

	// Negative levels are refused without persisting.
	form.Set("min_dao", "-1")
	if rec := doPost(t, h, "/settings", cookie, csrf, form); !strings.Contains(rec.Header().Get("Location"), "%21%21") {
		t.Fatalf("negative min accepted: %q", rec.Header().Get("Location"))
	}
}

func TestWebSchedulesValidateAndClear(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(7003)
	seedUser(t, bot, "7003", 30)
	cookie, csrf := loginAs(t, bot, h, chatID)

	// Comma separator and several times are normalized + sorted by parseChuotTimes.
	form := url.Values{"schedule_start_chuot": {"12:00, 18:30,9:05"}, "schedule_stop_chuot": {""}, "schedule_login": {""}, "schedule_logout": {""}}
	if rec := doPost(t, h, "/schedules", cookie, csrf, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("schedule POST: code=%d", rec.Code)
	}
	u, _ := model.GetUser(bot.ydb.GetDB(), "7003")
	if u.ChuotStartSchedule == "" || strings.Contains(u.ChuotStartSchedule, ",,") {
		t.Fatalf("chuot start schedule not stored: %q", u.ChuotStartSchedule)
	}
	if !strings.Contains(u.ChuotStartSchedule, "9:05") || !strings.Contains(u.ChuotStartSchedule, "18:30") {
		t.Fatalf("times missing after normalize: %q", u.ChuotStartSchedule)
	}

	// Garbage is refused and the old value survives.
	form.Set("schedule_start_chuot", "abc")
	if rec := doPost(t, h, "/schedules", cookie, csrf, form); !strings.Contains(rec.Header().Get("Location"), "%21%21") {
		t.Fatalf("invalid schedule accepted: %q", rec.Header().Get("Location"))
	}
	if u, _ := model.GetUser(bot.ydb.GetDB(), "7003"); !strings.Contains(u.ChuotStartSchedule, "12:00") {
		t.Fatalf("invalid schedule overwrote the old one: %q", u.ChuotStartSchedule)
	}

	// Clear keyword empties the schedule.
	form.Set("schedule_start_chuot", "xoa")
	doPost(t, h, "/schedules", cookie, csrf, form)
	if u, _ := model.GetUser(bot.ydb.GetDB(), "7003"); u.ChuotStartSchedule != "" {
		t.Fatalf("clear keyword did not clear: %q", u.ChuotStartSchedule)
	}
}

// Per-member table (member_form=2): WYSIWYG rows — prefill, save-one-leaves-
// others, blank clears, and a bad time rejects the whole POST with no
// half-applied writes.
func TestWebMemberSchedulesPerMember(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8600")
	seedMember(t, bot, "8601", "8600", 30, time.Time{})
	seedMember(t, bot, "8602", "8600", 30, time.Time{})
	if err := model.SetChuotStartSchedule(bot.ydb.GetDB(), "8601", "2:00"); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginAs(t, bot, h, 8600)

	body := doGet(t, h, "/schedules", cookie).Body.String()
	for _, want := range []string{`name="ms_8601_start" value="2:00"`, `name="ms_8602_stop" value=""`, "member_form\" value=\"2\""} {
		if !strings.Contains(body, want) {
			t.Fatalf("per-member row missing %q", want)
		}
	}

	// Save one member's rows + another's login — untouched fields must not
	// change (they come back equal to the stored value and are skipped).
	rec := doPost(t, h, "/schedules", cookie, csrf, url.Values{
		"member_form":    {"2"},
		"ms_8601_start":  {"2:00"}, // unchanged on purpose
		"ms_8601_stop":   {"10:00"},
		"ms_8601_login":  {""},
		"ms_8601_logout": {""},
		"ms_8602_start":  {""},
		"ms_8602_stop":   {""},
		"ms_8602_login":  {"6, 21:30"},
		"ms_8602_logout": {""},
	})
	if _, bad := flashOf(t, rec); bad {
		t.Fatalf("per-member save rejected")
	}
	m1, _ := model.GetUser(bot.ydb.GetDB(), "8601")
	if m1.ChuotStartSchedule != "2:00" || m1.ChuotStopSchedule != "10:00" {
		t.Fatalf("8601 rows wrong: %+v", m1)
	}
	m2, _ := model.GetUser(bot.ydb.GetDB(), "8602")
	if m2.ChuotStartSchedule != "" || m2.LoginSchedule != "6:00,21:30" {
		t.Fatalf("8602 rows wrong: %+v", m2)
	}
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8600"); u.ChuotStartSchedule != "" {
		t.Fatalf("per-member save touched the leader's own schedule")
	}

	// Blank = CLEAR on this form (unlike the bulk one): 8601 stop → off.
	rec = doPost(t, h, "/schedules", cookie, csrf, url.Values{
		"member_form": {"2"}, "ms_8601_stop": {""},
	})
	if _, bad := flashOf(t, rec); bad {
		t.Fatalf("blank clear rejected")
	}
	if m1, _ = model.GetUser(bot.ydb.GetDB(), "8601"); m1.ChuotStopSchedule != "" {
		t.Fatalf("blank did not clear 8601 stop: %+v", m1)
	}

	// One bad cell rejects the WHOLE table — the other good cell must not stick.
	rec = doPost(t, h, "/schedules", cookie, csrf, url.Values{
		"member_form":   {"2"},
		"ms_8601_start": {"25:00"},
		"ms_8602_login": {"9:00"},
	})
	if _, bad := flashOf(t, rec); !bad {
		t.Fatalf("invalid time accepted")
	}
	if m2, _ = model.GetUser(bot.ydb.GetDB(), "8602"); m2.LoginSchedule != "6:00,21:30" {
		t.Fatalf("failed table half-applied: %+v", m2)
	}
}

func TestWebMemberSchedules(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8600")
	seedMember(t, bot, "8601", "8600", 30, time.Time{})
	seedMember(t, bot, "8602", "8600", 30, time.Time{})
	cookie, csrf := loginAs(t, bot, h, 8600)

	// The leader page renders the member card with all four schedule fields.
	body := doGet(t, h, "/schedules", cookie).Body.String()
	for _, want := range []string{"member_schedule_start_chuot", "member_schedule_stop_chuot", "member_schedule_login", "member_schedule_logout"} {
		if !strings.Contains(body, want) {
			t.Fatalf("member schedule input %s missing from leader's schedules page", want)
		}
	}

	// A leader's own schedule is set first — the member form must not touch it.
	doPost(t, h, "/schedules", cookie, csrf, url.Values{"schedule_start_chuot": {"7"}})
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8600"); u.ChuotStartSchedule != "7:00" {
		t.Fatalf("own schedule not stored: %q", u.ChuotStartSchedule)
	}

	rec := doPost(t, h, "/schedules", cookie, csrf, url.Values{
		"member_form":                 {"1"},
		"member_schedule_start_chuot": {"6:30, 18"}, "member_schedule_stop_chuot": {"23"},
	})
	if msg, bad := flashOf(t, rec); bad {
		t.Fatalf("member save rejected: %q", msg)
	}
	for _, id := range []string{"8601", "8602"} {
		m, err := model.GetUser(bot.ydb.GetDB(), id)
		if err != nil {
			t.Fatal(err)
		}
		if m.ChuotStartSchedule != "6:30,18:00" || m.ChuotStopSchedule != "23:00" {
			t.Fatalf("member %s not scheduled: %+v", id, m)
		}
	}
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8600"); u.ChuotStartSchedule != "7:00" || u.ChuotStopSchedule != "" {
		t.Fatalf("member save clobbered own schedule: %+v", u)
	}

	// Clear keyword on one field, keep the other.
	rec = doPost(t, h, "/schedules", cookie, csrf, url.Values{
		"member_form":                 {"1"},
		"member_schedule_start_chuot": {"off"}, "member_schedule_stop_chuot": {"23"},
	})
	if _, bad := flashOf(t, rec); bad {
		t.Fatalf("member clear rejected")
	}
	if m, _ := model.GetUser(bot.ydb.GetDB(), "8601"); m.ChuotStartSchedule != "" || m.ChuotStopSchedule != "23:00" {
		t.Fatalf("clear did not apply cleanly: %+v", m)
	}

	// Login/logout schedules reach every member too, and "off" clears one
	// while the other keeps its value.
	rec = doPost(t, h, "/schedules", cookie, csrf, url.Values{
		"member_form":                 {"1"},
		"member_schedule_start_chuot": {"off"}, "member_schedule_stop_chuot": {"23"},
		"member_schedule_login": {"6, 21:30"}, "member_schedule_logout": {"23:50"},
	})
	if _, bad := flashOf(t, rec); bad {
		t.Fatalf("member login/logout save rejected")
	}
	for _, id := range []string{"8601", "8602"} {
		m, _ := model.GetUser(bot.ydb.GetDB(), id)
		if m.LoginSchedule != "6:00,21:30" || m.LogoutSchedule != "23:50" {
			t.Fatalf("member %s login/logout not scheduled: %+v", id, m)
		}
	}
	rec = doPost(t, h, "/schedules", cookie, csrf, url.Values{
		"member_form":                 {"1"},
		"member_schedule_start_chuot": {"off"}, "member_schedule_stop_chuot": {"23"},
		"member_schedule_login": {"off"}, "member_schedule_logout": {"23:50"},
	})
	if _, bad := flashOf(t, rec); bad {
		t.Fatalf("member login clear rejected")
	}
	if m, _ := model.GetUser(bot.ydb.GetDB(), "8601"); m.LoginSchedule != "" || m.LogoutSchedule != "23:50" {
		t.Fatalf("login/logout clear did not apply cleanly: %+v", m)
	}
	if u, _ := model.GetUser(bot.ydb.GetDB(), "8600"); u.LoginSchedule != "" || u.LogoutSchedule != "" {
		t.Fatalf("member login save clobbered own schedules: %+v", u)
	}

	// Bad time → error flash, nothing half-written.
	rec = doPost(t, h, "/schedules", cookie, csrf, url.Values{
		"member_form": {"1"}, "member_schedule_start_chuot": {"25:00"},
	})
	if _, bad := flashOf(t, rec); !bad {
		t.Fatal("invalid member time accepted")
	}

	// Non-leader gets the member form refused.
	seedUser(t, bot, "8603", 30)
	nCookie, nCSRF := loginAs(t, bot, h, 8603)
	rec = doPost(t, h, "/schedules", nCookie, nCSRF, url.Values{"member_form": {"1"}, "member_schedule_start_chuot": {"7"}})
	if _, bad := flashOf(t, rec); !bad {
		t.Fatal("non-leader member schedule accepted")
	}
}

func TestWebAccountCredsAndRedeem(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(7004)
	seedUser(t, bot, "7004", 30)
	bot.session.Store(chatID, &Player{Running: true})
	cookie, csrf := loginAs(t, bot, h, chatID)

	// Credentials change resets GameUID (same as /username).
	model.CreateUser(bot.ydb.GetDB(), &model.User{ID: "7004", Username: "u7004", Password: "pw",
		ServerID: 100, GameUID: 4242, VipLevel: 1, VipExpireAt: time.Now().Add(30 * 24 * time.Hour)})
	form := url.Values{"action": {"set_creds"}, "username": {"newnameweb"}, "password": {"newpw"}, "server_id": {"710547"}}
	if rec := doPost(t, h, "/account", cookie, csrf, form); strings.Contains(rec.Header().Get("Location"), "%21%21") {
		t.Fatalf("set_creds rejected: %q", rec.Header().Get("Location"))
	}
	u, _ := model.GetUser(bot.ydb.GetDB(), "7004")
	if u.Username != "newnameweb" || u.ServerID != 710547 || u.Password != "newpw" {
		t.Fatalf("creds not stored: %+v", u)
	}
	if u.GameUID != 1 {
		t.Fatalf("GameUID not reset: %d", u.GameUID)
	}
	// The account page renders and hosts the (moved) VIP-code box.
	if r := doGet(t, h, "/account", cookie); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "Nạp mã VIP") {
		t.Fatalf("account page render: code=%d", r.Code)
	}

	// A valid gift code goes through the same RedeemGiftCode the bot uses.
	if err := model.CreateGiftCode(bot.ydb.GetDB(), "WEBTEST", 1); err != nil {
		t.Fatal(err)
	}
	if rec := doPost(t, h, "/redeem", cookie, csrf, url.Values{"code": {"WEBTEST"}}); !strings.Contains(rec.Header().Get("Location"), "30") {
		t.Fatalf("redeem flash: %q", rec.Header().Get("Location"))
	}
	if rec := doPost(t, h, "/redeem", cookie, csrf, url.Values{"code": {"NOPE"}}); !strings.Contains(rec.Header().Get("Location"), "%21%21") {
		t.Fatalf("bad code accepted: %q", rec.Header().Get("Location"))
	}
}

// The game-logout action must route through stopPlayerForChatID: with a fake
// running session it must clear t.session without touching the network.
func TestWebAccountGameLogoutStopsSession(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(7005)
	seedUser(t, bot, "7005", 30)
	cookie, csrf := loginAs(t, bot, h, chatID)
	bot.session.Store(chatID, &Player{Running: true})

	if rec := doPost(t, h, "/account", cookie, csrf, url.Values{"action": {"logout"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("logout POST: code=%d", rec.Code)
	}
	if _, ok := bot.session.Load(chatID); ok {
		t.Fatal("session survived game logout")
	}

	// Second attempt reports "not running" instead of a crash.
	if rec := doPost(t, h, "/account", cookie, csrf, url.Values{"action": {"logout"}}); !strings.Contains(rec.Header().Get("Location"), "%21%21") {
		t.Fatalf("double logout: %q", rec.Header().Get("Location"))
	}
}

// Config form (2026-11-09): switches render as label.sw toggles and the
// whole form goes through the no-reload interceptor — JSON truth, never 303.
func TestWebSettingsSwitchesAjax(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(7008)
	seedUser(t, bot, "7008", 30)
	cookie, csrf := loginAs(t, bot, h, chatID)

	if rec := doGet(t, h, "/settings", cookie); !strings.Contains(rec.Body.String(), `class="sw"`) {
		t.Fatal("settings page missing the switch markup")
	}
	rec := doLivePost(t, h, "/settings", cookie, csrf, url.Values{
		"min_dao": {"3"}, "min_binh": {"2"}, "min_luuly": {"1"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("live settings POST must answer JSON, not %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("live settings POST not JSON: %v (%s)", err, rec.Body.String())
	}
	if body["ok"] != true || !strings.Contains(fmt.Sprint(body["msg"]), "Đã lưu") {
		t.Fatalf("live settings body: %v", body)
	}
	if u, _ := model.GetUser(bot.ydb.GetDB(), "7008"); u.MinPeachLevel != 3 {
		t.Fatalf("live settings POST did not persist: %+v", u)
	}
}

// Only events the server actually opened get a config row (ActList season
// status on ANY resident fleet session); no session online → all hidden.
// "active but not yet at fighting hour" still counts (season window, not
// the daily play window — owner 2026-11-09).
func TestWebSettingsOpenGate(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(7009)
	seedUser(t, bot, "7009", 30)
	cookie, _ := loginAs(t, bot, h, chatID)

	p := &Player{Running: true}
	p.ActList.entries = map[int32]activityEntry{
		900172: {ActivityID: 900172, Type: 172,
			BeginTime: time.Now().Add(-time.Hour).UnixMilli(), EndTime: time.Now().Add(24 * time.Hour).UnixMilli()},
	}
	bot.session.Store(chatID, p)
	body := doGet(t, h, "/settings", cookie).Body.String()
	if !strings.Contains(body, `name="auto_luanhoi"`) {
		t.Fatal("open 172 row must render")
	}
	if strings.Contains(body, `name="auto_tiencanh"`) || strings.Contains(body, `name="auto_thuongminh"`) {
		t.Fatal("closed events must not render config rows")
	}

	// Reward status (season ended, claim window) still counts as open.
	e := p.ActList.entries[900172]
	e.BeginTime, e.EndTime, e.EndShowTime = time.Now().Add(-48*time.Hour).UnixMilli(), time.Now().Add(-time.Hour).UnixMilli(), time.Now().Add(time.Hour).UnixMilli()
	p.ActList.entries[900172] = e
	if body := doGet(t, h, "/settings", cookie).Body.String(); !strings.Contains(body, `name="auto_luanhoi"`) {
		t.Fatal("reward-window row must still render")
	}

	// No resident session → the bot cannot know anything → hide them all.
	bot.session.Delete(chatID)
	if body := doGet(t, h, "/settings", cookie).Body.String(); strings.Contains(body, `name="auto_luanhoi"`) {
		t.Fatal("closed event config must not render")
	}
}
func doLivePost(t *testing.T, h http.Handler, path, cookie, csrf string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", csrf)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Tytd-JSON", "1")
	req.AddCookie(&http.Cookie{Name: webCookieName, Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The Công tắc card submits without a reload: JSON replies (never a 303)
// carry the full state frame the SSE stream sends, so pills/buttons repaint
// from server truth — refusals included. Plain posts keep the redirect flow.
func TestWebToggleLiveJSON(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	chatID := int64(7003)
	seedUser(t, bot, "7003", 30)
	cookie, csrf := loginAs(t, bot, h, chatID)

	// Refusal (no session) is JSON too, with running=false so the page can
	// re-render the "not running" truth instead of reloading.
	rec := doLivePost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {"chuot_on"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("live refusal: code=%d", rec.Code)
	}
	var ref map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ref); err != nil {
		t.Fatalf("live refusal not JSON: %v (%s)", err, rec.Body.String())
	}
	if ref["ok"] != false {
		t.Fatalf("refusal without session must answer ok=false: %v", ref["ok"])
	}
	if st, _ := ref["state"].(map[string]any); st == nil || st["running"] != false {
		t.Fatalf("refusal state must say running=false: %v", ref["state"])
	}

	// Unknown feat: refused as JSON, never a redirect.
	rec = doLivePost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {"nope"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("unknown feat: %d %s", rec.Code, rec.Body.String())
	}

	p := &Player{Running: true}
	bot.session.Store(chatID, p)
	rec = doLivePost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {"chatcay_xave_on"}})
	if rec.Code != http.StatusOK || !p.Peach || !p.XaVe {
		t.Fatalf("live success: code=%d peach=%v xave=%v body=%s", rec.Code, p.Peach, p.XaVe, rec.Body.String())
	}
	var ok map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
		t.Fatal(err)
	}
	if ok["ok"] != true {
		t.Fatalf("live success must answer ok=true: %v", ok["ok"])
	}
	st, _ := ok["state"].(map[string]any)
	tg, _ := st["toggles"].(map[string]any)
	if tg == nil || tg["peach"] != true || tg["xave"] != true {
		t.Fatalf("state frame must carry fresh toggles: %v", ok["state"])
	}
	if st["running"] != true {
		t.Fatalf("state frame must carry running=true: %v", st["running"])
	}

	// Classic (headerless) post still reloads via 303 — non-JS browsers
	// keep the old flow untouched.
	rec = doPost(t, h, "/toggle", cookie, csrf, url.Values{"feat": {"chuot_on"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("plain post must keep the redirect flow: %d", rec.Code)
	}
}
