package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RealIP is the address of whoever is on the other end, for rate limits (the
// request log keeps its own looser clientIP). Which headers count depends on
// who connected:
//
//   - A peer on the open internet: neither header. Its own address is the answer.
//   - A reverse proxy on the same host or network (loopback, private or
//     link-local address, as in the compose stack): X-Forwarded-For only, read
//     from the right and skipping proxies, because a proxy appends the address it
//     saw and whatever the client wrote sits to the left. CF-Connecting-IP is not
//     believed here: Caddy, nginx and Traefik pass a client-sent copy through, and
//     a client could pick a new address per try.
//   - A peer listed in KERA_TRUSTED_PROXIES (a CDN reaching Kera over a public
//     address): CF-Connecting-IP, which that CDN sets itself, then X-Forwarded-For
//     as above.
//
// A client on the same private network as Kera can still forge X-Forwarded-For,
// which this accepts.
//
// IPv6 addresses are cut to their /64, the size of one customer's block.
func RealIP(r *http.Request, trusted []netip.Prefix) string {
	peer := parseAddr(r.RemoteAddr)
	if !peer.IsValid() {
		return r.RemoteAddr
	}
	ip := peer
	switch {
	case inList(peer, trusted):
		if cf := parseAddr(r.Header.Get("CF-Connecting-IP")); cf.IsValid() {
			ip = cf
			break
		}
		ip = fromForwardedFor(r, peer, trusted)
	case isPrivate(peer):
		ip = fromForwardedFor(r, peer, trusted)
	}
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().Addr().String()
	}
	return ip.String()
}

// fromForwardedFor walks X-Forwarded-For from the right, past the proxies, to the
// first address that is not one; fallback when the header is missing or damaged.
func fromForwardedFor(r *http.Request, fallback netip.Addr, trusted []netip.Prefix) netip.Addr {
	ip := fallback
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a := parseAddr(parts[i])
		if !a.IsValid() {
			return fallback // garbage: stop believing the chain
		}
		ip = a
		if !isPrivate(a) && !inList(a, trusted) {
			break
		}
	}
	return ip
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

func isPrivate(a netip.Addr) bool { return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() }

func inList(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
