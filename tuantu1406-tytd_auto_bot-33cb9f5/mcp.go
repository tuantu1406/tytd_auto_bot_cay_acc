package tytd_go

// MCP-over-HTTP: a hand-rolled Model Context Protocol server (Streamable
// HTTP transport, JSON-RPC 2.0 over POST) so an MCP client (Claude, an
// agent, a script) can inspect and nudge the bot's live accounts without a
// Telegram session. Like the web dashboard this runs in-process inside the
// bot binary and shares its single source of truth — t.session and the DB —
// rather than talking over a socket to a second copy of state.
//
// There is deliberately no dependency here: the protocol surface an agent
// needs from this bot is initialize / ping / tools/list / tools/call, which
// is a few hundred lines of JSON-RPC. Sessions are not tracked (the server
// is stateless per request; an Mcp-Session-Id is issued at initialize for
// clients that want to echo one, but nothing is enforced).
//
// Auth is one shared key the admin sets with /mcp key <key>: every request
// must present it via X-MCP-Key or Authorization: Bearer, compared in
// constant time. The key is read fresh from the DB on each request, so
// rotation and /mcp off take effect instantly, no restart. Whoever holds
// the key operates at tool scope — treat it like an operator credential.
//
// What the tools may do, and may never do: they see account data WITHOUT
// passwords and WITHOUT chat ids (the response structs simply have no such
// fields; account_notify resolves its target chat internally), and every
// mutating tool requires the target account to be live and running —
// "interact with an online account", not remote-control arbitrary rows.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"tytd_go/internal/bigchess"
	"tytd_go/internal/model"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const (
	mcpDefaultBind = ":8090"
	mcpKeyHeader   = "X-MCP-Key"
	mcpMaxBody     = 1 << 20 // requests are tiny tool calls; refuse anything bigger
	// mcpLatestProto is echoed back when a client asks for a version we
	// already speak; unknown asks get this one too (spec: newest supported).
	mcpLatestProto = "2025-06-18"
)

var mcpKnownProtos = map[string]bool{"2024-11-05": true, "2025-03-26": true, "2025-06-18": true}

// ---- config accessors (fresh per call — admin flips take effect at once) ---

func (t *TeleBot) mcpEnabled() bool {
	c, err := model.GetConfig(t.ydb.GetDB(), model.ConfigKeyMCPEnabled)
	if err != nil || c == nil {
		return false
	}
	v, err := strconv.ParseBool(c.Value)
	return err == nil && v
}

func (t *TeleBot) mcpAuthKey() string {
	c, err := model.GetConfig(t.ydb.GetDB(), model.ConfigKeyMCPAuthKey)
	if err != nil || c == nil {
		return ""
	}
	return strings.TrimSpace(c.Value)
}

// ---- server ----------------------------------------------------------------

// mcpBind resolves the listen address exactly like webBind resolves the
// dashboard's: TYTD_MCP_BIND (hidden override) → MCP_PORT → the default.
func mcpBind() string {
	if s := strings.TrimSpace(os.Getenv("TYTD_MCP_BIND")); s != "" {
		if _, _, err := net.SplitHostPort(s); err == nil {
			return s
		}
	}
	if s := strings.TrimSpace(os.Getenv("MCP_PORT")); s != "" {
		if h, p, err := net.SplitHostPort(s); err == nil {
			if n, e := strconv.Atoi(p); e == nil && n >= 1 && n <= 65535 {
				return h + ":" + strconv.Itoa(n)
			}
		} else if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 65535 {
			return ":" + strconv.Itoa(n)
		}
		log.Printf("[mcp] invalid MCP_PORT=%q — falling back to %s", s, mcpDefaultBind)
	}
	return mcpDefaultBind
}

// StartMCPServer serves the MCP endpoint. Like StartWebServer it runs for
// the process lifetime and always listens; it answers only once the admin
// has both turned it on (/mcp on) and set a key (/mcp key …).
func (t *TeleBot) StartMCPServer() {
	bind := mcpBind()
	srv := &http.Server{Addr: bind, Handler: t.mcpRoutes(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("[mcp] listening on %s (503 until /mcp on + /mcp key)", bind)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("[mcp] server error: %v", err)
	}
}

func (t *TeleBot) mcpRoutes() http.Handler {
	mux := http.NewServeMux()
	h := t.mcpAuthGate(t.mcpHandle)
	mux.HandleFunc("/mcp", h)
	mux.HandleFunc("/", h) // many clients default to the bare origin
	return mux
}

func mcpPresentedKey(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get(mcpKeyHeader)); k != "" {
		return k
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("bearer "):])
	}
	return ""
}

// mcpAuthGate answers 503 while the feature is off or keyless (an enabled
// endpoint with no key would otherwise serve unauthenticated), and 401 for
// every wrong/missing key. The config is read per request on purpose.
func (t *TeleBot) mcpAuthGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := t.mcpAuthKey()
		if !t.mcpEnabled() || want == "" {
			http.Error(w, "MCP server is off", http.StatusServiceUnavailable)
			return
		}
		got := mcpPresentedKey(r)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="tytd-mcp"`)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// ---- JSON-RPC plumbing -------------------------------------------------------

type mcpRPCReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// mcpHandle speaks one JSON-RPC message per POST (MCP's Streamable HTTP
// transport does not require batching). GET would open the optional SSE
// stream — we don't push, so 405 per spec.
func (t *TeleBot) mcpHandle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Allow", "POST")
		http.Error(w, "streaming not supported", http.StatusMethodNotAllowed)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, mcpMaxBody)
	var req mcpRPCReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mcpRPCErrorOut(w, nil, -32700, "parse error: "+err.Error())
		return
	}
	// A notification (no id) gets 202 with no body, whichever it is.
	notify := len(req.ID) == 0 || string(req.ID) == "null"
	switch req.Method {
	case "initialize":
		mcpInitialize(w, &req)
		return
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
		return
	case "ping":
		if notify {
			w.WriteHeader(http.StatusAccepted)
			return
		}
	default:
		if notify { // unknown notification: ignore per JSON-RPC
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}

	switch req.Method {
	case "ping":
		mcpRPCResult(w, req.ID, map[string]any{})
	case "tools/list":
		mcpRPCResult(w, req.ID, map[string]any{"tools": mcpToolDefs})
	case "tools/call":
		t.mcpToolCall(w, req.ID, req.Params)
	case "resources/list":
		mcpRPCResult(w, req.ID, map[string]any{"resources": mcpResourceDefs})
	case "resources/read":
		t.mcpResourceRead(w, req.ID, req.Params)
	default:
		mcpRPCErrorOut(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func mcpInitialize(w http.ResponseWriter, req *mcpRPCReq) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &p)
	proto := mcpLatestProto
	if mcpKnownProtos[p.ProtocolVersion] {
		proto = p.ProtocolVersion
	}
	// Stateless server, but clients that track a session id get one to echo.
	w.Header().Set("Mcp-Session-Id", webRandomHex(16))
	mcpRPCResult(w, req.ID, map[string]any{
		"protocolVersion": proto,
		"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
		"serverInfo":      map[string]any{"name": "tytd-bot", "version": "1.0"},
		"instructions": "Inspect and steer the TYTD bot's live game accounts. All account data excludes passwords and chat ids (address accounts by account_id only); mutating tools require the target account to be online. " +
			"Big Chess (activity type 341) is played BY YOU (the AI): read resource bigchess://rules once, then loop bigchess_state -> decide one action -> bigchess_move/bigchess_use_random/bigchess_revive/bigchess_end, always acting on the latest stateVersion. The bot relays and never picks moves. Core doctrine: spawn waves only ADD pieces, so the board saturates and crows you in — eat enemy pieces FAST while the board is still open, and spend powers once pieces sit adjacent and glides stop working. Before EVERY move you must roll the game 2-3 rounds ahead per the 'Thinking ahead' section of bigchess://rules (your move, the wave, the enemy step) — never decide from the static board. bigchess_analyze (when the admin enables it) delegates that roll to a deterministic multi-ply engine: its recommendation is advisory — it cannot see the hidden spawn wave, so apply the rules' wave logic around it. Need a FREE unlimited run (no daily starts, no ingots, no powers): bigchess_practice_enter_game / bigchess_practice_move / bigchess_practice_end.",
	})
}

func mcpRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func mcpRPCErrorOut(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	})
}

// ---- account views (passwordless by construction) ---------------------------

// mcpRuntime mirrors the live session switches the /toggle page and the chat
// handlers expose; runtime-only, never persisted, hence read from the
// resident *Player only.
type mcpRuntime struct {
	ChuotRun       bool `json:"chuot_run"`
	ChuotDoing     int  `json:"chuot_doing"`
	ChuotTotal     int  `json:"chuot_total"`
	Notify         bool `json:"notify"`
	Maohiem        bool `json:"maohiem"`
	Lucdao         bool `json:"lucdao"`
	Yeuthap        bool `json:"yeuthap"`
	Peach          bool `json:"peach"`
	XaVe           bool `json:"xave"`
	Phanthan       bool `json:"phantan"`
	BottlePrefered bool `json:"bottle_prefered"`
}

// mcpAccount is the MCP wire shape of one game account. There is no
// Password field anywhere in it (or its builders) — that is the guarantee
// "exclude password" is enforced by, so adding a field later must keep that
// property.
// mcpActivityView is one live activity an online account currently has, so
// the AI can pick where to play (e.g. big chess = type 341, phase active).
type mcpActivityView struct {
	Type       int32  `json:"type"`
	ActivityID int32  `json:"activityId"`
	Phase      string `json:"phase"` // show | active | reward
}

type mcpAccount struct {
	AccountID   string `json:"account_id"`
	SessionType string `json:"session_type"` // player | event
	Username    string `json:"username"`
	IngameName  string `json:"ingame_name"`
	ClanName    string `json:"clan_name"`
	ServerID    int    `json:"server_id"`
	GameUID     uint64 `json:"game_uid"`
	Running     bool   `json:"running"` // live session present and connected
	// Deliberately NO chat ids here (notify target, leader): they were
	// stripped from the view — the account_notify tool resolves the chat
	// internally from the live session, and leader_id was just another
	// user's Telegram chat id. Address accounts by account_id only.
	IsLeader    bool              `json:"is_leader,omitempty"`
	AutoLogin   bool              `json:"auto_login,omitempty"`
	VipLevel    int               `json:"vip_level,omitempty"`
	VipExpireAt string            `json:"vip_expire_at,omitempty"`
	Runtime     *mcpRuntime       `json:"runtime,omitempty"`        // player sessions only
	Activities  []mcpActivityView `json:"liveActivities,omitempty"` // online sessions only
}

// mcpActsOf renders an online session's activity rows so an AI can pick
// where to play — e.g. big chess is the entry with type 341, phase active.
// (activityId here is the live instance id the big-chess tools re-resolve
// themselves; the type+phase pair is what the chooser needs.)
func mcpActsOf(a *activityStore) []mcpActivityView {
	now := time.Now()
	var out []mcpActivityView
	for _, e := range a.snapshot() {
		if ph := e.status(now); ph != "none" {
			out = append(out, mcpActivityView{Type: e.Type, ActivityID: e.ActivityID, Phase: ph})
		}
	}
	return out
}

func mcpVipStr(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.Format(time.RFC3339)
}

// mcpAccountOf renders one resident session. The caller must have held the
// value through a type switch already — t.session mixes *Player and
// *EventPlayer and a bare assertion panics (see memory tytd-session-keying).
func (t *TeleBot) mcpAccountOf(key int64, v any) mcpAccount {
	a := mcpAccount{AccountID: strconv.FormatInt(key, 10)}
	switch p := v.(type) {
	case *Player:
		a.SessionType = "player"
		a.Username, a.IngameName, a.ClanName = p.Username, p.Name, p.ClanName
		a.ServerID, a.GameUID, a.Running = p.ServerID, p.UID, p.Running
		a.Runtime = &mcpRuntime{
			ChuotRun: p.ChuotRun, ChuotDoing: p.ChuotDoing, ChuotTotal: p.ChuotTotal,
			Notify: p.Notify, Maohiem: p.Maohiem, Lucdao: p.Lucdao, Yeuthap: p.Yeuthap,
			Peach: p.Peach, XaVe: p.XaVe, Phanthan: p.Phanthan, BottlePrefered: p.BottlePrefered,
		}
		a.Activities = mcpActsOf(&p.ActList)
	case *EventPlayer:
		a.SessionType = "event"
		a.Username, a.IngameName, a.ClanName = p.Username, p.Name, p.ClanName
		a.ServerID, a.GameUID, a.Running = p.ServerID, p.UID, p.Running
		a.Activities = mcpActsOf(&p.ActList)
	default:
		return a
	}
	db := t.ydb.GetDB()
	// Session key == User.ID (regular & members) and == EventUser.ID alike,
	// so one lookup per table enriches both roles; event rows have no VIP.
	if u, err := model.GetUser(db, a.AccountID); err == nil {
		if a.IngameName == "" {
			a.IngameName = u.IngameName
		}
		a.IsLeader, a.AutoLogin = u.IsLeader, u.AutoLogin
		a.VipLevel = u.VipLevel
		a.VipExpireAt = mcpVipStr(u.VipExpireAt)
	} else if eu, err := model.GetEventUser(db, a.AccountID); err == nil {
		if a.IngameName == "" {
			a.IngameName = eu.IngameName
		}
	}
	return a
}

// mcpListLiveAccounts snapshots every running session, sorted by account id
// so repeated calls diff cleanly for the agent.
func (t *TeleBot) mcpListLiveAccounts() []mcpAccount {
	out := []mcpAccount{}
	t.session.Range(func(k, v any) bool {
		key, ok := k.(int64)
		if !ok || !sessionRunning(v) {
			return true
		}
		out = append(out, t.mcpAccountOf(key, v))
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

// mcpAccountByID resolves one account id to its view: live session wins;
// otherwise it falls back to the DB rows so get_user_info can still answer
// (with running=false) about an offline account.
func (t *TeleBot) mcpAccountByID(id string) (mcpAccount, bool) {
	db := t.ydb.GetDB()
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		if v, ok := t.session.Load(n); ok {
			return t.mcpAccountOf(n, v), true
		}
	}
	if u, err := model.GetUser(db, id); err == nil {
		return mcpAccount{
			AccountID: u.ID, SessionType: "player", Username: u.Username,
			IngameName: u.IngameName, ServerID: u.ServerID, GameUID: u.GameUID,
			IsLeader: u.IsLeader, AutoLogin: u.AutoLogin,
			VipLevel: u.VipLevel, VipExpireAt: mcpVipStr(u.VipExpireAt),
		}, true
	}
	if eu, err := model.GetEventUser(db, id); err == nil {
		return mcpAccount{
			AccountID: eu.ID, SessionType: "event", Username: eu.Username,
			IngameName: eu.IngameName, ServerID: eu.ServerID,
		}, true
	}
	return mcpAccount{}, false
}

// ---- tools -------------------------------------------------------------------

type mcpToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

var mcpFeatures = []string{"chuot", "notify", "maohiem", "lucdao", "yeuthap", "peach", "xave"}

var mcpToolDefs = []mcpToolDef{
	{
		Name:        "list_running_users",
		Description: "List every currently-running game account with live state: id, username, in-game name, clan, server, uid, vip, leader linkage, runtime switches, and liveActivities (type/activityId/phase) to pick which online account can play what — e.g. type 341 phase active = Big Chess available on that account's server.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	},
	{
		Name:        "get_user_info",
		Description: "Detailed info for one account by id (live session state when online, DB row when offline). Passwords are never included.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_id": map[string]any{"type": "string", "description": "numeric account id (Telegram chat id for regular users, member/event id otherwise)"},
			},
			"required":             []string{"account_id"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "account_set_toggle",
		Description: "Flip one runtime switch on a RUNNING account: chuot (auto mouse), notify, maohiem, lucdao, yeuthap, peach, xave. The account must be online; refuses otherwise.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_id": map[string]any{"type": "string"},
				"feature":    map[string]any{"type": "string", "enum": mcpFeatures},
				"enabled":    map[string]any{"type": "boolean"},
			},
			"required":             []string{"account_id", "feature", "enabled"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "account_notify",
		Description: "Send a Telegram notification to the chat that owns a RUNNING account (the member's leader for member accounts). Refuses for offline accounts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_id": map[string]any{"type": "string"},
				"text":       map[string]any{"type": "string"},
			},
			"required":             []string{"account_id", "text"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "skytrade_status",
		Description: "Read-only Tiên Vực Thương Đồ (Sky Trade, activity type 150) health report for the account — the exact inputs the auto-caravan runs on: the DB opt-in toggles (auto_sky_trade / auto_sky_trade_speed), every stored type-150 row (phase + begin/end in ICT), the next announced opening, and for the ACTIVE activity a live snapshot: ret, funds, held cargo (goodsId/num/cost), ship (sourcePort/targetPort/startTime/speeds), every port's reported rareGoodsId, the port's specialty from the config table, and the brain's pure verdict (plan: sell-here / buy+sail / sail-to-sell / wait + profit + destination). NEVER buys, sells, sails, claims or speeds — safe while the auto loop runs (if the loop holds the request seat the call simply errors; retry). Diagnoses 'auto thương đồ không chạy' without a test binary.",
		InputSchema: mcpAccountIDSchema,
	},
	{
		Name:        "bigchess_state",
		Description: "Authoritative Big Chess run snapshot for the account: board, player, score, step, gameStatus, spawn preview, consumed-power counters, live config (pieceScores, BIGCHESS_* params) and stateVersion. Errors point to bigchess_enter_game when no run exists.",
		InputSchema: mcpAccountIDSchema,
	},
	{
		Name:        "bigchess_analyze",
		Description: "Ask the external play engine for a deterministic multi-round search (depth-3 default) over the account's CURRENT Big Chess state: recommendation + risk-adjusted alternatives with principal variations. ADVISORY ONLY — the engine cannot see the server-hidden spawn wave (masked to composition) or enemy steps: wrap its advice in the bigchess://rules 'Thinking ahead' wave logic, then execute at most ONE action and re-analyze on the new state. Requires the admin to have run /bcengine on.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_id":   map[string]any{"type": "string"},
				"depth":        map[string]any{"type": "integer", "description": "1..12, default 3"},
				"nodes":        map[string]any{"type": "integer", "description": "1..5000000, default 100000"},
				"movetimeMs":   map[string]any{"type": "integer", "description": "1..30000, default 1000"},
				"multiPV":      map[string]any{"type": "integer", "description": "1..20, default 5"},
				"beamWidth":    map[string]any{"type": "integer", "description": "1..4096, default 256"},
				"riskAversion": map[string]any{"type": "number", "description": "0..3, default 1.0"},
			},
			"required":             []string{"account_id"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "bigchess_enter_game",
		Description: "Start or resume a ranked Big Chess run on the chosen online account (activity type 341 must be inside its live window — find eligible accounts via list_running_users → liveActivities) and return the full board snapshot.",
		InputSchema: mcpAccountIDSchema,
	},
	{
		Name:        "bigchess_move",
		Description: "Send one Big Chess move for the account and return the server-resolved state. Legality (rook geometry, powers) is YOUR job per bigchess://rules — the game server validates and refuses illegal moves with ret!=0. One action per stateVersion.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_id":        map[string]any{"type": "string"},
				"x":                 map[string]any{"type": "integer", "description": "destination column, 0..width-1"},
				"y":                 map[string]any{"type": "integer", "description": "destination row, 0..height-1"},
				"power_on":          map[string]any{"type": "boolean", "description": "pipe: glide through blockers, clear the crossed segment"},
				"power_cross":       map[string]any{"type": "boolean", "description": "consume a cross charge, clear destination row+column"},
				"power_remove_type": map[string]any{"type": "boolean", "description": "remove all pieces sharing the captured target's chessId"},
			},
			"required":             []string{"account_id", "x", "y"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "bigchess_use_random",
		Description: "Spend one random-removal charge (server picks the pieces); returns the fresh state with the removal already applied.",
		InputSchema: mcpAccountIDSchema,
	},
	{
		Name:        "bigchess_revive",
		Description: "Accept the Big Chess revival offer after gameStatus==1. The server chooses the price (item then ingot by BIGCHESS_REBORN_COST) — spend it only when continuing clearly pays.",
		InputSchema: mcpAccountIDSchema,
	},
	{
		Name:        "bigchess_end",
		Description: "Settle the Big Chess run (final score + rank). Also the correct move when step>=winRound or after gameStatus==1 when continuing is not worth the revive cost.",
		InputSchema: mcpAccountIDSchema,
	},
	{
		Name:        "bigchess_practice_enter_game",
		Description: "Start a FREE practice Big Chess run (activity type 341 practice trio 234121-23). Costs no daily starts and no ingots — the right mode for long engine experiments. Shares the run-state slot with ranked: start practice only when no ranked run matters, and note practice moves have no power flags.",
		InputSchema: mcpAccountIDSchema,
	},
	{
		Name:        "bigchess_practice_move",
		Description: "Send one practice move {x,y} (no power fields exist in practice). Returns the server-resolved state; same one-action-per-stateVersion contract as ranked.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_id": map[string]any{"type": "string"},
				"x":          map[string]any{"type": "integer"},
				"y":          map[string]any{"type": "integer"},
			},
			"required":             []string{"account_id", "x", "y"},
			"additionalProperties": false,
		},
	},
	{
		Name:        "bigchess_practice_end",
		Description: "Settle a practice run (always accepted — unlike ranked end it has no winRound gate).",
		InputSchema: mcpAccountIDSchema,
	},
}

// mcpAccountIDSchema is the {account_id}-only input used by every no-argument
// Big Chess tool.
var mcpAccountIDSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"account_id": map[string]any{"type": "string"}},
	"required":             []string{"account_id"},
	"additionalProperties": false,
}

// mcpResourceDefs serves the full game knowledge to the AI: read once, then
// act. Texts are embedded from internal/bigchess (ported from
// ../remoteBigChess's MCP bridge + protocol docs).
var mcpResourceDefs = []map[string]any{
	{"uri": bigchess.RulesURI, "name": "Big Chess rules", "mimeType": "text/markdown",
		"description": "Complete AI decision rules: movement, enemy threat geometry, spawn waves, powers, death/settlement, and the MCP play loop."},
	{"uri": bigchess.ProtocolURI, "name": "Big Chess protocol", "mimeType": "text/markdown",
		"description": "Wire-level command/field reference and the tool contract the bot enforces."},
}

func (t *TeleBot) mcpResourceRead(w http.ResponseWriter, id json.RawMessage, params json.RawMessage) {
	var p struct {
		URI string `json:"uri"`
	}
	_ = json.Unmarshal(params, &p)
	var text string
	switch strings.TrimSpace(p.URI) {
	case bigchess.RulesURI:
		text = bigchess.Rules
	case bigchess.ProtocolURI:
		text = bigchess.ProtocolDoc
	default:
		mcpRPCErrorOut(w, id, -32002, "unknown resource: "+p.URI)
		return
	}
	mcpRPCResult(w, id, map[string]any{
		"contents": []map[string]any{{"uri": p.URI, "mimeType": "text/markdown", "text": text}},
	})
}

func mcpToolText(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}
}

func mcpToolErrf(format string, args ...any) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": fmt.Sprintf(format, args...)}},
		"isError": true,
	}
}

func mcpToolJSON(v any) map[string]any {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcpToolErrf("internal: marshal: %v", err)
	}
	return mcpToolText(string(b))
}

func (t *TeleBot) mcpToolCall(w http.ResponseWriter, id json.RawMessage, params json.RawMessage) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		mcpRPCErrorOut(w, id, -32602, "invalid params: "+err.Error())
		return
	}
	var args map[string]any
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			mcpRPCErrorOut(w, id, -32602, "invalid arguments: "+err.Error())
			return
		}
	}
	argStr := func(k string) string {
		if s, ok := args[k].(string); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}
	argBool := func(k string) bool {
		b, _ := args[k].(bool)
		return b
	}

	var res map[string]any
	switch call.Name {
	case "list_running_users":
		res = mcpToolJSON(t.mcpListLiveAccounts())
	case "get_user_info":
		accountID := argStr("account_id")
		if accountID == "" {
			res = mcpToolErrf("account_id is required")
			break
		}
		a, ok := t.mcpAccountByID(accountID)
		if !ok {
			res = mcpToolErrf("account %q not found", accountID)
			break
		}
		res = mcpToolJSON(a)
	case "account_set_toggle":
		res = t.mcpSetToggle(argStr("account_id"), argStr("feature"), argBool("enabled"))
	case "account_notify":
		res = t.mcpNotify(argStr("account_id"), argStr("text"))
	case "skytrade_status":
		res = t.mcpSkyTradeStatus(argStr("account_id"))
	case "bigchess_state":
		p, fail := t.mcpLivePlayer(argStr("account_id"))
		if fail != "" {
			res = mcpToolErrf("%s", fail)
			break
		}
		view, ok := p.BigChessState()
		if !ok {
			res = mcpToolErrf("big-chess: no run state yet — the account has fetched no 34101/34102 answer; call bigchess_enter_game")
			break
		}
		res = mcpToolJSON(view)
	case "bigchess_enter_game":
		accountID := argStr("account_id")
		p, fail := t.mcpLivePlayer(accountID)
		if fail != "" {
			res = mcpToolErrf("%s", fail)
			break
		}
		view, ok := p.BigChessEnterGame(mcpBigChessTimeout)
		res = bcToolResult(view, ok)
		if ok {
			// Fresh run: the player may have bought the items behind
			// previously refused powers — lift the engine's quarantine.
			t.bcClearRejections(t.bcEngineCfg(), accountID)
		}
	case "bigchess_move":
		accountID := argStr("account_id")
		p, fail := t.mcpLivePlayer(accountID)
		if fail != "" {
			res = mcpToolErrf("%s", fail)
			break
		}
		x, hx := argInt32OK(args, "x")
		y, hy := argInt32OK(args, "y")
		if !hx || !hy {
			res = mcpToolErrf("x and y are required")
			break
		}
		eng, before, haveBefore := t.bcEngineBefore(p)
		moveAction := map[string]any{
			"command": "S_BIG_CHESS_MOVE",
			"params": map[string]any{"activityId": before.ActivityID, "x": x, "y": y,
				"powerOn": argBool("power_on"), "powerCross": argBool("power_cross"),
				"powerRemoveType": argBool("power_remove_type")},
		}
		view, ok := p.BigChessMove(x, y, argBool("power_on"), argBool("power_cross"), argBool("power_remove_type"), mcpBigChessTimeout)
		res = bcToolResult(view, ok)
		if ok {
			t.bcFeedOK(eng, accountID, before, haveBefore, view, moveAction)
		} else if ret, isR := view["ret"].(int32); isR {
			t.bcFeedReject(eng, accountID, before, moveAction, ret)
		}
	case "bigchess_use_random":
		accountID := argStr("account_id")
		p, fail := t.mcpLivePlayer(accountID)
		if fail != "" {
			res = mcpToolErrf("%s", fail)
			break
		}
		eng, before, haveBefore := t.bcEngineBefore(p)
		randAction := map[string]any{
			"command": "S_BIG_CHESS_USE_RANDOM",
			"params":  map[string]any{"activityId": before.ActivityID},
		}
		view, ok := p.BigChessUseRandom(mcpBigChessTimeout)
		res = bcToolResult(view, ok)
		if ok {
			t.bcFeedOK(eng, accountID, before, haveBefore, view, randAction)
		} else if ret, isR := view["ret"].(int32); isR {
			t.bcFeedReject(eng, accountID, before, randAction, ret)
		}
	case "bigchess_revive":
		accountID := argStr("account_id")
		p, fail := t.mcpLivePlayer(accountID)
		if fail != "" {
			res = mcpToolErrf("%s", fail)
			break
		}
		eng, before, haveBefore := t.bcEngineBefore(p)
		reviveAction := map[string]any{
			"command": "S_BIG_CHESS_REVIVE",
			"params":  map[string]any{"activityId": before.ActivityID, "type": 0},
		}
		view, ok := p.BigChessRevive(mcpBigChessTimeout)
		res = bcToolResult(view, ok)
		if ok {
			t.bcFeedOK(eng, accountID, before, haveBefore, view, reviveAction)
		} else if ret, isR := view["ret"].(int32); isR {
			t.bcFeedReject(eng, accountID, before, reviveAction, ret)
		}
	case "bigchess_analyze":
		eng := t.bcEngineCfg()
		if !eng.enabled {
			res = mcpToolErrf("big-chess engine is disabled — the admin must run /bcengine on")
			break
		}
		p, fail := t.mcpLivePlayer(argStr("account_id"))
		if fail != "" {
			res = mcpToolErrf("%s", fail)
			break
		}
		view, ok := p.BigChessState()
		if !ok {
			res = mcpToolErrf("big-chess: no run state yet — call bigchess_enter_game first")
			break
		}
		st, isSt := view["state"].(bigchess.State)
		if !isSt {
			res = mcpToolErrf("internal: unexpected state view type")
			break
		}
		options := map[string]any{}
		for _, k := range []string{"depth", "nodes", "movetimeMs", "multiPV", "beamWidth", "riskAversion"} {
			if f, isNum := args[k].(float64); isNum {
				if k == "riskAversion" {
					options[k] = f
				} else {
					options[k] = int64(f)
				}
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), bcEngineAnalyzeTO)
		data, err := eng.analyze(ctx, st, options)
		cancel()
		if err != nil {
			res = mcpToolErrf("big-chess engine: %v", err)
			break
		}
		res = mcpToolJSON(map[string]any{
			"stateVersion": st.Version,
			"engine":       data,
			"note":         "recommendation is advisory; the engine cannot see the hidden spawn wave — apply bigchess://rules wave logic, send at most ONE action from this state, then re-analyze",
		})
	case "bigchess_end":
		p, fail := t.mcpLivePlayer(argStr("account_id"))
		if fail != "" {
			res = mcpToolErrf("%s", fail)
			break
		}
		view, ok := p.BigChessEnd(mcpBigChessTimeout)
		res = bcToolResult(view, ok)
	case "bigchess_practice_enter_game":
		accountID := argStr("account_id")
		p, fail := t.mcpLivePlayer(accountID)
		if fail != "" {
			res = mcpToolErrf("%s", fail)
			break
		}
		view, ok := p.BigChessPracticeEnterGame(mcpBigChessTimeout)
		res = bcToolResult(view, ok)
		if ok {
			t.bcClearRejections(t.bcEngineCfg(), accountID)
		}
	case "bigchess_practice_move":
		accountID := argStr("account_id")
		p, fail := t.mcpLivePlayer(accountID)
		if fail != "" {
			res = mcpToolErrf("%s", fail)
			break
		}
		x, hx := argInt32OK(args, "x")
		y, hy := argInt32OK(args, "y")
		if !hx || !hy {
			res = mcpToolErrf("x and y are required")
			break
		}
		eng, before, haveBefore := t.bcEngineBefore(p)
		moveAction := map[string]any{
			"command": "S_BIG_CHESS_PRACTICE_MOVE",
			"params":  map[string]any{"activityId": before.ActivityID, "x": x, "y": y},
		}
		view, ok := p.BigChessPracticeMove(x, y, mcpBigChessTimeout)
		res = bcToolResult(view, ok)
		if ok {
			t.bcFeedOK(eng, accountID, before, haveBefore, view, moveAction)
		} else if ret, isR := view["ret"].(int32); isR {
			t.bcFeedReject(eng, accountID, before, moveAction, ret)
		}
	case "bigchess_practice_end":
		p, fail := t.mcpLivePlayer(argStr("account_id"))
		if fail != "" {
			res = mcpToolErrf("%s", fail)
			break
		}
		view, ok := p.BigChessPracticeEnd(mcpBigChessTimeout)
		res = bcToolResult(view, ok)
	default:
		mcpRPCErrorOut(w, id, -32602, "unknown tool: "+call.Name)
		return
	}
	mcpRPCResult(w, id, res)
}

// mcpSetToggle flips one runtime switch on a live running *Player — the MCP
// twin of webToggle / the runtime half of webMemberAction's rttoggle. It
// deliberately shares no code path with persistence: these switches only
// live on the session, same rule as the web page.
func (t *TeleBot) mcpSetToggle(accountID, feature string, enabled bool) map[string]any {
	if accountID == "" || feature == "" {
		return mcpToolErrf("account_id and feature are required")
	}
	n, err := strconv.ParseInt(accountID, 10, 64)
	if err != nil {
		return mcpToolErrf("account_id must be numeric")
	}
	v, ok := t.session.Load(n)
	if !ok {
		return mcpToolErrf("account %s has no session — it is offline", accountID)
	}
	if !sessionRunning(v) {
		return mcpToolErrf("account %s session exists but is not running", accountID)
	}
	p, ok := v.(*Player)
	if !ok {
		return mcpToolErrf("account %s is an event session — runtime toggles do not apply", accountID)
	}
	switch feature {
	case "chuot":
		p.ChuotRun = enabled
	case "notify":
		p.Notify = enabled
	case "maohiem":
		p.Maohiem = enabled
	case "lucdao":
		p.Lucdao = enabled
	case "yeuthap":
		p.Yeuthap = enabled
	case "peach":
		p.Peach = enabled
	case "xave":
		p.XaVe = enabled
	default:
		return mcpToolErrf("unknown feature %q (want one of: %s)", feature, strings.Join(mcpFeatures, ", "))
	}
	// Keep the open dashboards honest — same live repaint the web toggle does.
	t.pushWebState(n)
	return mcpToolJSON(map[string]any{"account_id": accountID, "feature": feature, "enabled": enabled, "ok": true})
}

// mcpNotify pushes a message to the owning chat of a running account. The
// session already carries the right target (p.ChatID is the leader's chat
// for members, the user's own otherwise), so no role branching is needed.
func (t *TeleBot) mcpNotify(accountID, text string) map[string]any {
	if accountID == "" || text == "" {
		return mcpToolErrf("account_id and text are required")
	}
	n, err := strconv.ParseInt(accountID, 10, 64)
	if err != nil {
		return mcpToolErrf("account_id must be numeric")
	}
	v, ok := t.session.Load(n)
	if !ok || !sessionRunning(v) {
		return mcpToolErrf("account %s is not online", accountID)
	}
	var chatID int64
	switch p := v.(type) {
	case *Player:
		chatID = p.ChatID
	case *EventPlayer:
		chatID = p.ChatID
	}
	if chatID == 0 {
		return mcpToolErrf("account %s has no notification chat", accountID)
	}
	t.pushNotify(chatID, text)
	return mcpToolJSON(map[string]any{"account_id": accountID, "sent_to_chat": chatID, "ok": true})
}

// ---- Sky Trade (Thương Đồ Tiên Vực, type 150) live check ----------------------
//
// mcpSkyTradeStatus is the deployed twin of the read-only diag probe
// (player_thiendo_diag_live_test.go): it answers "vì sao auto thương đồ
// không chạy" on a LIVE session without any test binary — the DB toggles
// are the account's real rows, the activity rows are the ones the running
// loop actually sees, and the snapshot is the same ENTER_MAP the loop
// polls. Pure reads only: NEVER stSellAll/stBuyAndGo/stSail/stClaimArrival/
// stSpeedUp. If the loop holds the SkyTrade request seat concurrently the
// snapshot simply times out — the tool reports it as snapshotError.
func (t *TeleBot) mcpSkyTradeStatus(accountID string) map[string]any {
	p, fail := t.mcpLivePlayer(accountID)
	if fail != "" {
		return mcpToolErrf("%s", fail)
	}
	now := time.Now()
	ict := func(ms int64) string {
		if ms == 0 {
			return ""
		}
		return time.UnixMilli(ms).In(ictZone).Format("2006-01-02 15:04:05")
	}
	out := map[string]any{
		"account_id":           accountID,
		// The loop's OWN truth: Player fields, not a DB row lookup (several
		// rows can share one game_uid — the session is the single source).
		"auto_sky_trade":       p.AutoSkyTrade,
		"auto_sky_trade_speed": p.AutoSkyTradeSpeed,
		"now_ict":              now.In(ictZone).Format("2006-01-02 15:04:05"),
	}
	rows := []map[string]any{}
	for _, e := range p.ActList.byType(skyTradeActivityType) {
		rows = append(rows, map[string]any{
			"activityId": e.ActivityID, "phase": e.status(now),
			"beginIct": ict(e.BeginTime), "endIct": ict(e.EndTime),
		})
	}
	out["rows"] = rows
	if begin, found := p.stNextOpening(now); found {
		out["nextOpeningIct"] = time.UnixMilli(begin).In(ictZone).Format("2006-01-02 15:04:05")
	}
	actID, ok := p.stActiveActivity()
	if !ok {
		out["active"] = false
		return mcpToolJSON(out)
	}
	out["active"] = true
	out["activeActivityId"] = actID
	snap, err := p.stSnapshot(actID, skyTradeReqTimeout)
	if err != nil {
		out["snapshotError"] = err.Error()
		return mcpToolJSON(out)
	}
	out["ret"] = snap.GetRet()
	if snap.GetRet() != 0 {
		return mcpToolJSON(out) // server's own trading window closed — the loop would log the refuse code
	}
	pd := snap.GetPlayerData()
	out["funds"] = pd.GetAvailableFunds()
	goods := []map[string]any{}
	var held []int32
	for _, g := range pd.GetGoods() {
		goods = append(goods, map[string]any{"goodsId": g.GetGoodsId(), "num": g.GetNum(), "cost": g.GetPrice()})
		held = append(held, g.GetGoodsId())
	}
	out["cargo"] = goods
	ports := []map[string]any{}
	for _, m := range snap.GetPortGoods() {
		ports = append(ports, map[string]any{"portId": m.GetPortId(), "rareGoodsId": m.GetRareGoodsId(), "listed": len(m.GetGoods())})
	}
	out["ports"] = ports
	if ship := pd.GetAirshipInfo(); ship != nil {
		out["ship"] = map[string]any{
			"sourcePort": ship.GetSourcePort(), "targetPort": ship.GetTargetPort(),
			"startTime": ship.GetStartTime(), "speeds": ship.GetSpeeds(),
		}
		out["specialtyOfSource"] = p.stSpecialGood(ship.GetSourcePort())
		// The brain's verdict, computed the same pure way the loop does it —
		// only meaningful while docked with no pending arrival report.
		if ship.GetTargetPort() == 0 && snap.GetEvent().GetEventType() == 0 {
			plan := skyTradeDecide(ship.GetSourcePort(), pd.GetAvailableFunds(), pd.GetGoods(),
				snap.GetPortGoods(), p.stSpecialGood(ship.GetSourcePort()))
			out["plan"] = map[string]any{
				"desc": stPlanDesc(plan), "action": plan.action,
				"profit": plan.profit, "destPort": plan.destPort,
			}
		} else if snap.GetEvent().GetEventType() != 0 {
			out["pendingArrivalClaim"] = true // the loop claims this on its next wake
		}
	}
	return mcpToolJSON(out)
}

// ---- Big Chess tool plumbing --------------------------------------------------

// mcpBigChessTimeout bounds one relayed game request: answers are request/
// response on the live socket, so anything slower than this means the
// reader loop or the game server is stuck, not that the move is "thinking".
const mcpBigChessTimeout = 20 * time.Second

// mcpLivePlayer resolves account_id to a running *Player with the same
// session preamble every mutating tool uses (Load → sessionRunning →
// type switch). Returns an AI-readable refusal instead.
func (t *TeleBot) mcpLivePlayer(accountID string) (*Player, string) {
	if accountID == "" {
		return nil, "account_id is required"
	}
	n, err := strconv.ParseInt(accountID, 10, 64)
	if err != nil {
		return nil, "account_id must be numeric"
	}
	v, ok := t.session.Load(n)
	if !ok || !sessionRunning(v) {
		return nil, fmt.Sprintf("account %s is not online", accountID)
	}
	p, ok := v.(*Player)
	if !ok {
		return nil, fmt.Sprintf("account %s is an event session — Big Chess runs on regular accounts", accountID)
	}
	return p, ""
}

// bcToolResult adapts the player-layer (view, ok) pairs: on failure the view
// map carries the server-layer error under "error".
func bcToolResult(view map[string]any, ok bool) map[string]any {
	if ok {
		return mcpToolJSON(view)
	}
	msg := "big-chess request failed"
	if view != nil {
		if s, isStr := view["error"].(string); isStr {
			msg = s
		}
	}
	return mcpToolErrf("%s", msg)
}

func argInt32OK(m map[string]any, k string) (int32, bool) {
	f, ok := m[k].(float64)
	if !ok {
		return 0, false
	}
	return int32(f), true
}

// ---- /mcp admin command -------------------------------------------------------

// MCPConfigHandler is the admin switchboard for the MCP endpoint, mirroring
// /web: /mcp on|off flips the gate, /mcp key sets (or generates) the shared
// auth key, /mcp clear kills it (endpoint goes 503 again), /mcp status
// reports. The key only ever appears in the admin's own Telegram chat,
// never through the endpoint itself.
func (t *TeleBot) MCPConfigHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !t.isAdmin(message.Chat.ID) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          message.Chat.ID,
			Text:            "admin only",
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
		return
	}
	send := func(text string) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: message.Chat.ID, Text: text, ParseMode: models.ParseModeHTML,
			ReplyParameters: &models.ReplyParameters{MessageID: message.ID},
		})
	}
	args := strings.Fields(message.Text)
	db := t.ydb.GetDB()
	usage := "usage:\n/mcp on|off\n/mcp key <key> | /mcp key generate\n/mcp clear\n/mcp status"
	if len(args) < 2 {
		send(usage)
		return
	}
	switch strings.ToLower(args[1]) {
	case "on", "off":
		on := strings.EqualFold(args[1], "on")
		if err := model.SetConfig(db, model.ConfigKeyMCPEnabled, strconv.FormatBool(on)); err != nil {
			send("failed: " + err.Error())
			return
		}
		state := "TẮT"
		if on {
			state = "BẬT"
		}
		note := ""
		if on && t.mcpAuthKey() == "" {
			note = "\n(chưa có key — endpoint còn 503 cho tới khi /mcp key <key>)"
		}
		send("MCP " + state + note)
	case "key":
		if len(args) != 3 {
			send(usage)
			return
		}
		key := args[2]
		if strings.EqualFold(key, "generate") {
			key = webRandomHex(32)
		}
		if len(key) < 8 {
			send("key phải dài ít nhất 8 ký tự")
			return
		}
		if len(key) > 128 {
			send("key quá dài (max 128)")
			return
		}
		if err := model.SetConfig(db, model.ConfigKeyMCPAuthKey, key); err != nil {
			send("failed: " + err.Error())
			return
		}
		send(fmt.Sprintf("MCP key đã đặt (có hiệu lực ngay):\n<code>%s</code>", key))
	case "clear":
		if err := model.SetConfig(db, model.ConfigKeyMCPAuthKey, ""); err != nil {
			send("failed: " + err.Error())
			return
		}
		send("đã xoá MCP key — endpoint trả 503")
	case "status":
		key := t.mcpAuthKey()
		shown := "(chưa đặt)"
		if key != "" {
			shown = fmt.Sprintf("%s… len=%d", key[:min(4, len(key))], len(key))
		}
		enabled := "TẮT"
		if t.mcpEnabled() {
			enabled = "BẬT"
		}
		send(fmt.Sprintf("MCP: %s\nkey: %s\nlisten: <code>%s/mcp</code>\nchạy: POST + header <code>X-MCP-Key</code> hoặc <code>Authorization: Bearer &lt;key&gt;</code>",
			enabled, shown, mcpBind()))
	default:
		send(usage)
	}
}
