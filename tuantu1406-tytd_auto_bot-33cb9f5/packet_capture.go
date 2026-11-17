package tytd_go

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tytd_go/internal/pack"
)

const (
	packetCaptureEnabledEnv = "TYTD_PACKET_CAPTURE"
	packetCaptureFileEnv    = "TYTD_PACKET_CAPTURE_FILE"
	packetCaptureMaxBytes   = 32 << 20
	packetCaptureAuthOpcode = "4e21" // AUTH_CMD; payload carries the session token.
)

type packetCaptureRecord struct {
	Time        string `json:"time"`
	AccountTag  string `json:"account_tag"`
	Direction   string `json:"direction"`
	Opcode      string `json:"opcode"`
	PayloadSize int    `json:"payload_size"`
	PayloadHex  string `json:"payload_hex"`
}

type packetCaptureSink struct {
	mu      sync.Mutex
	file    *os.File
	written int64
	stopped bool
}

var (
	packetCaptureOnce sync.Once
	packetCapture     *packetCaptureSink
)

// capturePacketFrame records a decoded game-protocol payload at the WebSocket
// boundary. The WebSocket library has already decrypted TLS at this point.
// Authentication frames are always excluded because their protobuf payload
// contains the short-lived game session token.
func capturePacketFrame(username, direction string, frame []byte) {
	if os.Getenv(packetCaptureEnabledEnv) != "1" {
		return
	}
	packetCaptureOnce.Do(initPacketCapture)
	if packetCapture == nil {
		return
	}

	isMessage, opcode, payload := pack.Unpack(frame)
	if !isMessage || opcode == packetCaptureAuthOpcode {
		return
	}

	accountHash := sha256.Sum256([]byte(username))
	record := packetCaptureRecord{
		Time:        time.Now().UTC().Format(time.RFC3339Nano),
		AccountTag:  hex.EncodeToString(accountHash[:6]),
		Direction:   direction,
		Opcode:      opcode,
		PayloadSize: len(payload),
		PayloadHex:  hex.EncodeToString(payload),
	}
	line, err := json.Marshal(record)
	if err != nil {
		log.Printf("[packet-capture] encode failed: %v", err)
		return
	}
	line = append(line, '\n')

	packetCapture.mu.Lock()
	defer packetCapture.mu.Unlock()
	if packetCapture.stopped {
		return
	}
	if packetCapture.written+int64(len(line)) > packetCaptureMaxBytes {
		packetCapture.stopLocked()
		log.Printf("[packet-capture] reached %d-byte limit; capture stopped", packetCaptureMaxBytes)
		return
	}
	n, err := packetCapture.file.Write(line)
	packetCapture.written += int64(n)
	if err != nil {
		packetCapture.stopLocked()
		log.Printf("[packet-capture] write failed; capture stopped: %v", err)
	}
}

func initPacketCapture() {
	path := strings.TrimSpace(os.Getenv(packetCaptureFileEnv))
	if path == "" {
		log.Printf("[packet-capture] %s=1 requires an absolute %s path; capture disabled", packetCaptureEnabledEnv, packetCaptureFileEnv)
		return
	}
	if !filepath.IsAbs(path) {
		log.Printf("[packet-capture] %s must be an absolute path; capture disabled", packetCaptureFileEnv)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Printf("[packet-capture] cannot create capture directory: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		log.Printf("[packet-capture] cannot create new capture file (existing files are never overwritten): %v", err)
		return
	}
	packetCapture = &packetCaptureSink{file: f}
	log.Printf("[packet-capture] enabled; writing protocol payloads to %s (AUTH frames excluded, limit %d bytes)", path, packetCaptureMaxBytes)
}

func (s *packetCaptureSink) stopLocked() {
	if s.stopped {
		return
	}
	s.stopped = true
	_ = s.file.Close()
}
