package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// health asks the server listening on listen (KERA_LISTEN, e.g. ":8080")
// whether it is ready: database reachable and migrations applied. It is the
// container healthcheck, because the distroless image has no curl.
func health(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("KERA_LISTEN %q: %w", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/ready")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("not ready: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
