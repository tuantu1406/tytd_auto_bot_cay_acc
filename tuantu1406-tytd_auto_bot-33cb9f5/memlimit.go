package tytd_go

// GC target for small VPSes.
//
// The user runs the bot on a 512 MB VPS. With Go's default GOGC=100 the heap
// floats at ~2× live set, so transient bursts (a kunlun /kunlun_map render
// decodes ≈40 MB before GC; SSE ticks churn too) inflate RSS until the
// kernel's OOM killer takes the bot down. When the box is small and the
// operator hasn't set a limit themselves, aim the whole process heap at 60 %
// of physical RAM — GC then works harder to stay inside it instead of the
// process getting killed.
// An explicit GOMEMLIMIT (Go ≤1.24) / MEMLIMIT (Go ≥1.25) env always wins.
//
// The limit is a soft target, not a cap: steady state on bigger boxes is
// untouched (we only engage under ~1.2 GB total RAM).

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// autoMemLimit is the target in bytes, or 0 = leave Go's default alone
// (operator env present, unknown platform, or a box big enough anyway).
func autoMemLimit() int64 {
	if os.Getenv("GOMEMLIMIT") != "" || os.Getenv("MEMLIMIT") != "" {
		return 0
	}
	mb, ok := procMemTotalMB()
	if !ok || mb <= 0 || mb > 1200 {
		return 0
	}
	return int64(mb) * 6 / 10 * 1048576
}

func init() {
	if v := autoMemLimit(); v > 0 {
		debug.SetMemoryLimit(v)
	}
}

// procMemTotalMB reads MemTotal from /proc/meminfo (first line, "kB").
// Returns !ok on non-Linux (dev mac) or any parse failure — no limit then.
func procMemTotalMB() (int, bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	line, _, _ := strings.Cut(string(b), "\n")
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "MemTotal:" {
		return 0, false
	}
	kb, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return kb / 1024, true
}
