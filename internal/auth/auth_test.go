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
