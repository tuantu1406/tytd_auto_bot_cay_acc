package tytd_go

// Event-delegation (uỷ thác) tests. The whole point: a delegated account shows
// up on the delegate leader's SỰ KIỆN tab (operable, orders reach it) but NEVER
// on their ĐỆ TỬ tab, and the delegate never learns the password or can mutate
// the account. Owner is notified on every delegated force-login.

import (
	"net/url"
	"strings"
	"testing"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// openKunlun gives a fresh EventPlayer a live Côn Lôn window + standing scene,
// enough for the argless tangtoc order to fire a real frame.
func openKunlun(t *testing.T, ep *EventPlayer, id string) {
	t.Helper()
	full, err := pb.Marshal(&proto.PushActivityList{MainConfig: []*proto.ActivityMainConfig{{
		ActivityId:    pb.Int32(777),
		Type:          pb.Int32(activityTypeKunlunWar),
		BeginShowTime: pb.Int64(time.Now().Add(-2 * time.Hour).UnixMilli()),
		BeginTime:     pb.Int64(time.Now().Add(-time.Hour).UnixMilli()),
		EndTime:       pb.Int64(time.Now().Add(time.Hour).UnixMilli()),
		EndShowTime:   pb.Int64(time.Now().Add(2 * time.Hour).UnixMilli()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ep.ActList.applyList(id, full, true)
	ep.ku = &kuEventState{SceneIn: true, Point: 123, SpeedUp: 4}
}

func TestDeputyValidateTarget(t *testing.T) {
	bot := newTestWebBot(t)
	db := bot.ydb.GetDB()
	seedLeader(t, bot, "8001") // valid target
	seedUser(t, bot, "8002", 30)
	seedUser(t, bot, "8003", 30)
	db.Model(&model.User{}).Where("id = ?", "8003").Update("is_leader", true)
	model.AddBannedUser(db, 8003, "banned-leader", "test")
	// leader flag on a MEMBER (LeaderID set) — must be rejected as a target.
	model.CreateUser(db, &model.User{ID: "8004", Username: "mem", LeaderID: "8001", IsLeader: true})

	if got, msg := deputyValidateTarget(db, "8002", "8001"); got != "8001" || msg != "" {
		t.Fatalf("valid leader: got %q %q", got, msg)
	}
	if got, msg := deputyValidateTarget(db, "8002", ""); got != "" || msg != "" {
		t.Fatalf("clear: got %q %q", got, msg)
	}
	for raw, why := range map[string]string{
		"8002": "self",
		"abc":  "garbage",
		"9999": "nonexistent",
		"8004": "member-as-leader",
		"8003": "banned leader",
		"-5":   "negative",
	} {
		if _, msg := deputyValidateTarget(db, "8002", raw); msg == "" {
			t.Fatalf("%s accepted", why)
		}
	}
}

func TestNotifyDeputyLoginRouting(t *testing.T) {
	bot := newTestWebBot(t)
	bot.notifyDeputyLogin(&model.User{ID: "8102", Username: "m", LeaderID: "8100", DeputyOf: "8101"}, 8101) // member → its leader
	bot.notifyDeputyLogin(&model.User{ID: "8110", Username: "r", DeputyOf: "8101"}, 8101)                   // regular → self
	bot.notifyDeputyLogin(&model.User{ID: "8120", Username: "x", LeaderID: "8101", DeputyOf: "8101"}, 8101) // owner==operator → skip
	bot.notifyDeputyLogin(&model.User{ID: "8130", Username: "y", DeputyOf: ""}, 8101)                       // no deputy → skip

	var got []int64
drain:
	for {
		select {
		case n := <-bot.notiChan:
			got = append(got, n.ChatID)
		default:
			break drain
		}
	}
	if len(got) != 2 {
		t.Fatalf("want exactly 2 notify (member leader + regular self), got %v", got)
	}
	if got[0] != 8100 || got[1] != 8110 {
		t.Fatalf("misrouted notify: %v (want 8100, 8110)", got)
	}
}

func TestWebDeputyEventsTabNotMembersTab(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8200") // owner A
	seedLeader(t, bot, "8201") // delegate B
	m := seedMember(t, bot, "8202", "8200", 30, time.Time{})
	m.DeputyOf = "8201" // A delegates member M to B (events only)
	if err := model.CreateUser(bot.ydb.GetDB(), m); err != nil {
		t.Fatal(err)
	}

	cookieB, csrfB := loginAs(t, bot, h, 8201)
	// Đệ tử tab: B must NOT see M.
	if rec := doGet(t, h, "/members?all=1", cookieB); strings.Contains(rec.Body.String(), "8202") {
		t.Fatal("delegate's Đệ tử tab leaked the delegated member")
	}
	// Sự kiện tab: B MUST see M, badged, with no delete affordance.
	rec := doGet(t, h, "/events?all=1", cookieB)
	body := rec.Body.String()
	if !strings.Contains(body, "8202") {
		t.Fatal("delegate's Sự kiện tab missing the delegated account")
	}
	if !strings.Contains(body, "uỷ thác") {
		t.Fatal("delegated row missing the 🤝 badge")
	}
	if strings.Contains(body, "Xoá acc sự kiện 8202") {
		t.Fatal("delegated row must NOT offer the Xoá button")
	}
	// Owner A still manages M normally.
	cookieA, _ := loginAs(t, bot, h, 8200)
	if rec := doGet(t, h, "/members?all=1", cookieA); !strings.Contains(rec.Body.String(), "8202") {
		t.Fatal("owner lost its own member")
	}
	// B may not delete a delegated account.
	if _, isErr := flashOf(t, doPost(t, h, "/events", cookieB, csrfB,
		url.Values{"action": {"remove"}, "member_id": {"8202"}})); !isErr {
		t.Fatal("delegate removed a delegated account")
	}

	// An order as B reaches M: webRunEventOrder enumerates delegated accounts.
	ep := &EventPlayer{Username: "m8202", Running: true}
	ep.writeChan = make(chan []byte, 8)
	wireCtxEP(t, ep)
	openKunlun(t, ep, "8202")
	bot.session.Store(int64(8202), ep)
	if msg, isErr := flashOf(t, doPost(t, h, "/events/order", cookieB, csrfB,
		url.Values{"which": {"kunlun"}, "act": {"tangtoc"}})); isErr || !strings.Contains(msg, "tăng tốc") {
		t.Fatalf("kunlun tangtoc as delegate failed: isErr=%v msg=%q", isErr, msg)
	}
	frame := <-ep.writeChan
	if got := frameFuncType(t, frame); got != KUNLUN_ACCELERATE_CMD {
		t.Fatalf("order reached a non-kunlun frame: %s", got)
	}
}

func TestWebDeputyLoginGates(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8300") // owner
	seedLeader(t, bot, "8301") // delegate
	m := seedMember(t, bot, "8302", "8300", 30, time.Time{})
	m.DeputyOf = "8301"
	model.CreateUser(bot.ydb.GetDB(), m)
	cookieB, csrfB := loginAs(t, bot, h, 8301)

	// A member NOT delegated to B → B cannot log it in.
	seedMember(t, bot, "8303", "8300", 30, time.Time{})
	if _, isErr := flashOf(t, doPost(t, h, "/events", cookieB, csrfB,
		url.Values{"action": {"login"}, "member_id": {"8303"}})); !isErr {
		t.Fatal("delegate logged in a member it was NOT delegated")
	}

	// GATES BEFORE SNATCH (owner protection): an expired-VIP delegated account
	// hanging in normal mode must be refused WITHOUT kicking the owner's session
	// (a snatch-then-fail would cost them a live hang for nothing). VIP gate is
	// ahead of the snatch, so the *Player survives untouched. Deterministic —
	// returns false at the VIP gate, so no login goroutine starts.
	m.VipExpireAt = time.Now().Add(-time.Hour)
	model.CreateUser(bot.ydb.GetDB(), m)
	bot.session.Store(int64(8302), &Player{Running: true})
	if _, isErr := flashOf(t, doPost(t, h, "/events", cookieB, csrfB,
		url.Values{"action": {"login"}, "member_id": {"8302"}})); !isErr {
		t.Fatal("expired-VIP delegate logged in anyway")
	}
	if v, ok := bot.session.Load(int64(8302)); !ok {
		t.Fatal("a gated-out login still snatched the owner's hang")
	} else if _, isPlayer := v.(*Player); !isPlayer {
		t.Fatal("owner's normal session was replaced despite a refused login")
	}
	bot.session.Delete(int64(8302))

	// SNATCH ALLOWED (user 2026-10-07 "cho phép lead giật acc đang treo"): with
	// valid creds the delegate is now ACCEPTED over a running normal hang — the
	// old "treo chế độ thường" protection is gone. We assert only the synchronous
	// accept (the returned flash); the background event-login attempt it kicks
	// off is the same real-network path startPlayerLogin already takes in prod.
	m.VipExpireAt = time.Now().Add(30 * 24 * time.Hour)
	model.CreateUser(bot.ydb.GetDB(), m)
	bot.session.Store(int64(8302), &Player{Running: true})
	if msg, isErr := flashOf(t, doPost(t, h, "/events", cookieB, csrfB,
		url.Values{"action": {"login"}, "member_id": {"8302"}})); isErr || !strings.Contains(msg, "uỷ thác") {
		t.Fatalf("delegate could not snatch a hanging acc: isErr=%v msg=%q", isErr, msg)
	}
	if v, ok := bot.session.Load(int64(8302)); ok {
		if _, isPlayer := v.(*Player); isPlayer {
			t.Fatal("owner's hang survived a successful snatch (not taken over)")
		}
	}
	bot.session.Delete(int64(8302))
}

func TestGetDeputyInFleet(t *testing.T) {
	bot := newTestWebBot(t)
	seedLeader(t, bot, "8400")
	m := seedMember(t, bot, "8401", "8400", 30, time.Time{})
	m.DeputyOf = "8400" // deputated to its OWN leader — allowed (owner
	// 2026-10-10): pulling your own member to events is the normal case;
	// a live hang is SNATCHED by the deputy-login path, never clobbered.
	model.CreateUser(bot.ydb.GetDB(), m)
	if _, ok := bot.getDeputyInFleet("8401", 8400); !ok {
		t.Fatal("own-leader delegation rejected — events tab rows would fail login")
	}
	m.DeputyOf = "8402" // genuine cross-leader delegation
	model.CreateUser(bot.ydb.GetDB(), m)
	if _, ok := bot.getDeputyInFleet("8401", 8402); !ok {
		t.Fatal("cross-leader delegation not recognised")
	}
	if _, ok := bot.getDeputyInFleet("8401", 8403); ok {
		t.Fatal("recognised a leader it did not delegate to")
	}
}

// The owning leader delegates a member to another leader through the Đệ tử
// config panel (deputy_of), and the receiving leader is notified.
func TestWebMemberConfigSetsDeputy(t *testing.T) {
	bot := newTestWebBot(t)
	h := bot.webRoutes()
	seedLeader(t, bot, "8500") // owner
	seedLeader(t, bot, "8501") // delegate
	seedMember(t, bot, "8502", "8500", 30, time.Time{})
	cookieA, csrfA := loginAs(t, bot, h, 8500)

	form := url.Values{
		"action": {"config"}, "member_id": {"8502"},
		"username": {"m8502"}, "server_id": {"100"},
		"min_dao": {"4"}, "min_binh": {"2"}, "min_luuly": {"6"}, "min_tuling": {"0"},
		"deputy_of": {"8501"},
	}
	if msg, isErr := flashOf(t, doPost(t, h, "/members", cookieA, csrfA, form)); isErr {
		t.Fatalf("config deputy_of refused: %q", msg)
	}
	got, err := model.GetUser(bot.ydb.GetDB(), "8502")
	if err != nil || got.DeputyOf != "8501" {
		t.Fatalf("deputy not persisted: %+v err=%v", got, err)
	}
	// The receiving leader (8501) got a notify.
	sawNotify := false
drain:
	for {
		select {
		case n := <-bot.notiChan:
			if n.ChatID == 8501 {
				sawNotify = true
			}
		default:
			break drain
		}
	}
	if !sawNotify {
		t.Fatal("receiving leader was not notified of the new delegation")
	}

	// Bad target (self) aborts the whole save: deputy stays 8501.
	form.Set("deputy_of", "8502") // member id, not a leader
	if _, isErr := flashOf(t, doPost(t, h, "/members", cookieA, csrfA, form)); !isErr {
		t.Fatal("non-leader delegation target accepted")
	}
	got, _ = model.GetUser(bot.ydb.GetDB(), "8502")
	if got.DeputyOf != "8501" {
		t.Fatalf("rejected save clobbered deputy: %q", got.DeputyOf)
	}
}

// The Sự kiện tab must show EXACTLY the accounts the login guard can start:
// an own member that also set deputy_of to its own leader used to render a
// row whose Login answered "acc này không thuộc quyền" (owner bug
// 2026-10-10). eventFleetOf now applies the same leader_id rule as
// getDeputyInFleet.
func fleetOf(t *testing.T, bot *TeleBot, leaderID string) []eventFleetRow {
	t.Helper()
	rows, err := bot.eventFleetOf(leaderID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestEventFleetOfMatchesDeputyGuard(t *testing.T) {
	// The tab must show EXACTLY what the login guard can log in (owner bug
	// 2026-10-10: row rendered but Login answered "acc này không thuộc
	// quyền"). Both own-leader and cross-leader delegations are legitimate.
	bot := newTestWebBot(t)
	seedLeader(t, bot, "8600")
	seedLeader(t, bot, "8601")
	m := seedMember(t, bot, "8602", "8600", 30, time.Time{})
	m.DeputyOf = "8600" // own-leader deputy — the exact failing case
	model.CreateUser(bot.ydb.GetDB(), m)
	var row *eventFleetRow
	fleet := fleetOf(t, bot, "8600")
	for i := range fleet {
		if fleet[i].Ev.ID == "8602" {
			row = &fleet[i]
		}
	}
	if row == nil || !row.Deputy {
		t.Fatal("own-leader delegation missing from the events tab")
	}
	if _, ok := bot.getDeputyInFleet("8602", 8600); !ok {
		t.Fatal("row shown but the login guard rejects it — display/guard diverged")
	}
	m.DeputyOf = "8601" // cross-leader still fine
	model.CreateUser(bot.ydb.GetDB(), m)
	found := false
	for _, fr := range fleetOf(t, bot, "8601") {
		if fr.Ev.ID == "8602" && fr.Deputy {
			found = true
		}
	}
	if !found {
		t.Fatal("cross-leader delegation missing from the events tab")
	}
	if _, ok := bot.getDeputyInFleet("8602", 8601); !ok {
		t.Fatal("cross-leader guard rejected a visible row")
	}
}
