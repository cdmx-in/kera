package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RealIP is the address of whoever is on the other end, for rate limits (the
// request log keeps its own looser clientIP).
//
// The address the connection came from is believed. Only when that is a
// loopback, private or link-local address (a reverse proxy on the same host or
// network, as in the compose stack) or one of trusted does it read
// CF-Connecting-IP, then X-Forwarded-For from the right, skipping proxies it
// trusts: whoever sits on the open internet cannot send its own forwarded address. A client on
// the same private network as Kera could, which this accepts.
// ponytail: a proxy that reaches Kera over a public address (an orange-cloud CDN
// straight to the origin) must be listed in KERA_TRUSTED_PROXIES, else every
// visitor looks like the CDN.
//
// IPv6 addresses are cut to their /64, the size of one customer's block.
func RealIP(r *http.Request, trusted []netip.Prefix) string {
	peer := parseAddr(r.RemoteAddr)
	if !peer.IsValid() {
		return r.RemoteAddr
	}
	ip := peer
	if isProxy(peer, trusted) {
		if a := parseAddr(r.Header.Get("CF-Connecting-IP")); a.IsValid() {
			ip = a
		} else {
			parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
			for i := len(parts) - 1; i >= 0; i-- {
				a := parseAddr(parts[i])
				if !a.IsValid() {
					break // garbage: stop believing the chain
				}
				ip = a
				if !isProxy(a, trusted) {
					break
				}
			}
		}
	}
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().Addr().String()
	}
	return ip.String()
}

// parseAddr reads "ip", "ip:port" or "[ip]:port" (empty or invalid gives the zero Addr).
func parseAddr(s string) netip.Addr {
	s = strings.TrimSpace(s)
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	a, err := netip.ParseAddr(strings.Trim(s, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap().WithZone("")
}

func isProxy(a netip.Addr, trusted []netip.Prefix) bool {
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() {
		return true
	}
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
