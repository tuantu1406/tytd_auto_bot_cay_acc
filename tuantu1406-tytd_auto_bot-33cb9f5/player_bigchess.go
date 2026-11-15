package tytd_go

// Big Chess (Tiên Dịch Bài Cục, activity type 341) transport. This game is
// different from every other supported activity: nobody on Telegram plays
// it. The bot is a dumb, faithful pipe between the game server and the AI
// sitting behind the MCP endpoint — it tracks the authoritative board the
// server last sent, forwards exactly one action at a time, and never picks
// a move (there is deliberately no solver here; bigchess://rules teaches
// the AI everything it needs). Requests/responses follow
// ../remoteBigChess/BIG_CHESS_PROTOCOL.md; responses reserve one seat per
// transport type, the Sky Trade pattern.

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/bigchess"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

const activityTypeBigChess int32 = 341

var (
	errBcBusy    = errors.New("another big-chess action is already in flight for this account")
	errBcTimeout = errors.New("big-chess: game server did not answer, retry after checking state")
	errBcGone    = errors.New("big-chess: account connection dropped mid-request")
)

// bigChessError carries the game server's own refusal code, so tool callers
// can tell "illegal move" from "wrong phase" from a transport failure
// without string-matching.
type bigChessError struct {
	Cmd string
	Ret int32
}

func (e *bigChessError) Error() string {
	return fmt.Sprintf("big-chess: game server refused %s (ret=%d)", e.Cmd, e.Ret)
}

// The server schema is proto2 with REQUIRED fields: a body missing one of
// them fails to parse and the server silently ignores the packet (verified
// live — generated proto3 messages omit false bools and 0 ints, and the
// move answer never arrives). Requests are therefore hand-encoded so every
// required field is on the wire even at zero value.

func bcActivityBody(id int32) []byte {
	b := protowire.AppendTag(nil, 1, protowire.VarintType)
	return protowire.AppendVarint(b, uint64(uint32(id)))
}

func bci32(b []byte, field int32, v int32) []byte {
	b = protowire.AppendTag(b, protowire.Number(field), protowire.VarintType)
	return protowire.AppendVarint(b, uint64(uint32(v)))
}

func bcBool(b []byte, field int32, v bool) []byte {
	var n uint64
	if v {
		n = 1
	}
	b = protowire.AppendTag(b, protowire.Number(field), protowire.VarintType)
	return protowire.AppendVarint(b, n)
}

func bcMoveBody(id, x, y int32, powerOn, powerCross, powerRemoveType bool) []byte {
	b := bcActivityBody(id)
	b = bci32(b, 2, x)
	b = bci32(b, 3, y)
	b = bcBool(b, 4, powerOn)
	b = bcBool(b, 5, powerCross)
	b = bcBool(b, 6, powerRemoveType)
	return b
}

func bcUseItemBody(id, typ int32) []byte {
	return bci32(bcActivityBody(id), 2, typ)
}

// bcActiveID resolves the live ranked Big Chess instance the same way Sky
// Trade does it: look the type up in the pushed activity list and pick the
// entry inside its begin/end window. The type is never the activityId.
func (p *Player) bcActiveID() (int32, error) {
	now := time.Now().UnixMilli()
	for _, e := range p.ActList.byType(activityTypeBigChess) {
		if e.BeginTime <= now && now < e.EndTime {
			return e.ActivityID, nil
		}
	}
	return 0, fmt.Errorf("big-chess: no active %d-instance in the activity list (event closed or not pushed yet)", activityTypeBigChess)
}

// ---- answer seats (Sky Trade stClaim/stRelease/stDeliver pattern) --------

func (p *Player) bcClaim(msgType string) (chan []byte, error) {
	p.BcMu.Lock()
	defer p.BcMu.Unlock()
	if p.bcChans == nil {
		p.bcChans = map[string]chan []byte{}
	}
	if _, busy := p.bcChans[msgType]; busy {
		return nil, errBcBusy
	}
	ch := make(chan []byte, 1)
	p.bcChans[msgType] = ch
	return ch, nil
}

func (p *Player) bcRelease(msgType string, ch chan []byte) {
	p.BcMu.Lock()
	defer p.BcMu.Unlock()
	if cur, ok := p.bcChans[msgType]; ok && cur == ch {
		delete(p.bcChans, msgType)
	}
}

// bcDeliver hands one answer to the seat waiting for its type, if any.
// Called by wsHandler (reader goroutine): it must never block.
func (p *Player) bcDeliver(msgType string, data []byte) {
	p.BcMu.Lock()
	ch := p.bcChans[msgType]
	p.BcMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

func (p *Player) bcRoundTrip(cmd, wantType string, body []byte, timeout time.Duration) ([]byte, error) {
	ch, err := p.bcClaim(wantType)
	if err != nil {
		return nil, err
	}
	defer p.bcRelease(wantType, ch)
	p.write(pack.Pack(cmd, body, p.UID))
	// A session built by Run always has ctx; a nil ctx means a test stub —
	// the nil channel simply never fires.
	var ctxDone <-chan struct{}
	if p.ctx != nil {
		ctxDone = p.ctx.Done()
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, errBcTimeout
	case <-ctxDone:
		return nil, errBcGone
	}
}

// ---- config (piece scores + BIGCHESS_* params via the 21003 detail) -----

// bcRequestConfig pulls the activity detail once per 10 minutes until it
// parses; the answer arrives on the shared SERVER_ACTIVITY_DETAIL route and
// is folded in by bcMergeConfig. Auto-polling (activityRefresh) covers the
// rest, so callers never wait on this.
func (p *Player) bcRequestConfig(id int32) {
	p.BcMu.Lock()
	fresh := p.bcCfgDone || time.Since(p.bcCfgAt) < 10*time.Minute
	p.BcMu.Unlock()
	if fresh {
		return
	}
	p.BcMu.Lock()
	p.bcCfgAt = time.Now()
	p.BcMu.Unlock()
	payload, err := activityPollFrame(id)
	if err != nil {
		return
	}
	p.write(pack.Pack(ACTIVITY_GET_DATA_CMD, payload, p.UID))
}

// bcMergeConfig consumes every 21003 answer; like stMergeConfig it checks
// the type itself and ignores everything else.
func (p *Player) bcMergeConfig(data []byte) {
	var rsp proto.RspGetActivityDetail
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return
	}
	dc := rsp.GetActivity().GetDetailConfig()
	if dc.GetCommonConfig().GetMainConfig().GetType() != activityTypeBigChess {
		return
	}
	scores := map[int32]int32{}
	if uc := dc.GetUniqueConfig(); len(uc) > 0 {
		var cfg proto.ChineseChessActivityConfig
		if err := pb.Unmarshal(uc, &cfg); err == nil {
			for _, c := range cfg.GetChessConfigList() {
				scores[c.GetId()] = c.GetScore()
			}
		} else {
			log.Printf("[%s] [BigChess] uniqueConfig decode: %v", p.Username, err)
		}
	}
	params := map[string]string{}
	for _, pc := range dc.GetCommonConfig().GetParamConfig() {
		if strings.HasPrefix(pc.GetParam(), "BIGCHESS_") {
			params[pc.GetParam()] = pc.GetValue()
		}
	}
	p.BcMu.Lock()
	defer p.BcMu.Unlock()
	p.bcCfgDone = true
	if p.bcState == nil {
		p.bcState = bigchess.New(0)
	}
	if len(scores) > 0 {
		p.bcState.PieceScores = scores
	}
	for k, v := range params {
		p.bcState.Params[k] = v
	}
	// Live board size overrides the documented 8*9 fallback.
	if xy := params["BIGCHESS_BOARD_XY"]; xy != "" {
		if w, h, ok := parseBoardXY(xy); ok {
			p.bcState.Width, p.bcState.Height = w, h
		}
	}
	p.bcState.Touch()
}

func parseBoardXY(s string) (int32, int32, bool) {
	a, b, ok := strings.Cut(s, "*")
	if !ok {
		return 0, 0, false
	}
	w, err1 := strconv.ParseInt(strings.TrimSpace(a), 10, 32)
	h, err2 := strconv.ParseInt(strings.TrimSpace(b), 10, 32)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return int32(w), int32(h), true
}

// ---- state access for the MCP layer ---------------------------------------

// BigChessState returns a defensive copy plus the live activity window, so
// the MCP layer can serve it without ever racing the reader goroutine.
func (p *Player) BigChessState() (map[string]any, bool) {
	p.BcMu.Lock()
	defer p.BcMu.Unlock()
	if p.bcState == nil {
		return nil, false
	}
	s := *p.bcState // scalars copy; slices/maps rebuilt below
	s.Board = append([]bigchess.Piece(nil), p.bcState.Board...)
	s.NextChess = append([]bigchess.Piece(nil), p.bcState.NextChess...)
	s.PieceScores = make(map[int32]int32, len(p.bcState.PieceScores))
	for k, v := range p.bcState.PieceScores {
		s.PieceScores[k] = v
	}
	s.Params = make(map[string]string, len(p.bcState.Params))
	for k, v := range p.bcState.Params {
		s.Params[k] = v
	}
	view := map[string]any{"state": s}
	if e := p.bcEntryLocked(s.ActivityID); e != nil {
		view["activityWindow"] = map[string]any{
			"activityId": e.ActivityID,
			"beginTime":  e.BeginTime,
			"endTime":    e.EndTime,
			"beginShow":  e.BeginShowTime,
			"endShow":    e.EndShowTime,
		}
	}
	return view, true
}

// bcEntryLocked finds a stored activity row by id (BcMu must be held; the
// ActList has its own lock and Range-by-id is fine at tool speed).
func (p *Player) bcEntryLocked(id int32) *bigchessActivityRow {
	if id == 0 {
		return nil
	}
	for _, e := range p.ActList.snapshot() {
		if e.ActivityID == id {
			return &bigchessActivityRow{ActivityID: e.ActivityID, BeginTime: e.BeginTime, EndTime: e.EndTime, BeginShowTime: e.BeginShowTime, EndShowTime: e.EndShowTime}
		}
	}
	return nil
}

type bigchessActivityRow struct {
	ActivityID    int32
	BeginTime     int64
	EndTime       int64
	BeginShowTime int64
	EndShowTime   int64
}

// ---- ranked-mode operations (the MCP tool bodies) -------------------------

// BigChessEnterGame starts OR resumes a run (server decides which) and
// installs the full board snapshot.
// BigChessEnter pulls the 234101 lobby: remaining free starts, best score,
// and the server-side run status. It is the read-only probe that explains a
// refused enter_game (freeCount=0, a dead run awaiting settle, …).
func (p *Player) BigChessEnter(timeout time.Duration) (map[string]any, bool) {
	id, err := p.bcActiveID()
	if err != nil {
		return p.bcFail(err)
	}
	data, err := p.bcRoundTrip(BIG_CHESS_ENTER_CMD, SERVER_BIG_CHESS_ENTER, bcActivityBody(id), timeout)
	if err != nil {
		return p.bcFail(err)
	}
	var resp proto.ChineseChessEnterResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		return p.bcFail(fmt.Errorf("big-chess: undecodable lobby answer: %w", err))
	}
	if resp.GetRet() != 0 {
		return p.bcFail(&bigChessError{Cmd: "enter", Ret: resp.GetRet()})
	}
	pd := resp.GetPlayerData()
	p.BcMu.Lock()
	if p.bcState == nil || p.bcState.ActivityID != id {
		// Shell state: lobby bookkeeping only; enter_game later installs the
		// real board. Having bcState exist lets End settle a dead leftover
		// run discovered from the lobby without a full snapshot first.
		p.bcState = bigchess.New(id)
	}
	p.bcState.FreeCount = pd.GetFreeCount()
	p.bcState.MaxScore = pd.GetMaxScore()
	p.bcState.GameStatus = pd.GetGameStatus()
	p.bcState.Touch()
	p.BcMu.Unlock()
	return map[string]any{
		"activityId": id,
		"freeCount":  pd.GetFreeCount(),
		"maxScore":   pd.GetMaxScore(),
		"gameStatus": pd.GetGameStatus(),
	}, true
}

func (p *Player) BigChessEnterGame(timeout time.Duration) (map[string]any, bool) {
	id, err := p.bcActiveID()
	if err != nil {
		return p.bcFail(err)
	}
	p.bcRequestConfig(id)
	data, err := p.bcRoundTrip(BIG_CHESS_ENTER_GAME_CMD, SERVER_BIG_CHESS_ENTER_GAME, bcActivityBody(id), timeout)
	if err != nil {
		return p.bcFail(err)
	}
	var resp proto.ChineseChessEnterGameResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		return p.bcFail(fmt.Errorf("big-chess: undecodable enter-game answer: %w", err))
	}
	if resp.GetRet() != 0 {
		return p.bcFail(&bigChessError{Cmd: "enter_game", Ret: resp.GetRet()})
	}
	p.BcMu.Lock()
	if p.bcState == nil || p.bcState.ActivityID != id {
		p.bcState = bigchess.New(id)
	}
	s := p.bcState
	s.Board = bcPieces(resp.GetChessDataList())
	s.SetSelf(bcFirst(resp.GetSelfChessData()))
	s.HasSelf = resp.GetSelfChessData() != nil
	s.Score = resp.GetScore()
	s.Step = resp.GetRound()
	s.GameStatus = resp.GetGameStatus()
	s.NextTurn = bcTurnPtr(resp.GetNextRound())
	s.NextChess = bcPieces(resp.GetNextChess())
	s.PowerTimes = resp.GetPowerTimes()
	s.ItemReviveCount = resp.GetItemReviveCount()
	s.IngotReviveCount = resp.GetIngotReviveCount()
	s.PassReviveCount = resp.GetPassReviveCount()
	s.PowerOnCount = resp.GetPowerOnCount()
	s.PowerCrossCount = resp.GetPowerCrossCount()
	s.PowerRemoveTypeCount = resp.GetPowerRemoveTypeCount()
	s.PowerRemoveRandomCount = resp.GetPowerRemoveRandomCount()
	s.FreeCount = resp.GetFreeCount()
	s.GuideOpen = resp.GetGuideOpen()
	s.Settled = false
	s.Touch()
	p.BcMu.Unlock()
	return p.BigChessState()
}

// BigChessMove forwards one validated move. Validation is transport-level
// only (coordinate range, live run): legality is the game server's call.
func (p *Player) BigChessMove(x, y int32, powerOn, powerCross, powerRemoveType bool, timeout time.Duration) (map[string]any, bool) {
	p.BcMu.Lock()
	s := p.bcState
	p.BcMu.Unlock()
	if s == nil || s.ActivityID == 0 {
		return p.bcFail(errors.New("big-chess: no run yet — call bigchess_enter_game first"))
	}
	if s.GameStatus != bigchess.StatusRunning {
		return p.bcFail(fmt.Errorf("big-chess: run is not in play (gameStatus=%d) — use bigchess_revive or bigchess_end", s.GameStatus))
	}
	if x < 0 || x >= s.Width || y < 0 || y >= s.Height {
		return p.bcFail(fmt.Errorf("big-chess: (%d,%d) outside the %dx%d board", x, y, s.Width, s.Height))
	}
	if s.HasSelf && x == s.Self.X && y == s.Self.Y {
		return p.bcFail(errors.New("big-chess: destination is the player's own cell — a move must change position"))
	}
	data, err := p.bcRoundTrip(BIG_CHESS_MOVE_CMD, SERVER_BIG_CHESS_MOVE,
		bcMoveBody(s.ActivityID, x, y, powerOn, powerCross, powerRemoveType), timeout)
	if err != nil {
		return p.bcFail(err)
	}
	var resp proto.ChineseChessMoveResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		return p.bcFail(fmt.Errorf("big-chess: undecodable move answer: %w", err))
	}
	if resp.GetRet() != 0 {
		return p.bcFail(&bigChessError{Cmd: "move", Ret: resp.GetRet()})
	}
	p.BcMu.Lock()
	s.SetBoard(bcPieces(resp.GetChess())) // survivor set replaces the board
	if resp.SelfChess != nil {
		s.SetSelf(bcFirst(resp.GetSelfChess()))
	}
	s.MoveChess = nil
	if resp.GetMoveChess() != nil {
		mc := bcFirst(resp.GetMoveChess())
		s.MoveChess = &mc
	}
	s.SetPreview(bcTurnPtr(resp.GetNextTurn()), bcPieces(resp.GetNextChess()))
	s.GameStatus = resp.GetGameStatus()
	s.Score = resp.GetScore()
	s.Step = resp.GetStep()
	s.PowerOnCount = resp.GetPowerOnCount()
	s.PowerCrossCount = resp.GetPowerCrossCount()
	s.PassReviveCount = resp.GetPassReviveCount()
	s.PowerRemoveTypeCount = resp.GetPowerRemoveTypeCount()
	s.NewPlayer = resp.GetNewPlayer()
	if resp.GetIsRevive() > 0 {
		s.Params["lastAutoReviveStep"] = strconv.Itoa(int(s.Step))
	}
	p.BcMu.Unlock()
	return p.BigChessState()
}

// BigChessRevive accepts the server's revival offer after a death
// (gameStatus==1). The server alone picks the price (item then ingot).
func (p *Player) BigChessRevive(timeout time.Duration) (map[string]any, bool) {
	p.BcMu.Lock()
	s := p.bcState
	p.BcMu.Unlock()
	if s == nil || s.ActivityID == 0 {
		return p.bcFail(errors.New("big-chess: no run yet"))
	}
	if s.GameStatus != bigchess.StatusRevive {
		return p.bcFail(fmt.Errorf("big-chess: not awaiting revival (gameStatus=%d)", s.GameStatus))
	}
	data, err := p.bcRoundTrip(BIG_CHESS_USE_ITEM_CMD, SERVER_BIG_CHESS_USE_ITEM, bcUseItemBody(s.ActivityID, 0), timeout)
	if err != nil {
		return p.bcFail(err)
	}
	var resp proto.ChineseChessUseItemResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		return p.bcFail(fmt.Errorf("big-chess: undecodable use-item answer: %w", err))
	}
	if resp.GetRet() != 0 {
		return p.bcFail(&bigChessError{Cmd: "revive", Ret: resp.GetRet()})
	}
	p.BcMu.Lock()
	s.SetBoard(bcPieces(resp.GetChess()))
	s.ItemReviveCount = resp.GetItemReviveCount()
	s.IngotReviveCount = resp.GetIngotReviveCount()
	s.GuideOpen = s.GuideOpen || resp.GetGuideOpen()
	s.GameStatus = bigchess.StatusRunning // revival accepted; the next move confirms
	p.BcMu.Unlock()
	return p.BigChessState()
}

// BigChessUseRandom spends one random-removal charge; the answer lists the
// REMOVED pieces (server's choice), never a survivor board.
func (p *Player) BigChessUseRandom(timeout time.Duration) (map[string]any, bool) {
	p.BcMu.Lock()
	s := p.bcState
	p.BcMu.Unlock()
	if s == nil || s.ActivityID == 0 {
		return p.bcFail(errors.New("big-chess: no run yet"))
	}
	data, err := p.bcRoundTrip(BIG_CHESS_USE_RANDOM_CMD, SERVER_BIG_CHESS_USE_RANDOM, bcActivityBody(s.ActivityID), timeout)
	if err != nil {
		return p.bcFail(err)
	}
	var resp proto.ChineseChessUseRemoveItemResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		return p.bcFail(fmt.Errorf("big-chess: undecodable use-random answer: %w", err))
	}
	if resp.GetRet() != 0 {
		return p.bcFail(&bigChessError{Cmd: "use_random", Ret: resp.GetRet()})
	}
	p.BcMu.Lock()
	s.ApplyRemoved(bcPieces(resp.GetChess()))
	s.PowerRemoveRandomCount = resp.GetPowerRemoveRandomCount()
	p.BcMu.Unlock()
	return p.BigChessState()
}

// BigChessEnd settles the run — the client sends it at winRound, on refused
// revival, or when the player (here: the AI) decides enough.
func (p *Player) BigChessEnd(timeout time.Duration) (map[string]any, bool) {
	p.BcMu.Lock()
	s := p.bcState
	p.BcMu.Unlock()
	if s == nil || s.ActivityID == 0 {
		return p.bcFail(errors.New("big-chess: no run yet"))
	}
	data, err := p.bcRoundTrip(BIG_CHESS_END_CMD, SERVER_BIG_CHESS_END, bcActivityBody(s.ActivityID), timeout)
	if err != nil {
		return p.bcFail(err)
	}
	var resp proto.ChineseChessEndResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		return p.bcFail(fmt.Errorf("big-chess: undecodable end answer: %w", err))
	}
	if resp.GetRet() != 0 {
		return p.bcFail(&bigChessError{Cmd: "end", Ret: resp.GetRet()})
	}
	p.BcMu.Lock()
	s.Settled = true
	s.Score = resp.GetScore()
	s.LastRank = resp.GetLastRank()
	s.NowRank = resp.GetNowRank()
	s.GameStatus = bigchess.StatusOver
	s.Touch()
	p.BcMu.Unlock()
	return p.BigChessState()
}

// ---- practice trio (free runs — engine playtest only) --------------------
//
// Practice answers are FREE (no daily starts, no ingots), which makes them
// the right transport for soak-testing the external engine. The enter-game
// answer is field-for-field identical to ranked (1-13, so the ranked message
// decodes it); move and end have practice-specific layouts. Practice move
// requests carry no power fields per the decoded client proto — we still send
// the full ranked body because protobuf skips unknown fields, while missing
// a proto2 REQUIRED field makes the server silently drop the packet.

func (p *Player) BigChessPracticeEnterGame(timeout time.Duration) (map[string]any, bool) {
	id, err := p.bcActiveID()
	if err != nil {
		return p.bcFail(err)
	}
	p.bcRequestConfig(id)
	data, err := p.bcRoundTrip(BIG_CHESS_PRACTICE_ENTER_GAME_CMD, SERVER_BIG_CHESS_PRACTICE_ENTER_GAME, bcActivityBody(id), timeout)
	if err != nil {
		return p.bcFail(err)
	}
	var resp proto.ChineseChessEnterGameResp // identical layout to the practice answer
	if err := pb.Unmarshal(data, &resp); err != nil {
		return p.bcFail(fmt.Errorf("big-chess practice: undecodable enter-game answer: %w", err))
	}
	if resp.GetRet() != 0 {
		return p.bcFail(&bigChessError{Cmd: "practice_enter_game", Ret: resp.GetRet()})
	}
	p.BcMu.Lock()
	p.bcState = bigchess.New(id) // practice always starts a fresh board
	s := p.bcState
	s.Board = bcPieces(resp.GetChessDataList())
	s.SetSelf(bcFirst(resp.GetSelfChessData()))
	s.HasSelf = resp.GetSelfChessData() != nil
	s.Score = resp.GetScore()
	s.Step = resp.GetRound()
	s.GameStatus = resp.GetGameStatus()
	s.NextTurn = bcTurnPtr(resp.GetNextRound())
	s.NextChess = bcPieces(resp.GetNextChess())
	s.PowerTimes = resp.GetPowerTimes()
	s.GuideOpen = resp.GetGuideOpen()
	s.Settled = false
	s.Touch()
	p.BcMu.Unlock()
	return p.BigChessState()
}

func (p *Player) BigChessPracticeMove(x, y int32, timeout time.Duration) (map[string]any, bool) {
	p.BcMu.Lock()
	s := p.bcState
	p.BcMu.Unlock()
	if s == nil || s.ActivityID == 0 {
		return p.bcFail(errors.New("big-chess practice: no run yet"))
	}
	if s.GameStatus != bigchess.StatusRunning {
		return p.bcFail(fmt.Errorf("big-chess practice: run not in play (gameStatus=%d)", s.GameStatus))
	}
	if x < 0 || x >= s.Width || y < 0 || y >= s.Height {
		return p.bcFail(fmt.Errorf("big-chess practice: (%d,%d) outside the %dx%d board", x, y, s.Width, s.Height))
	}
	if s.HasSelf && x == s.Self.X && y == s.Self.Y {
		return p.bcFail(errors.New("big-chess practice: destination is the player's own cell"))
	}
	data, err := p.bcRoundTrip(BIG_CHESS_PRACTICE_MOVE_CMD, SERVER_BIG_CHESS_PRACTICE_MOVE,
		bcMoveBody(s.ActivityID, x, y, false, false, false), timeout)
	if err != nil {
		return p.bcFail(err)
	}
	var resp proto.ChineseChessPracticeMoveResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		return p.bcFail(fmt.Errorf("big-chess practice: undecodable move answer: %w", err))
	}
	if resp.GetRet() != 0 {
		return p.bcFail(&bigChessError{Cmd: "practice_move", Ret: resp.GetRet()})
	}
	p.BcMu.Lock()
	s.SetBoard(bcPieces(resp.GetChess()))
	if resp.SelfChess != nil {
		s.SetSelf(bcFirst(resp.GetSelfChess()))
	}
	s.MoveChess = nil
	if resp.GetMoveChess() != nil {
		mc := bcFirst(resp.GetMoveChess())
		s.MoveChess = &mc
	}
	s.SetPreview(bcTurnPtr(resp.GetNextTurn()), bcPieces(resp.GetNextChess()))
	s.GameStatus = resp.GetGameStatus()
	s.Score = resp.GetScore()
	s.Step = resp.GetStep()
	s.NewPlayer = resp.GetNewPlayer()
	p.BcMu.Unlock()
	return p.BigChessState()
}

// BigChessPracticeEnd settles a practice run. Unlike ranked end it does NOT
// require step>=winRound — the free mode is always settleable — and its
// answer carries the survivor board with score at field 3.
func (p *Player) BigChessPracticeEnd(timeout time.Duration) (map[string]any, bool) {
	p.BcMu.Lock()
	s := p.bcState
	p.BcMu.Unlock()
	if s == nil || s.ActivityID == 0 {
		return p.bcFail(errors.New("big-chess practice: no run yet"))
	}
	data, err := p.bcRoundTrip(BIG_CHESS_PRACTICE_END_CMD, SERVER_BIG_CHESS_PRACTICE_END, bcActivityBody(s.ActivityID), timeout)
	if err != nil {
		return p.bcFail(err)
	}
	var resp proto.ChineseChessPracticeEndResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		return p.bcFail(fmt.Errorf("big-chess practice: undecodable end answer: %w", err))
	}
	if resp.GetRet() != 0 {
		return p.bcFail(&bigChessError{Cmd: "practice_end", Ret: resp.GetRet()})
	}
	p.BcMu.Lock()
	s.Settled = true
	s.Score = resp.GetScore()
	s.LastRank = resp.GetLastRank()
	s.NowRank = resp.GetNowRank()
	s.GameStatus = bigchess.StatusOver
	p.BcMu.Unlock()
	return p.BigChessState()
}

// ---- small converters -------------------------------------------------------

func bcPieces(list []*proto.ChessData) []bigchess.Piece {
	out := make([]bigchess.Piece, 0, len(list))
	for _, c := range list {
		out = append(out, bcFirst(c))
	}
	return out
}

func bcFirst(c *proto.ChessData) bigchess.Piece {
	if c == nil {
		return bigchess.Piece{}
	}
	return bigchess.Piece{ChessNo: c.GetChessNo(), ChessID: c.GetChessId(), X: c.GetX(), Y: c.GetY()}
}

// bcTurnPtr maps the generated optional counters to a pointer: countdown 0
// or absent both mean "no wave pending".
func bcTurnPtr(v int32) *int32 {
	if v == 0 {
		return nil
	}
	return &v
}

func (p *Player) bcFail(err error) (map[string]any, bool) {
	v := map[string]any{"error": err.Error()}
	// Structured ret so the MCP layer can report the refusal to the play
	// engine (/v1/rejections loop protection) without parsing prose.
	var be *bigChessError
	if errors.As(err, &be) {
		v["ret"] = be.Ret
	}
	return v, false
}
