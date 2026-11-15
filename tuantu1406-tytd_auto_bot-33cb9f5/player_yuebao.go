package tytd_go

// Yue Bao (Tiên Ngọc Bảo Phủ, activity type 170, 2170xx) automation.
//
// Live-verified protocol 2026-10-02 (player_yuebao_live_test.go):
//   - ENTER 217001 {activityId} → 4269 {ret, data{records}} — full history;
//     record {ts ms, kind, amount, flag}, kind 1 = OPEN deposit, 2/3 =
//     collected/withdrawn. Newest first.
//   - DEPOSIT 217002 {activityId, slot, amount} → 426a {ret}; slot 1 costs
//     2200 and matures in exactly 24 h, slot 3 in 3 days (pending sync 426c
//     head {maturity, slot, amount}). A second same-day slot pick can be
//     refused (17001) — the server is the oracle, refusal costs nothing.
//   - INTERACTE 217003 {activityId} → 426b {ret, reward} — collects EVERY
//     due deposit; nothing due answers ret≠0. Per-slot bodies are refused.
//
// Policy (the user's): the 1-day slot (slot 1) is the best — poll every
// 5 min, withdraw as soon as the open deposit matures, and immediately
// re-deposit; when the vault is free, deposit straight away. The real
// client waits ~5 s between collect and the next deposit; we mirror that.
// AMOUNT (owner 2026-10-09: "không phải acc nào cũng gửi max 2200, cần lấy
// thông tin để gửi cho đúng"): every deposit re-reads ENTER first and uses
// the account's OWN newest kind=1 record amount (ybPickAmount) — the plan
// the human last ran. 2200 is only the fallback for accounts with no
// deposit history; a broke account just gets refused (costs nothing) and the
// loop backs off 2 h.
//
// Transport: the Chaos Law single-seat round-trip bus (clRoundTrip /
// clDeliver) is reused — wsHandler routes the 4269/426a/426b answers into
// the same per-type seat registry.

import (
	"fmt"
	"log"
	"strings"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const (
	yueBaoActivityType   int32 = 170
	yueBaoSlot                 = int32(1)    // the 1-day slot
	yueBaoPrice                = int32(2200) // its captured client-side cost
	yueBaoDuration             = 24 * time.Hour
	yueBaoCycle                = 5 * time.Minute // status check cadence
	yueBaoRefusalBackoff       = 2 * time.Hour
)

// ybRoundTrip is Chaos Law's clRoundTrip under a Yue Bao name: the same
// generic one-seat-per-transport-type bus.
func (p *Player) ybRoundTrip(cmd, wantType string, body []byte) ([]byte, error) {
	return p.clRoundTrip(cmd, wantType, body)
}

// ybCycle performs one check-and-act pass and returns a log summary.
func (p *Player) ybCycle() string {
	var actID int32
	now := time.Now()
	for _, e := range p.ActList.byType(yueBaoActivityType) {
		if e.status(now) == "active" {
			actID = e.ActivityID
			break
		}
	}
	if actID == 0 {
		return "no active Yue Bao window"
	}
	actBody, _ := pb.Marshal(&proto.YueBaoActivityReq{ActivityId: pb.Int32(actID)})

	data, err := p.ybRoundTrip(S_YUE_BAO_ENTER, SERVER_YUE_BAO_ENTER, actBody)
	if err != nil {
		return fmt.Sprintf("enter: %v", err)
	}
	var enter proto.YueBaoEnterResp
	if err := pb.Unmarshal(data, &enter); err != nil {
		return fmt.Sprintf("enter: bad answer: %v", err)
	}
	if enter.GetRet() != 0 {
		return fmt.Sprintf("enter refused: ret=%d", enter.GetRet())
	}

	var last *proto.YueBaoRecord
	for _, r := range enter.GetData().GetRecords() {
		if last == nil || r.GetTs() > last.GetTs() {
			last = r
		}
	}
	amount := ybPickAmount(enter.GetData().GetRecords())

	deposit := func() string {
		dep, _ := pb.Marshal(&proto.YueBaoDepositReq{
			ActivityId: pb.Int32(actID), Slot: pb.Int32(yueBaoSlot), Amount: pb.Int32(amount),
		})
		dData, err := p.ybRoundTrip(S_YUE_BAO_DEPOSIT, SERVER_YUE_BAO_DEPOSIT, dep)
		if err != nil {
			return fmt.Sprintf("deposit: %v", err)
		}
		var dr proto.YueBaoDepositResp
		if err := pb.Unmarshal(dData, &dr); err != nil {
			return "deposit: answered(unparsed)"
		}
		if dr.GetRet() != 0 {
			return fmt.Sprintf("deposit refused: ret=%d", dr.GetRet())
		}
		return fmt.Sprintf("deposited %d vào hộc 1", amount)
	}

	if last != nil && last.GetKind() == 1 {
		mature := time.UnixMilli(last.GetTs()).Add(yueBaoDuration)
		if now.Before(mature) {
			return fmt.Sprintf("hộc 1 đang gửi, maturity %s", mature.In(ictZone).Format("01-02 15:04"))
		}
		iData, err := p.ybRoundTrip(S_YUE_BAO_INTERACTE, SERVER_YUE_BAO_INTERACTE, actBody)
		if err != nil {
			return fmt.Sprintf("withdraw: %v", err)
		}
		var ir proto.YueBaoInteracteResp
		if err := pb.Unmarshal(iData, &ir); err != nil {
			return "withdraw: answered(unparsed)"
		}
		if ir.GetRet() != 0 {
			return fmt.Sprintf("withdraw refused: ret=%d", ir.GetRet())
		}
		// Collect, then re-deposit like the real client does (~5 s apart).
		time.Sleep(5 * time.Second)
		return "withdrawn, " + deposit()
	}
	return deposit()
}

// ybPickAmount is the "gửi đúng số của acc này" rule (owner 2026-10-09:
// "không phải acc nào cũng gửi max 2200"): the account's plan is its own
// newest deposit record (kind=1, positive amount) in the ENTER history —
// whatever the human last chose in the client is what we re-open with.
// Only an account that has NEVER deposited falls back to the slot-1 list
// price (yueBaoPrice). The server stays the oracle: a refusal (broke
// account, same-day 17001) costs nothing and hits the 2 h backoff.
func ybPickAmount(records []*proto.YueBaoRecord) int32 {
	var best *proto.YueBaoRecord
	for _, r := range records {
		if r.GetKind() != 1 || r.GetAmount() <= 0 {
			continue
		}
		if best == nil || r.GetTs() > best.GetTs() {
			best = r
		}
	}
	if best != nil {
		return best.GetAmount()
	}
	return yueBaoPrice
}

// autoYueBao polls the vault every 5 min: withdraw when the 1-day slot
// matures, deposit when it is free. Repeated deposit refusals (broke
// account) back off to every 2 h so we do not hammer the server.
func (p *Player) autoYueBao() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(chaosLawWarmup):
	}
	refusals := 0
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			s := p.ybCycle()
			if !strings.Contains(s, "đang gửi") && !strings.Contains(s, "no active") {
				// Only real events reach the log at a 5-min cadence.
				log.Printf("[%s] [YueBao] %s", p.Username, s)
			}
			wait := yueBaoCycle
			switch {
			case strings.Contains(s, "deposited"):
				refusals = 0
			case strings.Contains(s, "refused"):
				refusals++
				if refusals >= 2 {
					wait = yueBaoRefusalBackoff
				}
			}
			select {
			case <-p.ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
}
