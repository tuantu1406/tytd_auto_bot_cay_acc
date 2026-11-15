package tytd_go

// "Uỷ thác" (event delegation). An account — either a regular user's own
// account OR one leader's member account — can delegate ITSELF (per account,
// never in bulk) to ANOTHER leader, identified by chat id, for EVENTS ONLY.
//
// What the delegate leader gains: the account shows up on their SỰ KIỆN tab,
// where they can force login/logout it as an EventPlayer and fire every event
// order (Côn Lôn, Bắc Minh, Bồng Lai/Thiên Ma, Cửu U, Thượng Cổ, Thương Đồ) —
// all WITHOUT ever learning the game password. What they never get: the account
// is NOT on their Đệ tử tab (GetMembersByLeader is untouched), and they cannot
// change its password/config/VIP/remove it. The OWNER is notified (Telegram) on
// every delegated force-login. Revocable anytime with /bo_uythac.
//
// Feature requested by the user 2026-10-07; the single model.User.DeputyOf
// column is the whole persistence story (GetDelegatedUsers reads it).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"gorm.io/gorm"
)

// deputyValidateTarget parses/validates a delegation target for account selfID.
// Empty input means CLEAR. Returns the canonical decimal target (or "" for
// clear) plus an error message ("" when valid). A target must be a real leader
// (IsLeader, not itself someone's member), not the account itself, not banned.
func deputyValidateTarget(db *gorm.DB, selfID, raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" || strings.EqualFold(raw, "none") {
		return "", "" // clear
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return "", "!!mã leader không hợp lệ (phải là chat id số)"
	}
	target := strconv.FormatInt(id, 10)
	if target == selfID {
		return "", "!!không thể uỷ thác acc cho chính mình"
	}
	if !model.IsLeaderChat(db, target) {
		return "", "!!leader đó không tồn tại (phải là tài khoản leader, không phải đệ tử)"
	}
	if model.IsBanned(db, id) {
		return "", "!!leader đó đang bị khoá"
	}
	return target, ""
}

// deputySet is the ONE mutator shared by the Telegram command, the account's
// own settings page and the owning leader's Đệ tử config panel. It validates,
// writes u.DeputyOf, and notifies the delegate leader on set / the previous
// delegate on revoke. Returns (message, isError).
func (t *TeleBot) deputySet(u *model.User, raw string) (string, bool) {
	db := t.ydb.GetDB()
	target, msg := deputyValidateTarget(db, u.ID, raw)
	if msg != "" {
		return msg, true
	}
	prev := u.DeputyOf
	if prev == target {
		if target == "" {
			return "Acc này đang không uỷ thác cho ai", false
		}
		return "Acc này đã uỷ thác cho leader " + target + " rồi", false
	}
	u.DeputyOf = target
	if err := model.CreateUser(db, u); err != nil {
		return "!!không lưu được uỷ thác: " + err.Error(), true
	}
	name := u.IngameName
	if name == "" {
		name = u.Username
	}
	logLine := fmt.Sprintf("[%s] [Uỷ thác] acc #%s deputy_of %q → %q", u.Username, u.ID, prev, target)
	if target != "" {
		lid, _ := strconv.ParseInt(target, 10, 64)
		t.pushNotify(lid, fmt.Sprintf(
			"🤝 %s (#%s) vừa uỷ thác acc này cho bạn CHẠY EVENT. Nó sẽ xuất hiện ở tab Sự Kiện của bạn — bạn force login/logout và gửi lệnh event được, KHÔNG cần biết mật khẩu, và không thấy nó ở tab Đệ Tử. Thu hồi do chủ acc quyết định.",
			name, u.ID))
		logLine += " · đã báo leader nhận"
		return "Đã uỷ thác acc " + name + " cho leader " + target + " (chỉ chạy event, không thấy ở tab Đệ Tử). Thu hồi: /bo_uythac", false
	}
	if prev != "" {
		if pid, err := strconv.ParseInt(prev, 10, 64); err == nil {
			t.pushNotify(pid, fmt.Sprintf("🤝 %s (#%s) đã THU HỒI uỷ thác — acc không còn ở tab Sự Kiện của bạn.", name, u.ID))
		}
	}
	return "Đã thu hồi uỷ thác cho acc " + name, false
}

// notifyDeputyLogin alerts the OWNER that the delegate leader just
// force-logged their account into the game. owner chat = the account's
// LeaderID (a member) or its own id (a regular user). No-op when the account
// has no deputy or the operator already IS the owner (that's a normal login).
func (t *TeleBot) notifyDeputyLogin(u *model.User, leaderChatID int64) {
	if u.DeputyOf == "" {
		return
	}
	ownerStr := u.LeaderID
	if ownerStr == "" {
		ownerStr = u.ID
	}
	owner, err := strconv.ParseInt(ownerStr, 10, 64)
	if err != nil || owner == leaderChatID {
		return
	}
	name := u.IngameName
	if name == "" {
		name = u.Username
	}
	t.pushNotify(owner, fmt.Sprintf(
		"🤝 Acc %s (#%s) vừa được leader %d ĐĂNG NHẬP UỶ THÁC vào game lúc %s (giờ VN) — do bạn đã uỷ thác acc này cho leader đó chạy event. Không phải bạn bấm? Thu hồi: /bo_uythac.",
		name, u.ID, leaderChatID, time.Now().In(ictZone).Format("15:04 02/01/2006")))
}

// startEventPlayerLoginForDeputy force-starts an EVENT session for a delegated
// USERS row (a regular user or ANOTHER leader's member) run by leaderChatID.
// It mirrors startEventPlayerLogin's gates + reduced RunEvent loop, adds the
// VIP gate User rows carry (event_users have no VIP), refuses while the account
// is hanging in normal (Player) mode so the delegate never kills the owner's
// own auto-play, and marks the session deputy so its nickname persists to the
// users table. Returns true when a login started.
func (t *TeleBot) startEventPlayerLoginForDeputy(u *model.User, leaderChatID int64, notify func(text string)) bool {
	db := t.ydb.GetDB()
	realUID, err := strconv.ParseInt(u.ID, 10, 64)
	if err != nil {
		notify("acc uỷ thác này không có mã số hợp lệ")
		return false
	}
	// Existing session decides the rest of the gating. A running EVENT player
	// is a double event-login → refuse. A normal (Player) hang the owner is
	// running is SNATCHABLE (user 2026-10-07 "cho phép lead giật acc đang treo")
	// and does NOT consume a new online slot (the account is already counted), so
	// the max-players gate is skipped for the takeover and every harmful gate
	// still runs BEFORE the kick.
	snatchableHang := false
	if v, ok := t.session.Load(realUID); ok {
		if _, isPlayer := v.(*Player); isPlayer {
			snatchableHang = true
		} else if sessionRunning(v) {
			notify("chay 1 lan` thoi đạo hữu")
			return false
		}
	}
	if model.IsBanned(db, realUID) {
		notify("🚫 acc này bị cấm sử dụng bot")
		return false
	}
	if u.VipLevel == 0 || u.VipExpireAt.Before(time.Now()) {
		notify("acc này hết hạn quyền, không đăng nhập được")
		return false
	}
	if !u.LastLoginFail.IsZero() && time.Since(u.LastLoginFail) < 3*time.Minute {
		wait := 3*time.Minute - time.Since(u.LastLoginFail)
		notify(fmt.Sprintf("acc vừa đăng nhập fail, đợi %s rồi bấm lại", wait.Round(time.Second)))
		return false
	}
	if !snatchableHang && t.getRunningCount() >= t.getMaxPlayers() {
		notify("server đang đầy, thử lại sau")
		return false
	}
	// All gates cleared → now take the owner's hang (graceful ctx-cancel: Run
	// returns true → NO login-fail cooldown; sessionDel is identity-guarded so
	// the old Player's async teardown cannot evict the EventPlayer stored next).
	// The owner learns of it two ways: the old session's own "bị đá ra rồi"
	// notice AND notifyDeputyLogin below.
	if snatchableHang {
		t.stopPlayerForChatID(realUID)
	}

	p := &EventPlayer{}
	p.UID = uint64(realUID)
	p.Username = u.Username
	p.Password = u.Password
	p.ServerID = u.ServerID
	// Event notifications for this delegated account route to the OPERATING
	// leader (who is running the event); the owner only learns of the login via
	// notifyDeputyLogin. The session key (realUID) stays the account id.
	p.ChatID = leaderChatID
	p.NotiChan = t.notiChan
	p.DB = db
	p.Notify = false
	p.deputy = true
	p.OnRunningChange = func() { t.pushWebState(realUID) }

	t.session.Store(realUID, p)
	u.Running = true
	model.CreateUser(db, u)
	idStr := u.ID
	username := u.Username
	go func() {
		if !p.Run() {
			model.SetLastLoginFail(db, idStr)
		}
		t.pushNotify(p.ChatID, "bi da' ra roi` đạo hữu "+username)
		if u2, err := model.GetUser(db, idStr); err == nil {
			u2.Running = false
			model.CreateUser(db, u2)
		}
		t.sessionDel(realUID, p)
		t.pushWebState(realUID)
	}()
	t.notifyDeputyLogin(u, leaderChatID)
	return true
}

// ---- Telegram commands ---------------------------------------------------------

// deputyReply is the one-shot plain reply these three commands share.
func (t *TeleBot) deputyReply(ctx context.Context, b *bot.Bot, msg *models.Message, text string) {
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          msg.Chat.ID,
		Text:            text,
		ReplyParameters: &models.ReplyParameters{MessageID: msg.ID},
	})
}

// /uythac <chat_id> delegates the CALLER's own account (their regular user
// account, or their own member row if they are a member) to another leader for
// events. This is per-account on purpose — there is no "delegate my whole
// fleet" command.
func (t *TeleBot) uythacHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	msg := update.Message
	args := strings.TrimSpace(strings.TrimPrefix(msg.Text, "/uythac"))
	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", msg.Chat.ID))
	if err != nil {
		t.deputyReply(ctx, b, msg, "lỗi đọc tài khoản, thử lại sau")
		return
	}
	res, _ := t.deputySet(u, args)
	t.deputyReply(ctx, b, msg, res)
}

// /bo_uythac revokes the CALLER's own account delegation.
func (t *TeleBot) boUythacHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	msg := update.Message
	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", msg.Chat.ID))
	if err != nil {
		t.deputyReply(ctx, b, msg, "lỗi đọc tài khoản, thử lại sau")
		return
	}
	res, _ := t.deputySet(u, "")
	t.deputyReply(ctx, b, msg, res)
}

// /uythac_xem shows the CALLER's own delegation state.
func (t *TeleBot) uythacXemHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	msg := update.Message
	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", msg.Chat.ID))
	if err != nil {
		t.deputyReply(ctx, b, msg, "lỗi đọc tài khoản, thử lại sau")
		return
	}
	if u.DeputyOf == "" {
		t.deputyReply(ctx, b, msg, "Acc của bạn đang KHÔNG uỷ thác cho ai. Uỷ thác: /uythac <chat_id_leader>")
		return
	}
	t.deputyReply(ctx, b, msg, fmt.Sprintf("Acc của bạn đang uỷ thác chạy event cho leader %s. Thu hồi: /bo_uythac", u.DeputyOf))
}
