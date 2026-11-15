package tytd_go

// Huyết Mạch (Talent system, 2062x) automation — /start_huyet_mach ·
// /stop_huyet_mach (per-session toggle like lucdao/maohiem).
//
// Live-verified 2026-10-02 (player_huyetmach_live_test.go):
//   - RANDOM_TALENT 20622 {randomTimes} → 026e {ret}; ret 1201 = item
//     100007 (TALENT_PULL_COST) missing — nothing is spent on refusal.
//     Cooldown 1229 ms (client drawTime).
//   - GET_UNDEAL 20625 {} → 0271 {ret, data{2: one entry per pending}}.
//   - DEAL 20623 {3:{index, action:1}, 4:0} → 026f {ret, reward, data} —
//     the user's captured client frame: action 1 = DELETE the rolled
//     result. Deleting feeds talentCreateLevel, which raises the roll cap.
//   - Roll multiplier cap (GameConfigDB AUTOTALENT_COST_RATE "N|lv|days"):
//     N=1@lv1, 2@20, 3@40, 4@50, 5@60; days always satisfied (server-age
//     gates are 1). Level comes from the 026d login sync (f2 — verified:
//     live account reported 47 → roll x3 accepted; an x1 roll earlier was
//     refused 1201, i.e. empty item bag, not a wrong multiplier).
//
// Answers ride the shared single-seat bus (clRoundTrip/clDeliver); the
// wsHandler routes 026e/026f/0271 there and 026d into hmHandleSync.

import (
	"fmt"
	"log"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const (
	hmRollCooldown    = 1500 * time.Millisecond // drawTime 1229 ms + slack
	hmDealPace        = 400 * time.Millisecond  // pacing between DELETE calls
	hmNoItemPause     = 10 * time.Minute        // unused wait hint; any error stops the loop for good
	hmOtherFailPause  = 60 * time.Second
	hmDealsPerRollCap = 200 // one x3 roll queues 150 (live); bound stays above that
	hmRetNoItem       = int32(1201)
)

// hmHandleSync records talentCreateLevel from the 026d push.
func (p *Player) hmHandleSync(data []byte) {
	var m proto.TalentPlayerDataMsg
	if err := pb.Unmarshal(data, &m); err != nil || m.TalentCreateLevel == nil {
		return
	}
	p.HmMu.Lock()
	p.hmLevel = m.GetTalentCreateLevel()
	p.hmLevelSeen = true
	p.HmMu.Unlock()
}

// hmMaxRoll maps talentCreateLevel → allowed randomTimes (AUTOTALENT_COST_RATE).
func hmMaxRoll(level int32) int32 {
	switch {
	case level >= 60:
		return 5
	case level >= 50:
		return 4
	case level >= 40:
		return 3
	case level >= 20:
		return 2
	default:
		return 1
	}
}

func (p *Player) hmCurrentLevel() int32 {
	p.HmMu.Lock()
	defer p.HmMu.Unlock()
	return p.hmLevel
}

// hmUndealCount asks the server how many results wait to be dealt.
func (p *Player) hmUndealCount() (int, error) {
	data, err := p.clRoundTrip(S_TALENT_GET_UNDEAL, SERVER_TALENT_UNDEAL, []byte{})
	if err != nil {
		return 0, err
	}
	var r proto.TalentUndealResp
	if err := pb.Unmarshal(data, &r); err != nil {
		return 0, err
	}
	if r.GetRet() != 0 {
		return 0, nil
	}
	return len(r.GetData().GetItems()), nil
}

const hmRetNothingToDeal = int32(1203)

// hmDeleteOne deletes queue position 0 (action 1). Positions shift after
// each delete, so the caller always targets 0 until the queue is empty.
// ret 1203 = queue already empty (live-verified) — reported, not an error.
func (p *Player) hmDeleteOne() (int32, error) {
	req := &proto.TalentDealReq{
		Item: &proto.TalentDealItem{
			Index:  pb.Int32(0),
			Action: pb.Int32(1),
		},
		Mode: pb.Int32(0),
	}
	body, _ := pb.Marshal(req)
	data, err := p.clRoundTrip(S_TALENT_DEAL, SERVER_TALENT_DEAL, body)
	if err != nil {
		return -1, err
	}
	var r proto.TalentDealResp
	if err := pb.Unmarshal(data, &r); err != nil {
		return -1, err
	}
	return r.GetRet(), nil
}

// hmTick performs one roll-and-deal pass and returns how long to wait
// before the next one plus the roll's ret (0 when the roll was accepted).
// The queue shifts toward index 0 after every delete, so deletes always
// target position 0, bounded per pass.
func (p *Player) hmTick() (time.Duration, string, int32) {
	deleted := 0
	n, err := p.hmUndealCount()
	if err != nil {
		return hmOtherFailPause, fmt.Sprintf("undeal check: %v", err), -1
	}
	for i := 0; i < n && i < hmDealsPerRollCap; i++ {
		ret, err := p.hmDeleteOne()
		if err != nil {
			return hmOtherFailPause, fmt.Sprintf("delete: %v", err), -1
		}
		if ret == hmRetNothingToDeal { // queue emptied under us (1203)
			break
		}
		if ret != 0 {
			return hmOtherFailPause, fmt.Sprintf("delete refused: ret=%d", ret), ret
		}
		deleted++
		p.hmDeletesDone++
		select {
		case <-p.ctx.Done():
			return 0, "gone", 0
		case <-time.After(hmDealPace):
		}
	}
	// Roll the max multiplier for our create level.
	roll := hmMaxRoll(p.hmCurrentLevel())
	body, _ := pb.Marshal(&proto.TalentRandomReq{RandomTimes: pb.Int32(roll)})
	data, err := p.clRoundTrip(S_TALENT_RANDOM, SERVER_TALENT_RANDOM, body)
	if err != nil {
		return hmOtherFailPause, fmt.Sprintf("roll x%d: %v", roll, err), -1
	}
	var r proto.TalentRandomResp
	if err := pb.Unmarshal(data, &r); err != nil {
		return hmOtherFailPause, "roll: bad answer", -1
	}
	summary := fmt.Sprintf("deleted=%d roll=x%d(lv%d) ret=%d", deleted, roll, p.hmCurrentLevel(), r.GetRet())
	switch r.GetRet() {
	case 0:
		p.hmRollsDone += int64(roll)
		return hmRollCooldown, summary, 0
	case hmRetNoItem: // item 100007 gone — autoHuyetMach treats this as terminal
		return hmNoItemPause, summary, hmRetNoItem
	default:
		return hmOtherFailPause, summary, r.GetRet()
	}
}

// autoHuyetMach: while the user's toggle is on, run hmTick on the client's
// own cooldown (or the pause hmTick asks for). Deleting feeds
// talentCreateLevel, which raises the multiplier cap over time.
// Any non-zero ret flips the toggle off permanently with a notify —
// the goroutine keeps idling so /start_huyet_mach can re-arm it.
func (p *Player) autoHuyetMach() {
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		if !p.HuyetMach {
			time.Sleep(2 * time.Second)
			continue
		}
		wait, summary, rollRet := p.hmTick()
		if summary != "gone" {
			log.Printf("[%s] [HuyetMach] %s", p.Username, summary)
		}
		// Notify policy (user ruling): no per-item messages — one status
		// update every 500 results processed; any error notifies and stops
		// the loop for good. Only reaches users with /start_notify.
		if p.hmNextStatusAt == 0 {
			p.hmNextStatusAt = 500
		}
		if p.hmDeletesDone >= p.hmNextStatusAt {
			p.hmNextStatusAt += 500
			p.sendNotify(fmt.Sprintf(
				"Huyết Mạch: đã xử lý %d kết quả (roll %d lần, cấp %d, đang roll x%d)",
				p.hmDeletesDone, p.hmRollsDone, p.hmCurrentLevel(), hmMaxRoll(p.hmCurrentLevel())))
		}
		if rollRet != 0 {
			// Any error (missing item, refusal, transport): notify once and
			// stop for good (user ruling) — re-arm with /start_huyet_mach.
			p.HuyetMach = false
			if rollRet == hmRetNoItem {
				p.sendNotify("Huyết Mạch lỗi: hết huyết dịch luyện hoá (ret 1201) — đã TẮT auto, đạo hữu nạp thêm rồi /start_huyet_mach lại nhé")
			} else {
				p.sendNotify(fmt.Sprintf("Huyết Mạch lỗi: ret=%d — đã TẮT auto, /start_huyet_mach lại khi đạo hữu muốn", rollRet))
			}
			continue
		}
		if wait <= 0 {
			return
		}
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
