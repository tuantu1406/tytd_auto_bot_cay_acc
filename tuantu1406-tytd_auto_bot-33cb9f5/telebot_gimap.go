package tytd_go

import (
	"bytes"
	"context"
	"log"
	"time"
	"tytd_go/internal/gimap"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// giMapHandler is the shared core of /bonglai_map and /thienma_map for a
// logged-in user: ask the server for the player's bracket minimap (206834) on
// one God Island variant, decode the ownership payload, render it over that
// variant's CDN geometry (mapOutList / mapOutListPeak) and send the PNG back.
// Rendering happens in a goroutine so the update handler returns immediately
// (config fetch + render can take a few seconds).
func (t *TeleBot) giMapHandler(ctx context.Context, b *bot.Bot, update *models.Update, which int) {
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

	peak := which == GIEventPeak
	peakName, fileName, caption := "đảo bồng lai", "bonglai_map.png", "Đảo bồng lai — map hiện tại"
	if peak {
		peakName, fileName, caption = "đảo thiên ma", "thienma_map.png", "Đảo thiên ma — map hiện tại"
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "đang lấy map " + peakName + "...",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})

	chatID := message.Chat.ID
	go func() {
		// The update ctx is done once this handler returns; use a fresh one.
		rctx := context.Background()
		payload, err := p.RequestGodIslandMiniMap(which, 15*time.Second)
		if err != nil {
			b.SendMessage(rctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   err.Error(),
			})
			return
		}
		render := gimap.Render
		if peak {
			render = gimap.RenderPeak
		}
		img, err := render(payload, 1400)
		if err != nil {
			log.Printf("[gi_map] render error: %v", err)
			b.SendMessage(rctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "lỗi render map: " + err.Error(),
			})
			return
		}
		appPage, appLabel := "bonglai", "🌴 Mở Bồng Lai trực tiếp"
		if peak {
			appPage, appLabel = "thienma", "🔥 Mở Thiên Ma trực tiếp"
		}
		_, err = b.SendPhoto(rctx, &bot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       &models.InputFileUpload{Filename: fileName, Data: bytes.NewReader(img)},
			Caption:     caption,
			ReplyMarkup: webAppKB(t.appURL(appPage), appLabel),
		})
		if err != nil {
			log.Printf("[gi_map] send photo error: %v", err)
		}
	}()
}

// bonglaiMapHandler answers /bonglai_map (normal island).
func (t *TeleBot) bonglaiMapHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giMapHandler(ctx, b, update, GIEventNormal)
}

// thienmaMapHandler answers /thienma_map (peak island).
func (t *TeleBot) thienmaMapHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.giMapHandler(ctx, b, update, GIEventPeak)
}

// ballGVGMapHandler answers /ditich_map with the active Thượng Cổ Di Tích room
// map. The 21111 response is self-contained for room ownership and camp
// populations; static activity config is intentionally not required.
func (t *TeleBot) ballGVGMapHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	v, ok := t.session.Load(message.Chat.ID)
	p, okPlayer := v.(*Player)
	if !ok || !okPlayer || !p.Running {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID, Text: "login đã đạo hữu",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID, Text: "đang lấy map Thượng Cổ Di Tích...",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	chatID := message.Chat.ID
	go func() {
		rctx := context.Background()
		payload, err := p.RequestBallGVGMap(15 * time.Second)
		if err != nil {
			b.SendMessage(rctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
			return
		}
		img, err := gimap.RenderBallGVG(payload, 900)
		if err != nil {
			log.Printf("[ball_gvg_map] render error: %v", err)
			b.SendMessage(rctx, &bot.SendMessageParams{ChatID: chatID, Text: "lỗi render map: " + err.Error()})
			return
		}
		if _, err := b.SendPhoto(rctx, &bot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       &models.InputFileUpload{Filename: "thuong_co_di_tich_map.png", Data: bytes.NewReader(img)},
			Caption:     "Thượng Cổ Di Tích — map hiện tại",
			ReplyMarkup: webAppKB(t.appURL("ditich"), "🏯 Mở Di Tích trực tiếp"),
		}); err != nil {
			log.Printf("[ball_gvg_map] send photo error: %v", err)
		}
	}()
}
