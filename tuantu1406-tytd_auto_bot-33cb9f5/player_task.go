package tytd_go

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// Union (bang hội) task auto-claim per ../remoteAll/TASK_PROTOCOL.md: the
// server pushes the full task list at login (501) and per-task patches
// (502), which we keep as a map; the bang hội tasks (union_task_static.go)
// get claimed in one batch as soon as their value reaches the target and
// state says unclaimed — immediately when a 502 carries the completion
// (taskKick), with the poll as the safety net. The five chop-count tasks
// also drive autoPeach: while any of them is short, chopping continues even
// with the Peach toggle off, until the SERVER-reported value catches up
// (never counted locally). The ad task 120006 is completed by watchAdTask
// before it can be claimed. A task that ever carried a selectItem needs a
// manual reward choice (S_TASK_SELECT_REWARD) and is never auto-claimed.
// The claim response does not update the task list — the 502 push that
// follows does, so after claiming we just wait for the next push to see the
// new state.

const (
	taskPollInterval = 10 * time.Minute
	taskFirstDelay   = 25 * time.Second // after login traffic, the yard's 15 s and union bounty's 20 s slots
)

// taskEntry is one task's server state plus the sticky "reward needs a
// manual pick" flag (see taskHandleList).
type taskEntry struct {
	data      *proto.TaskDataTempMsg
	hadSelect bool
}

// unionTaskClaimable returns the ids whose server state says: known bang
// hội task, unclaimed, no unresolved reward choice, progress reached.
func (p *Player) unionTaskClaimable() []int32 {
	p.TaskMu.Lock()
	defer p.TaskMu.Unlock()
	return p.unionTaskClaimableLocked()
}

// unionTaskClaimableLocked is the same walk with TaskMu already held
// (taskHandleList uses it to spot the exact push that unlocks a claim).
func (p *Player) unionTaskClaimableLocked() []int32 {
	var ids []int32
	for id, e := range p.tasks {
		target, ok := unionTaskTargets[id]
		if !ok || e.hadSelect || e.data.GetState() == 1 {
			continue
		}
		value, err := strconv.ParseInt(e.data.GetValue(), 10, 64)
		if err != nil {
			continue // unreadable progress: the server stays the arbiter
		}
		if value >= int64(target) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// unionChopPending reports whether any of the five chop-count union tasks is
// listed, unclaimed, and still short of its target. Before the first 501
// snapshot nothing is known, so it answers false (task-driven chopping starts
// when the server says a task is actually short). It never counts the ad
// task: watching an ad is not chopping.
func (p *Player) unionChopPending() bool {
	p.TaskMu.Lock()
	defer p.TaskMu.Unlock()
	if !p.taskSynced {
		return false
	}
	for _, id := range unionChopTaskIDs {
		e := p.tasks[id]
		if e == nil || e.hadSelect || e.data.GetState() == 1 {
			continue
		}
		value, err := strconv.ParseInt(e.data.GetValue(), 10, 64)
		if err != nil {
			continue // unreadable progress: the server stays the arbiter
		}
		if value < int64(unionTaskTargets[id]) {
			return true
		}
	}
	return false
}

func (p *Player) autoUnionTask() {
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(taskFirstDelay):
	}
	for {
		p.watchAdTask()
		if ids := p.unionTaskClaimable(); len(ids) > 0 {
			reqMSG, _ := pb.Marshal(&proto.TaskGetRewardReqMsg{TaskId: ids})
			p.write(pack.Pack(TASK_GET_REWARD_CMD, reqMSG, p.UID))
		}
		select {
		case <-p.ctx.Done():
			return
		case <-p.taskKick: // a 502 just turned something claimable
			if p.ctx.Err() != nil {
				return
			}
		case <-time.After(taskPollInterval):
		}
	}
}

// watchAdTask completes the ad-watch union task (120006): one
// S_WATCH_AD_TASK with conditionId = the task id, activityId 0, and the
// plain (non-skip-item) flag. This only completes the condition — success
// arrives as the follow-up 502 pushing value >= 1, which makes the task
// claimable and kicks this loop; the batch claim is the usual
// TaskGetRewardReqMsg path. Never retried on a guess: while value stays
// below 1 the next poll may re-send, exactly like the official client
// retrying after a failed ad callback.
func (p *Player) watchAdTask() {
	p.TaskMu.Lock()
	need := false
	// One request per poll window: task completions kick this loop often,
	// and the official client sends one request per watched ad, never a
	// burst.
	if p.taskSynced && time.Since(p.lastWatchAd) >= taskPollInterval {
		if e := p.tasks[unionAdTaskID]; e != nil && e.data.GetState() != 1 {
			if v, err := strconv.ParseInt(e.data.GetValue(), 10, 64); err == nil && v < int64(unionTaskTargets[unionAdTaskID]) {
				need = true
				p.lastWatchAd = time.Now()
			}
		}
	}
	p.TaskMu.Unlock()
	if !need {
		return
	}
	reqMSG, _ := pb.Marshal(&proto.WatchAdReq{
		ActivityId:  pb.Int32(0),
		ConditionId: pb.Int32(unionAdTaskID),
		IsUseADTime: pb.Bool(false),
	})
	p.write(pack.Pack(S_WATCH_AD_TASK, reqMSG, p.UID))
}

// taskLogOnce logs key at most once per login (same job as unionBountyLogOnce).
func (p *Player) taskLogOnce(key string) bool {
	p.TaskMu.Lock()
	defer p.TaskMu.Unlock()
	if p.taskLogged == nil {
		p.taskLogged = map[string]bool{}
	}
	if p.taskLogged[key] {
		return false
	}
	p.taskLogged[key] = true
	return true
}

// taskHandleList applies a 501 snapshot (replace whole map) or a 502 patch
// (replace the entry per task — taskId/value/state are required in the
// schema, so every patch carries them). selectItem is sticky across
// patches: once a task needs an explicit choice it stays out of auto-claim
// even if a later patch omits the field; the next full snapshot re-decides.
// Never clear on 502 — the official client merges, and so do we.
func (p *Player) taskHandleList(data []byte, full bool) {
	var msg proto.TaskDataListMsg
	if err := pb.Unmarshal(data, &msg); err != nil {
		log.Printf("[%s] [Task] bad task push: %v", p.Username, err)
		return
	}
	p.TaskMu.Lock()
	defer p.TaskMu.Unlock()
	if full {
		p.tasks = make(map[int32]*taskEntry, len(msg.GetDataList()))
		p.taskSynced = true
		// Fresh snapshot (login / daily reset) → forget the elixir claim
		// ledger so every done row gets one claim attempt again.
		p.elixirClaimTried = nil
	} else if p.tasks == nil {
		p.tasks = map[int32]*taskEntry{}
	}
	for _, t := range msg.GetDataList() {
		if t.GetTaskId() == 0 {
			continue
		}
		e := &taskEntry{data: t, hadSelect: len(t.GetSelectItem()) > 0}
		if !full {
			if old := p.tasks[t.GetTaskId()]; old != nil {
				e.hadSelect = old.hadSelect || e.hadSelect
			}
		}
		p.tasks[t.GetTaskId()] = e
	}
	// The push that carries a completion must trigger the claim right away;
	// waiting up to 10 min for the poll is how rewards used to sit claimed-
	// pending overnight. 1-deep buffered: concurrent pushes coalesce, the
	// claim itself re-reads the map.
	if p.taskKick != nil && len(p.unionTaskClaimableLocked()) > 0 {
		select {
		case p.taskKick <- struct{}{}:
		default:
		}
	}
}

func (p *Player) taskHandleReward(data []byte) {
	var resp proto.TaskGetRewardRespMsg
	if err := pb.Unmarshal(data, &resp); err != nil {
		log.Printf("[%s] [Task] bad claim resp: %v", p.Username, err)
		return
	}
	if resp.GetRet() != 0 {
		if p.taskLogOnce(fmt.Sprintf("claim:%d", resp.GetRet())) {
			log.Printf("[%s] [Task] nhận thưởng nhiệm vụ bị từ chối: ret=%d", p.Username, resp.GetRet())
		}
	}
	// Success is silent; the reward string lands server-side and the
	// follow-up 502 marks the tasks claimed.
}
