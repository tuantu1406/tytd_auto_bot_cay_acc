package tytd_go

import (
	"bytes"
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/bigchess"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// bcTestPlayer builds a running Player with a live type-341 window and a
// drained writeChan (no socket): the tests assert on the frames and feed
// answers straight into the seats.
func bcTestPlayer(t *testing.T) *Player {
	t.Helper()
	p := &Player{Username: "bc", Running: true, UID: 42, writeChan: make(chan []byte, 8)}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	t.Cleanup(p.cancel)
	now := time.Now().UnixMilli()
	setActivities(&p.ActList, activityEntry{ActivityID: 999901, Type: activityTypeBigChess,
		BeginTime: now - 1000, EndTime: now + 10_000_000_000})
	return p
}

// bcAnswer blocks until the given op occupies its answer seat, then hands it
// the payload — the same path the reader goroutine would take.
func bcAnswer(t *testing.T, p *Player, wantType string, payload []byte) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		p.BcMu.Lock()
		ch := p.bcChans[wantType]
		p.BcMu.Unlock()
		if ch != nil {
			ch <- payload
			return
		}
		select {
		case <-deadline:
			t.Fatalf("seat %s never claimed", wantType)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func bcFrames(p *Player) []string {
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

func TestBigChessEnterGameRoundTrip(t *testing.T) {
	p := bcTestPlayer(t)
	resp, err := pb.Marshal(&proto.ChineseChessEnterGameResp{
		Ret:           0,
		ChessDataList: []*proto.ChessData{{ChessNo: pb.Int32(11), ChessId: pb.Int32(1001), X: pb.Int32(3), Y: pb.Int32(1)}},
		SelfChessData: &proto.ChessData{ChessNo: pb.Int32(1), ChessId: pb.Int32(1008), X: pb.Int32(3), Y: pb.Int32(4)},
		Score:         pb.Int64(500),
		Round:         pb.Int32(3),
		NextRound:     pb.Int32(2),
		NextChess:     []*proto.ChessData{{ChessNo: pb.Int32(21), ChessId: pb.Int32(1007), X: pb.Int32(6), Y: pb.Int32(4)}},
		PowerTimes:    pb.Int32(1),
		FreeCount:     pb.Int32(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		view map[string]any
		ok   bool
	}
	done := make(chan result, 1)
	go func() {
		v, ok := p.BigChessEnterGame(2 * time.Second)
		done <- result{v, ok}
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_ENTER_GAME, resp)
	r := <-done
	if !r.ok {
		t.Fatalf("enter_game failed: %v", r.view)
	}
	// config pull (21003) then enter_game went out, in that order.
	frames := bcFrames(p)
	want := []string{"0000520b", BIG_CHESS_ENTER_GAME_CMD}
	if len(frames) != len(want) || frames[0] != want[0] || frames[1] != want[1] {
		t.Fatalf("frames=%v want=%v", frames, want)
	}
	st := r.view["state"].(bigchess.State)
	if st.ActivityID != 999901 || len(st.Board) != 1 || !st.HasSelf ||
		st.Score != 500 || st.Step != 3 || st.PowerTimes != 1 || st.FreeCount != 2 ||
		st.NextTurn == nil || *st.NextTurn != 2 || len(st.NextChess) != 1 {
		t.Fatalf("bad snapshot: %+v", st)
	}
	if st.Version < 1 {
		t.Fatalf("stateVersion not bumped: %d", st.Version)
	}
}

func TestBigChessMoveValidationAndRoundTrip(t *testing.T) {
	p := bcTestPlayer(t)
	st := bigchess.New(999901)
	st.SetSelf(bigchess.Piece{ChessNo: 1, ChessID: bigchess.IDPlayer, X: 3, Y: 4})
	st.SetBoard([]bigchess.Piece{{ChessNo: 11, ChessID: bigchess.IDGeneral, X: 6, Y: 4}})
	p.BcMu.Lock()
	p.bcState = st
	p.BcMu.Unlock()

	// Out-of-range refuses BEFORE any frame is written.
	if view, ok := p.BigChessMove(8, 0, false, false, false, time.Second); ok || view["error"] == nil {
		t.Fatalf("x=8 accepted on an 8-wide board: %v", view)
	}
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("illegal move wrote %d frames", n)
	}
	// Own cell refuses too.
	if view, ok := p.BigChessMove(3, 4, false, false, false, time.Second); ok || view["error"] == nil {
		t.Fatalf("self-cell move accepted: %v", view)
	}

	// Legal one round-trips: server survivors replace the board.
	resp, _ := pb.Marshal(&proto.ChineseChessMoveResp{
		Ret:       0,
		Chess:     []*proto.ChessData{{ChessNo: pb.Int32(12), ChessId: pb.Int32(1001), X: pb.Int32(0), Y: pb.Int32(0)}},
		SelfChess: &proto.ChessData{ChessNo: pb.Int32(1), ChessId: pb.Int32(1008), X: pb.Int32(6), Y: pb.Int32(4)},
		Score:     pb.Int64(600),
		Step:      pb.Int32(4),
	})
	done := make(chan map[string]any, 1)
	go func() {
		v, _ := p.BigChessMove(6, 4, false, false, false, 2*time.Second)
		done <- v
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_MOVE, resp)
	view := <-done
	s := view["state"].(bigchess.State)
	if len(s.Board) != 1 || s.Board[0].ChessNo != 12 || s.Self.X != 6 || s.Score != 600 || s.Step != 4 {
		t.Fatalf("move state not merged: %+v", s)
	}
	// The frame carries the full-ID command AND all three power bools are
	// on the wire explicitly (0x20/0x28/0x30 = fields 4/5/6) — the server's
	// proto2 parser drops bodies without them (live-verified).
	select {
	case f := <-p.writeChan:
		if hex.EncodeToString(f[6:10]) != BIG_CHESS_MOVE_CMD {
			t.Fatalf("cmd hex %q", f[6:10])
		}
		payload := f[18:]
		for _, tag := range []byte{0x20, 0x28, 0x30} {
			if !bytes.Contains(payload, []byte{tag}) {
				t.Fatalf("power field tag %#x missing from body %x", tag, payload)
			}
		}
	default:
		t.Fatal("no move frame written")
	}
}

func TestBigChessStatusGates(t *testing.T) {
	p := bcTestPlayer(t)
	st := bigchess.New(999901)
	st.SetSelf(bigchess.Piece{ChessNo: 1, ChessID: bigchess.IDPlayer, X: 3, Y: 4})
	p.BcMu.Lock()
	p.bcState = st
	p.BcMu.Unlock()

	// Dead (gameStatus=1): move refuses, revive is allowed to reach the
	// transport; then ret!=0 surfaces as bigChessError.
	st.GameStatus = bigchess.StatusRevive
	if view, ok := p.BigChessMove(3, 0, false, false, false, time.Second); ok || view["error"] == nil {
		t.Fatalf("move while dead accepted: %v", view)
	}
	reviveRefused, _ := pb.Marshal(&proto.ChineseChessUseItemResp{Ret: 9, ItemReviveCount: pb.Int32(0)})
	done := make(chan map[string]any, 1)
	go func() {
		v, _ := p.BigChessRevive(2 * time.Second)
		done <- v
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_USE_ITEM, reviveRefused)
	if v := <-done; v["error"] == nil || v["error"].(string) == "" {
		t.Fatalf("ret!=0 revive not surfaced: %v", v)
	} else if got := v["error"].(string); !strings.HasSuffix(got, "(ret=9)") {
		t.Fatalf("ret not quoted: %q", got)
	}
	if p.bcState.GameStatus != bigchess.StatusRevive {
		t.Fatal("refused revive must not flip status")
	}

	// Busy seat: a claim already in flight refuses with the busy message.
	ch, err := p.bcClaim(SERVER_BIG_CHESS_MOVE)
	if err != nil {
		t.Fatal(err)
	}
	st.GameStatus = bigchess.StatusRunning
	if view, ok := p.BigChessMove(3, 0, false, false, false, time.Second); ok || view["error"] == nil {
		t.Fatalf("second concurrent move accepted: %v", view)
	}
	p.bcRelease(SERVER_BIG_CHESS_MOVE, ch)
}

func TestBigChessUseRandomRemoval(t *testing.T) {
	p := bcTestPlayer(t)
	st := bigchess.New(999901)
	st.SetSelf(bigchess.Piece{ChessNo: 1, ChessID: bigchess.IDPlayer, X: 3, Y: 4})
	st.SetBoard([]bigchess.Piece{
		{ChessNo: 11, ChessID: bigchess.IDPawn, X: 0, Y: 0},
		{ChessNo: 12, ChessID: bigchess.IDRook, X: 7, Y: 8},
	})
	p.BcMu.Lock()
	p.bcState = st
	p.BcMu.Unlock()

	resp, _ := pb.Marshal(&proto.ChineseChessUseRemoveItemResp{
		Ret:                    0,
		Chess:                  []*proto.ChessData{{ChessNo: pb.Int32(11), ChessId: pb.Int32(1001)}}, // the REMOVED list
		PowerRemoveRandomCount: pb.Int32(1),
	})
	done := make(chan map[string]any, 1)
	go func() {
		v, _ := p.BigChessUseRandom(2 * time.Second)
		done <- v
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_USE_RANDOM, resp)
	s := (<-done)["state"].(bigchess.State)
	if len(s.Board) != 1 || s.Board[0].ChessNo != 12 || s.PowerRemoveRandomCount != 1 {
		t.Fatalf("removal not applied: %+v", s)
	}
}

func TestBigChessMergeConfig(t *testing.T) {
	p := bcTestPlayer(t)
	uc, err := pb.Marshal(&proto.ChineseChessActivityConfig{
		ChessConfigList: []*proto.ChessConfig{{Id: pb.Int32(1001), Score: pb.Int32(50)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, _ := pb.Marshal(&proto.RspGetActivityDetail{
		Activity: &proto.ActivityCommonData{
			DetailConfig: &proto.ActivityDetailConfig{
				CommonConfig: &proto.ActivityCommonConfig{
					MainConfig: &proto.ActivityMainConfig{ActivityId: pb.Int32(999901), Type: pb.Int32(activityTypeBigChess)},
					ParamConfig: []*proto.ActivityParamConfig{
						{Param: pb.String("BIGCHESS_BOARD_XY"), Value: pb.String("10*12")},
						{Param: pb.String("BIGCHESS_WIN_ROUND"), Value: pb.String("50")},
						{Param: pb.String("SOMETHING_ELSE"), Value: pb.String("x")},
					},
				},
				UniqueConfig: uc,
			},
		},
	})
	p.bcMergeConfig(frame)

	p.BcMu.Lock()
	s := p.bcState
	p.BcMu.Unlock()
	if s == nil || s.Width != 10 || s.Height != 12 {
		t.Fatalf("board size not applied: %+v", s)
	}
	if s.PieceScores[1001] != 50 {
		t.Fatalf("pieceScores not applied: %+v", s.PieceScores)
	}
	if s.WinRound() != 50 {
		t.Fatalf("win round: %d", s.WinRound())
	}
	if _, leaked := s.Params["SOMETHING_ELSE"]; leaked {
		t.Fatal("non-BIGCHESS params must not be stored")
	}
	// A different activity's detail answer must not touch Big Chess state.
	other, _ := pb.Marshal(&proto.RspGetActivityDetail{Activity: &proto.ActivityCommonData{
		DetailConfig: &proto.ActivityDetailConfig{CommonConfig: &proto.ActivityCommonConfig{
			MainConfig: &proto.ActivityMainConfig{Type: pb.Int32(150)},
		}},
	}})
	p2 := bcTestPlayer(t)
	p2.bcMergeConfig(other)
	if p2.bcState != nil {
		t.Fatal("type-150 detail created Big Chess state")
	}
}

func TestBigChessEndSetsSettled(t *testing.T) {
	p := bcTestPlayer(t)
	st := bigchess.New(999901)
	st.SetSelf(bigchess.Piece{ChessNo: 1, ChessID: bigchess.IDPlayer, X: 3, Y: 4})
	p.BcMu.Lock()
	p.bcState = st
	p.BcMu.Unlock()
	resp, _ := pb.Marshal(&proto.ChineseChessEndResp{Ret: 0, Score: pb.Int64(1234), NowRank: pb.Int32(7)})
	done := make(chan map[string]any, 1)
	go func() {
		v, _ := p.BigChessEnd(2 * time.Second)
		done <- v
	}()
	bcAnswer(t, p, SERVER_BIG_CHESS_END, resp)
	s := (<-done)["state"].(bigchess.State)
	if !s.Settled || s.Score != 1234 || s.NowRank != 7 || s.GameStatus != bigchess.StatusOver {
		t.Fatalf("end not applied: %+v", s)
	}
}
