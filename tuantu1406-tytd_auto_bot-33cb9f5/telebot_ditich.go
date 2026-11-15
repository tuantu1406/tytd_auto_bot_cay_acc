package tytd_go

// Telegram layer for Thượng Cổ Di Tích (Ball GVG) — leader-only orders that
// drive every running event user at once, mirroring the /cuuu_* plumbing.
// These commands only move people around (enter/home/leave/mark); walking
// inside a room and every attack are automatic on the event accounts
// themselves (bgAuto in player_event_ballgvg.go). Room ownership maps live
// on /ditich_map (leader's own account, telebot_gimap.go).

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// ballGVGMemberStatus is one Thượng Cổ status line for /my_event_members.
// Like Holy Land there is no DB fallback — the state only lives in the live
// EventPlayer, so no session or no announced event means a hidden row.
func (t *TeleBot) ballGVGMemberStatus(m model.EventUser) (string, bool) {
	if realUID, err := strconv.ParseInt(m.ID, 10, 64); err == nil {
		if v, ok := t.session.Load(realUID); ok {
			if p, ok2 := v.(*EventPlayer); ok2 && p.BallGVGEventOpen() {
				return p.BallGVGStatus(), true
			}
		}
	}
	return "", false
}

// DitichEnterHandler handles /ditich_enter <cứ_điểm>: every running event
// user enters that battlefield room.
func (t *TeleBot) DitichEnterHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.eventOrderWithArg(ctx, b, update, "/ditich_enter <cứ_điểm> (xem id: /ditich_rooms)", "cứ điểm",
		"🏯 Đã lệnh %d event user (Thượng Cổ) vào cứ điểm %d", 0,
		func(p *EventPlayer, placeID int32) error { return p.BallGVGEnterRoom(placeID) })
}

// DitichMarkHandler handles /ditich_mark <cứ_điểm>: set the camp rally
// mark (server only honors the camp leader's account).
func (t *TeleBot) DitichMarkHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.eventOrderWithArg(ctx, b, update, "/ditich_mark <cứ_điểm>", "cứ điểm",
		"🚩 Đã lệnh %d event user (Thượng Cổ) đánh dấu cứ điểm %d", 0,
		func(p *EventPlayer, placeID int32) error { return p.BallGVGMark(placeID) })
}

// DitichVeHandler handles /ditich_ve (no args): everyone returns to their
// camp's home room.
func (t *TeleBot) DitichVeHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if len(strings.Fields(update.Message.Text)) != 1 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: /ditich_ve (không tham số)",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
		return
	}
	t.eventOrderBroadcast(ctx, b, update,
		func(moved int) string {
			return fmt.Sprintf("🏠 Đã lệnh %d event user (Thượng Cổ) về cứ điểm nhà", moved)
		},
		func(p *EventPlayer) error { return p.BallGVGHome() })
}

// DitichLeaveHandler handles /ditich_leave (no args): everyone steps back
// to the activity lobby. Works while dead too (waiting out a revive).
func (t *TeleBot) DitichLeaveHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if len(strings.Fields(update.Message.Text)) != 1 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          update.Message.Chat.ID,
			Text:            "usage: /ditich_leave (không tham số)",
			ReplyParameters: &models.ReplyParameters{MessageID: update.Message.ID},
		})
		return
	}
	t.eventOrderBroadcast(ctx, b, update,
		func(moved int) string {
			return fmt.Sprintf("🚪 Đã lệnh %d event user (Thượng Cổ) rời cứ điểm", moved)
		},
		func(p *EventPlayer) error { return p.BallGVGLeaveRoom() })
}

// ballGVGInfoBroadcast runs fetch for every running event user of the leader
// that has Thượng Cổ announced, and sends the collected sections (chunked
// under Telegram's 4096 limit). emptyMsg goes out when nobody qualifies.
func (t *TeleBot) ballGVGInfoBroadcast(ctx context.Context, b *bot.Bot, update *models.Update, emptyMsg string, fetch func(*EventPlayer) string) {
	message := update.Message

	if !t.isLeader(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "leader only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	members, err := model.GetEventUsersByLeader(t.ydb.GetDB(), fmt.Sprintf("%d", message.Chat.ID))
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "failed to load event users: " + err.Error(),
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	var sections []string
	for _, u := range members {
		realUID, err := strconv.ParseInt(u.ID, 10, 64)
		if err != nil {
			continue
		}
		v, ok := t.session.Load(realUID)
		if !ok {
			continue
		}
		p, ok2 := v.(*EventPlayer)
		if !ok2 || !p.Running || !p.BallGVGEventOpen() {
			continue
		}
		sections = append(sections, "👤 "+u.Username+"\n"+fetch(p))
	}

	if len(sections) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            emptyMsg,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}

	chunk := ""
	for _, s := range sections {
		if len(chunk)+len(s) > 3600 {
			b.SendMessage(ctx, &bot.SendMessageParams{ChatID: message.Chat.ID, Text: chunk})
			chunk = ""
		}
		if chunk != "" {
			chunk += "\n\n"
		}
		chunk += s
	}
	if chunk != "" {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            chunk,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
	}
}

// DitichRoomsHandler handles /ditich_rooms: cached room table (ids,
// names, owners, rally mark) per account with the event open.
func (t *TeleBot) DitichRoomsHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.ballGVGInfoBroadcast(ctx, b, update,
		"không có event user nào đang mở Thượng Cổ Di Tích",
		func(p *EventPlayer) string { return p.BallGVGRooms() })
}

// DitichRosterHandler handles /ditich_roster: occupants of each account's
// current room (situational awareness — fighting itself is automatic).
func (t *TeleBot) DitichRosterHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	t.ballGVGInfoBroadcast(ctx, b, update,
		"không có event user nào đang mở Thượng Cổ Di Tích",
		func(p *EventPlayer) string { return p.BallGVGRoster() })
}
