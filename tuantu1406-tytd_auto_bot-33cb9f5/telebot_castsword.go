package tytd_go

// Telegram surface for Luyện Khí Đại Hội (remoteCastSword, type 119): a
// 4x4 2048 weapon board the bot can play itself with FREE moves only.
//
//	/lk        — status: window open? + last cached board/score/strength
//	/lk_choi | /start_luyenkhi — auto-play the board to the end (free moves
//	                  only; a half-played board is CONTINUED, never restarted,
//	                  and the bot never settles/ends the run itself)
//	/lk_dung   — stop an auto-play run (the open board is kept for a later resume)

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// csPlayer pulls the running *Player or replies the login hint.
func (t *TeleBot) csPlayer(ctx context.Context, b *bot.Bot, update *models.Update) (*Player, bool) {
	message := update.Message
	v, ok := t.session.Load(message.Chat.ID)
	p, okPlayer := v.(*Player)
	if !ok || !okPlayer || !p.Running {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "login đã đạo hữu",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return nil, false
	}
	return p, true
}

// /lk — cache-only status (no game request; safe mid-seat).
func (t *TeleBot) lkHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	p, ok := t.csPlayer(ctx, b, update)
	if !ok {
		return
	}
	bd, st := p.csBoard()
	_, err := p.csActiveID()
	open := err == nil
	lines := []string{"🗡 Luyện Khí Đại Hội"}
	if !open {
		lines = append(lines, "sự kiện chưa mở cho tài khoản này (xem /activities)")
	} else {
		lines = append(lines, fmt.Sprintf("thể lực: %d · điểm: %d · best: %d", st.Strength, st.Score, st.HighScore))
		lines = append(strings.Split(csBoardText(bd), "\n"), lines...)
		p.CsMu.Lock()
		if p.csPlaying {
			lines = append(lines, "⏳ đang tự chơi (/lk_dung để dừng)")
		}
		p.CsMu.Unlock()
		lines = append(lines, "/start_luyenkhi = tự chơi (đánh tiếp bàn dở, không end game)")
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            strings.Join(lines, "\n"),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// /lk_choi — run the board to the end.
func (t *TeleBot) lkChoiHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.lkStart(ctx, b, update)
}

// /start_luyenkhi (user 2026-10-08): same engine, phrased as "start luyện
// khí". ENTER is only the client's base-data read, so a run that is already
// half-played is RESUMED from the server board — nothing here restarts or
// ends the game.
func (t *TeleBot) startLuyenKhiHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.lkStart(ctx, b, update)
}

func (t *TeleBot) lkStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	p, ok := t.csPlayer(ctx, b, update)
	if !ok {
		return
	}
	p.CsMu.Lock()
	if p.csPlaying {
		p.CsMu.Unlock()
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "acc này đang tự chơi luyện khí rồi, đợi xong hoặc /lk_dung",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	stop := make(chan struct{})
	p.csStop = stop
	p.CsMu.Unlock()
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "⚔️ Tự chơi Luyện Khí (nước đi miễn phí, không item; bàn dở thì đánh tiếp, không restart/end game)… /lk_dung để dừng",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	go func() {
		moves, score, why, err := p.CastSwordAutoPlay(stop)
		text := fmt.Sprintf("🗡 Luyện Khí: dừng (%s) — %d nước, điểm %d", why, moves, score)
		if err != nil {
			text = "🗡 Luyện Khí lỗi: " + err.Error()
		}
		t.pushNotify(message.Chat.ID, text)
	}()
}

// lkStartWeb is the web-button twin of lkStart (no Telegram update to reply
// to): single-run guarded, resume-only, auto-play NEVER settles/ends the run.
// Notifies chatID (account owner's chat) + any extra ids (e.g. the leader who
// clicked) when the run stops; pushWebState keeps open tabs truthful.
func (t *TeleBot) lkStartWeb(p *Player, chatID int64, notifyAlso ...int64) string {
	p.CsMu.Lock()
	if p.csPlaying {
		p.CsMu.Unlock()
		return "!!acc này đang tự chơi luyện khí rồi, đợi xong hoặc /lk_dung"
	}
	stop := make(chan struct{})
	p.csStop = stop
	p.CsMu.Unlock()
	go func() {
		moves, score, why, err := p.CastSwordAutoPlay(stop)
		text := fmt.Sprintf("🗡 Luyện Khí: dừng (%s) — %d nước, điểm %d", why, moves, score)
		if err != nil {
			text = "🗡 Luyện Khí lỗi: " + err.Error()
		}
		t.pushNotify(chatID, text)
		for _, id := range notifyAlso {
			if id != chatID {
				t.pushNotify(id, text)
			}
		}
		t.pushWebState(chatID)
	}()
	t.pushWebState(chatID)
	return "⚔️ Đã giao bot tự chơi Luyện Khí (nước đi miễn phí, không item; bàn dở thì đánh tiếp, không restart/end game). /lk_dung để dừng."
}

// /lk_dung — stop the in-flight auto-play; the run stays open server-side for /start_luyenkhi.
func (t *TeleBot) lkDungHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	p, ok := t.csPlayer(ctx, b, update)
	if !ok {
		return
	}
	p.CsMu.Lock()
	playing := p.csPlaying
	var stop chan struct{}
	if playing {
		stop = p.csStop
	}
	p.CsMu.Unlock()
	text := "không có ván luyện khí nào đang chạy"
	if playing && stop != nil {
		close(stop)
		text = "⏹ Đã dừng — ván vẫn GIỮ NGUYÊN (bot không end game), /start_luyenkhi để chơi tiếp"
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            text,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}
