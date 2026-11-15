package model

import (
	"log"

	"gorm.io/gorm"
)

const (
	ConfigKeyAdminChatID         = "admin_chat_id"
	ConfigKeyMaxConcurrentPlayer = "max_concurrent_player"
	ConfigKeyDebugMode           = "debug_mode"
	ConfigKeyAuthKey             = "auth_key"
	ConfigKeyAutoLogin           = "auto_login"
	ConfigKeyTempAuto            = "temp_auto"
	ConfigKeySeedAutoGather      = "seed_auto_gather" // one-shot users.auto_gather backfill marker (db.NewDB)
	ConfigKeyAutoTuLinh          = "auto_tu_linh"     // global Tụ Linh gate, like auto_login
	ConfigKeyThanLongAuto        = "thanlong_auto"    // global auto-play gate for "đại hội luyện khí" (ComposeBall, /start_thanlong) — default off
	ConfigKeyMustJoinGroup       = "must_join_group"
	ConfigKeyGroqAPIKey          = "groq_api_key"
	ConfigKeyCopilotAPIKey       = "copilot_api_key"
	ConfigKeyChatbotEnabled      = "chatbot_enabled"
	ConfigKeyGroupChatID         = "group_chat_id"
	// Web UI: master switch, the public domain registered with @BotFather
	// (informational + shown on the login page), and the bot's Telegram
	// username (without @) that the Login Widget authenticates against.
	ConfigKeyWebEnabled     = "web_enabled"
	ConfigKeyWebDomain      = "web_domain"
	ConfigKeyWebBotUsername = "web_bot_username"
	// MCP-over-HTTP server: master switch and the single shared auth key
	// clients must present (X-MCP-Key or Authorization: Bearer). No key set
	// means the endpoint answers 503 even when the switch is on.
	ConfigKeyMCPEnabled = "mcp_enabled"
	ConfigKeyMCPAuthKey = "mcp_auth_key"
	// Big Chess external play engine (remoteBigChess/engine_api): master
	// switch, HTTP base URL, and optional bearer token. When enabled the MCP
	// exposes bigchess_analyze and feeds /v1/transitions after successful
	// actions so the engine's replay model learns from real games.
	ConfigKeyBCEngineEnabled = "bcengine_enabled"
	ConfigKeyBCEngineURL     = "bcengine_url"
	ConfigKeyBCEngineToken   = "bcengine_token"
)

type SystemConfig struct {
	Key   string `gorm:"primaryKey" json:"key"`
	Value string `json:"value"`
}

// GetConfig retrieves a config value by key. Returns default if not found (and currently doesn't error out explicitly, just empty string or similar logic if we wanted).
// Here we return the config object or error
func GetConfig(db *gorm.DB, key string) (*SystemConfig, error) {
	var config SystemConfig
	err := db.Where("key = ?", key).First(&config).Error
	return &config, err
}

// SetConfig sets a config value. Creates or updates.
func SetConfig(db *gorm.DB, key, value string) error {
	config := SystemConfig{
		Key:   key,
		Value: value,
	}
	// Save will match primary key "Key" and update or insert
	if err := db.Save(&config).Error; err != nil {
		log.Printf("Failed to set config %s: %v", key, err)
		return err
	}
	return nil
}

// Helper to get admin chat ID specifically, parsing it might be done by caller or here.
// Keeping it simple: caller parses string to int64.
