//go:build live

package tytd_go

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"
	"tytd_go/internal/bigchess"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	pb "google.golang.org/protobuf/proto"

	"github.com/gorilla/websocket"
)

// TestLiveBigChess plays the real Big Chess against the REAL server with the
// authorized test account: login → live type-341 instance → enter_game →
// loop of AI-chosen moves → settle. It proves the transport port end to end
// on the wire: command hexes, response routing, board merges, statuses.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveBigChess -v -timeout 300s .
//
// Money rule for this account: the test NEVER calls revive/use-random (the
// server may spend items or ingots there); deaths settle with bigchess_end,
// which is free.
func TestLiveBigChess(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveBcUser   = liveUserEnv(t)
		liveBcPass   = livePassEnv(t)
		liveBcServer = 710564
	)

	passportToken, err := request.LoginGPassport(liveBcUser, liveBcPass)
	if err != nil {
		t.Fatalf("LoginGPassport: %v", err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatalf("LoginFuntap: %v", err)
	}
	gameURL, err := request.GetServerURL(userID, liveBcServer)
	if err != nil {
		t.Fatalf("GetServerURL: %v", err)
	}
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
	if err != nil {
		t.Fatalf("GetWSKey: %v", err)
	}
	t.Logf("login ok uid=%d server=%d", uid, liveBcServer)

	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &Player{
		Username:  liveBcUser,
		UID:       uid,
		ChatID:    7,
		writeChan: make(chan []byte, 128),
	}
	p.ctx, p.cancel = context.WithCancel(ctx)

	go func() {
		for frame := range p.writeChan {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()

	dumpRaw := os.Getenv("TYTD_LIVE_RAWDUMP") != ""
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
			if dumpRaw && (msgType == SERVER_BIG_CHESS_ENTER_GAME || msgType == SERVER_BIG_CHESS_MOVE) {
				t.Logf("RAW %s: %s", msgType, hex.EncodeToString(realData))
			}
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			case SERVER_ACTIVITY_DETAIL:
				p.bcMergeConfig(realData)
			case SERVER_BIG_CHESS_ENTER, SERVER_BIG_CHESS_ENTER_GAME, SERVER_BIG_CHESS_MOVE,
				SERVER_BIG_CHESS_END, SERVER_BIG_CHESS_USE_ITEM, SERVER_BIG_CHESS_USE_RANDOM:
				p.bcDeliver(msgType, realData)
			}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	p.writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)

	// Wait for the live type-341 instance (21001 push is unasked).
	deadline := time.Now().Add(30 * time.Second)
	var actID int32
	for actID == 0 && time.Now().Before(deadline) {
		now := time.Now().UnixMilli()
		for _, e := range p.ActList.byType(activityTypeBigChess) {
			if e.BeginTime <= now && now < e.EndTime {
				actID = e.ActivityID
			}
		}
		if actID == 0 {
			time.Sleep(time.Second)
		}
	}
	if actID == 0 {
		t.Skip("Big Chess (type 341) is not inside its live window right now")
	}
	t.Logf("live big chess instance %d is open", actID)

	view, ok := p.BigChessEnterGame(20 * time.Second)
	if !ok {
		t.Fatalf("enter_game: %v", view["error"])
	}
	logBcState(t, view, "after enter_game")

	for turn := 0; turn < 12; turn++ {
		p.BcMu.Lock()
		st := *p.bcState
		st.Board = append([]bigchess.Piece(nil), p.bcState.Board...)
		st.NextChess = append([]bigchess.Piece(nil), p.bcState.NextChess...)
		p.BcMu.Unlock()
		if st.GameStatus != bigchess.StatusRunning {
			t.Logf("turn %d: status=%d — loop stops, settling below", turn, st.GameStatus)
			break
		}
		x, y, found := bcPickMove(st)
		if !found {
			t.Logf("turn %d: no destination at all", turn)
			break
		}
		view, ok := p.BigChessMove(x, y, false, false, false, 20*time.Second)
		if !ok {
			t.Logf("turn %d: move (%d,%d) refused: %v", turn, x, y, view["error"])
			if v2, ok2 := p.BigChessEnterGame(20 * time.Second); ok2 {
				logBcState(t, v2, "resync")
				continue
			}
			break
		}
		logBcState(t, view, "after move")
	}

	if v, ok := p.BigChessEnd(20 * time.Second); ok {
		s := v["state"].(bigchess.State)
		t.Logf("settled: score=%d rank=%d", s.Score, s.NowRank)
	} else {
		t.Logf("end: %v", v["error"])
	}
}

// bcPickMove is the TEST-LOCAL decision function (deliberately not shipped
// code, per the project rule that move choice belongs to the AI): normal
// rook-glide destinations, fewer enemy attackers after the pending wave
// first, capture value as tiebreak.
func bcPickMove(st bigchess.State) (int32, int32, bool) {
	occ := map[[2]int32]bigchess.Piece{}
	for _, e := range st.Board {
		occ[[2]int32{e.X, e.Y}] = e
	}
	bestX, bestY, bestScore := int32(0), int32(0), int32(0x7fffffff)
	found := false
	consider := func(nx, ny int32, cap int32) {
		post := make([]bigchess.Piece, 0, len(st.Board)+len(st.NextChess)+1)
		for _, e := range st.Board {
			if e.X == nx && e.Y == ny {
				continue // captured before it can threaten its own cell
			}
			post = append(post, e)
		}
		if st.NextTurn != nil && *st.NextTurn <= 1 {
			for _, e := range st.NextChess {
				if e.X == nx && e.Y == ny {
					continue
				}
				post = append(post, e)
			}
		}
		at := bcAttackers(nx, ny, post)
		rank := at*1000 - cap
		if !found || rank < bestScore {
			found, bestScore, bestX, bestY = true, rank, nx, ny
		}
	}
	for _, d := range [4][2]int32{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
		for step := int32(1); ; step++ {
			nx, ny := st.Self.X+d[0]*step, st.Self.Y+d[1]*step
			if nx < 0 || ny < 0 || nx >= st.Width || ny >= st.Height {
				break
			}
			if e, busy := occ[[2]int32{nx, ny}]; busy {
				consider(nx, ny, st.PieceScores[e.ChessID])
				break
			}
			consider(nx, ny, 0)
		}
	}
	return bestX, bestY, found
}

// bcAttackers counts how many of the given pieces attack (tx,ty) — the
// threat geometry from bigchess://rules, test-local mirror.
func bcAttackers(tx, ty int32, pieces []bigchess.Piece) int32 {
	pos := map[[2]int32]int32{}
	for _, e := range pieces {
		pos[[2]int32{e.X, e.Y}]++
	}
	n := int32(0)
	for _, e := range pieces {
		dx, dy := tx-e.X, ty-e.Y
		adx, ady := abs32(dx), abs32(dy)
		hit := false
		switch e.ChessID {
		case bigchess.IDPawn, bigchess.IDGeneral:
			hit = adx+ady == 1
		case bigchess.IDAdvisor:
			hit = adx == 1 && ady == 1
		case bigchess.IDElephant:
			hit = adx == 2 && ady == 2 && pos[[2]int32{e.X + dx/2, e.Y + dy/2}] == 0
		case bigchess.IDCannon:
			hit = (e.X == tx || e.Y == ty) && between(e, tx, ty, pieces) == 1
		case bigchess.IDKnight:
			if (adx == 1 && ady == 2) || (adx == 2 && ady == 1) {
				lx, ly := e.X, e.Y
				if ady == 2 {
					ly += sign(dy)
				} else {
					lx += sign(dx)
				}
				hit = pos[[2]int32{lx, ly}] == 0
			}
		case bigchess.IDRook:
			hit = (e.X == tx || e.Y == ty) && between(e, tx, ty, pieces) == 0
		}
		if hit {
			n++
		}
	}
	return n
}

func between(from bigchess.Piece, tx, ty int32, pieces []bigchess.Piece) int32 {
	if from.X != tx && from.Y != ty {
		return 99
	}
	c := int32(0)
	for _, e := range pieces {
		if e.ChessNo == from.ChessNo {
			continue
		}
		if e.X == tx && e.Y == ty {
			continue // the target square is not a screen
		}
		if from.X == tx && e.X == tx && min32(from.Y, ty) < e.Y && e.Y < max32(from.Y, ty) {
			c++
		}
		if from.Y == ty && e.Y == ty && min32(from.X, tx) < e.X && e.X < max32(from.X, tx) {
			c++
		}
	}
	return c
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
func sign(v int32) int32 {
	if v < 0 {
		return -1
	}
	return 1
}
func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}
func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

// logBcState dumps the snapshot exactly like the MCP tool would.
func logBcState(t *testing.T, view map[string]any, tag string) {
	t.Helper()
	b, _ := json.Marshal(view)
	t.Logf("%s: %s", tag, b)
}
