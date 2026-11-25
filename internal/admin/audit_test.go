package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

// A settings save is recorded with the names of the settings, never their values (secret ones included);
// a save of guardrail settings alone stands out as guardrails.update. Saved servers name the write-only
// field they replaced, not its value.
func TestAuditSettings(t *testing.T) {
	e := setup(t)
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	key := "tv-" + hex.EncodeToString(b) // a throwaway value, built at run time
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"site.name": "Acme Chat", "web.search.api_key": key}, nil)
	e.ok("POST", "/api/v1/configs/settings", map[string]any{"guardrails.pii": "redact"}, nil)
	e.ok("POST", "/api/v1/configs/hook_servers", map[string]any{"id": "audit-hook", "name": "Audit hook", "url": "https://hooks.example.com/x",
		"key": key, "events": []string{"inlet"}, "scope": "global", "timeout_ms": 1000, "on_error": "skip"}, nil)

	rows, err := e.a.DB.Query(context.Background(), `SELECT action || ' ' || target_id || ' ' || meta::text, row_to_json(a)::text FROM audit_event a ORDER BY at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var short, whole string
		if err := rows.Scan(&short, &whole); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(whole, key) || strings.Contains(whole, "Acme Chat") || strings.Contains(whole, "redact\"") {
			t.Fatalf("an audit row holds a setting value: %s", whole)
		}
		got = append(got, short)
	}
	want := []string{
		`settings.update  {"settings": ["site.name", "web.search.api_key"]}`,
		`guardrails.update  {"settings": ["guardrails.pii"]}`,
		`hook_server.update audit-hook {"changed": ["key"]}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
