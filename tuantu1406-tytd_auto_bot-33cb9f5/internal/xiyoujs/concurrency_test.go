package xiyoujs

import (
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// Owner question 2026-11-05: "kiểm tra xem server js có chạy concurrency
// được không". Facts to measure:
//   - Sim() (global single Runtime + mutex) → correct but SERIALIZED.
//   - Independent Runtimes (newVM) → can run truly parallel (goja Runtime
//     itself is goroutine-UNSAFE, so parallelism needs one Runtime per
//     worker). Cost per extra Runtime = load time + heap (RAM policy!).
// Gate behind role file so CI without dumps skips.
func TestConcurrencyBench(t *testing.T) {
	raw, err := os.ReadFile("/tmp/xiyou_role_71056440126.json")
	if err != nil {
		t.Skip("no role dump")
	}
	const (
		stage = int32(190307) // vietdart hard wall — TimeOut 45s fight (đậm frames)
		seed  = int32(424242)
		n     = 40 // sims per batch
	)

	// 1) serial baseline through Sim() (single runtime, mutex)
	t0 := time.Now()
	var want *Result
	for i := 0; i < n; i++ {
		want, err = Sim(raw, stage, seed)
		if err != nil {
			t.Fatalf("serial Sim: %v", err)
		}
	}
	serial := time.Since(t0)
	t.Logf("serial  Sim x%d: %v (%v/sim)", n, serial, serial/n)

	// 2) concurrent through Sim() — same global runtime: must stay correct
	// but wall ≈ serial (mutex serializes)
	t0 = time.Now()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n/8; i++ {
				r, err := Sim(raw, stage, seed)
				if err != nil || r.Result != want.Result {
					t.Errorf("concurrent Sim: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	t.Logf("mutex   x%d/8gor: %v — expect ≈serial (serialized by design)", n, time.Since(t0))

	// 3) independent runtimes: cost to build, then parallel sims
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	const workers = 4
	rts := make([]*goja.Runtime, workers)
	fns := make([]goja.Callable, workers)
	t0 = time.Now()
	for i := 0; i < workers; i++ {
		rt, fn, err := newVM()
		if err != nil {
			t.Fatalf("newVM %d: %v", i, err)
		}
		rts[i], fns[i] = rt, fn
		runtime.ReadMemStats(&m1)
		t.Logf("newVM #%d: heap %d→%d MiB (+%.1f)", i, m0.HeapAlloc>>20, m1.HeapAlloc>>20,
			float64(m1.HeapAlloc-m0.HeapAlloc)/(1<<20))
		m0 = m1
	}
	t.Logf("build %d runtimes: %v", workers, time.Since(t0))

	t0 = time.Now()
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rt, fn := rts[g], fns[g]
			for i := 0; i < n/workers; i++ {
				v, err := fn(goja.Undefined(),
					rt.ToValue(string(raw)), rt.ToValue(float64(stage)),
					rt.ToValue(float64(seed)), rt.ToValue(float64(45000)))
				if err != nil {
					t.Errorf("rt %d sim: %v", g, err)
					return
				}
				m, _ := v.Export().(map[string]interface{})
				if r, _ := m["result"].(int64); int(r) != want.Result {
					t.Errorf("rt %d result %v ≠ %v", g, m["result"], want.Result)
				}
			}
		}(g)
	}
	wg.Wait()
	t.Logf("parallel %d-RT x%d: %v — expect ≈serial/%d if it scales", workers, n, time.Since(t0), workers)

	// drop the extra runtimes → GC (idle policy proof)
	rts, fns = nil, nil
	runtime.GC()
	runtime.ReadMemStats(&m1)
	t.Logf("after drop: heap %d MiB", m1.HeapAlloc>>20)
}
