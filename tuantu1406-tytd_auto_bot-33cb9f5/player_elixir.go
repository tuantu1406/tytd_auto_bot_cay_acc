package tytd_go

/*
Đan Giới / Linh Điền (Elixir 235502..) — the plot system the old code half-
knew as "autoDanGioi". Ground truth: ../remoteElixir/Elixir.decoded.proto +
RemoteElixirMsgInfo registry, owner gameplay audit 2026-10-16 + live probes:

  • plots SELF-GROW on a server timer; the WATER request is DEAD by protocol
    — the server's own refusal text 35568 reads "chức năng tưới nước đã bị
    gỡ bỏ, gieo hạt sẽ tự động sản xuất" (watering removed; sowing
    auto-produces). The loop therefore never waters.
  • DƯỢC LINH (药灵/Elves) only roll if the plant carried isPlantElves=true —
    the "thu hút dược linh" tick; server caps 30/day itself (8ade
    elvesPlayerData.todayGetCount, read-only). The cap gates the SERVER's
    rolls only — since 2026-10-22 the loop plants by the task board
    regardless of the counter (no local cap switch).
  • only herbal grass (id 864xxx) rolls Dược Linh at all; the BEST tree is
    the season bonus tree: server table ELIXIR_PERI_GRASS_BONUS (login
    config frame 0010, captured live) is arithmetic — season N →
    864001+3*(N-1) — and the season number rides the passive push 8ae9
    (235561) at login (live: season 8 → 864022 Chiếu Mệnh Huyết San Hô).
    AVAILABLE-SEED LAW (owner 2026-10-21, live PROVEN): a tree is plantable
    iff its ROW EXISTS in grassInfos — the picker on an empty lot lists every
    row and num is NOT consumed (planting is free), so a `×0` row plants just
    the same; NO row at all = never unlocked = no-op. A tree demanded by the
    Dược Tiên task board always has its row (owner: "cây hiện ở bảng nhiệm vụ
    dược tiên thì chắc chắn mày có hạt và có thể trồng").
  • OWNER FLOW 2026-10-22 ("giờ chỉ làm chứ không nhận"), PURE EVENT-DRIVEN
    2026-10-23 (owner: "player.go đã gọi handler của đan giới rồi sao trong
    danGioiTickOnce còn handler khác — chuyển về thuần event-driven cho tao"):
    the TICK is dumb — every minute it BLIND-FIRES ENTER (235502) and waits
    for the handler's pass event; the FIRST tick right after login is what
    folds bag rows into the player vars ("gửi request để lấy ngay sau khi
    login … update vào 1 biến trong struct player"). AUTO-DƯỢC-LINH IS GONE
    (owner same day, latest word: "bỏ auto dược linh đi, khỏi phải check số
    lượng dược linh với cây dược linh") — no 235550 pull, no elves counter,
    no bestElf preference anywhere in the loop. The 8aae HANDLER is the
    brain: folds garden/bag; the pass sleeps 1s then runs loop #1 HÁI
    (per-ruộng GET, records landId→grassId — NO cuốc: "giờ không làm nhiệm vụ
    nữa nên không cuốc nữa") and loop #2 trồng: ô có cây → bỏ qua; ô vừa hái
    ở đây → trồng cây cũ trùng id đã thu hoạch ở ô đó; ô trống từ đầu (hoặc
    cây cũ bị server no-op) → random cây trong túi, mỗi cây bị no-op chỉ tốn
    1 packet cả pass (elixirPlantLoop). Task-board planting is OUT of the
    loop ("bỏ qua chỗ làm nhiệm vụ"). NO claims (20503), NO order
    submissions, NO husk sweep, and NO 235537 (铲除) ever leaves the engine
    ("8 chỉ trồng không cuốc bỏ nữa"): single-round plants auto-empty on the
    GET and get replanted same-pass; multi-round trees stay standing and
    re-ripen on the server's own round timer. The server stays the progress
    arbiter; a refused no-op plant is corrected by the next 8aae.
  • oneKey=true answers 35520 = "Không có Thẻ Đặc Quyền" on this server
    (privilege-card gated!) — the OLD bot's per-land harvest/replant worked
    (23 received rounds on the probe acc), so every send here is EXPLICIT:
    landId lists only, never oneKey.

v1's blind "GET oneKey → WATER → PLANT oneKey" cycle is gone: the engine
below ENTERs, harvests the ripe lots by id, and replants empty ground with
the best pick, all with the Elves tick. The permanent item ban holds: NO
speed-item (235507), no eat, no rob, no sell. The DEL (铲除 235537) — once
an owner-authorised free op — is NOT sent by the engine anymore either
(owner 2026-10-23: "không cuốc nữa"); elixirDelLots survives only for the
gated live-forensic tests [[tytd-item-ban]].
[[tytd-dangioi-elixir]]
*/

import (
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/pack"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

const (
	// elixirLoopGap: owner 2026-10-17: poll every minute. Each wake costs
	// one cheap ENTER (plus the per-lot hái frames when something is ripe)
	// and only then acts on what the field says; a 1-minute beat is
	// what catches ripe windows fast enough to keep the field productive.
	elixirLoopGap    = 1 * time.Minute
	elixirReqTimeout = 10 * time.Second
	// elixirClaimAnswerGap: how long one 20503 waits for its 01f7 verdict
	// inside elixirClaimTaskRewards. Generous enough for LAN lag, short
	// enough that a row of dead rows (ret=9 flood) costs seconds, not the
	// full round-trip timeout each.
	elixirClaimAnswerGap = 3 * time.Second
	// elixirBatch: lots per request — ONE (owner 2026-10-22: "mấy thằng
	// không có vip nó có thể bị chặn gate trồng batch, nên làm cái loop mà
	// hái, cuốc, trồng từng ruộng đi"). Multi-landId GET/DEL/PLANT packets
	// are privilege-flavoured (oneKey is privilege-card-gated and the
	// server may silently no-op a batch PLANT for a no-VIP acc — ret=0 with
	// an empty echo, which the echo-verify correctly reads as refusal, so a
	// batch gate once starved the WHOLE sweep of 6 lots on those accounts).
	// Every elixir operation now rides the per-lot pipeline: hái ruộng →
	// trồng ruộng, each its own packet, exactly what the client emits when a
	// player taps one plot (cuốc dropped 2026-10-23).
	elixirBatch = 1
)

// elixirRoundTrip reuses the generic sky-trade seat plumbing (same as RBT).
func (p *Player) elixirRoundTrip(cmd, wantType string, body []byte) ([]byte, error) {
	return p.stRoundTrip(cmd, wantType, body, elixirReqTimeout)
}

// elixirSigsGet lazily makes (once) and returns the two handler event
// channels. Created under ElixirGardenMu so the handler's fold (which locks
// the same mutex) always observes them before poking.
func (p *Player) elixirSigsGet() (garden, pass chan struct{}) {
	p.ElixirGardenMu.Lock()
	defer p.ElixirGardenMu.Unlock()
	if p.elixirGardenSig == nil {
		p.elixirGardenSig = make(chan struct{}, 1)
	}
	if p.elixirPassSig == nil {
		p.elixirPassSig = make(chan struct{}, 1)
	}
	return p.elixirGardenSig, p.elixirPassSig
}

// elixirFire sends one packet blind — the tick never waits for answers;
// handlers fold and act (pure event-driven).
func (p *Player) elixirFire(cmd string, body []byte) error {
	select {
	case p.writeChan <- pack.Pack(cmd, body, p.UID):
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}

// elixirEnterHome asks for the whole field and returns the handler's folded
// answer — PURE EVENT-DRIVEN (owner 2026-10-23): no stRoundTrip seat for
// ENTER anywhere; elixirDeliver is the ONE receiver of 8aae. Fire blind,
// wait for the fold event, read ElixirHome. Also used by the live tests'
// audits and /dangioi. The ENTER itself triggers a garden pass in the
// handler (owner spec: every 8aae runs the two loops) — that pass is
// serialised by elixirPassMu and never blocks this return.
func (p *Player) elixirEnterHome() (*proto.ElixirEnterHomeResp, error) {
	garden, _ := p.elixirSigsGet()
	select {
	case <-garden: // drain stale fold
	default:
	}
	body, err := pb.Marshal(&proto.ElixirEnterHomeReq{PlayerId: pb.Int64(int64(p.UID))})
	if err != nil {
		return nil, err
	}
	if err := p.elixirFire(S_ELIXIR_ENTER, body); err != nil {
		return nil, err
	}
	select {
	case <-garden:
		p.ElixirGardenMu.Lock()
		rsp := p.ElixirHome
		p.ElixirGardenMu.Unlock()
		if rsp == nil { // only the handler pokes, and it pokes after folding — unreachable
			return nil, fmt.Errorf("signal không kèm vườn (không đáng xảy ra)")
		}
		return rsp, nil
	case <-p.ctx.Done():
		return nil, p.ctx.Err()
	case <-time.After(elixirReqTimeout):
		return nil, fmt.Errorf("ENTER không có hồi âm sau %v (server limit? xem log [DanGioi] ret=… — vd client game đang mở vườn cùng lúc)", elixirReqTimeout)
	}
}

// elixirGoldenGrass is the season bonus tree. Server table
// ELIXIR_PERI_GRASS_BONUS (captured live in the login config push, frame
// 0010, 2026-10-16) is an arithmetic sequence: season N → 864001+3*(N-1)
// (season 8 → 864022 Chiếu Mệnh Huyết San Hô — matched against the baked
// table). The season itself rides the passive push 8ae9 (235561) at login.
// elixirMaxSeason: the 864xxx family spans 864000..864999, three ids per
// season → (864999−864001)/3+1 = 333 seasons fit the arithmetic. The old
// hard cap of 10 silently zeroed the golden from season 11 on (owner
// 2026-10-21 "không detect được dược linh mùa mới").
const elixirMaxSeason = 333

func elixirGoldenGrass(season int32) int32 {
	if season < 1 || season > elixirMaxSeason {
		return 0
	}
	return 864001 + 3*(season-1)
}

// elixirPredictSeason is the pure core of the stale-season self-heal: given
// the season number and its window (UNIX MILLIS, as 8ae9 carries them),
// return the season that should be LIVE at nowMillis. Inside the window (or
// with no usable window) it returns `season` untouched; past the end it
// advances by whole elapsed periods — the server kept a rigid 15-day
// cadence in every live capture. A fresh 8ae9 push overwrites the folded
// numbers and re-bases everything, so a wrong guess self-corrects.
func elixirPredictSeason(season int32, start, end, nowMillis int64) int32 {
	if season < 1 || start <= 0 || end <= start || nowMillis <= end {
		return season
	}
	p := season + int32((nowMillis-start)/(end-start))
	if p > elixirMaxSeason {
		p = elixirMaxSeason
	}
	return p
}

// elixirCurSeason is the ONLY read path for the season the loop plans with.
// It folds the passive 8ae9 value and self-heals when the window has passed
// without a push (long-running session across a rollover), logging the
// jump once per predicted season.
func (p *Player) elixirCurSeason() int32 {
	p.ElixirMu.Lock()
	defer p.ElixirMu.Unlock()
	pred := elixirPredictSeason(p.ElixirSeason, p.ElixirSeasonStart, p.ElixirSeasonEnd, time.Now().UnixMilli())
	return pred
}

// elixirSeasonTrio returns the season's x2-value trio. Owner rule (live
// 2026-10-16): every season doubles THREE trees — ELIXIR_PERI_BONUS lists
// the three Dược Linh ids (e.g. season 8 → 910022|910023|910024), which map
// 1:1 onto trees 864022|864023|864024 = {G, G+1, G+2} for G=golden.
func elixirSeasonTrio(season int32) []int32 {
	g := elixirGoldenGrass(season)
	if g == 0 {
		return nil
	}
	return []int32{g, g + 1, g + 2}
}

// elixirHerbNames maps the Dược Linh-capable tree family (grassId 864xxx ↔
// Dược Linh item 91xxx+864xxx−864000; live-proven: the game's own item text
// pairs each 91xxxx "Dược Linh" with "Sau khi trồng <864 tree>…"). Names
// from the shipped vi language table; ids outside this set are ordinary
// grass — they NEVER roll Dược Linh no matter the tick.
var elixirHerbNames = map[int32]string{
	864001: "Hoa Bỉ Ngạn Vong Xuyên",
	864002: "Cát Cánh Tử Vân",
	864003: "Tịnh Thế Liên",
	864004: "Thất Đàm Huyễn Linh Hoa",
	864005: "Sương Nhứ Linh Thảo",
	864006: "Bích Thiên La",
	864007: "Cửu Muội Thiên Hỏa Liên",
	864008: "Huyền Minh Hàn Hoa",
	864009: "Thanh Vũ La",
	864010: "Tân Nguyệt Ngọc U Lan",
	864011: "Bạch Linh Tiên Cúc",
	864012: "Huỳnh Quang Lung",
	864013: "Hỏa Đức Đạo Tâm Quả",
	864014: "Bích Tuyền Ngọc Lan",
	864015: "Thanh Vân Hoa",
	864016: "Huyền Hi Tiên Linh Chi",
	864017: "Cửu U Ảnh Đằng",
	864018: "Ngự Phong Đằng",
	864019: "Phù Đồ Mật Tháp Thảo",
	864020: "Minh Tiêu Lôi Giáp",
	864021: "Xuân Nhật Hướng Dương",
	864022: "Chiếu Mệnh Huyết San Hô",
	864023: "Tinh Hài Ký Sinh Đằng",
	864024: "Huyền Cốt Sâm Vương",
}

// elixirIsHerb reports whether grassId can roll Dược Linh (the whole 864xxx
// family — includes the untranslated 864025.. entries, so future trees still
// count). Every other family (860/861/862/863/865…) is ordinary grass.
func elixirIsHerb(grassId int32) bool { return grassId >= 864000 && grassId <= 864999 }

// elixirHerbName returns the Vietnamese display name for a herbal grassId.
func elixirHerbName(id int32) string {
	if n, ok := elixirHerbNames[id]; ok {
		return n
	}
	return fmt.Sprintf("cây %d", id)
}

// The four ordinary trees the daily harvest tasks (cType 496) ask for —
// names from the shipped vi table (ElixirHerb_Name_* covers every grass
// family, herbal or not). Only these ever show up in a plant order.
var elixirTaskGrassNames = map[int32]string{
	860001: "Bạch Linh Chỉ",
	860002: "Huyền Hoàng Cúc",
	860003: "Thanh Nguyên Thảo",
	860004: "Xích Càn Quả",
}

// elixirGrassName names any tree for display: herb table first, task-tree
// table second, id fallback last.
func elixirGrassName(id int32) string {
	if elixirIsHerb(id) {
		return elixirHerbName(id)
	}
	if n, ok := elixirTaskGrassNames[id]; ok {
		return n
	}
	return fmt.Sprintf("cây %d", id)
}

// elixirGrassQuality is a herb tree's intrinsic "độ xịn" (owner: 4 điểm =
// quality 5, 2 điểm = quality 4 — the UI điểm scale tracks this column).
// Mined 2026-10-18 from the live ElixirHerbDB config push (864xxx rows are
// NOT in the CDN table — they only ride the login frame): the family cycles
// quality 6,5,4 per trio {G, G+1, G+2} — the season-golden id
// (864001+3k) is ALWAYS quality 6 (attr 1001=150), +1 is 5 (attr 80), +2 is
// 4 (attr 40). So inside a trio the golden tree is the best, and older
// season trees keep their own quality forever.
func elixirGrassQuality(id int32) int32 {
	if !elixirIsHerb(id) {
		return 0
	}
	switch (id - 864001) % 3 {
	case 0:
		return 6
	case 1:
		return 5
	default:
		return 4
	}
}

// elixirPlantTiers ranks the candidate trees for free slots, best first
// (owner rules 2026-10-16, held-gate 2026-10-17, quality ranking 2026-10-18
// after the owner caught 864021 "Xuân Nhật Hướng Dương" (q4, 2đ) being
// planted over the held 864020 "Minh Tiêu Lôi Giáp" (q5, 4đ) — the old
// ladder sorted by id DESC, which is meaningless):
//   - ONLY trees the account has UNLOCKED ever enter the ladder — unlocked =
//     ROW PRESENT in grassInfos, count irrelevant (available-seed law, owner
//     2026-10-21: "click ô đất trống game show hết loại hạt available";
//     live PROVEN TestDanGioiSeedPlantLive: `864024×0` PLANTed and grew,
//     state=2 grass=864024). The OLD reading of this gate was num>0 — which
//     mis-firewalled accounts whose only herbal row carried ×0 (<live-user>
//     2026-10-21: engine saw an empty herb bag while the picker showed the
//     seed). What remains true from 2026-10-17
//     (TestDanGioiUnheldPlantLive): a tree with NO row at all is never
//     unlocked and its PLANT answers ret=0 with an EMPTY land echo (silent
//     no-op) — "Không có hạt nhưng vẫn trồng" was the bot firing junk
//     packets and (via the ret==0-only check) believing them. Owner policy
//     stands: acc with NO herbal row = harvest-only, lots stay empty.
//   - score = elixirGrassQuality × (2 if the tree is in THIS season's trio,
//     else 1) — owner 2026-10-18: "chỉ có cây mùa này được tính x2, còn các
//     cây mua trước thì không x2 nhưng vẫn tính theo độ xịn của nó". Any
//     held trio tree therefore outranks any ordinary herb (min trio score 8
//     > max ordinary 6); inside the trio the golden (q6) leads, then q5,
//     q4; ordinary herbs rank 6 > 5 > 4 among themselves.
//   - planting costs NOTHING (grassInfos num = trees held in the bag, not
//     seeds — "trồng không cần hạt", live-confirmed: 860003 num 60→60 across
//     a successful plant); the ladder still matters because an account may
//     hold only lower-score trees — the caller tries the best HELD score
//     and on a refused packet falls one tier down;
//   - growing[0] (like-for-like of whatever this lot had) as the floor, and
//     ordinary grass NEVER (owner: only the ngon nhất gets planted).
func elixirPlantTiers(seasonsTrees map[int32]bool, season int32, growing []int32) []int32 {
	inTrio := map[int32]bool{}
	for _, t := range elixirSeasonTrio(season) {
		inTrio[t] = true
	}
	type cand struct {
		id    int32
		score int32
	}
	var cands []cand
	for id := range seasonsTrees {
		if !elixirIsHerb(id) {
			continue
		}
		mult := int32(1)
		if inTrio[id] {
			mult = 2
		}
		cands = append(cands, cand{id, elixirGrassQuality(id) * mult})
	}
	// Best score first. Equal scores (two q5 trees from different seasons —
	// e.g. 864017 Cửu U Ảnh Đằng vs the owner's preferred 864020 Minh Tiêu
	// Lôi Giáp, whose rows are IDENTICAL in ElixirHerbDB except id/essence)
	// tie-break NEWER id first: the newest same-quality tree is what the
	// owner calls "ngon nhất" (2026-10-18). NOTE: id only ever BREAKS ties —
	// the old ladder sorted by id outright, which is how 2đ 864021 beat 4đ
	// 864020.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].id > cands[j].id
	})
	out := []int32{}
	for _, c := range cands {
		out = append(out, c.id)
	}
	for _, id := range growing {
		// Like-for-like floor, but only trees with a grassInfos row — a
		// rowless growing tree is a leftover, replanting it no-ops anyway.
		if elixirIsHerb(id) && seasonsTrees[id] && !containsInt32(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func containsInt32(v []int32, x int32) bool {
	for _, y := range v {
		if y == x {
			return true
		}
	}
	return false
}

// elixirElvesEnter pulls the read-only Dược Linh panel — todayGetCount is
// the thu-hôm-nay/cap-30 counter the game UI shows (235550→8ade).
func (p *Player) elixirElvesEnter() (*proto.ElixirElvesPlayerData, error) {
	data, err := p.elixirRoundTrip(S_ELIXIR_ELVES_ENTER, SERVER_ELIXIR_ELVES_INFO, []byte{})
	if err != nil {
		return nil, err
	}
	var rsp proto.ElixirElvesEnterResp
	if err := pb.Unmarshal(data, &rsp); err != nil {
		return nil, fmt.Errorf("giải mã 8ade: %w", err)
	}
	return rsp.GetElvesPlayerData(), nil
}

// elixirHarvest is loop #1 of the pass — HÁI THÔI, không cuốc (owner
// 2026-10-23, verbatim: "giờ không làm nhiệm vụ nữa nên không cuốc nữa").
// ONE loop over the field, PER LOT: ripe lot (canAward>0) → GET; unripe and
// empty lots continue. NO 235537 (铲除) is ever sent anymore — the DEL call
// that used to sit behind every accepted GET is GONE with the task work.
//
// Why no-cuốc is HEALTHY, not a regression (live-proven mechanics):
//   - a SINGLE-ROUND plant AUTO-EMPTIES on the GET itself (lot → state=0,
//     live 2026-10-19) → the plant loop finds it free and replants;
//   - a MULTI-ROUND tree stays standing after the GET (state=2, hadRecv+1)
//     and re-ripens on the server's own round timer → the plant loop's
//     "ô đang có cây thì bỏ qua" correctly leaves it to keep producing.
//     Cuốc'd it, we were KILLING those producers to make room for task
//     trees that we no longer do.
//
// Per-lot packets (elixirBatch=1, no-VIP gate). Returns the lots whose GET
// the server ACCEPTED — as a map landId → grassId (the "cây cũ trùng id đã
// thu hoạch ở ô đó" the plant loop replants; owner 2026-10-23).
func (p *Player) elixirHarvest(rsp *proto.ElixirEnterHomeResp) (harvested map[int32]int32) {
	harvested = map[int32]int32{}
	for _, l := range rsp.GetLands() {
		if l.GetCanAwardRound() <= 0 {
			continue // cây chưa chín hoặc ruộng trống → bỏ qua
		}
		id := l.GetLandId()
		gb, _ := pb.Marshal(&proto.ElixirGetGrassReq{LandId: []int32{id}})
		data, err := p.elixirRoundTrip(S_ELIXIR_GET_GRASS, SERVER_ELIXIR_GET, gb)
		if err != nil {
			continue
		}
		// PARSE the answer — do not byte-sniff for "08 00": `ret` is proto3
		// OPTIONAL, so a SUCCESSFUL GET legitimately arrives with the field
		// OMITTED (first byte 0x12 = land, no ret at all). GetRet()==0
		// covers missing==0 (owner 2026-10-20 "vẫn không cuốc sau mỗi lần
		// thu hoạch" was exactly this byte-sniff starve).
		var gr proto.ElixirGetGrassResp
		if pb.Unmarshal(data, &gr) != nil || gr.GetRet() != 0 {
			continue
		}
		harvested[id] = l.GetGrassId() // chỉ HÁI — không cuốc nữa
	}
	return harvested
}

// elixirClaimTaskRewards batch-claims every Đan Giới task (51xxxx/52xxxx)
// whose SERVER value reached the static target, regardless of state.
//
// NOTE 2026-10-22 (owner "giờ chỉ làm chứ không nhận"): danGioiTickOnce no
// longer calls this — the AUTO loop plants/harvests only and never receives
// rewards. The pass stays compiled (tests pin the protocol) and stays ready
// for a manual path.
//
// OWNER-CORRECTED 2026-10-17 (the bug "chưa thấy tự nhận thưởng nhiệm vụ ở
// dược tiên ghé thăm"): completion on THIS board is the client's bag check —
// the server only holds the milestone (target), and state flips to 1 the
// moment the row is DONE, not when paid (live proof: every one of ~40 rows
// sits at exactly state=1 ⇔ value>=target — impossible if state meant
// claimed; and the bot's old gate state!=1 therefore never claimed ANYTHING,
// while ids like 530005–07 it never even knew carried value>=target &
// state=1 with nobody having claimed them). The union-task idiom's
// state!=1 requirement was wrong here and silently starved the whole board.
// The server stays the arbiter: a claim on an already-paid row answers
// ret=9 "Đã nhận quà nhiệm vụ" — harmless. To avoid re-firing that every
// minute, one attempt per (id,value) is remembered; any value change (more
// progress, daily reset) re-arms the row. Known Đan-Giới id + no unresolved
// reward pick remain hard gates.
//
// OWNER-CORRECTED 2026-10-18 (bug "sao mày báo nhận 14 quà nhiệm vụ, t vào
// thấy có vài nhiệm vụ thôi"): the old return counted every 20503 SENT —
// done rows the owner had ALREADY claimed in-game answer ret=9 and are not
// new gifts, yet the notify shouted "nhận 14 quà". Claims are now seated
// one-by-one (20503 → 01f7 via stDeliver, pump reader-side for ordering)
// and the return lists ONLY ids whose answer says ret==0 — the honest
// "quà thực nhận" count. ret=9/10/timeout rows keep the ledger mark (no
// minute-respam) but never count as received. Two consecutive silences =
// the connection stopped answering → bail early with what was confirmed.
func (p *Player) elixirClaimTaskRewards() []int32 {
	// Read BEFORE TaskMu (elixirOrderLadderCount locks it — nested = deadlock).
	ladder := p.elixirOrderLadderCount()
	p.TaskMu.Lock()
	var ids []int32
	if p.taskSynced {
		for id, e := range p.tasks {
			row, ok := elixirTaskStatic[id]
			if !ok || e.hadSelect {
				continue
			}
			if row.cType == 506 && (ladder >= elixirOrderAutoCap || row.target > elixirOrderAutoCap) {
				// Owner 2026-10-21 (cap chốt ở 5): "sau khi đến max, không
				// nhận thưởng nhiệm vụ nữa" — ladder đã ≥ 5 thì MỌI bậc ct506
				// nghỉ (kể cả bậc target ≤ 5, value giờ luôn ≥ 5). Dưới max
				// chỉ bậc ≤ 5 được nhận; 10/15/20/25/30…200 là của chủ.
				continue
			}
			value, err := strconv.ParseInt(e.data.GetValue(), 10, 64)
			if err != nil {
				continue // unreadable progress: the server stays the arbiter
			}
			if value < row.target {
				continue
			}
			if p.elixirClaimTried != nil && p.elixirClaimTried[id] == e.data.GetValue() {
				continue // already fired for this exact progress value
			}
			if p.elixirClaimTried == nil {
				p.elixirClaimTried = map[int32]string{}
			}
			p.elixirClaimTried[id] = e.data.GetValue()
			ids = append(ids, id)
		}
	}
	p.TaskMu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	// ONE request per id: 20503 is atomic — a single already-paid row in a
	// batch answers one ret=9 and silently poisons every other row in it
	// (live batch experiment: 13 ids → one ret=9, nothing provable per-id).
	// Per-id, ret=9 only ever touches its own row, and the ledger keeps the
	// retry cost at one frame per (id,value) for the session.
	ch, serr := p.stClaim(SERVER_TASK_GET_REWARD)
	if serr != nil {
		// seat busy (foreign flow owns 01f7): fire blind as before but
		// report NOTHING — nobody heard the verdicts.
		for _, id := range ids {
			reqMSG, _ := pb.Marshal(&proto.TaskGetRewardReqMsg{TaskId: []int32{id}})
			p.write(pack.Pack(TASK_GET_REWARD_CMD, reqMSG, p.UID))
		}
		return nil
	}
	defer p.stRelease(SERVER_TASK_GET_REWARD, ch)
	var claimed []int32
	silent := 0
	for _, id := range ids {
		reqMSG, _ := pb.Marshal(&proto.TaskGetRewardReqMsg{TaskId: []int32{id}})
		select {
		case p.writeChan <- pack.Pack(TASK_GET_REWARD_CMD, reqMSG, p.UID):
		case <-p.ctx.Done():
			return claimed
		}
		ok := false
		select {
		case data := <-ch:
			var rr proto.TaskGetRewardRespMsg
			// ret nil = field absent = 0 = paid (proto2 optional).
			ok = pb.Unmarshal(data, &rr) == nil && rr.GetRet() == 0
		case <-time.After(elixirClaimAnswerGap):
		case <-p.ctx.Done():
			return claimed
		}
		if ok {
			claimed = append(claimed, id)
			silent = 0
			continue
		}
		if silent++; silent >= 2 {
			break // the answers stopped coming — keep the rest for next tick
		}
	}
	return claimed
}

// elixirTaskGrass is the EASIEST open harvest-task tree the account can
// ACTUALLY PLANT for the post-cap sweep (owner 2026-10-16: once Dược Linh
// hits 30/30, switch to task trees, easiest first; available-seed law
// 2026-10-21: plantable = ROW PRESENT in grassInfos — the empty-plot picker
// lists every row, num irrelevant, planting is free (live PROVEN: PLANT of
// `864024×0` grew); a tree with NO row never unlocked is the silent ret=0
// no-op (2026-10-17: <live-user> had NO 860004 row yet the sweep "planted" it
// forever). `held` therefore means row-presence, set by the caller).
// Incomplete cType-496 tasks, smallest remaining gap wins (ties: newer task
// id). 0 = nothing plantable → keep the field idle (and NEVER del herbs for
// a no-op plant).
func (p *Player) elixirTaskGrass(held map[int32]bool) (grass int32, desc string) {
	best := int64(1 << 40)
	p.TaskMu.Lock()
	defer p.TaskMu.Unlock()
	if !p.taskSynced {
		return 0, ""
	}
	var ids []int32
	for id := range p.tasks {
		if row, ok := elixirTaskStatic[id]; ok && row.cType == 496 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] }) // tie → newest first
	for _, id := range ids {
		e := p.tasks[id]
		if e.hadSelect {
			continue
		}
		value, err := strconv.ParseInt(e.data.GetValue(), 10, 64)
		if err != nil {
			continue
		}
		row := elixirTaskStatic[id]
		if value >= row.target {
			continue // done (state mirrors value>=target, never read), claim pass owns it
		}
		if !held[row.grass] {
			continue // not in the bag → PLANT would silently no-op
		}
		if gap := row.target - value; gap < best {
			best = gap
			grass = row.grass
			desc = fmt.Sprintf("thu %d× %s (đã %d/%d)", row.target, elixirGrassName(row.grass), value, row.target)
		}
	}
	return grass, desc
}

// elixirOrderGrass picks the tree CLOSEST-TO-COMPLETION among the ĐƠN HÀNG
// DƯỢC TIÊN orders — the plantable signal for the owner's "nhiệm vụ ở chỗ
// dược tiên ghé thăm/đơn hàng dược tiên" board (2026-10-17). The 501 never
// carried the type-51 496 dailies on <live-user>@710564 (PICK=0 → the field
// idled forever with 36 empty lots), but the same 8aae carried f4=
// ElixirOrderInfo with 5 destiny orders, each demanding 80× two trees. The
// ct506 rows ("hoàn thành N đơn hàng Dược Tiên", ids 530002–04/550001–08)
// only tick when orders get submitted, and submitting needs the trees
// planted first — so when the 496 board is silent the loop grows what the
// orders ask for.
//
// OWNER LAW 2026-10-18 ("cây đang có rất nhiều nhưng mày vẫn trồng ⇒ check
// bảng nhiệm vụ xem cây nào SẮP ĐỦ để hoàn thành thì trồng cây đó"): rank
// ORDERS, not needs. An order is REACHABLE only when every tree it demands
// has a ROW in grassInfos (available-seed law 2026-10-21, live PROVEN: the
// zero-count row `864024×0` plants and grows fine — planting is free) — one
// tree with NO row at all makes the whole order unreachable because PLANT of
// a never-unlocked seed is a silent no-op, so slots fed for it would never
// complete. Among reachable orders the SMALLEST total shortfall
// Σ max(0, need−bag) wins — that is the order "sắp đủ"; fully
// payable orders are skipped (submitOrders pays them THIS tick, planting
// more of a tree the bag already drowns in is exactly the bug: bag held
// 10862× 860008 while the picker kept planting 860008 because it ranked by
// biggest need). Within the chosen order, plant the tree with the biggest
// individual shortfall (ties: smaller grass id). bag = grassId→num from
// grassInfos (num>0 only — feeds the shortfall math). 0 = nothing plantable.
// Pet orders want danYao, not grass — skipped. Submission itself is the
// sibling pass elixirSubmitOrders.
func elixirOrderGrass(rsp *proto.ElixirEnterHomeResp, bag map[int32]int32) (int32, string) {
	// 1. reachable orders: every demanded tree UNLOCKED — its row rides
	//    grassInfos (num irrelevant, see available-seed law above). A tree
	//    with no row is a silent no-op to PLANT, so its order can never be
	//    grown toward.
	unlocked := map[int32]bool{}
	for _, g := range rsp.GetUserInfo().GetGrassInfos() {
		unlocked[g.GetGrassId()] = true
	}
	type oinfo struct {
		id    int32
		trees []*proto.ElixirGrassInfo // per-grassId summed within the order
	}
	var orders []oinfo
	sum := func(infos []*proto.ElixirGrassInfo) []*proto.ElixirGrassInfo {
		byID := map[int32]*proto.ElixirGrassInfo{}
		var out []*proto.ElixirGrassInfo
		for _, g := range infos {
			if g.GetNum() <= 0 {
				continue
			}
			if e, ok := byID[g.GetGrassId()]; ok {
				e.Num = pb.Int32(e.GetNum() + g.GetNum())
				continue
			}
			c := &proto.ElixirGrassInfo{GrassId: pb.Int32(g.GetGrassId()), Num: pb.Int32(g.GetNum())}
			byID[g.GetGrassId()] = c
			out = append(out, c)
		}
		return out
	}
	add := func(id int32, infos []*proto.ElixirGrassInfo) {
		trees := sum(infos)
		if len(trees) == 0 {
			return
		}
		for _, t := range trees {
			if !unlocked[t.GetGrassId()] {
				return // unreachable — tree never unlocked, PLANT would no-op
			}
		}
		orders = append(orders, oinfo{id: id, trees: trees})
	}
	for _, d := range rsp.GetOrderInfo().GetDestinyOrderInfos() {
		add(d.GetDestinyId(), d.GetGrassInfos())
	}
	for _, tr := range rsp.GetOrderInfo().GetTransOrderInfos() {
		if !tr.GetIsOver() {
			add(0, tr.GetGrassInfos())
		}
	}
	if len(orders) == 0 {
		return 0, ""
	}
	// 2. POOLED need across all reachable orders — two orders demanding the
	//    same tree must be summed before comparing to the bag (bag 100 vs
	//    2×80 of the same tree is a real 60-unit deficit, not "payable").
	need := map[int32]int64{}
	for _, o := range orders {
		for _, t := range o.trees {
			need[t.GetGrassId()] += int64(t.GetNum())
		}
	}
	deficit := map[int32]int64{} // per tree: max(0, pooled need − bag)
	var total int64
	for id, n := range need {
		if d := n - int64(bag[id]); d > 0 {
			deficit[id] = d
			total += d
		}
	}
	if total == 0 {
		return 0, "" // every reachable order is payable → submitOrders' job
	}
	// 3. the order whose trees carry the SMALLEST pooled deficit is "sắp đủ".
	var best *oinfo
	var bestShort int64 = 1 << 60
	for i := range orders {
		var s int64
		var hasDeficit bool
		for _, t := range orders[i].trees {
			if d := deficit[t.GetGrassId()]; d > 0 {
				s += d
				hasDeficit = true
			}
		}
		if !hasDeficit {
			continue // this order needs nothing planted (payable from bag)
		}
		if s < bestShort || (s == bestShort && best != nil && orders[i].id < best.id) {
			bestShort, best = s, &orders[i]
		}
	}
	if best == nil {
		return 0, ""
	}
	// 4. within it, the tree with the biggest pooled deficit (ties: smaller id).
	var gID int32
	var gShort int64
	for _, t := range best.trees {
		d := deficit[t.GetGrassId()]
		if d <= 0 {
			continue
		}
		if d > gShort || (d == gShort && t.GetGrassId() < gID) {
			gShort, gID = d, t.GetGrassId()
		}
	}
	if gID == 0 {
		return 0, ""
	}
	return gID, fmt.Sprintf("đơn hàng Dược Tiên sắp đủ (thiếu %d× %s, túi %d/%d)",
		gShort, elixirGrassName(gID), bag[gID], need[gID])
}

// elixirLeastBagGrass is the nothing-short fallback (owner 2026-10-21:
// "nếu không có cây nào thiếu thì trồng cây còn ít trong túi"): of the trees
// actually IN the bag (num>0 — every such tree has a grassInfos row, so it
// is plantable under the available-seed law; planting is free) pick the
// SMALLEST count — the rarest tree gets farmed toward its harvest tasks
// while the lots would otherwise idle. Ties: smaller grass id. 0 = empty
// bag → nothing plantable, field idles honestly.
func elixirLeastBagGrass(bag map[int32]int32) (int32, string) {
	var gID int32
	var least int32 = 1 << 30
	for id, n := range bag {
		if n <= 0 {
			continue
		}
		if n < least || (n == least && id < gID) {
			least, gID = n, id
		}
	}
	if gID == 0 {
		return 0, ""
	}
	return gID, fmt.Sprintf("không thiếu cây nào — trồng cây ít nhất trong túi: %s×%d", elixirGrassName(gID), least)
}

// elixirSubmitOrders auto-submits the destiny orders the bag can already
// pay for — owner directive 2026-10-17: "bot phải tự nộp đơn hàng". Pushing
// an order (235515 {type,index}) is free-by-protocol: NO item field in the
// request; the server only DEDUCTS the asked grass from the bag at payout
// and answers ret!=0 when short, so a submit attempt can never burn items
// beyond grass the order itself demands [[tytd-item-ban]] — and we still
// pre-check the bag so a short order never even fires. Destiny orders
// (仙友/“dược tiên ghé thăm”, ElixirOrderInfo.destinyOrderInfos) are the
// board the owner complained about; they are also what ticks the ct506
// ladder rows (530002–04/550001–08, "hoàn thành N đơn hàng Dược Tiên").
// pet orders want danYao (eat-items) — skipped, never touched.
//
// type/index: LIVE-PROVED 2026-10-17 (TestDanGioiPushOrderLive @<live-user>
// /710564): type=1, index = 0-based slot in destinyOrderInfos; a paid slot
// answers ret=0 + rewards ("100302=6400") and the answer's grassInfos carry
// the post-deduction bag. THE BOARD SHIFTS: completing slot i removes it and
// the higher slots slide down (the probe pushed 2,3,4 ascending — index 3
// actually paid the ORIGINAL order[4], and index 4 hit a freshly generated
// order and was refused ret=101 "Không đủ đạo cụ", server-side, free). The
// engine therefore submits HIGHEST-INDEX-FIRST: removals above an index
// never move the slots below it, so every later push still aims at the order
// the bag-check judged. (A mis-aimed push stays harmless anyway: the server
// deducts only what ITS order at that slot demands and refuses when short.)
// ret!=0 keeps the order in the ledger under "idx:signature" so a refused
// slot is retried at most once per content-change, not every minute; a
// SUCCESS drops the entry (the replacement order gets its own verdict).
// elixirOrderAutoCap — OWNER DIRECTIVE 2026-10-21 (twice, same day: first
// 25, then "chuyển giới hạn max nhiệm vụ hoàn thành từ 25 về 5, sau khi đến
// max, không nhận thưởng nhiệm vụ nữa nhưng vẫn tiếp tục check cây còn thiếu
// như thường, nếu không có cây nào thiếu thì trồng cây còn ít trong túi").
// The ct506 ladder ("hoàn thành N đơn hàng Dược Tiên", 530002–04/550001–08)
// counts completed orders; once the SERVER's own count reaches 5 the bot
// stops auto-submitting orders AND stops auto-claiming EVERY ladder rung
// (below the cap only rungs at/under the milestone claim — see
// elixirClaimTaskRewards). Planting stays untouched: the picker keeps
// growing whatever the board asks ("vẫn tiếp tục check cây còn thiếu như
// thường"), and when nothing is short it plants the RAREST tree in the bag
// (elixirLeastBagGrass) so the field never idles.
const elixirOrderAutoCap int64 = 5

// elixirOrderLadderCount reads the server's live completed-order count: the
// max value across the known ct506 ladder rows (they all ride the same
// counter). 0 = no ladder row seen this session → NO gate (the server must
// actually PROVE the 25 milestone; silence never blocks the owner's bot).
func (p *Player) elixirOrderLadderCount() int64 {
	p.TaskMu.Lock()
	defer p.TaskMu.Unlock()
	var best int64
	for id, e := range p.tasks {
		if row, ok := elixirTaskStatic[id]; !ok || row.cType != 506 {
			continue
		}
		if v, err := strconv.ParseInt(e.data.GetValue(), 10, 64); err == nil && v > best {
			best = v
		}
	}
	return best
}

func (p *Player) elixirSubmitOrders(rsp *proto.ElixirEnterHomeResp) (submitted int, desc string) {
	orders := rsp.GetOrderInfo().GetDestinyOrderInfos()
	if len(orders) == 0 {
		return 0, ""
	}
	// Owner cap 2026-10-21 (mốc chốt ở 5): ladder at/over cap → nộp TAY,
	// bot chỉ trồng. Checked before anything else so no slot can slip through.
	if n := p.elixirOrderLadderCount(); n >= elixirOrderAutoCap {
		debugf("[%s] [DanGioi] bậc thang đơn hàng %d ≥ %d — KHÔNG tự nộp nữa, chỉ trồng", p.Username, n, elixirOrderAutoCap)
		return 0, ""
	}
	held := map[int32]int32{}
	for _, g := range rsp.GetUserInfo().GetGrassInfos() {
		held[g.GetGrassId()] = g.GetNum()
	}
	p.ElixirMu.Lock()
	if p.elixirOrderTried == nil {
		p.elixirOrderTried = map[string]bool{}
	}
	p.ElixirMu.Unlock()
	// HIGHEST slot first (see header): completing a slot deletes it and the
	// server slides everything ABOVE it down — iterating downward means no
	// not-yet-submitted slot ever moves out from under its checked content.
	for idx := len(orders) - 1; idx >= 0; idx-- {
		d := orders[idx]
		var sig strings.Builder
		want := map[int32]int32{} // one grass can be listed twice — sum it
		for _, g := range d.GetGrassInfos() {
			fmt.Fprintf(&sig, "%d=%d;", g.GetGrassId(), g.GetNum())
			if g.GetNum() > 0 {
				want[g.GetGrassId()] += g.GetNum()
			}
		}
		ready := len(want) > 0
		for id, n := range want {
			if held[id] < n {
				ready = false // bag short → server would refuse; don't fire
			}
		}
		if !ready {
			continue
		}
		key := fmt.Sprintf("%d:%s", idx, sig.String())
		p.ElixirMu.Lock()
		tried := p.elixirOrderTried[key]
		p.ElixirMu.Unlock()
		if tried {
			continue
		}
		body, _ := pb.Marshal(&proto.ElixirPushOrderReq{Type: pb.Int32(1), Index: pb.Int32(int32(idx))})
		data, err := p.elixirRoundTrip(S_ELIXIR_PUSH_ORDER, SERVER_ELIXIR_PUSH_ORDER, body)
		var rr proto.ElixirPushOrderResp
		// rr.Ret nil = no ret field on the wire at all (game schema marks it
		// required — its absence means this was not a real answer): never a
		// success.
		if err != nil || pb.Unmarshal(data, &rr) != nil || rr.Ret == nil || rr.GetRet() != 0 {
			p.ElixirMu.Lock()
			p.elixirOrderTried[key] = true // don't re-knock this exact order every minute
			p.ElixirMu.Unlock()
			continue
		}
		p.ElixirMu.Lock()
		delete(p.elixirOrderTried, key) // replaced order gets a fresh chance
		p.ElixirMu.Unlock()
		// Keep the local bag honest for the NEXT order of this tick:
		// subtract what this payout asked for (and trust the answer's
		// absolute num when it lists that grass at all).
		paid := map[int32]bool{}
		for _, g := range rr.GetGrassInfos() {
			held[g.GetGrassId()] = g.GetNum()
			paid[g.GetGrassId()] = true
		}
		for id, n := range want {
			if !paid[id] {
				if left := held[id] - n; left > 0 {
					held[id] = left
				} else {
					held[id] = 0
				}
			}
		}
		submitted++
		if desc == "" {
			names := make([]string, 0, len(d.GetGrassInfos()))
			for _, g := range d.GetGrassInfos() {
				names = append(names, fmt.Sprintf("%d× %s", g.GetNum(), elixirGrassName(g.GetGrassId())))
			}
			desc = strings.Join(names, " + ")
		}
	}
	return submitted, desc
}

// elixirPlantLoop is loop #2 of the owner's event-driven handler, AUTO-DƯỢC
// LINH REMOVED 2026-10-23 (owner: "bỏ auto dược linh đi, khỏi phải check số
// lượng dược linh với cây dược linh"): the old "check current dược linh <30
// → trồng cây dược linh" first branch is GONE — no Elves counter read, no
// bestElf preference, no 235550 pull in the tick. The chain is now simply
// (owner verbatim, remaining branches): ô vừa hái ở đây → "trồng cây cũ
// trùng id đã thu hoạch ở ô đó"; ô trống từ đầu (hoặc cây cũ bị server
// no-op) → "pick random cây trong túi ra trồng". A herbal row can still be
// chosen — but only as a plain random bag member, never preferred.
//
// A lot is plantable when it is EMPTY in the frame (state==0 && grass==0) OR
// it sits in harvestedGrass (just GET'd by loop #1 of this same pass — a
// single-round plant is verifiably empty now; no second ENTER: "làm luôn
// không cần phải gọi thêm home enter lần nữa"). Growing lots are skipped
// outright — the plant pass NEVER cuốc's a standing tree ("8 chỉ trồng không
// cuốc bỏ nữa", and since 2026-10-23 it never sends 235537 at all: "không
// cuốc nữa"); a just-harvested MULTI-round lot is still standing, so its
// PLANT is a harmless echo-refused no-op and the next 8aae corrects the
// picture. Each branch of the chain FALLS THROUGH on a refusal (server
// no-op ≠ "lot done") until some tree actually echoes back planted.
//
// Task-board planting is GONE from the loop (owner 2026-10-23: "bỏ qua chỗ
// làm nhiệm vụ") — elixirTaskGrass stays dormant for the diagnostic
// snapshot. Per-lot packets (elixirBatch=1, no-VIP gate), Elves tick always
// on, echo-verified. Returns echo-accepted counts by pick kind.
func (p *Player) elixirPlantLoop(rsp *proto.ElixirEnterHomeResp, harvestedGrass map[int32]int32) (planted, nOld, nRand int) {
	p.ElixirGardenMu.Lock()
	bag := make([]int32, 0, len(p.ElixirBagRows))
	for id := range p.ElixirBagRows {
		bag = append(bag, id)
	}
	p.ElixirGardenMu.Unlock()
	sort.Slice(bag, func(i, j int) bool { return bag[i] < bag[j] }) // stable order for the random pick
	debugf("[%s] [DanGioi] plantLoop inputs: bag=%d dòng lands=%d vừaHái=%d",
		p.Username, len(bag), len(rsp.GetLands()), len(harvestedGrass))
	// DEAD set (live PROVEN 2026-10-23, <live-user>@710564): the server
	// silently no-ops (ret=0 EMPTY echo) a PLANT of a tree it will not grow
	// for THIS account — e.g. every 864xxx herbal while the account's Dược
	// Linh feature is off (elvesOpenOff=true), even with 42 seeds held and
	// the row present; cỏ thường echoes fine either way. Owner complaint
	// "đất trống không tự trồng… tao trồng bằng tay thì được" was exactly
	// the bot trusting the bag, getting the herbal no-op, and abandoning the
	// lot. So a REFUSED pick does not end the lot — it falls through the
	// chain (cây cũ → random túi), and the refused tree goes into `dead`
	// (server-gated for this whole pass; the next pass re-probes it once —
	// self-healing when e.g. Dược Linh unlocks).
	dead := map[int32]bool{}
	plantOne := func(lot, grass int32) bool {
		if grass == 0 || dead[grass] {
			return false
		}
		b, _ := pb.Marshal(&proto.ElixirPlantGrassReq{
			GrassInfo:    []*proto.ElixirPlantGrassInfo{{LandId: pb.Int32(lot), GrassId: pb.Int32(grass)}},
			IsPlantElves: pb.Bool(true)})
		if p.elixirPlantPacketOK(b) {
			return true
		}
		dead[grass] = true
		return false
	}
	for _, l := range rsp.GetLands() {
		id := l.GetLandId()
		oldID, wasHarvested := harvestedGrass[id]
		if !wasHarvested {
			// Only a VERIFIABLY empty lot (state==0 AND no grass) may be
			// planted; anything else counts as occupied (state 2 = growing).
			if !(l.GetState() == 0 && l.GetGrassId() == 0) {
				continue // ô đang có cây → bỏ qua
			}
		}
		// Cây cũ trước, random sau — FALLING THROUGH on a server no-op
		// until something actually grows (no dược linh branch anymore,
		// owner 2026-10-23).
		if wasHarvested && plantOne(id, oldID) {
			planted++
			nOld++ // lô vừa hái ở đây → trồng cây cũ trùng id đã thu hoạch ở ô đó
			continue
		}
		// ô trống từ đầu (hoặc hai nhánh trên đều bị server từ chối) →
		// random cây trong túi: shuffled order = "pick random" the owner
		// asked for, while GUARANTEEING every row gets exactly one shot
		// (dead-skip inside plantOne; a repeat draw would burn the budget
		// and could flake an empty lot).
		order := make([]int32, len(bag))
		copy(order, bag)
		rand.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, g := range order {
			if plantOne(id, g) {
				planted++
				nRand++
				break
			}
		}
	}
	return planted, nOld, nRand
}

// elixirDelLots pulls (铲除 235537→8ad1) whole growing lots. DORMANT since
// 2026-10-23 — the production engine NO LONGER cuốc's anything (owner: "giờ
// không làm nhiệm vụ nữa nên không cuốc nữa"): single-round plants auto-
// empty on GET and multi-round trees re-ripen standing, so nothing needs
// digging. Kept for the gated live-forensic tests only — NO production
// caller. Live-proved free 2026-10-16 (<live-user>): ret=0, NO item cost, NO
// refund — the bag is untouched and the tree is simply gone (lot resets to
// state=0). oneKey is privilege-gated like every other oneKey, so this is
// explicit landIds in batches. Returns lots VERIFIABLY pulled.
//
// RULES (2026-10-18 live + owner correction 2026-10-22 — historical, for
// whoever revives this):
//  1. NEVER GET from here. A lot with an UNCOLLECTED ripe round (canAward>0)
//     answers DEL with ret=0 + EMPTY echo = silent no-op, and a defensive
//     GET of its own STARTS A NEW ROUND on any tree with rounds remaining
//     (canAward back >0), which made the very next DEL no-op — "không bao giờ
//     cuốc" (owner 2026-10-22). Any caller must collect the round itself,
//     seconds before, with no ENTER in between.
//  2. ECHO-VERIFY (like PLANT): ret==0 alone is NOT success — success =
//     ret==0 AND every requested landId echoed back in landInfo.
func (p *Player) elixirDelLots(ids []int32) int {
	ok := 0
	for i := 0; i < len(ids); i += elixirBatch {
		end := i + elixirBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[i:end]
		// oneKey=false EXPLICIT (owner's captured client frame 2026-10-18:
		// 00039811 payload 08 09 10 00 = {landId:[9], oneKey:false}) — the
		// client always writes the flag; match it byte-for-byte. oneKey=true
		// stays privilege-gated, never sent (test-pinned in the suite).
		b, _ := pb.Marshal(&proto.ElixirDelPlantGrassReq{LandId: batch, OneKey: pb.Bool(false)})
		data, err := p.elixirRoundTrip(S_ELIXIR_DEL, SERVER_ELIXIR_DEL, b)
		if err != nil {
			continue
		}
		var dr proto.ElixirDelPlantGrassResp
		if pb.Unmarshal(data, &dr) != nil || dr.GetRet() != 0 {
			continue
		}
		echoed := make(map[int32]bool, len(dr.GetLandInfo()))
		for _, l := range dr.GetLandInfo() {
			echoed[l.GetLandId()] = true
		}
		all := true
		for _, id := range batch {
			if !echoed[id] {
				all = false // server did not free this lot → not a real pull
				break
			}
		}
		if all {
			ok += end - i
		}
	}
	return ok
}

// elixirPlantPacketOK sends one explicit PLANT packet; true = the server
// ACTUALLY planted. ret==0 alone is NOT success: live 2026-10-17
// (TestDanGioiUnheldPlantLive @<live-user>/710564) an unheld tree answers
// ret=0 with an EMPTY land list and the lot stays state=0 (silent no-op),
// while a real plant echoes one ElixirLandInfo per lot (TestDanGioiHeldPlant
// Live: 860003 held → ret=0, resp.land=1, ENTER shows state=2). So success =
// ret==0 AND every requested lot is echoed back. The request body is parsed
// just to count lots — malformed body ⇒ nothing was askable ⇒ false.
func (p *Player) elixirPlantPacketOK(body []byte) bool {
	var req proto.ElixirPlantGrassReq
	if err := pb.Unmarshal(body, &req); err != nil || len(req.GetGrassInfo()) == 0 {
		return false
	}
	data, err := p.elixirRoundTrip(S_ELIXIR_PLANT, SERVER_ELIXIR_PLANT, body)
	if err != nil {
		debugf("[%s] [DanGioi] PLANT round-trip err: %v", p.Username, err) // busy/timeout/gone — packet maybe never out
		return false
	}
	var rsp proto.ElixirPlantGrassResp
	if pb.Unmarshal(data, &rsp) != nil {
		debugf("[%s] [DanGioi] PLANT answer undecodable: % x", p.Username, data)
		return false
	}
	if rsp.GetRet() != 0 {
		return false
	}
	// Echo set must cover every requested landId, same tree, state taken.
	echoed := make(map[int32]int32, len(rsp.GetLand()))
	for _, l := range rsp.GetLand() {
		echoed[l.GetLandId()] = l.GetGrassId()
	}
	for _, gi := range req.GetGrassInfo() {
		if g, ok := echoed[gi.GetLandId()]; !ok || g != gi.GetGrassId() {
			// ret=0 EMPTY echo = the live server's UNHELD no-op (tree not in
			// grassInfos rows) — attributable thanks to per-lot packets.
			debugf("[%s] [DanGioi] PLANT lot %d %s no echo (ret=0 empty — unheld/no-op?)", p.Username, gi.GetLandId(), elixirGrassName(gi.GetGrassId()))
			return false
		}
	}
	return true
}

// danGioiTickResult reports what ONE garden pass did. danGioiAutoLoop owns
// only the pacing; danGioiTickOnce owns only the firing + waiting; the
// ACTING lives in danGioiGardenPass, driven by the 8aae handler — which is
// the code TestDanGioiLifecycleLive drives through danGioiTickOnce, so a
// green test is a statement about the real production path, never a
// hand-rolled copy (owner 2026-10-20: "xem code test của mày có match với
// engine thật không").
type danGioiTickResult struct {
	EnterErr  error   // ENTER unanswered — the loop logs it and sleeps the flat minute
	Harvested []int32 // lots whose GET the server ACCEPTED this pass
	// No ClearPulled since 2026-10-23 — the engine stopped cuốc'ing ("giờ
	// không làm nhiệm vụ nữa nên không cuốc nữa"): single-round plants
	// auto-empty on the GET and get replanted same-pass; multi-round trees
	// stay standing (state=2, hadRecv+1) and re-ripen on their own timer.
	// Ground truth for replanting is verified by TestDanGioiLifecycleLive
	// AFTER the pass on the AUTO-EMPTIED lots only.
	Planted   int // lots planted this pass (echo-accepted)
	PlantDesc string
	Season    int32 // season folded this pass (self-heal + notify text)
}

// danGioiTickOnce fires ONE tick — literally ONE blind packet per minute:
// ENTER (235502). The Dược Linh panel pull (235550) was DROPPED 2026-10-23
// (owner: "bỏ auto dược linh đi, khỏi phải check số lượng dược linh với cây
// dược linh") — the engine no longer reads the elves counter at all. It
// then WAITS for the 8aae handler's pass event (elixirPassSig) and returns
// that pass's report. No seat, no retry, no acting here. keepRunning=false
// only when the session ctx died.
func (p *Player) danGioiTickOnce() (danGioiTickResult, bool) {
	var res danGioiTickResult
	_, pass := p.elixirSigsGet()
	select {
	case <-pass: // drain stale pass report
	default:
	}
	eb, err := pb.Marshal(&proto.ElixirEnterHomeReq{PlayerId: pb.Int64(int64(p.UID))})
	if err != nil {
		res.EnterErr = err
		return res, true
	}
	if err := p.elixirFire(S_ELIXIR_ENTER, eb); err != nil {
		return res, false // ctx dead
	}
	select {
	case <-pass:
		p.ElixirGardenMu.Lock()
		res = p.elixirPassRes
		p.ElixirGardenMu.Unlock()
		return res, true
	case <-p.ctx.Done():
		return res, false
	case <-time.After(elixirReqTimeout + 3*time.Second): // 1s handler sleep + per-lot round trips
		res.EnterErr = fmt.Errorf("không có vườn hồi âm sau %v (server limit? xem log [DanGioi] ret=… — vd client game đang mở vườn cùng lúc)", elixirReqTimeout)
		return res, true
	}
}

// danGioiGardenPass is the BRAIN of the đan giới handler (owner
// 2026-10-23, verbatim): "handler của đan giới info sẽ chạy 1 loop quanh
// vườn xem cây chín (thu hoạch cây nào thì lưu lại cây id và ô id). chạy
// loop quanh vườn tiếp check ô đang có cây thì bỏ qua, ô không có cây thì …
// trồng cây cũ trùng id đã thu hoạch ở ô đó, nếu ô đó ban đầu không có cây
// gì thì pick random cây trong túi ra trồng)". The dược linh branches of
// that quote are VOID (same day, owner: "bỏ auto dược linh đi, khỏi phải
// check số lượng dược linh với cây dược linh").
//
// It starts with a 1-second settle (the original "sleep 1 giây để 2 thằng
// kia đến trước" predates the elves pull being dropped; the pause still
// lets the post-ENTER pushes land). Loop #1 = elixirHarvest (per-lot GET
// only, records landId→grassId; NO cuốc since 2026-10-23). Loop #2 =
// elixirPlantLoop (skips occupied lots, plants free ground cây-cũ-first
// then shuffled random, falling through server no-ops). Serialised by
// elixirPassMu — two back-to-back 8aae never double-run the field. Reports
// via elixirPassRes + elixirPassSig. "giờ chỉ làm chứ không nhận" holds: no
// claims, no order submits.
func (p *Player) danGioiGardenPass(g *proto.ElixirEnterHomeResp) {
	if !p.elixirPassMu.TryLock() {
		return // a pass is already working this garden
	}
	defer p.elixirPassMu.Unlock()
	select {
	case <-p.ctx.Done():
		return
	case <-time.After(time.Second):
	}
	var res danGioiTickResult
	res.Season = p.elixirCurSeason() // self-heals across a rollover with no 8ae9
	harvestedGrass := p.elixirHarvest(g)
	for id := range harvestedGrass {
		res.Harvested = append(res.Harvested, id)
	}
	sort.Slice(res.Harvested, func(i, j int) bool { return res.Harvested[i] < res.Harvested[j] })
	if len(res.Harvested) > 0 {
		p.sendNotify(fmt.Sprintf("🌿 Đan Giới: thu %d lô chín", len(res.Harvested)))
	}
	var nOld, nRand int
	res.Planted, nOld, nRand = p.elixirPlantLoop(g, harvestedGrass)
	res.PlantDesc = fmt.Sprintf("cây cũ %d | random %d", nOld, nRand)
	if res.Planted > 0 {
		p.sendNotify(fmt.Sprintf("🌱 Đan Giới: trồng %d lô — %s", res.Planted, res.PlantDesc))
	}
	p.ElixirGardenMu.Lock()
	p.elixirPassRes = res
	sig := p.elixirPassSig
	p.ElixirGardenMu.Unlock()
	if sig != nil {
		select {
		case sig <- struct{}{}:
		default:
		}
	}
}

// danGioiAutoLoop paces danGioiTickOnce — ALWAYS-ON for every account,
// owner decision 2026-10-17: "bỏ cái cấu hình tắt auto đan giới ở web đi,
// mặc định bật không cho tắt". The opt-out is gone entirely (DB field, web
// checkbox, and the old /auto_dangioi command all deleted); the only stop
// is the session ctx. Every send stays free-by-protocol (harvest/plant;
// no water, no speed items [[tytd-item-ban]]).
//
// The loop is deliberately dumb (owner 2026-10-23: "bắn request xong sleep
// 1 phút"): fire the ONE ENTER packet, wait for the handler's pass report
// (or expiry), log, sleep a flat minute, repeat. The FIRST tick fires within
// seconds of login — that is the "gửi request ngay sau khi login" that folds
// the bag into the player vars. The 1-minute pace IS the retry policy —
// gentle enough that even a rate-limited account (ret=30) drains its penalty
// window on its own.
func (p *Player) danGioiAutoLoop() {
	for {
		_, keep := p.danGioiTickOnce()
		if !keep {
			return
		}
		if !p.elixirSleepCtx(elixirLoopGap) {
			return
		}
	}
}

func (p *Player) elixirSleepCtx(d time.Duration) bool {
	select {
	case <-p.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// elixirDeliver feeds the round-trip seats from the pump (player.go) and
// logs server refusals once (ret!=0 on a oneKey no-op is normal-ish, so only
// harvest answers are worth a line).
func (p *Player) elixirDeliver(msgType string, data []byte) {
	switch msgType {
	case SERVER_ELIXIR_DRAW_SEASON: // passive push — fold, no seat
		var s proto.ElixirElvesDrawSeasonSync
		if err := pb.Unmarshal(data, &s); err == nil && s.GetSeason() > 0 {
			p.ElixirMu.Lock()
			p.ElixirSeason = s.GetSeason()
			p.ElixirSeasonStart = s.GetStartTime() // unix MILLIS (live-decoded 8ae9)
			p.ElixirSeasonEnd = s.GetEndTime()
			p.ElixirPredLogged = 0
			p.ElixirMu.Unlock()
		}
		return
	case SERVER_DANGIOI_INFO:
		// THE home-info HANDLER — the ONE receiver of 8aae AND the brain
		// (owner 2026-10-23: "handler của đan giới info sẽ chạy 1 loop quanh
		// vườn…"). Every full 8aae — ENTER answer OR unsolicited sync push —
		// folds garden map + bag rows + best Dược Linh (AVAILABLE-SEED LAW:
		// ×0 rows included) under ElixirGardenMu, pokes elixirGardenSig (for
		// elixirEnterHome waiters) and then runs the garden pass (serialised
		// by elixirPassMu; 1s sleep up front so the 8ade counter lands
		// first). Lands-less delta pushes carry no lot truth and must NOT
		// clobber the fold; refusals (ret!=0, e.g. 30 = rate limit) are
		// logged here — this is the only place 8aae is read.
		var g proto.ElixirEnterHomeResp
		if pb.Unmarshal(data, &g) == nil && g.GetRet() == 0 && len(g.GetLands()) > 0 {
			m := make(map[int32]*proto.ElixirLandInfo, len(g.GetLands()))
			for _, l := range g.GetLands() {
				m[l.GetLandId()] = l
			}
			bag := map[int32]int32{}
			for _, gi := range g.GetUserInfo().GetGrassInfos() {
				bag[gi.GetGrassId()] = gi.GetNum() // ×0 rows KEPT — row present = plantable
			}
			p.ElixirGardenMu.Lock()
			p.ElixirGarden = m
			p.ElixirHome = &g
			p.ElixirBagRows = bag
			p.ElixirGardenAt = time.Now()
			gsig := p.elixirGardenSig
			p.ElixirGardenMu.Unlock()
			if gsig != nil {
				select {
				case gsig <- struct{}{}:
				default:
				}
			}
			go p.danGioiGardenPass(&g) // fresh-8aae event → the two loops
		}
		// No stDeliver here: nothing claims a seat for 8aae anymore — the
		// fold above IS the delivery (pure event-driven, owner 2026-10-23).
	case SERVER_ELIXIR_ELVES_INFO:
		// Dược Linh panel answer (235550→8ade). The engine stopped ASKING
		// for it in the tick (owner 2026-10-23: "bỏ auto dược linh, khỏi
		// phải check số lượng dược linh") — this case only exists to feed
		// the seat of the on-demand /dangioi snapshot pull.
		p.stDeliver(msgType, data)
	case SERVER_ELIXIR_PLANT, SERVER_ELIXIR_WATER, SERVER_ELIXIR_GET, SERVER_ELIXIR_DEL, SERVER_ELIXIR_PUSH_ORDER:
		p.stDeliver(msgType, data)
	}
}

// elixirSnapshot renders the /dangioi diagnostics: a live ENTER (state +
// owned trees) plus the read-only Dược Linh panel (the thu-hôm-nay/cap-30
// counter). The 8ade pull is best-effort — an account that has never opened
// the panel, or a transient miss, just omits the counter line rather than
// failing the whole snapshot.
func (p *Player) elixirSnapshot() (string, error) {
	rsp, err := p.elixirEnterHome()
	if err != nil {
		return "", err
	}
	hist := map[int32]int{}
	var elvesReady int
	planted := map[int32]int{} // grassId → lots planted with it
	for _, l := range rsp.GetLands() {
		hist[l.GetState()]++
		elvesReady += int(l.GetCanAwardElvesNum())
		if id := l.GetGrassId(); id != 0 {
			planted[id]++
		}
	}
	ui := rsp.GetUserInfo()

	// Unlocked Dược Linh-capable trees (grassInfos rows = the empty-plot
	// picker), 864xxx only. AVAILABLE-SEED LAW (owner 2026-10-21, live
	// PROVEN): ROW PRESENT = trồng được, count irrelevant — `×0` vẫn trồng
	// (trồng miễn phí, num KHÔNG phải hạt tiêu hao — owner 2026-10-16:
	// "trồng không cần hạt, trồng nhiều thì có nhiều"); lv = sao cây.
	var ownedHerbs []*proto.ElixirGrassInfo
	for _, g := range ui.GetGrassInfos() {
		if elixirIsHerb(g.GetGrassId()) {
			ownedHerbs = append(ownedHerbs, g)
		}
	}

	out := fmt.Sprintf("🌿 Đan Giới — %d lô: ", len(rsp.GetLands()))
	first := true
	for st, n := range hist {
		if !first {
			out += " · "
		}
		out += fmt.Sprintf("state %d×%d", st, n)
		first = false
	}
	if ui != nil {
		out += fmt.Sprintf("\n💧 nước %d · bucket %d · dược linh chờ thu %d", ui.GetWater(), ui.GetWaterBucket(), elvesReady)
	}
	// /30 counter from the read-only 8ade panel.
	if pd, err := p.elixirElvesEnter(); err == nil && pd != nil {
		out += fmt.Sprintf("\n🧪 dược linh hôm nay: %d/30", pd.GetTodayGetCount())
	}
	season := p.elixirCurSeason()
	p.ElixirMu.Lock()
	folded := p.ElixirSeason
	p.ElixirMu.Unlock()
	golden := elixirGoldenGrass(season)
	if season != folded {
		out += fmt.Sprintf("\n🏆 mùa %d (8ae9 cũ %d đã hết hạn — suy ra theo chu kỳ) — cây vàng: %s",
			season, folded, elixirHerbName(golden))
	} else {
		out += fmt.Sprintf("\n🏆 mùa %d — cây vàng: %s", season, elixirHerbName(golden))
	}

	// Which herbal trees the account can actually roll Dược Linh with: both
	// what's planted now and what it holds. Sorted for a stable report.
	var plantIDs []int32
	for id := range planted {
		if elixirIsHerb(id) {
			plantIDs = append(plantIDs, id)
		}
	}
	if len(plantIDs) > 0 {
		sort.Slice(plantIDs, func(i, j int) bool { return plantIDs[i] < plantIDs[j] })
		var plantedHerbs []string
		for _, id := range plantIDs {
			plantedHerbs = append(plantedHerbs, fmt.Sprintf("%s×%d lô", elixirHerbName(id), planted[id]))
		}
		out += "\n🌱 đang trồng (ra dược linh): " + strings.Join(plantedHerbs, ", ")
	}
	var seedHerbs []string
	for _, g := range ownedHerbs { // ownedHerbs already filtered 864xxx, num>0
		seedHerbs = append(seedHerbs, fmt.Sprintf("%s (lv%d, %d cây)", elixirHerbName(g.GetGrassId()), g.GetLv(), g.GetNum()))
	}
	if len(seedHerbs) > 0 {
		sort.Strings(seedHerbs)
		out += "\n🎁 dược linh đang có: " + strings.Join(seedHerbs, ", ")
	} else {
		out += "\n🎁 chưa có cây dược linh nào (864xxx) trong túi"
	}
	// Task board (Linh Điền daily/accumulative): what can be claimed right
	// now and which 496 task is the easiest still open — the loop works the
	// same numbers every tick.
	p.TaskMu.Lock()
	claimable := 0
	if p.taskSynced {
		for id, e := range p.tasks {
			row, ok := elixirTaskStatic[id]
			if !ok || e.hadSelect {
				continue // state never read: it mirrors value>=target (owner model)
			}
			if v, err := strconv.ParseInt(e.data.GetValue(), 10, 64); err == nil && v >= row.target {
				claimable++
			}
		}
	}
	p.TaskMu.Unlock()
	// Same available-seed law the loop plants with: ROW PRESENT = plantable
	// (owner 2026-10-21, live PROVEN num==0 rows plant); bag keeps real
	// counts for the order shortfall ranking.
	held := map[int32]bool{}
	bag := map[int32]int32{}
	for _, g := range ui.GetGrassInfos() {
		held[g.GetGrassId()] = true
		if g.GetNum() > 0 {
			bag[g.GetGrassId()] = g.GetNum()
		}
	}
	if g, d := p.elixirTaskGrass(held); claimable > 0 || d != "" {
		out += fmt.Sprintf("\n📜 nhiệm vụ: nhận được %d", claimable)
		if g != 0 {
			out += " · dễ nhất: " + d
		}
	}
	// Đơn hàng Dược Tiên: what the loop would plant when the 496 board is
	// silent (the ct506 ladder only ticks on submitted orders).
	if og, od := elixirOrderGrass(rsp, bag); og != 0 {
		out += "\n🧾 " + od
	}
	return out, nil
}
