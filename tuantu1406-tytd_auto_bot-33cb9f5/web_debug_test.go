package tytd_go

// TYTD_DEBUG_SERVE=127.0.0.1:PORT go test -count=1 -tags sqlcipher -run TestDebugServeWeb -timeout 0 .
//
// Debug-only harness: serves the real web routes with a logged-in leader
// cookie and one stubbed Cửu U event account (minimap/routes/line all
// answered like a resident account). Opens the door for a headless browser
// to reproduce "server pushed but page blank".

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func debugStubEvent(t *testing.T, chatID int64, uid int64) *EventPlayer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := &EventPlayer{
		Username: "EvSon", ChatID: chatID, UID: uint64(uid), Running: true,
		hlOpen: true, ctx: ctx, cancel: cancel,
		writeChan: make(chan []byte, 32),
	}
	st := p.hlState()
	p.HolyMu.Lock()
	st.Group, st.GameType = 200004, 1
	// The stream/display gate needs a round running NOW, not the season
	// announcement alone (2026-10-06 gate fix).
	st.IsOpen, st.OpenKnown = true, true
	st.ScheduleApply = []int64{hlLiveAnchor(time.Now())}
	p.HolyMu.Unlock()

	// Owner map: three unions across the bracket, one fight.
	ownerOf := func(id int32) (int64, string) {
		switch id % 3 {
		case 0:
			return 77, "Thiên Long"
		case 1:
			return 5, "Bái Hổ"
		}
		return 0, ""
	}
	minimap := func() []byte {
		var resp proto.HolyLandGameMiniMapInfoRespMsg
		add := func(id int32, fight bool) {
			c := &proto.HolyLandGameMiniCityInfo{CityId: pb.Int32(id), HaveFight: pb.Bool(fight)}
			if u, name := ownerOf(id); u != 0 {
				c.BaseData = &proto.HolyLandMiniUnionInfo{UnionId: pb.Int64(u), Name: pb.String(name)}
			}
			resp.CityInfo = append(resp.CityInfo, c)
		}
		for id := 1001; id <= 1012; id++ {
			add(int32(id), id == 1004)
		}
		for id := 2001; id <= 2015; id++ {
			add(int32(id), false)
		}
		for id := 3001; id <= 3006; id++ {
			add(int32(id), false)
		}
		add(4001, false)
		resp.Ret = pb.Int32(0)
		b, err := pb.Marshal(&resp)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	routes := func() []byte {
		now := time.Now().UnixMilli()
		mk := func(from, to int32, u int64, name string, off, dur int64) *proto.HolyLandGameRouteInfo {
			return &proto.HolyLandGameRouteInfo{
				FromCityId: pb.Int32(from), ToCityId: pb.Int32(to),
				Time: pb.Int64(now + off), Arrival: pb.Int64(now + off + dur),
				UnionId: pb.Int64(u), Name: pb.String(name),
			}
		}
		b, err := pb.Marshal(&proto.HolyLandGameCityInfoRespMsg{Ret: pb.Int32(0), RouteInfo: []*proto.HolyLandGameRouteInfo{
			mk(1001, 2003, 77, "Rồng Lửa", 0, 40000),
			mk(2005, 3002, 5, "MeoMu", 0, 25000),
			mk(3006, 4001, 77, "Khải Ca", 0, 60000),
		}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	line := func() []byte {
		pInfo := func(name, union string) *proto.HolyLandGameLinePlayerInfo {
			return &proto.HolyLandGameLinePlayerInfo{Name: pb.String(name), UnionName: pb.String(union)}
		}
		b, err := pb.Marshal(&proto.HolyLandGameTargetCityLineInfoRespMsg{
			Ret:        pb.Int32(0),
			AttackInfo: []*proto.HolyLandGameLinePlayerInfo{pInfo("Hắc Phong", "Thiên Long"), pInfo("Tiểu Long", "Thiên Long")},
			DefendInfo: []*proto.HolyLandGameLinePlayerInfo{pInfo("Thủ vệ thành", ""), pInfo("Bạch Hổ", "Bái Hổ")},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	go func() {
		for frame := range p.writeChan {
			switch string(frame[6:10]) {
			case hexCmdMini:
				p.hlDeliverMiniMap(minimap())
			case hexCmdCity:
				p.hlDeliverRoutes(routes())
			case hexCmdLine:
				p.hlDeliverLine(line())
			}
		}
	}()
	return p
}

// cmd hex as 4 raw bytes (frames carry the transport type at [6:10]).
const (
	hexCmdMini = "\x00\x03\x32\x76"
	hexCmdCity = "\x00\x03\x32\x72"
	hexCmdLine = "\x00\x03\x32\x74"
)

func TestDebugServeWeb(t *testing.T) {
	addr := os.Getenv("TYTD_DEBUG_SERVE")
	if addr == "" {
		t.Skip("TYTD_DEBUG_SERVE=127.0.0.1:PORT to run")
	}
	bot := newTestWebBot(t)
	seedLeader(t, bot, "8500")
	bot.session.Store(int64(8500), &Player{ChatID: 8500, UID: 8500, Username: "main"}) // logged-in main, not running the game
	bot.session.Store(int64(9000000001), debugStubEvent(t, 8500, 9000000001))
	if err := model.CreateEventUser(bot.ydb.GetDB(), &model.EventUser{
		ID: "9000000001", Username: "EvSon", ServerID: 710547, Running: true, LeaderID: "8500",
	}); err != nil {
		t.Fatal(err)
	}

	h := bot.webRoutes()
	// Sanity: the web router answers (also warms template parsing early).
	if _, cookie := loginAs(t, bot, h, 8500); cookie == "" {
		t.Fatal("loginAs produced no cookie")
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DEBUG WEB UP on http://%s — login via /dbglogin", addr)
	_ = http.Serve(ln, &redirectBootstrap{h: h, chatID: 8500})
}

// redirectBootstrap mints a fresh leader session at /dbglogin (so the cookie
// is always in the store at request time), then hands everything to the real
// router.
type redirectBootstrap struct {
	h      http.Handler
	chatID int64
}

func (b *redirectBootstrap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/dbglogin" {
		token, _ := webSessions.create(b.chatID)
		http.SetCookie(w, &http.Cookie{Name: webCookieName, Value: token, Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/events", http.StatusSeeOther)
		return
	}
	b.h.ServeHTTP(w, r)
}
