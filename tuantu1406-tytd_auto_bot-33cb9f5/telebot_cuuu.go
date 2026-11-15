package tytd_go

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/gimap"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Cửu U (Holy Land) leader commands. Orders broadcast to every running
// event user of the leader through the shared island-order helpers (same
// machinery as /bonglai_*), and every refusal the account state raises comes
// back per account in game language. There is no auto-combat: attacks and
// auto-attacks fire ONLY from these leader orders.

// hlLabel is the Vietnamese event name used in every summary line.
const hlLabel = "Cửu U"

// CuuuMoveHandler handles /cuuu_move <city_id>: march every running
// event user of the leader to that holy-land city, walking only.
func (t *TeleBot) CuuuMoveHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.eventOrderWithArg(ctx, b, update, "/cuuu_move <city_id> (VD: /cuuu_move 1005)", "city id",
		fmt.Sprintf("🏯 Đã lệnh %%d event user (%s) hành quân tới thành %%d", hlLabel), 0,
		func(p *EventPlayer, cityID int32) error { return p.HolyLandMoveTo(cityID) })
}

// CuuuAttackHandler handles /cuuu_attack <city_id>: strike the city
// once, union defenders included (friendly fire on other leaders' accounts is
// the leader's call, same policy as the God Island attacks).
func (t *TeleBot) CuuuAttackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.eventOrderWithArg(ctx, b, update, "/cuuu_attack <city_id> (VD: /cuuu_attack 1005)", "city id",
		fmt.Sprintf("⚔️ Đã lệnh %%d event user (%s) tấn công thành %%d", hlLabel), 0,
		func(p *EventPlayer, cityID int32) error { return p.HolyLandAttackTo(cityID, false) })
}

// CuuuAttackNpcHandler handles /cuuu_attack_thuve <city_id>: strike
// the city once but only its NPC guards (thủ vệ), leaving union players alone.
func (t *TeleBot) CuuuAttackNpcHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.eventOrderWithArg(ctx, b, update, "/cuuu_attack_thuve <city_id> (VD: /cuuu_attack_thuve 1005)", "city id",
		fmt.Sprintf("🛡️ Đã lệnh %%d event user (%s) đánh thủ vệ thành %%d", hlLabel), 0,
		func(p *EventPlayer, cityID int32) error { return p.HolyLandAttackTo(cityID, true) })
}

// CuuuAutoHandler handles /cuuu_auto <city_id|0>: 托管 — park every
// running event user on the city and let the GAME auto-attack there (server-
// side 托管, not bot combat); 0 turns it off. Turning off is legal even while
// dead, so the arg is parsed here (0 passes) instead of eventOrderWithArg,
// which only admits positive ids.
func (t *TeleBot) CuuuAutoHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	args := strings.Fields(update.Message.Text)
	cityID := int32(0)
	if len(args) == 2 {
		n, err := strconv.ParseInt(args[1], 10, 32)
		if err != nil || n < 0 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          update.Message.Chat.ID,
				Text:            "city id không hợp lệ: " + args[1],
				ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
			})
			return
		}
		cityID = int32(n)
	} else if len(args) != 1 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: /cuuu_auto <city_id> (0 = tắt treo máy)",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
		return
	}
	off := cityID == 0
	summary := func(moved int) string {
		if off {
			return fmt.Sprintf("🛑 Đã tắt treo máy tấn công ở %d event user (%s)", moved, hlLabel)
		}
		return fmt.Sprintf("🎯 Đã lệnh %d event user (%s) treo máy đánh thành %d", moved, hlLabel, cityID)
	}
	t.eventOrderBroadcast(ctx, b, update, summary,
		func(p *EventPlayer) error { return p.HolyLandAutoAttack(cityID) })
}

// CuuuTangTocHandler handles /cuuu_tangtoc (no args): buy one march
// speed-up for every running event user currently marching. The request uses
// each account's current speed-up counter (from the residency poll), so no
// argument is needed; idle accounts refuse per account.
func (t *TeleBot) CuuuTangTocHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if len(strings.Fields(update.Message.Text)) != 1 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: /cuuu_tangtoc (không tham số)",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
		return
	}
	t.eventOrderBroadcast(ctx, b, update,
		func(moved int) string {
			return fmt.Sprintf("🚀 Đã lệnh %d event user (%s) tăng tốc", moved, hlLabel)
		},
		func(p *EventPlayer) error { return p.HolyLandAccelerate() })
}

// CuuuHoiSinhHandler handles /cuuu_hoisinh [item]: revive every dead
// running event user. Without an argument this is the free revive (needs the
// cooldown elapsed + a free count left — the same gate the auto-rebirth uses);
// with "item" it spends one revive item for an instant comeback. Rebirth is
// also automatic (free route only), this command is the manual override.
func (t *TeleBot) CuuuHoiSinhHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	args := strings.Fields(update.Message.Text)
	useItem := false
	switch len(args) {
	case 1:
	case 2:
		if strings.ToLower(args[1]) != "item" {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          update.Message.Chat.ID,
				Text:            "usage: /cuuu_hoisinh [item]",
				ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
			})
			return
		}
		useItem = true
	default:
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: /cuuu_hoisinh [item]",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
		return
	}
	summary := func(moved int) string {
		if useItem {
			return fmt.Sprintf("✨ Đã lệnh %d event user (%s) hồi sinh bằng vật phẩm", moved, hlLabel)
		}
		return fmt.Sprintf("🔄 Đã lệnh %d event user (%s) hồi sinh miễn phí", moved, hlLabel)
	}
	t.eventOrderBroadcast(ctx, b, update, summary,
		func(p *EventPlayer) error { return p.HolyLandRebirth(useItem) })
}

// cuuuMapHandler answers /cuuu_map for a logged-in regular player:
// ask the server for the player's bracket minimap, decode the ownership
// payload, render the embedded 34-city topology underneath and send the PNG
// back. Rendered in a goroutine so the update handler returns immediately.
func (t *TeleBot) cuuuMapHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "đang lấy map Cửu U...",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	chatID := message.Chat.ID
	go func() {
		// The update ctx is done once this handler returns; use a fresh one.
		rctx := context.Background()
		payload, err := p.RequestHolyLandMiniMap(15 * time.Second)
		if err != nil {
			b.SendMessage(rctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   err.Error(),
			})
			return
		}
		img, err := gimap.RenderHolyLand(payload, 1400)
		if err != nil {
			log.Printf("[hl_map] render error: %v", err)
			b.SendMessage(rctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "lỗi render map: " + err.Error(),
			})
			return
		}
		_, err = b.SendPhoto(rctx, &bot.SendPhotoParams{
			ChatID:  chatID,
			Photo:       &models.InputFileUpload{Filename: "cuuu_map.png", Data: bytes.NewReader(img)},
			Caption:     "Thánh địa — map hiện tại",
			ReplyMarkup: webAppKB(t.appURL("cuuu"), "⛩️ Mở Cửu U trực tiếp"),
		})
		if err != nil {
			log.Printf("[hl_map] send photo error: %v", err)
		}
	}()
}
