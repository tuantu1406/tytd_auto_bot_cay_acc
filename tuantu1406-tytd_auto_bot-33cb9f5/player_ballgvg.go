package tytd_go

import (
	"fmt"
	"log"
	"time"

	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

const activityTypeBallGVG int32 = 14

// bgClosedCooldown is how long the web map streamer trusts a ret=2100
// refusal after which it stops pulling the Di Tích board: the activity list
// can advertise a window that the server still refuses (between battle
// rounds), and the refusal is the only authoritative signal.
const bgClosedCooldown = 2 * time.Minute

// bgMarkClosed records a 2100 refusal; mapStreamerEligible holds off until
// the cooldown passes. A later accepted pull clears it immediately.
func (p *Player) bgMarkClosed(now time.Time) {
	p.BgMu.Lock()
	p.BgClosedUntil = now.Add(bgClosedCooldown)
	p.BgMu.Unlock()
}

func (p *Player) bgClearClosed() {
	p.BgMu.Lock()
	p.BgClosedUntil = time.Time{}
	p.BgMu.Unlock()
}

func (p *Player) bgClosed(now time.Time) bool {
	p.BgMu.Lock()
	defer p.BgMu.Unlock()
	return now.Before(p.BgClosedUntil)
}

// bgHandleActivityList learns the live event instance id. Type 14 is the
// stable system/activity type; it must never be sent as activityId.
func (p *Player) bgHandleActivityList(data []byte) {
	var push proto.PushActivityList
	if err := pb.Unmarshal(data, &push); err != nil {
		log.Printf("[%s] [BallGVG] bad activity list push: %v", p.Username, err)
		return
	}
	var activityID int32
	for _, cfg := range push.GetMainConfig() {
		if cfg.GetType() == activityTypeBallGVG {
			activityID = cfg.GetActivityId()
			break
		}
	}
	if activityID == 0 {
		return
	}
	p.BgMu.Lock()
	changed := p.BgActivityID != activityID
	p.BgActivityID = activityID
	p.BgMu.Unlock()
	if changed {
		log.Println(p.Username, "[BallGVG] activity", activityID)
	}
}

func (p *Player) bgDeliverMap(data []byte) {
	p.BgMu.Lock()
	ch := p.BgPending
	p.BgMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// RequestBallGVGMap requests the full room summary with 21111 and returns the
// raw BallGVGGetPlaceInfoRespMsg payload delivered on transport 0457.
func (p *Player) RequestBallGVGMap(timeout time.Duration) ([]byte, error) {
	p.BgMu.Lock()
	if p.BgPending != nil {
		p.BgMu.Unlock()
		return nil, fmt.Errorf("có yêu cầu map Thượng Cổ Di Tích đang chờ, thử lại sau nhé")
	}
	activityID := p.BgActivityID
	if activityID == 0 {
		p.BgMu.Unlock()
		return nil, fmt.Errorf("server chưa bật sự kiện Thượng Cổ Di Tích (hoặc chưa nhận được activity id)")
	}
	ch := make(chan []byte, 1)
	p.BgPending = ch
	p.BgMu.Unlock()
	defer func() {
		p.BgMu.Lock()
		if p.BgPending == ch {
			p.BgPending = nil
		}
		p.BgMu.Unlock()
	}()

	// BallGVGGetPlaceInfoReqMsg { activityId = 1 }.
	msg := protowire.AppendTag(nil, 1, protowire.VarintType)
	msg = protowire.AppendVarint(msg, uint64(activityID))
	// ctx-aware send: a half-dead connection (writer goroutine exited,
	// writeChan full) would block p.write forever and freeze the web map
	// streamer loop that calls this.
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
