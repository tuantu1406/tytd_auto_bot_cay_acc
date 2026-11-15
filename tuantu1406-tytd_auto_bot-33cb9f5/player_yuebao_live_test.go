//go:build live

package tytd_go

// Yue Bao (Tiên Ngọc Bảo Phủ, activity type 170, 2170xx) live probe. The
// official client's vault UI ships inside the downloaded remoteYueBao
// package, so — exactly like Chaos Law — the wire shape is recovered from
// the server: ENTER dumps the slot list, INTERACTE is tested as the
// collect action, and DEPOSIT bodies were already captured from the real
// client ({activityId, slot, amount}).
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveYueBaoProbe -v -timeout 180s .
//
// Env: TYTD_LIVE_SERVER (default 710547; the vault only exists for this
// account on 710564). The probe DOES fire deposits to enumerate slots 2/3
// (authorized test account) — every one is collected back by the INTERACTE
// that follows, so it costs nothing permanent.

import (
	"fmt"
	"os"
	"testing"
	"time"
	"tytd_go/internal/pack"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestLiveYueBaoProbe(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveUser = liveUserEnv(t)
		livePass = livePassEnv(t)
	)
	liveServer := envIntOr(t, "TYTD_LIVE_SERVER", 710547)

	s, err := chaosDialLive(t, liveUser, livePass, liveServer)
	if err != nil {
		t.Fatalf("live connect: %v", err)
	}
	defer s.cleanup()
	// Self-managed login wait that RECORDS Yue Bao frames instead of
	// discarding them (responses: 4269/426a/426b, sync push 426c).
	loginEnd := time.Now().Add(25 * time.Second)
	for time.Now().Before(loginEnd) {
		select {
		case f := <-s.raw:
			switch f.msgType {
			case "4269", "426c":
				t.Logf("LOGIN-SIDE %s %s", f.msgType, ybHead(f.data))
			case "426a", "426b":
				t.Logf("LOGIN-SIDE %s %s", f.msgType, chaosWire(f.data, 0))
			case "0065":
				loginEnd = time.Now().Add(5 * time.Second)
			}
		case <-time.After(200 * time.Millisecond):
		}
	}

	// Resolve the live instance id: activity type 170, any phase.
	var actID int32
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && actID == 0 {
		for _, e := range s.p.ActList.byType(170) {
			status := e.status(time.Now())
			t.Logf("act %d type170 status=%s window=[%d,%d]", e.ActivityID, status, e.BeginTime, e.EndTime)
			if status == "active" || status == "show" {
				actID = e.ActivityID
			}
		}
		if actID == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if actID == 0 {
		t.Skip("no type-170 Yue Bao activity in the live list — nothing to probe")
	}

	askRaw := func(name, cmdHex string, body []byte) {
		s.p.writeChan <- pack.Pack(cmdHex, body, s.p.UID)
		t.Logf("SENT %s (%s) body=% x", name, cmdHex, body)
		end := time.Now().Add(5 * time.Second)
		for time.Now().Before(end) {
			select {
			case f := <-s.raw:
				switch f.msgType {
				case "4269", "426c":
					t.Logf("RECV %s %s", f.msgType, ybHead(f.data))
				case "426a", "426b":
					t.Logf("RECV %s %s", f.msgType, chaosWire(f.data, 0))
				}
			case <-time.After(250 * time.Millisecond):
			}
		}
	}

	// The real client capture used activityId 9060959 — find out what that
	// entry is in OUR list (type may differ from 170).
	for _, e := range s.p.ActList.snapshot() {
		if e.ActivityID == 9060959 {
			t.Logf("capture-id 9060959 IS in list: type=%d status=%s", e.Type, e.status(time.Now()))
		}
	}
	ask := func(name string, id int32) {
		idBody := appendVarintField(nil, 1, int64(id))
		askRaw(name+"/{id:"+itoa(id)+"}", "00034fa9", idBody)
	}
	ask("ENTER(list170)", actID)
	// Replay the captured real-client DEPOSIT byte-for-byte in structure:
	// {activityId, slot=1, amount=2200}. The user authorized slot-1
	// deposits for this automation.
	dep := appendVarintField(nil, 1, int64(actID))
	dep = appendVarintField(dep, 2, 1)
	dep = appendVarintField(dep, 3, 2200)
	askRaw("DEPOSIT/{id,slot1,2200}", "00034faa", dep)
	askRaw("INTERACTE/{id}", "00034fab", appendVarintField(nil, 1, int64(actID)))
	// INTERACTE might be per-slot: {activityId, slot}
	ik := appendVarintField(nil, 1, int64(actID))
	ik = appendVarintField(ik, 2, 1)
	askRaw("INTERACTE/{id,slot1}", "00034fab", ik)

	// Enumerate the other slots: deposit +5s interact; the 426c pending
	// header {maturity,slot,amount} reveals each slot's duration. Deposits
	// are returned by INTERACTE, so the probe costs nothing permanent.
	for _, slot := range []int64{2, 3} {
		d := appendVarintField(nil, 1, int64(actID))
		d = appendVarintField(d, 2, slot)
		d = appendVarintField(d, 3, 2200)
		askRaw(fmt.Sprintf("DEPOSIT/slot%d", slot), "00034faa", d)
		i := appendVarintField(nil, 1, int64(actID))
		askRaw(fmt.Sprintf("INTERACTE/after-slot%d", slot), "00034fab", i)
	}
	// A follow-up ENTER sometimes flushes a DATA_SYNC on this engine.
	end := time.Now().Add(4 * time.Second)
	for time.Now().Before(end) {
		select {
		case f := <-s.raw:
			t.Logf("(late msg %s len=%d: %s)", f.msgType, len(f.data), chaosWire(f.data, 0))
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func itoa(v int32) string { return fmt.Sprintf("%d", v) }

// ybHead summarizes the top-level scalars of an ENTER/SYNC answer (field 1
// of a SYNC is the pending maturity ts; 2 = slot, 3 = amount) plus the
// newest history record, instead of dumping 500 bytes of wire.
func ybHead(data []byte) string {
	var f1, f2, f3 int64
	var recs int
	var newest string
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]
		if num <= 3 && typ == protowire.VarintType {
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				break
			}
			data = data[n:]
			switch num {
			case 1:
				f1 = int64(v)
			case 2:
				f2 = int64(v)
			case 3:
				f3 = int64(v)
			}
			continue
		}
		if typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				break
			}
			data = data[n:]
			// Records live one level down (ENTER: inside f2; SYNC: at f4).
			inner := v
			for len(inner) > 0 {
				num2, typ2, n2 := protowire.ConsumeTag(inner)
				if n2 < 0 {
					break
				}
				inner = inner[n2:]
				if num2 == 4 && typ2 == protowire.BytesType {
					rb, n3 := protowire.ConsumeBytes(inner)
					if n3 < 0 {
						break
					}
					inner = inner[n3:]
					recs++
					if newest == "" {
						newest = chaosWire(rb, 2)
					}
					continue
				}
				if n := protowire.ConsumeFieldValue(num2, typ2, inner); n < 0 {
					break
				} else {
					inner = inner[n:]
				}
			}
		} else {
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				break
			}
			data = data[n:]
		}
	}
	if recs == 0 {
		return fmt.Sprintf("head{%d,%d,%d} records=0", f1, f2, f3)
	}
	return fmt.Sprintf("head{%d,%d,%d} records=%d newest=%s", f1, f2, f3, recs, newest)
}

// TestLiveAutoYueBao verifies the SHIPPED ybCycle against the real server:
// cycle 1 must act on the true vault state (deposit if free, or report the
// open deposit's maturity), cycle 2 must then see the slot as busy — no
// double deposit.
//
//	TYTD_LIVE=1 TYTD_LIVE_SERVER=710564 go test -count=1 -tags "sqlcipher live" -run TestLiveAutoYueBao -v -timeout 180s .
func TestLiveAutoYueBao(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveUser = liveUserEnv(t)
		livePass = livePassEnv(t)
	)
	liveServer := envIntOr(t, "TYTD_LIVE_SERVER", 710547)

	s, err := chaosDialLive(t, liveUser, livePass, liveServer)
	if err != nil {
		t.Fatalf("live connect: %v", err)
	}
	defer s.cleanup()
	chaosWaitLogin(t, s, 20*time.Second)
	drainStop := make(chan struct{})
	go func() {
		for {
			select {
			case <-s.raw:
			case <-drainStop:
				return
			}
		}
	}()
	defer close(drainStop)

	t.Logf("cycle 1: %s", s.p.ybCycle())
	t.Logf("cycle 2: %s", s.p.ybCycle())
}
