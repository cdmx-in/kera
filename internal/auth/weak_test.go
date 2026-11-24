package auth

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// The vectors are shared with the web form (web/src/lib/password.test.ts runs the same file), so
// both sides accept and refuse the same passwords with the same words.
func TestPasswordVectors(t *testing.T) {
	b, err := os.ReadFile("../../web/src/lib/passwordVectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct{ Name, Password, Email, Message string }
	if err := json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) < 30 {
		t.Fatalf("only %d vectors", len(vectors))
	}
	for _, v := range vectors {
		got := ""
		if err := CheckPassword(v.Password, v.Email); err != nil {
			got = err.Error()
		}
		if got != v.Message {
			t.Errorf("%s: CheckPassword(%.30q, %q) = %q, want %q", v.Name, v.Password, v.Email, got, v.Message)
		}
	}
}

// The list the web form reads is this one, byte for byte.
func TestCommonPasswordsAreCopiedToTheWebApp(t *testing.T) {
	b, err := os.ReadFile("../../web/src/lib/commonPasswords.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, commonJSON) {
		t.Fatal("web/src/lib/commonPasswords.json differs from internal/auth/common_passwords.json: copy one over the other")
	}
	c := common()
	if len(c.exact) < 50 || len(c.words) < 50 {
		t.Fatalf("the list is nearly empty: %d exact, %d words", len(c.exact), len(c.words))
	}
	for w := range c.exact {
		if w != toLower(w) {
			t.Errorf("exact entry %q is not lower case", w)
		}
	}
}

func toLower(s string) string { return string(bytes.ToLower([]byte(s))) }
