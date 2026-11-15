package tytd_go

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (t *TeleBot) auth(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	message := update.Message

	// Check if user is banned
	if model.IsBanned(t.ydb.GetDB(), message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🚫 Bạn đã bị cấm sử dụng bot",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return false
	}

	if !model.UserExists(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID)) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "cut' di con cho' dmm",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return false
	}

	// Check VIP expiration
	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		log.Printf("auth error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return false
	}
	if err == nil && u.VipLevel > 0 {
		if u.VipExpireAt.Before(time.Now()) {
			// Update user
			u.VipLevel = 0
			u.VipExpireAt = time.Now()
			model.CreateUser(t.ydb.GetDB(), u)

			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "het' han VIP roi đạo hữu /redeem skibidi de duoc free 30 ngay",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return false
		}
		return true
	}

	if err == nil && u.VipLevel == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "het' han VIP roi đạo hữu /redeem skibidi de duoc free 30 ngay",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return false
	}
	return true
}

func (t *TeleBot) authHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if message.Chat.Type != "private" {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "cut' nhe'",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	// Check if user is banned
	if model.IsBanned(t.ydb.GetDB(), message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🚫 Bạn đã bị cấm sử dụng bot",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /auth <key>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if model.UserExists(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID)) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "auth 1 lan thoi",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	// Get auth key from database
	authKeyCfg, err := model.GetConfig(t.ydb.GetDB(), model.ConfigKeyAuthKey)
	if err != nil {
		log.Printf("Failed to get auth key from config: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if args[1] != authKeyCfg.Value {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "cut' di con cho' dmm",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	u := model.User{
		Username:       "",
		Password:       "",
		ServerID:       0,
		GameUID:        0,
		VipLevel:       1,
		VipExpireAt:    time.Now().Add(24 * time.Hour),
		MinBottleLevel: 2,
		MinPeachLevel:  4,
		MinLuuLyLevel:  5,
		// Tụ Linh gathered for everyone before the switch existed — new
		// accounts start with their own switch on (like auto_login); the
		// global /set_auto_tu_linh gate can still pause gathering for
		// everyone, and the owner opts out via /auto_tu_linh.
		AutoGather: true,
	}
	u.ID = fmt.Sprintf("%d", message.Chat.ID)
	err = model.CreateUser(t.ydb.GetDB(), &u)
	if err != nil {
		log.Println(err.Error())
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "okey đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	return
}

func (t *TeleBot) setUsernameHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /username <username>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		log.Printf("setUsername error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	uname := stripInvisible(args[1])
	if uname == "" {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "username rỗng (chỉ có kí tự ẩn?)",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	u.Username = uname
	u.GameUID = 1 // Reset GameUID when username changes
	model.CreateUser(t.ydb.GetDB(), u)

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "okey đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	return
}

func (t *TeleBot) setPasswordHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /password <password>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		log.Printf("setPassword error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	pass := stripInvisible(args[1])
	if pass == "" {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "password rỗng (chỉ có kí tự ẩn?)",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	u.Password = pass
	u.GameUID = 1 // Reset GameUID when password changes
	model.CreateUser(t.ydb.GetDB(), u)

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "okey đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	return
}

func (t *TeleBot) setServerIDHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /server_id <server_id>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		log.Printf("setServerID error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	// Never swallow a parse error into server 0 (the old bug that made every
	// later login use server 0): reject the save and show the raw token %q so
	// a trailing space or invisible character is visible to the owner.
	num, perr := parseServerID(args[1])
	if perr != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server_id không hợp lệ: " + perr.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	u.ServerID = num
	u.GameUID = 1 // Reset GameUID when server ID changes

	model.CreateUser(t.ydb.GetDB(), u)

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("okey đạo hữu — server %d", num),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	return
}

func (t *TeleBot) loginHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	if message.Chat.Type != "private" {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "cut' nhe'",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if model.IsBanned(t.ydb.GetDB(), message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🚫 Bạn đã bị cấm sử dụng bot",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Check if group membership verification is enabled
	mustJoinGroupConfig, err := model.GetConfig(t.ydb.GetDB(), model.ConfigKeyMustJoinGroup)
	mustJoinGroup := false
	if err == nil && mustJoinGroupConfig != nil {
		mustJoinGroup, _ = strconv.ParseBool(mustJoinGroupConfig.Value)
	}

	// Only check group membership if enabled
	if mustJoinGroup {
		// Check if user is in required group
		chatMember, err := b.GetChatMember(ctx, &bot.GetChatMemberParams{
			ChatID: "-1003507972955",
			UserID: message.From.ID,
		})

		if err != nil {
			log.Printf("GetChatMember error: %v", err)
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "chua vao group @tytd_tool_support thi cut' nhe'",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}

		// Check if user is a valid member (Member, Administrator, or Owner)
		// Reject if user has left, is banned, or is restricted
		if chatMember.Member == nil && chatMember.Administrator == nil && chatMember.Owner == nil {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "chua vao group @tytd_tool_support thi cut' nhe'",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	chatID := message.Chat.ID
	replyID := message.ID
	notify := func(text string) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          chatID,
			Text:            text,
			ReplyParameters: &models.ReplyParameters{MessageID: replyID},
		})
	}
	if t.startPlayerLogin(chatID, notify) {
		notify("go go!")
	}
}

// kickNoticeMsg words the disconnect notice for a player session. ownerChat
// differs from loginChat only for clone members, whose notices belong to the
// leader — there the username identifies WHICH account got kicked.
func kickNoticeMsg(ownerChat, loginChat int64, username string) string {
	if ownerChat == loginChat {
		return "bi da' ra roi` đạo hữu"
	}
	return "Acc " + username + " bị đá ra rồi đạo hữu"
}

// startPlayerLogin runs the shared, flood-protected login core for a chat. It
// enforces the already-running check, the 3-minute login-fail cooldown gate and
// the max-concurrent-player limit. notify delivers user-facing block/failure
// messages (manual login replies to the message; the scheduler pushes to
// notiChan). The eventual disconnect notice does NOT use notify — it goes via
// pushNotify so it arrives even when the login was triggered from the web.
// It returns true when a login was started; the caller sends its own
// success message on true.
// notifyChatFor picks where a session's game notifications (sendNotify)
// are delivered: a regular user or leader gets their own chat; a member —
// who may be a web-only clone with no Telegram chat of its own — has every
// notice routed to the leader who runs the account (the member name is
// prefixed on each message, so a leader watching many still tells them
// apart). The per-account Notify switch gates whether anything is sent.
func notifyChatFor(u *model.User, own int64) int64 {
	if u.LeaderID != "" {
		if lc, err := strconv.ParseInt(u.LeaderID, 10, 64); err == nil {
			return lc
		}
	}
	return own
}

func (t *TeleBot) startPlayerLogin(chatID int64, notify func(text string)) bool {
	v, ok := t.session.Load(chatID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			notify("chay 1 lan` thoi đạo hữu")
			return false
		}
	}

	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", chatID))
	if err != nil {
		log.Printf("startPlayerLogin error 1: %v", err)
		notify("server error")
		return false
	}

	// Owner 2026-11-11 ("acc hết vip rồi nhưng bot vẫn auto login"): this core
	// is the last line of defence — MỌI path (scheduler, web leader, debugweb)
	// đi qua đây đều phải dính gate ban+VIP, kể cả khi caller quên check.
	// Các caller hiện tại đã tự gate từ trước (auth()/webGate/login_all skips)
	// nên đây thuần là defence-in-depth, không đổi hành vi đường hợp lệ.
	if model.IsBanned(t.ydb.GetDB(), chatID) {
		notify("🚫 tài khoản này đã bị cấm sử dụng bot")
		return false
	}
	if u.VipLevel == 0 || u.VipExpireAt.Before(time.Now()) {
		notify("🛑 " + u.Username + " đã hết hạn VIP — bot không đăng nhập được. Gia hạn VIP rồi thử lại.")
		return false
	}

	if !u.LastLoginFail.IsZero() && time.Since(u.LastLoginFail) < 3*time.Minute {
		wait := 3*time.Minute - time.Since(u.LastLoginFail)
		notify(fmt.Sprintf("login fail, wait %v", wait.Round(time.Second)))
		return false
	}

	// Check max concurrent player
	if t.getRunningCount() >= t.getMaxPlayers() {
		notify("server is full, try again later")
		return false
	}

	p := &Player{}
	p.UID = uint64(chatID)
	p.Username = u.Username
	p.Password = u.Password
	p.ServerID = u.ServerID
	p.VipLevel = u.VipLevel
	// Regular users/leaders notify their own chat; a clone member's notices
	// go to the leader (notifyChatFor). The session key (chatID) stays the
	// account id in both cases.
	p.ChatID = notifyChatFor(u, chatID)
	p.NotiChan = t.notiChan
	p.AutoStartChan = t.autoStartChan
	p.ChuotMinDao = u.MinPeachLevel
	p.ChuotMinBinh = u.MinBottleLevel
	p.ChuotMinLuuly = u.MinLuuLyLevel
	p.BottlePrefered = u.BottlePrefered
	p.DB = t.ydb.GetDB()
	p.seedAutoGates(u)

	p.ClanMember = make(map[uint64]string)

	p.Notify = false
	// Web live wire: every Running flip re-pushes this account's state to
	// the owning browser (pushWebState routes member ids to the leader).
	p.OnRunningChange = func() { t.pushWebState(chatID) }

	if u.AutoMouse && chuotLoginRun(true, u.ChuotStartSchedule, u.ChuotStopSchedule, time.Now()) {
		p.ChuotRun = true
	}

	t.session.Store(chatID, p)
	u, err = model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", chatID))
	if err != nil {
		log.Printf("startPlayerLogin error 2: %v", err)
		t.sessionDel(chatID, p)
		return false
	}
	u.Running = true
	model.CreateUser(t.ydb.GetDB(), u)
	go func() {
		if !p.Run() {
			model.SetLastLoginFail(t.ydb.GetDB(), fmt.Sprintf("%d", chatID))
		}
		// The kick notice goes through the durable noti channel, NOT the
		// caller's notify closure: web logins pass throwaway closures (the
		// visitor's page is long gone by disconnect time), and a chat login
		// may sit behind quiet hours/reply context that no longer applies.
		// p.ChatID is the owner chat — own chat for a regular user, leader
		// for a clone member, so a web-triggered member login still notifies.
		t.pushNotify(p.ChatID, kickNoticeMsg(p.ChatID, chatID, p.Username))
		u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", chatID))
		if err != nil {
			log.Printf("startPlayerLogin error 3: %v", err)
			t.sessionDel(chatID, p)
			t.pushWebState(chatID)
			return
		}
		u.Running = false
		model.CreateUser(t.ydb.GetDB(), u)
		t.sessionDel(chatID, p)
		t.pushWebState(chatID) // covers login-fail and every disconnect path
	}()
	return true
}

// stopPlayerForChatID logs out the running player for chatID (if any),
// mirroring the /logout handler. Returns true when a running player was
// stopped. Used by the logout schedule.
func (t *TeleBot) stopPlayerForChatID(chatID int64) bool {
	v, ok := t.session.Load(chatID)
	if !ok {
		return false
	}
	// A delegated (uỷ thác) EventPlayer can sit under a user key (telebot_deputy.go)
	// — that's not this user's own Player session; skip it.
	p, isP := v.(*Player)
	if !isP || !p.Running {
		return false
	}
	if p.cancel != nil {
		p.cancel()
	}
	t.sessionDel(chatID, p)
	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", chatID))
	if err == nil {
		u.Running = false
		model.CreateUser(t.ydb.GetDB(), u)
	}
	t.pushWebState(chatID)
	return true
}

func (t *TeleBot) logoutHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	if message.Chat.Type != "private" {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "cut' nhe'",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if model.IsBanned(t.ydb.GetDB(), message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🚫 Bạn đã bị cấm sử dụng bot",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			if p.cancel != nil {
				p.cancel()
			}
			t.sessionDel(message.Chat.ID, p)

			u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
			if err == nil {
				u.Running = false
				model.CreateUser(t.ydb.GetDB(), u)
			}

			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "đã đăng xuất thành công",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "bạn chưa đăng nhập",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) GetConfigHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	user, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		log.Printf("GetConfigHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Build leader/member status
	leaderInfo := ""
	if user.IsLeader {
		memberCount := model.GetMembersCount(t.ydb.GetDB(), user.ID)
		leaderInfo = fmt.Sprintf(`	<code>│</code>
	<code>│</code> <b>👑 Leader Status</b>
	<code>│</code> • <b>Members:</b> %d
`, memberCount)
	} else if user.LeaderID != "" {
		leaderInfo = fmt.Sprintf(`	<code>│</code>
	<code>│</code> <b>👥 Member Of</b>
	<code>│</code> • <b>Leader ID:</b> <code>%s</code>
`, user.LeaderID)
	}

	lastFailStr := ""
	if !user.LastLoginFail.IsZero() {
		lastFailStr = fmt.Sprintf("\t<code>│</code> • <b>Last Fail:</b> %s\n", user.LastLoginFail.In(ictZone).Format("15:04:05 02/01"))
	}

	card := fmt.Sprintf(`<b>🎴 User Info</b>
	
	<code>┌─────────────────────────────</code>
	<b>👤 Personal Information</b>
	<code>│</code> • <b>Username:</b> %s
	<code>│</code> • <b>Password:</b> %s
	<code>│</code> • <b>Server ID:</b> %d
	<code>│</code> • <b>Vip Level:</b> %d
	<code>│</code> • <b>Vip Expire:</b> %s
	<code>│</code> • <b>Running:</b> %t
	<code>│</code> • <b>Auto Login:</b> %t
	<code>│</code> • <b>Auto Mouse:</b> %t
	<code>│</code> • <b>Auto Tụ Linh:</b> %t
%s	<code>│</code>
	<code>│</code> <b>⚙️ Mouse Details</b>
	<code>│</code> • <b>Min binh:</b> %d
	<code>│</code> • <b>Min dao:</b> %d
	<code>│</code> • <b>Min luuly:</b> %d
	<code>│</code> • <b>Min Tụ Linh:</b> %d
	<code>│</code> • <b>Bottle Prefered:</b> %t
	<code>│</code> • <b>Chuot Start:</b> %s
	<code>│</code> • <b>Chuot Stop:</b> %s
	<code>│</code> • <b>Login:</b> %s
	<code>│</code> • <b>Logout:</b> %s
%s	<code>└─────────────────────────────</code>`,

		user.Username,
		"******",
		user.ServerID,
		user.VipLevel,
		user.VipExpireAt.In(ictZone).Format("02/01/2006 15:04:05"),
		user.Running,
		user.AutoLogin,
		user.AutoMouse,
		user.AutoGather,
		lastFailStr,
		user.MinBottleLevel,
		user.MinPeachLevel,
		user.MinLuuLyLevel,
		user.MinGatherIncome,
		user.BottlePrefered,
		formatChuotTimes(user.ChuotStartSchedule),
		formatChuotTimes(user.ChuotStopSchedule),
		formatChuotTimes(user.LoginSchedule),
		formatChuotTimes(user.LogoutSchedule),
		leaderInfo,
	)
	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            card,
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	if err != nil {
		log.Println(err.Error())
	}

}

func (t *TeleBot) GetMember(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			card := "<b>🎴 Member Info</b>\n"
			for k, name := range p.ClanMember {
				card += fmt.Sprintf("%d: %s\n", k, name)
				//log.Println(v)
			}

			_, err := b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            card,
				ParseMode:       models.ParseModeHTML,
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			if err != nil {
				log.Println(err.Error())
			}
			return
		}
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login da đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

func (t *TeleBot) MyIDHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("Your Chat ID: <code>%d</code>", message.Chat.ID),
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) StartHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	var msg strings.Builder
	msg.WriteString(`<b>📜 Danh sách lệnh Bot (Command List)</b>

<b>🔐 Tài khoản (Account)</b>
• <code>/auth {key}</code>: Kích hoạt bot (VD: <code>/auth bananhviet</code>)
• <code>/login</code>: Đăng nhập vào game
• <code>/logout</code>: Đăng xuất khỏi game
• <code>/info</code>: Xem thông tin user & cấu hình
• <code>/ditich_map</code>: Vẽ map Thượng Cổ Di Tích (chủ phòng và số người từng phe, phải đang đăng nhập)
• <code>/bonglai_map</code>: Vẽ map sự kiện Đảo Bồng Lai (bang nào đang chiếm thành nào, phải đang đăng nhập)
• <code>/thienma_map</code>: Vẽ map sự kiện Đảo Thiên Ma (bản khốc liệt hơn của Đảo Bồng Lai, phải đang đăng nhập)
• <code>/cuuu_map</code>: Vẽ map sự kiện Cửu U (bang nào đang chiếm thành nào, thành nào đang giao tranh, phải đang đăng nhập)
• <code>/kunlun_map</code>: Vẽ map sự kiện Côn Lôn (toàn bản đồ 61×61 + thành đang giao tranh, phải đang đăng nhập)
• <code>/kunlun_info</code>: Xem pha hiện tại, tầng/nhóm, hạng và số thành đang giữ ở Côn Lôn
• <code>/kunlun_follow</code>: Lệnh mọi acc sự kiện đang treo hành quân tới đúng vị trí bang chủ ở Côn Lôn (tự vào trận khi chưa vào)
• <code>/kunlun_hoisinh [item]</code>: Hồi sinh quân Côn Lôn của mọi acc sự kiện đang chết — mặc định MIỄN PHÍ (bot cũng tự hồi sinh miễn phí khi tới hạn), thêm <code>item</code> để dùng vật phẩm hồi sinh ngay
• <code>/lk</code>: Xem bàn Luyện Khí Đại Hội (game ghép khí 2048 4×4 — thể lực, điểm, bàn hiện tại)
• <code>/lk_choi</code>: Bot TỰ CHƠI hết bàn Luyện Khí bằng nước đi MIỄN PHÍ (không dùng item gia tốc, không đổi thể lực) — xong báo điểm
• <code>/start_luyenkhi</code>: Như /lk_choi — vào thấy bàn đang chơi dở thì ĐÁNH TIẾP (không restart, không tự end game), xong ván notify kết quả
• <code>/lk_dung</code>: Dừng tự chơi Luyện Khí (vẫn chốt điểm phần đã chơi)
• <code>/bacminh_map</code>: Vẽ map sự kiện Đỉnh Bắc Minh (toàn bản đồ 119×119 + 81 thành, cùng máy engine Côn Lôn nhưng là giải đấu riêng, phải đang đăng nhập)
• <code>/bacminh_info</code>: Xem pha hiện tại, tầng/nhóm, hạng và số thành đang giữ ở Đỉnh Bắc Minh
• <code>/bacminh_follow</code>: Lệnh mọi acc sự kiện đang treo hành quân tới đúng vị trí bang chủ ở Đỉnh Bắc Minh (tự vào trận; acc đang ngồi Côn Lôn sẽ được báo rõ)
• <code>/bacminh_hoisinh [item]</code>: Hồi sinh quân Đỉnh Bắc Minh của mọi acc sự kiện đang chết — mặc định MIỄN PHÍ (bot cũng tự hồi sinh miễn phí khi tới hạn), thêm <code>item</code> để hồi sinh nhanh bằng vật phẩm (server tự trừ item hồi sinh, giống Côn Lôn)
• <code>/username {user}</code>: Cài username (VD: <code>/username user123</code>)
• <code>/password {pass}</code>: Cài password (VD: <code>/password 123456</code>)
• <code>/server_id {id}</code>: Cài Server ID, list server: https://raw.githubusercontent.com/xuanbach015/tytd/refs/heads/main/servers.json (VD: <code>/server_id 710010</code>)
• <code>/check_config</code>: Soi lại cấu hình login của bạn (username hiện nguyên văn + cảnh báo server_id lỗi/kí tự ẩn; leader xem thêm acc đáng ngờ). Không hiện mật khẩu.

<b>⚙️ Cấu hình (Settings)</b>
• <code>/auto_login {true/false}</code>: Tự động đăng nhập khi offline (VD: <code>/auto_login true</code>)
• <code>/auto_start_chuot {true/false}</code>: Tự động bật chuột khi login (VD: <code>/auto_start_chuot true</code>)
• <code>/auto_tu_linh {true/false}</code>: Bật/tắt vĩnh viễn tự động thu năng lượng Tụ Linh (VD: <code>/auto_tu_linh true</code>)
• <code>/tu_linh_min {số}</code>: Ngưỡng thu nhập tối thiểu của trận để tự ghép Tụ Linh (0 = không giới hạn, VD: <code>/tu_linh_min 100000</code>)
• <code>/auto_thiendo_tienvuc {true/false}</code>: Tự chạy thuyền Tiên Vực Thương Đồ — mua/bán 15 cảng, chỉ ra khơi khi chắc có lời (sự kiện 10:00–22:00 hằng ngày)
• <code>/auto_tangtoc_tienvuc {true/false}</code>: 21:58 tự tăng tốc cho thuyền về bến kịp giờ đóng; cuối ngày chưa có chuyến lời thì mua đặc sản cảng rồi dong buồm ngẫu nhiên cược giá sáng mai
• <code>/auto_luanhoi {true/false}</code>: Tự chơi Luân Hồi Bí Cảnh — bot chạy xe thư thương CẢ NGÀY khi vòng chiến mở (khám sự kiện, chọn phương án theo bảng mẹo — đánh bạc/giả kim chọn cửa đắt nhất, bái, nhận quà cây bang + tự phát nước cho cây bang (nộp sạch cả 3 loại nước đang có), nhận quà dự đoán, mua gói miễn phí/xem quảng cáo/gói tiên ngọc tới hạn ngày); trận chiến bang 19h bạn tự chơi, bot không đánh thay; bot TỰ CẮN QUẢ TĂNG THỂ LỰC khi xe cạn (chỉ loại quả thể lực, tối đa 5 quả/lần, dừng khi hết túi) — mọi item khác không bao giờ chạm, vé chiến vẫn chỉ cắn khi bạn ra <code>/luanhoi_ve</code>
• <code>/luanhoi_shop</code>: Xem cửa hàng Luân Hồi Bí Cảnh — danh sách gói, giá, số lần đã mua hôm nay, gói nào bot tự mua
• <code>/auto_tiencanh {true/false}</code>: Tự chơi Tiên Cảnh Vị Ương — bot cuốc bản đồ trong giờ khám phá (10:00–22:00): phá sương, đánh quái, giải sự kiện (sự kiện chọn phương án theo config — chọn cửa thưởng cao nhất), bái mỗi vòng, mở rương tầng, TỰ TƯỚI CÂY BANG bằng đủ mọi chai nước đang có (chỉ các id trong config sự kiện, không chạm item khác), nhận quà bang, tự mua gói miễn phí / xem quảng cáo / gói tiên ngọc tới hạn ngày; KHÔNG bao giờ cắn item tăng thể lực (xe cạn thì chờ hồi), trận chiến bang bot không đánh thay
• <code>/tiencanh_shop</code>: Xem cửa hàng Tiên Cảnh Vị Ương — danh sách gói, giá, số lần đã mua hôm nay, gói nào bot tự mua
• <code>/dangioi</code>: Xem nhanh ruộng Đan Giới — số lô theo trạng thái, nước, dược linh hôm nay/30, cây đang trồng, nhiệm vụ. Tự cày Đan Giới (Linh Điền) MẶC ĐỊNH chạy trên mọi acc không cần lệnh: mỗi phút thu lô chín + trồng lô trống cây x2 ngon nhất mùa (mùa 8: Huyền Cốt Sâm Vương), luôn bật "thu hút dược linh"; đủ 30 dược linh thì chuyển trồng theo nhiệm vụ ngày (dễ nhất trước) + tự nhận thưởng; server đã gỡ tưới nước; không bao giờ đốt item. Muốn tắt 1 acc: checkbox "TẮT tự cày Đan Giới" ở trang Cài đặt (web)
• <code>/luanhoi_attack [số]</code>: Xem bảng đấu Luân Hồi Bí Cảnh (kèm số thứ tự); tấn công base theo số — trận chiến bang luôn là quyết định của bạn, bot không tự đánh
• <code>/luanhoi_ve [1..5]</code>: Cắn vé tăng lượt đánh (dùng vé trong túi — chỉ chạy khi bạn ra lệnh này)
• <code>/luanhoi_map</code>: Ảnh chụp bảng đấu Luân Hồi Bí Cảnh + nút mở map trực tiếp
• <code>/auto_thuongminh {true/false}</code>: Tự chơi Thương Minh Yêu Vực — bot khám yêu vực CẢ NGÀY khi sự kiện mở (đánh quái, chọn người yếu nhất để đấu, mở rương, thám tuyền, luyện bảo thạch ƯU TIÊN thuộc tính SÁT THƯƠNG CUỐI, sang tầng miễn phí, bái, nhận phúc lợi + nước yêu minh); không đánh gia tộc chiến, không tự dùng vật phẩm thể lực; acc sự kiện tự chạy ngay khi login không cần lệnh
• <code>/thuongminh_suc [1..20]</code>: Dùng vật phẩm thể lực Thương Minh Yêu Vực cho acc chính + mọi acc sự kiện (chỉ chạy khi bạn ra lệnh này — bot không bao giờ tự đốt)
• <code>/bottle_pref {true/false}</code>: Ưu tiên nhặt bình (VD: <code>/bottle_pref true</code>)
• <code>/min_binh {level} [user_id]</code>: Level bình tối thiểu (VD: <code>/min_binh 4</code>)
• <code>/min_dao {level} [user_id]</code>: Level đào tối thiểu (VD: <code>/min_dao 5</code>)
• <code>/min_luuly {level} [user_id]</code>: Level lưu ly tối thiểu (VD: <code>/min_luuly 6</code>)

<b>🎮 Điều khiển (Game Control)</b>
• <code>/start_notify</code> | <code>/stop_notify</code>: Bật/tắt thông báo
• <code>/start_chuot</code> | <code>/stop_chuot</code>: Auto Chuột
• <code>/start_maohiem</code> | <code>/stop_maohiem</code>: Auto Mạo Hiểm
• <code>/start_lv100</code> | <code>/stop_lv100</code>: Auto Cày Acc lên Lv 100 (Chặt đào & mặc đồ xịn, nâng Tiên Thụ, tuyến chính, mạo hiểm)
• <code>/start_taydu</code> | <code>/stop_taydu</code>: Auto Tây Du Phục Ma (spam ải chính tuyến)
• <code>/start_taydu_team</code>: Leader bật auto Tây Du cho cả đội (mình + đệ tử)
• <code>/start_lucdao</code> | <code>/stop_lucdao</code>: Auto Lục Đạo
• <code>/start_yeuthap</code> | <code>/stop_yeuthap</code>: Auto Yêu Tháp
• <code>/start_huyet_mach</code> | <code>/stop_huyet_mach</code>: Auto Huyết Mạch (roll tối đa theo cấp, tự xoá kết quả)
• <code>/start_chat_cay</code> | <code>/stop_chat_cay</code>: Auto Chặt Cây
• <code>/start_chat_cay_xave</code>: Chặt cây có xả vé
• <code>/uythac {chat_id_leader}</code>: Uỷ thác acc này cho MỘT leader khác CHẠY EVENT — leader đó thấy acc ở tab Sự Kiện của họ, force login/logout + gửi lệnh event được mà không cần biết mật khẩu, không thấy ở tab Đệ Tử, không đổi mật khẩu/cấu hình/xoá được. Mỗi lần họ đăng nhập bạn nhận thông báo. Cấu hình chi tiết hơn ở trang Cài đặt (web).
• <code>/bo_uythac</code>: Thu hồi uỷ thác. • <code>/uythac_xem</code>: Xem acc đang uỷ thác cho ai
• <code>/schedule_start_chuot {thời_gian1} [thời_gian2] ...</code>: Đặt lịch bật chuột hàng ngày, hỗ trợ phút (VD: <code>/schedule_start_chuot 10:30 18 22:00</code>)
• <code>/schedule_stop_chuot {thời_gian1} [thời_gian2] ...</code>: Đặt lịch tắt chuột hàng ngày, hỗ trợ phút (VD: <code>/schedule_stop_chuot 6 12</code>)
• Lịch chuột là TRẠNG THÁI theo khoảng thời gian: đăng nhập vào giữa khoảng tắt (VD tắt 2:00 / bật 10:00, login 2:30) thì chuột vẫn TẮT cho tới giờ bật kế tiếp
• <code>/schedule_login {thời_gian1} [thời_gian2] ...</code>: Đặt lịch tự đăng nhập hàng ngày, hỗ trợ phút (VD: <code>/schedule_login 0:30 8 22:00</code>)
• <code>/schedule_logout {thời_gian1} [thời_gian2] ...</code>: Đặt lịch tự đăng xuất hàng ngày, hỗ trợ phút (VD: <code>/schedule_logout 5 23:30</code>)
• Gõ <code>/schedule_login off</code> hoặc <code>/schedule_logout off</code> để xóa lịch

<b>🎁 Tiện ích (Utilities)</b>
• <code>/redeem {CodeVIP}</code>: Nhập giftcode (VD: <code>/redeem skibidi</code>)
• <code>/my_id</code>: Xem Chat ID của bạn`)

	if t.isLeader(message.Chat.ID) {
		msg.WriteString(`
<b>👑 Leader Commands (Lệnh Leader)</b>
• <code>/add_member {user} {pass} {server}</code>: Tạo tài khoản member mới
• <code>/my_members</code>: Xem danh sách members
• <code>/remove_member {user_id}</code>: Xóa member
• <code>/member_login</code>: Đăng nhập tất cả members
• <code>/member_logout</code>: Đăng xuất tất cả members
• <code>/member_start_chat_cay</code>: Chặt cây tất cả members đang chạy
• <code>/member_start_chat_cay_xave</code>: Chặt cây có xả vé tất cả members đang chạy
• <code>/member_stop_chat_cay</code>: Dừng chặt cây tất cả members đang chạy
• <code>/member_start_chuot</code>: Bật chuột tất cả members đang chạy
• <code>/member_stop_chuot</code>: Dừng chuột tất cả members đang chạy
• <code>/member_schedule_start_chuot {thời_gian1} [thời_gian2] ...</code>: Đặt lịch bật chuột cho members, hỗ trợ phút (VD: <code>/member_schedule_start_chuot 10:30 18 22:00</code>)
• <code>/member_schedule_stop_chuot {thời_gian1} [thời_gian2] ...</code>: Đặt lịch tắt chuột cho members, hỗ trợ phút (VD: <code>/member_schedule_stop_chuot 6 12</code>)
• <code>/member_schedule_login {thời_gian1} [thời_gian2] ...</code>: Đặt lịch tự đăng nhập game cho tất cả members (VD: <code>/member_schedule_login 6 12:30</code>)
• <code>/member_schedule_logout {thời_gian1} [thời_gian2] ...</code>: Đặt lịch tự đăng xuất game cho tất cả members (VD: <code>/member_schedule_logout 22:30 23:50</code>)
• <code>/member_start_notify {user_id}</code> | <code>/member_stop_notify {user_id}</code>: Bật/tắt thông báo game của từng member đang treo (tin về chat leader kèm tên acc; leader bật /start_notify cho mình chỉ nhận tin acc mình)

<b>🎪 Event User (Acc sự kiện)</b>
• <code>/add_member_event {user} {pass} {server}</code>: Tạo tài khoản event_user (chỉ online xử lý event, không cần VIP, không auto)
• <code>/my_event_members</code>: Xem danh sách event_user
• <code>/remove_member_event {user_id}</code>: Xóa event_user
• <code>/event_login</code>: Đăng nhập (force) tất cả event_user
• <code>/event_logout</code>: Đăng xuất (force) tất cả event_user
• <code>/bonglai_move {city_id}</code>: Lệnh toàn bộ event_user đang chạy hành quân tới thành city_id ở Đảo Bồng Lai, chỉ đi không đánh (VD: <code>/bonglai_move 2005</code>)
• <code>/bonglai_attack {city_id}</code>: Lệnh toàn bộ event_user hành quân tới thành city_id và tự động tấn công, đánh cả người lẫn thủ vệ (⚠️ có thể đánh nhầm acc của mình)
• <code>/bonglai_attack_thuve {city_id}</code>: Lệnh toàn bộ event_user hành quân tới thành city_id và chỉ đánh thủ vệ NPC
• <code>/bonglai_canqua [số_quả]</code>: Lệnh toàn bộ event_user dùng số quả ở Đảo Bồng Lai để tăng lực chiến (không nhập = 50)
• <code>/bonglai_tangtoc</code>: Lệnh toàn bộ event_user mua thêm 1 lần tăng tốc hành quân ở Đảo Bồng Lai (chỉ có tác dụng khi quân đang hành quân)
• <code>/bonglai_hoimau</code>: Lệnh toàn bộ event_user dùng linh lực cầu hồi đầy máu ở Đảo Bồng Lai
• <code>/thienma_move {city_id}</code>: Giống <code>/bonglai_move</code> nhưng áp dụng cho Đảo Thiên Ma (VD: <code>/thienma_move 2005</code>)
• <code>/thienma_attack {city_id}</code>: Giống <code>/bonglai_attack</code> nhưng áp dụng cho Đảo Thiên Ma (⚠️ có thể đánh nhầm acc của mình)
• <code>/thienma_attack_thuve {city_id}</code>: Giống <code>/bonglai_attack_thuve</code> nhưng áp dụng cho Đảo Thiên Ma
• <code>/thienma_canqua [số_quả]</code>: Giống <code>/bonglai_canqua</code> nhưng dùng quả ở Đảo Thiên Ma (không nhập = 50)
• <code>/thienma_tangtoc</code>: Giống <code>/bonglai_tangtoc</code> nhưng áp dụng cho Đảo Thiên Ma
• <code>/thienma_hoimau</code>: Giống <code>/bonglai_hoimau</code> nhưng áp dụng cho Đảo Thiên Ma
• <code>/cuuu_move {city_id}</code>: Lệnh toàn bộ event_user đang chạy hành quân tới thành city_id ở Cửu U, chỉ đi không đánh (VD: <code>/cuuu_move 1005</code>)
• <code>/cuuu_attack {city_id}</code>: Lệnh toàn bộ event_user đánh thành city_id ở Cửu U một trận, đánh cả người lẫn thủ vệ (⚠️ có thể đánh nhầm acc của mình)
• <code>/cuuu_attack_thuve {city_id}</code>: Lệnh toàn bộ event_user đánh thành city_id ở Cửu U nhưng chỉ đánh thủ vệ NPC
• <code>/cuuu_auto {city_id|0}</code>: Lệnh toàn bộ event_user treo máy tự đánh tại thành city_id ở Cửu U (0 = tắt treo máy)
• <code>/cuuu_tangtoc</code>: Lệnh toàn bộ event_user mua thêm 1 lần tăng tốc hành quân ở Cửu U (chỉ có tác dụng khi quân đang hành quân)
• <code>/cuuu_hoisinh [item]</code>: Lệnh toàn bộ event_user đang chết hồi sinh ở Cửu U (mặc định miễn phí nếu hết hồi cooldown; thêm <code>item</code> để hồi sinh instant bằng vật phẩm). Bot cũng tự hồi sinh miễn phí khi tới lượt
• <code>/ditich_rooms</code>: Xem danh sách cứ điểm Thượng Cổ Di Tích (id, tên, phe đang giữ, điểm tập kết)
• <code>/ditich_enter {cứ_điểm}</code>: Lệnh toàn bộ event_user vào cứ điểm đó ở Thượng Cổ Di Tích (bang cần đăng ký acc trong game trước; vào phòng rồi bot tự đi và tự đánh quân địch gần nhất)
• <code>/ditich_ve</code>: Lệnh toàn bộ event_user về cứ điểm nhà của phe mình ở Thượng Cổ Di Tích
• <code>/ditich_leave</code>: Lệnh toàn bộ event_user rời cứ điểm hiện tại ở Thượng Cổ Di Tích
• <code>/ditich_roster</code>: Xem danh sách người trong cứ điểm mà mỗi event_user đang đứng (để nắm tình hình; việc đánh tự động)
• <code>/ditich_mark {cứ_điểm}</code>: Đánh dấu điểm tập kết của phe ở Thượng Cổ Di Tích (game chỉ nhận lệnh từ acc trưởng phe)`)
	}

	if t.isAdmin(message.Chat.ID) {
		msg.WriteString(`
<b>👑 Admin Commands (Lệnh Admin)</b>

<b>🎫 Gift Code</b>
• <code>/add_code {code} {max_usage}</code>: Tạo gift code
• <code>/gen_codes {số} {max_usage}</code>: Sinh nhiều gift code
• <code>/del_code {code}</code>: Xóa gift code
• <code>/list_codes</code>: Xem danh sách gift code

<b>🚫 Blacklist Clan</b>
• <code>/add_black_clan {clan_id}</code>: Thêm clan vào blacklist
• <code>/del_black_clan {clan_id}</code>: Xóa clan khỏi blacklist
• <code>/list_black_clan</code>: Xem danh sách blacklist clan

<b>⚙️ Cấu hình hệ thống</b>
• <code>/get_setting</code>: Xem cấu hình hệ thống
• <code>/set_max_player {số}</code>: Đặt số người chơi tối đa
• <code>/set_debug {true/false}</code>: Bật/tắt debug
• <code>/set_auth_key {key}</code>: Đặt auth key
• <code>/set_must_join_group {true/false}</code>: Bắt buộc join group
• <code>/set_auto_login {true/false}</code>: Auto login toàn hệ thống
• <code>/set_auto_tu_linh {true/false}</code>: cổng Tụ Linh toàn cục — tắt là MỌI acc ngừng tự thu năng lượng (giống /set_auto_login)
• <code>/set_temp_auto {true/false}</code>: Bật/tắt temp auto
• <code>/broadcast {message}</code>: Gửi thông báo cho tất cả user

<b>🔌 MCP (HTTP cho agent/AI)</b>
• <code>/mcp on|off</code>: Bật/tắt MCP server (port MCP_PORT, mặc định :8090)
• <code>/mcp key {key}</code> | <code>/mcp key generate</code>: Đặt key xác thực (mọi request phải kèm <code>X-MCP-Key</code> hoặc <code>Authorization: Bearer</code>)
• <code>/mcp clear</code>: Xoá key (endpoint trả 503) • <code>/mcp status</code>: Xem trạng thái
• Ai giữ key có quyền xem/sửa acc đang online qua MCP — thông tin acc không bao gồm mật khẩu

<b>♟ Big Chess Engine (engine phân tích ngoài)</b>
• <code>/bcengine on|off</code>: Bật/tắt engine chơi Cờ Lớn (tool <code>bigchess_analyze</code> + học transition)
• <code>/bcengine url {http://host:port}</code>: Địa chỉ engine (mặc định <code>http://127.0.0.1:8780</code>)
• <code>/bcengine token {token}</code> | <code>/bcengine token clear</code> • <code>/bcengine status</code> • <code>/bcengine test</code>: Thử kết nối

<b>👑 Quản lý Leader</b>
• <code>/set_leader {chat_id} {true/false}</code>: Đặt/gỡ leader
• <code>/list_leaders</code>: Xem danh sách leaders

<b>🚫 Cấm user</b>
• <code>/ban_user {chat_id} [lý do]</code>: Cấm user
• <code>/unban_user {chat_id}</code>: Mở cấm user
• <code>/list_banned</code>: Xem danh sách user bị cấm

<b>✅ Good User</b>
• <code>/add_good_user {chat_id}</code>: Thêm good user
• <code>/remove_good_user {chat_id}</code>: Xóa good user
• <code>/list_good_users</code>: Xem danh sách good users

<b>🤖 AI Chatbot</b>
• <code>/set_groq_key {api_key}</code>: Đặt Groq API key
• <code>/set_copilot_key {api_key}</code>: Đặt Copilot API key
• <code>/set_chatbot {true/false}</code>: Bật/tắt chatbot
• <code>/set_group_chat_id {chat_id}</code>: Đặt chat ID nhóm`)
	}

	// Telegram caps a single message at 4096 chars; for admin (and admin+leader)
	// users the combined help text is longer, so split it into line-aligned chunks
	// and send each as a separate message. Chaining ReplyParameters to the previous
	// message keeps the chunks threaded near the original /start message.
	var replyID int
	first := true
	for _, chunk := range splitTelegramMessage(msg.String(), 4096) {
		params := &bot.SendMessageParams{
			ChatID:    message.Chat.ID,
			Text:      chunk,
			ParseMode: models.ParseModeHTML,
		}
		if first {
			params.ReplyParameters = &models.ReplyParameters{MessageID: message.ID}
			first = false
		} else if replyID > 0 {
			params.ReplyParameters = &models.ReplyParameters{MessageID: replyID}
		}
		m, err := b.SendMessage(ctx, params)
		if err != nil {
			log.Println(err.Error())
			break
		}
		replyID = m.ID
	}
}

// splitTelegramMessage splits text into chunks of at most maxLen characters,
// breaking only at newline boundaries so each chunk stays valid HTML.
func splitTelegramMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var chunks []string
	var cur []string
	curLen := 0
	for _, line := range strings.Split(text, "\n") {
		add := len(line)
		if curLen > 0 {
			add++ // newline separator
		}
		if curLen > 0 && curLen+add > maxLen {
			chunks = append(chunks, strings.Join(cur, "\n"))
			cur = nil
			curLen = 0
			add = len(line)
		}
		cur = append(cur, line)
		curLen += add
	}
	if curLen > 0 {
		chunks = append(chunks, strings.Join(cur, "\n"))
	}
	return chunks
}
