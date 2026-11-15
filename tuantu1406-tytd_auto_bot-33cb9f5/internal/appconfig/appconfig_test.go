package appconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tytd_go/internal/crypto"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadPlaintextToken(t *testing.T) {
	p := writeCfg(t, "telegram_token: \"123:abc\"\nadmin_chat_id: 42\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tok, err := cfg.ResolveToken()
	if err != nil || tok != "123:abc" {
		t.Fatalf("ResolveToken = %q, %v", tok, err)
	}
	if cfg.AdminChatID != 42 {
		t.Fatalf("AdminChatID = %d", cfg.AdminChatID)
	}
}

func TestLoadEncryptedToken(t *testing.T) {
	enc, err := crypto.EncryptToHex("456:def")
	if err != nil {
		t.Fatal(err)
	}
	p := writeCfg(t, "telegram_token_encrypted: \""+enc+"\"\nadmin_chat_id: 7\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tok, err := cfg.ResolveToken()
	if err != nil || tok != "456:def" {
		t.Fatalf("ResolveToken = %q, %v", tok, err)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	p := writeCfg(t, "telegram_tokn: \"x\"\nadmin_chat_id: 1\n")
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "field telegram_tokn") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoadValidation(t *testing.T) {
	cases := map[string]string{
		"no token":      "admin_chat_id: 1\n",
		"both tokens":   "telegram_token: \"a\"\ntelegram_token_encrypted: \"b\"\nadmin_chat_id: 1\n",
		"missing admin": "telegram_token: \"a\"\n",
		"zero admin":    "telegram_token: \"a\"\nadmin_chat_id: 0\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeCfg(t, body)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestMissingFileMentionsExample(t *testing.T) {
	t.Setenv(EnvConfigPath, filepath.Join(t.TempDir(), "nope.yaml"))
	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), "config.example.yaml") {
		t.Fatalf("expected example-file hint, got %v", err)
	}
}

func TestEnvOverrides(t *testing.T) {
	p := writeCfg(t, "telegram_token: \"file:token\"\nadmin_chat_id: 1\n")
	t.Setenv(EnvToken, "env:token")
	t.Setenv(EnvAdminChatID, "99")
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tok, _ := cfg.ResolveToken(); tok != "env:token" {
		t.Fatalf("token = %q, want env override", tok)
	}
	if cfg.AdminChatID != 99 {
		t.Fatalf("admin = %d, want env override", cfg.AdminChatID)
	}
}
