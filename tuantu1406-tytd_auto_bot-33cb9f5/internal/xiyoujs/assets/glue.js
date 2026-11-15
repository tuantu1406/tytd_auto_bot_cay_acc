// Glue loaded INSIDE the goja runtime. Reimplements the node harness.js
// loader without fs/readline: everything (browserify registry, ext
// factories, DB TextAssets) is handed in by the Go side as strings.
// Go then calls xiyouSimOnce(roleObj, stageId, seed, timeLimit).
(function () {
  'use strict';
  globalThis.console = { log: function () {}, error: function () {}, warn: function () {} };
  globalThis.Logger = { log: function () {}, error: function () {}, warn: function () {} };
  globalThis.cc = { _RF: { push: function () {}, pop: function () {}, peek: function () { return {}; } } };
  globalThis.window = globalThis;
  globalThis.self = globalThis;

  // ── BigNumber (UMD) ──
  var moduleObj = { exports: {} };
  (function (module, exports) {
    // eslint-disable-next-line no-new-func
    new Function('module', 'exports', __BIGNUMBER_SRC)(moduleObj, moduleObj.exports);
  })();
  globalThis.BigNumber = moduleObj.exports.BigNumber || moduleObj.exports;

  // ── browserify registry ──
  var registry = eval('({' + __REGISTRY_SLICE + '})');
  var extSources = {
    LogicDBMgr: __EXT_LOGICDBMGR,
    StringUtil: __EXT_STRINGUTIL,
  };
  var extCache = {};
  function loadExternal(base) {
    if (extCache[base]) return extCache[base].exports;
    var factory = eval('(' + extSources[base] + ')');
    var mod = { exports: {} };
    extCache[base] = mod;
    factory(function (id) { return requireId(base, id); }, mod, mod.exports);
    return mod.exports;
  }
  var cache = {};
  function requireId(fromKey, id) {
    var key = id;
    var ent = registry[fromKey];
    if (ent && ent[1] && Object.prototype.hasOwnProperty.call(ent[1], id)) {
      key = ent[1][id];
    } else {
      var base = id.split('/').pop();
      if (registry[base]) key = base;
      else if (extSources[base]) return loadExternal(base);
    }
    if (key === undefined || key === null) {
      var base2 = id.split('/').pop();
      if (extSources[base2]) return loadExternal(base2);
      throw new Error('unresolved require "' + id + '" from ' + fromKey);
    }
    if (cache[key]) return cache[key].exports;
    var ent2 = registry[key];
    if (!ent2) {
      var base3 = id.split('/').pop();
      if (extSources[base3]) return loadExternal(base3);
      throw new Error('no registry module: ' + key);
    }
    var mod = { exports: {} };
    cache[key] = mod;
    ent2[0].call(mod.exports, function (x) { return requireId(key, x); }, mod, mod.exports);
    return mod.exports;
  }

  globalThis.__dbg = { requireId: requireId }; // debug probes from Go tests

  // ── DB TextAssets → LogicDBMgr (dbTexts: {filename: rawJsonText}) ──
  var LDM_NS = loadExternal('LogicDBMgr');
  var LDM = LDM_NS.LogicDBMgr || LDM_NS;
  var strMap = {};
  for (var f in __DB_TEXTS) {
    var o = JSON.parse(__DB_TEXTS[f]);
    var name = o[5][0][1];
    var txt = o[5][0][2];
    strMap[name] = JSON.parse(txt)[name];
  }
  LDM.setLogicGameConfig({}, strMap, {});
  // Client's enterGame does DBMgr.initLogicGameConfig() + XiYouDBMgr.
  // initGameBaseConfig() — the latter fills rebirthCount, monsterRefreshTime,
  // secret config lists AND initSpineMap (spineDataMap — secret monsters
  // touch SimSpine.setAnimation via doTestPrint and crash without it).
  requireId('XiYouCombat', 'XiYouDBMgr').XiYouDBMgr.initGameBaseConfig();

  // ── sim (parity with harness.js simOnce) ──
  var XiYouLogicUtil = requireId('XiYouCombat', 'XiYouLogicUtil').XiYouLogicUtil;
  var combatMod = requireId('root', 'XiYouCombat');
  var XiYouCombat = combatMod.XiYouCombat;
  var IXiYouCombatResult = combatMod.IXiYouCombatResult;
  var XiYouDBMgr = requireId('XiYouCombat', 'XiYouDBMgr').XiYouDBMgr;
  var MAX_FRAMES = 100000;

  globalThis.xiyouSimJSON = function (roleJson, stageId, seed, timeLimit) {
    return xiyouSimOnce(JSON.parse(roleJson), stageId, seed, timeLimit);
  };

  // ── Ảo Cảnh (Secret) session API — mirrors XiYouSecretStage's combat
  // orchestration room-by-room so the Go side can interleave the wire ops
  // (Select answers drive which template the next room fights). Parity
  // anchors from index.pretty.js:
  //   create:  new XiYouSecretCombat(data, base?) + setCurFightTempId +
  //            init({playerData, seed, timeLimit}) — init spawns wave 1
  //   room:    all waves spawned & dead & !isFightingBoss & progress<100
  //   rebuild: on the door that will cross 100 (try2SavePlayerData), the
  //            client DESTROYS combat and rebuilds from buildExportData()
  //            (fresh frame 0, same baseSeed, data carries curProgress) —
  //            Kill's levels = getLevels() of THAT combat.
  var SecretMod = requireId('XiYouCombat', 'XiYouSecretCombat');
  var XiYouSecretCombat = SecretMod.XiYouSecretCombat;
  var XDM = requireId('XiYouCombat', 'XiYouDBMgr').XiYouDBMgr;
  var secretSessions = {};
  var secretSeq = 0;
  var MAX_SECRET_FRAMES = 200000;

  function newCombat(s, tplId, baseExport) {
    var c = new XiYouSecretCombat(s.data, baseExport || null);
    c.setCurFightTempId(tplId);
    c.roomFlag = false;
    var orig = XiYouSecretCombat.prototype.onAllEnemyDied.bind(c);
    c.onAllEnemyDied = function () {
      orig();
      if (!c.roomFlag && c.gameStatus === 0 && !c.isFightingBoss &&
        c._monsterInfos && c.curWave >= c._monsterInfos.length) {
        c.roomFlag = true;
      }
    };
    return c;
  }

  globalThis.xiyouSecretCreate = function (roleJson, tplId, seed, timeLimit) {
    var info = JSON.parse(JSON.stringify(roleJson ? JSON.parse(roleJson) : roleJson));
    info.attributeList = XiYouLogicUtil.fixPlayerProperty(info.heroAppearanceId, info.attributeList);
    var s = {
      playerData: XiYouLogicUtil.buildRoleData(info),
      baseSeed: Number(seed),
      data: { curProgress: '0', rebirthCount: null },
    };
    s.combat = newCombat(s, Number(tplId), null);
    s.combat.init({ playerData: s.playerData, seed: Number(seed), timeLimit: Number(timeLimit) });
    var id = ++secretSeq;
    secretSessions[id] = s;
    return { id: id };
  };

  function stepCombat(s) {
    var c = s.combat, frames = 0;
    while (!c.isGameOver() && !c.roomFlag && frames < MAX_SECRET_FRAMES) {
      c.update(50);
      frames++;
    }
    // enemy HP left/max at this frame — boss room: ĐÚNG % máu boss còn lại
    // (room thường mob chết một phần thì % là xấp xỉ trên tổng đã spawn)
    var enemyHp = 0, enemyHpMax = 0;
    try {
      var er = c.enemyRoleList || [];
      for (var ei = 0; ei < er.length; ei++) {
        var rr = er[ei];
        if (!rr) continue;
        try {
          if (!rr.isDead(true)) enemyHp += Number(rr.property.getNumByType(1, null, null, null, true));
          enemyHpMax += Number(rr.property.getNumByType(2, null, null, null, true)); // MaxHp=2
        } catch (e3) { /* skip role */ }
      }
    } catch (e2) { /* not fatal */ }
    return {
      status: c.isGameOver() ? 'end' : (c.roomFlag ? 'room' : 'frames'),
      gameStatus: c.gameStatus,
      progress: String(s.data.curProgress),
      levels: c.getLevels(),
      ms: c.frameTime,
      frames: c.frameCount,
      enemyHpLeft: enemyHp,
      enemyHpMax: enemyHpMax,
    };
  }

  globalThis.xiyouSecretStep = function (id) {
    var s = secretSessions[id];
    if (!s) return { error: 'no session ' + id };
    return stepCombat(s);
  };

  // entering a NORMAL next room (same combat continues)
  globalThis.xiyouSecretEnterDoor = function (id, tplId) {
    var s = secretSessions[id];
    if (!s) return { error: 'no session ' + id };
    s.combat.setCurFightTempId(Number(tplId));
    s.combat.roomFlag = false; // goNextStage resets curWave/waveIId but not our room flag
    s.combat.goNextStage(false);
    return { ok: 1 };
  };

  globalThis.xiyouSecretExport = function (id) {
    var s = secretSessions[id];
    if (!s) return { error: 'no session ' + id };
    return { data: JSON.stringify(s.combat.buildExportData()) };
  };

  // rebuild for the last room (the one that crosses progress 100 → boss).
  // completedJson = selectedDoorsIds minus the just-picked door (refreshProgress).
  globalThis.xiyouSecretRebuild = function (id, exportJson, tplId, bossCand, completedJson) {
    var s = secretSessions[id];
    if (!s) return { error: 'no session ' + id };
    // refreshProgress: curProgress = Σ add(star) over completed rooms —
    // same LogicNumberUtil.add the client uses (string/decimal math).
    var LNU = requireId('XiYouCombat', 'LogicNumberUtil').LogicNumberUtil;
    var p = '0';
    var done = JSON.parse(completedJson || '[]');
    for (var i = 0; i < done.length; i++) {
      var row = XDM.XiYouSecretTemplateDB[String(done[i])];
      if (row) p = LNU.add(p, String(XDM.getSecretStartAddProgress(row.star)));
    }
    s.data.curProgress = p;
    s.data.bossTempId = Number(bossCand);
    s.combat = newCombat(s, Number(tplId), JSON.parse(exportJson));
    s.combat.init({ playerData: s.playerData, seed: s.baseSeed });
    return { ok: 1, progress: p };
  };

  globalThis.xiyouSecretDestroy = function (id) {
    delete secretSessions[id];
    return { ok: 1 };
  };

  // DB helpers for the Go orchestrator (door pick + offline scans)
  globalThis.xiyouTemplateStar = function (tplId) {
    var row = XDM.XiYouSecretTemplateDB[String(tplId)];
    return row ? Number(row.star) : 0;
  };

  function xiyouSimOnce(roleInfo, stageId, seed, timeLimit) {
    var stage = XiYouDBMgr.XiYouStageDB[String(stageId)];
    if (!stage) return { error: 'no stage row ' + stageId };
    var info = JSON.parse(JSON.stringify(roleInfo)); // fresh: fixPlayerProperty mutates
    info.attributeList = XiYouLogicUtil.fixPlayerProperty(info.heroAppearanceId, info.attributeList);
    var playerData = XiYouLogicUtil.buildRoleData(info);
    var combat = XiYouCombat.create({
      playerData: playerData,
      monsterCfgs: stage.monster,
      seed: Number(seed),
      timeLimit: Number(timeLimit),
    });
    var frames = 0;
    while (!combat.isGameOver() && frames < MAX_FRAMES) {
      combat.update(50);
      frames++;
    }
    var resultName = null;
    for (var k in IXiYouCombatResult) {
      if (IXiYouCombatResult[k] === combat.gameStatus) { resultName = k; break; }
    }
    var playerHp = null;
    try {
      playerHp = String(combat.playerRole.property.getNumByType(1, null, null, null, true));
    } catch (e) { /* not fatal */ }
    // hill-climb proxy for loadout search: enemy HP left at end (0 ≈ win)
    var enemyHp = 0, enemyHpMax = 0;
    try {
      var er = combat.enemyRoleList || [];
      for (var ei = 0; ei < er.length; ei++) {
        var rr = er[ei];
        if (!rr) continue;
        try {
          if (!rr.isDead(true)) enemyHp += Number(rr.property.getNumByType(1, null, null, null, true));
          enemyHpMax += Number(rr.property.getNumByType(2, null, null, null, true)); // MaxHp=2
        } catch (e3) { /* skip role */ }
      }
    } catch (e2) { /* not fatal */ }
    return {
      result: combat.gameStatus,
      resultName: resultName,
      levels: combat.getLevels(),
      frames: combat.frameCount,
      ms: combat.frameTime,
      playerHp: playerHp,
      enemyHpLeft: enemyHp,
      enemyHpMax: enemyHpMax,
      seed: combat.baseSeed,
    };
  };
})();
