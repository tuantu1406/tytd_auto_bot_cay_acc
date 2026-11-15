package gimap

// BacMinhSceneJSON exports the WHOLE static Đỉnh Bắc Minh scene through the
// same kuScene document buildKunlunScene produces (see kunlunscene.go for
// the field-by-field contract): the 119×119 grid, every sprite layer, the
// north sheet rects and the client geometry constants, so the /bacminh mini
// app bakes client-side exactly like /kunlun does — the server only ever
// serves this JSON and the raw art bytes (SERVER-RAM policy).
//
// Loose line/minimap art (client bmzd_smallmap.png + the klzb line pngs,
// the engine ships ONE line-art family for both boards) is embedded under
// bacminhart/ and served through the mapart bm arm alongside the sheet.

import (
	"embed"
	"sync"
)

//go:embed bacminhart/*
var bmArtFS embed.FS

// bmArtFile returns an embedded loose art file by its bare name.
func bmArtFile(name string) ([]byte, bool) {
	b, err := bmArtFS.ReadFile("bacminhart/" + name)
	return b, err == nil
}

// BmArtNames lists the bare names of every embedded north loose art file, so
// the web layer can build its mapart allow-list straight from the embed
// (never a hand-maintained copy — see KuArtNames).
func BmArtNames() []string {
	ents, err := bmArtFS.ReadDir("bacminhart")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// BmArtFile returns one embedded north loose art file by bare name, exported
// for the mapart bm arm.
func BmArtFile(name string) ([]byte, bool) { return bmArtFile(name) }

var (
	bmSceneOnce  sync.Once
	bmSceneBytes []byte
)

// BacMinhSceneJSON returns the static north scene bundle (memoised; nil
// only if the embedded layout cannot be parsed, which unit tests make
// impossible).
func BacMinhSceneJSON() []byte {
	bmSceneOnce.Do(func() {
		b, err := buildBacMinhScene()
		if err != nil {
			return
		}
		bmSceneBytes = b
	})
	return bmSceneBytes
}

func buildBacMinhScene() ([]byte, error) {
	mp, err := bacminhLayout()
	if err != nil {
		return nil, err
	}
	return buildWarScene(bmPackMain, mp, "bmzd_smallmap.png")
}
