package auth

import (
	"log/slog"
	"sync"
	"time"
)

// Key is one thing sign-in attempts are counted against: an email, a client
// address. Free is how many attempts it gets in a row before the first lock.
type Key struct {
	Kind, Value string
	Free        int
}

type tally struct {
	n           int
	until, last time.Time
}

// Attempts counts sign-in attempts per key and, from the Free-th one on, locks
// the key for 30 s, then 1 min, 2 min, 4 min ... up to 15 min, one doubling per
// further attempt. A key that stays quiet for an hour starts over. It is kept
// in memory, so a restart forgets it.
//
// An attempt is counted when it begins, not when it fails, so a burst of
// parallel guesses runs into the lock instead of all passing it before the first
// one is judged.
//
// ponytail: one process, no persistence (a restart clears every lock, which an
// attacker cannot trigger); the map is capped at maxTallies and evicts unlocked
// keys first. Move the counters to the database if Kera ever runs as several
// replicas.
type Attempts struct {
	mu  sync.Mutex
	m   map[string]*tally
	now func() time.Time
}

const (
	lockBase    = 30 * time.Second
	lockMax     = 15 * time.Minute
	forgetAfter = time.Hour
	maxTallies  = 10_000
)

// NewAttempts returns an empty counter.
func NewAttempts() *Attempts { return &Attempts{m: map[string]*tally{}, now: time.Now} }

func (k Key) id() string { return k.Kind + "\x00" + k.Value }

// Begin counts one attempt against every key. When any key is locked it counts
// nothing and returns how long to wait. Locks that this attempt starts are
// logged; they apply to the next attempt, not to this one.
func (a *Attempts) Begin(keys ...Key) (wait time.Duration, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for _, k := range keys {
		if t := a.m[k.id()]; t != nil && t.until.After(now) {
			wait = max(wait, t.until.Sub(now))
		}
	}
	if wait > 0 {
		return wait, false
	}
	for _, k := range keys {
		t := a.m[k.id()]
		if t == nil || now.Sub(t.last) > forgetAfter {
			a.makeRoom(now)
			t = &tally{}
			a.m[k.id()] = t
		}
		t.n++
		t.last = now
		if t.n >= k.Free {
			d := lockBase << min(t.n-k.Free, 10)
			t.until = now.Add(min(d, lockMax))
			slog.Warn("sign-in locked", "kind", k.Kind, "key", k.Value, "attempts", t.n, "seconds", int(min(d, lockMax).Seconds()))
		}
	}
	return 0, true
}

// Reset forgets a key after a good sign-in.
func (a *Attempts) Reset(k Key) {
	a.mu.Lock()
	delete(a.m, k.id())
	a.mu.Unlock()
}

// Refund gives back the attempt Begin counted, for a key that a good sign-in
// must not clear (a shared address: one user's success is no sign the others
// are honest) but should not charge either.
func (a *Attempts) Refund(k Key) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t := a.m[k.id()]; t != nil && t.n > 0 {
		t.n--
		if t.n < k.Free {
			t.until = time.Time{}
		}
	}
}

// makeRoom keeps the map under maxTallies. A full map is cut by a tenth in one
// sweep (expired entries, then unlocked ones in map order, then any), so the O(n)
// scan runs once per few hundred new keys, not once per request.
func (a *Attempts) makeRoom(now time.Time) {
	if len(a.m) < maxTallies {
		return
	}
	target := maxTallies - maxTallies/10
	for id, t := range a.m {
		if now.Sub(t.last) > forgetAfter && !t.until.After(now) {
			delete(a.m, id)
		}
	}
	for id, t := range a.m {
		if len(a.m) <= target {
			return
		}
		if !t.until.After(now) {
			delete(a.m, id)
		}
	}
	for id := range a.m { // everything is locked: drop some rather than grow
		if len(a.m) <= target {
			return
		}
		delete(a.m, id)
	}
}
