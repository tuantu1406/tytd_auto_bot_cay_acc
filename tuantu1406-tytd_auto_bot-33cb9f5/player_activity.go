package tytd_go

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Shared activity-list bookkeeping for BOTH Player and EventPlayer.
//
// The protocol has no pull for the main list: the server pushes 21001
// (PushActivityList, full, at login) and 21036 (SyncActivityList,
// add-or-update) on its own. What IS pullable per activity is 21003
// ReqGetActivityDetail {activityId, version} — the client's own
// "fetch activity detail config" request — whose RspGetActivityDetail
// re-carries the entry's mainConfig plus detailConfig and playerData.
// So: the store is fed by the login push, refreshed incrementally by
// pushes, and "polled" hourly by requesting 21003 for every stored id
// (version "0" forces a full answer; an empty request on 21001/21002 is
// ignored by the server — verified against the live server).

// Vars (not consts): tests shrink them to drive the poll loop quickly.
var (
	activityPollInterval = time.Hour
	activityPollStep     = 80 * time.Millisecond // pacing between detail pulls
	activityPollMax      = 128                   // sanity cap on ids pulled
	// activityWatchFast is the EVENT-LIST POLL cadence (owner directive
	// 2026-10-27 "thêm poll event list"): the main list itself cannot be
	// pulled (server-verified), so the poll is a 21003 sweep over the types
	// our engines actually play — window fields refresh AND missed-21036
	// rows come back (mergeDetail creates them from the server-authored
	// mainConfig). Every activityPollInterval the same loop additionally
	// does the FULL sweep of every stored id as before.
	activityWatchFast = 5 * time.Minute
)

// activityWatchTypes: every activity type an engine in this bot consumes.
// A row of any of these that goes stale or vanishes from the store is a
// silent "event đang chạy mà bot không thấy" — the fast poll covers them
// within minutes instead of an hour.
var activityWatchTypes = []int32{
	68,  // God Island (Bồng Lai)
	119, // Cast Sword (luyện khí 2048)
	130, // ComposeBall (đại hội luyện khí / thần long)
	150, // Sky Trade (Thương Đồ Tiên Vực)
	172, // RebornTrial (Luân Hồi Bí Cảnh)
	186, // WeYound (Tiên Cảnh Vị Ương)
	199, // KunlunWar (Côn Lôn)
	332, // Bac Minh
	333, // Peak Island (Thiên Ma)
	341, // Big Chess
	346, // Holy Land (Cửu U)
}

// activityEntry is the stored slice of ActivityMainConfig we care about —
// a plain value so snapshots never alias protobuf messages.
type activityEntry struct {
	ActivityID    int32
	Type          int32
	ChildType     int32
	BeginShowTime int64
	EndShowTime   int64
	BeginTime     int64
	EndTime       int64
	GroupType     int32
}

// activityStatus derives the client's phase from the window fields
// (beginShowTime ≤ now < beginTime → showing, beginTime ≤ now < endTime →
// active, endTime ≤ now < endShowTime → reward, else none). Times arrive
// in milliseconds (verified: login push values are ms-scaled).
func (e activityEntry) status(now time.Time) string {
	ms := now.UnixMilli()
	switch {
	case ms >= e.BeginTime && ms < e.EndTime:
		return "active"
	case ms >= e.BeginShowTime && ms < e.BeginTime:
		return "show"
	case ms >= e.EndTime && ms < e.EndShowTime:
		return "reward"
	default:
		return "none"
	}
}

func entryFromConfig(c *proto.ActivityMainConfig) activityEntry {
	return activityEntry{
		ActivityID:    c.GetActivityId(),
		Type:          c.GetType(),
		ChildType:     c.GetChildType(),
		BeginShowTime: c.GetBeginShowTime(),
		EndShowTime:   c.GetEndShowTime(),
		BeginTime:     c.GetBeginTime(),
		EndTime:       c.GetEndTime(),
		GroupType:     c.GetGroupType(),
	}
}

// activityStore holds the live activity list keyed by activityId. Zero
// value is ready to use; every method locks.
//
// lastOf remembers the newest activityId ever seen per type — including
// rows a later full push dropped. It powers the backfill (missingKnownIDs
// + mergeDetail creating rows): the protocol has NO pull for the main list
// (empty 21001/21002 requests are server-verified no-ops), so a session
// that logged in before the server announced an event and then missed its
// 21036 push could otherwise NEVER learn the event exists ("event đang
// chạy mà bot không detect" — owner question 2026-10-27). A 21003 answer's
// mainConfig is server-authored, so creating a row from it is as
// authoritative as a push.
type activityStore struct {
	mu      sync.Mutex
	entries map[int32]activityEntry
	lastOf  map[int32]int32
}

// applyList ingests a PushActivityList/SyncActivityList payload. A full
// list REPLACES the map (a withdrawn activity disappears); the incremental
// push only upserts, matching its ADD_OR_UPDATE name.
func (a *activityStore) applyList(who string, data []byte, full bool) {
	var push proto.PushActivityList
	if err := pb.Unmarshal(data, &push); err != nil {
		log.Printf("[%s] [Activity] bad list push: %v", who, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if full {
		a.entries = make(map[int32]activityEntry, len(push.MainConfig))
	} else if a.entries == nil {
		a.entries = make(map[int32]activityEntry)
	}
	for _, cfg := range push.MainConfig {
		if cfg.GetActivityId() == 0 {
			continue
		}
		a.entries[cfg.GetActivityId()] = entryFromConfig(cfg)
		if a.lastOf == nil {
			a.lastOf = map[int32]int32{}
		}
		if t := cfg.GetType(); t != 0 {
			a.lastOf[t] = cfg.GetActivityId()
		}
	}
}

// missingKnownIDs lists the remembered-but-currently-absent activity ids
// (type→newest id) — the backfill targets for the hourly poll.
func (a *activityStore) missingKnownIDs() []int32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []int32
	for _, id := range a.lastOf {
		if _, live := a.entries[id]; !live {
			out = append(out, id)
		}
	}
	return out
}

// lastIDForType reports the newest id ever seen for an activity type.
func (a *activityStore) lastIDForType(t int32) (int32, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.lastOf[t]
	return id, ok
}

// mergeDetail folds the mainConfig an RspGetActivityDetail answer carries
// back into the stored entry (field-by-field only when present, so a thin
// answer never blanks the window fields). An unknown activityId CREATES
// the row: the mainConfig inside the answer is server-authored, so this is
// the only legal backfill path for events whose push a long-lived session
// missed (the main list itself cannot be pulled — see activityStore). The
// answer only arrives for ids WE asked about (poll backfill / explicit
// pulls), so no foreign row can sneak in.
func (a *activityStore) mergeDetail(who string, data []byte) {
	var rsp proto.RspGetActivityDetail
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return
	}
	mc := rsp.GetActivity().GetDetailConfig().GetCommonConfig().GetMainConfig()
	if mc == nil || mc.GetActivityId() == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.entries[mc.GetActivityId()]
	ne := entryFromConfig(mc)
	if !ok {
		e = activityEntry{ActivityID: mc.GetActivityId()}
		log.Printf("[%s] [Activity] backfill: tạo dòng type=%d id=%d từ 21003 answer", who, mc.GetType(), mc.GetActivityId())
	}
	if a.lastOf == nil {
		a.lastOf = map[int32]int32{}
	}
	if t := ne.Type; t != 0 {
		a.lastOf[t] = mc.GetActivityId()
	}
	if ne.Type != 0 {
		e.Type = ne.Type
	}
	if ne.ChildType != 0 {
		e.ChildType = ne.ChildType
	}
	if ne.BeginShowTime != 0 {
		e.BeginShowTime = ne.BeginShowTime
	}
	if ne.EndShowTime != 0 {
		e.EndShowTime = ne.EndShowTime
	}
	if ne.BeginTime != 0 {
		e.BeginTime = ne.BeginTime
	}
	if ne.EndTime != 0 {
		e.EndTime = ne.EndTime
	}
	if ne.GroupType != 0 {
		e.GroupType = ne.GroupType
	}
	a.entries[mc.GetActivityId()] = e
}

// snapshot returns the entries sorted by activityId (stable for tests and
// status output).
func (a *activityStore) snapshot() []activityEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]activityEntry, 0, len(a.entries))
	for _, e := range a.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ActivityID < out[j].ActivityID })
	return out
}

// byType lists every stored instance of an ActivityMainConfig.type (an
// activity type can be open several times; e.g. type 18 recurs).
func (a *activityStore) byType(t int32) []activityEntry {
	var out []activityEntry
	for _, e := range a.snapshot() {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

func (a *activityStore) ids() []int32 {
	snap := a.snapshot()
	out := make([]int32, 0, len(snap))
	for _, e := range snap {
		out = append(out, e.ActivityID)
	}
	return out
}

// activityPollLoop is the hourly refresh shared by both account kinds: it
// sends the client's own "fetch activity detail config" request (21003)
// for every stored activity, paced and ctx-cancelled. The answers land on
// the normal reader: SERVER_ACTIVITY_DETAIL is merged into the store, and
// event accounts' existing 03eb routing keeps consuming the GI/BallGVG
// answers exactly as before.
func activityPollLoop(who string, ctx context.Context, store *activityStore, send func(id int32)) {
	fullDue := time.Now().Add(activityPollInterval) // first full sweep one interval in
	tick := time.NewTicker(activityWatchFast)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var ids []int32
		if time.Now().After(fullDue) {
			// Full sweep: every stored id (window refresh), as before.
			fullDue = time.Now().Add(activityPollInterval)
			ids = store.ids()
		} else {
			// Fast event-list poll: only the types engines play.
			seen := map[int32]bool{}
			for _, e := range store.snapshot() {
				for _, t := range activityWatchTypes {
					if e.Type == t && !seen[e.ActivityID] {
						seen[e.ActivityID] = true
						ids = append(ids, e.ActivityID)
					}
				}
			}
		}
		// Backfill pass (every tick): rows that existed at some point but
		// vanished (missed 21036 add, withdrawn-then-reopened instance). A
		// 21003 answer recreates them via mergeDetail; if the server stays
		// silent nothing changes and the next tick tries again.
		ids = append(ids, store.missingKnownIDs()...)
		if len(ids) == 0 {
			continue // login push not seen yet — nothing to refresh
		}
		if len(ids) > activityPollMax {
			ids = ids[:activityPollMax]
		}
		for i, id := range ids {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if i > 0 {
				time.Sleep(activityPollStep)
			}
			send(id)
		}
	}
}

// activityPollFrame builds the 21003 request (version "0": full answer —
// same rule the client follows with an empty local cache).
func activityPollFrame(id int32) ([]byte, error) {
	return pb.Marshal(&proto.ReqGetActivityDetail{ActivityId: id, Version: pb.String("0")})
}

// activityPull fires ONE 21003 backfill pull (answer recreates the row via
// mergeDetail) — used by engine loops sitting in a "no such event row"
// branch instead of waiting for the next hourly pass.
func (p *Player) activityPull(id int32) {
	payload, err := activityPollFrame(id)
	if err != nil {
		return
	}
	p.write(pack.Pack(ACTIVITY_GET_DATA_CMD, payload, p.UID))
}

// Player side.
func (p *Player) activityRefresh() {
	activityPollLoop(p.Username, p.ctx, &p.ActList, func(id int32) {
		payload, err := activityPollFrame(id)
		if err != nil {
			return
		}
		p.write(pack.Pack(ACTIVITY_GET_DATA_CMD, payload, p.UID))
	})
}

// EventPlayer side.
func (p *EventPlayer) activityRefresh() {
	activityPollLoop(p.Username, p.ctx, &p.ActList, func(id int32) {
		payload, err := activityPollFrame(id)
		if err != nil {
			return
		}
		p.write(pack.Pack(ACTIVITY_GET_DATA_CMD, payload, p.UID))
	})
}
