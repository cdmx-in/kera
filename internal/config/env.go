// Package config loads process settings from the environment. Runtime
// settings that admins change live in the database config table instead.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

// Env is everything the process needs before it can reach the database.
type Env struct {
	DatabaseURL string // KERA_DATABASE_URL, or the content of the file KERA_DATABASE_URL_FILE names (the URL carries the database password)
	SecretKey   string // KERA_SECRET_KEY, or the content of the file KERA_SECRET_KEY_FILE names; >= 32 bytes; signs sessions, encrypts tokens
	PublicURL   string // KERA_URL, e.g. https://chat.example.com (OAuth redirect URIs derive from it)
	CallbackURL string // KERA_CALLBACK_URL, base URL tool and hook servers call back on; default KERA_URL
	DataDir     string // KERA_DATA_DIR, uploads/, cache/, brand/
	ConfigFile  string // KERA_CONFIG, optional YAML applied by `kera apply`
	Listen      string // KERA_LISTEN, default :8080
	OllamaURL   string // OLLAMA_BASE_URL, default connection when no config declares one
	// ExtractorURL (KERA_EXTRACTOR_URL) is the default of the setting rag.extractor.url, used while the setting
	// has no stored value. The compose file points it at its extract service; empty = no default.
	ExtractorURL string
	// RunnerTokenFile (KERA_RUNNER_TOKEN_FILE) is where Kera keeps the token of the built-in pipeline runner: it
	// makes one when the file has none and registers the runner "builtin" with it. Empty = no built-in runner.
	RunnerTokenFile string

	// AdminEmail and AdminPassword (KERA_ADMIN_EMAIL, KERA_ADMIN_PASSWORD) create the first
	// admin when the user table is empty at startup; they change nothing once any user exists.
	AdminEmail, AdminPassword string
	// TrustedProxies (KERA_TRUSTED_PROXIES, comma-separated addresses or CIDRs) are CDNs that reach
	// Kera over a public address: their X-Forwarded-For is believed (read from the right; CF-Connecting-IP never is). Loopback
	// and private peers are always taken for reverse proxies, but only their X-Forwarded-For counts.
	// Used for sign-in rate limits (api.RealIP).
	TrustedProxies []netip.Prefix
}

// Load reads the environment. requireSecret is false for commands that never
// sign or decrypt anything (version, db status).
func Load(requireSecret bool) (Env, error) {
	e := Env{
		PublicURL:       strings.TrimRight(getenv("KERA_URL", "http://localhost:8080"), "/"),
		CallbackURL:     strings.TrimRight(os.Getenv("KERA_CALLBACK_URL"), "/"),
		DataDir:         getenv("KERA_DATA_DIR", "./data"),
		ConfigFile:      os.Getenv("KERA_CONFIG"),
		Listen:          getenv("KERA_LISTEN", ":8080"),
		OllamaURL:       strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/"),
		ExtractorURL:    strings.TrimRight(strings.TrimSpace(os.Getenv("KERA_EXTRACTOR_URL")), "/"),
		RunnerTokenFile: strings.TrimSpace(os.Getenv("KERA_RUNNER_TOKEN_FILE")),
	}
	if e.CallbackURL == "" {
		e.CallbackURL = e.PublicURL
	}
	var errs []error
	var err error
	if e.DatabaseURL, err = EnvOrFile("KERA_DATABASE_URL"); err != nil {
		errs = append(errs, err)
	} else if e.DatabaseURL == "" {
		errs = append(errs, errors.New("KERA_DATABASE_URL is required"))
	}
	if e.SecretKey, err = secretKey(); err != nil && requireSecret { // commands that never touch the key do not need it to be valid
		errs = append(errs, err)
	}
	e.AdminEmail = strings.TrimSpace(os.Getenv("KERA_ADMIN_EMAIL"))
	e.AdminPassword = os.Getenv("KERA_ADMIN_PASSWORD") // exactly as given: spaces can be part of a password
	if e.TrustedProxies, err = parsePrefixes(os.Getenv("KERA_TRUSTED_PROXIES")); err != nil {
		errs = append(errs, fmt.Errorf("KERA_TRUSTED_PROXIES: %w", err))
	}
	if u, err := url.Parse(e.ExtractorURL); e.ExtractorURL != "" && (err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "") {
		errs = append(errs, errors.New("KERA_EXTRACTOR_URL must be an http or https URL such as http://extract:8000"))
	}
	return e, errors.Join(errs...)
}

// EnvOrFile returns $name, or the content of the file that $name_FILE names (Docker and Kubernetes secrets are
// files); the file wins when both are set. Only trailing CR and LF are removed from the content: the value is
// used byte for byte, and a stray newline would change every key derived from it.
func EnvOrFile(name string) (string, error) {
	if p := strings.TrimSpace(os.Getenv(name + "_FILE")); p != "" {
		return readValueFile(name+"_FILE", p)
	}
	return os.Getenv(name), nil
}

// ReadKeyFile reads a secret key from the file at path (label names the flag or variable in errors) and checks
// its length. Used by `kera secrets rotate`, which needs two keys and so cannot take them from the environment.
func ReadKeyFile(label, path string) (string, error) {
	k, err := readValueFile(label, path)
	if err == nil && len(k) < minKey {
		err = keyTooShort(label+" "+path+": the key", len(k))
	}
	return k, err
}

const minKey = 32

func keyTooShort(what string, n int) error {
	return fmt.Errorf("%s must be at least %d bytes (got %d); generate one with: openssl rand -hex 32", what, minKey, n)
}

// secretKey is KERA_SECRET_KEY_FILE, else KERA_SECRET_KEY, checked for length.
func secretKey() (string, error) {
	if p := strings.TrimSpace(os.Getenv("KERA_SECRET_KEY_FILE")); p != "" {
		return ReadKeyFile("KERA_SECRET_KEY_FILE", p)
	}
	k := os.Getenv("KERA_SECRET_KEY")
	if len(k) < minKey {
		return k, keyTooShort("KERA_SECRET_KEY", len(k))
	}
	return k, nil
}

// readValueFile errors name the variable and the path, never the content.
func readValueFile(name, path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err) // the *PathError already names the path
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("%s: %s is empty", name, path)
	}
	return v, nil
}

// parsePrefixes reads "10.0.0.5, 173.245.48.0/20, 2400:cb00::/32"; a bare address is a /32 or /128.
func parsePrefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(f); err == nil {
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		} else {
			return nil, fmt.Errorf("%q is not an address or CIDR", f)
		}
	}
	return out, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
