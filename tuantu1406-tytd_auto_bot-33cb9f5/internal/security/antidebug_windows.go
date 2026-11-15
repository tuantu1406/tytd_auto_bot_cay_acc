//go:build windows && !noantidebug

package security

import (
	"syscall"
)

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procIsDebuggerPresent = kernel32.NewProc("IsDebuggerPresent")
)

// DetectDebug checks for various signs of a debugger being attached.
// Returns true if a debugger is detected.
func DetectDebug() bool {
	// IsDebuggerPresent returns non-zero if a debugger is attached
	ret, _, _ := procIsDebuggerPresent.Call()
	return ret != 0
}
