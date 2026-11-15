package gimap

// Sky Trade (Tiên Vực Thương Đồ) live map. The server's enter-map snapshot
// carries ship state and port markets, but every coordinate lives in the
// client UI package, not the wire — the embedded skytrade_map.json is our
// readable embedding of the exact client graph (remoteSkyTrade's
// map_topology.json): 15 ports on the rim of a 14-waypoint route lattice.
// Ships not moving show as docked markers (⚓) on their port; ships en
// route appear as 🚢 markers halfway along their leg, so traffic reads at
// a glance. All labels are plain game Vietnamese; the wire never shows.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

//go:embed skytrade_map.json
var skyTradeMapJSON []byte

type skyNode struct {
	N int     `json:"n"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	P bool    `json:"p"`
}

type skyMapFile struct {
	Width  float64   `json:"width"`
	Height float64   `json:"height"`
	Nodes  []skyNode `json:"nodes"`
	Links  [][2]int  `json:"links"`
}

// The wire identifies ports as 10000+visual index; node numbers 20..33 are
// waypoints. City ids in the stream keep those spaces apart so frames and
// static links agree, and ship markers never collide with a port id.
const (
	SkyWirePortBase = 10000
	skyWaypointBase = 20000
	skyShipBase     = 30000
)

// ArtSkyTrade is the mapart bundle key; skyTradeBg is the game's clean
// cloud-sea backdrop (no islands baked in, since the page now composites
// the client's own art on top of it — exactly like the game screen does).
// Moving ships wear the client's boat sprite for their hull level
// (images/boat/<level>).
const (
	ArtSkyTrade = "st"
	skyTradeBg  = "images/mapBg/bg2.jpg"
)

// Sprite footprints normalized over the map's world box: the port palaces
// (images/uiBuilding/<10000+n>, the client's square art for each place) and
// the hulls. Sizes are measured against the game screen's proportions; the
// browser draws them through the art proxy.
const (
	skyPortW = 0.12
	skyPortH = skyPortW * 346 / 441
	skyShipW = 0.08
	skyShipH = skyShipW * 317 / 463
)

// skyPortIcon is the client's own square art for a port's palace.
func skyPortIcon(n int) string {
	return fmt.Sprintf("images/uiBuilding/%d.png", SkyWirePortBase+n)
}

// clampSkyLevel pins an airship level into the five the game ships.
func clampSkyLevel(level int32) int32 {
	if level < 1 {
		level = 1
	}
	if level > 5 {
		level = 5
	}
	return level
}

// skyBoatIcon maps an airship level to the client's boat art; the bundle
// ships levels 1..5, so anything outside is clamped into that range.
func skyBoatIcon(level int32) string {
	return fmt.Sprintf("images/boat/%d.png", clampSkyLevel(level))
}

var (
	skyOnce sync.Once
	skyMap  *skyMapFile
	skyErr  error
)

func loadSkyMap() (*skyMapFile, error) {
	skyOnce.Do(func() {
		var f skyMapFile
		if err := json.Unmarshal(skyTradeMapJSON, &f); err != nil {
			skyErr = fmt.Errorf("parse embedded skytrade_map.json: %w", err)
			return
		}
		if len(f.Nodes) == 0 || f.Width <= 0 || f.Height <= 0 {
			skyErr = errors.New("skytrade_map.json carries no nodes")
			return
		}
		skyMap = &f
	})
	return skyMap, skyErr
}

// skyPorts normalizes the layout into id→node lookups. Port city ids are
// the wire ids (10001..10015); waypoints get 20000+n.
func skyPorts(m *skyMapFile) (byNode map[int]skyNode, links [][2]uint64) {
	byNode = make(map[int]skyNode, len(m.Nodes))
	for _, nd := range m.Nodes {
		byNode[nd.N] = nd
	}
	links = make([][2]uint64, 0, len(m.Links))
	for _, l := range m.Links {
		a, b := l[0], l[1]
		ida := uint64(skyWaypointBase + a)
		idb := uint64(skyWaypointBase + b)
		if an, ok := byNode[a]; ok && an.P {
			ida = uint64(SkyWirePortBase + a)
		}
		if bn, ok := byNode[b]; ok && bn.P {
			idb = uint64(SkyWirePortBase + b)
		}
		links = append(links, [2]uint64{ida, idb})
	}
	return
}

func skyXY(nd skyNode, m *skyMapFile) (float64, float64) {
	return nd.X / m.Width, nd.Y / m.Height
}

// The game never numbers its ports: the client's own language table
// (RUNBUSINESS_PLACE_Name_* / RUNBUSINESS_GOODS_Name_* /
// RUNBUSINESS_SHIP_Name_* in LanguageWordDB.asset.json) names every place,
// good and hull. These are those exact Vietnamese strings.
var skyPortRealName = map[int]string{
	1: "Bắc Hàn Giới", 2: "Thạch Lâm", 3: "Vụ Ẩn Cốc",
	4: "Tử Tiêu Tiên Cung", 5: "Thanh Liên Đài", 6: "Dao Trì Tiên Đảo",
	7: "Thiên Cơ Các", 8: "Ly Hỏa Cảnh", 9: "Ngộ Đạo Lâu",
	10: "Văn Tân Các", 11: "Thiên Kiếm Phong", 12: "Phá Hư Môn",
	13: "Tiên Trúc Lâm", 14: "Côn Lôn Bí Cảnh", 15: "Thanh Minh Cung",
}

var skyGoodsName = map[int32]string{
	1001: "Thương Vân Tiên Chi", 1002: "Linh Bích Thạch", 1003: "Hàn Yên Lộc Nhung",
	1004: "Bồ Đề Tử", 1005: "Ngũ Thái Huyền Liên", 1006: "Thiên Thanh Trúc",
	1007: "Cửu U Ngọc", 1008: "Vĩnh Dạ Minh Châu", 1009: "Tiên Tàm Tơ Trù",
	1010: "Tuệ Tâm Quả", 1011: "Thất Tu Linh Tham", 1012: "Hỏa Phượng Vũ",
	1013: "Thái Hư Tinh Thảo", 1014: "Xích Huyền Thiết", 1015: "Cửu Khúc Tiên Nhưỡng",
}

var skyShipName = map[int32]string{
	1: "Bích Trúc Khinh Chu", 2: "Tinh Trản Phong Chu", 3: "Lương Nghi Huyền Chu",
	4: "Côn Du Thiên Chu", 5: "Long Tương Tiên Chu",
}

// SkyGoodsName is the game's Vietnamese name for a trade good id, or ""
// when the id predates/escapes the language table (callers fall back to the
// raw id).
func SkyGoodsName(id int32) string { return skyGoodsName[id] }

// skyPortName labels a port the way the game shows it, with a numbered
// fallback so an unknown port id never renders blank.
func skyPortName(n int) string {
	if s, ok := skyPortRealName[n]; ok {
		return s
	}
	return "Cảng " + strconv.Itoa(n)
}

// SkyTradeStatic is the fixed route lattice.
func SkyTradeStatic() (*StaticMap, error) {
	m, err := loadSkyMap()
	if err != nil {
		return nil, err
	}
	_, links := skyPorts(m)
	out := &StaticMap{
		Which:  StreamThuongdo,
		Aspect: m.Width / m.Height,
		WorldW: m.Width, WorldH: m.Height,
		Art:    ArtSkyTrade,
		Bg:     skyTradeBg,
		Cities: make([]StaticCity, 0, len(m.Nodes)),
		Links:  links,
	}
	for _, nd := range m.Nodes {
		id := uint64(skyWaypointBase + nd.N)
		c := StaticCity{Lab: ""}
		if nd.P {
			id = uint64(SkyWirePortBase + nd.N)
			c.Name = skyPortName(nd.N)
			c.Lab = strconv.Itoa(nd.N)
			c.Icon = skyPortIcon(nd.N)
			c.W, c.H = skyPortW, skyPortH
		}
		c.ID = id
		c.X, c.Y = skyXY(nd, m)
		out.Cities = append(out.Cities, c)
	}
	sort.Slice(out.Cities, func(i, j int) bool { return out.Cities[i].ID < out.Cities[j].ID })
	return out, nil
}

// SkyTradeStream renders one enter-map snapshot from the viewer's own
// account: the snapshot's PlayerData ship is "you" (chip "Tôi", gold ring)
// exactly like the game client draws it.
func SkyTradeStream(payload []byte) (*StreamMap, error) {
	return SkyTradeStreamAs(payload, "Tôi", true)
}

// SkyTradeStreamAs renders the same snapshot when it was pulled from
// somebody else's session (leader view with the leader busy): the pulled
// account's own ship is a named boat among the union traffic — never
// mislabeled "Tôi" and never gold, because it is not the page viewer's.
func SkyTradeStreamAs(payload []byte, selfName string, selfIsViewer bool) (*StreamMap, error) {
	m, err := loadSkyMap()
	if err != nil {
		return nil, err
	}
	var resp proto.SkyTradeEnterResp
	if err := pb.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("decode sky trade map: %w", err)
	}
	if resp.GetRet() != 0 {
		return nil, fmt.Errorf("server rejected sky trade map (ret=%d)", resp.GetRet())
	}
	byNode, links := skyPorts(m)

	// Docking rule follows the client's own airship data: targetPort 0 means
	// the ship sits at sourcePort; anything else is a live leg.
	type ship struct {
		name, union string
		src, dst    int32
		own         bool
		level       int32
	}
	var ships []ship
	if ai := resp.GetPlayerData().GetAirshipInfo(); ai != nil {
		if selfName == "" {
			selfName = "?"
		}
		ships = append(ships, ship{name: selfName, src: ai.GetSourcePort(), dst: ai.GetTargetPort(), own: selfIsViewer, level: ai.GetLevel()})
	}
	for _, u := range resp.GetUnionPlayerData() {
		ai := u.GetAirshipInfo()
		if ai == nil {
			continue
		}
		s := ship{src: ai.GetSourcePort(), dst: ai.GetTargetPort(), level: ai.GetLevel()}
		if p := u.GetPlayer(); p != nil {
			s.name = p.GetNickName()
			s.union = p.GetUnionData().GetName()
		}
		if s.name == "" {
			s.name = "?"
		}
		ships = append(ships, s)
	}
	// Wire port ids may arrive as 10000+visual or raw visual; normalize once.
	vis := func(id int32) int {
		if n := int(id); n >= SkyWirePortBase {
			return n - SkyWirePortBase
		}
		return int(id)
	}

	rare := make(map[int]int32, len(resp.GetPortGoods()))
	for _, pg := range resp.GetPortGoods() {
		if g := pg.GetRareGoodsId(); g != 0 {
			rare[vis(pg.GetPortId())] = g
		}
	}

	out := &StreamMap{
		Which:  StreamThuongdo,
		Aspect: m.Width / m.Height,
		Art:    ArtSkyTrade,
		Bg:     skyTradeBg,
		Cities: make([]StreamCity, 0, len(m.Nodes)+len(ships)),
		Links:  links,
	}
	docked := make(map[int][]string, 16)
	var moving []ship
	for _, s := range ships {
		if s.dst == 0 {
			tag := s.name
			if s.own {
				tag = "★ " + s.name
			}
			docked[vis(s.src)] = append(docked[vis(s.src)], tag)
		} else {
			moving = append(moving, s)
		}
	}
	for _, nd := range m.Nodes {
		if !nd.P {
			x, y := skyXY(nd, m)
			out.Cities = append(out.Cities, StreamCity{
				ID: uint64(skyWaypointBase + nd.N), X: x, Y: y, Lab: "",
			})
			continue
		}
		x, y := skyXY(nd, m)
		info := make([]string, 0, 4)
		if names := docked[nd.N]; len(names) > 0 {
			shown := names
			if len(shown) > 3 {
				info = append(info, fmt.Sprintf("⚓ %d thuyền: %s…", len(names), strings.Join(shown[:3], ", ")))
			} else {
				info = append(info, fmt.Sprintf("⚓ %s", strings.Join(shown, ", ")))
			}
		}
		if g, ok := rare[nd.N]; ok {
			if name, known := skyGoodsName[g]; known {
				info = append(info, "hàng hiếm: "+name)
			} else {
				info = append(info, fmt.Sprintf("hàng hiếm #%d", g))
			}
		}
		own := strings.HasPrefix(strings.Join(docked[nd.N], "|"), "★ ")
		owner := ""
		if own {
			owner = "Thuyền của bạn đang neo tại đây"
		}
		city := StreamCity{
			ID: uint64(SkyWirePortBase + nd.N), X: x, Y: y,
			Name: skyPortName(nd.N), Lab: strconv.Itoa(nd.N),
			Owner: owner, Info: strings.Join(info, " · "),
			Icon: skyPortIcon(nd.N), W: skyPortW, H: skyPortH,
		}
		if own {
			city.Color = "#f2d98c"
		}
		out.Cities = append(out.Cities, city)
	}
	for i, s := range moving {
		a, okA := byNode[vis(s.src)]
		b, okB := byNode[vis(s.dst)]
		if !okA || !okB {
			continue // unknown port numbers never place a marker
		}
		ax, ay := skyXY(a, m)
		bx, by := skyXY(b, m)
		info := fmt.Sprintf("%s → %s", skyPortName(vis(s.src)), skyPortName(vis(s.dst)))
		if s.union != "" {
			info = s.union + " · " + info
		} else if s.own {
			// The player's own hull wears its in-game ship name.
			if hn, ok := skyShipName[clampSkyLevel(s.level)]; ok {
				info = hn + " · " + info
			}
		}
		city := StreamCity{
			ID: uint64(skyShipBase + i), X: (ax + bx) / 2, Y: (ay + by) / 2,
			Lab: "🚢", Info: info,
			// The name chip the game floats over every boat: own ship reads
			// "Tôi" exactly like the client's red self-tag.
			Name: s.name,
			// The client's own hull art for the ship's level; the page
			// paints it through the art proxy (Lab stays as fallback).
			Icon: skyBoatIcon(s.level), W: skyShipW, H: skyShipH,
		}
		if s.own {
			city.Color = "#f2d98c"
		}
		out.Cities = append(out.Cities, city)
	}
	sort.Slice(out.Cities, func(i, j int) bool { return out.Cities[i].ID < out.Cities[j].ID })
	return out, nil
}
