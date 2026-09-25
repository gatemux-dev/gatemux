package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const KeyPrefix = "gw-"

// GenerateKey produces a new virtual key for a team. It returns the raw key
// (shown to the user once), its SHA-256 hash for storage, and a public prefix
// used in admin listings (never the raw key itself).
func GenerateKey(teamSlug string) (rawKey string, keyHash []byte, keyPrefix string, err error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, "", fmt.Errorf("rand: %w", err)
	}
	secret := hex.EncodeToString(b[:])
	rawKey = fmt.Sprintf("%s%s-%s", KeyPrefix, teamSlug, secret)
	keyHash = HashKey(rawKey)
	prefixLen := 12
	if len(rawKey) < prefixLen {
		prefixLen = len(rawKey)
	}
	keyPrefix = rawKey[:prefixLen]
	return rawKey, keyHash, keyPrefix, nil
}

func HashKey(rawKey string) []byte {
	h := sha256.Sum256([]byte(rawKey))
	return h[:]
}
