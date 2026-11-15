package tytd_go

// GOD ISLAND — "def nhà" (owner 2026-10-19): "tất cả sẽ tự cắn quả tự hồi
// máu lần lượt, chỉ hồi vừa đủ máu để hồi sinh, nếu đang có người đang sống
// trong base thì đợi. Cùng 1 thời điểm chỉ được 1 người đang sống giúp câu
// giờ của team bạn khi đánh nhà mình." Fully automatic — no button, no
// toggle (owner: "nếu nhà chính đang bị đánh thì tự def không cần người
// bật"). Mechanics pinned from code + protocol (NOT guesses): a corpse
// whose revive countdown passed stays dead until healed, HP is raised ONLY
// by drinking the spiritual ball (206827 usePower, 1:1, per-draw ceiling
// BallMax), fruits (206837) only refill the ball, and the 1ace FightSeen
// ledger says whether the home city is under attack. The engine therefore:
// waits while any of OUR units stands alive at home; the moment the base
// has no live defender and the home is fighting, it wakes ONE corpse per
// window (oldest-waiting first, never the one it just woke) — refill the
// ball one fruit at a time when dry (item use owner-authorized for THIS
// engine only, see [[tytd-item-ban]]), then sip the ball up to the
// OWNER-FIXED revive bar of 30% maxPower ("chỗ linh lực thì mày cố định
// 30% đi", 2026-10-19). LIVE PROBE corrected the model the same day:
// dead ⇔ location==0 (curPower alone lies — dead at 2000/11206 HP), and
// the server stands the unit at the bar and drops it AT BASE by itself
// (owner: "sẽ tự về base ngay, bơm đủ máu là tự sống") — the engine NEVER
// sends a march-home.
//
// Coordination is a package-level per-(chat,variant) registry — EventPlayer
// has no TeleBot back-pointer, so accounts self-report through it: holders
// (alive-at-home presence), wanted revivals (dead-nealing-heal at home),
// and the single-actor turn stamp. All timestamps are local wall time.

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"sync"
	"time"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const (
	// giDefActGap: one engine action (sip / fruit) per account per gap —
	// and the same gap is the chat-wide exclusivity window, so at most one
	// account acts at a time ("lần lượt").
	giDefActGap = 8 * time.Second
	// giDefFruitGap throttles fruit bites: one 206837{useCount:1} per gap
	// while the ball reads dry (1ab5 refreshes the cache between bites).
	giDefFruitGap = 30 * time.Second
	// giDefHolderFresh: a "someone alive at home" stamp this old is stale —
	// the resident tick re-stamps every giInfoPollSeconds (3s).
	giDefHolderFresh = 10 * time.Second
	// giDefFightFresh: FightSeen stores server-ms; the home counts as
	// attacked within this window (1ace flaps ~19x/4min, so 90s rides out
	// a quiet patch mid-siege without arming def hours later).
	giDefFightFresh = 90 * time.Second
	// giDefSipFloor: the smallest revive target. LIVE-MEASURED 2026-10-19
	// (<live-user>@710564, gi_defnha_probe_live_test.go): a corpse stays
	// down (location=0) at 17.8% maxPower and stands + auto-returns to
	// base at maxPower-1; NO proto field publishes the bar (the ball
	// panel's deadWeakEffect.value=200 is NOT it — dead at 2000 HP).
	// OWNER RULING 2026-10-19: "chỗ linh lực thì mày cố định 30% đi" —
	// the engine tops the corpse up to a FIXED 30% of maxPower; the server
	// stands it there and drops it at base (no march-home needed).
	giDefSipFloor = 50
	// giDefRevivePct: the owner-fixed revive target, percent of maxPower.
	giDefRevivePct = 30
	// giDefWaitMax: a wanted-revival registration older than this is pruned
	// (the account stopped reporting itself as needing heal).
	giDefWaitMax = 5 * time.Minute
)

// giDefKey identifies one def arena: a leader chat on one island variant.
type giDefKey struct {
	chatID int64
	which  int
}

// giDefChat is the shared coordination state of one arena. Every field is
// guarded by its own mutex; the registry mutex only guards the map.
type giDefChat struct {
	mu          sync.Mutex
	holders     map[string]time.Time // account key → last seen alive AT home
	wants       map[string]time.Time // account key → first seen dead-need-heal
	lastHeal    map[string]time.Time // account key → last time it was picked
	lastRevived string               // account key picked most recently
	lastAct     time.Time            // chat-wide exclusivity stamp
}

var giDefReg = struct {
	mu     sync.Mutex
	byChat map[giDefKey]*giDefChat
}{byChat: map[giDefKey]*giDefChat{}}

// giDefAccountKey names an account inside the registry (usernames are game-
// global but ServerID makes the key honestly unique per session).
func giDefAccountKey(p *EventPlayer) string {
	return p.Username + "@" + strconv.Itoa(p.ServerID)
}

// giDefChatFor fetches (or creates) the arena state and prunes stale rows.
func giDefChatFor(chatID int64, which int) *giDefChat {
	k := giDefKey{chatID, which}
	giDefReg.mu.Lock()
	c := giDefReg.byChat[k]
	if c == nil {
		c = &giDefChat{holders: map[string]time.Time{}, wants: map[string]time.Time{}, lastHeal: map[string]time.Time{}}
		giDefReg.byChat[k] = c
	}
	giDefReg.mu.Unlock()
	now := time.Now()
	c.mu.Lock()
	for key, t := range c.holders {
		if now.Sub(t) > giDefWaitMax {
			delete(c.holders, key)
		}
	}
	for key, t := range c.wants {
		if now.Sub(t) > giDefWaitMax {
			delete(c.wants, key)
		}
	}
	for key, t := range c.lastHeal {
		if now.Sub(t) > giDefWaitMax {
			delete(c.lastHeal, key)
		}
	}
	c.mu.Unlock()
	return c
}

// stampHolder reports "I am alive and standing in the home city" — while
// any fresh stamp exists, every corpse waits.
func (c *giDefChat) stampHolder(key string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.holders[key] = now
}

// stampWant reports "I am dead at home, cooldown passed, and need healing".
// waitSince keeps the account's FIRST sighting so rotation is oldest-first
// even across restarts of the tick loop.
func (c *giDefChat) stampWant(key string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.wants[key]; !ok {
		c.wants[key] = now
	}
}

// clearWant drops the account from the revival queue (stood up).
func (c *giDefChat) clearWant(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.wants, key)
}

// holdersAlive reports whether any OUR unit stands alive at home (fresh
// stamp) — if so the whole fleet waits per the owner's rule.
func (c *giDefChat) holdersAlive(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.holders {
		if now.Sub(t) <= giDefHolderFresh {
			return true
		}
	}
	return false
}

// pickTurn is the single-actor gate: among the registered wanters it
// returns whether `key` is the one allowed to act NOW — oldest-waiting
// first, skipping the account just woke if anyone else waits, and only
// once per giDefActGap window chat-wide. Losers wait for their turn on a
// later tick; nobody acts twice (the stamp is taken atomically with the
// answer).
func (c *giDefChat) pickTurn(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.lastAct) < giDefActGap {
		return false // someone acted this very moment
	}
	var names []string
	for k := range c.wants {
		names = append(names, k)
	}
	if len(names) == 0 {
		return false
	}
	// Oldest want first; ties broken by name for determinism.
	sort.Slice(names, func(i, j int) bool {
		ti, tj := c.wants[names[i]], c.wants[names[j]]
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return names[i] < names[j]
	})
	choice := names[0]
	if choice == c.lastRevived && len(names) > 1 {
		choice = names[1] // never wake the same account twice in a row
	}
	if choice != key {
		return false
	}
	c.lastAct = now
	c.lastHeal[key] = now
	c.lastRevived = key
	return true
}

// wasHealedRecently reports whether the engine picked this account within
// the last few minutes — the stand-up notify uses it so "đứng dậy câu giờ"
// only fires after a def revive, not after every unrelated resurrection.
func (c *giDefChat) wasHealedRecently(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.lastHeal[key]
	return ok && now.Sub(t) <= giDefWaitMax
}

// giDefHome is the def-nha arbiter, called by godIslandStay for each
// announced variant on every 3s tick (self-throttled). It reads the
// account's own slot under GodIslandMu, then coordinates through the
// registry — actions (giSend paths) run with the lock released, matching
// the file-wide convention.
func (p *EventPlayer) giDefHome(which int) {
	now := time.Now()
	p.GodIslandMu.Lock()
	ev := p.GiEvents[which]
	if ev == nil || ev.ActivityID == 0 || ev.LeftOut {
		p.GodIslandMu.Unlock()
		return
	}
	if ev.Home == 0 {
		// Home still unknown (first INFO_LOADs) — never defend an assumed
		// city id (6001 differs between the islands).
		p.GodIslandMu.Unlock()
		return
	}
	home := ev.Home
	activityID := ev.ActivityID
	islandName := ev.Name

	_, cooldown := ev.reviveLeft()
	// LIVE-PROVEN 2026-10-19: a corpse reads location=0 and STAYS down no
	// matter how much it drinks until HP clears the revive bar, and the
	// moment it clears it the server stands it and drops it at base — so
	// "alive" is location!=0 (curPower alone lies: a 10-HP corpse is dead)
	// and no march-home is ever needed after a def revive.
	reviveTarget := ev.PowerMax * giDefRevivePct / 100
	if ev.PowerMax > 0 && reviveTarget < giDefSipFloor {
		reviveTarget = giDefSipFloor
	}
	alive := !cooldown && !ev.Moving && ev.PowerKnown && ev.PowerCur > 0 && ev.Location != 0
	// Edge: the engine woke this account and it now stands → tell the
	// leader once (sendNotify is buffered/non-blocking by contract).
	if alive && !ev.DefAlive {
		key := giDefAccountKey(p)
		p.GodIslandMu.Unlock()
		if giDefChatFor(p.ChatID, which).wasHealedRecently(key, now) {
			p.sendNotify(fmt.Sprintf("🛡️ [%s] đứng dậy câu giờ tại thành nhà %d", islandName, home))
		}
		p.GodIslandMu.Lock()
		ev = p.GiEvents[which]
	}
	ev.DefAlive = alive
	if alive {
		ev.DefWaitSince = time.Time{}
	}

	// Is the home under attack? FightSeen stores server-ms (1ace fold).
	underAttack := false
	if seen, ok := ev.FightSeen[home]; ok {
		underAttack = time.Now().UnixMilli()-seen <= giDefFightFresh.Milliseconds()
	}

	// Needing a heal: cooldown fully passed, standing still, and the death
	// happened at the home base (1acd's city; before the first 1acd, a
	// location==0 corpse with no DeadCity stamp counts too).
	// A corpse is location==0 with HP UNDER the revive bar — drinking a
	// little (cur>0) does NOT stand it up (live: dead at 2000/11206), so
	// the queue keeps it until it is topped up to the fixed 30%.
	diedHere := ev.DeadCity == home || (ev.DeadCity == 0 && ev.Location == 0)
	needHeal := !cooldown && !ev.Moving && ev.PowerKnown && ev.Location == 0 &&
		ev.PowerCur < reviveTarget && diedHere
	if needHeal && ev.DefWaitSince.IsZero() {
		ev.DefWaitSince = now
	}

	// Snapshot for the actions (reads finish here).
	throttled := !ev.DefLastAct.IsZero() && now.Sub(ev.DefLastAct) < giDefActGap
	fruitThrottled := !ev.DefLastFruit.IsZero() && now.Sub(ev.DefLastFruit) < giDefFruitGap
	ball := giDefBall{known: ev.BallKnown, cur: ev.BallCur, perDraw: ev.BallMax}
	powerCur := ev.PowerCur
	location := ev.Location
	p.GodIslandMu.Unlock()

	if !underAttack {
		return // peace at home: nothing to defend, nothing to say
	}
	key := giDefAccountKey(p)
	dc := giDefChatFor(p.ChatID, which)

	if alive {
		if location == home {
			dc.stampHolder(key, now) // the one live defender — everyone else waits
			dc.clearWant(key)
		}
		// Alive but away (attacking elsewhere): NOT our business to recall —
		// a def-revived unit already auto-landed at base (live-proven), so
		// anyone far from home got there by their own orders.
		return
	}
	if !needHeal {
		dc.clearWant(key) // alive-but-not-defending (dead countdown, marching…)
		return
	}
	// Dead and needing heal: the queue registration carries our turn order.
	dc.stampWant(key, func() time.Time {
		p.GodIslandMu.Lock()
		defer p.GodIslandMu.Unlock()
		if ev.DefWaitSince.IsZero() {
			ev.DefWaitSince = now
		}
		return ev.DefWaitSince
	}())
	if dc.holdersAlive(now) {
		return // "nếu đang có người đang sống trong base thì đợi"
	}
	if throttled || !dc.pickTurn(key, now) {
		return
	}

	if !ball.known || ball.cur <= 0 {
		// Ball dry → bite ONE fruit (206837 refills it; owner authorized
		// auto-fruit for THIS engine only). Throttled so a bag refusal
		// cannot spam; 1ab5 refreshes BallCur between bites.
		if fruitThrottled {
			return
		}
		p.giSend(ev, GOD_ISLAND_USE_FRUIT_CMD, SERVER_GOD_ISLAND_USE_FRUIT,
			&proto.GodIslandUseFruitReq{ActivityId: activityID, UseCount: 1})
		p.GodIslandMu.Lock()
		ev.DefLastFruit = now
		ev.DefLastAct = now
		p.GodIslandMu.Unlock()
		log.Printf("[%s] [GodIsland/defnha] cầu cạn, cắn 1 quả đổ cầu (nhà %d đang bị đánh)", p.Username, home)
		return
	}

	// Sip: top the corpse up to the owner-fixed 30% ("cố định 30%"),
	// clamped by what the ball holds and the per-draw ceiling (BallMax).
	// Nothing past the bar is ever drunk — the server stands the unit at
	// the bar and drops it at base; later ticks then read it alive.
	usePower := reviveTarget - powerCur
	if ball.cur < usePower {
		usePower = ball.cur
	}
	if ball.perDraw > 0 && usePower > ball.perDraw {
		usePower = ball.perDraw
	}
	if usePower <= 0 {
		return
	}
	p.giSend(ev, GOD_ISLAND_USE_BALL_CMD, SERVER_GOD_ISLAND_USE_BALL,
		&proto.GodIslandGameUseSpiritualBallReqMsg{
			ActivityId: activityID,
			UsePower:   pb.Int64(usePower),
		})
	p.GodIslandMu.Lock()
	ev.DefLastAct = now
	p.GodIslandMu.Unlock()
	log.Printf("[%s] [GodIsland/defnha] uống cầu usePower=%d (nhà %d đang bị đánh)", p.Username, usePower, home)
}

// giDefBall is the ball snapshot taken under GodIslandMu.
type giDefBall struct {
	known   bool
	cur     int64
	perDraw int64
}
