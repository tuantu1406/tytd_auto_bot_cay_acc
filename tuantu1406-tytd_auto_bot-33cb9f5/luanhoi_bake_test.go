package tytd_go

// Luân Hồi Bí Cảnh bracket-map bake test: the scene contract the /luanhoi
// mini app bakes from, the frame fold (anchors/palette/self), and the
// on-demand PNG. Set TYTD_LH_PREVIEW=<file>.png to also write the picture
// for a human look (owner review flow — same idea as the pagebake mirrors).

import (
	"bytes"
	"encoding/json"
	"image/png"
	"os"
	"testing"
	"tytd_go/internal/gimap"
	"tytd_go/internal/proto"

	pb "google.golang.org/protobuf/proto"
)

func lhFixtureMsgs() []*proto.RebornTrialUnionChallengeMsg {
	base := func(id int64, flag int32, name string, sv, score int64) *proto.RebornTrialUnionBaseMsg {
		return &proto.RebornTrialUnionBaseMsg{UnionId: pb.Int64(id), Flag: pb.Int32(flag),
			Name: pb.String(name), ServerId: pb.Int64(sv), Score: pb.Int64(score), MasterName: pb.String("chủ")}
	}
	return []*proto.RebornTrialUnionChallengeMsg{
		{Base: base(7105611979, 220002, "Thiên Long", 710561, 128400), ReduceBlood: pb.Int64(30500), Rank: pb.Int32(3), Position: pb.Int32(5), HasDefeated: pb.Bool(true)},
		{Base: base(7105620235, 5, "Mathien", 710562, 33210), ReduceBlood: pb.Int64(55030), Rank: pb.Int32(9), Position: pb.Int32(12)},
		{Base: base(7105626388, 1, "Bang Chủ", 710562, 8100), ReduceBlood: pb.Int64(1573202), Rank: pb.Int32(1), Position: pb.Int32(16), ShieldTriggerTime: pb.Int64(1)},
	}
}

func TestLuanHoiSceneContract(t *testing.T) {
	raw, err := gimap.LuanHoiSceneJSON()
	if err != nil {
		t.Fatal(err)
	}
	var sc struct {
		WorldW  int      `json:"worldW"`
		WorldH  int      `json:"worldH"`
		BG      string   `json:"bg"`
		Base    string   `json:"base"`
		DrawW   int      `json:"drawW"`
		Anchors [][2]int `json:"anchors"`
		Palette []string `json:"palette"`
	}
	if json.Unmarshal(raw, &sc) != nil {
		t.Fatalf("scene.json unparse: %v", err)
	}
	if sc.WorldW != gimap.LuanHoiWorldW || sc.WorldH != gimap.LuanHoiWorldH {
		t.Fatalf("world %d×%d", sc.WorldW, sc.WorldH)
	}
	if len(sc.Anchors) != 16 || sc.DrawW != gimap.LuanHoiBaseW {
		t.Fatalf("anchors=%d drawW=%d", len(sc.Anchors), sc.DrawW)
	}
	if len(sc.Palette) != 12 {
		t.Fatalf("palette %d", len(sc.Palette))
	}
	for _, p := range []string{"bg.jpg", "base.png", "scene.json"} {
		if p == "scene.json" {
			continue
		}
		if _, ok := gimap.LuanHoiArt(p); !ok {
			t.Fatalf("art %s missing from embed", p)
		}
	}
	// Anchors stay inside the world rect (a node off-canvas = a base nobody
	// can tap).
	for i, a := range sc.Anchors {
		if a[0] <= 0 || a[0] >= sc.WorldW || a[1] <= 0 || a[1] >= sc.WorldH {
			t.Fatalf("anchor %d out of world: %v", i+1, a)
		}
	}
}

func TestLuanHoiStreamFold(t *testing.T) {
	fights := []gimap.LhFightIn{
		{Name: "ducduy", TargetID: 7105620235, Damage: 900, AtMilli: 1_900_000}, // oldest of the pair
		{Name: "ktv 24/7", TargetID: 7105620235, Damage: 1200, AtMilli: 1_940_000},
		{Name: "tuilaai???", TargetID: 7105611979, Damage: 400, AtMilli: 3_601_000}, // >1800s: dropped
		{Name: "", TargetID: 424242, Damage: 1, AtMilli: 1_990_000},                 // unknown base: no node
	}
	sm := gimap.LuanHoiStream(lhFixtureMsgs(), 7105620235, 2, 3, 0, 0, 2_000_000, fights)
	if sm.Which != gimap.StreamLuanHoi || sm.Lh == nil || len(sm.Lh.Nodes) != 3 {
		t.Fatalf("frame %+v", sm)
	}
	if sm.Lh.Round != 2 || sm.Lh.BattleNum != 3 {
		t.Fatalf("counters %+v", sm.Lh)
	}
	byID := map[int64]gimap.LhNode{}
	for _, n := range sm.Lh.Nodes {
		byID[n.ID] = n
	}
	self := byID[7105620235]
	if !self.Self || self.Pos != 12 {
		t.Fatalf("self node %+v", self)
	}
	ax, ay := gimap.LuanHoiAnchor(12)
	if absF(self.X*float64(gimap.LuanHoiWorldW)-ax) > 1 || absF(self.Y*float64(gimap.LuanHoiWorldH)-ay) > 1 {
		t.Fatalf("normalized pos %.4f/%.4f ≠ anchor %v", self.X, self.Y, [2]float64{ax, ay})
	}
	sh := byID[7105626388]
	if !sh.Shield {
		t.Fatalf("fresh shield not flagged: %+v", sh)
	}
	def := byID[7105611979]
	if !def.Defeat {
		t.Fatalf("defeat not flagged: %+v", def)
	}
	// fight ring: self node carries the two hits newest-first, stale hit drops
	if len(self.Atk) != 2 || self.Atk[0].N != "ktv 24/7" || self.Atk[0].D != 1200 {
		t.Fatalf("atk fold %+v", self.Atk)
	}
	if self.Atk[0].Ago != 60 || self.Atk[1].Ago != 100 {
		t.Fatalf("atk ago %+v", self.Atk)
	}
	if len(def.Atk) != 0 {
		t.Fatalf("stale hit leaked: %+v", def.Atk)
	}
	// stable palette per union across folds
	sm2 := gimap.LuanHoiStream(lhFixtureMsgs(), 0, 1, 0, 0, 0, 5_000_000, nil)
	for i, n := range sm2.Lh.Nodes {
		if n.Color != sm.Lh.Nodes[i].Color {
			t.Fatalf("color not stable: %s vs %s", n.Color, sm.Lh.Nodes[i].Color)
		}
	}
	// position beyond 16 must not stack on slot 16 (overlapping bases)
	a17x, a17y := gimap.LuanHoiAnchor(17)
	a1x, a1y := gimap.LuanHoiAnchor(1)
	if a17x == a1x && a17y == a1y {
		t.Fatal("position 17 stacked exactly on 1")
	}
}

func TestLuanHoiRenderPreview(t *testing.T) {
	nodes := []gimap.LuanHoiNode{
		{ID: 7105611979, Name: "Thiên Long", Score: 128400, Rank: 3, Pos: 5, LeftHP: 30530, Defeat: true},
		{ID: 7105620235, Name: "Mathien", Score: 33210, Rank: 9, Pos: 12, LeftHP: 55030, IsSelf: true, AtkName: "ktv 24/7", AtkCount: 2},
		{ID: 7105626388, Name: "Bang Chủ", Score: 8100, Rank: 1, Pos: 16, LeftHP: 1573202},
		{ID: 7105633464, Name: "Hoa Sơn", Score: 40200, Rank: 5, Pos: 2, LeftHP: 220000, AtkName: "ducduy", AtkCount: 5},
		{ID: 7105640001, Name: "Thiếu Lâm", Score: 66600, Rank: 7, Pos: 15, LeftHP: 99000},
	}
	img, err := gimap.RenderLuanHoi(nodes, "Luân Hồi Bí Cảnh — bảng đấu", 1400)
	if err != nil {
		t.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	b := im.Bounds()
	if b.Dx() != 1400 || b.Dy() < 800 || b.Dy() > 860 {
		t.Fatalf("render bounds %v", b)
	}
	if out := os.Getenv("TYTD_LH_PREVIEW"); out != "" {
		if err := os.WriteFile(out, img, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("preview written: %s", out)
	}
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
