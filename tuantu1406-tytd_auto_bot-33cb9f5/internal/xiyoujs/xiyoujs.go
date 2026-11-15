// Package xiyoujs embeds the XiYou (Tây Du Phục Ma) client combat sim —
// the game's own browserify modules + DB TextAssets — and runs it in goja,
// a pure-Go JS engine. This removes the node dependency from the bot host
// (owner 2026-11-05: "nhúng js đó vào go không cài thêm node có được không").
//
// One goja runtime is shared bot-wide behind a mutex (goja is not
// concurrency-safe; boss sim rounds are ~1/s server-paced anyway). The
// runtime is RELEASED (set nil → GC) after idle so RAM returns to zero
// while nobody farms a boss — same policy as the node daemon.
package xiyoujs

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

//go:embed assets/index.464ca.js
var bundleSrc string

//go:embed assets/bignumber.js
var bignumberSrc string

//go:embed assets/logicdbmgr.module.js
var extLogicDBMgr string

//go:embed assets/stringutil.module.js
var extStringUtil string

//go:embed assets/glue.js
var glueSrc string

//go:embed assets/db
var dbFS embed.FS

func loadDBs() (map[string]string, error) {
	entries, err := dbFS.ReadDir("assets/db")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := dbFS.ReadFile(path.Join("assets/db", e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = string(b)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no db assets")
	}
	return out, nil
}

// Result mirrors the harness JSON line.
type Result struct {
	Result      int
	ResultName  string
	Levels      []int32
	Frames      int
	Ms          int
	EnemyHpLeft float64 // boss+wave HP left at end (loadout hill-climb proxy)
	EnemyHpMax  float64 // sum MaxHp of spawned roles — denominator for left%
}

var (
	mu      sync.Mutex
	vm      *goja.Runtime
	fnSim   goja.Callable
	lastUse time.Time
	idler   *time.Timer
	failN   int
	broken  bool // goja can't run this bundle → caller should use the node daemon
)

const idleRelease = 2 * time.Minute

// ErrBroken: goja path failed repeatedly; the caller should fall back.
var ErrBroken = fmt.Errorf("xiyoujs: runtime broken")

func registrySlice() (string, error) {
	start := indexOf(bundleSrc, "}({", 0)
	if start < 0 {
		return "", fmt.Errorf("registry start not found")
	}
	start += 3
	end := indexOf(bundleSrc, "},{},[", start)
	if end < 0 {
		return "", fmt.Errorf("registry end not found")
	}
	return bundleSrc[start:end], nil
}

func indexOf(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	i := -1
	for j := from; j+len(sub) <= len(s); j++ {
		if s[j:j+len(sub)] == sub {
			i = j
			break
		}
	}
	return i
}

func mustLit(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// newVM builds an INDEPENDENT goja runtime (own heap, own loaded bundle).
// goja.Runtime is goroutine-unsafe, so parallel sims require one Runtime per
// concurrent worker — newVM is the pool primitive (measured in
// concurrency_test.go: ~? MiB and ~? s each — see TestConcurrencyBench).
func newVM() (*goja.Runtime, goja.Callable, error) {
	reg, err := registrySlice()
	if err != nil {
		return nil, nil, err
	}
	dbs, err := loadDBs()
	if err != nil {
		return nil, nil, err
	}
	dbLit, err := json.Marshal(dbs)
	if err != nil {
		return nil, nil, err
	}
	glue := glueSrc
	glue = replaceAll(glue, "__BIGNUMBER_SRC", mustLit(bignumberSrc))
	glue = replaceAll(glue, "__REGISTRY_SLICE", mustLit(reg))
	glue = replaceAll(glue, "__EXT_LOGICDBMGR", mustLit(extLogicDBMgr))
	glue = replaceAll(glue, "__EXT_STRINGUTIL", mustLit(extStringUtil))
	glue = replaceAll(glue, "__DB_TEXTS", string(dbLit))
	r := goja.New()
	if _, err := r.RunString(glue); err != nil {
		return nil, nil, fmt.Errorf("glue: %w", err)
	}
	fn, ok := goja.AssertFunction(r.Get("xiyouSimJSON"))
	if !ok {
		return nil, nil, fmt.Errorf("xiyouSimJSON missing")
	}
	return r, fn, nil
}

func start() error {
	r, fn, err := newVM()
	if err != nil {
		return err
	}
	vm, fnSim = r, fn
	log.Printf("[XiYou] sim goja lên (nhúng trong binary, idle %v)", idleRelease)
	return nil
}

func replaceAll(s, old, new string) string {
	out := ""
	for {
		i := indexOf(s, old, 0)
		if i < 0 {
			return out + s
		}
		out += s[:i] + new
		s = s[i+len(old):]
	}
}

// Sim replays one fight: role = protojson'd XiYouPlayerDataMsg.info.
func Sim(role json.RawMessage, stage, seed int32) (*Result, error) {
	mu.Lock()
	defer mu.Unlock()
	if broken {
		return nil, ErrBroken
	}
	if vm == nil {
		if err := start(); err != nil {
			failN++
			if failN >= 2 {
				broken = true
			}
			return nil, fmt.Errorf("%w (lần %d)", err, failN)
		}
		failN = 0
	}
	lastUse = time.Now()
	if idler != nil {
		idler.Stop()
	}
	// watchdog: a healthy fight simulates in ~0.1s; if one ever wedges
	// (2026-11-05: live role stalled the goja call >3min) kill the JS via
	// interrupt, drop the runtime → caller falls back to the node daemon.
	watched := vm
	wd := time.AfterFunc(30*time.Second, func() { watched.Interrupt("sim watchdog") })
	t0 := time.Now()
	v, err := callSafe(fnSim,
		vm.ToValue(string(role)), vm.ToValue(float64(stage)), vm.ToValue(float64(seed)), vm.ToValue(float64(45000)))
	wd.Stop()
	if err != nil {
		log.Printf("[XiYou] goja sim fail stage=%d seed=%d sau %v: %v — fallback node", stage, seed, time.Since(t0), err)
		// runtime may be wedged — drop it, next call rebuilds
		vm, fnSim = nil, nil
		failN++
		if failN >= 3 {
			broken = true
		}
		return nil, fmt.Errorf("sim call: %w", err)
	}
	m, ok := v.Export().(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("sim output not an object")
	}
	if e, _ := m["error"].(string); e != "" {
		return &Result{ResultName: "ERR", Result: -1}, fmt.Errorf("sim: %s", e)
	}
	res := &Result{}
	if f, ok := m["result"].(int64); ok {
		res.Result = int(f)
	} else if f, ok := m["result"].(float64); ok {
		res.Result = int(f)
	}
	res.ResultName, _ = m["resultName"].(string)
	if f, ok := m["ms"].(int64); ok {
		res.Ms = int(f)
	} else if f, ok := m["ms"].(float64); ok {
		res.Ms = int(f)
	}
	if f, ok := m["frames"].(int64); ok {
		res.Frames = int(f)
	} else if f, ok := m["frames"].(float64); ok {
		res.Frames = int(f)
	}
	if f, ok := m["enemyHpLeft"].(float64); ok {
		res.EnemyHpLeft = f
	} else if f, ok := m["enemyHpLeft"].(int64); ok {
		res.EnemyHpLeft = float64(f)
	}
	if f, ok := m["enemyHpMax"].(float64); ok {
		res.EnemyHpMax = f
	} else if f, ok := m["enemyHpMax"].(int64); ok {
		res.EnemyHpMax = float64(f)
	}
	if lv, ok := m["levels"].([]interface{}); ok {
		for _, x := range lv {
			switch n := x.(type) {
			case int64:
				res.Levels = append(res.Levels, int32(n))
			case float64:
				res.Levels = append(res.Levels, int32(n))
			}
		}
	}
	idler = time.AfterFunc(idleRelease, func() {
		mu.Lock()
		if time.Since(lastUse) >= idleRelease {
			vm, fnSim = nil, nil
			log.Printf("[XiYou] sim goja nghỉ (idle) — RAM trả về GC")
		}
		mu.Unlock()
	})
	return res, nil
}

// ─── Ảo Cảnh (Secret) session API ─────────────────────────────────────────
//
// The client cannot run a secret run headlessly in one shot: between rooms
// it asks the SERVER which doors exist (Select resp.doorIds) and only then
// fights the chosen template. So the sim is a stateful session the Go
// orchestrator steps: Create → Step (until room clear / end) → EnterDoor /
// Export+Rebuild (last room) → Step → Kill with Export+levels. All session
// calls ride the same single runtime + mutex as Sim (goja is not
// concurrency-safe; one secret run per bot-wide sender anyway).

// SecretStep is the result of one sim burst inside the vm.
type SecretStep struct {
	Status     string  // "room" | "end" | "frames" (frame cap hit)
	GameStatus int     // IXiYouCombatResult (2 = AllEnemyDie = win)
	Progress   string  // curProgress (string math, as on the wire)
	Levels     []int32 // RNG counters — send verbatim on Kill (from the REBUILT combat)
	Ms         int
	Frames     int
	// EnemyHpLeft/Max — máu địch còn/tối đa tại frame cuối của step (boss
	// room = đúng % máu boss; room thường mob đã chết = xấp xỉ).
	EnemyHpLeft float64
	EnemyHpMax  float64
}

func secretCall(name string, args ...interface{}) (map[string]interface{}, error) {
	mu.Lock()
	defer mu.Unlock()
	if broken {
		return nil, ErrBroken
	}
	if vm == nil {
		if err := start(); err != nil {
			failN++
			if failN >= 2 {
				broken = true
			}
			return nil, fmt.Errorf("%w (lần %d)", err, failN)
		}
		failN = 0
	}
	lastUse = time.Now()
	if idler != nil {
		idler.Stop()
	}
	fn, ok := goja.AssertFunction(vm.Get(name))
	if !ok {
		return nil, fmt.Errorf("xiyoujs: %s missing", name)
	}
	var gargs []goja.Value
	for _, a := range args {
		gargs = append(gargs, vm.ToValue(a))
	}
	watched := vm
	wd := time.AfterFunc(30*time.Second, func() { watched.Interrupt("secret watchdog") })
	v, err := callSafe(fn, gargs...)
	wd.Stop()
	if err != nil {
		log.Printf("[XiYou] goja %s fail sau: %v — fallback node", name, err)
		vm, fnSim = nil, nil
		failN++
		if failN >= 3 {
			broken = true
		}
		return nil, fmt.Errorf("%s call: %w", name, err)
	}
	m, _ := v.Export().(map[string]interface{})
	if m == nil {
		return nil, fmt.Errorf("%s: output not an object", name)
	}
	if e, _ := m["error"].(string); e != "" {
		return nil, fmt.Errorf("%s: %s", name, e)
	}
	armSecretIdler()
	return m, nil
}

func armSecretIdler() {
	idler = time.AfterFunc(idleRelease, func() {
		mu.Lock()
		if time.Since(lastUse) >= idleRelease {
			vm, fnSim = nil, nil
			log.Printf("[XiYou] sim goja nghỉ (idle) — RAM trả về GC")
		}
		mu.Unlock()
	})
}

func gi32(v interface{}) int32 {
	switch n := v.(type) {
	case int64:
		return int32(n)
	case float64:
		return int32(n)
	}
	return 0
}

// SecretCreate starts a run: role = protojson'd XiYouPlayerDataMsg.info
// (same shape as Sim), tpl = the room template (last selectedDoorsIds from
// the Start resp), seed = the server's UPDATE_SEED answer.
func SecretCreate(role json.RawMessage, tpl, seed, timeLimit int32) (int, error) {
	m, err := secretCall("xiyouSecretCreate", string(role), float64(tpl), float64(seed), float64(timeLimit))
	if err != nil {
		return 0, err
	}
	return int(gi32(m["id"])), nil
}

func SecretRunStep(id int) (*SecretStep, error) {
	m, err := secretCall("xiyouSecretStep", float64(id))
	if err != nil {
		return nil, err
	}
	st := &SecretStep{GameStatus: int(gi32(m["gameStatus"]))}
	st.Status, _ = m["status"].(string)
	st.Progress, _ = m["progress"].(string)
	st.Ms = int(gi32(m["ms"]))
	st.Frames = int(gi32(m["frames"]))
	if f, ok := m["enemyHpLeft"].(float64); ok {
		st.EnemyHpLeft = f
	} else if f, ok := m["enemyHpLeft"].(int64); ok {
		st.EnemyHpLeft = float64(f)
	}
	if f, ok := m["enemyHpMax"].(float64); ok {
		st.EnemyHpMax = f
	} else if f, ok := m["enemyHpMax"].(int64); ok {
		st.EnemyHpMax = float64(f)
	}
	if lv, ok := m["levels"].([]interface{}); ok {
		for _, x := range lv {
			st.Levels = append(st.Levels, gi32(x))
		}
	}
	return st, nil
}

func SecretEnterDoor(id int, tpl int32) error {
	_, err := secretCall("xiyouSecretEnterDoor", float64(id), float64(tpl))
	return err
}

func SecretExport(id int) (string, error) {
	m, err := secretCall("xiyouSecretExport", float64(id))
	if err != nil {
		return "", err
	}
	s, _ := m["data"].(string)
	if s == "" {
		return "", fmt.Errorf("secretExport empty")
	}
	return s, nil
}

func SecretRebuild(id int, exportJSON string, tpl, bossCand int32, completed []int32) error {
	b, _ := json.Marshal(completed)
	_, err := secretCall("xiyouSecretRebuild", float64(id), exportJSON, float64(tpl), float64(bossCand), string(b))
	return err
}

func SecretDestroy(id int) {
	_, _ = secretCall("xiyouSecretDestroy", float64(id))
}

func callSafe(fn goja.Callable, args ...goja.Value) (v goja.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("goja panic: %v", r)
		}
	}()
	return fn(goja.Undefined(), args...)
}
