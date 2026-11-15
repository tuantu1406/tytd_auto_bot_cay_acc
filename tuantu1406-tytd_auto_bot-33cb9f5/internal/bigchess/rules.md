# Big Chess (Tiên Dịch Bài Cục) — AI rules

You control the single red piece (`chessId=1008`) on a grid (live size from
the `width`/`height` fields of `bigchess_state`; client fallback `8*9`,
zero-based coordinates, `x` 0..width-1, `y` 0..height-1). Survive as long as
possible and maximize captured score. The server is authoritative for
everything: legality, spawns, scoring, revival, settlement. Local reasoning
here is prediction only — always trust the next server board.

## Turn contract with the MCP tools

1. Call `bigchess_state` and remember `stateVersion`. If `hasSelf=false` or
   `settled=true`, start/resume with `bigchess_enter_game` first.
2. If `bigchess_analyze` is enabled (admin `/bcengine on`), call it on this
   state: it delegates your multi-round roll to a deterministic engine and
   returns a `recommendation` plus risk-adjusted `lines` with principal
   variations. It is ONE STRONG OPINION, not an oracle — it cannot see the
   hidden spawn cells (the bot masks placeholder previews, so deeper plies
   silently ignore the incoming wave) and it doesn't model the enemy step.
   Overlay the wave and enemy-move logic from "Thinking ahead" on top of
   every suggestion before executing it.
3. Derive your legal moves yourself from the board in the state (rules
   below). There is no legal-move oracle: the server will reject an illegal
   move with a non-zero `ret`.
4. Execute exactly ONE action per `stateVersion` (`bigchess_move`,
   `bigchess_use_random`, `bigchess_revive`, or `bigchess_end`). The tools
   wait for the server answer and return the new state — never fire two
   actions from the same state.
5. If `gameStatus==1` (dead, revival prompt), see "Death" below.

## Your movement (rook-like)

From your cell you may travel orthogonally (up/down/left/right) through empty
cells and may stop on any empty cell along the way; you may also stop ON the
first occupied cell in a direction, capturing that enemy piece. You may never
pass beyond the first blocker, move diagonally, or stay in place — except
with the pipe power (below).

## Enemy threat geometry

An enemy attacks a cell (e.g. your landing cell) as follows. "Line" means
same row or same column; all blocking uses the CURRENT enemy board.

| id | piece | attacks |
|---|---|---|
| 1001 | pawn | the 4 orthogonally adjacent cells |
| 1002 | advisor | the 4 diagonally adjacent cells |
| 1003 | elephant | exactly 2 diagonal cells away; blocked if the midpoint (eye) cell is occupied |
| 1004 | cannon | along a line with EXACTLY one intervening piece (screen); zero or two screens do not attack |
| 1005 | knight | the four (±1,±2)/(±2,±1) L cells; blocked if the orthogonal leg cell adjacent to the knight (in the direction of the larger delta) is occupied |
| 1006 | rook | along a line with no piece in between (the target cell itself is not a blocker) |
| 1007 | general | the 4 orthogonally adjacent cells (and see powers: generals are special) |

When you capture a piece, that piece cannot threaten its own landing cell —
evaluate threats AFTER your removals, on the post-move board.

## The defender runs (live-verified, 2900+ transitions)

After your move resolves, exactly ONE enemy moves: with overwhelming priority
(84% of turns) it is a piece your NEW position attacks, and it escapes by one
of its own move-pattern steps to a cell where you no longer attack it (93%
succeed; the rest are walled in). Consequences you must play with:

- **Never plan to capture next turn a piece you only threaten this turn** —
  it will have fled before your next move. Captures happen by MOVING ONTO
  the piece (this turn), not by aiming at it.
- **A threatened piece that cannot escape is pinned**: every one of its
  own-pattern steps is off-board, occupied, or still attacked. Crowding
  (walls, board edge, dense clusters) creates pins — the 1-2 step movers
  (pawns, advisors) near walls are your safest future meals; approach and
  take them next turn.
- **Double attack = free capture**: your rook can threaten up to four first
  blockers at once; only ONE flees, so any other threatened piece sits
  attacked-and-trapped and dies next turn. Look for moves attacking two
  pieces, especially when one has no escape.
- **Herding**: threatening a piece MOVES it — drive a blocker off a line you
  want open, steer pieces out of a cell where they'd spawn-support others,
  or shove a pawn aside to expose the advisor behind it. Never threaten the
  piece you intend to eat immediately — eat it, don't announce it.
- Enemy positions are volatile outside the current turn: every static threat
  map you compute is only guaranteed for this move's resolution, not next
  turn's board (the external engine's search treats them as frozen — apply
  this rule around its advice).

## Spawn waves (the main killer)

The board is repopulated in waves. The state carries `nextTurn` (countdown)
and `nextChess` — the NEXT wave's composition. When `nextTurn` is `1`, the
whole wave enters right after your move resolves (counters decrement each
turn; a missing `nextTurn` means no pending wave).

**The server hides where the wave lands**: live-verified, `nextChess`
entries carry real `chessId`/`chessNo` but PLACEHOLDER coordinates `(0,0)` —
the true cells appear only after the wave resolves. So treat the wave as
known composition in unknown positions:
- Count what arrives (e.g. two pawns = two new adjacent-attack threats
  anywhere near you; one advisor = one diagonal-adjacent threat).
- A landing cell is only safe if it stays safe against the wave wherever
  the wave could plausibly spawn — e.g. next to your piece, with clear
  lines, or anywhere that creates no new attacker on your cell given the
  types arriving. Corner-hugging with few enemy lines is usually safer than
  open center when a big wave is about to land.
- Spawns take empty cells only; your own cell won't be overwritten, but a
  pawn spawning orthogonally adjacent to you kills you like any pawn.

## Powers

Three boolean flags ride on every `bigchess_move`; all three are always sent
(false when unused). They are independent unless noted.

- `powerOn` (pipe / xuyên): your move glides THROUGH blockers instead of
  stopping at them and removes every piece crossed along the chosen segment
  (endpoints inclusive of the captured piece). Requires remaining pipe uses
  (`powerOnCount` is the number you have ALREADY consumed; the live limit
  comes from config when known — otherwise trust the game to refuse).
- `powerCross` (cross): clears ALL pieces on the destination row AND column.
  A cross charge (`powerTimes`) is consumed by any move sent with this flag.
  Capturing/removing a general (1007) GRANTS cross charges. Note: the client
  protocol fires the cross automatically on every move while `powerTimes > 0`
  — sending `powerCross=false` with `powerTimes>0` is allowed here and the
  server is the authority.
- `powerRemoveType` (similar): only sensible when your destination holds an
  enemy; removes every board piece sharing the target's `chessId`.

`bigchess_use_random` (random removal) is a separate targetless action: the
server removes a random set, the answer lists the REMOVED pieces, each
removed general again feeds a cross charge. It is not a move — call
`bigchess_state` again after it.

All `*Count` response fields are CONSUMED counters, not remaining counts.

## Scoring

Each removed enemy contributes its `score` from `pieceScores` (live
`ChessConfig` table; generals also grant a cross charge). `score` is int64 —
do not round or re-derive it; the server's value replaces any local estimate.

## Death and settlement

- `gameStatus==1`: you died. The server offers revival. `bigchess_revive`
  (type 0) accepts; the server picks the price from run state — item revive
  first, then ingot revive priced by `BIGCHESS_REBORN_COST` (real currency
  item), and `passReviveCount`/`isRevive>0` show free pass revives already
  spent. Reviving is costly and unbounded by you: prefer `bigchess_end`
  unless continuing clearly pays off, and NEVER loop revive blindly.
- `bigchess_end` settles the run — but the LIVE server only accepts it once
  `step >= winRound` (default 300) or from the dead/settle states; sending it
  mid-run is refused with `ret=34103` (live-verified). It returns final
  score and rank; `settled=true` afterwards.
- `bigchess_enter_game` starts a new run while `freeCount > 0` (or costs an
  item, which the server refuses via `ret` when it can't pay).

## The clock nobody shows you: the board only fills up

Every wave ADDS pieces; nothing removes them except your captures and
powers. Piece density therefore rises monotonically with `step`. The
endgame that actually kills your score is not death — it's CROWDING: pieces
spawn packed around you until every direction's first blocker is something
you can't (or can't afford to) eat, your rook glides die after one cell, and
there are no safe landings left — at which point you stop scoring entirely
and die anyway. Score eaten early is strictly better than the same piece
eaten late.

Play phases by density (count pieces within 2 cells of you + total board
size vs empty cells):

- **EARLY (few pieces, long empty lines)** — EAT FAST and aggressively:
  run at valuable pieces even if the approach needs a risky step, because
  escape lanes are everywhere and a pawn-only threat net is easy to dodge.
  Chasing a capture across the open board is correct here. Do NOT sit
  passively in a safe corner — every wasted turn lets the wave builder
  fence you in.
- **MID (board filling, groups forming)** — eat while KEEPING YOUR LINES:
  prefer captures that end on open glides, avoid sliding into pockets
  (cells where blockers already stand close on 3 sides). Your mobility IS
  your future capture rate; trade a pawn now against a line-out later only
  when the line-out has real prey.
- **LATE (crowded near you, glides die in 1–2 cells)** — this is POWERS
  time: `powerCross` clears the row+column around you (open your cage),
  `powerOn` (pipe) slides you THROUGH the wall to open ground while
  clearing the segment, `bigchess_use_random` thins the pack when no
  affordable capture exists. Generals removed = new cross charges — in a
  crowd, a general is worth both its score and the charge it funds. A
  crowded board where you never spend powers is a lost board.

## Thinking ahead (MANDATORY: roll 2–3 rounds, never decide on the static board)

Every turn changes the board three ways: your move, the incoming wave, and
an enemy step. Score each candidate destination by rolling it forward, not
by its immediate view:

- **Enemies move too.** After your move resolves, the server may slide one
  enemy (the answer reports it as `moveChess` — live-verified). Threat
  geometry is volatile: an adjacent pawn/general can close in, a cannon's
  screen can be moved into or out of place. A cell counts as safe only if
  it survives the wave AND one plausible enemy step.
- **Rollout per candidate, in order:**
  1. Ply 1 — board after your move (removed pieces can't threaten their own
     cells); if `nextTurn==1` add the wave at plausible cells. If even the
     BEST plausible placement of that wave attacks your landing, the
     landing is a trap.
  2. Ply 2 — enumerate YOUR destinations from there. The line stays alive
     only if you still have ≥2 survivable options; note which captures stay
     reachable — remembering "the defender runs": a capture you plan for
     ply 2 is only real if the target is pinned (no escape) or you can step
     onto it the moment you arrive.
  3. Ply 3 — one more wave (composition beyond `nextChess` is unknown —
     assume the same mix) plus another enemy step. Zero safe landings by
     ply 3 = dead line; skip it unless plies 1–2 harvest a big score.
- **Branching factor is the currency.** Among candidates that capture
  equally, take the one with the most safe ply-2 options. A capture that
  leaves you exactly one exit is worse than a pawn that keeps four exits —
  exits are cheapest to preserve EARLY and priceless LATE.
- **Count density inside the rollout too.** If your ply-3 projection shows
  pieces adjacent on 3+ sides of you, that line is a cage no matter the
  captures: reach for the LATE-phase power plan one turn early.

## Choosing a move (priority order)

1. Land where no enemy attacks you on the post-move, post-wave board AND the
   rollout above leaves you ≥2 survivable ply-2 options. A landing with any
   attacker is a predicted death unless beginner protection (`newPlayer`) is
   in effect.
2. **EARLY = maximize captures now** (the density clock above); among equal
   captures, prefer the one keeping open lines. From MID onward, among safe
   landings prefer captured score + exit quality; avoid cells where the
   incoming wave can wall you in — edges/corners are traps once dense.
3. `powerTimes>0` is freedom — landing where you can cross next turn is a
   fine survival AND opening plan.
4. Spend pipe/similar/random according to the phase rules: hoarding into
   the LATE phase is a strategy, hoarding PAST the crowding threshold is a
   loss.

The AI — not the bot — makes every choice. The bot only validates
coordinates against the live board size and relays server answers.
