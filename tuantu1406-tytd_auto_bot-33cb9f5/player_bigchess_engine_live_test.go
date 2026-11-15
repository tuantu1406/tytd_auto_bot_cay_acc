//go:build live

package tytd_go

// TestLiveBigChessEnginePlay drives a REAL Big Chess run where every move
// comes from the external analyzer server (remoteBigChess/engine_api) — the
// "raw engine plays" scenario. It exercises the full loop a client should
// run: analyze → send recommendation → feed transition / rejection →
// re-analyze.
//
// Live-verified robustness requirement: the game server stops answering ANY
// request after roughly 100-200 rapid protocol moves (seen twice, servers
// 710564 and 710547, socket never closed by the server first). The test
// therefore RECONNECTS on transport stalls and resumes the live run via
// enter_game — the game state is server-side, so a stall costs nothing.
//
// Requires the engine API running (default http://127.0.0.1:8780, override
// TYTD_ENGINE_URL / TYTD_ENGINE_TOKEN). Environment:
//
//	TYTD_LIVE=1                required
//	TYTD_LIVE_SERVER=710564    game server id
//	TYTD_ENGINE_REVIVES=3      per-run revive budget; -1 = full respawn
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveBigChessEnginePlay -v -timeout 3000s .

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/bigchess"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	pb "google.golang.org/protobuf/proto"

	"github.com/gorilla/websocket"
)

func envIntOr(t *testing.T, key string, def int) int {
	t.Helper()
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func engineURL() string {
	if v := os.Getenv("TYTD_ENGINE_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:8780"
}

// enginePost calls one engine endpoint and returns the envelope data.
func enginePost(path string, body map[string]any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, engineURL()+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	if tok := os.Getenv("TYTD_ENGINE_TOKEN"); tok != "" {
		req.Header.Set("authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env struct {
		OK    bool           `json:"ok"`
		Data  map[string]any `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncateForLog(string(data), 200))
	}
	if !env.OK {
		return nil, fmt.Errorf("engine error: %+v", env.Error)
	}
	return env.Data, nil
}

// enginePick turns a recommendation into a concrete move, falling back to the
// safe glide picker when the engine offers nothing (full board / dead end).
func enginePick(t *testing.T, st bigchess.State) (x, y int32, pipe, cross, similar bool, src string) {
	t.Helper()
	data, err := enginePost("/v1/analyze", map[string]any{
		"state":   bcEngineState(st),
		"options": map[string]any{"depth": envIntOr(t, "TYTD_ENGINE_DEPTH", 3), "movetimeMs": 1200, "multiPV": 3},
	})
	if err != nil {
		t.Logf("engine analyze failed (%v) — falling back to local picker", err)
	} else if rec, ok := data["recommendation"].(map[string]any); ok && rec != nil {
		b := func(k string) bool { v, _ := rec[k].(bool); return v }
		f := func(k string) int32 { v, _ := rec[k].(float64); return int32(v) }
		return f("x"), f("y"), b("powerOn"), b("powerCross"), b("powerRemoveType"), "engine"
	}
	if x, y, found := bcPickMove(st); found {
		return x, y, false, false, false, "fallback"
	}
	return 0, 0, false, false, false, "none"
}

// bcStalled reports whether an op error is transport-level (server silent /
// socket gone) rather than a game refusal — only stalls justify reconnect.
func bcStalled(view map[string]any) bool {
	e, _ := view["error"].(string)
	return strings.Contains(e, "did not answer") || strings.Contains(e, "connection dropped")
}

// bcSession is one live websocket + Player wired for big chess.
type bcSession struct {
	conn    *websocket.Conn
	p       *Player
	cleanup func()
}

// bcDialLive performs the full login chain, connects, authenticates, and waits
// for a live type-341 instance. Shared by the ranked and practice playtests.
func bcDialLive(t *testing.T, user, pass string, server int) (*bcSession, error) {
	passportToken, err := request.LoginGPassport(user, pass)
	if err != nil {
		return nil, fmt.Errorf("LoginGPassport: %w", err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		return nil, fmt.Errorf("LoginFuntap: %w", err)
	}
	gameURL, err := request.GetServerURL(userID, server)
	if err != nil {
		return nil, fmt.Errorf("GetServerURL: %w", err)
	}
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
	if err != nil {
		return nil, fmt.Errorf("GetWSKey: %w", err)
	}
	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ws dial: %w", err)
	}
	p := &Player{
		Username:  user,
		UID:       uid,
		ChatID:    7,
		writeChan: make(chan []byte, 128),
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		// Must exit on ctx cancel too: the channel is never closed, and
		// cleanup() waits on writerDone (a bare `range` would deadlock
		// the first reconnect — seen live).
		for {
			select {
			case frame, ok := <-p.writeChan:
				if !ok {
					return
				}
				if err := conn.WriteMessage(2, frame); err != nil {
					return
				}
			case <-p.ctx.Done():
				return
			}
		}
	}()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, msgType, realData := pack.Unpack(message)
			if !isMSG {
				continue
			}
			if os.Getenv("TYTD_LIVE_RAWDUMP") != "" {
				t.Logf("MSG %s len=%d", msgType, len(realData))
			}
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.bcMergeConfig(realData)
			case SERVER_BIG_CHESS_ENTER, SERVER_BIG_CHESS_ENTER_GAME, SERVER_BIG_CHESS_MOVE,
				SERVER_BIG_CHESS_END, SERVER_BIG_CHESS_USE_ITEM, SERVER_BIG_CHESS_USE_RANDOM,
				SERVER_BIG_CHESS_PRACTICE_ENTER_GAME, SERVER_BIG_CHESS_PRACTICE_MOVE,
				SERVER_BIG_CHESS_PRACTICE_END:
				p.bcDeliver(msgType, realData)
			}
		}
	}()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		now := time.Now().UnixMilli()
		for _, e := range p.ActList.byType(activityTypeBigChess) {
			if e.BeginTime <= now && now < e.EndTime {
				return &bcSession{conn: conn, p: p, cleanup: func() {
					conn.Close()
					p.cancel()
					<-readerDone
					<-writerDone
				}}, nil
			}
		}
		time.Sleep(time.Second)
	}
	conn.Close()
	p.cancel()
	return nil, fmt.Errorf("no live type-341 window within 30s")
}

func TestLiveBigChessEnginePlay(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveBcUser = liveUserEnv(t)
		liveBcPass = livePassEnv(t)
	)
	liveBcServer := envIntOr(t, "TYTD_LIVE_SERVER", 710564)
	revivesAllowed := envIntOr(t, "TYTD_ENGINE_REVIVES", 3) // -1 = full respawn

	if _, err := enginePost("/v1/legal-moves", map[string]any{"state": map[string]any{
		"width": 8, "height": 9, "gameData": map[string]any{"self": map[string]any{"chessId": 1008, "x": 0, "y": 0}},
	}}); err != nil {
		t.Fatalf("analyzer server not reachable at %s: %v", engineURL(), err)
	}

	// dial performs the full login chain, connects, authenticates, and waits
	// for a live type-341 instance.
	dial := func() (*bcSession, error) { return bcDialLive(t, liveBcUser, liveBcPass, liveBcServer) }

	s, err := dial()
	if err != nil {
		t.Fatalf("live connect: %v", err)
	}
	defer s.cleanup()
	p := s.p
	t.Logf("connected, revives allowed: %d (server %d)", revivesAllowed, liveBcServer)

	// Lobby probe once per session start: explains enter_game refusals.
	if lv, ok := p.BigChessEnter(20 * time.Second); ok {
		t.Logf("lobby: freeCount=%v maxScore=%v gameStatus=%v", lv["freeCount"], lv["maxScore"], lv["gameStatus"])
	} else {
		t.Logf("lobby probe: %v", lv["error"])
		if bcStalled(lv) {
			t.Fatalf("stalled before first move: %v", lv["error"])
		}
	}

	reconnects := 0
	reconnect := func() bool {
		reconnects++
		if reconnects > 8 {
			t.Log("reconnect budget (8) exhausted")
			return false
		}
		s.cleanup()
		time.Sleep(3 * time.Second)
		ns, derr := dial()
		if derr != nil {
			t.Logf("reconnect %d failed: %v", reconnects, derr)
			return false
		}
		s, p = ns, ns.p
		t.Logf("reconnected (attempt %d)", reconnects)
		return true
	}

	engineMoves, fallbackMoves, feedOK, feedFail, revivesUsed, runsPlayed, stalls := 0, 0, 0, 0, 0, 0, 0
	totalTurns := 0
	settle := func(tag string) {
		ev, ok := p.BigChessEnd(20 * time.Second)
		if ok {
			rst := ev["state"].(bigchess.State)
			t.Logf("run %d %s: score=%d rank=%d", runsPlayed, tag, rst.Score, rst.NowRank)
		} else {
			t.Logf("run %d %s: settle refused: %v", runsPlayed, tag, ev["error"])
		}
	}

	// Resume or start the first run (retry: a dead leftover may need settling).
	var entered bool
	for attempt := 1; attempt <= 5 && !entered; attempt++ {
		if view, ok := p.BigChessEnterGame(20 * time.Second); ok {
			_ = view
			entered = true
			break
		} else {
			t.Logf("enter_game try %d refused: %v", attempt, view["error"])
			if bcStalled(view) && !reconnect() {
				t.Fatal("stalled at enter_game and reconnect failed")
			}
		}
		if lv, ok := p.BigChessEnter(20 * time.Second); ok {
			gs, _ := lv["gameStatus"].(int32)
			if gs == bigchess.StatusRevive || gs == bigchess.StatusOver {
				if ev, ok2 := p.BigChessEnd(20 * time.Second); ok2 {
					rst := ev["state"].(bigchess.State)
					t.Logf("settled leftover run: score=%d rank=%d", rst.Score, rst.NowRank)
				}
			}
			if fc, _ := lv["freeCount"].(int32); fc == 0 {
				t.Skipf("no free Big Chess start left today (gameStatus=%d) — wait for the daily reset then re-run", gs)
			}
		}
		time.Sleep(8 * time.Second)
	}
	if !entered {
		t.Fatal("could not start/resume a run after retries")
	}
	runsPlayed++

	for totalTurns < 3000 {
		totalTurns++
		v, ok := p.BigChessState()
		if !ok {
			t.Fatalf("lost state after %d turns", totalTurns)
		}
		st := v["state"].(bigchess.State)
		runNumber := runsPlayed

		switch st.GameStatus {
		case bigchess.StatusRunning:
		case bigchess.StatusRevive: // dead — full respawn: revive whenever accepted
			revivesUsed++
			rv, rok := p.BigChessRevive(20 * time.Second)
			if rok {
				t.Logf("respawn #%d accepted (run %d)", revivesUsed, runNumber)
				continue
			}
			if bcStalled(rv) {
				stalls++
				t.Logf("revive stalled — reconnecting")
				if !reconnect() {
					settle("stuck(stall)")
					goto done
				}
				continue
			}
			if revivesAllowed == -1 {
				// Full respawn means "as much as the server allows": a
				// refusal here = no funds. Record + settle + next start.
				t.Logf("revive #%d refused (%v): out of revive funds", revivesUsed, rv["error"])
			} else if revivesUsed > revivesAllowed {
				t.Logf("revive budget (%d) exhausted", revivesAllowed)
			} else {
				t.Logf("revive #%d refused (%v)", revivesUsed, rv["error"])
			}
			settle("dead")
			// Try the next bought start.
			if ev2, ok2 := p.BigChessEnterGame(20 * time.Second); !ok2 {
				t.Logf("no further start: %v — done", ev2["error"])
				goto done
			}
			if _, err := enginePost("/v1/rejections/clear", map[string]any{"sessionId": "engine-play:" + liveBcUser}); err != nil {
				t.Logf("rejection clear failed: %v", err)
			}
			runsPlayed++
			revivesUsed = 0
			continue
		default: // over / pass
			settle("over")
			goto done
		}

		if st.Step >= st.WinRound() {
			settle(fmt.Sprintf("reached winRound=%d", st.WinRound()))
			goto done
		}

		before := st
		var x, y int32
		var pipe, cross, similar bool
		var src string
		x, y, pipe, cross, similar, src = enginePick(t, st)
		if src == "none" {
			t.Logf("run %d (step %d): no move at all", runNumber, st.Step)
			settle("stuck")
			goto done
		}
		view, ok := p.BigChessMove(x, y, pipe, cross, similar, 20*time.Second)
		if !ok {
			// A transport stall must NOT be recorded as a rejection — the
			// engine would quarantine good advice for a server problem.
			if bcStalled(view) {
				stalls++
				t.Logf("move stalled — reconnecting and resuming")
				if !reconnect() {
					settle("stuck(stall)")
					goto done
				}
				if _, ok := p.BigChessEnterGame(20 * time.Second); !ok {
					settle("stuck(no-resume)")
					goto done
				}
				continue
			}
			if r, isR := view["ret"].(int32); isR {
				_, _ = enginePost("/v1/rejections", map[string]any{
					"state": bcEngineState(before),
					"action": map[string]any{"command": "S_BIG_CHESS_MOVE", "params": map[string]any{
						"activityId": before.ActivityID, "x": x, "y": y,
						"powerOn": pipe, "powerCross": cross, "powerRemoveType": similar,
					}},
					"result":    map[string]any{"ok": false, "ret": r},
					"sessionId": "engine-play:" + liveBcUser,
				})
			}
			t.Logf("run %d turn %d: %s move (%d,%d) refused: %v", runNumber, totalTurns, src, x, y, view["error"])
			if src == "engine" {
				continue // engine advice is per-position; re-analyze (rejection quarantine keeps it fresh)
			}
			settle("refused")
			goto done
		}
		if src == "engine" {
			engineMoves++
		} else {
			fallbackMoves++
		}
		nst := view["state"].(bigchess.State)
		if totalTurns%10 == 0 || nst.GameStatus != 0 {
			t.Logf("run %d turn %d (step %d, %s): move (%d,%d) → score=%d pieces=%d status=%d",
				runNumber, totalTurns, nst.Step, src, x, y, nst.Score, len(nst.Board), nst.GameStatus)
		}

		// Feed the replay learner (best-effort, never fatal).
		if _, err := enginePost("/v1/transitions", map[string]any{
			"stateBefore": bcEngineState(before),
			"action": map[string]any{
				"command": "S_BIG_CHESS_MOVE",
				"params": map[string]any{"activityId": nst.ActivityID, "x": x, "y": y,
					"powerOn": pipe, "powerCross": cross, "powerRemoveType": similar},
			},
			"result":     map[string]any{"ok": true, "ret": 0},
			"stateAfter": bcEngineState(nst),
			"sessionId":  "engine-play:" + liveBcUser,
		}); err != nil {
			feedFail++
			if feedFail <= 3 {
				t.Logf("transition feed failed: %v", err)
			}
		} else {
			feedOK++
		}
	}
done:
	t.Logf("TOTAL runs=%d engineMoves=%d fallback=%d revives=%d stalls=%d reconnects=%d feeds ok=%d fail=%d",
		runsPlayed, engineMoves, fallbackMoves, revivesUsed, stalls, reconnects, feedOK, feedFail)
}
