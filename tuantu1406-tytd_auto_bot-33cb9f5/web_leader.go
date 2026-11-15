package tytd_go

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"
)

// Leader-tier pages: member roster and the event fleet. All fleet orders
// reach only the leader's own event users — the same GetEventUsersByLeader →
// session.Load(realUID) → *EventPlayer enumeration that eventOrderBroadcast
// uses — and drive the same GodIsland* methods.

// webMembers is GET (roster) / POST (add | remove | password | autologin |
// config | config_all | login | login_all | logout_all | chuot_on_all |
// chuot_off_all | chatcay_on_all | chatcay_xave_on_all | taydu_on_all |
// taydu_off_all | thanlong_on_all | lkstart | lkstart_all | rttoggle).
// "config" is the
// per-member full config panel (creds/thresholds/preferences/schedules);
// "rttoggle" flips one dashboard runtime switch on that member's live
// session (feat names identical to /toggle).
func (t *TeleBot) webMembers(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	leaderID := fmt.Sprintf("%d", chatID)
	if r.Method == http.MethodPost {
		msg := t.webMemberAction(r, chatID, leaderID)
		t.webRedirectMsg(w, r, "/members", msg)
		return
	}
	members, err := model.GetMembersByLeader(t.ydb.GetDB(), leaderID)
	if err != nil {
		t.webRedirectMsg(w, r, "/", "!!không tải được danh sách đệ tử")
		return
	}
	type row struct {
		M            model.User
		Running      bool
		OpenLK       bool            // Luyện Khí (type 119) open on the live session
		OpenTayDu    bool            // Tây Du (311) season open on THIS member's session
		OpenThanLong bool            // Thần Long ComposeBall (130) season open
		T            map[string]bool // live runtime switches (same keys as the dashboard)
	}
	var rows []row // nil when empty: the template's "no members" branch keys on it
	anyLK := false // ≥1 online member has the 119 window open → bulk 🗡 button
	nowAct := time.Now()
	for _, m := range members {
		rn := false
		lk := false
		var td, tl bool
		var tg map[string]bool
		if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
			if v, ok := t.session.Load(id); ok {
				rn = sessionRunning(v)
				if p, ok := v.(*Player); ok && rn {
					if _, err := p.csActiveID(); err == nil {
						lk = true
						anyLK = true
					}
					// Per-member on purpose: members may hang on different
					// game servers, each with its own activity announcements
					// (owner 2026-11-09: engine switches only show while the
					// activity is actually running for that account).
					td = webActOpen(p, 311, nowAct)
					tl = webActOpen(p, 130, nowAct)
				}
			}
			tg = t.webTogglesOf(id) // all-false when no running session
		}
		rows = append(rows, row{M: m, Running: rn, OpenLK: lk, OpenTayDu: td, OpenThanLong: tl, T: tg})
	}
	t.webRender(w, r, "members", chatID, u, webCSRFOf(r), map[string]any{
		"Rows": rows, "Open": t.webOpenEventKeys(chatID), "AnyLK": anyLK,
	})
}

// applyMemberEventToggles parses the five event opt-in switches off r onto m
// — but only rows whose shown_<key>=1 marker is present: the template hides
// an event's row while the server hasn't opened that season, and a save with
// a hidden row must never silently switch the event off (owner 2026-11-09).
func applyMemberEventToggles(m *model.User, r *http.Request) {
	if r.FormValue("shown_auto_thiendo") == "1" {
		m.AutoSkyTrade = r.FormValue("auto_thiendo") == "on"
	}
	if r.FormValue("shown_auto_thiendo_toc") == "1" {
		m.AutoSkyTradeSpeed = r.FormValue("auto_thiendo_toc") == "on"
	}
	if r.FormValue("shown_auto_luanhoi") == "1" {
		m.AutoRebornTrial = r.FormValue("auto_luanhoi") == "on"
	}
	if r.FormValue("shown_auto_tiencanh") == "1" {
		m.AutoTienCanh = r.FormValue("auto_tiencanh") == "on"
	}
	if r.FormValue("shown_auto_thuongminh") == "1" {
		m.AutoSeaWar = r.FormValue("auto_thuongminh") == "on"
	}
}

// applyEventOffSwitches parses the tab-Sự kiện off-switches (Luân Hồi /
// Thương Minh) off r onto ev — marker-gated like the member rows, with
// INVERTED semantics: a rendered-but-unchecked box means OFF (the row's
// default is running, "login là chạy"). Returns true when at least one
// marker was present (something may have changed → worth a Save).
func applyEventOffSwitches(ev *model.EventUser, r *http.Request) bool {
	touched := false
	if r.FormValue("shown_auto_luanhoi") == "1" {
		ev.OffRebornTrial = r.FormValue("auto_luanhoi") != "on"
		touched = true
	}
	if r.FormValue("shown_auto_thuongminh") == "1" {
		ev.OffSeaWar = r.FormValue("auto_thuongminh") != "on"
		touched = true
	}
	return touched
}

// patchMemberLivePlayer syncs the just-saved config onto the member's live
// session (same fields as the member's own /settings sync block — event gates
// live on the Player object, owner 2026-10-27 [[tytd-player-object-gates]]).
// No-op when the account is not resident.
func (t *TeleBot) patchMemberLivePlayer(memberID string, m *model.User) {
	id, err := strconv.ParseInt(memberID, 10, 64)
	if err != nil {
		return
	}
	v, ok := t.session.Load(id)
	if !ok {
		return
	}
	if p, ok := v.(*Player); ok {
		p.ChuotMinDao = m.MinPeachLevel
		p.ChuotMinBinh = m.MinBottleLevel
		p.ChuotMinLuuly = m.MinLuuLyLevel
		p.BottlePrefered = m.BottlePrefered
		p.AutoGather = m.AutoGather
		p.AutoSkyTrade = m.AutoSkyTrade
		p.AutoSkyTradeSpeed = m.AutoSkyTradeSpeed
		p.AutoRebornTrial = m.AutoRebornTrial
		p.AutoTienCanh = m.AutoTienCanh
		p.AutoSeaWar = m.AutoSeaWar
	}
}

// bulkMemberConfig applies the shared-field part of the per-member config
// form (thresholds, preferences, marker-gated event rows — NO credentials,
// schedules or deputy_of) to every member of leaderID. One save per member;
// live sessions are patched as we go. Returns the number applied.
func (t *TeleBot) bulkMemberConfig(r *http.Request, leaderID string) (int, string) {
	db := t.ydb.GetDB()
	members, err := model.GetMembersByLeader(db, leaderID)
	if err != nil {
		return 0, "!!không tải được danh sách đệ tử"
	}
	// Validate ALL numbers before touching any row (one bad field aborts
	// everything — same all-or-nothing law as the per-member panel).
	var minDao, minBinh, minLuuly int
	for _, f := range []struct {
		key string
		dst *int
	}{
		{"min_dao", &minDao},
		{"min_binh", &minBinh},
		{"min_luuly", &minLuuly},
	} {
		n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(f.key)))
		if err != nil || n < 0 {
			return 0, "!!" + f.key + " phải là số từ 0 trở lên"
		}
		*f.dst = n
	}
	var gatherFloor int64
	if s := strings.TrimSpace(r.FormValue("min_tuling")); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			return 0, "!!min_tuling phải là số từ 0 trở lên"
		}
		gatherFloor = n
	}
	applied := 0
	for _, m := range members {
		m.MinPeachLevel = minDao
		m.MinBottleLevel = minBinh
		m.MinLuuLyLevel = minLuuly
		if strings.TrimSpace(r.FormValue("min_tuling")) != "" {
			m.MinGatherIncome = gatherFloor
		}
		m.BottlePrefered = r.FormValue("bottle_pref") == "on"
		m.AutoLogin = r.FormValue("auto_login") == "on"
		m.AutoMouse = r.FormValue("auto_start_chuot") == "on"
		m.AutoGather = r.FormValue("tu_linh") == "on"
		applyMemberEventToggles(&m, r)
		if err := model.CreateUser(db, &m); err != nil {
			return applied, "!!không lưu được cấu hình cho " + m.Username + ": " + err.Error()
		}
		t.patchMemberLivePlayer(m.ID, &m)
		applied++
	}
	return applied, ""
}

func (t *TeleBot) webMemberAction(r *http.Request, chatID int64, leaderID string) string {
	db := t.ydb.GetDB()
	memberID := strings.TrimSpace(r.FormValue("member_id"))

	// Per-member actions: verify the row belongs to this leader first —
	// every branch below must pass this ownership gate.
	ownedMember := func() (*model.User, string) {
		m, err := model.GetUser(db, memberID)
		if err != nil {
			return nil, "!!không tìm thấy đệ tử này"
		}
		if m.LeaderID != leaderID {
			return nil, "!!đệ tử này không thuộc quyền quản lý của bạn"
		}
		return m, ""
	}

	switch r.FormValue("action") {
	case "lkstart":
		// Tự chơi Luyện Khí Đại Hội (resume-only; engine never settles/ends).
		m, msg := ownedMember()
		if m == nil {
			return msg
		}
		id, err := strconv.ParseInt(memberID, 10, 64)
		if err != nil {
			return "!!member_id không hợp lệ"
		}
		v, ok := t.session.Load(id)
		p, okPlayer := v.(*Player)
		if !ok || !okPlayer || !p.Running {
			return "!!" + m.Username + " chưa treo game — không tự chơi được"
		}
		return t.lkStartWeb(p, id, chatID)

	case "add":
		username := stripInvisible(r.FormValue("username"))
		password := stripInvisible(r.FormValue("password"))
		sv, err := parseServerID(r.FormValue("server_id"))
		if username == "" || password == "" || err != nil {
			return "!!điền đủ tên đăng nhập / mật khẩu / server khi thêm đệ tử" + serverErrHint(err)
		}
		randomID := model.GenerateRandomUserID(db)
		mu := &model.User{
			ID: randomID, Username: username, Password: password, ServerID: sv,
			VipLevel: 1, VipExpireAt: time.Now().Add(24 * time.Hour),
			AutoLogin: true, AutoMouse: true, AutoGather: true,
			MinBottleLevel: 2, MinPeachLevel: 4, MinLuuLyLevel: 6,
			LeaderID: leaderID,
		}
		if err := model.CreateUser(db, mu); err != nil {
			return "!!không tạo được đệ tử: " + err.Error()
		}
		id, _ := strconv.ParseInt(randomID, 10, 64)
		if !t.startPlayerLogin(id, func(string) {}) {
			return "Đã tạo đệ tử " + randomID + " (chưa đăng nhập được, thử lại sau)"
		}
		return "Đã tạo đệ tử " + randomID + " và đang đăng nhập"

	case "remove":
		m, msg := ownedMember()
		if m == nil {
			return msg
		}
		if id, err := strconv.ParseInt(memberID, 10, 64); err == nil {
			t.stopPlayerForChatID(id) // don't leave an orphan session behind
		}
		if err := model.DeleteUser(db, memberID); err != nil {
			return "!!không xoá được: " + err.Error()
		}
		return "Đã xoá đệ tử " + memberID

	case "login":
		// Force-login ONE member. "Force" = a stale/hung session is kicked
		// out first so the login always starts from a clean slate (plain
		// startPlayerLogin would refuse with "chay 1 lan` thoi"). The
		// server-protection gates stay: ban/VIP, the 3-minute login-fail
		// cooldown, and the max-online cap — the web already throttles
		// frantic re-clicks client-side, and the game server dislikes being
		// re-hit inside a cooldown more than the leader does.
		m, msg := ownedMember()
		if m == nil {
			return msg
		}
		id, err := strconv.ParseInt(memberID, 10, 64)
		if err != nil {
			return "!!đệ tử này không có mã số hợp lệ"
		}
		if model.IsBanned(db, id) || m.VipLevel == 0 || m.VipExpireAt.Before(time.Now()) {
			return "!!đệ tử này bị khóa hoặc hết hạn VIP, không đăng nhập được"
		}
		if !m.LastLoginFail.IsZero() && time.Since(m.LastLoginFail) < 3*time.Minute {
			return fmt.Sprintf("!!đệ tử vừa đăng nhập fail, đợi %s rồi bấm lại",
				(3*time.Minute - time.Since(m.LastLoginFail)).Round(time.Second))
		}
		if t.getRunningCount() >= t.getMaxPlayers() {
			return "!!server đang đầy, thử lại sau"
		}
		t.stopPlayerForChatID(id)
		var block string
		if t.startPlayerLogin(id, func(s string) { block = s }) {
			return "Đang đăng nhập đệ tử " + m.Username + "…"
		}
		if block != "" {
			return "!!" + block
		}
		return "!!không đăng nhập được đệ tử này, thử lại sau"

	case "password":
		m, msg := ownedMember()
		if m == nil {
			return msg
		}
		pw := r.FormValue("password")
		if pw == "" {
			return "!!mật khẩu mới không được rỗng"
		}
		m.Password = pw
		if err := model.UpdateUser(db, m); err != nil {
			return "!!không đổi được mật khẩu: " + err.Error()
		}
		if id, err := strconv.ParseInt(memberID, 10, 64); err == nil {
			if v, ok := t.session.Load(id); ok {
				if p, ok := v.(*Player); ok {
					p.Password = pw
				}
			}
		}
		return "Đã đổi mật khẩu cho đệ tử " + memberID

	case "autologin":
		m, msg := ownedMember()
		if m == nil {
			return msg
		}
		m.AutoLogin = !m.AutoLogin
		if err := model.UpdateUser(db, m); err != nil {
			return "!!không đổi được tự động đăng nhập: " + err.Error()
		}
		if m.AutoLogin {
			return "Đã bật tự động đăng nhập cho " + memberID
		}
		return "Đã tắt tự động đăng nhập cho " + memberID

	case "rttoggle":
		// The leader flips one RUNTIME switch on a member's live session —
		// the per-member twin of the dashboard's /toggle (same feat names,
		// same fields on the same *Player). Runtime-only like the main
		// user's: these live on the session, not the DB, and a member who
		// is not hanging gets the same "must be logged in" refusal.
		m, msg := ownedMember()
		if m == nil {
			return msg
		}
		id, err := strconv.ParseInt(memberID, 10, 64)
		if err != nil {
			return "!!đệ tử này không có mã số hợp lệ"
		}
		v, ok := t.session.Load(id)
		p, ok := v.(*Player)
		if !ok || !p.Running {
			return "!!" + m.Username + " chưa treo game — không đổi được công tắc"
		}
		feat := r.FormValue("feat")
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
			armXiyou(p) // same arm as /start_taydu (clear park/stop)
		case "taydu_off":
			p.Xiyou = false
		case "thanlong_run":
			started, busy := p.cbStartOnce()
			if !started {
				if busy {
					return "!!Luyện Khí " + m.Username + ": đang có thao tác chờ server, đợi chút rồi bấm lại"
				}
				return "!!Ván Luyện Khí của " + m.Username + " đang chạy rồi"
			}
			t.pushWebState(id)
			return "Đã cho đệ tử " + m.Username + " chạy 1 ván Luyện Khí"
		default:
			return "!!cài đặt không tồn tại"
		}
		// Same live nudge the dashboard toggle sends, so open tabs refresh.
		t.pushWebState(id)
		return "Đã đổi công tắc của đệ tử " + m.Username

	case "redeem":
		// Leader keys a giftcode on behalf of one of their members — the
		// web twin of Telegram's /redeem <code> <user_id>, but gated to the
		// leader's OWN roster (the chat command trusts the operator; the
		// browser must not). model.RedeemGiftCode owns all rules: one use
		// per member per code, usage cap, VIP extension.
		m, msg := ownedMember()
		if m == nil {
			return msg
		}
		code := strings.TrimSpace(r.FormValue("code"))
		if code == "" {
			return "!!nhập mã giftcode trước đã"
		}
		if err := model.RedeemGiftCode(db, code, memberID); err != nil {
			return "!!" + err.Error()
		}
		return "Đã nạp mã cho đệ tử " + memberID + " — VIP đã gia hạn"

	case "config":
		// Full per-member config (the expandable panel on each roster row):
		// credentials (blank keeps current), the same thresholds/preferences
		// the member can set on their own settings page, and the four
		// schedules. Everything validates in memory first; one Save at the
		// end means a bad schedule can never half-apply the rest.
		m, msg := ownedMember()
		if m == nil {
			return msg
		}
		changed := false
		if un := stripInvisible(r.FormValue("username")); un != "" && un != m.Username {
			m.Username = un
			changed = true
		}
		if pw := stripInvisible(r.FormValue("password")); pw != "" {
			m.Password = pw
			changed = true
		}
		if sv := r.FormValue("server_id"); strings.TrimSpace(sv) != "" {
			n, err := parseServerID(sv)
			if err != nil {
				return "!!server_id không hợp lệ: " + err.Error()
			}
			if n != m.ServerID {
				m.ServerID = n
				changed = true
			}
		}
		if changed {
			m.GameUID = 1 // creds changed → re-login picks them up, same rule as the member's own page
		}
		lv := []struct {
			key string
			dst *int
		}{
			{"min_dao", &m.MinPeachLevel},
			{"min_binh", &m.MinBottleLevel},
			{"min_luuly", &m.MinLuuLyLevel},
		}
		for _, f := range lv {
			n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(f.key)))
			if err != nil || n < 0 {
				return "!!" + f.key + " phải là số từ 0 trở lên"
			}
			*f.dst = n
		}
		// Tụ Linh income floor is int64 — parsed apart from the small ints.
		if s := strings.TrimSpace(r.FormValue("min_tuling")); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || n < 0 {
				return "!!min_tuling phải là số từ 0 trở lên"
			}
			m.MinGatherIncome = n
		}
		m.BottlePrefered = r.FormValue("bottle_pref") == "on"
		m.AutoLogin = r.FormValue("auto_login") == "on"
		m.AutoMouse = r.FormValue("auto_start_chuot") == "on"
		m.AutoGather = r.FormValue("tu_linh") == "on"
		// Event rows render only while open (server ActList); the shown_
		// marker keeps a save with a HIDDEN row from silently switching that
		// event off (2026-11-09, same rule on /settings + tab Sự kiện).
		applyMemberEventToggles(m, r)
		// Schedules use the chat grammar: blank/off clears, otherwise a CSV
		// of H:MM. Written onto the struct (not via Set*Schedule) so the one
		// Save below can't be clobbered by a stale full-row write.
		scheds := []struct {
			key, name string
			dst       *string
		}{
			{"schedule_start_chuot", "bật chuột", &m.ChuotStartSchedule},
			{"schedule_stop_chuot", "tắt chuột", &m.ChuotStopSchedule},
			{"schedule_login", "đăng nhập", &m.LoginSchedule},
			{"schedule_logout", "đăng xuất", &m.LogoutSchedule},
		}
		for _, s := range scheds {
			raw := strings.TrimSpace(r.FormValue(s.key))
			if raw == "" || isChuotScheduleClearKeyword(raw) {
				*s.dst = ""
				continue
			}
			csv, err := parseChuotTimes(strings.Fields(strings.ReplaceAll(raw, ",", " ")))
			if err != nil {
				return "!!lịch " + s.name + " của đệ tử: " + err.Error()
			}
			*s.dst = csv
		}
		// 🤝 Event delegation for THIS member (the owning leader delegates
		// one of their members to another leader). Validated so a bad chat id
		// aborts the whole save; the owner (chatID) chose it, so only the
		// receiving / previous delegate leader is notified.
		oldDeputy := m.DeputyOf
		dTarget, dMsg := deputyValidateTarget(db, m.ID, r.FormValue("deputy_of"))
		if dMsg != "" {
			return dMsg
		}
		m.DeputyOf = dTarget
		if err := model.CreateUser(db, m); err != nil {
			return "!!không lưu được cấu hình: " + err.Error()
		}
		if oldDeputy != dTarget {
			if dTarget != "" {
				if lid, err := strconv.ParseInt(dTarget, 10, 64); err == nil {
					t.pushNotify(lid, fmt.Sprintf("🤝 Leader %s vừa uỷ thác acc %s (#%s) cho bạn CHẠY EVENT — nó hiện ở tab Sự Kiện của bạn (force login/logout + lệnh event, không cần mật khẩu, không thấy ở tab Đệ Tử).", leaderID, m.Username, m.ID))
				}
			} else if oldDeputy != "" {
				if pid, err := strconv.ParseInt(oldDeputy, 10, 64); err == nil {
					t.pushNotify(pid, fmt.Sprintf("🤝 Acc %s (#%s) đã bị thu hồi uỷ thác — không còn ở tab Sự Kiện của bạn.", m.Username, m.ID))
				}
			}
		}
		// Live session keeps serving with the old numbers until patched —
		// same fields the member's own settings page syncs; schedules and
		// auto_login are re-read from the DB by their tickers, but the event
		// opt-in gates live on the Player object (owner 2026-10-27), so this
		// save must sync them here or the running loops ignore the flip.
		if id, err := strconv.ParseInt(memberID, 10, 64); err == nil {
			if v, ok := t.session.Load(id); ok {
				if p, ok := v.(*Player); ok {
					if pw := r.FormValue("password"); pw != "" {
						p.Password = pw
					}
					p.ChuotMinDao = m.MinPeachLevel
					p.ChuotMinBinh = m.MinBottleLevel
					p.ChuotMinLuuly = m.MinLuuLyLevel
					p.BottlePrefered = m.BottlePrefered
					p.AutoGather = m.AutoGather
					p.AutoSkyTrade = m.AutoSkyTrade
					p.AutoSkyTradeSpeed = m.AutoSkyTradeSpeed
					p.AutoRebornTrial = m.AutoRebornTrial
					p.AutoTienCanh = m.AutoTienCanh
					p.AutoSeaWar = m.AutoSeaWar
				}
			}
		}
		return "Đã lưu cấu hình cho đệ tử " + m.Username

	case "config_all":
		// ⚙️ Bulk config for every Đệ Tử at once — the same field set the
		// leader sees on their own /settings (minus credentials/schedules/
		// deputy, owner 2026-11-09 "acc chính có gì thì bulk có cái đó").
		applied, msg := t.bulkMemberConfig(r, leaderID)
		if msg != "" {
			return msg
		}
		if applied == 0 {
			return "Bạn chưa có đệ tử nào"
		}
		return fmt.Sprintf("Đã áp cấu hình cho %d đệ tử.", applied)

	case "login_all", "logout_all", "chuot_on_all", "chuot_off_all", "chatcay_on_all", "chatcay_xave_on_all", "taydu_on_all", "taydu_off_all", "thanlong_on_all", "lkstart_all":
		members, err := model.GetMembersByLeader(db, leaderID)
		if err != nil {
			return "!!không tải được danh sách đệ tử"
		}
		if len(members) == 0 {
			return "Bạn chưa có đệ tử nào"
		}
		act := r.FormValue("action")
		if act == "login_all" {
			started, skipped := 0, 0
			for i, m := range members {
				id, err := strconv.ParseInt(m.ID, 10, 64)
				if err != nil {
					skipped++
					continue
				}
				if v, ok := t.session.Load(id); ok && sessionRunning(v) {
					skipped++
					continue
				}
				if model.IsBanned(db, id) || m.VipLevel == 0 || m.VipExpireAt.Before(time.Now()) {
					skipped++
					continue
				}
				if !m.LastLoginFail.IsZero() && time.Since(m.LastLoginFail) < 3*time.Minute {
					skipped++
					continue
				}
				if t.getRunningCount()+started >= t.getMaxPlayers() {
					skipped += len(members) - i
					break
				}
				if t.startPlayerLogin(id, func(string) {}) {
					started++
				} else {
					skipped++
				}
			}
			return fmt.Sprintf("Đã cho %d đệ tử vào game, %d bỏ qua", started, skipped)
		}
		done, notRunning, busy, closed := 0, 0, 0, 0
		for _, m := range members {
			id, err := strconv.ParseInt(m.ID, 10, 64)
			if err != nil {
				continue
			}
			switch act {
			case "logout_all":
				if t.stopPlayerForChatID(id) {
					done++
				} else {
					notRunning++
				}
			case "lkstart_all":
				// Bulk twin of the per-member 🗡 button (owner 2026-11-11
				// "bấm vào thì sẽ chạy luyện khí cho tất cả member đang
				// online"): same resume-only engine as lkstart — busy accs
				// are counted, not queued; accs whose 119 window is closed
				// are skipped honestly (no blind ENTER).
				v, ok := t.session.Load(id)
				p, okPlayer := v.(*Player)
				if !ok || !okPlayer || !p.Running {
					notRunning++
					continue
				}
				if _, err := p.csActiveID(); err != nil {
					closed++
					continue
				}
				p.CsMu.Lock()
				inFlight := p.csPlaying
				p.CsMu.Unlock()
				if inFlight {
					busy++
					continue
				}
				t.lkStartWeb(p, id, chatID)
				done++
			case "chuot_on_all", "chuot_off_all", "chatcay_on_all", "chatcay_xave_on_all", "taydu_on_all", "taydu_off_all", "thanlong_on_all":
				v, ok := t.session.Load(id)
				p, ok := v.(*Player)
				if !ok || !p.Running {
					notRunning++
					continue
				}
				switch act {
				case "chuot_on_all":
					p.ChuotRun = true
				case "chuot_off_all":
					p.ChuotRun = false
				case "chatcay_on_all":
					p.Peach = true
				case "chatcay_xave_on_all":
					p.Peach = true
					p.XaVe = true
				case "taydu_on_all":
					armXiyou(p) // same arm as /start_taydu (clear park/stop)
				case "taydu_off_all":
					p.Xiyou = false
				case "thanlong_on_all":
					// One-shot engine: skip members whose ván is still in
					// flight instead of queueing a second one behind it.
					if started, _ := p.cbStartOnce(); !started {
						busy++
						continue
					}
					t.pushWebState(id)
				}
				done++
			}
		}
		if act == "lkstart_all" {
			if done == 0 {
				if busy > 0 {
					return fmt.Sprintf("!!%d đệ tử online đều đang có ván Luyện Khí chạy — đợi xong hoặc bấm 🗡 dừng từng acc", busy)
				}
				return "!!chưa đệ tử nào online đang mở Luyện Khí (119)"
			}
			return fmt.Sprintf("⚔️ Đã giao %d đệ tử tự chơi Luyện Khí, %d đang có ván, %d chưa mở sự kiện, %d chưa login",
				done, busy, closed, notRunning)
		}
		if busy > 0 {
			return fmt.Sprintf("Xong %d, %d đang có ván chạy, %d bỏ qua (chưa login)", done, busy, notRunning)
		}
		return fmt.Sprintf("Xong %d, %d bỏ qua (chưa login)", done, notRunning)
	}
	return "!!yêu cầu không hợp lệ"
}

// eventIslandLine composes the island status line of one event account —
// segments ONLY for islands the server has actually opened for that account
// (user 2026-10-06: "đang không mở Bồng Lai thì hiện Bồng Lai làm gì", so even
// Bồng Lai — the one line the roster used to print unconditionally — follows
// the open-gate; login state is the ĐANG TREO pill's job, not the island's).
// THE single source of that text: /events renders it and sseStateOf pushes the
// identical string as evstat, so a live re-paint and a manual reload can never
// disagree. Every status helper is an in-memory session read — safe on the
// 5 s SSE tick. Empty string = no island open at all (the page hides the
// whole segment, separator included).
func (t *TeleBot) eventIslandLine(m model.EventUser) string {
	sb := ""
	add := func(label, v string) {
		if sb != "" {
			sb += " · "
		}
		sb += label + ": " + v
	}
	// Thiên Ma/Cửu U/Thượng Cổ/Côn Lôn already answer ok=false unless the
	// variant is announced for this account; Bồng Lai's helper keeps the
	// Telegram /my_event_members contract (it always answers a line), so the
	// open-gate for it is applied here.
	if p, ok := t.eventSessionOf(m); ok && p.GodIslandEventOpen(GIEventNormal) {
		if v, ok := t.godIslandMemberStatus(m, GIEventNormal); ok {
			add("Bồng Lai", v)
		}
	}
	if v, ok := t.godIslandMemberStatus(m, GIEventPeak); ok {
		add("Thiên Ma", v)
	}
	if v, ok := t.holyLandMemberStatus(m); ok {
		add("Cửu U Tranh Bá", v)
	}
	if v, ok := t.ballGVGMemberStatus(m); ok {
		add("Thượng Cổ", v)
	}
	if v, ok := t.kunlunMemberStatus(m); ok {
		add("Côn Lôn", v)
	}
	if v, ok := t.bacminhMemberStatus(m); ok {
		add("Bắc Minh", v)
	}
	return sb
}

// eventFleetRow is one Sự kiện roster line: either the leader's own event_user
// (Usr nil) or an account that DELEGATED itself here for events (Deputy true,
// Usr is its live users row). The Ev projection exists so the island-status
// helpers (which read only m.ID/m.Running) and the events template keep working
// unchanged for BOTH kinds.
type eventFleetRow struct {
	Ev     model.EventUser
	Usr    *model.User
	Deputy bool
}

// eventFleetOf is the leader's Sự kiện roster = their OWN event_users PLUS every
// account whose deputy_of == leaderID (regular users or ANOTHER leader's
// members). Delegated accounts appear HERE and only here — never via
// GetMembersByLeader, so the delegate's Đệ tử tab stays clean (2026-10-07).
func (t *TeleBot) eventFleetOf(leaderID string) ([]eventFleetRow, error) {
	db := t.ydb.GetDB()
	evs, err := model.GetEventUsersByLeader(db, leaderID)
	if err != nil {
		return nil, err
	}
	out := make([]eventFleetRow, 0, len(evs))
	for _, e := range evs {
		out = append(out, eventFleetRow{Ev: e})
	}
	deps, err := model.GetDelegatedUsers(db, leaderID)
	if err != nil {
		return out, nil // own event fleet is still valid without the delegates
	}
	for i := range deps {
		u := &deps[i]
		out = append(out, eventFleetRow{
			Ev:     model.EventUser{ID: u.ID, Username: u.Username, IngameName: u.IngameName, ServerID: u.ServerID, Running: u.Running},
			Usr:    u,
			Deputy: true,
		})
	}
	return out, nil
}

// eventSessionOf resolves an event user row to its live *EventPlayer session
// (no Running check: open-gating only reads memory).
func (t *TeleBot) eventSessionOf(m model.EventUser) (*EventPlayer, bool) {
	if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
		if v, ok := t.session.Load(id); ok {
			if p, ok := v.(*EventPlayer); ok {
				return p, true
			}
		}
	}
	return nil, false
}

// webEvents is GET (fleet roster + status) / POST (add | remove | login_all |
// logout_all | config | config_all | eat_all). God Island orders live on their
// own POST endpoint.
func (t *TeleBot) webEvents(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	leaderID := fmt.Sprintf("%d", chatID)
	if r.Method == http.MethodPost {
		msg := t.webEventAction(r, chatID, leaderID)
		t.webRedirectMsg(w, r, "/events", msg)
		return
	}
	fleet, err := t.eventFleetOf(leaderID)
	if err != nil {
		t.webRedirectMsg(w, r, "/", "!!không tải được danh sách acc sự kiện")
		return
	}
	type row struct {
		M          model.EventUser
		Running    bool
		Deputy     bool   // event-only delegation (users row), not a real event_user — no Xoá button
		Isl        string // composed island status line (eventIslandLine) — the SSE evstat frame re-paints the very same text in place
		OpenNormal bool   // server announced Bồng Lai for a resident session
		OpenPeak   bool   // server announced Thiên Ma for a resident session
		OpenHoly   bool   // server opened Holy Land for a resident session
		OpenTc     bool   // server announced Ball GVG for a resident session
	}
	var rows []row
	onlineNormal, onlinePeak, onlineHoly, onlineTc, onlineSky, onlineKu, onlineBm, onlineLh, onlineSwWar, active := false, false, false, false, false, false, false, false, false, 0
	var holyStartMs int64
	for _, fr := range fleet {
		m := fr.Ev
		rn := false
		var openN, openP, openH, openT, openK, openBm, openSw bool
		if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
			if v, ok := t.session.Load(id); ok {
				rn = sessionRunning(v)
				if p, ok := v.(*EventPlayer); ok {
					openN = p.GodIslandEventOpen(GIEventNormal)
					openP = p.GodIslandEventOpen(GIEventPeak)
					openH = p.HolyLandEventOpen()
					if soon := p.HolyLandEventUpcoming(); soon > 0 && (holyStartMs == 0 || soon < holyStartMs) {
						holyStartMs = soon
					}
					openT = p.BallGVGEventOpen()
					// Côn Lôn counts from event sessions too — a fleet
					// with no regular account logged in would otherwise
					// never see the card (no kunlun logs at all, bug
					// report 2026-10-05). Same rule for Đỉnh Bắc Minh.
					openK = p.KunlunWarEventOpen()
					openBm = p.BacMinhWarEventOpen()
					// Thương Minh counts from event sessions too — the war
					// lives on the EVENT accounts; a leader whose regular
					// seat never opened 349 must still see the card
					// (same rule as the kunlun bug 2026-10-05).
					if _, act := p.swActiveActivity(); act {
						openSw = true
					}
				}
			}
		}
		island := t.eventIslandLine(m)
		if rn {
			active++
		}
		// An island counts as online only when one of OUR event accounts
		// currently has that variant announced by the server — no logged-in
		// spectator, no island section (the page hides it by default).
		onlineNormal = onlineNormal || openN
		onlinePeak = onlinePeak || openP
		onlineHoly = onlineHoly || openH
		onlineTc = onlineTc || openT
		onlineKu = onlineKu || openK
		onlineBm = onlineBm || openBm
		onlineSwWar = onlineSwWar || openSw
		rows = append(rows, row{M: m, Running: rn, Deputy: fr.Deputy, Isl: island, OpenNormal: openN, OpenPeak: openP, OpenHoly: openH, OpenTc: openT})
	}
	// A running event counts as online if ANY session of this leader's fleet
	// was told about it — event accounts only know while logged in, and the
	// map streamer never answers from those anyway (leader/member sessions
	// serve the pulls while the event accounts fight).
	addFleet := func(v any) {
		p, ok := v.(*Player)
		if !ok || !p.Running {
			return
		}
		onlineNormal = onlineNormal || playerEventOpen(p, gimap.StreamBonglai)
		onlinePeak = onlinePeak || playerEventOpen(p, gimap.StreamThienma)
		onlineHoly = onlineHoly || playerEventOpen(p, gimap.StreamCuuu)
		onlineTc = onlineTc || playerEventOpen(p, gimap.StreamDitich)
		onlineSky = onlineSky || playerEventOpen(p, gimap.StreamThuongdo)
		onlineKu = onlineKu || playerEventOpen(p, gimap.StreamKunlun)
		onlineBm = onlineBm || playerEventOpen(p, gimap.StreamBacMinh)
		onlineLh = onlineLh || playerEventOpen(p, gimap.StreamLuanHoi)
		onlineSwWar = onlineSwWar || playerEventOpen(p, gimap.StreamSeaWarWar)
		if soon := holyLandEventUpcoming(p); soon > 0 && (holyStartMs == 0 || soon < holyStartMs) {
			holyStartMs = soon
		}
	}
	if v, ok := t.session.Load(chatID); ok {
		addFleet(v)
	}
	if ms, err := model.GetMembersByLeader(t.ydb.GetDB(), leaderID); err == nil {
		for _, m := range ms {
			if id, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
				if v, ok := t.session.Load(id); ok {
					addFleet(v)
				}
			}
		}
	}
	// Default view = the ACTIVE fleet; resting accounts are hidden until
	// ?all=1 (the page links the toggle itself).
	showAll := r.URL.Query().Get("all") == "1"
	shown := rows
	if !showAll {
		shown = nil
		for _, rw := range rows {
			if rw.Running {
				shown = append(shown, rw)
			}
		}
	}
	t.webRender(w, r, "events", chatID, u, webCSRFOf(r), map[string]any{
		"Rows": shown, "ShowAll": showAll, "Open": t.webOpenEventKeys(chatID),
		"Total": len(rows), "Active": active,
		"OnlineNormal": onlineNormal, "OnlinePeak": onlinePeak, "OnlineHoly": onlineHoly, "OnlineTc": onlineTc, "OnlineSky": onlineSky, "OnlineKu": onlineKu, "OnlineBm": onlineBm, "OnlineLh": onlineLh, "OnlineSwWar": onlineSwWar, "HolySoon": holyStartMs > 0, "HolyStartMs": holyStartMs, "HolyStartICT": holyStartICT(holyStartMs),
	})
}

// holyStartICT is the countdown card's human line — Vietnam time, empty
// when there is no scheduled start.
func holyStartICT(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).In(ictZone).Format("15:04 ngày 02/01")
}

func (t *TeleBot) webEventAction(r *http.Request, chatID int64, leaderID string) string {
	db := t.ydb.GetDB()
	notify := func(string) {} // web feedback comes via the flash below + Telegram notices

	switch r.FormValue("action") {
	case "add":
		username := stripInvisible(r.FormValue("username"))
		password := stripInvisible(r.FormValue("password"))
		sv, err := parseServerID(r.FormValue("server_id"))
		if username == "" || password == "" || err != nil {
			return "!!điền đủ tên đăng nhập / mật khẩu / server khi thêm acc sự kiện" + serverErrHint(err)
		}
		randomID := model.GenerateRandomEventUserID(db)
		eu := &model.EventUser{
			ID: randomID, Username: username, Password: password,
			ServerID: sv, LeaderID: leaderID,
		}
		if err := model.CreateEventUser(db, eu); err != nil {
			return "!!không tạo được acc sự kiện: " + err.Error()
		}
		if !t.startEventPlayerLogin(randomID, chatID, notify) {
			return "Đã tạo acc sự kiện " + randomID + " (chưa đăng nhập được, thử lại sau)"
		}
		return "Đã tạo acc sự kiện " + randomID + " và đang đăng nhập"

	case "remove":
		memberID := strings.TrimSpace(r.FormValue("member_id"))
		// A delegated account is NOT this leader's to delete — it lives in the
		// users table and belongs to its owner. Only real event_users can go.
		if _, ok := t.getOwnedEventUser(memberID, chatID); !ok {
			return "!!không tìm thấy acc sự kiện này"
		}
		t.forceStopEventUser(memberID)
		if err := model.DeleteEventUser(db, memberID); err != nil {
			return "!!không xoá được: " + err.Error()
		}
		return "Đã xoá acc sự kiện " + memberID

	case "login":
		memberID := strings.TrimSpace(r.FormValue("member_id"))
		var block string
		capture := func(s string) { block = s } // surface the real refuse reason (Đệ tử login idiom)
		if m, ok := t.getOwnedEventUser(memberID, chatID); ok {
			if t.startEventPlayerLogin(m.ID, chatID, capture) {
				return "Đang đăng nhập acc sự kiện " + m.Username + "…"
			}
			if block != "" {
				return "!!" + block
			}
			return "!!không đăng nhập được acc sự kiện này"
		}
		if u, ok := t.getDeputyInFleet(memberID, chatID); ok {
			if t.startEventPlayerLoginForDeputy(u, chatID, capture) {
				return "Đang đăng nhập acc uỷ thác " + u.Username + " vào event…"
			}
			if block != "" {
				return "!!" + block
			}
			return "!!không đăng nhập được acc uỷ thác này"
		}
		return "!!acc này không thuộc quyền của bạn"

	case "logout":
		memberID := strings.TrimSpace(r.FormValue("member_id"))
		if _, ok := t.getOwnedEventUser(memberID, chatID); ok {
			if t.forceStopEventUser(memberID) {
				return "Đã cho acc sự kiện nghỉ"
			}
			return "acc đó đang không chạy"
		}
		if u, ok := t.getDeputyInFleet(memberID, chatID); ok {
			if t.forceStopEventUser(u.ID) {
				return "Đã cho acc uỷ thác nghỉ event"
			}
			return "acc đó đang không chạy event"
		}
		return "!!acc này không thuộc quyền của bạn"

	case "config":
		// Per-account event switches (tab Sự kiện, 2026-11-09): off-switches
		// on the EventUser row — default false keeps "login là chạy" for
		// every pre-existing account. Real event_users only: a 🤝 deputy is a
		// users row with no EventUser to gate (stays always-on, reported).
		memberID := strings.TrimSpace(r.FormValue("member_id"))
		ev, ok := t.getOwnedEventUser(memberID, chatID)
		if !ok {
			return "!!không tìm thấy acc sự kiện này"
		}
		if applyEventOffSwitches(ev, r) {
			if err := model.UpdateEventUser(db, ev); err != nil {
				return "!!không lưu được cấu hình: " + err.Error()
			}
		}
		if p, ok := t.eventSessionOf(*ev); ok {
			p.OffRbt, p.OffSeaWar = ev.OffRebornTrial, ev.OffSeaWar
		}
		return "Đã lưu cấu hình sự kiện cho " + ev.Username

	case "config_all":
		// Same two switches for the WHOLE fleet (deputy rows skipped — they
		// have no EventUser row to persist).
		fleet, err := t.eventFleetOf(leaderID)
		if err != nil {
			return "!!không tải được danh sách acc sự kiện"
		}
		applied := 0
		for i := range fleet {
			if fleet[i].Deputy {
				continue
			}
			ev := fleet[i].Ev
			if applyEventOffSwitches(&ev, r) {
				if err := model.UpdateEventUser(db, &ev); err != nil {
					return "!!không lưu được cấu hình cho " + ev.Username + ": " + err.Error()
				}
			}
			if p, ok := t.eventSessionOf(ev); ok {
				p.OffRbt, p.OffSeaWar = ev.OffRebornTrial, ev.OffSeaWar
			}
			applied++
		}
		if applied == 0 {
			return "Bạn chưa có acc sự kiện nào"
		}
		return fmt.Sprintf("Đã áp cấu hình sự kiện cho %d acc.", applied)

	case "eat_all":
		// 🍏 "Cắn quả" card (owner 2026-11-09): every hanging event account
		// drains its energy-fruit bag through the button-only 234922 path.
		// Deputies INCLUDED (runtime order, no DB row needed — they ARE
		// hanging clones). Fire-and-forget per [[tytd-async-order-fire]]:
		// the eat loop runs on its own goroutine per account.
		fleet, err := t.eventFleetOf(leaderID)
		if err != nil {
			return "!!không tải được danh sách acc sự kiện"
		}
		eaten := 0
		for i := range fleet {
			if p, ok := t.eventSessionOf(fleet[i].Ev); ok && sessionRunning(p) {
				p.swEatAll()
				eaten++
			}
		}
		if eaten == 0 {
			return "Chưa có acc sự kiện nào đang treo"
		}
		return fmt.Sprintf("🌊 Đã cho %d acc cắn quả (dùng hết vật phẩm thể lực)", eaten)

	case "login_all":
		fleet, err := t.eventFleetOf(leaderID)
		if err != nil {
			return "!!không tải được danh sách acc sự kiện"
		}
		if len(fleet) == 0 {
			return "Bạn chưa có acc sự kiện nào"
		}
		started, skipped := 0, 0
		for i, fr := range fleet {
			id, _ := strconv.ParseInt(fr.Ev.ID, 10, 64)
			hang := ""
			if v, ok := t.session.Load(id); ok && sessionRunning(v) {
				if _, isPlayer := v.(*Player); isPlayer {
					hang = "player"
				} else {
					hang = "event"
				}
			}
			// "call" FORCEs EventPlayer (owner 2026-10-10): a delegated
			// account hanging as a normal member is a target, not a skip —
			// the game server evicts that hang itself (1 acc = 1 online) and
			// the deputy path SNATCHES without burning a slot. Only accounts
			// already running as EventPlayer (or classic event_users with
			// any session) skip.
			if hang == "event" || (hang == "player" && !fr.Deputy) {
				skipped++
				continue
			}
			if hang != "player" && t.getRunningCount()+started >= t.getMaxPlayers() {
				skipped += len(fleet) - i
				break
			}
			// A delegate runs the users-row variant (VIP gate, deputy notify,
			// snatches a live Player hang); a real event_user keeps the
			// classic path.
			var ok bool
			if fr.Deputy {
				ok = t.startEventPlayerLoginForDeputy(fr.Usr, chatID, notify)
			} else if !fr.Ev.LastLoginFail.IsZero() && time.Since(fr.Ev.LastLoginFail) < 3*time.Minute {
				skipped++
				continue
			} else {
				ok = t.startEventPlayerLogin(fr.Ev.ID, chatID, notify)
			}
			if ok {
				started++
			} else {
				skipped++
			}
		}
		return fmt.Sprintf("Đã cho %d acc sự kiện vào game, %d bỏ qua", started, skipped)

	case "logout_all":
		fleet, err := t.eventFleetOf(leaderID)
		if err != nil {
			return "!!không tải được danh sách acc sự kiện"
		}
		done := 0
		for _, fr := range fleet {
			// forceStopEventUser only stops *EventPlayer sessions, so a
			// delegated account hanging in normal mode is left untouched.
			if t.forceStopEventUser(fr.Ev.ID) {
				done++
			}
		}
		return fmt.Sprintf("Đã cho %d acc sự kiện nghỉ", done)
	}
	return "!!yêu cầu không hợp lệ"
}

// getDeputyInFleet returns the users row a leader may operate as a DELEGATED
// event account: it exists, delegated to THIS leader (deputy_of == leader), and
// is not the leader's own member (those belong on the Đệ tử page, not here).
func (t *TeleBot) getDeputyInFleet(memberID string, leaderChatID int64) (*model.User, bool) {
	u, err := model.GetUser(t.ydb.GetDB(), memberID)
	if err != nil || u.DeputyOf == "" || u.DeputyOf != fmt.Sprintf("%d", leaderChatID) {
		return nil, false
	}
	// Owner 2026-10-10: a member of THIS leader may still be pulled to events
	// by this same leader (deputating your own alt is a normal case). The
	// deputy login path already handles the live hang safely — SNATCH, per
	// the standing "lead giật acc đang treo" rule — so no leader_id exclusion.
	return u, true
}

// giWhichFromForm maps the web island selector to the event variant.
func giWhichFromForm(v string) (int, string, bool) {
	switch v {
	case "bonglai":
		return GIEventNormal, giEventLabel(GIEventNormal), true
	case "thienma":
		return GIEventPeak, giEventLabel(GIEventPeak), true
	}
	return 0, "", false
}

// webRunEventOrder fires order at every RUNNING event user of the leader
// ASYNCHRONOUSLY (owner 2026-10-21: "gửi async, chỉ việc bắn lên server
// game, response nhận sau, acc nào lỗi thì alert nhẹ lên cho user còn không
// lỗi thì thôi") and returns as soon as the fleet was enumerated. The old
// synchronous twin of eventOrderBroadcast made one leader button wait on
// every account's guards back-to-back — a kunlun/bacminh press queued up to
// 8s of kuEnsureScene PER account (page-timeout territory), and one wedged
// socket head-of-line-blocked the accounts behind it. Safety of the async
// shape: each account owns ONE socket with one writer goroutine, so orders
// to the same account stay strictly serialized (per-account FIFO); nothing
// cross-account needed the old ordering. Immediate refusals ("chưa login"
// was filtered already; think "quân đang không hành quân", "sự kiện chưa
// mở") are COLLECTED across the fleet and flushed as ONE summary line per
// press (owner 2026-10-21, second strike: "cầm nhiều acc, thao tác nhanh cái
// này mày gửi thông báo kín cả màn hình" — one bubble per failing account is
// screen-flood at 20 acc; at most 4 names are listed, the rest counted).
// Async-side verdicts (GI's ⚡ refusals via giHandleAccelerateResp) still
// alert per account but the notiChan drain caps any chat to a small burst
// (notifyBurstCap/notifyBurstWin in telebot.go), so fast clicking can never
// cover the screen. Successes stay silent. `moved` now
// means DISPATCHED, not server-accepted. fails stays in the signature for
// caller compatibility but is always nil now. loadErr = roster read failed.
func (t *TeleBot) webRunEventOrder(leaderID, label string, order func(*EventPlayer) error) (moved, notRunning int, fails []string, loadErr error) {
	chatID, _ := strconv.ParseInt(leaderID, 10, 64)
	// Fleet = the leader's own event_users PLUS delegated accounts (eventFleetOf)
	// — a delegated account logged in via startEventPlayerLoginForDeputy lives
	// in the SAME session map under its account id and asserts to *EventPlayer,
	// so orders reach it exactly like a real event account.
	fleet, err := t.eventFleetOf(leaderID)
	if err != nil {
		return 0, 0, nil, err
	}
	var wg sync.WaitGroup
	var fmu sync.Mutex
	var errs []string
	for _, fr := range fleet {
		m := fr.Ev
		id, err := strconv.ParseInt(m.ID, 10, 64)
		if err != nil {
			notRunning++
			continue
		}
		v, ok := t.session.Load(id)
		if !ok {
			notRunning++
			continue
		}
		p, ok := v.(*EventPlayer)
		if !ok || !p.Running {
			notRunning++
			continue
		}
		wg.Add(1)
		go func(p *EventPlayer, name string) {
			defer wg.Done()
			if err := order(p); err != nil {
				log.Printf("[web order] %s: %v", name, err)
				fmu.Lock()
				errs = append(errs, name+": "+err.Error())
				fmu.Unlock()
			}
		}(p, m.Username)
		moved++
	}
	// One summary line for the whole press — collected off-thread so the
	// HTTP handler still returns the instant the fleet was dispatched
	// (kuEnsureScene's 8s must never reach the browser). Zero failures =
	// zero messages ("không lỗi thì thôi"). errs is a LOCAL slice: the named
	// return stays nil, no race with the handler's return.
	go func() {
		wg.Wait()
		if len(errs) == 0 {
			return
		}
		const maxListed = 4
		msg := fmt.Sprintf("⚠️ %s: %d/%d acc lỗi — %s", label, len(errs), moved, strings.Join(errs[:min(len(errs), maxListed)], "; "))
		if len(errs) > maxListed {
			msg += fmt.Sprintf(" …và %d acc nữa (xem log)", len(errs)-maxListed)
		}
		// t.pushNotify (not p.sendNotify): the event session is
		// built with Notify=false, but its leader-chat alert must
		// flow regardless.
		t.pushNotify(chatID, msg)
	}()
	return moved, notRunning, nil, nil
}

// webHolyLandOrder handles which=cuuu on /events/order: move / attack /
// attack_thuve / auto need a city id (auto also accepts 0 = off), tangtoc /
// hoisinh / hoisinh_item take none. All guards live in the EventPlayer
// HolyLand* methods; refusals are flashed per account in game language.
func (t *TeleBot) webHolyLandOrder(w http.ResponseWriter, r *http.Request, leaderID string) {
	act := r.FormValue("act")
	var arg int32
	switch act {
	case "move", "attack", "attack_thuve", "auto":
		n, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("arg")), 10, 32)
		if err != nil || n < 0 || (n == 0 && act != "auto") {
			t.webRedirectMsg(w, r, "/events", "!!city id không hợp lệ")
			return
		}
		arg = int32(n)
	case "tangtoc", "hoisinh", "hoisinh_item":
	default:
		t.webRedirectMsg(w, r, "/events", "!!chọn hành động trước đã")
		return
	}

	label := "Cửu U " + act
	order := func(p *EventPlayer) error {
		switch act {
		case "move":
			return p.HolyLandMoveTo(arg)
		case "attack":
			return p.HolyLandAttackTo(arg, false)
		case "attack_thuve":
			return p.HolyLandAttackTo(arg, true)
		case "auto":
			return p.HolyLandAutoAttack(arg)
		case "tangtoc":
			return p.HolyLandAccelerate()
		case "hoisinh":
			return p.HolyLandRebirth(false)
		case "hoisinh_item":
			return p.HolyLandRebirth(true)
		}
		return fmt.Errorf("hành động không hợp lệ")
	}

	moved, notRunning, fails, err := t.webRunEventOrder(leaderID, label, order)
	if err != nil {
		t.webRedirectMsg(w, r, "/events", "!!không tải được danh sách acc sự kiện")
		return
	}
	msg := fmt.Sprintf("Đã lệnh %d acc sự kiện (%s)", moved, hlLabel)
	switch act {
	case "move", "attack", "attack_thuve", "auto":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (%s) với thành %d", moved, hlLabel, arg)
		if act == "auto" && arg == 0 {
			msg = fmt.Sprintf("Đã tắt treo máy tấn công ở %d acc sự kiện (%s)", moved, hlLabel)
		}
	case "hoisinh_item":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (%s) hồi sinh bằng vật phẩm", moved, hlLabel)
	case "hoisinh":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (%s) hồi sinh miễn phí", moved, hlLabel)
	}
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d bỏ qua (chưa login)", notRunning)
	}
	if len(fails) > 0 {
		msg = "!!" + msg + " — " + strings.Join(fails, "; ")
	}
	t.webRedirectMsg(w, r, "/events", msg)
}

// webBallGVGOrder handles which=ditich on /events/order: enter / mark need
// a place id, home / leave take none. All guards live in the EventPlayer
// BallGVG* methods; walking and attacks inside a room are automatic on the
// event accounts (bgAuto) and have no web act. Refusals flash per account in
// game language.
func (t *TeleBot) webBallGVGOrder(w http.ResponseWriter, r *http.Request, leaderID string) {
	act := r.FormValue("act")
	label := "Thượng Cổ " + act
	var arg int32
	switch act {
	case "enter", "mark":
		n, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("arg")), 10, 32)
		if err != nil || n <= 0 {
			t.webRedirectMsg(w, r, "/events", "!!cứ điểm không hợp lệ")
			return
		}
		arg = int32(n)
	case "home", "leave":
	default:
		t.webRedirectMsg(w, r, "/events", "!!chọn hành động trước đã")
		return
	}

	order := func(p *EventPlayer) error {
		switch act {
		case "enter":
			return p.BallGVGEnterRoom(arg)
		case "mark":
			return p.BallGVGMark(arg)
		case "home":
			return p.BallGVGHome()
		case "leave":
			return p.BallGVGLeaveRoom()
		}
		return fmt.Errorf("hành động không hợp lệ")
	}

	moved, notRunning, fails, err := t.webRunEventOrder(leaderID, label, order)
	if err != nil {
		t.webRedirectMsg(w, r, "/events", "!!không tải được danh sách acc sự kiện")
		return
	}
	msg := fmt.Sprintf("Đã lệnh %d acc sự kiện (%s)", moved, label)
	switch act {
	case "enter":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (%s) vào cứ điểm %d", moved, label, arg)
	case "mark":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (%s) đánh dấu cứ điểm %d", moved, label, arg)
	case "home":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (%s) về cứ điểm nhà", moved, label)
	case "leave":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (%s) rời cứ điểm", moved, label)
	}
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d bỏ qua (chưa login)", notRunning)
	}
	if len(fails) > 0 {
		msg = "!!" + msg + " — " + strings.Join(fails, "; ")
	}
	t.webRedirectMsg(w, r, "/events", msg)
}

// webEventsOrder is POST-only: one island order to every running event
// user of the leader (web twin of eventOrderBroadcast). God Island variants
// flow through giWhichFromForm; which=cuuu branches to the Holy Land
// endpoint and which=ditich to the Ball GVG endpoint before any GI
// parsing, so GI act names and guards stay untouched.
// webKunlunOrder answers a Côn Lôn board popup or HUD button. march
// (attackCity=false) / attack (attackCity=true) reach ANY passable cell — the
// client does exactly this, GameMgr.reqMove sends {targetPoint:
// getPosIndex(x,y), attackCity} for whatever tile its radial menu was opened
// on and the server alone decides reachability (ret 19944). explore / sweep
// (219905/219906) are offered on EVERY cell now (user directive 2026-10-07)
// — kuGateStand checks only transport (login/window/seat), the server judges
// each account (sweep carries no target field, so an army standing elsewhere
// sweeps its own tile — accepted). arg is the posIndex y*61+x; void/obstacle
// taps are refused here so a stray click never reaches the game socket. tangtoc (219918),
// hoisinh_item (219915 useItem=true) and quat (219911 spirit-fruit claim, no
// count field — the server decides the payout) are army-level HUD actions
// taking no arg — the revive-item route is the ONE authorized item exception
// and fires only from an explicit button press, mirroring /kunlun_hoisinh
// item; the fruit tree is NOT an item path — collecting ally liquid
// (GET_WELFARE 219912) is free for the receiver and runs in the kuTreeTick
// auto loop, while SEND_WELFARE 219913 (spends a liquid) and the 🍈 Cắn quả
// USE_ITEM 219926 stay button-only/manual under the standing item ban.
func (t *TeleBot) webKunlunOrder(w http.ResponseWriter, r *http.Request, leaderID string) {
	act := r.FormValue("act")
	var order func(*EventPlayer) error
	var n int64
	var ate int // anqua: accounts that actually pressed (Energy<10 rule)
	switch act {
	case "tangtoc":
		order = func(p *EventPlayer) error { return p.KuAccelerate() }
	case "hoisinh_item":
		order = func(p *EventPlayer) error { return p.KunlunWarRebirth(true) }
	case "quat":
		order = func(p *EventPlayer) error { return p.KunlunWarClaimFruit() }
	case "anqua":
		// "Cắn quả" = 219926 use-item on the account's own picked fruit
		// (user packet capture 2026-10-06 authorized this exact item op).
		// v3 rule (customer fix 2026-10-07): accounts UNDER 10 energy arm a
		// bounded chase that bites one packet at a time — sized by the heal
		// LEARNED from verdicts and clamped by the counted bag — until 10;
		// ≥10 skips with no packet, a press while chasing never duplicates.
		order = func(p *EventPlayer) error {
			num, err := p.KunlunWarEatFruitToTen()
			if num > 0 {
				ate++
			}
			return err
		}
	default:
		if act != "march" && act != "attack" && act != "explore" && act != "sweep" {
			t.webRedirectMsg(w, r, "/events", "!!chọn hành động trước đã")
			return
		}
		var err error
		n, err = strconv.ParseInt(strings.TrimSpace(r.FormValue("arg")), 10, 64)
		if err != nil || !gimap.KunlunCellPassable(n) {
			t.webRedirectMsg(w, r, "/events", "!!ô không hợp lệ — chỉ bấm được ô trên sàn Côn Lôn")
			return
		}
		point := int32(n)
		order = func(p *EventPlayer) error {
			switch act {
			case "explore":
				return p.KunlunWarExplorePoint(point)
			case "sweep":
				return p.KunlunWarSweepHere(point)
			}
			return p.KunlunWarMoveToPoint(point, act == "attack")
		}
	}
	label := "Côn Lôn " + act
	moved, notRunning, fails, err := t.webRunEventOrder(leaderID, label, order)
	if err != nil {
		t.webRedirectMsg(w, r, "/events", "!!không tải được danh sách acc sự kiện")
		return
	}
	var msg string
	switch act {
	case "tangtoc":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (Côn Lôn) tăng tốc hành quân", moved)
	case "hoisinh_item":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (Côn Lôn) hồi sinh bằng vật phẩm", moved)
	case "quat":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (Côn Lôn) hái quả Côn Lôn — acc nào có quả chín sẽ ăn, kết quả ở log từng acc", moved)
	case "anqua":
		msg = fmt.Sprintf("Đã lệnh cắn quả (Côn Lôn): %d acc dưới 10 năng lượng cắn cho đủ 10, %d acc đã ≥10 bỏ qua — lượng hồi mỗi quả do server quyết, acc thiếu quả server từ chối (log từng acc)", ate, moved-ate)
	default:
		verb := "hành quân tới"
		switch act {
		case "attack":
			verb = "tấn công"
		case "explore":
			verb = "khám phá"
		case "sweep":
			verb = "càn quét"
		}
		where := fmt.Sprintf("ô (%d,%d)", n%61, n/61)
		if name, ok := gimap.KunlunCityNameAt(n); ok {
			where = name
		}
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (Côn Lôn) %s %s", moved, verb, where)
	}
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d bỏ qua (chưa login)", notRunning)
	}
	if len(fails) > 0 {
		msg = "!!" + msg + " — " + strings.Join(fails, "; ")
	}
	t.webRedirectMsg(w, r, "/events", msg)
}

// webBacMinhOrder is the Đỉnh Bắc Minh twin of webKunlunOrder — same acts
// (march/attack/explore/sweep/tangtoc/quat/anqua/hoisinh_item), everything
// validated and executed against the NORTH ledger: passability via
// gimap.BacMinhCellPassable (119² grid), march targets via the same
// posIndex, orders hit EventPlayer.BacMinh* (activityId 332). tangtoc is the
// user-authorized press-thru (server spends accel item first, else gems);
// anqua is the authorized north fruit eat (observed item 105363);
// hoisinh_item the authorized instant revive (server item 105368).
func (t *TeleBot) webBacMinhOrder(w http.ResponseWriter, r *http.Request, leaderID string) {
	act := r.FormValue("act")
	var order func(*EventPlayer) error
	var n int64
	var ate int // anqua: accounts that actually pressed (Energy<10 rule)
	switch act {
	case "tangtoc":
		order = func(p *EventPlayer) error { return p.BmAccelerate() }
	case "hoisinh_item":
		order = func(p *EventPlayer) error { return p.BacMinhWarRebirth(true) }
	case "quat":
		// Free harvest (219911) — the tree button; auto bmTreeTick already
		// runs it, this is the manual instant.
		order = func(p *EventPlayer) error { return p.BacMinhWarClaimFruit() }
	case "anqua":
		// 2026-10-07 rule (mirrored from Côn Lôn): only accounts UNDER 10
		// energy arm the v3 learned-heal chase (see KunlunWarEatFruitToTen);
		// ≥10 skips with no packet.
		order = func(p *EventPlayer) error {
			num, err := p.BacMinhEatFruitToTen()
			if num > 0 {
				ate++
			}
			return err
		}
	default:
		if act != "march" && act != "attack" && act != "explore" && act != "sweep" {
			t.webRedirectMsg(w, r, "/events", "!!chọn hành động trước đã")
			return
		}
		var err error
		n, err = strconv.ParseInt(strings.TrimSpace(r.FormValue("arg")), 10, 64)
		if err != nil || !gimap.BacMinhCellPassable(n) {
			t.webRedirectMsg(w, r, "/events", "!!ô không hợp lệ — chỉ bấm được ô trên sàn Đỉnh Bắc Minh")
			return
		}
		point := int32(n)
		order = func(p *EventPlayer) error {
			switch act {
			case "explore":
				return p.BacMinhWarExplorePoint(point)
			case "sweep":
				return p.BacMinhWarSweepHere(point)
			}
			return p.BacMinhWarMoveToPoint(point, act == "attack")
		}
	}
	label := "Bắc Minh " + act
	moved, notRunning, fails, err := t.webRunEventOrder(leaderID, label, order)
	if err != nil {
		t.webRedirectMsg(w, r, "/events", "!!không tải được danh sách acc sự kiện")
		return
	}
	var msg string
	switch act {
	case "tangtoc":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (Bắc Minh) tăng tốc hành quân — server trừ item tăng tốc nếu còn, hết mới trừ ngọc", moved)
	case "hoisinh_item":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (Bắc Minh) hồi sinh nhanh bằng vật phẩm", moved)
	case "quat":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (Bắc Minh) hái quả — acc nào có quả chín sẽ ăn, kết quả ở log từng acc", moved)
	case "anqua":
		msg = fmt.Sprintf("Đã lệnh cắn quả (Bắc Minh): %d acc dưới 10 năng lượng cắn cho đủ 10, %d acc đã ≥10 bỏ qua — lượng hồi do server quyết (log từng acc)", ate, moved-ate)
	default:
		verb := "hành quân tới"
		switch act {
		case "attack":
			verb = "tấn công"
		case "explore":
			verb = "khám phá"
		case "sweep":
			verb = "càn quét"
		}
		cols := gimap.BacMinhCols()
		if cols <= 0 {
			cols = 1
		}
		where := fmt.Sprintf("ô (%d,%d)", n%int64(cols), n/int64(cols))
		if name, ok := gimap.BacMinhCityNameAt(n); ok {
			where = name
		}
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (Bắc Minh) %s %s", moved, verb, where)
	}
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d bỏ qua (chưa login)", notRunning)
	}
	if len(fails) > 0 {
		msg = "!!" + msg + " — " + strings.Join(fails, "; ")
	}
	t.webRedirectMsg(w, r, "/events", msg)
}

func (t *TeleBot) webEventsOrder(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	if r.Method != http.MethodPost {
		t.webRedirectMsg(w, r, "/events", "")
		return
	}
	leaderID := fmt.Sprintf("%d", chatID)
	if r.FormValue("which") == "cuuu" {
		t.webHolyLandOrder(w, r, leaderID)
		return
	}
	if r.FormValue("which") == "ditich" {
		t.webBallGVGOrder(w, r, leaderID)
		return
	}
	if r.FormValue("which") == "kunlun" {
		t.webKunlunOrder(w, r, leaderID)
		return
	}
	if r.FormValue("which") == "bacminh" {
		t.webBacMinhOrder(w, r, leaderID)
		return
	}
	if r.FormValue("which") == "luanhoi" {
		// The bracket belongs to the leader's OWN account (its union), so
		// this arm runs on the main session, not the event fleet.
		t.webLuanHoiOrder(w, r, chatID)
		return
	}
	if r.FormValue("which") == gimap.StreamSeaWarWar {
		// SeaWar chiến trường: board reads ride the donor, the ⚔️ press
		// fans out to the event fleet (bang chiến law).
		t.webSeaWarWarOrder(w, r, chatID)
		return
	}
	which, label, ok := giWhichFromForm(r.FormValue("which"))
	if !ok {
		t.webRedirectMsg(w, r, "/events", "!!chọn đảo trước đã")
		return
	}
	act := r.FormValue("act")

	// Same argument rules as eventOrderWithArg: city orders need a positive
	// id, canqua defaults to 50, tangtoc/hoimau take none.
	var arg int32
	switch act {
	case "move", "attack", "attack_thuve":
		n, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("arg")), 10, 32)
		if err != nil || n <= 0 {
			t.webRedirectMsg(w, r, "/events", "!!city id không hợp lệ")
			return
		}
		arg = int32(n)
	case "canqua":
		arg = giDefaultFruitCount
		if s := strings.TrimSpace(r.FormValue("arg")); s != "" {
			n, err := strconv.ParseInt(s, 10, 32)
			if err != nil || n <= 0 {
				t.webRedirectMsg(w, r, "/events", "!!số quả không hợp lệ")
				return
			}
			arg = int32(n)
		}
	case "tangtoc", "hoimau":
	default:
		t.webRedirectMsg(w, r, "/events", "!!chọn hành động trước đã")
		return
	}

	label += " " + act // "Bồng Lai tangtoc" — the summary line says WHICH press failed
	order := func(p *EventPlayer) error {
		switch act {
		case "move":
			return p.GodIslandMoveTo(which, arg)
		case "attack":
			return p.GodIslandAttackTo(which, arg, false)
		case "attack_thuve":
			return p.GodIslandAttackTo(which, arg, true)
		case "canqua":
			return p.GodIslandUseFruit(which, arg)
		case "tangtoc":
			return p.GodIslandAccelerate(which)
		case "hoimau":
			return p.GodIslandHealPower(which)
		}
		return fmt.Errorf("hành động không hợp lệ")
	}

	moved, notRunning, fails, err := t.webRunEventOrder(leaderID, label, order)
	if err != nil {
		t.webRedirectMsg(w, r, "/events", "!!không tải được danh sách acc sự kiện")
		return
	}
	msg := fmt.Sprintf("Đã lệnh %d acc sự kiện (%s)", moved, label)
	switch act {
	case "move", "attack", "attack_thuve":
		msg = fmt.Sprintf("Đã lệnh %d acc sự kiện (%s) với thành %d", moved, label, arg)
	case "canqua":
		msg = fmt.Sprintf("Đã cho %d acc sự kiện (%s) dùng %d quả", moved, label, arg)
	}
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d bỏ qua (chưa login)", notRunning)
	}
	if len(fails) > 0 {
		msg = "!!" + msg + " — " + strings.Join(fails, "; ")
	}
	t.webRedirectMsg(w, r, "/events", msg)
}

// webMapWidth is THE render width: the PNG is produced at it and the
// &fmt=json layout uses the same number, so overlay coordinates stay glued
// to the circles drawn in the image (the page places them by percentage).
const webMapWidth = 1400

// webEventsMap is GET /events/map?which=bonglai|thienma — the leader's own
// logged-in account renders the current ownership minimap as PNG (same
// pipeline as /bonglai_map). With &fmt=json it answers the identical city
// layout as JSON instead, for the interactive HTML overlay. (kunlun and cuuu
// are the SERVER-RAM exceptions at the top of this handler: they 302 to their
// browser-baked mini apps and never reach the compositor; ditich keeps this
// contract.)
//
// Failures answer as plain-text error statuses, NEVER a redirect: the page
// embeds this endpoint via fetch(), which would transparently follow a 303 to
// the /events HTML and the UI would either show a broken image or (when the
// old target=_blank links were used) navigate the visitor to a whole other
// page that looked like a random redirect. Status classes let the JS pick its
// wording; the body carries the human Vietnamese reason.
func (t *TeleBot) webEventsMap(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	if r.URL.Query().Get("which") == "cuuu" {
		// SERVER-RAM POLICY (2026-10-06, same round as kunlun): the browser
		// bakes the identical board in /cuuu (embedded scene bundle, immutable
		// cache) — a page view must never run the Go compositor on the VPS.
		// RenderHolyLand now serves ONLY the on-demand /cuuu_map command.
		http.Redirect(w, r, "/cuuu", http.StatusFound)
		return
	}
	if r.URL.Query().Get("which") == "ditich" {
		t.webBallGVGMap(w, r, chatID)
		return
	}
	if r.URL.Query().Get("which") == "kunlun" {
		// SERVER-RAM POLICY (2026-10-06): the browser bakes the identical
		// board in /kunlun (scene.json + sheet, zero server pixels) — a page
		// view must never decode the 19 MB sprite sheet on the VPS. The old
		// GET-render arm is gone; /kunlun_map (Telegram, one photo per
		// explicit command) is the only server-side compositor left.
		http.Redirect(w, r, "/kunlun", http.StatusFound)
		return
	}
	if r.URL.Query().Get("which") == "bacminh" {
		// SERVER-RAM POLICY: same contract as kunlun — the browser bakes the
		// north board in /bacminh; a page view must never decode the ~54 MB
		// north sheet on the VPS.
		http.Redirect(w, r, "/bacminh", http.StatusFound)
		return
	}
	which, label, ok := giWhichFromForm(r.URL.Query().Get("which"))
	if !ok {
		http.Error(w, "chọn đảo trước đã", http.StatusBadRequest)
		return
	}
	v, sessOK := t.session.Load(chatID)
	p, playerOK := v.(*Player)
	if !sessOK || !playerOK || !p.Running {
		http.Error(w, "đăng nhập game (tài khoản chính) trước đã", http.StatusConflict)
		return
	}
	payload, err := p.RequestGodIslandMiniMap(which, webMapTimeout)
	if err != nil {
		http.Error(w, "không lấy được map "+label+": "+err.Error(), http.StatusBadGateway)
		return
	}
	if r.URL.Query().Get("fmt") == "json" {
		// Interactive overlay: city positions in the SAME coordinate space
		// the PNG below uses (webMapWidth), so a click target placed by
		// percentage lands on the drawn circle. Game-facing fields only —
		// no protocol/command detail ever crosses to the client.
		lay, err := gimap.Layout(payload, webMapWidth, which == GIEventPeak)
		if err != nil {
			http.Error(w, "lỗi đọc toạ độ thành: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(lay)
		return
	}
	render := gimap.Render
	if which == GIEventPeak {
		render = gimap.RenderPeak
	}
	img, err := render(payload, webMapWidth)
	if err != nil {
		http.Error(w, "lỗi render map: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(img)
}

// webBallGVGMap is the which=ditich arm of /events/map: the leader's own
// logged-in account renders the Thượng Cổ Di Tích room map (schematic — the
// protocol carries no coordinates) with the same error-status contract as the
// other map arms: plain-text statuses, never a redirect; &fmt=json answers the
// clickable layout in the SAME coordinate space as the PNG.
func (t *TeleBot) webBallGVGMap(w http.ResponseWriter, r *http.Request, chatID int64) {
	v, sessOK := t.session.Load(chatID)
	p, playerOK := v.(*Player)
	if !sessOK || !playerOK || !p.Running {
		http.Error(w, "đăng nhập game (tài khoản chính) trước đã", http.StatusConflict)
		return
	}
	payload, err := p.RequestBallGVGMap(webMapTimeout)
	if err != nil {
		http.Error(w, "không lấy được map Thượng Cổ Di Tích: "+err.Error(), http.StatusBadGateway)
		return
	}
	if r.URL.Query().Get("fmt") == "json" {
		lay, err := gimap.LayoutBallGVG(payload, webMapWidth)
		if err != nil {
			http.Error(w, "lỗi đọc vị trí cứ điểm: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(lay)
		return
	}
	img, err := gimap.RenderBallGVG(payload, webMapWidth)
	if err != nil {
		http.Error(w, "lỗi render map: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(img)
}
