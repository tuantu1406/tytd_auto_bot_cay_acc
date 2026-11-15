package tytd_go

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (t *TeleBot) AddGiftCodeHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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

	if len(args) != 3 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /add_code <code> <max_usage>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	code := args[1]
	maxUsage, err := strconv.Atoi(args[2])
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "invalid max usage",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	err = model.CreateGiftCode(t.ydb.GetDB(), code, maxUsage)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to create code: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "gift code created",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// GenGiftCodesHandler generates multiple random gift codes
func (t *TeleBot) GenGiftCodesHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
	if len(args) != 3 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /gen_codes <quantity> <max_usage>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	quantity, err := strconv.Atoi(args[1])
	if err != nil || quantity <= 0 || quantity > 100 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "invalid quantity (1-100)",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	maxUsage, err := strconv.Atoi(args[2])
	if err != nil || maxUsage <= 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "invalid max usage",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Generate codes
	var generatedCodes []string
	for i := 0; i < quantity; i++ {
		code := generateRandomCode(8)
		err := model.CreateGiftCode(t.ydb.GetDB(), code, maxUsage)
		if err != nil {
			continue // Skip if code already exists
		}
		generatedCodes = append(generatedCodes, code)
	}

	if len(generatedCodes) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to generate codes",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Format response
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ Generated %d codes:\n\n", len(generatedCodes)))
	for _, code := range generatedCodes {
		sb.WriteString(fmt.Sprintf("• <code>%s</code>\n", code))
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            sb.String(),
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// generateRandomCode generates a random alphanumeric string of specified length
func generateRandomCode(length int) string {
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789" // Removed confusing chars like 0, O, 1, I, l
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[time.Now().UnixNano()%int64(len(charset))]
		time.Sleep(1 * time.Nanosecond) // Ensure different seed
	}
	return string(result)
}

func (t *TeleBot) ListGiftCodesHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	codes, err := model.GetAllGiftCodes(t.ydb.GetDB())
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load codes: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(codes) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "no gift codes found",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	var sb strings.Builder
	header := "<b>🎁 Gift Codes</b>\n\n"
	sb.WriteString(header)

	for _, c := range codes {
		line := fmt.Sprintf("• <code>%s</code>: %d/%d used\n", c.Code, c.UsageCount, c.MaxUsage)

		// Telegram message limit is 4096. We use 4000 to be safe.
		if sb.Len()+len(line) > 3600 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:    message.Chat.ID,
				Text:      sb.String(),
				ParseMode: models.ParseModeHTML,
			})
			sb.Reset()
			sb.WriteString(header)
		}
		sb.WriteString(line)
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

func (t *TeleBot) AddBlacklistClanHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /add_black_clan <clan_id>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	clanID, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "invalid clan id",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	err = model.AddBlacklistClan(t.ydb.GetDB(), clanID)
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
		Text:            "clan added to blacklist",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) DelBlacklistClanHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /del_black_clan <clan_id>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	clanID, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "invalid clan id",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	err = model.RemoveBlacklistClan(t.ydb.GetDB(), clanID)
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
		Text:            "clan removed from blacklist",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) ListBlacklistClanHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	list, err := model.GetAllBlacklistClans(t.ydb.GetDB())
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(list) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "blacklist is empty",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	var sb strings.Builder
	sb.WriteString("<b>🚫 Clan Blacklist</b>\n\n")
	for _, id := range list {
		sb.WriteString(fmt.Sprintf("• %d\n", id))
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            sb.String(),
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) SetMaxPlayerHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /set_max_player <number>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	limit, err := strconv.Atoi(args[1])
	if err != nil || limit < 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "invalid number",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	err = model.SetConfig(t.ydb.GetDB(), model.ConfigKeyMaxConcurrentPlayer, fmt.Sprintf("%d", limit))
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
		Text:            fmt.Sprintf("max player set to %d", limit),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) GetSettingHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Helper function to get config value with default
	getConfigValue := func(key, defaultValue string) string {
		config, err := model.GetConfig(t.ydb.GetDB(), key)
		if err != nil || config == nil {
			return defaultValue
		}
		return config.Value
	}

	var sb strings.Builder
	sb.WriteString("<b>⚙️ System Settings</b>\n\n")

	// Display key settings with descriptions
	sb.WriteString("<b>Admin Settings:</b>\n")
	sb.WriteString(fmt.Sprintf("• Max Players: <code>%s</code>\n", getConfigValue(model.ConfigKeyMaxConcurrentPlayer, "not set")))
	sb.WriteString(fmt.Sprintf("• Auth Key: <code>%s</code>\n", getConfigValue(model.ConfigKeyAuthKey, "not set")))
	sb.WriteString(fmt.Sprintf("• Admin Chat ID: <code>%s</code>\n\n", getConfigValue(model.ConfigKeyAdminChatID, "not set")))

	sb.WriteString("<b>Feature Flags:</b>\n")
	sb.WriteString(fmt.Sprintf("• Debug Mode: <code>%s</code>\n", getConfigValue(model.ConfigKeyDebugMode, "false")))
	sb.WriteString(fmt.Sprintf("• Auto Login: <code>%s</code>\n", getConfigValue(model.ConfigKeyAutoLogin, "false")))
	sb.WriteString(fmt.Sprintf("• Temp Auto: <code>%s</code>\n", getConfigValue(model.ConfigKeyTempAuto, "true")))
	sb.WriteString(fmt.Sprintf("• Cổng Auto Tụ Linh: <code>%s</code>\n", getConfigValue(model.ConfigKeyAutoTuLinh, "true")))
	sb.WriteString(fmt.Sprintf("• Must Join Group: <code>%s</code>\n\n", getConfigValue(model.ConfigKeyMustJoinGroup, "false")))

	sb.WriteString("<b>AI Chatbot:</b>\n")

	// Show Groq API Key
	groqKey := getConfigValue(model.ConfigKeyGroqAPIKey, "not set")
	if groqKey != "not set" && len(groqKey) > 10 {
		groqKey = groqKey[:10] + "..." // Hide API key
	}
	sb.WriteString(fmt.Sprintf("• Groq API Key: <code>%s</code>\n", groqKey))

	// Show Copilot API Key
	copilotKey := getConfigValue(model.ConfigKeyCopilotAPIKey, "not set")
	if copilotKey != "not set" && len(copilotKey) > 10 {
		copilotKey = copilotKey[:10] + "..." // Hide API key
	}
	sb.WriteString(fmt.Sprintf("• Copilot API Key: <code>%s</code>\n", copilotKey))

	sb.WriteString(fmt.Sprintf("• Chatbot Enabled: <code>%s</code>\n", getConfigValue(model.ConfigKeyChatbotEnabled, "false")))
	sb.WriteString(fmt.Sprintf("• Group Chat ID: <code>%s</code>\n\n", getConfigValue(model.ConfigKeyGroupChatID, "not set")))

	mcpKey := getConfigValue(model.ConfigKeyMCPAuthKey, "")
	mcpKeyShown := "not set"
	if len(mcpKey) > 4 {
		mcpKeyShown = mcpKey[:4] + "..." // show a fingerprint, never the key
	} else if mcpKey != "" {
		mcpKeyShown = "set"
	}
	sb.WriteString("<b>🔌 MCP Server:</b>\n")
	sb.WriteString(fmt.Sprintf("• Enabled: <code>%s</code>\n", getConfigValue(model.ConfigKeyMCPEnabled, "false")))
	sb.WriteString(fmt.Sprintf("• Auth Key: <code>%s</code>\n\n", mcpKeyShown))

	bcTok := getConfigValue(model.ConfigKeyBCEngineToken, "")
	bcTokShown := "none"
	if len(bcTok) > 4 {
		bcTokShown = bcTok[:4] + "..." // fingerprint only, like the MCP key
	} else if bcTok != "" {
		bcTokShown = "set"
	}
	sb.WriteString("<b>♟ Big Chess Engine:</b>\n")
	sb.WriteString(fmt.Sprintf("• Enabled: <code>%s</code>\n", getConfigValue(model.ConfigKeyBCEngineEnabled, "false")))
	sb.WriteString(fmt.Sprintf("• URL: <code>%s</code>\n", getConfigValue(model.ConfigKeyBCEngineURL, "http://127.0.0.1:8780")))
	sb.WriteString(fmt.Sprintf("• Token: <code>%s</code>\n\n", bcTokShown))

	// Add dynamic info
	running := t.getRunningCount()
	maxPlayers, _ := strconv.Atoi(getConfigValue(model.ConfigKeyMaxConcurrentPlayer, "0"))
	sb.WriteString("<b>📊 Status:</b>\n")
	sb.WriteString(fmt.Sprintf("• Running Players: <code>%d/%d</code>", running, maxPlayers))

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            sb.String(),
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) DelGiftCodeHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /del_code <code>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	code := args[1]
	err := model.DeleteGiftCode(t.ydb.GetDB(), code)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to delete code: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "gift code deleted",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) SetDebugHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /set_debug true|false",
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

	model.SetConfig(t.ydb.GetDB(), model.ConfigKeyDebugMode, fmt.Sprintf("%t", enabled))
	t.ydb.SetDebugMode(enabled)

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("debug mode set to %t", enabled),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) SetMustJoinGroupHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /set_must_join_group true|false",
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
	model.SetConfig(t.ydb.GetDB(), model.ConfigKeyMustJoinGroup, fmt.Sprintf("%t", enabled))
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("must_join_group set to %t", enabled),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) SetAuthKeyHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /set_auth_key <key>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	newKey := args[1]

	// Validate auth key
	if len(newKey) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "auth key cannot be empty",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(newKey) > 50 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "auth key too long (max 50 characters)",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	err := model.SetConfig(t.ydb.GetDB(), model.ConfigKeyAuthKey, newKey)
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
		Text:            fmt.Sprintf("auth key updated to: %s", newKey),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) SetAutoLoginHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /set_auto_login <true/false>",
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

	err = model.SetConfig(t.ydb.GetDB(), model.ConfigKeyAutoLogin, fmt.Sprintf("%t", val))
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
		Text:            fmt.Sprintf("auto_login set to %t", val),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// SetAutoTuLinhHandler flips the GLOBAL Tụ Linh gate, mirroring
// /set_auto_login: before every gather attempt each player checks this
// gate first. Closing it pauses automatic energy gathering for ALL accounts
// at once (even those with their own switch on); reopening it resumes only
// the accounts whose owner/leader has Tụ Linh enabled.
func (t *TeleBot) SetAutoTuLinhHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
			Text:            "usage: /set_auto_tu_linh <true/false>",
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
	if err := model.SetConfig(t.ydb.GetDB(), model.ConfigKeyAutoTuLinh, fmt.Sprintf("%t", val)); err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("Cổng Tụ Linh toàn cục: %s — khi tắt, MỌI acc ngừng tự thu năng lượng từ phút tiếp theo; khi bật lại, chỉ acc nào đã bật Tụ Linh mới thu tiếp.", map[bool]string{true: "MỞ", false: "ĐÓNG"}[val]),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// StartThanLongHandler starts ONE "đại hội luyện khí" (ComposeBall, activity
// type 130) round for the CALLER's own session (user directive 2026-10-04:
// not a background loop — it runs exactly once per /start_thanlong). The run
// drops + cascade-merges uploads until HP is exhausted (it then STOPS and
// NOTIFIES without settling, so the round is resumable), or settles +
// notifies when the game ends / no legal move remains. Items are never used.
func (t *TeleBot) StartThanLongHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	v, ok := t.session.Load(message.Chat.ID)
	pl, isPlayer := v.(*Player)
	if !ok || !isPlayer || !pl.Running {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "Acc chưa đang treo tại server game — hãy /start acc trước rồi mới /start_thanlong.",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	log.Printf("[%s] [LuyenKhi] /start_thanlong từ chat %d — running=%v", pl.Username, message.Chat.ID, pl.Running)
	started, busy := pl.cbStartOnce()
	if started {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "Đã khởi động 1 ván Đại Hội Luyện Khí — tự thả + hợp nhất dây chuyền, ngừng khi hết Linh Khí (ván giữ nguyên để chơi tiếp).",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	if busy {
		// CbMu still held by a live compose op — never queue the handler
		// behind it (that was the 2026-10-25 "/start_thanlong không trả lời"
		// hang in the deployed binary).
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "Luyện Khí: đang có thao tác chờ server, đạo hữu đợi chút rồi bấm lại nhé.",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "Ván Luyện Khí đang chạy rồi, đạo hữu đợi kết quả thông báo nhé.",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) BroadcastHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	args := strings.SplitN(message.Text, " ", 2)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /broadcast <message>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	msg := args[1]
	userIDs := model.GetAllUserIDs(t.ydb.GetDB())

	successCount := 0
	failCount := 0

	for _, idStr := range userIDs {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			failCount++
			continue
		}

		// Check if user has a leader (exclude from broadcast)
		user, errGet := model.GetUser(t.ydb.GetDB(), idStr)
		if errGet == nil && user.LeaderID != "" {
			continue // Skip users with a leader
		}

		// Don't broadcast to self if you don't want to, but usually it's fine to verify
		_, err = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: id,
			Text:   "📢 Broadcast:\n" + msg,
		})
		if err != nil {
			failCount++
			// Check if it's a network error
			if netErr, ok := err.(net.Error); ok && (netErr.Timeout() || netErr.Temporary()) {
				// Temporary/Network error, do nothing or retry (but here we just skip)
			} else {
				// Not a network error, likely blocked or invalid user
				// Check if user has a leader
				user, errGet := model.GetUser(t.ydb.GetDB(), idStr)
				if errGet == nil && user.LeaderID != "" {
					fmt.Printf("User %s has leader %s, skipping deletion\n", idStr, user.LeaderID)
				} else {
					// Remove user from DB
					delErr := model.DeleteUser(t.ydb.GetDB(), idStr)
					if delErr != nil {
						fmt.Printf("Failed to delete user %s: %v\n", idStr, delErr)
					} else {
						fmt.Printf("Deleted user %s due to broadcast error: %v\n", idStr, err)
					}
				}
			}
		} else {
			successCount++
		}
		// Sleep briefly to avoid hitting rate limits if user base is large
		time.Sleep(50 * time.Millisecond)
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("Broadcast complete.\nSuccess: %d\nFailed: %d", successCount, failCount),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// SetLeaderHandler allows admin to set/unset leader status
func (t *TeleBot) SetLeaderHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
	if len(args) != 3 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /set_leader <telegram_chat_id> <true/false>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	targetID := args[1]
	isLeader, err := strconv.ParseBool(args[2])
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "invalid bool: use true or false",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Get user
	user, err := model.GetUser(t.ydb.GetDB(), targetID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "user not found",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Update leader status
	user.IsLeader = isLeader
	err = model.CreateUser(t.ydb.GetDB(), user)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	status := "removed from"
	if isLeader {
		status = "promoted to"
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("User %s %s leader", targetID, status),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// ListLeadersHandler shows all leaders and their member counts
func (t *TeleBot) ListLeadersHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	leaders, err := model.GetAllLeaders(t.ydb.GetDB())
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load leaders: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	if len(leaders) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "no leaders found",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	var sb strings.Builder
	sb.WriteString("<b>👑 Leaders</b>\n\n")
	for i, leader := range leaders {
		count := model.GetMembersCount(t.ydb.GetDB(), leader.ID)
		sb.WriteString(fmt.Sprintf("%d. <code>%s</code>\n", i+1, leader.ID))
		sb.WriteString(fmt.Sprintf("   • Members: %d\n", count))
		sb.WriteString(fmt.Sprintf("   • Username: %s\n\n", leader.Username))
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            sb.String(),
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}
