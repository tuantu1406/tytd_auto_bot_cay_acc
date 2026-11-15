package tytd_go

// Bồng Lai / Thiên Ma view-window detail (206823 → 1aa7) surfaced to the
// islands' mini app through /events/mapview?w=gi (dispatched from
// webKunlunMapView, same twin contract as w=bm): the page asks once on
// boot and again whenever the camera stops, and gets "who STANDS in each
// home-block city" + real city HP. The pull is the client's own fixed
// 16-city home-block vec — a wider ask is ret=6833 — so there are no
// camera-bounds params; the answer's routeInfo rows fold into the march
// ledger on the way, reaching the page through the SSE tick instead.

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// giViewHead / giViewCity are the compact wire shapes the page reads. O
// marks own-side server-side (the fleet's unions are known here, not in
// the frame): heads inherit their city's owner side, or their own side
// from a march row the merged ledger still holds for that playerId.
// Names are NEVER invented: headInfo.name is empty in the standing roster,
// so an empty row borrows the name the playerId carried on a march — and
// stays blank otherwise (the page falls back to "người <id>" verbatim,
// never a fabricated label).
type giViewHead struct {
	N string `json:"n,omitempty"`
	P int64  `json:"p,omitempty"` // playerId — the page's stable row key
	O bool   `json:"o,omitempty"`
}

type giViewCity struct {
	HP    int32        `json:"hp"`
	Cur   int32        `json:"cur"`
	Dead  bool         `json:"dead,omitempty"`
	Fight bool         `json:"fight,omitempty"`
	U     int64        `json:"u,omitempty"`
	UN    string       `json:"un,omitempty"`
	Heads []giViewHead `json:"heads,omitempty"`
}

type giViewBody struct {
	Cities map[string]giViewCity `json:"cities"`
	Heads  int                   `json:"heads"`
}

// giViewWhich resolves the ?which= selector; unknown values are a 400.
func giViewWhich(q string) (int, bool) {
	switch q {
	case "bonglai":
		return GIEventNormal, true
	case "thienma":
		return GIEventPeak, true
	}
	return 0, false
}

// giViewPullers enumerates this leader's event accounts seated in the
// variant (announced + not kicked out). GI has no separate scene flag —
// godIslandStay keeps every announced variant resident — so eligibility IS
// seating; the shuffle keeps one chatty account from owning every pull.
func (t *TeleBot) giViewPullers(chatID int64, which int) []*EventPlayer {
	var out []*EventPlayer
	t.session.Range(func(_, v any) bool {
		if ep, ok := v.(*EventPlayer); ok && ep.ChatID == chatID && ep.Running && ep.giVariantOpen(which) {
			out = append(out, ep)
		}
		return true
	})
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// giViewAnswer flattens one 1aa7 into the page body. Names resolve
// roster → live march row → persistent name cache (1ac0/1abf/1aa6 sightings
// outlive their rows); own decides team colouring (city union, live march
// union, or the cached union from a named sighting). Keys are decimal
// cityId strings so the page looks them up straight off the JSON. A head
// with no name anywhere stays blank — the page draws "người <id>" itself,
// never a fabricated label.
func giViewAnswer(rsp *proto.GodIslandGameCityInfoRespMsg, ledger map[int64]giRouteRec, names map[int64]giNameRec, own map[int64]bool) giViewBody {
	b := giViewBody{Cities: map[string]giViewCity{}}
	for _, c := range rsp.GetCityInfo() {
		if c.GetCityId() == 0 {
			continue
		}
		cu := c.GetBaseData().GetUnionId()
		row := giViewCity{
			HP: c.GetHp(), Cur: c.GetCurHp(),
			Dead: c.GetDestroyed(), Fight: c.GetHaveFight(),
			U: cu, UN: c.GetBaseData().GetUnionName(),
		}
		for _, h := range c.GetHeadInfo() {
			pid := h.GetPlayerId()
			if pid == 0 {
				continue
			}
			rec, has := ledger[pid]
			nr, hasN := names[pid]
			head := giViewHead{N: h.GetName(), P: pid}
			if head.N == "" && has {
				head.N = rec.Name
			}
			if head.N == "" && hasN {
				head.N = nr.Name
			}
			head.O = own[cu] || (has && own[rec.Union]) || (hasN && own[nr.Union])
			row.Heads = append(row.Heads, head)
			b.Heads++
		}
		b.Cities[strconv.FormatInt(int64(c.GetCityId()), 10)] = row
	}
	return b
}

// webGodIslandMapView is GET /events/mapview?w=gi&which=bonglai|thienma.
// Event accounts seated in the variant answer (≤4 tries — one stuck socket
// must not stall the fetch); any non-200 means "keep the old view" for the
// page, plain Vietnamese like the rest of the web surface.
func (t *TeleBot) webGodIslandMapView(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	which, ok := giViewWhich(r.URL.Query().Get("which"))
	if !ok {
		http.Error(w, "đảo không hợp lệ (bonglai|thienma)", http.StatusBadRequest)
		return
	}
	pullers := t.giViewPullers(chatID, which)
	if len(pullers) == 0 {
		http.Error(w, "không có acc sự kiện đang ở đảo này", http.StatusBadGateway)
		return
	}
	var rsp *proto.GodIslandGameCityInfoRespMsg
	var err error
	for i, ep := range pullers {
		if i >= 4 {
			break
		}
		raw, e := ep.RequestGodIslandCityInfo(which, mapStreamTimeout)
		if e != nil {
			err = e
			continue
		}
		var got proto.GodIslandGameCityInfoRespMsg
		if e := pb.Unmarshal(raw, &got); e != nil {
			err = e
			continue
		}
		if got.GetRet() != 0 {
			err = errGiRet(got.GetRet())
			continue
		}
		rsp = &got
		break
	}
	if rsp == nil {
		if err == nil {
			err = errGiNoAnswer
		}
		debugf("[web] gi mapview %d which=%d: %v", chatID, which, err)
		http.Error(w, "không lấy được danh sách người trên đảo", http.StatusBadGateway)
		return
	}
	body := giViewAnswer(rsp, t.giMergeRouteLedgers(chatID, which),
		t.giMergeNames(chatID), t.giOwnUnions(chatID, which))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}
