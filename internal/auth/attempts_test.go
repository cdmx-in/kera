package auth

import (
	"fmt"
	"log/slog"
	"testing"
	"time"
)

func clock() (*Attempts, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	a := NewAttempts()
	a.now = func() time.Time { return now }
	return a, &now
}

func TestAttemptsLockAndBackoff(t *testing.T) {
	a, now := clock()
	k := Key{"email", "ann@example.com", 5}
	for i := 1; i <= 5; i++ {
		if wait, ok := a.Begin(k); !ok || wait != 0 {
			t.Fatalf("attempt %d refused (%v)", i, wait)
		}
	}
	// The fifth attempt was allowed and started the first lock.
	wait, ok := a.Begin(k)
	if ok || wait != 30*time.Second {
		t.Fatalf("after 5 attempts: ok=%v wait=%v, want a 30 s lock", ok, wait)
	}
	// A refused attempt counts for nothing: hammering does not lengthen the lock.
	*now = now.Add(10 * time.Second)
	if wait, _ = a.Begin(k); wait != 20*time.Second {
		t.Fatalf("10 s later wait=%v, want 20 s", wait)
	}
	// Each attempt after a lock doubles the next one, up to 15 minutes.
	want := []time.Duration{60 * time.Second, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range want {
		*now = now.Add(time.Hour / 2) // longer than any lock, shorter than forgetAfter
		if _, ok := a.Begin(k); !ok {
			t.Fatalf("round %d: still locked after waiting", i)
		}
		if wait, ok = a.Begin(k); ok || wait != w {
			t.Fatalf("round %d: wait=%v ok=%v, want %v", i, wait, ok, w)
		}
	}
}

func TestAttemptsResetAndForget(t *testing.T) {
	a, now := clock()
	k := Key{"email", "ann@example.com", 3}
	for range 3 {
		a.Begin(k)
	}
	if _, ok := a.Begin(k); ok {
		t.Fatal("not locked")
	}
	a.Reset(k)
	if _, ok := a.Begin(k); !ok {
		t.Fatal("still locked after a good sign-in")
	}
	// Quiet for an hour: the count starts over.
	for range 2 {
		a.Begin(k)
	}
	*now = now.Add(2 * time.Hour)
	for i := range 3 {
		if _, ok := a.Begin(k); !ok && i < 2 {
			t.Fatalf("attempt %d after a quiet hour refused", i+1)
		}
	}
}

func TestAttemptsRefund(t *testing.T) {
	a, _ := clock()
	ip := Key{"ip", "203.0.113.9", 3}
	// Many good sign-ins from one address (an office) never add up to a lock.
	for range 50 {
		if _, ok := a.Begin(ip); !ok {
			t.Fatal("a shared address was locked by good sign-ins")
		}
		a.Refund(ip)
	}
	// Failures still add up, even with good sign-ins in between.
	for range 2 {
		a.Begin(ip)
	}
	a.Begin(ip)
	a.Refund(ip)
	if _, ok := a.Begin(ip); !ok {
		t.Fatal("locked one attempt early")
	}
	if _, ok := a.Begin(ip); ok {
		t.Fatal("failures between good sign-ins did not add up")
	}
}

// Any locked key refuses the attempt and nothing else is counted.
func TestAttemptsSeveralKeys(t *testing.T) {
	a, _ := clock()
	email := Key{"email", "ann@example.com", 2}
	ip := Key{"ip", "203.0.113.9", 100}
	a.Begin(email, ip)
	a.Begin(email, ip) // locks the email
	if wait, ok := a.Begin(email, ip); ok || wait == 0 {
		t.Fatal("locked email did not refuse")
	}
	// A refused attempt did not touch the address: another email from it is fine.
	if _, ok := a.Begin(Key{"email", "bob@example.com", 2}, ip); !ok {
		t.Fatal("the address was charged for a refused attempt")
	}
	if got := a.m[ip.id()].n; got != 3 {
		t.Fatalf("address counted %d attempts, want 3", got)
	}
}

func TestAttemptsStayBounded(t *testing.T) {
	was := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler)) // every key below is locked at once, and each lock logs a line
	t.Cleanup(func() { slog.SetDefault(was) })
	a, _ := clock()
	for i := range maxTallies * 3 {
		a.Begin(Key{"email", fmt.Sprintf("user%d@example.com", i), 5})
	}
	if len(a.m) > maxTallies {
		t.Fatalf("%d entries, want at most %d", len(a.m), maxTallies)
	}
	// Even when everything is locked the map does not grow.
	b, _ := clock()
	for i := range maxTallies + 500 {
		b.Begin(Key{"email", fmt.Sprintf("user%d@example.com", i), 1})
	}
	if len(b.m) > maxTallies {
		t.Fatalf("all-locked map grew to %d", len(b.m))
	}
}
