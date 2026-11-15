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

// UserSetAutoSeaWarHandler toggles the auto-play of Thương Minh Yêu Vực
// (CelestialSeaWar, activity type 349) — /auto_thuongminh. Saved to the DB
// and re-read by the loop every wake (Player-object gate, owner 2026-10-27),
// so it applies live without re-login. ON: the bot runs the yêu vực ALL DAY
// while the activity is open, FREE actions only — explore, monster/PVP
// fights, boxes, water spring, luyện bảo thạch (ưu tiên sát thương cuối),
// free next-region, bái, nhận phúc lợi yêu minh + nước; the guild-war
// family (khiêu chiến/tuyên chiến) is NEVER automated and the energy item
// is NEVER spent automatically [[tytd-item-ban]] — that stays
// /thuongminh_suc. Event accounts run the same loop with no switch
// (login-là-chạy).
func (t *TeleBot) UserSetAutoSeaWarHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /auto_thuongminh <true/false>",
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
		log.Printf("SetAutoSeaWarHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	u.AutoSeaWar = val
	if err := model.CreateUser(t.ydb.GetDB(), u); err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	t.patchAutoGates(message.Chat.ID, func(p *Player) { p.AutoSeaWar = val })

	state := "tắt"
	if val {
		state = "bật"
	}
	extra := "Bot tự khám yêu vực cả ngày: đánh quái, đánh người yếu nhất, mở rương, thám tuyền, luyện bảo thạch ƯU TIÊN sát thương cuối, sang tầng miễn phí, bái, nhận phúc lợi + nước yêu minh; không đánh gia tộc chiến, không tự dùng vật phẩm thể lực (bấm /thuongminh_suc). Acc sự kiện tự chạy ngay khi login."
	if !val {
		extra = "Vòng tự chạy sẽ dừng ở nhịp tiếp theo, không thao tác thêm."
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID,
		Text: fmt.Sprintf("Đã %s tự chơi Thương Minh Yêu Vực. %s "+
			"Cài đặt lưu vĩnh viễn, đang chạy sẽ áp dụng ngay.", state, extra),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// SeaWarSucHandler — /thuongminh_suc [số]: the OWNER-ONLY energy-item
// button (234922 USE_ITEM). The ONLY place the 349 item is ever spent —
// the auto engine never sends it [[tytd-item-ban]]; every verdict (ret≠0)
// comes back as a one-line alert per account. Fires on the leader's own
// session plus every running event account (eventOrderBroadcast).
func (t *TeleBot) SeaWarSucHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)
	num := 1
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 || n > 20 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "usage: /thuongminh_suc [1..20] — số vật phẩm thể lực mỗi acc",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
		num = n
	}
	// Own session first (fire-and-forget; 886a verdict alerts via
	// swHandleFrame).
	if v, ok := t.session.Load(message.Chat.ID); ok {
		if p, okPlayer := v.(*Player); okPlayer && p.Running {
			if err := p.swUseItemOrder(int32(num)); err != nil {
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID:          message.Chat.ID,
					Text:            "🌊 acc chính: " + err.Error(),
					ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
				})
				return
			}
		}
	}
	// Event accounts — leader-only broadcast, per-account failures surface
	// as fail rows (same idiom as /kunlun_hoisinh item).
	t.eventOrderBroadcast(ctx, b, update,
		func(moved int) string {
			return fmt.Sprintf("🌊 Đã lệnh %d acc sự kiện dùng %d vật phẩm thể lực Thương Minh Yêu Vực", moved, num)
		},
		func(p *EventPlayer) error { return p.swUseItemOrder(int32(num)) })
}
