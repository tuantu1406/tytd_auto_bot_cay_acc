package tytd_go

// TÂY DU Phục Ma (XiYou, activity type 311) — auto spam ải chính tuyến, the
// bot's twin of the client's own backAuto idle loop (owner 2026-11-04:
// "chỉ cần thêm tính năng spam đánh ải chính tuyến (gần giống mạo hiểm),
// không cần xem giao diện lan man").
//
// ARCHITECTURE (owner law 2026-11-04): event-driven, NO round-trip seats.
// autoXiyou() is the only sender; xiyouDeliver() (routed from wsHandler) is
// the only reader. All state lives on Player fields.
//
// THE LOOP (verified live on <live-user>@710564, activity 9063637, 2026-11-04):
//
//	every ~5s:  ATTACK 231101 {activityId, times:5}   → ret=0
//	            RAND_EQUIPMENT 231102 {activityId, num:2} → ret=0 (drops+exp+realm)
//	every 4th tick (AUTO_SETTING_PROGRESS): UPDATE_PROGRESS 231113
//	            {activityId, progress: StageDB[progress].nextStage,
//	             codeVersion:"1.0.4"} → ret=0.
//
// WHAT THE SERVER ACCEPTS vs GATES (owner was right — "ải cuối mỗi cấp
// không lừa được server", verified 2026-11-04 across five live probes):
//
//   - Normal/Elite rows (type 0/1): blind assert crosses them; 31171 is a
//     ~4s UPDATE_PROGRESS rate-limit that pure waiting unlocks.
//   - In-chapter boss rows (type 2 whose next stays in the chapter, e.g.
//     10207): the server doesn't even make you stand there — one claim from
//     the row before returns respProgress already past the boss
//     (observed 10204→resp 10301, boss never fought).
//   - CHAPTER-END bosses (type 2 whose next starts a new chapter:
//     10507→20101, 20507→30101, …): REAL-FIGHT GATE (owner was right:
//     "ải cuối cần fight ở server rồi mới gửi request lên server"). The
//     server answers a blind assert 31170/31171 forever; fabricated levels
//     all fail too. What crosses (LIVE-PROVED 2026-11-05 on <live-user>@
//     710564: 10507→20101 ret=0 reward "105305=2|105280=40"): the client's
//     own combat sim replayed headless — node /workspace/tytd/xiyou_sim/
//     harness.js runs the game's browserify sim modules (from the
//     remoteXiYou bundle) + real CDN DBs, seeded by UPDATE_SEED 231137 and
//     fed DATA_INFO's info (with fixPlayerProperty, exactly like the
//     client's msg_reqRoleData). On a sim WIN the engine waits out the
//     fight's real duration and asserts with the sim-exact levels
//     (RandomSeed counts at frames 20/120/220…) → server accepts.
//     On a sim LOSS the engine keeps attacking — every retry pulls a fresh
//     UPDATE_SEED so the battle rolls differently, and it loops until the
//     owner calls /stop_taydu (owner law 2026-11-05: "đánh thua thì chạy
//     loop đánh mãi, đến lúc user gọi stop mới dừng"). The engine only
//     parks (bossWall + one forceNotify) when the harness/node itself is
//     unavailable on the bot host. Set TYTD_XIYOU_SIM to point at the
//     xiyou_sim dir on other hosts.
//
// times=5 / num=2 / advance-every-4 mirror the client's RemoteXiYouConfig
// AUTO_SETTING_COST / AUTO_SETTING_KILL_NUM / AUTO_SETTING_PROGRESS.
// Energy = item 105280 (XIYOU_STRENGTH_ITEM); stage-ups pay it back
// (probes saw reward "…|105280=40") — when it runs out ATTACK/RAND start
// answering ret!=0 and the loop simply goes quiet (log-only, no spam-notify).
// Boss-row ATTACKs at energy=0 answer ret=0 but count nothing (no kills,
// no task progress) — matches owner: boss fight itself costs no energy.
//
// activityId is found from p.ActList.byType(311) inside the battle window;
// wrong ids answer ret=210, right id ret=0 (live-verified discriminator —
// never trust a 210).
//
// Equipment drops are LEFT UNRESOLVED on purpose: the owner asked for the
// spam only, and nothing in the attack path needs the unDealEquipments list
// cleared. RESOLVE (231104) stays unused.

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"
	"tytd_go/internal/xiyoujs"

	"google.golang.org/protobuf/encoding/protojson"
	pb "google.golang.org/protobuf/proto"
)

// xiyouState is the sender↔reader handshake: everything autoXiyou reads and
// xiyouDeliver writes. atomic — no locks, no seats (owner law 2026-11-04).
type xiyouState struct {
	actID    atomic.Int32 // live-discovered activityId (ret=210 resets to 0)
	progress atomic.Int32 // current XiYouStageDB row id
	realm    atomic.Int32
	power    atomic.Value // string
	exp      atomic.Value // string
	// bossWall = chapter-boss row id parked on because the sim harness
	// can't run (no node / bad dir / no server data) — a LOSS never walls.
	// 0 = no wall. While set, the sender stops asserting; a progress change
	// clears it.
	bossWall atomic.Int32
	walled   atomic.Bool // forceNotify-once guard per wall
	// mainStopped = the offline feasibility gate parked the MAIN LINE
	// (owner law 2026-11-05 "thua thì notify rồi dừng luôn"). Only
	// /start_taydu re-arms it — and only the main line: Ảo Cảnh keeps its
	// own engine alive under the same p.Xiyou toggle (secret has its own
	// loss-park, it must not starve behind a main-line wall).
	mainStopped atomic.Bool

	// Offline feasibility memo (owner law 2026-11-05: "test offline mà thấy
	// thua thì notify rồi dừng luôn… có qua ải thì spam với server thật đến
	// chết"). Only the bossSim sender goroutine touches these — plain
	// fields. Only WINS are memoized: a loss stops the engine, and a restart
	// must re-scan + re-notify, so losses are never cached.
	feasRow int32
	feasPow string
	feasWin bool

	// one-shot request pumps for the boss sim (buffered 1, never block the
	// reader: drain-then-push). Sender creates on first use, reader pushes.
	chanOnce sync.Once
	infoCh   chan *proto.XiYouPlayerDataMsg
	seedCh   chan xiyouSeedMsg
	// seedMu serialises UPDATE_SEED round-trips between the main-line boss
	// farm and the secret runner (two sender goroutines share seedCh).
	seedMu sync.Mutex

	// ── Ảo Cảnh (Secret) — owner 2026-11-05 "thêm auto chạy ảo cảnh" ──
	secID       atomic.Int32 // currentSecretId = frontier (Kill resp folds it)
	sweepTimes  atomic.Int32 // dailySweepTimes (free sweeps = 3/day)
	secChanOnce sync.Once
	infoSecCh   chan *proto.XiYouPlayerDataMsg // secret's own DATA_INFO pump
	startCh     chan *proto.XiYouSecretStartResp
	selectCh    chan *proto.XiYouSecretSelectResp
	killCh      chan *proto.XiYouSecretKillResp
	sweepCh     chan *proto.XiYouSecretSweepResp
	// start-blocked park: chỉ acc KHÔNG Start-được (thiếu lực chiến, feasibility
	// offline thua sạch) mới park — chờ power/frontier đổi (fold clears).
	// Thua đấu KHÔNG park (chủ lệnh 2026-11-09 "thua thì phải đánh mãi").
	// Sender goroutine owns the memo fields.
	secFeasID  int32
	secFeasPow string
	secFeasWin bool
	secParkID  atomic.Int32
	secParked  atomic.Bool
	// secParkAt = unix giây park lần gần nhất. Park CHỈ dành cho ca bị CHẶN
	// START (thiếu lực chiến / feasibility offline thua sạch) — THUA ĐẤU không
	// park (chủ lệnh 2026-11-09 "thua thì phải đánh mãi"). Power-unpark (fold)
	// chỉ được gỡ park sau xiyouSecretParkHold — main-line spam khiến power đổi
	// TỪNG TICK từng làm park bay tức thì, Ảo Cảnh quay lại ải thiếu lực chiến
	// liên tục trong khi acc chưa đủ lực.
	secParkAt atomic.Int64
	// secWonMax = highest secretId ever Kill-won — Start must NEVER target
	// it or below (owner law 2026-11-05: fold-trễ không được đốt vé vào ải
	// cũ; chỉ chạy ải mới nhất của server).
	secWonMax atomic.Int32
	// secStopped = Start ret=101 (hết vé 105286) — dừng Ảo Cảnh hẳn, chỉ
	// /start_taydu mới chạy lại (owner law 2026-11-05).
	secStopped atomic.Bool
	// opRet = reader-only memo of the last ATTACK/RAND ret (throttle: log a
	// dry-energy ret once, not every 5s tick — spam fix 2026-11-05).
	opRet int32
	// secRunning = một ván Ảo Cảnh đang chạy (từ lúc lấy role tới hết Kill).
	// Main-line tick nằm im khi bật — ATTACK/RAND/UPDATE_PROGRESS làm exp/
	// realm nhảy, còn server replay trận Ảo Cảnh bằng role HIỆN TẠI của nó →
	// drift giữa ván = Kill ret=31170 (chủ lệnh 2026-11-05 "sim đúng nhưng
	// server không cho qua"). Giả lập đúng client thật: mở Ảo Cảnh là auto
	// chính tuyến dừng. Ván chỉ vài chục giây.
	secRunning atomic.Bool
}

// xiyouSeedMsg = one UPDATE_SEED answer. ret!=0 (30 = rate-limited, live-
// measured ~1s window 2026-11-05) must reach the sender too — it paces the
// seed-farm loop instead of waiting out a timeout.
type xiyouSeedMsg struct {
	ret  int32
	seed int32
}

func (s *xiyouState) chans() {
	s.chanOnce.Do(func() {
		s.infoCh = make(chan *proto.XiYouPlayerDataMsg, 1)
		s.seedCh = make(chan xiyouSeedMsg, 1)
	})
}

func (s *xiyouState) secChans() {
	s.secChanOnce.Do(func() {
		s.infoSecCh = make(chan *proto.XiYouPlayerDataMsg, 1)
		s.startCh = make(chan *proto.XiYouSecretStartResp, 1)
		s.selectCh = make(chan *proto.XiYouSecretSelectResp, 1)
		s.killCh = make(chan *proto.XiYouSecretKillResp, 1)
		s.sweepCh = make(chan *proto.XiYouSecretSweepResp, 1)
	})
}

// pushDrop/drainCh: reader-side push (drop-if-full — the sender owns timing)
// and sender-side pre-fire drain, generic over the per-op reply channels.
func pushDrop[T any](ch chan T, v T) {
	if ch == nil {
		return
	}
	for {
		select {
		case <-ch:
			continue
		default:
		}
		select {
		case ch <- v:
		default:
		}
		return
	}
}

func drainCh[T any](ch chan T) {
	for {
		select {
		case <-ch:
			continue
		default:
			return
		}
	}
}

// pushInfo/pushSeed are reader-side: drop-if-full, the sender owns timing.
func (s *xiyouState) pushInfo(i *proto.XiYouPlayerDataMsg) {
	pushDrop(s.infoCh, i)
	pushDrop(s.infoSecCh, i) // secret runner has its own pump
}

func (s *xiyouState) pushSeed(m xiyouSeedMsg) {
	if s.seedCh == nil {
		return
	}
	for {
		select {
		case <-s.seedCh:
			continue
		default:
		}
		select {
		case s.seedCh <- m:
		default:
		}
		return
	}
}

// drainSeed drops any stale answer so the next wait only sees the reply to
// the request we are about to fire.
func (s *xiyouState) drainSeed() {
	for {
		select {
		case <-s.seedCh:
			continue
		default:
		}
		return
	}
}

// xiyouIsChapterBoss: type-2 row whose nextStage opens a new chapter
// (row ids encode chapter in the ten-thousands: 10507→20101 gates ch1→ch2).
func xiyouIsChapterBoss(st map[int32]xiyouStage, row int32) bool {
	s, ok := st[row]
	return ok && s.typ == 2 && s.next/10000 != row/10000
}

// xiyouIsBossRow: any type-2 row. An IN-chapter boss is normally auto-
// skipped (a claim from the row before jumps past it — nobody stands there),
// but once progress STANDS on a boss row (SYNC landed it there, e.g. from
// probes), leaving it is a real-fight gate too: blind assert from 190307
// answers 31170/31171 forever (live 2026-11-05, <live-user>). Both boss
// kinds therefore take the sim path.
func xiyouIsBossRow(st map[int32]xiyouStage, row int32) bool {
	s, ok := st[row]
	return ok && s.typ == 2
}

func (s *xiyouState) str(v *atomic.Value) string {
	if x, ok := v.Load().(string); ok {
		return x
	}
	return ""
}

// parkSec — CHỈ dùng cho ca bị CHẶN START (thiếu lực chiến / feasibility
// offline thua sạch): không đánh được nên nằm im chờ power/frontier đổi.
// THUA ĐẤU (TimeOut/RoleDie/frames) KHÔNG BAO GIỜ park — chủ lệnh 2026-11-09
// "thua thì phải đánh mãi": loop tự xin seed mới và bắn lại ải mới nhất,
// thua không tốn vé (vé chỉ bị Start ăn khi WIN).
func (s *xiyouState) parkSec(id int32) {
	s.secParkID.Store(id)
	s.secParkAt.Store(time.Now().Unix())
}

// secParkExpired = the minimum hold elapsed, so a power/frontier change may
// now legitimately disarm the loss-park.
func (s *xiyouState) secParkExpired() bool {
	at := s.secParkAt.Load()
	if at == 0 {
		return true
	}
	return time.Now().Unix()-at >= int64(xiyouSecretParkHold.Seconds())
}

// ─── wire ───────────────────────────────────────────────────────────────────

const (
	XIYOU_ATTACK_CMD            = "000386bd" // 231101 {activityId, times}
	XIYOU_RAND_EQUIPMENT_CMD    = "000386be" // 231102 {activityId, num}
	XIYOU_RESOLVE_EQUIPMENT_CMD = "000386c0" // 231104 {activityId, ids[]} (unused — see header)
	XIYOU_UPDATE_PROGRESS_CMD   = "000386c9" // 231113 {activityId, progress, codeVersion, levels}
	XIYOU_DATA_INFO_CMD         = "000386cc" // 231116 {activityId}
	XIYOU_UPDATE_SEED_CMD       = "000386e1" // 231137 {activityId} → {ret, seed}

	// Ảo Cảnh (Secret): Start picks the run (server hands doorIds),
	// Select pays a cleared room + hands the next doors, Kill settles the
	// boss win with the sim's fightData+levels, Sweep = instant clear of an
	// already-cleared secret (3 free/day).
	XIYOU_SECRET_START_CMD  = "000386d7" // 231127 {activityId,secretId,rate}
	XIYOU_SECRET_SELECT_CMD = "000386d8" // 231128 {…doorId}
	XIYOU_SECRET_KILL_CMD   = "000386d9" // 231129 {…useLuckItemIds,codeVersion,fightData,levels}
	XIYOU_SECRET_SWEEP_CMD  = "000386e2" // 231138 {…useLuckItemIds}

	SERVER_XIYOU_ATTACK       = "797d" // 31101
	SERVER_XIYOU_RAND_EQUIP   = "797e" // 31102
	SERVER_XIYOU_RESOLVE      = "7980" // 31104
	SERVER_XIYOU_UPD_PROGRESS = "7989" // 31113
	SERVER_XIYOU_DATA         = "798c" // 31116
	SERVER_XIYOU_SYNC         = "798d" // 31117 XiYouPlayerDataMsg push
	SERVER_XIYOU_UPDATE_SEED  = "79a1" // 311137 seed reply {ret, seed}

	SERVER_XIYOU_SECRET_START  = "7997" // 311127
	SERVER_XIYOU_SECRET_SELECT = "7998" // 311128
	SERVER_XIYOU_SECRET_KILL   = "7999" // 311129
	SERVER_XIYOU_SECRET_SWEEP  = "79a2" // 311138

	XIYOU_ACTIVITY_TYPE = 311 // activity-list Type of Tây Du Phục Ma (live-verified)

	xiyouCodeVersion = "1.0.4"         // client XiYouLogicConfig.CodeVersion, build 40108
	xiyouAttackTimes = 5               // AUTO_SETTING_COST (energy per attack tick)
	xiyouRandNum     = 2               // AUTO_SETTING_KILL_NUM (kills per tick)
	xiyouAdvanceN    = 4               // AUTO_SETTING_PROGRESS (ticks before stage-up)
	xiyouTickDelay   = 5 * time.Second // client backAuto setTimeout(5000)
)

// ─── stage table (baked CDN XiYouStageDB, id → type/nextStage) ──────────────

//go:embed xiyou_stages.json
var xiyouStagesRaw []byte

// stage row: type 0=Normal 1=Elite 2=Boss (client XiYouStageType).
type xiyouStage struct {
	typ  int32
	next int32
}

var (
	xiyouStagesOnce sync.Once
	xiyouStages     map[int32]xiyouStage
)

func xiyouStageTable() map[int32]xiyouStage {
	xiyouStagesOnce.Do(func() {
		var rows [][]int32
		if err := json.Unmarshal(xiyouStagesRaw, &rows); err != nil {
			log.Printf("[XiYou] stage table corrupt: %v", err)
			return
		}
		xiyouStages = make(map[int32]xiyouStage, len(rows))
		for _, r := range rows {
			if len(r) >= 3 {
				xiyouStages[r[0]] = xiyouStage{typ: r[1], next: r[2]}
			}
		}
	})
	return xiyouStages
}

// ─── sender ─────────────────────────────────────────────────────────────────

// autoXiyou is the main-line spam loop (the "gần giống mạo hiểm" loop):
// blind-fire ATTACK + RAND_EQUIPMENT every ~5s, assert a stage-up every 4th
// tick. State is read/written between sends so a response can land at any
// time (deliver runs on its own goroutine).
func (p *Player) autoXiyou() {
	ticks := 0
	// Prod visibility (owner 2026-11-05: "chạy ở prod mà không thấy log gì
	// của xiyou"): the main-line happy path used to be completely silent.
	// Now: one line when the toggle turns on, one on activity discovery, and
	// a 60s heartbeat while running (owner's skytrade "không thấy chạy"
	// lesson — the engine must PROVE it's alive in the log).
	var lastBeat time.Time
	onLogged := false
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		if !p.Xiyou {
			onLogged = false
			time.Sleep(10 * time.Second)
			continue
		}
		// Feasibility park: main line quiet (one log), Ảo Cảnh unaffected —
		// only /start_taydu re-arms (clears mainStopped).
		if p.xiyou.mainStopped.Load() {
			if !onLogged {
				log.Printf("[%s] [XiYou] main-line đang park (boss quá yếu) — chỉ /start_taydu mới mở lại", p.Username)
				onLogged = true
			}
			time.Sleep(60 * time.Second)
			continue
		}
		// Ảo Cảnh đang đánh ván nào — main-line tạm nghỉ (giống client thật:
		// mở Ảo Cảnh là auto chính tuyến dừng). Không bắn ATTACK/RAND/
		// UPDATE_PROGRESS giữa ván secret: exp/realm đổi là server replay
		// lệch sim → Kill 31170 (fix 2026-11-05).
		if p.xiyou.secRunning.Load() {
			time.Sleep(2 * time.Second)
			continue
		}
		if !onLogged {
			log.Printf("[%s] [XiYou] engine bật (chờ tìm activity…)", p.Username)
			onLogged = true
		}

		// (re)discover the activity each round it's needed: no id yet, or
		// the last probe said it's gone (ret=210 clears the id in deliver).
		if p.xiyou.actID.Load() == 0 {
			id := p.xiyouFindActivity()
			if id == 0 {
				time.Sleep(60 * time.Second)
				continue
			}
			p.xiyou.actID.Store(id)
			log.Printf("[%s] [XiYou] thấy activity %d — xin DATA_INFO", p.Username, id)
			// Ask for state once per discovery; deliver folds progress/power.
			// (Not a round-trip — fire and let deliver land it whenever.)
			body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: id})
			p.write(pack.Pack(XIYOU_DATA_INFO_CMD, body, p.UID))
			time.Sleep(2 * time.Second)
			continue
		}

		actID := p.xiyou.actID.Load()
		if now := time.Now(); now.Sub(lastBeat) >= 60*time.Second {
			lastBeat = now
			pw, _ := p.xiyou.power.Load().(string)
			log.Printf("[%s] [XiYou] heartbeat: act=%d ải=%d realm=%d lực=%s",
				p.Username, actID, p.xiyou.progress.Load(), p.xiyou.realm.Load(), pw)
		}
		// Parked because the sim harness couldn't run: go quiet, poll state
		// every minute so the engine resumes the moment progress moves
		// (owner crossed by hand, or the host got fixed and someone nudges).
		if wall := p.xiyou.bossWall.Load(); wall != 0 && wall == p.xiyou.progress.Load() {
			body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
			p.write(pack.Pack(XIYOU_DATA_INFO_CMD, body, p.UID))
			time.Sleep(60 * time.Second)
			continue
		}
		atk, _ := pb.Marshal(&proto.XiYouAttackReq{ActivityId: actID, Times: xiyouAttackTimes})
		p.write(pack.Pack(XIYOU_ATTACK_CMD, atk, p.UID))
		rand, _ := pb.Marshal(&proto.XiYouRandEquipmentReq{ActivityId: actID, Num: xiyouRandNum})
		p.write(pack.Pack(XIYOU_RAND_EQUIPMENT_CMD, rand, p.UID))
		ticks++

		if ticks >= xiyouAdvanceN {
			ticks = 0
			cur := p.xiyou.progress.Load()
			if st, ok := xiyouStageTable()[cur]; ok && st.next != 0 && p.xiyou.bossWall.Load() != cur {
				if xiyouIsBossRow(xiyouStageTable(), cur) {
					// Real-fight gate: replay the client combat sim and
					// claim with sim-exact levels (see xiyouBossSim).
					// LOSS = keep fighting — every attempt uses a fresh
					// server seed so the battle rolls differently (owner:
					// "đánh thua thì chạy loop đánh mãi, đến lúc user gọi
					// stop mới dừng"). Only park when the sim harness
					// itself is unavailable (no node on the bot host).
					won, available := p.xiyouBossSim(actID, cur, st.next)
					if !available {
						p.xiyouParkBoss(cur)
					} else if !won && !p.xiyou.mainStopped.Load() {
						// mainStopped = feasibility all-lose already parked the
						// main line (+notify) this call; don't contradict it.
						log.Printf("[%s] [XiYou] boss %d thua — đánh tiếp (seed mới, trận khác)", p.Username, cur)
					}
				} else {
					up, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
						ActivityId: actID, Progress: st.next, CodeVersion: xiyouCodeVersion,
					})
					p.write(pack.Pack(XIYOU_UPDATE_PROGRESS_CMD, up, p.UID))
				}
			}
		}

		time.Sleep(xiyouTickDelay)
	}
}

// ─── chapter boss: client-sim replay ────────────────────────────────────────

type xiyouSimResult struct {
	Result      int     `json:"result"` // IXiYouCombatResult: 2 = AllEnemyDie (win)
	ResultName  string  `json:"resultName"`
	Levels      []int32 `json:"levels"`
	Ms          int     `json:"ms"`
	Frames      int     `json:"frames"`
	EnemyHpLeft float64 `json:"enemyHpLeft"` // hill-climb proxy (loadout search)
	EnemyHpMax  float64 `json:"enemyHpMax"`  // sum MaxHp — denominator cho left%
}

// xiyouSimDir is where harness.js + the extracted bundle/DBs live
// (deployable; override with TYTD_XIYOU_SIM on other hosts).
func xiyouSimDir() string {
	if v := os.Getenv("TYTD_XIYOU_SIM"); v != "" {
		return v
	}
	return "/workspace/tytd/xiyou_sim"
}

// ─── shared sim daemon ──────────────────────────────────────────────────────
// ONE warm node process for the whole bot (owner RAM law: no per-round
// spawns). The bundle + 26 DBs are parsed once at spawn; requests ride
// stdin/stdout as JSON lines. Killed after idle → zero resident RAM while
// nobody farms a boss. All access serialized on mu (sim rounds are
// sequential by nature, ~1/s per server rate-limit).
var xiyouDaemon = struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	in    *bufio.Writer
	out   *bufio.Scanner
	idler *time.Timer
	seq   int
}{}

const xiyouDaemonIdle = 2 * time.Minute

func xiyouDaemonStartLocked() error {
	if xiyouDaemon.cmd != nil {
		return nil
	}
	cmd := exec.Command("node", "--max-old-space-size=96", "harness.js")
	cmd.Dir = xiyouSimDir()
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	xiyouDaemon.cmd = cmd
	xiyouDaemon.in = bufio.NewWriter(in)
	xiyouDaemon.out = sc
	log.Printf("[XiYou] sim daemon lên (node --max-old-space-size=96, idle %v)", xiyouDaemonIdle)
	return nil
}

func xiyouDaemonStopLocked() {
	if xiyouDaemon.idler != nil {
		xiyouDaemon.idler.Stop()
		xiyouDaemon.idler = nil
	}
	if xiyouDaemon.cmd != nil {
		_ = xiyouDaemon.cmd.Process.Kill()
		_ = xiyouDaemon.cmd.Wait()
		xiyouDaemon.cmd = nil
		xiyouDaemon.in = nil
		xiyouDaemon.out = nil
		log.Printf("[XiYou] sim daemon nghỉ (idle)")
	}
}

// xiyouFight replays one boss fight. Primary: the EMBEDDED goja engine
// (internal/xiyoujs — zero external deps, the parity test pins it bit-exact
// to the node harness). Fallback: the node daemon (only if goja's runtime
// proved broken on this bundle, e.g. a future game update uses JS goja
// can't parse). Both down → error → the engine parks with a host alarm.
func xiyouFight(role json.RawMessage, row, seed int32) (*xiyouSimResult, error) {
	if r, err := xiyoujs.Sim(role, row, seed); err == nil {
		return &xiyouSimResult{
			Result: r.Result, ResultName: r.ResultName, Levels: r.Levels,
			Ms: r.Ms, Frames: r.Frames, EnemyHpLeft: r.EnemyHpLeft, EnemyHpMax: r.EnemyHpMax,
		}, nil
	} else {
		log.Printf("[%s] [XiYou] goja sim fail (%v) — fallback node daemon", "bot", err)
	}
	m, err := xiyouSimRequest(role, row, seed)
	if err != nil {
		return nil, err
	}
	var res xiyouSimResult
	if e, _ := m["error"].(string); e != "" {
		return nil, fmt.Errorf("sim: %s", e)
	}
	if f, ok := m["result"].(float64); ok {
		res.Result = int(f)
	}
	res.ResultName, _ = m["resultName"].(string)
	if f, ok := m["ms"].(float64); ok {
		res.Ms = int(f)
	}
	if f, ok := m["frames"].(float64); ok {
		res.Frames = int(f)
	}
	if f, ok := m["enemyHpLeft"].(float64); ok {
		res.EnemyHpLeft = f
	}
	if f, ok := m["enemyHpMax"].(float64); ok {
		res.EnemyHpMax = f
	}
	if lv, ok := m["levels"].([]interface{}); ok {
		for _, v := range lv {
			if f, ok := v.(float64); ok {
				res.Levels = append(res.Levels, int32(f))
			}
		}
	}
	return &res, nil
}

// xiyouSimRequest replays one fight through the shared daemon. role is the
// protojson'd XiYouPlayerDataMsg.info.
func xiyouSimRequest(role json.RawMessage, stage, seed int32) (map[string]interface{}, error) {
	xiyouDaemon.mu.Lock()
	defer xiyouDaemon.mu.Unlock()
	if err := xiyouDaemonStartLocked(); err != nil {
		xiyouDaemonStopLocked()
		return nil, fmt.Errorf("daemon start: %w", err)
	}
	xiyouDaemon.seq++
	id := xiyouDaemon.seq
	req, _ := json.Marshal(map[string]interface{}{
		"id": id, "role": role, "stage": stage, "seed": seed, "timelimit": 45000,
	})
	if _, err := xiyouDaemon.in.Write(append(req, '\n')); err != nil {
		xiyouDaemonStopLocked()
		return nil, err
	}
	if err := xiyouDaemon.in.Flush(); err != nil {
		xiyouDaemonStopLocked()
		return nil, err
	}
	proc := xiyouDaemon.cmd // watchdog kills the process (never needs mu) → Scan unblocks
	wd := time.AfterFunc(60*time.Second, func() {
		if proc != nil && proc.Process != nil {
			_ = proc.Process.Kill()
		}
	})
	defer wd.Stop()
	for xiyouDaemon.out.Scan() {
		line := bytes.TrimSpace(xiyouDaemon.out.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m map[string]interface{}
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if f, _ := m["id"].(float64); int(f) != id {
			continue // stale line from an earlier wedged request
		}
		if xiyouDaemon.idler != nil {
			xiyouDaemon.idler.Stop()
		}
		xiyouDaemon.idler = time.AfterFunc(xiyouDaemonIdle, func() {
			xiyouDaemon.mu.Lock()
			xiyouDaemonStopLocked()
			xiyouDaemon.mu.Unlock()
		})
		return m, nil
	}
	err := xiyouDaemon.out.Err()
	xiyouDaemonStopLocked()
	if err == nil {
		err = fmt.Errorf("daemon đóng unexpectedly")
	}
	return nil, err
}

// Seed-farm pacing, live-measured 2026-11-05: UPDATE_SEED answers ret=0
// ~once per second (every other 0.5s request got ret=30). A sim round is
// ~0.3s (the fight is deterministic; 13s of fight-time simulates in ms),
// so ~1.4s/round is the legal max tempo — owner's "spam xin seed đến bao
// giờ win thì claim" (2026-11-05), run server-paced, not faster.
// Owner law 2026-11-05 (build-change day): "spam đủ 5 phút hoặc win thì
// dừng" — each boss visit farms the full budget.
const (
	xiyouSeedFarmTime = 5 * time.Minute // fresh seeds per boss visit
	xiyouSeedFarmPace = 1300 * time.Millisecond
	xiyouFeasScanMax  = 300 // offline seeds before declaring "acc quá yếu" (owner 2026-11-05: 1k→500→300)

	// Ảo Cảnh pacing: one run per tick-ish, quiet backoff on refusal.
	xiyouSecretTickDelay = 5 * time.Second
	xiyouSecretReplyWait = 10 * time.Second
	// xiyouFeasGateOn = 2 offline feasibility gates (300-seed scans, main
	// boss + Ảo Cảnh). TẮT theo chủ lệnh 2026-11-05 "server tao yếu quá,
	// tạm thời disable cái gate check offline 300" — host không gánh nổi
	// ~300 sim liên tục. Bật lại = đổi true (luật stop/notify vẫn nguyên).
	xiyouFeasGateOn = false
	// xiyouSweepOn = free-sweep path (SWEEP 231138 on frontier-1). TẮT theo
	// chủ lệnh 2026-11-05 "mất 3 2 vé mà lên có 1 tầng ảo cảnh": hạn free
	// ngày là SERVER-arbitrated và fold dailySweepTimes luôn = 0 (không tin
	// được) → khi quota cạn, MỖI SWEEP TRỪ 1 VÉ 105286 mà frontier không
	// nhúc nhích = đốt vé lấy thưởng, phạm luật ITEM BAN. Bao giờ fold
	// dailySweepTimes được xác nhận đếm đúng mới bật lại.
	xiyouSweepOn          = false
	xiyouSecretQuietSleep = 60 * time.Second
	// xiyouSecretParkHold = thời gian TỐI THIỂU giữ start-blocked park.
	// Main-line spam đổi power từng tick → fold power-unpark liên tục gỡ park,
	// Ảo Cảnh quay lại đốt cycle vào đúng ải thiếu lực chiến. Trong hạn này
	// power đổi cũng KHÔNG được unpark; hết hạn + power/frontier đổi mới thử lại.
	xiyouSecretParkHold = 10 * time.Minute
	xiyouSecretMaxRooms = 12 // rooms per run incl. boss (template pools are 5-8)
)

// xiyouFeasibilityScan (owner 2026-11-05: "auto gen max 1000 seed rồi lắp
// vào info acc chạy offline, nếu thua hết thì notify acc quá yếu, nếu có
// win thì dừng và xin seed thật để chạy") — pure OFFLINE probe: fabricate up
// to xiyouFeasScanMax random seeds (server seeds observed are ≤6 digits),
// replay each against the acc's own DATA_INFO role via xiyouFight (goja
// first, node fallback). Stops at the FIRST win. Since the fight is
// deterministic given (role, stage, seed), one offline win means the real
// gate is winnable once the server happens to issue a winning seed; 500
// losses at ~0.1s/seed means the acc simply can't out-DPS this boss yet.
// Returns (won, seedsTested, simErr) — simErr != nil only when the sim
// itself is broken (caller must NOT conclude weakness from that).
func (p *Player) xiyouFeasibilityScan(role json.RawMessage, row int32) (bool, int, error) {
	for i := 1; i <= xiyouFeasScanMax; i++ {
		select {
		case <-p.ctx.Done():
			return false, i - 1, nil
		default:
		}
		seed := rand.Int31n(1_000_000) + 1
		res, err := xiyouFight(role, row, seed)
		if err != nil {
			return false, i, err
		}
		if res.Result == 2 { // AllEnemyDie — thắng offline, đủ rồi
			log.Printf("[%s] [XiYou] boss %d: feasibility THẮNG offline ở seed thứ %d (seed=%d, %d ms fight) — xin seed thật",
				p.Username, row, i, seed, res.Ms)
			return true, i, nil
		}
		if i%100 == 0 {
			log.Printf("[%s] [XiYou] boss %d: feasibility offline %d/%d seed — toàn bộ THUA (result=%d, %d ms fight)",
				p.Username, row, i, xiyouFeasScanMax, res.Result, res.Ms)
		}
	}
	log.Printf("[%s] [XiYou] boss %d: feasibility thua cả %d seed offline — acc chưa đủ lực",
		p.Username, row, xiyouFeasScanMax)
	return false, xiyouFeasScanMax, nil
}

// xiyouBossSim mirrors the client's onReqBossFight, farmed: DATA_INFO once,
// then a xiyouSeedFarmTime budget of (UPDATE_SEED → game's own combat sim via
// the node harness) — LOSS goes straight to the next seed at the server's
// own rate limit; on the first WIN we wait out that fight's real duration
// and assert UPDATE_PROGRESS with the sim-exact levels. The server replays
// the fight against the seed it issued and pays out (live-proved 2026-11-05:
// 10507→20101 ret=0). Returns (won, available): available=false only when
// the harness itself can't run (node missing / bad dir / harness broke) —
// the sender parks then. won=false with available=true means "still losing"
// (or the farm budget ran out) — the sender loops back for another visit.
func (p *Player) xiyouBossSim(actID, row, next int32) (bool, bool) {
	p.xiyou.chans()
	// reqRoleData once — the role doesn't change between farm rounds.
	body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
	p.write(pack.Pack(XIYOU_DATA_INFO_CMD, body, p.UID))
	var info *proto.XiYouPlayerDataMsg
	select {
	case <-p.ctx.Done():
		return false, false
	case info = <-p.xiyou.infoCh:
	case <-time.After(10 * time.Second):
	}
	if info == nil {
		log.Printf("[%s] [XiYou] boss %d: không lấy được DATA_INFO (timeout/ret!=0) — thử lại sau", p.Username, row)
		return false, true
	}
	js, err := protojson.Marshal(info)
	if err != nil {
		return false, false
	}
	// FULL role for the sim — the real client builds its main-line combat
	// from the same buildRoleData(skills, magicWeapon) as Ảo Cảnh, so the
	// server validates levels against a FULL-role replay. (The old strip-pin
	// "proved" on <live-user> only because that acc owns no loadout — stripped
	// == full there; offline re-check 2026-11-05: vietdart @190407 loses
	// 0/30 BOTH ways, so the pin cost nothing yet risked false-negatives on
	// loadout accounts.)
	// debug dump: the exact role the farm used (offline repro of a goja/node
	// verdict difference: node harness.js --role <this> --stage --seed).
	// Enable with: touch /tmp/xiyou_debug_on
	if _, err := os.Stat("/tmp/xiyou_debug_on"); err == nil {
		_ = os.WriteFile(filepath.Join(os.TempDir(), fmt.Sprintf("xiyou_role_%d.json", p.UID)), js, 0o600)
	}
	// ── feasibility gate (owner law 2026-11-05): "test offline mà thấy thua
	// thì notify rồi dừng luôn… nếu trong 300 đó mà có qua ải thì spam với
	// server thật đến chết". Up to xiyouFeasScanMax OFFLINE seeds against the
	// acc's own DATA_INFO before spending any server seed. ANY win → memo the
	// (row,power) as passable and farm real server seeds (loss-forever loop).
	// ALL lose → forceNotify "acc quá yếu" and STOP the engine outright
	// (p.Xiyou=false; user /start_taydu again once stronger — the memo below
	// only remembers wins, so a restart re-scans and re-notifies).
	if xiyouFeasGateOn { // TẮT tạm thời (chủ lệnh 2026-11-05: host yếu) — bật lại = đổi const
		pwr := info.GetPower()
		if !(p.xiyou.feasRow == row && p.xiyou.feasPow == pwr && p.xiyou.feasWin) {
			won, tested, err := p.xiyouFeasibilityScan(js, row)
			switch {
			case err != nil:
				// sim itself broken — don't gate; let the farm surface the host alarm
				log.Printf("[%s] [XiYou] boss %d: feasibility scan lỗi (%v) — bỏ gate, farm luôn", p.Username, row, err)
			case won:
				p.xiyou.feasRow, p.xiyou.feasPow, p.xiyou.feasWin = row, pwr, true
				log.Printf("[%s] [XiYou] boss %d: feasibility OK — spam seed thật", p.Username, row)
			default:
				_ = tested // chủ lệnh 2026-11-05: notify chỉ nói "acc quá yếu", không nhắc seed/offline
				notif := fmt.Sprintf(
					"Tây Du: acc quá yếu, không qua được ải %d. Bot DỪNG đánh ải chính (Ảo Cảnh vẫn chạy riêng) — tăng lực chiến rồi /start_taydu lại.",
					row)
				log.Printf("[%s] [XiYou] %s", p.Username, notif)
				p.forceNotify(notif)
				p.xiyou.mainStopped.Store(true)
				return false, true
			}
		}
	}
	sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
	farmDeadline := time.Now().Add(xiyouSeedFarmTime)
	round := 0
	for time.Now().Before(farmDeadline) {
		round++
		select {
		case <-p.ctx.Done():
			return false, true
		default:
		}
		// One round = one EXCLUSIVE window over the server's single current
		// seed (fix 2026-11-05 "sim đúng nhưng server không cho qua"): hold
		// seedMu from the request through the sim — and through the claim on
		// a win — so no other engine's UPDATE_SEED (Ảo Cảnh shares 231137)
		// can replace the seed the server will replay our levels against.
		p.xiyou.seedMu.Lock()
		p.xiyou.drainSeed()
		p.write(pack.Pack(XIYOU_UPDATE_SEED_CMD, sb, p.UID))
		var sm xiyouSeedMsg
		have := false
		select {
		case <-p.ctx.Done():
			p.xiyou.seedMu.Unlock()
			return false, true
		case sm = <-p.xiyou.seedCh:
			have = true
		case <-time.After(10 * time.Second):
		}
		if !have {
			p.xiyou.seedMu.Unlock()
			log.Printf("[%s] [XiYou] boss %d: UPDATE_SEED im lặng 10s (reader có forward 79a1 không?) — thử lại sau", p.Username, row)
			return false, true
		}
		if sm.ret != 0 || sm.seed == 0 {
			p.xiyou.seedMu.Unlock()
			time.Sleep(xiyouSeedFarmPace) // ret=30 rate-limit — server-paced
			continue
		}
		seed := sm.seed
		res, err := xiyouFight(js, row, seed)
		if err != nil {
			p.xiyou.seedMu.Unlock()
			log.Printf("[%s] [XiYou] boss sim lỗi (goja+node đều hỏng? TYTD_XIYOU_SIM=%s): %v",
				p.Username, xiyouSimDir(), err)
			return false, false
		}
		if res.Result != 2 {
			p.xiyou.seedMu.Unlock()
			log.Printf("[%s] [XiYou] boss %d round %d: THUA (kết quả %d, %dms, seed %d%s) — xin seed mới",
				p.Username, row, round, res.Result, res.Ms, seed, xiyouEnemyHpTxt(res.EnemyHpLeft, res.EnemyHpMax))
			time.Sleep(xiyouSeedFarmPace) // stay inside the server's ~1s window
			continue
		}
		// WIN — still holding the seed window. Server replays with its OWN
		// current role, so re-check freshness before burning the claim: if
		// power moved since the sim snapshot (Ảo Cảnh rewards, realm-ups)
		// the asserted levels are stale → refresh the role, farm on.
		db, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
		drainCh(p.xiyou.infoCh)
		p.write(pack.Pack(XIYOU_DATA_INFO_CMD, db, p.UID))
		var fresh *proto.XiYouPlayerDataMsg
		select {
		case <-p.ctx.Done():
			p.xiyou.seedMu.Unlock()
			return false, true
		case fresh = <-p.xiyou.infoCh:
		case <-time.After(xiyouSecretReplyWait):
		}
		if fresh != nil && fresh.GetPower() != info.GetPower() {
			p.xiyou.seedMu.Unlock()
			log.Printf("[%s] [XiYou] boss %d round %d: WIN nhưng role đổi (%s → %s) giữa chừng — bỏ claim, sim lại với role mới",
				p.Username, row, round, info.GetPower(), fresh.GetPower())
			if nj, e := protojson.Marshal(fresh); e == nil {
				js, info = nj, fresh
			}
			time.Sleep(xiyouSeedFarmPace)
			continue
		}
		sleepFor := time.Duration(res.Ms) * time.Millisecond
		if sleepFor > 50*time.Second {
			sleepFor = 50 * time.Second
		}
		log.Printf("[%s] [XiYou] boss %d round %d: WIN sau %v (seed %d) — chờ hết thời gian đánh rồi claim",
			p.Username, row, round, sleepFor, seed)
		select {
		case <-p.ctx.Done():
			p.xiyou.seedMu.Unlock()
			return false, true
		case <-time.After(sleepFor):
		}
		up, _ := pb.Marshal(&proto.XiYouUpdateProgressReq{
			ActivityId: actID, Progress: next, CodeVersion: xiyouCodeVersion, Levels: res.Levels,
		})
		before := p.xiyou.progress.Load()
		p.write(pack.Pack(XIYOU_UPDATE_PROGRESS_CMD, up, p.UID))
		won := false
		lim := time.Now().Add(15 * time.Second)
		for time.Now().Before(lim) {
			if p.xiyou.progress.Load() != before {
				won = true
				break
			}
			select {
			case <-p.ctx.Done():
				p.xiyou.seedMu.Unlock()
				return false, true
			case <-time.After(300 * time.Millisecond):
			}
		}
		p.xiyou.seedMu.Unlock()
		if won {
			return true, true
		}
		// sim said WIN but the claim was refused (31170/31171 lands in
		// deliver) — count the round as lost, farm on.
		log.Printf("[%s] [XiYou] boss %d: claim bị từ chối — đánh tiếp", p.Username, row)
	}
	return false, true
}

// xiyouParkBoss parks the engine only when the sim harness itself cannot
// run (node missing / bad TYTD_XIYOU_SIM / no server data) — a LOSS never
// parks: the sender keeps attacking with a fresh seed until /stop_taydu.
// The wall self-clears once progress moves again (foldPlayerData).
func (p *Player) xiyouParkBoss(row int32) {
	p.xiyou.bossWall.Store(row)
	if !p.xiyou.walled.Swap(true) {
		p.forceNotify(fmt.Sprintf(
			"Tây Du: kẹt ải boss %d — bot không chạy được bộ giả lập trận đánh (thiếu node hoặc thư mục xiyou_sim trên host bot). Báo admin kiểm tra host; bot sẽ tự đi tiếp khi đánh được.",
			row))
	}
}

// xiyouFindActivity returns the activityId of the open Tây Du Phục Ma
// activity (type 311, now inside beginTime..endTime = Battle phase), or 0.
func (p *Player) xiyouFindActivity() int32 {
	nowMs := time.Now().UnixMilli()
	for _, e := range p.ActList.byType(XIYOU_ACTIVITY_TYPE) {
		if e.BeginTime <= nowMs && nowMs < e.EndTime {
			return e.ActivityID
		}
	}
	return 0
}

// ─── reader ─────────────────────────────────────────────────────────────────

// xiyouDeliver folds every XiYou server message into the Player fields.
// It never sends anything and never blocks.
func (p *Player) xiyouDeliver(msgType string, data []byte) {
	actID := p.xiyou.actID.Load()
	switch msgType {
	case SERVER_XIYOU_DATA:
		var r proto.XiYouPlayerDataInfoResp
		if err := pb.Unmarshal(data, &r); err != nil {
			return
		}
		if r.GetRet() == 210 && actID != 0 {
			// Blind id (activity ended / never was XiYou) — force rediscovery.
			p.xiyou.actID.Store(0)
			return
		}
		if r.GetRet() == 0 && r.GetInfo() != nil {
			p.xiyou.pushInfo(r.GetInfo())
			p.xiyouFoldPlayerData(r.GetInfo())
		}
	case SERVER_XIYOU_UPDATE_SEED:
		var r proto.XiYouUpdateSeedResp
		if err := pb.Unmarshal(data, &r); err == nil {
			// push ANY ret — 30 = rate-limited, the farm loop paces on it.
			p.xiyou.pushSeed(xiyouSeedMsg{ret: r.GetRet(), seed: r.GetSeed()})
		}
	case SERVER_XIYOU_SYNC:
		var m proto.XiYouPlayerDataMsg
		if err := pb.Unmarshal(data, &m); err == nil {
			p.xiyouFoldPlayerData(&m)
		}
	case SERVER_XIYOU_UPD_PROGRESS:
		var r proto.XiYouUpdateProgressResp
		if err := pb.Unmarshal(data, &r); err != nil {
			return
		}
		if r.GetRet() == 0 {
			p.xiyou.progress.Store(r.GetProgress())
			log.Printf("[%s] [XiYou] lên ải %d reward=%s", p.Username, r.GetProgress(), r.GetReward())
			p.forceNotify(fmt.Sprintf("Tây Du: qua ải %d — reward %s", r.GetProgress(), r.GetReward()))
			return
		}
		log.Printf("[%s] [XiYou] update_progress ret=%d", p.Username, r.GetRet())
		// Chapter-boss refusal (31170/31171): sim assert rejected (timing /
		// rate limit). Quiet — the sender keeps retrying with a fresh seed
		// until the owner stops the engine (owner law 2026-11-05).
	case SERVER_XIYOU_SECRET_START:
		var r proto.XiYouSecretStartResp
		if pb.Unmarshal(data, &r) == nil {
			pushDrop(p.xiyou.startCh, &r)
		}
	case SERVER_XIYOU_SECRET_SELECT:
		var r proto.XiYouSecretSelectResp
		if pb.Unmarshal(data, &r) == nil {
			pushDrop(p.xiyou.selectCh, &r)
		}
	case SERVER_XIYOU_SECRET_KILL:
		var r proto.XiYouSecretKillResp
		if pb.Unmarshal(data, &r) == nil {
			pushDrop(p.xiyou.killCh, &r)
			if r.GetRet() == 0 && r.GetCurrentSecretId() > p.xiyou.secID.Load() {
				p.xiyou.secID.Store(r.GetCurrentSecretId())
				p.xiyou.secParkID.Store(0) // frontier moved — a harder wall, retry
			}
		}
	case SERVER_XIYOU_SECRET_SWEEP:
		var r proto.XiYouSecretSweepResp
		if pb.Unmarshal(data, &r) == nil {
			pushDrop(p.xiyou.sweepCh, &r)
			if r.GetRet() == 0 {
				p.xiyou.sweepTimes.Store(r.GetDailySweepTimes())
			}
		}
	case SERVER_XIYOU_ATTACK, SERVER_XIYOU_RAND_EQUIP:
		// ret!=0 usually means energy (item 105280) ran dry — quiet, the
		// owner asked for a silent spam engine, log covers the diagnosis.
		var ret int32
		if msgType == SERVER_XIYOU_ATTACK {
			var r proto.XiYouAttackResp
			if pb.Unmarshal(data, &r) == nil {
				ret = r.GetRet()
			}
		} else {
			var r proto.XiYouRandEquipmentResp
			if pb.Unmarshal(data, &r) == nil {
				ret = r.GetRet()
			}
		}
		if ret != 0 {
			// Spam fix 2026-11-05: cạn năng → mỗi tick 5s đều trả lời ret!=0
			// (~101) — chỉ log khi ret ĐỔI giá trị, im lặng khi lặp lại.
			if p.xiyou.opRet != ret {
				log.Printf("[%s] [XiYou] %s ret=%d (cạn năng lượng 105280?)", p.Username, msgType, ret)
				p.xiyou.opRet = ret
			}
		} else {
			p.xiyou.opRet = 0
		}
	}
}

func (p *Player) xiyouFoldPlayerData(m *proto.XiYouPlayerDataMsg) {
	if m.GetProgress() != 0 && m.GetProgress() != p.xiyou.progress.Load() {
		p.xiyou.progress.Store(m.GetProgress())
		// progress moved (sim cross, owner fought by hand, or a stage-up
		// landed) — disarm the harness-unavailable wall.
		p.xiyou.bossWall.Store(0)
		p.xiyou.walled.Store(false)
	}
	p.xiyou.exp.Store(m.GetExp())
	p.xiyou.realm.Store(m.GetRealmId())
	// ── Ảo Cảnh state — rides the same DATA_INFO/SYNC frame ──
	if id := m.GetCurrentSecretId(); id != 0 {
		if p.xiyou.secID.Swap(id) != id {
			p.xiyou.secParkID.Store(0)
		}
	}
	p.xiyou.sweepTimes.Store(m.GetDailySweepTimes())
	// NOTE: m.items is the XiYou equipment ledger (5002xxxx ids), NOT the
	// bag — energy item 105286 never appears here (live-verified
	// 2026-11-05). Energy is server-arbitrated instead: a drained bag makes
	// START answer ret!=0 and the loop goes quiet (same law as main-line
	// 105280). Do not add a local energy gate back.
	if pw := m.GetPower(); pw != "" && pw != p.xiyou.str(&p.xiyou.power) {
		// power moved — a start-blocked park (thiếu lực chiến) becomes worth
		// retrying, NHƯNG chỉ sau min hold (main-line spam làm power nhảy từng
		// tick, gỡ park ngay là Ảo Cảnh đốt cycle vào ải chưa đủ lực liên tục).
		if p.xiyou.secParkExpired() {
			p.xiyou.secParkID.Store(0)
			p.xiyou.secParked.Store(false)
		}
	}
	p.xiyou.power.Store(m.GetPower())
}

// ─── Ảo Cảnh (Secret) auto-run — owner 2026-11-05 "thêm auto chạy ảo cảnh" ──
//
// Unlike the main line, a secret run CANNOT be simulated headlessly in one
// shot: between rooms the client asks the SERVER which doors exist
// (Select resp.doorIds) and only then fights the chosen template. So the
// client's own XiYouSecretCombat runs as a goja SESSION (internal/xiyoujs
// SecretCreate/Step/EnterDoor/Export/Rebuild) and this loop interleaves the
// wire ops, mirroring the client's orchestration to the letter:
//
//	UPDATE_SEED 231137            → seed (same pump as boss farm, seedMu)
//	START   231127 {act,secret,rate} → ret, doorIds[2], selectedDoorsIds (last = room tpl)
//	  step session until room cleared → pick door (starSelect rule) from doorIds
//	    normal: SELECT 231128 {…doorId} → rewards, next doorIds/selectedDoorsIds → EnterDoor
//	    boss  (progress + add(star(door)) ≥ 100): client DESTROYS + REBUILDS the
//	      combat from buildExportData() with data.bossTempId = doorIds[0]
//	  boss dead → KILL 231129 {…fightData=rebuild-export, levels=rebuilt getLevels}
//	  player dead → LOSS, nothing sent (client sends nothing either).
//
// Laws honoured: energy item 105286 (XIYOU_ENERGY_COST) is the only spend —
// authorized engine-scoped core-loop cost like main-line 105280; luck items
// 105288/89/90 NEVER (useLuckItemIds always empty); sweeps only the 3 free
// /day on already-cleared secrets. Offline feasibility first (owner law
// 2026-11-05): 300 fabricated-seed full runs → all lose = forceNotify "acc
// quá yếu" and PARK this frontier until power or the frontier moves (the
// main line keeps running — unlike the boss gate, which stops the engine).

const xiyouSecretEnergyItem = 105286 // XIYOU_ENERGY_COST (GameConfigDB, live-verified 2026-11-05)

// xiyouSecretAdd = client getSecretStartAddProgress(star): XIYOU_SECRET_PROGRESS
// [210,240,270,300,330] /10 → per-room progress on clear.
func xiyouSecretAdd(star int32) float64 {
	tab := [5]float64{21, 24, 27, 30, 33}
	if star < 1 {
		star = 1
	}
	if star > 5 {
		star = 5
	}
	return tab[star-1]
}

// xiyouPickDoor = client starSelect: index 0 → take the lower-star door.
func xiyouPickDoor(ids []int32, high bool) int32 {
	best := ids[0]
	bs := xiyoujs.StarOfTemplate(best)
	for _, d := range ids[1:] {
		s := xiyoujs.StarOfTemplate(d)
		if (high && s > bs) || (!high && s < bs) {
			best, bs = d, s
		}
	}
	return best
}

func xiyouParseProgress(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// xiyouEnemyHpTxt = "còn ~X% HP" từ enemyHpLeft/Max của sim (MaxHp getter
// type 2 của client). "" khi sim không trả được max (role lạ) — log gọn.
func xiyouEnemyHpTxt(left, max float64) string {
	if max <= 0 {
		return ""
	}
	p := int(left / max * 100)
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return fmt.Sprintf(", địch còn ~%d%% HP", p)
}

// xiyouGetSeed pulls one server seed (ret=30 = rate-limit → pace + retry).
// Shares seedMu with the boss farm — seedCh is one pump for two senders.
func (p *Player) xiyouGetSeed(actID int32) (int32, bool) {
	p.xiyou.seedMu.Lock()
	defer p.xiyou.seedMu.Unlock()
	return p.xiyouGetSeedLocked(actID)
}

// xiyouGetSeedLocked = xiyouGetSeed WITHOUT the seedMu dance. CALLER must
// hold seedMu for the whole seed→use window (the server keeps exactly ONE
// current seed per acc — an UPDATE_SEED from the other engine mid-window
// replaces it and the levels we later assert no longer match the server's
// replay: Kill 31170, owner 2026-11-05 "sim đúng nhưng server không cho
// qua"). The farm loop and xiyouSecretRun hold the lock themselves.
func (p *Player) xiyouGetSeedLocked(actID int32) (int32, bool) {
	sb, _ := pb.Marshal(&proto.XiYouUpdateSeedReq{ActivityId: actID})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		p.xiyou.drainSeed()
		p.write(pack.Pack(XIYOU_UPDATE_SEED_CMD, sb, p.UID))
		var sm xiyouSeedMsg
		have := false
		select {
		case <-p.ctx.Done():
			return 0, false
		case sm = <-p.xiyou.seedCh:
			have = true
		case <-time.After(xiyouSecretReplyWait):
		}
		if !have {
			return 0, false
		}
		if sm.ret == 0 && sm.seed != 0 {
			return sm.seed, true
		}
		time.Sleep(xiyouSeedFarmPace)
	}
	return 0, false
}

// autoXiyouSecret is the secret sender goroutine (spawned beside autoXiyou;
// goja calls serialise on the runtime mutex). One sender, one reader — the
// reader (xiyouDeliver) only folds + pushes, never sends.
func (p *Player) autoXiyouSecret() {
	var lastBeat time.Time
	onLogged := false
	// Local sweep cap: live 2026-11-05 (<live-user>) — Sweep ret=0 + reward
	// but dailySweepTimes stayed 0 across ~20 calls (server doesn't count,
	// or resp field guess is off). The FREE tier is 3/day; never trust the
	// fold alone — bound attempts locally (server-arbitrate, bounded-probe
	// law), sync up when the fold does move.
	secAttempts := 0
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		if !p.Xiyou {
			onLogged = false
			time.Sleep(10 * time.Second)
			continue
		}
		if p.xiyou.secStopped.Load() {
			// hết vé 105286 — nằm im tới khi /start_taydu (chủ lệnh 2026-11-05)
			onLogged = false
			time.Sleep(30 * time.Second)
			continue
		}
		if !onLogged {
			log.Printf("[%s] [XiYou] ảo cảnh bật (chờ activity + DATA_INFO…)", p.Username)
			onLogged = true
		}
		actID := p.xiyou.actID.Load()
		if actID == 0 {
			time.Sleep(30 * time.Second)
			continue
		}
		p.xiyou.secChans()
		frontier := p.xiyou.secID.Load()
		// Fire DATA_INFO on its own pump so energy/sweep/frontier stay fresh
		// without racing the main-line sender's infoCh waits.
		body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
		p.write(pack.Pack(XIYOU_DATA_INFO_CMD, body, p.UID))
		time.Sleep(1500 * time.Millisecond) // let the fold land before deciding
		frontier = p.xiyou.secID.Load()
		if frontier == 0 {
			time.Sleep(xiyouSecretQuietSleep) // acc chưa mở Ảo Cảnh (or no fold yet)
			continue
		}
		if now := time.Now(); now.Sub(lastBeat) >= 60*time.Second {
			lastBeat = now
			log.Printf("[%s] [XiYou] ảo cảnh heartbeat: secret=%d sweep=%d/%d (energy 105286 = server-arbitrated)",
				p.Username, frontier, p.xiyou.sweepTimes.Load(), xiyouSecretFreeSweep)
		}
		// Start-blocked park (thiếu lực chiến / feasibility): quiet until power
		// or the frontier moves (fold clears it). Thua đấu KHÔNG rơi vào đây.
		if pk := p.xiyou.secParkID.Load(); pk == frontier {
			time.Sleep(xiyouSecretQuietSleep)
			continue
		}
		// Free sweeps first: instant clear of an ALREADY-CLEARED secret
		// (secretId < frontier), 3/day FREE (client: dailySweepTimes <
		// secretEnergyFreeTimes costs nothing), rate 1, no sim, no fightData.
		// GATE xiyouSweepOn (chủ lệnh 2026-11-05 "bỏ sweep đi chưa"): tắt hẳn
		// vì quota free ngày server-arbitrated còn fold dailySweepTimes luôn=0
		// → hết quota là MỖI SWEEP TRỪ 1 VÉ 105286, frontier đứng nguyên.
		if xiyouSweepOn && p.xiyou.sweepTimes.Load() < xiyouSecretFreeSweep && secAttempts < xiyouSecretFreeSweep && frontier > 1 {
			if p.xiyouSecretSweep(actID, frontier-1) {
				secAttempts++
			}
			if f := int(p.xiyou.sweepTimes.Load()); f > secAttempts {
				secAttempts = f // fold caught up — server counts, trust it
			}
			continue
		}
		p.xiyouSecretVisit(actID, frontier)
		time.Sleep(xiyouSecretTickDelay)
	}
}

const xiyouSecretFreeSweep = 3 // XIYOU_ENERGY_REPEAT_FREE_LIMIT

// xiyouSecretSweep fires one free sweep and waits for the ret. Returns true
// whenever the SERVER answered (any ret) so the caller's local cap advances —
// only a timeout returns false. ret==101 also stops Ảo Cảnh (hết vé law).
func (p *Player) xiyouSecretSweep(actID, secretID int32) bool {
	sb, _ := pb.Marshal(&proto.XiYouSecretSweepReq{ActivityId: actID, SecretId: secretID, Rate: 1})
	drainCh(p.xiyou.sweepCh)
	p.write(pack.Pack(XIYOU_SECRET_SWEEP_CMD, sb, p.UID))
	select {
	case <-p.ctx.Done():
		return false
	case r := <-p.xiyou.sweepCh:
		if r.GetRet() == 101 {
			// Hết vé 105286 cũng chặn cả sweep free (hạn mức ngày cạn + bag
			// trống) — cùng luật Start: DỪNG Ảo Cảnh hẳn tới /start_taydu.
			// (Spam fix 2026-11-05: ret!=0 từng KHÔNG đếm try → loop sweep
			// lại tức thì, log "ret=101" vô hạn.)
			log.Printf("[%s] [XiYou] ảo cảnh sweep %d ret=101 — hết vé 105286, DỪNG Ảo Cảnh", p.Username, secretID)
			p.forceNotify("Tây Du Ảo Cảnh: acc hết vé — bot DỪNG Ảo Cảnh (main-line vẫn chạy). Nạp vé xong /start_taydu lại để đánh tiếp.")
			p.xiyou.secStopped.Store(true)
			return true
		}
		if r.GetRet() != 0 {
			log.Printf("[%s] [XiYou] ảo cảnh sweep %d ret=%d — tính 1 lần thử (chống spam)", p.Username, secretID, r.GetRet())
			return true // có trả lời = đốt 1 lượt local cap, không lặp cùng ret vô hạn
		}
		log.Printf("[%s] [XiYou] ảo cảnh sweep %d OK reward=%s (fold sweepTimes=%d)",
			p.Username, secretID, r.GetRewards(), r.GetDailySweepTimes())
		return true
	case <-time.After(xiyouSecretReplyWait):
		return false
	}
}

// xiyouSecretVisit = one frontier attempt: role → feasibility gate → run.
func (p *Player) xiyouSecretVisit(actID, frontier int32) {
	// Main-line tick tạm nghỉ suốt lượt này (lấy role → run → Kill): server
	// replay trận secret bằng role hiện tại của nó, để ATTACK/RAND chính
	// tuyến làm exp/realm nhảy giữa ván là Kill 31170 (fix 2026-11-05
	// "sim đúng nhưng server không cho qua").
	p.xiyou.secRunning.Store(true)
	defer p.xiyou.secRunning.Store(false)
	p.xiyou.secChans()
	// role = acc's own DATA_INFO info (same shape as the boss sim)
	body, _ := pb.Marshal(&proto.XiYouPlayerDataInfoReq{ActivityId: actID})
	drainCh(p.xiyou.infoSecCh)
	p.write(pack.Pack(XIYOU_DATA_INFO_CMD, body, p.UID))
	var info *proto.XiYouPlayerDataMsg
	select {
	case <-p.ctx.Done():
		return
	case info = <-p.xiyou.infoSecCh:
	case <-time.After(xiyouSecretReplyWait):
	}
	if info == nil {
		return
	}
	js, err := protojson.Marshal(info)
	if err != nil {
		return
	}
	// Server-as-truth frontier (chủ lệnh 2026-11-05): cửa đánh = currentSecretId
	// của DATA_INFO vừa bắn — không tin `frontier`param capture từ loop (fold
	// trễ sau khi qua ải từng làm đốt vé vào ải cũ).
	if id := info.GetCurrentSecretId(); id != 0 {
		if id != frontier {
			log.Printf("[%s] [XiYou] ảo cảnh: frontier loop=%d nhưng server=%d — theo server", p.Username, frontier, id)
			frontier = id
			p.xiyou.secID.Store(id)
		}
	}
	// Never re-fight a won door: Start ăn vé khi win, nên ải cũ = đốt vé trắng.
	if frontier <= p.xiyou.secWonMax.Load() {
		log.Printf("[%s] [XiYou] ảo cảnh: frontier %d ≤ cửa đã thắng %d — fold trễ, chờ tick sau (không đốt vé ải cũ)",
			p.Username, frontier, p.xiyou.secWonMax.Load())
		return
	}
	row, err := xiyoujs.SecretRowOf(frontier)
	if err != nil {
		log.Printf("[%s] [XiYou] ảo cảnh: không có dòng secret %d (%v) — bỏ", p.Username, frontier, err)
		time.Sleep(xiyouSecretQuietSleep)
		return
	}
	// power gate (client CheckEnough… before Start)
	pwf, _ := strconv.ParseFloat(info.GetPower(), 64)
	if pwf < float64(row.VerifyPower) {
		if !p.xiyou.secParked.Swap(true) {
			p.forceNotify(fmt.Sprintf("Tây Du: Ảo Cảnh %d cần lực chiến ≥ %d (acc đang %s). Bot tạm dừng Ảo Cảnh tới khi lực chiến tăng.",
				frontier, row.VerifyPower, info.GetPower()))
		}
		p.xiyou.parkSec(frontier)
		return
	}
	if _, err := os.Stat("/tmp/xiyou_debug_on"); err == nil {
		_ = os.WriteFile(filepath.Join(os.TempDir(), fmt.Sprintf("xiyou_secret_role_%d.json", p.UID)), js, 0o600)
	}
	// feasibility gate (offline, owner law): 300 fabricated full runs before
	// spending any server energy. All-lose → notify + park (main line keeps
	// running — the stop-engine law is specific to the main-line boss gate).
	if xiyouFeasGateOn { // TẮT tạm thời (chủ lệnh 2026-11-05: host yếu) — bật lại = đổi const
		pwr := info.GetPower()
		if !(p.xiyou.secFeasID == frontier && p.xiyou.secFeasPow == pwr && p.xiyou.secFeasWin) {
			won, tested, err := p.xiyouSecretFeasibilityScan(js, row)
			switch {
			case err != nil:
				log.Printf("[%s] [XiYou] ảo cảnh %d: feasibility lỗi (%v) — bỏ gate, chạy luôn", p.Username, frontier, err)
			case won:
				p.xiyou.secFeasID, p.xiyou.secFeasPow, p.xiyou.secFeasWin = frontier, pwr, true
				log.Printf("[%s] [XiYou] ảo cảnh %d: feasibility OK (%d seed) — chạy thật", p.Username, frontier, tested)
			default:
				_ = tested // chủ lệnh 2026-11-05: notify chỉ nói "acc quá yếu", không nhắc seed/offline
				notif := fmt.Sprintf("Tây Du: acc quá yếu, không qua được Ảo Cảnh %d. Bot TẠM DỪNG Ảo Cảnh (main-line vẫn chạy) — tăng lực chiến rồi /start_taydu lại.",
					frontier)
				log.Printf("[%s] [XiYou] %s", p.Username, notif)
				p.forceNotify(notif)
				p.xiyou.parkSec(frontier)
				return
			}
		}
	}
	// rate = hệ số giá vé (Start ăn `rate` vé 105286 khi WIN — client
	// CheckEnoughPoupGetWay(105286, curRate)). Chủ lệnh 2026-11-05 (acc 6 vé
	// hết sau 3 ải vì autoSet.rate=2): LUÔN chạy rate=1 — tiết kiệm vé, mặc
	// acc có set rate cao trong client. Start/Select/Kill phải cùng rate.
	const rate = int32(1)
	high := info.GetAutoSet().GetStarSelect() != 0
	p.xiyouSecretRun(actID, frontier, rate, high, js)
}

// xiyouSecretFeasibilityScan replays FULL runs offline (fabricated doors from
// the row's own template pool, random seeds) — same mechanics as the live run
// minus the wire. ~1s/run; 300 runs ≈ 5 min worst case, same budget law as
// the boss scan. (won, tested, simErr).
func (p *Player) xiyouSecretFeasibilityScan(role json.RawMessage, row *xiyoujs.SecretRow) (bool, int, error) {
	pool := row.Templates
	if len(pool) == 0 {
		return false, 0, fmt.Errorf("secret %d: empty template pool", row.ID)
	}
	tl := row.Timelimit * 1000
	for i := 1; i <= xiyouFeasScanMax; i++ {
		select {
		case <-p.ctx.Done():
			return false, i - 1, nil
		default:
		}
		seed := rand.Int31n(1_000_000) + 1
		first := pool[rand.Intn(len(pool))]
		sid, err := xiyoujs.SecretCreate(role, first, seed, tl)
		if err != nil {
			return false, i, err
		}
		won := false
		selected := []int32{first}
		for room := 0; room < xiyouSecretMaxRooms; room++ {
			st, err := xiyoujs.SecretRunStep(sid)
			if err != nil {
				xiyoujs.SecretDestroy(sid)
				return false, i, err
			}
			if st.Status == "end" {
				won = st.GameStatus == 2
				break
			}
			if st.Status != "room" {
				break // frame cap = not a win
			}
			pick := pool[rand.Intn(len(pool))]
			if xiyouParseProgress(st.Progress)+xiyouSecretAdd(xiyoujs.StarOfTemplate(pick)) >= 99.999 {
				exp, err := xiyoujs.SecretExport(sid)
				if err != nil {
					break
				}
				completed := selected
				if len(completed) > 0 {
					completed = completed[:len(completed)-1] // minus current room (refreshProgress)
				}
				if err := xiyoujs.SecretRebuild(sid, exp, pool[0], pool[0], completed); err != nil {
					break
				}
			} else if err := xiyoujs.SecretEnterDoor(sid, pick); err != nil {
				break
			}
			selected = append(selected, pick)
		}
		xiyoujs.SecretDestroy(sid)
		if won {
			log.Printf("[%s] [XiYou] ảo cảnh %d: feasibility THẮNG offline ở ván thứ %d (seed %d)",
				p.Username, row.ID, i, seed)
			return true, i, nil
		}
		if i%50 == 0 {
			log.Printf("[%s] [XiYou] ảo cảnh %d: feasibility offline %d/%d ván — toàn bộ THUA",
				p.Username, row.ID, i, xiyouFeasScanMax)
		}
	}
	log.Printf("[%s] [XiYou] ảo cảnh %d: feasibility thua cả %d ván offline", p.Username, row.ID, xiyouFeasScanMax)
	return false, xiyouFeasScanMax, nil
}

// xiyouSecretRun drives one real frontier run: seed → Start → room loop
// (Step ⇄ Select) → boss rebuild → Kill on win. Returns won.
func (p *Player) xiyouSecretRun(actID, secretID, rate int32, high bool, role json.RawMessage) bool {
	row, err := xiyoujs.SecretRowOf(secretID)
	if err != nil {
		return false
	}
	// ── Start ──
	sr, _ := pb.Marshal(&proto.XiYouSecretStartReq{ActivityId: actID, SecretId: secretID, Rate: rate})
	drainCh(p.xiyou.startCh)
	p.write(pack.Pack(XIYOU_SECRET_START_CMD, sr, p.UID))
	var startResp *proto.XiYouSecretStartResp
	select {
	case <-p.ctx.Done():
		return false
	case startResp = <-p.xiyou.startCh:
	case <-time.After(xiyouSecretReplyWait):
	}
	if startResp == nil {
		log.Printf("[%s] [XiYou] ảo cảnh %d: Start im lặng — thử lại sau", p.Username, secretID)
		return false
	}
	if startResp.GetRet() == 101 {
		// Hết vé 105286 (chủ lệnh 2026-11-05): DỪNG Ảo Cảnh hẳn, không đánh
		// nữa — chỉ /start_taydu (nạp vé xong) mới chạy lại.
		log.Printf("[%s] [XiYou] ảo cảnh %d: Start ret=101 — hết vé 105286, DỪNG Ảo Cảnh", p.Username, secretID)
		p.forceNotify("Tây Du Ảo Cảnh: acc hết vé — bot DỪNG Ảo Cảnh (main-line vẫn chạy). Nạp vé xong /start_taydu lại để đánh tiếp.")
		p.xiyou.secStopped.Store(true)
		return false
	}
	if startResp.GetRet() != 0 {
		// Server-arbitrated gate answer — back off a full minute, no hammering.
		log.Printf("[%s] [XiYou] ảo cảnh %d: Start ret=%d (rate / gate?) — nghỉ 60s", p.Username, secretID, startResp.GetRet())
		time.Sleep(xiyouSecretQuietSleep)
		return false
	}
	doorIds := startResp.GetDoorIds()
	selected := startResp.GetSelectedDoorsIds()
	tpl := int32(0)
	if n := len(selected); n > 0 {
		tpl = selected[n-1] // client: last selectedDoorsIds = current room template
	} else if len(doorIds) > 0 {
		tpl = doorIds[0]
	}
	if tpl == 0 {
		log.Printf("[%s] [XiYou] ảo cảnh %d: Start không có cửa — thử lại sau", p.Username, secretID)
		return false
	}
	// Client order: msg_secretStart OK → msg_reqSeed → combat init with that
	// seed (index.pretty.js:16605ff). Grab it AFTER Start so the server's
	// run-bound seed (if any) is the one the sim replays with.
	// ĐỘC QUYỀN seed window tới hết Kill (fix "sim đúng nhưng server không
	// cho qua", Kill ret=31170): server chỉ giữ MỘT current seed — nếu
	// main-line farm xin seed giữa ván ta, levels claim (tính từ seed của
	// ta) không khớp server replay nữa. Giữ seedMu từ đây tới lúc Kill xong.
	p.xiyou.seedMu.Lock()
	defer p.xiyou.seedMu.Unlock()
	seed, ok := p.xiyouGetSeedLocked(actID)
	if !ok {
		log.Printf("[%s] [XiYou] ảo cảnh %d: không lấy được seed — thử lại sau", p.Username, secretID)
		return false
	}
	sid, err := xiyoujs.SecretCreate(role, tpl, seed, row.Timelimit*1000)
	if err != nil {
		log.Printf("[%s] [XiYou] ảo cảnh %d: sim create lỗi: %v", p.Username, secretID, err)
		return false
	}
	defer xiyoujs.SecretDestroy(sid)
	fightData := ""
	for room := 0; room < xiyouSecretMaxRooms; room++ {
		select {
		case <-p.ctx.Done():
			return false
		default:
		}
		st, err := xiyoujs.SecretRunStep(sid)
		if err != nil {
			log.Printf("[%s] [XiYou] ảo cảnh %d: sim step lỗi: %v", p.Username, secretID, err)
			return false
		}
		switch st.Status {
		case "end":
			if st.GameStatus != 2 {
				// THUA ĐẤU (1=RoleDie, 3=TimeOut theo IXiYouCombatResult) —
				// KHÔNG park: chủ lệnh 2026-11-09 "thua thì phải đánh mãi".
				// Vé chỉ bị Start ăn khi WIN; loop tự xin seed mới bắn lại.
				// %HP = máu boss/còn lại của phòng đánh dở (boss room = đúng).
				log.Printf("[%s] [XiYou] ảo cảnh %d: THUA (kết quả %d, %d phòng, %dms, seed %d%s) — đánh tiếp",
					p.Username, secretID, st.GameStatus, room+1, st.Ms, seed,
					xiyouEnemyHpTxt(st.EnemyHpLeft, st.EnemyHpMax))
				return false
			}
			if fightData == "" {
				// boss died without the rebuild capture — server would reject;
				// treat as loss and let parity get fixed (must not happen).
				log.Printf("[%s] [XiYou] ảo cảnh %d: boss chết NHƯNG không có fightData — lỗi orchestration", p.Username, secretID)
				return false
			}
			kr := &proto.XiYouSecretKillReq{
				ActivityId: actID, SecretId: secretID, Rate: rate,
				CodeVersion: xiyouCodeVersion, FightData: fightData, Levels: st.Levels,
				// useLuckItemIds: LUÔN trống — item 105288/89/90 bị cấm (law).
			}
			kb, _ := pb.Marshal(kr)
			drainCh(p.xiyou.killCh)
			p.write(pack.Pack(XIYOU_SECRET_KILL_CMD, kb, p.UID))
			var killResp *proto.XiYouSecretKillResp
			select {
			case <-p.ctx.Done():
				return false
			case killResp = <-p.xiyou.killCh:
			case <-time.After(xiyouSecretReplyWait):
			}
			if killResp == nil || killResp.GetRet() != 0 {
				ret := int32(-1)
				if killResp != nil {
					ret = killResp.GetRet()
				}
				// Kill bị server REJECT (không phải thua đấu) — vẫn KHÔNG
				// park (chủ lệnh 2026-11-09 "thua thì phải đánh mãi") nhưng
				// nghỉ 60s chống spam: mỗi lần thử lại là seed + fightData
				// MỚI nên có cơ hội khác, chỉ cần không bắn dồn.
				log.Printf("[%s] [XiYou] ảo cảnh %d: sim WIN nhưng Kill ret=%d (fightData/levels lệch?) — nghỉ 60s rồi đánh tiếp", p.Username, secretID, ret)
				time.Sleep(xiyouSecretQuietSleep)
				return false
			}
			log.Printf("[%s] [XiYou] ảo cảnh %d: THẮNG (%d phòng, %dms, seed %d) reward=%s → secret hiện tại %d",
				p.Username, secretID, room+1, st.Ms, seed, killResp.GetRewards(), killResp.GetCurrentSecretId())
			// high-water mark: cửa đã thắng này không bao giờ được Start lại
			// (chủ lệnh 2026-11-05: tuyệt đối không đốt vé vào ải cũ).
			if secretID > p.xiyou.secWonMax.Load() {
				p.xiyou.secWonMax.Store(secretID)
			}
			p.forceNotify(fmt.Sprintf("Tây Du Ảo Cảnh: qua cửa %d (%d phòng) — reward %s", secretID, room+1, killResp.GetRewards()))
			return true
		case "frames":
			// chạm trần frame = coi như thua đấu — không park (đánh mãi)
			log.Printf("[%s] [XiYou] ảo cảnh %d: chạm trần frame (room %d%s) — đánh tiếp",
				p.Username, secretID, room+1, xiyouEnemyHpTxt(st.EnemyHpLeft, st.EnemyHpMax))
			return false
		}
		// room cleared → next door
		if len(doorIds) == 0 {
			log.Printf("[%s] [XiYou] ảo cảnh %d: server hết cửa (doorIds rỗng) — dừng ván", p.Username, secretID)
			return false
		}
		pick := xiyouPickDoor(doorIds, high)
		boundary := xiyouParseProgress(st.Progress)+xiyouSecretAdd(xiyoujs.StarOfTemplate(pick)) >= 99.999
		// EVERY door — the boss-boundary room included — goes through Select.
		// The server keeps its own selectedDoorsIds and recomputes progress on
		// Kill; the client sends Select for the boundary room too (onDoorClick
		// waterfall) and only the boss-door push (doorIds[0] of the BOUNDARY
		// room's list) stays local (onSecretCallBoss). Skipping the boundary
		// Select left server progress one room short → Kill ret=31140.
		sq, _ := pb.Marshal(&proto.XiYouSecretSelectReq{ActivityId: actID, SecretId: secretID, Rate: rate, DoorId: pick})
		drainCh(p.xiyou.selectCh)
		p.write(pack.Pack(XIYOU_SECRET_SELECT_CMD, sq, p.UID))
		var sel *proto.XiYouSecretSelectResp
		select {
		case <-p.ctx.Done():
			return false
		case sel = <-p.xiyou.selectCh:
		case <-time.After(xiyouSecretReplyWait):
		}
		if sel == nil {
			log.Printf("[%s] [XiYou] ảo cảnh %d: Select im lặng — dừng ván", p.Username, secretID)
			return false
		}
		if sel.GetRet() != 0 {
			log.Printf("[%s] [XiYou] ảo cảnh %d: Select cửa %d ret=%d — dừng ván, thử lại sau", p.Username, secretID, pick, sel.GetRet())
			return false
		}
		doorIds = sel.GetDoorIds()
		selected = sel.GetSelectedDoorsIds()
		if boundary {
			// Client try2SavePlayerData: export the just-cleared combat BEFORE
			// entering the boundary room; boss template = boundary room's
			// doorIds[0] (consumed locally, never Selected). Rebuild continues
			// that export with curFightTempId = pick; refreshProgress sums
			// selected minus the last (= pick), so killing the boundary room's
			// waves lands progress on 100 → boss summon.
			if len(doorIds) == 0 {
				log.Printf("[%s] [XiYou] ảo cảnh %d: phòng boss không có cửa — dừng ván", p.Username, secretID)
				return false
			}
			exp, err := xiyoujs.SecretExport(sid)
			if err != nil {
				log.Printf("[%s] [XiYou] ảo cảnh %d: export lỗi: %v", p.Username, secretID, err)
				return false
			}
			completed := selected
			if n := len(completed); n > 0 {
				completed = completed[:n-1] // drop pick — refreshProgress excludes it
			}
			if err := xiyoujs.SecretRebuild(sid, exp, pick, doorIds[0], completed); err != nil {
				log.Printf("[%s] [XiYou] ảo cảnh %d: rebuild lỗi: %v", p.Username, secretID, err)
				return false
			}
			fightData = exp
		} else {
			if err := xiyoujs.SecretEnterDoor(sid, pick); err != nil {
				log.Printf("[%s] [XiYou] ảo cảnh %d: enter door lỗi: %v", p.Username, secretID, err)
				return false
			}
		}
	}
	log.Printf("[%s] [XiYou] ảo cảnh %d: quá %d phòng chưa xong — dừng ván", p.Username, secretID, xiyouSecretMaxRooms)
	return false
}
