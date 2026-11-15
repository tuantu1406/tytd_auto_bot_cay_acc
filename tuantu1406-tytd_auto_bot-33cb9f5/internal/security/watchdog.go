package security

import (
	"fmt"
	"os"
	"time"
)

// Watchdog starts a background goroutine that periodically checks for debuggers.
// If detected, it kills the process.
func Watchdog() {
	go func() {
		for {
			if DetectDebug() {
				fmt.Println("Security violation detected.")
				os.Exit(1)
			}
			time.Sleep(2 * time.Second)
		}
	}()
}
