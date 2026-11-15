package tytd_go

import (
	"errors"
	"fmt"
	"log"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Sky Trade (Tiên Vực Thương Đồ) auto-report for every regular account.
//
// The event lives in the activity list as system type 150; the live
// activityId comes from p.ActList (login push / hourly pull — see
// player_activity.go), never the type itself. What we do is one piece of
// trade intelligence: publishing the scarce goods of our ship's CURRENT
// port for the whole alliance to see. The server derives that port from
// session state, so each report is the client's own three-step sequence:
// open session → load map state → report. Answers carry ret and are
// logged internally only; refusal codes are the server's authority on
// whether the daily window is open — we never fight it.
//
// Two triggers, per the request:
//   - login while the event is happening: first tick after the login
//     push has settled;
//   - event start while already online: the loop sleeps to the stored
//     beginTime and fires there.
// One report per activity instance per login; a new login re-reports.

const (
	skyTradeActivityType = 150
	skyTradeSettle       = 20 * time.Second // let the login list push land
	skyTradeStepGap      = 400 * time.Millisecond
	skyTradeIdleRecheck  = 10 * time.Minute // local-only recheck with nothing due
	skyTradeMinSleep     = time.Second
)

// skyTradePlan is the whole decision from one snapshot: which ACTIVE
// instances (beginTime ≤ now < endTime) have not been reported this
// login, and when to look again — exactly at the earliest future
// beginTime, else the idle recheck. Show/reward phases never report;
// the server owns the daily open-window rules.
func skyTradePlan(entries []activityEntry, now time.Time, done map[int32]bool) (report []int32, wake time.Time) {
	ms := now.UnixMilli()
	wake = now.Add(skyTradeIdleRecheck)
	for _, e := range entries {
		switch {
		case ms >= e.BeginTime && ms < e.EndTime:
			if !done[e.ActivityID] {
				report = append(report, e.ActivityID)
			}
		case e.BeginTime > ms && e.BeginTime < wake.UnixMilli():
			wake = time.UnixMilli(e.BeginTime)
		}
	}
	return report, wake
}

// skyTradeReport fires the enter → enter-map → report sequence, paced
// like every other send loop here. Fire-and-read: the answers land on
// the reader (stHandleFrame) which logs ret.
func (p *Player) skyTradeReport(activityID int32) {
	body, err := skyTradeActivityBody(activityID)
	if err != nil {
		return
	}
	for _, cmd := range []string{SKY_TRADE_ENTER_CMD, SKY_TRADE_ENTER_MAP_CMD, SKY_TRADE_REPORT_GOODS_CMD} {
		p.write(pack.Pack(cmd, body, p.UID))
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(skyTradeStepGap):
		}
	}
}

// skyTradeReportLoop is the per-login trigger schedule.
func (p *Player) skyTradeReportLoop() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(skyTradeSettle):
	}
	done := map[int32]bool{} // per login: each activity instance reports once
	for {
		report, wake := skyTradePlan(p.ActList.byType(skyTradeActivityType), time.Now(), done)
		for _, id := range report {
			p.skyTradeReport(id)
			done[id] = true
		}
		d := time.Until(wake)
		if d < skyTradeMinSleep {
			d = skyTradeMinSleep
		}
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

// stHandleFrame consumes the three answers on the reader. Success of the
// report is the interesting one (which port's scarce goods went public);
// everything else only logs when the server says no.
func (p *Player) stHandleFrame(msgType string, data []byte) {
	switch msgType {
	case SERVER_SKY_TRADE_ENTER:
		var rsp proto.SkyTradeEnterActivityResp
		if err := pb.Unmarshal(data, &rsp); err == nil && rsp.GetRet() != 0 {
			log.Printf("[%s] [SkyTrade] enter refused (code %d)", p.Username, rsp.GetRet())
		}
	case SERVER_SKY_TRADE_ENTER_MAP:
		var rsp proto.SkyTradeEnterResp
		if err := pb.Unmarshal(data, &rsp); err == nil && rsp.GetRet() != 0 {
			log.Printf("[%s] [SkyTrade] map load refused (code %d)", p.Username, rsp.GetRet())
		}
		// The same snapshot feeds the web map / auto-trader seat.
		p.stDeliver(msgType, data)
	case SERVER_SKY_TRADE_SPEED_UP:
		var rsp proto.SkyTradeAddSpeedResp
		if err := pb.Unmarshal(data, &rsp); err == nil && rsp.GetRet() != 0 {
			log.Printf("[%s] [SkyTrade] speed-up refused (code %d)", p.Username, rsp.GetRet())
		}
		p.stDeliver(msgType, data)
	case SERVER_SKY_TRADE_MOVE:
		var rsp proto.SkyTradeGotoPortResp
		if err := pb.Unmarshal(data, &rsp); err == nil && rsp.GetRet() != 0 {
			log.Printf("[%s] [SkyTrade] move refused (code %d)", p.Username, rsp.GetRet())
		}
		p.stDeliver(msgType, data)
	case SERVER_SKY_TRADE_BUY:
		var rsp proto.SkyTradeDealResp
		if err := pb.Unmarshal(data, &rsp); err == nil && rsp.GetRet() != 0 {
			log.Printf("[%s] [SkyTrade] deal refused (code %d)", p.Username, rsp.GetRet())
		}
		p.stDeliver(msgType, data)
	case SERVER_SKY_TRADE_MOVE_FINISH:
		var rsp proto.SkyTradeGetRewardResp
		if err := pb.Unmarshal(data, &rsp); err == nil && rsp.GetRet() != 0 {
			log.Printf("[%s] [SkyTrade] arrival claim refused (code %d)", p.Username, rsp.GetRet())
		}
		p.stDeliver(msgType, data)
	case SERVER_SKY_TRADE_REPORT_GOODS:
		var rsp proto.SkyTradeReportGoodsResp
		if err := pb.Unmarshal(data, &rsp); err != nil {
			return
		}
		if rsp.GetRet() != 0 {
			log.Printf("[%s] [SkyTrade] report refused (code %d)", p.Username, rsp.GetRet())
		} else {
			log.Printf("[%s] [SkyTrade] đã báo cảng %d (hàng hiếm %d)", p.Username, rsp.GetPortId(), rsp.GetRareGoodsId())
		}
	}
}

// Seat plumbing. Sky-trade answers are matched to requests purely by
// response type: one seat per type, never blocking the reader (a seat
// holder that vanished simply leaves the answer unread, same as before).
var (
	errStBusy    = errors.New("skytrade: seat busy")
	errStTimeout = errors.New("skytrade: answer timeout")
	errStGone    = errors.New("skytrade: connection gone")
)

func (p *Player) stSeatBusy(msgType string) bool {
	p.StMu.Lock()
	defer p.StMu.Unlock()
	_, busy := p.stChans[msgType]
	return busy
}

func (p *Player) stClaim(msgType string) (chan []byte, error) {
	p.StMu.Lock()
	defer p.StMu.Unlock()
	if p.stChans == nil {
		p.stChans = map[string]chan []byte{}
	}
	if _, busy := p.stChans[msgType]; busy {
		return nil, errStBusy
	}
	ch := make(chan []byte, 1)
	p.stChans[msgType] = ch
	return ch, nil
}

func (p *Player) stRelease(msgType string, ch chan []byte) {
	p.StMu.Lock()
	defer p.StMu.Unlock()
	if cur, ok := p.stChans[msgType]; ok && cur == ch {
		delete(p.stChans, msgType)
	}
}

// stDeliver hands one answer to the seat waiting for its type, if any.
func (p *Player) stDeliver(msgType string, data []byte) {
	p.StMu.Lock()
	ch := p.stChans[msgType]
	p.StMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

// stRoundTrip is the one-request-one-answer building block of the
// auto-trader: claim the answer's seat, send, wait.
func (p *Player) stRoundTrip(cmd, wantType string, body []byte, timeout time.Duration) ([]byte, error) {
	ch, err := p.stClaim(wantType)
	if err != nil {
		return nil, err
	}
	defer p.stRelease(wantType, ch)
	// ctx-aware send: a half-dead connection (writer goroutine exited,
	// writeChan full) would block p.write forever and freeze the web map
	// streamer loop (and the auto-trader) behind it.
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
	case <-p.ctx.Done():
		return nil, errStGone
	}
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, errStTimeout
	case <-p.ctx.Done():
		return nil, errStGone
	}
}

func skyTradeActivityBody(activityID int32) ([]byte, error) {
	return pb.Marshal(&proto.SkyTradeActivityReq{ActivityId: pb.Int32(activityID)})
}

// stSnapshot runs the client's open-session → load-map pair and returns the
// parsed live state (own ship + cargo + funds, every port market, any
// pending arrival). ENTER's own answer stays log-only, as before.
func (p *Player) stSnapshot(activityID int32, timeout time.Duration) (*proto.SkyTradeEnterResp, error) {
	body, err := skyTradeActivityBody(activityID)
	if err != nil {
		return nil, err
	}
	ch, err := p.stClaim(SERVER_SKY_TRADE_ENTER_MAP)
	if err != nil {
		return nil, err
	}
	defer p.stRelease(SERVER_SKY_TRADE_ENTER_MAP, ch)

	p.write(pack.Pack(SKY_TRADE_ENTER_CMD, body, p.UID))
	select {
	case <-p.ctx.Done():
		return nil, errStGone
	case <-time.After(skyTradeStepGap):
	}
	p.write(pack.Pack(SKY_TRADE_ENTER_MAP_CMD, body, p.UID))

	var data []byte
	select {
	case data = <-ch:
	case <-time.After(timeout):
		return nil, errStTimeout
	case <-p.ctx.Done():
		return nil, errStGone
	}
	var rsp proto.SkyTradeEnterResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, fmt.Errorf("skytrade: bad map snapshot: %w", err)
	}
	return &rsp, nil
}

// RequestSkyTradeMap returns the raw SkyTradeEnterResp snapshot for the web
// map renderer, via the same single ENTER_MAP seat the auto-trader uses —
// one snapshot pull at a time, whoever asks.
func (p *Player) RequestSkyTradeMap(timeout time.Duration) ([]byte, error) {
	var activityID int32
	now := time.Now()
	for _, e := range p.ActList.byType(skyTradeActivityType) {
		if e.status(now) == "active" {
			activityID = e.ActivityID
			break
		}
	}
	if activityID == 0 {
		return nil, fmt.Errorf("server chưa bật sự kiện Tiên Vực Thương Đồ (hoặc chưa nhận được activity id)")
	}
	body, err := skyTradeActivityBody(activityID)
	if err != nil {
		return nil, err
	}
	ch, err := p.stClaim(SERVER_SKY_TRADE_ENTER_MAP)
	if err != nil {
		return nil, fmt.Errorf("có yêu cầu map Tiên Vực Thương Đồ đang chờ, thử lại sau nhé")
	}
	defer p.stRelease(SERVER_SKY_TRADE_ENTER_MAP, ch)
	p.write(pack.Pack(SKY_TRADE_ENTER_CMD, body, p.UID))
	select {
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	case <-time.After(skyTradeStepGap):
	}
	p.write(pack.Pack(SKY_TRADE_ENTER_MAP_CMD, body, p.UID))
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("server không trả lời map Tiên Vực Thương Đồ, thử lại sau")
	case <-p.ctx.Done():
		return nil, fmt.Errorf("rớt kết nối server rồi đạo hữu")
	}
}
