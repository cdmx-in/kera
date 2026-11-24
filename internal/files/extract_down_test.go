package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What a person reads when the extractor cannot be reached: the cause, the fix, and no raw dial error or address.
func TestExtractorCause(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "http://extract:8000/extract", Err: &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}
	for _, c := range []struct {
		err  error
		want string
	}{
		{refused, "connection refused"},
		{&url.Error{Op: "Post", Err: &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "extract", IsNotFound: true}}}, "host name does not resolve"},
		{&url.Error{Op: "Post", Err: io.EOF}, "closed the connection"},
		{&url.Error{Op: "Post", Err: &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}}, "closed the connection"},
		{&url.Error{Op: "Post", Err: &net.OpError{Op: "write", Err: os.NewSyscallError("write", syscall.EPIPE)}}, "closed the connection"},
		{&url.Error{Op: "Post", Err: &net.OpError{Op: "write", Err: net.ErrClosed}}, "closed the connection"},
		{&url.Error{Op: "Post", Err: context.DeadlineExceeded}, "no answer in time"},
		{&url.Error{Op: "Post", URL: "http://extract:8000/extract", Err: errors.New("tls: bad certificate")}, "tls: bad certificate"},
	} {
		got := ExtractorCause(c.err)
		if !strings.Contains(got, c.want) || strings.Contains(got, "extract:8000") {
			t.Errorf("%v: cause %q, want %q", c.err, got, c.want)
		}
	}
	msg := extractorDown(refused).Error()
	for _, want := range []string{"document extractor not reachable (connection refused", "docker compose up -d extract", "Settings > Retrieval > Extractor URL", "retry this file"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "dial tcp") || strings.Contains(msg, "extract:8000") {
		t.Errorf("message shows the raw error or the address: %q", msg)
	}
}

// A PDF whose extractor is down, hangs up, hangs or fails ends as failed with a message that says why.
func TestExtractorDown(t *testing.T) {
	e := setup(t)
	set := func(url string) {
		t.Helper()
		if err := e.a.Settings.Set(context.Background(), "rag.extractor.url", url, false); err != nil {
			t.Fatal(err)
		}
	}
	failed := func(name string) string {
		t.Helper()
		got := e.wait(e.upload("alice", name, []byte("%PDF-1.4\n"), "").ID)
		if got.Status != Failed {
			t.Fatalf("%s: %+v", name, got)
		}
		return got.Error
	}

	// nothing listens on the port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String()
	l.Close()
	set(dead)
	if msg := failed("down.pdf"); !strings.Contains(msg, "not reachable (connection refused") || strings.Contains(msg, dead) || strings.Contains(msg, "dial tcp") {
		t.Fatalf("closed port: %q", msg)
	}

	// it takes the file and drops the connection (killed for memory, say)
	hangup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _, _ := w.(http.Hijacker).Hijack()
		c.Close()
	}))
	defer hangup.Close()
	set(hangup.URL)
	if msg := failed("hangup.pdf"); !strings.Contains(msg, "closed the connection") || !strings.Contains(msg, "docker compose up -d extract") {
		t.Fatalf("hang up: %q", msg)
	}

	// it never answers: the client gives up
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	e.svc.client.Timeout = 300 * time.Millisecond
	set(slow.URL)
	if msg := failed("slow.pdf"); !strings.Contains(msg, "no answer in time") {
		t.Fatalf("timeout: %q", msg)
	}

	// a service that answers, but with a server error
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) }))
	defer broken.Close()
	set(broken.URL)
	if msg := failed("broken.pdf"); !strings.Contains(msg, "HTTP 500") || !strings.Contains(msg, "docker compose logs extract") {
		t.Fatalf("500: %q", msg)
	}
}

// The environment's default (KERA_EXTRACTOR_URL) serves uploads until a value is stored; a stored empty value turns it off.
func TestExtractorDefaultFromEnvironment(t *testing.T) {
	e := setup(t)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"pages":[{"n":1,"text":"from the stub"}],"meta":{"pages":1}}`)
	}))
	defer stub.Close()
	ctx := context.Background()
	e.a.Settings.SetDefault("rag.extractor.url", stub.URL)
	if got := e.wait(e.upload("alice", "a.pdf", []byte("PDF"), "").ID); got.Status != Completed {
		t.Fatalf("with the default: %+v", got)
	}
	if err := e.a.Settings.Set(ctx, "rag.extractor.url", "", false); err != nil {
		t.Fatal(err)
	}
	if got := e.wait(e.upload("alice", "b.pdf", []byte("PDF"), "").ID); got.Status != Failed || !strings.HasPrefix(got.Error, "no document extractor configured") {
		t.Fatalf("with a stored empty value: %+v", got)
	}
}
