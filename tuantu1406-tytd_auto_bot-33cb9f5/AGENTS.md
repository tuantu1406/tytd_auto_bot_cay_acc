# AGENTS.md — how to add a new event to this bot

Playbook distilled from shipping Côn Lôn (type 199) and its parallel port Đỉnh Bắc
Minh (type 332). When the user asks for "handler for event X", work through these
phases in order. Do not skip the discovery phase even if the event "looks like" an
existing one — two events sharing an engine can still differ in grid, items, and
cost rules, and guessing here is what burns whole sessions later.

## 0. Ground rules (violating these = rework)
- **WELFARE PARITY LAW (owner 2026-11-13: "event nào cũng phải auto phát/nhận
  cây, nhận thưởng bang, mua gói…")** — EVERY event auto-engine ships with the
  same welfare baseline; a new engine that only plays the map is INCOMPLETE.
  Per activity cycle (the 10-min "tree cadence" idiom):
  1. **Phát/nhận cây bang** — donate the event's union-tree items (ids folded
     from the event's own `…_ITEM_DONATE`/`…_ITEM_ENERGY` param, owner must have
     named them free-to-spend — else they fall under the item ban) num=1 per
     press to server refusal, then CLAIM ready notices with the **claim-all
     press, id=0 forced present on the wire** (proto3 drops Id:0 = server
     SILENT — append `10 00` after marshal; proven on rbt 217222 & WeYound
     218622), per-notice ids only as fallback.
  2. **Nhận thưởng bang / mốc nạp-bang** — fold the 21003 `conditionConfig`
     rows and sweep BLIND: per-id generic 21045 for union-recharge rungs
     (type 84 or conditionId ≥ 20000) + ONE 21033 per other distinct task type.
     NEVER gate on the stored condition values (live-proven stale zeros,
     rbt 2026-10-09) — the server settles only genuinely-ready rungs and
     claiming is idempotent; answers ride the shared `stChans` seats.
  3. **Mua gói shop** — generic 21003 table + 21005 buys gated SOLELY by the
     `rbtMallAuto` shape: FREE (price "0"/"=0"), AD (param "3|"), TIÊN NGỌC
     ("100000=N") rows only, to daily cap; money rows are structurally never
     sent (this needs NO owner ask — it never spends real money).
  4. **Quà bang / worship / floor boxes / free receives** — everything the
     client's panel claims with zero cost, claimed every cycle.
  5. **Hỗ trợ / assist poll** — if the event carries an assist board (mates
     calling for help after a failed monster hit, trapped players), poll it on
     a fast clock (WeYound: 2 min) and answer every call for free —
     `wyAssistCycle` (218608 poll → 218607 batch rescue; client JS shows
     monster-help = just attacking the monster, so the monster-sweep covers
     that half).
  Reference implementations, copy the shape not the ids: WeYound
  (`wyWaterTree`/`wyBonusItemAll`/`wyCondSweep`/`wyShopSweep`/`wyGiftCycle`,
  player_weyound.go), Luân Hồi (`rbtLiquidGrant`/`rbtCondSweep`/`rbtMallSweep`,
  player_reborntrial*.go), Thương Minh (`swDonateLiquid`, player_event_seawar).
- **All game times are UTC+7** (Vietnam). Convert with `ictZone` before ANY hour/day check.
- **Item ban is PERMANENT**: the bot never spends game items. Only user-named,
  button/command-only exceptions exist (see `tytd-item-ban` memory + code comments).
  Auto loops stay free (e.g. `useItem=false` revives, free claims). Ask before ANY
  new item op; when authorized, record the capture, never an invented item id.
- **Never run live game tests unprompted.** Use the authorized test account only
  when the user says so. Never touch the user's `main_test` on `:80`; NEVER restart
  the bot mid-round (the user may be playing live). debugweb runs ONLY from
  `/tmp/dbgwork` with `TYTD_DEBUG_CHAT=787613537 WEB_PORT=8091` (page checks need
  no game login; add `TYTD_DEBUG_LOGIN="user|pass|serverID"` only for live tests).
- **Server RAM policy**: client (browser) RAM is expendable, server is minimal.
  The browser bakes maps; the server renders only on explicit commands. Release
  decoded pixels after every server render (see `kuSheetPack.releasePixels`).
- Chromium headless is broken in this sandbox — canvas pixels can ONLY be
  verified on the user's phone. Everything else you verify yourself, desktop-side.
- Report to the user in **Vietnamese**, honestly (502 without pullers is a PASS
  for wiring, say so).

## 1. Discovery (before writing any Go)
1. **activityType**: log in the test acc via debugweb/tests, decode a live
   `PushActivityList`, find the row's `type`. Keep a `const activityTypeX = NNN`.
2. **Opcodes**: if the event ships in the same remote bundle as an existing one,
   its scene ops are probably VERBATIM (server routes by `activityId` inside the
   body — nil body = silent timeout trap). A missing opcode can be DERIVED: the
   request/answer pair always differs by exactly 200000 (e.g. 219918 → "4dce",
   cmd hex = base + opcode). Pin derivations in a unit test, never ship one blind.
3. **Geometry**: pull the 219919-equivalent once → `maxPos` tells the grid
   (n²). Anchor: fit the client's `base[]` affine with least-squares (kunlun
   [6527,107,63], bacminh [12733,107,63]) — half-tile 107/63 is engine-stable,
   A0 scales with grid. Assert fit error = 0.
4. **Items/costs**: read the type's 21003 paramConfig (`*_ACCEL_ITEM`,
   `*_ITEM_REVIVE`, costs). Cost models differ per event (kunlun accel = gems,
   north = item-first-then-gems). Ask the user per item op — they decide, you wire.
5. **Whole-map pull pairing**: the minimap request REQUIRES `floor` AND `group`
   together — floor-only answers a silently EMPTY board (cities=0, cells=0 while
   the bracket really had 12 unions; bitten on kunlun, avoided on bacminh only
   because the lesson was written down). Learn them via EnterActivity first and
   always send both.
6. **ret codes are observed, never guessed**: some boards gate scene ops by
   residency (Holy Land `ret=9505`), some don't (kunlun 219919 answers cold);
   `19944` = unreachable target, `101`/`19922`-style refusals are costless and
   expected. Probe a handful of ops live (read-only + at most one authorized
   press) and record the meanings in comments.
7. Live-capture anything ambiguous (user can paste packets; decode varints with
   python before trusting them; byte-pin captures in tests).

## 2. Transport invariants (the part that bites)
- Two events answering on the SAME hexes ⇒ one account can seat ONE such scene at
  a time. Mirror the **warKind pattern**: a `warKind`/`warKindEP` flag on
  Player/EventPlayer that MUST flip **before the request write** (inside the
  RoundTrip choke point), never after success — the in-flight answer rides the
  shared hex and would route to the other board's seats. Add cross-seat refusal
  guards. Lock order: define one direction (BmMu→KuMu) and never reverse it.
- Reader dispatch: collapse the shared server-hex case list ONCE and branch on the
  flag (`bmDeliver` vs ku switch) — do not duplicate 17 cases.
- Request frames: cmd = `data[6:10]`, payload = `data[18:]`. Wire tests: bind ctx
  (`wireCtx`/`wireCtxEP` helpers or bare `p.ctx==nil` silently DROPS frames and
  the test hangs), capture via `writeChan`.
- On EVENT sockets the answer of a REQUEST arrives as a push-style frame — the
  reader needs an explicit `<ev>DeliverFrame` case per response type; a missing
  case looks exactly like "server refused" (timeout), but it was our dispatch.
- Events-only fleets exist: every online/eligibility flag must OR in the
  EventPlayer session too (a kunlun card stayed invisible for a day because
  `onlineKu` looked only at *Player fleets).

## 3. State layers (copy → rename → re-verify, never "generalize the gate")
- Leader: `player_<ev>.go` (state struct + seats per response msgType + RoundTrip
  + fold handlers + Status). Init every map field in the constructor (nil-map
  panics have happened).
- Auto-play: `player_event_<ev>.go` (ensureScene, follow/move/explore/sweep,
  tree/heartbeats, rebirth loop FREE-only, manual authorized ops take a flag).
- Status text must degrade when Cfg/phase never arrived (window fallback).
- PHASES are client-computed from the activity's config sync (kunlun `kuPhase`:
  beginTime in HOURS after activity begin, gap = prepare, settle = lastEnd+300s).
  If the server never sends the uniqueConfig for the new type, show "—" — do not
  fabricate phase from the wall clock alone.
- March/route ledgers: arrival timing is SERVER ms (fold the `ts` clock offset);
  merge per-account ledgers with newest-departure-wins and prune expired rows,
  or the page shows ghost marches forever.

## 4. Renderer (gimap) — parameterize, do not fork
- Bundle all mutable compositor state into a pack struct (`kuSheetPack` pattern:
  sheet/manifest/bg + once/mutexes/caches/scratch), pass pack+layout pointers into
  `renderWar/warFrame/warStatic/buildWarScene`. The second board = a second pack.
  Keep the first board's output byte-stable (its suite must stay green untouched).
- New board embeds: `<ev>_map.json` + `_sprites.png/.json` + `_bg.jpg` +
  `<ev>art/` — bake via persisted tools in `tools/` (validate the tool output
  against the known-good board first, e.g. bacminh_bake.py vs kunlun).
- Stream: add the `Stream<Ev>` const + `StreamOf`/`StreamStatic` arms.
- Write a desktop **pagebake test** that replays the PAGE's exact JS formulas in
  Go and writes a PNG you Read (inspect visually) + numeric geometry pins. This
  is how north geometry passed first try on the phone.

## 4b. Map art pipeline — "xịn" means the game's OWN pixels, not yours
The map looks right ONLY when it is drawn from the client's own assets with the
client's own geometry rules. Never hand-draw tiles, never invent palettes.
1. **Sources**: decoded bundle at `/workspace/real-game-data/engine/<remote>/`
   (+ its `*.decoded.proto` next to it). FGUI v6 binaries parse with
   `/workspace/remoteHolyLand/tools/parse_fgui.py <dir> <out.json>`; cities often
   live in atlas9; some loose PNGs (decoration168/169-style) sit in `images/map/`
   and must be cropped in manually. Copy bg/smallmap/line-art straight from the
   bundle (`bmzd_mapbg.jpg` / `*_smallmap.png`) — never recreate.
2. **Sheet + manifest**: crop every name the map JSON actually references into one
   `<ev>_sprites.png` + `.json` `{name:[x,y,w,h]}` (pack tools in `tools/`). Only
   pack names the json uses. If the engine draws via spine and an imgName has no
   FGUI art, alias per the client's init*City type-mapping (north precedent:
   city5→city3, city10→cityUp6, city13→citydown6, case fix citydown5→cityDown5)
   and say so honestly in a comment.
3. **Map json schema** (kunlun_map.json shape): `col,row,tileW,tileH,tiles,cities,
   Water,Floor,Block,Object,Cloud,anchor`. Tiles entry format mirrors the client's
   initObjMap string: `gid|x|y|imgName|flipX|flipY|typeStr|w|h`. gid: 0 void /
   1 event / 2 city / 3 obstacle / 4 bridge — passable set comes from the board,
   and cities may legally stand on obstacle gids on some boards (north: 2/3/4 vs
   kunlun 1/2) — assert it in a test, do not assume.
4. **GEOMETRY RULES (official, phone-verified, do not re-litigate):**
   `getPosByCoordinate` = diamond CENTER-X / **TOP-Y**; cell rect `[px±hw]×[py,
   py+2·hh]`, cell CENTRE `(px, py+hh)` with hw/hh = 107/63; GROUND plates, tints,
   line ends, feet, fight dots paint at the CENTRE (py+hh); RAISED sprites
   (decor/city) hang UP from the anchor — bottom edge ON py, `top = py − h`
   (client `position.y = data.y − height/2`); depth-sort by py; hit-test inverse
   `v = world_y/hh − 1`. Anchor affine `px = A0 + 107(tx−ty)`, `py = 63(tx+ty)`,
   A0 fit from base[] (error must be 0). posIndex = ROW-MAJOR `y·col+x`.
5. **Union colors**: first appearance in the client's CITY order → palette slot
   `% 12` (same 12 colors across boards). Road/tile tint ownership compares by
   `a.ci>=0 && a.ci===b.ci`, never by color string (slots collide).
6. **City names**: client localization only — extract `KUNLUNWAR_CITY_NAME_*` /
   the board's equivalent from `LanguageWordDB.asset.json` (nested TextAsset:
   `top[5][0][2]`) into `internal/gimap/event_names.json`. Schematic "Thành %d"
   is the honest fallback, never Google'd names.
7. **Compositor gotchas that lie to you**: `png.Decode` of color-type-6 gives
   NRGBA — convert via draw.Src (never a scaler); `image.RGBA.SubImage.Pix` starts
   AT the sub-rect Min (use `PixOffset`, and origin-zero the flip copies); faded
   decor = 8-bit premultiplied cache (`uint8(v*fade/255)`), cached per
   (name,flip,fade); downscale via integer box-average, CatmullRom only for upscale;
   in the page-bake Go port use NRGBA channels — `cc.RGBA()` 16-bit premultiplied
   produced rainbow noise; tint diamonds must ALPHA-BLEND (`(dst>>8)*(255-n)` in
   parens), not overwrite — overwriting masked an alignment bug once.
8. **Verification without a phone**: port the page's exact bake formulas to Go
   (`<ev>_pagebake_test.go`), render /tmp PNG, LOOK at it with the Read tool +
   numeric pins (centre city near middle, all tints land on passable gids). The
   client's own `*_smallmap.png` is the silhouette oracle. If art looks shifted on
   a screenshot, re-run the bake port FIRST — a phone screenshot alone is not
   evidence (a pure vertical 126px error also "matches" when dragged diagonally).
9. **RAM**: decode the sheet lazily (first real blit; scene.json needs rect SIZES
   only — png.DecodeConfig), releasePixels after every server render, reuse one
   scratch canvas+encoder, cache-prune live PNGs. Browser bakes the rest.
10. **Text on a zoomable canvas is a SEPARATE screen-space layer** — baked text
    becomes mush at zoom 5×; draw every label/nameplate at `fontSize·camScale`
    in a stage-sized canvas each frame (kunlun `kuTx` pattern). And keep the page
    hit-test inverse, the Go solve, and the scene export ALL derived from the same
    center rule — they once disagreed by half a row because three places each had
    their own copy of the math.

## 5. Web surface (every line the old board touched, the new one must too)
- Route `/ev` (page no-store) + `evAppURL` + dashboard button. `webtmpl/<page>.html`
  auto-registers as a page name (skip layout/login) — the handler is `webRender`.
- **SSE reality**: `/events/mapsub?which=X&on=1` only REGISTERS interest — frames
  ride the ONE shared `/sse` hub and the pump gate checks that the chat actually
  holds `/sse` open ("no SSE connection for this chat" in the log means the page,
  not the wiring, is missing). Page self-diagnosis: status line from the last
  frame + "mất khung hình Ns" watchdog — when the board "looks wrong", make the
  user read it to you.
- **Seat lifecycle**: the mapstream tick doubles as the want-signal — first tick
  seats the puller (EnterGame) and keeps a 15s heartbeat so PUSH marches (4de7)
  flow; page closed 60s ⇒ Leave + loop retires. "Chỉ thấy mình tôi trên map" is
  almost always an unseated feed, not a server problem.
- `/events/mapart?b=<abbr>` embed arm (scene.json day-cache, art year-immutable);
  `/events/mapstatic?which=<ev>` (col from JSON — NEVER hardcode the grid);
  `/events/mapview?w=<abbr>` = plumbing dispatch in the kunlun handler head;
  `/events/map?which=<ev>` → 302 to the mini app (RAM policy).
- `mapstream.go`: which + candidates (seated-first, leader LAST as fallback) +
  payload arm + route-ledger merge + own-cell overlay + `playerEventOpen` +
  `mapStreamerEligible`.
- `webtmpl/layout.html`: **add the path to the chrome-strip regex** — forgetting
  it gives the page a footer bar (real bug: bacminh shipped with one).
- events.html card (`data-which`, GI_NAMES, both engine JS branches, GI_WHICHS) —
  its inline `<script>` at line 16 breaks the awk JS-extract idiom; use line ranges.
- Page HTML: generate from the sibling page with assert-all-matched python
  replacements (bacminh.html pattern), then `node --check` the extracted script.
- Order endpoint: one `webEvOrder` per event with its OWN passability guard
  (`EvCellPassable`) + Vietnamese refusal messages; broadcast = "N acc sự kiện".
- Buttons obey the item ban; press-thru vs confirm per user directive (2026-10-07:
  tangtoc + anqua press straight; item-revive keeps its confirm).

## 6. Tests you must have (green before any ship)
- Grid/cities/anchor + click-solve roundtrip of every passable cell + stream frame
  + pixels-released-after-render assertion (OOM directive) — `internal/gimap`.
- Wire: follow→EnterGame→Move (flag flipped mid-flight asserted), seat-exclusion
  both ways, minimap floor+group pairing, FREE-probe loops, authorized-op EXACT
  bytes vs the user's capture, view-window sampler (clamp/sample/row-major/
  p>0 guard), pagebake.
- HUD-acts order test: POST the form on a running session and assert the FRAME
  cmd + exact body fields + the Vietnamese flash (refusal must emit NO frame).
- Root suite must run as `go test -tags sqlcipher -count=1 .` (NOT `./...` —
  cmd/ has a pre-existing "main redeclared" landmine). ~40 s is normal, and the
  `sqlite3.c … const qualifier` warning from cgo is harmless noise.
- LIVE-TEST TAG (owner 2026-11-10): every `*_live_test.go` + dev probe carries
  `//go:build live` — the plain root suite EXCLUDES them (compile-faster, no
  env-skipped noise). Run a live test with BOTH tags:
  `TYTD_LIVE=1 … go test -tags "sqlcipher live" -count=1 -run TestX -v .`
  Compile-check live files with `go vet -tags "sqlcipher live" .`

## 7. Verify + ship (this exact idiom)
```bash
export PATH="/root/.asdf/installs/golang/1.24.4/go/bin:$PATH"
env -u GOROOT -u GOPATH -u GOBIN -u GOTOOLCHAIN go vet -tags sqlcipher .
env -u GOROOT -u GOPATH -u GOBIN -u GOTOOLCHAIN go test -tags sqlcipher -count=1 .
# debugweb (rebuild first — webtmpl is go:embed ⇒ FULL binary rebuild for ANY page change):
cd /tmp/dbgwork && TYTD_DEBUG_CHAT=787613537 WEB_PORT=8091 nohup ./debugweb > log &
# curl battery: page 200+no-store, mapart all files, mapstatic (col), mapsub ok,
# mapview honest 502 w/o pullers, order POSTs (guard refusal + accepted msg), 302 arms.
env -u GOROOT -u GOPATH -u GOBIN -u GOTOOLCHAIN GOOS=linux GOARCH=amd64 \
  CC=x86_64-linux-gnu-gcc CGO_ENABLED=1 make build_all build_test   # 11 binaries
```
- `node --check` every touched page's JS (awk line-range extract around `<script>`).
- Quote URLs (`?`) in zsh. Bash cwd persists between calls.
- POSTs carry `csrf` from the page (KU_CSRF/`{{.CSRF}}`); the debugweb session's
  csrf is empty so plain curl POSTs pass there — production browsers get the
  token from the served page (never hardcode it).
- Update memory: a `tytd-<ev>-port.md` (type id, geometry, captures + authorized
  item ids, invariants, verified-vs-phone-pending split) + MEMORY.md index line;
  if the user authorized any new item op, the exception list in
  `tytd-item-ban.md` (and its MEMORY.md hook line) must gain it, WITH the capture.
- Tell the user plainly what still needs their phone (canvas pixels, live orders)
  and check whether the event window is still open before proposing live tests.

## 8. Known gotchas (all learned the hard way)
- Shared-answer boards: seat-routing bugs are invisible locally, visible in-game.
- PNG color-type-6 decodes to NRGBA — convert with draw.Src, never CatmullRom.
- iOS blanks canvases silently over ~10.5Mpx — one fit function, every bake.
- rAF redraw of bake-sized canvases = phone jitter: idle-skip unless animating.
- Test account auto-play loop runs when debugweb logs in — expect yard/spin logs,
  that's normal (it's a copy of the DB in /tmp, not production).
- `debugf`-gated per-event tracing; keep hot paths quiet.
- `cells=0` right after a battle window = SERVER reset the board (cities still
  there) — not a bug; and "0 đoàn" on the board honestly means no event account
  is seated — say that, don't "fix" it.
- Item-id hygiene: numeric item ids live ONLY in Go comments/tests — the
  customer-facing AI prompt and help text must stay id-free (user request).
- NEVER cap an item EFFECT by a guessed value (v2 ate `10/5=2` fruits and the
  north healed less → customer bug "cắn có 2 quả trong khi túi vẫn còn").
  LEARN the effect from the verdict (ΔEnergy/num) and chase the target with a
  BOUNDED loop; and trust the ledger: reward strings pay the same id on EVERY
  pair across all delimiters and welfare receipts pay fruit too — sum them all
  or the clamp fires early.
