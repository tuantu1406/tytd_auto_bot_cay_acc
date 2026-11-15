package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	KEY = "1234567890123456789012345678901212345678901234567890123456789012"
)

// EncryptToHex encrypts plaintext with AES-256-GCM and returns
// hex(ciphertext || nonce).  Key must be 32 bytes (64 hex chars).
func EncryptToHex(plaintext string) (string, error) {
	keyBytes, err := hex.DecodeString(KEY)
	if err != nil {
		return "", fmt.Errorf("invalid hex key: %w", err)
	}
	if len(keyBytes) != 32 {
		return "", errors.New("key must be 32 bytes (64 hex chars)")
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	cipherBytes := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	full := append(cipherBytes, nonce...) // ciphertext || nonce
	return hex.EncodeToString(full), nil
}

// DecryptFromHex reverses EncryptToHex.
func DecryptFromHex(hexCiphertext string) (string, error) {
	keyBytes, err := hex.DecodeString(KEY)
	if err != nil {
		return "", fmt.Errorf("invalid hex key: %w", err)
	}
	if len(keyBytes) != 32 {
		return "", errors.New("key must be 32 bytes (64 hex chars)")
	}

	full, err := hex.DecodeString(hexCiphertext)
	if err != nil {
		return "", fmt.Errorf("invalid hex ciphertext: %w", err)
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(full) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}

	// split: last gcm.NonceSize() bytes are nonce
	nonceStart := len(full) - gcm.NonceSize()
	nonce := full[nonceStart:]
	cipherBytes := full[:nonceStart]

	plainBytes, err := gcm.Open(nil, nonce, cipherBytes, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed: %w", err)
	}
	return string(plainBytes), nil
}
