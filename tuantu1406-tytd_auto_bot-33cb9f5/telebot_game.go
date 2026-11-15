package tytd_go

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (t *TeleBot) startChuotHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.ChuotRun = true
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

func (t *TeleBot) stopChuotHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.ChuotRun = false
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

func (t *TeleBot) startNotifyHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Notify = true
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

func (t *TeleBot) stopNotifyHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Notify = false
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu, tao đã bị khoá mõm",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

func (t *TeleBot) startMaohiemHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Maohiem = true
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

func (t *TeleBot) stopMaohiemHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Maohiem = false
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

// Tây Du Phục Ma — auto spam ải chính tuyến (player_xiyou.go). Toggle lives
// on the Player object only (owner law: opt-in switches never persist by
// shared game_uid), same shape as /start_maohiem.

// armXiyou is what /start_taydu does to ONE player: bật toggle + xóa mọi
// park/stop của engine (main-line feasibility, hết-vé, loss-park). Dùng chung
// cho /start_taydu và /start_taydu_team.
func armXiyou(p *Player) {
	p.Xiyou = true
	p.xiyou.mainStopped.Store(false) // re-arm main line after a feasibility park
	p.xiyou.secStopped.Store(false)  // re-arm Ảo Cảnh after hết-vé stop
	p.xiyou.secParkID.Store(0)       // clear loss-park — start = retry ngay, bỏ qua 10-min hold
	p.xiyou.secParked.Store(false)
}

func (t *TeleBot) startXiyouHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			armXiyou(p)
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu — spam ải Tây Du",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// startXiyouTeamHandler — /start_taydu_team (chủ lệnh 2026-11-05): leader bật
// một phát là CẢ leader lẫn mọi đệ tử đều chạy auto Tây Du. Đệ tử chưa có
// session Running thì bỏ qua và liệt kê cho leader biết (toggle là Player-
// field — không có Player sống thì không có gì để bật, đúng luật gates).
func (t *TeleBot) startXiyouTeamHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "Leader mới có quyền bật cho cả đội! (hoặc mỗi người tự /start_taydu)",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	leaderID := fmt.Sprintf("%d", message.Chat.ID)

	// armByID = armXiyou trên session đang sống của một telegram id (string
	// id trước, thử numeric id như các handler đệ tử khác).
	armByID := func(id string) bool {
		v, ok := t.session.Load(id)
		if !ok {
			if numID, err := strconv.ParseInt(id, 10, 64); err == nil {
				v, ok = t.session.Load(numID)
			}
		}
		if !ok {
			return false
		}
		p, isP := v.(*Player)
		if !isP || !p.Running {
			return false
		}
		armXiyou(p)
		return true
	}

	leaderOn := armByID(leaderID)
	members, err := model.GetMembersByLeader(t.ydb.GetDB(), leaderID)
	if err != nil {
		log.Printf("startXiyouTeamHandler: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	on := 0
	var skipped []string
	for _, m := range members {
		if armByID(m.ID) {
			on++
		} else {
			name := m.Username
			if name == "" {
				name = m.ID
			}
			skipped = append(skipped, name)
		}
	}

	txt := fmt.Sprintf("🐵 Tây Du cả đội: bật %d/%d đệ tử", on, len(members))
	if leaderOn {
		txt += " (+ leader)"
	} else {
		txt += "\nLeader chưa login nên leader chưa bật."
	}
	txt += "."
	if len(skipped) > 0 {
		txt += "\nBỏ qua (chưa login/chưa chạy): " + strings.Join(skipped, ", ")
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            txt,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) stopXiyouHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Xiyou = false
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) startLucdaoHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Lucdao = true
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

func (t *TeleBot) stopLucdaoHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Lucdao = false
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

// startHuyetMachHandler handles /start_huyet_mach: the auto roll+xoá loop
// (player_huyetmach.go) runs while the flag is on. The flag is session
// runtime only — a re-login needs the command again, like the other toggles.
func (t *TeleBot) startHuyetMachHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	if v, ok := t.session.Load(message.Chat.ID); ok {
		if p, isP := v.(*Player); isP && p.Running {
			p.HuyetMach = true
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu — auto Huyết Mạch chạy: roll tối đa theo cấp, tự xoá kết quả",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) stopHuyetMachHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	if v, ok := t.session.Load(message.Chat.ID); ok {
		if p, isP := v.(*Player); isP && p.Running {
			p.HuyetMach = false
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) startYeuthapHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Yeuthap = true
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

func (t *TeleBot) stopYeuthapHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Yeuthap = false
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	return
}

func (t *TeleBot) setChuotPeachHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) < 2 || len(args) > 3 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /min_dao <dao level> [user_id]",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	targetID := fmt.Sprintf("%d", message.Chat.ID)
	if len(args) == 3 {
		if !t.isLeader(message.Chat.ID) {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "Leader mới có quyền chỉnh cho đệ tử!",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}

		targetMember, err := model.GetUser(t.ydb.GetDB(), args[2])
		if err != nil || targetMember.LeaderID != targetID {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "Không tìm thấy đệ tử này hoặc đệ tử không thuộc quyền quản lý!",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
		targetID = args[2]
	}

	u, err := model.GetUser(t.ydb.GetDB(), targetID)
	if err != nil {
		log.Printf("setChuotPeachHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	num, _ := strconv.Atoi(args[1])
	u.MinPeachLevel = num

	model.CreateUser(t.ydb.GetDB(), u)

	v, ok := t.session.Load(targetID)
	if !ok {
		// Try numeric ID if targetID is a string representation of numeric ID
		if numID, err := strconv.ParseInt(targetID, 10, 64); err == nil {
			v, ok = t.session.Load(numID)
		}
	}

	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.ChuotMinDao = num
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "okey đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	return
}

func (t *TeleBot) setChuotBottleHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) < 2 || len(args) > 3 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /min_binh <binh level> [user_id]",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	targetID := fmt.Sprintf("%d", message.Chat.ID)
	if len(args) == 3 {
		if !t.isLeader(message.Chat.ID) {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "Leader mới có quyền chỉnh cho đệ tử!",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}

		targetMember, err := model.GetUser(t.ydb.GetDB(), args[2])
		if err != nil || targetMember.LeaderID != targetID {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "Không tìm thấy đệ tử này hoặc đệ tử không thuộc quyền quản lý!",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
		targetID = args[2]
	}

	u, err := model.GetUser(t.ydb.GetDB(), targetID)
	if err != nil {
		log.Printf("setChuotBottleHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	num, _ := strconv.Atoi(args[1])
	u.MinBottleLevel = num

	model.CreateUser(t.ydb.GetDB(), u)

	v, ok := t.session.Load(targetID)
	if !ok {
		// Try numeric ID if targetID is a string representation of numeric ID
		if numID, err := strconv.ParseInt(targetID, 10, 64); err == nil {
			v, ok = t.session.Load(numID)
		}
	}

	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.ChuotMinBinh = num
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "okey đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	return
}

func (t *TeleBot) setChuotLuuLyHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) < 2 || len(args) > 3 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /min_luuly <luuly level> [user_id]",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	targetID := fmt.Sprintf("%d", message.Chat.ID)
	if len(args) == 3 {
		if !t.isLeader(message.Chat.ID) {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "Leader mới có quyền chỉnh cho đệ tử!",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}

		targetMember, err := model.GetUser(t.ydb.GetDB(), args[2])
		if err != nil || targetMember.LeaderID != targetID {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "Không tìm thấy đệ tử này hoặc đệ tử không thuộc quyền quản lý!",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
		targetID = args[2]
	}

	u, err := model.GetUser(t.ydb.GetDB(), targetID)
	if err != nil {
		log.Printf("setChuotLuuLyHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	num, _ := strconv.Atoi(args[1])
	u.MinLuuLyLevel = num

	model.CreateUser(t.ydb.GetDB(), u)

	v, ok := t.session.Load(targetID)
	if !ok {
		// Try numeric ID if targetID is a string representation of numeric ID
		if numID, err := strconv.ParseInt(targetID, 10, 64); err == nil {
			v, ok = t.session.Load(numID)
		}
	}

	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.ChuotMinLuuly = num
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "okey đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	return
}

func (t *TeleBot) startChatcayHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Peach = true
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	return
}

func (t *TeleBot) startChatcayXaVeHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Peach = true
			p.XaVe = true
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	return
}

func (t *TeleBot) stopChatcayHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.Peach = false
			p.XaVe = false
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "okey đạo hữu",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "login đã đạo hữu",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	return
}

func (t *TeleBot) SetBottlePreferedHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)
	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /bottle_pref <true/false>",
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

	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u.BottlePrefered = val
	model.CreateUser(t.ydb.GetDB(), u)

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("bottle prefered set to %t", val),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.BottlePrefered = val
		}
	}

}

func (t *TeleBot) RedeemHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) < 2 || len(args) > 3 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /redeem <code> [user_id]\nExample: /redeem skibidi 123456789",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	code := args[1]
	targetUserID := fmt.Sprintf("%d", message.Chat.ID) // Default: redeem for self

	// If user_id is provided, redeem for that user
	if len(args) == 3 {
		targetUserID = args[2]

		// Verify target user exists
		if !model.UserExists(t.ydb.GetDB(), targetUserID) {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            fmt.Sprintf("User ID %s does not exist", targetUserID),
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	err := model.RedeemGiftCode(t.ydb.GetDB(), code, targetUserID)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	responseText := "oke đạo hữu, + 30 ngay` VIP"
	if len(args) == 3 {
		responseText = fmt.Sprintf("oke đạo hữu, + 30 ngay` VIP for user %s", targetUserID)
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            responseText,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) startGrind100Handler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.StartGrind100()
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "🚀 Đã kích hoạt chế độ Cày Acc lên Lv 100!\n\n• Tự chặt đào & trang bị đồ tăng lực chiến\n• Tự nâng cấp Tiên Thụ & giảm giờ\n• Tự quét & nhận thưởng nhiệm vụ chính tuyến\n• Tự vượt ải mạo hiểm (chống kẹt khi boss mạnh)\n\nGõ /stop_lv100 để dừng bất cứ lúc nào.",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "Vui lòng /login tài khoản game trước khi bật cày cấp nhé đạo hữu!",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) stopGrind100Handler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message

	v, ok := t.session.Load(message.Chat.ID)
	if ok {
		p, isP := v.(*Player)
		if isP && p.Running {
			p.StopGrind100()
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "🛑 Đã tắt chế độ Cày Acc lên Lv 100.",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "Tài khoản chưa đăng nhập đạo hữu ơi.",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}
