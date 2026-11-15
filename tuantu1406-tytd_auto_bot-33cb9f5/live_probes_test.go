//go:build live

package tytd_go

import (
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	pb "google.golang.org/protobuf/proto"

	"github.com/gorilla/websocket"
)

// TestLiveRewardProbes is a schema-discovery probe for the three claim
// systems being converted to poll-check-then-claim. It logs in with the
// leader's throwaway test account (state-changing calls allowed on it only),
// asks each system's panel/claim command with an empty body, and dumps the
// raw protobuf wire tree of every answer (field numbers + wire types +
// values) so the Go proto messages can be written from what the server
// actually sends rather than guesses. Read-mostly; the two claim probes
// (216201 free chip, 211814 ad reward) spend at most today's free budget on
// the throwaway account.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveRewardProbes -v -timeout 240s .
func TestLiveRewardProbes(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveUser   = liveUserEnv(t)
		livePass   = livePassEnv(t)
		liveServer = 710547 // the union bounty test server — account has a bang hội here
	)

	passportToken, err := request.LoginGPassport(liveUser, livePass)
	if err != nil {
		t.Fatalf("LoginGPassport: %v", err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatalf("LoginFuntap: %v", err)
	}
	gameURL, err := request.GetServerURL(userID, liveServer)
	if err != nil {
		t.Fatalf("GetServerURL: %v", err)
	}
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
	if err != nil {
		t.Fatalf("GetWSKey: %v", err)
	}

	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	wrCh := make(chan []byte, 16)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for frame := range wrCh {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	defer func() { close(wrCh); <-writerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	wrCh <- pack.Pack(AUTH_CMD, authMSG, uid)

	// Every interesting transport type lands here; the test names them.
	watch := map[string]string{
		"0845": "22117 union daily panel",
		"0846": "22118 union daily claim",
		"3f37": "216207 union treasure ENTER",
		"3f31": "216201 union treasure DRAW_CHIP",
		"2e22": "211810 pupil SECT_INFO",
		"2e26": "211814 pupil GET_AD_REWARD",
		"2e25": "211813 pupil LOGIN_SYNC",
	}
	gotCH := make(chan string, 64)
	rawCH := make(chan [2]string, 64) // [type, hex]
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
			if name, ok := watch[msgType]; ok {
				select {
				case rawCH <- [2]string{msgType + " " + name, hex.EncodeToString(realData)}:
				default:
				}
				select {
				case gotCH <- msgType:
				default:
				}
			}
		}
	}()
	defer func() {
		conn.Close()
		<-readerDone
	}()

	// Let login pushes (incl. any PupilSystemLoginSync) arrive.
	time.Sleep(8 * time.Second)

	send := func(cmd, label string) {
		t.Logf("--- probe: %s", label)
		wrCh <- pack.Pack(cmd, []byte{}, uid)
		deadline := time.After(6 * time.Second)
		for {
			select {
			case pair := <-rawCH:
				t.Logf("answer %s\n  hex: %s\n  tree:\n%s", pair[0], pair[1], wireTree([]byte(hexDecode(t, pair[1])), 1))
			case <-deadline:
				t.Logf("no answer to %s within 6 s", label)
				return
			}
		}
	}

	send("00005665", "22117 union daily panel (expect progress + claimed list)")
	send("00034c8f", "216207 union treasure ENTER (expect mapDatas/freeCount)")
	send("00034c89", "216201 union treasure DRAW_CHIP empty body")
	send("00033b62", "211810 pupil SECT_INFO (expect free-ad counters)")
	send("00033b66", "211814 pupil GET_AD_REWARD empty body")

	// Drain stragglers.
	time.Sleep(2 * time.Second)
	for {
		select {
		case pair := <-rawCH:
			t.Logf("late answer %s\n  hex: %s\n  tree:\n%s", pair[0], pair[1], wireTree([]byte(hexDecode(t, pair[1])), 1))
		default:
			return
		}
	}
}

func hexDecode(t *testing.T, s string) string {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	return string(b)
}

// wireTree renders a protobuf message as a field-number tree without a
// schema (groups up to depth 4). Varints print as ints; length-delimited
// fields print as a quoted string if fully printable, otherwise "(bytes)"
// with a nested tree attempt.
func wireTree(b []byte, depth int) string {
	out := ""
	for len(b) > 0 {
		key, n := varint(b)
		if n <= 0 {
			return out + indent(depth) + "…truncated\n"
		}
		b = b[n:]
		fn, wt := key>>3, key&7
		switch wt {
		case 0:
			v, n := varint(b)
			if n <= 0 {
				return out + indent(depth) + "…truncated\n"
			}
			b = b[n:]
			out += fmt.Sprintf("%s%d: %d\n", indent(depth), fn, v)
		case 1:
			if len(b) < 8 {
				return out + indent(depth) + "…truncated\n"
			}
			out += fmt.Sprintf("%s%d: fixed64 %016x\n", indent(depth), fn, b[:8])
			b = b[8:]
		case 2:
			l, n := varint(b)
			if n <= 0 || uint64(len(b[n:])) < l {
				return out + indent(depth) + "…truncated\n"
			}
			b = b[n:]
			val := b[:l]
			b = b[l:]
			head := fmt.Sprintf("%s%d: len=%d", indent(depth), fn, l)
			if s, ok := printable(val); ok {
				out += fmt.Sprintf("%s %q\n", head, s)
			} else if depth < 4 {
				sub := wireTree(val, depth+1)
				if sub == "" {
					out += fmt.Sprintf("%s (bytes %x)\n", head, val)
				} else {
					out += fmt.Sprintf("%s {\n%s%s}\n", head, sub, indent(depth))
				}
			} else {
				out += fmt.Sprintf("%s (bytes %x)\n", head, val)
			}
		case 5:
			if len(b) < 4 {
				return out + indent(depth) + "…truncated\n"
			}
			out += fmt.Sprintf("%s%d: fixed32 %08x\n", indent(depth), fn, b[:4])
			b = b[4:]
		default:
			return out + indent(depth) + fmt.Sprintf("…bad wiretype %d at field %d\n", wt, fn)
		}
	}
	return out
}

func indent(d int) string {
	s := ""
	for i := 0; i < d; i++ {
		s += "  "
	}
	return s
}

func varint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, -1
}

// TestLiveActivityListProbe answers one question for the hourly activity
// refresh: the server pushes 21001/PushActivityList at login, but is there a
// pull? It captures the login push (so we can see the live list), then tries
// an empty-body request on 21001 (00005209) and 21002 (0000520a) and reports
// whether 03e9/040c arrives afterwards. Read-only; worst case the server
// ignores or drops the probe session.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveActivityListProbe -v -timeout 180s .
func TestLiveActivityListProbe(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveUser   = liveUserEnv(t)
		livePass   = livePassEnv(t)
		liveServer = 710547
	)

	passportToken, err := request.LoginGPassport(liveUser, livePass)
	if err != nil {
		t.Fatalf("LoginGPassport: %v", err)
	}
	sessionData, userID, err := request.LoginFuntap(passportToken)
	if err != nil {
		t.Fatalf("LoginFuntap: %v", err)
	}
	gameURL, err := request.GetServerURL(userID, liveServer)
	if err != nil {
		t.Fatalf("GetServerURL: %v", err)
	}
	token, wsURL, uid, err := request.GetWSKey(gameURL, sessionData)
	if err != nil {
		t.Fatalf("GetWSKey: %v", err)
	}

	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	wrCh := make(chan []byte, 16)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for frame := range wrCh {
			if err := conn.WriteMessage(2, frame); err != nil {
				return
			}
		}
	}()
	defer func() { close(wrCh); <-writerDone }()

	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	wrCh <- pack.Pack(AUTH_CMD, authMSG, uid)

	listCH := make(chan string, 32) // hex of every 03e9/040c payload
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
			if msgType == SERVER_ACTIVITY_LIST || msgType == SERVER_ACTIVITY_LIST_UPDATE {
				select {
				case listCH <- msgType + " " + hex.EncodeToString(realData):
				default:
				}
			}
		}
	}()
	defer func() {
		conn.Close()
		<-readerDone
	}()

	dump := func(tag string) {
		for {
			select {
			case s := <-listCH:
				b, _ := hex.DecodeString(s[5:])
				var push proto.PushActivityList
				if err := pb.Unmarshal(b, &push); err != nil {
					t.Logf("%s: unmarshal %s: %v", tag, s[:4], err)
					continue
				}
				var ids []string
				for _, e := range push.MainConfig {
					ids = append(ids, fmt.Sprintf("%d:t%d", e.GetActivityId(), e.GetType()))
				}
				t.Logf("%s: %s %d entries: %v", tag, s[:4], len(push.MainConfig), ids)
			default:
				return
			}
		}
	}

	time.Sleep(8 * time.Second)
	t.Log("--- login push:")
	dump("login")

	send := func(cmd, label string) {
		t.Logf("--- probe: %s", label)
		wrCh <- pack.Pack(cmd, []byte{}, uid)
		time.Sleep(6 * time.Second)
		dump("after " + label)
	}
	send("00005209", "empty 21001 request")
	send("0000520a", "empty 21002 request")
	time.Sleep(2 * time.Second)
	dump("late")
}
