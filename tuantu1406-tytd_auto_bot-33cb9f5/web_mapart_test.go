package tytd_go

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Art proxy: manifest allow-listing, cache-once behavior, status contract.

func newMapArtTestBundle(t *testing.T) *artBundle {
	t.Helper()
	b := mapArtBundles["gi"]
	if len(b.artAllow()) == 0 {
		t.Fatal("gi manifest allow-list empty")
	}
	return b
}

func TestMapArtAllowList(t *testing.T) {
	gi := newMapArtTestBundle(t)
	hl := mapArtBundles["hl"]

	for _, ok := range []struct {
		b    *artBundle
		path string
		want bool
	}{
		{gi, "images/city/3.png", true},
		{gi, "images/city/peak/5.png", true},
		{gi, "images/ImmortalIslandBg.jpg", true},
		{gi, "images/ImmortalIslandBg_peak.jpg", true},
		{gi, "images/city/9.png", false}, // tier beyond the game's
		{gi, "images/city/3.jpg", false}, // wrong extension
		{gi, "images/city/3", false},     // no extension
		{hl, "images/city/1.png", true},
		{hl, "images/city/7.png", true},
		{hl, "images/bgItem/sdzf_bg1.jpg", true},
		{hl, "images/city/3.png", true},
		{hl, "images/ImmortalIslandBg.jpg", false}, // GI art not in HL bundle
		// Battle FX art the page fetches for the burning-city canvas: both
		// bundles ship spine/battle/skeleton as atlas+png.
		{gi, "spine/battle/skeleton.atlas", true},
		// Terrain sheet (ground tiles + union territory hexes) behind the
		// God Island renderer, decoded from the same bundle.
		{gi, "vn/RemoteImmortalIsland_atlas13.png", true},
		{gi, "vn/RemoteImmortalIsland_atlas10.png", true},
		{hl, "vn/RemoteImmortalIsland_atlas13.png", false},
		{gi, "spine/battle/skeleton.png", true},
		{hl, "spine/battle/skeleton.atlas", true},
		{hl, "spine/battle/skeleton.png", true},
		{gi, "../remoteAssets/secret", false},
		{gi, "images/../../etc/passwd", false},
		// Sky Trade (Thương Đồ): cloud backdrop, hull art, goods icons —
		// all from the third (st) bundle; nothing leaks across bundles.
		{mapArtBundles["st"], "images/mapBg/bgpre.jpg", true},
		{mapArtBundles["st"], "images/boat/3.png", true},
		{mapArtBundles["st"], "images/trade/1010.png", true},
		{mapArtBundles["st"], "images/building/10015.png", true},
		{mapArtBundles["st"], "images/city/3.png", false},         // GI art not in ST
		{mapArtBundles["gi"], "images/boat/1.png", false},         // ST art not in GI
		{mapArtBundles["st"], "images/boat/9.png", false},         // hull beyond the game's five
		{mapArtBundles["st"], "images/building/10016.png", false}, // no 16th port
	} {
		if got := ok.b.allowed(ok.path); got != ok.want {
			t.Errorf("allowed(%q) = %v, want %v", ok.path, got, ok.want)
		}
	}
}

func TestMapArtPathSafe(t *testing.T) {
	for p, want := range map[string]bool{
		"images/city/3.png":   true,
		"images/a-b_c/1.PNG":  true,
		"":                    false,
		"/etc/passwd":         false,
		"./x.png":             false,
		"../x.png":            false,
		"images\\city\\1.png": false,
		"images/city/1?.png":  false,
		"images//city/1.png":  true, // double slash still path-clean; allow-list decides
	} {
		if got := mapArtPathSafe(p); got != want {
			t.Errorf("mapArtPathSafe(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestMapArtHandler(t *testing.T) {
	oldFetch := mapArtFetch
	oldCache := mapArtCache
	defer func() { mapArtFetch, mapArtCache = oldFetch, oldCache }()

	calls := 0
	fakePNG := []byte("\x89PNG fake bytes")
	mapArtFetch = func(url string) ([]byte, error) {
		calls++
		if want := mapArtBaseGI + "/images/city/3.png"; url != want {
			t.Errorf("fetched %q, want %q", url, want)
		}
		return fakePNG, nil
	}
	mapArtCache = map[string][]byte{}

	serve := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/events/mapart?"+query, nil)
		w := httptest.NewRecorder()
		(&TeleBot{}).webEventMapArt(w, r, 1, nil)
		return w
	}

	w := serve("b=gi&p=images/city/3.png")
	if w.Code != http.StatusOK || string(w.Body.Bytes()) != string(fakePNG) {
		t.Fatalf("first fetch: %d %q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc == "" {
		t.Error("missing Cache-Control")
	}

	w = serve("b=gi&p=images/city/3.png")
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("cache miss on second hit: code %d, fetches %d", w.Code, calls)
	}

	for _, q := range []string{
		"b=nope&p=images/city/3.png",  // unknown bundle
		"b=gi&p=images/city/9.png",    // not in manifest
		"b=gi&p=images/city/9.jpg",    // wrong ext
		"b=gi&p=..%2F..%2Fsecret.png", // traversal
		"b=gi",                        // missing p
	} {
		w = serve(q)
		if w.Code != http.StatusNotFound {
			t.Errorf("%q: got %d, want 404", q, w.Code)
		}
	}

	mapArtFetch = func(url string) ([]byte, error) { return nil, errors.New("cdn down") }
	delete(mapArtCache, "gi/images/city/2.png")
	w = serve("b=gi&p=images/city/2.png")
	if w.Code != http.StatusBadGateway {
		t.Errorf("cdn failure: got %d, want 502", w.Code)
	}
}

func TestMapArtContentType(t *testing.T) {
	for path, want := range map[string]string{
		"images/city/1.png": "image/png",
		"images/x.jpeg":     "image/jpeg",
		"images/x.jpg":      "image/jpeg",
		// The browser parses atlas text — it must not arrive as bytes.
		"spine/battle/skeleton.atlas": "text/plain; charset=utf-8",
		"images/x":                    "application/octet-stream",
	} {
		if got := mapArtContentType(path); got != want {
			t.Errorf("contentType(%q) = %q, want %q", path, got, want)
		}
	}
}
