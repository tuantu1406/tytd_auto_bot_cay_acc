package main

// debugweb: leader-web dashboard with Telegram auth bypassed via
// TYTD_DEBUG_CHAT. No bot token, no anti-debug hooks — a map-debugging
// tool, NOT a deployment binary. See web_debug.go for env knobs.

import "tytd_go"

func main() { tytd_go.DebugWeb() }
