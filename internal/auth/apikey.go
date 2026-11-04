package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
)

const keyAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewAPIKey returns a fresh "sk-" key (shown once) and the values to store.
func NewAPIKey() (key, hash, prefix string) {
	var sb strings.Builder
	sb.WriteString("sk-")
	max := big.NewInt(int64(len(keyAlphabet)))
	for i := 0; i < 40; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		sb.WriteByte(keyAlphabet[n.Int64()])
	}
	key = sb.String()
	return key, HashAPIKey(key), KeyPrefix(key)
}

// HashAPIKey is what the api_key table stores. Imported keys (any format
// starting with sk-) hash the same way, so existing clients keep working.
func HashAPIKey(key string) string {
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:])
}

// KeyPrefix is the non-secret part shown in the UI to tell keys apart.
func KeyPrefix(key string) string {
	if len(key) <= 10 {
		return key
	}
	return key[:10]
}

// IsAPIKey reports whether a bearer credential is an API key rather than a session token.
func IsAPIKey(cred string) bool { return strings.HasPrefix(cred, "sk-") }
