//go:build ignore

// Standalone token-encryption helper, built explicitly by
// `make build-gen` (go build gen.go). The ignore constraint keeps it out of
// the cmd package so main.go remains the only main() there.

package main

import (
	"log"
	"os"
	"tytd_go/internal/crypto"
)

func main() {
	encrypted, err := crypto.EncryptToHex(os.Args[1])
	if err != nil {
		log.Println(err)
	}
	log.Println(encrypted)
}
