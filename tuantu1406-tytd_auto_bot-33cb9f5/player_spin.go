package tytd_go

import (
	"log"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Free-ad spins: quay tinh quái (S_SPIRIT_DRAW 20822) and quay thần thông
// (S_MAGIC_DERIVATION 24408). GameConfigDB caps both at SPIRIT_AD_LIMIT /
// MAGIC_AD_LIMIT = 2 per day with a 10 s cooldown; the client gates on the
// server-pushed used count (spiritFreeAd/magicFreeAd freeTimes), whose sync
// pushes (20821/24400) ship no public schema, so this loop gates on local
// attempts instead: at most 2 sends per feature per ICT day, spaced past the
// cooldown, sent as {times=1, isAd=true, isUseADTime=false} — exactly the
// official ad-draw body (same three fields on the wire for both commands).
// The server stays the arbiter: an already-used-from-app count makes the
// send come back ret != 0 and simply wastes one attempt; nothing is ever
// re-sent as a burst, and the next day's attempts reset fresh.

const (
	spinDailyLimit   = 2                // SPIRIT_AD_LIMIT == MAGIC_AD_LIMIT
	spinCooldown     = 15 * time.Second // client CD is 10 s (SPIRIT/MAGIC_AD_CD)
	spinPollInterval = 30 * time.Minute
	spinFirstDelay   = 35 * time.Second // behind tam bao, yard 15 s, bounty 20 s, task 25 s
)

// ictZone matches the game's VN day boundary (same zone telebot.go uses).
var ictZone = time.FixedZone("UTC+7", 7*60*60)

type spinKind int

const (
	SPIN_SPIRIT spinKind = iota
	SPIN_MAGIC
)

func (k spinKind) label() string {
	if k == SPIN_MAGIC {
		return "thần thông"
	}
	return "tinh quái"
}

// spinState is one feature's day budget; day is the ICT date the attempts
// belong to, so the first poll of a new day resets it.
type spinState struct {
	day      string
	attempts int
	lastSend time.Time
}

func (p *Player) spinStateLocked(kind spinKind) *spinState {
	if kind == SPIN_MAGIC {
		return &p.magicSpin
	}
	return &p.spiritSpin
}

// spinTryStart claims today's next attempt: day rollover first, then the
// daily cap and the cooldown. A claimed attempt is a send promised, so the
// counter moves here, before the packet is queued.
func (p *Player) spinTryStart(kind spinKind) bool {
	p.spinMu.Lock()
	defer p.spinMu.Unlock()
	st := p.spinStateLocked(kind)
	if today := time.Now().In(ictZone).Format("2006-01-02"); st.day != today {
		st.day = today
		st.attempts = 0
		st.lastSend = time.Time{}
	}
	if st.attempts >= spinDailyLimit || time.Since(st.lastSend) < spinCooldown {
		return false
	}
	st.attempts++
	st.lastSend = time.Now()
	return true
}

// spinOnce sends one ad-draw request when the budget allows it.
func (p *Player) spinOnce(kind spinKind) {
	if !p.spinTryStart(kind) {
		return
	}
	cmd := S_QUAY_TINHQUAI_QUANGCAO
	if kind == SPIN_MAGIC {
		cmd = S_QUAY_TTHANTHONG_QUANGCAO
	}
	// {times/drawTimes=1, isAd=true, isUseADTime=false} — wire 080110011800,
	// the same body the old login burst sent (and the official client sends
	// after a watched ad).
	reqMSG, _ := pb.Marshal(&proto.QuayTinhQuaiQuangCao{Var1: 1, Var2: 1, Var3: pb.Int32(0)})
	p.write(pack.Pack(cmd, reqMSG, p.UID))
}

func (p *Player) autoFreeSpin() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(spinFirstDelay):
	}
	for {
		p.spinOnce(SPIN_SPIRIT)
		p.spinOnce(SPIN_MAGIC)
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(spinPollInterval):
		}
	}
}

// spinHandle logs the draw verdict. Both responses carry ret as field 1;
// success is silent server-side (rewards just land), a rejection is logged
// once so the wasted attempt is explainable in the log.
func (p *Player) spinHandle(kind spinKind, data []byte) {
	ret := int32(-1)
	if kind == SPIN_MAGIC {
		var resp proto.MagicDerivationResp
		if err := pb.Unmarshal(data, &resp); err != nil {
			log.Printf("[%s] [Spin] bad %s draw resp: %v", p.Username, kind.label(), err)
			return
		}
		ret = resp.GetRet()
	} else {
		var resp proto.SpiritDrawResp
		if err := pb.Unmarshal(data, &resp); err != nil {
			log.Printf("[%s] [Spin] bad %s draw resp: %v", p.Username, kind.label(), err)
			return
		}
		ret = resp.GetRet()
	}
	if ret == 0 {
		p.spinMu.Lock()
		st := p.spinStateLocked(kind)
		attempt, day := st.attempts, st.day
		p.spinMu.Unlock()
		log.Printf("[%s] [Spin] quay %s free thành công (lần %d ngày %s)", p.Username, kind.label(), attempt, day)
		return
	}
	log.Printf("[%s] [Spin] quay %s free bị từ chối: ret=%d", p.Username, kind.label(), ret)
}
