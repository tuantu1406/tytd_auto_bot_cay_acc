// Package bigchess holds the shared data model and the AI-facing reference
// texts for the in-game "Big Chess" puzzle (activity type 341). The bot only
// relays this game: it tracks the server's authoritative board and forwards
// the AI's validated actions through the MCP tools. All game knowledge lives
// in the two embedded documents, which the MCP server serves as
// bigchess://rules and bigchess://protocol — the AI reasons about legal
// moves itself; there is deliberately no bot-side solver.
package bigchess

import (
	_ "embed"
	"time"
)

//go:embed rules.md
var Rules string

//go:embed protocol.md
var ProtocolDoc string

// Document resource URIs served over MCP.
const (
	RulesURI    = "bigchess://rules"
	ProtocolURI = "bigchess://protocol"
)

// Piece types (chessId). The player is the single red piece 1008.
const (
	IDPawn     = int32(1001)
	IDAdvisor  = int32(1002)
	IDElephant = int32(1003)
	IDCannon   = int32(1004)
	IDKnight   = int32(1005)
	IDRook     = int32(1006)
	IDGeneral  = int32(1007)
	IDPlayer   = int32(1008)
)

// GameStatus values (ChineseChess*.gameStatus).
const (
	StatusRunning = int32(0)
	StatusRevive  = int32(1)
	StatusOver    = int32(2)
	StatusPass    = int32(3)
)

// Piece is one board entry; ChessNo is the instance key, ChessID the type.
type Piece struct {
	ChessNo int32 `json:"chessNo"`
	ChessID int32 `json:"chessId"`
	X       int32 `json:"x"`
	Y       int32 `json:"y"`
}

// State is the authoritative run snapshot assembled from the ranked-mode
// responses (234101 lobby fields, 234102 full board, 234103 per-move
// updates) plus the 21003 config tables. Every scalar mirrors the server's
// last answer; *Count fields are CONSUMED uses, never remaining.
type State struct {
	ActivityID int32   `json:"activityId"`
	Width      int32   `json:"width"`
	Height     int32   `json:"height"`
	Board      []Piece `json:"board"`
	Self       Piece   `json:"self"`
	HasSelf    bool    `json:"hasSelf"`

	Score      int64 `json:"score"`
	Step       int32 `json:"step"`
	GameStatus int32 `json:"gameStatus"`

	NextTurn  *int32  `json:"nextTurn,omitempty"`
	NextChess []Piece `json:"nextChess,omitempty"`
	MoveChess *Piece  `json:"moveChess,omitempty"` // enemy moved on the last turn

	PowerTimes             int32 `json:"powerTimes"`
	ItemReviveCount        int32 `json:"itemReviveCount"`
	IngotReviveCount       int32 `json:"ingotReviveCount"`
	PassReviveCount        int32 `json:"passReviveCount"`
	PowerOnCount           int32 `json:"powerOnCount"`
	PowerCrossCount        int32 `json:"powerCrossCount"`
	PowerRemoveTypeCount   int32 `json:"powerRemoveTypeCount"`
	PowerRemoveRandomCount int32 `json:"powerRemoveRandomCount"`
	FreeCount              int32 `json:"freeCount"`
	MaxScore               int32 `json:"maxScore"`
	GuideOpen              bool  `json:"guideOpen"`
	NewPlayer              bool  `json:"newPlayer"`

	// Last settlement answer (234104), kept until the next EnterGame.
	Settled  bool  `json:"settled,omitempty"`
	LastRank int32 `json:"lastRank,omitempty"`
	NowRank  int32 `json:"nowRank,omitempty"`

	// Config learned from the 21003 detail: capture values per chessId and
	// the raw BIGCHESS_* common-config params (fallbacks are the client's,
	// so both are passed through and the AI sees live values when known).
	PieceScores map[int32]int32   `json:"pieceScores,omitempty"`
	Params      map[string]string `json:"params,omitempty"`

	Version   int64 `json:"stateVersion"`
	UpdatedMS int64 `json:"updatedAtMs"`
}

// New returns an empty snapshot with the documented client fallbacks as
// dimensions until the live BIGCHESS_BOARD_XY param is learned.
func New(activityID int32) *State {
	return &State{
		ActivityID: activityID,
		Width:      8, Height: 9, // BIGCHESS_BOARD_XY fallback "8*9"
		PieceScores: map[int32]int32{},
		Params:      map[string]string{},
	}
}

// Touch bumps the change counter; every mutation funnels through here so an
// AI can prove it is acting on the latest state (the Python bridge's
// stateVersion contract).
func (s *State) Touch() {
	s.Version++
	s.UpdatedMS = time.Now().UnixMilli()
}

// SetBoard replaces the survivor set (MOVE/EnterGame/UseItem semantics).
func (s *State) SetBoard(pieces []Piece) {
	s.Board = pieces
	s.Touch()
}

// ApplyRemoved drops the given pieces from the board (UseRandomResp carries
// the REMOVED list, not a survivor set).
func (s *State) ApplyRemoved(pieces []Piece) {
	drop := make(map[int32]bool, len(pieces))
	for _, p := range pieces {
		drop[p.ChessNo] = true
	}
	kept := s.Board[:0]
	for _, p := range s.Board {
		if !drop[p.ChessNo] {
			kept = append(kept, p)
		}
	}
	s.Board = kept
	s.Touch()
}

// SetSelf moves the player piece anchor.
func (s *State) SetSelf(p Piece) {
	s.Self, s.HasSelf = p, true
	s.Touch()
}

// SetPreview stores the next spawn wave + countdown.
func (s *State) SetPreview(nextTurn *int32, wave []Piece) {
	s.NextTurn, s.NextChess = nextTurn, wave
	s.Touch()
}

// WinRound returns the live BIGCHESS_WIN_ROUND step (client fallback 300).
func (s *State) WinRound() int32 {
	if v, ok := s.Params["BIGCHESS_WIN_ROUND"]; ok {
		var n int32
		for _, c := range v {
			if c < '0' || c > '9' {
				return 300
			}
			n = n*10 + int32(c-'0')
		}
		if n > 0 {
			return n
		}
	}
	return 300
}
