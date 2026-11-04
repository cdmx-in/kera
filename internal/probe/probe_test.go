package probe

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestBlockedAndCapped(t *testing.T) {
	for ip, want := range map[string]bool{"169.254.169.254": true, "fd00:ec2::254": true, "fe80::1": true,
		"127.0.0.1": false, "10.0.0.5": false, "8.8.8.8": false} {
		if Blocked(netip.MustParseAddr(ip)) != want {
			t.Errorf("Blocked(%s) != %v", ip, want)
		}
	}
	if _, err := Client().Get("http://169.254.169.254/latest/meta-data/"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("metadata reachable: %v", err)
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(make([]byte, MaxBody+10))
	}))
	defer big.Close()
	resp, err := Client().Get(big.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64<<10)
	var n int
	for err == nil {
		var k int
		k, err = resp.Body.Read(buf)
		n += k
	}
	if n != MaxBody || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("read %d, err %v", n, err)
	}
	if got := Redact("bad key sk-123 here", "sk-123", ""); got != "bad key ••• here" {
		t.Fatal(got)
	}
}
