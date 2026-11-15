//go:build live

package tytd_go

// Huyết Mạch / Talent system (2062x) live probe — recovers the wire field
// numbers the client bundles hide (Talent.proto ships in a downloaded
// package):
//
// Requests are the full 20xxx ids; ANSWERS come back at id-20000 (same rule
// as S_SPIRIT_DRAW 20822 → 0336):
//
//	20621 = 0000507d SYNC_DATA    → 026d  (login push: talentPlayerDataMsg, incl. talentCreateLevel)
//	20622 = 0000508e RANDOM_TALENT→ 026e  req {randomTimes}; resp {ret, unDealTalentData}
//	20623 = 0000508f DEAL_TALENT  → 026f  req {3:{index,action},4:0} (action 1 = DELETE — real-client capture)
//	20625 = 00005091 GET_UNDEAL    → 0271  req {}
//
// Costs: 1× item 100007 per roll (TALENT_PULL_COST), cooldown ~1.23 s
// (drawTime 1229.22). The one roll here is authorized on the test account.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveHuyetMachProbe -v -timeout 180s .

import (
	"fmt"
	"os"
	"testing"
	"time"
	"tytd_go/internal/pack"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestLiveHuyetMachProbe(t *testing.T) {
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

	// Record the login-side talent sync (507d) while waiting.
	loginEnd := time.Now().Add(25 * time.Second)
	sawSync := false
	for time.Now().Before(loginEnd) {
		select {
		case f := <-s.raw:
			if f.msgType == "026d" {
				t.Logf("LOGIN 507d SYNC %s", chaosWire(f.data, 0))
				sawSync = true
			}
			if f.msgType == "0065" {
				loginEnd = time.Now().Add(5 * time.Second)
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !sawSync {
		t.Log("no 507d talent sync at login (system may not push until first use)")
	}

	ask := func(name, cmdHex string, body []byte, want string) {
		s.p.writeChan <- pack.Pack(cmdHex, body, s.p.UID)
		t.Logf("SENT %s (%s) body=% x", name, cmdHex, body)
		end := time.Now().Add(5 * time.Second)
		for time.Now().Before(end) {
			select {
			case f := <-s.raw:
				switch f.msgType {
				case want, "026d", "0270":
					t.Logf("RECV %s %s", f.msgType, chaosWire(f.data, 0))
				}
			case <-time.After(250 * time.Millisecond):
			}
		}
	}

	// 1) pending queue, free
	ask("GET_UNDEAL", "00005091", []byte{}, "0271")
	// 2) one roll (1× item 100007)
	ask("RANDOM/1", "0000508e", appendVarintField(nil, 1, 1), "026e")
	// 3) read the queue again to see how results are represented
	ask("GET_UNDEAL/after", "00005091", []byte{}, "0271")
	// 4) delete result 0, exactly like the captured client frame
	var inner []byte
	inner = appendVarintField(inner, 1, 0) // index 0
	inner = appendVarintField(inner, 2, 1) // action 1 = delete
	outer := protowire.AppendTag(nil, 3, protowire.BytesType)
	outer = protowire.AppendBytes(outer, inner)
	outer = appendVarintField(outer, 4, 0)
	ask("DEAL/delete#0", "0000508f", outer, "026f")
	// 5) final queue state
	ask("GET_UNDEAL/final", "00005091", []byte{}, "0271")
}

// TestLiveHuyetMachTick verifies the SHIPPED hmTick against the real
// server through the seat bus: level read from the 026d login sync, the
// existing junk queue shrinks via DELETE, and the roll answers through its
// seat (ret=0 if the account holds item 100007, 1201 if not — either way
// the wiring is proven).
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveHuyetMachTick -v -timeout 240s .
func TestLiveHuyetMachTick(t *testing.T) {
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

	before, err := s.p.hmUndealCount()
	if err != nil {
		t.Fatalf("undeal count: %v", err)
	}
	t.Logf("undeal queue = %d, talentCreateLevel = %d (seen=%v) → max roll x%d",
		before, s.p.hmCurrentLevel(), s.p.hmLevelSeen, hmMaxRoll(s.p.hmCurrentLevel()))

	wait, summary, _ := s.p.hmTick()
	t.Logf("tick: %s (wait %v)", summary, wait)
	after, err := s.p.hmUndealCount()
	if err != nil {
		t.Fatalf("undeal count after: %v", err)
	}
	t.Logf("queue %d → %d", before, after)
	if before > 0 && after >= before {
		t.Fatalf("tick deleted nothing: %d → %d", before, after)
	}
}

// TestLiveHuyetMachDiag answers the open semantic questions:
//  1. where talentCreateLevel lives in the OWN 026d sync (top-level scan),
//  2. what the flat 0271 entries look like and how they shift on delete,
//  3. whether DEAL index is position (0) or the entry's own id field.
func TestLiveHuyetMachDiag(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	s, err := chaosDialLive(t, liveUserEnv(t), livePassEnv(t), envIntOr(t, "TYTD_LIVE_SERVER", 710547))
	if err != nil {
		t.Fatalf("live connect: %v", err)
	}
	defer s.cleanup()
	// Custom login wait that CATCHES the first 026d (chaosWaitLogin would
	// discard it).
	var sync026d []byte
	loginEnd := time.Now().Add(25 * time.Second)
	for time.Now().Before(loginEnd) {
		select {
		case f := <-s.raw:
			if f.msgType == "026d" && sync026d == nil {
				sync026d = f.data
			}
			if f.msgType == "0065" {
				loginEnd = time.Now().Add(4 * time.Second)
			}
		case <-time.After(150 * time.Millisecond):
		}
	}

	topScan := func(tag string, data []byte) {
		var out []string
		for len(data) > 0 {
			num, typ, n := protowire.ConsumeTag(data)
			if n < 0 {
				break
			}
			data = data[n:]
			if typ == protowire.VarintType {
				v, n := protowire.ConsumeVarint(data)
				if n < 0 {
					break
				}
				data = data[n:]
				out = append(out, fmt.Sprintf("%d=%d", num, v))
			} else {
				n := protowire.ConsumeFieldValue(num, typ, data)
				if n < 0 {
					break
				}
				data = data[n:]
				out = append(out, fmt.Sprintf("%d=(wire%d)", num, typ))
			}
		}
		t.Logf("%s TOP: %v", tag, out)
	}
	if sync026d != nil {
		topScan("026d", sync026d)
	} else {
		t.Log("no 026d at login")
	}

	getQueue := func() []byte {
		s.p.writeChan <- pack.Pack("00005091", []byte{}, s.p.UID)
		end := time.Now().Add(5 * time.Second)
		for time.Now().Before(end) {
			select {
			case f := <-s.raw:
				if f.msgType == "0271" {
					return f.data
				}
			case <-time.After(200 * time.Millisecond):
			}
		}
		return nil
	}
	scanQueue := func(tag string, data []byte) {
		var entries []string
		var ret string
		for len(data) > 0 {
			num, typ, n := protowire.ConsumeTag(data)
			if n < 0 {
				break
			}
			data = data[n:]
			if typ == protowire.BytesType {
				v, n := protowire.ConsumeBytes(data)
				if n < 0 {
					break
				}
				data = data[n:]
				if num == 2 {
					entries = append(entries, chaosWire(v, 1))
				}
				continue
			}
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				break
			}
			data = data[n:]
			if num == 1 {
				ret = fmt.Sprintf("%d", v)
			}
		}
		t.Logf("%s ret=%s entries=%d", tag, ret, len(entries))
		for i, e := range entries {
			if i >= 3 {
				break
			}
			t.Logf("%s entry[%d] %s", tag, i, e[:min(len(e), 150)])
		}
	}
	q := getQueue()
	if q == nil {
		t.Fatal("no 0271 answer")
	}
	scanQueue("Q1", q)

	sendDeal := func(body []byte) string {
		s.p.writeChan <- pack.Pack("0000508f", body, s.p.UID)
		end := time.Now().Add(5 * time.Second)
		for time.Now().Before(end) {
			select {
			case f := <-s.raw:
				if f.msgType == "026f" {
					return chaosWire(f.data, 0)[:min(len(chaosWire(f.data, 0)), 120)]
				}
			case <-time.After(200 * time.Millisecond):
			}
		}
		return "no answer"
	}
	var inner []byte
	inner = appendVarintField(inner, 1, 0) // index 0
	inner = appendVarintField(inner, 2, 2) // action 2 (keep? second known action)
	b := protowire.AppendTag(nil, 3, protowire.BytesType)
	b = protowire.AppendBytes(b, inner)
	b = appendVarintField(b, 4, 0)
	t.Logf("DEAL idx0 action2 → %s", sendDeal(b))

	var inner2 []byte
	inner2 = appendVarintField(inner2, 1, 0)
	inner2 = appendVarintField(inner2, 2, 1)
	b2 := protowire.AppendTag(nil, 3, protowire.BytesType)
	b2 = protowire.AppendBytes(b2, inner2)
	b2 = appendVarintField(b2, 4, 0)
	res := sendDeal(b2)
	t.Logf("DEAL idx0 action1 → %s", res)
	scanQueue("Q2", getQueue())
	_ = res
}

// TestLiveHuyetMachClear drains the test account's un-dealt queue WITHOUT
// rolling (one-time cleanup after verification runs). Needs the explicit
// env because it deletes real game data:
//
//	TYTD_LIVE=1 TYTD_HM_CLEAR=1 go test -count=1 -tags "sqlcipher live" -run TestLiveHuyetMachClear -v -timeout 600s .
func TestLiveHuyetMachClear(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_HM_CLEAR") == "" {
		t.Skip("set TYTD_LIVE=1 and TYTD_HM_CLEAR=1 to drain the test account queue")
	}
	s, err := chaosDialLive(t, liveUserEnv(t), livePassEnv(t), envIntOr(t, "TYTD_LIVE_SERVER", 710547))
	if err != nil {
		t.Fatalf("live connect: %v", err)
	}
	defer s.cleanup()
	chaosWaitLogin(t, s, 20*time.Second)
	for round := 1; ; round++ {
		n, err := s.p.hmUndealCount()
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if n == 0 {
			t.Logf("queue empty after %d rounds", round-1)
			return
		}
		for i := 0; i < n; i++ {
			ret, err := s.p.hmDeleteOne()
			if err != nil {
				t.Fatalf("delete: %v", err)
			}
			if ret == hmRetNothingToDeal {
				break
			}
			if ret != 0 {
				t.Fatalf("delete ret=%d", ret)
			}
			time.Sleep(120 * time.Millisecond)
		}
		t.Logf("round %d done (was %d)", round, n)
		if round > 40 {
			t.Fatal("not draining")
		}
	}
}
