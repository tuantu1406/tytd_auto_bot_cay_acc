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

func (t *TeleBot) UserSetAutoLoginHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /auto_login <true/false>",
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
		log.Printf("SetAutoLoginHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u.AutoLogin = val
	err = model.CreateUser(t.ydb.GetDB(), u)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("auto_login set to %t", val),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

func (t *TeleBot) UserSetAutoMouseHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /auto_start_chuot <true/false>",
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
		log.Printf("SetAutoMouseHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u.AutoMouse = val
	err = model.CreateUser(t.ydb.GetDB(), u)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          message.Chat.ID,
		Text:            fmt.Sprintf("auto_mouse set to %t", val),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// UserSetAutoGatherHandler toggles the account's permanent Tụ Linh switch
// (auto gather energy). It is saved to the DB (survives re-login) AND patched
// onto the live session's Player object, which is the only thing the gather
// loop reads (owner 2026-10-27: event gates live on the Player object, never
// a DB lookup — several rows can share one game_uid).
func (t *TeleBot) UserSetAutoGatherHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /auto_tu_linh <true/false>",
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
		log.Printf("SetAutoGatherHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u.AutoGather = val
	err = model.CreateUser(t.ydb.GetDB(), u)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Patch the LIVE session in place — the gather loop reads p.AutoGather,
	// not the DB (owner 2026-10-27: event gates live on the Player object).
	t.patchAutoGates(message.Chat.ID, func(p *Player) { p.AutoGather = val })

	state := "tắt"
	if val {
		state = "bật"
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID,
		Text: fmt.Sprintf("Đã %s tự động Tụ Linh (thu năng lượng). Cài đặt lưu vĩnh viễn, "+
			"đang chạy sẽ áp dụng ngay phút thu tiếp theo.", state),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// UserSetAutoSkyTradeHandler toggles the auto caravan for Tiên Vực Thương Đồ
// (/auto_thiendo_tienvuc). Saved to the DB AND patched onto the live session
// (the loop reads the Player field p.AutoSkyTrade every wake), so it applies
// live without re-login: on, the bot buys/sails only on
// legs that profit and reminds you every 3 minutes when nothing does; off,
// the ship simply stops between legs.
func (t *TeleBot) UserSetAutoSkyTradeHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /auto_thiendo_tienvuc <true/false>",
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
		log.Printf("SetAutoSkyTradeHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u.AutoSkyTrade = val
	err = model.CreateUser(t.ydb.GetDB(), u)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Live-session patch: the caravan loop reads p.AutoSkyTrade on every
	// wake (Player-object gate, owner 2026-10-27).
	t.patchAutoGates(message.Chat.ID, func(p *Player) { p.AutoSkyTrade = val })

	state := "tắt"
	if val {
		state = "bật"
	}
	extra := "Thuyền chỉ chạy khi thấy chuyến có lời; chưa có lời sẽ nhắc bạn mỗi 3 phút."
	if !val {
		extra = "Thuyền sẽ dừng sau lượt hiện tại, không mua bán thêm."
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID,
		Text: fmt.Sprintf("Đã %s tự chạy thuyền Tiên Vực Thương Đồ (mua/bán tự động). %s "+
			"Cài đặt lưu vĩnh viễn, đang chạy sẽ áp dụng ngay.", state, extra),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// UserSetAutoSkyTradeSpeedHandler toggles the end-of-day acceleration sprint
// (/auto_tangtoc_tienvuc). Saved to the DB AND patched onto the live session
// (the loop reads the Player field p.AutoSkyTradeSpeed every wake): on, at
// 21:58 VN time (the event closes at 22:00) the bot burns
// acceleration items until the in-flight ship docks, then sells, buys and
// runs again in the time left — and if the day ends with nothing profitable
// it loads the port's specialty and sails one last random-leg gamble; off,
// the ship just waits out its normal travel time.
func (t *TeleBot) UserSetAutoSkyTradeSpeedHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /auto_tangtoc_tienvuc <true/false>",
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
		log.Printf("SetAutoSkyTradeSpeedHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u.AutoSkyTradeSpeed = val
	err = model.CreateUser(t.ydb.GetDB(), u)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	// Live-session patch: the eod sprint reads p.AutoSkyTradeSpeed on every
	// wake (Player-object gate, owner 2026-10-27).
	t.patchAutoGates(message.Chat.ID, func(p *Player) { p.AutoSkyTradeSpeed = val })

	state := "tắt"
	if val {
		state = "bật"
	}
	extra := "Lúc 21:58 thuyền đang chạy sẽ được tăng tốc cập cảng ngay, rồi bán/mua và chạy chuyến mới tới khi sự kiện đóng (22:00). Nếu cuối ngày chưa có chuyến nào lời, bot mua đặc sản của cảng và dong buồm tới cảng ngẫu nhiên — biết đâu sáng mai 10:00 trúng giá lớn."
	if !val {
		extra = "Thuyền chờ về cảng theo giờ thường, không dùng giấy tăng tốc."
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID,
		Text: fmt.Sprintf("Đã %s tự tăng tốc tiến độ Thiên Vực. %s "+
			"Cần bật sẵn /auto_thiendo_tienvuc để bot còn mua bán. Cài đặt lưu vĩnh viễn, áp dụng ngay.", state, extra),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}

// UserSetGatherMinHandler sets the account's minimum Tụ Linh formation
// income (the server-displayed speed score) to join one. Set from the live
// DB on every board scan and re-checked on every join, so it applies from
// the next scan without a re-login. 0 removes the floor (join the fastest
// formation available, whatever its income).
func (t *TeleBot) UserSetGatherMinHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !t.auth(ctx, b, update) {
		return
	}
	message := update.Message
	args := strings.Fields(message.Text)

	if len(args) != 2 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "usage: /tu_linh_min <thu_nhap_toi_thieu>  (0 = không giới hạn)",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	val, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || val < 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "giá trị phải là số từ 0 trở lên",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u, err := model.GetUser(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		log.Printf("UserSetGatherMinHandler error: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "server error",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	u.MinGatherIncome = val
	if err := model.CreateUser(t.ydb.GetDB(), u); err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID,
		Text: fmt.Sprintf("Ngưỡng Tụ Linh tối thiểu: %d — acc chỉ ghép trận có thu nhập từ mức này "+
			"(0 = không giới hạn). Áp dụng từ lượt quét bảng trận tiếp theo.", val),
		ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
	})
}
