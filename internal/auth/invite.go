package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// GenerateInviteToken produces a 32-byte random invite token, its SHA-256
// hash for storage, and an 8-char prefix for admin display.
func GenerateInviteToken() (rawToken string, tokenHash []byte, tokenPrefix string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, "", fmt.Errorf("rand: %w", err)
	}
	rawToken = hex.EncodeToString(b[:])
	tokenHash = HashKey(rawToken)
	tokenPrefix = rawToken[:8]
	return rawToken, tokenHash, tokenPrefix, nil
}
