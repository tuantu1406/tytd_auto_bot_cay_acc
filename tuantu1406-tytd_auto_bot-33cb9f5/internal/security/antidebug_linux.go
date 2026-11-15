//go:build linux && !noantidebug

package security

import (
	"bufio"
	"os"
	"strings"
	"syscall"
)

// DetectDebug checks for various signs of a debugger being attached.
// Returns true if a debugger is detected.
func DetectDebug() bool {
	if checkPtrace() {
		return true
	}
	if checkTracerPid() {
		return true
	}
	return false
}

// checkPtrace attempts to trace the current process.
// If it fails, it's likely because a debugger is already tracing it.
func checkPtrace() bool {
	// PTRACE_TRACEME = 0
	_, _, err := syscall.Syscall(syscall.SYS_PTRACE, 0, 0, 0)
	if err != 0 {
		// If we can't trace ourselves, someone else likely is.
		return true
	}
	return false
}

// checkTracerPid reads /proc/self/status to check the TracerPid field.
// If TracerPid is not 0, a debugger is attached.
func checkTracerPid() bool {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		// If we can't open /proc/self/status, something smells fishy in a Linux env.
		return true
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "TracerPid:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				// If TracerPid != 0, we are being traced
				if parts[1] != "0" {
					return true
				}
			}
		}
	}
	return false
}
