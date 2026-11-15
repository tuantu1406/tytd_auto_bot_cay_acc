package tytd_go

// Đan Giới / Linh Điền — user-facing command. The cày loop itself has no
// toggle at all: always-ON for every account (owner 2026-10-17 "mặc định bật
// không cho tắt" — the /auto_dangioi command, the DanGioiOff DB field and the
// web checkbox were all deleted). /dangioi is read-only diagnostics.
// [[tytd-dangioi-elixir]]

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// danGioiHandler — /dangioi: one live ENTER, print the field as text. Read-
// only [[server-ram-policy]]; safe to run any time the acc is logged in.
func (t *TeleBot) danGioiHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	v, ok := t.session.Load(update.Message.Chat.ID)
	p, okPlayer := v.(*Player)
	if !ok || !okPlayer || !p.Running {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "login đã đạo hữu",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
		return
	}
	line, err := p.elixirSnapshot()
	if err != nil {
		line = "không đọc được đan giới: " + err.Error()
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		Text:            line,
		ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
	})
}
