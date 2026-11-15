// Package appconfig loads the runtime configuration (Telegram bot token,
// admin chat ID, …) from a YAML file so nothing secret is baked into the
// binary anymore (the old path: AES-encrypted token injected via ldflags
// `-X main.HELLO=…` + XOR-obfuscated admin ID in getAID()).
//
// Resolution order, highest first:
//  1. explicit path passed to Load (main.go's -config flag)
//  2. TYTD_CONFIG env var
//  3. ./config.yaml
//
// Env overrides for one-off/container runs: TYTD_TELEGRAM_TOKEN,
// TYTD_TELEGRAM_TOKEN_ENCRYPTED, TYTD_ADMIN_CHAT_ID.
package appconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"tytd_go/internal/crypto"
)

const (
	// EnvConfigPath overrides the config file location.
	EnvConfigPath = "TYTD_CONFIG"
	// EnvToken overrides telegram_token with a plaintext token.
	EnvToken = "TYTD_TELEGRAM_TOKEN"
	// EnvTokenEncrypted overrides telegram_token_encrypted.
	EnvTokenEncrypted = "TYTD_TELEGRAM_TOKEN_ENCRYPTED"
	// EnvAdminChatID overrides admin_chat_id.
	EnvAdminChatID = "TYTD_ADMIN_CHAT_ID"
	// DefaultPath is used when no explicit path or TYTD_CONFIG is set.
	DefaultPath = "config.yaml"
)

// Config is the on-disk schema of config.yaml (see config.example.yaml).
type Config struct {
	// TelegramToken is the plaintext bot token from @BotFather.
	TelegramToken string `yaml:"telegram_token"`
	// TelegramTokenEncrypted is the hex AES-256-GCM blob produced by the
	// cmd/gen tool — an alternative to plaintext for people who prefer not
	// to keep the raw token on disk. Exactly one of the two must be set.
	TelegramTokenEncrypted string `yaml:"telegram_token_encrypted"`
	// AdminChatID is the Telegram chat/user ID with admin rights.
	AdminChatID int64 `yaml:"admin_chat_id"`
	// Debug turns on verbose logging (same effect as TeleBot.Debug).
	Debug bool `yaml:"debug"`
}

// Load reads and validates the config. path may be "" to use
// TYTD_CONFIG / ./config.yaml. Unknown keys are rejected so a typo
// (telegram_tokn:) fails loudly instead of silently running without it.
func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv(EnvConfigPath)
	}
	if path == "" {
		path = DefaultPath
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config file %q not found — copy config.example.yaml to config.yaml and fill it in (or set %s)", path, EnvConfigPath)
		}
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	cfg := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	applyEnvOverrides(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv(EnvToken); v != "" {
		cfg.TelegramToken = strings.TrimSpace(v)
		cfg.TelegramTokenEncrypted = ""
	}
	if v := os.Getenv(EnvTokenEncrypted); v != "" {
		cfg.TelegramTokenEncrypted = strings.TrimSpace(v)
		cfg.TelegramToken = ""
	}
	if v := os.Getenv(EnvAdminChatID); v != "" {
		if id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			cfg.AdminChatID = id
		}
	}
}

// Validate enforces the invariants main.go relies on.
func (c *Config) Validate() error {
	token := strings.TrimSpace(c.TelegramToken)
	enc := strings.TrimSpace(c.TelegramTokenEncrypted)
	switch {
	case token != "" && enc != "":
		return errors.New("set only one of telegram_token / telegram_token_encrypted")
	case token == "" && enc == "":
		return errors.New("telegram_token (or telegram_token_encrypted) is required")
	}
	if c.AdminChatID <= 0 {
		return errors.New("admin_chat_id is required and must be a positive Telegram chat ID")
	}
	return nil
}

// ResolveToken returns the plaintext bot token, decrypting
// TelegramTokenEncrypted with the AES-256-GCM scheme of internal/crypto
// (the blob format produced by the cmd/gen tool).
func (c *Config) ResolveToken() (string, error) {
	if token := strings.TrimSpace(c.TelegramToken); token != "" {
		return token, nil
	}
	decrypted, err := crypto.DecryptFromHex(strings.TrimSpace(c.TelegramTokenEncrypted))
	if err != nil {
		return "", fmt.Errorf("decrypt telegram_token_encrypted: %w", err)
	}
	return decrypted, nil
}
