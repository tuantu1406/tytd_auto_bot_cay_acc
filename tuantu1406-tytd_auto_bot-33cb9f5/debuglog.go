package tytd_go

import (
	"flag"
	"log"
	"os"
)

// debugf routes the per-event DIAGNOSTIC prints (march-ledger folds, map-view
// pull errors, scene-feed flaps, position-change trails) that were added
// during the map debugging rounds. In production they are silent: a busy
// kunlun battle pushed one line per march message and every open map page
// polled one every 5 s, so the log grew forever on the 512 MB VPS
// (user order 2026-10-06: "bỏ print debug log, chỉ enable khi run test thôi").
//
// Enabled when running inside a test binary (testing.Init registers -test.v)
// or explicitly with TYTD_DEBUG_LOG=1 for live diagnosis. Error paths and
// real action reports (hành quân, khám phá, hái quả, hồi sinh…) stay on
// log.Printf — this is only for fold/trace noise. Both lookups are cheap map
// reads, so no init-order caching is needed.
func debugf(format string, args ...any) {
	if flag.Lookup("test.v") != nil || os.Getenv("TYTD_DEBUG_LOG") != "" {
		log.Printf(format, args...)
	}
}
