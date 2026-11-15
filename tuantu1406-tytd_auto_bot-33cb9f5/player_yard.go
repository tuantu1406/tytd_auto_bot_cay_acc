package tytd_go

import (
	"log"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Yard (vườn nhà) auto-production, per ../remoteAll/YARD_PRODUCTION_CHAIN.md:
// snapshot the yard, collect whatever finished (BUILD_GAIN_REWARD), and keep
// the Hồ Nguyện Vọng (Wish Pool, buildId 1004) producing. Idle pools are
// restarted with the crop the client would have preselected — the
// highest-tier crop unlocked at the building's current level at the level's
// max batch (yard_static.go, extracted from the shipped GameItemsDB and
// YardBuildingLevelDB configs) — falling back to whatever (productId,
// totalNum) the server last displayed on that instance if the level table
// has nothing for it.
//
// Farmland and Tree instances produce on their own and must never get
// BUILD_MAKE (chain doc §15); the Stove's batch size depends on static elixir
// costs we don't have, so only the Wish Pool is auto-made. Collection is
// state-safe for every producing instance: a BUILD_GAIN_REWARD with nothing
// ready is simply refused by the server (ret != 0, ignored).

const (
	yardWishPoolBuildID = int32(1004)      // WISHPOOL instance id from the wire capture
	yardEnterInterval   = 10 * time.Minute // snapshot cadence: collect+make decision cycle
	yardTryCollectAfter = time.Minute      // try collecting runs younger than this at most once

	// Draw side (quay thưởng vườn nhà). The server's YardDrawDataMsg counts
	// TODAY'S USED draws (ICT-day reset), capped by GameConfig
	// YARD_BUILD_DRAW_FREE_TIMES / YARD_BUILD_DRAW_AD_TIMES (1 and 2 in
	// GameConfig 4.9230). A plain draw request consumes the free quota first
	// and otherwise charges ticket item 100152 (YARD_BUILD_DRAW_COST), so we
	// only ever send while the server counter still shows free budget AND our
	// own day budget agrees — a stale snapshot can never burn a ticket.
	// An ad draw is the same single draw with the ad-slot flag set, matching
	// the body the client sends after a watched ad.
	yardFreeDrawLimit = 1
	yardAdDrawLimit   = 2
	yardDrawCooldown  = 15 * time.Second // spacing between sends
	yardDrawBackoff   = 10 * time.Minute // sit-out after the server refuses one
)

// yardRecipe is the last production the server displayed on one instance.
type yardRecipe struct {
	productID int32
	num       int32
}

// autoYard snapshots the yard every yardEnterInterval; all decisions happen
// in yardHandleEnter on the snapshot (the server is the only trusted state —
// pushes are applied server-side and the next ENTER reflects them).
func (p *Player) autoYard() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(15 * time.Second): // let the login flow settle first
	}
	for {
		reqMSG, _ := pb.Marshal(&proto.YardEnterReq{TargetPlayerId: pb.Int64(int64(p.UID))})
		p.write(pack.Pack(YARD_ENTER_CMD, reqMSG, p.UID))
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(yardEnterInterval):
		}
	}
}

// yardHandleEnter parses the YardEnterResp snapshot and issues collect/make
// orders for every building instance it can classify. Runs in its own
// goroutine (dispatched from wsHandler): the per-building writes would
// otherwise park the reader behind the socket.
func (p *Player) yardHandleEnter(data []byte) {
	var resp proto.YardEnterResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [Yard] bad enter resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		return // yard closed/not-ready; next cycle retries
	}
	// The snapshot rides today's draw counters — refresh and draw if free or
	// ad budget is left (yardDrawTick also chains the rest past its cooldown).
	p.yardDrawApply(resp.GetDrawData())
	p.yardDrawTick()
	now := time.Now().UnixMilli()
	for _, area := range resp.GetAreaInfo() {
		for _, b := range area.GetBuildingMsg() {
			cell, d := b.GetYardCellMsg(), b.GetYardBuildDetailMsg()
			if cell == nil || d == nil || cell.GetUniqueId() == 0 || cell.GetBuildId() == 0 {
				continue
			}
			uid, bid := cell.GetUniqueId(), cell.GetBuildId()

			// Chain doc §4: three indicators, not one, decide the state.
			switch {
			case d.GetStatus() == 1 || d.GetStartTime() > 0:
				// Making (or finished-but-uncollected, client "Make_end"):
				// learn the recipe, then try to collect ripe output.
				if d.GetProductId() != 0 && d.GetTotalNum() > 0 {
					p.yardLearnRecipe(uid, d.GetProductId(), int32(d.GetTotalNum()))
				}
				ready := (d.GetEndTime() > 0 && now >= d.GetEndTime()) ||
					(d.GetEndTime() == 0 && d.GetStartTime() > 0 &&
						now-d.GetStartTime() >= yardTryCollectAfter.Milliseconds())
				if !ready {
					continue
				}
				// Wish Pool leaves endTime=0 and we lack the static rate, so
				// "ready" is a guess; the server stays authoritative — ret!=0
				// (nothing ripe yet) is silently ignored by the reader.
				gainMSG, _ := pb.Marshal(&proto.YardBuildGainRewardReq{UniqueId: uid, BuildId: bid})
				p.write(pack.Pack(YARD_GAIN_REWARD_CMD, gainMSG, p.UID))

			case d.GetStatus() == 0 && d.GetStartTime() == 0 && d.GetLevelUpEndTime() == 0:
				// Truly idle. Only explicit-production instances get re-made,
				// and today that's the Wish Pool. Preferred choice: the
				// highest crop unlocked at this level (the client's own
				// default); only if the level tables can't answer do we fall
				// back to the recipe the server last showed here.
				if bid != yardWishPoolBuildID {
					continue
				}
				productID, num := int32(0), int32(0)
				if id, n, ok := yardHighestWishProduct(d.GetLevel()); ok {
					productID, num = id, n
				} else {
					recipe := p.yardRecipeFor(uid)
					if recipe == nil && d.GetProductId() != 0 && d.GetTotalNum() > 0 {
						// the server left the last recipe on the idle detail
						recipe = &yardRecipe{productID: d.GetProductId(), num: int32(d.GetTotalNum())}
						p.yardLearnRecipe(uid, recipe.productID, recipe.num)
					}
					if recipe == nil {
						p.yardNoRecipeHint(uid)
						continue
					}
					productID, num = recipe.productID, recipe.num
				}
				makeMSG, _ := pb.Marshal(&proto.YardBuildMakeReq{
					UniqueId:  pb.Int64(uid),
					BuildId:   pb.Int32(bid),
					ProductId: pb.Int32(productID),
					Num:       pb.Int32(num),
					IsCancel:  pb.Bool(false),
				})
				p.write(pack.Pack(YARD_BUILD_MAKE_CMD, makeMSG, p.UID))
			}
		}
	}
}

// yardDrawState paces the yard draws. data is the freshest server counters
// (ENTER snapshot / draw resp / rollover push); freeSent/adSent are OUR sends
// for the current ICT day — the second cap that keeps a stale data snapshot
// from spending a paid ticket after the human drew from the app. Guard with
// YardMu.
type yardDrawState struct {
	data     *proto.YardDrawDataMsg
	day      string
	freeSent int
	adSent   int
	lastSend time.Time
	nextTry  time.Time // zero unless backing off after a refusal
}

const (
	yardDrawNone = iota
	yardDrawFree
	yardDrawAd
)

// yardDrawWant decides which draw (if any) the cached state justifies right
// now. Pure except for the clock; locked for the caller's YardMu hold.
func yardDrawWant(st *yardDrawState, now time.Time) int {
	if st.data == nil || now.Before(st.nextTry) || now.Sub(st.lastSend) < yardDrawCooldown {
		return yardDrawNone
	}
	if today := now.In(ictZone).Format("2006-01-02"); st.day != today {
		st.day = today
		st.freeSent = 0
		st.adSent = 0
	}
	if st.data.GetFreeDrawTimes() < yardFreeDrawLimit && st.freeSent < yardFreeDrawLimit {
		return yardDrawFree
	}
	if st.data.GetAdCount() < yardAdDrawLimit && st.adSent < yardAdDrawLimit {
		return yardDrawAd
	}
	return yardDrawNone
}

// yardDrawTick sends at most one draw request when both caps allow it, then
// schedules one follow-up tick past the cooldown so the remaining draws of
// the day chain without waiting for the next ENTER.
func (p *Player) yardDrawTick() {
	select {
	case <-p.ctx.Done():
		return
	default:
	}
	p.YardMu.Lock()
	kind := yardDrawWant(&p.yardDraw, time.Now())
	switch kind {
	case yardDrawFree:
		p.yardDraw.freeSent++
	case yardDrawAd:
		p.yardDraw.adSent++
	}
	if kind != yardDrawNone {
		p.yardDraw.lastSend = time.Now()
	}
	p.YardMu.Unlock()
	if kind == yardDrawNone {
		return
	}
	// Single draw on the default pool; isTen is required on the wire even
	// when false (proto2 server schema).
	req := &proto.YardDrawReq{IsTen: pb.Bool(false), PoolId: pb.Int32(0)}
	if kind == yardDrawAd {
		req.IsADType = pb.Bool(true)
	}
	reqMSG, _ := pb.Marshal(req)
	p.write(pack.Pack(YARD_DRAW_CMD, reqMSG, p.UID))
	time.AfterFunc(yardDrawCooldown, func() { p.yardDrawTick() })
}

// yardDrawApply stores fresh server counters (nil keeps the old ones).
func (p *Player) yardDrawApply(d *proto.YardDrawDataMsg) {
	if d == nil {
		return
	}
	p.YardMu.Lock()
	p.yardDraw.data = d
	p.YardMu.Unlock()
}

// yardHandleDraw reads one YardDrawResp: success is logged with what landed,
// a refusal buys a sit-out so we never burst against the server.
func (p *Player) yardHandleDraw(data []byte) {
	var resp proto.YardDrawResp
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [Yard] bad draw resp: %v", p.Username, err)
		return
	}
	p.yardDrawApply(resp.GetDrawData())
	if resp.GetRet() != 0 {
		p.YardMu.Lock()
		p.yardDraw.nextTry = time.Now().Add(yardDrawBackoff)
		p.YardMu.Unlock()
		log.Printf("[%s] [Yard] quay thưởng bị từ chối: ret=%d", p.Username, resp.GetRet())
		return
	}
	var landed []string
	for _, r := range resp.GetDrawResult() {
		if r.GetRewards() != "" {
			landed = append(landed, r.GetRewards())
		}
		if r.GetItems() != "" {
			landed = append(landed, r.GetItems())
		}
	}
	log.Printf("[%s] [Yard] quay thưởng thành công: %v", p.Username, landed)
}

// yardHandleDataSync keeps the counters fresh across the ICT-day rollover.
func (p *Player) yardHandleDataSync(data []byte) {
	var sync proto.YardRefreshDataSync
	if err := pb.Unmarshal(data, &sync); err != nil {
		return
	}
	p.yardDrawApply(sync.GetDrawData())
	p.yardDrawTick()
}

// yardLearnRecipe records the recipe the server just displayed for uid.
func (p *Player) yardLearnRecipe(uid int64, productID, num int32) {
	p.YardMu.Lock()
	defer p.YardMu.Unlock()
	if p.YardRecipes == nil {
		p.YardRecipes = map[int64]*yardRecipe{}
	}
	p.YardRecipes[uid] = &yardRecipe{productID: productID, num: num}
}

// yardRecipeFor returns the remembered recipe for uid, nil if none.
func (p *Player) yardRecipeFor(uid int64) *yardRecipe {
	p.YardMu.Lock()
	defer p.YardMu.Unlock()
	return p.YardRecipes[uid]
}

// yardNoRecipeHint logs once per instance per login that we can't auto-make
// it yet — repeating the line every 10 minutes would only bury the other logs.
func (p *Player) yardNoRecipeHint(uid int64) {
	p.YardMu.Lock()
	hinted := p.yardHinted[uid]
	if p.yardHinted == nil {
		p.yardHinted = map[int64]bool{}
	}
	p.yardHinted[uid] = true
	p.YardMu.Unlock()
	if !hinted {
		log.Printf("[%s] [Yard] chưa biết công thức sản xuất của cơ sở %d — tự làm 1 lượt để bot học", p.Username, uid)
	}
}
