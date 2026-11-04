package importowui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// ExportPresets writes the system prompt of every source model row that has
// one to dir/<id>.md (model rows themselves are not imported; presets are
// re-declared in Kera's config file). It returns the number written.
func ExportPresets(ctx context.Context, src *pgxpool.Pool, dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, fmt.Errorf("create preset dir: %w", err)
	}
	rows, err := src.Query(ctx, `SELECT id, coalesce(params, '') FROM model ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("list models: %w", err)
	}
	n := 0
	var id, params string
	_, err = pgx.ForEachRow(rows, []any{&id, &params}, func() error {
		var p struct {
			System any `json:"system"`
		}
		if json.Unmarshal([]byte(params), &p) != nil {
			return nil
		}
		s, _ := p.System.(string)
		if strings.TrimSpace(s) == "" {
			return nil
		}
		if err := os.WriteFile(filepath.Join(dir, safeName(id)+".md"), []byte(s), 0o640); err != nil {
			return fmt.Errorf("write preset %s: %w", id, err)
		}
		n++
		return nil
	})
	return n, err
}

// configSQL selects the source settings worth folding into Kera's config file.
const configSQL = `SELECT key, value::text FROM config WHERE key LIKE 'rag.%' OR key LIKE 'web.%' OR key LIKE 'task.%'
	OR key LIKE 'ui.%' OR key = 'user.permissions' OR key LIKE 'user.permissions.%'
	OR key = 'tool_server.connections' OR key LIKE 'tool_server.connections.%' ORDER BY key`

// ReportConfig prints the source's retrieval, web, task, UI, permission and
// tool-server settings as "key = json" lines with secrets replaced.
func ReportConfig(ctx context.Context, src *pgxpool.Pool, w io.Writer) error {
	rows, err := src.Query(ctx, configSQL)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var key, raw string
	_, err = pgx.ForEachRow(rows, []any{&key, &raw}, func() error {
		var v any
		if json.Unmarshal([]byte(raw), &v) != nil {
			v = "[unreadable]"
		}
		b, _ := json.Marshal(redact(key, v))
		fmt.Fprintf(w, "%s = %s\n", key, b)
		return nil
	})
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	return nil
}

const redacted = "[redacted]"

// redact replaces values held under secret-looking names, every header value,
// sk-/Bearer strings and URL passwords. Empty values stay visible so "unset"
// is distinguishable from "set".
func redact(name string, v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if strings.EqualFold(k, "headers") {
				out[k] = redactAll(val)
				continue
			}
			out[k] = redact(k, val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redact(name, e)
		}
		return out
	case string:
		if x == "" {
			return x
		}
		// gAAAA starts a Fernet token: an encrypted secret is still a secret.
		if secretName(name) || strings.HasPrefix(x, "sk-") || strings.HasPrefix(x, "gAAAA") ||
			strings.HasPrefix(strings.ToLower(x), "bearer ") {
			return redacted
		}
		if u, err := url.Parse(x); err == nil && u.User != nil {
			if _, has := u.User.Password(); has {
				return u.Redacted()
			}
		}
	}
	return v
}

func redactAll(v any) any {
	if m, ok := v.(map[string]any); ok {
		out := make(map[string]any, len(m))
		for k := range m {
			out[k] = redacted
		}
		return out
	}
	if v == nil || v == "" {
		return v
	}
	return redacted
}

// secretName reports whether a (dotted, snake or camel) setting name looks
// like it holds a credential.
func secretName(name string) bool {
	name = strings.ToLower(name)
	if strings.Contains(name, "client_info") { // an OAuth client registration, secret included
		return true
	}
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == '_' || r == '-' }) {
		switch seg {
		case "key", "keys", "token", "tokens", "secret", "password", "passwd", "authorization", "cookie",
			"credential", "credentials", "sk", "sid", "bearer":
			return true
		}
		for _, suf := range []string{"apikey", "token", "secret", "password"} {
			if strings.HasSuffix(seg, suf) {
				return true
			}
		}
	}
	return false
}
