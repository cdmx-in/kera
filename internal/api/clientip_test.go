package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestRealIP(t *testing.T) {
	cdn := []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	for _, tc := range []struct {
		name    string
		remote  string
		headers map[string]string
		trusted []netip.Prefix
		want    string
	}{
		{"direct client", "203.0.113.9:5555", nil, nil, "203.0.113.9"},
		{"direct client cannot forge a forwarded address", "203.0.113.9:5555",
			map[string]string{"X-Forwarded-For": "1.2.3.4", "CF-Connecting-IP": "5.6.7.8"}, nil, "203.0.113.9"},
		{"proxy on the same host", "127.0.0.1:4000", map[string]string{"X-Forwarded-For": "203.0.113.9"}, nil, "203.0.113.9"},
		{"proxy on the docker network", "172.18.0.1:4000", map[string]string{"X-Forwarded-For": "203.0.113.9"}, nil, "203.0.113.9"},
		{"a forged entry on the left is ignored", "10.0.0.2:1", map[string]string{"X-Forwarded-For": "6.6.6.6, 203.0.113.9"}, nil, "203.0.113.9"},
		{"a chain of proxies", "10.0.0.2:1", map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.7"}, nil, "203.0.113.9"},
		{"two header lines", "10.0.0.2:1", map[string]string{"X-Forwarded-For": "6.6.6.6"}, nil, "6.6.6.6"},
		// A proxy on this host or network (Caddy, nginx, Traefik) passes a client-sent CF-Connecting-IP through;
		// only its own X-Forwarded-For says who connected, so the header must not change the key.
		{"client-sent CF-Connecting-IP behind a local proxy is ignored", "127.0.0.1:1", map[string]string{"CF-Connecting-IP": "198.51.100.1", "X-Forwarded-For": "203.0.113.5"}, nil, "203.0.113.5"},
		{"the same on the docker network", "172.18.0.2:1", map[string]string{"CF-Connecting-IP": "198.51.100.1", "X-Forwarded-For": "203.0.113.5"}, nil, "203.0.113.5"},
		{"CF-Connecting-IP alone from a local proxy is no evidence", "127.0.0.1:1", map[string]string{"CF-Connecting-IP": "198.51.100.1"}, nil, "127.0.0.1"},
		{"a tunnel on this host that sets X-Forwarded-For", "127.0.0.1:1", map[string]string{"CF-Connecting-IP": "203.0.113.9", "X-Forwarded-For": "203.0.113.9"}, nil, "203.0.113.9"},
		{"no header from a proxy", "10.0.0.2:1", nil, nil, "10.0.0.2"},
		{"garbage in the chain", "10.0.0.2:1", map[string]string{"X-Forwarded-For": "203.0.113.9, nonsense"}, nil, "10.0.0.2"},
		{"public proxy that is not trusted", "198.51.100.7:1", map[string]string{"CF-Connecting-IP": "203.0.113.9"}, nil, "198.51.100.7"},
		{"public proxy that is trusted", "198.51.100.7:1", map[string]string{"X-Forwarded-For": "203.0.113.9"}, cdn, "203.0.113.9"},
		// another CDN, or a proxy listed by mistake, may pass a client-sent CF-Connecting-IP through: never read
		{"a listed proxy's CF-Connecting-IP is not believed", "198.51.100.7:1", map[string]string{"CF-Connecting-IP": "203.0.113.9"}, cdn, "198.51.100.7"},
		{"trusted CDN: the address it appended wins over a forged one and a forged CF-Connecting-IP", "198.51.100.7:1", map[string]string{"CF-Connecting-IP": "1.1.1.1", "X-Forwarded-For": "6.6.6.6, 203.0.113.9"}, cdn, "203.0.113.9"},
		{"trusted CDN: X-Forwarded-For is read from the right", "198.51.100.7:1", map[string]string{"X-Forwarded-For": "6.6.6.6, 203.0.113.9"}, cdn, "203.0.113.9"},
		{"trusted CDN: ipv6 client is its /64", "198.51.100.7:1", map[string]string{"X-Forwarded-For": "2001:db8:1:2:aaaa::5"}, cdn, "2001:db8:1:2::"},
		{"a peer outside the list gets no header believed, even a CDN-looking one", "203.0.113.99:1", map[string]string{"CF-Connecting-IP": "198.51.100.1", "X-Forwarded-For": "198.51.100.2"}, cdn, "203.0.113.99"},
		{"trusted proxy in the chain", "198.51.100.7:1", map[string]string{"X-Forwarded-For": "203.0.113.9, 198.51.100.8"}, cdn, "203.0.113.9"},
		{"ipv6 client is its /64", "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:1", nil, nil, "2001:db8:1:2::"},
		{"ipv6 behind a proxy", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "2001:db8:1:2:1::5"}, nil, "2001:db8:1:2::"},
		{"mapped ipv4", "[::ffff:203.0.113.9]:1", nil, nil, "203.0.113.9"},
		{"unparseable peer", "pipe", nil, nil, "pipe"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = tc.remote
		for k, v := range tc.headers {
			r.Header.Set(k, v)
		}
		if tc.name == "two header lines" {
			r.Header.Set("X-Forwarded-For", "9.9.9.9")
			r.Header.Add("X-Forwarded-For", "6.6.6.6")
		}
		if got := RealIP(r, tc.trusted); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
