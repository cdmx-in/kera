package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func TestHashAndVerify(t *testing.T) {
	ctx := context.Background()
	h, err := HashPassword(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") || strings.Contains(h, "correct") {
		t.Fatalf("hash format %q", h)
	}
	if h2, _ := HashPassword(ctx, "correct horse battery"); h2 == h {
		t.Fatal("two hashes of one password are equal: the salt is not random")
	}
	match, rehash, err := VerifyPassword(ctx, "correct horse battery", h)
	if err != nil || !match || rehash {
		t.Fatalf("right password: match=%v rehash=%v err=%v", match, rehash, err)
	}
	for _, wrong := range []string{"correct horse batterY", "", "correct horse battery ", strings.Repeat("a", 2000)} {
		if match, _, err := VerifyPassword(ctx, wrong, h); err != nil || match {
			t.Fatalf("wrong password %q: match=%v err=%v", wrong, match, err)
		}
	}
}

// An empty stored hash is a user who does not exist or has no password: it never
// matches, whatever is typed, and still costs a full hash.
func TestVerifyWithoutAHash(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	match, rehash, err := VerifyPassword(ctx, "anything at all", "")
	if err != nil || match || rehash {
		t.Fatalf("match=%v rehash=%v err=%v", match, rehash, err)
	}
	t.Logf("no-hash check took %v", time.Since(start)) // the first call also makes the dummy hash
	h, _ := HashPassword(ctx, "x")
	fastest := func(enc string) time.Duration { // the best of three: a busy machine slows single runs
		best := time.Hour
		for range 3 {
			start := time.Now()
			_, _, _ = VerifyPassword(ctx, "anything at all", enc)
			best = min(best, time.Since(start))
		}
		return best
	}
	empty, real := fastest(""), fastest(h)
	if empty < real/3 || empty > real*3 {
		t.Fatalf("an empty hash took %v, a real one %v: timing tells them apart", empty, real)
	}
}

// Hashes made with other parameters still verify, and ask to be replaced.
func TestRehash(t *testing.T) {
	ctx := context.Background()
	salt := []byte("0123456789abcdef")
	old := func(mem, passes uint32, lanes uint8) string {
		sum := argon2.IDKey([]byte("old password!"), salt, passes, mem, lanes, 32)
		return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", mem, passes, lanes, b64std.EncodeToString(salt), b64std.EncodeToString(sum))
	}
	for name, enc := range map[string]string{"weaker memory": old(19*1024, 3, 2), "fewer passes": old(argonMemKiB, 2, 2), "one lane": old(argonMemKiB, 3, 1)} {
		match, rehash, err := VerifyPassword(ctx, "old password!", enc)
		if err != nil || !match || !rehash {
			t.Errorf("%s: match=%v rehash=%v err=%v", name, match, rehash, err)
		}
		if match, rehash, _ := VerifyPassword(ctx, "not it at all", enc); match || rehash {
			t.Errorf("%s: a wrong password asked for a rehash or matched", name)
		}
	}
}

func TestVerifyRejectsDamagedHashes(t *testing.T) {
	ctx := context.Background()
	good, _ := HashPassword(ctx, "whatever it is")
	for name, enc := range map[string]string{
		"plain text":        "hunter2",
		"other algorithm":   strings.Replace(good, "argon2id", "argon2i", 1),
		"old version":       strings.Replace(good, "v=19", "v=16", 1),
		"huge memory":       strings.Replace(good, "m=65536", "m=4194304", 1),
		"just over 256 MiB": strings.Replace(good, "m=65536", "m=262145", 1),
		"zero passes":       strings.Replace(good, "t=3", "t=0", 1),
		"too many lanes":    strings.Replace(good, "p=2", "p=200", 1),
		"missing field":     "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ",
		"bad salt":          strings.Replace(good, "$", "$!!", 3),
		"truncated hash":    good[:len(good)-30],
		"junk parameter":    strings.Replace(good, "t=3", "t=x", 1),
		"negative lanes":    strings.Replace(good, "p=2", "p=-2", 1),
		"empty salt field":  "$argon2id$v=19$m=65536,t=3,p=2$$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if match, _, err := VerifyPassword(ctx, "whatever it is", enc); match || err == nil {
			t.Errorf("%s: match=%v err=%v", name, match, err)
		}
	}
}

func TestCheckPassword(t *testing.T) {
	for _, tc := range []struct {
		pw, email string
		want      error
	}{
		{"correct horse", "a@example.com", nil},
		{"7391528640", "", nil}, // exactly the minimum
		{"short", "", ErrPasswordShort},
		{"ninechars", "", ErrPasswordShort},
		{"日本語のパスワードです", "", nil}, // ten characters, more than ten bytes
		{"日本語のパス", "", ErrPasswordShort},
		{"          ", "", ErrPasswordBlank},
		{"Ann@Example.com", "ann@example.com", ErrPasswordEmail},
		{strings.Repeat("a", PasswordMaxBytes-1) + "b", "", nil},
		{strings.Repeat("a", PasswordMaxBytes+1), "", ErrPasswordLong},
		{"alllowercaseletters", "", nil}, // no composition rules
	} {
		if got := CheckPassword(tc.pw, tc.email); !errors.Is(got, tc.want) {
			t.Errorf("CheckPassword(%.20q, %q) = %v, want %v", tc.pw, tc.email, got, tc.want)
		}
	}
}

// Parallel sign-ins queue for a slot, and a cancelled request stops waiting.
func TestHashSlots(t *testing.T) {
	var held []func()
	for range cap(slots) {
		rel, err := acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, rel)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := HashPassword(ctx, "waiting for a slot"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hash with every slot taken: %v", err)
	}
	for _, rel := range held {
		rel()
	}
	var wg sync.WaitGroup
	for range cap(slots) * 3 {
		wg.Go(func() {
			if _, err := HashPassword(context.Background(), "queued"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

// A stored hash that does not parse is answered after the same work as a wrong password, so its
// time does not mark the account (the handler treats both as an ordinary failure).
func TestDamagedHashCostsAsMuchAsAWrongPassword(t *testing.T) {
	ctx := context.Background()
	good, _ := HashPassword(ctx, "whatever it is")
	_, _, _ = VerifyPassword(ctx, "x", "") // makes the throwaway hash
	best := func(enc string) time.Duration {
		d := time.Hour
		for range 3 {
			start := time.Now()
			_, _, _ = VerifyPassword(ctx, "not the password", enc)
			d = min(d, time.Since(start))
		}
		return d
	}
	damaged, wrong := best("$2b$12$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"), best(good)
	if damaged < wrong/3 {
		t.Fatalf("a damaged hash took %v, a wrong password %v: it returns early", damaged, wrong)
	}
	if _, _, err := VerifyPassword(ctx, "x", "$2b$12$abc"); err == nil {
		t.Fatal("a damaged hash gave no error")
	}
	// the limit itself: 256 MiB still parses, one KiB more does not
	for m, ok := range map[string]bool{"262144": true, "262145": false} {
		_, err := parsePHC(strings.Replace(good, "m=65536", "m="+m, 1))
		if (err == nil) != ok {
			t.Errorf("m=%s: err = %v", m, err)
		}
	}
}
