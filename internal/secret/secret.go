// Package secret derives purpose-bound keys from KERA_SECRET_KEY and provides
// authenticated encryption for values stored at rest (OAuth tokens, API keys
// of upstream services) plus HMAC signing for short-lived tokens.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// Keys holds the derived subkeys. Never log or serialise it.
type Keys struct {
	jwt    []byte
	tokens cipher.AEAD
	calls  []byte
}

// Derive builds subkeys with HKDF-SHA256 so one root secret can be rotated as a unit.
func Derive(root string) (*Keys, error) {
	if len(root) < 32 {
		return nil, errors.New("secret key must be at least 32 bytes")
	}
	sub := func(info string) ([]byte, error) {
		return hkdf.Key(sha256.New, []byte(root), nil, "kera/"+info, 32)
	}
	jwt, err := sub("jwt")
	if err != nil {
		return nil, err
	}
	tk, err := sub("tokens")
	if err != nil {
		return nil, err
	}
	calls, err := sub("calls")
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(tk)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keys{jwt: jwt, tokens: aead, calls: calls}, nil
}

// Encrypt seals plaintext with AES-256-GCM; output is nonce||ciphertext.
func (k *Keys) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, k.tokens.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return k.tokens.Seal(nonce, nonce, plain, nil), nil
}

// Decrypt opens a value produced by Encrypt.
func (k *Keys) Decrypt(sealed []byte) ([]byte, error) {
	n := k.tokens.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("ciphertext too short")
	}
	out, err := k.tokens.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return out, nil
}

// EncryptString is Encrypt for strings; empty input yields nil (stored as NULL).
func (k *Keys) EncryptString(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return k.Encrypt([]byte(s))
}

// DecryptString is Decrypt for strings; nil input yields "".
func (k *Keys) DecryptString(b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}
	p, err := k.Decrypt(b)
	return string(p), err
}

// SignJWT returns the HMAC-SHA256 of msg with the session key.
func (k *Keys) SignJWT(msg []byte) []byte { return mac(k.jwt, msg) }

// SignCall returns the HMAC-SHA256 of msg with the tool-callback key.
func (k *Keys) SignCall(msg []byte) []byte { return mac(k.calls, msg) }

func mac(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}
