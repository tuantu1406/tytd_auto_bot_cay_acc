package tytd_go

// Telegram layer for Đỉnh Bắc Minh (北冥之巅, activity type 332) — the
// second KunlunWar-engine board. Commands mirror the Côn Lôn set but act on
// the NORTH ledger (Player.bm) and the 119² grid; the two events stay fully
// separate handlers because they are separate events (user directive), and
// the revive command is FREE-ONLY here (no item path — the Côn Lôn item
// exception does not port).

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// bacminhMemberStatus is one north status line for /my_event_members and
// the web events page (live EventPlayer state only — honest-hidden without
// a session or an active 332 window).
func (t *TeleBot) bacminhMemberStatus(m model.EventUser) (string, bool) {
	if realUID, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
		if v, ok := t.session.Load(realUID); ok {
			if p, ok2 := v.(*EventPlayer); ok2 && p.BacMinhWarEventOpen() {
				return p.BmStatus(), true
			}
		}
	}
	return "", false
}

// bacminhMapHandler answers /bacminh_map with a full ownership snapshot of
// the north board (one 219919 pull answers all 81 cities + 8821 cells).
func (t *TeleBot) bacminhMapHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
		ChatID: message.Chat.ID, Text: "đang lấy map Đỉnh Bắc Minh...",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	chatID := message.Chat.ID
	go func() {
		rctx := context.Background()
		payload, err := p.RequestBacMinhMiniMap(15 * time.Second)
		if err != nil {
			b.SendMessage(rctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
			return
		}
		img, err := gimap.RenderBacMinh(payload, 2048)
		if err != nil {
			log.Printf("[bacminh_map] render error: %v", err)
			b.SendMessage(rctx, &bot.SendMessageParams{ChatID: chatID, Text: "lỗi render map: " + err.Error()})
			return
		}
		caption := fmt.Sprintf("Đỉnh Bắc Minh — %s\n(bấm nút dưới để mở map trực tiếp, pan/zoom + hành quân ngay trên ô)", time.Now().In(ictZone).Format("HH:mm:ss"))
		if _, err := b.SendPhoto(rctx, &bot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       &models.InputFileUpload{Filename: "bacminh_map.png", Data: bytes.NewReader(img)},
			Caption:     caption,
			ReplyMarkup: webAppKB(t.bacminhAppURL(), "🗺️ Mở Bắc Minh trực tiếp"),
		}); err != nil {
			log.Printf("[bacminh_map] send photo error: %v", err)
		}
	}()
}

// bacminhFollowHandler answers /bacminh_follow (no args): every running
// event user joins the leader's own account on the NORTH board. The anchor
// is the leader's live north stand-point; cell math uses gimap.BacMinhCols
// (119), never a hard-coded width. Accounts auto-seat (219902 + heartbeat —
// refused while Côn Lôn holds that account's scene seat) and the server
// alone judges reachability.
func (t *TeleBot) bacminhFollowHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	if len(strings.Fields(update.Message.Text)) != 1 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: /bacminh_follow (không tham số — theo đúng vị trí bang chủ)",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
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
		ChatID: message.Chat.ID, Text: "đang xác định vị trí bang chủ ở Đỉnh Bắc Minh...",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	chatID := message.Chat.ID
	go func() {
		rctx := context.Background()
		point, err := p.BacMinhLeaderPoint(15 * time.Second)
		if err != nil {
			b.SendMessage(rctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
			return
		}
		cols := gimap.BacMinhCols()
		if cols <= 0 {
			cols = 1
		}
		x, y := point%int32(cols), point/int32(cols)
		t.eventOrderBroadcast(rctx, b, update, func(moved int) string {
			return fmt.Sprintf("🧊 %d acc sự kiện đang hành quân tới block %d (ô %d,%d) theo bang chủ", moved, point, x, y)
		}, func(ep *EventPlayer) error { return ep.BacMinhWarMoveTo(point) })
	}()
}

// BacMinhHoiSinhHandler handles /bacminh_hoisinh [item]: revive every
// running event user whose north army is dead. No argument = the FREE revive
// (what the auto loop runs too); "item" = the NHANH revive by item (219915
// useItem=true, server deducts its config item 105368) — user-authorized
// 2026-10-07 exactly like /kunlun_hoisinh item, and ONLY reachable from this
// explicit command.
func (t *TeleBot) BacMinhHoiSinhHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	args := strings.Fields(update.Message.Text)
	useItem := false
	switch len(args) {
	case 1:
	case 2:
		if strings.ToLower(args[1]) != "item" {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          update.Message.Chat.ID,
				Text:            "usage: /bacminh_hoisinh [item]",
				ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
			})
			return
		}
		useItem = true
	default:
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: /bacminh_hoisinh [item]",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
		return
	}
	summary := func(moved int) string {
		if useItem {
			return fmt.Sprintf("✨ Đã lệnh %d acc sự kiện hồi sinh Đỉnh Bắc Minh bằng VẬT PHẨM", moved)
		}
		return fmt.Sprintf("🔄 Đã lệnh %d acc sự kiện hồi sinh Đỉnh Bắc Minh miễn phí", moved)
	}
	t.eventOrderBroadcast(ctx, b, update, summary,
		func(p *EventPlayer) error { return p.BacMinhWarRebirth(useItem) })
}

// bacminhInfoHandler answers /bacminh_info from the cached north ledger —
// no game request, safe mid-seat.
func (t *TeleBot) bacminhInfoHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
	st := p.BacMinhStatus(time.Now())
	if id, _ := st["activityId"].(int32); id == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "sự kiện Đỉnh Bắc Minh chưa mở cho tài khoản này (xem /activities)",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	keys := make([]string, 0, len(st))
	for k := range st {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		if k == "activityId" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %v", k, st[k]))
	}
	text := "🧊 Đỉnh Bắc Minh\n" + "activity: " + fmt.Sprint(st["activityId"]) + "\n" +
		strings.Join(lines, "\n")
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            text,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}
