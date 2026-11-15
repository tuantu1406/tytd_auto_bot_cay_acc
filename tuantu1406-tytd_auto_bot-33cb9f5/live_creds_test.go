package tytd_go

// Live-test credentials come from the environment ONLY — the test account
// (user/pass) was hardcoded in ~100 live test files and is fully removed from
// the repo. Set them when running live probes:
//
//	TYTD_LIVE=1 TYTD_LIVE_USER=<acc> TYTD_LIVE_PASS=<pass> \
//	  go test -count=1 -tags "sqlcipher live" -run TestLiveXxx -v -timeout 180s .
//
// Without them every live test skips cleanly (even with TYTD_LIVE=1), so CI
// and plain `go test` never touch a real account.

import (
	"os"
	"strings"
	"testing"
)

func liveUserEnv(t *testing.T) string {
	t.Helper()
	return mustLiveCred(t, "TYTD_LIVE_USER")
}

func livePassEnv(t *testing.T) string {
	t.Helper()
	return mustLiveCred(t, "TYTD_LIVE_PASS")
}

func mustLiveCred(t *testing.T, key string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		t.Skipf("%s is not set — live test needs the test account (see live_creds_test.go)", key)
	}
	return v
}
