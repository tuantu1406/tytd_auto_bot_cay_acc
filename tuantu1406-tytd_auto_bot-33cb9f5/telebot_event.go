package tytd_go

import (
	"context"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// sessionRunning reports whether a value stored in t.session is a running
// player. The map holds both *Player (regular users/members) and *EventPlayer
// (event users), so callers must not assert a single concrete type.
func sessionRunning(v interface{}) bool {
	switch p := v.(type) {
	case *Player:
		return p.Running
	case *EventPlayer:
		return p.Running
	}
	return false
}

// getOwnedEventUser returns the event user only when it exists and belongs to
// this leader's chat.
func (t *TeleBot) getOwnedEventUser(memberID string, leaderChatID int64) (*model.EventUser, bool) {
	u, err := model.GetEventUser(t.ydb.GetDB(), memberID)
	if err != nil || u.LeaderID != fmt.Sprintf("%d", leaderChatID) {
		return nil, false
	}
	return u, true
}

// startEventPlayerLogin runs the gated login core for an event_user account,
// the event counterpart of startPlayerLogin. It enforces the same gates: the
// already-running check, the ban check, the 3-minute login-fail cooldown and
// the max-concurrent-player limit ("max online"). It skips every VIP check
// because event users have no VIP level. The account runs the reduced RunEvent
// loop (no auto-play goroutines) and its notifications go to the leader chat.
// notify delivers user-facing block/failure messages; the eventual disconnect
// notice goes via pushNotify (survives web-triggered logins). It returns true
// when a login was started.
func (t *TeleBot) startEventPlayerLogin(eventID string, leaderChatID int64, notify func(text string)) bool {
	realUID, err := strconv.ParseInt(eventID, 10, 64)
	if err != nil {
		notify("invalid event user id")
		return false
	}

	// Skip if already running (the shared session map can hold either type)
	if v, ok := t.session.Load(realUID); ok && sessionRunning(v) {
		notify("chay 1 lan` thoi đạo hữu")
		return false
	}

	// Skip if banned
	if model.IsBanned(t.ydb.GetDB(), realUID) {
		notify("🚫 Bạn đã bị cấm sử dụng bot")
		return false
	}

	u, err := model.GetEventUser(t.ydb.GetDB(), eventID)
	if err != nil {
		notify("event user not found")
		return false
	}

	// Gate: cooldown after a recent login failure
	if !u.LastLoginFail.IsZero() && time.Since(u.LastLoginFail) < 3*time.Minute {
		wait := 3*time.Minute - time.Since(u.LastLoginFail)
		notify(fmt.Sprintf("login fail, wait %v", wait.Round(time.Second)))
		return false
	}

	// Same game account@server hanging under another key (the account owner's
	// Player, or another leader's event session): take it down BEFORE the
	// slots gate — it frees the very slot this login will use, so a takeover
	// can never be wrongly told "server is full", and the twin never stays
	// behind "ôm" memory for a connection the game already dropped
	// (owner 2026-10-10).
	t.stopGameAccountSessions(eventID, u.Username, u.ServerID)

	// Gate: max concurrent player (event users count toward the same limit)
	if t.getRunningCount() >= t.getMaxPlayers() {
		notify("server is full, try again later")
		return false
	}

	p := &EventPlayer{}
	p.UID = uint64(realUID)
	p.Username = u.Username
	p.Password = u.Password
	p.ServerID = u.ServerID
	// An event user has no Telegram chat of its own, so its notifications go
	// to the leader. The session key (realUID) stays the account id, like a
	// member.
	p.ChatID = leaderChatID
	p.NotiChan = t.notiChan
	p.DB = t.ydb.GetDB()
	p.Notify = false
	// Leader's off-switches ride in from the DB row (default false = "login
	// là chạy" preserved for every pre-existing account).
	p.OffRbt, p.OffSeaWar = u.OffRebornTrial, u.OffSeaWar
	// Web live wire (see startPlayerLogin): flips re-push the LEADER's state
	// frame so the Sự kiện row pills follow the account.
	p.OnRunningChange = func() { t.pushWebState(realUID) }

	t.session.Store(realUID, p)
	u.Running = true
	model.CreateEventUser(t.ydb.GetDB(), u)
	username := u.Username
	go func() {
		if !p.Run() {
			model.SetEventUserLastLoginFail(t.ydb.GetDB(), eventID)
		}
		// Durable route (see startPlayerLogin): web login_all/add pass a
		// throwaway notify closure, so the kick notice goes to the leader via
		// the noti channel instead — p.ChatID already IS the leader chat.
		t.pushNotify(p.ChatID, "bi da' ra roi` đạo hữu "+username)
		u2, err := model.GetEventUser(t.ydb.GetDB(), eventID)
		if err != nil {
			log.Printf("startEventPlayerLogin error: %v", err)
			t.sessionDel(realUID, p)
			t.pushWebState(realUID)
			return
		}
		u2.Running = false
		model.CreateEventUser(t.ydb.GetDB(), u2)
		t.sessionDel(realUID, p)
		t.pushWebState(realUID)
	}()
	return true
}

// forceStopEventUser cancels the running event player for eventID (if any),
// mirroring the member logout path. Returns true when a running player was
// stopped.
func (t *TeleBot) forceStopEventUser(eventID string) bool {
	realUID, err := strconv.ParseInt(eventID, 10, 64)
	if err != nil {
		return false
	}
	v, ok := t.session.Load(realUID)
	if !ok {
		return false
	}
	p, ok2 := v.(*EventPlayer)
	if !ok2 {
		// Foreign session entry (regular user/member with the same key —
		// prevented by the 8/9 ID prefixes, but never touch what we don't own)
		return false
	}
	if !p.Running {
		// Stale entry not actually running — clean it up
		t.sessionDel(realUID, p)
		return false
	}
	if p.cancel != nil {
		p.cancel()
	}
	t.sessionDel(realUID, p)
	if u, err := model.GetEventUser(t.ydb.GetDB(), eventID); err == nil {
		u.Running = false
		model.CreateEventUser(t.ydb.GetDB(), u)
	}
	t.pushWebState(realUID)
	return true
}

// stopGameAccountSessions gracefully cancels every RUNNING session — Player or
// EventPlayer, under any session key — that hangs the same game
// account@server as the event login about to start. The game keeps exactly one
// connection per account@server, so a leftover twin becomes a half-dead hang:
// its websocket keeps dying/retrying while the bot still "ôm" the buffers and
// goroutines for it (owner 2026-10-10: leader logs an event account whose game
// acc is online as a Player — kill the Player first, tránh ôm). Victims stop
// through ctx-cancel, i.e. Run() returns cleanly — NO last-login-fail cooldown,
// and each announces itself to its own chat like a normal kick. eventID's own
// key is never touched (the same-key double-login guard already refused that
// case). Returns how many sessions were stopped.
func (t *TeleBot) stopGameAccountSessions(eventID, username string, serverID int) int {
	if username == "" {
		return 0
	}
	self, _ := strconv.ParseInt(eventID, 10, 64)
	stopped := 0
	t.session.Range(func(k, v interface{}) bool {
		key, ok := k.(int64)
		if !ok || key == self {
			return true
		}
		switch p := v.(type) {
		case *Player:
			if !p.Running || p.Username != username || p.ServerID != serverID {
				return true
			}
			if p.cancel != nil {
				p.cancel()
			}
			t.sessionDel(key, p)
			if nu, err := model.GetUser(t.ydb.GetDB(), strconv.FormatInt(key, 10)); err == nil {
				nu.Running = false
				model.CreateUser(t.ydb.GetDB(), nu)
			}
			t.pushWebState(key)
			stopped++
		case *EventPlayer:
			if !p.Running || p.Username != username || p.ServerID != serverID {
				return true
			}
			if p.cancel != nil {
				p.cancel()
			}
			t.sessionDel(key, p)
			if nu, err := model.GetEventUser(t.ydb.GetDB(), strconv.FormatInt(key, 10)); err == nil {
				nu.Running = false
				model.CreateEventUser(t.ydb.GetDB(), nu)
			}
			t.pushWebState(key)
			stopped++
		}
		return true
	})
	if stopped > 0 {
		log.Printf("[event] acc game %s@%d đang treo %d session ở nơi khác — đã tắt trước khi login event (tránh ôm kép)", username, serverID, stopped)
	}
	return stopped
}

// AddEventMemberHandler allows leaders to create event_user accounts, the
// event counterpart of /add_member. Event users have no VIP level; login goes
// through startEventPlayerLogin so the gate checks (login fail cooldown, max
// online, ...) still apply.
func (t *TeleBot) AddEventMemberHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /add_member_event <username> <password> <server_id>",
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

	// Generate random event user ID ("8" prefix, see GenerateRandomEventUserID)
	randomID := model.GenerateRandomEventUserID(t.ydb.GetDB())

	// Create event user account (no VIP level needed)
	u := &model.EventUser{
		ID:       randomID,
		Username: username,
		Password: password,
		ServerID: serverID,
		LeaderID: fmt.Sprintf("%d", message.Chat.ID),
	}

	err = model.CreateEventUser(t.ydb.GetDB(), u)
	if err != nil {
		log.Printf("AddEventMemberHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to create event user: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("Event user created!\nUser ID: <code>%s</code>\nUsername: %s\nServer: %d", randomID, username, serverID),
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	// login event user (gated)
	chatID := message.Chat.ID
	replyID := message.ID
	notify := func(text string) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          chatID,
			Text:            text,
			ReplyParameters: &models.ReplyParameters{MessageID: replyID},
		})
	}
	if t.startEventPlayerLogin(randomID, chatID, notify) {
		notify("go go! logged in")
	}
}

// RemoveEventMemberHandler allows leaders to remove event_user accounts
func (t *TeleBot) RemoveEventMemberHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /remove_member_event <user_id>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	memberID := args[1]

	if _, ok := t.getOwnedEventUser(memberID, message.Chat.ID); !ok {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "event user not found",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Force-stop it first if running, then delete
	t.forceStopEventUser(memberID)

	err := model.DeleteEventUser(t.ydb.GetDB(), memberID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to delete event user: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("Event user %s deleted successfully", memberID),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// godIslandMemberStatus is one God Island status line for /my_event_members
// (which = GIEventNormal for Bồng Lai, GIEventPeak for Thiên Ma): prefer the
// live EventPlayer cache (activity/death/city state comes from its 3s
// INFO_LOAD poll); fall back to the DB flag when no session is resident. The
// bool says whether the line is worth showing at all: without a live session
// the Thiên Ma state is unknowable, and even with one it stays hidden until
// the server announces that event variant for this account.
func (t *TeleBot) godIslandMemberStatus(m model.EventUser, which int) (string, bool) {
	if realUID, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
		if v, ok := t.session.Load(realUID); ok {
			if p, ok2 := v.(*EventPlayer); ok2 {
				if which == GIEventPeak && !p.GodIslandEventOpen(which) {
					return "", false
				}
				return p.GodIslandStatus(which), true
			}
		}
	}
	if m.Running {
		return "🔌 mất kết nối", which == GIEventNormal
	}
	return "⏹️ chưa login", which == GIEventNormal
}

// holyLandMemberStatus is one Holy Land status line for /my_event_members.
// Unlike God Island there is no DB fallback: the state (open/dead/marching)
// only lives in the live EventPlayer poll, so without a resident session — or
// while the server has not opened the event for that account — there is no
// honest line to show and the row stays hidden.
func (t *TeleBot) holyLandMemberStatus(m model.EventUser) (string, bool) {
	if realUID, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
		if v, ok := t.session.Load(realUID); ok {
			if p, ok2 := v.(*EventPlayer); ok2 && p.HolyLandEventOpen() {
				return p.HolyLandStatus(), true
			}
		}
	}
	return "", false
}

// ListMyEventMembersHandler shows all event users belonging to a leader
func (t *TeleBot) ListMyEventMembersHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetEventUsersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load event users: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no event users yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	var sb strings.Builder
	header := "<b>🎪 Your Event Users</b>\n\n"
	sb.WriteString(header)

	for i, m := range members {
		runningStatus := "Stopped"
		if m.Running {
			runningStatus = "Running"
		}

		memberInfo := fmt.Sprintf("%d. <code>%s</code>\n", i+1, html.EscapeString(m.ID))
		memberInfo += fmt.Sprintf("   • Username: %s\n", html.EscapeString(m.Username))
		memberInfo += fmt.Sprintf("   • Server: %d\n", m.ServerID)
		memberInfo += fmt.Sprintf("   • Status: %s\n", runningStatus)
		if line, show := t.godIslandMemberStatus(m, GIEventNormal); show {
			memberInfo += fmt.Sprintf("   • Bồng Lai: %s\n", html.EscapeString(line))
		}
		if line, show := t.godIslandMemberStatus(m, GIEventPeak); show {
			memberInfo += fmt.Sprintf("   • Thiên Ma: %s\n", html.EscapeString(line))
		}
		if line, show := t.holyLandMemberStatus(m); show {
			memberInfo += fmt.Sprintf("   • Cửu U: %s\n", html.EscapeString(line))
		}
		if line, show := t.ballGVGMemberStatus(m); show {
			memberInfo += fmt.Sprintf("   • Thượng Cổ: %s\n", html.EscapeString(line))
		}
		if line, show := t.kunlunMemberStatus(m); show {
			memberInfo += fmt.Sprintf("   • Côn Lôn: %s\n", html.EscapeString(line))
		}
		if line, show := t.bacminhMemberStatus(m); show {
			memberInfo += fmt.Sprintf("   • Bắc Minh: %s\n", html.EscapeString(line))
		}
		if !m.LastLoginFail.IsZero() {
			memberInfo += fmt.Sprintf("   • Last Fail: %s\n", m.LastLoginFail.In(ictZone).Format("15:04:05 02/01"))
		}
		memberInfo += "\n"

		// Telegram message limit is 4096. We use 3600 to be safe.
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

// EventLoginHandler allows leaders to force login all of their event users at
// once, like /member_login. Gate checks (cooldown, max online, ban) apply per
// account; VIP is not checked.
func (t *TeleBot) EventLoginHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetEventUsersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load event users: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no event users yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	started := 0
	skipped := 0
	remaining := 0

	// Event users have no chat of their own: login blocks go to the leader's
	// chat here; the eventual "bi da' ra roi`" notice is routed by the login
	// core itself through the noti channel (no reply reference, since the
	// message is long gone by disconnect time).
	notify := func(text string) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID,
			Text:   text,
		})
	}

	for i, u := range members {
		realUID, _ := strconv.ParseInt(u.ID, 10, 64)

		// Skip if already running
		if val, ok := t.session.Load(realUID); ok && sessionRunning(val) {
			skipped++
			continue
		}

		// Skip if in cooldown after a recent login failure
		if !u.LastLoginFail.IsZero() && time.Since(u.LastLoginFail) < 3*time.Minute {
			skipped++
			continue
		}

		// Check max concurrent players (count members already started in this batch)
		if t.getRunningCount()+started >= t.getMaxPlayers() {
			remaining = len(members) - i
			break
		}

		if t.startEventPlayerLogin(u.ID, message.Chat.ID, notify) {
			started++
		} else {
			skipped++
		}
	}

	msg := fmt.Sprintf("🚀 Started %d event user(s)", started)
	if skipped > 0 {
		msg += fmt.Sprintf(", skipped %d (running, banned or cooldown)", skipped)
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

// EventLogoutHandler allows leaders to force logout all of their event users
// at once, like /member_logout.
func (t *TeleBot) EventLogoutHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetEventUsersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load event users: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no event users yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	loggedOut := 0
	alreadyStopped := 0

	for _, u := range members {
		if t.forceStopEventUser(u.ID) {
			loggedOut++
		} else {
			alreadyStopped++
		}
	}

	msg := fmt.Sprintf("👋 Logged out %d event user(s)", loggedOut)
	if alreadyStopped > 0 {
		msg += fmt.Sprintf(", %d already stopped", alreadyStopped)
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            msg,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// eventOrderWithArg is the shared skeleton of the leader-only island orders
// (/bonglai_* and /thienma_* move/attack/attack_thuve/canqua, /cuuu_*
// move/attack/attack_thuve — the order closure names the event variant):
// parse one int arg (argDefault > 0 makes it optional,
// e.g. the fruit count), run order against every running event user of the
// leader, and report moved/skipped/failed with doneFmt (fmt string taking
// moved + arg). Event accounts have no auto-play: these commands are the only
// thing that makes them act. Accounts that are not logged in are skipped;
// every refusal is reported per account.
func (t *TeleBot) eventOrderWithArg(ctx context.Context, b *bot.Bot, update *models.Update, usage, argName, doneFmt string, argDefault int32, order func(*EventPlayer, int32) error) {
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
	arg := argDefault
	switch {
	case len(args) == 1 && argDefault > 0:
		// arg omitted → default (already in arg)
	case len(args) == 2:
		n, err := strconv.ParseInt(args[1], 10, 32)
		if err != nil || n <= 0 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            argName + " không hợp lệ: " + args[1],
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
		arg = int32(n)
	default:
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: " + usage,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	t.eventOrderBroadcast(ctx, b, update, func(moved int) string {
		return fmt.Sprintf(doneFmt, moved, arg)
	}, func(p *EventPlayer) error { return order(p, arg) })
}

// eventOrderBroadcast is the leader-gated core shared by all island orders
// (God Island and Holy Land alike): run order against every running event user
// of the leader, then send summary(moved) plus skipped/failed lines. Commands
// that parse an argument reach it through eventOrderWithArg; argument-less
// ones (like /bonglai_hoimau) or self-parsed ones (like /cuuu_auto, where
// 0 is a legal argument) call it directly. Accounts that are not logged in are
// skipped; every refusal is reported per account.
func (t *TeleBot) eventOrderBroadcast(ctx context.Context, b *bot.Bot, update *models.Update, summary func(moved int) string, order func(*EventPlayer) error) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetEventUsersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load event users: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(members) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "You have no event users yet",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	moved := 0
	notRunning := 0
	var fails []string
	for _, u := range members {
		realUID, err := strconv.ParseInt(u.ID, 10, 64)
		if err != nil {
			notRunning++
			continue
		}
		v, ok := t.session.Load(realUID)
		if !ok {
			notRunning++
			continue
		}
		p, ok2 := v.(*EventPlayer)
		if !ok2 || !p.Running {
			notRunning++
			continue
		}
		if err := order(p); err != nil {
			fails = append(fails, u.Username+": "+err.Error())
			continue
		}
		moved++
	}

	msg := summary(moved)
	if notRunning > 0 {
		msg += fmt.Sprintf(", %d bỏ qua (chưa login)", notRunning)
	}
	if len(fails) > 0 {
		msg += "\n" + strings.Join(fails, "\n")
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            msg,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// The God Island order handlers are variant-agnostic: every /bonglai_*
// command has a /thienma_* twin that drives the peak island instead. Each
// pair shares one implementation which threads the event selector into the
// EventPlayer commands; usage/summary strings name the variant so a leader
// never confuses the two islands.

// giCmdPrefix is the telegram command prefix of one event variant (with the
// trailing underscore, e.g. "/bonglai_move" = giCmdPrefix(GIEventNormal) +
// "move"). It matches giEvent.CmdPrefix.
func giCmdPrefix(which int) string {
	if which == GIEventPeak {
		return "/thienma_"
	}
	return "/bonglai_"
}

// giEventLabel is the Vietnamese display name of one event variant.
func giEventLabel(which int) string {
	if which == GIEventPeak {
		return "Thiên Ma"
	}
	return "Bồng Lai"
}

// giMoveHandler backs /bonglai_move and /thienma_move: march every running
// event user of the leader to that city of the variant. Move only — fighting
// is giAttackHandler / giAttackThuVeHandler.
func (t *TeleBot) giMoveHandler(ctx context.Context, b *bot.Bot, update *models.Update, which int) {
	prefix, label := giCmdPrefix(which), giEventLabel(which)
	t.eventOrderWithArg(ctx, b, update, prefix+"move <city_id> (VD: "+prefix+"move 2005)", "city id",
		fmt.Sprintf("🏝️ Đã lệnh %%d event user (%s) hành quân tới thành %%d", label), 0,
		func(p *EventPlayer, cityID int32) error { return p.GodIslandMoveTo(which, cityID) })
}

// giAttackHandler backs /bonglai_attack and /thienma_attack: march every
// running event user of the leader to the city and auto-attack everything
// there, union players included (friendly fire on other event accounts of
// this same leader is the leader's problem).
func (t *TeleBot) giAttackHandler(ctx context.Context, b *bot.Bot, update *models.Update, which int) {
	prefix, label := giCmdPrefix(which), giEventLabel(which)
	t.eventOrderWithArg(ctx, b, update, prefix+"attack <city_id> (VD: "+prefix+"attack 2005)", "city id",
		fmt.Sprintf("⚔️ Đã lệnh %%d event user (%s) tấn công thành %%d", label), 0,
		func(p *EventPlayer, cityID int32) error { return p.GodIslandAttackTo(which, cityID, false) })
}

// giAttackThuVeHandler backs /bonglai_attack_thuve and
// /thienma_attack_thuve: march every running event user of the leader to the
// city and auto-attack only its NPC guards (thủ vệ), leaving union players
// alone.
func (t *TeleBot) giAttackThuVeHandler(ctx context.Context, b *bot.Bot, update *models.Update, which int) {
	prefix, label := giCmdPrefix(which), giEventLabel(which)
	t.eventOrderWithArg(ctx, b, update, prefix+"attack_thuve <city_id> (VD: "+prefix+"attack_thuve 2005)", "city id",
		fmt.Sprintf("🛡️ Đã lệnh %%d event user (%s) đánh thủ vệ thành %%d", label), 0,
		func(p *EventPlayer, cityID int32) error { return p.GodIslandAttackTo(which, cityID, true) })
}

// giDefaultFruitCount is how many fruits the canqua commands use when the
// leader doesn't name a number.
const giDefaultFruitCount = 50

// giCanQuaHandler backs /bonglai_canqua and /thienma_canqua [count]: every
// running event user of the leader consumes count (default 50) of its fruits
// of the variant to boost its power.
func (t *TeleBot) giCanQuaHandler(ctx context.Context, b *bot.Bot, update *models.Update, which int) {
	prefix, label := giCmdPrefix(which), giEventLabel(which)
	t.eventOrderWithArg(ctx, b, update, prefix+"canqua [số_quả] (VD: "+prefix+"canqua 100, không nhập = 50)", "số quả",
		fmt.Sprintf("🍎 Đã cho %%d event user (%s) dùng %%d quả", label), giDefaultFruitCount,
		func(p *EventPlayer, count int32) error { return p.GodIslandUseFruit(which, count) })
}

// giTangTocHandler backs /bonglai_tangtoc and /thienma_tangtoc (no args):
// every running event user of the leader buys one more move speed-up on the
// variant (GodIslandAccelerate). The request carries each account's CURRENT
// count (from the residency INFO_LOAD poll and the previous accelerate
// response); the server performs the increment. Only accounts that are
// marching have a route, so the rest refuse per account.
func (t *TeleBot) giTangTocHandler(ctx context.Context, b *bot.Bot, update *models.Update, which int) {
	prefix, label := giCmdPrefix(which), giEventLabel(which)
	if len(strings.Fields(update.Message.Text)) != 1 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: " + prefix + "tangtoc (không tham số)",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
		return
	}
	t.eventOrderBroadcast(ctx, b, update,
		func(moved int) string {
			return fmt.Sprintf("🚀 Đã lệnh %d event user (%s) tăng tốc", moved, label)
		},
		func(p *EventPlayer) error { return p.GodIslandAccelerate(which) })
}

// giHoiMauHandler backs /bonglai_hoimau and /thienma_hoimau (no args): every
// running event user of the leader drinks from its blood ball on the variant
// (GodIslandHealPower), drawing min(HP missing, ball reservoir, per-draw
// ceiling). Dead members answer with their revive countdown instead of an
// order.
func (t *TeleBot) giHoiMauHandler(ctx context.Context, b *bot.Bot, update *models.Update, which int) {
	prefix, label := giCmdPrefix(which), giEventLabel(which)
	if len(strings.Fields(update.Message.Text)) != 1 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: " + prefix + "hoimau (không tham số)",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
		return
	}
	t.eventOrderBroadcast(ctx, b, update,
		func(moved int) string {
			return fmt.Sprintf("💚 Đã lệnh %d event user (%s) hồi máu", moved, label)
		},
		func(p *EventPlayer) error { return p.GodIslandHealPower(which) })
}

// --- Đảo Bồng Lai (normal) entry points -------------------------------

// BonglaiMoveHandler handles /bonglai_move <city_id>.
func (t *TeleBot) BonglaiMoveHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giMoveHandler(ctx, b, update, GIEventNormal)
}

// BonglaiAttackHandler handles /bonglai_attack <city_id>.
func (t *TeleBot) BonglaiAttackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giAttackHandler(ctx, b, update, GIEventNormal)
}

// BonglaiAttackThuVeHandler handles /bonglai_attack_thuve <city_id>.
func (t *TeleBot) BonglaiAttackThuVeHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giAttackThuVeHandler(ctx, b, update, GIEventNormal)
}

// BonglaiCanQuaHandler handles /bonglai_canqua [count].
func (t *TeleBot) BonglaiCanQuaHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giCanQuaHandler(ctx, b, update, GIEventNormal)
}

// BonglaiTangTocHandler handles /bonglai_tangtoc.
func (t *TeleBot) BonglaiTangTocHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giTangTocHandler(ctx, b, update, GIEventNormal)
}

// BonglaiHoiMauHandler handles /bonglai_hoimau.
func (t *TeleBot) BonglaiHoiMauHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giHoiMauHandler(ctx, b, update, GIEventNormal)
}

// --- Đảo Thiên Ma (peak island) entry points --------------------------

// ThienmaMoveHandler handles /thienma_move <city_id> — the Thiên Ma twin of
// /bonglai_move.
func (t *TeleBot) ThienmaMoveHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giMoveHandler(ctx, b, update, GIEventPeak)
}

// ThienmaAttackHandler handles /thienma_attack <city_id>.
func (t *TeleBot) ThienmaAttackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giAttackHandler(ctx, b, update, GIEventPeak)
}

// ThienmaAttackThuVeHandler handles /thienma_attack_thuve <city_id>.
func (t *TeleBot) ThienmaAttackThuVeHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giAttackThuVeHandler(ctx, b, update, GIEventPeak)
}

// ThienmaCanQuaHandler handles /thienma_canqua [count].
func (t *TeleBot) ThienmaCanQuaHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giCanQuaHandler(ctx, b, update, GIEventPeak)
}

// ThienmaTangTocHandler handles /thienma_tangtoc.
func (t *TeleBot) ThienmaTangTocHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giTangTocHandler(ctx, b, update, GIEventPeak)
}

// ThienmaHoiMauHandler handles /thienma_hoimau.
func (t *TeleBot) ThienmaHoiMauHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giHoiMauHandler(ctx, b, update, GIEventPeak)
}
