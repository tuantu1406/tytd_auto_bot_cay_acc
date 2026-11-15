package tytd_go

// /check_config — a diagnostic for the "login failed but everything LOOKS
// right" class of bug (owner audit 2026-10-10: a server id or username pasted
// with a trailing space / zero-width character). It re-reads the CALLER's own
// stored credentials and prints them %q-quoted so invisible bytes become
// visible escapes, plus an explicit warning when the server id is the silent
// 0 the old /server_id bug used to write. Passwords are NEVER printed — only
// their length and whether they carry invisible runes.
//
// A leader also gets a compact pass over every ĐỆ TỬ + acc sự kiện of theirs,
// flagging just the suspicious ones, so one member's bad row (e.g. an account
// that keeps failing login) is findable without dumping everything.

import (
	"context"
	"fmt"
	"html"
	"strings"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"gorm.io/gorm"
)

func (t *TeleBot) checkConfigHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	chatID := update.Message.Chat.ID
	db := t.ydb.GetDB()

	var sb strings.Builder
	// Password is intentionally NEVER shown or measured here (owner directive
	// 2026-10-10) — the diagnostic only needs to reveal hidden characters in the
	// username and an invalid server id, both of which are safe to print.
	if u, err := model.GetUser(db, fmt.Sprintf("%d", chatID)); err == nil && u != nil {
		sb.WriteString(fmt.Sprintf("🔎 <b>Cấu hình của bạn</b> (ID %s)\n", html.EscapeString(u.ID)))
		sb.WriteString(fmt.Sprintf("<code>%s</code>\n", html.EscapeString(configCredLine("username", u.Username))))
		sb.WriteString(fmt.Sprintf("<code>%s</code>\n", html.EscapeString(configServerLine(u.ServerID))))
		sb.WriteString(fmt.Sprintf("<code>game_uid = %d</code>\n", u.GameUID))
	}

	if t.isLeader(chatID) {
		if bad := t.leaderBadConfigs(db, fmt.Sprintf("%d", chatID)); bad != "" {
			sb.WriteString("\n<b>Acc đáng ngờ (đệ tử + sự kiện):</b>\n" + bad)
		} else if sb.Len() > 0 {
			sb.WriteString("\n✅ Không phát hiện đệ tử/acc sự kiện nào có server_id lỗi hay credential chứa kí tự ẩn.")
		}
	}

	if sb.Len() == 0 {
		sb.WriteString("không đọc được cấu hình của bạn")
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          chatID,
		Text:            sb.String(),
		ParseMode:       models.ParseModeHTML,
		ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
	})
}

// leaderBadConfigs returns HTML lines for every member/event account of this
// leader whose server id is invalid or whose credentials hide invisible runes.
func (t *TeleBot) leaderBadConfigs(db *gorm.DB, leaderID string) string {
	var out strings.Builder
	note := func(prefix, id, uname string, sid int) {
		issues := []string{}
		if sid <= 0 {
			issues = append(issues, "server_id vô hiệu (0)")
		}
		if n, _, bad := hasInvisible(uname); bad {
			issues = append(issues, fmt.Sprintf("username có %d kí tự ẩn", n))
		}
		if len(issues) == 0 {
			return
		}
		out.WriteString(fmt.Sprintf("• %s <code>%s</code> (ID %s): %s\n",
			prefix, html.EscapeString(uname), html.EscapeString(id), strings.Join(issues, ", ")))
	}
	if ms, err := model.GetMembersByLeader(db, leaderID); err == nil {
		for _, m := range ms {
			note("đệ tử", m.ID, m.Username, m.ServerID)
		}
	}
	if evs, err := model.GetEventUsersByLeader(db, leaderID); err == nil {
		for _, e := range evs {
			note("sự kiện", e.ID, e.Username, e.ServerID)
		}
	}
	return out.String()
}
