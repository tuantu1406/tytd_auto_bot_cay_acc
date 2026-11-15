package tytd_go

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"tytd_go/internal/model"
)

// User-tier pages. Every write mirrors its Telegram counterpart exactly:
// same model calls, same live-field updates, same validation helpers — the
// web layer only swaps SendMessage for a flash redirect.

// webDashboard renders "/" — account state + the runtime toggles.
func (t *TeleBot) webDashboard(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	t.webRender(w, r, "dashboard", chatID, u, webCSRFOf(r), nil)
}

func webCSRFOf(r *http.Request) string {
	s, _ := webSessions.get(webCookieValue(r))
	if s == nil {
		return ""
	}
	return s.csrf
}

func webCookieValue(r *http.Request) string {
	c, err := r.Cookie(webCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// webAccount is GET (credentials form + leader info) and POST
// (action=set_creds | login | logout).
func (t *TeleBot) webAccount(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	if r.Method == http.MethodPost {
		switch r.FormValue("action") {
		case "set_creds":
			changed := false
			if un := stripInvisible(r.FormValue("username")); un != "" && un != u.Username {
				u.Username = un
				changed = true
			}
			if pw := stripInvisible(r.FormValue("password")); pw != "" && pw != u.Password {
				u.Password = pw
				changed = true
			}
			if sv := r.FormValue("server_id"); strings.TrimSpace(sv) != "" {
				n, err := parseServerID(sv)
				if err != nil {
					t.webRedirectMsg(w, r, "/account", "!!server_id không hợp lệ: "+err.Error())
					return
				}
				if n != u.ServerID {
					u.ServerID = n
					changed = true
				}
			}
			if changed {
				u.GameUID = 1 // Reset GameUID when account credentials change (same as /username etc.)
				if err := model.CreateUser(t.ydb.GetDB(), u); err != nil {
					t.webRedirectMsg(w, r, "/account", "!!lỗi server, không lưu được")
					return
				}
				// A live session keeps the old credentials until re-login;
				// the chat handlers never patch them either.
				t.pushWebState(chatID) // server id / uid line on the dashboard
				t.webRedirectMsg(w, r, "/account", "Đã lưu. Đăng lại để áp dụng.")
				return
			}
			t.webRedirectMsg(w, r, "/account", "Không có gì thay đổi.")
			return
		case "login":
			var msgs []string
			if t.startPlayerLogin(chatID, func(text string) { msgs = append(msgs, text) }) {
				t.webRedirectMsg(w, r, "/", "Đang đăng nhập… (đợi chút rồi tải lại)")
			} else {
				t.webRedirectMsg(w, r, "/", "!!"+strings.Join(msgs, "; "))
			}
			return
		case "lkstart":
			v, ok := t.session.Load(chatID)
			p, okPlayer := v.(*Player)
			if !ok || !okPlayer || !p.Running {
				t.webRedirectMsg(w, r, "/", "!!acc chưa treo game — không tự chơi được")
				return
			}
			t.webRedirectMsg(w, r, "/", t.lkStartWeb(p, chatID))
			return
		case "logout":
			if t.stopPlayerForChatID(chatID) {
				t.webRedirectMsg(w, r, "/", "Đã thoát game.")
			} else {
				t.webRedirectMsg(w, r, "/", "!!đang không chạy có gì mà thoát")
			}
			return
		}
		t.webRedirectMsg(w, r, "/account", "!!yêu cầu không hợp lệ")
		return
	}
	data := map[string]any{"LeaderName": ""}
	if u.LeaderID != "" {
		data["LeaderName"] = u.LeaderID
	}
	t.webRender(w, r, "account", chatID, u, webCSRFOf(r), data)
}

// webSettings is GET (form) / POST (min levels + preferences).
func (t *TeleBot) webSettings(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	if r.Method == http.MethodPost {
		vals := map[string]*int{
			"min_dao":   &u.MinPeachLevel,
			"min_binh":  &u.MinBottleLevel,
			"min_luuly": &u.MinLuuLyLevel,
		}
		for name, dst := range vals {
			n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
			if err != nil || n < 0 {
				t.webRedirectMsg(w, r, "/settings", "!!"+name+" phải là số từ 0 trở lên")
				return
			}
			*dst = n
		}
		// Tụ Linh income floor is int64 — parsed apart from the small ints.
		// Blank keeps the current value; the gather loop reads it from the DB.
		if s := strings.TrimSpace(r.FormValue("min_tuling")); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < 0 {
				t.webRedirectMsg(w, r, "/settings", "!!min_tuling phải là số từ 0 trở lên")
				return
			}
			u.MinGatherIncome = n
		}
		u.BottlePrefered = r.FormValue("bottle_pref") == "on"
		u.AutoLogin = r.FormValue("auto_login") == "on"
		u.AutoMouse = r.FormValue("auto_start_chuot") == "on"
		// Event opt-in switches: the loops read the PLAYER FIELDS, never a
		// DB lookup (owner 2026-10-27 — several rows can share one game_uid
		// when different people configure the same game account). The row
		// persists them for the next login; the live session is synced in
		// the block below this POST.
		u.AutoGather = r.FormValue("tu_linh") == "on"
		// Event rows only render while the server says the event is open —
		// each visible one carries a shown_<key>=1 marker, so a save while a
		// row is HIDDEN never silently switches that event off (2026-11-09).
		if r.FormValue("shown_auto_thiendo") == "1" {
			u.AutoSkyTrade = r.FormValue("auto_thiendo") == "on"
		}
		if r.FormValue("shown_auto_thiendo_toc") == "1" {
			u.AutoSkyTradeSpeed = r.FormValue("auto_thiendo_toc") == "on"
		}
		if r.FormValue("shown_auto_luanhoi") == "1" {
			u.AutoRebornTrial = r.FormValue("auto_luanhoi") == "on"
		}
		if r.FormValue("shown_auto_tiencanh") == "1" {
			u.AutoTienCanh = r.FormValue("auto_tiencanh") == "on"
		}
		if r.FormValue("shown_auto_thuongminh") == "1" {
			u.AutoSeaWar = r.FormValue("auto_thuongminh") == "on"
		}
		// 🤝 Event delegation. Validate BEFORE the save so a bad chat id aborts
		// the whole POST (never half-persist a settings change). "" clears.
		oldDeputy := u.DeputyOf
		dTarget, dMsg := deputyValidateTarget(t.ydb.GetDB(), fmt.Sprintf("%d", chatID), r.FormValue("deputy_of"))
		if dMsg != "" {
			t.webRedirectMsg(w, r, "/settings", dMsg)
			return
		}
		u.DeputyOf = dTarget
		if err := model.CreateUser(t.ydb.GetDB(), u); err != nil {
			t.webRedirectMsg(w, r, "/settings", "!!lỗi server, không lưu được")
			return
		}
		if oldDeputy != dTarget {
			if dTarget != "" {
				if lid, err := strconv.ParseInt(dTarget, 10, 64); err == nil {
					t.pushNotify(lid, fmt.Sprintf("🤝 %s (#%d) vừa uỷ thác acc này cho bạn CHẠY EVENT — nó hiện ở tab Sự Kiện của bạn (force login/logout + lệnh event, không cần mật khẩu, không thấy ở tab Đệ Tử).", u.Username, chatID))
				}
			} else if oldDeputy != "" {
				if pid, err := strconv.ParseInt(oldDeputy, 10, 64); err == nil {
					t.pushNotify(pid, fmt.Sprintf("🤝 %s (#%d) đã THU HỒI uỷ thác — acc không còn ở tab Sự Kiện của bạn.", u.Username, chatID))
				}
			}
		}
		if v, ok := t.session.Load(chatID); ok {
			if p, ok := v.(*Player); ok && p.Running {
				p.ChuotMinDao = u.MinPeachLevel
				p.ChuotMinBinh = u.MinBottleLevel
				p.ChuotMinLuuly = u.MinLuuLyLevel
				p.BottlePrefered = u.BottlePrefered
				// Event gates live on the Player object — sync them now so
				// the toggles apply to the running loops this save tick.
				p.AutoGather = u.AutoGather
				p.AutoSkyTrade = u.AutoSkyTrade
				p.AutoSkyTradeSpeed = u.AutoSkyTradeSpeed
				p.AutoRebornTrial = u.AutoRebornTrial
				p.AutoTienCanh = u.AutoTienCanh
				p.AutoSeaWar = u.AutoSeaWar
			}
		}
		t.webRedirectMsg(w, r, "/settings", "Đã lưu cài đặt.")
		return
	}
	t.webRender(w, r, "settings", chatID, u, webCSRFOf(r), map[string]any{
		"Open": t.webOpenEventKeys(chatID),
	})
}

// webSchedules is GET (4 CSV inputs) / POST (validate + store; the minute
// tickers re-read the DB, so nothing needs re-arming).
func (t *TeleBot) webSchedules(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	if r.Method == http.MethodPost {
		// The leader's member schedule is a SEPARATE form (member_form=1):
		// the bot's /member_schedule_* commands set every member at once, and
		// mixing them into the own-schedule form would make "save mine" wipe
		// the members' schedules (missing field == clear). Same for the
		// reverse: this branch must not touch the leader's own rows.
		if r.FormValue("member_form") == "1" {
			t.webMemberSchedulesSave(w, r, chatID, u)
			return
		}
		if r.FormValue("member_form") == "2" {
			t.webMemberSchedulesPerSave(w, r, chatID, u)
			return
		}
		fields := []struct {
			key  string
			set  func(string) error
			name string
		}{
			{"schedule_start_chuot", func(csv string) error {
				return model.SetChuotStartSchedule(t.ydb.GetDB(), fmt.Sprintf("%d", chatID), csv)
			}, "bật chuột"},
			{"schedule_stop_chuot", func(csv string) error {
				return model.SetChuotStopSchedule(t.ydb.GetDB(), fmt.Sprintf("%d", chatID), csv)
			}, "tắt chuột"},
			{"schedule_login", func(csv string) error { return model.SetLoginSchedule(t.ydb.GetDB(), fmt.Sprintf("%d", chatID), csv) }, "đăng nhập"},
			{"schedule_logout", func(csv string) error { return model.SetLogoutSchedule(t.ydb.GetDB(), fmt.Sprintf("%d", chatID), csv) }, "đăng xuất"},
		}
		for _, f := range fields {
			raw := strings.TrimSpace(r.FormValue(f.key))
			if raw == "" || isChuotScheduleClearKeyword(raw) {
				if err := f.set(""); err != nil {
					t.webRedirectMsg(w, r, "/schedules", "!!lỗi server khi xoá lịch "+f.name)
					return
				}
				continue
			}
			csv, err := parseChuotTimes(strings.Fields(strings.ReplaceAll(raw, ",", " ")))
			if err != nil {
				t.webRedirectMsg(w, r, "/schedules", "!!lịch "+f.name+": "+err.Error())
				return
			}
			if err := f.set(csv); err != nil {
				t.webRedirectMsg(w, r, "/schedules", "!!lỗi server khi lưu lịch "+f.name)
				return
			}
		}
		t.webRedirectMsg(w, r, "/schedules", "Đã lưu lịch biểu.")
		return
	}
	data := map[string]any{
		"StartChuot": formatChuotTimes(u.ChuotStartSchedule),
		"StopChuot":  formatChuotTimes(u.ChuotStopSchedule),
		"Login":      formatChuotTimes(u.LoginSchedule),
		"Logout":     formatChuotTimes(u.LogoutSchedule),
	}
	if u.IsLeader {
		members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", chatID))
		if err != nil {
			log.Printf("[web] member schedules %d: %v", chatID, err)
			members = nil
		}
		// The commands set every member at once, so normally they all share
		// one value; show it, or say so when they drifted apart.
		uniform := func(get func(*model.User) string) (string, string) {
			var first string
			for i := range members {
				v := get(&members[i])
				if i == 0 {
					first = v
					continue
				}
				if v != first {
					return "khác nhau", ""
				}
			}
			return formatChuotTimes(first), first
		}
		startTxt, startRaw := uniform(func(m *model.User) string { return m.ChuotStartSchedule })
		stopTxt, stopRaw := uniform(func(m *model.User) string { return m.ChuotStopSchedule })
		loginTxt, loginRaw := uniform(func(m *model.User) string { return m.LoginSchedule })
		logoutTxt, logoutRaw := uniform(func(m *model.User) string { return m.LogoutSchedule })
		data["MemberCount"] = len(members)
		data["MemberStartChuot"] = startTxt
		data["MemberStopChuot"] = stopTxt
		data["MemberStartChuotRaw"] = startRaw
		data["MemberStopChuotRaw"] = stopRaw
		data["MemberLogin"] = loginTxt
		data["MemberLogout"] = logoutTxt
		data["MemberLoginRaw"] = loginRaw
		data["MemberLogoutRaw"] = logoutRaw
		// Per-member table (member_form=2): every row prefilled with THAT
		// member's current raw CSV so the page is WYSIWYG.
		type memSchedRow struct {
			ID, Username               string
			Start, Stop, Login, Logout string
		}
		rows := make([]memSchedRow, 0, len(members))
		for i := range members {
			m := &members[i]
			rows = append(rows, memSchedRow{ID: m.ID, Username: m.Username,
				Start: m.ChuotStartSchedule, Stop: m.ChuotStopSchedule,
				Login: m.LoginSchedule, Logout: m.LogoutSchedule})
		}
		data["Members"] = rows
	}
	t.webRender(w, r, "schedules", chatID, u, webCSRFOf(r), data)
}

// webMemberSchedulesSave is the web twin of handleMemberChuotSchedule and
// handleMemberLoginSchedule: one value written to every member of the leader
// (empty/off clears them all).
func (t *TeleBot) webMemberSchedulesSave(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	if !u.IsLeader {
		t.webRedirectMsg(w, r, "/schedules", "!!Chỉ trưởng bang mới có lịch đệ tử.")
		return
	}
	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", chatID))
	if err != nil {
		t.webRedirectMsg(w, r, "/schedules", "!!lỗi server khi tải danh sách đệ tử")
		return
	}
	if len(members) == 0 {
		t.webRedirectMsg(w, r, "/schedules", "!!Chưa có đệ tử nào để đặt lịch.")
		return
	}
	db := t.ydb.GetDB()
	type mfield struct {
		key  string
		set  func(id, csv string) error
		name string
		csv  string
	}
	fields := []mfield{
		{key: "member_schedule_start_chuot", set: func(id, csv string) error { return model.SetChuotStartSchedule(db, id, csv) }, name: "bật chuột"},
		{key: "member_schedule_stop_chuot", set: func(id, csv string) error { return model.SetChuotStopSchedule(db, id, csv) }, name: "tắt chuột"},
		{key: "member_schedule_login", set: func(id, csv string) error { return model.SetLoginSchedule(db, id, csv) }, name: "đăng nhập"},
		{key: "member_schedule_logout", set: func(id, csv string) error { return model.SetLogoutSchedule(db, id, csv) }, name: "đăng xuất"},
	}
	// Validate everything before writing anything (no half-applied save).
	for i := range fields {
		raw := strings.TrimSpace(r.FormValue(fields[i].key))
		if raw == "" || isChuotScheduleClearKeyword(raw) {
			continue
		}
		csv, err := parseChuotTimes(strings.Fields(strings.ReplaceAll(raw, ",", " ")))
		if err != nil {
			t.webRedirectMsg(w, r, "/schedules", "!!lịch "+fields[i].name+" cho đệ tử: "+err.Error())
			return
		}
		fields[i].csv = csv
	}
	for _, f := range fields {
		for i := range members {
			if err := f.set(members[i].ID, f.csv); err != nil {
				t.webRedirectMsg(w, r, "/schedules", "!!lỗi server khi lưu lịch "+f.name)
				return
			}
		}
	}
	t.webRedirectMsg(w, r, "/schedules", fmt.Sprintf("Đã lưu lịch cho %d đệ tử.", len(members)))
}

// webMemberSchedulesPerSave is the per-member schedule table save
// (member_form=2): inputs ms_<chatid>_{start|stop|login|logout}, each
// prefilled with that member's CURRENT value. Unlike the "set for all" bulk
// form, BLANK here means CLEAR that row — the leader edits WYSIWYG. Values
// equal to what is stored are skipped; everything is validated before the
// first write so one bad time never half-applies the table.
func (t *TeleBot) webMemberSchedulesPerSave(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	if !u.IsLeader {
		t.webRedirectMsg(w, r, "/schedules", "!!Chỉ trưởng bang mới có lịch đệ tử.")
		return
	}
	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", chatID))
	if err != nil {
		t.webRedirectMsg(w, r, "/schedules", "!!lỗi server khi tải danh sách đệ tử")
		return
	}
	db := t.ydb.GetDB()
	fields := []struct {
		suffix, name string
		get          func(*model.User) string
		set          func(id, csv string) error
	}{
		{"start", "bật chuột", func(m *model.User) string { return m.ChuotStartSchedule }, func(id, csv string) error { return model.SetChuotStartSchedule(db, id, csv) }},
		{"stop", "tắt chuột", func(m *model.User) string { return m.ChuotStopSchedule }, func(id, csv string) error { return model.SetChuotStopSchedule(db, id, csv) }},
		{"login", "đăng nhập", func(m *model.User) string { return m.LoginSchedule }, func(id, csv string) error { return model.SetLoginSchedule(db, id, csv) }},
		{"logout", "đăng xuất", func(m *model.User) string { return m.LogoutSchedule }, func(id, csv string) error { return model.SetLogoutSchedule(db, id, csv) }},
	}
	type wr struct {
		id, who, label string
		f              func(id, csv string) error
		csv            string
	}
	var writes []wr
	for i := range members {
		m := &members[i]
		for fi := range fields {
			f := &fields[fi]
			vals, ok := r.Form["ms_"+m.ID+"_"+f.suffix]
			if !ok {
				continue // row not sent (page older than member list) — leave alone
			}
			v := strings.TrimSpace(vals[0])
			csv := ""
			if v != "" && !isChuotScheduleClearKeyword(v) {
				parsed, perr := parseChuotTimes(strings.Fields(strings.ReplaceAll(v, ",", " ")))
				if perr != nil {
					t.webRedirectMsg(w, r, "/schedules", "!!lịch "+f.name+" của "+m.Username+": "+perr.Error())
					return
				}
				csv = parsed
			}
			if csv == f.get(m) {
				continue
			}
			writes = append(writes, wr{id: m.ID, who: m.Username, label: f.name, f: f.set, csv: csv})
		}
	}
	for _, p := range writes {
		if err := p.f(p.id, p.csv); err != nil {
			t.webRedirectMsg(w, r, "/schedules", "!!lỗi server khi lưu lịch "+p.label+" của "+p.who)
			return
		}
	}
	t.webRedirectMsg(w, r, "/schedules", fmt.Sprintf("Đã lưu lịch từng đệ tử (%d thay đổi).", len(writes)))
}

// webRedeem is POST-only: self-redeem a gift code (same core as /redeem).
func (t *TeleBot) webRedeem(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	if r.Method != http.MethodPost {
		t.webRedirectMsg(w, r, "/", "")
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	if code == "" {
		t.webRedirectMsg(w, r, "/", "!!nhập mã giftcode trước đã")
		return
	}
	if err := model.RedeemGiftCode(t.ydb.GetDB(), code, fmt.Sprintf("%d", chatID)); err != nil {
		t.webRedirectMsg(w, r, "/", "!!"+err.Error())
		return
	}
	t.pushWebState(chatID) // the VIP line updates live
	t.webRedirectMsg(w, r, "/", "oke đạo hữu, + 30 ngày VIP")
}

// webToggle is POST-only: one runtime switch, named exactly like the chat
// commands (chuot_on … chatcay_xave_on). Refuses without a running session,
// same as the chat handlers. No-reload replies ride the generic live branch
// of webRedirectMsg (see web.go).
func (t *TeleBot) webToggle(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	if r.Method != http.MethodPost {
		t.webRedirectMsg(w, r, "/", "")
		return
	}
	feat := r.FormValue("feat")
	v, ok := t.session.Load(chatID)
	p, ok := v.(*Player)
	if !ok || !p.Running {
		t.webRedirectMsg(w, r, "/", "!!login đã đạo hữu")
		return
	}
	switch feat {
	case "chuot_on":
		p.ChuotRun = true
	case "chuot_off":
		p.ChuotRun = false
	case "notify_on":
		p.Notify = true
	case "notify_off":
		p.Notify = false
	case "maohiem_on":
		p.Maohiem = true
	case "maohiem_off":
		p.Maohiem = false
	case "lucdao_on":
		p.Lucdao = true
	case "lucdao_off":
		p.Lucdao = false
	case "yeuthap_on":
		p.Yeuthap = true
	case "yeuthap_off":
		p.Yeuthap = false
	case "huyetmach_on":
		p.HuyetMach = true
	case "huyetmach_off":
		p.HuyetMach = false
	case "chatcay_on":
		p.Peach = true
		p.XaVe = false
	case "chatcay_xave_on":
		p.Peach = true
		p.XaVe = true
	case "chatcay_off":
		p.Peach = false
		p.XaVe = false
	case "taydu_on":
		// Same arm as /start_taydu: bật toggle + clear mọi park/stop.
		armXiyou(p)
	case "taydu_off":
		p.Xiyou = false
	case "thanlong_run":
		// One-shot engine (no off): the switch FIRES a run; refusals return
		// the chat command's own honest messages.
		started, busy := p.cbStartOnce()
		switch {
		case started:
		case busy:
			t.webRedirectMsg(w, r, "/", "!!Luyện Khí: đang có thao tác chờ server, đạo hữu đợi chút rồi bấm lại nhé")
			return
		default:
			t.webRedirectMsg(w, r, "/", "!!Ván Luyện Khí đang chạy rồi, đạo hữu đợi kết quả thông báo nhé")
			return
		}
	default:
		t.webRedirectMsg(w, r, "/", "!!cài đặt không tồn tại")
		return
	}
	// Push immediately: other open tabs (or the same one on reconnect) see
	// the switch flip without waiting for the 5 s tick.
	t.pushWebState(chatID)
	t.webRedirectMsg(w, r, "/", "okey đạo hữu")
}
