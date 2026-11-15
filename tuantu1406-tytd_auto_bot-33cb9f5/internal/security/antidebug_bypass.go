//go:build noantidebug

package security

// DetectDebug returns false when noantidebug tag is present.
func DetectDebug() bool {
	// fmt.Println("running in debug mode")
	return false
}
