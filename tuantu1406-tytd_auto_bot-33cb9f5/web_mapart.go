package tytd_go

// Game-art proxy for the streamed event maps. The browser renders the same
// art the game client uses (backgrounds and city sprites), but it never
// talks to the CDN directly: every file is served by this handler from
// GET /events/mapart?b=<bundle>&p=<path>.
//
// The two embedded remoteRes manifests are the ALLOW-LIST: a request only
// resolves if its exact path+extension is a key in that bundle's manifest,
// so "..", traversal tricks, and hotlinking of arbitrary CDN files simply
// 404. The bundle URLs pin the game's current remoteAssets hashes; when a
// game update rotates them the proxy logs the miss and browsers keep their
// cached art (re-pinning is a two-line change to the constants below).
//
// Fetched files are cached in memory for the process lifetime — bounded by
// the manifest key count — and re-served with a day of browser cache, so
// each sprite crosses the server exactly once.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
	"tytd_go/internal/gimap"
	"tytd_go/internal/model"

	_ "embed"
)

//go:embed webmapart/gi.manifest.json
var giArtManifest []byte

//go:embed webmapart/hl.manifest.json
var hlArtManifest []byte

//go:embed webmapart/st.manifest.json
var stArtManifest []byte

// Pinned CDN locations of the three art bundles (remoteAssets/<bundle>/<hash>).
const (
	mapArtBaseGI = "https://cdn-xddq-vn.hwrescdn.com/71004001/remoteAssets/remoteImmortalIsland/906e890b97"
	mapArtBaseHL = "https://cdn-xddq-vn.hwrescdn.com/71004001/remoteAssets/remoteHolyLand/139eaef310"
	mapArtBaseST = "https://cdn-xddq-vn.hwrescdn.com/71004001/remoteAssets/remoteSkyTrade/213788a9e5"

	mapArtFetchTimeout = 15 * time.Second
	mapArtMaxBytes     = 8 << 20 // every manifest entry is under 1 MB; generous cap
)

// artBundle is one proxied asset set: pinned base URL + manifest allow-list.
type artBundle struct {
	base     string
	manifest []byte

	once  sync.Once
	allow map[string]map[string]bool // extension-less path → allowed extensions
}

var mapArtBundles = map[string]*artBundle{
	"gi": {base: mapArtBaseGI, manifest: giArtManifest},
	"hl": {base: mapArtBaseHL, manifest: hlArtManifest},
	"st": {base: mapArtBaseST, manifest: stArtManifest},
}

// artAllow parses (once) the embedded manifest into the path→exts allow-list.
// Manifest shape: {"version":N, "<path>":[type,[w,h],[],[".ext",…],[size]],…};
// anything malformed is skipped, never fatal — a broken parse just means that
// key 404s.
func (b *artBundle) artAllow() map[string]map[string]bool {
	b.once.Do(func() {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b.manifest, &raw); err != nil {
			log.Printf("[web] mapart manifest parse: %v", err)
			b.allow = nil
			return
		}
		allow := map[string]map[string]bool{}
		for path, v := range raw {
			var entry []json.RawMessage
			if json.Unmarshal(v, &entry) != nil || len(entry) < 4 {
				continue
			}
			var exts []string
			if json.Unmarshal(entry[3], &exts) != nil || len(exts) == 0 {
				continue
			}
			set := make(map[string]bool, len(exts))
			for _, e := range exts {
				set[e] = true
			}
			allow[path] = set
		}
		b.allow = allow
	})
	return b.allow
}

// allowed reports whether path (with extension) is listed in the bundle.
func (b *artBundle) allowed(path string) bool {
	ext := ""
	for i := len(path) - 1; i >= 0 && path[i] != '/'; i-- {
		if path[i] == '.' {
			ext = path[i:]
			path = path[:i]
			break
		}
	}
	if ext == "" {
		return false
	}
	return b.artAllow()[path][ext]
}

// mapArtBytesCap bounds the proxied CDN cache. Assets are large (spine
// sheets ~2–6 MB) and were kept forever — one viewer per bundle could walk
// the whole art set and pin tens of MB on the 512 MB VPS (audit 2026-10-06).
// FIFO by first-store: with minute-scale page reuse that already beats LRU's
// complexity, and eviction only ever costs a CDN re-fetch.
const mapArtBytesCap = 24 << 20

var (
	mapArtMu    sync.Mutex
	mapArtCache = map[string][]byte{}
	mapArtOrder []string // insertion order for FIFO eviction
	mapArtBytes int
	// mapArtFetch is the CDN reader; tests swap it to stay offline.
	mapArtFetch = mapArtHTTPFetch
)

func mapArtHTTPFetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: mapArtFetchTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cdn status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, mapArtMaxBytes+1))
}

// mapArtGet returns one allow-listed asset, fetching it from the CDN once.
func mapArtGet(bundleKey, path string) ([]byte, error) {
	key := bundleKey + "/" + path
	mapArtMu.Lock()
	if b, ok := mapArtCache[key]; ok {
		mapArtMu.Unlock()
		return b, nil
	}
	mapArtMu.Unlock()

	b, err := mapArtFetch(mapArtBundles[bundleKey].base + "/" + path)
	if err != nil {
		return nil, err
	}
	if len(b) > mapArtMaxBytes {
		return nil, fmt.Errorf("cdn asset too large: %d bytes", len(b))
	}
	mapArtMu.Lock()
	if old, dup := mapArtCache[key]; dup {
		mapArtBytes -= len(old)
	} else {
		mapArtOrder = append(mapArtOrder, key)
	}
	mapArtCache[key] = b
	mapArtBytes += len(b)
	for mapArtBytes > mapArtBytesCap && len(mapArtOrder) > 0 {
		oldest := mapArtOrder[0]
		mapArtOrder = mapArtOrder[1:]
		if old, ok := mapArtCache[oldest]; ok {
			mapArtBytes -= len(old)
			delete(mapArtCache, oldest)
		}
	}
	mapArtMu.Unlock()
	return b, nil
}

func mapArtContentType(path string) string {
	switch {
	case len(path) >= 4 && path[len(path)-4:] == ".png":
		return "image/png"
	case len(path) >= 4 && (path[len(path)-4:] == ".jpg" || path[len(path)-5:] == ".jpeg"):
		return "image/jpeg"
	case len(path) >= 6 && path[len(path)-6:] == ".atlas":
		// Spine atlas text — the browser parses it, so it must arrive as text.
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// mapArtPathSafe rejects anything that is not a clean relative asset path.
// (The manifest allow-list already refuses unknown keys; this keeps obviously
// hostile input out of logs and the fetch URL.)
func mapArtPathSafe(p string) bool {
	if p == "" || len(p) > 128 {
		return false
	}
	if p[0] == '/' || p[0] == '.' {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '.' || c == '/' || c == '-' || c == '_' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

// kuStaticAsset resolves one static Kunlun mini-app asset from the embeds:
// the scene bundle JSON, the sprite sheet, the map background, or any loose
// kunlunart png (the five line sprites + the minimap). The allow-list is the
// embed itself — a name not shipped in kunlunart/ simply 404s. Returns nil
// for anything else.
func kuStaticAsset(path string) ([]byte, string) {
	if !mapArtPathSafe(path) {
		return nil, ""
	}
	switch path {
	case "scene.json":
		if b := gimap.KunlunSceneJSON(); b != nil {
			return b, "application/json"
		}
	case "sheet.png":
		return gimap.KuSheetPNG(), "image/png"
	case "bg.jpg":
		return gimap.KuBgJPEG(), "image/jpeg"
	}
	if b, ok := gimap.KuArtFile(path); ok {
		return b, mapArtContentType(path)
	}
	return nil, ""
}

// SERVER-RAM POLICY (user, 2026-10-06: "client ăn bao nhiêu ram cũng được
// nhưng server là phải tối ưu nhất"): the kunlun WEB surface renders NOTHING
// server-side. There used to be a live.png arm here (compositor PNG per chat,
// 30 s cache) plus webKunlunMap's GET render — both decoded/pinned the 19 MB
// sprite sheet on a page view. The /kunlun mini app bakes the identical board
// in the browser from scene.json + sheet art, so /events/map?which=kunlun
// redirects there and mapart serves only immutable embeds. The Go compositor
// (gimap.RenderKunlun) now runs exclusively for the on-demand /kunlun_map
// Telegram photo — one draw per explicit command, never per page view.

// bmStaticAsset resolves one static Đỉnh Bắc Minh mini-app asset from the
// embeds (the twin of kuStaticAsset): scene bundle, north sheet, north bg,
// or a loose bacminhart png — the embed is the allow-list.
func bmStaticAsset(path string) ([]byte, string) {
	if !mapArtPathSafe(path) {
		return nil, ""
	}
	switch path {
	case "scene.json":
		if b := gimap.BacMinhSceneJSON(); b != nil {
			return b, "application/json"
		}
	case "sheet.png":
		return gimap.BmSheetPNG(), "image/png"
	case "bg.jpg":
		return gimap.BmBgJPEG(), "image/jpeg"
	}
	if b, ok := gimap.BmArtFile(path); ok {
		return b, mapArtContentType(path)
	}
	return nil, ""
}

// serveSceneJSON writes one memoised mini-app scene bundle with a
// REVALIDATING cache (ETag + no-cache) instead of the old blind 24 h
// max-age. The 2026-11-03 Bắc Minh cầu-ghost fix rotates scene.json with the
// binary — a day of blind cache would have pinned phones to the OLD bake
// across the rebuild (the user reported the same duplicate twice for exactly
// this reason). The fingerprint is one sha256 over ≤1 MB of already-memoised
// bytes per page open, and an unchanged binary answers 304 in microseconds.
// serveEmbedArt is the serveSceneJSON contract for every OTHER embedded
// mini-app asset (sheet.png, bg.jpg, base.png, loose pack/line art): these
// rotate with the binary too, so a blind immutable year pinned phones to the
// OLD sheet whenever a bake moved the shelf-pack rects — the 2026-11-04 Bắc
// Minh castle-swap report ("map vẽ sai tùm lum") was exactly this: new
// scene.json (revalidating) blitted through yesterday's cached sheet rects.
// ETag + no-cache: unchanged binary answers 304 in microseconds; a rebuild
// re-sends the new bytes on the next open.
func serveEmbedArt(w http.ResponseWriter, r *http.Request, data []byte, ctype string) {
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

func serveSceneJSON(w http.ResponseWriter, r *http.Request, data []byte) {
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

// webEventMapArt serves GET /events/mapart?b=gi|hl&p=images/city/3.png.
// Plain-text Vietnamese statuses only, never a redirect: <img> tags follow
// those and the page would silently show something else.
func (t *TeleBot) webEventMapArt(w http.ResponseWriter, r *http.Request, chatID int64, u *model.User) {
	bundleKey := r.URL.Query().Get("b")
	path := r.URL.Query().Get("p")
	if bundleKey == "bm" {
		// Đỉnh Bắc Minh: same embed-only contract as ku (SERVER-RAM policy —
		// no CDN bundle, no server render; day-cache the scene JSON,
		// immutable year the art that rotates only with a rebuild).
		data, ctype := bmStaticAsset(path)
		if data == nil {
			http.Error(w, "không tìm thấy ảnh", http.StatusNotFound)
			return
		}
		if path == "scene.json" {
			serveSceneJSON(w, r, data)
			return
		}
		serveEmbedArt(w, r, data, ctype)
		return
	}
	if bundleKey == "ku" {
		// Kunlun has no CDN bundle and no live render either (see the
		// SERVER-RAM POLICY note above): the static scene files (scene.json +
		// the embedded sheet/bg/line art) rotate with a bot rebuild, so every
		// one of them ships ETag-revalidating (serveEmbedArt) — an immutable
		// year here is what pinned phones to stale sheet rects after the
		// 2026-11-04 north bake (see serveEmbedArt).
		data, ctype := kuStaticAsset(path)
		if data == nil {
			http.Error(w, "không tìm thấy ảnh", http.StatusNotFound)
			return
		}
		if path == "scene.json" {
			serveSceneJSON(w, r, data)
			return
		}
		serveEmbedArt(w, r, data, ctype)
		return
	}
	if bundleKey == "lh" {
		// Luân Hồi Bí Cảnh bracket: embed-only bundle (SERVER-RAM policy,
		// same contract as ku/bm): scene.json + bg.jpg + base.png rotate with
		// the binary only.
		if path == "scene.json" {
			data, err := gimap.LuanHoiSceneJSON()
			if err != nil {
				log.Printf("[web] mapart lh/scene: %v", err)
				http.Error(w, "không tải được nền bản đồ", http.StatusBadGateway)
				return
			}
			serveSceneJSON(w, r, data)
			return
		}
		if data, ok := gimap.LuanHoiArt(path); ok {
			serveEmbedArt(w, r, data, mapArtContentType(path))
			return
		}
		http.Error(w, "không tìm thấy ảnh", http.StatusNotFound)
		return
	}
	if bundleKey == "hl" {
		// Cửu U mini app: the scene bundle is the Go compositor's own inputs
		// (embedded pack), so it rotates with the binary exactly like ku's —
		// day-cache for scene.json, immutable year for pack files. Anything
		// else (the old card's city icons) falls through to the CDN bundle
		// untouched.
		if path == "scene.json" {
			data, err := gimap.HolyLandSceneJSON()
			if err != nil {
				log.Printf("[web] mapart hl/scene: %v", err)
				http.Error(w, "không tải được nền bản đồ", http.StatusBadGateway)
				return
			}
			serveSceneJSON(w, r, data)
			return
		}
		if data, ok := gimap.HLPackFile(path); ok {
			serveEmbedArt(w, r, data, mapArtContentType(path))
			return
		}
	}
	bundle, ok := mapArtBundles[bundleKey]
	if !ok {
		http.Error(w, "không tìm thấy ảnh", http.StatusNotFound)
		return
	}
	if !mapArtPathSafe(path) || !bundle.allowed(path) {
		http.Error(w, "không tìm thấy ảnh", http.StatusNotFound)
		return
	}
	data, err := mapArtGet(bundleKey, path)
	if err != nil {
		log.Printf("[web] mapart %s/%s: %v", bundleKey, path, err)
		http.Error(w, "không lấy được ảnh từ máy chủ game", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", mapArtContentType(path))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}
