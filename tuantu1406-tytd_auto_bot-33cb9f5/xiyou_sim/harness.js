// Headless replay of the XiYou client combat sim: runs the game's own
// browserify logic modules (extracted from the remoteXiYou bundle) with the
// real DB TextAssets from the CDN, so we can produce the exact `levels`
// array the server expects for a boss crossing.
//
// usage: node harness.js --role role.json --stage 190401 --seed 12345 [--timelimit 45000]
//   role.json  = JSON of XiYouPlayerDataInfoResp.info (heroAppearanceId, useSkills,
//                passiveSkillIds, attributeList[{type,value}], ...)
// prints one JSON line: {result, levels, frames, ms, playerHp, enemies:[...]}

const fs = require('fs');
const path = require('path');

function arg(name, dflt) {
  const i = process.argv.indexOf('--' + name);
  return i > 0 && process.argv[i + 1] !== undefined ? process.argv[i + 1] : dflt;
}

const ROLE_FILE = arg('role', null);
const STAGE_ID = arg('stage', null);
const SEED = arg('seed', '0');
const TIME_LIMIT = Number(arg('timelimit', 45000));
const MAX_FRAMES = Number(arg('maxframes', 100000));
const TAYDU = __dirname; // self-contained: index.464ca.js + db/ + *.module.js live here

if (!!ROLE_FILE !== !!STAGE_ID) {
  console.error('need BOTH --role and --stage (or neither → daemon mode)');
  process.exit(2);
}

// ── globals the game modules expect ────────────────────────────────────────
const BigNumber = require('./bignumber.js');
global.BigNumber = BigNumber;
global.cc = {
  _RF: { push() {}, pop() {}, peek() { return {}; } },
};
const QUIET = process.env.XIYOU_SIM_VERBOSE ? console : { log() {}, error() {}, warn() {} };
global.Logger = { log() {}, error() {}, warn() {} };
global.window = global;
global.self = global;

// ── browserify registry from the remoteXiYou bundle ────────────────────────
const src = fs.readFileSync(path.join(TAYDU, 'index.464ca.js'), 'utf8');
const dictStart = src.indexOf('}({') + 3;
const dictEnd = src.indexOf('},{},[', dictStart);
const registry = eval('({' + src.slice(dictStart, dictEnd) + '})');

// externals stripped from the bundle (sliced out of the main script.js)
const EXT_SOURCES = {
  LogicDBMgr: fs.readFileSync(path.join(TAYDU, 'logicdbmgr.module.js'), 'utf8'),
  StringUtil: fs.readFileSync(path.join(TAYDU, 'stringutil.module.js'), 'utf8'),
};
const extCache = {};
function loadExternal(base) {
  if (extCache[base]) return extCache[base].exports;
  const factory = eval('(' + EXT_SOURCES[base] + ')');
  const mod = { exports: {} };
  extCache[base] = mod;
  factory((id) => requireId(base, id), mod, mod.exports);
  return mod.exports;
}

const cache = {};
function requireId(fromKey, id) {
  let key = id;
  const ent = registry[fromKey];
  if (ent && ent[1] && Object.prototype.hasOwnProperty.call(ent[1], id)) {
    key = ent[1][id];
  } else {
    const base = id.split('/').pop();
    if (registry[base]) key = base;
    else if (EXT_SOURCES[base]) return loadExternal(base);
  }
  if (key === undefined || key === null) {
    const base = id.split('/').pop();
    if (EXT_SOURCES[base]) return loadExternal(base);
    throw new Error('unresolved require "' + id + '" from ' + fromKey);
  }
  if (cache[key]) return cache[key].exports;
  const ent2 = registry[key];
  if (!ent2) {
    const base = id.split('/').pop();
    if (EXT_SOURCES[base]) return loadExternal(base);
    throw new Error('no registry module: ' + key);
  }
  const mod = { exports: {} };
  cache[key] = mod;
  ent2[0].call(mod.exports, (x) => requireId(key, x), mod, mod.exports);
  return mod.exports;
}

// ── feed the real DB TextAssets to LogicDBMgr ──────────────────────────────
const LDM_NS = loadExternal('LogicDBMgr');
const LDM = LDM_NS.LogicDBMgr || LDM_NS;
const strMap = {};
for (const f of fs.readdirSync(path.join(TAYDU, 'db'))) {
  if (!f.endsWith('.json')) continue;
  const o = JSON.parse(fs.readFileSync(path.join(TAYDU, 'db', f), 'utf8'));
  const name = o[5][0][1];
  const txt = o[5][0][2];
  strMap[name] = JSON.parse(txt)[name];
}
{
  const _log = console.log;
  console.log = () => {};
  LDM.setLogicGameConfig({}, strMap, {});
  console.log = _log;
}

// ── run the sim ────────────────────────────────────────────────────────────
const { XiYouLogicUtil } = requireId('XiYouCombat', 'XiYouLogicUtil');
const { XiYouCombat, IXiYouCombatResult } = requireId('root', 'XiYouCombat');
const { XiYouDBMgr } = requireId('XiYouCombat', 'XiYouDBMgr');

// simOnce = one deterministic fight replay. Shared by CLI mode and daemon
// mode (stdin line-protocol) so the bot keeps ONE warm node process instead
// of re-parsing bundle+DBs per seed-farm round.
function simOnce(roleInfo, stageId, seed, timeLimit) {
  const stage = XiYouDBMgr.XiYouStageDB[String(stageId)];
  if (!stage) return { error: 'no stage row ' + stageId };
  const info = JSON.parse(JSON.stringify(roleInfo)); // fresh: fixPlayerProperty mutates
  // client applies fixPlayerProperty(heroAppearanceId, attributeList) in msg_reqRoleData
  info.attributeList = XiYouLogicUtil.fixPlayerProperty(info.heroAppearanceId, info.attributeList);
  const playerData = XiYouLogicUtil.buildRoleData(info);
  const combat = XiYouCombat.create({
    playerData,
    monsterCfgs: stage.monster,
    seed: Number(seed),
    timeLimit: Number(timeLimit),
  });
  let frames = 0;
  const TRACE = process.env.XIYOU_SIM_TRACE ? Number(process.env.XIYOU_SIM_TRACE) : 0;
  while (!combat.isGameOver() && frames < MAX_FRAMES) {
    combat.update(50); // LOGIC_FPS = 20 → 50 ms per frame
    frames++;
    if (TRACE && frames % TRACE === 0) {
      const php = combat.playerRole && combat.playerRole.property
        ? String(combat.playerRole.property.getNumByType(1, null, null, null, true)) : '?';
      const ehps = combat.enemyRoleList.map((e) =>
        e.instId + ':' + String(e.property.getNumByType(1, null, null, null, true))).join(',');
      process.stderr.write(`f${frames} t=${combat.frameTime}ms php=${php} enemies=[${ehps}]\n`);
    }
  }
  const resultName = Object.keys(IXiYouCombatResult)
    .find((k) => IXiYouCombatResult[k] === combat.gameStatus);
  let playerHp = null;
  try {
    playerHp = combat.playerRole.property.getNumByType(1, null, null, null, true);
  } catch (e) { /* not fatal */ }
  // hill-climb proxy for loadout search: enemy HP left at end (0 ≈ win)
  let enemyHp = 0, enemyHpMax = 0;
  try {
    const er = combat.enemyRoleList || [];
    for (const rr of er) {
      try {
        if (!rr.isDead(true)) enemyHp += Number(rr.property.getNumByType(1, null, null, null, true));
        enemyHpMax += Number(rr.property.getNumByType(2, null, null, null, true)); // MaxHp=2
      } catch (e3) { /* skip role */ }
    }
  } catch (e2) { /* not fatal */ }
  return {
    result: combat.gameStatus,
    resultName,
    levels: combat.getLevels(),
    frames: combat.frameCount,
    ms: combat.frameTime,
    playerHp: playerHp === null ? null : String(playerHp),
    enemyHpLeft: enemyHp,
    enemyHpMax: enemyHpMax,
    seed: combat.baseSeed,
  };
}

if (ROLE_FILE) {
  // ── CLI mode ──
  const roleInfo = JSON.parse(fs.readFileSync(ROLE_FILE, 'utf8'));
  const out = simOnce(roleInfo, STAGE_ID, SEED, TIME_LIMIT);
  if (out.error) { console.error(out.error); process.exit(2); }
  console.log(JSON.stringify(out));
} else {
  // ── daemon mode ──: stdin lines {"id","role","stage","seed","timelimit"}
  // → stdout same JSON + result fields. Stays resident: bundle+DBs parsed once.
  const rl = require('readline').createInterface({ input: process.stdin });
  rl.on('line', (line) => {
    line = line.trim();
    if (!line) return;
    let resp;
    try {
      const q = JSON.parse(line);
      resp = simOnce(q.role, q.stage, q.seed, q.timelimit || 45000);
      resp.id = q.id;
    } catch (e) {
      resp = { error: String(e && e.message || e), id: (() => { try { return JSON.parse(line).id; } catch (_) { return null; } })() };
    }
    process.stdout.write(JSON.stringify(resp) + '\n');
  });
}
