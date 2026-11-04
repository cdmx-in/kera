package auth

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/secret"
)

// Claims are the session token contents.
type Claims struct {
	Sub string `json:"sub"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

var b64 = base64.RawURLEncoding

const jwtHeader = `{"alg":"HS256","typ":"JWT"}`

// IssueToken returns an HS256 JWT for userID valid for ttl.
func IssueToken(keys *secret.Keys, userID string, ttl time.Duration) (string, error) {
	now := time.Now()
	payload, err := json.Marshal(Claims{Sub: userID, Iat: now.Unix(), Exp: now.Add(ttl).Unix()})
	if err != nil {
		return "", err
	}
	signing := b64.EncodeToString([]byte(jwtHeader)) + "." + b64.EncodeToString(payload)
	return signing + "." + b64.EncodeToString(keys.SignJWT([]byte(signing))), nil
}

// ErrInvalidToken covers every malformed, forged or expired token.
var ErrInvalidToken = errors.New("invalid or expired token")

// VerifyToken checks signature, algorithm and expiry.
func VerifyToken(keys *secret.Keys, tok string) (Claims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalidToken
	}
	hdr, err := b64.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var h struct{ Alg string }
	if json.Unmarshal(hdr, &h) != nil || h.Alg != "HS256" {
		return Claims{}, ErrInvalidToken
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, keys.SignJWT([]byte(parts[0]+"."+parts[1]))) {
		return Claims{}, ErrInvalidToken
	}
	body, err := b64.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var c Claims
	if json.Unmarshal(body, &c) != nil || c.Sub == "" || time.Now().Unix() >= c.Exp {
		return Claims{}, ErrInvalidToken
	}
	return c, nil
}
