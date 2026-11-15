package tytd_go

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"tytd_go/internal/bigchess"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func TestBCEngineStateAdapter(t *testing.T) {
	st := bigchess.New(999901)
	st.SetSelf(bigchess.Piece{ChessNo: 1, ChessID: bigchess.IDPlayer, X: 3, Y: 4})
	st.SetBoard([]bigchess.Piece{
		{ChessNo: 11, ChessID: bigchess.IDPawn, X: 0, Y: 2},
		{ChessNo: 12, ChessID: bigchess.IDGeneral, X: 5, Y: 5},
	})
	// Live server: placeholder (0,0) preview coords.
	one := int32(1)
	st.SetPreview(&one, []bigchess.Piece{
		{ChessNo: 21, ChessID: bigchess.IDPawn, X: 0, Y: 0},
		{ChessNo: 22, ChessID: bigchess.IDKnight, X: 0, Y: 0},
	})
	st.PowerTimes = 2
	st.PowerRemoveTypeCount = 1
	st.PowerRemoveRandomCount = 0
	st.Score = 1234567890123
	st.Params = map[string]string{
		"BIGCHESS_CLEARALL_NUM": "3", // similar limit → 2 remaining
		"BIGCHESS_CLEAR_NUMMAX": "0", // random limit → 0 (never negative)
		"BIGCHESS_WIN_ROUND":    "500",
	}

	adapted := bcEngineState(*st)

	nj, _ := json.Marshal(adapted)
	got := map[string]any{}
	if err := json.Unmarshal(nj, &got); err != nil {
		t.Fatalf("adapted state must marshal: %v", err)
	}
	if nc, _ := json.Marshal(got["nextChess"]); string(nc) != "[]" {
		t.Fatalf("placeholder preview must mask to []: %s", nc)
	}
	if got["nextTurn"] != float64(1) {
		t.Fatalf("nextTurn must survive: %v", got["nextTurn"])
	}
	res := got["resources"].(map[string]any)
	// pipe/similar/random stay 0 even with BIGCHESS_* limit params present:
	// live ret=101 proved those powers are gated by owned items we cannot see.
	if res["cross"] != float64(2) || res["similar"] != float64(0) || res["random"] != float64(0) || res["pipe"] != float64(0) {
		t.Fatalf("resources (only cross is knowable): %v", res)
	}
	if got["score"] != float64(1234567890123) {
		t.Fatalf("int64 score lost: %v", got["score"])
	}
	if got["winRound"] != float64(500) {
		t.Fatalf("winRound from params: %v", got["winRound"])
	}
	if ps, _ := got["pieceScores"].(map[string]any); ps == nil {
		t.Fatal("pieceScores must ride along")
	}

	// A real-coordinate preview must pass through untouched (server change
	// would mean previews are trustworthy again).
	st.SetPreview(&one, []bigchess.Piece{{ChessNo: 21, ChessID: bigchess.IDPawn, X: 4, Y: 4}})
	out := bcEngineState(*st)["nextChess"].([]bigchess.Piece)
	if len(out) != 1 || out[0].X != 4 {
		t.Fatalf("real preview coords must pass through: %+v", out)
	}
}

func TestMCPBigChessRejectionLoopProtection(t *testing.T) {
	var mu sync.Mutex
	recs := map[string][]map[string]any{}
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		recs[r.URL.Path] = append(recs[r.URL.Path], body)
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{}})
	}))
	defer engine.Close()

	b := newTestWebBot(t)
	db := b.ydb.GetDB()
	model.SetConfig(db, model.ConfigKeyMCPEnabled, "true")
	model.SetConfig(db, model.ConfigKeyMCPAuthKey, "rejkey12345")
	model.SetConfig(db, model.ConfigKeyBCEngineEnabled, "true")
	model.SetConfig(db, model.ConfigKeyBCEngineURL, engine.URL)
	h := b.mcpRoutes()

	p := &Player{Username: "bcrej", Running: true, ChatID: 5, UID: 55, writeChan: make(chan []byte, 16)}
	now := time.Now().UnixMilli()
	setActivities(&p.ActList, activityEntry{ActivityID: 999901, Type: activityTypeBigChess,
		BeginTime: now - 1000, EndTime: now + 10_000_000_000})
	st := bigchess.New(999901)
	st.SetSelf(bigchess.Piece{ChessNo: 1, ChessID: bigchess.IDPlayer, X: 3, Y: 4})
	st.SetBoard([]bigchess.Piece{{ChessNo: 11, ChessID: bigchess.IDPawn, X: 0, Y: 0}})
	p.BcMu.Lock()
	p.bcState = st
	p.BcMu.Unlock()
	b.session.Store(int64(5), p)

	// enter_game success must clear the quarantine (new run, items may differ).
	done := make(chan bool, 1)
	go func() {
		_, ie := mcpTool(t, h, "rejkey12345", "bigchess_enter_game", map[string]any{"account_id": "5"})
		done <- !ie
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_ENTER_GAME, marshalEnterGame())
	if !<-done {
		t.Fatal("enter_game should succeed")
	}
	waitPath(t, &mu, recs, "/v1/rejections/clear")

	// A game-refused move (ret!=0) must be reported to /v1/rejections.
	refused, _ := pb.Marshal(&proto.ChineseChessMoveResp{Ret: 101})
	go func() {
		_, _ = mcpTool(t, h, "rejkey12345", "bigchess_move", map[string]any{"account_id": "5", "x": 3, "y": 2})
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_MOVE, refused)

	waitPath(t, &mu, recs, "/v1/rejections")
	mu.Lock()
	rej := recs["/v1/rejections"][0]
	mu.Unlock()
	res := rej["result"].(map[string]any)
	if res["ok"] != false || res["ret"] != float64(101) {
		t.Fatalf("rejection result: %v", res)
	}
	if rej["sessionId"] != "tytd:5" {
		t.Fatalf("sessionId: %v", rej["sessionId"])
	}
	if act := rej["action"].(map[string]any); act["command"] != "S_BIG_CHESS_MOVE" {
		t.Fatalf("action: %v", act)
	}
	// The position sent for quarantine must be the PRE-move state.
	self := rej["state"].(map[string]any)["self"].(map[string]any)
	if self["y"] != float64(4) {
		t.Fatalf("quarantine state must be pre-move: %v", self)
	}
}

// waitPath polls until the fake engine has recorded a call to path.
func waitPath(t *testing.T, mu *sync.Mutex, recs map[string][]map[string]any, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(recs[path]) > 0
		mu.Unlock()
		if got {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("engine never received %s", path)
}

func TestMCPBigChessAnalyzeDisabledRefuses(t *testing.T) {
	b := newTestWebBot(t)
	db := b.ydb.GetDB()
	model.SetConfig(db, model.ConfigKeyMCPEnabled, "true")
	model.SetConfig(db, model.ConfigKeyMCPAuthKey, "ankey12345678")
	h := b.mcpRoutes()
	if text, isErr := mcpTool(t, h, "ankey12345678", "bigchess_analyze", map[string]any{"account_id": "5"}); !isErr || !strings.Contains(text, "disabled") {
		t.Fatalf("analyze with engine off: isErr=%v %q", isErr, text)
	}
}

func TestMCPBigChessAnalyzeAndTransitionFeed(t *testing.T) {
	var mu sync.Mutex
	type rec struct {
		path string
		body map[string]any
	}
	var recs []rec
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		recs = append(recs, rec{path: r.URL.Path, body: body})
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/v1/analyze":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{
				"recommendation": map[string]any{"x": 6, "y": 4, "powerOn": false, "powerCross": false, "powerRemoveType": false},
				"lines":          []any{},
				"limitations":    []any{"hidden"},
			}})
		case "/v1/transitions":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"transitionId": "t-1"}})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	}))
	defer engine.Close()

	b := newTestWebBot(t)
	db := b.ydb.GetDB()
	model.SetConfig(db, model.ConfigKeyMCPEnabled, "true")
	model.SetConfig(db, model.ConfigKeyMCPAuthKey, "enkey12345678")
	model.SetConfig(db, model.ConfigKeyBCEngineEnabled, "true")
	model.SetConfig(db, model.ConfigKeyBCEngineURL, engine.URL)
	h := b.mcpRoutes()

	p := &Player{Username: "bceng", Running: true, ChatID: 5, UID: 55, writeChan: make(chan []byte, 16)}
	now := time.Now().UnixMilli()
	setActivities(&p.ActList, activityEntry{ActivityID: 999901, Type: activityTypeBigChess,
		BeginTime: now - 1000, EndTime: now + 10_000_000_000})
	one := int32(1)
	st := bigchess.New(999901)
	st.SetSelf(bigchess.Piece{ChessNo: 1, ChessID: bigchess.IDPlayer, X: 3, Y: 4})
	st.SetBoard([]bigchess.Piece{{ChessNo: 11, ChessID: bigchess.IDPawn, X: 3, Y: 2}})
	st.SetPreview(&one, []bigchess.Piece{
		{ChessNo: 21, ChessID: bigchess.IDKnight, X: 0, Y: 0},
		{ChessNo: 22, ChessID: bigchess.IDPawn, X: 0, Y: 0},
	})
	p.BcMu.Lock()
	p.bcState = st
	p.BcMu.Unlock()
	b.session.Store(int64(5), p)

	// --- analyze passthrough + masking on the wire
	text, isErr := mcpTool(t, h, "enkey12345678", "bigchess_analyze",
		map[string]any{"account_id": "5", "depth": 4, "movetimeMs": 250})
	if isErr || !strings.Contains(text, `"recommendation"`) || !strings.Contains(text, `"stateVersion"`) {
		t.Fatalf("analyze: isErr=%v %q", isErr, text)
	}
	mu.Lock()
	if len(recs) != 1 || recs[0].path != "/v1/analyze" {
		mu.Unlock()
		t.Fatalf("engine recs: %+v", recs)
	}
	sent := recs[0].body
	mu.Unlock()
	if opt := sent["options"].(map[string]any); opt["depth"] != float64(4) {
		t.Fatalf("options not forwarded: %v", sent["options"])
	}
	state := sent["state"].(map[string]any)
	if nc, _ := json.Marshal(state["nextChess"]); string(nc) != "[]" {
		t.Fatalf("analyze must mask placeholder previews: %s", nc)
	}
	if self := state["self"].(map[string]any); self["x"] != float64(3) {
		t.Fatalf("self not adapted: %v", self)
	}

	// --- a successful move feeds /v1/transitions
	resp, _ := pb.Marshal(&proto.ChineseChessMoveResp{
		Ret:       0,
		Chess:     []*proto.ChessData{{ChessNo: pb.Int32(12), ChessId: pb.Int32(1001), X: pb.Int32(1), Y: pb.Int32(1)}},
		SelfChess: &proto.ChessData{ChessNo: pb.Int32(1), ChessId: pb.Int32(1008), X: pb.Int32(3), Y: pb.Int32(2)},
		Score:     pb.Int64(10),
		Step:      pb.Int32(5),
	})
	done := make(chan string, 1)
	go func() {
		tx, ie := mcpTool(t, h, "enkey12345678", "bigchess_move", map[string]any{"account_id": "5", "x": 3, "y": 2})
		if ie {
			done <- "isErr: " + tx
			return
		}
		done <- tx
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_MOVE, resp)
	if got := <-done; !strings.Contains(got, `"step": 5`) {
		t.Fatalf("move via MCP: %q", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		var feed *rec
		for i := range recs {
			if recs[i].path == "/v1/transitions" {
				feed = &recs[i]
			}
		}
		mu.Unlock()
		if feed != nil {
			b := feed.body
			if b["sessionId"] != "tytd:5" {
				t.Fatalf("sessionId: %v", b["sessionId"])
			}
			if res := b["result"].(map[string]any); res["ok"] != true || res["ret"] != float64(0) {
				t.Fatalf("result: %v", res)
			}
			act := b["action"].(map[string]any)
			if act["command"] != "S_BIG_CHESS_MOVE" {
				t.Fatalf("action command: %v", act["command"])
			}
			params := act["params"].(map[string]any)
			if params["x"] != float64(3) || params["y"] != float64(2) {
				t.Fatalf("action params: %v", params)
			}
			before := b["stateBefore"].(map[string]any)
			after := b["stateAfter"].(map[string]any)
			bs := before["self"].(map[string]any)
			as := after["self"].(map[string]any)
			if bs["y"] != float64(4) || as["y"] != float64(2) {
				t.Fatalf("before/after not ordered around the action: %v → %v", bs, as)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("engine never received /v1/transitions")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
