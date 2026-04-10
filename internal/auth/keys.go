package auth

import (
	"crypto/ed25519"
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"
)

// loadKeyFile reads the key file bytes from the given path.
func loadKeyFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	return data, nil
}

// parseSSHPrivateKey parses an OpenSSH-format Ed25519 private key and returns
// the ed25519.PrivateKey.
func parseSSHPrivateKey(data []byte) (ed25519.PrivateKey, error) {
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse ssh private key: %w", err)
	}

	// ssh.ParseRawPrivateKey may return *ed25519.PrivateKey or ed25519.PrivateKey
	// depending on the format. Handle both cases.
	switch key := raw.(type) {
	case ed25519.PrivateKey:
		return key, nil
	case *ed25519.PrivateKey:
		return *key, nil
	default:
		return nil, fmt.Errorf("key is not Ed25519, got %T", raw)
	}
}
