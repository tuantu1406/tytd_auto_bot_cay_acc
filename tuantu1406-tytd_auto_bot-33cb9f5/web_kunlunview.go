package tytd_go

// Côn Lôn view-window detail (219903 GAME_BLOCK_INFO) surfaced to the
// /kunlun mini app: opening the map and pausing a pan/zoom ask this endpoint,
// which pulls the tiles under the user's camera from an in-scene account and
// answers "who STANDS here (with names)" + "what is this city's real HP".
// The window's marches fold into the puller's march ledger on the way, so
// they reach the page through the existing SSE tick instead of twice.

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"time"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"
)

// kuViewMaxBlocks caps one 219903 request. The client's window is a screen,
// not the board — the full 61×61 would be 3721 block ints.
const kuViewMaxBlocks = 600

// kuViewBlocks clamps a tile rect to the 61×61 board and flattens it into
// posIndex (y*61+x) entries. An oversized rect is sampled with a uniform
// step so one request never exceeds kuViewMaxBlocks — popup rows then only
// appear on sampled tiles, while marches still cover every tile via the
// ledger/SSE path.
func kuViewBlocks(x0, y0, x1, y1 int) []int32 {
	const cols = 61
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > cols-1 {
			return cols - 1
		}
		return v
	}
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	x0, x1, y0, y1 = clamp(x0), clamp(x1), clamp(y0), clamp(y1)
	step := 1
	for ((x1-x0)/step+1)*((y1-y0)/step+1) > kuViewMaxBlocks {
		step++
	}
	out := make([]int32, 0, kuViewMaxBlocks)
	for y := y0; y <= y1; y += step {
		for x := x0; x <= x1; x += step {
			if p := int32(y*cols + x); p > 0 {
				out = append(out, p)
			}
		}
	}
	return out
}

// kuViewPlayer / kuViewCity are the compact wire shapes the page reads:
// standing-army rows (name, union, power) per tile plus real city HP. O marks
// the row as the VIEWER's own side (leader union + their event unions) so the
// mini app paints standing players team-coloured (own=green, enemy=red) — the
// union palette lives on KuFrame cells by SLOT, but a displayInfo carries a
// raw unionId, so the side must be decided server-side where both are known.
type kuViewPlayer struct {
	N string `json:"n,omitempty"`
	U int64  `json:"u,omitempty"`
	P string `json:"p,omitempty"`
	O bool   `json:"o,omitempty"`
}

type kuViewCity struct {
	HP    int32 `json:"hp"`
	Cur   int32 `json:"cur"`
	Dead  bool  `json:"dead,omitempty"`
	Fight bool  `json:"fight,omitempty"`
	U     int64 `json:"u,omitempty"`
}

// kuViewProg is one union's capture progress on a tile (the "cell HP" the
// user asked to surface): PROG/10 vs the occupy-area cost config max.
type kuViewProg struct {
	U    int64 `json:"u"`
	Prog int32 `json:"p"`
}

type kuViewBody struct {
	Cells map[string][]kuViewPlayer `json:"cells"`
	// Prog lists per-tile unionBlockData capture progress — the client's cell
	// "HP" bar (occupyProgress vs 10×KUNLUNWAR_OCCUPY_AREA_COST). Tiles with
	// no progress rows are absent; a tile can hold several unions at once.
	Prog   map[string][]kuViewProg `json:"prog,omitempty"`
	Max    int32                   `json:"max,omitempty"` // full-capture threshold (0 = config unseen)
	Cities map[string]kuViewCity   `json:"cities"`
	N      int                     `json:"blocks"`
	// Spar reports a RIPE spirit fruit via the answering puller's own tree
	// (219910 canReceive). It is a sample, not the fleet's sum — every
	// event account claims its own fruit through the 🍈 order, so the page
	// lights the button on this flag but never disables it.
	Spar bool `json:"spar,omitempty"`
}

// kuOwnUnions collects every union id on the VIEWER's side: the leader's own
// union (regular session) plus all their event accounts' unions. Used to mark
// standing players as friendly server-side (see kuViewPlayer.O).
func (t *TeleBot) kuOwnUnions(chatID int64) map[int64]bool {
	out := map[int64]bool{}
	add := func(id int64) {
		if id == 0 {
			return
		}
		out[id] = true
	}
	if v, ok := t.session.Load(chatID); ok {
		if lp, ok := v.(*Player); ok {
			st := lp.kuState()
			lp.KuMu.Lock()
			add(st.MyUnion)
			lp.KuMu.Unlock()
		}
	}
	t.session.Range(func(_, v any) bool {
		if ep, ok := v.(*EventPlayer); ok && ep.ChatID == chatID {
			st := ep.kuState()
			ep.KuMu.Lock()
			add(st.MyUnion)
			ep.KuMu.Unlock()
		}
		return true
	})
	return out
}

// kuViewAnswer flattens one 219903 response. Keys are decimal posIndex /
// cityId strings so the page can look them up straight off the JSON. own
// marks the viewer's side for team-coloured standing markers (may be nil).
func kuViewAnswer(rsp *proto.KunlunWarGameBlockInfoResp, own map[int64]bool) kuViewBody {
	b := kuViewBody{
		Cells:  map[string][]kuViewPlayer{},
		Cities: map[string]kuViewCity{},
		N:      len(rsp.GetBlockInfo()),
	}
	for _, bi := range rsp.GetBlockInfo() {
		if len(bi.GetDisplayInfo()) == 0 && len(bi.GetUnionBlockData()) == 0 {
			continue
		}
		key := strconv.FormatInt(int64(bi.GetPos()), 10)
		for _, d := range bi.GetDisplayInfo() {
			b.Cells[key] = append(b.Cells[key], kuViewPlayer{
				N: d.GetNickName(), U: d.GetUnionId(), P: d.GetPower(),
				O: own[d.GetUnionId()],
			})
		}
		for _, ub := range bi.GetUnionBlockData() {
			if b.Prog == nil {
				b.Prog = map[string][]kuViewProg{}
			}
			b.Prog[key] = append(b.Prog[key], kuViewProg{
				U: ub.GetUnionId(), Prog: ub.GetOccupyProgress(),
			})
		}
	}
	for _, ci := range rsp.GetCityInfo() {
		b.Cities[strconv.FormatInt(int64(ci.GetCityId()), 10)] = kuViewCity{
			HP: ci.GetHp(), Cur: ci.GetCurHp(), Dead: ci.GetDestroyed(),
			Fight: ci.GetHaveFight(), U: ci.GetUnionId(),
		}
	}
	return b
}

// kuViewPullers enumerates this leader's Côn Lôn-capable event accounts:
// seated first (their sockets already carry the scene), idle behind (the
// request seats them as a side effect — the established event-account
// policy). Shuffle keeps one chatty account from owning every pull.
func (t *TeleBot) kuViewPullers(chatID int64) []*EventPlayer {
	var seated, idle []*EventPlayer
	t.session.Range(func(_, v any) bool {
		ep, ok := v.(*EventPlayer)
		if !ok || ep.ChatID != chatID || !ep.Running || !ep.KunlunWarEventOpen() {
			return true
		}
		if st := ep.kuState(); st.SceneIn {
			seated = append(seated, ep)
		} else {
			idle = append(idle, ep)
		}
		return true
	})
	shuf := func(s []*EventPlayer) {
		rand.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
	}
	shuf(seated)
	shuf(idle)
	return append(seated, idle...)
}

// webKunlunMapView is GET /events/mapview?x0=&y0=&x1=&y1= (tile bounds of
// the page's camera). Seated event accounts answer first, idle event
// accounts next, and the leader's own account last — only when ALREADY in
// the scene, so watching the map never drags the phone into battle. Plain
// Vietnamese 400/502 like the rest of the web surface; the page treats any
// non-200 as "keep the old view".
func (t *TeleBot) webKunlunMapView(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	// w=bm routes the identical view contract to the Đỉnh Bắc Minh board
	// (own 119² clamp + 332 pullers) — plumbing dispatch, not a gate.
	if r.URL.Query().Get("w") == "bm" {
		t.webBacMinhMapView(w, r, chatID, u)
		return
	}
	// w=gi routes it to the islands' standing-people view (206823 fixed
	// home-block vec, no camera bounds) — same dispatch-not-gate pattern.
	if r.URL.Query().Get("w") == "gi" {
		t.webGodIslandMapView(w, r, chatID, u)
		return
	}
	q := r.URL.Query()
	num := func(name string) (int, bool) {
		v, err := strconv.Atoi(q.Get(name))
		return v, err == nil
	}
	x0, okx0 := num("x0")
	y0, oky0 := num("y0")
	x1, okx1 := num("x1")
	y1, oky1 := num("y1")
	if !(okx0 && oky0 && okx1 && oky1) {
		http.Error(w, "thiếu vùng bản đồ", http.StatusBadRequest)
		return
	}
	blocks := kuViewBlocks(x0, y0, x1, y1)
	if len(blocks) == 0 {
		http.Error(w, "vùng bản đồ không hợp lệ", http.StatusBadRequest)
		return
	}
	var rsp *proto.KunlunWarGameBlockInfoResp
	var err error
	var puller *EventPlayer
	for i, ep := range t.kuViewPullers(chatID) {
		if i >= 4 {
			break // bounded tries: one stuck socket must not stall the fetch
		}
		if rsp, err = ep.RequestKunlunGameBlockInfo(blocks, mapStreamTimeout); rsp != nil {
			puller = ep
			break
		}
	}
	if rsp == nil {
		if v, ok := t.session.Load(chatID); ok {
			if p, ok := v.(*Player); ok && p.Running {
				rsp, err = p.RequestKunlunGameBlockInfo(blocks, mapStreamTimeout)
			}
		}
	}
	if rsp == nil {
		if err == nil {
			err = errKuClosed
		}
		debugf("[web] mapview %d: %v", chatID, err)
		http.Error(w, "không lấy được thông tin khu vực", http.StatusBadGateway)
		return
	}
	body := kuViewAnswer(rsp, t.kuOwnUnions(chatID))
	if puller != nil {
		// Best-effort tree sample on the same socket that just answered; a
		// timeout here must NOT delay the view, so it gets a short leash.
		if cr, err := puller.RequestKunlunSparInfo(2 * time.Second); err == nil {
			body.Spar = cr == 1
		}
	}
	// The capture threshold is a per-activity config the leader folds out of
	// the 21003 detail (KUNLUNWAR_OCCUPY_AREA_COST). 0 = never seen — the
	// popup then shows raw progress without the "ĐÃ CHIẾM" verdict.
	body.Max = t.kuMaxHp(chatID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// kuMaxHp returns the full-capture occupyProgress for the viewer's activity
// (10×KUNLUNWAR_OCCUPY_AREA_COST, the client's getBlockMaxHp), 0 if the
// leader's account has not folded the config yet.
func (t *TeleBot) kuMaxHp(chatID int64) int32 {
	v, ok := t.session.Load(chatID)
	if !ok {
		return 0
	}
	lp, ok := v.(*Player)
	if !ok {
		return 0
	}
	st := lp.kuState()
	lp.KuMu.Lock()
	defer lp.KuMu.Unlock()
	if st.OccupyCost == 0 {
		return 0
	}
	return 10 * st.OccupyCost
}
