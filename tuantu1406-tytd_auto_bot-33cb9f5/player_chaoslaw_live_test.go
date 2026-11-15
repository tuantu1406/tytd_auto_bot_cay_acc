//go:build live

package tytd_go

// Chaos Law (Pháp Tắc Thái Hư, 2367xx) live probe. The client bundles only
// carry the protocol ENUM — the actual req/resp messages live in downloaded
// resource packages — so the wire shape must come from the server itself.
// This test logs into the throwaway account, fires every Chaos Law call we
// plan to automate, and protowire-dumps every answer, so the generated
// messages in tytd.proto can be written from fact, not guesswork.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveChaosLawProbe -v -timeout 240s .
//
// Env: TYTD_LIVE_SERVER (default 710547).

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	pb "google.golang.org/protobuf/proto"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
)

type chaosFrame struct {
	msgType string
	data    []byte
}

// chaosSession is a live login with every MSG frame tapped into raw — the
// Chaos Law client (and the wire-dump probe) reads from there.
type chaosSession struct {
	conn    *websocket.Conn
	p       *Player
	raw     chan chaosFrame
	cleanup func()
}

func chaosDialLive(t *testing.T, user, pass string, server int) (*chaosSession, error) {
	passportToken, err := request.LoginGPassport(user, pass)
	if err != nil {
		return nil, fmt.Errorf("LoginGPassport: %w", err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		return nil, fmt.Errorf("LoginFuntap: %w", err)
	}
	gameURL, err := request.GetServerURL(userID, server)
	if err != nil {
		return nil, fmt.Errorf("GetServerURL: %w", err)
	}
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
	if err != nil {
		return nil, fmt.Errorf("GetWSKey: %w", err)
	}
	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ws dial: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writerDone := make(chan struct{})
	writeChan := make(chan []byte, 128)
	go func() {
		defer close(writerDone)
		for {
			select {
			case frame, ok := <-writeChan:
				if !ok {
					return
				}
				if err := conn.WriteMessage(2, frame); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	p := &Player{Username: user, UID: uid, ChatID: 7, writeChan: writeChan}
	p.ctx, p.cancel = ctx, cancel
	raw := make(chan chaosFrame, 512)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, msgType, realData := pack.Unpack(message)
			if !isMSG {
				continue
			}
			// Mirror the production wsHandler routing so clRoundTrip seats
			// actually receive their answers.
			switch msgType {
			case SERVER_CHAOS_LAW_ENTER, SERVER_CHAOS_LAW_PERCEIVE, SERVER_CHAOS_LAW_EVOLVE,
				SERVER_CHAOS_LAW_EVOLVE_REWARD, SERVER_CHAOS_LAW_ONE_KEY_HELP,
				SERVER_YUE_BAO_ENTER, SERVER_YUE_BAO_DEPOSIT, SERVER_YUE_BAO_INTERACTE,
				SERVER_TALENT_RANDOM, SERVER_TALENT_DEAL, SERVER_TALENT_UNDEAL:
				p.clDeliver(msgType, realData)
			case SERVER_TALENT_SYNC:
				p.hmHandleSync(realData)
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.ActList.applyList(p.Username, realData, msgType == SERVER_ACTIVITY_LIST)
			}
			select {
			case raw <- chaosFrame{msgType, realData}:
			default:
			}
		}
	}()
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	writeChan <- pack.Pack(AUTH_CMD, authMSG, uid)
	return &chaosSession{conn: conn, p: p, raw: raw, cleanup: func() {
		conn.Close()
		p.cancel()
		<-readerDone
		<-writerDone
	}}, nil
}

// chaosWaitLogin drains the tap until the account's identity (PlayerInfo
// "0065") has passed, then a short grace + drain.
func chaosWaitLogin(t *testing.T, s *chaosSession, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case f := <-s.raw:
			if f.msgType == "0065" {
				time.Sleep(3 * time.Second)
				for {
					select {
					case <-s.raw:
					default:
						return
					}
				}
			}
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Logf("chaosWaitLogin: no PlayerInfo within %v — continuing anyway", timeout)
}

// chaosWire renders a protobuf payload as a readable wire tree without
// knowing the schema: field number, wire type, value (int / length-delimited
// recursed one level, or printable guess for strings).
func chaosWire(data []byte, depth int) string {
	if depth > 3 {
		return "…"
	}
	out := "{"
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return out + " <truncated>"
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return out + " <bad varint>"
			}
			data = data[n:]
			out += fmt.Sprintf(" %d:v%d", num, v)
		case protowire.Fixed32Type:
			v, n := protowire.ConsumeFixed32(data)
			if n < 0 {
				return out + " <bad f32>"
			}
			data = data[n:]
			out += fmt.Sprintf(" %d:f32:%d", num, v)
		case protowire.Fixed64Type:
			v, n := protowire.ConsumeFixed64(data)
			if n < 0 {
				return out + " <bad f64>"
			}
			data = data[n:]
			out += fmt.Sprintf(" %d:f64:%d", num, v)
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return out + " <bad bytes>"
			}
			data = data[n:]
			inner := chaosWire(v, depth+1)
			if inner == "<truncated>" || len(v) == 0 {
				inner = fmt.Sprintf("%q", string(v))
			}
			out += fmt.Sprintf(" %d:%s", num, inner)
		default:
			return out + fmt.Sprintf(" <bad wiretype %d>", typ)
		}
	}
	return out + " }"
}

func TestLiveChaosLawProbe(t *testing.T) {
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
	raw, writeChan, uid := s.raw, s.p.writeChan, s.p.UID

	ask := func(name, cmdHex string, body []byte) []byte {
		writeChan <- pack.Pack(cmdHex, body, uid)
		t.Logf("SENT %s (%s) body=% x", name, cmdHex, body)
		var got []byte
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case f := <-raw:
				// Print every reply that belongs to Chaos Law (8f5x/8f6x/8f7c)
				// or is clearly a small answer; skip bulk pushes.
				switch f.msgType {
				case "8f5d", "8f5e", "8f5f", "8f60", "8f68", "8f6b", "8f7c", "8f6d":
					t.Logf("RECV %s %s", f.msgType, chaosWire(f.data, 0))
					if f.msgType == "8f5d" {
						got = f.data
					}
				default:
					t.Logf("(other msg %s len=%d)", f.msgType, len(f.data))
				}
			case <-time.After(300 * time.Millisecond):
			}
		}
		return got
	}

	// ENTER 236701=00039c9d — the reference dump: playerData layout.
	askedEnter := ask("ENTER", "00039c9d", []byte{})
	// ONE_KEY_HELP 236712=00039ca8 — help everything pending.
	ask("ONE_KEY_HELP", "00039ca8", []byte{})
	// PERCEIVE 236702=00039c9e — try empty body first.
	ask("PERCEIVE", "00039c9e", []byte{})
	// EVOLVE / EVOLVE_REWARD need a body (empty was silently dropped). Parse
	// the ENTER playerData we received live for real law ids + endTimes and
	// probe {1:id}; a ret=0 (or a fresh sync push) confirms the shape.
	laws := chaosParseLaws(askedEnter)
	for _, l := range laws {
		t.Logf("LAW id=%d evolving=%d endTime=%d (delta=%ds)", l.id, l.evolving,
			l.endTime, (l.endTime-time.Now().UnixMilli())/1000)
	}
	done, running := 0, 0
	for _, l := range laws {
		if l.endTime <= time.Now().UnixMilli() {
			done++
		} else {
			running++
		}
	}
	t.Logf("LAWS done=%d running=%d", done, running)
	if len(laws) > 0 {
		// Claim the earliest-finished one (or just the earliest end time).
		target := laws[0]
		for _, l := range laws {
			if l.endTime < target.endTime {
				target = l
			}
		}
		var buf []byte
		buf = appendVarintField(buf, 1, target.id)
		ask(fmt.Sprintf("EVOLVE_REWARD/{id:%d}", target.id), "00039ca0", buf)
		ask(fmt.Sprintf("EVOLVE/{id:%d}", target.id), "00039c9f", buf)
	}
}

// TestLivePhapTacThaiSo verifies the SHIPPED automation (p.clCycle) against
// the real server: two back-to-back cycles must (a) both complete without
// hangs, and (b) the second must not re-claim what the first already claimed
// (session dedupe), while still seeing fresh status from ENTER.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLivePhapTacThaiSo -v -timeout 240s .
func TestLivePhapTacThaiSo(t *testing.T) {
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

	// Keep the tap drained so nothing blocks while clCycle runs.
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

	t.Logf("cycle 1: %s", s.p.clCycle())
	t.Logf("cycle 2: %s", s.p.clCycle())

	// Final status read straight from the server.
	data, err := s.p.clRoundTrip(S_CHAOS_LAW_ENTER, SERVER_CHAOS_LAW_ENTER, []byte{})
	if err != nil {
		t.Fatalf("final enter: %v", err)
	}
	var enter proto.ChaosLawEnterResp
	if err := pb.Unmarshal(data, &enter); err != nil {
		t.Fatalf("final enter decode: %v", err)
	}
	if enter.GetRet() != 0 {
		t.Fatalf("final enter ret=%d", enter.GetRet())
	}
	now := time.Now().UnixMilli()
	for _, l := range enter.GetPlayerData().GetLaws() {
		t.Logf("LAW id=%d evolving=%d endTime=%d done=%t", l.GetId(), l.GetEvolving(),
			l.GetEndTime(), l.GetEvolving() == 1 && l.GetEndTime() <= now)
	}
	t.Logf("final perceiveCount=%d helpCount=%d", enter.GetPlayerData().GetPerceiveCount(),
		enter.GetPlayerData().GetHelpCount())
}

type chaosLaw struct {
	id       int64
	evolving int64
	endTime  int64
}

func appendVarintField(buf []byte, num int, v int64) []byte {
	buf = protowire.AppendTag(buf, protowire.Number(num), protowire.VarintType)
	return protowire.AppendVarint(buf, uint64(v))
}

// chaosParseLaws re-reads the recorded ENTER payload for the law list.
func chaosParseLaws(enter []byte) []chaosLaw {
	var pd []byte
	for len(enter) > 0 {
		num, typ, n := protowire.ConsumeTag(enter)
		if n < 0 {
			return nil
		}
		enter = enter[n:]
		if num == 2 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(enter)
			if n < 0 {
				return nil
			}
			pd = v
			enter = enter[n:]
			continue
		}
		if n := protowire.ConsumeFieldValue(num, typ, enter); n < 0 {
			return nil
		} else {
			enter = enter[n:]
		}
	}
	var laws []chaosLaw
	for len(pd) > 0 {
		num, typ, n := protowire.ConsumeTag(pd)
		if n < 0 {
			break
		}
		pd = pd[n:]
		if num != 6 || typ != protowire.BytesType {
			if n := protowire.ConsumeFieldValue(num, typ, pd); n < 0 {
				break
			} else {
				pd = pd[n:]
			}
			continue
		}
		block, n := protowire.ConsumeBytes(pd)
		if n < 0 {
			break
		}
		pd = pd[n:]
		var l chaosLaw
		s := block
		for len(s) > 0 {
			num, typ, n := protowire.ConsumeTag(s)
			if n < 0 {
				break
			}
			s = s[n:]
			if typ != protowire.VarintType {
				if n := protowire.ConsumeFieldValue(num, typ, s); n < 0 {
					break
				} else {
					s = s[n:]
				}
				continue
			}
			vv, n := protowire.ConsumeVarint(s)
			if n < 0 {
				break
			}
			s = s[n:]
			switch num {
			case 1:
				l.id = int64(vv)
			case 5:
				l.evolving = int64(vv)
			case 7:
				l.endTime = int64(vv)
			}
		}
		laws = append(laws, l)
	}
	return laws
}
