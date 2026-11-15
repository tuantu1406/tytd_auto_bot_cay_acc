package tytd_go

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// SetTempAutoHandler allows admin or leader to toggle temp_auto config
func (t *TeleBot) SetTempAutoHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	// Check if user is admin or leader
	isAdminUser := t.isAdmin(message.Chat.ID)
	isLeaderUser := t.isLeader(message.Chat.ID)

	if !isAdminUser && !isLeaderUser {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin or leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /set_temp_auto <true/false>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	val, err := strconv.ParseBool(args[1])
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "invalid bool: use true or false",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	err = model.SetConfig(t.ydb.GetDB(), model.ConfigKeyTempAuto, fmt.Sprintf("%t", val))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("temp_auto set to %t", val),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}
