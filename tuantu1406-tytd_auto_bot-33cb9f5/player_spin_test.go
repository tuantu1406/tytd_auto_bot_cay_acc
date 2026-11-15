package tytd_go

import (
	"encoding/hex"
	"testing"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// The free-ad spin poller: 2 attempts per feature per ICT day, 15 s apart,
// one official ad-draw body on each of the two commands, and a day rollover
// that refills the budget.

func TestSpinTryStartCapAndCooldown(t *testing.T) {
	p := &Player{Username: "u", UID: 42}
	if !p.spinTryStart(SPIN_SPIRIT) {
		t.Fatal("first attempt of the day must be allowed")
	}
	if p.spinTryStart(SPIN_SPIRIT) {
		t.Fatal("second attempt inside the 10 s cooldown must be refused")
	}

	// Cooldown elapsed: second (and final) attempt of the day goes through.
	p.spinMu.Lock()
	p.spiritSpin.lastSend = time.Time{}
	p.spinMu.Unlock()
	if !p.spinTryStart(SPIN_SPIRIT) {
		t.Fatal("second attempt after cooldown must be allowed")
	}

	// Daily cap reached even with cooldown long gone.
	p.spinMu.Lock()
	p.spiritSpin.lastSend = time.Time{}
	p.spinMu.Unlock()
	if p.spinTryStart(SPIN_SPIRIT) {
		t.Fatal("third attempt of the day must be refused (SPIRIT_AD_LIMIT=2)")
	}

	// Magic has its own budget, unaffected by spirit usage.
	if !p.spinTryStart(SPIN_MAGIC) {
		t.Fatal("magic budget must be independent")
	}
}

func TestSpinDayRollover(t *testing.T) {
	p := &Player{Username: "u", UID: 42}
	p.spinMu.Lock()
	p.magicSpin.day = "2000-01-01"
	p.magicSpin.attempts = spinDailyLimit
	p.magicSpin.lastSend = time.Now()
	p.spinMu.Unlock()

	if !p.spinTryStart(SPIN_MAGIC) {
		t.Fatal("new ICT day must reset attempts and cooldown")
	}
	p.spinMu.Lock()
	defer p.spinMu.Unlock()
	if p.magicSpin.attempts != 1 {
		t.Fatalf("after rollover attempts=%d, want 1", p.magicSpin.attempts)
	}
	if want := time.Now().In(ictZone).Format("2006-01-02"); p.magicSpin.day != want {
		t.Fatalf("day=%s, want %s", p.magicSpin.day, want)
	}
}

func TestSpinWire(t *testing.T) {
	p := &Player{Username: "u", UID: 42, writeChan: make(chan []byte, 16)}
	wireCtx(t, p)

	p.spinOnce(SPIN_SPIRIT)
	frame := <-p.writeChan
	if ft := frameFuncType(t, frame); ft != S_QUAY_TINHQUAI_QUANGCAO {
		t.Fatalf("frame %s, want spirit draw", ft)
	}
	if got := hex.EncodeToString(frame[18:]); got != "080110011800" {
		t.Fatalf("spirit payload %s, want 080110011800 (times=1,isAd=1,isUseADTime=0)", got)
	}

	// Independent budget: magic goes out immediately.
	p.spinOnce(SPIN_MAGIC)
	frame = <-p.writeChan
	if ft := frameFuncType(t, frame); ft != S_QUAY_TTHANTHONG_QUANGCAO {
		t.Fatalf("frame %s, want magic derivation", ft)
	}
	if got := hex.EncodeToString(frame[18:]); got != "080110011800" {
		t.Fatalf("magic payload %s, want 080110011800", got)
	}

	// Cooldown: no burst right after, exactly the bug the login double-fire
	// used to be.
	p.spinOnce(SPIN_SPIRIT)
	p.spinOnce(SPIN_MAGIC)
	if n := len(p.writeChan); n != 0 {
		t.Fatalf("spins re-sent without waiting the cooldown (%d frames)", n)
	}
}

// The handlers only read ret (field 1); any other response fields must not
// break them, and neither verdict may panic.
func TestSpinHandle(t *testing.T) {
	p := &Player{Username: "u", UID: 42}
	p.spinMu.Lock()
	p.spiritSpin.attempts = 1
	p.spiritSpin.day = "today"
	p.spinMu.Unlock()

	ok, _ := pb.Marshal(&proto.SpiritDrawResp{Ret: pb.Int32(0)})
	p.spinHandle(SPIN_SPIRIT, ok)
	bad, _ := pb.Marshal(&proto.MagicDerivationResp{Ret: pb.Int32(7)})
	p.spinHandle(SPIN_MAGIC, bad)
	// A response carrying unmodelled fields beyond ret must still parse.
	raw := []byte{0x08, 0x00, 0x12, 0x02, 'x', 'y', 0x20, 0x05}
	p.spinHandle(SPIN_SPIRIT, raw)

	// Handling must not touch the attempt budget.
	p.spinMu.Lock()
	defer p.spinMu.Unlock()
	if p.spiritSpin.attempts != 1 {
		t.Fatalf("spinHandle changed attempts: %d", p.spiritSpin.attempts)
	}
}
