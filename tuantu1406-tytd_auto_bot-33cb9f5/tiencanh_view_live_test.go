//go:build live

package tytd_go

// VIEW-SOURCE probe (owner live test 2026-10-18): the engine live pass found
// scene.view EMPTY on ENTER (floor 20, pos 6,14, hp 30 — march never fired).
// This dumps (a) EVERY frame type that arrives after AUTH and after ENTER,
// (b) the full top-level field map of the 48a9 ENTER answer (protowire walk,
// incl. fields our proto may drop), so we can see whether the board rides a
// push opcode we never route, or lives under a different scene field.
//
//	TYTD_LIVE=1 TYTD_WYVIEW=1 go test -tags "sqlcipher live" -count=1 -run TestTienCanhViewLive -v -timeout 240s .

import (
	"encoding/hex"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	pb "google.golang.org/protobuf/proto"
)

func TestTienCanhViewLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_WYVIEW") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_WYVIEW=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710564
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}
	pt, err := request.LoginGPassport(user, pass)
	if err != nil {
		t.Fatal(err)
	}
	sd, uidF, err := request.LoginFuntap(pt)
	if err != nil {
		t.Fatal(err)
	}
	gu, err := request.GetServerURL(uidF, server)
	if err != nil {
		t.Fatal(err)
	}
	token, wsURL, uid, err := request.GetWSKey(gu, sd)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	write := make(chan []byte, 256)
	go func() {
		for f := range write {
			_ = conn.WriteMessage(2, f)
		}
	}()
	all := make(chan [2]string, 16384)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			isMSG, ty, d := pack.Unpack(msg)
			if !isMSG {
				continue
			}
			select {
			case all <- [2]string{ty, hex.EncodeToString(d)}:
			default:
			}
		}
	}()
	kwStop := make(chan struct{})
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-kwStop:
				return
			case <-tk.C:
				write <- pack.Pack(PING_CMD, []byte{}, uid)
			}
		}
	}()
	defer close(kwStop)
	authMSG, _ := pb.Marshal(&proto.Auth{Token: token, Locale: "vi_vn", Status: pb.Bool(false)})
	write <- pack.Pack(AUTH_CMD, authMSG, uid)

	// find actID from the activity list
	var actID int32
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) && actID == 0 {
		select {
		case a := <-all:
			if a[0] == SERVER_ACTIVITY_LIST {
				raw, _ := hex.DecodeString(a[1])
				var push proto.PushActivityList
				if pb.Unmarshal(raw, &push) == nil {
					now := time.Now()
					for _, c := range push.GetMainConfig() {
						if c.GetType() == 186 && entryFromConfig(c).status(now) == "active" {
							actID = c.GetActivityId()
						}
					}
				}
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if actID == 0 {
		t.Fatal("no active 186")
	}
	t.Logf("actID=%d", actID)

	// ENTER_ACTIVITY then ENTER, dump everything that answers
	body, _ := pb.Marshal(&proto.WeYoundEnterActivityReq{ActivityId: pb.Int32(actID)})
	write <- pack.Pack(WE_YOUND_ENTER_ACTIVITY_CMD, body, uid)
	time.Sleep(2 * time.Second)
	write <- pack.Pack(WE_YOUND_ENTER_CMD, body, uid)

	end := time.Now().Add(8 * time.Second)
	seenTypes := map[string]int{}
	var enterRaw []byte
	for time.Now().Before(end) {
		select {
		case a := <-all:
			seenTypes[a[0]]++
			if a[0] == SERVER_WE_YOUND_ENTER && enterRaw == nil {
				enterRaw, _ = hex.DecodeString(a[1])
			}
			// log every 48xx-band frame (WeYound family) + anything new
			if len(a[0]) == 4 && (a[0][:2] == "48" || a[0][:2] == "49") {
				t.Logf("frame %s len=%d head=%s", a[0], len(a[1])/2, a[1][:min(80, len(a[1]))])
			}
		case <-time.After(150 * time.Millisecond):
		}
	}
	types := make([]string, 0, len(seenTypes))
	for k := range seenTypes {
		types = append(types, k)
	}
	sort.Strings(types)
	t.Logf("frame types after ENTER: %v", types)

	if enterRaw == nil {
		t.Fatal("no 48a9")
	}
	// Top-level field walk of the ENTER answer: field numbers + wire types +
	// sizes, so we can spot a fat field our proto drops. Keep the RAW scene
	// bytes (the re-marshal hides exactly what our proto drops).
	var rawScene []byte
	var topCells [][]byte
	b := enterRaw
	for len(b) > 0 {
		num, wt, n := protowire.ConsumeField(b)
		if n <= 0 {
			t.Logf("walk break at %d", len(b))
			break
		}
		val := b[:n]
		t.Logf("ENTER f%d wt=%d len=%d", num, wt, len(val))
		if num == 1 && wt == protowire.VarintType {
			v, _ := protowire.ConsumeVarint(val)
			t.Logf("  → ret=%d", v)
		}
		if num == 2 && wt == protowire.BytesType {
			// val starts with the field TAG — ConsumeBytes starts at the
			// length varint, so skip the tag first.
			if body, _ := protowire.ConsumeBytes(val[1:]); len(body) > 0 {
				rawScene = body
			}
		}
		if num == 5 && wt == protowire.BytesType {
			if emb, _ := protowire.ConsumeBytes(val[1:]); len(emb) > 0 {
				topCells = append(topCells, emb)
			}
		}
		b = b[n:]
	}
	t.Logf("top-level f5 cells=%d, rawScene=%d bytes", len(topCells), len(rawScene))
	// Are the top-level f5 entries cells? Decode the first 3 as WeYoundCellMsg.
	for i, cb := range topCells {
		if i >= 3 {
			break
		}
		var c proto.WeYoundCellMsg
		if pb.Unmarshal(cb, &c) == nil {
			p := c.GetPos()
			t.Logf("topcell#%d type=%d pos=(%d,%d) reachable=%v hasEvent=%v",
				i, c.GetType(), p.GetX(), p.GetY(), c.GetReachable(), c.GetEvent() != nil)
		} else {
			t.Logf("topcell#%d NOT a cell (len=%d head=%x)", i, len(cb), cb[:min(16, len(cb))])
		}
	}
	// RAW scene walk (NOT the re-marshalled copy) — strip the outer field
	// header FIRST (val still carries tag+len), then walk the scene's own
	// fields and decode every length-delimited repeated as a cell candidate.
	if rawScene != nil {
		t.Logf("walking %d scene bytes", len(rawScene))
		cellLog := 0
		b2 := rawScene
		for len(b2) > 0 {
			num, wt, n := protowire.ConsumeField(b2)
			if n <= 0 {
				break
			}
			val := b2[:n]
			t.Logf("RAWscene f%d wt=%d len=%d", num, wt, len(val))
			if wt == protowire.BytesType {
				emb, _ := protowire.ConsumeBytes(val[1:]) // skip tag byte
				var c proto.WeYoundCellMsg
				errC := pb.Unmarshal(emb, &c)
				if num == 5 && cellLog < 6 {
					cellLog++
					t.Logf("  cell raw=%x unmarshalErr=%v pos=%v", emb, errC, c.GetPos())
				}
				if errC == nil && c.GetPos() != nil {
					t.Logf("  → cell f%d type=%d pos=(%d,%d)", num, c.GetType(),
						c.GetPos().GetX(), c.GetPos().GetY())
				}
			}
			b2 = b2[n:]
		}
	}
}
