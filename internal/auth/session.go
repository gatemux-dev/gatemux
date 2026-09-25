package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const SessionTokenPrefix = "sess-"

// GenerateSessionToken returns a 32-byte random token (64 hex chars), prefixed
// with "sess-" so it's distinguishable in logs from virtual keys (gw-) and
// invite tokens.
func GenerateSessionToken() (rawToken string, tokenHash []byte, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, fmt.Errorf("rand: %w", err)
	}
	rawToken = SessionTokenPrefix + hex.EncodeToString(b[:])
	tokenHash = HashKey(rawToken)
	return rawToken, tokenHash, nil
}
