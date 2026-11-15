package tytd_go

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// chuotScheduleZone is the timezone used for the daily chuot schedule. It matches
// the UTC+7 fixed zone used elsewhere in the bot (e.g. the quiet-hours check).
func chuotScheduleZone() *time.Location {
	return time.FixedZone("UTC+7", 7*60*60)
}

// parseChuotTimeArg parses a single schedule arg of form "H" or "H:MM" into an
// hour in [0,24] and a minute in [0,59]. Hour 0 is early morning (0:00-0:59);
// hour 24 is only valid with minute 0 (midnight, an alias for 0:00).
func parseChuotTimeArg(a string) (hour, minute int, ok bool) {
	fields := strings.Split(a, ":")
	if len(fields) < 1 || len(fields) > 2 {
		return 0, 0, false
	}
	h, err := strconv.Atoi(fields[0])
	if err != nil || h < 0 || h > 24 {
		return 0, 0, false
	}
	m := 0
	if len(fields) == 2 {
		m, err = strconv.Atoi(fields[1])
		if err != nil || m < 0 || m > 59 {
			return 0, 0, false
		}
	}
	if h == 24 && m != 0 {
		return 0, 0, false
	}
	return h, m, true
}

// parseChuotTimes validates that every arg is a time of day ("H" in [1,24] or
// "H:MM" with MM in [0,59]), de-dupes them and returns a sorted CSV string of
// "H:MM" entries (e.g. "10:00,18:30,24:00").
func parseChuotTimes(args []string) (string, error) {
	type tm struct{ h, m int }
	seen := make(map[tm]bool)
	var times []tm
	for _, a := range args {
		h, m, ok := parseChuotTimeArg(a)
		if !ok {
			return "", fmt.Errorf("thời gian %q không hợp lệ, dùng H:MM với H từ 0 đến 24, MM từ 0 đến 59 (24 chỉ dùng với :00)", a)
		}
		if !seen[tm{h, m}] {
			seen[tm{h, m}] = true
			times = append(times, tm{h, m})
		}
	}
	sort.Slice(times, func(i, j int) bool {
		if times[i].h != times[j].h {
			return times[i].h < times[j].h
		}
		return times[i].m < times[j].m
	})
	parts := make([]string, len(times))
	for i, x := range times {
		parts[i] = fmt.Sprintf("%d:%02d", x.h, x.m)
	}
	return strings.Join(parts, ","), nil
}

// formatChuotTimes turns a stored CSV of "H:MM" entries (e.g. "10:00,18:30")
// into a display string ("10:00, 18:30") or "Off" when empty. Legacy bare hours
// ("10,18") are normalized to "10:00, 18:00".
func formatChuotTimes(csv string) string {
	if csv == "" {
		return "Off"
	}
	parts := strings.Split(csv, ",")
	outs := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, ":") {
			outs = append(outs, p)
		} else {
			outs = append(outs, p+":00")
		}
	}
	if len(outs) == 0 {
		return "Off"
	}
	return strings.Join(outs, ", ")
}

// minuteMatchesSchedule reports whether the given local hour (0-23) and minute
// (0-59) matches any "H:MM" entry in the stored CSV. Entry hour 24 (24:00) is
// treated as midnight, i.e. local hour 0. Legacy bare hours are treated as ":00".
func minuteMatchesSchedule(csv string, hour, minute int) bool {
	if csv == "" {
		return false
	}
	for _, p := range strings.Split(csv, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		ah, am, ok := parseChuotTimeArg(p)
		if !ok {
			continue
		}
		targetHour := ah
		if targetHour == 24 {
			targetHour = 0
		}
		if targetHour == hour && am == minute {
			return true
		}
	}
	return false
}

// chuotLoginRun decides the mouse default AT LOGIN. With no chuot schedule it
// is just the AutoMouse default. Once a schedule exists the account lives in
// a TIME RANGE instead of isolated one-shot marks: the mouse is on at login
// iff the most recent daily start/stop event (UTC+7, a same-minute stop wins
// the tie exactly like applyChuotSchedules) was a START — so with "stop 2:00,
// start 10:00", logging in at 02:30 keeps the mouse OFF until 10:00 fires.
// Before today's first event, yesterday's last event carries over (daily
// repeat wraps midnight).
func chuotLoginRun(defaultOn bool, startCSV, stopCSV string, now time.Time) bool {
	if !defaultOn {
		return false
	}
	if startCSV == "" && stopCSV == "" {
		return true
	}
	type ev struct {
		m    int
		stop bool
	}
	var pts []ev
	add := func(csv string, stop bool) {
		for _, p := range strings.Split(csv, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			h, mm, ok := parseChuotTimeArg(p)
			if !ok {
				continue
			}
			if h == 24 {
				h = 0
			}
			pts = append(pts, ev{h*60 + mm, stop})
		}
	}
	add(startCSV, false)
	add(stopCSV, true)
	n := now.In(chuotScheduleZone())
	nowMin := n.Hour()*60 + n.Minute()
	last, lastStop := -1, false
	for _, e := range pts {
		if e.m <= nowMin && (e.m > last || (e.m == last && e.stop && !lastStop)) {
			last, lastStop = e.m, e.stop
		}
	}
	if last >= 0 {
		return !lastStop
	}
	for _, e := range pts {
		if e.m > last || (e.m == last && e.stop && !lastStop) {
			last, lastStop = e.m, e.stop
		}
	}
	return !lastStop
}

// isChuotScheduleClearKeyword reports whether the arg asks to clear the schedule.
func isChuotScheduleClearKeyword(s string) bool {
	switch strings.ToLower(s) {
	case "off", "xoa", "xóa", "clear", "delete", "reset":
		return true
	}
	return false
}

func (t *TeleBot) scheduleStartChuotHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.handleChuotSchedule(ctx, b, update, true)
}

func (t *TeleBot) scheduleStopChuotHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.handleChuotSchedule(ctx, b, update, false)
}

func (t *TeleBot) handleChuotSchedule(ctx context.Context, b *bot.Bot, update *models.Update, isStart bool) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	cmd := "/schedule_stop_chuot"
	verb := "tắt"
	if isStart {
		cmd = "/schedule_start_chuot"
		verb = "bật"
	}

	parts := strings.Fields(message.Text)
	args := parts[1:]

	send := func(text string) {
		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            text,
			ParseMode:       models.ParseModeHTML,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		if err != nil {
			log.Println(err.Error())
		}
	}

	if len(args) == 0 {
		send(fmt.Sprintf(
			"❌ Sử dụng: <code>%s {thời_gian1} [thời_gian2] ...</code> (H:MM, H từ 0 đến 24, MM từ 0 đến 59)\nVD: <code>%s 0:30 10:30 18 22:00</code>\nGõ <code>%s off</code> để xóa lịch",
			cmd, cmd, cmd,
		))
		return
	}

	// Clear the schedule when a single clear keyword is given.
	if len(args) == 1 && isChuotScheduleClearKeyword(args[0]) {
		if isStart {
			model.SetChuotStartSchedule(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID), "")
		} else {
			model.SetChuotStopSchedule(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID), "")
		}
		send(fmt.Sprintf("🗑️ Đã xóa lịch %s chuột hàng ngày", verb))
		return
	}

	csv, err := parseChuotTimes(args)
	if err != nil {
		send("❌ " + err.Error())
		return
	}

	if isStart {
		if err := model.SetChuotStartSchedule(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID), csv); err != nil {
			log.Printf("handleChuotSchedule error: %v", err)
			send("⚠️ server error")
			return
		}
	} else {
		if err := model.SetChuotStopSchedule(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID), csv); err != nil {
			log.Printf("handleChuotSchedule error: %v", err)
			send("⚠️ server error")
			return
		}
	}

	send(fmt.Sprintf(
		"✅ Đã đặt lịch %s chuột hàng ngày: <b>%s</b>\n(Hàng ngày vào các thời điểm này)",
		verb, formatChuotTimes(csv),
	))
}

// startChuotScheduler launches a background goroutine that, at the top of each
// minute (UTC+7), applies any matching daily chuot start/stop schedules.
func (t *TeleBot) startChuotScheduler() {
	go func() {
		zone := chuotScheduleZone()
		for {
			now := time.Now()
			// Sleep until the next top-of-minute, then fire.
			next := now.Truncate(time.Minute).Add(time.Minute)
			time.Sleep(next.Sub(now))
			n := time.Now().In(zone)
			t.applyChuotSchedules(n.Hour(), n.Minute())
			t.applyLoginSchedules(n.Hour(), n.Minute())
			t.applyLogoutSchedules(n.Hour(), n.Minute())
		}
	}()
}

// applyChuotSchedules applies every user's start/stop chuot schedule whose time
// matches the given local hour (0-23) and minute (0-59).
func (t *TeleBot) applyChuotSchedules(hour, minute int) {
	users := model.GetUsersWithChuotSchedule(t.ydb.GetDB())
	for _, u := range users {
		if u.ChuotStartSchedule != "" && minuteMatchesSchedule(u.ChuotStartSchedule, hour, minute) {
			t.setPlayerChuot(u.ID, true)
		}
		if u.ChuotStopSchedule != "" && minuteMatchesSchedule(u.ChuotStopSchedule, hour, minute) {
			t.setPlayerChuot(u.ID, false)
		}
	}
}

// setPlayerChuot toggles the chuot flag on a user's running player (if any).
func (t *TeleBot) setPlayerChuot(userID string, run bool) {
	chatID, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		return
	}
	v, ok := t.session.Load(chatID)
	if !ok {
		return
	}
	// A delegated (uỷ thác) EventPlayer can sit under a user key
	// (telebot_deputy.go) — the chuot schedule must skip it, not assert.
	p, isP := v.(*Player)
	if !isP || !p.Running || p.ChuotRun == run {
		return
	}
	p.ChuotRun = run
	log.Printf("[chuot-schedule] user %s (%s) chuot=%v", userID, p.Username, run)
	// Notify the owner's chat that the daily chuot schedule fired. p.ChatID is
	// the leader's chat for a clone member and the user's own chat otherwise.
	msg := "🛑 đã tự động tắt chuột (lịch)"
	if run {
		msg = "🖱️ đã tự động bật chuột (lịch)"
	}
	t.notiChan <- NotifyMGS{MSG: msg, ChatID: p.ChatID}
}

func (t *TeleBot) scheduleLoginHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.handleLoginSchedule(ctx, b, update, true)
}

func (t *TeleBot) scheduleLogoutHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.handleLoginSchedule(ctx, b, update, false)
}

// handleLoginSchedule manages the per-user daily login/logout schedule. It
// mirrors handleChuotSchedule: it stores a CSV of "H:MM" times and is cleared
// with a single off/reset keyword.
func (t *TeleBot) handleLoginSchedule(ctx context.Context, b *bot.Bot, update *models.Update, isLogin bool) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	cmd := "/schedule_logout"
	noun := "đăng xuất"
	if isLogin {
		cmd = "/schedule_login"
		noun = "đăng nhập"
	}

	parts := strings.Fields(message.Text)
	args := parts[1:]

	send := func(text string) {
		_, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            text,
			ParseMode:       models.ParseModeHTML,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		if err != nil {
			log.Println(err.Error())
		}
	}

	if len(args) == 0 {
		send(fmt.Sprintf(
			"❌ Sử dụng: <code>%s {thời_gian1} [thời_gian2] ...</code> (H:MM, H từ 0 đến 24, MM từ 0 đến 59)\nVD: <code>%s 0:30 10:30 18 22:00</code>\nGõ <code>%s off</code> để xóa lịch",
			cmd, cmd, cmd,
		))
		return
	}

	userID := fmt.Sprintf("%d", message.Chat.ID)

	// Clear the schedule when a single clear keyword is given.
	if len(args) == 1 && isChuotScheduleClearKeyword(args[0]) {
		if isLogin {
			model.SetLoginSchedule(t.ydb.GetDB(), userID, "")
		} else {
			model.SetLogoutSchedule(t.ydb.GetDB(), userID, "")
		}
		send(fmt.Sprintf("🗑️ Đã xóa lịch %s hàng ngày", noun))
		return
	}

	csv, err := parseChuotTimes(args)
	if err != nil {
		send("❌ " + err.Error())
		return
	}

	if isLogin {
		if err := model.SetLoginSchedule(t.ydb.GetDB(), userID, csv); err != nil {
			log.Printf("handleLoginSchedule error: %v", err)
			send("⚠️ server error")
			return
		}
	} else {
		if err := model.SetLogoutSchedule(t.ydb.GetDB(), userID, csv); err != nil {
			log.Printf("handleLoginSchedule error: %v", err)
			send("⚠️ server error")
			return
		}
	}

	send(fmt.Sprintf(
		"✅ Đã đặt lịch %s hàng ngày: <b>%s</b>\n(Hàng ngày vào các thời điểm này)",
		noun, formatChuotTimes(csv),
	))
}

// applyLoginSchedules applies every user's daily login schedule whose time
// matches the given local hour (0-23) and minute (0-59). It reuses the shared
// startPlayerLogin core, which enforces the 3-minute login-fail gate so a
// schedule cannot flood the game server. Notifications are pushed through
// notiChan so they respect the same quiet-hours rules as other notices.
func (t *TeleBot) applyLoginSchedules(hour, minute int) {
	for _, u := range model.GetUsersWithLoginSchedule(t.ydb.GetDB()) {
		if u.LoginSchedule == "" || !minuteMatchesSchedule(u.LoginSchedule, hour, minute) {
			continue
		}
		sessionKey, err := strconv.ParseInt(u.ID, 10, 64)
		if err != nil {
			continue
		}
		// A clone member has no Telegram chat of its own, so notifications go
		// to the leader; regular users/leaders notify on their own chat (the
		// session key). The session key stays ParseInt(u.ID) in both cases.
		notiChat := sessionKey
		if u.LeaderID != "" {
			if lc, err := strconv.ParseInt(u.LeaderID, 10, 64); err == nil {
				notiChat = lc
			}
		}
		notify := func(text string) {
			t.notiChan <- NotifyMGS{MSG: text, ChatID: notiChat}
		}
		// Owner 2026-11-11: "có khách báo acc hết vip rồi nhưng bot vẫn auto
		// login" — lịch đăng nhập là path auto-login DUY NHẤT còn thiếu gate
		// VIP (autoStartChan đã có từ 2026-10-27, fleet/web leader đều đã tự
		// check). Same law as auto_login: vấp hết VIP thì TẮT trigger luôn
		// (xóa login_schedule của dòng này) + đúng MỘT tin 🛑 — không bắn lại
		// mỗi ngày. Gia hạn VIP rồi bật lại lịch trong Cài đặt.
		if model.IsBanned(t.ydb.GetDB(), sessionKey) {
			log.Printf("[login-schedule] bỏ qua %s (banned)", u.Username)
			continue
		}
		if u.VipLevel == 0 || u.VipExpireAt.Before(time.Now()) {
			if err := model.SetLoginSchedule(t.ydb.GetDB(), u.ID, ""); err != nil {
				log.Printf("[login-schedule] không tắt được login_schedule khi VIP hết hạn: %s %v", u.Username, err)
			}
			notify("🛑 " + u.Username + ": lịch đăng nhập tự động vừa TẮT vì tài khoản đã hết hạn VIP. " +
				"Cho nó chạy lại: gia hạn VIP rồi bật lại lịch trong Cài đặt.")
			log.Printf("[login-schedule] VIP hết hạn, đã tắt login_schedule của %s", u.Username)
			continue
		}
		if t.startPlayerLogin(sessionKey, notify) {
			log.Printf("[login-schedule] user %s auto login started", u.ID)
		}
	}
}

// applyLogoutSchedules applies every user's daily logout schedule whose time
// matches the given local hour (0-23) and minute (0-59).
func (t *TeleBot) applyLogoutSchedules(hour, minute int) {
	for _, u := range model.GetUsersWithLoginSchedule(t.ydb.GetDB()) {
		if u.LogoutSchedule == "" || !minuteMatchesSchedule(u.LogoutSchedule, hour, minute) {
			continue
		}
		sessionKey, err := strconv.ParseInt(u.ID, 10, 64)
		if err != nil {
			continue
		}
		// Notify the leader's chat for a clone member; the member's own chat
		// (session key) for regular users.
		notiChat := sessionKey
		if u.LeaderID != "" {
			if lc, err := strconv.ParseInt(u.LeaderID, 10, 64); err == nil {
				notiChat = lc
			}
		}
		if t.stopPlayerForChatID(sessionKey) {
			t.notiChan <- NotifyMGS{MSG: "🚪 đã tự động đăng xuất (lịch)", ChatID: notiChat}
		}
	}
}
