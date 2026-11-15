/* appkit.js — shared base for the event mini-apps (kunlun pattern).
   Pages live inside the layout.html shell, which already provides: the SSE
   "map" event bridged to a window 'tytd:map' CustomEvent, the data-live form
   interceptor (fetch + toast, no reload), CSRF, and login. What this kit adds
   is the generic half every board repeats: transform-camera (pan/pinch/wheel/
   dblclick-reset with drag-vs-tap gating), server-clock alignment, the mapsub
   heartbeat, the order form, pop placement, and small canvas/number helpers.
   Served embedded at /webstatic/appkit.js — changing it ships with a new
   binary. Interface frozen after rollout phase P0. */
(function () {
  'use strict';
  var TK = {};

  // ---- server clock -----------------------------------------------------------
  // Every map frame carries f.ts = server Unix seconds; line/march interpolation
  // must run on SERVER time (client clocks drift), so pages use TK.clock().
  var off = 0;
  TK.off = function () { return off; };
  TK.clock = function () { return Date.now() + off; };

  // ---- loaders ----------------------------------------------------------------
  TK.loadImg = function (url) {
    return new Promise(function (res, rej) {
      var im = new Image();
      im.onload = function () { res(im); };
      im.onerror = function () { rej(new Error('img ' + url)); };
      im.src = url;
    });
  };
  // Promise of {url: HTMLImageElement}
  TK.preload = function (urls) {
    return Promise.all(urls.map(function (u) {
      return TK.loadImg(u).then(function (im) { return [u, im]; });
    })).then(function (pairs) {
      var m = {};
      pairs.forEach(function (p) { m[p[0]] = p[1]; });
      return m;
    });
  };
  TK.artURL = function (b, p) { return '/events/mapart?b=' + b + '&p=' + encodeURIComponent(p); };
  TK.fetchJSON = function (url) {
    return fetch(url, { credentials: 'same-origin' }).then(function (r) {
      if (!r.ok) throw new Error(url + ' ' + r.status);
      return r.json();
    });
  };
  TK.fetchScene = function (b) { return TK.fetchJSON(TK.artURL(b, 'scene.json')); };

  // Bake scale: one static bake sized to the world, capped by longest side AND
  // total area (older iOS Safari dies past ~16 Mpx per canvas).
  TK.fitSize = function (w, h, maxSide, maxArea) {
    var k = Math.min(1, maxSide / w, maxSide / h);
    if (maxArea && w * h * k * k > maxArea) k = Math.sqrt(maxArea / (w * h));
    return k;
  };

  // ---- camera (ported from kunlun.html, generalized) ----------------------------
  // stageEl = full-viewport clip; camEl = the transformed world wrapper.
  // opts: {min, max, exclude (CSS selector ignored for taps/drags), change(cb)}.
  // Returns live state {s, tx, ty, dragged} + apply/clamp/zoomAt/reset/onTap.
  TK.cam = function (stageEl, camEl, opts) {
    opts = opts || {};
    var min = opts.min || 1, max = opts.max || 5;
    var excl = opts.exclude || '.tk-pop,.tk-zoom,.tk-mini';
    var api = { s: 1, tx: 0, ty: 0, lastTap: 0, dragged: false, _tap: null, _change: opts.change || null };
    api.apply = function () {
      camEl.style.transform = 'translate(' + api.tx.toFixed(1) + 'px,' + api.ty.toFixed(1) + 'px) scale(' + api.s.toFixed(3) + ')';
      if (api._change) api._change();
    };
    // Per-axis constraint: when the box (scaled) is smaller than the stage
    // it centers (letterbox); when larger it may pan but never uncover the
    // stage. CSS left/top from fitStage/fitStageFill are folded in, so
    // fill-mode boards (box overflows the viewport) rest centered at s=1.
    var axis = function (stage, off, size, t, s) {
      var eff = size * s;
      if (eff <= stage) return (stage - eff) / 2 - off; // center; t ignored
      return Math.max(stage - off - eff, Math.min(-off, t));
    };
    api.clamp = function () {
      var W = stageEl.clientWidth, H = stageEl.clientHeight;
      var ox = parseFloat(camEl.style.left) || 0, oy = parseFloat(camEl.style.top) || 0;
      api.s = Math.min(max, Math.max(min, api.s));
      api.tx = axis(W, ox, camEl.clientWidth, api.tx, api.s);
      api.ty = axis(H, oy, camEl.clientHeight, api.ty, api.s);
    };
    api.zoomAt = function (px, py, factor) {
      var ns = Math.min(max, Math.max(min, api.s * factor)), k = ns / api.s;
      api.tx = px - (px - api.tx) * k;
      api.ty = py - (py - api.ty) * k;
      api.s = ns;
      api.clamp(); api.apply();
    };
    api.reset = function () { api.s = 1; api.tx = 0; api.ty = 0; api.apply(); };
    api.onTap = function (fn) { api._tap = fn; };
    var pts = {}, moved = 0, t0 = 0, pinchD = 0;
    var count = function () { return Object.keys(pts).length; };
    stageEl.addEventListener('pointerdown', function (e) {
      if (excl && e.target.closest(excl)) return;
      if (!count()) { moved = 0; t0 = Date.now(); api.dragged = false; }
      pts[e.pointerId] = { x: e.clientX, y: e.clientY };
      try { stageEl.setPointerCapture(e.pointerId); } catch (err) { }
      if (count() === 2) {
        var p = Object.keys(pts).map(function (kk) { return pts[kk]; });
        pinchD = Math.hypot(p[0].x - p[1].x, p[0].y - p[1].y);
        moved = 99; api.dragged = true;
      }
    });
    stageEl.addEventListener('pointermove', function (e) {
      if (!pts[e.pointerId]) return;
      var p = pts[e.pointerId], dx = e.clientX - p.x, dy = e.clientY - p.y;
      // Advance the stored point: dx/dy must be INCREMENTAL. Reusing the
      // pointerdown position made the map accelerate off the finger.
      p.x = e.clientX; p.y = e.clientY;
      if (count() >= 2) {
        var kk = Object.keys(pts), a = pts[kk[0]], b = pts[kk[1]];
        var d = Math.hypot(a.x - b.x, a.y - b.y);
        var r = stageEl.getBoundingClientRect();
        if (pinchD > 0 && d > 0) api.zoomAt((a.x + b.x) / 2 - r.left, (a.y + b.y) / 2 - r.top, d / pinchD);
        pinchD = d; moved = 99; api.dragged = true;
      } else {
        moved += Math.abs(dx) + Math.abs(dy);
        if (moved > 8) api.dragged = true;
        api.tx += dx; api.ty += dy;
        api.clamp(); api.apply();
      }
    });
    var up = function (e) {
      if (!pts[e.pointerId]) return;
      delete pts[e.pointerId];
      pinchD = 0;
      if (!count() && !api.dragged && Date.now() - t0 < 300) {
        if (Date.now() - api.lastTap < 300) { api.reset(); api.lastTap = 0; }
        else api.lastTap = Date.now();
      }
    };
    stageEl.addEventListener('pointerup', up);
    stageEl.addEventListener('pointercancel', up);
    stageEl.addEventListener('wheel', function (e) {
      e.preventDefault();
      var r = stageEl.getBoundingClientRect();
      api.zoomAt(e.clientX - r.left, e.clientY - r.top, Math.exp(-e.deltaY * 0.002));
      api.dragged = true;
    }, { passive: false });
    stageEl.addEventListener('dblclick', function (e) {
      if (!e.target.closest('.tk-pop')) api.reset();
    });
    stageEl.addEventListener('click', function (e) {
      if (api.dragged || (excl && e.target.closest(excl))) return;
      if (api._tap) api._tap(e);
    });
    return api;
  };

  // Fit a world of the given aspect into the stage (letterbox centered);
  // returns the box px the camera element got.
  TK.fitStage = function (stageEl, camEl, aspect) {
    var W = stageEl.clientWidth, H = stageEl.clientHeight;
    if (!W || !H || !aspect) return null;
    var w = W, h = W / aspect;
    if (h > H) { h = H; w = H * aspect; }
    camEl.style.width = w + 'px';
    camEl.style.height = h + 'px';
    camEl.style.left = ((W - w) / 2).toFixed(1) + 'px';
    camEl.style.top = ((H - h) / 2).toFixed(1) + 'px';
    return { w: w, h: h };
  };

  // Cover-fit: the cam box fills the stage in BOTH directions (extra world
  // overflows symmetrically and is reached by panning) — the game-client
  // feel on phones, where a square-ish board inside the letterboxed box
  // looked tiny. Use on real world maps; schematic boards (ditich) keep
  // fitStage so the whole layout is visible at a glance.
  TK.fitStageFill = function (stageEl, camEl, aspect) {
    var W = stageEl.clientWidth, H = stageEl.clientHeight;
    if (!W || !H || !aspect) return null;
    var w = Math.max(W, H * aspect), h = w / aspect;
    camEl.style.width = w.toFixed(1) + 'px';
    camEl.style.height = h.toFixed(1) + 'px';
    camEl.style.left = ((W - w) / 2).toFixed(1) + 'px';
    camEl.style.top = ((H - h) / 2).toFixed(1) + 'px';
    return { w: w, h: h };
  };

  // Normalized (0..1 on the cam box) point of a pointer event; null off-board.
  TK.normOf = function (e, camEl) {
    var g = camEl.getBoundingClientRect();
    var nx = (e.clientX - g.left) / g.width, ny = (e.clientY - g.top) / g.height;
    if (nx < -0.02 || nx > 1.02 || ny < -0.02 || ny > 1.02) return null;
    return { x: Math.min(1, Math.max(0, nx)), y: Math.min(1, Math.max(0, ny)) };
  };

  // ---- live frames + subscription ----------------------------------------------
  var handlers = [], bound = false;
  TK.mapFrame = function (which, fn) {
    handlers.push({ which: which, fn: fn });
    if (bound) return;
    bound = true;
    window.addEventListener('tytd:map', function (e) {
      var f = e.detail;
      if (!f) return;
      off = (f.ts || 0) * 1000 - Date.now();
      handlers.forEach(function (h) { if (h.which === f.which) h.fn(f); });
    });
  };
  TK.sub = function (which, on) {
    fetch('/events/mapsub?which=' + encodeURIComponent(which) + '&on=' + on, { credentials: 'same-origin' }).catch(function () { });
  };
  // Keeps mapStreamLoop pulling THIS board while the page is open: heartbeat
  // every 8 s (TTL 20 s), release on pagehide, re-sub when tab becomes visible.
  TK.heartbeat = function (which) {
    TK.sub(which, 1);
    setInterval(function () { if (!document.hidden) TK.sub(which, 1); }, 8000);
    window.addEventListener('pagehide', function () { TK.sub(which, 0); });
    document.addEventListener('visibilitychange', function () { TK.sub(which, document.hidden ? 0 : 1); });
  };

  // ---- orders (data-live rides layout.html's fetch+toast interceptor) ----------
  // opts: {csrf, which, act, arg, label, noun, confirm}
  // Orders fire straight on tap (owner 2026-10-19: "bỏ cái alert confirm khi
  // thao tác") — the button press IS the decision; opts.confirm is accepted
  // and ignored so older call sites keep working. Destructive account
  // deletion elsewhere still confirms.
  TK.order = function (opts) {
    var f = document.createElement('form');
    f.method = 'post'; f.action = '/events/order';
    f.setAttribute('data-live', '1');
    var vals = { csrf: opts.csrf, which: opts.which, act: opts.act, arg: String(opts.arg == null ? '' : opts.arg) };
    Object.keys(vals).forEach(function (n) {
      var i = document.createElement('input');
      i.type = 'hidden'; i.name = n; i.value = vals[n];
      f.appendChild(i);
    });
    document.body.appendChild(f);
    if (f.requestSubmit) f.requestSubmit(); else f.submit();
  };

  // ---- HUD helpers ---------------------------------------------------------------
  // Idle / err / live text rules shared by all boards (Vietnamese, game voice).
  TK.status = function (el, f, liveNote) {
    if (!el) return;
    if (f.idle) { el.textContent = 'sự kiện chưa mở'; return; }
    if (f.err) { el.textContent = f.err; return; }
    el.textContent = (liveNote || 'trực tiếp · ') + new Date((f.ts || 0) * 1000).toLocaleTimeString();
  };
  TK.mmss = function (sec) {
    sec = Math.max(0, Math.floor(sec));
    var m = Math.floor(sec / 60), s = sec % 60;
    return m + ':' + (s < 10 ? '0' : '') + s;
  };
  TK.haptic = function (kind) {
    try {
      var wf = window.Telegram && window.Telegram.WebApp;
      if (wf && wf.HapticFeedback) wf.HapticFeedback.impactOccurred(kind || 'light');
    } catch (e) { }
  };
  // Place a popup near a normalized world point under the camera (clamped).
  TK.placePop = function (popEl, stageEl, camEl, nx, ny) {
    var gx = camEl.getBoundingClientRect(), sr = stageEl.getBoundingClientRect();
    var x = gx.left - sr.left + nx * gx.width;
    var y = gx.top - sr.top + ny * gx.height;
    popEl.style.left = Math.min(sr.width * 0.86, Math.max(8, x)).toFixed(1) + 'px';
    popEl.style.top = Math.min(sr.height * 0.75, Math.max(8, y)).toFixed(1) + 'px';
  };
  // Tap hit-test on markers: first the point inside a box (hw,hh normalized),
  // nearest center wins; fallback nearest center within maxD. items: [{x,y,...}].
  TK.nearestBox = function (nx, ny, items, hw, hh, maxD) {
    var best = null, bd = Infinity;
    for (var i = 0; i < items.length; i++) {
      var it = items[i], dx = nx - it.x, dy = ny - it.y;
      if (Math.abs(dx) <= hw && Math.abs(dy) <= hh) {
        var d0 = dx * dx + dy * dy;
        if (d0 < bd) { best = it; bd = d0; }
      }
    }
    if (best) return best;
    for (var j = 0; j < items.length; j++) {
      var it2 = items[j], ax = nx - it2.x, ay = ny - it2.y, d = Math.hypot(ax, ay);
      if (d < bd) { best = it2; bd = d; }
    }
    return (maxD && bd <= maxD) || !maxD ? best : null;
  };

  // ---- canvas bits ----------------------------------------------------------------
  TK.setSize = function (cv, w, h) {
    if (cv.width !== w || cv.height !== h) { cv.width = w; cv.height = h; }
  };
  TK.diamond = function (ctx, cx, cy, hw, hh) {
    ctx.beginPath(); ctx.moveTo(cx, cy - hh); ctx.lineTo(cx + hw, cy);
    ctx.lineTo(cx, cy + hh); ctx.lineTo(cx - hw, cy); ctx.closePath();
  };
  // Draw an image rotated about (x,y) so local +Y points along angle (radians).
  TK.drawRot = function (ctx, im, x, y, angle, k) {
    ctx.save();
    ctx.translate(x, y);
    ctx.rotate(angle - Math.PI / 2);
    ctx.drawImage(im, -im.naturalWidth * k / 2, 0, im.naturalWidth * k, im.naturalHeight * k);
    ctx.restore();
  };

  // ---- game-style target panel ---------------------------------------------
  // The client shows a selected city as a gold-trimmed dark bar: union
  // ribbon badge, white place name, red fight cue, gold pill action buttons.
  // The mini-apps adopt that skin through injected CSS (the doubled .tk-pop
  // class out-ranks the pages' own popup rules, so no page style edits are
  // needed) and TK.popHead builds the header. Only LIVE fields are shown —
  // the pull protocol carries no per-city HP/score, so none are invented.
  TK.popSkin = function () {
    if (document.getElementById('tk-style')) return;
    var s = document.createElement('style');
    s.id = 'tk-style';
    s.textContent = [
      '.tk-pop.tk-pop{background:linear-gradient(180deg,rgba(26,32,46,.98),rgba(9,13,22,.98));' +
      'border:1px solid rgba(202,166,74,.55);border-radius:9px;box-shadow:0 6px 22px rgba(0,0,0,.6);color:#f0e9d8}',
      '.tk-pop .tk-hd{display:flex;align-items:center;gap:7px;margin-bottom:4px}',
      '.tk-pop .tk-ico{width:40px;height:40px;object-fit:contain;flex:0 0 auto;filter:drop-shadow(0 2px 3px rgba(0,0,0,.7))}',
      '.tk-pop .tk-hc{min-width:0;display:flex;align-items:center;gap:6px;flex-wrap:wrap}',
      '.tk-pop .tk-hc b{color:#fff;font-size:14px;text-shadow:0 1px 2px #000}',
      '.tk-pop .tk-rib{font-size:10px;padding:1px 7px;border-radius:3px;' +
      'background:linear-gradient(180deg,#332a15,#1c1708);border:1px solid #caa64a;color:#e8c56d;' +
      'letter-spacing:.4px;max-width:130px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}',
      '.tk-pop .tk-chip{font-size:11px;padding:1px 7px;border-radius:9px;background:rgba(30,42,60,.85);' +
      'border:1px solid #33455e;color:#cfe0f2;white-space:nowrap}',
      '.tk-pop .tk-chip.f{color:#ff7a6b;border-color:#6e2a24;animation:tkpuls 1s infinite}',
      '@keyframes tkpuls{50%{opacity:.35}}',
      '.tk-pop.tk-pop button{background:linear-gradient(180deg,#f3d27c,#c79a35);color:#3b2a08;font-weight:700;' +
      'border:1px solid #8a6a20;border-radius:14px}',
      '.tk-pop.tk-pop button.danger{background:linear-gradient(180deg,#e77a6a,#a3342c);color:#fff2ef;border-color:#6e201a}',
      '.tk-pop.tk-pop button.item{background:linear-gradient(180deg,#f0a95e,#c26f22);color:#3c2005;border-color:#7c4612}'
    ].join('\n');
    document.head.appendChild(s);
  };
  // cfg: {img, name, owner, color, fight, rank, sub} — missing fields skip.
  TK.popHead = function (popEl, cfg) {
    var h = document.createElement('div'); h.className = 'tk-hd';
    if (cfg.img) {
      var im = document.createElement('img'); im.className = 'tk-ico'; im.src = cfg.img; im.alt = '';
      h.appendChild(im);
    }
    var col = document.createElement('div'); col.className = 'tk-hc';
    if (cfg.owner) {
      var r = document.createElement('span'); r.className = 'tk-rib'; r.textContent = cfg.owner;
      if (cfg.color) { r.style.borderColor = cfg.color; r.style.color = cfg.color; }
      col.appendChild(r);
    }
    var n = document.createElement('b'); n.textContent = cfg.name || ''; col.appendChild(n);
    h.appendChild(col);
    if (cfg.rank != null) {
      var rk = document.createElement('span'); rk.className = 'tk-chip';
      rk.textContent = 'Hạng ' + cfg.rank; h.appendChild(rk);
    }
    if (cfg.fight) {
      var f = document.createElement('span'); f.className = 'tk-chip f'; f.textContent = '⚔'; h.appendChild(f);
    }
    popEl.appendChild(h);
    if (cfg.sub) {
      var o = document.createElement('div'); o.className = 'o'; o.textContent = cfg.sub; popEl.appendChild(o);
    }
  };
  TK.popSkin();

  window.TK = TK;
})();
