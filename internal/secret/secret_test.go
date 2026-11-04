package secret

import (
	"bytes"
	"testing"
)

func TestRoundTripAndKeySeparation(t *testing.T) {
	k, err := Derive("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := k.EncryptString("ya29.token")
	if err != nil {
		t.Fatal(err)
	}
	got, err := k.DecryptString(sealed)
	if err != nil || got != "ya29.token" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := k.DecryptString(sealed); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
	if bytes.Equal(k.SignJWT([]byte("x")), k.SignCall([]byte("x"))) {
		t.Fatal("jwt and call keys must differ")
	}
	other, _ := Derive("fedcba9876543210fedcba9876543210")
	if bytes.Equal(k.SignJWT([]byte("x")), other.SignJWT([]byte("x"))) {
		t.Fatal("different roots must give different keys")
	}
	if _, err := Derive("short"); err == nil {
		t.Fatal("short root accepted")
	}
	if s, err := k.DecryptString(nil); s != "" || err != nil {
		t.Fatal("nil should decrypt to empty")
	}
}
