package tytd_go

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// UserSetAutoTienCanhHandler toggles the auto-play of Tiên Cảnh Vị Ương
// (WeYound, systemOpenId 186) — /auto_tiencanh. Saved to the DB and
// re-read by the loop every wake, so it applies live without re-login:
// on, the bot clears the fog map every day inside the explore window
// (10:00–22:00, folded from config), resolves events, worships, claims the
// floor box, waters the guild tree with every water bottle in the bag
// (only the ids listed in the event config), receives guild gifts and
// buys the free / ad-watch / tiên-ngọc shop packs to their daily cap.
// The stamina item is NEVER bitten and the guild war is never fought
// [[tytd-item-ban]]; off, the loop parks at its next check.
func (t *TeleBot) UserSetAutoTienCanhHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /auto_tiencanh <true/false>",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	val, err := strconv.ParseBool(args[1])
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID,
			Text:   "invalid bool: use true or false",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		log.Printf("SetAutoTienCanhHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u.AutoTienCanh = val
	err = model.CreateUser(t.ydb.GetDB(), u)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Live-session patch: the auto loop reads p.AutoTienCanh on every wake
	// (Player-object gate, owner 2026-10-27).
	t.patchAutoGates(message.Chat.ID, func(p *Player) { p.AutoTienCanh = val })

	state := "tắt"
	if val {
		state = "bật"
	}
	extra := "Bot tự cuốc bản đồ Tiên Cảnh Vị Ương trong giờ khám phá (khám sương mù, đánh quái, giải sự kiện, bái, mở rương tầng), tự tưới cây bang bằng đủ mọi chai nước đang có (chỉ các id trong config sự kiện), nhận quà bang, tự mua gói miễn phí / xem quảng cáo / gói tiên ngọc tới hạn ngày; KHÔNG bao giờ cắn item tăng thể lực, không đánh trận bang thay."
	if !val {
		extra = "Vòng tự chạy sẽ dừng ở nhịp tiếp theo, không thao tác thêm."
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID,
		Text: fmt.Sprintf("Đã %s tự chơi Tiên Cảnh Vị Ương. %s "+
			"Cài đặt lưu vĩnh viễn, đang chạy sẽ áp dụng ngay.", state, extra),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// wyShopHandler lists the shop of Tiên Cảnh Vị Ương straight from the cached
// 21003 config — no game request, safe at any moment — and marks which packs
// the auto-buyer covers (free/ad-watch + tiên ngọc capped at buyLimit/day;
// nạp-tiền packs are shown but never sent).
func (t *TeleBot) wyShopHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	v, ok := t.session.Load(message.Chat.ID)
	p, okPlayer := v.(*Player)
	if !ok || !okPlayer || !p.Running {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "login đã đạo hữu",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	p.WyMu.Lock()
	rows := append([]*proto.ActivityMallTempMsg{}, p.wyMallRows...)
	bought, adds := p.wyMallBought, p.wyMallAdds
	p.WyMu.Unlock()
	if len(rows) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID,
			Text:   "🌄 Shop Tiên Cảnh Vị Ương chưa về cache — chờ kỳ đồng bộ 21003 kế tiếp.",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	prices := func(pr string) string {
		switch {
		case pr == "0" || strings.HasSuffix(pr, "=0"):
			return "FREE"
		case strings.HasPrefix(pr, "100000="):
			return strings.TrimPrefix(pr, "100000=") + " tiên ngọc"
		}
		return pr + " (nạp)"
	}
	lines := []string{"🌄 Shop Tiên Cảnh Vị Ương:"}
	for _, mt := range rows {
		limit, ad, jade := rbtMallAuto(mt)
		used := bought[mt.GetId()]
		used += adds[mt.GetId()]
		tag := "🤖 auto"
		if limit == 0 {
			tag = "🚫 bot không mua"
		} else if ad {
			tag = "🤖 auto (xem qc)"
		} else if jade {
			tag = "🤖 auto (tiên ngọc)"
		}
		lines = append(lines, fmt.Sprintf("• %s — %s — đã mua %d/%d — %s",
			mt.GetName(), prices(mt.GetPrice()), used, max(mt.GetBuyLimit(), 1), tag))
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            strings.Join(lines, "\n"),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}
