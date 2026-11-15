package tytd_go

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/bigchess"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// mcpCall posts one JSON-RPC message to the MCP handler and returns the
// decoded response (nil for 202/4xx bodies that aren't JSON-RPC).
func mcpCall(t *testing.T, h http.Handler, key, method string, params map[string]any) (map[string]any, int) {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	if key != "" {
		req.Header.Set("X-MCP-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return out, rec.Code
}

// mcpTool calls a tool and unpacks the standard tools/call envelope:
// (first content text, isError).
func mcpTool(t *testing.T, h http.Handler, key, name string, args map[string]any) (string, bool) {
	t.Helper()
	resp, code := mcpCall(t, h, key, "tools/call", map[string]any{"name": name, "arguments": args})
	if code != http.StatusOK {
		t.Fatalf("%s: http %d", name, code)
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no result: %v", name, resp)
	}
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("%s: no content: %v", name, res)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	isErr, _ := res["isError"].(bool)
	return text, isErr
}

func TestMCPEndpointGating(t *testing.T) {
	bot := newTestWebBot(t)
	db := bot.ydb.GetDB()
	model.SetConfig(db, model.ConfigKeyMCPEnabled, "false")
	model.SetConfig(db, model.ConfigKeyMCPAuthKey, "")
	h := bot.mcpRoutes()

	// Off → 503 even with a request that looks legit.
	if _, code := mcpCall(t, h, "whatever", "initialize", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("off: code=%d", code)
	}
	// On but keyless → still 503 (a keyless endpoint must never serve).
	model.SetConfig(db, model.ConfigKeyMCPEnabled, "true")
	if _, code := mcpCall(t, h, "whatever", "initialize", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("keyless: code=%d", code)
	}
	model.SetConfig(db, model.ConfigKeyMCPAuthKey, "s3cr3t-key")

	if _, code := mcpCall(t, h, "", "initialize", nil); code != http.StatusUnauthorized {
		t.Fatalf("missing key: code=%d", code)
	}
	if _, code := mcpCall(t, h, "wrong-key", "initialize", nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong key: code=%d", code)
	}
	// Bearer form is accepted identically (and only on an exact match).
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)))
	req.Header.Set("Authorization", "Bearer s3cr33t-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("near-miss bearer: code=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)))
	req.Header.Set("Authorization", "Bearer s3cr3t-key")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer ok: code=%d", rec.Code)
	}

	// initialize negotiates the client's known version and issues a session id.
	resp, code := mcpCall(t, h, "s3cr3t-key", "initialize", map[string]any{"protocolVersion": "2025-03-26"})
	if code != http.StatusOK {
		t.Fatalf("initialize: code=%d", code)
	}
	if resp["result"].(map[string]any)["protocolVersion"] != "2025-03-26" {
		t.Fatalf("protocol not negotiated: %v", resp)
	}
	if resp["result"].(map[string]any)["serverInfo"].(map[string]any)["name"] != "tytd-bot" {
		t.Fatalf("bad serverInfo: %v", resp)
	}
	// Notification gets 202, no body.
	n := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)))
	n.Header.Set("X-MCP-Key", "s3cr3t-key")
	nrec := httptest.NewRecorder()
	h.ServeHTTP(nrec, n)
	if nrec.Code != http.StatusAccepted || nrec.Body.Len() > 0 {
		t.Fatalf("initialized notification: code=%d body=%q", nrec.Code, nrec.Body.String())
	}
	// Unknown method → JSON-RPC error.
	resp, _ = mcpCall(t, h, "s3cr3t-key", "bogus/method", nil)
	if resp["error"] == nil {
		t.Fatalf("unknown method should error: %v", resp)
	}
	// GET (SSE) is explicitly refused with 405.
	grec := httptest.NewRecorder()
	greq := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	greq.Header.Set("X-MCP-Key", "s3cr3t-key")
	h.ServeHTTP(grec, greq)
	if grec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: code=%d", grec.Code)
	}
}

func TestMCPToolsListHasNoChatID(t *testing.T) {
	bot := newTestWebBot(t)
	db := bot.ydb.GetDB()
	model.SetConfig(db, model.ConfigKeyMCPEnabled, "true")
	model.SetConfig(db, model.ConfigKeyMCPAuthKey, "k12345678")
	h := bot.mcpRoutes()

	resp, _ := mcpCall(t, h, "k12345678", "tools/list", nil)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != len(mcpToolDefs) {
		t.Fatalf("tools/list count=%d", len(tools))
	}
	for _, traw := range tools {
		tool := traw.(map[string]any)
		blob, _ := json.Marshal(tool)
		if strings.Contains(string(blob), "chat_id") {
			t.Fatalf("tool %v still mentions chat_id: %s", tool["name"], blob)
		}
	}
}

func TestMCPBigChessResourcesAndTools(t *testing.T) {
	bot := newTestWebBot(t)
	db := bot.ydb.GetDB()
	model.SetConfig(db, model.ConfigKeyMCPEnabled, "true")
	model.SetConfig(db, model.ConfigKeyMCPAuthKey, "bckey12345678")
	key := "bckey12345678"
	h := bot.mcpRoutes()

	// tools/list now carries the big-chess play tools.
	resp, _ := mcpCall(t, h, key, "tools/list", nil)
	names := map[string]bool{}
	for _, traw := range resp["result"].(map[string]any)["tools"].([]any) {
		names[traw.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"bigchess_state", "bigchess_enter_game", "bigchess_move",
		"bigchess_use_random", "bigchess_revive", "bigchess_end", "bigchess_analyze",
		"bigchess_practice_enter_game", "bigchess_practice_move", "bigchess_practice_end"} {
		if !names[want] {
			t.Fatalf("tool %s missing from tools/list", want)
		}
	}

	// resources/list + resources/read serve the full rules.
	resp, _ = mcpCall(t, h, key, "resources/list", nil)
	res := resp["result"].(map[string]any)["resources"].([]any)
	if len(res) != 2 || res[0].(map[string]any)["uri"] != "bigchess://rules" {
		t.Fatalf("resources/list: %v", resp)
	}
	resp, _ = mcpCall(t, h, key, "resources/read", map[string]any{"uri": "bigchess://rules"})
	text := resp["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "rook") || !strings.Contains(text, "powerCross") {
		t.Fatalf("rules resource looks truncated: %d bytes", len(text))
	}
	resp, _ = mcpCall(t, h, key, "resources/read", map[string]any{"uri": "bigchess://nope"})
	if resp["error"] == nil {
		t.Fatal("unknown resource must be a JSON-RPC error")
	}

	// Offline account refuses every big-chess tool.
	if text, isErr := mcpTool(t, h, key, "bigchess_state", map[string]any{"account_id": "9"}); !isErr || !strings.Contains(text, "not online") {
		t.Fatalf("offline state: isErr=%v %q", isErr, text)
	}

	// A running *Player with state answers through the tool layer.
	p := &Player{Username: "bcu", Running: true, ChatID: 5, UID: 55, writeChan: make(chan []byte, 16)}
	wireCtx(t, p)
	now := time.Now().UnixMilli()
	setActivities(&p.ActList, activityEntry{ActivityID: 999901, Type: activityTypeBigChess,
		BeginTime: now - 1000, EndTime: now + 10_000_000_000})
	st := bigchess.New(999901)
	st.SetSelf(bigchess.Piece{ChessNo: 1, ChessID: bigchess.IDPlayer, X: 3, Y: 4})
	p.BcMu.Lock()
	p.bcState = st
	p.BcMu.Unlock()
	bot.session.Store(int64(5), p)
	text, isErr := mcpTool(t, h, key, "bigchess_state", map[string]any{"account_id": "5"})
	if isErr || !strings.Contains(text, `"stateVersion"`) || strings.Contains(text, `"pw"`) {
		t.Fatalf("state tool: isErr=%v %q", isErr, text)
	}

	// Full enter_game through MCP: tool call drives the transport, the
	// test plays the game server by answering the claimed seat.
	respEnter := marshalEnterGame()
	done := make(chan string, 1)
	go func() {
		text, isErr := mcpTool(t, h, key, "bigchess_enter_game", map[string]any{"account_id": "5"})
		if isErr {
			done <- "isErr: " + text
			return
		}
		done <- text
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_ENTER_GAME, respEnter)
	got := <-done
	if !strings.Contains(got, `"score": 777`) || !strings.Contains(got, `"stateVersion"`) {
		t.Fatalf("enter_game via MCP: %q", got)
	}
	// The request frame must carry the full-ID command hex.
	drain := func() []string {
		var out []string
		for {
			select {
			case f := <-p.writeChan:
				out = append(out, hex.EncodeToString(f[6:10]))
			default:
				return out
			}
		}
	}
	if frames := drain(); len(frames) == 0 || frames[len(frames)-1] != BIG_CHESS_ENTER_GAME_CMD {
		t.Fatalf("frames=%v", frames)
	}

	// Account chooser: list_running_users exposes the live 341 window so
	// an AI can pick a playable online account without trial and error.
	text, isErr = mcpTool(t, h, key, "list_running_users", map[string]any{})
	if isErr || !strings.Contains(text, `"liveActivities"`) || !strings.Contains(text, `"type": 341`) {
		t.Fatalf("chooser missing activity info: isErr=%v %q", isErr, text)
	}
}

func TestMCPBigChessPracticeRoundTrip(t *testing.T) {
	bot := newTestWebBot(t)
	db := bot.ydb.GetDB()
	model.SetConfig(db, model.ConfigKeyMCPEnabled, "true")
	model.SetConfig(db, model.ConfigKeyMCPAuthKey, "pckey12345678")
	h := bot.mcpRoutes()

	p := &Player{Username: "pcu", Running: true, ChatID: 5, UID: 55, writeChan: make(chan []byte, 16)}
	wireCtx(t, p)
	now := time.Now().UnixMilli()
	setActivities(&p.ActList, activityEntry{ActivityID: 999901, Type: activityTypeBigChess,
		BeginTime: now - 1000, EndTime: now + 10_000_000_000})
	bot.session.Store(int64(5), p)

	// Enter practice: same response layout as ranked enter-game.
	done := make(chan string, 1)
	go func() {
		text, isErr := mcpTool(t, h, "pckey12345678", "bigchess_practice_enter_game", map[string]any{"account_id": "5"})
		done <- (func() string { _ = isErr; return text })()
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_PRACTICE_ENTER_GAME, marshalEnterGame())
	if got := <-done; !strings.Contains(got, `"score": 777`) {
		t.Fatalf("practice enter: %q", got)
	}

	// Practice move: answered with the PRACTICE move message (newPlayer at
	// field 11 — a different layout than ranked) and must ride the practice
	// command hex.
	resp, _ := pb.Marshal(&proto.ChineseChessPracticeMoveResp{
		Ret:       0,
		SelfChess: &proto.ChessData{ChessNo: pb.Int32(1), ChessId: pb.Int32(1008), X: pb.Int32(3), Y: pb.Int32(6)},
		Score:     pb.Int64(8),
		Step:      pb.Int32(1),
	})
	done2 := make(chan string, 1)
	go func() {
		text, _ := mcpTool(t, h, "pckey12345678", "bigchess_practice_move", map[string]any{"account_id": "5", "x": 3, "y": 6})
		done2 <- text
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_PRACTICE_MOVE, resp)
	if got := <-done2; !strings.Contains(got, `"step": 1`) || strings.Contains(got, `"pw"`) {
		t.Fatalf("practice move: %q", got)
	}
	var last string
	for {
		select {
		case f := <-p.writeChan:
			last = hex.EncodeToString(f[6:10])
		default:
			goto checked
		}
	}
checked:
	if last != BIG_CHESS_PRACTICE_MOVE_CMD {
		t.Fatalf("practice move frame = %s, want %s", last, BIG_CHESS_PRACTICE_MOVE_CMD)
	}

	// Practice end settles freely (score field 3).
	endResp, _ := pb.Marshal(&proto.ChineseChessPracticeEndResp{Ret: 0, Score: pb.Int64(8), NowRank: pb.Int32(9)})
	done3 := make(chan string, 1)
	go func() {
		text, _ := mcpTool(t, h, "pckey12345678", "bigchess_practice_end", map[string]any{"account_id": "5"})
		done3 <- text
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_PRACTICE_END, endResp)
	if got := <-done3; !strings.Contains(got, `"settled": true`) || !strings.Contains(got, `"score": 8`) {
		t.Fatalf("practice end: %q", got)
	}
}

func marshalEnterGame() []byte {
	b, err := pb.Marshal(&proto.ChineseChessEnterGameResp{
		Ret:           0,
		ChessDataList: []*proto.ChessData{{ChessNo: pb.Int32(11), ChessId: pb.Int32(1001), X: pb.Int32(0), Y: pb.Int32(0)}},
		SelfChessData: &proto.ChessData{ChessNo: pb.Int32(1), ChessId: pb.Int32(1008), X: pb.Int32(3), Y: pb.Int32(4)},
		Score:         pb.Int64(777),
		Round:         pb.Int32(1),
	})
	if err != nil {
		panic(err)
	}
	return b
}

func TestMCPAccountTools(t *testing.T) {
	bot := newTestWebBot(t)
	db := bot.ydb.GetDB()
	model.SetConfig(db, model.ConfigKeyMCPEnabled, "true")
	model.SetConfig(db, model.ConfigKeyMCPAuthKey, "testkey12345678")
	key := "testkey12345678"

	const memberID = "9000000001"
	leader := seedUser(t, bot, "2", 30)
	leader.IsLeader = true
	model.CreateUser(db, leader)
	member := seedUser(t, bot, memberID, 30)
	member.LeaderID = "2"
	member.IngameName = "MemberGuy"
	model.CreateUser(db, member)
	seedUser(t, bot, "3", 30) // plain user with a live session
	seedUser(t, bot, "4", 30) // plain user, offline (no session)

	mp := &Player{Username: "memb", Password: "pw", Name: "MemberGuy", Running: true, ChatID: 2, ServerID: 100, ChuotRun: true}
	bot.session.Store(int64(9000000001), mp)
	up := &Player{Username: "u3", Password: "pw", Name: "UserGuy", Running: true, ChatID: 3, ServerID: 100}
	bot.session.Store(int64(3), up)
	evu := &model.EventUser{ID: "8000000001", Username: "ev", Password: "pw", ServerID: 5, LeaderID: "2"}
	model.CreateEventUser(db, evu)
	bot.session.Store(int64(8000000001), &EventPlayer{Username: "ev", Name: "EvGuy", Running: true, ChatID: 2, ServerID: 5})

	h := bot.mcpRoutes()

	// list: all three running accounts, nothing leaks the seeded password.
	text, isErr := mcpTool(t, h, key, "list_running_users", map[string]any{})
	if isErr {
		t.Fatalf("list: %q", text)
	}
	if strings.Contains(text, `"pw"`) {
		t.Fatalf("password leaked in list output")
	}
	if strings.Contains(text, "chat_id") {
		t.Fatalf("chat id leaked in list output: %s", text)
	}
	var seen []mcpAccount
	if err := json.Unmarshal([]byte(text), &seen); err != nil {
		t.Fatalf("list unmarshal: %v (%q)", err, text)
	}
	if len(seen) != 3 {
		t.Fatalf("saw %d accounts, want 3: %q", len(seen), text)
	}
	if seen[0].AccountID != "3" || seen[1].AccountID != "8000000001" || seen[2].AccountID != memberID {
		t.Fatalf("bad sort/ids: %q", text)
	}

	// get_user_info: live session, then offline DB row (running=false), then miss.
	text, isErr = mcpTool(t, h, key, "get_user_info", map[string]any{"account_id": memberID})
	if isErr || !strings.Contains(text, `"running": true`) || strings.Contains(text, "pw") {
		t.Fatalf("get member: isErr=%v %q", isErr, text)
	}
	text, isErr = mcpTool(t, h, key, "get_user_info", map[string]any{"account_id": "4"})
	if isErr || !strings.Contains(text, `"running": false`) {
		t.Fatalf("get offline: isErr=%v %q", isErr, text)
	}
	if _, isErr := mcpTool(t, h, key, "get_user_info", map[string]any{"account_id": "404404"}); !isErr {
		t.Fatalf("missing account not refused")
	}
	if _, isErr := mcpTool(t, h, key, "get_user_info", map[string]any{}); !isErr {
		t.Fatalf("missing account_id not refused")
	}

	// Toggles reach the live session; offline and event sessions refuse.
	if text, isErr := mcpTool(t, h, key, "account_set_toggle", map[string]any{"account_id": "3", "feature": "notify", "enabled": true}); isErr {
		t.Fatalf("toggle: %q", text)
	}
	if !up.Notify {
		t.Fatalf("toggle did not reach the live session")
	}
	if text, isErr := mcpTool(t, h, key, "account_set_toggle", map[string]any{"account_id": memberID, "feature": "chuot", "enabled": false}); isErr {
		t.Fatalf("toggle member: %q", text)
	}
	if mp.ChuotRun {
		t.Fatalf("toggle did not clear member chuot")
	}
	if text, isErr := mcpTool(t, h, key, "account_set_toggle", map[string]any{"account_id": "4", "feature": "chuot", "enabled": true}); !isErr || !strings.Contains(text, "offline") {
		t.Fatalf("toggle offline accepted: isErr=%v %q", isErr, text)
	}
	if text, isErr := mcpTool(t, h, key, "account_set_toggle", map[string]any{"account_id": "8000000001", "feature": "chuot", "enabled": true}); !isErr || !strings.Contains(text, "event") {
		t.Fatalf("toggle event: isErr=%v %q", isErr, text)
	}
	if _, isErr := mcpTool(t, h, key, "account_set_toggle", map[string]any{"account_id": "3", "feature": "nope", "enabled": true}); !isErr {
		t.Fatalf("unknown feature accepted")
	}

	// account_notify routes to the session's notification chat (own chat for
	// the plain user, the leader's chat for the member) and needs an online
	// account.
	drain := func() []NotifyMGS {
		var got []NotifyMGS
		for {
			select {
			case n := <-bot.notiChan:
				got = append(got, n)
			default:
				return got
			}
		}
	}
	if text, isErr := mcpTool(t, h, key, "account_notify", map[string]any{"account_id": "3", "text": "hi self"}); isErr {
		t.Fatalf("notify user: %q", text)
	}
	if text, isErr := mcpTool(t, h, key, "account_notify", map[string]any{"account_id": memberID, "text": "hello member"}); isErr {
		t.Fatalf("notify member: %q", text)
	}
	if _, isErr := mcpTool(t, h, key, "account_notify", map[string]any{"account_id": "4", "text": "nobody"}); !isErr {
		t.Fatalf("notify offline accepted")
	}
	notes := drain()
	if len(notes) != 2 || notes[0].ChatID != 3 || notes[1].ChatID != 2 || notes[1].MSG != "hello member" {
		t.Fatalf("notified=%+v", notes)
	}
}

// skytrade_status: offline refuses; a live account reports its REAL DB
// toggles, the stored type-150 rows and the announce-only verdict — and the
// whole tool is read-only (the live Player here has no socket at all: any
// hidden write would block/panic, and the snapshot path is only entered
// when an activity is ACTIVE).
func TestMCPSkyTradeStatus(t *testing.T) {
	bot := newTestWebBot(t)
	db := bot.ydb.GetDB()
	model.SetConfig(db, model.ConfigKeyMCPEnabled, "true")
	model.SetConfig(db, model.ConfigKeyMCPAuthKey, "stkey12345678")
	h := bot.mcpRoutes()
	const key = "stkey12345678"

	if _, isErr := mcpTool(t, h, key, "skytrade_status", map[string]any{"account_id": "404404"}); !isErr {
		t.Fatal("offline account must be refused")
	}

	u := seedUser(t, bot, "777", 30)
	u.GameUID = 77700
	u.AutoSkyTrade = true
	model.CreateUser(db, u)
	p := &Player{Username: "st", Password: "pw", Running: true, ChatID: 777, UID: 77700, DB: db}
	p.seedAutoGates(u) // the loop/tool truth lives on the Player (owner 2026-10-27)
	bot.session.Store(int64(777), p)
	now := time.Now()
	setActivities(&p.ActList, activityEntry{
		ActivityID: 9063118, Type: skyTradeActivityType,
		BeginShowTime: now.Add(-2 * time.Hour).UnixMilli(),
		BeginTime:     now.Add(time.Hour).UnixMilli(),
		EndTime:       now.Add(3 * time.Hour).UnixMilli(),
		EndShowTime:   now.Add(4 * time.Hour).UnixMilli(),
	})

	text, isErr := mcpTool(t, h, key, "skytrade_status", map[string]any{"account_id": "777"})
	if isErr {
		t.Fatalf("skytrade_status: %q", text)
	}
	for _, want := range []string{`"auto_sky_trade": true`, `"auto_sky_trade_speed": false`,
		`"phase": "show"`, `"active": false`, `"nextOpeningIct"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %q", want, text)
		}
	}
}
