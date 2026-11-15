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

// SetGroqAPIKeyHandler allows admin to set Groq API key
func (t *TeleBot) SetGroqAPIKeyHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /set_groq_key <api_key>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	apiKey := args[1]
	model.SetConfig(t.ydb.GetDB(), model.ConfigKeyGroqAPIKey, apiKey)

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "Groq API key set successfully",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// SetCopilotAPIKeyHandler allows admin to set Copilot API key
func (t *TeleBot) SetCopilotAPIKeyHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /set_copilot_key <api_key>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	apiKey := args[1]
	model.SetConfig(t.ydb.GetDB(), model.ConfigKeyCopilotAPIKey, apiKey)

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "Copilot API key set successfully",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// SetChatbotEnabledHandler allows admin to enable/disable chatbot
func (t *TeleBot) SetChatbotEnabledHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /set_chatbot true|false",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	enabled, err := strconv.ParseBool(args[1])
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "invalid value, use true or false",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	model.SetConfig(t.ydb.GetDB(), model.ConfigKeyChatbotEnabled, fmt.Sprintf("%t", enabled))

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("chatbot set to %t", enabled),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// SetGroupChatIDHandler allows admin to set target group chat ID
func (t *TeleBot) SetGroupChatIDHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.Fields(message.Text)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /set_group_chat_id <chat_id>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	chatID := args[1]
	model.SetConfig(t.ydb.GetDB(), model.ConfigKeyGroupChatID, chatID)

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("group chat ID set to %s", chatID),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}
