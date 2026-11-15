package main

import (
	"flag"
	"log"
	"os"

	"tytd_go"
	"tytd_go/internal/appconfig"
	"tytd_go/internal/security"
)

func main() {
	// Anti-debug check
	if security.DetectDebug() {
		// Silently exit or print a misleading message
		return
	}
	// Start background watchdog
	security.Watchdog()

	configPath := flag.String("config", "", "path to config.yaml (default: $TYTD_CONFIG, then ./config.yaml)")
	flag.Parse()

	cfg, err := appconfig.Load(*configPath)
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	token, err := cfg.ResolveToken()
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	t := tytd_go.TeleBot{Token: token, AdminID: cfg.AdminChatID, Debug: cfg.Debug}
	t.Run()
}
