package tytd_go

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/model"

	"html"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// isLeader checks if a user is a leader
func (t *TeleBot) isLeader(chatID int64) bool {
	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", chatID))
	if err != nil {
		return false
	}
	return u.IsLeader
}

// AddMemberHandler allows leaders to create member accounts
func (t *TeleBot) AddMemberHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 4 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /add_member <username> <password> <server_id>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	username := stripInvisible(args[1])
	password := stripInvisible(args[2])
	serverID, err := parseServerID(args[3])
	if err != nil || username == "" || password == "" {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "invalid server_id",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Generate random user ID
	randomID := model.GenerateRandomUserID(t.ydb.GetDB())

	// Create member account
	u := &model.User{
		ID:       randomID,
		Username: username,
		Password: password,
		ServerID: serverID,
		GameUID:  0,
		VipLevel: 1,
		// Member trial is 1 hour (owner 2026-11-08) — the VIP watchdog
		// (telebot_vipwatchdog.go) evicts the acc once it lapses, even
		// mid-session. New self-auth USER accounts keep their 24h.
		VipExpireAt:    time.Now().Add(time.Hour),
		AutoLogin:      true,
		AutoMouse:      true,
		AutoGather:     true,
		MinBottleLevel: 2,
		MinPeachLevel:  4,
		MinLuuLyLevel:  6,
		LeaderID:       fmt.Sprintf("%d", message.Chat.ID),
		IsLeader:       false,
	}

	err = model.CreateUser(t.ydb.GetDB(), u)
	if err != nil {
		log.Printf("AddMemberHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to create member: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("Member created!\nUser ID: <code>%s</code>\nUsername: %s\nServer: %d", randomID, username, serverID),
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	// login member

	if t.getRunningCount() >= t.getMaxPlayers() {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "auto login but server is full, try again later",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	p := &Player{}
	tUID, _ := strconv.ParseInt(u.ID, 10, 64)
	p.UID = uint64(tUID)
	p.Username = u.Username
	p.Password = u.Password
	p.ServerID = u.ServerID
	p.VipLevel = u.VipLevel
	p.ChatID = message.Chat.ID
	p.NotiChan = t.notiChan
	p.AutoStartChan = t.autoStartChan
	p.ChuotMinDao = u.MinPeachLevel
	p.ChuotMinBinh = u.MinBottleLevel
	p.ChuotMinBinh = u.MinBottleLevel
	p.ChuotMinLuuly = u.MinLuuLyLevel
	p.BottlePrefered = u.BottlePrefered
	p.DB = t.ydb.GetDB()
	p.seedAutoGates(u)
	if u.AutoMouse && chuotLoginRun(true, u.ChuotStartSchedule, u.ChuotStopSchedule, time.Now()) {
		p.ChuotRun = true
	}
	p.ClanMember = make(map[uint64]string)

	p.Notify = false
	t.session.Store(randomID, p)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "go go! logged in",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	u.Running = true
	model.CreateUser(t.ydb.GetDB(), u)
	go func() {
		if !p.Run() {
			model.SetLastLoginFail(t.ydb.GetDB(), u.ID)
		}
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "bi da' ra roi` đạo hữu",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		u, err := model.GetUser(t.ydb.GetDB(), randomID)
		if err != nil {
			log.Printf("loginHandler error 3: %v", err)
			t.sessionDel(randomID, p)
			return
		}
		u.Running = false
		model.CreateUser(t.ydb.GetDB(), u)
		t.sessionDel(randomID, p)
	}()
}

// RemoveMemberHandler allows leaders to remove member accounts
func (t *TeleBot) RemoveMemberHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /remove_member <user_id>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	memberID := args[1]

	// Verify this member belongs to this leader
	member, err := model.GetUser(t.ydb.GetDB(), memberID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "member not found",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if member.LeaderID != fmt.Sprintf("%d", message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "this member doesn't belong to you",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Delete the member
	err = model.DeleteUser(t.ydb.GetDB(), memberID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to delete member: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("Member %s deleted successfully", memberID),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// ListMyMembersHandler shows all members belonging to a leader
func (t *TeleBot) ListMyMembersHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load members: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no members yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	var sb strings.Builder
	header := "<b>👥 Your Members</b>\n\n"
	sb.WriteString(header)

	for i, m := range members {
		autoLoginStatus := "❌"
		if m.AutoLogin {
			autoLoginStatus = "✅"
		}

		runningStatus := "Stopped"
		if m.Running {
			runningStatus = "Running"
		}

		memberInfo := fmt.Sprintf("%d. <code>%s</code>\n", i+1, html.EscapeString(m.ID))
		memberInfo += fmt.Sprintf("   • Username: %s\n", html.EscapeString(m.Username))
		memberInfo += fmt.Sprintf("   • Game ID: %d\n", m.GameUID)
		memberInfo += fmt.Sprintf("   • Server: %d\n", m.ServerID)
		memberInfo += fmt.Sprintf("   • VIP: Level %d (expires: %s)\n", m.VipLevel, m.VipExpireAt.In(ictZone).Format("02/01/2006"))
		memberInfo += fmt.Sprintf("   • Auto Login: %s\n", autoLoginStatus)
		memberInfo += fmt.Sprintf("   • Status: %s\n", runningStatus)
		memberInfo += fmt.Sprintf("   • Settings: Binh %d | Dao %d | Luuly %d | Tuling min %d\n", m.MinBottleLevel, m.MinPeachLevel, m.MinLuuLyLevel, m.MinGatherIncome)
		if !m.LastLoginFail.IsZero() {
			memberInfo += fmt.Sprintf("   • Last Fail: %s\n", m.LastLoginFail.In(ictZone).Format("15:04:05 02/01"))
		}
		memberInfo += "\n"

		// Telegram message limit is 4096. We use 4000 to be safe.
		if sb.Len()+len(memberInfo) > 3600 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:    message.Chat.ID,
				Text:      sb.String(),
				ParseMode: models.ParseModeHTML,
			})
			sb.Reset()
			sb.WriteString(header)
		}
		sb.WriteString(memberInfo)
	}

	if sb.Len() > len(header) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            sb.String(),
			ParseMode:       models.ParseModeHTML,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
	}
}

// SetMemberPasswordHandler allows leaders to change member passwords
func (t *TeleBot) SetMemberPasswordHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 3 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /set_member_password <user_id> <new_password>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	memberID := args[1]
	newPassword := args[2]

	// Verify member belongs to leader
	member, err := model.GetUser(t.ydb.GetDB(), memberID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "member not found",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if member.LeaderID != fmt.Sprintf("%d", message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "this member doesn't belong to you",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Update password in DB
	member.Password = newPassword
	err = model.UpdateUser(t.ydb.GetDB(), member)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to update password: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Update active session if running
	realID, _ := strconv.ParseInt(memberID, 10, 64)
	if val, ok := t.session.Load(realID); ok {
		// A delegated (uỷ thác) session stores *EventPlayer under the same
		// key (telebot_deputy.go) — refresh whichever type is resident.
		if p, isP := val.(*Player); isP {
			p.Password = newPassword
		} else if ep, isEP := val.(*EventPlayer); isEP {
			ep.Password = newPassword
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("Password updated for member %s", memberID),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// ToggleMemberAutoLoginHandler allows leaders to toggle auto login for members
func (t *TeleBot) ToggleMemberAutoLoginHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /toggle_member_auto_login <user_id>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	memberID := args[1]

	// Verify member belongs to leader
	member, err := model.GetUser(t.ydb.GetDB(), memberID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "member not found",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if member.LeaderID != fmt.Sprintf("%d", message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "this member doesn't belong to you",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Toggle auto login
	member.AutoLogin = !member.AutoLogin
	err = model.UpdateUser(t.ydb.GetDB(), member)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to toggle auto login: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	status := "disabled"
	if member.AutoLogin {
		status = "enabled"
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("Auto login %s for member %s", status, memberID),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// ToggleMemberTuLinhHandler allows leaders to toggle the permanent Tụ Linh
// (auto gather energy) switch for one of their members. Saved to the DB, so
// it survives re-login; a running member picks it up on its next gather minute.
func (t *TeleBot) ToggleMemberTuLinhHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /toggle_member_tu_linh <user_id>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	memberID := args[1]

	// Verify member belongs to leader
	member, err := model.GetUser(t.ydb.GetDB(), memberID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "member not found",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if member.LeaderID != fmt.Sprintf("%d", message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "this member doesn't belong to you",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	member.AutoGather = !member.AutoGather
	err = model.UpdateUser(t.ydb.GetDB(), member)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to toggle tu linh: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Patch the member's LIVE session (keyed by member id, per
	// memberLoginAll) — the gather loop reads p.AutoGather, not the DB.
	if key, e := strconv.ParseInt(memberID, 10, 64); e == nil {
		t.patchAutoGates(key, func(p *Player) { p.AutoGather = member.AutoGather })
	}

	status := "đã tắt"
	if member.AutoGather {
		status = "đã bật"
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID,
		Text: fmt.Sprintf("Tự động Tụ Linh %s cho member %s (đang chạy sẽ áp dụng "+
			"ngay phút thu tiếp theo).", status, memberID),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// MemberNotifyHandler is the per-member twin of /start_notify:
// /member_start_notify <user_id> and /member_stop_notify <user_id> flip the
// game-notice switch of ONE running member session. Notifications are a
// runtime switch of the session (never a DB setting), so a member login
// starts silent until the leader turns it on. Every notice a member
// produces lands in the LEADER's chat with the account name prefixed — and
// the leader's own /start_notify only ever gates his own account's notices,
// so "notify cho tôi" never drags member alerts along.
func (t *TeleBot) MemberNotifyHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /member_start_notify <user_id> | /member_stop_notify <user_id>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	on := strings.HasPrefix(args[0], "/member_start")
	memberID := args[1]

	member, err := model.GetUser(t.ydb.GetDB(), memberID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID,
			Text:   "member not found",

			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	if member.LeaderID != fmt.Sprintf("%d", message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID,
			Text:   "this member doesn't belong to you",

			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	id, err := strconv.ParseInt(memberID, 10, 64)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID,
			Text:   "member not found",

			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	v, ok := t.session.Load(id)
	p, ok := v.(*Player)
	if !ok || !p.Running {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID,
			Text:   fmt.Sprintf("member %s chưa đang treo game — không bật/tắt thông báo được", memberID),

			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	p.Notify = on
	verb := "tắt"
	if on {
		verb = "bật"
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID,
		Text: fmt.Sprintf("Đã %s thông báo game của member %s — tin nhắn sẽ về chat này "+
			"(kèm tên acc) khi acc đó có sự kiện.", verb, memberID),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// MemberLoginHandler allows leaders to log in all of their members at once
func (t *TeleBot) MemberLoginHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load members: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no members yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	started := 0
	skipped := 0
	remaining := 0

	for i, u := range members {
		realUID, _ := strconv.ParseInt(u.ID, 10, 64)

		// Skip if already running
		if val, ok := t.session.Load(realUID); ok {
			if p, ok2 := val.(*Player); ok2 && p.Running {
				skipped++
				continue
			}
		}

		// Skip if banned
		if model.IsBanned(t.ydb.GetDB(), realUID) {
			skipped++
			continue
		}

		// Skip if VIP expired
		if u.VipExpireAt.Before(time.Now()) || u.VipLevel == 0 {
			skipped++
			continue
		}

		// Skip if in cooldown after a recent login failure
		if !u.LastLoginFail.IsZero() && time.Since(u.LastLoginFail) < 3*time.Minute {
			skipped++
			continue
		}

		// Check max concurrent players
		if t.getRunningCount()+started >= t.getMaxPlayers() {
			remaining = len(members) - i
			break
		}

		p := &Player{}
		p.UID = uint64(realUID)
		p.Username = u.Username
		p.Password = u.Password
		p.ServerID = u.ServerID
		p.VipLevel = u.VipLevel
		p.ChatID = message.Chat.ID
		p.NotiChan = t.notiChan
		p.AutoStartChan = t.autoStartChan
		p.ChuotMinDao = u.MinPeachLevel
		p.ChuotMinBinh = u.MinBottleLevel
		p.ChuotMinLuuly = u.MinLuuLyLevel
		p.BottlePrefered = u.BottlePrefered
		p.DB = t.ydb.GetDB()
		p.seedAutoGates(&u)
		if u.AutoMouse && chuotLoginRun(true, u.ChuotStartSchedule, u.ChuotStopSchedule, time.Now()) {
			p.ChuotRun = true
		}
		p.ClanMember = make(map[uint64]string)

		p.Notify = false
		t.session.Store(realUID, p)
		started++

		u.Running = true
		model.CreateUser(t.ydb.GetDB(), &u)
		go func(p *Player, memberID string, uid int64) {
			if !p.Run() {
				model.SetLastLoginFail(t.ydb.GetDB(), memberID)
			}
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          uid,
				Text:            "bi da' ra roi` đạo hữu " + p.Username,
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			u2, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", uid))
			if err != nil {
				log.Printf("MemberLoginHandler error: %v", err)
				t.sessionDel(uid, p)
				return
			}
			u2.Running = false
			model.CreateUser(t.ydb.GetDB(), u2)
			t.sessionDel(uid, p)
		}(p, u.ID, realUID)
	}

	msg := fmt.Sprintf("🚀 Started %d member(s)", started)
	if skipped > 0 {
		msg += fmt.Sprintf(", skipped %d (running, banned, VIP expired or cooldown)", skipped)
	}
	if remaining > 0 {
		msg += fmt.Sprintf(", %d not started (server is full)", remaining)
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            msg,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// MemberLogoutHandler allows leaders to log out all of their members at once
func (t *TeleBot) MemberLogoutHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load members: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no members yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	loggedOut := 0
	alreadyStopped := 0

	for _, u := range members {
		realUID, _ := strconv.ParseInt(u.ID, 10, 64)

		val, ok := t.session.Load(realUID)
		if !ok {
			alreadyStopped++
			continue
		}
		p, ok2 := val.(*Player)
		if !ok2 || !p.Running {
			// Stale entry not actually running — clean it up (still only
			// what we loaded: never a fresh re-login that won the race).
			t.sessionDel(realUID, val)
			alreadyStopped++
			continue
		}

		if p.cancel != nil {
			p.cancel()
		}
		t.sessionDel(realUID, p)

		nu, err := model.GetUser(t.ydb.GetDB(), u.ID)
		if err == nil {
			nu.Running = false
			model.CreateUser(t.ydb.GetDB(), nu)
		}
		loggedOut++
	}

	msg := fmt.Sprintf("👋 Logged out %d member(s)", loggedOut)
	if alreadyStopped > 0 {
		msg += fmt.Sprintf(", %d already stopped", alreadyStopped)
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            msg,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// MemberStartChatCayHandler allows leaders to enable chat-cay (chặt cây) for all of their running members at once
func (t *TeleBot) MemberStartChatCayHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load members: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no members yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	enabled := 0
	notRunning := 0

	for _, u := range members {
		realUID, _ := strconv.ParseInt(u.ID, 10, 64)

		val, ok := t.session.Load(realUID)
		if !ok {
			notRunning++
			continue
		}
		p, ok2 := val.(*Player)
		if !ok2 || !p.Running {
			notRunning++
			continue
		}

		p.Peach = true
		enabled++
	}

	msg := fmt.Sprintf("🪓 Started chat cay for %d member(s)", enabled)
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d not running", notRunning)
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            msg,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// MemberStopChatCayHandler allows leaders to disable chat-cay (chặt cây) for all of their running members at once
func (t *TeleBot) MemberStopChatCayHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load members: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no members yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	stopped := 0
	notRunning := 0

	for _, u := range members {
		realUID, _ := strconv.ParseInt(u.ID, 10, 64)

		val, ok := t.session.Load(realUID)
		if !ok {
			notRunning++
			continue
		}
		p, ok2 := val.(*Player)
		if !ok2 || !p.Running {
			notRunning++
			continue
		}

		p.Peach = false
		p.XaVe = false
		stopped++
	}

	msg := fmt.Sprintf("🛑 Stopped chat cay for %d member(s)", stopped)
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d not running", notRunning)
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            msg,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// MemberStartChatCayXaVeHandler allows leaders to enable chat-cay with xả vé for all of their running members at once
func (t *TeleBot) MemberStartChatCayXaVeHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load members: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no members yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	enabled := 0
	notRunning := 0

	for _, u := range members {
		realUID, _ := strconv.ParseInt(u.ID, 10, 64)

		val, ok := t.session.Load(realUID)
		if !ok {
			notRunning++
			continue
		}
		p, ok2 := val.(*Player)
		if !ok2 || !p.Running {
			notRunning++
			continue
		}

		p.Peach = true
		p.XaVe = true
		enabled++
	}

	msg := fmt.Sprintf("🪓 Started chat cay xave for %d member(s)", enabled)
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d not running", notRunning)
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            msg,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// MemberStartChuotHandler allows leaders to enable chuot (chuột) for all of their running members at once
func (t *TeleBot) MemberStartChuotHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load members: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no members yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	enabled := 0
	notRunning := 0

	for _, u := range members {
		realUID, _ := strconv.ParseInt(u.ID, 10, 64)

		val, ok := t.session.Load(realUID)
		if !ok {
			notRunning++
			continue
		}
		p, ok2 := val.(*Player)
		if !ok2 || !p.Running {
			notRunning++
			continue
		}

		p.ChuotRun = true
		enabled++
	}

	msg := fmt.Sprintf("🖱️ Started chuot for %d member(s)", enabled)
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d not running", notRunning)
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            msg,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// MemberStopChuotHandler allows leaders to disable chuot (chuột) for all of their running members at once
func (t *TeleBot) MemberStopChuotHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load members: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no members yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	stopped := 0
	notRunning := 0

	for _, u := range members {
		realUID, _ := strconv.ParseInt(u.ID, 10, 64)

		val, ok := t.session.Load(realUID)
		if !ok {
			notRunning++
			continue
		}
		p, ok2 := val.(*Player)
		if !ok2 || !p.Running {
			notRunning++
			continue
		}

		p.ChuotRun = false
		stopped++
	}

	msg := fmt.Sprintf("🛑 Stopped chuot for %d member(s)", stopped)
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d not running", notRunning)
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            msg,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// MemberScheduleStartChuotHandler lets a leader set a daily chuot start schedule
// (list of hours 1-24) for all of their members.
func (t *TeleBot) MemberScheduleStartChuotHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.handleMemberChuotSchedule(ctx, b, update, true)
}

// MemberScheduleStopChuotHandler lets a leader set a daily chuot stop schedule
// (list of hours 1-24) for all of their members.
func (t *TeleBot) MemberScheduleStopChuotHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.handleMemberChuotSchedule(ctx, b, update, false)
}

func (t *TeleBot) handleMemberChuotSchedule(ctx context.Context, b *bot.Bot, update *models.Update, isStart bool) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load members: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no members yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	cmd := "/member_schedule_stop_chuot"
	verb := "tắt"
	if isStart {
		cmd = "/member_schedule_start_chuot"
		verb = "bật"
	}

	send := func(text string) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            text,
			ParseMode:       models.ParseModeHTML,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
	}

	parts := strings.Fields(message.Text)
	args := parts[1:]

	if len(args) == 0 {
		send(fmt.Sprintf(
			"❌ Sử dụng: <code>%s {thời_gian1} [thời_gian2] ...</code> (H:MM, H từ 0 đến 24, MM từ 0 đến 59)\nVD: <code>%s 0:30 10:30 18 22:00</code>\nGõ <code>%s off</code> để xóa lịch",
			cmd, cmd, cmd,
		))
		return
	}

	// Clear the schedule for all members when a single clear keyword is given.
	if len(args) == 1 && isChuotScheduleClearKeyword(args[0]) {
		for _, u := range members {
			if isStart {
				model.SetChuotStartSchedule(t.ydb.GetDB(), u.ID, "")
			} else {
				model.SetChuotStopSchedule(t.ydb.GetDB(), u.ID, "")
			}
		}
		send(fmt.Sprintf("🗑️ Đã xóa lịch %s chuột cho %d members", verb, len(members)))
		return
	}

	csv, err := parseChuotTimes(args)
	if err != nil {
		send("❌ " + err.Error())
		return
	}

	for _, u := range members {
		if isStart {
			model.SetChuotStartSchedule(t.ydb.GetDB(), u.ID, csv)
		} else {
			model.SetChuotStopSchedule(t.ydb.GetDB(), u.ID, csv)
		}
	}

	send(fmt.Sprintf(
		"✅ Đã đặt lịch %s chuột hàng ngày cho <b>%d members</b>: <b>%s</b>\n(Hàng ngày vào các thời điểm này)",
		verb, len(members), formatChuotTimes(csv),
	))
}

// MemberScheduleLoginHandler lets a leader set a daily auto-login time list
// for all of their members (mirror of /schedule_login for members).
func (t *TeleBot) MemberScheduleLoginHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.handleMemberLoginSchedule(ctx, b, update, true)
}

// MemberScheduleLogoutHandler lets a leader set a daily auto-logout time list
// for all of their members (mirror of /schedule_logout for members).
func (t *TeleBot) MemberScheduleLogoutHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.handleMemberLoginSchedule(ctx, b, update, false)
}

// handleMemberLoginSchedule writes LoginSchedule/LogoutSchedule for every
// member of the leader. Nothing else needs arming: the shared minute ticker
// already scans every user row (members included) and its notifications land
// in the leader's chat.
func (t *TeleBot) handleMemberLoginSchedule(ctx context.Context, b *bot.Bot, update *models.Update, isLogin bool) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetMembersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load members: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no members yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	cmd := "/member_schedule_logout"
	noun := "đăng xuất"
	if isLogin {
		cmd = "/member_schedule_login"
		noun = "đăng nhập"
	}

	send := func(text string) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            text,
			ParseMode:       models.ParseModeHTML,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
	}

	args := strings.Fields(message.Text)[1:]

	if len(args) == 0 {
		send(fmt.Sprintf(
			"❌ Sử dụng: <code>%s {thời_gian1} [thời_gian2] ...</code> (H:MM, H từ 0 đến 24, MM từ 0 đến 59)\nVD: <code>%s 0:30 10:30 18 22:00</code>\nGõ <code>%s off</code> để xóa lịch",
			cmd, cmd, cmd,
		))
		return
	}

	if len(args) == 1 && isChuotScheduleClearKeyword(args[0]) {
		for _, u := range members {
			if isLogin {
				model.SetLoginSchedule(t.ydb.GetDB(), u.ID, "")
			} else {
				model.SetLogoutSchedule(t.ydb.GetDB(), u.ID, "")
			}
		}
		send(fmt.Sprintf("🗑️ Đã xóa lịch %s cho %d members", noun, len(members)))
		return
	}

	csv, err := parseChuotTimes(args)
	if err != nil {
		send("❌ " + err.Error())
		return
	}

	for _, u := range members {
		if isLogin {
			model.SetLoginSchedule(t.ydb.GetDB(), u.ID, csv)
		} else {
			model.SetLogoutSchedule(t.ydb.GetDB(), u.ID, csv)
		}
	}

	send(fmt.Sprintf(
		"✅ Đã đặt lịch %s hàng ngày cho <b>%d members</b>: <b>%s</b>\n(Hàng ngày vào các thời điểm này)",
		noun, len(members), formatChuotTimes(csv),
	))
}
