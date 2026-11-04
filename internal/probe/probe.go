// Package probe is the HTTP client behind the admin console's "Test" buttons:
// it calls admin-typed URLs (model servers, tool servers, hooks, OAuth
// providers) with a short timeout, a response size cap, and no access to
// link-local or cloud metadata addresses. Private and loopback addresses stay
// reachable: those are where an admin's own services live.
package probe

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

const (
	// Timeout bounds one test end to end.
	Timeout = 10 * time.Second
	// MaxBody caps every response body a test reads.
	MaxBody = 8 << 20
)

// ErrBlocked means the URL resolves to a link-local or metadata address.
var ErrBlocked = errors.New("address not allowed: link-local or cloud metadata")

var awsMetadataV6 = netip.MustParseAddr("fd00:ec2::254")

// Blocked reports link-local (169.254.0.0/16, fe80::/10) and metadata addresses.
func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip == awsMetadataV6
}

// Client returns a client for one test. Every connection (redirects included)
// is checked after DNS resolution.
func Client() *http.Client {
	d := &net.Dialer{Timeout: Timeout, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || Blocked(ip) {
			return ErrBlocked
		}
		return nil
	}}
	tr := &http.Transport{Proxy: nil, DialContext: d.DialContext, TLSHandshakeTimeout: Timeout,
		ResponseHeaderTimeout: Timeout, DisableKeepAlives: true}
	return &http.Client{Timeout: Timeout, Transport: capped{tr},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		}}
}

// capped limits every response body to MaxBody.
type capped struct{ http.RoundTripper }

func (c capped) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.RoundTripper.RoundTrip(r)
	if err == nil {
		resp.Body = &limited{rc: resp.Body, left: MaxBody}
	}
	return resp, err
}

type limited struct {
	rc   io.ReadCloser
	left int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, fmt.Errorf("response larger than %d MiB", MaxBody>>20)
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.rc.Read(p)
	l.left -= int64(n)
	return n, err
}

func (l *limited) Close() error { return l.rc.Close() }

// Check is one named step of a test.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Result is what every test endpoint answers (always HTTP 200; OK says
// whether the test passed).
type Result struct {
	OK     bool     `json:"ok"`
	MS     int64    `json:"ms"`
	Error  string   `json:"error,omitempty"`
	Note   string   `json:"note,omitempty"` // a warning on a passed test
	Via    string   `json:"via,omitempty"`  // the request that answered, e.g. "GET /health"
	Status int      `json:"status,omitempty"`
	Models []string `json:"models,omitempty"`
	Tools  []string `json:"tools,omitempty"`
	Checks []Check  `json:"checks,omitempty"`
}

// Since sets MS from start.
func (r *Result) Since(start time.Time) *Result {
	r.MS = time.Since(start).Milliseconds()
	return r
}

// Fail records err with secrets masked (a server may echo a credential back).
func (r *Result) Fail(err error, secrets ...string) *Result {
	r.OK, r.Error = false, Redact(err.Error(), secrets...)
	return r
}

// Redact replaces every non-empty secret in s.
func Redact(s string, secrets ...string) string {
	for _, x := range secrets {
		if x != "" {
			s = strings.ReplaceAll(s, x, "•••")
		}
	}
	return s
}
