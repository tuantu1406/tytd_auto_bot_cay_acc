//go:build live

package tytd_go

// Overplant semantics probe (owner 2026-10-16, "chạy thử đi rồi mới trả lời"):
// does PLANT on a lot that is ALREADY GROWING destroy the old tree? One
// experiment, one lot, test acc (owner-authorized to mess with):
//
//	1. ENTER → pick one lot with state=2 (growing); snapshot every field.
//	2. explicit PLANT a DIFFERENT grass (prefer an ordinary 860xxx, so the
//	   diff is unambiguous) onto that SAME landId, isPlantElves=true.
//	3. record ret of 8ab0: refused (non-zero) = server protects the lot.
//	4. ENTER again → field-by-field diff of the lot: grassId (tree identity),
//	   roundStartTime (growth timer), canAward/hadReceiveRound (accumulated
//	   harvest rounds = the thing a player would actually lose).
//
//	TYTD_LIVE=1 TYTD_ELIXIR=1 TYTD_LIVE_USER=<live-user> TYTD_LIVE_PASS=<live-pass> TYTD_LIVE_SERVER=710547 \
//	  go test -tags "sqlcipher live" -count=1 -run TestDanGioiOverplantLive -v -timeout 300s .

import (
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/request"

	"github.com/gorilla/websocket"
	pb "google.golang.org/protobuf/proto"
)

func TestDanGioiOverplantLive(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_ELIXIR") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_ELIXIR=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710547
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
	all := make(chan [2]string, 8192)
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

	call := func(cmd, ansType string, body []byte, tag string) []byte {
		end := time.Now().Add(3 * time.Second) // drain the login firehose
		for time.Now().Before(end) {
			select {
			case <-all:
			case <-time.After(150 * time.Millisecond):
			}
		}
		write <- pack.Pack(cmd, body, uid)
		end = time.Now().Add(8 * time.Second)
		for time.Now().Before(end) {
			select {
			case a := <-all:
				if a[0] == ansType {
					raw, _ := hex.DecodeString(a[1])
					t.Logf("%s %s len=%d", tag, ansType, len(raw))
					return raw
				}
			case <-time.After(250 * time.Millisecond):
			}
		}
		t.Logf("!! %s: no %s answer", tag, ansType)
		return nil
	}
	enter := func(tag string) *proto.ElixirEnterHomeResp {
		b, _ := pb.Marshal(&proto.ElixirEnterHomeReq{PlayerId: pb.Int64(int64(uid))})
		raw := call(S_ELIXIR_ENTER, SERVER_DANGIOI_INFO, b, tag)
		if raw == nil {
			return nil
		}
		var rsp proto.ElixirEnterHomeResp
		if err := pb.Unmarshal(raw, &rsp); err != nil {
			t.Fatalf("%s: 8aae: %v", tag, err)
		}
		return &rsp
	}
	landStr := func(l *proto.ElixirLandInfo) string {
		return fmt.Sprintf("grass=%d lv=%d state=%d start=%d canAward=%d hadRecv=%d elves=%d canAwardElves=%d",
			l.GetGrassId(), l.GetGrassLv(), l.GetState(), l.GetRoundStartTime(),
			l.GetCanAwardRound(), l.GetHadReceiveRound(), l.GetElvesNum(), l.GetCanAwardElvesNum())
	}
	find := func(rsp *proto.ElixirEnterHomeResp, id int32) *proto.ElixirLandInfo {
		for _, l := range rsp.GetLands() {
			if l.GetLandId() == id {
				return l
			}
		}
		return nil
	}

	before := enter("ENTER-before")
	if before == nil {
		t.Fatal("no ENTER")
	}
	var victim *proto.ElixirLandInfo
	for _, l := range before.GetLands() {
		if l.GetState() == 2 && l.GetGrassId() != 0 {
			victim = l // first growing lot — that's our overplant target
			break
		}
	}
	if victim == nil {
		t.Fatal("no growing lot on this acc to test overwrite with")
	}
	t.Logf("VICTIM land %d BEFORE: %s", victim.GetLandId(), landStr(victim))

	// Choose a clearly DIFFERENT tree the account holds. Prefer a second
	// herb or any ordinary grass (unambiguous diff against the current one).
	var alts []int32
	for _, g := range before.GetUserInfo().GetGrassInfos() {
		if g.GetGrassId() != victim.GetGrassId() {
			alts = append(alts, g.GetGrassId())
		}
	}
	sort.Slice(alts, func(i, j int) bool { return alts[i] > alts[j] })
	if len(alts) == 0 {
		t.Fatal("account holds only one tree — cannot build an overwrite diff")
	}
	alt := alts[0]
	t.Logf("overplanting land %d with grass %d (was %d)", victim.GetLandId(), alt, victim.GetGrassId())

	pb1, _ := pb.Marshal(&proto.ElixirPlantGrassReq{
		GrassInfo:    []*proto.ElixirPlantGrassInfo{{LandId: pb.Int32(victim.GetLandId()), GrassId: pb.Int32(alt)}},
		IsPlantElves: pb.Bool(true),
	})
	raw := call(S_ELIXIR_PLANT, SERVER_ELIXIR_PLANT, pb1, "PLANT-overwrite")
	ret := int32(-1)
	if raw != nil {
		var rsp proto.ElixirPlantGrassResp
		pb.Unmarshal(raw, &rsp)
		ret = rsp.GetRet()
	}
	t.Logf("PLANT-overwrite ret = %d", ret)

	after := enter("ENTER-after")
	if after == nil {
		t.Fatal("no ENTER after")
	}
	v2 := find(after, victim.GetLandId())
	if v2 == nil {
		t.Fatal("victim land vanished after ENTER?!")
	}
	t.Logf("VICTIM land %d AFTER : %s", v2.GetLandId(), landStr(v2))

	switch {
	case ret != 0:
		t.Logf("VERDICT: server REFUSED the overwrite (ret=%d)", ret)
	case v2.GetGrassId() == victim.GetGrassId():
		t.Logf("VERDICT: ret=0 but grass unchanged — treated as no-op")
	default:
		t.Logf("VERDICT: OVERWRITE ACCEPTED — old tree %d → %d; round diff: canAward %d→%d, hadRecv %d→%d, start %d→%d",
			victim.GetGrassId(), v2.GetGrassId(),
			victim.GetCanAwardRound(), v2.GetCanAwardRound(),
			victim.GetHadReceiveRound(), v2.GetHadReceiveRound(),
			victim.GetRoundStartTime(), v2.GetRoundStartTime())
		if v2.GetHadReceiveRound() < victim.GetHadReceiveRound() {
			t.Logf("!!! hadReceiveRound DECREASED — claimed-history rewound")
		}
	}
}
