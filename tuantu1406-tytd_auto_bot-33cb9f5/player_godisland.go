package tytd_go

import (
	"fmt"
	"log"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// God Island (đảo bồng lai) support for regular players, driven by the
// /bonglai_map command: capture the activity id from the server's activity
// pushes and answer one-shot minimap requests. The auto-play side of the
// event lives in player_event.go (EventPlayer).
//
// Protocol reference: ../remoteBlessFrog/STATIC_ANALYSIS.md and
// GOD_ISLAND_PROTOCOL.md; message types live in internal/proto/tytd.proto.
// giActivityTypeGodIsland is declared in player_event.go (same package).

// giHandleActivityList parses PushActivityList/SyncActivityList, both laid out
// as { repeated ActivityMainConfig mainConfig = 1 } with each entry carrying
// { activityId = 1, type = 2 }. It records the activityIds of BOTH God Island
// variants (68 = Bồng Lai, 333 = peak Thiên Ma); the server pushes the full
// list right after login.
func (p *Player) giHandleActivityList(data []byte) {
	var push proto.PushActivityList
	if err := pb.Unmarshal(data, &push); err != nil {
		log.Printf("[%s] [GodIsland] bad activity list push: %v", p.Username, err)
		return
	}
	var normalID, peakID int32
	for _, cfg := range push.GetMainConfig() {
		switch cfg.GetType() {
		case giActivityTypeGodIsland:
			normalID = cfg.GetActivityId()
		case giActivityTypePeakIsland:
			peakID = cfg.GetActivityId()
		}
	}
	p.GiMu.Lock()
	changedNormal := normalID != 0 && p.GiActivityID != normalID
	changedPeak := peakID != 0 && p.GiPeakActivityID != peakID
	if normalID != 0 {
		p.GiActivityID = normalID
	}
	if peakID != 0 {
		p.GiPeakActivityID = peakID
	}
	p.GiMu.Unlock()
	if changedNormal {
		log.Println(p.Username, "[GodIsland] activity", normalID)
	}
	if changedPeak {
		log.Println(p.Username, "[GodIsland] thiên ma activity", peakID)
	}
}

// giDeliverMiniMap hands a SERVER_GOD_ISLAND_MINIMAP payload to the pending
// /bonglai_map request, if one is outstanding. It never blocks the reader
// goroutine: with no listener (or a stale full channel) the payload is dropped.
func (p *Player) giDeliverMiniMap(data []byte) {
	p.GiMu.Lock()
	ch := p.GiPending
	p.GiMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// RequestGodIslandMiniMap asks the server for our bracket's minimap (206834)
// on ONE event variant (GIEventNormal = Bồng Lai, GIEventPeak = Thiên Ma) and
// returns the raw GodIslandGameMiniMapInfoRespMsg payload, or an error after
// timeout / disconnect. Both variants answer the same incoming type, so only
// one request may be outstanding per player.
func (p *Player) RequestGodIslandMiniMap(which int, timeout time.Duration) ([]byte, error) {
	p.GiMu.Lock()
	if p.GiPending != nil {
		p.GiMu.Unlock()
		return nil, fmt.Errorf("có yêu cầu map đang chờ, thử lại sau nhé")
	}
	activityID, eventName := p.GiActivityID, "đảo bồng lai"
	if which == GIEventPeak {
		activityID, eventName = p.GiPeakActivityID, "đảo thiên ma"
	}
	if activityID == 0 {
		p.GiMu.Unlock()
		return nil, fmt.Errorf("server chưa bật sự kiện %s (hoặc chưa nhận được activity id)", eventName)
	}
	ch := make(chan []byte, 1)
	p.GiPending = ch
	p.GiMu.Unlock()
	defer func() {
		p.GiMu.Lock()
		if p.GiPending == ch {
			p.GiPending = nil
		}
		p.GiMu.Unlock()
	}()

	// floor/group are optional: omit them and the server returns our bracket.
	mapMSG, _ := pb.Marshal(&proto.GodIslandGameMiniMapInfoReqMsg{ActivityId: activityID})
	// ctx-aware send: a half-dead connection (writer goroutine exited,
	// writeChan full) would block p.write forever and freeze the web map
	// streamer loop that calls this.
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
