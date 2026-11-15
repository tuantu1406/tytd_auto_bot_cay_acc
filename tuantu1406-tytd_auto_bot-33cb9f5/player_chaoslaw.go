package tytd_go

// Chaos Law (Pháp Tắc Thái Hư, 2367xx) automation.
//
// Wire truth captured live 2026-10-02 (player_chaoslaw_live_test.go):
//   - ENTER 236701 {} → 8f5d {ret, playerData}; playerData carries
//     perceiveCount (f5, client perceives while ≥5),
//     perceiveNextAccumulateTime (f2) and the eight unformed-law slots
//     (f6: {id, evolving, endTime}).
//   - PERCEIVE 236702 {} → 8f5e {ret, playerData(f6), newItems} (+8f6b sync).
//   - ONE_KEY_HELP 236712 {} → 8f68 {helpCount} — no ret field; any answer
//     means accepted, refusal/no-pending simply costs one request.
//   - EVOLVE 236703 {id} → 8f5f {scalar}; EVOLVE_REWARD 236704 {id} → 8f60
//     {ret, item, playerData, rewards}. Empty bodies for these two are
//     SILENTLY DROPPED by the server (proto2 required id) — never send {}.
//
// A law with evolving==1 && endTime<=now is finished and claimable; after
// claiming, the server keeps the row listed (verified live), so claimed
// (id,endTime) pairs are remembered for the session to avoid re-claim spam.

import (
	"errors"
	"fmt"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const (
	chaosLawCycle   = 2 * time.Hour // the user's cadence
	chaosLawTimeout = 20 * time.Second
	chaosLawWarmup  = 15 * time.Second // let login pushes settle

	// chaosLawAutoEvolve gates EVOLVE + EVOLVE_REWARD in clCycle. User
	// ruling 2026-10-03: auto is perceive + one-key-help only; the user
	// evolves and collects rewards themselves.
	chaosLawAutoEvolve = false
)

var (
	errClTimeout = errors.New("chaoslaw: answer timeout")
	errClGone    = errors.New("chaoslaw: connection gone")
	errClBusy    = errors.New("chaoslaw: another request in flight")
)

// clClaim reserves the single seat for one incoming transport type
// (SkyTrade/BigChess idiom).
func (p *Player) clClaim(msgType string) (chan []byte, error) {
	p.ClMu.Lock()
	defer p.ClMu.Unlock()
	if p.clChans == nil {
		p.clChans = map[string]chan []byte{}
	}
	if _, busy := p.clChans[msgType]; busy {
		return nil, errClBusy
	}
	ch := make(chan []byte, 1)
	p.clChans[msgType] = ch
	return ch, nil
}

func (p *Player) clRelease(msgType string, ch chan []byte) {
	p.ClMu.Lock()
	defer p.ClMu.Unlock()
	if cur, ok := p.clChans[msgType]; ok && cur == ch {
		delete(p.clChans, msgType)
	}
}

// clDeliver routes one Chaos Law answer to its waiting seat.
func (p *Player) clDeliver(msgType string, data []byte) {
	p.ClMu.Lock()
	ch := p.clChans[msgType]
	p.ClMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
	}
}

func (p *Player) clRoundTrip(cmd, wantType string, body []byte) ([]byte, error) {
	ch, err := p.clClaim(wantType)
	if err != nil {
		return nil, err
	}
	defer p.clRelease(wantType, ch)
	p.write(pack.Pack(cmd, body, p.UID))
	select {
	case data := <-ch:
		return data, nil
	case <-time.After(chaosLawTimeout):
		return nil, errClTimeout
	case <-p.ctx.Done():
		return nil, errClGone
	}
}

func chaosLawIDBody(id int32) []byte {
	b, _ := pb.Marshal(&proto.ChaosLawIdReq{Id: pb.Int32(id)})
	return b
}

// clCycle runs one full maintenance pass and returns a log summary.
func (p *Player) clCycle() string {
	summary := ""
	data, err := p.clRoundTrip(S_CHAOS_LAW_ENTER, SERVER_CHAOS_LAW_ENTER, []byte{})
	if err != nil {
		return fmt.Sprintf("enter: %v", err)
	}
	var enter proto.ChaosLawEnterResp
	if err := pb.Unmarshal(data, &enter); err != nil {
		return fmt.Sprintf("enter: bad answer: %v", err)
	}
	if enter.GetRet() != 0 {
		return fmt.Sprintf("enter refused: ret=%d", enter.GetRet())
	}
	pd := enter.GetPlayerData()
	summary += fmt.Sprintf("perceive=%d ", pd.GetPerceiveCount())

	// Help everything pending — blind, one shot per cycle.
	if _, err := p.clRoundTrip(S_CHAOS_LAW_ONE_KEY_HELP, SERVER_CHAOS_LAW_ONE_KEY_HELP, []byte{}); err != nil {
		summary += fmt.Sprintf("help:%v ", err)
	} else {
		summary += "help=ok "
	}

	// One PERCEIVE per cycle (the user's cadence). Live-verified: the
	// answer's field 1 is a monotonic server counter (~36.7k scale), NOT a
	// ret code — so it is logged as info only and a timeout/absence is the
	// only failure we act on. The server simply has nothing to give when
	// energy is empty.
	pdData, err := p.clRoundTrip(S_CHAOS_LAW_PERCEIVE, SERVER_CHAOS_LAW_PERCEIVE, []byte{})
	if err != nil {
		summary += fmt.Sprintf("perceive:%v ", err)
	} else {
		var pr proto.ChaosLawPerceiveResp
		if pb.Unmarshal(pdData, &pr) == nil {
			summary += fmt.Sprintf("perceive→%d ", pr.GetV1())
			if pr.GetPlayerData() != nil {
				pd = pr.GetPlayerData()
			}
		} else {
			summary += "perceive:answered(unparsed) "
		}
	}

	// Status-driven evolve bookkeeping. DISABLED per user ruling 2026-10-03:
	// auto now only PERCEIVEs and helps — the user starts evolves / collects
	// rewards by hand. Flip chaosLawAutoEvolve back to true to restore.
	var done, started, claimed int
	now := time.Now().UnixMilli()
	for _, l := range pd.GetLaws() {
		if !chaosLawAutoEvolve {
			break
		}
		id := l.GetId()
		if l.GetEvolving() == 1 && l.GetEndTime() <= now {
			if p.clClaimedEnd(id, l.GetEndTime()) {
				continue // already claimed for this exact finish
			}
			done++
			rData, err := p.clRoundTrip(S_CHAOS_LAW_EVOLVE_REWARD, SERVER_CHAOS_LAW_EVOLVE_REWARD, chaosLawIDBody(id))
			if err != nil {
				summary += fmt.Sprintf("reward(%d):%v ", id, err)
				continue
			}
			var rr proto.ChaosLawEvolveRewardResp
			if err := pb.Unmarshal(rData, &rr); err != nil {
				summary += fmt.Sprintf("reward(%d):bad-answer ", id)
				continue
			}
			if rr.GetRet() != 0 {
				summary += fmt.Sprintf("reward(%d):ret=%d ", id, rr.GetRet())
				continue
			}
			p.clMarkClaimed(id, l.GetEndTime())
			claimed++
			if rr.GetPlayerData() != nil {
				pd = rr.GetPlayerData()
			}
		} else if l.GetEvolving() == 0 {
			started++
			rData, err := p.clRoundTrip(S_CHAOS_LAW_EVOLVE, SERVER_CHAOS_LAW_EVOLVE, chaosLawIDBody(id))
			if err != nil {
				summary += fmt.Sprintf("evolve(%d):%v ", id, err)
				continue
			}
			var er proto.ChaosLawEvolveResp
			_ = pb.Unmarshal(rData, &er)
			summary += fmt.Sprintf("evolve(%d)→%d ", id, er.GetV1())
		}
	}
	switch {
	case !chaosLawAutoEvolve:
		summary += "evolve=manual"
	case done+started == 0:
		summary += "laws=none-ready"
	default:
		summary += fmt.Sprintf("laws: %d ready, %d idle, %d claimed", done, started, claimed)
	}
	return summary
}

// Session memory of claimed (law id → last claimed endTime). A law that
// finishes again carries a NEW endTime and becomes claimable again.
func (p *Player) clClaimedEnd(id int32, endTime int64) bool {
	p.ClMu.Lock()
	defer p.ClMu.Unlock()
	return p.clClaimed[id] == endTime
}

func (p *Player) clMarkClaimed(id int32, endTime int64) {
	p.ClMu.Lock()
	defer p.ClMu.Unlock()
	if p.clClaimed == nil {
		p.clClaimed = map[int32]int64{}
	}
	p.clClaimed[id] = endTime
}

// autoPhapTacThaiSo maintains Chaos Law every 2 h: enter, help-all and one
// perceive (evolve/claim are gated off, see chaosLawAutoEvolve). Silent —
// the summary is for live tests only; the feature is stable in production.
func (p *Player) autoPhapTacThaiSo() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(chaosLawWarmup):
	}
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			p.clCycle()
			time.Sleep(chaosLawCycle)
		}
	}
}
