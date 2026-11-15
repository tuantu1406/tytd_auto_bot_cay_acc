# Big Chess — wire protocol & tool reference (activity type 341)

Requests carry the FULL id; the client network layer writes ids ≥ 100000 as
id % 100000 on the way back from the server. Success is always `ret == 0`.
The bot (tytd) speaks this protocol for you and exposes only the tools below.

## Ranked commands (what the MCP tools relay)

| full id | transport | tool | request | answer highlights |
|---|---|---|---|---|
| 234101 | 34101 (8535) | — (folded into state) | `{activityId}` | `playerData{freeCount,maxScore,gameStatus}` lobby |
| 234102 | 34102 (8536) | `bigchess_enter_game` | `{activityId}` | full board `chessDataList`, `selfChessData`, previews, all counters |
| 234103 | 34103 (8537) | `bigchess_move` | `{activityId,x,y,powerOn,powerCross,powerRemoveType}` (bools always present) | survivor board `chess`, `selfChess`, optional `moveChess` (enemy that moved), new preview `nextTurn`/`nextChess`, `gameStatus`, `isRevive>0` (auto pass-revive happened), `score` (int64!), `step`, consumed counters, `newPlayer` (beginner protection) |
| 234104 | 34104 (8538) | `bigchess_end` | `{activityId}` | final `score`, `lastRank`, `nowRank` |
| 234105 | 34105 (8539) | `bigchess_revive` | `{activityId,type:0}` | survivor board, updated revive counts (server chose item vs ingot by `BIGCHESS_REBORN_COST`) |
| 234106 | 34106 (853A) | `bigchess_use_random` | `{activityId}` | `chess` = the REMOVED pieces, `powerRemoveRandomCount` |

Practice variants (FREE mode — no daily starts, no ingots, ideal for long
engine experiments), wired as their own tools:

| full id | transport | tool | note |
|---|---|---|---|
| 234121 | 34121 (8549) | `bigchess_practice_enter_game` | same response layout as ranked 234102 |
| 234122 | 34122 (854a) | `bigchess_practice_move` | request carries NO power fields; resp `newPlayer` is field 11 (ranked uses 11 for powerOnCount) |
| 234123 | 34123 (854b) | `bigchess_practice_end` | always accepted (no winRound gate); `score` is field 3, unlike ranked field 2, and the answer includes the survivor board |

Practice shares the one `bigchess_state` slot with ranked: whichever mode you
entered last owns the board. Death in practice just ends — there is no revive
ladder to pay.

## Board/state model (as `bigchess_state` returns it)

- Coordinates zero-based; live dims `width`/`height` (config
  `BIGCHESS_BOARD_XY` as `W*H`, fallback `8*9`).
- `board[]`: `{chessNo,chessId,x,y}` — `chessNo` is the unique instance key
  (many pieces share one `chessId`); `chessId`: 1001 pawn, 1002 advisor,
  1003 elephant, 1004 cannon, 1005 knight, 1006 rook, 1007 general, 1008 YOU.
- `nextTurn`/`nextChess`: the upcoming spawn wave (composition) + countdown;
  the wave enters after the move that sees `nextTurn==1`. LIVE-VERIFIED:
  preview entries have PLACEHOLDER coordinates `(0,0)` — the server does not
  reveal spawn cells until the wave resolves, so use the wave's `chessId`
  mix for probabilistic threat assessment only.
- `moveChess`: the enemy the server just moved. Live statistics: one enemy
  moves EVERY turn, in 84% of turns exactly a piece your new position
  attacked, escaping one own-pattern step out of your line — see rules
  "The defender runs".
- `gameStatus`: 0 running, 1 dead/revive-prompt, 2 over, 3 pass.
- Every `*Count` is CONSUMED uses, not remaining.
- `pieceScores`: live `ChessConfig{id,score}` capture table (canGo/moveLimit
  strings exist but no client uses them). `params`: raw BIGCHESS_* config
  when the bot has fetched the activity detail — incl. `BIGCHESS_WIN_ROUND`
  (default 300) and `BIGCHESS_REBORN_COST` (ordered ingot prices `|`-joined).
- `stateVersion` increments on every mutation from the server; one action
  per version, read the returned state after each action.
- `activityWindow` reports the live activity's phase/times (the game itself
  refuses moves outside its daily window; the `ret` code will tell you).

## Errors you will see from the tools

- `server rejected … (ret=N)` — the game server's own refusal (bad move,
  wrong phase, no free run, cooldown…). Observed live: `end` mid-run →
  `ret=34103` (end is only honored at `step>=winRound` or after death).
  Re-read state and adapt.
- `another big-chess action is in flight` — one request at a time per
  account; wait and retry.
- `server did not answer` / `connection dropped` — account transport
  problem, not a game decision; verify with `get_user_info`/fresh state.

## External play engine (optional — admin `/bcengine`)

`bigchess_analyze` relays the current state to the external engine service
(`remoteBigChess/engine_api`, POST `/v1/analyze`). Two format truths the bot
fixes on the way out, because the engine can't know them: only `cross`
charges are truly knowable (`powerTimes`), so the resources block carries
`cross` exact and pipe/similar/random as ZERO — live-verified 2026-10-01:
a `powerRemoveType` move built from `BIGCHESS_*` limit params was refused
with `ret=101` because these powers are gated by owned inventory items that
never appear in game state. The AI may still try them via `bigchess_move`
flags (server refusal is the oracle; a refused move costs nothing but the
turn's analysis), and an all-`(0,0)` preview is MASKED so the engine's
deeper plies don't spawn a phantom wave on cell (0,0) — consequence: the
engine's search cannot see the incoming wave, so always apply the rules'
wave logic around its advice.
After every successful move/use-random/revive the bot also POSTs
`/v1/transitions` (stateBefore/action/stateAfter, sessionId `tytd:<account>`)
so the engine's replay model learns from your real games. When the GAME
refuses one of your actions with `ret!=0`, the bot POSTs `/v1/rejections`
(same envelope, `result{ok:false,ret}`) — the engine then QUARANTINES that
advice for that position, so re-analyzing returns something else instead of
repeating the refused move forever (loop protection). A fresh
`bigchess_enter_game` clears the quarantine via `/v1/rejections/clear`
(you may have bought the items behind refused powers; note the bot still
cannot see your inventory, so paid powers stay disabled in analysis until
inventoryVerified is possible). Transport errors ("server did not answer")
are never reported as rejections. Tool errors starting with
`big-chess engine` mean disabled/unreachable — gameplay and the other tools
are unaffected.

## Supporting activity machinery (handled by the bot, FYI)

Activity discovery is by the live activity list: entries with `type == 341`
(the type is NEVER used as an activityId). The bot resolves the active
instance id, fetches the 21003 config detail (piece scores + BIGCHESS_*
params + window) automatically, and refuses tool calls when no active
instance exists.
