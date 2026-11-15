package gimap

// Thương Minh Yêu Vực (CelestialSeaWar 349) — union-war challenge board.
//
// The game's challenge scene lays rival union bases out by positionRank
// (SeaWarUnionChallengeMsg.positionRank, fallback rank) inside its own FGUI
// layout — the real coordinates again live in the client scene binary, so
// SeaWarWarAnchor is our own honest grid: a brick-offset 5-column layout on
// a 2000×1200 sea canvas, rank 1 top-left. The page draws each base as a
// HOUSE (roof + body + door, tinted with the union palette color) — this
// scene has no mined CDN art, so it ships schematic (same precedent as the
// gi/bacminh schematic layers). Tapping a house orders the whole event fleet
// to attack that union (root package: webSeaWarWarOrder → swWarOrderOne);
// the server never told us a local "attackable" bit other than
// canBattleUnionIds, which rides here as Ready — display hint only, the
// order path NEVER gates on it (owner 2026-11-11: "không cần check xem có
// lượt hay không").

import (
	"tytd_go/internal/proto"
)

// SeaWarWarW/H is the schematic battle canvas.
const (
	SeaWarWarW      = 2000
	SeaWarWarH      = 1200
	SeaWarWarCols   = 5
	SeaWarWarCellW  = 380.0
	SeaWarWarCellH  = 210.0
	SeaWarWarHouseW = 200 // house draw width in world px (browser + preview)
)

// SeaWarWarAnchor is the house slot for a 1-based position (positionRank,
// fallback rank). Brick-offset rows keep neighbouring ranks from aligning
// into a boring lattice; positions past the painted rows keep stacking.
func SeaWarWarAnchor(pos int) (float64, float64) {
	if pos < 1 {
		pos = 1
	}
	row := (pos - 1) / SeaWarWarCols
	col := (pos - 1) % SeaWarWarCols
	x := 220.0 + float64(col)*SeaWarWarCellW
	if row%2 == 1 {
		x += SeaWarWarCellW / 2
	}
	y := 190.0 + float64(row)*SeaWarWarCellH
	return x, y
}

// SwNode is one union base on the war board. Ready mirrors membership in
// canBattleUnionIds (display only — see the file header for the order law).
type SwNode struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	SV     int64   `json:"sv,omitempty"`
	Master string  `json:"master,omitempty"`
	Flag   int32   `json:"flag,omitempty"`
	Rank   int     `json:"rank,omitempty"`
	Pos    int     `json:"pos"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	CI     int     `json:"ci"`
	Color  string  `json:"color,omitempty"`
	Self   bool    `json:"self,omitempty"`
	Ready  bool    `json:"ready,omitempty"`
	// Atk: recent challenge pushes against THIS union (newest first) — the
	// red-ring layer, LHBC idiom.
	Atk []LhAtk `json:"atk,omitempty"`
}

// SwFrame is the which="seawarwar" extras the /seawarwar mini app draws.
type SwFrame struct {
	Nodes      []SwNode `json:"nodes"`
	FreeNormal int32    `json:"free,omitempty"` // display only, never a gate
}

// SeaWarWarStream folds a 234931 challenge-list answer into a StreamMap.
// selfUnion = the pulling account's union (0 = guildless → nothing is
// highlighted). fights = the fight ring (challenge pushes) for the red
// rings; canBattle = SeaWarChallengeListResp.canBattleUnionIds.
func SeaWarWarStream(msgs []*proto.SeaWarUnionChallengeMsg, canBattle []int64,
	selfUnion int64, nowMilli int64, fights []LhFightIn) *StreamMap {
	byTarget := map[int64][]LhAtk{}
	for _, f := range fights {
		ago := (nowMilli - f.AtMilli) / 1000
		if ago < 0 || ago > 1800 || len(byTarget[f.TargetID]) >= 5 {
			continue
		}
		byTarget[f.TargetID] = append([]LhAtk{{N: f.Name, Ago: ago}}, byTarget[f.TargetID]...)
	}
	battle := map[int64]bool{}
	for _, id := range canBattle {
		battle[id] = true
	}
	f := &SwFrame{}
	for _, m := range msgs {
		b := m.GetBase()
		if b == nil || b.GetUnionId() == 0 {
			continue
		}
		pos := int(m.GetPositionRank())
		if pos < 1 {
			pos = int(m.GetRank())
		}
		if pos < 1 {
			pos = len(f.Nodes) + 1
		}
		ax, ay := SeaWarWarAnchor(pos)
		nid := b.GetUnionId()
		f.Nodes = append(f.Nodes, SwNode{
			ID: nid, Name: b.GetName(), SV: b.GetServerId(), Master: b.GetMasterName(),
			Flag: b.GetFlag(), Rank: int(m.GetRank()), Pos: pos,
			X: ax / SeaWarWarW, Y: ay / SeaWarWarH,
			CI:    int(nid % 12),
			Color: LuanHoiColorHex(nid),
			Self:  selfUnion != 0 && nid == selfUnion,
			Ready: battle[nid],
			Atk:   byTarget[nid],
		})
	}
	return &StreamMap{Which: StreamSeaWarWar, Aspect: float64(SeaWarWarW) / float64(SeaWarWarH), Cities: nil, Sw: f}
}

// seaWarWarStatic ships the empty board frame (grid dots) so the events card
// can paint the layout before the first live list answer arrives.
func seaWarWarStatic() (*StaticMap, error) {
	sm := &StaticMap{
		Which: StreamSeaWarWar, Aspect: float64(SeaWarWarW) / float64(SeaWarWarH),
		WorldW: SeaWarWarW, WorldH: SeaWarWarH,
	}
	for i := 1; i <= SeaWarWarCols*4; i++ {
		x, y := SeaWarWarAnchor(i)
		sm.Cities = append(sm.Cities, StaticCity{
			ID: uint64(i), X: x / SeaWarWarW, Y: y / SeaWarWarH,
		})
	}
	return sm, nil
}
