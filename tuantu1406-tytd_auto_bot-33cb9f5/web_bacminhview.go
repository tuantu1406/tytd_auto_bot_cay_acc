package tytd_go

// Đỉnh Bắc Minh view-window detail (219903 GAME_BLOCK_INFO on the 332
// window) — the /bacminh twin of web_kunlunview.go. /events/mapview?w=bm
// branches here; the flattened answer shape (kuViewBody) and the block
// sampling rule are shared plumbing, only the board (119 cols), the state
// getter (p.bm), the event gate (332) and the tree sample differ.

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"
)

// bmViewBlocks clamps a tile rect to the north grid (cols from the baked
// layout — never a hard-coded 119) and flattens it into posIndex rows,
// sampled to stay under kuViewMaxBlocks like the kunlun arm.
func bmViewBlocks(x0, y0, x1, y1 int) []int32 {
	cols := gimap.BacMinhCols()
	if cols <= 0 {
		return nil
	}
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

// bmOwnUnions — the viewer's side on the north board (leader + event unions
// from their bacminh scene states).
func (t *TeleBot) bmOwnUnions(chatID int64) map[int64]bool {
	out := map[int64]bool{}
	add := func(id int64) {
		if id == 0 {
			return
		}
		out[id] = true
	}
	if v, ok := t.session.Load(chatID); ok {
		if lp, ok := v.(*Player); ok {
			st := lp.bmState()
			lp.BmMu.Lock()
			add(st.MyUnion)
			lp.BmMu.Unlock()
		}
	}
	t.session.Range(func(_, v any) bool {
		if ep, ok := v.(*EventPlayer); ok && ep.ChatID == chatID {
			st := ep.bmStateEP()
			ep.BmMu.Lock()
			add(st.MyUnion)
			ep.BmMu.Unlock()
		}
		return true
	})
	return out
}

// bmViewPullers enumerates this leader's north-capable event accounts,
// seated first (their sockets carry the scene), idle behind (the pull seats
// them as a side effect — the established event-account policy).
func (t *TeleBot) bmViewPullers(chatID int64) []*EventPlayer {
	var seated, idle []*EventPlayer
	t.session.Range(func(_, v any) bool {
		ep, ok := v.(*EventPlayer)
		if !ok || ep.ChatID != chatID || !ep.Running || !ep.BacMinhWarEventOpen() {
			return true
		}
		st := ep.bmStateEP()
		ep.BmMu.Lock()
		in := st.SceneIn
		ep.BmMu.Unlock()
		if in {
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

// webBacMinhMapView is GET /events/mapview?w=bm&x0=&y0=&x1=&y1= — same
// puller ladder as webKunlunMapView (seated event → idle event → leader
// already-in-scene last), north ledger fold, north tree sample, north
// occupy-cost max.
func (t *TeleBot) webBacMinhMapView(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
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
	blocks := bmViewBlocks(x0, y0, x1, y1)
	if len(blocks) == 0 {
		http.Error(w, "vùng bản đồ không hợp lệ", http.StatusBadRequest)
		return
	}
	var rsp *proto.KunlunWarGameBlockInfoResp
	var err error
	var puller *EventPlayer
	for i, ep := range t.bmViewPullers(chatID) {
		if i >= 4 {
			break
		}
		if rsp, err = ep.RequestBacMinhGameBlockInfo(blocks, mapStreamTimeout); rsp != nil {
			puller = ep
			break
		}
	}
	if rsp == nil {
		if v, ok := t.session.Load(chatID); ok {
			if p, ok := v.(*Player); ok && p.Running {
				rsp, err = p.RequestBacMinhGameBlockInfo(blocks, mapStreamTimeout)
			}
		}
	}
	if rsp == nil {
		if err == nil {
			err = errBmClosed
		}
		debugf("[web] mapview bm %d: %v", chatID, err)
		http.Error(w, "không lấy được thông tin khu vực", http.StatusBadGateway)
		return
	}
	body := kuViewAnswer(rsp, t.bmOwnUnions(chatID))
	if puller != nil {
		if cr, err := puller.RequestBacMinhSparInfo(2 * time.Second); err == nil {
			body.Spar = cr == 1
		}
	}
	body.Max = t.bmMaxHp(chatID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// bmMaxHp is the north full-capture threshold (10×KUNLUNWAR_OCCUPY_AREA_COST
// folded by bmMergeConfig), 0 = config unseen.
func (t *TeleBot) bmMaxHp(chatID int64) int32 {
	v, ok := t.session.Load(chatID)
	if !ok {
		return 0
	}
	lp, ok := v.(*Player)
	if !ok {
		return 0
	}
	st := lp.bmState()
	lp.BmMu.Lock()
	defer lp.BmMu.Unlock()
	if st.OccupyCost == 0 {
		return 0
	}
	return 10 * st.OccupyCost
}
