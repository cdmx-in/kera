package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password rules. Length only: no composition rules ("one digit, one symbol"),
// which make people pick worse passwords.
const (
	PasswordMinChars = 10   // characters, not bytes
	PasswordMaxBytes = 1024 // checked before hashing, so a huge body cannot cost CPU
)

var (
	ErrPasswordShort = fmt.Errorf("Use at least %d characters.", PasswordMinChars)
	ErrPasswordLong  = fmt.Errorf("Use at most %d bytes.", PasswordMaxBytes)
	ErrPasswordEmail = errors.New("Choose a password that is not your email address.")
	ErrPasswordBlank = errors.New("The password cannot be only spaces.")
)

// CheckPassword applies the rules above. email may be empty.
func CheckPassword(password, email string) error {
	switch {
	case len(password) > PasswordMaxBytes:
		return ErrPasswordLong
	case utf8.RuneCountInString(password) < PasswordMinChars:
		return ErrPasswordShort
	case strings.TrimSpace(password) == "":
		return ErrPasswordBlank
	case email != "" && strings.EqualFold(password, email):
		return ErrPasswordEmail
	}
	return nil
}

// argon2id cost for new hashes: 64 MiB, 3 passes, 2 lanes (RFC 9106's
// memory-constrained profile with fewer lanes). OWASP's floor is 19 MiB with 2
// passes; this is 3.4 times the memory and takes about 80 ms on one core of a
// current CPU, a few times that on a small VPS. The parameters are stored in
// each hash (PHC string), so raising them later needs no migration: hashes made
// with older numbers still verify and are replaced at the user's next sign-in.
const (
	argonMemKiB  = 64 * 1024
	argonPasses  = 3
	argonLanes   = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Bounds for a stored hash's parameters, so a damaged or hostile row cannot
// ask for gigabytes or minutes.
const (
	maxMemKiB = 1 << 20
	maxPasses = 20
	maxLanes  = 32
)

// slots bounds how many hashes run at once: each holds 64 MiB, so a burst of
// sign-ins must queue instead of exhausting memory.
// ponytail: waiters are bounded only by the HTTP server and the per-address
// limit; add a queue cap that answers 503 if that ever matters.
var slots = make(chan struct{}, max(1, min(4, runtime.NumCPU())))

func acquire(ctx context.Context) (release func(), err error) {
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var b64std = base64.RawStdEncoding

// HashPassword returns a PHC string ($argon2id$v=19$m=...,t=...,p=...$salt$hash).
func HashPassword(ctx context.Context, password string) (string, error) {
	release, err := acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password salt: %w", err)
	}
	sum := argon2.IDKey([]byte(password), salt, argonPasses, argonMemKiB, argonLanes, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemKiB, argonPasses, argonLanes,
		b64std.EncodeToString(salt), b64std.EncodeToString(sum)), nil
}

type phc struct {
	mem, passes uint32
	lanes       uint8
	salt, sum   []byte
}

func parsePHC(s string) (phc, error) {
	var p phc
	f := strings.Split(s, "$") // "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(f) != 6 || f[0] != "" || f[1] != "argon2id" || f[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, errors.New("not an argon2id hash")
	}
	var m, t, l uint64
	for _, kv := range strings.Split(f[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if !ok || err != nil {
			return p, errors.New("bad argon2 parameters")
		}
		switch k {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			l = n
		}
	}
	if m == 0 || m > maxMemKiB || t == 0 || t > maxPasses || l == 0 || l > maxLanes {
		return p, errors.New("argon2 parameters out of range")
	}
	var err error
	if p.salt, err = b64std.DecodeString(f[4]); err != nil || len(p.salt) < 8 {
		return p, errors.New("bad argon2 salt")
	}
	if p.sum, err = b64std.DecodeString(f[5]); err != nil || len(p.sum) < 16 {
		return p, errors.New("bad argon2 hash")
	}
	p.mem, p.passes, p.lanes = uint32(m), uint32(t), uint8(l)
	return p, nil
}

// VerifyPassword reports whether password matches the stored hash, and whether
// that hash was made with other parameters than today's (rehash it). The
// comparison takes the same time whether or not the bytes match.
//
// An empty hash (no such user, or a user without a password) is checked against
// a throwaway hash and never matches, so those cases cost the same time as a
// wrong password and cannot be told apart by timing.
func VerifyPassword(ctx context.Context, password, encoded string) (match, rehash bool, err error) {
	known := encoded != ""
	if !known {
		if encoded, err = dummyHash(); err != nil {
			return false, false, err
		}
	}
	p, err := parsePHC(encoded)
	if err != nil {
		return false, false, err
	}
	release, err := acquire(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()
	sum := argon2.IDKey([]byte(password), p.salt, p.passes, p.mem, p.lanes, uint32(len(p.sum)))
	match = subtle.ConstantTimeCompare(sum, p.sum) == 1 && known
	stale := p.mem != argonMemKiB || p.passes != argonPasses || p.lanes != argonLanes || len(p.sum) != argonKeyLen || len(p.salt) != argonSaltLen
	return match, match && stale, nil
}

var (
	dummyOnce sync.Once
	dummy     string
	dummyErr  error
)

// dummyHash is a hash of a random password, made once with the current cost.
func dummyHash() (string, error) {
	dummyOnce.Do(func() {
		pw := make([]byte, 24)
		_, _ = rand.Read(pw)
		dummy, dummyErr = HashPassword(context.Background(), string(pw))
	})
	return dummy, dummyErr
}
