// Package config loads process settings from the environment. Runtime
// settings that admins change live in the database config table instead.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Env is everything the process needs before it can reach the database.
type Env struct {
	DatabaseURL string // KERA_DATABASE_URL
	SecretKey   string // KERA_SECRET_KEY, >= 32 bytes; signs sessions, encrypts tokens
	PublicURL   string // KERA_URL, e.g. https://chat.example.com (OAuth redirect URIs derive from it)
	DataDir     string // KERA_DATA_DIR, uploads/, cache/, brand/
	ConfigFile  string // KERA_CONFIG, optional YAML applied by `kera apply`
	Listen      string // KERA_LISTEN, default :8080
	OllamaURL   string // OLLAMA_BASE_URL, default connection when no config declares one
}

// Load reads the environment. requireSecret is false for commands that never
// sign or decrypt anything (version, db status).
func Load(requireSecret bool) (Env, error) {
	e := Env{
		DatabaseURL: os.Getenv("KERA_DATABASE_URL"),
		SecretKey:   os.Getenv("KERA_SECRET_KEY"),
		PublicURL:   strings.TrimRight(getenv("KERA_URL", "http://localhost:8080"), "/"),
		DataDir:     getenv("KERA_DATA_DIR", "./data"),
		ConfigFile:  os.Getenv("KERA_CONFIG"),
		Listen:      getenv("KERA_LISTEN", ":8080"),
		OllamaURL:   strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/"),
	}
	var errs []error
	if e.DatabaseURL == "" {
		errs = append(errs, errors.New("KERA_DATABASE_URL is required"))
	}
	if requireSecret && len(e.SecretKey) < 32 {
		errs = append(errs, fmt.Errorf("KERA_SECRET_KEY must be at least 32 bytes (got %d); generate one with: openssl rand -hex 32", len(e.SecretKey)))
	}
	return e, errors.Join(errs...)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
