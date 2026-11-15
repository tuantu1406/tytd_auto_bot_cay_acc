package tytd_go

// The 512 MB VPS guard: parse /proc/meminfo exactly ("MemTotal:  NNNN kB")
// and never clobber an operator-set limit.

import "testing"

func TestProcMemTotalMB(t *testing.T) {
	mb, ok := procMemTotalMB()
	if !ok {
		t.Skip("no /proc/meminfo on this platform")
	}
	if mb < 128 || mb > 1<<20 {
		t.Fatalf("absurd MemTotal: %d MB", mb)
	}
}

func TestAutoMemLimitEnvWins(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "200MiB")
	if v := autoMemLimit(); v != 0 {
		t.Fatalf("GOMEMLIMIT set but autoMemLimit = %d", v)
	}
	t.Setenv("GOMEMLIMIT", "")
	t.Setenv("MEMLIMIT", "200MiB")
	if v := autoMemLimit(); v != 0 {
		t.Fatalf("MEMLIMIT set but autoMemLimit = %d", v)
	}
}

func TestAutoMemLimitRange(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	t.Setenv("MEMLIMIT", "")
	v := autoMemLimit()
	if v == 0 {
		t.Skip("no /proc/meminfo or box > 1200 MB — default left alone")
	}
	// Only engages under 1200 MB total; target = 60 % of that.
	if v < 64<<20 || v > 720<<20 {
		t.Fatalf("limit out of expected range: %d bytes", v)
	}
}
