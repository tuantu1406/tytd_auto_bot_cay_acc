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

// AddGoodUserHandler adds a user to good user list (admin only)
func (t *TeleBot) AddGoodUserHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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

	// Parse command: /add_good_user {chat_id}
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Sử dụng: /add_good_user {chat_id}",
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

	// Add good user
	err = model.AddGoodUser(t.ydb.GetDB(), targetChatID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("❌ Lỗi: %v", err),
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("✅ Đã thêm user %d vào danh sách good user", targetChatID),
	})
}

// RemoveGoodUserHandler removes a user from good user list (admin only)
func (t *TeleBot) RemoveGoodUserHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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

	// Parse command: /remove_good_user {chat_id}
	parts := strings.Fields(update.Message.Text)
	if len(parts) < 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Sử dụng: /remove_good_user {chat_id}",
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

	// Remove good user
	err = model.RemoveGoodUser(t.ydb.GetDB(), targetChatID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("❌ Lỗi: %v", err),
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("✅ Đã xóa user %d khỏi danh sách good user", targetChatID),
	})
}

// ListGoodUsersHandler lists all good users (admin only)
func (t *TeleBot) ListGoodUsersHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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

	// Get all good users
	goodUsers, err := model.GetAllGoodUsers(t.ydb.GetDB())
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("❌ Lỗi: %v", err),
		})
		return
	}

	if len(goodUsers) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "ℹ️ Chưa có good user nào",
		})
		return
	}

	// Build response
	var response strings.Builder
	response.WriteString("👍 <b>Danh sách Good Users:</b>\n\n")

	for i, user := range goodUsers {
		response.WriteString(fmt.Sprintf("%d. <code>%d</code>\n", i+1, user.ChatID))
	}

	response.WriteString(fmt.Sprintf("\n<b>Array format:</b> <code>%s</code>", model.GetGoodUserIDsString(t.ydb.GetDB())))

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
