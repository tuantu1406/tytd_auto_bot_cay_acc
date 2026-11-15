package tytd_go

// Bridge to the external Big Chess play engine
// (/workspace/remoteBigChess/engine_api, stdlib HTTP service, no game
// connectivity of its own). The bot remains the only thing that touches the
// game server: this file (a) adapts our authoritative bigchess.State to the
// engine's documented request shape, (b) proxies /v1/analyze for the
// bigchess_analyze MCP tool, and (c) feeds /v1/transitions after successful
// actions so the engine's replay/risk model learns from real games.
// The engine's advice never auto-executes: the AI still picks and sends
// exactly one action through the existing bigchess_* tools.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/bigchess"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const (
	bcEngineDefaultURL = "http://127.0.0.1:8780"
	bcEngineAnalyzeTO  = 35 * time.Second
	bcEngineFeedTO     = 8 * time.Second
)

type bcEngine struct {
	enabled bool
	url     string
	token   string
}

// bcEngineCfg is read fresh per request so /bcengine changes apply instantly.
func (t *TeleBot) bcEngineCfg() bcEngine {
	db := t.ydb.GetDB()
	e := bcEngine{url: bcEngineDefaultURL}
	if c, err := model.GetConfig(db, model.ConfigKeyBCEngineEnabled); err == nil {
		e.enabled = c.Value == "true"
	}
	if c, err := model.GetConfig(db, model.ConfigKeyBCEngineURL); err == nil {
		if u := strings.TrimSuffix(strings.TrimSpace(c.Value), "/"); u != "" {
			e.url = u
		}
	}
	if c, err := model.GetConfig(db, model.ConfigKeyBCEngineToken); err == nil {
		e.token = strings.TrimSpace(c.Value)
	}
	return e
}

// bcPreviewPlaceholder reports the live-verified server behaviour that spawn
// previews carry only placeholder coordinates: every nextChess entry sits on
// (0,0) while the real cells stay hidden until the wave resolves.
func bcPreviewPlaceholder(list []bigchess.Piece) bool {
	if len(list) == 0 {
		return false
	}
	for _, pc := range list {
		if pc.X != 0 || pc.Y != 0 {
			return false
		}
	}
	return true
}

// bcEngineState adapts an authoritative snapshot to the engine API state
// shape. Two live-verified corrections the engine cannot know on its own:
//
//   - Counter polarity & true ownership. Every *Count in our state is
//     CONSUMED; the engine's resources block means REMAINING. Only cross is
//     knowable (powerTimes is an in-run remaining-charge count). Live test
//     2026-10-01: a similar move derived from BIGCHESS_* limit params was
//     refused with ret=101 — these powers are gated by owned inventory
//     items that never appear in our state, so pipe/similar/random are
//     reported as 0. The engine therefore never proposes them; the AI may
//     still spend them via bigchess_move flags at its own risk (the server
//     refusal is the authority).
//   - Hidden spawn cells. The engine's forward search places preview waves
//     at their coordinates; against the live server that would teleport a
//     pawn onto (0,0) and count the rest of the wave as collisions,
//     poisoning every deeper ply. An all-placeholder preview is therefore
//     masked to composition-only (empty list); the AI applies the
//     bigchess://rules "Thinking ahead" wave handling for the real threat.
func bcEngineState(st bigchess.State) map[string]any {
	next := st.NextChess
	if bcPreviewPlaceholder(next) {
		next = nil
	}
	if next == nil {
		next = []bigchess.Piece{}
	}
	bcInt := func(key string) int {
		if v, err := strconv.Atoi(strings.TrimSpace(st.Params[key])); err == nil {
			return v
		}
		return 0
	}
	resources := map[string]int{
		"pipe": 0, "cross": int(st.PowerTimes), "similar": 0, "random": 0,
		"revive": 0, "ingotRevive": 0, "passRevive": 0,
	}
	winRound := bcInt("BIGCHESS_WIN_ROUND")
	if winRound == 0 {
		winRound = 300
	}
	return map[string]any{
		"activityId":  st.ActivityID,
		"width":       st.Width,
		"height":      st.Height,
		"winRound":    winRound,
		"board":       st.Board,
		"self":        st.Self,
		"score":       st.Score,
		"step":        st.Step,
		"gameStatus":  st.GameStatus,
		"powerTimes":  st.PowerTimes,
		"nextTurn":    st.NextTurn,
		"nextChess":   next,
		"pieceScores": st.PieceScores,
		"resources":   resources,
	}
}

// post sends one engine API request and returns the envelope's data.
func (e bcEngine) post(ctx context.Context, path string, body any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	if e.token != "" {
		req.Header.Set("authorization", "Bearer "+e.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var env struct {
		OK    bool           `json:"ok"`
		Data  map[string]any `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("bad engine response (http %d): %s", resp.StatusCode, truncateForLog(string(data), 200))
	}
	if !env.OK {
		code, msg := "ENGINE_ERROR", strings.TrimSpace(string(data))
		if env.Error != nil {
			code, msg = env.Error.Code, env.Error.Message
		}
		return nil, fmt.Errorf("engine %s (http %d): %s", code, resp.StatusCode, msg)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("engine returned ok=false-shaped %d", resp.StatusCode)
	}
	return env.Data, nil
}

// bcEngineAnalyze proxies /v1/analyze for the MCP tool. options may be nil
// (engine defaults: depth 3, 100k nodes, 1s, MultiPV 5).
func (e bcEngine) analyze(ctx context.Context, st bigchess.State, options map[string]any) (map[string]any, error) {
	body := map[string]any{"state": bcEngineState(st)}
	if len(options) > 0 {
		body["options"] = options
	}
	return e.post(ctx, "/v1/analyze", body)
}

// bcEngineBefore snapshots the engine config + pre-action state in one step;
// haveBefore=false means "skip the transition feed" (engine off, or no run
// state to learn from).
func (t *TeleBot) bcEngineBefore(p *Player) (bcEngine, bigchess.State, bool) {
	e := t.bcEngineCfg()
	if !e.enabled {
		return e, bigchess.State{}, false
	}
	v, ok := p.BigChessState()
	if !ok {
		return e, bigchess.State{}, false
	}
	st, isSt := v["state"].(bigchess.State)
	return e, st, isSt
}

// bcFeedOK reports view's post-action state against the before snapshot when
// the action succeeded and a full before/after pair exists.
func (t *TeleBot) bcFeedOK(e bcEngine, accountID string, before bigchess.State, haveBefore bool, view map[string]any, action map[string]any) {
	if !e.enabled || !haveBefore {
		return
	}
	after, isSt := view["state"].(bigchess.State)
	if !isSt {
		return
	}
	t.bcFeedTransition(e, accountID, action, before, after)
}

// bcFeedReject reports a GAME-REJECTED action (ret!=0) to the engine's loop
// protection: the engine quarantines that advice for that position, so
// re-analyzing the same state yields a different recommendation instead of
// the same refused one. Transport failures ("server did not answer") are NOT
// rejections — no ret means nothing to report.
func (t *TeleBot) bcFeedReject(e bcEngine, accountID string, st bigchess.State, action map[string]any, ret int32) {
	if !e.enabled || ret == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), bcEngineFeedTO)
		defer cancel()
		_, err := e.post(ctx, "/v1/rejections", map[string]any{
			"state":     bcEngineState(st),
			"action":    action,
			"result":    map[string]any{"ok": false, "ret": ret},
			"sessionId": "tytd:" + accountID,
		})
		if err != nil {
			fmt.Printf("bcengine: rejection report for %s failed: %v\n", accountID, err)
		}
	}()
}

// bcClearRejections lifts the quarantine when a fresh run begins — e.g. the
// account bought the missing power item and the refused paid move may now be
// legal (inventoryVerified stays our secret; clear is the explicit signal).
func (t *TeleBot) bcClearRejections(e bcEngine, accountID string) {
	if !e.enabled {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), bcEngineFeedTO)
		defer cancel()
		_, err := e.post(ctx, "/v1/rejections/clear", map[string]any{"sessionId": "tytd:" + accountID})
		if err != nil {
			fmt.Printf("bcengine: rejection clear for %s failed: %v\n", accountID, err)
		}
	}()
}

// bcFeedTransition reports one successful authoritative transition to the
// engine asynchronously; failures are intentionally silent (telemetry must
// never block or fail gameplay). before/after are defensive snapshots from
// BigChessState's views.
func (t *TeleBot) bcFeedTransition(e bcEngine, accountID string, action map[string]any, before, after bigchess.State) {
	if !e.enabled {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), bcEngineFeedTO)
		defer cancel()
		_, err := e.post(ctx, "/v1/transitions", map[string]any{
			"stateBefore": bcEngineState(before),
			"action":      action,
			"result":      map[string]any{"ok": true, "ret": 0},
			"stateAfter":  bcEngineState(after),
			"sessionId":   "tytd:" + accountID,
		})
		if err != nil {
			fmt.Printf("bcengine: transition feed for %s failed: %v\n", accountID, err)
		}
	}()
}

// truncateForLog keeps engine error bodies out of the log uncapped.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// health GETs /health (unauthenticated on the engine by design).
func (e bcEngine) health(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.url+"/health", nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode == http.StatusOK, nil
}

// ---- /bcengine admin command ---------------------------------------------

// BCEngineConfigHandler administers the external Big Chess play engine
// endpoint used by bigchess_analyze: on/off, url, token, status, test.
func (t *TeleBot) BCEngineConfigHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	send := func(text string) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID, Text: text, ParseMode: models.ParseModeHTML,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
	}
	args := strings.Fields(message.Text)
	db := t.ydb.GetDB()
	usage := "usage:\n/bcengine on|off\n/bcengine url <http://host:port>\n/bcengine token <token> | /bcengine token clear\n/bcengine status\n/bcengine test"
	if len(args) < 2 {
		send(usage)
		return
	}
	switch strings.ToLower(args[1]) {
	case "on", "off":
		on := strings.EqualFold(args[1], "on")
		if err := model.SetConfig(db, model.ConfigKeyBCEngineEnabled, strconv.FormatBool(on)); err != nil {
			send("failed: " + err.Error())
			return
		}
		state := "TẮT"
		if on {
			state = "BẬT"
		}
		e := t.bcEngineCfg()
		note := ""
		if on {
			hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			ok, herr := e.health(hctx)
			cancel()
			if !ok {
				note = "\n(cảnh báo: engine không trả lời /health"
				if herr != nil {
					note += ": " + herr.Error()
				}
				note += ")"
			}
		}
		send("Big Chess engine " + state + "\nurl: <code>" + e.url + "</code>" + note)
	case "url":
		if len(args) != 3 {
			send(usage)
			return
		}
		u := strings.TrimSuffix(args[2], "/")
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			send("url phải bắt đầu bằng http:// hoặc https://")
			return
		}
		if err := model.SetConfig(db, model.ConfigKeyBCEngineURL, u); err != nil {
			send("failed: " + err.Error())
			return
		}
		send("Big Chess engine url: <code>" + u + "</code>")
	case "token":
		if len(args) != 3 {
			send(usage)
			return
		}
		tok := args[2]
		if strings.EqualFold(tok, "clear") {
			tok = ""
		}
		if err := model.SetConfig(db, model.ConfigKeyBCEngineToken, tok); err != nil {
			send("failed: " + err.Error())
			return
		}
		send("Big Chess engine token đã cập nhật")
	case "status":
		e := t.bcEngineCfg()
		shown := "(không có)"
		if e.token != "" {
			shown = fmt.Sprintf("%s… len=%d", e.token[:min(4, len(e.token))], len(e.token))
		}
		enabled := "TẮT"
		if e.enabled {
			enabled = "BẬT"
		}
		send(fmt.Sprintf("Big Chess engine: %s\nurl: <code>%s</code>\ntoken: %s\n(analyze + transition feed cho tài khoản online)", enabled, e.url, shown))
	case "test":
		e := t.bcEngineCfg()
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if ok, err := e.health(hctx); ok {
			send("engine OK: <code>" + e.url + "</code>")
		} else {
			msg := "engine không trả lời /health"
			if err != nil {
				msg += ": " + err.Error()
			}
			send(msg)
		}
	default:
		send(usage)
	}
}
