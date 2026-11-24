// Package settings is the runtime configuration store: one JSON value per key
// in the config table, read through a short cache so admin changes (UI or
// `kera apply`) apply within seconds without a restart.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ttl = 10 * time.Second

// Store reads and writes config rows.
type Store struct {
	db    *pgxpool.Pool
	mu    sync.Mutex
	cache map[string]entry
	gen   atomic.Uint64 // bumped by Invalidate, so derived caches can notice
	// defaults replace entries of Defaults for this store (SetDefault): a value the environment supplies.
	defaults map[string]json.RawMessage
}

type entry struct {
	val   json.RawMessage // nil = key absent
	until time.Time
}

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{db: pool, cache: map[string]entry{}, defaults: map[string]json.RawMessage{}}
}

// SetDefault makes v the default of key in this store, in place of the one in Defaults. A stored value
// still wins, so removing it (Reset in the admin console) brings v back. Call it before serving.
func (s *Store) SetDefault(key string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic("bad default for " + key + ": " + err.Error())
	}
	s.mu.Lock()
	s.defaults[key] = b
	s.mu.Unlock()
}

// Default returns the value key has while no row exists: what SetDefault gave it, else its entry in Defaults.
func (s *Store) Default(key string) json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.defaults[key]; ok {
		return v
	}
	return Defaults[key]
}

// Raw returns the stored JSON for key, or the registered default, or nil.
func (s *Store) Raw(ctx context.Context, key string) (json.RawMessage, error) {
	s.mu.Lock()
	if e, ok := s.cache[key]; ok && time.Now().Before(e.until) {
		s.mu.Unlock()
		return s.orDefault(key, e.val), nil
	}
	s.mu.Unlock()
	var v json.RawMessage
	err := s.db.QueryRow(ctx, `SELECT value FROM config WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		v, err = nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", key, err)
	}
	s.mu.Lock()
	s.cache[key] = entry{val: v, until: time.Now().Add(ttl)}
	s.mu.Unlock()
	return s.orDefault(key, v), nil
}

// Get decodes key into dst. A missing key with no default leaves dst untouched.
func (s *Store) Get(ctx context.Context, key string, dst any) error {
	raw, err := s.Raw(ctx, key)
	if err != nil || raw == nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode config %s: %w", key, err)
	}
	return nil
}

// Bool returns a boolean key (false when absent).
func (s *Store) Bool(ctx context.Context, key string) bool {
	var b bool
	_ = s.Get(ctx, key, &b)
	return b
}

// Set upserts key. managed marks rows owned by `kera apply`.
func (s *Store) Set(ctx context.Context, key string, value any, managed bool) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO config (key, value, managed, updated_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, managed = EXCLUDED.managed, updated_at = now()`, key, b, managed)
	if err != nil {
		return fmt.Errorf("write config %s: %w", key, err)
	}
	s.Invalidate(key)
	return nil
}

// PinPasswordSignInOff writes auth.password.enabled = false when it holds no value, and leaves a
// value an admin set alone. It is called whenever an OAuth login provider is saved: with no value, password
// sign-in is on while no login provider exists, so without the pin, deleting the provider later
// would switch the password form on by itself. db is a pool or a transaction.
func PinPasswordSignInOff(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}) error {
	_, err := db.Exec(ctx, `INSERT INTO config (key, value, managed) VALUES ('auth.password.enabled', 'false', false)
		ON CONFLICT (key) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("pin auth.password.enabled: %w", err)
	}
	return nil
}

// Invalidate drops cached keys (all keys when none given).
func (s *Store) Invalidate(keys ...string) {
	s.gen.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(keys) == 0 {
		s.cache = map[string]entry{}
		return
	}
	for _, k := range keys {
		delete(s.cache, k)
	}
}

// Gen changes whenever this process invalidates keys (an admin save). Writes
// from another process (`kera apply`) still show up after the cache ttl.
func (s *Store) Gen() uint64 { return s.gen.Load() }

func (s *Store) orDefault(key string, v json.RawMessage) json.RawMessage {
	if v != nil {
		return v
	}
	return s.Default(key)
}
