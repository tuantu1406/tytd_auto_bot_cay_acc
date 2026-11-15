package tytd_go

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// UnknownCommandHandler handles unknown commands and provides helpful suggestions
func (t *TeleBot) UnknownCommandHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}

	message := update.Message

	// Only handle text messages that start with /
	if message.Text == "" || !strings.HasPrefix(message.Text, "/") {
		return
	}

	// Extract command (remove parameters)
	command := strings.Split(message.Text, " ")[0]
	command = strings.TrimPrefix(command, "/")

	// If command is empty, ignore
	if command == "" {
		return
	}

	// Send helpful response
	responseText := fmt.Sprintf(`❌ <b>Lệnh không tồn tại: /%s</b>

💡 <b>Gợi ý:</b>
• Gõ <code>/start</code> để xem danh sách lệnh
• Gõ <code>/info</code> để xem thông tin tài khoản
• Kiểm tra lại chính tả lệnh

📌 <b>Các lệnh phổ biến:</b>
• <code>/auth {key}</code> - Kích hoạt bot
• <code>/login</code> - Đăng nhập game
• <code>/logout</code> - Đăng xuất khỏi game
• <code>/start_chuot</code> - Bật auto chuột
• <code>/start_maohiem</code> - Bật auto mạo hiểm
• <code>/start_lv100</code> - Bật cày acc lên Lv 100 (Chặt đào, nâng cây, tuyến chính, mạo hiểm)
• <code>/stop_lv100</code> - Dừng cày acc lên Lv 100
• <code>/start_taydu</code> - Bật auto Tây Du Phục Ma (spam ải chính tuyến)
• <code>/start_taydu_team</code> - Leader bật cả đội Tây Du (leader + đệ tử)

💬 Cần hỗ trợ? @tytd_tool_support`, command)

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            responseText,
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	if err == nil && msg != nil {
		// Auto-delete after 20 seconds
		go func() {
			time.Sleep(60 * time.Second)
			b.DeleteMessage(ctx, &bot.DeleteMessageParams{
				ChatID:    message.Chat.ID,
				MessageID: msg.ID,
			})
		}()
	}
}
