package tytd_go

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// UserSetAutoRebornTrialHandler toggles the auto-play of Luân Hồi Bí Cảnh
// (RebornTrial, systemOpenId 172) — /auto_luanhoi. Saved to the DB and
// re-read by the loop every wake, so it applies live without re-login:
// on, the bot drives the caravan map ALL DAY once a battle round opens,
// using free operations only (explore, resolve/skip events, worship,
// tree + guess claims); the guild war is NEVER auto — event accounts fight
// only on the player's ⚔️ order from the mini map (owner 2026-11-08);
// off, the loop parks at its next check. Items are never spent [[tytd-item-ban]].
func (t *TeleBot) UserSetAutoRebornTrialHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /auto_luanhoi <true/false>",
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
		log.Printf("SetAutoRebornTrialHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u.AutoRebornTrial = val
	err = model.CreateUser(t.ydb.GetDB(), u)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Live-session patch: the auto loop reads p.AutoRebornTrial on every
	// wake (Player-object gate, owner 2026-10-27).
	t.patchAutoGates(message.Chat.ID, func(p *Player) { p.AutoRebornTrial = val })

	state := "tắt"
	if val {
		state = "bật"
	}
	extra := "Bot chạy xe thư thương cả ngày khi vòng chiến mở, chỉ nước đi miễn phí (khám sự kiện, bái, thu quả/nhận nước cây bang, nhận quà dự đoán); trận chiến bang 19h–22h bot để bạn tự đánh; không mua thể lực bằng item, không quyên quà."
	if !val {
		extra = "Vòng tự chạy sẽ dừng ở nhịp tiếp theo, không thao tác thêm."
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID,
		Text: fmt.Sprintf("Đã %s tự chơi Luân Hồi Bí Cảnh. %s "+
			"Cài đặt lưu vĩnh viễn, đang chạy sẽ áp dụng ngay.", state, extra),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// rbtShopHandler lists the shop of Luân Hồi Bí Cảnh straight from the cached
// 21003 config — no game request, safe at any moment — and marks which packs
// the auto-buyer covers (free/ad-watch + tiên ngọc capped at buyLimit/day;
// nạp-tiền packs are shown but never sent).
func (t *TeleBot) rbtShopHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
	p.RbtMu.Lock()
	rows := append([]*proto.ActivityMallTempMsg{}, p.rbtMallRows...)
	bought, adds := p.rbtMallBought, p.rbtMallAdds
	p.RbtMu.Unlock()
	if len(rows) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🌀 Shop Luân Hồi Bí Cảnh chưa về cache — chờ kỳ đồng bộ 21003 kế tiếp.",
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
	lines := []string{"🌀 Shop Luân Hồi Bí Cảnh:"}
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

// rbtAttackHandler — /luanhoi_attack [số thứ tự]: the player's direct war
// order. Bang chiến ORDER law (owner 2026-11-08 "đánh bang chiến chỉ đánh ở
// event thôi, theo lệnh của player trên mini map"): the hit is executed by
// the leader's EVENT accounts via t.rbtOrderAttack (same path as the map's
// ⚔️ button) — the regular account never fights, nothing auto-fights.
// No argument: refresh 217230 and list the targets numbered; with an
// argument: every online event account challenges the n-th target (free
// attempts only, never an item [[tytd-item-ban]]).
func (t *TeleBot) rbtAttackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)
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
	actID, _, active := p.rbtActiveActivity()
	if !active {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🌀 Luân Hồi Bí Cảnh chưa mở — chưa có bảng đấu.",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	pick := 0
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "usage: /luanhoi_attack  (xem mục tiêu) · /luanhoi_attack <số thứ tự>  (tấn công)",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
		pick = n
	}
	rsp, err := p.rbtBattleList(actID)
	if err != nil || rsp.GetRet() != 0 {
		note := "không lấy được bảng đấu"
		if err != nil {
			note += " (" + err.Error() + ")"
		} else {
			note += fmt.Sprintf(" (ret %d)", rsp.GetRet())
		}
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🌀 " + note + " — acc chưa vào vòng chiến?",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	nodes := luanHoiNodes(p, int64(p.ClanID))
	if pick == 0 {
		ws := t.rbtWarriors(message.Chat.ID)
		total := 0
		for _, ep := range ws {
			ep.RbtMu.Lock()
			total += int(ep.rbtBattleNum)
			ep.RbtMu.Unlock()
		}
		lines := []string{fmt.Sprintf("🌀 Bảng đấu Luân Hồi — %d acc sự kiện sẵn sàng đánh, tổng lượt còn %d:", len(ws), total)}
		for i, n := range nodes {
			mark := ""
			if n.IsSelf {
				mark = " ← bang của bạn (không đánh được)"
			}
			sh := ""
			if n.Shield {
				sh = " 🛡"
			}
			df := ""
			if n.Defeat {
				df = " ✖(thua)"
			}
			lines = append(lines, fmt.Sprintf("%d. %s — hạng %d · điểm %d · máu %d%s%s%s",
				i+1, n.Name, n.Rank, n.Score, n.LeftHP, sh, df, mark))
		}
		lines = append(lines, "⚔️ /luanhoi_attack <số> để tấn công.")
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            strings.Join(lines, "\n"),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	if pick > len(nodes) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            fmt.Sprintf("🌀 chỉ có %d mục tiêu — xem lại /luanhoi_attack", len(nodes)),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	tgt := nodes[pick-1]
	if tgt.IsSelf {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🌀 đó là bang của bạn — chọn mục tiêu khác.",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	// Bang chiến ORDER law (owner 2026-11-08): only event accounts fight,
	// and only on the player's order — this command is exactly that order.
	lines, err := t.rbtOrderAttack(message.Chat.ID, tgt.ID, tgt.Name)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🌀 " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            "⚔️ " + tgt.Name + "\n" + strings.Join(lines, "\n"),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// rbtTicketHandler — /luanhoi_ve [số]: bites challenge tickets from the bag
// for +N attack attempts. The ONLY place USE_ITEM is ever sent, and only
// because the owner typed the command [[tytd-item-ban]].
func (t *TeleBot) rbtTicketHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)
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
	num := 1
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 || n > 5 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:          message.Chat.ID,
				Text:            "usage: /luanhoi_ve [1..5] — số vé cắn mỗi lệnh",
				ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
			})
			return
		}
		num = n
	}
	actID, _, active := p.rbtActiveActivity()
	if !active {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🌀 Luân Hồi Bí Cảnh chưa mở — chưa cần vé.",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	rsp, err := p.rbtUseTicket(actID, num)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "🌀 gửi lệnh cắn vé thất bại: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	if rsp.GetRet() != 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            fmt.Sprintf("🌀 server từ chối cắn vé (ret %d — trong túi hết vé?)", rsp.GetRet()),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("🎫 Đã cắn %d vé — lượt đánh còn %d", num, rsp.GetBattleNum()),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// rbtMapHandler — /luanhoi_map: PNG snapshot of the bracket (on-demand
// render, SERVER-RAM policy) with a button into the live mini app.
func (t *TeleBot) rbtMapHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
		Text:            "đang vẽ map Luân Hồi...",
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
	chatID := message.Chat.ID
	go func() {
		rctx := context.Background()
		actID, _, active := p.rbtActiveActivity()
		if !active {
			b.SendMessage(rctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "🌀 Luân Hồi Bí Cảnh chưa mở — chưa có map.",
			})
			return
		}
		if _, err := p.rbtBattleList(actID); err != nil {
			b.SendMessage(rctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "🌀 không lấy được bảng đấu: " + err.Error(),
			})
			return
		}
		nodes := luanHoiNodes(p, int64(p.ClanID))
		if len(nodes) == 0 {
			b.SendMessage(rctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "🌀 bảng đấu trống (acc chưa vào vòng chiến?)",
			})
			return
		}
		img, err := gimap.RenderLuanHoi(nodes, "Luân Hồi Bí Cảnh — bảng đấu", 1400)
		if err != nil {
			log.Printf("[lh_map] render error: %v", err)
			b.SendMessage(rctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "lỗi render map: " + err.Error(),
			})
			return
		}
		_, err = b.SendPhoto(rctx, &bot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       &models.InputFileUpload{Filename: "luanhoi_map.png", Data: bytes.NewReader(img)},
			Caption:     "Luân Hồi Bí Cảnh — bảng đấu hiện tại",
			ReplyMarkup: webAppKB(t.appURL("luanhoi"), "🌀 Mở map trực tiếp"),
		})
		if err != nil {
			log.Printf("[lh_map] send photo error: %v", err)
		}
	}()
}
