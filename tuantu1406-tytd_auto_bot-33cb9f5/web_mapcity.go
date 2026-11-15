package tytd_go

// GET /events/mapcity?which=cuuu&city=1001 — the city-click panel of the
// streamed Cửu U map: who is queued attacking and who is defending, the
// same list the game shows when you tap a city. Sourced like the map
// stream itself (main first, then a random resident event account): only
// battlefield residents get a 209524 answer, so candidates are tried in
// order until one returns ret=0.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

// hlLinePuller is the city-queue capability of an account.
type hlLinePuller interface {
	RequestHolyLandCityLine(cityID int32, timeout time.Duration) ([]byte, error)
}

const mapCityTimeout = 8 * time.Second

type mapCityRow struct {
	Name      string `json:"name"`
	UnionName string `json:"unionName,omitempty"`
	NPC       bool   `json:"npc,omitempty"`
}

type mapCityResp struct {
	City      int          `json:"city"`
	Attackers []mapCityRow `json:"attackers"`
	Defenders []mapCityRow `json:"defenders"`
	Battles   int          `json:"battles"`
}

func hlLineRows(list []*proto.HolyLandGameLinePlayerInfo) []mapCityRow {
	rows := make([]mapCityRow, 0, len(list))
	for _, pl := range list {
		rows = append(rows, mapCityRow{Name: pl.GetName(), UnionName: pl.GetUnionName(), NPC: pl.GetNpc()})
	}
	return rows
}

// webEventMapCityAnswer is the pure request side: try candidates in stream
// order, decode the first ret=0 answer.
func webEventMapCityAnswer(pullers []hlLinePuller, cityID int32) (mapCityResp, error) {
	var lastErr error
	for _, p := range pullers {
		data, err := p.RequestHolyLandCityLine(cityID, mapCityTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		var resp proto.HolyLandGameTargetCityLineInfoRespMsg
		if err := pb.Unmarshal(data, &resp); err != nil {
			lastErr = fmt.Errorf("bad line answer: %w", err)
			continue
		}
		if resp.GetRet() != 0 {
			lastErr = fmt.Errorf("server trả lời ret=%d (acc chưa vào chiến trường?)", resp.GetRet())
			continue
		}
		return mapCityResp{
			City:      int(cityID),
			Attackers: hlLineRows(resp.GetAttackInfo()),
			Defenders: hlLineRows(resp.GetDefendInfo()),
			Battles:   len(resp.GetBattleInfo()),
		}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("không có acc nào trực tuyến cho sự kiện này")
	}
	return mapCityResp{}, lastErr
}

func (t *TeleBot) webEventMapCity(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	q := r.URL.Query()
	if q.Get("which") == gimap.StreamLuanHoi {
		// Luân Hồi base panel: the bracket belongs to the leader's own
		// session — no candidate shuffle, just that account's pulls.
		unionID, err := strconv.ParseInt(q.Get("union"), 10, 64)
		if err != nil || unionID <= 0 {
			http.Error(w, "bang khong hop le", http.StatusBadRequest)
			return
		}
		p := t.luanhoiDonor(chatID)
		if p == nil {
			http.Error(w, "acc chinh offline", http.StatusServiceUnavailable)
			return
		}
		res, err := t.luanHoiCityAnswer(p, unionID)
		if err != nil {
			http.Error(w, "khong lay duoc quan trong base ("+err.Error()+")", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(res)
		return
	}
	if q.Get("which") == gimap.StreamSeaWarWar {
		unionID, err := strconv.ParseInt(q.Get("union"), 10, 64)
		if err != nil || unionID <= 0 {
			http.Error(w, "bang khong hop le", http.StatusBadRequest)
			return
		}
		src := t.swWarDonor(chatID)
		if src == nil {
			http.Error(w, "chưa có acc nào trực tuyến trong Thương Minh Yêu Vực", http.StatusServiceUnavailable)
			return
		}
		res, err := t.swWarCityAnswer(src, unionID)
		if err != nil {
			http.Error(w, "khong lay duoc danh sach thu ("+err.Error()+")", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(res)
		return
	}
	if q.Get("which") != gimap.StreamCuuu {
		http.Error(w, "bản đồ không hợp lệ", http.StatusBadRequest)
		return
	}
	cityID, err := strconv.Atoi(q.Get("city"))
	if err != nil || cityID < 1001 || cityID > 4001 {
		http.Error(w, "thành không hợp lệ", http.StatusBadRequest)
		return
	}
	cands := t.cuuuCandidates(chatID)
	pullers := make([]hlLinePuller, 0, len(cands))
	for _, c := range cands {
		if lp, ok := c.(hlLinePuller); ok {
			pullers = append(pullers, lp)
		}
	}
	res, err := webEventMapCityAnswer(pullers, int32(cityID))
	if err != nil {
		http.Error(w, "không lấy được quân trong thành", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(res)
}
