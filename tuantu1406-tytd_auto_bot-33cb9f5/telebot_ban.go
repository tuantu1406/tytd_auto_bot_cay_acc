package tytd_go

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// BanUserHandler handles /ban_user command (admin only)
func (t *TeleBot) BanUserHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID

	// Check if user is admin
	if !t.isAdmin(chatID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "⛔ Chỉ admin mới có thể sử dụng lệnh này",
		})
		return
	}

	// Parse command: /ban_user {chat_id} [reason]
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Sử dụng: /ban_user {chat_id} [lý do]",
		})
		return
	}

	// Parse chat ID
	targetChatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Chat ID không hợp lệ",
		})
		return
	}

	// Get reason (optional)
	reason := "Không có lý do"
	if len(parts) > 2 {
		reason = strings.Join(parts[2:], " ")
	}

	// Get username (if available)
	username := fmt.Sprintf("User_%d", targetChatID)

	// Ban user
	err = model.AddBannedUser(t.ydb.GetDB(), targetChatID, username, reason)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("❌ Lỗi khi ban user: %v", err),
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text: fmt.Sprintf(`✅ Đã ban user thành công!

👤 Chat ID: %d
📝 Lý do: %s`, targetChatID, reason),
	})
}

// UnbanUserHandler handles /unban_user command (admin only)
func (t *TeleBot) UnbanUserHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID

	// Check if user is admin
	if !t.isAdmin(chatID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "⛔ Chỉ admin mới có thể sử dụng lệnh này",
		})
		return
	}

	// Parse command: /unban_user {chat_id}
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Sử dụng: /unban_user {chat_id}",
		})
		return
	}

	// Parse chat ID
	targetChatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Chat ID không hợp lệ",
		})
		return
	}

	// Check if user is actually banned
	if !model.IsBanned(t.ydb.GetDB(), targetChatID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("ℹ️ User %d không bị ban", targetChatID),
		})
		return
	}

	// Unban user
	err = model.RemoveBannedUser(t.ydb.GetDB(), targetChatID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("❌ Lỗi khi unban user: %v", err),
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("✅ Đã unban user %d thành công!", targetChatID),
	})
}

// ListBannedUsersHandler handles /list_banned command (admin only)
func (t *TeleBot) ListBannedUsersHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}

	chatID := update.Message.Chat.ID

	// Check if user is admin
	if !t.isAdmin(chatID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "⛔ Chỉ admin mới có thể sử dụng lệnh này",
		})
		return
	}

	// Get all banned users
	bannedUsers, err := model.GetAllBannedUsers(t.ydb.GetDB())
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("❌ Lỗi khi lấy danh sách: %v", err),
		})
		return
	}

	if len(bannedUsers) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "ℹ️ Không có user nào bị ban",
		})
		return
	}

	// Build response
	var response strings.Builder
	response.WriteString("🚫 <b>Danh sách user bị ban:</b>\n\n")

	for i, user := range bannedUsers {
		response.WriteString(fmt.Sprintf("<b>%d. Chat ID:</b> <code>%d</code>\n", i+1, user.ChatID))
		response.WriteString(fmt.Sprintf("   👤 Username: %s\n", user.Username))
		response.WriteString(fmt.Sprintf("   📅 Thời gian: %s\n", user.BannedAt.In(ictZone).Format("2006-01-02 15:04:05")))
		response.WriteString(fmt.Sprintf("   📝 Lý do: %s\n\n", user.Reason))
	}

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      response.String(),
		ParseMode: models.ParseModeHTML,
	})

	if err == nil && msg != nil {
		// Auto-delete after 60 seconds
		go func() {
			time.Sleep(60 * time.Second)
			b.DeleteMessage(ctx, &bot.DeleteMessageParams{
				ChatID:    chatID,
				MessageID: msg.ID,
			})
		}()
	}
}
