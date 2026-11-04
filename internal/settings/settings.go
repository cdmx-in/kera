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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ttl = 10 * time.Second

// Store reads and writes config rows.
type Store struct {
	db    *pgxpool.Pool
	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	val   json.RawMessage // nil = key absent
	until time.Time
}

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Store { return &Store{db: pool, cache: map[string]entry{}} }

// Raw returns the stored JSON for key, or the registered default, or nil.
func (s *Store) Raw(ctx context.Context, key string) (json.RawMessage, error) {
	s.mu.Lock()
	if e, ok := s.cache[key]; ok && time.Now().Before(e.until) {
		s.mu.Unlock()
		return orDefault(key, e.val), nil
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
	return orDefault(key, v), nil
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

// Invalidate drops cached keys (all keys when none given).
func (s *Store) Invalidate(keys ...string) {
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

func orDefault(key string, v json.RawMessage) json.RawMessage {
	if v != nil {
		return v
	}
	return Defaults[key]
}
