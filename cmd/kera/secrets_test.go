package main

import (
	"strings"
	"testing"
)

// kera secrets is wired in and its usage is listed; main turns every error returned here into exit status 1.
func TestSecretsCommandIsWired(t *testing.T) {
	for _, want := range []string{"kera secrets check", "kera secrets rotate --old-key-file", "KERA_SECRET_KEY_FILE"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage does not mention %q", want)
		}
	}
	for _, args := range [][]string{{"secrets"}, {"secrets", "bogus"}, {"secrets", "rotate"}} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "kera secrets check") {
			t.Errorf("kera %v: %v", args, err)
		}
	}
}
