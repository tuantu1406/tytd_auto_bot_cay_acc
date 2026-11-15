package gimap

// BallGVG (Thượng Cổ Di Tích) map rendering. The game UI has 13 fixed area
// slots; live ownership and per-camp population come from the room-info
// response. Unlike God Island, the response has no coordinates or room names,
// so this renderer uses a compact schematic layout and never invents names or
// movement connections. Text on the image is user-facing: game Vietnamese
// only, no protocol identifiers.

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// BallGVGError is a server refusal of command 21111, carrying the raw ret
// code so callers can react to specific codes (2100 = battle board closed)
// without string-matching the message.
type BallGVGError struct{ Ret int32 }

func (e *BallGVGError) Error() string {
	return fmt.Sprintf("server rejected BallGVG map request (ret=%d)", e.Ret)
}

// BallGVGErrBoardClosed is the code the server answers with when the Di Tích
// board is not accepting map requests right now.
const BallGVGErrBoardClosed = int32(2100)

// BallGVGCampMember is the number of players from one camp in a room.
type BallGVGCampMember struct {
	CampID int32 `json:"campId"`
	Num    int32 `json:"num"`
}

// BallGVGPlace is one live room record returned by command 21111.
type BallGVGPlace struct {
	PlaceID     int32               `json:"placeId"`
	SeizeCampID int32               `json:"seizeCampId"`
	CampMembers []BallGVGCampMember `json:"campMembers"`
}

// DecodeBallGVGPlaces decodes BallGVGGetPlaceInfoRespMsg without relying on
// the large generated game schema. Unknown fields are skipped, so it remains
// compatible if the server adds fields later.
func DecodeBallGVGPlaces(payload []byte) ([]BallGVGPlace, error) {
	var ret int32
	var places []BallGVGPlace
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		payload = payload[n:]
		switch num {
		case 1:
			if typ != protowire.VarintType {
				return nil, fmt.Errorf("BallGVG ret: wire type %d", typ)
			}
			v, n := protowire.ConsumeVarint(payload)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			ret, payload = int32(v), payload[n:]
		case 2:
			if typ != protowire.BytesType {
				return nil, fmt.Errorf("BallGVG place: wire type %d", typ)
			}
			v, n := protowire.ConsumeBytes(payload)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			place, err := decodeBallGVGPlace(v)
			if err != nil {
				return nil, err
			}
			places = append(places, place)
			payload = payload[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, payload)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			payload = payload[n:]
		}
	}
	if ret != 0 {
		return nil, &BallGVGError{Ret: ret}
	}
	if len(places) == 0 {
		return nil, errors.New("BallGVG map response carries no rooms")
	}
	sort.Slice(places, func(i, j int) bool { return places[i].PlaceID < places[j].PlaceID })
	return places, nil
}

func decodeBallGVGPlace(data []byte) (BallGVGPlace, error) {
	var place BallGVGPlace
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return place, protowire.ParseError(n)
		}
		data = data[n:]
		switch num {
		case 1, 2:
			if typ != protowire.VarintType {
				return place, fmt.Errorf("BallGVG room field %d: wire type %d", num, typ)
			}
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return place, protowire.ParseError(n)
			}
			if num == 1 {
				place.PlaceID = int32(v)
			} else {
				place.SeizeCampID = int32(v)
			}
			data = data[n:]
		case 3:
			if typ != protowire.BytesType {
				return place, fmt.Errorf("BallGVG campMember: wire type %d", typ)
			}
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return place, protowire.ParseError(n)
			}
			member, err := decodeBallGVGMember(v)
			if err != nil {
				return place, err
			}
			place.CampMembers = append(place.CampMembers, member)
			data = data[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return place, protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	sort.Slice(place.CampMembers, func(i, j int) bool {
		return place.CampMembers[i].CampID < place.CampMembers[j].CampID
	})
	return place, nil
}

func decodeBallGVGMember(data []byte) (BallGVGCampMember, error) {
	var member BallGVGCampMember
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return member, protowire.ParseError(n)
		}
		data = data[n:]
		if (num == 1 || num == 2) && typ == protowire.VarintType {
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return member, protowire.ParseError(n)
			}
			if num == 1 {
				member.CampID = int32(v)
			} else {
				member.Num = int32(v)
			}
			data = data[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, data)
		if n < 0 {
			return member, protowire.ParseError(n)
		}
		data = data[n:]
	}
	return member, nil
}

type ballGVGPoint struct{ x, y float64 }

// Fixed presentation slots corresponding to the bundled area1..area13 UI
// nodes. These are schematic positions: the protocol defines no paths or map
// coordinates, so the renderer deliberately draws no connecting roads.
var ballGVGSlots = [...]ballGVGPoint{
	{0.20, 0.20}, {0.50, 0.14}, {0.80, 0.20},
	{0.20, 0.39}, {0.50, 0.34}, {0.80, 0.39},
	{0.10, 0.59}, {0.37, 0.54}, {0.63, 0.54}, {0.90, 0.59},
	{0.20, 0.76}, {0.50, 0.72}, {0.80, 0.76},
}

var ballGVGCampColors = map[int32]rgba{
	1: {R: 236, G: 89, B: 93, A: 255},
	2: {R: 73, G: 159, B: 235, A: 255},
	3: {R: 76, G: 194, B: 128, A: 255},
}

// ballGVGGeom is the shared placement maths for the PNG and the JSON
// overlay: the SAME slot transform, so a click target placed by percentage
// lands on the drawn circle.
type ballGVGGeom struct {
	width, height int
	mapTop        float64
	mapHeight     float64
	px            func(float64) int
}

func ballGVGGeomFor(width int) ballGVGGeom {
	if width < 600 {
		width = 600
	}
	g := ballGVGGeom{width: width, height: width * 4 / 5}
	scale := float64(width) / 900
	g.px = func(v float64) int { return max(1, int(v*scale+0.5)) }
	g.mapTop = float64(g.px(78))
	g.mapHeight = float64(g.height - g.px(142))
	return g
}

// slot returns the pixel centre + radius of the i-th room (sorted by id).
func (g ballGVGGeom) slot(i int) (x, y, radius int) {
	var slot ballGVGPoint
	if i < len(ballGVGSlots) {
		slot = ballGVGSlots[i]
	} else {
		// Defensive fallback for a future server config with >13 places.
		j := i - len(ballGVGSlots)
		slot = ballGVGPoint{x: float64(j%5+1) / 6, y: 0.88 + float64(j/5)*0.12}
	}
	x = int(slot.x * float64(g.width))
	y = int(g.mapTop + slot.y*g.mapHeight)
	return x, y, g.px(31)
}

// RenderBallGVG converts a live room-info payload into a compact PNG. The
// stock package exposes 13 slots; extra server rooms are included by
// continuing onto additional rows rather than silently discarding them.
func RenderBallGVG(payload []byte, width int) ([]byte, error) {
	places, err := DecodeBallGVGPlaces(payload)
	if err != nil {
		return nil, err
	}
	g := ballGVGGeomFor(width)
	img := image.NewRGBA(image.Rect(0, 0, g.width, g.height))
	fillRect(img, img.Bounds(), background)
	ttf, err := loadFont()
	if err != nil {
		return nil, fmt.Errorf("font: %w", err)
	}
	p := newPainter(img, ttf)
	px := g.px

	_ = p.text(px(28), px(42), "THƯỢNG CỔ DI TÍCH", px(27), white, false)
	_ = p.text(px(29), px(67), fmt.Sprintf("%d cứ điểm  •  chủ và quân mỗi phe hiện tại", len(places)), px(13), dimText, false)

	for i, place := range places {
		x, y, radius := g.slot(i)
		ownerColor := gray
		if c, ok := ballGVGCampColors[place.SeizeCampID]; ok {
			ownerColor = c
		}
		fillCircle(img, x, y, radius+px(2), white)
		fillCircle(img, x, y, radius, ownerColor)
		_ = p.text(x, y+px(9), fmt.Sprintf("%d", place.PlaceID), px(25), rgba{R: 5, G: 13, B: 25, A: 255}, true)

		owner := "Vô chủ"
		if place.SeizeCampID > 0 {
			owner = fmt.Sprintf("Phe %d", place.SeizeCampID)
		}
		_ = p.text(x, y+radius+px(19), owner, px(12), white, true)
		_ = p.text(x, y+radius+px(35), ballGVGMemberLabel(place.CampMembers), px(10), dimText, true)
	}

	legendY := g.height - px(43)
	legendX := px(30)
	for campID := int32(1); campID <= 3; campID++ {
		fillCircle(img, legendX, legendY, px(6), ballGVGCampColors[campID])
		_ = p.text(legendX+px(12), legendY+px(4), fmt.Sprintf("Phe %d", campID), px(11), dimText, false)
		legendX += px(105)
	}

	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// LayoutBallGVG answers the interactive overlay for the same PNG: room
// centres in output-pixel coordinates under the identical slot transform,
// owner as the game-facing "Phe N" label. There is no per-room battle flag in
// the live payload, so Fight stays false for every room.
func LayoutBallGVG(payload []byte, width int) (*MapLayout, error) {
	places, err := DecodeBallGVGPlaces(payload)
	if err != nil {
		return nil, err
	}
	g := ballGVGGeomFor(width)
	out := &MapLayout{Width: g.width, Height: g.height, Cities: make([]CityPoint, 0, len(places))}
	for i, place := range places {
		x, y, radius := g.slot(i)
		owner := ""
		if place.SeizeCampID > 0 {
			owner = fmt.Sprintf("Phe %d", place.SeizeCampID)
		}
		out.Cities = append(out.Cities, CityPoint{
			ID: uint64(place.PlaceID), X: x, Y: y, R: radius, Owner: owner,
		})
	}
	return out, nil
}

func ballGVGMemberLabel(members []BallGVGCampMember) string {
	if len(members) == 0 {
		return "trống"
	}
	parts := make([]string, 0, len(members))
	for _, member := range members {
		parts = append(parts, fmt.Sprintf("P%d:%d", member.CampID, member.Num))
	}
	return strings.Join(parts, "  ")
}
