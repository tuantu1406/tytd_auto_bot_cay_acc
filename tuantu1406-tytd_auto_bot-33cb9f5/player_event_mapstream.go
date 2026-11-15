package tytd_go

// Event-account one-shot map pulls for the web streamer. When the leader's
// own session is offline the /events page streams Bồng Lai/Thiên Ma and Di
// Tích through a RANDOM ONLINE event account — the fleet members that
// actually sit inside the battlefield. Same seat pattern as the Cửu U pulls
// (player_event_holyland.go): a mutex-guarded one-shot channel, a ctx-aware
// wire send (a half-dead connection must never freeze mapStreamLoop), and
// delivery from eventWsHandler.
//
// Thương Đồ is deliberately absent: event accounts never sail, so its stream
// keeps the leader-first / sticky-member flow over regular sessions only.

import (
	"fmt"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

// giDeliverMiniMap routes a God Island minimap answer (1ab2) into the
// waiting seat (reader goroutine; mirrors Player.giDeliverMiniMap). Never
// blocks: no seat or a stale full channel simply drops the payload.
func (p *EventPlayer) giDeliverMiniMap(data []byte) {
	p.GodIslandMu.Lock()
	ch := p.giMiniPending
	p.GodIslandMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// eventGiEligible is the stream-pull gate for one island variant: announced,
// not kicked out, and the one-shot seat free — so a pull never collides with
// the account's own island traffic (and vice versa).
func eventGiEligible(p *EventPlayer, which int) bool {
	p.GodIslandMu.Lock()
	defer p.GodIslandMu.Unlock()
	if p.giMiniPending != nil {
		return false
	}
	ev := p.GiEvents[which]
	return ev != nil && ev.ActivityID != 0 && !ev.LeftOut
}

// RequestGodIslandMiniMap pulls the bracket minimap (206834) for ONE variant
// on an event account — the exact request Player.RequestGodIslandMiniMap
// speaks, with the event seat semantics (busy refusal, timeout, ctx).
func (p *EventPlayer) RequestGodIslandMiniMap(which int, timeout time.Duration) ([]byte, error) {
	p.GodIslandMu.Lock()
	if p.giMiniPending != nil {
		p.GodIslandMu.Unlock()
		return nil, fmt.Errorf("có yêu cầu map đang chờ, thử lại sau nhé")
	}
	var activityID int32
	if which >= 0 && which < giEventCount && p.GiEvents[which] != nil {
		activityID = p.GiEvents[which].ActivityID
	}
	if activityID == 0 {
		p.GodIslandMu.Unlock()
		return nil, fmt.Errorf("server chưa bật sự kiện (chưa có activity id)")
	}
	ch := make(chan []byte, 1)
	p.giMiniPending = ch
	p.GodIslandMu.Unlock()
	defer func() {
		p.GodIslandMu.Lock()
		if p.giMiniPending == ch {
			p.giMiniPending = nil
		}
		p.GodIslandMu.Unlock()
	}()

	mapMSG, _ := pb.Marshal(&proto.GodIslandGameMiniMapInfoReqMsg{ActivityId: activityID})
	select {
	case p.writeChan <- pack.Pack(GOD_ISLAND_MINIMAP_CMD, mapMSG, p.UID):
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("server không trả lời map, thử lại sau")
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
}

// RequestGodIslandCityInfo pulls the standing-people view (206823 → 1aa7)
// for ONE variant: the client's FIXED 16-city home-block vec (the only
// shape the server answers — a 40-city minimap ask returns ret=6833) plus
// this slot's folded floor/group. Routed through giSend so the 1aa7 answer
// attributes via the FIFO even when both islands are open; the seat makes
// concurrent pulls impossible (same one-shot rule as the minimap seat).
func (p *EventPlayer) RequestGodIslandCityInfo(which int, timeout time.Duration) ([]byte, error) {
	p.GodIslandMu.Lock()
	if p.giCityPending != nil {
		p.GodIslandMu.Unlock()
		return nil, fmt.Errorf("có yêu cầu thành đang chờ, thử lại sau nhé")
	}
	if which < 0 || which >= giEventCount || p.GiEvents[which] == nil {
		p.GodIslandMu.Unlock()
		return nil, fmt.Errorf("server chưa bật sự kiện (chưa có activity id)")
	}
	ev := p.GiEvents[which]
	if ev.ActivityID == 0 {
		p.GodIslandMu.Unlock()
		return nil, fmt.Errorf("server chưa bật sự kiện (chưa có activity id)")
	}
	ch := make(chan []byte, 1)
	p.giCityPending = ch
	// giSend takes GodIslandMu — release it first; the seat above keeps this
	// account's city pulls serialised regardless.
	p.GodIslandMu.Unlock()
	defer func() {
		p.GodIslandMu.Lock()
		if p.giCityPending == ch {
			p.giCityPending = nil
		}
		p.GodIslandMu.Unlock()
	}()

	p.giSend(ev, GOD_ISLAND_CITY_INFO_CMD, SERVER_GOD_ISLAND_CITY_INFO,
		&proto.GodIslandGameCityInfoReqMsg{
			ActivityId: ev.ActivityID,
			CityVec:    giCityVec,
			FloorId:    ev.Floor,
			GroupId:    ev.Group,
		})
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("server không trả lời danh sách người, thử lại sau")
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
}

// bgDeliverMap routes a Di Tích room-summary answer (0457) into the seat.
// The stay loop sends 21111 itself (bgSend) and bgHandlePlaceInfo keeps the
// state fresh either way — feeding the seat from the same frame is safe:
// with no seat pending the payload is dropped.
func (p *EventPlayer) bgDeliverMap(data []byte) {
	p.BgMu.Lock()
	ch := p.bgMiniPending
	p.BgMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// eventBgEligible is the Di Tích stream-pull gate (announced + window active
// via BallGVGEventOpen, seat free).
func eventBgEligible(p *EventPlayer) bool {
	p.BgMu.Lock()
	busy := p.bgMiniPending != nil
	p.BgMu.Unlock()
	return !busy && p.BallGVGEventOpen()
}

// RequestBallGVGMap pulls the full room summary (21111 → 0457) on an event
// account — the event twin of Player.RequestBallGVGMap.
func (p *EventPlayer) RequestBallGVGMap(timeout time.Duration) ([]byte, error) {
	p.BgMu.Lock()
	if p.bgMiniPending != nil {
		p.BgMu.Unlock()
		return nil, fmt.Errorf("có yêu cầu map Thượng Cổ Di Tích đang chờ, thử lại sau nhé")
	}
	activityID := int32(0)
	if p.bg != nil {
		activityID = p.bg.ActivityID
	}
	if activityID == 0 {
		p.BgMu.Unlock()
		return nil, fmt.Errorf("server chưa bật sự kiện Thượng Cổ Di Tích (hoặc chưa nhận được activity id)")
	}
	ch := make(chan []byte, 1)
	p.bgMiniPending = ch
	p.BgMu.Unlock()
	defer func() {
		p.BgMu.Lock()
		if p.bgMiniPending == ch {
			p.bgMiniPending = nil
		}
		p.BgMu.Unlock()
	}()

	// BallGVGGetPlaceInfoReqMsg { activityId } — same hand-built body as the
	// Player request.
	msg := protowire.AppendTag(nil, 1, protowire.VarintType)
	msg = protowire.AppendVarint(msg, uint64(activityID))
	select {
	case p.writeChan <- pack.Pack(BALL_GVG_GET_PLACE_CMD, msg, p.UID):
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("server không trả lời map Thượng Cổ Di Tích, thử lại sau")
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
}
