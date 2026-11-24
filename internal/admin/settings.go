package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/settings"
)

// settingOut is one known key. Secret values are never returned: Set says
// whether one is stored.
type settingOut struct {
	Key     string          `json:"key"`
	Value   json.RawMessage `json:"value,omitempty"`
	Default json.RawMessage `json:"default"`
	Managed bool            `json:"managed"`
	Secret  bool            `json:"secret,omitempty"`
	Set     bool            `json:"set"`
}

// knownKeys is settings.Defaults plus every key apply writes; the value is
// true for secret keys.
func knownKeys() map[string]bool {
	keys := apply.SettingKeys()
	for k := range settings.Defaults {
		if _, ok := keys[k]; !ok {
			keys[k] = false
		}
	}
	return keys
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := h.a.DB.Query(r.Context(), `SELECT key, value, managed FROM config`)
	if err != nil {
		api.Fail(w, r, fmt.Errorf("read config: %w", err))
		return
	}
	defer rows.Close()
	type stored struct {
		value   json.RawMessage
		managed bool
	}
	have := map[string]stored{}
	for rows.Next() {
		var k string
		var s stored
		if err := rows.Scan(&k, &s.value, &s.managed); err != nil {
			api.Fail(w, r, err)
			return
		}
		have[k] = s
	}
	if err := rows.Err(); err != nil {
		api.Fail(w, r, err)
		return
	}
	known := knownKeys()
	out := make([]settingOut, 0, len(known))
	for _, k := range slices.Sorted(maps.Keys(known)) {
		s, ok := have[k]
		o := settingOut{Key: k, Default: h.a.Settings.Default(k), Managed: s.managed, Secret: known[k], Set: ok}
		if o.Default == nil {
			o.Default = json.RawMessage(`null`)
		}
		if !o.Secret {
			o.Value = o.Default
			if ok {
				o.Value = s.value
			}
		}
		out = append(out, o)
	}
	api.JSON(w, http.StatusOK, out)
}

// setSettings writes {key: value, ...} in one transaction after validating
// every key. null resets a key to its default; for secret keys "" does too.
// The managed flag is kept, so a key `kera apply` owns still says so (and
// the next apply overwrites the edit).
func (h *handler) setSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := api.Decode(r, &body); err != nil {
		api.Fail(w, r, err)
		return
	}
	known := knownKeys()
	writes := map[string]json.RawMessage{} // nil value = delete
	for k, v := range body {
		secret, ok := known[k]
		if !ok {
			api.Fail(w, r, api.Errf(http.StatusBadRequest, "unknown_key", "unknown setting "+k))
			return
		}
		val, err := h.settingValue(k, v, secret)
		if err != nil {
			api.Fail(w, r, err)
			return
		}
		writes[k] = val
	}
	ctx := r.Context()
	tx, err := h.a.DB.Begin(ctx)
	if err != nil {
		api.Fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	for k, v := range writes {
		if v == nil {
			_, err = tx.Exec(ctx, `DELETE FROM config WHERE key = $1`, k)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO config (key, value, managed, updated_at) VALUES ($1, $2, false, now())
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, k, v)
		}
		if err != nil {
			api.Fail(w, r, fmt.Errorf("write config %s: %w", k, err))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		api.Fail(w, r, err)
		return
	}
	h.a.Settings.Invalidate(slices.Collect(maps.Keys(writes))...)
	h.getSettings(w, r)
}

// settingValue validates v for key and returns what to store (nil = delete).
func (h *handler) settingValue(key string, v json.RawMessage, secret bool) (json.RawMessage, error) {
	got := jsonType(v)
	if got == "null" {
		return nil, nil
	}
	if secret {
		var s string
		if json.Unmarshal(v, &s) != nil {
			return nil, api.Errf(http.StatusBadRequest, "bad_type", key+" must be a string")
		}
		if s == "" {
			return nil, nil
		}
		return apply.SealSecret(h.a.Keys, s)
	}
	def, ok := settings.Defaults[key]
	if !ok {
		return v, nil // a key apply writes without a built-in default: any JSON
	}
	if want := jsonType(def); want == "null" { // a switch with no value until someone sets it (auth.password.enabled)
		if got != "boolean" {
			return nil, api.Errf(http.StatusBadRequest, "bad_type", fmt.Sprintf("%s must be true or false, got %s", key, got))
		}
	} else if got != want {
		return nil, api.Errf(http.StatusBadRequest, "bad_type", fmt.Sprintf("%s must be a %s, got %s", key, want, got))
	}
	if key == "ui.brand" {
		if err := apply.ValidateBrand(v); err != nil {
			return nil, api.Errf(http.StatusBadRequest, "invalid_brand", err.Error())
		}
	}
	if got == "number" && apply.IntegerSettings()[key] && !isInt(v) {
		return nil, api.Errf(http.StatusBadRequest, "bad_type", key+" must be a whole number")
	}
	return v, nil
}

// jsonType names the JSON type of v: object, array, string, number, boolean,
// null, or invalid.
func jsonType(v json.RawMessage) string {
	if !json.Valid(v) {
		return "invalid"
	}
	switch b := bytes.TrimSpace(v); {
	case len(b) == 0:
		return "invalid"
	case b[0] == '{':
		return "object"
	case b[0] == '[':
		return "array"
	case b[0] == '"':
		return "string"
	case b[0] == 't' || b[0] == 'f':
		return "boolean"
	case b[0] == 'n':
		return "null"
	default:
		return "number"
	}
}

func isInt(v json.RawMessage) bool {
	var n json.Number
	if json.Unmarshal(v, &n) != nil {
		return false
	}
	_, err := n.Int64()
	return err == nil && !strings.ContainsAny(string(n), ".eE")
}
