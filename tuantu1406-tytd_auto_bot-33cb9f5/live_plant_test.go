//go:build live

package tytd_go

import (
	"encoding/binary"
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

// TestLiveGodIslandPlant probes the plant/liquid flow live (same throwaway
// test account, same TYTD_LIVE gate as TestLiveGodIsland):
//
//	ACTIVITY_GET_DATA (21003)  → raw-walk RspGetActivityDetail to pin down the
//	                             paramConfig field numbers and read
//	                             GODISLAND_ITEM_ENERGY (the liquidId list)
//	PLANT_INFO      (206836)   → decode plant state + pending liquid notices
//	PLANT_WATER     (206839)   → collect every status==0 notice by its id
//	RECEIVE_FRUITS  (206838)   → claim once canReceive == 1
//	SEND_LIQUID     (206851)   → donate 1 of each configured liquid item
//
// WATER/RECEIVE_FRUITS/SEND_LIQUID all change server state (they consume the
// throwaway account's donated liquid + bag items) — authorized by the leader
// for this test account only.
//
//	TYTD_LIVE=1 go test -count=1 -tags "sqlcipher live" -run TestLiveGodIslandPlant -v -timeout 240s .
func TestLiveGodIslandPlant(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" {
		t.Skip("set TYTD_LIVE=1 to run the live server test")
	}
	var (
		liveUser   = liveUserEnv(t)
		livePass   = livePassEnv(t)
		liveServer = 710564
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

	p := &EventPlayer{Username: liveUser, UID: uid, Running: true}

	// one buffered channel per incoming type we care about; reader keeps only
	// the latest frame per type (we always wait between requests).
	chs := map[string]chan []byte{}
	for _, typ := range []string{SERVER_ACTIVITY_DETAIL, SERVER_GOD_ISLAND_PLANT_INFO,
		SERVER_GOD_ISLAND_PLANT_WATER, SERVER_GOD_ISLAND_RECEIVE_FRUITS,
		SERVER_GOD_ISLAND_SEND_LIQUID} {
		chs[typ] = make(chan []byte, 1)
	}
	activityCH := make(chan []byte, 4)
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
			switch msgType {
			case SERVER_ACTIVITY_LIST, SERVER_ACTIVITY_LIST_UPDATE:
				p.giHandleActivityList(realData)
				select {
				case activityCH <- realData:
				default:
				}
			case SERVER_GOD_ISLAND_PLANT_INFO, SERVER_GOD_ISLAND_PLANT_WATER,
				SERVER_GOD_ISLAND_RECEIVE_FRUITS, SERVER_GOD_ISLAND_SEND_LIQUID,
				SERVER_ACTIVITY_DETAIL:
				select {
				case chs[msgType] <- realData:
				default:
				}
			}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()

	pingQuit := make(chan struct{})
	go func() {
		for {
			select {
			case <-pingQuit:
				return
			case <-time.After(5 * time.Second):
			}
			select {
			case wrCh <- pack.Pack(PING_CMD, []byte{}, uid):
			default:
			}
		}
	}()
	defer close(pingQuit)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if activityIDOf(p) != 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	activityID := activityIDOf(p)
	if activityID == 0 {
		t.Skip("login + push ok, but Đảo Bồng Lai is not open on this server right now")
	}
	t.Logf("God Island activity id = %d", activityID)

	await := func(typ string, d time.Duration) []byte {
		select {
		case data := <-chs[typ]:
			return data
		case <-time.After(d):
			t.Fatalf("server did not answer type %s", typ)
			return nil
		}
	}

	// --- 1. activity detail: raw-walk to find paramConfig + ENERGY config ---
	detailMSG, _ := pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: activityID})
	wrCh <- pack.Pack(ACTIVITY_GET_DATA_CMD, detailMSG, uid)
	detailRaw := await(SERVER_ACTIVITY_DETAIL, 20*time.Second)
	t.Logf("RspGetActivityDetail: %d bytes", len(detailRaw))
	protoWalk(t, detailRaw, "", 4)

	energy := findConfigString(detailRaw, "GODISLAND_ITEM_ENERGY")
	if energy == "" {
		t.Logf("GODISLAND_ITEM_ENERGY not found in detail — checking raw dump above")
	} else {
		t.Logf("GODISLAND_ITEM_ENERGY = %q", energy)
	}

	// --- 2. plant info ---
	plantMSG, _ := pb.Marshal(&proto.GodIslandGamePlantInfoReqMsg{ActivityId: activityID})
	wrCh <- pack.Pack(GOD_ISLAND_PLANT_INFO_CMD, plantMSG, uid)
	piData := await(SERVER_GOD_ISLAND_PLANT_INFO, 20*time.Second)
	var pi proto.GodIslandGamePlantInfoRespMsg
	if err := pb.Unmarshal(piData, &pi); err != nil {
		t.Fatalf("plant info decode: %v", err)
	}
	t.Logf("plant info: ret=%d lv=%d exp=%d canReceive=%d waterCount=%d notices=%d",
		pi.GetRet(), pi.GetLv(), pi.GetExp(), pi.GetCanReceive(), pi.GetWaterCount(),
		len(pi.GetNoticeInfo()))
	pending := []int64{}
	for _, n := range pi.GetNoticeInfo() {
		t.Logf("  notice: id=%d liquidId=%d status=%d name=%q time=%d",
			n.GetId(), n.GetLiquidId(), n.GetStatus(), n.GetName(), n.GetTime())
		if n.GetStatus() == 0 {
			pending = append(pending, n.GetId())
		}
	}

	// --- 3. water (receive) every pending notice by its id ---
	canReceive := pi.GetCanReceive()
	for _, id := range pending {
		waterMSG, _ := pb.Marshal(&proto.GodIslandGamePlantWaterReqMsg{ActivityId: activityID, Id: id})
		wrCh <- pack.Pack(GOD_ISLAND_PLANT_WATER_CMD, waterMSG, uid)
		wData := await(SERVER_GOD_ISLAND_PLANT_WATER, 15*time.Second)
		var w proto.GodIslandGamePlantWaterRespMsg
		if err := pb.Unmarshal(wData, &w); err != nil {
			t.Fatalf("water decode: %v", err)
		}
		t.Logf("water id=%d: ret=%d exp=%d canReceive=%d lv=%d waterCount=%d reward=%q",
			id, w.GetRet(), w.GetExp(), w.GetCanReceive(), w.GetLv(), w.GetWaterCount(), w.GetReward())
		if w.GetRet() == 0 {
			canReceive = w.GetCanReceive()
		}
	}

	// --- 4. claim fruit once ripe ---
	if canReceive == 1 {
		fruitMSG, _ := pb.Marshal(&proto.GodIslandGamePlantReceiveFruitsReqMsg{ActivityId: activityID})
		wrCh <- pack.Pack(GOD_ISLAND_RECEIVE_FRUITS_CMD, fruitMSG, uid)
		fData := await(SERVER_GOD_ISLAND_RECEIVE_FRUITS, 15*time.Second)
		var f proto.GodIslandGamePlantReceiveFruitsRespMsg
		if err := pb.Unmarshal(fData, &f); err != nil {
			t.Fatalf("receive fruits decode: %v", err)
		}
		t.Logf("receive fruits: ret=%d lv=%d exp=%d canReceive=%d reward=%q",
			f.GetRet(), f.GetLv(), f.GetExp(), f.GetCanReceive(), f.GetReward())
	} else {
		t.Logf("plant not ripe (canReceive=%d), skipping RECEIVE_FRUITS", canReceive)
	}

	// --- 5. donate one of each configured liquid item ---
	liquidIDs := giParseLiquidIDs(energy)
	if len(liquidIDs) == 0 {
		t.Logf("no liquidIds discovered from config — skipping SEND_LIQUID")
		return
	}
	for _, lid := range liquidIDs {
		sMSG, _ := pb.Marshal(&proto.GodIslandGameSendLiquidReqMsg{
			ActivityId: activityID, LiquidId: lid, SendNum: 1})
		wrCh <- pack.Pack(GOD_ISLAND_SEND_LIQUID_CMD, sMSG, uid)
		sData := await(SERVER_GOD_ISLAND_SEND_LIQUID, 15*time.Second)
		var s proto.GodIslandGameSendLiquidRespMsg
		if err := pb.Unmarshal(sData, &s); err != nil {
			t.Fatalf("send liquid decode: %v", err)
		}
		t.Logf("send liquid id=%d: ret=%d notice(id=%d liquidId=%d status=%d)",
			lid, s.GetRet(), s.GetNoticeInfo().GetId(), s.GetNoticeInfo().GetLiquidId(),
			s.GetNoticeInfo().GetStatus())
	}
}

// ---------------------------------------------------------------------------
// Generic protobuf wire walker — used only by the live probe to discover
// field numbers (e.g. ActivityParamConfig) before they are pinned in the .proto.
// ---------------------------------------------------------------------------

func protoWalk(t *testing.T, b []byte, path string, maxDepth int) {
	t.Helper()
	if maxDepth < 0 {
		return
	}
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			t.Logf("%s: <truncated varint, %d bytes left>", path, len(b))
			return
		}
		b = b[n:]
		field, wt := key>>3, key&7
		fpath := fmt.Sprintf("%s.%d", path, field)
		switch wt {
		case 0: // varint
			v, n := binary.Uvarint(b)
			if n <= 0 {
				t.Logf("%s: <bad varint>", fpath)
				return
			}
			b = b[n:]
			t.Logf("%s = %d", fpath, v)
		case 1: // fixed64
			if len(b) < 8 {
				return
			}
			t.Logf("%s = fixed64 %d", fpath, binary.LittleEndian.Uint64(b))
			b = b[8:]
		case 2: // length-delimited
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b[n:])) < l {
				t.Logf("%s: <bad len>", fpath)
				return
			}
			b = b[n:]
			payload := b[:l]
			b = b[l:]
			if s, ok := printable(payload); ok {
				t.Logf("%s = %q", fpath, s)
				continue
			}
			t.Logf("%s = <msg %d bytes>", fpath, l)
			protoWalk(t, payload, fpath, maxDepth-1)
		case 5: // fixed32
			if len(b) < 4 {
				return
			}
			t.Logf("%s = fixed32 %d", fpath, binary.LittleEndian.Uint32(b))
			b = b[4:]
		default:
			t.Logf("%s: <unknown wiretype %d>", fpath, wt)
			return
		}
	}
}

func printable(b []byte) (string, bool) {
	if len(b) == 0 || len(b) > 4096 {
		return "", false
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			if c < 0x80 { // allow utf-8 continuation/lead bytes
				return "", false
			}
		}
	}
	return string(b), true
}

// findConfigString walks RspGetActivityDetail bytes looking for the paramConfig
// entry whose param equals key; it returns the adjacent printable value string
// that follows the key inside the same length-delimited entry.
func findConfigString(raw []byte, key string) string {
	needle := []byte(key)
	i := 0
	for {
		j := indexBytes(raw[i:], needle)
		if j < 0 {
			return ""
		}
		j += i
		// param string is stored as {key} at some offset; the value string in
		// proto field order follows within a few hundred bytes as another
		// length-delimited printable field. Scan forward for a printable run
		// containing '|' or ';' (energy config format "id|n;id|n").
		end := j + len(needle)
		k, ok := nextPrintableWith(raw, end, end+512, []byte{'|'})
		if ok {
			return k
		}
		i = end
	}
}

// nextPrintableWith finds the longest printable run starting at or after from
// (within limit) that contains the given char; skips varint length prefixes
// greedily by only accepting runs that decode cleanly.
func nextPrintableWith(b []byte, from, limit int, want []byte) (string, bool) {
	if limit > len(b) {
		limit = len(b)
	}
	for p := from; p < limit; p++ {
		// try to interpret b[p] as a length prefix
		l := int(b[p])
		if l < 2 || p+1+l > limit {
			continue
		}
		run := b[p+1 : p+1+l]
		if !allPrintable(run) {
			continue
		}
		s := string(run)
		for _, c := range want {
			if indexBytes(run, []byte{c}) >= 0 {
				return s, true
			}
		}
	}
	return "", false
}

func allPrintable(b []byte) bool {
	for _, c := range b {
		if (c < 0x20 || c > 0x7e) && c < 0x80 {
			return false
		}
	}
	return true
}

func indexBytes(h, n []byte) int {
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for k := range n {
			if h[i+k] != n[k] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
