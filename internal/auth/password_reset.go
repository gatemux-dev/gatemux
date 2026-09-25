package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const PasswordResetPrefix = "rst-"

// GeneratePasswordResetToken returns a fresh 32-byte random token with
// the rst- prefix and its SHA-256 hash for storage. The raw token is
// only ever revealed once — to the admin issuing the reset. The user
// receives it through the URL the admin shares out-of-band.
func GeneratePasswordResetToken() (rawToken string, tokenHash []byte, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, fmt.Errorf("rand: %w", err)
	}
	rawToken = PasswordResetPrefix + hex.EncodeToString(b[:])
	tokenHash = HashKey(rawToken)
	return rawToken, tokenHash, nil
}
