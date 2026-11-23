package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/secret"
)

func keys(t *testing.T) *secret.Keys {
	k, err := secret.Derive(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestToken(t *testing.T) {
	k := keys(t)
	tok, err := IssueToken(k, "u1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c, err := VerifyToken(k, tok)
	if err != nil || c.Sub != "u1" {
		t.Fatalf("verify: %+v %v", c, err)
	}
	if _, err := VerifyToken(k, tok+"x"); err == nil {
		t.Fatal("bad signature accepted")
	}
	expired, _ := IssueToken(k, "u1", -time.Second)
	if _, err := VerifyToken(k, expired); err == nil {
		t.Fatal("expired token accepted")
	}
	parts := strings.Split(tok, ".")
	none := b64.EncodeToString([]byte(`{"alg":"none"}`)) + "." + parts[1] + "."
	if _, err := VerifyToken(k, none); err == nil {
		t.Fatal("alg none accepted")
	}
	other, _ := secret.Derive(strings.Repeat("z", 32))
	if _, err := VerifyToken(other, tok); err == nil {
		t.Fatal("token from another secret accepted")
	}
}

func TestAPIKey(t *testing.T) {
	key, hash, prefix := NewAPIKey()
	if !IsAPIKey(key) || len(key) != 43 || HashAPIKey(key) != hash || !strings.HasPrefix(key, prefix) {
		t.Fatalf("key=%s prefix=%s", key, prefix)
	}
	k2, _, _ := NewAPIKey()
	if k2 == key {
		t.Fatal("keys repeat")
	}
}

func TestNewKey(t *testing.T) {
	// "sk-" keys are unchanged: NewAPIKey is NewKey("sk-") with the 10-character display prefix.
	sk, skHash, skPrefix := NewAPIKey()
	k2, h2, err := NewKey("sk-")
	if err != nil {
		t.Fatal(err)
	}
	if len(sk) != len(k2) || len(sk) != 43 || skPrefix != sk[:10] || skHash != HashAPIKey(sk) || h2 != HashAPIKey(k2) {
		t.Fatalf("sk keys differ: %q %q %q", sk, skPrefix, k2)
	}

	key, hash, err := NewKey("kp-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "kp-") || len(key) != 3+40 || HashAPIKey(key) != hash {
		t.Fatalf("kp key %q hash %q", key, hash)
	}
	for _, c := range key[3:] {
		if !strings.ContainsRune(keyAlphabet, c) {
			t.Fatalf("character %q outside the alphabet", c)
		}
	}
	other, _, _ := NewKey("kp-")
	if other == key {
		t.Fatal("keys repeat")
	}

	// only sk- credentials are API keys; a kp- token never passes for one
	for cred, want := range map[string]bool{sk: true, k2: true, key: false, "kp-": false, "SK-" + sk[3:]: false, "": false, "eyJhbGciOi.e30.sig": false} {
		if IsAPIKey(cred) != want {
			t.Errorf("IsAPIKey(%q) = %v, want %v", cred, !want, want)
		}
	}
}

func TestPermissionsMerge(t *testing.T) {
	base := ParsePermissions([]byte(`{"features":{"api_keys":false,"memory":true},"workspace":{"models":false}}`))
	group := ParsePermissions([]byte(`{"features":{"api_keys":true,"memory":false},"workspace":{"tools":true}}`))
	p := Merge(base, group)
	for path, want := range map[string]bool{
		"features.api_keys": true,  // group grants
		"features.memory":   true,  // group cannot revoke
		"workspace.models":  false, // untouched
		"workspace.tools":   true,  // new key from group
		"missing.key":       false,
	} {
		if got := p.Allowed(path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if base.Allowed("features.api_keys") {
		t.Fatal("Merge mutated base")
	}
	admin := &User{Role: "admin"}
	if !admin.Can("anything.at.all") {
		t.Fatal("admin denied")
	}
}
