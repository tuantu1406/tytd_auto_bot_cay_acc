package tytd_go

// Telegram layer for Côn Lôn Tranh Bá (KunlunWar, activity type 199).
// This pass is READ-ONLY: the whole 61×61 board answers in one 219919
// minimap pull, so /kunlun_map needs nothing but a running main account;
// /kunlun_info prints the cached ledger (phase, floor/group, rank, held
// cities). March/attack orders are not wired yet — see player_kunlunwar.go.

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

// kunlunMemberStatus is one Côn Lôn status line for /my_event_members and
// the web events page. Like Holy Land/BallGVG the state only lives in the
// live EventPlayer (scene enter / position pushes) — no session or no
// announced 199 window means an honest-hidden row.
func (t *TeleBot) kunlunMemberStatus(m model.EventUser) (string, bool) {
	if realUID, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
		if v, ok := t.session.Load(realUID); ok {
			if p, ok2 := v.(*EventPlayer); ok2 && p.KunlunWarEventOpen() {
				return p.KunlunStatus(), true
			}
		}
	}
	return "", false
}

// kunlunMapHandler answers /kunlun_map with the leader's own account's view
// of the Côn Lôn ownership board (one snapshot; the server answers the
// whole map — every city + every occupied cell + union rows).
func (t *TeleBot) kunlunMapHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
		ChatID: message.Chat.ID, Text: "đang lấy map Côn Lôn...",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	chatID := message.Chat.ID
	go func() {
		rctx := context.Background()
		payload, err := p.RequestKunlunMiniMap(15 * time.Second)
		if err != nil {
			b.SendMessage(rctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
			return
		}
		img, err := gimap.RenderKunlun(payload, 2048)
		if err != nil {
			log.Printf("[kunlun_map] render error: %v", err)
			b.SendMessage(rctx, &bot.SendMessageParams{ChatID: chatID, Text: "lỗi render map: " + err.Error()})
			return
		}
		caption := fmt.Sprintf("Côn Lôn Tranh Bá — %s\n(bấm nút dưới để mở map trực tiếp, pan/zoom + hành quân ngay trên ô)", time.Now().In(ictZone).Format("HH:mm:ss"))
		if _, err := b.SendPhoto(rctx, &bot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       &models.InputFileUpload{Filename: "kunlun_map.png", Data: bytes.NewReader(img)},
			Caption:     caption,
			ReplyMarkup: webAppKB(t.kunlunAppURL(), "🗺️ Mở Côn Lôn trực tiếp"),
		}); err != nil {
			log.Printf("[kunlun_map] send photo error: %v", err)
		}
	}()
}

// kunlunFollowHandler answers /kunlun_follow (no args): every running event
// user joins the leader's own account on the Côn Lôn board. The anchor is
// the leader's live stand-point (EnterGame/219933 cache; re-asked when
// unknown); event accounts auto-enter the game scene (219902 + 15s heartbeat)
// before marching, and the server alone decides reachability (ret 19944 for
// impossible routes shows in that account's log, not here).
func (t *TeleBot) kunlunFollowHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	if len(strings.Fields(update.Message.Text)) != 1 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: /kunlun_follow (không tham số — theo đúng vị trí bang chủ)",
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
		ChatID: message.Chat.ID, Text: "đang xác định vị trí bang chủ ở Côn Lôn...",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	chatID := message.Chat.ID
	go func() {
		rctx := context.Background()
		point, err := p.KunlunLeaderPoint(15 * time.Second)
		if err != nil {
			b.SendMessage(rctx, &bot.SendMessageParams{ChatID: chatID, Text: err.Error()})
			return
		}
		x, y := point%61, point/61
		t.eventOrderBroadcast(rctx, b, update, func(moved int) string {
			return fmt.Sprintf("🏔 %d acc sự kiện đang hành quân tới block %d (ô %d,%d) theo bang chủ", moved, point, x, y)
		}, func(ep *EventPlayer) error { return ep.KunlunWarMoveTo(point) })
	}()
}

// KunlunHoiSinhHandler handles /kunlun_hoisinh [item]: revive every running
// event user whose Côn Lôn army is dead. No argument = the FREE revive
// (same 219915 useItem=false the auto loop uses); "item" spends one revive
// item per account — the ONLY place kunlun items are ever touched, and only
// because the leader asked explicitly. Free auto-rebirth already runs by
// itself; this command is the manual override and the instant-item path.
// Per-account verdicts ("quân vẫn đang sống", "chưa tới giờ…") come back as
// the broadcast's fail rows; the server's async answer lands as a notify.
func (t *TeleBot) KunlunHoiSinhHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	args := strings.Fields(update.Message.Text)
	useItem := false
	switch len(args) {
	case 1:
	case 2:
		if strings.ToLower(args[1]) != "item" {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          update.Message.Chat.ID,
				Text:            "usage: /kunlun_hoisinh [item]",
				ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
			})
			return
		}
		useItem = true
	default:
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: /kunlun_hoisinh [item]",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
		return
	}
	summary := func(moved int) string {
		if useItem {
			return fmt.Sprintf("✨ Đã lệnh %d acc sự kiện hồi sinh Côn Lôn bằng VẬT PHẨM", moved)
		}
		return fmt.Sprintf("🔄 Đã lệnh %d acc sự kiện hồi sinh Côn Lôn miễn phí", moved)
	}
	t.eventOrderBroadcast(ctx, b, update, summary,
		func(p *EventPlayer) error { return p.KunlunWarRebirth(useItem) })
}

// kunlunInfoHandler answers /kunlun_info from the cached ledger — no game
// request is sent, so it works even mid-seat (a map pull in flight).
func (t *TeleBot) kunlunInfoHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
	st := p.KunlunStatus(time.Now())
	if id, _ := st["activityId"].(int32); id == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "sự kiện Côn Lôn chưa mở cho tài khoản này (xem /activities)",
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
	text := "🏔 Côn Lôn Tranh Bá\n" + "activity: " + fmt.Sprint(st["activityId"]) + "\n" +
		strings.Join(lines, "\n") + "\n" +
		"(lệnh hành quân/chia thành chưa mở cho bot — chỉ map & thông tin)"
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            text,
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}
