//go:build !linux && !windows

package security

// DetectDebug checks for various signs of a debugger being attached.
// Returns true if a debugger is detected.
func DetectDebug() bool {
	// No implementation for this OS
	return false
}
