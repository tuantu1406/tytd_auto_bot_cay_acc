package tytd_go

// Route-ledger tests for the /kunlun mini app: the 219943 fold, the shared
// snapshot rules (kind/slot/name/prune), and the mapart ku allow-list.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func kuMiniRawFixture(t *testing.T) []byte {
	t.Helper()
	rsp := &proto.KunlunWarMiniMapInfoRespMsg{
		CityInfo: []*proto.KunlunWarMiniCityInfo{
			{CityId: pb.Int32(2001), UnionId: pb.Int64(202)},
			{CityId: pb.Int32(1001), UnionId: pb.Int64(101)},
			{CityId: pb.Int32(2002), UnionId: pb.Int64(202)},
		},
		BaseData: []*proto.KunlunUnionBaseData{
			{UnionId: pb.Int64(101), Name: pb.String("Ta")},
			{UnionId: pb.Int64(202), Name: pb.String("Địch")},
		},
	}
	b, err := pb.Marshal(rsp)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKuRouteFoldAndSnapshot(t *testing.T) {
	miniRaw := kuMiniRawFixture(t)
	unions := map[int64]*proto.KunlunUnionBaseData{
		101: {UnionId: pb.Int64(101), Name: pb.String("Ta")},
		202: {UnionId: pb.Int64(202), Name: pb.String("Địch")},
	}
	now := time.Now()
	ms := now.UnixMilli()
	sync := &proto.KunlunGameRouteInfoSyncMsg{RouteInfo: []*proto.KunlunWarGameRouteInfo{
		{PlayerId: pb.Int64(7), FromPoint: pb.Int32(100), ToPoint: pb.Int32(200),
			Time: pb.Int64(ms), Arrival: pb.Int64(ms + 30000), PlayerInfo: &proto.KunlunWarPlayerInfo{UnionId: pb.Int64(101)}},
		{PlayerId: pb.Int64(8), FromPoint: pb.Int32(101), ToPoint: pb.Int32(201),
			Time: pb.Int64(ms), Arrival: pb.Int64(ms + 20000), PlayerInfo: &proto.KunlunWarPlayerInfo{UnionId: pb.Int64(101)}},
		{PlayerId: pb.Int64(9), FromPoint: pb.Int32(102), ToPoint: pb.Int32(202),
			Time: pb.Int64(ms), Arrival: pb.Int64(ms + 40000), PlayerInfo: &proto.KunlunWarPlayerInfo{UnionId: pb.Int64(202)}},
		{PlayerId: pb.Int64(10), FromPoint: pb.Int32(103), ToPoint: pb.Int32(203),
			Time: pb.Int64(ms), Arrival: pb.Int64(ms + 10000),
			PlayerInfo: &proto.KunlunWarPlayerInfo{UnionId: pb.Int64(202), BackHomeMsg: &proto.KunlunWarBackHomeMsg{}}},
		{PlayerId: pb.Int64(11), FromPoint: pb.Int32(104), ToPoint: pb.Int32(204),
			Time: pb.Int64(ms - 60000), Arrival: pb.Int64(ms - 9000), PlayerInfo: &proto.KunlunWarPlayerInfo{UnionId: pb.Int64(202)}},
	}}
	data, err := pb.Marshal(sync)
	if err != nil {
		t.Fatal(err)
	}
	recs := map[int64]kuRouteRec{}
	if n := kuBuildRouteRecs(recs, data); n != 5 {
		t.Fatalf("fold applied %d, want 5", n)
	}
	if recs[10].BackHome != true || recs[7].UnionID != 101 {
		t.Fatalf("fold lost fields: %+v %+v", recs[10], recs[7])
	}
	out := kuRoutesFrom(recs, 101, 7, miniRaw, unions, now)
	if len(out) != 4 {
		t.Fatalf("snapshot %d rows (stale pid11 must be pruned): %+v", len(out), out)
	}
	// sorted by arrival: pid10(10s) pid8(20s) pid7(30s) pid9(40s)
	wantK := []int8{4, 3, 2, 1} // backHome wins over union; self=2; own=3; enemy=1
	for i, r := range out {
		if r.K != wantK[i] {
			t.Fatalf("row %d (%+v): kind %d want %d", i, r, r.K, wantK[i])
		}
	}
	if out[2].N != "Ta" {
		t.Fatalf("self row name %q", out[2].N)
	}
	if s := gimap.KuUnionSlot(miniRaw, 101); out[0].S != s && out[2].S != s {
		t.Fatalf("slot for union 101: rows %+v, direct %d", out, s)
	}
	for _, r := range out {
		if r.S < -1 || r.S >= 12 {
			t.Fatalf("slot out of palette range: %+v", r)
		}
	}
}

func TestKuUnionSlotUnknown(t *testing.T) {
	miniRaw := kuMiniRawFixture(t)
	if s := gimap.KuUnionSlot(miniRaw, 101); s < 0 || s >= 12 {
		t.Fatalf("known union slot %d", s)
	}
	if s := gimap.KuUnionSlot(miniRaw, 999); s != -1 {
		t.Fatalf("unknown union slot %d, want -1", s)
	}
}

func TestKuPhaseVNLabels(t *testing.T) {
	for _, tc := range []struct{ ph, want string }{
		{"signup", "Đăng ký"}, {"prepare", "Chuẩn bị"}, {"battle", "Giao chiến"},
		{"settle", "Tổng kết"}, {"reward", "Nhận thưởng"}, {"wat", ""},
	} {
		if got := kuPhaseVN(tc.ph); got != tc.want {
			t.Fatalf("kuPhaseVN(%q)=%q want %q", tc.ph, got, tc.want)
		}
	}
}

func TestMapArtKuStaticAssets(t *testing.T) {
	for _, p := range []string{"scene.json", "sheet.png", "klzb_imgLine1.png", "klzb_imgLine5.png", "klzb_smallmap.png"} {
		data, ctype := kuStaticAsset(p)
		if data == nil {
			t.Fatalf("kuStaticAsset(%q) returned nothing", p)
		}
		if ctype == "" {
			t.Fatalf("kuStaticAsset(%q) no content type", p)
		}
	}
	if data, _ := kuStaticAsset("scene.json"); json.Valid(data) != true {
		t.Fatal("scene.json is not valid JSON")
	}
	// The allow-list IS the embed: unknown or hostile names must not resolve.
	for _, bad := range []string{"../../etc/passwd", "kunlunart/sheet.png", "klzb_imgLine6.png", "", ".png", "live2.png"} {
		if data, _ := kuStaticAsset(bad); data != nil {
			t.Fatalf("kuStaticAsset(%q) leaked %d bytes", bad, len(data))
		}
	}
	// The handler arm must 404 unknown paths even when authed plumbing is fine.
	tl := &TeleBot{}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/events/mapart?b=ku&p=secret.png", nil)
	tl.webEventMapArt(rec, r, 1, nil)
	if rec.Code != 404 {
		t.Fatalf("unknown ku path status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/events/mapart?b=ku&p=scene.json", nil)
	tl.webEventMapArt(rec, r, 1, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" {
		t.Fatalf("scene.json cache: %d %q etag=%q", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
	}
	// A phone revalidating an unchanged binary must get a bare 304.
	etag := rec.Header().Get("ETag")
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/events/mapart?b=ku&p=scene.json", nil)
	r.Header.Set("If-None-Match", etag)
	tl.webEventMapArt(rec, r, 1, nil)
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("scene.json revalidation: %d body=%d", rec.Code, rec.Body.Len())
	}
	// Non-scene embeds rotate with the binary too (2026-11-04 Bắc Minh
	// castle-swap: an immutable year here pinned phones to the OLD sheet
	// while scene.json revalidated — "map vẽ sai tùm lum"). They must ship
	// the same revalidating contract as scene.json.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/events/mapart?b=ku&p=klzb_imgLine1.png", nil)
	tl.webEventMapArt(rec, r, 1, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" {
		t.Fatalf("line cache: %d %q etag=%q", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
	}
	lineEtag := rec.Header().Get("ETag")
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/events/mapart?b=ku&p=klzb_imgLine1.png", nil)
	r.Header.Set("If-None-Match", lineEtag)
	tl.webEventMapArt(rec, r, 1, nil)
	if rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("line revalidation: %d body=%d", rec.Code, rec.Body.Len())
	}
}
