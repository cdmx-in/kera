package config

import (
	"net/netip"
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
