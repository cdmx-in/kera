package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAdminAndProxies(t *testing.T) {
	t.Setenv("KERA_DATABASE_URL", "postgres://x")
	t.Setenv("KERA_ADMIN_EMAIL", " you@example.com ")
	t.Setenv("KERA_ADMIN_PASSWORD", " keep the spaces ")
	t.Setenv("KERA_TRUSTED_PROXIES", "203.0.113.5, 198.51.100.0/24\n2001:db8::/32")
	e, err := Load(false)
	if err != nil {
		t.Fatal(err)
	}
	if e.AdminEmail != "you@example.com" || e.AdminPassword != " keep the spaces " {
		t.Fatalf("admin %q %q", e.AdminEmail, e.AdminPassword)
	}
	want := []netip.Prefix{netip.MustParsePrefix("203.0.113.5/32"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("2001:db8::/32")}
	if len(e.TrustedProxies) != 3 || e.TrustedProxies[0] != want[0] || e.TrustedProxies[1] != want[1] || e.TrustedProxies[2] != want[2] {
		t.Fatalf("proxies %v", e.TrustedProxies)
	}
	t.Setenv("KERA_TRUSTED_PROXIES", "10.0.0.1, nonsense")
	if _, err := Load(false); err == nil || !strings.Contains(err.Error(), `"nonsense"`) {
		t.Fatalf("a bad proxy list gave %v", err)
	}
}

func TestLoadExtractorURL(t *testing.T) {
	t.Setenv("KERA_DATABASE_URL", "postgres://x")
	for in, want := range map[string]string{"": "", " http://extract:8000/ ": "http://extract:8000", "https://docs.example.com/x": "https://docs.example.com/x"} {
		t.Setenv("KERA_EXTRACTOR_URL", in)
		if e, err := Load(false); err != nil || e.ExtractorURL != want {
			t.Fatalf("KERA_EXTRACTOR_URL=%q gave %q, %v", in, e.ExtractorURL, err)
		}
	}
	for _, bad := range []string{"extract:8000", "ftp://extract", "http://", "not a url"} {
		t.Setenv("KERA_EXTRACTOR_URL", bad)
		if _, err := Load(false); err == nil || !strings.Contains(err.Error(), "KERA_EXTRACTOR_URL") {
			t.Fatalf("KERA_EXTRACTOR_URL=%q gave %v", bad, err)
		}
	}
}

func TestLoadRunnerTokenFile(t *testing.T) {
	t.Setenv("KERA_DATABASE_URL", "postgres://x")
	for in, want := range map[string]string{"": "", " /run/kera-runner/token ": "/run/kera-runner/token"} {
		t.Setenv("KERA_RUNNER_TOKEN_FILE", in)
		if e, err := Load(false); err != nil || e.RunnerTokenFile != want {
			t.Fatalf("KERA_RUNNER_TOKEN_FILE=%q gave %q, %v", in, e.RunnerTokenFile, err)
		}
	}
}

// writeFile makes a throwaway file and returns its path.
func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSecretKeyFile(t *testing.T) {
	t.Setenv("KERA_DATABASE_URL", "postgres://x")
	key := strings.Repeat("k", 40)
	t.Setenv("KERA_SECRET_KEY", "")

	// a trailing newline (or CRLF) is not part of the key, so both files derive the same keys
	for _, content := range []string{key, key + "\n", key + "\r\n", key + "\n\n"} {
		t.Setenv("KERA_SECRET_KEY_FILE", writeFile(t, content))
		if e, err := Load(true); err != nil || e.SecretKey != key {
			t.Fatalf("file %q gave a key of %d bytes, %v", content, len(e.SecretKey), err)
		}
	}
	// nothing else is trimmed: the key is hashed byte for byte
	t.Setenv("KERA_SECRET_KEY_FILE", writeFile(t, "  "+key+" \n"))
	if e, err := Load(true); err != nil || e.SecretKey != "  "+key+" " {
		t.Fatalf("spaces were trimmed: %q, %v", e.SecretKey, err)
	}

	// the file wins over the variable
	t.Setenv("KERA_SECRET_KEY", strings.Repeat("e", 40))
	t.Setenv("KERA_SECRET_KEY_FILE", writeFile(t, key+"\n"))
	if e, err := Load(true); err != nil || e.SecretKey != key {
		t.Fatalf("file did not win: %v", err)
	}

	// without the file variable the plain one still works
	t.Setenv("KERA_SECRET_KEY_FILE", "")
	if e, err := Load(true); err != nil || e.SecretKey != strings.Repeat("e", 40) {
		t.Fatalf("plain variable: %v", err)
	}
}

func TestLoadSecretKeyFileErrors(t *testing.T) {
	t.Setenv("KERA_DATABASE_URL", "postgres://x")
	t.Setenv("KERA_SECRET_KEY", strings.Repeat("e", 40)) // a good variable does not hide a bad file
	short := "short-but-unmistakable"
	for name, tc := range map[string]struct{ path, want string }{
		"missing": {filepath.Join(t.TempDir(), "nope"), "no such file"},
		"empty":   {writeFile(t, "\n"), "is empty"},
		"short":   {writeFile(t, short+"\n"), "at least 32 bytes (got 22)"},
		"dir":     {t.TempDir(), "KERA_SECRET_KEY_FILE"},
	} {
		t.Setenv("KERA_SECRET_KEY_FILE", tc.path)
		_, err := Load(true)
		if err == nil || !strings.Contains(err.Error(), "KERA_SECRET_KEY_FILE") || !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v should name KERA_SECRET_KEY_FILE, the path and %q", name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), short) {
			t.Errorf("%s: the error shows the file content: %v", name, err)
		}
		if _, err := Load(false); err != nil { // commands without a key do not care
			t.Errorf("%s: Load(false) failed: %v", name, err)
		}
	}
	t.Setenv("KERA_SECRET_KEY_FILE", "")
	t.Setenv("KERA_SECRET_KEY", short)
	if _, err := Load(true); err == nil || !strings.Contains(err.Error(), "KERA_SECRET_KEY must be at least 32 bytes (got 22)") {
		t.Errorf("short variable: %v", err)
	}
}

func TestLoadDatabaseURLFile(t *testing.T) {
	t.Setenv("KERA_DATABASE_URL", "postgres://from-env")
	t.Setenv("KERA_DATABASE_URL_FILE", writeFile(t, "postgres://from-file\n"))
	if e, err := Load(false); err != nil || e.DatabaseURL != "postgres://from-file" {
		t.Fatalf("file did not win: %q, %v", e.DatabaseURL, err)
	}
	t.Setenv("KERA_DATABASE_URL_FILE", writeFile(t, ""))
	if _, err := Load(false); err == nil || !strings.Contains(err.Error(), "KERA_DATABASE_URL_FILE") || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("empty file gave %v", err)
	}
	t.Setenv("KERA_DATABASE_URL_FILE", filepath.Join(t.TempDir(), "nope"))
	if _, err := Load(false); err == nil || !strings.Contains(err.Error(), "KERA_DATABASE_URL_FILE") {
		t.Fatalf("missing file gave %v", err)
	}
	t.Setenv("KERA_DATABASE_URL_FILE", "")
	t.Setenv("KERA_DATABASE_URL", "")
	if _, err := Load(false); err == nil || !strings.Contains(err.Error(), "KERA_DATABASE_URL is required") {
		t.Fatalf("no URL gave %v", err)
	}
}

func TestReadKeyFile(t *testing.T) {
	key := strings.Repeat("a", 32)
	if k, err := ReadKeyFile("--old-key-file", writeFile(t, key+"\r\n")); err != nil || k != key {
		t.Fatalf("got %q, %v", k, err)
	}
	p := writeFile(t, "tiny\n")
	if _, err := ReadKeyFile("--old-key-file", p); err == nil || !strings.Contains(err.Error(), "--old-key-file "+p) || strings.Contains(err.Error(), "tiny") {
		t.Fatalf("short key: %v", err)
	}
}
