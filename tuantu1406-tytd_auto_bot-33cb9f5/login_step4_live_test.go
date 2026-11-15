//go:build live

package tytd_go

// HTTP-only step 1→4 live check (NO websocket — cannot kick a running
// session). Proves the step-2 encoding parity fix (decodeURIComponent
// semantics, '+' survives) end to end against the real gateway.
//
//	TYTD_LIVE=1 TYTD_STEP4=1 go test -tags "sqlcipher live" -count=1 -run TestStep4HTTPOnly -v -timeout 120s .

import (
	"os"
	"strconv"
	"testing"
	"tytd_go/internal/request"
)

func TestStep4HTTPOnly(t *testing.T) {
	if os.Getenv("TYTD_LIVE") == "" || os.Getenv("TYTD_STEP4") == "" {
		t.Skip("set TYTD_LIVE=1 TYTD_STEP4=1")
	}
	user, pass := liveUserEnv(t), livePassEnv(t)
	if v := os.Getenv("TYTD_LIVE_USER"); v != "" {
		user, pass = v, os.Getenv("TYTD_LIVE_PASS")
	}
	server := 710547
	if v := os.Getenv("TYTD_LIVE_SERVER"); v != "" {
		n, _ := strconv.Atoi(v)
		server = n
	}
	pt, err := request.LoginGPassport(user, pass)
	if err != nil {
		t.Fatalf("b1: %v", err)
	}
	sd, uidF, err := request.LoginFuntap(pt)
	if err != nil {
		t.Fatalf("b2: %v", err)
	}
	t.Logf("b2 ok uid=%s data=%q", uidF, sd)
	gu, err := request.GetServerURL(uidF, server)
	if err != nil {
		t.Fatalf("b3: %v", err)
	}
	token, ws, pid, err := request.GetWSKey(gu, sd)
	if err != nil {
		t.Fatalf("b4: %v", err)
	}
	t.Logf("B4 OK player=%d ws=%s token_len=%d", pid, ws, len(token))
}
